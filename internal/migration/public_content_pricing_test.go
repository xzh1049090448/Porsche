package migration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

func TestPublicContentPricingMigrationContract(t *testing.T) {
	migration := publicContentPricingMigration(t)
	up := strings.ToLower(string(migration.UpSQL))

	for _, table := range []string{
		"public_model_configs",
		"public_price_snapshots",
		"public_price_snapshot_items",
		"public_publication_state",
		"public_content_drafts",
		"public_content_releases",
		"upstream_model_observations",
		"root_alerts",
		"root_alert_receipts",
		"public_render_jobs",
	} {
		if !strings.Contains(up, "create table if not exists "+table) {
			t.Errorf("0012 missing table %q", table)
		}
	}
	for _, fragment := range []string{
		"input_price_usd_per_million_tokens decimal(20,8)",
		"output_price_usd_per_million_tokens decimal(20,8)",
		"unique key uk_public_model_configs_model_key (model_key)",
		"unique key uk_public_model_configs_upstream_model_id (upstream_model_id)",
		"idx_public_model_configs_status_revision (status, is_deleted, revision)",
		"idx_public_price_snapshots_version (version, is_deleted)",
		"idx_public_content_drafts_document_revision (document_kind, revision, is_deleted)",
		"idx_upstream_model_observations_observed_at (observed_at, is_deleted)",
		"idx_root_alerts_active (state, is_deleted, updated_at)",
		"idx_root_alert_receipts_root_unread (root_user_id, is_deleted, read_at)",
		"idx_public_render_jobs_lease (state, is_deleted, lease_expires_at)",
		"foreign key (snapshot_id) references public_price_snapshots(id)",
		"foreign key (price_snapshot_id) references public_price_snapshots(id)",
		"foreign key (content_release_id) references public_content_releases(id)",
		"foreign key (alert_id) references root_alerts(id)",
		"foreign key (root_user_id) references users(id)",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0012 missing contract fragment %q", fragment)
		}
	}
	for _, table := range []string{
		"public_model_configs", "public_price_snapshots", "public_price_snapshot_items", "public_publication_state", "public_content_drafts", "public_content_releases", "upstream_model_observations", "root_alerts", "root_alert_receipts", "public_render_jobs",
	} {
		for _, column := range []string{"guid bigint not null", "created_at bigint not null", "created_by bigint null", "updated_at bigint not null", "updated_by bigint null", "is_deleted int not null default 0"} {
			if !tableHasColumn(up, table, column) {
				t.Errorf("%s missing audit/identity column %q", table, column)
			}
		}
	}
}

func TestPublicContentPricingMigrationDownDropsChildrenBeforeParents(t *testing.T) {
	migration := publicContentPricingMigration(t)
	down := strings.ToLower(string(migration.DownSQL))
	order := []string{
		"drop table if exists root_alert_receipts",
		"drop table if exists public_render_jobs",
		"drop table if exists public_publication_state",
		"drop table if exists public_content_releases",
		"drop table if exists public_content_drafts",
		"drop table if exists public_price_snapshot_items",
		"drop table if exists public_price_snapshots",
		"drop table if exists root_alerts",
		"drop table if exists upstream_model_observations",
		"drop table if exists public_model_configs",
	}
	previous := -1
	for _, drop := range order {
		position := strings.Index(down, drop)
		if position < 0 {
			t.Fatalf("0012 down missing %q", drop)
		}
		if position < previous {
			t.Fatalf("0012 down order is unsafe: %q occurs before its child", drop)
		}
		previous = position
	}
}

func TestPublicContentPricingMigrationRealMySQL(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit isolated TEST_DATABASE_URL MySQL fixture")
	}
	db := permissionSchemaDB(t)
	generator := persistence.NewSnowflake(41, persistence.SystemClock())
	if err := Up(context.Background(), db, generator.Next, func() int64 { return 1_900_000_000_000 }); err != nil {
		t.Fatal(err)
	}
	assertPublicContentPricingTables(t, db)
	migration := publicContentPricingMigration(t)
	if err := executePublicContentPricingSQL(db, migration.DownSQL); err != nil {
		t.Fatal(err)
	}
	for _, table := range publicContentPricingTables {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("table %s remains after 0012 down", table)
		}
	}
	if err := executePublicContentPricingSQL(db, migration.UpSQL); err != nil {
		t.Fatal(err)
	}
	assertPublicContentPricingTables(t, db)
}

var publicContentPricingTables = []string{
	"public_model_configs", "public_price_snapshots", "public_price_snapshot_items", "public_publication_state", "public_content_drafts", "public_content_releases", "upstream_model_observations", "root_alerts", "root_alert_receipts", "public_render_jobs",
}

func publicContentPricingMigration(t *testing.T) Migration {
	t.Helper()
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 12 || migrations[11].Version != "0012" {
		t.Fatalf("All() = %#v, want migration 0012 last", migrations)
	}
	return migrations[11]
}

func tableHasColumn(sql, table, column string) bool {
	start := strings.Index(sql, "create table if not exists "+table)
	if start < 0 {
		return false
	}
	rest := sql[start:]
	end := strings.Index(rest, ") engine=")
	return end >= 0 && strings.Contains(rest[:end], column)
}

func assertPublicContentPricingTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range publicContentPricingTables {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
}

func executePublicContentPricingSQL(db *gorm.DB, raw []byte) error {
	for index, statement := range splitStatements(string(raw)) {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("statement %d: %w", index+1, err)
		}
	}
	return nil
}
