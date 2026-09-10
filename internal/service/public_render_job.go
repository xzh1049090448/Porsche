package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	publicRenderMaxAttempts = 3
	publicRenderHealthyLag  = int64(60_000)
	publicRenderFailedLag   = int64(300_000)
)

var (
	ErrPublicRenderInvalid     = errors.New("invalid public render job request")
	ErrPublicRenderLeaseLost   = errors.New("public render job lease lost")
	ErrPublicRenderUnavailable = errors.New("public render job unavailable")
	publicRenderFailureCode    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

type PublicRenderJobService struct {
	db    *gorm.DB
	key   []byte
	clock PublicRenderClock
}
type PublicRenderClock interface{ Now() time.Time }
type publicRenderSystemClock struct{}

func (publicRenderSystemClock) Now() time.Time { return time.Now().UTC() }

type PublicRenderLeaseInput struct {
	OwnerToken  string
	LeaseMillis int64
}

type PublicRenderTransitionInput struct {
	JobGUID     int64
	OwnerToken  string
	Fence       int
	LeaseMillis int64
	Failure     string
}

type PublicRenderLease struct {
	JobGUID        int64  `json:"job_guid"`
	OwnerToken     string `json:"-"`
	Fence          int    `json:"fence"`
	Generation     int64  `json:"generation"`
	PriceVersion   int64  `json:"price_version"`
	ContentVersion int64  `json:"content_version"`
	PriceHash      string `json:"price_hash"`
	ContentHash    string `json:"content_hash"`
	LeaseExpiresAt int64  `json:"lease_expires_at"`
}

type PublicRenderHealthInput struct {
	CurrentGeneration  int64
	RenderedGeneration int64
	PublishedAt        int64
	RenderedAt         int64
	PendingGeneration  int64
	CurrentJobTerminal bool
	FailureCode        string
}

type PublicRenderHealthStatus struct {
	Status             string `json:"status"`
	LagMillis          int64  `json:"lag_millis"`
	CurrentGeneration  int64  `json:"current_generation,omitempty"`
	RenderedGeneration int64  `json:"rendered_generation,omitempty"`
	PendingGeneration  int64  `json:"pending_generation,omitempty"`
	FailureCode        string `json:"failure_code,omitempty"`
}
type PublicRenderGeneration struct {
	Generation     int64  `json:"generation"`
	PriceVersion   int64  `json:"price_version"`
	ContentVersion int64  `json:"content_version"`
	PriceHash      string `json:"price_hash"`
	ContentHash    string `json:"content_hash"`
}

func (s *PublicRenderJobService) Lookup(ctx context.Context) (*PublicRenderGeneration, error) {
	if !s.validDB() {
		return nil, ErrPublicRenderInvalid
	}
	var state models.PublicPublicationState
	if err := s.db.WithContext(ctx).Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; err != nil {
		return nil, ErrPublicRenderUnavailable
	}
	if state.PriceSnapshotID == nil || state.ContentReleaseID == nil {
		return nil, nil
	}
	var job models.PublicRenderJob
	if err := s.db.WithContext(ctx).Where("price_snapshot_id=? AND content_release_id=? AND is_deleted=0", *state.PriceSnapshotID, *state.ContentReleaseID).First(&job).Error; err != nil {
		return nil, ErrPublicRenderUnavailable
	}
	ok, p, c, err := loadPublicRenderGeneration(s.db.WithContext(ctx), job.PriceSnapshotID, job.ContentReleaseID)
	if err != nil || !ok {
		return nil, ErrPublicRenderUnavailable
	}
	return &PublicRenderGeneration{job.Guid, p.version, c.version, p.hash, c.hash}, nil
}

func NewPublicRenderJobService(db *gorm.DB, purposeKey []byte) *PublicRenderJobService {
	return NewPublicRenderJobServiceWithClock(db, purposeKey, publicRenderSystemClock{})
}
func NewPublicRenderJobServiceWithClock(db *gorm.DB, purposeKey []byte, clock PublicRenderClock) *PublicRenderJobService {
	return &PublicRenderJobService{db: db, key: append([]byte(nil), purposeKey...), clock: clock}
}
func (s *PublicRenderJobService) nowMillis() (int64, error) {
	if s == nil || s.clock == nil {
		return 0, ErrPublicRenderInvalid
	}
	now := s.clock.Now().UTC().UnixMilli()
	if now <= 0 {
		return 0, ErrPublicRenderInvalid
	}
	return now, nil
}

func (s *PublicRenderJobService) valid() bool   { return s != nil && s.db != nil && len(s.key) >= 16 }
func (s *PublicRenderJobService) validDB() bool { return s != nil && s.db != nil }

func (s *PublicRenderJobService) ownerHMAC(token string) string {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func validPublicRenderLeaseInput(owner string, lease int64) bool {
	return ValidPublicRenderOwnerToken(owner) && lease >= 5_000 && lease <= 300_000
}

func ValidPublicRenderOwnerToken(owner string) bool {
	if owner != strings.TrimSpace(owner) || len(owner) < 16 || len(owner) > 256 {
		return false
	}
	for _, r := range owner {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func (s *PublicRenderJobService) Lease(ctx context.Context, in PublicRenderLeaseInput) (*PublicRenderLease, error) {
	now, clockErr := s.nowMillis()
	if !s.valid() || clockErr != nil || !validPublicRenderLeaseInput(in.OwnerToken, in.LeaseMillis) {
		return nil, ErrPublicRenderInvalid
	}
	var out *PublicRenderLease
	err := s.db.Session(&gorm.Session{NewDB: true}).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for scan := 0; scan < 8; scan++ {
			var job models.PublicRenderJob
			err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Where("is_deleted=0 AND attempt_count < ? AND ((state=? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)) OR (state=? AND lease_expires_at <= ?))", publicRenderMaxAttempts, models.PublicRenderJobQueued, now, models.PublicRenderJobLeased, now).
				Order("id ASC").First(&job).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return ErrPublicRenderUnavailable
			}

			current, price, content, err := loadPublicRenderGeneration(tx, job.PriceSnapshotID, job.ContentReleaseID)
			if err != nil {
				return err
			}
			if !current {
				if err := tx.Model(&models.PublicRenderJob{}).Where("id=? AND state IN ?", job.ID, []models.PublicRenderJobState{models.PublicRenderJobQueued, models.PublicRenderJobLeased}).Updates(map[string]any{"state": models.PublicRenderJobFailed, "last_failure": "obsolete_generation", "lease_owner_hmac": nil, "lease_expires_at": nil, "updated_at": now}).Error; err != nil {
					return ErrPublicRenderUnavailable
				}
				continue
			}
			attempt := job.AttemptCount + 1
			expires, ok := publicRenderAddMillis(now, in.LeaseMillis)
			if !ok {
				return ErrPublicRenderInvalid
			}
			hash := s.ownerHMAC(strings.TrimSpace(in.OwnerToken))
			res := tx.Model(&models.PublicRenderJob{}).Where("id=? AND attempt_count=?", job.ID, job.AttemptCount).Updates(map[string]any{"state": models.PublicRenderJobLeased, "lease_owner_hmac": hash, "lease_expires_at": expires, "attempt_count": attempt, "last_failure": nil, "last_terminal_owner_hmac": nil, "last_terminal_fence": nil, "last_terminal_operation": nil, "last_terminal_state": nil, "updated_at": now})
			if res.Error != nil {
				return ErrPublicRenderUnavailable
			}
			if res.RowsAffected != 1 {
				continue
			}
			out = &PublicRenderLease{JobGUID: job.Guid, OwnerToken: strings.TrimSpace(in.OwnerToken), Fence: attempt, Generation: job.Guid, PriceVersion: price.version, ContentVersion: content.version, PriceHash: price.hash, ContentHash: content.hash, LeaseExpiresAt: expires}
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type renderGenerationPart struct {
	generation, version int64
	hash                string
}

func loadPublicRenderGeneration(tx *gorm.DB, priceID, contentID int64) (bool, renderGenerationPart, renderGenerationPart, error) {
	var state models.PublicPublicationState
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; err != nil {
		return false, renderGenerationPart{}, renderGenerationPart{}, ErrPublicRenderUnavailable
	}
	if state.PriceSnapshotID == nil || state.ContentReleaseID == nil || *state.PriceSnapshotID != priceID || *state.ContentReleaseID != contentID {
		return false, renderGenerationPart{}, renderGenerationPart{}, nil
	}
	var price models.PublicPriceSnapshot
	var content models.PublicContentRelease
	if err := tx.Where("id=? AND is_deleted=0", priceID).First(&price).Error; err != nil {
		return false, renderGenerationPart{}, renderGenerationPart{}, ErrPublicRenderUnavailable
	}
	if err := tx.Where("id=? AND is_deleted=0", contentID).First(&content).Error; err != nil {
		return false, renderGenerationPart{}, renderGenerationPart{}, ErrPublicRenderUnavailable
	}
	if len(price.ContentHash) != 64 || len(content.ContentHash) != 64 {
		return false, renderGenerationPart{}, renderGenerationPart{}, ErrPublicRenderUnavailable
	}
	return true, renderGenerationPart{state.Revision, price.Version, price.ContentHash}, renderGenerationPart{state.Revision, content.Version, content.ContentHash}, nil
}

func (s *PublicRenderJobService) Renew(ctx context.Context, in PublicRenderTransitionInput) error {
	now, clockErr := s.nowMillis()
	if !s.valid() || clockErr != nil || in.JobGUID <= 0 || in.Fence <= 0 || !validPublicRenderLeaseInput(in.OwnerToken, in.LeaseMillis) {
		return ErrPublicRenderInvalid
	}
	expires, ok := publicRenderAddMillis(now, in.LeaseMillis)
	if !ok {
		return ErrPublicRenderInvalid
	}
	return s.transitionCurrent(ctx, in, now, map[string]any{"lease_expires_at": expires, "updated_at": now})
}

func (s *PublicRenderJobService) Complete(ctx context.Context, in PublicRenderTransitionInput) error {
	now, clockErr := s.nowMillis()
	if !s.valid() || clockErr != nil || in.JobGUID <= 0 || in.Fence <= 0 || !ValidPublicRenderOwnerToken(in.OwnerToken) {
		return ErrPublicRenderInvalid
	}
	return s.db.Session(&gorm.Session{NewDB: true}).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job models.PublicRenderJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guid=? AND is_deleted=0", in.JobGUID).First(&job).Error; err != nil {
			return ErrPublicRenderLeaseLost
		}
		if terminalReplay(job, s.ownerHMAC(in.OwnerToken), in.Fence, 1) {
			return nil
		}
		current, _, _, err := loadPublicRenderGeneration(tx, job.PriceSnapshotID, job.ContentReleaseID)
		if err != nil {
			return err
		}
		if !current {
			return ErrPublicRenderLeaseLost
		}
		terminalState := models.PublicRenderJobSucceeded
		op := 1
		res := tx.Model(&models.PublicRenderJob{}).Where("id=? AND state=? AND attempt_count=? AND lease_owner_hmac=? AND lease_expires_at>?", job.ID, models.PublicRenderJobLeased, in.Fence, s.ownerHMAC(in.OwnerToken), now).Updates(map[string]any{"state": terminalState, "completed_at": now, "lease_owner_hmac": nil, "lease_expires_at": nil, "last_failure": nil, "last_terminal_owner_hmac": s.ownerHMAC(in.OwnerToken), "last_terminal_fence": in.Fence, "last_terminal_operation": op, "last_terminal_state": terminalState, "updated_at": now})
		return renderTransitionResult(res)
	})
}

func (s *PublicRenderJobService) Fail(ctx context.Context, in PublicRenderTransitionInput) error {
	now, clockErr := s.nowMillis()
	if !s.valid() || clockErr != nil || in.JobGUID <= 0 || in.Fence <= 0 || !ValidPublicRenderOwnerToken(in.OwnerToken) {
		return ErrPublicRenderInvalid
	}
	code := sanitizePublicRenderFailure(in.Failure)
	state := models.PublicRenderJobQueued
	next := int64(0)
	if in.Fence >= publicRenderMaxAttempts {
		state = models.PublicRenderJobFailed
	} else {
		var ok bool
		next, ok = publicRenderAddMillis(now, publicRenderRetryDelay(in.Fence).Milliseconds())
		if !ok {
			return ErrPublicRenderInvalid
		}
	}
	updates := map[string]any{"state": state, "last_failure": code, "lease_owner_hmac": nil, "updated_at": now}
	op := 2
	updates["last_terminal_owner_hmac"] = s.ownerHMAC(in.OwnerToken)
	updates["last_terminal_fence"] = in.Fence
	updates["last_terminal_operation"] = op
	updates["last_terminal_state"] = state
	if next == 0 {
		updates["lease_expires_at"] = nil
	} else {
		updates["lease_expires_at"] = next
	}
	return s.transitionCurrent(ctx, in, now, updates)
}

func (s *PublicRenderJobService) transitionCurrent(ctx context.Context, in PublicRenderTransitionInput, now int64, updates map[string]any) error {
	return s.db.Session(&gorm.Session{NewDB: true}).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job models.PublicRenderJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guid=? AND is_deleted=0", in.JobGUID).First(&job).Error; err != nil {
			return ErrPublicRenderLeaseLost
		}
		if terminalReplay(job, s.ownerHMAC(in.OwnerToken), in.Fence, 2) {
			failure, _ := updates["last_failure"].(string)
			if job.LastFailure != nil && *job.LastFailure == failure {
				return nil
			}
			return ErrPublicRenderLeaseLost
		}
		current, _, _, err := loadPublicRenderGeneration(tx, job.PriceSnapshotID, job.ContentReleaseID)
		if err != nil {
			return err
		}
		if !current {
			return ErrPublicRenderLeaseLost
		}
		owner := s.ownerHMAC(strings.TrimSpace(in.OwnerToken))
		if job.State != models.PublicRenderJobLeased || job.AttemptCount != in.Fence || job.LeaseOwnerHMAC == nil ||
			!hmac.Equal([]byte(*job.LeaseOwnerHMAC), []byte(owner)) || job.LeaseExpiresAt == nil || *job.LeaseExpiresAt <= now {
			return ErrPublicRenderLeaseLost
		}
		if res := tx.Model(&models.PublicRenderJob{}).Where("id=?", job.ID).Updates(updates); res.Error != nil {
			return ErrPublicRenderUnavailable
		}
		return nil
	})
}

