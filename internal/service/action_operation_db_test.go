package service

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type realActionClock struct{ now atomic.Int64 }

func newRealActionClock(now int64) *realActionClock {
	clock := &realActionClock{}
	clock.now.Store(now)
	return clock
}

func (clock *realActionClock) NowMillis() int64 { return clock.now.Load() }
func (clock *realActionClock) Set(now int64)    { clock.now.Store(now) }

type realActionFixture struct {
	db           *gorm.DB
	redis        *redis.Client
	crypto       *actionsecurity.Crypto
	limiter      *ActionSecurityRedis
	authRedis    *AuthRedis
	verification *ActionVerificationService
	operation    *ActionOperationService
	clock        *realActionClock
	actorRow     models.User
	targetRow    models.User
	sessionRow   models.Session
	actor        ActionActor
	password     string
}

func openRealActionFixture(t *testing.T, now int64) *realActionFixture {
	t.Helper()
	db := openTestMySQL(t)
	rawRedis := strings.TrimSpace(os.Getenv("TEST_REDIS_URL"))
	if rawRedis == "" {
		t.Skip("requires explicit disposable TEST_REDIS_URL")
	}
	options, err := redis.ParseURL(rawRedis)
	if err != nil {
		t.Fatalf("parse TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("open disposable Redis: %v", err)
	}
	rawRoot, reason := actionsecurity.ParseRootKey(strings.TrimSpace(os.Getenv("ACTION_SECURITY_HMAC_KEY")))
	if reason != "" {
		t.Fatalf("invalid test action root key: %s", reason)
	}
	crypto, err := actionsecurity.NewCrypto(rawRoot)
	clear(rawRoot)
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := NewActionSecurityRedis(client, crypto)
	if err != nil {
		t.Fatal(err)
	}
	authRedis, err := NewAuthRedis(client, "task12-isolated-auth-hmac-material")
	if err != nil {
		t.Fatal(err)
	}
	password := "Task12-Strong-Password!"
	passwordHash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	username := fixtureUsername(testSnowflake.Next())
	actorRow := models.User{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now},
		Username:    &username, PasswordHash: &passwordHash, Role: models.UserRoleRoot,
		Status: models.UserStatusActive, AuthVersion: 7, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{},
	}
	if err := db.Create(&actorRow).Error; err != nil {
		t.Fatal(err)
	}
	targetUsername := fixtureUsername(testSnowflake.Next())
	targetRow := models.User{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now},
		Username:    &targetUsername, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 4,
		PlanType: models.PlanFree, AllowedModels: models.JSONSlice{},
	}
	if err := db.Create(&targetRow).Error; err != nil {
		t.Fatal(err)
	}
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	sessionRow := models.Session{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now},
		SID:         sid, UserID: actorRow.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 3,
		RefreshHMAC: strings.Repeat("b", 64), LastActiveAt: now, ExpiresAt: now + 3_456_000_000,
	}
	if err := db.Create(&sessionRow).Error; err != nil {
		t.Fatal(err)
	}
	actor := ActionActor{UserID: actorRow.ID, UserGUID: actorRow.Guid, AuthVersion: actorRow.AuthVersion, SessionSID: sid, SessionVersion: sessionRow.SessionVersion}
	clock := newRealActionClock(now)
	resolver := func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		if action == testNoopAction {
			return testNoopDescriptor(), true
		}
		return actionsecurity.Descriptor{}, false
	}
	verification, err := newActionVerificationService(db, limiter, authRedis, crypto, resolver, clock, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newActionOperationService(db, limiter, authRedis, crypto, resolver, clock, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	if err != nil {
		t.Fatal(err)
	}
	return &realActionFixture{db: db, redis: client, crypto: crypto, limiter: limiter, authRedis: authRedis, verification: verification, operation: operation, clock: clock, actorRow: actorRow, targetRow: targetRow, sessionRow: sessionRow, actor: actor, password: password}
}

func newRealIdempotencyKey(t *testing.T) string {
	t.Helper()
	var raw [32]byte
	if _, err := io.ReadFull(cryptorand.Reader, raw[:]); err != nil {
		t.Fatal(err)
	}
	value := "ik_" + base64.RawURLEncoding.EncodeToString(raw[:])
	clear(raw[:])
	return value
}

func TestActionVerificationRealMySQLRedisTicketBoundaryAndLimits(t *testing.T) {
	now := int64(1_800_000_000_000)
	fixture := openRealActionFixture(t, now)
	var latest *IssuedVerification
	for attempt := 1; attempt <= 5; attempt++ {
		password := []byte(fixture.password)
		issued, err := fixture.verification.Issue(context.Background(), VerificationIssue{
			Action: testNoopAction, TargetGUID: &fixture.targetRow.Guid, Actor: fixture.actor, Intent: testNoopIntent(fixture.targetRow.Guid, "real-ticket-intent"),
			CurrentPassword: password, TrustedIP: "203.0.113.121",
		})
		if err != nil {
			t.Fatalf("Issue attempt %d: %v", attempt, err)
		}
		if issued.ExpiresAt != now+300_000 || len(issued.Ticket) != 46 || !strings.HasPrefix(issued.Ticket, "av_") {
			t.Fatalf("Issue attempt %d returned invalid fixed expiry", attempt)
		}
		if !bytes.Equal(password, make([]byte, len(password))) {
			t.Fatalf("Issue attempt %d retained password bytes", attempt)
		}
		latest = issued
	}
	password := []byte(fixture.password)
	if issued, err := fixture.verification.Issue(context.Background(), VerificationIssue{
		Action: testNoopAction, TargetGUID: &fixture.targetRow.Guid, Actor: fixture.actor, Intent: testNoopIntent(fixture.targetRow.Guid, "real-ticket-intent"),
		CurrentPassword: password, TrustedIP: "203.0.113.121",
	}); issued != nil {
		t.Fatal("sixth Issue returned a ticket")
	} else {
		var retry *RetryAfterError
		if !errors.As(err, &retry) || retry.Seconds < 899 || retry.Seconds > 900 {
			t.Fatalf("sixth Issue error = %#v", err)
		}
	}
	fixture.clock.Set(latest.ExpiresAt)
	if identity, view, err := fixture.operation.Begin(context.Background(), OperationBegin{
		Action: testNoopAction, Actor: fixture.actor, IdempotencyKeyValues: []string{newRealIdempotencyKey(t)},
		TicketValues: []string{latest.Ticket}, Intent: testNoopIntent(fixture.targetRow.Guid, "real-ticket-intent"),
	}); identity != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) {
		t.Fatalf("expires_at equality accepted: identity=%v view=%v err=%v", identity, view, err)
	}
	var rows []models.AdminActionVerification
	if err := fixture.db.Unscoped().Where("actor_user_id = ? AND action = ?", fixture.actor.UserID, int(testNoopAction)).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[4].IsDeleted != 0 {
		t.Fatalf("verification rows=%d latest_deleted=%d", len(rows), rows[len(rows)-1].IsDeleted)
	}
	for _, row := range rows {
		if len(row.IntentHMAC) != 64 || len(row.TicketHMAC) != 64 || strings.Contains(row.IntentHMAC+row.TicketHMAC, latest.Ticket) {
			t.Fatal("verification persistence was not digest-only")
		}
	}
}

