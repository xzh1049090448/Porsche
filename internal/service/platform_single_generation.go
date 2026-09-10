package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

var (
	ErrPlatformSingleGenerationInvalid     = errors.New("invalid platform single generation")
	ErrPlatformSingleGenerationQuota       = errors.New("platform single generation quota unavailable")
	ErrPlatformSingleGenerationUnavailable = errors.New("platform single generation unavailable")
	ErrPlatformSingleGenerationUpstream    = errors.New("platform single generation upstream failure")
	ErrPlatformSingleGenerationOversize    = errors.New("platform single generation content too large")
)

type platformSingleGenerationStore interface {
	Claim(context.Context, PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error)
	RecordDeltaOwned(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error)
	MarkModelDoneOwned(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error)
	BeginCommitOwned(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error)
	Complete(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error)
	ReconcileComplete(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error)
	Get(context.Context, int64, string) (PlatformGenerationSnapshot, error)
	RenewLease(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error)
	FailRunningOwned(context.Context, int64, string, string, string, int64) (PlatformGenerationSnapshot, error)
	AcknowledgeCancelledOwned(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error)
}

type platformSingleGenerationPersistence interface {
	Finalize(context.Context, *gorm.DB, PlatformGenerationPersistenceInput) (PlatformGenerationReceiptSnapshot, error)
}

