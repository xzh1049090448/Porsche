package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
)

func TestNormalizeUsernameTrimsAndEnforcesLength(t *testing.T) {
	username, err := NormalizeUsername("  porsche_user  ")
	if err != nil {
		t.Fatalf("normalize username: %v", err)
	}
	if username != "porsche_user" {
		t.Fatalf("username = %q, want trimmed value", username)
	}

	for _, invalid := range []string{"ab", strings.Repeat("a", 21), "user name", "用户"} {
		if _, err := NormalizeUsername(invalid); err == nil {
			t.Fatalf("NormalizeUsername(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestUsernameRegistrationPermanentlyReservesTrimmedUsername(t *testing.T) {
	db := openTestMySQL(t)
	prepareAuthRegistrationSchema(t, db)
	auth := NewAuthService(&config.Settings{RegisterEnabled: true, PasswordRegisterEnabled: true}, nil, db)
	created, err := auth.RegisterUsername(context.Background(), "  permanent_user  ", "Str0ng!pw", nil)
	if err != nil {
		t.Fatalf("register username: %v", err)
	}
	if created.Username == nil || *created.Username != "permanent_user" || created.Phone != nil || created.PasswordHash == nil || !strings.HasPrefix(*created.PasswordHash, "$argon2id$") {
		t.Fatalf("unsafe username registration result: %#v", created)
	}
	if err := db.Model(&models.User{}).Where("id = ?", created.ID).Updates(map[string]any{"is_deleted": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := auth.RegisterUsername(context.Background(), "permanent_user", "Str0ng!pw", nil); err == nil {
		t.Fatal("tombstoned username was reused")
	}
}

func TestRootBootstrapCreatesOnlyTheFirstRoot(t *testing.T) {
	db := openTestMySQL(t)
	prepareAuthRegistrationSchema(t, db)
	settings := &config.Settings{RootBootstrapUsername: "initial_root", RootBootstrapPassword: "Str0ng!Root1"}
	auth := NewAuthService(settings, nil, db)
	root, err := auth.BootstrapRoot(context.Background())
	if err != nil || root == nil || root.Role != models.UserRoleRoot {
		t.Fatalf("bootstrap root = %#v, %v", root, err)
	}
	if settings.RootBootstrapUsername != "" || settings.RootBootstrapPassword != "" {
		t.Fatal("successful bootstrap values remained reusable")
	}
	second := NewAuthService(&config.Settings{RootBootstrapUsername: "second_root", RootBootstrapPassword: "Str0ng!Root2"}, nil, db)
	created, err := second.BootstrapRoot(context.Background())
	if err != nil || created != nil {
		t.Fatalf("second root bootstrap = %#v, %v", created, err)
	}
	var count int64
	if err := db.Model(&models.User{}).Where("role = ?", models.UserRoleRoot).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("root count = %d, err=%v", count, err)
	}
}

func TestLoginUsernameRejectsDisabledAndSoftDeletedUser(t *testing.T) {
	db := openTestMySQL(t)
	prepareAuthRegistrationSchema(t, db)
	password := "Str0ng!pw"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("disabled_user_%d", testSnowflake.Next())
	now := persistence.NowMillis()
	user := &models.User{AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now}, Username: &username, PasswordHash: &hash, Status: models.UserStatusDisabled, Role: models.UserRoleUser, AuthVersion: 1, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{}}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	auth := NewAuthService(&config.Settings{PasswordLoginEnabled: true}, nil, db)
	auth.SetSessionService(&SessionService{})
	if _, _, _, err := auth.LoginUsername(context.Background(), username, password, SessionCreateInput{}); !isUnauthorized(err) {
		t.Fatalf("disabled login error = %v, want generic unauthorized", err)
	}
	if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{"status": models.UserStatusActive, "is_deleted": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := auth.LoginUsername(context.Background(), username, password, SessionCreateInput{}); !isUnauthorized(err) {
		t.Fatalf("soft-deleted login error = %v, want generic unauthorized", err)
	}
}

func prepareAuthRegistrationSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	generator := persistence.NewSnowflake(3, persistence.SystemClock())
	if err := migration.Up(context.Background(), db, generator.Next, persistence.NowMillis); err != nil {
		t.Fatalf("apply isolated auth migration: %v", err)
	}
}

func isUnauthorized(err error) bool {
	var typed *HTTPError
	return errors.As(err, &typed) && typed.Status == 401
}

func TestPasswordUsesArgon2idAndRejectsWeakPassword(t *testing.T) {
	if err := ValidatePassword("password"); err == nil {
		t.Fatal("common weak password was accepted")
	}
	if err := ValidatePassword("Str0ng!pw"); err != nil {
		t.Fatalf("strong password rejected: %v", err)
	}

	hash, err := security.HashPassword("Str0ng!pw")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("password hash must use Argon2id, got %q", hash)
	}
	if !security.VerifyPassword("Str0ng!pw", hash) || security.VerifyPassword("wrong-password", hash) {
		t.Fatal("Argon2id password verification contract failed")
	}
}
