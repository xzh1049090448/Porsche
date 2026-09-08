package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func validPlatformGenerationPersistenceInput() PlatformGenerationPersistenceInput {
	return PlatformGenerationPersistenceInput{
		UserID:       1,
		GenerationID: generationTestID,
		Mode:         PlatformGenerationModeSingle,
		Models:       []string{"model-a"},
		UserMessage:  " keep whitespace byte-for-byte ",
		Results: []PlatformGenerationPersistenceResult{{
			Model: "model-a", State: PlatformGenerationStateCompleted, Content: "answer", Tokens: 1,
		}},
		NowMillis: 1,
	}
}

func TestValidatePlatformGenerationPersistenceInputRejectsInvalidBeforeDependencies(t *testing.T) {
	unsafeTime := platformSSEV2MaxSafeInteger + 1
	invalidConversationGUID := int64(0)
	tests := []struct {
		name   string
		mutate func(*PlatformGenerationPersistenceInput)
	}{
		{"zero user", func(input *PlatformGenerationPersistenceInput) { input.UserID = 0 }},
		{"malformed UUID", func(input *PlatformGenerationPersistenceInput) { input.GenerationID = "not-a-uuid" }},
		{"nonpositive time", func(input *PlatformGenerationPersistenceInput) { input.NowMillis = 0 }},
		{"unsafe time", func(input *PlatformGenerationPersistenceInput) { input.NowMillis = unsafeTime }},
		{"invalid mode", func(input *PlatformGenerationPersistenceInput) { input.Mode = 99 }},
		{"single cardinality", func(input *PlatformGenerationPersistenceInput) {
			input.Models = []string{"model-a", "model-b"}
			input.Results = append(input.Results, PlatformGenerationPersistenceResult{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "timeout"})
		}},
		{"compare cardinality", func(input *PlatformGenerationPersistenceInput) { input.Mode = PlatformGenerationModeCompare }},
		{"duplicate model", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-a"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-a", State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
			}
		}},
		{"oversized model", func(input *PlatformGenerationPersistenceInput) {
			input.Models[0] = strings.Repeat("m", 129)
			input.Results[0].Model = input.Models[0]
		}},
		{"missing result", func(input *PlatformGenerationPersistenceInput) { input.Results = nil }},
		{"extra result", func(input *PlatformGenerationPersistenceInput) {
			input.Results = append(input.Results, input.Results[0])
		}},
		{"model order mismatch", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Model = "model-b" }},
		{"running result", func(input *PlatformGenerationPersistenceInput) {
			input.Results[0].State = PlatformGenerationStateRunning
		}},
		{"cancelling result", func(input *PlatformGenerationPersistenceInput) {
			input.Results[0].State = PlatformGenerationStateCancelling
		}},
		{"cancelled result", func(input *PlatformGenerationPersistenceInput) {
			input.Results[0].State = PlatformGenerationStateCancelled
		}},
		{"single failure", func(input *PlatformGenerationPersistenceInput) {
			input.Results[0] = PlatformGenerationPersistenceResult{Model: "model-a", State: PlatformGenerationStateFailed, ErrorCode: "timeout"}
		}},
		{"compare all failures", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
				{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "upstream_error"},
			}
		}},
		{"completed empty content", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Content = "" }},
		{"completed oversized content", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Content = strings.Repeat("x", 65536) }},
		{"completed negative tokens", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Tokens = -1 }},
		{"completed excessive tokens", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Tokens = 1 << 31 }},
		{"completed error code", func(input *PlatformGenerationPersistenceInput) { input.Results[0].ErrorCode = "timeout" }},
		{"failed content", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-b", State: PlatformGenerationStateFailed, Content: "partial", ErrorCode: "timeout"},
			}
		}},
		{"failed tokens", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-b", State: PlatformGenerationStateFailed, Tokens: 1, ErrorCode: "timeout"},
			}
		}},
		{"failed missing code", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-b", State: PlatformGenerationStateFailed},
			}
		}},
		{"failed unstable code", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "provider_secret"},
			}
		}},
		{"empty user message", func(input *PlatformGenerationPersistenceInput) { input.UserMessage = "" }},
		{"invalid UTF-8 user message", func(input *PlatformGenerationPersistenceInput) { input.UserMessage = string([]byte{0xff}) }},
		{"oversized user message", func(input *PlatformGenerationPersistenceInput) { input.UserMessage = strings.Repeat("x", 65536) }},
		{"invalid UTF-8 completed content", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Content = string([]byte{0xff}) }},
		{"invalid conversation GUID", func(input *PlatformGenerationPersistenceInput) { input.ConversationGUID = &invalidConversationGUID }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validPlatformGenerationPersistenceInput()
			test.mutate(&input)
			if err := validatePlatformGenerationPersistenceInput(input); !errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
				t.Fatalf("error=%v, want typed invalid", err)
			}
		})
	}
}

