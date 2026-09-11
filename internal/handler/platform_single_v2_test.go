package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

const platformSingleV2ExactBody = "event: meta\n" +
	"data: {\"schema\":\"platform-chat-sse.v2\",\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"conversation_guid\":\"8101\",\"models\":[\"model-a\"]}\n\n" +
	"event: delta\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"model\":\"model-a\",\"seq\":1,\"delta\":\"hello\"}\n\n" +
	"event: model_done\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"model\":\"model-a\",\"last_seq\":1}\n\n" +
	"event: done\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"status\":\"completed\",\"conversation_guid\":\"8101\",\"tokens\":3,\"total_tokens_used\":12}\n\n"

type platformSingleV2RunnerFake struct {
	run   func(service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error)
	calls int
	input service.PlatformSingleGenerationInput
}

func (f *platformSingleV2RunnerFake) Run(input service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
	f.calls++
	f.input = input
	if f.run == nil {
		return service.PlatformSingleGenerationRunResult{}, service.ErrPlatformSingleGenerationUnavailable
	}
	return f.run(input)
}

type platformSingleV2ControllerFake struct {
	view  service.PlatformGenerationView
	err   error
	calls int
	ctx   context.Context
	uid   int64
	id    string
}

type platformSingleV2FailWriter struct {
	header http.Header
	writes int
	flush  int
}

func (w *platformSingleV2FailWriter) Header() http.Header {
	return w.header
}

func (w *platformSingleV2FailWriter) WriteHeader(int) {}

func (w *platformSingleV2FailWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("injected writer failure")
}

func (w *platformSingleV2FailWriter) Flush() { w.flush++ }

func (f *platformSingleV2ControllerFake) Get(ctx context.Context, userID int64, generationID string) (service.PlatformGenerationView, error) {
	f.calls++
	f.ctx, f.uid, f.id = ctx, userID, generationID
	return f.view, f.err
}

func (f *platformSingleV2ControllerFake) Cancel(context.Context, int64, string) (service.PlatformGenerationView, bool, error) {
	return service.PlatformGenerationView{}, false, service.ErrPlatformGenerationControlUnavailable
}

