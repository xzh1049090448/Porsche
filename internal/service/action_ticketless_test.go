package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func ticketlessCreateDescriptor(t *testing.T) actionsecurity.Descriptor {
	t.Helper()
	for _, descriptor := range actionsecurity.FutureActionDescriptors() {
		if descriptor.Action == actionsecurity.ActionUsersCreate {
			descriptor.Active = true
			return descriptor
		}
	}
	t.Fatal("users.create descriptor missing")
	return actionsecurity.Descriptor{}
}

func adminCreateDescriptor(t *testing.T) actionsecurity.Descriptor {
	t.Helper()
	for _, descriptor := range actionsecurity.FutureActionDescriptors() {
		if descriptor.Action == actionsecurity.ActionUsersCreateAdmin {
			descriptor.Active = true
			return descriptor
		}
	}
	t.Fatal("users.create_admin descriptor missing")
	return actionsecurity.Descriptor{}
}

func ticketlessCreateIntent(username string) actionsecurity.CreateAccountIntent {
	return actionsecurity.CreateAccountIntent{
		Username: username, Password: []byte("Task4-test-password"), Role: "user", PlanType: int(models.PlanFree), DailyCallLimit: 100,
	}
}

func adminCreateIntent(username string) actionsecurity.CreateAccountIntent {
	return actionsecurity.CreateAccountIntent{
		Username: username, Password: []byte("Task4-admin-password"), Role: "admin", PlanType: int(models.PlanFree), DailyCallLimit: 100,
	}
}

func ticketlessOperationFixture(t *testing.T, now int64, existing *models.AdminOperation) (*ActionOperationService, *actionOperationScript, ActionActor, string) {
	t.Helper()
	service, script, actor, key, _ := actionOperationFixture(t, now, existing)
	descriptor := ticketlessCreateDescriptor(t)
	service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		if action != descriptor.Action {
			return actionsecurity.Descriptor{}, false
		}
		return descriptor, true
	}
	script.action = descriptor.Action
	script.ticketless = true
	script.rejectVerificationQueries = true
	script.target = nil
	script.verification = nil
	if existing != nil {
		existing.Action = int(descriptor.Action)
		existing.VerificationID = nil
		encoded, err := descriptor.Encode(ticketlessCreateIntent("alice"))
		if err != nil {
			t.Fatal(err)
		}
		digest := service.crypto.IntentDigest(encoded)
		clear(encoded)
		existing.RequestHMAC = hex.EncodeToString(digest[:])
		clear(digest[:])
	}
	return service, script, actor, key
}

func assertNoVerificationAccess(t *testing.T, queries, execs []string) {
	t.Helper()
	for _, statement := range append(append([]string(nil), queries...), execs...) {
		if strings.Contains(statement, "admin_action_verifications") {
			t.Fatalf("ticketless flow accessed verification storage: %s", statement)
		}
	}
}

func assertNoZeroTicketDigest(t *testing.T, service *ActionOperationService, script *actionOperationScript) {
	t.Helper()
	zero := [32]byte{}
	digest := service.crypto.TicketDigest(zero)
	digestHex := hex.EncodeToString(digest[:])
	clear(digest[:])
	if strings.Contains(actionOperationSQLValues(script), digestHex) {
		t.Fatal("ticketless flow retained a fabricated zero-ticket digest")
	}
}

