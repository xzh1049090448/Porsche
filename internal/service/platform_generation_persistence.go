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

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

func (p *PlatformGenerationPersistence) Finalize(ctx context.Context, db *gorm.DB, input PlatformGenerationPersistenceInput) (PlatformGenerationReceiptSnapshot, error) {
	if p == nil || p.generations == nil || p.runTx == nil || ctx == nil || db == nil || validatePlatformGenerationPersistenceInput(input) != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceInvalid
	}

	redisSnapshot, err := p.generations.Get(ctx, input.UserID, input.GenerationID)
	if err != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable
	}
	if !platformPersistenceMatchesRedis(input, redisSnapshot) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceConflict
	}
	if input.Mode != PlatformGenerationModeSingle {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceInvalid
	}

	if existing, loadErr := LoadPlatformGenerationReceipt(ctx, db, input.UserID, input.GenerationID); loadErr == nil {
		if platformReceiptMatchesInput(existing, input) {
			return existing, nil
		}
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceConflict
	} else if !errors.Is(loadErr, ErrPlatformGenerationPersistenceNotFound) {
		return PlatformGenerationReceiptSnapshot{}, loadErr
	}

	err = p.runTx(ctx, db, platformGenerationAdvisoryLockName(input.UserID, input.GenerationID), func(tx *gorm.DB) error {
		return persistPlatformGeneration(tx, input)
	})
	if err == nil {
		return LoadPlatformGenerationReceipt(ctx, db, input.UserID, input.GenerationID)
	}

	resolved, readErr := LoadPlatformGenerationReceipt(context.WithoutCancel(ctx), db, input.UserID, input.GenerationID)
	if readErr == nil {
		if platformReceiptMatchesInput(resolved, input) {
			return resolved, nil
		}
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceConflict
	}
	if errors.Is(err, ErrPlatformGenerationPersistenceQuota) ||
		errors.Is(err, ErrPlatformGenerationPersistenceConflict) ||
		errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
		return PlatformGenerationReceiptSnapshot{}, err
	}
	return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable
}

func platformPersistenceMatchesRedis(input PlatformGenerationPersistenceInput, snapshot PlatformGenerationSnapshot) bool {
	if snapshot.State != PlatformGenerationStateCommitting || snapshot.GenerationID != input.GenerationID ||
		snapshot.Mode != input.Mode || len(snapshot.Models) != len(input.Models) || len(snapshot.ModelStates) != len(input.Models) {
		return false
	}
	for index, model := range input.Models {
		state, ok := snapshot.ModelStates[model]
		if !ok || snapshot.Models[index] != model || state.State != input.Results[index].State ||
			state.AssistantMessageGUID != "" || state.ErrorCode != input.Results[index].ErrorCode {
			return false
		}
	}
	return true
}

func platformReceiptMatchesInput(receipt PlatformGenerationReceiptSnapshot, input PlatformGenerationPersistenceInput) bool {
	if receipt.UserID != input.UserID || receipt.GenerationID != input.GenerationID || receipt.Mode != input.Mode ||
		receipt.UserMessage != input.UserMessage || len(receipt.Results) != len(input.Results) {
		return false
	}
	if input.ConversationGUID != nil && receipt.ConversationGUID != *input.ConversationGUID {
		return false
	}
	successes := 0
	var totalTokens int64
	for index, result := range input.Results {
		committed := receipt.Results[index]
		if committed.Model != result.Model || committed.State != result.State || committed.Content != result.Content ||
			committed.Tokens != result.Tokens || committed.ErrorCode != result.ErrorCode {
			return false
		}
		if result.State == PlatformGenerationStateCompleted {
			if committed.AssistantMessageGUID == "" || totalTokens > math.MaxInt64-result.Tokens {
				return false
			}
			successes++
			totalTokens += result.Tokens
		} else if committed.AssistantMessageGUID != "" {
			return false
		}
	}
	return receipt.SuccessfulModelCount == successes && receipt.DailyCallsCharged == successes && receipt.TotalTokens == totalTokens
}

