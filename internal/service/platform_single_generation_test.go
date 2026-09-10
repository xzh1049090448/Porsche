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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

const platformSingleTestGenerationID = "550e8400-e29b-41d4-a716-446655440000"

type platformSingleTestStore struct {
	mu             sync.Mutex
	calls          *[]string
	claim          PlatformGenerationClaimResult
	claimErr       error
	onClaim        func(context.Context, PlatformGenerationClaimInput)
	claimContext   context.Context
	recordErr      error
	record         func(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error)
	renew          func(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error)
	get            func(context.Context, int64, string) (PlatformGenerationSnapshot, error)
	fail           func(context.Context, int64, string, string, string, int64) (PlatformGenerationSnapshot, error)
	ack            func(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error)
	complete       func(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error)
	reconcile      func(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error)
	completeCalled bool
}

func (s *platformSingleTestStore) add(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls != nil {
		*s.calls = append(*s.calls, call)
	}
}
func (s *platformSingleTestStore) Claim(ctx context.Context, in PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error) {
	s.add("claim")
	s.claimContext = ctx
	if s.onClaim != nil {
		s.onClaim(ctx, in)
	}
	return s.claim, s.claimErr
}
func (s *platformSingleTestStore) RecordDeltaOwned(ctx context.Context, userID int64, generationID, token, model string, seq, now int64) (PlatformGenerationSnapshot, error) {
	s.add("record:1")
	if s.record != nil {
		return s.record(ctx, userID, generationID, token, model, seq, now)
	}
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
func (s *platformSingleTestStore) Complete(ctx context.Context, userID int64, generationID string, guids map[string]string, now int64) (PlatformGenerationSnapshot, error) {
	s.add("complete")
	s.mu.Lock()
	s.completeCalled = true
	s.mu.Unlock()
	if s.complete != nil {
		return s.complete(ctx, userID, generationID, guids, now)
	}
	return platformSingleCompletedSnapshot(), nil
}
func (s *platformSingleTestStore) RenewLease(ctx context.Context, userID int64, generationID, token string, now int64) (PlatformGenerationSnapshot, error) {
	s.add("renew")
	if s.renew != nil {
		return s.renew(ctx, userID, generationID, token, now)
	}
	snapshot := s.claim.Snapshot
	snapshot.UpdatedAtMillis = now
	snapshot.LeaseUntilMillis = now + platformGenerationLeaseDuration.Milliseconds()
	return snapshot, nil
}
func (s *platformSingleTestStore) Get(ctx context.Context, userID int64, generationID string) (PlatformGenerationSnapshot, error) {
	s.add("get")
	if s.get != nil {
		return s.get(ctx, userID, generationID)
	}
	return s.claim.Snapshot, nil
}
func (s *platformSingleTestStore) ReconcileComplete(ctx context.Context, userID int64, generationID string, guids map[string]string, now int64) (PlatformGenerationSnapshot, error) {
	s.add("reconcile_complete")
	if s.reconcile != nil {
		return s.reconcile(ctx, userID, generationID, guids, now)
	}
	return platformSingleCompletedSnapshot(), nil
}
func (s *platformSingleTestStore) FailRunningOwned(ctx context.Context, userID int64, generationID, token, code string, now int64) (PlatformGenerationSnapshot, error) {
	s.add("fail:" + code)
	if s.fail != nil {
		return s.fail(ctx, userID, generationID, token, code, now)
	}
	snapshot := s.claim.Snapshot
	snapshot.State = PlatformGenerationStateFailed
	snapshot.ErrorCode = code
	snapshot.LeaseOwnerSHA256 = ""
	snapshot.LeaseUntilMillis = 0
	snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateFailed, ErrorCode: code}
	return snapshot, nil
}
func (s *platformSingleTestStore) AcknowledgeCancelledOwned(ctx context.Context, userID int64, generationID, token string, now int64) (PlatformGenerationSnapshot, error) {
	s.add("ack_cancel")
	if s.ack != nil {
		return s.ack(ctx, userID, generationID, token, now)
	}
	snapshot := s.claim.Snapshot
	snapshot.State = PlatformGenerationStateCancelled
	snapshot.ErrorCode = ""
	snapshot.LeaseOwnerSHA256 = ""
	snapshot.LeaseUntilMillis = 0
	snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateCancelled}
	return snapshot, nil
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