func TestValidatePlatformGenerationPersistenceInputPreservesMessageBytes(t *testing.T) {
	input := validPlatformGenerationPersistenceInput()
	if err := validatePlatformGenerationPersistenceInput(input); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}

func TestPlatformGenerationReceiptSnapshotHidesUserMessage(t *testing.T) {
	encoded, err := json.Marshal(PlatformGenerationReceiptSnapshot{UserMessage: "secret prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret prompt") || strings.Contains(string(encoded), "UserMessage") || strings.Contains(string(encoded), "user_message") {
		t.Fatalf("user message leaked through snapshot JSON: %s", encoded)
	}
}

func TestWithPlatformGenerationAdvisoryLockRejectsInvalidInputs(t *testing.T) {
	db := &gorm.DB{}
	validFn := func(*gorm.DB) error { return nil }
	for _, test := range []struct {
		name string
		ctx  context.Context
		db   *gorm.DB
		lock string
		fn   func(*gorm.DB) error
	}{
		{"nil context", nil, db, "lock", validFn},
		{"nil database", context.Background(), nil, "lock", validFn},
		{"empty name", context.Background(), db, "", validFn},
		{"oversized name", context.Background(), db, strings.Repeat("x", 65), validFn},
		{"nil callback", context.Background(), db, "lock", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := withPlatformGenerationAdvisoryLock(test.ctx, test.db, test.lock, test.fn); !errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
				t.Fatalf("error=%v, want invalid", err)
			}
		})
	}
}

func openPlatformGenerationAdvisoryLockMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires TEST_DATABASE_URL")
	}
	return openTestMySQL(t)
}

func assertPlatformGenerationLockFree(t *testing.T, db *gorm.DB, lockName string) {
	t.Helper()
	var free sql.NullInt64
	if err := db.Raw("SELECT IS_FREE_LOCK(?)", lockName).Scan(&free).Error; err != nil {
		t.Fatal(err)
	}
	if !free.Valid || free.Int64 != 1 {
		t.Fatalf("lock %q was not free after callback: %#v", lockName, free)
	}
}

