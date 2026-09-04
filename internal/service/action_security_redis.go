package service

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/netip"
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
	if redisClientIsNil(client) || crypto == nil || !actionSecurityRedisClientSupported(client) {
		return nil, ErrActionSecurityRedisUnavailable
	}
	return &ActionSecurityRedis{client: client, crypto: crypto}, nil
}

// ReserveVerification atomically reserves actor, trusted-IP, and logical
// session verification windows. A rejection still consumes every dimension.
func (r *ActionSecurityRedis) ReserveVerification(ctx context.Context, actorID, sessionID int64, trustedIP string) error {
	trustedAddr, err := netip.ParseAddr(trustedIP)
	if actorID <= 0 || sessionID <= 0 || err != nil || trustedAddr.Zone() != "" {
		return ErrActionSecurityRateInput
	}
	canonicalIP := trustedAddr.Unmap().String()
	actorPayload := encodeActionRateID(actorID)
	sessionPayload := encodeActionRateID(sessionID)
	keys, err := r.rateKeys([]actionRateIdentity{
		{purpose: actionsecurity.RateVerificationActor, payload: actorPayload[:]},
		{purpose: actionsecurity.RateVerificationIP, payload: []byte(canonicalIP)},
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

// The Lua contract needs all keys on one standalone Redis server. Sentinel
// clients are represented by *redis.Client and remain compatible; sharded
// ClusterClient/Ring clients are rejected instead of introducing a public hash
// tag that would correlate otherwise opaque rate dimensions.
func actionSecurityRedisClientSupported(client redis.UniversalClient) bool {
	switch client.(type) {
	case *redis.Client:
		return true
	case interface{ actionSecurityStandaloneRedis() }:
		return true
	default:
		return false
	}
}

const actionSecurityRateScript = `#!lua flags=no-cluster
local allowed = 1
local retry_ms = 0
local next_counts = {}
local windows = {}
local missing = {}
local seen = {}

if #KEYS == 0 or #ARGV ~= #KEYS * 2 then
  return redis.error_reply('invalid rate contract')
end

-- phase 1: read-only preflight. No state can change on any rejection here.
for i, key in ipairs(KEYS) do
  if seen[key] then
    return redis.error_reply('duplicate rate key')
  end
  seen[key] = true
  local limit = tonumber(ARGV[(i - 1) * 2 + 1])
  local window_ms = tonumber(ARGV[(i - 1) * 2 + 2])
  if not limit or not window_ms or limit <= 0 or window_ms <= 0 or
      limit ~= math.floor(limit) or window_ms ~= math.floor(window_ms) then
    return redis.error_reply('invalid rate contract')
  end
  windows[i] = window_ms

  local kind_reply = redis.call('TYPE', key)
  local kind = kind_reply['ok']
  local next_count = 1
  local remaining = window_ms
  if kind == 'none' then
    if not redis.acl_check_cmd('SET', key, '1', 'PX', window_ms) then
      return redis.error_reply('rate write denied')
    end
    missing[i] = true
  elseif kind == 'string' then
    local raw_count = redis.call('GET', key)
    if not raw_count or not string.match(raw_count, '^[0-9]+$') or
        (#raw_count > 1 and string.sub(raw_count, 1, 1) == '0') or
        #raw_count > 15 then
      return redis.error_reply('invalid rate counter')
    end
    local count = tonumber(raw_count)
    if not count or count < 0 or count > 999999999999999 then
      return redis.error_reply('invalid rate counter')
    end
    next_count = count + 1
    remaining = redis.call('PTTL', key)
    if remaining <= 0 or remaining > window_ms then
      return redis.error_reply('invalid rate ttl')
    end
    if not redis.acl_check_cmd('INCR', key) then
      return redis.error_reply('rate write denied')
    end
    missing[i] = false
  else
    return redis.error_reply('invalid rate type')
  end
  next_counts[i] = next_count
  if next_count > limit then
    allowed = 0
    if retry_ms == 0 or remaining < retry_ms then
      retry_ms = remaining
    end
  end
end

-- phase 2: writes. Preflight proved command types, integer bounds and TTLs.
-- It also checked ACL permission for each exact write. Redis 7 rejects write
-- scripts before execution on replicas, persistence errors and existing OOM.
-- Its first memory-growing command can fail before any write; after that it
-- lets the script finish to preserve atomicity.
for i, key in ipairs(KEYS) do
  if missing[i] then
    redis.call('SET', key, '1', 'PX', windows[i])
  else
    redis.call('INCR', key)
  end
end
return {allowed, retry_ms}
`
