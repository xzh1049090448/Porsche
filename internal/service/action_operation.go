package service

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"sync"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	actionOperationLeaseMillis      int64 = 30_000
	actionOperationRecoveryGraceMS  int64 = 60_000
	actionOperationQueryRetentionMS int64 = 2_592_000_000
)

var (
	ErrActionOperationInactive     = &HTTPError{Status: 422, Message: "admin action unavailable"}
	ErrActionOperationForbidden    = &HTTPError{Status: 403, Message: "admin action verification rejected"}
	ErrActionOperationHidden       = &HTTPError{Status: 404, Message: "admin operation unavailable"}
	ErrActionOperationConflict     = &HTTPError{Status: 409, Message: "idempotency_conflict"}
	ErrActionOperationCrossSession = &HTTPError{Status: 409, Message: "idempotency_cross_session"}
	ErrActionOperationExpired      = &HTTPError{Status: 410, Message: "admin operation expired"}
	ErrActionOperationUnavailable  = &HTTPError{Status: 503, Message: "admin operation unavailable"}
)

type OperationBegin struct {
	Action               actionsecurity.Action
	Actor                ActionActor
	IdempotencyKeyValues []string
	TicketValues         []string
	Intent               any
}

type OperationIdentity struct {
	ID         int64  `json:"-"`
	PublicRef  string `json:"public_ref"`
	actor      ActionActor
	capability *operationLeaseCapability
}

// ReadyForExecution reports whether Begin returned a fresh one-shot execution
// lease. Existing operation views never carry this capability and must be
// handled without replaying Execute.
func (identity *OperationIdentity) ReadyForExecution() bool {
	if identity == nil || identity.capability == nil {
		return false
	}
	identity.capability.mu.Lock()
	defer identity.capability.mu.Unlock()
	return !identity.capability.consumed && !operationLeaseIsZero(&identity.capability.raw)
}

// OperationIdentity is an in-memory handoff from Begin to Execute. It cannot
// be serialized and reconstructed because Execute must retain the exact actor
// claims presented to Begin and a shared, one-shot lease capability. Shallow
// copies share that capability, so at most one Execute call can consume it.

type operationLeaseCapability struct {
	mu       sync.Mutex
	raw      [32]byte
	consumed bool
}

func newOperationLeaseCapability(raw *[32]byte) *operationLeaseCapability {
	if raw == nil || operationLeaseIsZero(raw) {
		return nil
	}
	capability := &operationLeaseCapability{raw: *raw}
	clear(raw[:])
	return capability
}

func (capability *operationLeaseCapability) take() ([32]byte, bool) {
	if capability == nil {
		return [32]byte{}, false
	}
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.consumed || operationLeaseIsZero(&capability.raw) {
		capability.consumed = true
		clear(capability.raw[:])
		return [32]byte{}, false
	}
	leaseOwner := capability.raw
	clear(capability.raw[:])
	capability.consumed = true
	return leaseOwner, true
}

func (capability *operationLeaseCapability) discard() {
	if capability == nil {
		return
	}
	capability.mu.Lock()
	clear(capability.raw[:])
	capability.consumed = true
	capability.mu.Unlock()
}

func operationLeaseIsZero(raw *[32]byte) bool {
	var zero [32]byte
	return raw == nil || subtle.ConstantTimeCompare(raw[:], zero[:]) == 1
}

func (identity OperationIdentity) safeString() string {
	return "OperationIdentity{PublicRef:" + strconv.Quote(identity.PublicRef) + "}"
}

func (identity OperationIdentity) String() string { return identity.safeString() }

func (identity OperationIdentity) GoString() string { return identity.safeString() }

func (identity OperationIdentity) Format(state fmt.State, verb rune) {
	formatted := identity.safeString()
	if verb == 'q' {
		formatted = strconv.Quote(formatted)
	}
	_, _ = io.WriteString(state, formatted)
}

func (identity OperationIdentity) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		PublicRef string `json:"public_ref"`
	}{PublicRef: identity.PublicRef})
}

type OperationView struct {
	PublicRef                string
	Scope                    string
	Status                   string
	FinishedAt               *int64
	FailureCode              *string
	RetryAfterSeconds        int
	TargetGUID               *int64
	ResultAuthVersion        *int
	ResultPermissionsVersion *int64
	ResultRole               *models.UserRole
}

type ActionOperationService struct {
	db        *gorm.DB
	limiter   *ActionSecurityRedis
	authRedis *AuthRedis
	crypto    *actionsecurity.Crypto
	resolve   func(actionsecurity.Action) (actionsecurity.Descriptor, bool)
	clock     persistence.Clock
	random    io.Reader
	nextGUID  func() int64
}

func NewActionOperationService(db *gorm.DB, limiter *ActionSecurityRedis, authRedis *AuthRedis, crypto *actionsecurity.Crypto) (*ActionOperationService, error) {
	return newActionOperationService(db, limiter, authRedis, crypto, actionsecurity.ResolveActiveAction, persistence.SystemClock(), cryptorand.Reader, persistence.NextGUID)
}

