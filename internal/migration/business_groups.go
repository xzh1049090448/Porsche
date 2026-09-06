package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const businessGroupSeedMarker = "-- porsche:seed-default-business-group"

var ErrBusinessGroupsSchema = errors.New("business groups schema mismatch or unavailable")

type businessGroupColumnContract struct {
	name         string
	columnType   string
	nullable     string
	defaultVal   sql.NullString
	extra        string
	characterSet string
	collation    string
}

type businessGroupIndexContract struct {
	name    string
	columns []string
	unique  bool
}

type businessGroupCheckContract struct {
	name     string
	clause   string
	enforced string
}

type businessGroupTableContract struct {
	name    string
	columns []businessGroupColumnContract
	indexes []businessGroupIndexContract
	checks  []businessGroupCheckContract
}

type businessGroupColumnMetadata struct {
	name         string
	columnType   string
	nullable     string
	defaultVal   sql.NullString
	extra        string
	characterSet string
	collation    string
}

type businessGroupIndexMetadata struct {
	name       string
	column     string
	sequence   int
	nonUnique  int
	subPart    sql.NullInt64
	collation  sql.NullString
	indexType  string
	visible    string
	expression sql.NullString
}

type businessGroupForeignKeyMetadata struct {
	name         string
	column       string
	ordinal      int
	targetSchema string
	targetTable  string
	targetColumn string
	deleteRule   string
	updateRule   string
}

type businessGroupCheckMetadata struct {
	name     string
	clause   string
	enforced string
}

type businessGroupDefaultRow struct {
	id        int64
	guid      int64
	key       string
	status    int
	isDeleted int
}

type businessGroupTableMetadata struct {
	engine       string
	characterSet string
	collation    string
	columns      []businessGroupColumnMetadata
	indexes      []businessGroupIndexMetadata
	foreignKeys  []businessGroupForeignKeyMetadata
	checks       []businessGroupCheckMetadata
}

type businessGroupUserRelationContract struct {
	column     businessGroupColumnMetadata
	index      businessGroupIndexContract
	foreignKey businessGroupForeignKeyMetadata
}

type businessGroupUserRelationMetadata struct {
	columns     []businessGroupColumnMetadata
	indexes     []businessGroupIndexMetadata
	foreignKeys []businessGroupForeignKeyMetadata
}

