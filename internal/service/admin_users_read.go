package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// AdminUsersReadService owns fresh read transactions. Neither its DTOs nor the
// authentication projection is a reusable authorization credential.
type AdminUsersReadService struct {
	db    *gorm.DB
	redis *AuthRedis
}

func NewAdminUsersReadService(db *gorm.DB, redis *AuthRedis) *AdminUsersReadService {
	return &AdminUsersReadService{db: db, redis: redis}
}

type AuthPermissionProjection struct {
	AdminPermissions   []string `json:"admin_permissions"`
	PermissionsVersion string   `json:"permissions_version"`
}
type FreshAuthProjection struct {
	User        models.User
	Permissions *AuthPermissionProjection
}

func accountForRead(u models.User) authz.Account {
	return authz.Account{ID: u.ID, GUID: u.Guid, Role: u.Role, Status: u.Status, IsDeleted: u.IsDeleted}
}
func projectAuthPermissions(e *authz.Evaluator, version int64) (*AuthPermissionProjection, error) {
	if version < 0 || e == nil {
		return nil, ErrAdminPermissionUnavailable
	}
	return &AuthPermissionProjection{AdminPermissions: e.CapabilityNames(), PermissionsVersion: strconv.FormatInt(version, 10)}, nil
}

// AuthProjection requires the exact authenticated AV/SV copied by the adapter.
func (s *AdminUsersReadService) AuthProjection(ctx context.Context, actor AdminPermissionReadActor) (*FreshAuthProjection, error) {
	return s.authProjection(ctx, actor, nil)
}

