package migration

import (
	"context"
	"database/sql"
	"errors"
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

type adminOperationCheckContract struct {
	name     string
	clause   string
	enforced string
}

type adminOperationTableContract struct {
	name        string
	columns     []adminOperationColumnContract
	indexes     []adminOperationIndexContract
	foreignKeys []adminOperationForeignKeyContract
	checks      []adminOperationCheckContract
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

type adminOperationCheckMetadata struct {
	name     string
	clause   string
	enforced string
}

type adminOperationTableMetadata struct {
	engine      string
	collation   string
	columns     []adminOperationColumnMetadata
	indexes     []adminOperationIndexMetadata
	foreignKeys []adminOperationForeignKeyMetadata
	checks      []adminOperationCheckMetadata
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
		nullableColumn("result_auth_version", "int"), nullableColumn("result_http_status", "int"), requiredColumn("created_at", "bigint"),
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
				// Migration 0005 explicitly declares the fixed session support index.
				{"fk_admin_action_verifications_session", []string{"session_id"}, false},
			},
			foreignKeys: []adminOperationForeignKeyContract{
				{"fk_admin_action_verifications_actor", "actor_user_id", "users", "id"},
				{"fk_admin_action_verifications_session", "session_id", "user_sessions", "id"},
			},
			checks: []adminOperationCheckContract{
				{"chk_admin_action_verifications_audit", "actor_auth_version >= 0 AND created_at >= 0 AND updated_at >= 0 AND is_deleted IN (0, 1) AND (consumed_at IS NULL OR is_deleted = 1)", "YES"},
				{"chk_admin_action_verifications_target", "(target_kind = 1 AND target_guid IS NULL) OR (target_kind IN (2, 3) AND target_guid IS NOT NULL)", "YES"},
				{"chk_admin_action_verifications_times", "expires_at >= 0 AND (consumed_at IS NULL OR consumed_at >= 0)", "YES"},
			},
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
				// Migration 0005 explicitly declares the fixed session support index.
				{"fk_admin_operations_session", []string{"session_id"}, false},
			},
			foreignKeys: []adminOperationForeignKeyContract{
				{"fk_admin_operations_actor", "actor_user_id", "users", "id"},
				{"fk_admin_operations_session", "session_id", "user_sessions", "id"},
				{"fk_admin_operations_verification", "verification_id", "admin_action_verifications", "id"},
			},
			checks: []adminOperationCheckContract{
				{"chk_admin_operations_audit", "actor_auth_version >= 0 AND created_at >= 0 AND updated_at >= 0 AND ((state = 5 AND is_deleted = 1) OR (state IN (1, 2, 3, 4) AND is_deleted = 0))", "YES"},
				{"chk_admin_operations_failure", "error_code IS NULL OR error_code BETWEEN 1 AND 999", "YES"},
				{"chk_admin_operations_http_status", "result_http_status IS NULL OR result_http_status BETWEEN 100 AND 599", "YES"},
				{"chk_admin_operations_result_auth_version", "(state = 2 AND action = 2 AND result_kind = 2 AND result_guid IS NOT NULL AND result_auth_version IS NOT NULL AND result_auth_version > 0) OR ((state IN (1, 3, 4, 5) OR (state = 2 AND (action < 2 OR action > 2))) AND result_auth_version IS NULL)", "YES"},
				{"chk_admin_operations_lease", "(lease_owner_hmac IS NULL AND lease_expires_at IS NULL) OR (lease_owner_hmac IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_expires_at >= 0)", "YES"},
				{"chk_admin_operations_result", "(result_kind IS NULL AND result_guid IS NULL) OR (result_kind = 1 AND result_guid IS NULL) OR (result_kind IN (2, 3) AND result_guid IS NOT NULL)", "YES"},
				{"chk_admin_operations_state", "state IN (1, 2, 3, 4, 5)", "YES"},
				{"chk_admin_operations_terminal", "(state = 1 AND lease_owner_hmac IS NOT NULL AND finished_at IS NULL AND error_code IS NULL AND result_kind IS NULL AND result_http_status IS NULL) OR (state = 2 AND lease_owner_hmac IS NULL AND finished_at IS NOT NULL AND error_code IS NULL AND result_kind IS NOT NULL AND result_http_status IS NOT NULL) OR (state = 3 AND lease_owner_hmac IS NULL AND finished_at IS NOT NULL AND error_code IS NOT NULL AND result_kind IS NULL AND result_http_status IS NOT NULL) OR (state = 4 AND lease_owner_hmac IS NULL AND finished_at IS NULL AND error_code IS NULL AND result_kind IS NULL AND result_http_status IS NULL) OR (state = 5 AND lease_owner_hmac IS NULL AND error_code IS NULL AND result_kind IS NULL AND result_http_status IS NULL)", "YES"},
				{"chk_admin_operations_times", "query_expires_at >= 0 AND (finished_at IS NULL OR finished_at >= 0)", "YES"},
			},
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
	var actual adminOperationTableMetadata
	if err := db.WithContext(ctx).Raw(`SELECT engine, table_collation FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, contract.name).Row().Scan(&actual.engine, &actual.collation); err != nil {
		return false
	}
	columnRows, err := db.WithContext(ctx).Raw(`SELECT column_name, column_type, is_nullable, column_default, extra FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`, contract.name).Rows()
	if err != nil {
		return false
	}
	for columnRows.Next() {
		var row adminOperationColumnMetadata
		if err := columnRows.Scan(&row.name, &row.columnType, &row.nullable, &row.defaultVal, &row.extra); err != nil {
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
		var row adminOperationIndexMetadata
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
	query := `SELECT k.constraint_name, k.column_name, k.ordinal_position, k.referenced_table_schema, k.referenced_table_name, k.referenced_column_name, r.delete_rule, r.update_rule FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.table_name=k.table_name AND r.constraint_name=k.constraint_name WHERE k.table_schema=DATABASE() AND k.table_name=? AND k.referenced_table_name IS NOT NULL ORDER BY k.constraint_name, k.ordinal_position`
	foreignKeyRows, err := db.WithContext(ctx).Raw(query, contract.name).Rows()
	if err != nil {
		return false
	}
	for foreignKeyRows.Next() {
		var row adminOperationForeignKeyMetadata
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
		var row adminOperationCheckMetadata
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
	checks := make(map[string][]adminOperationCheckMetadata)
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

// canonicalizeCheckClause parses the deliberately small expression language
// used by migration 0005. Parsing an AST permits MySQL's case, backtick,
// whitespace, and redundant-parenthesis formatting while rejecting functions,
// strings, arithmetic, reordered predicates, and any unrecognised syntax.
func canonicalizeCheckClause(clause string) (string, bool) {
	tokens, ok := tokenizeCheckClause(clause)
	if !ok || len(tokens) == 0 {
		return "", false
	}
	parser := checkClauseParser{tokens: tokens}
	result, ok := parser.parseOr()
	return result, ok && parser.pos == len(tokens)
}

func tokenizeCheckClause(clause string) ([]string, bool) {
	var tokens []string
	for pos := 0; pos < len(clause); {
		if isCheckSpace(clause[pos]) {
			pos++
			continue
		}
		if clause[pos] == '`' {
			end := pos + 1
			for end < len(clause) && clause[end] != '`' {
				end++
			}
			if end == len(clause) || !isCheckIdentifier(clause[pos+1:end]) {
				return nil, false
			}
			tokens = append(tokens, strings.ToLower(clause[pos+1:end]))
			pos = end + 1
			continue
		}
		if isASCIIAlpha(clause[pos]) || clause[pos] == '_' {
			end := pos + 1
			for end < len(clause) && (isASCIIAlpha(clause[end]) || isASCIIDigit(clause[end]) || clause[end] == '_') {
				end++
			}
			tokens = append(tokens, strings.ToLower(clause[pos:end]))
			pos = end
			continue
		}
		if isASCIIDigit(clause[pos]) {
			end := pos + 1
			for end < len(clause) && isASCIIDigit(clause[end]) {
				end++
			}
			tokens = append(tokens, clause[pos:end])
			pos = end
			continue
		}
		if strings.ContainsRune("(),", rune(clause[pos])) {
			tokens = append(tokens, clause[pos:pos+1])
			pos++
			continue
		}
		if strings.ContainsRune("=><", rune(clause[pos])) {
			end := pos + 1
			if end < len(clause) && clause[end] == '=' {
				end++
			}
			op := clause[pos:end]
			if op != "=" && op != ">=" && op != "<=" && op != ">" && op != "<" {
				return nil, false
			}
			tokens = append(tokens, op)
			pos = end
			continue
		}
		return nil, false
	}
	return tokens, true
}

