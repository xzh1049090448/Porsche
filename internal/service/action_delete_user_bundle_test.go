package service

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestNewUserDeleteActionsBuildsOneCoherentBundle(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	authRedis, crypto := userDeleteBundleSecurity(t, client)
	clock := &actionIssueClock{now: 1_800_000_000_000}
	random := bytes.NewReader(bytes.Repeat([]byte{0x71}, 128))
	nextGUID := func() int64 { return 9101 }

	bundle, err := newUserDeleteActions(db, authRedis, crypto, actionsecurity.ActiveActionRegistry, clock, random, nextGUID)
	if err != nil {
		t.Fatal(err)
	}
	if bundle == nil || bundle.Verifications == nil || bundle.Operations == nil || bundle.Outbox == nil || bundle.NewExecution == nil {
		t.Fatalf("incomplete bundle: %#v", bundle)
	}
	if bundle.Verifications.redis != bundle.Operations.limiter || bundle.Verifications.redis.client != client ||
		bundle.Verifications.authRedis != authRedis || bundle.Operations.authRedis != authRedis ||
		bundle.Verifications.crypto != crypto || bundle.Operations.crypto != crypto || bundle.Verifications.redis.crypto != crypto {
		t.Fatal("verification and operation services do not share one Redis/client/crypto set")
	}
	if bundle.Verifications.clock != clock || bundle.Operations.clock != clock || bundle.Outbox.clock != clock ||
		bundle.Verifications.random != random || bundle.Operations.random != random ||
		reflect.ValueOf(bundle.Verifications.nextGUID).Pointer() != reflect.ValueOf(nextGUID).Pointer() ||
		reflect.ValueOf(bundle.Operations.nextGUID).Pointer() != reflect.ValueOf(nextGUID).Pointer() ||
		reflect.ValueOf(bundle.Outbox.nextGUID).Pointer() != reflect.ValueOf(nextGUID).Pointer() {
		t.Fatal("bundle did not retain one reviewed persistence dependency set")
	}
	for _, resolver := range []func(actionsecurity.Action) (actionsecurity.Descriptor, bool){bundle.Verifications.resolve, bundle.Operations.resolve} {
		descriptor, ok := resolver(actionsecurity.ActionUsersDelete)
		if !ok || !exactActiveUserDeleteDescriptor(descriptor) {
			t.Fatalf("private resolver rejected users.delete: %#v, %v", descriptor, ok)
		}
		if _, ok := resolver(actionsecurity.ActionUsersResetPassword); ok {
			t.Fatal("private resolver exposed an action outside users.delete")
		}
	}
}

func TestUserDeleteActionsNewExecutionOwnsOneRequest(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	authRedis, crypto := userDeleteBundleSecurity(t, client)
	clock := &actionIssueClock{now: 1_800_000_000_000}
	nextGUID := func() int64 { return 9201 }
	bundle, err := newUserDeleteActions(db, authRedis, crypto, actionsecurity.ActiveActionRegistry, clock, bytes.NewReader(make([]byte, 128)), nextGUID)
	if err != nil {
		t.Fatal(err)
	}

	reasonBytes := []byte("  reviewed deletion  ")
	intent := actionsecurity.DeleteUserIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, Reason: string(reasonBytes)}
	first, err := bundle.NewExecution(intent)
	if err != nil {
		t.Fatal(err)
	}
	reasonBytes[2] = 'X'
	intent.TargetGUID = 7001
	intent.ExpectedAuthVersion = 8
	intent.Reason = "changed"
	second, err := bundle.NewExecution(actionsecurity.DeleteUserIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, Reason: "reviewed deletion"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first.state == second.state || first.intent.TargetGUID != 6001 || first.intent.ExpectedAuthVersion != 7 || first.intent.Reason != "reviewed deletion" {
		t.Fatalf("request executions were shared or aliased: first=%#v second=%#v", first, second)
	}
	if first.clock != clock || second.clock != clock || reflect.ValueOf(first.nextGUID).Pointer() != reflect.ValueOf(nextGUID).Pointer() ||
		reflect.ValueOf(second.nextGUID).Pointer() != reflect.ValueOf(nextGUID).Pointer() {
		t.Fatal("execution did not use bundle persistence dependencies")
	}
	if !first.beginConsumer() || first.beginConsumer() || !second.beginConsumer() || second.beginConsumer() {
		t.Fatal("each request execution must be independently one-shot")
	}
}

