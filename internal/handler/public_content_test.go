package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
)

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
