package migration

import (
	"context"
	"errors"
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
	if len(ms) != 19 || ms[16].Version != "0017" {
		t.Fatalf("migration tail=%#v", ms)
	}
	up := strings.ToLower(string(ms[16].UpSQL))
	down := strings.ToLower(string(ms[16].DownSQL))
	for _, fragment := range []string{"alter table public_render_jobs", "last_terminal_owner_hmac char(64)", "last_terminal_fence int", "last_terminal_operation int", "last_terminal_state int", "chk_public_render_jobs_terminal"} {
		if !strings.Contains(up, fragment) {
			t.Errorf("up missing %q", fragment)
		}
	}
	if strings.Contains(up, "owner_token") || !strings.Contains(down, "drop column last_terminal_owner_hmac") {
		t.Fatalf("unsafe migration up/down")
	}
	source, err := os.ReadFile("public_render_job_terminal.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, fragment := range []string{"check_constraints", "check_clause", "column_default is null", "normalizePublicRenderTerminalCheck"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(fragment)) {
			t.Errorf("verifier source missing %q", fragment)
		}
	}
}

func TestPublicRenderTerminalCheckMatchesMySQLAtomicParentheses(t *testing.T) {
	mysqlClause := "(((`last_terminal_owner_hmac` is null) and (`last_terminal_fence` is null) and (`last_terminal_operation` is null) and (`last_terminal_state` is null)) or ((`last_terminal_owner_hmac` is not null) and (`last_terminal_fence` > 0) and (`last_terminal_operation` in (1,2)) and (`last_terminal_state` in (1,3,4))))"
	if !publicRenderTerminalCheckMatches(mysqlClause) {
		t.Fatal("MySQL canonical clause was rejected")
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
	if err := VerifyPublicRenderJobTerminalSchema(context.Background(), db); !errors.Is(err, ErrPublicRenderJobTerminalMigration) {
		t.Fatalf("down terminal verify=%v", err)
	}
	if err := Verify(context.Background(), db); err == nil {
		t.Fatal("global verify accepted inactive/down 0015")
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
	if err := Verify(context.Background(), db); err != nil {
		t.Fatalf("global verify after reapply: %v", err)
	}
	assertPublicContentPricingLedger(t, db, m, true)
	if err := db.Exec("ALTER TABLE public_render_jobs DROP CHECK chk_public_render_jobs_terminal, ADD CONSTRAINT chk_public_render_jobs_terminal CHECK (last_terminal_fence IS NULL)").Error; err != nil {
		t.Fatal(err)
	}
	restoreCheck := func() {
		_ = db.Exec("ALTER TABLE public_render_jobs DROP CHECK chk_public_render_jobs_terminal, ADD CONSTRAINT chk_public_render_jobs_terminal CHECK ((last_terminal_owner_hmac IS NULL AND last_terminal_fence IS NULL AND last_terminal_operation IS NULL AND last_terminal_state IS NULL) OR (last_terminal_owner_hmac IS NOT NULL AND last_terminal_fence > 0 AND last_terminal_operation IN (1,2) AND last_terminal_state IN (1,3,4)))").Error
	}
	t.Cleanup(restoreCheck)
	if err := VerifyPublicRenderJobTerminalSchema(context.Background(), db); !errors.Is(err, ErrPublicRenderJobTerminalMigration) {
		t.Fatalf("weakened check verify=%v", err)
	}
	if err := Verify(context.Background(), db); err == nil {
		t.Fatal("global verify accepted weakened check")
	}
	restoreCheck()
	if err := Verify(context.Background(), db); err != nil {
		t.Fatalf("global verify after semantic restore: %v", err)
	}
}
