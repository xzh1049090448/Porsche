package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestPlatformGenerationOwnedMutationsRequireCapabilityAndKeepTTL(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	input := PlatformGenerationClaimInput{UserID: 980001, GenerationID: "90000000-0000-4000-8000-000000000001", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claim := claimTestGeneration(t, store, input)
	beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)

	recorded, err := store.RecordDeltaOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", 1, 1001)
	if err != nil || recorded.ModelStates["a"].Seq != 1 || recorded.LeaseOwnerSHA256 != platformGenerationLeaseDigest(claim.LeaseToken) {
		t.Fatalf("recorded=%#v error=%v", recorded, err)
	}
	afterRecordTTL := requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)

	wrongToken := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	rawBeforeWrongToken, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	wrongOperations := []struct {
		name string
		run  func() (PlatformGenerationSnapshot, error)
	}{
		{"record delta", func() (PlatformGenerationSnapshot, error) {
			return store.RecordDeltaOwned(ctx, input.UserID, input.GenerationID, wrongToken, "a", 2, 1002)
		}},
		{"mark model done", func() (PlatformGenerationSnapshot, error) {
			return store.MarkModelDoneOwned(ctx, input.UserID, input.GenerationID, wrongToken, "a", 1, 1002)
		}},
		{"begin commit", func() (PlatformGenerationSnapshot, error) {
			return store.BeginCommitOwned(ctx, input.UserID, input.GenerationID, wrongToken, 1002)
		}},
		{"fail running", func() (PlatformGenerationSnapshot, error) {
			return store.FailRunningOwned(ctx, input.UserID, input.GenerationID, wrongToken, "internal_error", 1002)
		}},
	}
	afterWrongTTL := afterRecordTTL
	for _, operation := range wrongOperations {
		authoritative, operationErr := operation.run()
		if !errors.Is(operationErr, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, recorded) {
			t.Fatalf("%s wrong-token authority=%#v error=%v", operation.name, authoritative, operationErr)
		}
		rawAfterWrongToken, readErr := client.Get(ctx, key).Result()
		if readErr != nil || rawAfterWrongToken != rawBeforeWrongToken {
			t.Fatalf("%s wrong-token mutation raw=%q before=%q error=%v", operation.name, rawAfterWrongToken, rawBeforeWrongToken, readErr)
		}
		afterWrongTTL = requirePlatformGenerationTTLNotIncreased(t, client, key, afterWrongTTL)
	}

	done, err := store.MarkModelDoneOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", 1, 1003)
	if err != nil || done.ModelStates["a"].State != PlatformGenerationStateCompleted {
		t.Fatalf("done=%#v error=%v", done, err)
	}
	committing, err := store.BeginCommitOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, 1004)
	if err != nil || committing.State != PlatformGenerationStateCommitting || committing.LeaseOwnerSHA256 != "" || committing.LeaseUntilMillis != 0 {
		t.Fatalf("committing=%#v error=%v", committing, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, afterWrongTTL)
}

func TestPlatformGenerationFailRunningOwnedFailsOnlyRunningModels(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	input := PlatformGenerationClaimInput{UserID: 980002, GenerationID: "90000000-0000-4000-8000-000000000002", Mode: PlatformGenerationModeCompare, Models: []string{"done", "running"}, NowMillis: 1000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claim := claimTestGeneration(t, store, input)
	if _, err := store.MarkModelDoneOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "done", 0, 1001); err != nil {
		t.Fatal(err)
	}
	beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)

	failed, err := store.FailRunningOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "internal_error", 1002)
	if err != nil || failed.State != PlatformGenerationStateFailed || failed.ErrorCode != "internal_error" {
		t.Fatalf("failed=%#v error=%v", failed, err)
	}
	if failed.ModelStates["done"].State != PlatformGenerationStateCompleted || failed.ModelStates["running"].State != PlatformGenerationStateFailed || failed.ModelStates["running"].ErrorCode != "internal_error" {
		t.Fatalf("model states=%#v", failed.ModelStates)
	}
	if failed.LeaseOwnerSHA256 != "" || failed.LeaseUntilMillis != 0 {
		t.Fatalf("terminal lease retained: %#v", failed)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
}

