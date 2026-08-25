package migration

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrationsContainOneWayInitialSchema(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(migrations) != 1 || migrations[0].Version != "0001" {
		t.Fatalf("unexpected migrations: %#v", migrations)
	}
	up := strings.ToLower(string(migrations[0].UpSQL))
	if !strings.Contains(up, "create table if not exists users") || strings.Contains(up, "timestamp") {
		t.Fatalf("initial schema must create users without timestamp columns: %s", up)
	}
	down := strings.ToLower(string(migrations[0].DownSQL))
	if strings.Contains(down, "drop table") || strings.Contains(down, "drop database") {
		t.Fatalf("down migration must not destroy data: %s", down)
	}
}

func TestVerifyAppliedRejectsMissingAndTamperedMigrations(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyApplied(migrations, nil); err == nil {
		t.Fatal("expected missing migration verification error")
	}
	if err := VerifyApplied(migrations, []AppliedMigration{{Version: "0001", Checksum: "tampered"}}); err == nil {
		t.Fatal("expected checksum verification error")
	}
}
