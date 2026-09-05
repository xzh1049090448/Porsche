package service

import (
	"context"
	"math"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Execute consumes one request-bound users.delete execution inside the
// transaction owned by ActionOperationService.
func (execution *DeleteUserExecution) Execute(ctx context.Context, tx *gorm.DB, operation models.AdminOperation) (TerminalOutcome, error) {
	if !execution.beginConsumer() {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if ctx == nil || !validDeleteWriterTransaction(ctx, tx) || execution.nextGUID == nil ||
		operationInterfaceNil(execution.clock) || !deleteConsumerOperationValid(operation) {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}

	now := execution.clock.NowMillis()
	if !validOperationNow(now) || operation.CreatedAt > now || operation.UpdatedAt > now {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	db := deleteWriterDB(ctx, tx)
	var target models.User
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "guid", "role", "status", "is_deleted", "auth_version").
		Where("guid = ? AND is_deleted = 0", execution.intent.TargetGUID).First(&target).Error; err != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if target.ID <= 0 || target.ID == operation.ActorUserID || target.Guid != execution.intent.TargetGUID || target.IsDeleted != 0 || target.AuthVersion <= 0 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if !deleteConsumerTargetStateAllowed(target) {
		return deleteConsumerFailure(models.FailureTargetStateConflict), nil
	}
	if err := execution.recordAuditFacts(target.ID, target.Guid, target.Status); err != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if target.AuthVersion != execution.intent.ExpectedAuthVersion {
		return deleteConsumerFailure(models.FailureTargetVersionConflict), nil
	}
	if target.AuthVersion >= math.MaxInt32 {
		return deleteConsumerFailure(models.FailureConsumerValidation), nil
	}

	var sessions []models.Session
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "session_version").
		Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND expires_at > ?", target.ID, now).
		Order("id ASC").Find(&sessions).Error; err != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if failure, err := validateDeleteConsumerSessions(sessions); err != nil {
		return TerminalOutcome{}, err
	} else if failure != nil {
		return deleteConsumerFailure(*failure), nil
	}

	tokenIDs, err := lockDeleteConsumerIDs(db, &models.GatewayAPIToken{},
		"user_id = ? AND is_deleted = 0 AND status = ? AND (expires_at IS NULL OR expires_at > ?)",
		target.ID, models.GatewayTokenActive, now)
	if err != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	headIDs, err := lockDeleteConsumerIDs(db, &models.PermissionPolicyHead{}, "user_id = ? AND is_deleted = 0", target.ID)
	if err != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	overrideIDs, err := lockDeleteConsumerIDs(db, &models.PermissionOverride{}, "user_id = ? AND is_deleted = 0", target.ID)
	if err != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}

	auditGUID := execution.nextGUID()
	if auditGUID <= 0 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	result := db.Model(&models.Session{}).
		Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL AND expires_at > ? AND session_version < ?", target.ID, now, math.MaxInt32).
		Updates(map[string]any{"revoked_at": now, "session_version": gorm.Expr("session_version + 1"), "updated_at": now, "updated_by": operation.ActorUserID})
	if result.Error != nil || result.RowsAffected != int64(len(sessions)) {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	result = db.Model(&models.GatewayAPIToken{}).
		Where("user_id = ? AND is_deleted = 0 AND status = ? AND (expires_at IS NULL OR expires_at > ?)", target.ID, models.GatewayTokenActive, now).
		Updates(map[string]any{"status": models.GatewayTokenRevoked, "updated_at": now, "updated_by": operation.ActorUserID})
	if result.Error != nil || result.RowsAffected != int64(len(tokenIDs)) {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if err := tombstoneDeleteConsumerRows(db, &models.PermissionPolicyHead{}, target.ID, operation.ActorUserID, now, len(headIDs)); err != nil {
		return TerminalOutcome{}, err
	}
	if err := tombstoneDeleteConsumerRows(db, &models.PermissionOverride{}, target.ID, operation.ActorUserID, now, len(overrideIDs)); err != nil {
		return TerminalOutcome{}, err
	}

	userUpdate := db.Model(&models.User{}).
		Where("id = ? AND guid = ? AND is_deleted = 0 AND auth_version = ? AND role = ? AND status = ?",
			target.ID, target.Guid, target.AuthVersion, target.Role, target.Status).
		Updates(map[string]any{
			"auth_version": gorm.Expr("auth_version + 1"), "id_card_hash": nil, "is_deleted": 1, "is_verified": false,
			"nickname": nil, "password_hash": nil, "phone": nil, "real_name": nil, "status": models.UserStatusDisabled,
			"updated_at": now, "updated_by": operation.ActorUserID,
		})
	if userUpdate.Error != nil || userUpdate.RowsAffected != 1 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	actorID := operation.ActorUserID
	audit := models.AuthAuditEvent{
		AuditFields: models.AuditFields{Guid: auditGUID, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID},
		UserID:      &target.ID, EventType: models.AuthAuditEventUserDeleted,
	}
	created := db.Create(&audit)
	if created.Error != nil || created.RowsAffected != 1 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	targetGUID := target.Guid
	return TerminalOutcome{HTTPStatus: 200, ResultKind: models.ResultUser, ResultGUID: &targetGUID}, nil
}

func (execution *DeleteUserExecution) beginConsumer() bool {
	if execution == nil || execution.state == nil {
		return false
	}
	execution.state.mu.Lock()
	defer execution.state.mu.Unlock()
	if execution.state.consumerStarted || execution.state.auditStarted || execution.state.factsRecorded {
		execution.state.consumerStarted = true
		return false
	}
	execution.state.consumerStarted = true
	return true
}

func deleteConsumerOperationValid(operation models.AdminOperation) bool {
	if operation.ID <= 0 || operation.Guid <= 0 || operation.CreatedAt <= 0 || operation.UpdatedAt < operation.CreatedAt ||
		operation.CreatedBy == nil || *operation.CreatedBy != operation.ActorUserID || operation.UpdatedBy == nil ||
		*operation.UpdatedBy != operation.ActorUserID || operation.IsDeleted != 0 || operation.ActorUserID <= 0 ||
		operation.ActorAuthVersion <= 0 || operation.ActorAuthVersion > math.MaxInt32 || operation.SessionID <= 0 ||
		operation.Action != int(actionsecurity.ActionUsersDelete) || operation.VerificationID == nil || *operation.VerificationID <= 0 ||
		operation.State != models.OperationProcessing || operation.QueryExpiresAt <= 0 {
		return false
	}
	_, err := actionsecurity.ParsePublicRef(operation.PublicRef)
	return err == nil
}

func deleteConsumerTargetStateAllowed(target models.User) bool {
	return (target.Role == models.UserRoleUser || target.Role == models.UserRoleAdmin) &&
		(target.Status == models.UserStatusActive || target.Status == models.UserStatusDisabled)
}

func deleteConsumerFailure(value models.AdminOperationFailure) TerminalOutcome {
	failure := value
	return TerminalOutcome{Failure: &failure, HTTPStatus: 409}
}

func validateDeleteConsumerSessions(sessions []models.Session) (*models.AdminOperationFailure, error) {
	var previousID int64
	for _, session := range sessions {
		if session.ID <= previousID || session.SessionVersion <= 0 {
			return nil, ErrActionOperationUnavailable
		}
		if session.SessionVersion >= math.MaxInt32 {
			failure := models.FailureConsumerValidation
			return &failure, nil
		}
		previousID = session.ID
	}
	return nil, nil
}

func lockDeleteConsumerIDs(db *gorm.DB, model any, predicate string, args ...any) ([]int64, error) {
	var rows []struct{ ID int64 }
	if err := db.Model(model).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where(predicate, args...).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, ErrActionOperationUnavailable
	}
	ids := make([]int64, 0, len(rows))
	var previousID int64
	for _, row := range rows {
		if row.ID <= previousID {
			return nil, ErrActionOperationUnavailable
		}
		ids = append(ids, row.ID)
		previousID = row.ID
	}
	return ids, nil
}

func tombstoneDeleteConsumerRows(db *gorm.DB, model any, targetID, actorID, now int64, expected int) error {
	result := db.Model(model).Where("user_id = ? AND is_deleted = 0", targetID).
		Updates(map[string]any{"is_deleted": 1, "updated_at": now, "updated_by": actorID})
	if result.Error != nil || result.RowsAffected != int64(expected) {
		return ErrActionOperationUnavailable
	}
	return nil
}
