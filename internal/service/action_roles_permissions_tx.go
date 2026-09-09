package service

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type rolePermissionSessionRevoker interface {
	MarkSessionRevoked(context.Context, string, time.Duration) error
}

// rolePermissionTransactionalExecution adds the transaction-owned locks and
// writes to the pure, request-bound transition planner.
type rolePermissionTransactionalExecution struct {
	base    *RolePermissionExecution
	revoker rolePermissionSessionRevoker
	state   *rolePermissionTransactionalState
}

type rolePermissionTransactionalState struct {
	mu            sync.Mutex
	prelocked     bool
	started       bool
	auditStarted  bool
	factsRecorded bool
	actorGUID     int64
	binding       rolePermissionOperationBinding
	target        models.User
	sessions      []models.Session
	head          *models.PermissionPolicyHead
	rules         []actionsecurity.PermissionOverrideIntent
	facts         rolePermissionAuditFacts
}

type rolePermissionOperationBinding struct {
	id, guid, createdAt, updatedAt                   int64
	createdBy, updatedBy                             int64
	createdBySet, updatedBySet                       bool
	isDeleted                                        int
	actorUserID, actorAuthVersion, sessionID, action int64
	verificationID                                   int64
	state                                            models.AdminOperationState
	publicRef, requestHMAC, idempotencyKeyHMAC       string
	leaseOwnerHMAC                                   string
	leaseExpiresAt, queryExpiresAt                   int64
}

func newRolePermissionTransactionalExecution(base *RolePermissionExecution, revoker rolePermissionSessionRevoker) (*rolePermissionTransactionalExecution, error) {
	if base == nil || !base.validLocalInvariant() || operationInterfaceNil(revoker) {
		return nil, ErrActionOperationUnavailable
	}
	return &rolePermissionTransactionalExecution{base: base, revoker: revoker, state: &rolePermissionTransactionalState{}}, nil
}

func (execution *rolePermissionTransactionalExecution) prelockForAuthorization(ctx context.Context, tx *gorm.DB, operation models.AdminOperation, verification models.AdminActionVerification) (*models.User, error) {
	if execution == nil || execution.base == nil || execution.state == nil || ctx == nil ||
		!validDeleteWriterTransaction(ctx, tx) || !execution.validBinding(operation, verification) {
		return nil, ErrActionOperationForbidden
	}
	db := deleteWriterDB(ctx, tx)
	var actor models.User
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "guid", "role", "status", "is_deleted", "auth_version").
		Where("id = ? AND is_deleted = 0", operation.ActorUserID).First(&actor).Error; err != nil {
		return nil, ErrActionOperationUnavailable
	}
	if actor.ID != operation.ActorUserID || actor.Guid <= 0 || actor.IsDeleted != 0 || actor.AuthVersion != operation.ActorAuthVersion {
		return nil, ErrActionOperationForbidden
	}
	var target models.User
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "guid", "role", "status", "is_deleted", "auth_version").
		Where("guid = ? AND is_deleted = 0", execution.base.targetGUID()).First(&target).Error; err != nil {
		return nil, ErrActionOperationUnavailable
	}
	if target.ID <= 0 || target.Guid != execution.base.targetGUID() || target.IsDeleted != 0 || target.AuthVersion <= 0 {
		return nil, ErrActionOperationForbidden
	}

	var sessions []models.Session
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "guid", "sid", "user_id", "login_method", "session_version", "is_deleted", "revoked_at", "expires_at").
		Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL", target.ID).
		Order("id ASC").Find(&sessions).Error; err != nil {
		return nil, ErrActionOperationUnavailable
	}
	if !validRolePermissionSessions(sessions, target.ID) {
		return nil, ErrActionOperationUnavailable
	}

	var head models.PermissionPolicyHead
	headResult := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "guid", "user_id", "is_deleted", "policy_version", "catalog_version", "rule_count").
		Where("user_id = ? AND is_deleted = 0", target.ID).First(&head)
	var lockedHead *models.PermissionPolicyHead
	if headResult.Error == nil {
		if head.ID <= 0 || head.Guid <= 0 || head.UserID != target.ID || head.IsDeleted != 0 {
			return nil, ErrActionOperationUnavailable
		}
		copyHead := head
		lockedHead = &copyHead
	} else if !errors.Is(headResult.Error, gorm.ErrRecordNotFound) {
		return nil, ErrActionOperationUnavailable
	}

	var rows []models.PermissionOverride
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "guid", "user_id", "is_deleted", "policy_version", "capability", "effect").
		Where("user_id = ? AND is_deleted = 0", target.ID).
		Order("id ASC").Find(&rows).Error; err != nil {
		return nil, ErrActionOperationUnavailable
	}
	rules, ok := rolePermissionLockedRules(rows, target.ID)
	if !ok {
		return nil, ErrActionOperationUnavailable
	}
	if lockedHead != nil {
		if lockedHead.RuleCount != len(rows) {
			return nil, ErrActionOperationUnavailable
		}
		for i := range rows {
			if rows[i].PolicyVersion != lockedHead.PolicyVersion {
				return nil, ErrActionOperationUnavailable
			}
		}
	}

	execution.state.mu.Lock()
	defer execution.state.mu.Unlock()
	if execution.state.prelocked || execution.state.started {
		return nil, ErrActionOperationUnavailable
	}
	execution.state.prelocked = true
	execution.state.actorGUID = actor.Guid
	execution.state.binding = captureRolePermissionOperationBinding(operation)
	execution.state.target = target
	execution.state.sessions = append([]models.Session(nil), sessions...)
	execution.state.head = lockedHead
	execution.state.rules = rules
	copyTarget := target
	return &copyTarget, nil
}

