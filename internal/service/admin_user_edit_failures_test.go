package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type actorLockSignalPool struct {
	db     *sql.DB
	issued chan struct{}
	once   sync.Once
}

func (p *actorLockSignalPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.db.PrepareContext(ctx, query)
}
func (p *actorLockSignalPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}
func (p *actorLockSignalPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}
func (p *actorLockSignalPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.db.QueryRowContext(ctx, query, args...)
}
func (p *actorLockSignalPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &actorLockSignalTx{Tx: tx, issued: p.issued, once: &p.once}, nil
}

type actorLockSignalTx struct {
	*sql.Tx
	issued chan struct{}
	once   *sync.Once
}

func (tx *actorLockSignalTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "FROM `users`") && strings.Contains(query, "id = ?") && strings.Contains(query, "FOR UPDATE") {
		tx.once.Do(func() { close(tx.issued) })
	}
	return tx.Tx.QueryContext(ctx, query, args...)
}

func installActorLockSignal(t *testing.T, db *gorm.DB) (*gorm.DB, <-chan struct{}) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	issued := make(chan struct{})
	wrapped := db.Session(&gorm.Session{NewDB: true})
	wrapped.Statement.ConnPool = &actorLockSignalPool{db: sqlDB, issued: issued}
	return wrapped, issued
}

func TestAdminUserNicknameEditActorLockSignalObservesDriverQuery(t *testing.T) {
	script, _, _ := actionIssueScriptFixture(t, 1_800_000_000_000)
	db := openActionIssueScriptDB(t, script, nil)
	wrapped, issued := installActorLockSignal(t, db)
	done := make(chan error, 1)
	go func() {
		done <- wrapped.Transaction(func(tx *gorm.DB) error {
			var actor models.User
			return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "role", "status", "is_deleted", "auth_version").Where("id = ?", script.actor.ID).First(&actor).Error
		})
	}()
	<-issued
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if script.begins != 1 || script.commits != 1 {
		t.Fatalf("transaction counts=%d/%d", script.begins, script.commits)
	}
}

