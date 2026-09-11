package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type entitlementRedisSetFailureHook struct{ key string }

func (hook entitlementRedisSetFailureHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (hook entitlementRedisSetFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		args := cmd.Args()
		if cmd.Name() == "set" && len(args) > 1 && fmt.Sprint(args[1]) == hook.key {
			return errors.New("a07 entitlement Redis barrier failure")
		}
		return next(ctx, cmd)
	}
}

func (hook entitlementRedisSetFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error { return next(ctx, cmds) }
}

type entitlementFailureMutation struct {
	name       string
	action     string
	prepare    func(*testing.T, *gorm.DB) models.BusinessGroup
	change     func(context.Context, *AdminUserEntitlementService, AdminPermissionReadActor, *models.User, models.BusinessGroup) (*UserReadDTO, error)
	assertSame func(*testing.T, models.User, models.User)
}

func entitlementFailureMutations() []entitlementFailureMutation {
	return []entitlementFailureMutation{
		{
			name: "plan", action: "users.plan.change",
			prepare: func(*testing.T, *gorm.DB) models.BusinessGroup { return models.BusinessGroup{} },
			change: func(ctx context.Context, service *AdminUserEntitlementService, actor AdminPermissionReadActor, target *models.User, _ models.BusinessGroup) (*UserReadDTO, error) {
				return service.ChangePlan(ctx, actor, target.Guid, AdminUserPlanChangeInput{PlanType: models.PlanProfessional, Reason: "failure injection", ExpectedAuthVersion: target.AuthVersion, RequestID: "a07-plan-failure"})
			},
			assertSame: func(t *testing.T, before, after models.User) {
				t.Helper()
				if after.PlanType != before.PlanType || after.DailyCallLimit != before.DailyCallLimit {
					t.Fatalf("plan facts changed: before=%#v after=%#v", before, after)
				}
			},
		},
		{
			name: "group", action: "users.group.change",
			prepare: func(t *testing.T, db *gorm.DB) models.BusinessGroup {
				t.Helper()
				group := models.BusinessGroup{AuditFields: testAuditFields(), Key: "a07-failure-group", DisplayName: "A07 Failure Group", Status: models.BusinessGroupStatusActive}
				if err := db.Create(&group).Error; err != nil {
					t.Fatal(err)
				}
				return group
			},
			change: func(ctx context.Context, service *AdminUserEntitlementService, actor AdminPermissionReadActor, target *models.User, group models.BusinessGroup) (*UserReadDTO, error) {
				return service.ChangeGroup(ctx, actor, target.Guid, AdminUserGroupChangeInput{GroupGUID: group.Guid, Reason: "failure injection", ExpectedAuthVersion: target.AuthVersion, RequestID: "a07-group-failure"})
			},
			assertSame: func(t *testing.T, before, after models.User) {
				t.Helper()
				if after.GroupID != before.GroupID {
					t.Fatalf("group fact changed: before=%d after=%d", before.GroupID, after.GroupID)
				}
			},
		},
	}
}

