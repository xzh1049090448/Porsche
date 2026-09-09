package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const controlGenerationID = "550e8400-e29b-41d4-a716-446655440000"

type platformGenerationControlFake struct {
	get                func(context.Context, int64, string) (PlatformGenerationSnapshot, error)
	cancelOrCreate     func(context.Context, int64, string, int64) (PlatformGenerationCancelDecision, error)
	failExpiredRunning func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error)
	convergeCancelling func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error)
	reconcile          func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error)
	loadReceipt        func(context.Context, int64, string) (PlatformGenerationReceiptSnapshot, error)
	loadTotalTokens    func(context.Context, int64) (int64, error)
	getCalls           atomic.Int64
	cancelCalls        atomic.Int64
	failCalls          atomic.Int64
	convergeCalls      atomic.Int64
	reconcileCalls     atomic.Int64
	receiptCalls       atomic.Int64
	totalCalls         atomic.Int64
}

func newPlatformGenerationControlFake(snapshot PlatformGenerationSnapshot) *platformGenerationControlFake {
	fake := &platformGenerationControlFake{}
	fake.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
		fake.getCalls.Add(1)
		return snapshot, nil
	}
	fake.cancelOrCreate = func(context.Context, int64, string, int64) (PlatformGenerationCancelDecision, error) {
		fake.cancelCalls.Add(1)
		return PlatformGenerationCancelDecision{Snapshot: snapshot}, nil
	}
	fake.failExpiredRunning = func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error) {
		fake.failCalls.Add(1)
		return snapshot, nil
	}
	fake.convergeCancelling = func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error) {
		fake.convergeCalls.Add(1)
		return snapshot, nil
	}
	fake.reconcile = func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error) {
		fake.reconcileCalls.Add(1)
		return snapshot, nil
	}
	fake.loadReceipt = func(context.Context, int64, string) (PlatformGenerationReceiptSnapshot, error) {
		fake.receiptCalls.Add(1)
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceNotFound
	}
	fake.loadTotalTokens = func(context.Context, int64) (int64, error) {
		fake.totalCalls.Add(1)
		return 0, nil
	}
	return fake
}

func (f *platformGenerationControlFake) deps() platformGenerationControlDeps {
	return platformGenerationControlDeps{
		get:                     f.get,
		cancelOrCreate:          f.cancelOrCreate,
		failExpiredRunning:      f.failExpiredRunning,
		convergeStaleCancelling: f.convergeCancelling,
		reconcile:               f.reconcile,
		loadReceipt:             f.loadReceipt,
		loadTotalTokens:         f.loadTotalTokens,
	}
}

func controlSnapshot(state PlatformGenerationState, nowMillis int64) PlatformGenerationSnapshot {
	modelState := state
	if state == PlatformGenerationStateCancelling || state == PlatformGenerationStateRunning {
		modelState = PlatformGenerationStateRunning
	}
	if state == PlatformGenerationStateCommitting {
		modelState = PlatformGenerationStateCompleted
	}
	snapshot := PlatformGenerationSnapshot{
		GenerationID:    controlGenerationID,
		Mode:            PlatformGenerationModeSingle,
		Models:          []string{"model-a"},
		State:           state,
		ModelStates:     map[string]PlatformGenerationModel{"model-a": {State: modelState}},
		CreatedAtMillis: nowMillis - 60_000,
		UpdatedAtMillis: nowMillis,
	}
	if state == PlatformGenerationStateFailed {
		snapshot.ErrorCode = "internal_error"
		snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateFailed, ErrorCode: "internal_error"}
	}
	if state == PlatformGenerationStateCancelled {
		snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateCancelled}
	}
	if state == PlatformGenerationStateCompleted {
		snapshot.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001"}
	}
	return snapshot
}

func controlTombstone(nowMillis int64) PlatformGenerationSnapshot {
	return PlatformGenerationSnapshot{GenerationID: controlGenerationID, Models: []string{}, State: PlatformGenerationStateCancelled, ModelStates: map[string]PlatformGenerationModel{}, CreatedAtMillis: nowMillis, UpdatedAtMillis: nowMillis}
}

func mustControl(t *testing.T, fake *platformGenerationControlFake, registry *PlatformGenerationCancellationRegistry, now time.Time) *PlatformGenerationControl {
	t.Helper()
	control, err := newPlatformGenerationControl(fake.deps(), registry)
	if err != nil {
		t.Fatalf("newPlatformGenerationControl() error = %v", err)
	}
	control.now = func() time.Time { return now }
	return control
}

