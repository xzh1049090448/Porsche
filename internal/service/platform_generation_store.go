package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	platformGenerationPrefix            = "porsche:platform:generation:v2:"
	platformGenerationTTL               = 24 * time.Hour
	platformGenerationMaxRecordBytes    = 32 << 10
	platformGenerationConvergenceWindow = 30 * time.Second
	platformGenerationLeaseDuration     = 30 * time.Second
	platformGenerationLeaseBytes        = 32
)

type PlatformGenerationMode int

const (
	PlatformGenerationModeSingle  PlatformGenerationMode = 1
	PlatformGenerationModeCompare PlatformGenerationMode = 2
)

type PlatformGenerationState int

const (
	PlatformGenerationStateRunning    PlatformGenerationState = 1
	PlatformGenerationStateCancelling PlatformGenerationState = 2
	PlatformGenerationStateCancelled  PlatformGenerationState = 3
	PlatformGenerationStateCommitting PlatformGenerationState = 4
	PlatformGenerationStateCompleted  PlatformGenerationState = 5
	PlatformGenerationStateFailed     PlatformGenerationState = 6
)

var (
	ErrPlatformGenerationInvalid     = errors.New("invalid platform generation")
	ErrPlatformGenerationConflict    = errors.New("platform generation state conflict")
	ErrPlatformGenerationNotFound    = errors.New("platform generation not found")
	ErrPlatformGenerationUnavailable = errors.New("Redis generation store is unavailable")
)

type PlatformGenerationModel struct {
	Seq                  int64                   `json:"seq"`
	State                PlatformGenerationState `json:"state"`
	ErrorCode            string                  `json:"error_code,omitempty"`
	AssistantMessageGUID string                  `json:"assistant_message_guid,omitempty"`
}

// PlatformGenerationSnapshot intentionally stores lifecycle metadata only.
// It never contains prompt/reply text, credentials, quota counters, costs, or URLs.
type PlatformGenerationSnapshot struct {
	GenerationID     string                             `json:"generation_id"`
	Mode             PlatformGenerationMode             `json:"mode,omitempty"`
	Models           []string                           `json:"models"`
	State            PlatformGenerationState            `json:"state"`
	ModelStates      map[string]PlatformGenerationModel `json:"model_states"`
	CreatedAtMillis  int64                              `json:"created_at_ms"`
	UpdatedAtMillis  int64                              `json:"updated_at_ms"`
	ErrorCode        string                             `json:"error_code,omitempty"`
	LeaseOwnerSHA256 string                             `json:"lease_owner_sha256,omitempty"`
	LeaseUntilMillis int64                              `json:"lease_until_ms,omitempty"`
}

type PlatformGenerationClaimInput struct {
	UserID       int64
	GenerationID string
	Mode         PlatformGenerationMode
	Models       []string
	NowMillis    int64
}

type PlatformGenerationClaimResult struct {
	Snapshot   PlatformGenerationSnapshot `json:"snapshot"`
	Duplicate  bool                       `json:"duplicate"`
	LeaseToken string                     `json:"-"`
}

// PlatformGenerationStore is a fail-closed Redis lifecycle registry. Ownership
// is enforced by keys containing only the authenticated internal users.id and
// the client generation UUID.
type PlatformGenerationStore struct {
	client redis.UniversalClient
}

