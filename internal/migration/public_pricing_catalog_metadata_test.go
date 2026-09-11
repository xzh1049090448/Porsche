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
	if err != nil || len(ms) != 19 || ms[17].Version != "0018" {
		t.Fatalf("migrations=%v err=%v", len(ms), err)
	}
	up, down := strings.ToLower(string(ms[17].UpSQL)), strings.ToLower(string(ms[17].DownSQL))
	for _, token := range []string{"public_display_group", "endpoint_types", "public_restrictions", "price_source", "price_reviewer", "price_effective_at", "pricing_type", "effective_at"} {
		if !strings.Contains(up, token) || !strings.Contains(down, token) {
			t.Fatalf("0016 missing reversible %s", token)
		}
	}
	if strings.Contains(up, "upstream_url") || strings.Contains(up, "api_key") {
		t.Fatal("0016 contains secret-bearing column")
	}
	statements := splitStatements(string(ms[17].UpSQL))
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

func TestPublicPricingCatalogMetadataStopsBeforeSecondDDLWhenFirstVerificationFails(t *testing.T) {
	steps := []publicPricingCatalogMetadataStep{{"first", []string{"a"}}, {"second", []string{"b"}}}
	executed, verified := []string{}, []string{}
	err := applyPublicPricingCatalogMetadataSteps(steps, []string{"ALTER first", "ALTER second"},
		func(publicPricingCatalogMetadataStep) (int64, error) { return 0, nil },
		func(sql string) error { executed = append(executed, sql); return nil },
		func(table string) error {
			verified = append(verified, table)
			return ErrPublicPricingCatalogMetadataMigration
		},
	)
	if !errors.Is(err, ErrPublicPricingCatalogMetadataMigration) || strings.Join(executed, ",") != "ALTER first" || strings.Join(verified, ",") != "first" {
		t.Fatalf("err=%v executed=%v verified=%v", err, executed, verified)
	}
}

func TestPublicPricingCatalogMetadataFreshPathExecutesAndVerifiesEachStep(t *testing.T) {
	steps := []publicPricingCatalogMetadataStep{{"first", []string{"a"}}, {"second", []string{"b"}}}
	executed, verified := []string{}, []string{}
	err := applyPublicPricingCatalogMetadataSteps(steps, []string{"ALTER first", "ALTER second"},
		func(publicPricingCatalogMetadataStep) (int64, error) { return 0, nil },
		func(sql string) error { executed = append(executed, sql); return nil },
		func(table string) error { verified = append(verified, table); return nil },
	)
	if err != nil || strings.Join(executed, ",") != "ALTER first,ALTER second" || strings.Join(verified, ",") != "first,second" {
		t.Fatalf("err=%v executed=%v verified=%v", err, executed, verified)
	}
}
