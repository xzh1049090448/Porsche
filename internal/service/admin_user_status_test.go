package service

import (
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestAdminUserStatusDisableAndEnableCredentialSemantics(t *testing.T) {
	ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	targetSession, err := NewSessionService(edit.db, redisStore, testSessionSettings()).Create(ctx, target, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	service := NewAdminUserStatusService(edit.db, redisStore)
	reason := "risk review"
	disabled, err := service.Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusDisabled, Reason: &reason, ExpectedAuthVersion: target.AuthVersion})
	if err != nil || disabled == nil || disabled.Status != "disabled" || disabled.AuthVersion != target.AuthVersion+1 {
		t.Fatalf("disable=%#v err=%v", disabled, err)
	}
	if revoked, err := redisStore.IsSessionRevoked(ctx, targetSession.Session.SID); err != nil || !revoked {
		t.Fatalf("target session revoked=%t err=%v", revoked, err)
	}
	var storedSession models.Session
	if err := edit.db.Where("sid = ?", targetSession.Session.SID).First(&storedSession).Error; err != nil || storedSession.RevokedAt == nil {
		t.Fatalf("stored session=%#v err=%v", storedSession, err)
	}
	var audit models.AuditLog
	if err := edit.db.Where("user_id = ? AND action = ? AND is_deleted = 0", target.ID, "users.disable").Order("id DESC").First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.Detail["reason"] != reason || audit.Detail["before_status"] != "active" || audit.Detail["after_status"] != "disabled" {
		t.Fatalf("audit detail=%#v", audit.Detail)
	}

	enabled, err := service.Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusActive, ExpectedAuthVersion: disabled.AuthVersion})
	if err != nil || enabled == nil || enabled.Status != "active" || enabled.AuthVersion != disabled.AuthVersion+1 {
		t.Fatalf("enable=%#v err=%v", enabled, err)
	}
	if revoked, err := redisStore.IsSessionRevoked(ctx, targetSession.Session.SID); err != nil || !revoked {
		t.Fatalf("enable restored session: revoked=%t err=%v", revoked, err)
	}
	var enableAudit models.AuditLog
	if err := edit.db.Where("user_id = ? AND action = ? AND is_deleted = 0", target.ID, "users.enable").Order("id DESC").First(&enableAudit).Error; err != nil {
		t.Fatal(err)
	}
	if _, exists := enableAudit.Detail["reason"]; exists || enableAudit.Detail["before_status"] != "disabled" || enableAudit.Detail["after_status"] != "active" {
		t.Fatalf("enable audit detail=%#v", enableAudit.Detail)
	}
}