func TestPlatformGenerationRunningOwnedMutationsRejectExpiredLeaseWithoutMutation(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	operations := []struct {
		name    string
		prepare func(*PlatformGenerationStore, PlatformGenerationClaimInput, string) error
		run     func(*PlatformGenerationStore, PlatformGenerationClaimInput, string, int64) (PlatformGenerationSnapshot, error)
	}{
		{
			name:    "record delta",
			prepare: func(*PlatformGenerationStore, PlatformGenerationClaimInput, string) error { return nil },
			run: func(store *PlatformGenerationStore, input PlatformGenerationClaimInput, token string, nowMillis int64) (PlatformGenerationSnapshot, error) {
				return store.RecordDeltaOwned(ctx, input.UserID, input.GenerationID, token, "a", 1, nowMillis)
			},
		},
		{
			name:    "mark model done",
			prepare: func(*PlatformGenerationStore, PlatformGenerationClaimInput, string) error { return nil },
			run: func(store *PlatformGenerationStore, input PlatformGenerationClaimInput, token string, nowMillis int64) (PlatformGenerationSnapshot, error) {
				return store.MarkModelDoneOwned(ctx, input.UserID, input.GenerationID, token, "a", 0, nowMillis)
			},
		},
		{
			name: "begin commit",
			prepare: func(store *PlatformGenerationStore, input PlatformGenerationClaimInput, token string) error {
				_, err := store.MarkModelDoneOwned(ctx, input.UserID, input.GenerationID, token, "a", 0, 1001)
				return err
			},
			run: func(store *PlatformGenerationStore, input PlatformGenerationClaimInput, token string, nowMillis int64) (PlatformGenerationSnapshot, error) {
				return store.BeginCommitOwned(ctx, input.UserID, input.GenerationID, token, nowMillis)
			},
		},
		{
			name:    "fail running",
			prepare: func(*PlatformGenerationStore, PlatformGenerationClaimInput, string) error { return nil },
			run: func(store *PlatformGenerationStore, input PlatformGenerationClaimInput, token string, nowMillis int64) (PlatformGenerationSnapshot, error) {
				return store.FailRunningOwned(ctx, input.UserID, input.GenerationID, token, "internal_error", nowMillis)
			},
		},
	}
	caseID := 0
	for _, nowMillis := range []int64{31_000, 31_001} {
		for _, operation := range operations {
			caseID++
			t.Run(fmt.Sprintf("%s_at_%d", operation.name, nowMillis), func(t *testing.T) {
				generationID := fmt.Sprintf("90500000-0000-4000-8000-%012x", caseID)
				input := PlatformGenerationClaimInput{UserID: int64(980500 + caseID), GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
				key := store.key(input.UserID, input.GenerationID)
				preparePlatformGenerationTestKey(t, client, key)
				claim := claimTestGeneration(t, store, input)
				if err := operation.prepare(store, input, claim.LeaseToken); err != nil {
					t.Fatal(err)
				}
				before, err := store.Get(ctx, input.UserID, input.GenerationID)
				if err != nil {
					t.Fatal(err)
				}
				rawBefore, err := client.Get(ctx, key).Result()
				if err != nil {
					t.Fatal(err)
				}
				ttlBefore := requirePositivePlatformGenerationTTL(t, client, key)
				authoritative, err := operation.run(store, input, claim.LeaseToken, nowMillis)
				if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, before) {
					t.Fatalf("authority=%#v before=%#v error=%v", authoritative, before, err)
				}
				if rawAfter, readErr := client.Get(ctx, key).Result(); readErr != nil || rawAfter != rawBefore {
					t.Fatalf("expired operation mutated raw=%q before=%q error=%v", rawAfter, rawBefore, readErr)
				}
				requirePlatformGenerationTTLNotIncreased(t, client, key, ttlBefore)
			})
		}
	}
}

func TestPlatformGenerationOwnerExpiredMutationRaceWithFailExpiredReturnsLegalAuthority(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	for iteration := 0; iteration < 24; iteration++ {
		generationID := fmt.Sprintf("90600000-0000-4000-8000-%012x", iteration+1)
		userID := int64(980600 + iteration)
		key := store.key(userID, generationID)
		preparePlatformGenerationTestKey(t, client, key)
		claim := claimTestGeneration(t, store, PlatformGenerationClaimInput{UserID: userID, GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000})
		beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)
		start := make(chan struct{})
		type outcome struct {
			snapshot PlatformGenerationSnapshot
			err      error
		}
		results := make(chan outcome, 2)
		go func() {
			<-start
			snapshot, err := store.RecordDeltaOwned(ctx, userID, generationID, claim.LeaseToken, "a", 1, 31_000)
			results <- outcome{snapshot, err}
		}()
		go func() {
			<-start
			snapshot, err := store.FailExpiredRunning(ctx, userID, generationID, 31_000)
			results <- outcome{snapshot, err}
		}()
		close(start)
		first, second := <-results, <-results
		final, err := store.Get(ctx, userID, generationID)
		if err != nil || final.State != PlatformGenerationStateFailed || final.LeaseOwnerSHA256 != "" || final.LeaseUntilMillis != 0 {
			t.Fatalf("iteration %d final=%#v error=%v", iteration, final, err)
		}
		for _, result := range []outcome{first, second} {
			if result.err != nil && !errors.Is(result.err, ErrPlatformGenerationConflict) {
				t.Fatalf("iteration %d result=%#v", iteration, result)
			}
			if result.snapshot.State != PlatformGenerationStateRunning && result.snapshot.State != PlatformGenerationStateFailed {
				t.Fatalf("iteration %d illegal authority=%#v", iteration, result)
			}
		}
		requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
	}
}

