package service

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const actionIssueScriptDriverName = "porsche_action_issue_script"

var (
	actionIssueScriptOnce sync.Once
	actionIssueScriptSeq  atomic.Uint64
	actionIssueScripts    sync.Map
)

type actionIssueSQLCall struct {
	query string
	args  []driver.NamedValue
}

type actionIssueSQLScript struct {
	mu         sync.Mutex
	now        int64
	actor      models.User
	sessions   []models.Session
	target     *models.User
	targetGUID int64
	policyHead *models.PermissionPolicyHead
	overrides  []models.PermissionOverride
	groups     []models.BusinessGroup
	queries    []actionIssueSQLCall
	execs      []actionIssueSQLCall
	failQuery  string
	scanError  string
	failExecAt int
	failCommit bool
	begins     int
	commits    int
	rollbacks  int
}

type actionIssueDriver struct{}
type actionIssueConn struct{ script *actionIssueSQLScript }
type actionIssueTx struct{ script *actionIssueSQLScript }
type actionIssueRows struct {
	columns []string
	values  [][]driver.Value
	index   int
	nextErr error
}
type actionIssueResult struct{ id int64 }

func (actionIssueDriver) Open(name string) (driver.Conn, error) {
	value, ok := actionIssueScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown action issue script")
	}
	return &actionIssueConn{script: value.(*actionIssueSQLScript)}, nil
}
func (c *actionIssueConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare disabled")
}
func (c *actionIssueConn) Close() error { return nil }
func (c *actionIssueConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *actionIssueConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.script.mu.Lock()
	c.script.begins++
	c.script.mu.Unlock()
	return &actionIssueTx{script: c.script}, nil
}
func (c *actionIssueConn) CheckNamedValue(*driver.NamedValue) error { return nil }
func (c *actionIssueConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.script.mu.Lock()
	defer c.script.mu.Unlock()
	c.script.queries = append(c.script.queries, actionIssueSQLCall{query: query, args: copyNamedValues(args)})
	kind := ""
	switch {
	case strings.Contains(query, "FROM `users`") && strings.Contains(query, "guid = ?"):
		kind = "target"
		if err := requireActionIssueQuery(query, args, []string{"FROM `users`", "guid = ?", "is_deleted = 0", "FOR UPDATE"}, c.script.targetGUID, int64(1)); err != nil {
			return nil, err
		}
		if c.script.failQuery == kind {
			return nil, errors.New("private target query failure")
		}
		columns := []string{"id", "guid", "role", "status", "is_deleted", "auth_version"}
		if c.script.target == nil || c.script.target.IsDeleted != 0 {
			return scriptedActionIssueRows(kind, c.script, columns, nil), nil
		}
		u := c.script.target
		return scriptedActionIssueRows(kind, c.script, columns, [][]driver.Value{{u.ID, u.Guid, int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}}), nil
	case strings.Contains(query, "FROM `users`"):
		kind = "actor"
		if err := requireActionIssueQuery(query, args, []string{"FROM `users`", "id = ?", "FOR UPDATE"}, c.script.actor.ID, int64(1)); err != nil {
			return nil, err
		}
		if c.script.failQuery == kind {
			return nil, errors.New("private actor query failure")
		}
		u := &c.script.actor
		var password driver.Value
		if u.PasswordHash != nil {
			password = *u.PasswordHash
		}
		columns := []string{"id", "guid", "password_hash", "role", "status", "is_deleted", "auth_version"}
		return scriptedActionIssueRows(kind, c.script, columns, [][]driver.Value{{u.ID, u.Guid, password, int64(u.Role), int64(u.Status), int64(u.IsDeleted), int64(u.AuthVersion)}}), nil
	case strings.Contains(query, "FROM `user_sessions`"):
		kind = "session"
		if err := requireActionIssueQuery(query, args, []string{"FROM `user_sessions`", "user_id = ?", "is_deleted = 0", "revoked_at IS NULL", "expires_at > ?", "ORDER BY id ASC", "FOR UPDATE"}, c.script.actor.ID, c.script.now); err != nil {
			return nil, err
		}
		if strings.Contains(strings.ToUpper(query), " LIMIT ") {
			return nil, errors.New("unexpected session limit")
		}
		if c.script.failQuery == kind {
			return nil, errors.New("private session query failure")
		}
		columns := []string{"id", "guid", "sid", "user_id", "session_version", "is_deleted", "revoked_at", "expires_at"}
		values := make([][]driver.Value, 0, len(c.script.sessions))
		for i := range c.script.sessions {
			s := &c.script.sessions[i]
			var revoked driver.Value
			if s.RevokedAt != nil {
				revoked = *s.RevokedAt
			}
			values = append(values, []driver.Value{s.ID, s.Guid, s.SID, s.UserID, int64(s.SessionVersion), int64(s.IsDeleted), revoked, s.ExpiresAt})
		}
		return scriptedActionIssueRows(kind, c.script, columns, values), nil
	case strings.Contains(query, "FROM `user_permission_heads`"):
		kind = "policy_head"
		if err := requireActionIssueQuery(query, args, []string{"FROM `user_permission_heads`", "user_id = ?"}, c.script.actor.ID, int64(1)); err != nil {
			return nil, err
		}
		if c.script.failQuery == kind {
			return nil, errors.New("private policy head failure")
		}
		columns := []string{"id", "guid", "is_deleted", "policy_version", "catalog_version", "rule_count"}
		if c.script.policyHead == nil {
			return scriptedActionIssueRows(kind, c.script, columns, nil), nil
		}
		h := c.script.policyHead
		return scriptedActionIssueRows(kind, c.script, columns, [][]driver.Value{{h.ID, h.Guid, int64(h.IsDeleted), h.PolicyVersion, int64(h.CatalogVersion), int64(h.RuleCount)}}), nil
	case strings.Contains(query, "FROM `user_permission_overrides`"):
		kind = "policy_rules"
		if len(args) < 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(c.script.actor.ID) {
			return nil, errors.New("unexpected policy rule vars")
		}
		if c.script.failQuery == kind {
			return nil, errors.New("private policy rules failure")
		}
		if c.script.policyHead == nil {
			if err := requireActionIssueQuery(query, args, []string{"FROM `user_permission_overrides`", "user_id = ?", "LIMIT ?"}, c.script.actor.ID, int64(1)); err != nil {
				return nil, err
			}
			return scriptedActionIssueRows(kind, c.script, []string{"id"}, nil), nil
		}
		if err := requireActionIssueQuery(query, args, []string{"FROM `user_permission_overrides`", "user_id = ?", "is_deleted <> 1", "ORDER BY capability", "LIMIT ?"}, c.script.actor.ID, int64(25)); err != nil {
			return nil, err
		}
		columns := []string{"id", "guid", "is_deleted", "policy_version", "capability", "effect"}
		values := make([][]driver.Value, 0, len(c.script.overrides))
		for i := range c.script.overrides {
			r := &c.script.overrides[i]
			values = append(values, []driver.Value{r.ID, r.Guid, int64(r.IsDeleted), r.PolicyVersion, int64(r.Capability), int64(r.Effect)})
		}
		return scriptedActionIssueRows(kind, c.script, columns, values), nil
	case strings.Contains(query, "FROM `business_groups`"):
		kind = "group"
		fragments := []string{"FROM `business_groups`", "ORDER BY id ASC", "LIMIT ?", "FOR UPDATE"}
		var expected []any
		if strings.Contains(query, "group_key = ?") {
			fragments = append(fragments, "group_key = ?", "is_deleted = 0")
			expected = []any{"default", int64(2)}
		} else if strings.Contains(query, "guid = ?") {
			expected = []any{c.script.groups[0].Guid, int64(2)}
		} else {
			return nil, errors.New("missing group selector")
		}
		if err := requireActionIssueQuery(query, args, fragments, expected...); err != nil {
			return nil, err
		}
		if c.script.failQuery == kind {
			return nil, errors.New("private group query failure")
		}
		columns := []string{"id", "guid", "group_key", "display_name", "status", "is_deleted"}
		values := make([][]driver.Value, 0, len(c.script.groups))
		for i := range c.script.groups {
			group := &c.script.groups[i]
			values = append(values, []driver.Value{group.ID, group.Guid, group.Key, group.DisplayName, int64(group.Status), int64(group.IsDeleted)})
		}
		return scriptedActionIssueRows(kind, c.script, columns, values), nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

func requireActionIssueQuery(query string, args []driver.NamedValue, fragments []string, expected ...any) error {
	for _, fragment := range fragments {
		if !strings.Contains(query, fragment) {
			return fmt.Errorf("missing query fragment %q", fragment)
		}
	}
	if len(args) != len(expected) {
		return fmt.Errorf("query vars count %d, want %d", len(args), len(expected))
	}
	for i := range expected {
		if fmt.Sprint(args[i].Value) != fmt.Sprint(expected[i]) {
			return fmt.Errorf("query var %d = %v, want %v", i, args[i].Value, expected[i])
		}
	}
	return nil
}

func scriptedActionIssueRows(kind string, script *actionIssueSQLScript, columns []string, values [][]driver.Value) *actionIssueRows {
	rows := &actionIssueRows{columns: columns, values: values}
	if script.scanError == kind {
		rows.nextErr = errors.New("private row scan failure")
	}
	return rows
}
func (c *actionIssueConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.script.mu.Lock()
	defer c.script.mu.Unlock()
	c.script.execs = append(c.script.execs, actionIssueSQLCall{query: query, args: copyNamedValues(args)})
	if c.script.failExecAt > 0 && len(c.script.execs) == c.script.failExecAt {
		return nil, errors.New("scripted write failure")
	}
	return actionIssueResult{id: int64(len(c.script.execs))}, nil
}
func (tx *actionIssueTx) Commit() error {
	tx.script.mu.Lock()
	defer tx.script.mu.Unlock()
	tx.script.commits++
	if tx.script.failCommit {
		return errors.New("scripted commit failure")
	}
	return nil
}
func (tx *actionIssueTx) Rollback() error {
	tx.script.mu.Lock()
	tx.script.rollbacks++
	tx.script.mu.Unlock()
	return nil
}
func (r *actionIssueRows) Columns() []string { return r.columns }
func (r *actionIssueRows) Close() error      { return nil }
func (r *actionIssueRows) Next(dest []driver.Value) error {
	if r.nextErr != nil {
		err := r.nextErr
		r.nextErr = nil
		return err
	}
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
func (r actionIssueResult) LastInsertId() (int64, error) { return r.id, nil }
func (actionIssueResult) RowsAffected() (int64, error)   { return 1, nil }

func copyNamedValues(values []driver.NamedValue) []driver.NamedValue {
	return append([]driver.NamedValue(nil), values...)
}

type actionIssueObservingReader struct {
	password []byte
	data     []byte
	err      error
	cleared  bool
}

func (r *actionIssueObservingReader) Read(p []byte) (int, error) {
	r.cleared = bytes.Equal(r.password, make([]byte, len(r.password)))
	if r.err != nil {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

type actionIssueSequenceClock struct {
	values []int64
	index  int
}

func (c *actionIssueSequenceClock) NowMillis() int64 {
	if c.index >= len(c.values) {
		return c.values[len(c.values)-1]
	}
	value := c.values[c.index]
	c.index++
	return value
}

func openActionIssueScriptDB(t *testing.T, script *actionIssueSQLScript, logBuffer *bytes.Buffer) *gorm.DB {
	t.Helper()
	actionIssueScriptOnce.Do(func() { sql.Register(actionIssueScriptDriverName, actionIssueDriver{}) })
	name := fmt.Sprintf("script-%d", actionIssueScriptSeq.Add(1))
	actionIssueScripts.Store(name, script)
	t.Cleanup(func() { actionIssueScripts.Delete(name) })
	sqlDB, err := sql.Open(actionIssueScriptDriverName, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	gormLogger := logger.Default.LogMode(logger.Silent)
	if logBuffer != nil {
		gormLogger = logger.New(log.New(logBuffer, "", 0), logger.Config{LogLevel: logger.Info, ParameterizedQueries: false})
	}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: gormLogger})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func actionIssueScriptFixture(t *testing.T, now int64) (*actionIssueSQLScript, ActionActor, string) {
	t.Helper()
	password := "Task7-script-password!"
	hash := actionIssuePasswordHash(t, password)
	sid := "11111111-2222-4333-8444-555555555555"
	actor := models.User{ID: 10, AuditFields: models.AuditFields{Guid: 1001}, PasswordHash: &hash, Role: models.UserRoleRoot, Status: models.UserStatusActive, AuthVersion: 7}
	session := models.Session{ID: 20, AuditFields: models.AuditFields{Guid: 2001}, SID: sid, UserID: actor.ID, SessionVersion: 3, ExpiresAt: now + 60_000}
	target := &models.User{ID: 30, AuditFields: models.AuditFields{Guid: testNoopTargetGUID}, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 4}
	group := models.BusinessGroup{ID: 61, AuditFields: models.AuditFields{Guid: 6101}, Key: "default", DisplayName: "Default", Status: models.BusinessGroupStatusActive}
	script := &actionIssueSQLScript{now: now, actor: actor, sessions: []models.Session{session}, target: target, targetGUID: target.Guid, groups: []models.BusinessGroup{group}}
	claims := ActionActor{UserID: actor.ID, UserGUID: actor.Guid, AuthVersion: actor.AuthVersion, SessionSID: sid, SessionVersion: session.SessionVersion}
	return script, claims, password
}

func TestActionIssueScriptDriverRejectsWrongShapeAndVars(t *testing.T) {
	script, _, _ := actionIssueScriptFixture(t, 1_800_000_000_000)
	conn := &actionIssueConn{script: script}
	for _, tc := range []struct {
		name  string
		query string
		args  []driver.NamedValue
	}{
		{name: "missing actor where", query: "SELECT id FROM `users` FOR UPDATE", args: []driver.NamedValue{{Ordinal: 1, Value: script.actor.ID}, {Ordinal: 2, Value: int64(1)}}},
		{name: "wrong actor arg", query: "SELECT id FROM `users` WHERE id = ? LIMIT ? FOR UPDATE", args: []driver.NamedValue{{Ordinal: 1, Value: int64(999)}, {Ordinal: 2, Value: int64(1)}}},
		{name: "raw sid session where", query: "SELECT id FROM `user_sessions` WHERE sid = ? FOR UPDATE", args: []driver.NamedValue{{Ordinal: 1, Value: script.sessions[0].SID}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := conn.QueryContext(context.Background(), tc.query, tc.args); err == nil {
				t.Fatal("script driver accepted malformed query contract")
			}
		})
	}
}

func TestDeleteVerificationIssueAuthorizationMatrix(t *testing.T) {
	const now int64 = 1_800_000_000_000
	for _, tc := range []struct {
		name       string
		actorRole  models.UserRole
		targetRole models.UserRole
		allow      bool
	}{
		{name: "root to user", actorRole: models.UserRoleRoot, targetRole: models.UserRoleUser},
		{name: "root to admin", actorRole: models.UserRoleRoot, targetRole: models.UserRoleAdmin},
		{name: "authorized admin to user", actorRole: models.UserRoleAdmin, targetRole: models.UserRoleUser, allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, actor, passwordText := actionIssueScriptFixture(t, now)
			script.actor.Role = tc.actorRole
			script.target.Role = tc.targetRole
			if tc.allow {
				script.policyHead = &models.PermissionPolicyHead{ID: 40, AuditFields: models.AuditFields{Guid: 4001}, UserID: script.actor.ID, PolicyVersion: 2, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
				script.overrides = []models.PermissionOverride{{ID: 41, AuditFields: models.AuditFields{Guid: 4101}, UserID: script.actor.ID, PolicyVersion: 2, Capability: 12, Effect: 2}}
			}
			password := []byte(passwordText)
			service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, &actionIssueClock{now: now}, bytes.NewReader(bytes.Repeat([]byte{1}, 32)), func() int64 { return 9400 })
			result, err := service.Issue(context.Background(), VerificationIssue{
				Action: actionsecurity.ActionUsersDelete, Actor: actor, TargetGUID: &script.target.Guid,
				Intent:          actionsecurity.DeleteUserIntent{TargetGUID: script.target.Guid, ExpectedAuthVersion: script.target.AuthVersion, Reason: "matrix"},
				CurrentPassword: password, TrustedIP: "203.0.113.27",
			})
			if err != nil || result == nil || len(script.execs) != 2 || script.commits != 1 {
				t.Fatalf("result/error/writes/commits = %#v/%v/%d/%d", result, err, len(script.execs), script.commits)
			}
		})
	}
}

func TestCreateAdminVerificationAcceptsFreshRootAndBindsExactCanonicalIntent(t *testing.T) {
	const now int64 = 1_800_000_000_000
	script, actor, currentPassword := actionIssueScriptFixture(t, now)
	script.groups = []models.BusinessGroup{{ID: 62, AuditFields: models.AuditFields{Guid: 6201}, Key: "enterprise", DisplayName: "Enterprise", Status: models.BusinessGroupStatusActive}}
	groupGUID := script.groups[0].Guid
	nickname := "Managed Admin"
	intentPassword := []byte("A03Adm1n!Secret")
	intent := actionsecurity.CreateAccountIntent{
		Username: "managed_admin", Nickname: &nickname, Password: intentPassword, Role: "admin", GroupGUID: &groupGUID,
		PlanType: int(models.PlanEnterprise), AllowedModels: []string{}, DailyCallLimit: 100,
		Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}, {Capability: "users.sessions.read", Effect: 2}},
	}
	service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, &actionIssueClock{now: now}, bytes.NewReader(bytes.Repeat([]byte{0x51}, 32)), func() int64 { return 9701 })
	descriptor := activeCreateVerificationDescriptor(t, actionsecurity.ActionUsersCreateAdmin)
	service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		return descriptor, action == descriptor.Action
	}
	expectedPassword := append([]byte(nil), intentPassword...)
	expectedIntent := intent
	expectedIntent.Password = expectedPassword
	encoded, err := descriptor.Encode(expectedIntent)
	if err != nil {
		t.Fatal(err)
	}
	expectedDigest := service.crypto.IntentDigest(encoded)
	expectedHex := hex.EncodeToString(expectedDigest[:])
	clear(expectedDigest[:])
	clear(encoded)

	issued, err := service.Issue(context.Background(), VerificationIssue{
		Action: descriptor.Action, Actor: actor, Intent: intent, CurrentPassword: []byte(currentPassword), TrustedIP: "203.0.113.30",
	})
	if err != nil || issued == nil {
		t.Fatalf("issued/error = %#v/%v", issued, err)
	}
	if script.commits != 1 || script.rollbacks != 0 || len(script.execs) != 2 {
		t.Fatalf("commit/rollback/writes = %d/%d/%d", script.commits, script.rollbacks, len(script.execs))
	}
	if len(script.queries) != 5 || !strings.Contains(script.queries[4].query, "FROM `business_groups`") || !strings.Contains(script.queries[4].query, "FOR UPDATE") {
		t.Fatalf("locked create-admin query order = %#v", script.queries)
	}
	foundDigest := false
	for _, arg := range script.execs[1].args {
		if value, ok := arg.Value.(string); ok && value == expectedHex {
			foundDigest = true
		}
	}
	if !foundDigest {
		t.Fatalf("verification insert did not bind exact canonical intent digest %q", expectedHex)
	}
	if !bytes.Equal(intentPassword, make([]byte, len(intentPassword))) {
		t.Fatal("create-admin verification retained raw initial password")
	}
}