func TestAdminUserStatusAccessRefreshAndGatewayKeyMatrix(t *testing.T) {
	ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	sessions := NewSessionService(edit.db, redisStore, testSessionSettings())
	issued, err := sessions.Create(ctx, target, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	keys := NewGatewayTokenService(edit.db)
	keyRow, secret, err := keys.Create(target, GatewayTokenCreateInput{Name: "a06-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.AuthenticatePrincipal(secret, "127.0.0.1", "", time.Now()); err != nil {
		t.Fatalf("active key rejected: %v", err)
	}
	statusService := NewAdminUserStatusService(edit.db, redisStore)
	reason := "security review"
	disabled, err := statusService.Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusDisabled, Reason: &reason, ExpectedAuthVersion: target.AuthVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Validate(ctx, issued.Session.SID, target.ID, issued.Session.SessionVersion, target.AuthVersion); err == nil {
		t.Fatal("old Access/session proof remained valid after disable")
	}
	if _, err := sessions.Refresh(ctx, issued.RefreshToken); err == nil {
		t.Fatal("old Refresh remained valid after disable")
	}
	if _, err := keys.AuthenticatePrincipal(secret, "127.0.0.1", "", time.Now()); !IsGatewayTokenError(err, GatewayTokenDisabled) {
		t.Fatalf("disabled-owner key error=%v", err)
	}
	enabled, err := statusService.Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusActive, ExpectedAuthVersion: disabled.AuthVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Refresh(ctx, issued.RefreshToken); err == nil {
		t.Fatal("enable restored old Refresh")
	}
	if _, err := keys.AuthenticatePrincipal(secret, "127.0.0.1", "", time.Now()); err != nil {
		t.Fatalf("still-active key did not follow current owner state: %v", err)
	}
	if err := keys.Revoke(target.ID, keyRow.Guid); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.AuthenticatePrincipal(secret, "127.0.0.1", "", time.Now()); !IsGatewayTokenError(err, GatewayTokenRevoked) {
		t.Fatalf("revoked key error=%v", err)
	}
	_ = enabled
}

func TestAdminUserStatusRejectsStaleVersionAndSameState(t *testing.T) {
	ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	service := NewAdminUserStatusService(edit.db, redisStore)
	reason := "review"
	for name, input := range map[string]AdminUserStatusInput{
		"stale": {Status: models.UserStatusDisabled, Reason: &reason, ExpectedAuthVersion: target.AuthVersion + 1},
		"same":  {Status: models.UserStatusActive, ExpectedAuthVersion: target.AuthVersion},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := service.Change(ctx, actor, target.Guid, input)
			if got != nil {
				t.Fatalf("result=%#v", got)
			}
			if status, _ := StatusFromError(err); status != 409 {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}
}

func TestAdminUserStatusUsesTransitionCapabilityAndFreshHierarchy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capability string
		status     models.UserStatus
		reason     *string
	}{
		{name: "disable", capability: "users.disable", status: models.UserStatusDisabled, reason: stringPointer("review")},
		{name: "enable", capability: "users.enable", status: models.UserStatusActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
			if tc.status == models.UserStatusActive {
				if err := edit.db.Model(target).Update("status", models.UserStatusDisabled).Error; err != nil {
					t.Fatal(err)
				}
			}
			capability, _ := models.PermissionCapabilityCode(tc.capability)
			head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: actorUser.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
			rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: actorUser.ID, PolicyVersion: 1, Capability: capability, Effect: 3}
			if err := edit.db.Create(&head).Error; err != nil {
				t.Fatal(err)
			}
			if err := edit.db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
			got, err := NewAdminUserStatusService(edit.db, redisStore).Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: tc.status, Reason: tc.reason, ExpectedAuthVersion: target.AuthVersion})
			if got != nil {
				t.Fatalf("result=%#v", got)
			}
			if status, _ := StatusFromError(err); status != 403 {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}

	for _, name := range []string{"self", "equal", "root", "deleted"} {
		t.Run(name, func(t *testing.T) {
			ctx, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
			guid := target.Guid
			switch name {
			case "self":
				guid = actorUser.Guid
			case "equal":
				if err := edit.db.Model(target).Update("role", models.UserRoleAdmin).Error; err != nil {
					t.Fatal(err)
				}
			case "root":
				if err := edit.db.Model(target).Update("role", models.UserRoleRoot).Error; err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := edit.db.Model(target).Update("is_deleted", 1).Error; err != nil {
					t.Fatal(err)
				}
			}
			reason := "review"
			got, err := NewAdminUserStatusService(edit.db, redisStore).Change(ctx, actor, guid, AdminUserStatusInput{Status: models.UserStatusDisabled, Reason: &reason, ExpectedAuthVersion: target.AuthVersion})
			if got != nil {
				t.Fatalf("result=%#v", got)
			}
			if status, _ := StatusFromError(err); status != 404 {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}
}

func TestAdminUserStatusRejectsRevokedActorWithoutTargetMutation(t *testing.T) {
	ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	if err := redisStore.MarkSessionRevoked(ctx, actor.SessionSID, 60_000_000_000); err != nil {
		t.Fatal(err)
	}
	reason := "review"
	got, err := NewAdminUserStatusService(edit.db, redisStore).Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusDisabled, Reason: &reason, ExpectedAuthVersion: target.AuthVersion})
	if got != nil {
		t.Fatalf("result=%#v", got)
	}
	if status, _ := StatusFromError(err); status != 401 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	var stored models.User
	if err := edit.db.First(&stored, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.UserStatusActive || stored.AuthVersion != target.AuthVersion {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestAdminUserStatusRejectsRedisFailureWithoutTargetMutation(t *testing.T) {
	ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	if err := redisStore.Close(); err != nil {
		t.Fatal(err)
	}
	reason := "review"
	got, err := NewAdminUserStatusService(edit.db, redisStore).Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusDisabled, Reason: &reason, ExpectedAuthVersion: target.AuthVersion})
	if got != nil {
		t.Fatalf("result=%#v", got)
	}
	if status, _ := StatusFromError(err); status != 503 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	var stored models.User
	if err := edit.db.First(&stored, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != target.Status || stored.AuthVersion != target.AuthVersion {
		t.Fatalf("Redis failure mutated target=%#v", stored)
	}
	assertChangePasswordAuditCount(t, edit.db, target.ID, models.AuthAuditEventUserDisabled, 0)
}

func TestAdminUserStatusEnableRevokesLegacyActiveSessions(t *testing.T) {
	ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	legacy, err := NewSessionService(edit.db, redisStore, testSessionSettings()).Create(ctx, target, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	if err := edit.db.Model(target).Update("status", models.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	result, err := NewAdminUserStatusService(edit.db, redisStore).Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusActive, ExpectedAuthVersion: target.AuthVersion})
	if err != nil || result == nil || result.Status != "active" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if revoked, err := redisStore.IsSessionRevoked(ctx, legacy.Session.SID); err != nil || !revoked {
		t.Fatalf("legacy Redis session revoked=%t err=%v", revoked, err)
	}
	var stored models.Session
	if err := edit.db.Where("sid = ?", legacy.Session.SID).First(&stored).Error; err != nil || stored.RevokedAt == nil {
		t.Fatalf("legacy DB session=%#v err=%v", stored, err)
	}
}