func captureRolePermissionOperationBinding(operation models.AdminOperation) rolePermissionOperationBinding {
	binding := rolePermissionOperationBinding{
		id: operation.ID, guid: operation.Guid, createdAt: operation.CreatedAt, updatedAt: operation.UpdatedAt,
		isDeleted: operation.IsDeleted, actorUserID: operation.ActorUserID, actorAuthVersion: int64(operation.ActorAuthVersion),
		sessionID: operation.SessionID, action: int64(operation.Action), state: operation.State,
		publicRef: operation.PublicRef, requestHMAC: operation.RequestHMAC, idempotencyKeyHMAC: operation.IdempotencyKeyHMAC,
		queryExpiresAt: operation.QueryExpiresAt,
	}
	if operation.CreatedBy != nil {
		binding.createdBy, binding.createdBySet = *operation.CreatedBy, true
	}
	if operation.UpdatedBy != nil {
		binding.updatedBy, binding.updatedBySet = *operation.UpdatedBy, true
	}
	if operation.VerificationID != nil {
		binding.verificationID = *operation.VerificationID
	}
	if operation.LeaseOwnerHMAC != nil {
		binding.leaseOwnerHMAC = *operation.LeaseOwnerHMAC
	}
	if operation.LeaseExpiresAt != nil {
		binding.leaseExpiresAt = *operation.LeaseExpiresAt
	}
	return binding
}