func newActionOperationService(db *gorm.DB, limiter *ActionSecurityRedis, authRedis *AuthRedis, crypto *actionsecurity.Crypto,
	resolve func(actionsecurity.Action) (actionsecurity.Descriptor, bool), clock persistence.Clock, random io.Reader, nextGUID func() int64,
) (*ActionOperationService, error) {
	if db == nil || db.Statement == nil || db.Statement.ConnPool == nil || limiter == nil ||
		redisClientIsNil(limiter.client) || limiter.crypto == nil || limiter.crypto != crypto ||
		authRedis == nil || redisClientIsNil(authRedis.client) || crypto == nil || resolve == nil ||
		operationInterfaceNil(clock) || operationInterfaceNil(random) || nextGUID == nil {
		return nil, ErrActionOperationUnavailable
	}
	return &ActionOperationService{db: db, limiter: limiter, authRedis: authRedis, crypto: crypto, resolve: resolve, clock: clock, random: random, nextGUID: nextGUID}, nil
}

func (s *ActionOperationService) Begin(ctx context.Context, in OperationBegin) (*OperationIdentity, *OperationView, error) {
	if s == nil || ctx == nil || s.resolve == nil || !validOperationActorClaims(in.Actor) {
		return nil, nil, ErrActionOperationForbidden
	}
	descriptor, ok := s.resolve(in.Action)
	if !validOperationDescriptor(descriptor, in.Action, ok) {
		return nil, nil, ErrActionOperationInactive
	}

	keyRaw, err := actionsecurity.ParseIdempotencyKey(in.IdempotencyKeyValues)
	if err != nil {
		return nil, nil, ErrActionOperationForbidden
	}
	ticketRaw, hasTicket, err := parseOperationTicket(descriptor, in.TicketValues)
	if err != nil {
		clear(keyRaw[:])
		return nil, nil, ErrActionOperationForbidden
	}
	encoded, err := descriptor.Encode(in.Intent)
	if err != nil {
		clear(keyRaw[:])
		clear(ticketRaw[:])
		clear(encoded)
		return nil, nil, ErrActionOperationForbidden
	}
	requestDigest := s.crypto.IntentDigest(encoded)
	clear(encoded)
	parsedTarget, err := operationIntentTargetGUID(descriptor, in.Intent)
	if err != nil {
		clear(keyRaw[:])
		clear(ticketRaw[:])
		clear(requestDigest[:])
		return nil, nil, ErrActionOperationForbidden
	}
	keyDigest := s.crypto.IdempotencyDigest(keyRaw)
	clear(keyRaw[:])
	requestHex := hex.EncodeToString(requestDigest[:])
	keyHex := hex.EncodeToString(keyDigest[:])
	var ticketHex string
	if hasTicket {
		ticketDigest := s.crypto.TicketDigest(ticketRaw)
		ticketHex = hex.EncodeToString(ticketDigest[:])
		clear(ticketDigest[:])
	}
	clear(ticketRaw[:])
	clear(requestDigest[:])
	clear(keyDigest[:])

	// The logical SID is stable across refresh rotation. It is HMACed before
	// Redis and never appears in a Redis key, SQL argument, or retained error.
	if err := s.reserveBegin(ctx, in.Actor.SessionSID); err != nil {
		var retry *RetryAfterError
		if errors.As(err, &retry) {
			return nil, nil, err
		}
		return nil, nil, ErrActionOperationUnavailable
	}
	revoked, err := s.authRedis.IsSessionRevoked(ctx, in.Actor.SessionSID)
	if err != nil {
		return nil, nil, ErrActionOperationUnavailable
	}
	if revoked {
		return nil, nil, ErrActionOperationForbidden
	}

	now := s.clock.NowMillis()
	if !validOperationNow(now) {
		return nil, nil, ErrActionOperationUnavailable
	}
	var identity *OperationIdentity
	var view *OperationView
	expiredOutcome := false
	err = s.operationDB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, lockErr := lockOperationActorSession(tx, in.Actor, now)
		if lockErr != nil {
			return lockErr
		}

		var existing models.AdminOperation
		find := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("actor_user_id = ? AND action = ? AND idempotency_key_hmac = ?", locked.actor.ID, int(descriptor.Action), keyHex).
			First(&existing)
		if find.Error == nil {
			if existing.SessionID != locked.session.ID {
				return ErrActionOperationCrossSession
			}
			if !constantTimeOperationStringEqual(existing.RequestHMAC, requestHex) {
				return ErrActionOperationConflict
			}
			if existing.ActorAuthVersion != locked.actor.AuthVersion || !validOperationState(existing.State) {
				return ErrActionOperationForbidden
			}
			verification, err := lockOperationBeginVerification(tx, descriptor, ticketHex, locked, requestHex, parsedTarget, existing.VerificationID, true)
			if err != nil {
				return err
			}
			// Terminal replays return the already authorized and request-bound
			// result. A successful action may have changed the target so it no
			// longer satisfies the pre-action authorization predicate.
			if existing.State == models.OperationProcessing || existing.State == models.OperationPendingRecovery {
				if err := authorizeOperationDescriptor(tx, locked.actor, descriptor, parsedTarget); err != nil {
					return err
				}
			}
			finalNow := s.clock.NowMillis()
			if finalNow < now || !validOperationNow(finalNow) {
				return ErrActionOperationUnavailable
			}
			if locked.session.ExpiresAt <= finalNow || !validOperationStateWithOptionalVerification(existing, verification, finalNow) ||
				(descriptor.RequiresTicket && !validExistingBeginVerificationState(*verification, existing.State, finalNow)) {
				return ErrActionOperationForbidden
			}
			if existing.State == models.OperationExpired {
				expiredOutcome = true
				return nil
			}
			if finalNow >= existing.QueryExpiresAt && existing.State.CanTransitionTo(models.OperationExpired) {
				if err := expireOperation(tx, &existing, locked.actor.ID, finalNow); err != nil {
					return ErrActionOperationUnavailable
				}
				expiredOutcome = true
				return nil
			}
			identity = &OperationIdentity{ID: existing.ID, PublicRef: existing.PublicRef, actor: in.Actor}
			view = operationView(descriptor, existing, finalNow)
			return nil
		}
		if !errors.Is(find.Error, gorm.ErrRecordNotFound) {
			return ErrActionOperationUnavailable
		}

		publicRef, err := actionsecurity.NewPublicRef(s.random)
		if err != nil {
			return ErrActionOperationUnavailable
		}
		var leaseOwner [32]byte
		if _, err := io.ReadFull(s.random, leaseOwner[:]); err != nil {
			clear(leaseOwner[:])
			return ErrActionOperationUnavailable
		}
		leaseDigest := s.crypto.LeaseOwnerDigest(leaseOwner)
		leaseHex := hex.EncodeToString(leaseDigest[:])
		clear(leaseDigest[:])
		guid := s.nextGUID()
		if guid <= 0 {
			clear(leaseOwner[:])
			return ErrActionOperationUnavailable
		}
		actorID := locked.actor.ID
		leaseExpires := now + actionOperationLeaseMillis
		queryExpires := now + actionOperationQueryRetentionMS
		operation := models.AdminOperation{
			AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID},
			ActorUserID: actorID, ActorAuthVersion: locked.actor.AuthVersion, SessionID: locked.session.ID,
			Action: int(descriptor.Action), IdempotencyKeyHMAC: keyHex, RequestHMAC: requestHex,
			State: models.OperationProcessing, PublicRef: publicRef, LeaseOwnerHMAC: &leaseHex,
			LeaseExpiresAt: &leaseExpires, QueryExpiresAt: queryExpires,
		}
		if err := tx.Create(&operation).Error; err != nil {
			clear(leaseOwner[:])
			return ErrActionOperationUnavailable
		}
		verification, err := lockOperationBeginVerification(tx, descriptor, ticketHex, locked, requestHex, parsedTarget, operation.VerificationID, false)
		if err != nil {
			clear(leaseOwner[:])
			return err
		}
		if err := authorizeOperationDescriptor(tx, locked.actor, descriptor, parsedTarget); err != nil {
			clear(leaseOwner[:])
			return err
		}
		finalNow := s.clock.NowMillis()
		if finalNow < now || !validOperationNow(finalNow) {
			clear(leaseOwner[:])
			return ErrActionOperationUnavailable
		}
		if locked.session.ExpiresAt <= finalNow || (descriptor.RequiresTicket && verificationRelationAt(*verification, finalNow) != operationVerificationActive) {
			clear(leaseOwner[:])
			return ErrActionOperationForbidden
		}
		leaseExpires = finalNow + actionOperationLeaseMillis
		queryExpires = finalNow + actionOperationQueryRetentionMS
		updates := map[string]any{
			"created_at": finalNow, "lease_expires_at": leaseExpires, "query_expires_at": queryExpires,
			"updated_at": finalNow, "updated_by": actorID,
		}
		if descriptor.RequiresTicket {
			updates["verification_id"] = verification.ID
		}
		result := tx.Model(&models.AdminOperation{}).
			Where("id = ? AND state = ? AND is_deleted = 0 AND verification_id IS NULL", operation.ID, models.OperationProcessing).
			Updates(updates)
		if result.Error != nil {
			clear(leaseOwner[:])
			var mysqlErr *mysqlDriver.MySQLError
			if errors.As(result.Error, &mysqlErr) && mysqlErr.Number == 1062 {
				return ErrActionOperationForbidden
			}
			return ErrActionOperationUnavailable
		}
		if descriptor.RequiresTicket {
			operation.VerificationID = &verification.ID
		}
		operation.CreatedAt = finalNow
		operation.UpdatedAt = finalNow
		operation.LeaseExpiresAt = &leaseExpires
		operation.QueryExpiresAt = queryExpires
		if result.RowsAffected == 0 {
			if descriptor.RequiresTicket || !lockedTicketlessBeginNoopMatches(tx, operation) {
				clear(leaseOwner[:])
				return ErrActionOperationUnavailable
			}
		} else if result.RowsAffected != 1 {
			clear(leaseOwner[:])
			return ErrActionOperationUnavailable
		}
		identity = &OperationIdentity{ID: operation.ID, PublicRef: publicRef, actor: in.Actor, capability: newOperationLeaseCapability(&leaseOwner)}
		if identity.capability == nil {
			return ErrActionOperationUnavailable
		}
		view = operationView(descriptor, operation, finalNow)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		if identity != nil {
			identity.capability.discard()
			identity = nil
		}
		return nil, nil, mapOperationError(err)
	}
	if expiredOutcome {
		return nil, nil, ErrActionOperationExpired
	}
	if identity == nil || view == nil {
		return nil, nil, ErrActionOperationUnavailable
	}
	return identity, view, nil
}

