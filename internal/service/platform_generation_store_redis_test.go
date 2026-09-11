package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestPlatformGenerationStoreMarkModelFailedOwnedRequiresCurrentLease(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	storeB, _ := openSecondTestPlatformGenerationStore(t)
	ctx := context.Background()
	input := PlatformGenerationClaimInput{UserID: 982001, GenerationID: "92000000-0000-4000-8000-000000000001", Mode: PlatformGenerationModeCompare, Models: []string{"a", "b"}, NowMillis: 1000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claim := claimTestGeneration(t, store, input)
	wrongToken := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))

	assertRejectedWithoutMutation := func(name string, want PlatformGenerationSnapshot, run func() (PlatformGenerationSnapshot, error)) {
		t.Helper()
		rawBefore, err := client.Get(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		authoritative, err := run()
		if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, want) {
			t.Fatalf("%s authority=%#v want=%#v error=%v", name, authoritative, want, err)
		}
		rawAfter, readErr := client.Get(ctx, key).Result()
		if readErr != nil || rawAfter != rawBefore {
			t.Fatalf("%s mutated encoded record: before=%q after=%q error=%v", name, rawBefore, rawAfter, readErr)
		}
	}

	assertRejectedWithoutMutation("model outside claim", claim.Snapshot, func() (PlatformGenerationSnapshot, error) {
		return store.MarkModelFailedOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "c", "timeout", 1001)
	})
	assertRejectedWithoutMutation("wrong lease", claim.Snapshot, func() (PlatformGenerationSnapshot, error) {
		return storeB.MarkModelFailedOwned(ctx, input.UserID, input.GenerationID, wrongToken, "a", "timeout", 1001)
	})
	assertRejectedWithoutMutation("expired lease boundary", claim.Snapshot, func() (PlatformGenerationSnapshot, error) {
		return storeB.MarkModelFailedOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", "timeout", 31_000)
	})

	cancelling, err := store.RequestCancel(ctx, input.UserID, input.GenerationID, 1002)
	if err != nil {
		t.Fatal(err)
	}
	assertRejectedWithoutMutation("non-running global", cancelling, func() (PlatformGenerationSnapshot, error) {
		return store.MarkModelFailedOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", "timeout", 1003)
	})
}

