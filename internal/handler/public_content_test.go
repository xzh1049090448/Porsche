package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func TestPublicIfNoneMatchUsesRFCWeakComparison(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		match bool
	}{
		{`"abc"`, true}, {`W/"abc"`, true}, {` "other" , W/"abc" `, true}, {`*`, true},
		{`"non,current", W/"abc"`, true}, {`"one,two", "three,four", W/"abc"`, true},
		{`W/abc, "abc"`, true}, {`, , "abc",`, true}, {`"abc"suffix, W/"abc"`, true},
		{`"other"`, false}, {``, false}, {`W/abc`, false}, {`"abc`, false},
		{"\x00\n\r", false}, {`*, "other"`, false}, {`*, "abc"`, true},
		{`"unterminated, W/"abc"`, false}, {`w/"abc"`, false}, {`"a\\b"`, false},
	} {
		if got := publicIfNoneMatch(tc.raw, `"abc"`); got != tc.match {
			t.Errorf("%q match=%v want=%v", tc.raw, got, tc.match)
		}
	}
	if !publicIfNoneMatch(`"a,b"`, `"a,b"`) {
		t.Error("comma-bearing opaque tag did not match")
	}
	if !publicIfNoneMatch(`"a\b"`, `"a\b"`) {
		t.Error("backslash must be an ordinary etagc byte")
	}
	if publicIfNoneMatch("\"a\"b\"", `"a"`) {
		t.Error("embedded DQUOTE accepted")
	}
}

type publicReadStub struct {
	projection *service.PublicCatalogProjection
	err        error
}

func (s publicReadStub) Projection(context.Context) (*service.PublicCatalogProjection, error) {
	return s.projection, s.err
}

func testPublicProjection() *service.PublicCatalogProjection {
	return &service.PublicCatalogProjection{
		Content: service.PublicContentDraft{Home: "home", About: "about", Terms: "terms", Privacy: "privacy"}, ContentReleaseVersion: 7, PriceReleaseVersion: 9,
		PriceVisibility: models.PublicPriceVisibilityAuthenticatedOnly, ETag: `"abc"`, Items: []service.PublicCatalogItem{{ModelKey: "alpha-chat", DisplayName: "Alpha", Provider: "acme", Capabilities: []string{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: "1.00000000", OutputPriceUSDPerMillionTokens: "2.00000000"}}, GoneKeys: map[string]struct{}{"retired": {}},
	}
}

func TestPublicReadSuccessfulHTTPBoundaryAllRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerPublicContentWithReader(r, publicReadStub{projection: testPublicProjection()}, func(*gin.Context) bool { return false })
	cases := []struct {
		path string
		keys []string
	}{{"/api/v1/public/site", []string{"content_release_version", "price_release_version", "price_visibility"}}, {"/api/v1/public/home", []string{"document", "release_version"}}, {"/api/v1/public/pages/about", []string{"document", "release_version"}}, {"/api/v1/public/pages/terms", []string{"document", "release_version"}}, {"/api/v1/public/pages/privacy", []string{"document", "release_version"}}, {"/api/v1/public/models", []string{"items", "page", "page_size", "release_version", "total"}}, {"/api/v1/public/models/alpha-chat", []string{"model"}}}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatal("invalid json")
		}
		got := make([]string, 0, len(body))
		for k := range body {
			got = append(got, k)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(tc.keys, ",") {
			t.Fatalf("%s keys=%v want=%v", tc.path, got, tc.keys)
		}
		if rec.Header().Get("ETag") != `"abc"` || rec.Header().Get("X-Public-Release-Version") == "" {
			t.Fatalf("%s headers=%#v", tc.path, rec.Header())
		}
		if rec.Header().Get("Cache-Control") != publicCacheControl || rec.Header().Get("Vary") != "Authorization" {
			t.Fatalf("%s cache headers=%#v", tc.path, rec.Header())
		}
		wantVersion := "7"
		if strings.Contains(tc.path, "models") {
			wantVersion = "9"
		}
		if rec.Header().Get("X-Public-Release-Version") != wantVersion {
			t.Fatalf("%s version=%s", tc.path, rec.Header().Get("X-Public-Release-Version"))
		}
		for _, bad := range []string{"upstream_model_id", "snapshot_id", "model_config_id", "guid", "draft", "credential", "pricing_disclaimer", "references_only_no_automatic_charge"} {
			if strings.Contains(rec.Body.String(), bad) {
				t.Fatalf("%s leaked %s: %s", tc.path, bad, rec.Body.String())
			}
		}
		if strings.Contains(tc.path, "models") && strings.Contains(rec.Body.String(), "_price_usd_") {
			t.Fatalf("anonymous price leak %s", rec.Body.String())
		}
	}
}