func lockedTicketlessBeginNoopMatches(tx *gorm.DB, expected models.AdminOperation) bool {
	if tx == nil || expected.ID <= 0 || expected.VerificationID != nil {
		return false
	}
	var stored models.AdminOperation
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", expected.ID).Take(&stored)
	return result.Error == nil && result.RowsAffected == 1 && reflect.DeepEqual(stored, expected)
}

func (s *ActionOperationService) Query(ctx context.Context, action actionsecurity.Action, actor ActionActor, keyValues []string) (*OperationView, error) {
	if s == nil || ctx == nil || s.resolve == nil || !validOperationActorClaims(actor) {
		return nil, ErrActionOperationHidden
	}
	descriptor, ok := s.resolve(action)
	if !validOperationDescriptor(descriptor, action, ok) {
		return nil, ErrActionOperationHidden
	}
	keyRaw, err := actionsecurity.ParseIdempotencyKey(keyValues)
	if err != nil {
		return nil, ErrActionOperationHidden
	}
	keyDigest := s.crypto.IdempotencyDigest(keyRaw)
	clear(keyRaw[:])
	keyHex := hex.EncodeToString(keyDigest[:])
	clear(keyDigest[:])
	revoked, err := s.authRedis.IsSessionRevoked(ctx, actor.SessionSID)
	if err != nil {
		return nil, ErrActionOperationUnavailable
	}
	if revoked {
		return nil, ErrActionOperationHidden
	}
	now := s.clock.NowMillis()
	if !validOperationNow(now) {
		return nil, ErrActionOperationUnavailable
	}
	var view *OperationView
	expiredOutcome := false
	err = s.operationDB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockOperationActorSession(tx, actor, now)
		if err != nil {
			if errors.Is(err, ErrActionOperationUnavailable) {
				return err
			}
			return ErrActionOperationHidden
		}
		var operation models.AdminOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("actor_user_id = ? AND action = ? AND idempotency_key_hmac = ?", locked.actor.ID, int(descriptor.Action), keyHex).
			First(&operation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrActionOperationHidden
			}
			return ErrActionOperationUnavailable
		}
		if operation.SessionID != locked.session.ID {
			return ErrActionOperationHidden
		}
		if operation.ActorAuthVersion != locked.actor.AuthVersion || !validOperationState(operation.State) {
			return ErrActionOperationHidden
		}
		verification, targetGUID, err := lockOperationQueryVerification(tx, descriptor, locked, operation)
		if err != nil {
			if errors.Is(err, ErrActionOperationUnavailable) {
				return err
			}
			return ErrActionOperationHidden
		}
		// Terminal operations are already bound to this exact actor, session,
		// action and verification. The action may have changed its target state,
		// so reauthorizing that target would hide a committed-but-unknown result.
		if operation.State == models.OperationProcessing || operation.State == models.OperationPendingRecovery {
			if err := authorizeOperationDescriptor(tx, locked.actor, descriptor, targetGUID); err != nil {
				if errors.Is(err, ErrActionOperationUnavailable) {
					return err
				}
				return ErrActionOperationHidden
			}
		}
		finalNow := s.clock.NowMillis()
		if finalNow < now || !validOperationNow(finalNow) {
			return ErrActionOperationUnavailable
		}
		if locked.session.ExpiresAt <= finalNow || !validOperationStateWithOptionalVerification(operation, verification, finalNow) {
			return ErrActionOperationHidden
		}
		if operation.State == models.OperationExpired {
			expiredOutcome = true
			return nil
		}
		if finalNow >= operation.QueryExpiresAt && operation.State.CanTransitionTo(models.OperationExpired) {
			if err := expireOperation(tx, &operation, locked.actor.ID, finalNow); err != nil {
				return ErrActionOperationUnavailable
			}
			expiredOutcome = true
			return nil
		}
		view = operationView(descriptor, operation, finalNow)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, mapOperationError(err)
	}
	if expiredOutcome {
		return nil, ErrActionOperationExpired
	}
	if view == nil {
		return nil, ErrActionOperationUnavailable
	}
	return view, nil
}

