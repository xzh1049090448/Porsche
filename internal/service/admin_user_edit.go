package service

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AdminUserNicknameEditInput is the normalized A05 edit intent. ClearNickname
// distinguishes JSON null from an omitted or invalid nickname field.
type AdminUserNicknameEditInput struct {
	Nickname            *string
	ClearNickname       bool
	ExpectedAuthVersion int
}

// AdminUserNicknameEditService owns the fresh authorization and atomic write
// boundary for PATCH /admin/v2/users/{guid}.
type AdminUserNicknameEditService struct {
	db    *gorm.DB
	redis *AuthRedis
	now   func() int64
}

func NewAdminUserNicknameEditService(db *gorm.DB, redisStore *AuthRedis) *AdminUserNicknameEditService {
	return &AdminUserNicknameEditService{db: db, redis: redisStore, now: func() int64 { return time.Now().UTC().UnixMilli() }}
}

// Edit changes only nickname and the mandatory update audit columns. It never
// advances auth_version or revokes any session because nickname is not a
// credential or authorization field.
func (s *AdminUserNicknameEditService) Edit(ctx context.Context, actor AdminPermissionReadActor, targetGUID int64, input AdminUserNicknameEditInput) (*UserReadDTO, error) {
	if err := validateAdminUserNicknameEditCall(s, ctx, actor, targetGUID, input); err != nil {
		return nil, err
	}
	var result *UserReadDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		storedActor, err := lockNicknameEditActor(tx, actor.UserID)
		if err != nil {
			return err
		}
		if err := validateNicknameEditActor(*storedActor, actor); err != nil {
			return err
		}

		target, err := lockNicknameEditTarget(tx, targetGUID)
		if err != nil {
			return err
		}

		session, err := lockNicknameEditSession(tx, storedActor.ID, actor.SessionSID, s.now())
		if err != nil {
			return err
		}
		if err := validateNicknameEditSession(*session, actor, s.now()); err != nil {
			return err
		}
		revoked, err := s.redis.IsSessionRevoked(ctx, actor.SessionSID)
		if err != nil {
			return errUnavailable("管理员用户昵称更新暂不可用")
		}
		if revoked {
			return errUnauthorized("认证会话不可用")
		}

		rules, err := lockNicknameEditPolicy(tx, storedActor.ID)
		if err != nil {
			return errUnavailable("管理员用户昵称更新暂不可用")
		}
		evaluator, err := authz.NewEvaluator(accountForRead(*storedActor), rules)
		if err != nil {
			return errUnavailable("管理员用户昵称更新暂不可用")
		}
		switch evaluator.User("users.edit", accountForRead(*target)) {
		case authz.Hidden:
			return errNotFound("用户不存在")
		case authz.Allowed:
		default:
			return errForbidden("无权限管理该用户")
		}
		if target.AuthVersion != input.ExpectedAuthVersion {
			return errConflict("auth_version_conflict")
		}

		var next *string
		if !input.ClearNickname {
			value := *input.Nickname
			next = &value
		}
		if sameOptionalString(target.Nickname, next) {
			groupKey, err := readAdminUserGroupKey(tx, target.ID)
			if err != nil {
				return errUnavailable("管理员用户昵称更新暂不可用")
			}
			result, err = projectUserReadWithGroup(*target, groupKey)
			if err != nil {
				return errUnavailable("管理员用户昵称更新暂不可用")
			}
			return nil
		}

		updatedBy := storedActor.ID
		auditFields := auditFields(&updatedBy)
		now := auditFields.CreatedAt
		updated := tx.Model(&models.User{}).
			Where("id = ? AND guid = ? AND is_deleted = 0 AND auth_version = ? AND role = ? AND status = ?", target.ID, target.Guid, target.AuthVersion, target.Role, target.Status).
			Updates(map[string]any{"nickname": next, "updated_at": now, "updated_by": updatedBy})
		if updated.Error != nil || updated.RowsAffected != 1 {
			return errUnavailable("管理员用户昵称更新暂不可用")
		}
		if err := tx.Create(&models.AuthAuditEvent{
			AuditFields: auditFields,
			UserID:      &target.ID, EventType: models.AuthAuditEventManagedUserUpdated,
		}).Error; err != nil {
			return errUnavailable("管理员用户昵称更新暂不可用")
		}
		target.Nickname, target.UpdatedAt, target.UpdatedBy = next, now, &updatedBy
		groupKey, err := readAdminUserGroupKey(tx, target.ID)
		if err != nil {
			return errUnavailable("管理员用户昵称更新暂不可用")
		}
		result, err = projectUserReadWithGroup(*target, groupKey)
		if err != nil {
			return errUnavailable("管理员用户昵称更新暂不可用")
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		if _, ok := err.(*HTTPError); ok {
			return nil, err
		}
		return nil, errUnavailable("管理员用户昵称更新暂不可用")
	}
	if result == nil {
		return nil, errUnavailable("管理员用户昵称更新暂不可用")
	}
	return result, nil
}

