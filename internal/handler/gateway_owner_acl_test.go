package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/router"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The same key and WhiteLabel instance survive all updates: a warm metadata
// cache must never serve as a cache of the owner's authorization decision.
func TestGatewayOwnerACLManagedUpdateImmediatelyFiltersWarmHTTPMetadata(t *testing.T) {
	state, calls, clock := gatewayOwnerHTTPState(t)
	actor := gatewayOwnerHTTPUser(t, state, models.UserRoleAdmin, nil)
	owner := gatewayOwnerHTTPUser(t, state, models.UserRoleUser, models.JSONSlice{"model-a", "model-b"})
	other := gatewayOwnerHTTPUser(t, state, models.UserRoleUser, models.JSONSlice{"model-b"})
	key := gatewayOwnerHTTPKey(t, state, owner, models.JSONSlice{"model-a", "model-b", "global-denied"})
	otherKey := gatewayOwnerHTTPKey(t, state, other, nil)
	issued, err := state.Sessions.Create(context.Background(), owner, service.SessionCreateInput{LoginMethod: models.LoginMethodPassword})
	if err != nil {
		t.Fatal("create owner session failed")
	}
	engine := router.New(state)
	request := func(method, path, body, secret string) *httptest.ResponseRecorder {
		return gatewayOwnerHTTPRequest(engine, method, path, body, secret)
	}
	list := func(secret string, want ...string) {
		t.Helper()
		rec := request(http.MethodGet, "/v1/models", "", secret)
		gatewayOwnerAssertCatalog(t, rec, want)
	}
	update := func(acl models.JSONSlice) {
		t.Helper()
		before := owner.AuthVersion
		updated, err := state.Auth.UpdateManagedUser(context.Background(), actor.ID, owner.Guid, service.ManagedUserUpdateInput{AllowedModels: &acl})
		if err != nil {
			t.Fatal("managed owner ACL update failed")
		}
		if updated.AuthVersion != before+1 || !reflect.DeepEqual(updated.AllowedModels, acl) {
			t.Fatal("managed update did not commit the requested ACL and auth version")
		}
		owner = updated
	}
	denied := func(secret, model string, chatStatus int, chatCode string) {
		t.Helper()
		for _, path := range []string{"/v1/models/detail?id=" + url.QueryEscape(model), "/v1/models/" + model} {
			before := calls.snapshot()
			rec := request(http.MethodGet, path, "", secret)
			gatewayOwnerAssertError(t, rec, http.StatusNotFound, "model_unavailable")
			if after := calls.snapshot(); after != before {
				t.Fatalf("denied detail contacted upstream: before=%v after=%v", before, after)
			}
		}
		for _, stream := range []string{"false", "true"} {
			before := calls.snapshot()
			body := `{"model":"` + model + `","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"stream":` + stream + `}`
			rec := request(http.MethodPost, "/v1/chat/completions", body, secret)
			gatewayOwnerAssertError(t, rec, chatStatus, chatCode)
			if after := calls.snapshot(); after != before {
				t.Fatalf("denied chat contacted upstream: before=%v after=%v", before, after)
			}
		}
	}

	list(key, "model-a", "model-b")
	for _, path := range []string{"/v1/models/detail?id=model-b", "/v1/models/model-b"} {
		rec := request(http.MethodGet, path, "", key)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("allowed detail status=%d cache-control=%q", rec.Code, rec.Header().Get("Cache-Control"))
		}
		var detail struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || detail.ID != "model-b" {
			t.Fatal("allowed detail did not return the requested model")
		}
	}
	if got := calls.snapshot(); got != [3]int64{1, 1, 0} {
		t.Fatalf("expected one catalog and one detail fetch to warm both caches, got %v", got)
	}

	update(models.JSONSlice{"model-a"})
	list(key, "model-a") // The first request after commit must see the narrowed ACL.
	revoked, err := state.AuthRedis.IsSessionRevoked(context.Background(), issued.Session.SID)
	if err != nil || !revoked {
		t.Fatal("managed update did not establish the real Redis revocation barrier")
	}
	denied(key, "model-b", http.StatusForbidden, "gateway_model_not_allowed")
	list(otherKey, "model-b")
	list(key, "model-a")
	if got := calls.snapshot(); got != [3]int64{1, 1, 0} {
		t.Fatalf("warm cache unexpectedly fetched upstream: %v", got)
	}

	update(models.JSONSlice{"key-denied"})
	list(key) // No intersection must encode data:[], never null or all models.
	denied(key, "model-a", http.StatusForbidden, "gateway_model_not_allowed")

	update(models.JSONSlice{"model-a", "model-b", "key-denied", "global-denied"})
	list(key, "model-a", "model-b")
	denied(key, "key-denied", http.StatusForbidden, "gateway_model_not_allowed")
	denied(key, "global-denied", http.StatusNotFound, "model_unavailable")
	list(otherKey, "model-b")

	update(models.JSONSlice{"model-a"})
	clock.Add(int64(6 * time.Minute)) // Exceeds WhiteLabel's five-minute catalog TTL.
	before := calls.snapshot()
	list(key, "model-a")
	if got := calls.snapshot(); got != [3]int64{before[0] + 1, before[1], before[2]} {
		t.Fatalf("expected a successful catalog refresh, before=%v after=%v", before, got)
	}
	denied(key, "model-b", http.StatusForbidden, "gateway_model_not_allowed")
	list(otherKey, "model-b")
	list(key, "model-a")
}

