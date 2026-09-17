package service

import (
	"reflect"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestBuildConversationGenerationGroupsProjectsResultsInModelIndexOrder(t *testing.T) {
	fixture := newConversationGenerationGroupFixture()
	failedCode := "upstream_error"
	modelC := "model-c"
	fixture.conversation.Messages[2].Model = &modelC
	fixture.conversation.Messages[2].Tokens = 30
	fixture.receipt.SuccessfulModelCount = 2
	fixture.receipt.DailyCallsCharged = 2
	fixture.receipt.TotalTokens = 40
	fixture.rows = []models.PlatformChatGenerationResult{
		fixture.completedResult(3, 3003, 2, "model-c", 23, 30),
		fixture.completedResult(1, 3001, 0, "model-a", 22, 10),
		fixture.failedResult(2, 3002, 1, "model-b", failedCode),
	}
	originalRowIDs := []int64{fixture.rows[0].ID, fixture.rows[1].ID, fixture.rows[2].ID}

	groups, omitted := buildConversationGenerationGroups(fixture.userID, &fixture.conversation, []models.PlatformChatGenerationReceipt{fixture.receipt}, fixture.rows)

	if omitted != 0 || len(groups) != 1 {
		t.Fatalf("groups=%d omitted=%d, want 1/0", len(groups), omitted)
	}
	group := groups[0]
	if group.GenerationID != fixture.receipt.GenerationID || group.Mode != "compare" || group.UserMessageGUID != "1001" {
		t.Fatalf("unexpected group identity: %+v", group)
	}
	if got := []string{group.Results[0].Model, group.Results[1].Model, group.Results[2].Model}; !reflect.DeepEqual(got, []string{"model-a", "model-b", "model-c"}) {
		t.Fatalf("result order=%v", got)
	}
	if got := group.Results[0]; got.Status != "completed" || got.AssistantMessageGUID == nil || *got.AssistantMessageGUID != "1002" || got.Tokens != 10 || got.ErrorCode != nil {
		t.Fatalf("completed result=%+v", got)
	}
	if got := group.Results[1]; got.Status != "failed" || got.AssistantMessageGUID != nil || got.Tokens != 0 || got.ErrorCode == nil || *got.ErrorCode != failedCode {
		t.Fatalf("failed result=%+v", got)
	}
	if got := group.Results[2]; got.Status != "completed" || got.AssistantMessageGUID == nil || *got.AssistantMessageGUID != "1003" || got.Tokens != 30 || got.ErrorCode != nil {
		t.Fatalf("completed result=%+v", got)
	}
	if got := []int64{fixture.rows[0].ID, fixture.rows[1].ID, fixture.rows[2].ID}; !reflect.DeepEqual(got, originalRowIDs) {
		t.Fatalf("input row order mutated: %v", got)
	}
}

func TestBuildConversationGenerationGroupsExcludesSingleReceipts(t *testing.T) {
	fixture := newConversationGenerationGroupFixture()
	fixture.receipt.Mode = models.PlatformGenerationReceiptModeSingle
	fixture.receipt.SuccessfulModelCount = 1
	fixture.receipt.DailyCallsCharged = 1
	fixture.receipt.TotalTokens = 10
	rows := []models.PlatformChatGenerationResult{fixture.completedResult(1, 3001, 0, "model-a", 22, 10)}

	groups, omitted := buildConversationGenerationGroups(fixture.userID, &fixture.conversation, []models.PlatformChatGenerationReceipt{fixture.receipt}, rows)

	if len(groups) != 0 || omitted != 0 {
		t.Fatalf("groups=%d omitted=%d, want 0/0", len(groups), omitted)
	}
}

func TestBuildConversationGenerationGroupsRejectsCrossConversationReferences(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*conversationGenerationGroupFixture)
	}{
		{
			name: "user message",
			mutate: func(f *conversationGenerationGroupFixture) {
				f.conversation.Messages[0].ConversationID++
			},
		},
		{
			name: "assistant message",
			mutate: func(f *conversationGenerationGroupFixture) {
				f.conversation.Messages[1].ConversationID++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConversationGenerationGroupFixture()
			test.mutate(&fixture)
			groups, omitted := buildConversationGenerationGroups(fixture.userID, &fixture.conversation, []models.PlatformChatGenerationReceipt{fixture.receipt}, fixture.rows)
			if len(groups) != 0 || omitted != 1 {
				t.Fatalf("groups=%d omitted=%d, want 0/1", len(groups), omitted)
			}
		})
	}
}