func TestWithPlatformGenerationAdvisoryLockUsesPinnedConnectionAndAlwaysReleases(t *testing.T) {
	db := openPlatformGenerationAdvisoryLockMySQL(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	sentinel := errors.New("callback sentinel")

	tests := []struct {
		name string
		run  func(context.Context, context.CancelFunc, *gorm.DB, string) error
		want error
	}{
		{"success", func(_ context.Context, _ context.CancelFunc, conn *gorm.DB, lockName string) error {
			var connectionID int64
			if err := conn.Raw("SELECT CONNECTION_ID()").Scan(&connectionID).Error; err != nil {
				return err
			}
			var owner sql.NullInt64
			if err := db.Raw("SELECT IS_USED_LOCK(?)", lockName).Scan(&owner).Error; err != nil {
				return err
			}
			if !owner.Valid || owner.Int64 != connectionID {
				t.Fatalf("lock owner=%#v, callback connection=%d", owner, connectionID)
			}
			return nil
		}, nil},
		{"sentinel error", func(_ context.Context, _ context.CancelFunc, _ *gorm.DB, _ string) error { return sentinel }, sentinel},
		{"sentinel survives release failure", func(_ context.Context, _ context.CancelFunc, conn *gorm.DB, lockName string) error {
			var released sql.NullInt64
			if err := conn.Raw("SELECT RELEASE_LOCK(?)", lockName).Scan(&released).Error; err != nil {
				return err
			}
			if !released.Valid || released.Int64 != 1 {
				t.Fatalf("callback could not release fixture lock: %#v", released)
			}
			return sentinel
		}, sentinel},
		{"release missing fails closed", func(_ context.Context, _ context.CancelFunc, conn *gorm.DB, lockName string) error {
			var released sql.NullInt64
			if err := conn.Raw("SELECT RELEASE_LOCK(?)", lockName).Scan(&released).Error; err != nil {
				return err
			}
			if !released.Valid || released.Int64 != 1 {
				t.Fatalf("callback could not release fixture lock: %#v", released)
			}
			return nil
		}, ErrPlatformGenerationPersistenceUnavailable},
		{"caller cancellation", func(ctx context.Context, cancel context.CancelFunc, _ *gorm.DB, _ string) error {
			cancel()
			return ctx.Err()
		}, context.Canceled},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lockName := platformGenerationAdvisoryLockName(int64(index+1), generationTestID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := withPlatformGenerationAdvisoryLock(ctx, db, lockName, func(conn *gorm.DB) error {
				return test.run(ctx, cancel, conn, lockName)
			})
			if !errors.Is(err, test.want) || (test.want == nil && err != nil) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
			assertPlatformGenerationLockFree(t, db, lockName)
		})
	}
}

type platformGenerationReceiptFixture struct {
	db                *gorm.DB
	owner             models.User
	other             models.User
	conversation      models.Conversation
	otherConversation models.Conversation
	userMessage       models.Message
	assistantMessages []models.Message
	receipt           models.PlatformChatGenerationReceipt
	results           []models.PlatformChatGenerationResult
}

func openPlatformGenerationPersistenceMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if raw == "" {
		t.Skip("BLOCKED_FIXTURE: requires TEST_DATABASE_URL")
	}
	if err := validateTestDatabaseURL(raw, os.Getenv("DATABASE_URL")); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Fatal("TEST_DATABASE_URL must target a database ending in _test")
	}
	gdb, err := db.Open(raw, "test")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := time.Now().UTC().UnixMilli()
	if err := migration.Up(context.Background(), gdb, testSnowflake.Next, func() int64 { return now }); err != nil {
		t.Fatalf("migrate receipt test database: %v", err)
	}
	return gdb
}

func openPlatformGenerationReceiptSchemaMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	parent := openPlatformGenerationPersistenceMySQL(t)
	raw := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))

	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "porsche_receipt_" + hex.EncodeToString(token[:]) + "_test"
	if err := parent.Exec("CREATE DATABASE `" + name + "`").Error; err != nil {
		t.Fatalf("create owned receipt database: %v", err)
	}
	t.Cleanup(func() {
		if err := parent.Exec("DROP DATABASE `" + name + "`").Error; err != nil {
			t.Errorf("drop owned receipt database: %v", err)
		}
	})

	childURL, err := url.Parse(raw)
	if err != nil {
		t.Fatal("parse validated TEST_DATABASE_URL")
	}
	childURL.Path, childURL.RawPath = "/"+name, ""
	child, err := db.Open(childURL.String(), "test")
	if err != nil {
		t.Fatal(err)
	}
	childSQL, err := child.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = childSQL.Close() })
	now := time.Now().UTC().UnixMilli()
	if err := migration.Up(context.Background(), child, testSnowflake.Next, func() int64 { return now }); err != nil {
		t.Fatalf("migrate owned receipt database: %v", err)
	}
	return child
}

