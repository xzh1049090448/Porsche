package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

type fakePublicRenderClock struct{ millis int64 }

func (c *fakePublicRenderClock) Now() time.Time { return time.UnixMilli(c.millis).UTC() }

func TestPublicRenderFailureSanitizationAndBackoff(t *testing.T) {
	got := sanitizePublicRenderFailure("render failed at /var/www/private: Authorization: Bearer super-secret\nraw stderr")
	if got != "render_failed" {
		t.Fatalf("sanitized failure = %q", got)
	}
	if d := publicRenderRetryDelay(1); d != 5*time.Second {
		t.Fatalf("first retry = %s", d)
	}
	if d := publicRenderRetryDelay(publicRenderMaxAttempts); d != 0 {
		t.Fatalf("terminal retry = %s", d)
	}
}

func TestPublicRenderCheckedDeadlineAndHealthClockSkew(t *testing.T) {
	if _, ok := publicRenderAddMillis(math.MaxInt64-1, 2); ok {
		t.Fatal("overflow accepted")
	}
	if got, ok := publicRenderAddMillis(1000, 5000); !ok || got != 6000 {
		t.Fatalf("deadline=%d/%v", got, ok)
	}
	health := PublicRenderHealth(100, PublicRenderHealthInput{CurrentGeneration: 2, RenderedGeneration: 1, PublishedAt: 200, PendingGeneration: 2})
	if health.LagMillis != 0 || health.Status != "degraded" {
		t.Fatalf("skew health=%#v", health)
	}
}

func TestPublicRenderOwnerTokenValidation(t *testing.T) {
	for _, tc := range []struct {
		token string
		want  bool
	}{{strings.Repeat("a", 15), false}, {strings.Repeat("a", 16), true}, {strings.Repeat("a", 256), true}, {strings.Repeat("a", 257), false}, {" " + strings.Repeat("a", 16), false}, {strings.Repeat("a", 15) + "\n", false}, {strings.Repeat("a", 15) + "\x00", false}} {
		if got := ValidPublicRenderOwnerToken(tc.token); got != tc.want {
			t.Fatalf("len=%d token valid=%v want=%v", len(tc.token), got, tc.want)
		}
	}
}

func TestPublicRenderHealthThresholds(t *testing.T) {
	now := int64(100_000)
	for _, tc := range []struct {
		name string
		in   PublicRenderHealthInput
		want string
	}{
		{"healthy current generation", PublicRenderHealthInput{CurrentGeneration: 7, RenderedGeneration: 7, PublishedAt: now - 60_000, RenderedAt: now}, "healthy"},
		{"degraded inside failure threshold", PublicRenderHealthInput{CurrentGeneration: 8, RenderedGeneration: 7, PublishedAt: now - 61_000, PendingGeneration: 8}, "degraded"},
		{"failed terminal current job", PublicRenderHealthInput{CurrentGeneration: 8, RenderedGeneration: 7, PublishedAt: now - 301_000, PendingGeneration: 8, CurrentJobTerminal: true}, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PublicRenderHealth(now, tc.in)
			if got.Status != tc.want || got.LagMillis < 0 {
				t.Fatalf("health = %#v", got)
			}
			encoded := strings.Join([]string{got.Status, got.FailureCode}, " ")
			if strings.Contains(encoded, "/") || strings.Contains(encoded, "token") {
				t.Fatalf("health leaked sensitive detail: %q", encoded)
			}
		})
	}
}

