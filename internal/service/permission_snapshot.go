package service

import (
	"context"
	"database/sql"
	"errors"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrPermissionReadUnavailable  = errors.New("permission snapshot unavailable")
	ErrPermissionActorUnavailable = errors.New("permission actor unavailable")
	ErrPermissionPolicyInvalid    = errors.New("permission policy invalid")
)

// PermissionSnapshot is valid only as of its loader transaction; callers must not cache it as a fresh authorization credential.
type PermissionSnapshot struct {
	evaluator     *authz.Evaluator
	authVersion   int
	policyVersion int64
}

func (s *PermissionSnapshot) Evaluator() *authz.Evaluator {
	if s == nil {
		return nil
	}
	return s.evaluator
}
func (s *PermissionSnapshot) AuthVersion() int {
	if s == nil {
		return 0
	}
	return s.authVersion
}
func (s *PermissionSnapshot) PolicyVersion() int64 {
	if s == nil {
		return 0
	}
	return s.policyVersion
}
func (s *PermissionSnapshot) CatalogVersion() int {
	if s == nil {
		return 0
	}
	return models.PermissionCatalogVersion
}

// LoadPermissionSnapshot accepts only the root SQL pool and owns a READ COMMITTED
// transaction. Future writers must lock users FOR UPDATE, then head and rules;
// this as-of-load snapshot cannot authorize a later mutation without fresh locks.
func LoadPermissionSnapshot(ctx context.Context, root *gorm.DB, userID int64) (*PermissionSnapshot, error) {
	if ctx == nil || root == nil || root.Statement == nil || userID <= 0 {
		return nil, ErrPermissionReadUnavailable
	}
	if _, ok := root.Statement.ConnPool.(*sql.DB); !ok {
		return nil, ErrPermissionReadUnavailable
	}
	var result *PermissionSnapshot
	err := root.Session(&gorm.Session{NewDB: true, Logger: logger.Discard}).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id", "guid", "role", "status", "is_deleted", "auth_version").Where("id = ?", userID).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPermissionActorUnavailable
			}
			return ErrPermissionReadUnavailable
		}
		actor := authz.Account{ID: user.ID, GUID: user.Guid, Role: user.Role, Status: user.Status, IsDeleted: user.IsDeleted}
		if user.AuthVersion <= 0 {
			return ErrPermissionActorUnavailable
		}
		if _, err := authz.NewEvaluator(actor, nil); err != nil {
			return ErrPermissionActorUnavailable
		}
		policyVersion, rules, err := readPermissionPolicyRows(tx, userID)
		if err != nil {
			return err
		}

		e, err := authz.NewEvaluator(actor, rules)
		if err != nil {
			return ErrPermissionPolicyInvalid
		}
		result = &PermissionSnapshot{evaluator: e, authVersion: user.AuthVersion, policyVersion: policyVersion}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		if errors.Is(err, ErrPermissionActorUnavailable) || errors.Is(err, ErrPermissionPolicyInvalid) {
			return nil, err
		}
		return nil, ErrPermissionReadUnavailable
	}
	if result == nil {
		return nil, ErrPermissionReadUnavailable
	}
	return result, nil
}

// readPermissionPolicyRows is private: the caller must hold the subject user
// SHARE lock in a root-owned READ COMMITTED transaction. History is inspected
// deliberately to reject orphaned or corrupt policy data, including tombstones.
func readPermissionPolicyRows(tx *gorm.DB, userID int64) (int64, []authz.Override, error) {
	var head models.PermissionPolicyHead
	err := tx.Select("id", "guid", "is_deleted", "policy_version", "catalog_version", "rule_count").Where("user_id = ?", userID).First(&head).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var rows []models.PermissionOverride
		if err := tx.Select("id").Where("user_id = ?", userID).Limit(1).Find(&rows).Error; err != nil {
			return 0, nil, ErrPermissionReadUnavailable
		}
		if len(rows) != 0 {
			return 0, nil, ErrPermissionPolicyInvalid
		}
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, ErrPermissionReadUnavailable
	}
	if head.ID <= 0 || head.Guid <= 0 || head.IsDeleted != 0 || head.PolicyVersion <= 0 || head.CatalogVersion != models.PermissionCatalogVersion || head.RuleCount < 0 || head.RuleCount > len(authz.Catalog()) {
		return 0, nil, ErrPermissionPolicyInvalid
	}
	var rows []models.PermissionOverride
	if err := tx.Select("id", "guid", "is_deleted", "policy_version", "capability", "effect").Where("user_id = ? AND is_deleted <> 1", userID).Order("capability").Limit(len(authz.Catalog()) + 1).Find(&rows).Error; err != nil {
		return 0, nil, ErrPermissionReadUnavailable
	}
	if len(rows) != head.RuleCount {
		return 0, nil, ErrPermissionPolicyInvalid
	}
	rules := make([]authz.Override, 0, len(rows))
	for _, row := range rows {
		if row.ID <= 0 || row.Guid <= 0 || row.IsDeleted != 0 || row.PolicyVersion != head.PolicyVersion {
			return 0, nil, ErrPermissionPolicyInvalid
		}
		name, ok := models.PermissionCapabilityName(row.Capability)
		if !ok {
			return 0, nil, ErrPermissionPolicyInvalid
		}
		effect, ok := models.PermissionEffectName(row.Effect)
		if !ok {
			return 0, nil, ErrPermissionPolicyInvalid
		}
		rules = append(rules, authz.Override{Capability: name, Effect: authz.Effect(effect)})
	}
	return head.PolicyVersion, rules, nil
}
