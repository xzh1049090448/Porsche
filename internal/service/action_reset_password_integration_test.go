package service

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type a07ResetServices struct {
	verification *ActionVerificationService
	operation    *ActionOperationService
	outbox       *ResetPasswordOutboxWriter
	descriptor   actionsecurity.Descriptor
}

type a07PreparedReset struct {
	identity     *OperationIdentity
	execution    *ResetPasswordExecution
	key          string
	ticket       string
	operation    models.AdminOperation
	verification models.AdminActionVerification
}

type a07ResetSQLFailingAuditWriter struct{ delegate TransactionalAuditWriter }

func (writer a07ResetSQLFailingAuditWriter) Write(ctx context.Context, tx *gorm.DB, event ActionAuditEvent) error {
	if err := writer.delegate.Write(ctx, tx, event); err != nil {
		return err
	}
	return tx.Exec("INSERT INTO a07_reset_missing_audit_sink (operation_ref) VALUES (?)", event.PublicRef).Error
}

func newA07ResetServices(t *testing.T, fixture *realActionFixture) a07ResetServices {
	t.Helper()
	descriptor, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersResetPassword)
	if !ok {
		t.Fatal("reset descriptor inactive")
	}
	resolve := func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		return descriptor, action == actionsecurity.ActionUsersResetPassword
	}
	verification, err := newActionVerificationService(fixture.db, fixture.limiter, fixture.authRedis, fixture.crypto, resolve, fixture.clock, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newActionOperationService(fixture.db, fixture.limiter, fixture.authRedis, fixture.crypto, resolve, fixture.clock, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := NewResetPasswordOutboxWriter(func() int64 { return testSnowflake.Next() }, fixture.clock)
	if err != nil {
		t.Fatal(err)
	}
	return a07ResetServices{verification: verification, operation: operation, outbox: outbox, descriptor: descriptor}
}

func createA07ResetTarget(t *testing.T, fixture *realActionFixture, status models.UserStatus) (models.User, models.Session, models.GatewayAPIToken) {
	t.Helper()
	username := fixtureUsername(testSnowflake.Next())
	oldHash, err := security.HashPassword("A07-Old-Password!")
	if err != nil {
		t.Fatal(err)
	}
	target := models.User{AuditFields: a14Audit(fixture.clock.NowMillis()), GroupID: testDefaultBusinessGroupID(t, fixture.db), Username: &username, PasswordHash: &oldHash, Role: models.UserRoleUser, Status: status, AuthVersion: 4, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{}}
	if err := fixture.db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	session := models.Session{AuditFields: a14Audit(fixture.clock.NowMillis()), SID: sid, UserID: target.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 2, RefreshHMAC: strings.Repeat("c", 64), LastActiveAt: fixture.clock.NowMillis(), ExpiresAt: fixture.clock.NowMillis() + 86_400_000}
	if err := fixture.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	token, _, err := NewGatewayTokenService(fixture.db).Create(&target, GatewayTokenCreateInput{Name: "a07-reset-key"})
	if err != nil {
		t.Fatal(err)
	}
	return target, session, *token
}

func prepareA07Reset(t *testing.T, fixture *realActionFixture, services a07ResetServices, target models.User, password string, executionRedis *AuthRedis) a07PreparedReset {
	t.Helper()
	makeIntent := func() actionsecurity.ResetPasswordIntent {
		return actionsecurity.ResetPasswordIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, NewPassword: []byte(password), Reason: "security rotation"}
	}
	issueIntent := makeIntent()
	issued, err := services.verification.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersResetPassword, Actor: fixture.actor, TargetGUID: &target.Guid, Intent: issueIntent, CurrentPassword: []byte(fixture.password), TrustedIP: "203.0.113.77"})
	if err != nil {
		t.Fatal(err)
	}
	key := newRealIdempotencyKey(t)
	beginIntent := makeIntent()
	identity, view, err := services.operation.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersResetPassword, Actor: fixture.actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{issued.Ticket}, Intent: beginIntent})
	if err != nil || identity == nil || view == nil || !identity.ReadyForExecution() {
		t.Fatalf("Begin identity=%v view=%v err=%v", identity, view, err)
	}
	plain := []byte(password)
	hash, err := HashManagedCreationPasswordBytes(plain)
	clear(plain)
	if err != nil {
		t.Fatal(err)
	}
	executionIntent := makeIntent()
	execution, err := newResetPasswordExecution(services.descriptor, executionIntent, hash, executionRedis, fixture.clock, func() int64 { return testSnowflake.Next() }, fixture.crypto, ResetPasswordRequestMetadata{RequestID: "a07-reset-real"})
	if err != nil {
		t.Fatal(err)
	}
	var operation models.AdminOperation
	if err := fixture.db.Where("id = ?", identity.ID).First(&operation).Error; err != nil || operation.VerificationID == nil {
		t.Fatalf("prepared operation=%#v err=%v", operation, err)
	}
	var verification models.AdminActionVerification
	if err := fixture.db.Where("id = ?", *operation.VerificationID).First(&verification).Error; err != nil {
		t.Fatal(err)
	}
	return a07PreparedReset{identity: identity, execution: execution, key: key, ticket: issued.Ticket, operation: operation, verification: verification}
}

