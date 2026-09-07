package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gorm.io/gorm"
)

var ErrAdminActionOutboxSchema = errors.New("admin action outbox schema mismatch or unavailable")

type adminActionOutboxColumnContract struct {
	name         string
	columnType   string
	nullable     string
	defaultVal   sql.NullString
	extra        string
	characterSet string
	collation    string
}

type adminActionOutboxIndexContract = adminOperationIndexContract
type adminActionOutboxForeignKeyContract = adminOperationForeignKeyContract
type adminActionOutboxCheckContract = adminOperationCheckContract

type adminActionOutboxTableContract struct {
	name        string
	columns     []adminActionOutboxColumnContract
	indexes     []adminActionOutboxIndexContract
	foreignKeys []adminActionOutboxForeignKeyContract
	checks      []adminActionOutboxCheckContract
}

type adminActionOutboxColumnMetadata struct {
	name         string
	columnType   string
	nullable     string
	defaultVal   sql.NullString
	extra        string
	characterSet string
	collation    string
}

type adminActionOutboxIndexMetadata = adminOperationIndexMetadata
type adminActionOutboxForeignKeyMetadata = adminOperationForeignKeyMetadata
type adminActionOutboxCheckMetadata = adminOperationCheckMetadata

type adminActionOutboxTableMetadata struct {
	engine       string
	characterSet string
	collation    string
	columns      []adminActionOutboxColumnMetadata
	indexes      []adminActionOutboxIndexMetadata
	foreignKeys  []adminActionOutboxForeignKeyMetadata
	checks       []adminActionOutboxCheckMetadata
}

func requiredOutboxColumn(name, columnType string) adminActionOutboxColumnContract {
	return adminActionOutboxColumnContract{name: name, columnType: columnType, nullable: "NO"}
}

func nullableOutboxColumn(name, columnType string) adminActionOutboxColumnContract {
	return adminActionOutboxColumnContract{name: name, columnType: columnType, nullable: "YES"}
}

func adminActionOutboxContract() adminActionOutboxTableContract {
	columns := []adminActionOutboxColumnContract{
		requiredOutboxColumn("id", "bigint"),
		requiredOutboxColumn("guid", "bigint"),
		requiredOutboxColumn("created_at", "bigint"),
		nullableOutboxColumn("created_by", "bigint"),
		requiredOutboxColumn("updated_at", "bigint"),
		nullableOutboxColumn("updated_by", "bigint"),
		requiredOutboxColumn("is_deleted", "int"),
		requiredOutboxColumn("operation_id", "bigint"),
		requiredOutboxColumn("public_ref", "char(46)"),
		requiredOutboxColumn("action", "int"),
		requiredOutboxColumn("target_kind", "int"),
		nullableOutboxColumn("target_guid", "bigint"),
		requiredOutboxColumn("state", "int"),
		nullableOutboxColumn("failure_code", "int"),
		nullableOutboxColumn("result_kind", "int"),
		nullableOutboxColumn("result_guid", "bigint"),
		requiredOutboxColumn("delivery_state", "int"),
		requiredOutboxColumn("available_at", "bigint"),
		nullableOutboxColumn("delivered_at", "bigint"),
		requiredOutboxColumn("attempt_count", "int"),
	}
	columns[0].extra = "auto_increment"
	columns[6].defaultVal = sql.NullString{String: "0", Valid: true}
	columns[8].characterSet = "ascii"
	columns[8].collation = "ascii_bin"
	columns[16].defaultVal = sql.NullString{String: "1", Valid: true}
	columns[19].defaultVal = sql.NullString{String: "0", Valid: true}

	return adminActionOutboxTableContract{
		name:    "admin_action_outbox",
		columns: columns,
		indexes: []adminActionOutboxIndexContract{
			{"PRIMARY", []string{"id"}, true},
			{"uk_admin_action_outbox_guid", []string{"guid"}, true},
			{"uk_admin_action_outbox_operation", []string{"operation_id"}, true},
			{"uk_admin_action_outbox_public_ref", []string{"public_ref"}, true},
			{"idx_admin_action_outbox_delivery", []string{"delivery_state", "is_deleted", "available_at", "id"}, false},
			{"idx_admin_action_outbox_target", []string{"target_kind", "target_guid", "is_deleted", "created_at"}, false},
		},
		foreignKeys: []adminActionOutboxForeignKeyContract{
			{"fk_admin_action_outbox_operation", "operation_id", "admin_operations", "id"},
		},
		checks: []adminActionOutboxCheckContract{
			{"chk_admin_action_outbox_action", "action BETWEEN 1 AND 2147483647", "YES"},
			{"chk_admin_action_outbox_attempt_count", "attempt_count BETWEEN 0 AND 2147483647", "YES"},
			{"chk_admin_action_outbox_audit", "created_at >= 0 AND updated_at >= 0 AND is_deleted IN (0, 1)", "YES"},
			{"chk_admin_action_outbox_delivery", "(delivery_state = 1 AND delivered_at IS NULL) OR (delivery_state IN (2, 3) AND delivered_at IS NOT NULL)", "YES"},
			{"chk_admin_action_outbox_delivery_state", "delivery_state IN (1, 2, 3)", "YES"},
			{"chk_admin_action_outbox_outcome", "(state = 2 AND failure_code IS NULL AND (result_kind IS NULL OR result_kind IN (1, 2, 3)) AND result_guid IS NOT NULL) OR (state = 3 AND failure_code IN (1, 2, 3, 4, 5) AND result_kind IS NULL AND result_guid IS NULL)", "YES"},
			{"chk_admin_action_outbox_state", "state IN (2, 3, 4)", "YES"},
			{"chk_admin_action_outbox_target_kind", "target_kind IN (1, 2, 3)", "YES"},
			{"chk_admin_action_outbox_times", "available_at >= 0 AND (delivered_at IS NULL OR delivered_at >= 0)", "YES"},
		},
	}
}

