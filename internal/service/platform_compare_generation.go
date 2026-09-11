package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

var (
	ErrPlatformCompareGenerationInvalid     = errors.New("invalid platform compare generation")
	ErrPlatformCompareGenerationQuota       = errors.New("platform compare generation quota unavailable")
	ErrPlatformCompareGenerationUnavailable = errors.New("platform compare generation unavailable")
)

type platformCompareGenerationStore interface {
	Claim(context.Context, PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error)
}

type PlatformCompareGenerationInput struct {
	Context      context.Context
	User         *models.User
	GenerationID string
	RequestID    string
	Models       []string
	Params       ChatParams
	Write        func([]byte) error
}

type PlatformCompareGenerationRunResult struct {
	Started   bool
	Duplicate *PlatformGenerationSnapshot
}

type PlatformCompareGenerationRunnerAPI interface {
	Run(PlatformCompareGenerationInput) (PlatformCompareGenerationRunResult, error)
}

type platformCompareGenerationDeps struct {
	db               *gorm.DB
	store            platformCompareGenerationStore
	persistence      platformSingleGenerationPersistence
	registry         platformSingleGenerationRegistry
	upstream         platformSingleGenerationUpstream
	rootContext      context.Context
	now              func() time.Time
	newGUID          func() int64
	loadConversation func(context.Context, *gorm.DB, int64, int64) error
	loadReceipt      func(context.Context, *gorm.DB, int64, string) (PlatformGenerationReceiptSnapshot, error)
	upstreamTimeout  time.Duration
}

type PlatformCompareGenerationRunner struct {
	deps platformCompareGenerationDeps
}

func NewPlatformCompareGenerationRunner(
	db *gorm.DB,
	store *PlatformGenerationStore,
	persistenceService *PlatformGenerationPersistence,
	registry *PlatformGenerationCancellationRegistry,
	upstream *whitelabel.WhiteLabelService,
	rootContext context.Context,
	upstreamTimeout time.Duration,
) (*PlatformCompareGenerationRunner, error) {
	if db == nil || store == nil || persistenceService == nil || registry == nil || upstream == nil || rootContext == nil || upstreamTimeout <= 0 {
		return nil, ErrPlatformCompareGenerationUnavailable
	}
	deps := platformCompareGenerationDeps{
		db: db, store: store, persistence: persistenceService, registry: registry,
		upstream: upstream, rootContext: rootContext, now: time.Now,
		newGUID:          persistence.NextGUID,
		loadConversation: loadPlatformSingleConversation,
		loadReceipt:      LoadPlatformGenerationReceipt,
		upstreamTimeout:  upstreamTimeout,
	}
	if !validPlatformCompareGenerationDeps(deps) {
		return nil, ErrPlatformCompareGenerationUnavailable
	}
	return &PlatformCompareGenerationRunner{deps: deps}, nil
}

func validPlatformCompareGenerationDeps(deps platformCompareGenerationDeps) bool {
	return deps.db != nil && deps.store != nil && deps.persistence != nil && deps.registry != nil &&
		deps.upstream != nil && deps.rootContext != nil && deps.now != nil && deps.newGUID != nil &&
		deps.loadConversation != nil && deps.loadReceipt != nil && deps.upstreamTimeout > 0
}

type platformCompareRun struct {
	userID                   int64
	generationID             string
	requestID                string
	models                   []string
	params                   ChatParams
	userMessage              string
	conversationGUID         int64
	existingConversationGUID *int64
	reservedConversationGUID *int64
	payloads                 map[string][]byte
	encoder                  *PlatformSSEV2Encoder
}

func (r *PlatformCompareGenerationRunner) Run(input PlatformCompareGenerationInput) (PlatformCompareGenerationRunResult, error) {
	if r == nil || !validPlatformCompareGenerationDeps(r.deps) {
		return PlatformCompareGenerationRunResult{}, ErrPlatformCompareGenerationUnavailable
	}
	if _, _, err := r.prepare(input); err != nil {
		return PlatformCompareGenerationRunResult{}, err
	}
	return PlatformCompareGenerationRunResult{}, ErrPlatformCompareGenerationUnavailable
}