func TestActionOperationTicketlessDescriptorAndTicketHeaderBoundary(t *testing.T) {
	descriptor := ticketlessCreateDescriptor(t)
	if !validOperationDescriptor(descriptor, descriptor.Action, true) {
		t.Fatal("structurally valid active ticketless descriptor was rejected")
	}

	now := int64(1_800_000_000_000)
	service, script, actor, key := ticketlessOperationFixture(t, now, nil)
	client := service.limiter.client.(*actionIssueRedisClient)
	for _, ticketValues := range [][]string{{"av_" + strings.Repeat("A", 43)}, {"one", "two"}} {
		identity, view, err := service.Begin(context.Background(), OperationBegin{
			Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: ticketValues, Intent: ticketlessCreateIntent("alice"),
		})
		if identity != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) {
			t.Fatalf("ticketless descriptor accepted ticket headers %q: identity=%v view=%v err=%v", ticketValues, identity, view, err)
		}
	}
	if client.evalCalls != 0 || script.beginCount != 0 {
		t.Fatalf("forbidden ticket headers reached Redis/MySQL: %d/%d", client.evalCalls, script.beginCount)
	}

	for _, tc := range []struct {
		name   string
		values []string
	}{
		{name: "absent", values: nil},
		{name: "multiple", values: []string{"one", "two"}},
		{name: "malformed", values: []string{"bad"}},
	} {
		t.Run("ticket-required-"+tc.name, func(t *testing.T) {
			ticketed, ticketedScript, ticketedActor, ticketedKey, _ := actionOperationFixture(t, now, nil)
			identity, view, err := ticketed.Begin(context.Background(), OperationBegin{
				Action: testNoopAction, Actor: ticketedActor, IdempotencyKeyValues: []string{ticketedKey}, TicketValues: tc.values,
				Intent: testNoopIntent(testNoopTargetGUID, "same-intent"),
			})
			if identity != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) || ticketedScript.beginCount != 0 {
				t.Fatalf("ticket-required %s boundary = %v %v %v begins=%d", tc.name, identity, view, err, ticketedScript.beginCount)
			}
		})
	}

	t.Run("ticket-required-binding-is-exact", func(t *testing.T) {
		ticketed, ticketedScript, ticketedActor, ticketedKey, ticket := actionOperationFixture(t, now, nil)
		ticketedScript.verification.IntentHMAC = strings.Repeat("f", 64)
		identity, view, err := ticketed.Begin(context.Background(), OperationBegin{
			Action: testNoopAction, Actor: ticketedActor, IdempotencyKeyValues: []string{ticketedKey}, TicketValues: []string{ticket},
			Intent: testNoopIntent(testNoopTargetGUID, "same-intent"),
		})
		if identity != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) || ticketedScript.rollbackCount != 1 {
			t.Fatalf("same-length binding mismatch = %v %v %v rollbacks=%d", identity, view, err, ticketedScript.rollbackCount)
		}
	})
}

func TestActionOperationTicketlessBeginReplayConflictAndCrossSession(t *testing.T) {
	now := int64(1_800_000_000_000)
	service, script, actor, key := ticketlessOperationFixture(t, now, nil)
	descriptor := ticketlessCreateDescriptor(t)
	firstIntent := ticketlessCreateIntent("alice")
	identity, view, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: firstIntent,
	})
	if err != nil || identity == nil || view == nil || view.Status != "processing" || !identity.ReadyForExecution() {
		t.Fatalf("fresh ticketless Begin = %#v %#v %v", identity, view, err)
	}
	if script.operation == nil || script.operation.VerificationID != nil {
		t.Fatalf("ticketless operation verification FK = %#v", script.operation)
	}
	if !bytes.Equal(firstIntent.Password, make([]byte, len(firstIntent.Password))) {
		t.Fatal("ticketless intent password was not cleared after HMAC encoding")
	}
	assertNoVerificationAccess(t, script.queries, script.execs)
	assertNoZeroTicketDigest(t, service, script)

	replayed, replayView, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("alice"),
	})
	if err != nil || replayed == nil || replayView == nil || replayed.ID != identity.ID || replayed.ReadyForExecution() {
		t.Fatalf("ticketless replay = %#v %#v %v", replayed, replayView, err)
	}
	conflict, conflictView, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("bob"),
	})
	if conflict != nil || conflictView != nil || !errors.Is(err, ErrActionOperationConflict) {
		t.Fatalf("ticketless request conflict = %#v %#v %v", conflict, conflictView, err)
	}

	secondSID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	second := models.Session{ID: 21, AuditFields: models.AuditFields{Guid: 2002}, SID: secondSID, UserID: script.actor.ID, SessionVersion: 1, ExpiresAt: now + actionOperationQueryRetentionMS + 1}
	script.sessions = append(script.sessions, second)
	otherSession := actor
	otherSession.SessionSID = secondSID
	otherSession.SessionVersion = second.SessionVersion
	cross, crossView, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: otherSession, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("alice"),
	})
	if cross != nil || crossView != nil || !errors.Is(err, ErrActionOperationCrossSession) {
		t.Fatalf("ticketless cross-session reuse = %#v %#v %v", cross, crossView, err)
	}
	script.actor.AuthVersion++
	if stale, staleView, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("alice"),
	}); stale != nil || staleView != nil || !errors.Is(err, ErrActionOperationForbidden) {
		t.Fatalf("ticketless replay accepted changed auth version = %#v %#v %v", stale, staleView, err)
	}
	script.actor.AuthVersion = actor.AuthVersion
	client := service.authRedis.client.(*actionIssueRedisClient)
	client.revoked = true
	if revoked, revokedView, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("alice"),
	}); revoked != nil || revokedView != nil || !errors.Is(err, ErrActionOperationForbidden) {
		t.Fatalf("ticketless replay accepted revoked session = %#v %#v %v", revoked, revokedView, err)
	}
	assertNoVerificationAccess(t, script.queries, script.execs)
}

