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

	projectdb "github.com/porsche/ai-gateway-go/internal/db"
	"gorm.io/gorm"
)

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func TestAdminOperationSafetyFixturePreservesDependentRollbackOrder(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	rollback, restore, err := adminOperationSafetyFixtureDependencyOrder(migrations)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{rollback[0].Version, rollback[1].Version, rollback[2].Version, rollback[3].Version}; got[0] != "0009" || got[1] != "0008" || got[2] != "0006" || got[3] != "0005" {
		t.Fatalf("rollback order = %v, want [0009 0008 0006 0005]", got)
	}
	if got := []string{restore[0].Version, restore[1].Version, restore[2].Version, restore[3].Version}; got[0] != "0005" || got[1] != "0006" || got[2] != "0008" || got[3] != "0009" {
		t.Fatalf("restore order = %v, want [0005 0006 0008 0009]", got)
	}
}

func TestAdminOperationSafetyRealMySQLDownUpAndVerifier(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if raw == "" {
		t.Skip("requires isolated TEST_DATABASE_URL MySQL fixture")
	}
	if !isTestDatabaseURL(raw) {
		t.Fatal("TEST_DATABASE_URL must point to a dedicated *_test database")
	}
	gdb, err := projectdb.Open(raw, "test")
	if err != nil {
		t.Fatalf("open isolated MySQL fixture: %v", err)
	}
	nextGUID := int64(9_050_000_000_000_000)
	if err := Up(context.Background(), gdb, func() int64 {
		nextGUID++
		return nextGUID
	}, func() int64 { return 1_900_000_000_000 }); err != nil {
		t.Fatal("initialize isolated MySQL fixture migrations failed")
	}
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 9 || migrations[4].Version != "0005" || migrations[5].Version != "0006" || migrations[6].Version != "0007" || migrations[7].Version != "0008" || migrations[8].Version != "0009" {
		t.Fatalf("unexpected migration sequence: %#v", migrations)
	}
	rollback, restore, err := adminOperationSafetyFixtureDependencyOrder(migrations)
	if err != nil {
		t.Fatal(err)
	}
	beforeLedger, err := Status(context.Background(), gdb)
	if err != nil {
		t.Fatalf("read migration ledger before rollback: %v", err)
	}
	restored := false
	t.Cleanup(func() {
		if restored {
			return
		}
		for _, migration := range restore {
			if err := executeAdminOperationSafetyFixtureSQL(gdb, migration.UpSQL); err != nil {
				t.Errorf("restore %s after failed down/up test: %v", migration.Version, err)
				return
			}
		}
		for _, version := range []string{"0006", "0008", "0009"} {
			if err := setFixtureMigrationActive(gdb, version, true); err != nil {
				t.Errorf("restore %s migration ledger after failed down/up test: %v", version, err)
			}
		}
	})
	if err := Verify(context.Background(), gdb); err != nil {
		t.Fatalf("verify fresh 0001..0009 schema: %v", err)
	}
	for _, migration := range rollback {
		if err := executeAdminOperationSafetyFixtureSQL(gdb, migration.DownSQL); err != nil {
			t.Fatalf("apply fixture-only %s down: %v", migration.Version, err)
		}
		if migration.Version != "0005" {
			if err := setFixtureMigrationActive(gdb, migration.Version, false); err != nil {
				t.Fatalf("deactivate %s fixture migration ledger: %v", migration.Version, err)
			}
		}
	}
	for _, table := range []string{"admin_action_verifications", "admin_operations"} {
		var count int64
		if err := gdb.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?`, table).Scan(&count).Error; err != nil {
			t.Fatalf("inspect dropped table %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("0005 down retained %s", table)
		}
	}
	for _, table := range []string{"schema_migrations", "users", "user_sessions", "user_permission_heads", "user_permission_overrides"} {
		var count int64
		if err := gdb.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?`, table).Scan(&count).Error; err != nil {
			t.Fatalf("inspect retained table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("0005 down changed prior migration table %s", table)
		}
	}
	var countIndex int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'users' AND index_name = 'idx_users_admin_read_count'`).Scan(&countIndex).Error; err != nil {
		t.Fatalf("inspect retained 0004 index: %v", err)
	}
	if countIndex != 3 {
		t.Fatalf("0005 down changed 0004 index columns: %d", countIndex)
	}
	for _, migration := range restore {
		if err := executeAdminOperationSafetyFixtureSQL(gdb, migration.UpSQL); err != nil {
			t.Fatalf("reapply fixture-only %s up: %v", migration.Version, err)
		}
	}
	for _, version := range []string{"0006", "0008", "0009"} {
		if err := setFixtureMigrationActive(gdb, version, true); err != nil {
			t.Fatalf("reactivate %s fixture migration ledger: %v", version, err)
		}
	}
	afterLedger, err := Status(context.Background(), gdb)
	if err != nil {
		t.Fatalf("read migration ledger after restore: %v", err)
	}
	if len(afterLedger) != len(beforeLedger) {
		t.Fatalf("restored migration ledger count = %d, want %d", len(afterLedger), len(beforeLedger))
	}
	for i := range beforeLedger {
		if afterLedger[i] != beforeLedger[i] {
			t.Fatalf("restored migration ledger[%d] = %#v, want %#v", i, afterLedger[i], beforeLedger[i])
		}
	}
	if err := Verify(context.Background(), gdb); err != nil {
		t.Fatalf("verify schema after dependency-safe 0009/0008/0006/0005 down/up: %v", err)
	}
	restored = true
	var enforcedChecks int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM information_schema.table_constraints tc JOIN information_schema.check_constraints cc ON cc.constraint_schema = tc.constraint_schema AND cc.constraint_name = tc.constraint_name WHERE tc.table_schema = DATABASE() AND tc.table_name IN ('admin_action_verifications','admin_operations') AND tc.constraint_type = 'CHECK' AND tc.enforced = 'YES' AND cc.check_clause <> ''`).Scan(&enforcedChecks).Error; err != nil {
		t.Fatalf("read MySQL 8 CHECK metadata: %v", err)
	}
	if enforcedChecks != 11 {
		t.Fatalf("enforced CHECK count = %d, want 11", enforcedChecks)
	}
}

