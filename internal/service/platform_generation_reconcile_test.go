package service

import (
	"context"
	"errors"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

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
	before, _ := f.store.client.PTTL(context.Background(), key).Result()
	snapshot, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, input.NowMillis+1)
	if err != nil || snapshot.State != PlatformGenerationStateCompleted || snapshot.ModelStates["model-a"].AssistantMessageGUID != receipt.Results[0].AssistantMessageGUID {
		t.Fatalf("reconcile=%#v receipt=%#v error=%v", snapshot, receipt, err)
	}
	after, _ := f.store.client.PTTL(context.Background(), key).Result()
	if after > before {
		t.Fatalf("receipt reconciliation refreshed TTL: before=%v after=%v", before, after)
	}
	assertPlatformGenerationRedisMetadataOnly(t, f.store.client, key)
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
	before, _ := f.store.client.PTTL(context.Background(), key).Result()
	snapshot, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, receipt.CommittedAtMillis+1)
	if !errors.Is(err, ErrPlatformGenerationPersistenceIntegrity) || snapshot.State != PlatformGenerationStateCommitting {
		t.Fatalf("invalid receipt reconcile=%#v error=%v", snapshot, err)
	}
	after, _ := f.store.client.PTTL(context.Background(), key).Result()
	if after > before {
		t.Fatalf("integrity failure refreshed TTL: before=%v after=%v", before, after)
	}
}

func TestReconcilePlatformGenerationFailsReceiptlessStaleCommit(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	committing, err := f.store.Get(context.Background(), input.UserID, input.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	key := f.store.key(input.UserID, input.GenerationID)
	before, _ := f.store.client.PTTL(context.Background(), key).Result()
	failed, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds())
	if err != nil || failed.State != PlatformGenerationStateFailed || failed.ErrorCode != "internal_error" || failed.ModelStates["model-a"].AssistantMessageGUID != "" {
		t.Fatalf("stale reconcile=%#v error=%v", failed, err)
	}
	after, _ := f.store.client.PTTL(context.Background(), key).Result()
	if after > before {
		t.Fatalf("stale reconciliation refreshed TTL: before=%v after=%v", before, after)
	}
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
	before, _ := f.store.client.PTTL(context.Background(), key).Result()
	current, err := ReconcilePlatformGeneration(context.Background(), f.db, f.store, input.UserID, input.GenerationID, committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds()-1)
	if !errors.Is(err, ErrPlatformGenerationConflict) || current.State != PlatformGenerationStateCommitting {
		t.Fatalf("fresh reconcile=%#v error=%v", current, err)
	}
	after, _ := f.store.client.PTTL(context.Background(), key).Result()
	if after > before {
		t.Fatalf("fresh reconciliation refreshed TTL: before=%v after=%v", before, after)
	}
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
	store := &PlatformGenerationStore{}
	_, err := ReconcilePlatformGeneration(context.Background(), &gorm.DB{}, store, 1, generationTestID, 1)
	if !errors.Is(err, ErrPlatformGenerationUnavailable) {
		t.Fatalf("error=%v, want unchanged Redis unavailable error", err)
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
