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

	"github.com/porsche/ai-gateway-go/internal/config"
	projectdb "github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

// managedUserFailureFixture owns a child MySQL schema for destructive failure
// injection. Redis uses a per-session key, while the SQL fixture is dropped by
// openRootTestMySQL cleanup.
func managedUserFailureFixture(t *testing.T) (context.Context, *gorm.DB, *AuthRedis, *AuthService, *models.User, *models.User, *IssuedSession) {
	t.Helper()
	ctx := context.Background()
	db := openRootTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	redisStore := openTestAuthRedis(t)
	actor := createAuthSessionTestUser(t, db)
	if err := db.Model(&models.User{}).Where("id = ?", actor.ID).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ? AND is_deleted = 0", actor.ID).First(actor).Error; err != nil {
		t.Fatal(err)
	}
	target := createAuthSessionTestUser(t, db)
	sessions := NewSessionService(db, redisStore, testSessionSettings())
	issued, err := sessions.Create(ctx, target, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.72"})
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthService(&config.Settings{}, nil, db)
	auth.SetSessionService(sessions)
	return ctx, db, redisStore, auth, actor, target, issued
}

func assertManagedUserFailureRollback(t *testing.T, ctx context.Context, db *gorm.DB, redisStore *AuthRedis, target *models.User, issued *IssuedSession, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("UpdateManagedUser succeeded")
	}
	status, message := StatusFromError(err)
	if status != 503 {
		t.Fatalf("status=%d err=%v, want 503", status, err)
	}
	if strings.Contains(message, "managed-user-failure-marker") {
		t.Fatalf("availability response leaked injected error marker: %q", message)
	}
	stored := loadChangePasswordUser(t, db, target.ID)
	if stored.PlanType != target.PlanType || stored.AuthVersion != target.AuthVersion {
		t.Fatalf("failed update committed target state: %#v", stored)
	}
	assertChangePasswordSessionActive(t, db, issued.Session.ID)
	assertChangePasswordAuditCount(t, db, target.ID, models.AuthAuditEventManagedUserUpdated, 0)
	if revoked, redisErr := redisStore.IsSessionRevoked(ctx, issued.Session.SID); redisErr != nil || !revoked {
		t.Fatalf("deny-first Redis barrier = %t, %v; want true, nil", revoked, redisErr)
	}
}

// TestUpdateManagedUserRollsBackSQLAndAuditWriteFailures verifies both durable
// writes roll back after Redis has safely denied the already-issued session.
func TestUpdateManagedUserRollsBackSQLAndAuditWriteFailures(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		statement func(string) string
	}{
		{name: "user-update", statement: func(name string) string {
			return fmt.Sprintf("ALTER TABLE users ADD CONSTRAINT %s CHECK (plan_type <> %d)", name, models.PlanProfessional)
		}},
		{name: "event-insert", statement: func(name string) string {
			return fmt.Sprintf("ALTER TABLE auth_audit_events ADD CONSTRAINT %s CHECK (event_type <> %d)", name, models.AuthAuditEventManagedUserUpdated)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, db, redisStore, auth, actor, target, issued := managedUserFailureFixture(t)
			constraint := fmt.Sprintf("chk_managed_failure_%d", testSnowflake.Next())
			if err := db.Exec(testCase.statement(constraint)).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = db.Exec(fmt.Sprintf("ALTER TABLE %s DROP CHECK %s", map[string]string{"user-update": "users", "event-insert": "auth_audit_events"}[testCase.name], constraint)).Error
			})
			plan := models.PlanProfessional
			updated, err := auth.UpdateManagedUser(ctx, actor.ID, target.Guid, ManagedUserUpdateInput{PlanType: &plan})
			if updated != nil {
				t.Fatalf("unexpected result: %#v", updated)
			}
			assertManagedUserFailureRollback(t, ctx, db, redisStore, target, issued, err)
		})
	}
}

type commitFailurePool struct {
	db  *sql.DB
	hit *atomic.Bool
}

