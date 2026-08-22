package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/router"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

func TestGatewayModelsUseTokenACLAndDynamicCatalog(t *testing.T) {
	state, upstream, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a","owned_by":"white"},{"id":"model-b"}]}`)
	user := &models.User{Phone: "13900200001", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "catalog", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "model-a" {
		t.Fatalf("catalog=%s", rec.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls=%d, want 1", got)
	}

	detail := httptest.NewRequest(http.MethodGet, "/v1/models/model-b", nil)
	detail.Header.Set("Authorization", "Bearer "+secret)
	detailRec := httptest.NewRecorder()
	router.New(state).ServeHTTP(detailRec, detail)
	if detailRec.Code != http.StatusNotFound {
		t.Fatalf("unauthorized detail status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}
	_ = upstream
}

func TestGatewayChatRejectsBeforeWhiteLabelUpstream(t *testing.T) {
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200002", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "chat", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"model":"model-b","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`,
		`{"model":"model-a","messages":[{"role":"user","content":"hello"}]}`,
		string(bytes.Repeat([]byte("x"), whitelabel.MaxRequestBodyBytes+1)),
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.New(state).ServeHTTP(rec, req)
		if rec.Code < 400 {
			t.Fatalf("invalid request accepted: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d, want 0", got)
	}
}

func TestGatewaySSEPostFirstChunkEmitsErrorAndDone(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200003", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "stream", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !bytes.Contains([]byte(got), []byte("data: first\n\n")) || !bytes.Contains([]byte(got), []byte("event: error\n")) || !bytes.Contains([]byte(got), []byte("data: [DONE]\n\n")) {
		t.Fatalf("SSE boundary = %q", got)
	}
}

func gatewayWhiteLabelState(t *testing.T, catalog string) (*app.State, *httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(catalog))
		case "/chat/completions":
			if r.URL.Query().Get("fail_before") != "" {
				http.Error(w, "secret upstream failure", http.StatusBadGateway)
				return
			}
			if bytes.Contains(mustRead(t, r), []byte(`"stream":true`)) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: first\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"safe"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	settings := &config.Settings{AppEnv: "test", DatabaseURL: "sqlite://" + dir + "/gateway.db", AllowedHosts: "example.com", ModelsConfigPath: "../../config/models.yaml", ClientsConfigPath: "../../config/clients.yaml", JWTSecretKey: "test", ChromaPersistDir: dir + "/chroma", DatasetUploadDir: dir + "/uploads", EnvKeys: map[string]string{}}
	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal(err)
	}
	state.WhiteLabel, err = whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: upstream.URL, APIKey: "test-key", AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}}}, upstream.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return state, upstream, &calls
}

func mustRead(t *testing.T, r *http.Request) []byte {
	t.Helper()
	defer r.Body.Close()
	var b bytes.Buffer
	if _, err := b.ReadFrom(r.Body); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
