package service

import (
	"context"
	"errors"
	"math"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

// TerminalOutcome is the complete, persistence-safe result of a transactional
// action callback. A non-nil Failure is a known business rejection; a returned
// error is reserved for infrastructure failure and causes a full rollback.
type TerminalOutcome struct {
	Failure                  *models.AdminOperationFailure
	ResultKind               models.AdminResultKind
	ResultGUID               *int64
	ResultAuthVersion        *int
	ResultPermissionsVersion *int64
	ResultRole               *models.UserRole
	HTTPStatus               int
}

type TransactionalActionConsumer interface {
	Execute(ctx context.Context, tx *gorm.DB, operation models.AdminOperation) (TerminalOutcome, error)
}

type TransactionalAuditWriter interface {
	Write(ctx context.Context, tx *gorm.DB, event ActionAuditEvent) error
}

type TransactionalOutboxWriter interface {
	Write(ctx context.Context, tx *gorm.DB, event ActionOutboxEvent) error
}

// ActionAuditEvent deliberately contains no ticket, key, HMAC, SID, password,
// or database row identifier. PublicRef is the sole structured correlation ID.
type ActionAuditEvent struct {
	PublicRef   string
	ActorGUID   int64
	SessionGUID int64
	Action      actionsecurity.Action
	TargetKind  actionsecurity.TargetKind
	TargetGUID  *int64
	State       models.AdminOperationState
	Failure     *models.AdminOperationFailure
	ResultKind  *models.AdminResultKind
	ResultGUID  *int64
	OccurredAt  int64
}

// ActionOutboxEvent has the same redacted terminal envelope as the audit event.
// Implementations may persist it only through the transaction passed to Write.
type ActionOutboxEvent struct {
	PublicRef   string
	ActorGUID   int64
	SessionGUID int64
	Action      actionsecurity.Action
	TargetKind  actionsecurity.TargetKind
	TargetGUID  *int64
	State       models.AdminOperationState
	Failure     *models.AdminOperationFailure
	ResultKind  *models.AdminResultKind
	ResultGUID  *int64
	OccurredAt  int64
}

// CommitUnknownError means the transaction callback completed but Commit did
// not produce a reliable acknowledgement. Callers must query by the original
// scope and idempotency key; they must never replay the callback.
type CommitUnknownError struct {
	PublicRef string
	Cause     error
}

func (e *CommitUnknownError) Error() string { return "admin operation commit outcome unknown" }

func (e *CommitUnknownError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *CommitUnknownError) Is(target error) bool {
	return target == ErrActionOperationUnavailable
}

func validateTerminalOutcome(outcome TerminalOutcome) error {
	return validateTerminalOutcomeForAction(0, outcome)
}

func validateTerminalOutcomeForAction(action actionsecurity.Action, outcome TerminalOutcome) error {
	if outcome.Failure != nil {
		if outcome.ResultKind != 0 || outcome.ResultGUID != nil || outcome.ResultAuthVersion != nil || outcome.ResultPermissionsVersion != nil || outcome.ResultRole != nil || outcome.HTTPStatus < 400 || outcome.HTTPStatus > 499 {
			return errors.New("invalid terminal outcome")
		}
		switch *outcome.Failure {
		case models.FailureActionRejected, models.FailureTargetVersionConflict, models.FailurePolicyVersionConflict,
			models.FailureTargetStateConflict, models.FailureConsumerValidation:
			return nil
		default:
			return errors.New("invalid terminal outcome")
		}
	}
	if outcome.HTTPStatus < 200 || outcome.HTTPStatus > 299 {
		return errors.New("invalid terminal outcome")
	}
	switch outcome.ResultKind {
	case models.ResultNone:
		if outcome.ResultGUID != nil {
			return errors.New("invalid terminal outcome")
		}
	case models.ResultUser, models.ResultPublicContent:
		if outcome.ResultGUID == nil || *outcome.ResultGUID <= 0 {
			return errors.New("invalid terminal outcome")
		}
	default:
		return errors.New("invalid terminal outcome")
	}
	if outcome.ResultAuthVersion != nil && (*outcome.ResultAuthVersion <= 0 || int64(*outcome.ResultAuthVersion) > math.MaxInt32 || outcome.ResultKind != models.ResultUser) {
		return errors.New("invalid terminal outcome")
	}
	if isA08RolePermissionAction(action) {
		if outcome.ResultKind != models.ResultUser || outcome.ResultGUID == nil || outcome.ResultAuthVersion == nil ||
			outcome.ResultPermissionsVersion == nil || *outcome.ResultPermissionsVersion <= 0 || outcome.ResultRole == nil ||
			(*outcome.ResultRole != models.UserRoleUser && *outcome.ResultRole != models.UserRoleAdmin) {
			return errors.New("invalid terminal outcome")
		}
		if (action == actionsecurity.ActionUsersPromote || action == actionsecurity.ActionUsersPermissionsWrite) && *outcome.ResultRole != models.UserRoleAdmin {
			return errors.New("invalid terminal outcome")
		}
		if action == actionsecurity.ActionUsersDemote && *outcome.ResultRole != models.UserRoleUser {
			return errors.New("invalid terminal outcome")
		}
	} else if outcome.ResultPermissionsVersion != nil || outcome.ResultRole != nil {
		return errors.New("invalid terminal outcome")
	}
	return nil
}

func isA08RolePermissionAction(action actionsecurity.Action) bool {
	return action == actionsecurity.ActionUsersPromote || action == actionsecurity.ActionUsersDemote || action == actionsecurity.ActionUsersPermissionsWrite
}
