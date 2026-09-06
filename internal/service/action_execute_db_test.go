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
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupRealActionPrimitiveTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	tables := []string{
		"fixture_action_official_outbox", "fixture_action_official_audits",
		"fixture_action_callback_outbox", "fixture_action_callback_audits", "fixture_action_effects", "fixture_action_rollback_probes",
	}
	for _, table := range tables {
		if err := db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			t.Fatalf("drop stale fixture-only table %s: %v", table, err)
		}
	}
	statements := []string{
		"CREATE TABLE fixture_action_effects (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, operation_ref CHAR(46) NOT NULL, UNIQUE KEY uk_fixture_effect_ref (operation_ref)) ENGINE=InnoDB",
		"CREATE TABLE fixture_action_callback_audits (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, operation_ref CHAR(46) NOT NULL) ENGINE=InnoDB",
		"CREATE TABLE fixture_action_callback_outbox (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, operation_ref CHAR(46) NOT NULL) ENGINE=InnoDB",
		"CREATE TABLE fixture_action_official_audits (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, operation_ref CHAR(46) NOT NULL, state INT NOT NULL) ENGINE=InnoDB",
		"CREATE TABLE fixture_action_official_outbox (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, operation_ref CHAR(46) NOT NULL, state INT NOT NULL) ENGINE=InnoDB",
		"CREATE TABLE fixture_action_rollback_probes (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, stage VARCHAR(64) NOT NULL) ENGINE=InnoDB",
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create fixture-only primitive table: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, table := range tables {
			if err := db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
				t.Errorf("drop fixture-only table %s: %v", table, err)
			}
		}
	})
}

func prepareRealActionOperation(t *testing.T, fixture *realActionFixture, intent, ip string) (*OperationIdentity, string) {
	t.Helper()
	typedIntent := testNoopIntent(fixture.targetRow.Guid, intent)
	issued, err := fixture.verification.Issue(context.Background(), VerificationIssue{Action: realFixtureAction, TargetGUID: &fixture.targetRow.Guid, Actor: fixture.actor, Intent: typedIntent, CurrentPassword: []byte(fixture.password), TrustedIP: ip})
	if err != nil {
		t.Fatal(err)
	}
	key := newRealIdempotencyKey(t)
	identity, view, err := fixture.operation.Begin(context.Background(), OperationBegin{Action: realFixtureAction, Actor: fixture.actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{issued.Ticket}, Intent: typedIntent})
	if err != nil || identity == nil || view == nil || view.Status != "processing" {
		t.Fatalf("prepare Begin identity=%v view=%v err=%v", identity, view, err)
	}
	return identity, key
}

type realFailingAuditWriter struct{ err error }

func (writer realFailingAuditWriter) Write(context.Context, *gorm.DB, ActionAuditEvent) error {
	return writer.err
}

type realFailingOutboxWriter struct{ err error }

func (writer realFailingOutboxWriter) Write(context.Context, *gorm.DB, ActionOutboxEvent) error {
	return writer.err
}

type realCancelOutboxWriter struct {
	delegate TransactionalOutboxWriter
	cancel   context.CancelFunc
}

func (writer realCancelOutboxWriter) Write(ctx context.Context, tx *gorm.DB, event ActionOutboxEvent) error {
	if err := writer.delegate.Write(ctx, tx, event); err != nil {
		return err
	}
	writer.cancel()
	return nil
}

type realCommitUnknownRunner struct{ calls atomic.Int64 }

type realTargetFactsConsumer struct {
	targetGUID  int64
	wantStatus  models.UserStatus
	wantVersion int
	calls       int
}

func (consumer *realTargetFactsConsumer) Execute(ctx context.Context, tx *gorm.DB, _ models.AdminOperation) (TerminalOutcome, error) {
	consumer.calls++
	var target models.User
	if err := tx.WithContext(ctx).Select("id", "guid", "status", "auth_version").Where("guid = ? AND is_deleted = 0", consumer.targetGUID).First(&target).Error; err != nil {
		return TerminalOutcome{}, err
	}
	if target.Status != consumer.wantStatus || target.AuthVersion != consumer.wantVersion {
		failure := models.FailureActionRejected
		return TerminalOutcome{Failure: &failure, HTTPStatus: 409}, nil
	}
	return TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}, nil
}

func (runner *realCommitUnknownRunner) Run(ctx context.Context, db *gorm.DB, callback func(*gorm.DB) error) error {
	runner.calls.Add(1)
	if err := db.WithContext(ctx).Transaction(callback, &sql.TxOptions{Isolation: sql.LevelReadCommitted}); err != nil {
		return err
	}
	return errors.New("fixture commit acknowledgement unavailable")
}

func assertRealPrimitiveCounts(t *testing.T, db *gorm.DB, wantEffect, wantCallback, wantOfficial int64) {
	t.Helper()
	for table, want := range map[string]int64{
		"fixture_action_effects": wantEffect, "fixture_action_callback_audits": wantCallback,
		"fixture_action_callback_outbox": wantCallback, "fixture_action_official_audits": wantOfficial,
		"fixture_action_official_outbox": wantOfficial,
	} {
		var got int64
		if err := db.Table(table).Count(&got).Error; err != nil || got != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, got, want, err)
		}
	}
}

const actionExecuteDriverName = "porsche_action_execute_script"

var (
	actionExecuteDriverOnce sync.Once
	actionExecuteDriverSeq  atomic.Uint64
	actionExecuteScripts    sync.Map
)