func executeAdminOperationSafetyFixtureSQL(gdb *gorm.DB, sqlBytes []byte) error {
	for _, statement := range splitStatements(string(sqlBytes)) {
		if err := gdb.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func adminOperationSafetyFixtureDependencyOrder(migrations []Migration) ([]Migration, []Migration, error) {
	byVersion := make(map[string]Migration, len(migrations))
	for _, migration := range migrations {
		if migration.Version == "" {
			return nil, nil, fmt.Errorf("fixture migration has empty version")
		}
		if _, exists := byVersion[migration.Version]; exists {
			return nil, nil, fmt.Errorf("fixture migration %s is duplicated", migration.Version)
		}
		byVersion[migration.Version] = migration
	}
	operationSafety, operationSafetyOK := byVersion["0005"]
	outbox, outboxOK := byVersion["0006"]
	responses, responsesOK := byVersion["0008"]
	integrity, integrityOK := byVersion["0009"]
	if !operationSafetyOK || !outboxOK || !responsesOK || !integrityOK {
		return nil, nil, fmt.Errorf("fixture requires migrations 0005, 0006, 0008, and 0009")
	}
	return []Migration{integrity, responses, outbox, operationSafety}, []Migration{operationSafety, outbox, responses, integrity}, nil
}

func setFixtureMigrationActive(gdb *gorm.DB, version string, active bool) error {
	isDeleted := 1
	if active {
		isDeleted = 0
	}
	result := gdb.Exec("UPDATE schema_migrations SET is_deleted=? WHERE version=?", isDeleted, version)
	if result.Error != nil {
		return result.Error
	}
	var count int64
	if err := gdb.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version=? AND is_deleted=?", version, isDeleted).Scan(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("migration ledger state matched %d rows, want 1", count)
	}
	return nil
}

func cloneAdminOperationTableMetadata(value adminOperationTableMetadata) adminOperationTableMetadata {
	value.columns = append([]adminOperationColumnMetadata(nil), value.columns...)
	value.indexes = append([]adminOperationIndexMetadata(nil), value.indexes...)
	value.foreignKeys = append([]adminOperationForeignKeyMetadata(nil), value.foreignKeys...)
	value.checks = append([]adminOperationCheckMetadata(nil), value.checks...)
	return value
}

func TestAdminOperationSafetyMigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 9 || migrations[4].Version != "0005" || migrations[5].Version != "0006" || migrations[6].Version != "0007" || migrations[7].Version != "0008" || migrations[8].Version != "0009" {
		t.Fatalf("admin operation safety migration 0005 is missing: %#v", migrations)
	}

	up := strings.ToLower(string(migrations[4].UpSQL))
	for _, fragment := range []string{
		"create table if not exists admin_action_verifications",
		"create table if not exists admin_operations",
		"uk_admin_action_verifications_guid (guid)",
		"uk_admin_action_verifications_ticket_hmac (ticket_hmac)",
		"idx_admin_action_verifications_actor_session_active (actor_user_id, session_id, is_deleted, expires_at)",
		"idx_admin_action_verifications_action_target_active (action, target_kind, target_guid, is_deleted)",
		"idx_admin_action_verifications_expiry (is_deleted, expires_at)",
		"fk_admin_action_verifications_actor",
		"fk_admin_action_verifications_session",
		"uk_admin_operations_guid (guid)",
		"uk_admin_operations_public_ref (public_ref)",
		"uk_admin_operations_actor_action_key (actor_user_id, action, idempotency_key_hmac)",
		"uk_admin_operations_verification (verification_id)",
		"idx_admin_operations_state_session (state, session_id, is_deleted, lease_expires_at)",
		"idx_admin_operations_recovery (state, is_deleted, lease_expires_at)",
		"idx_admin_operations_expiry (is_deleted, query_expires_at)",
		"fk_admin_operations_actor",
		"fk_admin_operations_session",
		"fk_admin_operations_verification",
		"engine=innodb default charset=utf8mb4 collate=utf8mb4_unicode_ci",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0005 missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"timestamp", "datetime", " enum(", "create trigger", "delete from"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("0005 contains forbidden %q", forbidden)
		}
	}
	for _, forbidden := range []string{
		"fk_admin_action_verifications_created_by",
		"fk_admin_action_verifications_updated_by",
		"fk_admin_operations_created_by",
		"fk_admin_operations_updated_by",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("0005 contains non-contract audit FK %q", forbidden)
		}
	}
	for _, fragment := range []string{
		"consumed_at is null or is_deleted = 1",
		"state = 5 and is_deleted = 1",
		"state in (1, 2, 3, 4) and is_deleted = 0",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0005 missing lifecycle coherence %q", fragment)
		}
	}
	for _, support := range []struct {
		index      string
		constraint string
	}{
		{"\n  KEY fk_admin_action_verifications_session (session_id),", "\n  CONSTRAINT fk_admin_action_verifications_session FOREIGN KEY"},
		{"\n  KEY fk_admin_operations_session (session_id),", "\n  CONSTRAINT fk_admin_operations_session FOREIGN KEY"},
	} {
		if got := strings.Count(string(migrations[4].UpSQL), support.index); got != 1 {
			t.Errorf("explicit session support index count = %d, want exactly 1 for %q", got, support.index)
		}
		indexAt := strings.Index(string(migrations[4].UpSQL), support.index)
		constraintAt := strings.Index(string(migrations[4].UpSQL), support.constraint)
		if indexAt < 0 || constraintAt < 0 || indexAt >= constraintAt {
			t.Errorf("session support index must precede matching FK constraint: index=%d constraint=%d", indexAt, constraintAt)
		}
	}

	down := strings.ToLower(strings.TrimSpace(string(migrations[4].DownSQL)))
	wantDown := "drop table if exists admin_operations;\ndrop table if exists admin_action_verifications;"
	if down != wantDown {
		t.Fatalf("0005 down = %q, want exact dependency-safe rollback %q", down, wantDown)
	}
}

