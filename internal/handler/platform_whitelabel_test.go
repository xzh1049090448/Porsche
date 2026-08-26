package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

type platformRoundTripper func(*http.Request) (*http.Response, error)

func (f platformRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestPlatformModelsUseWhiteLabelCatalogAndUserACL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newPlatformWhiteLabelTestState(t)
	user := platformTestUser("13900139003", models.JSONSlice{"model-a"})
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/models", nil)
	req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, &user))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"data":[{"id":"model-a"`) || strings.Contains(rec.Body.String(), "model-b") {
		t.Fatalf("unexpected catalog body=%s", rec.Body.String())
	}
}

func TestPlatformModelDetailHidesUnauthorizedAs404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newPlatformWhiteLabelTestState(t)
	user := platformTestUser("13900139004", models.JSONSlice{"model-a"})
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/models/model-b", nil)
	req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, &user))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestModelIDFromDetailQueryPreservesLegacyDetailAndEmptyQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		url  string
		want string
	}{
		{url: "/models/detail", want: "detail"},
		{url: "/models/detail?id=", want: ""},
		{url: "/models/detail?id=zai-org%2Fglm-5.1", want: "zai-org/glm-5.1"},
		{url: "/models/detail?id=one&id=two", want: ""},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = req
		if got := modelIDFromDetailQuery(ctx); got != tc.want {
			t.Fatalf("url=%s model ID=%q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestModelIDFromDetailQueryRejectsMalformedEscapesWithoutLegacyFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodGet, "/models/detail", nil)
	req.URL.RawQuery = "id=%zz"
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req
	if got := modelIDFromDetailQuery(ctx); got != "" {
		t.Fatalf("malformed query model ID=%q, want rejected empty ID", got)
	}
}

func TestPlatformDetailRoutePreservesLegacyDetailModelID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const modelID = "detail"
	state, detailCalls := newSlashPlatformWhiteLabelTestState(t, modelID)
	user := platformTestUser("13900139008", models.JSONSlice{modelID})
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/models/detail", nil)
	req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, &user))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"detail"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := detailCalls.Load(); got != 1 {
		t.Fatalf("detail upstream calls=%d, want 1", got)
	}
}

func TestPlatformSlashModelDetailUsesQueryIDAndUserACL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const modelID = "zai-org/glm-5.1"
	state, detailCalls := newSlashPlatformWhiteLabelTestState(t, modelID)
	user := platformTestUser("13900139006", models.JSONSlice{modelID})
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/models/detail?id=zai-org%2Fglm-5.1", nil)
	req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, &user))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"zai-org/glm-5.1"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := detailCalls.Load(); got != 1 {
		t.Fatalf("detail upstream calls=%d, want 1", got)
	}
}

func TestPlatformSlashModelDetailDoesNotCallUpstreamWhenUserDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const modelID = "zai-org/glm-5.1"
	state, detailCalls := newSlashPlatformWhiteLabelTestState(t, modelID)
	user := platformTestUser("13900139007", models.JSONSlice{"model-a"})
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/models/detail?id=zai-org%2Fglm-5.1", nil)
	req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, &user))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := detailCalls.Load(); got != 0 {
		t.Fatalf("detail upstream calls=%d, want 0", got)
	}
}

func TestPlatformMalformedOrDuplicateDetailQueryDoesNotCallUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const modelID = "zai-org/glm-5.1"
	state, detailCalls := newSlashPlatformWhiteLabelTestState(t, modelID)
	user := platformTestUser("13900139009", models.JSONSlice{modelID})
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	for _, rawQuery := range []string{"id=%zz", "id=zai-org%2Fglm-5.1&id=zai-org%2Fglm-5.1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/models/detail", nil)
		req.URL.RawQuery = rawQuery
		req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, &user))
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("query=%q status=%d body=%s", rawQuery, rec.Code, rec.Body.String())
		}
	}
	if got := detailCalls.Load(); got != 0 {
		t.Fatalf("detail upstream calls=%d, want 0", got)
	}
}

// A failure before the platform emits an SSE frame must remain a normal JSON
// API error, so clients can distinguish it from a truncated live stream.
func TestPlatformStreamFailureBeforeFirstFrameReturnsJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newPlatformWhiteLabelTestState(t)
	user := platformTestUser("13900139005", nil)
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"hi"}],"max_tokens":5,"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, &user))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"error"`) || strings.Contains(rec.Body.String(), "data:") {
		t.Fatalf("expected pre-frame JSON 503, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON content type before first frame, got %q", contentType)
	}
}