type platformSingleGenerationUpstream interface {
	Chat(context.Context, []byte) (*http.Response, *whitelabel.Error)
	ConsumeChatCompletionSSEContext(context.Context, io.Reader, string, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error
}

type platformSingleGenerationRegistry interface {
	Register(int64, string, context.CancelFunc) (string, error)
	Unregister(int64, string, string) bool
}

type PlatformSingleGenerationInput struct {
	Context      context.Context
	User         *models.User
	GenerationID string
	RequestID    string
	Params       ChatParams
	Write        func([]byte) error
}

type PlatformSingleGenerationRunResult struct {
	Started   bool
	Duplicate *PlatformGenerationSnapshot
}

type PlatformSingleGenerationRunnerAPI interface {
	Run(PlatformSingleGenerationInput) (PlatformSingleGenerationRunResult, error)
}

type platformSingleGenerationDeps struct {
	db               *gorm.DB
	store            platformSingleGenerationStore
	persistence      platformSingleGenerationPersistence
	registry         platformSingleGenerationRegistry
	upstream         platformSingleGenerationUpstream
	rootContext      context.Context
	now              func() time.Time
	newGUID          func() int64
	loadTotalTokens  func(context.Context, *gorm.DB, int64) (int64, error)
	loadConversation func(context.Context, *gorm.DB, int64, int64) error
	upstreamTimeout  time.Duration
	newTimer         platformSingleTimerFactory
	newRunnerContext func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

type platformSingleTimer interface {
	Chan() <-chan time.Time
	Stop()
}

type platformSingleTimerFactory func(time.Duration) platformSingleTimer

type platformSingleRealTimer struct{ timer *time.Timer }

func (t *platformSingleRealTimer) Chan() <-chan time.Time { return t.timer.C }
func (t *platformSingleRealTimer) Stop()                  { t.timer.Stop() }
func newPlatformSingleTimer(duration time.Duration) platformSingleTimer {
	return &platformSingleRealTimer{timer: time.NewTimer(duration)}
}

type PlatformSingleGenerationRunner struct {
	deps platformSingleGenerationDeps
}

func NewPlatformSingleGenerationRunner(
	db *gorm.DB,
	store *PlatformGenerationStore,
	persistenceService *PlatformGenerationPersistence,
	registry *PlatformGenerationCancellationRegistry,
	upstream *whitelabel.WhiteLabelService,
	rootContext context.Context,
	upstreamTimeout time.Duration,
) (*PlatformSingleGenerationRunner, error) {
	deps := platformSingleGenerationDeps{
		db: db, store: store, persistence: persistenceService, registry: registry,
		upstream: upstream, rootContext: rootContext, now: time.Now,
		newGUID: persistence.NextGUID, loadTotalTokens: loadPlatformSingleTotalTokens,
		loadConversation: loadPlatformSingleConversation,
		upstreamTimeout:  upstreamTimeout,
		newTimer:         newPlatformSingleTimer,
		newRunnerContext: context.WithTimeout,
	}
	if !validPlatformSingleGenerationDeps(deps) {
		return nil, ErrPlatformSingleGenerationUnavailable
	}
	return &PlatformSingleGenerationRunner{deps: deps}, nil
}

func validPlatformSingleGenerationDeps(deps platformSingleGenerationDeps) bool {
	return deps.db != nil && deps.store != nil && deps.persistence != nil && deps.registry != nil &&
		deps.upstream != nil && deps.rootContext != nil && deps.now != nil && deps.newGUID != nil &&
		deps.loadTotalTokens != nil && deps.loadConversation != nil && deps.upstreamTimeout > 0 && deps.newTimer != nil && deps.newRunnerContext != nil
}

type platformSingleRun struct {
	userID                   int64
	generationID             string
	model                    string
	userMessage              string
	conversationGUID         int64
	existingConversationGUID *int64
	reservedConversationGUID *int64
	leaseToken               string
	requestID                string
	payload                  []byte
	encoder                  *PlatformSSEV2Encoder
}

func (r *PlatformSingleGenerationRunner) nowMillis() int64 {
	return r.deps.now().UTC().UnixMilli()
}

func (r *PlatformSingleGenerationRunner) Run(input PlatformSingleGenerationInput) (PlatformSingleGenerationRunResult, error) {
	if r == nil || !validPlatformSingleGenerationDeps(r.deps) {
		return PlatformSingleGenerationRunResult{}, ErrPlatformSingleGenerationUnavailable
	}
	run, claimAt, err := r.prepare(input)
	if err != nil {
		return PlatformSingleGenerationRunResult{}, err
	}
	claim, claimErr := r.deps.store.Claim(input.Context, PlatformGenerationClaimInput{
		UserID: run.userID, GenerationID: run.generationID, Mode: PlatformGenerationModeSingle,
		Models: []string{run.model}, NowMillis: claimAt,
	})
	if claim.Duplicate {
		if (claimErr != nil && !errors.Is(claimErr, ErrPlatformGenerationConflict)) || !validPlatformSingleDuplicate(claim.Snapshot, run) {
			return PlatformSingleGenerationRunResult{}, ErrPlatformSingleGenerationUnavailable
		}
		duplicate := clonePlatformGeneration(claim.Snapshot)
		return PlatformSingleGenerationRunResult{Duplicate: &duplicate}, nil
	}
	if claimErr != nil || !validPlatformSingleClaim(claim, run, claimAt) {
		return PlatformSingleGenerationRunResult{}, ErrPlatformSingleGenerationUnavailable
	}
	run.leaseToken = claim.LeaseToken

	runnerCtx, cancelRunner := r.deps.newRunnerContext(r.deps.rootContext, r.deps.upstreamTimeout)
	defer cancelRunner()
	registrationToken, err := r.deps.registry.Register(run.userID, run.generationID, cancelRunner)
	if err != nil {
		r.settleRegistrationFailure(run)
		return PlatformSingleGenerationRunResult{}, ErrPlatformSingleGenerationUnavailable
	}
	defer r.deps.registry.Unregister(run.userID, run.generationID, registrationToken)

	output := platformSingleOutput{write: input.Write}
	meta := run.encoder.Meta(strconv.FormatInt(run.conversationGUID, 10))
	if meta == nil || run.encoder.Err() != nil {
		return PlatformSingleGenerationRunResult{}, ErrPlatformSingleGenerationUnavailable
	}
	result := PlatformSingleGenerationRunResult{Started: true}
	output.emit(meta)

	var mutationMu sync.Mutex
	body := &platformSingleResponseBody{}
	consumeResults := make(chan platformSingleConsumeResult, 1)
	go r.consume(runnerCtx, run, &output, &mutationMu, body, consumeResults)

	renewCtx, cancelRenew := context.WithCancel(context.WithoutCancel(runnerCtx))
	renewResults := make(chan platformSingleRenewalResult, 1)
	renewDone := make(chan struct{})
	go r.renew(renewCtx, run, &mutationMu, renewResults, renewDone)

	var consumed platformSingleConsumeResult
	var terminalCause error
	var renewalAuthority *PlatformGenerationSnapshot
	renewalAuthorityUnknown := false
	select {
	case consumed = <-consumeResults:
		terminalCause = consumed.cause
	case renewed := <-renewResults:
		terminalCause = renewed.err
		if terminalCause == nil {
			terminalCause = ErrPlatformSingleGenerationUnavailable
		}
		if validPlatformSingleSnapshotIdentity(renewed.snapshot, run) {
			snapshot := clonePlatformGeneration(renewed.snapshot)
			renewalAuthority = &snapshot
		} else {
			renewalAuthorityUnknown = true
		}
		cancelRunner()
		body.Close()
		consumed = <-consumeResults
	case <-runnerCtx.Done():
		terminalCause = runnerCtx.Err()
		cancelRenew()
		cancelRunner()
		body.Close()
		consumed = <-consumeResults
	}
	cancelRenew()
	<-renewDone
	body.Close()
	if renewalAuthority == nil && !renewalAuthorityUnknown {
		select {
		case renewed := <-renewResults:
			terminalCause = renewed.err
			if terminalCause == nil {
				terminalCause = ErrPlatformSingleGenerationUnavailable
			}
			if validPlatformSingleSnapshotIdentity(renewed.snapshot, run) {
				snapshot := clonePlatformGeneration(renewed.snapshot)
				renewalAuthority = &snapshot
			} else {
				renewalAuthorityUnknown = true
			}
		default:
		}
	}
	if terminalCause == nil {
		terminalCause = consumed.cause
	}
	if terminalCause != nil {
		if renewalAuthorityUnknown {
			r.emitFailure(run, &output, "internal_error", false)
			return result, ErrPlatformSingleGenerationUnavailable
		}
		if renewalAuthority != nil {
			return result, r.finishFailureAuthority(run, &output, &mutationMu, terminalCause, false, *renewalAuthority)
		}
		return result, r.finishFailure(run, &output, &mutationMu, terminalCause, false)
	}
	chunkState := consumed.state
	if runnerCtx.Err() != nil {
		return result, r.finishFailure(run, &output, &mutationMu, runnerCtx.Err(), false)
	}

	mutationMu.Lock()
	_, err = r.deps.store.MarkModelDoneOwned(runnerCtx, run.userID, run.generationID, run.leaseToken, run.model, chunkState.seq, r.nowMillis())
	if err == nil {
		_, err = r.deps.store.BeginCommitOwned(runnerCtx, run.userID, run.generationID, run.leaseToken, r.nowMillis())
	}
	mutationMu.Unlock()
	if err != nil {
		return result, r.finishFailure(run, &output, &mutationMu, ErrPlatformSingleGenerationUnavailable, false)
	}
	modelDone := run.encoder.ModelDone(run.model, chunkState.seq)
	if modelDone == nil || run.encoder.Err() != nil {
		return result, r.finishFailure(run, &output, &mutationMu, ErrPlatformSingleGenerationUnavailable, false)
	}
	output.emit(modelDone)

	persistenceInput := PlatformGenerationPersistenceInput{
		UserID: run.userID, GenerationID: run.generationID, Mode: PlatformGenerationModeSingle,
		Models: []string{run.model}, ConversationGUID: cloneInt64Pointer(run.existingConversationGUID),
		ReservedConversationGUID: cloneInt64Pointer(run.reservedConversationGUID), UserMessage: run.userMessage,
		Results: []PlatformGenerationPersistenceResult{{
			Model: run.model, State: PlatformGenerationStateCompleted, Content: chunkState.content.String(),
			Tokens: chunkState.totalTokens, Seq: chunkState.seq,
		}},
		NowMillis: r.nowMillis(),
	}
	receipt, err := r.deps.persistence.Finalize(runnerCtx, r.deps.db, persistenceInput)
	if err != nil || !platformSingleReceiptMatches(receipt, persistenceInput) {
		r.emitGlobalError(run, &output, "internal_error")
		return result, ErrPlatformSingleGenerationUnavailable
	}
	assistantGUIDs := map[string]string{run.model: receipt.Results[0].AssistantMessageGUID}
	mutationMu.Lock()
	completed, completeErr := r.deps.store.Complete(runnerCtx, run.userID, run.generationID, assistantGUIDs, r.nowMillis())
	if completeErr != nil || !validPlatformSingleCompleted(completed, run, assistantGUIDs) {
		reconcileCtx, reconcileCancel := r.terminalContext()
		completed, completeErr = r.deps.store.ReconcileComplete(reconcileCtx, run.userID, run.generationID, assistantGUIDs, r.nowMillis())
		reconcileCancel()
	}
	mutationMu.Unlock()
	if completeErr != nil || !validPlatformSingleCompleted(completed, run, assistantGUIDs) {
		r.emitGlobalError(run, &output, "internal_error")
		return result, ErrPlatformSingleGenerationUnavailable
	}
	totalTokensUsed, err := r.deps.loadTotalTokens(runnerCtx, r.deps.db, run.userID)
	if err != nil || !platformSSEV2SafeInteger(receipt.Results[0].Tokens) || !platformSSEV2SafeInteger(totalTokensUsed) {
		r.emitGlobalError(run, &output, "internal_error")
		return result, ErrPlatformSingleGenerationUnavailable
	}
	done := run.encoder.DoneSingle(strconv.FormatInt(receipt.ConversationGUID, 10), receipt.Results[0].Tokens, totalTokensUsed)
	if done == nil || run.encoder.Err() != nil {
		return result, ErrPlatformSingleGenerationUnavailable
	}
	output.emit(done)
	return result, nil
}

func (r *PlatformSingleGenerationRunner) settleRegistrationFailure(run platformSingleRun) {
	ctx, cancel := r.terminalContext()
	defer cancel()
	snapshot, err := r.deps.store.Get(ctx, run.userID, run.generationID)
	if err != nil || !validPlatformSingleSnapshotIdentity(snapshot, run) {
		return
	}
	switch snapshot.State {
	case PlatformGenerationStateCancelling:
		_, _ = r.deps.store.AcknowledgeCancelledOwned(ctx, run.userID, run.generationID, run.leaseToken, r.nowMillis())
	case PlatformGenerationStateRunning:
		if platformGenerationRunningLeaseAuthorized(snapshot, platformGenerationLeaseDigest(run.leaseToken), r.nowMillis()) {
			_, _ = r.deps.store.FailRunningOwned(ctx, run.userID, run.generationID, run.leaseToken, "internal_error", r.nowMillis())
		}
	}
}

type platformSingleConsumeResult struct {
	state platformSingleChunkState
	cause error
}

type platformSingleRenewalResult struct {
	snapshot PlatformGenerationSnapshot
	err      error
}

type platformSingleResponseBody struct {
	mu     sync.Mutex
	body   io.ReadCloser
	closed bool
}

func (b *platformSingleResponseBody) Set(body io.ReadCloser) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		_ = body.Close()
		return
	}
	b.body = body
}

