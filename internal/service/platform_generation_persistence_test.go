package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func validPlatformGenerationPersistenceInput() PlatformGenerationPersistenceInput {
	return PlatformGenerationPersistenceInput{
		UserID:       1,
		GenerationID: generationTestID,
		Mode:         PlatformGenerationModeSingle,
		Models:       []string{"model-a"},
		UserMessage:  " keep whitespace byte-for-byte ",
		Results: []PlatformGenerationPersistenceResult{{
			Model: "model-a", State: PlatformGenerationStateCompleted, Content: "answer", Tokens: 1, Seq: 0,
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
			input.Results = append(input.Results, PlatformGenerationPersistenceResult{Model: "model-b", State: PlatformGenerationStateFailed, Seq: 0, ErrorCode: "timeout"})
		}},
		{"compare cardinality", func(input *PlatformGenerationPersistenceInput) { input.Mode = PlatformGenerationModeCompare }},
		{"duplicate model", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-a"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1, Seq: 0},
				{Model: "model-a", State: PlatformGenerationStateFailed, Seq: 0, ErrorCode: "timeout"},
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
			input.Results[0] = PlatformGenerationPersistenceResult{Model: "model-a", State: PlatformGenerationStateFailed, Seq: 0, ErrorCode: "timeout"}
		}},
		{"compare all failures", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateFailed, Seq: 0, ErrorCode: "timeout"},
				{Model: "model-b", State: PlatformGenerationStateFailed, Seq: 0, ErrorCode: "upstream_error"},
			}
		}},
		{"completed empty content", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Content = "" }},
		{"completed oversized content", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Content = strings.Repeat("x", 65536) }},
		{"completed negative tokens", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Tokens = -1 }},
		{"completed excessive tokens", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Tokens = 1 << 31 }},
		{"negative terminal seq", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Seq = -1 }},
		{"unsafe terminal seq", func(input *PlatformGenerationPersistenceInput) {
			input.Results[0].Seq = platformSSEV2MaxSafeInteger + 1
		}},
		{"completed error code", func(input *PlatformGenerationPersistenceInput) { input.Results[0].ErrorCode = "timeout" }},
		{"failed content", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1, Seq: 0},
				{Model: "model-b", State: PlatformGenerationStateFailed, Content: "partial", Seq: 0, ErrorCode: "timeout"},
			}
		}},
		{"failed tokens", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1, Seq: 0},
				{Model: "model-b", State: PlatformGenerationStateFailed, Tokens: 1, Seq: 0, ErrorCode: "timeout"},
			}
		}},
		{"failed missing code", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1, Seq: 0},
				{Model: "model-b", State: PlatformGenerationStateFailed, Seq: 0},
			}
		}},
		{"failed unstable code", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1, Seq: 0},
				{Model: "model-b", State: PlatformGenerationStateFailed, Seq: 0, ErrorCode: "provider_secret"},
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
	encoded, err := json.Marshal(PlatformGenerationReceiptSnapshot{UserMessage: "secret prompt", RequestedExistingConversation: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret prompt") || strings.Contains(string(encoded), "UserMessage") || strings.Contains(string(encoded), "user_message") || strings.Contains(string(encoded), "RequestedExistingConversation") || strings.Contains(string(encoded), "requested_existing_conversation") {
		t.Fatalf("internal receipt provenance leaked through snapshot JSON: %s", encoded)
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

func TestWithPlatformGenerationAdvisoryLockReleaseFailureDiscardsPinnedSession(t *testing.T) {
	lockedDB := openPlatformGenerationAdvisoryLockMySQL(t).Session(&gorm.Session{Logger: gormlogger.Discard})
	observerDB := openPlatformGenerationAdvisoryLockMySQL(t)
	lockedSQL, err := lockedDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	lockedSQL.SetMaxOpenConns(1)
	lockedSQL.SetMaxIdleConns(1)
	observerSQL, err := observerDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	observerSQL.SetMaxOpenConns(1)
	if err := observerDB.Exec("SELECT 1").Error; err != nil {
		t.Fatal(err)
	}

	releaseFailure := errors.New("simulated release failure")
	var releaseAttempts atomic.Int32
	callbackName := fmt.Sprintf("platform_generation_release_failure_%d", testSnowflake.Next())
	if err := lockedDB.Callback().Row().Before("gorm:row").Register(callbackName, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "RELEASE_LOCK") {
			releaseAttempts.Add(1)
			tx.AddError(releaseFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lockedDB.Callback().Row().Remove(callbackName) })

	lockName := platformGenerationAdvisoryLockName(testSnowflake.Next(), generationTestID)
	err = withPlatformGenerationAdvisoryLock(context.Background(), lockedDB, lockName, func(*gorm.DB) error { return nil })
	if !errors.Is(err, ErrPlatformGenerationPersistenceUnavailable) {
		t.Fatalf("release failure error=%v, want unavailable", err)
	}
	if releaseAttempts.Load() != 1 {
		t.Fatalf("release attempts=%d, want 1", releaseAttempts.Load())
	}
	assertPlatformGenerationLockFree(t, observerDB, lockName)
	stats := lockedSQL.Stats()
	if stats.InUse != 0 || stats.Idle != 0 {
		t.Fatalf("locked pool in-use/idle=%d/%d, want discarded session", stats.InUse, stats.Idle)
	}
}

func TestWithPlatformGenerationAdvisoryLockDoesNotLeakScalarStatementModel(t *testing.T) {
	db := openPlatformGenerationAdvisoryLockMySQL(t)
	lockName := platformGenerationAdvisoryLockName(99, generationTestID)
	err := withPlatformGenerationAdvisoryLock(context.Background(), db, lockName, func(conn *gorm.DB) error {
		if conn.Statement.Model != nil || conn.Statement.Dest != nil || conn.Statement.Table != "" {
			t.Fatalf("callback inherited scalar statement: model=%T dest=%T table=%q", conn.Statement.Model, conn.Statement.Dest, conn.Statement.Table)
		}
		var callbackConnectionID int64
		if err := conn.Raw("SELECT CONNECTION_ID()").Scan(&callbackConnectionID).Error; err != nil {
			return err
		}
		contextual := conn.WithContext(context.Background())
		if contextual.Statement.Model != nil || contextual.Statement.Dest != nil || contextual.Statement.Table != "" {
			t.Fatalf("WithContext inherited scalar statement: model=%T dest=%T table=%q", contextual.Statement.Model, contextual.Statement.Dest, contextual.Statement.Table)
		}
		var contextConnectionID int64
		if err := contextual.Raw("SELECT CONNECTION_ID()").Scan(&contextConnectionID).Error; err != nil {
			return err
		}
		var lockOwner sql.NullInt64
		if err := db.Raw("SELECT IS_USED_LOCK(?)", lockName).Scan(&lockOwner).Error; err != nil {
			return err
		}
		if !lockOwner.Valid || lockOwner.Int64 != callbackConnectionID || contextConnectionID != callbackConnectionID {
			t.Fatalf("lock owner=%#v callback connection=%d contextual connection=%d", lockOwner, callbackConnectionID, contextConnectionID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPlatformGenerationLockFree(t, db, lockName)
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
		Mode: models.PlatformGenerationReceiptMode(mode), RequestedExistingConversation: 1, ConversationID: fixture.conversation.ID, UserMessageID: fixture.userMessage.ID,
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
		if err := fixture.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", fixture.results[2].ID).UpdateColumn("is_deleted", 1).Error; err != nil {
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
			if err := f.db.Model(&models.PlatformChatGenerationReceipt{}).Where("id = ?", f.receipt.ID).UpdateColumn("updated_by", f.other.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"result audit owner mismatch", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[0].ID).UpdateColumn("created_by", f.other.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"noncontiguous index", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[2].ID).UpdateColumn("model_index", 4).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"duplicate model", true, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Exec("ALTER TABLE platform_chat_generation_results DROP INDEX uk_platform_chat_generation_results_model").Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[2].ID).UpdateColumn("model", f.results[0].Model).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&models.Message{}).Where("id = ?", f.assistantMessages[1].ID).Update("model", f.results[0].Model).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown status", true, func(t *testing.T, f platformGenerationReceiptFixture) {
			dropReceiptConstraint(t, f, "platform_chat_generation_results", "chk_platform_chat_generation_results_status")
			dropReceiptConstraint(t, f, "platform_chat_generation_results", "chk_platform_chat_generation_results_shape")
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[1].ID).UpdateColumn("status", 99).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"failed unstable code", false, func(t *testing.T, f platformGenerationReceiptFixture) {
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[1].ID).UpdateColumn("error_code", "secret-provider-detail").Error; err != nil {
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
			if err := f.db.Model(&models.PlatformChatGenerationResult{}).Where("id = ?", f.results[0].ID).UpdateColumn("error_code", "timeout").Error; err != nil {
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
		{"requested conversation provenance", "chk_platform_chat_generation_receipts_requested_conversation", "requested_existing_conversation", 2},
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
			if err := fixture.db.Model(&models.PlatformChatGenerationReceipt{}).Where("id = ?", fixture.receipt.ID).UpdateColumn(test.column, test.value).Error; err != nil {
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

type platformGenerationFinalizationFixture struct {
	db    *gorm.DB
	store *PlatformGenerationStore
	user  models.User
	now   int64
}

func openPlatformGenerationFinalizationFixture(t *testing.T) platformGenerationFinalizationFixture {
	t.Helper()
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" || strings.TrimSpace(os.Getenv("TEST_REDIS_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires TEST_DATABASE_URL and TEST_REDIS_URL")
	}
	gdb := openPlatformGenerationFinalizationMySQL(t)
	store, client := openTestPlatformGenerationStore(t)
	now := int64(1_900_000_000_000 + testSnowflake.Next()%10_000_000)
	username := fixtureUsername(testSnowflake.Next())
	user := models.User{
		AuditFields:       models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now - 1000, UpdatedAt: now - 1000},
		GroupID:           testDefaultBusinessGroupID(t, gdb),
		Username:          &username,
		Nickname:          &username,
		AllowedModels:     models.JSONSlice{},
		PlanType:          models.PlanFree,
		Status:            models.UserStatusActive,
		Role:              models.UserRoleUser,
		AuthVersion:       1,
		DailyCallLimit:    5,
		DailyCallsUsed:    2,
		TotalTokensUsed:   10,
		DailyCallsResetAt: &now,
	}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create finalization user: %v", err)
	}
	if err := client.Del(context.Background(), store.key(user.ID, generationTestID)).Err(); err != nil {
		t.Fatalf("clear owned generation key: %v", err)
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), store.key(user.ID, generationTestID)).Err() })
	return platformGenerationFinalizationFixture{db: gdb, store: store, user: user, now: now}
}

// openPlatformGenerationFinalizationMySQL deliberately reads only the explicit
// disposable test URL. Task 5 must never inspect or fall back to DATABASE_URL.
func openPlatformGenerationFinalizationMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	parsed, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Fatal("TEST_DATABASE_URL must target a database ending in _test")
	}
	parent, err := db.Open(raw, "test")
	if err != nil {
		t.Fatal(err)
	}
	parentSQL, err := parent.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parentSQL.Close() })

	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "porsche_finalize_" + hex.EncodeToString(token[:]) + "_test"
	if err := parent.Exec("CREATE DATABASE `" + name + "`").Error; err != nil {
		t.Fatalf("create owned finalization database: %v", err)
	}
	t.Cleanup(func() {
		if err := parent.Exec("DROP DATABASE `" + name + "`").Error; err != nil {
			t.Errorf("drop owned finalization database: %v", err)
		}
	})

	childURL := *parsed
	childURL.Path, childURL.RawPath = "/"+name, ""
	childQuery := childURL.Query()
	childQuery.Del("clientFoundRows")
	childURL.RawQuery = childQuery.Encode()
	child, err := db.Open(childURL.String(), "test")
	if err != nil {
		t.Fatal(err)
	}
	childSQL, err := child.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = childSQL.Close() })
	if err := migration.Up(context.Background(), child, testSnowflake.Next, func() int64 { return time.Now().UTC().UnixMilli() }); err != nil {
		t.Fatalf("migrate owned finalization database: %v", err)
	}
	return child
}

