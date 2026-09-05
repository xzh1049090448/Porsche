package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
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

func TestDeleteUserExecutionImplementsTransactionalConsumer(t *testing.T) {
	var _ TransactionalActionConsumer = (*DeleteUserExecution)(nil)
}

func TestDeleteUserExecutionPersistsExactAtomicSoftDelete(t *testing.T) {
	db, script := newDeleteConsumerDB(t)
	clock := &deleteConsumerClock{now: 8_001}
	var guidCalls atomic.Int32
	execution := newDeleteConsumerExecution(t, deleteWriterIntent(), clock, func() int64 {
		guidCalls.Add(1)
		return 7_001
	})

	tx := db.Begin()
	outcome, err := execution.Execute(context.Background(), tx, validDeleteConsumerOperation())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failure != nil || outcome.HTTPStatus != 200 || outcome.ResultKind != models.ResultUser ||
		outcome.ResultGUID == nil || *outcome.ResultGUID != script.target.Guid {
		t.Fatalf("outcome = %#v", outcome)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if clock.calls.Load() != 1 || guidCalls.Load() != 1 {
		t.Fatalf("clock/GUID calls = %d/%d, want 1/1", clock.calls.Load(), guidCalls.Load())
	}

	calls := script.committedCalls()
	wantKinds := []string{
		"query:users", "query:user_sessions", "query:gateway_api_tokens", "query:user_permission_heads", "query:user_permission_overrides",
		"exec:user_sessions", "exec:gateway_api_tokens", "exec:user_permission_heads", "exec:user_permission_overrides", "exec:users", "exec:auth_audit_events",
	}
	if got := deleteConsumerCallKinds(calls); fmt.Sprint(got) != fmt.Sprint(wantKinds) {
		t.Fatalf("call order = %v, want %v", got, wantKinds)
	}

	assertDeleteConsumerQuery(t, calls[0], "SELECT `id`,`guid`,`role`,`status`,`is_deleted`,`auth_version` FROM `users` WHERE guid = ? AND is_deleted = 0", "FOR UPDATE")
	assertDeleteConsumerQuery(t, calls[1], "SELECT `id`,`session_version` FROM `user_sessions` WHERE user_id = ? AND is_deleted = 0 AND revoked_at IS NULL ORDER BY id ASC", "FOR UPDATE")
	assertDeleteConsumerQuery(t, calls[2], "SELECT `id` FROM `gateway_api_tokens` WHERE user_id = ? AND is_deleted = 0 AND status = ? AND (expires_at IS NULL OR expires_at > ?) ORDER BY id ASC", "FOR UPDATE")
	assertDeleteConsumerQuery(t, calls[3], "SELECT `id` FROM `user_permission_heads` WHERE user_id = ? AND is_deleted = 0 ORDER BY id ASC", "FOR UPDATE")
	assertDeleteConsumerQuery(t, calls[4], "SELECT `id` FROM `user_permission_overrides` WHERE user_id = ? AND is_deleted = 0 ORDER BY id ASC", "FOR UPDATE")
	assertDeleteConsumerArgs(t, calls[0], int64(6_001), int64(1))
	assertDeleteConsumerArgs(t, calls[1], int64(61))
	assertDeleteConsumerArgs(t, calls[2], int64(61), int64(models.GatewayTokenActive), int64(8_001))
	assertDeleteConsumerArgs(t, calls[3], int64(61))
	assertDeleteConsumerArgs(t, calls[4], int64(61))

	assertDeleteConsumerUpdate(t, calls[5], []string{"revoked_at", "session_version", "updated_at", "updated_by"},
		"WHERE user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND session_version < ?")
	assertDeleteConsumerUpdate(t, calls[6], []string{"status", "updated_at", "updated_by"},
		"WHERE user_id = ? AND is_deleted = 0 AND status = ? AND (expires_at IS NULL OR expires_at > ?)")
	assertDeleteConsumerUpdate(t, calls[7], []string{"is_deleted", "updated_at", "updated_by"}, "WHERE user_id = ? AND is_deleted = 0")
	assertDeleteConsumerUpdate(t, calls[8], []string{"is_deleted", "updated_at", "updated_by"}, "WHERE user_id = ? AND is_deleted = 0")
	assertDeleteConsumerUpdate(t, calls[9], []string{
		"auth_version", "id_card_hash", "is_deleted", "is_verified", "nickname", "password_hash", "phone", "real_name", "status", "updated_at", "updated_by",
	}, "WHERE id = ? AND guid = ? AND is_deleted = 0 AND auth_version = ? AND role = ? AND status = ?")
	assertDeleteConsumerArgs(t, calls[5], int64(8_001), int64(8_001), int64(41), int64(61), int64(math.MaxInt32))
	assertDeleteConsumerArgs(t, calls[6], int64(models.GatewayTokenRevoked), int64(8_001), int64(41), int64(61), int64(models.GatewayTokenActive), int64(8_001))
	assertDeleteConsumerArgs(t, calls[7], int64(1), int64(8_001), int64(41), int64(61))
	assertDeleteConsumerArgs(t, calls[8], int64(1), int64(8_001), int64(41), int64(61))
	assertDeleteConsumerArgs(t, calls[9], nil, int64(1), false, nil, nil, nil, nil, int64(models.UserStatusDisabled), int64(8_001), int64(41),
		int64(61), int64(6_001), int64(7), int64(models.UserRoleUser), int64(models.UserStatusActive))
	for _, preserved := range []string{"username", "guid", "role", "plan_type", "allowed_models", "created_at", "created_by", "last_login_at", "daily_call_limit", "daily_calls_used", "daily_calls_reset_at", "total_tokens_used"} {
		if deleteConsumerSetColumns(calls[9].query)[preserved] {
			t.Errorf("user update changed preserved column %q: %s", preserved, calls[9].query)
		}
	}

	audit := deleteConsumerInsertValues(t, calls[10])
	wantAudit := map[string]any{
		"guid": int64(7_001), "created_at": int64(8_001), "created_by": int64(41), "updated_at": int64(8_001), "updated_by": int64(41),
		"is_deleted": int64(0), "user_id": int64(61), "session_guid": nil, "event_type": int64(models.AuthAuditEventUserDeleted),
		"login_method": nil, "ip": nil, "user_agent": nil,
	}
	if len(audit) != len(wantAudit) {
		t.Fatalf("auth audit columns = %v, want %v", audit, wantAudit)
	}
	for column, want := range wantAudit {
		if fmt.Sprint(audit[column]) != fmt.Sprint(want) {
			t.Errorf("auth audit %s = %v, want %v", column, audit[column], want)
		}
	}
	for _, call := range calls {
		lower := strings.ToLower(call.query)
		if strings.Contains(lower, "delete from") || strings.Contains(lower, "savepoint") || strings.Contains(lower, "start transaction") {
			t.Errorf("forbidden SQL: %s", call.query)
		}
		if strings.Contains(call.query, "approved deletion") {
			t.Errorf("reason leaked into SQL: %s", call.query)
		}
		for _, arg := range call.args {
			if fmt.Sprint(arg.Value) == "approved deletion" {
				t.Errorf("reason leaked into SQL args: %v", call.args)
			}
		}
	}
	if script.nestedBegins.Load() != 0 {
		t.Fatalf("nested transactions = %d", script.nestedBegins.Load())
	}
	if got := script.lockedSessions(); fmt.Sprint(got) != fmt.Sprint([]int64{71, 72, 73}) {
		t.Fatalf("locked sessions = %v, want active and expired unrevoked rows", got)
	}
	execution.state.mu.Lock()
	facts := execution.state.facts
	recorded := execution.state.factsRecorded
	execution.state.mu.Unlock()
	if !recorded || facts.targetID != 61 || facts.targetGUID != 6_001 || facts.beforeStatus != models.UserStatusActive {
		t.Fatalf("audit facts = %#v recorded=%t", facts, recorded)
	}
}

func TestDeleteUserExecutionKnownConflictsHaveZeroWrites(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*deleteConsumerScript, *actionsecurity.DeleteUserIntent)
		want        models.AdminOperationFailure
		wantQueries int
	}{
		{"version", func(script *deleteConsumerScript, _ *actionsecurity.DeleteUserIntent) { script.target.AuthVersion++ }, models.FailureTargetVersionConflict, 1},
		{"role state", func(script *deleteConsumerScript, _ *actionsecurity.DeleteUserIntent) {
			script.target.Role = models.UserRoleRoot
		}, models.FailureTargetStateConflict, 1},
		{"status state", func(script *deleteConsumerScript, _ *actionsecurity.DeleteUserIntent) {
			script.target.Status = models.UserStatus(99)
		}, models.FailureTargetStateConflict, 1},
		{"target overflow after matched version", func(script *deleteConsumerScript, intent *actionsecurity.DeleteUserIntent) {
			script.target.AuthVersion = math.MaxInt32
			intent.ExpectedAuthVersion = math.MaxInt32
		}, models.FailureConsumerValidation, 1},
		{"version mismatch precedes overflow", func(script *deleteConsumerScript, _ *actionsecurity.DeleteUserIntent) {
			script.target.AuthVersion = math.MaxInt32
		}, models.FailureTargetVersionConflict, 1},
		{"session version overflow", func(script *deleteConsumerScript, _ *actionsecurity.DeleteUserIntent) {
			script.sessions[1].SessionVersion = math.MaxInt32
		}, models.FailureConsumerValidation, 2},
		{"expired session version overflow", func(script *deleteConsumerScript, _ *actionsecurity.DeleteUserIntent) {
			script.sessions[2].SessionVersion = math.MaxInt32
		}, models.FailureConsumerValidation, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, script := newDeleteConsumerDB(t)
			intent := deleteWriterIntent()
			tc.mutate(script, &intent)
			execution := newDeleteConsumerExecution(t, intent, &deleteConsumerClock{now: 8_001}, func() int64 {
				t.Fatal("GUID generator called on rejected execution")
				return 0
			})
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, validDeleteConsumerOperation())
			if err != nil || outcome.Failure == nil || *outcome.Failure != tc.want || outcome.HTTPStatus != 409 {
				t.Fatalf("outcome/error = %#v/%v, want %v/409", outcome, err, tc.want)
			}
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			if got := script.queryCount(); got != tc.wantQueries {
				t.Fatalf("queries = %d, want %d", got, tc.wantQueries)
			}
			if got := script.execCount(); got != 0 {
				t.Fatalf("writes = %d, want 0", got)
			}
		})
	}
}