func TestAdminOperationSafetyMetadataComparisonIsExact(t *testing.T) {
	want := adminOperationTableContract{
		name: "sample",
		columns: []adminOperationColumnContract{
			{name: "id", columnType: "bigint", nullable: "NO", extra: "auto_increment"},
			{name: "is_deleted", columnType: "int", nullable: "NO", defaultVal: nullString("0")},
		},
		indexes: []adminOperationIndexContract{
			{name: "PRIMARY", columns: []string{"id"}, unique: true},
			{name: "idx_sample", columns: []string{"is_deleted", "id"}, unique: false},
		},
		foreignKeys: []adminOperationForeignKeyContract{
			{name: "fk_sample", column: "id", targetTable: "users", targetColumn: "id"},
		},
		checks: []adminOperationCheckContract{{name: "chk_sample", clause: "is_deleted IN (0, 1)", enforced: "YES"}},
	}
	valid := adminOperationTableMetadata{
		engine: "InnoDB", collation: "utf8mb4_unicode_ci",
		columns: []adminOperationColumnMetadata{
			{name: "id", columnType: "bigint", nullable: "NO", extra: "auto_increment"},
			{name: "is_deleted", columnType: "int", nullable: "NO", defaultVal: nullString("0")},
		},
		indexes: []adminOperationIndexMetadata{
			{name: "idx_sample", column: "is_deleted", sequence: 1, nonUnique: 1},
			{name: "idx_sample", column: "id", sequence: 2, nonUnique: 1},
			{name: "PRIMARY", column: "id", sequence: 1, nonUnique: 0},
		},
		foreignKeys: []adminOperationForeignKeyMetadata{
			{name: "fk_sample", column: "id", ordinal: 1, targetSchema: "fixture", targetTable: "users", targetColumn: "id", deleteRule: "RESTRICT", updateRule: "NO ACTION"},
		},
		checks: []adminOperationCheckMetadata{{name: "chk_sample", clause: " (((`IS_DELETED` in ( 0 , 1 ))) ) ", enforced: "YES"}},
	}
	if !matchesAdminOperationTableContract(want, valid, "fixture") {
		t.Fatal("exact metadata rejected")
	}

	mutations := []struct {
		name string
		edit func(*adminOperationTableMetadata)
	}{
		{"engine", func(got *adminOperationTableMetadata) { got.engine = "MyISAM" }},
		{"collation", func(got *adminOperationTableMetadata) { got.collation = "utf8mb4_bin" }},
		{"column_name", func(got *adminOperationTableMetadata) { got.columns[0].name = "other" }},
		{"column_order", func(got *adminOperationTableMetadata) {
			got.columns[0], got.columns[1] = got.columns[1], got.columns[0]
		}},
		{"column_type", func(got *adminOperationTableMetadata) { got.columns[0].columnType = "int" }},
		{"column_nullable", func(got *adminOperationTableMetadata) { got.columns[0].nullable = "YES" }},
		{"column_default", func(got *adminOperationTableMetadata) { got.columns[1].defaultVal = nullString("1") }},
		{"column_extra", func(got *adminOperationTableMetadata) { got.columns[0].extra = "" }},
		{"column_unsigned", func(got *adminOperationTableMetadata) { got.columns[0].columnType = "bigint unsigned" }},
		{"missing_index", func(got *adminOperationTableMetadata) { got.indexes = got.indexes[:1] }},
		{"extra_index", func(got *adminOperationTableMetadata) {
			got.indexes = append(got.indexes, adminOperationIndexMetadata{name: "extra", column: "id", sequence: 1, nonUnique: 1})
		}},
		{"wrong_index_order", func(got *adminOperationTableMetadata) { got.indexes[0].sequence, got.indexes[1].sequence = 2, 1 }},
		{"duplicate_index_sequence", func(got *adminOperationTableMetadata) { got.indexes = append(got.indexes, got.indexes[0]) }},
		{"missing_fk", func(got *adminOperationTableMetadata) { got.foreignKeys = nil }},
		{"extra_fk", func(got *adminOperationTableMetadata) {
			got.foreignKeys = append(got.foreignKeys, adminOperationForeignKeyMetadata{name: "extra"})
		}},
		{"wrong_fk_rule", func(got *adminOperationTableMetadata) { got.foreignKeys[0].deleteRule = "CASCADE" }},
		{"duplicate_fk", func(got *adminOperationTableMetadata) { got.foreignKeys = append(got.foreignKeys, got.foreignKeys[0]) }},
		{"missing_check", func(got *adminOperationTableMetadata) { got.checks = nil }},
		{"extra_check", func(got *adminOperationTableMetadata) {
			got.checks = append(got.checks, adminOperationCheckMetadata{name: "extra", clause: "is_deleted = 0", enforced: "YES"})
		}},
		{"duplicate_check", func(got *adminOperationTableMetadata) { got.checks = append(got.checks, got.checks[0]) }},
		{"disabled_check", func(got *adminOperationTableMetadata) { got.checks[0].enforced = "NO" }},
		{"check_expression", func(got *adminOperationTableMetadata) { got.checks[0].clause = "is_deleted IN (0, 2)" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			got := cloneAdminOperationTableMetadata(valid)
			tc.edit(&got)
			if matchesAdminOperationTableContract(want, got, "fixture") {
				t.Fatal("schema drift accepted")
			}
		})
	}
}

