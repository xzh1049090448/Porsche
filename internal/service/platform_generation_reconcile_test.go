package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestReconcilePlatformGenerationReceiptMatcherGuardsCommittingCAS(t *testing.T) {
	committing := PlatformGenerationSnapshot{
		GenerationID: generationTestID,
		Mode:         PlatformGenerationModeCompare,
		Models:       []string{"model-a", "model-b"},
		State:        PlatformGenerationStateCommitting,
		ModelStates: map[string]PlatformGenerationModel{
			"model-a": {State: PlatformGenerationStateCompleted},
			"model-b": {State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
		},
		CreatedAtMillis: 1,
		UpdatedAtMillis: 2,
	}
	receipt := PlatformGenerationReceiptSnapshot{
		UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeCompare,
		ConversationGUID: 101, UserMessage: "secret prompt must not participate",
		SuccessfulModelCount: 1, DailyCallsCharged: 1, TotalTokens: 7, CommittedAtMillis: 3,
		Results: []PlatformGenerationCommittedResult{
			{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "201", Content: "secret answer", Tokens: 7},
			{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
		},
	}
	if !platformGenerationReceiptMatches(committing, receipt, 1, generationTestID) {
		t.Fatal("matching committing receipt was not eligible for reconciliation")
	}

	mutations := []struct {
		name   string
		mutate func(*PlatformGenerationSnapshot, *PlatformGenerationReceiptSnapshot)
	}{
		{name: "mode", mutate: func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.Mode = PlatformGenerationModeSingle
		}},
		{name: "order", mutate: func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.Results[0], r.Results[1] = r.Results[1], r.Results[0]
		}},
		{name: "state", mutate: func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.Results[1] = PlatformGenerationCommittedResult{Model: "model-b", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "202", Content: "other secret", Tokens: 1}
		}},
		{name: "failed code", mutate: func(_ *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			r.Results[1].ErrorCode = "upstream_error"
		}},
		{name: "completed guid", mutate: func(s *PlatformGenerationSnapshot, r *PlatformGenerationReceiptSnapshot) {
			s.State = PlatformGenerationStateCompleted
			model := s.ModelStates["model-a"]
			model.AssistantMessageGUID = "201"
			s.ModelStates["model-a"] = model
			r.Results[0].AssistantMessageGUID = "202"
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			snapshot := clonePlatformGeneration(committing)
			candidate := receipt
			candidate.Results = append([]PlatformGenerationCommittedResult(nil), receipt.Results...)
			test.mutate(&snapshot, &candidate)
			if platformGenerationReceiptMatches(snapshot, candidate, 1, generationTestID) {
				t.Fatal("mismatched receipt was eligible for reconciliation")
			}
		})
	}

	receipt.UserMessage = "different ignored secret prompt"
	if !platformGenerationReceiptMatches(committing, receipt, 1, generationTestID) {
		t.Fatal("matcher must not compare or expose UserMessage")
	}
}

func TestReconcilePlatformGenerationCompletesFromReceipt(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Finalize(context.Background(), f.db, input)
	if err != nil {
		t.Fatal(err)
	}
	key := f.store.key(input.UserID, input.GenerationID)
	before := requirePositivePlatformGenerationTTL(t, f.store.client, key)
	snapshot, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, input.NowMillis+1)
	if err != nil || snapshot.State != PlatformGenerationStateCompleted || snapshot.ModelStates["model-a"].AssistantMessageGUID != receipt.Results[0].AssistantMessageGUID {
		t.Fatalf("reconcile=%#v receipt=%#v error=%v", snapshot, receipt, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, f.store.client, key, before)
	assertPlatformGenerationRedisMetadataOnly(t, f.store.client, key)
}

