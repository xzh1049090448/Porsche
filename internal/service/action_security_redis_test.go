package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/redis/go-redis/v9"
)

type actionRateWindow struct {
	count     int64
	expiresAt time.Time
}

type actionRateEvalClient struct {
	redis.UniversalClient

	mu        sync.Mutex
	now       time.Time
	windows   map[string]actionRateWindow
	evalCalls int
	lastKeys  []string
	err       error
	malformed interface{}
}

func newActionRateEvalClient() *actionRateEvalClient {
	return &actionRateEvalClient{
		now:     time.Unix(1_700_000_000, 0),
		windows: make(map[string]actionRateWindow),
	}
}

func (*actionRateEvalClient) actionSecurityStandaloneRedis() {}

func (c *actionRateEvalClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evalCalls++
	c.lastKeys = append([]string(nil), keys...)
	cmd := redis.NewCmd(ctx)
	if err := ctx.Err(); err != nil {
		cmd.SetErr(err)
		return cmd
	}
	if c.err != nil {
		cmd.SetErr(c.err)
		return cmd
	}
	if c.malformed != nil {
		cmd.SetVal(c.malformed)
		return cmd
	}
	if !strings.Contains(script, "read-only preflight") || !strings.Contains(script, "PTTL") ||
		!strings.Contains(script, "'SET', key, '1', 'PX'") || len(args) != len(keys)*2 {
		cmd.SetErr(errors.New("unexpected rate script contract"))
		return cmd
	}
	allowed := int64(1)
	retryMS := int64(0)
	for i, key := range keys {
		limit := actionRateTestInt64(args[i*2])
		windowMS := actionRateTestInt64(args[i*2+1])
		window, ok := c.windows[key]
		if !ok || !c.now.Before(window.expiresAt) {
			window = actionRateWindow{expiresAt: c.now.Add(time.Duration(windowMS) * time.Millisecond)}
		}
		window.count++
		c.windows[key] = window
		remaining := window.expiresAt.Sub(c.now).Milliseconds()
		if window.count > limit {
			allowed = 0
			if retryMS == 0 || remaining < retryMS {
				retryMS = remaining
			}
		}
	}
	cmd.SetVal([]interface{}{allowed, retryMS})
	return cmd
}

func actionRateTestInt64(value interface{}) int64 {
	n, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil {
		panic(err)
	}
	return n
}

