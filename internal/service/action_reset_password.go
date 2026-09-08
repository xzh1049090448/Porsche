package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ResetPasswordRequestMetadata struct{ RequestID, TrustedIP string }

type ResetPasswordExecution struct {
	descriptor  actionsecurity.Descriptor
	intent      actionsecurity.ResetPasswordIntent
	redis       *AuthRedis
	clock       persistence.Clock
	nextGUID    func() int64
	metadata    ResetPasswordRequestMetadata
	requestHMAC string
	state       *resetPasswordExecutionState
}
type resetPasswordExecutionState struct {
	mu                    sync.Mutex
	hash                  []byte
	started, auditStarted bool
	targetID              int64
	prelocked             bool
	target                models.User
	sessions              []models.Session
}

func NewResetPasswordExecution(intent actionsecurity.ResetPasswordIntent, hash []byte, redis *AuthRedis, clock persistence.Clock, nextGUID func() int64, crypto *actionsecurity.Crypto) (*ResetPasswordExecution, error) {
	d, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersResetPassword)
	if !ok {
		clear(hash)
		return nil, ErrActionOperationUnavailable
	}
	return newResetPasswordExecution(d, intent, hash, redis, clock, nextGUID, crypto, ResetPasswordRequestMetadata{})
}
func newResetPasswordExecution(d actionsecurity.Descriptor, intent actionsecurity.ResetPasswordIntent, hash []byte, redis *AuthRedis, clock persistence.Clock, nextGUID func() int64, crypto *actionsecurity.Crypto, metadata ResetPasswordRequestMetadata) (*ResetPasswordExecution, error) {
	defer clear(intent.NewPassword)
	expected, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersResetPassword)
	if !ok || d.Action != expected.Action || d.Name != expected.Name || d.Capability != expected.Capability || d.RootOnly != expected.RootOnly || d.RequiresTicket != expected.RequiresTicket || d.Active != expected.Active || d.TargetKind != expected.TargetKind || d.Encode == nil || intent.TargetGUID <= 0 || intent.ExpectedAuthVersion <= 0 || intent.Reason == "" || !validManagedCreatePasswordHash(hash) || redis == nil || redisClientIsNil(redis.client) || operationInterfaceNil(clock) || nextGUID == nil || crypto == nil || (metadata.RequestID != "" && !validCreateRequestID(metadata.RequestID)) {
		clear(hash)
		return nil, ErrActionOperationUnavailable
	}
	encoded, err := d.Encode(intent)
	if err != nil || len(encoded) == 0 {
		clear(encoded)
		clear(hash)
		return nil, ErrActionOperationUnavailable
	}
	digest := crypto.IntentDigest(encoded)
	clear(encoded)
	requestHMAC := hex.EncodeToString(digest[:])
	clear(digest[:])
	ownedHash := append([]byte(nil), hash...)
	clear(hash)
	ownedIntent := actionsecurity.ResetPasswordIntent{TargetGUID: intent.TargetGUID, ExpectedAuthVersion: intent.ExpectedAuthVersion, Reason: strings.Clone(intent.Reason)}
	return &ResetPasswordExecution{descriptor: d, intent: ownedIntent, redis: redis, clock: clock, nextGUID: nextGUID, metadata: metadata, requestHMAC: requestHMAC, state: &resetPasswordExecutionState{hash: ownedHash}}, nil
}
func (e *ResetPasswordExecution) String() string {
	if e == nil {
		return "ResetPasswordExecution<nil>"
	}
	return "ResetPasswordExecution{Action:users.reset_password,TargetGUID:" + strconv.FormatInt(e.intent.TargetGUID, 10) + ",PasswordHash:<redacted>}"
}
func (e ResetPasswordExecution) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, e.String()) }
func (ResetPasswordExecution) MarshalJSON() ([]byte, error) { return json.Marshal(struct{}{}) }
func (e *ResetPasswordExecution) ClearSecrets() {
	if e == nil || e.state == nil {
		return
	}
	e.state.mu.Lock()
	clear(e.state.hash)
	e.state.hash = nil
	for i := range e.state.sessions {
		e.state.sessions[i].SID = ""
		e.state.sessions[i].RefreshHMAC = ""
		e.state.sessions[i].PreviousRefreshHMAC = nil
	}
	e.state.sessions = nil
	e.state.mu.Unlock()
}

