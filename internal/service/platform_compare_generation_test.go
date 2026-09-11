package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

const platformCompareTestGenerationID = "93000000-0000-4000-8000-000000000001"

type platformCompareTestEffects struct {
	claim, get, fail, record, modelDone, modelFailed, persist, admission, admissionRelease, register, unregister, upstream, receipt, write, guid, runnerCancel int
}

type platformCompareTestStore struct {
	effects        *platformCompareTestEffects
	claim          PlatformGenerationClaimResult
	claimErr       error
	claimInput     PlatformGenerationClaimInput
	claimContext   context.Context
	onClaim        func(context.Context, PlatformGenerationClaimInput)
	getSnapshot    PlatformGenerationSnapshot
	getErr         error
	failSnapshot   PlatformGenerationSnapshot
	failErr        error
	failLease      string
	failCode       string
	failNowMillis  int64
	getUserID      int64
	getGeneration  string
	failUserID     int64
	failGeneration string
}

func (s *platformCompareTestStore) Claim(ctx context.Context, input PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error) {
	s.effects.claim++
	s.claimContext, s.claimInput = ctx, input
	if s.onClaim != nil {
		s.onClaim(ctx, input)
	}
	if s.claimErr != nil || s.claim.Snapshot.GenerationID != "" || s.claim.Duplicate {
		return s.claim, s.claimErr
	}
	return PlatformGenerationClaimResult{}, errors.New("unexpected claim")
}

func (s *platformCompareTestStore) Get(_ context.Context, userID int64, generationID string) (PlatformGenerationSnapshot, error) {
	s.effects.get++
	s.getUserID, s.getGeneration = userID, generationID
	return s.getSnapshot, s.getErr
}

func (s *platformCompareTestStore) FailRunningOwned(_ context.Context, userID int64, generationID, leaseToken, code string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	s.effects.fail++
	s.failUserID, s.failGeneration = userID, generationID
	s.failLease, s.failCode, s.failNowMillis = leaseToken, code, nowMillis
	return s.failSnapshot, s.failErr
}
func (s *platformCompareTestStore) RecordDeltaOwned(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error) {
	s.effects.record++
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformCompareTestStore) MarkModelDoneOwned(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error) {
	s.effects.modelDone++
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformCompareTestStore) MarkModelFailedOwned(context.Context, int64, string, string, string, string, int64) (PlatformGenerationSnapshot, error) {
	s.effects.modelFailed++
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformCompareTestStore) RenewLease(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error) {
	return PlatformGenerationSnapshot{}, errors.New("unexpected renewal")
}
func (s *platformCompareTestStore) AcknowledgeCancelledOwned(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error) {
	return PlatformGenerationSnapshot{}, errors.New("unexpected cancellation acknowledgement")
}
func (s *platformCompareTestStore) ReconcileComplete(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error) {
	return PlatformGenerationSnapshot{}, errors.New("unexpected completion reconciliation")
}

type platformCompareTestPersistence struct{ effects *platformCompareTestEffects }

func (p *platformCompareTestPersistence) Finalize(context.Context, *gorm.DB, PlatformGenerationPersistenceInput) (PlatformGenerationReceiptSnapshot, error) {
	p.effects.persist++
	return PlatformGenerationReceiptSnapshot{}, errors.New("unexpected persistence")
}

type platformCompareTestRegistry struct {
	effects         *platformCompareTestEffects
	onBegin         func()
	beginErr        error
	registerErr     error
	registeredUID   int64
	registeredID    string
	registered      context.CancelFunc
	onRegister      func()
	token           string
	unregisterUID   int64
	unregisterID    string
	unregisterToken string
}

func (r *platformCompareTestRegistry) BeginAdmission() (func(), error) {
	r.effects.admission++
	if r.onBegin != nil {
		r.onBegin()
	}
	if r.beginErr != nil {
		return nil, r.beginErr
	}
	return func() { r.effects.admissionRelease++ }, nil
}
func (r *platformCompareTestRegistry) Register(userID int64, generationID string, cancel context.CancelFunc) (string, error) {
	r.effects.register++
	r.registeredUID, r.registeredID, r.registered = userID, generationID, cancel
	if r.onRegister != nil {
		r.onRegister()
	}
	if r.registerErr != nil {
		return "", r.registerErr
	}
	if r.token == "" {
		r.token = "compare-registration"
	}
	return r.token, nil
}
func (r *platformCompareTestRegistry) Unregister(userID int64, generationID, token string) bool {
	r.effects.unregister++
	r.unregisterUID, r.unregisterID, r.unregisterToken = userID, generationID, token
	return userID == r.registeredUID && generationID == r.registeredID && token == r.token
}

type platformCompareTestUpstream struct {
	mu      sync.Mutex
	effects *platformCompareTestEffects
}

func (u *platformCompareTestUpstream) Chat(context.Context, []byte) (*http.Response, *whitelabel.Error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.effects.upstream++
	return nil, whitelabel.ErrUpstreamUnavailable("unexpected upstream")
}
func (*platformCompareTestUpstream) ConsumeChatCompletionSSEContext(context.Context, io.Reader, string, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
	return whitelabel.ErrUpstreamUnavailable("unexpected upstream consume")
}

func platformCompareTestUser() *models.User {
	return &models.User{
		ID: 17, AuditFields: models.AuditFields{IsDeleted: 0}, Status: models.UserStatusActive,
		PlanType: models.PlanFree, DailyCallLimit: 10, DailyCallsUsed: 1,
	}
}

func platformCompareTestParams() ChatParams {
	maxTokens := 64
	contextWindow := 1
	return ChatParams{
		Messages: []map[string]interface{}{
			{"role": "system", "content": "system", "nested": map[string]interface{}{"items": []interface{}{"original"}}},
			{"role": "user", "content": "hello"},
		},
		MaxTokens:      &maxTokens,
		ContextWindow:  &contextWindow,
		WhiteLabelBody: []byte(`{"model":"ignored","models":["model-a","model-b"],"messages":[{"role":"user","content":"stale"}],"max_tokens":64,"stream":true,"stream_version":"platform-chat-sse.v2","generation_id":"93000000-0000-4000-8000-000000000001"}`),
	}
}

func platformCompareTestInput() PlatformCompareGenerationInput {
	return PlatformCompareGenerationInput{
		Context: context.Background(), User: platformCompareTestUser(), GenerationID: platformCompareTestGenerationID,
		RequestID: "request-compare", Models: []string{"model-b", "model-a"}, Params: platformCompareTestParams(), Write: func([]byte) error { return nil },
	}
}

func platformCompareTestRunner(now time.Time) (*PlatformCompareGenerationRunner, *platformCompareTestEffects) {
	effects := &platformCompareTestEffects{}
	deps := platformCompareGenerationDeps{
		db: &gorm.DB{}, store: &platformCompareTestStore{effects: effects}, persistence: &platformCompareTestPersistence{effects: effects},
		registry: &platformCompareTestRegistry{effects: effects}, upstream: &platformCompareTestUpstream{effects: effects}, rootContext: context.Background(),
		now: func() time.Time { return now }, newGUID: func() int64 { effects.guid++; return 8101 },
		loadConversation: func(context.Context, *gorm.DB, int64, int64) error { return nil },
		loadReceipt: func(context.Context, *gorm.DB, int64, string) (PlatformGenerationReceiptSnapshot, error) {
			effects.receipt++
			return PlatformGenerationReceiptSnapshot{}, errors.New("unexpected receipt")
		},
		upstreamTimeout: time.Minute,
		newRunnerContext: func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithTimeout(parent, timeout)
			return ctx, func() { effects.runnerCancel++; cancel() }
		},
		newEncoder: func(generationID string, models []string) (platformCompareEncoder, error) {
			return NewPlatformSSEV2Encoder(generationID, models)
		},
		newTimer: newPlatformSingleTimer,
	}
	return &PlatformCompareGenerationRunner{deps: deps}, effects
}

func requireNoPlatformComparePostPrepareEffects(t *testing.T, effects *platformCompareTestEffects) {
	t.Helper()
	if effects.claim != 0 || effects.get != 0 || effects.fail != 0 || effects.record != 0 || effects.modelDone != 0 || effects.modelFailed != 0 || effects.persist != 0 || effects.admission != 0 || effects.admissionRelease != 0 || effects.register != 0 || effects.unregister != 0 || effects.upstream != 0 || effects.receipt != 0 || effects.write != 0 || effects.runnerCancel != 0 {
		t.Fatalf("unexpected post-prepare effects: %+v", effects)
	}
}

func TestPlatformCompareGenerationRejectsInvalidDependencies(t *testing.T) {
	db := &gorm.DB{}
	store := &PlatformGenerationStore{}
	persistence := &PlatformGenerationPersistence{}
	registry := &PlatformGenerationCancellationRegistry{}
	upstream := &whitelabel.WhiteLabelService{}
	valid := func(db *gorm.DB, store *PlatformGenerationStore, persistence *PlatformGenerationPersistence, registry *PlatformGenerationCancellationRegistry, upstream *whitelabel.WhiteLabelService, root context.Context, timeout time.Duration) error {
		_, err := NewPlatformCompareGenerationRunner(db, store, persistence, registry, upstream, root, timeout)
		return err
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{"nil db", func() error {
			return valid(nil, store, persistence, registry, upstream, context.Background(), time.Minute)
		}},
		{"nil store", func() error {
			return valid(db, nil, persistence, registry, upstream, context.Background(), time.Minute)
		}},
		{"nil persistence", func() error { return valid(db, store, nil, registry, upstream, context.Background(), time.Minute) }},
		{"nil registry", func() error { return valid(db, store, persistence, nil, upstream, context.Background(), time.Minute) }},
		{"nil upstream", func() error { return valid(db, store, persistence, registry, nil, context.Background(), time.Minute) }},
		{"nil root context", func() error { return valid(db, store, persistence, registry, upstream, nil, time.Minute) }},
		{"zero timeout", func() error { return valid(db, store, persistence, registry, upstream, context.Background(), 0) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, ErrPlatformCompareGenerationUnavailable) {
				t.Fatalf("constructor error=%v, want unavailable", err)
			}
		})
	}
	runner, err := NewPlatformCompareGenerationRunner(db, store, persistence, registry, upstream, context.Background(), time.Minute)
	if err != nil || runner == nil || runner.deps.loadReceipt == nil {
		t.Fatalf("valid constructor runner=%#v error=%v", runner, err)
	}
	runner.deps.loadReceipt = nil
	if _, err := runner.Run(platformCompareTestInput()); !errors.Is(err, ErrPlatformCompareGenerationUnavailable) {
		t.Fatalf("nil receipt reader Run error=%v, want unavailable", err)
	}
}

