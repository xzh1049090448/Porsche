package service

import (
	"context"
	"errors"
	"math"
	"os"
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

func TestEffectiveAnnouncementWatermarkQueuesOncePerReachedBoundary(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour).Format(time.RFC3339)
	at := now.Format(time.RFC3339)
	future := now.Add(time.Hour).Format(time.RFC3339)
	home := PublicHomeConfig{Announcements: []PublicHomeConfigAnnouncement{{GUID: "1", EffectiveAt: &past}, {GUID: "2", EffectiveAt: &at}, {GUID: "3", EffectiveAt: &future}}}
	watermark, err := effectiveAnnouncementWatermark(home, now.UnixMilli())
	if err != nil || watermark != now.UnixMilli() {
		t.Fatalf("watermark=%d err=%v", watermark, err)
	}
	queued := models.PublicRenderJob{AuditFields: models.AuditFields{UpdatedAt: now.Add(-2 * time.Hour).UnixMilli()}, State: models.PublicRenderJobQueued}
	succeededBefore := models.PublicRenderJob{AuditFields: models.AuditFields{UpdatedAt: now.Add(-time.Second).UnixMilli()}, State: models.PublicRenderJobSucceeded, CompletedAt: pointerInt64(now.Add(-time.Second).UnixMilli())}
	succeededAt := models.PublicRenderJob{AuditFields: models.AuditFields{UpdatedAt: now.UnixMilli()}, State: models.PublicRenderJobSucceeded, CompletedAt: pointerInt64(now.UnixMilli())}
	failedBefore := models.PublicRenderJob{AuditFields: models.AuditFields{UpdatedAt: now.Add(-time.Second).UnixMilli()}, State: models.PublicRenderJobFailed}
	failedAfter := models.PublicRenderJob{AuditFields: models.AuditFields{UpdatedAt: now.Add(time.Second).UnixMilli()}, State: models.PublicRenderJobFailed}
	for name, tc := range map[string]struct {
		job  models.PublicRenderJob
		want bool
	}{"queued": {queued, false}, "succeeded before": {succeededBefore, true}, "succeeded at": {succeededAt, false}, "failed before": {failedBefore, true}, "failed after": {failedAfter, false}} {
		if got := shouldQueueEffectiveAnnouncement(tc.job, watermark); got != tc.want {
			t.Fatalf("%s=%v want=%v", name, got, tc.want)
		}
	}
}