func seedPlatformGenerationReceipt(t *testing.T, mode PlatformGenerationMode) platformGenerationReceiptFixture {
	t.Helper()
	gdb := openPlatformGenerationPersistenceMySQL(t)
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatalf("begin receipt fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	return seedPlatformGenerationReceiptOnDB(t, mode, tx, 1)
}

func seedPlatformGenerationReceiptWithTrailingFailure(t *testing.T) platformGenerationReceiptFixture {
	t.Helper()
	gdb := openPlatformGenerationPersistenceMySQL(t)
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatalf("begin receipt fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	return seedPlatformGenerationReceiptOnDB(t, PlatformGenerationModeCompare, tx, 2)
}

func seedPlatformGenerationReceiptSchema(t *testing.T, mode PlatformGenerationMode) platformGenerationReceiptFixture {
	t.Helper()
	return seedPlatformGenerationReceiptOnDB(t, mode, openPlatformGenerationReceiptSchemaMySQL(t), 1)
}

func seedPlatformGenerationReceiptOnDB(t *testing.T, mode PlatformGenerationMode, gdb *gorm.DB, failedIndex int) platformGenerationReceiptFixture {
	t.Helper()
	now := int64(1_800_000_000_000)
	var group models.BusinessGroup
	if err := gdb.Where("group_key = ? AND is_deleted = 0", "default").First(&group).Error; err != nil {
		t.Fatalf("load default group: %v", err)
	}
	newUser := func() models.User {
		username := fixtureUsername(testSnowflake.Next())
		return models.User{
			AuditFields:   models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now},
			GroupID:       group.ID,
			Username:      &username,
			Nickname:      &username,
			AllowedModels: models.JSONSlice{},
			PlanType:      models.PlanFree,
			Status:        models.UserStatusActive,
			Role:          models.UserRoleUser,
			AuthVersion:   1,
		}
	}
	fixture := platformGenerationReceiptFixture{db: gdb, owner: newUser(), other: newUser()}
	if err := gdb.Create(&fixture.owner).Error; err != nil {
		t.Fatalf("create receipt owner: %v", err)
	}
	if err := gdb.Create(&fixture.other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	fixture.conversation = models.Conversation{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, CreatedBy: &fixture.owner.ID, UpdatedAt: now, UpdatedBy: &fixture.owner.ID},
		UserID:      fixture.owner.ID, Title: "receipt fixture",
	}
	fixture.otherConversation = models.Conversation{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, CreatedBy: &fixture.owner.ID, UpdatedAt: now, UpdatedBy: &fixture.owner.ID},
		UserID:      fixture.owner.ID, Title: "other conversation",
	}
	if err := gdb.Create(&fixture.conversation).Error; err != nil {
		t.Fatalf("create receipt conversation: %v", err)
	}
	if err := gdb.Create(&fixture.otherConversation).Error; err != nil {
		t.Fatalf("create other conversation: %v", err)
	}
	fixture.userMessage = models.Message{
		AuditFields:    models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, CreatedBy: &fixture.owner.ID, UpdatedAt: now, UpdatedBy: &fixture.owner.ID},
		ConversationID: fixture.conversation.ID, Role: models.MessageRoleUser, Content: " prompt bytes ", Tokens: 0,
	}
	if err := gdb.Create(&fixture.userMessage).Error; err != nil {
		t.Fatalf("create receipt user message: %v", err)
	}
	modelsInOrder := []string{"Model-B"}
	if mode == PlatformGenerationModeCompare {
		modelsInOrder = []string{"Model-B", "model-a", "model-c"}
	}
	for index, model := range modelsInOrder {
		if mode == PlatformGenerationModeCompare && index == failedIndex {
			continue
		}
		modelCopy := model
		message := models.Message{
			AuditFields:    models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, CreatedBy: &fixture.owner.ID, UpdatedAt: now, UpdatedBy: &fixture.owner.ID},
			ConversationID: fixture.conversation.ID, Role: models.MessageRoleAssistant,
			Content: "answer-" + model, Model: &modelCopy, Tokens: index + 2,
		}
		if err := gdb.Create(&message).Error; err != nil {
			t.Fatalf("create assistant message: %v", err)
		}
		fixture.assistantMessages = append(fixture.assistantMessages, message)
	}
	successCount := len(fixture.assistantMessages)
	totalTokens := int64(0)
	for _, message := range fixture.assistantMessages {
		totalTokens += int64(message.Tokens)
	}
	fixture.receipt = models.PlatformChatGenerationReceipt{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, CreatedBy: &fixture.owner.ID, UpdatedAt: now, UpdatedBy: &fixture.owner.ID},
		UserID:      fixture.owner.ID, GenerationID: generationTestID,
		Mode: models.PlatformGenerationReceiptMode(mode), ConversationID: fixture.conversation.ID, UserMessageID: fixture.userMessage.ID,
		SuccessfulModelCount: successCount, DailyCallsCharged: successCount, TotalTokens: totalTokens, CommittedAt: now,
	}
	if err := gdb.Create(&fixture.receipt).Error; err != nil {
		t.Fatalf("create generation receipt: %v", err)
	}
	completedIndex := 0
	for index, model := range modelsInOrder {
		result := models.PlatformChatGenerationResult{
			AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, CreatedBy: &fixture.owner.ID, UpdatedAt: now, UpdatedBy: &fixture.owner.ID},
			ReceiptID:   fixture.receipt.ID, ModelIndex: index, Model: model,
		}
		if mode == PlatformGenerationModeCompare && index == failedIndex {
			code := "timeout"
			result.Status, result.Tokens, result.ErrorCode = models.PlatformGenerationResultFailed, 0, &code
		} else {
			message := fixture.assistantMessages[completedIndex]
			completedIndex++
			result.Status, result.AssistantMessageID, result.Tokens = models.PlatformGenerationResultCompleted, &message.ID, int64(message.Tokens)
		}
		if err := gdb.Create(&result).Error; err != nil {
			t.Fatalf("create generation result: %v", err)
		}
		fixture.results = append(fixture.results, result)
	}
	return fixture
}