type platformSingleTestRegistry struct {
	calls *[]string
	err   error
}

func (r *platformSingleTestRegistry) Register(int64, string, context.CancelFunc) (string, error) {
	if r.calls != nil {
		*r.calls = append(*r.calls, "register")
	}
	return platformSingleTestLeaseToken(), r.err
}
func (r *platformSingleTestRegistry) Unregister(int64, string, string) bool {
	if r.calls != nil {
		*r.calls = append(*r.calls, "unregister")
	}
	return true
}

type platformSingleTestUpstream struct {
	calls       *[]string
	body        []byte
	response    *http.Response
	chatErr     *whitelabel.Error
	chunks      []whitelabel.ChatCompletionChunk
	consumeErr  *whitelabel.Error
	consume     func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error
	cancelled   atomic.Bool
	chatStarted chan struct{}
	chatOnce    sync.Once
}

type platformSingleTestReadCloser struct {
	io.Reader
	closed     bool
	closeCalls atomic.Int32
}

func (r *platformSingleTestReadCloser) Close() error {
	r.closeCalls.Add(1)
	r.closed = true
	return nil
}

func (u *platformSingleTestUpstream) Chat(_ context.Context, body []byte) (*http.Response, *whitelabel.Error) {
	if u.chatStarted != nil {
		u.chatOnce.Do(func() { close(u.chatStarted) })
	}
	if u.calls != nil {
		*u.calls = append(*u.calls, "upstream")
	}
	u.body = append([]byte(nil), body...)
	return u.response, u.chatErr
}
func (u *platformSingleTestUpstream) ConsumeChatCompletionSSEContext(ctx context.Context, _ io.Reader, _ string, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
	if u.consume != nil {
		return u.consume(ctx, emit)
	}
	for _, chunk := range u.chunks {
		if err := emit(chunk); err != nil {
			return whitelabel.ErrUpstreamUnavailable("callback")
		}
	}
	return u.consumeErr
}

func platformSingleCompletedSnapshot() PlatformGenerationSnapshot {
	snapshot := platformSingleTestClaim(1_000, "model-a").Snapshot
	snapshot.State = PlatformGenerationStateCompleted
	snapshot.LeaseOwnerSHA256 = ""
	snapshot.LeaseUntilMillis = 0
	snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateCompleted, Seq: 1, AssistantMessageGUID: "9001"}
	return snapshot
}

type platformSingleTestTimer struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func (t *platformSingleTestTimer) Chan() <-chan time.Time { return t.ch }
func (t *platformSingleTestTimer) Stop()                  { t.stopped.Store(true) }

type platformSingleTestTimerFactory struct {
	created chan *platformSingleTestTimer
}

func (f *platformSingleTestTimerFactory) New(duration time.Duration) platformSingleTimer {
	if duration != 10*time.Second {
		panic("unexpected timer duration")
	}
	timer := &platformSingleTestTimer{ch: make(chan time.Time, 1)}
	f.created <- timer
	return timer
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
		newTimer:         newPlatformSingleTimer,
		newRunnerContext: context.WithTimeout,
	}}
	return runner, store, persist, upstream
}