func (f platformGenerationFinalizationFixture) committingSingle(t *testing.T) PlatformGenerationPersistenceInput {
	t.Helper()
	input := f.singleInput()
	claim := PlatformGenerationClaimInput{UserID: f.user.ID, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"model-a"}, NowMillis: f.now}
	claimTestGeneration(t, f.store, claim)
	if _, err := f.store.MarkModelDone(context.Background(), f.user.ID, generationTestID, "model-a", 0, f.now+1); err != nil {
		t.Fatalf("mark model done: %v", err)
	}
	if _, err := f.store.BeginCommit(context.Background(), f.user.ID, generationTestID, f.now+2); err != nil {
		t.Fatalf("begin commit: %v", err)
	}
	return input
}

func (f platformGenerationFinalizationFixture) singleInput() PlatformGenerationPersistenceInput {
	return PlatformGenerationPersistenceInput{
		UserID: f.user.ID, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle,
		Models: []string{"model-a"}, UserMessage: "  exact user bytes  ", NowMillis: f.now + 3,
		Results: []PlatformGenerationPersistenceResult{{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "exact answer bytes", Tokens: 7, Seq: 0}},
	}
}

func TestPlatformGenerationPersistenceFinalizesSingleAtomically(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Finalize(context.Background(), f.db, input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.UserID != input.UserID || receipt.GenerationID != input.GenerationID || receipt.Mode != input.Mode ||
		receipt.UserMessage != input.UserMessage || receipt.SuccessfulModelCount != 1 || receipt.DailyCallsCharged != 1 ||
		receipt.TotalTokens != 7 || receipt.CommittedAtMillis != input.NowMillis || len(receipt.Results) != 1 {
		t.Fatalf("unexpected final receipt: %#v", receipt)
	}
	result := receipt.Results[0]
	if result.Model != "model-a" || result.State != PlatformGenerationStateCompleted || result.Content != "exact answer bytes" ||
		result.Tokens != 7 || result.ErrorCode != "" || result.AssistantMessageGUID == "" {
		t.Fatalf("unexpected final result: %#v", result)
	}
	var conversation models.Conversation
	if err := f.db.Where("guid = ? AND user_id = ? AND is_deleted = 0", receipt.ConversationGUID, f.user.ID).First(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	if conversation.Title != "exact user bytes" || conversation.Model == nil || *conversation.Model != "model-a" || conversation.UpdatedAt != input.NowMillis || conversation.UpdatedBy == nil || *conversation.UpdatedBy != f.user.ID {
		t.Fatalf("unexpected conversation: %#v", conversation)
	}
	var messages []models.Message
	if err := f.db.Where("conversation_id = ? AND is_deleted = 0", conversation.ID).Order("id ASC").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != models.MessageRoleUser || messages[0].Content != input.UserMessage || messages[0].Content == "" ||
		messages[1].Role != models.MessageRoleAssistant || messages[1].Content != input.Results[0].Content || messages[1].Model == nil || *messages[1].Model != "model-a" || messages[1].Tokens != 7 || stringInt64(messages[1].Guid) != result.AssistantMessageGUID {
		t.Fatalf("unexpected messages: %#v", messages)
	}
	var usage []models.UsageRecord
	if err := f.db.Where("user_id = ? AND is_deleted = 0", f.user.ID).Find(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].RecordType != models.UsageRecordChat || usage[0].Tokens != 7 || usage[0].Model == nil || *usage[0].Model != "model-a" {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	var updated models.User
	if err := f.db.First(&updated, f.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.DailyCallsUsed != 3 || updated.TotalTokensUsed != 17 || updated.UpdatedAt != input.NowMillis || updated.UpdatedBy == nil || *updated.UpdatedBy != f.user.ID {
		t.Fatalf("unexpected charged user: %#v", updated)
	}
	var parent models.PlatformChatGenerationReceipt
	if err := f.db.Where("user_id = ? AND generation_id = ?", f.user.ID, generationTestID).First(&parent).Error; err != nil {
		t.Fatal(err)
	}
	var persistedResults []models.PlatformChatGenerationResult
	if err := f.db.Where("receipt_id = ?", parent.ID).Find(&persistedResults).Error; err != nil {
		t.Fatal(err)
	}
	if parent.UserMessageID != messages[0].ID || len(persistedResults) != 1 || persistedResults[0].AssistantMessageID == nil || *persistedResults[0].AssistantMessageID != messages[1].ID {
		t.Fatalf("receipt graph mismatch: parent=%#v results=%#v", parent, persistedResults)
	}
}

func TestPlatformGenerationPersistenceRejectsRedisIdentityMismatchBeforeMySQL(t *testing.T) {
	t.Run("pure ordered models", func(t *testing.T) {
		input := PlatformGenerationPersistenceInput{
			UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeCompare,
			Models: []string{"model-a", "model-b"}, UserMessage: "prompt", NowMillis: 1,
			Results: []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "a", Tokens: 1, Seq: 0},
				{Model: "model-b", State: PlatformGenerationStateCompleted, Content: "b", Tokens: 1, Seq: 0},
			},
		}
		snapshot := PlatformGenerationSnapshot{
			GenerationID: generationTestID, Mode: PlatformGenerationModeCompare,
			Models: []string{"model-b", "model-a"}, State: PlatformGenerationStateCommitting, CreatedAtMillis: 1, UpdatedAtMillis: 1,
			ModelStates: map[string]PlatformGenerationModel{
				"model-a": {State: PlatformGenerationStateCompleted},
				"model-b": {State: PlatformGenerationStateCompleted},
			},
		}
		if platformPersistenceMatchesRedis(input, snapshot) {
			t.Fatal("reordered Redis models matched input")
		}
	})
	t.Run("pure mode and cardinality", func(t *testing.T) {
		input := validPlatformGenerationPersistenceInput()
		snapshot := PlatformGenerationSnapshot{
			GenerationID: generationTestID, Mode: PlatformGenerationModeCompare,
			Models: []string{"model-a", "model-b"}, State: PlatformGenerationStateCommitting, CreatedAtMillis: 1, UpdatedAtMillis: 1,
			ModelStates: map[string]PlatformGenerationModel{
				"model-a": {State: PlatformGenerationStateCompleted},
				"model-b": {State: PlatformGenerationStateCompleted},
			},
		}
		if platformPersistenceMatchesRedis(input, snapshot) {
			t.Fatal("compare Redis identity matched single input")
		}
	})
	t.Run("pure terminal state", func(t *testing.T) {
		input := PlatformGenerationPersistenceInput{
			UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeCompare,
			Models: []string{"model-a", "model-b"}, UserMessage: "prompt", NowMillis: 1,
			Results: []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "a", Tokens: 1, Seq: 0},
				{Model: "model-b", State: PlatformGenerationStateCompleted, Content: "b", Tokens: 1, Seq: 0},
			},
		}
		snapshot := PlatformGenerationSnapshot{
			GenerationID: generationTestID, Mode: PlatformGenerationModeCompare,
			Models: []string{"model-a", "model-b"}, State: PlatformGenerationStateCommitting, CreatedAtMillis: 1, UpdatedAtMillis: 1,
			ModelStates: map[string]PlatformGenerationModel{
				"model-a": {State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
				"model-b": {State: PlatformGenerationStateCompleted},
			},
		}
		if platformPersistenceMatchesRedis(input, snapshot) {
			t.Fatal("different terminal state matched input")
		}
	})
	t.Run("pure terminal error", func(t *testing.T) {
		input := PlatformGenerationPersistenceInput{
			UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeCompare,
			Models: []string{"model-a", "model-b"}, UserMessage: "prompt", NowMillis: 1,
			Results: []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateFailed, Seq: 0, ErrorCode: "upstream_error"},
				{Model: "model-b", State: PlatformGenerationStateCompleted, Content: "b", Tokens: 1, Seq: 0},
			},
		}
		snapshot := PlatformGenerationSnapshot{
			GenerationID: generationTestID, Mode: PlatformGenerationModeCompare,
			Models: []string{"model-a", "model-b"}, State: PlatformGenerationStateCommitting, CreatedAtMillis: 1, UpdatedAtMillis: 1,
			ModelStates: map[string]PlatformGenerationModel{
				"model-a": {State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
				"model-b": {State: PlatformGenerationStateCompleted},
			},
		}
		if platformPersistenceMatchesRedis(input, snapshot) {
			t.Fatal("different terminal error matched input")
		}
	})
	t.Run("pure committing assistant guid", func(t *testing.T) {
		input := validPlatformGenerationPersistenceInput()
		snapshot := PlatformGenerationSnapshot{
			GenerationID: generationTestID, Mode: PlatformGenerationModeSingle,
			Models: []string{"model-a"}, State: PlatformGenerationStateCommitting, CreatedAtMillis: 1, UpdatedAtMillis: 1,
			ModelStates: map[string]PlatformGenerationModel{
				"model-a": {State: PlatformGenerationStateCompleted, AssistantMessageGUID: "123"},
			},
		}
		if platformPersistenceMatchesRedis(input, snapshot) {
			t.Fatal("committing Redis snapshot with assistant GUID matched input")
		}
	})
	t.Run("pure terminal seq", func(t *testing.T) {
		input := validPlatformGenerationPersistenceInput()
		input.Results[0].Seq = 2
		snapshot := PlatformGenerationSnapshot{
			GenerationID: generationTestID, Mode: PlatformGenerationModeSingle,
			Models: []string{"model-a"}, State: PlatformGenerationStateCommitting, CreatedAtMillis: 1, UpdatedAtMillis: 1,
			ModelStates: map[string]PlatformGenerationModel{"model-a": {State: PlatformGenerationStateCompleted, Seq: 1}},
		}
		if platformPersistenceMatchesRedis(input, snapshot) {
			t.Fatal("different terminal sequence matched input")
		}
	})
	t.Run("pure stale Redis time", func(t *testing.T) {
		input := validPlatformGenerationPersistenceInput()
		input.NowMillis = 9
		snapshot := PlatformGenerationSnapshot{
			GenerationID: generationTestID, Mode: PlatformGenerationModeSingle,
			Models: []string{"model-a"}, State: PlatformGenerationStateCommitting, CreatedAtMillis: 1, UpdatedAtMillis: 10,
			ModelStates: map[string]PlatformGenerationModel{"model-a": {State: PlatformGenerationStateCompleted, Seq: input.Results[0].Seq}},
		}
		if platformPersistenceMatchesRedis(input, snapshot) {
			t.Fatal("Redis snapshot newer than persistence input matched")
		}
	})

	for _, test := range []struct {
		name    string
		prepare func(*testing.T, platformGenerationFinalizationFixture) PlatformGenerationPersistenceInput
	}{
		{"generation state", func(t *testing.T, f platformGenerationFinalizationFixture) PlatformGenerationPersistenceInput {
			claimTestGeneration(t, f.store, PlatformGenerationClaimInput{UserID: f.user.ID, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"model-a"}, NowMillis: f.now})
			if _, err := f.store.MarkModelDone(context.Background(), f.user.ID, generationTestID, "model-a", 0, f.now+1); err != nil {
				t.Fatal(err)
			}
			return f.singleInput()
		}},
		{"model identity", func(t *testing.T, f platformGenerationFinalizationFixture) PlatformGenerationPersistenceInput {
			claimTestGeneration(t, f.store, PlatformGenerationClaimInput{UserID: f.user.ID, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle, Models: []string{"model-b"}, NowMillis: f.now})
			if _, err := f.store.MarkModelDone(context.Background(), f.user.ID, generationTestID, "model-b", 0, f.now+1); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.BeginCommit(context.Background(), f.user.ID, generationTestID, f.now+2); err != nil {
				t.Fatal(err)
			}
			return f.singleInput()
		}},
		{"completed non-committing with assistant guid", func(t *testing.T, f platformGenerationFinalizationFixture) PlatformGenerationPersistenceInput {
			input := f.committingSingle(t)
			if _, err := f.store.Complete(context.Background(), f.user.ID, generationTestID, map[string]string{"model-a": stringInt64(testSnowflake.Next())}, f.now+4); err != nil {
				t.Fatal(err)
			}
			return input
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := openPlatformGenerationFinalizationFixture(t)
			input := test.prepare(t, f)
			p, err := NewPlatformGenerationPersistence(f.store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
				t.Fatalf("Finalize() error=%v, want conflict", err)
			}
			assertPlatformGenerationFinalizationEffects(t, f, 0, 0, 0, 0, 0, 2, 10)
		})
	}
}