func requireReceiptIntegrity(t *testing.T, fixture platformGenerationReceiptFixture) {
	t.Helper()
	_, err := LoadPlatformGenerationReceipt(context.Background(), fixture.db, fixture.owner.ID, generationTestID)
	if !errors.Is(err, ErrPlatformGenerationPersistenceIntegrity) {
		t.Fatalf("error=%v, want receipt integrity", err)
	}
}

func dropReceiptConstraint(t *testing.T, fixture platformGenerationReceiptFixture, table, constraint string) {
	t.Helper()
	if err := fixture.db.Exec("ALTER TABLE `" + table + "` DROP CHECK `" + constraint + "`").Error; err != nil {
		t.Fatalf("drop owned CHECK %s: %v", constraint, err)
	}
	if err := migration.VerifyPlatformGenerationReceiptSchema(context.Background(), fixture.db); !errors.Is(err, migration.ErrPlatformGenerationReceiptSchema) {
		t.Fatalf("schema verifier error=%v after dropping %s", err, constraint)
	}
}

func TestLoadPlatformGenerationReceiptHydratesOwnedSingleResult(t *testing.T) {
	fixture := seedPlatformGenerationReceipt(t, PlatformGenerationModeSingle)
	snapshot, err := LoadPlatformGenerationReceipt(context.Background(), fixture.db, fixture.owner.ID, generationTestID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.UserID != fixture.owner.ID || snapshot.GenerationID != generationTestID || snapshot.Mode != PlatformGenerationModeSingle ||
		snapshot.ConversationGUID != fixture.conversation.Guid || snapshot.UserMessage != fixture.userMessage.Content ||
		snapshot.SuccessfulModelCount != 1 || snapshot.DailyCallsCharged != 1 || snapshot.TotalTokens != 2 ||
		snapshot.CommittedAtMillis != fixture.receipt.CommittedAt || len(snapshot.Results) != 1 {
		t.Fatalf("unexpected hydrated receipt: %#v", snapshot)
	}
	result := snapshot.Results[0]
	if result.Model != "Model-B" || result.State != PlatformGenerationStateCompleted || result.Content != "answer-Model-B" ||
		result.Tokens != 2 || result.AssistantMessageGUID != stringInt64(fixture.assistantMessages[0].Guid) || result.ErrorCode != "" {
		t.Fatalf("unexpected hydrated result: %#v", result)
	}
}

func TestLoadPlatformGenerationReceiptPreservesCompareModelOrder(t *testing.T) {
	fixture := seedPlatformGenerationReceipt(t, PlatformGenerationModeCompare)
	snapshot, err := LoadPlatformGenerationReceipt(context.Background(), fixture.db, fixture.owner.ID, generationTestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Results) != 3 || snapshot.Results[0].Model != "Model-B" || snapshot.Results[1].Model != "model-a" || snapshot.Results[2].Model != "model-c" {
		t.Fatalf("result order not preserved: %#v", snapshot.Results)
	}
	if snapshot.Results[1].State != PlatformGenerationStateFailed || snapshot.Results[1].ErrorCode != "timeout" || snapshot.Results[1].Content != "" || snapshot.Results[1].AssistantMessageGUID != "" {
		t.Fatalf("failed result hydrated incorrectly: %#v", snapshot.Results[1])
	}
}

func TestLoadPlatformGenerationReceiptRejectsCrossUserAccess(t *testing.T) {
	fixture := seedPlatformGenerationReceipt(t, PlatformGenerationModeSingle)
	if _, err := LoadPlatformGenerationReceipt(context.Background(), fixture.db, fixture.other.ID, generationTestID); !errors.Is(err, ErrPlatformGenerationPersistenceNotFound) {
		t.Fatalf("cross-user error=%v, want not found", err)
	}
}

func TestLoadPlatformGenerationReceiptRejectsMalformedGraph(t *testing.T) {
	for _, input := range []struct {
		name         string
		ctx          context.Context
		db           *gorm.DB
		userID       int64
		generationID string
	}{
		{"nil context", nil, &gorm.DB{}, 1, generationTestID},
		{"nil database", context.Background(), nil, 1, generationTestID},
		{"invalid owner", context.Background(), &gorm.DB{}, 0, generationTestID},
		{"malformed generation", context.Background(), &gorm.DB{}, 1, "not-a-uuid"},
	} {
		t.Run(input.name, func(t *testing.T) {
			if _, err := LoadPlatformGenerationReceipt(input.ctx, input.db, input.userID, input.generationID); !errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
				t.Fatalf("error=%v, want invalid", err)
			}
		})
	}
	t.Run("soft-deleted trailing result", func(t *testing.T) {
		fixture := seedPlatformGenerationReceiptWithTrailingFailure(t)
		if err := fixture.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", fixture.results[2].ID).Update("is_deleted", 1).Error; err != nil {
			t.Fatal(err)
		}
		requireReceiptIntegrity(t, fixture)
	})

	tests := []struct {
		name        string
		freshSchema bool
		mutate      func(*testing.T, platformGenerationReceiptFixture)
	}{
		{"deleted conversation", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Conversation{}).Where("id = ?", f.conversation.ID).Update("is_deleted", 1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"conversation owner mismatch", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Conversation{}).Where("id = ?", f.conversation.ID).Update("user_id", f.other.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"receipt audit owner mismatch", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.PlatformChatGenerationReceipt{}).Where("id = ?", f.receipt.ID).Update("updated_by", f.other.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"result audit owner mismatch", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[0].ID).Update("created_by", f.other.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"noncontiguous index", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[2].ID).Update("model_index", 4).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"duplicate model", true, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Exec("ALTER TABLE platform_chat_generation_results DROP INDEX uk_platform_chat_generation_results_model").Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[2].ID).Update("model", f.results[0].Model).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.assistantMessages[1].ID).Update("model", f.results[0].Model).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown status", true, func(t *testing.T, f platformGenerationReceiptFixture) {
			dropReceiptConstraint(t, f, "platform_chat_generation_results", "chk_platform_chat_generation_results_status")
			dropReceiptConstraint(t, f, "platform_chat_generation_results", "chk_platform_chat_generation_results_shape")
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[1].ID).Update("status", 99).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"failed unstable code", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[1].ID).Update("error_code", "secret-provider-detail").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"extra result", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			code := "timeout"
			row := models.PlatformChatGenerationResult{
				AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: f.receipt.CreatedAt, CreatedBy: &f.owner.ID, UpdatedAt: f.receipt.CreatedAt, UpdatedBy: &f.owner.ID},
				ReceiptID:   f.receipt.ID, ModelIndex: 3, Model: "model-d", Status: models.PlatformGenerationResultFailed, ErrorCode: &code,
			}
			if err := f.db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"completed has error", true, func(t *testing.T, f platformGenerationReceiptFixture) {
			dropReceiptConstraint(t, f, "platform_chat_generation_results", "chk_platform_chat_generation_results_shape")
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[0].ID).Update("error_code", "timeout").Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var fixture platformGenerationReceiptFixture
			if test.freshSchema {
				fixture = seedPlatformGenerationReceiptSchema(t, PlatformGenerationModeCompare)
			} else {
				fixture = seedPlatformGenerationReceipt(t, PlatformGenerationModeCompare)
			}
			test.mutate(t, fixture)
			requireReceiptIntegrity(t, fixture)
		})
	}
}

