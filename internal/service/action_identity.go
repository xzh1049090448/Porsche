package service

import (
	"crypto/subtle"
	"errors"
	"math"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ActionActor is a trusted copy of the authenticated JWT/session claims. Every
// field is checked again against rows locked by the action transaction.
type ActionActor struct {
	UserID         int64
	UserGUID       int64
	AuthVersion    int
	SessionSID     string
	SessionVersion int
}

type lockedActionIdentity struct {
	actor   models.User
	session models.Session
	target  *models.User
}

// lockActionIdentity owns the common actor -> session -> target -> policy lock
// order. The Redis limiter and revocation barrier must already have succeeded.
func lockActionIdentity(tx *gorm.DB, actor ActionActor, descriptor actionsecurity.Descriptor, targetGUID *int64, intent any, lockPublicTarget bool, now int64) (lockedActionIdentity, error) {
	if tx == nil || actor.UserID <= 0 || actor.UserGUID <= 0 || actor.AuthVersion <= 0 ||
		len(actor.SessionSID) != 36 || actor.SessionVersion <= 0 || now <= 0 {
		return lockedActionIdentity{}, ErrActionVerificationUnavailable
	}

	fields := []string{"id", "guid", "password_hash", "role", "status", "is_deleted", "auth_version"}
	var storedActor models.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(fields).Where("id = ?", actor.UserID).First(&storedActor).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return lockedActionIdentity{}, ErrActionVerificationForbidden
		}
		return lockedActionIdentity{}, ErrActionVerificationUnavailable
	}
	if !validActionActor(storedActor) || storedActor.Guid != actor.UserGUID ||
		storedActor.AuthVersion != actor.AuthVersion {
		return lockedActionIdentity{}, ErrActionVerificationForbidden
	}

	// SID is a secret selector and must never be a SQL argument or query log.
	// Lock the actor's full active candidate set by non-secret user ID, then
	// select the exact SID in memory with constant-time comparisons. The session
	// writer's configured limit remains the single source of lifecycle policy.
	var candidates []models.Session
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "guid", "sid", "user_id", "session_version", "is_deleted", "revoked_at", "expires_at").
		Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND expires_at > ?", storedActor.ID, now).
		Order("id ASC").Find(&candidates).Error; err != nil {
		return lockedActionIdentity{}, ErrActionVerificationUnavailable
	}
	var session models.Session
	matches := 0
	for i := range candidates {
		if constantTimeSIDEqual(candidates[i].SID, actor.SessionSID) {
			session = candidates[i]
			matches++
		}
	}
	if matches != 1 {
		return lockedActionIdentity{}, ErrActionVerificationForbidden
	}
	if session.ID <= 0 || session.Guid <= 0 || session.UserID != storedActor.ID ||
		session.SessionVersion != actor.SessionVersion ||
		session.IsDeleted != 0 || session.RevokedAt != nil || session.ExpiresAt <= now {
		return lockedActionIdentity{}, ErrActionVerificationForbidden
	}

	var target models.User
	switch descriptor.TargetKind {
	case actionsecurity.TargetNone:
		if targetGUID != nil {
			return lockedActionIdentity{}, ErrActionVerificationHidden
		}
	case actionsecurity.TargetUser:
		if targetGUID == nil || *targetGUID <= 0 {
			return lockedActionIdentity{}, ErrActionVerificationHidden
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "role", "status", "is_deleted", "auth_version").
			Where("guid = ? AND is_deleted = 0", *targetGUID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return lockedActionIdentity{}, ErrActionVerificationHidden
			}
			return lockedActionIdentity{}, ErrActionVerificationUnavailable
		}
	case actionsecurity.TargetPublicContent:
		if lockPublicTarget {
			if err := lockPublicActionTarget(tx, storedActor.ID, descriptor.Action, targetGUID, intent); err != nil {
				return lockedActionIdentity{}, err
			}
		}
	default:
		if targetGUID == nil || *targetGUID <= 0 {
			return lockedActionIdentity{}, ErrActionVerificationHidden
		}
	}

	_, rules, err := readPermissionPolicyRows(tx, storedActor.ID)
	if err != nil {
		return lockedActionIdentity{}, ErrActionVerificationUnavailable
	}
	evaluator, err := authz.NewEvaluator(actionAccount(storedActor), rules)
	if err != nil {
		return lockedActionIdentity{}, ErrActionVerificationUnavailable
	}
	if descriptor.RootOnly && storedActor.Role != models.UserRoleRoot {
		return lockedActionIdentity{}, ErrActionVerificationForbidden
	}

	var decision authz.Decision
	switch descriptor.TargetKind {
	case actionsecurity.TargetNone:
		if descriptor.Capability == "users.create" {
			decision = evaluator.Create(models.UserRoleAdmin)
		} else {
			decision = evaluator.Resource(descriptor.Capability)
		}
	case actionsecurity.TargetUser:
		if isA08RolePermissionAction(descriptor.Action) {
			decision = rolePermissionPreauthorizationDecision(evaluator, descriptor, target)
		} else {
			decision = evaluator.User(descriptor.Capability, actionAccount(target))
		}
	case actionsecurity.TargetPublicContent:
		decision = evaluator.Resource(descriptor.Capability)
	default:
		return lockedActionIdentity{}, ErrActionVerificationUnavailable
	}
	if decision == authz.Hidden {
		return lockedActionIdentity{}, ErrActionVerificationHidden
	}
	if decision != authz.Allowed {
		return lockedActionIdentity{}, ErrActionVerificationForbidden
	}
	var lockedTarget *models.User
	if descriptor.TargetKind == actionsecurity.TargetUser {
		targetCopy := target
		lockedTarget = &targetCopy
	}
	return lockedActionIdentity{actor: storedActor, session: session, target: lockedTarget}, nil
}

