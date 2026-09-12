package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/openaicompat"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/router"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

type gatewayRoundTripper func(*http.Request) (*http.Response, error)

func (f gatewayRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestGatewayModelsUseTokenACLAndDynamicCatalog(t *testing.T) {
	state, upstream, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a","owned_by":"white"},{"id":"model-b"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200001")
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

func TestGatewaySlashModelDetailUsesQueryIDAndTokenACL(t *testing.T) {
	const modelID = "zai-org/glm-5.1"
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"zai-org/glm-5.1"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200013")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "slash-detail", AllowedModels: models.JSONSlice{modelID}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models/detail?id=zai-org%2Fglm-5.1", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"zai-org/glm-5.1"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls=%d, want catalog plus detail", got)
	}
}

func TestGatewaySlashModelDetailDoesNotCallUpstreamWhenTokenDenied(t *testing.T) {
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"zai-org/glm-5.1"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200014")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "slash-denied", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models/detail?id=zai-org%2Fglm-5.1", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d, want 0", got)
	}
}

func TestGatewayMalformedOrDuplicateDetailQueryDoesNotCallUpstream(t *testing.T) {
	const modelID = "zai-org/glm-5.1"
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"zai-org/glm-5.1"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200016")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "malformed-detail", AllowedModels: models.JSONSlice{modelID}})
	if err != nil {
		t.Fatal(err)
	}
	for _, rawQuery := range []string{"id=%zz", "id=zai-org%2Fglm-5.1&id=zai-org%2Fglm-5.1"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models/detail", nil)
		req.URL.RawQuery = rawQuery
		req.Header.Set("Authorization", "Bearer "+secret)
		rec := httptest.NewRecorder()
		router.New(state).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("query=%q status=%d body=%s", rawQuery, rec.Code, rec.Body.String())
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d, want 0", got)
	}
}

func TestGatewayDetailRoutePreservesLegacyDetailModelID(t *testing.T) {
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"detail"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200015")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "legacy-detail", AllowedModels: models.JSONSlice{"detail"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models/detail", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"detail"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls=%d, want catalog plus detail", got)
	}
}

func TestGatewayChatRejectsBeforeWhiteLabelUpstream(t *testing.T) {
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200002")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "chat", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"model":"model-b","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`,
		`{"model":"model-a","messages":[{"role":"tool","tool_call_id":"missing","content":"hello"}]}`,
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

func TestGatewayResponsesRejectsStateBeforeUpstream(t *testing.T) {
	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200021")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "responses", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"model":"model-a","input":"hello","store":true}`,
		`{"model":"model-a","input":"hello","previous_response_id":"resp_1"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.New(state).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"unsupported_parameter"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d", got)
	}
}

func TestGatewayToolPayloadLimitsRejectBeforeUpstream(t *testing.T) {
	tests := []struct {
		name string
		path string
		body map[string]any
	}{
		{
			name: "chat arguments", path: "/v1/chat/completions",
			body: map[string]any{"model": "model-a", "messages": []any{
				map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
					map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": strings.Repeat("a", openaicompat.MaxArgumentsBytes+1)}},
				}},
			}},
		},
		{
			name: "chat output", path: "/v1/chat/completions",
			body: map[string]any{"model": "model-a", "messages": []any{
				map[string]any{"role": "tool", "tool_call_id": "call_1", "content": strings.Repeat("o", openaicompat.MaxToolOutputBytes+1)},
			}},
		},
		{
			name: "responses arguments", path: "/v1/responses",
			body: map[string]any{"model": "model-a", "input": []any{
				map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": strings.Repeat("a", openaicompat.MaxArgumentsBytes+1)},
			}},
		},
		{
			name: "responses output", path: "/v1/responses",
			body: map[string]any{"model": "model-a", "input": []any{
				map[string]any{"type": "function_call_output", "call_id": "call_1", "output": strings.Repeat("o", openaicompat.MaxToolOutputBytes+1)},
			}},
		},
	}
	encoded := make([][]byte, len(tests))
	for _, tt := range tests {
		t.Run("decoder "+tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatal(err)
			}
			if len(body) >= openaicompat.MaxRequestBodyBytes {
				t.Fatalf("test body=%d exceeds request body boundary", len(body))
			}
			var decodeErr *openaicompat.Error
			if tt.path == "/v1/responses" {
				_, decodeErr = openaicompat.DecodeResponses(body)
			} else {
				_, decodeErr = openaicompat.DecodeChat(body)
			}
			if decodeErr == nil || decodeErr.Status != http.StatusRequestEntityTooLarge || decodeErr.Code != "request_too_large" {
				t.Fatalf("decoder error=%#v", decodeErr)
			}
		})
	}
	for i, tt := range tests {
		body, err := json.Marshal(tt.body)
		if err != nil {
			t.Fatal(err)
		}
		encoded[i] = body
	}
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Log("TEST_DATABASE_URL unset: decoder boundary evidence passed; authenticated Gin zero-upstream chain not run")
		return
	}

	state, _, calls := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200025")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "tool-size", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	for i, tt := range tests {
		t.Run("gateway "+tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(encoded[i]))
			req.Header.Set("Authorization", "Bearer "+secret)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.New(state).ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), `"code":"request_too_large"`) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d, want 0", got)
	}
}

func TestGatewayResponsesCancellationStopsUpstream(t *testing.T) {
	assertWhiteLabelCancellation := func(t *testing.T) {
		t.Helper()
		started := make(chan struct{})
		canceled := make(chan struct{})
		client := &http.Client{Transport: gatewayRoundTripper(func(req *http.Request) (*http.Response, error) {
			close(started)
			<-req.Context().Done()
			close(canceled)
			return nil, req.Context().Err()
		})}
		whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://white-label.test/v1", APIKey: "test-key", AllowedModels: map[string]struct{}{"model-a": {}}}, client, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			response, _ := whiteLabel.Chat(ctx, []byte(`{"model":"model-a","messages":[{"role":"user","content":"x"}]}`))
			if response != nil {
				response.Body.Close()
			}
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("low-level upstream request did not start")
		}
		cancel()
		select {
		case <-canceled:
		case <-time.After(2 * time.Second):
			t.Fatal("low-level upstream context was not canceled")
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("low-level WhiteLabel.Chat did not return after cancellation")
		}
	}

	// This evidence is independent of the optional MySQL fixture.
	assertWhiteLabelCancellation(t)
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Log("TEST_DATABASE_URL unset: lower-level WhiteLabel cancellation evidence passed; Gin chain not run")
		return
	}

	started := make(chan struct{})
	canceled := make(chan struct{})
	client := &http.Client{Transport: gatewayRoundTripper(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/models":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"}]}`)), Request: req}, nil
		case "/v1/chat/completions":
			close(started)
			<-req.Context().Done()
			close(canceled)
			return nil, req.Context().Err()
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found")), Request: req}, nil
		}
	})}
	settings := &config.Settings{AppEnv: "test", DatabaseURL: os.Getenv("TEST_DATABASE_URL"), AllowedHosts: "example.com", JWTSecretKey: "test"}
	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal(err)
	}
	state.WhiteLabel, err = whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://white-label.test/v1", APIKey: "test-key", AllowedModels: map[string]struct{}{"model-a": {}}}, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	user := gatewayWhiteLabelUser(t, state, "13900200026")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "cancel-responses", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.WhiteLabel.ListModels(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-a","input":"wait","stream":true}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.New(state).ServeHTTP(rec, req)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Gin gateway did not reach upstream")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("Gin cancellation did not cancel upstream request context")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Gin gateway did not return after cancellation")
	}
}

