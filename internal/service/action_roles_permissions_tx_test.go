package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	_                              TransactionalActionConsumer           = (*rolePermissionTransactionalExecution)(nil)
	_                              actionExecutionAuthorizationPrelocker = (*rolePermissionTransactionalExecution)(nil)
	rolePermissionTxDriverOnce     sync.Once
	rolePermissionTxDriverSequence atomic.Uint64
	rolePermissionTxScripts        sync.Map
)

const rolePermissionTxDriverName = "porsche_a08_role_permission_tx"

type rolePermissionTxCall struct {
	kind, table, sql string
	args             []driver.NamedValue
}

type rolePermissionTxScript struct {
	mu            sync.Mutex
	now           int64
	actor         models.User
	target        models.User
	sessions      []models.Session
	head          *models.PermissionPolicyHead
	rules         []models.PermissionOverride
	failAt        int
	rowsAt        map[int]int64
	observed      []rolePermissionTxCall
	committed     []rolePermissionTxCall
	commitCount   int
	rollbackCount int
}

type rolePermissionTxDriver struct{}
type rolePermissionTxConn struct {
	script *rolePermissionTxScript
	tx     *rolePermissionTxDriverTx
}
type rolePermissionTxDriverTx struct {
	conn    *rolePermissionTxConn
	pending []rolePermissionTxCall
}
type rolePermissionTxRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}
type rolePermissionTxResult int64

func (rolePermissionTxDriver) Open(name string) (driver.Conn, error) {
	value, ok := rolePermissionTxScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown A08 transaction script")
	}
	return &rolePermissionTxConn{script: value.(*rolePermissionTxScript)}, nil
}

func (c *rolePermissionTxConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare disabled")
}
func (c *rolePermissionTxConn) Close() error { return nil }
func (c *rolePermissionTxConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *rolePermissionTxConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.tx != nil {
		return nil, errors.New("nested A08 transaction")
	}
	c.tx = &rolePermissionTxDriverTx{conn: c}
	return c.tx, nil
}
func (c *rolePermissionTxConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case actionsecurity.Action:
		value.Value = int64(typed)
	case models.UserRole:
		value.Value = int64(typed)
	case models.UserStatus:
		value.Value = int64(typed)
	case models.LoginMethod:
		value.Value = int64(typed)
	case models.AuthAuditEventType:
		value.Value = int64(typed)
	case *int64:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = *typed
		}
	case *models.LoginMethod:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = int64(*typed)
		}
	}
	return nil
}

func (c *rolePermissionTxConn) record(kind, query string, args []driver.NamedValue) (rolePermissionTxCall, int, bool) {
	call := rolePermissionTxCall{kind: kind, table: rolePermissionTxTable(query), sql: query, args: append([]driver.NamedValue(nil), args...)}
	c.script.mu.Lock()
	defer c.script.mu.Unlock()
	c.script.observed = append(c.script.observed, call)
	index := len(c.script.observed)
	return call, index, c.script.failAt == index
}

func (c *rolePermissionTxConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.tx == nil {
		return nil, errors.New("A08 query outside transaction")
	}
	call, index, fail := c.record("query", query, args)
	if fail {
		return nil, fmt.Errorf("injected A08 query failure %d", index)
	}
	s := c.script
	s.mu.Lock()
	defer s.mu.Unlock()
	switch call.table {
	case "users":
		u := s.target
		if strings.Contains(query, "WHERE id = ? AND is_deleted = 0") && strings.Contains(query, "FOR UPDATE") {
			u = s.actor
		}
		if u.ID <= 0 || u.IsDeleted != 0 {
			return &rolePermissionTxRows{columns: []string{"id", "guid", "role", "status", "is_deleted", "auth_version"}}, nil
		}
		return rolePermissionTxOne([]string{"id", "guid", "role", "status", "is_deleted", "auth_version"}, []driver.Value{u.ID, u.Guid, int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}), nil
	case "user_sessions":
		values := make([][]driver.Value, len(s.sessions))
		for i, row := range s.sessions {
			values[i] = []driver.Value{row.ID, row.Guid, row.SID, row.UserID, int64(row.LoginMethod), int64(row.SessionVersion), int64(row.IsDeleted), row.RevokedAt, row.ExpiresAt}
		}
		return &rolePermissionTxRows{columns: []string{"id", "guid", "sid", "user_id", "login_method", "session_version", "is_deleted", "revoked_at", "expires_at"}, values: values}, nil
	case "user_permission_heads":
		if s.head == nil || s.head.IsDeleted != 0 {
			return &rolePermissionTxRows{columns: []string{"id", "guid", "user_id", "is_deleted", "policy_version", "catalog_version", "rule_count"}}, nil
		}
		h := *s.head
		return rolePermissionTxOne([]string{"id", "guid", "user_id", "is_deleted", "policy_version", "catalog_version", "rule_count"}, []driver.Value{h.ID, h.Guid, h.UserID, int64(h.IsDeleted), h.PolicyVersion, int64(h.CatalogVersion), int64(h.RuleCount)}), nil
	case "user_permission_overrides":
		values := make([][]driver.Value, 0, len(s.rules))
		for _, row := range s.rules {
			if row.IsDeleted != 0 {
				continue
			}
			values = append(values, []driver.Value{row.ID, row.Guid, row.UserID, int64(row.IsDeleted), row.PolicyVersion, int64(row.Capability), int64(row.Effect)})
		}
		return &rolePermissionTxRows{columns: []string{"id", "guid", "user_id", "is_deleted", "policy_version", "capability", "effect"}, values: values}, nil
	default:
		return nil, fmt.Errorf("unexpected A08 query: %s", query)
	}
}

func (c *rolePermissionTxConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.tx == nil {
		return nil, errors.New("A08 write outside transaction")
	}
	call, index, fail := c.record("exec", query, args)
	if fail {
		return nil, fmt.Errorf("injected A08 write failure %d", index)
	}
	c.script.mu.Lock()
	affected, configured := c.script.rowsAt[index]
	c.script.mu.Unlock()
	if !configured {
		affected = 1
	}
	c.tx.pending = append(c.tx.pending, call)
	return rolePermissionTxResult(affected), nil
}

