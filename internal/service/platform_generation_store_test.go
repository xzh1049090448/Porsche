package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const generationTestID = "550e8400-e29b-41d4-a716-446655440000"

func TestPlatformGenerationStoreRejectsInvalidInputBeforeRedis(t *testing.T) {
	if _, err := NewPlatformGenerationStore(nil); err == nil {
		t.Fatal("nil Redis client was accepted")
	}
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	longModel := strings.Repeat("m", platformSSEV2MaxIdentifierBytes+1)
	for _, input := range []PlatformGenerationClaimInput{
		{UserID: 0, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"model-a"}},
		{UserID: 1, GenerationID: "bad", Mode: PlatformGenerationModeSingle, Models: []string{"model-a"}},
		{UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: nil},
		{UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a", "b"}},
		{UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeCompare, Models: []string{"a"}},
		{UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeCompare, Models: []string{"a", "a"}},
		{UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeCompare, Models: []string{"a", "b", "c", "d"}},
		{UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeCompare, Models: []string{"a", longModel}},
	} {
		if _, err := store.Claim(context.Background(), input); !errors.Is(err, ErrPlatformGenerationInvalid) {
			t.Fatalf("Claim(%#v) error=%v, want invalid", input, err)
		}
	}
}

func TestPlatformGenerationStoreRejectsModelOverPersistenceLimitBeforeRedis(t *testing.T) {
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Claim(context.Background(), PlatformGenerationClaimInput{
		UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle,
		Models: []string{strings.Repeat("m", 129)}, NowMillis: 1,
	})
	if !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("Claim() error=%v, want invalid before Redis", err)
	}
}

func TestPlatformGenerationStoreRejectsLeaseDeadlinePastSafeIntegerBeforeDependencies(t *testing.T) {
	maxNow := platformSSEV2MaxSafeInteger - platformGenerationLeaseDuration.Milliseconds()
	valid := PlatformGenerationClaimInput{UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: maxNow}
	if err := validatePlatformGenerationInput(valid); err != nil {
		t.Fatalf("max lease-safe input error=%v", err)
	}
	invalid := valid
	invalid.NowMillis++
	if err := validatePlatformGenerationInput(invalid); err != nil {
		t.Fatalf("stored-record input validation changed: %v", err)
	}
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(context.Background(), invalid); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("Claim unsafe lease input error=%v, want invalid before dependency", err)
	}
}

func TestPlatformGenerationStoreRejectsInvalidUTF8ModelBeforeRedis(t *testing.T) {
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Claim(context.Background(), PlatformGenerationClaimInput{
		UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle,
		Models: []string{string([]byte{0xff})}, NowMillis: 1,
	})
	if !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("Claim() error=%v, want invalid before Redis", err)
	}
}

func TestPlatformGenerationStoreKeyOnlyUsesInternalUserIDAndGenerationID(t *testing.T) {
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	key := store.key(42, generationTestID)
	if key != "porsche:platform:generation:v2:42:"+generationTestID || strings.Contains(key, "prompt") || strings.Contains(key, "Authorization") {
		t.Fatalf("unsafe key %q", key)
	}
}

func openTestPlatformGenerationStore(t *testing.T) (*PlatformGenerationStore, *redis.Client) {
	t.Helper()
	rawURL := strings.TrimSpace(os.Getenv("TEST_REDIS_URL"))
	if rawURL == "" {
		t.Skip("BLOCKED_FIXTURE: requires TEST_REDIS_URL")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	store, err := NewPlatformGenerationStore(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAvailable(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, client
}

func claimTestGeneration(t *testing.T, store *PlatformGenerationStore, input PlatformGenerationClaimInput) PlatformGenerationClaimResult {
	t.Helper()
	result, err := store.Claim(context.Background(), input)
	if err != nil || result.Duplicate || result.Snapshot.GenerationID != input.GenerationID || result.LeaseToken == "" {
		t.Fatalf("claim=%#v error=%v", result, err)
	}
	return result
}

func preparePlatformGenerationTestKey(t *testing.T, client redis.Cmdable, key string) {
	t.Helper()
	if err := client.Del(context.Background(), key).Err(); err != nil {
		t.Fatalf("delete initial generation key %q: %v", key, err)
	}
	t.Cleanup(func() {
		if err := client.Del(context.Background(), key).Err(); err != nil {
			t.Errorf("delete generation key %q during cleanup: %v", key, err)
		}
	})
}

func requirePositivePlatformGenerationTTL(t *testing.T, client redis.Cmdable, key string) time.Duration {
	t.Helper()
	ttl, err := client.PTTL(context.Background(), key).Result()
	if err != nil || ttl <= 0 {
		t.Fatalf("generation TTL=%v error=%v, want positive expiring key", ttl, err)
	}
	return ttl
}

func requirePlatformGenerationTTLNotIncreased(t *testing.T, client redis.Cmdable, key string, prior time.Duration) time.Duration {
	t.Helper()
	if prior <= 0 {
		t.Fatalf("prior generation TTL=%v, want positive", prior)
	}
	current := requirePositivePlatformGenerationTTL(t, client, key)
	if current > prior {
		t.Fatalf("generation TTL refreshed: before=%v after=%v", prior, current)
	}
	return current
}

func TestPlatformGenerationStoreClaimIsAtomicAndKeepsOriginalTTL(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910001, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"model-a"}, NowMillis: 1000}
	_ = client.Del(context.Background(), store.key(input.UserID, input.GenerationID)).Err()
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.Claim(context.Background(), input)
			if (result.Duplicate && !errors.Is(err, ErrPlatformGenerationConflict)) || (!result.Duplicate && err != nil) || result.Snapshot.CreatedAtMillis != input.NowMillis || (!result.Duplicate && result.LeaseToken == "") || (result.Duplicate && result.LeaseToken != "") {
				t.Errorf("claim result=%#v error=%v", result, err)
				return
			}
			results <- result.Duplicate
		}()
	}
	wg.Wait()
	close(results)
	duplicates := 0
	for duplicate := range results {
		if duplicate {
			duplicates++
		}
	}
	if duplicates != 7 {
		t.Fatalf("duplicates=%d, want 7", duplicates)
	}
	before, err := client.PTTL(context.Background(), store.key(input.UserID, input.GenerationID)).Result()
	if err != nil || before <= 23*time.Hour || before > platformGenerationTTL {
		t.Fatalf("initial TTL=%v error=%v", before, err)
	}
	input.NowMillis = 999999
	if result, err := store.Claim(context.Background(), input); !errors.Is(err, ErrPlatformGenerationConflict) || !result.Duplicate || result.Snapshot.CreatedAtMillis != 1000 || result.LeaseToken != "" {
		t.Fatalf("duplicate result=%#v error=%v", result, err)
	}
	after, err := client.PTTL(context.Background(), store.key(input.UserID, input.GenerationID)).Result()
	if err != nil || after > before {
		t.Fatalf("duplicate refreshed TTL: before=%v after=%v error=%v", before, after, err)
	}
}

func TestPlatformGenerationStoreMissingRecordIsTypedNotFound(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	userID := int64(910099)
	generationID := "640e8400-e29b-41d4-a716-446655440000"
	_ = client.Del(context.Background(), store.key(userID, generationID)).Err()
	if _, err := store.Get(context.Background(), userID, generationID); !errors.Is(err, ErrPlatformGenerationNotFound) {
		t.Fatalf("Get missing error=%v, want not found", err)
	}
	if _, err := store.RequestCancel(context.Background(), userID, generationID, 1); !errors.Is(err, ErrPlatformGenerationNotFound) {
		t.Fatalf("mutate missing error=%v, want not found", err)
	}
}

func TestPlatformGenerationStoreSingleCompletesWithExactMessageGUID(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910002, GenerationID: "650e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeSingle, Models: []string{"model-a"}, NowMillis: 1000}
	_ = client.Del(context.Background(), store.key(input.UserID, input.GenerationID)).Err()
	claimTestGeneration(t, store, input)
	if _, err := store.RecordDelta(context.Background(), input.UserID, input.GenerationID, "model-a", 2, 1001); !errors.Is(err, ErrPlatformGenerationConflict) {
		t.Fatalf("out-of-order delta error=%v", err)
	}
	if snapshot, err := store.RecordDelta(context.Background(), input.UserID, input.GenerationID, "model-a", 1, 1002); err != nil || snapshot.ModelStates["model-a"].Seq != 1 {
		t.Fatalf("delta snapshot=%#v error=%v", snapshot, err)
	}
	if _, err := store.BeginCommit(context.Background(), input.UserID, input.GenerationID, 1003); !errors.Is(err, ErrPlatformGenerationConflict) {
		t.Fatalf("commit before model_done error=%v", err)
	}
	if _, err := store.MarkModelDone(context.Background(), input.UserID, input.GenerationID, "model-a", 0, 1004); !errors.Is(err, ErrPlatformGenerationConflict) {
		t.Fatalf("wrong last_seq error=%v", err)
	}
	if _, err := store.MarkModelDone(context.Background(), input.UserID, input.GenerationID, "model-a", 1, 1005); err != nil {
		t.Fatal(err)
	}
	before, _ := client.PTTL(context.Background(), store.key(input.UserID, input.GenerationID)).Result()
	if _, err := store.BeginCommit(context.Background(), input.UserID, input.GenerationID, 1006); err != nil {
		t.Fatal(err)
	}
	completed, err := store.Complete(context.Background(), input.UserID, input.GenerationID, map[string]string{"model-a": "900000000000000001"}, 1007)
	if err != nil || completed.State != PlatformGenerationStateCompleted || completed.ModelStates["model-a"].AssistantMessageGUID != "900000000000000001" {
		t.Fatalf("complete=%#v error=%v", completed, err)
	}
	after, _ := client.PTTL(context.Background(), store.key(input.UserID, input.GenerationID)).Result()
	if after > before {
		t.Fatalf("transitions refreshed TTL: before=%v after=%v", before, after)
	}
}

func TestPlatformGenerationStoreCompareSupportsPerModelSuccessAndFailure(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910003, GenerationID: "660e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeCompare, Models: []string{"a", "b"}, NowMillis: 2000}
	_ = client.Del(context.Background(), store.key(input.UserID, input.GenerationID)).Err()
	claimTestGeneration(t, store, input)
	if _, err := store.RecordDelta(context.Background(), input.UserID, input.GenerationID, "a", 1, 2001); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkModelDone(context.Background(), input.UserID, input.GenerationID, "a", 1, 2002); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkModelFailed(context.Background(), input.UserID, input.GenerationID, "b", "timeout", 2003); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCommit(context.Background(), input.UserID, input.GenerationID, 2004); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(context.Background(), input.UserID, input.GenerationID, map[string]string{"a": "bad-guid"}, 2005); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("invalid GUID error=%v", err)
	}
	if _, err := store.Complete(context.Background(), input.UserID, input.GenerationID, map[string]string{"a": "900000000000000002", "b": "900000000000000003"}, 2005); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("excess GUID map error=%v", err)
	}
	snapshot, err := store.Complete(context.Background(), input.UserID, input.GenerationID, map[string]string{"a": "900000000000000004"}, 2006)
	if err != nil || snapshot.ModelStates["a"].AssistantMessageGUID == "" || snapshot.ModelStates["b"].ErrorCode != "timeout" {
		t.Fatalf("complete=%#v error=%v", snapshot, err)
	}
	raw, err := client.Get(context.Background(), store.key(input.UserID, input.GenerationID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "reply", "authorization", "quota", "cost", "token"} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Fatalf("stored record contains forbidden field %q: %s", forbidden, raw)
		}
	}
}

