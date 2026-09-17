package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestGetConversationDetailRejectsUnavailableDependencies(t *testing.T) {
	validUser := &models.User{ID: 1}
	for _, test := range []struct {
		name string
		ctx  context.Context
		db   *gorm.DB
		user *models.User
	}{
		{name: "nil context", db: &gorm.DB{}, user: validUser},
		{name: "nil database", ctx: context.Background(), user: validUser},
		{name: "nil user", ctx: context.Background(), db: &gorm.DB{}},
		{name: "invalid user", ctx: context.Background(), db: &gorm.DB{}, user: &models.User{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			detail, err := GetConversationDetail(test.ctx, test.db, test.user, 1)
			status, message := StatusFromError(err)
			if detail != nil || status != 503 || message != "会话详情不可用" {
				t.Fatalf("detail=%#v status=%d message=%q", detail, status, message)
			}
		})
	}
}

func TestGetConversationDetailPreservesOwnerBoundNotFound(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "test:test@tcp(127.0.0.1:1)/conversation_detail_test",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	const callbackName = "conversation_detail_test:owner_not_found"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "conversations" {
			tx.AddError(gorm.ErrRecordNotFound)
		}
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := GetConversationDetail(context.Background(), db, &models.User{ID: 1}, 42)
	status, message := StatusFromError(err)
	if detail != nil || status != 404 || message != "对话不存在" {
		t.Fatalf("detail=%#v status=%d message=%q, want owner-bound 404", detail, status, message)
	}
}

func TestGetConversationDetailRejectsForeignConversationAsNotFound(t *testing.T) {
	fixture := seedPlatformGenerationReceiptWithTrailingFailure(t)
	detail, err := GetConversationDetail(context.Background(), fixture.db, &fixture.other, fixture.conversation.Guid)
	status, message := StatusFromError(err)
	if detail != nil || status != 404 || message != "对话不存在" {
		t.Fatalf("detail=%#v status=%d message=%q, want owner-bound 404", detail, status, message)
	}
}

func TestGetConversationDetailLoadsOwnedCompareGroupAndKeepsRawMessages(t *testing.T) {
	fixture := seedPlatformGenerationReceiptWithTrailingFailure(t)
	seedConversationDetailReceipt(t, fixture.db, fixture.other, fixture.conversation, models.PlatformGenerationReceiptModeCompare, "11111111-1111-4111-8111-111111111111")
	seedConversationDetailReceipt(t, fixture.db, fixture.owner, fixture.otherConversation, models.PlatformGenerationReceiptModeCompare, "22222222-2222-4222-8222-222222222222")
	seedConversationDetailReceipt(t, fixture.db, fixture.owner, fixture.conversation, models.PlatformGenerationReceiptModeSingle, "33333333-3333-4333-8333-333333333333")

	const tiedCreatedAt = int64(1_800_000_000_000)
	if err := fixture.db.Model(&models.Message{}).
		Where("conversation_id = ?", fixture.conversation.ID).
		Updates(map[string]any{"created_at": tiedCreatedAt, "updated_at": tiedCreatedAt}).Error; err != nil {
		t.Fatal(err)
	}
	var expectedMessages []models.Message
	if err := fixture.db.Where("conversation_id = ? AND is_deleted = 0", fixture.conversation.ID).
		Order("created_at asc, id asc").Find(&expectedMessages).Error; err != nil {
		t.Fatal(err)
	}

	detail, err := GetConversationDetail(context.Background(), fixture.db, &fixture.owner, fixture.conversation.Guid)
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || detail.Conversation == nil || detail.Conversation.Guid != fixture.conversation.Guid {
		t.Fatalf("unexpected conversation detail: %#v", detail)
	}
	if len(detail.Conversation.Messages) != len(expectedMessages) {
		t.Fatalf("raw message count=%d, want %d", len(detail.Conversation.Messages), len(expectedMessages))
	}
	gotMessageIDs := make([]int64, 0, len(detail.Conversation.Messages))
	wantMessageIDs := make([]int64, 0, len(expectedMessages))
	for index, message := range detail.Conversation.Messages {
		gotMessageIDs = append(gotMessageIDs, message.ID)
		wantMessageIDs = append(wantMessageIDs, expectedMessages[index].ID)
		if message.CreatedAt != tiedCreatedAt {
			t.Fatalf("message %d created_at=%d, want tied timestamp", message.ID, message.CreatedAt)
		}
	}
	if !reflect.DeepEqual(gotMessageIDs, wantMessageIDs) {
		t.Fatalf("message order=%v, want %v", gotMessageIDs, wantMessageIDs)
	}
	if len(detail.GenerationGroups) != 1 || detail.OmittedGenerationGroupCount != 0 {
		t.Fatalf("groups=%d omitted=%d, want 1/0", len(detail.GenerationGroups), detail.OmittedGenerationGroupCount)
	}
	group := detail.GenerationGroups[0]
	if group.GenerationID != fixture.receipt.GenerationID || group.UserMessageGUID != stringInt64(fixture.userMessage.Guid) || len(group.Results) != len(fixture.results) {
		t.Fatalf("unexpected owned compare group: %#v", group)
	}
}

