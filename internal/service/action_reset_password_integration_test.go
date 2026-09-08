package service

import (
	"context"
	cryptorand "crypto/rand"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
)

type a07ResetServices struct {
	verification *ActionVerificationService
	operation    *ActionOperationService
	outbox       *ResetPasswordOutboxWriter
	descriptor   actionsecurity.Descriptor
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

func executeA07Reset(t *testing.T, fixture *realActionFixture, services a07ResetServices, target models.User, password string, executionRedis *AuthRedis, audit TransactionalAuditWriter) (*OperationView, string, string) {
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
	if audit == nil {
		audit = execution
	}
	view, err = services.operation.Execute(context.Background(), identity, execution, audit, services.outbox)
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	return view, key, issued.Ticket
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
