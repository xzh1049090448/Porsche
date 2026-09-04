package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const actionOperationScriptDriverName = "porsche_action_operation_script"

var (
	actionOperationScriptOnce sync.Once
	actionOperationScriptSeq  atomic.Uint64
	actionOperationScripts    sync.Map
)

type actionOperationScript struct {
	mu            sync.Mutex
	now           int64
	keyHex        string
	actor         models.User
	target        *models.User
	sessions      []models.Session
	operation     *models.AdminOperation
	verification  *models.AdminActionVerification
	policyHead    *models.PermissionPolicyHead
	overrides     []models.PermissionOverride
	queries       []string
	queryArgs     [][]driver.NamedValue
	execs         []string
	execArgs      [][]driver.NamedValue
	beginCount    int
	commitCount   int
	rollbackCount int
	isolations    []driver.IsolationLevel
	failExec      bool
	failExecAt    int
	execError     error
	zeroAffected  bool
}

type actionOperationDriver struct{}
type actionOperationConn struct{ script *actionOperationScript }
type actionOperationTx struct{ script *actionOperationScript }
type actionOperationRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}
type actionOperationResult struct{ id, affected int64 }

func (actionOperationDriver) Open(name string) (driver.Conn, error) {
	value, ok := actionOperationScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown action operation script")
	}
	return &actionOperationConn{script: value.(*actionOperationScript)}, nil
}
func (c *actionOperationConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare disabled")
}
func (c *actionOperationConn) Close() error { return nil }
func (c *actionOperationConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *actionOperationConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if opts.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		return nil, errors.New("operation transaction is not READ COMMITTED")
	}
	c.script.mu.Lock()
	c.script.beginCount++
	c.script.isolations = append(c.script.isolations, opts.Isolation)
	c.script.mu.Unlock()
	return &actionOperationTx{script: c.script}, nil
}

func TestActionOperationDBScriptRejectsWrongSelectorsAndIsolation(t *testing.T) {
	script := &actionOperationScript{actor: models.User{ID: 10}, now: 100, keyHex: strings.Repeat("a", 64), operation: &models.AdminOperation{ID: 30}}
	conn := &actionOperationConn{script: script}
	badQueries := []struct {
		query string
		args  []driver.NamedValue
	}{
		{query: "SELECT * FROM `admin_operations` WHERE actor_user_id = ? AND action = ? AND idempotency_key_hmac = ? AND is_deleted = 0 LIMIT ? FOR UPDATE", args: []driver.NamedValue{{Value: int64(10)}, {Value: int64(testNoopAction)}, {Value: script.keyHex}, {Value: int64(1)}}},
		{query: "SELECT * FROM `admin_operations` WHERE actor_user_id = ? AND action = ? AND idempotency_key_hmac = ? LIMIT ? FOR UPDATE", args: []driver.NamedValue{{Value: int64(99)}, {Value: int64(testNoopAction)}, {Value: script.keyHex}, {Value: int64(1)}}},
		{query: "SELECT * FROM `admin_action_verifications` WHERE consumed_at IS NULL FOR UPDATE", args: nil},
		{query: "SELECT * FROM `user_sessions` WHERE sid = ? FOR UPDATE", args: []driver.NamedValue{{Value: "raw-secret"}}},
	}
	for _, tc := range badQueries {
		if _, err := conn.QueryContext(context.Background(), tc.query, tc.args); err == nil {
			t.Fatalf("script accepted malformed selector %q", tc.query)
		}
	}
	if _, err := conn.BeginTx(context.Background(), driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSerializable)}); err == nil {
		t.Fatal("script accepted non-READ-COMMITTED transaction")
	}
	lease := int64(1)
	script.operation = &models.AdminOperation{ID: 30, State: models.OperationProcessing, LeaseExpiresAt: &lease, QueryExpiresAt: 200}
	badWrites := []struct {
		query string
		args  []driver.NamedValue
	}{
		{query: "UPDATE `admin_operations` SET `verification_id`=? WHERE id = ? AND verification_id IS NULL", args: []driver.NamedValue{{Value: int64(40)}, {Value: int64(30)}}},
		{query: "UPDATE `admin_operations` SET `is_deleted`=1,`lease_owner_hmac`=NULL,`error_code`=NULL,`result_http_status`=NULL WHERE id = ? AND state = ? AND is_deleted = 0", args: []driver.NamedValue{{Value: int64(30)}, {Value: int64(models.OperationProcessing)}}},
		{query: "UPDATE `admin_operations` SET `lease_owner_hmac`=NULL,`lease_expires_at`=NULL WHERE id = ? AND state = ? AND is_deleted = 0 AND lease_expires_at = ?", args: []driver.NamedValue{{Value: int64(30)}, {Value: int64(models.OperationProcessing)}, {Value: lease}}},
	}
	for _, tc := range badWrites {
		if _, err := conn.ExecContext(context.Background(), tc.query, tc.args); err == nil {
			t.Fatalf("script accepted malformed write %q", tc.query)
		}
	}
}
func (c *actionOperationConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case *int64:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = *typed
		}
	case *int:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = int64(*typed)
		}
	case *string:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = *typed
		}
	case models.AdminOperationState:
		value.Value = int64(typed)
	case models.AdminOperationFailure:
		value.Value = int64(typed)
	case models.AdminResultKind:
		value.Value = int64(typed)
	}
	return nil
}