func TestDeleteUserExecutionRejectsDisabledStateDriftBeforeMutations(t *testing.T) {
	db, script := newDeleteConsumerDB(t)
	script.target.Role = models.UserRoleAdmin
	script.target.Status = models.UserStatusDisabled
	execution := newDeleteConsumerExecution(t, deleteWriterIntent(), &deleteConsumerClock{now: 8_001}, func() int64 {
		t.Fatal("GUID generator called after state drift")
		return 0
	})
	tx := db.Begin()
	outcome, err := execution.Execute(context.Background(), tx, validDeleteConsumerOperation())
	if err != nil || outcome.Failure == nil || *outcome.Failure != models.FailureTargetStateConflict || outcome.HTTPStatus != 409 ||
		outcome.ResultKind != 0 || outcome.ResultGUID != nil {
		t.Fatalf("outcome/error = %#v/%v", outcome, err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if got := script.queryCount(); got != 1 {
		t.Fatalf("queries = %d, want target lock only", got)
	}
	if got := script.execCount(); got != 0 {
		t.Fatalf("state drift issued %d mutations: %v", got, script.allCalls())
	}
}

func TestDeleteUserExecutionFailsClosedForInvalidBindingsAndStorage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*deleteConsumerScript, *models.AdminOperation)
	}{
		{"missing target", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.target = nil }},
		{"corrupt target id", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.target.ID = 0 }},
		{"corrupt target guid", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.target.Guid++ }},
		{"corrupt target version", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.target.AuthVersion = 0 }},
		{"target actor binding", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.target.ID = 41 }},
		{"corrupt session id", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.sessions[0].ID = 0 }},
		{"corrupt session version", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.sessions[0].SessionVersion = 0 }},
		{"corrupt token id", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.tokens[0].ID = 0 }},
		{"corrupt policy head id", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.heads[0].ID = 0 }},
		{"corrupt override id", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.overrides[0].ID = 0 }},
		{"target query", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.failQuery = "users" }},
		{"session query", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.failQuery = "user_sessions" }},
		{"token query", func(script *deleteConsumerScript, _ *models.AdminOperation) { script.failQuery = "gateway_api_tokens" }},
		{"policy head query", func(script *deleteConsumerScript, _ *models.AdminOperation) {
			script.failQuery = "user_permission_heads"
		}},
		{"override query", func(script *deleteConsumerScript, _ *models.AdminOperation) {
			script.failQuery = "user_permission_overrides"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, script := newDeleteConsumerDB(t)
			op := validDeleteConsumerOperation()
			tc.mutate(script, &op)
			execution := newDeleteConsumerExecution(t, deleteWriterIntent(), &deleteConsumerClock{now: 8_001}, func() int64 { return 7_001 })
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, op)
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) || err.Error() != ErrActionOperationUnavailable.Error() {
				t.Fatalf("outcome/error = %#v/%v", outcome, err)
			}
			_ = tx.Rollback().Error
			if script.execCount() != 0 {
				t.Fatal("failed preflight issued a write")
			}
		})
	}
}