func TestPlatformGenerationPersistenceRejectsEmptyUserMessageBeforeDependencies(t *testing.T) {
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	p := &PlatformGenerationPersistence{generations: store, loadReceipt: LoadPlatformGenerationReceipt, runLocked: func(context.Context, *gorm.DB, string, func(*gorm.DB) error) error {
		panic("transaction dependency touched")
	}}
	t.Run("empty user message", func(t *testing.T) {
		input := validPlatformGenerationPersistenceInput()
		input.UserMessage = ""
		if _, err := p.Finalize(context.Background(), &gorm.DB{}, input); !errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
			t.Fatalf("Finalize() error=%v, want invalid", err)
		}
	})
}

type platformGenerationBeginObserver struct {
	gorm.ConnPool
	beginner gorm.TxBeginner
	began    *atomic.Bool
}

func (o *platformGenerationBeginObserver) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	o.began.Store(true)
	return o.beginner.BeginTx(ctx, options)
}

type platformGenerationRedisOrderHook struct {
	gets                atomic.Int32
	transactionBegan    *atomic.Bool
	recheckAfterBeginTx atomic.Bool
}

func (h *platformGenerationRedisOrderHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *platformGenerationRedisOrderHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() == "get" && h.gets.Add(1) == 2 && h.transactionBegan.Load() {
			h.recheckAfterBeginTx.Store(true)
		}
		return next(ctx, command)
	}
}

func (h *platformGenerationRedisOrderHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestPlatformGenerationPersistenceRechecksRedisBeforeBeginningTransaction(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := f.store.client.(*redis.Client)
	if !ok {
		t.Fatalf("fixture Redis client type=%T", f.store.client)
	}
	var began atomic.Bool
	var persistenceQueryObserved atomic.Bool
	var persistenceQueryInsideTx atomic.Bool
	callbackName := fmt.Sprintf("platform_generation_tx_order_%d", testSnowflake.Next())
	if err := f.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			persistenceQueryObserved.Store(true)
			_, inside := tx.Statement.ConnPool.(gorm.TxCommitter)
			persistenceQueryInsideTx.Store(inside)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callbackName) })
	hook := &platformGenerationRedisOrderHook{transactionBegan: &began}
	client.AddHook(hook)
	p.runLocked = func(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
		return withPlatformGenerationAdvisoryLock(ctx, db, lockName, func(conn *gorm.DB) error {
			beginner, ok := conn.Statement.ConnPool.(gorm.TxBeginner)
			if !ok {
				return errors.New("pinned connection cannot begin transaction")
			}
			observed := platformGenerationPinnedSession(conn, ctx)
			observed.Statement.ConnPool = &platformGenerationBeginObserver{ConnPool: conn.Statement.ConnPool, beginner: beginner, began: &began}
			return fn(observed)
		})
	}

	if _, err := p.Finalize(context.Background(), f.db, input); err != nil {
		t.Fatal(err)
	}
	if hook.gets.Load() != 2 {
		t.Fatalf("Redis GET count=%d, want initial read plus locked recheck", hook.gets.Load())
	}
	if hook.recheckAfterBeginTx.Load() {
		t.Fatal("locked Redis recheck ran after BeginTx")
	}
	if !began.Load() {
		t.Fatal("Finalize did not begin its SQL transaction after the Redis recheck")
	}
	if !persistenceQueryObserved.Load() || !persistenceQueryInsideTx.Load() {
		t.Fatalf("persistence query observed/inside transaction=%v/%v, want true/true", persistenceQueryObserved.Load(), persistenceQueryInsideTx.Load())
	}
}

func TestPlatformGenerationPersistenceSingleWriteFailuresRollbackEveryEffect(t *testing.T) {
	faults := []struct {
		name                 string
		operation            string
		table                string
		occurrence           int
		badReceipt           bool
		existingConversation bool
	}{
		{"conversation", "create", "conversations", 1, false, false},
		{"user message", "create", "messages", 1, false, false},
		{"assistant message", "create", "messages", 2, false, false},
		{"usage", "create", "usage_records", 1, false, false},
		{"existing conversation title update", "update", "conversations", 1, false, true},
		{"user update", "update", "users", 1, false, false},
		{"receipt bad user message reference", "create", "platform_chat_generation_receipts", 1, true, false},
		{"result", "create", "platform_chat_generation_results", 1, false, false},
	}
	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			f := openPlatformGenerationFinalizationFixture(t)
			input := f.committingSingle(t)
			var existing models.Conversation
			if fault.existingConversation {
				existing = models.Conversation{
					AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: f.now - 1000, CreatedBy: &f.user.ID, UpdatedAt: f.now - 1000, UpdatedBy: &f.user.ID},
					UserID:      f.user.ID,
					Title:       "新对话",
				}
				if err := f.db.Create(&existing).Error; err != nil {
					t.Fatal(err)
				}
				input.ConversationGUID = &existing.Guid
			}
			hook := fmt.Sprintf("platform_single_fault_%d", testSnowflake.Next())
			matches := 0
			inject := func(tx *gorm.DB) {
				table := tx.Statement.Table
				if table == "" && tx.Statement.Schema != nil {
					table = tx.Statement.Schema.Table
				}
				if table != fault.table {
					return
				}
				matches++
				if matches != fault.occurrence {
					return
				}
				if fault.badReceipt {
					receipt, ok := tx.Statement.Dest.(*models.PlatformChatGenerationReceipt)
					if !ok {
						tx.AddError(errors.New("unexpected receipt destination"))
						return
					}
					receipt.UserMessageID = int64(^uint64(0) >> 1)
					return
				}
				tx.AddError(errors.New("isolated platform finalization write fault"))
			}
			if fault.operation == "update" {
				if err := f.db.Callback().Update().Before("gorm:update").Register(hook, inject); err != nil {
					t.Fatal(err)
				}
				defer f.db.Callback().Update().Remove(hook)
			} else {
				if err := f.db.Callback().Create().Before("gorm:create").Register(hook, inject); err != nil {
					t.Fatal(err)
				}
				defer f.db.Callback().Create().Remove(hook)
			}
			p, err := NewPlatformGenerationPersistence(f.store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceUnavailable) {
				t.Fatalf("Finalize() error=%v, want unavailable", err)
			}
			if matches < fault.occurrence {
				t.Fatalf("fault boundary not reached: matches=%d", matches)
			}
			conversationCount := int64(0)
			if fault.existingConversation {
				conversationCount = 1
			}
			assertPlatformGenerationFinalizationEffects(t, f, conversationCount, 0, 0, 0, 0, 2, 10)
			if fault.existingConversation {
				var unchanged models.Conversation
				if err := f.db.First(&unchanged, existing.ID).Error; err != nil {
					t.Fatal(err)
				}
				if unchanged.Title != "新对话" || unchanged.UpdatedAt != existing.UpdatedAt || unchanged.UpdatedBy == nil || *unchanged.UpdatedBy != f.user.ID {
					t.Fatalf("existing conversation changed despite rollback: before=%#v after=%#v", existing, unchanged)
				}
			}
		})
	}
}

func TestPlatformGenerationPersistenceSingleInsufficientQuotaRollsBack(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	if err := f.db.Model(&models.User{}).Where("id = ?", f.user.ID).Updates(map[string]any{"daily_calls_used": 5, "daily_call_limit": 5}).Error; err != nil {
		t.Fatal(err)
	}
	f.user.DailyCallsUsed = 5
	input := f.committingSingle(t)
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceQuota) {
		t.Fatalf("Finalize() error=%v, want quota", err)
	}
	assertPlatformGenerationFinalizationEffects(t, f, 0, 0, 0, 0, 0, 5, 10)
}