func TestActionSecurityRedisRealWindowsTTLAndTotalFailure(t *testing.T) {
	fixture := openRealActionFixture(t, 1_800_100_000_000)
	ctx := context.Background()
	var retry *RetryAfterError
	assertTTL := func(t *testing.T, key string, first time.Duration) {
		t.Helper()
		after, err := fixture.redis.PTTL(ctx, key).Result()
		if err != nil || after <= 0 || after >= first {
			t.Fatalf("fixed window TTL slid: %v -> %v err=%v", first, after, err)
		}
	}
	keyFor := func(t *testing.T, purpose string, payload []byte) string {
		t.Helper()
		keys, err := fixture.limiter.rateKeys([]actionRateIdentity{{purpose: purpose, payload: payload}})
		if err != nil || len(keys) != 1 {
			t.Fatalf("rate key: %v", err)
		}
		return keys[0]
	}
	base := testSnowflake.Next()
	t.Run("actor_5_per_15m_non_sliding", func(t *testing.T) {
		actorID := base + 100
		actorPayload := encodeActionRateID(actorID)
		key := keyFor(t, actionsecurity.RateVerificationActor, actorPayload[:])
		for attempt := int64(1); attempt <= 5; attempt++ {
			if err := fixture.limiter.ReserveVerification(ctx, actorID, base+1000+attempt, fmt.Sprintf("198.18.1.%d", attempt)); err != nil {
				t.Fatalf("actor attempt %d: %v", attempt, err)
			}
			if attempt == 1 {
				first, _ := fixture.redis.PTTL(ctx, key).Result()
				time.Sleep(20 * time.Millisecond)
				defer assertTTL(t, key, first)
			}
		}
		if err := fixture.limiter.ReserveVerification(ctx, actorID, base+1006, "198.18.1.6"); !errors.As(err, &retry) || retry.Seconds < 899 || retry.Seconds > 900 {
			t.Fatalf("actor N+1 = %#v", err)
		}
	})
	t.Run("ip_20_per_15m_varied_actor_session_non_sliding", func(t *testing.T) {
		var nonce [8]byte
		if _, err := io.ReadFull(cryptorand.Reader, nonce[:]); err != nil {
			t.Fatal(err)
		}
		ip := fmt.Sprintf("2001:db8:%x:%x:%x:%x::20", nonce[0:2], nonce[2:4], nonce[4:6], nonce[6:8])
		clear(nonce[:])
		key := keyFor(t, actionsecurity.RateVerificationIP, []byte(netip.MustParseAddr(ip).Unmap().String()))
		var first time.Duration
		for attempt := int64(1); attempt <= 20; attempt++ {
			if err := fixture.limiter.ReserveVerification(ctx, base+2000+attempt, base+3000+attempt, ip); err != nil {
				t.Fatalf("IP attempt %d: %v", attempt, err)
			}
			if attempt == 1 {
				first, _ = fixture.redis.PTTL(ctx, key).Result()
				time.Sleep(20 * time.Millisecond)
			}
		}
		assertTTL(t, key, first)
		if err := fixture.limiter.ReserveVerification(ctx, base+2021, base+3021, ip); !errors.As(err, &retry) || retry.Seconds < 899 || retry.Seconds > 900 {
			t.Fatalf("IP N+1 = %#v", err)
		}
	})
	t.Run("logical_session_10_per_hour_varied_actor_ip_non_sliding", func(t *testing.T) {
		sessionID := base + 4000
		payload := encodeActionRateID(sessionID)
		key := keyFor(t, actionsecurity.RateVerificationSession, payload[:])
		var first time.Duration
		for attempt := int64(1); attempt <= 10; attempt++ {
			if err := fixture.limiter.ReserveVerification(ctx, base+5000+attempt, sessionID, fmt.Sprintf("198.18.3.%d", attempt)); err != nil {
				t.Fatalf("session attempt %d: %v", attempt, err)
			}
			if attempt == 1 {
				first, _ = fixture.redis.PTTL(ctx, key).Result()
				time.Sleep(20 * time.Millisecond)
			}
		}
		assertTTL(t, key, first)
		if err := fixture.limiter.ReserveVerification(ctx, base+5011, sessionID, "198.18.3.11"); !errors.As(err, &retry) || retry.Seconds < 3599 || retry.Seconds > 3600 {
			t.Fatalf("session N+1 = %#v", err)
		}
	})
	t.Run("issuance_session_10_per_hour_varied_actor_ip_non_sliding", func(t *testing.T) {
		sid, err := security.NewSessionSID()
		if err != nil {
			t.Fatal(err)
		}
		key := keyFor(t, actionsecurity.RateVerificationSession, []byte(sid))
		var first time.Duration
		for attempt := int64(1); attempt <= 10; attempt++ {
			if err := fixture.verification.reserveVerification(ctx, base+6000+attempt, sid, fmt.Sprintf("198.18.4.%d", attempt)); err != nil {
				t.Fatalf("issuance attempt %d: %v", attempt, err)
			}
			if attempt == 1 {
				first, _ = fixture.redis.PTTL(ctx, key).Result()
				time.Sleep(20 * time.Millisecond)
			}
		}
		assertTTL(t, key, first)
		if err := fixture.verification.reserveVerification(ctx, base+6011, sid, "198.18.4.11"); !errors.As(err, &retry) || retry.Seconds < 3599 || retry.Seconds > 3600 {
			t.Fatalf("issuance session N+1 = %#v", err)
		}
	})
	t.Run("begin_60_per_minute_non_sliding", func(t *testing.T) {
		beginSession := base + 7000
		payload := encodeActionRateID(beginSession)
		key := keyFor(t, actionsecurity.RateBeginSession, payload[:])
		var first time.Duration
		for attempt := 1; attempt <= 60; attempt++ {
			if err := fixture.limiter.ReserveBegin(ctx, beginSession); err != nil {
				t.Fatalf("Begin attempt %d: %v", attempt, err)
			}
			if attempt == 1 {
				first, _ = fixture.redis.PTTL(ctx, key).Result()
				time.Sleep(20 * time.Millisecond)
			}
		}
		assertTTL(t, key, first)
		if err := fixture.limiter.ReserveBegin(ctx, beginSession); !errors.As(err, &retry) || retry.Seconds < 59 || retry.Seconds > 60 {
			t.Fatalf("Begin N+1 = %#v", err)
		}
	})
	brokenClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond, MaxRetries: 0})
	t.Cleanup(func() { _ = brokenClient.Close() })
	brokenLimiter, err := NewActionSecurityRedis(brokenClient, fixture.crypto)
	if err != nil {
		t.Fatal(err)
	}
	brokenService, err := newActionOperationService(fixture.db, brokenLimiter, fixture.authRedis, fixture.crypto, func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		return testNoopDescriptor(), action == testNoopAction
	}, fixture.clock, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Set(1_800_100_000_000)
	if identity, view, err := brokenService.Begin(ctx, OperationBegin{Action: testNoopAction, Actor: fixture.actor, IdempotencyKeyValues: []string{newRealIdempotencyKey(t)}, TicketValues: []string{"av_" + strings.Repeat("A", 43)}, Intent: testNoopIntent(fixture.targetRow.Guid, "redis-down")}); identity != nil || view != nil || !errors.Is(err, ErrActionOperationUnavailable) || ErrActionOperationUnavailable.Status != 503 {
		t.Fatalf("Redis total failure did not map to fixed 503: identity=%v view=%v err=%v", identity, view, err)
	}
}

