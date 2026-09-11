package service

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
)

func TestA08RolePermissionRealOperationChainCommitsAtomicFacts(t *testing.T) {
	f, identity, execution, outbox, targetSessions := prepareA08RealPromote(t, 1_920_000_000_000)
	view, err := f.operation.Execute(context.Background(), identity, execution, execution, outbox)
	if err != nil || view == nil || view.Status != "succeeded" || view.TargetGUID == nil || *view.TargetGUID != f.targetRow.Guid ||
		view.ResultAuthVersion == nil || *view.ResultAuthVersion != f.targetRow.AuthVersion+1 || view.ResultPermissionsVersion == nil ||
		*view.ResultPermissionsVersion != 1 || view.ResultRole == nil || *view.ResultRole != models.UserRoleAdmin {
		t.Fatalf("Execute = %#v/%v", view, err)
	}
	var stored models.User
	if err := f.db.Where("id = ?", f.targetRow.ID).First(&stored).Error; err != nil || stored.Role != models.UserRoleAdmin || stored.AuthVersion != f.targetRow.AuthVersion+1 {
		t.Fatalf("stored target = %#v/%v", stored, err)
	}
	var revoked, sessionEvents, managedEvents, managementAudits, outboxRows int64
	if err := f.db.Model(&models.Session{}).Where("user_id = ? AND revoked_at IS NOT NULL", f.targetRow.ID).Count(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ?", f.targetRow.ID, models.AuthAuditEventSessionRevoked).Count(&sessionEvents).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ?", f.targetRow.ID, models.AuthAuditEventManagedUserUpdated).Count(&managedEvents).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AuditLog{}).Where("user_id = ? AND action = ?", f.targetRow.ID, "users.promote").Count(&managementAudits).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AdminActionOutbox{}).Where("public_ref = ?", identity.PublicRef).Count(&outboxRows).Error; err != nil {
		t.Fatal(err)
	}
	if revoked != int64(len(targetSessions)) || sessionEvents != int64(len(targetSessions)) || managedEvents != 1 || managementAudits != 1 || outboxRows != 1 {
		t.Fatalf("facts revoked/session/managed/audit/outbox = %d/%d/%d/%d/%d", revoked, sessionEvents, managedEvents, managementAudits, outboxRows)
	}
}

func TestA08RolePermissionRealSameStatePreflightLeavesIssuedChainUnchanged(t *testing.T) {
	f, identity, execution, outbox, targetSessions := prepareA08RealPromote(t, 1_920_005_000_000)
	if err := f.db.Model(&models.User{}).Where("id = ?", f.targetRow.ID).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	var beforeOperation models.AdminOperation
	if err := f.db.Where("id = ?", identity.ID).First(&beforeOperation).Error; err != nil || beforeOperation.VerificationID == nil {
		t.Fatalf("load operation = %#v/%v", beforeOperation, err)
	}
	var beforeVerification models.AdminActionVerification
	if err := f.db.Where("id = ?", *beforeOperation.VerificationID).First(&beforeVerification).Error; err != nil {
		t.Fatal(err)
	}
	var beforeTarget models.User
	var beforeSessions []models.Session
	if err := f.db.Where("id = ?", f.targetRow.ID).First(&beforeTarget).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("user_id = ?", f.targetRow.ID).Order("id ASC").Find(&beforeSessions).Error; err != nil {
		t.Fatal(err)
	}
	countFacts := func() [6]int64 {
		var counts [6]int64
		queries := []*gorm.DB{
			f.db.Model(&models.AuthAuditEvent{}).Where("user_id = ?", f.targetRow.ID).Count(&counts[0]),
			f.db.Model(&models.AuditLog{}).Where("user_id = ? AND action = ?", f.targetRow.ID, "users.promote").Count(&counts[1]),
			f.db.Model(&models.AdminActionOutbox{}).Where("public_ref = ?", identity.PublicRef).Count(&counts[2]),
			f.db.Model(&models.AdminOperationResponse{}).Where("operation_id = ?", identity.ID).Count(&counts[3]),
			f.db.Model(&models.PermissionPolicyHead{}).Where("user_id = ?", f.targetRow.ID).Count(&counts[4]),
			f.db.Model(&models.PermissionOverride{}).Where("user_id = ?", f.targetRow.ID).Count(&counts[5]),
		}
		for _, query := range queries {
			if query.Error != nil {
				t.Fatal(query.Error)
			}
		}
		return counts
	}
	beforeFacts := countFacts()

	view, err := f.operation.Execute(context.Background(), identity, execution, execution, outbox)
	if view != nil || !errors.Is(err, ErrRolePermissionTargetStateConflict) || err.Error() != models.FailureTargetStateConflict.String() {
		t.Fatalf("same-state Execute = %#v/%v", view, err)
	}
	var afterOperation models.AdminOperation
	var afterVerification models.AdminActionVerification
	var afterTarget models.User
	var afterSessions []models.Session
	if err := f.db.Where("id = ?", identity.ID).First(&afterOperation).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("id = ?", beforeVerification.ID).First(&afterVerification).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("id = ?", f.targetRow.ID).First(&afterTarget).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("user_id = ?", f.targetRow.ID).Order("id ASC").Find(&afterSessions).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterOperation, beforeOperation) || !reflect.DeepEqual(afterVerification, beforeVerification) ||
		!reflect.DeepEqual(afterTarget, beforeTarget) || !reflect.DeepEqual(afterSessions, beforeSessions) || countFacts() != beforeFacts {
		t.Fatalf("same-state mutated chain: operation=%t verification=%t target=%t sessions=%t facts=%v/%v",
			reflect.DeepEqual(afterOperation, beforeOperation), reflect.DeepEqual(afterVerification, beforeVerification),
			reflect.DeepEqual(afterTarget, beforeTarget), reflect.DeepEqual(afterSessions, beforeSessions), countFacts(), beforeFacts)
	}
	if len(targetSessions) != len(beforeSessions) {
		t.Fatalf("target session fixture changed: target=%d all=%d", len(targetSessions), len(beforeSessions))
	}
	for _, session := range targetSessions {
		revoked, redisErr := f.authRedis.IsSessionRevoked(context.Background(), session.SID)
		if redisErr != nil || revoked {
			t.Fatalf("session %s Redis revoked=%t err=%v", session.SID, revoked, redisErr)
		}
	}
}