func NewPlatformGenerationStoreFromURL(ctx context.Context, rawURL string) (*PlatformGenerationStore, error) {
	options, err := redis.ParseURL(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	client := redis.NewClient(options)
	store, err := NewPlatformGenerationStore(client)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := store.CheckAvailable(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return store, nil
}

func NewPlatformGenerationStore(client redis.UniversalClient) (*PlatformGenerationStore, error) {
	if client == nil {
		return nil, ErrPlatformGenerationUnavailable
	}
	return &PlatformGenerationStore{client: client}, nil
}

func (s *PlatformGenerationStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *PlatformGenerationStore) CheckAvailable(ctx context.Context) error {
	if s == nil || s.client == nil {
		return ErrPlatformGenerationUnavailable
	}
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("verify Redis generation store: %w", err)
	}
	return nil
}

func (s *PlatformGenerationStore) key(userID int64, generationID string) string {
	return fmt.Sprintf("%s%d:%s", platformGenerationPrefix, userID, generationID)
}

// Claim creates one immutable-identity registry record. Duplicate results return
// the authoritative existing snapshot without changing it or refreshing TTL.
func (s *PlatformGenerationStore) Claim(ctx context.Context, input PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error) {
	if err := validatePlatformGenerationInput(input); err != nil {
		return PlatformGenerationClaimResult{}, err
	}
	if input.NowMillis > platformSSEV2MaxSafeInteger-platformGenerationLeaseDuration.Milliseconds() {
		return PlatformGenerationClaimResult{}, ErrPlatformGenerationInvalid
	}
	if s == nil || s.client == nil {
		return PlatformGenerationClaimResult{}, ErrPlatformGenerationUnavailable
	}
	leaseToken, leaseDigest, err := newPlatformGenerationLease()
	if err != nil {
		return PlatformGenerationClaimResult{}, err
	}
	models := append([]string(nil), input.Models...)
	modelStates := make(map[string]PlatformGenerationModel, len(models))
	for _, model := range models {
		modelStates[model] = PlatformGenerationModel{State: PlatformGenerationStateRunning}
	}
	snapshot := PlatformGenerationSnapshot{
		GenerationID:     input.GenerationID,
		Mode:             input.Mode,
		Models:           models,
		State:            PlatformGenerationStateRunning,
		ModelStates:      modelStates,
		CreatedAtMillis:  input.NowMillis,
		UpdatedAtMillis:  input.NowMillis,
		LeaseOwnerSHA256: leaseDigest,
		LeaseUntilMillis: input.NowMillis + platformGenerationLeaseDuration.Milliseconds(),
	}
	encoded, err := encodePlatformGeneration(snapshot)
	if err != nil {
		return PlatformGenerationClaimResult{}, err
	}
	enrichedStates := make(map[string]PlatformGenerationModel, len(models))
	for _, model := range models {
		enrichedStates[model] = PlatformGenerationModel{State: PlatformGenerationStateCancelled}
	}
	enriched, err := encodePlatformGeneration(PlatformGenerationSnapshot{
		GenerationID:    input.GenerationID,
		Mode:            input.Mode,
		Models:          models,
		State:           PlatformGenerationStateCancelled,
		ModelStates:     enrichedStates,
		CreatedAtMillis: input.NowMillis,
		UpdatedAtMillis: input.NowMillis,
	})
	if err != nil {
		return PlatformGenerationClaimResult{}, err
	}
	result, err := s.client.Eval(ctx, platformGenerationClaimScript, []string{s.key(input.UserID, input.GenerationID)}, encoded, enriched, platformGenerationTTL.Milliseconds(), input.GenerationID, int64(PlatformGenerationStateCancelled), platformSSEV2MaxSafeInteger).Result()
	if err != nil {
		return PlatformGenerationClaimResult{}, ErrPlatformGenerationUnavailable
	}
	decision, stored, err := platformGenerationDecisionResult(result)
	if err != nil {
		return PlatformGenerationClaimResult{}, err
	}
	decoded, err := decodePlatformGeneration(stored)
	if err != nil {
		return PlatformGenerationClaimResult{}, err
	}
	duplicate := decision != platformGenerationDecisionCreated
	if decoded.GenerationID != input.GenerationID {
		return PlatformGenerationClaimResult{Snapshot: decoded, Duplicate: duplicate}, ErrPlatformGenerationInvalid
	}
	if duplicate {
		return PlatformGenerationClaimResult{Snapshot: decoded, Duplicate: true}, ErrPlatformGenerationConflict
	}
	return PlatformGenerationClaimResult{Snapshot: decoded, LeaseToken: leaseToken}, nil
}

func newPlatformGenerationLease() (string, string, error) {
	return newPlatformGenerationLeaseFrom(rand.Reader)
}

func newPlatformGenerationLeaseFrom(reader io.Reader) (string, string, error) {
	if reader == nil {
		return "", "", ErrPlatformGenerationUnavailable
	}
	raw := make([]byte, platformGenerationLeaseBytes)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", "", ErrPlatformGenerationUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(digest[:]), nil
}

func (s *PlatformGenerationStore) Get(ctx context.Context, userID int64, generationID string) (PlatformGenerationSnapshot, error) {
	if err := validatePlatformGenerationIdentity(userID, generationID); err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if s == nil || s.client == nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	raw, err := s.client.Get(ctx, s.key(userID, generationID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return PlatformGenerationSnapshot{}, ErrPlatformGenerationNotFound
		}
		return PlatformGenerationSnapshot{}, fmt.Errorf("read Redis generation: %w", err)
	}
	snapshot, err := decodePlatformGeneration(raw)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if snapshot.GenerationID != generationID {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return snapshot, nil
}

func (s *PlatformGenerationStore) RecordDelta(ctx context.Context, userID int64, generationID, model string, seq, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if !platformSSEV2ModelIdentifier(model) || !platformSSEV2SafeInteger(seq) || seq == 0 {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		modelState, found := snapshot.ModelStates[model]
		if snapshot.State != PlatformGenerationStateRunning || !found || modelState.State != PlatformGenerationStateRunning || seq != modelState.Seq+1 {
			return ErrPlatformGenerationConflict
		}
		modelState.Seq = seq
		snapshot.ModelStates[model] = modelState
		return nil
	})
}

func (s *PlatformGenerationStore) RecordDeltaOwned(ctx context.Context, userID int64, generationID, leaseToken, model string, seq, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if !platformSSEV2ModelIdentifier(model) || !platformSSEV2SafeInteger(seq) || seq == 0 {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return s.mutateOwned(ctx, userID, generationID, leaseToken, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		modelState, found := snapshot.ModelStates[model]
		if snapshot.State != PlatformGenerationStateRunning || !found || modelState.State != PlatformGenerationStateRunning || seq != modelState.Seq+1 {
			return ErrPlatformGenerationConflict
		}
		modelState.Seq = seq
		snapshot.ModelStates[model] = modelState
		return nil
	})
}

func (s *PlatformGenerationStore) MarkModelDone(ctx context.Context, userID int64, generationID, model string, lastSeq, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if !platformSSEV2ModelIdentifier(model) || !platformSSEV2SafeInteger(lastSeq) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		modelState, found := snapshot.ModelStates[model]
		if snapshot.State != PlatformGenerationStateRunning || !found || modelState.State != PlatformGenerationStateRunning || modelState.Seq != lastSeq {
			return ErrPlatformGenerationConflict
		}
		modelState.State = PlatformGenerationStateCompleted
		snapshot.ModelStates[model] = modelState
		return nil
	})
}