func TestActionOperationConcurrencyRealMySQLLocksBindingsAndClockEdges(t *testing.T) {
	now := int64(1_800_200_000_000)
	fixture := openRealActionFixture(t, now)
	issued, err := fixture.verification.Issue(context.Background(), VerificationIssue{Action: testNoopAction, TargetGUID: &fixture.targetRow.Guid, Actor: fixture.actor, Intent: testNoopIntent(fixture.targetRow.Guid, "real-operation-intent"), CurrentPassword: []byte(fixture.password), TrustedIP: "203.0.113.123"})
	if err != nil {
		t.Fatal(err)
	}
	key := newRealIdempotencyKey(t)
	input := OperationBegin{Action: testNoopAction, Actor: fixture.actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{issued.Ticket}, Intent: testNoopIntent(fixture.targetRow.Guid, "real-operation-intent")}
	identity, view, err := fixture.operation.Begin(context.Background(), input)
	if err != nil || identity == nil || view == nil || view.Status != "processing" {
		t.Fatalf("initial Begin identity=%v view=%v err=%v", identity, view, err)
	}
	var stored models.AdminOperation
	if err := fixture.db.First(&stored, identity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LeaseExpiresAt == nil || *stored.LeaseExpiresAt != now+30_000 || stored.QueryExpiresAt != now+2_592_000_000 {
		t.Fatalf("lease/query clocks = %v/%d", stored.LeaseExpiresAt, stored.QueryExpiresAt)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gotIdentity, gotView, gotErr := fixture.operation.Begin(context.Background(), input)
			if gotErr == nil && (gotIdentity == nil || gotView == nil || gotIdentity.ID != identity.ID || gotView.PublicRef != identity.PublicRef) {
				gotErr = errors.New("concurrent Begin returned inconsistent operation")
			}
			results <- gotErr
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("same-key concurrent Begin: %v", err)
		}
	}
	var operationCount int64
	if err := fixture.db.Model(&models.AdminOperation{}).Where("actor_user_id = ? AND action = ?", fixture.actor.UserID, int(testNoopAction)).Count(&operationCount).Error; err != nil || operationCount != 1 {
		t.Fatalf("concurrent Begin operations=%d err=%v", operationCount, err)
	}
	conflict := input
	conflict.Intent = testNoopIntent(fixture.targetRow.Guid, "different-payload")
	if gotIdentity, gotView, err := fixture.operation.Begin(context.Background(), conflict); gotIdentity != nil || gotView != nil || !errors.Is(err, ErrActionOperationConflict) {
		t.Fatalf("payload conflict identity=%v view=%v err=%v", gotIdentity, gotView, err)
	}
	reuse := input
	reuse.IdempotencyKeyValues = []string{newRealIdempotencyKey(t)}
	if gotIdentity, gotView, err := fixture.operation.Begin(context.Background(), reuse); gotIdentity != nil || gotView != nil || !errors.Is(err, ErrActionOperationForbidden) {
		t.Fatalf("ticket reuse identity=%v view=%v err=%v", gotIdentity, gotView, err)
	}
	secondSID, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	secondSession := models.Session{AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now}, SID: secondSID, UserID: fixture.actorRow.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 1, RefreshHMAC: strings.Repeat("c", 64), LastActiveAt: now, ExpiresAt: now + 3_456_000_000}
	if err := fixture.db.Create(&secondSession).Error; err != nil {
		t.Fatal(err)
	}
	cross := input
	cross.Actor.SessionSID, cross.Actor.SessionVersion = secondSID, 1
	if gotIdentity, gotView, err := fixture.operation.Begin(context.Background(), cross); gotIdentity != nil || gotView != nil || !errors.Is(err, ErrActionOperationCrossSession) {
		t.Fatalf("cross-session identity=%v view=%v err=%v", gotIdentity, gotView, err)
	}
	if err := fixture.db.Model(&models.Session{}).Where("id = ?", fixture.sessionRow.ID).Updates(map[string]any{"session_version": 4, "updated_at": now + 1}).Error; err != nil {
		t.Fatal(err)
	}
	refreshed := input
	refreshed.Actor.SessionVersion = 4
	if gotIdentity, gotView, err := fixture.operation.Begin(context.Background(), refreshed); err != nil || gotIdentity == nil || gotView == nil || gotIdentity.ID != identity.ID {
		t.Fatalf("refresh-same-session identity=%v view=%v err=%v", gotIdentity, gotView, err)
	}
	fixture.clock.Set(*stored.LeaseExpiresAt + 60_000)
	if err := fixture.operation.MarkPendingRecovery(context.Background(), stored.ID); !errors.Is(err, ErrActionOperationConflict) {
		t.Fatalf("60s grace equality = %v", err)
	}
	fixture.clock.Set(*stored.LeaseExpiresAt + 60_001)
	if err := fixture.operation.MarkPendingRecovery(context.Background(), stored.ID); err != nil {
		t.Fatalf("60s grace +1 = %v", err)
	}
	fixture.clock.Set(stored.QueryExpiresAt)
	if got, err := fixture.operation.Query(context.Background(), testNoopAction, refreshed.Actor, []string{key}); got != nil || !errors.Is(err, ErrActionOperationExpired) {
		t.Fatalf("30d equality query=%v err=%v", got, err)
	}
	if err := fixture.db.Unscoped().First(&stored, stored.ID).Error; err != nil || stored.State != models.OperationExpired || stored.IsDeleted != 1 {
		t.Fatalf("expired tombstone state=%v deleted=%d err=%v", stored.State, stored.IsDeleted, err)
	}
	if gotIdentity, gotView, err := fixture.operation.Begin(context.Background(), refreshed); gotIdentity != nil || gotView != nil || !errors.Is(err, ErrActionOperationExpired) {
		t.Fatalf("tombstone key reuse identity=%v view=%v err=%v", gotIdentity, gotView, err)
	}
}

