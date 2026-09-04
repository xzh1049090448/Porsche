package service

import (
	"context"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func managedUserSecurityFixture(t *testing.T) (*AuthRedis, *AuthService, *models.User, *models.User, *IssuedSession) {
	t.Helper()
	_, db, redisStore, auth, target, issued := changePasswordFixture(t)
	actor := createAuthSessionTestUser(t, db)
	if err := db.Model(&models.User{}).Where("id = ?", actor.ID).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ? AND is_deleted = 0", actor.ID).First(actor).Error; err != nil {
		t.Fatal(err)
	}
	return redisStore, auth, actor, target, issued
}

// TestUpdateManagedUserPlanChangeRevokesExistingSession establishes that a
// security-relevant administrator plan change invalidates credentials issued
// before the durable transition.
func TestUpdateManagedUserPlanChangeRevokesExistingSession(t *testing.T) {
	ctx, db, redisStore, auth, target, issued := changePasswordFixture(t)
	actor := createAuthSessionTestUser(t, db)
	if err := db.Model(&models.User{}).Where("id = ?", actor.ID).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ? AND is_deleted = 0", actor.ID).First(actor).Error; err != nil {
		t.Fatal(err)
	}
	plan := models.PlanProfessional
	if _, err := auth.UpdateManagedUser(ctx, actor.ID, target.Guid, ManagedUserUpdateInput{PlanType: &plan}); err != nil {
		t.Fatal(err)
	}
	stored := loadChangePasswordUser(t, db, target.ID)
	if stored.AuthVersion != target.AuthVersion+1 {
		t.Fatalf("auth version = %d, want %d", stored.AuthVersion, target.AuthVersion+1)
	}
	assertChangePasswordSessionRevoked(t, db, issued.Session.ID)
	if revoked, err := redisStore.IsSessionRevoked(ctx, issued.Session.SID); err != nil || !revoked {
		t.Fatalf("Redis session barrier = %t, %v; want true, nil", revoked, err)
	}
	var sessionAudit models.AuthAuditEvent
	if err := db.Where("user_id = ? AND session_guid = ? AND event_type = ?", target.ID, issued.Session.Guid, models.AuthAuditEventSessionRevoked).First(&sessionAudit).Error; err != nil {
		t.Fatal(err)
	}
	if sessionAudit.CreatedBy == nil || sessionAudit.UpdatedBy == nil || *sessionAudit.CreatedBy != actor.ID || *sessionAudit.UpdatedBy != actor.ID {
		t.Fatalf("session audit actor=%v/%v, want %d", sessionAudit.CreatedBy, sessionAudit.UpdatedBy, actor.ID)
	}
	assertChangePasswordAuditCount(t, db, target.ID, models.AuthAuditEventType(10), 1)
	if _, err := auth.sessions.Validate(ctx, issued.Session.SID, target.ID, issued.Session.SessionVersion, target.AuthVersion); err == nil {
		t.Fatal("old access session remained valid")
	}
	if _, err := auth.sessions.Refresh(ctx, issued.RefreshToken); err == nil {
		t.Fatal("old refresh token remained valid")
	}
	newSession, err := auth.sessions.Create(ctx, &stored, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.77"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.sessions.Validate(ctx, newSession.Session.SID, target.ID, newSession.Session.SessionVersion, stored.AuthVersion); err != nil {
		t.Fatalf("new login was not valid: %v", err)
	}
	auth.settings.PasswordLoginEnabled = true
	auth.settings.JWTSecretKey = "managed-user-login-test-secret"
	auth.settings.SessionAccessMinutes = 5
	loggedIn, loginSession, access, err := auth.LoginUsername(ctx, *stored.Username, changePasswordOld, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.78"})
	if err != nil || loggedIn == nil || loginSession == nil || access == "" {
		t.Fatalf("LoginUsername user=%#v session=%#v token=%q err=%v", loggedIn, loginSession, access, err)
	}
	if _, err := auth.sessions.Validate(ctx, loginSession.Session.SID, target.ID, loginSession.Session.SessionVersion, stored.AuthVersion); err != nil {
		t.Fatalf("new LoginUsername session was not valid: %v", err)
	}
}

func TestUpdateManagedUserRejectsInvalidTypedInputWithoutMutation(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		input ManagedUserUpdateInput
		want  int
	}{
		{name: "status", input: ManagedUserUpdateInput{Status: ptr(models.UserStatus(99))}, want: 422},
		{name: "plan", input: ManagedUserUpdateInput{PlanType: ptr(models.PlanType(99))}, want: 422},
		{name: "negative limit", input: ManagedUserUpdateInput{DailyCallLimit: ptr(-1)}, want: 400},
		{name: "empty ACL value", input: ManagedUserUpdateInput{AllowedModels: ptr(models.JSONSlice{""})}, want: 400},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, auth, actor, target, _ := managedUserSecurityFixture(t)
			before := *target
			updated, err := auth.UpdateManagedUser(context.Background(), actor.ID, target.Guid, testCase.input)
			if updated != nil {
				t.Fatalf("unexpected user: %#v", updated)
			}
			if got, _ := StatusFromError(err); got != testCase.want {
				t.Fatalf("status=%d err=%v, want %d", got, err, testCase.want)
			}
			var stored models.User
			if err := auth.db.Where("id = ?", target.ID).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.AuthVersion != before.AuthVersion || stored.PlanType != before.PlanType || stored.DailyCallLimit != before.DailyCallLimit {
				t.Fatalf("invalid input mutated target: %#v", stored)
			}
		})
	}
}