func TestUserDeleteActionsNewExecutionStaysBoundToReviewedDescriptor(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	authRedis, crypto := userDeleteBundleSecurity(t, client)
	descriptor := actionsecurity.ActiveActionRegistry()[0]
	productionEncode := descriptor.Encode
	encodeCalls := 0
	descriptor.Encode = func(value any) ([]byte, error) {
		encodeCalls++
		return productionEncode(value)
	}
	bundle, err := newUserDeleteActions(db, authRedis, crypto, func() []actionsecurity.Descriptor {
		return []actionsecurity.Descriptor{descriptor}
	}, &actionIssueClock{now: 1_800_000_000_000}, bytes.NewReader(make([]byte, 128)), func() int64 { return 9251 })
	if err != nil {
		t.Fatal(err)
	}
	encodeCalls = 0
	descriptor.Encode = func(any) ([]byte, error) { return nil, errors.New("injected registry drift") }

	execution, err := bundle.NewExecution(actionsecurity.DeleteUserIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, Reason: "reviewed deletion"})
	if err != nil || execution == nil || encodeCalls != 1 {
		t.Fatalf("execution/error/reviewed encode calls = %#v/%v/%d, want execution/nil/1", execution, err, encodeCalls)
	}
	if invalid, err := bundle.NewExecution(actionsecurity.DeleteUserIntent{TargetGUID: 6001, Reason: "reviewed deletion"}); invalid != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("mismatched intent execution/error = %#v/%v", invalid, err)
	}
}

func TestNewUserDeleteActionsRejectsMissingOrInvalidDependencies(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	authRedis, crypto := userDeleteBundleSecurity(t, client)
	clock := persistence.Clock(&actionIssueClock{now: 1_800_000_000_000})
	random := io.Reader(bytes.NewReader(make([]byte, 128)))
	nextGUID := func() int64 { return 9301 }
	registry := actionsecurity.ActiveActionRegistry

	tests := []struct {
		name      string
		db        *gorm.DB
		authRedis *AuthRedis
		crypto    *actionsecurity.Crypto
		registry  func() []actionsecurity.Descriptor
		clock     persistence.Clock
		random    io.Reader
		nextGUID  func() int64
	}{
		{name: "database", authRedis: authRedis, crypto: crypto, registry: registry, clock: clock, random: random, nextGUID: nextGUID},
		{name: "invalid database", db: &gorm.DB{}, authRedis: authRedis, crypto: crypto, registry: registry, clock: clock, random: random, nextGUID: nextGUID},
		{name: "typed nil database connection", db: &gorm.DB{Statement: &gorm.Statement{ConnPool: (*sql.DB)(nil)}}, authRedis: authRedis, crypto: crypto, registry: registry, clock: clock, random: random, nextGUID: nextGUID},
		{name: "authentication Redis", db: db, crypto: crypto, registry: registry, clock: clock, random: random, nextGUID: nextGUID},
		{name: "crypto", db: db, authRedis: authRedis, registry: registry, clock: clock, random: random, nextGUID: nextGUID},
		{name: "registry", db: db, authRedis: authRedis, crypto: crypto, clock: clock, random: random, nextGUID: nextGUID},
		{name: "clock", db: db, authRedis: authRedis, crypto: crypto, registry: registry, random: random, nextGUID: nextGUID},
		{name: "random", db: db, authRedis: authRedis, crypto: crypto, registry: registry, clock: clock, nextGUID: nextGUID},
		{name: "GUID", db: db, authRedis: authRedis, crypto: crypto, registry: registry, clock: clock, random: random},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle, err := newUserDeleteActions(test.db, test.authRedis, test.crypto, test.registry, test.clock, test.random, test.nextGUID)
			if bundle != nil || !errors.Is(err, ErrActionVerificationUnavailable) {
				t.Fatalf("bundle/error = %#v/%v, want nil/sanitized unavailable", bundle, err)
			}
		})
	}

	unsupported, err := NewAuthRedis(redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{"127.0.0.1:1"}}), "bundle-auth-hmac-key-material")
	if err != nil {
		t.Fatal(err)
	}
	if bundle, err := newUserDeleteActions(db, unsupported, crypto, registry, clock, random, nextGUID); bundle != nil || !errors.Is(err, ErrActionVerificationUnavailable) {
		t.Fatalf("unsupported Redis bundle/error = %#v/%v", bundle, err)
	}
}

