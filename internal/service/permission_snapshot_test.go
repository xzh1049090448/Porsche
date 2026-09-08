package service

import (
	"context"
	"errors"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/authz"
	porscheDB "github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func permissionFixture(t *testing.T) (*gorm.DB, *models.User) {
	t.Helper()
	db := openRootTestMySQL(t)
	prepareAuthRegistrationSchema(t, db)
	u := createAuthSessionTestUser(t, db)
	if err := db.Model(u).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	u.Role = models.UserRoleAdmin
	return db, u
}

func TestPermissionBaselineAndPersistedSnapshot(t *testing.T) {
	db, user := permissionFixture(t)
	snapshot, err := LoadPermissionSnapshot(context.Background(), db, user.ID)
	if err != nil || snapshot == nil || snapshot.PolicyVersion() != 0 || snapshot.AuthVersion() != user.AuthVersion || snapshot.CatalogVersion() != 1 || snapshot.Evaluator().Collection("users.read", false) != authz.Allowed {
		t.Fatalf("baseline snapshot: %v", err)
	}
	head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, CatalogVersion: 1, RuleCount: 1}
	rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, Capability: 1, Effect: 3}
	if err := db.Create(&head).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err = LoadPermissionSnapshot(context.Background(), db, user.ID)
	if err != nil || snapshot == nil || snapshot.PolicyVersion() != 1 || snapshot.Evaluator().Collection("users.read", false) != authz.Denied {
		t.Fatalf("persisted deny: %v", err)
	}
}

func TestPermissionEmptyHeadAndDeletedHistory(t *testing.T) {
	db, user := permissionFixture(t)
	head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 3, CatalogVersion: 1, RuleCount: 0}
	if err := db.Create(&head).Error; err != nil {
		t.Fatal(err)
	}
	s, err := LoadPermissionSnapshot(context.Background(), db, user.ID)
	if err != nil || s == nil || s.PolicyVersion() != 3 || s.Evaluator().Collection("users.read", false) != authz.Allowed {
		t.Fatalf("empty head: %v", err)
	}
	db2, user2 := permissionFixture(t)
	history := models.PermissionOverride{AuditFields: testAuditFields(), UserID: user2.ID, PolicyVersion: 1, Capability: 1, Effect: 3}
	history.IsDeleted = 1
	if err := db2.Create(&history).Error; err != nil {
		t.Fatal(err)
	}
	if s, err := LoadPermissionSnapshot(context.Background(), db2, user2.ID); s != nil || !errors.Is(err, ErrPermissionPolicyInvalid) {
		t.Fatalf("historical orphan fallback: %v", err)
	}
}

func TestPermissionRejectsExternalTransaction(t *testing.T) {
	db, user := permissionFixture(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	snapshot, err := LoadPermissionSnapshot(context.Background(), tx, user.ID)
	if snapshot != nil || !errors.Is(err, ErrPermissionReadUnavailable) {
		t.Fatalf("external tx: %v", err)
	}
}

func TestPermissionCorruptAndOrphanStatesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, table, column string
		value               any
	}{
		{"deleted_head", "user_permission_heads", "is_deleted", 1}, {"bad_version", "user_permission_heads", "policy_version", 0},
		{"bad_catalog", "user_permission_heads", "catalog_version", 2}, {"bad_count", "user_permission_heads", "rule_count", 2},
		{"bad_rule_version", "user_permission_overrides", "policy_version", 2}, {"unknown_capability", "user_permission_overrides", "capability", 99},
		{"unknown_effect", "user_permission_overrides", "effect", 99},
		{"root_only", "user_permission_overrides", "capability", 14}, {"unavailable", "user_permission_overrides", "capability", 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, user := permissionFixture(t)
			head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, CatalogVersion: 1, RuleCount: 1}
			rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, Capability: 1, Effect: 3}
			if err := db.Create(&head).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Table(tc.table).Where("user_id = ?", user.ID).Update(tc.column, tc.value).Error; err != nil {
				t.Fatal(err)
			}
			s, err := LoadPermissionSnapshot(context.Background(), db, user.ID)
			if s != nil || !errors.Is(err, ErrPermissionPolicyInvalid) {
				t.Fatalf("accepted corrupt %s: %v", tc.name, err)
			}
		})
	}
	db, user := permissionFixture(t)
	orphan := models.PermissionOverride{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, Capability: 1, Effect: 3}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	if s, err := LoadPermissionSnapshot(context.Background(), db, user.ID); s != nil || !errors.Is(err, ErrPermissionPolicyInvalid) {
		t.Fatalf("orphan fallback: %v", err)
	}
}

func TestPermissionRejectsNilAndClosedRoots(t *testing.T) {
	if s, err := LoadPermissionSnapshot(context.Background(), nil, 1); s != nil || !errors.Is(err, ErrPermissionReadUnavailable) {
		t.Fatalf("nil root: %v", err)
	}
	db, user := permissionFixture(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadPermissionSnapshot(context.Background(), db, user.ID); s != nil || !errors.Is(err, ErrPermissionReadUnavailable) {
		t.Fatalf("closed root: %v", err)
	}
}

func TestPermissionRejectsInactiveActorsAndCancelledRead(t *testing.T) {
	for _, tc := range []struct {
		name, column string
		value        any
	}{
		{"unknown_role", "role", 99}, {"disabled", "status", models.UserStatusDisabled}, {"deleted", "is_deleted", 1}, {"zero_auth_version", "auth_version", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, user := permissionFixture(t)
			if err := db.Model(user).Update(tc.column, tc.value).Error; err != nil {
				t.Fatal(err)
			}
			s, err := LoadPermissionSnapshot(context.Background(), db, user.ID)
			if s != nil || !errors.Is(err, ErrPermissionActorUnavailable) {
				t.Fatalf("accepted %s actor: %v", tc.name, err)
			}
		})
	}
	db, user := permissionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := LoadPermissionSnapshot(ctx, db, user.ID)
	if s != nil || !errors.Is(err, ErrPermissionReadUnavailable) {
		t.Fatalf("cancelled read: %v", err)
	}
}