func TestPublicRenderJobFixtureLeaseFencingAndCurrentGeneration(t *testing.T) {
	db := openTestMySQL(t)
	ctx := context.Background()
	fixture := seedPublicRenderJobFixture(t, db)
	now := int64(1_900_000_000_000)
	clock := &fakePublicRenderClock{now}
	service := NewPublicRenderJobServiceWithClock(db, []byte("fixture-render-job-purpose-key-32"), clock)

	lease, err := service.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-one-long-random-token", LeaseMillis: 30_000})
	if err != nil || lease == nil || lease.Generation != fixture.generation || lease.PriceHash == "" || lease.ContentHash == "" {
		t.Fatalf("lease = %#v, %v", lease, err)
	}
	if other, err := service.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-two-long-random-token", LeaseMillis: 30_000}); err != nil || other != nil {
		t.Fatalf("concurrent lease = %#v, %v", other, err)
	}
	if err := service.Complete(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: "wrong-owner-long-random-token", Fence: lease.Fence}); err != ErrPublicRenderLeaseLost {
		t.Fatalf("wrong owner complete = %v", err)
	}
	var leased models.PublicRenderJob
	if err := db.Where("guid=?", lease.JobGUID).First(&leased).Error; err != nil {
		t.Fatal(err)
	}
	if leased.LeaseOwnerHMAC == nil || leased.LeaseExpiresAt == nil {
		t.Fatalf("wrong-owner transition changed lease: %#v", leased)
	}
	if leased.State != models.PublicRenderJobLeased || leased.AttemptCount != lease.Fence || *leased.LeaseOwnerHMAC != service.ownerHMAC(lease.OwnerToken) || *leased.LeaseExpiresAt <= now {
		t.Fatalf("stored lease does not match issued lease: %#v issued=%#v", leased, lease)
	}
	if err := service.Renew(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence, LeaseMillis: 30_000}); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if err := service.Complete(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	clock.millis = now + 30_001
	if err := service.Complete(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence}); err != nil {
		t.Fatalf("identical complete replay after expiry: %v", err)
	}
	if err := service.Fail(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence, Failure: "render_failed"}); err != ErrPublicRenderLeaseLost {
		t.Fatalf("cross-operation replay=%v", err)
	}
	health, err := service.Health(ctx)
	if err != nil || health.Status != "healthy" || health.RenderedGeneration != fixture.generation {
		t.Fatalf("health = %#v, %v", health, err)
	}
	if err := service.Complete(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence + 1}); err != ErrPublicRenderLeaseLost {
		t.Fatalf("stale fence complete = %v", err)
	}

	_ = models.PublicRenderJobQueued
}

func TestPublicRenderJobFixtureExpiryRetryAndTerminalFailure(t *testing.T) {
	db := openTestMySQL(t)
	ctx := context.Background()
	seedPublicRenderJobFixture(t, db)
	now := int64(1_900_000_100_000)
	clock := &fakePublicRenderClock{now}
	svc := NewPublicRenderJobServiceWithClock(db, []byte("fixture-render-job-purpose-key-32"), clock)
	first, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-one-long-random-token", LeaseMillis: 5_000})
	if err != nil || first == nil {
		t.Fatalf("first lease=%#v %v", first, err)
	}
	clock.millis = now + 5_000
	if err := svc.Complete(ctx, PublicRenderTransitionInput{JobGUID: first.JobGUID, OwnerToken: first.OwnerToken, Fence: first.Fence}); err != ErrPublicRenderLeaseLost {
		t.Fatalf("complete at exact expiry=%v", err)
	}
	clock.millis = now + 5_001
	second, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-two-long-random-token", LeaseMillis: 5_000})
	if err != nil || second == nil || second.Fence != 2 {
		t.Fatalf("takeover=%#v %v", second, err)
	}
	clock.millis = now + 5_002
	if err := svc.Fail(ctx, PublicRenderTransitionInput{JobGUID: second.JobGUID, OwnerToken: second.OwnerToken, Fence: second.Fence, Failure: "/private/raw stderr SECRET"}); err != nil {
		t.Fatal(err)
	}
	clock.millis = now + 10_000
	if early, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-three-long-random-token", LeaseMillis: 5_000}); err != nil || early != nil {
		t.Fatalf("early retry=%#v %v", early, err)
	}
	clock.millis = now + 15_003
	if err := svc.Fail(ctx, PublicRenderTransitionInput{JobGUID: second.JobGUID, OwnerToken: second.OwnerToken, Fence: second.Fence, Failure: "render_failed"}); err != nil {
		t.Fatalf("identical fail replay after retry expiry: %v", err)
	}
	if err := svc.Fail(ctx, PublicRenderTransitionInput{JobGUID: second.JobGUID, OwnerToken: second.OwnerToken, Fence: second.Fence, Failure: "validation_failed"}); err != ErrPublicRenderLeaseLost {
		t.Fatalf("different fail replay=%v", err)
	}
	var afterReplay models.PublicRenderJob
	if err := db.Where("guid=?", second.JobGUID).First(&afterReplay).Error; err != nil || afterReplay.AttemptCount != 2 || afterReplay.LeaseExpiresAt == nil || *afterReplay.LeaseExpiresAt != now+15_002 {
		t.Fatalf("fail replay mutated outcome=%#v %v", afterReplay, err)
	}
	third, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-three-long-random-token", LeaseMillis: 5_000})
	if err != nil || third == nil || third.Fence != 3 {
		t.Fatalf("third=%#v %v", third, err)
	}
	if err := svc.Fail(ctx, PublicRenderTransitionInput{JobGUID: third.JobGUID, OwnerToken: third.OwnerToken, Fence: third.Fence, Failure: "validation_failed"}); err != nil {
		t.Fatal(err)
	}
	clock.millis = now + 99_000
	if retry, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-four-long-random-token", LeaseMillis: 5_000}); err != nil || retry != nil {
		t.Fatalf("terminal retried=%#v %v", retry, err)
	}
	var job models.PublicRenderJob
	if err := db.Where("guid=?", third.JobGUID).First(&job).Error; err != nil || job.State != models.PublicRenderJobFailed || job.LastFailure == nil || *job.LastFailure != "validation_failed" {
		t.Fatalf("terminal job=%#v %v", job, err)
	}
}