func TestActionOperationTicketlessBeginDistinguishesNoopUpdateFromCASMiss(t *testing.T) {
	now := int64(1_800_000_000_000)
	t.Run("exact locked row", func(t *testing.T) {
		service, script, actor, key := ticketlessOperationFixture(t, now, nil)
		script.zeroAffectedAt = 2
		identity, view, err := service.Begin(context.Background(), OperationBegin{
			Action: actionsecurity.ActionUsersCreate, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("alice"),
		})
		if err != nil || identity == nil || view == nil || !identity.ReadyForExecution() || script.commitCount != 1 || script.rollbackCount != 0 {
			t.Fatalf("confirmed no-op = %#v %#v %v commits=%d rollbacks=%d", identity, view, err, script.commitCount, script.rollbackCount)
		}
		if len(script.execs) != 2 || len(script.queries) == 0 || !strings.Contains(script.queries[len(script.queries)-1], "FROM `admin_operations`") || !strings.Contains(script.queries[len(script.queries)-1], "FOR UPDATE") {
			t.Fatalf("confirmed no-op writes/queries = %v/%v", script.execs, script.queries)
		}
		assertNoVerificationAccess(t, script.queries, script.execs)
	})

	t.Run("missing locked row", func(t *testing.T) {
		service, script, actor, key := ticketlessOperationFixture(t, now, nil)
		script.zeroAffectedAt = 2
		script.hidePendingOperation = true
		identity, view, err := service.Begin(context.Background(), OperationBegin{
			Action: actionsecurity.ActionUsersCreate, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("alice"),
		})
		if identity != nil || view != nil || !errors.Is(err, ErrActionOperationUnavailable) || script.commitCount != 0 || script.rollbackCount != 1 {
			t.Fatalf("CAS miss = %#v %#v %v commits=%d rollbacks=%d", identity, view, err, script.commitCount, script.rollbackCount)
		}
		assertNoVerificationAccess(t, script.queries, script.execs)
	})

	t.Run("mismatched locked row", func(t *testing.T) {
		service, script, actor, key := ticketlessOperationFixture(t, now, nil)
		script.zeroAffectedAt = 2
		script.corruptPendingAfterNoop = true
		identity, view, err := service.Begin(context.Background(), OperationBegin{
			Action: actionsecurity.ActionUsersCreate, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("alice"),
		})
		if identity != nil || view != nil || !errors.Is(err, ErrActionOperationUnavailable) || script.commitCount != 0 || script.rollbackCount != 1 {
			t.Fatalf("CAS mismatch = %#v %#v %v commits=%d rollbacks=%d", identity, view, err, script.commitCount, script.rollbackCount)
		}
		assertNoVerificationAccess(t, script.queries, script.execs)
	})
}

