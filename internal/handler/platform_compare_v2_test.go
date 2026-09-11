package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

const platformCompareV2ExactBody = "event: meta\n" +
	"data: {\"schema\":\"platform-chat-sse.v2\",\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"conversation_guid\":\"8101\",\"models\":[\"model-b\",\"model-a\"]}\n\n" +
	"event: delta\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"model\":\"model-b\",\"seq\":1,\"delta\":\"bravo\"}\n\n" +
	"event: model_done\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"model\":\"model-b\",\"last_seq\":1}\n\n" +
	"event: model_error\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"model\":\"model-a\",\"code\":\"timeout\",\"request_id\":\"request-public-1\"}\n\n" +
	"event: done\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"status\":\"completed\",\"conversation_guid\":\"8101\",\"models\":{\"model-b\":{\"status\":\"completed\",\"tokens\":3},\"model-a\":{\"status\":\"failed\",\"code\":\"timeout\"}},\"total_tokens_used\":12}\n\n"

type platformCompareV2RunnerFake struct {
	run   func(service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error)
	calls int
	input service.PlatformCompareGenerationInput
}

func (f *platformCompareV2RunnerFake) Run(input service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error) {
	f.calls++
	f.input = input
	if f.run == nil {
		return service.PlatformCompareGenerationRunResult{}, service.ErrPlatformCompareGenerationUnavailable
	}
	return f.run(input)
}

func platformGenerationV2Engine(t *testing.T, single service.PlatformSingleGenerationRunnerAPI, compare service.PlatformCompareGenerationRunnerAPI, control service.PlatformGenerationController, allowed models.JSONSlice) *gin.Engine {
	t.Helper()
	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{
		BaseURL: "https://white-label.test/v1", APIKey: "provider-secret",
		AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}, "model-c": {}},
	}, &http.Client{Transport: platformRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"},{"id":"model-b"},{"id":"model-c"}]}`)), Request: req}, nil
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &app.State{Settings: &config.Settings{}, WhiteLabel: whiteLabel, PlatformGenerationControl: control, PlatformSingleGeneration: single, PlatformCompareGeneration: compare}
	engine := gin.New()
	registerPlatformWithAuthentication(engine, state, func(c *gin.Context) {
		c.Set(middleware.ContextUser, &models.User{ID: 47, Status: models.UserStatusActive, DailyCallLimit: 10, AllowedModels: allowed})
		c.Set(middleware.ContextUserID, int64(47))
		c.Next()
	})
	return engine
}

func platformCompareV2Engine(t *testing.T, runner service.PlatformCompareGenerationRunnerAPI, control service.PlatformGenerationController, allowed models.JSONSlice) *gin.Engine {
	return platformGenerationV2Engine(t, nil, runner, control, allowed)
}

func platformCompareV2Payload(modelsJSON, extra string) string {
	return `{"model":"model-a","models":` + modelsJSON + `,"messages":[{"role":"user","content":"hello"}],"max_tokens":8,"stream":true,"stream_version":"platform-chat-sse.v2","generation_id":"` + platformV2GenerationID + `"` + extra + `}`
}

func platformCompareV2Request(engine *gin.Engine, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/compare", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "request-public-1")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestPlatformCompareV2StreamsExactPublicProtocol(t *testing.T) {
	type contextKey string
	const key contextKey = "compare-v2"
	runner := &platformCompareV2RunnerFake{run: func(input service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error) {
		if input.Context == nil || input.Context.Value(key) != "request-context" || input.User == nil || input.User.ID != 47 || input.GenerationID != platformV2GenerationID || input.RequestID != "request-public-1" || !reflect.DeepEqual(input.Models, []string{"model-b", "model-a"}) {
			t.Fatalf("runner input=%+v", input)
		}
		if input.Params.MaxTokens == nil || *input.Params.MaxTokens != 8 || len(input.Params.Messages) != 1 || strings.Contains(string(input.Params.WhiteLabelBody), "models") || strings.Contains(string(input.Params.WhiteLabelBody), "stream_version") || strings.Contains(string(input.Params.WhiteLabelBody), "generation_id") {
			t.Fatalf("runner params=%+v payload=%s", input.Params, input.Params.WhiteLabelBody)
		}
		if err := input.Write([]byte(platformCompareV2ExactBody)); err != nil {
			return service.PlatformCompareGenerationRunResult{Started: true}, err
		}
		return service.PlatformCompareGenerationRunResult{Started: true}, nil
	}}
	engine := platformCompareV2Engine(t, runner, nil, models.JSONSlice{"model-a", "model-b"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/compare", strings.NewReader(platformCompareV2Payload(`["model-b","model-a"]`, `,"stream_options":{"include_usage":true}`)))
	req = req.WithContext(context.WithValue(req.Context(), key, "request-context"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "request-public-1")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != platformCompareV2ExactBody || runner.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%q", rec.Code, runner.calls, rec.Body.String())
	}
	for name, want := range map[string]string{"Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache, no-transform", "X-Accel-Buffering": "no"} {
		if got := rec.Header().Get(name); got != want {
			t.Fatalf("%s=%q want=%q", name, got, want)
		}
	}
	if got := rec.Header().Get("Connection"); got != "" {
		t.Fatalf("v2 compare Connection=%q, want absent", got)
	}
	allowedEvents := map[string]bool{"meta": true, "delta": true, "model_done": true, "model_error": true, "done": true, "error": true}
	meta := 0
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "event: ") {
			event := strings.TrimPrefix(line, "event: ")
			if !allowedEvents[event] {
				t.Fatalf("unexpected event %q", event)
			}
			if event == "meta" {
				meta++
			}
		}
	}
	if meta != 1 || strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("meta=%d body=%s", meta, rec.Body.String())
	}
}