func TestRenderAttemptCrossingEffectiveBoundaryRequiresFollowup(t *testing.T) {
	for _, tc := range []struct {
		name         string
		attemptStart *int64
		watermark    int64
		want         bool
	}{
		{name: "started before boundary", attemptStart: pointerInt64(900), watermark: 1000, want: true},
		{name: "started at boundary", attemptStart: pointerInt64(1000), watermark: 1000, want: false},
		{name: "started after boundary", attemptStart: pointerInt64(1100), watermark: 1000, want: false},
		{name: "legacy missing marker is conservative", attemptStart: nil, watermark: 1000, want: true},
		{name: "no effective announcement", attemptStart: nil, watermark: 0, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderAttemptCrossedEffectiveBoundary(tc.attemptStart, tc.watermark); got != tc.want {
				t.Fatalf("crossed=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestPublicRenderFenceAdvancesAcrossBoundaryAndResetsRetryCycle(t *testing.T) {
	for _, tc := range []struct {
		fence int
		base  int
	}{{0, 0}, {1, 3}, {2, 3}, {3, 3}, {4, 6}, {5, 6}, {6, 6}} {
		base, ok := publicRenderNextCycleBase(tc.fence)
		if !ok || base != tc.base {
			t.Fatalf("fence=%d next cycle base=%d/%v want=%d/true", tc.fence, base, ok, tc.base)
		}
	}
	base, _ := publicRenderNextCycleBase(1)
	nextFence := base + 1
	if nextFence <= 1 {
		t.Fatalf("next fence=%d must advance", nextFence)
	}
	if ordinal := publicRenderAttemptOrdinal(nextFence); ordinal != 1 {
		t.Fatalf("next attempt ordinal=%d want=1", ordinal)
	}
	for _, tc := range []struct {
		fence   int
		ordinal int
		delay   time.Duration
	}{{1, 1, 5 * time.Second}, {4, 1, 5 * time.Second}, {5, 2, 10 * time.Second}, {6, 3, 0}} {
		ordinal := publicRenderAttemptOrdinal(tc.fence)
		if ordinal != tc.ordinal || publicRenderRetryDelay(ordinal) != tc.delay {
			t.Fatalf("fence=%d ordinal/delay=%d/%s want=%d/%s", tc.fence, ordinal, publicRenderRetryDelay(ordinal), tc.ordinal, tc.delay)
		}
	}
	if _, ok := publicRenderNextCycleBase(publicRenderMaxFence); ok {
		t.Fatal("overflowing next cycle accepted")
	}
}

func TestPublicRenderJobFixtureBoundaryRequeueRejectsDelayedFirstAttempt(t *testing.T) {
	for _, operation := range []string{"complete", "fail"} {
		t.Run(operation, func(t *testing.T) {
			assertBoundaryRequeueRejectsDelayedFirstAttempt(t, operation)
		})
	}
}

func assertBoundaryRequeueRejectsDelayedFirstAttempt(t *testing.T, operation string) {
	t.Helper()
	db := openTestMySQL(t)
	ctx := context.Background()
	effective := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	job := seedStructuredPublicRenderJobFixture(t, db, effective)
	var releasesBefore, jobsBefore int64
	if err := db.Model(&models.PublicContentRelease{}).Count(&releasesBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.PublicRenderJob{}).Count(&jobsBefore).Error; err != nil {
		t.Fatal(err)
	}

	clock := &fakePublicRenderClock{millis: effective.Add(-100 * time.Millisecond).UnixMilli()}
	svc := NewPublicRenderJobServiceWithClock(db, []byte("fixture-render-job-purpose-key-32"), clock)
	first, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "cross-boundary-render-owner", LeaseMillis: 30_000})
	if err != nil || first == nil || first.JobGUID != job.Guid {
		t.Fatalf("pre-boundary lease=%#v err=%v", first, err)
	}
	attemptInputAt := clock.millis
	clock.millis = effective.Add(-50 * time.Millisecond).UnixMilli()
	if err = svc.Renew(ctx, PublicRenderTransitionInput{JobGUID: first.JobGUID, OwnerToken: first.OwnerToken, Fence: first.Fence, LeaseMillis: 30_000}); err != nil {
		t.Fatal(err)
	}
	var renewed models.PublicRenderJob
	if err = db.Where("guid=?", first.JobGUID).First(&renewed).Error; err != nil || renewed.CompletedAt == nil || *renewed.CompletedAt != attemptInputAt || renewed.UpdatedAt != clock.millis {
		t.Fatalf("renewed job=%#v input_at=%d updated_at=%d err=%v", renewed, attemptInputAt, clock.millis, err)
	}
	reader := newPublicCatalogReadServiceWithClock(db, func() time.Time { return time.UnixMilli(clock.millis).UTC() })
	projection, err := reader.Projection(ctx)
	if err != nil || !projection.HomeConfigAvailable || len(projection.HomeConfig.Announcements) != 0 {
		t.Fatalf("pre-boundary projection=%#v err=%v", projection, err)
	}

	clock.millis = effective.Add(100 * time.Millisecond).UnixMilli()
	if err = svc.Complete(ctx, PublicRenderTransitionInput{JobGUID: first.JobGUID, OwnerToken: first.OwnerToken, Fence: first.Fence}); err != nil {
		t.Fatal(err)
	}
	var crossed models.PublicRenderJob
	if err = db.Where("guid=?", first.JobGUID).First(&crossed).Error; err != nil || crossed.State != models.PublicRenderJobQueued || crossed.UpdatedAt != clock.millis || crossed.CompletedAt != nil {
		t.Fatalf("crossed completion=%#v input_at=%d completed_at=%d err=%v", crossed, attemptInputAt, clock.millis, err)
	}
	second, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "cross-boundary-render-owner", LeaseMillis: 30_000})
	if err != nil || second == nil || second.JobGUID != job.Guid || second.Fence <= first.Fence {
		t.Fatalf("due-boundary lease=%#v err=%v", second, err)
	}
	projection, err = reader.Projection(ctx)
	if err != nil || len(projection.HomeConfig.Announcements) != 1 {
		t.Fatalf("post-boundary projection=%#v err=%v", projection, err)
	}
	var delayedErr error
	switch operation {
	case "complete":
		delayedErr = svc.Complete(ctx, PublicRenderTransitionInput{JobGUID: first.JobGUID, OwnerToken: first.OwnerToken, Fence: first.Fence})
	case "fail":
		delayedErr = svc.Fail(ctx, PublicRenderTransitionInput{JobGUID: first.JobGUID, OwnerToken: first.OwnerToken, Fence: first.Fence, Failure: "render_failed"})
	default:
		t.Fatalf("unknown operation %q", operation)
	}
	if delayedErr != ErrPublicRenderLeaseLost {
		t.Fatalf("delayed first %s=%v want lease lost", operation, delayedErr)
	}
	var active models.PublicRenderJob
	if err = db.Where("guid=?", second.JobGUID).First(&active).Error; err != nil || active.State != models.PublicRenderJobLeased || active.AttemptCount != second.Fence || active.LeaseOwnerHMAC == nil || *active.LeaseOwnerHMAC != svc.ownerHMAC(second.OwnerToken) {
		t.Fatalf("delayed first %s mutated active second attempt=%#v err=%v", operation, active, err)
	}
	if err = svc.Complete(ctx, PublicRenderTransitionInput{JobGUID: second.JobGUID, OwnerToken: second.OwnerToken, Fence: second.Fence}); err != nil {
		t.Fatal(err)
	}
	clock.millis++
	again, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "cross-boundary-render-owner", LeaseMillis: 30_000})
	if err != nil || again != nil {
		t.Fatalf("duplicate due-boundary lease=%#v err=%v", again, err)
	}
	var releasesAfter, jobsAfter int64
	if err = db.Model(&models.PublicContentRelease{}).Count(&releasesAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&models.PublicRenderJob{}).Count(&jobsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if releasesAfter != releasesBefore || jobsAfter != jobsBefore {
		t.Fatalf("boundary tick created rows releases=%d/%d jobs=%d/%d", releasesBefore, releasesAfter, jobsBefore, jobsAfter)
	}
}