type actionExecuteState struct {
	operation      models.AdminOperation
	verification   models.AdminActionVerification
	effects        int
	callbackAudits int
	callbackOutbox int
	officialAudits int
	officialOutbox int
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
	lastError     string
	queryStep     int
	ticketless    bool
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
	lockStep  int
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
	c.script.queryStep = 0
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

func (c *actionExecuteConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.script.mu.Lock()
	defer c.script.mu.Unlock()
	c.script.queries = append(c.script.queries, query)
	kind := executeQueryKind(query)
	if c.script.ticketless && kind == "verification" {
		return nil, errors.New("ticketless execute accessed verification storage")
	}
	if err := validateExecuteQuery(c.script, c.tx, kind, query, args); err != nil {
		return nil, err
	}
	sequenceKind := kind
	if (kind == "policy" || kind == "rules") && strings.Contains(query, "FOR UPDATE") {
		sequenceKind += "_lock"
	}
	expected := []string{"actor", "session", "operation", "verification", "target", "policy_lock", "rules_lock", "policy", "rules"}
	if c.script.ticketless {
		expected = []string{"actor", "session", "operation", "policy_lock", "rules_lock", "policy", "rules"}
	}
	if c.script.queryStep >= len(expected) || sequenceKind != expected[c.script.queryStep] {
		return nil, fmt.Errorf("execute query order step %d=%s", c.script.queryStep, sequenceKind)
	}
	c.script.queryStep++
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

func validateExecuteQuery(script *actionExecuteScript, tx *actionExecuteTx, kind, query string, args []driver.NamedValue) error {
	exactSQL := func(want string) error {
		if query != want {
			return fmt.Errorf("%s SQL shape mismatch: %s", kind, query)
		}
		return nil
	}
	require := func(parts ...string) error {
		for _, part := range parts {
			if !strings.Contains(query, part) {
				return fmt.Errorf("%s query missing %q", kind, part)
			}
		}
		return nil
	}
	exactArgs := func(want ...any) error {
		if len(args) != len(want) {
			return fmt.Errorf("%s args=%d want=%d", kind, len(args), len(want))
		}
		for i := range want {
			if fmt.Sprint(args[i].Value) != fmt.Sprint(want[i]) {
				return fmt.Errorf("%s arg %d=%v want=%v", kind, i, args[i].Value, want[i])
			}
		}
		return nil
	}
	locked := false
	switch kind {
	case "preflight_operation":
		if err := exactSQL("SELECT `id`,`actor_user_id`,`session_id`,`public_ref` FROM `admin_operations` WHERE id = ? ORDER BY `admin_operations`.`id` LIMIT ?"); err != nil {
			return err
		}
		if err := require("SELECT `id`,`actor_user_id`,`session_id`,`public_ref`", "FROM `admin_operations`", "WHERE id = ?", "LIMIT ?"); err != nil {
			return err
		}
		if err := exactArgs(script.state.operation.ID, int64(1)); err != nil {
			return err
		}
	case "preflight_session":
		if err := exactSQL("SELECT `id`,`sid`,`user_id`,`session_version` FROM `user_sessions` WHERE id = ? ORDER BY `user_sessions`.`id` LIMIT ?"); err != nil {
			return err
		}
		if err := require("SELECT `id`,`sid`,`user_id`,`session_version`", "FROM `user_sessions`", "WHERE id = ?", "LIMIT ?"); err != nil {
			return err
		}
		if err := exactArgs(script.session.ID, int64(1)); err != nil {
			return err
		}
	case "preflight_actor":
		if err := exactSQL("SELECT `id`,`guid`,`auth_version` FROM `users` WHERE id = ? ORDER BY `users`.`id` LIMIT ?"); err != nil {
			return err
		}
		if err := require("SELECT `id`,`guid`,`auth_version`", "FROM `users`", "WHERE id = ?", "LIMIT ?"); err != nil {
			return err
		}
		if err := exactArgs(script.actor.ID, int64(1)); err != nil {
			return err
		}
	case "actor":
		locked = true
		if err := exactSQL("SELECT `id`,`guid`,`password_hash`,`role`,`status`,`is_deleted`,`auth_version` FROM `users` WHERE id = ? ORDER BY `users`.`id` LIMIT ? FOR UPDATE"); err != nil {
			return err
		}
		if err := require("SELECT `id`,`guid`,`password_hash`,`role`,`status`,`is_deleted`,`auth_version`", "FROM `users`", "WHERE id = ?", "LIMIT ?", "FOR UPDATE"); err != nil {
			return err
		}
		if err := exactArgs(script.actor.ID, int64(1)); err != nil {
			return err
		}
	case "session":
		locked = true
		if err := exactSQL("SELECT `id`,`guid`,`sid`,`user_id`,`session_version`,`is_deleted`,`revoked_at`,`expires_at` FROM `user_sessions` WHERE user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND expires_at > ? ORDER BY id ASC FOR UPDATE"); err != nil {
			return err
		}
		if err := require("SELECT `id`,`guid`,`sid`,`user_id`,`session_version`,`is_deleted`,`revoked_at`,`expires_at`", "FROM `user_sessions`", "user_id = ?", "is_deleted = 0", "revoked_at IS NULL", "expires_at > ?", "ORDER BY id ASC", "FOR UPDATE"); err != nil {
			return err
		}
		if err := exactArgs(script.actor.ID, script.now); err != nil {
			return err
		}
	case "operation":
		locked = true
		if err := exactSQL("SELECT * FROM `admin_operations` WHERE id = ? ORDER BY `admin_operations`.`id` LIMIT ? FOR UPDATE"); err != nil {
			return err
		}
		if err := require("SELECT * FROM `admin_operations`", "WHERE id = ?", "LIMIT ?", "FOR UPDATE"); err != nil {
			return err
		}
		if err := exactArgs(script.state.operation.ID, int64(1)); err != nil {
			return err
		}
	case "verification":
		locked = true
		if err := exactSQL("SELECT * FROM `admin_action_verifications` WHERE id = ? ORDER BY `admin_action_verifications`.`id` LIMIT ? FOR UPDATE"); err != nil {
			return err
		}
		if err := require("SELECT * FROM `admin_action_verifications`", "WHERE id = ?", "LIMIT ?", "FOR UPDATE"); err != nil {
			return err
		}
		if err := exactArgs(script.state.verification.ID, int64(1)); err != nil {
			return err
		}
	case "target":
		locked = true
		if err := exactSQL("SELECT `id`,`guid`,`role`,`status`,`is_deleted`,`auth_version` FROM `users` WHERE guid = ? AND is_deleted = 0 ORDER BY `users`.`id` LIMIT ? FOR UPDATE"); err != nil {
			return err
		}
		if err := require("SELECT `id`,`guid`,`role`,`status`,`is_deleted`,`auth_version`", "FROM `users`", "guid = ?", "is_deleted = 0", "LIMIT ?", "FOR UPDATE"); err != nil {
			return err
		}
		if err := exactArgs(script.target.Guid, int64(1)); err != nil {
			return err
		}
	case "policy":
		if strings.Contains(query, "FOR UPDATE") {
			locked = true
			if err := exactSQL("SELECT `id` FROM `user_permission_heads` WHERE user_id = ? ORDER BY `user_permission_heads`.`id` LIMIT ? FOR UPDATE"); err != nil {
				return err
			}
			if err := require("SELECT `id`", "WHERE user_id = ?", "LIMIT ?", "FOR UPDATE"); err != nil {
				return err
			}
			if err := exactArgs(script.actor.ID, int64(1)); err != nil {
				return err
			}
		} else {
			if err := exactSQL("SELECT `id`,`guid`,`is_deleted`,`policy_version`,`catalog_version`,`rule_count` FROM `user_permission_heads` WHERE user_id = ? ORDER BY `user_permission_heads`.`id` LIMIT ?"); err != nil {
				return err
			}
			if err := require("WHERE user_id = ?", "LIMIT ?"); err != nil {
				return err
			}
			if err := exactArgs(script.actor.ID, int64(1)); err != nil {
				return err
			}
		}
	case "rules":
		if strings.Contains(query, "FOR UPDATE") {
			locked = true
			if err := exactSQL("SELECT `id` FROM `user_permission_overrides` WHERE user_id = ? ORDER BY id ASC FOR UPDATE"); err != nil {
				return err
			}
			if err := require("SELECT `id`", "WHERE user_id = ?", "ORDER BY id ASC", "FOR UPDATE"); err != nil {
				return err
			}
			if err := exactArgs(script.actor.ID); err != nil {
				return err
			}
		} else {
			if err := exactSQL("SELECT `id`,`guid`,`is_deleted`,`policy_version`,`capability`,`effect` FROM `user_permission_overrides` WHERE user_id = ? AND is_deleted <> 1 ORDER BY capability LIMIT ?"); err != nil {
				return err
			}
			if err := require("WHERE user_id = ? AND is_deleted <> 1", "ORDER BY capability", "LIMIT ?"); err != nil {
				return err
			}
			if err := exactArgs(script.actor.ID, int64(len(authz.Catalog())+1)); err != nil {
				return err
			}
		}
	default:
		return errors.New("unrecognized execute query")
	}
	if locked {
		if tx == nil {
			return fmt.Errorf("%s lock outside transaction", kind)
		}
		order := []string{"actor", "session", "operation", "verification", "target", "policy", "rules"}
		if script.ticketless {
			order = []string{"actor", "session", "operation", "policy", "rules"}
		}
		if tx.lockStep >= len(order) || kind != order[tx.lockStep] {
			return fmt.Errorf("lock order step %d=%s", tx.lockStep, kind)
		}
		tx.lockStep++
	}
	return nil
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
	if c.script.ticketless && kind == "consume" {
		return nil, errors.New("ticketless execute consumed verification storage")
	}
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
	if err := validateExecuteExec(c.script, c.tx, kind, query, args); err != nil {
		c.script.lastError = err.Error()
		return nil, err
	}
	if c.script.failAt == kind {
		return nil, errors.New("private scripted write failure")
	}
	if c.tx == nil {
		return nil, errors.New("execute write outside owned transaction")
	}
	switch kind {
	case "consume":
		if c.tx.state.verification.ConsumedAt != nil || c.tx.state.verification.IsDeleted != 0 || c.tx.state.verification.ExpiresAt <= c.script.now {
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
	case "callback_audit":
		c.tx.state.callbackAudits++
	case "callback_outbox":
		c.tx.state.callbackOutbox++
	case "official_audit":
		c.tx.state.officialAudits++
	case "official_outbox":
		c.tx.state.officialOutbox++
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

func validateExecuteExec(script *actionExecuteScript, tx *actionExecuteTx, kind, query string, args []driver.NamedValue) error {
	if tx == nil {
		return errors.New("execute write outside owned transaction")
	}
	require := func(parts ...string) error {
		for _, part := range parts {
			if !strings.Contains(query, part) {
				return fmt.Errorf("%s write missing %q", kind, part)
			}
		}
		return nil
	}
	exactArgs := func(want ...any) error {
		if len(args) != len(want) {
			return fmt.Errorf("%s args=%d want=%d", kind, len(args), len(want))
		}
		for i := range want {
			if fmt.Sprint(args[i].Value) != fmt.Sprint(want[i]) {
				return fmt.Errorf("%s arg %d=%v want=%v", kind, i, args[i].Value, want[i])
			}
		}
		return nil
	}
	exactSQL := func(want string) error {
		if query != want {
			return fmt.Errorf("%s SQL shape mismatch: %s", kind, query)
		}
		return nil
	}
	switch kind {
	case "consume":
		if err := exactSQL("UPDATE `admin_action_verifications` SET `consumed_at`=?,`is_deleted`=?,`updated_at`=?,`updated_by`=? WHERE id = ? AND consumed_at IS NULL AND expires_at > ? AND is_deleted = 0"); err != nil {
			return err
		}
		if err := require("UPDATE `admin_action_verifications`", "`consumed_at`=?", "`is_deleted`=?", "`updated_at`=?", "`updated_by`=?", "id = ? AND consumed_at IS NULL AND expires_at > ? AND is_deleted = 0"); err != nil {
			return err
		}
		return exactArgs(script.now, int64(1), script.now, script.actor.ID, script.state.verification.ID, script.now)
	case "savepoint":
		if query != "SAVEPOINT "+actionExecuteSavepoint {
			return errors.New("wrong callback savepoint")
		}
		return exactArgs()
	case "rollback_to":
		if query != "ROLLBACK TO SAVEPOINT "+actionExecuteSavepoint {
			return errors.New("wrong callback rollback-to")
		}
		return exactArgs()
	case "effect", "callback_audit", "callback_outbox":
		wantSQL := map[string]string{
			"effect":          "INSERT INTO fixture_action_effects (operation_ref) VALUES (?)",
			"callback_audit":  "INSERT INTO fixture_action_callback_audits (operation_ref) VALUES (?)",
			"callback_outbox": "INSERT INTO fixture_action_callback_outbox (operation_ref) VALUES (?)",
		}
		if err := exactSQL(wantSQL[kind]); err != nil {
			return err
		}
		return exactArgs(script.state.operation.PublicRef)
	case "official_audit", "official_outbox":
		wantSQL := map[string]string{
			"official_audit":  "INSERT INTO fixture_action_official_audits (operation_ref, state) VALUES (?, ?)",
			"official_outbox": "INSERT INTO fixture_action_official_outbox (operation_ref, state) VALUES (?, ?)",
		}
		if err := exactSQL(wantSQL[kind]); err != nil {
			return err
		}
		if len(args) != 2 || fmt.Sprint(args[0].Value) != script.state.operation.PublicRef {
			return fmt.Errorf("%s redacted args invalid", kind)
		}
		return nil
	case "terminal_success", "terminal_failed":
		terminalSQL := "UPDATE `admin_operations` SET `error_code`=?,`finished_at`=?,`lease_expires_at`=?,`lease_owner_hmac`=?,`query_expires_at`=?,`result_guid`=?,`result_http_status`=?,`result_kind`=?,`state`=?,`updated_at`=?,`updated_by`=? WHERE id = ? AND state = ? AND is_deleted = 0 AND lease_owner_hmac = ? AND verification_id = ?"
		selector := "id = ? AND state = ? AND is_deleted = 0 AND lease_owner_hmac = ? AND verification_id = ?"
		if script.ticketless {
			terminalSQL = "UPDATE `admin_operations` SET `error_code`=?,`finished_at`=?,`lease_expires_at`=?,`lease_owner_hmac`=?,`query_expires_at`=?,`result_guid`=?,`result_http_status`=?,`result_kind`=?,`state`=?,`updated_at`=?,`updated_by`=? WHERE id = ? AND state = ? AND is_deleted = 0 AND lease_owner_hmac = ? AND verification_id IS NULL"
			selector = "id = ? AND state = ? AND is_deleted = 0 AND lease_owner_hmac = ? AND verification_id IS NULL"
		}
		if err := exactSQL(terminalSQL); err != nil {
			return err
		}
		if err := require("UPDATE `admin_operations`", selector); err != nil {
			return err
		}
		values, err := operationUpdateValues(query, args)
		if err != nil {
			return err
		}
		if len(values) != 11 {
			return fmt.Errorf("terminal assignments=%d", len(values))
		}
		for _, key := range []string{"state", "finished_at", "query_expires_at", "lease_owner_hmac", "lease_expires_at", "error_code", "result_kind", "result_guid", "result_http_status", "updated_at", "updated_by"} {
			if _, ok := values[key]; !ok {
				return fmt.Errorf("terminal write missing assignment %s", key)
			}
		}
		wantState := models.OperationSucceeded
		wantFailure, wantKind, wantStatus := any(nil), any(int64(models.ResultNone)), any(int64(204))
		if kind == "terminal_failed" {
			wantState, wantFailure, wantKind, wantStatus = models.OperationFailed, int64(models.FailureActionRejected), nil, int64(409)
		}
		want := map[string]any{"state": int64(wantState), "finished_at": script.now, "query_expires_at": script.now + actionOperationQueryRetentionMS,
			"lease_owner_hmac": nil, "lease_expires_at": nil, "error_code": wantFailure, "result_kind": wantKind, "result_guid": nil,
			"result_http_status": wantStatus, "updated_at": script.now, "updated_by": script.actor.ID}
		for key, expected := range want {
			if fmt.Sprint(values[key]) != fmt.Sprint(expected) {
				return fmt.Errorf("terminal %s=%v want=%v", key, values[key], expected)
			}
		}
		assignments := len(values)
		wantTailLen := 4
		if script.ticketless {
			wantTailLen = 3
		}
		if len(args) != assignments+wantTailLen {
			return fmt.Errorf("terminal tail args=%d", len(args)-assignments)
		}
		tail := args[assignments:]
		lease := script.state.operation.LeaseOwnerHMAC
		if lease == nil {
			return errors.New("terminal expected lease missing")
		}
		wantTail := []any{script.state.operation.ID, int64(models.OperationProcessing), *lease, script.state.verification.ID}
		if script.ticketless {
			wantTail = []any{script.state.operation.ID, int64(models.OperationProcessing), *lease}
		}
		if len(tail) != len(wantTail) {
			return fmt.Errorf("terminal selector tail=%v", tail)
		}
		for i := range tail {
			if fmt.Sprint(tail[i].Value) != fmt.Sprint(wantTail[i]) {
				return fmt.Errorf("terminal selector %d=%v want=%v", i, tail[i].Value, wantTail[i])
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown execute write kind %s", kind)
	}
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
	case strings.Contains(query, "fixture_action_callback_audits"):
		return "callback_audit"
	case strings.Contains(query, "fixture_action_callback_outbox"):
		return "callback_outbox"
	case strings.Contains(query, "fixture_action_official_audits"):
		return "official_audit"
	case strings.Contains(query, "fixture_action_official_outbox"):
		return "official_outbox"
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
	if s.commitUnknown {
		tx.conn.tx = nil
		return errors.New("private commit acknowledgement failure")
	}
	s.state, tx.conn.tx = tx.state, nil
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

func actionExecuteFixture(t *testing.T) (*ActionOperationService, *actionExecuteScript, *OperationIdentity) {
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
	identity := &OperationIdentity{ID: operation.ID, PublicRef: publicRef,
		actor:      ActionActor{UserID: actor.ID, UserGUID: actor.Guid, AuthVersion: actor.AuthVersion, SessionSID: session.SID, SessionVersion: session.SessionVersion},
		capability: newOperationLeaseCapability(&leaseOwner)}
	if identity.capability == nil {
		t.Fatal("fixture lease capability is nil")
	}
	return service, script, identity
}

func TestActionExecuteSucceededAtomicAndLockOrder(t *testing.T) {
	service, script, identity := actionExecuteFixture(t)
	consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
	view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
	if err != nil || view == nil || view.Status != "succeeded" {
		t.Fatalf("Execute = %#v, %v validation=%s queries=%v execs=%v", view, err, script.lastError, script.queries, script.execs)
	}
	if consumer.calls != 1 || script.state.effects != 1 || script.state.callbackAudits != 1 || script.state.callbackOutbox != 1 || script.state.officialAudits != 1 || script.state.officialOutbox != 1 || script.state.operation.State != models.OperationSucceeded {
		t.Fatalf("non-atomic success: %#v", script.state)
	}
	if !operationLeaseCleared(identity) {
		t.Fatal("success retained caller lease")
	}
	if len(script.queries) != 9 || executeQueryKind(script.queries[0]) != "actor" {
		t.Fatalf("unexpected pre-transaction DB reads or missing locks: %v", script.queries)
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
	if script.state.effects != 0 || script.state.callbackAudits != 0 || script.state.callbackOutbox != 0 || script.state.officialAudits != 1 || script.state.officialOutbox != 1 || script.state.operation.State != models.OperationFailed || script.state.verification.ConsumedAt == nil {
		t.Fatalf("rejection atomicity=%#v", script.state)
	}
	if !containsExecuteKind(script.execs, "savepoint") || !containsExecuteKind(script.execs, "rollback_to") {
		t.Fatalf("savepoint events=%v", script.execs)
	}
	if !operationLeaseCleared(identity) {
		t.Fatal("known failure retained caller lease")
	}
}

func TestActionExecuteFaultMatrixRollsBackWithoutFalseTerminal(t *testing.T) {
	for _, point := range []string{"actor", "session", "operation", "verification", "target", "policy", "rules", "consume", "savepoint", "effect", "callback_audit", "callback_outbox", "official_audit", "official_outbox", "terminal_success"} {
		t.Run(point, func(t *testing.T) {
			service, script, identity := actionExecuteFixture(t)
			script.failAt = point
			view, err := service.Execute(context.Background(), identity, &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
			if view != nil || !errors.Is(err, ErrActionOperationUnavailable) {
				t.Fatalf("fault %s=%#v,%v", point, view, err)
			}
			if !executeStateIsPristine(script.state) {
				t.Fatalf("fault %s committed %#v", point, script.state)
			}
			if !operationLeaseCleared(identity) {
				t.Fatalf("fault %s retained caller lease", point)
			}
		})
	}
	t.Run("terminal_failed", func(t *testing.T) {
		service, script, identity := actionExecuteFixture(t)
		script.failAt = "terminal_failed"
		failure := models.FailureActionRejected
		view, err := service.Execute(context.Background(), identity, &fixtureActionConsumer{outcome: TerminalOutcome{Failure: &failure, HTTPStatus: 409}}, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
		if view != nil || !errors.Is(err, ErrActionOperationUnavailable) || !executeStateIsPristine(script.state) {
			t.Fatalf("failed-terminal fault=%#v %v %#v", view, err, script.state)
		}
		if !operationLeaseCleared(identity) {
			t.Fatal("failed terminal update retained caller lease")
		}
	})
	t.Run("rollback_to", func(t *testing.T) {
		service, script, identity := actionExecuteFixture(t)
		script.failAt = "rollback_to"
		failure := models.FailureActionRejected
		consumer := &fixtureActionConsumer{outcome: TerminalOutcome{Failure: &failure, HTTPStatus: 409}}
		view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
		if view != nil || !errors.Is(err, ErrActionOperationUnavailable) || consumer.calls != 1 || !executeStateIsPristine(script.state) || script.rollbackCount != 1 {
			t.Fatalf("rollback-to fault=%#v %v calls=%d state=%#v rollbacks=%d", view, err, consumer.calls, script.state, script.rollbackCount)
		}
		if !operationLeaseCleared(identity) {
			t.Fatal("rollback-to failure retained caller lease")
		}
	})
}

func TestActionExecuteCommitUnknownDoesNotReplayCallback(t *testing.T) {
	service, script, identity := actionExecuteFixture(t)
	copyOne, copyTwo := *identity, *identity
	script.commitUnknown = true
	consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
	view, err := service.Execute(context.Background(), &copyOne, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
	var unknown *CommitUnknownError
	if view != nil || !errors.As(err, &unknown) || unknown.PublicRef != identity.PublicRef || consumer.calls != 1 {
		t.Fatalf("commit unknown=%#v %#v calls=%d", view, err, consumer.calls)
	}
	if strings.Contains(err.Error(), "private") || script.beginCount != 1 || !executeStateIsPristine(script.state) {
		t.Fatalf("commit unknown leaked/replayed: %v %#v", err, script.state)
	}
	if !operationLeaseCleared(identity) {
		t.Fatal("commit unknown retained caller lease")
	}
	for name, retry := range map[string]*OperationIdentity{"original": identity, "copy": &copyTwo} {
		if retryView, retryErr := service.Execute(context.Background(), retry, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); retryView != nil || !errors.Is(retryErr, ErrActionOperationUnavailable) || consumer.calls != 1 {
			t.Fatalf("%s replay = %#v %v calls=%d", name, retryView, retryErr, consumer.calls)
		}
	}
}

func executeStateIsPristine(state actionExecuteState) bool {
	return state.operation.State == models.OperationProcessing && state.verification.ConsumedAt == nil && state.verification.IsDeleted == 0 &&
		state.effects == 0 && state.callbackAudits == 0 && state.callbackOutbox == 0 && state.officialAudits == 0 && state.officialOutbox == 0
}

func TestActionExecuteRejectsWrongLeaseExpiredAndNonProcessing(t *testing.T) {
	mutations := []func(*actionExecuteScript, *OperationIdentity){
		func(_ *actionExecuteScript, identity *OperationIdentity) {
			identity.capability.mu.Lock()
			identity.capability.raw[0] ^= 0xff
			identity.capability.mu.Unlock()
		},
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
		mutate(script, identity)
		consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
		view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
		if view != nil || err == nil || consumer.calls != 0 || script.state.effects != 0 {
			t.Fatalf("invalid identity executed: %#v %v %#v", view, err, script.state)
		}
		if !operationLeaseCleared(identity) {
			t.Fatal("identity rejection retained caller lease")
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
		mutate(service, script, identity)
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
		if view != nil || !errors.Is(err, ErrActionOperationUnavailable) || strings.Contains(err.Error(), "private") || consumer.calls != 1 || !executeStateIsPristine(script.state) {
			t.Fatalf("callback failure leaked/committed: %#v %v %#v", view, err, script.state)
		}
	}
}

func TestActionExecuteRejectsDatabaseIdentityDriftWithoutConsumer(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*actionExecuteScript)
	}{
		{name: "user guid", mutate: func(script *actionExecuteScript) { script.actor.Guid++ }},
		{name: "auth version", mutate: func(script *actionExecuteScript) { script.actor.AuthVersion++ }},
		{name: "session version", mutate: func(script *actionExecuteScript) { script.session.SessionVersion++ }},
		{name: "session sid", mutate: func(script *actionExecuteScript) { script.session.SID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, script, identity := actionExecuteFixture(t)
			tc.mutate(script)
			consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
			view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
			if view != nil || !errors.Is(err, ErrActionOperationForbidden) || consumer.calls != 0 || !executeStateIsPristine(script.state) || script.rollbackCount != 1 {
				t.Fatalf("identity drift executed: %#v %v calls=%d state=%#v rollbacks=%d", view, err, consumer.calls, script.state, script.rollbackCount)
			}
			if !operationLeaseCleared(identity) {
				t.Fatal("database identity drift retained caller lease")
			}
		})
	}
}

func TestActionExecuteRejectsMutatedOrMissingPrivateClaims(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OperationIdentity)
		want   error
	}{
		{name: "missing", mutate: func(identity *OperationIdentity) { identity.actor = ActionActor{} }, want: ErrActionOperationUnavailable},
		{name: "user id", mutate: func(identity *OperationIdentity) { identity.actor.UserID = 0 }, want: ErrActionOperationUnavailable},
		{name: "user guid", mutate: func(identity *OperationIdentity) { identity.actor.UserGUID++ }, want: ErrActionOperationForbidden},
		{name: "auth version", mutate: func(identity *OperationIdentity) { identity.actor.AuthVersion++ }, want: ErrActionOperationForbidden},
		{name: "session sid", mutate: func(identity *OperationIdentity) { identity.actor.SessionSID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" }, want: ErrActionOperationForbidden},
		{name: "session version", mutate: func(identity *OperationIdentity) { identity.actor.SessionVersion++ }, want: ErrActionOperationForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, script, identity := actionExecuteFixture(t)
			tc.mutate(identity)
			consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
			view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
			if view != nil || !errors.Is(err, tc.want) || consumer.calls != 0 || !executeStateIsPristine(script.state) {
				t.Fatalf("private claims accepted: %#v %v want=%v calls=%d state=%#v", view, err, tc.want, consumer.calls, script.state)
			}
			if !operationLeaseCleared(identity) {
				t.Fatal("private claim rejection retained caller lease")
			}
		})
	}
}

type executeRecordingRedis struct {
	redis.UniversalClient
	mu      sync.Mutex
	keys    []string
	revoked bool
	err     error
}

func (client *executeRecordingRedis) Exists(ctx context.Context, keys ...string) *redis.IntCmd {
	client.mu.Lock()
	client.keys = append(client.keys, keys...)
	client.mu.Unlock()
	cmd := redis.NewIntCmd(ctx)
	if client.err != nil {
		cmd.SetErr(client.err)
	} else if client.revoked {
		cmd.SetVal(1)
	}
	return cmd
}

func TestActionExecuteRedisRevocationUsesBoundSIDAndFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name           string
		revoked        bool
		err            error
		want           error
		wantBeginCount int
	}{
		{name: "revoked", revoked: true, want: ErrActionOperationForbidden},
		{name: "error", err: errors.New("private redis failure"), want: ErrActionOperationUnavailable},
		{name: "healthy then database sid drift", want: ErrActionOperationForbidden, wantBeginCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, script, identity := actionExecuteFixture(t)
			client := &executeRecordingRedis{UniversalClient: service.authRedis.client, revoked: tc.revoked, err: tc.err}
			service.authRedis.client = client
			script.session.SID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
			view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
			if view != nil || !errors.Is(err, tc.want) || consumer.calls != 0 || script.beginCount != tc.wantBeginCount || !executeStateIsPristine(script.state) {
				t.Fatalf("Redis case=%#v %v", view, err)
			}
			if !operationLeaseCleared(identity) {
				t.Fatal("Redis path retained caller lease")
			}
			wantKey := service.authRedis.revokedKey(identity.actor.SessionSID)
			if len(client.keys) != 1 || client.keys[0] != wantKey || strings.Contains(client.keys[0], identity.actor.SessionSID) {
				t.Fatalf("Redis used wrong/raw SID key: %v", client.keys)
			}
			if strings.Contains(err.Error(), identity.actor.SessionSID) || strings.Contains(strings.Join(script.queries, "\n"), identity.actor.SessionSID) {
				t.Fatal("raw SID leaked to SQL or error")
			}
		})
	}
}

func TestActionExecuteNilPrevalidationAndConsumedIdentityCannotReplay(t *testing.T) {
	service, script, identity := actionExecuteFixture(t)
	consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
	if view, err := service.Execute(context.Background(), nil, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); view != nil || !errors.Is(err, ErrActionOperationUnavailable) || consumer.calls != 0 || script.beginCount != 0 {
		t.Fatalf("nil identity = %#v %v", view, err)
	}
	invalidService, invalidScript, invalid := actionExecuteFixture(t)
	invalid.ID = 0
	if view, err := invalidService.Execute(context.Background(), invalid, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); view != nil || !errors.Is(err, ErrActionOperationUnavailable) || !operationLeaseCleared(invalid) || consumer.calls != 0 || invalidScript.beginCount != 0 {
		t.Fatalf("prevalidation = %#v %v", view, err)
	}
	manual := &OperationIdentity{ID: identity.ID, PublicRef: identity.PublicRef, actor: identity.actor}
	if view, err := service.Execute(context.Background(), manual, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); view != nil || !errors.Is(err, ErrActionOperationUnavailable) || consumer.calls != 0 || script.beginCount != 0 {
		t.Fatalf("manual identity = %#v %v", view, err)
	}
	zeroCapability := &OperationIdentity{ID: identity.ID, PublicRef: identity.PublicRef, actor: identity.actor, capability: &operationLeaseCapability{}}
	if view, err := service.Execute(context.Background(), zeroCapability, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); view != nil || !errors.Is(err, ErrActionOperationUnavailable) || !operationLeaseCleared(zeroCapability) || consumer.calls != 0 || script.beginCount != 0 {
		t.Fatalf("zero identity = %#v %v", view, err)
	}
	firstCopy, secondCopy, thirdCopy := *identity, *identity, *identity
	if view, err := service.Execute(context.Background(), &firstCopy, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); err != nil || view == nil || consumer.calls != 1 || !operationLeaseCleared(identity) {
		t.Fatalf("first Execute = %#v %v", view, err)
	}
	for name, retry := range map[string]*OperationIdentity{"original": identity, "copy": &secondCopy, "more_copy": &thirdCopy} {
		if view, err := service.Execute(context.Background(), retry, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); view != nil || !errors.Is(err, ErrActionOperationUnavailable) || consumer.calls != 1 || !operationLeaseCleared(retry) {
			t.Fatalf("%s consumed identity replay = %#v %v calls=%d", name, view, err, consumer.calls)
		}
	}
}

func TestActionExecuteConcurrentShallowCopiesEnterConsumerOnce(t *testing.T) {
	service, script, identity := actionExecuteFixture(t)
	copyOne, copyTwo := *identity, *identity
	consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
	type executeResult struct {
		view *OperationView
		err  error
	}
	start := make(chan struct{})
	results := make(chan executeResult, 2)
	for _, candidate := range []*OperationIdentity{&copyOne, &copyTwo} {
		go func(candidate *OperationIdentity) {
			<-start
			view, err := service.Execute(context.Background(), candidate, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
			results <- executeResult{view: view, err: err}
		}(candidate)
	}
	close(start)
	succeeded, rejected := 0, 0
	for range 2 {
		result := <-results
		if result.err == nil && result.view != nil && result.view.Status == "succeeded" {
			succeeded++
		} else if result.view == nil && errors.Is(result.err, ErrActionOperationUnavailable) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent result: %#v %v", result.view, result.err)
		}
	}
	if succeeded != 1 || rejected != 1 || consumer.calls != 1 || script.beginCount != 1 || !operationLeaseCleared(identity) {
		t.Fatalf("concurrent Execute succeeded=%d rejected=%d calls=%d begins=%d", succeeded, rejected, consumer.calls, script.beginCount)
	}
}

func operationLeaseCleared(identity *OperationIdentity) bool {
	if identity == nil || identity.capability == nil {
		return true
	}
	identity.capability.mu.Lock()
	defer identity.capability.mu.Unlock()
	return identity.capability.consumed && operationLeaseIsZero(&identity.capability.raw)
}

func TestActionExecuteScriptRejectsWeakSelectorsWrongArgsAndLockOrder(t *testing.T) {
	_, script, _ := actionExecuteFixture(t)
	named := func(values ...any) []driver.NamedValue {
		result := make([]driver.NamedValue, len(values))
		for i := range values {
			result[i] = driver.NamedValue{Ordinal: i + 1, Value: values[i]}
		}
		return result
	}
	queryCases := []struct {
		name  string
		kind  string
		step  int
		query string
		args  []driver.NamedValue
	}{
		{name: "actor lacks lock", kind: "actor", query: "SELECT * FROM `users` WHERE id = ? LIMIT ?", args: named(script.actor.ID, int64(1))},
		{name: "session raw sid", kind: "session", step: 1, query: "SELECT * FROM `user_sessions` WHERE sid = ? FOR UPDATE", args: named(script.session.SID)},
		{name: "operation wrong id", kind: "operation", step: 2, query: "SELECT * FROM `admin_operations` WHERE id = ? LIMIT ? FOR UPDATE", args: named(int64(99), int64(1))},
		{name: "verification lacks lock", kind: "verification", step: 3, query: "SELECT * FROM `admin_action_verifications` WHERE id = ? LIMIT ?", args: named(script.state.verification.ID, int64(1))},
		{name: "target wrong guid", kind: "target", step: 4, query: "SELECT * FROM `users` WHERE guid = ? AND is_deleted = 0 LIMIT ? FOR UPDATE", args: named(int64(99), int64(1))},
		{name: "policy wrong actor", kind: "policy", step: 5, query: "SELECT `id` FROM `user_permission_heads` WHERE user_id = ? LIMIT ? FOR UPDATE", args: named(int64(99), int64(1))},
	}
	for _, tc := range queryCases {
		t.Run(tc.name, func(t *testing.T) {
			tx := &actionExecuteTx{state: script.state, lockStep: tc.step}
			if err := validateExecuteQuery(script, tx, tc.kind, tc.query, tc.args); err == nil {
				t.Fatal("weak selector accepted")
			}
		})
	}
	tx := &actionExecuteTx{state: script.state}
	actorQuery := "SELECT `id`,`guid`,`password_hash`,`role`,`status`,`is_deleted`,`auth_version` FROM `users` WHERE id = ? ORDER BY `users`.`id` LIMIT ? FOR UPDATE"
	if err := validateExecuteQuery(script, tx, "actor", actorQuery, named(script.actor.ID, int64(1))); err != nil {
		t.Fatal(err)
	}
	operationQuery := "SELECT * FROM `admin_operations` WHERE id = ? ORDER BY `admin_operations`.`id` LIMIT ? FOR UPDATE"
	if err := validateExecuteQuery(script, tx, "operation", operationQuery, named(script.state.operation.ID, int64(1))); err == nil {
		t.Fatal("out-of-order lock accepted")
	}

	execTx := &actionExecuteTx{state: script.state}
	badConsume := "UPDATE `admin_action_verifications` SET `consumed_at`=?,`is_deleted`=?,`updated_at`=?,`updated_by`=? WHERE id = ? AND is_deleted = 0"
	if err := validateExecuteExec(script, execTx, "consume", badConsume, named(script.now, int64(1), script.now, script.actor.ID, script.state.verification.ID)); err == nil {
		t.Fatal("weakened consume accepted")
	}
	goodConsume := "UPDATE `admin_action_verifications` SET `consumed_at`=?,`is_deleted`=?,`updated_at`=?,`updated_by`=? WHERE id = ? AND consumed_at IS NULL AND expires_at > ? AND is_deleted = 0"
	if err := validateExecuteExec(script, execTx, "consume", goodConsume, named(script.now, int64(1), script.now, script.actor.ID, script.state.verification.ID, script.now+1)); err == nil {
		t.Fatal("wrong consume finalNow accepted")
	}
	badTerminal := "UPDATE `admin_operations` SET `state`=? WHERE id = ? AND state = ?"
	if err := validateExecuteExec(script, execTx, "terminal_success", badTerminal, named(int64(models.OperationSucceeded), script.state.operation.ID, int64(models.OperationProcessing))); err == nil {
		t.Fatal("weakened terminal selector accepted")
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

func TestActionExecuteRealMySQLSuccessRejectionFaultsAndCommitUnknown(t *testing.T) {
	cases := []struct {
		name                 string
		outcome              TerminalOutcome
		consumerErr          error
		failAudit            bool
		failOutbox           bool
		cancelBeforeTerminal bool
		wantState            models.AdminOperationState
		wantEffect           int64
		wantCallback         int64
		wantOfficial         int64
		wantErr              error
	}{
		{name: "success", outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}, wantState: models.OperationSucceeded, wantEffect: 1, wantCallback: 1, wantOfficial: 1},
		{name: "known_rejection", outcome: TerminalOutcome{Failure: func() *models.AdminOperationFailure { v := models.FailureActionRejected; return &v }(), HTTPStatus: 409}, wantState: models.OperationFailed, wantOfficial: 1},
		{name: "consumer_effect_fault", outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}, consumerErr: errors.New("fixture effect fault"), wantState: models.OperationProcessing, wantErr: ErrActionOperationUnavailable},
		{name: "official_audit_fault", outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}, failAudit: true, wantState: models.OperationProcessing, wantErr: ErrActionOperationUnavailable},
		{name: "official_outbox_fault", outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}, failOutbox: true, wantState: models.OperationProcessing, wantErr: ErrActionOperationUnavailable},
		{name: "terminal_update_fault", outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}, cancelBeforeTerminal: true, wantState: models.OperationProcessing, wantErr: ErrActionOperationUnavailable},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := int64(1_800_300_000_000 + index*1_000_000)
			fixture := openRealActionFixture(t, now)
			setupRealActionPrimitiveTables(t, fixture.db)
			identity, _ := prepareRealActionOperation(t, fixture, "execute-"+tc.name, fmt.Sprintf("203.0.113.%d", 130+index))
			consumer := &fixtureActionConsumer{outcome: tc.outcome, err: tc.consumerErr}
			var audit TransactionalAuditWriter = &fixtureActionAuditWriter{}
			var outbox TransactionalOutboxWriter = &fixtureActionOutboxWriter{}
			if tc.failAudit {
				audit = realFailingAuditWriter{err: errors.New("fixture audit fault")}
			}
			if tc.failOutbox {
				outbox = realFailingOutboxWriter{err: errors.New("fixture outbox fault")}
			}
			ctx := context.Background()
			if tc.cancelBeforeTerminal {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				outbox = realCancelOutboxWriter{delegate: outbox, cancel: cancel}
			}
			view, err := fixture.operation.Execute(ctx, identity, consumer, audit, outbox)
			if tc.wantErr != nil {
				if view != nil || !errors.Is(err, tc.wantErr) {
					t.Fatalf("Execute view=%v err=%v", view, err)
				}
			} else if err != nil || view == nil || view.Status != map[models.AdminOperationState]string{models.OperationSucceeded: "succeeded", models.OperationFailed: "failed"}[tc.wantState] {
				t.Fatalf("Execute view=%v err=%v", view, err)
			}
			var stored models.AdminOperation
			if err := fixture.db.First(&stored, identity.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.State != tc.wantState {
				t.Fatalf("stored state=%v want=%v", stored.State, tc.wantState)
			}
			assertRealPrimitiveCounts(t, fixture.db, tc.wantEffect, tc.wantCallback, tc.wantOfficial)
		})
	}

	t.Run("commit_unknown_zero_replay", func(t *testing.T) {
		fixture := openRealActionFixture(t, 1_800_310_000_000)
		setupRealActionPrimitiveTables(t, fixture.db)
		identity, _ := prepareRealActionOperation(t, fixture, "execute-commit-unknown", "203.0.113.140")
		consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
		audit, outbox := &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}
		runner := &realCommitUnknownRunner{}
		view, err := fixture.operation.executeWithRunner(context.Background(), identity, consumer, audit, outbox, runner)
		var unknown *CommitUnknownError
		if view != nil || !errors.As(err, &unknown) || unknown.PublicRef != identity.PublicRef || runner.calls.Load() != 1 || consumer.calls != 1 {
			t.Fatalf("commit unknown view=%v err=%v runner=%d consumer=%d", view, err, runner.calls.Load(), consumer.calls)
		}
		assertRealPrimitiveCounts(t, fixture.db, 1, 1, 1)
		if replay, replayErr := fixture.operation.Execute(context.Background(), identity, consumer, audit, outbox); replay != nil || !errors.Is(replayErr, ErrActionOperationUnavailable) || consumer.calls != 1 {
			t.Fatalf("commit unknown replay view=%v err=%v calls=%d", replay, replayErr, consumer.calls)
		}
	})
}

func TestActionExecuteRealMySQLLockAndConsumeFaultMatrix(t *testing.T) {
	type faultCase struct {
		name          string
		queryTable    string
		updateTable   string
		completedLock []string
	}
	cases := []faultCase{
		{name: "actor_lock", queryTable: "users"},
		{name: "session_lock", queryTable: "user_sessions", completedLock: []string{"users"}},
		{name: "operation_lock", queryTable: "admin_operations", completedLock: []string{"users", "user_sessions"}},
		{name: "verification_lock", queryTable: "admin_action_verifications", completedLock: []string{"users", "user_sessions", "admin_operations"}},
		{name: "ticket_consume_update", updateTable: "admin_action_verifications", completedLock: []string{"users", "user_sessions", "admin_operations", "admin_action_verifications", "users", "user_permission_heads", "user_permission_overrides", "user_permission_heads", "user_permission_overrides"}},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := openRealActionFixture(t, 1_800_325_000_000+int64(index)*1_000_000)
			setupRealActionPrimitiveTables(t, fixture.db)
			identity, _ := prepareRealActionOperation(t, fixture, "execute-fault-"+tc.name, fmt.Sprintf("198.18.12.%d", index+1))
			var before models.AdminOperation
			if err := fixture.db.First(&before, identity.ID).Error; err != nil {
				t.Fatal(err)
			}
			if before.LeaseOwnerHMAC == nil || before.LeaseExpiresAt == nil || before.VerificationID == nil {
				t.Fatal("prepared operation lacks lease or verification binding")
			}
			var beforeVerification models.AdminActionVerification
			if err := fixture.db.First(&beforeVerification, *before.VerificationID).Error; err != nil {
				t.Fatal(err)
			}

			sentinel := errors.New("fixture execute stage unavailable")
			callbackName := fmt.Sprintf("b1e_execute_fault_%d", testSnowflake.Next())
			completed := make([]string, 0, len(tc.completedLock))
			hit := 0
			probe := func(tx *gorm.DB) {
				if err := tx.Session(&gorm.Session{NewDB: true}).Exec("INSERT INTO fixture_action_rollback_probes (stage) VALUES (?)", tc.name).Error; err != nil {
					tx.AddError(err)
					return
				}
				hit++
				tx.AddError(sentinel)
			}
			queryBefore := func(tx *gorm.DB) {
				table := tx.Statement.Table
				if table == "" && tx.Statement.Schema != nil {
					table = tx.Statement.Schema.Table
				}
				if tc.queryTable == table && hit == 0 {
					if _, ok := tx.Statement.Clauses["FOR"]; !ok {
						tx.AddError(errors.New("fixture fault did not target a locking query"))
						return
					}
					probe(tx)
				}
			}
			queryAfter := func(tx *gorm.DB) {
				if tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
					return
				}
				table := tx.Statement.Table
				if table == "" && tx.Statement.Schema != nil {
					table = tx.Statement.Schema.Table
				}
				completed = append(completed, table)
			}
			updateBefore := func(tx *gorm.DB) {
				table := tx.Statement.Table
				if table == "" && tx.Statement.Schema != nil {
					table = tx.Statement.Schema.Table
				}
				if tc.updateTable != table || hit != 0 {
					return
				}
				values, ok := tx.Statement.Dest.(map[string]any)
				if !ok || values["consumed_at"] == nil || fmt.Sprint(values["is_deleted"]) != "1" {
					tx.AddError(errors.New("fixture fault did not target ticket consumption"))
					return
				}
				probe(tx)
			}
			if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName+"_before", queryBefore); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName+"_after", queryAfter); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName+"_update", updateBefore); err != nil {
				t.Fatal(err)
			}
			removed := false
			removeCallbacks := func() {
				if removed {
					return
				}
				removed = true
				_ = fixture.db.Callback().Query().Remove(callbackName + "_before")
				_ = fixture.db.Callback().Query().Remove(callbackName + "_after")
				_ = fixture.db.Callback().Update().Remove(callbackName + "_update")
			}
			t.Cleanup(removeCallbacks)

			consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
			audit, outbox := &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}
			view, err := fixture.operation.Execute(context.Background(), identity, consumer, audit, outbox)
			removeCallbacks()
			if view != nil || !errors.Is(err, ErrActionOperationUnavailable) || err.Error() != ErrActionOperationUnavailable.Error() {
				t.Fatalf("Execute fault view=%v err=%v", view, err)
			}
			if hit != 1 {
				t.Fatalf("fault hit count=%d", hit)
			}
			if strings.Join(completed, ",") != strings.Join(tc.completedLock, ",") {
				t.Fatalf("completed locks=%v want=%v", completed, tc.completedLock)
			}
			if consumer.calls != 0 || audit.calls != 0 || outbox.calls != 0 {
				t.Fatalf("post-fault callbacks consumer/audit/outbox=%d/%d/%d", consumer.calls, audit.calls, outbox.calls)
			}
			var probeCount int64
			if err := fixture.db.Table("fixture_action_rollback_probes").Count(&probeCount).Error; err != nil || probeCount != 0 {
				t.Fatalf("outer rollback probe count=%d err=%v", probeCount, err)
			}
			var after models.AdminOperation
			if err := fixture.db.First(&after, before.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.State != models.OperationProcessing || after.LeaseOwnerHMAC == nil || after.LeaseExpiresAt == nil ||
				*after.LeaseOwnerHMAC != *before.LeaseOwnerHMAC || *after.LeaseExpiresAt != *before.LeaseExpiresAt {
				t.Fatalf("operation changed after rollback: %#v", after)
			}
			var verification models.AdminActionVerification
			if err := fixture.db.Unscoped().First(&verification, beforeVerification.ID).Error; err != nil {
				t.Fatal(err)
			}
			if verification.ConsumedAt != nil || verification.IsDeleted != 0 {
				t.Fatalf("verification consumed after rollback: %#v", verification)
			}
			assertRealPrimitiveCounts(t, fixture.db, 0, 0, 0)
		})
	}
}