func businessGroupTableContractDefinition() businessGroupTableContract {
	columns := []businessGroupColumnContract{
		{name: "id", columnType: "bigint", nullable: "NO", extra: "auto_increment"},
		{name: "guid", columnType: "bigint", nullable: "NO"},
		{name: "group_key", columnType: "varchar(64)", nullable: "NO", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"},
		{name: "display_name", columnType: "varchar(64)", nullable: "NO", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"},
		{name: "status", columnType: "int", nullable: "NO"},
		{name: "created_at", columnType: "bigint", nullable: "NO"},
		{name: "created_by", columnType: "bigint", nullable: "YES"},
		{name: "updated_at", columnType: "bigint", nullable: "NO"},
		{name: "updated_by", columnType: "bigint", nullable: "YES"},
		{name: "is_deleted", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "0", Valid: true}},
	}
	return businessGroupTableContract{
		name:    "business_groups",
		columns: columns,
		indexes: []businessGroupIndexContract{
			{name: "PRIMARY", columns: []string{"id"}, unique: true},
			{name: "uk_business_groups_guid", columns: []string{"guid"}, unique: true},
			{name: "uk_business_groups_key_deleted", columns: []string{"group_key", "is_deleted"}, unique: true},
			{name: "idx_business_groups_active", columns: []string{"status", "is_deleted", "group_key"}, unique: false},
		},
		checks: []businessGroupCheckContract{
			{name: "chk_business_groups_audit", clause: "created_at >= 0 AND updated_at >= 0 AND is_deleted IN (0, 1)", enforced: "YES"},
			{name: "chk_business_groups_status", clause: "status IN (1, 2)", enforced: "YES"},
		},
	}
}

func businessGroupUserRelationContractDefinition() businessGroupUserRelationContract {
	return businessGroupUserRelationContract{
		column: businessGroupColumnMetadata{name: "group_id", columnType: "bigint", nullable: "NO"},
		index:  businessGroupIndexContract{name: "idx_users_group_id", columns: []string{"group_id"}, unique: false},
		foreignKey: businessGroupForeignKeyMetadata{
			name: "fk_users_business_group", column: "group_id", ordinal: 1,
			targetTable: "business_groups", targetColumn: "id",
		},
	}
}

// VerifyBusinessGroupsSchema validates both the table contract and the
// completed users.group_id relation, then rejects missing/default duplicates,
// nullable user associations, and orphaned rows.
func VerifyBusinessGroupsSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrBusinessGroupsSchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrBusinessGroupsSchema
	}
	tableMetadata, ok := loadBusinessGroupTableMetadata(ctx, db, businessGroupTableContractDefinition().name)
	if !ok || !matchesBusinessGroupTableContract(businessGroupTableContractDefinition(), tableMetadata) {
		return ErrBusinessGroupsSchema
	}
	relationMetadata, ok := loadBusinessGroupUserRelationMetadata(ctx, db)
	if !ok || !matchesBusinessGroupUserRelationContract(businessGroupUserRelationContractDefinition(), relationMetadata, currentSchema, false) {
		return ErrBusinessGroupsSchema
	}
	var defaultRows []struct {
		ID        int64  `gorm:"column:id"`
		GUID      int64  `gorm:"column:guid"`
		Key       string `gorm:"column:group_key"`
		Status    int    `gorm:"column:status"`
		IsDeleted int    `gorm:"column:is_deleted"`
	}
	if err := db.WithContext(ctx).Raw("SELECT id,guid,group_key,status,is_deleted FROM business_groups WHERE group_key='default' AND is_deleted=0 ORDER BY id ASC").Scan(&defaultRows).Error; err != nil {
		return ErrBusinessGroupsSchema
	}
	groups := make([]businessGroupDefaultRow, 0, len(defaultRows))
	for _, row := range defaultRows {
		groups = append(groups, businessGroupDefaultRow{id: row.ID, guid: row.GUID, key: row.Key, status: row.Status, isDeleted: row.IsDeleted})
	}
	var nullableUsers, orphanedUsers int64
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM users WHERE group_id IS NULL").Row().Scan(&nullableUsers); err != nil {
		return ErrBusinessGroupsSchema
	}
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM users u LEFT JOIN business_groups g ON g.id=u.group_id WHERE g.id IS NULL").Row().Scan(&orphanedUsers); err != nil {
		return ErrBusinessGroupsSchema
	}
	if !validBusinessGroupState(groups, nullableUsers, orphanedUsers) {
		return ErrBusinessGroupsSchema
	}
	return nil
}

func validBusinessGroupState(groups []businessGroupDefaultRow, nullableUsers, orphanedUsers int64) bool {
	if len(groups) != 1 || nullableUsers != 0 || orphanedUsers != 0 {
		return false
	}
	group := groups[0]
	return group.id > 0 && group.guid > 0 && group.key == "default" && group.status == 1 && group.isDeleted == 0
}

func matchesBusinessGroupTableContract(want businessGroupTableContract, got businessGroupTableMetadata) bool {
	if got.engine != "InnoDB" || got.characterSet != "utf8mb4" || got.collation != "utf8mb4_unicode_ci" || len(got.columns) != len(want.columns) || len(got.foreignKeys) != 0 {
		return false
	}
	for i, expected := range want.columns {
		actual := got.columns[i]
		columnType := strings.ToLower(actual.columnType)
		if actual.name != expected.name || columnType != expected.columnType || actual.nullable != expected.nullable || actual.defaultVal != expected.defaultVal || strings.ToLower(actual.extra) != expected.extra || actual.characterSet != expected.characterSet || actual.collation != expected.collation || strings.Contains(columnType, "unsigned") {
			return false
		}
	}
	indexes := make(map[string][]businessGroupIndexMetadata)
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
			if row.sequence != i+1 || row.column != expected.columns[i] || (row.nonUnique == 0) != expected.unique || !validRequiredBusinessGroupIndexMetadata(row) {
				return false
			}
		}
	}
	checks := make(map[string][]businessGroupCheckMetadata)
	for _, check := range got.checks {
		checks[check.name] = append(checks[check.name], check)
	}
	if len(checks) != len(want.checks) {
		return false
	}
	for _, expected := range want.checks {
		rows, exists := checks[expected.name]
		if !exists || len(rows) != 1 || rows[0].enforced != "YES" || expected.enforced != "YES" {
			return false
		}
		wantClause, wantOK := canonicalizeCheckClause(expected.clause)
		gotClause, gotOK := canonicalizeCheckClause(rows[0].clause)
		if !wantOK || !gotOK || wantClause != gotClause {
			return false
		}
	}
	return true
}