func (tx *rolePermissionTxDriverTx) Commit() error {
	tx.conn.script.mu.Lock()
	tx.conn.script.committed = append(tx.conn.script.committed, tx.pending...)
	tx.conn.script.commitCount++
	tx.conn.script.mu.Unlock()
	tx.conn.tx = nil
	return nil
}
func (tx *rolePermissionTxDriverTx) Rollback() error {
	tx.conn.script.mu.Lock()
	tx.conn.script.rollbackCount++
	tx.conn.script.mu.Unlock()
	tx.conn.tx = nil
	return nil
}
func (rows *rolePermissionTxRows) Columns() []string { return rows.columns }
func (rows *rolePermissionTxRows) Close() error      { return nil }
func (rows *rolePermissionTxRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}
func (result rolePermissionTxResult) LastInsertId() (int64, error) { return 1, nil }
func (result rolePermissionTxResult) RowsAffected() (int64, error) { return int64(result), nil }

func rolePermissionTxOne(columns []string, values []driver.Value) *rolePermissionTxRows {
	return &rolePermissionTxRows{columns: columns, values: [][]driver.Value{values}}
}
func rolePermissionTxTable(query string) string {
	for _, table := range []string{"auth_audit_events", "user_permission_overrides", "user_permission_heads", "user_sessions", "users"} {
		if strings.Contains(query, "`"+table+"`") {
			return table
		}
	}
	return ""
}

func newRolePermissionTxDB(t *testing.T, script *rolePermissionTxScript) (*gorm.DB, *sql.DB) {
	t.Helper()
	rolePermissionTxDriverOnce.Do(func() { sql.Register(rolePermissionTxDriverName, rolePermissionTxDriver{}) })
	dsn := fmt.Sprintf("a08-role-permission-%d", rolePermissionTxDriverSequence.Add(1))
	rolePermissionTxScripts.Store(dsn, script)
	t.Cleanup(func() { rolePermissionTxScripts.Delete(dsn) })
	root, err := sql.Open(rolePermissionTxDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	root.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = root.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: root, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db, root
}

type rolePermissionTxRevoker struct {
	mu     sync.Mutex
	failAt int
	calls  []string
	ttls   []time.Duration
}

func (r *rolePermissionTxRevoker) MarkSessionRevoked(_ context.Context, sid string, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, sid)
	r.ttls = append(r.ttls, ttl)
	if r.failAt == len(r.calls) {
		return errors.New("private redis failure")
	}
	return nil
}

func TestRolePermissionTransactionPromoteNoHeadPersistsCanonicalTransition(t *testing.T) {
	script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
	db, _ := newRolePermissionTxDB(t, script)
	revoker := &rolePermissionTxRevoker{}
	var guid atomic.Int64
	guid.Store(7000)
	base, err := NewRolePermissionExecution(rolePermissionTestDescriptor(t, actionsecurity.ActionUsersPromote), actionsecurity.PromoteIntent{
		TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1,
		Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}, {Capability: "users.delete", Effect: 3}}, Reason: "promotion",
	}, func() int64 { return guid.Add(1) }, &createAccountTestClock{now: 8001}, createAccountTestCrypto(t))
	if err != nil {
		t.Fatal(err)
	}
	execution, err := newRolePermissionTransactionalExecution(base, revoker)
	if err != nil {
		t.Fatal(err)
	}
	operation, verification := rolePermissionTxBinding(actionsecurity.ActionUsersPromote, base.requestHMAC, base.targetGUID())
	tx := db.Begin()
	locked, err := execution.prelockForAuthorization(context.Background(), tx, operation, verification)
	if err != nil || locked == nil {
		t.Fatalf("prelock: %#v/%v", locked, err)
	}
	outcome, err := execution.Execute(context.Background(), tx, operation)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Failure != nil || outcome.ResultKind != models.ResultUser || outcome.ResultGUID == nil || *outcome.ResultGUID != 6001 || outcome.ResultAuthVersion == nil || *outcome.ResultAuthVersion != 8 || outcome.ResultPermissionsVersion == nil || *outcome.ResultPermissionsVersion != 1 || outcome.ResultRole == nil || *outcome.ResultRole != models.UserRoleAdmin || outcome.HTTPStatus != 200 {
		t.Fatalf("outcome = %#v", outcome)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(revoker.calls, []string{"sid-71", "sid-72"}) {
		t.Fatalf("redis calls = %v", revoker.calls)
	}
	if !reflect.DeepEqual(revoker.ttls, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("redis TTLs = %v", revoker.ttls)
	}
	assertRolePermissionTxHappyCalls(t, script)
}

func TestRolePermissionTransactionWritesOneManagedUpdateAfterSessionRevocations(t *testing.T) {
	script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
	outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{
		TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "audit transition",
	}, 0, nil)
	if err != nil || outcome.Failure != nil {
		t.Fatalf("transition = %#v/%v", outcome, err)
	}
	audits := committedCalls(script, "auth_audit_events", "INSERT")
	if len(audits) != len(script.sessions)+1 {
		t.Fatalf("auth audit inserts = %d, want %d", len(audits), len(script.sessions)+1)
	}
	last := rolePermissionArgValues(audits[len(audits)-1].args)
	if len(last) < 10 || fmt.Sprint(last[6:10]) != fmt.Sprintf("[%d <nil> %d <nil>]", script.target.ID, models.AuthAuditEventManagedUserUpdated) {
		t.Fatalf("managed role/policy event args = %v", last)
	}
}

