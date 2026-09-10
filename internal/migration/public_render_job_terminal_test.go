package migration

import (
	"context"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"os"
	"strings"
	"testing"
)

func TestPublicRenderJobTerminalMigrationContract(t *testing.T) {
	ms, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 15 || ms[14].Version != "0015" {
		t.Fatalf("migration tail=%#v", ms)
	}
	up := strings.ToLower(string(ms[14].UpSQL))
	down := strings.ToLower(string(ms[14].DownSQL))
	for _, fragment := range []string{"alter table public_render_jobs", "last_terminal_owner_hmac char(64)", "last_terminal_fence int", "last_terminal_operation int", "last_terminal_state int", "chk_public_render_jobs_terminal"} {
		if !strings.Contains(up, fragment) {
			t.Errorf("up missing %q", fragment)
		}
	}
	if strings.Contains(up, "owner_token") || !strings.Contains(down, "drop column last_terminal_owner_hmac") {
		t.Fatalf("unsafe migration up/down")
	}
}

func TestPublicRenderJobTerminalMigrationRealMySQLDownAndReapply(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit isolated TEST_DATABASE_URL MySQL fixture; .env is never read")
	}
	db := permissionSchemaDB(t)
	generator := persistence.NewSnowflake(47, persistence.SystemClock())
	now := int64(1_900_000_400_000)
	if err := Up(context.Background(), db, generator.Next, func() int64 { return now }); err != nil {
		t.Fatal(err)
	}
	ms, _ := All()
	m := ms[14]
	if err := VerifyPublicRenderJobTerminalSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := executePublicContentPricingSQL(db, m.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := setFixtureMigrationActive(db, m.Version, false); err != nil {
		t.Fatal(err)
	}
	if err := executePublicContentPricingSQL(db, m.UpSQL); err != nil {
		t.Fatal(err)
	}
	if err := setFixtureMigrationActive(db, m.Version, true); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPublicRenderJobTerminalSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}