func (r *PlatformCompareGenerationRunner) prepare(input PlatformCompareGenerationInput) (platformCompareRun, int64, error) {
	if r == nil || !validPlatformCompareGenerationDeps(r.deps) {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationUnavailable
	}
	if input.Context == nil || input.User == nil || input.Write == nil {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
	}
	if input.Context.Err() != nil {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationUnavailable
	}
	if !platformSSEV2CanonicalUUID(input.GenerationID) || len(input.Models) < 2 || len(input.Models) > platformSSEV2MaxModels {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
	}
	modelsCopy := append([]string(nil), input.Models...)
	seenModels := make(map[string]struct{}, len(modelsCopy))
	for _, model := range modelsCopy {
		if !platformSSEV2ModelIdentifier(model) {
			return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
		}
		if _, duplicate := seenModels[model]; duplicate {
			return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
		}
		seenModels[model] = struct{}{}
	}
	params, err := clonePlatformSingleParams(input.Params)
	if err != nil || params.MaxTokens == nil || *params.MaxTokens <= 0 || len(params.WhiteLabelBody) == 0 || len(params.WhiteLabelBody) > whitelabel.MaxRequestBodyBytes {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
	}
	claimAt := r.deps.now().UTC().UnixMilli()
	if !platformSSEV2SafeInteger(claimAt) || claimAt <= 0 || claimAt > platformSSEV2MaxSafeInteger-platformGenerationLeaseDuration.Milliseconds() {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationUnavailable
	}
	user := models.User{
		ID: input.User.ID, AuditFields: models.AuditFields{IsDeleted: input.User.IsDeleted}, Status: input.User.Status,
		PlanType: input.User.PlanType, DailyCallLimit: input.User.DailyCallLimit, DailyCallsUsed: input.User.DailyCallsUsed,
	}
	user.DailyCallsResetAt = cloneInt64Pointer(input.User.DailyCallsResetAt)
	if !platformCompareQuotaAvailable(&user, time.UnixMilli(claimAt).UTC(), len(modelsCopy)) {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationQuota
	}
	trimmed := trimPlatformSingleMessages(params.Messages, params.ContextWindow)
	userMessage, ok := platformSingleFinalUserMessage(trimmed)
	if !ok {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
	}
	upstreamBody, err := platformCompareUpstreamBody(params.WhiteLabelBody)
	if err != nil || platformSingleExplicitUsageDisabled(upstreamBody) || whitelabel.ValidateRequest(upstreamBody, whitelabel.PlatformValidation) != nil {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
	}
	params.WhiteLabelBody = append([]byte(nil), upstreamBody...)
	payloads := make(map[string][]byte, len(modelsCopy))
	for _, model := range modelsCopy {
		payload, payloadErr := platformSingleStreamingPayload(upstreamBody, model, trimmed, params.Temperature, params.MaxTokens)
		if payloadErr != nil || whitelabel.ValidateRequest(payload, whitelabel.PlatformValidation) != nil {
			return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
		}
		payloads[model] = append([]byte(nil), payload...)
	}
	encoder, err := NewPlatformSSEV2Encoder(input.GenerationID, modelsCopy)
	if err != nil {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
	}
	run := platformCompareRun{
		userID: input.User.ID, generationID: input.GenerationID, requestID: input.RequestID,
		models: modelsCopy, params: params, userMessage: userMessage, payloads: payloads, encoder: encoder,
	}
	if params.ConversationGUID != nil {
		guid, parseErr := parseConversationGUID(*params.ConversationGUID)
		if parseErr != nil {
			return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
		}
		if loadErr := r.deps.loadConversation(input.Context, r.deps.db, user.ID, guid); loadErr != nil {
			if errors.Is(loadErr, gorm.ErrRecordNotFound) {
				return platformCompareRun{}, 0, ErrPlatformCompareGenerationInvalid
			}
			return platformCompareRun{}, 0, ErrPlatformCompareGenerationUnavailable
		}
		run.conversationGUID = guid
		run.existingConversationGUID = &guid
	} else {
		guid := r.deps.newGUID()
		if guid <= 0 {
			return platformCompareRun{}, 0, ErrPlatformCompareGenerationUnavailable
		}
		run.conversationGUID = guid
		run.reservedConversationGUID = &guid
	}
	if input.Context.Err() != nil {
		return platformCompareRun{}, 0, ErrPlatformCompareGenerationUnavailable
	}
	return run, claimAt, nil
}

func platformCompareUpstreamBody(validated []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(validated, &fields) != nil || fields == nil {
		return nil, ErrPlatformCompareGenerationInvalid
	}
	for _, field := range []string{"conversation_guid", "context_window", "stream_version", "generation_id", "models"} {
		delete(fields, field)
	}
	return json.Marshal(fields)
}

func platformCompareQuotaAvailable(user *models.User, now time.Time, requestedModels int) bool {
	if user == nil || requestedModels < 2 || requestedModels > platformSSEV2MaxModels || user.ID <= 0 || user.Status != models.UserStatusActive || user.IsDeleted != 0 {
		return false
	}
	if user.PlanType == models.PlanProfessional || user.PlanType == models.PlanEnterprise {
		return true
	}
	used := user.DailyCallsUsed
	if user.DailyCallsResetAt == nil || time.UnixMilli(*user.DailyCallsResetAt).UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		used = 0
	}
	return user.DailyCallLimit >= 0 && used >= 0 && used <= user.DailyCallLimit && user.DailyCallLimit-used >= requestedModels
}