func TestPlatformSingleGenerationNewConversationSuccess(t *testing.T) {
	var calls []string
	runner, store, persist, upstream := platformSingleTestRunner(&calls)
	var output bytes.Buffer
	result, err := runner.Run(PlatformSingleGenerationInput{Context: context.Background(), User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, RequestID: "request-1", Params: platformSingleTestParams("hello"), Write: func(frame []byte) error {
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
	result, err := runner.Run(PlatformSingleGenerationInput{Context: context.Background(), User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { t.Fatal("unexpected output"); return nil }})
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
		{"nil context", func(in *PlatformSingleGenerationInput) { in.Context = nil }, ErrPlatformSingleGenerationInvalid},
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
			in := PlatformSingleGenerationInput{Context: context.Background(), User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
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

func TestPlatformSingleGenerationAdmissionCancellationStopsBeforeClaim(t *testing.T) {
	t.Run("pre-cancelled", func(t *testing.T) {
		var calls []string
		runner, _, _, _ := platformSingleTestRunner(&calls)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		in := PlatformSingleGenerationInput{Context: ctx, User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
		result, err := runner.Run(in)
		if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || result.Started || len(calls) != 0 {
			t.Fatalf("result=%+v err=%v calls=%v", result, err, calls)
		}
	})

	t.Run("cancelled after preflight", func(t *testing.T) {
		var calls []string
		runner, _, _, _ := platformSingleTestRunner(&calls)
		ctx, cancel := context.WithCancel(context.Background())
		runner.deps.newGUID = func() int64 { cancel(); return 8001 }
		in := PlatformSingleGenerationInput{Context: ctx, User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
		result, err := runner.Run(in)
		if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || result.Started {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		for _, call := range calls {
			if call == "claim" || call == "register" || call == "upstream" {
				t.Fatalf("post-cancel call: %v", calls)
			}
		}
	})

	t.Run("cancelled ownership query", func(t *testing.T) {
		var calls []string
		runner, _, _, _ := platformSingleTestRunner(&calls)
		ctx, cancel := context.WithCancel(context.Background())
		runner.deps.loadConversation = func(queryCtx context.Context, _ *gorm.DB, _, _ int64) error { cancel(); return queryCtx.Err() }
		guid := "8001"
		in := PlatformSingleGenerationInput{Context: ctx, User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
		in.Params.ConversationGUID = &guid
		result, err := runner.Run(in)
		if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || result.Started {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		for _, call := range calls {
			if call == "claim" || call == "register" || call == "upstream" {
				t.Fatalf("post-cancel call: %v", calls)
			}
		}
	})
}

func TestPlatformSingleGenerationRequestCancellationAfterClaimDoesNotStopRunner(t *testing.T) {
	var calls []string
	runner, store, _, _ := platformSingleTestRunner(&calls)
	ctx, cancel := context.WithCancel(context.Background())
	store.onClaim = func(claimCtx context.Context, _ PlatformGenerationClaimInput) {
		if claimCtx != ctx {
			t.Fatalf("claim context does not match admission context")
		}
		cancel()
	}
	in := PlatformSingleGenerationInput{Context: ctx, User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
	result, err := runner.Run(in)
	if err != nil || !result.Started || !store.completeCalled {
		t.Fatalf("result=%+v err=%v complete=%v calls=%v", result, err, store.completeCalled, calls)
	}
}

func TestPlatformSingleGenerationCopiesRequestBeforeClaim(t *testing.T) {
	var calls []string
	runner, store, persist, upstream := platformSingleTestRunner(&calls)
	params := platformSingleTestParams("hello")
	user := platformSingleTestUser()
	store.onClaim = func(_ context.Context, in PlatformGenerationClaimInput) {
		params.Model = "mutated"
		params.Messages[0]["content"] = "mutated"
		params.WhiteLabelBody[0] = '['
		user.ID = 99
		if !reflect.DeepEqual(in.Models, []string{"model-a"}) {
			t.Fatalf("claim models aliased: %v", in.Models)
		}
	}
	result, err := runner.Run(PlatformSingleGenerationInput{Context: context.Background(), User: user, GenerationID: platformSingleTestGenerationID, Params: params, Write: func([]byte) error { return nil }})
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
	result, err := runner.Run(PlatformSingleGenerationInput{Context: context.Background(), User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { writes++; return errors.New("disconnected") }})
	if err != nil || !result.Started || writes != 1 || !store.completeCalled {
		t.Fatalf("result=%+v err=%v writes=%d complete=%v", result, err, writes, store.completeCalled)
	}
}

func TestPlatformSingleGenerationRequestCancelAndSecondWriteFailureOnlyDetachOutput(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	f.store.onClaim = func(context.Context, PlatformGenerationClaimInput) { cancelRequest() }
	writes := 0
	in := f.input()
	in.Context = requestCtx
	in.Write = func([]byte) error {
		writes++
		if writes == 2 {
			return io.ErrClosedPipe
		}
		return nil
	}
	result, err := f.runner.Run(in)
	if err != nil || !result.Started || writes != 2 || !f.store.completeCalled || f.persist.input.GenerationID == "" {
		t.Fatalf("result=%+v err=%v writes=%d complete=%v persistence=%+v", result, err, writes, f.store.completeCalled, f.persist.input)
	}
	if f.upstream.cancelled.Load() {
		t.Fatal("request cancellation cancelled the application runner")
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
			result, err := runner.Run(PlatformSingleGenerationInput{Context: context.Background(), User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }})
			if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || result.Started {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestPlatformSingleGenerationRegistrationFailureDoesNotOverwriteCommitting(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	f.runner.deps.registry = &platformSingleTestRegistry{err: errors.New("registry closed")}
	committing := clonePlatformGeneration(f.store.claim.Snapshot)
	committing.State = PlatformGenerationStateCommitting
	committing.LeaseOwnerSHA256 = ""
	committing.LeaseUntilMillis = 0
	committing.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateCompleted, Seq: 1}
	f.store.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) { return committing, nil }
	var failCalled atomic.Bool
	f.store.fail = func(context.Context, int64, string, string, string, int64) (PlatformGenerationSnapshot, error) {
		failCalled.Store(true)
		return PlatformGenerationSnapshot{}, nil
	}
	result, err := f.runner.Run(f.input())
	if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || result.Started || failCalled.Load() {
		t.Fatalf("result=%+v err=%v fail_called=%v", result, err, failCalled.Load())
	}
}

func TestPlatformSingleGenerationRejectsInvalidDuplicateSnapshot(t *testing.T) {
	runner, store, _, _ := platformSingleTestRunner(nil)
	store.claim.Duplicate = true
	store.claimErr = ErrPlatformGenerationConflict
	store.claim.Snapshot.State = PlatformGenerationStateCompleted
	result, err := runner.Run(PlatformSingleGenerationInput{Context: context.Background(), User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }})
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
			in := PlatformSingleGenerationInput{Context: context.Background(), User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
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
	if !errors.Is(err, ErrPlatformSingleGenerationUpstream) || !result.Started || !body.closed || body.closeCalls.Load() != 1 {
		t.Fatalf("result=%+v err=%v closed=%v close_calls=%d", result, err, body.closed, body.closeCalls.Load())
	}
}

func TestPlatformSingleGenerationRejectsInvalidReceiptBeforeComplete(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	f.persist.receipt.Results[0].AssistantMessageGUID = "invalid"
	writes := 0
	in := f.input()
	in.Write = func([]byte) error { writes++; return nil }
	result, err := f.runner.Run(in)
	if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || !result.Started || f.store.completeCalled || writes != 4 {
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
	return PlatformSingleGenerationInput{Context: context.Background(), User: platformSingleTestUser(), GenerationID: platformSingleTestGenerationID, Params: platformSingleTestParams("hello"), Write: func([]byte) error { return nil }}
}

func TestPlatformSingleGenerationDeltaStoreFailureDoesNotEmitOrPersist(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	f.store.recordErr = ErrPlatformGenerationUnavailable
	writes := 0
	in := f.input()
	in.Write = func([]byte) error { writes++; return nil }
	result, err := f.runner.Run(in)
	if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || !result.Started || writes != 3 || f.persist.input.GenerationID != "" {
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

func TestPlatformSingleGenerationRenewsEveryTenSecondsWithoutSleeping(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	var now atomic.Int64
	now.Store(1_000)
	f.runner.deps.now = func() time.Time { return time.UnixMilli(now.Load()).UTC() }
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 4)}
	f.runner.deps.newTimer = timers.New
	release := make(chan struct{})
	f.upstream.consume = func(ctx context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
		select {
		case <-release:
			for _, chunk := range f.upstream.chunks {
				if err := emit(chunk); err != nil {
					return whitelabel.ErrUpstreamUnavailable("callback")
				}
			}
			return nil
		case <-ctx.Done():
			f.upstream.cancelled.Store(true)
			return whitelabel.ErrUpstreamUnavailable("cancelled")
		}
	}
	renewed := make(chan struct{}, 1)
	f.store.renew = func(_ context.Context, _ int64, _ string, token string, now int64) (PlatformGenerationSnapshot, error) {
		if token != platformSingleTestLeaseToken() {
			t.Fatalf("renew token changed")
		}
		snapshot := f.store.claim.Snapshot
		snapshot.UpdatedAtMillis = now
		snapshot.LeaseUntilMillis = now + platformGenerationLeaseDuration.Milliseconds()
		renewed <- struct{}{}
		return snapshot, nil
	}
	done := make(chan error, 1)
	go func() { _, err := f.runner.Run(f.input()); done <- err }()
	first := <-timers.created
	now.Store(11_000)
	first.ch <- time.UnixMilli(11_000)
	<-renewed
	second := <-timers.created
	if !first.stopped.Load() || second.stopped.Load() {
		t.Fatalf("timer lifecycle first=%v second=%v", first.stopped.Load(), second.stopped.Load())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Run() err=%v", err)
	}
	if !second.stopped.Load() {
		t.Fatal("last renewal timer was not stopped")
	}
}

func TestPlatformSingleGenerationMalformedRenewalStopsWithoutFailMutation(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 2)}
	f.runner.deps.newTimer = timers.New
	f.upstream.consume = blockingPlatformSingleConsume(f.upstream)
	f.store.renew = func(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error) {
		return PlatformGenerationSnapshot{GenerationID: "malformed"}, nil
	}
	var getCalled, failCalled atomic.Bool
	f.store.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
		getCalled.Store(true)
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	f.store.fail = func(context.Context, int64, string, string, string, int64) (PlatformGenerationSnapshot, error) {
		failCalled.Store(true)
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	var frames bytes.Buffer
	in := f.input()
	in.Write = func(frame []byte) error { _, _ = frames.Write(frame); return nil }
	done := make(chan error, 1)
	go func() { _, err := f.runner.Run(in); done <- err }()
	timer := <-timers.created
	timer.ch <- time.UnixMilli(11_000)
	if err := <-done; !errors.Is(err, ErrPlatformSingleGenerationUnavailable) {
		t.Fatalf("Run() err=%v", err)
	}
	if getCalled.Load() || failCalled.Load() {
		t.Fatalf("malformed renewal retrusted authority get=%v fail=%v", getCalled.Load(), failCalled.Load())
	}
	if got := platformSingleTestEventNames(frames.Bytes()); !reflect.DeepEqual(got, []string{"meta", "model_error", "error"}) {
		t.Fatalf("events=%v", got)
	}
}

func TestPlatformSingleGenerationRenewalCancellingSnapshotIsAcknowledgedWithoutReload(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 2)}
	f.runner.deps.newTimer = timers.New
	f.upstream.consume = blockingPlatformSingleConsume(f.upstream)
	cancelling := f.store.claim.Snapshot
	cancelling.State = PlatformGenerationStateCancelling
	cancelling.LeaseUntilMillis = 0
	f.store.renew = func(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error) {
		return cancelling, ErrPlatformGenerationConflict
	}
	var getCalled atomic.Bool
	f.store.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
		getCalled.Store(true)
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	done := make(chan error, 1)
	go func() { _, err := f.runner.Run(f.input()); done <- err }()
	timer := <-timers.created
	timer.ch <- time.UnixMilli(11_000)
	if err := <-done; !errors.Is(err, ErrPlatformSingleGenerationUpstream) {
		t.Fatalf("Run() err=%v", err)
	}
	if getCalled.Load() {
		t.Fatal("authoritative renewal snapshot was reloaded")
	}
}

func TestPlatformSingleGenerationSerializesDeltaAndRenewalMutations(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 2)}
	f.runner.deps.newTimer = timers.New
	var active, overlap atomic.Int32
	recordEntered := make(chan struct{})
	releaseRecord := make(chan struct{})
	f.store.record = func(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error) {
		if active.Add(1) != 1 {
			overlap.Store(1)
		}
		close(recordEntered)
		<-releaseRecord
		active.Add(-1)
		return f.store.claim.Snapshot, nil
	}
	f.store.renew = func(_ context.Context, _ int64, _ string, _ string, now int64) (PlatformGenerationSnapshot, error) {
		if active.Add(1) != 1 {
			overlap.Store(1)
		}
		active.Add(-1)
		snapshot := f.store.claim.Snapshot
		snapshot.UpdatedAtMillis = now
		snapshot.LeaseUntilMillis = now + platformGenerationLeaseDuration.Milliseconds()
		return snapshot, nil
	}
	done := make(chan error, 1)
	go func() { _, err := f.runner.Run(f.input()); done <- err }()
	timer := <-timers.created
	<-recordEntered
	timer.ch <- time.UnixMilli(11_000)
	close(releaseRecord)
	if err := <-done; err != nil {
		t.Fatalf("Run() err=%v", err)
	}
	if overlap.Load() != 0 {
		t.Fatal("runner-owned store mutations overlapped")
	}
}

func TestPlatformSingleGenerationCancelAcknowledgesAuthorityAndStopsUpstream(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	registry := &platformSingleCapturingRegistry{registered: make(chan context.CancelFunc, 1)}
	f.runner.deps.registry = registry
	f.runner.deps.newTimer = (&platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 1)}).New
	f.upstream.consume = blockingPlatformSingleConsume(f.upstream)
	cancelling := f.store.claim.Snapshot
	cancelling.State = PlatformGenerationStateCancelling
	cancelling.LeaseUntilMillis = 0
	f.store.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) { return cancelling, nil }
	var frames bytes.Buffer
	in := f.input()
	in.Write = func(frame []byte) error { _, _ = frames.Write(frame); return nil }
	done := make(chan error, 1)
	go func() { _, err := f.runner.Run(in); done <- err }()
	cancel := <-registry.registered
	cancel()
	if err := <-done; !errors.Is(err, ErrPlatformSingleGenerationUpstream) {
		t.Fatalf("Run() err=%v", err)
	}
	if !f.upstream.cancelled.Load() {
		t.Fatal("upstream was not cancelled")
	}
	if got := platformSingleTestEventNames(frames.Bytes()); !reflect.DeepEqual(got, []string{"meta", "model_error", "error"}) {
		t.Fatalf("events=%v", got)
	}
	if f.persist.input.GenerationID != "" {
		t.Fatalf("unexpected persistence=%+v", f.persist.input)
	}
}

func TestPlatformSingleGenerationShutdownFailsOnlyProvenRunningAuthority(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	root, shutdown := context.WithCancel(context.Background())
	f.runner.deps.rootContext = root
	f.runner.deps.newTimer = (&platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 1)}).New
	f.upstream.chatStarted = make(chan struct{})
	f.upstream.consume = blockingPlatformSingleConsume(f.upstream)
	f.store.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
		return f.store.claim.Snapshot, nil
	}
	var code string
	f.store.fail = func(_ context.Context, _ int64, _ string, _ string, got string, _ int64) (PlatformGenerationSnapshot, error) {
		code = got
		snapshot := f.store.claim.Snapshot
		snapshot.State = PlatformGenerationStateFailed
		snapshot.ErrorCode = got
		snapshot.LeaseOwnerSHA256 = ""
		snapshot.LeaseUntilMillis = 0
		snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateFailed, ErrorCode: got}
		return snapshot, nil
	}
	done := make(chan error, 1)
	go func() { _, err := f.runner.Run(f.input()); done <- err }()
	<-f.upstream.chatStarted
	shutdown()
	if err := <-done; !errors.Is(err, ErrPlatformSingleGenerationUpstream) {
		t.Fatalf("Run() err=%v", err)
	}
	if code != "internal_error" || !f.upstream.cancelled.Load() {
		t.Fatalf("code=%q cancelled=%v", code, f.upstream.cancelled.Load())
	}
}