func TestPlatformGenerationControlConstructorAndInputValidationFailClosed(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	fake := newPlatformGenerationControlFake(controlSnapshot(PlatformGenerationStateRunning, now.UnixMilli()))
	registry := NewPlatformGenerationCancellationRegistry()

	if _, err := newPlatformGenerationControl(platformGenerationControlDeps{}, registry); err != ErrPlatformGenerationControlUnavailable {
		t.Fatalf("empty deps error = %v", err)
	}
	for _, test := range []struct {
		name   string
		remove func(*platformGenerationControlDeps)
	}{
		{name: "get", remove: func(d *platformGenerationControlDeps) { d.get = nil }},
		{name: "cancelOrCreate", remove: func(d *platformGenerationControlDeps) { d.cancelOrCreate = nil }},
		{name: "failExpiredRunning", remove: func(d *platformGenerationControlDeps) { d.failExpiredRunning = nil }},
		{name: "convergeStaleCancelling", remove: func(d *platformGenerationControlDeps) { d.convergeStaleCancelling = nil }},
		{name: "reconcile", remove: func(d *platformGenerationControlDeps) { d.reconcile = nil }},
		{name: "loadReceipt", remove: func(d *platformGenerationControlDeps) { d.loadReceipt = nil }},
		{name: "loadTotalTokens", remove: func(d *platformGenerationControlDeps) { d.loadTotalTokens = nil }},
	} {
		t.Run("missing "+test.name, func(t *testing.T) {
			deps := fake.deps()
			test.remove(&deps)
			if _, err := newPlatformGenerationControl(deps, registry); err != ErrPlatformGenerationControlUnavailable {
				t.Fatalf("constructor error = %v", err)
			}
		})
	}
	if _, err := newPlatformGenerationControl(fake.deps(), nil); err != ErrPlatformGenerationControlUnavailable {
		t.Fatalf("nil registry error = %v", err)
	}
	control := mustControl(t, fake, registry, now)
	var nilControl *PlatformGenerationControl
	for _, test := range []struct {
		name         string
		controller   *PlatformGenerationControl
		ctx          context.Context
		userID       int64
		generationID string
	}{
		{name: "nil controller", ctx: context.Background(), userID: 1, generationID: controlGenerationID},
		{name: "nil context", controller: control, userID: 1, generationID: controlGenerationID},
		{name: "zero user", controller: control, ctx: context.Background(), generationID: controlGenerationID},
		{name: "noncanonical uuid", controller: control, ctx: context.Background(), userID: 1, generationID: strings.ToUpper(controlGenerationID)},
	} {
		t.Run(test.name+" get", func(t *testing.T) {
			controller := test.controller
			if test.name == "nil controller" {
				controller = nilControl
			}
			if _, err := controller.Get(test.ctx, test.userID, test.generationID); err != ErrPlatformGenerationControlInvalid {
				t.Fatalf("Get() error = %v", err)
			}
		})
		t.Run(test.name+" cancel", func(t *testing.T) {
			controller := test.controller
			if test.name == "nil controller" {
				controller = nilControl
			}
			if _, _, err := controller.Cancel(test.ctx, test.userID, test.generationID); err != ErrPlatformGenerationControlInvalid {
				t.Fatalf("Cancel() error = %v", err)
			}
		})
	}
	if fake.getCalls.Load() != 0 || fake.cancelCalls.Load() != 0 || fake.failCalls.Load() != 0 || fake.receiptCalls.Load() != 0 {
		t.Fatalf("dependencies called during validation: %#v", fake)
	}
}

func TestPlatformGenerationViewProjectsEveryNonCompletedStateWithoutSecrets(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	for _, state := range []PlatformGenerationState{PlatformGenerationStateRunning, PlatformGenerationStateCancelling, PlatformGenerationStateCancelled, PlatformGenerationStateCommitting, PlatformGenerationStateFailed} {
		t.Run(platformGenerationControlStateString(state), func(t *testing.T) {
			snapshot := controlSnapshot(state, now.UnixMilli())
			if state == PlatformGenerationStateRunning {
				snapshot.LeaseOwnerSHA256 = strings.Repeat("a", 64)
				snapshot.LeaseUntilMillis = now.Add(time.Second).UnixMilli()
			}
			fake := newPlatformGenerationControlFake(snapshot)
			if state == PlatformGenerationStateCommitting {
				fake.reconcile = func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error) {
					fake.reconcileCalls.Add(1)
					return snapshot, ErrPlatformGenerationConflict
				}
			}
			view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if view.GenerationID != controlGenerationID || view.Status != platformGenerationControlStateString(state) || view.Mode == nil || *view.Mode != "single" || view.ConversationGUID != nil || view.Result != nil || len(view.Results) != 0 || view.TotalTokensUsed != nil || view.RequestID != "" {
				t.Fatalf("view = %#v", view)
			}
			if state == PlatformGenerationStateFailed && view.Code != "internal_error" {
				t.Fatalf("failed code = %q", view.Code)
			}
			encoded, marshalErr := json.Marshal(view)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, forbidden := range []string{"content", "tokens", "assistant_message_guid", "\"result\"", "\"results\"", "request_id"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("JSON %s leaked %q", encoded, forbidden)
				}
			}
		})
	}

	tombstone := controlTombstone(now.UnixMilli())
	view, err := mustControl(t, newPlatformGenerationControlFake(tombstone), NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if string(encoded) != `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"cancelled","mode":null,"conversation_guid":null}` {
		t.Fatalf("tombstone JSON = %s", encoded)
	}
}

