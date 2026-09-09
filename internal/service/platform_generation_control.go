package service

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

const (
	platformGenerationControlDefaultCancelBudget = 3 * time.Second
	platformGenerationControlDefaultCancelPoll   = 50 * time.Millisecond
)

var (
	ErrPlatformGenerationControlInvalid     = errors.New("invalid platform generation control request")
	ErrPlatformGenerationControlNotFound    = errors.New("platform generation control not found")
	ErrPlatformGenerationControlUnavailable = errors.New("platform generation control unavailable")
)

type PlatformGenerationResultView struct {
	Model                string `json:"model"`
	Status               string `json:"status"`
	AssistantMessageGUID string `json:"assistant_message_guid,omitempty"`
	Content              string `json:"content,omitempty"`
	Tokens               *int64 `json:"tokens,omitempty"`
	Code                 string `json:"code,omitempty"`
}

type PlatformGenerationView struct {
	GenerationID     string                         `json:"generation_id"`
	Status           string                         `json:"status"`
	Mode             *string                        `json:"mode"`
	ConversationGUID *string                        `json:"conversation_guid"`
	Result           *PlatformGenerationResultView  `json:"result,omitempty"`
	Results          []PlatformGenerationResultView `json:"results,omitempty"`
	TotalTokensUsed  *int64                         `json:"total_tokens_used,omitempty"`
	Code             string                         `json:"code,omitempty"`
	RequestID        string                         `json:"request_id,omitempty"`
}

type PlatformGenerationController interface {
	Get(context.Context, int64, string) (PlatformGenerationView, error)
	Cancel(context.Context, int64, string) (PlatformGenerationView, bool, error)
}

type platformGenerationControlDeps struct {
	get                     func(context.Context, int64, string) (PlatformGenerationSnapshot, error)
	cancelOrCreate          func(context.Context, int64, string, int64) (PlatformGenerationCancelDecision, error)
	failExpiredRunning      func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error)
	convergeStaleCancelling func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error)
	reconcile               func(context.Context, int64, string, int64) (PlatformGenerationSnapshot, error)
	loadReceipt             func(context.Context, int64, string) (PlatformGenerationReceiptSnapshot, error)
	loadTotalTokens         func(context.Context, int64) (int64, error)
}

type PlatformGenerationControl struct {
	deps          platformGenerationControlDeps
	cancellations *PlatformGenerationCancellationRegistry
	now           func() time.Time
	cancelBudget  time.Duration
	cancelPoll    time.Duration
}

var _ PlatformGenerationController = (*PlatformGenerationControl)(nil)

func NewPlatformGenerationControl(db *gorm.DB, store *PlatformGenerationStore, cancellations *PlatformGenerationCancellationRegistry) (*PlatformGenerationControl, error) {
	if db == nil || db.Config == nil || store == nil || store.client == nil || cancellations == nil {
		return nil, ErrPlatformGenerationControlUnavailable
	}
	return newPlatformGenerationControl(platformGenerationControlDeps{
		get:                     store.Get,
		cancelOrCreate:          store.CancelOrCreate,
		failExpiredRunning:      store.FailExpiredRunning,
		convergeStaleCancelling: store.ConvergeStaleCancelling,
		reconcile: func(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error) {
			return ReconcilePlatformGeneration(ctx, db, store, userID, generationID, nowMillis)
		},
		loadReceipt: func(ctx context.Context, userID int64, generationID string) (PlatformGenerationReceiptSnapshot, error) {
			return LoadPlatformGenerationReceipt(ctx, db, userID, generationID)
		},
		loadTotalTokens: func(ctx context.Context, userID int64) (int64, error) {
			return loadPlatformGenerationControlTotalTokens(ctx, db, userID)
		},
	}, cancellations)
}