func TestPlatformGenerationStoreCompareRejectsSharedAssistantMessageGUID(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910007, GenerationID: "6a0e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeCompare, Models: []string{"a", "b"}, NowMillis: 2100}
	_ = client.Del(context.Background(), store.key(input.UserID, input.GenerationID)).Err()
	claimTestGeneration(t, store, input)
	for _, model := range input.Models {
		if _, err := store.MarkModelDone(context.Background(), input.UserID, input.GenerationID, model, 0, 2101); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BeginCommit(context.Background(), input.UserID, input.GenerationID, 2102); err != nil {
		t.Fatal(err)
	}
	shared := "900000000000000020"
	if _, err := store.Complete(context.Background(), input.UserID, input.GenerationID, map[string]string{"a": shared, "b": shared}, 2103); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("shared assistant message GUID error=%v, want invalid", err)
	}
}

func TestPlatformGenerationStoreCancelWinsAgainstCommitAndIsTerminal(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910004, GenerationID: "670e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 3000}
	_ = client.Del(context.Background(), store.key(input.UserID, input.GenerationID)).Err()
	claimTestGeneration(t, store, input)
	if snapshot, err := store.RequestCancel(context.Background(), input.UserID, input.GenerationID, 3001); err != nil || snapshot.State != PlatformGenerationStateCancelling {
		t.Fatalf("cancel=%#v error=%v", snapshot, err)
	}
	if snapshot, err := store.BeginCommit(context.Background(), input.UserID, input.GenerationID, 3002); !errors.Is(err, ErrPlatformGenerationConflict) || snapshot.State != PlatformGenerationStateCancelling {
		t.Fatalf("commit after cancel=%#v error=%v", snapshot, err)
	}
	if snapshot, err := store.MarkCancelled(context.Background(), input.UserID, input.GenerationID, 3003); err != nil || snapshot.State != PlatformGenerationStateCancelled {
		t.Fatalf("cancelled=%#v error=%v", snapshot, err)
	}
	if snapshot, err := store.RequestCancel(context.Background(), input.UserID, input.GenerationID, 3004); !errors.Is(err, ErrPlatformGenerationConflict) || snapshot.State != PlatformGenerationStateCancelled {
		t.Fatalf("terminal cancel=%#v error=%v", snapshot, err)
	}
}

func TestPlatformGenerationStoreCommitWinsAgainstCancel(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910005, GenerationID: "680e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 4000}
	_ = client.Del(context.Background(), store.key(input.UserID, input.GenerationID)).Err()
	claimTestGeneration(t, store, input)
	if _, err := store.MarkModelDone(context.Background(), input.UserID, input.GenerationID, "a", 0, 4001); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCommit(context.Background(), input.UserID, input.GenerationID, 4002); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := store.RequestCancel(context.Background(), input.UserID, input.GenerationID, 4003); !errors.Is(err, ErrPlatformGenerationConflict) || snapshot.State != PlatformGenerationStateCommitting {
		t.Fatalf("cancel after commit=%#v error=%v", snapshot, err)
	}
	if _, err := store.Complete(context.Background(), input.UserID, input.GenerationID, map[string]string{"a": "900000000000000004"}, 4004); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := store.RequestCancel(context.Background(), input.UserID, input.GenerationID, 4005); !errors.Is(err, ErrPlatformGenerationConflict) || snapshot.State != PlatformGenerationStateCompleted {
		t.Fatalf("cancel completed=%#v error=%v", snapshot, err)
	}
}

func TestPlatformGenerationStoreCancelAndCommitRaceHasOneWinner(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	for iteration := 0; iteration < 24; iteration++ {
		generationID := fmt.Sprintf("70000000-0000-4000-8000-%012x", iteration+1)
		input := PlatformGenerationClaimInput{UserID: 920000 + int64(iteration), GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 6000}
		_ = client.Del(context.Background(), store.key(input.UserID, input.GenerationID)).Err()
		claimTestGeneration(t, store, input)
		if _, err := store.MarkModelDone(context.Background(), input.UserID, input.GenerationID, "a", 0, 6001); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		type result struct {
			snapshot PlatformGenerationSnapshot
			err      error
		}
		results := make(chan result, 2)
		go func() {
			<-start
			snapshot, err := store.RequestCancel(context.Background(), input.UserID, input.GenerationID, 6002)
			results <- result{snapshot, err}
		}()
		go func() {
			<-start
			snapshot, err := store.BeginCommit(context.Background(), input.UserID, input.GenerationID, 6002)
			results <- result{snapshot, err}
		}()
		close(start)
		first, second := <-results, <-results
		winners := 0
		for _, outcome := range []result{first, second} {
			if outcome.err == nil {
				winners++
			} else if !errors.Is(outcome.err, ErrPlatformGenerationConflict) {
				t.Fatalf("iteration %d unexpected race error: %v", iteration, outcome.err)
			}
		}
		if winners != 1 {
			t.Fatalf("iteration %d winners=%d, results=%#v %#v", iteration, winners, first, second)
		}
		authoritative, err := store.Get(context.Background(), input.UserID, input.GenerationID)
		if err != nil || (authoritative.State != PlatformGenerationStateCancelling && authoritative.State != PlatformGenerationStateCommitting) {
			t.Fatalf("iteration %d authoritative=%#v error=%v", iteration, authoritative, err)
		}
	}
}

func TestPlatformGenerationStoreFailureStoresOnlyStableCode(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910006, GenerationID: "690e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 5000}
	_ = client.Del(context.Background(), store.key(input.UserID, input.GenerationID)).Err()
	claimTestGeneration(t, store, input)
	if _, err := store.Fail(context.Background(), input.UserID, input.GenerationID, "secret upstream URL", 5001); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("unsafe failure code error=%v", err)
	}
	snapshot, err := store.Fail(context.Background(), input.UserID, input.GenerationID, "upstream_error", 5002)
	if err != nil || snapshot.State != PlatformGenerationStateFailed || snapshot.ErrorCode != "upstream_error" {
		t.Fatalf("failure=%#v error=%v", snapshot, err)
	}
}

func TestPlatformGenerationStoreReconcileCompleteIsIdempotent(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910020, GenerationID: "710e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeCompare, Models: []string{"a", "b"}, NowMillis: 1000}
	ctx := context.Background()
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claimTestGeneration(t, store, input)
	if _, err := store.MarkModelDone(ctx, input.UserID, input.GenerationID, "a", 0, 1001); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkModelFailed(ctx, input.UserID, input.GenerationID, "b", "timeout", 1002); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCommit(ctx, input.UserID, input.GenerationID, 1003); err != nil {
		t.Fatal(err)
	}
	before := requirePositivePlatformGenerationTTL(t, client, key)
	guids := map[string]string{"a": "900000000000000101"}
	first, err := store.ReconcileComplete(ctx, input.UserID, input.GenerationID, guids, 1004)
	if err != nil || first.State != PlatformGenerationStateCompleted {
		t.Fatalf("first reconcile=%#v error=%v", first, err)
	}
	firstTTL := requirePlatformGenerationTTLNotIncreased(t, client, key, before)
	second, err := store.ReconcileComplete(ctx, input.UserID, input.GenerationID, guids, 1005)
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("idempotent reconcile=%#v want=%#v error=%v", second, first, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, firstTTL)
	assertPlatformGenerationRedisMetadataOnly(t, client, key)
}

func TestPlatformGenerationStoreReconcileCompleteRejectsDifferentGUIDMap(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910021, GenerationID: "720e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 2000}
	ctx := context.Background()
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claimTestGeneration(t, store, input)
	if _, err := store.MarkModelDone(ctx, input.UserID, input.GenerationID, "a", 0, 2001); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCommit(ctx, input.UserID, input.GenerationID, 2002); err != nil {
		t.Fatal(err)
	}
	original := map[string]string{"a": "900000000000000102"}
	if _, err := store.ReconcileComplete(ctx, input.UserID, input.GenerationID, original, 2003); err != nil {
		t.Fatal(err)
	}
	before := requirePositivePlatformGenerationTTL(t, client, key)
	snapshot, err := store.ReconcileComplete(ctx, input.UserID, input.GenerationID, map[string]string{"a": "900000000000000103"}, 2004)
	if !errors.Is(err, ErrPlatformGenerationConflict) || snapshot.ModelStates["a"].AssistantMessageGUID != original["a"] {
		t.Fatalf("different GUID reconcile=%#v error=%v", snapshot, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, before)
	assertPlatformGenerationRedisMetadataOnly(t, client, key)
}

func TestPlatformGenerationStoreFailStaleCommitRequiresThirtySeconds(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910022, GenerationID: "730e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 3000}
	ctx := context.Background()
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claimTestGeneration(t, store, input)
	if _, err := store.MarkModelDone(ctx, input.UserID, input.GenerationID, "a", 0, 3001); err != nil {
		t.Fatal(err)
	}
	committing, err := store.BeginCommit(ctx, input.UserID, input.GenerationID, 3002)
	if err != nil {
		t.Fatal(err)
	}
	before := requirePositivePlatformGenerationTTL(t, client, key)
	tooFresh, err := store.FailStaleCommit(ctx, input.UserID, input.GenerationID, "internal_error", committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds()-1)
	if !errors.Is(err, ErrPlatformGenerationConflict) || tooFresh.State != PlatformGenerationStateCommitting {
		t.Fatalf("fresh fail=%#v error=%v", tooFresh, err)
	}
	freshTTL := requirePlatformGenerationTTLNotIncreased(t, client, key, before)
	failed, err := store.FailStaleCommit(ctx, input.UserID, input.GenerationID, "internal_error", committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds())
	if err != nil || failed.State != PlatformGenerationStateFailed || failed.ErrorCode != "internal_error" || failed.ModelStates["a"].AssistantMessageGUID != "" {
		t.Fatalf("stale fail=%#v error=%v", failed, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, freshTTL)
	assertPlatformGenerationRedisMetadataOnly(t, client, key)
}

func TestPlatformGenerationStoreFailStaleCommitLosesToCompletedReceipt(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 910023, GenerationID: "740e8400-e29b-41d4-a716-446655440000", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 4000}
	ctx := context.Background()
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claimTestGeneration(t, store, input)
	if _, err := store.MarkModelDone(ctx, input.UserID, input.GenerationID, "a", 0, 4001); err != nil {
		t.Fatal(err)
	}
	committing, err := store.BeginCommit(ctx, input.UserID, input.GenerationID, 4002)
	if err != nil {
		t.Fatal(err)
	}
	guid := "900000000000000104"
	if _, err := store.ReconcileComplete(ctx, input.UserID, input.GenerationID, map[string]string{"a": guid}, 4003); err != nil {
		t.Fatal(err)
	}
	before := requirePositivePlatformGenerationTTL(t, client, key)
	completed, err := store.FailStaleCommit(ctx, input.UserID, input.GenerationID, "internal_error", committing.UpdatedAtMillis+platformGenerationConvergenceWindow.Milliseconds())
	if !errors.Is(err, ErrPlatformGenerationConflict) || completed.State != PlatformGenerationStateCompleted || completed.ModelStates["a"].AssistantMessageGUID != guid {
		t.Fatalf("completed lost stale-fail race: snapshot=%#v error=%v", completed, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, before)
	assertPlatformGenerationRedisMetadataOnly(t, client, key)
}

func TestPlatformGenerationStoreReconciliationRejectsInvalidInputBeforeDependencies(t *testing.T) {
	disconnected, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = disconnected.Close() }()
	tests := []struct {
		name         string
		store        *PlatformGenerationStore
		ctx          context.Context
		userID       int64
		generationID string
		nowMillis    int64
	}{
		{"nil store", nil, context.Background(), 1, generationTestID, 1},
		{"nil context", disconnected, nil, 1, generationTestID, 1},
		{"invalid owner", disconnected, context.Background(), 0, generationTestID, 1},
		{"invalid generation", disconnected, context.Background(), 1, "bad", 1},
		{"zero time", disconnected, context.Background(), 1, generationTestID, 0},
		{"negative time", disconnected, context.Background(), 1, generationTestID, -1},
		{"unsafe time", disconnected, context.Background(), 1, generationTestID, platformSSEV2MaxSafeInteger + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.store.ReconcileComplete(test.ctx, test.userID, test.generationID, map[string]string{"a": "900000000000000105"}, test.nowMillis); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("ReconcileComplete error=%v, want invalid", err)
			}
			if _, err := test.store.FailStaleCommit(test.ctx, test.userID, test.generationID, "internal_error", test.nowMillis); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("FailStaleCommit error=%v, want invalid", err)
			}
		})
	}
}

