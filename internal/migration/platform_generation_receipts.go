package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gorm.io/gorm"
)

var ErrPlatformGenerationReceiptSchema = errors.New("platform generation receipt schema mismatch or unavailable")

type platformGenerationReceiptForeignKeyContract struct {
	name         string
	column       string
	targetTable  string
	targetColumn string
}

type platformGenerationReceiptTableContract struct {
	table       businessGroupTableContract
	foreignKeys []platformGenerationReceiptForeignKeyContract
}

func platformGenerationReceiptTableContracts() []platformGenerationReceiptTableContract {
	return []platformGenerationReceiptTableContract{
		{
			table: businessGroupTableContract{
				name: "platform_chat_generation_receipts",
				columns: []businessGroupColumnContract{
					{name: "id", columnType: "bigint", nullable: "NO", extra: "auto_increment"},
					{name: "guid", columnType: "bigint", nullable: "NO"},
					{name: "user_id", columnType: "bigint", nullable: "NO"},
					{name: "generation_id", columnType: "char(36)", nullable: "NO", characterSet: "ascii", collation: "ascii_bin"},
					{name: "mode", columnType: "int", nullable: "NO"},
					{name: "requested_existing_conversation", columnType: "tinyint", nullable: "NO"},
					{name: "conversation_id", columnType: "bigint", nullable: "NO"},
					{name: "user_message_id", columnType: "bigint", nullable: "NO"},
					{name: "successful_model_count", columnType: "int", nullable: "NO"},
					{name: "daily_calls_charged", columnType: "int", nullable: "NO"},
					{name: "total_tokens", columnType: "bigint", nullable: "NO"},
					{name: "committed_at", columnType: "bigint", nullable: "NO"},
					{name: "created_at", columnType: "bigint", nullable: "NO"},
					{name: "created_by", columnType: "bigint", nullable: "YES"},
					{name: "updated_at", columnType: "bigint", nullable: "NO"},
					{name: "updated_by", columnType: "bigint", nullable: "YES"},
					{name: "is_deleted", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "0", Valid: true}},
				},
				indexes: []businessGroupIndexContract{
					{name: "PRIMARY", columns: []string{"id"}, unique: true},
					{name: "uk_platform_chat_generation_receipts_guid", columns: []string{"guid"}, unique: true},
					{name: "uk_platform_chat_generation_receipts_owner_generation", columns: []string{"user_id", "generation_id"}, unique: true},
					{name: "uk_platform_chat_generation_receipts_user_message", columns: []string{"user_message_id"}, unique: true},
					{name: "idx_platform_chat_generation_receipts_owner_active_created", columns: []string{"user_id", "is_deleted", "created_at"}, unique: false},
					{name: "idx_platform_chat_generation_receipts_conversation_active", columns: []string{"conversation_id", "is_deleted"}, unique: false},
				},
				checks: []businessGroupCheckContract{
					{name: "chk_platform_chat_generation_receipts_mode", clause: "mode IN (1, 2)", enforced: "YES"},
					{name: "chk_platform_chat_generation_receipts_requested_conversation", clause: "requested_existing_conversation IN (0, 1)", enforced: "YES"},
					{name: "chk_platform_chat_generation_receipts_counts", clause: "successful_model_count >= 1 AND daily_calls_charged = successful_model_count AND total_tokens >= 0", enforced: "YES"},
					{name: "chk_platform_chat_generation_receipts_time", clause: "committed_at > 0 AND updated_at = created_at", enforced: "YES"},
					{name: "chk_platform_chat_generation_receipts_deleted", clause: "is_deleted IN (0, 1)", enforced: "YES"},
				},
			},
			foreignKeys: []platformGenerationReceiptForeignKeyContract{
				{name: "fk_platform_chat_generation_receipts_user", column: "user_id", targetTable: "users", targetColumn: "id"},
				{name: "fk_platform_chat_generation_receipts_conversation", column: "conversation_id", targetTable: "conversations", targetColumn: "id"},
				{name: "fk_platform_chat_generation_receipts_user_message", column: "user_message_id", targetTable: "messages", targetColumn: "id"},
			},
		},
		{
			table: businessGroupTableContract{
				name: "platform_chat_generation_results",
				columns: []businessGroupColumnContract{
					{name: "id", columnType: "bigint", nullable: "NO", extra: "auto_increment"},
					{name: "guid", columnType: "bigint", nullable: "NO"},
					{name: "receipt_id", columnType: "bigint", nullable: "NO"},
					{name: "model_index", columnType: "int", nullable: "NO"},
					{name: "model", columnType: "varchar(128)", nullable: "NO", characterSet: "utf8mb4", collation: "utf8mb4_bin"},
					{name: "status", columnType: "int", nullable: "NO"},
					{name: "assistant_message_id", columnType: "bigint", nullable: "YES"},
					{name: "tokens", columnType: "bigint", nullable: "NO", defaultVal: sql.NullString{String: "0", Valid: true}},
					{name: "error_code", columnType: "varchar(64)", nullable: "YES", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"},
					{name: "created_at", columnType: "bigint", nullable: "NO"},
					{name: "created_by", columnType: "bigint", nullable: "YES"},
					{name: "updated_at", columnType: "bigint", nullable: "NO"},
					{name: "updated_by", columnType: "bigint", nullable: "YES"},
					{name: "is_deleted", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "0", Valid: true}},
				},
				indexes: []businessGroupIndexContract{
					{name: "PRIMARY", columns: []string{"id"}, unique: true},
					{name: "uk_platform_chat_generation_results_guid", columns: []string{"guid"}, unique: true},
					{name: "uk_platform_chat_generation_results_model", columns: []string{"receipt_id", "model"}, unique: true},
					{name: "uk_platform_chat_generation_results_position", columns: []string{"receipt_id", "model_index"}, unique: true},
					{name: "uk_platform_chat_generation_results_message", columns: []string{"assistant_message_id"}, unique: true},
					{name: "idx_platform_chat_generation_results_receipt_active", columns: []string{"receipt_id", "is_deleted"}, unique: false},
				},
				checks: []businessGroupCheckContract{
					{name: "chk_platform_chat_generation_results_position", clause: "model_index >= 0", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_status", clause: "status IN (1, 2)", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_tokens", clause: "tokens >= 0", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_shape", clause: "(status = 1 AND assistant_message_id IS NOT NULL AND error_code IS NULL) OR (status = 2 AND assistant_message_id IS NULL AND tokens = 0 AND error_code IS NOT NULL)", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_time", clause: "updated_at = created_at", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_deleted", clause: "is_deleted IN (0, 1)", enforced: "YES"},
				},
			},
			foreignKeys: []platformGenerationReceiptForeignKeyContract{
				{name: "fk_platform_chat_generation_results_receipt", column: "receipt_id", targetTable: "platform_chat_generation_receipts", targetColumn: "id"},
				{name: "fk_platform_chat_generation_results_message", column: "assistant_message_id", targetTable: "messages", targetColumn: "id"},
			},
		},
	}
}