func TestPlatformGenerationPersistenceSingleRejectsStaleLockedRowsBeforeWrites(t *testing.T) {
	for _, test := range []struct {
		name                 string
		existingConversation bool
		makeStale            func(*testing.T, platformGenerationFinalizationFixture, PlatformGenerationPersistenceInput, *models.Conversation)
	}{
		{"user audit", false, func(t *testing.T, f platformGenerationFinalizationFixture, input PlatformGenerationPersistenceInput, _ *models.Conversation) {
			if err := f.db.Model(&models.User{}).Where("id = ?", f.user.ID).Update("updated_at", input.NowMillis+1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"daily reset timestamp", false, func(t *testing.T, f platformGenerationFinalizationFixture, input PlatformGenerationPersistenceInput, _ *models.Conversation) {
			if err := f.db.Model(&models.User{}).Where("id = ?", f.user.ID).Update("daily_calls_reset_at", input.NowMillis+1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"conversation audit", true, func(t *testing.T, f platformGenerationFinalizationFixture, input PlatformGenerationPersistenceInput, conversation *models.Conversation) {
			if err := f.db.Model(&models.Conversation{}).Where("id = ?", conversation.ID).Update("updated_at", input.NowMillis+1).Error; err != nil {
				t.Fatal(err)
			}
			conversation.UpdatedAt = input.NowMillis + 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := openPlatformGenerationFinalizationFixture(t)
			input := f.committingSingle(t)
			var conversation models.Conversation
			if test.existingConversation {
				conversation = models.Conversation{
					AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: f.now, CreatedBy: &f.user.ID, UpdatedAt: f.now, UpdatedBy: &f.user.ID},
					UserID:      f.user.ID, Title: "新对话",
				}
				if err := f.db.Create(&conversation).Error; err != nil {
					t.Fatal(err)
				}
				input.ConversationGUID = &conversation.Guid
			}
			test.makeStale(t, f, input, &conversation)
			p, err := NewPlatformGenerationPersistence(f.store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
				t.Fatalf("Finalize() error=%v, want conflict", err)
			}
			conversationCount := int64(0)
			if test.existingConversation {
				conversationCount = 1
			}
			assertPlatformGenerationFinalizationEffects(t, f, conversationCount, 0, 0, 0, 0, 2, 10)
		})
	}
}

func TestPlatformGenerationPersistenceSingleAllowsEqualLockedTimestamps(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingSingle(t)
	conversation := models.Conversation{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: f.now, CreatedBy: &f.user.ID, UpdatedAt: input.NowMillis, UpdatedBy: &f.user.ID},
		UserID:      f.user.ID,
		Title:       "新对话",
	}
	if err := f.db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.User{}).Where("id = ?", f.user.ID).Updates(map[string]any{
		"updated_at": input.NowMillis, "daily_calls_reset_at": input.NowMillis,
	}).Error; err != nil {
		t.Fatal(err)
	}
	input.ConversationGUID = &conversation.Guid
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Finalize(context.Background(), f.db, input); err != nil {
		t.Fatalf("equal locked timestamps rejected: %v", err)
	}
	assertPlatformGenerationFinalizationEffects(t, f, 1, 2, 1, 1, 1, 3, 17)
}

func TestPlatformGenerationPersistenceSingleAllowsNoOpConversationWrites(t *testing.T) {
	for _, test := range []struct {
		name                 string
		existingConversation bool
	}{
		{name: "new conversation with whitespace user message"},
		{name: "existing conversation with same title and audit", existingConversation: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := openPlatformGenerationFinalizationFixture(t)
			input := f.committingSingle(t)
			input.UserMessage = "   "
			conversationUpdates := 0
			hook := fmt.Sprintf("platform_single_noop_%d", testSnowflake.Next())
			if err := f.db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
				table := tx.Statement.Table
				if table == "" && tx.Statement.Schema != nil {
					table = tx.Statement.Schema.Table
				}
				if table == "conversations" {
					conversationUpdates++
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer f.db.Callback().Update().Remove(hook)

			var conversation models.Conversation
			if test.existingConversation {
				conversation = models.Conversation{
					AuditFields: models.AuditFields{
						Guid: testSnowflake.Next(), CreatedAt: f.now, CreatedBy: &f.user.ID,
						UpdatedAt: input.NowMillis, UpdatedBy: &f.user.ID,
					},
					UserID: f.user.ID, Title: "新对话",
				}
				if err := f.db.Create(&conversation).Error; err != nil {
					t.Fatal(err)
				}
				input.ConversationGUID = &conversation.Guid
			}

			p, err := NewPlatformGenerationPersistence(f.store)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := p.Finalize(context.Background(), f.db, input)
			if err != nil {
				t.Fatalf("Finalize() rejected valid no-op conversation write: %v", err)
			}
			if receipt.UserMessage != input.UserMessage {
				t.Fatalf("receipt user message=%q, want exact bytes %q", receipt.UserMessage, input.UserMessage)
			}
			if conversationUpdates != 0 {
				t.Fatalf("redundant conversation updates=%d, want 0", conversationUpdates)
			}
			if err := f.db.Where("guid = ?", receipt.ConversationGUID).First(&conversation).Error; err != nil {
				t.Fatal(err)
			}
			if conversation.Title != "新对话" || conversation.UpdatedAt != input.NowMillis ||
				conversation.UpdatedBy == nil || *conversation.UpdatedBy != f.user.ID {
				t.Fatalf("unexpected fallback conversation state: %#v", conversation)
			}
			var userMessage models.Message
			if err := f.db.Where("conversation_id = ? AND role = ?", conversation.ID, models.MessageRoleUser).First(&userMessage).Error; err != nil {
				t.Fatal(err)
			}
			if userMessage.Content != input.UserMessage {
				t.Fatalf("stored user message=%q, want exact bytes %q", userMessage.Content, input.UserMessage)
			}
			assertPlatformGenerationFinalizationEffects(t, f, 1, 2, 1, 1, 1, 3, 17)
		})
	}
}

func TestPlatformGenerationPersistenceSingleWriteErrorsDoNotLogMessageContent(t *testing.T) {
	for _, test := range []struct {
		name        string
		blockedText string
	}{
		{"user message", "TASK5PROMPTSENTINEL"},
		{"assistant message", "TASK5ANSWERSENTINEL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := openPlatformGenerationFinalizationFixture(t)
			input := f.committingSingle(t)
			input.UserMessage = "TASK5PROMPTSENTINEL"
			input.Results[0].Content = "TASK5ANSWERSENTINEL"
			constraint := fmt.Sprintf("chk_finalize_sensitive_%d", testSnowflake.Next())
			if err := f.db.Exec(fmt.Sprintf("ALTER TABLE messages ADD CONSTRAINT %s CHECK (content <> '%s')", constraint, test.blockedText)).Error; err != nil {
				t.Fatal(err)
			}
			var captured bytes.Buffer
			captureLogger := gormlogger.New(log.New(&captured, "", 0), gormlogger.Config{
				SlowThreshold: time.Nanosecond, LogLevel: gormlogger.Info, ParameterizedQueries: false, Colorful: false,
			})
			p, err := NewPlatformGenerationPersistence(f.store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Finalize(context.Background(), f.db.Session(&gorm.Session{Logger: captureLogger}), input); !errors.Is(err, ErrPlatformGenerationPersistenceUnavailable) {
				t.Fatalf("Finalize() error=%v, want unavailable", err)
			}
			if output := captured.String(); strings.Contains(output, input.UserMessage) || strings.Contains(output, input.Results[0].Content) {
				t.Fatalf("sensitive generation content reached GORM logs: %q", output)
			}
			assertPlatformGenerationFinalizationEffects(t, f, 0, 0, 0, 0, 0, 2, 10)
		})
	}
}

func TestPlatformGenerationPersistenceCommitUnknownUsesBoundedIndependentContextAndPreservesIntegrity(t *testing.T) {
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	input := validPlatformGenerationPersistenceInput()
	matching := PlatformGenerationReceiptSnapshot{
		UserID: input.UserID, GenerationID: input.GenerationID, Mode: input.Mode, ConversationGUID: 2,
		UserMessage: input.UserMessage, SuccessfulModelCount: 1, DailyCallsCharged: 1, TotalTokens: 1, CommittedAtMillis: 1,
		Results: []PlatformGenerationCommittedResult{{
			Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "3", Content: "answer", Tokens: 1,
		}},
	}
	run := func(t *testing.T, snapshot PlatformGenerationReceiptSnapshot, readErr, runErr, wantErr error) PlatformGenerationReceiptSnapshot {
		t.Helper()
		called := false
		p := &PlatformGenerationPersistence{loadReceipt: func(ctx context.Context, _ *gorm.DB, _ int64, _ string) (PlatformGenerationReceiptSnapshot, error) {
			called = true
			if err := ctx.Err(); err != nil {
				t.Fatalf("recovery inherited caller cancellation: %v", err)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > platformGenerationReceiptRecoveryTimeout {
				t.Fatalf("recovery context deadline=%v ok=%v", deadline, ok)
			}
			return snapshot, readErr
		}}
		resolved, err := p.resolveRunTxError(caller, &gorm.DB{}, input, runErr)
		if !called || !errors.Is(err, wantErr) || (wantErr == nil && err != nil) {
			t.Fatalf("called=%v error=%v, want %v", called, err, wantErr)
		}
		return resolved
	}
	t.Run("matching receipt", func(t *testing.T) {
		if got := run(t, matching, nil, errors.New("commit status unknown"), nil); got.GenerationID != input.GenerationID {
			t.Fatalf("unexpected resolved receipt: %#v", got)
		}
	})
	t.Run("mismatched receipt", func(t *testing.T) {
		mismatched := matching
		mismatched.UserMessage = "different"
		run(t, mismatched, nil, errors.New("commit status unknown"), ErrPlatformGenerationPersistenceConflict)
	})
	t.Run("integrity", func(t *testing.T) {
		run(t, PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity, errors.New("commit status unknown"), ErrPlatformGenerationPersistenceIntegrity)
	})
	t.Run("not found preserves typed error", func(t *testing.T) {
		run(t, PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceNotFound, ErrPlatformGenerationPersistenceQuota, ErrPlatformGenerationPersistenceQuota)
	})
	t.Run("not found maps operational error", func(t *testing.T) {
		run(t, PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceNotFound, errors.New("commit status unknown"), ErrPlatformGenerationPersistenceUnavailable)
	})
	t.Run("not found duplicate remains unavailable", func(t *testing.T) {
		run(t, PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceNotFound, &mysqlDriver.MySQLError{Number: 1062}, ErrPlatformGenerationPersistenceUnavailable)
	})
	t.Run("unavailable read wins", func(t *testing.T) {
		run(t, PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable, ErrPlatformGenerationPersistenceQuota, ErrPlatformGenerationPersistenceUnavailable)
	})
}

const (
	platformCompareGenerationID = "550e8400-e29b-41d4-a716-446655440101"
	platformQuotaGenerationOne  = "550e8400-e29b-41d4-a716-446655440102"
	platformQuotaGenerationTwo  = "550e8400-e29b-41d4-a716-446655440103"
)

func (f platformGenerationFinalizationFixture) committingResults(t *testing.T, generationID string, mode PlatformGenerationMode, results []PlatformGenerationPersistenceResult) PlatformGenerationPersistenceInput {
	t.Helper()
	modelsInOrder := make([]string, len(results))
	for index := range results {
		modelsInOrder[index] = results[index].Model
	}
	claimTestGeneration(t, f.store, PlatformGenerationClaimInput{
		UserID: f.user.ID, GenerationID: generationID, Mode: mode, Models: modelsInOrder, NowMillis: f.now,
	})
	for index, result := range results {
		var err error
		if result.State == PlatformGenerationStateCompleted {
			_, err = f.store.MarkModelDone(context.Background(), f.user.ID, generationID, result.Model, result.Seq, f.now+int64(index)+1)
		} else {
			_, err = f.store.MarkModelFailed(context.Background(), f.user.ID, generationID, result.Model, result.ErrorCode, f.now+int64(index)+1)
		}
		if err != nil {
			t.Fatalf("mark model %q terminal: %v", result.Model, err)
		}
	}
	if _, err := f.store.BeginCommit(context.Background(), f.user.ID, generationID, f.now+int64(len(results))+1); err != nil {
		t.Fatalf("begin compare commit: %v", err)
	}
	t.Cleanup(func() { _ = f.store.client.Del(context.Background(), f.store.key(f.user.ID, generationID)).Err() })
	return PlatformGenerationPersistenceInput{
		UserID: f.user.ID, GenerationID: generationID, Mode: mode, Models: modelsInOrder,
		UserMessage: " compare prompt bytes ", Results: results, NowMillis: f.now + int64(len(results)) + 2,
	}
}

func fullCompareResults() []PlatformGenerationPersistenceResult {
	return []PlatformGenerationPersistenceResult{
		{Model: "Model-A", State: PlatformGenerationStateCompleted, Content: "answer A", Tokens: 3, Seq: 0},
		{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "answer a", Tokens: 5, Seq: 0},
		{Model: "model-c", State: PlatformGenerationStateCompleted, Content: "answer c", Tokens: 7, Seq: 0},
	}
}

func partialCompareResults() []PlatformGenerationPersistenceResult {
	return []PlatformGenerationPersistenceResult{
		{Model: "Model-A", State: PlatformGenerationStateCompleted, Content: "answer A", Tokens: 7, Seq: 0},
		{Model: "model-b", State: PlatformGenerationStateFailed, Tokens: 0, Seq: 0, ErrorCode: "timeout"},
	}
}

func TestPlatformGenerationPersistenceFinalizesCompareWithIndependentMessages(t *testing.T) {
	t.Run("dependency-free mode guard", func(t *testing.T) {
		store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		p, err := NewPlatformGenerationPersistence(store)
		if err != nil {
			t.Fatal(err)
		}
		input := PlatformGenerationPersistenceInput{
			UserID: 1, GenerationID: platformCompareGenerationID, Mode: PlatformGenerationModeCompare,
			Models: []string{"model-a", "model-b"}, UserMessage: "prompt", NowMillis: 1,
			Results: []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-b", State: PlatformGenerationStateCompleted, Content: "two", Tokens: 2},
			},
		}
		if _, err := p.Finalize(context.Background(), &gorm.DB{}, input); !errors.Is(err, ErrPlatformGenerationPersistenceUnavailable) {
			t.Fatalf("valid compare rejected by dependency-free guard: %v", err)
		}
	})

	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingResults(t, platformCompareGenerationID, PlatformGenerationModeCompare, fullCompareResults())
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Finalize(context.Background(), f.db, input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Mode != PlatformGenerationModeCompare || receipt.UserMessage != input.UserMessage ||
		receipt.SuccessfulModelCount != 3 || receipt.DailyCallsCharged != 3 || receipt.TotalTokens != 15 ||
		len(receipt.Results) != 3 {
		t.Fatalf("unexpected compare receipt: %#v", receipt)
	}
	guids := make(map[string]struct{}, 3)
	for index, result := range receipt.Results {
		want := input.Results[index]
		if result.Model != want.Model || result.State != want.State || result.Content != want.Content ||
			result.Tokens != want.Tokens || result.ErrorCode != "" || result.AssistantMessageGUID == "" {
			t.Fatalf("result[%d]=%#v, want input %#v", index, result, want)
		}
		if _, duplicate := guids[result.AssistantMessageGUID]; duplicate {
			t.Fatalf("assistant GUID reused: %q", result.AssistantMessageGUID)
		}
		guids[result.AssistantMessageGUID] = struct{}{}
	}
	var messages []models.Message
	if err := f.db.Order("id ASC").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[0].Role != models.MessageRoleUser || messages[0].Content != input.UserMessage {
		t.Fatalf("unexpected compare messages: %#v", messages)
	}
	for index, message := range messages[1:] {
		want := input.Results[index]
		if message.Role != models.MessageRoleAssistant || message.Model == nil || *message.Model != want.Model ||
			message.Content != want.Content || int64(message.Tokens) != want.Tokens || strings.Contains(message.Content, "__MULTI_MODEL__") ||
			stringInt64(message.Guid) != receipt.Results[index].AssistantMessageGUID {
			t.Fatalf("assistant[%d]=%#v, want independent result %#v", index, message, want)
		}
	}
	var usage []models.UsageRecord
	if err := f.db.Order("id ASC").Find(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if len(usage) != 3 {
		t.Fatalf("usage count=%d, want one per successful model", len(usage))
	}
	for index, record := range usage {
		want := input.Results[index]
		if record.Model == nil || *record.Model != want.Model || int64(record.Tokens) != want.Tokens {
			t.Fatalf("usage[%d]=%#v, want model/tokens from %#v", index, record, want)
		}
	}
	assertPlatformGenerationFinalizationEffects(t, f, 1, 4, 3, 1, 3, 5, 25)
}

func TestPlatformGenerationPersistenceFinalizesPartialCompareWithoutChargingFailure(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingResults(t, platformCompareGenerationID, PlatformGenerationModeCompare, partialCompareResults())
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Finalize(context.Background(), f.db, input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SuccessfulModelCount != 1 || receipt.DailyCallsCharged != 1 || receipt.TotalTokens != 7 || len(receipt.Results) != 2 {
		t.Fatalf("unexpected partial receipt: %#v", receipt)
	}
	failed := receipt.Results[1]
	if failed.Model != "model-b" || failed.State != PlatformGenerationStateFailed || failed.AssistantMessageGUID != "" ||
		failed.Content != "" || failed.Tokens != 0 || failed.ErrorCode != "timeout" {
		t.Fatalf("failed result persisted incorrectly: %#v", failed)
	}
	var messages []models.Message
	if err := f.db.Order("id ASC").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Model == nil || *messages[1].Model != input.Results[0].Model ||
		stringInt64(messages[1].Guid) != receipt.Results[0].AssistantMessageGUID {
		t.Fatalf("partial compare messages=%#v receipt=%#v", messages, receipt)
	}
	var usage []models.UsageRecord
	if err := f.db.Find(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].Model == nil || *usage[0].Model != input.Results[0].Model || int64(usage[0].Tokens) != input.Results[0].Tokens {
		t.Fatalf("failed model was charged or successful usage malformed: %#v", usage)
	}
	assertPlatformGenerationFinalizationEffects(t, f, 1, 2, 1, 1, 2, 3, 17)
}

func TestPlatformGenerationPersistenceAllModelFailureWritesNothing(t *testing.T) {
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	p := &PlatformGenerationPersistence{
		generations: store,
		loadReceipt: func(context.Context, *gorm.DB, int64, string) (PlatformGenerationReceiptSnapshot, error) {
			panic("receipt dependency touched")
		},
		runLocked: func(context.Context, *gorm.DB, string, func(*gorm.DB) error) error {
			panic("transaction dependency touched")
		},
	}
	input := PlatformGenerationPersistenceInput{
		UserID: 1, GenerationID: platformCompareGenerationID, Mode: PlatformGenerationModeCompare,
		Models: []string{"model-a", "model-b"}, UserMessage: "prompt", NowMillis: 1,
		Results: []PlatformGenerationPersistenceResult{
			{Model: "model-a", State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
			{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "upstream_error"},
		},
	}
	if _, err := p.Finalize(context.Background(), &gorm.DB{}, input); !errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
		t.Fatalf("Finalize() error=%v, want invalid before dependencies", err)
	}
}

func TestPlatformGenerationPersistenceConcurrentDuplicateHasOneWinner(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingResults(t, platformCompareGenerationID, PlatformGenerationModeCompare, partialCompareResults())
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	snapshots := make([]PlatformGenerationReceiptSnapshot, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for index := range snapshots {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			snapshots[index], errs[index] = p.Finalize(context.Background(), f.db, input)
		}(index)
	}
	wg.Wait()
	for index := range snapshots {
		if errs[index] != nil {
			t.Fatalf("worker %d: %v", index, errs[index])
		}
		if !reflect.DeepEqual(snapshots[0], snapshots[index]) {
			t.Fatalf("worker %d snapshot differs:\nfirst=%#v\ngot=%#v", index, snapshots[0], snapshots[index])
		}
	}
	assertPlatformGenerationFinalizationEffects(t, f, 1, 2, 1, 1, 2, 3, 17)
}

func TestPlatformGenerationPersistenceConflictingDuplicateIsRejected(t *testing.T) {
	t.Run("mysql 1062 classifier", func(t *testing.T) {
		wrapped := fmt.Errorf("wrapped: %w", &mysqlDriver.MySQLError{Number: 1062, Message: "private duplicate detail"})
		if !isPlatformGenerationDuplicateKey(wrapped) || isPlatformGenerationDuplicateKey(&mysqlDriver.MySQLError{Number: 1213}) || isPlatformGenerationDuplicateKey(nil) {
			t.Fatal("duplicate-key classifier rejected 1062 or accepted a non-duplicate")
		}
	})
	for _, test := range []struct {
		name    string
		partial bool
		mutate  func(*PlatformGenerationPersistenceInput)
	}{
		{"content", false, func(input *PlatformGenerationPersistenceInput) { input.Results[0].Content = "different answer" }},
		{"tokens", false, func(input *PlatformGenerationPersistenceInput) { input.Results[0].Tokens++ }},
		{"model", false, func(input *PlatformGenerationPersistenceInput) {
			input.Models[0], input.Results[0].Model = "model-z", "model-z"
		}},
		{"error", true, func(input *PlatformGenerationPersistenceInput) { input.Results[1].ErrorCode = "upstream_error" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := openPlatformGenerationFinalizationFixture(t)
			results := fullCompareResults()
			if test.partial {
				results = partialCompareResults()
			}
			input := f.committingResults(t, platformCompareGenerationID, PlatformGenerationModeCompare, results)
			p, err := NewPlatformGenerationPersistence(f.store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Finalize(context.Background(), f.db, input); err != nil {
				t.Fatal(err)
			}
			test.mutate(&input)
			if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
				t.Fatalf("conflicting duplicate error=%v, want conflict", err)
			}
			if test.partial {
				assertPlatformGenerationFinalizationEffects(t, f, 1, 2, 1, 1, 2, 3, 17)
			} else {
				assertPlatformGenerationFinalizationEffects(t, f, 1, 4, 3, 1, 3, 5, 25)
			}
		})
	}
}

func TestPlatformGenerationPersistenceDuplicateRejectsDifferentUserMessage(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingResults(t, platformCompareGenerationID, PlatformGenerationModeCompare, partialCompareResults())
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Finalize(context.Background(), f.db, input); err != nil {
		t.Fatal(err)
	}
	input.UserMessage = "different prompt bytes"
	if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
		t.Fatalf("different user message error=%v, want conflict", err)
	}
	assertPlatformGenerationFinalizationEffects(t, f, 1, 2, 1, 1, 2, 3, 17)
}

func TestPlatformGenerationPersistenceDuplicateIgnoresRetryTimestamp(t *testing.T) {
	t.Run("pure matcher", func(t *testing.T) {
		input := validPlatformGenerationPersistenceInput()
		receipt := PlatformGenerationReceiptSnapshot{
			UserID: input.UserID, GenerationID: input.GenerationID, Mode: input.Mode, ConversationGUID: 2,
			RequestedExistingConversation: false,
			UserMessage:                   input.UserMessage, SuccessfulModelCount: 1, DailyCallsCharged: 1, TotalTokens: 1, CommittedAtMillis: 7,
			Results: []PlatformGenerationCommittedResult{{Model: "model-a", State: PlatformGenerationStateCompleted, AssistantMessageGUID: "3", Content: "answer", Tokens: 1}},
		}
		input.NowMillis = 99
		input.Results[0].Seq = 42
		if !platformReceiptMatchesInput(receipt, input) {
			t.Fatal("receipt match incorrectly compared retry timestamp or Redis-only sequence")
		}
		conversationGUID := receipt.ConversationGUID
		input.ConversationGUID = &conversationGUID
		if platformReceiptMatchesInput(receipt, input) {
			t.Fatal("new-conversation receipt accepted an explicit retry of its resulting conversation")
		}
		receipt.RequestedExistingConversation = true
		if !platformReceiptMatchesInput(receipt, input) {
			t.Fatal("same existing-conversation request was not idempotent")
		}
		input.ConversationGUID = nil
		if platformReceiptMatchesInput(receipt, input) {
			t.Fatal("existing-conversation receipt accepted a retry requesting a new conversation")
		}
		receipt.RequestedExistingConversation = false
		for _, mutate := range []func(*PlatformGenerationPersistenceInput){
			func(value *PlatformGenerationPersistenceInput) { value.UserMessage = "different" },
			func(value *PlatformGenerationPersistenceInput) { value.Results[0].Model = "model-b" },
			func(value *PlatformGenerationPersistenceInput) { value.Results[0].Content = "different" },
			func(value *PlatformGenerationPersistenceInput) { value.Results[0].Tokens++ },
			func(value *PlatformGenerationPersistenceInput) {
				value.Results[0].State = PlatformGenerationStateFailed
				value.Results[0].Content = ""
				value.Results[0].Tokens = 0
				value.Results[0].ErrorCode = "timeout"
			},
		} {
			candidate := input
			candidate.Models = append([]string(nil), input.Models...)
			candidate.Results = append([]PlatformGenerationPersistenceResult(nil), input.Results...)
			mutate(&candidate)
			if platformReceiptMatchesInput(receipt, candidate) {
				t.Fatal("receipt matcher accepted a changed idempotency field")
			}
		}
	})
	t.Run("integration", func(t *testing.T) {
		f := openPlatformGenerationFinalizationFixture(t)
		input := f.committingResults(t, platformCompareGenerationID, PlatformGenerationModeCompare, partialCompareResults())
		p, err := NewPlatformGenerationPersistence(f.store)
		if err != nil {
			t.Fatal(err)
		}
		first, err := p.Finalize(context.Background(), f.db, input)
		if err != nil {
			t.Fatal(err)
		}
		input.NowMillis += 100
		retry, err := p.Finalize(context.Background(), f.db, input)
		if err != nil || !reflect.DeepEqual(first, retry) || retry.CommittedAtMillis != first.CommittedAtMillis {
			t.Fatalf("later retry=%#v error=%v, want original %#v", retry, err, first)
		}
		input.NowMillis = f.now + 1
		if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
			t.Fatalf("stale retry error=%v, want conflict", err)
		}
	})
}

func TestPlatformGenerationPersistenceDuplicateRequiresExactConversationProvenance(t *testing.T) {
	t.Run("existing conversation requires exact request guid", func(t *testing.T) {
		f := openPlatformGenerationFinalizationFixture(t)
		conversation := models.Conversation{
			AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: f.now - 1, CreatedBy: &f.user.ID, UpdatedAt: f.now - 1, UpdatedBy: &f.user.ID},
			UserID:      f.user.ID,
			Title:       "existing",
		}
		if err := f.db.Create(&conversation).Error; err != nil {
			t.Fatal(err)
		}
		otherConversation := models.Conversation{
			AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: f.now - 1, CreatedBy: &f.user.ID, UpdatedAt: f.now - 1, UpdatedBy: &f.user.ID},
			UserID:      f.user.ID,
			Title:       "other existing",
		}
		if err := f.db.Create(&otherConversation).Error; err != nil {
			t.Fatal(err)
		}
		input := f.committingSingle(t)
		input.ConversationGUID = &conversation.Guid
		p, err := NewPlatformGenerationPersistence(f.store)
		if err != nil {
			t.Fatal(err)
		}
		first, err := p.Finalize(context.Background(), f.db, input)
		if err != nil {
			t.Fatal(err)
		}
		retry, err := p.Finalize(context.Background(), f.db, input)
		if err != nil || !reflect.DeepEqual(first, retry) {
			t.Fatalf("same existing-conversation retry=%#v error=%v, want %#v", retry, err, first)
		}
		input.ConversationGUID = &otherConversation.Guid
		if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
			t.Fatalf("existing-to-different-existing retry error=%v, want conflict", err)
		}
		assertPlatformGenerationFinalizationEffects(t, f, 2, 2, 1, 1, 1, 3, 17)
		input.ConversationGUID = nil
		if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
			t.Fatalf("existing-to-new retry error=%v, want conflict", err)
		}
		assertPlatformGenerationFinalizationEffects(t, f, 2, 2, 1, 1, 1, 3, 17)
	})

	t.Run("new conversation cannot retry as explicit result", func(t *testing.T) {
		f := openPlatformGenerationFinalizationFixture(t)
		input := f.committingSingle(t)
		p, err := NewPlatformGenerationPersistence(f.store)
		if err != nil {
			t.Fatal(err)
		}
		first, err := p.Finalize(context.Background(), f.db, input)
		if err != nil {
			t.Fatal(err)
		}
		retry, err := p.Finalize(context.Background(), f.db, input)
		if err != nil || !reflect.DeepEqual(first, retry) {
			t.Fatalf("same new-conversation retry=%#v error=%v, want %#v", retry, err, first)
		}
		input.ConversationGUID = &first.ConversationGUID
		if _, err := p.Finalize(context.Background(), f.db, input); !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
			t.Fatalf("new-to-explicit-result retry error=%v, want conflict", err)
		}
	})
}