func assertPlatformGenerationRedisMetadataOnly(t *testing.T, client redis.Cmdable, key string) {
	t.Helper()
	raw, err := client.Get(context.Background(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"content", "token", "quota", "cost"} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Fatalf("stored record contains forbidden field %q: %s", forbidden, raw)
		}
	}
}

func TestDecodePlatformGenerationRejectsMalformedRedisRecords(t *testing.T) {
	valid := PlatformGenerationSnapshot{GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}}, CreatedAtMillis: 1, UpdatedAtMillis: 1}
	mutations := []func(*PlatformGenerationSnapshot){
		func(s *PlatformGenerationSnapshot) { s.GenerationID = "bad" },
		func(s *PlatformGenerationSnapshot) {
			s.ModelStates["extra"] = PlatformGenerationModel{State: PlatformGenerationStateRunning}
		},
		func(s *PlatformGenerationSnapshot) {
			s.ModelStates["a"] = PlatformGenerationModel{Seq: -1, State: PlatformGenerationStateRunning}
		},
		func(s *PlatformGenerationSnapshot) { s.ErrorCode = "secret detail" },
		func(s *PlatformGenerationSnapshot) { s.UpdatedAtMillis = 0 },
	}
	for index, mutate := range mutations {
		copySnapshot := valid
		copySnapshot.Models = append([]string(nil), valid.Models...)
		copySnapshot.ModelStates = map[string]PlatformGenerationModel{"a": valid.ModelStates["a"]}
		mutate(&copySnapshot)
		raw, _ := json.Marshal(copySnapshot)
		if _, err := decodePlatformGeneration(string(raw)); !errors.Is(err, ErrPlatformGenerationInvalid) {
			t.Fatalf("mutation %d accepted: %s", index, raw)
		}
	}
	raw, _ := json.Marshal(valid)
	withUnknown := strings.TrimSuffix(string(raw), "}") + `,"prompt":"secret"}`
	if _, err := decodePlatformGeneration(withUnknown); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("unknown Redis field accepted: %s", withUnknown)
	}
}

func TestPlatformGenerationRecordAcceptsLeaseAwareAndPristineTombstoneShapes(t *testing.T) {
	claimed := PlatformGenerationSnapshot{
		GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning,
		ModelStates:     map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}},
		CreatedAtMillis: 1000, UpdatedAtMillis: 1000, LeaseOwnerSHA256: strings.Repeat("a", 64), LeaseUntilMillis: 31000,
	}
	tombstone := PlatformGenerationSnapshot{
		GenerationID: generationTestID, State: PlatformGenerationStateCancelled, Models: []string{}, ModelStates: map[string]PlatformGenerationModel{},
		CreatedAtMillis: 1000, UpdatedAtMillis: 1000,
	}
	for _, snapshot := range []PlatformGenerationSnapshot{claimed, tombstone} {
		raw, err := encodePlatformGeneration(snapshot)
		if err != nil {
			t.Fatalf("encode %#v: %v", snapshot, err)
		}
		if snapshot.State == PlatformGenerationStateCancelled && (!strings.Contains(raw, `"models":[]`) || !strings.Contains(raw, `"model_states":{}`)) {
			t.Fatalf("canonical tombstone shape=%s", raw)
		}
		decoded, err := decodePlatformGeneration(raw)
		if err != nil || !reflect.DeepEqual(decoded, snapshot) {
			t.Fatalf("decode=%#v want=%#v error=%v", decoded, snapshot, err)
		}
	}
}

func TestPlatformGenerationRecordRejectsPristineTombstoneWithMissingOrNullCollections(t *testing.T) {
	canonical := `{"generation_id":"` + generationTestID + `","models":[],"state":3,"model_states":{},"created_at_ms":1000,"updated_at_ms":1000}`
	tests := []struct {
		name string
		raw  string
	}{
		{"models omitted", strings.Replace(canonical, `"models":[],`, "", 1)},
		{"models null", strings.Replace(canonical, `"models":[]`, `"models":null`, 1)},
		{"model states omitted", strings.Replace(canonical, `,"model_states":{}`, "", 1)},
		{"model states null", strings.Replace(canonical, `"model_states":{}`, `"model_states":null`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodePlatformGeneration(test.raw); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("decode error=%v, want invalid", err)
			}
		})
	}
}

func TestPlatformGenerationRecordRejectsMissingOrNullRequiredWireFields(t *testing.T) {
	claimed := `{"generation_id":"` + generationTestID + `","mode":1,"models":["a"],"state":1,"model_states":{"a":{"seq":0,"state":1}},"created_at_ms":1000,"updated_at_ms":1000}`
	tests := []struct {
		name string
		raw  string
	}{
		{"generation id missing", strings.Replace(claimed, `"generation_id":"`+generationTestID+`",`, "", 1)},
		{"generation id null", strings.Replace(claimed, `"generation_id":"`+generationTestID+`"`, `"generation_id":null`, 1)},
		{"models missing", strings.Replace(claimed, `"models":["a"],`, "", 1)},
		{"models null", strings.Replace(claimed, `"models":["a"]`, `"models":null`, 1)},
		{"state missing", strings.Replace(claimed, `"state":1,`, "", 1)},
		{"state null", strings.Replace(claimed, `"state":1`, `"state":null`, 1)},
		{"model states missing", strings.Replace(claimed, `"model_states":{"a":{"seq":0,"state":1}},`, "", 1)},
		{"model states null", strings.Replace(claimed, `"model_states":{"a":{"seq":0,"state":1}}`, `"model_states":null`, 1)},
		{"created at missing", strings.Replace(claimed, `"created_at_ms":1000,`, "", 1)},
		{"created at null", strings.Replace(claimed, `"created_at_ms":1000`, `"created_at_ms":null`, 1)},
		{"updated at missing", strings.Replace(claimed, `,"updated_at_ms":1000`, "", 1)},
		{"updated at null", strings.Replace(claimed, `"updated_at_ms":1000`, `"updated_at_ms":null`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodePlatformGeneration(test.raw); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("decode error=%v, want invalid", err)
			}
		})
	}
}

