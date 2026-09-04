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
	actor         models.User
	sessions      []models.Session
	operation     *models.AdminOperation
	verification  *models.AdminActionVerification
	queries       []string
	queryArgs     [][]driver.NamedValue
	execs         []string
	execArgs      [][]driver.NamedValue
	beginCount    int
	commitCount   int
	rollbackCount int
	failExec      bool
	failExecAt    int
	execError     error
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
func (c *actionOperationConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.script.mu.Lock()
	c.script.beginCount++
	c.script.mu.Unlock()
	return &actionOperationTx{script: c.script}, nil
}
func (c *actionOperationConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *actionOperationConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.script
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, query)
	s.queryArgs = append(s.queryArgs, append([]driver.NamedValue(nil), args...))
	switch {
	case strings.Contains(query, "FROM `users`"):
		if !strings.Contains(query, "id = ?") || !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("actor not locked by id")
		}
		u := s.actor
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
		if s.operation == nil {
			return operationRows(operationColumns(), nil), nil
		}
		return operationRows(operationColumns(), [][]driver.Value{operationValues(*s.operation)}), nil
	case strings.Contains(query, "FROM `admin_action_verifications`"):
		if !strings.Contains(query, "ticket_hmac = ?") || !strings.Contains(query, "FOR UPDATE") {
			return nil, errors.New("verification not locked by digest")
		}
		if s.verification == nil {
			return operationRows(verificationColumns(), nil), nil
		}
		return operationRows(verificationColumns(), [][]driver.Value{verificationValues(*s.verification)}), nil
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
	if s.failExec || (s.failExecAt > 0 && len(s.execs) == s.failExecAt) {
		if s.execError != nil {
			return nil, s.execError
		}
		return nil, errors.New("scripted operation write failure")
	}
	return actionOperationResult{id: 30, affected: 1}, nil
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
	if got := actionOperationQueryKinds(script.queries); strings.Join(got, ",") != "actor,session,operation,verification" {
		t.Fatalf("lock order = %v", got)
	}
	if script.commitCount != 1 || script.rollbackCount != 0 {
		t.Fatalf("commit/rollback = %d/%d", script.commitCount, script.rollbackCount)
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
}

func actionOperationQueryKinds(queries []string) []string {
	out := make([]string, 0, len(queries))
	for _, query := range queries {
		switch {
		case strings.Contains(query, "FROM `users`"):
			out = append(out, "actor")
		case strings.Contains(query, "FROM `user_sessions`"):
			out = append(out, "session")
		case strings.Contains(query, "FROM `admin_operations`"):
			out = append(out, "operation")
		case strings.Contains(query, "FROM `admin_action_verifications`"):
			out = append(out, "verification")
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