const actionOperationScriptDriverName = "porsche_action_operation_script"

var (
	actionOperationScriptOnce sync.Once
	actionOperationScriptSeq  atomic.Uint64
	actionOperationScripts    sync.Map
)

type actionOperationScript struct {
	mu               sync.Mutex
	now              int64
	keyHex           string
	actor            models.User
	target           *models.User
	sessions         []models.Session
	operation        *models.AdminOperation
	pendingOperation *models.AdminOperation
	verification     *models.AdminActionVerification
	policyHead       *models.PermissionPolicyHead
	overrides        []models.PermissionOverride
	queries          []string
	queryArgs        [][]driver.NamedValue
	execs            []string
	execArgs         [][]driver.NamedValue
	events           []string
	beginCount       int
	commitCount      int
	rollbackCount    int
	isolations       []driver.IsolationLevel
	failExec         bool
	failExecAt       int
	execError        error
	zeroAffected     bool
	failCommit       bool
}

type actionOperationDriver struct{}
type actionOperationConn struct{ script *actionOperationScript }
type actionOperationTx struct{ script *actionOperationScript }
type actionOperationRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}
type actionOperationResult struct{ id, affected int64 }

func (actionOperationDriver) Open(name string) (driver.Conn, error) {
	value, ok := actionOperationScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown action operation script")
	}
	return &actionOperationConn{script: value.(*actionOperationScript)}, nil
}
func (c *actionOperationConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare disabled")
}
func (c *actionOperationConn) Close() error { return nil }
func (c *actionOperationConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *actionOperationConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if opts.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		return nil, errors.New("operation transaction is not READ COMMITTED")
	}
	c.script.mu.Lock()
	c.script.beginCount++
	c.script.pendingOperation = nil
	c.script.isolations = append(c.script.isolations, opts.Isolation)
	c.script.mu.Unlock()
	return &actionOperationTx{script: c.script}, nil
}

func TestActionOperationDBScriptRejectsWrongSelectorsAndIsolation(t *testing.T) {
	script := &actionOperationScript{actor: models.User{ID: 10}, now: 100, keyHex: strings.Repeat("a", 64), operation: &models.AdminOperation{ID: 30}}
	conn := &actionOperationConn{script: script}
	badQueries := []struct {
		query string
		args  []driver.NamedValue
	}{
		{query: "SELECT * FROM `admin_operations` WHERE actor_user_id = ? AND action = ? AND idempotency_key_hmac = ? AND is_deleted = 0 LIMIT ? FOR UPDATE", args: []driver.NamedValue{{Value: int64(10)}, {Value: int64(testNoopAction)}, {Value: script.keyHex}, {Value: int64(1)}}},
		{query: "SELECT * FROM `admin_operations` WHERE actor_user_id = ? AND action = ? AND idempotency_key_hmac = ? LIMIT ? FOR UPDATE", args: []driver.NamedValue{{Value: int64(99)}, {Value: int64(testNoopAction)}, {Value: script.keyHex}, {Value: int64(1)}}},
		{query: "SELECT * FROM `admin_action_verifications` WHERE consumed_at IS NULL FOR UPDATE", args: nil},
		{query: "SELECT * FROM `user_sessions` WHERE sid = ? FOR UPDATE", args: []driver.NamedValue{{Value: "raw-secret"}}},
	}
	for _, tc := range badQueries {
		if _, err := conn.QueryContext(context.Background(), tc.query, tc.args); err == nil {
			t.Fatalf("script accepted malformed selector %q", tc.query)
		}
	}
	if _, err := conn.BeginTx(context.Background(), driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSerializable)}); err == nil {
		t.Fatal("script accepted non-READ-COMMITTED transaction")
	}
	lease := int64(1)
	script.operation = &models.AdminOperation{ID: 30, State: models.OperationProcessing, LeaseExpiresAt: &lease, QueryExpiresAt: 200}
	badWrites := []struct {
		query string
		args  []driver.NamedValue
	}{
		{query: "UPDATE `admin_operations` SET `verification_id`=? WHERE id = ? AND verification_id IS NULL", args: []driver.NamedValue{{Value: int64(40)}, {Value: int64(30)}}},
		{query: "UPDATE `admin_operations` SET `created_at`=?,`lease_expires_at`=?,`query_expires_at`=?,`updated_at`=?,`updated_by`=?,`verification_id`=? WHERE id = ? AND state = ? AND is_deleted = 0 AND verification_id IS NULL", args: []driver.NamedValue{{Value: "bad-time"}, {Value: int64(30100)}, {Value: int64(2592000100)}, {Value: int64(100)}, {Value: int64(10)}, {Value: int64(40)}, {Value: int64(30)}, {Value: int64(models.OperationProcessing)}}},
		{query: "UPDATE `admin_operations` SET `is_deleted`=1,`lease_owner_hmac`=NULL,`error_code`=NULL,`result_http_status`=NULL WHERE id = ? AND state = ? AND is_deleted = 0", args: []driver.NamedValue{{Value: int64(30)}, {Value: int64(models.OperationProcessing)}}},
		{query: "UPDATE `admin_operations` SET `lease_owner_hmac`=NULL,`lease_expires_at`=NULL WHERE id = ? AND state = ? AND is_deleted = 0 AND lease_expires_at = ?", args: []driver.NamedValue{{Value: int64(30)}, {Value: int64(models.OperationProcessing)}, {Value: lease}}},
	}
	for _, tc := range badWrites {
		if _, err := conn.ExecContext(context.Background(), tc.query, tc.args); err == nil {
			t.Fatalf("script accepted malformed write %q", tc.query)
		}
	}
}
func (c *actionOperationConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case *int64:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = *typed
		}
	case *int:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = int64(*typed)
		}
	case *string:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = *typed
		}
	case models.AdminOperationState:
		value.Value = int64(typed)
	case models.AdminOperationFailure:
		value.Value = int64(typed)
	case models.AdminResultKind:
		value.Value = int64(typed)
	}
	return nil
}

