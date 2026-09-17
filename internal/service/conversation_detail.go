package service

import (
	"sort"
	"strconv"

	"github.com/porsche/ai-gateway-go/internal/models"
)

type ConversationGenerationResult struct {
	Model                string
	Status               string
	AssistantMessageGUID *string
	Tokens               int64
	ErrorCode            *string
}

type ConversationGenerationGroup struct {
	GenerationID    string
	Mode            string
	UserMessageGUID string
	Results         []ConversationGenerationResult
}

type ConversationDetail struct {
	Conversation                *models.Conversation
	GenerationGroups            []ConversationGenerationGroup
	OmittedGenerationGroupCount int
}

// buildConversationGenerationGroups creates the public compare-history
// projection from already-loaded rows. It performs no I/O and never mutates
// its inputs.
func buildConversationGenerationGroups(
	userID int64,
	conv *models.Conversation,
	receipts []models.PlatformChatGenerationReceipt,
	rows []models.PlatformChatGenerationResult,
) ([]ConversationGenerationGroup, int) {
	compareReceiptCount := 0
	for _, receipt := range receipts {
		if receipt.Mode == models.PlatformGenerationReceiptModeCompare {
			compareReceiptCount++
		}
	}
	if conv == nil || conv.ID <= 0 || conv.UserID != userID || conv.IsDeleted != 0 {
		return nil, compareReceiptCount
	}

	messagesByID := make(map[int64][]models.Message, len(conv.Messages))
	for _, message := range conv.Messages {
		messagesByID[message.ID] = append(messagesByID[message.ID], message)
	}
	rowsByReceiptID := make(map[int64][]models.PlatformChatGenerationResult, len(receipts))
	for _, row := range rows {
		rowsByReceiptID[row.ReceiptID] = append(rowsByReceiptID[row.ReceiptID], row)
	}

	groups := make([]ConversationGenerationGroup, 0, compareReceiptCount)
	omitted := 0
	usedUserIDs := make(map[int64]struct{}, compareReceiptCount)
	usedUserGUIDs := make(map[int64]struct{}, compareReceiptCount)
	usedAssistantIDs := make(map[int64]struct{})
	usedAssistantGUIDs := make(map[int64]struct{})

	for _, receipt := range receipts {
		if receipt.Mode != models.PlatformGenerationReceiptModeCompare {
			continue
		}

		if !validPlatformGenerationReceiptParent(receipt, userID, receipt.GenerationID) ||
			validatePlatformGenerationIdentity(userID, receipt.GenerationID) != nil ||
			receipt.ConversationID != conv.ID {
			omitted++
			continue
		}

		userMessages := messagesByID[receipt.UserMessageID]
		if len(userMessages) != 1 || userMessages[0].ConversationID != conv.ID || userMessages[0].IsDeleted != 0 ||
			!validPlatformGenerationReceiptUserMessage(userMessages[0], receipt) {
			omitted++
			continue
		}
		userMessage := userMessages[0]

		receiptRows := append([]models.PlatformChatGenerationResult(nil), rowsByReceiptID[receipt.ID]...)
		sort.SliceStable(receiptRows, func(left, right int) bool {
			return receiptRows[left].ModelIndex < receiptRows[right].ModelIndex
		})
		if !validPlatformGenerationReceiptResultSet(receiptRows, receipt.ID, receipt.UserID) ||
			!validPlatformGenerationReceiptCardinality(receipt.Mode, len(receiptRows)) {
			omitted++
			continue
		}

		group := ConversationGenerationGroup{
			GenerationID:    receipt.GenerationID,
			Mode:            receipt.Mode.String(),
			UserMessageGUID: strconv.FormatInt(userMessage.Guid, 10),
			Results:         make([]ConversationGenerationResult, 0, len(receiptRows)),
		}
		assistantIDs := make([]int64, 0, len(receiptRows))
		assistantGUIDs := make([]int64, 0, len(receiptRows))
		groupAssistantGUIDs := make(map[int64]struct{}, len(receiptRows))
		valid := true
		successes := 0
		var totalTokens int64

		for _, row := range receiptRows {
			result := ConversationGenerationResult{Model: row.Model, Status: row.Status.String()}
			switch row.Status {
			case models.PlatformGenerationResultCompleted:
				assistantMessages := messagesByID[*row.AssistantMessageID]
				if len(assistantMessages) != 1 || assistantMessages[0].ConversationID != conv.ID || assistantMessages[0].IsDeleted != 0 ||
					!validPlatformGenerationReceiptAssistantMessage(assistantMessages[0], row) {
					valid = false
					break
				}
				assistant := assistantMessages[0]
				if _, duplicate := groupAssistantGUIDs[assistant.Guid]; duplicate {
					valid = false
					break
				}
				groupAssistantGUIDs[assistant.Guid] = struct{}{}
				guid := strconv.FormatInt(assistant.Guid, 10)
				result.AssistantMessageGUID = &guid
				result.Tokens = row.Tokens
				assistantIDs = append(assistantIDs, assistant.ID)
				assistantGUIDs = append(assistantGUIDs, assistant.Guid)
				successes++
				totalTokens += row.Tokens
			case models.PlatformGenerationResultFailed:
				code := *row.ErrorCode
				result.ErrorCode = &code
			default:
				valid = false
			}
			if !valid {
				break
			}
			group.Results = append(group.Results, result)
		}

		if valid && (successes != receipt.SuccessfulModelCount || totalTokens != receipt.TotalTokens) {
			valid = false
		}
		if valid {
			if _, duplicate := usedUserIDs[userMessage.ID]; duplicate {
				valid = false
			}
			if _, duplicate := usedUserGUIDs[userMessage.Guid]; duplicate {
				valid = false
			}
		}
		if valid {
			for index := range assistantIDs {
				if _, duplicate := usedAssistantIDs[assistantIDs[index]]; duplicate {
					valid = false
					break
				}
				if _, duplicate := usedAssistantGUIDs[assistantGUIDs[index]]; duplicate {
					valid = false
					break
				}
			}
		}
		if !valid {
			omitted++
			continue
		}

		usedUserIDs[userMessage.ID] = struct{}{}
		usedUserGUIDs[userMessage.Guid] = struct{}{}
		for index := range assistantIDs {
			usedAssistantIDs[assistantIDs[index]] = struct{}{}
			usedAssistantGUIDs[assistantGUIDs[index]] = struct{}{}
		}
		groups = append(groups, group)
	}

	return groups, omitted
}