func TestPlatformGenerationViewHydratesCompletedSingleAndCompareExactly(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	t.Run("single zero tokens and current account total", func(t *testing.T) {
		snapshot := controlSnapshot(PlatformGenerationStateCompleted, now.UnixMilli())
		fake := newPlatformGenerationControlFake(snapshot)
		fake.loadReceipt = func(context.Context, int64, string) (PlatformGenerationReceiptSnapshot, error) {
			fake.receiptCalls.Add(1)
			return PlatformGenerationReceiptSnapshot{UserID: 7, GenerationID: controlGenerationID, Mode: PlatformGenerationModeSingle, ConversationGUID: 8001, UserMessage: "never expose", Results: []PlatformGenerationCommittedResult{{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001", Content: "answer", Tokens: 0}}, SuccessfulModelCount: 1}, nil
		}
		fake.loadTotalTokens = func(context.Context, int64) (int64, error) { fake.totalCalls.Add(1); return 99, nil }
		view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Result == nil || view.Result.Tokens == nil || *view.Result.Tokens != 0 || view.Result.Content != "answer" || view.Result.AssistantMessageGUID != "9001" || view.TotalTokensUsed == nil || *view.TotalTokensUsed != 99 || view.ConversationGUID == nil || *view.ConversationGUID != "8001" || view.Results != nil {
			t.Fatalf("single view = %#v", view)
		}
		encoded, _ := json.Marshal(view)
		wantJSON := `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"completed","mode":"single","conversation_guid":"8001","result":{"model":"model-a","status":"completed","assistant_message_guid":"9001","content":"answer","tokens":0},"total_tokens_used":99}`
		if string(encoded) != wantJSON || strings.Contains(string(encoded), "never expose") {
			t.Fatalf("single JSON = %s", encoded)
		}
	})

	t.Run("ordered compare partial success", func(t *testing.T) {
		snapshot := controlSnapshot(PlatformGenerationStateCompleted, now.UnixMilli())
		snapshot.Mode = PlatformGenerationModeCompare
		snapshot.Models = []string{"model-a", "model-b"}
		snapshot.ModelStates = map[string]PlatformGenerationModel{
			"model-a": {State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001"},
			"model-b": {State: PlatformGenerationStateFailed, ErrorCode: "upstream_error"},
		}
		fake := newPlatformGenerationControlFake(snapshot)
		fake.loadReceipt = func(context.Context, int64, string) (PlatformGenerationReceiptSnapshot, error) {
			return PlatformGenerationReceiptSnapshot{UserID: 7, GenerationID: controlGenerationID, Mode: PlatformGenerationModeCompare, ConversationGUID: 8001, Results: []PlatformGenerationCommittedResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001", Content: "ok", Tokens: 4},
				{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "upstream_error"},
			}, SuccessfulModelCount: 1, UserMessage: "secret prompt"}, nil
		}
		fake.loadTotalTokens = func(context.Context, int64) (int64, error) { return 123, nil }
		view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Result != nil || len(view.Results) != 2 || view.Results[0].Model != "model-a" || view.Results[0].Tokens == nil || *view.Results[0].Tokens != 4 || view.Results[1].Model != "model-b" || view.Results[1].Code != "upstream_error" || view.Results[1].Tokens != nil || view.Results[1].Content != "" || view.Results[1].AssistantMessageGUID != "" {
			t.Fatalf("compare view = %#v", view)
		}
		encoded, _ := json.Marshal(view)
		wantJSON := `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"completed","mode":"compare","conversation_guid":"8001","results":[{"model":"model-a","status":"completed","assistant_message_guid":"9001","content":"ok","tokens":4},{"model":"model-b","status":"failed","code":"upstream_error"}],"total_tokens_used":123}`
		if string(encoded) != wantJSON || strings.Contains(string(encoded), "secret prompt") {
			t.Fatalf("compare JSON = %s", encoded)
		}
	})
}

func TestPlatformGenerationReceiptMatchesRejectsEveryRedisDifferenceAndNeverUsesUserMessage(t *testing.T) {
	now := int64(1_800_000_000_000)
	snapshot := controlSnapshot(PlatformGenerationStateCompleted, now)
	receipt := PlatformGenerationReceiptSnapshot{UserID: 7, GenerationID: controlGenerationID, Mode: PlatformGenerationModeSingle, ConversationGUID: 8001, UserMessage: "ignored", SuccessfulModelCount: 1, Results: []PlatformGenerationCommittedResult{{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001", Content: "answer", Tokens: 1}}}
	if !platformGenerationReceiptMatches(snapshot, receipt, 7, controlGenerationID) {
		t.Fatal("valid graph rejected")
	}
	mutations := map[string]func(*PlatformGenerationSnapshot, *PlatformGenerationReceiptSnapshot){
		"user": func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) { r.UserID++ },
		"generation": func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.GenerationID = "c0a8012e-ef48-4a5d-9ca7-9a78d055e7f6"
		},
		"mode": func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.Mode = PlatformGenerationModeCompare
		},
		"order model": func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.Results[0].Model = "model-b"
		},
		"cardinality": func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.Results = append(r.Results, r.Results[0])
		},
		"state": func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.Results[0].State = PlatformGenerationStateFailed
			r.Results[0].ErrorCode = "internal_error"
			r.Results[0].AssistantMessageGUID = ""
			r.Results[0].Content = ""
		},
		"guid": func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.Results[0].AssistantMessageGUID = "9002"
		},
		"redis code": func(s *PlatformGenerationSnapshot, _ *PlatformGenerationReceiptSnapshot) {
			s.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateFailed, ErrorCode: "internal_error"}
		},
		"tombstone": func(s *PlatformGenerationSnapshot, _ *PlatformGenerationReceiptSnapshot) { *s = controlTombstone(now) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := clonePlatformGeneration(snapshot)
			r := receipt
			r.Results = append([]PlatformGenerationCommittedResult(nil), receipt.Results...)
			mutate(&s, &r)
			if platformGenerationReceiptMatches(s, r, 7, controlGenerationID) {
				t.Fatal("mismatch accepted")
			}
		})
	}
	receipt.UserMessage = "different ignored prompt"
	if !platformGenerationReceiptMatches(snapshot, receipt, 7, controlGenerationID) {
		t.Fatal("matcher used UserMessage")
	}

	compare := snapshot
	compare.Mode = PlatformGenerationModeCompare
	compare.Models = []string{"model-a", "model-b"}
	compare.ModelStates = map[string]PlatformGenerationModel{
		"model-a": {State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001"},
		"model-b": {State: PlatformGenerationStateFailed, ErrorCode: "upstream_error"},
	}
	compareReceipt := PlatformGenerationReceiptSnapshot{UserID: 7, GenerationID: controlGenerationID, Mode: PlatformGenerationModeCompare, ConversationGUID: 8001, SuccessfulModelCount: 1, Results: []PlatformGenerationCommittedResult{
		{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001", Content: "answer", Tokens: 1},
		{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "upstream_error"},
	}}
	if !platformGenerationReceiptMatches(compare, compareReceipt, 7, controlGenerationID) {
		t.Fatal("valid partial compare rejected")
	}
	for _, mutate := range []func(*PlatformGenerationReceiptSnapshot){
		func(r *PlatformGenerationReceiptSnapshot) { r.Results[0], r.Results[1] = r.Results[1], r.Results[0] },
		func(r *PlatformGenerationReceiptSnapshot) { r.Results[1].ErrorCode = "internal_error" },
		func(r *PlatformGenerationReceiptSnapshot) { r.Results[1].State = PlatformGenerationStateCancelled },
	} {
		r := compareReceipt
		r.Results = append([]PlatformGenerationCommittedResult(nil), compareReceipt.Results...)
		mutate(&r)
		if platformGenerationReceiptMatches(compare, r, 7, controlGenerationID) {
			t.Fatal("compare receipt mismatch accepted")
		}
	}
}

func TestPlatformGenerationControlDependencyErrorsAreFixedAndSecretFree(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	secret := "redis-password-raw"
	for _, dependencyErr := range []error{errors.New(secret), ErrPlatformGenerationInvalid, ErrPlatformGenerationPersistenceIntegrity} {
		fake := newPlatformGenerationControlFake(controlSnapshot(PlatformGenerationStateRunning, now.UnixMilli()))
		fake.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
			return PlatformGenerationSnapshot{}, dependencyErr
		}
		_, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
		if err != ErrPlatformGenerationControlUnavailable || strings.Contains(err.Error(), secret) {
			t.Fatalf("Get() error = %q", err)
		}
	}
	fake := newPlatformGenerationControlFake(controlSnapshot(PlatformGenerationStateRunning, now.UnixMilli()))
	fake.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationNotFound
	}
	if _, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID); err != ErrPlatformGenerationControlNotFound {
		t.Fatalf("not found = %v", err)
	}
}