func TestPlatformSingleGenerationShutdownPreservesCommittingAuthority(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	root, shutdown := context.WithCancel(context.Background())
	f.runner.deps.rootContext = root
	f.runner.deps.newTimer = (&platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 1)}).New
	f.upstream.chatStarted = make(chan struct{})
	f.upstream.consume = blockingPlatformSingleConsume(f.upstream)
	committing := clonePlatformGeneration(f.store.claim.Snapshot)
	committing.State = PlatformGenerationStateCommitting
	committing.LeaseOwnerSHA256 = ""
	committing.LeaseUntilMillis = 0
	committing.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateCompleted, Seq: 1}
	f.store.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) { return committing, nil }
	var failCalled atomic.Bool
	f.store.fail = func(context.Context, int64, string, string, string, int64) (PlatformGenerationSnapshot, error) {
		failCalled.Store(true)
		return PlatformGenerationSnapshot{}, nil
	}
	var frames bytes.Buffer
	in := f.input()
	in.Write = func(frame []byte) error { _, _ = frames.Write(frame); return nil }
	done := make(chan error, 1)
	go func() { _, err := f.runner.Run(in); done <- err }()
	<-f.upstream.chatStarted
	shutdown()
	if err := <-done; !errors.Is(err, ErrPlatformSingleGenerationUnavailable) {
		t.Fatalf("Run() err=%v", err)
	}
	if failCalled.Load() {
		t.Fatal("shutdown overwrote committing authority")
	}
	if got := platformSingleTestEventNames(frames.Bytes()); !reflect.DeepEqual(got, []string{"meta", "error"}) {
		t.Fatalf("events=%v", got)
	}
}

