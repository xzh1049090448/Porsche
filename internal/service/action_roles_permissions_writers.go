package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

type rolePermissionAuditFacts struct {
	targetID, targetGUID                    int64
	beforeAuthVersion, afterAuthVersion     int
	beforePolicyVersion, afterPolicyVersion int64
	catalogVersion                          int
	beforeRole, afterRole                   models.UserRole
	beforeRuleCount, afterRuleCount         int
}

var _ TransactionalAuditWriter = (*rolePermissionTransactionalExecution)(nil)

func (execution *rolePermissionTransactionalExecution) recordAuditFacts(targetID int64, target RolePermissionTargetSnapshot,
	policy *RolePermissionPolicySnapshot, activeRules []actionsecurity.PermissionOverrideIntent, plan *RolePermissionTransitionPlan,
) error {
	if execution == nil || execution.base == nil || execution.state == nil || targetID <= 0 || plan == nil || target.TargetGUID != execution.base.targetGUID() ||
		target.AuthVersion <= 0 || plan.NextAuthVersion != target.AuthVersion+1 || plan.NextPolicyVersion <= 0 ||
		plan.CatalogVersion != models.PermissionCatalogVersion || len(activeRules) > len(authz.Catalog()) || len(plan.DesiredRules) > len(authz.Catalog()) {
		return ErrActionOperationUnavailable
	}
	beforePolicy := int64(0)
	if policy != nil {
		if policy.PolicyVersion <= 0 || policy.CatalogVersion != models.PermissionCatalogVersion {
			return ErrActionOperationUnavailable
		}
		beforePolicy = policy.PolicyVersion
	}
	if plan.ExpectedPolicyVersion != beforePolicy || plan.NextPolicyVersion != beforePolicy+1 {
		return ErrActionOperationUnavailable
	}
	facts := rolePermissionAuditFacts{targetID: targetID, targetGUID: target.TargetGUID, beforeAuthVersion: target.AuthVersion,
		afterAuthVersion: plan.NextAuthVersion, beforePolicyVersion: beforePolicy, afterPolicyVersion: plan.NextPolicyVersion,
		catalogVersion: plan.CatalogVersion, beforeRole: target.Role, afterRole: plan.DesiredRole,
		beforeRuleCount: len(activeRules), afterRuleCount: len(plan.DesiredRules)}
	execution.state.mu.Lock()
	defer execution.state.mu.Unlock()
	if execution.state.factsRecorded || execution.state.auditStarted {
		return ErrActionOperationUnavailable
	}
	execution.state.facts = facts
	execution.state.factsRecorded = true
	return nil
}

func (execution *rolePermissionTransactionalExecution) Write(ctx context.Context, tx *gorm.DB, event ActionAuditEvent) error {
	if execution == nil || execution.base == nil || execution.state == nil || !validDeleteWriterTransaction(ctx, tx) ||
		event.Action != execution.base.invariant.action || event.TargetGUID == nil || *event.TargetGUID != execution.base.targetGUID() ||
		!validRolePermissionTerminalEnvelope(event.PublicRef, event.ActorGUID, event.SessionGUID, event.Action, event.TargetKind,
			event.TargetGUID, event.State, event.Failure, event.ResultKind, event.ResultGUID, event.OccurredAt, execution.base.clock) {
		return ErrActionOperationUnavailable
	}
	execution.state.mu.Lock()
	if execution.state.auditStarted || (event.State == models.OperationSucceeded && !execution.state.factsRecorded) {
		execution.state.mu.Unlock()
		return ErrActionOperationUnavailable
	}
	execution.state.auditStarted = true
	facts, hasFacts := execution.state.facts, execution.state.factsRecorded
	execution.state.mu.Unlock()
	if hasFacts && facts.targetGUID != *event.TargetGUID {
		return ErrActionOperationUnavailable
	}

	binding, err := loadRolePermissionWriterBinding(deleteWriterDB(ctx, tx), event.PublicRef, event.ActorGUID, event.SessionGUID, event.Action, *event.TargetGUID, execution.base.requestHMAC)
	if err != nil {
		return ErrActionOperationUnavailable
	}
	guid := execution.base.nextGUID()
	if guid <= 0 {
		return ErrActionOperationUnavailable
	}
	actorID := binding.actorUserID
	resource := "user:" + strconv.FormatInt(*event.TargetGUID, 10)
	detail := models.JSONMap{"operation_ref": event.PublicRef, "actor_guid": event.ActorGUID, "target_guid": *event.TargetGUID, "reason": execution.reason()}
	var targetID *int64
	if hasFacts {
		targetID = &facts.targetID
		detail["before_role"], detail["after_role"] = facts.beforeRole.String(), facts.afterRole.String()
		detail["before_auth_version"], detail["after_auth_version"] = facts.beforeAuthVersion, facts.afterAuthVersion
		detail["before_permissions_version"], detail["after_permissions_version"] = facts.beforePolicyVersion, facts.afterPolicyVersion
		detail["catalog_version"] = facts.catalogVersion
		detail["before_rule_count"], detail["after_rule_count"] = facts.beforeRuleCount, facts.afterRuleCount
	}
	row := models.AuditLog{AuditFields: models.AuditFields{Guid: guid, CreatedAt: event.OccurredAt, CreatedBy: &actorID,
		UpdatedAt: event.OccurredAt, UpdatedBy: &actorID}, UserID: targetID, Action: execution.base.descriptor.Name,
		Resource: &resource, Detail: detail}
	created := deleteWriterDB(ctx, tx).Create(&row)
	if created.Error != nil || created.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	return nil
}

