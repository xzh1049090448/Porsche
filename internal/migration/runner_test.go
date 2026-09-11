package migration

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

func TestUpRerunUsesActiveAdminOperationSchemaVersionAndRejectsDrift(t *testing.T) {
	runUp := func(gdb *gorm.DB) error {
		nextGUID := int64(9_130_000_000_000_000)
		return Up(context.Background(), gdb, func() int64 {
			nextGUID++
			return nextGUID
		}, func() int64 { return 1_900_000_000_000 })
	}

	t.Run("fully applied 0013 reruns", func(t *testing.T) {
		gdb := permissionSchemaDB(t)
		if err := runUp(gdb); err != nil {
			t.Fatalf("first Up: %v", err)
		}
		if err := runUp(gdb); err != nil {
			t.Fatalf("second Up on valid 0013 schema: %v", err)
		}
	})

	t.Run("0013 drift fails with current schema verifier", func(t *testing.T) {
		gdb := permissionSchemaDB(t)
		if err := runUp(gdb); err != nil {
			t.Fatalf("first Up: %v", err)
		}
		if err := gdb.Exec("ALTER TABLE admin_operations DROP CHECK chk_admin_operations_result_role_permission").Error; err != nil {
			t.Fatalf("introduce owned 0013 drift: %v", err)
		}
		if err := runUp(gdb); !errors.Is(err, ErrAdminOperationRolePermissionResultsSchema) {
			t.Fatalf("rerun error = %v, want %v", err, ErrAdminOperationRolePermissionResultsSchema)
		}
	})

	t.Run("0012 drift still fails closed", func(t *testing.T) {
		gdb := permissionSchemaDB(t)
		if err := runUp(gdb); err != nil {
			t.Fatalf("first Up: %v", err)
		}
		migrations, err := All()
		if err != nil {
			t.Fatal(err)
		}
		if err := executeAdminOperationSafetyFixtureSQL(gdb, migrations[12].DownSQL); err != nil {
			t.Fatalf("return owned fixture to valid 0012 schema: %v", err)
		}
		if err := gdb.Exec("UPDATE schema_migrations SET is_deleted=1 WHERE version='0013' AND is_deleted=0").Error; err != nil {
			t.Fatalf("deactivate owned 0013 ledger: %v", err)
		}
		if err := gdb.Exec("ALTER TABLE admin_operations DROP CHECK chk_admin_operations_result_auth_version").Error; err != nil {
			t.Fatalf("introduce owned 0012 drift: %v", err)
		}
		if err := runUp(gdb); !errors.Is(err, ErrAdminOperationSafetySchema) {
			t.Fatalf("rerun error = %v, want %v", err, ErrAdminOperationSafetySchema)
		}
	})
}

func TestAdminOperationVerifierVersionUsesHighestActiveSchema(t *testing.T) {
	tests := []struct {
		name             string
		migrationVersion string
		applied          map[string]AppliedMigration
		want             string
	}{
		{
			name:             "0012 remains exact before 0013 is active",
			migrationVersion: "0012",
			applied:          map[string]AppliedMigration{"0012": {Version: "0012"}},
			want:             "0012",
		},
		{
			name:             "0012 uses exact 0013 shape after extension is active",
			migrationVersion: "0012",
			applied: map[string]AppliedMigration{
				"0012": {Version: "0012"},
				"0013": {Version: "0013"},
			},
			want: "0013",
		},
		{
			name:             "0013 verifies its own exact shape",
			migrationVersion: "0013",
			applied:          map[string]AppliedMigration{"0013": {Version: "0013"}},
			want:             "0013",
		},
		{
			name:             "unrelated migration has no admin operation verifier",
			migrationVersion: "0011",
			applied:          map[string]AppliedMigration{"0013": {Version: "0013"}},
			want:             "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := adminOperationVerifierVersion(tt.migrationVersion, tt.applied); got != tt.want {
				t.Fatalf("adminOperationVerifierVersion(%q) = %q, want %q", tt.migrationVersion, got, tt.want)
			}
		})
	}
}