func pointerInt64(value int64) *int64 { return &value }

func TestPublicRenderJobFixtureEffectiveAnnouncementRequeuesExistingGenerationOnce(t *testing.T) {
	db := openTestMySQL(t)
	ctx := context.Background()
	effective := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	job := seedStructuredPublicRenderJobFixture(t, db, effective)
	completedBefore := effective.Add(-time.Second).UnixMilli()
	if err := db.Model(&models.PublicRenderJob{}).Where("id=?", job.ID).Updates(map[string]any{"state": models.PublicRenderJobSucceeded, "completed_at": completedBefore, "updated_at": completedBefore}).Error; err != nil {
		t.Fatal(err)
	}
	var releasesBefore, jobsBefore int64
	if err := db.Model(&models.PublicContentRelease{}).Count(&releasesBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.PublicRenderJob{}).Count(&jobsBefore).Error; err != nil {
		t.Fatal(err)
	}
	clock := &fakePublicRenderClock{millis: effective.UnixMilli()}
	svc := NewPublicRenderJobServiceWithClock(db, []byte("fixture-render-job-purpose-key-32"), clock)
	lease, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "effective-announcement-render-owner", LeaseMillis: 30_000})
	if err != nil || lease == nil || lease.JobGUID != job.Guid || lease.Fence != 1 {
		t.Fatalf("lease=%#v err=%v", lease, err)
	}
	if err = svc.Complete(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence}); err != nil {
		t.Fatal(err)
	}
	clock.millis++
	again, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "effective-announcement-render-owner", LeaseMillis: 30_000})
	if err != nil || again != nil {
		t.Fatalf("duplicate due render=%#v err=%v", again, err)
	}
	var releasesAfter, jobsAfter int64
	if err = db.Model(&models.PublicContentRelease{}).Count(&releasesAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&models.PublicRenderJob{}).Count(&jobsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if releasesAfter != releasesBefore || jobsAfter != jobsBefore {
		t.Fatalf("due tick created rows releases=%d/%d jobs=%d/%d", releasesBefore, releasesAfter, jobsBefore, jobsAfter)
	}
}