func (e *ResetPasswordExecution) prelockForAuthorization(ctx context.Context, tx *gorm.DB, operation models.AdminOperation, verification models.AdminActionVerification) (*models.User, error) {
	if e == nil || e.state == nil || ctx == nil || !validDeleteWriterTransaction(ctx, tx) || !e.validExecutionBinding(operation, verification) {
		return nil, ErrActionOperationForbidden
	}
	db := deleteWriterDB(ctx, tx)
	var target models.User
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "role", "status", "is_deleted", "auth_version").Where("guid = ? AND is_deleted = 0", e.intent.TargetGUID).First(&target).Error; err != nil {
		return nil, ErrActionOperationUnavailable
	}
	if target.ID <= 0 || target.Guid != e.intent.TargetGUID || target.IsDeleted != 0 {
		return nil, ErrActionOperationForbidden
	}
	type lockedResetSession struct {
		ID             int64
		GUID           int64  `gorm:"column:guid"`
		SID            string `gorm:"column:sid"`
		UserID         int64
		LoginMethod    models.LoginMethod
		SessionVersion int
		IsDeleted      int
		RevokedAt      *int64
		ExpiresAt      int64
	}
	var rows []lockedResetSession
	if err := db.Model(&models.Session{}).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "sid", "user_id", "login_method", "session_version", "is_deleted", "revoked_at", "expires_at").Where("user_id = ? AND is_deleted = 0 AND revoked_at IS NULL", target.ID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, ErrActionOperationUnavailable
	}
	sessions := make([]models.Session, len(rows))
	var previousID int64
	for i := range rows {
		row := rows[i]
		if row.ID <= previousID || row.GUID <= 0 || row.UserID != target.ID || row.SID == "" || row.SessionVersion <= 0 || row.SessionVersion >= math.MaxInt32 || row.IsDeleted != 0 || row.RevokedAt != nil || row.ExpiresAt <= 0 {
			return nil, ErrActionOperationUnavailable
		}
		sessions[i] = models.Session{ID: row.ID, AuditFields: models.AuditFields{Guid: row.GUID}, SID: row.SID, UserID: row.UserID, LoginMethod: row.LoginMethod, SessionVersion: row.SessionVersion, ExpiresAt: row.ExpiresAt}
		previousID = row.ID
	}
	e.state.mu.Lock()
	defer e.state.mu.Unlock()
	if e.state.prelocked || e.state.started || e.state.auditStarted {
		return nil, ErrActionOperationUnavailable
	}
	e.state.prelocked = true
	e.state.target = target
	e.state.sessions = sessions
	result := target
	return &result, nil
}

func (e *ResetPasswordExecution) validExecutionBinding(operation models.AdminOperation, verification models.AdminActionVerification) bool {
	_, publicRefErr := actionsecurity.ParsePublicRef(operation.PublicRef)
	return publicRefErr == nil && operation.ID > 0 && operation.Guid > 0 && operation.ActorUserID > 0 && operation.ActorAuthVersion > 0 && operation.SessionID > 0 &&
		operation.Action == int(actionsecurity.ActionUsersResetPassword) && operation.VerificationID != nil && *operation.VerificationID > 0 &&
		operation.State == models.OperationProcessing && operation.IsDeleted == 0 && operation.LeaseOwnerHMAC != nil && len(*operation.LeaseOwnerHMAC) == 64 &&
		operation.LeaseExpiresAt != nil && *operation.LeaseExpiresAt > 0 && operation.QueryExpiresAt > 0 &&
		constantTimeOperationStringEqual(operation.RequestHMAC, e.requestHMAC) &&
		verification.ID == *operation.VerificationID && verification.ActorUserID == operation.ActorUserID &&
		verification.ActorAuthVersion == operation.ActorAuthVersion && verification.SessionID == operation.SessionID &&
		verification.Action == operation.Action && verification.TargetKind == int(actionsecurity.TargetUser) &&
		verification.TargetGUID != nil && *verification.TargetGUID == e.intent.TargetGUID &&
		verification.ConsumedAt == nil && verification.IsDeleted == 0 && verification.ExpiresAt > 0 &&
		constantTimeOperationStringEqual(verification.IntentHMAC, e.requestHMAC)
}

