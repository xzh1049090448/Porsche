package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	projectdb "github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// adminReadFixture connects only to an explicitly provided, already migrated
// disposable database. It does not create/drop databases or migrate schemas.
func adminReadFixture(t *testing.T) (*gorm.DB, *AuthRedis, *models.User, *models.User, AdminPermissionReadActor) {
	t.Helper()
	raw, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if raw == "" || redisURL == "" {
		t.Skip("requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL; no fixture provision or migration performed")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Fatal("explicit disposable test database required")
	}
	db, err := projectdb.Open(raw, "test")
	if err != nil {
		t.Fatal("open disposable database failed")
	}
	db = db.Session(&gorm.Session{Logger: logger.Discard})
	pool, err := db.DB()
	if err != nil {
		t.Fatal("root SQL pool missing")
	}
	t.Cleanup(func() { _ = pool.Close() })
	var actual string
	if err := db.Raw("SELECT DATABASE()").Scan(&actual).Error; err != nil || actual != strings.TrimPrefix(parsed.Path, "/") {
		t.Fatal("disposable database identity mismatch")
	}
	redisStore, err := NewAuthRedisFromURL(context.Background(), redisURL, testSessionSettings().AuthHMACKey)
	if err != nil {
		t.Fatal("open disposable Redis failed")
	}
	t.Cleanup(func() { _ = redisStore.Close() })
	root := createAuthSessionTestUser(t, db)
	target := createAuthSessionTestUser(t, db)
	root.Role = models.UserRoleRoot
	target.Role = models.UserRoleAdmin
	for _, user := range []*models.User{root, target} {
		if err := db.Model(user).Update("role", user.Role).Error; err != nil {
			t.Fatal("prepare fixture role failed")
		}
	}
	issued, err := NewSessionService(db, redisStore, testSessionSettings()).Create(context.Background(), root, SessionCreateInput{LoginMethod: models.LoginMethodPassword})
	if err != nil {
		t.Fatal("prepare actor session failed")
	}
	actor := AdminPermissionReadActor{UserID: root.ID, AuthVersion: root.AuthVersion, SessionSID: issued.Session.SID, SessionVersion: issued.Session.SessionVersion}
	return db, redisStore, root, target, actor
}

func TestAdminPermissionDBReadPolicyAndRoleMatrix(t *testing.T) {
	db, redisStore, root, target, actor := adminReadFixture(t)
	s := NewAdminPermissionReadService(db, redisStore)
	ctx := context.Background()
	if got, err := s.Catalog(ctx, actor); err != nil || got == nil || len(got.Capabilities) != 24 {
		t.Fatalf("root catalog failed: %v", err)
	}
	got, err := s.Detail(ctx, actor, target.Guid)
	if err != nil || got == nil || got.PermissionsVersion != "0" {
		t.Fatalf("baseline detail failed: %v", err)
	}
	head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: target.ID, PolicyVersion: 9, CatalogVersion: 1, RuleCount: 0}
	if err := db.Create(&head).Error; err != nil {
		t.Fatal("prepare empty policy failed")
	}
	got, err = s.Detail(ctx, actor, target.Guid)
	if err != nil || got == nil || got.PermissionsVersion != "9" {
		t.Fatal("empty head version lost")
	}
	if err := db.Model(&head).Update("rule_count", 2).Error; err != nil {
		t.Fatal("prepare head failed")
	}
	for _, pair := range [][2]int{{1, 3}, {6, 2}} {
		row := models.PermissionOverride{AuditFields: testAuditFields(), UserID: target.ID, PolicyVersion: 9, Capability: pair[0], Effect: pair[1]}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal("prepare override failed")
		}
	}
	if err := db.Model(target).Update("status", models.UserStatusDisabled).Error; err != nil {
		t.Fatal("disable target failed")
	}
	got, err = s.Detail(ctx, actor, target.Guid)
	if err != nil || got == nil || got.Status != "disabled" || got.Capabilities[0].PolicyEffective || !got.Capabilities[5].PolicyEffective {
		t.Fatalf("disabled policy failed: %v", err)
	}
	for _, capability := range got.Capabilities {
		if capability.Effective {
			t.Fatal("disabled effective capability leaked")
		}
	}
	for _, guid := range []int64{root.Guid, 9223372036854775807} {
		got, err := s.Detail(ctx, actor, guid)
		if got != nil || !errors.Is(err, ErrAdminPermissionHidden) {
			t.Fatal("hidden target differs")
		}
	}
	for _, role := range []models.UserRole{models.UserRoleAdmin, models.UserRoleUser} {
		if err := db.Model(root).Update("role", role).Error; err != nil {
			t.Fatal("prepare actor role failed")
		}
		catalog, err := s.Catalog(ctx, actor)
		if role == models.UserRoleAdmin {
			if err != nil || catalog == nil {
				t.Fatal("active admin catalog denied")
			}
		} else if catalog != nil || !errors.Is(err, ErrAdminPermissionDenied) {
			t.Fatal("user catalog permitted")
		}
		if got, err := s.Detail(ctx, actor, target.Guid); got != nil || !errors.Is(err, ErrAdminPermissionDenied) {
			t.Fatal("non-root detail permitted")
		}
	}
}