func TestActionOperationTicketlessAdminUserCreateBeginReplayAndQuery(t *testing.T) {
	now := int64(1_800_000_000_000)
	service, script, actor, key := ticketlessOperationFixture(t, now, nil)
	script.actor.Role = models.UserRoleAdmin
	descriptor := ticketlessCreateDescriptor(t)
	identity, view, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("admin-created-user"),
	})
	if err != nil || identity == nil || view == nil || view.Status != "processing" || !identity.ReadyForExecution() {
		t.Fatalf("Admin ticketless Begin = %#v %#v %v", identity, view, err)
	}
	replay, replayView, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("admin-created-user"),
	})
	if err != nil || replay == nil || replayView == nil || replay.ID != identity.ID || replay.ReadyForExecution() {
		t.Fatalf("Admin ticketless replay = %#v %#v %v", replay, replayView, err)
	}
	if queried, err := service.Query(context.Background(), descriptor.Action, actor, []string{key}); err != nil || queried == nil || queried.Status != "processing" {
		t.Fatalf("Admin ticketless Query = %#v %v", queried, err)
	}
	assertNoVerificationAccess(t, script.queries, script.execs)
}

func TestActionOperationTicketlessAdminCreateCapabilityRevocationBlocksProcessingAndRecovery(t *testing.T) {
	now := int64(1_800_000_000_000)
	for _, state := range []models.AdminOperationState{models.OperationProcessing, models.OperationPendingRecovery} {
		t.Run(state.String(), func(t *testing.T) {
			operation := models.AdminOperation{ID: 30, SessionID: 20, State: state, QueryExpiresAt: now + actionOperationQueryRetentionMS}
			service, script, actor, key := ticketlessOperationFixture(t, now, &operation)
			script.actor.Role = models.UserRoleAdmin
			if view, err := service.Query(context.Background(), actionsecurity.ActionUsersCreate, actor, []string{key}); err != nil || view == nil || view.Status != state.String() {
				t.Fatalf("pre-revocation Query = %#v %v", view, err)
			}
			script.policyHead = &models.PermissionPolicyHead{ID: 51, AuditFields: models.AuditFields{Guid: 5101}, UserID: script.actor.ID, PolicyVersion: 2, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
			script.overrides = []models.PermissionOverride{{ID: 52, AuditFields: models.AuditFields{Guid: 5201}, UserID: script.actor.ID, PolicyVersion: 2, Capability: 2, Effect: 3}}
			if identity, view, err := service.Begin(context.Background(), OperationBegin{
				Action: actionsecurity.ActionUsersCreate, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("alice"),
			}); identity != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) {
				t.Fatalf("revoked %s Begin = %#v %#v %v", state, identity, view, err)
			}
			if view, err := service.Query(context.Background(), actionsecurity.ActionUsersCreate, actor, []string{key}); view != nil || !errors.Is(err, ErrActionOperationHidden) {
				t.Fatalf("revoked %s Query = %#v %v", state, view, err)
			}
			assertNoVerificationAccess(t, script.queries, script.execs)
		})
	}
}

func TestActionOperationTicketlessUnknownCreateDescriptorCombinationFailsClosed(t *testing.T) {
	now := int64(1_800_000_000_000)
	service, script, actor, key := ticketlessOperationFixture(t, now, nil)
	descriptor := ticketlessCreateDescriptor(t)
	descriptor.Name = "users.create_admin"
	service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		return descriptor, action == descriptor.Action
	}
	identity, view, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: ticketlessCreateIntent("alice"),
	})
	if identity != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) || script.rollbackCount != 1 {
		t.Fatalf("unknown create descriptor combination = %#v %#v %v rollbacks=%d", identity, view, err, script.rollbackCount)
	}
	assertNoVerificationAccess(t, script.queries, script.execs)
}

func TestActionOperationTicketlessChangeRetainsRootAdminCreateTicketedAuthorization(t *testing.T) {
	now := int64(1_800_000_000_000)
	service, script, actor, key, ticket := actionOperationFixture(t, now, nil)
	descriptor := adminCreateDescriptor(t)
	service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		return descriptor, action == descriptor.Action
	}
	script.action = descriptor.Action
	script.target = nil
	script.verification.Action = int(descriptor.Action)
	script.verification.TargetKind = int(actionsecurity.TargetNone)
	script.verification.TargetGUID = nil
	encoded, err := descriptor.Encode(adminCreateIntent("new-admin"))
	if err != nil {
		t.Fatal(err)
	}
	digest := service.crypto.IntentDigest(encoded)
	clear(encoded)
	script.verification.IntentHMAC = hex.EncodeToString(digest[:])
	clear(digest[:])
	identity, view, err := service.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: adminCreateIntent("new-admin"),
	})
	if err != nil || identity == nil || view == nil || !identity.ReadyForExecution() || script.operation.VerificationID == nil {
		t.Fatalf("Root admin-create ticketed Begin = %#v %#v %v", identity, view, err)
	}
}