func (b *platformSingleResponseBody) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	if b.body != nil {
		_ = b.body.Close()
	}
}

func (r *PlatformSingleGenerationRunner) consume(ctx context.Context, run platformSingleRun, output *platformSingleOutput, mutationMu *sync.Mutex, body *platformSingleResponseBody, results chan<- platformSingleConsumeResult) {
	state := platformSingleChunkState{}
	response, upstreamErr := r.deps.upstream.Chat(ctx, run.payload)
	if response != nil && response.Body != nil {
		body.Set(response.Body)
	}
	if upstreamErr != nil || response == nil || response.Body == nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		results <- platformSingleConsumeResult{state: state, cause: ErrPlatformSingleGenerationUpstream}
		return
	}
	var callbackCause error
	consumeErr := r.deps.upstream.ConsumeChatCompletionSSEContext(ctx, response.Body, run.model, func(chunk whitelabel.ChatCompletionChunk) error {
		delta, acceptErr := state.accept(chunk)
		if acceptErr != nil {
			callbackCause = acceptErr
			return acceptErr
		}
		if delta == "" {
			return nil
		}
		if !utf8.ValidString(delta) {
			callbackCause = ErrPlatformSingleGenerationUpstream
			return callbackCause
		}
		if len(delta) > platformGenerationMessageTextMaxBytes-state.content.Len() {
			callbackCause = ErrPlatformSingleGenerationOversize
			return callbackCause
		}
		nextSeq := state.seq + 1
		if !platformSSEV2SafeInteger(nextSeq) || nextSeq == 0 {
			callbackCause = ErrPlatformSingleGenerationOversize
			return callbackCause
		}
		mutationMu.Lock()
		_, storeErr := r.deps.store.RecordDeltaOwned(ctx, run.userID, run.generationID, run.leaseToken, run.model, nextSeq, r.nowMillis())
		mutationMu.Unlock()
		if storeErr != nil {
			callbackCause = ErrPlatformSingleGenerationUnavailable
			return callbackCause
		}
		frame := run.encoder.Delta(run.model, nextSeq, delta)
		if frame == nil || run.encoder.Err() != nil {
			callbackCause = ErrPlatformSingleGenerationUnavailable
			return callbackCause
		}
		state.seq = nextSeq
		_, _ = state.content.WriteString(delta)
		output.emit(frame)
		return nil
	})
	if callbackCause != nil {
		results <- platformSingleConsumeResult{state: state, cause: callbackCause}
		return
	}
	if consumeErr != nil || !state.complete() {
		results <- platformSingleConsumeResult{state: state, cause: ErrPlatformSingleGenerationUpstream}
		return
	}
	results <- platformSingleConsumeResult{state: state}
}