func TestAdminPermissionDBCorruptPolicy(t *testing.T) {
	for _, name := range []string{"orphan", "head_version", "catalog", "count", "row_version", "capability", "effect", "ungrantable", "tombstone"} {
		t.Run(name, func(t *testing.T) {
			db, redisStore, _, target, actor := adminReadFixture(t)
			head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: target.ID, PolicyVersion: 1, CatalogVersion: 1, RuleCount: 1}
			row := models.PermissionOverride{AuditFields: testAuditFields(), UserID: target.ID, PolicyVersion: 1, Capability: 1, Effect: 2}
			switch name {
			case "head_version":
				head.PolicyVersion = 0
			case "catalog":
				head.CatalogVersion = 2
			case "count":
				head.RuleCount = 2
			case "row_version":
				row.PolicyVersion = 2
			case "capability":
				row.Capability = 999
			case "effect":
				row.Effect = 999
			case "ungrantable":
				row.Capability = 14
			case "tombstone":
				head.IsDeleted = 1
			}
			if name != "orphan" {
				if err := db.Create(&head).Error; err != nil {
					t.Fatal("prepare corrupt head failed")
				}
			}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal("prepare corrupt override failed")
			}
			got, err := NewAdminPermissionReadService(db, redisStore).Detail(context.Background(), actor, target.Guid)
			if got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
				t.Fatalf("corrupt policy accepted: %v", err)
			}
		})
	}
}