func matchesBusinessGroupUserRelationContract(want businessGroupUserRelationContract, got businessGroupUserRelationMetadata, currentSchema string, allowNullable bool) bool {
	if len(got.columns) != 1 || !matchesBusinessGroupColumn(want.column, got.columns[0], allowNullable) {
		return false
	}
	var requiredIndexes []businessGroupIndexMetadata
	for _, index := range got.indexes {
		if index.name == want.index.name {
			requiredIndexes = append(requiredIndexes, index)
		}
	}
	if len(requiredIndexes) != 1 {
		return false
	}
	index := requiredIndexes[0]
	if index.column != want.index.columns[0] || index.sequence != 1 || index.nonUnique != 1 || !validRequiredBusinessGroupIndexMetadata(index) {
		return false
	}
	if len(got.foreignKeys) != 1 {
		return false
	}
	foreignKey := got.foreignKeys[0]
	return foreignKey.name == want.foreignKey.name && foreignKey.column == want.foreignKey.column && foreignKey.ordinal == 1 &&
		foreignKey.targetSchema == currentSchema && foreignKey.targetTable == want.foreignKey.targetTable && foreignKey.targetColumn == want.foreignKey.targetColumn &&
		restrictRule(foreignKey.deleteRule) && restrictRule(foreignKey.updateRule)
}

func validRequiredBusinessGroupIndexMetadata(index businessGroupIndexMetadata) bool {
	return !index.subPart.Valid && index.collation.Valid && index.collation.String == "A" && strings.EqualFold(index.indexType, "BTREE") && index.visible == "YES" && !index.expression.Valid
}

func businessGroupIndexMetadataProjection(hasVisibility, hasExpression bool) string {
	visibility := "'YES' AS is_visible"
	if hasVisibility {
		visibility = "is_visible"
	}
	expression := "NULL AS expression"
	if hasExpression {
		expression = "expression"
	}
	return "index_name, column_name, seq_in_index, non_unique, sub_part, collation, index_type, " + visibility + ", " + expression
}

func businessGroupIndexMetadataCapabilities(ctx context.Context, db *gorm.DB) (bool, bool, bool) {
	rows, err := db.WithContext(ctx).Raw(`SELECT column_name FROM information_schema.columns WHERE table_schema='information_schema' AND UPPER(table_name)='STATISTICS' AND UPPER(column_name) IN ('IS_VISIBLE','EXPRESSION')`).Rows()
	if err != nil {
		return false, false, false
	}
	var hasVisibility, hasExpression bool
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			_ = rows.Close()
			return false, false, false
		}
		switch {
		case strings.EqualFold(column, "IS_VISIBLE"):
			hasVisibility = true
		case strings.EqualFold(column, "EXPRESSION"):
			hasExpression = true
		}
	}
	if rows.Err() != nil || rows.Close() != nil {
		return false, false, false
	}
	return hasVisibility, hasExpression, true
}

func matchesBusinessGroupColumn(want, got businessGroupColumnMetadata, allowNullable bool) bool {
	nullable := got.nullable == want.nullable
	if allowNullable {
		nullable = got.nullable == "YES" || got.nullable == "NO"
	}
	columnType := strings.ToLower(got.columnType)
	return got.name == want.name && columnType == want.columnType && !strings.Contains(columnType, "unsigned") && nullable &&
		got.defaultVal == want.defaultVal && strings.ToLower(got.extra) == want.extra && got.characterSet == want.characterSet && got.collation == want.collation
}