func newPlatformGenerationControl(deps platformGenerationControlDeps, cancellations *PlatformGenerationCancellationRegistry) (*PlatformGenerationControl, error) {
	if deps.get == nil || deps.cancelOrCreate == nil || deps.failExpiredRunning == nil ||
		deps.convergeStaleCancelling == nil || deps.reconcile == nil || deps.loadReceipt == nil ||
		deps.loadTotalTokens == nil || cancellations == nil {
		return nil, ErrPlatformGenerationControlUnavailable
	}
	return &PlatformGenerationControl{
		deps: deps, cancellations: cancellations, now: time.Now,
		cancelBudget: platformGenerationControlDefaultCancelBudget,
		cancelPoll:   platformGenerationControlDefaultCancelPoll,
	}, nil
}

func loadPlatformGenerationControlTotalTokens(ctx context.Context, db *gorm.DB, userID int64) (int64, error) {
	if ctx == nil || db == nil || userID <= 0 {
		return 0, ErrPlatformGenerationControlUnavailable
	}
	var user struct {
		TotalTokensUsed int64
	}
	err := db.WithContext(ctx).Model(&models.User{}).
		Select("total_tokens_used").
		Where("id = ? AND is_deleted = 0 AND status = ?", userID, models.UserStatusActive).
		First(&user).Error
	if err != nil || user.TotalTokensUsed < 0 {
		return 0, ErrPlatformGenerationControlUnavailable
	}
	return user.TotalTokensUsed, nil
}

func (c *PlatformGenerationControl) Get(ctx context.Context, userID int64, generationID string) (PlatformGenerationView, error) {
	if !validPlatformGenerationControlRequest(c, ctx, userID, generationID) {
		return PlatformGenerationView{}, ErrPlatformGenerationControlInvalid
	}
	nowMillis, ok := c.controlNowMillis()
	if !ok {
		return PlatformGenerationView{}, ErrPlatformGenerationControlUnavailable
	}
	snapshot, err := c.deps.get(ctx, userID, generationID)
	if err != nil {
		return PlatformGenerationView{}, platformGenerationControlReadError(err)
	}
	snapshot, err = c.converge(ctx, userID, generationID, nowMillis, snapshot)
	if err != nil {
		return PlatformGenerationView{}, err
	}
	return c.project(ctx, userID, generationID, snapshot)
}

func (c *PlatformGenerationControl) converge(ctx context.Context, userID int64, generationID string, nowMillis int64, snapshot PlatformGenerationSnapshot) (PlatformGenerationSnapshot, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if !validPlatformGenerationControlSnapshot(snapshot, generationID) || nowMillis < snapshot.UpdatedAtMillis {
			return PlatformGenerationSnapshot{}, ErrPlatformGenerationControlUnavailable
		}
		var next PlatformGenerationSnapshot
		var err error
		switch snapshot.State {
		case PlatformGenerationStateRunning:
			if snapshot.LeaseUntilMillis != 0 && nowMillis < snapshot.LeaseUntilMillis {
				return snapshot, nil
			}
			next, err = c.deps.failExpiredRunning(ctx, userID, generationID, nowMillis)
		case PlatformGenerationStateCancelling:
			if nowMillis-snapshot.UpdatedAtMillis < platformGenerationConvergenceWindow.Milliseconds() {
				return snapshot, nil
			}
			next, err = c.deps.convergeStaleCancelling(ctx, userID, generationID, nowMillis)
		case PlatformGenerationStateCommitting:
			next, err = c.deps.reconcile(ctx, userID, generationID, nowMillis)
			if errors.Is(err, ErrPlatformGenerationConflict) || errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
				if attempt == 1 && validPlatformGenerationControlSnapshot(next, generationID) && next.State == PlatformGenerationStateCommitting && nowMillis-next.UpdatedAtMillis < platformGenerationConvergenceWindow.Milliseconds() {
					return next, nil
				}
			}
		default:
			return snapshot, nil
		}
		if err == nil {
			if !validPlatformGenerationControlSnapshot(next, generationID) {
				return PlatformGenerationSnapshot{}, ErrPlatformGenerationControlUnavailable
			}
			return next, nil
		}
		if !errors.Is(err, ErrPlatformGenerationConflict) && !errors.Is(err, ErrPlatformGenerationPersistenceConflict) {
			return PlatformGenerationSnapshot{}, ErrPlatformGenerationControlUnavailable
		}
		if attempt == 1 {
			return PlatformGenerationSnapshot{}, ErrPlatformGenerationControlUnavailable
		}
		snapshot, err = c.deps.get(ctx, userID, generationID)
		if err != nil {
			return PlatformGenerationSnapshot{}, platformGenerationControlReadError(err)
		}
	}
	return PlatformGenerationSnapshot{}, ErrPlatformGenerationControlUnavailable
}

