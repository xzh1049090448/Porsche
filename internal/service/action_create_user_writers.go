package service

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var _ TransactionalAuditWriter = (*CreateAccountExecution)(nil)

// Write persists the management audit after a create consumer outcome has
// been validated by ActionOperationService.Execute.
func (execution *CreateAccountExecution) Write(ctx context.Context, tx *gorm.DB, event ActionAuditEvent) error {
	if execution == nil || execution.state == nil || !validCreateWriterTransaction(ctx, tx) || !execution.validAuditEvent(event) {
		return ErrActionOperationUnavailable
	}
	execution.state.mu.Lock()
	if execution.state.auditStarted {
		execution.state.mu.Unlock()
		return ErrActionOperationUnavailable
	}
	execution.state.auditStarted = true
	resultRecorded := execution.state.resultRecorded
	user := copyCreateAccountUser(execution.state.resultUser)
	group := execution.state.resultGroup
	execution.state.mu.Unlock()
	if event.State == models.OperationSucceeded && (!resultRecorded || event.ResultGUID == nil || user.ID <= 0 || user.Guid != *event.ResultGUID) {
		return ErrActionOperationUnavailable
	}

	db := createWriterDB(ctx, tx)
	binding, err := loadCreateWriterBinding(db, event.PublicRef, event.ActorGUID, event.SessionGUID, event.Action)
	if err != nil {
		return ErrActionOperationUnavailable
	}
	guid := execution.nextGUID()
	if guid <= 0 {
		return ErrActionOperationUnavailable
	}
	actorID := binding.actorUserID
	detail := models.JSONMap{
		"role":                 execution.intent.Role,
		"plan":                 models.PlanType(execution.intent.PlanType).String(),
		"permission_overrides": createAuditOverrides(execution.intent.Overrides),
		"request_id":           execution.metadata.RequestID,
		"operation_ref":        event.PublicRef,
		"terminal_state":       event.State.String(),
		"failure_code":         nil,
	}
	resourceValue := "users"
	if event.State == models.OperationSucceeded {
		resourceValue += "/" + strconv.FormatInt(user.Guid, 10)
		detail["target_guid"] = user.Guid
		detail["group_key"] = group.Key
	} else {
		detail["failure_code"] = event.Failure.String()
	}
	ip := strings.Clone(execution.metadata.TrustedIP)
	row := models.AuditLog{
		AuditFields: models.AuditFields{Guid: guid, CreatedAt: event.OccurredAt, CreatedBy: &actorID, UpdatedAt: event.OccurredAt, UpdatedBy: &actorID},
		UserID:      &actorID, Action: execution.descriptor.Name, Resource: &resourceValue, Detail: detail, IP: &ip,
	}
	created := db.Create(&row)
	if created.Error != nil || created.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	return nil
}

func (execution *CreateAccountExecution) validAuditEvent(event ActionAuditEvent) bool {
	if execution == nil || operationInterfaceNil(execution.clock) || event.ActorGUID <= 0 || event.SessionGUID <= 0 ||
		event.Action != execution.descriptor.Action || event.TargetKind != actionsecurity.TargetNone || event.TargetGUID != nil || event.OccurredAt <= 0 {
		return false
	}
	if _, err := actionsecurity.ParsePublicRef(event.PublicRef); err != nil {
		return false
	}
	now := execution.clock.NowMillis()
	if now <= 0 || event.OccurredAt > now {
		return false
	}
	switch event.State {
	case models.OperationSucceeded:
		return event.Failure == nil && event.ResultKind != nil && *event.ResultKind == models.ResultUser && event.ResultGUID != nil && *event.ResultGUID > 0
	case models.OperationFailed:
		return validCreateFailure(event.Failure) && event.ResultKind == nil && event.ResultGUID == nil
	default:
		return false
	}
}

func createAuditOverrides(overrides []actionsecurity.PermissionOverrideIntent) []models.JSONMap {
	out := make([]models.JSONMap, 0, len(overrides))
	for _, override := range overrides {
		effect, _ := models.PermissionEffectName(override.Effect)
		out = append(out, models.JSONMap{"capability": override.Capability, "effect": effect})
	}
	return out
}

// CreateAccountOutboxWriter persists terminal create delivery facts with the
// descriptor's TargetNone encoded as a NULL target_guid.
type CreateAccountOutboxWriter struct {
	nextGUID func() int64
	clock    persistence.Clock
}

var _ TransactionalOutboxWriter = (*CreateAccountOutboxWriter)(nil)

func NewCreateAccountOutboxWriter(nextGUID func() int64, clock persistence.Clock) (*CreateAccountOutboxWriter, error) {
	if nextGUID == nil || operationInterfaceNil(clock) {
		return nil, ErrActionOperationUnavailable
	}
	return &CreateAccountOutboxWriter{nextGUID: nextGUID, clock: clock}, nil
}