func TestDeleteUserExecutionRejectsInvalidInvocationAndOperationBeforeSQL(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*models.AdminOperation)
	}{
		{"id", func(op *models.AdminOperation) { op.ID = 0 }},
		{"guid", func(op *models.AdminOperation) { op.Guid = 0 }},
		{"created at", func(op *models.AdminOperation) { op.CreatedAt = 0 }},
		{"updated at", func(op *models.AdminOperation) { op.UpdatedAt = op.CreatedAt - 1 }},
		{"future audit", func(op *models.AdminOperation) { op.UpdatedAt = 8_002 }},
		{"created by", func(op *models.AdminOperation) { value := int64(42); op.CreatedBy = &value }},
		{"updated by", func(op *models.AdminOperation) { op.UpdatedBy = nil }},
		{"deleted", func(op *models.AdminOperation) { op.IsDeleted = 1 }},
		{"actor", func(op *models.AdminOperation) { op.ActorUserID = 0 }},
		{"actor version", func(op *models.AdminOperation) { op.ActorAuthVersion = 0 }},
		{"session", func(op *models.AdminOperation) { op.SessionID = 0 }},
		{"action", func(op *models.AdminOperation) { op.Action = int(actionsecurity.ActionUsersPromote) }},
		{"verification", func(op *models.AdminOperation) { op.VerificationID = nil }},
		{"state", func(op *models.AdminOperation) { op.State = models.OperationSucceeded }},
		{"public ref", func(op *models.AdminOperation) { op.PublicRef = "op_invalid" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			db, script := newDeleteConsumerDB(t)
			op := validDeleteConsumerOperation()
			tc.mutate(&op)
			execution := newDeleteConsumerExecution(t, deleteWriterIntent(), &deleteConsumerClock{now: 8_001}, func() int64 { return 7_001 })
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, op)
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) {
				t.Fatalf("outcome/error = %#v/%v", outcome, err)
			}
			_ = tx.Rollback().Error
			if script.queryCount()+script.execCount() != 0 {
				t.Fatalf("invalid operation reached SQL: %v", script.allCalls())
			}
		})
	}

	db, script := newDeleteConsumerDB(t)
	execution := newDeleteConsumerExecution(t, deleteWriterIntent(), &deleteConsumerClock{now: 8_001}, func() int64 { return 7_001 })
	tx := db.Begin()
	if outcome, err := execution.Execute(nil, tx, validDeleteConsumerOperation()); outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil context outcome/error = %#v/%v", outcome, err)
	}
	_ = tx.Rollback().Error
	if script.queryCount()+script.execCount() != 0 {
		t.Fatal("nil context reached SQL")
	}
}

