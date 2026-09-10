package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPublicPricingCatalogMetadataVerifierFailsClosedWithoutDatabase(t *testing.T) {
	if !errors.Is(VerifyPublicPricingCatalogMetadataSchema(context.Background(), nil), ErrPublicPricingCatalogMetadataMigration) {
		t.Fatal("nil verifier did not fail closed")
	}
}

func TestPublicPricingCatalogMetadataMigrationIsLatestAndReversible(t *testing.T) {
	ms, err := All()
	if err != nil || len(ms) != 17 || ms[15].Version != "0016" {
		t.Fatalf("migrations=%v err=%v", len(ms), err)
	}
	up, down := strings.ToLower(string(ms[15].UpSQL)), strings.ToLower(string(ms[15].DownSQL))
	for _, token := range []string{"public_display_group", "endpoint_types", "public_restrictions", "price_source", "price_reviewer", "price_effective_at", "pricing_type", "effective_at"} {
		if !strings.Contains(up, token) || !strings.Contains(down, token) {
			t.Fatalf("0016 missing reversible %s", token)
		}
	}
	if strings.Contains(up, "upstream_url") || strings.Contains(up, "api_key") {
		t.Fatal("0016 contains secret-bearing column")
	}
	statements := splitStatements(string(ms[15].UpSQL))
	if len(statements) != 2 {
		t.Fatalf("0016 statements=%d want=2: %#v", len(statements), statements)
	}
	for i, statement := range statements {
		normalized := strings.ToLower(strings.TrimSpace(statement))
		if !strings.HasPrefix(normalized, "alter table ") || strings.Contains(statement, `\n`) {
			t.Fatalf("0016 statement %d invalid: %q", i, statement)
		}
	}
}
