package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"stream":true,"seed":6}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	if !bytes.Contains([]byte(got), []byte(`data: {"id":"safe","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{"content":"first"},"finish_reason":null}]}`)) || !bytes.Contains([]byte(got), []byte("event: error\n")) || !bytes.Contains([]byte(got), []byte("data: [DONE]\n\n")) {
		t.Fatalf("SSE boundary = %q", got)
	}
	first := strings.Index(got, `"content":"first"`)
	errorFrame := strings.Index(got, "event: error\n")
	doneFrame := strings.Index(got, "data: [DONE]\n\n")
	if first == -1 || errorFrame < first || doneFrame < errorFrame {
		t.Fatalf("expected post-first event:error followed by data:[DONE], got %q", got)
	}
}

func TestGatewaySSEProjectsChunksAndDropsUpstreamFields(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200011", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "stream-project", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(strings.Replace(validGatewayChatBody(true), `"seed":1`, `"seed":3`, 1)))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	for _, secret := range []string{"top-secret", "delta-secret", "tool-secret", "function-secret", "event-secret", "header-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("stream leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"model":"model-a"`) || !strings.Contains(got, `"content":"hello"`) || !strings.Contains(got, `"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}`) || !strings.Contains(got, "data: [DONE]\n\n") {
		t.Fatalf("allowed projection missing: %s", got)
	}
}

func TestGatewaySSEMalformedFirstChunkReturnsJSON503(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200012", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "stream-malformed", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(strings.Replace(validGatewayChatBody(true), `"seed":1`, `"seed":5`, 1)))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status=%d content-type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	assertGatewayError(t, rec, "api_error")
}

func TestGatewaySSEBeforeFirstPayloadReturnsJSONError(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200008", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "stream-first", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(strings.Replace(validGatewayChatBody(true), `"seed":1`, `"seed":0`, 1)))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status=%d content-type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	assertGatewayError(t, rec, "api_error")
}

func TestGatewayChatAuthenticatesBeforeReadingOrValidatingBody(t *testing.T) {
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200004", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	revoked, revokedSecret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "revoked", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.GatewayTokens.Revoke(user.ID, revoked.ID); err != nil {
		t.Fatal(err)
	}
	_, deniedSecret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "ip", AllowedModels: models.JSONSlice{"model-a"}, IPAllowlist: models.JSONSlice{"203.0.113.1"}})
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{"", "not-a-gateway-token", revokedSecret, deniedSecret} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("not json"))
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.New(state).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Fatalf("secret=%q status=%d body=%s", secret, rec.Code, rec.Body.String())
		}
		assertGatewayError(t, rec, "authentication_error")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d, want 0", got)
	}
}

func TestGatewayChatRequiresExactJSONMediaTypeAndStableErrors(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200005", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "content-type", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}

	for _, contentType := range []string{"", "application/jsonp"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(validGatewayChatBody(false)))
		req.Header.Set("Authorization", "Bearer "+secret)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		router.New(state).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("content-type=%q status=%d body=%s", contentType, rec.Code, rec.Body.String())
		}
		assertGatewayError(t, rec, "invalid_request_error")
	}
}

func TestGatewayChatKeepsAuthenticatedRequestBodyLimit(t *testing.T) {
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200009", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "size", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bytes.Repeat([]byte("x"), whitelabel.MaxRequestBodyBytes+1)))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertGatewayError(t, rec, "invalid_request_error")
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d, want 0", got)
	}
}

func TestGatewayChatRequiresCurrentCatalogAndEnabledModelBeforeChat(t *testing.T) {
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200006", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "catalog", AllowedModels: models.JSONSlice{"model-a", "model-b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.WhiteLabel.ListModels(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	baseline := calls.Load()
	for _, model := range []string{"model-b", "model-a"} {
		if model == "model-a" {
			// A trusted detail 404 marks the model disabled until catalog refresh.
			if _, detailErr := state.WhiteLabel.GetModel(context.Background(), model, nil); detailErr == nil {
				t.Fatal("expected detail 404 to disable model")
			}
			baseline = calls.Load()
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(strings.Replace(validGatewayChatBody(false), "model-a", model, 1)))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.New(state).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("model=%s status=%d body=%s", model, rec.Code, rec.Body.String())
		}
		assertGatewayError(t, rec, "invalid_request_error")
		if got := calls.Load(); got != baseline {
			t.Fatalf("model=%s upstream calls=%d, want %d", model, got, baseline)
		}
	}
}

func TestGatewayChatProjectsValidatedCompletionAndMasksUpstreamFields(t *testing.T) {
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200007", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "valid", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.WhiteLabel.ListModels(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(validGatewayChatBody(false)))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	var completion struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index int `json:"index"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || completion.ID != "safe" || completion.Object != "chat.completion" || completion.Created != 1 || completion.Model != "model-a" || len(completion.Choices) != 1 || completion.Choices[0].Index != 0 || completion.Usage.TotalTokens != 3 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("upstream-secret")) {
		t.Fatalf("upstream secret leaked: %s", rec.Body.String())
	}
	if got := calls.Load(); got != 2 { // catalog + chat
		t.Fatalf("upstream calls=%d, want 2", got)
	}
}

