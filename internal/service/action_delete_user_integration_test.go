package service

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type a14DeleteFixture struct {
	db       *gorm.DB
	redis    *redis.Client
	bundle   *UserDeleteActions
	clock    *realActionClock
	actor    models.User
	actorAPI ActionActor
	session  models.Session
	password string
}

func openA14DeleteFixture(t *testing.T, now int64, role models.UserRole, allowDelete bool) *a14DeleteFixture {
	t.Helper()
	db := openTestMySQL(t)
	if err := migration.Up(context.Background(), db, func() int64 { return testSnowflake.Next() }, func() int64 { return now }); err != nil {
		t.Fatal(err)
	}
	rawRedis := strings.TrimSpace(os.Getenv("TEST_REDIS_URL"))
	if rawRedis == "" {
		t.Skip("requires explicit disposable TEST_REDIS_URL")
	}
	opts, err := redis.ParseURL(rawRedis)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	authRedis, err := NewAuthRedis(client, "a14-real-auth-hmac-material")
	if err != nil {
		t.Fatal(err)
	}
	root, reason := actionsecurity.ParseRootKey(strings.TrimSpace(os.Getenv("ACTION_SECURITY_HMAC_KEY")))
	if reason != "" {
		t.Fatalf("invalid isolated action key: %s", reason)
	}
	crypto, err := actionsecurity.NewCrypto(root)
	clear(root)
	if err != nil {
		t.Fatal(err)
	}
	clock := newRealActionClock(now)
	bundle, err := newUserDeleteActions(db, authRedis, crypto, actionsecurity.ActiveActionRegistry, clock, cryptorand.Reader, func() int64 { return testSnowflake.Next() })
	if err != nil {
		t.Fatal(err)
	}
	password := "A14-Strong-Password!"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	username := fixtureUsername(testSnowflake.Next())
	actor := models.User{AuditFields: a14Audit(now), Username: &username, PasswordHash: &hash, Role: role, Status: models.UserStatusActive, AuthVersion: 7, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{}}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	session := models.Session{AuditFields: a14Audit(now), SID: sid, UserID: actor.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 3, RefreshHMAC: strings.Repeat("a", 64), LastActiveAt: now, ExpiresAt: now + 86_400_000}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	if role == models.UserRoleAdmin && allowDelete {
		actorID := actor.ID
		head := models.PermissionPolicyHead{AuditFields: a14AuditBy(now, &actorID), UserID: actor.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
		if err := db.Create(&head).Error; err != nil {
			t.Fatal(err)
		}
		capability, _ := models.PermissionCapabilityCode("users.delete")
		effect, _ := models.PermissionEffectCode("allow")
		rule := models.PermissionOverride{AuditFields: a14AuditBy(now, &actorID), UserID: actor.ID, PolicyVersion: 1, Capability: capability, Effect: effect}
		if err := db.Create(&rule).Error; err != nil {
			t.Fatal(err)
		}
	}
	return &a14DeleteFixture{db: db, redis: client, bundle: bundle, clock: clock, actor: actor, session: session, password: password,
		actorAPI: ActionActor{UserID: actor.ID, UserGUID: actor.Guid, AuthVersion: actor.AuthVersion, SessionSID: sid, SessionVersion: session.SessionVersion}}
}

func a14Audit(now int64) models.AuditFields {
	return models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now}
}

func a14AuditBy(now int64, actor *int64) models.AuditFields {
	value := a14Audit(now)
	value.CreatedBy, value.UpdatedBy = actor, actor
	return value
}

func (f *a14DeleteFixture) createTarget(t *testing.T, role models.UserRole, status models.UserStatus, authVersion int) models.User {
	t.Helper()
	username := fixtureUsername(testSnowflake.Next())
	nickname, phone, realName, idCard := "delete-me", testPhone(), "private", strings.Repeat("c", 64)
	target := models.User{AuditFields: a14Audit(f.clock.NowMillis()), Username: &username, Nickname: &nickname, Phone: &phone, RealName: &realName, IDCardHash: &idCard,
		PasswordHash: f.actor.PasswordHash, Role: role, Status: status, AuthVersion: authVersion, IsVerified: true, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{}}
	if err := f.db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	return target
}

func (f *a14DeleteFixture) issue(t *testing.T, target models.User, intent actionsecurity.DeleteUserIntent) (*IssuedVerification, error) {
	t.Helper()
	password := []byte(f.password)
	issued, err := f.bundle.Verifications.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, Actor: f.actorAPI,
		TargetGUID: &target.Guid, Intent: intent, CurrentPassword: password, TrustedIP: fmt.Sprintf("198.18.%d.%d", f.actor.ID%200+1, target.ID%200+1)})
	return issued, err
}

