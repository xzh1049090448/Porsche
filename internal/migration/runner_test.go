package migration

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

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

// TestAuthCoreMigrationContract protects the explicit, additive auth schema
// migration. It intentionally asserts the SQL contract without connecting to
// any configured database.
func TestAuthCoreMigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 2 || migrations[1].Version != "0002" {
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
