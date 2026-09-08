package migration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

// permissionSchemaDB is a test-owned child database. Every destructive case
// below is confined to this child; the caller-provided fixture is never changed.
func permissionSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if raw == "" {
		t.Skip("requires isolated TEST_DATABASE_URL MySQL fixture")
	}
	if !isTestDatabaseURL(raw) {
		t.Fatal("requires explicit dedicated TEST_DATABASE_URL")
	}
	parent, err := db.Open(raw, "test")
	if err != nil {
		t.Fatal(err)
	}
	parentSQL, err := parent.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parentSQL.Close() })
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "porsche_permission_" + hex.EncodeToString(token[:]) + "_test"
	if err := parent.Exec("CREATE DATABASE `" + name + "`").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := parent.Exec("DROP DATABASE `" + name + "`").Error; err != nil {
			t.Errorf("drop owned database: %v", err)
		}
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Path, u.RawPath = "/"+name, ""
	child, err := db.Open(u.String(), "test")
	if err != nil {
		t.Fatal(err)
	}
	childSQL, err := child.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = childSQL.Close() })
	return child
}

func permissionUp(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	g := persistence.NewSnowflake(19, persistence.SystemClock())
	if err := Up(context.Background(), gdb, g.Next, func() int64 { return 1_700_000_000_000 }); err != nil {
		t.Fatal(err)
	}
}