func TestPlatformGenerationControlConvergesBoundariesAndCASOnce(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	t.Run("active lease stays running", func(t *testing.T) {
		snapshot := controlSnapshot(PlatformGenerationStateRunning, now.UnixMilli())
		snapshot.LeaseOwnerSHA256, snapshot.LeaseUntilMillis = strings.Repeat("a", 64), now.Add(time.Millisecond).UnixMilli()
		fake := newPlatformGenerationControlFake(snapshot)
		view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
		if err != nil || view.Status != "running" || fake.failCalls.Load() != 0 {
			t.Fatalf("view/error/calls = %#v/%v/%d", view, err, fake.failCalls.Load())
		}
	})
	t.Run("legacy and expired running fail", func(t *testing.T) {
		for _, leased := range []bool{false, true} {
			snapshot := controlSnapshot(PlatformGenerationStateRunning, now.Add(-time.Second).UnixMilli())
			if leased {
				snapshot.LeaseOwnerSHA256, snapshot.LeaseUntilMillis = strings.Repeat("a", 64), now.UnixMilli()
			}
			failed := controlSnapshot(PlatformGenerationStateFailed, now.UnixMilli())
			fake := newPlatformGenerationControlFake(snapshot)
			fake.failExpiredRunning = func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error) {
				fake.failCalls.Add(1)
				return failed, nil
			}
			view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
			if err != nil || view.Status != "failed" || fake.failCalls.Load() != 1 {
				t.Fatalf("view/error/calls = %#v/%v/%d", view, err, fake.failCalls.Load())
			}
		}
	})
	t.Run("cancelling boundary", func(t *testing.T) {
		for _, age := range []time.Duration{29_999 * time.Millisecond, 30_000 * time.Millisecond} {
			snapshot := controlSnapshot(PlatformGenerationStateCancelling, now.Add(-age).UnixMilli())
			cancelled := controlSnapshot(PlatformGenerationStateCancelled, now.UnixMilli())
			fake := newPlatformGenerationControlFake(snapshot)
			fake.convergeCancelling = func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error) {
				fake.convergeCalls.Add(1)
				return cancelled, nil
			}
			view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := int64(0)
			wantState := "cancelling"
			if age == 30*time.Second {
				wantCalls, wantState = 1, "cancelled"
			}
			if fake.convergeCalls.Load() != wantCalls || view.Status != wantState {
				t.Fatalf("age %s calls/status = %d/%s", age, fake.convergeCalls.Load(), view.Status)
			}
		}
	})
	t.Run("one conflict reload and no loop", func(t *testing.T) {
		snapshot := controlSnapshot(PlatformGenerationStateRunning, now.Add(-time.Second).UnixMilli())
		fake := newPlatformGenerationControlFake(snapshot)
		fake.failExpiredRunning = func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error) {
			fake.failCalls.Add(1)
			return snapshot, ErrPlatformGenerationConflict
		}
		_, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
		if err != ErrPlatformGenerationControlUnavailable || fake.failCalls.Load() != 2 || fake.getCalls.Load() != 2 {
			t.Fatalf("error/calls = %v/%d/%d", err, fake.failCalls.Load(), fake.getCalls.Load())
		}
	})
}