func clearResetPasswordSessions(sessions []models.Session) {
	for i := range sessions {
		sessions[i].SID = ""
		sessions[i].RefreshHMAC = ""
		sessions[i].PreviousRefreshHMAC = nil
	}
}

func (e *ResetPasswordExecution) Execute(ctx context.Context, tx *gorm.DB, operation models.AdminOperation) (TerminalOutcome, error) {
	if e == nil || e.state == nil || ctx == nil || !validDeleteWriterTransaction(ctx, tx) {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	e.state.mu.Lock()
	if e.state.started || !e.state.prelocked || len(e.state.hash) == 0 {
		e.state.started = true
		e.state.mu.Unlock()
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	e.state.started = true
	hash := e.state.hash
	e.state.hash = nil
	target := e.state.target
	sessions := e.state.sessions
	e.state.sessions = nil
	e.state.mu.Unlock()
	defer clear(hash)
	defer clearResetPasswordSessions(sessions)
	now := e.clock.NowMillis()
	if !validOperationNow(now) || operation.Action != int(actionsecurity.ActionUsersResetPassword) || operation.ActorUserID <= 0 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	db := deleteWriterDB(ctx, tx)
	if target.ID <= 0 || target.ID == operation.ActorUserID || target.AuthVersion <= 0 || (target.Status != models.UserStatusActive && target.Status != models.UserStatusDisabled) {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if target.AuthVersion != e.intent.ExpectedAuthVersion {
		return resetPasswordFailure(models.FailureTargetVersionConflict), nil
	}
	if target.AuthVersion >= math.MaxInt32 {
		return resetPasswordFailure(models.FailureConsumerValidation), nil
	}
	sessionService := &SessionService{redis: e.redis, now: e.clock.NowMillis}
	for i := range sessions {
		if err := e.redis.MarkSessionRevoked(ctx, sessions[i].SID, sessionTTL(sessions[i], now)); err != nil {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
	}
	for i := range sessions {
		if err := sessionService.revokeLocked(db, &sessions[i], operation.ActorUserID, models.AuthAuditEventSessionRevoked, SessionCreateInput{}); err != nil {
			return TerminalOutcome{}, ErrActionOperationUnavailable
		}
	}
	updated := db.Model(&models.User{}).Where("id = ? AND guid = ? AND is_deleted = 0 AND auth_version = ?", target.ID, target.Guid, target.AuthVersion).Updates(map[string]any{"password_hash": string(hash), "auth_version": target.AuthVersion + 1, "updated_at": now, "updated_by": operation.ActorUserID})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	auditGUID := e.nextGUID()
	if auditGUID <= 0 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if err := db.Create(&models.AuthAuditEvent{AuditFields: models.AuditFields{Guid: auditGUID, CreatedAt: now, CreatedBy: &operation.ActorUserID, UpdatedAt: now, UpdatedBy: &operation.ActorUserID}, UserID: &target.ID, EventType: models.AuthAuditEventPasswordChanged}).Error; err != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	e.state.mu.Lock()
	e.state.targetID = target.ID
	e.state.mu.Unlock()
	guid, version := target.Guid, target.AuthVersion+1
	return TerminalOutcome{ResultKind: models.ResultUser, ResultGUID: &guid, ResultAuthVersion: &version, HTTPStatus: 200}, nil
}
func resetPasswordFailure(f models.AdminOperationFailure) TerminalOutcome {
	return TerminalOutcome{Failure: &f, HTTPStatus: 409}
}

func (e *ResetPasswordExecution) Write(ctx context.Context, tx *gorm.DB, event ActionAuditEvent) error {
	if e == nil || e.state == nil || !validDeleteWriterTransaction(ctx, tx) || !validResetPasswordTerminalEnvelope(event.PublicRef, event.ActorGUID, event.SessionGUID, event.Action, event.TargetKind, event.TargetGUID, event.State, event.Failure, event.ResultKind, event.ResultGUID, event.OccurredAt, e.intent.TargetGUID, e.clock) {
		return ErrActionOperationUnavailable
	}
	e.state.mu.Lock()
	if e.state.auditStarted {
		e.state.mu.Unlock()
		return ErrActionOperationUnavailable
	}
	e.state.auditStarted = true
	targetID := e.state.targetID
	e.state.mu.Unlock()
	binding, err := loadResetPasswordBinding(deleteWriterDB(ctx, tx), event.PublicRef, event.ActorGUID, event.SessionGUID, e.intent.TargetGUID)
	if err != nil {
		return err
	}
	resource := "user:" + strconv.FormatInt(e.intent.TargetGUID, 10)
	detail := models.JSONMap{"operation_ref": event.PublicRef, "actor_guid": event.ActorGUID, "target_guid": e.intent.TargetGUID, "reason": e.intent.Reason, "request_id": e.metadata.RequestID, "terminal_state": event.State.String(), "failure_code": nil}
	var uid *int64
	if targetID > 0 {
		uid = &targetID
	}
	if event.Failure != nil {
		detail["failure_code"] = event.Failure.String()
	}
	row := models.AuditLog{AuditFields: models.AuditFields{Guid: e.nextGUID(), CreatedAt: event.OccurredAt, CreatedBy: &binding.actorUserID, UpdatedAt: event.OccurredAt, UpdatedBy: &binding.actorUserID}, UserID: uid, Action: "users.reset_password", Resource: &resource, Detail: detail}
	if row.Guid <= 0 {
		return ErrActionOperationUnavailable
	}
	if created := deleteWriterDB(ctx, tx).Create(&row); created.Error != nil || created.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	return nil
}

type ResetPasswordOutboxWriter struct {
	nextGUID func() int64
	clock    persistence.Clock
}

func NewResetPasswordOutboxWriter(nextGUID func() int64, clock persistence.Clock) (*ResetPasswordOutboxWriter, error) {
	if nextGUID == nil || operationInterfaceNil(clock) {
		return nil, ErrActionOperationUnavailable
	}
	return &ResetPasswordOutboxWriter{nextGUID: nextGUID, clock: clock}, nil
}

func (w *ResetPasswordOutboxWriter) Write(ctx context.Context, tx *gorm.DB, event ActionOutboxEvent) error {
	if w == nil || event.TargetGUID == nil || !validDeleteWriterTransaction(ctx, tx) || !validResetPasswordTerminalEnvelope(event.PublicRef, event.ActorGUID, event.SessionGUID, event.Action, event.TargetKind, event.TargetGUID, event.State, event.Failure, event.ResultKind, event.ResultGUID, event.OccurredAt, *event.TargetGUID, w.clock) {
		return ErrActionOperationUnavailable
	}
	b, err := loadResetPasswordBinding(deleteWriterDB(ctx, tx), event.PublicRef, event.ActorGUID, event.SessionGUID, *event.TargetGUID)
	if err != nil {
		return err
	}
	row := models.AdminActionOutbox{AuditFields: models.AuditFields{Guid: w.nextGUID(), CreatedAt: event.OccurredAt, CreatedBy: &b.actorUserID, UpdatedAt: event.OccurredAt, UpdatedBy: &b.actorUserID}, OperationID: b.operationID, PublicRef: event.PublicRef, Action: int(event.Action), TargetKind: int(actionsecurity.TargetUser), TargetGUID: copyInt64(event.TargetGUID), State: event.State, FailureCode: copyOperationFailure(event.Failure), ResultKind: copyResultKind(event.ResultKind), ResultGUID: copyInt64(event.ResultGUID), DeliveryState: models.DeliveryPending, AvailableAt: event.OccurredAt}
	if row.Guid <= 0 {
		return ErrActionOperationUnavailable
	}
	if created := deleteWriterDB(ctx, tx).Create(&row); created.Error != nil || created.RowsAffected != 1 {
		return ErrActionOperationUnavailable
	}
	return nil
}

func validResetPasswordTerminalEnvelope(publicRef string, actorGUID, sessionGUID int64, action actionsecurity.Action, targetKind actionsecurity.TargetKind, targetGUID *int64, state models.AdminOperationState, failure *models.AdminOperationFailure, resultKind *models.AdminResultKind, resultGUID *int64, occurredAt, expectedTargetGUID int64, clock persistence.Clock) bool {
	if _, err := actionsecurity.ParsePublicRef(publicRef); err != nil || actorGUID <= 0 || sessionGUID <= 0 || action != actionsecurity.ActionUsersResetPassword || targetKind != actionsecurity.TargetUser || targetGUID == nil || *targetGUID != expectedTargetGUID || expectedTargetGUID <= 0 || occurredAt <= 0 || operationInterfaceNil(clock) {
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

type resetPasswordBinding struct{ operationID, actorUserID int64 }

func loadResetPasswordBinding(db *gorm.DB, ref string, actorGUID, sessionGUID, targetGUID int64) (resetPasswordBinding, error) {
	var op models.AdminOperation
	if err := db.Select("id", "actor_user_id", "actor_auth_version", "session_id", "action", "verification_id", "state", "public_ref").Where("public_ref = ? AND is_deleted = 0", ref).First(&op).Error; err != nil ||
		op.ID <= 0 || op.ActorUserID <= 0 || op.ActorAuthVersion <= 0 || op.SessionID <= 0 ||
		op.Action != int(actionsecurity.ActionUsersResetPassword) || op.VerificationID == nil || *op.VerificationID <= 0 ||
		op.State != models.OperationProcessing || !constantTimeOperationStringEqual(op.PublicRef, ref) {
		return resetPasswordBinding{}, ErrActionOperationUnavailable
	}
	var actor models.User
	if err := db.Select("id", "guid").Where("id = ? AND is_deleted = 0", op.ActorUserID).First(&actor).Error; err != nil || actor.ID != op.ActorUserID || actor.Guid <= 0 || actor.Guid != actorGUID {
		return resetPasswordBinding{}, ErrActionOperationUnavailable
	}
	var verification models.AdminActionVerification
	if err := db.Select("id", "actor_user_id", "actor_auth_version", "session_id", "action", "target_kind", "target_guid", "consumed_at", "is_deleted").Where("id = ?", *op.VerificationID).First(&verification).Error; err != nil ||
		verification.ID != *op.VerificationID || verification.ActorUserID != op.ActorUserID ||
		verification.ActorAuthVersion != op.ActorAuthVersion || verification.SessionID != op.SessionID ||
		verification.Action != int(actionsecurity.ActionUsersResetPassword) || verification.TargetKind != int(actionsecurity.TargetUser) ||
		verification.TargetGUID == nil || *verification.TargetGUID != targetGUID || verification.ConsumedAt == nil ||
		*verification.ConsumedAt <= 0 || verification.IsDeleted != 1 {
		return resetPasswordBinding{}, ErrActionOperationUnavailable
	}
	var session models.Session
	if err := db.Select("id", "guid", "user_id").Where("id = ?", op.SessionID).First(&session).Error; err != nil ||
		session.ID != op.SessionID || session.Guid <= 0 || session.Guid != sessionGUID || session.UserID != op.ActorUserID {
		return resetPasswordBinding{}, ErrActionOperationUnavailable
	}
	return resetPasswordBinding{op.ID, actor.ID}, nil
}

type ResetPasswordResult struct {
	TargetGUID           int64
	ResultingAuthVersion int
}

func (s *ActionOperationService) ResetPasswordResponse(ctx context.Context, actor ActionActor, publicRef string) (*ResetPasswordResult, error) {
	if s == nil || ctx == nil {
		return nil, ErrActionOperationUnavailable
	}
	var op models.AdminOperation
	if err := s.db.WithContext(ctx).Where("public_ref = ? AND actor_user_id = ? AND actor_auth_version = ? AND action = ? AND state = ? AND is_deleted = 0", publicRef, actor.UserID, actor.AuthVersion, int(actionsecurity.ActionUsersResetPassword), models.OperationSucceeded).First(&op).Error; err != nil || op.ResultKind == nil || *op.ResultKind != models.ResultUser || op.ResultGUID == nil || op.ResultAuthVersion == nil || op.ResultHTTPStatus == nil || *op.ResultHTTPStatus != 200 || op.FinishedAt == nil || *op.FinishedAt <= 0 || op.ErrorCode != nil || op.LeaseOwnerHMAC != nil || op.LeaseExpiresAt != nil || *op.ResultGUID <= 0 || *op.ResultAuthVersion <= 0 || *op.ResultAuthVersion > math.MaxInt32 {
		return nil, ErrActionOperationUnavailable
	}
	return &ResetPasswordResult{TargetGUID: *op.ResultGUID, ResultingAuthVersion: *op.ResultAuthVersion}, nil
}
