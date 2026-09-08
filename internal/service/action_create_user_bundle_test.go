package service

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestNewCreateAccountActionsBuildsOneCoherentUserManagementBundle(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	authRedis, crypto := userDeleteBundleSecurity(t, client)
	clock := &actionIssueClock{now: 1_800_000_000_000}
	random := bytes.NewReader(bytes.Repeat([]byte{0x61}, 256))
	nextGUID := func() int64 { return 9801 }

	bundle, err := newUserManagementActions(db, authRedis, crypto, actionsecurity.ActiveActionRegistry, clock, random, nextGUID)
	if err != nil {
		t.Fatal(err)
	}
	if !completeUserManagementActions(bundle) {
		t.Fatalf("incomplete user-management bundle: %#v", bundle)
	}
	if bundle.Verifications.redis != bundle.Operations.limiter || bundle.Verifications.redis.client != client ||
		bundle.Verifications.authRedis != authRedis || bundle.Operations.authRedis != authRedis ||
		bundle.Verifications.crypto != crypto || bundle.Operations.crypto != crypto || bundle.Verifications.redis.crypto != crypto {
		t.Fatal("verification and operation services do not share one Redis/client/crypto set")
	}
	if bundle.Verifications.clock != clock || bundle.Operations.clock != clock || bundle.DeleteOutbox.clock != clock || bundle.CreateOutbox.clock != clock ||
		bundle.Verifications.random != random || bundle.Operations.random != random ||
		reflect.ValueOf(bundle.Verifications.nextGUID).Pointer() != reflect.ValueOf(nextGUID).Pointer() ||
		reflect.ValueOf(bundle.Operations.nextGUID).Pointer() != reflect.ValueOf(nextGUID).Pointer() ||
		reflect.ValueOf(bundle.DeleteOutbox.nextGUID).Pointer() != reflect.ValueOf(nextGUID).Pointer() ||
		reflect.ValueOf(bundle.CreateOutbox.nextGUID).Pointer() != reflect.ValueOf(nextGUID).Pointer() {
		t.Fatal("bundle did not retain one reviewed persistence dependency set")
	}
	for _, resolver := range []func(actionsecurity.Action) (actionsecurity.Descriptor, bool){bundle.Verifications.resolve, bundle.Operations.resolve} {
		for _, action := range []actionsecurity.Action{actionsecurity.ActionUsersCreate, actionsecurity.ActionUsersCreateAdmin, actionsecurity.ActionUsersDelete} {
			descriptor, ok := resolver(action)
			if !ok || descriptor.Action != action || !descriptor.Active {
				t.Fatalf("private resolver rejected action %d: %#v, %v", action, descriptor, ok)
			}
		}
		if _, ok := resolver(actionsecurity.ActionUsersResetPassword); ok {
			t.Fatal("private resolver exposed an action outside the complete user-management bundle")
		}
	}
	deleteView := bundle.DeleteActions()
	if deleteView == nil || deleteView.Verifications != bundle.Verifications || deleteView.Operations != bundle.Operations ||
		deleteView.Outbox != bundle.DeleteOutbox || reflect.ValueOf(deleteView.NewExecution).Pointer() != reflect.ValueOf(bundle.NewDeleteExecution).Pointer() {
		t.Fatalf("delete compatibility view did not share the coherent bundle: %#v", deleteView)
	}
}

func TestCreateAccountActionsFactoriesStayBoundToReviewedDescriptors(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	authRedis, crypto := userDeleteBundleSecurity(t, client)
	clock := &actionIssueClock{now: 1_800_000_000_000}
	bundle, err := newUserManagementActions(db, authRedis, crypto, actionsecurity.ActiveActionRegistry, clock, bytes.NewReader(make([]byte, 256)), func() int64 { return 9802 })
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := bundle.NewDeleteExecution(actionsecurity.DeleteUserIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, Reason: "reviewed deletion"})
	if err != nil || deletion == nil {
		t.Fatalf("delete execution/error = %#v/%v", deletion, err)
	}
	for _, tc := range []struct {
		action actionsecurity.Action
		role   string
	}{
		{actionsecurity.ActionUsersCreate, "user"},
		{actionsecurity.ActionUsersCreateAdmin, "admin"},
	} {
		hash := []byte(createAccountTestPasswordHash)
		execution, err := bundle.NewCreateExecution(tc.action, actionsecurity.CreateAccountIntent{
			Username: "managed_account", Role: tc.role, PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
		}, hash, CreateAccountRequestMetadata{RequestID: "request.bundle-1", TrustedIP: "203.0.113.33"})
		if err != nil || execution == nil || execution.descriptor.Action != tc.action || execution.clock != clock {
			t.Fatalf("create action %d execution/error = %#v/%v", tc.action, execution, err)
		}
		execution.ClearSecrets()
	}
	if execution, err := bundle.NewCreateExecution(actionsecurity.ActionUsersDelete, actionsecurity.CreateAccountIntent{}, []byte(createAccountTestPasswordHash), CreateAccountRequestMetadata{}); execution != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("wrong create descriptor execution/error = %#v/%v", execution, err)
	}
}

