package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
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
	Get(context.Context, int64, string) (PlatformGenerationSnapshot, error)
	FailRunningOwned(context.Context, int64, string, string, string, int64) (PlatformGenerationSnapshot, error)
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
	newRunnerContext func(context.Context, time.Duration) (context.Context, context.CancelFunc)
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
		newRunnerContext: context.WithTimeout,
	}
	if !validPlatformCompareGenerationDeps(deps) {
		return nil, ErrPlatformCompareGenerationUnavailable
	}
	return &PlatformCompareGenerationRunner{deps: deps}, nil
}

func validPlatformCompareGenerationDeps(deps platformCompareGenerationDeps) bool {
	return deps.db != nil && deps.store != nil && deps.persistence != nil && deps.registry != nil &&
		deps.upstream != nil && deps.rootContext != nil && deps.now != nil && deps.newGUID != nil &&
		deps.loadConversation != nil && deps.loadReceipt != nil && deps.upstreamTimeout > 0 && deps.newRunnerContext != nil
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
	leaseToken               string
	payloads                 map[string][]byte
	encoder                  *PlatformSSEV2Encoder
}

type platformCompareValidatedInput struct {
	ctx              context.Context
	write            func([]byte) error
	user             models.User
	generationID     string
	requestID        string
	models           []string
	params           ChatParams
	trimmedMessages  []map[string]interface{}
	userMessage      string
	upstreamBody     []byte
	conversationGUID *int64
	claimAt          int64
}

type platformCompareOutput struct {
	write    func([]byte) error
	detached bool
}

func (o *platformCompareOutput) emit(frame []byte) bool {
	if o.detached || len(frame) == 0 {
		return false
	}
	if err := o.write(frame); err != nil {
		o.detached = true
		return false
	}
	return true
}

// platformCompareOwnedRun is the lifecycle handoff Task 4 will continue. Its
// Close method is safe to call from every post-registration exit path.
type platformCompareOwnedRun struct {
	runner            *PlatformCompareGenerationRunner
	run               platformCompareRun
	ctx               context.Context
	cancel            context.CancelFunc
	registrationToken string
	output            platformCompareOutput
	closeOnce         sync.Once
}

func (entry *platformCompareOwnedRun) Close() {
	if entry == nil {
		return
	}
	entry.closeOnce.Do(func() {
		entry.cancel()
		entry.runner.settleOwnedRunning(entry.run)
		entry.runner.deps.registry.Unregister(entry.run.userID, entry.run.generationID, entry.registrationToken)
	})
}

func (r *PlatformCompareGenerationRunner) Run(input PlatformCompareGenerationInput) (PlatformCompareGenerationRunResult, error) {
	if r == nil || !validPlatformCompareGenerationDeps(r.deps) {
		return PlatformCompareGenerationRunResult{}, ErrPlatformCompareGenerationUnavailable
	}
	validated, err := r.validateAndCopy(input)
	if err != nil {
		return PlatformCompareGenerationRunResult{}, err
	}
	entry, result, err := r.enterValidated(validated)
	if entry != nil {
		entry.Close()
		if err == nil {
			err = ErrPlatformCompareGenerationUnavailable
		}
	}
	return result, err
}

func (r *PlatformCompareGenerationRunner) enterValidated(input platformCompareValidatedInput) (*platformCompareOwnedRun, PlatformCompareGenerationRunResult, error) {
	if r == nil || !validPlatformCompareGenerationDeps(r.deps) {
		return nil, PlatformCompareGenerationRunResult{}, ErrPlatformCompareGenerationUnavailable
	}
	releaseAdmission, err := r.deps.registry.BeginAdmission()
	if err != nil || releaseAdmission == nil {
		return nil, PlatformCompareGenerationRunResult{}, ErrPlatformCompareGenerationUnavailable
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(releaseAdmission) }
	defer release()

	admissionCtx, cancelAdmission := context.WithCancel(input.ctx)
	stopRootCancellation := context.AfterFunc(r.deps.rootContext, cancelAdmission)
	if r.deps.rootContext.Err() != nil {
		cancelAdmission()
	}
	defer stopRootCancellation()
	defer cancelAdmission()
	input.ctx = admissionCtx

	run, err := r.prepareValidated(input)
	if err != nil {
		return nil, PlatformCompareGenerationRunResult{}, err
	}
	claim, claimErr := r.deps.store.Claim(input.ctx, PlatformGenerationClaimInput{
		UserID: run.userID, GenerationID: run.generationID, Mode: PlatformGenerationModeCompare,
		Models: append([]string(nil), run.models...), NowMillis: input.claimAt,
	})
	if claim.Duplicate {
		if (claimErr != nil && !errors.Is(claimErr, ErrPlatformGenerationConflict)) || !validPlatformCompareDuplicate(claim.Snapshot, run) {
			return nil, PlatformCompareGenerationRunResult{}, ErrPlatformCompareGenerationUnavailable
		}
		duplicate := clonePlatformGeneration(claim.Snapshot)
		return nil, PlatformCompareGenerationRunResult{Duplicate: &duplicate}, nil
	}
	if claimErr != nil || !validPlatformCompareClaim(claim, run, input.claimAt) {
		return nil, PlatformCompareGenerationRunResult{}, ErrPlatformCompareGenerationUnavailable
	}
	run.leaseToken = claim.LeaseToken

	runnerCtx, cancelRunner := r.deps.newRunnerContext(r.deps.rootContext, r.deps.upstreamTimeout)
	registrationToken, err := r.deps.registry.Register(run.userID, run.generationID, cancelRunner)
	if err != nil {
		cancelRunner()
		r.settleOwnedRunning(run)
		return nil, PlatformCompareGenerationRunResult{}, ErrPlatformCompareGenerationUnavailable
	}
	release()
	stopRootCancellation()
	cancelAdmission()

	entry := &platformCompareOwnedRun{
		runner: r, run: run, ctx: runnerCtx, cancel: cancelRunner, registrationToken: registrationToken,
		output: platformCompareOutput{write: input.write},
	}
	meta := run.encoder.Meta(strconv.FormatInt(run.conversationGUID, 10))
	if meta == nil || run.encoder.Err() != nil {
		entry.Close()
		return nil, PlatformCompareGenerationRunResult{}, ErrPlatformCompareGenerationUnavailable
	}
	result := PlatformCompareGenerationRunResult{Started: true}
	entry.output.emit(meta)
	return entry, result, nil
}

