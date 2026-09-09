package migration

import (
	"database/sql"
	"reflect"
	"testing"
)

func TestPublicContentPricingSchemaContractRejectsMissingPartialDriftAndInterruptedCreate(t *testing.T) {
	contracts := publicContentPricingTableContracts()
	valid := publicContentPricingMetadataFromContracts(contracts, "porsche_test")
	applyPublicContentPricingExpectedChecks(valid)
	if !matchesPublicContentPricingSchema(contracts, valid, "porsche_test") {
		t.Fatal("complete 0012 metadata does not match its schema contract")
	}

	tests := []struct {
		name   string
		mutate func(map[string]businessGroupTableMetadata)
	}{
		{name: "missing_table", mutate: func(metadata map[string]businessGroupTableMetadata) {
			delete(metadata, "root_alerts")
		}},
		{name: "partial_table", mutate: func(metadata map[string]businessGroupTableMetadata) {
			table := metadata["public_price_snapshot_items"]
			table.columns = table.columns[:2]
			metadata["public_price_snapshot_items"] = table
		}},
		{name: "column_type_drift", mutate: func(metadata map[string]businessGroupTableMetadata) {
			table := metadata["public_model_configs"]
			for i := range table.columns {
				if table.columns[i].name == "input_price_usd_per_million_tokens" {
					table.columns[i].columnType = "decimal(18,6)"
				}
			}
			metadata["public_model_configs"] = table
		}},
		{name: "missing_required_index", mutate: func(metadata map[string]businessGroupTableMetadata) {
			table := metadata["public_content_releases"]
			table.indexes = table.indexes[:1]
			metadata["public_content_releases"] = table
		}},
		{name: "foreign_key_target_drift", mutate: func(metadata map[string]businessGroupTableMetadata) {
			table := metadata["public_render_jobs"]
			table.foreignKeys[0].targetColumn = "guid"
			metadata["public_render_jobs"] = table
		}},
		{name: "missing_check", mutate: func(metadata map[string]businessGroupTableMetadata) {
			table := metadata["public_model_configs"]
			table.checks = table.checks[1:]
			metadata["public_model_configs"] = table
		}},
		{name: "altered_check", mutate: func(metadata map[string]businessGroupTableMetadata) {
			table := metadata["public_render_jobs"]
			table.checks[0].clause = "state IN (1, 2, 3)"
			metadata["public_render_jobs"] = table
		}},
		{name: "interrupted_create_prefix", mutate: func(metadata map[string]businessGroupTableMetadata) {
			for table := range metadata {
				if table != "public_model_configs" {
					delete(metadata, table)
				}
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metadata := publicContentPricingMetadataFromContracts(contracts, "porsche_test")
			applyPublicContentPricingExpectedChecks(metadata)
			tc.mutate(metadata)
			if matchesPublicContentPricingSchema(contracts, metadata, "porsche_test") {
				t.Fatal("schema verifier accepted invalid 0012 metadata")
			}
		})
	}
}

func TestPublicContentPricingCheckContractsMatchIndependentExpectedChecks(t *testing.T) {
	expected := publicContentPricingExpectedChecks()
	contracts := publicContentPricingTableContracts()
	if len(expected) != len(contracts) {
		t.Fatalf("independent expected CHECK tables = %d, contracts = %d", len(expected), len(contracts))
	}
	for _, contract := range contracts {
		want, ok := expected[contract.table.name]
		if !ok {
			t.Errorf("independent expected checks missing %s", contract.table.name)
			continue
		}
		if !reflect.DeepEqual(contract.table.checks, want) {
			t.Errorf("%s CHECK contract = %#v, want %#v", contract.table.name, contract.table.checks, want)
		}
		for _, check := range want {
			if canonical, ok := canonicalizeCheckClause(check.clause); !ok || canonical == "" {
				t.Errorf("%s.%s CHECK cannot be normalized: %q", contract.table.name, check.name, check.clause)
			}
		}
	}
}

func TestPublicContentPricingSchemaAcceptsNormalizedChecksAndImplicitForeignKeyIndexes(t *testing.T) {
	contracts := publicContentPricingTableContracts()
	metadata := publicContentPricingMetadataFromContracts(contracts, "porsche_test")
	applyPublicContentPricingExpectedChecks(metadata)

	config := metadata["public_model_configs"]
	config.checks[0].clause = " ( `STATUS` in (001, 2, 003) ) "
	metadata["public_model_configs"] = config
	renderJobs := metadata["public_render_jobs"]
	renderJobs.checks[1].clause = "((`ATTEMPT_COUNT` >= (000)) AND (`IS_DELETED` IN ((0), (01))))"
	metadata["public_render_jobs"] = renderJobs
	publication := metadata["public_publication_state"]
	publication.indexes = append(publication.indexes, businessGroupIndexMetadata{
		name: "fk_public_publication_state_price", column: "price_snapshot_id", sequence: 1, nonUnique: 1,
		collation: sql.NullString{String: "A", Valid: true}, indexType: "BTREE", visible: "YES",
	})
	metadata["public_publication_state"] = publication

	if !matchesPublicContentPricingSchema(contracts, metadata, "porsche_test") {
		t.Fatal("schema verifier rejected normalized CHECK output or a legitimate implicit foreign-key index")
	}
}

func TestVerifyPublicContentPricingSchemaRejectsNilDB(t *testing.T) {
	if err := VerifyPublicContentPricingSchema(nil, nil); err != ErrPublicContentPricingSchema {
		t.Fatalf("VerifyPublicContentPricingSchema(nil) = %v, want %v", err, ErrPublicContentPricingSchema)
	}
}

func publicContentPricingMetadataFromContracts(contracts []publicContentPricingTableContract, schema string) map[string]businessGroupTableMetadata {
	metadata := make(map[string]businessGroupTableMetadata, len(contracts))
	for _, contract := range contracts {
		table := businessGroupTableMetadata{engine: "InnoDB", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"}
		for _, column := range contract.table.columns {
			table.columns = append(table.columns, businessGroupColumnMetadata{
				name: column.name, columnType: column.columnType, nullable: column.nullable, defaultVal: column.defaultVal,
				extra: column.extra, characterSet: column.characterSet, collation: column.collation,
			})
		}
		for _, index := range contract.table.indexes {
			for position, column := range index.columns {
				nonUnique := 1
				if index.unique {
					nonUnique = 0
				}
				table.indexes = append(table.indexes, businessGroupIndexMetadata{
					name: index.name, column: column, sequence: position + 1, nonUnique: nonUnique,
					collation: sql.NullString{String: "A", Valid: true}, indexType: "BTREE", visible: "YES",
				})
			}
		}
		for _, foreignKey := range contract.foreignKeys {
			table.foreignKeys = append(table.foreignKeys, businessGroupForeignKeyMetadata{
				name: foreignKey.name, column: foreignKey.column, ordinal: 1, targetSchema: schema, targetTable: foreignKey.targetTable, targetColumn: foreignKey.targetColumn,
				deleteRule: "RESTRICT", updateRule: "RESTRICT",
			})
		}
		metadata[contract.table.name] = table
	}
	return metadata
}

// This literal expectation is independent from production contracts. It
// captures every named CHECK in 0012 so a self-referential fixture cannot make
// an empty or incomplete verifier contract appear valid.
func publicContentPricingExpectedChecks() map[string][]businessGroupCheckContract {
	return map[string][]businessGroupCheckContract{
		"public_model_configs": {
			{name: "chk_public_model_configs_status", clause: "status IN (1, 2, 3)", enforced: "YES"},
			{name: "chk_public_model_configs_values", clause: "context_window > 0 AND consecutive_absences >= 0 AND revision > 0 AND ever_published IN (0, 1) AND is_deleted IN (0, 1)", enforced: "YES"},
			{name: "chk_public_model_configs_prices", clause: "(input_price_usd_per_million_tokens IS NULL OR input_price_usd_per_million_tokens >= 0) AND (output_price_usd_per_million_tokens IS NULL OR output_price_usd_per_million_tokens >= 0)", enforced: "YES"},
		},
		"public_price_snapshots": {
			{name: "chk_public_price_snapshots_reason", clause: "reason IN (1, 2, 3)", enforced: "YES"},
			{name: "chk_public_price_snapshots_values", clause: "version > 0 AND source_revision > 0 AND published_at > 0 AND is_deleted IN (0, 1)", enforced: "YES"},
		},
		"public_price_snapshot_items": {
			{name: "chk_public_price_snapshot_items_values", clause: "context_window > 0 AND input_price_usd_per_million_tokens >= 0 AND output_price_usd_per_million_tokens >= 0 AND is_deleted IN (0, 1)", enforced: "YES"},
		},
		"public_content_drafts": {
			{name: "chk_public_content_drafts_kind", clause: "document_kind IN (1, 2, 3, 4, 5)", enforced: "YES"},
			{name: "chk_public_content_drafts_review", clause: "review_state IN (1, 2)", enforced: "YES"},
			{name: "chk_public_content_drafts_values", clause: "revision > 0 AND is_deleted IN (0, 1)", enforced: "YES"},
		},
		"public_content_releases": {
			{name: "chk_public_content_releases_kind", clause: "document_kind IN (1, 2, 3, 4, 5)", enforced: "YES"},
			{name: "chk_public_content_releases_values", clause: "version > 0 AND source_revision > 0 AND published_at > 0 AND is_deleted IN (0, 1)", enforced: "YES"},
		},
		"public_publication_state": {
			{name: "chk_public_publication_state_visibility", clause: "price_visibility IN (1, 2)", enforced: "YES"},
			{name: "chk_public_publication_state_values", clause: "revision > 0 AND is_deleted IN (0, 1)", enforced: "YES"},
		},
		"upstream_model_observations": {
			{name: "chk_upstream_model_observations_catalog", clause: "catalog_complete IN (0, 1) AND catalog_fresh IN (0, 1) AND is_deleted IN (0, 1)", enforced: "YES"},
			{name: "chk_upstream_model_observations_prices", clause: "(input_price_usd_per_million_tokens IS NULL OR input_price_usd_per_million_tokens >= 0) AND (output_price_usd_per_million_tokens IS NULL OR output_price_usd_per_million_tokens >= 0)", enforced: "YES"},
		},
		"root_alerts": {
			{name: "chk_root_alerts_type", clause: "alert_type IN (1, 2, 3, 4, 5, 6, 7)", enforced: "YES"},
			{name: "chk_root_alerts_state", clause: "state IN (1, 2)", enforced: "YES"},
			{name: "chk_root_alerts_values", clause: "occurrence_count > 0 AND first_observed_at > 0 AND last_observed_at >= first_observed_at AND is_deleted IN (0, 1)", enforced: "YES"},
		},
		"root_alert_receipts": {
			{name: "chk_root_alert_receipts_deleted", clause: "is_deleted IN (0, 1)", enforced: "YES"},
		},
		"public_render_jobs": {
			{name: "chk_public_render_jobs_state", clause: "state IN (1, 2, 3, 4)", enforced: "YES"},
			{name: "chk_public_render_jobs_values", clause: "attempt_count >= 0 AND is_deleted IN (0, 1)", enforced: "YES"},
		},
	}
}

func applyPublicContentPricingExpectedChecks(metadata map[string]businessGroupTableMetadata) {
	for tableName, checks := range publicContentPricingExpectedChecks() {
		table := metadata[tableName]
		for _, check := range checks {
			table.checks = append(table.checks, businessGroupCheckMetadata{name: check.name, clause: check.clause, enforced: check.enforced})
		}
		metadata[tableName] = table
	}
}