func TestAdminPermissionDBFreshActorSessionAndRedis(t *testing.T) {
	for _, name := range []string{"actor_stale", "actor_disabled", "actor_deleted", "actor_missing", "actor_role_corrupt", "actor_status_corrupt", "actor_version_corrupt", "session_stale", "session_revoked", "session_deleted", "session_expired", "session_missing", "session_foreign", "session_version_corrupt", "redis_revoked", "redis_error", "target_role_corrupt", "target_status_corrupt", "target_version_corrupt", "role_denied_and_redis_revoked", "hidden_target_and_redis_revoked"} {
		t.Run(name, func(t *testing.T) {
			db, redisStore, root, target, actor := adminReadFixture(t)
			want := ErrAdminPermissionUnauthenticated
			var err error
			switch name {
			case "actor_stale":
				err = db.Model(root).Update("auth_version", root.AuthVersion+1).Error
			case "actor_disabled":
				err = db.Model(root).Update("status", models.UserStatusDisabled).Error
			case "actor_deleted":
				err = db.Model(root).Update("is_deleted", 1).Error
			case "actor_missing":
				actor.UserID = 9223372036854775807
			case "actor_role_corrupt":
				err = db.Model(root).Update("role", 999).Error
				want = ErrAdminPermissionUnavailable
			case "actor_status_corrupt":
				err = db.Model(root).Update("status", 999).Error
				want = ErrAdminPermissionUnavailable
			case "actor_version_corrupt":
				err = db.Model(root).Update("auth_version", 0).Error
				want = ErrAdminPermissionUnavailable
			case "session_stale":
				err = db.Model(&models.Session{}).Where("sid = ?", actor.SessionSID).Update("session_version", actor.SessionVersion+1).Error
			case "session_revoked":
				err = db.Model(&models.Session{}).Where("sid = ?", actor.SessionSID).Update("revoked_at", time.Now().UnixMilli()).Error
			case "session_deleted":
				err = db.Model(&models.Session{}).Where("sid = ?", actor.SessionSID).Update("is_deleted", 1).Error
			case "session_expired":
				err = db.Model(&models.Session{}).Where("sid = ?", actor.SessionSID).Update("expires_at", time.Now().UnixMilli()).Error
			case "session_missing":
				actor.SessionSID = "00000000-0000-0000-0000-000000000000"
			case "session_foreign":
				err = db.Model(&models.Session{}).Where("sid = ?", actor.SessionSID).Update("user_id", target.ID).Error
			case "session_version_corrupt":
				err = db.Model(&models.Session{}).Where("sid = ?", actor.SessionSID).Update("session_version", 0).Error
				want = ErrAdminPermissionUnavailable
			case "redis_revoked":
				err = redisStore.MarkSessionRevoked(context.Background(), actor.SessionSID, time.Minute)
			case "redis_error":
				err = redisStore.Close()
				want = ErrAdminPermissionUnavailable
			case "role_denied_and_redis_revoked":
				err = db.Model(root).Update("role", models.UserRoleUser).Error
				if err == nil {
					err = redisStore.MarkSessionRevoked(context.Background(), actor.SessionSID, time.Minute)
				}
			case "hidden_target_and_redis_revoked":
				target = root
				err = redisStore.MarkSessionRevoked(context.Background(), actor.SessionSID, time.Minute)
			case "target_role_corrupt":
				err = db.Model(target).Update("role", 999).Error
				want = ErrAdminPermissionUnavailable
			case "target_status_corrupt":
				err = db.Model(target).Update("status", 999).Error
				want = ErrAdminPermissionUnavailable
			case "target_version_corrupt":
				err = db.Model(target).Update("auth_version", 0).Error
				want = ErrAdminPermissionUnavailable
			}
			if err != nil {
				t.Fatal("prepare fresh authorization failure failed")
			}
			got, err := NewAdminPermissionReadService(db, redisStore).Detail(context.Background(), actor, target.Guid)
			if got != nil || !errors.Is(err, want) {
				t.Fatalf("fresh failure classification=%v want=%v", err, want)
			}
		})
	}
}

func TestAdminPermissionDBRejectsExternalTransactionAndCommitFailure(t *testing.T) {
	db, redisStore, _, target, actor := adminReadFixture(t)
	outer := db.Begin()
	if outer.Error != nil {
		t.Fatal("begin outer failed")
	}
	defer outer.Rollback()
	if got, err := NewAdminPermissionReadService(outer, redisStore).Detail(context.Background(), actor, target.Guid); got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
		t.Fatal("external transaction accepted")
	}
	hook := fmt.Sprintf("admin_read_commit_%d", testSnowflake.Next())
	var hit atomic.Bool
	var rollbackErr error
	if err := db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_permission_overrides" && hit.CompareAndSwap(false, true) {
			rollbackErr = tx.Rollback().Error
		}
	}); err != nil {
		t.Fatal("register commit failure injection failed")
	}
	defer db.Callback().Query().Remove(hook)
	got, err := NewAdminPermissionReadService(db, redisStore).Detail(context.Background(), actor, target.Guid)
	if !hit.Load() || rollbackErr != nil {
		t.Fatal("commit injection did not roll back live transaction")
	}
	if got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) {
		t.Fatal("failed commit exposed DTO")
	}
}