func TestPlatformGenerationControlCancelTombstoneTransitionTerminalAndTimeout(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	t.Run("tombstone immediate", func(t *testing.T) {
		tombstone := controlTombstone(now.UnixMilli())
		fake := newPlatformGenerationControlFake(tombstone)
		fake.cancelOrCreate = func(context.Context, int64, string, int64) (PlatformGenerationCancelDecision, error) {
			fake.cancelCalls.Add(1)
			return PlatformGenerationCancelDecision{Snapshot: tombstone, CreatedTombstone: true}, nil
		}
		view, pending, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Cancel(context.Background(), 7, controlGenerationID)
		if err != nil || pending || view.Status != "cancelled" || view.Mode != nil || fake.getCalls.Load() != 0 {
			t.Fatalf("Cancel() = %#v/%v/%v", view, pending, err)
		}
	})
	t.Run("running callback once then terminal", func(t *testing.T) {
		running := controlSnapshot(PlatformGenerationStateRunning, now.UnixMilli())
		cancelling := controlSnapshot(PlatformGenerationStateCancelling, now.UnixMilli())
		cancelled := controlSnapshot(PlatformGenerationStateCancelled, now.UnixMilli())
		fake := newPlatformGenerationControlFake(running)
		fake.cancelOrCreate = func(context.Context, int64, string, int64) (PlatformGenerationCancelDecision, error) {
			fake.cancelCalls.Add(1)
			return PlatformGenerationCancelDecision{Snapshot: cancelling, Transitioned: true}, nil
		}
		fake.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
			fake.getCalls.Add(1)
			return cancelled, nil
		}
		registry := NewPlatformGenerationCancellationRegistry()
		var callbackCalls atomic.Int64
		if _, err := registry.Register(7, controlGenerationID, func() { callbackCalls.Add(1) }); err != nil {
			t.Fatal(err)
		}
		control := mustControl(t, fake, registry, now)
		control.cancelBudget = 20 * time.Millisecond
		control.cancelPoll = time.Millisecond
		view, pending, err := control.Cancel(context.Background(), 7, controlGenerationID)
		if err != nil || pending || view.Status != "cancelled" || callbackCalls.Load() != 1 || fake.cancelCalls.Load() != 1 {
			t.Fatalf("Cancel() = %#v/%v/%v calls=%d/%d", view, pending, err, callbackCalls.Load(), fake.cancelCalls.Load())
		}
	})
	t.Run("existing terminal is idempotent", func(t *testing.T) {
		failed := controlSnapshot(PlatformGenerationStateFailed, now.UnixMilli())
		fake := newPlatformGenerationControlFake(failed)
		view, pending, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Cancel(context.Background(), 7, controlGenerationID)
		if err != nil || pending || view.Status != "failed" || fake.getCalls.Load() != 0 {
			t.Fatalf("Cancel() = %#v/%v/%v", view, pending, err)
		}
	})
	t.Run("cancelling timeout is pending", func(t *testing.T) {
		cancelling := controlSnapshot(PlatformGenerationStateCancelling, now.UnixMilli())
		fake := newPlatformGenerationControlFake(cancelling)
		control := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now)
		control.cancelBudget = 3 * time.Millisecond
		control.cancelPoll = time.Millisecond
		view, pending, err := control.Cancel(context.Background(), 7, controlGenerationID)
		if err != nil || !pending || view.Status != "cancelling" {
			t.Fatalf("Cancel() = %#v/%v/%v", view, pending, err)
		}
	})
}