func TestRolePermissionTransactionActorIDTargetGUIDNamespaceCollisionSucceeds(t *testing.T) {
	script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
	script.sessions = nil
	script.target.Guid = 41
	outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 41, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "namespace collision"}, 0, nil)
	if err != nil || outcome.Failure != nil || outcome.ResultGUID == nil || *outcome.ResultGUID != 41 || outcome.ResultRole == nil || *outcome.ResultRole != models.UserRoleAdmin {
		t.Fatalf("namespace collision = %#v/%v", outcome, err)
	}
}

func TestRolePermissionTransactionExecuteWithoutPrelockFailsWithoutSideEffects(t *testing.T) {
	script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
	db, _ := newRolePermissionTxDB(t, script)
	revoker := &rolePermissionTxRevoker{}
	base := rolePermissionTestExecution(t, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "no prelock"})
	execution, err := newRolePermissionTransactionalExecution(base, revoker)
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := rolePermissionTxBinding(actionsecurity.ActionUsersPromote, base.requestHMAC, base.targetGUID())
	tx := db.Begin()
	outcome, err := execution.Execute(context.Background(), tx, operation)
	if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("direct execute = %#v/%v", outcome, err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if len(revoker.calls) != 0 || len(observedCalls(script, "exec", "")) != 0 || len(script.committed) != 0 || script.rollbackCount != 1 {
		t.Fatalf("direct execute side effects: redis=%v exec=%d committed=%d rollback=%d", revoker.calls, len(observedCalls(script, "exec", "")), len(script.committed), script.rollbackCount)
	}
}

func TestRolePermissionTransactionLockedPreflightRejectsNoopAndStaleWithoutWrites(t *testing.T) {
	head2, active := rolePermissionPolicyRows(2, []struct{ capability, effect int }{{7, 2}})
	cases := []struct {
		name   string
		action actionsecurity.Action
		intent any
		role   models.UserRole
		head   *models.PermissionPolicyHead
		rules  []models.PermissionOverride
		want   models.AdminOperationFailure
	}{
		{"promote same role", actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "same role"}, models.UserRoleAdmin, head2, active, models.FailureTargetStateConflict},
		{"demote same role", actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "same role"}, models.UserRoleUser, head2, active, models.FailureTargetStateConflict},
		{"permissions same canonical", actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}}, Reason: "same policy"}, models.UserRoleAdmin, head2, active, models.FailureTargetStateConflict},
		{"stale auth", actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 6, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "stale auth"}, models.UserRoleUser, head2, active, models.FailureTargetVersionConflict},
		{"stale policy", actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 1, CatalogVersion: 1, Reason: "stale policy"}, models.UserRoleUser, head2, active, models.FailurePolicyVersionConflict},
		{"stale catalog", actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 2, Reason: "stale catalog"}, models.UserRoleUser, head2, active, models.FailurePolicyVersionConflict},
		{"wrong role", actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "wrong role"}, models.UserRoleUser, head2, active, models.FailureTargetStateConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := rolePermissionHappyScript(tc.role, tc.head, tc.rules)
			db, _ := newRolePermissionTxDB(t, script)
			revoker := &rolePermissionTxRevoker{}
			base := rolePermissionTestExecution(t, tc.action, tc.intent)
			execution, err := newRolePermissionTransactionalExecution(base, revoker)
			if err != nil {
				t.Fatal(err)
			}
			operation, verification := rolePermissionTxBinding(tc.action, base.requestHMAC, base.targetGUID())
			tx := db.Begin()
			if _, err := execution.prelockForAuthorization(context.Background(), tx, operation, verification); err != nil {
				t.Fatal(err)
			}
			failure, err := execution.preflightLocked(context.Background(), tx, operation)
			if err != nil || failure == nil || *failure != tc.want {
				t.Fatalf("preflight = %v/%v, want %s", failure, err, tc.want.String())
			}
			_ = tx.Rollback().Error
			if len(revoker.calls) != 0 || len(observedCalls(script, "exec", "")) != 0 || len(script.committed) != 0 {
				t.Fatalf("preflight side effects: redis=%v exec=%d committed=%d", revoker.calls, len(observedCalls(script, "exec", "")), len(script.committed))
			}
		})
	}
}

func TestRolePermissionTransactionExecuteRejectsDifferentOperationBinding(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*models.AdminOperation)
	}{
		{"id", func(op *models.AdminOperation) { op.ID++ }},
		{"actor", func(op *models.AdminOperation) { op.ActorUserID++ }},
		{"session", func(op *models.AdminOperation) { op.SessionID++ }},
		{"public ref", func(op *models.AdminOperation) { op.PublicRef = "op_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA" }},
		{"request hmac", func(op *models.AdminOperation) { op.RequestHMAC = strings.Repeat("c", 64) }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
			db, _ := newRolePermissionTxDB(t, script)
			revoker := &rolePermissionTxRevoker{}
			base := rolePermissionTestExecution(t, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "binding"})
			execution, err := newRolePermissionTransactionalExecution(base, revoker)
			if err != nil {
				t.Fatal(err)
			}
			operation, verification := rolePermissionTxBinding(actionsecurity.ActionUsersPromote, base.requestHMAC, base.targetGUID())
			tx := db.Begin()
			if _, err := execution.prelockForAuthorization(context.Background(), tx, operation, verification); err != nil {
				t.Fatal(err)
			}
			different := operation
			tc.mutate(&different)
			outcome, err := execution.Execute(context.Background(), tx, different)
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) {
				t.Fatalf("different operation = %#v/%v", outcome, err)
			}
			_ = tx.Rollback().Error
			if len(revoker.calls) != 0 || len(observedCalls(script, "exec", "")) != 0 || len(script.committed) != 0 {
				t.Fatalf("different operation side effects: redis=%v exec=%d committed=%d", revoker.calls, len(observedCalls(script, "exec", "")), len(script.committed))
			}
		})
	}
}

