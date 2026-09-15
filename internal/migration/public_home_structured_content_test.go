package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

func TestAllIncludesPublicHomeStructuredContent0020(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 20 {
		t.Fatalf("All() length = %d, want 20", len(migrations))
	}
	if migrations[19].Version != "0020" {
		t.Fatalf("final migration = %q, want 0020", migrations[19].Version)
	}
}

func TestPublicHomeStructuredContentMigrationSQLContract(t *testing.T) {
	migration := publicHomeStructuredContentMigration(t)
	up := strings.ToLower(string(migration.UpSQL))
	if strings.Count(up, "create table if not exists") != 2 {
		t.Fatalf("0020 CREATE TABLE count = %d, want 2", strings.Count(up, "create table if not exists"))
	}
	for _, fragment := range []string{
		"create table if not exists public_home_announcements",
		"create table if not exists public_home_faqs",
		"id bigint not null auto_increment primary key",
		"guid bigint not null",
		"content_draft_id bigint not null",
		"title varchar(120) not null",
		"body_markdown mediumtext not null",
		"effective_at bigint null",
		"question varchar(200) not null",
		"answer_markdown mediumtext not null",
		"is_visible int not null default 1",
		"sort_order int not null default 0",
		"revision bigint not null default 1",
		"created_at bigint not null",
		"created_by bigint null",
		"updated_at bigint not null",
		"updated_by bigint null",
		"is_deleted int not null default 0",
		"unique key uk_public_home_announcements_guid (guid)",
		"unique key uk_public_home_faqs_guid (guid)",
		"key idx_public_home_announcements_draft_active_order (content_draft_id, is_deleted, sort_order, guid)",
		"key idx_public_home_faqs_draft_active_order (content_draft_id, is_deleted, sort_order, guid)",
		"constraint fk_public_home_announcements_draft foreign key (content_draft_id) references public_content_drafts(id) on delete restrict on update restrict",
		"constraint fk_public_home_faqs_draft foreign key (content_draft_id) references public_content_drafts(id) on delete restrict on update restrict",
		"constraint chk_public_home_announcements_values check (revision > 0 and is_visible in (0, 1) and sort_order >= 0 and is_deleted in (0, 1))",
		"constraint chk_public_home_faqs_values check (revision > 0 and is_visible in (0, 1) and sort_order >= 0 and is_deleted in (0, 1))",
		"engine=innodb default charset=utf8mb4 collate=utf8mb4_unicode_ci",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0020 missing contract fragment %q", fragment)
		}
	}
	for _, table := range []string{"public_home_announcements", "public_home_faqs"} {
		for _, column := range []string{
			"id bigint not null auto_increment primary key",
			"guid bigint not null",
			"content_draft_id bigint not null",
			"is_visible int not null default 1",
			"sort_order int not null default 0",
			"revision bigint not null default 1",
			"created_at bigint not null",
			"created_by bigint null",
			"updated_at bigint not null",
			"updated_by bigint null",
			"is_deleted int not null default 0",
		} {
			if !tableHasColumn(up, table, column) {
				t.Errorf("0020 table %s missing column contract %q", table, column)
			}
		}
	}
	for _, forbidden := range []string{"\ninsert into ", "\nupdate ", "\ndelete from ", "timestamp", "datetime", " enum(", "automigrate"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("0020 contains forbidden fragment %q", forbidden)
		}
	}

	down := strings.ToLower(strings.TrimSpace(string(migration.DownSQL)))
	if strings.Count(down, "drop table") != 2 {
		t.Fatalf("0020 down DROP TABLE count = %d, want 2", strings.Count(down, "drop table"))
	}
	faq := strings.Index(down, "drop table if exists public_home_faqs")
	announcement := strings.Index(down, "drop table if exists public_home_announcements")
	if faq < 0 || announcement < 0 || faq > announcement {
		t.Fatalf("0020 down order is unsafe: %s", down)
	}
}

func TestPublicHomeStructuredContentVerifierFailsClosedWithoutDatabase(t *testing.T) {
	if err := VerifyPublicHomeStructuredContentSchema(context.Background(), nil); !errors.Is(err, ErrPublicHomeStructuredContentSchema) {
		t.Fatalf("nil verifier error = %v, want %v", err, ErrPublicHomeStructuredContentSchema)
	}
}