func (c *actionOperationConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.script
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, query)
	s.queryArgs = append(s.queryArgs, append([]driver.NamedValue(nil), args...))
	s.events = append(s.events, "query:"+query)
	switch {
	case strings.Contains(query, "FROM `users`") && strings.Contains(query, "guid = ?"):
		if !strings.Contains(query, "is_deleted = 0") || !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("target not locked by visible guid")
		}
		columns := []string{"id", "guid", "role", "status", "is_deleted", "auth_version"}
		if s.target == nil {
			return operationRows(columns, nil), nil
		}
		u := s.target
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(u.Guid) {
			return nil, errors.New("wrong target selector vars")
		}
		return operationRows(columns, [][]driver.Value{{u.ID, u.Guid, int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}}), nil
	case strings.Contains(query, "FROM `users`"):
		if !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("user not locked")
		}
		u := s.actor
		if strings.Contains(query, "guid = ?") {
			if s.target == nil || len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.target.Guid) {
				return operationRows([]string{"id", "guid", "password_hash", "role", "status", "is_deleted", "auth_version"}, nil), nil
			}
			u = *s.target
		} else if !strings.Contains(query, "id = ?") || len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(u.ID) {
			return nil, errors.New("wrong actor selector vars")
		}
		var password driver.Value
		if u.PasswordHash != nil {
			password = *u.PasswordHash
		}
		return operationRows([]string{"id", "guid", "password_hash", "role", "status", "is_deleted", "auth_version"}, [][]driver.Value{{u.ID, u.Guid, password, int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}}), nil
	case strings.Contains(query, "FROM `user_sessions`"):
		if strings.Contains(query, "sid = ?") || !strings.Contains(query, "user_id = ?") || !strings.Contains(query, "ORDER BY id ASC") || !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("unsafe session lock")
		}
		values := make([][]driver.Value, 0, len(s.sessions))
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.actor.ID) || fmt.Sprint(args[1].Value) != fmt.Sprint(s.now) {
			return nil, errors.New("wrong session selector vars")
		}
		for i := range s.sessions {
			row := s.sessions[i]
			var revoked driver.Value
			if row.RevokedAt != nil {
				revoked = *row.RevokedAt
			}
			values = append(values, []driver.Value{row.ID, row.Guid, row.SID, row.UserID, int64(row.SessionVersion), int64(row.IsDeleted), revoked, row.ExpiresAt})
		}
		return operationRows([]string{"id", "guid", "sid", "user_id", "session_version", "is_deleted", "revoked_at", "expires_at"}, values), nil
	case strings.Contains(query, "FROM `admin_operations`"):
		if !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("operation not locked")
		}
		if strings.Contains(query, "is_deleted = 0") && strings.Contains(query, "idempotency_key_hmac") {
			return nil, errors.New("operation lookup excluded tombstones")
		}
		if strings.Contains(query, "idempotency_key_hmac = ?") {
			if len(args) != 4 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.actor.ID) || fmt.Sprint(args[1].Value) != fmt.Sprint(int(testNoopAction)) || fmt.Sprint(args[2].Value) != s.keyHex {
				return nil, errors.New("wrong operation selector vars")
			}
		} else if strings.Contains(query, "id = ?") && (s.operation == nil || len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.operation.ID)) {
			return nil, errors.New("wrong operation id selector vars")
		}
		if s.operation == nil {
			return operationRows(operationColumns(), nil), nil
		}
		return operationRows(operationColumns(), [][]driver.Value{operationValues(*s.operation)}), nil
	case strings.Contains(query, "FROM `admin_action_verifications`"):
		if (!strings.Contains(query, "ticket_hmac = ?") && !strings.Contains(query, "id = ?")) || !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("verification not locked by digest")
		}
		if s.verification == nil {
			return operationRows(verificationColumns(), nil), nil
		}
		if len(args) < 1 {
			return nil, errors.New("missing verification selector")
		}
		if strings.Contains(query, "ticket_hmac = ?") && fmt.Sprint(args[0].Value) != s.verification.TicketHMAC {
			return operationRows(verificationColumns(), nil), nil
		}
		if strings.Contains(query, "id = ?") && fmt.Sprint(args[0].Value) != fmt.Sprint(s.verification.ID) {
			return operationRows(verificationColumns(), nil), nil
		}
		return operationRows(verificationColumns(), [][]driver.Value{verificationValues(*s.verification)}), nil
	case strings.Contains(query, "FROM `user_permission_heads`"):
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.actor.ID) {
			return nil, errors.New("wrong policy head selector vars")
		}
		columns := []string{"id", "guid", "is_deleted", "policy_version", "catalog_version", "rule_count"}
		if s.policyHead == nil {
			return operationRows(columns, nil), nil
		}
		h := s.policyHead
		return operationRows(columns, [][]driver.Value{{h.ID, h.Guid, int64(h.IsDeleted), h.PolicyVersion, int64(h.CatalogVersion), int64(h.RuleCount)}}), nil
	case strings.Contains(query, "FROM `user_permission_overrides`"):
		if len(args) < 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.actor.ID) {
			return nil, errors.New("wrong policy rule selector vars")
		}
		if s.policyHead == nil {
			return operationRows([]string{"id"}, nil), nil
		}
		columns := []string{"id", "guid", "is_deleted", "policy_version", "capability", "effect"}
		values := make([][]driver.Value, 0, len(s.overrides))
		for i := range s.overrides {
			r := s.overrides[i]
			values = append(values, []driver.Value{r.ID, r.Guid, int64(r.IsDeleted), r.PolicyVersion, int64(r.Capability), int64(r.Effect)})
		}
		return operationRows(columns, values), nil
	default:
		return nil, fmt.Errorf("unexpected operation query: %s", query)
	}
}

func (c *actionOperationConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s := c.script
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execs = append(s.execs, query)
	s.execArgs = append(s.execArgs, append([]driver.NamedValue(nil), args...))
	s.events = append(s.events, "exec:"+query)
	if err := validateActionOperationExec(s, query, args); err != nil {
		return nil, err
	}
	if s.failExec || (s.failExecAt > 0 && len(s.execs) == s.failExecAt) {
		if s.execError != nil {
			return nil, s.execError
		}
		return nil, errors.New("scripted operation write failure")
	}
	affected := int64(1)
	if s.zeroAffected {
		affected = 0
	}
	if affected == 1 && s.operation != nil && strings.HasPrefix(query, "UPDATE `admin_operations`") {
		copy := *s.operation
		if strings.Contains(query, "query_expires_at <= ?") {
			copy.State = models.OperationExpired
			copy.IsDeleted = 1
			copy.LeaseOwnerHMAC = nil
			copy.LeaseExpiresAt = nil
			copy.ErrorCode = nil
			copy.ResultKind = nil
			copy.ResultGUID = nil
			copy.ResultHTTPStatus = nil
		} else if !strings.Contains(query, "verification_id IS NULL") {
			copy.State = models.OperationPendingRecovery
			copy.LeaseOwnerHMAC = nil
			copy.LeaseExpiresAt = nil
		}
		s.pendingOperation = &copy
	}
	if affected == 1 && strings.HasPrefix(query, "INSERT INTO `admin_operations`") {
		pending, err := operationFromInsert(query, args)
		if err != nil {
			return nil, err
		}
		s.pendingOperation = pending
	}
	if affected == 1 && strings.HasPrefix(query, "UPDATE `admin_operations`") && strings.Contains(query, "verification_id IS NULL") && s.pendingOperation != nil {
		values, err := operationUpdateValues(query, args)
		if err != nil {
			return nil, err
		}
		copy := *s.pendingOperation
		copy.CreatedAt = mustOperationInt64(values["created_at"])
		copy.UpdatedAt = mustOperationInt64(values["updated_at"])
		leaseExpires := mustOperationInt64(values["lease_expires_at"])
		copy.LeaseExpiresAt = &leaseExpires
		copy.QueryExpiresAt = mustOperationInt64(values["query_expires_at"])
		verificationID := mustOperationInt64(values["verification_id"])
		copy.VerificationID = &verificationID
		s.pendingOperation = &copy
	}
	return actionOperationResult{id: 30, affected: affected}, nil
}