func (r *PlatformSingleGenerationRunner) renew(ctx context.Context, run platformSingleRun, mutationMu *sync.Mutex, results chan<- platformSingleRenewalResult, done chan<- struct{}) {
	timer := r.deps.newTimer(10 * time.Second)
	defer func() { timer.Stop() }()
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.Chan():
			now := r.nowMillis()
			mutationMu.Lock()
			snapshot, err := r.deps.store.RenewLease(ctx, run.userID, run.generationID, run.leaseToken, now)
			mutationMu.Unlock()
			if err != nil || !validPlatformSingleRenewal(snapshot, run, now) {
				if err == nil {
					err = ErrPlatformSingleGenerationUnavailable
				}
				results <- platformSingleRenewalResult{snapshot: snapshot, err: err}
				return
			}
			timer.Stop()
			timer = r.deps.newTimer(10 * time.Second)
		}
	}
}

func (r *PlatformSingleGenerationRunner) terminalContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.deps.rootContext), 2*time.Second)
}

func (r *PlatformSingleGenerationRunner) finishFailure(run platformSingleRun, output *platformSingleOutput, mutationMu *sync.Mutex, cause error, afterModelDone bool) error {
	ctx, cancel := r.terminalContext()
	defer cancel()
	mutationMu.Lock()
	snapshot, err := r.deps.store.Get(ctx, run.userID, run.generationID)
	mutationMu.Unlock()
	if err != nil || !validPlatformSingleSnapshotIdentity(snapshot, run) {
		r.emitFailure(run, output, "internal_error", afterModelDone)
		return ErrPlatformSingleGenerationUnavailable
	}
	return r.finishFailureAuthority(run, output, mutationMu, cause, afterModelDone, snapshot)
}

