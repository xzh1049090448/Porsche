package service

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
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

func testUser(t *testing.T, db *gorm.DB, _ string) models.User {
	t.Helper()
	return models.User{
		AuditFields:   testAuditFields(),
		GroupID:       testDefaultBusinessGroupID(t, db),
		Phone:         testPhonePointer(),
		Status:        models.UserStatusActive,
		PlanType:      models.PlanFree,
		AllowedModels: models.JSONSlice{},
	}
}

func testDefaultBusinessGroupID(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var groups []models.BusinessGroup
	if err := db.Where("group_key = ? AND is_deleted = 0", "default").Order("id ASC").Find(&groups).Error; err != nil {
		t.Fatalf("load default business group: %v", err)
	}
	if len(groups) != 1 || groups[0].ID <= 0 || groups[0].Guid <= 0 || groups[0].Key != "default" || groups[0].Status != models.BusinessGroupStatusActive || groups[0].IsDeleted != 0 {
		t.Fatalf("invalid default business group fixture: %#v", groups)
	}
	return groups[0].ID
}

func assertUserCanonicalDefaultGroup(t *testing.T, db *gorm.DB, user *models.User) {
	t.Helper()
	if user == nil || user.GroupID <= 0 {
		t.Fatalf("user has invalid group association: %#v", user)
	}
	if want := testDefaultBusinessGroupID(t, db); user.GroupID != want {
		t.Fatalf("user group_id = %d, want canonical default %d", user.GroupID, want)
	}
}

// testPhone yields an 11-digit value for each fixture write, so an isolated
// MySQL test database can retain data across package and process test runs.
func testPhone() string {
	return fmt.Sprintf("13%09d", testSnowflake.Next()%1_000_000_000)
}

func testPhonePointer() *string {
	phone := testPhone()
	return &phone
}

// A one-byte prefix plus all 19 possible decimal digits fits VARCHAR(20).
// Padding also keeps small fixture IDs within the minimum username length.
func fixtureUsername(guid int64) string {
	return fmt.Sprintf("u%019d", guid)
}

func TestFixtureUsernamePreservesIDWithinSchemaLimit(t *testing.T) {
	seen := make(map[string]bool)
	for _, guid := range []int64{1, 9, 10, 353560822848135168, math.MaxInt64 - 1, math.MaxInt64} {
		username := fixtureUsername(guid)
		if len(username) > 20 {
			t.Fatalf("fixture username exceeds VARCHAR(20): %q", username)
		}
		if normalized, err := NormalizeUsername(username); err != nil || normalized != username {
			t.Fatalf("fixture username violates production validation: %q (%v)", username, err)
		}
		if seen[username] {
			t.Fatalf("distinct IDs produced duplicate username: %q", username)
		}
		if decoded, err := strconv.ParseInt(username[1:], 10, 64); err != nil || decoded != guid {
			t.Fatalf("fixture username lost ID precision: %q", username)
		}
		seen[username] = true
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
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close test MySQL connection: %v", err)
		}
	})
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
