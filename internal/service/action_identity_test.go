package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const testNoopAction actionsecurity.Action = 2147483000

type actionIssueClock struct{ now int64 }

func (c *actionIssueClock) NowMillis() int64 { return c.now }

type actionIssueRedisClient struct {
	*actionRateEvalClient
	revoked bool
	err     error
}

func (c *actionIssueRedisClient) Exists(ctx context.Context, keys ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx)
	if c.err != nil {
		cmd.SetErr(c.err)
	} else if c.revoked {
		cmd.SetVal(1)
	} else {
		cmd.SetVal(0)
	}
	return cmd
}

func testNoopDescriptor() actionsecurity.Descriptor {
	return actionsecurity.Descriptor{
		Action: testNoopAction, Name: "test.noop", Capability: "users.create", RootOnly: true,
		RequiresTicket: true, Active: true, TargetKind: actionsecurity.TargetNone,
		Encode: func(value any) ([]byte, error) {
			text, ok := value.(string)
			if !ok || text == "" {
				return nil, errors.New("invalid test intent")
			}
			return []byte(text), nil
		},
	}
}

func newTestActionVerificationService(t *testing.T, db *gorm.DB, client *actionIssueRedisClient, clock persistence.Clock, random io.Reader, nextGUID func() int64) *ActionVerificationService {
	t.Helper()
	root := bytes.Repeat([]byte{0x33}, 32)
	crypto, err := actionsecurity.NewCrypto(root)
	clear(root)
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := NewActionSecurityRedis(client, crypto)
	if err != nil {
		t.Fatal(err)
	}
	authRedis, err := NewAuthRedis(client, "task7-auth-hmac-key-material")
	if err != nil {
		t.Fatal(err)
	}
	service, err := newActionVerificationService(db, limiter, authRedis, crypto, func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		if action == testNoopAction {
			return testNoopDescriptor(), true
		}
		return actionsecurity.Descriptor{}, false
	}, clock, random, nextGUID)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestActionIdentityContractIsTyped(t *testing.T) {
	actor := ActionActor{UserID: 1, UserGUID: 2, AuthVersion: 3, SessionSID: "sid", SessionVersion: 4}
	issue := VerificationIssue{Action: actionsecurity.Action(1), Actor: actor}
	if issue.Actor != actor {
		t.Fatal("verification issue did not retain its typed actor")
	}
}

func TestActionSecurityConstructorRejectsPartialAndTypedNilDependencies(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	root := bytes.Repeat([]byte{0x44}, 32)
	crypto, err := actionsecurity.NewCrypto(root)
	clear(root)
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := NewActionSecurityRedis(client, crypto)
	if err != nil {
		t.Fatal(err)
	}
	authRedis, err := NewAuthRedis(client, "task7-constructor-hmac-key")
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		return actionsecurity.Descriptor{}, false
	}
	clock := &actionIssueClock{now: time.Now().UnixMilli()}
	reader := bytes.NewReader(bytes.Repeat([]byte{1}, 32))
	guid := func() int64 { return 1 }
	valid := []any{db, limiter, authRedis, crypto, resolver, clock, reader, guid}
	for index := range valid {
		args := append([]any(nil), valid...)
		args[index] = nil
		_, err := newActionVerificationService(valueDB(args[0]), valueLimiter(args[1]), valueAuthRedis(args[2]), valueCrypto(args[3]), valueResolver(args[4]), valueClock(args[5]), valueReader(args[6]), valueGUID(args[7]))
		if !errors.Is(err, ErrActionVerificationUnavailable) {
			t.Fatalf("nil dependency %d error = %v", index, err)
		}
	}

	var nilClock *actionIssueClock
	if _, err := newActionVerificationService(db, limiter, authRedis, crypto, resolver, nilClock, reader, guid); !errors.Is(err, ErrActionVerificationUnavailable) {
		t.Fatalf("typed nil clock error = %v", err)
	}
	var nilReader *bytes.Reader
	if _, err := newActionVerificationService(db, limiter, authRedis, crypto, resolver, clock, nilReader, guid); !errors.Is(err, ErrActionVerificationUnavailable) {
		t.Fatalf("typed nil reader error = %v", err)
	}
}

