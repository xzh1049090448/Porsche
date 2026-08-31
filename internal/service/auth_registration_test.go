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
	redisStore := openTestAuthRedis(t)
	db := openTestMySQL(t)
	prepareAuthRegistrationSchema(t, db)
	auth := NewAuthService(&config.Settings{RegisterEnabled: true, PasswordRegisterEnabled: true}, nil, db)
	auth.SetSessionService(NewSessionService(db, redisStore, testSessionSettings()))
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

// TestCurrentUserWritesRejectAccountsChangedAfterAuthentication protects the
// interval after middleware has loaded a user and before a self-service write
// acquires its transaction lock. Neither a disabled nor a soft-deleted user
// may be revived by stale profile, identity-verification, or password data.
func TestCurrentUserWritesRejectAccountsChangedAfterAuthentication(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(context.Context, *AuthService, *models.User) error
	}{
		{
			name: "profile",
			run: func(ctx context.Context, auth *AuthService, user *models.User) error {
				nickname := "stale-profile"
				_, err := auth.UpdateOwnProfile(ctx, user.ID, &nickname)
				return err
			},
		},
		{
			name: "identity verification",
			run: func(ctx context.Context, auth *AuthService, user *models.User) error {
				return auth.VerifyOwnIdentity(ctx, user.ID, "Stale User", "11010519491231002X")
			},
		},
		{
			name: "password",
			run: func(ctx context.Context, auth *AuthService, user *models.User) error {
				return auth.ChangePassword(ctx, user.ID, changePasswordOld, changePasswordNew)
			},
		},
	} {
		for _, accountChange := range []struct {
			name  string
			apply func(*gorm.DB, *models.User) models.User
		}{
			{
				name: "disabled",
				apply: func(db *gorm.DB, user *models.User) models.User {
					if err := db.Model(&models.User{}).Where("id = ?", user.ID).Update("status", models.UserStatusDisabled).Error; err != nil {
						t.Fatal(err)
					}
					return loadAuthUserIncludingDeleted(t, db, user.ID)
				},
			},
			{
				name: "soft deleted",
				apply: func(db *gorm.DB, user *models.User) models.User {
					if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
						"status": models.UserStatusDisabled, "is_deleted": 1, "phone": nil, "password_hash": nil,
					}).Error; err != nil {
						t.Fatal(err)
					}
					return loadAuthUserIncludingDeleted(t, db, user.ID)
				},
			},
		} {
			t.Run(operation.name+"/"+accountChange.name, func(t *testing.T) {
				ctx, db, _, auth, user, _ := changePasswordFixture(t)
				before := accountChange.apply(db, user)

				if err := operation.run(ctx, auth, user); !isUnauthorized(err) {
					t.Fatalf("%s after %s = %v, want unauthorized", operation.name, accountChange.name, err)
				}

				after := loadAuthUserIncludingDeleted(t, db, user.ID)
				if after.Status != before.Status || after.AuthVersion != before.AuthVersion || after.IsDeleted != before.IsDeleted || !equalStringPointer(after.Phone, before.Phone) || !equalStringPointer(after.PasswordHash, before.PasswordHash) {
					t.Fatalf("%s after %s changed protected columns: before=%#v after=%#v", operation.name, accountChange.name, before, after)
				}
			})
		}
	}
}

