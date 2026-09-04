package service

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"reflect"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
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
	ID         int64
	PublicRef  string
	LeaseOwner [32]byte
}

type OperationView struct {
	PublicRef         string
	Scope             string
	Status            string
	FinishedAt        *int64
	FailureCode       *string
	RetryAfterSeconds int
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
	ticketRaw, err := actionsecurity.ParseTicket(in.TicketValues)
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
	keyDigest := s.crypto.IdempotencyDigest(keyRaw)
	ticketDigest := s.crypto.TicketDigest(ticketRaw)
	clear(keyRaw[:])
	clear(ticketRaw[:])
	requestHex := hex.EncodeToString(requestDigest[:])
	keyHex := hex.EncodeToString(keyDigest[:])
	ticketHex := hex.EncodeToString(ticketDigest[:])
	clear(requestDigest[:])
	clear(keyDigest[:])
	clear(ticketDigest[:])

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
	err = s.operationDB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, lockErr := lockOperationActorSession(tx, in.Actor, now)
		if lockErr != nil {
			return lockErr
		}
		lockedNow := s.clock.NowMillis()
		if lockedNow < now || !validOperationNow(lockedNow) {
			return ErrActionOperationUnavailable
		}
		if locked.session.ExpiresAt <= lockedNow {
			return ErrActionOperationForbidden
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
			if existing.State == models.OperationExpired || existing.IsDeleted == 1 || lockedNow >= existing.QueryExpiresAt {
				if existing.State != models.OperationExpired || existing.IsDeleted != 1 {
					if err := expireOperation(tx, &existing, locked.actor.ID, lockedNow); err != nil {
						return ErrActionOperationUnavailable
					}
				}
				return ErrActionOperationExpired
			}
			identity = &OperationIdentity{ID: existing.ID, PublicRef: existing.PublicRef}
			view = operationView(descriptor, existing, lockedNow)
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
		leaseExpires := lockedNow + actionOperationLeaseMillis
		queryExpires := lockedNow + actionOperationQueryRetentionMS
		operation := models.AdminOperation{
			AuditFields: models.AuditFields{Guid: guid, CreatedAt: lockedNow, CreatedBy: &actorID, UpdatedAt: lockedNow, UpdatedBy: &actorID},
			ActorUserID: actorID, ActorAuthVersion: locked.actor.AuthVersion, SessionID: locked.session.ID,
			Action: int(descriptor.Action), IdempotencyKeyHMAC: keyHex, RequestHMAC: requestHex,
			State: models.OperationProcessing, PublicRef: publicRef, LeaseOwnerHMAC: &leaseHex,
			LeaseExpiresAt: &leaseExpires, QueryExpiresAt: queryExpires,
		}
		if err := tx.Create(&operation).Error; err != nil {
			clear(leaseOwner[:])
			return ErrActionOperationUnavailable
		}

		var verification models.AdminActionVerification
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("ticket_hmac = ?", ticketHex).First(&verification).Error; err != nil {
			clear(leaseOwner[:])
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrActionOperationForbidden
			}
			return ErrActionOperationUnavailable
		}
		if verification.ActorUserID != locked.actor.ID || verification.ActorAuthVersion != locked.actor.AuthVersion ||
			verification.SessionID != locked.session.ID || verification.Action != int(descriptor.Action) ||
			verification.TargetKind != int(descriptor.TargetKind) || !constantTimeOperationStringEqual(verification.IntentHMAC, requestHex) ||
			verification.ConsumedAt != nil || verification.IsDeleted != 0 || verification.ExpiresAt <= lockedNow {
			clear(leaseOwner[:])
			return ErrActionOperationForbidden
		}
		result := tx.Model(&models.AdminOperation{}).Where("id = ? AND verification_id IS NULL", operation.ID).
			Updates(map[string]any{"verification_id": verification.ID, "updated_at": lockedNow, "updated_by": actorID})
		if result.Error != nil {
			clear(leaseOwner[:])
			var mysqlErr *mysqlDriver.MySQLError
			if errors.As(result.Error, &mysqlErr) && mysqlErr.Number == 1062 {
				return ErrActionOperationForbidden
			}
			return ErrActionOperationUnavailable
		}
		if result.RowsAffected != 1 {
			clear(leaseOwner[:])
			return ErrActionOperationUnavailable
		}
		operation.VerificationID = &verification.ID
		identity = &OperationIdentity{ID: operation.ID, PublicRef: publicRef, LeaseOwner: leaseOwner}
		view = operationView(descriptor, operation, lockedNow)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, nil, mapOperationError(err)
	}
	if identity == nil || view == nil {
		return nil, nil, ErrActionOperationUnavailable
	}
	return identity, view, nil
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
	err = s.operationDB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockOperationActorSession(tx, actor, now)
		if err != nil {
			if errors.Is(err, ErrActionOperationUnavailable) {
				return err
			}
			return ErrActionOperationHidden
		}
		lockedNow := s.clock.NowMillis()
		if lockedNow < now || !validOperationNow(lockedNow) {
			return ErrActionOperationUnavailable
		}
		if locked.session.ExpiresAt <= lockedNow {
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
		if operation.State == models.OperationExpired || operation.IsDeleted == 1 {
			return ErrActionOperationExpired
		}
		if lockedNow >= operation.QueryExpiresAt {
			if err := expireOperation(tx, &operation, locked.actor.ID, lockedNow); err != nil {
				return ErrActionOperationUnavailable
			}
			return ErrActionOperationExpired
		}
		view = operationView(descriptor, operation, lockedNow)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, mapOperationError(err)
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
		if now >= operation.QueryExpiresAt {
			if err := expireOperation(tx, &operation, operation.ActorUserID, now); err != nil {
				return ErrActionOperationUnavailable
			}
			return ErrActionOperationExpired
		}
		if operation.State != models.OperationProcessing || operation.LeaseExpiresAt == nil ||
			*operation.LeaseExpiresAt > math.MaxInt64-actionOperationRecoveryGraceMS || now <= *operation.LeaseExpiresAt+actionOperationRecoveryGraceMS {
			return ErrActionOperationConflict
		}
		actorID := operation.ActorUserID
		result := tx.Model(&models.AdminOperation{}).
			Where("id = ? AND state = ? AND is_deleted = 0 AND lease_expires_at = ?", operation.ID, models.OperationProcessing, *operation.LeaseExpiresAt).
			Updates(map[string]any{"state": models.OperationPendingRecovery, "lease_owner_hmac": nil, "lease_expires_at": nil, "updated_at": now, "updated_by": actorID})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrActionOperationUnavailable
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return mapOperationError(err)
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

func expireOperation(tx *gorm.DB, operation *models.AdminOperation, actorID, now int64) error {
	result := tx.Model(&models.AdminOperation{}).Where("id = ? AND is_deleted = 0", operation.ID).Updates(map[string]any{
		"state": models.OperationExpired, "is_deleted": 1, "lease_owner_hmac": nil, "lease_expires_at": nil,
		"error_code": nil, "result_kind": nil, "result_guid": nil, "result_http_status": nil,
		"updated_at": now, "updated_by": actorID,
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
	return ok && descriptor.Active && descriptor.RequiresTicket && descriptor.Action == action && descriptor.Name != "" && descriptor.Capability != "" && descriptor.Encode != nil
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
