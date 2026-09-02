package service

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

// Root bootstrap observes all Root rows, including tombstones, so its tests
// need an owned database, not a shared schema with other account fixtures.
// Only an explicit, validated TEST_DATABASE_URL grants the parent connection.
func openRootTestMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	parent := openTestMySQL(t)
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "porsche_root_" + hex.EncodeToString(random[:]) + "_test"
	if err := parent.Exec("CREATE DATABASE `" + name + "`").Error; err != nil {
		t.Fatalf("create owned Root test database: %v", err)
	}
	// Register ownership only after successful CREATE, never on name collision.
	// LIFO cleanup closes the child before dropping it, then closes the parent.
	t.Cleanup(func() {
		if err := parent.Exec("DROP DATABASE `" + name + "`").Error; err != nil {
			t.Errorf("drop owned Root test database %s: %v", name, err)
		}
	})
	childURL, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("parse validated TEST_DATABASE_URL")
	}
	childURL.Path = "/" + name
	childURL.RawPath = ""
	child, err := db.Open(childURL.String(), "test")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := child.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close owned Root test database: %v", err)
		}
	})
	return child
}

func TestRootTestMySQLIsolatesFixturesAndPreservesParent(t *testing.T) {
	parent := openTestMySQL(t)
	prepareAuthRegistrationSchema(t, parent)
	sentinel := createAuthSessionTestUser(t, parent)
	var parentName string
	if err := parent.Raw("SELECT DATABASE()").Scan(&parentName).Error; err != nil {
		t.Fatal(err)
	}
	var ownedNames []string
	t.Run("owned databases", func(t *testing.T) {
		first := openRootTestMySQL(t)
		second := openRootTestMySQL(t)
		for _, child := range []*gorm.DB{first, second} {
			var name string
			if err := child.Raw("SELECT DATABASE()").Scan(&name).Error; err != nil {
				t.Fatal(err)
			}
			if name == parentName {
				t.Fatal("Root helper reused parent database")
			}
			ownedNames = append(ownedNames, name)
			prepareAuthRegistrationSchema(t, child)
		}
		if ownedNames[0] == ownedNames[1] {
			t.Fatal("Root helper reused an owned database")
		}
		fixture := createAuthSessionTestUser(t, first)
		if err := first.Model(&models.User{}).Where("id = ?", fixture.ID).Update("role", models.UserRoleRoot).Error; err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := second.Model(&models.User{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("second Root database inherited fixtures: count=%d err=%v", count, err)
		}
	})
	for _, name := range ownedNames {
		var count int64
		if err := parent.Raw("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", name).Scan(&count).Error; err != nil || count != 0 {
			t.Fatalf("owned Root database was not cleaned up: count=%d err=%v", count, err)
		}
	}
	var preserved models.User
	if err := parent.Where("id = ? AND guid = ? AND is_deleted = 0", sentinel.ID, sentinel.Guid).First(&preserved).Error; err != nil {
		t.Fatalf("Root database lifecycle changed parent fixture: %v", err)
	}
}