func valueLimiter(value any) *ActionSecurityRedis {
	if value == nil {
		return nil
	}
	return value.(*ActionSecurityRedis)
}
func valueDB(value any) *gorm.DB {
	if value == nil {
		return nil
	}
	return value.(*gorm.DB)
}
func valueAuthRedis(value any) *AuthRedis {
	if value == nil {
		return nil
	}
	return value.(*AuthRedis)
}
func valueCrypto(value any) *actionsecurity.Crypto {
	if value == nil {
		return nil
	}
	return value.(*actionsecurity.Crypto)
}
func valueResolver(value any) func(actionsecurity.Action) (actionsecurity.Descriptor, bool) {
	if value == nil {
		return nil
	}
	return value.(func(actionsecurity.Action) (actionsecurity.Descriptor, bool))
}
func valueClock(value any) persistence.Clock {
	if value == nil {
		return nil
	}
	return value.(persistence.Clock)
}
func valueReader(value any) io.Reader {
	if value == nil {
		return nil
	}
	return value.(io.Reader)
}
func valueGUID(value any) func() int64 {
	if value == nil {
		return nil
	}
	return value.(func() int64)
}

func TestActionVerificationIssueInactiveFailsBeforeRedisAndClearsPassword(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	service := newTestActionVerificationService(t, db, client, &actionIssueClock{now: 1_800_000_000_000}, bytes.NewReader(bytes.Repeat([]byte{1}, 32)), func() int64 { return 1 })
	service.resolve = actionsecurity.ResolveActiveAction
	password := []byte("sensitive-password")
	_, err := service.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, CurrentPassword: password})
	if !errors.Is(err, ErrActionVerificationInactive) {
		t.Fatalf("Issue inactive error = %v", err)
	}
	if client.evalCalls != 0 {
		t.Fatalf("inactive action reached Redis: %d calls", client.evalCalls)
	}
	if !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("inactive Issue did not clear caller password buffer")
	}
}

func TestActionVerificationIssueLimiterThenRevocationFailClosedBeforeMySQL(t *testing.T) {
	db := actionIssueDryDB(t)
	for _, tc := range []struct {
		name    string
		revoked bool
		err     error
		want    error
	}{
		{name: "revoked", revoked: true, want: ErrActionVerificationForbidden},
		{name: "redis error", err: errors.New("private redis detail"), want: ErrActionVerificationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient(), revoked: tc.revoked, err: tc.err}
			service := newTestActionVerificationService(t, db, client, &actionIssueClock{now: 1_800_000_000_000}, bytes.NewReader(bytes.Repeat([]byte{1}, 32)), func() int64 { return 1 })
			password := []byte("sensitive-password")
			_, err := service.Issue(context.Background(), VerificationIssue{
				Action: testNoopAction,
				Actor:  ActionActor{UserID: 1, UserGUID: 2, AuthVersion: 3, SessionSID: "sid", SessionVersion: 4},
				Intent: "intent", CurrentPassword: password, TrustedIP: "203.0.113.10",
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Issue error = %v, want %v", err, tc.want)
			}
			if client.evalCalls != 1 {
				t.Fatalf("limiter calls = %d, want 1 before revocation", client.evalCalls)
			}
			if !bytes.Equal(password, make([]byte, len(password))) {
				t.Fatal("failed Issue did not clear caller password")
			}
		})
	}
}

func actionIssueDryDB(t *testing.T) *gorm.DB {
	t.Helper()
	sqlDB, err := sql.Open("mysql", "task7:task7@tcp(127.0.0.1:1)/task7_test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func actionIssuePasswordHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
