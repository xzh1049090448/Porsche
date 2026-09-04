package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestAdminUsersReadDBListAndDetail(t *testing.T) {
	db, r, root, target, actor := adminReadFixture(t)
	s := NewAdminUsersReadService(db, r)
	ctx := context.Background()
	// Keep the literal LIKE metacharacters while fitting the production
	// users.username VARCHAR(20) limit enforced by the real MySQL fixture.
	username := fmt.Sprintf("b%%%010x_!", uint64(target.Guid)&0xffffffffff)
	last := int64(1700000000123)
	if err := db.Model(target).Updates(map[string]interface{}{"username": username, "last_login_at": last}).Error; err != nil {
		t.Fatalf("prepare searchable user: %v", err)
	}
	for _, search := range []string{strconv.FormatInt(target.Guid, 10), username} {
		q := AdminUsersReadQuery{Page: 1, PageSize: 20, Q: search, Sort: "guid", Order: "desc"}
		page, err := s.List(ctx, actor, q)
		if err != nil || page == nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].GUID != strconv.FormatInt(target.Guid, 10) || page.Items[0].LastLoginAt == nil {
			t.Fatalf("exact/literal page failed %v", err)
		}
		q.Page = 2
		page, err = s.List(ctx, actor, q)
		if err != nil || page.Total != 1 || page.Items == nil || len(page.Items) != 0 {
			t.Fatal("empty-page total lost", err)
		}
	}
	filtered, _ := ParseAdminUsersReadQuery("role=admin&status=active&q=" + strconv.FormatInt(target.Guid, 10))
	page, err := s.List(ctx, actor, filtered)
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatal("active role/status total-items differ", err)
	}
	if err := db.Model(target).Update("status", models.UserStatusDisabled).Error; err != nil {
		t.Fatal("prepare disabled filter")
	}
	page, err = s.List(ctx, actor, filtered)
	if err != nil || page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
		t.Fatal("active empty result differs", err)
	}
	filtered.Status = "disabled"
	page, err = s.List(ctx, actor, filtered)
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatal("disabled role/status total-items differ", err)
	}
	if err := db.Model(target).Update("status", models.UserStatusActive).Error; err != nil {
		t.Fatal("restore active filter")
	}
	for _, sort := range []string{"guid", "username", "created_at", "last_login_at"} {
		for _, order := range []string{"asc", "desc"} {
			q, _ := ParseAdminUsersReadQuery("sort=" + sort + "&order=" + order + "&q=" + strconv.FormatInt(target.Guid, 10))
			if _, err := s.List(ctx, actor, q); err != nil {
				t.Fatal("CTE ordering failed", err)
			}
		}
	}
	detail, err := s.Detail(ctx, actor, target.Guid)
	if err != nil || detail.Role != "admin" {
		t.Fatal("root cannot read admin", err)
	}
	for _, guid := range []int64{root.Guid, 9223372036854775807} {
		if got, err := s.Detail(ctx, actor, guid); got != nil || !errors.Is(err, ErrAdminPermissionHidden) {
			t.Fatal("hidden target differs")
		}
	}
	if err := db.Model(root).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal("role fixture")
	}
	if got, err := s.Detail(ctx, actor, target.Guid); got != nil || !errors.Is(err, ErrAdminPermissionHidden) {
		t.Fatal("peer admin visible")
	}
	if err := db.Model(target).Update("role", models.UserRoleUser).Error; err != nil {
		t.Fatal("target role")
	}
	if got, err := s.Detail(ctx, actor, target.Guid); err != nil || got.Role != "user" {
		t.Fatal("admin user read", err)
	}
	if got, err := s.LegacyBehavior(ctx, actor, target.Guid); err != nil || got["model_preferences"] == nil {
		t.Fatal("legacy behavior", err)
	}
	if got, err := s.LegacyList(ctx, actor, 0, 0, ""); err != nil || got == nil || len(got) != 0 {
		t.Fatal("legacy zero limit", err)
	}
}