func (f *a14DeleteFixture) prepare(t *testing.T, target models.User, key string) (*OperationIdentity, actionsecurity.DeleteUserIntent, string) {
	t.Helper()
	intent := actionsecurity.DeleteUserIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, Reason: "  approved deletion  "}
	issued, err := f.issue(t, target, intent)
	if err != nil {
		t.Fatal(err)
	}
	identity, view, err := f.bundle.Operations.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersDelete, Actor: f.actorAPI,
		IdempotencyKeyValues: []string{key}, TicketValues: []string{issued.Ticket}, Intent: intent})
	if err != nil || identity == nil || view == nil || view.Status != "processing" || !identity.ReadyForExecution() {
		t.Fatal("prepare did not return one executable processing operation")
	}
	return identity, intent, issued.Ticket
}

func TestDeleteUserRealLifecycleRootAdminAndAuthorizedAdmin(t *testing.T) {
	cases := []struct {
		name          string
		actor, target models.UserRole
		allow         bool
	}{
		{name: "root_to_user", actor: models.UserRoleRoot, target: models.UserRoleUser},
		{name: "root_to_admin", actor: models.UserRoleRoot, target: models.UserRoleAdmin},
		{name: "authorized_admin_to_user", actor: models.UserRoleAdmin, target: models.UserRoleUser, allow: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := int64(1_900_000_000_000 + i*1_000_000)
			f := openA14DeleteFixture(t, now, tc.actor, tc.allow)
			target := f.createTarget(t, tc.target, models.UserStatusActive, 4)
			sessionSettings := &config.Settings{SessionDays: 1, SessionMaxActive: 10, SessionIssueLimit24h: 20, RefreshReplaySeconds: 30, AuthHMACKey: "a14-refresh-hmac-material"}
			sessionsService := NewSessionService(f.db, mustA14AuthRedis(t, f.redis), sessionSettings)
			sessionsService.now = func() int64 { return now }
			issuedSession, err := sessionsService.Create(context.Background(), &target, SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: "198.18.1.1", UserAgent: "a14-real-fixture"})
			if err != nil {
				t.Fatal(err)
			}
			expired := models.Session{AuditFields: a14Audit(now), SID: mustA14SID(t), UserID: target.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 8, RefreshHMAC: strings.Repeat("e", 64), LastActiveAt: now - 100_000, ExpiresAt: now - 1}
			if err := f.db.Create(&expired).Error; err != nil {
				t.Fatal(err)
			}
			gatewayTokens := NewGatewayTokenService(f.db)
			token, gatewaySecret, err := gatewayTokens.Create(&target, GatewayTokenCreateInput{Name: "delete-token"})
			if err != nil {
				t.Fatal(err)
			}
			head := models.PermissionPolicyHead{AuditFields: a14Audit(now), UserID: target.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
			if err := f.db.Create(&head).Error; err != nil {
				t.Fatal(err)
			}
			capability, _ := models.PermissionCapabilityCode("users.read")
			effect, _ := models.PermissionEffectCode("deny")
			rule := models.PermissionOverride{AuditFields: a14Audit(now), UserID: target.ID, PolicyVersion: 1, Capability: capability, Effect: effect}
			if err := f.db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}

			key := newRealIdempotencyKey(t)
			identity, intent, ticket := f.prepare(t, target, key)
			execution, err := f.bundle.NewExecution(intent)
			if err != nil {
				t.Fatal(err)
			}
			view, err := f.bundle.Operations.Execute(context.Background(), identity, execution, execution, f.bundle.Outbox)
			if err != nil || view == nil || view.Status != "succeeded" || view.PublicRef != identity.PublicRef {
				t.Fatal("execute did not return the bound succeeded operation")
			}
			replayIdentity, replayView, replayErr := f.bundle.Operations.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersDelete, Actor: f.actorAPI, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: intent})
			if replayErr != nil || replayIdentity == nil || replayIdentity.ReadyForExecution() || replayView == nil || replayView.Status != "succeeded" || replayView.PublicRef != identity.PublicRef {
				t.Fatal("terminal ticket/idempotency replay binding mismatch")
			}

			var stored models.User
			if err := f.db.Unscoped().First(&stored, target.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.IsDeleted != 1 || stored.Status != models.UserStatusDisabled || stored.AuthVersion != target.AuthVersion+1 || stored.Username == nil || *stored.Username != *target.Username || stored.PasswordHash != nil || stored.Phone != nil || stored.RealName != nil || stored.IDCardHash != nil || stored.Nickname != nil || stored.IsVerified {
				t.Fatal("deleted user projection mismatch")
			}
			var sessions []models.Session
			if err := f.db.Where("user_id = ?", target.ID).Order("id").Find(&sessions).Error; err != nil {
				t.Fatal(err)
			}
			if len(sessions) != 2 || sessions[0].RevokedAt == nil || sessions[1].RevokedAt == nil || sessions[0].SessionVersion != issuedSession.Session.SessionVersion+1 || sessions[1].SessionVersion != 9 {
				t.Fatal("session revocation facts mismatch")
			}
			if err := f.db.First(token, token.ID).Error; err != nil || token.Status != models.GatewayTokenRevoked {
				t.Fatal("gateway token revocation fact mismatch")
			}
			if err := f.db.Unscoped().First(&head, head.ID).Error; err != nil || head.IsDeleted != 1 {
				t.Fatal("policy head tombstone fact mismatch")
			}
			if err := f.db.Unscoped().First(&rule, rule.ID).Error; err != nil || rule.IsDeleted != 1 {
				t.Fatal("policy override tombstone fact mismatch")
			}
			assertA14TerminalFacts(t, f, identity, target, intent, key, ticket)
			if validated, validateErr := sessionsService.Validate(context.Background(), issuedSession.Session.SID, target.ID, issuedSession.Session.SessionVersion, target.AuthVersion); validated != nil || validateErr == nil {
				t.Fatal("old access or logical session remained valid")
			}
			if refreshed, refreshErr := sessionsService.Refresh(context.Background(), issuedSession.RefreshToken); refreshed != nil || refreshErr == nil {
				t.Fatal("old refresh credential remained valid")
			}
			if listed, listErr := sessionsService.List(context.Background(), target.ID, target.AuthVersion); listErr != nil || len(listed) != 0 {
				t.Fatal("deleted logical session listing mismatch")
			}
			if principal, gatewayErr := gatewayTokens.AuthenticatePrincipal(gatewaySecret, "", "", time.UnixMilli(now)); principal != nil || !IsGatewayTokenError(gatewayErr, GatewayTokenRevoked) {
				t.Fatal("old gateway credential remained valid")
			}

			auth := NewAuthService(&config.Settings{RegisterEnabled: true, PasswordRegisterEnabled: true}, nil, f.db)
			auth.SetSessionService(NewSessionService(f.db, mustA14AuthRedis(t, f.redis), &config.Settings{}))
			if created, registerErr := auth.RegisterUsername(context.Background(), *target.Username, "Another-Strong-1!", nil); created != nil || registerErr == nil {
				t.Fatal("tombstoned username was not permanently reserved")
			}
			if second, secondErr := f.issue(t, target, intent); second != nil || !errors.Is(secondErr, ErrActionVerificationHidden) {
				t.Fatal("duplicate delete issue did not fail hidden")
			}
		})
	}
}