func TestPlatformGenerationRecordRejectsExplicitOptionalWireFields(t *testing.T) {
	claimed := `{"generation_id":"` + generationTestID + `","mode":1,"models":["a"],"state":1,"model_states":{"a":{"seq":0,"state":1}},"created_at_ms":1000,"updated_at_ms":1000}`
	tombstone := `{"generation_id":"` + generationTestID + `","models":[],"state":3,"model_states":{},"created_at_ms":1000,"updated_at_ms":1000}`
	tests := []struct {
		name string
		raw  string
	}{
		{"legacy lease fields both null", strings.TrimSuffix(claimed, "}") + `,"lease_owner_sha256":null,"lease_until_ms":null}`},
		{"legacy lease fields empty and zero", strings.TrimSuffix(claimed, "}") + `,"lease_owner_sha256":"","lease_until_ms":0}`},
		{"tombstone mode null", strings.TrimSuffix(tombstone, "}") + `,"mode":null}`},
		{"tombstone mode zero", strings.TrimSuffix(tombstone, "}") + `,"mode":0}`},
		{"tombstone error null", strings.TrimSuffix(tombstone, "}") + `,"error_code":null}`},
		{"tombstone error empty", strings.TrimSuffix(tombstone, "}") + `,"error_code":""}`},
		{"tombstone lease digest null", strings.TrimSuffix(tombstone, "}") + `,"lease_owner_sha256":null}`},
		{"tombstone lease digest empty", strings.TrimSuffix(tombstone, "}") + `,"lease_owner_sha256":""}`},
		{"tombstone lease deadline null", strings.TrimSuffix(tombstone, "}") + `,"lease_until_ms":null}`},
		{"tombstone lease deadline zero", strings.TrimSuffix(tombstone, "}") + `,"lease_until_ms":0}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodePlatformGeneration(test.raw); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("decode error=%v, want invalid", err)
			}
		})
	}
}

func TestPlatformGenerationRecordRejectsMissingNullAndForbiddenNestedWireFields(t *testing.T) {
	claimed := `{"generation_id":"` + generationTestID + `","mode":1,"models":["a"],"state":1,"model_states":{"a":{"seq":0,"state":1}},"created_at_ms":1000,"updated_at_ms":1000}`
	tests := []struct {
		name string
		raw  string
	}{
		{"nested seq missing", strings.Replace(claimed, `"seq":0,`, "", 1)},
		{"nested seq null", strings.Replace(claimed, `"seq":0`, `"seq":null`, 1)},
		{"nested state missing", strings.Replace(claimed, `,"state":1}`, "}", 1)},
		{"nested state null", strings.Replace(claimed, `"state":1}`, `"state":null}`, 1)},
		{"nested error null", strings.Replace(claimed, `"state":1}`, `"state":1,"error_code":null}`, 1)},
		{"nested error empty", strings.Replace(claimed, `"state":1}`, `"state":1,"error_code":""}`, 1)},
		{"nested guid null", strings.Replace(claimed, `"state":1}`, `"state":1,"assistant_message_guid":null}`, 1)},
		{"nested guid empty", strings.Replace(claimed, `"state":1}`, `"state":1,"assistant_message_guid":""}`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodePlatformGeneration(test.raw); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("decode error=%v, want invalid", err)
			}
		})
	}
}

func TestPlatformGenerationRecordRejectsInvalidLeaseAndTombstoneShapes(t *testing.T) {
	claimed := PlatformGenerationSnapshot{
		GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning,
		ModelStates:     map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}},
		CreatedAtMillis: 1000, UpdatedAtMillis: 1000, LeaseOwnerSHA256: strings.Repeat("a", 64), LeaseUntilMillis: 31000,
	}
	tombstone := PlatformGenerationSnapshot{
		GenerationID: generationTestID, State: PlatformGenerationStateCancelled, Models: []string{}, ModelStates: map[string]PlatformGenerationModel{},
		CreatedAtMillis: 1000, UpdatedAtMillis: 1000,
	}
	failed := PlatformGenerationSnapshot{
		GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateFailed,
		ModelStates:     map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateFailed, ErrorCode: "internal_error"}},
		CreatedAtMillis: 1000, UpdatedAtMillis: 1000, ErrorCode: "internal_error",
	}
	tests := []struct {
		name     string
		snapshot PlatformGenerationSnapshot
	}{
		{"lease digest without deadline", func() PlatformGenerationSnapshot { s := claimed; s.LeaseUntilMillis = 0; return s }()},
		{"lease deadline without digest", func() PlatformGenerationSnapshot { s := claimed; s.LeaseOwnerSHA256 = ""; return s }()},
		{"uppercase lease digest", func() PlatformGenerationSnapshot {
			s := claimed
			s.LeaseOwnerSHA256 = strings.Repeat("A", 64)
			return s
		}()},
		{"nonhex lease digest", func() PlatformGenerationSnapshot {
			s := claimed
			s.LeaseOwnerSHA256 = strings.Repeat("g", 64)
			return s
		}()},
		{"lease deadline not after updated", func() PlatformGenerationSnapshot { s := claimed; s.LeaseUntilMillis = 1000; return s }()},
		{"tombstone with mode", func() PlatformGenerationSnapshot { s := tombstone; s.Mode = PlatformGenerationModeSingle; return s }()},
		{"tombstone with models", func() PlatformGenerationSnapshot {
			s := tombstone
			s.Models = []string{"a"}
			s.ModelStates = map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateCancelled}}
			return s
		}()},
		{"tombstone with error", func() PlatformGenerationSnapshot { s := tombstone; s.ErrorCode = "cancelled"; return s }()},
		{"tombstone with lease", func() PlatformGenerationSnapshot {
			s := tombstone
			s.LeaseOwnerSHA256 = strings.Repeat("a", 64)
			s.LeaseUntilMillis = 31000
			return s
		}()},
		{"non-cancelled empty identity", func() PlatformGenerationSnapshot { s := tombstone; s.State = PlatformGenerationStateRunning; return s }()},
		{"terminal failed record retaining lease", func() PlatformGenerationSnapshot {
			s := failed
			s.LeaseOwnerSHA256 = strings.Repeat("a", 64)
			s.LeaseUntilMillis = 31000
			return s
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := encodePlatformGeneration(test.snapshot); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("encode error=%v, want invalid", err)
			}
			raw, err := json.Marshal(test.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodePlatformGeneration(string(raw)); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("decode error=%v, want invalid", err)
			}
		})
	}
}

func TestPlatformGenerationRecordAcceptsLegacyRecordsWithoutLease(t *testing.T) {
	for _, snapshot := range []PlatformGenerationSnapshot{
		{GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}}, CreatedAtMillis: 1000, UpdatedAtMillis: 1000},
		{GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateCompleted, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateCompleted, AssistantMessageGUID: "900000000000000001"}}, CreatedAtMillis: 1000, UpdatedAtMillis: 1001},
	} {
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodePlatformGeneration(string(raw)); err != nil {
			t.Fatalf("legacy record rejected: %v", err)
		}
	}
}

func TestPlatformGenerationRecordAcceptsLegacySafeTimestampsPastLeaseWindow(t *testing.T) {
	nearMaximum := platformSSEV2MaxSafeInteger - platformGenerationLeaseDuration.Milliseconds() + 1
	snapshots := []PlatformGenerationSnapshot{
		{GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}}, CreatedAtMillis: nearMaximum, UpdatedAtMillis: nearMaximum},
		{GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateCompleted, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateCompleted, AssistantMessageGUID: "900000000000000009"}}, CreatedAtMillis: nearMaximum, UpdatedAtMillis: platformSSEV2MaxSafeInteger},
	}
	for _, snapshot := range snapshots {
		encoded, err := encodePlatformGeneration(snapshot)
		if err != nil {
			t.Fatalf("encode legacy snapshot %#v: %v", snapshot, err)
		}
		if _, err := decodePlatformGeneration(encoded); err != nil {
			t.Fatalf("decode encoded legacy snapshot: %v", err)
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodePlatformGeneration(string(raw)); err != nil {
			t.Fatalf("decode raw legacy snapshot: %v", err)
		}
	}
}

func TestPlatformGenerationClaimResultDoesNotMarshalLeaseToken(t *testing.T) {
	raw, err := json.Marshal(PlatformGenerationClaimResult{Snapshot: PlatformGenerationSnapshot{GenerationID: generationTestID}, Duplicate: true, LeaseToken: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), `"Snapshot"`) || strings.Contains(string(raw), `"Duplicate"`) || !strings.Contains(string(raw), `"snapshot"`) || !strings.Contains(string(raw), `"duplicate":true`) {
		t.Fatalf("claim result leaked lease token: %s", raw)
	}
}

func TestNewPlatformGenerationLeaseUsesRawURLTokenAndSHA256Digest(t *testing.T) {
	token, digest, err := newPlatformGenerationLease()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != platformGenerationLeaseBytes {
		t.Fatalf("lease token=%q raw=%x error=%v", token, raw, err)
	}
	sum := sha256.Sum256([]byte(token))
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("lease digest=%q, want SHA-256 of raw token", digest)
	}
}

func TestNewPlatformGenerationLeaseSanitizesEntropyFailure(t *testing.T) {
	if _, _, err := newPlatformGenerationLeaseFrom(platformGenerationFailingReader{}); !errors.Is(err, ErrPlatformGenerationUnavailable) {
		t.Fatalf("new lease error=%v, want unavailable", err)
	}
}

func TestNewPlatformGenerationLeaseRejectsShortEntropyRead(t *testing.T) {
	if _, _, err := newPlatformGenerationLeaseFrom(strings.NewReader("short")); !errors.Is(err, ErrPlatformGenerationUnavailable) {
		t.Fatalf("new lease error=%v, want unavailable", err)
	}
}

type platformGenerationFailingReader struct{}

func (platformGenerationFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func TestPlatformGenerationStoreRejectsRecordWhoseGenerationIDDoesNotMatchKey(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	userID := int64(910008)
	keyGenerationID := "6b0e8400-e29b-41d4-a716-446655440000"
	recordGenerationID := "6c0e8400-e29b-41d4-a716-446655440000"
	snapshot := PlatformGenerationSnapshot{GenerationID: recordGenerationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}}, CreatedAtMillis: 1, UpdatedAtMillis: 1}
	raw, err := encodePlatformGeneration(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	key := store.key(userID, keyGenerationID)
	if err := client.Set(context.Background(), key, raw, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), userID, keyGenerationID); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("Get mismatched record error=%v, want invalid", err)
	}
	input := PlatformGenerationClaimInput{UserID: userID, GenerationID: keyGenerationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 2}
	if result, err := store.Claim(context.Background(), input); !result.Duplicate || !errors.Is(err, ErrPlatformGenerationInvalid) || result.LeaseToken != "" {
		t.Fatalf("duplicate mismatched record result=%#v error=%v", result, err)
	}
}

func TestPlatformGenerationCancelOrCreateCreatesOneCanonicalTombstone(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	const userID int64 = 930001
	const generationID = "80000000-0000-4000-8000-000000000001"
	key := store.key(userID, generationID)
	preparePlatformGenerationTestKey(t, client, key)

	start := make(chan struct{})
	results := make(chan PlatformGenerationCancelDecision, 12)
	errors := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			decision, err := store.CancelOrCreate(context.Background(), userID, generationID, 1000)
			results <- decision
			errors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("CancelOrCreate error=%v", err)
		}
	}
	created := 0
	for decision := range results {
		if decision.CreatedTombstone {
			created++
		}
		if decision.Transitioned || decision.Snapshot.State != PlatformGenerationStateCancelled {
			t.Fatalf("unexpected cancel decision=%#v", decision)
		}
	}
	if created != 1 {
		t.Fatalf("created tombstones=%d, want 1", created)
	}
	raw, err := client.Get(context.Background(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"generation_id":"` + generationID + `","models":[],"state":3,"model_states":{},"created_at_ms":1000,"updated_at_ms":1000}`
	if raw != want {
		t.Fatalf("tombstone JSON=%s, want=%s", raw, want)
	}
	ttl := requirePositivePlatformGenerationTTL(t, client, key)
	if ttl <= 23*time.Hour || ttl > platformGenerationTTL {
		t.Fatalf("tombstone TTL=%v, want approximately 24h", ttl)
	}
}

func TestPlatformGenerationCancelBeforeClaimEnrichesOnlyMatchingIdentity(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 930002, GenerationID: "80000000-0000-4000-8000-000000000002", Mode: PlatformGenerationModeCompare, Models: []string{"a", "b"}, NowMillis: 2000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	if _, err := store.CancelOrCreate(context.Background(), input.UserID, input.GenerationID, 1000); err != nil {
		t.Fatal(err)
	}
	before := requirePositivePlatformGenerationTTL(t, client, key)

	result, err := store.Claim(context.Background(), input)
	if !errors.Is(err, ErrPlatformGenerationConflict) || !result.Duplicate || result.LeaseToken != "" {
		t.Fatalf("enriched Claim=%#v error=%v", result, err)
	}
	if result.Snapshot.State != PlatformGenerationStateCancelled || result.Snapshot.Mode != input.Mode || !reflect.DeepEqual(result.Snapshot.Models, input.Models) || result.Snapshot.CreatedAtMillis != 1000 || result.Snapshot.UpdatedAtMillis != 1000 {
		t.Fatalf("enriched snapshot=%#v", result.Snapshot)
	}
	for _, model := range input.Models {
		if state := result.Snapshot.ModelStates[model]; state.State != PlatformGenerationStateCancelled || state.Seq != 0 || state.ErrorCode != "" || state.AssistantMessageGUID != "" {
			t.Fatalf("model %q state=%#v", model, state)
		}
	}
	if result.Snapshot.LeaseOwnerSHA256 != "" || result.Snapshot.LeaseUntilMillis != 0 {
		t.Fatalf("enriched tombstone retained lease: %#v", result.Snapshot)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, before)

	different := input
	different.Models = []string{"a", "c"}
	different.NowMillis = 3000
	second, err := store.Claim(context.Background(), different)
	if !errors.Is(err, ErrPlatformGenerationConflict) || !second.Duplicate || !reflect.DeepEqual(second.Snapshot.Models, input.Models) {
		t.Fatalf("different identity overwrote tombstone: result=%#v error=%v", second, err)
	}
}

func TestPlatformGenerationCancelRunningTransitionsOnlyOnceAndKeepsTTL(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 930003, GenerationID: "80000000-0000-4000-8000-000000000003", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claim := claimTestGeneration(t, store, input)
	before := requirePositivePlatformGenerationTTL(t, client, key)

	start := make(chan struct{})
	results := make(chan PlatformGenerationCancelDecision, 12)
	errors := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			decision, err := store.CancelOrCreate(context.Background(), input.UserID, input.GenerationID, 1001)
			results <- decision
			errors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("CancelOrCreate error=%v", err)
		}
	}
	winners := 0
	for decision := range results {
		if decision.Transitioned {
			winners++
		}
		if decision.CreatedTombstone || decision.Snapshot.State != PlatformGenerationStateCancelling || decision.Snapshot.LeaseOwnerSHA256 != "" || decision.Snapshot.LeaseUntilMillis != 0 {
			t.Fatalf("unexpected cancel decision=%#v", decision)
		}
	}
	if winners != 1 {
		t.Fatalf("transition winners=%d, want 1", winners)
	}
	if claim.LeaseToken == "" {
		t.Fatal("claim did not return raw lease token")
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, before)
}

func TestPlatformGenerationLeaseRenewalValidatesRawTokenBeforeRedis(t *testing.T) {
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for _, token := range []string{"", "x", strings.Repeat("a", 44), "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"} {
		if _, err := store.RenewLease(context.Background(), 1, generationTestID, token, 1); !errors.Is(err, ErrPlatformGenerationInvalid) {
			t.Fatalf("RenewLease token=%q error=%v, want invalid", token, err)
		}
	}
	validToken := base64.RawURLEncoding.EncodeToString(make([]byte, platformGenerationLeaseBytes))
	if _, err := store.RenewLease(context.Background(), 1, generationTestID, validToken, platformSSEV2MaxSafeInteger-platformGenerationLeaseDuration.Milliseconds()+1); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("RenewLease unsafe deadline error=%v, want invalid", err)
	}
}