func (s *ActionOperationService) MarkPendingRecovery(ctx context.Context, id int64) error {
	if s == nil || ctx == nil || id <= 0 {
		return ErrActionOperationUnavailable
	}
	now := s.clock.NowMillis()
	if !validOperationNow(now) {
		return ErrActionOperationUnavailable
	}
	expiredOutcome := false
	err := s.operationDB(ctx).Transaction(func(tx *gorm.DB) error {
		var operation models.AdminOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&operation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrActionOperationHidden
			}
			return ErrActionOperationUnavailable
		}
		if operation.State == models.OperationExpired || operation.IsDeleted == 1 {
			return ErrActionOperationExpired
		}
		lockedNow := s.clock.NowMillis()
		if lockedNow < now || !validOperationNow(lockedNow) {
			return ErrActionOperationUnavailable
		}
		if operation.State == models.OperationPendingRecovery {
			if lockedNow < operation.QueryExpiresAt {
				return ErrActionOperationConflict
			}
			if err := expireOperation(tx, &operation, 0, lockedNow); err != nil {
				return ErrActionOperationUnavailable
			}
			expiredOutcome = true
			return nil
		}
		if operation.State != models.OperationProcessing {
			return ErrActionOperationConflict
		}
		if operation.LeaseExpiresAt == nil ||
			*operation.LeaseExpiresAt > math.MaxInt64-actionOperationRecoveryGraceMS || lockedNow <= *operation.LeaseExpiresAt+actionOperationRecoveryGraceMS {
			return ErrActionOperationConflict
		}
		leaseCutoff := lockedNow - actionOperationRecoveryGraceMS
		result := tx.Model(&models.AdminOperation{}).
			Where("id = ? AND state = ? AND is_deleted = 0 AND lease_expires_at = ? AND lease_expires_at < ?", operation.ID, models.OperationProcessing, *operation.LeaseExpiresAt, leaseCutoff).
			Updates(map[string]any{"state": models.OperationPendingRecovery, "lease_owner_hmac": nil, "lease_expires_at": nil, "updated_at": lockedNow, "updated_by": nil})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrActionOperationUnavailable
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return mapOperationError(err)
	}
	if expiredOutcome {
		return ErrActionOperationExpired
	}
	return nil
}

