package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
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
	db := openConversationDetailSyntheticDB(t, false)
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

func TestGetConversationDetailSanitizesConversationQueryErrors(t *testing.T) {
	db := openConversationDetailSyntheticDB(t, false)
	const callbackName = "conversation_detail_test:conversation_query_error"
	const secret = "conversation-query-sql-secret"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "conversations" {
			tx.AddError(errors.New(secret))
		}
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := GetConversationDetail(context.Background(), db, &models.User{ID: 1}, 42)
	status, message := StatusFromError(err)
	if detail != nil || status != 503 || message != "会话详情不可用" || strings.Contains(message, secret) {
		t.Fatalf("detail=%#v status=%d message=%q", detail, status, message)
	}
}

func TestGetConversationDetailPreservesConversationQueryCancellation(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{
			name: "canceled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			want: context.Canceled,
		},
		{
			name: "deadline exceeded",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Unix(1, 0))
			},
			want: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openConversationDetailSyntheticDB(t, false)
			ctx, cancel := test.ctx()
			defer cancel()

			detail, err := GetConversationDetail(ctx, db, &models.User{ID: 1}, 42)
			if detail != nil || !errors.Is(err, test.want) {
				t.Fatalf("detail=%#v error=%v, want %v", detail, err, test.want)
			}
		})
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

func TestLoadConversationGenerationResultsBatchesReceiptIDs(t *testing.T) {
	db := openConversationDetailSyntheticDB(t, true)
	batchSizes := make([]int, 0, 2)
	const callbackName = "conversation_detail_test:record_result_batches"
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (models.PlatformChatGenerationResult{}).TableName() {
			batchSizes = append(batchSizes, len(tx.Statement.Vars))
		}
	}); err != nil {
		t.Fatal(err)
	}
	receipts := make([]models.PlatformChatGenerationReceipt, 501)
	for index := range receipts {
		receipts[index].ID = int64(index + 1)
	}

	results, err := loadConversationGenerationResults(db, receipts)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 || !reflect.DeepEqual(batchSizes, []int{500, 1}) {
		t.Fatalf("results=%d batch sizes=%v, want 0 and [500 1]", len(results), batchSizes)
	}
	for _, size := range batchSizes {
		if size > 500 {
			t.Fatalf("batch size=%d exceeds 500", size)
		}
	}
}

func TestLoadConversationGenerationResultsFailsClosedOnLaterBatchError(t *testing.T) {
	db := openConversationDetailSyntheticDB(t, true)
	const callbackName = "conversation_detail_test:fail_second_result_batch"
	const secret = "second-result-batch-secret"
	calls := 0
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != (models.PlatformChatGenerationResult{}).TableName() {
			return
		}
		calls++
		if calls == 1 {
			rows, ok := tx.Statement.Dest.(*[]models.PlatformChatGenerationResult)
			if !ok {
				t.Fatalf("result destination type=%T", tx.Statement.Dest)
			}
			*rows = append(*rows, models.PlatformChatGenerationResult{ID: 1, ReceiptID: 1})
			return
		}
		tx.AddError(errors.New(secret))
	}); err != nil {
		t.Fatal(err)
	}
	receipts := make([]models.PlatformChatGenerationReceipt, 501)
	for index := range receipts {
		receipts[index].ID = int64(index + 1)
	}

	results, err := loadConversationGenerationResults(db, receipts)
	if results != nil || err == nil || !strings.Contains(err.Error(), secret) || calls != 2 {
		t.Fatalf("results=%#v error=%v calls=%d, want nil/raw second-batch error/2", results, err, calls)
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

func openConversationDetailSyntheticDB(t *testing.T, dryRun bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "test:test@tcp(127.0.0.1:1)/conversation_detail_test",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: dryRun, Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}