func actionRateTestStore(t *testing.T, client redis.UniversalClient) *ActionSecurityRedis {
	t.Helper()
	root := make([]byte, 32)
	for i := range root {
		root[i] = byte(i + 1)
	}
	crypto, err := actionsecurity.NewCrypto(root)
	clear(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewActionSecurityRedis(client, crypto)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestActionSecurityRedisVerificationLimitsAreAtomicAndOpaque(t *testing.T) {
	client := newActionRateEvalClient()
	store := actionRateTestStore(t, client)
	ctx := context.Background()

	for attempt := 1; attempt <= 5; attempt++ {
		if err := store.ReserveVerification(ctx, 1042, 2084, "203.0.113.44"); err != nil {
			t.Fatalf("actor attempt %d: %v", attempt, err)
		}
	}
	err := store.ReserveVerification(ctx, 1042, 2084, "203.0.113.44")
	var retry *RetryAfterError
	if !errors.As(err, &retry) || retry.Seconds != 900 {
		t.Fatalf("sixth actor attempt error = %#v, want 900-second RetryAfterError", err)
	}
	if client.evalCalls != 6 || len(client.lastKeys) != 3 {
		t.Fatalf("Eval calls=%d keys=%d, want one Eval per attempt with three dimensions", client.evalCalls, len(client.lastKeys))
	}
	for _, key := range client.lastKeys {
		if !strings.HasPrefix(key, "porsche:action:rate:v1:") || len(strings.TrimPrefix(key, "porsche:action:rate:v1:")) != 64 {
			t.Fatalf("non-opaque rate key shape: %q", key)
		}
		for _, secret := range []string{"1042", "2084", "203.0.113.44", "actor", "session", "ip"} {
			if strings.Contains(strings.ToLower(key), strings.ToLower(secret)) {
				t.Fatalf("rate key %q leaks %q", key, secret)
			}
		}
	}

	// The rejected sixth actor attempt must still consume the IP and session
	// dimensions. Fourteen different actors therefore bring the shared IP to
	// twenty; the next request is rejected by IP even with a fresh actor/session.
	for actor := int64(1); actor <= 14; actor++ {
		if err := store.ReserveVerification(ctx, actor, 3000+actor, "203.0.113.44"); err != nil {
			t.Fatalf("shared-IP fill actor %d: %v", actor, err)
		}
	}
	err = store.ReserveVerification(ctx, 9999, 9999, "203.0.113.44")
	if !errors.As(err, &retry) || retry.Seconds != 900 {
		t.Fatalf("twenty-first shared-IP attempt error = %#v", err)
	}
}

func TestActionSecurityRedisCanonicalizesTrustedIPBeforeHashing(t *testing.T) {
	client := newActionRateEvalClient()
	store := actionRateTestStore(t, client)
	if err := store.ReserveVerification(context.Background(), 1, 1, "2001:0db8:0:0:0:0:0:1"); err != nil {
		t.Fatal(err)
	}
	firstIPKey := client.lastKeys[1]
	if err := store.ReserveVerification(context.Background(), 2, 2, "2001:db8::1"); err != nil {
		t.Fatal(err)
	}
	if client.lastKeys[1] != firstIPKey {
		t.Fatalf("equivalent IPv6 forms produced different keys: %q != %q", client.lastKeys[1], firstIPKey)
	}
	if err := store.ReserveVerification(context.Background(), 3, 3, "::ffff:192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	mappedKey := client.lastKeys[1]
	if err := store.ReserveVerification(context.Background(), 4, 4, "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	if client.lastKeys[1] != mappedKey {
		t.Fatal("IPv4-mapped address was not normalized with Unmap")
	}
}

func TestActionSecurityRedisVerificationSessionLimitAndNonSlidingTTL(t *testing.T) {
	client := newActionRateEvalClient()
	store := actionRateTestStore(t, client)
	ctx := context.Background()
	for attempt := 1; attempt <= 5; attempt++ {
		if err := store.ReserveVerification(ctx, int64(attempt), 7007, fmt.Sprintf("198.51.100.%d", attempt)); err != nil {
			t.Fatal(err)
		}
	}
	client.now = client.now.Add(250 * time.Millisecond)
	for attempt := 6; attempt <= 10; attempt++ {
		if err := store.ReserveVerification(ctx, int64(attempt), 7007, fmt.Sprintf("198.51.100.%d", attempt)); err != nil {
			t.Fatal(err)
		}
	}
	err := store.ReserveVerification(ctx, 11, 7007, "198.51.100.11")
	var retry *RetryAfterError
	if !errors.As(err, &retry) || retry.Seconds != 3600 {
		t.Fatalf("session limit error = %#v, want rounded-up original 3600-second window", err)
	}
	for _, window := range client.windows {
		if window.count == 11 && !window.expiresAt.Equal(time.Unix(1_700_000_000, 0).Add(time.Hour)) {
			t.Fatalf("session TTL slid to %v", window.expiresAt)
		}
	}
}

func TestActionSecurityRedisBeginLimitRoundsRetryAndDoesNotSlide(t *testing.T) {
	client := newActionRateEvalClient()
	store := actionRateTestStore(t, client)
	ctx := context.Background()
	for attempt := 1; attempt <= 60; attempt++ {
		if err := store.ReserveBegin(ctx, 8080); err != nil {
			t.Fatalf("begin attempt %d: %v", attempt, err)
		}
	}
	client.now = client.now.Add(59*time.Second + time.Millisecond)
	err := store.ReserveBegin(ctx, 8080)
	var retry *RetryAfterError
	if !errors.As(err, &retry) || retry.Seconds != 1 {
		t.Fatalf("begin retry = %#v, want minimum rounded-up one second", err)
	}
	if client.evalCalls != 61 || len(client.lastKeys) != 1 {
		t.Fatalf("Eval calls=%d keys=%d", client.evalCalls, len(client.lastKeys))
	}
}

func TestActionSecurityRedisUsesEarliestExceededWindow(t *testing.T) {
	client := newActionRateEvalClient()
	store := actionRateTestStore(t, client)
	ctx := context.Background()
	for attempt := 1; attempt <= 10; attempt++ {
		err := store.ReserveVerification(ctx, 4040, 5050, fmt.Sprintf("192.0.2.%d", attempt))
		if attempt <= 5 && err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	err := store.ReserveVerification(ctx, 4040, 5050, "192.0.2.11")
	var retry *RetryAfterError
	if !errors.As(err, &retry) || retry.Seconds != 900 {
		t.Fatalf("actor and session exceeded error = %#v, want earliest 900-second window", err)
	}
}

func TestActionSecurityRedisFailsClosedForDependenciesContextAndMalformedReply(t *testing.T) {
	root := make([]byte, 32)
	crypto, err := actionsecurity.NewCrypto(root)
	if err != nil {
		t.Fatal(err)
	}
	var typedNil *redis.Client
	for name, client := range map[string]redis.UniversalClient{"nil": nil, "typed_nil": typedNil} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewActionSecurityRedis(client, crypto); !errors.Is(err, ErrActionSecurityRedisUnavailable) {
				t.Fatalf("constructor error = %v", err)
			}
		})
	}
	cluster := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{"127.0.0.1:1"}})
	ring := redis.NewRing(&redis.RingOptions{Addrs: map[string]string{"one": "127.0.0.1:1"}})
	defer cluster.Close()
	defer ring.Close()
	for name, client := range map[string]redis.UniversalClient{"cluster": cluster, "ring": ring} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewActionSecurityRedis(client, crypto); !errors.Is(err, ErrActionSecurityRedisUnavailable) {
				t.Fatalf("constructor accepted non-standalone %s client: %v", name, err)
			}
		})
	}
	if _, err := NewActionSecurityRedis(newActionRateEvalClient(), nil); !errors.Is(err, ErrActionSecurityRedisUnavailable) {
		t.Fatalf("nil crypto constructor error = %v", err)
	}

	for name, prepare := range map[string]func(*actionRateEvalClient, *context.Context){
		"redis_error": func(c *actionRateEvalClient, _ *context.Context) { c.err = errors.New("backend secret detail") },
		"malformed":   func(c *actionRateEvalClient, _ *context.Context) { c.malformed = []interface{}{int64(1)} },
		"cancelled": func(_ *actionRateEvalClient, ctx *context.Context) {
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			*ctx = cancelled
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := newActionRateEvalClient()
			store := actionRateTestStore(t, client)
			ctx := context.Background()
			prepare(client, &ctx)
			err := store.ReserveBegin(ctx, 9090)
			if !errors.Is(err, ErrActionSecurityRedisUnavailable) || err.Error() != ErrActionSecurityRedisUnavailable.Error() {
				t.Fatalf("error = %q, want single redacted unavailable error", err)
			}
		})
	}
}