func TestRolePermissionTransactionPrelockRejectsBindingMismatch(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*models.AdminOperation, *models.AdminActionVerification)
	}{
		{"operation request", func(op *models.AdminOperation, _ *models.AdminActionVerification) {
			op.RequestHMAC = strings.Repeat("c", 64)
		}},
		{"operation actor version", func(op *models.AdminOperation, _ *models.AdminActionVerification) { op.ActorAuthVersion++ }},
		{"operation creator", func(op *models.AdminOperation, _ *models.AdminActionVerification) {
			creator := op.ActorUserID + 1
			op.CreatedBy = &creator
		}},
		{"verification actor", func(_ *models.AdminOperation, verification *models.AdminActionVerification) {
			verification.ActorUserID++
		}},
		{"verification intent", func(_ *models.AdminOperation, verification *models.AdminActionVerification) {
			verification.IntentHMAC = strings.Repeat("d", 64)
		}},
		{"verification target", func(_ *models.AdminOperation, verification *models.AdminActionVerification) {
			target := *verification.TargetGUID + 1
			verification.TargetGUID = &target
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
			db, _ := newRolePermissionTxDB(t, script)
			base := rolePermissionTestExecution(t, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "binding"})
			execution, err := newRolePermissionTransactionalExecution(base, &rolePermissionTxRevoker{})
			if err != nil {
				t.Fatal(err)
			}
			operation, verification := rolePermissionTxBinding(actionsecurity.ActionUsersPromote, base.requestHMAC, base.targetGUID())
			tc.mutate(&operation, &verification)
			tx := db.Begin()
			locked, err := execution.prelockForAuthorization(context.Background(), tx, operation, verification)
			if locked != nil || !errors.Is(err, ErrActionOperationForbidden) || len(script.observed) != 0 {
				t.Fatalf("prelock mismatch = %#v/%v queries=%d", locked, err, len(script.observed))
			}
			_ = tx.Rollback().Error
		})
	}
}

func TestRolePermissionPolicyDemoteReplaceAndHistoricalPromotion(t *testing.T) {
	t.Run("promote no head uses admin baseline", func(t *testing.T) {
		script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
		script.sessions = nil
		outcome, revoker, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{}, Reason: "baseline"}, 0, nil)
		if err != nil || outcome.ResultRole == nil || *outcome.ResultRole != models.UserRoleAdmin || outcome.ResultPermissionsVersion == nil || *outcome.ResultPermissionsVersion != 1 || len(revoker.calls) != 0 {
			t.Fatalf("baseline promote = %#v/%v redis=%v", outcome, err, revoker.calls)
		}
		if len(committedCalls(script, "user_permission_overrides", "INSERT")) != 0 {
			t.Fatal("baseline promotion inserted overrides")
		}
		assertCommittedSQL(t, script, "INSERT INTO `user_permission_heads` (`guid`,`created_at`,`created_by`,`updated_at`,`updated_by`,`is_deleted`,`user_id`,`policy_version`,`catalog_version`,`rule_count`) VALUES (?,?,?,?,?,?,?,?,?,?)", "[7001 8001 41 8001 41 0 61 1 1 0]")
	})

	t.Run("demote keeps an empty nondeleted head", func(t *testing.T) {
		head, rules := rolePermissionPolicyRows(2, []struct{ capability, effect int }{{1, 3}, {7, 2}})
		script := rolePermissionHappyScript(models.UserRoleAdmin, head, rules)
		outcome, revoker, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "demotion"}, 0, nil)
		if err != nil || outcome.ResultRole == nil || *outcome.ResultRole != models.UserRoleUser || outcome.ResultPermissionsVersion == nil || *outcome.ResultPermissionsVersion != 3 {
			t.Fatalf("demote = %#v/%v", outcome, err)
		}
		if len(revoker.calls) != 2 {
			t.Fatalf("revocations = %v", revoker.calls)
		}
		assertCommittedSQL(t, script, "UPDATE `user_permission_overrides` SET `is_deleted`=?,`updated_at`=?,`updated_by`=? WHERE user_id = ? AND is_deleted = 0", "[1 8001 41 61]")
		headCall := assertCommittedSQL(t, script, "UPDATE `user_permission_heads` SET `catalog_version`=?,`policy_version`=?,`rule_count`=?,`updated_at`=?,`updated_by`=? WHERE id = ? AND user_id = ? AND is_deleted = 0 AND policy_version = ? AND catalog_version = ?", "[1 3 0 8001 41 91 61 2 1]")
		if strings.Contains(headCall.sql, "is_deleted`=1") {
			t.Fatal("demotion deleted the policy head")
		}
		for _, call := range script.committed {
			if call.table == "user_permission_overrides" && strings.HasPrefix(call.sql, "INSERT") {
				t.Fatal("demotion inserted an override")
			}
		}
	})

	t.Run("permissions replace tombstones and inserts canonical rules", func(t *testing.T) {
		head, rules := rolePermissionPolicyRows(4, []struct{ capability, effect int }{{1, 3}})
		script := rolePermissionHappyScript(models.UserRoleAdmin, head, rules)
		outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 4, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}, {Capability: "users.delete", Effect: 3}}, Reason: "replace"}, 0, nil)
		if err != nil || outcome.ResultRole == nil || *outcome.ResultRole != models.UserRoleAdmin || outcome.ResultPermissionsVersion == nil || *outcome.ResultPermissionsVersion != 5 || outcome.ResultAuthVersion == nil || *outcome.ResultAuthVersion != 8 {
			t.Fatalf("replace = %#v/%v", outcome, err)
		}
		inserts := committedCalls(script, "user_permission_overrides", "INSERT")
		if len(inserts) != 2 || fmt.Sprint(rolePermissionArgValues(inserts[0].args)[8:]) != "[12 3]" || fmt.Sprint(rolePermissionArgValues(inserts[1].args)[8:]) != "[7 2]" {
			t.Fatalf("canonical inserts = %#v", inserts)
		}
	})

	t.Run("promotion after demotion ignores tombstoned history", func(t *testing.T) {
		head := &models.PermissionPolicyHead{ID: 91, AuditFields: models.AuditFields{Guid: 9001}, UserID: 61, PolicyVersion: 3, CatalogVersion: 1, RuleCount: 0}
		_, history := rolePermissionPolicyRows(2, []struct{ capability, effect int }{{1, 3}})
		history[0].IsDeleted = 1
		script := rolePermissionHappyScript(models.UserRoleUser, head, history)
		outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 3, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{}, Reason: "promote again"}, 0, nil)
		if err != nil || outcome.ResultPermissionsVersion == nil || *outcome.ResultPermissionsVersion != 4 {
			t.Fatalf("later promote = %#v/%v", outcome, err)
		}
		if len(committedCalls(script, "user_permission_overrides", "INSERT")) != 0 {
			t.Fatal("later promotion revived historical policy")
		}
	})
}