func (execution *rolePermissionTransactionalExecution) reason() string {
	if execution == nil || execution.base == nil {
		return ""
	}
	switch {
	case execution.base.intent.Promote != nil:
		return strings.Clone(execution.base.intent.Promote.Reason)
	case execution.base.intent.Demote != nil:
		return strings.Clone(execution.base.intent.Demote.Reason)
	case execution.base.intent.PermissionsWrite != nil:
		return strings.Clone(execution.base.intent.PermissionsWrite.Reason)
	default:
		return ""
	}
}

// RolePermissionOutboxWriter persists terminal A08 delivery facts through the
// transaction owned by ActionOperationService.
type RolePermissionOutboxWriter struct {
	nextGUID func() int64
	clock    persistence.Clock
}

var _ TransactionalOutboxWriter = (*RolePermissionOutboxWriter)(nil)

func NewRolePermissionOutboxWriter(nextGUID func() int64, clock persistence.Clock) (*RolePermissionOutboxWriter, error) {
	if nextGUID == nil || operationInterfaceNil(clock) {
		return nil, ErrActionOperationUnavailable
	}
	return &RolePermissionOutboxWriter{nextGUID: nextGUID, clock: clock}, nil
}

func (writer *RolePermissionOutboxWriter) Write(ctx context.Context, tx *gorm.DB, event ActionOutboxEvent) error {
	if writer == nil || !validDeleteWriterTransaction(ctx, tx) || !validRolePermissionTerminalEnvelope(event.PublicRef, event.ActorGUID, event.SessionGUID,
		event.Action, event.TargetKind, event.TargetGUID, event.State, event.Failure, event.ResultKind, event.ResultGUID, event.OccurredAt, writer.clock) {
		return ErrActionOperationUnavailable
	}
	binding, err := loadRolePermissionWriterBinding(deleteWriterDB(ctx, tx), event.PublicRef, event.ActorGUID, event.SessionGUID, event.Action, *event.TargetGUID, "")
	if err != nil {
		return ErrActionOperationUnavailable
	}
	guid := writer.nextGUID()
	if guid <= 0 {
		return ErrActionOperationUnavailable
	}
	actorID := binding.actorUserID
	row := models.AdminActionOutbox{
		AuditFields: models.AuditFields{Guid: guid, CreatedAt: event.OccurredAt, CreatedBy: &actorID, UpdatedAt: event.OccurredAt, UpdatedBy: &actorID},
		OperationID: binding.operationID, PublicRef: event.PublicRef, Action: int(event.Action), TargetKind: int(event.TargetKind),
		TargetGUID: copyInt64(event.TargetGUID), State: event.State, FailureCode: copyOperationFailure(event.Failure),
		ResultKind: copyResultKind(event.ResultKind), ResultGUID: copyInt64(event.ResultGUID), DeliveryState: models.DeliveryPending,
		AvailableAt: event.OccurredAt,
	}
	created := deleteWriterDB(ctx, tx).Create(&row)
	if created.Error != nil || created.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	return nil
}