func TestAdminUsersReadDBDeletedAndPolicy(t *testing.T) {
	for _, mode := range []string{"baseline", "allow_deleted", "deny_read", "corrupt", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			db, r, root, target, actor := adminReadFixture(t)
			ctx := context.Background()
			s := NewAdminUsersReadService(db, r)
			if err := db.Model(root).Update("role", models.UserRoleAdmin).Error; err != nil {
				t.Fatal("actor role")
			}
			if err := db.Model(target).Updates(map[string]interface{}{"role": models.UserRoleUser, "is_deleted": 1}).Error; err != nil {
				t.Fatal("tombstone")
			}
			if mode == "ordinary" {
				if err := db.Model(root).Update("role", models.UserRoleUser).Error; err != nil {
					t.Fatal("ordinary")
				}
			}
			if mode == "allow_deleted" || mode == "deny_read" || mode == "corrupt" {
				head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 7, CatalogVersion: 1, RuleCount: 1}
				row := models.PermissionOverride{AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 7, Capability: 13, Effect: 2}
				if mode == "deny_read" {
					row.Capability = 1
					row.Effect = 3
				}
				if mode == "corrupt" {
					head.RuleCount = 2
				}
				if err := db.Create(&head).Error; err != nil {
					t.Fatal("head")
				}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal("rule")
				}
			}
			q, _ := ParseAdminUsersReadQuery("status=deleted&q=" + strconv.FormatInt(target.Guid, 10))
			page, err := s.List(ctx, actor, q)
			switch mode {
			case "allow_deleted":
				if err != nil || page.Total != 1 || page.Items[0].Status != "deleted" {
					t.Fatal("deleted allow", err)
				}
			case "corrupt":
				if page != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
					t.Fatal("corrupt policy not unavailable")
				}
			default:
				if page != nil || !errors.Is(err, ErrAdminPermissionDenied) {
					t.Fatal("deleted list not denied", err)
				}
			}
			got, err := s.Detail(ctx, actor, target.Guid)
			switch mode {
			case "allow_deleted":
				if err != nil || got.Status != "deleted" {
					t.Fatal("deleted detail", err)
				}
			case "corrupt":
				if got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
					t.Fatal("corrupt detail")
				}
			case "deny_read", "ordinary":
				if got != nil || !errors.Is(err, ErrAdminPermissionDenied) {
					t.Fatal("read denied", err)
				}
			default:
				if got != nil || !errors.Is(err, ErrAdminPermissionHidden) {
					t.Fatal("deleted hidden", err)
				}
			}
			if mode == "allow_deleted" || mode == "baseline" {
				if got, err := s.LegacyDetail(ctx, actor, target.Guid); got != nil || !errors.Is(err, ErrAdminPermissionHidden) {
					t.Fatal("legacy deleted exposed")
				}
			}
		})
	}
}

func TestAuthProjectionDBFreshnessAndOmission(t *testing.T) {
	for _, mode := range []string{"baseline", "empty_head", "orphan", "illegal_root_rule", "actor_av", "actor_disabled", "actor_deleted", "session_sv", "session_revoked", "session_expired", "session_deleted", "redis_error", "unknown_status"} {
		t.Run(mode, func(t *testing.T) {
			db, r, root, _, actor := adminReadFixture(t)
			s := NewAdminUsersReadService(db, r)
			ctx := context.Background()
			switch mode {
			case "empty_head", "illegal_root_rule":
				head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 9, CatalogVersion: 1}
				if mode == "illegal_root_rule" {
					head.RuleCount = 1
				}
				if err := db.Create(&head).Error; err != nil {
					t.Fatal("head")
				}
				if mode == "illegal_root_rule" {
					row := models.PermissionOverride{AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 9, Capability: 1, Effect: 2}
					if err := db.Create(&row).Error; err != nil {
						t.Fatal("rule")
					}
				}
			case "orphan":
				row := models.PermissionOverride{AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 1, Capability: 1, Effect: 2}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal("orphan")
				}
			case "actor_av":
				actor.AuthVersion++
			case "actor_disabled":
				if err := db.Model(root).Update("status", models.UserStatusDisabled).Error; err != nil {
					t.Fatal("prepare mutation failed")
				}
			case "actor_deleted":
				if err := db.Model(root).Update("is_deleted", 1).Error; err != nil {
					t.Fatal("prepare mutation failed")
				}
			case "session_sv":
				actor.SessionVersion++
			case "session_revoked":
				if err := db.Model(&models.Session{}).Where("sid = ?", actor.SessionSID).Update("revoked_at", time.Now().UnixMilli()).Error; err != nil {
					t.Fatal("prepare mutation failed")
				}
			case "session_expired":
				if err := db.Model(&models.Session{}).Where("sid = ?", actor.SessionSID).Update("expires_at", time.Now().Add(-time.Minute).UnixMilli()).Error; err != nil {
					t.Fatal("prepare mutation failed")
				}
			case "session_deleted":
				if err := db.Model(&models.Session{}).Where("sid = ?", actor.SessionSID).Update("is_deleted", 1).Error; err != nil {
					t.Fatal("prepare mutation failed")
				}
			case "redis_error":
				r.Close()
			case "unknown_status":
				if err := db.Model(root).Update("status", 99).Error; err != nil {
					t.Fatal("prepare mutation failed")
				}
			}
			fresh, err := s.AuthProjection(ctx, actor)
			switch mode {
			case "baseline", "empty_head":
				if err != nil || fresh == nil || fresh.Permissions == nil || len(fresh.Permissions.AdminPermissions) == 0 {
					t.Fatal("valid projection", err)
				}
				want := "0"
				if mode == "empty_head" {
					want = "9"
				}
				if fresh.Permissions.PermissionsVersion != want {
					t.Fatal("version")
				}
			case "orphan", "illegal_root_rule":
				if err != nil || fresh == nil || fresh.User.ID != root.ID || fresh.Permissions != nil {
					t.Fatal("identity-valid policy omission", err)
				}
			case "redis_error", "unknown_status":
				if fresh != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
					t.Fatal("unsafe identity downgraded", err)
				}
			default:
				if fresh != nil || !errors.Is(err, ErrAdminPermissionUnauthenticated) {
					t.Fatal("stale identity downgraded", err)
				}
			}
		})
	}
}