// VerifyAdminActionOutboxSchema fails closed when migration 0006's active
// MySQL schema differs from its fixed columns, indexes, foreign key, or checks.
func VerifyAdminActionOutboxSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrAdminActionOutboxSchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrAdminActionOutboxSchema
	}
	if !verifyAdminActionOutboxTable(ctx, db, currentSchema, adminActionOutboxContract()) {
		return ErrAdminActionOutboxSchema
	}
	return nil
}

func verifyAdminActionOutboxTable(ctx context.Context, db *gorm.DB, currentSchema string, contract adminActionOutboxTableContract) bool {
	var actual adminActionOutboxTableMetadata
	tableQuery := `SELECT t.engine, c.character_set_name, t.table_collation FROM information_schema.tables t JOIN information_schema.collation_character_set_applicability c ON c.collation_name=t.table_collation WHERE t.table_schema=DATABASE() AND t.table_name=?`
	if err := db.WithContext(ctx).Raw(tableQuery, contract.name).Row().Scan(&actual.engine, &actual.characterSet, &actual.collation); err != nil {
		return false
	}
	columnQuery := `SELECT column_name, column_type, is_nullable, column_default, extra, COALESCE(character_set_name, ''), COALESCE(collation_name, '') FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`
	columnRows, err := db.WithContext(ctx).Raw(columnQuery, contract.name).Rows()
	if err != nil {
		return false
	}
	for columnRows.Next() {
		var row adminActionOutboxColumnMetadata
		if err := columnRows.Scan(&row.name, &row.columnType, &row.nullable, &row.defaultVal, &row.extra, &row.characterSet, &row.collation); err != nil {
			_ = columnRows.Close()
			return false
		}
		actual.columns = append(actual.columns, row)
	}
	if err := columnRows.Err(); err != nil {
		_ = columnRows.Close()
		return false
	}
	if err := columnRows.Close(); err != nil {
		return false
	}

	indexRows, err := db.WithContext(ctx).Raw(`SELECT index_name, column_name, seq_in_index, non_unique FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? ORDER BY index_name, seq_in_index`, contract.name).Rows()
	if err != nil {
		return false
	}
	for indexRows.Next() {
		var row adminActionOutboxIndexMetadata
		if err := indexRows.Scan(&row.name, &row.column, &row.sequence, &row.nonUnique); err != nil {
			_ = indexRows.Close()
			return false
		}
		actual.indexes = append(actual.indexes, row)
	}
	if err := indexRows.Err(); err != nil {
		_ = indexRows.Close()
		return false
	}
	if err := indexRows.Close(); err != nil {
		return false
	}

	foreignKeyQuery := `SELECT k.constraint_name, k.column_name, k.ordinal_position, k.referenced_table_schema, k.referenced_table_name, k.referenced_column_name, r.delete_rule, r.update_rule FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.table_name=k.table_name AND r.constraint_name=k.constraint_name WHERE k.table_schema=DATABASE() AND k.table_name=? AND k.referenced_table_name IS NOT NULL ORDER BY k.constraint_name, k.ordinal_position`
	foreignKeyRows, err := db.WithContext(ctx).Raw(foreignKeyQuery, contract.name).Rows()
	if err != nil {
		return false
	}
	for foreignKeyRows.Next() {
		var row adminActionOutboxForeignKeyMetadata
		if err := foreignKeyRows.Scan(&row.name, &row.column, &row.ordinal, &row.targetSchema, &row.targetTable, &row.targetColumn, &row.deleteRule, &row.updateRule); err != nil {
			_ = foreignKeyRows.Close()
			return false
		}
		actual.foreignKeys = append(actual.foreignKeys, row)
	}
	if err := foreignKeyRows.Err(); err != nil {
		_ = foreignKeyRows.Close()
		return false
	}
	if err := foreignKeyRows.Close(); err != nil {
		return false
	}

	checkQuery := `SELECT tc.constraint_name, cc.check_clause, tc.enforced FROM information_schema.table_constraints tc JOIN information_schema.check_constraints cc ON cc.constraint_schema=tc.constraint_schema AND cc.constraint_name=tc.constraint_name WHERE tc.table_schema=DATABASE() AND tc.table_name=? AND tc.constraint_type='CHECK' ORDER BY tc.constraint_name`
	checkRows, err := db.WithContext(ctx).Raw(checkQuery, contract.name).Rows()
	if err != nil {
		return false
	}
	for checkRows.Next() {
		var row adminActionOutboxCheckMetadata
		if err := checkRows.Scan(&row.name, &row.clause, &row.enforced); err != nil {
			_ = checkRows.Close()
			return false
		}
		actual.checks = append(actual.checks, row)
	}
	if err := checkRows.Err(); err != nil {
		_ = checkRows.Close()
		return false
	}
	if err := checkRows.Close(); err != nil {
		return false
	}
	return matchesAdminActionOutboxTableContract(contract, actual, currentSchema)
}