func (s *PlatformGenerationStore) MarkModelDoneOwned(ctx context.Context, userID int64, generationID, leaseToken, model string, lastSeq, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if !platformSSEV2ModelIdentifier(model) || !platformSSEV2SafeInteger(lastSeq) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return s.mutateOwned(ctx, userID, generationID, leaseToken, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		modelState, found := snapshot.ModelStates[model]
		if snapshot.State != PlatformGenerationStateRunning || !found || modelState.State != PlatformGenerationStateRunning || modelState.Seq != lastSeq {
			return ErrPlatformGenerationConflict
		}
		modelState.State = PlatformGenerationStateCompleted
		snapshot.ModelStates[model] = modelState
		return nil
	})
}

func (s *PlatformGenerationStore) MarkModelFailed(ctx context.Context, userID int64, generationID, model, code string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if !platformSSEV2ModelIdentifier(model) || !platformGenerationStableCode(code) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		modelState, found := snapshot.ModelStates[model]
		if snapshot.State != PlatformGenerationStateRunning || !found || modelState.State != PlatformGenerationStateRunning {
			return ErrPlatformGenerationConflict
		}
		modelState.State = PlatformGenerationStateFailed
		modelState.ErrorCode = code
		snapshot.ModelStates[model] = modelState
		return nil
	})
}

func (s *PlatformGenerationStore) RequestCancel(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if snapshot.State != PlatformGenerationStateRunning {
			return ErrPlatformGenerationConflict
		}
		snapshot.State = PlatformGenerationStateCancelling
		return nil
	})
}

func (s *PlatformGenerationStore) MarkCancelled(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if snapshot.State != PlatformGenerationStateCancelling {
			return ErrPlatformGenerationConflict
		}
		for model, modelState := range snapshot.ModelStates {
			if modelState.State == PlatformGenerationStateRunning {
				modelState.State = PlatformGenerationStateCancelled
				snapshot.ModelStates[model] = modelState
			}
		}
		snapshot.State = PlatformGenerationStateCancelled
		return nil
	})
}

func (s *PlatformGenerationStore) BeginCommit(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if snapshot.State != PlatformGenerationStateRunning {
			return ErrPlatformGenerationConflict
		}
		successes := 0
		for _, model := range snapshot.Models {
			switch snapshot.ModelStates[model].State {
			case PlatformGenerationStateCompleted:
				successes++
			case PlatformGenerationStateFailed:
			default:
				return ErrPlatformGenerationConflict
			}
		}
		if successes == 0 {
			return ErrPlatformGenerationConflict
		}
		snapshot.State = PlatformGenerationStateCommitting
		return nil
	})
}