func persistPlatformGeneration(tx *gorm.DB, input PlatformGenerationPersistenceInput) error {
	if tx == nil {
		return ErrPlatformGenerationPersistenceInvalid
	}
	var user models.User
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = ? AND is_deleted = 0", input.UserID, models.UserStatusActive).
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrPlatformGenerationPersistenceConflict
	}
	if err != nil {
		return err
	}

	successes := 0
	var totalTokens int64
	for _, result := range input.Results {
		if result.State != PlatformGenerationStateCompleted {
			continue
		}
		if totalTokens > math.MaxInt64-result.Tokens {
			return ErrPlatformGenerationPersistenceUnavailable
		}
		successes++
		totalTokens += result.Tokens
	}
	resetDailyAt(&user, input.NowMillis)
	if user.DailyCallsUsed < 0 || user.TotalTokensUsed < 0 ||
		user.DailyCallsUsed > math.MaxInt32-successes || user.TotalTokensUsed > math.MaxInt64-totalTokens {
		return ErrPlatformGenerationPersistenceUnavailable
	}
	if user.PlanType == models.PlanFree {
		if user.DailyCallLimit < 0 || user.DailyCallsUsed > user.DailyCallLimit || successes > user.DailyCallLimit-user.DailyCallsUsed {
			return ErrPlatformGenerationPersistenceQuota
		}
	}

	var conversation models.Conversation
	if input.ConversationGUID != nil {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("guid = ? AND user_id = ? AND is_deleted = 0", *input.ConversationGUID, input.UserID).
			First(&conversation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPlatformGenerationPersistenceConflict
		}
		if err != nil {
			return err
		}
	} else {
		model := input.Models[0]
		conversation = models.Conversation{
			AuditFields: platformPersistenceAudit(input.UserID, input.NowMillis),
			UserID:      input.UserID,
			Title:       "新对话",
			Model:       &model,
		}
		if err := tx.Create(&conversation).Error; err != nil {
			return err
		}
	}

	userMessage := models.Message{
		AuditFields:    platformPersistenceAudit(input.UserID, input.NowMillis),
		ConversationID: conversation.ID,
		Role:           models.MessageRoleUser,
		Content:        input.UserMessage,
	}
	if err := tx.Create(&userMessage).Error; err != nil {
		return err
	}
	if conversation.Title == "新对话" {
		conversation.Title = truncateTitle(input.UserMessage)
	}

	messageIDs := make(map[string]int64, successes)
	for _, result := range input.Results {
		if result.State != PlatformGenerationStateCompleted {
			continue
		}
		model := result.Model
		message := models.Message{
			AuditFields:    platformPersistenceAudit(input.UserID, input.NowMillis),
			ConversationID: conversation.ID,
			Role:           models.MessageRoleAssistant,
			Content:        result.Content,
			Model:          &model,
			Tokens:         int(result.Tokens),
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		messageIDs[result.Model] = message.ID
		usage := models.UsageRecord{
			AuditFields: platformPersistenceAudit(input.UserID, input.NowMillis),
			UserID:      input.UserID,
			RecordType:  models.UsageRecordChat,
			Tokens:      int(result.Tokens),
			Model:       &model,
		}
		if err := tx.Create(&usage).Error; err != nil {
			return err
		}
	}

	conversation.UpdatedAt = input.NowMillis
	conversation.UpdatedBy = &input.UserID
	if err := tx.Save(&conversation).Error; err != nil {
		return err
	}
	user.DailyCallsUsed += successes
	user.TotalTokensUsed += totalTokens
	user.UpdatedAt = input.NowMillis
	user.UpdatedBy = &input.UserID
	if err := tx.Save(&user).Error; err != nil {
		return err
	}

	receipt := models.PlatformChatGenerationReceipt{
		AuditFields:          platformPersistenceAudit(input.UserID, input.NowMillis),
		UserID:               input.UserID,
		GenerationID:         input.GenerationID,
		Mode:                 models.PlatformGenerationReceiptMode(input.Mode),
		ConversationID:       conversation.ID,
		UserMessageID:        userMessage.ID,
		SuccessfulModelCount: successes,
		DailyCallsCharged:    successes,
		TotalTokens:          totalTokens,
		CommittedAt:          input.NowMillis,
	}
	if err := tx.Create(&receipt).Error; err != nil {
		return err
	}
	for index, result := range input.Results {
		row := models.PlatformChatGenerationResult{
			AuditFields: platformPersistenceAudit(input.UserID, input.NowMillis),
			ReceiptID:   receipt.ID,
			ModelIndex:  index,
			Model:       result.Model,
			Tokens:      result.Tokens,
		}
		if result.State == PlatformGenerationStateCompleted {
			row.Status = models.PlatformGenerationResultCompleted
			messageID := messageIDs[result.Model]
			row.AssistantMessageID = &messageID
		} else {
			row.Status = models.PlatformGenerationResultFailed
			row.Tokens = 0
			code := result.ErrorCode
			row.ErrorCode = &code
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func platformPersistenceAudit(userID, nowMillis int64) models.AuditFields {
	return models.AuditFields{
		Guid: persistence.NextGUID(), CreatedAt: nowMillis, CreatedBy: &userID,
		UpdatedAt: nowMillis, UpdatedBy: &userID, IsDeleted: 0,
	}
}

func resetDailyAt(user *models.User, nowMillis int64) {
	now := time.UnixMilli(nowMillis).UTC()
	if user.DailyCallsResetAt == nil || time.UnixMilli(*user.DailyCallsResetAt).UTC().Format("2006-01-02") != now.Format("2006-01-02") {
		user.DailyCallsUsed = 0
		user.DailyCallsResetAt = &nowMillis
	}
}