func TestPlatformGenerationLeaseRenewalUsesDigestAndPreservesUpdatedAtAndTTL(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	input := PlatformGenerationClaimInput{UserID: 930010, GenerationID: "81000000-0000-4000-8000-000000000010", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claim := claimTestGeneration(t, store, input)
	before := requirePositivePlatformGenerationTTL(t, client, key)

	renewed, err := store.RenewLease(context.Background(), input.UserID, input.GenerationID, claim.LeaseToken, 2000)
	if err != nil || renewed.LeaseUntilMillis != 32000 || renewed.CreatedAtMillis != 1000 || renewed.UpdatedAtMillis != 1000 {
		t.Fatalf("renewed=%#v error=%v", renewed, err)
	}
	if renewed.LeaseOwnerSHA256 != platformGenerationLeaseDigest(claim.LeaseToken) || strings.Contains(fmt.Sprint(renewed), claim.LeaseToken) {
		t.Fatalf("lease token was not represented only by digest: %#v", renewed)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, before)
	raw, err := client.Get(context.Background(), key).Result()
	if err != nil || strings.Contains(raw, claim.LeaseToken) {
		t.Fatalf("stored raw lease token: raw=%s error=%v", raw, err)
	}

	authoritative, err := store.RenewLease(context.Background(), input.UserID, input.GenerationID, claim.LeaseToken+"x", 2001)
	if !errors.Is(err, ErrPlatformGenerationInvalid) || authoritative.GenerationID != "" {
		t.Fatalf("malformed token result=%#v error=%v", authoritative, err)
	}
	wrongRaw := make([]byte, platformGenerationLeaseBytes)
	wrongRaw[0] = 1
	wrong := base64.RawURLEncoding.EncodeToString(wrongRaw)
	authoritative, err = store.RenewLease(context.Background(), input.UserID, input.GenerationID, wrong, 2001)
	if !errors.Is(err, ErrPlatformGenerationConflict) || authoritative.LeaseUntilMillis != renewed.LeaseUntilMillis {
		t.Fatalf("wrong token authority=%#v error=%v", authoritative, err)
	}
}

func TestPlatformGenerationLeaseRenewalRejectsLateLegacyAndNonRunningRecords(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	token := base64.RawURLEncoding.EncodeToString(make([]byte, platformGenerationLeaseBytes))
	digest := platformGenerationLeaseDigest(token)
	tests := []struct {
		name     string
		userID   int64
		snapshot PlatformGenerationSnapshot
		now      int64
	}{
		{"late", 930011, PlatformGenerationSnapshot{GenerationID: "81000000-0000-4000-8000-000000000011", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}}, CreatedAtMillis: 1000, UpdatedAtMillis: 1000, LeaseOwnerSHA256: digest, LeaseUntilMillis: 31000}, 31001},
		{"legacy", 930012, PlatformGenerationSnapshot{GenerationID: "81000000-0000-4000-8000-000000000012", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}}, CreatedAtMillis: 1000, UpdatedAtMillis: 1000}, 2000},
		{"cancelled", 930013, PlatformGenerationSnapshot{GenerationID: "81000000-0000-4000-8000-000000000013", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateCancelled, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateCancelled}}, CreatedAtMillis: 1000, UpdatedAtMillis: 1001}, 2000},
		{"committing", 930014, PlatformGenerationSnapshot{GenerationID: "81000000-0000-4000-8000-000000000014", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateCommitting, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateCompleted}}, CreatedAtMillis: 1000, UpdatedAtMillis: 1001}, 2000},
		{"completed", 930015, PlatformGenerationSnapshot{GenerationID: "81000000-0000-4000-8000-000000000015", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateCompleted, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateCompleted, AssistantMessageGUID: "900000000000000015"}}, CreatedAtMillis: 1000, UpdatedAtMillis: 1001}, 2000},
		{"failed", 930016, PlatformGenerationSnapshot{GenerationID: "81000000-0000-4000-8000-000000000016", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateFailed, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateFailed, ErrorCode: "internal_error"}}, CreatedAtMillis: 1000, UpdatedAtMillis: 1001, ErrorCode: "internal_error"}, 2000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := store.key(test.userID, test.snapshot.GenerationID)
			preparePlatformGenerationTestKey(t, client, key)
			raw, err := encodePlatformGeneration(test.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Set(ctx, key, raw, platformGenerationTTL).Err(); err != nil {
				t.Fatal(err)
			}
			before := requirePositivePlatformGenerationTTL(t, client, key)
			authoritative, err := store.RenewLease(ctx, test.userID, test.snapshot.GenerationID, token, test.now)
			if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, test.snapshot) {
				t.Fatalf("RenewLease authority=%#v error=%v", authoritative, err)
			}
			requirePlatformGenerationTTLNotIncreased(t, client, key, before)
		})
	}
}

func TestPlatformGenerationExpiredRunningFailsOnlyRemainingModels(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	const userID int64 = 930020
	const generationID = "82000000-0000-4000-8000-000000000020"
	snapshot := PlatformGenerationSnapshot{
		GenerationID: generationID, Mode: PlatformGenerationModeCompare, Models: []string{"done", "failed", "running"}, State: PlatformGenerationStateRunning,
		ModelStates: map[string]PlatformGenerationModel{
			"done":    {Seq: 2, State: PlatformGenerationStateCompleted},
			"failed":  {Seq: 1, State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
			"running": {Seq: 3, State: PlatformGenerationStateRunning},
		},
		CreatedAtMillis: 1000, UpdatedAtMillis: 2000, LeaseOwnerSHA256: strings.Repeat("a", 64), LeaseUntilMillis: 32000,
	}
	key := store.key(userID, generationID)
	preparePlatformGenerationTestKey(t, client, key)
	raw, err := encodePlatformGeneration(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, key, raw, platformGenerationTTL).Err(); err != nil {
		t.Fatal(err)
	}
	before := requirePositivePlatformGenerationTTL(t, client, key)
	failed, err := store.FailExpiredRunning(ctx, userID, generationID, 32000)
	if err != nil || failed.State != PlatformGenerationStateFailed || failed.ErrorCode != "internal_error" || failed.UpdatedAtMillis != 32000 {
		t.Fatalf("expired snapshot=%#v error=%v", failed, err)
	}
	if failed.ModelStates["done"] != snapshot.ModelStates["done"] || failed.ModelStates["failed"] != snapshot.ModelStates["failed"] || failed.ModelStates["running"].State != PlatformGenerationStateFailed || failed.ModelStates["running"].ErrorCode != "internal_error" {
		t.Fatalf("expired model states=%#v", failed.ModelStates)
	}
	if failed.LeaseOwnerSHA256 != "" || failed.LeaseUntilMillis != 0 {
		t.Fatalf("expired lease retained: %#v", failed)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, before)
}

func TestPlatformGenerationExpiredRunningConflictsWithActiveLeaseAndExpiresLegacyOrphan(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	input := PlatformGenerationClaimInput{UserID: 930021, GenerationID: "82000000-0000-4000-8000-000000000021", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claim := claimTestGeneration(t, store, input)
	authoritative, err := store.FailExpiredRunning(ctx, input.UserID, input.GenerationID, 30999)
	if !errors.Is(err, ErrPlatformGenerationConflict) || authoritative.LeaseOwnerSHA256 != platformGenerationLeaseDigest(claim.LeaseToken) {
		t.Fatalf("active authority=%#v error=%v", authoritative, err)
	}

	legacyID := "82000000-0000-4000-8000-000000000022"
	legacy := PlatformGenerationSnapshot{GenerationID: legacyID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}}, CreatedAtMillis: 1000, UpdatedAtMillis: 1000}
	legacyKey := store.key(input.UserID, legacyID)
	preparePlatformGenerationTestKey(t, client, legacyKey)
	raw, err := encodePlatformGeneration(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, legacyKey, raw, platformGenerationTTL).Err(); err != nil {
		t.Fatal(err)
	}
	if expired, err := store.FailExpiredRunning(ctx, input.UserID, legacyID, 1000); err != nil || expired.State != PlatformGenerationStateFailed {
		t.Fatalf("legacy expiry=%#v error=%v", expired, err)
	}
}

func TestPlatformGenerationExpiredRunningRejectsClockRegressionWithoutMutation(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	const userID int64 = 930023
	const generationID = "82000000-0000-4000-8000-000000000023"
	snapshot := PlatformGenerationSnapshot{GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning, ModelStates: map[string]PlatformGenerationModel{"a": {State: PlatformGenerationStateRunning}}, CreatedAtMillis: 1000, UpdatedAtMillis: 2000}
	key := store.key(userID, generationID)
	preparePlatformGenerationTestKey(t, client, key)
	raw, err := encodePlatformGeneration(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, key, raw, platformGenerationTTL).Err(); err != nil {
		t.Fatal(err)
	}
	authoritative, err := store.FailExpiredRunning(ctx, userID, generationID, 1999)
	if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, snapshot) {
		t.Fatalf("clock regression authority=%#v error=%v", authoritative, err)
	}
	if stored, err := client.Get(ctx, key).Result(); err != nil || stored != raw {
		t.Fatalf("clock regression mutated record: raw=%s error=%v", stored, err)
	}
}

func TestPlatformGenerationLeaseRenewVersusExpireHasOneAuthority(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	for iteration := 0; iteration < 24; iteration++ {
		generationID := fmt.Sprintf("83000000-0000-4000-8000-%012x", iteration+1)
		input := PlatformGenerationClaimInput{UserID: 930030 + int64(iteration), GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
		key := store.key(input.UserID, generationID)
		preparePlatformGenerationTestKey(t, client, key)
		claim := claimTestGeneration(t, store, input)
		beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)

		start := make(chan struct{})
		type outcome struct {
			operation string
			snapshot  PlatformGenerationSnapshot
			err       error
		}
		outcomes := make(chan outcome, 2)
		go func() {
			<-start
			snapshot, err := store.RenewLease(ctx, input.UserID, generationID, claim.LeaseToken, 31000)
			outcomes <- outcome{"renew", snapshot, err}
		}()
		go func() {
			<-start
			snapshot, err := store.FailExpiredRunning(ctx, input.UserID, generationID, 31000)
			outcomes <- outcome{"expire", snapshot, err}
		}()
		close(start)
		first, second := <-outcomes, <-outcomes
		winners := 0
		for _, result := range []outcome{first, second} {
			if result.err == nil {
				winners++
			} else if !errors.Is(result.err, ErrPlatformGenerationConflict) {
				t.Fatalf("iteration %d unexpected error=%v snapshot=%#v", iteration, result.err, result.snapshot)
			}
		}
		if winners != 1 {
			t.Fatalf("iteration %d winners=%d first=%#v second=%#v", iteration, winners, first, second)
		}
		authoritative, err := store.Get(ctx, input.UserID, generationID)
		if err != nil || (authoritative.State == PlatformGenerationStateRunning && authoritative.LeaseUntilMillis != 61000) || (authoritative.State != PlatformGenerationStateRunning && authoritative.State != PlatformGenerationStateFailed) {
			t.Fatalf("iteration %d authority=%#v error=%v", iteration, authoritative, err)
		}
		for _, result := range []outcome{first, second} {
			if !reflect.DeepEqual(result.snapshot, authoritative) {
				t.Fatalf("iteration %d %s snapshot=%#v authority=%#v error=%v", iteration, result.operation, result.snapshot, authoritative, result.err)
			}
		}
		requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
	}
}

