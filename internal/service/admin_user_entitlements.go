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
	"gorm.io/gorm/clause"
)

const (
	adminUserEntitlementVersionConflict = "admin user entitlement version conflict"
	adminUserGroupStateConflict         = "admin user group state conflict"
	adminUserPlanStateConflict          = "admin user plan state conflict"
)

type AdminUserGroupChangeInput struct {
	GroupGUID           int64
	Reason              string
	ExpectedAuthVersion int
	RequestID           string
}

type AdminUserPlanChangeInput struct {
	PlanType            models.PlanType
	Reason              string
	ExpectedAuthVersion int
	RequestID           string
}

type AdminUserEntitlementService struct {
	db       *gorm.DB
	redis    *AuthRedis
	sessions *SessionService
	now      func() int64
}

func NewAdminUserEntitlementService(db *gorm.DB, redisStore *AuthRedis) *AdminUserEntitlementService {
	return &AdminUserEntitlementService{db: db, redis: redisStore, sessions: &SessionService{db: db, redis: redisStore, now: persistence.NowMillis}, now: func() int64 { return time.Now().UTC().UnixMilli() }}
}

func (s *AdminUserEntitlementService) ChangeGroup(ctx context.Context, actor AdminPermissionReadActor, targetGUID int64, input AdminUserGroupChangeInput) (*UserReadDTO, error) {
	if err := validateAdminUserEntitlementCall(s, ctx, actor, targetGUID, input.ExpectedAuthVersion, input.Reason, input.RequestID); err != nil || input.GroupGUID <= 0 {
		if err != nil {
			return nil, err
		}
		return nil, errBadRequest("无效用户组更新")
	}
	var group *models.BusinessGroup
	var beforeKey string
	var groupErr error
	return s.change(ctx, actor, targetGUID, input.ExpectedAuthVersion, input.Reason, input.RequestID, "users.group.change", func(tx *gorm.DB, target *models.User, actorID int64, now int64, phase int) (models.JSONMap, error) {
		if phase == entitlementPrepare {
			group, groupErr = lockAdminUserEntitlementGroup(tx, input.GroupGUID)
			return nil, nil
		}
		if phase == entitlementValidate {
			if groupErr != nil {
				return nil, groupErr
			}
			if target.GroupID == group.ID {
				return nil, errConflict(adminUserGroupStateConflict)
			}
			var readErr error
			beforeKey, readErr = readAdminUserGroupKey(tx, target.ID)
			if readErr != nil {
				return nil, errUnavailable("管理员用户组更新暂不可用")
			}
			return models.JSONMap{"before_group": beforeKey, "after_group": group.Key}, nil
		}
		updated := tx.Model(&models.User{}).Where("id = ? AND guid = ? AND is_deleted = 0 AND auth_version = ? AND group_id = ?", target.ID, target.Guid, target.AuthVersion, target.GroupID).
			Updates(map[string]any{"group_id": group.ID, "auth_version": target.AuthVersion + 1, "updated_at": now, "updated_by": actorID})
		if updated.Error != nil || updated.RowsAffected != 1 {
			return nil, errUnavailable("管理员用户组更新暂不可用")
		}
		target.GroupID, target.AuthVersion, target.UpdatedAt, target.UpdatedBy = group.ID, target.AuthVersion+1, now, &actorID
		return nil, nil
	})
}

