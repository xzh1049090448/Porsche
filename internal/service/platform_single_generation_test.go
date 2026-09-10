package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

const platformSingleTestGenerationID = "550e8400-e29b-41d4-a716-446655440000"

type platformSingleTestStore struct {
	calls          *[]string
	claim          PlatformGenerationClaimResult
	claimErr       error
	onClaim        func(PlatformGenerationClaimInput)
	recordErr      error
	completeCalled bool
}

func (s *platformSingleTestStore) add(call string) {
	if s.calls != nil {
		*s.calls = append(*s.calls, call)
	}
}
func (s *platformSingleTestStore) Claim(_ context.Context, in PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error) {
	s.add("claim")
	if s.onClaim != nil {
		s.onClaim(in)
	}
	return s.claim, s.claimErr
}
func (s *platformSingleTestStore) RecordDeltaOwned(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error) {
	s.add("record:1")
	return PlatformGenerationSnapshot{}, s.recordErr
}
func (s *platformSingleTestStore) MarkModelDoneOwned(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error) {
	s.add("model_done_store")
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformSingleTestStore) BeginCommitOwned(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error) {
	s.add("begin_commit")
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformSingleTestStore) Complete(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error) {
	s.add("complete")
	s.completeCalled = true
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformSingleTestStore) RenewLease(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error) {
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformSingleTestStore) FailRunningOwned(context.Context, int64, string, string, string, int64) (PlatformGenerationSnapshot, error) {
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformSingleTestStore) AcknowledgeCancelledOwned(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error) {
	return PlatformGenerationSnapshot{}, nil
}

type platformSingleTestPersistence struct {
	calls   *[]string
	input   PlatformGenerationPersistenceInput
	receipt PlatformGenerationReceiptSnapshot
	err     error
}

func (p *platformSingleTestPersistence) Finalize(_ context.Context, _ *gorm.DB, in PlatformGenerationPersistenceInput) (PlatformGenerationReceiptSnapshot, error) {
	if p.calls != nil {
		*p.calls = append(*p.calls, "persist")
	}
	p.input = in
	return p.receipt, p.err
}

type platformSingleTestRegistry struct{ calls *[]string }

func (r *platformSingleTestRegistry) Register(int64, string, context.CancelFunc) (string, error) {
	if r.calls != nil {
		*r.calls = append(*r.calls, "register")
	}
	return platformSingleTestLeaseToken(), nil
}
func (r *platformSingleTestRegistry) Unregister(int64, string, string) bool {
	if r.calls != nil {
		*r.calls = append(*r.calls, "unregister")
	}
	return true
}

type platformSingleTestUpstream struct {
	calls      *[]string
	body       []byte
	response   *http.Response
	chatErr    *whitelabel.Error
	chunks     []whitelabel.ChatCompletionChunk
	consumeErr *whitelabel.Error
}

type platformSingleTestReadCloser struct {
	io.Reader
	closed bool
}

func (r *platformSingleTestReadCloser) Close() error { r.closed = true; return nil }

func (u *platformSingleTestUpstream) Chat(_ context.Context, body []byte) (*http.Response, *whitelabel.Error) {
	if u.calls != nil {
		*u.calls = append(*u.calls, "upstream")
	}
	u.body = append([]byte(nil), body...)
	return u.response, u.chatErr
}
func (u *platformSingleTestUpstream) ConsumeChatCompletionSSEContext(ctx context.Context, _ io.Reader, _ string, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
	for _, chunk := range u.chunks {
		if err := emit(chunk); err != nil {
			return whitelabel.ErrUpstreamUnavailable("callback")
		}
	}
	return u.consumeErr
}

func platformSingleTestLeaseToken() string {
	return base64.RawURLEncoding.EncodeToString(make([]byte, platformGenerationLeaseBytes))
}

func platformSingleTestClaim(now int64, model string) PlatformGenerationClaimResult {
	token := platformSingleTestLeaseToken()
	return PlatformGenerationClaimResult{LeaseToken: token, Snapshot: PlatformGenerationSnapshot{
		GenerationID: platformSingleTestGenerationID, Mode: PlatformGenerationModeSingle,
		Models: []string{model}, State: PlatformGenerationStateRunning,
		ModelStates:     map[string]PlatformGenerationModel{model: {State: PlatformGenerationStateRunning}},
		CreatedAtMillis: now, UpdatedAtMillis: now,
		LeaseOwnerSHA256: platformGenerationLeaseDigest(token), LeaseUntilMillis: now + platformGenerationLeaseDuration.Milliseconds(),
	}}
}