func TestRolePermissionRedisFailuresPrecedeEveryBusinessWrite(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("redis_%d", failAt), func(t *testing.T) {
			script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
			outcome, revoker, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "promote"}, failAt, nil)
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) || len(revoker.calls) != failAt {
				t.Fatalf("redis failure = %#v/%v/%v", outcome, err, revoker.calls)
			}
			if calls := observedCalls(script, "exec", ""); len(calls) != 0 || len(script.committed) != 0 || script.rollbackCount != 1 {
				t.Fatalf("business writes/commit/rollback = %d/%d/%d", len(calls), len(script.committed), script.rollbackCount)
			}
		})
	}
}

func TestRolePermissionRollbackOnEverySQLStageAndLostUpdate(t *testing.T) {
	intent := actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.delete", Effect: 3}, {Capability: "users.sessions.read", Effect: 2}}, Reason: "promotion"}
	for stage := 1; stage <= 14; stage++ {
		t.Run(fmt.Sprintf("sql_error_%d", stage), func(t *testing.T) {
			script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
			outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersPromote, intent, 0, func(s *rolePermissionTxScript) { s.failAt = stage })
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) || len(script.committed) != 0 || script.rollbackCount != 1 {
				t.Fatalf("stage %d = %#v/%v committed=%d rollback=%d", stage, outcome, err, len(script.committed), script.rollbackCount)
			}
		})
		if stage <= 5 {
			continue
		}
		for _, affected := range []int64{0, 2} {
			t.Run(fmt.Sprintf("rows_%d_%d", stage, affected), func(t *testing.T) {
				script := rolePermissionHappyScript(models.UserRoleUser, nil, nil)
				outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersPromote, intent, 0, func(s *rolePermissionTxScript) { s.rowsAt[stage] = affected })
				if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) || len(script.committed) != 0 || script.rollbackCount != 1 {
					t.Fatalf("stage/rows %d/%d = %#v/%v committed=%d", stage, affected, outcome, err, len(script.committed))
				}
			})
		}
	}

	head, rules := rolePermissionPolicyRows(2, []struct{ capability, effect int }{{1, 3}})
	for _, stage := range []int{11, 12, 13} {
		t.Run(fmt.Sprintf("demote_stage_%d", stage), func(t *testing.T) {
			script := rolePermissionHappyScript(models.UserRoleAdmin, head, rules)
			outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "demote"}, 0, func(s *rolePermissionTxScript) { s.failAt = stage })
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) || len(script.committed) != 0 || script.rollbackCount != 1 {
				t.Fatalf("demote stage %d = %#v/%v", stage, outcome, err)
			}
		})
		for _, affected := range []int64{0, 2} {
			t.Run(fmt.Sprintf("demote_rows_%d_%d", stage, affected), func(t *testing.T) {
				script := rolePermissionHappyScript(models.UserRoleAdmin, head, rules)
				outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "demote"}, 0, func(s *rolePermissionTxScript) { s.rowsAt[stage] = affected })
				if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) || len(script.committed) != 0 || script.rollbackCount != 1 {
					t.Fatalf("demote stage/rows %d/%d = %#v/%v", stage, affected, outcome, err)
				}
			})
		}
	}
}

func TestRolePermissionPolicyRejectionsPerformZeroBusinessWrites(t *testing.T) {
	head, rules := rolePermissionPolicyRows(2, []struct{ capability, effect int }{{1, 3}})
	tests := []struct {
		name   string
		action actionsecurity.Action
		intent any
		mutate func(*rolePermissionTxScript, *models.AdminOperation, *models.AdminActionVerification)
		want   models.AdminOperationFailure
	}{
		{"stale auth", actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 6, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "stale"}, nil, models.FailureTargetVersionConflict},
		{"stale policy", actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 1, CatalogVersion: 1, Reason: "stale"}, nil, models.FailurePolicyVersionConflict},
		{"stale catalog", actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 2, Reason: "stale"}, nil, models.FailurePolicyVersionConflict},
		{"wrong role", actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "wrong"}, nil, models.FailureTargetStateConflict},
		{"wrong status", actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "wrong"}, func(s *rolePermissionTxScript, _ *models.AdminOperation, _ *models.AdminActionVerification) {
			s.target.Status = models.UserStatus(99)
		}, models.FailureTargetStateConflict},
		{"root target", actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "root"}, func(s *rolePermissionTxScript, _ *models.AdminOperation, _ *models.AdminActionVerification) {
			s.target.Role = models.UserRoleRoot
		}, models.FailureTargetStateConflict},
		{"self", actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "self"}, func(s *rolePermissionTxScript, _ *models.AdminOperation, _ *models.AdminActionVerification) {
			s.target.ID = 41
			for i := range s.sessions {
				s.sessions[i].UserID = 41
			}
			s.head.UserID = 41
			for i := range s.rules {
				s.rules[i].UserID = 41
			}
		}, models.FailureActionRejected},
		{"no op", actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}, Reason: "same"}, nil, models.FailureTargetStateConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script := rolePermissionHappyScript(models.UserRoleAdmin, head, rules)
			outcome, _, err := runRolePermissionTxWithBinding(t, script, tc.action, tc.intent, 0, nil, tc.mutate)
			if err != nil || outcome.Failure == nil || *outcome.Failure != tc.want || len(observedCalls(script, "exec", "")) != 0 {
				t.Fatalf("rejection = %#v/%v writes=%d", outcome, err, len(observedCalls(script, "exec", "")))
			}
		})
	}

	for _, tc := range []struct {
		name   string
		mutate func(*rolePermissionTxScript, *models.AdminOperation, *models.AdminActionVerification)
	}{
		{"deleted", func(s *rolePermissionTxScript, _ *models.AdminOperation, _ *models.AdminActionVerification) {
			s.target.IsDeleted = 1
		}},
		{"wrong action binding", func(_ *rolePermissionTxScript, op *models.AdminOperation, _ *models.AdminActionVerification) {
			op.Action = int(actionsecurity.ActionUsersPromote)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := rolePermissionHappyScript(models.UserRoleAdmin, head, rules)
			outcome, _, err := runRolePermissionTxWithBinding(t, script, actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "reject"}, 0, nil, tc.mutate)
			if outcome != (TerminalOutcome{}) || err == nil || len(observedCalls(script, "exec", "")) != 0 {
				t.Fatalf("closed boundary = %#v/%v", outcome, err)
			}
		})
	}
}