func TestPlatformCompareV2MapsPreStreamOutcomes(t *testing.T) {
	for _, test := range []struct {
		name       string
		runnerErr  error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid", runnerErr: service.ErrPlatformCompareGenerationInvalid, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "quota", runnerErr: service.ErrPlatformCompareGenerationQuota, wantStatus: http.StatusTooManyRequests, wantCode: "rate_limited"},
		{name: "unavailable", runnerErr: service.ErrPlatformCompareGenerationUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "platform_stream_v2_unavailable"},
		{name: "private", runnerErr: errors.New("private compare failure"), wantStatus: http.StatusServiceUnavailable, wantCode: "platform_stream_v2_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &platformCompareV2RunnerFake{run: func(service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error) {
				return service.PlatformCompareGenerationRunResult{}, test.runnerErr
			}}
			rec := platformCompareV2Request(platformCompareV2Engine(t, runner, nil, models.JSONSlice{"model-a", "model-b"}), platformCompareV2Payload(`["model-a","model-b"]`, ""))
			if rec.Code != test.wantStatus || !strings.Contains(rec.Body.String(), `"code":"`+test.wantCode+`"`) || strings.Contains(rec.Body.String(), test.runnerErr.Error()) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPlatformCompareV2DuplicateUsesAuthenticatedControlView(t *testing.T) {
	mode, conversation := "compare", "8101"
	view := service.PlatformGenerationView{GenerationID: platformV2GenerationID, Status: "completed", Mode: &mode, ConversationGUID: &conversation, Results: []service.PlatformGenerationResultView{{Model: "model-a", Status: "completed", Content: "hydrated"}, {Model: "model-b", Status: "failed", Code: "timeout"}}}
	control := &platformSingleV2ControllerFake{view: view}
	runner := &platformCompareV2RunnerFake{run: func(service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error) {
		return service.PlatformCompareGenerationRunResult{Duplicate: &service.PlatformGenerationSnapshot{GenerationID: platformV2GenerationID, LeaseOwnerSHA256: "private-lease"}}, nil
	}}
	rec := platformCompareV2Request(platformCompareV2Engine(t, runner, control, models.JSONSlice{"model-a", "model-b"}), platformCompareV2Payload(`["model-a","model-b"]`, ""))
	if rec.Code != http.StatusConflict || control.calls != 1 || control.uid != 47 || control.id != platformV2GenerationID || control.ctx == nil || !strings.Contains(rec.Body.String(), `"content":"hydrated"`) || strings.Contains(rec.Body.String(), "private-lease") {
		t.Fatalf("status=%d control=%+v body=%s", rec.Code, control, rec.Body.String())
	}
}

func TestPlatformCompareV2RejectsModelCardinalityAndDuplicatesBeforeRunner(t *testing.T) {
	runner := &platformCompareV2RunnerFake{run: func(service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error) {
		t.Fatal("invalid compare models reached runner")
		return service.PlatformCompareGenerationRunResult{}, nil
	}}
	for _, modelsJSON := range []string{`[]`, `["model-a"]`, `["model-a","model-b","model-c","model-d"]`, `["model-a","model-a"]`, `["model-a",""]`, `["model-a","../model"]`} {
		rec := platformCompareV2Request(platformCompareV2Engine(t, runner, nil, models.JSONSlice{"model-a", "model-b", "model-c"}), platformCompareV2Payload(modelsJSON, ""))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("models=%s status=%d body=%s", modelsJSON, rec.Code, rec.Body.String())
		}
	}
	if runner.calls != 0 {
		t.Fatalf("invalid model runner calls=%d", runner.calls)
	}
}

