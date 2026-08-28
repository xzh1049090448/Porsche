package service

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
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