func (s *ActionOperationService) reserveBegin(ctx context.Context, sessionSID string) error {
	if s == nil || s.limiter == nil || len(sessionSID) != 36 {
		return ErrActionSecurityRateInput
	}
	keys, err := s.limiter.rateKeys([]actionRateIdentity{{purpose: actionsecurity.RateBeginSession, payload: []byte(sessionSID)}})
	if err != nil {
		return err
	}
	return s.limiter.reserve(ctx, keys, []actionRateWindowLimit{{limit: 60, windowMS: 60_000}})
}

type operationLockedIdentity struct {
	actor   models.User
	session models.Session
}

func lockOperationActorSession(tx *gorm.DB, actor ActionActor, now int64) (operationLockedIdentity, error) {
	if tx == nil || !validOperationActorClaims(actor) || now <= 0 {
		return operationLockedIdentity{}, ErrActionOperationForbidden
	}
	var stored models.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "guid", "password_hash", "role", "status", "is_deleted", "auth_version").
		Where("id = ?", actor.UserID).First(&stored).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return operationLockedIdentity{}, ErrActionOperationForbidden
		}
		return operationLockedIdentity{}, ErrActionOperationUnavailable
	}
	if !validActionActor(stored) || stored.Guid != actor.UserGUID || stored.AuthVersion != actor.AuthVersion {
		return operationLockedIdentity{}, ErrActionOperationForbidden
	}
	var candidates []models.Session
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "guid", "sid", "user_id", "session_version", "is_deleted", "revoked_at", "expires_at").
		Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND expires_at > ?", stored.ID, now).
		Order("id ASC").Find(&candidates).Error; err != nil {
		return operationLockedIdentity{}, ErrActionOperationUnavailable
	}
	var session models.Session
	matches := 0
	for i := range candidates {
		if constantTimeSIDEqual(candidates[i].SID, actor.SessionSID) {
			session = candidates[i]
			matches++
		}
	}
	if matches != 1 || session.ID <= 0 || session.UserID != stored.ID || session.SessionVersion != actor.SessionVersion ||
		session.IsDeleted != 0 || session.RevokedAt != nil || session.ExpiresAt <= now {
		return operationLockedIdentity{}, ErrActionOperationForbidden
	}
	return operationLockedIdentity{actor: stored, session: session}, nil
}