func TestUpdateManagedUserStatusMixedAndDailyLimitTransitions(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		input         func(*models.User) ManagedUserUpdateInput
		wantVersionUp bool
		wantRevoked   bool
	}{
		{name: "status", input: func(*models.User) ManagedUserUpdateInput {
			return ManagedUserUpdateInput{Status: ptr(models.UserStatusDisabled)}
		}, wantVersionUp: true, wantRevoked: true},
		{name: "mixed", input: func(*models.User) ManagedUserUpdateInput {
			return ManagedUserUpdateInput{Status: ptr(models.UserStatusDisabled), PlanType: ptr(models.PlanEnterprise), AllowedModels: ptr(models.JSONSlice{"model-a"}), DailyCallLimit: ptr(7)}
		}, wantVersionUp: true, wantRevoked: true},
		{name: "daily limit only", input: func(*models.User) ManagedUserUpdateInput { return ManagedUserUpdateInput{DailyCallLimit: ptr(7)} }, wantVersionUp: false, wantRevoked: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, auth, actor, target, issued := managedUserSecurityFixture(t)
			updated, err := auth.UpdateManagedUser(context.Background(), actor.ID, target.Guid, testCase.input(target))
			if err != nil || updated == nil {
				t.Fatalf("UpdateManagedUser = %#v, %v", updated, err)
			}
			var stored models.User
			if err := auth.db.Where("id = ?", target.ID).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			wantVersion := target.AuthVersion
			if testCase.wantVersionUp {
				wantVersion++
			}
			if stored.AuthVersion != wantVersion {
				t.Fatalf("auth version=%d, want %d", stored.AuthVersion, wantVersion)
			}
			var session models.Session
			if err := auth.db.Where("id = ?", issued.Session.ID).First(&session).Error; err != nil {
				t.Fatal(err)
			}
			if (session.RevokedAt != nil) != testCase.wantRevoked {
				t.Fatalf("revoked=%v want %t", session.RevokedAt, testCase.wantRevoked)
			}
			assertChangePasswordAuditCount(t, auth.db, target.ID, models.AuthAuditEventManagedUserUpdated, 1)
		})
	}
}

func TestUpdateManagedUserSemanticACLNoopReturnsStoredRepresentation(t *testing.T) {
	_, auth, actor, target, issued := managedUserSecurityFixture(t)
	storedACL := models.JSONSlice{"model-a", "model-b"}
	if err := auth.db.Model(&models.User{}).Where("id = ?", target.ID).Update("allowed_models", storedACL).Error; err != nil {
		t.Fatal(err)
	}
	if err := auth.db.Where("id = ?", target.ID).First(target).Error; err != nil {
		t.Fatal(err)
	}
	inputACL := models.JSONSlice{"model-b", "model-a", "model-a"}
	updated, err := auth.UpdateManagedUser(context.Background(), actor.ID, target.Guid, ManagedUserUpdateInput{AllowedModels: &inputACL})
	if err != nil || updated == nil {
		t.Fatalf("UpdateManagedUser = %#v, %v", updated, err)
	}
	if !equalJSONSlice(updated.AllowedModels, storedACL) {
		t.Fatalf("returned ACL=%#v, want stored %#v", updated.AllowedModels, storedACL)
	}
	var stored models.User
	if err := auth.db.Where("id = ?", target.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !equalJSONSlice(stored.AllowedModels, storedACL) || stored.AuthVersion != target.AuthVersion {
		t.Fatalf("semantic no-op persisted mutation: %#v", stored)
	}
	assertChangePasswordSessionActive(t, auth.db, issued.Session.ID)
	assertChangePasswordAuditCount(t, auth.db, target.ID, models.AuthAuditEventManagedUserUpdated, 0)
}

func TestUpdateManagedUserSecurityChangeWithNoSessionStillAudits(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	actor := createAuthSessionTestUser(t, db)
	target := createAuthSessionTestUser(t, db)
	if err := db.Model(&models.User{}).Where("id = ?", actor.ID).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", actor.ID).First(actor).Error; err != nil {
		t.Fatal(err)
	}
	auth := NewAuthService(&config.Settings{}, nil, db)
	auth.SetSessionService(NewSessionService(db, redisStore, testSessionSettings()))
	plan := models.PlanProfessional
	updated, err := auth.UpdateManagedUser(context.Background(), actor.ID, target.Guid, ManagedUserUpdateInput{PlanType: &plan})
	if err != nil || updated == nil || updated.AuthVersion != target.AuthVersion+1 {
		t.Fatalf("UpdateManagedUser=%#v, %v", updated, err)
	}
	assertChangePasswordAuditCount(t, db, target.ID, models.AuthAuditEventManagedUserUpdated, 1)
	assertChangePasswordAuditCount(t, db, target.ID, models.AuthAuditEventSessionRevoked, 0)
}