func (r *PlatformCompareGenerationRunner) settleOwnedRunning(run platformCompareRun) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.deps.rootContext), 2*time.Second)
	defer cancel()
	snapshot, err := r.deps.store.Get(ctx, run.userID, run.generationID)
	if err != nil || !validPlatformCompareSnapshotIdentity(snapshot, run) || snapshot.State != PlatformGenerationStateRunning {
		return
	}
	nowMillis := r.deps.now().UTC().UnixMilli()
	if !platformGenerationRunningLeaseAuthorized(snapshot, platformGenerationLeaseDigest(run.leaseToken), nowMillis) {
		return
	}
	_, _ = r.deps.store.FailRunningOwned(ctx, run.userID, run.generationID, run.leaseToken, "internal_error", nowMillis)
}

func validPlatformCompareClaim(claim PlatformGenerationClaimResult, run platformCompareRun, nowMillis int64) bool {
	if claim.Duplicate || !validPlatformGenerationLeaseToken(claim.LeaseToken) || !validPlatformCompareSnapshotIdentity(claim.Snapshot, run) || claim.Snapshot.State != PlatformGenerationStateRunning ||
		!platformGenerationRunningLeaseAuthorized(claim.Snapshot, platformGenerationLeaseDigest(claim.LeaseToken), nowMillis) {
		return false
	}
	for _, model := range run.models {
		state := claim.Snapshot.ModelStates[model]
		if state.State != PlatformGenerationStateRunning || state.Seq != 0 {
			return false
		}
	}
	return true
}

func validPlatformCompareDuplicate(snapshot PlatformGenerationSnapshot, run platformCompareRun) bool {
	return validPlatformCompareSnapshotIdentity(snapshot, run) && snapshot.State >= PlatformGenerationStateRunning && snapshot.State <= PlatformGenerationStateFailed
}

func validPlatformCompareSnapshotIdentity(snapshot PlatformGenerationSnapshot, run platformCompareRun) bool {
	if !validPlatformGenerationSnapshot(snapshot) || snapshot.GenerationID != run.generationID || snapshot.Mode != PlatformGenerationModeCompare || len(snapshot.Models) != len(run.models) || len(snapshot.ModelStates) != len(run.models) {
		return false
	}
	for index := range run.models {
		if snapshot.Models[index] != run.models[index] {
			return false
		}
	}
	return true
}

func (r *PlatformCompareGenerationRunner) prepare(input PlatformCompareGenerationInput) (platformCompareRun, int64, error) {
	validated, err := r.validateAndCopy(input)
	if err != nil {
		return platformCompareRun{}, 0, err
	}
	run, err := r.prepareValidated(validated)
	return run, validated.claimAt, err
}

