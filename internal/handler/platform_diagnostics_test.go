package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/diagnostics"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

const diagnosticChunk = "data: {\"id\":\"chunk\",\"object\":\"chat.completion.chunk\",\"created\":0,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer-SENSITIVE\"}}]}\n\n"

func captureDiagnostic(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := log.Writer()
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(old) })
	return &b
}
func readDiagnostic(t *testing.T, b *bytes.Buffer) diagnostics.Record {
	t.Helper()
	var r diagnostics.Record
	if err := json.Unmarshal(b.Bytes(), &r); err != nil {
		t.Fatalf("expected one JSON diagnostic: %v; output=%q", err, b.String())
	}
	if strings.Contains(b.String(), "SENSITIVE") {
		t.Fatal("sensitive log")
	}
	return r
}
func TestPlatformDiagnosticRequestIDAndRouteScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newPlatformWhiteLabelTestState(t)
	engine := gin.New()
	RegisterPlatform(engine, state)
	b := captureDiagnostic(t)
	seen := map[string]bool{}
	for _, id := range []string{"SENSITIVE-valid-id", "SENSITIVE-valid-id", "illegal SENSITIVE\t"} {
		b.Reset()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", nil)
		req.Header.Set("X-Request-ID", id)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatal(rec.Code)
		}
		r := readDiagnostic(t, b)
		sum := sha256.Sum256([]byte(rec.Header().Get("X-Request-ID")))
		if r.RequestIDSHA256 != hex.EncodeToString(sum[:]) || seen[r.TraceID] || r.Stages[diagnostics.Auth].Reason != diagnostics.Rejected || r.Stages[diagnostics.Validation].State != "not_run" || r.HTTPStatus != 401 {
			t.Fatalf("bad auth record: %+v", r)
		}
		seen[r.TraceID] = true
	}
	for _, route := range []string{"/api/v1/platform/chat/compare", "/api/v1/platform/models", "/v1/chat/completions"} {
		b.Reset()
		req := httptest.NewRequest(http.MethodPost, route, nil)
		engine.ServeHTTP(httptest.NewRecorder(), req)
		if b.Len() != 0 {
			t.Fatalf("non-target instrumented: %s", route)
		}
	}
}
func TestPlatformDiagnosticPipeline(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stage       diagnostics.Stage
		reason      diagnostics.Reason
		status      int
		body        string
		upStatus    int
		upErr       error
		dbStage     diagnostics.Stage
		first       bool
		writeAt     int
		cancelWrite bool
	}{
		{name: "validation", stage: diagnostics.Validation, reason: diagnostics.Invalid, status: 400, body: `{}`},
		{name: "quota_exhausted", stage: diagnostics.Quota, reason: diagnostics.Rejected, status: 503},
		{name: "conversation_other_user", stage: diagnostics.Conversation, reason: diagnostics.Rejected, status: 503},
		{name: "conversation_missing", stage: diagnostics.Conversation, reason: diagnostics.Rejected, status: 503, body: `{"model":"model-a","messages":[{"role":"user","content":"SENSITIVE"}],"max_tokens":5,"stream":true,"conversation_guid":"1"}`},
		{name: "acl", stage: diagnostics.Catalog, reason: diagnostics.Rejected, status: 404, body: `{"model":"model-b","messages":[{"role":"user","content":"SENSITIVE"}],"max_tokens":5,"stream":true}`},
		{name: "quota_db", stage: diagnostics.Quota, reason: diagnostics.Database, status: 503, dbStage: diagnostics.Quota},
		{name: "conversation_db", stage: diagnostics.Conversation, reason: diagnostics.Database, status: 503, dbStage: diagnostics.Conversation},
		{name: "message_db", stage: diagnostics.UserMessage, reason: diagnostics.Database, status: 503, dbStage: diagnostics.UserMessage},
		{name: "title_db", stage: diagnostics.Title, reason: diagnostics.Database, status: 503, dbStage: diagnostics.Title},
		{name: "timeout", stage: diagnostics.Connect, reason: diagnostics.Timeout, status: 503, upErr: context.DeadlineExceeded},
		{name: "429", stage: diagnostics.Connect, reason: diagnostics.Non2xx, status: 503, upStatus: 429},
		{name: "500", stage: diagnostics.Connect, reason: diagnostics.Non2xx, status: 503, upStatus: 500},
		{name: "malformed", stage: diagnostics.Stream, reason: diagnostics.Malformed, status: 503},
		{name: "early_eof", stage: diagnostics.Stream, reason: diagnostics.Incomplete, status: 200, first: true},
		{name: "assistant_db", stage: diagnostics.Assistant, reason: diagnostics.Database, status: 200, dbStage: diagnostics.Assistant, first: true},
		{name: "usage_db", stage: diagnostics.Usage, reason: diagnostics.Database, status: 200, dbStage: diagnostics.Usage, first: true},
		{name: "write_meta", stage: diagnostics.Stream, reason: diagnostics.Write, status: 200, writeAt: 1},
		{name: "write_delta", stage: diagnostics.Stream, reason: diagnostics.Write, status: 200, writeAt: 2, first: true},
		{name: "cancel_stream", stage: diagnostics.Stream, reason: diagnostics.Canceled, status: 200, writeAt: 2, first: true, cancelWrite: true},
		{name: "cancel_connect", stage: diagnostics.Connect, reason: diagnostics.Canceled, status: 503, upErr: context.Canceled},
		{name: "write_done", stage: diagnostics.FinalWrite, reason: diagnostics.Write, status: 200, writeAt: 4, first: true},
		{name: "normal", stage: diagnostics.FinalWrite, reason: diagnostics.OK, status: 200, first: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			state := newPlatformWhiteLabelTestState(t)
			user := platformTestUser(t, state, "diagnostic-"+tc.name, models.JSONSlice{"model-a"})
			if tc.name == "quota_exhausted" {
				user.DailyCallLimit = 1
				user.DailyCallsUsed = 1
				now := time.Now().UTC().UnixMilli()
				user.DailyCallsResetAt = &now
			}
			if err := state.DB.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			token := platformJWT(t, state, &user)
			calls := 0
			wl, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{BaseURL: "https://SENSITIVE.test/v1", APIKey: "SENSITIVE", AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}}}, &http.Client{Transport: platformRoundTripper(func(req *http.Request) (*http.Response, error) {
				payload := `{"data":[{"id":"model-a"},{"id":"model-b"}]}`
				status := 200
				if strings.HasSuffix(req.URL.Path, "/chat/completions") {
					calls++
					if tc.upErr != nil {
						return nil, tc.upErr
					}
					if tc.upStatus != 0 {
						status = tc.upStatus
					}
					payload = diagnosticChunk + "data: [DONE]\n\n"
					if tc.name == "malformed" {
						payload = "data: SENSITIVE\n\n"
					}
					if tc.name == "early_eof" {
						payload = diagnosticChunk
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
			})}, nil)
			if err != nil {
				t.Fatal(err)
			}
			state.WhiteLabel = wl
			state.Platform = service.NewPlatformChatService(service.PlatformDeps{Settings: state.Settings, DB: state.DB, Billing: state.Billing, WhiteLabel: wl})
			if tc.dbStage != "" {
				updates := 0
				inject := func(tx *gorm.DB) {
					fail := false
					switch tc.dbStage {
					case diagnostics.Quota:
						fail = tx.Statement.Table == "users"
					case diagnostics.Conversation:
						fail = tx.Statement.Table == "conversations"
					case diagnostics.UserMessage:
						if m, ok := tx.Statement.Dest.(*models.Message); ok {
							fail = m.Role.String() == "user"
						}
					case diagnostics.Title:
						if tx.Statement.Table == "conversations" {
							updates++
							fail = updates == 2
						}
					case diagnostics.Assistant:
						if m, ok := tx.Statement.Dest.(*models.Message); ok {
							fail = m.Role.String() == "assistant"
						}
					case diagnostics.Usage:
						if tx.Statement.Table == "users" {
							updates++
							fail = updates == 2
						}
					}
					if fail {
						tx.AddError(errors.New("SENSITIVE DB error"))
					}
				}
				if tc.dbStage == diagnostics.Quota || tc.dbStage == diagnostics.Title || tc.dbStage == diagnostics.Usage {
					if err := state.DB.Callback().Update().Before("gorm:update").Register("diagnostic_failure", inject); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { state.DB.Callback().Update().Remove("diagnostic_failure") })
				} else {
					if err := state.DB.Callback().Create().Before("gorm:create").Register("diagnostic_failure", inject); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { state.DB.Callback().Create().Remove("diagnostic_failure") })
				}
			}
			b := captureDiagnostic(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			engine := gin.New()
			if tc.writeAt > 0 {
				engine.Use(func(c *gin.Context) {
					c.Writer = &diagnosticFailWriter{ResponseWriter: c.Writer, failAt: tc.writeAt, cancel: cancel, doCancel: tc.cancelWrite}
					c.Next()
				})
			}
			RegisterPlatform(engine, state)
			body := tc.body
			if tc.name == "conversation_other_user" {
				other := platformTestUser(t, state, "diag-other", nil)
				if err := state.DB.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
				conv, err := service.CreateConversation(state.DB, &other, "SENSITIVE", "model-a")
				if err != nil {
					t.Fatal(err)
				}
				body = fmt.Sprintf(`{"model":"model-a","messages":[{"role":"user","content":"SENSITIVE"}],"max_tokens":5,"stream":true,"conversation_guid":"%d"}`, conv.Guid)
			}
			if body == "" {
				body = `{"model":"model-a","messages":[{"role":"user","content":"prompt-SENSITIVE"}],"max_tokens":5,"stream":true}`
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(body))
			req = req.WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", "SENSITIVE")
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			r := readDiagnostic(t, b)
			if r.Stages[tc.stage].Reason != tc.reason || r.Stages[tc.stage].State == "not_run" || r.FirstFrameEmitted != tc.first || r.HTTPStatus != tc.status {
				t.Fatalf("stage=%s result=%+v first=%v", tc.stage, r.Stages[tc.stage], r.FirstFrameEmitted)
			}
			expectedCalls := 0
			if tc.stage == diagnostics.Connect || tc.stage == diagnostics.Stream || tc.first {
				expectedCalls = 1
			}
			if calls != expectedCalls {
				t.Fatalf("upstream calls=%d want=%d", calls, expectedCalls)
			}
			if r.DailyCallSaved != (tc.stage != diagnostics.Validation && tc.stage != diagnostics.Catalog && tc.stage != diagnostics.Quota) {
				t.Fatal("daily call flag incorrect")
			}
			if r.FinalSaved != (tc.name == "normal" || tc.name == "write_done") {
				t.Fatal("final persisted flag incorrect")
			}
			var saved models.User
			if err := state.DB.First(&saved, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			wantCalls := 0
			if r.DailyCallSaved || tc.name == "quota_exhausted" {
				wantCalls = 1
			}
			if saved.DailyCallsUsed != wantCalls {
				t.Fatalf("persisted daily calls=%d want=%d", saved.DailyCallsUsed, wantCalls)
			}
			if saved.TotalTokensUsed != 0 && tc.name != "normal" && tc.name != "write_done" {
				t.Fatal("unexpected token persistence")
			}
			if tc.name == "normal" {
				out := rec.Body.String()
				meta, delta, done, final := strings.Index(out, `"type":"meta"`), strings.Index(out, `chat.completion.chunk`), strings.Index(out, "data: [DONE]"), strings.Index(out, `"type":"done"`)
				if !(meta >= 0 && delta > meta && done > delta && final > done) {
					t.Fatalf("event contract changed: %s", out)
				}
			}
			if tc.first && tc.name != "normal" && !strings.Contains(rec.Body.String(), "event: error") {
				t.Fatal("missing existing SSE error event")
			}
		})
	}
}

