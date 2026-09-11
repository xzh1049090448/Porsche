package service

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const actionExecuteSavepoint = "admin_action_callback"

type actionExecuteTransactionRunner interface {
	Run(context.Context, *gorm.DB, func(*gorm.DB) error) error
}

type gormActionExecuteTransactionRunner struct{}

type actionExecutionSecretClearer interface {
	ClearSecrets()
}

type actionExecutionCommitObserver interface {
	actionCommitConfirmed()
}

// actionExecutionAuthorizationPrelocker lets a consumer extend the shared
// authorization lock order without performing writes. The returned target is
// the exact row the authorization decision must use.
type actionExecutionAuthorizationPrelocker interface {
	prelockForAuthorization(context.Context, *gorm.DB, models.AdminOperation, models.AdminActionVerification) (*models.User, error)
}

// actionExecutionLockedPreflighter is implemented only by A08 consumers. It
// rejects locked business conflicts before a verification is consumed.
type actionExecutionLockedPreflighter interface {
	actionExecutionAuthorizationPrelocker
	preflightLocked(context.Context, *gorm.DB, models.AdminOperation) (*models.AdminOperationFailure, error)
}

func (gormActionExecuteTransactionRunner) Run(ctx context.Context, db *gorm.DB, callback func(*gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(callback, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

func (s *ActionOperationService) Execute(ctx context.Context, identity *OperationIdentity, consumer TransactionalActionConsumer, audit TransactionalAuditWriter, outbox TransactionalOutboxWriter) (*OperationView, error) {
	return s.executeWithRunner(ctx, identity, consumer, audit, outbox, gormActionExecuteTransactionRunner{})
}

func (s *ActionOperationService) executeWithRunner(ctx context.Context, identity *OperationIdentity, consumer TransactionalActionConsumer, audit TransactionalAuditWriter, outbox TransactionalOutboxWriter, runner actionExecuteTransactionRunner) (*OperationView, error) {
	if clearer, ok := consumer.(actionExecutionSecretClearer); ok {
		defer clearer.ClearSecrets()
	}
	if identity == nil {
		return nil, ErrActionOperationUnavailable
	}
	leaseOwner, ok := identity.capability.take()
	if !ok {
		return nil, ErrActionOperationUnavailable
	}
	defer clear(leaseOwner[:])
	if s == nil || ctx == nil || identity.ID <= 0 || len(identity.PublicRef) != 46 || operationInterfaceNil(consumer) ||
		operationInterfaceNil(audit) || operationInterfaceNil(outbox) || operationInterfaceNil(runner) || !validOperationActorClaims(identity.actor) {
		return nil, ErrActionOperationUnavailable
	}
	revoked, err := s.authRedis.IsSessionRevoked(ctx, identity.actor.SessionSID)
	if err != nil {
		return nil, ErrActionOperationUnavailable
	}
	if revoked {
		return nil, ErrActionOperationForbidden
	}

	startedAt := s.clock.NowMillis()
	if !validOperationNow(startedAt) {
		return nil, ErrActionOperationUnavailable
	}
	callbackComplete := false
	var resultView *OperationView
	err = runner.Run(ctx, s.operationDB(ctx), func(tx *gorm.DB) error {
		locked, err := lockOperationActorSession(tx, identity.actor, startedAt)
		if err != nil {
			return err
		}
		var operation models.AdminOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", identity.ID).First(&operation).Error; err != nil {
			return ErrActionOperationUnavailable
		}
		if operation.ActorUserID != locked.actor.ID || operation.SessionID != locked.session.ID || operation.ActorAuthVersion != locked.actor.AuthVersion ||
			operation.State != models.OperationProcessing || operation.IsDeleted != 0 ||
			operation.LeaseOwnerHMAC == nil || operation.LeaseExpiresAt == nil || !constantTimeOperationStringEqual(operation.PublicRef, identity.PublicRef) {
			return ErrActionOperationForbidden
		}
		leaseDigest := s.crypto.LeaseOwnerDigest(leaseOwner)
		clear(leaseOwner[:])
		leaseHex := hex.EncodeToString(leaseDigest[:])
		clear(leaseDigest[:])
		if !constantTimeOperationStringEqual(*operation.LeaseOwnerHMAC, leaseHex) {
			return ErrActionOperationForbidden
		}
		descriptor, ok := s.resolve(actionsecurity.Action(operation.Action))
		if !validOperationDescriptor(descriptor, actionsecurity.Action(operation.Action), ok) {
			return ErrActionOperationForbidden
		}
		var verification *models.AdminActionVerification
		var targetGUID *int64
		if descriptor.RequiresTicket {
			if operation.VerificationID == nil {
				return ErrActionOperationForbidden
			}
			lockedVerification, err := lockOperationVerificationByID(tx, *operation.VerificationID)
			if err != nil {
				return ErrActionOperationUnavailable
			}
			if !validOperationVerificationBinding(lockedVerification, locked, descriptor, operation.RequestHMAC, lockedVerification.TargetGUID) {
				return ErrActionOperationForbidden
			}
			verification = &lockedVerification
			targetGUID = lockedVerification.TargetGUID
		} else if operation.VerificationID != nil || descriptor.TargetKind != actionsecurity.TargetNone {
			return ErrActionOperationForbidden
		}
		var prelockedTarget *models.User
		if prelocker, ok := consumer.(actionExecutionAuthorizationPrelocker); ok {
			if verification == nil {
				return ErrActionOperationForbidden
			}
			prelockedTarget, err = prelocker.prelockForAuthorization(ctx, tx, operation, *verification)
			if err != nil {
				return err
			}
		}
		if err := authorizeExecuteDescriptorWithTarget(tx, locked.actor, descriptor, targetGUID, prelockedTarget); err != nil {
			return err
		}
		finalNow := s.clock.NowMillis()
		if finalNow < startedAt || !validOperationNow(finalNow) || locked.session.ExpiresAt <= finalNow ||
			*operation.LeaseExpiresAt <= finalNow || (descriptor.RequiresTicket && verificationRelationAt(*verification, finalNow) != operationVerificationActive) {
			return ErrActionOperationForbidden
		}
		if preflighter, ok := consumer.(actionExecutionLockedPreflighter); ok && isA08RolePermissionAction(descriptor.Action) {
			failure, preflightErr := preflighter.preflightLocked(ctx, tx, operation)
			if preflightErr != nil {
				return preflightErr
			}
			if failure != nil {
				return rolePermissionPreflightError(*failure)
			}
		}
		if descriptor.RequiresTicket {
			consume := tx.Model(&models.AdminActionVerification{}).
				Where("id = ? AND consumed_at IS NULL AND expires_at > ? AND is_deleted = 0", verification.ID, finalNow).
				Updates(map[string]any{"consumed_at": finalNow, "is_deleted": 1, "updated_at": finalNow, "updated_by": locked.actor.ID})
			if consume.Error != nil || consume.RowsAffected != 1 {
				return ErrActionOperationUnavailable
			}
		}
		if err := tx.SavePoint(actionExecuteSavepoint).Error; err != nil {
			return ErrActionOperationUnavailable
		}
		outcome, err := consumer.Execute(ctx, tx, operation)
		if err != nil || validateTerminalOutcomeForAction(descriptor.Action, outcome) != nil {
			return ErrActionOperationUnavailable
		}

		terminalState := models.OperationSucceeded
		var failure *models.AdminOperationFailure
		var resultKind *models.AdminResultKind
		var resultGUID *int64
		if outcome.Failure != nil {
			if err := tx.RollbackTo(actionExecuteSavepoint).Error; err != nil {
				return ErrActionOperationUnavailable
			}
			terminalState = models.OperationFailed
			failure = copyOperationFailure(outcome.Failure)
		} else {
			kind := outcome.ResultKind
			resultKind = &kind
			resultGUID = copyInt64(outcome.ResultGUID)
		}
		resultAuthVersion := copyInt(outcome.ResultAuthVersion)
		resultPermissionsVersion := copyInt64(outcome.ResultPermissionsVersion)
		resultRole := copyUserRole(outcome.ResultRole)
		auditEvent := ActionAuditEvent{PublicRef: operation.PublicRef, ActorGUID: locked.actor.Guid, SessionGUID: locked.session.Guid,
			Action: descriptor.Action, TargetKind: descriptor.TargetKind, TargetGUID: copyInt64(targetGUID), State: terminalState,
			Failure: copyOperationFailure(failure), ResultKind: copyResultKind(resultKind), ResultGUID: copyInt64(resultGUID), OccurredAt: finalNow}
		outboxEvent := ActionOutboxEvent{PublicRef: auditEvent.PublicRef, ActorGUID: auditEvent.ActorGUID, SessionGUID: auditEvent.SessionGUID,
			Action: auditEvent.Action, TargetKind: auditEvent.TargetKind, TargetGUID: copyInt64(auditEvent.TargetGUID), State: auditEvent.State,
			Failure: copyOperationFailure(auditEvent.Failure), ResultKind: copyResultKind(auditEvent.ResultKind), ResultGUID: copyInt64(auditEvent.ResultGUID), OccurredAt: finalNow}
		if (terminalState == models.OperationSucceeded && descriptor.Action == actionsecurity.ActionUsersResetPassword && resultAuthVersion == nil) ||
			(terminalState == models.OperationSucceeded && !isA08RolePermissionAction(descriptor.Action) && descriptor.Action != actionsecurity.ActionUsersResetPassword && resultAuthVersion != nil) ||
			(terminalState != models.OperationSucceeded && (resultAuthVersion != nil || resultPermissionsVersion != nil || resultRole != nil)) {
			return ErrActionOperationUnavailable
		}
		if err := audit.Write(ctx, tx, auditEvent); err != nil {
			return ErrActionOperationUnavailable
		}
		if err := outbox.Write(ctx, tx, outboxEvent); err != nil {
			return ErrActionOperationUnavailable
		}
		if finalNow > math.MaxInt64-actionOperationQueryRetentionMS {
			return ErrActionOperationUnavailable
		}
		updates := map[string]any{"state": terminalState, "finished_at": finalNow, "query_expires_at": finalNow + actionOperationQueryRetentionMS,
			"lease_owner_hmac": nil, "lease_expires_at": nil, "error_code": failure, "result_kind": resultKind,
			"result_guid": resultGUID, "result_auth_version": resultAuthVersion, "result_permissions_version": resultPermissionsVersion,
			"result_role": resultRole, "result_http_status": outcome.HTTPStatus, "updated_at": finalNow, "updated_by": locked.actor.ID}
		var terminal *gorm.DB
		if descriptor.RequiresTicket {
			terminal = tx.Model(&models.AdminOperation{}).
				Where("id = ? AND state = ? AND is_deleted = 0 AND lease_owner_hmac = ? AND verification_id = ?", operation.ID, models.OperationProcessing, leaseHex, verification.ID).
				Updates(updates)
		} else {
			terminal = tx.Model(&models.AdminOperation{}).
				Where("id = ? AND state = ? AND is_deleted = 0 AND lease_owner_hmac = ? AND verification_id IS NULL", operation.ID, models.OperationProcessing, leaseHex).
				Updates(updates)
		}
		if terminal.Error != nil || terminal.RowsAffected != 1 {
			return ErrActionOperationUnavailable
		}
		operation.State, operation.FinishedAt, operation.QueryExpiresAt = terminalState, &finalNow, finalNow+actionOperationQueryRetentionMS
		operation.LeaseOwnerHMAC, operation.LeaseExpiresAt = nil, nil
		operation.ErrorCode, operation.ResultKind, operation.ResultGUID = failure, resultKind, resultGUID
		operation.ResultHTTPStatus = &outcome.HTTPStatus
		operation.ResultAuthVersion = resultAuthVersion
		operation.ResultPermissionsVersion = resultPermissionsVersion
		operation.ResultRole = resultRole
		resultView = operationView(descriptor, operation, finalNow)
		callbackComplete = true
		return nil
	})
	if err != nil {
		if callbackComplete {
			return nil, &CommitUnknownError{PublicRef: identity.PublicRef, Cause: err}
		}
		return nil, mapOperationError(err)
	}
	if !callbackComplete || resultView == nil {
		return nil, ErrActionOperationUnavailable
	}
	if observer, ok := consumer.(actionExecutionCommitObserver); ok {
		observer.actionCommitConfirmed()
	}
	return resultView, nil
}

func authorizeExecuteDescriptor(tx *gorm.DB, actor models.User, descriptor actionsecurity.Descriptor, targetGUID *int64) error {
	return authorizeExecuteDescriptorWithTarget(tx, actor, descriptor, targetGUID, nil)
}

func authorizeExecuteDescriptorWithTarget(tx *gorm.DB, actor models.User, descriptor actionsecurity.Descriptor, targetGUID *int64, prelockedTarget *models.User) error {
	if descriptor.RootOnly && actor.Role != models.UserRoleRoot {
		return ErrActionOperationForbidden
	}
	var target models.User
	switch descriptor.TargetKind {
	case actionsecurity.TargetNone:
		if targetGUID != nil {
			return ErrActionOperationHidden
		}
	case actionsecurity.TargetUser:
		if targetGUID == nil || *targetGUID <= 0 {
			return ErrActionOperationHidden
		}
		if prelockedTarget != nil {
			target = *prelockedTarget
			if target.ID <= 0 || target.Guid != *targetGUID || target.IsDeleted != 0 {
				return ErrActionOperationForbidden
			}
		} else {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "role", "status", "is_deleted", "auth_version").Where("guid = ? AND is_deleted = 0", *targetGUID).First(&target).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrActionOperationHidden
				}
				return ErrActionOperationUnavailable
			}
		}
	case actionsecurity.TargetPublicContent:
		return ErrActionOperationUnavailable
	default:
		return ErrActionOperationUnavailable
	}
	// Lock the policy head and all extant rules before interpreting the
	// snapshot. This is after the target lock and before any callback write.
	var head models.PermissionPolicyHead
	headErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("user_id = ?", actor.ID).First(&head).Error
	if headErr != nil && !errors.Is(headErr, gorm.ErrRecordNotFound) {
		return ErrActionOperationUnavailable
	}
	var lockedRules []models.PermissionOverride
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("user_id = ?", actor.ID).Order("id ASC").Find(&lockedRules).Error; err != nil {
		return ErrActionOperationUnavailable
	}
	_, rules, err := readPermissionPolicyRows(tx, actor.ID)
	if err != nil {
		return ErrActionOperationUnavailable
	}
	evaluator, err := authz.NewEvaluator(actionAccount(actor), rules)
	if err != nil {
		return ErrActionOperationUnavailable
	}
	decision := operationDescriptorAuthorizationDecision(evaluator, descriptor, target)
	if decision == authz.Hidden {
		return ErrActionOperationHidden
	}
	if decision != authz.Allowed {
		return ErrActionOperationForbidden
	}
	return nil
}

func copyOperationFailure(value *models.AdminOperationFailure) *models.AdminOperationFailure {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyResultKind(value *models.AdminResultKind) *models.AdminResultKind {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyUserRole(value *models.UserRole) *models.UserRole {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