// VerifyPlatformGenerationReceiptSchema rejects missing, partial, or drifted
// migration 0011 tables before receipt-backed streaming can be activated.
func VerifyPlatformGenerationReceiptSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPlatformGenerationReceiptSchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrPlatformGenerationReceiptSchema
	}
	for _, contract := range platformGenerationReceiptTableContracts() {
		metadata, ok := loadBusinessGroupTableMetadata(ctx, db, contract.table.name)
		if !ok || !matchesPlatformGenerationReceiptTableContract(contract, metadata, currentSchema) {
			return ErrPlatformGenerationReceiptSchema
		}
	}
	return nil
}

func matchesPlatformGenerationReceiptTableContract(want platformGenerationReceiptTableContract, got businessGroupTableMetadata, currentSchema string) bool {
	if got.engine != "InnoDB" || got.characterSet != "utf8mb4" || got.collation != "utf8mb4_unicode_ci" || len(got.columns) != len(want.table.columns) {
		return false
	}
	for i, expected := range want.table.columns {
		actual := got.columns[i]
		columnType := strings.ToLower(actual.columnType)
		if actual.name != expected.name || columnType != expected.columnType || strings.Contains(columnType, "unsigned") || actual.nullable != expected.nullable ||
			actual.defaultVal != expected.defaultVal || strings.ToLower(actual.extra) != expected.extra || actual.characterSet != expected.characterSet || actual.collation != expected.collation {
			return false
		}
	}

	indexes := make(map[string][]businessGroupIndexMetadata, len(want.table.indexes))
	for _, index := range got.indexes {
		indexes[index.name] = append(indexes[index.name], index)
	}
	if len(indexes) != len(want.table.indexes) {
		return false
	}
	for _, expected := range want.table.indexes {
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

	foreignKeys := make(map[string][]businessGroupForeignKeyMetadata, len(want.foreignKeys))
	for _, foreignKey := range got.foreignKeys {
		foreignKeys[foreignKey.name] = append(foreignKeys[foreignKey.name], foreignKey)
	}
	if len(foreignKeys) != len(want.foreignKeys) {
		return false
	}
	for _, expected := range want.foreignKeys {
		rows := foreignKeys[expected.name]
		if len(rows) != 1 {
			return false
		}
		actual := rows[0]
		if actual.ordinal != 1 || actual.column != expected.column || actual.targetSchema != currentSchema || actual.targetTable != expected.targetTable ||
			actual.targetColumn != expected.targetColumn || !restrictRule(actual.deleteRule) || !restrictRule(actual.updateRule) {
			return false
		}
	}

	checks := make(map[string][]businessGroupCheckMetadata, len(want.table.checks))
	for _, check := range got.checks {
		checks[check.name] = append(checks[check.name], check)
	}
	if len(checks) != len(want.table.checks) {
		return false
	}
	for _, expected := range want.table.checks {
		rows := checks[expected.name]
		if len(rows) != 1 || expected.enforced != "YES" || rows[0].enforced != "YES" {
			return false
		}
		wantClause, wantOK := canonicalizePlatformGenerationReceiptCheck(expected.clause)
		actualClause, actualOK := canonicalizePlatformGenerationReceiptCheck(rows[0].clause)
		if !wantOK || !actualOK || wantClause != actualClause {
			return false
		}
	}
	return true
}

func canonicalizePlatformGenerationReceiptCheck(clause string) (string, bool) {
	if canonical, ok := canonicalizeCheckClause(clause); ok {
		return canonical, true
	}
	return canonicalizeAdminOperationResponseCheck(clause)
}