func (s *PlatformGenerationStore) BeginCommitOwned(ctx context.Context, userID int64, generationID, leaseToken string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	return s.mutateOwned(ctx, userID, generationID, leaseToken, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if snapshot.State != PlatformGenerationStateRunning {
			return ErrPlatformGenerationConflict
		}
		successes := 0
		for _, model := range snapshot.Models {
			switch snapshot.ModelStates[model].State {
			case PlatformGenerationStateCompleted:
				successes++
			case PlatformGenerationStateFailed:
			default:
				return ErrPlatformGenerationConflict
			}
		}
		if successes == 0 {
			return ErrPlatformGenerationConflict
		}
		snapshot.State = PlatformGenerationStateCommitting
		return nil
	})
}

func (s *PlatformGenerationStore) Complete(ctx context.Context, userID int64, generationID string, assistantMessageGUIDs map[string]string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if snapshot.State != PlatformGenerationStateCommitting {
			return ErrPlatformGenerationConflict
		}
		expectedGUIDs := 0
		seenGUIDs := make(map[string]struct{}, len(assistantMessageGUIDs))
		for _, model := range snapshot.Models {
			modelState := snapshot.ModelStates[model]
			if modelState.State != PlatformGenerationStateCompleted {
				continue
			}
			expectedGUIDs++
			guid, found := assistantMessageGUIDs[model]
			if !found || !platformGenerationMessageGUID(guid) {
				return ErrPlatformGenerationInvalid
			}
			if _, duplicate := seenGUIDs[guid]; duplicate {
				return ErrPlatformGenerationInvalid
			}
			seenGUIDs[guid] = struct{}{}
			modelState.AssistantMessageGUID = guid
			snapshot.ModelStates[model] = modelState
		}
		if len(assistantMessageGUIDs) != expectedGUIDs {
			return ErrPlatformGenerationInvalid
		}
		snapshot.State = PlatformGenerationStateCompleted
		return nil
	})
}

func (s *PlatformGenerationStore) ReconcileComplete(ctx context.Context, userID int64, generationID string, assistantMessageGUIDs map[string]string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if validatePlatformGenerationReconciliationRequest(s, ctx, userID, generationID, nowMillis) != nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	snapshot, err := s.Get(ctx, userID, generationID)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if snapshot.State == PlatformGenerationStateCompleted {
		if platformGenerationGUIDMapMatches(snapshot, assistantMessageGUIDs) {
			return snapshot, nil
		}
		return snapshot, ErrPlatformGenerationConflict
	}
	completed, completeErr := s.Complete(ctx, userID, generationID, assistantMessageGUIDs, nowMillis)
	if errors.Is(completeErr, ErrPlatformGenerationConflict) && completed.State == PlatformGenerationStateCompleted && platformGenerationGUIDMapMatches(completed, assistantMessageGUIDs) {
		return completed, nil
	}
	return completed, completeErr
}

func (s *PlatformGenerationStore) FailStaleCommit(ctx context.Context, userID int64, generationID, code string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if validatePlatformGenerationReconciliationRequest(s, ctx, userID, generationID, nowMillis) != nil || !platformGenerationStableCode(code) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if snapshot.State != PlatformGenerationStateCommitting || nowMillis-snapshot.UpdatedAtMillis < platformGenerationConvergenceWindow.Milliseconds() {
			return ErrPlatformGenerationConflict
		}
		snapshot.State = PlatformGenerationStateFailed
		snapshot.ErrorCode = code
		return nil
	})
}

func validatePlatformGenerationReconciliationRequest(s *PlatformGenerationStore, ctx context.Context, userID int64, generationID string, nowMillis int64) error {
	if s == nil || ctx == nil || validatePlatformGenerationIdentity(userID, generationID) != nil ||
		!platformSSEV2SafeInteger(nowMillis) || nowMillis <= 0 {
		return ErrPlatformGenerationInvalid
	}
	return nil
}

func platformGenerationGUIDMapMatches(snapshot PlatformGenerationSnapshot, expected map[string]string) bool {
	count := 0
	for _, model := range snapshot.Models {
		state := snapshot.ModelStates[model]
		if state.State != PlatformGenerationStateCompleted {
			continue
		}
		count++
		if expected[model] != state.AssistantMessageGUID {
			return false
		}
	}
	return count == len(expected)
}