func TestPlatformGenerationCancelVersusBeginCommitHasOneWinner(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	for iteration := 0; iteration < 24; iteration++ {
		generationID := fmt.Sprintf("84000000-0000-4000-8000-%012x", iteration+1)
		input := PlatformGenerationClaimInput{UserID: 930060 + int64(iteration), GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
		key := store.key(input.UserID, generationID)
		preparePlatformGenerationTestKey(t, client, key)
		claimTestGeneration(t, store, input)
		if _, err := store.MarkModelDone(ctx, input.UserID, generationID, "a", 0, 1001); err != nil {
			t.Fatal(err)
		}
		beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)

		start := make(chan struct{})
		cancelResults := make(chan struct {
			decision PlatformGenerationCancelDecision
			err      error
		}, 1)
		type commitOutcome struct {
			snapshot PlatformGenerationSnapshot
			err      error
		}
		commitResults := make(chan commitOutcome, 1)
		go func() {
			<-start
			decision, err := store.CancelOrCreate(ctx, input.UserID, generationID, 1002)
			cancelResults <- struct {
				decision PlatformGenerationCancelDecision
				err      error
			}{decision, err}
		}()
		go func() {
			<-start
			snapshot, err := store.BeginCommit(ctx, input.UserID, generationID, 1002)
			commitResults <- commitOutcome{snapshot, err}
		}()
		close(start)
		cancel := <-cancelResults
		commit := <-commitResults
		winners := 0
		if cancel.err == nil && cancel.decision.Transitioned {
			winners++
		}
		if commit.err == nil {
			winners++
		}
		if winners != 1 || (cancel.err != nil && !errors.Is(cancel.err, ErrPlatformGenerationConflict)) || (commit.err != nil && !errors.Is(commit.err, ErrPlatformGenerationConflict)) {
			t.Fatalf("iteration %d cancel=%#v commit=%#v winners=%d", iteration, cancel, commit, winners)
		}
		final, err := store.Get(ctx, input.UserID, generationID)
		if err != nil || !reflect.DeepEqual(cancel.decision.Snapshot, final) || !reflect.DeepEqual(commit.snapshot, final) {
			t.Fatalf("iteration %d cancel=%#v commit=%#v final=%#v error=%v", iteration, cancel, commit, final, err)
		}
		if final.State == PlatformGenerationStateCancelling && (!cancel.decision.Transitioned || cancel.decision.CreatedTombstone) {
			t.Fatalf("iteration %d cancelling flags=%#v", iteration, cancel.decision)
		}
		if final.State == PlatformGenerationStateCommitting && (cancel.decision.Transitioned || cancel.decision.CreatedTombstone) {
			t.Fatalf("iteration %d committing flags=%#v", iteration, cancel.decision)
		}
		requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
	}
}

func TestPlatformGenerationCancelFailsClosedOnMalformedRecordAndSanitizesRedisError(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	const userID int64 = 930090
	const generationID = "85000000-0000-4000-8000-000000000090"
	key := store.key(userID, generationID)
	preparePlatformGenerationTestKey(t, client, key)
	malformed := `{"generation_id":"` + generationID + `","mode":1,"models":["a"],"state":1,"model_states":{},"created_at_ms":1000,"updated_at_ms":1000}`
	if err := client.Set(ctx, key, malformed, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelOrCreate(ctx, userID, generationID, 1001); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("malformed CancelOrCreate error=%v, want invalid", err)
	}
	if raw, err := client.Get(ctx, key).Result(); err != nil || raw != malformed {
		t.Fatalf("malformed record mutated: raw=%s error=%v", raw, err)
	}

	disconnected, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = disconnected.Close() }()
	_, err = disconnected.CancelOrCreate(ctx, userID, generationID, 1001)
	if err != ErrPlatformGenerationUnavailable || err.Error() != "Redis generation store is unavailable" {
		t.Fatalf("disconnected error=%q, want exact sanitized sentinel", err)
	}
}

func TestPlatformGenerationCancelConvergesAtThirtySecondBoundary(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	const userID int64 = 930100
	const generationID = "86000000-0000-4000-8000-000000000100"
	snapshot := PlatformGenerationSnapshot{
		GenerationID: generationID, Mode: PlatformGenerationModeCompare, Models: []string{"done", "failed", "running"}, State: PlatformGenerationStateCancelling,
		ModelStates: map[string]PlatformGenerationModel{
			"done":    {Seq: 2, State: PlatformGenerationStateCompleted},
			"failed":  {Seq: 1, State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
			"running": {Seq: 3, State: PlatformGenerationStateRunning},
		},
		CreatedAtMillis: 1000, UpdatedAtMillis: 2000,
	}
	key := store.key(userID, generationID)
	preparePlatformGenerationTestKey(t, client, key)
	raw, err := encodePlatformGeneration(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, key, raw, platformGenerationTTL).Err(); err != nil {
		t.Fatal(err)
	}
	before := requirePositivePlatformGenerationTTL(t, client, key)
	authoritative, err := store.ConvergeStaleCancelling(ctx, userID, generationID, 31999)
	if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, snapshot) {
		t.Fatalf("29999ms authority=%#v error=%v", authoritative, err)
	}
	converged, err := store.ConvergeStaleCancelling(ctx, userID, generationID, 32000)
	if err != nil || converged.State != PlatformGenerationStateCancelled || converged.UpdatedAtMillis != 32000 {
		t.Fatalf("30000ms convergence=%#v error=%v", converged, err)
	}
	if converged.ModelStates["done"] != snapshot.ModelStates["done"] || converged.ModelStates["failed"] != snapshot.ModelStates["failed"] || converged.ModelStates["running"].State != PlatformGenerationStateCancelled {
		t.Fatalf("converged models=%#v", converged.ModelStates)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, before)
}

func TestPlatformGenerationScanContinuesCursorAndStrictlyParsesKeys(t *testing.T) {
	_, fixtureClient := openTestPlatformGenerationStore(t)
	options := *fixtureClient.Options()
	options.DB = 15
	client := redis.NewClient(&options)
	store, err := NewPlatformGenerationStore(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	valid := make(map[PlatformGenerationIdentity]struct{})
	keys := make([]string, 0, 80)
	for index := 1; index <= 64; index++ {
		identity := PlatformGenerationIdentity{UserID: 940000 + int64(index), GenerationID: fmt.Sprintf("87000000-0000-4000-8000-%012x", index)}
		valid[identity] = struct{}{}
		keys = append(keys, store.key(identity.UserID, identity.GenerationID))
	}
	malformed := []string{
		platformGenerationPrefix + "0:" + generationTestID,
		platformGenerationPrefix + "+1:" + generationTestID,
		platformGenerationPrefix + "-1:" + generationTestID,
		platformGenerationPrefix + "01:" + generationTestID,
		platformGenerationPrefix + "9223372036854775808:" + generationTestID,
		platformGenerationPrefix + "1:" + generationTestID + ":extra",
		platformGenerationPrefix + "1:" + strings.ToUpper(generationTestID),
		platformGenerationPrefix + "1:not-a-uuid",
	}
	keys = append(keys, malformed...)
	unrelated := "porsche:platform:generation:v20:1:" + generationTestID
	keys = append(keys, unrelated)
	if err := client.MSet(ctx, func() []any {
		values := make([]any, 0, len(keys)*2)
		for _, key := range keys {
			values = append(values, key, "sentinel")
		}
		return values
	}()...).Err(); err != nil {
		t.Fatal(err)
	}
	untouchedTTLs := make(map[string]time.Duration, len(malformed)+1)
	for _, key := range append(malformed, unrelated) {
		ttl, err := client.PTTL(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		untouchedTTLs[key] = ttl
	}
	t.Cleanup(func() {
		if err := client.Del(context.Background(), keys...).Err(); err != nil {
			t.Errorf("scan key cleanup: %v", err)
		}
	})

	found := make(map[PlatformGenerationIdentity]struct{})
	cursor := uint64(0)
	calls := 0
	for {
		identities, next, err := store.ScanGenerationKeys(ctx, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		calls++
		for _, identity := range identities {
			if _, ok := valid[identity]; !ok {
				t.Fatalf("scan returned identity outside expected set: %#v", identity)
			}
			found[identity] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			break
		}
		if calls > 10000 {
			t.Fatal("SCAN cursor did not terminate")
		}
	}
	if calls < 2 || len(found) != len(valid) {
		t.Fatalf("scan calls=%d identities=%d want=%d", calls, len(found), len(valid))
	}
	for _, key := range append(malformed, unrelated) {
		if raw, err := client.Get(ctx, key).Result(); err != nil || raw != "sentinel" {
			t.Fatalf("malformed key was touched: key=%q raw=%q error=%v", key, raw, err)
		}
		if ttl, err := client.PTTL(ctx, key).Result(); err != nil || ttl != untouchedTTLs[key] {
			t.Fatalf("malformed key TTL changed: key=%q before=%v after=%v error=%v", key, untouchedTTLs[key], ttl, err)
		}
	}
}

func TestPlatformGenerationScanValidatesCountBeforeRedisAndSanitizesErrors(t *testing.T) {
	disconnected, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = disconnected.Close() }()
	for _, count := range []int64{0, -1, 1001} {
		identities, next, err := disconnected.ScanGenerationKeys(context.Background(), 91, count)
		if !errors.Is(err, ErrPlatformGenerationInvalid) || identities != nil || next != 91 {
			t.Fatalf("count=%d identities=%#v next=%d error=%v", count, identities, next, err)
		}
	}
	_, next, err := disconnected.ScanGenerationKeys(context.Background(), 91, 1)
	if err != ErrPlatformGenerationUnavailable || err.Error() != "Redis generation store is unavailable" || next != 91 {
		t.Fatalf("disconnected next=%d error=%q, want exact sanitized sentinel", next, err)
	}
}

func TestPlatformGenerationLeaseAndExpiryControlsFailClosedWithoutMutation(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	token := base64.RawURLEncoding.EncodeToString(make([]byte, platformGenerationLeaseBytes))
	digest := platformGenerationLeaseDigest(token)
	tests := []struct {
		name         string
		userID       int64
		generationID string
		raw          string
		call         func(int64, string) error
	}{
		{
			name: "renew malformed shape", userID: 950001, generationID: "88000000-0000-4000-8000-000000000001",
			raw: `{"generation_id":"88000000-0000-4000-8000-000000000001","mode":1,"models":["a"],"state":1,"model_states":{},"created_at_ms":1000,"updated_at_ms":1000,"lease_owner_sha256":"` + digest + `","lease_until_ms":31000}`,
			call: func(userID int64, generationID string) error {
				_, err := store.RenewLease(ctx, userID, generationID, token, 2000)
				return err
			},
		},
		{
			name: "expire half lease", userID: 950002, generationID: "88000000-0000-4000-8000-000000000002",
			raw: `{"generation_id":"88000000-0000-4000-8000-000000000002","mode":1,"models":["a"],"state":1,"model_states":{"a":{"seq":0,"state":1}},"created_at_ms":1000,"updated_at_ms":1000,"lease_owner_sha256":"` + digest + `"}`,
			call: func(userID int64, generationID string) error {
				_, err := store.FailExpiredRunning(ctx, userID, generationID, 31000)
				return err
			},
		},
		{
			name: "converge malformed models", userID: 950003, generationID: "88000000-0000-4000-8000-000000000003",
			raw: `{"generation_id":"88000000-0000-4000-8000-000000000003","mode":1,"models":["a"],"state":2,"model_states":{},"created_at_ms":1000,"updated_at_ms":1000}`,
			call: func(userID int64, generationID string) error {
				_, err := store.ConvergeStaleCancelling(ctx, userID, generationID, 31000)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := store.key(test.userID, test.generationID)
			preparePlatformGenerationTestKey(t, client, key)
			if err := client.Set(ctx, key, test.raw, time.Hour).Err(); err != nil {
				t.Fatal(err)
			}
			if err := test.call(test.userID, test.generationID); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("control error=%v, want invalid", err)
			}
			if stored, err := client.Get(ctx, key).Result(); err != nil || stored != test.raw {
				t.Fatalf("malformed record mutated: raw=%s error=%v", stored, err)
			}
		})
	}
}

func TestPlatformGenerationCancelClaimDoesNotEnrichUnsafePristineTombstone(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	input := PlatformGenerationClaimInput{UserID: 950010, GenerationID: "88000000-0000-4000-8000-000000000010", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	unsafe := fmt.Sprintf(`{"generation_id":"%s","models":[],"state":%d,"model_states":{},"created_at_ms":9007199254740992,"updated_at_ms":9007199254740992}`, input.GenerationID, PlatformGenerationStateCancelled)
	if err := client.Set(ctx, key, unsafe, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, input); !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("unsafe tombstone Claim error=%v, want invalid", err)
	}
	if stored, err := client.Get(ctx, key).Result(); err != nil || stored != unsafe {
		t.Fatalf("unsafe tombstone enriched: raw=%s error=%v", stored, err)
	}
}

func TestPlatformGenerationStoreControlRedisErrorsAreExactSanitizedSentinels(t *testing.T) {
	disconnected, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = disconnected.Close() }()
	ctx := context.Background()
	token := base64.RawURLEncoding.EncodeToString(make([]byte, platformGenerationLeaseBytes))
	checks := []struct {
		name string
		call func() error
	}{
		{"claim", func() error {
			_, err := disconnected.Claim(ctx, PlatformGenerationClaimInput{UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1})
			return err
		}},
		{"renew", func() error {
			_, err := disconnected.RenewLease(ctx, 1, generationTestID, token, 1)
			return err
		}},
		{"expire", func() error {
			_, err := disconnected.FailExpiredRunning(ctx, 1, generationTestID, 1)
			return err
		}},
		{"converge", func() error {
			_, err := disconnected.ConvergeStaleCancelling(ctx, 1, generationTestID, 1)
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err != ErrPlatformGenerationUnavailable || err.Error() != "Redis generation store is unavailable" {
				t.Fatalf("error=%q, want exact sanitized sentinel", err)
			}
		})
	}
}

