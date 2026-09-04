package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrAdminPermissionUnauthenticated = errors.New("permission authentication invalid")
	ErrAdminPermissionInvalid         = errors.New("invalid permission request")
	ErrAdminPermissionDenied          = errors.New("permission access denied")
	ErrAdminPermissionHidden          = errors.New("permission target hidden")
	ErrAdminPermissionUnavailable     = errors.New("permission information unavailable")
)

type AdminCatalogCapability struct {
	Name         string `json:"name"`
	AdminDefault bool   `json:"admin_default"`
	Grantable    bool   `json:"grantable"`
	RootOnly     bool   `json:"root_only"`
	Available    bool   `json:"available"`
}

type AdminPermissionCatalog struct {
	CatalogVersion  int                      `json:"catalog_version"`
	OverrideEffects []string                 `json:"override_effects"`
	Capabilities    []AdminCatalogCapability `json:"capabilities"`
}

func AdminPermissionCatalogProjection() AdminPermissionCatalog {
	defs := authz.Catalog()
	items := make([]AdminCatalogCapability, 0, len(defs))
	for _, d := range defs {
		items = append(items, AdminCatalogCapability{Name: d.Name, AdminDefault: d.AdminDefault, Grantable: d.Grantable, RootOnly: d.RootOnly, Available: !d.Unavailable})
	}
	return AdminPermissionCatalog{CatalogVersion: 1, OverrideEffects: []string{"inherit", "allow", "deny"}, Capabilities: items}
}

// ParseAdminPermissionGUID accepts only a canonical positive decimal int64.
func ParseAdminPermissionGUID(raw string) (int64, error) {
	if raw == "" || raw[0] == '+' || raw[0] == '-' || raw[0] == '0' {
		return 0, ErrAdminPermissionInvalid
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, ErrAdminPermissionInvalid
		}
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0, ErrAdminPermissionInvalid
	}
	return v, nil
}

// AdminPermissionReadActor is copied only from successful authentication context.
// It is never bound from request input or serialized as a public DTO.
type AdminPermissionReadActor struct {
	UserID         int64
	AuthVersion    int
	SessionSID     string
	SessionVersion int
}

type AdminPermissionCapability struct {
	Name            string `json:"name"`
	Baseline        bool   `json:"baseline"`
	Override        string `json:"override"`
	PolicyEffective bool   `json:"policy_effective"`
	Effective       bool   `json:"effective"`
}
type AdminPermissionDetail struct {
	UserGUID           string                      `json:"user_guid"`
	Role               string                      `json:"role"`
	Status             string                      `json:"status"`
	CatalogVersion     int                         `json:"catalog_version"`
	PermissionsVersion string                      `json:"permissions_version"`
	Capabilities       []AdminPermissionCapability `json:"capabilities"`
}

func adminPermissionDetailProjection(target models.User, version int64, rules []authz.Override) (*AdminPermissionDetail, error) {
	if version < 0 || target.AuthVersion <= 0 {
		return nil, ErrAdminPermissionUnavailable
	}
	items, err := authz.ProjectAdminPolicy(authz.Account{ID: target.ID, GUID: target.Guid, Role: target.Role, Status: target.Status, IsDeleted: target.IsDeleted}, rules)
	if err != nil {
		return nil, ErrAdminPermissionUnavailable
	}
	result := &AdminPermissionDetail{UserGUID: strconv.FormatInt(target.Guid, 10), Role: "admin", Status: target.Status.String(), CatalogVersion: models.PermissionCatalogVersion, PermissionsVersion: strconv.FormatInt(version, 10), Capabilities: make([]AdminPermissionCapability, 0, len(items))}
	for _, item := range items {
		result.Capabilities = append(result.Capabilities, AdminPermissionCapability{Name: item.Name, Baseline: item.Baseline, Override: string(item.Override), PolicyEffective: item.PolicyEffective, Effective: item.Effective})
	}
	return result, nil
}

// AdminPermissionReadService provides fresh display reads, never write authority.
type AdminPermissionReadService struct {
	db    *gorm.DB
	redis *AuthRedis
}