func TestCreateAdminVerificationRejectsOrdinaryCreateAndNonFreshAuthority(t *testing.T) {
	const now int64 = 1_800_000_000_000
	for _, tc := range []struct {
		name   string
		action actionsecurity.Action
		mutate func(*actionIssueSQLScript, *ActionActor)
		want   error
	}{
		{name: "ordinary create", action: actionsecurity.ActionUsersCreate, want: ErrActionVerificationInactive},
		{name: "admin actor", action: actionsecurity.ActionUsersCreateAdmin, mutate: func(script *actionIssueSQLScript, _ *ActionActor) { script.actor.Role = models.UserRoleAdmin }, want: ErrActionVerificationForbidden},
		{name: "stale auth version", action: actionsecurity.ActionUsersCreateAdmin, mutate: func(_ *actionIssueSQLScript, actor *ActionActor) { actor.AuthVersion++ }, want: ErrActionVerificationForbidden},
		{name: "stale session version", action: actionsecurity.ActionUsersCreateAdmin, mutate: func(_ *actionIssueSQLScript, actor *ActionActor) { actor.SessionVersion++ }, want: ErrActionVerificationForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, actor, currentPassword := actionIssueScriptFixture(t, now)
			if tc.mutate != nil {
				tc.mutate(script, &actor)
			}
			password := []byte("A03Adm1n!Secret")
			intent := actionsecurity.CreateAccountIntent{Username: "managed_admin", Password: password, Role: "admin", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
			service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, &actionIssueClock{now: now}, bytes.NewReader(bytes.Repeat([]byte{0x52}, 32)), func() int64 { return 9702 })
			descriptor := activeCreateVerificationDescriptor(t, tc.action)
			service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
				return descriptor, action == descriptor.Action
			}
			issued, err := service.Issue(context.Background(), VerificationIssue{Action: tc.action, Actor: actor, Intent: intent, CurrentPassword: []byte(currentPassword), TrustedIP: "203.0.113.31"})
			if issued != nil || !errors.Is(err, tc.want) || len(script.execs) != 0 {
				t.Fatalf("issued/error/writes = %#v/%v/%d, want nil/%v/0", issued, err, len(script.execs), tc.want)
			}
			if !bytes.Equal(password, make([]byte, len(password))) {
				t.Fatal("rejected verification retained raw initial password")
			}
		})
	}
}