func TestRolePermissionPolicyCorruptLockedGraphRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*models.PermissionPolicyHead, []models.PermissionOverride)
	}{
		{"rule count mismatch", func(head *models.PermissionPolicyHead, _ []models.PermissionOverride) { head.RuleCount = 2 }},
		{"rule version mismatch", func(_ *models.PermissionPolicyHead, rules []models.PermissionOverride) { rules[0].PolicyVersion = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head, rules := rolePermissionPolicyRows(2, []struct{ capability, effect int }{{1, 3}})
			tc.mutate(head, rules)
			script := rolePermissionHappyScript(models.UserRoleAdmin, head, rules)
			outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "corrupt graph"}, 0, nil)
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) || len(observedCalls(script, "exec", "")) != 0 || script.rollbackCount != 1 {
				t.Fatalf("corrupt graph = %#v/%v writes=%d rollback=%d", outcome, err, len(observedCalls(script, "exec", "")), script.rollbackCount)
			}
		})
	}
}

func TestRolePermissionTransactionAcceptsDisabledTarget(t *testing.T) {
	script := rolePermissionHappyScript(models.UserRoleAdmin, &models.PermissionPolicyHead{ID: 91, AuditFields: models.AuditFields{Guid: 9001}, UserID: 61, PolicyVersion: 2, CatalogVersion: 1}, nil)
	script.target.Status = models.UserStatusDisabled
	outcome, _, err := runRolePermissionTx(t, script, actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "disabled target"}, 0, nil)
	if err != nil || outcome.ResultRole == nil || *outcome.ResultRole != models.UserRoleUser {
		t.Fatalf("disabled target = %#v/%v", outcome, err)
	}
}

func TestRolePermissionSnapshotDemotedHeadIgnoresTombstonedHistory(t *testing.T) {
	head := &models.PermissionPolicyHead{ID: 91, AuditFields: models.AuditFields{Guid: 9001}, UserID: 61, PolicyVersion: 3, CatalogVersion: 1, RuleCount: 0}
	_, history := rolePermissionPolicyRows(2, []struct{ capability, effect int }{{1, 3}})
	history[0].IsDeleted = 1
	script := rolePermissionHappyScript(models.UserRoleUser, head, history)
	db, _ := newRolePermissionTxDB(t, script)
	snapshot, err := LoadPermissionSnapshot(context.Background(), db, 61)
	if err != nil || snapshot == nil || snapshot.PolicyVersion() != 3 || snapshot.AuthVersion() != 7 {
		t.Fatalf("demoted snapshot = %#v/%v", snapshot, err)
	}
}

func runRolePermissionTx(t *testing.T, script *rolePermissionTxScript, action actionsecurity.Action, intent any, redisFailAt int, configure func(*rolePermissionTxScript)) (TerminalOutcome, *rolePermissionTxRevoker, error) {
	t.Helper()
	return runRolePermissionTxWithBinding(t, script, action, intent, redisFailAt, configure, nil)
}

func runRolePermissionTxWithBinding(t *testing.T, script *rolePermissionTxScript, action actionsecurity.Action, intent any, redisFailAt int, configure func(*rolePermissionTxScript), mutateBinding func(*rolePermissionTxScript, *models.AdminOperation, *models.AdminActionVerification)) (TerminalOutcome, *rolePermissionTxRevoker, error) {
	t.Helper()
	if configure != nil {
		configure(script)
	}
	var guid atomic.Int64
	guid.Store(7000)
	base, err := NewRolePermissionExecution(rolePermissionTestDescriptor(t, action), intent, func() int64 { return guid.Add(1) }, &createAccountTestClock{now: 8001}, createAccountTestCrypto(t))
	if err != nil {
		t.Fatal(err)
	}
	revoker := &rolePermissionTxRevoker{failAt: redisFailAt}
	execution, err := newRolePermissionTransactionalExecution(base, revoker)
	if err != nil {
		t.Fatal(err)
	}
	operation, verification := rolePermissionTxBinding(action, base.requestHMAC, base.targetGUID())
	if mutateBinding != nil {
		mutateBinding(script, &operation, &verification)
	}
	db, _ := newRolePermissionTxDB(t, script)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if _, err := execution.prelockForAuthorization(context.Background(), tx, operation, verification); err != nil {
		_ = tx.Rollback().Error
		return TerminalOutcome{}, revoker, err
	}
	outcome, err := execution.Execute(context.Background(), tx, operation)
	if err != nil {
		_ = tx.Rollback().Error
		return outcome, revoker, err
	}
	if err := tx.Commit().Error; err != nil {
		return TerminalOutcome{}, revoker, err
	}
	return outcome, revoker, nil
}

