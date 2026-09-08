package service

import (
	"context"
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
	platformGenerationPrefix         = "porsche:platform:generation:v2:"
	platformGenerationTTL            = 24 * time.Hour
	platformGenerationMaxRecordBytes = 32 << 10
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
	GenerationID    string                             `json:"generation_id"`
	Mode            PlatformGenerationMode             `json:"mode"`
	Models          []string                           `json:"models"`
	State           PlatformGenerationState            `json:"state"`
	ModelStates     map[string]PlatformGenerationModel `json:"model_states"`
	CreatedAtMillis int64                              `json:"created_at_ms"`
	UpdatedAtMillis int64                              `json:"updated_at_ms"`
	ErrorCode       string                             `json:"error_code,omitempty"`
}

type PlatformGenerationClaimInput struct {
	UserID       int64
	GenerationID string
	Mode         PlatformGenerationMode
	Models       []string
	NowMillis    int64
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

// Claim creates one immutable-identity registry record. duplicate=true returns
// the authoritative existing snapshot without changing it or refreshing TTL.
func (s *PlatformGenerationStore) Claim(ctx context.Context, input PlatformGenerationClaimInput) (PlatformGenerationSnapshot, bool, error) {
	if err := validatePlatformGenerationInput(input); err != nil {
		return PlatformGenerationSnapshot{}, false, err
	}
	if s == nil || s.client == nil {
		return PlatformGenerationSnapshot{}, false, ErrPlatformGenerationUnavailable
	}
	models := append([]string(nil), input.Models...)
	modelStates := make(map[string]PlatformGenerationModel, len(models))
	for _, model := range models {
		modelStates[model] = PlatformGenerationModel{State: PlatformGenerationStateRunning}
	}
	snapshot := PlatformGenerationSnapshot{
		GenerationID:    input.GenerationID,
		Mode:            input.Mode,
		Models:          models,
		State:           PlatformGenerationStateRunning,
		ModelStates:     modelStates,
		CreatedAtMillis: input.NowMillis,
		UpdatedAtMillis: input.NowMillis,
	}
	encoded, err := encodePlatformGeneration(snapshot)
	if err != nil {
		return PlatformGenerationSnapshot{}, false, err
	}
	result, err := s.client.Eval(ctx, platformGenerationClaimScript, []string{s.key(input.UserID, input.GenerationID)}, encoded, platformGenerationTTL.Milliseconds()).Result()
	if err != nil {
		return PlatformGenerationSnapshot{}, false, fmt.Errorf("claim Redis generation: %w", err)
	}
	created, stored, err := platformGenerationEvalResult(result)
	if err != nil {
		return PlatformGenerationSnapshot{}, false, err
	}
	decoded, err := decodePlatformGeneration(stored)
	if err != nil {
		return PlatformGenerationSnapshot{}, false, err
	}
	duplicate := !created
	if decoded.GenerationID != input.GenerationID {
		return decoded, duplicate, ErrPlatformGenerationInvalid
	}
	if duplicate {
		return decoded, true, ErrPlatformGenerationConflict
	}
	return decoded, false, nil
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
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var snapshot PlatformGenerationSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || !validPlatformGenerationSnapshot(snapshot) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return snapshot, nil
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

func validPlatformGenerationSnapshot(snapshot PlatformGenerationSnapshot) bool {
	if validatePlatformGenerationInput(PlatformGenerationClaimInput{UserID: 1, GenerationID: snapshot.GenerationID, Mode: snapshot.Mode, Models: snapshot.Models, NowMillis: snapshot.CreatedAtMillis}) != nil || !platformSSEV2SafeInteger(snapshot.UpdatedAtMillis) || snapshot.UpdatedAtMillis < snapshot.CreatedAtMillis || len(snapshot.ModelStates) != len(snapshot.Models) {
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
if old then return {0, old} end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return {1, ARGV[1]}
`

const platformGenerationCASScript = `
local old = redis.call('GET', KEYS[1])
if not old then return {0, ''} end
if old ~= ARGV[1] then return {0, old} end
redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
return {1, ARGV[2]}
`