func TestGatewayChatToolRoundTrip(t *testing.T) {
	first := mustFixture(t, "opencode-chat-request.json")
	second := `{"model":"model-a","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"not-json"}},{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_2","content":"two"},{"role":"tool","tool_call_id":"call_1","content":"one"}]}`
	runGatewayToolRoundTrip(t, "/v1/chat/completions", first, second, `"tool_calls"`, `"content":"hello"`)
}

func TestGatewayResponsesToolRoundTrip(t *testing.T) {
	first := mustFixture(t, "opencode-responses-request.json")
	second := `{"model":"model-a","input":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"not-json"},{"type":"function_call","call_id":"call_2","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"call_2","output":"two"},{"type":"function_call_output","call_id":"call_1","output":"one"}],"store":false}`
	runGatewayToolRoundTrip(t, "/v1/responses", first, second, `"type":"function_call"`, `"type":"output_text"`)
}

func TestGatewayResponsesStreamUsesResponsesEventsWithoutDoneSentinel(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200023")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "responses-stream", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-a","input":"responses-stream-ok","stream":true,"store":false}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	got := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(got, "event: response.created\n") || !strings.Contains(got, "event: response.completed\n") || strings.Contains(got, "[DONE]") {
		t.Fatalf("status=%d body=%s", rec.Code, got)
	}
}

func TestGatewayResponsesPostStartFailureEmitsFailed(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200024")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "responses-fail", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-a","input":"responses-stream-fail","stream":true,"store":false}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	got := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Count(got, "event: response.failed\n") != 1 || strings.Contains(got, "event: response.completed\n") || strings.Contains(got, "[DONE]") {
		t.Fatalf("status=%d body=%s", rec.Code, got)
	}
}