func TestActionExecuteConcurrencyRealMySQLOneShotCapability(t *testing.T) {
	fixture := openRealActionFixture(t, 1_800_320_000_000)
	setupRealActionPrimitiveTables(t, fixture.db)
	identity, _ := prepareRealActionOperation(t, fixture, "execute-concurrent", "203.0.113.141")
	copyIdentity := *identity
	consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
	audit, outbox := &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}
	type result struct {
		view *OperationView
		err  error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, candidate := range []*OperationIdentity{identity, &copyIdentity} {
		wg.Add(1)
		go func(value *OperationIdentity) {
			defer wg.Done()
			view, err := fixture.operation.Execute(context.Background(), value, consumer, audit, outbox)
			results <- result{view: view, err: err}
		}(candidate)
	}
	wg.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for got := range results {
		if got.err == nil && got.view != nil && got.view.Status == "succeeded" {
			succeeded++
		} else if got.view == nil && errors.Is(got.err, ErrActionOperationUnavailable) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent Execute result: view=%v err=%v", got.view, got.err)
		}
	}
	if succeeded != 1 || rejected != 1 || consumer.calls != 1 {
		t.Fatalf("concurrent Execute succeeded=%d rejected=%d consumer=%d", succeeded, rejected, consumer.calls)
	}
	assertRealPrimitiveCounts(t, fixture.db, 1, 1, 1)
}

