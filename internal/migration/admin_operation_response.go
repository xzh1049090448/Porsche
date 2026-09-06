package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gorm.io/gorm"
)

var ErrAdminOperationResponseSchema = errors.New("admin operation response schema mismatch or unavailable")

func adminOperationResponseTableContract() businessGroupTableContract {
	return businessGroupTableContract{
		name: "admin_operation_responses",
		columns: []businessGroupColumnContract{
			{name: "id", columnType: "bigint", nullable: "NO", extra: "auto_increment"},
			{name: "guid", columnType: "bigint", nullable: "NO"},
			{name: "created_at", columnType: "bigint", nullable: "NO"},
			{name: "created_by", columnType: "bigint", nullable: "YES"},
			{name: "updated_at", columnType: "bigint", nullable: "NO"},
			{name: "updated_by", columnType: "bigint", nullable: "YES"},
			{name: "is_deleted", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "0", Valid: true}},
			{name: "operation_id", columnType: "bigint", nullable: "NO"},
			{name: "http_status", columnType: "int", nullable: "NO"},
			{name: "media_type", columnType: "varchar(64)", nullable: "NO", characterSet: "ascii", collation: "ascii_bin"},
			{name: "response_body", columnType: "varbinary(4096)", nullable: "NO"},
			{name: "body_sha256", columnType: "char(64)", nullable: "NO", characterSet: "ascii", collation: "ascii_bin"},
		},
		indexes: []businessGroupIndexContract{
			{name: "PRIMARY", columns: []string{"id"}, unique: true},
			{name: "uk_admin_operation_responses_guid", columns: []string{"guid"}, unique: true},
			{name: "uk_admin_operation_responses_operation", columns: []string{"operation_id"}, unique: true},
			{name: "idx_admin_operation_responses_active", columns: []string{"is_deleted", "created_at"}, unique: false},
		},
		checks: []businessGroupCheckContract{
			{name: "chk_admin_operation_responses_http", clause: "http_status = 201 AND media_type = 'application/json'", enforced: "YES"},
			{name: "chk_admin_operation_responses_body", clause: "OCTET_LENGTH(response_body) BETWEEN 2 AND 4096 AND OCTET_LENGTH(body_sha256) = 64", enforced: "YES"},
			{name: "chk_admin_operation_responses_immutable", clause: "created_at >= 0 AND updated_at = created_at AND is_deleted = 0 AND ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))", enforced: "YES"},
		},
	}
}

// VerifyAdminOperationResponseSchema rejects partial or drifted snapshot
// tables before the application exposes managed action routes.
func VerifyAdminOperationResponseSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrAdminOperationResponseSchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrAdminOperationResponseSchema
	}
	contract := adminOperationResponseTableContract()
	metadata, ok := loadBusinessGroupTableMetadata(ctx, db, contract.name)
	if !ok || !matchesAdminOperationResponseContract(contract, metadata, currentSchema) {
		return ErrAdminOperationResponseSchema
	}
	return nil
}

func matchesAdminOperationResponseContract(want businessGroupTableContract, got businessGroupTableMetadata, currentSchema string) bool {
	if got.engine != "InnoDB" || got.characterSet != "utf8mb4" || got.collation != "utf8mb4_unicode_ci" || len(got.columns) != len(want.columns) {
		return false
	}
	for i, expected := range want.columns {
		actual := got.columns[i]
		columnType := strings.ToLower(actual.columnType)
		if actual.name != expected.name || columnType != expected.columnType || strings.Contains(columnType, "unsigned") || actual.nullable != expected.nullable ||
			actual.defaultVal != expected.defaultVal || strings.ToLower(actual.extra) != expected.extra || actual.characterSet != expected.characterSet || actual.collation != expected.collation {
			return false
		}
	}
	indexes := make(map[string][]businessGroupIndexMetadata, len(want.indexes))
	for _, index := range got.indexes {
		indexes[index.name] = append(indexes[index.name], index)
	}
	if len(indexes) != len(want.indexes) {
		return false
	}
	for _, expected := range want.indexes {
		rows := indexes[expected.name]
		if len(rows) != len(expected.columns) {
			return false
		}
		for i, row := range rows {
			if row.sequence != i+1 || row.column != expected.columns[i] || (row.nonUnique == 0) != expected.unique || !validRequiredBusinessGroupIndexMetadata(row) {
				return false
			}
		}
	}
	if len(got.foreignKeys) != 1 {
		return false
	}
	fk := got.foreignKeys[0]
	if fk.name != "fk_admin_operation_responses_operation" || fk.column != "operation_id" || fk.ordinal != 1 || fk.targetSchema != currentSchema ||
		fk.targetTable != "admin_operations" || fk.targetColumn != "id" || !restrictRule(fk.deleteRule) || !restrictRule(fk.updateRule) {
		return false
	}
	checks := make(map[string][]businessGroupCheckMetadata, len(want.checks))
	for _, check := range got.checks {
		checks[check.name] = append(checks[check.name], check)
	}
	if len(checks) != len(want.checks) {
		return false
	}
	for _, expected := range want.checks {
		rows := checks[expected.name]
		if len(rows) != 1 || rows[0].enforced != "YES" {
			return false
		}
		if normalizeAdminOperationResponseCheck(rows[0].clause) != normalizeAdminOperationResponseCheck(expected.clause) {
			return false
		}
	}
	return true
}

func normalizeAdminOperationResponseCheck(clause string) string {
	normalized := strings.ToLower(clause)
	normalized = strings.ReplaceAll(normalized, "`", "")
	normalized = strings.ReplaceAll(normalized, "_utf8mb4", "")
	normalized = strings.ReplaceAll(normalized, `\'`, `'`)
	normalized = strings.ReplaceAll(normalized, "octet_length", "length")
	for _, discarded := range []string{" ", "\t", "\r", "\n", "(", ")"} {
		normalized = strings.ReplaceAll(normalized, discarded, "")
	}
	return normalized
}