// lockPublicActionTarget locks the revisioned aggregate and exact public target
// in the same order as the corresponding business transaction. Issue and
// consume both call this function, so a ticket cannot cross targets or survive
// a draft revision change between password verification and execution.
func lockPublicActionTarget(tx *gorm.DB, actorID int64, action actionsecurity.Action, targetGUID *int64, intent any) error {
	if tx == nil || actorID <= 0 {
		return ErrActionVerificationUnavailable
	}
	if err := validatePublicVerificationBinding(action, intent, targetGUID); err != nil {
		return err
	}

	switch action {
	case actionsecurity.ActionPublicModelDelete:
		value := intent.(actionsecurity.PublicModelDeleteIntent)
		if _, err := lockPublicPriceDraftRevision(tx); err != nil {
			return err
		}
		var model models.PublicModelConfig
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "guid", "revision").
			Where("guid = ? AND is_deleted = 0", value.ModelGUID).
			First(&model).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrActionVerificationHidden
			}
			return ErrActionVerificationUnavailable
		}
		if model.Revision != value.ExpectedRevision {
			return ErrActionVerificationConflict
		}
		return nil

	case actionsecurity.ActionPublicPricingRestore:
		value := intent.(actionsecurity.PublicPricingRestoreIntent)
		draft, err := lockPublicPriceDraftRevision(tx)
		if err != nil {
			return err
		}
		if _, err := lockPublicActionPublicationState(tx); err != nil {
			return err
		}
		var snapshot models.PublicPriceSnapshot
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "guid").
			Where("guid = ? AND is_deleted = 0", value.ReleaseGUID).
			First(&snapshot).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrActionVerificationHidden
			}
			return ErrActionVerificationUnavailable
		}
		if draft.Revision != value.ExpectedRevision {
			committed, err := publicPricingRestoreAlreadyCommitted(tx, actorID, snapshot.ID, value.ExpectedRevision)
			if err != nil {
				return err
			}
			if !committed {
				return ErrActionVerificationConflict
			}
		}
		return nil

	case actionsecurity.ActionPublicContentPublish:
		value := intent.(actionsecurity.PublicContentPublishIntent)
		draft, err := lockPublicContentDraftRevision(tx)
		if err != nil {
			return err
		}
		if draft.Revision != value.ExpectedRevision {
			return ErrActionVerificationConflict
		}
		state, err := lockPublicActionPublicationState(tx)
		if err != nil {
			return err
		}
		if state.PriceSnapshotID == nil {
			return ErrActionVerificationUnavailable
		}
		var current models.PublicPriceSnapshot
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "guid").
			Where("id = ? AND is_deleted = 0", *state.PriceSnapshotID).
			First(&current).Error; err != nil {
			return ErrActionVerificationUnavailable
		}
		if current.Guid == value.PriceReleaseGUID {
			return nil
		}
		var requested models.PublicPriceSnapshot
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "guid").
			Where("guid = ? AND is_deleted = 0", value.PriceReleaseGUID).
			First(&requested).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrActionVerificationHidden
			}
			return ErrActionVerificationUnavailable
		}
		return ErrActionVerificationConflict

	case actionsecurity.ActionPublicContentRestore:
		value := intent.(actionsecurity.PublicContentRestoreIntent)
		draft, err := lockPublicContentDraftRevision(tx)
		if err != nil {
			return err
		}
		var release models.PublicContentRelease
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "guid").
			Where("guid = ? AND document_kind = ? AND is_deleted = 0", value.ReleaseGUID, models.PublicContentDocumentSite).
			First(&release).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrActionVerificationHidden
			}
			return ErrActionVerificationUnavailable
		}
		if draft.Revision != value.ExpectedRevision {
			committed, err := publicContentRestoreAlreadyCommitted(tx, actorID, release.ID, value.ExpectedRevision)
			if err != nil {
				return err
			}
			if !committed {
				return ErrActionVerificationConflict
			}
		}
		return nil

	default:
		return ErrActionVerificationUnavailable
	}
}