func TestPublicRenderJobFixtureLeaseRaceHasOneOwner(t *testing.T) {
	db := openTestMySQL(t)
	seedPublicRenderJobFixture(t, db)
	ctx := context.Background()
	now := int64(1_900_000_200_000)
	clock := &fakePublicRenderClock{now}
	svc := NewPublicRenderJobServiceWithClock(db, []byte("fixture-render-job-purpose-key-32"), clock)
	first, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "expired-race-owner-token", LeaseMillis: 30_000})
	if err != nil || first == nil {
		t.Fatalf("initial lease=%#v %v", first, err)
	}
	clock.millis = now + 30_001
	start := make(chan struct{})
	results := make(chan *PublicRenderLease, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, owner := range []string{"race-owner-one-long-token", "race-owner-two-long-token"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			lease, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: owner, LeaseMillis: 30_000})
			results <- lease
			errs <- err
		}(owner)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for lease := range results {
		if lease != nil {
			winners++
			if lease.Fence != first.Fence+1 {
				t.Fatalf("takeover fence=%d want=%d", lease.Fence, first.Fence+1)
			}
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("race error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("lease winners=%d", winners)
	}
}

func TestPublicRenderJobFixtureConcurrentCompleteReplayConverges(t *testing.T) {
	db := openTestMySQL(t)
	seedPublicRenderJobFixture(t, db)
	ctx := context.Background()
	now := int64(1_900_000_250_000)
	clock := &fakePublicRenderClock{now}
	svc := NewPublicRenderJobServiceWithClock(db, []byte("fixture-render-job-purpose-key-32"), clock)
	lease, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "concurrent-complete-owner", LeaseMillis: 30_000})
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.Complete(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replay=%v", err)
		}
	}
}