func TestAdminHealthCheckRejectsConcurrentSameModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newPlatformWhiteLabelTestState(t)
	state.Settings.AdminToken = "admin-test"
	modelHealthChecks.Store("model-a", struct{}{})
	t.Cleanup(func() { modelHealthChecks.Delete("model-a") })
	engine := gin.New()
	RegisterAdmin(engine, state)
	req := httptest.NewRequest(http.MethodPost, "/admin/models/model-a/health-check", nil)
	req.Header.Set("Authorization", "Bearer admin-test")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "health_check_in_progress") {
		t.Fatalf("expected 409 concurrent health check, status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminSlashHealthCheckUsesQueryIDForExactLock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const modelID = "zai-org/glm-5.1"
	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://white-label.test/v1", APIKey: "test-key", AllowedModels: map[string]struct{}{modelID: {}}}, http.DefaultClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &app.State{Settings: &config.Settings{AdminToken: "admin-test"}, WhiteLabel: whiteLabel}
	modelHealthChecks.Store(modelID, struct{}{})
	t.Cleanup(func() { modelHealthChecks.Delete(modelID) })
	engine := gin.New()
	RegisterAdmin(engine, state)
	req := httptest.NewRequest(http.MethodPost, "/admin/models/health-check?id=zai-org%2Fglm-5.1", nil)
	req.Header.Set("Authorization", "Bearer admin-test")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "health_check_in_progress") {
		t.Fatalf("expected 409 concurrent health check, status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminHealthCheckRejectsMalformedQueryID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://white-label.test/v1", APIKey: "test-key", AllowedModels: map[string]struct{}{"model-a": {}}}, http.DefaultClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &app.State{Settings: &config.Settings{AdminToken: "admin-test"}, WhiteLabel: whiteLabel}
	engine := gin.New()
	RegisterAdmin(engine, state)
	req := httptest.NewRequest(http.MethodPost, "/admin/models/health-check", nil)
	req.URL.RawQuery = "id=%zz"
	req.Header.Set("Authorization", "Bearer admin-test")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func newPlatformWhiteLabelTestState(t *testing.T) *app.State {
	t.Helper()
	settings := &config.Settings{AppEnv: "test", DatabaseURL: testDatabaseURL(t), JWTSecretKey: "test-secret"}
	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal(err)
	}
	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://white-label.test/v1", APIKey: "test-key", AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}}}, &http.Client{Transport: platformRoundTripper(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"model-a","title":"Model A"},{"id":"model-b","title":"Model B"}]}`
		if strings.HasSuffix(req.URL.Path, "/models/model-a") {
			body = `{"id":"model-a","title":"Model A"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.WhiteLabel = whiteLabel
	state.Platform = service.NewPlatformChatService(service.PlatformDeps{
		Settings: settings, DB: state.DB, Billing: state.Billing, WhiteLabel: whiteLabel,
	})
	return state
}

func newSlashPlatformWhiteLabelTestState(t *testing.T, modelID string) (*app.State, *atomic.Int64) {
	t.Helper()
	settings := &config.Settings{AppEnv: "test", DatabaseURL: testDatabaseURL(t), JWTSecretKey: "test-secret"}
	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal(err)
	}
	var detailCalls atomic.Int64
	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://white-label.test/v1", APIKey: "test-key", AllowedModels: map[string]struct{}{modelID: {}}}, &http.Client{Transport: platformRoundTripper(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"` + modelID + `","title":"Model"}]}`
		if strings.HasSuffix(req.URL.EscapedPath(), "/models/"+modelID) || strings.HasSuffix(req.URL.EscapedPath(), "/models/zai-org%2Fglm-5.1") {
			detailCalls.Add(1)
			body = `{"id":"` + modelID + `","title":"Model"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.WhiteLabel = whiteLabel
	return state, &detailCalls
}

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires isolated TEST_DATABASE_URL MySQL fixture")
	}
	return url
}

func platformJWT(t *testing.T, state *app.State, user *models.User) string {
	t.Helper()
	token, err := security.CreateAccessToken(strconv.FormatInt(user.Guid, 10), state.Settings.JWTSecretKey, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

var platformTestSnowflake = persistence.NewSnowflake(os.Getpid()%1024, persistence.SystemClock())

func platformTestUser(_ string, allowed models.JSONSlice) models.User {
	now := time.Now().UTC().UnixMilli()
	if allowed == nil {
		allowed = models.JSONSlice{}
	}
	return models.User{AuditFields: models.AuditFields{Guid: platformTestSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0}, Phone: platformTestPhone(), Status: models.UserStatusActive, PlanType: models.PlanFree, AllowedModels: allowed}
}

// platformTestPhone keeps handler fixtures isolated even when MySQL data from
// a prior `go test` process remains in the dedicated test database.
func platformTestPhone() string {
	return strconv.FormatInt(13_000_000_000+platformTestSnowflake.Next()%1_000_000_000, 10)
}