func TestGatewayChatRejectsMalformedUpstreamCompletion(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := &models.User{Phone: "13900200010", Status: models.UserStatusActive}
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "malformed", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.WhiteLabel.ListModels(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(strings.Replace(validGatewayChatBody(false), `"seed":1`, `"seed":2`, 1)))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertGatewayError(t, rec, "api_error")
}

func assertGatewayError(t *testing.T, rec *httptest.ResponseRecorder, wantType string) {
	t.Helper()
	var body struct {
		Error struct {
			Type      string `json:"type"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != wantType || body.Error.Code == "" || body.Error.RequestID == "" {
		t.Fatalf("unexpected error envelope: %s", rec.Body.String())
	}
}

func validGatewayChatBody(stream bool) string {
	return `{"model":"model-a","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"max_tokens":1,"n":1,"temperature":1,"top_p":1,"frequency_penalty":0,"presence_penalty":0,"stop":["END"],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"response_format":{"type":"text"},"stream_options":{"include_usage":true},"stream":` + strconv.FormatBool(stream) + `,"seed":1}`
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
			body := mustRead(t, r)
			if bytes.Contains(body, []byte(`"stream":true`)) {
				w.Header().Set("Content-Type", "text/event-stream")
				if bytes.Contains(body, []byte(`"seed":0`)) {
					return
				}
				if bytes.Contains(body, []byte(`"seed":3`)) {
					_, _ = w.Write([]byte("event: event-secret\nX-Upstream: header-secret\ndata: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"upstream\",\"top\":\"top-secret\",\"choices\":[{\"index\":0,\"finish_reason\":null,\"delta\":{\"content\":\"hello\",\"delta_secret\":\"delta-secret\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"tool_secret\":\"tool-secret\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\",\"function_secret\":\"function-secret\"}}]} }],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3,\"secret\":\"usage-secret\"}}\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				if bytes.Contains(body, []byte(`"seed":4`)) {
					_, _ = w.Write([]byte("data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"},\"finish_reason\":null}]}\n\ndata: not-json\n\n"))
					return
				}
				if bytes.Contains(body, []byte(`"seed":5`)) {
					_, _ = w.Write([]byte("data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"content\":123},\"finish_reason\":null}]}\n\n"))
					return
				}
				if bytes.Contains(body, []byte(`"seed":6`)) {
					_, _ = w.Write([]byte("data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"},\"finish_reason\":null}]}\n\n"))
					return
				}
				_, _ = w.Write([]byte("data: first\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if bytes.Contains(body, []byte(`"seed":2`)) {
				_, _ = w.Write([]byte(`{"id":"malformed"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"safe","object":"chat.completion","created":1,"model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3},"internal":"upstream-secret"}`))
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