func TestDeleteUserExecutionSanitizesEveryWriteFailureAndRowsMismatch(t *testing.T) {
	stages := []string{"user_sessions", "gateway_api_tokens", "user_permission_heads", "user_permission_overrides", "users", "auth_audit_events"}
	for _, stage := range stages {
		for _, mode := range []string{"error", "rows"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				db, script := newDeleteConsumerDB(t)
				if mode == "error" {
					script.failExec = stage
				} else {
					script.rowsAffected = map[string]int64{stage: 0}
				}
				execution := newDeleteConsumerExecution(t, deleteWriterIntent(), &deleteConsumerClock{now: 8_001}, func() int64 { return 7_001 })
				tx := db.Begin()
				outcome, err := execution.Execute(context.Background(), tx, validDeleteConsumerOperation())
				if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) ||
					strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), stage) {
					t.Fatalf("unsanitized outcome/error = %#v/%v", outcome, err)
				}
				if rollbackErr := tx.Rollback().Error; rollbackErr != nil {
					t.Fatal(rollbackErr)
				}
				if len(script.committedCalls()) != script.queryCount() {
					t.Fatalf("failed execution committed writes: %v", script.committedCalls())
				}
				copied := *execution
				retry := db.Begin()
				if retryOutcome, retryErr := copied.Execute(context.Background(), retry, validDeleteConsumerOperation()); retryOutcome != (TerminalOutcome{}) || !errors.Is(retryErr, ErrActionOperationUnavailable) {
					t.Fatalf("uncertain execution was reusable: %#v/%v", retryOutcome, retryErr)
				}
				_ = retry.Rollback().Error
			})
		}
	}
}