func TestNewUserDeleteActionsRejectsRegistryDrift(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	authRedis, crypto := userDeleteBundleSecurity(t, client)
	valid := actionsecurity.ActiveActionRegistry()[0]
	tests := []struct {
		name     string
		registry func() []actionsecurity.Descriptor
	}{
		{name: "empty", registry: func() []actionsecurity.Descriptor { return nil }},
		{name: "extra", registry: func() []actionsecurity.Descriptor { return []actionsecurity.Descriptor{valid, valid} }},
	}
	mutations := []struct {
		name   string
		mutate func(*actionsecurity.Descriptor)
	}{
		{name: "action", mutate: func(d *actionsecurity.Descriptor) { d.Action = actionsecurity.ActionUsersResetPassword }},
		{name: "name", mutate: func(d *actionsecurity.Descriptor) { d.Name = "users.delete.changed" }},
		{name: "capability", mutate: func(d *actionsecurity.Descriptor) { d.Capability = "users.edit" }},
		{name: "root only", mutate: func(d *actionsecurity.Descriptor) { d.RootOnly = true }},
		{name: "ticket", mutate: func(d *actionsecurity.Descriptor) { d.RequiresTicket = false }},
		{name: "inactive", mutate: func(d *actionsecurity.Descriptor) { d.Active = false }},
		{name: "target", mutate: func(d *actionsecurity.Descriptor) { d.TargetKind = actionsecurity.TargetNone }},
		{name: "nil encoder", mutate: func(d *actionsecurity.Descriptor) { d.Encode = nil }},
		{name: "untyped encoder", mutate: func(d *actionsecurity.Descriptor) {
			d.Encode = func(any) ([]byte, error) { return []byte("wrong"), nil }
		}},
	}
	for _, mutation := range mutations {
		mutation := mutation
		tests = append(tests, struct {
			name     string
			registry func() []actionsecurity.Descriptor
		}{name: mutation.name, registry: func() []actionsecurity.Descriptor {
			descriptor := valid
			mutation.mutate(&descriptor)
			return []actionsecurity.Descriptor{descriptor}
		}})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle, err := newUserDeleteActions(db, authRedis, crypto, test.registry, &actionIssueClock{now: 1}, bytes.NewReader(make([]byte, 128)), func() int64 { return 1 })
			if bundle != nil || !errors.Is(err, ErrActionVerificationUnavailable) || err.Error() != ErrActionVerificationUnavailable.Error() {
				t.Fatalf("bundle/error = %#v/%v, want fixed sanitized registry failure", bundle, err)
			}
		})
	}
}

func userDeleteBundleSecurity(t *testing.T, client redis.UniversalClient) (*AuthRedis, *actionsecurity.Crypto) {
	t.Helper()
	authRedis, err := NewAuthRedis(client, "bundle-auth-hmac-key-material")
	if err != nil {
		t.Fatal(err)
	}
	root := bytes.Repeat([]byte{0x72}, 32)
	crypto, err := actionsecurity.NewCrypto(root)
	clear(root)
	if err != nil {
		t.Fatal(err)
	}
	return authRedis, crypto
}