func TestCreateAdminVerificationValidatesCanonicalGroupPlanAndOverridesUnderLocks(t *testing.T) {
	const now int64 = 1_800_000_000_000
	tests := []struct {
		name   string
		mutate func(*actionIssueSQLScript, *actionsecurity.CreateAccountIntent)
		want   error
	}{
		{name: "missing default group", mutate: func(script *actionIssueSQLScript, _ *actionsecurity.CreateAccountIntent) { script.groups = nil }, want: ErrActionVerificationUnavailable},
		{name: "inactive explicit group", mutate: func(script *actionIssueSQLScript, intent *actionsecurity.CreateAccountIntent) {
			script.groups[0].Status = models.BusinessGroupStatusInactive
			intent.GroupGUID = &script.groups[0].Guid
		}, want: ErrActionVerificationHidden},
		{name: "invalid plan", mutate: func(_ *actionIssueSQLScript, intent *actionsecurity.CreateAccountIntent) { intent.PlanType = 99 }, want: ErrActionVerificationConflict},
		{name: "weak password", mutate: func(_ *actionIssueSQLScript, intent *actionsecurity.CreateAccountIntent) {
			intent.Password = []byte("password")
		}, want: ErrActionVerificationConflict},
		{name: "unknown override", mutate: func(_ *actionIssueSQLScript, intent *actionsecurity.CreateAccountIntent) {
			intent.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "unknown", Effect: 2}}
		}, want: ErrActionVerificationConflict},
		{name: "unsorted overrides", mutate: func(_ *actionIssueSQLScript, intent *actionsecurity.CreateAccountIntent) {
			intent.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}, {Capability: "users.read", Effect: 3}}
		}, want: ErrActionVerificationConflict},
		{name: "noncanonical username", mutate: func(_ *actionIssueSQLScript, intent *actionsecurity.CreateAccountIntent) {
			intent.Username = " managed_admin "
		}, want: ErrActionVerificationConflict},
		{name: "noncanonical defaults", mutate: func(_ *actionIssueSQLScript, intent *actionsecurity.CreateAccountIntent) { intent.DailyCallLimit = 101 }, want: ErrActionVerificationConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script, actor, currentPassword := actionIssueScriptFixture(t, now)
			password := []byte("A03Adm1n!Secret")
			intent := actionsecurity.CreateAccountIntent{Username: "managed_admin", Password: password, Role: "admin", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
			tc.mutate(script, &intent)
			password = intent.Password
			service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, &actionIssueClock{now: now}, &actionIssueObservingReader{password: []byte(currentPassword), data: bytes.Repeat([]byte{0x53}, 32)}, func() int64 { return 9703 })
			descriptor := activeCreateVerificationDescriptor(t, actionsecurity.ActionUsersCreateAdmin)
			service.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
				return descriptor, action == descriptor.Action
			}
			issued, err := service.Issue(context.Background(), VerificationIssue{Action: descriptor.Action, Actor: actor, Intent: intent, CurrentPassword: []byte(currentPassword), TrustedIP: "203.0.113.32"})
			if issued != nil || !errors.Is(err, tc.want) || len(script.execs) != 0 || script.commits != 0 || script.rollbacks != 1 {
				t.Fatalf("issued/error/writes/commits/rollbacks = %#v/%v/%d/%d/%d, want nil/%v/0/0/1", issued, err, len(script.execs), script.commits, script.rollbacks, tc.want)
			}
			if len(script.queries) < 4 || !strings.Contains(script.queries[0].query, "FOR UPDATE") || !strings.Contains(script.queries[1].query, "FOR UPDATE") {
				t.Fatalf("validation occurred before identity locks: %#v", script.queries)
			}
			if !bytes.Equal(password, make([]byte, len(password))) {
				t.Fatal("invalid create-admin intent retained raw initial password")
			}
		})
	}
}

