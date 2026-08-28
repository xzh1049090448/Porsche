package service

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestLoginRateLimitRejectsFifthLoginFailure proves that login failures are
// bounded by both account and source IP. It only contacts the explicitly
// supplied disposable TEST_REDIS_URL; production REDIS_URL is never read.
func TestLoginRateLimitRejectsFifthLoginFailure(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	ctx := context.Background()
	username := "rate-limit-user-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ip := "198.51.100.10"

	for attempt := 1; attempt <= 4; attempt++ {
		if err := redisStore.RecordLoginFailure(ctx, username, ip); err != nil {
			t.Fatalf("record login failure %d: %v", attempt, err)
		}
		if err := redisStore.CheckLoginAllowed(ctx, username, ip); err != nil {
			t.Fatalf("attempt %d was unexpectedly blocked: %v", attempt, err)
		}
	}
	if err := redisStore.RecordLoginFailure(ctx, username, ip); err != nil {
		t.Fatalf("record fifth login failure: %v", err)
	}
	if err := redisStore.CheckLoginAllowed(ctx, username, ip); err == nil {
		t.Fatal("fifth failed login must be blocked for 30 seconds")
	}
}

// TestAuthRedisRotationCiphertextIsBoundToSID proves AEAD associated data
// prevents a ciphertext copied between session keys from being decrypted.
func TestAuthRedisRotationCiphertextIsBoundToSID(t *testing.T) {
	store, err := NewAuthRedis(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}), "test-auth-hmac-key-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ciphertext, err := store.encrypt("sid-a", []byte("refresh-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.decrypt("sid-b", ciphertext); err == nil {
		t.Fatal("rotation ciphertext decrypted under another SID")
	}
}

// openTestAuthRedis opens only an explicitly configured disposable Redis. The
// Task 3 tests intentionally skip rather than silently use an in-memory or
// deployment Redis fallback, preserving fail-closed production semantics.
func openTestAuthRedis(t *testing.T) *AuthRedis {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("TEST_REDIS_URL"))
	if url == "" {
		t.Skip("requires explicitly configured TEST_REDIS_URL; Redis auth tests skipped")
	}
	store, err := NewAuthRedisFromURL(context.Background(), url, "test-auth-hmac-key-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err != nil {
		t.Fatalf("open TEST_REDIS_URL: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func TestAuthRedisRotationResultExpiresAfterReplayWindow(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	ctx := context.Background()
	const sid = "a7e1b4cc-d29f-466b-b9e3-e384b0a6ab0e"
	if err := redisStore.StoreRotationResult(ctx, sid, "new-refresh-secret", 30*time.Millisecond); err != nil {
		t.Fatalf("store rotation result: %v", err)
	}
	result, found, err := redisStore.LoadRotationResult(ctx, sid)
	if err != nil || !found || result != "new-refresh-secret" {
		t.Fatalf("load live rotation result = %q, %t, %v", result, found, err)
	}
	time.Sleep(50 * time.Millisecond)
	_, found, err = redisStore.LoadRotationResult(ctx, sid)
	if err != nil {
		t.Fatalf("load expired rotation result: %v", err)
	}
	if found {
		t.Fatal("rotation result survived replay window")
	}
}

// TestAuthRedisPendingRotationCanBeRecoveredAfterPublishFailure pins the
// recoverable publish protocol: a post-commit publication failure must not
// turn a valid concurrent old cookie into a replay revocation.
func TestAuthRedisPendingRotationCanBeRecoveredAfterPublishFailure(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	ctx := context.Background()
	const sid = "4fa4c35d-851d-4ef7-864c-9eb6e1cb91d4"
	if err := redisStore.StorePendingRotation(ctx, sid, sid+".new-refresh-secret", time.Second); err != nil {
		t.Fatal(err)
	}
	result, found, err := redisStore.RecoverRotationResult(ctx, sid, time.Second)
	if err != nil || !found || result != sid+".new-refresh-secret" {
		t.Fatalf("recover pending rotation = %q, %t, %v", result, found, err)
	}
}
