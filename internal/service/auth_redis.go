package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/hkdf"
)

const (
	loginFailureLimit  = 5
	loginFailureWindow = 30 * time.Second
	authRedisPrefix    = "porsche:auth:v1:"
)

// AuthRedis is the fail-closed Redis boundary for authentication rate limits,
// session revocation barriers, and encrypted concurrent-refresh results.
type AuthRedis struct {
	client redis.UniversalClient
	aead   cipher.AEAD
}

type rotationRecord struct {
	TargetHMAC string `json:"target_hmac"`
	Ciphertext string `json:"ciphertext"`
}

// NewAuthRedisFromURL creates and verifies an AuthRedis client. It is never
// permitted to silently fall back to an in-memory store.
func NewAuthRedisFromURL(ctx context.Context, rawURL, hmacKey string) (*AuthRedis, error) {
	options, err := redis.ParseURL(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	client := redis.NewClient(options)
	store, err := NewAuthRedis(client, hmacKey)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("verify Redis authentication store: %w", err)
	}
	return store, nil
}

// NewAuthRedis wraps a caller-supplied Redis client with an AEAD key derived
// from the configured authentication HMAC key.
func NewAuthRedis(client redis.UniversalClient, hmacKey string) (*AuthRedis, error) {
	if client == nil {
		return nil, errors.New("Redis authentication client is required")
	}
	if strings.TrimSpace(hmacKey) == "" {
		return nil, errors.New("authentication HMAC key is required")
	}
	// The AEAD key is purpose-separated from the Refresh HMAC key. Rotating or
	// compromising one primitive therefore does not reuse raw key material in
	// the other protocol.
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, []byte(hmacKey), nil, []byte("porsche/auth/redis-rotation-aead/v1")), key); err != nil {
		return nil, fmt.Errorf("derive authentication AEAD key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create authentication cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create authentication AEAD: %w", err)
	}
	return &AuthRedis{client: client, aead: aead}, nil
}

// Close releases the Redis client owned by this store.
func (r *AuthRedis) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}

// CheckAvailable verifies that the mandatory Redis authentication dependency
// is reachable before a credential-changing path mutates MySQL.
func (r *AuthRedis) CheckAvailable(ctx context.Context) error {
	if err := r.requireClient(); err != nil {
		return err
	}
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("verify Redis authentication store: %w", err)
	}
	return nil
}

// CheckLoginAllowed fails closed when either the account or source-IP lock is
// present or Redis cannot be read.
func (r *AuthRedis) CheckLoginAllowed(ctx context.Context, username, ip string) error {
	if err := r.requireClient(); err != nil {
		return err
	}
	keys := []string{r.loginKey("account", username), r.loginKey("ip", ip)}
	values, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return fmt.Errorf("read Redis login lock: %w", err)
	}
	for _, value := range values {
		if value != nil {
			return errTooMany("登录尝试过于频繁，请稍后再试")
		}
	}
	return nil
}

// RecordLoginFailure atomically increments account and IP counters. On the
// fifth failure it writes 30-second locks for both dimensions.
func (r *AuthRedis) RecordLoginFailure(ctx context.Context, username, ip string) error {
	if err := r.requireClient(); err != nil {
		return err
	}
	for _, dimension := range []struct{ kind, value string }{{"account", username}, {"ip", ip}} {
		key := r.failureKey(dimension.kind, dimension.value)
		count, err := r.client.Incr(ctx, key).Result()
		if err != nil {
			return fmt.Errorf("increment Redis login failure counter: %w", err)
		}
		if count == 1 {
			if err := r.client.Expire(ctx, key, loginFailureWindow).Err(); err != nil {
				return fmt.Errorf("expire Redis login failure counter: %w", err)
			}
		}
		if count >= loginFailureLimit {
			if err := r.client.Set(ctx, r.loginKey(dimension.kind, dimension.value), "1", loginFailureWindow).Err(); err != nil {
				return fmt.Errorf("write Redis login lock: %w", err)
			}
		}
	}
	return nil
}

// ClearLoginFailures removes successful-login failure counters. A Redis error
// is returned to the caller so successful authentication never bypasses a
// failed security-store mutation.
func (r *AuthRedis) ClearLoginFailures(ctx context.Context, username, ip string) error {
	if err := r.requireClient(); err != nil {
		return err
	}
	if err := r.client.Del(ctx, r.failureKey("account", username), r.failureKey("ip", ip), r.loginKey("account", username), r.loginKey("ip", ip)).Err(); err != nil {
		return fmt.Errorf("clear Redis login failures: %w", err)
	}
	return nil
}

// ReserveSessionIssue enforces the rolling 24-hour issuance ceiling before a
// database session is created. Consuming a slot on a later MySQL failure is
// intentionally conservative rather than weakening the limit.
func (r *AuthRedis) ReserveSessionIssue(ctx context.Context, userID int64, limit int) error {
	if err := r.requireClient(); err != nil {
		return err
	}
	if limit <= 0 {
		return errors.New("session issue limit must be positive")
	}
	key := fmt.Sprintf("%ssession:issue:%d", authRedisPrefix, userID)
	count, err := r.client.Incr(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("increment Redis session issuance counter: %w", err)
	}
	if count == 1 {
		if err := r.client.Expire(ctx, key, 24*time.Hour).Err(); err != nil {
			return fmt.Errorf("expire Redis session issuance counter: %w", err)
		}
	}
	if count > int64(limit) {
		return errTooMany("24小时内会话签发次数已达上限")
	}
	return nil
}

