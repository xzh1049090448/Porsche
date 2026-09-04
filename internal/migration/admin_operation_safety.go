package migration

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"gorm.io/gorm"
)

var ErrAdminOperationSafetySchema = errors.New("admin operation safety schema mismatch or unavailable")

type adminOperationColumnContract struct {
	name       string
	columnType string
	nullable   string
	defaultVal sql.NullString
	extra      string
}

type adminOperationIndexContract struct {
	name    string
	columns []string
	unique  bool
}

type adminOperationForeignKeyContract struct {
	name         string
	column       string
	targetTable  string
	targetColumn string
}

type adminOperationTableContract struct {
	name        string
	columns     []adminOperationColumnContract
	indexes     []adminOperationIndexContract
	foreignKeys []adminOperationForeignKeyContract
	checks      []string
}

type adminOperationColumnMetadata struct {
	name       string
	columnType string
	nullable   string
	defaultVal sql.NullString
	extra      string
}

type adminOperationIndexMetadata struct {
	name      string
	column    string
	sequence  int
	nonUnique int
}

type adminOperationForeignKeyMetadata struct {
	name         string
	column       string
	ordinal      int
	targetSchema string
	targetTable  string
	targetColumn string
	deleteRule   string
	updateRule   string
}

type adminOperationTableMetadata struct {
	engine      string
	collation   string
	columns     []adminOperationColumnMetadata
	indexes     []adminOperationIndexMetadata
	foreignKeys []adminOperationForeignKeyMetadata
	checks      []string
}

func requiredColumn(name, columnType string) adminOperationColumnContract {
	return adminOperationColumnContract{name: name, columnType: columnType, nullable: "NO"}
}

func nullableColumn(name, columnType string) adminOperationColumnContract {
	return adminOperationColumnContract{name: name, columnType: columnType, nullable: "YES"}
}

func adminOperationSafetyContracts() []adminOperationTableContract {
	verificationColumns := []adminOperationColumnContract{
		requiredColumn("id", "bigint"), requiredColumn("guid", "bigint"),
		requiredColumn("actor_user_id", "bigint"), requiredColumn("actor_auth_version", "int"),
		requiredColumn("session_id", "bigint"), requiredColumn("action", "int"),
		requiredColumn("target_kind", "int"), nullableColumn("target_guid", "bigint"),
		requiredColumn("intent_hmac", "char(64)"), requiredColumn("ticket_hmac", "char(64)"),
		requiredColumn("expires_at", "bigint"), nullableColumn("consumed_at", "bigint"),
		requiredColumn("created_at", "bigint"), nullableColumn("created_by", "bigint"),
		requiredColumn("updated_at", "bigint"), nullableColumn("updated_by", "bigint"),
		requiredColumn("is_deleted", "int"),
	}
	verificationColumns[0].extra = "auto_increment"
	verificationColumns[len(verificationColumns)-1].defaultVal = sql.NullString{String: "0", Valid: true}

	operationColumns := []adminOperationColumnContract{
		requiredColumn("id", "bigint"), requiredColumn("guid", "bigint"),
		requiredColumn("actor_user_id", "bigint"), requiredColumn("actor_auth_version", "int"),
		requiredColumn("session_id", "bigint"), requiredColumn("action", "int"),
		requiredColumn("idempotency_key_hmac", "char(64)"), requiredColumn("request_hmac", "char(64)"),
		nullableColumn("verification_id", "bigint"), requiredColumn("state", "int"),
		requiredColumn("public_ref", "char(46)"), nullableColumn("lease_owner_hmac", "char(64)"),
		nullableColumn("lease_expires_at", "bigint"), nullableColumn("finished_at", "bigint"),
		requiredColumn("query_expires_at", "bigint"), nullableColumn("error_code", "int"),
		nullableColumn("result_kind", "int"), nullableColumn("result_guid", "bigint"),
		nullableColumn("result_http_status", "int"), requiredColumn("created_at", "bigint"),
		nullableColumn("created_by", "bigint"), requiredColumn("updated_at", "bigint"),
		nullableColumn("updated_by", "bigint"), requiredColumn("is_deleted", "int"),
	}
	operationColumns[0].extra = "auto_increment"
	operationColumns[len(operationColumns)-1].defaultVal = sql.NullString{String: "0", Valid: true}

	return []adminOperationTableContract{
		{
			name:    "admin_action_verifications",
			columns: verificationColumns,
			indexes: []adminOperationIndexContract{
				{"PRIMARY", []string{"id"}, true},
				{"uk_admin_action_verifications_guid", []string{"guid"}, true},
				{"uk_admin_action_verifications_ticket_hmac", []string{"ticket_hmac"}, true},
				{"idx_admin_action_verifications_actor_session_active", []string{"actor_user_id", "session_id", "is_deleted", "expires_at"}, false},
				{"idx_admin_action_verifications_action_target_active", []string{"action", "target_kind", "target_guid", "is_deleted"}, false},
				{"idx_admin_action_verifications_expiry", []string{"is_deleted", "expires_at"}, false},
				// InnoDB creates this source index for the fixed session FK because
				// none of the public query indexes starts with session_id.
				{"fk_admin_action_verifications_session", []string{"session_id"}, false},
			},
			foreignKeys: []adminOperationForeignKeyContract{
				{"fk_admin_action_verifications_actor", "actor_user_id", "users", "id"},
				{"fk_admin_action_verifications_session", "session_id", "user_sessions", "id"},
			},
			checks: []string{"chk_admin_action_verifications_audit", "chk_admin_action_verifications_target", "chk_admin_action_verifications_times"},
		},
		{
			name:    "admin_operations",
			columns: operationColumns,
			indexes: []adminOperationIndexContract{
				{"PRIMARY", []string{"id"}, true},
				{"uk_admin_operations_guid", []string{"guid"}, true},
				{"uk_admin_operations_public_ref", []string{"public_ref"}, true},
				{"uk_admin_operations_actor_action_key", []string{"actor_user_id", "action", "idempotency_key_hmac"}, true},
				{"uk_admin_operations_verification", []string{"verification_id"}, true},
				{"idx_admin_operations_state_session", []string{"state", "session_id", "is_deleted", "lease_expires_at"}, false},
				{"idx_admin_operations_recovery", []string{"state", "is_deleted", "lease_expires_at"}, false},
				{"idx_admin_operations_expiry", []string{"is_deleted", "query_expires_at"}, false},
				// This is the deterministic InnoDB source index for the session FK.
				{"fk_admin_operations_session", []string{"session_id"}, false},
			},
			foreignKeys: []adminOperationForeignKeyContract{
				{"fk_admin_operations_actor", "actor_user_id", "users", "id"},
				{"fk_admin_operations_session", "session_id", "user_sessions", "id"},
				{"fk_admin_operations_verification", "verification_id", "admin_action_verifications", "id"},
			},
			checks: []string{"chk_admin_operations_audit", "chk_admin_operations_failure", "chk_admin_operations_http_status", "chk_admin_operations_lease", "chk_admin_operations_result", "chk_admin_operations_state", "chk_admin_operations_terminal", "chk_admin_operations_times"},
		},
	}
}