func TestDeleteUserExecutionIsOneShotAcrossCopiesAndConcurrency(t *testing.T) {
	db, script := newDeleteConsumerDB(t)
	execution := newDeleteConsumerExecution(t, deleteWriterIntent(), &deleteConsumerClock{now: 8_001}, func() int64 { return 7_001 })
	var successes atomic.Int32
	var unavailable atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	for i := 0; i < 24; i++ {
		wait.Add(1)
		copyExecution := *execution
		go func(candidate DeleteUserExecution) {
			defer wait.Done()
			<-start
			tx := db.Begin()
			outcome, err := candidate.Execute(context.Background(), tx, validDeleteConsumerOperation())
			if err == nil && outcome.Failure == nil && outcome.ResultKind == models.ResultUser {
				successes.Add(1)
				_ = tx.Commit().Error
				return
			}
			if errors.Is(err, ErrActionOperationUnavailable) {
				unavailable.Add(1)
			}
			_ = tx.Rollback().Error
		}(copyExecution)
	}
	close(start)
	wait.Wait()
	if successes.Load() != 1 || unavailable.Load() != 23 {
		t.Fatalf("success/unavailable = %d/%d, want 1/23", successes.Load(), unavailable.Load())
	}
	if script.execKindCount("auth_audit_events") != 1 {
		t.Fatalf("auth audit writes = %d, want 1", script.execKindCount("auth_audit_events"))
	}
}

type deleteConsumerClock struct {
	now   int64
	calls atomic.Int32
}

func (clock *deleteConsumerClock) NowMillis() int64 {
	clock.calls.Add(1)
	return clock.now
}

func newDeleteConsumerExecution(t *testing.T, intent actionsecurity.DeleteUserIntent, clock *deleteConsumerClock, nextGUID func() int64) *DeleteUserExecution {
	t.Helper()
	execution, err := NewDeleteUserExecution(intent, nextGUID, clock)
	if err != nil {
		t.Fatal(err)
	}
	return execution
}

func validDeleteConsumerOperation() models.AdminOperation {
	actor := int64(41)
	verification := int64(51)
	return models.AdminOperation{
		ID:          31,
		AuditFields: models.AuditFields{Guid: 3_001, CreatedAt: 7_001, CreatedBy: &actor, UpdatedAt: 8_001, UpdatedBy: &actor},
		ActorUserID: 41, ActorAuthVersion: 3, SessionID: 45, Action: int(actionsecurity.ActionUsersDelete), VerificationID: &verification,
		State: models.OperationProcessing, PublicRef: deleteWriterPublicRef, QueryExpiresAt: 9_001,
	}
}

const deleteConsumerDriverName = "porsche_delete_action_consumer"

var (
	deleteConsumerDriverOnce sync.Once
	deleteConsumerDriverSeq  atomic.Uint64
	deleteConsumerScripts    sync.Map
)

type deleteConsumerCall struct {
	kind  string
	table string
	query string
	args  []driver.NamedValue
}

