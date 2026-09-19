package handler_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

type harnessUpstreamRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
	calls  atomic.Int64
}

func (r *harnessUpstreamRecorder) record(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, append([]byte(nil), body...))
}

func (r *harnessUpstreamRecorder) last() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return nil
	}
	return r.bodies[len(r.bodies)-1]
}

// gatewayHarnessState builds a real app.State whose upstream records every
// forwarded body and returns a reasoning-bearing completion.
func gatewayHarnessState(t *testing.T, reasoning, passthrough string) (*app.State, *harnessUpstreamRecorder) {
	t.Helper()
	recorder := &harnessUpstreamRecorder{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.calls.Add(1)
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
		case "/chat/completions":
			body, _ := io.ReadAll(r.Body)
			recorder.record(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"safe","object":"chat.completion","created":1,"model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"cot"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"completion_tokens_details":{"reasoning_tokens":1},"prompt_tokens_details":{"cached_tokens":1}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	whiteLabelSettings, err := config.ParseWhiteLabelSettings("cn", "test-key", "model-a", reasoning, passthrough)
	if err != nil {
		t.Fatalf("ParseWhiteLabelSettings: %v", err)
	}
	whiteLabelSettings.BaseURL = upstream.URL
	settings := &config.Settings{AppEnv: "test", DatabaseURL: testDatabaseURL(t), AllowedHosts: "example.com", JWTSecretKey: "test", WhiteLabel: whiteLabelSettings}
	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal(err)
	}
	state.WhiteLabel, err = whitelabel.NewWhiteLabelService(whiteLabelSettings, upstream.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return state, recorder
}

func gatewayHarnessToken(t *testing.T, state *app.State) string {
	t.Helper()
	user := gatewayWhiteLabelUser(t, state, "harness")
	if err := state.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	_, secret, err := state.GatewayTokens.Create(user, service.GatewayTokenCreateInput{Name: "harness", AllowedModels: models.JSONSlice{"model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func postGatewayChat(t *testing.T, state *app.State, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.New(state).ServeHTTP(rec, req)
	return rec
}

const harnessChatBody = `{
	"model":"model-a",
	"messages":[{"role":"user","content":"hi"}],
	"reasoning_effort":"high",
	"thinking":{"type":"enabled"},
	"max_tokens":16,
	"stream":false
}`

func TestGatewayChatDeepSeekHarnessReasoningRoundTrip(t *testing.T) {
	state, recorder := gatewayHarnessState(t, "model-a", "")
	secret := gatewayHarnessToken(t, state)

	rec := postGatewayChat(t, state, secret, harnessChatBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	forwarded := recorder.last()
	for _, want := range []string{`"reasoning_effort":"high"`, `"thinking":{"type":"enabled"}`} {
		if !bytes.Contains(forwarded, []byte(want)) {
			t.Fatalf("upstream body missing %s: %s", want, forwarded)
		}
	}
	var completion struct {
		Choices []struct {
			Message struct {
				ReasoningContent *string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens             int `json:"total_tokens"`
			CompletionTokensDetails *struct {
				ReasoningTokens *int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &completion); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	if len(completion.Choices) != 1 || completion.Choices[0].Message.ReasoningContent == nil || *completion.Choices[0].Message.ReasoningContent != "cot" {
		t.Fatalf("response dropped reasoning_content: %s", rec.Body.String())
	}
	if completion.Usage.TotalTokens != 3 || completion.Usage.CompletionTokensDetails == nil || completion.Usage.CompletionTokensDetails.ReasoningTokens == nil || *completion.Usage.CompletionTokensDetails.ReasoningTokens != 1 {
		t.Fatalf("response usage details wrong: %s", rec.Body.String())
	}
}

func TestGatewayChatRejectsReasoningForUndeclaredModel(t *testing.T) {
	state, recorder := gatewayHarnessState(t, "", "")
	secret := gatewayHarnessToken(t, state)

	rec := postGatewayChat(t, state, secret, harnessChatBody)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"unsupported_parameter"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := recorder.calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestGatewayChatPassthroughSanitizesSensitiveFieldsAndAudits(t *testing.T) {
	state, recorder := gatewayHarnessState(t, "", "model-a")
	secret := gatewayHarnessToken(t, state)

	var logBuffer bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logBuffer)
	t.Cleanup(func() { log.SetOutput(previous) })

	body := `{"model":"model-a","messages":[{"role":"user","content":"prompt-sentinel"}],"max_tokens":16,"safety_field":"passthrough-safe","prompt_cache_key":"cache-sentinel","user":"user-sentinel","safety_identifier":"safety-sentinel","store":true,"stream":false}`
	rec := postGatewayChat(t, state, secret, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	forwarded := recorder.last()
	if !bytes.Contains(forwarded, []byte(`"safety_field":"passthrough-safe"`)) {
		t.Fatalf("passthrough did not forward the safe field: %s", forwarded)
	}
	for _, denied := range []string{"cache-sentinel", "user-sentinel", "safety-sentinel", `"store":true`} {
		if bytes.Contains(forwarded, []byte(denied)) {
			t.Fatalf("passthrough forwarded denied content %q: %s", denied, forwarded)
		}
	}
	logged := logBuffer.String()
	if !strings.Contains(logged, "gateway passthrough") || !strings.Contains(logged, "model=model-a") {
		t.Fatalf("audit log missing metadata: %s", logged)
	}
	for _, leaked := range []string{"prompt-sentinel", "cache-sentinel", "user-sentinel", "safety-sentinel"} {
		if strings.Contains(logged, leaked) {
			t.Fatalf("audit log leaked %q: %s", leaked, logged)
		}
	}
}

func TestGatewayChatPassthroughDisabledRejectsUnknownField(t *testing.T) {
	state, recorder := gatewayHarnessState(t, "", "")
	secret := gatewayHarnessToken(t, state)

	body := `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"safety_field":"passthrough-safe","stream":false}`
	rec := postGatewayChat(t, state, secret, body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"unsupported_parameter"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := recorder.calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestGatewayChatPassthroughDeniedModelNeverReachesUpstream(t *testing.T) {
	state, recorder := gatewayHarnessState(t, "", "model-a")
	secret := gatewayHarnessToken(t, state)

	body := `{"model":"model-b","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"stream":false}`
	rec := postGatewayChat(t, state, secret, body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := recorder.calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}
