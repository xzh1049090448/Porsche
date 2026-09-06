package service

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net/netip"
	"reflect"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const actionVerificationTTLMillis int64 = 300_000

var (
	ErrActionVerificationInactive    = &HTTPError{Status: 422, Message: "admin action unavailable"}
	ErrActionVerificationForbidden   = &HTTPError{Status: 403, Message: "admin action verification rejected"}
	ErrActionVerificationHidden      = &HTTPError{Status: 404, Message: "admin action target unavailable"}
	ErrActionVerificationConflict    = &HTTPError{Status: 409, Message: "admin action verification conflict"}
	ErrActionVerificationUnavailable = &HTTPError{Status: 503, Message: "admin action verification unavailable"}
)

type VerificationIssue struct {
	Action          actionsecurity.Action
	Actor           ActionActor
	TargetGUID      *int64
	Intent          any
	CurrentPassword []byte
	TrustedIP       string
}

type IssuedVerification struct {
	Ticket    string
	ExpiresAt int64
}

type ActionVerificationService struct {
	db        *gorm.DB
	redis     *ActionSecurityRedis
	authRedis *AuthRedis
	crypto    *actionsecurity.Crypto
	resolve   func(actionsecurity.Action) (actionsecurity.Descriptor, bool)
	clock     persistence.Clock
	random    io.Reader
	nextGUID  func() int64
}

// NewActionVerificationService constructs the production service with the
// reviewed active action registry.
func NewActionVerificationService(db *gorm.DB, limiter *ActionSecurityRedis, authRedis *AuthRedis, crypto *actionsecurity.Crypto) (*ActionVerificationService, error) {
	return newActionVerificationService(db, limiter, authRedis, crypto, actionsecurity.ResolveActiveAction, persistence.SystemClock(), cryptorand.Reader, persistence.NextGUID)
}

// NewActionVerificationServiceFromAuthRedis lets app wiring share the already
// verified standalone Redis client without exposing it outside this package.
func NewActionVerificationServiceFromAuthRedis(db *gorm.DB, authRedis *AuthRedis, crypto *actionsecurity.Crypto) (*ActionVerificationService, error) {
	if authRedis == nil || redisClientIsNil(authRedis.client) {
		return nil, ErrActionVerificationUnavailable
	}
	limiter, err := NewActionSecurityRedis(authRedis.client, crypto)
	if err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	return NewActionVerificationService(db, limiter, authRedis, crypto)
}

func newActionVerificationService(db *gorm.DB, limiter *ActionSecurityRedis, authRedis *AuthRedis, crypto *actionsecurity.Crypto,
	resolve func(actionsecurity.Action) (actionsecurity.Descriptor, bool), clock persistence.Clock, random io.Reader, nextGUID func() int64,
) (*ActionVerificationService, error) {
	if db == nil || db.Statement == nil || db.Statement.ConnPool == nil || limiter == nil ||
		redisClientIsNil(limiter.client) || limiter.crypto == nil || limiter.crypto != crypto ||
		authRedis == nil || redisClientIsNil(authRedis.client) || crypto == nil || resolve == nil ||
		interfaceNil(clock) || interfaceNil(random) || nextGUID == nil {
		return nil, ErrActionVerificationUnavailable
	}
	return &ActionVerificationService{db: db, redis: limiter, authRedis: authRedis, crypto: crypto, resolve: resolve, clock: clock, random: random, nextGUID: nextGUID}, nil
}

