package service

import (
	"context"
	"errors"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ChangePassword verifies the existing password, invalidates every existing
// session, and advances auth_version in one MySQL transaction. Redis denial
// barriers are written before the durable transition so a cache failure never
// leaves a successful password change with live sessions.
func (a *AuthService) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) error {
	if err := a.requireAuthRedis(ctx); err != nil {
		return err
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := security.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND is_deleted = 0", userID).First(&user).Error; err != nil {
			return errUnauthorized("原密码错误")
		}
		if user.PasswordHash == nil || !security.VerifyPassword(oldPassword, *user.PasswordHash) {
			return errUnauthorized("原密码错误")
		}
		if err := a.revokeUserSessionsLocked(ctx, tx, &user, userID, models.AuthAuditEventSessionRevoked); err != nil {
			return err
		}
		nextVersion := user.AuthVersion + 1
		TouchAudit(&user.AuditFields, userID)
		if err := tx.Model(&models.User{}).Where("id = ? AND is_deleted = 0", user.ID).Updates(map[string]any{
			"password_hash": hash, "auth_version": nextVersion, "updated_at": user.UpdatedAt, "updated_by": user.UpdatedBy,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&models.AuthAuditEvent{AuditFields: auditFields(&userID), UserID: &userID, EventType: models.AuthAuditEventPasswordChanged}).Error
	})
}

// DisableUser disables a strictly lower-role account and revokes all of its
// sessions atomically with the auth-version change.
func (a *AuthService) DisableUser(ctx context.Context, actorID, targetID int64) error {
	return a.mutateManagedUser(ctx, actorID, targetID, false)
}

// SoftDeleteUser creates the required tombstone without physically deleting a
// user. Username remains occupied while credential and personal data are
// cleared, and all sessions are logically revoked.
func (a *AuthService) SoftDeleteUser(ctx context.Context, actorID, targetID int64) error {
	return a.mutateManagedUser(ctx, actorID, targetID, true)
}

// ManagedUserUpdateInput contains the mutable administrator-managed account
// fields. The handler converts external strings into the stable model enums
// before calling the service, so this write path never persists raw input.
type ManagedUserUpdateInput struct {
	Status         *models.UserStatus
	PlanType       *models.PlanType
	AllowedModels  *models.JSONSlice
	DailyCallLimit *int
}

// UpdateManagedUser locks and re-authorizes both the acting administrator and
// target account in one transaction before changing plan, model ACL, quota, or
// status. This closes the interval between request authentication and the
// durable write when the actor may have been disabled or demoted.
func (a *AuthService) UpdateManagedUser(ctx context.Context, actorID, targetGUID int64, input ManagedUserUpdateInput) (*models.User, error) {
	if a == nil || a.db == nil {
		return nil, errors.New("authentication service is unavailable")
	}
	var updated models.User
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var actor, target models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND is_deleted = 0", actorID).First(&actor).Error; err != nil {
			return errForbidden("无权限管理该用户")
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guid = ? AND is_deleted = 0", targetGUID).First(&target).Error; err != nil {
			return errNotFound("用户不存在")
		}
		if err := CanManageUser(&actor, &target); err != nil {
			return err
		}

		if input.Status != nil && *input.Status == models.UserStatusDisabled {
			if err := a.requireAuthRedis(ctx); err != nil {
				return err
			}
			if err := a.revokeUserSessionsLocked(ctx, tx, &target, actor.ID, models.AuthAuditEventUserDisabled); err != nil {
				return err
			}
			target.AuthVersion++
		}
		if input.Status != nil {
			target.Status = *input.Status
		}
		if input.PlanType != nil {
			target.PlanType = *input.PlanType
		}
		if input.AllowedModels != nil {
			target.AllowedModels = *input.AllowedModels
		}
		if input.DailyCallLimit != nil {
			target.DailyCallLimit = *input.DailyCallLimit
		}
		TouchAudit(&target.AuditFields, actor.ID)
		updates := map[string]any{
			"status":           target.Status,
			"plan_type":        target.PlanType,
			"allowed_models":   target.AllowedModels,
			"daily_call_limit": target.DailyCallLimit,
			"auth_version":     target.AuthVersion,
			"updated_at":       target.UpdatedAt,
			"updated_by":       target.UpdatedBy,
		}
		if err := tx.Model(&models.User{}).Where("id = ? AND is_deleted = 0", target.ID).Updates(updates).Error; err != nil {
			return err
		}
		updated = target
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (a *AuthService) mutateManagedUser(ctx context.Context, actorID, targetID int64, deleteUser bool) error {
	if err := a.requireAuthRedis(ctx); err != nil {
		return err
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var actor, target models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND is_deleted = 0", actorID).First(&actor).Error; err != nil {
			return errForbidden("无权限管理该用户")
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND is_deleted = 0", targetID).First(&target).Error; err != nil {
			return errNotFound("用户不存在")
		}
		if err := CanManageUser(&actor, &target); err != nil {
			return err
		}
		event := models.AuthAuditEventUserDisabled
		if deleteUser {
			event = models.AuthAuditEventUserDeleted
		}
		if err := a.revokeUserSessionsLocked(ctx, tx, &target, actorID, event); err != nil {
			return err
		}
		TouchAudit(&target.AuditFields, actorID)
		updates := map[string]any{
			"status": models.UserStatusDisabled, "auth_version": target.AuthVersion + 1,
			"updated_at": target.UpdatedAt, "updated_by": target.UpdatedBy,
		}
		if deleteUser {
			updates["is_deleted"] = 1
			updates["password_hash"] = nil
			updates["phone"] = nil
			updates["nickname"] = nil
			updates["real_name"] = nil
			updates["id_card_hash"] = nil
			updates["is_verified"] = false
		}
		if err := tx.Model(&models.User{}).Where("id = ? AND is_deleted = 0", target.ID).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Create(&models.AuthAuditEvent{AuditFields: auditFields(&actorID), UserID: &target.ID, EventType: event}).Error
	})
}

func (a *AuthService) revokeUserSessionsLocked(ctx context.Context, tx *gorm.DB, user *models.User, actorID int64, event models.AuthAuditEventType) error {
	if a.sessions == nil || a.sessions.redis == nil {
		return errors.New("Redis authentication store is unavailable")
	}
	now := a.sessions.now()
	var sessions []models.Session
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL", user.ID).Find(&sessions).Error; err != nil {
		return err
	}
	for _, session := range sessions {
		if err := a.sessions.redis.MarkSessionRevoked(ctx, session.SID, sessionTTL(session, now)); err != nil {
			return err
		}
	}
	for i := range sessions {
		if err := a.sessions.revokeLocked(tx, &sessions[i], actorID, event, SessionCreateInput{}); err != nil {
			return err
		}
	}
	return nil
}