type diagnosticFailWriter struct {
	gin.ResponseWriter
	calls, failAt int
	cancel        context.CancelFunc
	doCancel      bool
}

func (w *diagnosticFailWriter) Write(b []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		if w.doCancel {
			w.cancel()
		}
		return 0, errors.New("SENSITIVE client write")
	}
	return w.ResponseWriter.Write(b)
}

func TestPlatformDiagnosticSerializationFailureBeforeUpstream(t *testing.T) {
	state := newPlatformWhiteLabelTestState(t)
	user := platformTestUser(t, state, "diag-serialize", models.JSONSlice{"model-a"})
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	ctx, tr := diagnostics.New(context.Background())
	err := state.Platform.Stream(ctx, state.DB, &user, service.ChatParams{Model: "model-a", Messages: []map[string]interface{}{{"role": "user", "content": "SENSITIVE"}}, WhiteLabelBody: []byte("{")}, func([]byte) error { t.Fatal("unexpected write"); return nil })
	if err == nil {
		t.Fatal("expected original serialization failure")
	}
	var b bytes.Buffer
	tr.End(&b, 503, "id")
	r := readDiagnostic(t, &b)
	if r.Stages[diagnostics.Serialization].Reason != diagnostics.Invalid || r.UpstreamRequestAttempted || !r.DailyCallSaved || !r.UserMessageSaved {
		t.Fatalf("unexpected state: %+v", r)
	}
}
