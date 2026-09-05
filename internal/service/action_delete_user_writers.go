package service

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DeleteUserExecution owns the reviewed users.delete request and the audit
// facts produced by its single transactional consumer invocation.
type DeleteUserExecution struct {
	intent   actionsecurity.DeleteUserIntent
	nextGUID func() int64
	clock    persistence.Clock
	state    *deleteUserExecutionState
}

type deleteUserExecutionState struct {
	mu              sync.Mutex
	facts           deleteUserAuditFacts
	factsRecorded   bool
	consumerStarted bool
	auditStarted    bool
}

var _ TransactionalAuditWriter = (*DeleteUserExecution)(nil)

type deleteUserAuditFacts struct {
	targetID     int64
	targetGUID   int64
	beforeStatus models.UserStatus
}

// NewDeleteUserExecution validates and takes an immutable normalized copy of
// the active users.delete intent. The returned value is bound to one request.
func NewDeleteUserExecution(intent actionsecurity.DeleteUserIntent, nextGUID func() int64, clock persistence.Clock) (*DeleteUserExecution, error) {
	descriptor, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersDelete)
	if !ok {
		return nil, ErrActionOperationUnavailable
	}
	return newDeleteUserExecution(descriptor, intent, nextGUID, clock)
}

func newDeleteUserExecution(descriptor actionsecurity.Descriptor, intent actionsecurity.DeleteUserIntent, nextGUID func() int64, clock persistence.Clock) (*DeleteUserExecution, error) {
	if descriptor.Action != actionsecurity.ActionUsersDelete || descriptor.Name != "users.delete" ||
		descriptor.Capability != "users.delete" || descriptor.RootOnly || !descriptor.RequiresTicket || !descriptor.Active ||
		descriptor.TargetKind != actionsecurity.TargetUser || descriptor.Encode == nil || nextGUID == nil || operationInterfaceNil(clock) {
		return nil, ErrActionOperationUnavailable
	}
	encoded, err := descriptor.Encode(intent)
	clear(encoded)
	if err != nil {
		return nil, ErrActionOperationUnavailable
	}
	owned := actionsecurity.DeleteUserIntent{
		TargetGUID:          intent.TargetGUID,
		ExpectedAuthVersion: intent.ExpectedAuthVersion,
		Reason:              strings.Clone(strings.TrimSpace(intent.Reason)),
	}
	return &DeleteUserExecution{intent: owned, nextGUID: nextGUID, clock: clock, state: &deleteUserExecutionState{}}, nil
}

func (execution *DeleteUserExecution) String() string {
	if execution == nil {
		return "DeleteUserExecution<nil>"
	}
	return "DeleteUserExecution{Action:users.delete,TargetGUID:" + strconv.FormatInt(execution.intent.TargetGUID, 10) + "}"
}

func (execution DeleteUserExecution) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "DeleteUserExecution{Action:users.delete,TargetGUID:"+strconv.FormatInt(execution.intent.TargetGUID, 10)+"}")
}

func (DeleteUserExecution) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// recordAuditFacts is the narrow same-package handoff from the transactional
// consumer. Invalid and repeated recordings fail closed without replacing the
// original facts.
func (execution *DeleteUserExecution) recordAuditFacts(targetID, targetGUID int64, beforeStatus models.UserStatus) error {
	if execution == nil || execution.state == nil || targetID <= 0 || targetGUID <= 0 ||
		(beforeStatus != models.UserStatusActive && beforeStatus != models.UserStatusDisabled) {
		return ErrActionOperationUnavailable
	}
	execution.state.mu.Lock()
	defer execution.state.mu.Unlock()
	if execution.state.factsRecorded || execution.state.auditStarted || targetGUID != execution.intent.TargetGUID {
		return ErrActionOperationUnavailable
	}
	execution.state.facts = deleteUserAuditFacts{targetID: targetID, targetGUID: targetGUID, beforeStatus: beforeStatus}
	execution.state.factsRecorded = true
	return nil
}