func (r *PlatformSingleGenerationRunner) finishFailureAuthority(run platformSingleRun, output *platformSingleOutput, mutationMu *sync.Mutex, cause error, afterModelDone bool, snapshot PlatformGenerationSnapshot) error {
	ctx, cancel := r.terminalContext()
	defer cancel()
	now := r.nowMillis()
	switch snapshot.State {
	case PlatformGenerationStateCancelling:
		mutationMu.Lock()
		cancelled, ackErr := r.deps.store.AcknowledgeCancelledOwned(ctx, run.userID, run.generationID, run.leaseToken, now)
		mutationMu.Unlock()
		if ackErr == nil && validPlatformSingleTerminal(cancelled, run, PlatformGenerationStateCancelled, "cancelled") {
			r.emitFailure(run, output, "cancelled", afterModelDone)
			return ErrPlatformSingleGenerationUpstream
		}
		r.emitFailure(run, output, "internal_error", afterModelDone)
		return ErrPlatformSingleGenerationUnavailable
	case PlatformGenerationStateCancelled:
		r.emitFailure(run, output, "cancelled", afterModelDone)
		return ErrPlatformSingleGenerationUpstream
	case PlatformGenerationStateFailed:
		code := snapshot.ErrorCode
		if !platformGenerationStableCode(code) {
			code = "internal_error"
		}
		r.emitFailure(run, output, code, afterModelDone)
		return platformSingleRunError(cause)
	case PlatformGenerationStateCommitting, PlatformGenerationStateCompleted:
		r.emitGlobalError(run, output, "internal_error")
		return ErrPlatformSingleGenerationUnavailable
	case PlatformGenerationStateRunning:
		if !platformGenerationRunningLeaseAuthorized(snapshot, platformGenerationLeaseDigest(run.leaseToken), now) {
			r.emitFailure(run, output, "internal_error", afterModelDone)
			return ErrPlatformSingleGenerationUnavailable
		}
		code := platformSingleStableCode(cause, r.deps.rootContext.Err())
		mutationMu.Lock()
		failed, failErr := r.deps.store.FailRunningOwned(ctx, run.userID, run.generationID, run.leaseToken, code, now)
		mutationMu.Unlock()
		if failErr != nil || !validPlatformSingleTerminal(failed, run, PlatformGenerationStateFailed, code) {
			r.emitFailure(run, output, "internal_error", afterModelDone)
			return ErrPlatformSingleGenerationUnavailable
		}
		r.emitFailure(run, output, code, afterModelDone)
		return platformSingleRunError(cause)
	default:
		r.emitFailure(run, output, "internal_error", afterModelDone)
		return ErrPlatformSingleGenerationUnavailable
	}
}

func (r *PlatformSingleGenerationRunner) emitFailure(run platformSingleRun, output *platformSingleOutput, code string, afterModelDone bool) {
	if !afterModelDone {
		output.emit(run.encoder.ModelError(run.model, code, run.requestID))
	}
	r.emitGlobalError(run, output, code)
}

func (r *PlatformSingleGenerationRunner) emitGlobalError(run platformSingleRun, output *platformSingleOutput, code string) {
	frame := run.encoder.Error(code, run.requestID)
	if frame != nil && run.encoder.Err() == nil {
		output.emit(frame)
	}
}

