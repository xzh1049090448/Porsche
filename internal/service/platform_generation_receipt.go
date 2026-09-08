package service

import (
	"context"
	"errors"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

// LoadPlatformGenerationReceipt returns a completed receipt only when its
// entire persisted graph still belongs to userID and satisfies the write-side
// contract. It deliberately collapses graph failures to a body-free integrity
// error so persisted prompts and answers cannot escape through diagnostics.
func LoadPlatformGenerationReceipt(ctx context.Context, db *gorm.DB, userID int64, generationID string) (PlatformGenerationReceiptSnapshot, error) {
	if ctx == nil || db == nil || validatePlatformGenerationIdentity(userID, generationID) != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceInvalid
	}

	var receipt models.PlatformChatGenerationReceipt
	err := db.WithContext(ctx).
		Where("user_id = ? AND generation_id = ? AND is_deleted = 0", userID, generationID).
		First(&receipt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceNotFound
	}
	if err != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable
	}
	if !validPlatformGenerationReceiptParent(receipt, userID, generationID) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}

	var conversation models.Conversation
	if err := db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND is_deleted = 0", receipt.ConversationID, userID).
		First(&conversation).Error; err != nil {
		return PlatformGenerationReceiptSnapshot{}, platformGenerationReceiptReferenceError(err)
	}
	if conversation.ID <= 0 || conversation.Guid <= 0 || conversation.UserID != userID {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}

	var userMessage models.Message
	if err := db.WithContext(ctx).
		Where("id = ? AND conversation_id = ? AND is_deleted = 0", receipt.UserMessageID, receipt.ConversationID).
		First(&userMessage).Error; err != nil {
		return PlatformGenerationReceiptSnapshot{}, platformGenerationReceiptReferenceError(err)
	}
	if !validPlatformGenerationReceiptUserMessage(userMessage, receipt) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}

	var rows []models.PlatformChatGenerationResult
	if err := db.WithContext(ctx).
		Where("receipt_id = ?", receipt.ID).
		Order("model_index ASC").
		Find(&rows).Error; err != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable
	}
	if !validPlatformGenerationReceiptResultSet(rows, receipt.ID, receipt.UserID) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}
	if !validPlatformGenerationReceiptCardinality(receipt.Mode, len(rows)) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}

	snapshot := PlatformGenerationReceiptSnapshot{
		UserID:                        receipt.UserID,
		GenerationID:                  receipt.GenerationID,
		Mode:                          PlatformGenerationMode(receipt.Mode),
		ConversationGUID:              conversation.Guid,
		RequestedExistingConversation: receipt.RequestedExistingConversation == 1,
		UserMessage:                   userMessage.Content,
		SuccessfulModelCount:          receipt.SuccessfulModelCount,
		DailyCallsCharged:             receipt.DailyCallsCharged,
		TotalTokens:                   receipt.TotalTokens,
		CommittedAtMillis:             receipt.CommittedAt,
		Results:                       make([]PlatformGenerationCommittedResult, 0, len(rows)),
	}
	successes := 0
	var totalTokens int64
	for _, row := range rows {
		result := PlatformGenerationCommittedResult{Model: row.Model}
		switch row.Status {
		case models.PlatformGenerationResultCompleted:
			var assistant models.Message
			if err := db.WithContext(ctx).
				Where("id = ? AND conversation_id = ? AND is_deleted = 0", *row.AssistantMessageID, receipt.ConversationID).
				First(&assistant).Error; err != nil {
				return PlatformGenerationReceiptSnapshot{}, platformGenerationReceiptReferenceError(err)
			}
			if !validPlatformGenerationReceiptAssistantMessage(assistant, row) {
				return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
			}
			result.State = PlatformGenerationStateCompleted
			result.AssistantMessageGUID = strconv.FormatInt(assistant.Guid, 10)
			result.Content = assistant.Content
			result.Tokens = row.Tokens
			successes++
			totalTokens += row.Tokens
		case models.PlatformGenerationResultFailed:
			result.State = PlatformGenerationStateFailed
			result.ErrorCode = *row.ErrorCode
		}
		snapshot.Results = append(snapshot.Results, result)
	}
	if successes < 1 || successes != receipt.SuccessfulModelCount || totalTokens != receipt.TotalTokens ||
		(receipt.Mode == models.PlatformGenerationReceiptModeSingle && successes != 1) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}
	return snapshot, nil
}