type deleteConsumerScript struct {
	mu               sync.Mutex
	now              int64
	target           *models.User
	sessions         []models.Session
	tokens           []models.GatewayAPIToken
	heads            []models.PermissionPolicyHead
	overrides        []models.PermissionOverride
	failQuery        string
	failExec         string
	rowsAffected     map[string]int64
	observed         []deleteConsumerCall
	committed        []deleteConsumerCall
	lockedSessionIDs []int64
	nestedBegins     atomic.Int32
}

type deleteConsumerDriver struct{}
type deleteConsumerConn struct {
	script *deleteConsumerScript
	tx     *deleteConsumerTx
}
type deleteConsumerTx struct {
	conn    *deleteConsumerConn
	pending []deleteConsumerCall
}
type deleteConsumerRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}
type deleteConsumerResult int64

func (deleteConsumerDriver) Open(name string) (driver.Conn, error) {
	value, ok := deleteConsumerScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown delete consumer script")
	}
	return &deleteConsumerConn{script: value.(*deleteConsumerScript)}, nil
}

func (conn *deleteConsumerConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare disabled")
}
func (conn *deleteConsumerConn) Close() error { return nil }
func (conn *deleteConsumerConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}
func (conn *deleteConsumerConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if conn.tx != nil {
		conn.script.nestedBegins.Add(1)
		return nil, errors.New("nested transaction")
	}
	conn.tx = &deleteConsumerTx{conn: conn}
	return conn.tx, nil
}
func (conn *deleteConsumerConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case actionsecurity.Action:
		value.Value = int64(typed)
	case models.UserRole:
		value.Value = int64(typed)
	case models.UserStatus:
		value.Value = int64(typed)
	case models.GatewayTokenStatus:
		value.Value = int64(typed)
	case models.AuthAuditEventType:
		value.Value = int64(typed)
	case *int64:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = *typed
		}
	}
	return nil
}

func (conn *deleteConsumerConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if conn.tx == nil {
		return nil, errors.New("private query outside transaction")
	}
	table := deleteConsumerTable(query)
	call := deleteConsumerCall{kind: "query", table: table, query: query, args: append([]driver.NamedValue(nil), args...)}
	conn.script.mu.Lock()
	conn.script.observed = append(conn.script.observed, call)
	fail := conn.script.failQuery == table
	conn.script.mu.Unlock()
	if fail {
		return nil, errors.New("private target lookup failure")
	}
	switch table {
	case "users":
		conn.script.mu.Lock()
		defer conn.script.mu.Unlock()
		if conn.script.target == nil {
			return &deleteConsumerRows{columns: []string{"id", "guid", "role", "status", "is_deleted", "auth_version"}}, nil
		}
		u := *conn.script.target
		return deleteConsumerRow([]string{"id", "guid", "role", "status", "is_deleted", "auth_version"}, []driver.Value{u.ID, u.Guid, int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}), nil
	case "user_sessions":
		conn.script.mu.Lock()
		defer conn.script.mu.Unlock()
		values := make([][]driver.Value, 0, len(conn.script.sessions))
		conn.script.lockedSessionIDs = nil
		for _, row := range conn.script.sessions {
			if strings.Contains(query, "expires_at > ?") && row.ExpiresAt <= conn.script.now {
				continue
			}
			values = append(values, []driver.Value{row.ID, int64(row.SessionVersion)})
			conn.script.lockedSessionIDs = append(conn.script.lockedSessionIDs, row.ID)
		}
		return &deleteConsumerRows{columns: []string{"id", "session_version"}, values: values}, nil
	case "gateway_api_tokens", "user_permission_heads", "user_permission_overrides":
		return conn.script.idRows(table)
	default:
		return nil, fmt.Errorf("private unexpected query: %s", query)
	}
}