func (c *actionOperationConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.script
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, query)
	s.queryArgs = append(s.queryArgs, append([]driver.NamedValue(nil), args...))
	switch {
	case strings.Contains(query, "FROM `users`") && strings.Contains(query, "guid = ?"):
		if !strings.Contains(query, "is_deleted = 0") || !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("target not locked by visible guid")
		}
		columns := []string{"id", "guid", "role", "status", "is_deleted", "auth_version"}
		if s.target == nil {
			return operationRows(columns, nil), nil
		}
		u := s.target
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(u.Guid) {
			return nil, errors.New("wrong target selector vars")
		}
		return operationRows(columns, [][]driver.Value{{u.ID, u.Guid, int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}}), nil
	case strings.Contains(query, "FROM `users`"):
		if !strings.Contains(query, "id = ?") || !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("actor not locked by id")
		}
		u := s.actor
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(u.ID) {
			return nil, errors.New("wrong actor selector vars")
		}
		var password driver.Value
		if u.PasswordHash != nil {
			password = *u.PasswordHash
		}
		return operationRows([]string{"id", "guid", "password_hash", "role", "status", "is_deleted", "auth_version"}, [][]driver.Value{{u.ID, u.Guid, password, int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}}), nil
	case strings.Contains(query, "FROM `user_sessions`"):
		if strings.Contains(query, "sid = ?") || !strings.Contains(query, "user_id = ?") || !strings.Contains(query, "ORDER BY id ASC") || !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("unsafe session lock")
		}
		values := make([][]driver.Value, 0, len(s.sessions))
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.actor.ID) || fmt.Sprint(args[1].Value) != fmt.Sprint(s.now) {
			return nil, errors.New("wrong session selector vars")
		}
		for i := range s.sessions {
			row := s.sessions[i]
			var revoked driver.Value
			if row.RevokedAt != nil {
				revoked = *row.RevokedAt
			}
			values = append(values, []driver.Value{row.ID, row.Guid, row.SID, row.UserID, int64(row.SessionVersion), int64(row.IsDeleted), revoked, row.ExpiresAt})
		}
		return operationRows([]string{"id", "guid", "sid", "user_id", "session_version", "is_deleted", "revoked_at", "expires_at"}, values), nil
	case strings.Contains(query, "FROM `admin_operations`"):
		if !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("operation not locked")
		}
		if strings.Contains(query, "is_deleted = 0") && strings.Contains(query, "idempotency_key_hmac") {
			return nil, errors.New("operation lookup excluded tombstones")
		}
		if strings.Contains(query, "idempotency_key_hmac = ?") {
			if len(args) != 4 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.actor.ID) || fmt.Sprint(args[1].Value) != fmt.Sprint(int(testNoopAction)) || fmt.Sprint(args[2].Value) != s.keyHex {
				return nil, errors.New("wrong operation selector vars")
			}
		} else if strings.Contains(query, "id = ?") && (s.operation == nil || len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.operation.ID)) {
			return nil, errors.New("wrong operation id selector vars")
		}
		if s.operation == nil {
			return operationRows(operationColumns(), nil), nil
		}
		return operationRows(operationColumns(), [][]driver.Value{operationValues(*s.operation)}), nil
	case strings.Contains(query, "FROM `admin_action_verifications`"):
		if (!strings.Contains(query, "ticket_hmac = ?") && !strings.Contains(query, "id = ?")) || !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("verification not locked by digest")
		}
		if s.verification == nil {
			return operationRows(verificationColumns(), nil), nil
		}
		if len(args) < 1 {
			return nil, errors.New("missing verification selector")
		}
		if strings.Contains(query, "ticket_hmac = ?") && fmt.Sprint(args[0].Value) != s.verification.TicketHMAC {
			return operationRows(verificationColumns(), nil), nil
		}
		if strings.Contains(query, "id = ?") && fmt.Sprint(args[0].Value) != fmt.Sprint(s.verification.ID) {
			return operationRows(verificationColumns(), nil), nil
		}
		return operationRows(verificationColumns(), [][]driver.Value{verificationValues(*s.verification)}), nil
	case strings.Contains(query, "FROM `user_permission_heads`"):
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.actor.ID) {
			return nil, errors.New("wrong policy head selector vars")
		}
		columns := []string{"id", "guid", "is_deleted", "policy_version", "catalog_version", "rule_count"}
		if s.policyHead == nil {
			return operationRows(columns, nil), nil
		}
		h := s.policyHead
		return operationRows(columns, [][]driver.Value{{h.ID, h.Guid, int64(h.IsDeleted), h.PolicyVersion, int64(h.CatalogVersion), int64(h.RuleCount)}}), nil
	case strings.Contains(query, "FROM `user_permission_overrides`"):
		if len(args) < 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(s.actor.ID) {
			return nil, errors.New("wrong policy rule selector vars")
		}
		if s.policyHead == nil {
			return operationRows([]string{"id"}, nil), nil
		}
		columns := []string{"id", "guid", "is_deleted", "policy_version", "capability", "effect"}
		values := make([][]driver.Value, 0, len(s.overrides))
		for i := range s.overrides {
			r := s.overrides[i]
			values = append(values, []driver.Value{r.ID, r.Guid, int64(r.IsDeleted), r.PolicyVersion, int64(r.Capability), int64(r.Effect)})
		}
		return operationRows(columns, values), nil
	default:
		return nil, fmt.Errorf("unexpected operation query: %s", query)
	}
}

