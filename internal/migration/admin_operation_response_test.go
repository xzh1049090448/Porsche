package migration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm/schema"
)

func TestAdminOperationResponseMigrationIsLatestAndChecksumProtected(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 8 || migrations[7].Version != "0008" {
		t.Fatalf("All() = %#v, want exactly eight migrations ending at 0008", migrations)
	}
	up := strings.ToLower(string(migrations[7].UpSQL))
	for _, fragment := range []string{
		"create table if not exists admin_operation_responses",
		"response_body varbinary(4096) not null",
		"body_sha256 char(64)",
		"unique key uk_admin_operation_responses_guid (guid)",
		"unique key uk_admin_operation_responses_operation (operation_id)",
		"constraint fk_admin_operation_responses_operation foreign key (operation_id) references admin_operations(id) on delete restrict on update restrict",
		"constraint chk_admin_operation_responses_immutable",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0008 missing %q", fragment)
		}
	}
	if strings.Count(up, "create table") != 1 || strings.Contains(up, "alter table") || strings.Contains(up, "timestamp") || strings.Contains(up, "datetime") {
		t.Fatalf("0008 must be one additive, rerunnable CREATE TABLE statement: %s", up)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(migrations[7].UpSQL)); got == strings.Repeat("0", 64) || len(got) != 64 {
		t.Fatalf("0008 checksum = %q", got)
	}
}

func TestAdminOperationResponseModelIsInternalAndImmutable(t *testing.T) {
	parsed, err := schema.Parse(&models.AdminOperationResponse{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Table != "admin_operation_responses" {
		t.Fatalf("table = %q", parsed.Table)
	}
	for _, name := range []string{"ID", "Guid", "OperationID", "HTTPStatus", "MediaType", "ResponseBody", "BodySHA256", "CreatedAt", "CreatedBy", "UpdatedAt", "UpdatedBy", "IsDeleted"} {
		field := parsed.LookUpField(name)
		if field == nil {
			t.Errorf("missing model field %s", name)
		}
	}
	if field := parsed.LookUpField("ResponseBody"); field == nil || field.DBName != "response_body" || string(field.DataType) != "varbinary(4096)" || !field.NotNull {
		t.Fatalf("response body metadata = %#v", field)
	}
}

func TestAdminOperationResponseCheckNormalizationAcceptsMySQLStringEscaping(t *testing.T) {
	want := normalizeAdminOperationResponseCheck("http_status = 201 AND media_type = 'application/json'")
	got := normalizeAdminOperationResponseCheck("((`http_status` = 201) and (`media_type` = _utf8mb4\\'application/json\\'))")
	if got != want {
		t.Fatalf("normalized MySQL clause = %q, want %q", got, want)
	}
}

func TestAdminOperationResponseMigrationOnIsolatedMySQLIsRerunnable(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if raw == "" {
		t.Skip("requires explicit disposable TEST_DATABASE_URL; no fixture provision performed")
	}
	if !isTestDatabaseURL(raw) {
		t.Fatal("TEST_DATABASE_URL must point to a database whose name ends in _test")
	}
	gdb, err := db.Open(raw, "test")
	if err != nil {
		t.Fatal(err)
	}
	generator := persistence.NewSnowflake(29, persistence.SystemClock())
	apply := func() {
		t.Helper()
		if err := Up(context.Background(), gdb, generator.Next, func() int64 { return time.Now().UTC().UnixMilli() }); err != nil {
			t.Fatal(err)
		}
		if err := VerifyAdminOperationResponseSchema(context.Background(), gdb); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	// Simulate a process crash after MySQL atomically created the table but
	// before the runner recorded the version. The next run must verify the
	// existing table and recreate exactly one ledger entry.
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version = '0008'").Error; err != nil {
		t.Fatal(err)
	}
	apply()
	apply()
	var ledgerCount int64
	if err := gdb.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version = '0008' AND is_deleted = 0").Row().Scan(&ledgerCount); err != nil || ledgerCount != 1 {
		t.Fatalf("0008 ledger count = %d (%v)", ledgerCount, err)
	}
}