func platformSingleStableCode(cause error, rootCtxErr error) string {
	switch {
	case rootCtxErr != nil:
		return "internal_error"
	case errors.Is(cause, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(cause, ErrPlatformSingleGenerationOversize):
		return "upstream_error"
	case errors.Is(cause, ErrPlatformSingleGenerationUnavailable):
		return "internal_error"
	default:
		return "gateway_upstream_error"
	}
}

func platformSingleRunError(cause error) error {
	switch {
	case errors.Is(cause, ErrPlatformSingleGenerationOversize):
		return ErrPlatformSingleGenerationOversize
	case errors.Is(cause, ErrPlatformSingleGenerationUnavailable):
		return ErrPlatformSingleGenerationUnavailable
	default:
		return ErrPlatformSingleGenerationUpstream
	}
}

func validPlatformSingleRenewal(snapshot PlatformGenerationSnapshot, run platformSingleRun, nowMillis int64) bool {
	if !validPlatformSingleSnapshotIdentity(snapshot, run) || snapshot.State != PlatformGenerationStateRunning ||
		snapshot.UpdatedAtMillis != nowMillis || snapshot.LeaseUntilMillis != nowMillis+platformGenerationLeaseDuration.Milliseconds() ||
		snapshot.LeaseOwnerSHA256 != platformGenerationLeaseDigest(run.leaseToken) {
		return false
	}
	model, ok := snapshot.ModelStates[run.model]
	return ok && model.State == PlatformGenerationStateRunning && model.Seq >= 0
}

func validPlatformSingleTerminal(snapshot PlatformGenerationSnapshot, run platformSingleRun, state PlatformGenerationState, code string) bool {
	expectedSnapshotCode := code
	expectedModelCode := code
	if state == PlatformGenerationStateCancelled {
		expectedSnapshotCode = ""
		expectedModelCode = ""
	}
	if !validPlatformSingleSnapshotIdentity(snapshot, run) || snapshot.State != state || snapshot.LeaseOwnerSHA256 != "" || snapshot.LeaseUntilMillis != 0 || snapshot.ErrorCode != expectedSnapshotCode {
		return false
	}
	model, ok := snapshot.ModelStates[run.model]
	return ok && model.State == state && model.ErrorCode == expectedModelCode
}

func validPlatformSingleCompleted(snapshot PlatformGenerationSnapshot, run platformSingleRun, guids map[string]string) bool {
	if !validPlatformSingleSnapshotIdentity(snapshot, run) || snapshot.State != PlatformGenerationStateCompleted || snapshot.LeaseOwnerSHA256 != "" || snapshot.LeaseUntilMillis != 0 || len(guids) != 1 {
		return false
	}
	model, ok := snapshot.ModelStates[run.model]
	return ok && model.State == PlatformGenerationStateCompleted && model.AssistantMessageGUID == guids[run.model]
}

func (r *PlatformSingleGenerationRunner) prepare(input PlatformSingleGenerationInput) (platformSingleRun, int64, error) {
	if input.Context == nil || input.User == nil || input.Write == nil {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationInvalid
	}
	if input.Context.Err() != nil {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationUnavailable
	}
	user := models.User{
		ID: input.User.ID, AuditFields: models.AuditFields{IsDeleted: input.User.IsDeleted},
		Status: input.User.Status, PlanType: input.User.PlanType,
		DailyCallLimit: input.User.DailyCallLimit, DailyCallsUsed: input.User.DailyCallsUsed,
	}
	user.DailyCallsResetAt = cloneInt64Pointer(input.User.DailyCallsResetAt)
	params, err := clonePlatformSingleParams(input.Params)
	if err != nil || !platformSSEV2CanonicalUUID(input.GenerationID) || !platformSSEV2ModelIdentifier(params.Model) {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationInvalid
	}
	claimAt := r.nowMillis()
	if !platformSSEV2SafeInteger(claimAt) || claimAt <= 0 || claimAt > platformSSEV2MaxSafeInteger-platformGenerationLeaseDuration.Milliseconds() {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationUnavailable
	}
	if !platformSingleQuotaAvailable(&user, time.UnixMilli(claimAt).UTC()) {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationQuota
	}
	trimmed := trimPlatformSingleMessages(params.Messages, params.ContextWindow)
	userMessage, ok := platformSingleFinalUserMessage(trimmed)
	if !ok {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationInvalid
	}
	if platformSingleExplicitUsageDisabled(params.WhiteLabelBody) {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationInvalid
	}
	payload, err := platformSingleStreamingPayload(params.WhiteLabelBody, params.Model, trimmed, params.Temperature, params.MaxTokens)
	if err != nil {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationInvalid
	}
	encoder, err := NewPlatformSSEV2Encoder(input.GenerationID, []string{params.Model})
	if err != nil {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationInvalid
	}
	run := platformSingleRun{userID: user.ID, generationID: input.GenerationID, model: params.Model, userMessage: userMessage, requestID: input.RequestID, payload: payload, encoder: encoder}
	if params.ConversationGUID != nil {
		guid, parseErr := parseConversationGUID(*params.ConversationGUID)
		if parseErr != nil {
			return platformSingleRun{}, 0, ErrPlatformSingleGenerationInvalid
		}
		if loadErr := r.deps.loadConversation(input.Context, r.deps.db, user.ID, guid); loadErr != nil {
			if errors.Is(loadErr, gorm.ErrRecordNotFound) {
				return platformSingleRun{}, 0, ErrPlatformSingleGenerationInvalid
			}
			return platformSingleRun{}, 0, ErrPlatformSingleGenerationUnavailable
		}
		run.conversationGUID = guid
		run.existingConversationGUID = &guid
	} else {
		guid := r.deps.newGUID()
		if guid <= 0 {
			return platformSingleRun{}, 0, ErrPlatformSingleGenerationUnavailable
		}
		run.conversationGUID = guid
		run.reservedConversationGUID = &guid
	}
	if input.Context.Err() != nil {
		return platformSingleRun{}, 0, ErrPlatformSingleGenerationUnavailable
	}
	return run, claimAt, nil
}

func clonePlatformSingleParams(params ChatParams) (ChatParams, error) {
	messages := make([]map[string]interface{}, len(params.Messages))
	for index, message := range params.Messages {
		cloned, ok := clonePlatformSingleJSONValue(message)
		if !ok {
			return ChatParams{}, ErrPlatformSingleGenerationInvalid
		}
		messages[index], ok = cloned.(map[string]interface{})
		if !ok {
			return ChatParams{}, ErrPlatformSingleGenerationInvalid
		}
	}
	copyParams := ChatParams{Model: params.Model, Messages: messages, WhiteLabelBody: append([]byte(nil), params.WhiteLabelBody...)}
	if params.ConversationGUID != nil {
		value := *params.ConversationGUID
		copyParams.ConversationGUID = &value
	}
	if params.Temperature != nil {
		value := *params.Temperature
		copyParams.Temperature = &value
	}
	if params.MaxTokens != nil {
		value := *params.MaxTokens
		copyParams.MaxTokens = &value
	}
	if params.ContextWindow != nil {
		value := *params.ContextWindow
		copyParams.ContextWindow = &value
	}
	return copyParams, nil
}

func clonePlatformSingleJSONValue(value interface{}) (interface{}, bool) {
	switch typed := value.(type) {
	case nil, bool, float64, string:
		return typed, true
	case []interface{}:
		copyValue := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned, ok := clonePlatformSingleJSONValue(item)
			if !ok {
				return nil, false
			}
			copyValue[index] = cloned
		}
		return copyValue, true
	case map[string]interface{}:
		copyValue := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			cloned, ok := clonePlatformSingleJSONValue(item)
			if !ok {
				return nil, false
			}
			copyValue[key] = cloned
		}
		return copyValue, true
	default:
		return nil, false
	}
}