func platformSingleV2Engine(t *testing.T, runner service.PlatformSingleGenerationRunnerAPI, control service.PlatformGenerationController, allowed models.JSONSlice) *gin.Engine {
	t.Helper()
	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{
		BaseURL: "https://white-label.test/v1", APIKey: "provider-secret",
		AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}},
	}, &http.Client{Transport: platformRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`)), Request: req}, nil
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &app.State{Settings: &config.Settings{}, WhiteLabel: whiteLabel, PlatformGenerationControl: control, PlatformSingleGeneration: runner}
	engine := gin.New()
	registerPlatformWithAuthentication(engine, state, func(c *gin.Context) {
		c.Set(middleware.ContextUser, &models.User{ID: 47, Status: models.UserStatusActive, DailyCallLimit: 10, AllowedModels: allowed})
		c.Set(middleware.ContextUserID, int64(47))
		c.Next()
	})
	return engine
}

func platformSingleV2Request(engine *gin.Engine, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "request-public-1")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func platformSingleV2Payload(extra string) string {
	return `{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":8,"stream":true,"stream_version":"platform-chat-sse.v2","generation_id":"` + platformV2GenerationID + `"` + extra + `}`
}

func TestPlatformSingleV2StreamsExactPublicProtocol(t *testing.T) {
	type contextKey string
	const key contextKey = "single-v2"
	runner := &platformSingleV2RunnerFake{run: func(input service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
		if input.Context == nil || input.Context.Value(key) != "request-context" {
			t.Fatalf("runner context did not derive from request: %v", input.Context)
		}
		if input.User == nil || input.User.ID != 47 || input.GenerationID != platformV2GenerationID || input.RequestID != "request-public-1" || input.Params.Model != "model-a" {
			t.Fatalf("runner input=%+v", input)
		}
		if err := input.Write([]byte(platformSingleV2ExactBody)); err != nil {
			return service.PlatformSingleGenerationRunResult{Started: true}, err
		}
		return service.PlatformSingleGenerationRunResult{Started: true}, nil
	}}
	engine := platformSingleV2Engine(t, runner, nil, models.JSONSlice{"model-a"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(platformSingleV2Payload(`,"stream_options":{"include_usage":true}`)))
	req = req.WithContext(context.WithValue(req.Context(), key, "request-context"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "request-public-1")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != platformSingleV2ExactBody {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	for name, want := range map[string]string{"Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache, no-transform", "X-Accel-Buffering": "no"} {
		if got := rec.Header().Get(name); got != want {
			t.Fatalf("%s=%q want=%q", name, got, want)
		}
	}
	for _, forbidden := range []string{"[DONE]", "provider-secret", "raw-provider-id", "hello prompt secret", "lease-token"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestPlatformSingleV2MapsPreStreamOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		runnerErr  error
		wantStatus int
		wantCode   string
	}{
		{"invalid", service.ErrPlatformSingleGenerationInvalid, http.StatusBadRequest, "invalid_request"},
		{"quota", service.ErrPlatformSingleGenerationQuota, http.StatusTooManyRequests, "rate_limited"},
		{"unavailable", service.ErrPlatformSingleGenerationUnavailable, http.StatusServiceUnavailable, "platform_stream_v2_unavailable"},
		{"upstream", service.ErrPlatformSingleGenerationUpstream, http.StatusServiceUnavailable, "platform_stream_v2_unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &platformSingleV2RunnerFake{run: func(service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
				return service.PlatformSingleGenerationRunResult{}, tc.runnerErr
			}}
			rec := platformSingleV2Request(platformSingleV2Engine(t, runner, nil, models.JSONSlice{"model-a"}), platformSingleV2Payload(""))
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), `"code":"`+tc.wantCode+`"`) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), tc.runnerErr.Error()) {
				t.Fatalf("raw service error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestPlatformSingleV2DuplicateUsesAuthenticatedControlView(t *testing.T) {
	mode := "single"
	conversation := "8101"
	view := service.PlatformGenerationView{GenerationID: platformV2GenerationID, Status: "completed", Mode: &mode, ConversationGUID: &conversation, Result: &service.PlatformGenerationResultView{Model: "model-a", Status: "completed", Content: "hydrated", Tokens: generationInt64Pointer(3)}}
	control := &platformSingleV2ControllerFake{view: view}
	runner := &platformSingleV2RunnerFake{run: func(service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
		return service.PlatformSingleGenerationRunResult{Duplicate: &service.PlatformGenerationSnapshot{GenerationID: platformV2GenerationID}}, nil
	}}
	rec := platformSingleV2Request(platformSingleV2Engine(t, runner, control, models.JSONSlice{"model-a"}), platformSingleV2Payload(""))
	if rec.Code != http.StatusConflict || control.calls != 1 || control.uid != 47 || control.id != platformV2GenerationID || control.ctx == nil {
		t.Fatalf("status=%d control=%+v body=%s", rec.Code, control, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"hydrated"`) || strings.Contains(rec.Body.String(), "lease") {
		t.Fatalf("duplicate response not hydrated/private: %s", rec.Body.String())
	}

	control.view = service.PlatformGenerationView{GenerationID: platformV2GenerationID, Status: "cancelled"}
	rec = platformSingleV2Request(platformSingleV2Engine(t, runner, control, models.JSONSlice{"model-a"}), platformSingleV2Payload(""))
	if rec.Code != http.StatusConflict || rec.Body.String() != `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"cancelled","mode":null,"conversation_guid":null}` {
		t.Fatalf("cancel tombstone status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlatformSingleV2FailsClosedForMissingDependenciesAndHydrationFailure(t *testing.T) {
	t.Run("nil runner", func(t *testing.T) {
		rec := platformSingleV2Request(platformSingleV2Engine(t, nil, nil, models.JSONSlice{"model-a"}), platformSingleV2Payload(""))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"platform_stream_v2_unavailable"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("duplicate control error", func(t *testing.T) {
		runner := &platformSingleV2RunnerFake{run: func(service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
			return service.PlatformSingleGenerationRunResult{Duplicate: &service.PlatformGenerationSnapshot{GenerationID: platformV2GenerationID}}, nil
		}}
		control := &platformSingleV2ControllerFake{err: errors.New("private database detail")}
		rec := platformSingleV2Request(platformSingleV2Engine(t, runner, control, models.JSONSlice{"model-a"}), platformSingleV2Payload(""))
		if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "private database detail") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestPlatformSingleV2DecodeAndAuthorizationPrecedeRunner(t *testing.T) {
	runner := &platformSingleV2RunnerFake{run: func(service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
		return service.PlatformSingleGenerationRunResult{}, service.ErrPlatformSingleGenerationInvalid
	}}
	for name, payload := range map[string]string{
		"invalid JSON":         `{`,
		"stream false":         strings.Replace(platformSingleV2Payload(""), `"stream":true`, `"stream":false`, 1),
		"include usage false":  platformSingleV2Payload(`,"stream_options":{"include_usage":false}`),
		"invalid conversation": platformSingleV2Payload(`,"conversation_guid":"bad"`),
		"invalid model":        strings.Replace(platformSingleV2Payload(""), `"model":"model-a"`, `"model":""`, 1),
		"invalid message":      strings.Replace(platformSingleV2Payload(""), `"content":"hello"`, `"content":""`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			rec := platformSingleV2Request(platformSingleV2Engine(t, runner, nil, models.JSONSlice{"model-a"}), payload)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls=%d, want only conversation/message preflight cases", runner.calls)
	}

	beforeUnauthorized := runner.calls
	rec := platformSingleV2Request(platformSingleV2Engine(t, runner, nil, models.JSONSlice{"model-b"}), platformSingleV2Payload(""))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"model_unavailable"`) || runner.calls != beforeUnauthorized {
		t.Fatalf("unauthorized status=%d calls=%d body=%s", rec.Code, runner.calls, rec.Body.String())
	}
}

func TestPlatformSingleV2RejectsMissingStreamBeforeAuthorizationOrRunner(t *testing.T) {
	runner := &platformSingleV2RunnerFake{run: func(service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
		t.Fatal("runner must not be called")
		return service.PlatformSingleGenerationRunResult{}, nil
	}}
	control := &platformSingleV2ControllerFake{}
	whiteLabel, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{
		BaseURL: "https://white-label.test/v1", APIKey: "provider-secret",
		AllowedModels: map[string]struct{}{"model-a": {}},
	}, &http.Client{Transport: platformRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("model catalog authorization must not run")
		return nil, errors.New("unexpected catalog call")
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &app.State{
		Settings: &config.Settings{}, WhiteLabel: whiteLabel,
		PlatformGenerationControl: control, PlatformSingleGeneration: runner,
	}
	engine := gin.New()
	registerPlatformWithAuthentication(engine, state, func(c *gin.Context) {
		c.Set(middleware.ContextUser, &models.User{ID: 47, Status: models.UserStatusActive, AllowedModels: models.JSONSlice{"model-a"}})
		c.Set(middleware.ContextUserID, int64(47))
		c.Next()
	})
	payload := strings.Replace(platformSingleV2Payload(""), `"stream":true,`, "", 1)
	rec := platformSingleV2Request(engine, payload)
	want := `{"error":{"code":"invalid_request","message":"Invalid request.","type":"invalid_request_error","request_id":"request-public-1"}}`
	if rec.Code != http.StatusBadRequest || rec.Body.String() != want {
		t.Fatalf("status=%d body=%s want=%s", rec.Code, rec.Body.String(), want)
	}
	if runner.calls != 0 || control.calls != 0 {
		t.Fatalf("runner/control calls=%d/%d, want zero", runner.calls, control.calls)
	}
}

func TestPlatformSingleV2NeverWritesJSONAfterStreamStarts(t *testing.T) {
	runner := &platformSingleV2RunnerFake{run: func(input service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
		if err := input.Write([]byte("event: meta\ndata: {}\n\n")); err != nil {
			return service.PlatformSingleGenerationRunResult{Started: true}, err
		}
		return service.PlatformSingleGenerationRunResult{Started: true}, errors.New("private post-stream failure")
	}}
	rec := platformSingleV2Request(platformSingleV2Engine(t, runner, nil, models.JSONSlice{"model-a"}), platformSingleV2Payload(""))
	if rec.Code != http.StatusOK || rec.Body.String() != "event: meta\ndata: {}\n\n" || strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}

	runner.run = func(service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
		return service.PlatformSingleGenerationRunResult{Started: true}, errors.New("first write failed")
	}
	rec = platformSingleV2Request(platformSingleV2Engine(t, runner, nil, models.JSONSlice{"model-a"}), platformSingleV2Payload(""))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("result-started status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestPlatformSingleV2MarksStartedBeforeFirstWriterFailure(t *testing.T) {
	runner := &platformSingleV2RunnerFake{run: func(input service.PlatformSingleGenerationInput) (service.PlatformSingleGenerationRunResult, error) {
		err := input.Write([]byte("event: meta\ndata: {}\n\n"))
		if err == nil || err.Error() != "injected writer failure" {
			t.Fatalf("write error=%v", err)
		}
		return service.PlatformSingleGenerationRunResult{}, err
	}}
	engine := platformSingleV2Engine(t, runner, nil, models.JSONSlice{"model-a"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(platformSingleV2Payload("")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "request-public-1")
	writer := &platformSingleV2FailWriter{header: make(http.Header)}
	engine.ServeHTTP(writer, req)
	if writer.writes != 1 {
		t.Fatalf("writes=%d, want no second JSON write", writer.writes)
	}
	if writer.flush != 0 {
		t.Fatalf("flushes=%d, want no flush after failed write", writer.flush)
	}
	for name, want := range map[string]string{"Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache, no-transform", "X-Accel-Buffering": "no"} {
		if got := writer.header.Get(name); got != want {
			t.Fatalf("%s=%q want=%q", name, got, want)
		}
	}
}

func TestPlatformSingleV2LeavesCompareAndLegacyRoutingGuarded(t *testing.T) {
	runner := &platformSingleV2RunnerFake{}
	engine := platformSingleV2Engine(t, runner, nil, models.JSONSlice{"model-a"})
	compare := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/compare", strings.NewReader(`{"model":"model-a","models":["model-a","model-b"],"messages":[{"role":"user","content":"hello"}],"max_tokens":8,"stream":true,"stream_version":"platform-chat-sse.v2","generation_id":"`+platformV2GenerationID+`"}`))
	compare.Header.Set("Content-Type", "application/json")
	compareRec := httptest.NewRecorder()
	engine.ServeHTTP(compareRec, compare)
	if compareRec.Code != http.StatusServiceUnavailable || !strings.Contains(compareRec.Body.String(), `"code":"platform_stream_v2_unavailable"`) || runner.calls != 0 {
		t.Fatalf("compare status=%d calls=%d body=%s", compareRec.Code, runner.calls, compareRec.Body.String())
	}

	legacy := platformSingleV2Request(engine, `{"model":"model-a","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	if legacy.Code != http.StatusBadRequest || !strings.Contains(legacy.Body.String(), `"code":"missing_max_tokens"`) || runner.calls != 0 {
		t.Fatalf("legacy status=%d calls=%d body=%s", legacy.Code, runner.calls, legacy.Body.String())
	}
}
