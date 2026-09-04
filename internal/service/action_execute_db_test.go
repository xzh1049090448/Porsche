package service

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const actionExecuteDriverName = "porsche_action_execute_script"

var (
	actionExecuteDriverOnce sync.Once
	actionExecuteDriverSeq  atomic.Uint64
	actionExecuteScripts    sync.Map
)

type actionExecuteState struct {
	operation    models.AdminOperation
	verification models.AdminActionVerification
	effects      int
	audits       int
	outbox       int
}
type actionExecuteScript struct {
	mu            sync.Mutex
	now           int64
	actor         models.User
	session       models.Session
	target        models.User
	policy        models.PermissionPolicyHead
	state         actionExecuteState
	queries       []string
	execs         []string
	beginCount    int
	commitCount   int
	rollbackCount int
	failAt        string
	commitUnknown bool
}
type actionExecuteDriver struct{}
type actionExecuteConn struct {
	script *actionExecuteScript
	tx     *actionExecuteTx
}
type actionExecuteTx struct {
	conn      *actionExecuteConn
	state     actionExecuteState
	savepoint actionExecuteState
}
type actionExecuteRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}
type actionExecuteResult int64

func (actionExecuteDriver) Open(name string) (driver.Conn, error) {
	value, ok := actionExecuteScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown execute script")
	}
	return &actionExecuteConn{script: value.(*actionExecuteScript)}, nil
}
func (c *actionExecuteConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare disabled")
}
func (c *actionExecuteConn) Close() error { return nil }
func (c *actionExecuteConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *actionExecuteConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if opts.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		return nil, errors.New("not read committed")
	}
	c.script.mu.Lock()
	defer c.script.mu.Unlock()
	c.script.beginCount++
	c.tx = &actionExecuteTx{conn: c, state: c.script.state}
	return c.tx, nil
}
func (c *actionExecuteConn) CheckNamedValue(value *driver.NamedValue) error {
	switch v := value.Value.(type) {
	case models.AdminOperationState:
		value.Value = int64(v)
	case models.AdminOperationFailure:
		value.Value = int64(v)
	case models.AdminResultKind:
		value.Value = int64(v)
	case *models.AdminOperationFailure:
		if v == nil {
			value.Value = nil
		} else {
			value.Value = int64(*v)
		}
	case *models.AdminResultKind:
		if v == nil {
			value.Value = nil
		} else {
			value.Value = int64(*v)
		}
	case *int64:
		if v == nil {
			value.Value = nil
		} else {
			value.Value = *v
		}
	case *int:
		if v == nil {
			value.Value = nil
		} else {
			value.Value = int64(*v)
		}
	}
	return nil
}

func (c *actionExecuteConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.script.mu.Lock()
	defer c.script.mu.Unlock()
	c.script.queries = append(c.script.queries, query)
	kind := executeQueryKind(query)
	if c.script.failAt == kind {
		return nil, errors.New("private scripted query failure")
	}
	state := c.script.state
	if c.tx != nil {
		state = c.tx.state
	}
	switch kind {
	case "preflight_operation":
		o := state.operation
		return executeRows([]string{"id", "actor_user_id", "session_id", "public_ref"}, [][]driver.Value{{o.ID, o.ActorUserID, o.SessionID, o.PublicRef}}), nil
	case "preflight_session":
		s := c.script.session
		return executeRows([]string{"id", "sid", "user_id", "session_version"}, [][]driver.Value{{s.ID, s.SID, s.UserID, int64(s.SessionVersion)}}), nil
	case "preflight_actor":
		u := c.script.actor
		return executeRows([]string{"id", "guid", "auth_version"}, [][]driver.Value{{u.ID, u.Guid, int64(u.AuthVersion)}}), nil
	case "actor":
		u := c.script.actor
		return executeRows([]string{"id", "guid", "password_hash", "role", "status", "is_deleted", "auth_version"}, [][]driver.Value{{u.ID, u.Guid, ptrDriver(u.PasswordHash), int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}}), nil
	case "session":
		s := c.script.session
		return executeRows([]string{"id", "guid", "sid", "user_id", "session_version", "is_deleted", "revoked_at", "expires_at"}, [][]driver.Value{{s.ID, s.Guid, s.SID, s.UserID, int64(s.SessionVersion), int64(s.IsDeleted), nil, s.ExpiresAt}}), nil
	case "operation":
		return executeRows(operationColumns(), [][]driver.Value{operationValues(state.operation)}), nil
	case "verification":
		return executeRows(verificationColumns(), [][]driver.Value{verificationValues(state.verification)}), nil
	case "target":
		u := c.script.target
		return executeRows([]string{"id", "guid", "role", "status", "is_deleted", "auth_version"}, [][]driver.Value{{u.ID, u.Guid, int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}}), nil
	case "policy":
		p := c.script.policy
		return executeRows([]string{"id", "guid", "is_deleted", "policy_version", "catalog_version", "rule_count"}, [][]driver.Value{{p.ID, p.Guid, int64(p.IsDeleted), p.PolicyVersion, int64(p.CatalogVersion), int64(p.RuleCount)}}), nil
	case "rules":
		return executeRows([]string{"id", "guid", "is_deleted", "policy_version", "capability", "effect"}, nil), nil
	default:
		return nil, fmt.Errorf("unexpected execute query: %s", query)
	}
}