type checkClauseParser struct {
	tokens []string
	pos    int
}

func (p *checkClauseParser) parseOr() (string, bool) {
	left, ok := p.parseAnd()
	if !ok {
		return "", false
	}
	for p.match("or") {
		right, ok := p.parseAnd()
		if !ok {
			return "", false
		}
		left = "or(" + left + "," + right + ")"
	}
	return left, true
}

func (p *checkClauseParser) parseAnd() (string, bool) {
	left, ok := p.parsePrimary()
	if !ok {
		return "", false
	}
	for p.match("and") {
		right, ok := p.parsePrimary()
		if !ok {
			return "", false
		}
		left = "and(" + left + "," + right + ")"
	}
	return left, true
}

func (p *checkClauseParser) parsePrimary() (string, bool) {
	if p.match("(") {
		expression, ok := p.parseOr()
		if !ok || !p.match(")") {
			return "", false
		}
		return expression, true
	}
	if p.pos >= len(p.tokens) || !isCheckIdentifier(p.tokens[p.pos]) {
		return "", false
	}
	identifier := p.tokens[p.pos]
	p.pos++
	if p.match("is") {
		negated := p.match("not")
		if !p.match("null") {
			return "", false
		}
		if negated {
			return "notnull(" + identifier + ")", true
		}
		return "isnull(" + identifier + ")", true
	}
	if p.match("in") {
		if !p.match("(") {
			return "", false
		}
		var values []string
		for {
			value, ok := p.parseNumber()
			if !ok {
				return "", false
			}
			values = append(values, value)
			if !p.match(",") {
				break
			}
		}
		if !p.match(")") {
			return "", false
		}
		return "in(" + identifier + "," + strings.Join(values, ",") + ")", true
	}
	if p.match("between") {
		low, lowOK := p.parseNumber()
		if !lowOK || !p.match("and") {
			return "", false
		}
		high, highOK := p.parseNumber()
		if !highOK {
			return "", false
		}
		return "between(" + identifier + "," + low + "," + high + ")", true
	}
	if p.pos >= len(p.tokens) || !isCheckComparison(p.tokens[p.pos]) {
		return "", false
	}
	operator := p.tokens[p.pos]
	p.pos++
	value, ok := p.parseNumber()
	if !ok {
		return "", false
	}
	return "compare(" + operator + "," + identifier + "," + value + ")", true
}