func platformSingleTestUser() *models.User {
	return &models.User{ID: 7, AuditFields: models.AuditFields{IsDeleted: 0}, Status: models.UserStatusActive, PlanType: models.PlanFree, DailyCallLimit: 10, DailyCallsUsed: 1}
}

func platformSingleTestParams(message string) ChatParams {
	return ChatParams{Model: "model-a", Messages: []map[string]interface{}{{"role": "user", "content": message}}, WhiteLabelBody: []byte(`{"model":"model-a","messages":[{"role":"user","content":"hello"}],"stream":true}`)}
}

func platformSingleTestRunner(calls *[]string) (*PlatformSingleGenerationRunner, *platformSingleTestStore, *platformSingleTestPersistence, *platformSingleTestUpstream) {
	now := time.UnixMilli(1_000).UTC()
	store := &platformSingleTestStore{calls: calls, claim: platformSingleTestClaim(now.UnixMilli(), "model-a")}
	persist := &platformSingleTestPersistence{calls: calls, receipt: PlatformGenerationReceiptSnapshot{
		UserID: 7, GenerationID: platformSingleTestGenerationID, Mode: PlatformGenerationModeSingle,
		ConversationGUID: 8001, UserMessage: "hello", SuccessfulModelCount: 1, DailyCallsCharged: 1,
		TotalTokens: 3, CommittedAtMillis: 1_001,
		Results: []PlatformGenerationCommittedResult{{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001", Content: "answer", Tokens: 3}},
	}}
	finish := "stop"
	answer := "answer"
	upstream := &platformSingleTestUpstream{calls: calls, response: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ignored"))}, chunks: []whitelabel.ChatCompletionChunk{
		{Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{Content: &answer}, FinishReason: &finish}}},
		{Usage: &whitelabel.ChatCompletionUsage{TotalTokens: 3}},
	}}
	runner := &PlatformSingleGenerationRunner{deps: platformSingleGenerationDeps{
		db: &gorm.DB{}, store: store, persistence: persist, registry: &platformSingleTestRegistry{calls: calls}, upstream: upstream,
		rootContext: context.Background(), now: func() time.Time { return now },
		newGUID: func() int64 {
			if calls != nil {
				*calls = append(*calls, "preflight")
			}
			return 8001
		},
		loadTotalTokens:  func(context.Context, *gorm.DB, int64) (int64, error) { return 30, nil },
		loadConversation: func(context.Context, *gorm.DB, int64, int64) error { return nil },
		upstreamTimeout:  time.Minute,
	}}
	return runner, store, persist, upstream
}