func TestActionSecurityRedisRejectsInvalidInputsBeforeEval(t *testing.T) {
	client := newActionRateEvalClient()
	store := actionRateTestStore(t, client)
	for name, call := range map[string]func() error{
		"actor":   func() error { return store.ReserveVerification(context.Background(), 0, 1, "203.0.113.1") },
		"session": func() error { return store.ReserveVerification(context.Background(), 1, 0, "203.0.113.1") },
		"ip":      func() error { return store.ReserveVerification(context.Background(), 1, 1, "") },
		"hostname": func() error {
			return store.ReserveVerification(context.Background(), 1, 1, "example.test")
		},
		"host_port": func() error {
			return store.ReserveVerification(context.Background(), 1, 1, "192.0.2.1:443")
		},
		"zone": func() error {
			return store.ReserveVerification(context.Background(), 1, 1, "fe80::1%en0")
		},
		"begin": func() error { return store.ReserveBegin(context.Background(), 0) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrActionSecurityRateInput) {
				t.Fatalf("invalid input error = %v", err)
			}
		})
	}
	if client.evalCalls != 0 {
		t.Fatalf("invalid inputs reached Redis %d times", client.evalCalls)
	}
}

func TestActionSecurityRedisScriptPreflightsEveryKeyBeforeWriting(t *testing.T) {
	phaseOne := strings.Index(actionSecurityRateScript, "-- phase 1: read-only preflight")
	phaseTwo := strings.Index(actionSecurityRateScript, "-- phase 2: writes")
	firstWrite := strings.Index(actionSecurityRateScript, "redis.call('INCR'")
	firstSet := strings.Index(actionSecurityRateScript, "redis.call('SET'")
	lastExplicitError := strings.LastIndex(actionSecurityRateScript, "redis.error_reply")
	if phaseOne < 0 || phaseTwo <= phaseOne || firstWrite <= phaseTwo || firstSet <= phaseTwo || lastExplicitError >= phaseTwo {
		t.Fatalf("rate script does not separate complete read-only preflight from writes")
	}
}