func TestReconcilePlatformGenerationMismatchDoesNotCompleteRedisIntegration(t *testing.T) {
	requirePlatformGenerationCombinedFixture(t)
	for _, test := range []struct {
		name   string
		mutate func(*PlatformGenerationSnapshot)
	}{
		{name: "ordered models", mutate: func(snapshot *PlatformGenerationSnapshot) {
			snapshot.Models[0], snapshot.Models[1] = snapshot.Models[1], snapshot.Models[0]
		}},
		{name: "mode", mutate: func(snapshot *PlatformGenerationSnapshot) {
			model := snapshot.Models[0]
			state := snapshot.ModelStates[model]
			snapshot.Mode = PlatformGenerationModeSingle
			snapshot.Models = []string{model}
			snapshot.ModelStates = map[string]PlatformGenerationModel{
				model: state,
			}
		}},
		{name: "failed code", mutate: func(snapshot *PlatformGenerationSnapshot) {
			model := snapshot.ModelStates["model-b"]
			model.ErrorCode = "upstream_error"
			snapshot.ModelStates["model-b"] = model
		}},
		{name: "model state", mutate: func(snapshot *PlatformGenerationSnapshot) {
			model := snapshot.ModelStates["model-b"]
			model.State = PlatformGenerationStateCompleted
			model.ErrorCode = ""
			snapshot.ModelStates["model-b"] = model
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := openPlatformGenerationFinalizationFixture(t)
			input := f.committingResults(t, platformCompareGenerationID, PlatformGenerationModeCompare, partialCompareResults())
			committing, err := f.store.Get(context.Background(), input.UserID, input.GenerationID)
			if err != nil {
				t.Fatal(err)
			}
			persistence, err := NewPlatformGenerationPersistence(f.store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := persistence.Finalize(context.Background(), f.db, input); err != nil {
				t.Fatal(err)
			}

			mismatched := clonePlatformGeneration(committing)
			test.mutate(&mismatched)
			mismatchedRaw, err := encodePlatformGeneration(mismatched)
			if err != nil {
				t.Fatalf("encode valid mismatched authority: %v", err)
			}
			if err := f.store.client.Set(context.Background(), f.store.key(input.UserID, input.GenerationID), mismatchedRaw, platformGenerationTTL).Err(); err != nil {
				t.Fatal(err)
			}

			resolved, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, input.NowMillis+1)
			if !errors.Is(err, ErrPlatformGenerationPersistenceIntegrity) || err.Error() != ErrPlatformGenerationPersistenceIntegrity.Error() || resolved.State != PlatformGenerationStateCommitting {
				t.Fatalf("mismatch reconcile snapshot=%#v error=%v", resolved, err)
			}
			authoritative, getErr := f.store.Get(context.Background(), input.UserID, input.GenerationID)
			if getErr != nil || authoritative.State != PlatformGenerationStateCommitting {
				t.Fatalf("Redis mutated after mismatch: snapshot=%#v error=%v", authoritative, getErr)
			}

			control, err := NewPlatformGenerationControl(f.db, f.store, NewPlatformGenerationCancellationRegistry())
			if err != nil {
				t.Fatal(err)
			}
			control.now = func() time.Time { return time.UnixMilli(input.NowMillis + 1).UTC() }
			view, getErr := control.Get(context.Background(), input.UserID, input.GenerationID)
			if !errors.Is(getErr, ErrPlatformGenerationControlUnavailable) {
				t.Fatalf("GET mismatch view=%#v error=%v", view, getErr)
			}
			encoded, marshalErr := json.Marshal(view)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), "content") || strings.Contains(string(encoded), "answer") || strings.Contains(string(encoded), "prompt") {
				t.Fatalf("GET mismatch leaked receipt data: %s", encoded)
			}

			committingRaw, err := encodePlatformGeneration(committing)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.client.Set(context.Background(), f.store.key(input.UserID, input.GenerationID), committingRaw, platformGenerationTTL).Err(); err != nil {
				t.Fatal(err)
			}
			recovered, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, input.NowMillis+1)
			if err != nil || recovered.State != PlatformGenerationStateCompleted {
				t.Fatalf("recovered reconcile snapshot=%#v error=%v", recovered, err)
			}
		})
	}
}