func TestPlatformCompareGenerationPreflightRejectsBeforeClaim(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	maxContext := int(^uint(0) >> 1)
	overMessage := strings.Repeat("x", platformGenerationMessageTextMaxBytes+1)
	tests := []struct {
		name   string
		mutate func(*PlatformCompareGenerationInput)
		want   error
	}{
		{"nil context", func(in *PlatformCompareGenerationInput) { in.Context = nil }, ErrPlatformCompareGenerationInvalid},
		{"cancelled context", func(in *PlatformCompareGenerationInput) {
			ctx, cancel := context.WithCancel(in.Context)
			cancel()
			in.Context = ctx
		}, ErrPlatformCompareGenerationUnavailable},
		{"nil user", func(in *PlatformCompareGenerationInput) { in.User = nil }, ErrPlatformCompareGenerationInvalid},
		{"nil write", func(in *PlatformCompareGenerationInput) { in.Write = nil }, ErrPlatformCompareGenerationInvalid},
		{"uppercase generation", func(in *PlatformCompareGenerationInput) { in.GenerationID = "93000000-0000-4000-8000-00000000000A" }, ErrPlatformCompareGenerationInvalid},
		{"last assistant", func(in *PlatformCompareGenerationInput) { in.Params.Messages[1]["role"] = "assistant" }, ErrPlatformCompareGenerationInvalid},
		{"empty final message", func(in *PlatformCompareGenerationInput) { in.Params.Messages[1]["content"] = "" }, ErrPlatformCompareGenerationInvalid},
		{"invalid utf8", func(in *PlatformCompareGenerationInput) { in.Params.Messages[1]["content"] = invalidUTF8 }, ErrPlatformCompareGenerationInvalid},
		{"oversize final message", func(in *PlatformCompareGenerationInput) { in.Params.Messages[1]["content"] = overMessage }, ErrPlatformCompareGenerationInvalid},
		{"unsupported nested value", func(in *PlatformCompareGenerationInput) { in.Params.Messages[0]["nested"] = make(chan int) }, ErrPlatformCompareGenerationInvalid},
		{"nil params max tokens", func(in *PlatformCompareGenerationInput) { in.Params.MaxTokens = nil }, ErrPlatformCompareGenerationInvalid},
		{"zero params max tokens", func(in *PlatformCompareGenerationInput) { zero := 0; in.Params.MaxTokens = &zero }, ErrPlatformCompareGenerationInvalid},
		{"invalid max tokens", func(in *PlatformCompareGenerationInput) {
			in.Params.WhiteLabelBody = []byte(`{"model":"x","messages":[{"role":"user","content":"hello"}],"max_tokens":0}`)
		}, ErrPlatformCompareGenerationInvalid},
		{"unsupported payload field", func(in *PlatformCompareGenerationInput) {
			in.Params.WhiteLabelBody = []byte(`{"model":"x","messages":[{"role":"user","content":"hello"}],"max_tokens":64,"secret_extension":true}`)
		}, ErrPlatformCompareGenerationInvalid},
		{"oversize request", func(in *PlatformCompareGenerationInput) {
			in.Params.WhiteLabelBody = make([]byte, whitelabel.MaxRequestBodyBytes+1)
		}, ErrPlatformCompareGenerationInvalid},
		{"explicit usage disabled", func(in *PlatformCompareGenerationInput) {
			in.Params.WhiteLabelBody = []byte(`{"model":"x","messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream_options":{"include_usage":false}}`)
		}, ErrPlatformCompareGenerationInvalid},
		{"context trim leaves assistant", func(in *PlatformCompareGenerationInput) {
			one := 1
			in.Params.ContextWindow = &one
			in.Params.Messages = []map[string]interface{}{{"role": "user", "content": "x"}, {"role": "user", "content": "y"}, {"role": "assistant", "content": "z"}}
		}, ErrPlatformCompareGenerationInvalid},
		{"context overflow remains safe", func(in *PlatformCompareGenerationInput) {
			in.Params.ContextWindow = &maxContext
			in.Params.Messages[1]["role"] = "assistant"
		}, ErrPlatformCompareGenerationInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
			input := platformCompareTestInput()
			input.Write = func([]byte) error { effects.write++; return nil }
			test.mutate(&input)
			result, err := runner.Run(input)
			if !errors.Is(err, test.want) || result.Started || result.Duplicate != nil {
				t.Fatalf("Run result=%+v error=%v, want=%v", result, err, test.want)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}

func testPlatformCompareEncoderFailures(t *testing.T) {
	for _, encoderFailure := range []string{"delta", "done", "failed"} {
		t.Run(encoderFailure+" encoder failure cancels coordinator", func(t *testing.T) {
			models := []string{"model-a", "model-b"}
			ready, release, abortSibling := make(chan string, 2), make(chan struct{}), make(chan struct{})
			scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{
				"model-a": func(ctx context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
					ready <- "model-a"
					select {
					case <-release:
					case <-ctx.Done():
						return whitelabel.ErrUpstreamUnavailable("cancelled")
					}
					if encoderFailure == "failed" {
						_ = emit(platformCompareDeltaChunk("wrong-model", "bad", true))
						return whitelabel.ErrUpstreamUnavailable("malformed")
					}
					return platformCompareSuccessfulScript("model-a", "answer")(ctx, emit)
				},
				"model-b": func(ctx context.Context, _ func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
					ready <- "model-b"
					select {
					case <-release:
					case <-ctx.Done():
						return whitelabel.ErrUpstreamUnavailable("cancelled")
					}
					select {
					case <-ctx.Done():
						return whitelabel.ErrUpstreamUnavailable("cancelled")
					case <-abortSibling:
						return whitelabel.ErrUpstreamUnavailable("test abort")
					}
				},
			}
			runner, failingStore, upstream, failingWriter := platformCompareStreamFixture(t, models, scripts)
			registry := runner.deps.registry.(*platformCompareTestRegistry)
			snapshot := clonePlatformGeneration(failingStore.claim.Snapshot)
			snapshot.ModelStates["model-b"] = PlatformGenerationModel{State: PlatformGenerationStateCompleted, Seq: 1}
			switch encoderFailure {
			case "delta":
				snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateRunning, Seq: 1}
			case "done":
				snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateCompleted, Seq: 1}
			case "failed":
				snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateFailed, ErrorCode: "gateway_upstream_error"}
			}
			failingStore.settleSnapshot = snapshot
			runner.deps.newEncoder = func(generationID string, orderedModels []string) (platformCompareEncoder, error) {
				inner, err := NewPlatformSSEV2Encoder(generationID, orderedModels)
				if err != nil {
					return nil, err
				}
				return &platformCompareFailingEncoder{inner: inner, failure: encoderFailure}, nil
			}
			cancelCalled := make(chan struct{})
			var cancelCalls atomic.Int32
			runner.deps.newRunnerContext = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(parent)
				return ctx, func() {
					if cancelCalls.Add(1) == 1 {
						close(cancelCalled)
					}
					cancel()
				}
			}
			input := platformCompareTestInput()
			input.Models, input.Write = models, failingWriter.Write
			type runOutcome struct {
				result PlatformCompareGenerationRunResult
				err    error
			}
			done := make(chan runOutcome, 1)
			go func() { result, err := runner.Run(input); done <- runOutcome{result: result, err: err} }()
			<-ready
			<-ready
			upstream.mu.Lock()
			targetBody := upstream.bodies["model-a"]
			upstream.mu.Unlock()
			if targetBody == nil {
				t.Fatal("target response body missing")
			}
			close(release)
			<-targetBody.closed
			if cancelCalls.Load() == 0 {
				close(abortSibling)
			}
			outcome := <-done
			if !outcome.result.Started || !errors.Is(outcome.err, ErrPlatformCompareGenerationUnavailable) || cancelCalls.Load() != 1 {
				t.Fatalf("outcome=%+v cancelCalls=%d", outcome, cancelCalls.Load())
			}
			frames := platformCompareDecodeFrames(t, failingWriter.frames)
			for _, frame := range frames {
				if (encoderFailure == "delta" && frame.event == "delta") || (encoderFailure == "done" && frame.event == "model_done") || (encoderFailure == "failed" && frame.event == "model_error") {
					t.Fatalf("invented %s frame after encoder failure: %+v", encoderFailure, frame)
				}
			}
			if encoderFailure == "delta" && !reflect.DeepEqual(failingStore.records["model-a"], []int64{1}) {
				t.Fatalf("delta mutation=%v", failingStore.records)
			}
			if encoderFailure == "done" && failingStore.done["model-a"] != 1 {
				t.Fatalf("done mutation=%v", failingStore.done)
			}
			if encoderFailure == "failed" && failingStore.failed["model-a"] != "gateway_upstream_error" {
				t.Fatalf("failed mutation=%v", failingStore.failed)
			}
			if failingStore.globalFails != 1 || failingStore.gets != 1 || failingStore.globalFailLease != failingStore.claim.LeaseToken || failingStore.globalFailedSnapshot.ModelStates["model-b"].State != PlatformGenerationStateCompleted || registry.effects.unregister != 1 {
				t.Fatalf("cleanup store=%+v registry=%+v", failingStore, registry.effects)
			}
			targetAfterSettlement := failingStore.globalFailedSnapshot.ModelStates["model-a"]
			if (encoderFailure == "delta" && (targetAfterSettlement.State != PlatformGenerationStateFailed || targetAfterSettlement.Seq != 1)) || (encoderFailure == "done" && targetAfterSettlement.State != PlatformGenerationStateCompleted) || (encoderFailure == "failed" && (targetAfterSettlement.State != PlatformGenerationStateFailed || targetAfterSettlement.ErrorCode != "gateway_upstream_error")) {
				t.Fatalf("target state not preserved after %s encoder failure: %+v", encoderFailure, targetAfterSettlement)
			}
			for model, body := range upstream.bodies {
				if body.closes.Load() != 1 {
					t.Fatalf("model=%s closes=%d", model, body.closes.Load())
				}
			}
		})
	}
}