func loadBusinessGroupTableMetadata(ctx context.Context, db *gorm.DB, table string) (businessGroupTableMetadata, bool) {
	var actual businessGroupTableMetadata
	tableQuery := `SELECT t.engine, c.character_set_name, t.table_collation FROM information_schema.tables t JOIN information_schema.collation_character_set_applicability c ON c.collation_name=t.table_collation WHERE t.table_schema=DATABASE() AND t.table_name=?`
	if err := db.WithContext(ctx).Raw(tableQuery, table).Row().Scan(&actual.engine, &actual.characterSet, &actual.collation); err != nil {
		return actual, false
	}
	columnQuery := `SELECT column_name, column_type, is_nullable, column_default, extra, COALESCE(character_set_name, ''), COALESCE(collation_name, '') FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`
	rows, err := db.WithContext(ctx).Raw(columnQuery, table).Rows()
	if err != nil {
		return actual, false
	}
	for rows.Next() {
		var row businessGroupColumnMetadata
		if err := rows.Scan(&row.name, &row.columnType, &row.nullable, &row.defaultVal, &row.extra, &row.characterSet, &row.collation); err != nil {
			_ = rows.Close()
			return actual, false
		}
		actual.columns = append(actual.columns, row)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return actual, false
	}
	hasVisibility, hasExpression, ok := businessGroupIndexMetadataCapabilities(ctx, db)
	if !ok {
		return actual, false
	}
	indexQuery := "SELECT " + businessGroupIndexMetadataProjection(hasVisibility, hasExpression) + " FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? ORDER BY index_name, seq_in_index"
	indexRows, err := db.WithContext(ctx).Raw(indexQuery, table).Rows()
	if err != nil {
		return actual, false
	}
	for indexRows.Next() {
		var row businessGroupIndexMetadata
		if err := indexRows.Scan(&row.name, &row.column, &row.sequence, &row.nonUnique, &row.subPart, &row.collation, &row.indexType, &row.visible, &row.expression); err != nil {
			_ = indexRows.Close()
			return actual, false
		}
		actual.indexes = append(actual.indexes, row)
	}
	if indexRows.Err() != nil || indexRows.Close() != nil {
		return actual, false
	}
	foreignKeyRows, err := db.WithContext(ctx).Raw(`SELECT k.constraint_name, k.column_name, k.ordinal_position, k.referenced_table_schema, k.referenced_table_name, k.referenced_column_name, r.delete_rule, r.update_rule FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.table_name=k.table_name AND r.constraint_name=k.constraint_name WHERE k.table_schema=DATABASE() AND k.table_name=? AND k.referenced_table_name IS NOT NULL ORDER BY k.constraint_name, k.ordinal_position`, table).Rows()
	if err != nil {
		return actual, false
	}
	for foreignKeyRows.Next() {
		var row businessGroupForeignKeyMetadata
		if err := foreignKeyRows.Scan(&row.name, &row.column, &row.ordinal, &row.targetSchema, &row.targetTable, &row.targetColumn, &row.deleteRule, &row.updateRule); err != nil {
			_ = foreignKeyRows.Close()
			return actual, false
		}
		actual.foreignKeys = append(actual.foreignKeys, row)
	}
	if foreignKeyRows.Err() != nil || foreignKeyRows.Close() != nil {
		return actual, false
	}
	checkRows, err := db.WithContext(ctx).Raw(`SELECT tc.constraint_name, cc.check_clause, tc.enforced FROM information_schema.table_constraints tc JOIN information_schema.check_constraints cc ON cc.constraint_schema=tc.constraint_schema AND cc.constraint_name=tc.constraint_name WHERE tc.table_schema=DATABASE() AND tc.table_name=? AND tc.constraint_type='CHECK' ORDER BY tc.constraint_name`, table).Rows()
	if err != nil {
		return actual, false
	}
	for checkRows.Next() {
		var row businessGroupCheckMetadata
		if err := checkRows.Scan(&row.name, &row.clause, &row.enforced); err != nil {
			_ = checkRows.Close()
			return actual, false
		}
		actual.checks = append(actual.checks, row)
	}
	if checkRows.Err() != nil || checkRows.Close() != nil {
		return actual, false
	}
	return actual, true
}