func runGatewayToolRoundTrip(t *testing.T, path, first, second, firstWant, secondWant string) {
	t.Helper()
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200022")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "tools", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.New(state).ServeHTTP(rec, req)
		return rec
	}
	firstResponse := request(first)
	if firstResponse.Code != http.StatusOK || !strings.Contains(firstResponse.Body.String(), firstWant) {
		t.Fatalf("first status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	secondResponse := request(second)
	if secondResponse.Code != http.StatusOK || !strings.Contains(secondResponse.Body.String(), secondWant) {
		t.Fatalf("second status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
}

func mustFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestGatewaySSEPostFirstChunkEmitsErrorAndDone(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200003")
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
	var errorEnvelope struct {
		Error struct {
			Code      string `json:"code"`
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	errorData := got[errorFrame+len("event: error\n") : doneFrame]
	errorData = strings.TrimPrefix(errorData, "data: ")
	errorData = strings.TrimSpace(errorData)
	if err := json.Unmarshal([]byte(errorData), &errorEnvelope); err != nil {
		t.Fatalf("post-first error is not a JSON envelope: %v; data=%q", err, errorData)
	}
	if errorEnvelope.Error.Code != "gateway_upstream_unavailable" || errorEnvelope.Error.Type != "api_error" || errorEnvelope.Error.RequestID == "" {
		t.Fatalf("unexpected post-first error envelope: %s", errorData)
	}
}

func TestGatewaySSEProjectsChunksAndDropsUpstreamFields(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200011")
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
	user := gatewayWhiteLabelUser(t, state, "13900200012")
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
	user := gatewayWhiteLabelUser(t, state, "13900200008")
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
	user := gatewayWhiteLabelUser(t, state, "13900200004")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	revoked, revokedSecret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "revoked", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.GatewayTokens.Revoke(user.ID, revoked.Guid); err != nil {
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
		wantType := "authentication_error"
		if rec.Code == http.StatusForbidden {
			wantType = "permission_error"
		}
		assertGatewayError(t, rec, wantType)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d, want 0", got)
	}
}

func TestGatewayChatRequiresExactJSONMediaTypeAndStableErrors(t *testing.T) {
	state, _, _ := gatewayWhiteLabelState(t, `{"data":[{"id":"model-a"}]}`)
	user := gatewayWhiteLabelUser(t, state, "13900200005")
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
	user := gatewayWhiteLabelUser(t, state, "13900200009")
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
	user := gatewayWhiteLabelUser(t, state, "13900200006")
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
	user := gatewayWhiteLabelUser(t, state, "13900200007")
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
	user := gatewayWhiteLabelUser(t, state, "13900200010")
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
		case "/models/zai-org/glm-5.1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"zai-org/glm-5.1"}`))
		case "/models/detail":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"detail"}`))
		case "/chat/completions":
			if r.URL.Query().Get("fail_before") != "" {
				http.Error(w, "secret upstream failure", http.StatusBadGateway)
				return
			}
			body := mustRead(t, r)
			if bytes.Contains(body, []byte(`"stream":true`)) {
				w.Header().Set("Content-Type", "text/event-stream")
				if bytes.Contains(body, []byte(`responses-stream-ok`)) {
					_, _ = w.Write([]byte("data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
					return
				}
				if bytes.Contains(body, []byte(`responses-stream-fail`)) {
					_, _ = w.Write([]byte("data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"},\"finish_reason\":null}]}\n\ndata: not-json\n\n"))
					return
				}
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
			if bytes.Contains(body, []byte(`tools-please`)) {
				_, _ = w.Write([]byte(`{"id":"safe","object":"chat.completion","created":1,"model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"not-json"}},{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"safe","object":"chat.completion","created":1,"model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3},"internal":"upstream-secret"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	settings := &config.Settings{AppEnv: "test", DatabaseURL: testDatabaseURL(t), AllowedHosts: "example.com", JWTSecretKey: "test"}
	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal(err)
	}
	state.WhiteLabel, err = whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: upstream.URL, APIKey: "test-key", AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}, "detail": {}, "zai-org/glm-5.1": {}}}, upstream.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return state, upstream, &calls
}

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires isolated TEST_DATABASE_URL MySQL fixture")
	}
	return url
}

var gatewayWhiteLabelSnowflake = persistence.NewSnowflake(os.Getpid()%1024, persistence.SystemClock())

func gatewayWhiteLabelUser(t *testing.T, state *app.State, _ string) *models.User {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	return &models.User{
		AuditFields:   models.AuditFields{Guid: gatewayWhiteLabelSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0},
		GroupID:       gatewayWhiteLabelDefaultBusinessGroupID(t, state),
		Phone:         gatewayWhiteLabelTestPhone(),
		Status:        models.UserStatusActive,
		PlanType:      models.PlanFree,
		AllowedModels: models.JSONSlice{},
	}
}

func gatewayWhiteLabelDefaultBusinessGroupID(t *testing.T, state *app.State) int64 {
	t.Helper()
	var groups []models.BusinessGroup
	if state == nil || state.DB == nil {
		t.Fatal("gateway white-label test state has no database")
	}
	if err := state.DB.Where("group_key = ? AND is_deleted = 0", "default").Order("id ASC").Find(&groups).Error; err != nil {
		t.Fatalf("load gateway white-label default business group: %v", err)
	}
	if len(groups) != 1 || groups[0].ID <= 0 || groups[0].Guid <= 0 || groups[0].Key != "default" || groups[0].Status != models.BusinessGroupStatusActive || groups[0].IsDeleted != 0 {
		t.Fatalf("invalid gateway white-label default business group: %#v", groups)
	}
	return groups[0].ID
}

// gatewayWhiteLabelTestPhone makes each persisted handler fixture distinct
// across repeated runs against the same dedicated MySQL test database.
func gatewayWhiteLabelTestPhone() *string {
	phone := strconv.FormatInt(13_000_000_000+gatewayWhiteLabelSnowflake.Next()%1_000_000_000, 10)
	return &phone
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
