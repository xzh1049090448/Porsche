package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestAdminActionOutboxMigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 9 || migrations[5].Version != "0006" || migrations[6].Version != "0007" || migrations[7].Version != "0008" || migrations[8].Version != "0009" {
		t.Fatalf("admin action outbox migration 0006 is missing: %#v", migrations)
	}

	up := strings.ToLower(string(migrations[5].UpSQL))
	for _, fragment := range []string{
		"create table if not exists admin_action_outbox",
		"id bigint not null auto_increment primary key",
		"guid bigint not null",
		"created_at bigint not null",
		"created_by bigint null",
		"updated_at bigint not null",
		"updated_by bigint null",
		"is_deleted int not null default 0",
		"operation_id bigint not null",
		"public_ref char(46) character set ascii collate ascii_bin not null",
		"delivery_state int not null default 1",
		"attempt_count int not null default 0",
		"unique key uk_admin_action_outbox_guid (guid)",
		"unique key uk_admin_action_outbox_operation (operation_id)",
		"unique key uk_admin_action_outbox_public_ref (public_ref)",
		"key idx_admin_action_outbox_delivery (delivery_state, is_deleted, available_at, id)",
		"key idx_admin_action_outbox_target (target_kind, target_guid, is_deleted, created_at)",
		"constraint fk_admin_action_outbox_operation foreign key (operation_id) references admin_operations(id)",
		"constraint chk_admin_action_outbox_action check (action between 1 and 2147483647)",
		"constraint chk_admin_action_outbox_target_kind check (target_kind in (1, 2, 3))",
		"constraint chk_admin_action_outbox_state check (state in (2, 3, 4))",
		"constraint chk_admin_action_outbox_delivery_state check (delivery_state in (1, 2, 3))",
		"constraint chk_admin_action_outbox_attempt_count check (attempt_count between 0 and 2147483647)",
		"engine=innodb default charset=utf8mb4 collate=utf8mb4_unicode_ci",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0006 missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"delivery_state = 1 and delivered_at is null",
		"delivery_state in (2, 3) and delivered_at is not null",
		"available_at >= 0",
		"delivered_at is null or delivered_at >= 0",
		"created_at >= 0",
		"updated_at >= 0",
		"is_deleted in (0, 1)",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0006 missing lifecycle/audit check %q", fragment)
		}
	}
	for _, forbidden := range []string{"timestamp", "datetime", " enum(", "create trigger", "delete from"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("0006 contains forbidden %q", forbidden)
		}
	}

	down := strings.ToLower(strings.TrimSpace(string(migrations[5].DownSQL)))
	if want := "drop table if exists admin_action_outbox;"; down != want {
		t.Fatalf("0006 down = %q, want exact rollback %q", down, want)
	}
}

func TestAdminActionOutboxContractIsExact(t *testing.T) {
	want := adminActionOutboxContract()
	if want.name != "admin_action_outbox" || len(want.columns) != 19 || len(want.indexes) != 6 || len(want.foreignKeys) != 1 || len(want.checks) != 9 {
		t.Fatalf("unexpected outbox contract: %#v", want)
	}
	valid := adminActionOutboxMetadataFromContract(want, "fixture")
	if !matchesAdminActionOutboxTableContract(want, valid, "fixture") {
		t.Fatal("exact outbox metadata rejected")
	}

	mutations := []struct {
		name string
		edit func(*adminActionOutboxTableMetadata)
	}{
		{"engine", func(got *adminActionOutboxTableMetadata) { got.engine = "MyISAM" }},
		{"charset", func(got *adminActionOutboxTableMetadata) { got.characterSet = "latin1" }},
		{"collation", func(got *adminActionOutboxTableMetadata) { got.collation = "utf8mb4_bin" }},
		{"column_order", func(got *adminActionOutboxTableMetadata) {
			got.columns[0], got.columns[1] = got.columns[1], got.columns[0]
		}},
		{"column_type", func(got *adminActionOutboxTableMetadata) { got.columns[0].columnType = "int" }},
		{"column_nullable", func(got *adminActionOutboxTableMetadata) { got.columns[0].nullable = "YES" }},
		{"column_default", func(got *adminActionOutboxTableMetadata) {
			got.columns[6].defaultVal = sql.NullString{String: "1", Valid: true}
		}},
		{"column_extra", func(got *adminActionOutboxTableMetadata) { got.columns[0].extra = "" }},
		{"column_unsigned", func(got *adminActionOutboxTableMetadata) { got.columns[0].columnType = "bigint unsigned" }},
		{"public_ref_charset", func(got *adminActionOutboxTableMetadata) { got.columns[8].characterSet = "utf8mb4" }},
		{"public_ref_collation", func(got *adminActionOutboxTableMetadata) { got.columns[8].collation = "utf8mb4_unicode_ci" }},
		{"missing_index", func(got *adminActionOutboxTableMetadata) { got.indexes = got.indexes[1:] }},
		{"wrong_index_order", func(got *adminActionOutboxTableMetadata) { got.indexes[4].sequence = 2 }},
		{"wrong_index_uniqueness", func(got *adminActionOutboxTableMetadata) { got.indexes[1].nonUnique = 1 }},
		{"wrong_fk_target", func(got *adminActionOutboxTableMetadata) { got.foreignKeys[0].targetTable = "users" }},
		{"wrong_fk_rule", func(got *adminActionOutboxTableMetadata) { got.foreignKeys[0].deleteRule = "CASCADE" }},
		{"missing_check", func(got *adminActionOutboxTableMetadata) { got.checks = got.checks[1:] }},
		{"disabled_check", func(got *adminActionOutboxTableMetadata) { got.checks[0].enforced = "NO" }},
		{"changed_check", func(got *adminActionOutboxTableMetadata) { got.checks[0].clause = "action BETWEEN 1 AND 8" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			got := cloneAdminActionOutboxTableMetadata(valid)
			tc.edit(&got)
			if matchesAdminActionOutboxTableContract(want, got, "fixture") {
				t.Fatal("schema drift accepted")
			}
		})
	}
}