func TestA08RolePermissionRealOutboxFailureRollsBackEverySQLFact(t *testing.T) {
	f, identity, execution, _, targetSessions := prepareA08RealPromote(t, 1_920_010_000_000)
	view, err := f.operation.Execute(context.Background(), identity, execution, execution, realFailingOutboxWriter{err: errors.New("isolated outbox failure")})
	if view != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("Execute = %#v/%v", view, err)
	}
	var stored models.User
	if err := f.db.Where("id = ?", f.targetRow.ID).First(&stored).Error; err != nil || stored.Role != f.targetRow.Role || stored.AuthVersion != f.targetRow.AuthVersion {
		t.Fatalf("rolled-back target = %#v/%v", stored, err)
	}
	var revoked, authEvents, managementAudits, outboxRows int64
	if err := f.db.Model(&models.Session{}).Where("user_id = ? AND revoked_at IS NOT NULL", f.targetRow.ID).Count(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AuthAuditEvent{}).Where("user_id = ?", f.targetRow.ID).Count(&authEvents).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AuditLog{}).Where("user_id = ? AND action = ?", f.targetRow.ID, "users.promote").Count(&managementAudits).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.AdminActionOutbox{}).Where("public_ref = ?", identity.PublicRef).Count(&outboxRows).Error; err != nil {
		t.Fatal(err)
	}
	if revoked != 0 || authEvents != 0 || managementAudits != 0 || outboxRows != 0 || len(targetSessions) != 2 {
		t.Fatalf("partial SQL facts revoked/auth/audit/outbox = %d/%d/%d/%d", revoked, authEvents, managementAudits, outboxRows)
	}
}