func (binding rolePermissionOperationBinding) matches(operation models.AdminOperation) bool {
	createdBy, createdBySet := int64(0), operation.CreatedBy != nil
	if createdBySet {
		createdBy = *operation.CreatedBy
	}
	updatedBy, updatedBySet := int64(0), operation.UpdatedBy != nil
	if updatedBySet {
		updatedBy = *operation.UpdatedBy
	}
	verificationID, leaseExpiresAt, leaseOwnerHMAC := int64(0), int64(0), ""
	if operation.VerificationID != nil {
		verificationID = *operation.VerificationID
	}
	if operation.LeaseExpiresAt != nil {
		leaseExpiresAt = *operation.LeaseExpiresAt
	}
	if operation.LeaseOwnerHMAC != nil {
		leaseOwnerHMAC = *operation.LeaseOwnerHMAC
	}
	return binding.id == operation.ID && binding.guid == operation.Guid && binding.createdAt == operation.CreatedAt &&
		binding.updatedAt == operation.UpdatedAt && binding.createdBySet == createdBySet && binding.createdBy == createdBy &&
		binding.updatedBySet == updatedBySet && binding.updatedBy == updatedBy && binding.isDeleted == operation.IsDeleted &&
		binding.actorUserID == operation.ActorUserID && binding.actorAuthVersion == int64(operation.ActorAuthVersion) &&
		binding.sessionID == operation.SessionID && binding.action == int64(operation.Action) &&
		binding.verificationID == verificationID && binding.state == operation.State &&
		constantTimeOperationStringEqual(binding.publicRef, operation.PublicRef) &&
		constantTimeOperationStringEqual(binding.requestHMAC, operation.RequestHMAC) &&
		constantTimeOperationStringEqual(binding.idempotencyKeyHMAC, operation.IdempotencyKeyHMAC) &&
		constantTimeOperationStringEqual(binding.leaseOwnerHMAC, leaseOwnerHMAC) &&
		binding.leaseExpiresAt == leaseExpiresAt && binding.queryExpiresAt == operation.QueryExpiresAt
}

func (execution *rolePermissionTransactionalExecution) validBinding(operation models.AdminOperation, verification models.AdminActionVerification) bool {
	if execution == nil || execution.base == nil || !execution.base.validLocalInvariant() {
		return false
	}
	_, publicRefErr := actionsecurity.ParsePublicRef(operation.PublicRef)
	return publicRefErr == nil && operation.ID > 0 && operation.Guid > 0 && operation.CreatedAt > 0 && operation.UpdatedAt >= operation.CreatedAt &&
		operation.CreatedBy != nil && *operation.CreatedBy == operation.ActorUserID && operation.UpdatedBy != nil && *operation.UpdatedBy == operation.ActorUserID &&
		operation.ActorUserID > 0 && operation.ActorAuthVersion > 0 && operation.ActorAuthVersion <= math.MaxInt32 &&
		operation.SessionID > 0 && operation.Action == int(execution.base.invariant.action) &&
		operation.VerificationID != nil && *operation.VerificationID > 0 && operation.State == models.OperationProcessing &&
		operation.IsDeleted == 0 && operation.LeaseOwnerHMAC != nil && len(*operation.LeaseOwnerHMAC) == 64 &&
		operation.LeaseExpiresAt != nil && *operation.LeaseExpiresAt > 0 && operation.QueryExpiresAt > 0 &&
		constantTimeOperationStringEqual(operation.RequestHMAC, execution.base.requestHMAC) &&
		verification.ID == *operation.VerificationID && verification.ActorUserID == operation.ActorUserID &&
		verification.ActorAuthVersion == operation.ActorAuthVersion && verification.SessionID == operation.SessionID &&
		verification.Action == operation.Action && verification.TargetKind == int(actionsecurity.TargetUser) &&
		verification.TargetGUID != nil && *verification.TargetGUID == execution.base.targetGUID() &&
		verification.ConsumedAt == nil && verification.IsDeleted == 0 && verification.ExpiresAt > 0 &&
		constantTimeOperationStringEqual(verification.IntentHMAC, execution.base.requestHMAC)
}

func validRolePermissionSessions(sessions []models.Session, targetID int64) bool {
	var previous int64
	for i := range sessions {
		session := sessions[i]
		if session.ID <= previous || session.Guid <= 0 || session.SID == "" || session.UserID != targetID ||
			session.SessionVersion <= 0 || session.SessionVersion >= math.MaxInt32 || session.IsDeleted != 0 ||
			session.RevokedAt != nil || session.ExpiresAt <= 0 {
			return false
		}
		previous = session.ID
	}
	return true
}