func executeA07Reset(t *testing.T, fixture *realActionFixture, services a07ResetServices, target models.User, password string, executionRedis *AuthRedis, audit TransactionalAuditWriter) (*OperationView, string, string) {
	t.Helper()
	prepared := prepareA07Reset(t, fixture, services, target, password, executionRedis)
	if audit == nil {
		audit = prepared.execution
	}
	view, err := services.operation.Execute(context.Background(), prepared.identity, prepared.execution, audit, services.outbox)
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	return view, prepared.key, prepared.ticket
}

func TestResetPasswordRealSuccessStableReplayAndEntitlementPreservation(t *testing.T) {
	for _, status := range []models.UserStatus{models.UserStatusActive, models.UserStatusDisabled} {
		t.Run(status.String(), func(t *testing.T) {
			db := openTestMySQL(t)
			prepareAuthSessionSchema(t, db)
			fixture := openRealActionFixtureOnDB(t, 1_800_200_000_000+int64(status)*10_000, db)
			services := newA07ResetServices(t, fixture)
			target, session, token := createA07ResetTarget(t, fixture, status)
			newPassword := "A07-New-Password!"
			view, key, ticket := executeA07Reset(t, fixture, services, target, newPassword, fixture.authRedis, nil)
			if view == nil || view.Status != "succeeded" || view.ResultAuthVersion == nil || *view.ResultAuthVersion != target.AuthVersion+1 || view.TargetGUID == nil || *view.TargetGUID != target.Guid {
				t.Fatalf("success view=%#v", view)
			}
			result, err := services.operation.ResetPasswordResponse(context.Background(), fixture.actor, view.PublicRef)
			if err != nil || result.TargetGUID != target.Guid || result.ResultingAuthVersion != target.AuthVersion+1 {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			var stored models.User
			if err := fixture.db.First(&stored, target.ID).Error; err != nil || stored.PasswordHash == nil || !security.VerifyPassword(newPassword, *stored.PasswordHash) || stored.AuthVersion != target.AuthVersion+1 || stored.Status != status {
				t.Fatalf("stored target=%#v err=%v", stored, err)
			}
			var storedSession models.Session
			if err := fixture.db.First(&storedSession, session.ID).Error; err != nil || storedSession.RevokedAt == nil {
				t.Fatalf("stored session=%#v err=%v", storedSession, err)
			}
			if revoked, err := fixture.authRedis.IsSessionRevoked(context.Background(), session.SID); err != nil || !revoked {
				t.Fatalf("redis revoked=%t err=%v", revoked, err)
			}
			var storedToken models.GatewayAPIToken
			if err := fixture.db.First(&storedToken, token.ID).Error; err != nil || storedToken.Status != token.Status || storedToken.TokenHash != token.TokenHash {
				t.Fatalf("gateway key changed=%#v err=%v", storedToken, err)
			}
			replayIntent := actionsecurity.ResetPasswordIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, NewPassword: []byte(newPassword), Reason: "security rotation"}
			identity, replay, err := services.operation.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersResetPassword, Actor: fixture.actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: replayIntent})
			if err != nil || identity == nil || identity.ReadyForExecution() || replay == nil || replay.PublicRef != view.PublicRef || replay.Status != "succeeded" || replay.ResultAuthVersion == nil || *replay.ResultAuthVersion != target.AuthVersion+1 {
				t.Fatalf("replay identity=%v view=%#v err=%v", identity, replay, err)
			}
			queried, err := services.operation.Query(context.Background(), actionsecurity.ActionUsersResetPassword, fixture.actor, []string{key})
			if err != nil || queried.PublicRef != view.PublicRef || queried.ResultAuthVersion == nil || *queried.ResultAuthVersion != target.AuthVersion+1 {
				t.Fatalf("query=%#v err=%v", queried, err)
			}
		})
	}
}

func TestResetPasswordRealRedisBarrierFailureRollsBackEveryMySQLFact(t *testing.T) {
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	fixture := openRealActionFixtureOnDB(t, 1_800_310_000_000, db)
	services := newA07ResetServices(t, fixture)
	target, session, _ := createA07ResetTarget(t, fixture, models.UserStatusActive)
	badClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = badClient.Close() })
	badRedis, err := NewAuthRedis(badClient, "a07-reset-failing-redis-hmac-material")
	if err != nil {
		t.Fatal(err)
	}
	prepared := prepareA07Reset(t, fixture, services, target, "A07-Failed-Password!", badRedis)
	view, err := services.operation.Execute(context.Background(), prepared.identity, prepared.execution, prepared.execution, services.outbox)
	if view != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("Redis barrier failure view=%#v err=%v", view, err)
	}
	assertA07ResetMySQLRolledBack(t, fixture, target, session, prepared)
	if revoked, err := fixture.authRedis.IsSessionRevoked(context.Background(), session.SID); err != nil || revoked {
		t.Fatalf("failed barrier changed working Redis revoked=%t err=%v", revoked, err)
	}
}