func TestPlatformGenerationAcknowledgeCancelledOwnedPreservesAuthority(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	input := PlatformGenerationClaimInput{UserID: 980003, GenerationID: "90000000-0000-4000-8000-000000000003", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claim := claimTestGeneration(t, store, input)
	digest := platformGenerationLeaseDigest(claim.LeaseToken)
	decision, err := store.CancelOrCreate(ctx, input.UserID, input.GenerationID, 1001)
	if err != nil || !decision.Transitioned || decision.Snapshot.State != PlatformGenerationStateCancelling || decision.Snapshot.LeaseOwnerSHA256 != digest || decision.Snapshot.LeaseUntilMillis != 0 {
		t.Fatalf("cancel decision=%#v error=%v", decision, err)
	}
	beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)
	if renewed, err := store.RenewLease(ctx, input.UserID, input.GenerationID, claim.LeaseToken, 1002); !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(renewed, decision.Snapshot) {
		t.Fatalf("renewed=%#v error=%v", renewed, err)
	}

	wrongToken := base64.RawURLEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyzABCDEF"))
	authoritative, err := store.AcknowledgeCancelledOwned(ctx, input.UserID, input.GenerationID, wrongToken, 1002)
	if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, decision.Snapshot) {
		t.Fatalf("wrong-token authority=%#v error=%v", authoritative, err)
	}
	cancelled, err := store.AcknowledgeCancelledOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, 31_001)
	if err != nil || cancelled.State != PlatformGenerationStateCancelled || cancelled.ModelStates["a"].State != PlatformGenerationStateCancelled || cancelled.LeaseOwnerSHA256 != "" || cancelled.LeaseUntilMillis != 0 {
		t.Fatalf("cancelled=%#v error=%v", cancelled, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
}

func TestPlatformGenerationAcknowledgeCancelledOwnedCannotOverwriteTerminalOrCommitting(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	for index, target := range []PlatformGenerationState{PlatformGenerationStateCommitting, PlatformGenerationStateCompleted, PlatformGenerationStateFailed} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			generationID := fmt.Sprintf("90000000-0000-4000-8000-%012x", 100+index)
			userID := int64(980100 + index)
			key := store.key(userID, generationID)
			preparePlatformGenerationTestKey(t, client, key)
			claim := claimTestGeneration(t, store, PlatformGenerationClaimInput{UserID: userID, GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000})
			if target == PlatformGenerationStateFailed {
				if _, err := store.FailRunningOwned(ctx, userID, generationID, claim.LeaseToken, "internal_error", 1001); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := store.MarkModelDoneOwned(ctx, userID, generationID, claim.LeaseToken, "a", 0, 1001); err != nil {
					t.Fatal(err)
				}
				if _, err := store.BeginCommitOwned(ctx, userID, generationID, claim.LeaseToken, 1002); err != nil {
					t.Fatal(err)
				}
				if target == PlatformGenerationStateCompleted {
					if _, err := store.Complete(ctx, userID, generationID, map[string]string{"a": "900000000000000100"}, 1003); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := store.Get(ctx, userID, generationID)
			if err != nil {
				t.Fatal(err)
			}
			authoritative, err := store.AcknowledgeCancelledOwned(ctx, userID, generationID, claim.LeaseToken, 1004)
			if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, before) {
				t.Fatalf("target=%v authority=%#v before=%#v error=%v", target, authoritative, before, err)
			}
		})
	}
}

func TestPlatformGenerationOwnerBeginCommitRaceWithCancelReturnsAuthority(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	for iteration := 0; iteration < 24; iteration++ {
		generationID := fmt.Sprintf("91000000-0000-4000-8000-%012x", iteration+1)
		userID := int64(981000 + iteration)
		key := store.key(userID, generationID)
		preparePlatformGenerationTestKey(t, client, key)
		claim := claimTestGeneration(t, store, PlatformGenerationClaimInput{UserID: userID, GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000})
		if _, err := store.MarkModelDoneOwned(ctx, userID, generationID, claim.LeaseToken, "a", 0, 1001); err != nil {
			t.Fatal(err)
		}
		beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)
		start := make(chan struct{})
		type outcome struct {
			snapshot PlatformGenerationSnapshot
			err      error
		}
		results := make(chan outcome, 2)
		go func() {
			<-start
			snapshot, err := store.BeginCommitOwned(ctx, userID, generationID, claim.LeaseToken, 1002)
			results <- outcome{snapshot, err}
		}()
		go func() {
			<-start
			decision, err := store.CancelOrCreate(ctx, userID, generationID, 1002)
			results <- outcome{decision.Snapshot, err}
		}()
		close(start)
		first, second := <-results, <-results
		final, err := store.Get(ctx, userID, generationID)
		if err != nil || (final.State != PlatformGenerationStateCommitting && final.State != PlatformGenerationStateCancelling) {
			t.Fatalf("iteration %d final=%#v error=%v", iteration, final, err)
		}
		for _, result := range []outcome{first, second} {
			if !reflect.DeepEqual(result.snapshot, final) || (result.err != nil && !errors.Is(result.err, ErrPlatformGenerationConflict)) {
				t.Fatalf("iteration %d result=%#v final=%#v", iteration, result, final)
			}
		}
		if final.State == PlatformGenerationStateCancelling && (final.LeaseOwnerSHA256 != platformGenerationLeaseDigest(claim.LeaseToken) || final.LeaseUntilMillis != 0) {
			t.Fatalf("iteration %d cancelling authority=%#v", iteration, final)
		}
		requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
	}
}