func TestActionOperationTicketlessQueryExpiryAuthorizationAndRecovery(t *testing.T) {
	now := int64(1_800_000_000_000)
	processing := models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationProcessing, QueryExpiresAt: now + actionOperationQueryRetentionMS}
	service, script, actor, key := ticketlessOperationFixture(t, now, &processing)
	view, err := service.Query(context.Background(), actionsecurity.ActionUsersCreate, actor, []string{key})
	if err != nil || view == nil || view.Status != "processing" {
		t.Fatalf("ticketless processing Query = %#v %v", view, err)
	}
	assertNoVerificationAccess(t, script.queries, script.execs)

	script.actor.Role = models.UserRoleUser
	if hidden, err := service.Query(context.Background(), actionsecurity.ActionUsersCreate, actor, []string{key}); hidden != nil || !errors.Is(err, ErrActionOperationHidden) {
		t.Fatalf("ticketless Query retained stale authorization: %#v %v", hidden, err)
	}
	script.actor.Role = models.UserRoleRoot
	script.actor.AuthVersion++
	if hidden, err := service.Query(context.Background(), actionsecurity.ActionUsersCreate, actor, []string{key}); hidden != nil || !errors.Is(err, ErrActionOperationHidden) {
		t.Fatalf("ticketless Query accepted changed auth version: %#v %v", hidden, err)
	}
	script.actor.AuthVersion = actor.AuthVersion
	client := service.authRedis.client.(*actionIssueRedisClient)
	client.revoked = true
	if hidden, err := service.Query(context.Background(), actionsecurity.ActionUsersCreate, actor, []string{key}); hidden != nil || !errors.Is(err, ErrActionOperationHidden) {
		t.Fatalf("ticketless Query accepted revoked session: %#v %v", hidden, err)
	}
	client.revoked = false
	script.sessions[0].ExpiresAt = now
	if hidden, err := service.Query(context.Background(), actionsecurity.ActionUsersCreate, actor, []string{key}); hidden != nil || !errors.Is(err, ErrActionOperationHidden) {
		t.Fatalf("ticketless Query accepted expired session: %#v %v", hidden, err)
	}

	finishedBeforeRetention := now - 1
	terminalCurrent := models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationSucceeded, FinishedAt: &finishedBeforeRetention, QueryExpiresAt: now + actionOperationQueryRetentionMS}
	terminalService, terminalScript, terminalActor, terminalKey := ticketlessOperationFixture(t, now, &terminalCurrent)
	terminalIdentity, terminalView, err := terminalService.Begin(context.Background(), OperationBegin{
		Action: actionsecurity.ActionUsersCreate, Actor: terminalActor, IdempotencyKeyValues: []string{terminalKey}, Intent: ticketlessCreateIntent("alice"),
	})
	if err != nil || terminalIdentity == nil || terminalView == nil || terminalView.Status != "succeeded" || terminalIdentity.ReadyForExecution() {
		t.Fatalf("ticketless terminal Begin replay = %#v %#v %v", terminalIdentity, terminalView, err)
	}
	if queried, err := terminalService.Query(context.Background(), actionsecurity.ActionUsersCreate, terminalActor, []string{terminalKey}); err != nil || queried == nil || queried.Status != "succeeded" {
		t.Fatalf("ticketless terminal Query replay = %#v %v", queried, err)
	}
	assertNoVerificationAccess(t, terminalScript.queries, terminalScript.execs)

	finished := now - actionOperationQueryRetentionMS
	terminal := models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationSucceeded, FinishedAt: &finished, QueryExpiresAt: now}
	expiryService, expiryScript, expiryActor, expiryKey := ticketlessOperationFixture(t, now, &terminal)
	if expired, err := expiryService.Query(context.Background(), actionsecurity.ActionUsersCreate, expiryActor, []string{expiryKey}); expired != nil || !errors.Is(err, ErrActionOperationExpired) {
		t.Fatalf("ticketless expiry = %#v %v", expired, err)
	}
	if expiryScript.operation.State != models.OperationExpired || expiryScript.operation.IsDeleted != 1 {
		t.Fatalf("ticketless expiry was not tombstoned: %#v", expiryScript.operation)
	}
	assertNoVerificationAccess(t, expiryScript.queries, expiryScript.execs)

	recoveryNow := now + actionOperationRecoveryGraceMS + 2
	lease := recoveryNow - actionOperationRecoveryGraceMS - 1
	recovering := models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationProcessing, LeaseExpiresAt: &lease, QueryExpiresAt: recoveryNow + 1}
	recoveryService, recoveryScript, _, _ := ticketlessOperationFixture(t, recoveryNow, &recovering)
	if err := recoveryService.MarkPendingRecovery(context.Background(), recovering.ID); err != nil {
		t.Fatalf("ticketless recovery = %v", err)
	}
	if recoveryScript.operation.State != models.OperationPendingRecovery || recoveryScript.operation.VerificationID != nil {
		t.Fatalf("ticketless recovery state = %#v", recoveryScript.operation)
	}
	assertNoVerificationAccess(t, recoveryScript.queries, recoveryScript.execs)
}