func TestPlatformCompareGenerationRequiresTwoOrThreeDistinctModels(t *testing.T) {
	tests := []struct {
		name   string
		models []string
		valid  bool
	}{
		{"none", nil, false}, {"one", []string{"a"}, false}, {"two", []string{"b", "a"}, true}, {"three", []string{"c", "a", "b"}, true},
		{"four", []string{"a", "b", "c", "d"}, false}, {"duplicate", []string{"a", "a"}, false}, {"blank", []string{"a", ""}, false},
		{"invalid utf8", []string{"a", string([]byte{0xff})}, false}, {"too long", []string{"a", strings.Repeat("m", platformSSEV2MaxModelIdentifierBytes+1)}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
			input := platformCompareTestInput()
			input.Models = test.models
			run, _, err := runner.prepare(input)
			if test.valid {
				if err != nil || !reflect.DeepEqual(run.models, test.models) || run.encoder == nil {
					t.Fatalf("prepare run=%+v error=%v", run, err)
				}
			} else if !errors.Is(err, ErrPlatformCompareGenerationInvalid) {
				t.Fatalf("prepare error=%v, want invalid", err)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}

func TestPlatformCompareGenerationCopiesRequestBeforeClaim(t *testing.T) {
	runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
	input := platformCompareTestInput()
	run, claimAt, err := runner.prepare(input)
	if err != nil || claimAt != 10_000 || !reflect.DeepEqual(run.models, []string{"model-b", "model-a"}) || run.encoder == nil {
		t.Fatalf("prepare run=%+v claimAt=%d error=%v", run, claimAt, err)
	}
	input.Models[0] = "mutated"
	input.Params.Messages[0]["role"] = "mutated"
	input.Params.Messages[0]["nested"].(map[string]interface{})["items"].([]interface{})[0] = "mutated"
	input.Params.WhiteLabelBody[0] = '['
	*input.Params.MaxTokens = 1
	*input.Params.ContextWindow = 99
	input.User.ID = 999
	preparedBody := string(run.params.WhiteLabelBody)
	if !reflect.DeepEqual(run.models, []string{"model-b", "model-a"}) || run.params.Messages[0]["role"] != "system" || run.params.Messages[0]["nested"].(map[string]interface{})["items"].([]interface{})[0] != "original" || len(preparedBody) == 0 || preparedBody[0] != '{' || strings.Contains(preparedBody, "stream_version") || strings.Contains(preparedBody, "generation_id") || strings.Contains(preparedBody, `"models"`) || *run.params.MaxTokens != 64 || *run.params.ContextWindow != 1 || run.userID != 17 {
		t.Fatalf("prepared run changed after caller mutation: %+v", run)
	}
	for _, model := range []string{"model-b", "model-a"} {
		payload := string(run.payloads[model])
		if !strings.Contains(payload, `"model":"`+model+`"`) || strings.Contains(payload, "stream_version") || strings.Contains(payload, "generation_id") || strings.Contains(payload, `"models"`) {
			t.Fatalf("unsafe payload for %s: %s", model, payload)
		}
	}
	requireNoPlatformComparePostPrepareEffects(t, effects)
}

func TestPlatformCompareGenerationValidatesConversationOwnershipAndQuota(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	t.Run("owned conversation", func(t *testing.T) {
		runner, _ := platformCompareTestRunner(now)
		var gotUserID, gotGUID int64
		runner.deps.newGUID = func() int64 { t.Fatal("new GUID called for existing conversation"); return 0 }
		runner.deps.loadConversation = func(_ context.Context, _ *gorm.DB, userID, guid int64) error {
			gotUserID, gotGUID = userID, guid
			return nil
		}
		input := platformCompareTestInput()
		guid := "8201"
		input.Params.ConversationGUID = &guid
		run, _, err := runner.prepare(input)
		if err != nil || gotUserID != input.User.ID || gotGUID != 8201 || run.existingConversationGUID == nil || *run.existingConversationGUID != 8201 || run.reservedConversationGUID != nil {
			t.Fatalf("run=%+v user=%d guid=%d error=%v", run, gotUserID, gotGUID, err)
		}
	})
	for _, test := range []struct {
		name    string
		loadErr error
		want    error
	}{{"not owned", gorm.ErrRecordNotFound, ErrPlatformCompareGenerationInvalid}, {"database unavailable", errors.New("db down"), ErrPlatformCompareGenerationUnavailable}} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(now)
			runner.deps.loadConversation = func(context.Context, *gorm.DB, int64, int64) error { return test.loadErr }
			input := platformCompareTestInput()
			guid := "8201"
			input.Params.ConversationGUID = &guid
			if _, _, err := runner.prepare(input); !errors.Is(err, test.want) {
				t.Fatalf("prepare error=%v, want=%v", err, test.want)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
	t.Run("malformed conversation guid", func(t *testing.T) {
		runner, effects := platformCompareTestRunner(now)
		input := platformCompareTestInput()
		guid := "not-a-guid"
		input.Params.ConversationGUID = &guid
		if _, _, err := runner.prepare(input); !errors.Is(err, ErrPlatformCompareGenerationInvalid) {
			t.Fatalf("prepare error=%v, want invalid", err)
		}
		requireNoPlatformComparePostPrepareEffects(t, effects)
	})
	for _, mutate := range []func(*models.User){
		func(user *models.User) { user.ID = 0 },
		func(user *models.User) { user.Status = models.UserStatusDisabled },
		func(user *models.User) { user.IsDeleted = 1 },
	} {
		runner, effects := platformCompareTestRunner(now)
		input := platformCompareTestInput()
		mutate(input.User)
		if _, _, err := runner.prepare(input); !errors.Is(err, ErrPlatformCompareGenerationQuota) {
			t.Fatalf("inactive/deleted prepare error=%v, want quota", err)
		}
		requireNoPlatformComparePostPrepareEffects(t, effects)
	}
}

func TestPlatformCompareGenerationQuotaRequiresCapacityForEveryRequestedModel(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	reset := now.UnixMilli()
	for _, test := range []struct {
		name   string
		models []string
		plan   models.PlanType
		limit  int
		used   int
		valid  bool
	}{
		{"two exact capacity", []string{"a", "b"}, models.PlanFree, 3, 1, true}, {"two short", []string{"a", "b"}, models.PlanFree, 3, 2, false},
		{"three exact capacity", []string{"a", "b", "c"}, models.PlanFree, 4, 1, true}, {"three short", []string{"a", "b", "c"}, models.PlanFree, 4, 2, false},
		{"other daily limited plan", []string{"a", "b"}, models.PlanType(99), 2, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(now)
			input := platformCompareTestInput()
			input.Models = test.models
			input.User.PlanType, input.User.DailyCallLimit, input.User.DailyCallsUsed, input.User.DailyCallsResetAt = test.plan, test.limit, test.used, &reset
			_, _, err := runner.prepare(input)
			if test.valid && err != nil {
				t.Fatalf("prepare error=%v", err)
			}
			if !test.valid && !errors.Is(err, ErrPlatformCompareGenerationQuota) {
				t.Fatalf("prepare error=%v, want quota", err)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}

func TestPlatformCompareGenerationQuotaAppliesDailyReset(t *testing.T) {
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		reset *int64
		valid bool
	}{
		{"nil reset", nil, true},
		{"one millisecond before day", func() *int64 { value := now.Add(-time.Millisecond).UnixMilli(); return &value }(), true},
		{"day boundary", func() *int64 { value := now.UnixMilli(); return &value }(), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(now)
			input := platformCompareTestInput()
			input.User.DailyCallLimit, input.User.DailyCallsUsed, input.User.DailyCallsResetAt = 2, 2, test.reset
			_, _, err := runner.prepare(input)
			if test.valid && err != nil {
				t.Fatalf("prepare error=%v", err)
			}
			if !test.valid && !errors.Is(err, ErrPlatformCompareGenerationQuota) {
				t.Fatalf("prepare error=%v, want quota", err)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}

func TestPlatformCompareGenerationProfessionalAndEnterpriseQuotaRemainUnlimited(t *testing.T) {
	for _, plan := range []models.PlanType{models.PlanProfessional, models.PlanEnterprise} {
		t.Run(plan.String(), func(t *testing.T) {
			runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
			input := platformCompareTestInput()
			input.Models = []string{"a", "b", "c"}
			input.User.PlanType, input.User.DailyCallLimit, input.User.DailyCallsUsed = plan, -1, -1
			if _, _, err := runner.prepare(input); err != nil {
				t.Fatalf("unlimited plan prepare error=%v", err)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}

func platformCompareTestClaim(nowMillis int64, orderedModels []string) PlatformGenerationClaimResult {
	token := platformSingleTestLeaseToken()
	states := make(map[string]PlatformGenerationModel, len(orderedModels))
	for _, model := range orderedModels {
		states[model] = PlatformGenerationModel{State: PlatformGenerationStateRunning}
	}
	return PlatformGenerationClaimResult{LeaseToken: token, Snapshot: PlatformGenerationSnapshot{
		GenerationID: platformCompareTestGenerationID, Mode: PlatformGenerationModeCompare,
		Models: append([]string(nil), orderedModels...), State: PlatformGenerationStateRunning, ModelStates: states,
		CreatedAtMillis: nowMillis, UpdatedAtMillis: nowMillis, LeaseOwnerSHA256: platformGenerationLeaseDigest(token),
		LeaseUntilMillis: nowMillis + platformGenerationLeaseDuration.Milliseconds(),
	}}
}

func TestPlatformCompareGenerationClaimsOrderedModelsOnce(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	runner, effects := platformCompareTestRunner(now)
	store := runner.deps.store.(*platformCompareTestStore)
	registry := runner.deps.registry.(*platformCompareTestRegistry)
	store.claim = platformCompareTestClaim(now.UnixMilli(), []string{"model-b", "model-a"})
	store.getSnapshot = clonePlatformGeneration(store.claim.Snapshot)
	var frame []byte
	input := platformCompareTestInput()
	input.Write = func(value []byte) error {
		effects.write++
		frame = append([]byte(nil), value...)
		return errors.New("client left after meta")
	}
	registry.onBegin = func() {
		input.Models[0] = "mutated-model"
		input.Params.Messages[0]["nested"].(map[string]interface{})["items"].([]interface{})[0] = "mutated"
		input.Params.Messages[1]["content"] = ""
		input.Params.WhiteLabelBody[0] = '['
		*input.Params.MaxTokens = -1
		input.User.ID = 999
	}

	result, err := runner.Run(input)
	if !errors.Is(err, ErrPlatformCompareGenerationUnavailable) || !result.Started || result.Duplicate != nil {
		t.Fatalf("Run result=%+v error=%v", result, err)
	}
	if effects.claim != 1 || effects.admission != 1 || effects.admissionRelease != 1 || effects.guid != 1 || effects.register != 1 || effects.write != 1 {
		t.Fatalf("entry effects=%+v", effects)
	}
	if store.claimInput.UserID != 17 || store.claimInput.GenerationID != platformCompareTestGenerationID || store.claimInput.Mode != PlatformGenerationModeCompare || !reflect.DeepEqual(store.claimInput.Models, []string{"model-b", "model-a"}) || store.claimInput.NowMillis != now.UnixMilli() {
		t.Fatalf("claim input=%+v", store.claimInput)
	}
	if registry.registeredUID != 17 || registry.registeredID != platformCompareTestGenerationID {
		t.Fatalf("registration registry=%+v", registry)
	}
	expectedEncoder, encoderErr := NewPlatformSSEV2Encoder(platformCompareTestGenerationID, []string{"model-b", "model-a"})
	if encoderErr != nil || string(frame) != string(expectedEncoder.Meta(strconv.FormatInt(8101, 10))) {
		t.Fatalf("meta=%q encoder error=%v", frame, encoderErr)
	}
	if effects.upstream != 2 || effects.modelFailed != 2 || effects.persist != 0 || effects.receipt != 0 {
		t.Fatalf("unexpected post-entry work: %+v", effects)
	}

	if effects.unregister != 1 || effects.runnerCancel != 1 || effects.get != 1 || effects.fail != 1 || registry.unregisterUID != 17 || registry.unregisterID != platformCompareTestGenerationID || registry.unregisterToken != registry.token {
		t.Fatalf("cleanup effects=%+v store=%+v", effects, store)
	}
}

func TestPlatformCompareGenerationDuplicateHasNoPostClaimSideEffects(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	for _, test := range []struct {
		name      string
		mutate    func(*PlatformGenerationSnapshot)
		wantError bool
	}{
		{name: "valid duplicate"},
		{name: "reordered identity rejected", mutate: func(snapshot *PlatformGenerationSnapshot) {
			snapshot.Models[0], snapshot.Models[1] = snapshot.Models[1], snapshot.Models[0]
		}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(now)
			store := runner.deps.store.(*platformCompareTestStore)
			claim := platformCompareTestClaim(now.UnixMilli(), []string{"model-b", "model-a"})
			claim.Duplicate, claim.LeaseToken = true, ""
			if test.mutate != nil {
				test.mutate(&claim.Snapshot)
			}
			store.claim, store.claimErr = claim, ErrPlatformGenerationConflict
			result, err := runner.Run(platformCompareTestInput())
			if test.wantError {
				if !errors.Is(err, ErrPlatformCompareGenerationUnavailable) || result.Started || result.Duplicate != nil {
					t.Fatalf("malformed duplicate result=%+v error=%v", result, err)
				}
			} else {
				if err != nil || result.Started || result.Duplicate == nil || !reflect.DeepEqual(*result.Duplicate, claim.Snapshot) {
					t.Fatalf("duplicate result=%+v error=%v", result, err)
				}
				claim.Snapshot.Models[0] = "mutated"
				claim.Snapshot.ModelStates["model-b"] = PlatformGenerationModel{State: PlatformGenerationStateFailed, ErrorCode: "internal_error"}
				if result.Duplicate.Models[0] != "model-b" || result.Duplicate.ModelStates["model-b"].State != PlatformGenerationStateRunning {
					t.Fatalf("duplicate projection aliases store snapshot: %+v", result.Duplicate)
				}
			}
			if effects.claim != 1 || effects.admission != 1 || effects.admissionRelease != 1 || effects.register != 0 || effects.unregister != 0 || effects.write != 0 || effects.upstream != 0 || effects.persist != 0 || effects.receipt != 0 || effects.get != 0 || effects.fail != 0 || effects.runnerCancel != 0 {
				t.Fatalf("duplicate side effects=%+v", effects)
			}
		})
	}
}

func TestPlatformCompareGenerationAdmissionCancellationStopsBeforeClaim(t *testing.T) {
	for _, test := range []struct {
		name          string
		cancelWhen    string
		wantAdmission int
	}{
		{name: "request already cancelled", cancelWhen: "before", wantAdmission: 0},
		{name: "request cancelled after admission", cancelWhen: "during", wantAdmission: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
			registry := runner.deps.registry.(*platformCompareTestRegistry)
			input := platformCompareTestInput()
			ctx, cancel := context.WithCancel(input.Context)
			input.Context = ctx
			if test.cancelWhen == "before" {
				cancel()
			} else {
				registry.onBegin = cancel
			}
			result, err := runner.Run(input)
			if !errors.Is(err, ErrPlatformCompareGenerationUnavailable) || result.Started || result.Duplicate != nil {
				t.Fatalf("Run result=%+v error=%v", result, err)
			}
			if effects.admission != test.wantAdmission || effects.admissionRelease != test.wantAdmission || effects.claim != 0 || effects.register != 0 || effects.write != 0 {
				t.Fatalf("cancellation effects=%+v", effects)
			}
		})
	}
	t.Run("invalid input does not acquire admission", func(t *testing.T) {
		runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
		input := platformCompareTestInput()
		input.Models = []string{"model-a", "model-a"}
		result, err := runner.Run(input)
		if !errors.Is(err, ErrPlatformCompareGenerationInvalid) || result.Started || result.Duplicate != nil {
			t.Fatalf("Run result=%+v error=%v", result, err)
		}
		if effects.admission != 0 || effects.admissionRelease != 0 || effects.claim != 0 {
			t.Fatalf("invalid input effects=%+v", effects)
		}
	})
}

func TestPlatformCompareGenerationRegistrationFailureSettlesOwnedRunningState(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	for _, test := range []struct {
		name     string
		mutate   func(*PlatformGenerationSnapshot)
		wantFail int
	}{
		{name: "owned running", wantFail: 1},
		{name: "wrong owner", mutate: func(snapshot *PlatformGenerationSnapshot) { snapshot.LeaseOwnerSHA256 = strings.Repeat("a", 64) }},
		{name: "expired lease", mutate: func(snapshot *PlatformGenerationSnapshot) { snapshot.LeaseUntilMillis = now.UnixMilli() }},
		{name: "cancelling", mutate: func(snapshot *PlatformGenerationSnapshot) {
			snapshot.State, snapshot.LeaseUntilMillis = PlatformGenerationStateCancelling, 0
		}},
		{name: "committing", mutate: func(snapshot *PlatformGenerationSnapshot) {
			snapshot.State, snapshot.LeaseOwnerSHA256, snapshot.LeaseUntilMillis = PlatformGenerationStateCommitting, "", 0
			for model := range snapshot.ModelStates {
				snapshot.ModelStates[model] = PlatformGenerationModel{State: PlatformGenerationStateCompleted}
			}
		}},
		{name: "terminal", mutate: func(snapshot *PlatformGenerationSnapshot) {
			snapshot.State, snapshot.LeaseOwnerSHA256, snapshot.LeaseUntilMillis = PlatformGenerationStateCancelled, "", 0
			for model := range snapshot.ModelStates {
				snapshot.ModelStates[model] = PlatformGenerationModel{State: PlatformGenerationStateCancelled}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(now)
			store := runner.deps.store.(*platformCompareTestStore)
			registry := runner.deps.registry.(*platformCompareTestRegistry)
			store.claim = platformCompareTestClaim(now.UnixMilli(), []string{"model-b", "model-a"})
			store.getSnapshot = clonePlatformGeneration(store.claim.Snapshot)
			if test.mutate != nil {
				test.mutate(&store.getSnapshot)
			}
			registry.registerErr = errors.New("registry draining")
			result, err := runner.Run(platformCompareTestInput())
			if !errors.Is(err, ErrPlatformCompareGenerationUnavailable) || result.Started || result.Duplicate != nil {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if effects.claim != 1 || effects.register != 1 || effects.get != 1 || effects.fail != test.wantFail || effects.unregister != 0 || effects.write != 0 || effects.runnerCancel != 1 || effects.admissionRelease != 1 {
				t.Fatalf("registration failure effects=%+v wantFail=%d", effects, test.wantFail)
			}
		})
	}
}

func TestPlatformCompareGenerationRegistrationRejectsInvalidRunnerContext(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	for _, test := range []struct {
		name       string
		newContext func(*platformCompareTestEffects) (context.Context, context.CancelFunc)
		wantCancel int
	}{
		{
			name: "nil context",
			newContext: func(effects *platformCompareTestEffects) (context.Context, context.CancelFunc) {
				return nil, func() { effects.runnerCancel++ }
			},
			wantCancel: 1,
		},
		{
			name: "nil cancel",
			newContext: func(*platformCompareTestEffects) (context.Context, context.CancelFunc) {
				return context.Background(), nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(now)
			store := runner.deps.store.(*platformCompareTestStore)
			store.claim = platformCompareTestClaim(now.UnixMilli(), []string{"model-b", "model-a"})
			store.getSnapshot = clonePlatformGeneration(store.claim.Snapshot)
			runner.deps.newRunnerContext = func(context.Context, time.Duration) (context.Context, context.CancelFunc) {
				return test.newContext(effects)
			}

			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("Run panicked for invalid runner context: %v", recovered)
				}
			}()
			result, err := runner.Run(platformCompareTestInput())
			if !errors.Is(err, ErrPlatformCompareGenerationUnavailable) || result.Started || result.Duplicate != nil {
				t.Fatalf("Run result=%+v error=%v", result, err)
			}
			if effects.claim != 1 || effects.get != 1 || effects.fail != 1 || effects.runnerCancel != test.wantCancel || effects.admission != 1 || effects.admissionRelease != 1 || effects.register != 0 || effects.unregister != 0 || effects.write != 0 || effects.upstream != 0 || effects.persist != 0 || effects.receipt != 0 {
				t.Fatalf("invalid runner context effects=%+v wantCancel=%d", effects, test.wantCancel)
			}
		})
	}
}

func TestPlatformCompareGenerationRequestCancellationAfterClaimDoesNotStopRunner(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	runner, effects := platformCompareTestRunner(now)
	store := runner.deps.store.(*platformCompareTestStore)
	registry := runner.deps.registry.(*platformCompareTestRegistry)
	input := platformCompareTestInput()
	requestCtx, cancelRequest := context.WithCancel(input.Context)
	input.Context = requestCtx
	store.claim = platformCompareTestClaim(now.UnixMilli(), []string{"model-b", "model-a"})
	store.getSnapshot = clonePlatformGeneration(store.claim.Snapshot)
	store.onClaim = func(ctx context.Context, _ PlatformGenerationClaimInput) {
		cancelRequest()
		if ctx.Err() == nil {
			t.Fatal("claim context did not observe request cancellation")
		}
	}
	root := context.WithValue(context.Background(), struct{ name string }{"root"}, "application")
	runner.deps.rootContext = root
	var runnerParent context.Context
	var runnerContext context.Context
	runner.deps.newRunnerContext = func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
		runnerParent = parent
		if timeout != time.Minute {
			t.Fatalf("runner timeout=%v", timeout)
		}
		var cancel context.CancelFunc
		runnerContext, cancel = context.WithCancel(parent)
		return runnerContext, func() { effects.runnerCancel++; cancel() }
	}
	registry.onRegister = func() {
		if runnerContext == nil || runnerContext.Err() != nil {
			t.Fatalf("application-rooted runner stopped before registration: %v", runnerContext)
		}
	}

	result, err := runner.Run(input)
	var runnerErr error
	if runnerContext != nil {
		runnerErr = runnerContext.Err()
	}
	if !errors.Is(err, ErrPlatformCompareGenerationUnavailable) || !result.Started || runnerParent != root || runnerContext == nil || runnerErr == nil || requestCtx.Err() == nil {
		t.Fatalf("result=%+v error=%v parentRoot=%v runnerErr=%v requestErr=%v", result, err, runnerParent == root, runnerErr, requestCtx.Err())
	}
	if effects.unregister != 1 || effects.runnerCancel != 1 || effects.get != 1 || effects.fail != 1 || effects.upstream != 2 || effects.modelFailed != 2 {
		t.Fatalf("cleanup effects=%+v", effects)
	}
}

type platformCompareStreamStore struct {
	mu                   sync.Mutex
	claim                PlatformGenerationClaimResult
	settleSnapshot       PlatformGenerationSnapshot
	records              map[string][]int64
	done                 map[string]int64
	doneObserved         chan struct{}
	doneObservedOnce     sync.Once
	failed               map[string]string
	recordErrModel       string
	doneErrModel         string
	failedErrModel       string
	gets                 int
	globalFails          int
	globalFailUserID     int64
	globalFailGeneration string
	globalFailLease      string
	globalFailCode       string
	globalFailedSnapshot PlatformGenerationSnapshot
	renewCalls           int
	renewUserID          int64
	renewGeneration      string
	renewLease           string
	renewNowMillis       int64
	renewErr             error
	renewed              chan struct{}
	ackCalls             int
	acknowledgedSnapshot PlatformGenerationSnapshot
	mutationActive       atomic.Int32
	mutationMax          atomic.Int32
}

func (s *platformCompareStreamStore) Claim(context.Context, PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error) {
	return s.claim, nil
}
func (s *platformCompareStreamStore) Get(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
	s.mu.Lock()
	s.gets++
	s.mu.Unlock()
	if s.settleSnapshot.GenerationID != "" {
		return clonePlatformGeneration(s.settleSnapshot), nil
	}
	return clonePlatformGeneration(s.claim.Snapshot), nil
}
func (s *platformCompareStreamStore) FailRunningOwned(_ context.Context, userID int64, generationID, lease, code string, _ int64) (PlatformGenerationSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.globalFails++
	s.globalFailUserID, s.globalFailGeneration, s.globalFailLease, s.globalFailCode = userID, generationID, lease, code
	snapshot := clonePlatformGeneration(s.settleSnapshot)
	if snapshot.GenerationID == "" {
		snapshot = clonePlatformGeneration(s.claim.Snapshot)
	}
	for model, state := range snapshot.ModelStates {
		if state.State == PlatformGenerationStateRunning {
			state.State, state.ErrorCode = PlatformGenerationStateFailed, code
			snapshot.ModelStates[model] = state
		}
	}
	snapshot.State, snapshot.ErrorCode = PlatformGenerationStateFailed, code
	snapshot.LeaseOwnerSHA256, snapshot.LeaseUntilMillis = "", 0
	s.globalFailedSnapshot = snapshot
	return snapshot, nil
}
func (s *platformCompareStreamStore) mutate(fn func()) {
	active := s.mutationActive.Add(1)
	for {
		maximum := s.mutationMax.Load()
		if active <= maximum || s.mutationMax.CompareAndSwap(maximum, active) {
			break
		}
	}
	defer s.mutationActive.Add(-1)
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}
func (s *platformCompareStreamStore) RecordDeltaOwned(_ context.Context, _ int64, _, _, model string, seq, _ int64) (PlatformGenerationSnapshot, error) {
	if model == s.recordErrModel {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	s.mutate(func() { s.records[model] = append(s.records[model], seq) })
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformCompareStreamStore) MarkModelDoneOwned(_ context.Context, _ int64, _, _, model string, lastSeq, _ int64) (PlatformGenerationSnapshot, error) {
	if model == s.doneErrModel {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	s.mutate(func() { s.done[model] = lastSeq })
	if s.doneObserved != nil {
		s.doneObservedOnce.Do(func() { close(s.doneObserved) })
	}
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformCompareStreamStore) MarkModelFailedOwned(_ context.Context, _ int64, _, _, model, code string, _ int64) (PlatformGenerationSnapshot, error) {
	if model == s.failedErrModel {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	s.mutate(func() { s.failed[model] = code })
	return PlatformGenerationSnapshot{}, nil
}
func (s *platformCompareStreamStore) RenewLease(_ context.Context, userID int64, generationID, lease string, now int64) (PlatformGenerationSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewCalls++
	s.renewUserID, s.renewGeneration, s.renewLease, s.renewNowMillis = userID, generationID, lease, now
	if s.renewed != nil && s.renewCalls == 1 {
		close(s.renewed)
	}
	if s.renewErr != nil {
		return PlatformGenerationSnapshot{}, s.renewErr
	}
	snapshot := clonePlatformGeneration(s.claim.Snapshot)
	snapshot.UpdatedAtMillis = now
	snapshot.LeaseUntilMillis = now + platformGenerationLeaseDuration.Milliseconds()
	return snapshot, nil
}
func (s *platformCompareStreamStore) AcknowledgeCancelledOwned(_ context.Context, _ int64, _ string, _ string, now int64) (PlatformGenerationSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ackCalls++
	snapshot := clonePlatformGeneration(s.settleSnapshot)
	snapshot.State, snapshot.UpdatedAtMillis, snapshot.LeaseOwnerSHA256, snapshot.LeaseUntilMillis = PlatformGenerationStateCancelled, now, "", 0
	for model, state := range snapshot.ModelStates {
		if state.State == PlatformGenerationStateRunning {
			state.State = PlatformGenerationStateCancelled
			snapshot.ModelStates[model] = state
		}
	}
	s.acknowledgedSnapshot = clonePlatformGeneration(snapshot)
	return snapshot, nil
}
func (*platformCompareStreamStore) ReconcileComplete(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error) {
	return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
}

type platformCompareStreamBody struct {
	closes atomic.Int32
	closed chan struct{}
}

func (*platformCompareStreamBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *platformCompareStreamBody) Close() error {
	if b.closes.Add(1) == 1 && b.closed != nil {
		close(b.closed)
	}
	return nil
}

type platformCompareStreamUpstream struct {
	mu      sync.Mutex
	scripts map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error
	bodies  map[string]*platformCompareStreamBody
	calls   []string
}

func (u *platformCompareStreamUpstream) Chat(_ context.Context, payload []byte) (*http.Response, *whitelabel.Error) {
	var request struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(payload, &request) != nil || request.Model == "" {
		return nil, whitelabel.ErrUpstreamUnavailable("invalid test payload")
	}
	u.mu.Lock()
	u.calls = append(u.calls, request.Model)
	body := &platformCompareStreamBody{closed: make(chan struct{})}
	u.bodies[request.Model] = body
	u.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
}
func (u *platformCompareStreamUpstream) ConsumeChatCompletionSSEContext(ctx context.Context, _ io.Reader, model string, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
	return u.scripts[model](ctx, emit)
}

type platformCompareFrameWriter struct {
	mu             sync.Mutex
	frames         [][]byte
	active         atomic.Int32
	maximum        atomic.Int32
	failFirst      bool
	onModelError   chan struct{}
	modelErrorOnce sync.Once
}

func (w *platformCompareFrameWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.frames)
}

func (w *platformCompareFrameWriter) Write(frame []byte) error {
	active := w.active.Add(1)
	for {
		maximum := w.maximum.Load()
		if active <= maximum || w.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	defer w.active.Add(-1)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames = append(w.frames, append([]byte(nil), frame...))
	if w.onModelError != nil && strings.Contains(string(frame), "event: model_error") {
		w.modelErrorOnce.Do(func() { close(w.onModelError) })
	}
	if w.failFirst && len(w.frames) == 1 {
		return errors.New("detached")
	}
	return nil
}

type platformCompareDecodedFrame struct {
	event string
	data  map[string]interface{}
}

type platformCompareFailingEncoder struct {
	inner   *PlatformSSEV2Encoder
	failure string
	err     error
}

func (e *platformCompareFailingEncoder) Meta(conversationGUID string) []byte {
	return e.inner.Meta(conversationGUID)
}
func (e *platformCompareFailingEncoder) Delta(model string, seq int64, delta string) []byte {
	if e.failure == "delta" {
		e.err = errors.New("injected delta encoder failure")
		return nil
	}
	return e.inner.Delta(model, seq, delta)
}
func (e *platformCompareFailingEncoder) ModelDone(model string, seq int64) []byte {
	if e.failure == "done" {
		e.err = errors.New("injected done encoder failure")
		return nil
	}
	return e.inner.ModelDone(model, seq)
}
func (e *platformCompareFailingEncoder) ModelError(model, code, requestID string) []byte {
	if e.failure == "failed" {
		e.err = errors.New("injected failed encoder failure")
		return nil
	}
	return e.inner.ModelError(model, code, requestID)
}
func (e *platformCompareFailingEncoder) Error(code, requestID string) []byte {
	return e.inner.Error(code, requestID)
}
func (e *platformCompareFailingEncoder) Err() error {
	if e.err != nil {
		return e.err
	}
	return e.inner.Err()
}

func platformCompareDecodeFrames(t *testing.T, frames [][]byte) []platformCompareDecodedFrame {
	t.Helper()
	decoded := make([]platformCompareDecodedFrame, 0, len(frames))
	for _, frame := range frames {
		text := string(frame)
		if !strings.HasSuffix(text, "\n\n") || strings.Count(text, "event: ") != 1 || strings.Count(text, "data: ") != 1 {
			t.Fatalf("torn SSE frame: %q", text)
		}
		lines := strings.Split(strings.TrimSuffix(text, "\n\n"), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event: ") || !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("invalid SSE frame: %q", text)
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &data); err != nil {
			t.Fatalf("invalid SSE data: %v", err)
		}
		decoded = append(decoded, platformCompareDecodedFrame{event: strings.TrimPrefix(lines[0], "event: "), data: data})
	}
	return decoded
}

func platformCompareDeltaChunk(model, content string, terminal bool) whitelabel.ChatCompletionChunk {
	chunk := whitelabel.ChatCompletionChunk{Model: model, Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{Content: &content}}}}
	if terminal {
		finish := "stop"
		chunk.Choices[0].FinishReason = &finish
	}
	return chunk
}
func platformCompareUsageChunk(model string, tokens int) whitelabel.ChatCompletionChunk {
	return whitelabel.ChatCompletionChunk{Model: model, Usage: &whitelabel.ChatCompletionUsage{TotalTokens: tokens}}
}

func platformCompareStreamFixture(t *testing.T, models []string, scripts map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error) (*PlatformCompareGenerationRunner, *platformCompareStreamStore, *platformCompareStreamUpstream, *platformCompareFrameWriter) {
	t.Helper()
	now := time.UnixMilli(10_000).UTC()
	runner, effects := platformCompareTestRunner(now)
	store := &platformCompareStreamStore{claim: platformCompareTestClaim(now.UnixMilli(), models), records: map[string][]int64{}, done: map[string]int64{}, failed: map[string]string{}}
	upstream := &platformCompareStreamUpstream{scripts: scripts, bodies: map[string]*platformCompareStreamBody{}}
	writer := &platformCompareFrameWriter{}
	runner.deps.store, runner.deps.upstream = store, upstream
	input := platformCompareTestInput()
	input.Models, input.Write = append([]string(nil), models...), writer.Write
	runner.deps.registry = &platformCompareTestRegistry{effects: effects}
	return runner, store, upstream, writer
}

func platformCompareSuccessfulScript(model string, parts ...string) func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
	return func(_ context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
		for index, part := range parts {
			if err := emit(platformCompareDeltaChunk(model, part, index == len(parts)-1)); err != nil {
				return whitelabel.ErrUpstreamUnavailable("callback")
			}
		}
		if err := emit(platformCompareUsageChunk(model, len(parts))); err != nil {
			return whitelabel.ErrUpstreamUnavailable("callback")
		}
		return nil
	}
}

func TestPlatformCompareGenerationFansOutTwoAndThreeModels(t *testing.T) {
	for _, models := range [][]string{{"model-a", "model-b"}, {"model-a", "model-b", "model-c"}} {
		scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{}
		started, release := make(chan string, len(models)), make(chan struct{})
		for _, model := range models {
			model := model
			scripts[model] = func(ctx context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
				started <- model
				select {
				case <-release:
				case <-ctx.Done():
					return whitelabel.ErrUpstreamUnavailable("cancelled")
				}
				return platformCompareSuccessfulScript(model, model)(ctx, emit)
			}
		}
		runner, store, upstream, writer := platformCompareStreamFixture(t, models, scripts)
		input := platformCompareTestInput()
		input.Models, input.Write = models, writer.Write
		done := make(chan PlatformCompareGenerationRunResult, 1)
		go func() { result, _ := runner.Run(input); done <- result }()
		seen := map[string]bool{}
		for range models {
			select {
			case model := <-started:
				seen[model] = true
			case result := <-done:
				t.Fatalf("Run returned before fan-out barrier: %+v", result)
			}
		}
		close(release)
		result := <-done
		if !result.Started || len(seen) != len(models) || len(store.done) != len(models) || len(store.failed) != 0 || len(upstream.calls) != len(models) {
			t.Fatalf("models=%v result=%+v seen=%v done=%v failed=%v calls=%v", models, result, seen, store.done, store.failed, upstream.calls)
		}
		frames := platformCompareDecodeFrames(t, writer.frames)
		if len(frames) != 1+2*len(models) {
			t.Fatalf("models=%v frames=%+v", models, frames)
		}
	}
}

func TestPlatformCompareGenerationAllowsCrossModelInterleavingWithStrictPerModelSequence(t *testing.T) {
	aFirst, bFinished := make(chan struct{}), make(chan struct{})
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{
		"model-a": func(ctx context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			if emit(platformCompareDeltaChunk("model-a", "a1", false)) != nil {
				return whitelabel.ErrUpstreamUnavailable("callback")
			}
			close(aFirst)
			select {
			case <-bFinished:
			case <-ctx.Done():
				return whitelabel.ErrUpstreamUnavailable("cancelled")
			}
			return platformCompareSuccessfulScript("model-a", "a2")(ctx, emit)
		},
		"model-b": func(ctx context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			select {
			case <-aFirst:
			case <-ctx.Done():
				return whitelabel.ErrUpstreamUnavailable("cancelled")
			}
			result := platformCompareSuccessfulScript("model-b", "b1")(ctx, emit)
			close(bFinished)
			return result
		},
	}
	runner, store, _, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b"}, scripts)
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b"}, writer.Write
	result, _ := runner.Run(input)
	if !result.Started || !reflect.DeepEqual(store.records["model-a"], []int64{1, 2}) || !reflect.DeepEqual(store.records["model-b"], []int64{1}) {
		t.Fatalf("result=%+v records=%v", result, store.records)
	}
	frames := platformCompareDecodeFrames(t, writer.frames)
	var order []string
	for _, frame := range frames {
		if frame.event == "delta" {
			order = append(order, frame.data["model"].(string)+":"+strconv.Itoa(int(frame.data["seq"].(float64))))
		}
	}
	if !reflect.DeepEqual(order, []string{"model-a:1", "model-b:1", "model-a:2"}) {
		t.Fatalf("delta order=%v", order)
	}
}

func TestPlatformCompareGenerationModelFailureDoesNotCancelSiblings(t *testing.T) {
	failureObserved := make(chan struct{})
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{
		"model-a": func(_ context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			_ = emit(platformCompareDeltaChunk("wrong-model", "bad", true))
			close(failureObserved)
			return whitelabel.ErrUpstreamUnavailable("provider secret")
		},
		"model-b": func(ctx context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			select {
			case <-failureObserved:
			case <-ctx.Done():
				return whitelabel.ErrUpstreamUnavailable("cancelled")
			}
			return platformCompareSuccessfulScript("model-b", "sibling-ok")(ctx, emit)
		},
	}
	runner, store, _, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b"}, scripts)
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b"}, writer.Write
	result, _ := runner.Run(input)
	frames := platformCompareDecodeFrames(t, writer.frames)
	terminals := map[string]string{}
	terminalCount := map[string]int{}
	for _, frame := range frames {
		if frame.event == "model_error" || frame.event == "model_done" {
			model := frame.data["model"].(string)
			terminals[model] = frame.event
			terminalCount[model]++
		}
	}
	allFrames := string(func() []byte {
		var joined []byte
		for _, frame := range writer.frames {
			joined = append(joined, frame...)
		}
		return joined
	}())
	if !result.Started || store.failed["model-a"] != "gateway_upstream_error" || store.done["model-b"] != 1 || !reflect.DeepEqual(terminals, map[string]string{"model-a": "model_error", "model-b": "model_done"}) || terminalCount["model-a"] != 1 || terminalCount["model-b"] != 1 || strings.Contains(allFrames, "provider secret") {
		t.Fatalf("result=%+v failed=%v done=%v terminals=%v counts=%v frames=%q", result, store.failed, store.done, terminals, terminalCount, allFrames)
	}
}

func platformCompareConcurrentFixture(t *testing.T, detached bool) (*platformCompareStreamStore, *platformCompareFrameWriter, map[string]*platformCompareStreamBody, PlatformCompareGenerationRunResult) {
	t.Helper()
	ready, release := make(chan string, 2), make(chan struct{})
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{}
	for _, model := range []string{"model-a", "model-b"} {
		model := model
		scripts[model] = func(ctx context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			ready <- model
			select {
			case <-release:
			case <-ctx.Done():
				return whitelabel.ErrUpstreamUnavailable("cancelled")
			}
			return platformCompareSuccessfulScript(model, "one", "two")(ctx, emit)
		}
	}
	runner, store, upstream, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b"}, scripts)
	writer.failFirst = detached
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b"}, writer.Write
	done := make(chan PlatformCompareGenerationRunResult, 1)
	go func() { result, _ := runner.Run(input); done <- result }()
	for index := 0; index < 2; index++ {
		select {
		case <-ready:
		case result := <-done:
			t.Fatalf("Run returned before concurrent workers started: %+v", result)
		}
	}
	close(release)
	return store, writer, upstream.bodies, <-done
}

func TestPlatformCompareGenerationSerializesMutationStateEncoderAndFrames(t *testing.T) {
	store, writer, _, result := platformCompareConcurrentFixture(t, false)
	if !result.Started || store.mutationMax.Load() != 1 || len(platformCompareDecodeFrames(t, writer.frames)) != 7 {
		t.Fatalf("result=%+v mutationMax=%d frames=%d", result, store.mutationMax.Load(), len(writer.frames))
	}
	for _, mutation := range []string{"delta", "done", "failed"} {
		t.Run(mutation+" mutation failure cancels coordinator", func(t *testing.T) {
			models := []string{"model-a", "model-b"}
			ready, release, abortSibling := make(chan string, 2), make(chan struct{}), make(chan struct{})
			scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{
				"model-a": func(ctx context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
					ready <- "model-a"
					select {
					case <-release:
					case <-ctx.Done():
						return whitelabel.ErrUpstreamUnavailable("cancelled")
					}
					if mutation == "failed" {
						_ = emit(platformCompareDeltaChunk("wrong-model", "bad", true))
						return whitelabel.ErrUpstreamUnavailable("malformed")
					}
					return platformCompareSuccessfulScript("model-a", "answer")(ctx, emit)
				},
				"model-b": func(ctx context.Context, _ func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
					ready <- "model-b"
					select {
					case <-release:
					case <-ctx.Done():
						return whitelabel.ErrUpstreamUnavailable("cancelled")
					}
					select {
					case <-ctx.Done():
						return whitelabel.ErrUpstreamUnavailable("cancelled")
					case <-abortSibling:
						return whitelabel.ErrUpstreamUnavailable("test abort")
					}
				},
			}
			runner, failingStore, upstream, failingWriter := platformCompareStreamFixture(t, models, scripts)
			registry := runner.deps.registry.(*platformCompareTestRegistry)
			snapshot := clonePlatformGeneration(failingStore.claim.Snapshot)
			snapshot.ModelStates["model-b"] = PlatformGenerationModel{State: PlatformGenerationStateCompleted, Seq: 1}
			failingStore.settleSnapshot = snapshot
			switch mutation {
			case "delta":
				failingStore.recordErrModel = "model-a"
			case "done":
				failingStore.doneErrModel = "model-a"
			case "failed":
				failingStore.failedErrModel = "model-a"
			}
			modelErrorWritten := make(chan struct{})
			failingWriter.onModelError = modelErrorWritten
			cancelCalled := make(chan struct{})
			var cancelCalls atomic.Int32
			runner.deps.newRunnerContext = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(parent)
				return ctx, func() {
					if cancelCalls.Add(1) == 1 {
						close(cancelCalled)
					}
					cancel()
				}
			}
			input := platformCompareTestInput()
			input.Models, input.Write = models, failingWriter.Write
			type runOutcome struct {
				result PlatformCompareGenerationRunResult
				err    error
			}
			done := make(chan runOutcome, 1)
			go func() { result, err := runner.Run(input); done <- runOutcome{result: result, err: err} }()
			<-ready
			<-ready
			close(release)
			select {
			case <-cancelCalled:
			case <-modelErrorWritten:
				close(abortSibling)
			}
			outcome := <-done
			if !outcome.result.Started || !errors.Is(outcome.err, ErrPlatformCompareGenerationUnavailable) {
				t.Fatalf("outcome=%+v", outcome)
			}
			frames := platformCompareDecodeFrames(t, failingWriter.frames)
			globalErrors := 0
			for _, frame := range frames {
				if frame.event == "model_error" || frame.event == "model_done" {
					t.Fatalf("invented terminal frame after %s mutation failure: %+v", mutation, frame)
				}
				if frame.event == "error" {
					globalErrors++
					if frame.data["code"] != "internal_error" {
						t.Fatalf("fatal error frame=%+v", frame)
					}
				}
			}
			if mutation == "done" {
				if len(frames) != 3 || frames[1].event != "delta" || frames[2].event != "error" {
					t.Fatalf("done failure frames=%+v", frames)
				}
			} else if len(frames) != 2 || frames[1].event != "error" {
				t.Fatalf("%s failure frames=%+v", mutation, frames)
			}
			if globalErrors != 1 {
				t.Fatalf("global errors=%d frames=%+v", globalErrors, frames)
			}
			if len(failingStore.failed) != 0 || len(failingStore.done) != 0 || failingStore.globalFails != 1 || failingStore.gets != 1 || failingStore.globalFailUserID != 17 || failingStore.globalFailGeneration != platformCompareTestGenerationID || failingStore.globalFailLease != failingStore.claim.LeaseToken || failingStore.globalFailCode != "internal_error" || failingStore.globalFailedSnapshot.ModelStates["model-b"].State != PlatformGenerationStateCompleted {
				t.Fatalf("store after fatal=%+v", failingStore)
			}
			if cancelCalls.Load() != 1 || registry.effects.unregister != 1 || registry.effects.persist != 0 || registry.effects.receipt != 0 {
				t.Fatalf("cancel=%d registry effects=%+v", cancelCalls.Load(), registry.effects)
			}
			if len(upstream.bodies) != 2 {
				t.Fatalf("bodies=%v", upstream.bodies)
			}
			for model, body := range upstream.bodies {
				if body.closes.Load() != 1 {
					t.Fatalf("model=%s closes=%d", model, body.closes.Load())
				}
			}
		})
	}
}
func TestPlatformCompareGenerationSerializesEncoderFailures(t *testing.T) {
	testPlatformCompareEncoderFailures(t)
}
func TestPlatformCompareGenerationSerializesEncoderFactoryFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		factory func(string, []string) (platformCompareEncoder, error)
	}{
		{
			name: "factory error",
			factory: func(string, []string) (platformCompareEncoder, error) {
				return nil, errors.New("encoder factory unavailable")
			},
		},
		{
			name: "nil encoder",
			factory: func(string, []string) (platformCompareEncoder, error) {
				return nil, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
			runner.deps.newEncoder = test.factory
			input := platformCompareTestInput()
			input.Write = func([]byte) error { effects.write++; return nil }
			result, err := runner.Run(input)
			if !errors.Is(err, ErrPlatformCompareGenerationUnavailable) || errors.Is(err, ErrPlatformCompareGenerationInvalid) || result.Started || result.Duplicate != nil {
				t.Fatalf("Run result=%+v error=%v", result, err)
			}
			if effects.admission != 1 || effects.admissionRelease != 1 || effects.guid != 0 || effects.claim != 0 || effects.register != 0 || effects.unregister != 0 || effects.upstream != 0 || effects.write != 0 || effects.persist != 0 || effects.receipt != 0 {
				t.Fatalf("factory failure effects=%+v", effects)
			}
		})
	}
}
func TestPlatformCompareGenerationNeverCallsHTTPWriterConcurrently(t *testing.T) {
	_, writer, _, result := platformCompareConcurrentFixture(t, false)
	if !result.Started || writer.maximum.Load() != 1 {
		t.Fatalf("result=%+v writer max=%d", result, writer.maximum.Load())
	}
}
func TestPlatformCompareGenerationClosesEveryResponseBodyExactlyOnce(t *testing.T) {
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{
		"model-a": platformCompareSuccessfulScript("model-a", "ok"),
		"model-b": func(_ context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			_ = emit(platformCompareDeltaChunk("wrong-model", "bad", true))
			return whitelabel.ErrUpstreamUnavailable("callback")
		},
		"model-c": func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			return whitelabel.ErrUpstreamUnavailable("provider failure")
		},
	}
	runner, store, upstream, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b", "model-c"}, scripts)
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b", "model-c"}, writer.Write
	result, _ := runner.Run(input)
	if !result.Started || len(upstream.bodies) != 3 || len(store.done) != 1 || len(store.failed) != 2 {
		t.Fatalf("result=%+v bodies=%v done=%v failed=%v", result, upstream.bodies, store.done, store.failed)
	}
	for model, body := range upstream.bodies {
		if body.closes.Load() != 1 {
			t.Fatalf("model=%s closes=%d", model, body.closes.Load())
		}
	}
}
func TestPlatformCompareGenerationDetachedWriterStillRunsAllModels(t *testing.T) {
	store, writer, bodies, result := platformCompareConcurrentFixture(t, true)
	if !result.Started || len(writer.frames) != 1 || len(store.done) != 2 || len(store.records) != 2 {
		t.Fatalf("result=%+v writes=%d records=%v done=%v", result, len(writer.frames), store.records, store.done)
	}
	for model, body := range bodies {
		if body.closes.Load() != 1 {
			t.Fatalf("model=%s closes=%d", model, body.closes.Load())
		}
	}
}

func TestPlatformCompareGenerationRenewsOneLeaseEveryTenSeconds(t *testing.T) {
	finish := make(chan struct{})
	started := make(chan string, 2)
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{}
	for _, model := range []string{"model-a", "model-b"} {
		model := model
		scripts[model] = func(ctx context.Context, emit func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			started <- model
			select {
			case <-finish:
			case <-ctx.Done():
				return whitelabel.ErrUpstreamUnavailable("cancelled")
			}
			return platformCompareSuccessfulScript(model, "ok")(ctx, emit)
		}
	}
	runner, store, _, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b"}, scripts)
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 2)}
	runner.deps.newTimer = timers.New
	store.renewed = make(chan struct{})
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b"}, writer.Write
	done := make(chan struct{})
	go func() { _, _ = runner.Run(input); close(done) }()
	<-started
	<-started
	timer := <-timers.created
	timer.ch <- time.UnixMilli(20_000)
	<-store.renewed
	if writer.count() != 1 {
		t.Fatalf("renewal advanced encoder or wrote output: frames=%d", writer.count())
	}
	second := <-timers.created
	close(finish)
	<-done
	if store.renewCalls != 1 || store.renewUserID != 17 || store.renewGeneration != platformCompareTestGenerationID || store.renewLease != store.claim.LeaseToken || store.renewNowMillis != 10_000 || !timer.stopped.Load() || !second.stopped.Load() {
		t.Fatalf("renewals=%d firstStopped=%v secondStopped=%v", store.renewCalls, timer.stopped.Load(), second.stopped.Load())
	}
}

func testPlatformCompareCancellationClosesBodies(t *testing.T, shutdown, detached bool) {
	abort := make(chan struct{})
	started := make(chan string, 2)
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{}
	for _, model := range []string{"model-a", "model-b"} {
		model := model
		scripts[model] = func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			started <- model
			<-abort
			return whitelabel.ErrUpstreamUnavailable("blocked consumer")
		}
	}
	runner, store, upstream, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b"}, scripts)
	registry := runner.deps.registry.(*platformCompareTestRegistry)
	if !shutdown {
		cancelling := clonePlatformGeneration(store.claim.Snapshot)
		cancelling.State = PlatformGenerationStateCancelling
		cancelling.LeaseUntilMillis = 0
		store.settleSnapshot = cancelling
	}
	writer.failFirst = detached
	root, cancelRoot := context.WithCancel(context.Background())
	runner.deps.rootContext = root
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b"}, writer.Write
	done := make(chan struct{})
	go func() { _, _ = runner.Run(input); close(done) }()
	<-started
	<-started
	if shutdown {
		cancelRoot()
	} else {
		registry.registered()
	}
	for _, body := range upstream.bodies {
		<-body.closed
	}
	close(abort)
	<-done
	for model, body := range upstream.bodies {
		if body.closes.Load() != 1 {
			t.Fatalf("model=%s closes=%d", model, body.closes.Load())
		}
	}
	effects := registry.effects
	if effects.admission != 1 || effects.admissionRelease != 1 || effects.register != 1 || effects.unregister != 1 || effects.runnerCancel != 1 || effects.persist != 0 || effects.receipt != 0 {
		t.Fatalf("cancellation cleanup effects=%+v", effects)
	}
	if shutdown {
		if store.globalFails != 1 || store.ackCalls != 0 {
			t.Fatalf("shutdown authority fail=%d ack=%d", store.globalFails, store.ackCalls)
		}
	} else if store.ackCalls != 1 || store.globalFails != 0 {
		t.Fatalf("explicit cancel authority fail=%d ack=%d", store.globalFails, store.ackCalls)
	}
	frames := platformCompareDecodeFrames(t, writer.frames)
	if detached {
		if len(frames) != 1 || frames[0].event != "meta" {
			t.Fatalf("detached writer received post-detach frames=%+v", frames)
		}
		return
	}
	wantCode := "cancelled"
	if shutdown {
		wantCode = "internal_error"
	}
	globalErrors := 0
	for _, frame := range frames {
		if frame.event == "done" {
			t.Fatalf("cancellation emitted done: %+v", frames)
		}
		if frame.event == "error" {
			globalErrors++
			if frame.data["code"] != wantCode {
				t.Fatalf("global error=%+v want code=%s", frame, wantCode)
			}
		}
	}
	if globalErrors != 1 {
		t.Fatalf("global errors=%d frames=%+v", globalErrors, frames)
	}
}

func TestPlatformCompareGenerationExplicitCancelStopsAllWorkersAndBodies(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(strconv.FormatBool(detached), func(t *testing.T) {
			testPlatformCompareCancellationClosesBodies(t, false, detached)
		})
	}
}
func TestPlatformCompareGenerationShutdownDrainsAdmissionAndRegistration(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(strconv.FormatBool(detached), func(t *testing.T) {
			testPlatformCompareCancellationClosesBodies(t, true, detached)
		})
	}
}

func TestPlatformCompareGenerationAllModelsFailedSkipsCommitAndPersistence(t *testing.T) {
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{}
	for _, model := range []string{"model-a", "model-b"} {
		model := model
		scripts[model] = func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			return whitelabel.ErrUpstreamUnavailable("provider secret")
		}
	}
	runner, store, _, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b"}, scripts)
	snapshot := clonePlatformGeneration(store.claim.Snapshot)
	for model := range snapshot.ModelStates {
		snapshot.ModelStates[model] = PlatformGenerationModel{State: PlatformGenerationStateFailed, ErrorCode: "gateway_upstream_error"}
	}
	store.settleSnapshot = snapshot
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b"}, writer.Write
	result, err := runner.Run(input)
	effects := runner.deps.persistence.(*platformCompareTestPersistence).effects
	frames := platformCompareDecodeFrames(t, writer.frames)
	globalErrors := 0
	for _, frame := range frames {
		if frame.event == "error" {
			globalErrors++
			if frame.data["code"] != "gateway_upstream_error" {
				t.Fatalf("unsanitized global error=%+v", frame)
			}
		}
		if frame.event == "done" {
			t.Fatal("all-failed emitted done")
		}
	}
	if !result.Started || !errors.Is(err, ErrPlatformCompareGenerationUnavailable) || store.globalFails != 1 || globalErrors != 1 || effects.persist != 0 || effects.receipt != 0 {
		t.Fatalf("result=%+v err=%v store=%+v frames=%+v", result, err, store, frames)
	}
}

