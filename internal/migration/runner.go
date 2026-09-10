// Package migration applies Porsche's embedded, forward-only MySQL schema.
package migration

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

//go:embed sql/0001_initial_schema.up.sql
var initialSchemaUp []byte

//go:embed sql/0001_initial_schema.down.sql
var initialSchemaDown []byte

//go:embed sql/0002_auth_core.up.sql
var authCoreUp []byte

//go:embed sql/0002_auth_core.down.sql
var authCoreDown []byte

//go:embed sql/0003_permission_policy.up.sql
var permissionPolicyUp []byte

//go:embed sql/0003_permission_policy.down.sql
var permissionPolicyDown []byte

//go:embed sql/0004_admin_users_read_count.up.sql
var adminUsersReadCountUp []byte

//go:embed sql/0004_admin_users_read_count.down.sql
var adminUsersReadCountDown []byte

//go:embed sql/0005_admin_operation_safety.up.sql
var adminOperationSafetyUp []byte

//go:embed sql/0005_admin_operation_safety.down.sql
var adminOperationSafetyDown []byte

//go:embed sql/0006_admin_action_outbox.up.sql
var adminActionOutboxUp []byte

//go:embed sql/0006_admin_action_outbox.down.sql
var adminActionOutboxDown []byte

//go:embed sql/0007_business_groups.up.sql
var businessGroupsUp []byte

//go:embed sql/0007_business_groups.down.sql
var businessGroupsDown []byte

//go:embed sql/0008_admin_operation_responses.up.sql
var adminOperationResponsesUp []byte

//go:embed sql/0008_admin_operation_responses.down.sql
var adminOperationResponsesDown []byte

//go:embed sql/0009_admin_response_integrity.up.sql
var adminResponseIntegrityUp []byte

//go:embed sql/0009_admin_response_integrity.down.sql
var adminResponseIntegrityDown []byte

//go:embed sql/0010_admin_operation_response_targets.up.sql
var adminOperationResponseTargetsUp []byte

//go:embed sql/0010_admin_operation_response_targets.down.sql
var adminOperationResponseTargetsDown []byte

//go:embed sql/0011_platform_generation_receipts.up.sql
var platformGenerationReceiptsUp []byte

//go:embed sql/0011_platform_generation_receipts.down.sql
var platformGenerationReceiptsDown []byte

//go:embed sql/0012_public_content_pricing.up.sql
var publicContentPricingUp []byte

//go:embed sql/0012_public_content_pricing.down.sql
var publicContentPricingDown []byte

//go:embed sql/0013_public_price_draft_state.up.sql
var publicPriceDraftStateUp []byte

//go:embed sql/0013_public_price_draft_state.down.sql
var publicPriceDraftStateDown []byte

//go:embed sql/0014_upstream_monitor_lease.up.sql
var upstreamMonitorLeaseUp []byte

//go:embed sql/0014_upstream_monitor_lease.down.sql
var upstreamMonitorLeaseDown []byte

//go:embed sql/0015_public_render_job_terminal.up.sql
var publicRenderJobTerminalUp []byte

//go:embed sql/0015_public_render_job_terminal.down.sql
var publicRenderJobTerminalDown []byte

//go:embed sql/0016_public_pricing_catalog_metadata.up.sql
var publicPricingCatalogMetadataUp []byte

//go:embed sql/0016_public_pricing_catalog_metadata.down.sql
var publicPricingCatalogMetadataDown []byte

//go:embed sql/0017_public_pricing_optional_prices.up.sql
var publicPricingOptionalPricesUp []byte

//go:embed sql/0017_public_pricing_optional_prices.down.sql
var publicPricingOptionalPricesDown []byte

// Migration is an immutable, embedded schema version.
type Migration struct {
	Version string
	UpSQL   []byte
	DownSQL []byte
}

// AppliedMigration records a version already present in the migration ledger.
type AppliedMigration struct {
	Version  string
	Checksum string
}