// Write persists a terminal deletion audit in the caller's transaction.
func (execution *DeleteUserExecution) Write(ctx context.Context, tx *gorm.DB, event ActionAuditEvent) error {
	if execution == nil || execution.state == nil || !validDeleteWriterTransaction(ctx, tx) {
		return ErrActionOperationUnavailable
	}
	execution.state.mu.Lock()
	if execution.state.auditStarted || !validDeleteAuditEvent(event, execution.intent.TargetGUID, execution.clock) ||
		(event.State == models.OperationSucceeded && !execution.state.factsRecorded) {
		execution.state.mu.Unlock()
		return ErrActionOperationUnavailable
	}
	execution.state.auditStarted = true
	facts := execution.state.facts
	hasFacts := execution.state.factsRecorded
	intent := execution.intent
	execution.state.mu.Unlock()

	db := deleteWriterDB(ctx, tx)
	binding, err := loadDeleteWriterBinding(db, event.PublicRef, event.ActorGUID, event.SessionGUID, intent.TargetGUID)
	if err != nil {
		return ErrActionOperationUnavailable
	}
	guid := execution.nextGUID()
	if guid <= 0 {
		return ErrActionOperationUnavailable
	}
	actorID := binding.actorUserID
	resource := "user:" + strconv.FormatInt(intent.TargetGUID, 10)
	detail := models.JSONMap{
		"operation_ref": event.PublicRef,
		"actor_guid":    event.ActorGUID,
		"target_guid":   intent.TargetGUID,
		"reason":        intent.Reason,
	}
	var targetID *int64
	if hasFacts {
		targetID = &facts.targetID
		detail["before_status"] = facts.beforeStatus.String()
		if event.State == models.OperationSucceeded {
			detail["after_status"] = "deleted"
		}
	}
	row := models.AuditLog{
		AuditFields: models.AuditFields{Guid: guid, CreatedAt: event.OccurredAt, CreatedBy: &actorID,
			UpdatedAt: event.OccurredAt, UpdatedBy: &actorID, IsDeleted: 0},
		UserID:   targetID,
		Action:   "users.delete",
		Resource: &resource,
		Detail:   detail,
		IP:       nil,
	}
	if err := db.Create(&row).Error; err != nil {
		return ErrActionOperationUnavailable
	}
	return nil
}

// AdminActionOutboxWriter persists terminal users.delete delivery records.
// It is stateless and may be shared; database uniqueness owns replay defense.
type AdminActionOutboxWriter struct {
	nextGUID func() int64
	clock    persistence.Clock
}

var _ TransactionalOutboxWriter = (*AdminActionOutboxWriter)(nil)

func NewAdminActionOutboxWriter(nextGUID func() int64, clock persistence.Clock) (*AdminActionOutboxWriter, error) {
	if nextGUID == nil || operationInterfaceNil(clock) {
		return nil, ErrActionOperationUnavailable
	}
	return &AdminActionOutboxWriter{nextGUID: nextGUID, clock: clock}, nil
}

// Write persists one pending outbox row in the caller's transaction.
func (writer *AdminActionOutboxWriter) Write(ctx context.Context, tx *gorm.DB, event ActionOutboxEvent) error {
	if writer == nil || !validDeleteWriterTransaction(ctx, tx) || !validDeleteOutboxEvent(event, writer.clock) {
		return ErrActionOperationUnavailable
	}
	db := deleteWriterDB(ctx, tx)
	binding, err := loadDeleteWriterBinding(db, event.PublicRef, event.ActorGUID, event.SessionGUID, *event.TargetGUID)
	if err != nil {
		return ErrActionOperationUnavailable
	}
	guid := writer.nextGUID()
	if guid <= 0 {
		return ErrActionOperationUnavailable
	}
	actorID := binding.actorUserID
	row := models.AdminActionOutbox{
		AuditFields: models.AuditFields{Guid: guid, CreatedAt: event.OccurredAt, CreatedBy: &actorID,
			UpdatedAt: event.OccurredAt, UpdatedBy: &actorID, IsDeleted: 0},
		OperationID: binding.operationID, PublicRef: event.PublicRef, Action: int(event.Action),
		TargetKind: int(event.TargetKind), TargetGUID: copyInt64(event.TargetGUID), State: event.State,
		ResultGUID: copyInt64(event.ResultGUID), DeliveryState: models.DeliveryPending, AvailableAt: event.OccurredAt,
		DeliveredAt: nil, AttemptCount: 0,
	}
	if err := db.Create(&row).Error; err != nil {
		return ErrActionOperationUnavailable
	}
	return nil
}

type deleteWriterBinding struct {
	operationID int64
	actorUserID int64
}