func TestPlatformGenerationReceiptResultSetValidationIsolatesRowGuards(t *testing.T) {
	ownerID, receiptID := int64(41), int64(42)
	assistantOne, assistantTwo := int64(51), int64(52)
	validRows := []models.PlatformChatGenerationResult{
		{ID: 1, AuditFields: models.AuditFields{Guid: 101, CreatedAt: 10, CreatedBy: &ownerID, UpdatedAt: 10, UpdatedBy: &ownerID}, ReceiptID: receiptID, ModelIndex: 0, Model: "model-a", Status: models.PlatformGenerationResultCompleted, AssistantMessageID: &assistantOne, Tokens: 1},
		{ID: 2, AuditFields: models.AuditFields{Guid: 102, CreatedAt: 10, CreatedBy: &ownerID, UpdatedAt: 10, UpdatedBy: &ownerID}, ReceiptID: receiptID, ModelIndex: 1, Model: "model-b", Status: models.PlatformGenerationResultCompleted, AssistantMessageID: &assistantTwo, Tokens: 2},
	}
	if !validPlatformGenerationReceiptResultSet(validRows, receiptID, ownerID) {
		t.Fatal("valid row baseline rejected")
	}
	caseDistinct := append([]models.PlatformChatGenerationResult(nil), validRows...)
	caseDistinct[1].Model = "Model-A"
	if !validPlatformGenerationReceiptResultSet(caseDistinct, receiptID, ownerID) {
		t.Fatal("case-distinct models rejected")
	}
	tests := []struct {
		name   string
		mutate func([]models.PlatformChatGenerationResult)
	}{
		{"invalid model", func(rows []models.PlatformChatGenerationResult) { rows[0].Model = " model-a" }},
		{"invalid UTF-8 model", func(rows []models.PlatformChatGenerationResult) { rows[0].Model = string([]byte{0xff}) }},
		{"multibyte model byte overflow", func(rows []models.PlatformChatGenerationResult) { rows[0].Model = strings.Repeat("界", 43) }},
		{"duplicate model", func(rows []models.PlatformChatGenerationResult) { rows[1].Model = rows[0].Model }},
		{"excessive token", func(rows []models.PlatformChatGenerationResult) { rows[0].Tokens = int64(1) << 31 }},
		{"duplicate assistant ID", func(rows []models.PlatformChatGenerationResult) {
			rows[1].AssistantMessageID = rows[0].AssistantMessageID
		}},
		{"soft-deleted row", func(rows []models.PlatformChatGenerationResult) { rows[1].IsDeleted = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := append([]models.PlatformChatGenerationResult(nil), validRows...)
			test.mutate(rows)
			if validPlatformGenerationReceiptResultSet(rows, receiptID, ownerID) {
				t.Fatal("isolated invalid row set accepted")
			}
		})
	}
}

