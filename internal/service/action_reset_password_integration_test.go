package service

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
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

type a07ResetTargetCredentials struct {
	sessions      []models.Session
	refreshTokens []string
	activeKey     models.GatewayAPIToken
	activeSecret  string
	revokedKey    models.GatewayAPIToken
	revokedSecret string
}

func createA07ResetTarget(t *testing.T, fixture *realActionFixture, status models.UserStatus) (models.User, a07ResetTargetCredentials) {
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
	credentials := a07ResetTargetCredentials{}
	for index := 0; index < 3; index++ {
		sid, sidErr := security.NewSessionSID()
		if sidErr != nil {
			t.Fatal(sidErr)
		}
		secret, secretErr := security.NewRefreshSecret()
		if secretErr != nil {
			t.Fatal(secretErr)
		}
		session := models.Session{AuditFields: a14Audit(fixture.clock.NowMillis()), SID: sid, UserID: target.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: index + 1, RefreshHMAC: security.RefreshHMAC(secret, testSessionSettings().AuthHMACKey), LastActiveAt: fixture.clock.NowMillis(), ExpiresAt: fixture.clock.NowMillis() + 86_400_000}
		if err := fixture.db.Create(&session).Error; err != nil {
			t.Fatal(err)
		}
		credentials.sessions = append(credentials.sessions, session)
		credentials.refreshTokens = append(credentials.refreshTokens, sid+"."+secret)
	}
	activeKey, activeSecret, err := NewGatewayTokenService(fixture.db).Create(&target, GatewayTokenCreateInput{Name: "a07-reset-active-key"})
	if err != nil {
		t.Fatal(err)
	}
	revokedKey, revokedSecret, err := NewGatewayTokenService(fixture.db).Create(&target, GatewayTokenCreateInput{Name: "a07-reset-revoked-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewGatewayTokenService(fixture.db).Revoke(target.ID, revokedKey.Guid); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(revokedKey, revokedKey.ID).Error; err != nil {
		t.Fatal(err)
	}
	credentials.activeKey, credentials.activeSecret = *activeKey, activeSecret
	credentials.revokedKey, credentials.revokedSecret = *revokedKey, revokedSecret
	return target, credentials
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
			target, credentials := createA07ResetTarget(t, fixture, status)
			sessionService := NewSessionService(fixture.db, fixture.authRedis, testSessionSettings())
			sessionService.now = fixture.clock.NowMillis
			authSettings := testSessionSettings()
			authSettings.PasswordLoginEnabled = true
			authSettings.JWTSecretKey = "a07-reset-login-secret"
			authSettings.SessionAccessMinutes = 5
			auth := NewAuthService(authSettings, nil, fixture.db)
			auth.SetSessionService(sessionService)
			access, err := security.CreateAccessToken(fmt.Sprint(target.Guid), authSettings.JWTSecretKey, authSettings.SessionAccessMinutes, map[string]interface{}{"sid": credentials.sessions[0].SID, "sv": credentials.sessions[0].SessionVersion, "av": target.AuthVersion, "role": int(target.Role)})
			if err != nil || access == "" {
				t.Fatalf("old access issue=%q err=%v", access, err)
			}
			claims, err := security.DecodeAccessToken(access, authSettings.JWTSecretKey)
			if err != nil {
				t.Fatalf("decode old Access: %v", err)
			}
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
			for _, session := range credentials.sessions {
				var storedSession models.Session
				if err := fixture.db.First(&storedSession, session.ID).Error; err != nil || storedSession.RevokedAt == nil {
					t.Fatalf("stored session=%#v err=%v", storedSession, err)
				}
				if revoked, err := fixture.authRedis.IsSessionRevoked(context.Background(), session.SID); err != nil || !revoked {
					t.Fatalf("redis revoked sid=%s: %t err=%v", session.SID, revoked, err)
				}
			}
			if _, err := sessionService.Validate(context.Background(), claims["sid"].(string), target.ID, int(claims["sv"].(float64)), int(claims["av"].(float64))); err == nil {
				t.Fatal("old Access session proof remained valid after reset")
			}
			if _, err := sessionService.Refresh(context.Background(), credentials.refreshTokens[0]); err == nil {
				t.Fatal("old Refresh remained valid after reset")
			}
			for _, token := range []models.GatewayAPIToken{credentials.activeKey, credentials.revokedKey} {
				var storedToken models.GatewayAPIToken
				if err := fixture.db.First(&storedToken, token.ID).Error; err != nil || storedToken.Status != token.Status || storedToken.TokenHash != token.TokenHash {
					t.Fatalf("gateway key changed=%#v err=%v", storedToken, err)
				}
			}
			keys := NewGatewayTokenService(fixture.db)
			if status == models.UserStatusActive {
				if _, err := keys.AuthenticatePrincipal(credentials.activeSecret, "127.0.0.1", "", time.Now()); err != nil {
					t.Fatalf("active Gateway Key after reset: %v", err)
				}
			} else if _, err := keys.AuthenticatePrincipal(credentials.activeSecret, "127.0.0.1", "", time.Now()); !IsGatewayTokenError(err, GatewayTokenDisabled) {
				t.Fatalf("disabled owner Gateway Key error=%v", err)
			}
			if _, err := keys.AuthenticatePrincipal(credentials.revokedSecret, "127.0.0.1", "", time.Now()); !IsGatewayTokenError(err, GatewayTokenRevoked) {
				t.Fatalf("revoked Gateway Key error=%v", err)
			}
			if user, issued, token, err := auth.LoginUsername(context.Background(), *stored.Username, "A07-Old-Password!", SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.40"}); err == nil || user != nil || issued != nil || token != "" {
				t.Fatalf("old password login user=%#v issued=%#v token=%q err=%v", user, issued, token, err)
			}
			if status == models.UserStatusDisabled {
				if user, issued, token, err := auth.LoginUsername(context.Background(), *stored.Username, newPassword, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.41"}); err == nil || user != nil || issued != nil || token != "" {
					t.Fatalf("disabled new password login user=%#v issued=%#v token=%q err=%v", user, issued, token, err)
				}
				statusActor := AdminPermissionReadActor{UserID: fixture.actor.UserID, AuthVersion: fixture.actor.AuthVersion, SessionSID: fixture.actor.SessionSID, SessionVersion: fixture.actor.SessionVersion}
				enabled, err := NewAdminUserStatusService(fixture.db, fixture.authRedis).Change(context.Background(), statusActor, target.Guid, AdminUserStatusInput{Status: models.UserStatusActive, ExpectedAuthVersion: target.AuthVersion + 1})
				if err != nil || enabled == nil || enabled.Status != models.UserStatusActive.String() {
					t.Fatalf("enable result=%#v err=%v", enabled, err)
				}
				if user, issued, token, err := auth.LoginUsername(context.Background(), *stored.Username, "A07-Old-Password!", SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.43"}); err == nil || user != nil || issued != nil || token != "" {
					t.Fatalf("enabled old password login user=%#v issued=%#v token=%q err=%v", user, issued, token, err)
				}
				if _, err := keys.AuthenticatePrincipal(credentials.activeSecret, "127.0.0.1", "", time.Now()); err != nil {
					t.Fatalf("active Gateway Key after enable: %v", err)
				}
			}
			if user, issued, token, err := auth.LoginUsername(context.Background(), *stored.Username, newPassword, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.42"}); err != nil || user == nil || issued == nil || token == "" {
				t.Fatalf("new password login user=%#v issued=%#v token=%q err=%v", user, issued, token, err)
			}
			var sessionAuditCount int64
			if err := fixture.db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ? AND is_deleted = 0", target.ID, models.AuthAuditEventSessionRevoked).Count(&sessionAuditCount).Error; err != nil || sessionAuditCount != int64(len(credentials.sessions)) {
				t.Fatalf("session revoke audits=%d want=%d err=%v", sessionAuditCount, len(credentials.sessions), err)
			}
			var sessionAudits []models.AuthAuditEvent
			if err := fixture.db.Where("user_id = ? AND event_type = ? AND is_deleted = 0", target.ID, models.AuthAuditEventSessionRevoked).Find(&sessionAudits).Error; err != nil {
				t.Fatal(err)
			}
			seenSessionGUIDs := make(map[int64]int, len(sessionAudits))
			for _, audit := range sessionAudits {
				if audit.SessionGuid == nil {
					t.Fatalf("session revoke audit lacks session_guid: %#v", audit)
				}
				seenSessionGUIDs[*audit.SessionGuid]++
			}
			for _, session := range credentials.sessions {
				if seenSessionGUIDs[session.Guid] != 1 {
					t.Fatalf("session %d audit occurrences=%d want=1", session.Guid, seenSessionGUIDs[session.Guid])
				}
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

func TestResetPasswordRealConcurrentSameIdempotencyKeyExecutesOnceAndReplays(t *testing.T) {
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	fixture := openRealActionFixtureOnDB(t, 1_800_305_000_000, db)
	services := newA07ResetServices(t, fixture)
	target, credentials := createA07ResetTarget(t, fixture, models.UserStatusActive)
	password := "A07-Concurrent-New!"
	issueIntent := actionsecurity.ResetPasswordIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, NewPassword: []byte(password), Reason: "security rotation"}
	issued, err := services.verification.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersResetPassword, Actor: fixture.actor, TargetGUID: &target.Guid, Intent: issueIntent, CurrentPassword: []byte(fixture.password), TrustedIP: "203.0.113.78"})
	if err != nil {
		t.Fatal(err)
	}
	key := newRealIdempotencyKey(t)
	type beginResult struct {
		identity *OperationIdentity
		view     *OperationView
		err      error
	}
	start := make(chan struct{})
	results := make(chan beginResult, 2)
	for range 2 {
		go func() {
			<-start
			intent := actionsecurity.ResetPasswordIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, NewPassword: []byte(password), Reason: "security rotation"}
			identity, view, beginErr := services.operation.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersResetPassword, Actor: fixture.actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{issued.Ticket}, Intent: intent})
			results <- beginResult{identity: identity, view: view, err: beginErr}
		}()
	}
	close(start)
	var ready *OperationIdentity
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Begin: %v", result.err)
		}
		if result.identity != nil && result.identity.ReadyForExecution() {
			if ready != nil {
				t.Fatal("two concurrent Begin calls became execution-ready")
			}
			ready = result.identity
		}
	}
	if ready == nil {
		t.Fatal("no concurrent Begin call became execution-ready")
	}
	hash, err := HashManagedCreationPasswordBytes([]byte(password))
	if err != nil {
		t.Fatal(err)
	}
	execution, err := newResetPasswordExecution(services.descriptor, actionsecurity.ResetPasswordIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, NewPassword: []byte(password), Reason: "security rotation"}, hash, fixture.authRedis, fixture.clock, func() int64 { return testSnowflake.Next() }, fixture.crypto, ResetPasswordRequestMetadata{RequestID: "a07-reset-concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := services.operation.Execute(context.Background(), ready, execution, execution, services.outbox)
	if err != nil || view == nil || view.Status != "succeeded" {
		t.Fatalf("Execute view=%#v err=%v", view, err)
	}
	replayIntent := actionsecurity.ResetPasswordIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, NewPassword: []byte(password), Reason: "security rotation"}
	replayIdentity, replay, err := services.operation.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersResetPassword, Actor: fixture.actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{issued.Ticket}, Intent: replayIntent})
	if err != nil || replayIdentity == nil || replayIdentity.ReadyForExecution() || replay == nil || replay.PublicRef != view.PublicRef || replay.ResultAuthVersion == nil || *replay.ResultAuthVersion != target.AuthVersion+1 {
		t.Fatalf("stable replay identity=%v view=%#v err=%v", replayIdentity, replay, err)
	}
	var passwordAudits, managementAudits int64
	if err := fixture.db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ?", target.ID, models.AuthAuditEventPasswordChanged).Count(&passwordAudits).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.AuditLog{}).Where("user_id = ? AND action = ?", target.ID, "users.reset_password").Count(&managementAudits).Error; err != nil {
		t.Fatal(err)
	}
	if passwordAudits != 1 || managementAudits != 1 {
		t.Fatalf("concurrent mutation audit counts password/management=%d/%d", passwordAudits, managementAudits)
	}
	var sessionAudits int64
	if err := fixture.db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ?", target.ID, models.AuthAuditEventSessionRevoked).Count(&sessionAudits).Error; err != nil || sessionAudits != int64(len(credentials.sessions)) {
		t.Fatalf("session audits=%d want=%d err=%v", sessionAudits, len(credentials.sessions), err)
	}
}