func (p *commitFailurePool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.db.PrepareContext(ctx, query)
}
func (p *commitFailurePool) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}
func (p *commitFailurePool) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}
func (p *commitFailurePool) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return p.db.QueryRowContext(ctx, query, args...)
}
func (p *commitFailurePool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &commitFailureTx{Tx: tx, hit: p.hit}, nil
}

type commitFailureTx struct {
	*sql.Tx
	hit *atomic.Bool
}

func (tx *commitFailureTx) Commit() error {
	tx.hit.Store(true)
	if err := tx.Tx.Rollback(); err != nil {
		return err
	}
	return errors.New("managed-user-failure-marker")
}

// TestUpdateManagedUserRollsBackWhenCommitFails forces a commit failure only
// after the transaction function has inserted event 10. The wrapper rolls back
// the real MySQL transaction before reporting its marker error.
func TestUpdateManagedUserRollsBackWhenCommitFails(t *testing.T) {
	ctx, db, redisStore, auth, actor, target, issued := managedUserFailureFixture(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	var commitHit atomic.Bool
	commitDB := db.Session(&gorm.Session{NewDB: true})
	commitDB.Statement.ConnPool = &commitFailurePool{db: sqlDB, hit: &commitHit}
	auth.db = commitDB
	plan := models.PlanProfessional
	updated, err := auth.UpdateManagedUser(ctx, actor.ID, target.Guid, ManagedUserUpdateInput{PlanType: &plan})
	if updated != nil {
		t.Fatalf("unexpected result: %#v", updated)
	}
	if !commitHit.Load() {
		t.Fatal("commit failure hook was not reached after the event write")
	}
	assertManagedUserFailureRollback(t, ctx, db, redisStore, target, issued, err)
}

// TestUpdateManagedUserConcurrentSamePlanCommitsOneSecurityTransition uses
// two independent sql.DB pools and a start barrier. The row locks make one
// call perform the durable security transition while the other returns the
// now-semantic no-op result.
func TestUpdateManagedUserConcurrentSamePlanCommitsOneSecurityTransition(t *testing.T) {
	ctx, db, redisStore, auth, actor, target, issued := managedUserFailureFixture(t)
	var database string
	if err := db.Raw("SELECT DATABASE()").Scan(&database).Error; err != nil || database == "" {
		t.Fatalf("read child database: %q %v", database, err)
	}
	rawURL, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rawURL.Path = "/" + database
	rawURL.RawPath = ""
	secondDB, err := projectdb.Open(rawURL.String(), "test")
	if err != nil {
		t.Fatal(err)
	}
	secondSQL, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondSQL.Close() })
	secondAuth := NewAuthService(&config.Settings{}, nil, secondDB)
	secondAuth.SetSessionService(NewSessionService(secondDB, redisStore, testSessionSettings()))
	plan := models.PlanProfessional
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan error, 2)
	for _, service := range []*AuthService{auth, secondAuth} {
		go func(service *AuthService) {
			ready <- struct{}{}
			<-start
			updated, err := service.UpdateManagedUser(ctx, actor.ID, target.Guid, ManagedUserUpdateInput{PlanType: &plan})
			if err == nil && updated == nil {
				err = errors.New("managed-user-failure-marker nil successful result")
			}
			results <- err
		}(service)
	}
	<-ready
	<-ready
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent update error: %v", err)
		}
	}
	var stored models.User
	if err := db.Where("id = ? AND is_deleted = 0", target.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.PlanType != models.PlanProfessional || stored.AuthVersion != target.AuthVersion+1 {
		t.Fatalf("persisted concurrent state=%#v", stored)
	}
	assertChangePasswordSessionRevoked(t, db, issued.Session.ID)
	assertChangePasswordAuditCount(t, db, target.ID, models.AuthAuditEventManagedUserUpdated, 1)
	assertChangePasswordAuditCount(t, db, target.ID, models.AuthAuditEventSessionRevoked, 1)
	if revoked, err := redisStore.IsSessionRevoked(ctx, issued.Session.SID); err != nil || !revoked {
		t.Fatalf("Redis session barrier=%t err=%v", revoked, err)
	}
}