type actionRateRedisSnapshot struct {
	typeName string
	dump     string
	pttl     time.Duration
}

func TestActionSecurityRedisProductionLuaRejectsCorruptLaterKeysWithoutMutation(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("TEST_REDIS_URL"))
	if rawURL == "" {
		t.Skip("requires explicit disposable TEST_REDIS_URL to execute production Lua")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	store := actionRateTestStore(t, client)
	ctx := context.Background()

	tests := []struct {
		name  string
		index int
		seed  func(string) error
	}{
		{name: "second_key_wrong_type", index: 1, seed: func(key string) error {
			return client.LPush(ctx, key, "not-a-counter").Err()
		}},
		{name: "third_key_wrong_type", index: 2, seed: func(key string) error {
			return client.LPush(ctx, key, "not-a-counter").Err()
		}},
		{name: "second_key_persistent", index: 1, seed: func(key string) error {
			return client.Set(ctx, key, "1", 0).Err()
		}},
		{name: "third_key_overflow", index: 2, seed: func(key string) error {
			return client.Set(ctx, key, "1000000000000000", time.Hour).Err()
		}},
	}
	for offset, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actorID := int64(8_000_000 + offset*10)
			sessionID := actorID + 1
			ip := netip.AddrFrom4([4]byte{198, 18, byte(offset), 1}).String()
			actorPayload := encodeActionRateID(actorID)
			sessionPayload := encodeActionRateID(sessionID)
			keys, err := store.rateKeys([]actionRateIdentity{
				{purpose: actionsecurity.RateVerificationActor, payload: actorPayload[:]},
				{purpose: actionsecurity.RateVerificationIP, payload: []byte(ip)},
				{purpose: actionsecurity.RateVerificationSession, payload: sessionPayload[:]},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Del(context.Background(), keys...).Err() })
			if err := client.Del(ctx, keys...).Err(); err != nil {
				t.Fatal(err)
			}
			if err := tc.seed(keys[tc.index]); err != nil {
				t.Fatal(err)
			}
			before := actionRateReadSnapshots(t, ctx, client, keys)
			if err := store.ReserveVerification(ctx, actorID, sessionID, ip); !errors.Is(err, ErrActionSecurityRedisUnavailable) {
				t.Fatalf("corrupt rate state error = %v", err)
			}
			after := actionRateReadSnapshots(t, ctx, client, keys)
			actionRateAssertSnapshotsUnchanged(t, before, after)
		})
	}
}

