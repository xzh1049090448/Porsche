package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestActionOperationStateContractIsTyped(t *testing.T) {
	in := OperationBegin{Action: actionsecurity.Action(2147483000)}
	if in.Action == 0 {
		t.Fatal("operation begin lost its typed action")
	}
}

func actionOperationFixture(t *testing.T, now int64, existing *models.AdminOperation) (*ActionOperationService, *actionOperationScript, ActionActor, string, string) {
	t.Helper()
	root := bytes.Repeat([]byte{0x63}, 32)
	crypto, err := actionsecurity.NewCrypto(root)
	clear(root)
	if err != nil {
		t.Fatal(err)
	}
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	limiter, err := NewActionSecurityRedis(client, crypto)
	if err != nil {
		t.Fatal(err)
	}
	authRedis, err := NewAuthRedis(client, "task8-auth-hmac-key-material")
	if err != nil {
		t.Fatal(err)
	}
	password := "not-used-but-required"
	actorRow := models.User{ID: 10, AuditFields: models.AuditFields{Guid: 1001}, PasswordHash: &password, Role: models.UserRoleRoot, Status: models.UserStatusActive, AuthVersion: 7}
	sid := "11111111-2222-4333-8444-555555555555"
	session := models.Session{ID: 20, AuditFields: models.AuditFields{Guid: 2001}, SID: sid, UserID: actorRow.ID, SessionVersion: 3, ExpiresAt: now + actionOperationQueryRetentionMS + 1}
	actor := ActionActor{UserID: actorRow.ID, UserGUID: actorRow.Guid, AuthVersion: actorRow.AuthVersion, SessionSID: sid, SessionVersion: session.SessionVersion}
	keyRaw := [32]byte{}
	ticketRaw := [32]byte{}
	for i := range keyRaw {
		keyRaw[i] = byte(i + 1)
		ticketRaw[i] = byte(101 + i)
	}
	key := "ik_" + base64.RawURLEncoding.EncodeToString(keyRaw[:])
	ticket := "av_" + base64.RawURLEncoding.EncodeToString(ticketRaw[:])
	keyDigest := crypto.IdempotencyDigest(keyRaw)
	ticketDigest := crypto.TicketDigest(ticketRaw)
	requestDigest := crypto.IntentDigest([]byte("same-intent"))
	keyHex := hex.EncodeToString(keyDigest[:])
	ticketHex := hex.EncodeToString(ticketDigest[:])
	requestHex := hex.EncodeToString(requestDigest[:])
	clear(keyRaw[:])
	clear(ticketRaw[:])
	clear(keyDigest[:])
	clear(ticketDigest[:])
	clear(requestDigest[:])
	if existing != nil {
		existing.ActorUserID = actorRow.ID
		existing.ActorAuthVersion = actorRow.AuthVersion
		existing.Action = int(testNoopAction)
		existing.IdempotencyKeyHMAC = keyHex
		if existing.RequestHMAC == "" {
			existing.RequestHMAC = requestHex
		}
		if existing.PublicRef == "" {
			existing.PublicRef = "op_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))
		}
		if existing.QueryExpiresAt == 0 {
			existing.QueryExpiresAt = now + actionOperationQueryRetentionMS
		}
	}
	verification := &models.AdminActionVerification{ID: 40, AuditFields: models.AuditFields{Guid: 4001}, ActorUserID: actorRow.ID, ActorAuthVersion: actorRow.AuthVersion, SessionID: session.ID, Action: int(testNoopAction), TargetKind: int(actionsecurity.TargetNone), IntentHMAC: requestHex, TicketHMAC: ticketHex, ExpiresAt: now + 300_000}
	script := &actionOperationScript{actor: actorRow, sessions: []models.Session{session}, operation: existing, verification: verification}
	random := bytes.NewReader(bytes.Repeat([]byte{0x71}, 64))
	service, err := newActionOperationService(openActionOperationScriptDB(t, script), limiter, authRedis, crypto, func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		if action == testNoopAction {
			return testNoopDescriptor(), true
		}
		return actionsecurity.Descriptor{}, false
	}, &actionIssueClock{now: now}, random, func() int64 { return 3001 })
	if err != nil {
		t.Fatal(err)
	}
	return service, script, actor, key, ticket
}

func TestActionOperationBeginStrictParsingPrecedesRedisAndMySQL(t *testing.T) {
	service, script, actor, _, ticket := actionOperationFixture(t, 1_800_000_000_000, nil)
	client := service.limiter.client.(*actionIssueRedisClient)
	result, view, err := service.Begin(context.Background(), OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{"bad"}, TicketValues: []string{ticket}, Intent: "same-intent"})
	if result != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) {
		t.Fatalf("invalid parse = %#v %#v %v", result, view, err)
	}
	if client.evalCalls != 0 || script.beginCount != 0 {
		t.Fatalf("invalid parse reached Redis/MySQL: %d/%d", client.evalCalls, script.beginCount)
	}
}