func TestReconcilePlatformGenerationRejectsInvalidUserMessageReceipt(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Finalize(context.Background(), f.db, input)
	if err != nil {
		t.Fatal(err)
	}
	var persisted models.PlatformChatGenerationReceipt
	if err := f.db.Where("user_id = ? AND generation_id = ?", input.UserID, input.GenerationID).First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.Message{}).Where("id = ?", persisted.UserMessageID).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	key := f.store.key(input.UserID, input.GenerationID)
	before := requirePositivePlatformGenerationTTL(t, f.store.client, key)
	snapshot, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, receipt.CommittedAtMillis+1)
	if !errors.Is(err, ErrPlatformGenerationPersistenceIntegrity) || snapshot.State != PlatformGenerationStateCommitting {
		t.Fatalf("invalid receipt reconcile=%#v error=%v", snapshot, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, f.store.client, key, before)
}

func TestReconcilePlatformGenerationFailsReceiptlessStaleCommit(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	committing, err := f.store.Get(context.Background(), input.UserID, input.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	key := f.store.key(input.UserID, input.GenerationID)
	before := requirePositivePlatformGenerationTTL(t, f.store.client, key)
	failed, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds())
	if err != nil || failed.State != PlatformGenerationStateFailed || failed.ErrorCode != "internal_error" || failed.ModelStates["model-a"].AssistantMessageGUID != "" {
		t.Fatalf("stale reconcile=%#v error=%v", failed, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, f.store.client, key, before)
	assertPlatformGenerationRedisMetadataOnly(t, f.store.client, key)
}

func TestReconcilePlatformGenerationDoesNotFailFreshCommit(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	committing, err := f.store.Get(context.Background(), input.UserID, input.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	key := f.store.key(input.UserID, input.GenerationID)
	before := requirePositivePlatformGenerationTTL(t, f.store.client, key)
	current, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds()-1)
	if !errors.Is(err, ErrPlatformGenerationConflict) || current.State != PlatformGenerationStateCommitting {
		t.Fatalf("fresh reconcile=%#v error=%v", current, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, f.store.client, key, before)
}

func TestReconcilePlatformGenerationReturnsResolvedSnapshotOnLockReleaseError(t *testing.T) {
	sentinel := errors.New("simulated advisory lock release failure")
	t.Run("completed receipt", func(t *testing.T) {
		f := openPlatformGenerationFinalizationFixture(t)
		input := f.committingSingle(t)
		p, err := NewPlatformGenerationPersistence(f.store)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := p.Finalize(context.Background(), f.db, input)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := reconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, input.NowMillis+1, func(_ context.Context, _ *gorm.DB, _ string, fn func(*gorm.DB) error) error {
			if err := fn(f.db); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, ErrPlatformGenerationPersistenceUnavailable) || err.Error() != ErrPlatformGenerationPersistenceUnavailable.Error() || snapshot.State != PlatformGenerationStateCompleted || snapshot.ModelStates["model-a"].AssistantMessageGUID != receipt.Results[0].AssistantMessageGUID {
			t.Fatalf("completed release error snapshot=%#v error=%v", snapshot, err)
		}
	})

	t.Run("receiptless stale fail", func(t *testing.T) {
		f := openPlatformGenerationFinalizationFixture(t)
		input := f.committingSingle(t)
		committing, err := f.store.Get(context.Background(), input.UserID, input.GenerationID)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := reconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds(), func(_ context.Context, _ *gorm.DB, _ string, fn func(*gorm.DB) error) error {
			if err := fn(f.db); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, ErrPlatformGenerationPersistenceUnavailable) || err.Error() != ErrPlatformGenerationPersistenceUnavailable.Error() || snapshot.State != PlatformGenerationStateFailed || snapshot.ErrorCode != "internal_error" {
			t.Fatalf("stale-fail release error snapshot=%#v error=%v", snapshot, err)
		}
	})
}

func TestReconcilePlatformGenerationReturnsCompletedAuthoritativeConflict(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Finalize(context.Background(), f.db, input)
	if err != nil {
		t.Fatal(err)
	}
	competingGUID := stringInt64(testSnowflake.Next())
	if competingGUID == receipt.Results[0].AssistantMessageGUID {
		t.Fatal("fixture generated duplicate competing GUID")
	}

	snapshot, err := reconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, input.NowMillis+1, func(_ context.Context, _ *gorm.DB, _ string, fn func(*gorm.DB) error) error {
		if _, completeErr := f.store.Complete(context.Background(), input.UserID, input.GenerationID, map[string]string{"model-a": competingGUID}, input.NowMillis+1); completeErr != nil {
			return completeErr
		}
		return fn(f.db)
	})
	if !errors.Is(err, ErrPlatformGenerationConflict) || snapshot.State != PlatformGenerationStateCompleted || snapshot.ModelStates["model-a"].AssistantMessageGUID != competingGUID {
		t.Fatalf("authoritative completed conflict=%#v error=%v", snapshot, err)
	}
}

func TestReconcilePlatformGenerationReturnsAuthoritativeStaleFailCASLoser(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	competingGUID := stringInt64(testSnowflake.Next())
	committing, err := f.store.Get(context.Background(), input.UserID, input.GenerationID)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := reconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds(), func(_ context.Context, _ *gorm.DB, _ string, fn func(*gorm.DB) error) error {
		if _, completeErr := f.store.Complete(context.Background(), input.UserID, input.GenerationID, map[string]string{"model-a": competingGUID}, committing.UpdatedAtMillis+1); completeErr != nil {
			return completeErr
		}
		return fn(f.db)
	})
	if !errors.Is(err, ErrPlatformGenerationConflict) || snapshot.State != PlatformGenerationStateCompleted || snapshot.ModelStates["model-a"].AssistantMessageGUID != competingGUID {
		t.Fatalf("authoritative stale-fail loser=%#v error=%v", snapshot, err)
	}
}