func TestA08RolePermissionRealConcurrentPromoteSerializesOneTransition(t *testing.T) {
	f := openRealActionFixture(t, 1_920_020_000_000)
	intent := actionsecurity.PromoteIntent{TargetGUID: f.targetRow.Guid, ExpectedAuthVersion: f.targetRow.AuthVersion, ExpectedPermissionsVersion: 0,
		CatalogVersion: 1, Reason: "concurrent promote"}
	start := make(chan struct{})
	var succeeded, conflicted atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			outcome, err := executeA08RealTransition(f, actionsecurity.ActionUsersPromote, intent)
			if err == nil && outcome.Failure == nil {
				succeeded.Add(1)
			} else if err == nil && outcome.Failure != nil && *outcome.Failure == models.FailureTargetStateConflict {
				conflicted.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	var head models.PermissionPolicyHead
	if err := f.db.Where("user_id = ? AND is_deleted = 0", f.targetRow.ID).First(&head).Error; err != nil {
		t.Fatal(err)
	}
	if succeeded.Load() != 1 || conflicted.Load() != 1 || head.PolicyVersion != 1 {
		t.Fatalf("concurrent result success/conflict/head = %d/%d/%d", succeeded.Load(), conflicted.Load(), head.PolicyVersion)
	}
}

func TestA08RolePermissionRealDemoteAndRepromoteNeverRevivesHistory(t *testing.T) {
	f := openRealActionFixture(t, 1_920_030_000_000)
	promote := actionsecurity.PromoteIntent{TargetGUID: f.targetRow.Guid, ExpectedAuthVersion: f.targetRow.AuthVersion, ExpectedPermissionsVersion: 0,
		CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 3}}, Reason: "initial promote"}
	if outcome, err := executeA08RealTransition(f, actionsecurity.ActionUsersPromote, promote); err != nil || outcome.Failure != nil {
		t.Fatalf("initial promote = %#v/%v", outcome, err)
	}
	demote := actionsecurity.DemoteIntent{TargetGUID: f.targetRow.Guid, ExpectedAuthVersion: f.targetRow.AuthVersion + 1, ExpectedPermissionsVersion: 1, CatalogVersion: 1, Reason: "demote"}
	if outcome, err := executeA08RealTransition(f, actionsecurity.ActionUsersDemote, demote); err != nil || outcome.Failure != nil {
		t.Fatalf("demote = %#v/%v", outcome, err)
	}
	demotedSnapshot, err := LoadPermissionSnapshot(context.Background(), f.db, f.targetRow.ID)
	if err != nil || demotedSnapshot == nil || demotedSnapshot.PolicyVersion() != 2 || len(demotedSnapshot.Evaluator().CapabilityNames()) != 0 {
		t.Fatalf("demoted effective policy = %#v/%v", demotedSnapshot, err)
	}
	repromote := actionsecurity.PromoteIntent{TargetGUID: f.targetRow.Guid, ExpectedAuthVersion: f.targetRow.AuthVersion + 2, ExpectedPermissionsVersion: 2,
		CatalogVersion: 1, Reason: "baseline repromote"}
	if outcome, err := executeA08RealTransition(f, actionsecurity.ActionUsersPromote, repromote); err != nil || outcome.Failure != nil {
		t.Fatalf("repromote = %#v/%v", outcome, err)
	}
	var head models.PermissionPolicyHead
	var active, history int64
	if err := f.db.Where("user_id = ? AND is_deleted = 0", f.targetRow.ID).First(&head).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.PermissionOverride{}).Where("user_id = ? AND is_deleted = 0", f.targetRow.ID).Count(&active).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.PermissionOverride{}).Where("user_id = ? AND is_deleted = 1", f.targetRow.ID).Count(&history).Error; err != nil {
		t.Fatal(err)
	}
	if head.PolicyVersion != 3 || head.RuleCount != 0 || active != 0 || history != 1 {
		t.Fatalf("history head/active/deleted = %#v/%d/%d", head, active, history)
	}
	repromotedSnapshot, err := LoadPermissionSnapshot(context.Background(), f.db, f.targetRow.ID)
	if err != nil || repromotedSnapshot == nil || repromotedSnapshot.PolicyVersion() != 3 || repromotedSnapshot.Evaluator().Collection("users.read", false) != authz.Allowed {
		t.Fatalf("repromoted baseline policy = %#v/%v", repromotedSnapshot, err)
	}
}