func activeCreateVerificationDescriptor(t *testing.T, action actionsecurity.Action) actionsecurity.Descriptor {
	t.Helper()
	for _, descriptor := range actionsecurity.FutureActionDescriptors() {
		if descriptor.Action == action {
			descriptor.Active = true
			return descriptor
		}
	}
	t.Fatalf("create descriptor %d missing", action)
	return actionsecurity.Descriptor{}
}

func TestDeleteVerificationIssueRejectsLockedIntentBeforePasswordAndWrites(t *testing.T) {
	const now int64 = 1_800_000_000_000
	for _, tc := range []struct {
		name   string
		mutate func(*actionIssueSQLScript, *actionsecurity.DeleteUserIntent)
	}{
		{name: "target binding", mutate: func(_ *actionIssueSQLScript, intent *actionsecurity.DeleteUserIntent) { intent.TargetGUID++ }},
		{name: "stale version", mutate: func(_ *actionIssueSQLScript, intent *actionsecurity.DeleteUserIntent) { intent.ExpectedAuthVersion-- }},
		{name: "disabled target", mutate: func(script *actionIssueSQLScript, _ *actionsecurity.DeleteUserIntent) {
			script.target.Status = models.UserStatusDisabled
		}},
		{name: "max version", mutate: func(script *actionIssueSQLScript, intent *actionsecurity.DeleteUserIntent) {
			script.target.AuthVersion = math.MaxInt32
			intent.ExpectedAuthVersion = math.MaxInt32
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, actor, _ := actionIssueScriptFixture(t, now)
			invalidHash := "password-verifier-must-not-run"
			script.actor.PasswordHash = &invalidHash
			intent := actionsecurity.DeleteUserIntent{TargetGUID: script.target.Guid, ExpectedAuthVersion: script.target.AuthVersion, Reason: "conflict"}
			tc.mutate(script, &intent)
			password := []byte("private-current-password")
			reader := &actionIssueObservingReader{password: password, data: bytes.Repeat([]byte{1}, 32)}
			service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, &actionIssueClock{now: now}, reader, func() int64 { return 9500 })
			result, err := service.Issue(context.Background(), VerificationIssue{
				Action: actionsecurity.ActionUsersDelete, Actor: actor, TargetGUID: &script.target.Guid, Intent: intent,
				CurrentPassword: password, TrustedIP: "203.0.113.28",
			})
			if result != nil || !errors.Is(err, ErrActionVerificationConflict) || err.Error() != ErrActionVerificationConflict.Error() {
				t.Fatalf("result/error = %#v/%v, want fixed conflict", result, err)
			}
			if len(script.execs) != 0 || script.commits != 0 || script.rollbacks != 1 || reader.cleared || len(reader.data) != 32 {
				t.Fatalf("policy rejection side effects writes/commits/rollbacks/random = %d/%d/%d/%t/%d", len(script.execs), script.commits, script.rollbacks, reader.cleared, len(reader.data))
			}
			if !bytes.Equal(password, make([]byte, len(password))) {
				t.Fatal("policy rejection did not clear caller password")
			}
			for _, private := range []string{fmt.Sprint(script.target.Guid), fmt.Sprint(script.target.AuthVersion), script.target.Role.String(), "private-current-password"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("policy conflict leaked %q", private)
				}
			}
		})
	}
}