func (c *PlatformGenerationControl) Cancel(ctx context.Context, userID int64, generationID string) (PlatformGenerationView, bool, error) {
	if !validPlatformGenerationControlRequest(c, ctx, userID, generationID) {
		return PlatformGenerationView{}, false, ErrPlatformGenerationControlInvalid
	}
	if c.cancelBudget <= 0 || c.cancelPoll <= 0 {
		return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
	}
	return c.cancelUntil(ctx, userID, generationID, time.Now().Add(c.cancelBudget), false)
}

func (c *PlatformGenerationControl) cancelUntil(ctx context.Context, userID int64, generationID string, deadline time.Time, callbackInvoked bool) (PlatformGenerationView, bool, error) {
	var latest PlatformGenerationSnapshot
	for {
		if err := ctx.Err(); err != nil {
			return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
		}
		nowMillis, ok := c.controlNowMillis()
		if !ok {
			return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
		}
		decision, err := c.deps.cancelOrCreate(ctx, userID, generationID, nowMillis)
		if errors.Is(err, ErrPlatformGenerationNotFound) {
			if !platformGenerationControlWait(ctx, c.cancelPoll, deadline) {
				return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
			}
			continue
		}
		if !validPlatformGenerationControlSnapshot(decision.Snapshot, generationID) {
			return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
		}
		latest = decision.Snapshot
		if err != nil && !errors.Is(err, ErrPlatformGenerationConflict) {
			return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
		}
		if decision.Transitioned && !callbackInvoked {
			c.cancellations.Cancel(userID, generationID)
			callbackInvoked = true
		}
		if decision.CreatedTombstone || platformGenerationControlTerminal(latest.State) {
			view, projectErr := c.project(ctx, userID, generationID, latest)
			return view, false, projectErr
		}
		if latest.State == PlatformGenerationStateCancelling || latest.State == PlatformGenerationStateCommitting {
			break
		}
		if latest.State != PlatformGenerationStateRunning || !time.Now().Before(deadline) {
			return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
		}
		if !platformGenerationControlWait(ctx, c.cancelPoll, deadline) {
			return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
		}
	}

	for {
		if ctx.Err() != nil {
			return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			view, err := c.project(ctx, userID, generationID, latest)
			if err != nil {
				return PlatformGenerationView{}, false, err
			}
			if latest.State != PlatformGenerationStateCancelling && latest.State != PlatformGenerationStateCommitting {
				return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
			}
			return view, true, nil
		}
		wait := c.cancelPoll
		if remaining < wait {
			wait = remaining
		}
		if !platformGenerationControlWait(ctx, wait, deadline) {
			if ctx.Err() != nil {
				return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
			}
			continue
		}

		view, err := c.Get(ctx, userID, generationID)
		if err != nil {
			if err == ErrPlatformGenerationControlNotFound && time.Now().Before(deadline) {
				return c.cancelUntil(ctx, userID, generationID, deadline, callbackInvoked)
			}
			return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
		}
		switch view.Status {
		case "cancelled", "completed", "failed":
			return view, false, nil
		case "cancelling", "committing":
			latest.State = platformGenerationControlState(view.Status)
			latest.Mode = platformGenerationControlMode(view.Mode)
		case "running":
			if !time.Now().Before(deadline) {
				return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
			}
			nowMillis, ok := c.controlNowMillis()
			if !ok {
				return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
			}
			decision, cancelErr := c.deps.cancelOrCreate(ctx, userID, generationID, nowMillis)
			if cancelErr != nil && !errors.Is(cancelErr, ErrPlatformGenerationConflict) {
				return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
			}
			if !validPlatformGenerationControlSnapshot(decision.Snapshot, generationID) {
				return PlatformGenerationView{}, false, ErrPlatformGenerationControlUnavailable
			}
			latest = decision.Snapshot
			if decision.Transitioned && !callbackInvoked {
				c.cancellations.Cancel(userID, generationID)
				callbackInvoked = true
			}
			if platformGenerationControlTerminal(latest.State) {
				projected, projectErr := c.project(ctx, userID, generationID, latest)
				return projected, false, projectErr
			}
		}
	}
}