func lockOperationVerificationByTicket(tx *gorm.DB, ticketHex string) (models.AdminActionVerification, error) {
	var verification models.AdminActionVerification
	if len(ticketHex) != 64 {
		return verification, ErrActionOperationForbidden
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("ticket_hmac = ?", ticketHex).First(&verification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.AdminActionVerification{}, ErrActionOperationForbidden
		}
		return models.AdminActionVerification{}, ErrActionOperationUnavailable
	}
	return verification, nil
}

func lockOperationVerificationByID(tx *gorm.DB, id int64) (models.AdminActionVerification, error) {
	var verification models.AdminActionVerification
	if id <= 0 {
		return verification, ErrActionOperationHidden
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&verification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.AdminActionVerification{}, ErrActionOperationHidden
		}
		return models.AdminActionVerification{}, ErrActionOperationUnavailable
	}
	return verification, nil
}

func parseOperationTicket(descriptor actionsecurity.Descriptor, values []string) ([32]byte, bool, error) {
	if !descriptor.RequiresTicket {
		if len(values) != 0 {
			return [32]byte{}, false, ErrActionOperationForbidden
		}
		return [32]byte{}, false, nil
	}
	raw, err := actionsecurity.ParseTicket(values)
	if err != nil {
		return [32]byte{}, false, err
	}
	return raw, true, nil
}

func lockOperationBeginVerification(tx *gorm.DB, descriptor actionsecurity.Descriptor, ticketHex string, locked operationLockedIdentity, requestHex string, targetGUID *int64, expectedID *int64, existing bool) (*models.AdminActionVerification, error) {
	if !descriptor.RequiresTicket {
		if ticketHex != "" || expectedID != nil {
			return nil, ErrActionOperationForbidden
		}
		return nil, nil
	}
	verification, err := lockOperationVerificationByTicket(tx, ticketHex)
	if err != nil {
		return nil, err
	}
	if (existing && (expectedID == nil || *expectedID != verification.ID)) || !validOperationVerificationBinding(verification, locked, descriptor, requestHex, targetGUID) {
		return nil, ErrActionOperationForbidden
	}
	return &verification, nil
}

func lockOperationQueryVerification(tx *gorm.DB, descriptor actionsecurity.Descriptor, locked operationLockedIdentity, operation models.AdminOperation) (*models.AdminActionVerification, *int64, error) {
	if !descriptor.RequiresTicket {
		if operation.VerificationID != nil || descriptor.TargetKind != actionsecurity.TargetNone {
			return nil, nil, ErrActionOperationHidden
		}
		return nil, nil, nil
	}
	if operation.VerificationID == nil {
		return nil, nil, ErrActionOperationHidden
	}
	verification, err := lockOperationVerificationByID(tx, *operation.VerificationID)
	if err != nil {
		return nil, nil, err
	}
	if !validOperationVerificationBinding(verification, locked, descriptor, operation.RequestHMAC, verification.TargetGUID) {
		return nil, nil, ErrActionOperationHidden
	}
	return &verification, verification.TargetGUID, nil
}

func validOperationVerificationBinding(verification models.AdminActionVerification, locked operationLockedIdentity, descriptor actionsecurity.Descriptor, requestHex string, targetGUID *int64) bool {
	return verification.ID > 0 && verification.ActorUserID == locked.actor.ID && verification.ActorAuthVersion == locked.actor.AuthVersion &&
		verification.SessionID == locked.session.ID && verification.Action == int(descriptor.Action) &&
		verification.TargetKind == int(descriptor.TargetKind) && sameOptionalInt64(verification.TargetGUID, targetGUID) &&
		constantTimeOperationStringEqual(verification.IntentHMAC, requestHex)
}

func validExistingBeginVerificationState(verification models.AdminActionVerification, operationState models.AdminOperationState, now int64) bool {
	relation := verificationRelationAt(verification, now)
	switch operationState {
	case models.OperationProcessing, models.OperationPendingRecovery:
		return relation == operationVerificationActive
	case models.OperationSucceeded, models.OperationFailed:
		return relation == operationVerificationConsumed
	case models.OperationExpired:
		return relation != operationVerificationInvalid
	default:
		return false
	}
}

type operationVerificationRelation uint8

const (
	operationVerificationInvalid operationVerificationRelation = iota
	operationVerificationActive
	operationVerificationExpiredUnconsumed
	operationVerificationConsumed
)

func verificationRelationAt(verification models.AdminActionVerification, now int64) operationVerificationRelation {
	if verification.ExpiresAt <= 0 || (verification.IsDeleted != 0 && verification.IsDeleted != 1) {
		return operationVerificationInvalid
	}
	if verification.ConsumedAt != nil {
		if *verification.ConsumedAt <= 0 || *verification.ConsumedAt > now || verification.IsDeleted != 1 {
			return operationVerificationInvalid
		}
		return operationVerificationConsumed
	}
	if verification.IsDeleted == 0 && verification.ExpiresAt > now {
		return operationVerificationActive
	}
	return operationVerificationExpiredUnconsumed
}