func TestReconcilePlatformGenerationReceiptWinsCASRace(t *testing.T) {
	t.Run("stale failure wins before SQL", func(t *testing.T) {
		f := openPlatformGenerationFinalizationFixture(t)
		input := f.committingSingle(t)
		p, err := NewPlatformGenerationPersistence(f.store)
		if err != nil {
			t.Fatal(err)
		}
		prechecked := make(chan struct{})
		allowLock := make(chan struct{})
		p.runLocked = func(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
			close(prechecked)
			<-allowLock
			return withPlatformGenerationAdvisoryLock(ctx, db, lockName, fn)
		}
		finalizeErr := make(chan error, 1)
		go func() {
			_, err := p.Finalize(context.Background(), f.db, input)
			finalizeErr <- err
		}()
		<-prechecked
		committing, err := f.store.Get(context.Background(), input.UserID, input.GenerationID)
		if err != nil {
			t.Fatal(err)
		}
		failed, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds())
		if err != nil || failed.State != PlatformGenerationStateFailed {
			t.Fatalf("stale-first reconcile=%#v error=%v", failed, err)
		}
		close(allowLock)
		if err := <-finalizeErr; !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
			t.Fatalf("Finalize after stale failure error=%v, want conflict", err)
		}
		assertPlatformGenerationFinalizationEffects(t, f, 0, 0, 0, 0, 0, 2, 10)
	})

	t.Run("receipt commits under lock first", func(t *testing.T) {
		f := openPlatformGenerationFinalizationFixture(t)
		input := f.committingSingle(t)
		p, err := NewPlatformGenerationPersistence(f.store)
		if err != nil {
			t.Fatal(err)
		}
		lockHeld := make(chan struct{})
		allowCommit := make(chan struct{})
		p.runLocked = func(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
			return withPlatformGenerationAdvisoryLock(ctx, db, lockName, func(conn *gorm.DB) error {
				close(lockHeld)
				<-allowCommit
				return fn(conn)
			})
		}
		finalizeErr := make(chan error, 1)
		go func() {
			_, err := p.Finalize(context.Background(), f.db, input)
			finalizeErr <- err
		}()
		<-lockHeld
		type reconcileResult struct {
			snapshot PlatformGenerationSnapshot
			err      error
		}
		reconciled := make(chan reconcileResult, 1)
		lockAttempted := make(chan struct{})
		go func() {
			snapshot, err := reconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, input.NowMillis+platformGenerationConvergenceWindow.Milliseconds(), func(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
				close(lockAttempted)
				return withPlatformGenerationAdvisoryLock(ctx, db, lockName, fn)
			})
			reconciled <- reconcileResult{snapshot: snapshot, err: err}
		}()
		<-lockAttempted
		close(allowCommit)
		if err := <-finalizeErr; err != nil {
			t.Fatalf("Finalize under lock: %v", err)
		}
		result := <-reconciled
		if result.err != nil || result.snapshot.State != PlatformGenerationStateCompleted || result.snapshot.ModelStates["model-a"].AssistantMessageGUID == "" {
			t.Fatalf("receipt-first reconcile=%#v error=%v", result.snapshot, result.err)
		}
		assertPlatformGenerationFinalizationEffects(t, f, 1, 2, 1, 1, 1, 3, 17)
	})
}