func TestUpdateManagedUserRejectsRedisFailureWithoutSQLMutation(t *testing.T) {
	redisStore, auth, actor, target, issued := managedUserSecurityFixture(t)
	if err := redisStore.Close(); err != nil {
		t.Fatal(err)
	}
	plan := models.PlanProfessional
	updated, err := auth.UpdateManagedUser(context.Background(), actor.ID, target.Guid, ManagedUserUpdateInput{PlanType: &plan})
	if updated != nil {
		t.Fatalf("unexpected user: %#v", updated)
	}
	if status, _ := StatusFromError(err); status != 503 {
		t.Fatalf("status=%d err=%v, want 503", status, err)
	}
	var stored models.User
	if err := auth.db.Where("id = ?", target.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.PlanType != target.PlanType || stored.AuthVersion != target.AuthVersion {
		t.Fatalf("Redis failure mutated target: %#v", stored)
	}
	assertChangePasswordSessionActive(t, auth.db, issued.Session.ID)
	assertChangePasswordAuditCount(t, auth.db, target.ID, models.AuthAuditEventManagedUserUpdated, 0)
}

func TestUpdateManagedUserRejectsAuthVersionOverflowBeforeMutation(t *testing.T) {
	_, auth, actor, target, issued := managedUserSecurityFixture(t)
	if err := auth.db.Model(&models.User{}).Where("id = ?", target.ID).Update("auth_version", 2147483647).Error; err != nil {
		t.Fatal(err)
	}
	plan := models.PlanProfessional
	updated, err := auth.UpdateManagedUser(context.Background(), actor.ID, target.Guid, ManagedUserUpdateInput{PlanType: &plan})
	if updated != nil {
		t.Fatalf("unexpected user: %#v", updated)
	}
	if status, _ := StatusFromError(err); status != 503 {
		t.Fatalf("status=%d err=%v, want 503", status, err)
	}
	var stored models.User
	if err := auth.db.Where("id = ?", target.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.PlanType != target.PlanType || stored.AuthVersion != 2147483647 {
		t.Fatalf("overflow mutated target: %#v", stored)
	}
	assertChangePasswordSessionActive(t, auth.db, issued.Session.ID)
}

func TestUpdateManagedUserRoleAndAuthVersionGuards(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		actor   map[string]any
		target  map[string]any
		self    bool
		allowed bool
	}{
		{name: "ordinary actor", actor: map[string]any{"role": models.UserRoleUser}},
		{name: "disabled actor", actor: map[string]any{"status": models.UserStatusDisabled}},
		{name: "self", self: true},
		{name: "equal role", target: map[string]any{"role": models.UserRoleAdmin}},
		{name: "root target", target: map[string]any{"role": models.UserRoleRoot}},
		{name: "unknown actor role", actor: map[string]any{"role": models.UserRole(999)}},
		{name: "unknown target role", target: map[string]any{"role": models.UserRole(99)}},
		{name: "actor auth version zero", actor: map[string]any{"auth_version": 0}},
		{name: "target auth version zero", target: map[string]any{"auth_version": 0}},
		{name: "root manages admin", actor: map[string]any{"role": models.UserRoleRoot}, target: map[string]any{"role": models.UserRoleAdmin}, allowed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, auth, actor, target, issued := managedUserSecurityFixture(t)
			if len(testCase.actor) != 0 {
				if err := auth.db.Model(&models.User{}).Where("id = ?", actor.ID).Updates(testCase.actor).Error; err != nil {
					t.Fatal(err)
				}
			}
			if len(testCase.target) != 0 {
				if err := auth.db.Model(&models.User{}).Where("id = ?", target.ID).Updates(testCase.target).Error; err != nil {
					t.Fatal(err)
				}
			}
			guid := target.Guid
			if testCase.self {
				guid = actor.Guid
			}
			plan := models.PlanProfessional
			updated, err := auth.UpdateManagedUser(context.Background(), actor.ID, guid, ManagedUserUpdateInput{PlanType: &plan})
			if testCase.allowed {
				if err != nil || updated == nil {
					t.Fatalf("legal management=%#v, %v", updated, err)
				}
				return
			}
			if updated != nil || err == nil {
				t.Fatalf("guard accepted: %#v, %v", updated, err)
			}
			if testCase.self {
				return
			}
			var stored models.User
			if err := auth.db.Where("id = ?", target.ID).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.PlanType == models.PlanProfessional {
				t.Fatalf("guard mutated target: %#v", stored)
			}
			assertChangePasswordSessionActive(t, auth.db, issued.Session.ID)
			assertChangePasswordAuditCount(t, auth.db, target.ID, models.AuthAuditEventManagedUserUpdated, 0)
		})
	}
}

func ptr[T any](value T) *T { return &value }