func TestGatewayOwnerACLAuthenticationUnavailableHTTPEnvelope(t *testing.T) {
	state, calls, _ := gatewayOwnerHTTPState(t)
	owner := gatewayOwnerHTTPUser(t, state, models.UserRoleUser, nil)
	secret := gatewayOwnerHTTPKey(t, state, owner, nil)
	sqlDB, err := state.DB.DB()
	if err != nil {
		t.Fatal("get fixture database connection failed")
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal("close owned fixture connection failed")
	}
	for _, dependency := range []string{"closed-database", "missing-service"} {
		t.Run(dependency, func(t *testing.T) {
			if dependency == "missing-service" {
				state.GatewayTokens = nil
			}
			engine := router.New(state)
			for _, route := range []struct{ method, path, body string }{
				{http.MethodGet, "/v1/models", ""},
				{http.MethodGet, "/v1/models/detail?id=model-a", ""},
				{http.MethodGet, "/v1/models/model-a", ""},
				{http.MethodPost, "/v1/chat/completions", `{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"stream":false}`},
				{http.MethodPost, "/v1/chat/completions", `{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"stream":true}`},
			} {
				rec := gatewayOwnerHTTPRequest(engine, route.method, route.path, route.body, secret)
				gatewayOwnerAssertError(t, rec, http.StatusServiceUnavailable, "gateway_authentication_unavailable")
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal("503 response was not JSON")
				}
				want := map[string]any{"error": map[string]any{
					"code": "gateway_authentication_unavailable", "type": "api_error",
					"message":    "Gateway authentication is temporarily unavailable.",
					"request_id": rec.Header().Get("X-Request-ID"),
				}}
				if !reflect.DeepEqual(body, want) {
					t.Fatal("503 response was not the exact sanitized authentication envelope")
				}
				for _, private := range []string{secret, state.Settings.DatabaseURL, state.Settings.RedisURL, "sql:", "SELECT", "127.0.0.1"} {
					if private != "" && strings.Contains(rec.Body.String(), private) {
						t.Fatal("503 response exposed private dependency details")
					}
				}
				if got := calls.snapshot(); got != [3]int64{} {
					t.Fatalf("authentication failure contacted upstream: %v", got)
				}
			}
		})
	}
}

type gatewayOwnerHTTPCalls struct{ catalog, detail, chat atomic.Int64 }

func (c *gatewayOwnerHTTPCalls) snapshot() [3]int64 {
	return [3]int64{c.catalog.Load(), c.detail.Load(), c.chat.Load()}
}