func TestPlatformGenerationCancelPreservesMaximumSafeIntegersExactly(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	const userID int64 = 950020
	const generationID = "88000000-0000-4000-8000-000000000020"
	snapshot := PlatformGenerationSnapshot{
		GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, State: PlatformGenerationStateRunning,
		ModelStates:     map[string]PlatformGenerationModel{"a": {Seq: platformSSEV2MaxSafeInteger, State: PlatformGenerationStateRunning}},
		CreatedAtMillis: platformSSEV2MaxSafeInteger - 1, UpdatedAtMillis: platformSSEV2MaxSafeInteger - 1,
	}
	key := store.key(userID, generationID)
	preparePlatformGenerationTestKey(t, client, key)
	raw, err := encodePlatformGeneration(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, key, raw, platformGenerationTTL).Err(); err != nil {
		t.Fatal(err)
	}
	decision, err := store.CancelOrCreate(ctx, userID, generationID, platformSSEV2MaxSafeInteger)
	if err != nil || !decision.Transitioned || decision.Snapshot.UpdatedAtMillis != platformSSEV2MaxSafeInteger || decision.Snapshot.ModelStates["a"].Seq != platformSSEV2MaxSafeInteger {
		t.Fatalf("max-safe cancellation=%#v error=%v", decision, err)
	}
}

func TestPlatformGenerationLeaseRenewalAndCancelRejectClockRegression(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	input := PlatformGenerationClaimInput{UserID: 950030, GenerationID: "88000000-0000-4000-8000-000000000030", Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
	key := store.key(input.UserID, input.GenerationID)
	preparePlatformGenerationTestKey(t, client, key)
	claim := claimTestGeneration(t, store, input)
	renewed, err := store.RenewLease(ctx, input.UserID, input.GenerationID, claim.LeaseToken, 2000)
	if err != nil || renewed.LeaseUntilMillis != 32000 || renewed.UpdatedAtMillis != 1000 {
		t.Fatalf("initial renewal=%#v error=%v", renewed, err)
	}
	rawAfterRenewal, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	ttlAfterRenewal := requirePositivePlatformGenerationTTL(t, client, key)

	authoritative, err := store.RenewLease(ctx, input.UserID, input.GenerationID, claim.LeaseToken, 1500)
	if !errors.Is(err, ErrPlatformGenerationConflict) || !reflect.DeepEqual(authoritative, renewed) {
		t.Fatalf("regressed renewal authority=%#v error=%v", authoritative, err)
	}
	if raw, err := client.Get(ctx, key).Result(); err != nil || raw != rawAfterRenewal {
		t.Fatalf("regressed renewal mutated raw=%s error=%v", raw, err)
	}
	ttlAfterRejectedRenewal := requirePlatformGenerationTTLNotIncreased(t, client, key, ttlAfterRenewal)

	decision, err := store.CancelOrCreate(ctx, input.UserID, input.GenerationID, 1500)
	if !errors.Is(err, ErrPlatformGenerationConflict) || decision.CreatedTombstone || decision.Transitioned || !reflect.DeepEqual(decision.Snapshot, renewed) {
		t.Fatalf("regressed cancel decision=%#v error=%v", decision, err)
	}
	if raw, err := client.Get(ctx, key).Result(); err != nil || raw != rawAfterRenewal {
		t.Fatalf("regressed cancel mutated raw=%s error=%v", raw, err)
	}
	requirePlatformGenerationTTLNotIncreased(t, client, key, ttlAfterRejectedRenewal)

	decision, err = store.CancelOrCreate(ctx, input.UserID, input.GenerationID, 2000)
	if err != nil || decision.CreatedTombstone || !decision.Transitioned || decision.Snapshot.State != PlatformGenerationStateCancelling || decision.Snapshot.UpdatedAtMillis != 2000 || decision.Snapshot.LeaseUntilMillis != 0 {
		t.Fatalf("boundary cancel decision=%#v error=%v", decision, err)
	}
	decision, err = store.CancelOrCreate(ctx, input.UserID, input.GenerationID, 2001)
	if err != nil || decision.CreatedTombstone || decision.Transitioned || decision.Snapshot.State != PlatformGenerationStateCancelling || decision.Snapshot.UpdatedAtMillis != 2000 {
		t.Fatalf("idempotent cancel decision=%#v error=%v", decision, err)
	}
}

func TestPlatformGenerationStoreDecodeRejectsDuplicateObjectMembersRecursively(t *testing.T) {
	digest := strings.Repeat("a", 64)
	running := `{"generation_id":"` + generationTestID + `","mode":1,"models":["a"],"state":1,"model_states":{"a":{"seq":0,"state":1}},"created_at_ms":1000,"updated_at_ms":1000,"lease_owner_sha256":"` + digest + `","lease_until_ms":31000}`
	failedNested := `{"generation_id":"` + generationTestID + `","mode":2,"models":["a","b"],"state":1,"model_states":{"a":{"seq":0,"state":6,"error_code":"timeout","error_code":"timeout"},"b":{"seq":0,"state":1}},"created_at_ms":1000,"updated_at_ms":1000,"lease_owner_sha256":"` + digest + `","lease_until_ms":31000}`
	completedNested := `{"generation_id":"` + generationTestID + `","mode":1,"models":["a"],"state":5,"model_states":{"a":{"seq":0,"state":5,"assistant_message_guid":"900000000000000001","assistant_message_guid":"900000000000000001"}},"created_at_ms":1000,"updated_at_ms":1001}`
	tests := map[string]string{
		"top state":          strings.Replace(running, `"state":1`, `"state":1,"state":1`, 1),
		"top mode":           strings.Replace(running, `"mode":1`, `"mode":1,"mode":1`, 1),
		"top lease owner":    strings.Replace(running, `"lease_owner_sha256":"`+digest+`"`, `"lease_owner_sha256":"`+digest+`","lease_owner_sha256":"`+digest+`"`, 1),
		"top lease deadline": strings.Replace(running, `"lease_until_ms":31000`, `"lease_until_ms":31000,"lease_until_ms":31000`, 1),
		"nested seq":         strings.Replace(running, `"seq":0`, `"seq":0,"seq":0`, 1),
		"nested state":       strings.Replace(running, `"seq":0,"state":1`, `"seq":0,"state":1,"state":1`, 1),
		"nested error":       failedNested,
		"nested guid":        completedNested,
		"model state key":    strings.Replace(running, `"model_states":{"a":`, `"model_states":{"a":{"seq":0,"state":1},"a":`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePlatformGeneration(raw); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("duplicate member accepted: raw=%s error=%v", raw, err)
			}
		})
	}
}

func TestPlatformGenerationStoreDuplicateMemberRecordsRemainUntouchedByMutators(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	token := base64.RawURLEncoding.EncodeToString(make([]byte, platformGenerationLeaseBytes))
	digest := platformGenerationLeaseDigest(token)
	type duplicateCase struct {
		name string
		raw  func(string) string
	}
	running := func(generationID string) string {
		return `{"generation_id":"` + generationID + `","mode":1,"models":["a"],"state":1,"model_states":{"a":{"seq":0,"state":1}},"created_at_ms":1000,"updated_at_ms":1000,"lease_owner_sha256":"` + digest + `","lease_until_ms":31000}`
	}
	cases := []duplicateCase{
		{"top state", func(id string) string { return strings.Replace(running(id), `"state":1`, `"state":1,"state":1`, 1) }},
		{"top mode", func(id string) string { return strings.Replace(running(id), `"mode":1`, `"mode":1,"mode":1`, 1) }},
		{"top lease owner", func(id string) string {
			return strings.Replace(running(id), `"lease_owner_sha256":"`+digest+`"`, `"lease_owner_sha256":"`+digest+`","lease_owner_sha256":"`+digest+`"`, 1)
		}},
		{"top lease deadline", func(id string) string {
			return strings.Replace(running(id), `"lease_until_ms":31000`, `"lease_until_ms":31000,"lease_until_ms":31000`, 1)
		}},
		{"nested seq", func(id string) string { return strings.Replace(running(id), `"seq":0`, `"seq":0,"seq":0`, 1) }},
		{"nested state", func(id string) string {
			return strings.Replace(running(id), `"seq":0,"state":1`, `"seq":0,"state":1,"state":1`, 1)
		}},
		{"nested error", func(id string) string {
			return `{"generation_id":"` + id + `","mode":2,"models":["a","b"],"state":1,"model_states":{"a":{"seq":0,"state":6,"error_code":"timeout","error_code":"timeout"},"b":{"seq":0,"state":1}},"created_at_ms":1000,"updated_at_ms":1000,"lease_owner_sha256":"` + digest + `","lease_until_ms":31000}`
		}},
		{"nested guid", func(id string) string {
			return `{"generation_id":"` + id + `","mode":1,"models":["a"],"state":5,"model_states":{"a":{"seq":0,"state":5,"assistant_message_guid":"900000000000000001","assistant_message_guid":"900000000000000001"}},"created_at_ms":1000,"updated_at_ms":1001}`
		}},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			generationID := fmt.Sprintf("89000000-0000-4000-8000-%012x", index+1)
			userID := int64(960000 + index)
			key := store.key(userID, generationID)
			preparePlatformGenerationTestKey(t, client, key)
			raw := test.raw(generationID)
			if err := client.Set(ctx, key, raw, time.Hour).Err(); err != nil {
				t.Fatal(err)
			}
			for _, mutation := range []struct {
				name string
				call func() error
			}{
				{"cancel", func() error { _, err := store.CancelOrCreate(ctx, userID, generationID, 2000); return err }},
				{"claim", func() error {
					_, err := store.Claim(ctx, PlatformGenerationClaimInput{UserID: userID, GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 2000})
					return err
				}},
				{"renew", func() error { _, err := store.RenewLease(ctx, userID, generationID, token, 2000); return err }},
				{"expire", func() error { _, err := store.FailExpiredRunning(ctx, userID, generationID, 31000); return err }},
				{"converge", func() error { _, err := store.ConvergeStaleCancelling(ctx, userID, generationID, 31000); return err }},
			} {
				beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)
				if err := mutation.call(); !errors.Is(err, ErrPlatformGenerationInvalid) {
					t.Fatalf("%s error=%v, want invalid", mutation.name, err)
				}
				if stored, err := client.Get(ctx, key).Result(); err != nil || stored != raw {
					t.Fatalf("%s mutated duplicate record: raw=%s error=%v", mutation.name, stored, err)
				}
				requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
			}
		})
	}
}