func TestPublicReadUnsupportedMethodsRemainReal404(t *testing.T) {
	r := gin.New()
	registerPublicContentWithReader(r, publicReadStub{projection: testPublicProjection()}, func(*gin.Context) bool { return false })
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, "/api/v1/public/site", nil))
		if rec.Code != 404 {
			t.Fatalf("%s status=%d", method, rec.Code)
		}
	}
}

func TestPublicReadConditionalVariantsPreserveHeadersAndEmptyBody(t *testing.T) {
	for _, value := range []string{`"abc"`, `W/"abc"`, ` "no", W/"abc" `, `"non,current", W/"abc"`, `W/abc, "abc"`, `, "abc",`, `*`} {
		r := gin.New()
		registerPublicContentWithReader(r, publicReadStub{projection: testPublicProjection()}, func(*gin.Context) bool { return false })
		req := httptest.NewRequest(http.MethodGet, "/api/v1/public/models", nil)
		req.Header.Set("If-None-Match", value)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != `"abc"` || rec.Header().Get("Vary") != "Authorization" || rec.Header().Get("X-Public-Release-Version") != "9" {
			t.Fatalf("%q status=%d headers=%#v body=%q", value, rec.Code, rec.Header(), rec.Body.String())
		}
	}
}

func TestPublicReadConditionalCombinesRepeatedHeaderLines(t *testing.T) {
	r := gin.New()
	registerPublicContentWithReader(r, publicReadStub{projection: testPublicProjection()}, func(*gin.Context) bool { return false })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/site", nil)
	req.Header.Add("If-None-Match", `"other"`)
	req.Header.Add("If-None-Match", `W/"abc"`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestPublicReadAuthenticatedPricingUsesPrivatePartition(t *testing.T) {
	r := gin.New()
	registerPublicContentWithReader(r, publicReadStub{projection: testPublicProjection()}, func(*gin.Context) bool { return true })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/models/alpha-chat", nil)
	req.Header.Set("Authorization", "Bearer valid")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("Vary") != "Authorization" || !strings.Contains(rec.Body.String(), `"input_price_usd_per_million_tokens":"1.00000000"`) {
		t.Fatalf("status=%d headers=%#v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	var body struct {
		Model map[string]any `json:"model"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	keys := make([]string, 0, len(body.Model))
	for k := range body.Model {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"capabilities", "context_window", "display_name", "input_price_usd_per_million_tokens", "model_key", "output_price_usd_per_million_tokens", "price_visibility", "provider", "release_version"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("model keys=%v", keys)
	}
}

func TestPublicReadAnonymousModelSchemasAreExactAndPricesOmitted(t *testing.T) {
	r := gin.New()
	registerPublicContentWithReader(r, publicReadStub{projection: testPublicProjection()}, func(*gin.Context) bool { return false })
	for _, path := range []string{"/api/v1/public/models", "/api/v1/public/models/alpha-chat"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var root map[string]any
		if json.Unmarshal(rec.Body.Bytes(), &root) != nil {
			t.Fatal("invalid JSON")
		}
		var model map[string]any
		if raw, ok := root["model"].(map[string]any); ok {
			model = raw
		} else {
			items, _ := root["items"].([]any)
			model, _ = items[0].(map[string]any)
		}
		keys := make([]string, 0, len(model))
		for k := range model {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		want := []string{"capabilities", "context_window", "display_name", "model_key", "price_visibility", "provider", "release_version"}
		if strings.Join(keys, ",") != strings.Join(want, ",") {
			t.Fatalf("%s keys=%v", path, keys)
		}
		if model["price_visibility"] != "authenticated_only" {
			t.Fatalf("%s model=%#v", path, model)
		}
	}
}

func TestPublicReadNotFoundGoneUnavailableAndChunkedBodyAreNoStore(t *testing.T) {
	for _, tc := range []struct {
		path   string
		reader publicProjectionReader
		want   int
	}{{"/api/v1/public/models/unknown", publicReadStub{projection: testPublicProjection()}, 404}, {"/api/v1/public/models/retired", publicReadStub{projection: testPublicProjection()}, 410}, {"/api/v1/public/site", publicReadStub{err: &service.HTTPError{Status: 503, Message: "unsafe"}}, 503}} {
		r := gin.New()
		registerPublicContentWithReader(r, tc.reader, func(*gin.Context) bool { return false })
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("ETag") != "" || rec.Header().Get("X-Public-Release-Version") != "" {
			t.Fatalf("%s status=%d headers=%#v body=%s", tc.path, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	r := gin.New()
	registerPublicContentWithReader(r, publicReadStub{projection: testPublicProjection()}, func(*gin.Context) bool { return false })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/site", strings.NewReader("x"))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 400 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("chunked status=%d headers=%#v", rec.Code, rec.Header())
	}
}

func TestPublicReadMalformedIfNoneMatchDoesNotSuppressBody(t *testing.T) {
	for _, value := range []string{`W/abc`, `*, "other"`, `"abc`} {
		r := gin.New()
		registerPublicContentWithReader(r, publicReadStub{projection: testPublicProjection()}, func(*gin.Context) bool { return false })
		req := httptest.NewRequest(http.MethodGet, "/api/v1/public/site", nil)
		req.Header.Set("If-None-Match", value)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Body.Len() == 0 {
			t.Fatalf("%q status=%d body=%q", value, rec.Code, rec.Body.String())
		}
	}
}

func TestPublicSiteRoutesAreRegisteredWithoutAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterPublicContent(r, &app.State{Settings: &config.Settings{}})
	want := map[string]bool{
		"GET /api/v1/public/site": true, "GET /api/v1/public/home": true, "GET /api/v1/public/models": true,
		"GET /api/v1/public/models/:modelKey": true, "GET /api/v1/public/pages/about": true,
		"GET /api/v1/public/pages/terms": true, "GET /api/v1/public/pages/privacy": true,
	}
	for _, route := range r.Routes() {
		delete(want, route.Method+" "+route.Path)
	}
	if len(want) != 0 {
		t.Fatalf("missing routes: %#v", want)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/public/site", nil))
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("public route required authentication: %s", rec.Body.String())
	}
}

func TestPublicReadRejectsMalformedQueriesBeforeReading(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterPublicContent(r, &app.State{Settings: &config.Settings{}})
	for _, path := range []string{"/api/v1/public/site?x=1", "/api/v1/public/models?page=01", "/api/v1/public/models?page_size=21", "/api/v1/public/models?search=a&search=b", "/api/v1/public/models/%2F"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestPublicReadInvalidOptionalAuthenticationUsesSafeNoStoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterPublicContent(r, &app.State{Settings: &config.Settings{JWTSecretKey: "test"}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/models", nil)
	req.Header.Set("Authorization", "Bearer invalid")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
		t.Fatalf("status=%d headers=%#v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
}