// Connect only to explicit disposable fixtures; never migrate, clean tables,
// flush Redis, or derive credentials from the application's production env.
func gatewayOwnerHTTPState(t *testing.T) (*app.State, *gatewayOwnerHTTPCalls, *atomic.Int64) {
	t.Helper()
	databaseURL, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Skip("requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed == nil || (!strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") && parsed.Path != "/porsche_test") {
		t.Fatal("TEST_DATABASE_URL must identify a disposable test database")
	}
	gdb, err := db.Open(databaseURL, "test")
	if err != nil {
		t.Fatal("open isolated MySQL fixture failed")
	}
	gdb = gdb.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal("get isolated MySQL fixture connection failed")
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var actual string
	if err := gdb.Raw("SELECT DATABASE()").Scan(&actual).Error; err != nil || actual != strings.TrimPrefix(parsed.Path, "/") {
		t.Fatal("connected database does not match the explicit fixture")
	}
	settings := &config.Settings{AppEnv: "test", DatabaseURL: databaseURL, RedisURL: redisURL, AllowedHosts: "example.com",
		JWTSecretKey: "owner-http-fixture-jwt", AuthHMACKey: "owner-http-fixture-hmac-key-0123456789",
		SessionDays: 1, SessionMaxActive: 10, SessionIssueLimit24h: 30, RefreshReplaySeconds: 30,
	}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal("initialize isolated application fixture failed")
	}
	t.Cleanup(func() { _ = state.AuthRedis.Close() })
	calls := &gatewayOwnerHTTPCalls{}
	clock := &atomic.Int64{}
	clock.Store(time.Now().UnixNano())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/models":
			calls.catalog.Add(1)
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"},{"id":"key-denied"},{"id":"global-denied"}]}`))
		case strings.HasPrefix(r.URL.Path, "/models/"):
			calls.detail.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": strings.TrimPrefix(r.URL.Path, "/models/")})
		default:
			calls.chat.Add(1)
			http.Error(w, "unexpected generation request", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(upstream.Close)
	state.WhiteLabel, err = whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{
		BaseURL: upstream.URL, APIKey: "owner-http-fake-upstream",
		AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}, "key-denied": {}},
	}, upstream.Client(), func() time.Time { return time.Unix(0, clock.Load()) })
	if err != nil {
		t.Fatal("initialize fake WhiteLabel upstream failed")
	}
	gin.SetMode(gin.TestMode)
	return state, calls, clock
}

func gatewayOwnerHTTPUser(t *testing.T, state *app.State, role models.UserRole, acl models.JSONSlice) *models.User {
	t.Helper()
	user := gatewayWhiteLabelUser(t, state, "")
	user.Role, user.AuthVersion, user.AllowedModels = role, 1, acl
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal("create unique owner HTTP fixture user failed")
	}
	return user
}

func gatewayOwnerHTTPKey(t *testing.T, state *app.State, owner *models.User, acl models.JSONSlice) string {
	t.Helper()
	_, secret, err := state.GatewayTokens.Create(owner, service.GatewayTokenCreateInput{Name: "owner-http-fixture", AllowedModels: acl})
	if err != nil {
		t.Fatal("create unique owner HTTP fixture key failed")
	}
	return secret
}

func gatewayOwnerHTTPRequest(engine http.Handler, method, path, body, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func gatewayOwnerAssertCatalog(t *testing.T, rec *httptest.ResponseRecorder, want []string) {
	t.Helper()
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("catalog status=%d cache-control=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Data == nil {
		t.Fatal("catalog data must be a non-null JSON array")
	}
	got := make([]string, 0, len(body.Data))
	for _, item := range body.Data {
		got = append(got, item.ID)
	}
	sort.Strings(got)
	want = append([]string{}, want...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog ids=%v, want %v", got, want)
	}
}

func gatewayOwnerAssertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status=%d response was not an error JSON envelope", rec.Code)
	}
	if rec.Code != status || body.Error.Code != code {
		t.Fatalf("status=%d code=%q, want %d %q", rec.Code, body.Error.Code, status, code)
	}
	if body.Error.RequestID == "" || body.Error.RequestID != rec.Header().Get("X-Request-ID") {
		t.Fatal("error envelope request_id did not match the generated response header")
	}
}
