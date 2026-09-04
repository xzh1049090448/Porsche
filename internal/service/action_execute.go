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

func (gormActionExecuteTransactionRunner) Run(ctx context.Context, db *gorm.DB, callback func(*gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(callback, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

func (s *ActionOperationService) Execute(ctx context.Context, identity OperationIdentity, consumer TransactionalActionConsumer, audit TransactionalAuditWriter, outbox TransactionalOutboxWriter) (*OperationView, error) {
	return s.executeWithRunner(ctx, identity, consumer, audit, outbox, gormActionExecuteTransactionRunner{})
}

func (s *ActionOperationService) executeWithRunner(ctx context.Context, identity OperationIdentity, consumer TransactionalActionConsumer, audit TransactionalAuditWriter, outbox TransactionalOutboxWriter, runner actionExecuteTransactionRunner) (*OperationView, error) {
	defer clear(identity.LeaseOwner[:])
	if s == nil || ctx == nil || identity.ID <= 0 || len(identity.PublicRef) != 46 || operationInterfaceNil(consumer) ||
		operationInterfaceNil(audit) || operationInterfaceNil(outbox) || operationInterfaceNil(runner) {
		return nil, ErrActionOperationUnavailable
	}

	// Resolve the lock keys without retaining a transaction or trusting this
	// snapshot for authorization. Every value is re-read under locks below.
	var snapshot models.AdminOperation
	if err := s.operationDB(ctx).Select("id", "actor_user_id", "session_id", "public_ref").Where("id = ?", identity.ID).First(&snapshot).Error; err != nil ||
		!constantTimeOperationStringEqual(snapshot.PublicRef, identity.PublicRef) {
		return nil, ErrActionOperationUnavailable
	}
	var sessionSnapshot models.Session
	if err := s.operationDB(ctx).Select("id", "sid", "user_id", "session_version").Where("id = ?", snapshot.SessionID).First(&sessionSnapshot).Error; err != nil ||
		sessionSnapshot.UserID != snapshot.ActorUserID || len(sessionSnapshot.SID) != 36 {
		return nil, ErrActionOperationUnavailable
	}
	var actorSnapshot models.User
	if err := s.operationDB(ctx).Select("id", "guid", "auth_version").Where("id = ?", snapshot.ActorUserID).First(&actorSnapshot).Error; err != nil {
		return nil, ErrActionOperationUnavailable
	}
	actorClaims := ActionActor{UserID: actorSnapshot.ID, UserGUID: actorSnapshot.Guid, AuthVersion: actorSnapshot.AuthVersion,
		SessionSID: sessionSnapshot.SID, SessionVersion: sessionSnapshot.SessionVersion}
	revoked, err := s.authRedis.IsSessionRevoked(ctx, sessionSnapshot.SID)
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
		locked, err := lockOperationActorSession(tx, actorClaims, startedAt)
		if err != nil {
			return err
		}
		var operation models.AdminOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", identity.ID).First(&operation).Error; err != nil {
			return ErrActionOperationUnavailable
		}
		if operation.ActorUserID != locked.actor.ID || operation.SessionID != locked.session.ID || operation.ActorAuthVersion != locked.actor.AuthVersion ||
			operation.State != models.OperationProcessing || operation.IsDeleted != 0 || operation.VerificationID == nil ||
			operation.LeaseOwnerHMAC == nil || operation.LeaseExpiresAt == nil || !constantTimeOperationStringEqual(operation.PublicRef, identity.PublicRef) {
			return ErrActionOperationForbidden
		}
		leaseDigest := s.crypto.LeaseOwnerDigest(identity.LeaseOwner)
		leaseHex := hex.EncodeToString(leaseDigest[:])
		clear(leaseDigest[:])
		if !constantTimeOperationStringEqual(*operation.LeaseOwnerHMAC, leaseHex) {
			return ErrActionOperationForbidden
		}
		verification, err := lockOperationVerificationByID(tx, *operation.VerificationID)
		if err != nil {
			return ErrActionOperationUnavailable
		}
		descriptor, ok := s.resolve(actionsecurity.Action(operation.Action))
		if !validOperationDescriptor(descriptor, actionsecurity.Action(operation.Action), ok) ||
			!validOperationVerificationBinding(verification, locked, descriptor, operation.RequestHMAC, verification.TargetGUID) {
			return ErrActionOperationForbidden
		}
		if err := authorizeExecuteDescriptor(tx, locked.actor, descriptor, verification.TargetGUID); err != nil {
			return err
		}
		finalNow := s.clock.NowMillis()
		if finalNow < startedAt || !validOperationNow(finalNow) || locked.session.ExpiresAt <= finalNow ||
			*operation.LeaseExpiresAt <= finalNow || verificationRelationAt(verification, finalNow) != operationVerificationActive {
			return ErrActionOperationForbidden
		}
		consume := tx.Model(&models.AdminActionVerification{}).
			Where("id = ? AND consumed_at IS NULL AND expires_at > ? AND is_deleted = 0", verification.ID, finalNow).
			Updates(map[string]any{"consumed_at": finalNow, "is_deleted": 1, "updated_at": finalNow, "updated_by": locked.actor.ID})
		if consume.Error != nil || consume.RowsAffected != 1 {
			return ErrActionOperationUnavailable
		}
		if err := tx.SavePoint(actionExecuteSavepoint).Error; err != nil {
			return ErrActionOperationUnavailable
		}
		outcome, err := consumer.Execute(ctx, tx, operation)
		if err != nil || validateTerminalOutcome(outcome) != nil {
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
		auditEvent := ActionAuditEvent{PublicRef: operation.PublicRef, ActorGUID: locked.actor.Guid, SessionGUID: locked.session.Guid,
			Action: descriptor.Action, TargetKind: descriptor.TargetKind, TargetGUID: copyInt64(verification.TargetGUID), State: terminalState,
			Failure: copyOperationFailure(failure), ResultKind: copyResultKind(resultKind), ResultGUID: copyInt64(resultGUID), OccurredAt: finalNow}
		outboxEvent := ActionOutboxEvent{PublicRef: auditEvent.PublicRef, ActorGUID: auditEvent.ActorGUID, SessionGUID: auditEvent.SessionGUID,
			Action: auditEvent.Action, TargetKind: auditEvent.TargetKind, TargetGUID: copyInt64(auditEvent.TargetGUID), State: auditEvent.State,
			Failure: copyOperationFailure(auditEvent.Failure), ResultKind: copyResultKind(auditEvent.ResultKind), ResultGUID: copyInt64(auditEvent.ResultGUID), OccurredAt: finalNow}
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
			"result_guid": resultGUID, "result_http_status": outcome.HTTPStatus, "updated_at": finalNow, "updated_by": locked.actor.ID}
		terminal := tx.Model(&models.AdminOperation{}).
			Where("id = ? AND state = ? AND is_deleted = 0 AND lease_owner_hmac = ? AND verification_id = ?", operation.ID, models.OperationProcessing, leaseHex, verification.ID).
			Updates(updates)
		if terminal.Error != nil || terminal.RowsAffected != 1 {
			return ErrActionOperationUnavailable
		}
		operation.State, operation.FinishedAt, operation.QueryExpiresAt = terminalState, &finalNow, finalNow+actionOperationQueryRetentionMS
		operation.LeaseOwnerHMAC, operation.LeaseExpiresAt = nil, nil
		operation.ErrorCode, operation.ResultKind, operation.ResultGUID = failure, resultKind, resultGUID
		operation.ResultHTTPStatus = &outcome.HTTPStatus
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
	return resultView, nil
}

func authorizeExecuteDescriptor(tx *gorm.DB, actor models.User, descriptor actionsecurity.Descriptor, targetGUID *int64) error {
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
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "role", "status", "is_deleted", "auth_version").Where("guid = ? AND is_deleted = 0", *targetGUID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrActionOperationHidden
			}
			return ErrActionOperationUnavailable
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
	var decision authz.Decision
	switch descriptor.TargetKind {
	case actionsecurity.TargetNone:
		if descriptor.Capability == "users.create" {
			decision = evaluator.Create(models.UserRoleAdmin)
		} else {
			decision = evaluator.Resource(descriptor.Capability)
		}
	case actionsecurity.TargetUser:
		decision = evaluator.User(descriptor.Capability, actionAccount(target))
	default:
		return ErrActionOperationUnavailable
	}
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