func (writer *CreateAccountOutboxWriter) Write(ctx context.Context, tx *gorm.DB, event ActionOutboxEvent) error {
	if writer == nil || !validCreateWriterTransaction(ctx, tx) || !validCreateOutboxEvent(event, writer.clock) {
		return ErrActionOperationUnavailable
	}
	db := createWriterDB(ctx, tx)
	binding, err := loadCreateWriterBinding(db, event.PublicRef, event.ActorGUID, event.SessionGUID, event.Action)
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
		OperationID: binding.operationID, PublicRef: event.PublicRef, Action: int(event.Action), TargetKind: int(actionsecurity.TargetNone), TargetGUID: nil,
		State: event.State, FailureCode: copyOperationFailure(event.Failure), ResultKind: copyResultKind(event.ResultKind), ResultGUID: copyInt64(event.ResultGUID), DeliveryState: models.DeliveryPending, AvailableAt: event.OccurredAt,
	}
	created := db.Create(&row)
	if created.Error != nil || created.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	return nil
}

func validCreateOutboxEvent(event ActionOutboxEvent, clock persistence.Clock) bool {
	if operationInterfaceNil(clock) || event.ActorGUID <= 0 || event.SessionGUID <= 0 || event.TargetKind != actionsecurity.TargetNone || event.TargetGUID != nil ||
		(event.Action != actionsecurity.ActionUsersCreate && event.Action != actionsecurity.ActionUsersCreateAdmin) || event.OccurredAt <= 0 {
		return false
	}
	if _, err := actionsecurity.ParsePublicRef(event.PublicRef); err != nil {
		return false
	}
	now := clock.NowMillis()
	if now <= 0 || event.OccurredAt > now {
		return false
	}
	switch event.State {
	case models.OperationSucceeded:
		return event.Failure == nil && event.ResultKind != nil && *event.ResultKind == models.ResultUser && event.ResultGUID != nil && *event.ResultGUID > 0
	case models.OperationFailed:
		return validCreateFailure(event.Failure) && event.ResultKind == nil && event.ResultGUID == nil
	default:
		return false
	}
}

func validCreateFailure(failure *models.AdminOperationFailure) bool {
	if failure == nil {
		return false
	}
	switch *failure {
	case models.FailureActionRejected, models.FailureConsumerValidation:
		return true
	default:
		return false
	}
}

type createWriterBinding struct {
	operationID int64
	actorUserID int64
}

func loadCreateWriterBinding(db *gorm.DB, publicRef string, actorGUID, sessionGUID int64, action actionsecurity.Action) (createWriterBinding, error) {
	var operation models.AdminOperation
	if err := db.Select("id", "actor_user_id", "actor_auth_version", "session_id", "action", "verification_id", "state", "public_ref").
		Where("public_ref = ? AND is_deleted = 0", publicRef).First(&operation).Error; err != nil || operation.ID <= 0 || operation.ActorUserID <= 0 ||
		operation.ActorAuthVersion <= 0 || operation.SessionID <= 0 || operation.Action != int(action) || operation.State != models.OperationProcessing ||
		!constantTimeOperationStringEqual(operation.PublicRef, publicRef) {
		return createWriterBinding{}, ErrActionOperationUnavailable
	}
	if action == actionsecurity.ActionUsersCreate {
		if operation.VerificationID != nil {
			return createWriterBinding{}, ErrActionOperationUnavailable
		}
	} else if action == actionsecurity.ActionUsersCreateAdmin {
		if operation.VerificationID == nil || *operation.VerificationID <= 0 {
			return createWriterBinding{}, ErrActionOperationUnavailable
		}
		var verification models.AdminActionVerification
		if err := db.Select("id", "actor_user_id", "actor_auth_version", "session_id", "action", "target_kind", "target_guid", "consumed_at", "is_deleted").
			Where("id = ?", *operation.VerificationID).First(&verification).Error; err != nil || verification.ID != *operation.VerificationID ||
			verification.ActorUserID != operation.ActorUserID || verification.ActorAuthVersion != operation.ActorAuthVersion ||
			verification.SessionID != operation.SessionID || verification.Action != int(actionsecurity.ActionUsersCreateAdmin) ||
			verification.TargetKind != int(actionsecurity.TargetNone) || verification.TargetGUID != nil || verification.ConsumedAt == nil ||
			*verification.ConsumedAt <= 0 || verification.IsDeleted != 1 {
			return createWriterBinding{}, ErrActionOperationUnavailable
		}
	} else {
		return createWriterBinding{}, ErrActionOperationUnavailable
	}
	var actor models.User
	if err := db.Select("id", "guid").Where("id = ? AND is_deleted = 0", operation.ActorUserID).First(&actor).Error; err != nil ||
		actor.ID != operation.ActorUserID || actor.Guid != actorGUID {
		return createWriterBinding{}, ErrActionOperationUnavailable
	}
	var session models.Session
	if err := db.Select("id", "guid", "user_id").Where("id = ?", operation.SessionID).First(&session).Error; err != nil ||
		session.ID != operation.SessionID || session.Guid != sessionGUID || session.UserID != operation.ActorUserID {
		return createWriterBinding{}, ErrActionOperationUnavailable
	}
	return createWriterBinding{operationID: operation.ID, actorUserID: operation.ActorUserID}, nil
}

func validCreateWriterTransaction(ctx context.Context, tx *gorm.DB) bool {
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

func createWriterDB(ctx context.Context, tx *gorm.DB) *gorm.DB {
	return tx.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx)
}