func TestBuildConversationGenerationGroupsRejectsDuplicateAssistantReference(t *testing.T) {
	fixture := newConversationGenerationGroupFixture()
	fixture.rows[1].AssistantMessageID = fixture.rows[0].AssistantMessageID
	fixture.rows[1].Tokens = fixture.rows[0].Tokens

	groups, omitted := buildConversationGenerationGroups(fixture.userID, &fixture.conversation, []models.PlatformChatGenerationReceipt{fixture.receipt}, fixture.rows)

	if len(groups) != 0 || omitted != 1 {
		t.Fatalf("groups=%d omitted=%d, want 0/1", len(groups), omitted)
	}
}

func TestBuildConversationGenerationGroupsRejectsGUIDReuseAcrossGroups(t *testing.T) {
	tests := []struct {
		name               string
		reuseUserGUID      bool
		reuseAssistantGUID bool
	}{
		{name: "user guid", reuseUserGUID: true},
		{name: "assistant guid", reuseAssistantGUID: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConversationGenerationGroupFixture()
			secondReceipt, secondRows := fixture.addSecondGroup(test.reuseUserGUID, test.reuseAssistantGUID)

			groups, omitted := buildConversationGenerationGroups(
				fixture.userID,
				&fixture.conversation,
				[]models.PlatformChatGenerationReceipt{fixture.receipt, secondReceipt},
				append(fixture.rows, secondRows...),
			)

			if len(groups) != 1 || omitted != 1 {
				t.Fatalf("groups=%d omitted=%d, want first group only and one omitted", len(groups), omitted)
			}
			if groups[0].GenerationID != fixture.receipt.GenerationID {
				t.Fatalf("retained generation=%q, want first %q", groups[0].GenerationID, fixture.receipt.GenerationID)
			}
		})
	}
}