func TestActionVerificationIssueFailsClosedForUnreviewedFutureActiveAction(t *testing.T) {
	const now int64 = 1_800_000_000_000
	script, actor, _ := actionIssueScriptFixture(t, now)
	invalidHash := "password-verifier-must-not-run"
	script.actor.PasswordHash = &invalidHash
	password := []byte("private-current-password")
	reader := &actionIssueObservingReader{password: password, data: bytes.Repeat([]byte{1}, 32)}
	service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, &actionIssueClock{now: now}, reader, func() int64 { return 9600 })
	result, err := service.Issue(context.Background(), VerificationIssue{
		Action: testNoopAction, Actor: actor, TargetGUID: testNoopTargetGUIDValue(), Intent: testNoopIntent(testNoopTargetGUID, "future"),
		CurrentPassword: password, TrustedIP: "203.0.113.29",
	})
	if result != nil || !errors.Is(err, ErrActionVerificationUnavailable) || len(script.execs) != 0 || reader.cleared || len(reader.data) != 32 {
		t.Fatalf("future action result/error/writes/random = %#v/%v/%d/%t/%d", result, err, len(script.execs), reader.cleared, len(reader.data))
	}
}

func TestActionVerificationIssueContractExists(t *testing.T) {
	var _ *ActionVerificationService
	var _ *IssuedVerification
}

func TestActionVerificationIssueScriptedTransactionIsOrderedSecretFreeAndImmediateClear(t *testing.T) {
	const now int64 = 1_800_000_000_000
	script, actor, passwordText := actionIssueScriptFixture(t, now)
	var logs bytes.Buffer
	db := openActionIssueScriptDB(t, script, &logs)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	password := []byte(passwordText)
	reader := &actionIssueObservingReader{password: password, data: bytes.Repeat([]byte{0x5a}, 32)}
	service := newTestActionVerificationService(t, db, client, &actionIssueClock{now: now}, reader, func() int64 { return 9001 })
	issued, err := service.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, TargetGUID: testNoopTargetGUIDValue(), Actor: actor, Intent: testNoopIntent(testNoopTargetGUID, "sensitive-intent"), CurrentPassword: password, TrustedIP: "203.0.113.20"})
	if err != nil {
		t.Fatal(err)
	}
	if !reader.cleared || !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("random stage observed uncleared current password")
	}
	if issued.ExpiresAt != now+300_000 || len(issued.Ticket) != 46 {
		t.Fatalf("issued = %#v", issued)
	}
	if script.begins != 1 || script.commits != 1 || script.rollbacks != 0 || len(script.execs) != 2 {
		t.Fatalf("transaction begin/commit/rollback/exec = %d/%d/%d/%d", script.begins, script.commits, script.rollbacks, len(script.execs))
	}
	if len(script.queries) < 4 || !strings.Contains(script.queries[0].query, "FROM `users`") || !strings.Contains(script.queries[0].query, "FOR UPDATE") ||
		!strings.Contains(script.queries[1].query, "FROM `user_sessions`") || !strings.Contains(script.queries[1].query, "FOR UPDATE") {
		t.Fatalf("actor/session lock order missing: %#v", script.queries)
	}
	if strings.Contains(script.queries[1].query, "sid = ?") || !strings.Contains(script.queries[1].query, "user_id = ?") ||
		!strings.Contains(script.queries[1].query, "is_deleted = 0") || !strings.Contains(script.queries[1].query, "revoked_at IS NULL") ||
		!strings.Contains(script.queries[1].query, "expires_at > ?") || strings.Contains(strings.ToUpper(script.queries[1].query), " LIMIT ") {
		t.Fatalf("unsafe or unbounded session lookup: %s", script.queries[1].query)
	}
	if !strings.HasPrefix(script.execs[0].query, "UPDATE `admin_action_verifications`") || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(script.execs[0].query)), "DELETE ") ||
		!strings.Contains(script.execs[0].query, "consumed_at IS NULL") || !strings.Contains(script.execs[0].query, "is_deleted = 0") || !strings.Contains(script.execs[0].query, "expires_at > ?") ||
		strings.Contains(strings.ToUpper(script.execs[0].query), " LIMIT ") ||
		!strings.HasPrefix(script.execs[1].query, "INSERT INTO `admin_action_verifications`") {
		t.Fatalf("unexpected reissue writes: %#v", script.execs)
	}
	allSQL := logs.String()
	for _, call := range append(append([]actionIssueSQLCall(nil), script.queries...), script.execs...) {
		allSQL += call.query
		for _, arg := range call.args {
			allSQL += fmt.Sprint(arg.Value)
		}
	}
	for _, secret := range []string{actor.SessionSID, passwordText, "sensitive-intent", issued.Ticket} {
		if strings.Contains(allSQL, secret) {
			t.Fatalf("SQL args/logs leaked secret %q", secret)
		}
	}
}