func TestPlatformCompareGenerationRenewalFailureCancelsWorkersAndFailsClosed(t *testing.T) {
	finish := make(chan struct{})
	started := make(chan string, 2)
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{}
	for _, model := range []string{"model-a", "model-b"} {
		model := model
		scripts[model] = func(ctx context.Context, _ func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			started <- model
			select {
			case <-ctx.Done():
				return whitelabel.ErrUpstreamUnavailable("cancelled")
			case <-finish:
				return nil
			}
		}
	}
	runner, store, upstream, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b"}, scripts)
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 1)}
	runner.deps.newTimer = timers.New
	store.renewErr, store.renewed = ErrPlatformGenerationUnavailable, make(chan struct{})
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b"}, writer.Write
	done := make(chan struct{})
	go func() { _, _ = runner.Run(input); close(done) }()
	<-started
	<-started
	timer := <-timers.created
	timer.ch <- time.UnixMilli(20_000)
	<-store.renewed
	<-done
	if store.globalFails != 1 || store.renewCalls != 1 || !timer.stopped.Load() {
		t.Fatalf("store=%+v timerStopped=%v", store, timer.stopped.Load())
	}
	for model, body := range upstream.bodies {
		if body.closes.Load() != 1 {
			t.Fatalf("model=%s closes=%d", model, body.closes.Load())
		}
	}
	frames := platformCompareDecodeFrames(t, writer.frames)
	globalErrors := 0
	for _, frame := range frames {
		if frame.event == "done" {
			t.Fatalf("renewal failure emitted done: %+v", frames)
		}
		if frame.event == "error" {
			globalErrors++
			if frame.data["code"] != "internal_error" {
				t.Fatalf("renewal failure error=%+v", frame)
			}
		}
	}
	if globalErrors != 1 {
		t.Fatalf("renewal failure global errors=%d frames=%+v", globalErrors, frames)
	}
}