func loadBusinessGroupUserRelationMetadata(ctx context.Context, db *gorm.DB) (businessGroupUserRelationMetadata, bool) {
	var actual businessGroupUserRelationMetadata
	columnRows, err := db.WithContext(ctx).Raw(`SELECT column_name, column_type, is_nullable, column_default, extra, COALESCE(character_set_name, ''), COALESCE(collation_name, '') FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='users' AND column_name='group_id' ORDER BY ordinal_position`).Rows()
	if err != nil {
		return actual, false
	}
	for columnRows.Next() {
		var row businessGroupColumnMetadata
		if err := columnRows.Scan(&row.name, &row.columnType, &row.nullable, &row.defaultVal, &row.extra, &row.characterSet, &row.collation); err != nil {
			_ = columnRows.Close()
			return actual, false
		}
		actual.columns = append(actual.columns, row)
	}
	if columnRows.Err() != nil || columnRows.Close() != nil {
		return actual, false
	}
	hasVisibility, hasExpression, ok := businessGroupIndexMetadataCapabilities(ctx, db)
	if !ok {
		return actual, false
	}
	indexQuery := "SELECT " + businessGroupIndexMetadataProjection(hasVisibility, hasExpression) + " FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='users' AND index_name='idx_users_group_id' ORDER BY seq_in_index"
	indexRows, err := db.WithContext(ctx).Raw(indexQuery).Rows()
	if err != nil {
		return actual, false
	}
	for indexRows.Next() {
		var row businessGroupIndexMetadata
		if err := indexRows.Scan(&row.name, &row.column, &row.sequence, &row.nonUnique, &row.subPart, &row.collation, &row.indexType, &row.visible, &row.expression); err != nil {
			_ = indexRows.Close()
			return actual, false
		}
		actual.indexes = append(actual.indexes, row)
	}
	if indexRows.Err() != nil || indexRows.Close() != nil {
		return actual, false
	}
	foreignKeyQuery := `SELECT k.constraint_name, k.column_name, k.ordinal_position, k.referenced_table_schema, k.referenced_table_name, k.referenced_column_name, r.delete_rule, r.update_rule FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.table_name=k.table_name AND r.constraint_name=k.constraint_name WHERE k.table_schema=DATABASE() AND k.table_name='users' AND k.constraint_name IN (SELECT kg.constraint_name FROM information_schema.key_column_usage kg WHERE kg.table_schema=DATABASE() AND kg.table_name='users' AND kg.column_name='group_id' AND kg.referenced_table_name IS NOT NULL) ORDER BY k.constraint_name, k.ordinal_position`
	foreignKeyRows, err := db.WithContext(ctx).Raw(foreignKeyQuery).Rows()
	if err != nil {
		return actual, false
	}
	for foreignKeyRows.Next() {
		var row businessGroupForeignKeyMetadata
		if err := foreignKeyRows.Scan(&row.name, &row.column, &row.ordinal, &row.targetSchema, &row.targetTable, &row.targetColumn, &row.deleteRule, &row.updateRule); err != nil {
			_ = foreignKeyRows.Close()
			return actual, false
		}
		actual.foreignKeys = append(actual.foreignKeys, row)
	}
	if foreignKeyRows.Err() != nil || foreignKeyRows.Close() != nil {
		return actual, false
	}
	return actual, true
}

func splitBusinessGroupsMigration(upSQL []byte) ([]string, []string, error) {
	source := string(upSQL)
	if strings.Count(source, businessGroupSeedMarker) != 1 {
		return nil, nil, fmt.Errorf("business group migration requires exactly one seed marker")
	}
	parts := strings.SplitN(source, businessGroupSeedMarker, 2)
	preSeed := splitStatements(parts[0])
	postSeed := splitStatements(parts[1])
	if len(preSeed) == 0 || len(postSeed) == 0 {
		return nil, nil, fmt.Errorf("business group migration seed marker must separate SQL phases")
	}
	return preSeed, postSeed, nil
}