func rolePermissionPolicyRows(version int64, values []struct{ capability, effect int }) (*models.PermissionPolicyHead, []models.PermissionOverride) {
	head := &models.PermissionPolicyHead{ID: 91, AuditFields: models.AuditFields{Guid: 9001}, UserID: 61, PolicyVersion: version, CatalogVersion: 1, RuleCount: len(values)}
	rules := make([]models.PermissionOverride, len(values))
	for i := range values {
		rules[i] = models.PermissionOverride{ID: int64(101 + i), AuditFields: models.AuditFields{Guid: int64(10001 + i)}, UserID: 61, PolicyVersion: version, Capability: values[i].capability, Effect: values[i].effect}
	}
	return head, rules
}

func observedCalls(script *rolePermissionTxScript, kind, table string) []rolePermissionTxCall {
	script.mu.Lock()
	defer script.mu.Unlock()
	var result []rolePermissionTxCall
	for _, call := range script.observed {
		if (kind == "" || call.kind == kind) && (table == "" || call.table == table) {
			result = append(result, call)
		}
	}
	return result
}

func committedCalls(script *rolePermissionTxScript, table, prefix string) []rolePermissionTxCall {
	script.mu.Lock()
	defer script.mu.Unlock()
	var result []rolePermissionTxCall
	for _, call := range script.committed {
		if call.table == table && strings.HasPrefix(call.sql, prefix) {
			result = append(result, call)
		}
	}
	return result
}

func assertCommittedSQL(t *testing.T, script *rolePermissionTxScript, wantSQL, wantArgs string) rolePermissionTxCall {
	t.Helper()
	script.mu.Lock()
	defer script.mu.Unlock()
	for _, call := range script.committed {
		if call.sql == wantSQL {
			if got := fmt.Sprint(rolePermissionArgValues(call.args)); got != wantArgs {
				t.Fatalf("args for %q = %s, want %s", wantSQL, got, wantArgs)
			}
			return call
		}
	}
	t.Fatalf("missing committed SQL %q; calls=%#v", wantSQL, script.committed)
	return rolePermissionTxCall{}
}

func rolePermissionHappyScript(role models.UserRole, head *models.PermissionPolicyHead, rules []models.PermissionOverride) *rolePermissionTxScript {
	var ownedHead *models.PermissionPolicyHead
	if head != nil {
		copyHead := *head
		ownedHead = &copyHead
	}
	ownedRules := append([]models.PermissionOverride(nil), rules...)
	activeRules := int64(0)
	for i := range ownedRules {
		if ownedRules[i].IsDeleted == 0 {
			activeRules++
		}
	}
	rowsAt := map[int]int64{}
	if activeRules > 0 {
		rowsAt[11] = activeRules
	}
	return &rolePermissionTxScript{now: 8001,
		actor:  models.User{ID: 41, AuditFields: models.AuditFields{Guid: 4001}, Role: models.UserRoleRoot, Status: models.UserStatusActive, AuthVersion: 3},
		target: models.User{ID: 61, AuditFields: models.AuditFields{Guid: 6001}, Role: role, Status: models.UserStatusActive, AuthVersion: 7},
		sessions: []models.Session{
			{ID: 71, AuditFields: models.AuditFields{Guid: 7001}, SID: "sid-71", UserID: 61, LoginMethod: models.LoginMethodPassword, SessionVersion: 2, ExpiresAt: 9001},
			{ID: 72, AuditFields: models.AuditFields{Guid: 7002}, SID: "sid-72", UserID: 61, LoginMethod: models.LoginMethodPassword, SessionVersion: 3, ExpiresAt: 10001},
		}, head: ownedHead, rules: ownedRules, rowsAt: rowsAt}
}