func terminalReplay(job models.PublicRenderJob, owner string, fence, operation int) bool {
	return job.LastTerminalOwnerHMAC != nil && hmac.Equal([]byte(*job.LastTerminalOwnerHMAC), []byte(owner)) && job.LastTerminalFence != nil && *job.LastTerminalFence == fence && job.LastTerminalOperation != nil && *job.LastTerminalOperation == operation && job.LastTerminalState != nil && *job.LastTerminalState == job.State
}

func renderTransitionResult(res *gorm.DB) error {
	if res.Error != nil {
		return ErrPublicRenderUnavailable
	}
	if res.RowsAffected != 1 {
		return ErrPublicRenderLeaseLost
	}
	return nil
}

func sanitizePublicRenderFailure(raw string) string {
	raw = strings.TrimSpace(raw)
	if publicRenderFailureCode.MatchString(raw) {
		return raw
	}
	return "render_failed"
}

func publicRenderRetryDelay(attempt int) time.Duration {
	if attempt <= 0 || attempt >= publicRenderMaxAttempts {
		return 0
	}
	return time.Duration(5*(1<<(attempt-1))) * time.Second
}
func publicRenderAddMillis(now, delta int64) (int64, bool) {
	if now <= 0 || delta < 0 || delta > 0 && now > int64(^uint64(0)>>1)-delta {
		return 0, false
	}
	return now + delta, true
}

