package service

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	crypto        *actionsecurity.Crypto
}

func requireDefaultMySQLAffectedRows(t *testing.T) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	for key := range parsed.Query() {
		if strings.EqualFold(key, "clientFoundRows") {
			t.Fatalf("TEST_DATABASE_URL must exercise default MySQL changed-row semantics without %s", key)
		}
	}
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
	return f, &a03CreateServices{operations: operations, verifications: verifications, outbox: outbox, descriptors: descriptors, crypto: crypto}
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
		CreateAccountRequestMetadata{RequestID: "a03-real-request", TrustedIP: "198.18.3.1"}, func() int64 { return testSnowflake.Next() }, f.clock, services.crypto)
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
	requireDefaultMySQLAffectedRows(t)
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
			auditAction := services.descriptorsForRole(base.Role).Name
			if err := f.db.Where("action = ? AND resource = ? AND is_deleted = 0", auditAction, "users/"+guid).First(&audit).Error; err != nil || audit.UserID == nil || *audit.UserID != f.actor.ID || audit.IP == nil || *audit.IP != "198.18.3.1" ||
				audit.Detail["terminal_state"] != "succeeded" || audit.Detail["failure_code"] != nil || audit.Detail["request_id"] != "a03-real-request" || audit.Detail["operation_ref"] != identity.PublicRef || audit.Detail["password"] != nil {
				t.Fatalf("management audit = %#v/%v", audit, err)
			}
			var outbox models.AdminActionOutbox
			if err := f.db.Where("public_ref = ?", identity.PublicRef).First(&outbox).Error; err != nil || outbox.TargetKind != int(actionsecurity.TargetNone) || outbox.TargetGUID != nil ||
				outbox.ResultGUID == nil || *outbox.ResultGUID != user.Guid || outbox.State != models.OperationSucceeded || outbox.FailureCode != nil {
				t.Fatalf("outbox = %#v/%v", outbox, err)
			}
			var operation models.AdminOperation
			if err := f.db.Where("public_ref = ?", identity.PublicRef).First(&operation).Error; err != nil || operation.State != models.OperationSucceeded || operation.ResultKind == nil ||
				*operation.ResultKind != models.ResultUser || operation.ResultGUID == nil || *operation.ResultGUID != user.Guid || operation.ResultHTTPStatus == nil || *operation.ResultHTTPStatus != 201 {
				t.Fatalf("terminal operation = %#v/%v", operation, err)
			}
			var snapshot models.AdminOperationResponse
			if err := f.db.Where("operation_id = ?", operation.ID).First(&snapshot).Error; err != nil || snapshot.Guid <= 0 || snapshot.Guid == operation.Guid {
				t.Fatalf("independent response identity = %#v/%v operation_guid=%d", snapshot, err, operation.Guid)
			}
			response, err := services.operations.CreateAccountResponse(context.Background(), services.descriptorsForRole(base.Role).Action, f.actorAPI, identity.PublicRef)
			if err != nil || response == nil || response.HTTPStatus != 201 || response.MediaType != createAccountResponseMediaType || !validCreateAccountResponseBody(response.Body, identity.PublicRef, services.descriptorsForRole(base.Role).Action, user.Guid) {
				t.Fatalf("persisted operation response = %#v/%v", response, err)
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

func TestCreateAccountRealDeleteRedactsSnapshotAndReplayReturnsGone(t *testing.T) {
	requireDefaultMySQLAffectedRows(t)
	f, services := openA03CreateServices(t, 1_910_050_000_000)
	username := fixtureUsername(testSnowflake.Next())
	nickname := "Immutable Replay"
	key := newRealIdempotencyKey(t)
	groupKey := fixtureUsername(testSnowflake.Next())
	group := models.BusinessGroup{AuditFields: a14Audit(f.clock.NowMillis()), Key: groupKey, DisplayName: "Replay Group", Status: models.BusinessGroupStatusActive}
	if err := f.db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	base := actionsecurity.CreateAccountIntent{Username: username, Nickname: &nickname, Role: models.UserRoleUser.String(),
		GroupGUID: &group.Guid, PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", key)
	view, err := services.operations.Execute(context.Background(), identity, execution, execution, services.outbox)
	if err != nil || view == nil || view.Status != "succeeded" {
		t.Fatalf("execute = %#v/%v", view, err)
	}
	first, err := services.operations.CreateAccountResponse(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, identity.PublicRef)
	if err != nil || first == nil || first.HTTPStatus != 201 || first.MediaType != createAccountResponseMediaType || len(first.Body) == 0 {
		t.Fatalf("first response = %#v/%v", first, err)
	}
	activeRestart := restartA03CreateOperationService(t, f)
	activeReplay, err := activeRestart.CreateAccountResponse(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, identity.PublicRef)
	if err != nil || activeReplay == nil || activeReplay.HTTPStatus != http.StatusCreated || activeReplay.MediaType != first.MediaType || !bytes.Equal(activeReplay.Body, first.Body) {
		t.Fatalf("active replay drifted first=%q replay=%#v err=%v", first.Body, activeReplay, err)
	}

	var operation models.AdminOperation
	if err := f.db.Where("public_ref = ?", identity.PublicRef).First(&operation).Error; err != nil || operation.ResultGUID == nil {
		t.Fatalf("operation = %#v/%v", operation, err)
	}
	var user models.User
	if err := f.db.Where("guid = ?", *operation.ResultGUID).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	counts := func() [4]int64 {
		t.Helper()
		var got [4]int64
		if err := f.db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ?", user.ID, models.AuthAuditEventRegistered).Count(&got[0]).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&models.AuditLog{}).Where("detail->>'$.operation_ref' = ?", identity.PublicRef).Count(&got[1]).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&models.AdminActionOutbox{}).Where("public_ref = ?", identity.PublicRef).Count(&got[2]).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&models.AdminOperationResponse{}).Where("operation_id = ?", operation.ID).Count(&got[3]).Error; err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := counts()
	deleteIdentity, deleteIntent, _ := f.prepare(t, user, newRealIdempotencyKey(t))
	deleteExecution, err := f.bundle.NewExecution(deleteIntent)
	if err != nil {
		t.Fatal(err)
	}
	deleteView, err := f.bundle.Operations.Execute(context.Background(), deleteIdentity, deleteExecution, deleteExecution, f.bundle.Outbox)
	if err != nil || deleteView == nil || deleteView.Status != "succeeded" {
		t.Fatalf("delete = %#v/%v", deleteView, err)
	}
	var storedResponse models.AdminOperationResponse
	if err := f.db.Unscoped().Where("operation_id = ?", operation.ID).First(&storedResponse).Error; err != nil {
		t.Fatal(err)
	}
	if storedResponse.LifecycleState != models.OperationResponseRedacted || storedResponse.IsDeleted != 1 || storedResponse.IntegrityVersion != models.OperationResponseIntegrityHMACV1 || storedResponse.ResponseHMAC == nil || len(*storedResponse.ResponseHMAC) != 64 ||
		!bytes.Equal(storedResponse.ResponseBody, []byte("{}")) || storedResponse.BodySHA256 != redactedCreateResponseSHA256 || bytes.Contains(storedResponse.ResponseBody, []byte(username)) || bytes.Contains(storedResponse.ResponseBody, []byte(nickname)) {
		t.Fatalf("redacted response = %#v", storedResponse)
	}

	restarted := restartA03CreateOperationService(t, f)
	replayIntent := base
	replayIntent.Password = []byte("A03-Strong-Password!")
	replayed, replayView, err := restarted.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersCreate,
		Actor: f.actorAPI, IdempotencyKeyValues: []string{key}, Intent: replayIntent})
	if err != nil || replayed == nil || replayed.ReadyForExecution() || replayView == nil || replayView.Status != "succeeded" || replayView.PublicRef != identity.PublicRef {
		t.Fatalf("replay Begin = %#v/%#v/%v", replayed, replayView, err)
	}
	second, err := restarted.CreateAccountResponse(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, replayView.PublicRef)
	if second != nil || !errors.Is(err, ErrCreatedAccountDeleted) {
		t.Fatalf("deleted replay = %#v/%v", second, err)
	}
	if after := counts(); after != before || after != [4]int64{1, 1, 1, 1} {
		t.Fatalf("replay created side effects before=%v after=%v", before, after)
	}
}

func TestCreateAccountRealExpiredOperationDeleteRedactsDurableTarget(t *testing.T) {
	for index, expireThrough := range []string{"query", "begin"} {
		t.Run(expireThrough, func(t *testing.T) {
			requireDefaultMySQLAffectedRows(t)
			f, services := openA03CreateServices(t, 1_910_052_000_000+int64(index)*1_000_000)
			username := fixtureUsername(testSnowflake.Next())
			nickname := "Expired private snapshot"
			key := newRealIdempotencyKey(t)
			base := actionsecurity.CreateAccountIntent{Username: username, Nickname: &nickname, Role: models.UserRoleUser.String(), PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
			identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", key)
			if view, err := services.operations.Execute(context.Background(), identity, execution, execution, services.outbox); err != nil || view == nil || view.Status != "succeeded" {
				t.Fatalf("create before expiry = %#v/%v", view, err)
			}
			var operation models.AdminOperation
			if err := f.db.Where("id = ?", identity.ID).First(&operation).Error; err != nil || operation.ResultGUID == nil {
				t.Fatalf("created operation = %#v/%v", operation, err)
			}
			var target models.User
			if err := f.db.Where("guid = ?", *operation.ResultGUID).First(&target).Error; err != nil {
				t.Fatal(err)
			}
			if result := f.db.Model(&models.Session{}).Where("id = ?", f.session.ID).Update("expires_at", operation.QueryExpiresAt+60_000); result.Error != nil || result.RowsAffected != 1 {
				t.Fatalf("extend isolated actor session for retention expiry = %d/%v", result.RowsAffected, result.Error)
			}
			f.clock.Set(operation.QueryExpiresAt)
			switch expireThrough {
			case "query":
				if got, err := services.operations.Query(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, []string{key}); got != nil || !errors.Is(err, ErrActionOperationExpired) {
					t.Fatalf("query expiry = %#v/%v", got, err)
				}
			case "begin":
				replayIntent := base
				replayIntent.Password = []byte("A03-Strong-Password!")
				gotIdentity, gotView, err := services.operations.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersCreate, Actor: f.actorAPI, IdempotencyKeyValues: []string{key}, Intent: replayIntent})
				clear(replayIntent.Password)
				if gotIdentity != nil || gotView != nil || !errors.Is(err, ErrActionOperationExpired) {
					t.Fatalf("begin expiry = %#v/%#v/%v", gotIdentity, gotView, err)
				}
			}
			if err := f.db.Unscoped().First(&operation, operation.ID).Error; err != nil || operation.State != models.OperationExpired || operation.IsDeleted != 1 || operation.ResultGUID != nil {
				t.Fatalf("expired operation = %#v/%v", operation, err)
			}
			activeReplay, activeReplayErr := services.operations.CreateAccountResponse(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, identity.PublicRef)
			if activeReplayErr != nil || activeReplay == nil || activeReplay.HTTPStatus != http.StatusCreated || !bytes.Contains(activeReplay.Body, []byte(username)) {
				t.Fatalf("active target response after operation expiry = %#v/%v", activeReplay, activeReplayErr)
			}
			clear(activeReplay.Body)
			deleteIdentity, deleteIntent, _ := f.prepare(t, target, newRealIdempotencyKey(t))
			deleteExecution, err := f.bundle.NewExecution(deleteIntent)
			if err != nil {
				t.Fatal(err)
			}
			if view, err := f.bundle.Operations.Execute(context.Background(), deleteIdentity, deleteExecution, deleteExecution, f.bundle.Outbox); err != nil || view == nil || view.Status != "succeeded" {
				t.Fatalf("delete after %s expiry = %#v/%v", expireThrough, view, err)
			}
			var response models.AdminOperationResponse
			if err := f.db.Unscoped().Where("operation_id = ?", operation.ID).First(&response).Error; err != nil || response.TargetGUID != target.Guid || response.LifecycleState != models.OperationResponseRedacted || response.IsDeleted != 1 || !bytes.Equal(response.ResponseBody, []byte("{}")) || bytes.Contains(response.ResponseBody, []byte(username)) || bytes.Contains(response.ResponseBody, []byte(nickname)) {
				t.Fatalf("expired redaction = %#v/%v", response, err)
			}
			if got, err := services.operations.CreateAccountResponse(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, identity.PublicRef); got != nil || !errors.Is(err, ErrCreatedAccountDeleted) {
				t.Fatalf("expired deleted replay = %#v/%v", got, err)
			}
			replayIntent := base
			replayIntent.Password = []byte("A03-Strong-Password!")
			gotIdentity, gotView, replayErr := services.operations.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersCreate, Actor: f.actorAPI, IdempotencyKeyValues: []string{key}, Intent: replayIntent})
			clear(replayIntent.Password)
			if gotIdentity != nil || gotView != nil || !errors.Is(replayErr, ErrActionOperationExpired) {
				t.Fatalf("expired idempotent HTTP path did not retain stable 410 = %#v/%#v/%v", gotIdentity, gotView, replayErr)
			}
		})
	}
}