func TestActionVerificationIssueScriptedFailuresRollbackAndClearSecrets(t *testing.T) {
	const now int64 = 1_800_000_000_000
	tests := []struct {
		name       string
		configure  func(*actionIssueSQLScript, *actionIssueObservingReader, *string, *func() int64)
		want       error
		wantExecs  int
		wantCommit int
	}{
		{name: "wrong password", configure: func(_ *actionIssueSQLScript, _ *actionIssueObservingReader, password *string, _ *func() int64) {
			*password = "wrong-password"
		}, want: ErrActionVerificationForbidden},
		{name: "random failure", configure: func(_ *actionIssueSQLScript, reader *actionIssueObservingReader, _ *string, _ *func() int64) {
			reader.err = errors.New("private random failure")
		}, want: ErrActionVerificationUnavailable},
		{name: "guid failure", configure: func(_ *actionIssueSQLScript, _ *actionIssueObservingReader, _ *string, guid *func() int64) {
			*guid = func() int64 { return 0 }
		}, want: ErrActionVerificationUnavailable},
		{name: "update failure", configure: func(script *actionIssueSQLScript, _ *actionIssueObservingReader, _ *string, _ *func() int64) {
			script.failExecAt = 1
		}, want: ErrActionVerificationUnavailable, wantExecs: 1},
		{name: "insert failure", configure: func(script *actionIssueSQLScript, _ *actionIssueObservingReader, _ *string, _ *func() int64) {
			script.failExecAt = 2
		}, want: ErrActionVerificationUnavailable, wantExecs: 2},
		{name: "commit failure", configure: func(script *actionIssueSQLScript, _ *actionIssueObservingReader, _ *string, _ *func() int64) {
			script.failCommit = true
		}, want: ErrActionVerificationUnavailable, wantExecs: 2, wantCommit: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script, actor, correctPassword := actionIssueScriptFixture(t, now)
			passwordText := correctPassword
			password := []byte(passwordText)
			reader := &actionIssueObservingReader{password: password, data: bytes.Repeat([]byte{0x6b}, 32)}
			guid := func() int64 { return 9002 }
			tc.configure(script, reader, &passwordText, &guid)
			password = []byte(passwordText)
			reader.password = password
			db := openActionIssueScriptDB(t, script, nil)
			client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
			service := newTestActionVerificationService(t, db, client, &actionIssueClock{now: now}, reader, guid)
			result, err := service.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, TargetGUID: testNoopTargetGUIDValue(), Actor: actor, Intent: testNoopIntent(testNoopTargetGUID, "failure-intent"), CurrentPassword: password, TrustedIP: "203.0.113.21"})
			if result != nil || !errors.Is(err, tc.want) || err.Error() != tc.want.Error() {
				t.Fatalf("result/error = %#v/%v, want nil/%v", result, err, tc.want)
			}
			if !bytes.Equal(password, make([]byte, len(password))) {
				t.Fatal("failure did not clear password")
			}
			if tc.name != "wrong password" && !reader.cleared {
				t.Fatal("post-password stage observed uncleared password")
			}
			if len(script.execs) != tc.wantExecs || script.commits != tc.wantCommit {
				t.Fatalf("execs/commits = %d/%d, want %d/%d", len(script.execs), script.commits, tc.wantExecs, tc.wantCommit)
			}
			if tc.name != "commit failure" && script.rollbacks != 1 {
				t.Fatalf("rollbacks = %d, want 1", script.rollbacks)
			}
		})
	}
}

func TestActionIdentityFreshClaimsCandidateBoundsAndLockWaitExpiry(t *testing.T) {
	const now int64 = 1_800_000_000_000
	tests := []struct {
		name   string
		mutate func(*actionIssueSQLScript, *ActionActor)
		clock  persistence.Clock
		want   error
	}{
		{name: "user guid", mutate: func(_ *actionIssueSQLScript, actor *ActionActor) { actor.UserGUID++ }, want: ErrActionVerificationForbidden},
		{name: "auth version", mutate: func(_ *actionIssueSQLScript, actor *ActionActor) { actor.AuthVersion++ }, want: ErrActionVerificationForbidden},
		{name: "session version", mutate: func(_ *actionIssueSQLScript, actor *ActionActor) { actor.SessionVersion++ }, want: ErrActionVerificationForbidden},
		{name: "disabled actor", mutate: func(script *actionIssueSQLScript, _ *ActionActor) { script.actor.Status = models.UserStatusDisabled }, want: ErrActionVerificationForbidden},
		{name: "wrong role", mutate: func(script *actionIssueSQLScript, _ *ActionActor) { script.actor.Role = models.UserRoleUser }, want: ErrActionVerificationForbidden},
		{name: "admin without delete permission", mutate: func(script *actionIssueSQLScript, _ *ActionActor) { script.actor.Role = models.UserRoleAdmin }, want: ErrActionVerificationForbidden},
		{name: "revoked session", mutate: func(script *actionIssueSQLScript, _ *ActionActor) {
			revoked := now
			script.sessions[0].RevokedAt = &revoked
		}, want: ErrActionVerificationForbidden},
		{name: "deleted session", mutate: func(script *actionIssueSQLScript, _ *ActionActor) { script.sessions[0].IsDeleted = 1 }, want: ErrActionVerificationForbidden},
		{name: "expired session", mutate: func(script *actionIssueSQLScript, _ *ActionActor) { script.sessions[0].ExpiresAt = now }, want: ErrActionVerificationForbidden},
		{name: "duplicate sid", mutate: func(script *actionIssueSQLScript, _ *ActionActor) {
			script.sessions = append(script.sessions, script.sessions[0])
		}, want: ErrActionVerificationForbidden},
		{name: "expired while waiting", mutate: func(script *actionIssueSQLScript, _ *ActionActor) { script.sessions[0].ExpiresAt = now + 1 }, clock: &actionIssueSequenceClock{values: []int64{now, now + 1}}, want: ErrActionVerificationForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script, actor, passwordText := actionIssueScriptFixture(t, now)
			tc.mutate(script, &actor)
			clock := tc.clock
			if clock == nil {
				clock = &actionIssueClock{now: now}
			}
			password := []byte(passwordText)
			reader := &actionIssueObservingReader{password: password, data: bytes.Repeat([]byte{1}, 32)}
			service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, clock, reader, func() int64 { return 9003 })
			result, err := service.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, TargetGUID: testNoopTargetGUIDValue(), Actor: actor, Intent: testNoopIntent(testNoopTargetGUID, "fresh-intent"), CurrentPassword: password, TrustedIP: "203.0.113.22"})
			if result != nil || !errors.Is(err, tc.want) || len(script.execs) != 0 || script.commits != 0 || script.rollbacks != 1 {
				t.Fatalf("result/error/exec/commit/rollback = %#v/%v/%d/%d/%d", result, err, len(script.execs), script.commits, script.rollbacks)
			}
			if !bytes.Equal(password, make([]byte, len(password))) {
				t.Fatal("freshness failure did not clear password")
			}
		})
	}
}