func (s *AdminUserEntitlementService) ChangePlan(ctx context.Context, actor AdminPermissionReadActor, targetGUID int64, input AdminUserPlanChangeInput) (*UserReadDTO, error) {
	if err := validateAdminUserEntitlementCall(s, ctx, actor, targetGUID, input.ExpectedAuthVersion, input.Reason, input.RequestID); err != nil {
		return nil, err
	}
	if input.PlanType != models.PlanFree && input.PlanType != models.PlanProfessional && input.PlanType != models.PlanEnterprise {
		return nil, errBadRequest("无效套餐更新")
	}
	return s.change(ctx, actor, targetGUID, input.ExpectedAuthVersion, input.Reason, input.RequestID, "users.plan.change", func(tx *gorm.DB, target *models.User, actorID int64, now int64, phase int) (models.JSONMap, error) {
		if phase == entitlementPrepare {
			return nil, nil
		}
		if phase == entitlementValidate {
			if target.PlanType == input.PlanType {
				return nil, errConflict(adminUserPlanStateConflict)
			}
			return models.JSONMap{"before_plan_type": target.PlanType.String(), "after_plan_type": input.PlanType.String(), "change_kind": "grant_adjustment"}, nil
		}
		updated := tx.Model(&models.User{}).Where("id = ? AND guid = ? AND is_deleted = 0 AND auth_version = ? AND plan_type = ?", target.ID, target.Guid, target.AuthVersion, target.PlanType).
			Updates(map[string]any{"plan_type": input.PlanType, "daily_call_limit": 100, "auth_version": target.AuthVersion + 1, "updated_at": now, "updated_by": actorID})
		if updated.Error != nil || updated.RowsAffected != 1 {
			return nil, errUnavailable("管理员用户套餐更新暂不可用")
		}
		target.PlanType, target.DailyCallLimit, target.AuthVersion, target.UpdatedAt, target.UpdatedBy = input.PlanType, 100, target.AuthVersion+1, now, &actorID
		return nil, nil
	})
}

const (
	entitlementPrepare = iota
	entitlementValidate
	entitlementApply
)

type adminUserEntitlementMutation func(*gorm.DB, *models.User, int64, int64, int) (models.JSONMap, error)