func TestPlatformSingleGenerationNewConversationSuccess(t *testing.T) {
	var calls []string
	runner, store, persist, upstream := platformSingleTestRunner(&calls)
	var output bytes.Buffer
	result, err := runner.Run(PlatformSingleGenerationInput{User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, RequestID: "request-1", Params: platformSingleTestParams("hello"), Write: func(frame []byte) error {
		var event string
		if bytes.HasPrefix(frame, []byte("event: ")) {
			event = strings.SplitN(strings.TrimPrefix(string(frame), "event: "), "\n", 2)[0]
		}
		switch event {
		case "meta":
			calls = append(calls, "meta")
		case "delta":
			calls = append(calls, "delta:1")
		case "model_done":
			calls = append(calls, "model_done_sse")
		case "done":
			calls = append(calls, "done")
		}
		_, _ = output.Write(frame)
		return nil
	}})
	if err != nil || !result.Started || result.Duplicate != nil {
		t.Fatalf("Run() result=%+v err=%v", result, err)
	}
	wantCalls := []string{"preflight", "claim", "register", "meta", "upstream", "record:1", "delta:1", "model_done_store", "begin_commit", "model_done_sse", "persist", "complete", "done", "unregister"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls=%v want=%v", calls, wantCalls)
	}
	encoder, _ := NewPlatformSSEV2Encoder(platformSingleTestGenerationID, []string{"model-a"})
	want := append([]byte{}, encoder.Meta("8001")...)
	want = append(want, encoder.Delta("model-a", 1, "answer")...)
	want = append(want, encoder.ModelDone("model-a", 1)...)
	want = append(want, encoder.DoneSingle("8001", 3, 30)...)
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("output=%q want=%q", output.Bytes(), want)
	}
	var upstreamBody struct {
		Model         string `json:"model"`
		Stream        bool   `json:"stream"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		Messages []whitelabel.ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(upstream.body, &upstreamBody); err != nil || upstreamBody.Model != "model-a" || !upstreamBody.Stream || !upstreamBody.StreamOptions.IncludeUsage || len(upstreamBody.Messages) != 1 || upstreamBody.Messages[0].Role != "user" || upstreamBody.Messages[0].Content != "hello" {
		t.Fatalf("upstream payload=%s decoded=%+v err=%v", upstream.body, upstreamBody, err)
	}
	if !store.completeCalled {
		t.Fatal("Complete was not called")
	}
	in := persist.input
	if in.UserID != 7 || in.GenerationID != platformSingleTestGenerationID || in.Mode != PlatformGenerationModeSingle || !reflect.DeepEqual(in.Models, []string{"model-a"}) || in.ConversationGUID != nil || in.ReservedConversationGUID == nil || *in.ReservedConversationGUID != 8001 || in.UserMessage != "hello" || len(in.Results) != 1 || in.Results[0].Content != "answer" || in.Results[0].Tokens != 3 || in.Results[0].Seq != 1 {
		t.Fatalf("persistence input=%+v", in)
	}
}

func TestPlatformSingleGenerationDuplicateHasNoPostClaimSideEffects(t *testing.T) {
	var calls []string
	runner, store, _, _ := platformSingleTestRunner(&calls)
	duplicate := store.claim.Snapshot
	duplicate.State = PlatformGenerationStateCompleted
	duplicate.LeaseOwnerSHA256 = ""
	duplicate.LeaseUntilMillis = 0
	duplicate.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001"}
	store.claim = PlatformGenerationClaimResult{Duplicate: true, Snapshot: duplicate}
	store.claimErr = ErrPlatformGenerationConflict
	result, err := runner.Run(PlatformSingleGenerationInput{User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { t.Fatal("unexpected output"); return nil }})
	if err != nil || result.Started || result.Duplicate == nil || result.Duplicate.State != PlatformGenerationStateCompleted {
		t.Fatalf("Run() result=%+v err=%v", result, err)
	}
	if !reflect.DeepEqual(calls, []string{"preflight", "claim"}) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestPlatformSingleGenerationPreflightRejectsBeforeClaim(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	over := strings.Repeat("x", platformGenerationMessageTextMaxBytes+1)
	maxContext := int(^uint(0) >> 1)
	tests := []struct {
		name   string
		mutate func(*PlatformSingleGenerationInput)
		want   error
	}{
		{"nil user", func(in *PlatformSingleGenerationInput) { in.User = nil }, ErrPlatformSingleGenerationInvalid},
		{"inactive", func(in *PlatformSingleGenerationInput) { in.User.Status = models.UserStatusDisabled }, ErrPlatformSingleGenerationQuota},
		{"deleted", func(in *PlatformSingleGenerationInput) { in.User.IsDeleted = 1 }, ErrPlatformSingleGenerationQuota},
		{"quota", func(in *PlatformSingleGenerationInput) {
			reset := int64(1_000)
			in.User.DailyCallsResetAt = &reset
			in.User.DailyCallsUsed = in.User.DailyCallLimit
		}, ErrPlatformSingleGenerationQuota},
		{"nil write", func(in *PlatformSingleGenerationInput) { in.Write = nil }, ErrPlatformSingleGenerationInvalid},
		{"bad generation", func(in *PlatformSingleGenerationInput) { in.GenerationID = "BAD" }, ErrPlatformSingleGenerationInvalid},
		{"bad model", func(in *PlatformSingleGenerationInput) { in.Params.Model = "" }, ErrPlatformSingleGenerationInvalid},
		{"empty messages", func(in *PlatformSingleGenerationInput) { in.Params.Messages = nil }, ErrPlatformSingleGenerationInvalid},
		{"last assistant", func(in *PlatformSingleGenerationInput) {
			in.Params.Messages = []map[string]interface{}{{"role": "assistant", "content": "x"}}
		}, ErrPlatformSingleGenerationInvalid},
		{"content type", func(in *PlatformSingleGenerationInput) { in.Params.Messages[0]["content"] = 42 }, ErrPlatformSingleGenerationInvalid},
		{"empty content", func(in *PlatformSingleGenerationInput) { in.Params.Messages[0]["content"] = "" }, ErrPlatformSingleGenerationInvalid},
		{"invalid utf8", func(in *PlatformSingleGenerationInput) { in.Params.Messages[0]["content"] = invalidUTF8 }, ErrPlatformSingleGenerationInvalid},
		{"oversize", func(in *PlatformSingleGenerationInput) { in.Params.Messages[0]["content"] = over }, ErrPlatformSingleGenerationInvalid},
		{"include usage false", func(in *PlatformSingleGenerationInput) {
			in.Params.WhiteLabelBody = []byte(`{"stream_options":{"include_usage":false}}`)
		}, ErrPlatformSingleGenerationInvalid},
		{"context trim leaves assistant", func(in *PlatformSingleGenerationInput) {
			one := 1
			in.Params.ContextWindow = &one
			in.Params.Messages = []map[string]interface{}{{"role": "user", "content": "x"}, {"role": "user", "content": "y"}, {"role": "assistant", "content": "z"}}
		}, ErrPlatformSingleGenerationInvalid},
		{"context overflow remains safe", func(in *PlatformSingleGenerationInput) {
			in.Params.ContextWindow = &maxContext
			in.Params.Messages[0]["role"] = "assistant"
		}, ErrPlatformSingleGenerationInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			runner, _, _, _ := platformSingleTestRunner(&calls)
			in := PlatformSingleGenerationInput{User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
			tc.mutate(&in)
			if result, err := runner.Run(in); !errors.Is(err, tc.want) || result.Started {
				t.Fatalf("Run() result=%+v err=%v", result, err)
			}
			for _, call := range calls {
				if call == "claim" {
					t.Fatalf("claim called: %v", calls)
				}
			}
		})
	}
}

func TestPlatformSingleGenerationCopiesRequestBeforeClaim(t *testing.T) {
	var calls []string
	runner, store, persist, upstream := platformSingleTestRunner(&calls)
	params := platformSingleTestParams("hello")
	user := platformSingleTestUser()
	store.onClaim = func(in PlatformGenerationClaimInput) {
		params.Model = "mutated"
		params.Messages[0]["content"] = "mutated"
		params.WhiteLabelBody[0] = '['
		user.ID = 99
		if !reflect.DeepEqual(in.Models, []string{"model-a"}) {
			t.Fatalf("claim models aliased: %v", in.Models)
		}
	}
	result, err := runner.Run(PlatformSingleGenerationInput{User: user, GenerationID: platformSingleTestGenerationID, Params: params, Write: func([]byte) error { return nil }})
	if err != nil || !result.Started {
		t.Fatalf("Run()=%+v err=%v", result, err)
	}
	if persist.input.UserID != 7 || persist.input.UserMessage != "hello" || persist.input.Models[0] != "model-a" {
		t.Fatalf("aliased persistence=%+v", persist.input)
	}
	if upstream.chunks[0].Choices[0].Delta.Content == nil || *upstream.chunks[0].Choices[0].Delta.Content != "answer" {
		t.Fatal("upstream fixture mutated")
	}
}

func TestPlatformSingleGenerationDetachedWriterStillCompletes(t *testing.T) {
	runner, store, _, _ := platformSingleTestRunner(nil)
	writes := 0
	result, err := runner.Run(PlatformSingleGenerationInput{User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { writes++; return errors.New("disconnected") }})
	if err != nil || !result.Started || writes != 1 || !store.completeCalled {
		t.Fatalf("result=%+v err=%v writes=%d complete=%v", result, err, writes, store.completeCalled)
	}
}

func TestPlatformSingleGenerationRejectsInvalidClaimResult(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PlatformGenerationClaimResult)
	}{
		{"identity", func(c *PlatformGenerationClaimResult) {
			c.Snapshot.GenerationID = "650e8400-e29b-41d4-a716-446655440000"
		}},
		{"mode", func(c *PlatformGenerationClaimResult) { c.Snapshot.Mode = PlatformGenerationModeCompare }},
		{"model", func(c *PlatformGenerationClaimResult) { c.Snapshot.Models = []string{"other"} }},
		{"state", func(c *PlatformGenerationClaimResult) { c.Snapshot.State = PlatformGenerationStateCommitting }},
		{"lease token", func(c *PlatformGenerationClaimResult) { c.LeaseToken = "bad" }},
		{"lease owner", func(c *PlatformGenerationClaimResult) { c.Snapshot.LeaseOwnerSHA256 = strings.Repeat("0", 64) }},
		{"lease expired", func(c *PlatformGenerationClaimResult) { c.Snapshot.LeaseUntilMillis = 1_000 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner, store, _, _ := platformSingleTestRunner(nil)
			tc.mutate(&store.claim)
			result, err := runner.Run(PlatformSingleGenerationInput{User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }})
			if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || result.Started {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestPlatformSingleGenerationRejectsInvalidDuplicateSnapshot(t *testing.T) {
	runner, store, _, _ := platformSingleTestRunner(nil)
	store.claim.Duplicate = true
	store.claimErr = ErrPlatformGenerationConflict
	store.claim.Snapshot.State = PlatformGenerationStateCompleted
	result, err := runner.Run(PlatformSingleGenerationInput{User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }})
	if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || result.Started || result.Duplicate != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPlatformSingleGenerationExistingConversationOwnership(t *testing.T) {
	for _, tc := range []struct {
		name          string
		loadErr, want error
	}{{"owned", nil, nil}, {"not found", gorm.ErrRecordNotFound, ErrPlatformSingleGenerationInvalid}, {"database", errors.New("db down"), ErrPlatformSingleGenerationUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			runner, _, persist, _ := platformSingleTestRunner(nil)
			guid := "8001"
			persist.receipt.RequestedExistingConversation = true
			runner.deps.loadConversation = func(context.Context, *gorm.DB, int64, int64) error { return tc.loadErr }
			in := PlatformSingleGenerationInput{User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
			in.Params.ConversationGUID = &guid
			result, err := runner.Run(in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("result=%+v err=%v want=%v", result, err, tc.want)
			}
			if tc.want == nil && (persist.input.ConversationGUID == nil || *persist.input.ConversationGUID != 8001 || persist.input.ReservedConversationGUID != nil) {
				t.Fatalf("persistence=%+v", persist.input)
			}
		})
	}
}

func TestPlatformSingleGenerationStrictChunkAndResponseFailures(t *testing.T) {
	content := "x"
	invalidUTF8 := string([]byte{0xff})
	finish := "stop"
	tests := []struct {
		name      string
		configure func(*platformSingleTestRunnerFixture)
		want      error
	}{
		{"nil response", func(f *platformSingleTestRunnerFixture) { f.upstream.response = nil }, ErrPlatformSingleGenerationUpstream},
		{"nil body", func(f *platformSingleTestRunnerFixture) { f.upstream.response = &http.Response{StatusCode: 200} }, ErrPlatformSingleGenerationUpstream},
		{"duplicate usage", func(f *platformSingleTestRunnerFixture) {
			f.upstream.chunks = []whitelabel.ChatCompletionChunk{{Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{Content: &content}, FinishReason: &finish}}}, {Usage: &whitelabel.ChatCompletionUsage{TotalTokens: 1}}, {Usage: &whitelabel.ChatCompletionUsage{TotalTokens: 1}}}
		}, ErrPlatformSingleGenerationUpstream},
		{"choice index", func(f *platformSingleTestRunnerFixture) { f.upstream.chunks[0].Choices[0].Index = 1 }, ErrPlatformSingleGenerationUpstream},
		{"refusal", func(f *platformSingleTestRunnerFixture) {
			refusal := "no"
			f.upstream.chunks[0].Choices[0].Delta.Refusal = &refusal
		}, ErrPlatformSingleGenerationUpstream},
		{"tool call", func(f *platformSingleTestRunnerFixture) {
			f.upstream.chunks[0].Choices[0].Delta.ToolCalls = []whitelabel.ChatCompletionChunkToolCall{{Index: 0}}
		}, ErrPlatformSingleGenerationUpstream},
		{"invalid utf8 delta", func(f *platformSingleTestRunnerFixture) {
			f.upstream.chunks[0].Choices[0].Delta.Content = &invalidUTF8
		}, ErrPlatformSingleGenerationUpstream},
		{"chunk after finish", func(f *platformSingleTestRunnerFixture) {
			f.upstream.chunks = append([]whitelabel.ChatCompletionChunk{f.upstream.chunks[0], f.upstream.chunks[0]}, f.upstream.chunks[1])
		}, ErrPlatformSingleGenerationUpstream},
		{"usage with choices", func(f *platformSingleTestRunnerFixture) {
			f.upstream.chunks[1].Choices = []whitelabel.ChatCompletionChunkChoice{{Index: 0}}
		}, ErrPlatformSingleGenerationUpstream},
		{"negative usage", func(f *platformSingleTestRunnerFixture) {
			f.upstream.chunks[1].Usage.TotalTokens = -1
		}, ErrPlatformSingleGenerationUpstream},
		{"empty completion", func(f *platformSingleTestRunnerFixture) {
			f.upstream.chunks = []whitelabel.ChatCompletionChunk{{Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, FinishReason: &finish}}}, {Usage: &whitelabel.ChatCompletionUsage{TotalTokens: 0}}}
		}, ErrPlatformSingleGenerationUpstream},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlatformSingleTestRunnerFixture()
			tc.configure(f)
			result, err := f.runner.Run(f.input())
			if !errors.Is(err, tc.want) || !result.Started {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestPlatformSingleGenerationClosesResponseOnUpstreamError(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	body := &platformSingleTestReadCloser{Reader: strings.NewReader("ignored")}
	f.upstream.response = &http.Response{StatusCode: http.StatusOK, Body: body}
	f.upstream.chatErr = whitelabel.ErrUpstreamUnavailable("failed")
	result, err := f.runner.Run(f.input())
	if !errors.Is(err, ErrPlatformSingleGenerationUpstream) || !result.Started || !body.closed {
		t.Fatalf("result=%+v err=%v closed=%v", result, err, body.closed)
	}
}

func TestPlatformSingleGenerationRejectsInvalidReceiptBeforeComplete(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	f.persist.receipt.Results[0].AssistantMessageGUID = "invalid"
	writes := 0
	in := f.input()
	in.Write = func([]byte) error { writes++; return nil }
	result, err := f.runner.Run(in)
	if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || !result.Started || f.store.completeCalled || writes != 3 {
		t.Fatalf("result=%+v err=%v complete=%v writes=%d", result, err, f.store.completeCalled, writes)
	}
}

func TestPlatformSingleGenerationAcceptsExactContentLimit(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	content := strings.Repeat("x", platformGenerationMessageTextMaxBytes)
	finish := "stop"
	f.upstream.chunks[0].Choices[0].Delta.Content = &content
	f.upstream.chunks[0].Choices[0].FinishReason = &finish
	f.persist.receipt.Results[0].Content = content
	result, err := f.runner.Run(f.input())
	if err != nil || !result.Started || !f.store.completeCalled || f.persist.input.Results[0].Content != content {
		t.Fatalf("result=%+v err=%v complete=%v persisted=%d", result, err, f.store.completeCalled, len(f.persist.input.Results[0].Content))
	}
}

type platformSingleTestRunnerFixture struct {
	runner   *PlatformSingleGenerationRunner
	store    *platformSingleTestStore
	persist  *platformSingleTestPersistence
	upstream *platformSingleTestUpstream
}

func newPlatformSingleTestRunnerFixture() *platformSingleTestRunnerFixture {
	r, s, p, u := platformSingleTestRunner(nil)
	return &platformSingleTestRunnerFixture{r, s, p, u}
}
func (f *platformSingleTestRunnerFixture) input() PlatformSingleGenerationInput {
	return PlatformSingleGenerationInput{User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
}

func TestPlatformSingleGenerationDeltaStoreFailureDoesNotEmitOrPersist(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	f.store.recordErr = ErrPlatformGenerationUnavailable
	writes := 0
	in := f.input()
	in.Write = func([]byte) error { writes++; return nil }
	result, err := f.runner.Run(in)
	if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || !result.Started || writes != 1 || f.persist.input.GenerationID != "" {
		t.Fatalf("result=%+v err=%v writes=%d persist=%+v", result, err, writes, f.persist.input)
	}
}

func TestPlatformSingleGenerationDeltaOversizeDoesNotStore(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	over := strings.Repeat("x", platformGenerationMessageTextMaxBytes+1)
	finish := "stop"
	f.upstream.chunks = []whitelabel.ChatCompletionChunk{{Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{Content: &over}, FinishReason: &finish}}}}
	result, err := f.runner.Run(f.input())
	if !errors.Is(err, ErrPlatformSingleGenerationOversize) || !result.Started {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