func (s *PlatformGenerationStore) Fail(ctx context.Context, userID int64, generationID, code string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if !platformGenerationStableCode(code) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if snapshot.State != PlatformGenerationStateRunning {
			return ErrPlatformGenerationConflict
		}
		for model, modelState := range snapshot.ModelStates {
			if modelState.State == PlatformGenerationStateRunning {
				modelState.State = PlatformGenerationStateFailed
				modelState.ErrorCode = code
				snapshot.ModelStates[model] = modelState
			}
		}
		snapshot.State = PlatformGenerationStateFailed
		snapshot.ErrorCode = code
		return nil
	})
}

func (s *PlatformGenerationStore) FailRunningOwned(ctx context.Context, userID int64, generationID, leaseToken, code string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if !platformGenerationStableCode(code) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return s.mutateOwned(ctx, userID, generationID, leaseToken, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		return failOwnedSnapshot(snapshot, code)
	})
}

func failOwnedSnapshot(snapshot *PlatformGenerationSnapshot, code string) error {
	if snapshot.State != PlatformGenerationStateRunning {
		return ErrPlatformGenerationConflict
	}
	for model, state := range snapshot.ModelStates {
		if state.State == PlatformGenerationStateRunning {
			state.State = PlatformGenerationStateFailed
			state.ErrorCode = code
			snapshot.ModelStates[model] = state
		}
	}
	snapshot.State = PlatformGenerationStateFailed
	snapshot.ErrorCode = code
	return nil
}

func (s *PlatformGenerationStore) AcknowledgeCancelledOwned(ctx context.Context, userID int64, generationID, leaseToken string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	return s.mutateOwned(ctx, userID, generationID, leaseToken, nowMillis, acknowledgeCancelledOwnedSnapshot)
}

func acknowledgeCancelledOwnedSnapshot(snapshot *PlatformGenerationSnapshot) error {
	if snapshot.State != PlatformGenerationStateCancelling {
		return ErrPlatformGenerationConflict
	}
	for model, state := range snapshot.ModelStates {
		if state.State == PlatformGenerationStateRunning {
			state.State = PlatformGenerationStateCancelled
			snapshot.ModelStates[model] = state
		}
	}
	snapshot.State = PlatformGenerationStateCancelled
	return nil
}

func platformGenerationLeaseMatches(snapshot PlatformGenerationSnapshot, digest string) bool {
	if len(snapshot.LeaseOwnerSHA256) != sha256.Size*2 || len(digest) != sha256.Size*2 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(snapshot.LeaseOwnerSHA256), []byte(digest)) == 1
}

func (s *PlatformGenerationStore) mutateOwned(
	ctx context.Context,
	userID int64,
	generationID string,
	leaseToken string,
	nowMillis int64,
	change func(*PlatformGenerationSnapshot) error,
) (PlatformGenerationSnapshot, error) {
	if ctx == nil || !validPlatformGenerationLeaseToken(leaseToken) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	digest := platformGenerationLeaseDigest(leaseToken)
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if !platformGenerationLeaseMatches(*snapshot, digest) {
			return ErrPlatformGenerationConflict
		}
		return change(snapshot)
	})
}

func (s *PlatformGenerationStore) mutate(ctx context.Context, userID int64, generationID string, nowMillis int64, change func(*PlatformGenerationSnapshot) error) (PlatformGenerationSnapshot, error) {
	if err := validatePlatformGenerationIdentity(userID, generationID); err != nil || !platformSSEV2SafeInteger(nowMillis) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	if s == nil || s.client == nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	key := s.key(userID, generationID)
	currentRaw, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return PlatformGenerationSnapshot{}, ErrPlatformGenerationNotFound
		}
		return PlatformGenerationSnapshot{}, fmt.Errorf("read Redis generation: %w", err)
	}
	current, err := decodePlatformGeneration(currentRaw)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if current.GenerationID != generationID {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	if nowMillis < current.UpdatedAtMillis {
		return current, ErrPlatformGenerationConflict
	}
	next := clonePlatformGeneration(current)
	if err := change(&next); err != nil {
		return current, err
	}
	next.UpdatedAtMillis = nowMillis
	if next.State == PlatformGenerationStateCancelling {
		next.LeaseUntilMillis = 0
	} else if next.State != PlatformGenerationStateRunning {
		next.LeaseOwnerSHA256 = ""
		next.LeaseUntilMillis = 0
	}
	nextRaw, err := encodePlatformGeneration(next)
	if err != nil {
		return current, err
	}
	result, err := s.client.Eval(ctx, platformGenerationCASScript, []string{key}, currentRaw, nextRaw).Result()
	if err != nil {
		return PlatformGenerationSnapshot{}, fmt.Errorf("transition Redis generation: %w", err)
	}
	changed, stored, err := platformGenerationEvalResult(result)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	authoritative, err := decodePlatformGeneration(stored)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if !changed {
		return authoritative, ErrPlatformGenerationConflict
	}
	return authoritative, nil
}