func TestAdminOperationSafetyVerifierErrorsAreFailClosedAndRedacted(t *testing.T) {
	err := VerifyAdminOperationSafetySchema(context.Background(), nil)
	if !errors.Is(err, ErrAdminOperationSafetySchema) {
		t.Fatalf("nil database error = %v", err)
	}
	if err.Error() != "admin operation safety schema mismatch or unavailable" {
		t.Fatalf("verifier exposed variable diagnostics: %q", err)
	}
	if matchesAdminOperationTableContract(adminOperationTableContract{}, adminOperationTableMetadata{}, "private_schema") {
		t.Fatal("unavailable metadata accepted")
	}
}

func TestAdminOperationSafetyCheckContractsUseSupportedStrictGrammar(t *testing.T) {
	for _, table := range adminOperationSafetyContracts() {
		for _, check := range table.checks {
			if canonical, ok := canonicalizeCheckClause(check.clause); !ok || canonical == "" {
				t.Errorf("contract %s.%s is not canonicalizable", table.name, check.name)
			}
		}
	}
	for _, unsafe := range []string{
		"is_deleted IN (0, 1) OR 1 = 1",
		"LOWER(state) = 1",
		"state + 1 = 2",
		"state = '1'",
		"state IN (1, 2) /* drift */",
	} {
		if canonical, ok := canonicalizeCheckClause(unsafe); ok || canonical != "" {
			t.Errorf("unsupported CHECK syntax accepted: %q", unsafe)
		}
	}
}