func (r *PlatformCompareGenerationRunner) validateAndCopy(input PlatformCompareGenerationInput) (platformCompareValidatedInput, error) {
	if r == nil || !validPlatformCompareGenerationDeps(r.deps) {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationUnavailable
	}
	if input.Context == nil || input.User == nil || input.Write == nil {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationInvalid
	}
	if input.Context.Err() != nil {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationUnavailable
	}
	if !platformSSEV2CanonicalUUID(input.GenerationID) || len(input.Models) < 2 || len(input.Models) > platformSSEV2MaxModels {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationInvalid
	}
	modelsCopy := append([]string(nil), input.Models...)
	seenModels := make(map[string]struct{}, len(modelsCopy))
	for _, model := range modelsCopy {
		if !platformSSEV2ModelIdentifier(model) {
			return platformCompareValidatedInput{}, ErrPlatformCompareGenerationInvalid
		}
		if _, duplicate := seenModels[model]; duplicate {
			return platformCompareValidatedInput{}, ErrPlatformCompareGenerationInvalid
		}
		seenModels[model] = struct{}{}
	}
	params, err := clonePlatformSingleParams(input.Params)
	if err != nil || params.MaxTokens == nil || *params.MaxTokens <= 0 || len(params.WhiteLabelBody) == 0 || len(params.WhiteLabelBody) > whitelabel.MaxRequestBodyBytes {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationInvalid
	}
	claimAt := r.deps.now().UTC().UnixMilli()
	if !platformSSEV2SafeInteger(claimAt) || claimAt <= 0 || claimAt > platformSSEV2MaxSafeInteger-platformGenerationLeaseDuration.Milliseconds() {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationUnavailable
	}
	user := models.User{
		ID: input.User.ID, AuditFields: models.AuditFields{IsDeleted: input.User.IsDeleted}, Status: input.User.Status,
		PlanType: input.User.PlanType, DailyCallLimit: input.User.DailyCallLimit, DailyCallsUsed: input.User.DailyCallsUsed,
	}
	user.DailyCallsResetAt = cloneInt64Pointer(input.User.DailyCallsResetAt)
	if !platformCompareQuotaAvailable(&user, time.UnixMilli(claimAt).UTC(), len(modelsCopy)) {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationQuota
	}
	trimmed := trimPlatformSingleMessages(params.Messages, params.ContextWindow)
	userMessage, ok := platformSingleFinalUserMessage(trimmed)
	if !ok {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationInvalid
	}
	upstreamBody, err := platformCompareUpstreamBody(params.WhiteLabelBody)
	if err != nil || platformSingleExplicitUsageDisabled(upstreamBody) || whitelabel.ValidateRequest(upstreamBody, whitelabel.PlatformValidation) != nil {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationInvalid
	}
	params.WhiteLabelBody = append([]byte(nil), upstreamBody...)
	validated := platformCompareValidatedInput{
		ctx: input.Context, write: input.Write, user: user, generationID: input.GenerationID, requestID: input.RequestID,
		models: modelsCopy, params: params, trimmedMessages: trimmed, userMessage: userMessage,
		upstreamBody: append([]byte(nil), upstreamBody...), claimAt: claimAt,
	}
	if params.ConversationGUID != nil {
		guid, parseErr := parseConversationGUID(*params.ConversationGUID)
		if parseErr != nil {
			return platformCompareValidatedInput{}, ErrPlatformCompareGenerationInvalid
		}
		validated.conversationGUID = &guid
	}
	if input.Context.Err() != nil {
		return platformCompareValidatedInput{}, ErrPlatformCompareGenerationUnavailable
	}
	return validated, nil
}

func (r *PlatformCompareGenerationRunner) prepareValidated(input platformCompareValidatedInput) (platformCompareRun, error) {
	if input.ctx.Err() != nil {
		return platformCompareRun{}, ErrPlatformCompareGenerationUnavailable
	}
	payloads := make(map[string][]byte, len(input.models))
	for _, model := range input.models {
		payload, err := platformSingleStreamingPayload(input.upstreamBody, model, input.trimmedMessages, input.params.Temperature, input.params.MaxTokens)
		if err != nil || whitelabel.ValidateRequest(payload, whitelabel.PlatformValidation) != nil {
			return platformCompareRun{}, ErrPlatformCompareGenerationInvalid
		}
		payloads[model] = append([]byte(nil), payload...)
	}
	encoder, err := NewPlatformSSEV2Encoder(input.generationID, input.models)
	if err != nil {
		return platformCompareRun{}, ErrPlatformCompareGenerationInvalid
	}
	run := platformCompareRun{
		userID: input.user.ID, generationID: input.generationID, requestID: input.requestID,
		models: input.models, params: input.params, userMessage: input.userMessage, payloads: payloads, encoder: encoder,
	}
	if input.conversationGUID != nil {
		guid := *input.conversationGUID
		if loadErr := r.deps.loadConversation(input.ctx, r.deps.db, input.user.ID, guid); loadErr != nil {
			if errors.Is(loadErr, gorm.ErrRecordNotFound) {
				return platformCompareRun{}, ErrPlatformCompareGenerationInvalid
			}
			return platformCompareRun{}, ErrPlatformCompareGenerationUnavailable
		}
		run.conversationGUID = guid
		run.existingConversationGUID = &guid
	} else {
		guid := r.deps.newGUID()
		if guid <= 0 {
			return platformCompareRun{}, ErrPlatformCompareGenerationUnavailable
		}
		run.conversationGUID = guid
		run.reservedConversationGUID = &guid
	}
	if input.ctx.Err() != nil {
		return platformCompareRun{}, ErrPlatformCompareGenerationUnavailable
	}
	return run, nil
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