func TestPlatformCompareGenerationRenewalFailureUnknownAuthorityEmitsNothing(t *testing.T) {
	started := make(chan string, 2)
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{}
	for _, model := range []string{"model-a", "model-b"} {
		model := model
		scripts[model] = func(ctx context.Context, _ func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			started <- model
			<-ctx.Done()
			return whitelabel.ErrUpstreamUnavailable("cancelled")
		}
	}
	runner, store, _, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b"}, scripts)
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 1)}
	runner.deps.newTimer = timers.New
	store.renewErr, store.renewed = ErrPlatformGenerationUnavailable, make(chan struct{})
	store.settleSnapshot = PlatformGenerationSnapshot{GenerationID: "unknown-authority"}
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b"}, writer.Write
	done := make(chan struct{})
	go func() { _, _ = runner.Run(input); close(done) }()
	<-started
	<-started
	timer := <-timers.created
	timer.ch <- time.UnixMilli(20_000)
	<-store.renewed
	<-done
	frames := platformCompareDecodeFrames(t, writer.frames)
	for _, frame := range frames {
		if frame.event == "error" || frame.event == "done" {
			t.Fatalf("unknown authority invented terminal frame=%+v", frames)
		}
	}
	if store.gets != 1 || store.globalFails != 0 || store.ackCalls != 0 {
		t.Fatalf("unknown authority mutated store=%+v", store)
	}
}