func PublicRenderHealth(now int64, in PublicRenderHealthInput) PublicRenderHealthStatus {
	lag := int64(0)
	if in.CurrentGeneration > in.RenderedGeneration && in.PublishedAt > 0 && now > in.PublishedAt {
		lag = now - in.PublishedAt
	}
	status := "healthy"
	if in.CurrentGeneration > in.RenderedGeneration {
		status = "degraded"
		if in.CurrentJobTerminal || lag > publicRenderFailedLag {
			status = "failed"
		}
	}
	if lag <= publicRenderHealthyLag && in.CurrentGeneration == in.RenderedGeneration {
		status = "healthy"
	}
	return PublicRenderHealthStatus{Status: status, LagMillis: lag, CurrentGeneration: in.CurrentGeneration, RenderedGeneration: in.RenderedGeneration, PendingGeneration: in.PendingGeneration, FailureCode: sanitizeHealthFailure(in.FailureCode)}
}

func sanitizeHealthFailure(code string) string {
	if publicRenderFailureCode.MatchString(code) {
		return code
	}
	return ""
}

func (s *PublicRenderJobService) Health(ctx context.Context) (PublicRenderHealthStatus, error) {
	now, clockErr := s.nowMillis()
	if !s.validDB() || clockErr != nil {
		return PublicRenderHealthStatus{}, ErrPublicRenderInvalid
	}
	var state models.PublicPublicationState
	if err := s.db.WithContext(ctx).Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; err != nil {
		return PublicRenderHealthStatus{}, ErrPublicRenderUnavailable
	}
	in := PublicRenderHealthInput{}
	if state.ContentReleaseID != nil {
		var c models.PublicContentRelease
		if err := s.db.WithContext(ctx).Where("id=? AND is_deleted=0", *state.ContentReleaseID).First(&c).Error; err == nil {
			in.PublishedAt = c.PublishedAt
		}
	}
	if state.PriceSnapshotID != nil && state.ContentReleaseID != nil {
		var current models.PublicRenderJob
		err := s.db.WithContext(ctx).Where("price_snapshot_id=? AND content_release_id=? AND is_deleted=0", *state.PriceSnapshotID, *state.ContentReleaseID).First(&current).Error
		if err == nil {
			in.CurrentGeneration = current.Guid
			if current.State != models.PublicRenderJobSucceeded {
				in.PendingGeneration = current.Guid
			}
			in.CurrentJobTerminal = current.State == models.PublicRenderJobFailed
			if current.LastFailure != nil {
				in.FailureCode = *current.LastFailure
			}
		} else {
			return PublicRenderHealthStatus{}, ErrPublicRenderUnavailable
		}
	}
	var succeeded models.PublicRenderJob
	if err := s.db.WithContext(ctx).Where("state=? AND is_deleted=0", models.PublicRenderJobSucceeded).Order("completed_at DESC").First(&succeeded).Error; err == nil && succeeded.CompletedAt != nil {
		in.RenderedGeneration = succeeded.Guid
		in.RenderedAt = *succeeded.CompletedAt
	}
	return PublicRenderHealth(now, in), nil
}