func TestActionExecuteTicketlessCompletesWithoutVerificationAccessOrConsumption(t *testing.T) {
	service, script, identity := actionExecuteFixture(t)
	descriptor := ticketlessCreateDescriptor(t)
	script.ticketless = true
	script.state.operation.Action = int(descriptor.Action)
	script.state.operation.VerificationID = nil
	service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		return descriptor, action == descriptor.Action
	}
	consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
	view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
	if err != nil || view == nil || view.Status != "succeeded" || consumer.calls != 1 {
		t.Fatalf("ticketless Execute = %#v %v calls=%d validation=%s", view, err, consumer.calls, script.lastError)
	}
	if script.state.operation.VerificationID != nil || script.state.verification.ConsumedAt != nil || script.state.verification.IsDeleted != 0 {
		t.Fatalf("ticketless Execute mutated verification state: operation=%#v verification=%#v", script.state.operation, script.state.verification)
	}
	assertNoVerificationAccess(t, script.queries, script.execs)
}

func TestActionExecuteTicketlessAdminUserCreateSucceedsAndFreshDenyRejects(t *testing.T) {
	t.Run("Admin default allow", func(t *testing.T) {
		service, script, identity := actionExecuteFixture(t)
		descriptor := ticketlessCreateDescriptor(t)
		script.ticketless = true
		script.actor.Role = models.UserRoleAdmin
		script.state.operation.Action = int(descriptor.Action)
		script.state.operation.VerificationID = nil
		service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
			return descriptor, action == descriptor.Action
		}
		consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
		if view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); err != nil || view == nil || view.Status != "succeeded" || consumer.calls != 1 {
			t.Fatalf("Admin ticketless Execute = %#v %v calls=%d", view, err, consumer.calls)
		}
		assertNoVerificationAccess(t, script.queries, script.execs)
	})

	t.Run("Admin explicit deny", func(t *testing.T) {
		service, script, identity := actionExecuteFixture(t)
		descriptor := ticketlessCreateDescriptor(t)
		script.ticketless = true
		script.actor.Role = models.UserRoleAdmin
		script.policy.RuleCount = 1
		script.overrides = []models.PermissionOverride{{ID: 51, AuditFields: models.AuditFields{Guid: 5101}, UserID: script.actor.ID, PolicyVersion: script.policy.PolicyVersion, Capability: 2, Effect: 3}}
		script.state.operation.Action = int(descriptor.Action)
		script.state.operation.VerificationID = nil
		service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
			return descriptor, action == descriptor.Action
		}
		consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
		if view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); view != nil || !errors.Is(err, ErrActionOperationForbidden) || consumer.calls != 0 {
			t.Fatalf("Admin denied ticketless Execute = %#v %v calls=%d", view, err, consumer.calls)
		}
		assertNoVerificationAccess(t, script.queries, script.execs)
	})
}

