package service

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
)

type a03CreateServices struct {
	operations    *ActionOperationService
	verifications *ActionVerificationService
	outbox        *CreateAccountOutboxWriter
	descriptors   map[actionsecurity.Action]actionsecurity.Descriptor
}

func openA03CreateServices(t *testing.T, now int64) (*a14DeleteFixture, *a03CreateServices) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" || strings.TrimSpace(os.Getenv("TEST_REDIS_URL")) == "" {
		t.Skip("requires explicit disposable TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision performed")
	}
	f := openA14DeleteFixture(t, now, models.UserRoleRoot, false)
	root, reason := actionsecurity.ParseRootKey(strings.TrimSpace(os.Getenv("ACTION_SECURITY_HMAC_KEY")))
	if reason != "" {
		t.Fatalf("invalid isolated action key: %s", reason)
	}
	crypto, err := actionsecurity.NewCrypto(root)
	clear(root)
	if err != nil {
		t.Fatal(err)
	}
	authRedis, err := NewAuthRedis(f.redis, "a03-create-auth-hmac-material")
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := NewActionSecurityRedis(f.redis, crypto)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := make(map[actionsecurity.Action]actionsecurity.Descriptor)
	active := make(map[actionsecurity.Action]actionsecurity.Descriptor)
	for _, descriptor := range actionsecurity.FutureActionDescriptors() {
		if descriptor.Action != actionsecurity.ActionUsersCreate && descriptor.Action != actionsecurity.ActionUsersCreateAdmin {
			continue
		}
		descriptors[descriptor.Action] = descriptor
		descriptor.Active = true
		active[descriptor.Action] = descriptor
	}
	resolve := func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		descriptor, ok := active[action]
		return descriptor, ok
	}
	operations, err := newActionOperationService(f.db, limiter, authRedis, crypto, resolve, f.clock, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	if err != nil {
		t.Fatal(err)
	}
	verifications, err := newActionVerificationService(f.db, limiter, authRedis, crypto, resolve, f.clock, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := NewCreateAccountOutboxWriter(func() int64 { return testSnowflake.Next() }, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	return f, &a03CreateServices{operations: operations, verifications: verifications, outbox: outbox, descriptors: descriptors}
}

func (services *a03CreateServices) prepare(t *testing.T, f *a14DeleteFixture, base actionsecurity.CreateAccountIntent, plaintext, key string) (*OperationIdentity, *CreateAccountExecution) {
	t.Helper()
	beginIntent := base
	beginIntent.Password = []byte(plaintext)
	var ticketValues []string
	if base.Role == models.UserRoleAdmin.String() {
		verificationIntent := base
		verificationIntent.Password = []byte(plaintext)
		current := []byte(f.password)
		issued, err := services.verifications.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersCreateAdmin,
			Actor: f.actorAPI, Intent: verificationIntent, CurrentPassword: current, TrustedIP: "198.18.3.1"})
		if err != nil {
			t.Fatal(err)
		}
		ticketValues = []string{issued.Ticket}
		beginIntent.Password = []byte(plaintext)
	}
	identity, view, err := services.operations.Begin(context.Background(), OperationBegin{Action: services.descriptorsForRole(base.Role).Action,
		Actor: f.actorAPI, IdempotencyKeyValues: []string{key}, TicketValues: ticketValues, Intent: beginIntent})
	if err != nil || identity == nil || view == nil || view.Status != "processing" {
		t.Fatalf("begin create = %#v/%#v/%v", identity, view, err)
	}
	hash, err := HashManagedCreationPassword(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	hashBytes := []byte(hash)
	execution, err := NewCreateAccountExecution(services.descriptorsForRole(base.Role), base, hashBytes,
		CreateAccountRequestMetadata{RequestID: "a03-real-request", TrustedIP: "198.18.3.1"}, func() int64 { return testSnowflake.Next() }, f.clock)
	clear(hashBytes)
	if err != nil {
		t.Fatal(err)
	}
	return identity, execution
}

func (services *a03CreateServices) descriptorsForRole(role string) actionsecurity.Descriptor {
	if role == models.UserRoleAdmin.String() {
		return services.descriptors[actionsecurity.ActionUsersCreateAdmin]
	}
	return services.descriptors[actionsecurity.ActionUsersCreate]
}

func TestCreateAccountRealOrdinaryAndAdminPersistAtomicState(t *testing.T) {
	cases := []struct {
		name      string
		role      models.UserRole
		overrides []actionsecurity.PermissionOverrideIntent
	}{
		{name: "ordinary", role: models.UserRoleUser},
		{name: "admin", role: models.UserRoleAdmin, overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}, {Capability: "users.sessions.read", Effect: 2}}},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			now := int64(1_910_000_000_000 + index*1_000_000)
			f, services := openA03CreateServices(t, now)
			username := fixtureUsername(testSnowflake.Next())
			nickname := "Managed " + test.name
			base := actionsecurity.CreateAccountIntent{Username: username, Nickname: &nickname, Role: test.role.String(), PlanType: int(models.PlanFree),
				AllowedModels: []string{}, DailyCallLimit: 100, Overrides: test.overrides}
			identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", newRealIdempotencyKey(t))
			view, err := services.operations.Execute(context.Background(), identity, execution, execution, services.outbox)
			if err != nil || view == nil || view.Status != "succeeded" || view.PublicRef != identity.PublicRef {
				t.Fatalf("execute create = %#v/%v", view, err)
			}
			result, ok := execution.ResultUser()
			if !ok || result == nil || result.Username == nil || *result.Username != username || result.Group == nil || *result.Group != "default" || result.Role != test.role.String() {
				t.Fatalf("result projection = %#v", result)
			}
			guid := result.GUID
			var user models.User
			if err := f.db.Where("guid = ?", guid).First(&user).Error; err != nil {
				t.Fatal(err)
			}
			if user.Username == nil || *user.Username != username || user.PasswordHash == nil || !security.VerifyPassword("A03-Strong-Password!", *user.PasswordHash) ||
				user.GroupID != testDefaultBusinessGroupID(t, f.db) || user.Role != test.role || user.Status != models.UserStatusActive || user.AuthVersion != 1 ||
				user.PlanType != models.PlanFree || user.DailyCallLimit != 100 || user.DailyCallsUsed != 0 || user.TotalTokensUsed != 0 || len(user.AllowedModels) != 0 ||
				user.CreatedBy == nil || *user.CreatedBy != f.actor.ID || user.CreatedAt != now || user.UpdatedAt != now {
				t.Fatalf("persisted user = %#v", user)
			}
			var authEvents []models.AuthAuditEvent
			if err := f.db.Where("user_id = ? AND event_type = ? AND is_deleted = 0", user.ID, models.AuthAuditEventRegistered).Find(&authEvents).Error; err != nil || len(authEvents) != 1 ||
				authEvents[0].CreatedBy == nil || *authEvents[0].CreatedBy != f.actor.ID || authEvents[0].IP == nil || *authEvents[0].IP != "198.18.3.1" {
				t.Fatalf("registration events = %#v/%v", authEvents, err)
			}
			var audit models.AuditLog
			if err := f.db.Where("action = ? AND resource = ? AND is_deleted = 0", "users.create", "users/"+guid).First(&audit).Error; err != nil || audit.UserID == nil || *audit.UserID != f.actor.ID || audit.IP == nil || *audit.IP != "198.18.3.1" ||
				audit.Detail["password"] != "set" || audit.Detail["operation_ref"] != identity.PublicRef {
				t.Fatalf("management audit = %#v/%v", audit, err)
			}
			var outbox models.AdminActionOutbox
			if err := f.db.Where("public_ref = ?", identity.PublicRef).First(&outbox).Error; err != nil || outbox.TargetKind != int(actionsecurity.TargetNone) || outbox.TargetGUID != nil ||
				outbox.ResultGUID == nil || *outbox.ResultGUID != user.Guid || outbox.State != models.OperationSucceeded {
				t.Fatalf("outbox = %#v/%v", outbox, err)
			}
			var operation models.AdminOperation
			if err := f.db.Where("public_ref = ?", identity.PublicRef).First(&operation).Error; err != nil || operation.State != models.OperationSucceeded || operation.ResultKind == nil ||
				*operation.ResultKind != models.ResultUser || operation.ResultGUID == nil || *operation.ResultGUID != user.Guid || operation.ResultHTTPStatus == nil || *operation.ResultHTTPStatus != 201 {
				t.Fatalf("terminal operation = %#v/%v", operation, err)
			}
			var heads []models.PermissionPolicyHead
			if err := f.db.Where("user_id = ?", user.ID).Find(&heads).Error; err != nil {
				t.Fatal(err)
			}
			if test.role == models.UserRoleUser {
				if len(heads) != 0 {
					t.Fatal("ordinary user received permission policy")
				}
			} else {
				if len(heads) != 1 || heads[0].PolicyVersion != 1 || heads[0].CatalogVersion != models.PermissionCatalogVersion || heads[0].RuleCount != len(test.overrides) {
					t.Fatalf("admin head = %#v", heads)
				}
				var rules []models.PermissionOverride
				if err := f.db.Where("user_id = ?", user.ID).Order("capability ASC").Find(&rules).Error; err != nil || len(rules) != len(test.overrides) {
					t.Fatalf("admin rules = %#v/%v", rules, err)
				}
			}
			assertCreateHashCleared(t, execution)
		})
	}
}