func TestPlatformSingleGenerationFailureCodesAndTerminalOrder(t *testing.T) {
	over := strings.Repeat("x", platformGenerationMessageTextMaxBytes+1)
	finish := "stop"
	tests := []struct {
		name, code string
		events     []string
		configure  func(*platformSingleTestRunnerFixture)
	}{
		{name: "timeout", code: "timeout", events: []string{"meta", "model_error", "error"}, configure: func(f *platformSingleTestRunnerFixture) {
			f.runner.deps.newRunnerContext = func(context.Context, time.Duration) (context.Context, context.CancelFunc) {
				return platformSingleExpiredContext{}, func() {}
			}
			f.upstream.consume = blockingPlatformSingleConsume(f.upstream)
		}},
		{name: "malformed", code: "gateway_upstream_error", events: []string{"meta", "delta", "model_error", "error"}, configure: func(f *platformSingleTestRunnerFixture) {
			f.upstream.consumeErr = whitelabel.ErrUpstreamUnavailable("raw secret")
		}},
		{name: "early eof", code: "gateway_upstream_error", events: []string{"meta", "model_error", "error"}, configure: func(f *platformSingleTestRunnerFixture) { f.upstream.chunks = nil }},
		{name: "oversize", code: "upstream_error", events: []string{"meta", "model_error", "error"}, configure: func(f *platformSingleTestRunnerFixture) {
			f.upstream.chunks = []whitelabel.ChatCompletionChunk{{Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{Content: &over}, FinishReason: &finish}}}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlatformSingleTestRunnerFixture()
			tc.configure(f)
			var frames bytes.Buffer
			in := f.input()
			in.Write = func(frame []byte) error { _, _ = frames.Write(frame); return nil }
			result, err := f.runner.Run(in)
			if err == nil || !result.Started {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if got := platformSingleTestEventNames(frames.Bytes()); !reflect.DeepEqual(got, tc.events) {
				t.Fatalf("events=%v", got)
			}
			if count := strings.Count(frames.String(), `"code":"`+tc.code+`"`); count != 2 {
				t.Fatalf("code count=%d stream=%q", count, frames.String())
			}
			if strings.Contains(frames.String(), "raw secret") || f.persist.input.GenerationID != "" {
				t.Fatalf("leak or persistence stream=%q persist=%+v", frames.String(), f.persist.input)
			}
		})
	}
}

