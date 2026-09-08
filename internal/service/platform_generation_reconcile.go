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
		return PlatformGenerationSnapshot{}, err
	}
	if current.State != PlatformGenerationStateCommitting {
		return current, nil
	}

	var resolved PlatformGenerationSnapshot
	resolvedAuthoritativeOnError := false
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
			resolvedAuthoritativeOnError = receiptErr != nil && resolved.GenerationID != ""
			return receiptErr
		}
		if !errors.Is(receiptErr, ErrPlatformGenerationPersistenceNotFound) {
			return receiptErr
		}
		resolved, receiptErr = store.FailStaleCommit(ctx, userID, generationID, "internal_error", nowMillis)
		resolvedAuthoritativeOnError = receiptErr != nil && resolved.GenerationID != ""
		return receiptErr
	})
	if err != nil {
		if resolvedAuthoritativeOnError {
			return resolved, err
		}
		return current, err
	}
	return resolved, nil
}