func platformGenerationControlWait(ctx context.Context, wait time.Duration, deadline time.Time) bool {
	if wait <= 0 || !time.Now().Before(deadline) {
		return false
	}
	if remaining := time.Until(deadline); remaining < wait {
		wait = remaining
	}
	timer := time.NewTimer(wait)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *PlatformGenerationControl) project(ctx context.Context, userID int64, generationID string, snapshot PlatformGenerationSnapshot) (PlatformGenerationView, error) {
	if !validPlatformGenerationControlSnapshot(snapshot, generationID) {
		return PlatformGenerationView{}, ErrPlatformGenerationControlUnavailable
	}
	view := PlatformGenerationView{GenerationID: snapshot.GenerationID, Status: platformGenerationControlStateString(snapshot.State)}
	if snapshot.Mode != 0 {
		mode := platformGenerationControlModeString(snapshot.Mode)
		if mode == "" {
			return PlatformGenerationView{}, ErrPlatformGenerationControlUnavailable
		}
		view.Mode = &mode
	}
	if snapshot.State == PlatformGenerationStateFailed {
		view.Code = snapshot.ErrorCode
	}
	if snapshot.State != PlatformGenerationStateCompleted {
		return view, nil
	}
	receipt, err := c.deps.loadReceipt(ctx, userID, generationID)
	if err != nil || !platformGenerationReceiptMatches(snapshot, receipt, userID, generationID) {
		return PlatformGenerationView{}, ErrPlatformGenerationControlUnavailable
	}
	totalTokens, err := c.deps.loadTotalTokens(ctx, userID)
	if err != nil || totalTokens < 0 {
		return PlatformGenerationView{}, ErrPlatformGenerationControlUnavailable
	}
	conversationGUID := strconv.FormatInt(receipt.ConversationGUID, 10)
	view.ConversationGUID = &conversationGUID
	view.TotalTokensUsed = &totalTokens
	results := make([]PlatformGenerationResultView, 0, len(receipt.Results))
	for _, committed := range receipt.Results {
		result := PlatformGenerationResultView{Model: committed.Model, Status: platformGenerationControlStateString(committed.State)}
		if committed.State == PlatformGenerationStateCompleted {
			tokens := committed.Tokens
			result.AssistantMessageGUID = committed.AssistantMessageGUID
			result.Content = committed.Content
			result.Tokens = &tokens
		} else {
			result.Code = committed.ErrorCode
		}
		results = append(results, result)
	}
	if snapshot.Mode == PlatformGenerationModeSingle {
		view.Result = &results[0]
	} else {
		view.Results = results
	}
	return view, nil
}