type platformSingleExpiredContext struct{ context.Context }

func (platformSingleExpiredContext) Deadline() (time.Time, bool) { return time.Unix(0, 0), true }
func (platformSingleExpiredContext) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (platformSingleExpiredContext) Err() error { return context.DeadlineExceeded }
func (platformSingleExpiredContext) Value(key interface{}) interface{} {
	return context.Background().Value(key)
}

func TestPlatformSingleGenerationCommitUnknownReconcilesWithoutReplayingSQL(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	f.runner.deps.newTimer = (&platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 1)}).New
	f.store.complete = func(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	f.store.reconcile = func(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error) {
		return platformSingleCompletedSnapshot(), nil
	}
	writes := 0
	in := f.input()
	in.Write = func([]byte) error { writes++; return nil }
	result, err := f.runner.Run(in)
	if err != nil || !result.Started || writes != 4 {
		t.Fatalf("result=%+v err=%v writes=%d", result, err, writes)
	}
	if f.persist.input.GenerationID == "" {
		t.Fatal("Finalize not called")
	}
}

func TestPlatformSingleGenerationUnprovenCompletionNeverEmitsDone(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	f.runner.deps.newTimer = (&platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 1)}).New
	f.store.complete = func(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	f.store.reconcile = func(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	var frames bytes.Buffer
	in := f.input()
	in.Write = func(frame []byte) error { _, _ = frames.Write(frame); return nil }
	result, err := f.runner.Run(in)
	if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || !result.Started {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := platformSingleTestEventNames(frames.Bytes()); !reflect.DeepEqual(got, []string{"meta", "delta", "model_done", "error"}) {
		t.Fatalf("events=%v", got)
	}
}

func TestPlatformSingleGenerationUnsafeDoneTotalEmitsOnlyGlobalError(t *testing.T) {
	f := newPlatformSingleTestRunnerFixture()
	f.runner.deps.loadTotalTokens = func(context.Context, *gorm.DB, int64) (int64, error) { return platformSSEV2MaxSafeInteger + 1, nil }
	var frames bytes.Buffer
	in := f.input()
	in.Write = func(frame []byte) error { _, _ = frames.Write(frame); return nil }
	result, err := f.runner.Run(in)
	if !errors.Is(err, ErrPlatformSingleGenerationUnavailable) || !result.Started {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := platformSingleTestEventNames(frames.Bytes()); !reflect.DeepEqual(got, []string{"meta", "delta", "model_done", "error"}) {
		t.Fatalf("events=%v", got)
	}
}

type platformSingleCapturingRegistry struct{ registered chan context.CancelFunc }

func (r *platformSingleCapturingRegistry) Register(_ int64, _ string, cancel context.CancelFunc) (string, error) {
	r.registered <- cancel
	return platformSingleTestLeaseToken(), nil
}
func (r *platformSingleCapturingRegistry) Unregister(int64, string, string) bool { return true }

func blockingPlatformSingleConsume(upstream *platformSingleTestUpstream) func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
	return func(ctx context.Context, _ func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
		<-ctx.Done()
		upstream.cancelled.Store(true)
		return whitelabel.ErrUpstreamUnavailable("cancelled")
	}
}

func platformSingleTestEventNames(stream []byte) []string {
	var names []string
	for _, line := range strings.Split(string(stream), "\n") {
		if strings.HasPrefix(line, "event: ") {
			names = append(names, strings.TrimPrefix(line, "event: "))
		}
	}
	return names
}
