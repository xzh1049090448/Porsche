package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

type PlatformGenerationIdentity struct {
	UserID       int64
	GenerationID string
}

type PlatformGenerationCancelDecision struct {
	Snapshot         PlatformGenerationSnapshot
	CreatedTombstone bool
	Transitioned     bool
}

func (s *PlatformGenerationStore) CancelOrCreate(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationCancelDecision, error) {
	if ctx == nil || validatePlatformGenerationIdentity(userID, generationID) != nil || !platformSSEV2SafeInteger(nowMillis) || nowMillis <= 0 {
		return PlatformGenerationCancelDecision{}, ErrPlatformGenerationInvalid
	}
	if s == nil || s.client == nil {
		return PlatformGenerationCancelDecision{}, ErrPlatformGenerationUnavailable
	}
	tombstone := PlatformGenerationSnapshot{
		GenerationID:    generationID,
		Models:          []string{},
		State:           PlatformGenerationStateCancelled,
		ModelStates:     map[string]PlatformGenerationModel{},
		CreatedAtMillis: nowMillis,
		UpdatedAtMillis: nowMillis,
	}
	raw, err := encodePlatformGeneration(tombstone)
	if err != nil {
		return PlatformGenerationCancelDecision{}, err
	}
	key := s.key(userID, generationID)
	expectedRaw := ""
	transitionedRaw := ""
	expectedState := int64(platformGenerationDecisionInvalid)
	currentRaw, readErr := s.client.Get(ctx, key).Result()
	if readErr == nil {
		expectedRaw = currentRaw
		current, decodeErr := decodePlatformGeneration(currentRaw)
		if decodeErr == nil && current.GenerationID == generationID {
			expectedState = int64(current.State)
			if current.State == PlatformGenerationStateRunning && nowMillis >= platformGenerationLatestRunningActivity(current) {
				next := clonePlatformGeneration(current)
				next.State = PlatformGenerationStateCancelling
				next.UpdatedAtMillis = nowMillis
				next.LeaseUntilMillis = 0
				transitionedRaw, err = encodePlatformGeneration(next)
				if err != nil {
					return PlatformGenerationCancelDecision{}, err
				}
			}
		}
	} else if !errors.Is(readErr, redis.Nil) {
		return PlatformGenerationCancelDecision{}, ErrPlatformGenerationUnavailable
	}
	result, err := s.client.Eval(ctx, platformGenerationCancelOrCreateScript, []string{s.key(userID, generationID)},
		raw,
		platformGenerationTTL.Milliseconds(),
		expectedRaw,
		transitionedRaw,
		expectedState,
		int64(PlatformGenerationStateRunning),
	).Result()
	if err != nil {
		return PlatformGenerationCancelDecision{}, ErrPlatformGenerationUnavailable
	}
	code, stored, err := platformGenerationDecisionResult(result)
	if err != nil {
		return PlatformGenerationCancelDecision{}, err
	}
	snapshot, err := decodePlatformGeneration(stored)
	if err != nil || snapshot.GenerationID != generationID {
		return PlatformGenerationCancelDecision{}, ErrPlatformGenerationInvalid
	}
	decision := PlatformGenerationCancelDecision{Snapshot: snapshot}
	switch code {
	case platformGenerationDecisionUnchanged:
		return decision, nil
	case platformGenerationDecisionCreated:
		decision.CreatedTombstone = true
		return decision, nil
	case platformGenerationDecisionTransitioned:
		decision.Transitioned = true
		return decision, nil
	case platformGenerationDecisionConflict:
		return decision, ErrPlatformGenerationConflict
	default:
		return PlatformGenerationCancelDecision{}, ErrPlatformGenerationInvalid
	}
}

func platformGenerationLeaseDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

const (
	platformGenerationDecisionInvalid      int64 = -1
	platformGenerationDecisionConflict     int64 = -2
	platformGenerationDecisionUnchanged    int64 = 0
	platformGenerationDecisionCreated      int64 = 1
	platformGenerationDecisionTransitioned int64 = 2
)