func validateAdminUserNicknameEditCall(s *AdminUserNicknameEditService, ctx context.Context, actor AdminPermissionReadActor, targetGUID int64, input AdminUserNicknameEditInput) error {
	if s == nil || s.db == nil || s.db.Config == nil || s.db.Statement == nil || s.db.Statement.ConnPool == nil || s.redis == nil || s.now == nil || ctx == nil {
		return errUnavailable("管理员用户昵称更新暂不可用")
	}
	if actor.UserID <= 0 || actor.AuthVersion <= 0 || actor.SessionSID == "" || actor.SessionVersion <= 0 {
		return errUnauthorized("认证会话不可用")
	}
	if targetGUID <= 0 {
		return errBadRequest("无效用户标识")
	}
	if input.ExpectedAuthVersion <= 0 || input.ExpectedAuthVersion > math.MaxInt32 || input.Nickname == nil == !input.ClearNickname {
		return errBadRequest("无效昵称更新")
	}
	if input.Nickname != nil && (!utf8.ValidString(*input.Nickname) || strings.TrimSpace(*input.Nickname) != *input.Nickname || utf8.RuneCountInString(*input.Nickname) < 1 || utf8.RuneCountInString(*input.Nickname) > 64) {
		return errBadRequest("无效昵称更新")
	}
	return nil
}

func lockNicknameEditActor(tx *gorm.DB, actorID int64) (*models.User, error) {
	var actor models.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "role", "status", "is_deleted", "auth_version").Where("id = ?", actorID).First(&actor).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errUnauthorized("认证会话不可用")
		}
		return nil, errUnavailable("管理员用户昵称更新暂不可用")
	}
	return &actor, nil
}

func validateNicknameEditActor(stored models.User, proof AdminPermissionReadActor) error {
	if !validPermissionReadUser(stored) {
		return errUnavailable("管理员用户昵称更新暂不可用")
	}
	if stored.ID != proof.UserID || stored.IsDeleted != 0 || stored.Status != models.UserStatusActive || stored.AuthVersion != proof.AuthVersion {
		return errUnauthorized("认证会话不可用")
	}
	if stored.Role != models.UserRoleAdmin && stored.Role != models.UserRoleRoot {
		return errForbidden("无权限管理该用户")
	}
	return nil
}

func lockNicknameEditTarget(tx *gorm.DB, guid int64) (*models.User, error) {
	var target models.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guid = ? AND is_deleted = 0", guid).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errNotFound("用户不存在")
		}
		return nil, errUnavailable("管理员用户昵称更新暂不可用")
	}
	if !validPermissionReadUser(target) {
		return nil, errUnavailable("管理员用户昵称更新暂不可用")
	}
	return &target, nil
}

func lockNicknameEditSession(tx *gorm.DB, userID int64, sid string, now int64) (*models.Session, error) {
	var candidates []models.Session
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "sid", "user_id", "session_version", "is_deleted", "revoked_at", "expires_at").Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND expires_at > ?", userID, now).Order("id ASC").Find(&candidates).Error; err != nil {
		return nil, errUnavailable("管理员用户昵称更新暂不可用")
	}
	var found *models.Session
	for i := range candidates {
		if constantTimeSIDEqual(candidates[i].SID, sid) {
			if found != nil {
				return nil, errUnavailable("管理员用户昵称更新暂不可用")
			}
			copy := candidates[i]
			found = &copy
		}
	}
	if found == nil {
		return nil, errUnauthorized("认证会话不可用")
	}
	return found, nil
}

func validateNicknameEditSession(session models.Session, actor AdminPermissionReadActor, now int64) error {
	if session.ID <= 0 || session.Guid <= 0 || session.SessionVersion <= 0 {
		return errUnavailable("管理员用户昵称更新暂不可用")
	}
	if session.UserID != actor.UserID || session.SessionVersion != actor.SessionVersion || session.IsDeleted != 0 || session.RevokedAt != nil || session.ExpiresAt <= now {
		return errUnauthorized("认证会话不可用")
	}
	return nil
}

func lockNicknameEditPolicy(tx *gorm.DB, userID int64) ([]authz.Override, error) {
	var headIDs []int64
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&models.PermissionPolicyHead{}).Where("user_id = ?").Order("id ASC").Pluck("id", &headIDs).Error; err != nil {
		return nil, err
	}
	var ruleIDs []int64
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&models.PermissionOverride{}).Where("user_id = ?").Order("id ASC").Pluck("id", &ruleIDs).Error; err != nil {
		return nil, err
	}
	_, rules, err := readPermissionPolicyRows(tx, userID)
	return rules, err
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
