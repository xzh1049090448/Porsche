package service

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type VerificationConsume struct {
	Actor        ActionActor
	Action       actionsecurity.Action
	TargetGUID   *int64
	Intent       any
	TicketValues []string
}

// Consume verifies and atomically tombstones one action ticket in a standalone
// transaction. Business mutations should use ConsumeInTx instead.
func (s *ActionVerificationService) Consume(ctx context.Context, in VerificationConsume) error {
	if s == nil || s.db == nil || ctx == nil {
		return ErrActionVerificationUnavailable
	}
	err := s.db.Session(&gorm.Session{NewDB: true, Logger: logger.Discard}).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.ConsumeInTx(ctx, tx, in)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return normalizeVerificationConsumeError(err)
}

// ConsumeInTx verifies and tombstones a ticket in the caller's business
// transaction, so rollback keeps the ticket reusable.
func (s *ActionVerificationService) ConsumeInTx(ctx context.Context, tx *gorm.DB, in VerificationConsume) error {
	return s.VerifyAndConsumeInTx(ctx, tx, in)
}

// VerifyAndConsumeInTx is the explicit transaction-aware verification seam.
func (s *ActionVerificationService) VerifyAndConsumeInTx(ctx context.Context, tx *gorm.DB, in VerificationConsume) error {
	if s == nil || ctx == nil || s.resolve == nil || s.crypto == nil || s.clock == nil {
		return ErrActionVerificationUnavailable
	}
	if tx == nil {
		return ErrActionVerificationUnavailable
	}
	descriptor, ok := s.resolve(in.Action)
	if !ok || !descriptor.Active || !descriptor.RequiresTicket || descriptor.Action != in.Action || descriptor.Encode == nil {
		return ErrActionVerificationInactive
	}
	if err := validatePublicVerificationBinding(in.Action, in.Intent, in.TargetGUID); err != nil {
		return err
	}
	ticketRaw, err := parseVerificationConsumeTicket(in.TicketValues)
	if err != nil {
		return ErrActionVerificationForbidden
	}
	defer clear(ticketRaw[:])
	encoded, err := descriptor.Encode(in.Intent)
	if err != nil {
		return ErrActionVerificationConflict
	}
	intentDigest := s.crypto.IntentDigest(encoded)
	clear(encoded)
	defer clear(intentDigest[:])
	ticketDigest := s.crypto.TicketDigest(ticketRaw)
	defer clear(ticketDigest[:])
	intentHex := hex.EncodeToString(intentDigest[:])
	ticketHex := hex.EncodeToString(ticketDigest[:])

	revoked, err := s.authRedis.IsSessionRevoked(ctx, in.Actor.SessionSID)
	if err != nil {
		return ErrActionVerificationUnavailable
	}
	if revoked {
		return ErrActionVerificationForbidden
	}
	now := s.clock.NowMillis()
	if now <= 0 {
		return ErrActionVerificationUnavailable
	}
	locked, err := lockActionIdentity(tx, in.Actor, descriptor, in.TargetGUID, now)
	if err != nil {
		return err
	}
	var verification models.AdminActionVerification
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("ticket_hmac = ?", ticketHex).First(&verification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrActionVerificationForbidden
		}
		return ErrActionVerificationUnavailable
	}
	if !validVerificationConsumeBinding(verification, locked.actor.ID, locked.actor.AuthVersion, locked.session.ID, descriptor, in.TargetGUID, intentHex, ticketHex, now) {
		return ErrActionVerificationForbidden
	}
	actorID := locked.actor.ID
	result := tx.Model(&models.AdminActionVerification{}).
		Where("id = ? AND consumed_at IS NULL AND is_deleted = 0 AND expires_at > ?", verification.ID, now).
		Updates(map[string]any{"consumed_at": now, "is_deleted": 1, "updated_at": now, "updated_by": actorID})
	if result.Error != nil {
		return ErrActionVerificationUnavailable
	}
	if result.RowsAffected != 1 {
		return ErrActionVerificationForbidden
	}
	return nil
}

func normalizeVerificationConsumeError(err error) error {
	if errors.Is(err, ErrActionVerificationInactive) || errors.Is(err, ErrActionVerificationForbidden) || errors.Is(err, ErrActionVerificationHidden) || errors.Is(err, ErrActionVerificationConflict) {
		return err
	}
	if err != nil {
		return ErrActionVerificationUnavailable
	}
	return nil
}

func validVerificationConsumeBinding(verification models.AdminActionVerification, actorID int64, authVersion int, sessionID int64, descriptor actionsecurity.Descriptor, targetGUID *int64, intentHex, ticketHex string, now int64) bool {
	return verification.ActorUserID == actorID && verification.ActorAuthVersion == authVersion && verification.SessionID == sessionID &&
		verification.Action == int(descriptor.Action) && verification.TargetKind == int(descriptor.TargetKind) &&
		sameOptionalGUID(verification.TargetGUID, targetGUID) && verification.ConsumedAt == nil && verification.IsDeleted == 0 && verification.ExpiresAt > now &&
		subtle.ConstantTimeCompare([]byte(verification.IntentHMAC), []byte(intentHex)) == 1 &&
		subtle.ConstantTimeCompare([]byte(verification.TicketHMAC), []byte(ticketHex)) == 1
}

func parseVerificationConsumeTicket(values []string) ([32]byte, error) {
	return actionsecurity.ParseTicket(values)
}

func validatePublicVerificationBinding(action actionsecurity.Action, intent any, target *int64) error {
	var expected *int64
	switch action {
	case actionsecurity.ActionPublicModelDelete:
		value, ok := intent.(actionsecurity.PublicModelDeleteIntent)
		if !ok {
			return ErrActionVerificationConflict
		}
		expected = &value.ModelGUID
	case actionsecurity.ActionPublicPricingPublish:
		if _, ok := intent.(actionsecurity.PublicPricingPublishIntent); !ok {
			return ErrActionVerificationConflict
		}
	case actionsecurity.ActionPublicPricingRestore:
		value, ok := intent.(actionsecurity.PublicPricingRestoreIntent)
		if !ok {
			return ErrActionVerificationConflict
		}
		expected = &value.ReleaseGUID
	case actionsecurity.ActionPublicContentPublish:
		value, ok := intent.(actionsecurity.PublicContentPublishIntent)
		if !ok {
			return ErrActionVerificationConflict
		}
		expected = &value.PriceReleaseGUID
	case actionsecurity.ActionPublicContentRestore:
		value, ok := intent.(actionsecurity.PublicContentRestoreIntent)
		if !ok {
			return ErrActionVerificationConflict
		}
		expected = &value.ReleaseGUID
	default:
		return ErrActionVerificationConflict
	}
	if !sameOptionalGUID(expected, target) {
		return ErrActionVerificationConflict
	}
	return nil
}

func sameOptionalGUID(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left > 0 && *left == *right
}