func platformGenerationReceiptMatches(snapshot PlatformGenerationSnapshot, receipt PlatformGenerationReceiptSnapshot, userID int64, generationID string) bool {
	if !validPlatformGenerationControlSnapshot(snapshot, generationID) || snapshot.State != PlatformGenerationStateCompleted ||
		receipt.UserID != userID || receipt.GenerationID != generationID ||
		receipt.Mode != snapshot.Mode || receipt.ConversationGUID <= 0 || len(receipt.Results) != len(snapshot.Models) {
		return false
	}
	successes := 0
	for index, model := range snapshot.Models {
		stored, found := snapshot.ModelStates[model]
		committed := receipt.Results[index]
		if !found || committed.Model != model || committed.State != stored.State {
			return false
		}
		switch committed.State {
		case PlatformGenerationStateCompleted:
			successes++
			if committed.AssistantMessageGUID != stored.AssistantMessageGUID || !platformGenerationMessageGUID(committed.AssistantMessageGUID) ||
				committed.Content == "" || !utf8.ValidString(committed.Content) || len([]byte(committed.Content)) > platformGenerationMessageTextMaxBytes ||
				committed.Tokens < 0 || committed.Tokens > math.MaxInt32 || committed.ErrorCode != "" {
				return false
			}
		case PlatformGenerationStateFailed:
			if committed.ErrorCode != stored.ErrorCode || !platformGenerationStableCode(committed.ErrorCode) || committed.AssistantMessageGUID != "" || committed.Content != "" || committed.Tokens != 0 {
				return false
			}
		default:
			return false
		}
	}
	if successes < 1 || successes != receipt.SuccessfulModelCount {
		return false
	}
	return snapshot.Mode != PlatformGenerationModeSingle || (len(receipt.Results) == 1 && receipt.Results[0].State == PlatformGenerationStateCompleted)
}

func validPlatformGenerationControlRequest(c *PlatformGenerationControl, ctx context.Context, userID int64, generationID string) bool {
	return c != nil && ctx != nil && validatePlatformGenerationIdentity(userID, generationID) == nil
}

func validPlatformGenerationControlSnapshot(snapshot PlatformGenerationSnapshot, generationID string) bool {
	return snapshot.GenerationID == generationID && validPlatformGenerationSnapshot(snapshot)
}

func (c *PlatformGenerationControl) controlNowMillis() (int64, bool) {
	if c.now == nil {
		return 0, false
	}
	nowMillis := c.now().UTC().UnixMilli()
	return nowMillis, nowMillis > 0 && platformSSEV2SafeInteger(nowMillis)
}

func platformGenerationControlReadError(err error) error {
	if errors.Is(err, ErrPlatformGenerationNotFound) {
		return ErrPlatformGenerationControlNotFound
	}
	return ErrPlatformGenerationControlUnavailable
}

func platformGenerationControlTerminal(state PlatformGenerationState) bool {
	return state == PlatformGenerationStateCancelled || state == PlatformGenerationStateCompleted || state == PlatformGenerationStateFailed
}

func platformGenerationControlStateString(state PlatformGenerationState) string {
	switch state {
	case PlatformGenerationStateRunning:
		return "running"
	case PlatformGenerationStateCancelling:
		return "cancelling"
	case PlatformGenerationStateCancelled:
		return "cancelled"
	case PlatformGenerationStateCommitting:
		return "committing"
	case PlatformGenerationStateCompleted:
		return "completed"
	case PlatformGenerationStateFailed:
		return "failed"
	default:
		return ""
	}
}

func platformGenerationControlModeString(mode PlatformGenerationMode) string {
	switch mode {
	case PlatformGenerationModeSingle:
		return "single"
	case PlatformGenerationModeCompare:
		return "compare"
	default:
		return ""
	}
}

func platformGenerationControlState(value string) PlatformGenerationState {
	switch value {
	case "cancelling":
		return PlatformGenerationStateCancelling
	case "committing":
		return PlatformGenerationStateCommitting
	default:
		return 0
	}
}

func platformGenerationControlMode(value *string) PlatformGenerationMode {
	if value == nil {
		return 0
	}
	if *value == "single" {
		return PlatformGenerationModeSingle
	}
	if *value == "compare" {
		return PlatformGenerationModeCompare
	}
	return 0
}