func executeQueryKind(query string) string {
	locked := strings.Contains(query, "FOR UPDATE")
	switch {
	case strings.Contains(query, "FROM `admin_operations`") && !locked:
		return "preflight_operation"
	case strings.Contains(query, "FROM `user_sessions`") && !locked:
		return "preflight_session"
	case strings.Contains(query, "FROM `users`") && !locked:
		return "preflight_actor"
	case strings.Contains(query, "FROM `users`") && strings.Contains(query, "guid = ?"):
		return "target"
	case strings.Contains(query, "FROM `users`"):
		return "actor"
	case strings.Contains(query, "FROM `user_sessions`"):
		return "session"
	case strings.Contains(query, "FROM `admin_operations`"):
		return "operation"
	case strings.Contains(query, "FROM `admin_action_verifications`"):
		return "verification"
	case strings.Contains(query, "FROM `user_permission_heads`"):
		return "policy"
	case strings.Contains(query, "FROM `user_permission_overrides`"):
		return "rules"
	default:
		return "unknown"
	}
}

func (c *actionExecuteConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.script.mu.Lock()
	defer c.script.mu.Unlock()
	c.script.execs = append(c.script.execs, query)
	kind := executeExecKind(query)
	if strings.HasPrefix(query, "UPDATE `admin_operations`") {
		values, parseErr := operationUpdateValues(query, args)
		if parseErr != nil {
			return nil, parseErr
		}
		if values["error_code"] == nil {
			kind = "terminal_success"
		} else {
			kind = "terminal_failed"
		}
	}
	if c.script.failAt == kind {
		return nil, errors.New("private scripted write failure")
	}
	if c.tx == nil {
		return nil, errors.New("execute write outside owned transaction")
	}
	switch kind {
	case "consume":
		if c.tx.state.verification.ConsumedAt != nil || c.tx.state.verification.IsDeleted != 0 {
			return actionExecuteResult(0), nil
		}
		consumed := c.script.now
		c.tx.state.verification.ConsumedAt, c.tx.state.verification.IsDeleted = &consumed, 1
	case "savepoint":
		c.tx.savepoint = c.tx.state
	case "rollback_to":
		c.tx.state = c.tx.savepoint
	case "effect":
		c.tx.state.effects++
	case "audit":
		c.tx.state.audits++
	case "outbox":
		c.tx.state.outbox++
	case "terminal_success", "terminal_failed":
		if c.tx.state.operation.State != models.OperationProcessing {
			return actionExecuteResult(0), nil
		}
		finished := c.script.now
		c.tx.state.operation.FinishedAt = &finished
		c.tx.state.operation.QueryExpiresAt = finished + actionOperationQueryRetentionMS
		c.tx.state.operation.LeaseOwnerHMAC, c.tx.state.operation.LeaseExpiresAt = nil, nil
		if kind == "terminal_success" {
			resultKind, status := models.ResultNone, 204
			c.tx.state.operation.State = models.OperationSucceeded
			c.tx.state.operation.ResultKind, c.tx.state.operation.ResultHTTPStatus = &resultKind, &status
		} else {
			failure, status := models.FailureActionRejected, 409
			c.tx.state.operation.State = models.OperationFailed
			c.tx.state.operation.ErrorCode, c.tx.state.operation.ResultHTTPStatus = &failure, &status
		}
	default:
		return nil, fmt.Errorf("unexpected execute write: %s", query)
	}
	return actionExecuteResult(1), nil
}