func TestResetPasswordRealPostMutationAuditSQLFailureRollsBackMySQLAndKeepsDenial(t *testing.T) {
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	fixture := openRealActionFixtureOnDB(t, 1_800_320_000_000, db)
	services := newA07ResetServices(t, fixture)
	target, session, _ := createA07ResetTarget(t, fixture, models.UserStatusActive)
	prepared := prepareA07Reset(t, fixture, services, target, "A07-Rollback-Pass!", fixture.authRedis)
	audit := a07ResetSQLFailingAuditWriter{delegate: prepared.execution}
	view, err := services.operation.Execute(context.Background(), prepared.identity, prepared.execution, audit, services.outbox)
	if view != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("audit SQL failure view=%#v err=%v", view, err)
	}
	assertA07ResetMySQLRolledBack(t, fixture, target, session, prepared)
	if revoked, err := fixture.authRedis.IsSessionRevoked(context.Background(), session.SID); err != nil || !revoked {
		t.Fatalf("post-barrier rollback lost Redis denial revoked=%t err=%v", revoked, err)
	}
	fixture.clock.Set(*prepared.operation.LeaseExpiresAt + actionOperationRecoveryGraceMS + 1)
	if err := services.operation.MarkPendingRecovery(context.Background(), prepared.operation.ID); err != nil {
		t.Fatalf("mark pending recovery: %v", err)
	}
	var recovered models.AdminOperation
	if err := fixture.db.First(&recovered, prepared.operation.ID).Error; err != nil || recovered.State != models.OperationPendingRecovery || recovered.LeaseOwnerHMAC != nil || recovered.LeaseExpiresAt != nil {
		t.Fatalf("recovered operation=%#v err=%v", recovered, err)
	}
}

func assertA07ResetMySQLRolledBack(t *testing.T, fixture *realActionFixture, target models.User, session models.Session, prepared a07PreparedReset) {
	t.Helper()
	var storedTarget models.User
	if err := fixture.db.First(&storedTarget, target.ID).Error; err != nil || storedTarget.AuthVersion != target.AuthVersion || storedTarget.PasswordHash == nil || target.PasswordHash == nil || *storedTarget.PasswordHash != *target.PasswordHash {
		t.Fatalf("target rollback=%#v err=%v", storedTarget, err)
	}
	var storedSession models.Session
	if err := fixture.db.First(&storedSession, session.ID).Error; err != nil || storedSession.RevokedAt != nil || storedSession.SessionVersion != session.SessionVersion || storedSession.RefreshHMAC != session.RefreshHMAC {
		t.Fatalf("session rollback=%#v err=%v", storedSession, err)
	}
	var operation models.AdminOperation
	if err := fixture.db.First(&operation, prepared.operation.ID).Error; err != nil || operation.State != models.OperationProcessing || operation.FinishedAt != nil || operation.ResultKind != nil || operation.ResultGUID != nil || operation.ResultAuthVersion != nil || operation.ErrorCode != nil || operation.LeaseOwnerHMAC == nil || prepared.operation.LeaseOwnerHMAC == nil || *operation.LeaseOwnerHMAC != *prepared.operation.LeaseOwnerHMAC || operation.LeaseExpiresAt == nil || prepared.operation.LeaseExpiresAt == nil || *operation.LeaseExpiresAt != *prepared.operation.LeaseExpiresAt {
		t.Fatalf("operation rollback=%#v err=%v", operation, err)
	}
	var verification models.AdminActionVerification
	if err := fixture.db.First(&verification, prepared.verification.ID).Error; err != nil || verification.ConsumedAt != nil || verification.IsDeleted != 0 || verification.UpdatedAt != prepared.verification.UpdatedAt {
		t.Fatalf("verification rollback=%#v err=%v", verification, err)
	}
	var passwordAudits, sessionAudits, managementAudits, outboxRows int64
	if err := fixture.db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ?", target.ID, models.AuthAuditEventPasswordChanged).Count(&passwordAudits).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ?", target.ID, models.AuthAuditEventSessionRevoked).Count(&sessionAudits).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.AuditLog{}).Where("user_id = ? AND action = ?", target.ID, "users.reset_password").Count(&managementAudits).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.AdminActionOutbox{}).Where("public_ref = ?", prepared.operation.PublicRef).Count(&outboxRows).Error; err != nil {
		t.Fatal(err)
	}
	if passwordAudits != 0 || sessionAudits != 0 || managementAudits != 0 || outboxRows != 0 {
		t.Fatalf("rollback counts password/session/management/outbox=%d/%d/%d/%d", passwordAudits, sessionAudits, managementAudits, outboxRows)
	}
}