func TestActionOperationBeginLimiterPrecedesOperationLookupAndProductionRegistryIsEmpty(t *testing.T) {
	now := int64(1_800_000_000_000)
	service, script, actor, key, ticket := actionOperationFixture(t, now, nil)
	client := service.limiter.client.(*actionIssueRedisClient)
	client.actionRateEvalClient.err = errors.New("private redis failure")
	identity, view, err := service.Begin(context.Background(), OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: "same-intent"})
	if identity != nil || view != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("limiter failure = %#v %#v %v", identity, view, err)
	}
	if script.beginCount != 0 {
		t.Fatalf("limiter failure reached MySQL %d times", script.beginCount)
	}
	client.actionRateEvalClient.err = nil
	service.resolve = actionsecurity.ResolveActiveAction
	identity, view, err = service.Begin(context.Background(), OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: "same-intent"})
	if identity != nil || view != nil || !errors.Is(err, ErrActionOperationInactive) {
		t.Fatalf("production registry = %#v %#v %v", identity, view, err)
	}
	if client.evalCalls != 1 || script.beginCount != 0 {
		t.Fatalf("inactive action reached dependency: redis=%d mysql=%d", client.evalCalls, script.beginCount)
	}
}

func TestActionOperationBeginIdempotencyConflictsAndTombstone(t *testing.T) {
	now := int64(1_800_000_000_000)
	tests := []struct {
		name       string
		operation  models.AdminOperation
		mutate     func(*OperationBegin)
		want       error
		wantStatus string
	}{
		{name: "same request", operation: models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationProcessing}, wantStatus: "processing"},
		{name: "payload conflict", operation: models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationProcessing}, mutate: func(in *OperationBegin) { in.Intent = "other-intent" }, want: ErrActionOperationConflict},
		{name: "cross session", operation: models.AdminOperation{ID: 30, SessionID: 99, State: models.OperationProcessing}, want: ErrActionOperationCrossSession},
		{name: "tombstone", operation: models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationExpired, AuditFields: models.AuditFields{IsDeleted: 1}}, want: ErrActionOperationExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op := tc.operation
			service, script, actor, key, ticket := actionOperationFixture(t, now, &op)
			in := OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: "same-intent"}
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			identity, view, err := service.Begin(context.Background(), in)
			if tc.want != nil {
				if identity != nil || view != nil || !errors.Is(err, tc.want) {
					t.Fatalf("got %#v %#v %v, want %v", identity, view, err, tc.want)
				}
			} else if err != nil || identity == nil || view == nil || view.Status != tc.wantStatus {
				t.Fatalf("same request = %#v %#v %v", identity, view, err)
			}
			if stringsContainAny(script.queries, "admin_action_verifications") {
				t.Fatal("existing operation re-locked or consumed verification")
			}
		})
	}
}

func TestActionOperationBeginTicketExpiryBoundaryRollsBack(t *testing.T) {
	now := int64(1_800_000_000_000)
	service, script, actor, key, ticket := actionOperationFixture(t, now, nil)
	script.verification.ExpiresAt = now
	identity, view, err := service.Begin(context.Background(), OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: "same-intent"})
	if identity != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) {
		t.Fatalf("ticket expiry boundary = %#v %#v %v", identity, view, err)
	}
	if script.commitCount != 0 || script.rollbackCount != 1 {
		t.Fatalf("commit/rollback = %d/%d", script.commitCount, script.rollbackCount)
	}
}

func TestActionOperationBeginTicketMismatchAndReuseAreFixedForbidden(t *testing.T) {
	now := int64(1_800_000_000_000)
	for _, tc := range []struct {
		name   string
		mutate func(*actionOperationScript)
	}{
		{name: "ticket belongs to another session", mutate: func(script *actionOperationScript) { script.verification.SessionID = 99 }},
		{name: "same ticket reserved by another key", mutate: func(script *actionOperationScript) {
			script.failExecAt = 2
			script.execError = &mysqlDriver.MySQLError{Number: 1062, Message: "private duplicate"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, script, actor, key, ticket := actionOperationFixture(t, now, nil)
			tc.mutate(script)
			identity, view, err := service.Begin(context.Background(), OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: "same-intent"})
			if identity != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) || err.Error() != ErrActionOperationForbidden.Error() {
				t.Fatalf("fixed forbidden = %#v %#v %v", identity, view, err)
			}
			if script.commitCount != 0 || script.rollbackCount != 1 {
				t.Fatalf("commit/rollback = %d/%d", script.commitCount, script.rollbackCount)
			}
		})
	}
}

