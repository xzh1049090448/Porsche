package migration

import (
	"database/sql"
	"testing"
)

func TestPublicContentPricingSchemaContractRejectsMissingPartialDriftAndInterruptedCreate(t *testing.T) {
	contracts := publicContentPricingTableContracts()
	valid := publicContentPricingMetadataFromContracts(contracts, "porsche_test")
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
			tc.mutate(metadata)
			if matchesPublicContentPricingSchema(contracts, metadata, "porsche_test") {
				t.Fatal("schema verifier accepted invalid 0012 metadata")
			}
		})
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