func TestActionExecuteRealFreshIdentityPolicyAndTargetChangesRejectBeforeConsumer(t *testing.T) {
	type mutationCase struct {
		name    string
		prepare func(*testing.T, *realActionFixture)
		mutate  func(*testing.T, *realActionFixture)
	}
	cases := []mutationCase{
		{name: "actor_auth_version_changed", mutate: func(t *testing.T, fixture *realActionFixture) {
			if err := fixture.db.Model(&models.User{}).Where("id = ?", fixture.actorRow.ID).Update("auth_version", fixture.actorRow.AuthVersion+1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "session_version_changed", mutate: func(t *testing.T, fixture *realActionFixture) {
			if err := fixture.db.Model(&models.Session{}).Where("id = ?", fixture.sessionRow.ID).Update("session_version", fixture.sessionRow.SessionVersion+1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "policy_allow_to_deny", prepare: func(t *testing.T, fixture *realActionFixture) {
			if err := fixture.db.Model(&models.User{}).Where("id = ?", fixture.actorRow.ID).Update("role", models.UserRoleAdmin).Error; err != nil {
				t.Fatal(err)
			}
			head := models.PermissionPolicyHead{AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: fixture.clock.NowMillis(), UpdatedAt: fixture.clock.NowMillis()}, UserID: fixture.actorRow.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
			if err := fixture.db.Create(&head).Error; err != nil {
				t.Fatal(err)
			}
			rule := models.PermissionOverride{AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: fixture.clock.NowMillis(), UpdatedAt: fixture.clock.NowMillis()}, UserID: fixture.actorRow.ID, PolicyVersion: 1, Capability: 12, Effect: 2}
			if err := fixture.db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
		}, mutate: func(t *testing.T, fixture *realActionFixture) {
			if err := fixture.db.Model(&models.PermissionOverride{}).Where("user_id = ? AND capability = ?", fixture.actorRow.ID, 12).Update("effect", 3).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "target_hierarchy_changed", mutate: func(t *testing.T, fixture *realActionFixture) {
			if err := fixture.db.Model(&models.User{}).Where("id = ?", fixture.targetRow.ID).Update("role", models.UserRoleRoot).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "target_soft_deleted_hidden", mutate: func(t *testing.T, fixture *realActionFixture) {
			if err := fixture.db.Model(&models.User{}).Where("id = ?", fixture.targetRow.ID).Update("is_deleted", 1).Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := openRealActionFixture(t, 1_800_330_000_000+int64(index)*1_000_000)
			setupRealActionPrimitiveTables(t, fixture.db)
			if tc.prepare != nil {
				tc.prepare(t, fixture)
			}
			identity, key := prepareRealActionOperation(t, fixture, "fresh-"+tc.name, fmt.Sprintf("198.18.10.%d", index+1))
			tc.mutate(t, fixture)
			intent := testNoopIntent(fixture.targetRow.Guid, "fresh-"+tc.name)
			if replayIdentity, replayView, err := fixture.operation.Begin(context.Background(), OperationBegin{Action: realFixtureAction, Actor: fixture.actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{"av_" + strings.Repeat("A", 43)}, Intent: intent}); replayIdentity != nil || replayView != nil || err == nil {
				t.Fatalf("Begin accepted stale facts: identity=%v view=%v err=%v", replayIdentity, replayView, err)
			}
			if view, err := fixture.operation.Query(context.Background(), realFixtureAction, fixture.actor, []string{key}); view != nil || err == nil {
				t.Fatalf("Query accepted stale facts: view=%v err=%v", view, err)
			}
			consumer := &fixtureActionConsumer{outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
			if view, err := fixture.operation.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{}); view != nil || err == nil {
				t.Fatalf("Execute accepted stale facts: view=%v err=%v", view, err)
			}
			if consumer.calls != 0 {
				t.Fatalf("consumer calls=%d", consumer.calls)
			}
			assertRealPrimitiveCounts(t, fixture.db, 0, 0, 0)
			var operation models.AdminOperation
			if err := fixture.db.First(&operation, identity.ID).Error; err != nil || operation.State != models.OperationProcessing {
				t.Fatalf("operation side effect state=%v err=%v", operation.State, err)
			}
		})
	}
}

func TestActionExecuteRealTargetStatusAndVersionChangesAreConsumerRejectionsWithoutBusinessEffect(t *testing.T) {
	for index, fact := range []string{"status", "version"} {
		t.Run(fact, func(t *testing.T) {
			fixture := openRealActionFixture(t, 1_800_340_000_000+int64(index)*1_000_000)
			setupRealActionPrimitiveTables(t, fixture.db)
			identity, _ := prepareRealActionOperation(t, fixture, "target-"+fact, fmt.Sprintf("198.18.11.%d", index+1))
			if fact == "status" {
				if err := fixture.db.Model(&models.User{}).Where("id = ?", fixture.targetRow.ID).Update("status", models.UserStatusDisabled).Error; err != nil {
					t.Fatal(err)
				}
			} else if err := fixture.db.Model(&models.User{}).Where("id = ?", fixture.targetRow.ID).Update("auth_version", fixture.targetRow.AuthVersion+1).Error; err != nil {
				t.Fatal(err)
			}
			consumer := &realTargetFactsConsumer{targetGUID: fixture.targetRow.Guid, wantStatus: fixture.targetRow.Status, wantVersion: fixture.targetRow.AuthVersion}
			view, err := fixture.operation.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
			if err != nil || view == nil || view.Status != "failed" || consumer.calls != 1 {
				t.Fatalf("target %s rejection view=%v err=%v calls=%d", fact, view, err, consumer.calls)
			}
			assertRealPrimitiveCounts(t, fixture.db, 0, 0, 1)
		})
	}
}