func validPlatformGenerationReceiptParent(receipt models.PlatformChatGenerationReceipt, userID int64, generationID string) bool {
	if receipt.ID <= 0 || receipt.Guid <= 0 || receipt.UserID != userID || receipt.GenerationID != generationID ||
		receipt.ConversationID <= 0 || receipt.UserMessageID <= 0 ||
		(receipt.RequestedExistingConversation != 0 && receipt.RequestedExistingConversation != 1) ||
		receipt.SuccessfulModelCount < 1 || receipt.DailyCallsCharged != receipt.SuccessfulModelCount || receipt.TotalTokens < 0 ||
		receipt.CommittedAt <= 0 || !platformSSEV2SafeInteger(receipt.CommittedAt) ||
		receipt.CreatedAt <= 0 || receipt.UpdatedAt != receipt.CreatedAt ||
		receipt.CreatedBy == nil || *receipt.CreatedBy != userID || receipt.UpdatedBy == nil || *receipt.UpdatedBy != userID {
		return false
	}
	return receipt.Mode == models.PlatformGenerationReceiptModeSingle || receipt.Mode == models.PlatformGenerationReceiptModeCompare
}

func validPlatformGenerationReceiptCardinality(mode models.PlatformGenerationReceiptMode, count int) bool {
	switch mode {
	case models.PlatformGenerationReceiptModeSingle:
		return count == 1
	case models.PlatformGenerationReceiptModeCompare:
		return count >= 2 && count <= platformSSEV2MaxModels
	default:
		return false
	}
}

func validPlatformGenerationReceiptUserMessage(message models.Message, receipt models.PlatformChatGenerationReceipt) bool {
	return message.ID == receipt.UserMessageID && message.ID > 0 && message.Guid > 0 &&
		message.ConversationID == receipt.ConversationID && message.Role == models.MessageRoleUser &&
		message.Content != "" && utf8.ValidString(message.Content) && len([]byte(message.Content)) <= platformGenerationMessageTextMaxBytes &&
		message.Model == nil && message.Tokens == 0
}

func validPlatformGenerationReceiptResultIdentity(row models.PlatformChatGenerationResult, receiptID, userID int64, index int) bool {
	return row.ID > 0 && row.Guid > 0 && row.ReceiptID == receiptID && row.ModelIndex == index &&
		platformSSEV2ModelIdentifier(row.Model) && row.CreatedAt > 0 && row.UpdatedAt == row.CreatedAt &&
		row.CreatedBy != nil && *row.CreatedBy == userID && row.UpdatedBy != nil && *row.UpdatedBy == userID
}

// validPlatformGenerationReceiptResultSet validates every row-local and
// cross-row invariant before the reader follows any assistant message ID.
func validPlatformGenerationReceiptResultSet(rows []models.PlatformChatGenerationResult, receiptID, userID int64) bool {
	seenModels := make(map[string]struct{}, len(rows))
	seenAssistantMessages := make(map[int64]struct{}, len(rows))
	for index, row := range rows {
		if row.IsDeleted != 0 || !validPlatformGenerationReceiptResultIdentity(row, receiptID, userID, index) {
			return false
		}
		if _, duplicate := seenModels[row.Model]; duplicate {
			return false
		}
		seenModels[row.Model] = struct{}{}
		switch row.Status {
		case models.PlatformGenerationResultCompleted:
			if row.AssistantMessageID == nil || *row.AssistantMessageID <= 0 || row.ErrorCode != nil || row.Tokens < 0 || row.Tokens > math.MaxInt32 {
				return false
			}
			if _, duplicate := seenAssistantMessages[*row.AssistantMessageID]; duplicate {
				return false
			}
			seenAssistantMessages[*row.AssistantMessageID] = struct{}{}
		case models.PlatformGenerationResultFailed:
			if row.AssistantMessageID != nil || row.Tokens != 0 || row.ErrorCode == nil || !platformGenerationStableCode(*row.ErrorCode) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validPlatformGenerationReceiptAssistantMessage(message models.Message, row models.PlatformChatGenerationResult) bool {
	return row.AssistantMessageID != nil && message.ID == *row.AssistantMessageID && message.ID > 0 && message.Guid > 0 &&
		message.Role == models.MessageRoleAssistant && message.Model != nil && *message.Model == row.Model &&
		message.Tokens >= 0 && int64(message.Tokens) == row.Tokens && message.Content != "" &&
		utf8.ValidString(message.Content) && len([]byte(message.Content)) <= platformGenerationMessageTextMaxBytes
}

func platformGenerationReceiptReferenceError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrPlatformGenerationPersistenceIntegrity
	}
	return ErrPlatformGenerationPersistenceUnavailable
}