func TestAdminUsersReadDBCommitFailureAndOuterTransaction(t *testing.T) {
	for _, projection := range []bool{true, false} {
		t.Run(fmt.Sprint(projection), func(t *testing.T) {
			db, r, _, target, actor := adminReadFixture(t)
			ctx := context.Background()
			outer := db.Begin()
			defer outer.Rollback()
			if got, err := NewAdminUsersReadService(outer, r).AuthProjection(ctx, actor); got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
				t.Fatal("external transaction accepted")
			}
			hook := fmt.Sprintf("b1d_commit_%d", testSnowflake.Next())
			var hit atomic.Bool
			if err := db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
				if tx.Statement.Table == "user_sessions" && hit.CompareAndSwap(false, true) {
					if err := tx.Rollback().Error; err != nil {
						tx.AddError(err)
					}
				}
			}); err != nil {
				t.Fatal("hook")
			}
			defer db.Callback().Query().Remove(hook)
			s := NewAdminUsersReadService(db, r)
			if projection {
				got, err := s.AuthProjection(ctx, actor)
				if got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
					t.Fatal("projection returned after commit failure")
				}
			} else {
				got, err := s.Detail(ctx, actor, target.Guid)
				if got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
					t.Fatal("detail returned after commit failure")
				}
			}
			if !hit.Load() {
				t.Fatal("live transaction rollback injection did not execute")
			}
		})
	}
}