func TestEmbeddedMigrationsContainOneWayInitialSchema(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(migrations) < 1 || migrations[0].Version != "0001" {
		t.Fatalf("unexpected migrations: %#v", migrations)
	}
	up := strings.ToLower(string(migrations[0].UpSQL))
	if !strings.Contains(up, "create table if not exists users") || strings.Contains(up, "timestamp") {
		t.Fatalf("initial schema must create users without timestamp columns: %s", up)
	}
	down := strings.ToLower(string(migrations[0].DownSQL))
	if strings.Contains(down, "drop table") || strings.Contains(down, "drop database") {
		t.Fatalf("down migration must not destroy data: %s", down)
	}
}

// TestAuthCoreMigrationOnIsolatedMySQL verifies the embedded migration against
// MySQL only when the caller explicitly supplies a dedicated *_test database.
// It never reads DATABASE_URL or any .env file.
func TestAuthCoreMigrationOnIsolatedMySQL(t *testing.T) {
	testDatabaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if testDatabaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; isolated MySQL migration test skipped")
	}
	if !isTestDatabaseURL(testDatabaseURL) {
		t.Fatal("TEST_DATABASE_URL must point to a database whose name ends in _test")
	}

	gdb, err := db.Open(testDatabaseURL, "test")
	if err != nil {
		t.Fatalf("open isolated test database: %v", err)
	}
	generator := persistence.NewSnowflake(11, persistence.SystemClock())
	if err := Up(context.Background(), gdb, generator.Next, func() int64 { return time.Now().UTC().UnixMilli() }); err != nil {
		t.Fatalf("apply isolated auth migration: %v", err)
	}

	for _, table := range []string{"user_sessions", "auth_audit_events"} {
		for _, column := range []string{"id", "guid", "created_at", "created_by", "updated_at", "updated_by", "is_deleted"} {
			var dataType string
			if err := gdb.Raw(`SELECT data_type FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).Scan(&dataType).Error; err != nil {
				t.Fatalf("read %s.%s metadata: %v", table, column, err)
			}
			if column != "is_deleted" && dataType != "bigint" {
				t.Errorf("%s.%s type = %q, want bigint", table, column, dataType)
			}
			if column == "is_deleted" && dataType != "int" {
				t.Errorf("%s.%s type = %q, want int", table, column, dataType)
			}
		}
	}
	assertColumn(t, gdb, "users", "phone", "varchar", true)
	assertColumn(t, gdb, "users", "username", "varchar", true)
	assertColumn(t, gdb, "user_sessions", "user_id", "bigint", false)
	assertColumn(t, gdb, "user_sessions", "login_method", "int", false)
	assertColumn(t, gdb, "auth_audit_events", "event_type", "int", false)

	assertUniqueIndex(t, gdb, "users", "uk_users_username")
	assertUniqueIndex(t, gdb, "users", "uk_users_phone")
	assertUniqueIndex(t, gdb, "user_sessions", "uk_user_sessions_sid")
	assertIndex(t, gdb, "user_sessions", "idx_user_sessions_user_active_expires")
	assertIndex(t, gdb, "auth_audit_events", "idx_auth_audit_events_user_active_created")
}

func isTestDatabaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	name := strings.Trim(strings.TrimSpace(parsed.Path), "/")
	return strings.HasSuffix(name, "_test")
}

func assertUniqueIndex(t *testing.T, gdb *gorm.DB, table, index string) {
	t.Helper()
	assertIndex(t, gdb, table, index)
	var nonUnique int
	if err := gdb.Raw(`SELECT non_unique FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? LIMIT 1`, table, index).Scan(&nonUnique).Error; err != nil {
		t.Fatalf("read index %s.%s metadata: %v", table, index, err)
	}
	if nonUnique != 0 {
		t.Errorf("%s.%s is not unique", table, index)
	}
}

func assertIndex(t *testing.T, gdb *gorm.DB, table, index string) {
	t.Helper()
	var count int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`, table, index).Scan(&count).Error; err != nil {
		t.Fatalf("read index %s.%s metadata: %v", table, index, err)
	}
	if count == 0 {
		t.Errorf("%s.%s is missing", table, index)
	}
}

func assertColumn(t *testing.T, gdb *gorm.DB, table, column, wantType string, nullable bool) {
	t.Helper()
	var result struct {
		DataType   string `gorm:"column:data_type"`
		IsNullable string `gorm:"column:is_nullable"`
	}
	// MySQL reports DATA_TYPE/IS_NULLABLE labels even for lower-case SQL.
	// Positional scanning avoids label mapping and fails on a missing column.
	if err := gdb.Raw(`SELECT data_type, is_nullable FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).Row().Scan(&result.DataType, &result.IsNullable); err != nil {
		t.Fatalf("read column %s.%s metadata: %v", table, column, err)
	}
	if result.DataType != wantType {
		t.Errorf("%s.%s type = %q, want %q", table, column, result.DataType, wantType)
	}
	wantNullable := "NO"
	if nullable {
		wantNullable = "YES"
	}
	if result.IsNullable != wantNullable {
		t.Errorf("%s.%s nullable = %q, want %q", table, column, result.IsNullable, wantNullable)
	}
}

func TestVerifyAppliedRejectsMissingAndTamperedMigrations(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyApplied(migrations, nil); err == nil {
		t.Fatal("expected missing migration verification error")
	}
	if err := VerifyApplied(migrations, []AppliedMigration{{Version: "0001", Checksum: "tampered"}}); err == nil {
		t.Fatal("expected checksum verification error")
	}
}

func TestAdminUsersReadCountIndexMigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 19 || migrations[3].Version != "0004" || migrations[5].Version != "0006" || migrations[6].Version != "0007" || migrations[7].Version != "0008" || migrations[8].Version != "0009" || migrations[9].Version != "0010" {
		t.Fatalf("admin users count migration 0004 is missing: %#v", migrations)
	}
	up := strings.ToLower(string(migrations[3].UpSQL))
	for _, fragment := range []string{
		"create index idx_users_admin_read_count",
		"on users (is_deleted, role, status)",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0004 missing %q: %s", fragment, up)
		}
	}
	if strings.Contains(up, "drop ") || strings.Contains(up, "delete ") || strings.Contains(up, "update ") {
		t.Fatalf("0004 must be additive: %s", up)
	}
}

// TestAuthCoreMigrationContract protects the explicit, additive auth schema
// migration. It intentionally asserts the SQL contract without connecting to
// any configured database.
func TestAuthCoreMigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 19 || migrations[1].Version != "0002" || migrations[2].Version != "0003" || migrations[3].Version != "0004" || migrations[5].Version != "0006" || migrations[6].Version != "0007" || migrations[7].Version != "0008" || migrations[8].Version != "0009" || migrations[9].Version != "0010" {
		t.Fatalf("auth migration 0002 is missing: %#v", migrations)
	}

	up := strings.ToLower(string(migrations[1].UpSQL))
	for _, fragment := range []string{
		"alter table users",
		"modify column phone varchar(20) null",
		"add column username varchar(20) null",
		"unique key uk_users_username (username)",
		"create table if not exists user_sessions",
		"create table if not exists auth_audit_events",
		"guid bigint not null",
		"created_at bigint not null",
		"created_by bigint null",
		"updated_at bigint not null",
		"updated_by bigint null",
		"is_deleted int not null default 0",
		"login_method int not null",
		"event_type int not null",
		"foreign key (user_id) references users(id)",
		"key idx_user_sessions_user_active_expires (user_id, is_deleted, expires_at)",
		"key idx_auth_audit_events_user_active_created (user_id, is_deleted, created_at)",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("auth migration missing %q:\n%s", fragment, up)
		}
	}
	if strings.Contains(up, "drop table") || strings.Contains(up, "timestamp") || strings.Contains(up, "datetime") {
		t.Fatalf("auth migration must be additive and use bigint timestamps: %s", up)
	}
	for _, forbidden := range []string{"refresh_token", "access_token", "authorization", "cookie", "password"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("auth migration must not persist raw credentials (%q): %s", forbidden, up)
		}
	}

	down := strings.ToLower(string(migrations[1].DownSQL))
	if strings.Contains(down, "drop table") || strings.Contains(down, "alter table") {
		t.Fatalf("auth down migration must not destructively change user data: %s", down)
	}
}