func TestPublicRenderJobFixtureFailureDeduplicatesAlertAndCompleteResolves(t *testing.T) {
	db := openTestMySQL(t)
	ctx := context.Background()
	fixture := seedPublicRenderJobFixture(t, db)
	now := int64(1_900_000_400_000)
	clock := &fakePublicRenderClock{millis: now}
	svc := NewPublicRenderJobServiceWithClock(db, []byte("fixture-render-job-purpose-key-32"), clock)
	lease, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "renderer-alert-owner-token", LeaseMillis: 30_000})
	if err != nil || lease == nil {
		t.Fatalf("lease=%#v err=%v", lease, err)
	}
	if err = svc.Fail(ctx, PublicRenderTransitionInput{JobGUID: lease.JobGUID, OwnerToken: lease.OwnerToken, Fence: lease.Fence, Failure: "supersecretpassword"}); err != nil {
		t.Fatal(err)
	}
	var failedJob models.PublicRenderJob
	if err = db.Where("guid=?", lease.JobGUID).First(&failedJob).Error; err != nil || failedJob.LastFailure == nil || *failedJob.LastFailure != "render_failed" {
		t.Fatalf("sanitized failed job=%#v err=%v", failedJob, err)
	}
	fingerprint := rootAlertFingerprint(models.RootAlertTypeRendererFailure, "", publicRenderAlertIdentity(lease.JobGUID))
	var first models.RootAlert
	if err = db.Where("fingerprint=? AND is_deleted=0", fingerprint).First(&first).Error; err != nil || first.State != models.RootAlertStateActive || first.OccurrenceCount != 1 {
		t.Fatalf("first alert=%#v err=%v", first, err)
	}
	if got := first.Payload["error_code"]; got != "render_failed" {
		t.Fatalf("sanitized renderer alert error_code=%#v", got)
	}
	health, err := svc.Health(ctx)
	if err != nil || health.FailureCode != "render_failed" {
		t.Fatalf("sanitized public render health=%#v err=%v", health, err)
	}
	clock.millis = now + 5_001
	second, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "renderer-alert-owner-token", LeaseMillis: 30_000})
	if err != nil || second == nil || second.Fence != 2 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if err = svc.Fail(ctx, PublicRenderTransitionInput{JobGUID: second.JobGUID, OwnerToken: second.OwnerToken, Fence: second.Fence, Failure: "render_failed"}); err != nil {
		t.Fatal(err)
	}
	var repeated models.RootAlert
	if err = db.Where("fingerprint=? AND is_deleted=0", fingerprint).First(&repeated).Error; err != nil || repeated.ID != first.ID || repeated.OccurrenceCount != 2 {
		t.Fatalf("repeated alert=%#v first=%#v err=%v", repeated, first, err)
	}
	clock.millis = now + 15_002
	third, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: "renderer-alert-owner-token", LeaseMillis: 30_000})
	if err != nil || third == nil || third.Fence != 3 {
		t.Fatalf("third=%#v err=%v", third, err)
	}
	if err = svc.Complete(ctx, PublicRenderTransitionInput{JobGUID: third.JobGUID, OwnerToken: third.OwnerToken, Fence: third.Fence}); err != nil {
		t.Fatal(err)
	}
	var resolved models.RootAlert
	if err = db.Where("id=?", first.ID).First(&resolved).Error; err != nil || resolved.State != models.RootAlertStateResolved || resolved.ResolvedAt == nil {
		t.Fatalf("resolved alert=%#v err=%v", resolved, err)
	}
	_ = fixture
}

