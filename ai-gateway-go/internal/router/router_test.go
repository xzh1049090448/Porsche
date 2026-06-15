package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/router"
)

func TestHealthOK(t *testing.T) {
	settings := &config.Settings{
		AppEnv:              "development",
		DatabaseURL:         "sqlite://./data/test_platform.db",
		ModelsConfigPath:    "../../config/models.yaml",
		ClientsConfigPath:   "../../config/clients.yaml",
		JWTSecretKey:        "test-secret",
		AdminToken:          "admin-test",
		PlatformClientSecret: "sk-platform-internal",
		FixedLoginEnabled:   true,
		FixedLoginPhone:     "13800138000",
		FixedLoginPassword:  "test",
		ChromaPersistDir:    "./data/test_chroma",
		DatasetUploadDir:    "./data/test_uploads",
		EnvKeys:             map[string]string{},
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
	if models, ok := data["models_loaded"].(float64); !ok || models < 1 {
		t.Fatalf("expected models_loaded >= 1, got %v", data["models_loaded"])
	}
}