func TestPublicHomeStructuredContentContractRejectsPartialAndMalformedMetadata(t *testing.T) {
	for _, contract := range publicHomeStructuredContentContracts() {
		if !matchesPublicHomeStructuredContentContract(contract, validPublicHomeStructuredContentMetadata(contract, "fixture"), "fixture") {
			t.Fatalf("valid metadata rejected for %s", contract.name)
		}

		partial := validPublicHomeStructuredContentMetadata(contract, "fixture")
		partial.columns = partial.columns[:len(partial.columns)-1]
		if matchesPublicHomeStructuredContentContract(contract, partial, "fixture") {
			t.Errorf("partial columns accepted for %s", contract.name)
		}

		wrongType := validPublicHomeStructuredContentMetadata(contract, "fixture")
		wrongType.columns[1].columnType = "bigint unsigned"
		if matchesPublicHomeStructuredContentContract(contract, wrongType, "fixture") {
			t.Errorf("unsigned guid accepted for %s", contract.name)
		}

		wrongIndex := validPublicHomeStructuredContentMetadata(contract, "fixture")
		wrongIndex.indexes = wrongIndex.indexes[:len(wrongIndex.indexes)-1]
		if matchesPublicHomeStructuredContentContract(contract, wrongIndex, "fixture") {
			t.Errorf("missing query index accepted for %s", contract.name)
		}

		wrongForeignKey := validPublicHomeStructuredContentMetadata(contract, "fixture")
		wrongForeignKey.foreignKeys[0].targetTable = "wrong_parent"
		if matchesPublicHomeStructuredContentContract(contract, wrongForeignKey, "fixture") {
			t.Errorf("wrong foreign key accepted for %s", contract.name)
		}

		wrongCheck := validPublicHomeStructuredContentMetadata(contract, "fixture")
		wrongCheck.checks[0].clause = "revision >= 0 AND is_visible IN (0, 1) AND sort_order >= 0 AND is_deleted IN (0, 1)"
		if matchesPublicHomeStructuredContentContract(contract, wrongCheck, "fixture") {
			t.Errorf("wrong check constraint accepted for %s", contract.name)
		}
	}
}

func TestPublicHomeStructuredContentMigrationRealMySQLLifecycleAndDrift(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit isolated TEST_DATABASE_URL MySQL fixture; .env is never read")
	}
	db := permissionSchemaDB(t)
	generator := persistence.NewSnowflake(52, persistence.SystemClock())
	now := func() int64 { return 1_900_000_000_000 }
	migration := publicHomeStructuredContentMigration(t)

	if err := Up(context.Background(), db, generator.Next, now); err != nil {
		t.Fatal(err)
	}
	assertPublicHomeStructuredContentTables(t, db, true)
	if err := VerifyPublicHomeStructuredContentSchema(context.Background(), db); err != nil {
		t.Fatalf("verify after up: %v", err)
	}
	if err := Verify(context.Background(), db); err != nil {
		t.Fatalf("global verify after up: %v", err)
	}
	assertPublicHomeStructuredContentLedger(t, db, migration, true)
	if err := Up(context.Background(), db, generator.Next, now); err != nil {
		t.Fatalf("repeated up: %v", err)
	}

	if err := executePublicHomeStructuredContentSQL(db, migration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := setFixtureMigrationActive(db, migration.Version, false); err != nil {
		t.Fatal(err)
	}
	assertPublicHomeStructuredContentTables(t, db, false)
	assertPublicHomeStructuredContentLedger(t, db, migration, false)
	if err := VerifyPublicHomeStructuredContentSchema(context.Background(), db); !errors.Is(err, ErrPublicHomeStructuredContentSchema) {
		t.Fatalf("verify after down = %v, want %v", err, ErrPublicHomeStructuredContentSchema)
	}

	if err := Up(context.Background(), db, generator.Next, now); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	assertPublicHomeStructuredContentTables(t, db, true)
	assertPublicHomeStructuredContentLedger(t, db, migration, true)
	if err := Verify(context.Background(), db); err != nil {
		t.Fatalf("global verify after re-up: %v", err)
	}

	if err := db.Exec("DROP TABLE public_home_faqs").Error; err != nil {
		t.Fatal(err)
	}
	if err := Up(context.Background(), db, generator.Next, now); !errors.Is(err, ErrPublicHomeStructuredContentSchema) {
		t.Fatalf("active-ledger partial schema up = %v, want %v", err, ErrPublicHomeStructuredContentSchema)
	}
	if err := executePublicHomeStructuredContentSQL(db, migration.UpSQL); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE public_home_announcements DROP CHECK chk_public_home_announcements_values").Error; err != nil {
		t.Fatal(err)
	}
	if err := VerifyPublicHomeStructuredContentSchema(context.Background(), db); !errors.Is(err, ErrPublicHomeStructuredContentSchema) {
		t.Fatalf("malformed schema verify = %v, want %v", err, ErrPublicHomeStructuredContentSchema)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPublicHomeStructuredContentSchema(context.Background(), db); !errors.Is(err, ErrPublicHomeStructuredContentSchema) {
		t.Fatalf("unavailable database verify = %v, want %v", err, ErrPublicHomeStructuredContentSchema)
	}
}