func TestAdminUserNicknameEditRollsBackUpdateAuditAndCommitFailures(t *testing.T) {
	for _, name := range []string{"user_update", "audit_insert", "commit"} {
		t.Run(name, func(t *testing.T) {
			ctx, service, _, _, target, claims := adminUserNicknameEditFixture(t, models.UserRoleRoot, models.UserRoleUser)
			var original *string
			if target.Nickname != nil {
				value := *target.Nickname
				original = &value
			}
			var commitHit atomic.Bool
			var constraint, table string
			switch name {
			case "user_update":
				constraint, table = fmt.Sprintf("chk_a05_user_%d", testSnowflake.Next()), "users"
				if err := service.db.Exec(fmt.Sprintf("ALTER TABLE users ADD CONSTRAINT %s CHECK (nickname <> 'a05-forced')", constraint)).Error; err != nil {
					t.Fatal(err)
				}
			case "audit_insert":
				constraint, table = fmt.Sprintf("chk_a05_audit_%d", testSnowflake.Next()), "auth_audit_events"
				if err := service.db.Exec(fmt.Sprintf("ALTER TABLE auth_audit_events ADD CONSTRAINT %s CHECK (event_type <> %d)", constraint, models.AuthAuditEventManagedUserUpdated)).Error; err != nil {
					t.Fatal(err)
				}
			case "commit":
				sqlDB, err := service.db.DB()
				if err != nil {
					t.Fatal(err)
				}
				wrapped := service.db.Session(&gorm.Session{NewDB: true})
				wrapped.Statement.ConnPool = &commitFailurePool{db: sqlDB, hit: &commitHit}
				service.db = wrapped
			}
			if constraint != "" {
				t.Cleanup(func() { _ = service.db.Exec(fmt.Sprintf("ALTER TABLE %s DROP CHECK %s", table, constraint)).Error })
			}
			nickname := "a05-forced"
			got, err := service.Edit(ctx, claims, target.Guid, AdminUserNicknameEditInput{Nickname: &nickname, ExpectedAuthVersion: target.AuthVersion})
			if got != nil || err == nil {
				t.Fatalf("failure accepted: %#v %v", got, err)
			}
			if status, message := StatusFromError(err); status != 503 || strings.Contains(message, "a05-forced") {
				t.Fatalf("status/message=%d/%q", status, message)
			}
			var stored models.User
			if err := service.db.First(&stored, target.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !sameOptionalString(stored.Nickname, original) {
				t.Fatalf("partial nickname commit: %#v", stored.Nickname)
			}
			assertChangePasswordAuditCount(t, service.db, target.ID, models.AuthAuditEventManagedUserUpdated, 0)
			if name == "commit" && !commitHit.Load() {
				t.Fatal("commit hook not reached")
			}
		})
	}
}

func TestAdminUserNicknameEditConcurrentActorRaceFailsStaleOperation(t *testing.T) {
	for _, race := range []string{"auth_version", "permission"} {
		t.Run(race, func(t *testing.T) {
			ctx, service, _, actor, target, claims := adminUserNicknameEditFixture(t, models.UserRoleAdmin, models.UserRoleUser)
			tx := service.db.WithContext(ctx).Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			var locked models.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, actor.ID).Error; err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			wrappedDB, actorSelectIssued := installActorLockSignal(t, service.db)
			service.db = wrappedDB
			result := make(chan error, 1)
			go func() {
				nickname := "race"
				_, err := service.Edit(ctx, claims, target.Guid, AdminUserNicknameEditInput{Nickname: &nickname, ExpectedAuthVersion: target.AuthVersion})
				result <- err
			}()
			// The signal is emitted by the transaction's QueryContext immediately
			// before the actor SELECT FOR UPDATE delegates to MySQL. Because tx
			// already owns that row lock, the edit is now deterministically blocked.
			<-actorSelectIssued
			if race == "auth_version" {
				if err := tx.Model(actor).Update("auth_version", actor.AuthVersion+1).Error; err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
			} else {
				capability, _ := models.PermissionCapabilityCode("users.edit")
				head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: actor.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
				rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: actor.ID, PolicyVersion: 1, Capability: capability, Effect: 3}
				if err := tx.Create(&head).Error; err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Create(&rule).Error; err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
			}
			if err := tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			editErr := <-result
			want := 401
			if race == "permission" {
				want = 403
			}
			if status, _ := StatusFromError(editErr); status != want {
				t.Fatalf("race status=%d err=%v want=%d", status, editErr, want)
			}
			var storedTarget models.User
			if err := service.db.First(&storedTarget, target.ID).Error; err != nil {
				t.Fatal(err)
			}
			if storedTarget.Nickname != nil && *storedTarget.Nickname == "race" {
				t.Fatal("stale edit committed")
			}
			assertChangePasswordAuditCount(t, service.db, target.ID, models.AuthAuditEventManagedUserUpdated, 0)
		})
	}
}

func TestAdminUserNicknameEditAuditHasNoNicknamePayload(t *testing.T) {
	ctx, service, _, _, target, claims := adminUserNicknameEditFixture(t, models.UserRoleRoot, models.UserRoleUser)
	nickname := "private-display-value"
	if _, err := service.Edit(ctx, claims, target.Guid, AdminUserNicknameEditInput{Nickname: &nickname, ExpectedAuthVersion: target.AuthVersion}); err != nil {
		t.Fatal(err)
	}
	var events []models.AuthAuditEvent
	if err := service.db.Where("user_id = ? AND event_type = ?", target.ID, models.AuthAuditEventManagedUserUpdated).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].IP != nil || events[0].UserAgent != nil || events[0].LoginMethod != nil {
		t.Fatalf("audit shape=%#v", events)
	}
}