func TestActionExecuteTicketlessFreshAuthorizationAndTicketedOneShotRemainEnforced(t *testing.T) {
	t.Run("ticketless-fresh-authorization", func(t *testing.T) {
		service, script, identity := actionExecuteFixture(t)
		descriptor := ticketlessCreateDescriptor(t)
		script.ticketless = true
		script.actor.Role = models.UserRoleUser
		script.state.operation.Action = int(descriptor.Action)
		script.state.operation.VerificationID = nil
		service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
			return descriptor, action == descriptor.Action
		}
		consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
		if view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); view != nil || !errors.Is(err, ErrActionOperationForbidden) || consumer.calls != 0 {
			t.Fatalf("ticketless stale authorization = %#v %v calls=%d", view, err, consumer.calls)
		}
		assertNoVerificationAccess(t, script.queries, script.execs)
	})

	t.Run("ticketed-consumption-remains-one-shot", func(t *testing.T) {
		service, script, identity := actionExecuteFixture(t)
		consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
		if view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); err != nil || view == nil {
			t.Fatalf("ticketed Execute = %#v %v", view, err)
		}
		consumeCount := 0
		for _, statement := range script.execs {
			if strings.Contains(statement, "UPDATE `admin_action_verifications`") {
				consumeCount++
			}
		}
		if consumeCount != 1 || script.state.verification.ConsumedAt == nil || script.state.verification.IsDeleted != 1 {
			t.Fatalf("ticketed consumption count/state = %d/%#v", consumeCount, script.state.verification)
		}
		if replay, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); replay != nil || !errors.Is(err, ErrActionOperationUnavailable) || consumer.calls != 1 {
			t.Fatalf("ticketed one-shot replay = %#v %v calls=%d", replay, err, consumer.calls)
		}
	})
}

func TestActionOperationTicketlessRealMySQLRedisLifecycle(t *testing.T) {
	now := int64(1_800_300_000_000)
	fixture := openRealActionFixture(t, now)
	setupRealActionPrimitiveTables(t, fixture.db)
	descriptor := ticketlessCreateDescriptor(t)
	fixture.operation.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		return descriptor, action == descriptor.Action
	}
	key := newRealIdempotencyKey(t)
	intent := ticketlessCreateIntent("ticketless-real")
	identity, beginView, err := fixture.operation.Begin(context.Background(), OperationBegin{
		Action: descriptor.Action, Actor: fixture.actor, IdempotencyKeyValues: []string{key}, Intent: intent,
	})
	if err != nil || identity == nil || beginView == nil || !identity.ReadyForExecution() {
		t.Fatalf("real ticketless Begin = %#v %#v %v", identity, beginView, err)
	}
	var stored models.AdminOperation
	if err := fixture.db.First(&stored, identity.ID).Error; err != nil || stored.VerificationID != nil {
		t.Fatalf("real ticketless operation = %#v %v", stored, err)
	}
	var verificationCount int64
	if err := fixture.db.Model(&models.AdminActionVerification{}).Where("actor_user_id = ? AND action = ?", fixture.actor.UserID, int(descriptor.Action)).Count(&verificationCount).Error; err != nil || verificationCount != 0 {
		t.Fatalf("real ticketless verification count = %d %v", verificationCount, err)
	}
	if queried, err := fixture.operation.Query(context.Background(), descriptor.Action, fixture.actor, []string{key}); err != nil || queried == nil || queried.Status != "processing" {
		t.Fatalf("real ticketless Query = %#v %v", queried, err)
	}
	consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
	if executed, err := fixture.operation.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); err != nil || executed == nil || executed.Status != "succeeded" {
		t.Fatalf("real ticketless Execute = %#v %v", executed, err)
	}
	if terminal, err := fixture.operation.Query(context.Background(), descriptor.Action, fixture.actor, []string{key}); err != nil || terminal == nil || terminal.Status != "succeeded" {
		t.Fatalf("real ticketless terminal Query = %#v %v", terminal, err)
	}
	if err := fixture.db.Model(&models.AdminActionVerification{}).Where("actor_user_id = ? AND action = ?", fixture.actor.UserID, int(descriptor.Action)).Count(&verificationCount).Error; err != nil || verificationCount != 0 {
		t.Fatalf("real ticketless verification changed = %d %v", verificationCount, err)
	}
}