func TestDeleteUserRealPolicyDriftTicketAndIdempotency(t *testing.T) {
	t.Run("policy_and_target_boundaries", func(t *testing.T) {
		now := int64(1_900_100_000_000)
		for _, tc := range []struct {
			name                  string
			actorRole, targetRole models.UserRole
			allow                 bool
			status                models.UserStatus
			version               int
			self, missing         bool
			want                  error
		}{
			{name: "deny", actorRole: models.UserRoleAdmin, targetRole: models.UserRoleUser, status: models.UserStatusActive, version: 4, want: ErrActionVerificationForbidden},
			{name: "self", actorRole: models.UserRoleRoot, targetRole: models.UserRoleRoot, status: models.UserStatusActive, version: 7, self: true, want: ErrActionVerificationHidden},
			{name: "equal", actorRole: models.UserRoleAdmin, targetRole: models.UserRoleAdmin, allow: true, status: models.UserStatusActive, version: 4, want: ErrActionVerificationHidden},
			{name: "higher", actorRole: models.UserRoleAdmin, targetRole: models.UserRoleRoot, allow: true, status: models.UserStatusActive, version: 4, want: ErrActionVerificationHidden},
			{name: "root_target", actorRole: models.UserRoleRoot, targetRole: models.UserRoleRoot, status: models.UserStatusActive, version: 4, want: ErrActionVerificationHidden},
			{name: "disabled", actorRole: models.UserRoleRoot, targetRole: models.UserRoleUser, status: models.UserStatusDisabled, version: 4, want: ErrActionVerificationConflict},
			{name: "overflow", actorRole: models.UserRoleRoot, targetRole: models.UserRoleUser, status: models.UserStatusActive, version: math.MaxInt32, want: ErrActionVerificationConflict},
			{name: "missing", actorRole: models.UserRoleRoot, targetRole: models.UserRoleUser, status: models.UserStatusActive, version: 4, missing: true, want: ErrActionVerificationHidden},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := openA14DeleteFixture(t, now+testSnowflake.Next()%100_000, tc.actorRole, tc.allow)
				target := f.createTarget(t, tc.targetRole, tc.status, tc.version)
				if tc.self {
					target = f.actor
				}
				if tc.missing {
					target.Guid = testSnowflake.Next()
				}
				intent := actionsecurity.DeleteUserIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, Reason: "boundary"}
				issued, err := f.issue(t, target, intent)
				if issued != nil || !errors.Is(err, tc.want) {
					t.Fatalf("boundary issue result mismatch for %s", tc.name)
				}
			})
		}
	})

	t.Run("version_drift_at_execute", func(t *testing.T) {
		f := openA14DeleteFixture(t, 1_900_110_000_000, models.UserRoleRoot, false)
		target := f.createTarget(t, models.UserRoleUser, models.UserStatusActive, 4)
		identity, intent, _ := f.prepare(t, target, newRealIdempotencyKey(t))
		if err := f.db.Model(&models.User{}).Where("id = ?", target.ID).Update("auth_version", 5).Error; err != nil {
			t.Fatal(err)
		}
		execution, _ := f.bundle.NewExecution(intent)
		view, err := f.bundle.Operations.Execute(context.Background(), identity, execution, execution, f.bundle.Outbox)
		if err != nil || view == nil || view.Status != "failed" || view.FailureCode == nil || *view.FailureCode != "target_version_conflict" {
			t.Fatal("version drift terminal result mismatch")
		}
	})

	t.Run("state_drift_at_execute", func(t *testing.T) {
		f := openA14DeleteFixture(t, 1_900_115_000_000, models.UserRoleRoot, false)
		target := f.createTarget(t, models.UserRoleUser, models.UserStatusActive, 4)
		identity, intent, _ := f.prepare(t, target, newRealIdempotencyKey(t))
		if err := f.db.Model(&models.User{}).Where("id = ?", target.ID).Update("status", models.UserStatusDisabled).Error; err != nil {
			t.Fatal(err)
		}
		execution, _ := f.bundle.NewExecution(intent)
		view, err := f.bundle.Operations.Execute(context.Background(), identity, execution, execution, f.bundle.Outbox)
		if err != nil || view == nil || view.Status != "failed" || view.FailureCode == nil || *view.FailureCode != "target_state_conflict" {
			t.Fatal("state drift terminal result mismatch")
		}
	})

	t.Run("expired_ticket", func(t *testing.T) {
		f := openA14DeleteFixture(t, 1_900_120_000_000, models.UserRoleRoot, false)
		target := f.createTarget(t, models.UserRoleUser, models.UserStatusActive, 4)
		intent := actionsecurity.DeleteUserIntent{TargetGUID: target.Guid, ExpectedAuthVersion: 4, Reason: "expiry"}
		issued, err := f.issue(t, target, intent)
		if err != nil {
			t.Fatal(err)
		}
		f.clock.Set(issued.ExpiresAt)
		identity, view, err := f.bundle.Operations.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersDelete, Actor: f.actorAPI, IdempotencyKeyValues: []string{newRealIdempotencyKey(t)}, TicketValues: []string{issued.Ticket}, Intent: intent})
		if identity != nil || view != nil || !errors.Is(err, ErrActionOperationForbidden) {
			t.Fatal("expired ticket begin result mismatch")
		}
	})

	t.Run("same_key_payload_replay_conflict_and_query", func(t *testing.T) {
		f := openA14DeleteFixture(t, 1_900_130_000_000, models.UserRoleRoot, false)
		target := f.createTarget(t, models.UserRoleUser, models.UserStatusActive, 4)
		key := newRealIdempotencyKey(t)
		identity, intent, ticket := f.prepare(t, target, key)
		replay, view, err := f.bundle.Operations.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersDelete, Actor: f.actorAPI, IdempotencyKeyValues: []string{key}, TicketValues: []string{ticket}, Intent: intent})
		if err != nil || replay == nil || replay.ReadyForExecution() || view == nil || view.PublicRef != identity.PublicRef {
			t.Fatal("same-key same-payload replay binding mismatch")
		}
		changed := intent
		changed.Reason = "changed payload"
		issued2, err := f.issue(t, target, changed)
		if err != nil {
			t.Fatal(err)
		}
		conflict, conflictView, conflictErr := f.bundle.Operations.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersDelete, Actor: f.actorAPI, IdempotencyKeyValues: []string{key}, TicketValues: []string{issued2.Ticket}, Intent: changed})
		if conflict != nil || conflictView != nil || !errors.Is(conflictErr, ErrActionOperationConflict) {
			t.Fatal("changed-payload replay did not conflict")
		}
	})

	t.Run("refresh_same_logical_session_and_cross_session", func(t *testing.T) {
		f := openA14DeleteFixture(t, 1_900_140_000_000, models.UserRoleRoot, false)
		target := f.createTarget(t, models.UserRoleUser, models.UserStatusActive, 4)
		intent := actionsecurity.DeleteUserIntent{TargetGUID: target.Guid, ExpectedAuthVersion: 4, Reason: "refresh"}
		issued, err := f.issue(t, target, intent)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&models.Session{}).Where("id = ?", f.session.ID).Update("session_version", gorm.Expr("session_version + 1")).Error; err != nil {
			t.Fatal(err)
		}
		f.actorAPI.SessionVersion++
		key := newRealIdempotencyKey(t)
		identity, _, err := f.bundle.Operations.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersDelete, Actor: f.actorAPI, IdempotencyKeyValues: []string{key}, TicketValues: []string{issued.Ticket}, Intent: intent})
		if err != nil || identity == nil {
			t.Fatal("same logical session refresh did not preserve begin authority")
		}
		secondSID := mustA14SID(t)
		secondSession := models.Session{AuditFields: a14Audit(f.clock.NowMillis()), SID: secondSID, UserID: f.actor.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 1, RefreshHMAC: strings.Repeat("f", 64), LastActiveAt: f.clock.NowMillis(), ExpiresAt: f.clock.NowMillis() + 60_000}
		if err := f.db.Create(&secondSession).Error; err != nil {
			t.Fatal(err)
		}
		secondActor := f.actorAPI
		secondActor.SessionSID = secondSID
		secondActor.SessionVersion = 1
		password := []byte(f.password)
		secondTicket, err := f.bundle.Verifications.Issue(context.Background(), VerificationIssue{Action: actionsecurity.ActionUsersDelete, Actor: secondActor, TargetGUID: &target.Guid, Intent: intent, CurrentPassword: password, TrustedIP: "198.18.240.1"})
		if err != nil {
			t.Fatal(err)
		}
		cross, crossView, crossErr := f.bundle.Operations.Begin(context.Background(), OperationBegin{Action: actionsecurity.ActionUsersDelete, Actor: secondActor, IdempotencyKeyValues: []string{key}, TicketValues: []string{secondTicket.Ticket}, Intent: intent})
		if cross != nil || crossView != nil || !errors.Is(crossErr, ErrActionOperationCrossSession) {
			t.Fatal("cross-session idempotency reuse did not conflict")
		}
	})
}