func TestActionIdentityTargetUserHiddenAndHierarchy(t *testing.T) {
	const now int64 = 1_800_000_000_000
	for _, tc := range []struct {
		name      string
		actorRole models.UserRole
		target    *models.User
		want      error
	}{
		{name: "missing", want: ErrActionVerificationHidden},
		{name: "deleted is filtered as missing", target: &models.User{ID: 30, AuditFields: models.AuditFields{Guid: 3001, IsDeleted: 1}, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 1}, want: ErrActionVerificationHidden},
		{name: "self", target: &models.User{ID: 10, AuditFields: models.AuditFields{Guid: 1001}, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 7}, want: ErrActionVerificationHidden},
		{name: "root target", target: &models.User{ID: 30, AuditFields: models.AuditFields{Guid: 3001}, Role: models.UserRoleRoot, Status: models.UserStatusActive, AuthVersion: 1}, want: ErrActionVerificationHidden},
		{name: "admin equal role", actorRole: models.UserRoleAdmin, target: &models.User{ID: 30, AuditFields: models.AuditFields{Guid: 3001}, Role: models.UserRoleAdmin, Status: models.UserStatusActive, AuthVersion: 1}, want: ErrActionVerificationHidden},
		{name: "admin higher role", actorRole: models.UserRoleAdmin, target: &models.User{ID: 30, AuditFields: models.AuditFields{Guid: 3001}, Role: models.UserRoleRoot, Status: models.UserStatusActive, AuthVersion: 1}, want: ErrActionVerificationHidden},
		{name: "admin without delete permission", actorRole: models.UserRoleAdmin, target: &models.User{ID: 30, AuditFields: models.AuditFields{Guid: 3001}, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 1}, want: ErrActionVerificationForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, actor, passwordText := actionIssueScriptFixture(t, now)
			script.target = tc.target
			if tc.actorRole != 0 {
				script.actor.Role = tc.actorRole
			}
			targetGUID := int64(3001)
			if tc.target != nil {
				targetGUID = tc.target.Guid
			}
			script.targetGUID = targetGUID
			password := []byte(passwordText)
			service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, &actionIssueClock{now: now}, &actionIssueObservingReader{password: password, data: bytes.Repeat([]byte{1}, 32)}, func() int64 { return 9004 })
			result, err := service.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, Actor: actor, TargetGUID: &targetGUID, Intent: testNoopIntent(testNoopTargetGUID, "target"), CurrentPassword: password, TrustedIP: "203.0.113.23"})
			if result != nil || !errors.Is(err, tc.want) || len(script.execs) != 0 {
				t.Fatalf("result/error/execs = %#v/%v/%d", result, err, len(script.execs))
			}
		})
	}
}

func TestActionIdentityAllConfiguredSessionCandidatesRemainEligible(t *testing.T) {
	const now int64 = 1_800_000_000_000
	script, actor, passwordText := actionIssueScriptFixture(t, now)
	match := script.sessions[0]
	script.sessions = nil
	for i := 0; i < 75; i++ {
		candidate := match
		candidate.ID = int64(100 + i)
		candidate.Guid = int64(10_000 + i)
		candidate.SID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		script.sessions = append(script.sessions, candidate)
	}
	match.ID = 999
	match.Guid = 9999
	script.sessions = append(script.sessions, match)
	password := []byte(passwordText)
	service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, &actionIssueClock{now: now}, &actionIssueObservingReader{password: password, data: bytes.Repeat([]byte{1}, 32)}, func() int64 { return 9100 })
	if _, err := service.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, TargetGUID: testNoopTargetGUIDValue(), Actor: actor, Intent: testNoopIntent(testNoopTargetGUID, "many-sessions"), CurrentPassword: password, TrustedIP: "203.0.113.24"}); err != nil {
		t.Fatal(err)
	}
	if script.commits != 1 || len(script.execs) != 2 || strings.Contains(strings.ToUpper(script.queries[1].query), " LIMIT ") {
		t.Fatalf("many-candidate transaction = commits:%d execs:%d query:%s", script.commits, len(script.execs), script.queries[1].query)
	}
}

func TestActionIdentityTargetUserAllowedAndFreshPolicyAllowDeny(t *testing.T) {
	const now int64 = 1_800_000_000_000
	script, actor, passwordText := actionIssueScriptFixture(t, now)
	script.actor.Role = models.UserRoleAdmin
	target := &models.User{ID: 30, AuditFields: models.AuditFields{Guid: 3001}, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 4}
	script.target, script.targetGUID = target, target.Guid
	script.policyHead = &models.PermissionPolicyHead{ID: 40, AuditFields: models.AuditFields{Guid: 4001}, UserID: script.actor.ID, PolicyVersion: 2, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
	script.overrides = []models.PermissionOverride{{ID: 41, AuditFields: models.AuditFields{Guid: 4101}, UserID: script.actor.ID, PolicyVersion: 2, Capability: 12, Effect: 2}}
	db := openActionIssueScriptDB(t, script, nil)
	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	service := newTestActionVerificationService(t, db, client, &actionIssueClock{now: now}, bytes.NewReader(bytes.Repeat([]byte{0x31}, 64)), func() int64 { return 9200 })
	password := []byte(passwordText)
	result, err := service.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, Actor: actor, TargetGUID: &target.Guid, Intent: testNoopIntent(testNoopTargetGUID, "allowed"), CurrentPassword: password, TrustedIP: "203.0.113.25"})
	if err != nil || result == nil {
		t.Fatalf("allowed policy result/error = %#v/%v", result, err)
	}
	if len(script.execs) != 2 {
		t.Fatalf("allowed policy writes = %d", len(script.execs))
	}
	insertArgs := script.execs[1].args
	storedTarget, ok := insertArgs[11].Value.(*int64)
	if !ok || storedTarget == nil || *storedTarget != target.Guid || len(fmt.Sprint(insertArgs[12].Value)) != 64 || len(fmt.Sprint(insertArgs[13].Value)) != 64 {
		t.Fatalf("target/hash persistence args = %#v", insertArgs)
	}

	// A fresh policy read on the next Issue must observe deny and produce no
	// additional mutation, even though the same service instance previously saw allow.
	script.overrides[0].Effect = 3
	password = []byte(passwordText)
	result, err = service.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, Actor: actor, TargetGUID: &target.Guid, Intent: testNoopIntent(testNoopTargetGUID, "denied"), CurrentPassword: password, TrustedIP: "203.0.113.25"})
	if result != nil || !errors.Is(err, ErrActionVerificationForbidden) || len(script.execs) != 2 || script.rollbacks != 1 {
		t.Fatalf("fresh deny result/error/writes/rollbacks = %#v/%v/%d/%d", result, err, len(script.execs), script.rollbacks)
	}
}

