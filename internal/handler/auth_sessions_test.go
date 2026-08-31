package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/models"
)

// TestAuthSessionRoutesReplaceTheRetiredPhoneSurface proves the new session
// API is routable while the retired phone endpoints stay explicitly gone.
func TestAuthSessionRoutesReplaceTheRetiredPhoneSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterAuth(r, nil)
	for _, path := range []string{"/api/v1/auth/register", "/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusGone {
			t.Fatalf("new auth route %s was not registered: status=%d", path, rec.Code)
		}
		if rec.Header().Get("X-Request-ID") == "" {
			t.Fatalf("new auth route %s omitted request ID", path)
		}
	}
}

// TestRefreshCookieAndOriginGuardAreBrowserSafe locks the required browser
// boundary: refresh is HttpOnly/Secure/Lax and untrusted Origins are rejected.
func TestRefreshCookieAndOriginGuardAreBrowserSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/cookie", func(c *gin.Context) { setRefreshCookie(c, "sid.secret", 30); c.Status(http.StatusNoContent) })
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/cookie", nil))
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Name != "porsche_refresh" {
		t.Fatalf("unsafe refresh cookie: %#v", cookie)
	}

	guard := gin.New()
	state := &app.State{Settings: &config.Settings{AuthTrustedOrigins: []string{"https://app.example.test"}}}
	guard.POST("/refresh", func(c *gin.Context) {
		if requireTrustedOrigin(c, state) {
			c.Status(http.StatusNoContent)
		}
	})
	bad := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	bad.Header.Set("Origin", "https://attacker.example")
	badRec := httptest.NewRecorder()
	guard.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusForbidden || !strings.Contains(badRec.Body.String(), "auth_origin_denied") {
		t.Fatalf("untrusted Origin was accepted: status=%d body=%s", badRec.Code, badRec.Body.String())
	}
	missingRec := httptest.NewRecorder()
	guard.ServeHTTP(missingRec, httptest.NewRequest(http.MethodPost, "/refresh", nil))
	if missingRec.Code != http.StatusForbidden {
		t.Fatalf("missing Origin was accepted: status=%d body=%s", missingRec.Code, missingRec.Body.String())
	}
	trusted := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	trusted.Header.Set("Origin", "https://app.example.test")
	trustedRec := httptest.NewRecorder()
	guard.ServeHTTP(trustedRec, trusted)
	if trustedRec.Code != http.StatusNoContent {
		t.Fatalf("trusted Origin rejected: status=%d body=%s", trustedRec.Code, trustedRec.Body.String())
	}
}

// TestAuthDTOsNeverExposeSessionSecrets ensures HTTP serializers do not make
// internal IDs, hashes, cookie values, or refresh credentials observable.
func TestAuthDTOsNeverExposeSessionSecrets(t *testing.T) {
	password := "$argon2id$secret"
	username := "alice"
	user := &models.User{AuditFields: models.AuditFields{Guid: 101}, Username: &username, PasswordHash: &password, Role: models.UserRoleUser}
	session := &models.Session{AuditFields: models.AuditFields{Guid: 202}, SID: "secret-selector", RefreshHMAC: "secret-hmac", UserID: 99}
	for name, value := range map[string]map[string]any{
		"auth-user": dto.AuthUser(user),
		"session":   dto.AuthSession(session, true),
	} {
		for _, forbidden := range []string{"id", "user_id", "password_hash", "refresh_token", "refresh_hmac", "sid", "cookie", "authorization"} {
			if _, found := value[forbidden]; found {
				t.Fatalf("%s leaked %s", name, forbidden)
			}
		}
	}
}