func TestDeleteUserRealWriteFaultsRollbackAndCommitUnknownQueries(t *testing.T) {
	faults := []struct {
		name, table string
		create      bool
		terminal    bool
	}{
		{name: "session", table: "user_sessions"}, {name: "token", table: "gateway_api_tokens"}, {name: "policy_head", table: "user_permission_heads"},
		{name: "policy_override", table: "user_permission_overrides"}, {name: "user", table: "users"}, {name: "auth_audit", table: "auth_audit_events", create: true},
		{name: "management_audit", table: "audit_logs", create: true}, {name: "outbox", table: "admin_action_outbox", create: true}, {name: "terminal_operation", table: "admin_operations", terminal: true},
	}
	for i, tc := range faults {
		t.Run(tc.name, func(t *testing.T) {
			f := openA14DeleteFixture(t, 1_900_200_000_000+int64(i)*1_000_000, models.UserRoleRoot, false)
			target := f.createTarget(t, models.UserRoleUser, models.UserStatusActive, 4)
			dependencies := seedA14DeleteDependencies(t, f, target)
			identity, intent, _ := f.prepare(t, target, newRealIdempotencyKey(t))
			hook := fmt.Sprintf("a14_delete_fault_%s_%d", tc.name, testSnowflake.Next())
			var hits atomic.Int32
			inject := func(tx *gorm.DB) {
				table := tx.Statement.Table
				if table == "" && tx.Statement.Schema != nil {
					table = tx.Statement.Schema.Table
				}
				if table != tc.table {
					return
				}
				if tc.terminal {
					values, ok := tx.Statement.Dest.(map[string]any)
					if !ok || values["finished_at"] == nil {
						return
					}
				}
				hits.Add(1)
				tx.AddError(errors.New("isolated injected write fault"))
			}
			if tc.create {
				if err := f.db.Callback().Create().Before("gorm:create").Register(hook, inject); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.db.Callback().Update().Before("gorm:update").Register(hook, inject); err != nil {
					t.Fatal(err)
				}
			}
			remove := func() {
				if tc.create {
					_ = f.db.Callback().Create().Remove(hook)
				} else {
					_ = f.db.Callback().Update().Remove(hook)
				}
			}
			t.Cleanup(remove)
			execution, _ := f.bundle.NewExecution(intent)
			view, err := f.bundle.Operations.Execute(context.Background(), identity, execution, execution, f.bundle.Outbox)
			remove()
			if view != nil || !errors.Is(err, ErrActionOperationUnavailable) || hits.Load() != 1 {
				t.Fatalf("injected write fault result mismatch; hit count=%d", hits.Load())
			}
			assertA14RolledBack(t, f.db, identity, target, dependencies)
		})
	}

	t.Run("commit_unknown_resolves_only_through_query", func(t *testing.T) {
		f := openA14DeleteFixture(t, 1_900_220_000_000, models.UserRoleRoot, false)
		target := f.createTarget(t, models.UserRoleUser, models.UserStatusActive, 4)
		seedA14DeleteDependencies(t, f, target)
		key := newRealIdempotencyKey(t)
		identity, intent, _ := f.prepare(t, target, key)
		execution, _ := f.bundle.NewExecution(intent)
		runner := &realCommitUnknownRunner{}
		view, err := f.bundle.Operations.executeWithRunner(context.Background(), identity, execution, execution, f.bundle.Outbox, runner)
		var unknown *CommitUnknownError
		if view != nil || !errors.As(err, &unknown) || unknown.PublicRef != identity.PublicRef || runner.calls.Load() != 1 {
			t.Fatalf("commit-unknown result mismatch; runner calls=%d", runner.calls.Load())
		}
		queried, queryErr := f.bundle.Operations.Query(context.Background(), actionsecurity.ActionUsersDelete, f.actorAPI, []string{key})
		if queryErr != nil || queried == nil || queried.Status != "succeeded" || queried.PublicRef != identity.PublicRef {
			t.Fatal("commit-unknown query resolution mismatch")
		}
		if replay, replayErr := f.bundle.Operations.Execute(context.Background(), identity, execution, execution, f.bundle.Outbox); replay != nil || !errors.Is(replayErr, ErrActionOperationUnavailable) {
			t.Fatal("commit-unknown execute replay was not rejected")
		}
	})
}