func validateActionOperationExec(script *actionOperationScript, query string, args []driver.NamedValue) error {
	requireFragments := func(fragments ...string) error {
		for _, fragment := range fragments {
			if !strings.Contains(query, fragment) {
				return fmt.Errorf("operation write missing %q", fragment)
			}
		}
		return nil
	}
	requireTail := func(expected ...any) error {
		if len(args) < len(expected) {
			return fmt.Errorf("operation write args %d, need tail %d", len(args), len(expected))
		}
		offset := len(args) - len(expected)
		for i := range expected {
			if fmt.Sprint(args[offset+i].Value) != fmt.Sprint(expected[i]) {
				return fmt.Errorf("operation write tail arg %d=%v, want %v", i, args[offset+i].Value, expected[i])
			}
		}
		return nil
	}
	requireHead := func(expected ...any) error {
		if len(args) < len(expected) {
			return fmt.Errorf("operation write args %d, need head %d", len(args), len(expected))
		}
		for i := range expected {
			if fmt.Sprint(args[i].Value) != fmt.Sprint(expected[i]) {
				return fmt.Errorf("operation write head arg %d=%v, want %v", i, args[i].Value, expected[i])
			}
		}
		return nil
	}
	switch {
	case strings.HasPrefix(query, "INSERT INTO `admin_operations`"):
		return requireFragments("`actor_user_id`", "`idempotency_key_hmac`", "`request_hmac`", "`state`", "`lease_owner_hmac`", "`lease_expires_at`", "`query_expires_at`", "`created_by`", "`updated_by`")
	case strings.HasPrefix(query, "UPDATE `admin_operations`") && strings.Contains(query, "verification_id IS NULL"):
		if err := requireFragments("SET", "`created_at`=?", "`lease_expires_at`=?", "`query_expires_at`=?", "`updated_at`=?", "`updated_by`=?", "`verification_id`=?", "id = ? AND state = ? AND is_deleted = 0 AND verification_id IS NULL"); err != nil {
			return err
		}
		if len(args) != 8 || script.verification == nil {
			return errors.New("verification reservation has wrong args")
		}
		createdAt, createdOK := operationInt64(args[0].Value)
		leaseExpires, leaseOK := operationInt64(args[1].Value)
		queryExpires, queryOK := operationInt64(args[2].Value)
		if !createdOK || !leaseOK || !queryOK ||
			fmt.Sprint(args[0].Value) != fmt.Sprint(args[3].Value) ||
			leaseExpires != createdAt+actionOperationLeaseMillis || queryExpires != createdAt+actionOperationQueryRetentionMS ||
			fmt.Sprint(args[4].Value) != fmt.Sprint(script.actor.ID) || fmt.Sprint(args[5].Value) != fmt.Sprint(script.verification.ID) {
			return errors.New("verification reservation did not refresh final timestamps and audit")
		}
		return requireTail(int64(30), int64(models.OperationProcessing))
	case strings.HasPrefix(query, "UPDATE `admin_operations`") && strings.Contains(query, "query_expires_at <= ?"):
		if script.operation == nil {
			return errors.New("expiry write has no locked operation")
		}
		if err := requireFragments("id = ? AND state = ? AND is_deleted = 0 AND query_expires_at = ? AND query_expires_at <= ?", "`lease_owner_hmac`=?", "`error_code`=?", "`result_http_status`=?"); err != nil {
			return err
		}
		return requireTail(script.operation.ID, int64(script.operation.State), script.operation.QueryExpiresAt, script.now)
	case strings.HasPrefix(query, "UPDATE `admin_operations`"):
		if script.operation == nil || script.operation.LeaseExpiresAt == nil {
			return errors.New("recovery write has no locked lease")
		}
		if err := requireFragments("id = ? AND state = ? AND is_deleted = 0 AND lease_expires_at = ? AND lease_expires_at < ?", "`lease_owner_hmac`=?", "`lease_expires_at`=?"); err != nil {
			return err
		}
		if err := requireHead(nil, nil, int64(models.OperationPendingRecovery), script.now, nil); err != nil {
			return err
		}
		return requireTail(script.operation.ID, int64(models.OperationProcessing), *script.operation.LeaseExpiresAt, script.now-actionOperationRecoveryGraceMS)
	default:
		return fmt.Errorf("unexpected operation write: %s", query)
	}
}
func (tx *actionOperationTx) Commit() error {
	tx.script.mu.Lock()
	if tx.script.failCommit {
		tx.script.pendingOperation = nil
		tx.script.mu.Unlock()
		return errors.New("scripted commit failure")
	}
	if tx.script.pendingOperation != nil {
		if tx.script.operation == nil {
			copy := *tx.script.pendingOperation
			tx.script.operation = &copy
		} else {
			*tx.script.operation = *tx.script.pendingOperation
		}
	}
	tx.script.pendingOperation = nil
	tx.script.commitCount++
	tx.script.mu.Unlock()
	return nil
}

func operationFromInsert(query string, args []driver.NamedValue) (*models.AdminOperation, error) {
	open := strings.Index(query, "(")
	close := strings.Index(query, ") VALUES")
	if open < 0 || close <= open {
		return nil, errors.New("unparseable operation INSERT")
	}
	columns := strings.Split(query[open+1:close], ",")
	if len(columns) != len(args) {
		return nil, errors.New("operation INSERT columns/args mismatch")
	}
	values := make(map[string]any, len(columns))
	for i, column := range columns {
		values[strings.Trim(strings.TrimSpace(column), "`")] = args[i].Value
	}
	createdBy := mustOperationInt64(values["created_by"])
	updatedBy := mustOperationInt64(values["updated_by"])
	leaseHex := fmt.Sprint(values["lease_owner_hmac"])
	leaseExpires := mustOperationInt64(values["lease_expires_at"])
	return &models.AdminOperation{
		ID:          30,
		AuditFields: models.AuditFields{Guid: mustOperationInt64(values["guid"]), CreatedAt: mustOperationInt64(values["created_at"]), CreatedBy: &createdBy, UpdatedAt: mustOperationInt64(values["updated_at"]), UpdatedBy: &updatedBy},
		ActorUserID: mustOperationInt64(values["actor_user_id"]), ActorAuthVersion: int(mustOperationInt64(values["actor_auth_version"])),
		SessionID: mustOperationInt64(values["session_id"]), Action: int(mustOperationInt64(values["action"])),
		IdempotencyKeyHMAC: fmt.Sprint(values["idempotency_key_hmac"]), RequestHMAC: fmt.Sprint(values["request_hmac"]),
		State: models.AdminOperationState(mustOperationInt64(values["state"])), PublicRef: fmt.Sprint(values["public_ref"]),
		LeaseOwnerHMAC: &leaseHex, LeaseExpiresAt: &leaseExpires, QueryExpiresAt: mustOperationInt64(values["query_expires_at"]),
	}, nil
}