func publicHomeStructuredContentMigration(t *testing.T) Migration {
	t.Helper()
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 20 || migrations[19].Version != "0020" {
		t.Fatalf("All() = %d migrations, want 0020 at its published position", len(migrations))
	}
	return migrations[19]
}

func assertPublicHomeStructuredContentTables(t *testing.T, db *gorm.DB, present bool) {
	t.Helper()
	want := int64(0)
	if present {
		want = 1
	}
	for _, table := range []string{"public_home_announcements", "public_home_faqs"} {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", table).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("table %s count = %d, want %d", table, count, want)
		}
	}
}

func assertPublicHomeStructuredContentLedger(t *testing.T, db *gorm.DB, migration Migration, active bool) {
	t.Helper()
	var checksum string
	var isDeleted int
	if err := db.Raw("SELECT checksum,is_deleted FROM schema_migrations WHERE version=?", migration.Version).Row().Scan(&checksum, &isDeleted); err != nil {
		t.Fatal(err)
	}
	wantChecksum := fmt.Sprintf("%x", sha256.Sum256(migration.UpSQL))
	if checksum != wantChecksum {
		t.Errorf("0020 checksum = %q, want %q", checksum, wantChecksum)
	}
	wantDeleted := 1
	if active {
		wantDeleted = 0
	}
	if isDeleted != wantDeleted {
		t.Errorf("0020 is_deleted = %d, want %d", isDeleted, wantDeleted)
	}
}

func executePublicHomeStructuredContentSQL(db *gorm.DB, raw []byte) error {
	for index, statement := range splitStatements(string(raw)) {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("statement %d: %w", index+1, err)
		}
	}
	return nil
}

func validPublicHomeStructuredContentMetadata(contract publicHomeTableContract, schema string) businessGroupTableMetadata {
	metadata := businessGroupTableMetadata{
		engine:       "InnoDB",
		characterSet: "utf8mb4",
		collation:    "utf8mb4_unicode_ci",
		columns:      append([]businessGroupColumnMetadata(nil), contract.columns...),
		foreignKeys: []businessGroupForeignKeyMetadata{{
			name: contract.foreignKey.name, column: contract.foreignKey.column, ordinal: 1,
			targetSchema: schema, targetTable: contract.foreignKey.targetTable, targetColumn: contract.foreignKey.targetColumn,
			deleteRule: "RESTRICT", updateRule: "RESTRICT",
		}},
		checks: []businessGroupCheckMetadata{{name: contract.check.name, clause: contract.check.clause, enforced: "YES"}},
	}
	for _, expected := range contract.indexes {
		for index, column := range expected.columns {
			nonUnique := 1
			if expected.unique {
				nonUnique = 0
			}
			metadata.indexes = append(metadata.indexes, businessGroupIndexMetadata{
				name: expected.name, column: column, sequence: index + 1, nonUnique: nonUnique,
				collation: sql.NullString{String: "A", Valid: true}, indexType: "BTREE", visible: "YES",
			})
		}
	}
	return metadata
}