func TestMigrationSequencePreservesPublishedChecksums(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		version  string
		checksum string
	}{
		{"0001", "2da41ffd07c44d45cb05a705f867db2f2b8f01defb519000197dedce9998aedd"},
		{"0002", "58712428ca668fb1fea0943d71a2209b2e7faf26de043d870195a033ac0f413c"},
		{"0003", "31c49d9bb1f171d9ea6caab49714d9de05552b8f6e9cb73f2989760efd0a015c"},
		{"0004", "44b5caba0473162c239e6b3035e6d9067e3af0b35e998a79c827a1424621494e"},
		{"0005", "4fc34da357e155c4a04548838153796803f618f0c7199adda17098ef5190cd68"},
		{"0006", "c0bc9f68370985315db711c0028ee644dafe4d9667c595a49193ed38e2d6f6b6"},
		{"0007", "b3c3351771fce2dbf5466d300cb92ffd5dbd183cffaff15671e46a8bc87e143e"},
		{"0008", "21289da334e7ef4425f697c659e6f45227e88c4d86ac4c099867dd6895f666f2"},
		{"0009", "4dc818d93180bb6777d2ec6d8318e728fe76add4c736a178f19b808ca2afedf7"},
	}
	if len(want) != len(migrations) {
		t.Fatalf("checksum list length = %d, migrations = %d", len(want), len(migrations))
	}
	for i, published := range want {
		if migrations[i].Version != published.version {
			t.Fatalf("migration[%d] version = %q, want %q", i, migrations[i].Version, published.version)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(migrations[i].UpSQL)); got != published.checksum {
			t.Fatalf("migration %s checksum = %s, want immutable %s", published.version, got, published.checksum)
		}
	}
}