func loadDeleteWriterBinding(db *gorm.DB, publicRef string, actorGUID, sessionGUID, targetGUID int64) (deleteWriterBinding, error) {
	var operation models.AdminOperation
	if err := db.Select("id", "actor_user_id", "actor_auth_version", "session_id", "action", "verification_id", "state", "public_ref").
		Where("public_ref = ? AND is_deleted = 0", publicRef).First(&operation).Error; err != nil ||
		operation.ID <= 0 || operation.ActorUserID <= 0 || operation.ActorAuthVersion <= 0 || operation.SessionID <= 0 ||
		operation.Action != int(actionsecurity.ActionUsersDelete) ||
		operation.VerificationID == nil || *operation.VerificationID <= 0 || operation.State != models.OperationProcessing ||
		!constantTimeOperationStringEqual(operation.PublicRef, publicRef) {
		return deleteWriterBinding{}, ErrActionOperationUnavailable
	}
	var actor models.User
	if err := db.Select("id", "guid").Where("id = ? AND is_deleted = 0", operation.ActorUserID).First(&actor).Error; err != nil ||
		actor.ID != operation.ActorUserID || actor.Guid <= 0 || actor.Guid != actorGUID {
		return deleteWriterBinding{}, ErrActionOperationUnavailable
	}
	var verification models.AdminActionVerification
	if err := db.Select("id", "actor_user_id", "actor_auth_version", "session_id", "action", "target_kind", "target_guid", "consumed_at", "is_deleted").
		Where("id = ?", *operation.VerificationID).First(&verification).Error; err != nil ||
		verification.ID != *operation.VerificationID || verification.ActorUserID != operation.ActorUserID ||
		verification.ActorAuthVersion != operation.ActorAuthVersion || verification.SessionID != operation.SessionID ||
		verification.Action != int(actionsecurity.ActionUsersDelete) || verification.TargetKind != int(actionsecurity.TargetUser) ||
		verification.TargetGUID == nil || *verification.TargetGUID != targetGUID || verification.ConsumedAt == nil ||
		*verification.ConsumedAt <= 0 || verification.IsDeleted != 1 {
		return deleteWriterBinding{}, ErrActionOperationUnavailable
	}
	var session models.Session
	if err := db.Select("id", "guid", "user_id").Where("id = ?", operation.SessionID).First(&session).Error; err != nil ||
		session.ID != operation.SessionID || session.Guid <= 0 || session.Guid != sessionGUID || session.UserID != operation.ActorUserID {
		return deleteWriterBinding{}, ErrActionOperationUnavailable
	}
	return deleteWriterBinding{operationID: operation.ID, actorUserID: operation.ActorUserID}, nil
}

func validDeleteAuditEvent(event ActionAuditEvent, targetGUID int64, clock persistence.Clock) bool {
	return validDeleteTerminalEnvelope(event.PublicRef, event.ActorGUID, event.SessionGUID, event.Action, event.TargetKind,
		event.TargetGUID, event.State, event.Failure, event.ResultKind, event.ResultGUID, event.OccurredAt, targetGUID, clock)
}

func validDeleteOutboxEvent(event ActionOutboxEvent, clock persistence.Clock) bool {
	if event.TargetGUID == nil {
		return false
	}
	return validDeleteTerminalEnvelope(event.PublicRef, event.ActorGUID, event.SessionGUID, event.Action, event.TargetKind,
		event.TargetGUID, event.State, event.Failure, event.ResultKind, event.ResultGUID, event.OccurredAt, *event.TargetGUID, clock)
}

func validDeleteTerminalEnvelope(publicRef string, actorGUID, sessionGUID int64, action actionsecurity.Action,
	targetKind actionsecurity.TargetKind, targetGUID *int64, state models.AdminOperationState, failure *models.AdminOperationFailure,
	resultKind *models.AdminResultKind, resultGUID *int64, occurredAt, expectedTargetGUID int64, clock persistence.Clock,
) bool {
	if _, err := actionsecurity.ParsePublicRef(publicRef); err != nil || actorGUID <= 0 || sessionGUID <= 0 ||
		action != actionsecurity.ActionUsersDelete || targetKind != actionsecurity.TargetUser || targetGUID == nil ||
		*targetGUID <= 0 || *targetGUID != expectedTargetGUID || occurredAt <= 0 || operationInterfaceNil(clock) {
		return false
	}
	switch state {
	case models.OperationSucceeded:
		if failure != nil || resultKind == nil || *resultKind != models.ResultUser || resultGUID == nil || *resultGUID != expectedTargetGUID {
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

func validDeleteFailure(failure *models.AdminOperationFailure) bool {
	if failure == nil {
		return false
	}
	switch *failure {
	case models.FailureActionRejected, models.FailureTargetVersionConflict, models.FailurePolicyVersionConflict,
		models.FailureTargetStateConflict, models.FailureConsumerValidation:
		return true
	default:
		return false
	}
}

func validDeleteWriterTransaction(ctx context.Context, tx *gorm.DB) bool {
	if ctx == nil || tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil || tx.Error != nil {
		return false
	}
	switch tx.Statement.ConnPool.(type) {
	case *sql.Tx, *gorm.PreparedStmtTX:
		return true
	default:
		return false
	}
}

func deleteWriterDB(ctx context.Context, tx *gorm.DB) *gorm.DB {
	return tx.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx)
}
