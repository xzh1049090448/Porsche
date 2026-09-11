package service

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

const (
	adminUserStatusVersionConflict = "admin user status version conflict"
	adminUserStatusStateConflict   = "admin user status state conflict"
)

type AdminUserStatusInput struct {
	Status              models.UserStatus
	Reason              *string
	ExpectedAuthVersion int
}

type AdminUserStatusService struct {
	db       *gorm.DB
	redis    *AuthRedis
	sessions *SessionService
	now      func() int64
}

func NewAdminUserStatusService(db *gorm.DB, redisStore *AuthRedis) *AdminUserStatusService {
	return &AdminUserStatusService{
		db: db, redis: redisStore,
		sessions: &SessionService{db: db, redis: redisStore, now: persistence.NowMillis},
		now:      func() int64 { return time.Now().UTC().UnixMilli() },
	}
}

// Change owns the A06 fresh authorization and status transition boundary.
func (s *AdminUserStatusService) Change(ctx context.Context, actor AdminPermissionReadActor, targetGUID int64, input AdminUserStatusInput) (*UserReadDTO, error) {
	if err := validateAdminUserStatusCall(s, ctx, actor, targetGUID, input); err != nil {
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
			return errUnavailable("管理员用户状态更新暂不可用")
		}
		if revoked {
			return errUnauthorized("认证会话不可用")
		}
		rules, err := lockNicknameEditPolicy(tx, storedActor.ID)
		if err != nil {
			return errUnavailable("管理员用户状态更新暂不可用")
		}
		evaluator, err := authz.NewEvaluator(accountForRead(*storedActor), rules)
		if err != nil {
			return errUnavailable("管理员用户状态更新暂不可用")
		}
		capability := "users.enable"
		if input.Status == models.UserStatusDisabled {
			capability = "users.disable"
		}
		switch evaluator.User(capability, accountForRead(*target)) {
		case authz.Hidden:
			return errNotFound("用户不存在")
		case authz.Allowed:
		default:
			return errForbidden("无权限管理该用户")
		}
		if target.AuthVersion != input.ExpectedAuthVersion {
			return errConflict(adminUserStatusVersionConflict)
		}
		if target.Status == input.Status {
			return errConflict(adminUserStatusStateConflict)
		}
		if target.AuthVersion >= math.MaxInt32 {
			return errUnavailable("管理员用户状态更新暂不可用")
		}

		beforeStatus := target.Status.String()
		// Enabling also clears any active session left by older or partial
		// disable paths. Otherwise an old Refresh could rotate after the account
		// becomes active and silently restore a pre-disable session.
		auth := &AuthService{db: s.db, sessions: s.sessions}
		if err := auth.revokeUserSessionsLocked(ctx, tx, target, storedActor.ID, models.AuthAuditEventSessionRevoked); err != nil {
			return errUnavailable("管理员用户状态更新暂不可用")
		}
		nextVersion := target.AuthVersion + 1
		updatedAt := s.now()
		updated := tx.Model(&models.User{}).Where("id = ? AND guid = ? AND is_deleted = 0 AND auth_version = ? AND status = ? AND role = ?", target.ID, target.Guid, target.AuthVersion, target.Status, target.Role).
			Updates(map[string]any{"status": input.Status, "auth_version": nextVersion, "updated_at": updatedAt, "updated_by": storedActor.ID})
		if updated.Error != nil || updated.RowsAffected != 1 {
			return errUnavailable("管理员用户状态更新暂不可用")
		}
		event := models.AuthAuditEventManagedUserUpdated
		if input.Status == models.UserStatusDisabled {
			event = models.AuthAuditEventUserDisabled
		}
		if err := tx.Create(&models.AuthAuditEvent{AuditFields: auditFields(&storedActor.ID), UserID: &target.ID, EventType: event}).Error; err != nil {
			return errUnavailable("管理员用户状态更新暂不可用")
		}
		action := capability
		resource := "user:" + strconv.FormatInt(target.Guid, 10)
		detail := models.JSONMap{"target_guid": target.Guid, "before_status": beforeStatus, "after_status": input.Status.String()}
		if input.Reason != nil {
			detail["reason"] = *input.Reason
		}
		if err := tx.Create(&models.AuditLog{AuditFields: auditFields(&storedActor.ID), UserID: &target.ID, Action: action, Resource: &resource, Detail: detail}).Error; err != nil {
			return errUnavailable("管理员用户状态更新暂不可用")
		}
		target.Status, target.AuthVersion, target.UpdatedAt, target.UpdatedBy = input.Status, nextVersion, updatedAt, &storedActor.ID
		groupKey, err := readAdminUserGroupKey(tx, target.ID)
		if err != nil {
			return errUnavailable("管理员用户状态更新暂不可用")
		}
		result, err = projectUserReadWithGroup(*target, groupKey)
		if err != nil {
			return errUnavailable("管理员用户状态更新暂不可用")
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		if _, ok := err.(*HTTPError); ok {
			return nil, err
		}
		return nil, errUnavailable("管理员用户状态更新暂不可用")
	}
	if result == nil {
		return nil, errUnavailable("管理员用户状态更新暂不可用")
	}
	return result, nil
}

func validateAdminUserStatusCall(s *AdminUserStatusService, ctx context.Context, actor AdminPermissionReadActor, targetGUID int64, input AdminUserStatusInput) error {
	if s == nil || s.db == nil || s.db.Config == nil || s.db.Statement == nil || s.db.Statement.ConnPool == nil || s.redis == nil || s.sessions == nil || s.now == nil || ctx == nil {
		return errUnavailable("管理员用户状态更新暂不可用")
	}
	if actor.UserID <= 0 || actor.AuthVersion <= 0 || actor.SessionSID == "" || actor.SessionVersion <= 0 {
		return errUnauthorized("认证会话不可用")
	}
	if targetGUID <= 0 || input.ExpectedAuthVersion <= 0 || input.ExpectedAuthVersion > math.MaxInt32 {
		return errBadRequest("无效用户状态更新")
	}
	switch input.Status {
	case models.UserStatusDisabled:
		if input.Reason == nil || !utf8.ValidString(*input.Reason) || strings.TrimSpace(*input.Reason) != *input.Reason || utf8.RuneCountInString(*input.Reason) < 1 || utf8.RuneCountInString(*input.Reason) > 200 {
			return errBadRequest("无效用户状态更新")
		}
	case models.UserStatusActive:
		if input.Reason != nil {
			return errBadRequest("无效用户状态更新")
		}
	default:
		return errBadRequest("无效用户状态更新")
	}
	return nil
}

func AdminUserStatusConflictCode(err error) string {
	var httpError *HTTPError
	if !errors.As(err, &httpError) || httpError.Status != 409 {
		return ""
	}
	if httpError.Message == adminUserStatusStateConflict {
		return "user_status_conflict"
	}
	if httpError.Message == adminUserStatusVersionConflict {
		return "auth_version_conflict"
	}
	return ""
}
