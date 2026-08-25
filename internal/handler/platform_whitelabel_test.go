package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

type platformRoundTripper func(*http.Request) (*http.Response, error)

func (f platformRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestPlatformModelsUseWhiteLabelCatalogAndUserACL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newPlatformWhiteLabelTestState(t)
	user := &models.User{Phone: "13900139003", Status: models.UserStatusActive, AllowedModels: models.JSONSlice{"model-a"}}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/models", nil)
	req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, user))
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
	user := &models.User{Phone: "13900139004", Status: models.UserStatusActive, AllowedModels: models.JSONSlice{"model-a"}}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/models/model-b", nil)
	req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, user))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// A failure before the platform emits an SSE frame must remain a normal JSON
// API error, so clients can distinguish it from a truncated live stream.
func TestPlatformStreamFailureBeforeFirstFrameReturnsJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newPlatformWhiteLabelTestState(t)
	user := &models.User{Phone: "13900139005", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterPlatform(engine, state)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"hi"}],"max_tokens":5,"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, user))
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

func newPlatformWhiteLabelTestState(t *testing.T) *app.State {
	t.Helper()
	settings := &config.Settings{AppEnv: "test", DatabaseURL: "mysql://test:test@localhost:3306/platform", JWTSecretKey: "test-secret"}
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

func platformJWT(t *testing.T, state *app.State, user *models.User) string {
	t.Helper()
	token, err := security.CreateAccessToken(strconv.Itoa(user.ID), state.Settings.JWTSecretKey, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
