package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

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
