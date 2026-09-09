package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
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

func TestPublicAdminRootAuthenticationMatrixRealFixtures(t *testing.T) {
	state := adminAuthzHTTPState(t)
	newEngine := func() *gin.Engine {
		r := gin.New()
		g := r.Group("/admin/v2/public-probe", gatewayRequestID(), publicAdminNoStore, middleware.RequireRootWithError(state, publicAdminAuthError))
		g.GET("", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		return r
	}
	tests := []struct {
		name    string
		prepare func() string
		want    int
	}{
		{"unauthenticated", func() string { return "" }, 401},
		{"active_root", func() string { u := adminAuthzHTTPUser(t, state, models.UserRoleRoot); return platformJWT(t, state, u) }, 204},
		{"active_admin", func() string {
			u := adminAuthzHTTPUser(t, state, models.UserRoleAdmin)
			return platformJWT(t, state, u)
		}, 403},
		{"active_user", func() string { u := adminAuthzHTTPUser(t, state, models.UserRoleUser); return platformJWT(t, state, u) }, 403},
		{"disabled_root", func() string {
			u := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
			token := platformJWT(t, state, u)
			if err := state.DB.Model(u).Update("status", models.UserStatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
			return token
		}, 401},
		{"deleted_root", func() string {
			u := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
			token := platformJWT(t, state, u)
			if err := state.DB.Model(u).Update("is_deleted", 1).Error; err != nil {
				t.Fatal(err)
			}
			return token
		}, 401},
		{"stale_auth_version", func() string {
			u := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
			token := platformJWT(t, state, u)
			if err := state.DB.Model(u).Update("auth_version", u.AuthVersion+1).Error; err != nil {
				t.Fatal(err)
			}
			return token
		}, 401},
		{"stale_role", func() string {
			u := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
			token := platformJWT(t, state, u)
			if err := state.DB.Model(u).Update("role", models.UserRoleAdmin).Error; err != nil {
				t.Fatal(err)
			}
			return token
		}, 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/admin/v2/public-probe", nil)
			if token := tc.prepare(); token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			newEngine().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
				t.Fatalf("headers=%#v", rec.Header())
			}
			if tc.want != 204 {
				var body struct {
					Error struct {
						Code      string `json:"code"`
						Message   string `json:"message"`
						RequestID string `json:"request_id"`
					} `json:"error"`
				}
				if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.RequestID != rec.Header().Get("X-Request-ID") {
					t.Fatalf("body=%s", rec.Body.String())
				}
				wantCode := "authentication_required"
				if tc.want == 403 {
					wantCode = "root_role_required"
				}
				if body.Error.Code != wantCode || body.Error.Message == "" {
					t.Fatalf("body=%s", rec.Body.String())
				}
			}
		})
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
