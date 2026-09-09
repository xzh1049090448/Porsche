package migration

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

// ErrPublicContentPricingSchema is returned when migration 0012 is only
// partially present or its live MySQL metadata differs from the contract.
var ErrPublicContentPricingSchema = errors.New("public content pricing schema mismatch or unavailable")

type publicContentPricingForeignKeyContract struct {
	name         string
	column       string
	targetTable  string
	targetColumn string
}

type publicContentPricingTableContract struct {
	table       businessGroupTableContract
	foreignKeys []publicContentPricingForeignKeyContract
}

func publicContentPricingTableContracts() []publicContentPricingTableContract {
	return []publicContentPricingTableContract{
		{table: publicContentPricingTable("public_model_configs", publicContentPricingColumns(
			publicColumn("model_key", "varchar(128)", "NO", "", "utf8mb4", "utf8mb4_bin"),
			publicColumn("upstream_model_id", "varchar(255)", "NO", "", "utf8mb4", "utf8mb4_bin"),
			publicColumn("display_name", "varchar(128)", "NO", "", "utf8mb4", "utf8mb4_unicode_ci"),
			publicColumn("provider", "varchar(128)", "NO", "", "utf8mb4", "utf8mb4_unicode_ci"),
			publicColumn("capabilities", "json", "NO", "", "utf8mb4", "utf8mb4_bin"),
			publicColumn("context_window", "bigint", "NO", "", "", ""),
			publicColumn("input_price_usd_per_million_tokens", "decimal(20,8)", "YES", "", "", ""),
			publicColumn("output_price_usd_per_million_tokens", "decimal(20,8)", "YES", "", "", ""),
			publicColumn("status", "int", "NO", "", "", ""),
			publicColumn("inactive_reason", "varchar(128)", "YES", "", "utf8mb4", "utf8mb4_unicode_ci"),
			publicColumn("last_upstream_observed_at", "bigint", "YES", "", "", ""),
			publicColumn("last_upstream_check_at", "bigint", "YES", "", "", ""),
			publicColumn("consecutive_absences", "int", "NO", "0", "", ""),
			publicColumn("revision", "bigint", "NO", "1", "", ""),
			publicColumn("ever_published", "int", "NO", "0", "", ""),
		), []businessGroupIndexContract{
			publicPrimary(), publicIndex("uk_public_model_configs_guid", true, "guid"), publicIndex("uk_public_model_configs_model_key", true, "model_key"), publicIndex("uk_public_model_configs_upstream_model_id", true, "upstream_model_id"),
			publicIndex("idx_public_model_configs_status_revision", false, "status", "is_deleted", "revision"), publicIndex("idx_public_model_configs_provider_active", false, "provider", "is_deleted", "display_name"),
		})},
		{table: publicContentPricingTable("public_price_snapshots", publicContentPricingColumns(
			publicColumn("version", "bigint", "NO", "", "", ""), publicColumn("reason", "int", "NO", "", "", ""), publicColumn("source_revision", "bigint", "NO", "", "", ""),
			publicColumn("content_hash", "char(64)", "NO", "", "ascii", "ascii_bin"), publicColumn("restored_from_snapshot_id", "bigint", "YES", "", "", ""), publicColumn("published_at", "bigint", "NO", "", "", ""),
		), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_public_price_snapshots_guid", true, "guid"), publicIndex("uk_public_price_snapshots_version", true, "version"), publicIndex("idx_public_price_snapshots_version", false, "version", "is_deleted"), publicIndex("idx_public_price_snapshots_restore", false, "restored_from_snapshot_id", "is_deleted")}),
			foreignKeys: []publicContentPricingForeignKeyContract{{"fk_public_price_snapshots_restore", "restored_from_snapshot_id", "public_price_snapshots", "id"}}},
		{table: publicContentPricingTable("public_price_snapshot_items", publicContentPricingColumns(
			publicColumn("snapshot_id", "bigint", "NO", "", "", ""), publicColumn("model_config_id", "bigint", "NO", "", "", ""), publicColumn("model_key", "varchar(128)", "NO", "", "utf8mb4", "utf8mb4_bin"), publicColumn("upstream_model_id", "varchar(255)", "NO", "", "utf8mb4", "utf8mb4_bin"),
			publicColumn("display_name", "varchar(128)", "NO", "", "utf8mb4", "utf8mb4_unicode_ci"), publicColumn("provider", "varchar(128)", "NO", "", "utf8mb4", "utf8mb4_unicode_ci"), publicColumn("capabilities", "json", "NO", "", "utf8mb4", "utf8mb4_bin"), publicColumn("context_window", "bigint", "NO", "", "", ""),
			publicColumn("input_price_usd_per_million_tokens", "decimal(20,8)", "NO", "", "", ""), publicColumn("output_price_usd_per_million_tokens", "decimal(20,8)", "NO", "", "", ""), publicColumn("upstream_checked_at", "bigint", "YES", "", "", ""),
		), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_public_price_snapshot_items_guid", true, "guid"), publicIndex("uk_public_price_snapshot_items_snapshot_model", true, "snapshot_id", "model_config_id"), publicIndex("uk_public_price_snapshot_items_snapshot_key", true, "snapshot_id", "model_key"), publicIndex("idx_public_price_snapshot_items_model", false, "model_config_id", "is_deleted")}),
			foreignKeys: []publicContentPricingForeignKeyContract{{"fk_public_price_snapshot_items_snapshot", "snapshot_id", "public_price_snapshots", "id"}, {"fk_public_price_snapshot_items_model", "model_config_id", "public_model_configs", "id"}}},
		{table: publicContentPricingTable("public_content_drafts", publicContentPricingColumns(
			publicColumn("document_kind", "int", "NO", "", "", ""), publicColumn("payload", "json", "NO", "", "utf8mb4", "utf8mb4_bin"), publicColumn("revision", "bigint", "NO", "1", "", ""), publicColumn("review_state", "int", "NO", "1", "", ""),
		), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_public_content_drafts_guid", true, "guid"), publicIndex("uk_public_content_drafts_document_active", true, "document_kind", "is_deleted"), publicIndex("idx_public_content_drafts_document_revision", false, "document_kind", "revision", "is_deleted")})},
		{table: publicContentPricingTable("public_content_releases", publicContentPricingColumns(
			publicColumn("document_kind", "int", "NO", "", "", ""), publicColumn("version", "bigint", "NO", "", "", ""), publicColumn("source_revision", "bigint", "NO", "", "", ""), publicColumn("payload", "json", "NO", "", "utf8mb4", "utf8mb4_bin"), publicColumn("content_hash", "char(64)", "NO", "", "ascii", "ascii_bin"), publicColumn("restored_from_release_id", "bigint", "YES", "", "", ""), publicColumn("published_at", "bigint", "NO", "", "", ""),
		), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_public_content_releases_guid", true, "guid"), publicIndex("uk_public_content_releases_document_version", true, "document_kind", "version"), publicIndex("idx_public_content_releases_document_version", false, "document_kind", "version", "is_deleted"), publicIndex("idx_public_content_releases_restore", false, "restored_from_release_id", "is_deleted")}),
			foreignKeys: []publicContentPricingForeignKeyContract{{"fk_public_content_releases_restore", "restored_from_release_id", "public_content_releases", "id"}}},
		{table: publicContentPricingTable("public_publication_state", publicContentPricingColumns(
			publicColumn("state_key", "varchar(64)", "NO", "", "ascii", "ascii_bin"), publicColumn("price_snapshot_id", "bigint", "YES", "", "", ""), publicColumn("content_release_id", "bigint", "YES", "", "", ""), publicColumn("price_visibility", "int", "NO", "", "", ""), publicColumn("revision", "bigint", "NO", "1", "", ""),
		), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_public_publication_state_guid", true, "guid"), publicIndex("uk_public_publication_state_key", true, "state_key"), publicIndex("idx_public_publication_state_revision", false, "revision", "is_deleted")}),
			foreignKeys: []publicContentPricingForeignKeyContract{{"fk_public_publication_state_price", "price_snapshot_id", "public_price_snapshots", "id"}, {"fk_public_publication_state_content", "content_release_id", "public_content_releases", "id"}}},
		{table: publicContentPricingTable("upstream_model_observations", publicContentPricingColumns(
			publicColumn("upstream_model_id", "varchar(255)", "NO", "", "utf8mb4", "utf8mb4_bin"), publicColumn("provider", "varchar(128)", "NO", "", "utf8mb4", "utf8mb4_unicode_ci"), publicColumn("input_price_usd_per_million_tokens", "decimal(20,8)", "YES", "", "", ""), publicColumn("output_price_usd_per_million_tokens", "decimal(20,8)", "YES", "", "", ""), publicColumn("catalog_complete", "int", "NO", "", "", ""), publicColumn("catalog_fresh", "int", "NO", "", "", ""), publicColumn("observed_at", "bigint", "NO", "", "", ""), publicColumn("response_summary_hash", "char(64)", "NO", "", "ascii", "ascii_bin"),
		), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_upstream_model_observations_guid", true, "guid"), publicIndex("uk_upstream_model_observations_identity", true, "upstream_model_id", "observed_at"), publicIndex("idx_upstream_model_observations_observed_at", false, "observed_at", "is_deleted"), publicIndex("idx_upstream_model_observations_model_observed", false, "upstream_model_id", "is_deleted", "observed_at")})},
		{table: publicContentPricingTable("root_alerts", publicContentPricingColumns(
			publicColumn("model_config_id", "bigint", "YES", "", "", ""), publicColumn("model_key", "varchar(128)", "YES", "", "utf8mb4", "utf8mb4_bin"), publicColumn("alert_type", "int", "NO", "", "", ""), publicColumn("state", "int", "NO", "", "", ""), publicColumn("fingerprint", "char(64)", "NO", "", "ascii", "ascii_bin"), publicColumn("payload", "json", "NO", "", "utf8mb4", "utf8mb4_bin"), publicColumn("occurrence_count", "int", "NO", "1", "", ""), publicColumn("first_observed_at", "bigint", "NO", "", "", ""), publicColumn("last_observed_at", "bigint", "NO", "", "", ""), publicColumn("resolved_at", "bigint", "YES", "", "", ""),
		), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_root_alerts_guid", true, "guid"), publicIndex("uk_root_alerts_fingerprint", true, "fingerprint"), publicIndex("idx_root_alerts_active", false, "state", "is_deleted", "updated_at"), publicIndex("idx_root_alerts_model_active", false, "model_config_id", "state", "is_deleted")}),
			foreignKeys: []publicContentPricingForeignKeyContract{{"fk_root_alerts_model", "model_config_id", "public_model_configs", "id"}}},
		{table: publicContentPricingTable("root_alert_receipts", publicContentPricingColumns(
			publicColumn("alert_id", "bigint", "NO", "", "", ""), publicColumn("root_user_id", "bigint", "NO", "", "", ""), publicColumn("read_at", "bigint", "YES", "", "", ""), publicColumn("acknowledged_at", "bigint", "YES", "", "", ""),
		), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_root_alert_receipts_guid", true, "guid"), publicIndex("uk_root_alert_receipts_alert_root", true, "alert_id", "root_user_id"), publicIndex("idx_root_alert_receipts_root_unread", false, "root_user_id", "is_deleted", "read_at")}),
			foreignKeys: []publicContentPricingForeignKeyContract{{"fk_root_alert_receipts_alert", "alert_id", "root_alerts", "id"}, {"fk_root_alert_receipts_root", "root_user_id", "users", "id"}}},
		{table: publicContentPricingTable("public_render_jobs", publicContentPricingColumns(
			publicColumn("price_snapshot_id", "bigint", "NO", "", "", ""), publicColumn("content_release_id", "bigint", "NO", "", "", ""), publicColumn("state", "int", "NO", "", "", ""), publicColumn("lease_owner_hmac", "char(64)", "YES", "", "ascii", "ascii_bin"), publicColumn("lease_expires_at", "bigint", "YES", "", "", ""), publicColumn("attempt_count", "int", "NO", "0", "", ""), publicColumn("last_failure", "varchar(1024)", "YES", "", "utf8mb4", "utf8mb4_unicode_ci"), publicColumn("completed_at", "bigint", "YES", "", "", ""),
		), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_public_render_jobs_guid", true, "guid"), publicIndex("uk_public_render_jobs_release_pair", true, "price_snapshot_id", "content_release_id"), publicIndex("idx_public_render_jobs_lease", false, "state", "is_deleted", "lease_expires_at")}),
			foreignKeys: []publicContentPricingForeignKeyContract{{"fk_public_render_jobs_price", "price_snapshot_id", "public_price_snapshots", "id"}, {"fk_public_render_jobs_content", "content_release_id", "public_content_releases", "id"}}},
	}
}

func publicContentPricingColumns(business ...businessGroupColumnContract) []businessGroupColumnContract {
	columns := []businessGroupColumnContract{
		publicColumn("id", "bigint", "NO", "", "", ""),
		publicColumn("guid", "bigint", "NO", "", "", ""),
	}
	columns = append(columns, business...)
	return append(columns,
		publicColumn("created_at", "bigint", "NO", "", "", ""),
		publicColumn("created_by", "bigint", "YES", "", "", ""),
		publicColumn("updated_at", "bigint", "NO", "", "", ""),
		publicColumn("updated_by", "bigint", "YES", "", "", ""),
		publicColumn("is_deleted", "int", "NO", "0", "", ""),
	)
}

func publicColumn(name, columnType, nullable, defaultValue, characterSet, collation string) businessGroupColumnContract {
	column := businessGroupColumnContract{name: name, columnType: columnType, nullable: nullable, characterSet: characterSet, collation: collation}
	if defaultValue != "" {
		column.defaultVal = sql.NullString{String: defaultValue, Valid: true}
	}
	if name == "id" {
		column.extra = "auto_increment"
	}
	return column
}

func publicPrimary() businessGroupIndexContract { return publicIndex("PRIMARY", true, "id") }
func publicIndex(name string, unique bool, columns ...string) businessGroupIndexContract {
	return businessGroupIndexContract{name: name, columns: columns, unique: unique}
}

func publicContentPricingTable(name string, columns []businessGroupColumnContract, indexes []businessGroupIndexContract) businessGroupTableContract {
	return businessGroupTableContract{name: name, columns: columns, indexes: indexes}
}

// VerifyPublicContentPricingSchema fails closed when 0012 is missing, partial,
// or has been changed after its checksum-protected migration was recorded.
func VerifyPublicContentPricingSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPublicContentPricingSchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrPublicContentPricingSchema
	}
	metadata := make(map[string]businessGroupTableMetadata)
	for _, contract := range publicContentPricingTableContracts() {
		table, ok := loadBusinessGroupTableMetadata(ctx, db, contract.table.name)
		if !ok {
			return ErrPublicContentPricingSchema
		}
		metadata[contract.table.name] = table
	}
	if !matchesPublicContentPricingSchema(publicContentPricingTableContracts(), metadata, currentSchema) {
		return ErrPublicContentPricingSchema
	}
	return nil
}

func matchesPublicContentPricingSchema(contracts []publicContentPricingTableContract, metadata map[string]businessGroupTableMetadata, currentSchema string) bool {
	if len(metadata) != len(contracts) {
		return false
	}
	for _, contract := range contracts {
		table, ok := metadata[contract.table.name]
		if !ok || !matchesPublicContentPricingTableContract(contract, table, currentSchema) {
			return false
		}
	}
	return true
}

func matchesPublicContentPricingTableContract(want publicContentPricingTableContract, got businessGroupTableMetadata, currentSchema string) bool {
	if got.engine != "InnoDB" || got.characterSet != "utf8mb4" || got.collation != "utf8mb4_unicode_ci" || len(got.columns) != len(want.table.columns) {
		return false
	}
	for i, expected := range want.table.columns {
		if !matchesBusinessGroupColumn(businessGroupColumnMetadata{name: expected.name, columnType: expected.columnType, nullable: expected.nullable, defaultVal: expected.defaultVal, extra: expected.extra, characterSet: expected.characterSet, collation: expected.collation}, got.columns[i], false) {
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
		if actual.ordinal != 1 || actual.column != expected.column || actual.targetSchema != currentSchema || actual.targetTable != expected.targetTable || actual.targetColumn != expected.targetColumn || !restrictRule(actual.deleteRule) || !restrictRule(actual.updateRule) {
			return false
		}
	}
	return true
}
