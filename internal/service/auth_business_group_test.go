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
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const authBusinessGroupDriverName = "porsche_auth_business_group_script"

var (
	authBusinessGroupDriverOnce sync.Once
	authBusinessGroupScripts    sync.Map
)

type authBusinessGroupScript struct {
	mu      sync.Mutex
	rows    [][]driver.Value
	queries []authBusinessGroupQuery
}

type authBusinessGroupQuery struct {
	query string
	args  []driver.NamedValue
}

type authBusinessGroupDriver struct{}

func (authBusinessGroupDriver) Open(name string) (driver.Conn, error) {
	value, ok := authBusinessGroupScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown auth business group script")
	}
	return &authBusinessGroupConn{script: value.(*authBusinessGroupScript)}, nil
}

type authBusinessGroupConn struct{ script *authBusinessGroupScript }

func (c *authBusinessGroupConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (c *authBusinessGroupConn) Close() error { return nil }
func (c *authBusinessGroupConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c *authBusinessGroupConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.script.mu.Lock()
	c.script.queries = append(c.script.queries, authBusinessGroupQuery{query: query, args: append([]driver.NamedValue(nil), args...)})
	rows := make([][]driver.Value, len(c.script.rows))
	for i := range c.script.rows {
		rows[i] = append([]driver.Value(nil), c.script.rows[i]...)
	}
	c.script.mu.Unlock()
	return &authBusinessGroupRows{rows: rows}, nil
}

type authBusinessGroupRows struct {
	rows  [][]driver.Value
	index int
}

func (r *authBusinessGroupRows) Columns() []string {
	return []string{"id", "guid", "group_key", "status", "is_deleted"}
}
func (r *authBusinessGroupRows) Close() error { return nil }
func (r *authBusinessGroupRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

func TestDefaultBusinessGroupResolverLocksAndRejectsCorruptRows(t *testing.T) {
	valid := []driver.Value{int64(41), int64(4201), "default", int64(models.BusinessGroupStatusActive), int64(0)}
	tests := []struct {
		name string
		rows [][]driver.Value
		want int64
	}{
		{name: "canonical", rows: [][]driver.Value{valid}, want: 41},
		{name: "missing"},
		{name: "duplicate", rows: [][]driver.Value{valid, {int64(42), int64(4202), "default", int64(models.BusinessGroupStatusActive), int64(0)}}},
		{name: "case variant", rows: [][]driver.Value{{int64(41), int64(4201), "Default", int64(models.BusinessGroupStatusActive), int64(0)}}},
		{name: "inactive", rows: [][]driver.Value{{int64(41), int64(4201), "default", int64(models.BusinessGroupStatusInactive), int64(0)}}},
		{name: "deleted", rows: [][]driver.Value{{int64(41), int64(4201), "default", int64(models.BusinessGroupStatusActive), int64(1)}}},
		{name: "zero guid", rows: [][]driver.Value{{int64(41), int64(0), "default", int64(models.BusinessGroupStatusActive), int64(0)}}},
		{name: "negative guid", rows: [][]driver.Value{{int64(41), int64(-1), "default", int64(models.BusinessGroupStatusActive), int64(0)}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, script := newAuthBusinessGroupScriptDB(t, tc.rows)
			groupID, err := lockCanonicalDefaultBusinessGroup(db)
			if tc.want == 0 {
				if !errors.Is(err, errDefaultBusinessGroupUnavailable) || groupID != 0 {
					t.Fatalf("corrupt resolver result = %d, %v", groupID, err)
				}
			} else if err != nil || groupID != tc.want {
				t.Fatalf("canonical resolver result = %d, %v", groupID, err)
			}

			script.mu.Lock()
			queries := append([]authBusinessGroupQuery(nil), script.queries...)
			script.mu.Unlock()
			if len(queries) != 1 {
				t.Fatalf("queries = %#v", queries)
			}
			query := queries[0]
			for _, fragment := range []string{"SELECT `id`,`guid`,`group_key`,`status`,`is_deleted`", "FROM `business_groups`", "group_key = ?", "is_deleted = 0", "ORDER BY id ASC", "FOR UPDATE"} {
				if !strings.Contains(query.query, fragment) {
					t.Errorf("resolver query missing %q: %s", fragment, query.query)
				}
			}
			if len(query.args) != 1 || fmt.Sprint(query.args[0].Value) != "default" {
				t.Fatalf("resolver args = %#v", query.args)
			}
		})
	}
}

func newAuthBusinessGroupScriptDB(t *testing.T, rows [][]driver.Value) (*gorm.DB, *authBusinessGroupScript) {
	t.Helper()
	authBusinessGroupDriverOnce.Do(func() { sql.Register(authBusinessGroupDriverName, authBusinessGroupDriver{}) })
	name := strings.ReplaceAll(t.Name(), "/", "_")
	script := &authBusinessGroupScript{rows: rows}
	authBusinessGroupScripts.Store(name, script)
	t.Cleanup(func() { authBusinessGroupScripts.Delete(name) })
	sqlDB, err := sql.Open(authBusinessGroupDriverName, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db, script
}
