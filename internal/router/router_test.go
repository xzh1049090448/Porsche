package router_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/router"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

func TestHealthOK(t *testing.T) {
	settings := &config.Settings{
		AppEnv:             "development",
		DatabaseURL:        "sqlite://./data/test_platform.db",
		AllowedHosts:       "example.com",
		JWTSecretKey:       "test-secret",
		AdminToken:         "admin-test",
		FixedLoginEnabled:  true,
		FixedLoginPhone:    "13800138000",
		FixedLoginPassword: "test",
		ChromaPersistDir:   "./data/test_chroma",
		DatasetUploadDir:   "./data/test_uploads",
	}

	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal(err)
	}
	engine := router.New(state)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var data map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", data["status"])
	}
	if data["upstream"] != "whitelabel" {
		t.Fatalf("expected whitelabel health status, got %v", data["upstream"])
	}
}

func TestHostAllowlistAcceptsDomainAndRejectsDirectIPAddress(t *testing.T) {
	state := newGatewayTestState(t)
	state.Settings.AllowedHosts = "aiportcloud.com"
	engine := router.New(state)

	allowed := httptest.NewRequest(http.MethodGet, "/health", nil)
	allowed.Host = "aiportcloud.com:8000"
	allowedRec := httptest.NewRecorder()
	engine.ServeHTTP(allowedRec, allowed)
	if allowedRec.Code != http.StatusOK {
		t.Fatalf("allowed host status=%d body=%s", allowedRec.Code, allowedRec.Body.String())
	}

	blocked := httptest.NewRequest(http.MethodGet, "/health", nil)
	blocked.Host = "127.0.0.1:8000"
	blockedRec := httptest.NewRecorder()
	engine.ServeHTTP(blockedRec, blocked)
	if blockedRec.Code != http.StatusForbidden {
		t.Fatalf("direct IP status=%d body=%s", blockedRec.Code, blockedRec.Body.String())
	}
}

func TestGatewayModelsAreFilteredByDatabaseToken(t *testing.T) {
	state := newGatewayTestState(t)
	user := models.User{Phone: "13900139000", Status: models.UserStatusActive}
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(&user, service.GatewayTokenCreateInput{AllowedModels: models.JSONSlice{"qwen-turbo"}, Name: "models"})
	if err != nil {
		t.Fatal(err)
	}
	engine := router.New(state)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected request ID header")
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "qwen-turbo" {
		t.Fatalf("unexpected filtered models: %#v", body.Data)
	}
}