func TestLoadPlatformGenerationReceiptRejectsInvalidParentScalars(t *testing.T) {
	tests := []struct {
		name, constraint, column string
		value                    any
	}{
		{"mode", "chk_platform_chat_generation_receipts_mode", "mode", 3},
		{"success count", "chk_platform_chat_generation_receipts_counts", "successful_model_count", 0},
		{"daily count", "chk_platform_chat_generation_receipts_counts", "daily_calls_charged", 2},
		{"negative tokens", "chk_platform_chat_generation_receipts_counts", "total_tokens", -1},
		{"zero committed", "chk_platform_chat_generation_receipts_time", "committed_at", 0},
		{"unsafe committed", "chk_platform_chat_generation_receipts_time", "committed_at", platformSSEV2MaxSafeInteger + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedPlatformGenerationReceiptSchema(t, PlatformGenerationModeSingle)
			dropReceiptConstraint(t, fixture, "platform_chat_generation_receipts", test.constraint)
			if err := fixture.db.Model(&models.PlatformChatGenerationReceipt{}).Where("id = ?", fixture.receipt.ID).Update(test.column, test.value).Error; err != nil {
				t.Fatal(err)
			}
			requireReceiptIntegrity(t, fixture)
		})
	}
}

func TestLoadPlatformGenerationReceiptRejectsDeletedOrMismatchedMessage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, platformGenerationReceiptFixture)
	}{
		{"deleted assistant", func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.assistantMessages[0].ID).Update("is_deleted", 1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong assistant role", func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.assistantMessages[0].ID).Update("role", models.MessageRoleUser).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong assistant model", func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.assistantMessages[0].ID).Update("model", "model-b").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong assistant tokens", func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.assistantMessages[0].ID).Update("tokens", 9).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"cross-conversation assistant", func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.assistantMessages[0].ID).Update("conversation_id", f.otherConversation.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedPlatformGenerationReceipt(t, PlatformGenerationModeSingle)
			test.mutate(t, fixture)
			requireReceiptIntegrity(t, fixture)
		})
	}
}