func TestPlatformCompareV2FailsClosedForMissingDependencies(t *testing.T) {
	t.Run("nil state", func(t *testing.T) {
		engine := gin.New()
		registerPlatformWithAuthentication(engine, nil, func(c *gin.Context) {
			c.Set(middleware.ContextUser, &models.User{ID: 47, Status: models.UserStatusActive})
			c.Next()
		})
		rec := platformCompareV2Request(engine, platformCompareV2Payload(`["model-a","model-b"]`, ""))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"platform_stream_v2_unavailable"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("missing authenticated user", func(t *testing.T) {
		runner := &platformCompareV2RunnerFake{}
		whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://white-label.test/v1", APIKey: "provider-secret", AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}}}, http.DefaultClient, nil)
		if err != nil {
			t.Fatal(err)
		}
		state := &app.State{Settings: &config.Settings{}, WhiteLabel: whiteLabel, PlatformCompareGeneration: runner}
		engine := gin.New()
		registerPlatformWithAuthentication(engine, state, func(c *gin.Context) { c.Next() })
		rec := platformCompareV2Request(engine, platformCompareV2Payload(`["model-a","model-b"]`, ""))
		if rec.Code != http.StatusServiceUnavailable || runner.calls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, runner.calls, rec.Body.String())
		}
	})
	t.Run("nil runner", func(t *testing.T) {
		rec := platformCompareV2Request(platformCompareV2Engine(t, nil, nil, models.JSONSlice{"model-a", "model-b"}), platformCompareV2Payload(`["model-a","model-b"]`, ""))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"platform_stream_v2_unavailable"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("missing catalog", func(t *testing.T) {
		runner := &platformCompareV2RunnerFake{}
		state := &app.State{Settings: &config.Settings{}, PlatformCompareGeneration: runner}
		engine := gin.New()
		registerPlatformWithAuthentication(engine, state, func(c *gin.Context) {
			c.Set(middleware.ContextUser, &models.User{ID: 47, Status: models.UserStatusActive})
			c.Next()
		})
		rec := platformCompareV2Request(engine, platformCompareV2Payload(`["model-a","model-b"]`, ""))
		if rec.Code != http.StatusServiceUnavailable || runner.calls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, runner.calls, rec.Body.String())
		}
	})
	t.Run("duplicate without control", func(t *testing.T) {
		runner := &platformCompareV2RunnerFake{run: func(service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error) {
			return service.PlatformCompareGenerationRunResult{Duplicate: &service.PlatformGenerationSnapshot{GenerationID: platformV2GenerationID}}, nil
		}}
		rec := platformCompareV2Request(platformCompareV2Engine(t, runner, nil, models.JSONSlice{"model-a", "model-b"}), platformCompareV2Payload(`["model-a","model-b"]`, ""))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"platform_stream_v2_unavailable"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestPlatformCompareV2NeverWritesJSONAfterStreamStarts(t *testing.T) {
	runner := &platformCompareV2RunnerFake{run: func(input service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error) {
		if err := input.Write([]byte("event: meta\ndata: {}\n\n")); err != nil {
			return service.PlatformCompareGenerationRunResult{Started: true}, err
		}
		return service.PlatformCompareGenerationRunResult{Started: true}, errors.New("private post-stream failure")
	}}
	engine := platformCompareV2Engine(t, runner, nil, models.JSONSlice{"model-a", "model-b"})
	rec := platformCompareV2Request(engine, platformCompareV2Payload(`["model-a","model-b"]`, ""))
	if rec.Code != http.StatusOK || rec.Body.String() != "event: meta\ndata: {}\n\n" || strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	runner.run = func(service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error) {
		return service.PlatformCompareGenerationRunResult{Started: true}, errors.New("failure before write")
	}
	rec = platformCompareV2Request(engine, platformCompareV2Payload(`["model-a","model-b"]`, ""))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("result-started status=%d body=%q", rec.Code, rec.Body.String())
	}
	runner.run = func(input service.PlatformCompareGenerationInput) (service.PlatformCompareGenerationRunResult, error) {
		return service.PlatformCompareGenerationRunResult{}, input.Write([]byte("event: meta\ndata: {}\n\n"))
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/compare", strings.NewReader(platformCompareV2Payload(`["model-a","model-b"]`, "")))
	req.Header.Set("Content-Type", "application/json")
	writer := &platformSingleV2FailWriter{header: make(http.Header)}
	engine.ServeHTTP(writer, req)
	if writer.writes != 1 || writer.flush != 0 {
		t.Fatalf("failed writer writes=%d flush=%d", writer.writes, writer.flush)
	}
	for name, want := range map[string]string{"Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache, no-transform", "X-Accel-Buffering": "no"} {
		if got := writer.header.Get(name); got != want {
			t.Fatalf("failed writer %s=%q want=%q", name, got, want)
		}
	}
	if got := writer.header.Get("Connection"); got != "" {
		t.Fatalf("failed writer Connection=%q, want absent", got)
	}
}

func TestPlatformCompareV2PreservesLegacyAndNonStreamingBehavior(t *testing.T) {
	runner := &platformCompareV2RunnerFake{}
	engine := platformCompareV2Engine(t, runner, nil, models.JSONSlice{"model-a", "model-b"})
	for _, payload := range []string{
		`{"model":"model-a","models":["model-a","model-b"],"messages":[{"role":"user","content":"hello"}],"stream":true}`,
		`{"model":"model-a","models":["model-a","model-b"],"messages":[{"role":"user","content":"hello"}],"stream":false}`,
	} {
		rec := platformCompareV2Request(engine, payload)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"missing_max_tokens"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if runner.calls != 0 {
		t.Fatalf("legacy/nonstream compare runner calls=%d", runner.calls)
	}
}