func (c *actionOperationConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s := c.script
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execs = append(s.execs, query)
	s.execArgs = append(s.execArgs, append([]driver.NamedValue(nil), args...))
	if err := validateActionOperationExec(s, query, args); err != nil {
		return nil, err
	}
	if s.failExec || (s.failExecAt > 0 && len(s.execs) == s.failExecAt) {
		if s.execError != nil {
			return nil, s.execError
		}
		return nil, errors.New("scripted operation write failure")
	}
	affected := int64(1)
	if s.zeroAffected {
		affected = 0
	}
	return actionOperationResult{id: 30, affected: affected}, nil
}

func validateActionOperationExec(script *actionOperationScript, query string, args []driver.NamedValue) error {
	requireFragments := func(fragments ...string) error {
		for _, fragment := range fragments {
			if !strings.Contains(query, fragment) {
				return fmt.Errorf("operation write missing %q", fragment)
			}
		}
		return nil
	}
	requireTail := func(expected ...any) error {
		if len(args) < len(expected) {
			return fmt.Errorf("operation write args %d, need tail %d", len(args), len(expected))
		}
		offset := len(args) - len(expected)
		for i := range expected {
			if fmt.Sprint(args[offset+i].Value) != fmt.Sprint(expected[i]) {
				return fmt.Errorf("operation write tail arg %d=%v, want %v", i, args[offset+i].Value, expected[i])
			}
		}
		return nil
	}
	requireHead := func(expected ...any) error {
		if len(args) < len(expected) {
			return fmt.Errorf("operation write args %d, need head %d", len(args), len(expected))
		}
		for i := range expected {
			if fmt.Sprint(args[i].Value) != fmt.Sprint(expected[i]) {
				return fmt.Errorf("operation write head arg %d=%v, want %v", i, args[i].Value, expected[i])
			}
		}
		return nil
	}
	switch {
	case strings.HasPrefix(query, "INSERT INTO `admin_operations`"):
		return requireFragments("`actor_user_id`", "`idempotency_key_hmac`", "`request_hmac`", "`state`", "`lease_owner_hmac`", "`lease_expires_at`", "`query_expires_at`", "`created_by`", "`updated_by`")
	case strings.HasPrefix(query, "UPDATE `admin_operations`") && strings.Contains(query, "verification_id IS NULL"):
		if err := requireFragments("SET", "`verification_id`=?", "id = ? AND state = ? AND is_deleted = 0 AND verification_id IS NULL"); err != nil {
			return err
		}
		return requireTail(int64(30), int64(models.OperationProcessing))
	case strings.HasPrefix(query, "UPDATE `admin_operations`") && strings.Contains(query, "query_expires_at <= ?"):
		if script.operation == nil {
			return errors.New("expiry write has no locked operation")
		}
		if err := requireFragments("id = ? AND state = ? AND is_deleted = 0 AND query_expires_at = ? AND query_expires_at <= ?", "`lease_owner_hmac`=?", "`error_code`=?", "`result_http_status`=?"); err != nil {
			return err
		}
		return requireTail(script.operation.ID, int64(script.operation.State), script.operation.QueryExpiresAt, script.now)
	case strings.HasPrefix(query, "UPDATE `admin_operations`"):
		if script.operation == nil || script.operation.LeaseExpiresAt == nil {
			return errors.New("recovery write has no locked lease")
		}
		if err := requireFragments("id = ? AND state = ? AND is_deleted = 0 AND lease_expires_at = ? AND lease_expires_at < ? AND query_expires_at > ?", "`lease_owner_hmac`=?", "`lease_expires_at`=?"); err != nil {
			return err
		}
		if err := requireHead(nil, nil, int64(models.OperationPendingRecovery), script.now, script.operation.ActorUserID); err != nil {
			return err
		}
		return requireTail(script.operation.ID, int64(models.OperationProcessing), *script.operation.LeaseExpiresAt, script.now-actionOperationRecoveryGraceMS, script.now)
	default:
		return fmt.Errorf("unexpected operation write: %s", query)
	}
}
func (tx *actionOperationTx) Commit() error {
	tx.script.mu.Lock()
	tx.script.commitCount++
	tx.script.mu.Unlock()
	return nil
}
func (tx *actionOperationTx) Rollback() error {
	tx.script.mu.Lock()
	tx.script.rollbackCount++
	tx.script.mu.Unlock()
	return nil
}
func (r *actionOperationRows) Columns() []string { return r.columns }
func (r *actionOperationRows) Close() error      { return nil }
func (r *actionOperationRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
func (r actionOperationResult) LastInsertId() (int64, error) { return r.id, nil }
func (r actionOperationResult) RowsAffected() (int64, error) { return r.affected, nil }

func operationRows(columns []string, values [][]driver.Value) *actionOperationRows {
	return &actionOperationRows{columns: columns, values: values}
}
func operationColumns() []string {
	return []string{"id", "guid", "created_at", "created_by", "updated_at", "updated_by", "is_deleted", "actor_user_id", "actor_auth_version", "session_id", "action", "idempotency_key_hmac", "request_hmac", "verification_id", "state", "public_ref", "lease_owner_hmac", "lease_expires_at", "finished_at", "query_expires_at", "error_code", "result_kind", "result_guid", "result_http_status"}
}
func operationValues(o models.AdminOperation) []driver.Value {
	return []driver.Value{o.ID, o.Guid, o.CreatedAt, ptrDriver(o.CreatedBy), o.UpdatedAt, ptrDriver(o.UpdatedBy), int64(o.IsDeleted), o.ActorUserID, int64(o.ActorAuthVersion), o.SessionID, int64(o.Action), o.IdempotencyKeyHMAC, o.RequestHMAC, ptrDriver(o.VerificationID), int64(o.State), o.PublicRef, ptrDriver(o.LeaseOwnerHMAC), ptrDriver(o.LeaseExpiresAt), ptrDriver(o.FinishedAt), o.QueryExpiresAt, ptrDriver(o.ErrorCode), ptrDriver(o.ResultKind), ptrDriver(o.ResultGUID), ptrDriver(o.ResultHTTPStatus)}
}
func verificationColumns() []string {
	return []string{"id", "guid", "created_at", "created_by", "updated_at", "updated_by", "is_deleted", "actor_user_id", "actor_auth_version", "session_id", "action", "target_kind", "target_guid", "intent_hmac", "ticket_hmac", "expires_at", "consumed_at"}
}
func verificationValues(v models.AdminActionVerification) []driver.Value {
	return []driver.Value{v.ID, v.Guid, v.CreatedAt, ptrDriver(v.CreatedBy), v.UpdatedAt, ptrDriver(v.UpdatedBy), int64(v.IsDeleted), v.ActorUserID, int64(v.ActorAuthVersion), v.SessionID, int64(v.Action), int64(v.TargetKind), ptrDriver(v.TargetGUID), v.IntentHMAC, v.TicketHMAC, v.ExpiresAt, ptrDriver(v.ConsumedAt)}
}
func ptrDriver(value any) driver.Value {
	switch v := value.(type) {
	case *int64:
		if v != nil {
			return *v
		}
	case *int:
		if v != nil {
			return int64(*v)
		}
	case *string:
		if v != nil {
			return *v
		}
	case *models.AdminOperationFailure:
		if v != nil {
			return int64(*v)
		}
	case *models.AdminResultKind:
		if v != nil {
			return int64(*v)
		}
	}
	return nil
}

func openActionOperationScriptDB(t *testing.T, script *actionOperationScript) *gorm.DB {
	t.Helper()
	actionOperationScriptOnce.Do(func() { sql.Register(actionOperationScriptDriverName, actionOperationDriver{}) })
	name := fmt.Sprintf("operation-%d", actionOperationScriptSeq.Add(1))
	actionOperationScripts.Store(name, script)
	t.Cleanup(func() { actionOperationScripts.Delete(name) })
	sqlDB, err := sql.Open(actionOperationScriptDriverName, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestActionOperationBeginDBScriptEnforcesLockOrderAndSecretFreeSQL(t *testing.T) {
	now := int64(1_800_000_000_000)
	service, script, actor, key, ticket := actionOperationFixture(t, now, nil)
	identity, view, err := service.Begin(context.Background(), OperationBegin{Action: testNoopAction, Actor: actor, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: "same-intent"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if identity == nil || identity.ID != 30 || identity.PublicRef == "" || identity.LeaseOwner == [32]byte{} {
		t.Fatalf("identity = %#v", identity)
	}
	if view == nil || view.Status != "processing" || view.Scope != "test.noop" || view.RetryAfterSeconds != 30 {
		t.Fatalf("view = %#v", view)
	}
	if got := actionOperationQueryKinds(script.queries); strings.Join(got, ",") != "actor,session,operation,verification,target_or_policy,target_or_policy" {
		t.Fatalf("lock order = %v", got)
	}
	if script.commitCount != 1 || script.rollbackCount != 0 {
		t.Fatalf("commit/rollback = %d/%d", script.commitCount, script.rollbackCount)
	}
	if len(script.isolations) != 1 || script.isolations[0] != driver.IsolationLevel(sql.LevelReadCommitted) {
		t.Fatalf("transaction isolation = %v", script.isolations)
	}
	joined := actionOperationSQLValues(script)
	for _, secret := range []string{actor.SessionSID, key, ticket, "same-intent"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("SQL retained raw secret %q", secret)
		}
	}
	if len(script.execs) != 2 || !strings.Contains(script.execs[0], "INSERT INTO `admin_operations`") || !strings.Contains(script.execs[1], "verification_id") {
		t.Fatalf("writes = %v", script.execs)
	}
	inserted := actionOperationInsertValues(t, script.execs[0], script.execArgs[0])
	for column, want := range map[string]string{
		"guid": "3001", "created_at": fmt.Sprint(now), "created_by": "10", "updated_at": fmt.Sprint(now), "updated_by": "10",
		"is_deleted": "0", "actor_user_id": "10", "actor_auth_version": "7", "session_id": "20", "action": fmt.Sprint(int(testNoopAction)),
		"idempotency_key_hmac": script.keyHex, "request_hmac": script.verification.IntentHMAC, "state": fmt.Sprint(int(models.OperationProcessing)),
		"lease_expires_at": fmt.Sprint(now + actionOperationLeaseMillis), "query_expires_at": fmt.Sprint(now + actionOperationQueryRetentionMS),
	} {
		if got := inserted[column]; got != want {
			t.Fatalf("INSERT %s=%q, want %q", column, got, want)
		}
	}
	if len(inserted["public_ref"]) != 46 || len(inserted["lease_owner_hmac"]) != 64 {
		t.Fatalf("public ref / lease HMAC shapes = %d/%d", len(inserted["public_ref"]), len(inserted["lease_owner_hmac"]))
	}
	updated := actionOperationUpdateValues(t, script.execs[1], script.execArgs[1])
	for column, want := range map[string]string{"verification_id": "40", "updated_at": fmt.Sprint(now), "updated_by": "10"} {
		if got := updated[column]; got != want {
			t.Fatalf("verification reservation %s=%q, want %q", column, got, want)
		}
	}
}

func actionOperationInsertValues(t *testing.T, query string, args []driver.NamedValue) map[string]string {
	t.Helper()
	open := strings.Index(query, "(")
	close := strings.Index(query, ") VALUES")
	if open < 0 || close <= open {
		t.Fatalf("unparseable INSERT: %s", query)
	}
	columns := strings.Split(query[open+1:close], ",")
	if len(columns) != len(args) {
		t.Fatalf("INSERT columns/args = %d/%d", len(columns), len(args))
	}
	out := make(map[string]string, len(columns))
	for i, column := range columns {
		out[strings.Trim(strings.TrimSpace(column), "`")] = fmt.Sprint(args[i].Value)
	}
	return out
}

func actionOperationUpdateValues(t *testing.T, query string, args []driver.NamedValue) map[string]string {
	t.Helper()
	setAt := strings.Index(query, " SET ")
	whereAt := strings.Index(query, " WHERE ")
	if setAt < 0 || whereAt <= setAt {
		t.Fatalf("unparseable UPDATE: %s", query)
	}
	assignments := strings.Split(query[setAt+5:whereAt], ",")
	if len(args) < len(assignments) {
		t.Fatalf("UPDATE assignments/args = %d/%d", len(assignments), len(args))
	}
	out := make(map[string]string, len(assignments))
	for i, assignment := range assignments {
		column := strings.Trim(strings.TrimSpace(strings.SplitN(assignment, "=", 2)[0]), "`")
		out[column] = fmt.Sprint(args[i].Value)
	}
	return out
}

func actionOperationQueryKinds(queries []string) []string {
	out := make([]string, 0, len(queries))
	for _, query := range queries {
		switch {
		case strings.Contains(query, "FROM `users`") && strings.Contains(query, "guid = ?"):
			out = append(out, "target")
		case strings.Contains(query, "FROM `users`"):
			out = append(out, "actor")
		case strings.Contains(query, "FROM `user_sessions`"):
			out = append(out, "session")
		case strings.Contains(query, "FROM `admin_operations`"):
			out = append(out, "operation")
		case strings.Contains(query, "FROM `admin_action_verifications`"):
			out = append(out, "verification")
		case strings.Contains(query, "FROM `user_permission_"):
			out = append(out, "target_or_policy")
		}
	}
	return out
}
func actionOperationSQLValues(script *actionOperationScript) string {
	var b strings.Builder
	for _, sets := range [][][]driver.NamedValue{script.queryArgs, script.execArgs} {
		for _, args := range sets {
			for _, arg := range args {
				fmt.Fprint(&b, arg.Value, "|")
			}
		}
	}
	return b.String()
}
