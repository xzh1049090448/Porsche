package service

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func countAdminUserStatusAudit(t *testing.T, db *gorm.DB, targetID int64, action string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&models.AuditLog{}).Where("user_id = ? AND action = ? AND is_deleted = 0", targetID, action).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func assertAdminUserStatusRollback(t *testing.T, db *gorm.DB, target *models.User, err error) {
	t.Helper()
	if status, message := StatusFromError(err); status != 503 || strings.Contains(message, "a06-forced") {
		t.Fatalf("status/message=%d/%q err=%v", status, message, err)
	}
	var stored models.User
	if queryErr := db.First(&stored, target.ID).Error; queryErr != nil {
		t.Fatal(queryErr)
	}
	if stored.Status != target.Status || stored.AuthVersion != target.AuthVersion {
		t.Fatalf("partial user commit: status=%s auth_version=%d", stored.Status.String(), stored.AuthVersion)
	}
	assertChangePasswordAuditCount(t, db, target.ID, models.AuthAuditEventUserDisabled, 0)
	if count := countAdminUserStatusAudit(t, db, target.ID, "users.disable"); count != 0 {
		t.Fatalf("partial management audit count=%d", count)
	}
}

func TestAdminUserStatusRollsBackUpdateAuditAndCommitFailures(t *testing.T) {
	for _, name := range []string{"user_update", "auth_audit", "management_audit", "commit"} {
		t.Run(name, func(t *testing.T) {
			ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
			baseDB := edit.db
			service := NewAdminUserStatusService(baseDB, redisStore)
			var constraint, table string
			var commitHit atomic.Bool
			switch name {
			case "user_update":
				constraint, table = fmt.Sprintf("chk_a06_user_%d", testSnowflake.Next()), "users"
				if err := baseDB.Exec(fmt.Sprintf("ALTER TABLE users ADD CONSTRAINT %s CHECK (status <> %d)", constraint, models.UserStatusDisabled)).Error; err != nil {
					t.Fatal(err)
				}
			case "auth_audit":
				constraint, table = fmt.Sprintf("chk_a06_auth_audit_%d", testSnowflake.Next()), "auth_audit_events"
				if err := baseDB.Exec(fmt.Sprintf("ALTER TABLE auth_audit_events ADD CONSTRAINT %s CHECK (event_type <> %d)", constraint, models.AuthAuditEventUserDisabled)).Error; err != nil {
					t.Fatal(err)
				}
			case "management_audit":
				constraint, table = fmt.Sprintf("chk_a06_management_audit_%d", testSnowflake.Next()), "audit_logs"
				if err := baseDB.Exec(fmt.Sprintf("ALTER TABLE audit_logs ADD CONSTRAINT %s CHECK (action <> 'users.disable')", constraint)).Error; err != nil {
					t.Fatal(err)
				}
			case "commit":
				sqlDB, err := baseDB.DB()
				if err != nil {
					t.Fatal(err)
				}
				wrapped := baseDB.Session(&gorm.Session{NewDB: true})
				wrapped.Statement.ConnPool = &commitFailurePool{db: sqlDB, hit: &commitHit}
				service.db = wrapped
			}
			if constraint != "" {
				t.Cleanup(func() { _ = baseDB.Exec(fmt.Sprintf("ALTER TABLE %s DROP CHECK %s", table, constraint)).Error })
			}
			reason := "a06 failure injection"
			got, err := service.Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusDisabled, Reason: &reason, ExpectedAuthVersion: target.AuthVersion})
			if got != nil || err == nil {
				t.Fatalf("failure accepted: result=%#v err=%v", got, err)
			}
			assertAdminUserStatusRollback(t, baseDB, target, err)
			if name == "commit" && !commitHit.Load() {
				t.Fatal("commit failure hook not reached")
			}
		})
	}
}

func TestAdminUserStatusRejectsActorDriftAndTargetVersionOverflow(t *testing.T) {
	t.Run("actor auth version drift", func(t *testing.T) {
		ctx, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
		if err := edit.db.Model(actorUser).Update("auth_version", actorUser.AuthVersion+1).Error; err != nil {
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
	})

	t.Run("actor session version drift", func(t *testing.T) {
		ctx, edit, redisStore, actorUser, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
		if err := edit.db.Model(&models.Session{}).Where("user_id = ? AND sid = ?", actorUser.ID, actor.SessionSID).Update("session_version", actor.SessionVersion+1).Error; err != nil {
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
	})

	t.Run("target auth version overflow", func(t *testing.T) {
		ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
		if err := edit.db.Model(target).Update("auth_version", math.MaxInt32).Error; err != nil {
			t.Fatal(err)
		}
		target.AuthVersion = math.MaxInt32
		reason := "review"
		got, err := NewAdminUserStatusService(edit.db, redisStore).Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusDisabled, Reason: &reason, ExpectedAuthVersion: math.MaxInt32})
		if got != nil || err == nil {
			t.Fatalf("overflow accepted: result=%#v err=%v", got, err)
		}
		assertAdminUserStatusRollback(t, edit.db, target, err)
	})
}

func TestAdminUserStatusConcurrentDisableCommitsExactlyOnce(t *testing.T) {
	ctx, edit, redisStore, _, target, actor := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			ready <- struct{}{}
			<-start
			reason := "concurrent review"
			result, err := NewAdminUserStatusService(edit.db, redisStore).Change(ctx, actor, target.Guid, AdminUserStatusInput{Status: models.UserStatusDisabled, Reason: &reason, ExpectedAuthVersion: target.AuthVersion})
			if err == nil && result == nil {
				err = errors.New("successful call returned nil result")
			}
			results <- err
		}()
	}
	<-ready
	<-ready
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		if status, _ := StatusFromError(err); status == 409 && AdminUserStatusConflictCode(err) == "auth_version_conflict" {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent error: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	var stored models.User
	if err := edit.db.First(&stored, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.UserStatusDisabled || stored.AuthVersion != target.AuthVersion+1 {
		t.Fatalf("stored status/version=%s/%d", stored.Status.String(), stored.AuthVersion)
	}
	assertChangePasswordAuditCount(t, edit.db, target.ID, models.AuthAuditEventUserDisabled, 1)
	if count := countAdminUserStatusAudit(t, edit.db, target.ID, "users.disable"); count != 1 {
		t.Fatalf("management audit count=%d", count)
	}
}
