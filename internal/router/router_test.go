package router_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/handler"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/router"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

func TestPublicContentPricingAdminRoutesMatchFrozenContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := &app.State{Settings: &config.Settings{AllowedHosts: "example.com"}}
	engine := router.New(state)
	got := map[string]int{}
	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/admin/v2/public-models") || strings.HasPrefix(route.Path, "/admin/v2/public-pricing") || strings.HasPrefix(route.Path, "/admin/v2/public-content") || strings.HasPrefix(route.Path, "/admin/v2/notifications") {
			got[route.Method+" "+route.Path]++
		}
	}
	raw, err := os.ReadFile("../../docs/agents/contracts/public-content-pricing-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Routes []map[string]any `json:"routes"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	rootMetadata := make([]map[string]any, 0, 28)
	for _, route := range contract.Routes {
		if route["role"] != "root" {
			continue
		}
		rootMetadata = append(rootMetadata, route)
		method, methodOK := route["method"].(string)
		contractPath, pathOK := route["path"].(string)
		if !methodOK || !pathOK {
			t.Fatal("invalid route metadata types")
		}
		for _, field := range []string{"request_headers", "response_headers", "path_schema", "query_schema", "body_schema", "response_schema", "status"} {
			if _, ok := route[field]; !ok {
				t.Fatalf("%s %s missing %s", method, contractPath, field)
			}
		}
		path := strings.ReplaceAll(contractPath, "{guid}", ":guid")
		key := method + " " + path
		want[key] = true
		if got[key] != 1 {
			t.Errorf("missing route %s %s", method, path)
		}
		requestPath := strings.ReplaceAll(contractPath, "{guid}", "1")
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(method, requestPath, nil))
		if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Request-ID") == "" {
			t.Fatalf("%s %s auth boundary status=%d headers=%#v", method, requestPath, recorder.Code, recorder.Header())
		}
		var envelope struct {
			Error struct {
				Code      string `json:"code"`
				RequestID string `json:"request_id"`
			} `json:"error"`
		}
		if json.Unmarshal(recorder.Body.Bytes(), &envelope) != nil || envelope.Error.Code != "authentication_required" || envelope.Error.RequestID != recorder.Header().Get("X-Request-ID") {
			t.Fatalf("%s %s envelope=%s", method, requestPath, recorder.Body.String())
		}
	}
	if len(want) != 28 || len(got) != len(want) {
		t.Fatalf("route count got=%d want=%d", len(got), len(want))
	}
	for route, count := range got {
		if !want[route] || count != 1 {
			t.Errorf("extra or duplicate route %s count=%d", route, count)
		}
	}
	metadataJSON, err := json.Marshal(rootMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(metadataJSON)); digest != "ecf66cbdabc47f85a74d72021b1429ab0a6a846816aee773a4d2bf1a6001b0c6" {
		t.Fatalf("root route metadata drift: %s", digest)
	}
}

func TestPublicModelStaticPathsAreNeverCapturedAsGUID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	matched := ""
	engine.Use(func(c *gin.Context) { matched = c.FullPath(); c.AbortWithStatus(299) })
	handler.RegisterPublicModelAdmin(engine, &app.State{Settings: &config.Settings{}})
	for _, tc := range []struct{ method, path, want string }{{http.MethodGet, "/admin/v2/public-models/missing", "/admin/v2/public-models/missing"}, {http.MethodPost, "/admin/v2/public-models/sync", "/admin/v2/public-models/sync"}} {
		matched = ""
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if matched != tc.want {
			t.Fatalf("%s matched %q", tc.path, matched)
		}
	}
}