func TestAdminPermissionDBLockOrderAndFreshExpiry(t *testing.T) {
	db, redisStore, root, target, actor := adminReadFixture(t)
	var order []string
	hook := fmt.Sprintf("admin_read_order_%d", testSnowflake.Next())
	if err := db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" || tx.Statement.Table == "user_sessions" {
			lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
			if !ok || lock.Strength != "SHARE" {
				tx.AddError(errors.New("missing SHARE lock"))
				return
			}
			order = append(order, tx.Statement.Table)
		}
	}); err != nil {
		t.Fatal("register lock observer failed")
	}
	got, err := NewAdminPermissionReadService(db, redisStore).Detail(context.Background(), actor, target.Guid)
	db.Callback().Query().Remove(hook)
	if err != nil || got == nil || strings.Join(order, ",") != "users,users,user_sessions" {
		t.Fatalf("read lock order failed: %v %v", order, err)
	}
	// A separate root pool owns the session writer; the reader must calculate
	// expiry after it acquires the waited-for session lock.
	second := adminReadSecondPool(t)
	writer := second.Begin()
	if writer.Error != nil {
		t.Fatal("begin session writer failed")
	}
	defer writer.Rollback()
	var session models.Session
	if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).Where("sid = ?", actor.SessionSID).First(&session).Error; err != nil {
		t.Fatal("lock actor session failed")
	}
	expiry := time.Now().Add(300 * time.Millisecond).UnixMilli()
	if err := writer.Model(&session).Update("expires_at", expiry).Error; err != nil {
		t.Fatal("prepare session expiry failed")
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	readerHook := fmt.Sprintf("admin_read_wait_%d", root.ID)
	if err := db.Callback().Query().Before("gorm:query").Register(readerHook, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_sessions" {
			close(started)
		}
	}); err != nil {
		t.Fatal("register session barrier failed")
	}
	defer db.Callback().Query().Remove(readerHook)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		got, err := NewAdminPermissionReadService(db, redisStore).Detail(ctx, actor, target.Guid)
		if got != nil {
			err = errors.New("expired session exposed DTO")
		}
		result <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("reader did not reach session lock")
	}
	timer := time.NewTimer(time.Until(time.UnixMilli(expiry)) + 50*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if err := writer.Commit().Error; err != nil {
		t.Fatal("commit expiry failed")
	}
	if err := <-result; !errors.Is(err, ErrAdminPermissionUnauthenticated) {
		t.Fatalf("expiry checked before lock acquisition: %v", err)
	}
}

func adminReadSecondPool(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := projectdb.Open(os.Getenv("TEST_DATABASE_URL"), "test")
	if err != nil {
		t.Fatal("open second isolated pool failed")
	}
	db = db.Session(&gorm.Session{Logger: logger.Discard})
	pool, err := db.DB()
	if err != nil {
		t.Fatal("second root SQL pool missing")
	}
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func TestAdminPermissionDBUserWriterSerializesRead(t *testing.T) {
	db, redisStore, _, target, actor := adminReadFixture(t)
	head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: target.ID, PolicyVersion: 1, CatalogVersion: 1, RuleCount: 1}
	rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: target.ID, PolicyVersion: 1, Capability: 1, Effect: 2}
	if err := db.Create(&head).Error; err != nil {
		t.Fatal("prepare policy head failed")
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal("prepare policy rule failed")
	}
	second := adminReadSecondPool(t)
	firstSQL, _ := db.DB()
	secondSQL, _ := second.DB()
	if firstSQL == secondSQL {
		t.Fatal("independent pool required")
	}
	writer := second.Begin(&sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if writer.Error != nil {
		t.Fatal("begin writer failed")
	}
	defer writer.Rollback()
	var locked models.User
	if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", target.ID).First(&locked).Error; err != nil {
		t.Fatal("target writer lock failed")
	}
	if err := writer.Model(target).Update("status", models.UserStatusDisabled).Error; err != nil {
		t.Fatal("prepare target update failed")
	}
	if err := writer.Model(&head).Update("policy_version", 2).Error; err != nil {
		t.Fatal("update policy head failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	got, err := NewAdminPermissionReadService(db, redisStore).Detail(ctx, actor, target.Guid)
	if got != nil || !errors.Is(err, ErrAdminPermissionUnavailable) || time.Since(start) < 200*time.Millisecond {
		t.Fatal("reader escaped target writer lock")
	}
	if err := writer.Model(&rule).Updates(map[string]any{"policy_version": 2, "effect": 3}).Error; err != nil {
		t.Fatal("update policy rule failed")
	}
	if err := writer.Commit().Error; err != nil {
		t.Fatal("commit writer failed")
	}
	got, err = NewAdminPermissionReadService(db, redisStore).Detail(context.Background(), actor, target.Guid)
	if err != nil || got == nil || got.Status != "disabled" || got.PermissionsVersion != "2" || got.Capabilities[0].Override != "deny" || got.Capabilities[0].PolicyEffective {
		t.Fatal("reader did not observe committed target")
	}
	for _, c := range got.Capabilities {
		if c.Effective {
			t.Fatal("disabled target authorized")
		}
	}
}