func executeA08RealTransition(f *realActionFixture, action actionsecurity.Action, intent any) (TerminalOutcome, error) {
	base, err := NewRolePermissionExecution(rolePermissionTestDescriptorNoTest(action), intent, persistence.NextGUID, f.clock, f.crypto)
	if err != nil {
		return TerminalOutcome{}, err
	}
	execution, err := newRolePermissionTransactionalExecution(base, f.authRedis)
	if err != nil {
		return TerminalOutcome{}, err
	}
	actorID, verificationID, leaseUntil := f.actorRow.ID, testSnowflake.Next(), f.clock.NowMillis()+60_000
	lease := strings.Repeat("b", 64)
	operation := models.AdminOperation{ID: testSnowflake.Next(), AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: f.clock.NowMillis(), CreatedBy: &actorID,
		UpdatedAt: f.clock.NowMillis(), UpdatedBy: &actorID}, ActorUserID: actorID, ActorAuthVersion: f.actorRow.AuthVersion, SessionID: f.sessionRow.ID,
		Action: int(action), VerificationID: &verificationID, State: models.OperationProcessing, PublicRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		RequestHMAC: base.requestHMAC, LeaseOwnerHMAC: &lease, LeaseExpiresAt: &leaseUntil, QueryExpiresAt: leaseUntil + 60_000}
	targetGUID := base.targetGUID()
	verification := models.AdminActionVerification{ID: verificationID, ActorUserID: actorID, ActorAuthVersion: f.actorRow.AuthVersion, SessionID: f.sessionRow.ID,
		Action: int(action), TargetKind: int(actionsecurity.TargetUser), TargetGUID: &targetGUID, IntentHMAC: base.requestHMAC, ExpiresAt: leaseUntil}
	var outcome TerminalOutcome
	err = f.db.Transaction(func(tx *gorm.DB) error {
		if _, lockErr := execution.prelockForAuthorization(context.Background(), tx, operation, verification); lockErr != nil {
			return lockErr
		}
		var executeErr error
		outcome, executeErr = execution.Execute(context.Background(), tx, operation)
		return executeErr
	})
	return outcome, err
}

func rolePermissionTestDescriptorNoTest(action actionsecurity.Action) actionsecurity.Descriptor {
	for _, descriptor := range actionsecurity.ActiveActionRegistry() {
		if descriptor.Action == action {
			return descriptor
		}
	}
	return actionsecurity.Descriptor{}
}

func prepareA08RealPromote(t *testing.T, now int64) (*realActionFixture, *OperationIdentity, *rolePermissionTransactionalExecution, *RolePermissionOutboxWriter, []models.Session) {
	t.Helper()
	f := openRealActionFixture(t, now)
	active := rolePermissionTestDescriptor(t, actionsecurity.ActionUsersPromote)
	resolver := func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		if action == active.Action {
			return active, true
		}
		return actionsecurity.Descriptor{}, false
	}
	verification, err := newActionVerificationService(f.db, f.limiter, f.authRedis, f.crypto, resolver, f.clock, cryptorand.Reader, persistence.NextGUID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := newActionOperationService(f.db, f.limiter, f.authRedis, f.crypto, resolver, f.clock, cryptorand.Reader, persistence.NextGUID)
	if err != nil {
		t.Fatal(err)
	}
	f.verification, f.operation = verification, operation
	targetSessions := make([]models.Session, 2)
	for i := range targetSessions {
		sid, sidErr := security.NewSessionSID()
		if sidErr != nil {
			t.Fatal(sidErr)
		}
		targetSessions[i] = models.Session{AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now}, SID: sid,
			UserID: f.targetRow.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: i + 1, RefreshHMAC: string(make([]byte, 64)), LastActiveAt: now, ExpiresAt: now + int64(i+1)*60_000}
		if err := f.db.Create(&targetSessions[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	intent := actionsecurity.PromoteIntent{TargetGUID: f.targetRow.Guid, ExpectedAuthVersion: f.targetRow.AuthVersion, ExpectedPermissionsVersion: 0,
		CatalogVersion: models.PermissionCatalogVersion, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}}, Reason: "approved fixture promotion"}
	targetGUID := f.targetRow.Guid
	issued, err := f.verification.Issue(context.Background(), VerificationIssue{Action: active.Action, Actor: f.actor, TargetGUID: &targetGUID,
		Intent: intent, CurrentPassword: []byte(f.password), TrustedIP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	key := newRealIdempotencyKey(t)
	identity, view, err := f.operation.Begin(context.Background(), OperationBegin{Action: active.Action, Actor: f.actor,
		IdempotencyKeyValues: []string{key}, TicketValues: []string{issued.Ticket}, Intent: intent})
	if err != nil || identity == nil || view == nil || view.Status != "processing" {
		t.Fatalf("Begin = %#v/%#v/%v", identity, view, err)
	}
	base, err := NewRolePermissionExecution(active, intent, persistence.NextGUID, f.clock, f.crypto)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := newRolePermissionTransactionalExecution(base, f.authRedis)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := NewRolePermissionOutboxWriter(persistence.NextGUID, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	return f, identity, execution, outbox, targetSessions
}
