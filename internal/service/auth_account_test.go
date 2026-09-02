package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
)

const (
	changePasswordOld = "Str0ng!Old1"
	changePasswordNew = "Str0ng!New2"
)

// TestChangePasswordRejectsIncorrectOldPasswordWithoutMutation verifies that
// an invalid current password leaves credentials, sessions, Redis barriers,
// and audit records untouched.
func TestChangePasswordRejectsIncorrectOldPasswordWithoutMutation(t *testing.T) {
	ctx, db, redisStore, auth, user, issued := changePasswordFixture(t)

	if err := auth.ChangePassword(ctx, user.ID, "wrong-current-password", changePasswordNew); !isUnauthorized(err) {
		t.Fatalf("ChangePassword wrong current password = %v, want unauthorized", err)
	}

	stored := loadChangePasswordUser(t, db, user.ID)
	if stored.PasswordHash == nil || !security.VerifyPassword(changePasswordOld, *stored.PasswordHash) || stored.AuthVersion != user.AuthVersion {
		t.Fatalf("wrong password mutated user credentials: %#v", stored)
	}
	assertChangePasswordSessionActive(t, db, issued.Session.ID)
	if revoked, err := redisStore.IsSessionRevoked(ctx, issued.Session.SID); err != nil || revoked {
		t.Fatalf("wrong password wrote Redis denial barrier: revoked=%t err=%v", revoked, err)
	}
	assertChangePasswordAuditCount(t, db, user.ID, models.AuthAuditEventPasswordChanged, 0)
	assertChangePasswordAuditCount(t, db, user.ID, models.AuthAuditEventSessionRevoked, 0)
}

// TestChangePasswordRehashesCredentialsRevokesSessionsAndAudits verifies the
// durable password transition invalidates all existing sessions and leaves
// Redis denial barriers for each session.
func TestChangePasswordRehashesCredentialsRevokesSessionsAndAudits(t *testing.T) {
	ctx, db, redisStore, auth, user, first := changePasswordFixture(t)
	second, err := auth.sessions.Create(ctx, user, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.62"})
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}

	if err := auth.ChangePassword(ctx, user.ID, changePasswordOld, changePasswordNew); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	stored := loadChangePasswordUser(t, db, user.ID)
	if stored.PasswordHash == nil || !strings.HasPrefix(*stored.PasswordHash, "$argon2id$") || !security.VerifyPassword(changePasswordNew, *stored.PasswordHash) || security.VerifyPassword(changePasswordOld, *stored.PasswordHash) {
		t.Fatalf("password hash was not replaced with a verifiable Argon2id value: %#v", stored.PasswordHash)
	}
	if stored.AuthVersion != user.AuthVersion+1 {
		t.Fatalf("auth version = %d, want %d", stored.AuthVersion, user.AuthVersion+1)
	}
	for _, issued := range []*IssuedSession{first, second} {
		assertChangePasswordSessionRevoked(t, db, issued.Session.ID)
		if revoked, err := redisStore.IsSessionRevoked(ctx, issued.Session.SID); err != nil || !revoked {
			t.Fatalf("session %s Redis denial barrier = %t, %v; want true, nil", issued.Session.SID, revoked, err)
		}
	}
	assertChangePasswordAuditCount(t, db, user.ID, models.AuthAuditEventPasswordChanged, 1)
	assertChangePasswordAuditCount(t, db, user.ID, models.AuthAuditEventSessionRevoked, 2)
}

// TestChangePasswordRejectsRedisDenyFailureWithoutMutation proves a failed
// mandatory Redis dependency cannot report a successful change or mutate MySQL.
func TestChangePasswordRejectsRedisDenyFailureWithoutMutation(t *testing.T) {
	ctx, db, redisStore, auth, user, issued := changePasswordFixture(t)
	if err := redisStore.Close(); err != nil {
		t.Fatalf("close test Redis client: %v", err)
	}

	if err := auth.ChangePassword(ctx, user.ID, changePasswordOld, changePasswordNew); err == nil {
		t.Fatal("ChangePassword succeeded after Redis became unavailable")
	}

	stored := loadChangePasswordUser(t, db, user.ID)
	if stored.PasswordHash == nil || !security.VerifyPassword(changePasswordOld, *stored.PasswordHash) || stored.AuthVersion != user.AuthVersion {
		t.Fatalf("Redis failure mutated user credentials: %#v", stored)
	}
	assertChangePasswordSessionActive(t, db, issued.Session.ID)
	assertChangePasswordAuditCount(t, db, user.ID, models.AuthAuditEventPasswordChanged, 0)
	assertChangePasswordAuditCount(t, db, user.ID, models.AuthAuditEventSessionRevoked, 0)
}

