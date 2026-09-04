package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func cloneAdminOperationTableMetadata(value adminOperationTableMetadata) adminOperationTableMetadata {
	value.columns = append([]adminOperationColumnMetadata(nil), value.columns...)
	value.indexes = append([]adminOperationIndexMetadata(nil), value.indexes...)
	value.foreignKeys = append([]adminOperationForeignKeyMetadata(nil), value.foreignKeys...)
	value.checks = append([]string(nil), value.checks...)
	return value
}

func TestAdminOperationSafetyMigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 5 || migrations[4].Version != "0005" {
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
		checks: []string{"chk_sample"},
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
		checks: []string{"chk_sample"},
	}
	if !matchesAdminOperationTableContract(want, valid, "fixture") {
		t.Fatal("exact metadata rejected")
	}

	mutations := []struct {
		name string
		edit func(*adminOperationTableMetadata)
	}{
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
	}
	if len(migrations) != 5 {
		t.Fatalf("migration count = %d, want 5", len(migrations))
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