func TestPermissionCommitFailureReturnsNilSnapshot(t *testing.T) {
	db, user := permissionFixture(t)
	head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, CatalogVersion: 1, RuleCount: 1}
	rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, Capability: 1, Effect: 3}
	if err := db.Create(&head).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	hook := "permission_snapshot_force_rollback"
	var called atomic.Bool
	var rollbackErr error
	if err := db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "user_permission_overrides" && called.CompareAndSwap(false, true) {
			rollbackErr = tx.Rollback().Error
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(hook)
	s, err := LoadPermissionSnapshot(context.Background(), db, user.ID)
	if !called.Load() {
		t.Fatal("commit-failure callback did not observe override query")
	}
	if rollbackErr != nil {
		t.Fatalf("callback could not rollback transaction: %v", rollbackErr)
	}
	if s != nil || !errors.Is(err, ErrPermissionReadUnavailable) {
		t.Fatalf("commit failure returned snapshot: %v", err)
	}
}

func TestPermissionWriterLockPreventsMixedSnapshot(t *testing.T) {
	db, user := permissionFixture(t)
	head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, CatalogVersion: 1, RuleCount: 1}
	rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, Capability: 1, Effect: 2}
	if err := db.Create(&head).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	writer := db.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	defer writer.Rollback()
	var locked models.User
	if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", user.ID).First(&locked).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Model(&models.PermissionPolicyHead{}).Where("user_id = ?", user.ID).Update("policy_version", 2).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	s, err := LoadPermissionSnapshot(ctx, db, user.ID)
	if s != nil || !errors.Is(err, ErrPermissionReadUnavailable) {
		t.Fatalf("loader observed partial policy: %v", err)
	}
	if err := writer.Model(&models.PermissionOverride{}).Where("user_id = ?", user.ID).Updates(map[string]any{"policy_version": 2, "effect": 3}).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit().Error; err != nil {
		t.Fatal(err)
	}
	s, err = LoadPermissionSnapshot(context.Background(), db, user.ID)
	if err != nil || s == nil || s.PolicyVersion() != 2 || s.Evaluator().Collection("users.read", false) != authz.Denied {
		t.Fatalf("post-commit snapshot: %v", err)
	}
}

func TestPermissionDualRootWriterLockSerializesSnapshot(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			db, user := permissionFixture(t)
			head := models.PermissionPolicyHead{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, CatalogVersion: 1, RuleCount: 1}
			rule := models.PermissionOverride{AuditFields: testAuditFields(), UserID: user.ID, PolicyVersion: 1, Capability: 1, Effect: 2}
			if err := db.Create(&head).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
			var owned string
			if err := db.Raw("SELECT DATABASE()").Row().Scan(&owned); err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			parsed.Path = "/" + owned
			parsed.RawPath = ""
			reader, err := porscheDB.Open(parsed.String(), "test")
			if err != nil {
				t.Fatal(err)
			}
			readerSQL, err := reader.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer readerSQL.Close()
			writerSQL, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			if readerSQL == writerSQL {
				t.Fatal("reader is not a distinct root pool")
			}
			before, err := LoadPermissionSnapshot(context.Background(), reader, user.ID)
			if err != nil || before == nil || before.PolicyVersion() != 1 || before.Evaluator().Collection("users.read", false) != authz.Allowed {
				t.Fatalf("reader precondition: %v", err)
			}
			writer := db.Begin()
			if writer.Error != nil {
				t.Fatal(writer.Error)
			}
			defer writer.Rollback()
			var locked models.User
			if err := writer.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", user.ID).First(&locked).Error; err != nil {
				t.Fatal(err)
			}
			if err := writer.Model(&models.PermissionPolicyHead{}).Where("user_id = ?", user.ID).Update("policy_version", 2).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			start := time.Now()
			s, err := LoadPermissionSnapshot(ctx, reader, user.ID)
			elapsed := time.Since(start)
			cancel()
			if s != nil || !errors.Is(err, ErrPermissionReadUnavailable) || elapsed < 200*time.Millisecond {
				t.Fatalf("loader escaped user lock: elapsed=%s err=%v", elapsed, err)
			}
			if err := writer.Model(&models.PermissionOverride{}).Where("user_id = ?", user.ID).Updates(map[string]any{"policy_version": 2, "effect": 3}).Error; err != nil {
				t.Fatal(err)
			}
			if commit {
				err = writer.Commit().Error
			} else {
				err = writer.Rollback().Error
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := LoadPermissionSnapshot(context.Background(), reader, user.ID)
			version := int64(1)
			decision := authz.Allowed
			if commit {
				version = 2
				decision = authz.Denied
			}
			if err != nil || after == nil || after.PolicyVersion() != version || after.Evaluator().Collection("users.read", false) != decision {
				t.Fatalf("post writer snapshot: %v", err)
			}
		})
	}
}