func applyThroughAuthCore(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := gdb.Exec(createLedgerSQL).Error; err != nil {
		t.Fatal(err)
	}
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range all[:2] {
		for _, statement := range splitStatements(string(m.UpSQL)) {
			if err := gdb.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(m.UpSQL))
		if err := gdb.Exec("INSERT INTO schema_migrations (guid,version,checksum,created_at,updated_at,is_deleted) VALUES (?,?,?,?,?,0)", 100+i, m.Version, checksum, 1, 1).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPermissionSchemaMigratesAuthDataAndReruns(t *testing.T) {
	gdb := permissionSchemaDB(t)
	applyThroughAuthCore(t, gdb)
	if err := gdb.Exec("INSERT INTO users (guid,username,password_hash,allowed_models,created_at,updated_at,is_deleted) VALUES (?,?,?,?,?,?,0)", 101, "permission-preserved", "hash", "[]", 1, 1).Error; err != nil {
		t.Fatal(err)
	}
	permissionUp(t, gdb)
	var users int64
	if err := gdb.Raw("SELECT COUNT(*) FROM users WHERE username='permission-preserved'").Scan(&users).Error; err != nil || users != 1 {
		t.Fatalf("0003 changed 0002 user data: count=%d err=%v", users, err)
	}
	if err := Verify(context.Background(), gdb); err != nil {
		t.Fatal(err)
	}
	permissionUp(t, gdb)
	if err := Verify(context.Background(), gdb); err != nil {
		t.Fatalf("rerun verify: %v", err)
	}
	var n int64
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw("SELECT COUNT(*) FROM schema_migrations WHERE is_deleted=0").Scan(&n).Error; err != nil || n != int64(len(migrations)) {
		t.Fatalf("ledger = %d, %v; want %d", n, err, len(migrations))
	}
}

func TestPermissionSchemaRejectsDrift(t *testing.T) {
	for _, tc := range []struct {
		name string
		ddl  []string
	}{
		{"nullable", []string{"ALTER TABLE user_permission_heads MODIFY rule_count INT NULL"}},
		{"unsigned", []string{"ALTER TABLE user_permission_heads MODIFY policy_version BIGINT UNSIGNED NOT NULL"}},
		{"default", []string{"ALTER TABLE user_permission_heads MODIFY is_deleted INT NOT NULL DEFAULT 1"}},
		{"missing_unique", []string{"ALTER TABLE user_permission_overrides DROP INDEX uk_permission_overrides_version_cap"}},
		{"missing_fk", []string{"ALTER TABLE user_permission_heads DROP FOREIGN KEY fk_permission_heads_user"}},
		{"cascade_delete", []string{"ALTER TABLE user_permission_heads DROP FOREIGN KEY fk_permission_heads_user", "ALTER TABLE user_permission_heads ADD CONSTRAINT fk_permission_heads_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE"}},
		{"cascade_update", []string{"ALTER TABLE user_permission_heads DROP FOREIGN KEY fk_permission_heads_user", "ALTER TABLE user_permission_heads ADD CONSTRAINT fk_permission_heads_user FOREIGN KEY(user_id) REFERENCES users(id) ON UPDATE CASCADE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gdb := permissionSchemaDB(t)
			permissionUp(t, gdb)
			for _, ddl := range tc.ddl {
				if err := gdb.Exec(ddl).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := VerifyPermissionSchema(context.Background(), gdb); err == nil {
				t.Fatal("accepted schema drift")
			}
		})
	}
}

func TestPermissionSchemaRejectsCrossSchemaAndCompositeForeignKeys(t *testing.T) {
	t.Run("cross_schema", func(t *testing.T) {
		target := permissionSchemaDB(t)
		permissionUp(t, target)
		source := permissionSchemaDB(t)
		permissionUp(t, source)
		var targetName string
		if err := target.Raw("SELECT DATABASE()").Row().Scan(&targetName); err != nil {
			t.Fatal(err)
		}
		if err := source.Exec("ALTER TABLE user_permission_heads DROP FOREIGN KEY fk_permission_heads_user").Error; err != nil {
			t.Fatal(err)
		}
		if err := source.Exec("ALTER TABLE user_permission_heads ADD CONSTRAINT fk_permission_heads_user FOREIGN KEY(user_id) REFERENCES `" + targetName + "`.users(id)").Error; err != nil {
			t.Fatal(err)
		}
		if err := VerifyPermissionSchema(context.Background(), source); err == nil {
			t.Fatal("accepted cross-schema users foreign key")
		}
	})
	t.Run("composite", func(t *testing.T) {
		gdb := permissionSchemaDB(t)
		permissionUp(t, gdb)
		if err := gdb.Exec("ALTER TABLE users ADD UNIQUE KEY uk_permission_test_id_role (id,role)").Error; err != nil {
			t.Fatal(err)
		}
		if err := gdb.Exec("ALTER TABLE user_permission_heads DROP FOREIGN KEY fk_permission_heads_user").Error; err != nil {
			t.Fatal(err)
		}
		if err := gdb.Exec("ALTER TABLE user_permission_heads ADD CONSTRAINT fk_permission_heads_user FOREIGN KEY(user_id,catalog_version) REFERENCES users(id,role)").Error; err != nil {
			t.Fatal(err)
		}
		if err := VerifyPermissionSchema(context.Background(), gdb); err == nil {
			t.Fatal("accepted composite users foreign key")
		}
	})
}

func TestPermissionMigrationRejectsPartialDDLAndLedgerDrift(t *testing.T) {
	gdb := permissionSchemaDB(t)
	permissionUp(t, gdb)
	if err := gdb.Exec("DROP TABLE user_permission_overrides").Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("ALTER TABLE user_permission_heads MODIFY rule_count VARCHAR(16) NOT NULL").Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version='0003'").Error; err != nil {
		t.Fatal(err)
	}
	if err := Up(context.Background(), gdb, persistence.NewSnowflake(20, persistence.SystemClock()).Next, func() int64 { return 1 }); err == nil {
		t.Fatal("accepted malformed preexisting partial DDL")
	}
	var n int64
	if err := gdb.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version='0003'").Scan(&n).Error; err != nil || n != 0 {
		t.Fatalf("bad partial schema entered ledger: count=%d err=%v", n, err)
	}
	if err := Verify(context.Background(), gdb); err == nil {
		t.Fatal("Verify accepted missing ledger entry")
	}
}

func TestPermissionMigrationRepairsValidPartialDDLOnRerun(t *testing.T) {
	gdb := permissionSchemaDB(t)
	permissionUp(t, gdb)
	if err := gdb.Exec("DROP TABLE user_permission_overrides").Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version='0003'").Error; err != nil {
		t.Fatal(err)
	}
	g := persistence.NewSnowflake(22, persistence.SystemClock())
	if err := Up(context.Background(), gdb, g.Next, func() int64 { return 1 }); err != nil {
		t.Fatalf("rerun did not repair valid partial DDL: %v", err)
	}
	if err := Verify(context.Background(), gdb); err != nil {
		t.Fatalf("repaired schema did not verify: %v", err)
	}
}

func TestPermissionMigrationRejectsRecordedSchemaDriftAndForwardOnlyDown(t *testing.T) {
	gdb := permissionSchemaDB(t)
	permissionUp(t, gdb)
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(all[2].DownSQL)), "drop table") || strings.Contains(strings.ToLower(string(all[2].DownSQL)), "delete from") {
		t.Fatal("0003 down destroys retained state")
	}
	if err := gdb.Exec("ALTER TABLE user_permission_heads MODIFY rule_count VARCHAR(16) NOT NULL").Error; err != nil {
		t.Fatal(err)
	}
	g := persistence.NewSnowflake(21, persistence.SystemClock())
	if err := Verify(context.Background(), gdb); err == nil {
		t.Fatal("Verify accepted post-ledger drift")
	}
	if err := Up(context.Background(), gdb, g.Next, func() int64 { return 1 }); err == nil {
		t.Fatal("Up accepted post-ledger drift")
	}
	status, err := Status(context.Background(), gdb)
	if err != nil || VerifyApplied(all[:2], status) == nil || VerifyApplied(all, status[:2]) == nil {
		t.Fatalf("old/new binary ledger boundary not fail-closed: %v", err)
	}
}