func TestPlatformGenerationStoreMarkModelFailedOwnedIsTerminalAndRejectsReplay(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	nextCase := 0
	newClaim := func(models []string) (PlatformGenerationClaimInput, PlatformGenerationClaimResult, string) {
		t.Helper()
		nextCase++
		input := PlatformGenerationClaimInput{
			UserID:       int64(982100 + nextCase),
			GenerationID: fmt.Sprintf("92100000-0000-4000-8000-%012x", nextCase),
			Mode:         PlatformGenerationModeSingle,
			Models:       models,
			NowMillis:    1000,
		}
		if len(models) > 1 {
			input.Mode = PlatformGenerationModeCompare
		}
		key := store.key(input.UserID, input.GenerationID)
		preparePlatformGenerationTestKey(t, client, key)
		return input, claimTestGeneration(t, store, input), key
	}
	assertConflictUnchanged := func(name, key string, want PlatformGenerationSnapshot, run func() (PlatformGenerationSnapshot, error)) {
		t.Helper()
		rawBefore, err := client.Get(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		authoritative, err := run()
		if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, want) {
			t.Fatalf("%s authority=%#v want=%#v error=%v", name, authoritative, want, err)
		}
		if rawAfter, readErr := client.Get(ctx, key).Result(); readErr != nil || rawAfter != rawBefore {
			t.Fatalf("%s mutated encoded record: before=%q after=%q error=%v", name, rawBefore, rawAfter, readErr)
		}
	}

	input, claim, key := newClaim([]string{"a"})
	done, err := store.MarkModelDoneOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", 0, 1001)
	if err != nil {
		t.Fatal(err)
	}
	assertConflictUnchanged("done to failed", key, done, func() (PlatformGenerationSnapshot, error) {
		return store.MarkModelFailedOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", "timeout", 1002)
	})

	input, claim, key = newClaim([]string{"a", "b"})
	failed, err := store.MarkModelFailedOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", "timeout", 1001)
	if err != nil {
		t.Fatal(err)
	}
	for _, replay := range []struct {
		name string
		code string
	}{{"same code replay", "timeout"}, {"different code replay", "upstream_error"}} {
		assertConflictUnchanged(replay.name, key, failed, func() (PlatformGenerationSnapshot, error) {
			return store.MarkModelFailedOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", replay.code, 1002)
		})
	}

	tests := []struct {
		name  string
		setup func(PlatformGenerationClaimInput, PlatformGenerationClaimResult) (PlatformGenerationSnapshot, error)
	}{
		{"cancelling", func(input PlatformGenerationClaimInput, _ PlatformGenerationClaimResult) (PlatformGenerationSnapshot, error) {
			return store.RequestCancel(ctx, input.UserID, input.GenerationID, 1001)
		}},
		{"cancelled", func(input PlatformGenerationClaimInput, claim PlatformGenerationClaimResult) (PlatformGenerationSnapshot, error) {
			if _, err := store.RequestCancel(ctx, input.UserID, input.GenerationID, 1001); err != nil {
				return PlatformGenerationSnapshot{}, err
			}
			return store.AcknowledgeCancelledOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, 1002)
		}},
		{"committing", func(input PlatformGenerationClaimInput, claim PlatformGenerationClaimResult) (PlatformGenerationSnapshot, error) {
			if _, err := store.MarkModelDoneOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", 0, 1001); err != nil {
				return PlatformGenerationSnapshot{}, err
			}
			return store.BeginCommitOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, 1002)
		}},
		{"completed", func(input PlatformGenerationClaimInput, claim PlatformGenerationClaimResult) (PlatformGenerationSnapshot, error) {
			if _, err := store.MarkModelDoneOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", 0, 1001); err != nil {
				return PlatformGenerationSnapshot{}, err
			}
			if _, err := store.BeginCommitOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, 1002); err != nil {
				return PlatformGenerationSnapshot{}, err
			}
			return store.Complete(ctx, input.UserID, input.GenerationID, map[string]string{"a": "900000000000000201"}, 1003)
		}},
		{"failed", func(input PlatformGenerationClaimInput, claim PlatformGenerationClaimResult) (PlatformGenerationSnapshot, error) {
			return store.FailRunningOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "internal_error", 1001)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, claim, key := newClaim([]string{"a"})
			terminal, err := test.setup(input, claim)
			if err != nil {
				t.Fatal(err)
			}
			assertConflictUnchanged(test.name, key, terminal, func() (PlatformGenerationSnapshot, error) {
				return store.MarkModelFailedOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "a", "timeout", terminal.UpdatedAtMillis+1)
			})
		})
	}
}