func executeExecKind(query string) string {
	switch {
	case strings.HasPrefix(query, "UPDATE `admin_action_verifications`"):
		return "consume"
	case strings.HasPrefix(query, "SAVEPOINT"):
		return "savepoint"
	case strings.HasPrefix(query, "ROLLBACK TO SAVEPOINT"):
		return "rollback_to"
	case strings.Contains(query, "fixture_action_effects"):
		return "effect"
	case strings.Contains(query, "fixture_action_audits"):
		return "audit"
	case strings.Contains(query, "fixture_action_outbox"):
		return "outbox"
	case strings.HasPrefix(query, "UPDATE `admin_operations`"):
		return "terminal"
	default:
		return "unknown"
	}
}

func (tx *actionExecuteTx) Commit() error {
	s := tx.conn.script
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state, tx.conn.tx = tx.state, nil
	if s.commitUnknown {
		return errors.New("private commit acknowledgement failure")
	}
	s.commitCount++
	return nil
}
func (tx *actionExecuteTx) Rollback() error {
	s := tx.conn.script
	s.mu.Lock()
	defer s.mu.Unlock()
	tx.conn.tx = nil
	s.rollbackCount++
	return nil
}
func (rows *actionExecuteRows) Columns() []string { return rows.columns }
func (rows *actionExecuteRows) Close() error      { return nil }
func (rows *actionExecuteRows) Next(dest []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(dest, rows.values[rows.index])
	rows.index++
	return nil
}
func (result actionExecuteResult) LastInsertId() (int64, error) { return 0, nil }
func (result actionExecuteResult) RowsAffected() (int64, error) { return int64(result), nil }
func executeRows(columns []string, values [][]driver.Value) *actionExecuteRows {
	return &actionExecuteRows{columns: columns, values: values}
}

func actionExecuteFixture(t *testing.T) (*ActionOperationService, *actionExecuteScript, OperationIdentity) {
	t.Helper()
	now := int64(1_800_000_000_000)
	root := bytes.Repeat([]byte{0x73}, 32)
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
	authRedis, err := NewAuthRedis(client, "task9-auth-hmac-key-material")
	if err != nil {
		t.Fatal(err)
	}
	passwordHash := "not-used-but-required"
	actor := models.User{ID: 10, AuditFields: models.AuditFields{Guid: 1001}, PasswordHash: &passwordHash, Role: models.UserRoleRoot, Status: models.UserStatusActive, AuthVersion: 7}
	session := models.Session{ID: 20, AuditFields: models.AuditFields{Guid: 2001}, SID: "11111111-2222-4333-8444-555555555555", UserID: actor.ID, SessionVersion: 3, ExpiresAt: now + 600_000}
	target := models.User{ID: 11, AuditFields: models.AuditFields{Guid: 1101}, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 2}
	leaseOwner := [32]byte{}
	for i := range leaseOwner {
		leaseOwner[i] = byte(31 + i)
	}
	leaseDigest := crypto.LeaseOwnerDigest(leaseOwner)
	leaseHex := fmt.Sprintf("%x", leaseDigest)
	clear(leaseDigest[:])
	verificationID, leaseExpires := int64(40), now+actionOperationLeaseMillis
	publicRef := "op_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))
	requestHex := strings.Repeat("b", 64)
	operation := models.AdminOperation{ID: 30, AuditFields: models.AuditFields{Guid: 3001}, ActorUserID: actor.ID, ActorAuthVersion: actor.AuthVersion,
		SessionID: session.ID, Action: int(testNoopAction), IdempotencyKeyHMAC: strings.Repeat("a", 64), RequestHMAC: requestHex,
		VerificationID: &verificationID, State: models.OperationProcessing, PublicRef: publicRef, LeaseOwnerHMAC: &leaseHex,
		LeaseExpiresAt: &leaseExpires, QueryExpiresAt: now + actionOperationQueryRetentionMS}
	verification := models.AdminActionVerification{ID: verificationID, AuditFields: models.AuditFields{Guid: 4001}, ActorUserID: actor.ID,
		ActorAuthVersion: actor.AuthVersion, SessionID: session.ID, Action: int(testNoopAction), TargetKind: int(actionsecurity.TargetUser),
		TargetGUID: &target.Guid, IntentHMAC: requestHex, TicketHMAC: strings.Repeat("c", 64), ExpiresAt: now + 300_000}
	script := &actionExecuteScript{now: now, actor: actor, session: session, target: target,
		policy: models.PermissionPolicyHead{ID: 50, AuditFields: models.AuditFields{Guid: 5001}, UserID: actor.ID, PolicyVersion: 1, CatalogVersion: 1},
		state:  actionExecuteState{operation: operation, verification: verification}}
	actionExecuteDriverOnce.Do(func() { sql.Register(actionExecuteDriverName, actionExecuteDriver{}) })
	dsn := fmt.Sprintf("script-%d", actionExecuteDriverSeq.Add(1))
	actionExecuteScripts.Store(dsn, script)
	t.Cleanup(func() { actionExecuteScripts.Delete(dsn) })
	sqlDB, err := sql.Open(actionExecuteDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Discard, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		if action != testNoopAction {
			return actionsecurity.Descriptor{}, false
		}
		return actionsecurity.Descriptor{Action: testNoopAction, Name: "test.noop", Capability: "users.delete", RequiresTicket: true,
			TargetKind: actionsecurity.TargetUser, Active: true, Encode: func(any) ([]byte, error) { return []byte("unused"), nil }}, true
	}
	service, err := newActionOperationService(db, limiter, authRedis, crypto, resolver, &actionIssueClock{now: now}, bytes.NewReader(nil), func() int64 { return 1 })
	if err != nil {
		t.Fatal(err)
	}
	return service, script, OperationIdentity{ID: operation.ID, PublicRef: publicRef, LeaseOwner: leaseOwner}
}

