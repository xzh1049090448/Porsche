package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

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
	service := NewPublicRenderJobService(db, []byte("fixture-render-job-purpose-key-32"))
	now := int64(1_900_000_000_000)

	lease, err := service.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-one-long-random-token", NowMillis: now, LeaseMillis: 30_000})
	if err != nil || lease == nil || lease.Generation != fixture.generation || lease.PriceHash == "" || lease.ContentHash == "" {
		t.Fatalf("lease = %#v, %v", lease, err)
	}
	if other, err := service.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-two-long-random-token", NowMillis: now + 1, LeaseMillis: 30_000}); err != nil || other != nil {
		t.Fatalf("concurrent lease = %#v, %v", other, err)
	}
	if err := service.Complete(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: "wrong-owner-long-random-token", Fence: lease.Fence, NowMillis: now + 2}); err != ErrPublicRenderLeaseLost {
		t.Fatalf("wrong owner complete = %v", err)
	}
	if err := service.Renew(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence, NowMillis: now + 3, LeaseMillis: 30_000}); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if err := service.Complete(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence, NowMillis: now + 4}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	health, err := service.Health(ctx, now+5)
	if err != nil || health.Status != "healthy" || health.RenderedGeneration != fixture.generation {
		t.Fatalf("health = %#v, %v", health, err)
	}
	if err := service.Complete(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence, NowMillis: now + 6}); err != ErrPublicRenderLeaseLost {
		t.Fatalf("stale fence complete = %v", err)
	}

	_ = models.PublicRenderJobQueued
}

func TestPublicRenderJobFixtureExpiryRetryAndTerminalFailure(t *testing.T) {
	db := openTestMySQL(t)
	ctx := context.Background()
	seedPublicRenderJobFixture(t, db)
	svc := NewPublicRenderJobService(db, []byte("fixture-render-job-purpose-key-32"))
	now := int64(1_900_000_100_000)
	first, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-one-long-random-token", NowMillis: now, LeaseMillis: 5_000})
	if err != nil || first == nil {
		t.Fatalf("first lease=%#v %v", first, err)
	}
	second, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-two-long-random-token", NowMillis: now + 5_001, LeaseMillis: 5_000})
	if err != nil || second == nil || second.Fence != 2 {
		t.Fatalf("takeover=%#v %v", second, err)
	}
	if err := svc.Fail(ctx, PublicRenderTransitionInput{JobGUID: second.JobGUID, OwnerToken: second.OwnerToken, Fence: second.Fence, NowMillis: now + 5_002, Failure: "/private/raw stderr SECRET"}); err != nil {
		t.Fatal(err)
	}
	if early, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-three-long-random-token", NowMillis: now + 10_000, LeaseMillis: 5_000}); err != nil || early != nil {
		t.Fatalf("early retry=%#v %v", early, err)
	}
	third, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-three-long-random-token", NowMillis: now + 15_003, LeaseMillis: 5_000})
	if err != nil || third == nil || third.Fence != 3 {
		t.Fatalf("third=%#v %v", third, err)
	}
	if err := svc.Fail(ctx, PublicRenderTransitionInput{JobGUID: third.JobGUID, OwnerToken: third.OwnerToken, Fence: third.Fence, NowMillis: now + 15_004, Failure: "validation_failed"}); err != nil {
		t.Fatal(err)
	}
	if retry, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "owner-four-long-random-token", NowMillis: now + 99_000, LeaseMillis: 5_000}); err != nil || retry != nil {
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
	svc := NewPublicRenderJobService(db, []byte("fixture-render-job-purpose-key-32"))
	ctx := context.Background()
	now := int64(1_900_000_200_000)
	start := make(chan struct{})
	results := make(chan *PublicRenderLease, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, owner := range []string{"race-owner-one-long-token", "race-owner-two-long-token"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			lease, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: owner, NowMillis: now, LeaseMillis: 30_000})
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
	lease, err := NewPublicRenderJobService(db, []byte("fixture-render-job-purpose-key-32")).Lease(ctx, PublicRenderLeaseInput{OwnerToken: "obsolete-scan-owner-token", NowMillis: now + 1, LeaseMillis: 30_000})
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
