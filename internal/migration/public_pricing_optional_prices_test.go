package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPublicPricingOptionalPricesMigrationIsLatestAndReversible(t *testing.T) {
	ms, e := All()
	if e != nil || len(ms) != 19 || ms[18].Version != "0019" {
		t.Fatalf("migrations=%d err=%v", len(ms), e)
	}
	for _, token := range []string{"input_price_usd_per_million_tokens", "output_price_usd_per_million_tokens", " null", " not null"} {
		if !strings.Contains(strings.ToLower(string(ms[18].UpSQL))+strings.ToLower(string(ms[18].DownSQL)), token) {
			t.Fatalf("missing %s", token)
		}
	}
	down := strings.ToLower(string(ms[18].DownSQL))
	if strings.Contains(down, "update ") || strings.Contains(down, "coalesce") || strings.Count(down, "alter table") != 1 {
		t.Fatalf("0017 down must fail atomically on NULL without coercion: %s", down)
	}
}

func TestPublicPricingOptionalPricesVerifierFailsClosedWithoutDatabase(t *testing.T) {
	if !errors.Is(VerifyPublicPricingOptionalPricesSchema(context.Background(), nil), ErrPublicPricingOptionalPricesMigration) {
		t.Fatal("nil verifier accepted")
	}
}