func (conn *deleteConsumerConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if conn.tx == nil {
		return nil, errors.New("private write outside transaction")
	}
	table := deleteConsumerTable(query)
	call := deleteConsumerCall{kind: "exec", table: table, query: query, args: append([]driver.NamedValue(nil), args...)}
	conn.script.mu.Lock()
	conn.script.observed = append(conn.script.observed, call)
	fail := conn.script.failExec == table
	affected, overridden := conn.script.rowsAffected[table]
	if !overridden {
		switch table {
		case "user_sessions":
			for _, row := range conn.script.sessions {
				if !strings.Contains(query, "expires_at > ?") || row.ExpiresAt > conn.script.now {
					affected++
				}
			}
		case "gateway_api_tokens":
			affected = int64(len(conn.script.tokens))
		case "user_permission_heads":
			affected = int64(len(conn.script.heads))
		case "user_permission_overrides":
			affected = int64(len(conn.script.overrides))
		case "users", "auth_audit_events":
			affected = 1
		default:
			conn.script.mu.Unlock()
			return nil, fmt.Errorf("private unexpected write: %s", query)
		}
	}
	conn.script.mu.Unlock()
	if fail {
		return nil, errors.New("private secret database failure")
	}
	conn.tx.pending = append(conn.tx.pending, call)
	return deleteConsumerResult(affected), nil
}

func (tx *deleteConsumerTx) Commit() error {
	tx.conn.script.mu.Lock()
	tx.conn.script.committed = append(tx.conn.script.committed, tx.pending...)
	tx.conn.script.mu.Unlock()
	tx.conn.tx = nil
	return nil
}
func (tx *deleteConsumerTx) Rollback() error       { tx.conn.tx = nil; return nil }
func (rows *deleteConsumerRows) Columns() []string { return rows.columns }
func (rows *deleteConsumerRows) Close() error      { return nil }
func (rows *deleteConsumerRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}
func (result deleteConsumerResult) LastInsertId() (int64, error) { return 1, nil }
func (result deleteConsumerResult) RowsAffected() (int64, error) { return int64(result), nil }

func (script *deleteConsumerScript) idRows(table string) (driver.Rows, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	var ids []int64
	switch table {
	case "gateway_api_tokens":
		for _, row := range script.tokens {
			ids = append(ids, row.ID)
		}
	case "user_permission_heads":
		for _, row := range script.heads {
			ids = append(ids, row.ID)
		}
	case "user_permission_overrides":
		for _, row := range script.overrides {
			ids = append(ids, row.ID)
		}
	}
	values := make([][]driver.Value, 0, len(ids))
	for _, id := range ids {
		values = append(values, []driver.Value{id})
	}
	return &deleteConsumerRows{columns: []string{"id"}, values: values}, nil
}
func deleteConsumerRow(columns []string, values []driver.Value) *deleteConsumerRows {
	return &deleteConsumerRows{columns: columns, values: [][]driver.Value{values}}
}
func deleteConsumerTable(query string) string {
	for _, table := range []string{"auth_audit_events", "user_permission_overrides", "user_permission_heads", "gateway_api_tokens", "user_sessions", "users"} {
		if strings.Contains(query, "`"+table+"`") {
			return table
		}
	}
	return ""
}