func TestPlatformGenerationPersistenceQuotaRaceCannotOverrunLimit(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	if err := f.db.Model(&models.User{}).Where("id = ?", f.user.ID).Updates(map[string]any{"daily_calls_used": 4, "daily_call_limit": 5}).Error; err != nil {
		t.Fatal(err)
	}
	f.user.DailyCallsUsed = 4
	result := []PlatformGenerationPersistenceResult{{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "answer", Tokens: 2, Seq: 0}}
	inputs := []PlatformGenerationPersistenceInput{
		f.committingResults(t, platformQuotaGenerationOne, PlatformGenerationModeSingle, result),
		f.committingResults(t, platformQuotaGenerationTwo, PlatformGenerationModeSingle, result),
	}
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for index := range inputs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, errs[index] = p.Finalize(context.Background(), f.db, inputs[index])
		}(index)
	}
	wg.Wait()
	successes, quota := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrPlatformGenerationPersistenceQuota):
			quota++
		default:
			t.Fatalf("unexpected quota race error: %v", err)
		}
	}
	if successes != 1 || quota != 1 {
		t.Fatalf("quota race successes/quota=%d/%d, want 1/1; errors=%v", successes, quota, errs)
	}
	assertPlatformGenerationFinalizationEffects(t, f, 1, 2, 1, 1, 1, 5, 12)
}