func TestActionSecurityRedisProductionLuaFixedWindowAndRetry(t *testing.T) {
	client, store := openActionRateRedisFixture(t)
	ctx := context.Background()
	actorID, sessionID := int64(8_100_000), int64(8_100_001)
	ip := "2001:db8::8100"
	actorPayload := encodeActionRateID(actorID)
	sessionPayload := encodeActionRateID(sessionID)
	keys, err := store.rateKeys([]actionRateIdentity{
		{purpose: actionsecurity.RateVerificationActor, payload: actorPayload[:]},
		{purpose: actionsecurity.RateVerificationIP, payload: []byte(ip)},
		{purpose: actionsecurity.RateVerificationSession, payload: sessionPayload[:]},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), keys...).Err() })
	if err := client.Del(ctx, keys...).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveVerification(ctx, actorID, sessionID, ip); err != nil {
		t.Fatal(err)
	}
	firstTTL := make([]time.Duration, len(keys))
	for i, key := range keys {
		firstTTL[i], err = client.PTTL(ctx, key).Result()
		if err != nil || firstTTL[i] <= 0 {
			t.Fatalf("first PTTL[%d]=%v err=%v", i, firstTTL[i], err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if err := store.ReserveVerification(ctx, actorID, sessionID, ip); err != nil {
		t.Fatal(err)
	}
	for i, key := range keys {
		count, err := client.Get(ctx, key).Int64()
		if err != nil || count != 2 {
			t.Fatalf("counter[%d]=%d err=%v", i, count, err)
		}
		afterTTL, err := client.PTTL(ctx, key).Result()
		if err != nil || afterTTL <= 0 || afterTTL >= firstTTL[i] {
			t.Fatalf("PTTL[%d] slid or vanished: %v -> %v err=%v", i, firstTTL[i], afterTTL, err)
		}
	}
	for attempt := 3; attempt <= 5; attempt++ {
		if err := store.ReserveVerification(ctx, actorID, sessionID, ip); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	err = store.ReserveVerification(ctx, actorID, sessionID, ip)
	var retry *RetryAfterError
	if !errors.As(err, &retry) || retry.Seconds < 899 || retry.Seconds > 900 {
		t.Fatalf("sixth attempt error=%#v", err)
	}
	for i, key := range keys {
		count, err := client.Get(ctx, key).Int64()
		if err != nil || count != 6 {
			t.Fatalf("rejected attempt did not atomically consume counter[%d]: %d err=%v", i, count, err)
		}
	}
}

func openActionRateRedisFixture(t *testing.T) (*redis.Client, *ActionSecurityRedis) {
	t.Helper()
	rawURL := strings.TrimSpace(os.Getenv("TEST_REDIS_URL"))
	if rawURL == "" {
		t.Skip("requires explicit disposable TEST_REDIS_URL to execute production Lua")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	return client, actionRateTestStore(t, client)
}

func actionRateReadSnapshots(t *testing.T, ctx context.Context, client *redis.Client, keys []string) []actionRateRedisSnapshot {
	t.Helper()
	out := make([]actionRateRedisSnapshot, len(keys))
	for i, key := range keys {
		typeName, err := client.Type(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		out[i].typeName = typeName
		if typeName == "none" {
			continue
		}
		out[i].dump, err = client.Dump(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		out[i].pttl, err = client.PTTL(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func actionRateAssertSnapshotsUnchanged(t *testing.T, before, after []actionRateRedisSnapshot) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("snapshot lengths differ")
	}
	for i := range before {
		if before[i].typeName != after[i].typeName || before[i].dump != after[i].dump {
			t.Fatalf("key %d changed: before=%#v after=%#v", i, before[i], after[i])
		}
		if before[i].typeName == "none" {
			continue
		}
		if before[i].pttl < 0 {
			if before[i].pttl != after[i].pttl {
				t.Fatalf("key %d TTL state changed: %v -> %v", i, before[i].pttl, after[i].pttl)
			}
		} else if after[i].pttl <= 0 || after[i].pttl > before[i].pttl || before[i].pttl-after[i].pttl > time.Second {
			t.Fatalf("key %d expiry changed beyond clock passage: %v -> %v", i, before[i].pttl, after[i].pttl)
		}
	}
}

func TestActionSecurityRedisReplyContractRejectsUnsafeValues(t *testing.T) {
	for name, reply := range map[string]interface{}{
		"allowed_out_of_range": []interface{}{int64(2), int64(0)},
		"denied_without_ttl":   []interface{}{int64(0), int64(0)},
		"unexpected_retry":     []interface{}{int64(1), int64(1)},
		"overflow_retry":       []interface{}{int64(0), int64(^uint64(0) >> 1)},
		"wrong_types":          []interface{}{"1", "0"},
	} {
		t.Run(name, func(t *testing.T) {
			client := newActionRateEvalClient()
			client.malformed = reply
			store := actionRateTestStore(t, client)
			if err := store.ReserveBegin(context.Background(), 1); !errors.Is(err, ErrActionSecurityRedisUnavailable) {
				t.Fatalf("reply %#v error = %v", reply, err)
			}
		})
	}
}

func TestActionSecurityRedisContractShape(t *testing.T) {
	typeOf := reflect.TypeOf(ActionSecurityRedis{})
	if typeOf.NumField() != 2 || typeOf.Field(0).Name != "client" || typeOf.Field(1).Name != "crypto" {
		t.Fatalf("ActionSecurityRedis fields changed: %v", typeOf)
	}
}