func loadAuthUserIncludingDeleted(t *testing.T, db *gorm.DB, userID int64) models.User {
	t.Helper()
	var user models.User
	if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

func equalStringPointer(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
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

func TestRootBootstrapDoesNotReplaceTombstonedRoot(t *testing.T) {
	db := openTestMySQL(t)
	prepareAuthRegistrationSchema(t, db)
	username := "retired_root"
	now := persistence.NowMillis()
	retired := &models.User{AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 1}, Username: &username, Nickname: &username, PlanType: models.PlanFree, Status: models.UserStatusDisabled, Role: models.UserRoleRoot, AuthVersion: 2, AllowedModels: models.JSONSlice{}}
	if err := db.Create(retired).Error; err != nil {
		t.Fatal(err)
	}
	auth := NewAuthService(&config.Settings{RootBootstrapUsername: "replacement_root", RootBootstrapPassword: "Str0ng!Root1"}, nil, db)
	created, err := auth.BootstrapRoot(context.Background())
	if err != nil || created != nil {
		t.Fatalf("tombstoned Root must permanently consume bootstrap: %#v, %v", created, err)
	}
}

func TestCanManageUserRequiresStrictlyHigherRoleAndProtectsRoot(t *testing.T) {
	admin := &models.User{Status: models.UserStatusActive, Role: models.UserRoleAdmin}
	user := &models.User{Status: models.UserStatusActive, Role: models.UserRoleUser}
	peer := &models.User{Status: models.UserStatusActive, Role: models.UserRoleAdmin}
	root := &models.User{Status: models.UserStatusActive, Role: models.UserRoleRoot}
	if err := CanManageUser(admin, user); err != nil {
		t.Fatalf("admin should manage ordinary user: %v", err)
	}
	for _, target := range []*models.User{peer, root} {
		if err := CanManageUser(admin, target); err == nil {
			t.Fatalf("admin unexpectedly managed role %v", target.Role)
		}
	}
	if err := CanManageUser(root, admin); err != nil {
		t.Fatalf("Root should manage lower role: %v", err)
	}
	if err := CanManageUser(root, root); err == nil {
		t.Fatal("Root must not manage Root accounts")
	}
}

// TestCanManageUserRejectsDisabledActor closes the interval between an
// administrator request passing middleware and the later management
// transaction acquiring its actor row lock. A newly disabled actor must not
// retain authority merely because its role is still Admin.
func TestCanManageUserRejectsDisabledActor(t *testing.T) {
	actor := &models.User{Status: models.UserStatusDisabled, Role: models.UserRoleAdmin}
	target := &models.User{Status: models.UserStatusActive, Role: models.UserRoleUser}

	if err := CanManageUser(actor, target); err == nil {
		t.Fatal("disabled administrator unexpectedly retained management authority")
	}
}

// TestDisableUserRejectsActorDisabledAfterAuthorization verifies the service
// rechecks the locked actor instead of trusting an earlier RequireAdmin
// decision. The target must remain unchanged when the actor is disabled in
// the interval before the management transaction starts.
func TestDisableUserRejectsActorDisabledAfterAuthorization(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	actor := createAuthSessionTestUser(t, db)
	target := createAuthSessionTestUser(t, db)
	if err := db.Model(&models.User{}).Where("id = ?", actor.ID).Updates(map[string]any{"role": models.UserRoleAdmin}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ? AND is_deleted = 0", actor.ID).First(actor).Error; err != nil {
		t.Fatal(err)
	}
	if err := CanManageUser(actor, target); err != nil {
		t.Fatalf("precondition: active administrator should pass authorization: %v", err)
	}
	if err := db.Model(&models.User{}).Where("id = ?", actor.ID).Updates(map[string]any{"status": models.UserStatusDisabled}).Error; err != nil {
		t.Fatal(err)
	}

	auth := NewAuthService(&config.Settings{}, nil, db)
	auth.SetSessionService(NewSessionService(db, redisStore, testSessionSettings()))
	if err := auth.DisableUser(context.Background(), actor.ID, target.ID); err == nil {
		t.Fatal("disabled actor unexpectedly disabled target")
	} else if status, _ := StatusFromError(err); status != 403 {
		t.Fatalf("DisableUser status = %d, want 403; err=%v", status, err)
	}

	var stored models.User
	if err := db.Where("id = ? AND is_deleted = 0", target.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.UserStatusActive || stored.AuthVersion != target.AuthVersion {
		t.Fatalf("target changed after disabled actor request: %#v", stored)
	}
}

// TestUpdateManagedUserRejectsActorChangedAfterAuthentication proves that the
// administrator update transaction re-reads and locks the persisted actor
// instead of trusting an earlier successful administrator authentication. A
// disabled or downgraded actor cannot change a target's status, plan, ACL, or
// quota after that authentication boundary.
func TestUpdateManagedUserRejectsActorChangedAfterAuthentication(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		actorUpdate map[string]any
	}{
		{name: "disabled", actorUpdate: map[string]any{"status": models.UserStatusDisabled}},
		{name: "downgraded", actorUpdate: map[string]any{"role": models.UserRoleUser}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			redisStore := openTestAuthRedis(t)
			db := openTestMySQL(t)
			prepareAuthSessionSchema(t, db)
			actor := createAuthSessionTestUser(t, db)
			target := createAuthSessionTestUser(t, db)
			if err := db.Model(&models.User{}).Where("id = ?", actor.ID).Updates(map[string]any{"role": models.UserRoleAdmin}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("id = ? AND is_deleted = 0", actor.ID).First(actor).Error; err != nil {
				t.Fatal(err)
			}
			if err := CanManageUser(actor, target); err != nil {
				t.Fatalf("precondition: active administrator should pass authorization: %v", err)
			}

			if err := db.Model(&models.User{}).Where("id = ?", actor.ID).Updates(testCase.actorUpdate).Error; err != nil {
				t.Fatal(err)
			}
			status := models.UserStatusDisabled
			plan := models.PlanEnterprise
			allowedModels := models.JSONSlice{"model-a", "model-b"}
			quota := 777
			auth := NewAuthService(&config.Settings{}, nil, db)
			auth.SetSessionService(NewSessionService(db, redisStore, testSessionSettings()))
			_, err := auth.UpdateManagedUser(context.Background(), actor.ID, target.Guid, ManagedUserUpdateInput{
				Status:         &status,
				PlanType:       &plan,
				AllowedModels:  &allowedModels,
				DailyCallLimit: &quota,
			})
			if statusCode, _ := StatusFromError(err); statusCode != 403 {
				t.Fatalf("UpdateManagedUser status = %d, want 403; err=%v", statusCode, err)
			}

			var stored models.User
			if err := db.Where("id = ? AND is_deleted = 0", target.ID).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != target.Status || stored.PlanType != target.PlanType || stored.DailyCallLimit != target.DailyCallLimit || !equalJSONSlice(stored.AllowedModels, target.AllowedModels) {
				t.Fatalf("target changed after %s actor request: %#v", testCase.name, stored)
			}
		})
	}
}

func equalJSONSlice(left, right models.JSONSlice) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