func rolePermissionTxBinding(action actionsecurity.Action, requestHMAC string, targetGUID int64) (models.AdminOperation, models.AdminActionVerification) {
	actor, verificationID, leaseUntil := int64(41), int64(51), int64(9001)
	lease := strings.Repeat("b", 64)
	operation := models.AdminOperation{ID: 31, AuditFields: models.AuditFields{Guid: 3001, CreatedAt: 7001, CreatedBy: &actor, UpdatedAt: 7001, UpdatedBy: &actor}, ActorUserID: actor, ActorAuthVersion: 3, SessionID: 45, Action: int(action), VerificationID: &verificationID, State: models.OperationProcessing, PublicRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", RequestHMAC: requestHMAC, LeaseOwnerHMAC: &lease, LeaseExpiresAt: &leaseUntil, QueryExpiresAt: 10001}
	target := targetGUID
	verification := models.AdminActionVerification{ID: verificationID, ActorUserID: actor, ActorAuthVersion: 3, SessionID: 45, Action: int(action), TargetKind: int(actionsecurity.TargetUser), TargetGUID: &target, IntentHMAC: requestHMAC, ExpiresAt: 9001}
	return operation, verification
}

func assertRolePermissionTxHappyCalls(t *testing.T, script *rolePermissionTxScript) {
	t.Helper()
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.commitCount != 1 || script.rollbackCount != 0 || len(script.committed) != 9 {
		t.Fatalf("commit/rollback/writes = %d/%d/%d; observed=%#v", script.commitCount, script.rollbackCount, len(script.committed), script.observed)
	}
	wantTables := []string{"users", "users", "user_sessions", "user_permission_heads", "user_permission_overrides", "user_sessions", "auth_audit_events", "user_sessions", "auth_audit_events", "users", "user_permission_overrides", "user_permission_overrides", "user_permission_heads", "auth_audit_events"}
	if len(script.observed) != len(wantTables) {
		t.Fatalf("calls = %d, want %d: %#v", len(script.observed), len(wantTables), script.observed)
	}
	for i, want := range wantTables {
		if script.observed[i].table != want {
			t.Fatalf("call %d table = %s, want %s; sql=%s", i+1, script.observed[i].table, want, script.observed[i].sql)
		}
	}
	queries := script.observed[:5]
	wantQueries := []string{
		"SELECT `id`,`guid`,`role`,`status`,`is_deleted`,`auth_version` FROM `users` WHERE id = ? AND is_deleted = 0 ORDER BY `users`.`id` LIMIT ? FOR UPDATE",
		"SELECT `id`,`guid`,`role`,`status`,`is_deleted`,`auth_version` FROM `users` WHERE guid = ? AND is_deleted = 0 ORDER BY `users`.`id` LIMIT ? FOR UPDATE",
		"SELECT `id`,`guid`,`sid`,`user_id`,`login_method`,`session_version`,`is_deleted`,`revoked_at`,`expires_at` FROM `user_sessions` WHERE user_id = ? AND is_deleted = 0 AND revoked_at IS NULL ORDER BY id ASC FOR UPDATE",
		"SELECT `id`,`guid`,`user_id`,`is_deleted`,`policy_version`,`catalog_version`,`rule_count` FROM `user_permission_heads` WHERE user_id = ? AND is_deleted = 0 ORDER BY `user_permission_heads`.`id` LIMIT ? FOR UPDATE",
		"SELECT `id`,`guid`,`user_id`,`is_deleted`,`policy_version`,`capability`,`effect` FROM `user_permission_overrides` WHERE user_id = ? AND is_deleted = 0 ORDER BY id ASC FOR UPDATE",
	}
	for i := range queries {
		if queries[i].sql != wantQueries[i] {
			t.Fatalf("query %d = %q\nwant %q", i+1, queries[i].sql, wantQueries[i])
		}
	}
	if got := rolePermissionArgValues(queries[0].args); fmt.Sprint(got) != "[41 1]" {
		t.Fatalf("actor args = %#v", got)
	}
	if got := rolePermissionArgValues(queries[1].args); fmt.Sprint(got) != "[6001 1]" {
		t.Fatalf("target args = %#v", got)
	}
	for i := 2; i < 5; i++ {
		if got := rolePermissionArgValues(queries[i].args); fmt.Sprint(got) != "[61]" && !(i == 3 && fmt.Sprint(got) == "[61 1]") {
			t.Fatalf("query %d args = %#v", i+1, got)
		}
	}
	for _, call := range script.committed {
		if call.table == "user_permission_overrides" && strings.HasPrefix(call.sql, "INSERT") {
			values := rolePermissionArgValues(call.args)
			if len(values) < 10 {
				t.Fatalf("rule insert args = %#v", values)
			}
		}
	}
	writes := script.observed[5:]
	wantWriteSQL := []string{
		"UPDATE `user_sessions` SET `revoked_at`=?,`session_version`=?,`updated_at`=?,`updated_by`=? WHERE id = ? AND user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND session_version = ?",
		"INSERT INTO `auth_audit_events` (`guid`,`created_at`,`created_by`,`updated_at`,`updated_by`,`is_deleted`,`user_id`,`session_guid`,`event_type`,`login_method`,`ip`,`user_agent`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
		"UPDATE `user_sessions` SET `revoked_at`=?,`session_version`=?,`updated_at`=?,`updated_by`=? WHERE id = ? AND user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND session_version = ?",
		"INSERT INTO `auth_audit_events` (`guid`,`created_at`,`created_by`,`updated_at`,`updated_by`,`is_deleted`,`user_id`,`session_guid`,`event_type`,`login_method`,`ip`,`user_agent`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
		"UPDATE `users` SET `auth_version`=?,`role`=?,`updated_at`=?,`updated_by`=? WHERE id = ? AND guid = ? AND is_deleted = 0 AND auth_version = ? AND role = ? AND status = ?",
		"INSERT INTO `user_permission_overrides` (`guid`,`created_at`,`created_by`,`updated_at`,`updated_by`,`is_deleted`,`user_id`,`policy_version`,`capability`,`effect`) VALUES (?,?,?,?,?,?,?,?,?,?)",
		"INSERT INTO `user_permission_overrides` (`guid`,`created_at`,`created_by`,`updated_at`,`updated_by`,`is_deleted`,`user_id`,`policy_version`,`capability`,`effect`) VALUES (?,?,?,?,?,?,?,?,?,?)",
		"INSERT INTO `user_permission_heads` (`guid`,`created_at`,`created_by`,`updated_at`,`updated_by`,`is_deleted`,`user_id`,`policy_version`,`catalog_version`,`rule_count`) VALUES (?,?,?,?,?,?,?,?,?,?)",
		"INSERT INTO `auth_audit_events` (`guid`,`created_at`,`created_by`,`updated_at`,`updated_by`,`is_deleted`,`user_id`,`session_guid`,`event_type`,`login_method`,`ip`,`user_agent`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
	}
	wantWriteArgs := []string{
		"[8001 3 8001 41 71 61 2]", "[7001 8001 41 8001 41 0 61 7001 6 1]",
		"[8001 4 8001 41 72 61 3]", "[7002 8001 41 8001 41 0 61 7002 6 1]",
		"[8 10 8001 41 61 6001 7 1 1]", "[7003 8001 41 8001 41 0 61 1 12 3]",
		"[7004 8001 41 8001 41 0 61 1 7 2]", "[7005 8001 41 8001 41 0 61 1 1 2]",
		"[7006 8001 41 8001 41 0 61 <nil> 10 <nil>]",
	}
	for i := range writes {
		if writes[i].sql != wantWriteSQL[i] {
			t.Fatalf("write %d SQL = %q, want %q", i+1, writes[i].sql, wantWriteSQL[i])
		}
		args := rolePermissionArgValues(writes[i].args)
		if writes[i].table == "auth_audit_events" {
			args = args[:10]
		}
		if got := fmt.Sprint(args); got != wantWriteArgs[i] {
			t.Fatalf("write %d args = %s, want %s", i+1, got, wantWriteArgs[i])
		}
	}
}

func rolePermissionArgValues(args []driver.NamedValue) []any {
	values := make([]any, len(args))
	for i := range args {
		values[i] = args[i].Value
	}
	return values
}