// RefreshProjection accepts only the service-issued session, never request DTOs.
// Refresh credentials do not carry AV: the locked current identity supplies AV
// before access signing. Known AV proofs must use AuthProjection instead.
func (s *AdminUsersReadService) RefreshProjection(ctx context.Context, issued *IssuedSession) (*FreshAuthProjection, error) {
	if issued == nil || !issued.refreshProof || issued.Session == nil || issued.Session.ID <= 0 {
		return nil, ErrAdminPermissionUnavailable
	}
	session := issued.Session
	actor := AdminPermissionReadActor{UserID: session.UserID, SessionSID: session.SID, SessionVersion: session.SessionVersion}
	return s.authProjection(ctx, actor, issued)
}
func (s *AdminUsersReadService) authProjection(ctx context.Context, actor AdminPermissionReadActor, issued *IssuedSession) (*FreshAuthProjection, error) {
	var result *FreshAuthProjection
	err := s.freshRead(ctx, actor, readIntent{projection: true, issued: issued}, func(tx *gorm.DB, user, target models.User, e *authz.Evaluator, version int64, policyErr error) error {
		result = &FreshAuthProjection{User: user}
		if policyErr == nil {
			var err error
			result.Permissions, err = projectAuthPermissions(e, version)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type readIntent struct {
	guid       int64
	legacy     bool
	deleted    bool
	projection bool
	issued     *IssuedSession
}
type freshReadResult func(*gorm.DB, models.User, models.User, *authz.Evaluator, int64, error) error

var userReadColumns = []string{"id", "guid", "username", "nickname", "plan_type", "role", "status", "is_deleted", "auth_version", "created_at", "last_login_at", "is_verified", "total_tokens_used", "daily_calls_used", "daily_call_limit"}

func (s *AdminUsersReadService) freshRead(ctx context.Context, actor AdminPermissionReadActor, intent readIntent, consume freshReadResult) error {
	if s == nil || ctx == nil || s.db == nil || s.db.Statement == nil || s.redis == nil || actor.UserID <= 0 || actor.SessionSID == "" || actor.SessionVersion <= 0 || intent.issued == nil && actor.AuthVersion <= 0 {
		return ErrAdminPermissionUnavailable
	}
	if _, ok := s.db.Statement.ConnPool.(*sql.DB); !ok {
		return ErrAdminPermissionUnavailable
	}
	err := s.db.Session(&gorm.Session{NewDB: true, Logger: logger.Discard}).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select(userReadColumns).Where("id = ?", actor.UserID).First(&user).Error; err != nil {
			return readIdentityDBError(err)
		}
		proof := actor
		if intent.issued != nil {
			proof.AuthVersion = user.AuthVersion
		}
		if err := validateAdminReadActor(user, proof); err != nil {
			return err
		}
		version, rules, policyErr := readPermissionPolicyRows(tx, user.ID)
		var evaluator *authz.Evaluator
		if policyErr == nil {
			evaluator, policyErr = authz.NewEvaluator(accountForRead(user), rules)
		}
		denied := !intent.projection && policyErr == nil && evaluator.Collection("users.read", intent.deleted) != authz.Allowed
		var target models.User
		var targetErr error
		if intent.guid != 0 && policyErr == nil && !denied {
			if intent.guid == user.Guid {
				targetErr = ErrAdminPermissionHidden
			} else {
				err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select(userReadColumns).Where("guid = ?", intent.guid).First(&target).Error
				switch {
				case errors.Is(err, gorm.ErrRecordNotFound):
					targetErr = ErrAdminPermissionHidden
				case err != nil:
					return ErrAdminPermissionUnavailable
				case target.ID == user.ID || target.Role == models.UserRoleRoot || target.Role == models.UserRoleAdmin && user.Role == models.UserRoleAdmin:
					targetErr = ErrAdminPermissionHidden
				case target.IsDeleted == 1 && (intent.legacy || evaluator.Collection("users.read", true) != authz.Allowed):
					targetErr = ErrAdminPermissionHidden
				case !validPermissionReadUser(target):
					targetErr = ErrAdminPermissionUnavailable
				case evaluator.User("users.read", accountForRead(target)) != authz.Allowed:
					targetErr = ErrAdminPermissionHidden
				}
			}
		}
		var session models.Session
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id", "guid", "sid", "user_id", "session_version", "is_deleted", "revoked_at", "expires_at").Where("sid = ?", actor.SessionSID).First(&session).Error; err != nil {
			return readIdentityDBError(err)
		}
		if intent.issued != nil && session.ID != intent.issued.Session.ID {
			return ErrAdminPermissionUnauthenticated
		}
		if err := validateAdminReadSession(session, proof, time.Now().UTC().UnixMilli()); err != nil {
			return err
		}
		revoked, err := s.redis.IsSessionRevoked(ctx, actor.SessionSID)
		if err != nil {
			return ErrAdminPermissionUnavailable
		}
		if revoked {
			return ErrAdminPermissionUnauthenticated
		}
		if !intent.projection && policyErr != nil {
			return ErrAdminPermissionUnavailable
		}
		if denied {
			return ErrAdminPermissionDenied
		}
		if targetErr != nil {
			return targetErr
		}
		return consume(tx, user, target, evaluator, version, policyErr)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if errors.Is(err, ErrAdminPermissionUnauthenticated) || errors.Is(err, ErrAdminPermissionDenied) || errors.Is(err, ErrAdminPermissionHidden) {
		return err
	}
	if err != nil {
		return ErrAdminPermissionUnavailable
	}
	return nil
}
func readIdentityDBError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrAdminPermissionUnauthenticated
	}
	return ErrAdminPermissionUnavailable
}

func (s *AdminUsersReadService) Detail(ctx context.Context, actor AdminPermissionReadActor, guid int64) (*UserReadDTO, error) {
	if guid <= 0 {
		return nil, ErrAdminPermissionInvalid
	}
	var result *UserReadDTO
	err := s.freshRead(ctx, actor, readIntent{guid: guid}, func(tx *gorm.DB, user, target models.User, e *authz.Evaluator, version int64, policyErr error) error {
		var err error
		result, err = ProjectUserRead(target)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (s *AdminUsersReadService) LegacyDetail(ctx context.Context, actor AdminPermissionReadActor, guid int64) (*models.User, error) {
	if guid <= 0 {
		return nil, ErrAdminPermissionHidden
	}
	var result *models.User
	err := s.freshRead(ctx, actor, readIntent{guid: guid, legacy: true}, func(tx *gorm.DB, user, target models.User, e *authz.Evaluator, version int64, policyErr error) error {
		if _, err := ProjectUserRead(target); err != nil {
			return err
		}
		result = &target
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (s *AdminUsersReadService) LegacyBehavior(ctx context.Context, actor AdminPermissionReadActor, guid int64) (map[string]interface{}, error) {
	if guid <= 0 {
		return nil, ErrAdminPermissionHidden
	}
	var result map[string]interface{}
	err := s.freshRead(ctx, actor, readIntent{guid: guid, legacy: true}, func(tx *gorm.DB, user, target models.User, e *authz.Evaluator, version int64, policyErr error) error {
		var err error
		if _, err := ProjectUserRead(target); err != nil {
			return err
		}
		result, err = UserBehavior(tx, target.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func usersReadWhere(user models.User, q AdminUsersReadQuery) (string, []interface{}) {
	where := "id <> ? AND guid <> ? AND role IN (?)"
	roles := []models.UserRole{models.UserRoleUser}
	if user.Role == models.UserRoleRoot {
		roles = append(roles, models.UserRoleAdmin)
	}
	args := []interface{}{user.ID, user.Guid, roles}
	if q.Status == "deleted" {
		where += " AND is_deleted = 1"
	} else {
		where += " AND is_deleted = 0"
		switch q.Status {
		case "active":
			where += " AND status = ?"
			args = append(args, models.UserStatusActive)
		case "disabled":
			where += " AND status = ?"
			args = append(args, models.UserStatusDisabled)
		default:
			where += " AND status IN (?,?)"
			args = append(args, models.UserStatusActive, models.UserStatusDisabled)
		}
	}
	if q.Role != "" {
		role, _ := models.ParseUserRole(q.Role)
		where += " AND role = ?"
		args = append(args, role)
	}
	if q.Q != "" {
		where += " AND (username LIKE ? ESCAPE '!' OR nickname LIKE ? ESCAPE '!'"
		args = append(args, q.LikePattern(), q.LikePattern())
		if guid := q.ExactGUID(); guid > 0 {
			where += " OR guid = ?"
			args = append(args, guid)
		}
		where += ")"
	}
	return where, args
}

func adminUsersListStatement(user models.User, q AdminUsersReadQuery) (string, []interface{}) {
	where, whereArgs := usersReadWhere(user, q)
	order := q.Sort + " " + q.Order
	if q.Sort != "guid" {
		order += ", guid " + q.Order
	}
	columns := strings.Join(userReadColumns, ",")
	// Both reads remain in one statement snapshot. The count uses the exact
	// predicate generated for filtered while avoiding the low-selectivity
	// active/updated index; paged keeps the optimizer-selected ordering index.
	query := "WITH filtered AS (SELECT " + columns + " FROM users WHERE " + where + "), counted AS (SELECT COUNT(*) AS total FROM users IGNORE INDEX FOR JOIN (idx_users_active_updated) WHERE " + where + "), paged AS (SELECT * FROM filtered ORDER BY " + order + " LIMIT ? OFFSET ?) SELECT counted.total, paged.* FROM counted LEFT JOIN paged ON TRUE ORDER BY " + strings.ReplaceAll(order, ", ", ", paged.")
	// Qualify the first ordering term as well; identifiers are a fixed allowlist.
	query = strings.Replace(query, "ON TRUE ORDER BY ", "ON TRUE ORDER BY paged.", 1)
	args := make([]interface{}, 0, len(whereArgs)*2+2)
	args = append(args, whereArgs...)
	args = append(args, whereArgs...)
	args = append(args, q.PageSize, q.Offset())
	return query, args
}

func (s *AdminUsersReadService) List(ctx context.Context, actor AdminPermissionReadActor, q AdminUsersReadQuery) (*AdminUsersReadPage, error) {
	if !q.valid() {
		return nil, ErrAdminPermissionInvalid
	}
	var result *AdminUsersReadPage
	err := s.freshRead(ctx, actor, readIntent{deleted: q.Status == "deleted"}, func(tx *gorm.DB, user, target models.User, e *authz.Evaluator, version int64, policyErr error) error {
		query, args := adminUsersListStatement(user, q)
		rows, err := tx.Raw(query, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		result = &AdminUsersReadPage{Items: make([]UserReadDTO, 0), Page: q.Page, PageSize: q.PageSize}
		for rows.Next() {
			// Scan into nullable columns because an empty page still yields the count.
			var total int64
			var id, guid, plan, role, status, deleted, av, created, last, verified, tokens, calls, limit sql.NullInt64
			var username, nickname sql.NullString
			if err := rows.Scan(&total, &id, &guid, &username, &nickname, &plan, &role, &status, &deleted, &av, &created, &last, &verified, &tokens, &calls, &limit); err != nil {
				return err
			}
			result.Total = total
			if !id.Valid {
				continue
			}
			u := models.User{ID: id.Int64, AuditFields: models.AuditFields{Guid: guid.Int64, CreatedAt: created.Int64, IsDeleted: int(deleted.Int64)}, PlanType: models.PlanType(plan.Int64), Role: models.UserRole(role.Int64), Status: models.UserStatus(status.Int64), AuthVersion: int(av.Int64)}
			if username.Valid {
				u.Username = &username.String
			}
			if nickname.Valid {
				u.Nickname = &nickname.String
			}
			if last.Valid {
				u.LastLoginAt = &last.Int64
			}
			dto, err := ProjectUserRead(u)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, *dto)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (s *AdminUsersReadService) LegacyList(ctx context.Context, actor AdminPermissionReadActor, skip int64, limit int, status string) ([]models.User, error) {
	if skip < 0 || limit < 0 || limit > 100 {
		return nil, ErrAdminPermissionInvalid
	}
	if status != "" && status != "active" && status != "disabled" {
		return nil, ErrAdminPermissionInvalid
	}
	result := make([]models.User, 0)
	err := s.freshRead(ctx, actor, readIntent{legacy: true}, func(tx *gorm.DB, user, target models.User, e *authz.Evaluator, version int64, policyErr error) error {
		if limit == 0 {
			return nil
		}
		where, args := usersReadWhere(user, AdminUsersReadQuery{Status: status})
		if err := tx.Select(userReadColumns).Where(where, args...).Order("created_at desc, guid desc").Offset(int(skip)).Limit(limit).Find(&result).Error; err != nil {
			return err
		}
		for _, u := range result {
			if _, err := ProjectUserRead(u); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