// VerifyAdminOperationSafetySchema fails closed when migration 0005's active
// MySQL schema differs from its fixed columns, indexes, foreign keys, or checks.
func VerifyAdminOperationSafetySchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrAdminOperationSafetySchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrAdminOperationSafetySchema
	}
	for _, contract := range adminOperationSafetyContracts() {
		if !verifyAdminOperationTable(ctx, db, currentSchema, contract) {
			return ErrAdminOperationSafetySchema
		}
	}
	return nil
}

func verifyAdminOperationTable(ctx context.Context, db *gorm.DB, currentSchema string, contract adminOperationTableContract) bool {
	var metadata struct {
		Engine    string `gorm:"column:engine"`
		Collation string `gorm:"column:table_collation"`
	}
	if err := db.WithContext(ctx).Raw(`SELECT engine, table_collation FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, contract.name).Scan(&metadata).Error; err != nil {
		return false
	}
	var columnRows []struct {
		Name       string         `gorm:"column:column_name"`
		ColumnType string         `gorm:"column:column_type"`
		Nullable   string         `gorm:"column:is_nullable"`
		DefaultVal sql.NullString `gorm:"column:column_default"`
		Extra      string         `gorm:"column:extra"`
	}
	if err := db.WithContext(ctx).Raw(`SELECT column_name, column_type, is_nullable, column_default, extra FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`, contract.name).Scan(&columnRows).Error; err != nil {
		return false
	}
	actual := adminOperationTableMetadata{engine: metadata.Engine, collation: metadata.Collation}
	for _, row := range columnRows {
		actual.columns = append(actual.columns, adminOperationColumnMetadata{name: row.Name, columnType: row.ColumnType, nullable: row.Nullable, defaultVal: row.DefaultVal, extra: row.Extra})
	}
	var indexRows []struct {
		Name      string `gorm:"column:index_name"`
		Column    string `gorm:"column:column_name"`
		Sequence  int    `gorm:"column:seq_in_index"`
		NonUnique int    `gorm:"column:non_unique"`
	}
	if err := db.WithContext(ctx).Raw(`SELECT index_name, column_name, seq_in_index, non_unique FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? ORDER BY index_name, seq_in_index`, contract.name).Scan(&indexRows).Error; err != nil {
		return false
	}
	for _, row := range indexRows {
		actual.indexes = append(actual.indexes, adminOperationIndexMetadata{name: row.Name, column: row.Column, sequence: row.Sequence, nonUnique: row.NonUnique})
	}
	var foreignKeyRows []struct {
		Name         string `gorm:"column:constraint_name"`
		Column       string `gorm:"column:column_name"`
		Ordinal      int    `gorm:"column:ordinal_position"`
		TargetSchema string `gorm:"column:referenced_table_schema"`
		TargetTable  string `gorm:"column:referenced_table_name"`
		TargetColumn string `gorm:"column:referenced_column_name"`
		DeleteRule   string `gorm:"column:delete_rule"`
		UpdateRule   string `gorm:"column:update_rule"`
	}
	query := `SELECT k.constraint_name, k.column_name, k.ordinal_position, k.referenced_table_schema, k.referenced_table_name, k.referenced_column_name, r.delete_rule, r.update_rule FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.table_name=k.table_name AND r.constraint_name=k.constraint_name WHERE k.table_schema=DATABASE() AND k.table_name=? AND k.referenced_table_name IS NOT NULL ORDER BY k.constraint_name, k.ordinal_position`
	if err := db.WithContext(ctx).Raw(query, contract.name).Scan(&foreignKeyRows).Error; err != nil {
		return false
	}
	for _, row := range foreignKeyRows {
		actual.foreignKeys = append(actual.foreignKeys, adminOperationForeignKeyMetadata{name: row.Name, column: row.Column, ordinal: row.Ordinal, targetSchema: row.TargetSchema, targetTable: row.TargetTable, targetColumn: row.TargetColumn, deleteRule: row.DeleteRule, updateRule: row.UpdateRule})
	}
	if err := db.WithContext(ctx).Raw(`SELECT constraint_name FROM information_schema.table_constraints WHERE table_schema=DATABASE() AND table_name=? AND constraint_type='CHECK' ORDER BY constraint_name`, contract.name).Scan(&actual.checks).Error; err != nil {
		return false
	}
	return matchesAdminOperationTableContract(contract, actual, currentSchema)
}

func matchesAdminOperationTableContract(want adminOperationTableContract, got adminOperationTableMetadata, currentSchema string) bool {
	if got.engine != "InnoDB" || got.collation != "utf8mb4_unicode_ci" || len(got.columns) != len(want.columns) {
		return false
	}
	for i, expected := range want.columns {
		actual := got.columns[i]
		columnType := strings.ToLower(actual.columnType)
		if actual.name != expected.name || columnType != expected.columnType || actual.nullable != expected.nullable || actual.defaultVal != expected.defaultVal || strings.ToLower(actual.extra) != expected.extra || strings.Contains(columnType, "unsigned") {
			return false
		}
	}
	indexes := make(map[string][]adminOperationIndexMetadata)
	for _, index := range got.indexes {
		indexes[index.name] = append(indexes[index.name], index)
	}
	if len(indexes) != len(want.indexes) {
		return false
	}
	for _, expected := range want.indexes {
		rows, exists := indexes[expected.name]
		if !exists || len(rows) != len(expected.columns) {
			return false
		}
		for i, row := range rows {
			if row.sequence != i+1 || row.column != expected.columns[i] || (row.nonUnique == 0) != expected.unique {
				return false
			}
		}
	}
	foreignKeys := make(map[string][]adminOperationForeignKeyMetadata)
	for _, foreignKey := range got.foreignKeys {
		foreignKeys[foreignKey.name] = append(foreignKeys[foreignKey.name], foreignKey)
	}
	if len(foreignKeys) != len(want.foreignKeys) {
		return false
	}
	for _, expected := range want.foreignKeys {
		rows, exists := foreignKeys[expected.name]
		if !exists || len(rows) != 1 {
			return false
		}
		row := rows[0]
		if row.ordinal != 1 || row.column != expected.column || row.targetSchema != currentSchema || row.targetTable != expected.targetTable || row.targetColumn != expected.targetColumn || !restrictRule(row.deleteRule) || !restrictRule(row.updateRule) {
			return false
		}
	}
	wantChecks := append([]string(nil), want.checks...)
	actualChecks := append([]string(nil), got.checks...)
	sort.Strings(wantChecks)
	sort.Strings(actualChecks)
	if len(actualChecks) != len(wantChecks) {
		return false
	}
	for i := range actualChecks {
		if actualChecks[i] != wantChecks[i] {
			return false
		}
	}
	return true
}

func restrictRule(rule string) bool {
	return rule == "RESTRICT" || rule == "NO ACTION"
}
