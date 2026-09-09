package models

import (
	"reflect"
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

func TestPublicContentPricingCapabilitiesAreStringArrays(t *testing.T) {
	wantType := reflect.TypeOf(JSONSlice{})
	for _, tc := range []struct {
		name  string
		value interface{}
	}{
		{"public model config", PublicModelConfig{}.Capabilities},
		{"public price snapshot item", PublicPriceSnapshotItem{}.Capabilities},
	} {
		if got := reflect.TypeOf(tc.value); got != wantType {
			t.Errorf("%s capabilities type = %v, want %v", tc.name, got, wantType)
		}
	}
	for _, capabilities := range []JSONSlice{{"chat", "vision"}, {"text"}} {
		raw, err := capabilities.Value()
		if err != nil {
			t.Fatal(err)
		}
		if reflect.TypeOf(raw).Kind() != reflect.String || len(raw.(string)) < 2 || raw.(string)[0] != '[' {
			t.Fatalf("capabilities JSON = %q, want JSON array", raw)
		}
	}
}

func TestPublicContentPricingEnumsAreBidirectionalAndRejectUnknown(t *testing.T) {
	tests := []struct {
		name    string
		entries []struct {
			value int
			name  string
		}
		stringify func(int) string
		parse     func(string) (int, bool)
	}{
		{
			name: "price snapshot reason",
			entries: []struct {
				value int
				name  string
			}{{1, "root_publish"}, {2, "upstream_safety"}, {3, "restore"}},
			stringify: func(value int) string { return PublicPriceSnapshotReason(value).String() },
			parse: func(value string) (int, bool) {
				parsed, ok := ParsePublicPriceSnapshotReason(value)
				return int(parsed), ok
			},
		},
		{
			name: "content document kind",
			entries: []struct {
				value int
				name  string
			}{{1, "site"}, {2, "home"}, {3, "about"}, {4, "terms"}, {5, "privacy"}},
			stringify: func(value int) string { return PublicContentDocumentKind(value).String() },
			parse: func(value string) (int, bool) {
				parsed, ok := ParsePublicContentDocumentKind(value)
				return int(parsed), ok
			},
		},
		{
			name: "content review state",
			entries: []struct {
				value int
				name  string
			}{{1, "pending"}, {2, "approved"}},
			stringify: func(value int) string { return PublicContentReviewState(value).String() },
			parse: func(value string) (int, bool) {
				parsed, ok := ParsePublicContentReviewState(value)
				return int(parsed), ok
			},
		},
		{
			name: "price visibility",
			entries: []struct {
				value int
				name  string
			}{{1, "visible"}, {2, "authenticated_only"}},
			stringify: func(value int) string { return PublicPriceVisibility(value).String() },
			parse: func(value string) (int, bool) {
				parsed, ok := ParsePublicPriceVisibility(value)
				return int(parsed), ok
			},
		},
		{
			name: "root alert type",
			entries: []struct {
				value int
				name  string
			}{{1, "published_price_below_upstream"}, {2, "upstream_missing"}, {3, "automatic_inactivation"}, {4, "upstream_reappearance"}, {5, "catalog_sync_failure"}, {6, "price_not_comparable"}, {7, "renderer_failure"}},
			stringify: func(value int) string { return RootAlertType(value).String() },
			parse:     func(value string) (int, bool) { parsed, ok := ParseRootAlertType(value); return int(parsed), ok },
		},
		{
			name: "root alert state",
			entries: []struct {
				value int
				name  string
			}{{1, "active"}, {2, "resolved"}},
			stringify: func(value int) string { return RootAlertState(value).String() },
			parse:     func(value string) (int, bool) { parsed, ok := ParseRootAlertState(value); return int(parsed), ok },
		},
		{
			name: "public render job state",
			entries: []struct {
				value int
				name  string
			}{{1, "queued"}, {2, "leased"}, {3, "succeeded"}, {4, "failed"}},
			stringify: func(value int) string { return PublicRenderJobState(value).String() },
			parse:     func(value string) (int, bool) { parsed, ok := ParsePublicRenderJobState(value); return int(parsed), ok },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, entry := range tc.entries {
				if got := tc.stringify(entry.value); got != entry.name {
					t.Errorf("String(%d) = %q, want %q", entry.value, got, entry.name)
				}
				if got, ok := tc.parse(entry.name); !ok || got != entry.value {
					t.Errorf("Parse(%q) = %d/%v, want %d/true", entry.name, got, ok, entry.value)
				}
			}
			if got := tc.stringify(99); got != "unknown" {
				t.Errorf("String(99) = %q, want unknown", got)
			}
			if got, ok := tc.parse("not_a_public_content_pricing_value"); ok || got != 0 {
				t.Errorf("unknown parse = %d/%v, want 0/false", got, ok)
			}
		})
	}
}