func TestActionIdentityQueryFailuresAreFixedRollbackAndStop(t *testing.T) {
	const now int64 = 1_800_000_000_000
	for _, tc := range []struct {
		stage       string
		target      bool
		scan        bool
		wantQueries int
	}{
		{stage: "actor", wantQueries: 1},
		{stage: "actor", scan: true, wantQueries: 1},
		{stage: "session", wantQueries: 2},
		{stage: "session", scan: true, wantQueries: 2},
		{stage: "target", target: true, wantQueries: 3},
		{stage: "target", target: true, scan: true, wantQueries: 3},
		{stage: "policy_head", wantQueries: 4},
		{stage: "policy_head", scan: true, wantQueries: 4},
		{stage: "policy_rules", wantQueries: 5},
		{stage: "policy_rules", scan: true, wantQueries: 5},
	} {
		name := tc.stage
		if tc.scan {
			name += " scan"
		}
		t.Run(name, func(t *testing.T) {
			script, actor, passwordText := actionIssueScriptFixture(t, now)
			if tc.scan {
				script.scanError = tc.stage
			} else {
				script.failQuery = tc.stage
			}
			targetGUID := testNoopTargetGUIDValue()
			password := []byte(passwordText)
			service := newTestActionVerificationService(t, openActionIssueScriptDB(t, script, nil), &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}, &actionIssueClock{now: now}, &actionIssueObservingReader{password: password, data: bytes.Repeat([]byte{1}, 32)}, func() int64 { return 9300 })
			if tc.target {
				target := models.User{ID: 30, AuditFields: models.AuditFields{Guid: 3001}, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 1}
				script.target, script.targetGUID = &target, target.Guid
				targetGUID = &target.Guid
			}
			result, err := service.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, Actor: actor, TargetGUID: targetGUID, Intent: testNoopIntent(testNoopTargetGUID, "query-failure"), CurrentPassword: password, TrustedIP: "203.0.113.26"})
			if result != nil || !errors.Is(err, ErrActionVerificationUnavailable) || err.Error() != ErrActionVerificationUnavailable.Error() ||
				len(script.queries) != tc.wantQueries || len(script.execs) != 0 || script.begins != 1 || script.rollbacks != 1 || script.commits != 0 {
				t.Fatalf("result/error/q/e/b/r/c = %#v/%v/%d/%d/%d/%d/%d", result, err, len(script.queries), len(script.execs), script.begins, script.rollbacks, script.commits)
			}
			if strings.Contains(err.Error(), actor.SessionSID) || strings.Contains(err.Error(), passwordText) {
				t.Fatal("fixed query error leaked a secret")
			}
		})
	}
}

func TestActionVerificationIssuePersistsOnlyDigestsAndReissueSoftDeletes(t *testing.T) {
	db := openTestMySQL(t)
	now := int64(1_800_000_000_000)
	passwordText := "Task7-Strong-Password!"
	passwordHash := actionIssuePasswordHash(t, passwordText)
	username := fixtureUsername(testSnowflake.Next())
	actor := models.User{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0},
		GroupID:     testDefaultBusinessGroupID(t, db),
		Username:    &username, PasswordHash: &passwordHash, Status: models.UserStatusActive,
		Role: models.UserRoleRoot, AuthVersion: 7, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{},
	}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	targetUsername := fixtureUsername(testSnowflake.Next())
	target := models.User{AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now}, GroupID: testDefaultBusinessGroupID(t, db), Username: &targetUsername, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 4, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{}}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	session := models.Session{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0},
		SID:         sid, UserID: actor.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 3,
		RefreshHMAC: strings.Repeat("a", 64), LastActiveAt: now, ExpiresAt: now + 3_600_000,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}

	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	service := newTestActionVerificationService(t, db, client, &actionIssueClock{now: now}, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	issue := func() (*IssuedVerification, []byte) {
		password := []byte(passwordText)
		result, err := service.Issue(context.Background(), VerificationIssue{
			Action: actionsecurity.ActionUsersDelete, TargetGUID: &target.Guid, Actor: ActionActor{UserID: actor.ID, UserGUID: actor.Guid, AuthVersion: 7, SessionSID: sid, SessionVersion: 3},
			Intent: testNoopIntent(target.Guid, "opaque-intent-value"), CurrentPassword: password, TrustedIP: "203.0.113.9",
		})
		if err != nil {
			t.Fatal(err)
		}
		return result, password
	}
	first, firstPassword := issue()
	if first.ExpiresAt != now+300_000 || len(first.Ticket) != 46 || !strings.HasPrefix(first.Ticket, "av_") {
		t.Fatalf("first result = %#v", first)
	}
	if !bytes.Equal(firstPassword, make([]byte, len(firstPassword))) {
		t.Fatal("successful Issue did not clear password")
	}
	second, secondPassword := issue()
	if second.Ticket == first.Ticket || !bytes.Equal(secondPassword, make([]byte, len(secondPassword))) {
		t.Fatal("reissue did not rotate ticket or clear password")
	}

	var rows []models.AdminActionVerification
	if err := db.Unscoped().Where("actor_user_id = ? AND action = ?", actor.ID, int(actionsecurity.ActionUsersDelete)).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].IsDeleted != 1 || rows[0].ConsumedAt != nil || rows[1].IsDeleted != 0 {
		t.Fatalf("verification rows = %#v", rows)
	}
	for _, row := range rows {
		if row.ActorUserID != actor.ID || row.ActorAuthVersion != 7 || row.SessionID != session.ID || row.TargetGUID == nil || *row.TargetGUID != target.Guid ||
			row.TargetKind != int(actionsecurity.TargetUser) || row.CreatedBy == nil || *row.CreatedBy != actor.ID || row.UpdatedBy == nil || *row.UpdatedBy != actor.ID ||
			len(row.IntentHMAC) != 64 || len(row.TicketHMAC) != 64 {
			t.Fatalf("unsafe or incomplete persisted row: %#v", row)
		}
		if _, err := hex.DecodeString(row.IntentHMAC); err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{passwordText, sid, "opaque-intent-value", first.Ticket, second.Ticket} {
			if strings.Contains(row.IntentHMAC+row.TicketHMAC, raw) {
				t.Fatalf("persisted digest leaked raw input %q", raw)
			}
		}
	}
}