func validOperationVerificationState(operation models.AdminOperation, verification models.AdminActionVerification, now int64) bool {
	if (operation.State == models.OperationExpired && operation.IsDeleted != 1) ||
		(operation.State != models.OperationExpired && operation.IsDeleted != 0) {
		return false
	}
	relation := verificationRelationAt(verification, now)
	switch operation.State {
	case models.OperationProcessing, models.OperationPendingRecovery:
		return relation == operationVerificationActive || relation == operationVerificationExpiredUnconsumed
	case models.OperationSucceeded, models.OperationFailed:
		return relation == operationVerificationConsumed
	case models.OperationExpired:
		if operation.QueryExpiresAt > now {
			return false
		}
		return (relation == operationVerificationExpiredUnconsumed && verification.IsDeleted == 0) ||
			relation == operationVerificationConsumed
	default:
		return false
	}
}

func validOperationStateWithOptionalVerification(operation models.AdminOperation, verification *models.AdminActionVerification, now int64) bool {
	if verification != nil {
		return operation.VerificationID != nil && validOperationVerificationState(operation, *verification, now)
	}
	if operation.VerificationID != nil || (operation.State == models.OperationExpired && operation.IsDeleted != 1) ||
		(operation.State != models.OperationExpired && operation.IsDeleted != 0) {
		return false
	}
	if operation.State == models.OperationExpired {
		return operation.QueryExpiresAt <= now
	}
	return operation.State == models.OperationProcessing || operation.State == models.OperationPendingRecovery ||
		operation.State == models.OperationSucceeded || operation.State == models.OperationFailed
}

func authorizeOperationDescriptor(tx *gorm.DB, actor models.User, descriptor actionsecurity.Descriptor, targetGUID *int64) error {
	if descriptor.RootOnly && actor.Role != models.UserRoleRoot {
		return ErrActionOperationForbidden
	}
	var target models.User
	switch descriptor.TargetKind {
	case actionsecurity.TargetNone:
		if targetGUID != nil {
			return ErrActionOperationHidden
		}
	case actionsecurity.TargetUser:
		if targetGUID == nil || *targetGUID <= 0 {
			return ErrActionOperationHidden
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "guid", "role", "status", "is_deleted", "auth_version").
			Where("guid = ? AND is_deleted = 0", *targetGUID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrActionOperationHidden
			}
			return ErrActionOperationUnavailable
		}
	case actionsecurity.TargetPublicContent:
		return ErrActionOperationUnavailable
	default:
		return ErrActionOperationUnavailable
	}
	_, rules, err := readPermissionPolicyRows(tx, actor.ID)
	if err != nil {
		return ErrActionOperationUnavailable
	}
	evaluator, err := authz.NewEvaluator(actionAccount(actor), rules)
	if err != nil {
		return ErrActionOperationUnavailable
	}
	decision := operationDescriptorAuthorizationDecision(evaluator, descriptor, target)
	if decision == authz.Hidden {
		return ErrActionOperationHidden
	}
	if decision != authz.Allowed {
		return ErrActionOperationForbidden
	}
	return nil
}

func operationDescriptorAuthorizationDecision(evaluator *authz.Evaluator, descriptor actionsecurity.Descriptor, target models.User) authz.Decision {
	if evaluator == nil {
		return authz.Denied
	}
	createDescriptor := descriptor.Action == actionsecurity.ActionUsersCreate || descriptor.Action == actionsecurity.ActionUsersCreateAdmin ||
		descriptor.Name == "users.create" || descriptor.Name == "users.create_admin" || descriptor.Capability == "users.create"
	if createDescriptor {
		switch {
		case descriptor.Action == actionsecurity.ActionUsersCreate && descriptor.Name == "users.create" &&
			descriptor.Capability == "users.create" && descriptor.TargetKind == actionsecurity.TargetNone && !descriptor.RootOnly && !descriptor.RequiresTicket:
			return evaluator.Create(models.UserRoleUser)
		case descriptor.Action == actionsecurity.ActionUsersCreateAdmin && descriptor.Name == "users.create_admin" &&
			descriptor.Capability == "users.create" && descriptor.TargetKind == actionsecurity.TargetNone && descriptor.RootOnly && descriptor.RequiresTicket:
			return evaluator.Create(models.UserRoleAdmin)
		default:
			return authz.Denied
		}
	}
	switch descriptor.TargetKind {
	case actionsecurity.TargetNone:
		return evaluator.Resource(descriptor.Capability)
	case actionsecurity.TargetUser:
		return evaluator.User(descriptor.Capability, actionAccount(target))
	default:
		return authz.Denied
	}
}