func TestBuildConversationGenerationGroupsPublicTypesContainNoInternalIDs(t *testing.T) {
	assertFields := func(value any, want []string) {
		t.Helper()
		typ := reflect.TypeOf(value)
		got := make([]string, 0, typ.NumField())
		for index := 0; index < typ.NumField(); index++ {
			got = append(got, typ.Field(index).Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s fields=%v, want %v", typ.Name(), got, want)
		}
	}
	assertFields(ConversationGenerationResult{}, []string{"Model", "Status", "AssistantMessageGUID", "Tokens", "ErrorCode"})
	assertFields(ConversationGenerationGroup{}, []string{"GenerationID", "Mode", "UserMessageGUID", "Results"})
	assertFields(ConversationDetail{}, []string{"Conversation", "GenerationGroups", "OmittedGenerationGroupCount"})
}

type conversationGenerationGroupFixture struct {
	userID       int64
	conversation models.Conversation
	receipt      models.PlatformChatGenerationReceipt
	rows         []models.PlatformChatGenerationResult
}

func newConversationGenerationGroupFixture() conversationGenerationGroupFixture {
	userID := int64(7)
	modelA, modelB := "model-a", "model-b"
	conversation := models.Conversation{
		ID:          11,
		AuditFields: models.AuditFields{Guid: 1000, CreatedAt: 100, UpdatedAt: 100},
		UserID:      userID,
		Title:       "grouped history",
		Messages: []models.Message{
			{ID: 21, AuditFields: models.AuditFields{Guid: 1001, CreatedAt: 101, UpdatedAt: 101}, ConversationID: 11, Role: models.MessageRoleUser, Content: "prompt"},
			{ID: 22, AuditFields: models.AuditFields{Guid: 1002, CreatedAt: 102, UpdatedAt: 102}, ConversationID: 11, Role: models.MessageRoleAssistant, Content: "answer-a", Model: &modelA, Tokens: 10},
			{ID: 23, AuditFields: models.AuditFields{Guid: 1003, CreatedAt: 103, UpdatedAt: 103}, ConversationID: 11, Role: models.MessageRoleAssistant, Content: "answer-b", Model: &modelB, Tokens: 20},
		},
	}
	receipt := validConversationGenerationReceipt(userID, 31, 2001, "01234567-89ab-4cde-8f01-23456789abcd", 11, 21, 2, 30)
	fixture := conversationGenerationGroupFixture{userID: userID, conversation: conversation, receipt: receipt}
	fixture.rows = []models.PlatformChatGenerationResult{
		fixture.completedResult(1, 3001, 0, modelA, 22, 10),
		fixture.completedResult(2, 3002, 1, modelB, 23, 20),
	}
	return fixture
}

func (f conversationGenerationGroupFixture) completedResult(id, guid int64, index int, model string, assistantID, tokens int64) models.PlatformChatGenerationResult {
	return models.PlatformChatGenerationResult{
		ID:                 id,
		AuditFields:        validConversationGenerationAudit(guid, f.userID),
		ReceiptID:          f.receipt.ID,
		ModelIndex:         index,
		Model:              model,
		Status:             models.PlatformGenerationResultCompleted,
		AssistantMessageID: &assistantID,
		Tokens:             tokens,
	}
}

func (f conversationGenerationGroupFixture) failedResult(id, guid int64, index int, model, code string) models.PlatformChatGenerationResult {
	return models.PlatformChatGenerationResult{
		ID:          id,
		AuditFields: validConversationGenerationAudit(guid, f.userID),
		ReceiptID:   f.receipt.ID,
		ModelIndex:  index,
		Model:       model,
		Status:      models.PlatformGenerationResultFailed,
		ErrorCode:   &code,
	}
}

func (f *conversationGenerationGroupFixture) addSecondGroup(reuseUserGUID, reuseAssistantGUID bool) (models.PlatformChatGenerationReceipt, []models.PlatformChatGenerationResult) {
	modelC, modelD := "model-c", "model-d"
	userGUID := int64(1011)
	if reuseUserGUID {
		userGUID = f.conversation.Messages[0].Guid
	}
	assistantGUID := int64(1012)
	if reuseAssistantGUID {
		assistantGUID = f.conversation.Messages[1].Guid
	}
	f.conversation.Messages = append(f.conversation.Messages,
		models.Message{ID: 24, AuditFields: models.AuditFields{Guid: userGUID, CreatedAt: 104, UpdatedAt: 104}, ConversationID: f.conversation.ID, Role: models.MessageRoleUser, Content: "second prompt"},
		models.Message{ID: 25, AuditFields: models.AuditFields{Guid: assistantGUID, CreatedAt: 105, UpdatedAt: 105}, ConversationID: f.conversation.ID, Role: models.MessageRoleAssistant, Content: "answer-c", Model: &modelC, Tokens: 30},
		models.Message{ID: 26, AuditFields: models.AuditFields{Guid: 1013, CreatedAt: 106, UpdatedAt: 106}, ConversationID: f.conversation.ID, Role: models.MessageRoleAssistant, Content: "answer-d", Model: &modelD, Tokens: 40},
	)
	receipt := validConversationGenerationReceipt(f.userID, 32, 2002, "11234567-89ab-4cde-8f01-23456789abcd", f.conversation.ID, 24, 2, 70)
	rowOne := models.PlatformChatGenerationResult{ID: 4, AuditFields: validConversationGenerationAudit(3004, f.userID), ReceiptID: receipt.ID, ModelIndex: 0, Model: modelC, Status: models.PlatformGenerationResultCompleted, AssistantMessageID: int64Pointer(25), Tokens: 30}
	rowTwo := models.PlatformChatGenerationResult{ID: 5, AuditFields: validConversationGenerationAudit(3005, f.userID), ReceiptID: receipt.ID, ModelIndex: 1, Model: modelD, Status: models.PlatformGenerationResultCompleted, AssistantMessageID: int64Pointer(26), Tokens: 40}
	return receipt, []models.PlatformChatGenerationResult{rowOne, rowTwo}
}

func validConversationGenerationReceipt(userID, id, guid int64, generationID string, conversationID, userMessageID int64, successes int, totalTokens int64) models.PlatformChatGenerationReceipt {
	return models.PlatformChatGenerationReceipt{
		ID:                            id,
		AuditFields:                   validConversationGenerationAudit(guid, userID),
		UserID:                        userID,
		GenerationID:                  generationID,
		Mode:                          models.PlatformGenerationReceiptModeCompare,
		ConversationID:                conversationID,
		UserMessageID:                 userMessageID,
		SuccessfulModelCount:          successes,
		DailyCallsCharged:             successes,
		TotalTokens:                   totalTokens,
		CommittedAt:                   1000 + id,
		RequestedExistingConversation: 1,
	}
}

func validConversationGenerationAudit(guid, userID int64) models.AuditFields {
	return models.AuditFields{Guid: guid, CreatedAt: 900, CreatedBy: &userID, UpdatedAt: 900, UpdatedBy: &userID}
}

func int64Pointer(value int64) *int64 { return &value }
