package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
)

func TestAdminUserEntitlementChangesRevokeSessionsAndPreserveCounters(t *testing.T) {
	ctx, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	grantAdminUserEntitlementCapabilities(t, edit.db, *actorUser)
	target.DailyCallLimit, target.DailyCallsUsed = 17, 9
	if err := edit.db.Model(target).Updates(map[string]any{"daily_call_limit": 17, "daily_calls_used": 9}).Error; err != nil {
		t.Fatal(err)
	}
	issued, err := NewSessionService(edit.db, redisStore, testSessionSettings()).Create(ctx, target, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	service := NewAdminUserEntitlementService(edit.db, redisStore)
	key, _, err := NewGatewayTokenService(edit.db).Create(target, GatewayTokenCreateInput{Name: "a07-key"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.ChangePlan(ctx, actor, target.Guid, AdminUserPlanChangeInput{PlanType: models.PlanProfessional, Reason: "grant plan", ExpectedAuthVersion: target.AuthVersion, RequestID: "a07-plan"})
	if err != nil || plan == nil || plan.PlanType != "professional" || plan.AuthVersion != target.AuthVersion+1 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	var stored models.User
	if err := edit.db.First(&stored, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.DailyCallLimit != 100 || stored.DailyCallsUsed != 9 || stored.PlanType != models.PlanProfessional {
		t.Fatalf("stored plan/counters=%#v", stored)
	}
	if revoked, err := redisStore.IsSessionRevoked(ctx, issued.Session.SID); err != nil || !revoked {
		t.Fatalf("revoked=%t err=%v", revoked, err)
	}
	assertAdminUserEntitlementAudit(t, edit.db, actorUser.Guid, target.ID, target.Guid, "users.plan.change", "a07-plan", "grant_adjustment", 7)
	assertAdminUserEntitlementEventCounts(t, edit.db, target.ID, 1, 1)

	group := models.BusinessGroup{AuditFields: testAuditFields(), Key: "research", DisplayName: "Research", Status: models.BusinessGroupStatusActive}
	if err := edit.db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	groupResult, err := service.ChangeGroup(ctx, actor, target.Guid, AdminUserGroupChangeInput{GroupGUID: group.Guid, Reason: "move group", ExpectedAuthVersion: plan.AuthVersion, RequestID: "a07-group"})
	if err != nil || groupResult == nil || groupResult.Group == nil || *groupResult.Group != "research" || groupResult.AuthVersion != plan.AuthVersion+1 {
		t.Fatalf("group=%#v err=%v", groupResult, err)
	}
	if err := edit.db.First(&stored, target.ID).Error; err != nil || stored.GroupID != group.ID || stored.DailyCallsUsed != 9 {
		t.Fatalf("stored group=%#v err=%v", stored, err)
	}
	assertAdminUserEntitlementAudit(t, edit.db, actorUser.Guid, target.ID, target.Guid, "users.group.change", "a07-group", "", 6)
	assertAdminUserEntitlementEventCounts(t, edit.db, target.ID, 1, 2)
	var storedKey models.GatewayAPIToken
	if err := edit.db.First(&storedKey, key.ID).Error; err != nil || storedKey.Status != models.GatewayTokenActive {
		t.Fatalf("gateway key changed=%#v err=%v", storedKey, err)
	}
}

func TestAdminUserGroupChangeHidesUnavailableGroupsAndRejectsSameGroup(t *testing.T) {
	ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	var actorUser models.User
	if err := edit.db.First(&actorUser, actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	grantAdminUserEntitlementCapabilities(t, edit.db, actorUser)
	service := NewAdminUserEntitlementService(edit.db, redisStore)
	var current models.BusinessGroup
	if err := edit.db.First(&current, target.GroupID).Error; err != nil {
		t.Fatal(err)
	}
	for name, group := range map[string]models.BusinessGroup{
		"inactive": {AuditFields: testAuditFields(), Key: "inactive-a07", DisplayName: "Inactive", Status: models.BusinessGroupStatusInactive},
		"deleted":  {AuditFields: func() models.AuditFields { value := testAuditFields(); value.IsDeleted = 1; return value }(), Key: "deleted-a07", DisplayName: "Deleted", Status: models.BusinessGroupStatusActive},
	} {
		t.Run(name, func(t *testing.T) {
			if err := edit.db.Create(&group).Error; err != nil {
				t.Fatal(err)
			}
			got, err := service.ChangeGroup(ctx, actor, target.Guid, AdminUserGroupChangeInput{GroupGUID: group.Guid, Reason: "move", ExpectedAuthVersion: target.AuthVersion, RequestID: name})
			if got != nil || AdminUserEntitlementNotFoundCode(err) != "group_not_found" {
				t.Fatalf("result=%#v err=%v", got, err)
			}
		})
	}
	got, err := service.ChangeGroup(ctx, actor, target.Guid, AdminUserGroupChangeInput{GroupGUID: current.Guid, Reason: "same", ExpectedAuthVersion: target.AuthVersion, RequestID: "same"})
	if got != nil || AdminUserEntitlementConflictCode(err) != "user_group_conflict" {
		t.Fatalf("result=%#v err=%v", got, err)
	}
}

func TestAdminUserEntitlementRejectsConflictsWithoutRevocation(t *testing.T) {
	ctx, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	grantAdminUserEntitlementCapabilities(t, edit.db, *actorUser)
	issued, err := NewSessionService(edit.db, redisStore, testSessionSettings()).Create(ctx, target, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	service := NewAdminUserEntitlementService(edit.db, redisStore)
	for name, input := range map[string]AdminUserPlanChangeInput{
		"same":  {PlanType: target.PlanType, Reason: "same", ExpectedAuthVersion: target.AuthVersion, RequestID: "same"},
		"stale": {PlanType: models.PlanProfessional, Reason: "stale", ExpectedAuthVersion: target.AuthVersion + 1, RequestID: "stale"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := service.ChangePlan(ctx, actor, target.Guid, input)
			if got != nil || AdminUserEntitlementConflictCode(err) == "" {
				t.Fatalf("result=%#v err=%v", got, err)
			}
			if revoked, checkErr := redisStore.IsSessionRevoked(ctx, issued.Session.SID); checkErr != nil || revoked {
				t.Fatalf("conflict revoked session=%t err=%v", revoked, checkErr)
			}
		})
	}
}

func TestAdminUserPlanChangeControlsBearerAccessRuntimeQuota(t *testing.T) {
	for _, tc := range []struct {
		name       string
		beforePlan models.PlanType
		plan       models.PlanType
		used       int
		calls      int
		wantStatus int
	}{
		{name: "free_enforces_100", beforePlan: models.PlanProfessional, plan: models.PlanFree, used: 99, calls: 2, wantStatus: 429},
		{name: "professional_is_unlimited", beforePlan: models.PlanFree, plan: models.PlanProfessional, used: 100, calls: 2},
		{name: "enterprise_is_unlimited", beforePlan: models.PlanFree, plan: models.PlanEnterprise, used: 100, calls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
			grantAdminUserEntitlementCapabilities(t, edit.db, *actorUser)
			now := time.Now().UTC().UnixMilli()
			if err := edit.db.Model(target).Updates(map[string]any{"plan_type": tc.beforePlan, "daily_calls_used": tc.used, "daily_calls_reset_at": now}).Error; err != nil {
				t.Fatal(err)
			}
			result, err := NewAdminUserEntitlementService(edit.db, redisStore).ChangePlan(ctx, actor, target.Guid, AdminUserPlanChangeInput{PlanType: tc.plan, Reason: "runtime quota", ExpectedAuthVersion: target.AuthVersion, RequestID: "a07-runtime-" + tc.name})
			if err != nil || result == nil || result.PlanType != tc.plan.String() {
				t.Fatalf("ChangePlan result=%#v err=%v", result, err)
			}
			var runtimeUser models.User
			if err := edit.db.First(&runtimeUser, target.ID).Error; err != nil {
				t.Fatal(err)
			}
			sessions := NewSessionService(edit.db, redisStore, testSessionSettings())
			issued, err := sessions.Create(ctx, &runtimeUser, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.81"})
			if err != nil {
				t.Fatal(err)
			}
			access, err := security.CreateAccessToken(fmt.Sprint(runtimeUser.Guid), "a07-runtime-jwt", 5, map[string]interface{}{"sid": issued.Session.SID, "sv": issued.Session.SessionVersion, "av": runtimeUser.AuthVersion, "role": int(runtimeUser.Role)})
			if err != nil {
				t.Fatal(err)
			}
			claims, err := security.DecodeAccessToken(access, "a07-runtime-jwt")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sessions.Validate(ctx, claims["sid"].(string), runtimeUser.ID, int(claims["sv"].(float64)), int(claims["av"].(float64))); err != nil {
				t.Fatalf("Bearer Access validation: %v", err)
			}
			billing := NewBillingService(&config.Settings{})
			var consumeErr error
			for range tc.calls {
				consumeErr = billing.CheckAndConsumeCall(edit.db, &runtimeUser, 1)
				if consumeErr != nil {
					break
				}
			}
			gotStatus := 0
			if consumeErr != nil {
				gotStatus, _ = StatusFromError(consumeErr)
			}
			if gotStatus != tc.wantStatus {
				t.Fatalf("runtime quota status=%d err=%v want=%d", gotStatus, consumeErr, tc.wantStatus)
			}
		})
	}
}

func TestAdminUserEntitlementConcurrentStaleVersionCommitsOneMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*AdminUserEntitlementService, AdminPermissionReadActor, *models.User, models.BusinessGroup, string) (*UserReadDTO, error)
	}{
		{name: "plan", run: func(service *AdminUserEntitlementService, actor AdminPermissionReadActor, target *models.User, _ models.BusinessGroup, requestID string) (*UserReadDTO, error) {
			return service.ChangePlan(context.Background(), actor, target.Guid, AdminUserPlanChangeInput{PlanType: models.PlanProfessional, Reason: "concurrent plan", ExpectedAuthVersion: target.AuthVersion, RequestID: requestID})
		}},
		{name: "group", run: func(service *AdminUserEntitlementService, actor AdminPermissionReadActor, target *models.User, group models.BusinessGroup, requestID string) (*UserReadDTO, error) {
			return service.ChangeGroup(context.Background(), actor, target.Guid, AdminUserGroupChangeInput{GroupGUID: group.Guid, Reason: "concurrent group", ExpectedAuthVersion: target.AuthVersion, RequestID: requestID})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
			grantAdminUserEntitlementCapabilities(t, edit.db, *actorUser)
			group := models.BusinessGroup{AuditFields: testAuditFields(), Key: "concurrent-" + tc.name, DisplayName: "Concurrent " + tc.name, Status: models.BusinessGroupStatusActive}
			if err := edit.db.Create(&group).Error; err != nil {
				t.Fatal(err)
			}
			service := NewAdminUserEntitlementService(edit.db, redisStore)
			start := make(chan struct{})
			results := make(chan error, 2)
			requestIDs := [2]string{newRealIdempotencyKey(t), newRealIdempotencyKey(t)}
			for index := range 2 {
				go func(requestID string) {
					<-start
					result, err := tc.run(service, actor, target, group, requestID)
					if err == nil && result == nil {
						err = fmt.Errorf("nil successful entitlement result")
					}
					results <- err
				}(requestIDs[index])
			}
			close(start)
			successes, conflicts := 0, 0
			for range 2 {
				err := <-results
				switch {
				case err == nil:
					successes++
				case AdminUserEntitlementConflictCode(err) == "auth_version_conflict":
					conflicts++
				default:
					t.Fatalf("concurrent result err=%v", err)
				}
			}
			if successes != 1 || conflicts != 1 {
				t.Fatalf("success/conflict=%d/%d want=1/1", successes, conflicts)
			}
			var stored models.User
			if err := edit.db.First(&stored, target.ID).Error; err != nil || stored.AuthVersion != target.AuthVersion+1 {
				t.Fatalf("stored=%#v err=%v", stored, err)
			}
		})
	}
}

func grantAdminUserEntitlementCapabilities(t *testing.T, db *gorm.DB, actor models.User) {
	t.Helper()
	actorID := actor.ID
	head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: actor.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 2}
	head.CreatedBy, head.UpdatedBy = &actorID, &actorID
	if err := db.Create(&head).Error; err != nil {
		t.Fatal(err)
	}
	effect, ok := models.PermissionEffectCode("allow")
	if !ok {
		t.Fatal("allow permission effect unavailable")
	}
	for _, name := range []string{"users.plan.change", "users.group.change"} {
		capability, found := models.PermissionCapabilityCode(name)
		if !found {
			t.Fatalf("capability %q unavailable", name)
		}
		rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: actor.ID, PolicyVersion: 1, Capability: capability, Effect: effect}
		rule.CreatedBy, rule.UpdatedBy = &actorID, &actorID
		if err := db.Create(&rule).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func assertAdminUserEntitlementAudit(t *testing.T, db *gorm.DB, actorGUID, targetID, targetGUID int64, action, requestID, changeKind string, keyCount int) {
	t.Helper()
	var audit models.AuditLog
	if err := db.Where("user_id = ? AND action = ? AND is_deleted = 0", targetID, action).Order("id DESC").First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if len(audit.Detail) != keyCount || audit.Detail["request_id"] != requestID || audit.Detail["reason"] == "" || audit.Detail["actor_guid"] != float64(actorGUID) || audit.Detail["target_guid"] != float64(targetGUID) {
		t.Fatalf("audit detail=%#v", audit.Detail)
	}
	if changeKind != "" && audit.Detail["change_kind"] != changeKind {
		t.Fatalf("audit detail=%#v", audit.Detail)
	}
}

func assertAdminUserEntitlementEventCounts(t *testing.T, db *gorm.DB, targetID int64, revoked, changed int64) {
	t.Helper()
	var revokedCount, changedCount int64
	if err := db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ? AND is_deleted = 0", targetID, models.AuthAuditEventSessionRevoked).Count(&revokedCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ? AND is_deleted = 0", targetID, models.AuthAuditEventManagedUserUpdated).Count(&changedCount).Error; err != nil {
		t.Fatal(err)
	}
	if revokedCount != revoked || changedCount != changed {
		t.Fatalf("event counts revoked/changed=%d/%d want=%d/%d", revokedCount, changedCount, revoked, changed)
	}
}