func validRolePermissionTerminalEnvelope(publicRef string, actorGUID, sessionGUID int64, action actionsecurity.Action,
	targetKind actionsecurity.TargetKind, targetGUID *int64, state models.AdminOperationState, failure *models.AdminOperationFailure,
	resultKind *models.AdminResultKind, resultGUID *int64, occurredAt int64, clock persistence.Clock,
) bool {
	if _, err := actionsecurity.ParsePublicRef(publicRef); err != nil || actorGUID <= 0 || sessionGUID <= 0 ||
		!isA08RolePermissionAction(action) || targetKind != actionsecurity.TargetUser || targetGUID == nil || *targetGUID <= 0 ||
		occurredAt <= 0 || operationInterfaceNil(clock) {
		return false
	}
	switch state {
	case models.OperationSucceeded:
		if failure != nil || resultKind == nil || *resultKind != models.ResultUser || resultGUID == nil || *resultGUID != *targetGUID {
			return false
		}
	case models.OperationFailed:
		if !validDeleteFailure(failure) || resultKind != nil || resultGUID != nil {
			return false
		}
	default:
		return false
	}
	now := clock.NowMillis()
	return now > 0 && occurredAt <= now
}

func loadRolePermissionWriterBinding(db *gorm.DB, publicRef string, actorGUID, sessionGUID int64, action actionsecurity.Action, targetGUID int64, expectedRequestHMAC string) (deleteWriterBinding, error) {
	if !isA08RolePermissionAction(action) {
		return deleteWriterBinding{}, ErrActionOperationUnavailable
	}
	var operation models.AdminOperation
	if err := db.Select("id", "actor_user_id", "actor_auth_version", "session_id", "action", "verification_id", "state", "public_ref", "request_hmac").
		Where("public_ref = ? AND is_deleted = 0", publicRef).First(&operation).Error; err != nil || operation.ID <= 0 || operation.ActorUserID <= 0 ||
		operation.ActorAuthVersion <= 0 || operation.SessionID <= 0 || operation.Action != int(action) || operation.VerificationID == nil ||
		*operation.VerificationID <= 0 || operation.State != models.OperationProcessing || len(operation.RequestHMAC) != 64 ||
		!constantTimeOperationStringEqual(operation.PublicRef, publicRef) ||
		(expectedRequestHMAC != "" && (len(expectedRequestHMAC) != 64 || !constantTimeOperationStringEqual(operation.RequestHMAC, expectedRequestHMAC))) {
		return deleteWriterBinding{}, ErrActionOperationUnavailable
	}
	var actor models.User
	if err := db.Select("id", "guid").Where("id = ? AND is_deleted = 0", operation.ActorUserID).First(&actor).Error; err != nil ||
		actor.ID != operation.ActorUserID || actor.Guid != actorGUID {
		return deleteWriterBinding{}, ErrActionOperationUnavailable
	}
	var verification models.AdminActionVerification
	if err := db.Select("id", "actor_user_id", "actor_auth_version", "session_id", "action", "target_kind", "target_guid", "intent_hmac", "consumed_at", "is_deleted").
		Where("id = ?", *operation.VerificationID).First(&verification).Error; err != nil || verification.ID != *operation.VerificationID ||
		verification.ActorUserID != operation.ActorUserID || verification.ActorAuthVersion != operation.ActorAuthVersion || verification.SessionID != operation.SessionID ||
		verification.Action != int(action) || verification.TargetKind != int(actionsecurity.TargetUser) || verification.TargetGUID == nil ||
		*verification.TargetGUID != targetGUID || len(verification.IntentHMAC) != 64 || !constantTimeOperationStringEqual(verification.IntentHMAC, operation.RequestHMAC) ||
		verification.ConsumedAt == nil || *verification.ConsumedAt <= 0 || verification.IsDeleted != 1 {
		return deleteWriterBinding{}, ErrActionOperationUnavailable
	}
	var session models.Session
	if err := db.Select("id", "guid", "user_id").Where("id = ?", operation.SessionID).First(&session).Error; err != nil ||
		session.ID != operation.SessionID || session.Guid != sessionGUID || session.UserID != operation.ActorUserID {
		return deleteWriterBinding{}, ErrActionOperationUnavailable
	}
	return deleteWriterBinding{operationID: operation.ID, actorUserID: operation.ActorUserID}, nil
}
