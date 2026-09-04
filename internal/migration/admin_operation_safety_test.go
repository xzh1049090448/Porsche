package migration

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestAdminOperationSafetyMigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 5 || migrations[4].Version != "0005" {
		t.Fatalf("admin operation safety migration 0005 is missing: %#v", migrations)
	}

	up := strings.ToLower(string(migrations[4].UpSQL))
	for _, fragment := range []string{
		"create table if not exists admin_action_verifications",
		"create table if not exists admin_operations",
		"uk_admin_action_verifications_guid (guid)",
		"uk_admin_action_verifications_ticket_hmac (ticket_hmac)",
		"idx_admin_action_verifications_actor_session_active (actor_user_id, session_id, is_deleted, expires_at)",
		"idx_admin_action_verifications_action_target_active (action, target_kind, target_guid, is_deleted)",
		"idx_admin_action_verifications_expiry (is_deleted, expires_at)",
		"fk_admin_action_verifications_actor",
		"fk_admin_action_verifications_session",
		"uk_admin_operations_guid (guid)",
		"uk_admin_operations_public_ref (public_ref)",
		"uk_admin_operations_actor_action_key (actor_user_id, action, idempotency_key_hmac)",
		"uk_admin_operations_verification (verification_id)",
		"idx_admin_operations_state_session (state, session_id, is_deleted, lease_expires_at)",
		"idx_admin_operations_recovery (state, is_deleted, lease_expires_at)",
		"idx_admin_operations_expiry (is_deleted, query_expires_at)",
		"fk_admin_operations_actor",
		"fk_admin_operations_session",
		"fk_admin_operations_verification",
		"engine=innodb default charset=utf8mb4 collate=utf8mb4_unicode_ci",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0005 missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"timestamp", "datetime", " enum(", "create trigger", "delete from"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("0005 contains forbidden %q", forbidden)
		}
	}

	down := strings.ToLower(strings.TrimSpace(string(migrations[4].DownSQL)))
	wantDown := "drop table if exists admin_operations;\ndrop table if exists admin_action_verifications;"
	if down != wantDown {
		t.Fatalf("0005 down = %q, want exact dependency-safe rollback %q", down, wantDown)
	}
}

func TestMigrationSequencePreservesPublishedChecksums(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		version  string
		checksum string
	}{
		{"0001", "2da41ffd07c44d45cb05a705f867db2f2b8f01defb519000197dedce9998aedd"},
		{"0002", "58712428ca668fb1fea0943d71a2209b2e7faf26de043d870195a033ac0f413c"},
		{"0003", "31c49d9bb1f171d9ea6caab49714d9de05552b8f6e9cb73f2989760efd0a015c"},
		{"0004", "44b5caba0473162c239e6b3035e6d9067e3af0b35e998a79c827a1424621494e"},
	}
	if len(migrations) != 5 {
		t.Fatalf("migration count = %d, want 5", len(migrations))
	}
	for i, published := range want {
		if migrations[i].Version != published.version {
			t.Fatalf("migration[%d] version = %q, want %q", i, migrations[i].Version, published.version)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(migrations[i].UpSQL)); got != published.checksum {
			t.Fatalf("migration %s checksum = %s, want immutable %s", published.version, got, published.checksum)
		}
	}
}