func TestPlatformGenerationPersistenceCommitUnknownResolvesFromReceipt(t *testing.T) {
	f := openPlatformGenerationFinalizationFixture(t)
	input := f.committingResults(t, platformCompareGenerationID, PlatformGenerationModeCompare, partialCompareResults())
	p, err := NewPlatformGenerationPersistence(f.store)
	if err != nil {
		t.Fatal(err)
	}
	originalRunLocked := p.runLocked
	sentinel := errors.New("commit acknowledgement lost")
	p.runLocked = func(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
		if err := originalRunLocked(ctx, db, lockName, fn); err != nil {
			return err
		}
		return sentinel
	}
	receipt, err := p.Finalize(context.Background(), f.db, input)
	if err != nil || !platformReceiptMatchesInput(receipt, input) {
		t.Fatalf("commit-unknown receipt=%#v error=%v", receipt, err)
	}
	assertPlatformGenerationFinalizationEffects(t, f, 1, 2, 1, 1, 2, 3, 17)
}

func assertPlatformGenerationFinalizationEffects(t *testing.T, f platformGenerationFinalizationFixture, conversations, messages, usage, receipts, results int64, dailyCalls int, totalTokens int64) {
	t.Helper()
	counts := []struct {
		name string
		got  int64
		want int64
	}{
		{name: "conversations", want: conversations},
		{name: "messages", want: messages},
		{name: "usage_records", want: usage},
		{name: "platform_chat_generation_receipts", want: receipts},
		{name: "platform_chat_generation_results", want: results},
	}
	modelsToCount := []any{&models.Conversation{}, &models.Message{}, &models.UsageRecord{}, &models.PlatformChatGenerationReceipt{}, &models.PlatformChatGenerationResult{}}
	for index := range counts {
		if err := f.db.Model(modelsToCount[index]).Count(&counts[index].got).Error; err != nil {
			t.Fatal(err)
		}
		if counts[index].got != counts[index].want {
			t.Fatalf("%s count=%d, want %d", counts[index].name, counts[index].got, counts[index].want)
		}
	}
	var user models.User
	if err := f.db.First(&user, f.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.DailyCallsUsed != dailyCalls || user.TotalTokensUsed != totalTokens {
		t.Fatalf("user counters=%d/%d, want %d/%d", user.DailyCallsUsed, user.TotalTokensUsed, dailyCalls, totalTokens)
	}
}