func TestPlatformCompareGenerationCancelAndTerminalRaceHasOneAuthority(t *testing.T) {
	doneObserved := make(chan struct{})
	abort := make(chan struct{})
	modelBStarted := make(chan struct{})
	scripts := map[string]func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error{
		"model-a": platformCompareSuccessfulScript("model-a", "winner"),
		"model-b": func(context.Context, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
			close(modelBStarted)
			<-abort
			return whitelabel.ErrUpstreamUnavailable("cancelled loser")
		},
	}
	runner, store, upstream, writer := platformCompareStreamFixture(t, []string{"model-a", "model-b"}, scripts)
	store.doneObserved = doneObserved
	cancelling := clonePlatformGeneration(store.claim.Snapshot)
	cancelling.State = PlatformGenerationStateCancelling
	cancelling.LeaseUntilMillis = 0
	cancelling.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateCompleted, Seq: 1}
	store.settleSnapshot = cancelling
	registry := runner.deps.registry.(*platformCompareTestRegistry)
	input := platformCompareTestInput()
	input.Models, input.Write = []string{"model-a", "model-b"}, writer.Write
	finished := make(chan struct{})
	go func() { _, _ = runner.Run(input); close(finished) }()
	<-doneObserved
	<-modelBStarted
	registry.registered()
	<-upstream.bodies["model-b"].closed
	close(abort)
	<-finished
	if store.ackCalls != 1 || store.globalFails != 0 || store.acknowledgedSnapshot.State != PlatformGenerationStateCancelled || store.acknowledgedSnapshot.ModelStates["model-a"].State != PlatformGenerationStateCompleted || store.acknowledgedSnapshot.ModelStates["model-b"].State != PlatformGenerationStateCancelled {
		t.Fatalf("authority store=%+v", store)
	}
	frames := platformCompareDecodeFrames(t, writer.frames)
	globalErrors := 0
	for _, frame := range frames {
		if frame.event == "error" {
			globalErrors++
			if frame.data["code"] != "cancelled" {
				t.Fatalf("race error=%+v", frame)
			}
		}
		if frame.event == "done" {
			t.Fatalf("race emitted done=%+v", frames)
		}
	}
	if globalErrors != 1 {
		t.Fatalf("race global errors=%d frames=%+v", globalErrors, frames)
	}
}