func rolePermissionLockedRules(rows []models.PermissionOverride, targetID int64) ([]actionsecurity.PermissionOverrideIntent, bool) {
	rules := make([]actionsecurity.PermissionOverrideIntent, len(rows))
	var previousID int64
	for i := range rows {
		row := rows[i]
		capability, capabilityOK := models.PermissionCapabilityName(row.Capability)
		if row.ID <= previousID || row.Guid <= 0 || row.UserID != targetID || row.IsDeleted != 0 || row.PolicyVersion <= 0 || !capabilityOK || (row.Effect != 2 && row.Effect != 3) {
			return nil, false
		}
		rules[i] = actionsecurity.PermissionOverrideIntent{Capability: capability, Effect: row.Effect}
		previousID = row.ID
	}
	return rules, true
}

func (execution *rolePermissionTransactionalExecution) Execute(ctx context.Context, tx *gorm.DB, operation models.AdminOperation) (TerminalOutcome, error) {
	if execution == nil || execution.base == nil || execution.state == nil || ctx == nil || !validDeleteWriterTransaction(ctx, tx) {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	execution.state.mu.Lock()
	if execution.state.started || !execution.state.prelocked {
		execution.state.started = true
		execution.state.mu.Unlock()
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	execution.state.started = true
	actorGUID := execution.state.actorGUID
	binding := execution.state.binding
	target := execution.state.target
	sessions := append([]models.Session(nil), execution.state.sessions...)
	head := execution.state.head
	rules := append([]actionsecurity.PermissionOverrideIntent(nil), execution.state.rules...)
	execution.state.sessions = nil
	execution.state.rules = nil
	execution.state.mu.Unlock()
	defer clearRolePermissionSessions(sessions)
	if !binding.matches(operation) || actorGUID <= 0 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if err := execution.base.Begin(); err != nil || operation.Action != int(execution.base.invariant.action) || operation.ActorUserID <= 0 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if target.ID == operation.ActorUserID {
		return rolePermissionTransactionalFailure(models.FailureActionRejected), nil
	}

	targetSnapshot := RolePermissionTargetSnapshot{ActorGUID: actorGUID, TargetGUID: target.Guid, Role: target.Role, Status: target.Status, AuthVersion: target.AuthVersion, Deleted: target.IsDeleted != 0}
	var policySnapshot *RolePermissionPolicySnapshot
	if head != nil {
		policySnapshot = &RolePermissionPolicySnapshot{PolicyVersion: head.PolicyVersion, CatalogVersion: head.CatalogVersion}
	}
	plan, failure := execution.base.PlanTransition(targetSnapshot, policySnapshot, rules)
	if failure != nil {
		return rolePermissionTransactionalFailure(*failure), nil
	}
	if plan == nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if err := execution.recordAuditFacts(target.ID, targetSnapshot, policySnapshot, rules, plan); err != nil {
		return TerminalOutcome{}, err
	}

	now := execution.base.clock.NowMillis()
	if !validOperationNow(now) || operation.CreatedAt > now || operation.UpdatedAt > now {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	for i := range sessions {
		if err := execution.revoker.MarkSessionRevoked(ctx, sessions[i].SID, sessionTTL(sessions[i], now)); err != nil {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
	}
	db := deleteWriterDB(ctx, tx)
	for i := range sessions {
		if err := execution.revokeSession(db, operation.ActorUserID, now, sessions[i]); err != nil {
			return TerminalOutcome{}, err
		}
	}
	updated := db.Model(&models.User{}).
		Where("id = ? AND guid = ? AND is_deleted = 0 AND auth_version = ? AND role = ? AND status = ?", target.ID, target.Guid, target.AuthVersion, target.Role, target.Status).
		Updates(map[string]any{"role": plan.DesiredRole, "auth_version": plan.NextAuthVersion, "updated_at": now, "updated_by": operation.ActorUserID})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if len(rules) > 0 {
		tombstoned := db.Model(&models.PermissionOverride{}).
			Where("user_id = ? AND is_deleted = 0", target.ID).
			Updates(map[string]any{"is_deleted": 1, "updated_at": now, "updated_by": operation.ActorUserID})
		if tombstoned.Error != nil || tombstoned.RowsAffected != int64(len(rules)) {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
	}
	for i := range plan.DesiredRules {
		capability, ok := models.PermissionCapabilityCode(plan.DesiredRules[i].Capability)
		if !ok {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
		guid := execution.base.nextGUID()
		if guid <= 0 {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
		row := models.PermissionOverride{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &operation.ActorUserID, UpdatedAt: now, UpdatedBy: &operation.ActorUserID}, UserID: target.ID, PolicyVersion: plan.NextPolicyVersion, Capability: capability, Effect: plan.DesiredRules[i].Effect}
		created := db.Create(&row)
		if created.Error != nil || created.RowsAffected != 1 {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
	}
	if head == nil {
		guid := execution.base.nextGUID()
		if guid <= 0 {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
		created := db.Create(&models.PermissionPolicyHead{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &operation.ActorUserID, UpdatedAt: now, UpdatedBy: &operation.ActorUserID}, UserID: target.ID, PolicyVersion: plan.NextPolicyVersion, CatalogVersion: plan.CatalogVersion, RuleCount: len(plan.DesiredRules)})
		if created.Error != nil || created.RowsAffected != 1 {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
	} else {
		updatedHead := db.Model(&models.PermissionPolicyHead{}).
			Where("id = ? AND user_id = ? AND is_deleted = 0 AND policy_version = ? AND catalog_version = ?", head.ID, target.ID, plan.ExpectedPolicyVersion, head.CatalogVersion).
			Updates(map[string]any{"policy_version": plan.NextPolicyVersion, "catalog_version": plan.CatalogVersion, "rule_count": len(plan.DesiredRules), "updated_at": now, "updated_by": operation.ActorUserID})
		if updatedHead.Error != nil || updatedHead.RowsAffected != 1 {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
	}
	if err := execution.writeManagedUpdateAudit(db, operation.ActorUserID, target.ID, now); err != nil {
		return TerminalOutcome{}, err
	}

	guid, authVersion, policyVersion, role := target.Guid, plan.NextAuthVersion, plan.NextPolicyVersion, plan.DesiredRole
	return TerminalOutcome{ResultKind: models.ResultUser, ResultGUID: &guid, ResultAuthVersion: &authVersion, ResultPermissionsVersion: &policyVersion, ResultRole: &role, HTTPStatus: 200}, nil
}

func (execution *rolePermissionTransactionalExecution) writeManagedUpdateAudit(db *gorm.DB, actorID, targetID, now int64) error {
	guid := execution.base.nextGUID()
	if guid <= 0 {
		return ErrActionOperationUnavailable
	}
	audit := models.AuthAuditEvent{
		AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID},
		UserID:      &targetID, EventType: models.AuthAuditEventManagedUserUpdated,
	}
	created := db.Create(&audit)
	if created.Error != nil || created.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	return nil
}

func (execution *rolePermissionTransactionalExecution) revokeSession(db *gorm.DB, actorID, now int64, session models.Session) error {
	updated := db.Model(&models.Session{}).
		Where("id = ? AND user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND session_version = ?", session.ID, session.UserID, session.SessionVersion).
		Updates(map[string]any{"revoked_at": now, "session_version": session.SessionVersion + 1, "updated_at": now, "updated_by": actorID})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	guid := execution.base.nextGUID()
	if guid <= 0 {
		return ErrActionOperationUnavailable
	}
	audit := models.AuthAuditEvent{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID}, UserID: &session.UserID, SessionGuid: &session.Guid, EventType: models.AuthAuditEventSessionRevoked, LoginMethod: &session.LoginMethod}
	created := db.Create(&audit)
	if created.Error != nil || created.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	return nil
}

func clearRolePermissionSessions(sessions []models.Session) {
	for i := range sessions {
		sessions[i].SID = ""
	}
}

func rolePermissionTransactionalFailure(failure models.AdminOperationFailure) TerminalOutcome {
	return TerminalOutcome{Failure: &failure, HTTPStatus: 409}
}