func (s *ActionVerificationService) Issue(ctx context.Context, in VerificationIssue) (*IssuedVerification, error) {
	defer clear(in.CurrentPassword)
	defer clearCreateVerificationPassword(in.Intent)
	if s == nil || ctx == nil || s.resolve == nil {
		return nil, ErrActionVerificationUnavailable
	}
	descriptor, ok := s.resolve(in.Action)
	if !ok || !descriptor.Active || !descriptor.RequiresTicket || descriptor.Action != in.Action ||
		descriptor.Name == "" || descriptor.Capability == "" || descriptor.Encode == nil {
		return nil, ErrActionVerificationInactive
	}
	if in.Actor.UserID <= 0 || in.Actor.UserGUID <= 0 || in.Actor.AuthVersion <= 0 ||
		len(in.Actor.SessionSID) != 36 || in.Actor.SessionVersion <= 0 || len(in.CurrentPassword) == 0 {
		return nil, ErrActionVerificationForbidden
	}

	// The limiter must run before any SQL. The authenticated SID is used only as
	// input to the purpose-separated session-rate HMAC and is never retained.
	if err := s.reserveVerification(ctx, in.Actor.UserID, in.Actor.SessionSID, in.TrustedIP); err != nil {
		var retry *RetryAfterError
		if errors.As(err, &retry) {
			return nil, err
		}
		return nil, ErrActionVerificationUnavailable
	}
	revoked, err := s.authRedis.IsSessionRevoked(ctx, in.Actor.SessionSID)
	if err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	if revoked {
		return nil, ErrActionVerificationForbidden
	}

	now := s.clock.NowMillis()
	if now <= 0 || now > math.MaxInt64-actionVerificationTTLMillis {
		return nil, ErrActionVerificationUnavailable
	}
	var issued *IssuedVerification
	err = s.db.Session(&gorm.Session{NewDB: true, Logger: logger.Discard}).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		identity, err := lockActionIdentity(tx, in.Actor, descriptor, in.TargetGUID, now)
		if err != nil {
			return err
		}
		// A row lock may have waited. Re-read the injected clock after every
		// identity/target/policy lock so an expired session cannot be accepted
		// with the pre-transaction timestamp and the ticket receives a full,
		// exact five-minute lifetime from the successful locked check.
		lockedNow := s.clock.NowMillis()
		if lockedNow < now || lockedNow <= 0 || lockedNow > math.MaxInt64-actionVerificationTTLMillis {
			return ErrActionVerificationUnavailable
		}
		if identity.session.ExpiresAt <= lockedNow {
			return ErrActionVerificationForbidden
		}
		switch descriptor.Action {
		case actionsecurity.ActionUsersDelete:
			if err := validateLockedDeleteIntent(descriptor, in.Intent, identity.target); err != nil {
				return err
			}
		case actionsecurity.ActionUsersCreateAdmin:
			if err := validateLockedCreateAdminIntent(tx, descriptor, in.Intent, identity.actor); err != nil {
				return err
			}
		default:
			return ErrActionVerificationUnavailable
		}
		encoded, err := descriptor.Encode(in.Intent)
		if err != nil {
			return ErrActionVerificationConflict
		}
		intentDigest := s.crypto.IntentDigest(encoded)
		clear(encoded)
		intentHex := hex.EncodeToString(intentDigest[:])
		clear(intentDigest[:])
		passwordOK := identity.actor.PasswordHash != nil && security.VerifyPassword(string(in.CurrentPassword), *identity.actor.PasswordHash)
		clear(in.CurrentPassword)
		if !passwordOK {
			return ErrActionVerificationForbidden
		}

		var ticketRaw [32]byte
		if _, err := io.ReadFull(s.random, ticketRaw[:]); err != nil {
			clear(ticketRaw[:])
			return ErrActionVerificationUnavailable
		}
		ticketDigest := s.crypto.TicketDigest(ticketRaw)
		ticket := "av_" + base64.RawURLEncoding.EncodeToString(ticketRaw[:])
		clear(ticketRaw[:])
		ticketHex := hex.EncodeToString(ticketDigest[:])
		clear(ticketDigest[:])
		guid := s.nextGUID()
		if guid <= 0 {
			return ErrActionVerificationUnavailable
		}
		expiresAt := lockedNow + actionVerificationTTLMillis
		actorID := identity.actor.ID

		old := tx.Model(&models.AdminActionVerification{}).
			Where("session_id = ? AND action = ? AND target_kind = ? AND consumed_at IS NULL AND is_deleted = 0 AND expires_at > ?", identity.session.ID, int(descriptor.Action), int(descriptor.TargetKind), lockedNow)
		if in.TargetGUID == nil {
			old = old.Where("target_guid IS NULL")
		} else {
			old = old.Where("target_guid = ?", *in.TargetGUID)
		}
		if err := old.Updates(map[string]any{"is_deleted": 1, "updated_at": lockedNow, "updated_by": actorID}).Error; err != nil {
			return ErrActionVerificationUnavailable
		}

		verification := models.AdminActionVerification{
			AuditFields: models.AuditFields{Guid: guid, CreatedAt: lockedNow, CreatedBy: &actorID, UpdatedAt: lockedNow, UpdatedBy: &actorID, IsDeleted: 0},
			ActorUserID: identity.actor.ID, ActorAuthVersion: identity.actor.AuthVersion, SessionID: identity.session.ID,
			Action: int(descriptor.Action), TargetKind: int(descriptor.TargetKind), TargetGUID: copyInt64(in.TargetGUID),
			IntentHMAC: intentHex, TicketHMAC: ticketHex, ExpiresAt: expiresAt,
		}
		if err := tx.Create(&verification).Error; err != nil {
			return ErrActionVerificationUnavailable
		}
		issued = &IssuedVerification{Ticket: ticket, ExpiresAt: expiresAt}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		if errors.Is(err, ErrActionVerificationForbidden) || errors.Is(err, ErrActionVerificationHidden) || errors.Is(err, ErrActionVerificationConflict) {
			return nil, err
		}
		return nil, ErrActionVerificationUnavailable
	}
	if issued == nil {
		return nil, ErrActionVerificationUnavailable
	}
	return issued, nil
}