func operationIntentTargetGUID(descriptor actionsecurity.Descriptor, intent any) (*int64, error) {
	if descriptor.TargetKind == actionsecurity.TargetNone {
		return nil, nil
	}
	var guid int64
	switch value := intent.(type) {
	case actionsecurity.ResetPasswordIntent:
		guid = value.TargetGUID
	case actionsecurity.PromoteIntent:
		guid = value.TargetGUID
	case actionsecurity.DemoteIntent:
		guid = value.TargetGUID
	case actionsecurity.PermissionsWriteIntent:
		guid = value.TargetGUID
	case actionsecurity.DeleteUserIntent:
		guid = value.TargetGUID
	case actionsecurity.PublishIntent:
		guid = value.VersionGUID
	case actionsecurity.RollbackIntent:
		guid = value.VersionGUID
	default:
		return nil, ErrActionOperationForbidden
	}
	if guid <= 0 {
		return nil, ErrActionOperationForbidden
	}
	return &guid, nil
}

func sameOptionalInt64(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func expireOperation(tx *gorm.DB, operation *models.AdminOperation, actorID, now int64) error {
	if operation == nil || !operation.State.CanTransitionTo(models.OperationExpired) {
		return ErrActionOperationUnavailable
	}
	var updatedBy any
	if actorID > 0 {
		updatedBy = actorID
	}
	result := tx.Model(&models.AdminOperation{}).
		Where("id = ? AND state = ? AND is_deleted = 0 AND query_expires_at = ? AND query_expires_at <= ?", operation.ID, operation.State, operation.QueryExpiresAt, now).
		Updates(map[string]any{
			"state": models.OperationExpired, "is_deleted": 1, "lease_owner_hmac": nil, "lease_expires_at": nil,
			"error_code": nil, "result_kind": nil, "result_guid": nil, "result_auth_version": nil,
			"result_permissions_version": nil, "result_role": nil, "result_http_status": nil,
			"updated_at": now, "updated_by": updatedBy,
		})
	if result.Error != nil || result.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	operation.State = models.OperationExpired
	operation.IsDeleted = 1
	return nil
}

func operationView(descriptor actionsecurity.Descriptor, operation models.AdminOperation, now int64) *OperationView {
	view := &OperationView{PublicRef: operation.PublicRef, Scope: descriptor.Name, Status: operation.State.String(), FinishedAt: copyInt64(operation.FinishedAt)}
	if operation.ErrorCode != nil {
		code := operation.ErrorCode.String()
		view.FailureCode = &code
	}
	if operation.State == models.OperationSucceeded && (descriptor.Action == actionsecurity.ActionUsersResetPassword || isA08RolePermissionAction(descriptor.Action)) {
		view.TargetGUID = copyInt64(operation.ResultGUID)
		if operation.ResultAuthVersion != nil {
			value := *operation.ResultAuthVersion
			view.ResultAuthVersion = &value
		}
		if isA08RolePermissionAction(descriptor.Action) {
			view.ResultPermissionsVersion = copyInt64(operation.ResultPermissionsVersion)
			view.ResultRole = copyUserRole(operation.ResultRole)
		}
	}
	if operation.State == models.OperationProcessing {
		seconds := 1
		if operation.LeaseExpiresAt != nil && *operation.LeaseExpiresAt > now {
			seconds = int((*operation.LeaseExpiresAt - now + 999) / 1000)
		}
		if seconds < 1 {
			seconds = 1
		}
		if seconds > 30 {
			seconds = 30
		}
		view.RetryAfterSeconds = seconds
	}
	return view
}

func validOperationDescriptor(descriptor actionsecurity.Descriptor, action actionsecurity.Action, ok bool) bool {
	return ok && descriptor.Active && descriptor.Action == action && descriptor.Name != "" && descriptor.Capability != "" && descriptor.Encode != nil
}

func validOperationActorClaims(actor ActionActor) bool {
	return actor.UserID > 0 && actor.UserGUID > 0 && actor.AuthVersion > 0 && len(actor.SessionSID) == 36 && actor.SessionVersion > 0
}

func validOperationNow(now int64) bool {
	return now > 0 && now <= math.MaxInt64-actionOperationQueryRetentionMS
}

func validOperationState(state models.AdminOperationState) bool {
	return state == models.OperationProcessing || state == models.OperationSucceeded || state == models.OperationFailed ||
		state == models.OperationPendingRecovery || state == models.OperationExpired
}

func constantTimeOperationStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func (s *ActionOperationService) operationDB(ctx context.Context) *gorm.DB {
	return s.db.Session(&gorm.Session{NewDB: true, Logger: logger.Discard}).WithContext(ctx)
}

func mapOperationError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrActionOperationInactive, ErrActionOperationForbidden, ErrActionOperationHidden, ErrActionOperationConflict, ErrActionOperationCrossSession, ErrActionOperationExpired, ErrActionOperationUnavailable} {
		if errors.Is(err, known) {
			return known
		}
	}
	var retry *RetryAfterError
	if errors.As(err, &retry) {
		return err
	}
	return ErrActionOperationUnavailable
}

func operationInterfaceNil(value any) bool {
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
