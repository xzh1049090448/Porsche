package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gorm.io/gorm"
)

var ErrPublicHomeStructuredContentSchema = errors.New("public home structured content schema mismatch or unavailable")

type publicHomeTableContract struct {
	name       string
	columns    []businessGroupColumnMetadata
	indexes    []businessGroupIndexContract
	foreignKey businessGroupForeignKeyMetadata
	check      businessGroupCheckContract
}

func publicHomeStructuredContentContracts() []publicHomeTableContract {
	audit := []businessGroupColumnMetadata{
		{name: "created_at", columnType: "bigint", nullable: "NO"},
		{name: "created_by", columnType: "bigint", nullable: "YES"},
		{name: "updated_at", columnType: "bigint", nullable: "NO"},
		{name: "updated_by", columnType: "bigint", nullable: "YES"},
		{name: "is_deleted", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "0", Valid: true}},
	}
	return []publicHomeTableContract{
		{
			name: "public_home_announcements",
			columns: append([]businessGroupColumnMetadata{
				{name: "id", columnType: "bigint", nullable: "NO", extra: "auto_increment"},
				{name: "guid", columnType: "bigint", nullable: "NO"},
				{name: "content_draft_id", columnType: "bigint", nullable: "NO"},
				{name: "title", columnType: "varchar(120)", nullable: "NO", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"},
				{name: "body_markdown", columnType: "mediumtext", nullable: "NO", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"},
				{name: "effective_at", columnType: "bigint", nullable: "YES"},
				{name: "is_visible", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "1", Valid: true}},
				{name: "sort_order", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "0", Valid: true}},
				{name: "revision", columnType: "bigint", nullable: "NO", defaultVal: sql.NullString{String: "1", Valid: true}},
			}, audit...),
			indexes: []businessGroupIndexContract{
				{name: "PRIMARY", columns: []string{"id"}, unique: true},
				{name: "uk_public_home_announcements_guid", columns: []string{"guid"}, unique: true},
				{name: "idx_public_home_announcements_draft_active_order", columns: []string{"content_draft_id", "is_deleted", "sort_order", "guid"}},
			},
			foreignKey: businessGroupForeignKeyMetadata{name: "fk_public_home_announcements_draft", column: "content_draft_id", ordinal: 1, targetTable: "public_content_drafts", targetColumn: "id"},
			check:      businessGroupCheckContract{name: "chk_public_home_announcements_values", clause: "revision > 0 AND is_visible IN (0, 1) AND sort_order >= 0 AND is_deleted IN (0, 1)", enforced: "YES"},
		},
		{
			name: "public_home_faqs",
			columns: append([]businessGroupColumnMetadata{
				{name: "id", columnType: "bigint", nullable: "NO", extra: "auto_increment"},
				{name: "guid", columnType: "bigint", nullable: "NO"},
				{name: "content_draft_id", columnType: "bigint", nullable: "NO"},
				{name: "question", columnType: "varchar(200)", nullable: "NO", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"},
				{name: "answer_markdown", columnType: "mediumtext", nullable: "NO", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"},
				{name: "is_visible", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "1", Valid: true}},
				{name: "sort_order", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "0", Valid: true}},
				{name: "revision", columnType: "bigint", nullable: "NO", defaultVal: sql.NullString{String: "1", Valid: true}},
			}, audit...),
			indexes: []businessGroupIndexContract{
				{name: "PRIMARY", columns: []string{"id"}, unique: true},
				{name: "uk_public_home_faqs_guid", columns: []string{"guid"}, unique: true},
				{name: "idx_public_home_faqs_draft_active_order", columns: []string{"content_draft_id", "is_deleted", "sort_order", "guid"}},
			},
			foreignKey: businessGroupForeignKeyMetadata{name: "fk_public_home_faqs_draft", column: "content_draft_id", ordinal: 1, targetTable: "public_content_drafts", targetColumn: "id"},
			check:      businessGroupCheckContract{name: "chk_public_home_faqs_values", clause: "revision > 0 AND is_visible IN (0, 1) AND sort_order >= 0 AND is_deleted IN (0, 1)", enforced: "YES"},
		},
	}
}

// VerifyPublicHomeStructuredContentSchema rejects missing, partial, or drifted
// metadata for both structured home-content tables.
func VerifyPublicHomeStructuredContentSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPublicHomeStructuredContentSchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrPublicHomeStructuredContentSchema
	}
	for _, contract := range publicHomeStructuredContentContracts() {
		metadata, ok := loadBusinessGroupTableMetadata(ctx, db, contract.name)
		if !ok || !matchesPublicHomeStructuredContentContract(contract, metadata, currentSchema) {
			return ErrPublicHomeStructuredContentSchema
		}
	}
	return nil
}

func matchesPublicHomeStructuredContentContract(want publicHomeTableContract, got businessGroupTableMetadata, currentSchema string) bool {
	if got.engine != "InnoDB" || got.characterSet != "utf8mb4" || got.collation != "utf8mb4_unicode_ci" || len(got.columns) != len(want.columns) {
		return false
	}
	for index := range want.columns {
		if !matchesBusinessGroupColumn(want.columns[index], got.columns[index], false) {
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
		for index, row := range rows {
			if row.sequence != index+1 || row.column != expected.columns[index] || (row.nonUnique == 0) != expected.unique || !validRequiredBusinessGroupIndexMetadata(row) {
				return false
			}
		}
	}
	if len(got.foreignKeys) != 1 {
		return false
	}
	foreignKey := got.foreignKeys[0]
	if foreignKey.name != want.foreignKey.name || foreignKey.column != want.foreignKey.column || foreignKey.ordinal != 1 || foreignKey.targetSchema != currentSchema || foreignKey.targetTable != want.foreignKey.targetTable || foreignKey.targetColumn != want.foreignKey.targetColumn || !restrictRule(foreignKey.deleteRule) || !restrictRule(foreignKey.updateRule) {
		return false
	}
	if len(got.checks) != 1 || got.checks[0].name != want.check.name || got.checks[0].enforced != "YES" {
		return false
	}
	wantClause, wantOK := canonicalizeCheckClause(want.check.clause)
	gotClause, gotOK := canonicalizeCheckClause(got.checks[0].clause)
	return wantOK && gotOK && strings.EqualFold(wantClause, gotClause)
}
