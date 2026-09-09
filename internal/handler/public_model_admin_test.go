package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
)

func TestPublicAdminGUIDIsCanonical(t *testing.T) {
	for _, v := range []string{"", "0", "01", "-1", "9223372036854775808", "1x"} {
		if _, ok := publicAdminGUID(v); ok {
			t.Fatalf("accepted %q", v)
		}
	}
	if v, ok := publicAdminGUID("123"); !ok || v != 123 {
		t.Fatal("canonical guid rejected")
	}
}

func TestPublicAdminAuthenticationUsesFrozenErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterPublicModelAdmin(r, &app.State{Settings: &config.Settings{}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/v2/public-models", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
		t.Fatalf("status/headers = %d %#v", rec.Code, rec.Header())
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code != "authentication_required" || body.Error.Message == "" || body.Error.RequestID != rec.Header().Get("X-Request-ID") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
func TestPublicAdminQueryRejectsUnknownAndDuplicate(t *testing.T) {
	allowed := map[string]bool{"page": true}
	for _, raw := range []string{"x=1", "page=1&page=2", "page"} {
		if _, ok := publicAdminQuery(raw, allowed); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestPublicAdminQueryUsesStandardPercentAndPlusDecoding(t *testing.T) {
	q, ok := publicAdminQuery("search=alpha%2Fbeta+gamma", map[string]bool{"search": true})
	if !ok || q["search"] != "alpha/beta gamma" {
		t.Fatalf("query = %#v, ok=%v", q, ok)
	}
	for _, raw := range []string{"search=%ZZ", "search=a;b=c"} {
		if _, ok := publicAdminQuery(raw, map[string]bool{"search": true}); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
}