func clonePlatformGeneration(snapshot PlatformGenerationSnapshot) PlatformGenerationSnapshot {
	copySnapshot := snapshot
	copySnapshot.Models = append([]string(nil), snapshot.Models...)
	copySnapshot.ModelStates = make(map[string]PlatformGenerationModel, len(snapshot.ModelStates))
	for model, state := range snapshot.ModelStates {
		copySnapshot.ModelStates[model] = state
	}
	return copySnapshot
}

func encodePlatformGeneration(snapshot PlatformGenerationSnapshot) (string, error) {
	if !validPlatformGenerationSnapshot(snapshot) {
		return "", ErrPlatformGenerationInvalid
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > platformGenerationMaxRecordBytes {
		return "", ErrPlatformGenerationInvalid
	}
	return string(encoded), nil
}

func decodePlatformGeneration(raw string) (PlatformGenerationSnapshot, error) {
	if raw == "" || len(raw) > platformGenerationMaxRecordBytes {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	if err := validatePlatformGenerationJSONMembers(raw); err != nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var snapshot PlatformGenerationSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || !validPlatformGenerationWire(raw, snapshot) || !validPlatformGenerationSnapshot(snapshot) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return snapshot, nil
}

func validatePlatformGenerationJSONMembers(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := walkPlatformGenerationJSONValue(decoder); err != nil {
		return ErrPlatformGenerationInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrPlatformGenerationInvalid
	}
	return nil
}

func walkPlatformGenerationJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrPlatformGenerationInvalid
			}
			if _, duplicate := members[key]; duplicate {
				return ErrPlatformGenerationInvalid
			}
			members[key] = struct{}{}
			if err := walkPlatformGenerationJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return ErrPlatformGenerationInvalid
		}
	case '[':
		for decoder.More() {
			if err := walkPlatformGenerationJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return ErrPlatformGenerationInvalid
		}
	default:
		return ErrPlatformGenerationInvalid
	}
	return nil
}

func validatePlatformGenerationIdentity(userID int64, generationID string) error {
	if userID <= 0 || !platformSSEV2CanonicalUUID(generationID) {
		return ErrPlatformGenerationInvalid
	}
	return nil
}

func validatePlatformGenerationInput(input PlatformGenerationClaimInput) error {
	if validatePlatformGenerationIdentity(input.UserID, input.GenerationID) != nil || !platformSSEV2SafeInteger(input.NowMillis) || len(input.Models) == 0 || len(input.Models) > platformSSEV2MaxModels {
		return ErrPlatformGenerationInvalid
	}
	if (input.Mode == PlatformGenerationModeSingle && len(input.Models) != 1) || (input.Mode == PlatformGenerationModeCompare && len(input.Models) < 2) {
		return ErrPlatformGenerationInvalid
	}
	if input.Mode != PlatformGenerationModeSingle && input.Mode != PlatformGenerationModeCompare {
		return ErrPlatformGenerationInvalid
	}
	seen := make(map[string]struct{}, len(input.Models))
	for _, model := range input.Models {
		if !platformSSEV2ModelIdentifier(model) {
			return ErrPlatformGenerationInvalid
		}
		if _, duplicate := seen[model]; duplicate {
			return ErrPlatformGenerationInvalid
		}
		seen[model] = struct{}{}
	}
	return nil
}