func TestPlatformGenerationControlCancelContextStopsPromptlyAndSanitizes(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	cancelling := controlSnapshot(PlatformGenerationStateCancelling, now.UnixMilli())
	fake := newPlatformGenerationControlFake(cancelling)
	control := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now)
	control.cancelBudget, control.cancelPoll = time.Second, time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, _, err := control.Cancel(ctx, 7, controlGenerationID)
	if err != ErrPlatformGenerationControlUnavailable || time.Since(started) > 50*time.Millisecond || strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("Cancel() error/duration = %v/%s", err, time.Since(started))
	}
}

func TestPlatformGenerationControlCompletedDependenciesFailClosed(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	snapshot := controlSnapshot(PlatformGenerationStateCompleted, now.UnixMilli())
	validReceipt := PlatformGenerationReceiptSnapshot{
		UserID: 7, GenerationID: controlGenerationID, Mode: PlatformGenerationModeSingle,
		ConversationGUID: 8001, SuccessfulModelCount: 1,
		Results: []PlatformGenerationCommittedResult{{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001", Content: "answer", Tokens: 1}},
	}
	for _, test := range []struct {
		name   string
		mutate func(*PlatformGenerationReceiptSnapshot)
		err    error
	}{
		{name: "missing", err: ErrPlatformGenerationPersistenceNotFound},
		{name: "integrity", err: ErrPlatformGenerationPersistenceIntegrity},
		{name: "unavailable raw", err: errors.New("mysql://root:password@private")},
		{name: "zero conversation", mutate: func(r *PlatformGenerationReceiptSnapshot) { r.ConversationGUID = 0 }},
		{name: "negative tokens", mutate: func(r *PlatformGenerationReceiptSnapshot) { r.Results[0].Tokens = -1 }},
		{name: "empty content", mutate: func(r *PlatformGenerationReceiptSnapshot) { r.Results[0].Content = "" }},
		{name: "bad assistant guid", mutate: func(r *PlatformGenerationReceiptSnapshot) { r.Results[0].AssistantMessageGUID = "09001" }},
		{name: "success count", mutate: func(r *PlatformGenerationReceiptSnapshot) { r.SuccessfulModelCount = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newPlatformGenerationControlFake(snapshot)
			fake.loadReceipt = func(context.Context, int64, string) (PlatformGenerationReceiptSnapshot, error) {
				receipt := validReceipt
				receipt.Results = append([]PlatformGenerationCommittedResult(nil), validReceipt.Results...)
				if test.mutate != nil {
					test.mutate(&receipt)
				}
				return receipt, test.err
			}
			view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
			if err != ErrPlatformGenerationControlUnavailable || view.GenerationID != "" || fake.totalCalls.Load() != 0 {
				t.Fatalf("Get() = %#v, %v; total calls=%d", view, err, fake.totalCalls.Load())
			}
		})
	}
	for _, test := range []struct {
		name  string
		value int64
		err   error
	}{
		{name: "missing or disabled", err: gormErrRecordNotFoundForControlTest{}},
		{name: "negative", value: -1},
		{name: "unavailable", err: errors.New("db DSN secret")},
	} {
		t.Run("total "+test.name, func(t *testing.T) {
			fake := newPlatformGenerationControlFake(snapshot)
			fake.loadReceipt = func(context.Context, int64, string) (PlatformGenerationReceiptSnapshot, error) {
				return validReceipt, nil
			}
			fake.loadTotalTokens = func(context.Context, int64) (int64, error) { return test.value, test.err }
			view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
			if err != ErrPlatformGenerationControlUnavailable || view.GenerationID != "" || strings.Contains(err.Error(), "secret") {
				t.Fatalf("Get() = %#v, %v", view, err)
			}
		})
	}
}

type gormErrRecordNotFoundForControlTest struct{}

func (gormErrRecordNotFoundForControlTest) Error() string { return "record not found" }

func TestPlatformGenerationControlCommittingAlwaysReconcilesAcrossStaleBoundary(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	for _, test := range []struct {
		name      string
		age       time.Duration
		resolved  PlatformGenerationSnapshot
		reconcile error
		want      string
		wantCalls int64
	}{
		{name: "receipt absent pre boundary", age: 29_999 * time.Millisecond, reconcile: ErrPlatformGenerationConflict, want: "committing", wantCalls: 2},
		{name: "receipt absent at boundary fails", age: 30_000 * time.Millisecond, resolved: controlSnapshot(PlatformGenerationStateFailed, now.UnixMilli()), want: "failed", wantCalls: 1},
		{name: "receipt absent post boundary fails", age: 31 * time.Second, resolved: controlSnapshot(PlatformGenerationStateFailed, now.UnixMilli()), want: "failed", wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			committing := controlSnapshot(PlatformGenerationStateCommitting, now.Add(-test.age).UnixMilli())
			resolved := test.resolved
			if resolved.GenerationID == "" {
				resolved = committing
			}
			fake := newPlatformGenerationControlFake(committing)
			fake.reconcile = func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error) {
				fake.reconcileCalls.Add(1)
				return resolved, test.reconcile
			}
			view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
			if err != nil || view.Status != test.want || fake.reconcileCalls.Load() != test.wantCalls {
				t.Fatalf("Get() = %#v, %v; reconcile calls=%d", view, err, fake.reconcileCalls.Load())
			}
		})
	}

	t.Run("receipt present completes and hydrates", func(t *testing.T) {
		committing := controlSnapshot(PlatformGenerationStateCommitting, now.Add(-time.Millisecond).UnixMilli())
		completed := controlSnapshot(PlatformGenerationStateCompleted, now.UnixMilli())
		fake := newPlatformGenerationControlFake(committing)
		fake.reconcile = func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error) { return completed, nil }
		fake.loadReceipt = func(context.Context, int64, string) (PlatformGenerationReceiptSnapshot, error) {
			return PlatformGenerationReceiptSnapshot{UserID: 7, GenerationID: controlGenerationID, Mode: PlatformGenerationModeSingle, ConversationGUID: 8, SuccessfulModelCount: 1, Results: []PlatformGenerationCommittedResult{{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001", Content: "done"}}}, nil
		}
		view, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Get(context.Background(), 7, controlGenerationID)
		if err != nil || view.Status != "completed" || view.Result == nil || view.Result.Content != "done" {
			t.Fatalf("Get() = %#v, %v", view, err)
		}
	})
}

func TestPlatformGenerationControlCancelPollRacesAndSingleDeadline(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	t.Run("committing race resolves completed", func(t *testing.T) {
		committing := controlSnapshot(PlatformGenerationStateCommitting, now.UnixMilli())
		completed := controlSnapshot(PlatformGenerationStateCompleted, now.UnixMilli())
		fake := newPlatformGenerationControlFake(committing)
		fake.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) { return completed, nil }
		fake.loadReceipt = func(context.Context, int64, string) (PlatformGenerationReceiptSnapshot, error) {
			return PlatformGenerationReceiptSnapshot{UserID: 7, GenerationID: controlGenerationID, Mode: PlatformGenerationModeSingle, ConversationGUID: 8, SuccessfulModelCount: 1, Results: []PlatformGenerationCommittedResult{{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "9001", Content: "won"}}}, nil
		}
		control := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now)
		control.cancelBudget, control.cancelPoll = 20*time.Millisecond, time.Millisecond
		view, pending, err := control.Cancel(context.Background(), 7, controlGenerationID)
		if err != nil || pending || view.Status != "completed" || view.Result == nil || view.Result.Content != "won" {
			t.Fatalf("Cancel() = %#v/%v/%v", view, pending, err)
		}
	})

	t.Run("poll running reissues atomic cancel and never returns running", func(t *testing.T) {
		cancelling := controlSnapshot(PlatformGenerationStateCancelling, now.UnixMilli())
		running := controlSnapshot(PlatformGenerationStateRunning, now.UnixMilli())
		running.LeaseOwnerSHA256, running.LeaseUntilMillis = strings.Repeat("a", 64), now.Add(time.Second).UnixMilli()
		cancelled := controlSnapshot(PlatformGenerationStateCancelled, now.UnixMilli())
		fake := newPlatformGenerationControlFake(cancelling)
		var getCalls atomic.Int64
		fake.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
			if getCalls.Add(1) == 1 {
				return running, nil
			}
			return cancelled, nil
		}
		var decisions atomic.Int64
		fake.cancelOrCreate = func(context.Context, int64, string, int64) (PlatformGenerationCancelDecision, error) {
			switch decisions.Add(1) {
			case 1:
				return PlatformGenerationCancelDecision{Snapshot: cancelling}, nil
			default:
				return PlatformGenerationCancelDecision{Snapshot: cancelling, Transitioned: true}, nil
			}
		}
		control := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now)
		control.cancelBudget, control.cancelPoll = 20*time.Millisecond, time.Millisecond
		view, pending, err := control.Cancel(context.Background(), 7, controlGenerationID)
		if err != nil || pending || view.Status != "cancelled" || decisions.Load() != 2 {
			t.Fatalf("Cancel() = %#v/%v/%v decisions=%d", view, pending, err, decisions.Load())
		}
	})

	t.Run("running conflicts exhaust original budget without running response", func(t *testing.T) {
		running := controlSnapshot(PlatformGenerationStateRunning, now.UnixMilli())
		running.LeaseOwnerSHA256, running.LeaseUntilMillis = strings.Repeat("a", 64), now.Add(time.Second).UnixMilli()
		fake := newPlatformGenerationControlFake(running)
		fake.cancelOrCreate = func(context.Context, int64, string, int64) (PlatformGenerationCancelDecision, error) {
			fake.cancelCalls.Add(1)
			return PlatformGenerationCancelDecision{Snapshot: running}, ErrPlatformGenerationConflict
		}
		control := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now)
		control.cancelBudget, control.cancelPoll = 4*time.Millisecond, time.Millisecond
		started := time.Now()
		view, pending, err := control.Cancel(context.Background(), 7, controlGenerationID)
		if err != ErrPlatformGenerationControlUnavailable || pending || view.Status == "running" || time.Since(started) > 20*time.Millisecond || fake.cancelCalls.Load() < 2 {
			t.Fatalf("Cancel() = %#v/%v/%v duration=%s calls=%d", view, pending, err, time.Since(started), fake.cancelCalls.Load())
		}
	})

	t.Run("destructive notfound retry keeps deadline", func(t *testing.T) {
		cancelling := controlSnapshot(PlatformGenerationStateCancelling, now.UnixMilli())
		fake := newPlatformGenerationControlFake(cancelling)
		fake.get = func(context.Context, int64, string) (PlatformGenerationSnapshot, error) {
			return PlatformGenerationSnapshot{}, ErrPlatformGenerationNotFound
		}
		control := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now)
		control.cancelBudget, control.cancelPoll = 4*time.Millisecond, time.Millisecond
		started := time.Now()
		_, _, err := control.Cancel(context.Background(), 7, controlGenerationID)
		if err != ErrPlatformGenerationControlUnavailable || time.Since(started) > 20*time.Millisecond {
			t.Fatalf("error/duration = %v/%s", err, time.Since(started))
		}
	})
}

