package service

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/redis/go-redis/v9"
)

const actionSecurityRatePrefix = "porsche:action:rate:v1:"

var (
	// ErrActionSecurityRedisUnavailable intentionally collapses dependency,
	// cancellation, script, and reply failures without exposing Redis details.
	ErrActionSecurityRedisUnavailable = errors.New("action security rate limiter unavailable")
	ErrActionSecurityRateInput        = errors.New("invalid action security rate input")
)

// ActionSecurityRedis is the fail-closed Redis boundary for action-security
// rate limits. Redis stores only purpose-separated HMAC counters.
type ActionSecurityRedis struct {
	client redis.UniversalClient
	crypto *actionsecurity.Crypto
}

// RetryAfterError reports the fixed-window time remaining after a limit is
// exceeded. It deliberately carries no Redis key or caller identity.
type RetryAfterError struct {
	Seconds int
}

func (e *RetryAfterError) Error() string { return "action security rate limit exceeded" }

// NewActionSecurityRedis rejects unavailable dependencies before a protected
// operation can reach its MySQL transaction.
func NewActionSecurityRedis(client redis.UniversalClient, crypto *actionsecurity.Crypto) (*ActionSecurityRedis, error) {
	if redisClientIsNil(client) || crypto == nil {
		return nil, ErrActionSecurityRedisUnavailable
	}
	return &ActionSecurityRedis{client: client, crypto: crypto}, nil
}

// ReserveVerification atomically reserves actor, trusted-IP, and logical
// session verification windows. A rejection still consumes every dimension.
func (r *ActionSecurityRedis) ReserveVerification(ctx context.Context, actorID, sessionID int64, trustedIP string) error {
	if actorID <= 0 || sessionID <= 0 || trustedIP == "" {
		return ErrActionSecurityRateInput
	}
	actorPayload := encodeActionRateID(actorID)
	sessionPayload := encodeActionRateID(sessionID)
	keys, err := r.rateKeys([]actionRateIdentity{
		{purpose: actionsecurity.RateVerificationActor, payload: actorPayload[:]},
		{purpose: actionsecurity.RateVerificationIP, payload: []byte(trustedIP)},
		{purpose: actionsecurity.RateVerificationSession, payload: sessionPayload[:]},
	})
	if err != nil {
		return err
	}
	return r.reserve(ctx, keys, []actionRateWindowLimit{
		{limit: 5, windowMS: 900_000},
		{limit: 20, windowMS: 900_000},
		{limit: 10, windowMS: 3_600_000},
	})
}

// ReserveBegin atomically reserves one Begin attempt for the logical session.
func (r *ActionSecurityRedis) ReserveBegin(ctx context.Context, sessionID int64) error {
	if sessionID <= 0 {
		return ErrActionSecurityRateInput
	}
	payload := encodeActionRateID(sessionID)
	keys, err := r.rateKeys([]actionRateIdentity{{purpose: actionsecurity.RateBeginSession, payload: payload[:]}})
	if err != nil {
		return err
	}
	return r.reserve(ctx, keys, []actionRateWindowLimit{{limit: 60, windowMS: 60_000}})
}

type actionRateIdentity struct {
	purpose string
	payload []byte
}

type actionRateWindowLimit struct {
	limit    int64
	windowMS int64
}

func (r *ActionSecurityRedis) rateKeys(identities []actionRateIdentity) ([]string, error) {
	if r == nil || redisClientIsNil(r.client) || r.crypto == nil {
		return nil, ErrActionSecurityRedisUnavailable
	}
	keys := make([]string, len(identities))
	for i, identity := range identities {
		digest, err := r.crypto.RateDigest(identity.purpose, identity.payload)
		if err != nil {
			return nil, ErrActionSecurityRedisUnavailable
		}
		keys[i] = actionSecurityRatePrefix + hex.EncodeToString(digest[:])
		clear(digest[:])
	}
	return keys, nil
}

func (r *ActionSecurityRedis) reserve(ctx context.Context, keys []string, limits []actionRateWindowLimit) error {
	if r == nil || redisClientIsNil(r.client) || r.crypto == nil || ctx == nil || len(keys) == 0 || len(keys) != len(limits) {
		return ErrActionSecurityRedisUnavailable
	}
	args := make([]interface{}, 0, len(limits)*2)
	var maxWindowMS int64
	for _, limit := range limits {
		if limit.limit <= 0 || limit.windowMS <= 0 {
			return ErrActionSecurityRedisUnavailable
		}
		if limit.windowMS > maxWindowMS {
			maxWindowMS = limit.windowMS
		}
		args = append(args, limit.limit, limit.windowMS)
	}
	reply, err := r.client.Eval(ctx, actionSecurityRateScript, keys, args...).Result()
	if err != nil {
		return ErrActionSecurityRedisUnavailable
	}
	values, ok := reply.([]interface{})
	if !ok || len(values) != 2 {
		return ErrActionSecurityRedisUnavailable
	}
	allowed, okAllowed := values[0].(int64)
	retryMS, okRetry := values[1].(int64)
	if !okAllowed || !okRetry || (allowed != 0 && allowed != 1) || retryMS < 0 {
		return ErrActionSecurityRedisUnavailable
	}
	if allowed == 1 {
		if retryMS != 0 {
			return ErrActionSecurityRedisUnavailable
		}
		return nil
	}
	if retryMS <= 0 || retryMS > maxWindowMS {
		return ErrActionSecurityRedisUnavailable
	}
	seconds := (retryMS + 999) / 1000
	if seconds < 1 {
		seconds = 1
	}
	if seconds > int64(^uint(0)>>1) {
		return ErrActionSecurityRedisUnavailable
	}
	return &RetryAfterError{Seconds: int(seconds)}
}

func encodeActionRateID(id int64) [8]byte {
	var payload [8]byte
	binary.BigEndian.PutUint64(payload[:], uint64(id))
	return payload
}

func redisClientIsNil(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

const actionSecurityRateScript = `
local allowed = 1
local retry_ms = 0
for i, key in ipairs(KEYS) do
  local limit = tonumber(ARGV[(i - 1) * 2 + 1])
  local window_ms = tonumber(ARGV[(i - 1) * 2 + 2])
  if not limit or not window_ms or limit <= 0 or window_ms <= 0 then
    return redis.error_reply('invalid rate contract')
  end
  local count = redis.call('INCR', key)
  if count == 1 then
    if redis.call('PEXPIRE', key, window_ms) ~= 1 then
      return redis.error_reply('rate expiry unavailable')
    end
  end
  local remaining = redis.call('PTTL', key)
  if remaining <= 0 then
    return redis.error_reply('rate ttl unavailable')
  end
  if count > limit then
    allowed = 0
    if retry_ms == 0 or remaining < retry_ms then
      retry_ms = remaining
    end
  end
end
return {allowed, retry_ms}
`