func TestLoadPlatformGenerationReceiptRejectsMissingDeletedWrongRoleOrCrossConversationUserMessage(t *testing.T) {
	tests := []struct {
		name        string
		freshSchema bool
		mutate      func(*testing.T, platformGenerationReceiptFixture)
	}{
		{"missing", true, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Exec("ALTER TABLE platform_chat_generation_receipts DROP FOREIGN KEY fk_platform_chat_generation_receipts_user_message").Error; err != nil {
				t.Fatal(err)
			}
			if err := migration.VerifyPlatformGenerationReceiptSchema(context.Background(), f.db); !errors.Is(err, migration.ErrPlatformGenerationReceiptSchema) {
				t.Fatalf("schema verifier error=%v", err)
			}
			if err := f.db.Delete(&models.Message{}, f.userMessage.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"deleted", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.userMessage.ID).Update("is_deleted", 1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong role", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.userMessage.ID).Update("role", models.MessageRoleAssistant).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"cross conversation", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.userMessage.ID).Update("conversation_id", f.otherConversation.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"nonzero tokens", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.userMessage.ID).Update("tokens", 1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"unexpected model", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			model := "model-a"
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.userMessage.ID).Update("model", model).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"empty content", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.userMessage.ID).Update("content", "").Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var fixture platformGenerationReceiptFixture
			if test.freshSchema {
				fixture = seedPlatformGenerationReceiptSchema(t, PlatformGenerationModeSingle)
			} else {
				fixture = seedPlatformGenerationReceipt(t, PlatformGenerationModeSingle)
			}
			test.mutate(t, fixture)
			requireReceiptIntegrity(t, fixture)
		})
	}
}

func stringInt64(value int64) string {
	return fmt.Sprintf("%d", value)
}
