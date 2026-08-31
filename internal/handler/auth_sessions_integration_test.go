package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
)

// TestAuthSessionHTTPFlow exercises the browser-facing contract against the
// explicit, isolated MySQL and Redis fixtures. It intentionally requires both
// TEST_* variables and refuses non-test database names before applying schema.
func TestAuthSessionHTTPFlow(t *testing.T) {
	state := authHTTPTestState(t)
	engine := gin.New()
	RegisterAuth(engine, state)

	register := authJSONRequest(http.MethodPost, "/api/v1/auth/register", `{"username":"http_flow_user","password":"Str0ng!Pass1","nickname":"HTTP User"}`)
	registerRec := serveAuthRequest(engine, register)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registerRec.Code, registerRec.Body.String())
	}
	assertAuthResponseHasNoSecrets(t, registerRec.Body.Bytes())

	accessA, cookieA := authHTTPLogin(t, engine, "http_flow_user", "Str0ng!Pass1")
	sidA := refreshSID(t, cookieA)
	assertAccessSID(t, state, accessA, sidA)

	self := authJSONRequest(http.MethodGet, "/api/v1/auth/self", "")
	self.Header.Set("Authorization", "Bearer "+accessA)
	selfRec := serveAuthRequest(engine, self)
	if selfRec.Code != http.StatusOK {
		t.Fatalf("self status=%d body=%s", selfRec.Code, selfRec.Body.String())
	}
	assertAuthResponseHasNoSecrets(t, selfRec.Body.Bytes())

	accessB, _ := authHTTPLogin(t, engine, "http_flow_user", "Str0ng!Pass1")
	sessions := authJSONRequest(http.MethodGet, "/api/v1/auth/sessions", "")
	sessions.Header.Set("Authorization", "Bearer "+accessA)
	sessionsRec := serveAuthRequest(engine, sessions)
	if sessionsRec.Code != http.StatusOK {
		t.Fatalf("sessions status=%d body=%s", sessionsRec.Code, sessionsRec.Body.String())
	}
	assertAuthResponseHasNoSecrets(t, sessionsRec.Body.Bytes())
	if !strings.Contains(sessionsRec.Body.String(), `"current":true`) {
		t.Fatalf("current session was not marked current: %s", sessionsRec.Body.String())
	}

	revokeOthers := authJSONRequest(http.MethodPost, "/api/v1/auth/sessions/revoke-others", "")
	revokeOthers.Header.Set("Authorization", "Bearer "+accessA)
	if rec := serveAuthRequest(engine, revokeOthers); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke others status=%d body=%s", rec.Code, rec.Body.String())
	}
	revokedSelf := authJSONRequest(http.MethodGet, "/api/v1/auth/self", "")
	revokedSelf.Header.Set("Authorization", "Bearer "+accessB)
	if rec := serveAuthRequest(engine, revokedSelf); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked other session still authenticated: status=%d body=%s", rec.Code, rec.Body.String())
	}

	badRefresh := authJSONRequest(http.MethodPost, "/api/v1/auth/refresh", "")
	badRefresh.Header.Set("Origin", "https://app.example.test")
	badRefresh.AddCookie(cookieA)
	badRefresh.Header.Set("X-Auth-Session", "00000000-0000-0000-0000-000000000000")
	if rec := serveAuthRequest(engine, badRefresh); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh accepted mismatched X-Auth-Session: status=%d body=%s", rec.Code, rec.Body.String())
	}

	refresh := authJSONRequest(http.MethodPost, "/api/v1/auth/refresh", "")
	refresh.Header.Set("Origin", "https://app.example.test")
	refresh.AddCookie(cookieA)
	refresh.Header.Set("X-Auth-Session", sidA)
	refreshRec := serveAuthRequest(engine, refresh)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refreshRec.Code, refreshRec.Body.String())
	}
	accessRefreshed := authAccessToken(t, refreshRec.Body.Bytes())
	cookieRefreshed := refreshRec.Result().Cookies()[0]
	assertAccessSID(t, state, accessRefreshed, refreshSID(t, cookieRefreshed))
	assertAuthResponseHasNoSecrets(t, refreshRec.Body.Bytes())

	logout := authJSONRequest(http.MethodPost, "/api/v1/auth/logout", "")
	logout.Header.Set("Authorization", "Bearer "+accessRefreshed)
	logout.Header.Set("Origin", "https://app.example.test")
	logout.AddCookie(cookieRefreshed)
	logout.Header.Set("X-Auth-Session", refreshSID(t, cookieRefreshed))
	logoutRec := serveAuthRequest(engine, logout)
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", logoutRec.Code, logoutRec.Body.String())
	}
	cleared := logoutRec.Result().Cookies()[0]
	if cleared.MaxAge >= 0 || !cleared.HttpOnly || !cleared.Secure || cleared.SameSite != http.SameSiteLaxMode {
		t.Fatalf("logout did not securely clear refresh cookie: %#v", cleared)
	}
	loggedOutSelf := authJSONRequest(http.MethodGet, "/api/v1/auth/self", "")
	loggedOutSelf.Header.Set("Authorization", "Bearer "+accessRefreshed)
	if rec := serveAuthRequest(engine, loggedOutSelf); rec.Code != http.StatusUnauthorized {
		t.Fatalf("logged out session still authenticated: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminUsersHTTPHierarchy ensures list/detail paths apply the same strict
// downward hierarchy and never serialize sensitive account fields.
func TestAdminUsersHTTPHierarchy(t *testing.T) {
	state := authHTTPTestState(t)
	admin := platformTestUser("auth-http-admin", nil)
	admin.Role = models.UserRoleAdmin
	peer := platformTestUser("auth-http-peer", nil)
	peer.Role = models.UserRoleAdmin
	root := platformTestUser("auth-http-root", nil)
	root.Role = models.UserRoleRoot
	ordinary := platformTestUser("auth-http-user", nil)
	for _, user := range []*models.User{&admin, &peer, &root, &ordinary} {
		if err := state.DB.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	engine := gin.New()
	RegisterAdminUsers(engine, state)
	adminAccess := platformJWT(t, state, &admin)

	list := authJSONRequest(http.MethodGet, "/admin/users", "")
	list.Header.Set("Authorization", "Bearer "+adminAccess)
	listRec := serveAuthRequest(engine, list)
	if listRec.Code != http.StatusOK || strings.Contains(listRec.Body.String(), strconv.FormatInt(peer.Guid, 10)) || strings.Contains(listRec.Body.String(), strconv.FormatInt(root.Guid, 10)) {
		t.Fatalf("admin list exposed equal/root role: status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	assertAuthResponseHasNoSecrets(t, listRec.Body.Bytes())
	for _, target := range []*models.User{&peer, &root} {
		req := authJSONRequest(http.MethodGet, "/admin/users/"+strconv.FormatInt(target.Guid, 10), "")
		req.Header.Set("Authorization", "Bearer "+adminAccess)
		if rec := serveAuthRequest(engine, req); rec.Code != http.StatusForbidden {
			t.Fatalf("admin accessed target role=%v: status=%d body=%s", target.Role, rec.Code, rec.Body.String())
		}
	}
	rootAccess := platformJWT(t, state, &root)
	allowed := authJSONRequest(http.MethodGet, "/admin/users/"+strconv.FormatInt(ordinary.Guid, 10), "")
	allowed.Header.Set("Authorization", "Bearer "+rootAccess)
	allowedRec := serveAuthRequest(engine, allowed)
	if allowedRec.Code != http.StatusOK {
		t.Fatalf("root could not access lower role: status=%d body=%s", allowedRec.Code, allowedRec.Body.String())
	}
	assertAuthResponseHasNoSecrets(t, allowedRec.Body.Bytes())
}

// TestAuthUnavailableUsesTheReviewedErrorEnvelope keeps setup and dependency
// failures from leaking implementation details through browser auth endpoints.
func TestAuthUnavailableUsesTheReviewedErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterAuth(engine, nil)
	rec := serveAuthRequest(engine, authJSONRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"alice","password":"Str0ng!Pass1"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable auth status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Error.Code != "auth_unavailable" || response.Error.Message == "" || response.Error.Type != "authentication_error" || response.Error.RequestID == "" {
		t.Fatalf("invalid auth error envelope: err=%v body=%s", err, rec.Body.String())
	}
}

func authHTTPTestState(t *testing.T) *app.State {
	t.Helper()
	state := newPlatformWhiteLabelTestState(t)
	parsed, err := url.Parse(state.Settings.DatabaseURL)
	if err != nil || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") && strings.TrimPrefix(parsed.Path, "/") != "porsche_test" {
		t.Fatal("TEST_DATABASE_URL must target a dedicated *_test or porsche_test database")
	}
	state.Settings.RegisterEnabled = true
	state.Settings.PasswordRegisterEnabled = true
	state.Settings.PasswordLoginEnabled = true
	state.Settings.AuthTrustedOrigins = []string{"https://app.example.test"}
	return state
}

func authHTTPLogin(t *testing.T, engine *gin.Engine, username, password string) (string, *http.Cookie) {
	t.Helper()
	req := authJSONRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"`+username+`","password":"`+password+`"}`)
	rec := serveAuthRequest(engine, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 1 {
		t.Fatalf("login cookies=%d, want 1", len(rec.Result().Cookies()))
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/api/v1/auth" {
		t.Fatalf("unsafe login cookie: %#v", cookie)
	}
	assertAuthResponseHasNoSecrets(t, rec.Body.Bytes())
	return authAccessToken(t, rec.Body.Bytes()), cookie
}

func authJSONRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func serveAuthRequest(engine *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func authAccessToken(t *testing.T, payload []byte) string {
	t.Helper()
	var response struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.AccessToken == "" {
		t.Fatalf("response has no access token: %v body=%s", err, payload)
	}
	return response.AccessToken
}

func refreshSID(t *testing.T, cookie *http.Cookie) string {
	t.Helper()
	sid, _, found := strings.Cut(cookie.Value, ".")
	if !found || sid == "" {
		t.Fatalf("invalid refresh cookie value")
	}
	return sid
}

func assertAccessSID(t *testing.T, state *app.State, access, wantSID string) {
	t.Helper()
	claims, err := security.DecodeAccessToken(access, state.Settings.JWTSecretKey)
	if err != nil || claims["sid"] != wantSID {
		t.Fatalf("access SID does not match refresh cookie SID: claims=%v err=%v", claims, err)
	}
}

func assertAuthResponseHasNoSecrets(t *testing.T, payload []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	encoded := string(payload)
	for _, forbidden := range []string{"\"id\"", "\"user_id\"", "password_hash", "refresh_hmac", "\"sid\"", "refresh_token", "authorization"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("response leaked %s: %s", forbidden, payload)
		}
	}
}
