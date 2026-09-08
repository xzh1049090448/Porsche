package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

const (
	platformGenerationMessageTextMaxBytes        = 65535
	platformGenerationAdvisoryLockReleaseTimeout = 2 * time.Second
)

var (
	ErrPlatformGenerationPersistenceInvalid     = errors.New("invalid platform generation persistence input")
	ErrPlatformGenerationPersistenceNotFound    = errors.New("platform generation receipt not found")
	ErrPlatformGenerationPersistenceIntegrity   = errors.New("platform generation receipt integrity failure")
	ErrPlatformGenerationPersistenceConflict    = errors.New("platform generation persistence conflict")
	ErrPlatformGenerationPersistenceUnavailable = errors.New("platform generation persistence unavailable")
	ErrPlatformGenerationPersistenceQuota       = errors.New("platform generation quota unavailable")
)

type PlatformGenerationPersistenceResult struct {
	Model     string
	State     PlatformGenerationState
	Content   string
	Tokens    int64
	ErrorCode string
}

type PlatformGenerationPersistenceInput struct {
	UserID           int64
	GenerationID     string
	Mode             PlatformGenerationMode
	Models           []string
	ConversationGUID *int64
	UserMessage      string
	Results          []PlatformGenerationPersistenceResult
	NowMillis        int64
}

type PlatformGenerationCommittedResult struct {
	Model                string
	State                PlatformGenerationState
	AssistantMessageGUID string
	Content              string
	Tokens               int64
	ErrorCode            string
}

type PlatformGenerationReceiptSnapshot struct {
	UserID               int64
	GenerationID         string
	Mode                 PlatformGenerationMode
	ConversationGUID     int64
	UserMessage          string `json:"-"`
	SuccessfulModelCount int
	DailyCallsCharged    int
	TotalTokens          int64
	CommittedAtMillis    int64
	Results              []PlatformGenerationCommittedResult
}

type platformGenerationTxRunner func(context.Context, *gorm.DB, string, func(*gorm.DB) error) error

type PlatformGenerationPersistence struct {
	generations *PlatformGenerationStore
	runTx       platformGenerationTxRunner
}

func NewPlatformGenerationPersistence(generations *PlatformGenerationStore) (*PlatformGenerationPersistence, error) {
	if generations == nil {
		return nil, ErrPlatformGenerationPersistenceUnavailable
	}
	return &PlatformGenerationPersistence{
		generations: generations,
		runTx: func(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
			return withPlatformGenerationAdvisoryLock(ctx, db, lockName, func(conn *gorm.DB) error {
				return conn.Transaction(fn)
			})
		},
	}, nil
}

func platformGenerationAdvisoryLockName(userID int64, generationID string) string {
	digest := sha256.Sum256([]byte(strconv.FormatInt(userID, 10) + ":" + generationID))
	return "porsche:gen:v2:" + hex.EncodeToString(digest[:16])
}

func withPlatformGenerationAdvisoryLock(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
	if ctx == nil || db == nil || len(lockName) == 0 || len(lockName) > 64 || fn == nil {
		return ErrPlatformGenerationPersistenceInvalid
	}
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		var acquired sql.NullInt64
		if err := conn.Raw("SELECT GET_LOCK(?, 5)", lockName).Scan(&acquired).Error; err != nil || !acquired.Valid || acquired.Int64 != 1 {
			return ErrPlatformGenerationPersistenceUnavailable
		}
		return runWithPlatformGenerationAdvisoryLockRelease(conn, lockName, fn)
	})
}

func runWithPlatformGenerationAdvisoryLockRelease(conn *gorm.DB, lockName string, fn func(*gorm.DB) error) (primaryErr error) {
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), platformGenerationAdvisoryLockReleaseTimeout)
		defer cancel()
		var released sql.NullInt64
		releaseErr := conn.WithContext(cleanupCtx).Raw("SELECT RELEASE_LOCK(?)", lockName).Scan(&released).Error
		if primaryErr == nil && (releaseErr != nil || !released.Valid || released.Int64 != 1) {
			primaryErr = ErrPlatformGenerationPersistenceUnavailable
		}
	}()
	primaryErr = fn(conn)
	return primaryErr
}

func validatePlatformGenerationPersistenceInput(input PlatformGenerationPersistenceInput) error {
	if validatePlatformGenerationIdentity(input.UserID, input.GenerationID) != nil ||
		!platformSSEV2SafeInteger(input.NowMillis) || input.NowMillis <= 0 ||
		input.UserMessage == "" || !utf8.ValidString(input.UserMessage) ||
		len([]byte(input.UserMessage)) > platformGenerationMessageTextMaxBytes ||
		len(input.Models) != len(input.Results) {
		return ErrPlatformGenerationPersistenceInvalid
	}
	if validatePlatformGenerationInput(PlatformGenerationClaimInput{
		UserID: input.UserID, GenerationID: input.GenerationID, Mode: input.Mode,
		Models: input.Models, NowMillis: input.NowMillis,
	}) != nil {
		return ErrPlatformGenerationPersistenceInvalid
	}
	successes := 0
	for index, result := range input.Results {
		if result.Model != input.Models[index] || !platformSSEV2ModelIdentifier(result.Model) ||
			!utf8.ValidString(result.Content) ||
			len([]byte(result.Content)) > platformGenerationMessageTextMaxBytes ||
			result.Tokens < 0 || result.Tokens > math.MaxInt32 {
			return ErrPlatformGenerationPersistenceInvalid
		}
		switch result.State {
		case PlatformGenerationStateCompleted:
			if result.Content == "" || result.ErrorCode != "" {
				return ErrPlatformGenerationPersistenceInvalid
			}
			successes++
		case PlatformGenerationStateFailed:
			if result.Content != "" || result.Tokens != 0 || !platformGenerationStableCode(result.ErrorCode) {
				return ErrPlatformGenerationPersistenceInvalid
			}
		default:
			return ErrPlatformGenerationPersistenceInvalid
		}
	}
	if successes == 0 || (input.Mode == PlatformGenerationModeSingle && successes != 1) {
		return ErrPlatformGenerationPersistenceInvalid
	}
	if input.ConversationGUID != nil && *input.ConversationGUID <= 0 {
		return ErrPlatformGenerationPersistenceInvalid
	}
	return nil
}
