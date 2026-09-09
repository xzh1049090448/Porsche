package migration

import (
	"context"
	"crypto/sha256"
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
		"idx_public_publication_state_price_snapshot (price_snapshot_id)",
		"idx_public_publication_state_content_release (content_release_id)",
		"idx_public_content_drafts_document_revision (document_kind, revision, is_deleted)",
		"idx_upstream_model_observations_observed_at (observed_at, is_deleted)",
		"idx_root_alerts_active (state, is_deleted, updated_at)",
		"idx_root_alert_receipts_root_unread (root_user_id, is_deleted, read_at)",
		"idx_public_render_jobs_lease (state, is_deleted, lease_expires_at)",
		"idx_public_render_jobs_content_release (content_release_id)",
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
	migration := publicContentPricingMigration(t)
	if err := Up(context.Background(), db, generator.Next, func() int64 { return 1_900_000_000_000 }); err != nil {
		t.Fatal(err)
	}
	assertPublicContentPricingRealSchema(t, db, migration)
	if err := VerifyPublicContentPricingSchema(context.Background(), db); err != nil {
		t.Fatalf("verify 0012 immediately after apply: %v", err)
	}
	if err := Verify(context.Background(), db); err != nil {
		t.Fatalf("global verify immediately after apply: %v", err)
	}
	if err := executePublicContentPricingSQL(db, migration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := setFixtureMigrationActive(db, migration.Version, false); err != nil {
		t.Fatal(err)
	}
	assertPublicContentPricingTablesAbsent(t, db)
	assertPublicContentPricingLedger(t, db, migration, false)
	if err := executePublicContentPricingSQL(db, migration.UpSQL); err != nil {
		t.Fatal(err)
	}
	if err := setFixtureMigrationActive(db, migration.Version, true); err != nil {
		t.Fatal(err)
	}
	assertPublicContentPricingRealSchema(t, db, migration)
	if err := Up(context.Background(), db, generator.Next, func() int64 { return 1_900_000_000_000 }); err != nil {
		t.Fatalf("rerun with active 0012 ledger entry: %v", err)
	}
	if err := Verify(context.Background(), db); err != nil {
		t.Fatalf("global verify after active-ledger rerun: %v", err)
	}
}

func TestUpRejectsInterruptedPublicContentPricingCreateWithActiveLedger(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit isolated TEST_DATABASE_URL MySQL fixture")
	}
	db := permissionSchemaDB(t)
	generator := persistence.NewSnowflake(42, persistence.SystemClock())
	migration := publicContentPricingMigration(t)
	if err := Up(context.Background(), db, generator.Next, func() int64 { return 1_900_000_000_000 }); err != nil {
		t.Fatal(err)
	}
	if err := executePublicContentPricingSQL(db, migration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := setFixtureMigrationActive(db, migration.Version, false); err != nil {
		t.Fatal(err)
	}
	statements := splitStatements(string(migration.UpSQL))
	if len(statements) == 0 {
		t.Fatal("0012 has no create statements")
	}
	if err := db.Exec(statements[0]).Error; err != nil {
		t.Fatal(err)
	}
	if err := setFixtureMigrationActive(db, migration.Version, true); err != nil {
		t.Fatal(err)
	}
	if err := Up(context.Background(), db, generator.Next, func() int64 { return 1_900_000_000_000 }); err != ErrPublicContentPricingSchema {
		t.Fatalf("Up with interrupted 0012 and checksum-correct ledger = %v, want %v", err, ErrPublicContentPricingSchema)
	}
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
	if len(migrations) != 13 || migrations[11].Version != "0012" || migrations[12].Version != "0013" {
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

func assertPublicContentPricingTablesAbsent(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range publicContentPricingTables {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("table %s remains after 0012 down", table)
		}
	}
}

func assertPublicContentPricingRealSchema(t *testing.T, db *gorm.DB, migration Migration) {
	t.Helper()
	assertPublicContentPricingTables(t, db)
	for _, column := range []struct{ table, column string }{
		{"public_model_configs", "input_price_usd_per_million_tokens"},
		{"public_model_configs", "output_price_usd_per_million_tokens"},
		{"public_price_snapshot_items", "input_price_usd_per_million_tokens"},
		{"public_price_snapshot_items", "output_price_usd_per_million_tokens"},
		{"upstream_model_observations", "input_price_usd_per_million_tokens"},
		{"upstream_model_observations", "output_price_usd_per_million_tokens"},
	} {
		var columnType string
		if err := db.Raw("SELECT column_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", column.table, column.column).Row().Scan(&columnType); err != nil {
			t.Fatal(err)
		}
		if strings.ToLower(columnType) != "decimal(20,8)" {
			t.Errorf("%s.%s type = %q, want decimal(20,8)", column.table, column.column, columnType)
		}
	}
	for _, index := range []struct {
		table, name string
		unique      bool
		columns     []string
	}{
		{"public_model_configs", "uk_public_model_configs_model_key", true, []string{"model_key"}},
		{"public_model_configs", "uk_public_model_configs_upstream_model_id", true, []string{"upstream_model_id"}},
		{"public_model_configs", "idx_public_model_configs_status_revision", false, []string{"status", "is_deleted", "revision"}},
		{"public_model_configs", "idx_public_model_configs_provider_active", false, []string{"provider", "is_deleted", "display_name"}},
		{"public_price_snapshots", "idx_public_price_snapshots_version", false, []string{"version", "is_deleted"}},
		{"public_price_snapshot_items", "idx_public_price_snapshot_items_model", false, []string{"model_config_id", "is_deleted"}},
		{"public_content_drafts", "idx_public_content_drafts_document_revision", false, []string{"document_kind", "revision", "is_deleted"}},
		{"public_content_releases", "idx_public_content_releases_document_version", false, []string{"document_kind", "version", "is_deleted"}},
		{"public_publication_state", "idx_public_publication_state_revision", false, []string{"revision", "is_deleted"}},
		{"public_publication_state", "idx_public_publication_state_price_snapshot", false, []string{"price_snapshot_id"}},
		{"public_publication_state", "idx_public_publication_state_content_release", false, []string{"content_release_id"}},
		{"upstream_model_observations", "idx_upstream_model_observations_model_observed", false, []string{"upstream_model_id", "is_deleted", "observed_at"}},
		{"root_alerts", "idx_root_alerts_active", false, []string{"state", "is_deleted", "updated_at"}},
		{"root_alert_receipts", "idx_root_alert_receipts_root_unread", false, []string{"root_user_id", "is_deleted", "read_at"}},
		{"public_render_jobs", "idx_public_render_jobs_lease", false, []string{"state", "is_deleted", "lease_expires_at"}},
		{"public_render_jobs", "idx_public_render_jobs_content_release", false, []string{"content_release_id"}},
	} {
		assertPublicContentPricingIndex(t, db, index.table, index.name, index.unique, index.columns)
	}
	for _, foreignKey := range []struct {
		table, name, target string
	}{
		{"public_price_snapshots", "fk_public_price_snapshots_restore", "public_price_snapshots"},
		{"public_price_snapshot_items", "fk_public_price_snapshot_items_snapshot", "public_price_snapshots"},
		{"public_price_snapshot_items", "fk_public_price_snapshot_items_model", "public_model_configs"},
		{"public_content_releases", "fk_public_content_releases_restore", "public_content_releases"},
		{"public_publication_state", "fk_public_publication_state_price", "public_price_snapshots"},
		{"public_publication_state", "fk_public_publication_state_content", "public_content_releases"},
		{"root_alerts", "fk_root_alerts_model", "public_model_configs"},
		{"root_alert_receipts", "fk_root_alert_receipts_alert", "root_alerts"},
		{"root_alert_receipts", "fk_root_alert_receipts_root", "users"},
		{"public_render_jobs", "fk_public_render_jobs_price", "public_price_snapshots"},
		{"public_render_jobs", "fk_public_render_jobs_content", "public_content_releases"},
	} {
		assertPublicContentPricingForeignKey(t, db, foreignKey.table, foreignKey.name, foreignKey.target)
	}
	assertPublicContentPricingLedger(t, db, migration, true)
}

func assertPublicContentPricingIndex(t *testing.T, db *gorm.DB, table, name string, unique bool, wantColumns []string) {
	t.Helper()
	rows, err := db.Raw("SELECT column_name, non_unique FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? ORDER BY seq_in_index", table, name).Rows()
	if err != nil {
		t.Fatalf("read index %s.%s: %v", table, name, err)
	}
	defer rows.Close()
	var columns []string
	nonUnique := -1
	for rows.Next() {
		var column string
		if err := rows.Scan(&column, &nonUnique); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(columns) == 0 {
		t.Fatalf("index %s.%s is missing", table, name)
	}
	if unique && nonUnique != 0 {
		t.Errorf("%s.%s non_unique = %d, want 0", table, name, nonUnique)
	}
	if !unique && nonUnique != 1 {
		t.Errorf("%s.%s non_unique = %d, want 1", table, name, nonUnique)
	}
	if len(columns) != len(wantColumns) {
		t.Errorf("%s.%s columns = %v, want %v", table, name, columns, wantColumns)
		return
	}
	for index := range wantColumns {
		if columns[index] != wantColumns[index] {
			t.Errorf("%s.%s column %d = %q, want %q", table, name, index+1, columns[index], wantColumns[index])
		}
	}
}

func assertPublicContentPricingForeignKey(t *testing.T, db *gorm.DB, table, name, target string) {
	t.Helper()
	var found string
	if err := db.Raw("SELECT referenced_table_name FROM information_schema.key_column_usage WHERE table_schema = DATABASE() AND table_name = ? AND constraint_name = ? AND referenced_table_name IS NOT NULL LIMIT 1", table, name).Row().Scan(&found); err != nil {
		t.Fatalf("read foreign key %s.%s: %v", table, name, err)
	}
	if found != target {
		t.Errorf("foreign key %s.%s target = %q, want %q", table, name, found, target)
	}
}

func assertPublicContentPricingLedger(t *testing.T, db *gorm.DB, migration Migration, active bool) {
	t.Helper()
	var checksum string
	var isDeleted int
	if err := db.Raw("SELECT checksum, is_deleted FROM schema_migrations WHERE version = ?", migration.Version).Row().Scan(&checksum, &isDeleted); err != nil {
		t.Fatalf("read %s migration ledger: %v", migration.Version, err)
	}
	wantChecksum := fmt.Sprintf("%x", sha256.Sum256(migration.UpSQL))
	if checksum != wantChecksum {
		t.Errorf("%s checksum = %q, want %q", migration.Version, checksum, wantChecksum)
	}
	wantDeleted := 1
	if active {
		wantDeleted = 0
	}
	if isDeleted != wantDeleted {
		t.Errorf("%s is_deleted = %d, want %d", migration.Version, isDeleted, wantDeleted)
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