func TestPublicRenderFailureSanitizationAndBackoff(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "generic renderer failure", raw: "render_failed", want: "render_failed"},
		{name: "validated payload failure", raw: "validation_failed", want: "validation_failed"},
		{name: "obsolete generation", raw: "obsolete_generation", want: "obsolete_generation"},
		{name: "password shaped value", raw: "supersecretpassword", want: "render_failed"},
		{name: "secret label", raw: "database_secret", want: "render_failed"},
		{name: "token label", raw: "token", want: "render_failed"},
		{name: "encoded secret", raw: "c3VwZXJzZWNyZXRwYXNzd29yZA", want: "render_failed"},
		{name: "unknown code", raw: "template_parse_failed", want: "render_failed"},
		{name: "raw renderer output", raw: "render failed at /var/www/private: Authorization: Bearer super-secret\nraw stderr", want: "render_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizePublicRenderFailure(tc.raw); got != tc.want {
				t.Fatalf("sanitized persistence failure = %q, want %q", got, tc.want)
			}
			if got := sanitizeHealthFailure(tc.raw); got != tc.want {
				t.Fatalf("sanitized anonymous health failure = %q, want %q", got, tc.want)
			}
		})
	}
	if got := sanitizeHealthFailure(""); got != "" {
		t.Fatalf("empty healthy failure = %q", got)
	}
	if d := publicRenderRetryDelay(1); d != 5*time.Second {
		t.Fatalf("first retry = %s", d)
	}
	if d := publicRenderRetryDelay(publicRenderMaxAttempts); d != 0 {
		t.Fatalf("terminal retry = %s", d)
	}
}