func validPlatformGenerationWire(raw string, snapshot PlatformGenerationSnapshot) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || !platformGenerationWireRequired(fields, "generation_id", "models", "state", "model_states", "created_at_ms", "updated_at_ms") {
		return false
	}
	if snapshot.State == PlatformGenerationStateCancelled && snapshot.Mode == 0 {
		return !platformGenerationWirePresent(fields, "mode") && !platformGenerationWirePresent(fields, "error_code") && !platformGenerationWirePresent(fields, "lease_owner_sha256") && !platformGenerationWirePresent(fields, "lease_until_ms")
	}
	if !platformGenerationWireRequired(fields, "mode") {
		return false
	}
	if snapshot.State == PlatformGenerationStateFailed {
		if !platformGenerationWireRequired(fields, "error_code") || snapshot.ErrorCode == "" {
			return false
		}
	} else if platformGenerationWirePresent(fields, "error_code") {
		return false
	}
	if snapshot.State == PlatformGenerationStateRunning {
		digestPresent := platformGenerationWirePresent(fields, "lease_owner_sha256")
		deadlinePresent := platformGenerationWirePresent(fields, "lease_until_ms")
		if digestPresent != deadlinePresent {
			return false
		}
		if digestPresent && (!platformGenerationWireRequired(fields, "lease_owner_sha256", "lease_until_ms") || snapshot.LeaseOwnerSHA256 == "" || snapshot.LeaseUntilMillis <= 0) {
			return false
		}
	} else if snapshot.State == PlatformGenerationStateCancelling {
		digestPresent := platformGenerationWirePresent(fields, "lease_owner_sha256")
		if platformGenerationWirePresent(fields, "lease_until_ms") || (digestPresent && (!platformGenerationWireRequired(fields, "lease_owner_sha256") || snapshot.LeaseOwnerSHA256 == "")) {
			return false
		}
	} else if platformGenerationWirePresent(fields, "lease_owner_sha256") || platformGenerationWirePresent(fields, "lease_until_ms") {
		return false
	}
	var modelFields map[string]map[string]json.RawMessage
	if err := json.Unmarshal(fields["model_states"], &modelFields); err != nil || len(modelFields) != len(snapshot.ModelStates) {
		return false
	}
	for model, state := range snapshot.ModelStates {
		fields, found := modelFields[model]
		if !found || !platformGenerationWireRequired(fields, "seq", "state") {
			return false
		}
		if state.State == PlatformGenerationStateFailed {
			if !platformGenerationWireRequired(fields, "error_code") || state.ErrorCode == "" {
				return false
			}
		} else if platformGenerationWirePresent(fields, "error_code") {
			return false
		}
		requiresGUID := state.State == PlatformGenerationStateCompleted && snapshot.State == PlatformGenerationStateCompleted
		if requiresGUID {
			if !platformGenerationWireRequired(fields, "assistant_message_guid") || state.AssistantMessageGUID == "" {
				return false
			}
		} else if platformGenerationWirePresent(fields, "assistant_message_guid") {
			return false
		}
	}
	return true
}

func platformGenerationWireRequired(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		raw, found := fields[name]
		if !found || strings.TrimSpace(string(raw)) == "null" {
			return false
		}
	}
	return true
}

func platformGenerationWirePresent(fields map[string]json.RawMessage, name string) bool {
	_, found := fields[name]
	return found
}

func validPlatformGenerationSnapshot(snapshot PlatformGenerationSnapshot) bool {
	if snapshot.State == PlatformGenerationStateCancelled && snapshot.Mode == 0 {
		return validPlatformGenerationTombstone(snapshot)
	}
	if validatePlatformGenerationInput(PlatformGenerationClaimInput{UserID: 1, GenerationID: snapshot.GenerationID, Mode: snapshot.Mode, Models: snapshot.Models, NowMillis: snapshot.CreatedAtMillis}) != nil || !platformSSEV2SafeInteger(snapshot.UpdatedAtMillis) || snapshot.UpdatedAtMillis < snapshot.CreatedAtMillis || len(snapshot.ModelStates) != len(snapshot.Models) {
		return false
	}
	if !validPlatformGenerationLease(snapshot) {
		return false
	}
	if snapshot.State < PlatformGenerationStateRunning || snapshot.State > PlatformGenerationStateFailed {
		return false
	}
	if snapshot.State == PlatformGenerationStateFailed {
		if !platformGenerationStableCode(snapshot.ErrorCode) {
			return false
		}
	} else if snapshot.ErrorCode != "" {
		return false
	}
	terminalModels := 0
	successfulModels := 0
	for _, model := range snapshot.Models {
		modelState, found := snapshot.ModelStates[model]
		if !found || !platformSSEV2SafeInteger(modelState.Seq) {
			return false
		}
		switch modelState.State {
		case PlatformGenerationStateRunning:
			if modelState.ErrorCode != "" || modelState.AssistantMessageGUID != "" {
				return false
			}
		case PlatformGenerationStateCompleted:
			terminalModels++
			successfulModels++
			if modelState.ErrorCode != "" {
				return false
			}
			if snapshot.State == PlatformGenerationStateCompleted {
				if !platformGenerationMessageGUID(modelState.AssistantMessageGUID) {
					return false
				}
			} else if modelState.AssistantMessageGUID != "" {
				return false
			}
		case PlatformGenerationStateFailed:
			terminalModels++
			if !platformGenerationStableCode(modelState.ErrorCode) || modelState.AssistantMessageGUID != "" {
				return false
			}
		case PlatformGenerationStateCancelled:
			terminalModels++
			if modelState.ErrorCode != "" || modelState.AssistantMessageGUID != "" {
				return false
			}
		default:
			return false
		}
	}
	if snapshot.State == PlatformGenerationStateCommitting || snapshot.State == PlatformGenerationStateCompleted {
		return terminalModels == len(snapshot.Models) && successfulModels > 0
	}
	if snapshot.State == PlatformGenerationStateCancelled || snapshot.State == PlatformGenerationStateFailed {
		return terminalModels == len(snapshot.Models)
	}
	return true
}

