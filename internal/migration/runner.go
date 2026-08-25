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
	migrations := []Migration{{Version: "0001", UpSQL: initialSchemaUp, DownSQL: initialSchemaDown}}
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
				continue
			}
			for _, statement := range splitStatements(string(migration.UpSQL)) {
				if err := conn.Exec(statement).Error; err != nil {
					return fmt.Errorf("apply migration %s: %w", migration.Version, err)
				}
			}
			now := nowMillis()
			if err := conn.Exec(
				"INSERT INTO schema_migrations (guid, version, checksum, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, ?, 0)",
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
	return VerifyApplied(migrations, status)
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
