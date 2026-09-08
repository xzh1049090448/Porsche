package service

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type platformGenerationReconcileLock func(context.Context, *gorm.DB, string, func(*gorm.DB) error) error

// ReconcilePlatformGeneration converges a committing Redis lifecycle record
// from the durable receipt while serializing against the matching SQL writer.
func ReconcilePlatformGeneration(ctx context.Context, db *gorm.DB, store *PlatformGenerationStore, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	return reconcilePlatformGeneration(ctx, db, store, userID, generationID, nowMillis, withPlatformGenerationAdvisoryLock)
}

func reconcilePlatformGeneration(ctx context.Context, db *gorm.DB, store *PlatformGenerationStore, userID int64, generationID string, nowMillis int64, withLock platformGenerationReconcileLock) (PlatformGenerationSnapshot, error) {
	if ctx == nil || db == nil || store == nil || validatePlatformGenerationIdentity(userID, generationID) != nil ||
		!platformSSEV2SafeInteger(nowMillis) || nowMillis <= 0 || withLock == nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationPersistenceInvalid
	}
	current, err := store.Get(ctx, userID, generationID)
	if err != nil {
		return PlatformGenerationSnapshot{}, normalizePlatformGenerationReconcileError(err)
	}
	if current.State != PlatformGenerationStateCommitting {
		return current, nil
	}

	var resolved PlatformGenerationSnapshot
	resolvedAuthoritative := false
	err = withLock(ctx, db, platformGenerationAdvisoryLockName(userID, generationID), func(conn *gorm.DB) error {
		receipt, receiptErr := LoadPlatformGenerationReceipt(ctx, conn, userID, generationID)
		if receiptErr == nil {
			guids := make(map[string]string, receipt.SuccessfulModelCount)
			for _, result := range receipt.Results {
				if result.State == PlatformGenerationStateCompleted {
					guids[result.Model] = result.AssistantMessageGUID
				}
			}
			resolved, receiptErr = store.ReconcileComplete(ctx, userID, generationID, guids, nowMillis)
			resolvedAuthoritative = resolved.GenerationID != ""
			return receiptErr
		}
		if !errors.Is(receiptErr, ErrPlatformGenerationPersistenceNotFound) {
			return receiptErr
		}
		resolved, receiptErr = store.FailStaleCommit(ctx, userID, generationID, "internal_error", nowMillis)
		resolvedAuthoritative = resolved.GenerationID != ""
		return receiptErr
	})
	if err != nil {
		if resolvedAuthoritative {
			return resolved, normalizePlatformGenerationReconcileError(err)
		}
		return current, normalizePlatformGenerationReconcileError(err)
	}
	return resolved, nil
}

func normalizePlatformGenerationReconcileError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrPlatformGenerationPersistenceInvalid):
		return ErrPlatformGenerationPersistenceInvalid
	case errors.Is(err, ErrPlatformGenerationPersistenceIntegrity):
		return ErrPlatformGenerationPersistenceIntegrity
	case errors.Is(err, ErrPlatformGenerationPersistenceConflict):
		return ErrPlatformGenerationPersistenceConflict
	case errors.Is(err, ErrPlatformGenerationPersistenceNotFound):
		return ErrPlatformGenerationPersistenceNotFound
	case errors.Is(err, ErrPlatformGenerationInvalid):
		return ErrPlatformGenerationInvalid
	case errors.Is(err, ErrPlatformGenerationConflict):
		return ErrPlatformGenerationConflict
	case errors.Is(err, ErrPlatformGenerationNotFound):
		return ErrPlatformGenerationNotFound
	default:
		return ErrPlatformGenerationPersistenceUnavailable
	}
}