func TestReconcilePlatformGenerationRedisUnavailableDoesNotReplayMySQL(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewPlatformGenerationStore(client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReconcilePlatformGeneration(context.Background(), &gorm.DB{}, store, 1, generationTestID, 1)
	if !errors.Is(err, ErrPlatformGenerationPersistenceUnavailable) || err.Error() != ErrPlatformGenerationPersistenceUnavailable.Error() || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error=%q, want stable persistence unavailable without Redis address", err)
	}
}

func TestReconcilePlatformGenerationNormalizesLockedRedisCASErrors(t *testing.T) {
	for _, test := range []struct {
		name        string
		seedReceipt bool
	}{
		{name: "complete", seedReceipt: true},
		{name: "fail stale", seedReceipt: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := openPlatformGenerationFinalizationFixture(t)
			input := f.committingSingle(t)
			if test.seedReceipt {
				p, err := NewPlatformGenerationPersistence(f.store)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.Finalize(context.Background(), f.db, input); err != nil {
					t.Fatal(err)
				}
			}
			current, err := f.store.Get(context.Background(), input.UserID, input.GenerationID)
			if err != nil {
				t.Fatal(err)
			}
			nowMillis := current.UpdatedAtMillis + 1
			if !test.seedReceipt {
				nowMillis = current.UpdatedAtMillis + platformGenerationConvergenceWindow.Milliseconds()
			}

			liveClient := f.store.client
			snapshot, err := reconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, nowMillis, func(_ context.Context, _ *gorm.DB, _ string, fn func(*gorm.DB) error) error {
				unreachable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
				f.store.client = unreachable
				defer func() {
					f.store.client = liveClient
					_ = unreachable.Close()
				}()
				return fn(f.db)
			})
			if snapshot.GenerationID != input.GenerationID || snapshot.State != PlatformGenerationStateCommitting {
				t.Fatalf("snapshot=%#v, want unchanged pre-lock committing snapshot", snapshot)
			}
			if !errors.Is(err, ErrPlatformGenerationPersistenceUnavailable) || err.Error() != ErrPlatformGenerationPersistenceUnavailable.Error() || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "redis") {
				t.Fatalf("error=%q, want stable persistence unavailable without dependency detail", err)
			}
		})
	}
}

func TestReconcilePlatformGenerationRejectsInvalidInputBeforeDependencies(t *testing.T) {
	for _, test := range []struct {
		name         string
		ctx          context.Context
		db           *gorm.DB
		store        *PlatformGenerationStore
		userID       int64
		generationID string
		nowMillis    int64
	}{
		{"nil context", nil, &gorm.DB{}, &PlatformGenerationStore{}, 1, generationTestID, 1},
		{"nil database", context.Background(), nil, &PlatformGenerationStore{}, 1, generationTestID, 1},
		{"nil store", context.Background(), &gorm.DB{}, nil, 1, generationTestID, 1},
		{"invalid identity", context.Background(), &gorm.DB{}, &PlatformGenerationStore{}, 0, generationTestID, 1},
		{"zero time", context.Background(), &gorm.DB{}, &PlatformGenerationStore{}, 1, generationTestID, 0},
		{"unsafe time", context.Background(), &gorm.DB{}, &PlatformGenerationStore{}, 1, generationTestID, platformSSEV2MaxSafeInteger + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ReconcilePlatformGeneration(test.ctx, test.db, test.store, test.userID, test.generationID, test.nowMillis); !errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
				t.Fatalf("error=%v, want persistence invalid", err)
			}
		})
	}
}
