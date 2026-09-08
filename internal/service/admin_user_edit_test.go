package service

import (
	"bytes"
	"context"
	"log"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLockNicknameEditPolicyBindsActorUserIDToEveryLockQuery(t *testing.T) {
	db := actionIssueDryDB(t)
	const actorUserID int64 = 4242
	var output bytes.Buffer
	db.Logger = logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info, ParameterizedQueries: false})
	_, _ = lockNicknameEditPolicy(db, actorUserID)
	queries := output.String()
	for _, table := range []string{"user_permission_heads", "user_permission_overrides"} {
		line := "SELECT `id` FROM `" + table + "` WHERE user_id = 4242 ORDER BY id ASC FOR UPDATE"
		if !strings.Contains(queries, line) {
			t.Fatalf("%s lock did not bind actor user ID; SQL log:\n%s", table, queries)
		}
	}
}

func adminUserNicknameEditFixture(t *testing.T, actorRole, targetRole models.UserRole) (context.Context, *AdminUserNicknameEditService, *AuthRedis, *models.User, *models.User, AdminPermissionReadActor) {
	t.Helper()
	ctx := context.Background()
	db := openRootTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	redisStore := openTestAuthRedis(t)
	actor := createAuthSessionTestUser(t, db)
	target := createAuthSessionTestUser(t, db)
	if err := db.Model(actor).Update("role", actorRole).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(target).Update("role", targetRole).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(actor, actor.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(target, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	issued, err := NewSessionService(db, redisStore, testSessionSettings()).Create(ctx, actor, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	claims := AdminPermissionReadActor{UserID: actor.ID, AuthVersion: actor.AuthVersion, SessionSID: issued.Session.SID, SessionVersion: issued.Session.SessionVersion}
	return ctx, NewAdminUserNicknameEditService(db, redisStore), redisStore, actor, target, claims
}

func TestAdminUserNicknameEditAuthorizationAndSetClear(t *testing.T) {
	for _, tc := range []struct {
		name          string
		actor, target models.UserRole
	}{
		{"root_to_user", models.UserRoleRoot, models.UserRoleUser},
		{"root_to_admin", models.UserRoleRoot, models.UserRoleAdmin},
		{"admin_to_user", models.UserRoleAdmin, models.UserRoleUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, service, redisStore, _, target, claims := adminUserNicknameEditFixture(t, tc.actor, tc.target)
			before := *target
			before.AllowedModels = slices.Clone(target.AllowedModels)
			var beforeSession models.Session
			if err := service.db.Where("sid = ?", claims.SessionSID).First(&beforeSession).Error; err != nil {
				t.Fatal(err)
			}
			beforeVersion := target.AuthVersion
			nickname := "New display"
			got, err := service.Edit(ctx, claims, target.Guid, AdminUserNicknameEditInput{Nickname: &nickname, ExpectedAuthVersion: beforeVersion})
			if err != nil || got == nil || got.Nickname == nil || *got.Nickname != nickname || got.Group == nil || *got.Group == "" {
				t.Fatalf("set=%#v err=%v", got, err)
			}
			var stored models.User
			if err := service.db.First(&stored, target.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.AuthVersion != beforeVersion || stored.Role != tc.target || stored.Status != before.Status || stored.PlanType != before.PlanType || stored.GroupID != before.GroupID ||
				!slices.Equal(stored.AllowedModels, before.AllowedModels) || stored.DailyCallLimit != before.DailyCallLimit || stored.DailyCallsUsed != before.DailyCallsUsed ||
				stored.TotalTokensUsed != before.TotalTokensUsed || stored.Username == nil != (before.Username == nil) || stored.CreatedAt != before.CreatedAt || stored.CreatedBy == nil != (before.CreatedBy == nil) {
				t.Fatalf("security fields changed: %#v", stored)
			}
			var afterSession models.Session
			if err := service.db.Where("sid = ?", claims.SessionSID).First(&afterSession).Error; err != nil {
				t.Fatal(err)
			}
			if afterSession.SessionVersion != beforeSession.SessionVersion || afterSession.RevokedAt != nil || afterSession.IsDeleted != beforeSession.IsDeleted {
				t.Fatalf("session state changed: %#v", afterSession)
			}
			if revoked, err := redisStore.IsSessionRevoked(ctx, claims.SessionSID); err != nil || revoked {
				t.Fatalf("actor session changed: revoked=%t err=%v", revoked, err)
			}
			got, err = service.Edit(ctx, claims, target.Guid, AdminUserNicknameEditInput{ClearNickname: true, ExpectedAuthVersion: beforeVersion})
			if err != nil || got == nil || got.Nickname != nil {
				t.Fatalf("clear=%#v err=%v", got, err)
			}
			assertChangePasswordAuditCount(t, service.db, target.ID, models.AuthAuditEventManagedUserUpdated, 2)
		})
	}
}

func TestAdminUserNicknameEditNoopDoesNotAudit(t *testing.T) {
	ctx, service, _, _, target, claims := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	input := AdminUserNicknameEditInput{ExpectedAuthVersion: target.AuthVersion}
	if target.Nickname == nil {
		input.ClearNickname = true
	} else {
		nickname := *target.Nickname
		input.Nickname = &nickname
	}
	got, err := service.Edit(ctx, claims, target.Guid, input)
	if err != nil || got == nil {
		t.Fatalf("noop=%#v err=%v", got, err)
	}
	assertChangePasswordAuditCount(t, service.db, target.ID, models.AuthAuditEventManagedUserUpdated, 0)
}

func TestAdminUserNicknameEditRejectsHiddenForbiddenAndConflict(t *testing.T) {
	for _, name := range []string{"self", "equal", "root", "missing", "deleted", "capability_deny", "version"} {
		t.Run(name, func(t *testing.T) {
			ctx, service, _, actor, target, claims := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
			guid, expected, want := target.Guid, target.AuthVersion, 404
			switch name {
			case "self":
				guid = actor.Guid
			case "equal":
				if err := service.db.Model(target).Update("role", models.UserRoleAdmin).Error; err != nil {
					t.Fatal(err)
				}
			case "root":
				if err := service.db.Model(target).Update("role", models.UserRoleRoot).Error; err != nil {
					t.Fatal(err)
				}
			case "missing":
				guid = 9223372036854775807
			case "deleted":
				if err := service.db.Model(target).Update("is_deleted", 1).Error; err != nil {
					t.Fatal(err)
				}
			case "capability_deny":
				want = 403
				capability, _ := models.PermissionCapabilityCode("users.edit")
				head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: actor.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
				rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: actor.ID, PolicyVersion: 1, Capability: capability, Effect: 3}
				if err := service.db.Create(&head).Error; err != nil {
					t.Fatal(err)
				}
				if err := service.db.Create(&rule).Error; err != nil {
					t.Fatal(err)
				}
			case "version":
				expected++
				want = 409
			}
			nickname := "blocked"
			got, err := service.Edit(ctx, claims, guid, AdminUserNicknameEditInput{Nickname: &nickname, ExpectedAuthVersion: expected})
			if got != nil {
				t.Fatalf("unexpected result: %#v", got)
			}
			if status, _ := StatusFromError(err); status != want {
				t.Fatalf("status=%d err=%v want=%d", status, err, want)
			}
			assertChangePasswordAuditCount(t, service.db, target.ID, models.AuthAuditEventManagedUserUpdated, 0)
		})
	}
}

func TestAdminUserNicknameEditRejectsStaleIdentitySessionAndCorruptPolicy(t *testing.T) {
	for _, name := range []string{"disabled_actor", "stale_actor", "stale_session", "redis_revoked", "corrupt_policy"} {
		t.Run(name, func(t *testing.T) {
			ctx, service, redisStore, actor, target, claims := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
			switch name {
			case "disabled_actor":
				if err := service.db.Model(actor).Update("status", models.UserStatusDisabled).Error; err != nil {
					t.Fatal(err)
				}
			case "stale_actor":
				if err := service.db.Model(actor).Update("auth_version", actor.AuthVersion+1).Error; err != nil {
					t.Fatal(err)
				}
			case "stale_session":
				if err := service.db.Model(&models.Session{}).Where("sid = ?", claims.SessionSID).Update("session_version", claims.SessionVersion+1).Error; err != nil {
					t.Fatal(err)
				}
			case "redis_revoked":
				if err := redisStore.MarkSessionRevoked(ctx, claims.SessionSID, time.Minute); err != nil {
					t.Fatal(err)
				}
			case "corrupt_policy":
				head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: actor.ID, PolicyVersion: 1, CatalogVersion: 999, RuleCount: 0}
				if err := service.db.Create(&head).Error; err != nil {
					t.Fatal(err)
				}
			}
			nickname := "blocked"
			got, err := service.Edit(ctx, claims, target.Guid, AdminUserNicknameEditInput{Nickname: &nickname, ExpectedAuthVersion: target.AuthVersion})
			if got != nil {
				t.Fatalf("unexpected result: %#v", got)
			}
			status, _ := StatusFromError(err)
			if name == "corrupt_policy" {
				if status != 503 {
					t.Fatalf("status=%d err=%v", status, err)
				}
			} else if status != 401 {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}
}

func TestAdminUserNicknameEditConstructorAndInputFailClosed(t *testing.T) {
	if service := NewAdminUserNicknameEditService(nil, nil); service == nil {
		t.Fatal("constructor must return fail-closed service")
	}
	nickname := "x"
	for _, input := range []AdminUserNicknameEditInput{{Nickname: &nickname}, {Nickname: &nickname, ClearNickname: true, ExpectedAuthVersion: 1}} {
		got, err := NewAdminUserNicknameEditService(nil, nil).Edit(context.Background(), AdminPermissionReadActor{}, 1, input)
		if got != nil || err == nil {
			t.Fatalf("invalid input accepted: %#v %v", got, err)
		}
	}
}

func TestAdminUserNicknameEditCorruptGORMFailsClosedWithoutPanic(t *testing.T) {
	service := &AdminUserNicknameEditService{db: &gorm.DB{}, redis: &AuthRedis{}, now: func() int64 { return 100 }}
	actor := AdminPermissionReadActor{UserID: 1, AuthVersion: 2, SessionSID: "11111111-2222-4333-8444-555555555555", SessionVersion: 3}
	nickname := "valid"
	got, err := service.Edit(context.Background(), actor, 2, AdminUserNicknameEditInput{Nickname: &nickname, ExpectedAuthVersion: 1})
	if got != nil {
		t.Fatalf("corrupt DB returned result: %#v", got)
	}
	if status, _ := StatusFromError(err); status != 503 {
		t.Fatalf("corrupt DB status=%d err=%v", status, err)
	}
}

func TestAdminUserNicknameEditPureGuardsRunWithoutExternalFixtures(t *testing.T) {
	service := &AdminUserNicknameEditService{db: actionIssueDryDB(t), redis: &AuthRedis{}, now: func() int64 { return 100 }}
	actor := AdminPermissionReadActor{UserID: 1, AuthVersion: 2, SessionSID: "11111111-2222-4333-8444-555555555555", SessionVersion: 3}
	valid := "valid"
	for _, tc := range []struct {
		name  string
		guid  int64
		input AdminUserNicknameEditInput
		want  int
	}{
		{"invalid_guid", 0, AdminUserNicknameEditInput{Nickname: &valid, ExpectedAuthVersion: 1}, 400},
		{"missing_intent", 1, AdminUserNicknameEditInput{ExpectedAuthVersion: 1}, 400},
		{"ambiguous_clear", 1, AdminUserNicknameEditInput{Nickname: &valid, ClearNickname: true, ExpectedAuthVersion: 1}, 400},
		{"zero_version", 1, AdminUserNicknameEditInput{Nickname: &valid}, 400},
		{"overflow_version", 1, AdminUserNicknameEditInput{Nickname: &valid, ExpectedAuthVersion: 2147483647 + 1}, 400},
		{"unnormalized", 1, AdminUserNicknameEditInput{Nickname: stringPointerForEdit(" valid "), ExpectedAuthVersion: 1}, 400},
		{"too_long", 1, AdminUserNicknameEditInput{Nickname: stringPointerForEdit(strings.Repeat("界", 65)), ExpectedAuthVersion: 1}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status, _ := StatusFromError(validateAdminUserNicknameEditCall(service, context.Background(), actor, tc.guid, tc.input)); status != tc.want {
				t.Fatalf("status=%d want=%d", status, tc.want)
			}
		})
	}
	if err := validateAdminUserNicknameEditCall(service, context.Background(), actor, 1, AdminUserNicknameEditInput{Nickname: &valid, ExpectedAuthVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminUserNicknameEditCall(service, context.Background(), actor, 1, AdminUserNicknameEditInput{ClearNickname: true, ExpectedAuthVersion: 1}); err != nil {
		t.Fatal(err)
	}

	stored := models.User{ID: 1, AuditFields: models.AuditFields{Guid: 11}, Role: models.UserRoleAdmin, Status: models.UserStatusActive, AuthVersion: 2}
	if err := validateNicknameEditActor(stored, actor); err != nil {
		t.Fatal(err)
	}
	stored.Status = models.UserStatusDisabled
	if status, _ := StatusFromError(validateNicknameEditActor(stored, actor)); status != 401 {
		t.Fatalf("disabled actor status=%d", status)
	}
	session := models.Session{ID: 1, AuditFields: models.AuditFields{Guid: 12}, UserID: actor.UserID, SID: actor.SessionSID, SessionVersion: actor.SessionVersion, ExpiresAt: 200}
	if err := validateNicknameEditSession(session, actor, 100); err != nil {
		t.Fatal(err)
	}
	session.SessionVersion++
	if status, _ := StatusFromError(validateNicknameEditSession(session, actor, 100)); status != 401 {
		t.Fatalf("stale session status=%d", status)
	}
	if !sameOptionalString(nil, nil) || sameOptionalString(nil, &valid) || !sameOptionalString(&valid, &valid) {
		t.Fatal("nickname equality differs")
	}
}

func stringPointerForEdit(value string) *string { return &value }