func operationUpdateValues(query string, args []driver.NamedValue) (map[string]any, error) {
	setAt := strings.Index(query, " SET ")
	whereAt := strings.Index(query, " WHERE ")
	if setAt < 0 || whereAt <= setAt {
		return nil, errors.New("unparseable operation UPDATE")
	}
	assignments := strings.Split(query[setAt+5:whereAt], ",")
	if len(args) < len(assignments) {
		return nil, errors.New("operation UPDATE assignments/args mismatch")
	}
	values := make(map[string]any, len(assignments))
	for i, assignment := range assignments {
		column := strings.Trim(strings.TrimSpace(strings.SplitN(assignment, "=", 2)[0]), "`")
		values[column] = args[i].Value
	}
	return values, nil
}

func mustOperationInt64(value any) int64 {
	parsed, _ := operationInt64(value)
	return parsed
}

func operationInt64(value any) (int64, bool) {
	parsed, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	return parsed, err == nil
}
func (tx *actionOperationTx) Rollback() error {
	tx.script.mu.Lock()
	tx.script.pendingOperation = nil
	tx.script.rollbackCount++
	tx.script.mu.Unlock()
	return nil
}
func (r *actionOperationRows) Columns() []string { return r.columns }
func (r *actionOperationRows) Close() error      { return nil }
func (r *actionOperationRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
func (r actionOperationResult) LastInsertId() (int64, error) { return r.id, nil }
func (r actionOperationResult) RowsAffected() (int64, error) { return r.affected, nil }

func operationRows(columns []string, values [][]driver.Value) *actionOperationRows {
	return &actionOperationRows{columns: columns, values: values}
}
func operationColumns() []string {
	return []string{"id", "guid", "created_at", "created_by", "updated_at", "updated_by", "is_deleted", "actor_user_id", "actor_auth_version", "session_id", "action", "idempotency_key_hmac", "request_hmac", "verification_id", "state", "public_ref", "lease_owner_hmac", "lease_expires_at", "finished_at", "query_expires_at", "error_code", "result_kind", "result_guid", "result_http_status"}
}
func operationValues(o models.AdminOperation) []driver.Value {
	return []driver.Value{o.ID, o.Guid, o.CreatedAt, ptrDriver(o.CreatedBy), o.UpdatedAt, ptrDriver(o.UpdatedBy), int64(o.IsDeleted), o.ActorUserID, int64(o.ActorAuthVersion), o.SessionID, int64(o.Action), o.IdempotencyKeyHMAC, o.RequestHMAC, ptrDriver(o.VerificationID), int64(o.State), o.PublicRef, ptrDriver(o.LeaseOwnerHMAC), ptrDriver(o.LeaseExpiresAt), ptrDriver(o.FinishedAt), o.QueryExpiresAt, ptrDriver(o.ErrorCode), ptrDriver(o.ResultKind), ptrDriver(o.ResultGUID), ptrDriver(o.ResultHTTPStatus)}
}
func verificationColumns() []string {
	return []string{"id", "guid", "created_at", "created_by", "updated_at", "updated_by", "is_deleted", "actor_user_id", "actor_auth_version", "session_id", "action", "target_kind", "target_guid", "intent_hmac", "ticket_hmac", "expires_at", "consumed_at"}
}
func verificationValues(v models.AdminActionVerification) []driver.Value {
	return []driver.Value{v.ID, v.Guid, v.CreatedAt, ptrDriver(v.CreatedBy), v.UpdatedAt, ptrDriver(v.UpdatedBy), int64(v.IsDeleted), v.ActorUserID, int64(v.ActorAuthVersion), v.SessionID, int64(v.Action), int64(v.TargetKind), ptrDriver(v.TargetGUID), v.IntentHMAC, v.TicketHMAC, v.ExpiresAt, ptrDriver(v.ConsumedAt)}
}
func ptrDriver(value any) driver.Value {
	switch v := value.(type) {
	case *int64:
		if v != nil {
			return *v
		}
	case *int:
		if v != nil {
			return int64(*v)
		}
	case *string:
		if v != nil {
			return *v
		}
	case *models.AdminOperationFailure:
		if v != nil {
			return int64(*v)
		}
	case *models.AdminResultKind:
		if v != nil {
			return int64(*v)
		}
	}
	return nil
}

func openActionOperationScriptDB(t *testing.T, script *actionOperationScript) *gorm.DB {
	t.Helper()
	actionOperationScriptOnce.Do(func() { sql.Register(actionOperationScriptDriverName, actionOperationDriver{}) })
	name := fmt.Sprintf("operation-%d", actionOperationScriptSeq.Add(1))
	actionOperationScripts.Store(name, script)
	t.Cleanup(func() { actionOperationScripts.Delete(name) })
	sqlDB, err := sql.Open(actionOperationScriptDriverName, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestActionOperationBeginDBScriptEnforcesLockOrderAndSecretFreeSQL(t *testing.T) {
	now := int64(1_800_000_000_000)
	service, script, actor, key, ticket := actionOperationFixture(t, now, nil)
	identity, view, err := service.Begin(context.Background(), OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: testNoopIntent(testNoopTargetGUID, "same-intent")})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if identity == nil || identity.ID != 30 || identity.PublicRef == "" || identity.capability == nil {
		t.Fatalf("identity = %#v", identity)
	}
	identity.capability.mu.Lock()
	capabilityReady := !identity.capability.consumed && !operationLeaseIsZero(&identity.capability.raw)
	identity.capability.mu.Unlock()
	if !capabilityReady {
		t.Fatal("Begin did not return a ready one-shot lease capability")
	}
	if view == nil || view.Status != "processing" || view.Scope != "test.noop" || view.RetryAfterSeconds != 30 {
		t.Fatalf("view = %#v", view)
	}
	if got := actionOperationQueryKinds(script.queries); strings.Join(got, ",") != "actor,session,operation,verification,target,target_or_policy,target_or_policy" {
		t.Fatalf("lock order = %v", got)
	}
	if err := validateNewOperationEventOrder(script.events); err != nil {
		t.Fatalf("query/write lock order: %v; events=%v", err, script.events)
	}
	if script.commitCount != 1 || script.rollbackCount != 0 {
		t.Fatalf("commit/rollback = %d/%d", script.commitCount, script.rollbackCount)
	}
	if len(script.isolations) != 1 || script.isolations[0] != driver.IsolationLevel(sql.LevelReadCommitted) {
		t.Fatalf("transaction isolation = %v", script.isolations)
	}
	joined := actionOperationSQLValues(script)
	for _, secret := range []string{actor.SessionSID, key, ticket, "same-intent"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("SQL retained raw secret %q", secret)
		}
	}
	if len(script.execs) != 2 || !strings.Contains(script.execs[0], "INSERT INTO `admin_operations`") || !strings.Contains(script.execs[1], "verification_id") {
		t.Fatalf("writes = %v", script.execs)
	}
	inserted := actionOperationInsertValues(t, script.execs[0], script.execArgs[0])
	for column, want := range map[string]string{
		"guid": "3001", "created_at": fmt.Sprint(now), "created_by": "10", "updated_at": fmt.Sprint(now), "updated_by": "10",
		"is_deleted": "0", "actor_user_id": "10", "actor_auth_version": "7", "session_id": "20", "action": fmt.Sprint(int(testNoopAction)),
		"idempotency_key_hmac": script.keyHex, "request_hmac": script.verification.IntentHMAC, "state": fmt.Sprint(int(models.OperationProcessing)),
		"lease_expires_at": fmt.Sprint(now + actionOperationLeaseMillis), "query_expires_at": fmt.Sprint(now + actionOperationQueryRetentionMS),
	} {
		if got := inserted[column]; got != want {
			t.Fatalf("INSERT %s=%q, want %q", column, got, want)
		}
	}
	if len(inserted["public_ref"]) != 46 || len(inserted["lease_owner_hmac"]) != 64 {
		t.Fatalf("public ref / lease HMAC shapes = %d/%d", len(inserted["public_ref"]), len(inserted["lease_owner_hmac"]))
	}
	updated := actionOperationUpdateValues(t, script.execs[1], script.execArgs[1])
	for column, want := range map[string]string{"verification_id": "40", "updated_at": fmt.Sprint(now), "updated_by": "10"} {
		if got := updated[column]; got != want {
			t.Fatalf("verification reservation %s=%q, want %q", column, got, want)
		}
	}
}

func TestActionOperationDBScriptRejectsWrongNewOperationEventOrder(t *testing.T) {
	wrong := []string{
		"query:SELECT * FROM `admin_operations` FOR UPDATE",
		"query:SELECT * FROM `admin_action_verifications` FOR UPDATE",
		"exec:INSERT INTO `admin_operations` VALUES (?)",
		"query:SELECT * FROM `user_permission_heads` FOR UPDATE",
	}
	if err := validateNewOperationEventOrder(wrong); err == nil {
		t.Fatal("event-order validator accepted verification before INSERT")
	}
}

func validateNewOperationEventOrder(events []string) error {
	index := func(prefix, fragment string) int {
		for i, event := range events {
			if strings.HasPrefix(event, prefix) && strings.Contains(event, fragment) {
				return i
			}
		}
		return -1
	}
	operation := index("query:", "FROM `admin_operations`")
	insert := index("exec:", "INSERT INTO `admin_operations`")
	verification := index("query:", "FROM `admin_action_verifications`")
	target := index("query:", "FROM `users` WHERE guid = ?")
	policy := index("query:", "FROM `user_permission_heads`")
	if operation < 0 || insert < 0 || verification < 0 || policy < 0 || !(operation < insert && insert < verification && verification < policy) {
		return fmt.Errorf("want operation SELECT < INSERT < verification SELECT < policy SELECT, got %d < %d < %d < %d", operation, insert, verification, policy)
	}
	if target >= 0 && verification >= target {
		return fmt.Errorf("want verification SELECT < target SELECT, got %d < %d", verification, target)
	}
	return nil
}

func actionOperationInsertValues(t *testing.T, query string, args []driver.NamedValue) map[string]string {
	t.Helper()
	open := strings.Index(query, "(")
	close := strings.Index(query, ") VALUES")
	if open < 0 || close <= open {
		t.Fatalf("unparseable INSERT: %s", query)
	}
	columns := strings.Split(query[open+1:close], ",")
	if len(columns) != len(args) {
		t.Fatalf("INSERT columns/args = %d/%d", len(columns), len(args))
	}
	out := make(map[string]string, len(columns))
	for i, column := range columns {
		out[strings.Trim(strings.TrimSpace(column), "`")] = fmt.Sprint(args[i].Value)
	}
	return out
}

func actionOperationUpdateValues(t *testing.T, query string, args []driver.NamedValue) map[string]string {
	t.Helper()
	setAt := strings.Index(query, " SET ")
	whereAt := strings.Index(query, " WHERE ")
	if setAt < 0 || whereAt <= setAt {
		t.Fatalf("unparseable UPDATE: %s", query)
	}
	assignments := strings.Split(query[setAt+5:whereAt], ",")
	if len(args) < len(assignments) {
		t.Fatalf("UPDATE assignments/args = %d/%d", len(assignments), len(args))
	}
	out := make(map[string]string, len(assignments))
	for i, assignment := range assignments {
		column := strings.Trim(strings.TrimSpace(strings.SplitN(assignment, "=", 2)[0]), "`")
		out[column] = fmt.Sprint(args[i].Value)
	}
	return out
}

func actionOperationQueryKinds(queries []string) []string {
	out := make([]string, 0, len(queries))
	for _, query := range queries {
		switch {
		case strings.Contains(query, "FROM `users`") && strings.Contains(query, "guid = ?"):
			out = append(out, "target")
		case strings.Contains(query, "FROM `users`"):
			out = append(out, "actor")
		case strings.Contains(query, "FROM `user_sessions`"):
			out = append(out, "session")
		case strings.Contains(query, "FROM `admin_operations`"):
			out = append(out, "operation")
		case strings.Contains(query, "FROM `admin_action_verifications`"):
			out = append(out, "verification")
		case strings.Contains(query, "FROM `user_permission_"):
			out = append(out, "target_or_policy")
		}
	}
	return out
}
func actionOperationSQLValues(script *actionOperationScript) string {
	var b strings.Builder
	for _, sets := range [][][]driver.NamedValue{script.queryArgs, script.execArgs} {
		for _, args := range sets {
			for _, arg := range args {
				fmt.Fprint(&b, arg.Value, "|")
			}
		}
	}
	return b.String()
}