func TestCreateAccountRealRedactionFailureRollsBackDeletedUser(t *testing.T) {
	requireDefaultMySQLAffectedRows(t)
	f, services := openA03CreateServices(t, 1_910_054_500_000)
	username := fixtureUsername(testSnowflake.Next())
	nickname := "Rollback private snapshot"
	base := actionsecurity.CreateAccountIntent{Username: username, Nickname: &nickname, Role: models.UserRoleUser.String(), PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", newRealIdempotencyKey(t))
	if view, err := services.operations.Execute(context.Background(), identity, execution, execution, services.outbox); err != nil || view == nil || view.Status != "succeeded" {
		t.Fatalf("create before rollback probe = %#v/%v", view, err)
	}
	var operation models.AdminOperation
	if err := f.db.Where("id = ?", identity.ID).First(&operation).Error; err != nil || operation.ResultGUID == nil {
		t.Fatal(err)
	}
	var target models.User
	if err := f.db.Where("guid = ?", *operation.ResultGUID).First(&target).Error; err != nil {
		t.Fatal(err)
	}
	deleteIdentity, deleteIntent, _ := f.prepare(t, target, newRealIdempotencyKey(t))
	hook := fmt.Sprintf("a03_redaction_rollback_%d", testSnowflake.Next())
	var hits atomic.Int32
	if err := f.db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "admin_operation_responses" {
			hits.Add(1)
			tx.AddError(errors.New("isolated response redaction failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	remove := func() { _ = f.db.Callback().Update().Remove(hook) }
	t.Cleanup(remove)
	deleteExecution, _ := f.bundle.NewExecution(deleteIntent)
	view, err := f.bundle.Operations.Execute(context.Background(), deleteIdentity, deleteExecution, deleteExecution, f.bundle.Outbox)
	remove()
	if view != nil || !errors.Is(err, ErrActionOperationUnavailable) || hits.Load() != 1 {
		t.Fatalf("redaction rollback result = %#v/%v hits=%d", view, err, hits.Load())
	}
	var storedUser models.User
	if err := f.db.Unscoped().First(&storedUser, target.ID).Error; err != nil || storedUser.IsDeleted != 0 || storedUser.Nickname == nil || *storedUser.Nickname != nickname || storedUser.PasswordHash == nil {
		t.Fatalf("user changed despite redaction rollback = %#v/%v", storedUser, err)
	}
	var storedResponse models.AdminOperationResponse
	if err := f.db.Where("operation_id = ?", operation.ID).First(&storedResponse).Error; err != nil || storedResponse.LifecycleState != models.OperationResponseActive || storedResponse.IsDeleted != 0 || !bytes.Contains(storedResponse.ResponseBody, []byte(username)) {
		t.Fatalf("snapshot changed despite rollback = %#v/%v", storedResponse, err)
	}
}

func TestCreateAccountRealExpiryAndDeleteConcurrencyRedactsOnce(t *testing.T) {
	requireDefaultMySQLAffectedRows(t)
	f, services := openA03CreateServices(t, 1_910_055_500_000)
	username := fixtureUsername(testSnowflake.Next())
	base := actionsecurity.CreateAccountIntent{Username: username, Role: models.UserRoleUser.String(), PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	key := newRealIdempotencyKey(t)
	identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", key)
	if view, err := services.operations.Execute(context.Background(), identity, execution, execution, services.outbox); err != nil || view == nil || view.Status != "succeeded" {
		t.Fatalf("create before concurrent expiry = %#v/%v", view, err)
	}
	var createOperation models.AdminOperation
	if err := f.db.Where("id = ?", identity.ID).First(&createOperation).Error; err != nil || createOperation.ResultGUID == nil {
		t.Fatal(err)
	}
	var target models.User
	if err := f.db.Where("guid = ?", *createOperation.ResultGUID).First(&target).Error; err != nil {
		t.Fatal(err)
	}
	if result := f.db.Model(&models.Session{}).Where("id = ?", f.session.ID).Update("expires_at", createOperation.QueryExpiresAt+60_000); result.Error != nil || result.RowsAffected != 1 {
		t.Fatal(result.Error)
	}
	f.clock.Set(createOperation.QueryExpiresAt)
	deleteIdentity, deleteIntent, _ := f.prepare(t, target, newRealIdempotencyKey(t))
	deleteExecution, _ := f.bundle.NewExecution(deleteIntent)
	start := make(chan struct{})
	var wait sync.WaitGroup
	var expireErr, deleteErr error
	var deleteView *OperationView
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, expireErr = services.operations.Query(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, []string{key})
	}()
	go func() {
		defer wait.Done()
		<-start
		deleteView, deleteErr = f.bundle.Operations.Execute(context.Background(), deleteIdentity, deleteExecution, deleteExecution, f.bundle.Outbox)
	}()
	close(start)
	wait.Wait()
	if !errors.Is(expireErr, ErrActionOperationExpired) || deleteErr != nil || deleteView == nil || deleteView.Status != "succeeded" {
		t.Fatalf("concurrent expiry/delete = expire:%v delete:%#v/%v", expireErr, deleteView, deleteErr)
	}
	var response models.AdminOperationResponse
	if err := f.db.Unscoped().Where("operation_id = ?", createOperation.ID).First(&response).Error; err != nil || response.LifecycleState != models.OperationResponseRedacted || response.IsDeleted != 1 || string(response.ResponseBody) != "{}" {
		t.Fatalf("concurrent redaction = %#v/%v", response, err)
	}
}

func TestCreateAccountRealDeleteRedactsEverySnapshotForTarget(t *testing.T) {
	requireDefaultMySQLAffectedRows(t)
	f, services := openA03CreateServices(t, 1_910_056_500_000)
	username := fixtureUsername(testSnowflake.Next())
	base := actionsecurity.CreateAccountIntent{Username: username, Role: models.UserRoleUser.String(), PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", newRealIdempotencyKey(t))
	if view, err := services.operations.Execute(context.Background(), identity, execution, execution, services.outbox); err != nil || view == nil || view.Status != "succeeded" {
		t.Fatalf("create before multi-snapshot = %#v/%v", view, err)
	}
	var operation models.AdminOperation
	if err := f.db.Where("id = ?", identity.ID).First(&operation).Error; err != nil || operation.ResultGUID == nil {
		t.Fatal(err)
	}
	var target models.User
	if err := f.db.Where("guid = ?", *operation.ResultGUID).First(&target).Error; err != nil {
		t.Fatal(err)
	}
	var originalResponse models.AdminOperationResponse
	if err := f.db.Where("operation_id = ?", operation.ID).First(&originalResponse).Error; err != nil {
		t.Fatal(err)
	}
	cloneOperation := operation
	cloneOperation.ID = 0
	cloneOperation.Guid = testSnowflake.Next()
	cloneOperation.PublicRef = "op_" + fmt.Sprintf("%043d", testSnowflake.Next())
	cloneOperation.IdempotencyKeyHMAC = strings.Repeat("2", 64)
	cloneOperation.RequestHMAC = strings.Repeat("3", 64)
	if err := f.db.Create(&cloneOperation).Error; err != nil {
		t.Fatal(err)
	}
	var originalOutbox models.AdminActionOutbox
	if err := f.db.Where("operation_id = ?", operation.ID).First(&originalOutbox).Error; err != nil {
		t.Fatal(err)
	}
	cloneOutbox := originalOutbox
	cloneOutbox.ID = 0
	cloneOutbox.Guid = testSnowflake.Next()
	cloneOutbox.OperationID = cloneOperation.ID
	cloneOutbox.PublicRef = cloneOperation.PublicRef
	if err := f.db.Create(&cloneOutbox).Error; err != nil {
		t.Fatal(err)
	}
	cloneResponse := originalResponse
	cloneResponse.ID = 0
	cloneResponse.Guid = testSnowflake.Next()
	cloneResponse.OperationID = cloneOperation.ID
	cloneResponse.ResponseBody = bytes.Replace(cloneResponse.ResponseBody, []byte(operation.PublicRef), []byte(cloneOperation.PublicRef), 1)
	digest := sha256.Sum256(cloneResponse.ResponseBody)
	cloneResponse.BodySHA256 = fmt.Sprintf("%x", digest)
	clear(digest[:])
	mac, ok := createAccountResponseHMAC(services.crypto, cloneOperation, models.OperationResponseActive, target.Guid, http.StatusCreated, createAccountResponseMediaType, cloneResponse.ResponseBody)
	if !ok {
		t.Fatal("clone response HMAC")
	}
	cloneResponse.ResponseHMAC = &mac
	if err := f.db.Create(&cloneResponse).Error; err != nil {
		t.Fatal(err)
	}
	deleteIdentity, deleteIntent, _ := f.prepare(t, target, newRealIdempotencyKey(t))
	deleteExecution, _ := f.bundle.NewExecution(deleteIntent)
	if view, err := f.bundle.Operations.Execute(context.Background(), deleteIdentity, deleteExecution, deleteExecution, f.bundle.Outbox); err != nil || view == nil || view.Status != "succeeded" {
		t.Fatalf("delete multi-snapshot = %#v/%v", view, err)
	}
	var redacted []models.AdminOperationResponse
	if err := f.db.Unscoped().Where("target_guid = ?", target.Guid).Order("operation_id").Find(&redacted).Error; err != nil || len(redacted) != 2 {
		t.Fatalf("multi-snapshot rows = %d/%v", len(redacted), err)
	}
	for _, response := range redacted {
		if response.LifecycleState != models.OperationResponseRedacted || response.IsDeleted != 1 || string(response.ResponseBody) != "{}" || bytes.Contains(response.ResponseBody, []byte(username)) {
			t.Fatalf("multi-snapshot retained PII = %#v", response)
		}
	}
}

func TestCreateAccountRealSnapshotRejectsDirectMutationAndRotatedKey(t *testing.T) {
	requireDefaultMySQLAffectedRows(t)
	f, services := openA03CreateServices(t, 1_910_060_000_000)
	username := fixtureUsername(testSnowflake.Next())
	base := actionsecurity.CreateAccountIntent{Username: username, Role: models.UserRoleUser.String(), PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", newRealIdempotencyKey(t))
	view, err := services.operations.Execute(context.Background(), identity, execution, execution, services.outbox)
	if err != nil || view == nil || view.Status != "succeeded" {
		t.Fatalf("execute = %#v/%v", view, err)
	}
	var operation models.AdminOperation
	if err := f.db.Where("public_ref = ?", identity.PublicRef).First(&operation).Error; err != nil {
		t.Fatal(err)
	}
	var response models.AdminOperationResponse
	if err := f.db.Where("operation_id = ?", operation.ID).First(&response).Error; err != nil {
		t.Fatal(err)
	}
	if response.IntegrityVersion != models.OperationResponseIntegrityHMACV1 || response.ResponseHMAC == nil || len(*response.ResponseHMAC) != 64 {
		t.Fatalf("unsealed HMAC response = %#v", response)
	}
	restore := map[string]any{"target_guid": response.TargetGUID, "lifecycle_state": response.LifecycleState, "integrity_version": response.IntegrityVersion, "response_hmac": response.ResponseHMAC,
		"http_status": response.HTTPStatus, "media_type": response.MediaType, "response_body": response.ResponseBody, "body_sha256": response.BodySHA256,
		"is_deleted": response.IsDeleted, "updated_at": response.UpdatedAt, "updated_by": response.UpdatedBy}
	for name, update := range map[string]map[string]any{
		"target":    {"target_guid": f.actor.Guid},
		"body":      {"response_body": []byte("{}")},
		"digest":    {"body_sha256": strings.Repeat("0", 64)},
		"hmac":      {"response_hmac": strings.Repeat("0", 64)},
		"lifecycle": {"lifecycle_state": models.OperationResponseRedacted, "response_body": []byte("{}"), "body_sha256": redactedCreateResponseSHA256, "is_deleted": 1, "updated_at": response.UpdatedAt + 1, "updated_by": f.actor.ID},
	} {
		t.Run(name, func(t *testing.T) {
			if result := f.db.Unscoped().Model(&models.AdminOperationResponse{}).Where("id = ?", response.ID).UpdateColumns(update); result.Error != nil || result.RowsAffected != 1 {
				t.Fatalf("direct database mutation setup = %d/%v", result.RowsAffected, result.Error)
			}
			if got, err := services.operations.CreateAccountResponse(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, identity.PublicRef); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
				t.Fatalf("tampered replay = %#v/%v", got, err)
			}
			if result := f.db.Unscoped().Model(&models.AdminOperationResponse{}).Where("id = ?", response.ID).UpdateColumns(restore); result.Error != nil || result.RowsAffected != 1 {
				t.Fatalf("restore valid fixture response = %d/%v", result.RowsAffected, result.Error)
			}
		})
	}
	if result := f.db.Unscoped().Model(&models.AdminOperationResponse{}).Where("id = ?", response.ID).UpdateColumns(map[string]any{"integrity_version": models.OperationResponseIntegrityLegacySealed, "response_hmac": nil}); result.Error == nil {
		t.Fatal("schema accepted an active response without HMAC integrity")
	}
	current, err := services.operations.CreateAccountResponse(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, identity.PublicRef)
	if err != nil || current == nil || current.HTTPStatus != http.StatusCreated {
		t.Fatalf("same-key replay = %#v/%v", current, err)
	}
	rotatedCrypto, err := actionsecurity.NewCrypto([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	rotated := *services.operations
	rotated.crypto = rotatedCrypto
	if got, err := rotated.CreateAccountResponse(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, identity.PublicRef); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("rotated-key replay = %#v/%v", got, err)
	}
	if result := f.db.Unscoped().Delete(&models.AdminOperationResponse{}, response.ID); result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("direct snapshot delete = %d/%v", result.RowsAffected, result.Error)
	}
	if got, err := services.operations.CreateAccountResponse(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, identity.PublicRef); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("missing snapshot replay = %#v/%v", got, err)
	}
}

func restartA03CreateOperationService(t *testing.T, f *a14DeleteFixture) *ActionOperationService {
	t.Helper()
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
	active := make(map[actionsecurity.Action]actionsecurity.Descriptor)
	for _, descriptor := range actionsecurity.FutureActionDescriptors() {
		if descriptor.Action == actionsecurity.ActionUsersCreate || descriptor.Action == actionsecurity.ActionUsersCreateAdmin {
			descriptor.Active = true
			active[descriptor.Action] = descriptor
		}
	}
	resolve := func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		descriptor, ok := active[action]
		return descriptor, ok
	}
	service, err := newActionOperationService(f.db, limiter, authRedis, crypto, resolve, f.clock, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestCreateAccountRealResponseGUIDFailureRollsBackEntireOperation(t *testing.T) {
	requireDefaultMySQLAffectedRows(t)
	f, services := openA03CreateServices(t, 1_910_075_000_000)
	username := fixtureUsername(testSnowflake.Next())
	base := actionsecurity.CreateAccountIntent{Username: username, Role: models.UserRoleUser.String(), PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", newRealIdempotencyKey(t))
	userGUID, authAuditGUID := testSnowflake.Next(), testSnowflake.Next()
	execution.nextGUID = createAccountTestGUIDs(userGUID, authAuditGUID, 0)

	view, err := services.operations.Execute(context.Background(), identity, execution, execution, services.outbox)
	if view != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("response GUID failure = %#v/%v", view, err)
	}
	if result, ok := execution.ResultUser(); ok || result != nil {
		t.Fatalf("response GUID failure exposed result = %#v", result)
	}
	var userCount, authCount, responseCount, managementCount, outboxCount int64
	if err := f.db.Model(&models.User{}).Where("guid = ? OR username = ?", userGUID, username).Count(&userCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AuthAuditEvent{}).Where("guid = ?", authAuditGUID).Count(&authCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AdminOperationResponse{}).Where("operation_id = ?", identity.ID).Count(&responseCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AuditLog{}).Where("detail->>'$.operation_ref' = ?", identity.PublicRef).Count(&managementCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AdminActionOutbox{}).Where("public_ref = ?", identity.PublicRef).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	var operation models.AdminOperation
	if err := f.db.Where("id = ? AND public_ref = ?", identity.ID, identity.PublicRef).First(&operation).Error; err != nil {
		t.Fatal(err)
	}
	if userCount != 0 || authCount != 0 || responseCount != 0 || managementCount != 0 || outboxCount != 0 || operation.State != models.OperationProcessing ||
		operation.FinishedAt != nil || operation.ErrorCode != nil || operation.ResultKind != nil || operation.ResultGUID != nil || operation.ResultHTTPStatus != nil {
		t.Fatalf("partial response-GUID state user/auth/response/audit/outbox/op = %d/%d/%d/%d/%d/%#v", userCount, authCount, responseCount, managementCount, outboxCount, operation)
	}
	assertCreateHashCleared(t, execution)
}

func TestCreateAccountRealConcurrentUsernameRaceCommitsOneUser(t *testing.T) {
	requireDefaultMySQLAffectedRows(t)
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
		{"operation_response", "admin_operation_responses", 1, false},
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
			if result, ok := execution.ResultUser(); ok || result != nil {
				t.Fatalf("rollback exposed uncommitted result: %#v", result)
			}
			var userCount, authCount, responseCount, managementCount, outboxCount int64
			if err := f.db.Model(&models.User{}).Where("username = ?", username).Count(&userCount).Error; err != nil {
				t.Fatal(err)
			}
			_ = f.db.Model(&models.AuthAuditEvent{}).Joins("JOIN users ON users.id = auth_audit_events.user_id").Where("users.username = ?", username).Count(&authCount).Error
			_ = f.db.Model(&models.AdminOperationResponse{}).Where("operation_id = ?", identity.ID).Count(&responseCount).Error
			_ = f.db.Model(&models.AuditLog{}).Where("detail->>'$.operation_ref' = ?", identity.PublicRef).Count(&managementCount).Error
			_ = f.db.Model(&models.AdminActionOutbox{}).Where("public_ref = ?", identity.PublicRef).Count(&outboxCount).Error
			var operation models.AdminOperation
			if err := f.db.Where("public_ref = ?", identity.PublicRef).First(&operation).Error; err != nil {
				t.Fatal(err)
			}
			if userCount != 0 || authCount != 0 || responseCount != 0 || managementCount != 0 || outboxCount != 0 || operation.State != models.OperationProcessing ||
				operation.ResultGUID != nil || operation.ResultKind != nil || operation.FinishedAt != nil {
				t.Fatalf("partial state user/auth/response/audit/outbox/op = %d/%d/%d/%d/%d/%#v", userCount, authCount, responseCount, managementCount, outboxCount, operation)
			}
			assertCreateHashCleared(t, execution)
		})
	}
}

func TestCreateAccountRealCommitUnknownDoesNotPublishResult(t *testing.T) {
	requireDefaultMySQLAffectedRows(t)
	f, services := openA03CreateServices(t, 1_910_300_000_000)
	username := fixtureUsername(testSnowflake.Next())
	key := newRealIdempotencyKey(t)
	base := actionsecurity.CreateAccountIntent{Username: username, Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	identity, execution := services.prepare(t, f, base, "A03-Strong-Password!", key)
	runner := &realCommitUnknownRunner{}
	view, err := services.operations.executeWithRunner(context.Background(), identity, execution, execution, services.outbox, runner)
	var unknown *CommitUnknownError
	if view != nil || !errors.As(err, &unknown) || unknown.PublicRef != identity.PublicRef || runner.calls.Load() != 1 {
		t.Fatalf("commit unknown = %#v/%v calls=%d", view, err, runner.calls.Load())
	}
	if result, ok := execution.ResultUser(); ok || result != nil {
		t.Fatalf("commit-unknown exposed result: %#v", result)
	}
	queried, queryErr := services.operations.Query(context.Background(), actionsecurity.ActionUsersCreate, f.actorAPI, []string{key})
	if queryErr != nil || queried == nil || queried.Status != "succeeded" || queried.PublicRef != identity.PublicRef {
		t.Fatalf("commit-unknown query = %#v/%v", queried, queryErr)
	}
	assertCreateHashCleared(t, execution)
}