func TestActionExecuteSucceededAtomicAndLockOrder(t *testing.T) {
	service, script, identity := actionExecuteFixture(t)
	consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
	view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
	if err != nil || view == nil || view.Status != "succeeded" {
		t.Fatalf("Execute = %#v, %v queries=%v execs=%v", view, err, script.queries, script.execs)
	}
	if consumer.calls != 1 || script.state.effects != 1 || script.state.audits != 1 || script.state.outbox != 1 || script.state.operation.State != models.OperationSucceeded {
		t.Fatalf("non-atomic success: %#v", script.state)
	}
	locked := make([]string, 0)
	for _, query := range script.queries {
		if strings.Contains(query, "FOR UPDATE") {
			locked = append(locked, executeQueryKind(query))
		}
	}
	want := []string{"actor", "session", "operation", "verification", "target", "policy"}
	if strings.Join(locked[:len(want)], ",") != strings.Join(want, ",") {
		t.Fatalf("lock order=%v", locked)
	}
}

func TestActionExecuteKnownRejectionRollsBackCallbackSavepoint(t *testing.T) {
	service, script, identity := actionExecuteFixture(t)
	failure := models.FailureActionRejected
	view, err := service.Execute(context.Background(), identity, &fixtureActionConsumer{outcome: TerminalOutcome{Failure: &failure, HTTPStatus: 409}}, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
	if err != nil || view == nil || view.Status != "failed" {
		t.Fatalf("rejection = %#v, %v", view, err)
	}
	if script.state.effects != 0 || script.state.audits != 1 || script.state.outbox != 1 || script.state.operation.State != models.OperationFailed || script.state.verification.ConsumedAt == nil {
		t.Fatalf("rejection atomicity=%#v", script.state)
	}
	if !containsExecuteKind(script.execs, "savepoint") || !containsExecuteKind(script.execs, "rollback_to") {
		t.Fatalf("savepoint events=%v", script.execs)
	}
}

func TestActionExecuteFaultMatrixRollsBackWithoutFalseTerminal(t *testing.T) {
	for _, point := range []string{"actor", "session", "operation", "verification", "target", "policy", "rules", "consume", "savepoint", "effect", "audit", "outbox", "terminal_success"} {
		t.Run(point, func(t *testing.T) {
			service, script, identity := actionExecuteFixture(t)
			script.failAt = point
			view, err := service.Execute(context.Background(), identity, &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
			if view != nil || !errors.Is(err, ErrActionOperationUnavailable) {
				t.Fatalf("fault %s=%#v,%v", point, view, err)
			}
			if script.state.operation.State != models.OperationProcessing || script.state.verification.ConsumedAt != nil || script.state.effects != 0 || script.state.audits != 0 || script.state.outbox != 0 {
				t.Fatalf("fault %s committed %#v", point, script.state)
			}
		})
	}
	t.Run("terminal_failed", func(t *testing.T) {
		service, script, identity := actionExecuteFixture(t)
		script.failAt = "terminal_failed"
		failure := models.FailureActionRejected
		view, err := service.Execute(context.Background(), identity, &fixtureActionConsumer{outcome: TerminalOutcome{Failure: &failure, HTTPStatus: 409}}, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
		if view != nil || !errors.Is(err, ErrActionOperationUnavailable) || script.state.operation.State != models.OperationProcessing || script.state.verification.ConsumedAt != nil || script.state.effects != 0 || script.state.audits != 0 || script.state.outbox != 0 {
			t.Fatalf("failed-terminal fault=%#v %v %#v", view, err, script.state)
		}
	})
}

func TestActionExecuteCommitUnknownDoesNotReplayCallback(t *testing.T) {
	service, script, identity := actionExecuteFixture(t)
	script.commitUnknown = true
	consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
	view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
	var unknown *CommitUnknownError
	if view != nil || !errors.As(err, &unknown) || unknown.PublicRef != identity.PublicRef || consumer.calls != 1 {
		t.Fatalf("commit unknown=%#v %#v calls=%d", view, err, consumer.calls)
	}
	if strings.Contains(err.Error(), "private") || script.beginCount != 1 || script.state.operation.State != models.OperationSucceeded {
		t.Fatalf("commit unknown leaked/replayed: %v %#v", err, script.state)
	}
}

func TestActionExecuteRejectsWrongLeaseExpiredAndNonProcessing(t *testing.T) {
	mutations := []func(*actionExecuteScript, *OperationIdentity){
		func(_ *actionExecuteScript, identity *OperationIdentity) { identity.LeaseOwner[0] ^= 0xff },
		func(script *actionExecuteScript, _ *OperationIdentity) {
			expiry := script.now
			script.state.operation.LeaseExpiresAt = &expiry
		},
		func(script *actionExecuteScript, _ *OperationIdentity) {
			script.state.operation.State = models.OperationPendingRecovery
			script.state.operation.LeaseOwnerHMAC, script.state.operation.LeaseExpiresAt = nil, nil
		},
	}
	for _, mutate := range mutations {
		service, script, identity := actionExecuteFixture(t)
		mutate(script, &identity)
		consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
		view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
		if view != nil || err == nil || consumer.calls != 0 || script.state.effects != 0 {
			t.Fatalf("invalid identity executed: %#v %v %#v", view, err, script.state)
		}
	}
}

func TestActionExecuteCorruptBindingsAndCallbackErrorsFailClosed(t *testing.T) {
	mutations := []func(*ActionOperationService, *actionExecuteScript, *OperationIdentity){
		func(_ *ActionOperationService, script *actionExecuteScript, _ *OperationIdentity) {
			script.state.operation.ActorAuthVersion++
		},
		func(_ *ActionOperationService, script *actionExecuteScript, _ *OperationIdentity) {
			script.state.verification.IntentHMAC = strings.Repeat("d", 64)
		},
		func(_ *ActionOperationService, script *actionExecuteScript, _ *OperationIdentity) {
			script.state.verification.ExpiresAt = script.now
		},
		func(_ *ActionOperationService, script *actionExecuteScript, _ *OperationIdentity) {
			consumed := script.now - 1
			script.state.verification.ConsumedAt, script.state.verification.IsDeleted = &consumed, 1
		},
		func(service *ActionOperationService, _ *actionExecuteScript, _ *OperationIdentity) {
			service.resolve = actionsecurity.ResolveActiveAction
		},
	}
	for i, mutate := range mutations {
		service, script, identity := actionExecuteFixture(t)
		mutate(service, script, &identity)
		consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
		view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
		if view != nil || err == nil || consumer.calls != 0 || script.state.effects != 0 || script.state.operation.State != models.OperationProcessing {
			t.Fatalf("corrupt case %d executed: %#v %v %#v", i, view, err, script.state)
		}
	}
	for _, consumer := range []*fixtureActionConsumer{
		{err: errors.New("private callback failure")},
		{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 500}},
	} {
		service, script, identity := actionExecuteFixture(t)
		view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
		if view != nil || !errors.Is(err, ErrActionOperationUnavailable) || strings.Contains(err.Error(), "private") || consumer.calls != 1 || script.state.effects != 0 || script.state.verification.ConsumedAt != nil || script.state.operation.State != models.OperationProcessing {
			t.Fatalf("callback failure leaked/committed: %#v %v %#v", view, err, script.state)
		}
	}
}

func containsExecuteKind(queries []string, kind string) bool {
	for _, query := range queries {
		if executeExecKind(query) == kind {
			return true
		}
	}
	return false
}