func TestPublicRenderTransitionsLockPublicationStateBeforeRenderJob(t *testing.T) {
	source, err := os.ReadFile("public_render_job.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, tc := range []struct {
		name  string
		start string
		end   string
	}{
		{name: "complete", start: "func (s *PublicRenderJobService) Complete", end: "func (s *PublicRenderJobService) Fail"},
		{name: "renew and fail transition", start: "func (s *PublicRenderJobService) transitionCurrent", end: "func publicRenderAlertIdentity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := strings.Index(text, tc.start)
			end := strings.Index(text, tc.end)
			if start < 0 || end <= start {
				t.Fatalf("cannot locate transaction body %q", tc.name)
			}
			body := text[start:end]
			stateLock := strings.Index(body, "lockPublicRenderState(tx)")
			jobLock := strings.Index(body, "Clauses(clause.Locking{Strength: \"UPDATE\"}).Where(\"guid=? AND is_deleted=0\"")
			if stateLock < 0 || jobLock < 0 || stateLock > jobLock {
				t.Fatalf("%s must lock publication state before render job", tc.name)
			}
		})
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

func TestPublicRenderJobFixtureLeaseAndRenewUseConsistentLockOrder(t *testing.T) {
	db := openTestMySQL(t)
	fixture := seedPublicRenderJobFixture(t, db)
	var job models.PublicRenderJob
	if err := db.Where("guid=?", fixture.generation).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	clock := &fakePublicRenderClock{}
	svc := NewPublicRenderJobServiceWithClock(db, []byte("fixture-render-job-purpose-key-32"), clock)
	oldOwner := "lock-order-old-owner-token"
	newOwner := "lock-order-new-owner-token"

	for iteration := 0; iteration < 12; iteration++ {
		now := int64(1_900_000_230_000 + iteration*100)
		clock.millis = now
		if err := db.Model(&models.PublicRenderJob{}).Where("id=?", job.ID).Updates(map[string]any{
			"state":                    models.PublicRenderJobLeased,
			"attempt_count":            1,
			"lease_owner_hmac":         svc.ownerHMAC(oldOwner),
			"lease_expires_at":         now,
			"last_failure":             nil,
			"last_terminal_owner_hmac": nil,
			"last_terminal_fence":      nil,
			"last_terminal_operation":  nil,
			"last_terminal_state":      nil,
			"completed_at":             nil,
		}).Error; err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := make(chan struct{})
		leaseResult := make(chan *PublicRenderLease, 1)
		leaseError := make(chan error, 1)
		renewError := make(chan error, 1)
		go func() {
			<-start
			lease, err := svc.Lease(ctx, PublicRenderLeaseInput{OwnerToken: newOwner, LeaseMillis: 30_000})
			leaseResult <- lease
			leaseError <- err
		}()
		go func() {
			<-start
			renewError <- svc.Renew(ctx, PublicRenderTransitionInput{JobGUID: job.Guid, OwnerToken: oldOwner, Fence: 1, LeaseMillis: 30_000})
		}()
		close(start)
		lease, leaseErr := <-leaseResult, <-leaseError
		renewErr := <-renewError
		cancel()
		if leaseErr != nil || lease == nil || lease.Fence != 2 {
			t.Fatalf("iteration %d lease=%#v err=%v", iteration, lease, leaseErr)
		}
		if renewErr != ErrPublicRenderLeaseLost {
			t.Fatalf("iteration %d expired renew=%v", iteration, renewErr)
		}
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
		db.Exec("DELETE FROM root_alerts WHERE fingerprint=?", rootAlertFingerprint(models.RootAlertTypeRendererFailure, "", publicRenderAlertIdentity(job.Guid)))
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

func seedStructuredPublicRenderJobFixture(t *testing.T, db *gorm.DB, effective time.Time) models.PublicRenderJob {
	t.Helper()
	now := effective.Add(-time.Hour).UnixMilli()
	price := models.PublicPriceSnapshot{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now, Version: 911, Reason: models.PublicPriceSnapshotReasonRootPublish, SourceRevision: 1, PublishedAt: now}
	var err error
	price.ContentHash, err = hashPublicPriceSnapshotItems(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	effectiveText := effective.UTC().Format(time.RFC3339)
	prepared, issues := preparePublicContent(
		PublicContentDraft{Revision: 1, Home: "legacy", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true},
		PublicHomeDraft{Revision: 1, Announcements: []PublicHomeAnnouncementDraft{{GUID: "101", Title: "scheduled", BodyMarkdown: "ready", EffectiveAt: &effectiveText, IsVisible: true}}},
		price,
		nil,
	)
	if len(issues) != 0 {
		t.Fatalf("structured fixture issues=%+v", issues)
	}
	content := models.PublicContentRelease{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now, DocumentKind: models.PublicContentDocumentSite, Version: 912, SourceRevision: 1, Payload: prepared.Payload, ContentHash: prepared.Hash, PublishedAt: now}
	if err := db.Create(&content).Error; err != nil {
		t.Fatal(err)
	}
	var state models.PublicPublicationState
	createdState := false
	if err := db.Where("state_key=?", publicPublicationStateKey).First(&state).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal(err)
		}
		state = models.PublicPublicationState{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now}, StateKey: publicPublicationStateKey, PriceVisibility: models.PublicPriceVisibilityVisible, Revision: 913}
		if err := db.Create(&state).Error; err != nil {
			t.Fatal(err)
		}
		createdState = true
	}
	previousState := state
	if err := db.Model(&state).Updates(map[string]any{"price_snapshot_id": price.ID, "content_release_id": content.ID, "revision": int64(913), "is_deleted": 0, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	job := models.PublicRenderJob{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now}, PriceSnapshotID: price.ID, ContentReleaseID: content.ID, State: models.PublicRenderJobQueued}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM root_alerts WHERE fingerprint=?", rootAlertFingerprint(models.RootAlertTypeRendererFailure, "", publicRenderAlertIdentity(job.Guid)))
		db.Exec("DELETE FROM public_render_jobs WHERE id=?", job.ID)
		if createdState {
			db.Exec("DELETE FROM public_publication_state WHERE id=?", state.ID)
		} else {
			db.Model(&models.PublicPublicationState{}).Where("id=?", state.ID).Updates(map[string]any{"price_snapshot_id": previousState.PriceSnapshotID, "content_release_id": previousState.ContentReleaseID, "revision": previousState.Revision, "is_deleted": previousState.IsDeleted, "updated_at": previousState.UpdatedAt})
		}
		db.Exec("DELETE FROM public_content_releases WHERE id=?", content.ID)
		db.Exec("DELETE FROM public_price_snapshots WHERE id=?", price.ID)
	})
	return job
}