func (s *AdminUserEntitlementService) change(ctx context.Context, actor AdminPermissionReadActor, targetGUID int64, expectedVersion int, reason, requestID, capability string, mutate adminUserEntitlementMutation) (*UserReadDTO, error) {
	var result *UserReadDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		storedActor, err := lockNicknameEditActor(tx, actor.UserID)
		if err != nil {
			return err
		}
		if err = validateNicknameEditActor(*storedActor, actor); err != nil {
			return err
		}
		now := s.now()
		session, err := lockNicknameEditSession(tx, storedActor.ID, actor.SessionSID, now)
		if err != nil {
			return err
		}
		if err = validateNicknameEditSession(*session, actor, now); err != nil {
			return err
		}
		revoked, err := s.redis.IsSessionRevoked(ctx, actor.SessionSID)
		if err != nil {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		if revoked {
			return errUnauthorized("认证会话不可用")
		}
		target, err := lockNicknameEditTarget(tx, targetGUID)
		if err != nil {
			return err
		}
		if _, err = mutate(tx, target, storedActor.ID, s.now(), entitlementPrepare); err != nil {
			return err
		}
		if err = lockAdminUserEntitlementTargetSessions(tx, target.ID); err != nil {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		rules, err := lockNicknameEditPolicy(tx, storedActor.ID)
		if err != nil {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		evaluator, err := authz.NewEvaluator(accountForRead(*storedActor), rules)
		if err != nil {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		switch evaluator.User(capability, accountForRead(*target)) {
		case authz.Hidden:
			return errNotFound("用户不存在")
		case authz.Allowed:
		default:
			return errForbidden("无权限管理该用户")
		}
		if target.AuthVersion != expectedVersion {
			return errConflict(adminUserEntitlementVersionConflict)
		}
		if target.AuthVersion >= math.MaxInt32 {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		detail, err := mutate(tx, target, storedActor.ID, s.now(), entitlementValidate)
		if err != nil {
			return err
		}
		auth := &AuthService{db: s.db, sessions: s.sessions}
		if err = auth.revokeUserSessionsLocked(ctx, tx, target, storedActor.ID, models.AuthAuditEventSessionRevoked); err != nil {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		if _, err = mutate(tx, target, storedActor.ID, s.now(), entitlementApply); err != nil {
			return err
		}
		if err = tx.Create(&models.AuthAuditEvent{AuditFields: auditFields(&storedActor.ID), UserID: &target.ID, EventType: models.AuthAuditEventManagedUserUpdated}).Error; err != nil {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		detail["actor_guid"], detail["target_guid"], detail["reason"], detail["request_id"] = storedActor.Guid, target.Guid, reason, requestID
		resource := "user:" + strconv.FormatInt(target.Guid, 10)
		if err = tx.Create(&models.AuditLog{AuditFields: auditFields(&storedActor.ID), UserID: &target.ID, Action: capability, Resource: &resource, Detail: detail}).Error; err != nil {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		groupKey, err := readAdminUserGroupKey(tx, target.ID)
		if err != nil {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		result, err = projectUserReadWithGroup(*target, groupKey)
		if err != nil {
			return errUnavailable("管理员用户权益更新暂不可用")
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		if _, ok := err.(*HTTPError); ok {
			return nil, err
		}
		return nil, errUnavailable("管理员用户权益更新暂不可用")
	}
	if result == nil {
		return nil, errUnavailable("管理员用户权益更新暂不可用")
	}
	return result, nil
}

func validateAdminUserEntitlementCall(s *AdminUserEntitlementService, ctx context.Context, actor AdminPermissionReadActor, targetGUID int64, version int, reason, requestID string) error {
	if s == nil || s.db == nil || s.db.Config == nil || s.db.Statement == nil || s.db.Statement.ConnPool == nil || s.redis == nil || s.sessions == nil || s.now == nil || ctx == nil {
		return errUnavailable("管理员用户权益更新暂不可用")
	}
	if actor.UserID <= 0 || actor.AuthVersion <= 0 || actor.SessionSID == "" || actor.SessionVersion <= 0 {
		return errUnauthorized("认证会话不可用")
	}
	if targetGUID <= 0 || version <= 0 || version > math.MaxInt32 || requestID == "" || !utf8.ValidString(reason) || strings.TrimSpace(reason) != reason || utf8.RuneCountInString(reason) < 1 || utf8.RuneCountInString(reason) > 200 {
		return errBadRequest("无效用户权益更新")
	}
	return nil
}

func lockAdminUserEntitlementGroup(tx *gorm.DB, guid int64) (*models.BusinessGroup, error) {
	var groups []models.BusinessGroup
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guid = ? AND status = ? AND is_deleted = 0", guid, models.BusinessGroupStatusActive).Order("id ASC").Limit(2).Find(&groups).Error; err != nil {
		return nil, errUnavailable("管理员用户组更新暂不可用")
	}
	if len(groups) == 0 {
		return nil, errNotFound("用户组不存在")
	}
	if len(groups) != 1 || groups[0].ID <= 0 || groups[0].Guid != guid || groups[0].Status != models.BusinessGroupStatusActive || groups[0].IsDeleted != 0 || !businessGroupKeyPattern.MatchString(groups[0].Key) {
		return nil, errUnavailable("管理员用户组更新暂不可用")
	}
	return &groups[0], nil
}

func lockAdminUserEntitlementTargetSessions(tx *gorm.DB, userID int64) error {
	var rows []models.Session
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL", userID).Order("id ASC").Find(&rows).Error
}

func AdminUserEntitlementConflictCode(err error) string {
	var value *HTTPError
	if !errors.As(err, &value) || value.Status != 409 {
		return ""
	}
	switch value.Message {
	case adminUserEntitlementVersionConflict:
		return "auth_version_conflict"
	case adminUserGroupStateConflict:
		return "user_group_conflict"
	case adminUserPlanStateConflict:
		return "user_plan_conflict"
	}
	return ""
}

func AdminUserEntitlementNotFoundCode(err error) string {
	var value *HTTPError
	if !errors.As(err, &value) || value.Status != 404 {
		return ""
	}
	if value.Message == "用户不存在" {
		return "user_not_found"
	}
	if value.Message == "用户组不存在" {
		return "group_not_found"
	}
	return ""
}