// StorePendingRotation writes the encrypted new refresh token before the
// caller commits MySQL. It is never returned to another caller until MySQL
// confirms the matching new HMAC, at which point RecoverRotationResult can
// safely publish it after a transient Redis publication failure.
func (r *AuthRedis) StorePendingRotation(ctx context.Context, sid, targetHMAC, refreshToken string, ttl time.Duration) error {
	if err := r.requireClient(); err != nil {
		return err
	}
	if ttl <= 0 {
		return errors.New("pending rotation TTL must be positive")
	}
	ciphertext, err := r.encrypt(sid, []byte(refreshToken))
	if err != nil {
		return err
	}
	value, err := json.Marshal(rotationRecord{TargetHMAC: targetHMAC, Ciphertext: ciphertext})
	if err != nil {
		return fmt.Errorf("encode Redis pending rotation result: %w", err)
	}
	if err := r.client.Set(ctx, r.pendingRotationKey(sid), value, ttl).Err(); err != nil {
		return fmt.Errorf("store Redis pending rotation result: %w", err)
	}
	return nil
}

// RecoverRotationResult atomically promotes an encrypted pending result to the
// public concurrent-refresh slot. The value remains AEAD-bound to this SID.
func (r *AuthRedis) RecoverRotationResult(ctx context.Context, sid, targetHMAC string, ttl time.Duration) (string, bool, error) {
	if err := r.requireClient(); err != nil {
		return "", false, err
	}
	if ttl <= 0 {
		return "", false, errors.New("rotation result TTL must be positive")
	}
	// A single Lua transition prevents a stale public result (for B) from
	// surviving a later B→C rotation and prevents GET→SET→DEL interleavings.
	value, err := r.client.Eval(ctx, `
local public = redis.call('GET', KEYS[1])
if public then
  local p = cjson.decode(public)
  if p.target_hmac == ARGV[1] then return public end
end
local pending = redis.call('GET', KEYS[2])
if not pending then return '' end
local q = cjson.decode(pending)
if q.target_hmac ~= ARGV[1] then return '' end
redis.call('SET', KEYS[1], pending, 'PX', ARGV[2])
redis.call('DEL', KEYS[2])
return pending
`, []string{r.rotationKey(sid), r.pendingRotationKey(sid)}, targetHMAC, ttl.Milliseconds()).Text()
	if err != nil {
		return "", false, fmt.Errorf("atomically recover Redis rotation result: %w", err)
	}
	if value == "" {
		return "", false, nil
	}
	record, err := decodeRotationRecord(value)
	if err != nil || record.TargetHMAC != targetHMAC {
		return "", false, errors.New("invalid Redis rotation generation")
	}
	plain, err := r.decrypt(sid, record.Ciphertext)
	if err != nil {
		return "", false, err
	}
	return string(plain), true, nil
}

func decodeRotationRecord(value string) (rotationRecord, error) {
	var record rotationRecord
	if err := json.Unmarshal([]byte(value), &record); err != nil || record.Ciphertext == "" {
		return rotationRecord{}, errors.New("invalid encrypted Redis rotation result")
	}
	return record, nil
}

// MarkSessionRevoked writes the Redis denial barrier before callers mutate
// MySQL. A failed write must prevent the authentication operation from
// reporting success.
func (r *AuthRedis) MarkSessionRevoked(ctx context.Context, sid string, ttl time.Duration) error {
	if err := r.requireClient(); err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if err := r.client.Set(ctx, r.revokedKey(sid), "1", ttl).Err(); err != nil {
		return fmt.Errorf("write Redis session revocation barrier: %w", err)
	}
	return nil
}

// IsSessionRevoked checks the Redis denial barrier and fails closed on read
// errors rather than treating a missing backend as a live session.
func (r *AuthRedis) IsSessionRevoked(ctx context.Context, sid string) (bool, error) {
	if err := r.requireClient(); err != nil {
		return false, err
	}
	exists, err := r.client.Exists(ctx, r.revokedKey(sid)).Result()
	if err != nil {
		return false, fmt.Errorf("read Redis session revocation barrier: %w", err)
	}
	return exists > 0, nil
}

func (r *AuthRedis) requireClient() error {
	if r == nil || r.client == nil {
		return errors.New("Redis authentication store is unavailable")
	}
	return nil
}

func (r *AuthRedis) failureKey(kind, value string) string {
	return authRedisPrefix + "login:failure:" + kind + ":" + hashedRedisKey(value)
}
func (r *AuthRedis) loginKey(kind, value string) string {
	return authRedisPrefix + "login:lock:" + kind + ":" + hashedRedisKey(value)
}
func (r *AuthRedis) rotationKey(sid string) string {
	return authRedisPrefix + "rotation:" + hashedRedisKey(sid)
}
func (r *AuthRedis) pendingRotationKey(sid string) string {
	return authRedisPrefix + "rotation:pending:" + hashedRedisKey(sid)
}
func (r *AuthRedis) revokedKey(sid string) string {
	return authRedisPrefix + "session:revoked:" + hashedRedisKey(sid)
}

func hashedRedisKey(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func (r *AuthRedis) encrypt(sid string, plain []byte) (string, error) {
	nonce := make([]byte, r.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate rotation encryption nonce: %w", err)
	}
	sealed := r.aead.Seal(nil, nonce, plain, rotationAAD(sid))
	return base64.RawURLEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (r *AuthRedis) decrypt(sid, value string) ([]byte, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(encoded) < r.aead.NonceSize() {
		return nil, errors.New("invalid encrypted Redis rotation result")
	}
	plain, err := r.aead.Open(nil, encoded[:r.aead.NonceSize()], encoded[r.aead.NonceSize():], rotationAAD(sid))
	if err != nil {
		return nil, errors.New("decrypt Redis rotation result")
	}
	return plain, nil
}

func rotationAAD(sid string) []byte {
	return []byte("porsche/auth/v1/refresh-rotation/" + strings.TrimSpace(sid))
}