func platformGenerationDecisionResult(value any) (int64, string, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) != 2 {
		return 0, "", ErrPlatformGenerationInvalid
	}
	code, ok := items[0].(int64)
	if !ok || code < platformGenerationDecisionConflict || code > platformGenerationDecisionTransitioned {
		return 0, "", ErrPlatformGenerationInvalid
	}
	stored, ok := items[1].(string)
	if !ok || stored == "" {
		return 0, "", ErrPlatformGenerationInvalid
	}
	return code, stored, nil
}

func (s *PlatformGenerationStore) RenewLease(ctx context.Context, userID int64, generationID, leaseToken string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if ctx == nil || validatePlatformGenerationIdentity(userID, generationID) != nil || !validPlatformGenerationLeaseToken(leaseToken) || !platformSSEV2SafeInteger(nowMillis) || nowMillis <= 0 || nowMillis > platformSSEV2MaxSafeInteger-platformGenerationLeaseDuration.Milliseconds() {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	if s == nil || s.client == nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	digest := platformGenerationLeaseDigest(leaseToken)
	current, raw, err := s.loadPlatformGenerationForControl(ctx, userID, generationID)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if !platformGenerationRunningLeaseAuthorized(current, digest, nowMillis) || nowMillis < platformGenerationLatestRunningActivity(current) {
		return current, ErrPlatformGenerationConflict
	}
	next := clonePlatformGeneration(current)
	renewPlatformGenerationLeaseSnapshot(&next, nowMillis)
	return s.writePlatformGenerationControlCAS(ctx, userID, generationID, raw, next)
}

func renewPlatformGenerationLeaseSnapshot(snapshot *PlatformGenerationSnapshot, nowMillis int64) {
	snapshot.UpdatedAtMillis = nowMillis
	snapshot.LeaseUntilMillis = nowMillis + platformGenerationLeaseDuration.Milliseconds()
}

func platformGenerationLatestRunningActivity(snapshot PlatformGenerationSnapshot) int64 {
	latest := snapshot.UpdatedAtMillis
	if snapshot.LeaseUntilMillis != 0 {
		leaseActivity := snapshot.LeaseUntilMillis - platformGenerationLeaseDuration.Milliseconds()
		if leaseActivity > latest {
			latest = leaseActivity
		}
	}
	return latest
}

func validPlatformGenerationLeaseToken(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == platformGenerationLeaseBytes && base64.RawURLEncoding.EncodeToString(raw) == token
}

func (s *PlatformGenerationStore) FailExpiredRunning(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if ctx == nil || validatePlatformGenerationIdentity(userID, generationID) != nil || !platformSSEV2SafeInteger(nowMillis) || nowMillis <= 0 {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	if s == nil || s.client == nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	current, raw, err := s.loadPlatformGenerationForControl(ctx, userID, generationID)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if current.State != PlatformGenerationStateRunning || nowMillis < current.UpdatedAtMillis || (current.LeaseUntilMillis != 0 && nowMillis < current.LeaseUntilMillis) {
		return current, ErrPlatformGenerationConflict
	}
	next := clonePlatformGeneration(current)
	for model, state := range next.ModelStates {
		if state.State == PlatformGenerationStateRunning {
			state.State = PlatformGenerationStateFailed
			state.ErrorCode = "internal_error"
			next.ModelStates[model] = state
		}
	}
	next.State = PlatformGenerationStateFailed
	next.ErrorCode = "internal_error"
	next.UpdatedAtMillis = nowMillis
	next.LeaseOwnerSHA256 = ""
	next.LeaseUntilMillis = 0
	return s.writePlatformGenerationControlCAS(ctx, userID, generationID, raw, next)
}

func (s *PlatformGenerationStore) ConvergeStaleCancelling(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if ctx == nil || validatePlatformGenerationIdentity(userID, generationID) != nil || !platformSSEV2SafeInteger(nowMillis) || nowMillis <= 0 {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	if s == nil || s.client == nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	current, raw, err := s.loadPlatformGenerationForControl(ctx, userID, generationID)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if current.State != PlatformGenerationStateCancelling || nowMillis < current.UpdatedAtMillis || nowMillis-current.UpdatedAtMillis < platformGenerationConvergenceWindow.Milliseconds() {
		return current, ErrPlatformGenerationConflict
	}
	next := clonePlatformGeneration(current)
	for model, state := range next.ModelStates {
		if state.State == PlatformGenerationStateRunning {
			state.State = PlatformGenerationStateCancelled
			next.ModelStates[model] = state
		}
	}
	next.State = PlatformGenerationStateCancelled
	next.UpdatedAtMillis = nowMillis
	next.LeaseOwnerSHA256 = ""
	next.LeaseUntilMillis = 0
	return s.writePlatformGenerationControlCAS(ctx, userID, generationID, raw, next)
}

func (s *PlatformGenerationStore) loadPlatformGenerationForControl(ctx context.Context, userID int64, generationID string) (PlatformGenerationSnapshot, string, error) {
	raw, err := s.client.Get(ctx, s.key(userID, generationID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return PlatformGenerationSnapshot{}, "", ErrPlatformGenerationNotFound
		}
		return PlatformGenerationSnapshot{}, "", ErrPlatformGenerationUnavailable
	}
	snapshot, err := decodePlatformGeneration(raw)
	if err != nil || snapshot.GenerationID != generationID {
		return PlatformGenerationSnapshot{}, raw, ErrPlatformGenerationInvalid
	}
	return snapshot, raw, nil
}

func (s *PlatformGenerationStore) writePlatformGenerationControlCAS(ctx context.Context, userID int64, generationID, expectedRaw string, next PlatformGenerationSnapshot) (PlatformGenerationSnapshot, error) {
	nextRaw, err := encodePlatformGeneration(next)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	result, err := s.client.Eval(ctx, platformGenerationCASScript, []string{s.key(userID, generationID)}, expectedRaw, nextRaw).Result()
	if err != nil {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationUnavailable
	}
	changed, raw, err := platformGenerationEvalResult(result)
	if err != nil {
		if errors.Is(err, ErrPlatformGenerationConflict) {
			return PlatformGenerationSnapshot{}, ErrPlatformGenerationNotFound
		}
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	snapshot, err := decodePlatformGeneration(raw)
	if err != nil || snapshot.GenerationID != generationID {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	if changed {
		return snapshot, nil
	}
	return snapshot, ErrPlatformGenerationConflict
}

func (s *PlatformGenerationStore) ScanGenerationKeys(ctx context.Context, cursor uint64, count int64) ([]PlatformGenerationIdentity, uint64, error) {
	if ctx == nil || count <= 0 || count > 1000 {
		return nil, cursor, ErrPlatformGenerationInvalid
	}
	if s == nil || s.client == nil {
		return nil, cursor, ErrPlatformGenerationUnavailable
	}
	keys, next, err := s.client.Scan(ctx, cursor, platformGenerationPrefix+"*", count).Result()
	if err != nil {
		return nil, cursor, ErrPlatformGenerationUnavailable
	}
	identities := make([]PlatformGenerationIdentity, 0, len(keys))
	for _, key := range keys {
		suffix, ok := strings.CutPrefix(key, platformGenerationPrefix)
		if !ok || strings.Count(suffix, ":") != 1 {
			continue
		}
		userText, generationID, _ := strings.Cut(suffix, ":")
		if userText == "" || userText[0] == '0' || !allPlatformGenerationDecimal(userText) || !platformSSEV2CanonicalUUID(generationID) {
			continue
		}
		userID, parseErr := strconv.ParseInt(userText, 10, 64)
		if parseErr != nil || userID <= 0 || strconv.FormatInt(userID, 10) != userText {
			continue
		}
		identities = append(identities, PlatformGenerationIdentity{UserID: userID, GenerationID: generationID})
	}
	return identities, next, nil
}

func allPlatformGenerationDecimal(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return value != ""
}

const platformGenerationCancelOrCreateScript = `
local old = redis.call('GET', KEYS[1])
if not old then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return {1, ARGV[1]}
end
if old == ARGV[3] then
  if tonumber(ARGV[5]) < 0 then return {-1, old} end
  if tonumber(ARGV[5]) == tonumber(ARGV[6]) then
    if ARGV[4] == '' then return {-2, old} end
    redis.call('SET', KEYS[1], ARGV[4], 'KEEPTTL')
    return {2, ARGV[4]}
  end
  return {0, old}
end
local decoded, current = pcall(cjson.decode, old)
if not decoded or type(current) ~= 'table' or type(current.state) ~= 'number' then return {-1, old} end
if current.state == tonumber(ARGV[6]) then return {-2, old} end
return {0, old}
`