func (p *checkClauseParser) parseNumber() (string, bool) {
	parentheses := 0
	for p.match("(") {
		parentheses++
	}
	if p.pos >= len(p.tokens) || !isCheckNumber(p.tokens[p.pos]) {
		return "", false
	}
	value := strings.TrimLeft(p.tokens[p.pos], "0")
	if value == "" {
		value = "0"
	}
	p.pos++
	for ; parentheses > 0; parentheses-- {
		if !p.match(")") {
			return "", false
		}
	}
	return value, true
}

func (p *checkClauseParser) match(token string) bool {
	if p.pos >= len(p.tokens) || p.tokens[p.pos] != token {
		return false
	}
	p.pos++
	return true
}

func isCheckIdentifier(value string) bool {
	if value == "" || (!isASCIIAlpha(value[0]) && value[0] != '_') {
		return false
	}
	for i := 1; i < len(value); i++ {
		if !isASCIIAlpha(value[i]) && !isASCIIDigit(value[i]) && value[i] != '_' {
			return false
		}
	}
	return true
}

func isCheckNumber(value string) bool {
	if value == "" {
		return false
	}
	for i := range value {
		if !isASCIIDigit(value[i]) {
			return false
		}
	}
	return true
}

func isCheckComparison(value string) bool {
	return value == "=" || value == ">=" || value == "<=" || value == ">" || value == "<"
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isCheckSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\f'
}

func restrictRule(rule string) bool {
	return rule == "RESTRICT" || rule == "NO ACTION"
}
