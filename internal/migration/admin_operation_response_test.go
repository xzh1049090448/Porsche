package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	if len(migrations) != 9 || migrations[8].Version != "0009" {
		t.Fatalf("All() = %#v, want exactly nine migrations ending at 0009", migrations)
	}
	up := strings.ToLower(string(migrations[8].UpSQL))
	for _, fragment := range []string{
		"lifecycle_state int not null default 1",
		"integrity_version int not null default 0",
		"response_hmac char(64)",
		"failure_code int null",
		"chk_admin_operation_responses_lifecycle",
		"chk_admin_action_outbox_outcome",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0009 missing %q", fragment)
		}
	}
	if !strings.Contains(up, "alter table admin_operation_responses") || !strings.Contains(up, "alter table admin_action_outbox") || strings.Contains(up, "create trigger") || strings.Contains(up, "timestamp") || strings.Contains(up, "datetime") {
		t.Fatalf("0009 must migrate response integrity and outbox outcome fields: %s", up)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(migrations[8].UpSQL)); got == strings.Repeat("0", 64) || len(got) != 64 {
		t.Fatalf("0009 checksum = %q", got)
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
	for _, name := range []string{"ID", "Guid", "OperationID", "LifecycleState", "IntegrityVersion", "ResponseHMAC", "HTTPStatus", "MediaType", "ResponseBody", "BodySHA256", "CreatedAt", "CreatedBy", "UpdatedAt", "UpdatedBy", "IsDeleted"} {
		field := parsed.LookUpField(name)
		if field == nil {
			t.Errorf("missing model field %s", name)
		}
	}
	if field := parsed.LookUpField("ResponseBody"); field == nil || field.DBName != "response_body" || string(field.DataType) != "varbinary(4096)" || !field.NotNull {
		t.Fatalf("response body metadata = %#v", field)
	}
}

func TestAdminOperationResponseCheckCanonicalizationAcceptsMySQLStringEscaping(t *testing.T) {
	want, wantOK := canonicalizeAdminOperationResponseCheck("http_status = 201 AND media_type = 'application/json'")
	got, gotOK := canonicalizeAdminOperationResponseCheck("((`HTTP_STATUS` = 0201) AnD (`MEDIA_TYPE` = _ASCII\\'application/json\\'))")
	if !wantOK || !gotOK || got != want {
		t.Fatalf("canonical MySQL clause = %q/%v, want %q/%v", got, gotOK, want, wantOK)
	}
}

func TestAdminOperationResponseCheckCanonicalizationPreservesActorBooleanGrouping(t *testing.T) {
	want, wantOK := canonicalizeAdminOperationResponseCheck("created_at >= 0 AND updated_at = created_at AND is_deleted = 0 AND ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))")
	drifted, driftedOK := canonicalizeAdminOperationResponseCheck("(created_at >= 0 AND updated_at = created_at AND is_deleted = 0 AND created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by)")
	if !wantOK || !driftedOK || drifted == want {
		t.Fatalf("actor OR moved outside immutable conjunction was accepted: %q", drifted)
	}
	notGroup, notGroupOK := canonicalizeAdminOperationResponseCheck("NOT (created_by IS NULL OR updated_by IS NULL)")
	notDrift, notDriftOK := canonicalizeAdminOperationResponseCheck("(NOT created_by IS NULL) OR updated_by IS NULL")
	if !notGroupOK || !notDriftOK || notGroup == notDrift {
		t.Fatalf("NOT grouping drift was accepted: %q", notDrift)
	}
}

func TestAdminOperationResponseCheckCanonicalizationAcceptsFunctionFormatting(t *testing.T) {
	want, wantOK := canonicalizeAdminOperationResponseCheck("OCTET_LENGTH(response_body) BETWEEN 2 AND 4096 AND OCTET_LENGTH(body_sha256) = 64")
	got, gotOK := canonicalizeAdminOperationResponseCheck("(( LENGTH ( `RESPONSE_BODY` ) BETWEEN (0002) AND 04096) aNd (length(`BODY_SHA256`) = 0064))")
	if !wantOK || !gotOK || got != want {
		t.Fatalf("canonical function clause = %q/%v, want %q/%v", got, gotOK, want, wantOK)
	}
}

func TestAdminOperationResponseContractRejectsLifecycleBooleanDrift(t *testing.T) {
	want := adminOperationResponseTableContract()
	got := matchingAdminOperationResponseMetadata(want, "fixture_test")
	if !matchesAdminOperationResponseContract(want, got, "fixture_test") {
		t.Fatal("equivalent MySQL formatting was rejected")
	}
	for index := range got.checks {
		if got.checks[index].name == "chk_admin_operation_responses_lifecycle" {
			got.checks[index].clause = "lifecycle_state = 1 OR is_deleted = 0"
		}
	}
	if matchesAdminOperationResponseContract(want, got, "fixture_test") {
		t.Fatal("verifier accepted a weakened response lifecycle check")
	}
}

func matchingAdminOperationResponseMetadata(contract businessGroupTableContract, schemaName string) businessGroupTableMetadata {
	metadata := businessGroupTableMetadata{engine: "InnoDB", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"}
	for _, column := range contract.columns {
		metadata.columns = append(metadata.columns, businessGroupColumnMetadata{
			name: column.name, columnType: column.columnType, nullable: column.nullable, defaultVal: column.defaultVal,
			extra: column.extra, characterSet: column.characterSet, collation: column.collation,
		})
	}
	for _, index := range contract.indexes {
		for position, column := range index.columns {
			nonUnique := 1
			if index.unique {
				nonUnique = 0
			}
			metadata.indexes = append(metadata.indexes, businessGroupIndexMetadata{
				name: index.name, column: column, sequence: position + 1, nonUnique: nonUnique,
				collation: sql.NullString{String: "A", Valid: true}, indexType: "BTREE", visible: "YES",
			})
		}
	}
	metadata.foreignKeys = []businessGroupForeignKeyMetadata{{
		name: "fk_admin_operation_responses_operation", column: "operation_id", ordinal: 1, targetSchema: schemaName,
		targetTable: "admin_operations", targetColumn: "id", deleteRule: "RESTRICT", updateRule: "RESTRICT",
	}}
	for _, check := range contract.checks {
		metadata.checks = append(metadata.checks, businessGroupCheckMetadata{name: check.name, clause: check.clause, enforced: "YES"})
	}
	return metadata
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
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version = '0009'").Error; err != nil {
		t.Fatal(err)
	}
	apply()
	apply()
	var ledgerCount int64
	if err := gdb.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version = '0009' AND is_deleted = 0").Row().Scan(&ledgerCount); err != nil || ledgerCount != 1 {
		t.Fatalf("0009 ledger count = %d (%v)", ledgerCount, err)
	}
}