func NewAdminPermissionReadService(db *gorm.DB, redis *AuthRedis) *AdminPermissionReadService {
	return &AdminPermissionReadService{db: db, redis: redis}
}
func (s *AdminPermissionReadService) Catalog(ctx context.Context, actor AdminPermissionReadActor) (*AdminPermissionCatalog, error) {
	var result *AdminPermissionCatalog
	err := s.read(ctx, actor, 0, func(tx *gorm.DB, target models.User) error {
		v := AdminPermissionCatalogProjection()
		result = &v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (s *AdminPermissionReadService) Detail(ctx context.Context, actor AdminPermissionReadActor, guid int64) (*AdminPermissionDetail, error) {
	if guid <= 0 {
		return nil, ErrAdminPermissionInvalid
	}
	var result *AdminPermissionDetail
	err := s.read(ctx, actor, guid, func(tx *gorm.DB, target models.User) error {
		version, rules, err := readPermissionPolicyRows(tx, target.ID)
		if err != nil {
			return ErrAdminPermissionUnavailable
		}
		result, err = adminPermissionDetailProjection(target, version, rules)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func validPermissionReadUser(user models.User) bool {
	return user.ID > 0 && user.Guid > 0 && user.AuthVersion > 0 &&
		(user.Role == models.UserRoleUser || user.Role == models.UserRoleAdmin || user.Role == models.UserRoleRoot) &&
		(user.Status == models.UserStatusActive || user.Status == models.UserStatusDisabled) &&
		(user.IsDeleted == 0 || user.IsDeleted == 1)
}
func (s *AdminPermissionReadService) read(ctx context.Context, actor AdminPermissionReadActor, targetGUID int64, project func(*gorm.DB, models.User) error) error {
	if s == nil || ctx == nil || s.db == nil || s.db.Statement == nil || s.redis == nil || actor.UserID <= 0 || actor.AuthVersion <= 0 || actor.SessionSID == "" || actor.SessionVersion <= 0 {
		return ErrAdminPermissionUnavailable
	}
	if _, ok := s.db.Statement.ConnPool.(*sql.DB); !ok {
		return ErrAdminPermissionUnavailable
	}
	err := s.db.Session(&gorm.Session{NewDB: true, Logger: logger.Discard}).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user models.User
		fields := []string{"id", "guid", "role", "status", "is_deleted", "auth_version"}
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select(fields).Where("id = ?", actor.UserID).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAdminPermissionUnauthenticated
			}
			return ErrAdminPermissionUnavailable
		}
		if err := validateAdminReadActor(user, actor); err != nil {
			return err
		}

		roleDenied := user.Role != models.UserRoleAdmin && user.Role != models.UserRoleRoot || targetGUID != 0 && user.Role != models.UserRoleRoot
		var target models.User
		var targetErr error
		if !roleDenied && targetGUID != 0 {
			if targetGUID == user.Guid {
				targetErr = ErrAdminPermissionHidden
			} else {
				err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select(fields).Where("guid = ? AND is_deleted = 0", targetGUID).First(&target).Error
				switch {
				case errors.Is(err, gorm.ErrRecordNotFound):
					targetErr = ErrAdminPermissionHidden
				case err != nil:
					return ErrAdminPermissionUnavailable
				case target.Role == models.UserRoleRoot || target.Role == models.UserRoleUser || target.ID == user.ID:
					targetErr = ErrAdminPermissionHidden
				case !validPermissionReadUser(target):
					targetErr = ErrAdminPermissionUnavailable
				}
			}
		}

		var session models.Session
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id", "guid", "sid", "user_id", "session_version", "is_deleted", "revoked_at", "expires_at").Where("sid = ?", actor.SessionSID).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAdminPermissionUnauthenticated
			}
			return ErrAdminPermissionUnavailable
		}
		if err := validateAdminReadSession(session, actor, time.Now().UTC().UnixMilli()); err != nil {
			return err
		}

		revoked, err := s.redis.IsSessionRevoked(ctx, actor.SessionSID)
		if err != nil {
			return ErrAdminPermissionUnavailable
		}
		if revoked {
			return ErrAdminPermissionUnauthenticated
		}
		if roleDenied {
			return ErrAdminPermissionDenied
		}
		if targetErr != nil {
			return targetErr
		}
		return project(tx, target)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if errors.Is(err, ErrAdminPermissionUnauthenticated) || errors.Is(err, ErrAdminPermissionDenied) || errors.Is(err, ErrAdminPermissionHidden) {
		return err
	}
	if err != nil {
		return ErrAdminPermissionUnavailable
	}
	return nil
}

func validateAdminReadActor(user models.User, actor AdminPermissionReadActor) error {
	if !validPermissionReadUser(user) {
		return ErrAdminPermissionUnavailable
	}
	if user.ID != actor.UserID || user.IsDeleted != 0 || user.Status != models.UserStatusActive || user.AuthVersion != actor.AuthVersion {
		return ErrAdminPermissionUnauthenticated
	}
	return nil
}
func validateAdminReadSession(session models.Session, actor AdminPermissionReadActor, now int64) error {
	if session.ID <= 0 || session.Guid <= 0 || session.SessionVersion <= 0 || session.IsDeleted != 0 && session.IsDeleted != 1 {
		return ErrAdminPermissionUnavailable
	}
	if session.UserID != actor.UserID || session.SID != actor.SessionSID || session.SessionVersion != actor.SessionVersion || session.IsDeleted != 0 || session.RevokedAt != nil || session.ExpiresAt <= now {
		return ErrAdminPermissionUnauthenticated
	}
	return nil
}