func TestResetPasswordRealRedisBarrierFailureRollsBackEveryMySQLFact(t *testing.T) {
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	fixture := openRealActionFixtureOnDB(t, 1_800_310_000_000, db)
	services := newA07ResetServices(t, fixture)
	target, credentials := createA07ResetTarget(t, fixture, models.UserStatusActive)
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
	assertA07ResetMySQLRolledBack(t, fixture, target, credentials.sessions[0], prepared)
	if revoked, err := fixture.authRedis.IsSessionRevoked(context.Background(), credentials.sessions[0].SID); err != nil || revoked {
		t.Fatalf("failed barrier changed working Redis revoked=%t err=%v", revoked, err)
	}
}

func TestResetPasswordRealPostMutationAuditSQLFailureRollsBackMySQLAndKeepsDenial(t *testing.T) {
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	fixture := openRealActionFixtureOnDB(t, 1_800_320_000_000, db)
	services := newA07ResetServices(t, fixture)
	target, credentials := createA07ResetTarget(t, fixture, models.UserStatusActive)
	prepared := prepareA07Reset(t, fixture, services, target, "A07-Rollback-Pass!", fixture.authRedis)
	audit := a07ResetSQLFailingAuditWriter{delegate: prepared.execution}
	view, err := services.operation.Execute(context.Background(), prepared.identity, prepared.execution, audit, services.outbox)
	if view != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("audit SQL failure view=%#v err=%v", view, err)
	}
	assertA07ResetMySQLRolledBack(t, fixture, target, credentials.sessions[0], prepared)
	if revoked, err := fixture.authRedis.IsSessionRevoked(context.Background(), credentials.sessions[0].SID); err != nil || !revoked {
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
