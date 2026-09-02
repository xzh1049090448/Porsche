package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/redis/go-redis/v9"
)

// TestLoginRateLimitRejectsFifthLoginFailure proves that login failures are
// bounded by both account and source IP. It only contacts the explicitly
// supplied disposable TEST_REDIS_URL; production REDIS_URL is never read.
func TestLoginRateLimitRejectsFifthLoginFailure(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	ctx := context.Background()
	var identity [16]byte
	if _, err := rand.Read(identity[:]); err != nil {
		t.Fatal(err)
	}
	username := "rate-limit-user-" + hex.EncodeToString(identity[:])
	ip := net.IP(identity[:]).String()

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

// TestAuthRedisPendingRotationCanBeRecoveredAfterPublishFailure pins the
// recoverable publish protocol: a post-commit publication failure must not
// turn a valid concurrent old cookie into a replay revocation.
func TestAuthRedisPendingRotationCanBeRecoveredAfterPublishFailure(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	ctx := context.Background()
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := redisStore.StorePendingRotation(ctx, sid, "target-hmac", sid+".new-refresh-secret", time.Second); err != nil {
		t.Fatal(err)
	}
	result, found, err := redisStore.RecoverRotationResult(ctx, sid, "target-hmac", time.Second)
	if err != nil || !found || result != sid+".new-refresh-secret" {
		t.Fatalf("recover pending rotation = %q, %t, %v", result, found, err)
	}
}

// TestAuthRedisGenerationNeverReturnsStaleRotationResult covers A→B→C while
// B remains within TTL: an old B public entry must be atomically replaced with
// C and a B-cookie concurrent refresh must receive C, never stale B.
func TestAuthRedisGenerationNeverReturnsStaleRotationResult(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	ctx := context.Background()
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := redisStore.StorePendingRotation(ctx, sid, "hmac-b", sid+".B", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, found, err := redisStore.RecoverRotationResult(ctx, sid, "hmac-b", time.Second); err != nil || !found {
		t.Fatalf("publish B: found=%t err=%v", found, err)
	}
	if err := redisStore.StorePendingRotation(ctx, sid, "hmac-c", sid+".C", time.Second); err != nil {
		t.Fatal(err)
	}
	result, found, err := redisStore.RecoverRotationResult(ctx, sid, "hmac-c", time.Second)
	if err != nil || !found || result != sid+".C" {
		t.Fatalf("recover C = %q, %t, %v", result, found, err)
	}
}
