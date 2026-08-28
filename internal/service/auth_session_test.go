package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
)

// TestAuthSessionCreateEvictsOldestAt51 proves the active-session cap is
// enforced transactionally and never physically deletes session rows.
func TestAuthSessionCreateEvictsOldestAt51(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	user := createAuthSessionTestUser(t, db)
	sessions := NewSessionService(db, redisStore, testSessionSettings())
	ctx := context.Background()

	var oldestGUID int64
	for i := 0; i < 51; i++ {
		issued, err := sessions.Create(ctx, user, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: fmt.Sprintf("198.51.100.%d", i+1)})
		if err != nil {
			t.Fatalf("create session %d: %v", i+1, err)
		}
		if i == 0 {
			oldestGUID = issued.Session.Guid
		}
	}
	var active int64
	if err := db.Model(&models.Session{}).Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL", user.ID).Count(&active).Error; err != nil {
		t.Fatal(err)
	}
	if active != 50 {
		t.Fatalf("active sessions = %d, want 50", active)
	}
	var oldest models.Session
	if err := db.Where("guid = ? AND is_deleted = 0", oldestGUID).First(&oldest).Error; err != nil {
		t.Fatal(err)
	}
	if oldest.RevokedAt == nil {
		t.Fatal("oldest session was not revoked when 51st session was created")
	}
}

// TestRefreshRotationConcurrentRequestsReuseOneResult proves concurrent
// refreshes receive one encrypted, short-lived rotation result rather than
// minting distinct credentials.
func TestRefreshRotationConcurrentRequestsReuseOneResult(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	user := createAuthSessionTestUser(t, db)
	sessions := NewSessionService(db, redisStore, testSessionSettings())
	ctx := context.Background()
	issued, err := sessions.Create(ctx, user, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.21"})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan string, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			rotated, err := sessions.Refresh(ctx, issued.RefreshToken)
			if err != nil {
				errs <- err
				return
			}
			results <- rotated.RefreshToken
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent refresh failed: %v", err)
	}
	var first string
	for result := range results {
		if first == "" {
			first = result
			continue
		}
		if result != first {
			t.Fatalf("concurrent refresh results differ: %q != %q", result, first)
		}
	}
}

// TestRefreshRotationReplayOutsideWindowRevokesSession ensures a stolen old
// refresh token cannot be reused after the 30-second concurrent refresh grace.
func TestRefreshRotationReplayOutsideWindowRevokesSession(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	user := createAuthSessionTestUser(t, db)
	settings := testSessionSettings()
	settings.RefreshReplaySeconds = 1
	sessions := NewSessionService(db, redisStore, settings)
	ctx := context.Background()
	issued, err := sessions.Create(ctx, user, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.22"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Refresh(ctx, issued.RefreshToken); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := sessions.Refresh(ctx, issued.RefreshToken); err == nil {
		t.Fatal("refresh replay outside the concurrent window must fail")
	}
	var stored models.Session
	if err := db.Where("guid = ? AND is_deleted = 0", issued.Session.Guid).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.RevokedAt == nil {
		t.Fatal("refresh replay did not revoke session")
	}
}

// TestRefreshRotationConcurrentOldBReturnsC protects the generation state
// machine under real isolated MySQL and Redis: A→B→C may leave B's public
// result within TTL, but concurrent old-B requests must all receive C.
func TestRefreshRotationConcurrentOldBReturnsC(t *testing.T) {
	redisStore := openTestAuthRedis(t)
	db := openTestMySQL(t)
	prepareAuthSessionSchema(t, db)
	user := createAuthSessionTestUser(t, db)
	sessions := NewSessionService(db, redisStore, testSessionSettings())
	ctx := context.Background()
	a, err := sessions.Create(ctx, user, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.51.100.31"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := sessions.Refresh(ctx, a.RefreshToken)
	if err != nil {
		t.Fatalf("A to B: %v", err)
	}
	c, err := sessions.Refresh(ctx, b.RefreshToken)
	if err != nil {
		t.Fatalf("B to C: %v", err)
	}
	start := make(chan struct{})
	results := make(chan string, 8)
	errs := make(chan error, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			issued, err := sessions.Refresh(ctx, b.RefreshToken)
			if err != nil {
				errs <- err
				return
			}
			results <- issued.RefreshToken
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("old B concurrent refresh: %v", err)
	}
	for result := range results {
		if result != c.RefreshToken || result == b.RefreshToken {
			t.Fatalf("old B received stale or unexpected result")
		}
	}
	var stored models.Session
	if err := db.Where("guid = ? AND is_deleted = 0", a.Session.Guid).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.RefreshHMAC != security.RefreshHMAC(strings.Split(c.RefreshToken, ".")[1], testSessionSettings().AuthHMACKey) || stored.PreviousRefreshHMAC == nil || *stored.PreviousRefreshHMAC != security.RefreshHMAC(strings.Split(b.RefreshToken, ".")[1], testSessionSettings().AuthHMACKey) {
		t.Fatal("stored refresh HMAC generation does not equal C/current and B/previous")
	}
}

func testSessionSettings() *config.Settings {
	return &config.Settings{SessionDays: 30, SessionMaxActive: 50, SessionIssueLimit24h: 100, RefreshReplaySeconds: 30, AuthHMACKey: "test-auth-hmac-key-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ"}
}

func prepareAuthSessionSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	generator := persistence.NewSnowflake(1, persistence.SystemClock())
	if err := migration.Up(context.Background(), db, generator.Next, func() int64 { return time.Now().UTC().UnixMilli() }); err != nil {
		t.Fatalf("apply isolated auth migration: %v", err)
	}
}

func createAuthSessionTestUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	phone := testPhone()
	username := fmt.Sprintf("session_user_%d", testSnowflake.Next())
	user := &models.User{
		AuditFields:   models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0},
		Phone:         &phone,
		Username:      &username,
		Status:        models.UserStatusActive,
		PlanType:      models.PlanFree,
		Role:          models.UserRoleUser,
		AuthVersion:   1,
		AllowedModels: models.JSONSlice{},
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create session test user: %v", err)
	}
	return user
}