func TestPlatformGenerationControlCancelDependencyErrorNeverLeaks(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	snapshot := controlSnapshot(PlatformGenerationStateRunning, now.UnixMilli())
	fake := newPlatformGenerationControlFake(snapshot)
	fake.cancelOrCreate = func(context.Context, int64, string, int64) (PlatformGenerationCancelDecision, error) {
		return PlatformGenerationCancelDecision{Snapshot: snapshot}, errors.New("redis://:password@private")
	}
	_, _, err := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now).Cancel(context.Background(), 7, controlGenerationID)
	if err != ErrPlatformGenerationControlUnavailable || strings.Contains(err.Error(), "password") {
		t.Fatalf("Cancel() error = %v", err)
	}
}

func TestPlatformGenerationControlCancelInitialDestructiveNotFoundRetriesWithinDeadline(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	tombstone := controlTombstone(now.UnixMilli())
	fake := newPlatformGenerationControlFake(tombstone)
	fake.cancelOrCreate = func(context.Context, int64, string, int64) (PlatformGenerationCancelDecision, error) {
		if fake.cancelCalls.Add(1) == 1 {
			return PlatformGenerationCancelDecision{}, ErrPlatformGenerationNotFound
		}
		return PlatformGenerationCancelDecision{Snapshot: tombstone, CreatedTombstone: true}, nil
	}
	control := mustControl(t, fake, NewPlatformGenerationCancellationRegistry(), now)
	control.cancelBudget, control.cancelPoll = 20*time.Millisecond, time.Millisecond
	view, pending, err := control.Cancel(context.Background(), 7, controlGenerationID)
	if err != nil || pending || view.Status != "cancelled" || fake.cancelCalls.Load() != 2 {
		t.Fatalf("Cancel() = %#v/%v/%v calls=%d", view, pending, err, fake.cancelCalls.Load())
	}
}