func trimPlatformSingleMessages(messages []map[string]interface{}, contextWindow *int) []map[string]interface{} {
	if contextWindow == nil || *contextWindow <= 0 || *contextWindow > len(messages)/2 {
		return messages
	}
	count := *contextWindow * 2
	if len(messages) <= count {
		return messages
	}
	return messages[len(messages)-count:]
}

func platformSingleFinalUserMessage(messages []map[string]interface{}) (string, bool) {
	if len(messages) == 0 {
		return "", false
	}
	last := messages[len(messages)-1]
	role, roleOK := last["role"].(string)
	content, contentOK := last["content"].(string)
	return content, roleOK && role == "user" && contentOK && content != "" && utf8.ValidString(content) && len(content) <= platformGenerationMessageTextMaxBytes
}

func platformSingleExplicitUsageDisabled(validated []byte) bool {
	if len(validated) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(validated, &fields) != nil || fields == nil {
		return true
	}
	raw, found := fields["stream_options"]
	if !found {
		return false
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(raw, &options) != nil || options == nil {
		return true
	}
	includeRaw, found := options["include_usage"]
	if !found {
		return false
	}
	var include bool
	return json.Unmarshal(includeRaw, &include) != nil || !include
}

func platformSingleStreamingPayload(validated []byte, model string, messages []map[string]interface{}, temperature *float64, maxTokens *int) ([]byte, error) {
	body := whitelabel.ChatCompletionRequest{Model: model, Messages: toGatewayMessages(messages), Temperature: temperature, MaxTokens: maxTokens, Stream: true}
	payload, err := whiteLabelPayload(validated, body)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, ErrPlatformSingleGenerationInvalid
	}
	fields["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	return json.Marshal(fields)
}

func platformSingleQuotaAvailable(user *models.User, now time.Time) bool {
	if user == nil || user.ID <= 0 || user.Status != models.UserStatusActive || user.IsDeleted != 0 {
		return false
	}
	if user.PlanType == models.PlanProfessional || user.PlanType == models.PlanEnterprise {
		return true
	}
	used := user.DailyCallsUsed
	if user.DailyCallsResetAt == nil || time.UnixMilli(*user.DailyCallsResetAt).UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		used = 0
	}
	return user.DailyCallLimit >= 0 && used >= 0 && used < user.DailyCallLimit
}

type platformSingleOutput struct {
	write    func([]byte) error
	detached bool
}

func (o *platformSingleOutput) emit(frame []byte) bool {
	if o.detached || len(frame) == 0 {
		return false
	}
	if err := o.write(frame); err != nil {
		o.detached = true
		return false
	}
	return true
}

type platformSingleChunkState struct {
	content     strings.Builder
	seq         int64
	modelEnded  bool
	usageSeen   bool
	totalTokens int64
}