func TestActionOperationQueryHidesMismatchAndDoesNotConsumeBeginQuota(t *testing.T) {
	now := int64(1_800_000_000_000)
	op := models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationProcessing}
	service, _, actor, key, _ := actionOperationFixture(t, now, &op)
	client := service.limiter.client.(*actionIssueRedisClient)
	view, err := service.Query(context.Background(), testNoopAction, actor, []string{key})
	if err != nil || view == nil || view.Status != "processing" || view.RetryAfterSeconds != 1 {
		t.Fatalf("Query = %#v %v", view, err)
	}
	if client.evalCalls != 0 {
		t.Fatalf("Query consumed Begin quota: %d", client.evalCalls)
	}
	op.SessionID = 99
	view, err = service.Query(context.Background(), testNoopAction, actor, []string{key})
	if view != nil || !errors.Is(err, ErrActionOperationHidden) {
		t.Fatalf("other session = %#v %v", view, err)
	}
}

func TestActionOperationQueryProcessingLeaseEdgesRemainProcessing(t *testing.T) {
	base := int64(1_800_000_000_000)
	lease := base + actionOperationLeaseMillis
	for _, now := range []int64{lease, lease + actionOperationRecoveryGraceMS} {
		op := models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationProcessing, LeaseExpiresAt: &lease, QueryExpiresAt: base + actionOperationQueryRetentionMS}
		service, _, actor, key, _ := actionOperationFixture(t, now, &op)
		view, err := service.Query(context.Background(), testNoopAction, actor, []string{key})
		if err != nil || view == nil || view.Status != "processing" || view.RetryAfterSeconds != 1 {
			t.Fatalf("Query at %d = %#v %v", now, view, err)
		}
	}
}

func TestActionOperationExpiryBoundaryTombstones(t *testing.T) {
	now := int64(1_800_000_000_000)
	finished := now - actionOperationQueryRetentionMS
	failure := models.FailureActionRejected
	resultKind := models.ResultUser
	resultGUID := int64(777)
	status := 409
	op := models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationFailed, FinishedAt: &finished, QueryExpiresAt: now, ErrorCode: &failure, ResultKind: &resultKind, ResultGUID: &resultGUID, ResultHTTPStatus: &status}
	service, script, actor, key, _ := actionOperationFixture(t, now, &op)
	view, err := service.Query(context.Background(), testNoopAction, actor, []string{key})
	if view != nil || !errors.Is(err, ErrActionOperationExpired) {
		t.Fatalf("expiry boundary = %#v %v", view, err)
	}
	if len(script.execs) != 1 {
		t.Fatalf("expiry writes = %v", script.execs)
	}
	write := script.execs[0]
	for _, column := range []string{"`state`", "`is_deleted`", "`lease_owner_hmac`", "`lease_expires_at`", "`error_code`", "`result_kind`", "`result_guid`", "`result_http_status`"} {
		if !bytes.Contains([]byte(write), []byte(column)) {
			t.Fatalf("expiry update missing %s: %s", column, write)
		}
	}
}

func TestActionOperationLeaseGraceBoundary(t *testing.T) {
	base := int64(1_800_000_000_000)
	lease := base + actionOperationLeaseMillis
	for _, tc := range []struct {
		name string
		now  int64
		want error
	}{
		{name: "lease boundary", now: lease, want: ErrActionOperationConflict},
		{name: "grace boundary", now: lease + actionOperationRecoveryGraceMS, want: ErrActionOperationConflict},
		{name: "one millisecond after grace", now: lease + actionOperationRecoveryGraceMS + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := models.AdminOperation{ID: 30, SessionID: 20, State: models.OperationProcessing, LeaseExpiresAt: &lease, QueryExpiresAt: base + actionOperationQueryRetentionMS}
			service, script, _, _, _ := actionOperationFixture(t, tc.now, &op)
			err := service.MarkPendingRecovery(context.Background(), op.ID)
			if !errors.Is(err, tc.want) {
				t.Fatalf("MarkPendingRecovery error = %v, want %v", err, tc.want)
			}
			if tc.want == nil && (len(script.execs) != 1 || !bytes.Contains([]byte(script.execs[0]), []byte("pending"))) {
				// GORM binds the integer state, so the SQL need only prove one update.
				if len(script.execs) != 1 {
					t.Fatalf("recovery writes = %v", script.execs)
				}
			}
		})
	}
}

func stringsContainAny(values []string, needle string) bool {
	for _, value := range values {
		if bytes.Contains([]byte(value), []byte(needle)) {
			return true
		}
	}
	return false
}