func validPlatformGenerationTombstone(snapshot PlatformGenerationSnapshot) bool {
	return snapshot.Models != nil && snapshot.ModelStates != nil && len(snapshot.Models) == 0 && len(snapshot.ModelStates) == 0 && snapshot.ErrorCode == "" && snapshot.LeaseOwnerSHA256 == "" && snapshot.LeaseUntilMillis == 0 && platformSSEV2CanonicalUUID(snapshot.GenerationID) && snapshot.CreatedAtMillis > 0 && platformSSEV2SafeInteger(snapshot.CreatedAtMillis) && snapshot.UpdatedAtMillis == snapshot.CreatedAtMillis
}

func validPlatformGenerationLease(snapshot PlatformGenerationSnapshot) bool {
	switch snapshot.State {
	case PlatformGenerationStateRunning:
		if snapshot.LeaseOwnerSHA256 == "" && snapshot.LeaseUntilMillis == 0 {
			return true
		}
		return validPlatformGenerationLeaseDigest(snapshot.LeaseOwnerSHA256) && platformSSEV2SafeInteger(snapshot.LeaseUntilMillis) && snapshot.LeaseUntilMillis > snapshot.UpdatedAtMillis
	case PlatformGenerationStateCancelling:
		return (snapshot.LeaseOwnerSHA256 == "" || validPlatformGenerationLeaseDigest(snapshot.LeaseOwnerSHA256)) && snapshot.LeaseUntilMillis == 0
	default:
		return snapshot.LeaseOwnerSHA256 == "" && snapshot.LeaseUntilMillis == 0
	}
}

func validPlatformGenerationLeaseDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func platformGenerationMessageGUID(value string) bool {
	guid, err := strconv.ParseInt(value, 10, 64)
	return err == nil && guid > 0 && strconv.FormatInt(guid, 10) == value
}

func platformGenerationStableCode(code string) bool {
	_, ok := platformSSEV2StableCodes[code]
	return ok
}

func platformGenerationEvalResult(value any) (bool, string, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) != 2 {
		return false, "", ErrPlatformGenerationInvalid
	}
	changed, ok := items[0].(int64)
	if !ok || (changed != 0 && changed != 1) {
		return false, "", ErrPlatformGenerationInvalid
	}
	stored, ok := items[1].(string)
	if !ok || stored == "" {
		return false, "", ErrPlatformGenerationConflict
	}
	return changed == 1, stored, nil
}

const platformGenerationClaimScript = `
local old = redis.call('GET', KEYS[1])
if old then
  local decoded, existing = pcall(cjson.decode, old)
  local prefix = '{"generation_id":"' .. ARGV[4] .. '","models":[],"state":' .. ARGV[5] .. ',"model_states":{},"created_at_ms":'
  if decoded and type(existing) == 'table' and existing.generation_id == ARGV[4] and existing.state == tonumber(ARGV[5]) and existing.mode == nil and existing.error_code == nil and existing.lease_owner_sha256 == nil and existing.lease_until_ms == nil and string.sub(old, 1, string.len(prefix)) == prefix then
    local tail = string.sub(old, string.len(prefix) + 1)
    local created, updated = string.match(tail, '^([1-9][0-9]*),"updated_at_ms":([1-9][0-9]*)}$')
    if created and created == updated and tonumber(created) <= tonumber(ARGV[6]) then
      local next, replacements = string.gsub(ARGV[2], '"created_at_ms":[0-9]+,"updated_at_ms":[0-9]+}', '"created_at_ms":' .. created .. ',"updated_at_ms":' .. updated .. '}')
      if replacements == 1 then
        redis.call('SET', KEYS[1], next, 'KEEPTTL')
        return {2, next}
      end
    end
  end
  return {0, old}
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
return {1, ARGV[1]}
`

const platformGenerationCASScript = `
local old = redis.call('GET', KEYS[1])
if not old then return {0, ''} end
if old ~= ARGV[1] then return {0, old} end
redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
return {1, ARGV[2]}
`
