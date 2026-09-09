package models

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestPublicContentPricingModelsUseExplicitTablesAndStableEnums(t *testing.T) {
	models := []struct {
		model interface{}
		table string
	}{
		{PublicModelConfig{}, "public_model_configs"},
		{PublicPriceSnapshot{}, "public_price_snapshots"},
		{PublicPriceSnapshotItem{}, "public_price_snapshot_items"},
		{PublicPublicationState{}, "public_publication_state"},
		{PublicContentDraft{}, "public_content_drafts"},
		{PublicContentRelease{}, "public_content_releases"},
		{UpstreamModelObservation{}, "upstream_model_observations"},
		{RootAlert{}, "root_alerts"},
		{RootAlertReceipt{}, "root_alert_receipts"},
		{PublicRenderJob{}, "public_render_jobs"},
	}
	for _, tc := range models {
		parsed, err := schema.Parse(tc.model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Table != tc.table {
			t.Errorf("%T table = %q, want %q", tc.model, parsed.Table, tc.table)
		}
		for _, field := range []string{"ID", "Guid", "CreatedAt", "CreatedBy", "UpdatedAt", "UpdatedBy", "IsDeleted"} {
			if parsed.LookUpField(field) == nil {
				t.Errorf("%T missing %s", tc.model, field)
			}
		}
	}
	if PublicModelConfigStatusDraft != 1 || PublicModelConfigStatusActive != 2 || PublicModelConfigStatusInactive != 3 {
		t.Fatalf("public model status enum changed: %d %d %d", PublicModelConfigStatusDraft, PublicModelConfigStatusActive, PublicModelConfigStatusInactive)
	}
	if RootAlertStateActive != 1 || RootAlertStateResolved != 2 {
		t.Fatalf("root alert state enum changed: %d %d", RootAlertStateActive, RootAlertStateResolved)
	}
}

func TestPublicContentPricingModelsPreserveSchemaColumnTypes(t *testing.T) {
	config, err := schema.Parse(&PublicModelConfig{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	for _, expectation := range []struct {
		field    string
		column   string
		typeName string
	}{
		{"InputPriceUSDPerMillionTokens", "input_price_usd_per_million_tokens", "decimal(20,8)"},
		{"OutputPriceUSDPerMillionTokens", "output_price_usd_per_million_tokens", "decimal(20,8)"},
		{"Capabilities", "capabilities", "json"},
	} {
		field := config.LookUpField(expectation.field)
		if field == nil || field.DBName != expectation.column || string(field.DataType) != expectation.typeName {
			t.Fatalf("%s metadata = %#v, want %s/%s", expectation.field, field, expectation.column, expectation.typeName)
		}
	}
	draft, err := schema.Parse(&PublicContentDraft{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	if field := draft.LookUpField("Payload"); field == nil || field.DBName != "payload" || string(field.DataType) != "json" {
		t.Fatalf("draft payload metadata = %#v", field)
	}
}