type a14SeededDeleteDependencies struct {
	token models.GatewayAPIToken
	head  models.PermissionPolicyHead
	rule  models.PermissionOverride
}

func seedA14DeleteDependencies(t *testing.T, f *a14DeleteFixture, target models.User) a14SeededDeleteDependencies {
	t.Helper()
	now := f.clock.NowMillis()
	actorID := f.actor.ID
	session := models.Session{AuditFields: a14Audit(now), SID: mustA14SID(t), UserID: target.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 1, RefreshHMAC: strings.Repeat("9", 64), LastActiveAt: now, ExpiresAt: now + 60_000}
	if err := f.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	expires := now + 60_000
	token := models.GatewayAPIToken{AuditFields: a14Audit(now), UserID: target.ID, Name: "fault-token", TokenHash: fmt.Sprintf("%064x", testSnowflake.Next()), TokenPrefix: "sk-a14", Status: models.GatewayTokenActive, AllowedModels: models.JSONSlice{}, IPAllowlist: models.JSONSlice{}, ExpiresAt: &expires}
	if err := f.db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	head := models.PermissionPolicyHead{AuditFields: a14AuditBy(now, &actorID), UserID: target.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
	if err := f.db.Create(&head).Error; err != nil {
		t.Fatal(err)
	}
	capability, _ := models.PermissionCapabilityCode("users.read")
	effect, _ := models.PermissionEffectCode("deny")
	rule := models.PermissionOverride{AuditFields: a14AuditBy(now, &actorID), UserID: target.ID, PolicyVersion: 1, Capability: capability, Effect: effect}
	if err := f.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	return a14SeededDeleteDependencies{token: token, head: head, rule: rule}
}

func assertA14TerminalFacts(t *testing.T, f *a14DeleteFixture, identity *OperationIdentity, target models.User,
	intent actionsecurity.DeleteUserIntent, key, ticket string,
) {
	t.Helper()
	db := f.db
	var authRows []models.AuthAuditEvent
	if err := db.Where("user_id = ? AND event_type = ?", target.ID, models.AuthAuditEventUserDeleted).Find(&authRows).Error; err != nil {
		t.Fatal(err)
	}
	var auditRows []models.AuditLog
	if err := db.Where("action = ? AND user_id = ?", "users.delete", target.ID).Find(&auditRows).Error; err != nil {
		t.Fatal(err)
	}
	var outboxRows []models.AdminActionOutbox
	if err := db.Where("operation_id = ?", identity.ID).Find(&outboxRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(authRows) != 1 || len(auditRows) != 1 || len(outboxRows) != 1 {
		t.Fatalf("terminal fact counts auth/management/outbox = %d/%d/%d", len(authRows), len(auditRows), len(outboxRows))
	}

	normalizedReason := strings.TrimSpace(intent.Reason)
	audit := auditRows[0]
	if got, ok := audit.Detail["reason"].(string); !ok || got != normalizedReason || got == intent.Reason {
		t.Fatal("management audit did not contain only the normalized deletion reason")
	}
	if audit.Resource == nil || *audit.Resource != fmt.Sprintf("user:%d", target.Guid) || audit.Action != "users.delete" ||
		audit.UserID == nil || *audit.UserID != target.ID || audit.Detail["operation_ref"] != identity.PublicRef ||
		audit.Detail["target_guid"] != float64(target.Guid) || audit.Detail["after_status"] != "deleted" {
		t.Fatal("management audit terminal binding mismatch")
	}
	auth := authRows[0]
	if auth.UserID == nil || *auth.UserID != target.ID || auth.EventType != models.AuthAuditEventUserDeleted ||
		auth.LoginMethod != nil || auth.IP != nil || auth.UserAgent != nil {
		t.Fatal("authentication audit terminal binding mismatch")
	}
	assertA14TableHasNoReasonOrPayloadColumns(t, db, "auth_audit_events", false)
	assertA14TableHasNoReasonOrPayloadColumns(t, db, "admin_action_outbox", true)

	outbox := outboxRows[0]
	if outbox.OperationID != identity.ID || outbox.PublicRef != identity.PublicRef || outbox.Action != int(actionsecurity.ActionUsersDelete) ||
		outbox.TargetKind != int(actionsecurity.TargetUser) || outbox.TargetGUID == nil || *outbox.TargetGUID != target.Guid ||
		outbox.State != models.OperationSucceeded || outbox.ResultGUID == nil || *outbox.ResultGUID != target.Guid ||
		outbox.DeliveryState != models.DeliveryPending || outbox.DeliveredAt != nil || outbox.AttemptCount != 0 {
		t.Fatal("outbox terminal binding mismatch")
	}

	var operationRows []models.AdminOperation
	if err := db.Where("id = ?", identity.ID).Find(&operationRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(operationRows) != 1 {
		t.Fatalf("terminal operation row count=%d", len(operationRows))
	}
	operation := operationRows[0]
	if operation.VerificationID == nil {
		t.Fatal("terminal operation lost verification binding")
	}
	var verificationRows []models.AdminActionVerification
	if err := db.Unscoped().Where("id = ?", *operation.VerificationID).Find(&verificationRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(verificationRows) != 1 {
		t.Fatalf("consumed verification row count=%d", len(verificationRows))
	}
	verification := verificationRows[0]
	keyRaw, err := actionsecurity.ParseIdempotencyKey([]string{key})
	if err != nil {
		t.Fatal("test idempotency key became invalid")
	}
	keyDigest := f.bundle.Operations.crypto.IdempotencyDigest(keyRaw)
	clear(keyRaw[:])
	expectedKeyHMAC := hex.EncodeToString(keyDigest[:])
	clear(keyDigest[:])
	ticketRaw, err := actionsecurity.ParseTicket([]string{ticket})
	if err != nil {
		t.Fatal("test verification ticket became invalid")
	}
	ticketDigest := f.bundle.Operations.crypto.TicketDigest(ticketRaw)
	clear(ticketRaw[:])
	expectedTicketHMAC := hex.EncodeToString(ticketDigest[:])
	clear(ticketDigest[:])
	descriptor, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersDelete)
	if !ok || descriptor.Encode == nil {
		t.Fatal("active users.delete descriptor unavailable")
	}
	encodedIntent, err := descriptor.Encode(intent)
	if err != nil {
		t.Fatal("test intent became invalid")
	}
	requestDigest := f.bundle.Operations.crypto.IntentDigest(encodedIntent)
	clear(encodedIntent)
	expectedRequestHMAC := hex.EncodeToString(requestDigest[:])
	clear(requestDigest[:])
	resultKind := models.ResultUser
	if operation.ActorUserID != f.actor.ID || operation.ActorAuthVersion != f.actor.AuthVersion || operation.SessionID != f.session.ID ||
		operation.Action != int(actionsecurity.ActionUsersDelete) || operation.IdempotencyKeyHMAC != expectedKeyHMAC ||
		operation.RequestHMAC != expectedRequestHMAC || operation.RequestHMAC != verification.IntentHMAC || !validA14HMAC(operation.RequestHMAC) ||
		operation.State != models.OperationSucceeded || operation.PublicRef != identity.PublicRef || operation.LeaseOwnerHMAC != nil ||
		operation.LeaseExpiresAt != nil || operation.FinishedAt == nil || operation.QueryExpiresAt <= *operation.FinishedAt || operation.ErrorCode != nil ||
		operation.ResultKind == nil || *operation.ResultKind != resultKind || operation.ResultGUID == nil || *operation.ResultGUID != target.Guid ||
		operation.ResultHTTPStatus == nil || *operation.ResultHTTPStatus != 200 || operation.CreatedBy == nil || *operation.CreatedBy != f.actor.ID ||
		operation.UpdatedBy == nil || *operation.UpdatedBy != f.actor.ID || operation.IsDeleted != 0 {
		t.Fatal("terminal operation exact binding mismatch")
	}
	if verification.ActorUserID != f.actor.ID || verification.ActorAuthVersion != f.actor.AuthVersion || verification.SessionID != f.session.ID ||
		verification.Action != int(actionsecurity.ActionUsersDelete) || verification.TargetKind != int(actionsecurity.TargetUser) ||
		verification.TargetGUID == nil || *verification.TargetGUID != target.Guid || verification.TicketHMAC != expectedTicketHMAC ||
		verification.IntentHMAC != operation.RequestHMAC || verification.ConsumedAt == nil || verification.IsDeleted != 1 {
		t.Fatal("consumed verification exact binding mismatch")
	}
}

func assertA14TableHasNoReasonOrPayloadColumns(t *testing.T, db *gorm.DB, table string, rejectPayload bool) {
	t.Helper()
	predicates := "LOWER(column_name) LIKE '%reason%'"
	if rejectPayload {
		predicates += " OR LOWER(column_name) LIKE '%payload%'"
	}
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND ("+predicates+")", table).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%s unexpectedly exposes reason or payload columns", table)
	}
}

func validA14HMAC(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func assertA14RolledBack(t *testing.T, db *gorm.DB, identity *OperationIdentity, target models.User, dependencies a14SeededDeleteDependencies) {
	t.Helper()
	var stored models.User
	if err := db.Unscoped().First(&stored, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.IsDeleted != 0 || stored.Status != models.UserStatusActive || stored.AuthVersion != target.AuthVersion {
		t.Fatal("partial user mutation persisted")
	}
	var token models.GatewayAPIToken
	if err := db.Unscoped().First(&token, dependencies.token.ID).Error; err != nil {
		t.Fatal(err)
	}
	if token.Status != dependencies.token.Status || token.Status != models.GatewayTokenActive || token.IsDeleted != dependencies.token.IsDeleted ||
		token.UpdatedAt != dependencies.token.UpdatedAt || !equalA14OptionalInt64(token.UpdatedBy, dependencies.token.UpdatedBy) {
		t.Fatal("partial gateway token revocation persisted")
	}
	var head models.PermissionPolicyHead
	if err := db.Unscoped().First(&head, dependencies.head.ID).Error; err != nil {
		t.Fatal(err)
	}
	if head.IsDeleted != dependencies.head.IsDeleted || head.IsDeleted != 0 || head.UpdatedAt != dependencies.head.UpdatedAt ||
		!equalA14OptionalInt64(head.UpdatedBy, dependencies.head.UpdatedBy) || head.PolicyVersion != dependencies.head.PolicyVersion ||
		head.CatalogVersion != dependencies.head.CatalogVersion || head.RuleCount != dependencies.head.RuleCount {
		t.Fatal("partial policy head tombstone persisted")
	}
	var rule models.PermissionOverride
	if err := db.Unscoped().First(&rule, dependencies.rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if rule.IsDeleted != dependencies.rule.IsDeleted || rule.IsDeleted != 0 || rule.UpdatedAt != dependencies.rule.UpdatedAt ||
		!equalA14OptionalInt64(rule.UpdatedBy, dependencies.rule.UpdatedBy) || rule.PolicyVersion != dependencies.rule.PolicyVersion ||
		rule.Capability != dependencies.rule.Capability || rule.Effect != dependencies.rule.Effect {
		t.Fatal("partial policy override tombstone persisted")
	}
	var revoked, authCount, auditCount, outboxCount int64
	if err := db.Model(&models.Session{}).Where("user_id = ? AND revoked_at IS NOT NULL", target.ID).Count(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ?", target.ID, models.AuthAuditEventUserDeleted).Count(&authCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.AuditLog{}).Where("action = ? AND user_id = ?", "users.delete", target.ID).Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.AdminActionOutbox{}).Where("operation_id = ?", identity.ID).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	var op models.AdminOperation
	if err := db.First(&op, identity.ID).Error; err != nil {
		t.Fatal(err)
	}
	var verification models.AdminActionVerification
	if op.VerificationID == nil {
		t.Fatal("operation lost verification")
	}
	if err := db.Unscoped().First(&verification, *op.VerificationID).Error; err != nil {
		t.Fatal(err)
	}
	if revoked != 0 || authCount != 0 || auditCount != 0 || outboxCount != 0 || op.State != models.OperationProcessing || verification.ConsumedAt != nil || verification.IsDeleted != 0 {
		t.Fatalf("rollback fact mismatch: revoked/auth/management/outbox=%d/%d/%d/%d operation_state=%s verification_consumed=%t verification_deleted=%d",
			revoked, authCount, auditCount, outboxCount, op.State.String(), verification.ConsumedAt != nil, verification.IsDeleted)
	}
}

func equalA14OptionalInt64(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func mustA14SID(t *testing.T) string {
	t.Helper()
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}
func mustA14AuthRedis(t *testing.T, client redis.UniversalClient) *AuthRedis {
	t.Helper()
	store, err := NewAuthRedis(client, "a14-real-auth-hmac-material")
	if err != nil {
		t.Fatal(err)
	}
	return store
}
