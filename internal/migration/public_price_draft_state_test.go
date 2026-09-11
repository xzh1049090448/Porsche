package migration

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/persistence"
)

func TestPublicPriceDraftStateMigrationContract(t *testing.T) {
	ms, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 19 || ms[14].Version != "0015" {
		t.Fatalf("migration tail=%d/%v", len(ms), ms)
	}
	up := strings.ToLower(string(ms[14].UpSQL))
	for _, fragment := range []string{"create table if not exists public_price_draft_state", "state_key varchar(64)", "revision bigint not null default 1", "unique key uk_public_price_draft_state_key (state_key)", "check (revision > 0 and is_deleted in (0, 1))", "-- porsche:seed-public-price-draft-state"} {
		if !strings.Contains(up, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
	down := strings.ToLower(string(ms[14].DownSQL))
	if strings.Count(down, "drop table if exists public_price_draft_state") != 1 || strings.Contains(up, "foreign key") {
		t.Fatal("unsafe/missing down")
	}
}

func TestPublicPriceDraftStateMigrationRealMySQLDownAndReapply(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit isolated TEST_DATABASE_URL MySQL fixture; .env is never read")
	}
	db := permissionSchemaDB(t)
	generator := persistence.NewSnowflake(43, persistence.SystemClock())
	now := int64(1_900_000_000_123)
	if err := Up(context.Background(), db, generator.Next, func() int64 { return now }); err != nil {
		t.Fatal(err)
	}
	migration := publicPriceDraftStateMigration(t)
	if err := VerifyPublicPriceDraftStateSchema(context.Background(), db); err != nil {
		t.Fatalf("verify 0013 after apply: %v", err)
	}
	var dependents int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.key_column_usage WHERE referenced_table_schema=DATABASE() AND referenced_table_name='public_price_draft_state'").Scan(&dependents).Error; err != nil {
		t.Fatal(err)
	}
	if dependents != 0 {
		t.Fatalf("0013 down has %d FK dependents", dependents)
	}
	if err := executePublicContentPricingSQL(db, migration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := setFixtureMigrationActive(db, migration.Version, false); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPublicPriceDraftStateSchema(context.Background(), db); err != ErrPublicPriceDraftStateSchema {
		t.Fatalf("verify accepted down schema: %v", err)
	}
	if err := Verify(context.Background(), db); err == nil {
		t.Fatal("global verifier accepted missing 0013")
	}
	if err := Up(context.Background(), db, generator.Next, func() int64 { return now }); err != nil {
		t.Fatalf("reapply 0013: %v", err)
	}
	if err := VerifyPublicPriceDraftStateSchema(context.Background(), db); err != nil {
		t.Fatalf("verify reapplied 0013: %v", err)
	}
	if err := Verify(context.Background(), db); err != nil {
		t.Fatalf("global verify after 0013 reapply: %v", err)
	}
	assertPublicContentPricingLedger(t, db, migration, true)
	var count, revision, guid, createdAt int64
	if err := db.Raw("SELECT COUNT(*),MIN(revision),MIN(guid),MIN(created_at) FROM public_price_draft_state WHERE BINARY state_key=BINARY 'pricing' AND is_deleted=0").Row().Scan(&count, &revision, &guid, &createdAt); err != nil {
		t.Fatal(err)
	}
	if count != 1 || revision != 1 || guid <= 0 || createdAt != now {
		t.Fatalf("singleton=%d revision=%d guid=%d created_at=%d", count, revision, guid, createdAt)
	}
}

func publicPriceDraftStateMigration(t *testing.T) Migration {
	t.Helper()
	ms, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 19 || ms[12].Version != "0013" {
		t.Fatalf("All()=%#v, want 0013 last", ms)
	}
	return ms[12]
}