// applyBusinessGroupsMigration is the only version-specific migration path.
// The caller pins conn to the advisory-lock connection before invoking it.
func applyBusinessGroupsMigration(conn *gorm.DB, upSQL []byte, nextGUID func() int64, nowMillis func() int64) error {
	ctx := conn.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	preSeed, postSeed, err := splitBusinessGroupsMigration(upSQL)
	if err != nil {
		return err
	}
	if len(preSeed) != 2 || len(postSeed) != 4 {
		return fmt.Errorf("business group migration has unexpected SQL phase shape")
	}
	if err := conn.Exec(preSeed[0]).Error; err != nil {
		return fmt.Errorf("create business groups table: %w", err)
	}
	column, exists, err := loadBusinessGroupIDColumn(conn)
	if err != nil {
		return err
	}
	if !exists {
		if err := conn.Exec(preSeed[1]).Error; err != nil {
			return fmt.Errorf("add users group relation: %w", err)
		}
		column, exists, err = loadBusinessGroupIDColumn(conn)
		if err != nil {
			return err
		}
	}
	if !exists || !matchesBusinessGroupColumn(businessGroupUserRelationContractDefinition().column, column, true) {
		return fmt.Errorf("users group relation has incompatible partial schema")
	}
	tableMetadata, ok := loadBusinessGroupTableMetadata(ctx, conn, "business_groups")
	if !ok || !matchesBusinessGroupTableContract(businessGroupTableContractDefinition(), tableMetadata) {
		return fmt.Errorf("business groups table has incompatible partial schema")
	}

	defaultID, err := ensureDefaultBusinessGroup(conn, nextGUID, nowMillis)
	if err != nil {
		return err
	}
	if err := conn.Exec(postSeed[0], defaultID).Error; err != nil {
		return fmt.Errorf("backfill users group relation: %w", err)
	}
	column, exists, err = loadBusinessGroupIDColumn(conn)
	if err != nil || !exists {
		return fmt.Errorf("read users group relation after backfill: %w", err)
	}
	if column.nullable == "YES" {
		if err := conn.Exec(postSeed[1]).Error; err != nil {
			return fmt.Errorf("finalize users group relation: %w", err)
		}
	} else if column.nullable != "NO" {
		return fmt.Errorf("users group relation has invalid nullability")
	}
	if exists, valid, err := namedBusinessGroupUserIndex(conn); err != nil {
		return err
	} else if !exists {
		if err := conn.Exec(postSeed[2]).Error; err != nil {
			return fmt.Errorf("create users group index: %w", err)
		}
	} else if !valid {
		return fmt.Errorf("users group index has incompatible partial schema")
	}
	if exists, valid, err := namedBusinessGroupUserForeignKey(conn); err != nil {
		return err
	} else if !exists {
		if err := conn.Exec(postSeed[3]).Error; err != nil {
			return fmt.Errorf("create users group foreign key: %w", err)
		}
	} else if !valid {
		return fmt.Errorf("users group foreign key has incompatible partial schema")
	}
	return nil
}

func loadBusinessGroupIDColumn(conn *gorm.DB) (businessGroupColumnMetadata, bool, error) {
	var columns []struct {
		Name         string         `gorm:"column:column_name"`
		ColumnType   string         `gorm:"column:column_type"`
		Nullable     string         `gorm:"column:is_nullable"`
		DefaultVal   sql.NullString `gorm:"column:column_default"`
		Extra        string         `gorm:"column:extra"`
		CharacterSet string         `gorm:"column:character_set_name"`
		Collation    string         `gorm:"column:collation_name"`
	}
	query := `SELECT column_name,column_type,is_nullable,column_default,extra,COALESCE(character_set_name, '') AS character_set_name,COALESCE(collation_name, '') AS collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='users' AND column_name='group_id'`
	if err := conn.Raw(query).Scan(&columns).Error; err != nil {
		return businessGroupColumnMetadata{}, false, fmt.Errorf("read users group relation: %w", err)
	}
	if len(columns) == 0 {
		return businessGroupColumnMetadata{}, false, nil
	}
	if len(columns) != 1 {
		return businessGroupColumnMetadata{}, false, fmt.Errorf("users group relation metadata is ambiguous")
	}
	column := columns[0]
	return businessGroupColumnMetadata{
		name: column.Name, columnType: column.ColumnType, nullable: column.Nullable, defaultVal: column.DefaultVal,
		extra: column.Extra, characterSet: column.CharacterSet, collation: column.Collation,
	}, true, nil
}

