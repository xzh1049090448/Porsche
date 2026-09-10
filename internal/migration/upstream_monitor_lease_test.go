package migration

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/persistence"
)

func TestUpstreamMonitorLeaseMigrationContract(t *testing.T) {
	ms, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 15 || ms[13].Version != "0014" {
		t.Fatalf("migration tail=%d/%v", len(ms), ms)
	}
	up := strings.ToLower(string(ms[13].UpSQL))
	for _, fragment := range []string{"create table if not exists upstream_monitor_leases", "lease_key varchar(64)", "owner_token char(64)", "lease_expires_at bigint", "revision bigint not null default 1", "unique key uk_upstream_monitor_leases_key (lease_key)", "check (lease_expires_at >= 0 and revision > 0 and is_deleted in (0, 1))", "-- porsche:seed-upstream-monitor-lease"} {
		if !strings.Contains(up, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
	if down := strings.ToLower(string(ms[13].DownSQL)); strings.Count(down, "drop table if exists upstream_monitor_leases") != 1 {
		t.Fatal("unsafe/missing down")
	}
}

func TestUpstreamMonitorLeaseMigrationRealMySQLDownAndReapply(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit isolated TEST_DATABASE_URL MySQL fixture; .env is never read")
	}
	db := permissionSchemaDB(t)
	generator := persistence.NewSnowflake(44, persistence.SystemClock())
	now := int64(1_900_000_000_456)
	if err := Up(context.Background(), db, generator.Next, func() int64 { return now }); err != nil {
		t.Fatal(err)
	}
	ms, _ := All()
	migration := ms[13]
	if err := VerifyUpstreamMonitorLeaseSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := executePublicContentPricingSQL(db, migration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := setFixtureMigrationActive(db, migration.Version, false); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUpstreamMonitorLeaseSchema(context.Background(), db); err != ErrUpstreamMonitorLeaseSchema {
		t.Fatalf("verify accepted down schema: %v", err)
	}
	if err := Verify(context.Background(), db); err == nil {
		t.Fatal("global verifier accepted missing 0014")
	}
	if err := Up(context.Background(), db, generator.Next, func() int64 { return now }); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUpstreamMonitorLeaseSchema(context.Background(), db); err != nil {
		t.Fatalf("dedicated verify after 0014 reapply: %v", err)
	}
	if err := Verify(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	assertPublicContentPricingLedger(t, db, migration, true)
}