func clearCreateVerificationPassword(value any) {
	if intent, ok := value.(actionsecurity.CreateAccountIntent); ok {
		clear(intent.Password)
	}
}

func validateLockedCreateAdminIntent(tx *gorm.DB, descriptor actionsecurity.Descriptor, value any, actor models.User) error {
	expected, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersCreateAdmin)
	if !ok || descriptor.Action != expected.Action || descriptor.Name != expected.Name ||
		descriptor.Capability != expected.Capability || descriptor.RootOnly != expected.RootOnly ||
		descriptor.RequiresTicket != expected.RequiresTicket || descriptor.Active != expected.Active ||
		descriptor.TargetKind != expected.TargetKind || descriptor.Encode == nil || expected.Encode == nil ||
		reflect.ValueOf(descriptor.Encode).Pointer() != reflect.ValueOf(expected.Encode).Pointer() {
		return ErrActionVerificationConflict
	}
	intent, ok := value.(actionsecurity.CreateAccountIntent)
	if !ok || actor.ID <= 0 || actor.Guid <= 0 || actor.Role != models.UserRoleRoot || actor.Status != models.UserStatusActive || actor.IsDeleted != 0 ||
		intent.Role != models.UserRoleAdmin.String() || !validCreateVerificationPassword(intent.Password) || len(intent.AllowedModels) != 0 || intent.DailyCallLimit != 100 ||
		(intent.PlanType != int(models.PlanFree) && intent.PlanType != int(models.PlanProfessional) && intent.PlanType != int(models.PlanEnterprise)) {
		return ErrActionVerificationConflict
	}
	username, err := NormalizeUsername(intent.Username)
	if err != nil || username != intent.Username {
		return ErrActionVerificationConflict
	}
	if intent.Nickname != nil {
		nickname, err := NormalizeManagedUserNickname(*intent.Nickname)
		if err != nil || nickname != *intent.Nickname {
			return ErrActionVerificationConflict
		}
	}
	if intent.GroupGUID != nil && *intent.GroupGUID <= 0 {
		return ErrActionVerificationConflict
	}
	for index, override := range intent.Overrides {
		if !validCreateOverride(override) || (index > 0 && intent.Overrides[index-1].Capability >= override.Capability) {
			return ErrActionVerificationConflict
		}
	}
	group, err := lockCreateAccountGroup(tx, intent.GroupGUID)
	if err != nil {
		if errors.Is(err, errCreateAccountGroupRejected) {
			if intent.GroupGUID == nil {
				return ErrActionVerificationUnavailable
			}
			return ErrActionVerificationHidden
		}
		return ErrActionVerificationUnavailable
	}
	evaluator, err := authz.NewEvaluator(actionAccount(actor), nil)
	if err != nil || evaluator.Create(models.UserRoleAdmin) != authz.Allowed {
		return ErrActionVerificationForbidden
	}
	if group.Key != "default" && !createAccountCapabilityAllowed(evaluator, actor, models.UserRoleAdmin, "users.group.change") {
		return ErrActionVerificationForbidden
	}
	if models.PlanType(intent.PlanType) != models.PlanFree && !createAccountCapabilityAllowed(evaluator, actor, models.UserRoleAdmin, "users.plan.change") {
		return ErrActionVerificationForbidden
	}
	return nil
}

func validCreateVerificationPassword(value []byte) bool {
	if !utf8.Valid(value) || utf8.RuneCount(value) < 8 || utf8.RuneCount(value) > 20 {
		return false
	}
	trimmed := bytes.TrimSpace(value)
	for _, weak := range [][]byte{
		[]byte("password"), []byte("password123"), []byte("12345678"),
		[]byte("qwerty123"), []byte("porsche"), []byte("porsche@2026"),
	} {
		if bytes.EqualFold(trimmed, weak) {
			return false
		}
	}
	return true
}

func (s *ActionVerificationService) reserveVerification(ctx context.Context, actorID int64, sessionSID, trustedIP string) error {
	addr, err := netip.ParseAddr(trustedIP)
	if s == nil || s.redis == nil || actorID <= 0 || sessionSID == "" || err != nil || addr.Zone() != "" {
		return ErrActionSecurityRateInput
	}
	actorPayload := encodeActionRateID(actorID)
	keys, err := s.redis.rateKeys([]actionRateIdentity{
		{purpose: actionsecurity.RateVerificationActor, payload: actorPayload[:]},
		{purpose: actionsecurity.RateVerificationIP, payload: []byte(addr.Unmap().String())},
		{purpose: actionsecurity.RateVerificationSession, payload: []byte(sessionSID)},
	})
	if err != nil {
		return err
	}
	return s.redis.reserve(ctx, keys, []actionRateWindowLimit{
		{limit: 5, windowMS: 900_000},
		{limit: 20, windowMS: 900_000},
		{limit: 10, windowMS: 3_600_000},
	})
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func interfaceNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