func TestPlatformGenerationCancelClaimRaceOnMissingKeyConvergesToOneLegalAuthority(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	for iteration := 0; iteration < 24; iteration++ {
		generationID := fmt.Sprintf("8a000000-0000-4000-8000-%012x", iteration+1)
		userID := int64(970000 + iteration)
		key := store.key(userID, generationID)
		preparePlatformGenerationTestKey(t, client, key)
		input := PlatformGenerationClaimInput{UserID: userID, GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
		start := make(chan struct{})
		type claimOutcome struct {
			result PlatformGenerationClaimResult
			err    error
		}
		type cancelOutcome struct {
			decision PlatformGenerationCancelDecision
			err      error
		}
		claims := make(chan claimOutcome, 1)
		cancels := make(chan cancelOutcome, 1)
		go func() {
			<-start
			result, err := store.Claim(ctx, input)
			claims <- claimOutcome{result, err}
		}()
		go func() {
			<-start
			decision, err := store.CancelOrCreate(ctx, userID, generationID, 1000)
			cancels <- cancelOutcome{decision, err}
		}()
		close(start)
		claim := <-claims
		cancel := <-cancels
		converged, convergeErr := store.CancelOrCreate(ctx, userID, generationID, 1000)
		if convergeErr != nil {
			t.Fatalf("iteration %d convergence error=%v", iteration, convergeErr)
		}
		final, err := store.Get(ctx, userID, generationID)
		if err != nil || !reflect.DeepEqual(converged.Snapshot, final) {
			t.Fatalf("iteration %d final=%#v convergence=%#v error=%v", iteration, final, converged, err)
		}
		switch {
		case claim.err == nil:
			if claim.result.Duplicate || claim.result.LeaseToken == "" || claim.result.Snapshot.State != PlatformGenerationStateRunning || final.State != PlatformGenerationStateCancelling {
				t.Fatalf("iteration %d running claim=%#v final=%#v", iteration, claim, final)
			}
			if errors.Is(cancel.err, ErrPlatformGenerationConflict) && (!reflect.DeepEqual(cancel.decision.Snapshot, claim.result.Snapshot) || cancel.decision.CreatedTombstone || cancel.decision.Transitioned) {
				t.Fatalf("iteration %d cancel conflict=%#v running authority=%#v", iteration, cancel, claim.result.Snapshot)
			}
		case errors.Is(claim.err, ErrPlatformGenerationConflict):
			if !claim.result.Duplicate || claim.result.LeaseToken != "" || claim.result.Snapshot.State != PlatformGenerationStateCancelled || !reflect.DeepEqual(claim.result.Snapshot, final) {
				t.Fatalf("iteration %d cancelled claim=%#v final=%#v", iteration, claim, final)
			}
			if !cancel.decision.CreatedTombstone {
				t.Fatalf("iteration %d cancelled authority without tombstone creator: cancel=%#v", iteration, cancel)
			}
		default:
			t.Fatalf("iteration %d unexpected claim=%#v", iteration, claim)
		}
		if cancel.err != nil && !errors.Is(cancel.err, ErrPlatformGenerationConflict) {
			t.Fatalf("iteration %d cancel=%#v", iteration, cancel)
		}
		if cancel.decision.CreatedTombstone {
			if cancel.err != nil || cancel.decision.Transitioned || cancel.decision.Snapshot.Mode != 0 || final.State != PlatformGenerationStateCancelled {
				t.Fatalf("iteration %d created cancel=%#v final=%#v", iteration, cancel, final)
			}
		} else if cancel.decision.Transitioned {
			if cancel.err != nil || !reflect.DeepEqual(cancel.decision.Snapshot, final) {
				t.Fatalf("iteration %d transitioned cancel=%#v final=%#v", iteration, cancel, final)
			}
		}
	}
}

func TestPlatformGenerationCancelVersusRenewConvergesWithoutLeaseRegression(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	for iteration := 0; iteration < 24; iteration++ {
		generationID := fmt.Sprintf("8b000000-0000-4000-8000-%012x", iteration+1)
		userID := int64(971000 + iteration)
		key := store.key(userID, generationID)
		preparePlatformGenerationTestKey(t, client, key)
		input := PlatformGenerationClaimInput{UserID: userID, GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000}
		claim := claimTestGeneration(t, store, input)
		beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)
		start := make(chan struct{})
		type snapshotOutcome struct {
			snapshot PlatformGenerationSnapshot
			err      error
		}
		type cancelOutcome struct {
			decision PlatformGenerationCancelDecision
			err      error
		}
		renews := make(chan snapshotOutcome, 1)
		cancels := make(chan cancelOutcome, 1)
		go func() {
			<-start
			snapshot, err := store.RenewLease(ctx, userID, generationID, claim.LeaseToken, 2000)
			renews <- snapshotOutcome{snapshot, err}
		}()
		go func() {
			<-start
			decision, err := store.CancelOrCreate(ctx, userID, generationID, 2000)
			cancels <- cancelOutcome{decision, err}
		}()
		close(start)
		renew := <-renews
		cancel := <-cancels
		if renew.err == nil {
			if renew.snapshot.State != PlatformGenerationStateRunning || renew.snapshot.LeaseUntilMillis != 32000 {
				t.Fatalf("iteration %d renew=%#v", iteration, renew)
			}
			if errors.Is(cancel.err, ErrPlatformGenerationConflict) && !reflect.DeepEqual(cancel.decision.Snapshot, renew.snapshot) {
				t.Fatalf("iteration %d cancel CAS authority=%#v renew=%#v", iteration, cancel, renew)
			}
		} else if !errors.Is(renew.err, ErrPlatformGenerationConflict) {
			t.Fatalf("iteration %d unexpected renew=%#v", iteration, renew)
		}
		if errors.Is(cancel.err, ErrPlatformGenerationConflict) && (cancel.decision.CreatedTombstone || cancel.decision.Transitioned) {
			t.Fatalf("iteration %d cancel conflict flags=%#v", iteration, cancel)
		}
		if cancel.err == nil && (!cancel.decision.Transitioned || cancel.decision.CreatedTombstone) {
			t.Fatalf("iteration %d cancel success flags=%#v", iteration, cancel)
		}
		converged, err := store.CancelOrCreate(ctx, userID, generationID, 2000)
		if err != nil {
			t.Fatalf("iteration %d convergence error=%v", iteration, err)
		}
		final, err := store.Get(ctx, userID, generationID)
		if err != nil || final.State != PlatformGenerationStateCancelling || final.LeaseOwnerSHA256 != "" || final.LeaseUntilMillis != 0 || final.UpdatedAtMillis != 2000 || !reflect.DeepEqual(converged.Snapshot, final) {
			t.Fatalf("iteration %d final=%#v convergence=%#v error=%v", iteration, final, converged, err)
		}
		if errors.Is(renew.err, ErrPlatformGenerationConflict) && !reflect.DeepEqual(renew.snapshot, final) {
			t.Fatalf("iteration %d renew loser=%#v final=%#v", iteration, renew, final)
		}
		if cancel.err == nil && cancel.decision.Transitioned && !reflect.DeepEqual(cancel.decision.Snapshot, final) {
			t.Fatalf("iteration %d cancel winner=%#v final=%#v", iteration, cancel, final)
		}
		requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
	}
}

func TestPlatformGenerationExpiredMultiCallerHasOneWinnerAndOneAuthority(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	for iteration := 0; iteration < 12; iteration++ {
		generationID := fmt.Sprintf("8c000000-0000-4000-8000-%012x", iteration+1)
		userID := int64(972000 + iteration)
		key := store.key(userID, generationID)
		preparePlatformGenerationTestKey(t, client, key)
		claimTestGeneration(t, store, PlatformGenerationClaimInput{UserID: userID, GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000})
		beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)
		start := make(chan struct{})
		type outcome struct {
			snapshot PlatformGenerationSnapshot
			err      error
		}
		results := make(chan outcome, 8)
		for range 8 {
			go func() {
				<-start
				snapshot, err := store.FailExpiredRunning(ctx, userID, generationID, 31000)
				results <- outcome{snapshot, err}
			}()
		}
		close(start)
		all := make([]outcome, 0, 8)
		for range 8 {
			all = append(all, <-results)
		}
		final, err := store.Get(ctx, userID, generationID)
		if err != nil || final.State != PlatformGenerationStateFailed {
			t.Fatalf("iteration %d final=%#v error=%v", iteration, final, err)
		}
		winners := 0
		for _, result := range all {
			if result.err == nil {
				winners++
			} else if !errors.Is(result.err, ErrPlatformGenerationConflict) {
				t.Fatalf("iteration %d result=%#v", iteration, result)
			}
			if !reflect.DeepEqual(result.snapshot, final) {
				t.Fatalf("iteration %d result=%#v final=%#v", iteration, result, final)
			}
		}
		if winners != 1 {
			t.Fatalf("iteration %d winners=%d", iteration, winners)
		}
		requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
	}
}

func TestPlatformGenerationCancelConvergenceMultiCallerHasOneWinnerAndOneAuthority(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	ctx := context.Background()
	for iteration := 0; iteration < 12; iteration++ {
		generationID := fmt.Sprintf("8d000000-0000-4000-8000-%012x", iteration+1)
		userID := int64(973000 + iteration)
		key := store.key(userID, generationID)
		preparePlatformGenerationTestKey(t, client, key)
		claimTestGeneration(t, store, PlatformGenerationClaimInput{UserID: userID, GenerationID: generationID, Mode: PlatformGenerationModeSingle, Models: []string{"a"}, NowMillis: 1000})
		if _, err := store.CancelOrCreate(ctx, userID, generationID, 1001); err != nil {
			t.Fatal(err)
		}
		beforeTTL := requirePositivePlatformGenerationTTL(t, client, key)
		start := make(chan struct{})
		type outcome struct {
			snapshot PlatformGenerationSnapshot
			err      error
		}
		results := make(chan outcome, 8)
		for range 8 {
			go func() {
				<-start
				snapshot, err := store.ConvergeStaleCancelling(ctx, userID, generationID, 31001)
				results <- outcome{snapshot, err}
			}()
		}
		close(start)
		all := make([]outcome, 0, 8)
		for range 8 {
			all = append(all, <-results)
		}
		final, err := store.Get(ctx, userID, generationID)
		if err != nil || final.State != PlatformGenerationStateCancelled {
			t.Fatalf("iteration %d final=%#v error=%v", iteration, final, err)
		}
		winners := 0
		for _, result := range all {
			if result.err == nil {
				winners++
			} else if !errors.Is(result.err, ErrPlatformGenerationConflict) {
				t.Fatalf("iteration %d result=%#v", iteration, result)
			}
			if !reflect.DeepEqual(result.snapshot, final) {
				t.Fatalf("iteration %d result=%#v final=%#v", iteration, result, final)
			}
		}
		if winners != 1 {
			t.Fatalf("iteration %d winners=%d", iteration, winners)
		}
		requirePlatformGenerationTTLNotIncreased(t, client, key, beforeTTL)
	}
}