func TestGetConversationDetailSkipsResultQueryWhenNoCompareReceipts(t *testing.T) {
	fixture := seedPlatformGenerationReceiptWithTrailingFailure(t)
	const callbackName = "conversation_detail_test:reject_empty_result_query"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (models.PlatformChatGenerationResult{}).TableName() {
			tx.AddError(errors.New("result query must not execute"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callbackName) })

	detail, err := GetConversationDetail(context.Background(), fixture.db, &fixture.owner, fixture.otherConversation.Guid)
	if err != nil {
		t.Fatal(err)
	}
	if detail.GenerationGroups == nil || len(detail.GenerationGroups) != 0 || detail.OmittedGenerationGroupCount != 0 {
		t.Fatalf("groups=%#v omitted=%d, want non-nil empty/0", detail.GenerationGroups, detail.OmittedGenerationGroupCount)
	}
}

func TestGetConversationDetailSanitizesDatabaseQueryErrors(t *testing.T) {
	fixture := seedPlatformGenerationReceiptWithTrailingFailure(t)
	const callbackName = "conversation_detail_test:fail_receipt_query"
	const secret = "sql-secret-table-detail"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (models.PlatformChatGenerationReceipt{}).TableName() {
			tx.AddError(errors.New(secret))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callbackName) })

	detail, err := GetConversationDetail(context.Background(), fixture.db, &fixture.owner, fixture.conversation.Guid)
	status, message := StatusFromError(err)
	if detail != nil || status != 503 || message != "会话详情不可用" || strings.Contains(message, secret) {
		t.Fatalf("detail=%#v status=%d message=%q", detail, status, message)
	}
}

func seedConversationDetailReceipt(
	t *testing.T,
	db *gorm.DB,
	owner models.User,
	conversation models.Conversation,
	mode models.PlatformGenerationReceiptMode,
	generationID string,
) models.PlatformChatGenerationReceipt {
	t.Helper()
	now := int64(1_800_000_000_100)
	audit := func() models.AuditFields {
		return models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, CreatedBy: &owner.ID, UpdatedAt: now, UpdatedBy: &owner.ID}
	}
	userMessage := models.Message{AuditFields: audit(), ConversationID: conversation.ID, Role: models.MessageRoleUser, Content: "detail prompt"}
	if err := db.Create(&userMessage).Error; err != nil {
		t.Fatal(err)
	}

	modelsInOrder := []string{"detail-single"}
	if mode == models.PlatformGenerationReceiptModeCompare {
		modelsInOrder = []string{"detail-a", "detail-b"}
	}
	assistantMessages := make([]models.Message, 0, len(modelsInOrder))
	var totalTokens int64
	for index, model := range modelsInOrder {
		modelCopy := model
		message := models.Message{
			AuditFields: audit(), ConversationID: conversation.ID, Role: models.MessageRoleAssistant,
			Content: "answer-" + model, Model: &modelCopy, Tokens: index + 1,
		}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		assistantMessages = append(assistantMessages, message)
		totalTokens += int64(message.Tokens)
	}
	receipt := models.PlatformChatGenerationReceipt{
		AuditFields: audit(), UserID: owner.ID, GenerationID: generationID, Mode: mode,
		RequestedExistingConversation: 1, ConversationID: conversation.ID, UserMessageID: userMessage.ID,
		SuccessfulModelCount: len(modelsInOrder), DailyCallsCharged: len(modelsInOrder), TotalTokens: totalTokens, CommittedAt: now,
	}
	if err := db.Create(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	for index, model := range modelsInOrder {
		messageID := assistantMessages[index].ID
		result := models.PlatformChatGenerationResult{
			AuditFields: audit(), ReceiptID: receipt.ID, ModelIndex: index, Model: model,
			Status: models.PlatformGenerationResultCompleted, AssistantMessageID: &messageID, Tokens: int64(assistantMessages[index].Tokens),
		}
		if err := db.Create(&result).Error; err != nil {
			t.Fatal(err)
		}
	}
	return receipt
}