func matchesAdminActionOutboxTableContract(want adminActionOutboxTableContract, got adminActionOutboxTableMetadata, currentSchema string) bool {
	if got.engine != "InnoDB" || got.characterSet != "utf8mb4" || got.collation != "utf8mb4_unicode_ci" || len(got.columns) != len(want.columns) {
		return false
	}
	for i, expected := range want.columns {
		actual := got.columns[i]
		columnType := strings.ToLower(actual.columnType)
		if actual.name != expected.name || columnType != expected.columnType || actual.nullable != expected.nullable || actual.defaultVal != expected.defaultVal || strings.ToLower(actual.extra) != expected.extra || actual.characterSet != expected.characterSet || actual.collation != expected.collation || strings.Contains(columnType, "unsigned") {
			return false
		}
	}
	indexes := make(map[string][]adminActionOutboxIndexMetadata)
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
	foreignKeys := make(map[string][]adminActionOutboxForeignKeyMetadata)
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
	checks := make(map[string][]adminActionOutboxCheckMetadata)
	for _, check := range got.checks {
		checks[check.name] = append(checks[check.name], check)
	}
	if len(checks) != len(want.checks) {
		return false
	}
	for _, expected := range want.checks {
		rows, exists := checks[expected.name]
		if !exists || len(rows) != 1 || rows[0].enforced != expected.enforced || expected.enforced != "YES" {
			return false
		}
		wantClause, wantOK := canonicalizeCheckClause(expected.clause)
		actualClause, actualOK := canonicalizeCheckClause(rows[0].clause)
		if !wantOK || !actualOK || actualClause != wantClause {
			return false
		}
	}
	return true
}