// All returns schema versions in application order.
func All() ([]Migration, error) {
	migrations := []Migration{
		{Version: "0001", UpSQL: initialSchemaUp, DownSQL: initialSchemaDown},
		{Version: "0002", UpSQL: authCoreUp, DownSQL: authCoreDown},
		{Version: "0003", UpSQL: permissionPolicyUp, DownSQL: permissionPolicyDown},
		{Version: "0004", UpSQL: adminUsersReadCountUp, DownSQL: adminUsersReadCountDown},
		{Version: "0005", UpSQL: adminOperationSafetyUp, DownSQL: adminOperationSafetyDown},
		{Version: "0006", UpSQL: adminActionOutboxUp, DownSQL: adminActionOutboxDown},
		{Version: "0007", UpSQL: businessGroupsUp, DownSQL: businessGroupsDown},
		{Version: "0008", UpSQL: adminOperationResponsesUp, DownSQL: adminOperationResponsesDown},
		{Version: "0009", UpSQL: adminResponseIntegrityUp, DownSQL: adminResponseIntegrityDown},
		{Version: "0010", UpSQL: adminOperationResponseTargetsUp, DownSQL: adminOperationResponseTargetsDown},
		{Version: "0011", UpSQL: platformGenerationReceiptsUp, DownSQL: platformGenerationReceiptsDown},
		{Version: "0012", UpSQL: publicContentPricingUp, DownSQL: publicContentPricingDown},
		{Version: "0013", UpSQL: publicPriceDraftStateUp, DownSQL: publicPriceDraftStateDown},
		{Version: "0014", UpSQL: upstreamMonitorLeaseUp, DownSQL: upstreamMonitorLeaseDown},
		{Version: "0015", UpSQL: publicRenderJobTerminalUp, DownSQL: publicRenderJobTerminalDown},
		{Version: "0016", UpSQL: publicPricingCatalogMetadataUp, DownSQL: publicPricingCatalogMetadataDown},
		{Version: "0017", UpSQL: publicPricingOptionalPricesUp, DownSQL: publicPricingOptionalPricesDown},
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

const createLedgerSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  version VARCHAR(64) NOT NULL,
  checksum CHAR(64) NOT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_schema_migrations_guid (guid),
  UNIQUE KEY uk_schema_migrations_version (version),
  KEY idx_schema_migrations_active (is_deleted, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

// Up runs missing forward migrations under a MySQL advisory lock. MySQL DDL
// implicitly commits, so correctness relies on idempotent CREATE statements,
// the lock, and a checksum-protected ledger rather than a pretend transaction.
func Up(ctx context.Context, db *gorm.DB, nextGUID func() int64, nowMillis func() int64) error {
	if db == nil || nextGUID == nil || nowMillis == nil {
		return fmt.Errorf("migration runner requires database, GUID generator, and clock")
	}
	// GET_LOCK is connection-scoped in MySQL. Connection pins every operation
	// through release to one physical connection, preventing a pool checkout
	// from silently losing the advisory lock between statements.
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		var lockAcquired int
		if err := conn.Raw("SELECT GET_LOCK('porsche_schema_migrations', 30)").Scan(&lockAcquired).Error; err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if lockAcquired != 1 {
			return fmt.Errorf("acquire migration lock: lock not acquired")
		}
		defer conn.WithContext(context.Background()).Exec("SELECT RELEASE_LOCK('porsche_schema_migrations')") //nolint:errcheck

		if err := conn.Exec(createLedgerSQL).Error; err != nil {
			return fmt.Errorf("create migration ledger: %w", err)
		}
		migrations, err := All()
		if err != nil {
			return err
		}
		for _, migration := range migrations {
			checksum := fmt.Sprintf("%x", sha256.Sum256(migration.UpSQL))
			var applied AppliedMigration
			err := conn.Raw("SELECT version, checksum FROM schema_migrations WHERE version = ? AND is_deleted = 0", migration.Version).Scan(&applied).Error
			if err != nil {
				return fmt.Errorf("read migration %s: %w", migration.Version, err)
			}
			if applied.Version != "" {
				if applied.Checksum != checksum {
					return fmt.Errorf("migration %s checksum mismatch", migration.Version)
				}
				if migration.Version == "0003" {
					if err := VerifyPermissionSchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0004" {
					if err := VerifyAdminUsersReadCountIndex(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0005" {
					if err := VerifyAdminOperationSafetySchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0007" {
					if err := VerifyBusinessGroupsSchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0010" {
					if err := VerifyAdminActionOutboxSchema(ctx, conn); err != nil {
						return err
					}
					if err := VerifyAdminOperationResponseSchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0011" {
					if err := VerifyPlatformGenerationReceiptSchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0012" {
					if err := VerifyPublicContentPricingSchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0013" {
					if err := VerifyPublicPriceDraftStateSchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0014" {
					if err := VerifyUpstreamMonitorLeaseSchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0015" {
					if err := VerifyPublicRenderJobTerminalSchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0016" {
					if err := VerifyPublicPricingCatalogMetadataSchema(ctx, conn); err != nil {
						return err
					}
				}
				if migration.Version == "0017" {
					if err := VerifyPublicPricingOptionalPricesSchema(ctx, conn); err != nil {
						return err
					}
				}
				continue
			}
			if migration.Version == "0007" {
				if err := applyBusinessGroupsMigration(conn, migration.UpSQL, nextGUID, nowMillis); err != nil {
					return fmt.Errorf("apply migration %s: %w", migration.Version, err)
				}
			} else if migration.Version == "0009" {
				if err := applyAdminResponseIntegrityMigration(conn, migration.UpSQL); err != nil {
					return fmt.Errorf("apply migration %s: %w", migration.Version, err)
				}
			} else if migration.Version == "0010" {
				if err := applyAdminOperationResponseTargetsMigration(conn, migration.UpSQL); err != nil {
					return fmt.Errorf("apply migration %s: %w", migration.Version, err)
				}
			} else if migration.Version == "0013" {
				parts := strings.Split(string(migration.UpSQL), "-- porsche:seed-public-price-draft-state")
				if len(parts) != 2 {
					return fmt.Errorf("apply migration 0013: invalid seed marker")
				}
				statements := splitStatements(parts[0])
				if len(statements) != 1 {
					return fmt.Errorf("apply migration 0013: invalid statement count")
				}
				if err := conn.Exec(statements[0]).Error; err != nil {
					return fmt.Errorf("apply migration 0013: %w", err)
				}
				now := nowMillis()
				if err := conn.Exec("INSERT INTO public_price_draft_state (guid,state_key,revision,created_at,updated_at,is_deleted) VALUES (?,'pricing',1,?,?,0) ON DUPLICATE KEY UPDATE state_key=VALUES(state_key)", nextGUID(), now, now).Error; err != nil {
					return fmt.Errorf("apply migration 0013: %w", err)
				}
			} else if migration.Version == "0014" {
				parts := strings.Split(string(migration.UpSQL), "-- porsche:seed-upstream-monitor-lease")
				if len(parts) != 2 {
					return fmt.Errorf("apply migration 0014: invalid seed marker")
				}
				statements := splitStatements(parts[0])
				if len(statements) != 1 {
					return fmt.Errorf("apply migration 0014: invalid statement count")
				}
				if err := conn.Exec(statements[0]).Error; err != nil {
					return fmt.Errorf("apply migration 0014: %w", err)
				}
				now := nowMillis()
				if err := conn.Exec("INSERT INTO upstream_monitor_leases (guid,lease_key,owner_token,lease_expires_at,revision,created_at,updated_at,is_deleted) VALUES (?,'catalog',NULL,0,1,?,?,0) ON DUPLICATE KEY UPDATE lease_key=VALUES(lease_key)", nextGUID(), now, now).Error; err != nil {
					return fmt.Errorf("apply migration 0014: %w", err)
				}
			} else {
				for _, statement := range splitStatements(string(migration.UpSQL)) {
					if err := conn.Exec(statement).Error; err != nil {
						return fmt.Errorf("apply migration %s: %w", migration.Version, err)
					}
				}
			}
			if migration.Version == "0010" {
				if err := VerifyAdminActionOutboxSchema(ctx, conn); err != nil {
					return err
				}
				if err := VerifyAdminOperationResponseSchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0003" {
				if err := VerifyPermissionSchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0004" {
				if err := VerifyAdminUsersReadCountIndex(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0005" {
				if err := VerifyAdminOperationSafetySchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0007" {
				if err := VerifyBusinessGroupsSchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0011" {
				if err := VerifyPlatformGenerationReceiptSchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0012" {
				if err := VerifyPublicContentPricingSchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0013" {
				if err := VerifyPublicPriceDraftStateSchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0014" {
				if err := VerifyUpstreamMonitorLeaseSchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0015" {
				if err := VerifyPublicRenderJobTerminalSchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0016" {
				if err := VerifyPublicPricingCatalogMetadataSchema(ctx, conn); err != nil {
					return err
				}
			}
			if migration.Version == "0017" {
				if err := VerifyPublicPricingOptionalPricesSchema(ctx, conn); err != nil {
					return err
				}
			}
			now := nowMillis()
			if err := conn.Exec(
				"INSERT INTO schema_migrations (guid, version, checksum, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, ?, 0) ON DUPLICATE KEY UPDATE checksum=VALUES(checksum),updated_at=VALUES(updated_at),updated_by=NULL,is_deleted=0",
				nextGUID(), migration.Version, checksum, now, now,
			).Error; err != nil {
				return fmt.Errorf("record migration %s: %w", migration.Version, err)
			}
		}
		return nil
	})
}

// Status returns the applied migration ledger without changing database state.
func Status(ctx context.Context, db *gorm.DB) ([]AppliedMigration, error) {
	if db == nil {
		return nil, fmt.Errorf("migration runner requires database")
	}
	var status []AppliedMigration
	if err := db.WithContext(ctx).Raw("SELECT version, checksum FROM schema_migrations WHERE is_deleted = 0 ORDER BY version").Scan(&status).Error; err != nil {
		return nil, err
	}
	return status, nil
}

// Verify checks that every embedded migration is present exactly once with its
// immutable checksum. The server uses this fail-closed check before serving
// requests; applying migrations remains an explicit deploy operation.
func Verify(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("migration runner requires database")
	}
	status, err := Status(ctx, db)
	if err != nil {
		return fmt.Errorf("read migration status: %w", err)
	}
	migrations, err := All()
	if err != nil {
		return err
	}
	if err := VerifyApplied(migrations, status); err != nil {
		return err
	}
	if err := VerifyPermissionSchema(ctx, db); err != nil {
		return err
	}
	if err := VerifyAdminUsersReadCountIndex(ctx, db); err != nil {
		return err
	}
	if err := VerifyAdminOperationSafetySchema(ctx, db); err != nil {
		return err
	}
	if err := VerifyAdminActionOutboxSchema(ctx, db); err != nil {
		return err
	}
	if err := VerifyBusinessGroupsSchema(ctx, db); err != nil {
		return err
	}
	if err := VerifyAdminOperationResponseSchema(ctx, db); err != nil {
		return err
	}
	if err := VerifyPlatformGenerationReceiptSchema(ctx, db); err != nil {
		return err
	}
	if err := VerifyPublicContentPricingSchema(ctx, db); err != nil {
		return err
	}
	if err := VerifyPublicPriceDraftStateSchema(ctx, db); err != nil {
		return err
	}
	if err := VerifyUpstreamMonitorLeaseSchema(ctx, db); err != nil {
		return err
	}
	return VerifyPublicRenderJobTerminalSchema(ctx, db)
}

// VerifyApplied is the side-effect-free portion of Verify, kept separate so
// checksum and completeness behavior can be unit-tested without a database.
func VerifyApplied(migrations []Migration, applied []AppliedMigration) error {
	if len(applied) != len(migrations) {
		return fmt.Errorf("database schema is not fully migrated")
	}
	byVersion := make(map[string]string, len(applied))
	for _, entry := range applied {
		if entry.Version == "" || entry.Checksum == "" {
			return fmt.Errorf("database schema has invalid migration ledger")
		}
		if _, duplicate := byVersion[entry.Version]; duplicate {
			return fmt.Errorf("database schema has duplicate migration version")
		}
		byVersion[entry.Version] = entry.Checksum
	}
	for _, migration := range migrations {
		checksum := fmt.Sprintf("%x", sha256.Sum256(migration.UpSQL))
		if byVersion[migration.Version] != checksum {
			return fmt.Errorf("migration %s checksum mismatch or missing", migration.Version)
		}
	}
	return nil
}

func splitStatements(sql string) []string {
	parts := strings.Split(sql, ";")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		if statement := strings.TrimSpace(part); statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}