func TestGatewayRejectsTokenModelBeforeUpstream(t *testing.T) {
	state := newGatewayTestState(t)
	user := models.User{Phone: "13900139001", Status: models.UserStatusActive}
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(&user, service.GatewayTokenCreateInput{AllowedModels: models.JSONSlice{"qwen-turbo"}, Name: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	engine := router.New(state)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"qwen-plus","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != string(service.GatewayTokenModelDenied) {
		t.Fatalf("unexpected error: %s", body.Error.Code)
	}
}

func TestGatewayRejectsSpoofedForwardedIPFromUntrustedPeer(t *testing.T) {
	state := newGatewayTestState(t)
	state.Settings.TrustProxyHeaders = true
	user := models.User{Phone: "13900139002", Status: models.UserStatusActive}
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(&user, service.GatewayTokenCreateInput{Name: "ip", IPAllowlist: models.JSONSlice{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	engine := router.New(state)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "198.51.100.24:5000"
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("spoofed XFF status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnalyticsChartsRejectInvalidQueriesAfterAdminAuthorization(t *testing.T) {
	state := newGatewayTestState(t)
	state.Settings.AnalyticsAdminPhones = "13900139999"
	admin := createGatewayTestUser(t, state, "13900139999")
	engine := router.New(state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/analytics/charts/unknown?top_n=999", nil)
	req.Header.Set("Authorization", "Bearer "+gatewayTestJWT(t, state, admin))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte(`"invalid_analytics_query"`)) || bytes.Contains(rec.Body.Bytes(), []byte("999")) {
		t.Fatalf("invalid analytics contract must be a generic 400: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayTokenJWTCRUDScopesOwnerAndNeverReturnsPlaintextAgain(t *testing.T) {
	state := newGatewayTestState(t)
	owner := createGatewayTestUser(t, state, "13900139003")
	other := createGatewayTestUser(t, state, "13900139004")
	engine := router.New(state)

	create := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", bytes.NewBufferString(`{"name":"production","allowed_models":["qwen-turbo"]}`))
	create.Header.Set("Authorization", "Bearer "+gatewayTestJWT(t, state, owner))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	engine.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var response struct {
		ID    int    `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID == 0 || response.Token == "" || response.Token[:6] != "sk-gw-" {
		t.Fatalf("unexpected create response: %#v", response)
	}
	var stored models.GatewayAPIToken
	if err := state.DB.First(&stored, response.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash == "" || stored.TokenHash == response.Token || created.Body.String() == stored.TokenHash {
		t.Fatalf("plaintext token leaked or token hash was not persisted: response=%s", created.Body.String())
	}

	list := httptest.NewRequest(http.MethodGet, "/api/v1/tokens", nil)
	list.Header.Set("Authorization", "Bearer "+gatewayTestJWT(t, state, owner))
	listed := httptest.NewRecorder()
	engine.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || bytes.Contains(listed.Body.Bytes(), []byte(response.Token)) {
		t.Fatalf("list should omit plaintext token: status=%d body=%s", listed.Code, listed.Body.String())
	}

	foreignGet := httptest.NewRequest(http.MethodGet, "/api/v1/tokens/"+strconv.Itoa(response.ID), nil)
	foreignGet.Header.Set("Authorization", "Bearer "+gatewayTestJWT(t, state, other))
	foreignRec := httptest.NewRecorder()
	engine.ServeHTTP(foreignRec, foreignGet)
	if foreignRec.Code != http.StatusNotFound {
		t.Fatalf("cross-user token read status=%d body=%s", foreignRec.Code, foreignRec.Body.String())
	}
	foreignRevoke := httptest.NewRequest(http.MethodPost, "/api/v1/tokens/"+strconv.Itoa(response.ID)+"/revoke", nil)
	foreignRevoke.Header.Set("Authorization", "Bearer "+gatewayTestJWT(t, state, other))
	foreignRevokeRec := httptest.NewRecorder()
	engine.ServeHTTP(foreignRevokeRec, foreignRevoke)
	if foreignRevokeRec.Code != http.StatusNotFound {
		t.Fatalf("cross-user token revoke status=%d body=%s", foreignRevokeRec.Code, foreignRevokeRec.Body.String())
	}

	revoke := httptest.NewRequest(http.MethodPost, "/api/v1/tokens/"+strconv.Itoa(response.ID)+"/revoke", nil)
	revoke.Header.Set("Authorization", "Bearer "+gatewayTestJWT(t, state, owner))
	revoked := httptest.NewRecorder()
	engine.ServeHTTP(revoked, revoke)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoked.Code, revoked.Body.String())
	}

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer "+response.Token)
	modelsRec := httptest.NewRecorder()
	engine.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusUnauthorized || !bytes.Contains(modelsRec.Body.Bytes(), []byte(`"gateway_token_revoked"`)) {
		t.Fatalf("revoked token status=%d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
}

func TestGatewayRejectsIPBeforeUpstreamAndHonorsTrustedProxy(t *testing.T) {
	state := newGatewayTestState(t)
	user := createGatewayTestUser(t, state, "13900139005")
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{
		Name: "ip", AllowedModels: models.JSONSlice{"qwen-turbo"}, IPAllowlist: models.JSONSlice{"203.0.113.7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := router.New(state)

	denied := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"qwen-turbo","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`))
	denied.RemoteAddr = "198.51.100.24:5000"
	denied.Header.Set("Authorization", "Bearer "+secret)
	denied.Header.Set("Content-Type", "application/json")
	deniedRec := httptest.NewRecorder()
	engine.ServeHTTP(deniedRec, denied)
	if deniedRec.Code != http.StatusForbidden || !bytes.Contains(deniedRec.Body.Bytes(), []byte(`"gateway_ip_not_allowed"`)) {
		t.Fatalf("IP ACL must reject before upstream: status=%d body=%s", deniedRec.Code, deniedRec.Body.String())
	}

	state.Settings.TrustProxyHeaders = true
	state.Settings.TrustedProxyCIDRs = "198.51.100.0/24"
	trusted := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	trusted.RemoteAddr = "198.51.100.24:5000"
	trusted.Header.Set("Authorization", "Bearer "+secret)
	trusted.Header.Set("X-Forwarded-For", "203.0.113.7, 198.51.100.24")
	trustedRec := httptest.NewRecorder()
	engine.ServeHTTP(trustedRec, trusted)
	if trustedRec.Code != http.StatusOK {
		t.Fatalf("trusted proxy XFF status=%d body=%s", trustedRec.Code, trustedRec.Body.String())
	}
}

func TestGatewayErrorDoesNotEchoSecretAndSanitizesRequestID(t *testing.T) {
	state := newGatewayTestState(t)
	engine := router.New(state)
	secret := "sk-gw-not-a-real-secret"
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("X-Request-ID", strings.Repeat("x", 129))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || bytes.Contains(rec.Body.Bytes(), []byte(secret)) {
		t.Fatalf("gateway error leaked credential or used unexpected status: status=%d body=%s", rec.Code, rec.Body.String())
	}
	requestID := rec.Header().Get("X-Request-ID")
	if len(requestID) != 32 || requestID == req.Header.Get("X-Request-ID") {
		t.Fatalf("invalid request ID was not replaced safely: %q", requestID)
	}
}

func createGatewayTestUser(t *testing.T, state *app.State, phone string) *models.User {
	t.Helper()
	user := &models.User{Phone: phone, Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

func gatewayTestJWT(t *testing.T, state *app.State, user *models.User) string {
	t.Helper()
	token, err := security.CreateAccessToken(strconv.Itoa(user.ID), state.Settings.JWTSecretKey, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func newGatewayTestState(t *testing.T) *app.State {
	t.Helper()
	dir := t.TempDir()
	settings := &config.Settings{
		AppEnv: "test", DatabaseURL: "sqlite://" + dir + "/platform.db", AllowedHosts: "example.com",
		JWTSecretKey: "test-secret", AdminToken: "admin-test", ChromaPersistDir: dir + "/chroma", DatasetUploadDir: dir + "/uploads",
	}
	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal(err)
	}
	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{
		BaseURL: "https://white-label.test/v1", APIKey: "test-key", AllowedModels: map[string]struct{}{"qwen-turbo": {}, "qwen-plus": {}},
	}, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"qwen-turbo"},{"id":"qwen-plus"}]}`
		if strings.HasPrefix(req.URL.Path, "/v1/chat/completions") {
			body = `{"id":"chatcmpl-test"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.WhiteLabel = whiteLabel
	return state
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