func TestPlatformGenerationStoreMarkModelFailedOwnedLosesToCancelOrCommit(t *testing.T) {
	storeA, clientA := openTestPlatformGenerationStore(t)
	ctx := context.Background()

	input := PlatformGenerationClaimInput{UserID: 982201, GenerationID: "92200000-0000-4000-8000-000000000001", Mode: PlatformGenerationModeCompare, Models: []string{"a", "b"}, NowMillis: 1000}
	key := storeA.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, clientA, key)
	claim := claimTestGeneration(t, storeA, input)

	storeB, clientB := openSecondTestPlatformGenerationStore(t)

	barrier := newPlatformGenerationGETBarrier()
	clientA.AddHook(&platformGenerationGETBarrierHook{key: key, barrier: barrier})
	clientB.AddHook(&platformGenerationGETBarrierHook{key: key, barrier: barrier})
	type outcome struct {
		snapshot PlatformGenerationSnapshot
		err      error
	}
	results := make(chan outcome, 2)
	go func() {
		snapshot, err := storeA.MarkModelFailedOwned(ctx, input.UserID, input.GenerationID, claim.LeaseToken, "b", "timeout", 1001)
		results <- outcome{snapshot: snapshot, err: err}
	}()
	go func() {
		snapshot, err := storeB.RequestCancel(ctx, input.UserID, input.GenerationID, 1001)
		results <- outcome{snapshot: snapshot, err: err}
	}()
	first, second := <-results, <-results
	winners := 0
	for _, result := range []outcome{first, second} {
		if result.err == nil {
			winners++
		} else if !errors.Is(result.err, ErrPlatformGenerationConflict) {
			t.Fatalf("unexpected race result=%#v", result)
		}
	}
	if winners != 1 || !reflect.DeepEqual(first.snapshot, second.snapshot) {
		t.Fatalf("cancel/failure winners=%d first=%#v second=%#v", winners, first, second)
	}
	authoritative, err := storeA.Get(ctx, input.UserID, input.GenerationID)
	if err != nil || !reflect.DeepEqual(authoritative, first.snapshot) || (authoritative.State != PlatformGenerationStateRunning && authoritative.State != PlatformGenerationStateCancelling) {
		t.Fatalf("authoritative=%#v first=%#v error=%v", authoritative, first, err)
	}

	commitInput := PlatformGenerationClaimInput{UserID: 982202, GenerationID: "92200000-0000-4000-8000-000000000002", Mode: PlatformGenerationModeCompare, Models: []string{"a", "b"}, NowMillis: 2000}
	commitKey := storeA.key(commitInput.UserID, commitInput.GenerationID)
	preparePlatformGenerationTestKey(t, clientA, commitKey)
	commitClaim := claimTestGeneration(t, storeA, commitInput)
	if _, err := storeA.MarkModelDoneOwned(ctx, commitInput.UserID, commitInput.GenerationID, commitClaim.LeaseToken, "a", 0, 2001); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.MarkModelFailedOwned(ctx, commitInput.UserID, commitInput.GenerationID, commitClaim.LeaseToken, "b", "timeout", 2002); err != nil {
		t.Fatal(err)
	}
	committing, err := storeA.BeginCommitOwned(ctx, commitInput.UserID, commitInput.GenerationID, commitClaim.LeaseToken, 2003)
	if err != nil {
		t.Fatal(err)
	}
	rawBefore, err := clientA.Get(ctx, commitKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	loser, err := storeB.MarkModelFailedOwned(ctx, commitInput.UserID, commitInput.GenerationID, commitClaim.LeaseToken, "b", "upstream_error", 2004)
	if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(loser, committing) {
		t.Fatalf("failure after commit authority=%#v want=%#v error=%v", loser, committing, err)
	}
	if rawAfter, readErr := clientA.Get(ctx, commitKey).Result(); readErr != nil || rawAfter != rawBefore {
		t.Fatalf("failure after commit mutated encoded record: before=%q after=%q error=%v", rawBefore, rawAfter, readErr)
	}
}

func openSecondTestPlatformGenerationStore(t *testing.T) (*PlatformGenerationStore, *redis.Client) {
	t.Helper()
	options, err := redis.ParseURL(strings.TrimSpace(os.Getenv("TEST_REDIS_URL")))
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewPlatformGenerationStore(client)
	if err != nil {
		t.Fatal(err)
	}
	return store, client
}

type platformGenerationGETBarrier struct {
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func newPlatformGenerationGETBarrier() *platformGenerationGETBarrier {
	return &platformGenerationGETBarrier{release: make(chan struct{})}
}

func (b *platformGenerationGETBarrier) wait() {
	b.mu.Lock()
	b.arrived++
	if b.arrived == 2 {
		close(b.release)
	}
	b.mu.Unlock()
	<-b.release
}

type platformGenerationGETBarrierHook struct {
	key     string
	barrier *platformGenerationGETBarrier
	once    sync.Once
}

func (h *platformGenerationGETBarrierHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *platformGenerationGETBarrierHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err == nil && cmd.Name() == "get" && len(cmd.Args()) == 2 && fmt.Sprint(cmd.Args()[1]) == h.key {
			h.once.Do(h.barrier.wait)
		}
		return err
	}
}

func (h *platformGenerationGETBarrierHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

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