func TestAdminUserEntitlementRedisBarrierFailureCommitsNoSQLFacts(t *testing.T) {
	for _, mutation := range entitlementFailureMutations() {
		t.Run(mutation.name, func(t *testing.T) {
			ctx, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
			grantAdminUserEntitlementCapabilities(t, edit.db, *actorUser)
			group := mutation.prepare(t, edit.db)
			issued, err := NewSessionService(edit.db, redisStore, testSessionSettings()).Create(ctx, target, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
			if err != nil {
				t.Fatal(err)
			}
			client, ok := redisStore.client.(*redis.Client)
			if !ok {
				t.Fatalf("unexpected Redis fixture client %T", redisStore.client)
			}
			client.AddHook(entitlementRedisSetFailureHook{key: redisStore.revokedKey(issued.Session.SID)})
			result, err := mutation.change(ctx, NewAdminUserEntitlementService(edit.db, redisStore), actor, target, group)
			if result != nil || err == nil {
				t.Fatalf("Redis barrier failure result=%#v err=%v", result, err)
			}
			assertAdminUserEntitlementFailureRollback(t, ctx, edit.db, redisStore, target, issued, mutation, false, err)
		})
	}
}

func TestAdminUserEntitlementPostMutationAuditFailuresRollBackMySQLAndKeepRedisDenial(t *testing.T) {
	for _, mutation := range entitlementFailureMutations() {
		for _, fault := range []string{"auth_audit", "management_audit"} {
			t.Run(mutation.name+"/"+fault, func(t *testing.T) {
				ctx, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
				grantAdminUserEntitlementCapabilities(t, edit.db, *actorUser)
				group := mutation.prepare(t, edit.db)
				issued, err := NewSessionService(edit.db, redisStore, testSessionSettings()).Create(ctx, target, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
				if err != nil {
					t.Fatal(err)
				}
				constraint := fmt.Sprintf("chk_a07_entitlement_%s_%d", fault, testSnowflake.Next())
				table := "auth_audit_events"
				statement := fmt.Sprintf("ALTER TABLE auth_audit_events ADD CONSTRAINT %s CHECK (event_type <> %d)", constraint, models.AuthAuditEventManagedUserUpdated)
				if fault == "management_audit" {
					table = "audit_logs"
					statement = fmt.Sprintf("ALTER TABLE audit_logs ADD CONSTRAINT %s CHECK (action <> '%s')", constraint, mutation.action)
				}
				if err := edit.db.Exec(statement).Error; err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = edit.db.Exec(fmt.Sprintf("ALTER TABLE %s DROP CHECK %s", table, constraint)).Error })
				result, err := mutation.change(ctx, NewAdminUserEntitlementService(edit.db, redisStore), actor, target, group)
				if result != nil || err == nil {
					t.Fatalf("%s failure result=%#v err=%v", fault, result, err)
				}
				assertAdminUserEntitlementFailureRollback(t, ctx, edit.db, redisStore, target, issued, mutation, true, err)
			})
		}
	}
}

func assertAdminUserEntitlementFailureRollback(t *testing.T, ctx context.Context, db *gorm.DB, redisStore *AuthRedis, target *models.User, issued *IssuedSession, mutation entitlementFailureMutation, wantRedisDenial bool, failure error) {
	t.Helper()
	if status, message := StatusFromError(failure); status != 503 || strings.Contains(message, "a07 entitlement") {
		t.Fatalf("failure status/message=%d/%q err=%v", status, message, failure)
	}
	var stored models.User
	if err := db.First(&stored, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AuthVersion != target.AuthVersion {
		t.Fatalf("auth_version changed: before=%d after=%d", target.AuthVersion, stored.AuthVersion)
	}
	mutation.assertSame(t, *target, stored)
	var session models.Session
	if err := db.First(&session, issued.Session.ID).Error; err != nil || session.RevokedAt != nil || session.SessionVersion != issued.Session.SessionVersion {
		t.Fatalf("session SQL fact changed=%#v err=%v", session, err)
	}
	for event, name := range map[models.AuthAuditEventType]string{
		models.AuthAuditEventSessionRevoked:     "session revoke",
		models.AuthAuditEventManagedUserUpdated: "managed update",
	} {
		var count int64
		if err := db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ? AND is_deleted = 0", target.ID, event).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s auth audit count=%d err=%v", name, count, err)
		}
	}
	var managementCount int64
	if err := db.Model(&models.AuditLog{}).Where("user_id = ? AND action = ? AND is_deleted = 0", target.ID, mutation.action).Count(&managementCount).Error; err != nil || managementCount != 0 {
		t.Fatalf("management audit count=%d err=%v", managementCount, err)
	}
	if revoked, err := redisStore.IsSessionRevoked(ctx, issued.Session.SID); err != nil || revoked != wantRedisDenial {
		t.Fatalf("Redis denial=%t want=%t err=%v", revoked, wantRedisDenial, err)
	}
}