func ensureDefaultBusinessGroup(conn *gorm.DB, nextGUID func() int64, nowMillis func() int64) (int64, error) {
	var groups []struct {
		ID        int64  `gorm:"column:id"`
		GUID      int64  `gorm:"column:guid"`
		Key       string `gorm:"column:group_key"`
		Status    int    `gorm:"column:status"`
		IsDeleted int    `gorm:"column:is_deleted"`
	}
	if err := conn.Raw("SELECT id,guid,group_key,status,is_deleted FROM business_groups WHERE group_key='default' AND is_deleted=0 ORDER BY id LIMIT 2").Scan(&groups).Error; err != nil {
		return 0, fmt.Errorf("read default business group: %w", err)
	}
	if len(groups) > 1 {
		return 0, fmt.Errorf("multiple live default business groups")
	}
	if len(groups) == 1 {
		group := groups[0]
		if group.ID <= 0 || group.GUID <= 0 || group.Key != "default" || group.Status != 1 || group.IsDeleted != 0 {
			return 0, fmt.Errorf("default business group is corrupt")
		}
		return group.ID, nil
	}
	guid := nextGUID()
	if guid <= 0 {
		return 0, fmt.Errorf("default business group GUID must be positive")
	}
	now := nowMillis()
	result := conn.Exec(`INSERT INTO business_groups (guid,group_key,display_name,status,created_at,created_by,updated_at,updated_by,is_deleted) VALUES (?,'default','Default',1,?,NULL,?,NULL,0)`, guid, now, now)
	if result.Error != nil {
		return 0, fmt.Errorf("seed default business group: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return 0, fmt.Errorf("seed default business group affected unexpected rows")
	}
	var id int64
	if err := conn.Raw("SELECT id FROM business_groups WHERE guid=? AND BINARY group_key=BINARY 'default' AND status=1 AND is_deleted=0", guid).Row().Scan(&id); err != nil {
		return 0, fmt.Errorf("read seeded default business group: %w", err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("seeded default business group has invalid internal ID")
	}
	return id, nil
}

func namedBusinessGroupUserIndex(conn *gorm.DB) (bool, bool, error) {
	var rows []struct {
		Name       string         `gorm:"column:index_name"`
		Column     string         `gorm:"column:column_name"`
		Sequence   int            `gorm:"column:seq_in_index"`
		NonUnique  int            `gorm:"column:non_unique"`
		SubPart    sql.NullInt64  `gorm:"column:sub_part"`
		Collation  sql.NullString `gorm:"column:collation"`
		IndexType  string         `gorm:"column:index_type"`
		Visible    string         `gorm:"column:is_visible"`
		Expression sql.NullString `gorm:"column:expression"`
	}
	ctx := conn.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	hasVisibility, hasExpression, ok := businessGroupIndexMetadataCapabilities(ctx, conn)
	if !ok {
		return false, false, fmt.Errorf("read index metadata capabilities")
	}
	indexQuery := "SELECT " + businessGroupIndexMetadataProjection(hasVisibility, hasExpression) + " FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='users' AND index_name='idx_users_group_id' ORDER BY seq_in_index"
	if err := conn.Raw(indexQuery).Scan(&rows).Error; err != nil {
		return false, false, fmt.Errorf("read users group index: %w", err)
	}
	if len(rows) == 0 {
		return false, false, nil
	}
	if len(rows) != 1 {
		return true, false, nil
	}
	row := rows[0]
	metadata := businessGroupIndexMetadata{name: row.Name, column: row.Column, sequence: row.Sequence, nonUnique: row.NonUnique, subPart: row.SubPart, collation: row.Collation, indexType: row.IndexType, visible: row.Visible, expression: row.Expression}
	return true, row.Column == "group_id" && row.Sequence == 1 && row.NonUnique == 1 && validRequiredBusinessGroupIndexMetadata(metadata), nil
}

func namedBusinessGroupUserForeignKey(conn *gorm.DB) (bool, bool, error) {
	var currentSchema string
	if err := conn.Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil {
		return false, false, fmt.Errorf("read current schema: %w", err)
	}
	if currentSchema == "" {
		return false, false, fmt.Errorf("current schema is empty")
	}
	var rows []struct {
		Column       string `gorm:"column:column_name"`
		Ordinal      int    `gorm:"column:ordinal_position"`
		TargetSchema string `gorm:"column:referenced_table_schema"`
		TargetTable  string `gorm:"column:referenced_table_name"`
		TargetColumn string `gorm:"column:referenced_column_name"`
		DeleteRule   string `gorm:"column:delete_rule"`
		UpdateRule   string `gorm:"column:update_rule"`
	}
	query := `SELECT k.column_name,k.ordinal_position,k.referenced_table_schema,k.referenced_table_name,k.referenced_column_name,r.delete_rule,r.update_rule FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.table_name=k.table_name AND r.constraint_name=k.constraint_name WHERE k.table_schema=DATABASE() AND k.table_name='users' AND k.constraint_name='fk_users_business_group' ORDER BY k.ordinal_position`
	if err := conn.Raw(query).Scan(&rows).Error; err != nil {
		return false, false, fmt.Errorf("read users group foreign key: %w", err)
	}
	if len(rows) == 0 {
		return false, false, nil
	}
	row := rows[0]
	valid := len(rows) == 1 && row.Column == "group_id" && row.Ordinal == 1 && row.TargetSchema == currentSchema && row.TargetTable == "business_groups" && row.TargetColumn == "id" && restrictRule(row.DeleteRule) && restrictRule(row.UpdateRule)
	return true, valid, nil
}