func TestPublicRenderJobFixtureSkipsObsoleteGeneration(t *testing.T) {
	db := openTestMySQL(t)
	seedPublicRenderJobFixture(t, db)
	ctx := context.Background()
	now := int64(1_900_000_300_000)
	var state models.PublicPublicationState
	if err := db.Where("state_key=?", publicPublicationStateKey).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	price := models.PublicPriceSnapshot{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now, Version: 991, Reason: models.PublicPriceSnapshotReasonRootPublish, SourceRevision: 1, ContentHash: strings.Repeat("c", 64), PublishedAt: now}
	content := models.PublicContentRelease{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now, DocumentKind: models.PublicContentDocumentSite, Version: 992, SourceRevision: 1, Payload: models.JSONMap{"site": "new"}, ContentHash: strings.Repeat("d", 64), PublishedAt: now}
	if err := db.Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&content).Error; err != nil {
		t.Fatal(err)
	}
	job := models.PublicRenderJob{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now}, PriceSnapshotID: price.ID, ContentReleaseID: content.ID, State: models.PublicRenderJobQueued}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&state).Updates(map[string]any{"price_snapshot_id": price.ID, "content_release_id": content.ID, "revision": state.Revision + 1, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Model(&models.PublicPublicationState{}).Where("id=?", state.ID).Updates(map[string]any{"price_snapshot_id": state.PriceSnapshotID, "content_release_id": state.ContentReleaseID, "revision": state.Revision})
		db.Exec("DELETE FROM public_render_jobs WHERE id=?", job.ID)
		db.Exec("DELETE FROM public_content_releases WHERE id=?", content.ID)
		db.Exec("DELETE FROM public_price_snapshots WHERE id=?", price.ID)
	})
	clock := &fakePublicRenderClock{now + 1}
	lease, err := NewPublicRenderJobServiceWithClock(db, []byte("fixture-render-job-purpose-key-32"), clock).Lease(ctx, PublicRenderLeaseInput{OwnerToken: "obsolete-scan-owner-token", LeaseMillis: 30_000})
	if err != nil || lease == nil || lease.JobGUID != job.Guid {
		t.Fatalf("current lease=%#v %v", lease, err)
	}
	var obsolete models.PublicRenderJob
	if err := db.Where("id < ?", job.ID).Order("id DESC").First(&obsolete).Error; err != nil || obsolete.State != models.PublicRenderJobFailed || obsolete.LastFailure == nil || *obsolete.LastFailure != "obsolete_generation" {
		t.Fatalf("obsolete=%#v %v", obsolete, err)
	}
}

type publicRenderFixture struct{ generation int64 }

func seedPublicRenderJobFixture(t *testing.T, db *gorm.DB) publicRenderFixture {
	t.Helper()
	now := int64(1_899_999_900_000)
	price := models.PublicPriceSnapshot{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now, Version: 901, Reason: models.PublicPriceSnapshotReasonRootPublish, SourceRevision: 1, ContentHash: strings.Repeat("a", 64), PublishedAt: now}
	content := models.PublicContentRelease{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now, DocumentKind: models.PublicContentDocumentSite, Version: 902, SourceRevision: 1, Payload: models.JSONMap{"site": "fixture"}, ContentHash: strings.Repeat("b", 64), PublishedAt: now}
	if err := db.Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&content).Error; err != nil {
		t.Fatal(err)
	}
	var state models.PublicPublicationState
	createdState := false
	if err := db.Where("state_key=?", publicPublicationStateKey).First(&state).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal(err)
		}
		state = models.PublicPublicationState{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now}, StateKey: publicPublicationStateKey, PriceVisibility: models.PublicPriceVisibilityVisible, Revision: 903}
		if err := db.Create(&state).Error; err != nil {
			t.Fatal(err)
		}
		createdState = true
	}
	previousState := state
	if err := db.Model(&state).Updates(map[string]any{"price_snapshot_id": price.ID, "content_release_id": content.ID, "revision": int64(903), "is_deleted": 0, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	state.PriceSnapshotID = &price.ID
	state.ContentReleaseID = &content.ID
	state.Revision = 903
	job := models.PublicRenderJob{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now}, PriceSnapshotID: price.ID, ContentReleaseID: content.ID, State: models.PublicRenderJobQueued}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM public_render_jobs WHERE id=?", job.ID)
		if createdState {
			db.Exec("DELETE FROM public_publication_state WHERE id=?", state.ID)
		} else {
			db.Model(&models.PublicPublicationState{}).Where("id=?", state.ID).Updates(map[string]any{"price_snapshot_id": previousState.PriceSnapshotID, "content_release_id": previousState.ContentReleaseID, "revision": previousState.Revision, "is_deleted": previousState.IsDeleted, "updated_at": previousState.UpdatedAt})
		}
		db.Exec("DELETE FROM public_content_releases WHERE id=?", content.ID)
		db.Exec("DELETE FROM public_price_snapshots WHERE id=?", price.ID)
	})
	return publicRenderFixture{generation: job.Guid}
}