func lockPublicPriceDraftRevision(tx *gorm.DB) (*models.PublicPriceDraftState, error) {
	var draft models.PublicPriceDraftState
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "revision").
		Where("state_key = ? AND is_deleted = 0", "pricing").
		First(&draft).Error; err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	return &draft, nil
}

func lockPublicContentDraftRevision(tx *gorm.DB) (*models.PublicContentDraft, error) {
	var draft models.PublicContentDraft
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "revision").
		Where("document_kind = ? AND is_deleted = 0", models.PublicContentDocumentSite).
		First(&draft).Error; err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	return &draft, nil
}

func lockPublicActionPublicationState(tx *gorm.DB) (*models.PublicPublicationState, error) {
	var state models.PublicPublicationState
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "price_snapshot_id").
		Where("state_key = ? AND is_deleted = 0", publicPublicationStateKey).
		First(&state).Error; err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	return &state, nil
}

func publicPricingRestoreAlreadyCommitted(tx *gorm.DB, actorID, sourceID, expectedRevision int64) (bool, error) {
	if expectedRevision <= 0 || expectedRevision == math.MaxInt64 {
		return false, nil
	}
	var restored models.PublicPriceSnapshot
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("restored_from_snapshot_id = ? AND source_revision = ? AND created_by = ? AND is_deleted = 0", sourceID, expectedRevision+1, actorID).
		First(&restored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, ErrActionVerificationUnavailable
	}
	return true, nil
}

func publicContentRestoreAlreadyCommitted(tx *gorm.DB, actorID, sourceID, expectedRevision int64) (bool, error) {
	if expectedRevision <= 0 || expectedRevision == math.MaxInt64 {
		return false, nil
	}
	var restored models.PublicContentRelease
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("restored_from_release_id = ? AND source_revision = ? AND created_by = ? AND document_kind = ? AND is_deleted = 0", sourceID, expectedRevision+1, actorID, models.PublicContentDocumentSite).
		First(&restored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, ErrActionVerificationUnavailable
	}
	return true, nil
}

func constantTimeSIDEqual(stored, claimed string) bool {
	// user_sessions.sid is VARCHAR(36). Fixed buffers plus a constant-time
	// length equality avoid data-dependent comparison of the secret selector.
	var left, right [36]byte
	copy(left[:], stored)
	copy(right[:], claimed)
	content := subtle.ConstantTimeCompare(left[:], right[:])
	length := subtle.ConstantTimeEq(int32(len(stored)), int32(len(claimed)))
	clear(left[:])
	clear(right[:])
	return content&length == 1
}

func validActionActor(user models.User) bool {
	return user.ID > 0 && user.Guid > 0 && user.AuthVersion > 0 && user.IsDeleted == 0 &&
		user.Status == models.UserStatusActive &&
		(user.Role == models.UserRoleAdmin || user.Role == models.UserRoleRoot) &&
		user.PasswordHash != nil && *user.PasswordHash != ""
}

func actionAccount(user models.User) authz.Account {
	return authz.Account{ID: user.ID, GUID: user.Guid, Role: user.Role, Status: user.Status, IsDeleted: user.IsDeleted}
}