func TestAdminUsersReadDBActorPolicyWriterSerializes(t *testing.T) {
	db, r, root, target, actor := adminReadFixture(t)
	ctx := context.Background()
	if err := db.Model(root).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal("actor role")
	}
	if err := db.Model(target).Update("role", models.UserRoleUser).Error; err != nil {
		t.Fatal("target role")
	}
	writer := db.Begin(&sql.TxOptions{Isolation: sql.LevelReadCommitted})
	defer writer.Rollback()
	var locked models.User
	if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", root.ID).First(&locked).Error; err != nil {
		t.Fatal("writer lock")
	}
	head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 2, CatalogVersion: 1, RuleCount: 1}
	if err := writer.Create(&head).Error; err != nil {
		t.Fatal("head")
	}
	done := make(chan error, 1)
	go func() { _, err := NewAdminUsersReadService(db, r).Detail(ctx, actor, target.Guid); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("reader bypassed actor lock %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	row := models.PermissionOverride{AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 2, Capability: 1, Effect: 3}
	if err := writer.Create(&row).Error; err != nil {
		t.Fatal("rule")
	}
	if err := writer.Commit().Error; err != nil {
		t.Fatal("writer commit")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrAdminPermissionDenied) {
			t.Fatal("reader saw half policy or old authority", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not resume")
	}
}

func TestAdminUsersReadDBStableNullOrderingAcrossPages(t *testing.T) {
	db, r, _, _, actor := adminReadFixture(t)
	s := NewAdminUsersReadService(db, r)
	ctx := context.Background()
	tag := fmt.Sprintf("b1d_tie_%d", testSnowflake.Next())
	last := int64(1700000000123)
	var users []*models.User
	for i := 0; i < 21; i++ {
		u := createAuthSessionTestUser(t, db)
		u.Nickname = &tag
		if i >= 10 {
			u.LastLoginAt = &last
		}
		if err := db.Model(u).Updates(map[string]interface{}{"nickname": tag, "last_login_at": u.LastLoginAt}).Error; err != nil {
			t.Fatal("prepare ties")
		}
		users = append(users, u)
	}
	for _, order := range []string{"asc", "desc"} {
		expected := append([]*models.User(nil), users...)
		sort.Slice(expected, func(i, j int) bool {
			a, b := expected[i], expected[j]
			if (a.LastLoginAt == nil) != (b.LastLoginAt == nil) {
				if order == "asc" {
					return a.LastLoginAt == nil
				}
				return a.LastLoginAt != nil
			}
			if order == "asc" {
				return a.Guid < b.Guid
			}
			return a.Guid > b.Guid
		})
		var actual []string
		for _, page := range []int64{1, 2} {
			q := AdminUsersReadQuery{Page: page, PageSize: 20, Q: tag, Sort: "last_login_at", Order: order}
			got, err := s.List(ctx, actor, q)
			if err != nil || got.Total != 21 {
				t.Fatal("cross-page total", err)
			}
			for _, item := range got.Items {
				actual = append(actual, item.GUID)
			}
		}
		if len(actual) != 21 {
			t.Fatal("cross-page omission/duplicate")
		}
		for i, u := range expected {
			if actual[i] != strconv.FormatInt(u.Guid, 10) {
				t.Fatalf("stable %s tie position %d differs", order, i)
			}
		}
	}
}

func TestAuthProjectionDBAdminOverridesAndRefreshProof(t *testing.T) {
	db, r, root, _, actor := adminReadFixture(t)
	s := NewAdminUsersReadService(db, r)
	ctx := context.Background()
	if err := db.Model(root).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal("role")
	}
	root.Role = models.UserRoleAdmin
	head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 3, CatalogVersion: 1, RuleCount: 2}
	rows := []models.PermissionOverride{{AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 3, Capability: 1, Effect: 3}, {AuditFields: testAuditFields(), UserID: root.ID, PolicyVersion: 3, Capability: 6, Effect: 2}}
	if err := db.Create(&head).Error; err != nil {
		t.Fatal("head")
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal("rules")
	}
	fresh, err := s.AuthProjection(ctx, actor)
	if err != nil || fresh.Permissions == nil || fresh.Permissions.PermissionsVersion != "3" {
		t.Fatal("admin projection", err)
	}
	foundReset := false
	for _, name := range fresh.Permissions.AdminPermissions {
		if name == "users.read" {
			t.Fatal("deny ignored")
		}
		if name == "users.reset_password" {
			foundReset = true
		}
	}
	if !foundReset {
		t.Fatal("allow missing")
	}
	actor.AuthVersion = 0
	if got, err := s.AuthProjection(ctx, actor); got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
		t.Fatal("generic AV0 accepted")
	}
	sessions := NewSessionService(db, r, testSessionSettings())
	created, err := sessions.Create(ctx, root, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
	if err != nil {
		t.Fatal("create session")
	}
	if got, err := s.RefreshProjection(ctx, created); got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
		t.Fatal("non-refresh proof accepted")
	}
	rotated, err := sessions.Refresh(ctx, created.RefreshToken)
	if err != nil {
		t.Fatal("refresh")
	}
	if got, err := s.RefreshProjection(ctx, rotated); err != nil || got.User.AuthVersion != root.AuthVersion {
		t.Fatal("internal refresh proof", err)
	}
	if err := r.MarkSessionRevoked(ctx, rotated.Session.SID, time.Minute); err != nil {
		t.Fatal("revoke")
	}
	if got, err := s.RefreshProjection(ctx, rotated); got != nil || !errors.Is(err, ErrAdminPermissionUnauthenticated) {
		t.Fatal("revoked issued proof")
	}
}