// TestChangePasswordRollsBackMySQLWhenPasswordAuditWriteFails verifies an
// audit-write failure rolls the credential, auth-version, and session writes
// back together. The earlier Redis barriers intentionally remain deny-first.
func TestChangePasswordRollsBackMySQLWhenPasswordAuditWriteFails(t *testing.T) {
	ctx, db, redisStore, auth, user, issued := changePasswordFixture(t)
	constraint := fmt.Sprintf("chk_change_password_audit_%d", testSnowflake.Next())
	if err := db.Exec(fmt.Sprintf("ALTER TABLE auth_audit_events ADD CONSTRAINT %s CHECK (user_id <> %d OR event_type <> %d)", constraint, user.ID, models.AuthAuditEventPasswordChanged)).Error; err != nil {
		t.Fatalf("add isolated password audit failure constraint: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Exec(fmt.Sprintf("ALTER TABLE auth_audit_events DROP CHECK %s", constraint)).Error; err != nil {
			t.Errorf("drop isolated password audit failure constraint: %v", err)
		}
	})

	if err := auth.ChangePassword(ctx, user.ID, changePasswordOld, changePasswordNew); err == nil {
		t.Fatal("ChangePassword succeeded despite password audit write failure")
	}

	stored := loadChangePasswordUser(t, db, user.ID)
	if stored.PasswordHash == nil || !security.VerifyPassword(changePasswordOld, *stored.PasswordHash) || stored.AuthVersion != user.AuthVersion {
		t.Fatalf("failed MySQL transaction partially committed user credentials: %#v", stored)
	}
	assertChangePasswordSessionActive(t, db, issued.Session.ID)
	assertChangePasswordAuditCount(t, db, user.ID, models.AuthAuditEventPasswordChanged, 0)
	assertChangePasswordAuditCount(t, db, user.ID, models.AuthAuditEventSessionRevoked, 0)
	if revoked, err := redisStore.IsSessionRevoked(ctx, issued.Session.SID); err != nil || !revoked {
		t.Fatalf("failed transaction must retain deny-first Redis barrier: revoked=%t err=%v", revoked, err)
	}
}

func changePasswordFixture(t *testing.T) (context.Context, *gorm.DB, *AuthRedis, *AuthService, *models.User, *IssuedSession) {
	t.Helper()
	redisStore := openTestAuthRedis(t)
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	user := createAuthSessionTestUser(t, db)
	hash, err := security.HashPassword(changePasswordOld)
	if err != nil {
		t.Fatalf("hash fixture password: %v", err)
	}
	if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{"password_hash": hash}).Error; err != nil {
		t.Fatalf("set fixture password: %v", err)
	}
	if err := db.Where("id = ? AND is_deleted = 0", user.ID).First(user).Error; err != nil {
		t.Fatalf("reload fixture user: %v", err)
	}
	sessions := NewSessionService(db, redisStore, testSessionSettings())
	issued, err := sessions.Create(context.Background(), user, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.61"})
	if err != nil {
		t.Fatalf("create fixture session: %v", err)
	}
	auth := NewAuthService(&config.Settings{}, nil, db)
	auth.SetSessionService(sessions)
	return context.Background(), db, redisStore, auth, user, issued
}

func loadChangePasswordUser(t *testing.T, db *gorm.DB, userID int64) models.User {
	t.Helper()
	var user models.User
	if err := db.Where("id = ? AND is_deleted = 0", userID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	return user
}

func assertChangePasswordSessionActive(t *testing.T, db *gorm.DB, sessionID int64) {
	t.Helper()
	var session models.Session
	if err := db.Where("id = ? AND is_deleted = 0", sessionID).First(&session).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.RevokedAt != nil {
		t.Fatalf("session %d was unexpectedly revoked at %d", session.ID, *session.RevokedAt)
	}
}

func assertChangePasswordSessionRevoked(t *testing.T, db *gorm.DB, sessionID int64) {
	t.Helper()
	var session models.Session
	if err := db.Where("id = ? AND is_deleted = 0", sessionID).First(&session).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.RevokedAt == nil {
		t.Fatalf("session %d was not revoked", session.ID)
	}
}

func assertChangePasswordAuditCount(t *testing.T, db *gorm.DB, userID int64, event models.AuthAuditEventType, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ? AND is_deleted = 0", userID, event).Count(&count).Error; err != nil {
		t.Fatalf("count audit event %v: %v", event, err)
	}
	if count != want {
		t.Fatalf("audit event %v count = %d, want %d", event, count, want)
	}
}