func newDeleteConsumerDB(t *testing.T) (*gorm.DB, *deleteConsumerScript) {
	t.Helper()
	deleteConsumerDriverOnce.Do(func() { sql.Register(deleteConsumerDriverName, deleteConsumerDriver{}) })
	phone, username, password, nickname := "13800000000", "retained_user", "secret hash", "delete me"
	realName, idCard := "Sensitive", "card hash"
	expires := int64(10_000)
	script := &deleteConsumerScript{
		now: 8_001,
		target: &models.User{ID: 61, AuditFields: models.AuditFields{Guid: 6_001}, Phone: &phone, Username: &username, PasswordHash: &password,
			Nickname: &nickname, RealName: &realName, IDCardHash: &idCard, IsVerified: true, PlanType: models.PlanProfessional,
			Status: models.UserStatusActive, Role: models.UserRoleUser, AuthVersion: 7, AllowedModels: models.JSONSlice{"retained"}, DailyCallLimit: 50},
		sessions:     []models.Session{{ID: 71, SessionVersion: 2, ExpiresAt: 9_000}, {ID: 72, SessionVersion: 3, ExpiresAt: 10_000}, {ID: 73, SessionVersion: 4, ExpiresAt: 7_000}},
		tokens:       []models.GatewayAPIToken{{ID: 81, Status: models.GatewayTokenActive}, {ID: 82, Status: models.GatewayTokenActive, ExpiresAt: &expires}},
		heads:        []models.PermissionPolicyHead{{ID: 91}},
		overrides:    []models.PermissionOverride{{ID: 101}, {ID: 102}},
		rowsAffected: map[string]int64{},
	}
	dsn := fmt.Sprintf("delete-consumer-%d", deleteConsumerDriverSeq.Add(1))
	deleteConsumerScripts.Store(dsn, script)
	t.Cleanup(func() { deleteConsumerScripts.Delete(dsn) })
	sqlDB, err := sql.Open(deleteConsumerDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db, script
}

func (script *deleteConsumerScript) allCalls() []deleteConsumerCall {
	script.mu.Lock()
	defer script.mu.Unlock()
	return append([]deleteConsumerCall(nil), script.observed...)
}

func (script *deleteConsumerScript) lockedSessions() []int64 {
	script.mu.Lock()
	defer script.mu.Unlock()
	return append([]int64(nil), script.lockedSessionIDs...)
}

func (script *deleteConsumerScript) committedCalls() []deleteConsumerCall {
	script.mu.Lock()
	defer script.mu.Unlock()
	queries := make([]deleteConsumerCall, 0, len(script.observed))
	for _, call := range script.observed {
		if call.kind == "query" {
			queries = append(queries, call)
		}
	}
	return append(queries, script.committed...)
}
func (script *deleteConsumerScript) queryCount() int {
	count := 0
	for _, call := range script.allCalls() {
		if call.kind == "query" {
			count++
		}
	}
	return count
}
func (script *deleteConsumerScript) execCount() int {
	count := 0
	for _, call := range script.allCalls() {
		if call.kind == "exec" {
			count++
		}
	}
	return count
}
func (script *deleteConsumerScript) execKindCount(table string) int {
	count := 0
	for _, call := range script.committedCalls() {
		if call.kind == "exec" && call.table == table {
			count++
		}
	}
	return count
}
func deleteConsumerCallKinds(calls []deleteConsumerCall) []string {
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, call.kind+":"+call.table)
	}
	return out
}
func assertDeleteConsumerQuery(t *testing.T, call deleteConsumerCall, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(call.query, fragment) {
			t.Errorf("query %q missing %q", call.query, fragment)
		}
	}
}

func assertDeleteConsumerArgs(t *testing.T, call deleteConsumerCall, want ...any) {
	t.Helper()
	got := make([]any, 0, len(call.args))
	for _, arg := range call.args {
		got = append(got, arg.Value)
	}
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Errorf("SQL args = %v, want %v; SQL=%s", got, want, call.query)
	}
}
func assertDeleteConsumerUpdate(t *testing.T, call deleteConsumerCall, wantColumns []string, where string) {
	t.Helper()
	columns := deleteConsumerSetColumns(call.query)
	if len(columns) != len(wantColumns) {
		t.Fatalf("update columns = %v, want %v; SQL=%s", columns, wantColumns, call.query)
	}
	for _, column := range wantColumns {
		if !columns[column] {
			t.Errorf("update missing column %q: %s", column, call.query)
		}
	}
	if !strings.Contains(call.query, where) {
		t.Errorf("update predicate mismatch: %s; want %s", call.query, where)
	}
}
func deleteConsumerSetColumns(query string) map[string]bool {
	columns := map[string]bool{}
	start, end := strings.Index(query, " SET "), strings.Index(query, " WHERE ")
	if start < 0 || end <= start {
		return columns
	}
	for _, assignment := range strings.Split(query[start+5:end], ",") {
		parts := strings.SplitN(assignment, "=", 2)
		if len(parts) == 2 {
			columns[strings.Trim(strings.TrimSpace(parts[0]), "`")] = true
		}
	}
	return columns
}
func deleteConsumerInsertValues(t *testing.T, call deleteConsumerCall) map[string]any {
	t.Helper()
	open, close := strings.Index(call.query, "("), strings.Index(call.query, ") VALUES")
	if open < 0 || close <= open {
		t.Fatalf("unrecognized insert: %s", call.query)
	}
	columns := strings.Split(call.query[open+1:close], ",")
	if len(columns) != len(call.args) {
		t.Fatalf("insert columns/args = %d/%d: %s", len(columns), len(call.args), call.query)
	}
	values := make(map[string]any, len(columns))
	for index, column := range columns {
		values[strings.Trim(strings.TrimSpace(column), "`")] = call.args[index].Value
	}
	return values
}