func TestAdminActionOutboxVerifierFailsClosedAndContractsUseStrictChecks(t *testing.T) {
	err := VerifyAdminActionOutboxSchema(context.Background(), nil)
	if !errors.Is(err, ErrAdminActionOutboxSchema) {
		t.Fatalf("nil database error = %v", err)
	}
	if err.Error() != "admin action outbox schema mismatch or unavailable" {
		t.Fatalf("verifier exposed variable diagnostics: %q", err)
	}
	for _, check := range adminActionOutboxContract().checks {
		if canonical, ok := canonicalizeCheckClause(check.clause); !ok || canonical == "" {
			t.Errorf("contract check %s is not canonicalizable", check.name)
		}
	}
}

func adminActionOutboxMetadataFromContract(contract adminActionOutboxTableContract, schema string) adminActionOutboxTableMetadata {
	metadata := adminActionOutboxTableMetadata{engine: "InnoDB", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"}
	for _, column := range contract.columns {
		metadata.columns = append(metadata.columns, adminActionOutboxColumnMetadata{
			name: column.name, columnType: column.columnType, nullable: column.nullable,
			defaultVal: column.defaultVal, extra: column.extra,
			characterSet: column.characterSet, collation: column.collation,
		})
	}
	for _, index := range contract.indexes {
		for i, column := range index.columns {
			nonUnique := 1
			if index.unique {
				nonUnique = 0
			}
			metadata.indexes = append(metadata.indexes, adminActionOutboxIndexMetadata{name: index.name, column: column, sequence: i + 1, nonUnique: nonUnique})
		}
	}
	for _, foreignKey := range contract.foreignKeys {
		metadata.foreignKeys = append(metadata.foreignKeys, adminActionOutboxForeignKeyMetadata{
			name: foreignKey.name, column: foreignKey.column, ordinal: 1, targetSchema: schema,
			targetTable: foreignKey.targetTable, targetColumn: foreignKey.targetColumn,
			deleteRule: "RESTRICT", updateRule: "NO ACTION",
		})
	}
	for _, check := range contract.checks {
		metadata.checks = append(metadata.checks, adminActionOutboxCheckMetadata{name: check.name, clause: check.clause, enforced: check.enforced})
	}
	return metadata
}

func cloneAdminActionOutboxTableMetadata(value adminActionOutboxTableMetadata) adminActionOutboxTableMetadata {
	value.columns = append([]adminActionOutboxColumnMetadata(nil), value.columns...)
	value.indexes = append([]adminActionOutboxIndexMetadata(nil), value.indexes...)
	value.foreignKeys = append([]adminActionOutboxForeignKeyMetadata(nil), value.foreignKeys...)
	value.checks = append([]adminActionOutboxCheckMetadata(nil), value.checks...)
	return value
}
