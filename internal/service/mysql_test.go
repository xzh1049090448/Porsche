package service

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

var testSnowflake = persistence.NewSnowflake(os.Getpid()%1024, persistence.SystemClock())

// testAuditFields supplies every non-null persistence field explicitly. Tests
// must model production writes rather than relying on GORM lifecycle hooks.
func testAuditFields() models.AuditFields {
	now := time.Now().UTC().UnixMilli()
	return models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0}
}

func testUser(phone string) models.User {
	return models.User{
		AuditFields:   testAuditFields(),
		Phone:         phone,
		Status:        models.UserStatusActive,
		PlanType:      models.PlanFree,
		AllowedModels: models.JSONSlice{},
	}
}

func TestValidateTestDatabaseURLRejectsUnsafeTargets(t *testing.T) {
	for _, value := range []string{"mysql://u:p@host:3306/platform", "mysql://u:p@host:3306/platform_test"} {
		err := validateTestDatabaseURL(value, "mysql://u:p@host:3306/platform_test")
		if value == "mysql://u:p@host:3306/platform_test" && err == nil {
			t.Fatalf("accepted DATABASE_URL-equivalent test target")
		}
		if value != "mysql://u:p@host:3306/platform_test" && err == nil {
			t.Fatalf("accepted unsafe database: %s", value)
		}
	}
	if err := validateTestDatabaseURL("mysql://u:p@host:3306/porsche_test", "mysql://u:p@host:3306/platform"); err != nil {
		t.Fatalf("rejected isolated test database: %v", err)
	}
}

// openTestMySQL only permits explicitly configured isolated MySQL integration tests.
func openTestMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires isolated TEST_DATABASE_URL MySQL fixture")
	}
	if err := validateTestDatabaseURL(url, os.Getenv("DATABASE_URL")); err != nil {
		t.Fatal(err)
	}
	gdb, err := db.Open(url, "test")
	if err != nil {
		t.Fatal(err)
	}
	var currentDatabase string
	if err := gdb.Raw("SELECT DATABASE()").Scan(&currentDatabase).Error; err != nil || !isSafeTestDatabaseName(currentDatabase) {
		t.Fatal("TEST_DATABASE_URL must connect to a dedicated *_test or porsche_test database")
	}
	return gdb
}

func validateTestDatabaseURL(raw, runtimeURL string) error {
	if raw == "" || (runtimeURL != "" && raw == runtimeURL) {
		return fmt.Errorf("TEST_DATABASE_URL must be set and differ from DATABASE_URL")
	}
	u, err := url.Parse(raw)
	if err != nil || !isSafeTestDatabaseName(strings.TrimPrefix(u.Path, "/")) {
		return fmt.Errorf("TEST_DATABASE_URL must target a dedicated *_test or porsche_test database")
	}
	return nil
}

func isSafeTestDatabaseName(name string) bool {
	return name == "porsche_test" || strings.HasSuffix(name, "_test")
}
