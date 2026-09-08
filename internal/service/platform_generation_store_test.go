package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
		if _, _, err := store.Claim(context.Background(), input); !errors.Is(err, ErrPlatformGenerationInvalid) {
			t.Fatalf("Claim(%#v) error=%v, want invalid", input, err)
		}
	}
}

func TestPlatformGenerationStoreRejectsModelOverPersistenceLimitBeforeRedis(t *testing.T) {
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Claim(context.Background(), PlatformGenerationClaimInput{
		UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle,
		Models: []string{strings.Repeat("m", 129)}, NowMillis: 1,
	})
	if !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("Claim() error=%v, want invalid before Redis", err)
	}
}

func TestPlatformGenerationStoreRejectsInvalidUTF8ModelBeforeRedis(t *testing.T) {
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Claim(context.Background(), PlatformGenerationClaimInput{
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

func claimTestGeneration(t *testing.T, store *PlatformGenerationStore, input PlatformGenerationClaimInput) {
	t.Helper()
	if snapshot, duplicate, err := store.Claim(context.Background(), input); err != nil || duplicate || snapshot.GenerationID != input.GenerationID {
		t.Fatalf("claim=%#v duplicate=%v error=%v", snapshot, duplicate, err)
	}
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
			snapshot, duplicate, err := store.Claim(context.Background(), input)
			if (duplicate && !errors.Is(err, ErrPlatformGenerationConflict)) || (!duplicate && err != nil) || snapshot.CreatedAtMillis != input.NowMillis {
				t.Errorf("claim snapshot=%#v error=%v", snapshot, err)
				return
			}
			results <- duplicate
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
	if snapshot, duplicate, err := store.Claim(context.Background(), input); !errors.Is(err, ErrPlatformGenerationConflict) || !duplicate || snapshot.CreatedAtMillis != 1000 {
		t.Fatalf("duplicate snapshot=%#v duplicate=%v error=%v", snapshot, duplicate, err)
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
	if _, duplicate, err := store.Claim(context.Background(), input); !duplicate || !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("duplicate mismatched record duplicate=%v error=%v", duplicate, err)
	}
}