func (s *platformSingleChunkState) accept(chunk whitelabel.ChatCompletionChunk) (string, error) {
	if chunk.Usage != nil {
		if s.usageSeen || len(chunk.Choices) != 0 || chunk.Usage.TotalTokens < 0 || chunk.Usage.TotalTokens > math.MaxInt32 {
			return "", ErrPlatformSingleGenerationUpstream
		}
		s.usageSeen = true
		s.totalTokens = int64(chunk.Usage.TotalTokens)
		return "", nil
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 || s.modelEnded {
		return "", ErrPlatformSingleGenerationUpstream
	}
	choice := chunk.Choices[0]
	if choice.Delta.Refusal != nil || len(choice.Delta.ToolCalls) != 0 {
		return "", ErrPlatformSingleGenerationUpstream
	}
	delta := ""
	if choice.Delta.Content != nil {
		delta = *choice.Delta.Content
	}
	if choice.FinishReason != nil {
		s.modelEnded = true
	}
	return delta, nil
}
func (s *platformSingleChunkState) complete() bool {
	return s.modelEnded && s.usageSeen && s.content.Len() > 0
}

func validPlatformSingleClaim(claim PlatformGenerationClaimResult, run platformSingleRun, nowMillis int64) bool {
	if claim.Duplicate || !validPlatformGenerationLeaseToken(claim.LeaseToken) || !validPlatformSingleSnapshotIdentity(claim.Snapshot, run) {
		return false
	}
	modelState, ok := claim.Snapshot.ModelStates[run.model]
	return ok && claim.Snapshot.State == PlatformGenerationStateRunning && modelState.State == PlatformGenerationStateRunning && modelState.Seq == 0 &&
		platformGenerationRunningLeaseAuthorized(claim.Snapshot, platformGenerationLeaseDigest(claim.LeaseToken), nowMillis)
}

func validPlatformSingleDuplicate(snapshot PlatformGenerationSnapshot, run platformSingleRun) bool {
	if !validPlatformSingleSnapshotIdentity(snapshot, run) {
		return false
	}
	_, ok := snapshot.ModelStates[run.model]
	return ok && snapshot.State >= PlatformGenerationStateRunning && snapshot.State <= PlatformGenerationStateFailed
}

func validPlatformSingleSnapshotIdentity(snapshot PlatformGenerationSnapshot, run platformSingleRun) bool {
	return validPlatformGenerationSnapshot(snapshot) && snapshot.GenerationID == run.generationID && snapshot.Mode == PlatformGenerationModeSingle && len(snapshot.Models) == 1 && snapshot.Models[0] == run.model && len(snapshot.ModelStates) == 1
}

func platformSingleReceiptMatches(receipt PlatformGenerationReceiptSnapshot, input PlatformGenerationPersistenceInput) bool {
	if len(input.Results) != 1 || receipt.UserID != input.UserID || receipt.GenerationID != input.GenerationID || receipt.Mode != PlatformGenerationModeSingle || receipt.ConversationGUID <= 0 ||
		receipt.RequestedExistingConversation != (input.ConversationGUID != nil) || receipt.UserMessage != input.UserMessage || receipt.SuccessfulModelCount != 1 ||
		receipt.DailyCallsCharged != 1 || receipt.TotalTokens != input.Results[0].Tokens || receipt.CommittedAtMillis <= 0 || len(receipt.Results) != 1 {
		return false
	}
	var expectedGUID int64
	if input.ConversationGUID != nil {
		expectedGUID = *input.ConversationGUID
	} else if input.ReservedConversationGUID != nil {
		expectedGUID = *input.ReservedConversationGUID
	} else {
		return false
	}
	result, expected := receipt.Results[0], input.Results[0]
	return receipt.ConversationGUID == expectedGUID && result.Model == expected.Model && result.State == PlatformGenerationStateCompleted &&
		platformGenerationMessageGUID(result.AssistantMessageGUID) && result.Content == expected.Content && result.Tokens == expected.Tokens && result.ErrorCode == ""
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func loadPlatformSingleConversation(ctx context.Context, db *gorm.DB, userID, guid int64) error {
	if ctx == nil || db == nil || userID <= 0 || guid <= 0 {
		return ErrPlatformSingleGenerationUnavailable
	}
	var conversation models.Conversation
	return db.WithContext(ctx).Select("id").Where("guid = ? AND user_id = ? AND is_deleted = 0", guid, userID).First(&conversation).Error
}

func loadPlatformSingleTotalTokens(ctx context.Context, db *gorm.DB, userID int64) (int64, error) {
	if ctx == nil || db == nil || userID <= 0 {
		return 0, ErrPlatformSingleGenerationUnavailable
	}
	var user models.User
	if err := db.WithContext(ctx).Select("total_tokens_used").Where("id = ? AND status = ? AND is_deleted = 0", userID, models.UserStatusActive).First(&user).Error; err != nil || user.TotalTokensUsed < 0 {
		return 0, ErrPlatformSingleGenerationUnavailable
	}
	return user.TotalTokensUsed, nil
}