func TestCreateAccountRealConcurrentUsernameRaceCommitsOneUser(t *testing.T) {
	f, services := openA03CreateServices(t, 1_910_100_000_000)
	username := fixtureUsername(testSnowflake.Next())
	base := actionsecurity.CreateAccountIntent{Username: username, Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	type candidate struct {
		identity  *OperationIdentity
		execution *CreateAccountExecution
	}
	candidates := make([]candidate, 2)
	for index := range candidates {
		identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", newRealIdempotencyKey(t))
		candidates[index] = candidate{identity: identity, execution: execution}
	}
	start := make(chan struct{})
	var wait sync.WaitGroup
	var succeeded, failed atomic.Int32
	for _, item := range candidates {
		item := item
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			view, err := services.operations.Execute(context.Background(), item.identity, item.execution, item.execution, services.outbox)
			if err != nil || view == nil {
				return
			}
			if view.Status == "succeeded" {
				succeeded.Add(1)
			} else if view.Status == "failed" && view.FailureCode != nil && *view.FailureCode == models.FailureConsumerValidation.String() {
				failed.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	var count int64
	if err := f.db.Model(&models.User{}).Where("username = ?", username).Count(&count).Error; err != nil || count != 1 || succeeded.Load() != 1 || failed.Load() != 1 {
		t.Fatalf("race user/succeeded/failed = %d/%d/%d (%v)", count, succeeded.Load(), failed.Load(), err)
	}
}

func TestCreateAccountRealWriteFaultsRollbackEveryStage(t *testing.T) {
	faults := []struct {
		name, table string
		occurrence  int32
		terminal    bool
	}{
		{"user", "users", 1, false},
		{"permission_head", "user_permission_heads", 1, false},
		{"first_override_batch", "user_permission_overrides", 1, false},
		{"second_override_batch", "user_permission_overrides", 2, false},
		{"auth_audit", "auth_audit_events", 1, false},
		{"management_audit", "audit_logs", 1, false},
		{"outbox", "admin_action_outbox", 1, false},
		{"terminal_operation", "admin_operations", 1, true},
	}
	for index, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			f, services := openA03CreateServices(t, 1_910_200_000_000+int64(index)*1_000_000)
			username := fixtureUsername(testSnowflake.Next())
			base := actionsecurity.CreateAccountIntent{Username: username, Role: "admin", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
				Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.sessions.read", Effect: 3}}}
			identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", newRealIdempotencyKey(t))
			hook := fmt.Sprintf("a03_create_fault_%s_%d", fault.name, testSnowflake.Next())
			var matches atomic.Int32
			inject := func(tx *gorm.DB) {
				table := tx.Statement.Table
				if table == "" && tx.Statement.Schema != nil {
					table = tx.Statement.Schema.Table
				}
				if table != fault.table {
					return
				}
				if fault.terminal {
					values, ok := tx.Statement.Dest.(map[string]any)
					if !ok || values["finished_at"] == nil {
						return
					}
				}
				if matches.Add(1) == fault.occurrence {
					tx.AddError(errors.New("isolated a03 create write fault"))
				}
			}
			callback := f.db.Callback().Create()
			if fault.terminal {
				if err := f.db.Callback().Update().Before("gorm:update").Register(hook, inject); err != nil {
					t.Fatal(err)
				}
			} else if err := callback.Before("gorm:create").Register(hook, inject); err != nil {
				t.Fatal(err)
			}
			remove := func() {
				if fault.terminal {
					_ = f.db.Callback().Update().Remove(hook)
				} else {
					_ = f.db.Callback().Create().Remove(hook)
				}
			}
			t.Cleanup(remove)
			view, err := services.operations.Execute(context.Background(), identity, execution, execution, services.outbox)
			remove()
			if view != nil || !errors.Is(err, ErrActionOperationUnavailable) || matches.Load() < fault.occurrence {
				t.Fatalf("fault result = %#v/%v matches=%d", view, err, matches.Load())
			}
			var userCount, authCount, managementCount, outboxCount int64
			if err := f.db.Model(&models.User{}).Where("username = ?", username).Count(&userCount).Error; err != nil {
				t.Fatal(err)
			}
			_ = f.db.Model(&models.AuthAuditEvent{}).Joins("JOIN users ON users.id = auth_audit_events.user_id").Where("users.username = ?", username).Count(&authCount).Error
			_ = f.db.Model(&models.AuditLog{}).Where("detail->>'$.operation_ref' = ?", identity.PublicRef).Count(&managementCount).Error
			_ = f.db.Model(&models.AdminActionOutbox{}).Where("public_ref = ?", identity.PublicRef).Count(&outboxCount).Error
			var operation models.AdminOperation
			if err := f.db.Where("public_ref = ?", identity.PublicRef).First(&operation).Error; err != nil {
				t.Fatal(err)
			}
			if userCount != 0 || authCount != 0 || managementCount != 0 || outboxCount != 0 || operation.State != models.OperationProcessing ||
				operation.ResultGUID != nil || operation.ResultKind != nil || operation.FinishedAt != nil {
				t.Fatalf("partial state user/auth/audit/outbox/op = %d/%d/%d/%d/%#v", userCount, authCount, managementCount, outboxCount, operation)
			}
			assertCreateHashCleared(t, execution)
		})
	}
}
