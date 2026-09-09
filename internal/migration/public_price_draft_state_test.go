package migration

import (
	"strings"
	"testing"
)

func TestPublicPriceDraftStateMigrationContract(t *testing.T) {
	ms, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 13 || ms[12].Version != "0013" {
		t.Fatalf("migration tail=%d/%v", len(ms), ms)
	}
	up := strings.ToLower(string(ms[12].UpSQL))
	for _, fragment := range []string{"create table if not exists public_price_draft_state", "state_key varchar(64)", "revision bigint not null default 1", "unique key uk_public_price_draft_state_key (state_key)", "check (revision > 0 and is_deleted in (0, 1))", "-- porsche:seed-public-price-draft-state"} {
		if !strings.Contains(up, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
	down := strings.ToLower(string(ms[12].DownSQL))
	if !strings.Contains(down, "drop table if exists public_price_draft_state") {
		t.Fatal("unsafe/missing down")
	}
}