func TestNewCreateAccountActionsRejectsMissingDependencies(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	authRedis, crypto := userDeleteBundleSecurity(t, client)
	clock := persistence.Clock(&actionIssueClock{now: 1_800_000_000_000})
	random := io.Reader(bytes.NewReader(make([]byte, 256)))
	nextGUID := func() int64 { return 9803 }
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
			bundle, err := newUserManagementActions(test.db, test.authRedis, test.crypto, test.registry, test.clock, test.random, test.nextGUID)
			if bundle != nil || !errors.Is(err, ErrActionVerificationUnavailable) || err.Error() != ErrActionVerificationUnavailable.Error() {
				t.Fatalf("bundle/error = %#v/%v, want nil/sanitized unavailable", bundle, err)
			}
		})
	}
	unsupported, err := NewAuthRedis(redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{"127.0.0.1:1"}}), "bundle-auth-hmac-key-material")
	if err != nil {
		t.Fatal(err)
	}
	if bundle, err := newUserManagementActions(db, unsupported, crypto, registry, clock, random, nextGUID); bundle != nil || !errors.Is(err, ErrActionVerificationUnavailable) {
		t.Fatalf("unsupported Redis bundle/error = %#v/%v", bundle, err)
	}
}

func TestNewCreateAccountActionsRejectsRegistryOrderMetadataAndEncoderDrift(t *testing.T) {
	db := actionIssueDryDB(t)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	authRedis, crypto := userDeleteBundleSecurity(t, client)
	valid := actionsecurity.ActiveActionRegistry()
	tests := []struct {
		name     string
		registry func() []actionsecurity.Descriptor
	}{
		{name: "empty", registry: func() []actionsecurity.Descriptor { return nil }},
		{name: "missing", registry: func() []actionsecurity.Descriptor { return append([]actionsecurity.Descriptor(nil), valid[:2]...) }},
		{name: "extra", registry: func() []actionsecurity.Descriptor {
			return append(append([]actionsecurity.Descriptor(nil), valid...), valid[2])
		}},
		{name: "order", registry: func() []actionsecurity.Descriptor { return []actionsecurity.Descriptor{valid[1], valid[0], valid[2]} }},
	}
	mutations := []struct {
		name   string
		mutate func(*actionsecurity.Descriptor)
	}{
		{name: "action", mutate: func(d *actionsecurity.Descriptor) { d.Action = actionsecurity.ActionUsersResetPassword }},
		{name: "name", mutate: func(d *actionsecurity.Descriptor) { d.Name += ".changed" }},
		{name: "capability", mutate: func(d *actionsecurity.Descriptor) { d.Capability = "users.edit" }},
		{name: "root only", mutate: func(d *actionsecurity.Descriptor) { d.RootOnly = !d.RootOnly }},
		{name: "ticket", mutate: func(d *actionsecurity.Descriptor) { d.RequiresTicket = !d.RequiresTicket }},
		{name: "inactive", mutate: func(d *actionsecurity.Descriptor) { d.Active = false }},
		{name: "target", mutate: func(d *actionsecurity.Descriptor) { d.TargetKind = actionsecurity.TargetPublicContent }},
		{name: "nil encoder", mutate: func(d *actionsecurity.Descriptor) { d.Encode = nil }},
		{name: "untyped encoder", mutate: func(d *actionsecurity.Descriptor) {
			d.Encode = func(any) ([]byte, error) { return []byte("wrong"), nil }
		}},
	}
	for descriptorIndex := range valid {
		for _, mutation := range mutations {
			descriptorIndex, mutation := descriptorIndex, mutation
			tests = append(tests, struct {
				name     string
				registry func() []actionsecurity.Descriptor
			}{name: mutation.name + " descriptor", registry: func() []actionsecurity.Descriptor {
				out := append([]actionsecurity.Descriptor(nil), valid...)
				mutation.mutate(&out[descriptorIndex])
				return out
			}})
		}
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle, err := newUserManagementActions(db, authRedis, crypto, test.registry, &actionIssueClock{now: 1}, bytes.NewReader(make([]byte, 256)), func() int64 { return 1 })
			if bundle != nil || !errors.Is(err, ErrActionVerificationUnavailable) || err.Error() != ErrActionVerificationUnavailable.Error() {
				t.Fatalf("bundle/error = %#v/%v, want fixed sanitized registry failure", bundle, err)
			}
		})
	}
}