func TestHealthOK(t *testing.T) {
	settings := &config.Settings{
		AppEnv:             "development",
		DatabaseURL:        testDatabaseURL(t),
		AllowedHosts:       "example.com",
		JWTSecretKey:       "test-secret",
		AdminToken:         "admin-test",
		FixedLoginEnabled:  true,
		FixedLoginPhone:    "13800138000",
		FixedLoginPassword: "test",
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

func TestAdminUsersGroupDirectoryRouteIsRegistered(t *testing.T) {
	settings := &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}
	engine := router.New(&app.State{Settings: settings})
	request := httptest.NewRequest(http.MethodGet, "/admin/v2/groups?status=active", nil)
	request.Host = "example.com"
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("registered group directory status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestNewStateDoesNotRegisterGenerationRoutes(t *testing.T) {
	settings := &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}
	engine := router.New(&app.State{Settings: settings})
	wantExisting := []routeContract{
		{http.MethodPost, "/api/v1/platform/chat/completions"},
		{http.MethodPost, "/api/v1/platform/chat/compare"},
	}
	for _, want := range wantExisting {
		count := 0
		for _, route := range engine.Routes() {
			if route.Method == want.Method && route.Path == want.Path {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("existing platform route %s %s count=%d, want 1", want.Method, want.Path, count)
		}
	}

	for _, unregistered := range []routeContract{
		{http.MethodGet, "/api/v1/platform/chat/generations/550e8400-e29b-41d4-a716-446655440000"},
		{http.MethodPost, "/api/v1/platform/chat/generations/550e8400-e29b-41d4-a716-446655440000/cancel"},
	} {
		request := httptest.NewRequest(unregistered.Method, unregistered.Path, nil)
		request.Host = "example.com"
		request.Header.Set("Authorization", "Bearer syntactically-valid-test-token")
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("generation route %s %s status=%d body=%s, want 404", unregistered.Method, unregistered.Path, recorder.Code, recorder.Body.String())
		}
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
	user := gatewayTestUserFixture(t, state, "13900139000")
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
	user := gatewayTestUserFixture(t, state, "13900139001")
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
	user := gatewayTestUserFixture(t, state, "13900139002")
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
	admin := createGatewayTestUser(t, state, "analytics-admin")
	admin.Role = models.UserRoleAdmin
	if err := state.DB.Model(&models.User{}).Where("id = ? AND is_deleted = 0", admin.ID).Update("role", admin.Role).Error; err != nil {
		t.Fatal(err)
	}
	if admin.Phone == nil {
		t.Fatal("analytics admin fixture must have a phone")
	}
	state.Settings.AnalyticsAdminPhones = *admin.Phone
	engine := router.New(state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/analytics/charts/unknown?top_n=999", nil)
	req.Header.Set("Authorization", "Bearer "+gatewayTestJWT(t, state, admin))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte(`"invalid_analytics_query"`)) || bytes.Contains(rec.Body.Bytes(), []byte("999")) {
		t.Fatalf("invalid analytics contract must be a generic 400: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestGatewayTokenManagementRejectsLegacyJWTWithoutSessionClaims ensures the
// test fixture cannot accidentally revive pre-session JWT authorization.
func TestGatewayTokenManagementRejectsLegacyJWTWithoutSessionClaims(t *testing.T) {
	state := newGatewayTestState(t)
	user := createGatewayTestUser(t, state, "legacy-token-owner")
	legacy, err := security.CreateAccessToken(strconv.FormatInt(user.Guid, 10), state.Settings.JWTSecretKey, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := router.New(state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+legacy)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy JWT status=%d body=%s, want 401", rec.Code, rec.Body.String())
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
		GUID  string `json:"guid"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.GUID == "" || response.Token == "" || response.Token[:6] != "sk-gw-" || bytes.Contains(created.Body.Bytes(), []byte(`"id"`)) {
		t.Fatalf("unexpected create response: %#v", response)
	}
	var stored models.GatewayAPIToken
	if err := state.DB.Where("guid = ?", response.GUID).First(&stored).Error; err != nil {
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

	foreignGet := httptest.NewRequest(http.MethodGet, "/api/v1/tokens/"+response.GUID, nil)
	foreignGet.Header.Set("Authorization", "Bearer "+gatewayTestJWT(t, state, other))
	foreignRec := httptest.NewRecorder()
	engine.ServeHTTP(foreignRec, foreignGet)
	if foreignRec.Code != http.StatusNotFound {
		t.Fatalf("cross-user token read status=%d body=%s", foreignRec.Code, foreignRec.Body.String())
	}
	foreignRevoke := httptest.NewRequest(http.MethodPost, "/api/v1/tokens/"+response.GUID+"/revoke", nil)
	foreignRevoke.Header.Set("Authorization", "Bearer "+gatewayTestJWT(t, state, other))
	foreignRevokeRec := httptest.NewRecorder()
	engine.ServeHTTP(foreignRevokeRec, foreignRevoke)
	if foreignRevokeRec.Code != http.StatusNotFound {
		t.Fatalf("cross-user token revoke status=%d body=%s", foreignRevokeRec.Code, foreignRevokeRec.Body.String())
	}

	revoke := httptest.NewRequest(http.MethodPost, "/api/v1/tokens/"+response.GUID+"/revoke", nil)
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
	user := gatewayTestUserFixture(t, state, phone)
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return &user
}

var gatewayTestSnowflake = persistence.NewSnowflake(os.Getpid()%1024, persistence.SystemClock())

func gatewayTestUserFixture(t *testing.T, state *app.State, _ string) models.User {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	return models.User{
		AuditFields:   models.AuditFields{Guid: gatewayTestSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0},
		GroupID:       gatewayTestDefaultBusinessGroupID(t, state),
		Phone:         gatewayTestPhone(),
		Status:        models.UserStatusActive,
		Role:          models.UserRoleUser,
		AuthVersion:   1,
		PlanType:      models.PlanFree,
		AllowedModels: models.JSONSlice{},
	}
}

func gatewayTestDefaultBusinessGroupID(t *testing.T, state *app.State) int64 {
	t.Helper()
	var groups []models.BusinessGroup
	if state == nil || state.DB == nil {
		t.Fatal("gateway test state has no database")
	}
	if err := state.DB.Where("group_key = ? AND is_deleted = 0", "default").Order("id ASC").Find(&groups).Error; err != nil {
		t.Fatalf("load gateway default business group: %v", err)
	}
	if len(groups) != 1 || groups[0].ID <= 0 || groups[0].Guid <= 0 || groups[0].Key != "default" || groups[0].Status != models.BusinessGroupStatusActive || groups[0].IsDeleted != 0 {
		t.Fatalf("invalid gateway default business group: %#v", groups)
	}
	return groups[0].ID
}

// gatewayTestPhone derives a database-safe phone value from the package test
// snowflake. It prevents prior test runs from colliding in a shared test DB.
func gatewayTestPhone() *string {
	phone := strconv.FormatInt(13_000_000_000+gatewayTestSnowflake.Next()%1_000_000_000, 10)
	return &phone
}

func gatewayTestJWT(t *testing.T, state *app.State, user *models.User) string {
	t.Helper()
	if state.Sessions == nil {
		t.Fatal("gateway test state must provide a real session service")
	}
	issued, err := state.Sessions.Create(context.Background(), user, service.SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.100", UserAgent: "router-test"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := security.CreateAccessToken(strconv.FormatInt(user.Guid, 10), state.Settings.JWTSecretKey, state.Settings.SessionAccessMinutes, map[string]interface{}{
		"sid": issued.Session.SID, "sv": issued.Session.SessionVersion, "av": user.AuthVersion, "role": int(user.Role),
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func newGatewayTestState(t *testing.T) *app.State {
	t.Helper()
	settings := &config.Settings{
		AppEnv: "test", DatabaseURL: testDatabaseURL(t), AllowedHosts: "example.com",
		JWTSecretKey: "test-secret", AdminToken: "admin-test", RedisURL: testRedisURL(t),
		AuthHMACKey:          "test-auth-hmac-key-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		SessionAccessMinutes: 15, SessionDays: 30, SessionMaxActive: 50, SessionIssueLimit24h: 100, RefreshReplaySeconds: 30,
	}
	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	generator := persistence.NewSnowflake(1, persistence.SystemClock())
	if err := migration.Up(context.Background(), gdb, generator.Next, func() int64 { return time.Now().UTC().UnixMilli() }); err != nil {
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

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires isolated TEST_DATABASE_URL MySQL fixture")
	}
	return url
}

func testRedisURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("requires explicitly configured TEST_REDIS_URL Redis fixture")
	}
	return url
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type routeContract struct {
	Method string
	Path   string
}

// preB1ERouteInventory freezes the complete production route multiset. Keep
// duplicate method/path entries: silently registering the same route twice is
// a regression even when the sorted set of routes would look unchanged.
var preB1ERouteInventory = []routeContract{
	{http.MethodGet, "/admin/dashboard"},
	{http.MethodGet, "/admin/dashboard/models/health"},
	{http.MethodPost, "/admin/dashboard/models/health/check"},
	{http.MethodGet, "/admin/logs"},
	{http.MethodGet, "/admin/logs/alerts"},
	{http.MethodPut, "/admin/logs/alerts/:alert_type"},
	{http.MethodPost, "/admin/models/:id/health-check"},
	{http.MethodPost, "/admin/models/health-check"},
	{http.MethodGet, "/admin/status"},
	{http.MethodGet, "/admin/users"},
	{http.MethodDelete, "/admin/users/:guid"},
	{http.MethodGet, "/admin/users/:guid"},
	{http.MethodPut, "/admin/users/:guid"},
	{http.MethodGet, "/admin/users/:guid/behavior"},
	{http.MethodGet, "/admin/v2/authz/catalog"},
	{http.MethodGet, "/admin/v2/groups"},
	{http.MethodGet, "/admin/v2/users"},
	{http.MethodGet, "/admin/v2/users/:guid"},
	{http.MethodGet, "/admin/v2/users/:guid/permissions"},
	{http.MethodPost, "/api/v1/auth/login"},
	{http.MethodPost, "/api/v1/auth/login/code"},
	{http.MethodPost, "/api/v1/auth/login/password"},
	{http.MethodPost, "/api/v1/auth/logout"},
	{http.MethodPost, "/api/v1/auth/refresh"},
	{http.MethodPost, "/api/v1/auth/register"},
	{http.MethodGet, "/api/v1/auth/self"},
	{http.MethodPost, "/api/v1/auth/self/password"},
	{http.MethodPost, "/api/v1/auth/self/verify"},
	{http.MethodPost, "/api/v1/auth/send-code"},
	{http.MethodGet, "/api/v1/auth/sessions"},
	{http.MethodDelete, "/api/v1/auth/sessions/:guid"},
	{http.MethodPost, "/api/v1/auth/sessions/revoke-others"},
	{http.MethodGet, "/api/v1/billing/analytics/access"},
	{http.MethodGet, "/api/v1/billing/analytics/charts/:view"},
	{http.MethodGet, "/api/v1/billing/analytics/export"},
	{http.MethodGet, "/api/v1/billing/analytics/models"},
	{http.MethodGet, "/api/v1/billing/analytics/summary"},
	{http.MethodPost, "/api/v1/billing/invoice"},
	{http.MethodGet, "/api/v1/billing/orders"},
	{http.MethodPost, "/api/v1/billing/orders"},
	{http.MethodPost, "/api/v1/billing/orders/:guid/pay"},
	{http.MethodGet, "/api/v1/billing/plans"},
	{http.MethodGet, "/api/v1/conversations"},
	{http.MethodPost, "/api/v1/conversations"},
	{http.MethodDelete, "/api/v1/conversations/:guid"},
	{http.MethodGet, "/api/v1/conversations/:guid"},
	{http.MethodPut, "/api/v1/conversations/:guid"},
	{http.MethodGet, "/api/v1/conversations/:guid/export/markdown"},
	{http.MethodPost, "/api/v1/platform/chat/compare"},
	{http.MethodPost, "/api/v1/platform/chat/completions"},
	{http.MethodGet, "/api/v1/platform/models"},
	{http.MethodGet, "/api/v1/platform/models/:id"},
	{http.MethodGet, "/api/v1/platform/models/detail"},
	{http.MethodGet, "/api/v1/tokens"},
	{http.MethodPost, "/api/v1/tokens"},
	{http.MethodDelete, "/api/v1/tokens/:guid"},
	{http.MethodGet, "/api/v1/tokens/:guid"},
	{http.MethodPatch, "/api/v1/tokens/:guid"},
	{http.MethodPost, "/api/v1/tokens/:guid/revoke"},
	{http.MethodGet, "/api/v1/users/me"},
	{http.MethodPut, "/api/v1/users/me"},
	{http.MethodPost, "/api/v1/users/me/password"},
	{http.MethodGet, "/api/v1/users/me/usage"},
	{http.MethodPost, "/api/v1/users/me/verify"},
	{http.MethodGet, "/health"},
	{http.MethodGet, "/metrics"},
	{http.MethodPost, "/v1/chat/completions"},
	{http.MethodGet, "/v1/models"},
	{http.MethodGet, "/v1/models/:id"},
	{http.MethodGet, "/v1/models/detail"},
}
