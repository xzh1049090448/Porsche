package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

type monitorCatalogFake struct {
	observation whitelabel.CatalogObservation
	err         error
}

type blockingMonitorCatalog struct {
	started     chan struct{}
	release     chan struct{}
	observation whitelabel.CatalogObservation
}

func (f *blockingMonitorCatalog) ObserveCatalog(ctx context.Context) (whitelabel.CatalogObservation, error) {
	close(f.started)
	select {
	case <-ctx.Done():
		return whitelabel.CatalogObservation{}, ctx.Err()
	case <-f.release:
		return f.observation, nil
	}
}

func (f *monitorCatalogFake) ObserveCatalog(context.Context) (whitelabel.CatalogObservation, error) {
	return f.observation, f.err
}

func TestUpstreamPriceMonitorDBThreeStrikesDurableAlertsSafetyRetryReappearanceAndRetention(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openTask8MonitorDBFixture(t)
	ctx := context.Background()
	cleanMonitorFixture(t, f.db)
	t.Cleanup(func() { cleanMonitorFixture(t, f.db) })
	if err := requirePublicPriceSnapshotFixture(f.db, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	admin := NewPublicModelAdminService(f.db)
	makeActive := func(label string) PublicModelAdmin {
		in := f.input("monitor-" + label)
		f.observe(t, in.UpstreamModelID)
		created, e := admin.Create(ctx, f.actor.ID, in)
		if e != nil {
			t.Fatal(e)
		}
		active, e := admin.Activate(ctx, f.actor.ID, mustGUID(t, created.GUID), 1)
		if e != nil {
			t.Fatal(e)
		}
		return *active
	}
	missing, keeper, manual := makeActive("missing"), makeActive("keeper"), makeActive("manual")
	manualRow, err := admin.Deactivate(ctx, f.actor.ID, mustGUID(t, manual.GUID), DeactivationRequest{ExpectedRevision: 2, Reason: "manual review"})
	if err != nil || manualRow.Status != "inactive" {
		t.Fatalf("manual inactive=%#v err=%v", manualRow, err)
	}
	draft := publicPriceDraftRevision(t, f.db)
	if _, err := NewPublicPriceSnapshotService(f.db).Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: draft, IdempotencyKey: "monitor-initial"}); err != nil {
		t.Fatal(err)
	}
	missingID := publicModelIDByGUID(t, f.db, missing.GUID)
	keeperID := publicModelIDByGUID(t, f.db, keeper.GUID)
	base := int64(1_900_000_000_000)
	price := "2.00000000"
	catalog := &monitorCatalogFake{}
	alerts := NewRootAlertService(f.db)
	alerts.now = func() int64 { return base }
	alerts.nextGUID = persistence.NextGUID
	m := NewUpstreamPriceMonitor(f.db, catalog, alerts)
	m.now = func() int64 { return base }
	m.nextGUID = persistence.NextGUID
	m.renewTicker = quietMonitorTicker
	setCatalog := func(at int64, complete, fresh bool, ids ...string) {
		modelsOut := make([]whitelabel.CatalogObservedModel, 0, len(ids))
		for _, id := range ids {
			modelsOut = append(modelsOut, whitelabel.CatalogObservedModel{NormalizedID: id, Provider: "provider", InputPriceUSDPerMillionTokens: &price, OutputPriceUSDPerMillionTokens: &price})
		}
		catalog.observation = whitelabel.CatalogObservation{Models: modelsOut, FetchedAt: time.UnixMilli(at), Successful: true, Complete: complete, Fresh: fresh}
	}
	var missingRow models.PublicModelConfig
	if err := f.db.First(&missingRow, missingID).Error; err != nil {
		t.Fatal(err)
	}
	catalog.err = errors.New("upstream unavailable")
	base += 300_000
	if err := m.Tick(ctx); err == nil {
		t.Fatal("catalog error not returned")
	}
	catalog.err = nil
	setCatalog(base+300_000, true, true, missing.UpstreamModelID, keeper.UpstreamModelID, manual.UpstreamModelID)
	var observationsBeforeRecovery int64
	f.db.Model(&models.UpstreamModelObservation{}).Count(&observationsBeforeRecovery)
	alerts.fail = func(point string) error {
		if point == "resolve.audit" {
			return errors.New("injected catalog recovery resolution failure")
		}
		return nil
	}
	base += 300_000
	if err := m.Tick(ctx); err == nil {
		t.Fatal("catalog recovery resolution failure not returned")
	}
	var observationsAfterFailedRecovery int64
	f.db.Model(&models.UpstreamModelObservation{}).Count(&observationsAfterFailedRecovery)
	if observationsAfterFailedRecovery != observationsBeforeRecovery {
		t.Fatalf("failed catalog recovery committed observations %d -> %d", observationsBeforeRecovery, observationsAfterFailedRecovery)
	}
	var catalogFailure models.RootAlert
	if err := f.db.Where("fingerprint=?", rootAlertFingerprint(models.RootAlertTypeCatalogSyncFailure, "", "catalog")).First(&catalogFailure).Error; err != nil || catalogFailure.State != models.RootAlertStateActive {
		t.Fatalf("catalog failure lost on recovery rollback %#v %v", catalogFailure, err)
	}
	alerts.fail = func(string) error { return nil }
	base += 300_000
	setCatalog(base, true, true, missing.UpstreamModelID, keeper.UpstreamModelID, manual.UpstreamModelID)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("id=?", catalogFailure.ID).First(&catalogFailure).Error; err != nil || catalogFailure.State != models.RootAlertStateResolved {
		t.Fatalf("catalog recovery unresolved %#v %v", catalogFailure, err)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var resolvedAudits int64
	f.db.Model(&models.AuditLog{}).Where("action=? AND resource=?", "root_alert.resolved", "root-alerts/"+fmt.Sprint(catalogFailure.Guid)).Count(&resolvedAudits)
	if resolvedAudits != 1 {
		t.Fatalf("catalog recovery resolution audits=%d", resolvedAudits)
	}
	base += 300_000
	higher := "3.00000000"
	catalog.observation = whitelabel.CatalogObservation{Models: []whitelabel.CatalogObservedModel{{NormalizedID: missing.UpstreamModelID, Provider: "provider", InputPriceUSDPerMillionTokens: &price, OutputPriceUSDPerMillionTokens: &price}, {NormalizedID: keeper.UpstreamModelID, Provider: "provider", InputPriceUSDPerMillionTokens: &higher, OutputPriceUSDPerMillionTokens: &price}, {NormalizedID: manual.UpstreamModelID, Provider: "provider", InputPriceUSDPerMillionTokens: &price, OutputPriceUSDPerMillionTokens: &price}}, FetchedAt: time.UnixMilli(base), Successful: true, Complete: true, Fresh: true}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var inputBelow, outputBelow int64
	f.db.Model(&models.RootAlert{}).Where("model_config_id=? AND alert_type=? AND fingerprint=?", keeperID, models.RootAlertTypePublishedPriceBelowUpstream, rootAlertFingerprint(models.RootAlertTypePublishedPriceBelowUpstream, keeper.ModelKey, "input")).Count(&inputBelow)
	f.db.Model(&models.RootAlert{}).Where("model_config_id=? AND alert_type=? AND fingerprint=?", keeperID, models.RootAlertTypePublishedPriceBelowUpstream, rootAlertFingerprint(models.RootAlertTypePublishedPriceBelowUpstream, keeper.ModelKey, "output")).Count(&outputBelow)
	if inputBelow != 1 || outputBelow != 0 {
		t.Fatalf("component alerts input=%d output=%d", inputBelow, outputBelow)
	}
	base += 300_000
	catalog.observation = whitelabel.CatalogObservation{Models: []whitelabel.CatalogObservedModel{{NormalizedID: missing.UpstreamModelID, Provider: "provider", InputPriceUSDPerMillionTokens: &price, OutputPriceUSDPerMillionTokens: &price}, {NormalizedID: keeper.UpstreamModelID, Provider: "provider", OutputPriceUSDPerMillionTokens: &price}}, FetchedAt: time.UnixMilli(base), Successful: true, Complete: true, Fresh: true}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var nonComparable int64
	f.db.Model(&models.RootAlert{}).Where("model_config_id=? AND alert_type=?", keeperID, models.RootAlertTypePriceNotComparable).Count(&nonComparable)
	if nonComparable != 1 {
		t.Fatalf("not comparable alerts=%d", nonComparable)
	}
	for _, flags := range [][2]bool{{false, true}, {true, false}} {
		base += 300_000
		setCatalog(base, flags[0], flags[1], keeper.UpstreamModelID)
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.First(&missingRow, missingID).Error; err != nil || missingRow.ConsecutiveAbsences != 0 {
		t.Fatalf("invalid catalogs struck %#v %v", missingRow, err)
	}
	for strike := 1; strike <= 2; strike++ {
		base += 300_000
		setCatalog(base, true, true, keeper.UpstreamModelID)
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if err := f.db.First(&missingRow, missingID).Error; err != nil || missingRow.ConsecutiveAbsences != strike || missingRow.Status != models.PublicModelConfigStatusActive {
			t.Fatalf("strike %d row=%#v err=%v", strike, missingRow, err)
		}
		if strike == 1 {
			if err := m.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.db.First(&missingRow, missingID).Error; err != nil || missingRow.ConsecutiveAbsences != 1 {
				t.Fatalf("repeat tick incremented %#v %v", missingRow, err)
			}
		}
	}
	var pointerBefore models.PublicPublicationState
	if err := f.db.Where("state_key=?", publicPublicationStateKey).First(&pointerBefore).Error; err != nil {
		t.Fatal(err)
	}
	draftBeforeInactivation := publicPriceDraftRevision(t, f.db)
	m.fail = func(point string) error {
		if point == "safety_before_pointer" {
			return errors.New("injected safety failure")
		}
		return nil
	}
	base += 300_000
	setCatalog(base, true, true, keeper.UpstreamModelID)
	if err := m.Tick(ctx); err == nil {
		t.Fatal("safety failure not returned")
	}
	if err := f.db.First(&missingRow, missingID).Error; err != nil || missingRow.Status != models.PublicModelConfigStatusInactive || missingRow.ConsecutiveAbsences != 3 || missingRow.InactiveReason == nil || *missingRow.InactiveReason != "upstream_removed" {
		t.Fatalf("inactive row=%#v err=%v", missingRow, err)
	}
	if got := publicPriceDraftRevision(t, f.db); got != draftBeforeInactivation+1 {
		t.Fatalf("inactivation draft revision=%d want=%d", got, draftBeforeInactivation+1)
	}
	var pointerAfterFailure models.PublicPublicationState
	if err := f.db.Where("state_key=?", publicPublicationStateKey).First(&pointerAfterFailure).Error; err != nil || pointerAfterFailure.PriceSnapshotID == nil || *pointerAfterFailure.PriceSnapshotID != *pointerBefore.PriceSnapshotID {
		t.Fatalf("pointer changed on safety failure %#v %v", pointerAfterFailure, err)
	}
	projection, projectionErr := NewPublicPriceSnapshotService(f.db).CurrentActiveProjection(ctx)
	if projectionErr != nil {
		t.Fatal(projectionErr)
	}
	for _, item := range projection {
		if item.ModelKey == missing.ModelKey {
			t.Fatal("dynamic projection exposed inactive model")
		}
	}
	if stale, e := NewPublicContentService(f.db).PublicProjection(ctx); status(e) != 503 || stale != nil {
		t.Fatalf("stale public projection=%#v err=%v", stale, e)
	}
	var autoAlerts, pendingAlerts, audits int64
	f.db.Model(&models.RootAlert{}).Where("model_config_id=? AND alert_type=? AND state=?", missingID, models.RootAlertTypeAutomaticInactivation, models.RootAlertStateActive).Count(&autoAlerts)
	f.db.Model(&models.RootAlert{}).Where("alert_type=? AND fingerprint=? AND state=?", models.RootAlertTypeCatalogSyncFailure, rootAlertFingerprint(models.RootAlertTypeCatalogSyncFailure, "", "safety"), models.RootAlertStateActive).Count(&pendingAlerts)
	f.db.Model(&models.AuditLog{}).Where("action=? AND resource=?", "public_models.upstream_auto_inactivate", "public-models/"+missing.ModelKey).Count(&audits)
	if autoAlerts != 1 || pendingAlerts != 1 || audits != 1 {
		t.Fatalf("durability auto=%d safety=%d audit=%d", autoAlerts, pendingAlerts, audits)
	}
	m.fail = func(string) error { return nil }
	alerts.fail = func(point string) error {
		if point == "resolve.audit" {
			return errors.New("injected safety resolution failure")
		}
		return nil
	}
	var snapshotsBeforeResolutionFailure, releasesBeforeResolutionFailure, jobsBeforeResolutionFailure int64
	f.db.Model(&models.PublicPriceSnapshot{}).Count(&snapshotsBeforeResolutionFailure)
	f.db.Model(&models.PublicContentRelease{}).Count(&releasesBeforeResolutionFailure)
	f.db.Model(&models.PublicRenderJob{}).Count(&jobsBeforeResolutionFailure)
	base += 300_000
	setCatalog(base, true, true, keeper.UpstreamModelID)
	if err := m.Tick(ctx); err == nil {
		t.Fatal("safety alert resolution failure not returned")
	}
	var pointerAfterResolutionFailure models.PublicPublicationState
	if err := f.db.Where("state_key=?", publicPublicationStateKey).First(&pointerAfterResolutionFailure).Error; err != nil || pointerAfterResolutionFailure.PriceSnapshotID == nil || *pointerAfterResolutionFailure.PriceSnapshotID != *pointerBefore.PriceSnapshotID {
		t.Fatalf("pointer changed on resolution failure %#v %v", pointerAfterResolutionFailure, err)
	}
	var snapshotsAfterResolutionFailure, releasesAfterResolutionFailure, jobsAfterResolutionFailure int64
	f.db.Model(&models.PublicPriceSnapshot{}).Count(&snapshotsAfterResolutionFailure)
	f.db.Model(&models.PublicContentRelease{}).Count(&releasesAfterResolutionFailure)
	f.db.Model(&models.PublicRenderJob{}).Count(&jobsAfterResolutionFailure)
	if snapshotsAfterResolutionFailure != snapshotsBeforeResolutionFailure || releasesAfterResolutionFailure != releasesBeforeResolutionFailure || jobsAfterResolutionFailure != jobsBeforeResolutionFailure {
		t.Fatalf("resolution failure left partial publication snapshots=%d/%d releases=%d/%d jobs=%d/%d", snapshotsAfterResolutionFailure, snapshotsBeforeResolutionFailure, releasesAfterResolutionFailure, releasesBeforeResolutionFailure, jobsAfterResolutionFailure, jobsBeforeResolutionFailure)
	}
	var pendingAfterResolutionFailure models.RootAlert
	if err := f.db.Where("fingerprint=?", rootAlertFingerprint(models.RootAlertTypeCatalogSyncFailure, "", "safety")).First(&pendingAfterResolutionFailure).Error; err != nil || pendingAfterResolutionFailure.State != models.RootAlertStateActive {
		t.Fatalf("pending alert lost on resolution failure %#v %v", pendingAfterResolutionFailure, err)
	}
	alerts.fail = func(string) error { return nil }
	base += 300_000
	setCatalog(base, true, true, keeper.UpstreamModelID)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var pointerAfterRetry models.PublicPublicationState
	if err := f.db.Where("state_key=?", publicPublicationStateKey).First(&pointerAfterRetry).Error; err != nil || pointerAfterRetry.PriceSnapshotID == nil || *pointerAfterRetry.PriceSnapshotID == *pointerBefore.PriceSnapshotID {
		t.Fatalf("safety retry %#v %v", pointerAfterRetry, err)
	}
	var publishedMissing int64
	f.db.Model(&models.PublicPriceSnapshotItem{}).Where("snapshot_id=? AND model_config_id=?", *pointerAfterRetry.PriceSnapshotID, missingID).Count(&publishedMissing)
	if publishedMissing != 0 {
		t.Fatal("inactive model remained in safety snapshot")
	}
	publicProjection, projectionErr := NewPublicContentService(f.db).PublicProjection(ctx)
	if projectionErr != nil || publicProjection == nil || publicProjection.ETag == "" {
		t.Fatalf("compatible public projection=%#v err=%v", publicProjection, projectionErr)
	}
	var resolvedSafety models.RootAlert
	if err := f.db.Where("fingerprint=?", rootAlertFingerprint(models.RootAlertTypeCatalogSyncFailure, "", "safety")).First(&resolvedSafety).Error; err != nil || resolvedSafety.State != models.RootAlertStateResolved {
		t.Fatalf("safety alert unresolved after compatible commit %#v %v", resolvedSafety, err)
	}
	base += 300_000
	setCatalog(base, true, true, missing.UpstreamModelID, keeper.UpstreamModelID)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&missingRow, missingID).Error; err != nil || missingRow.Status != models.PublicModelConfigStatusInactive || missingRow.ConsecutiveAbsences != 0 {
		t.Fatalf("reappearance %#v %v", missingRow, err)
	}
	var reappearance int64
	f.db.Model(&models.RootAlert{}).Where("model_config_id=? AND alert_type=?", missingID, models.RootAlertTypeUpstreamReappearance).Count(&reappearance)
	if reappearance != 1 {
		t.Fatalf("reappearance alerts=%d", reappearance)
	}
	base += 300_000
	setCatalog(base, true, true, missing.UpstreamModelID, keeper.UpstreamModelID, manual.UpstreamModelID)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var manualAlerts int64
	f.db.Model(&models.RootAlert{}).Where("model_config_id=? AND alert_type=?", publicModelIDByGUID(t, f.db, manual.GUID), models.RootAlertTypeUpstreamReappearance).Count(&manualAlerts)
	if manualAlerts != 0 {
		t.Fatalf("manual inactive reappearance alerted=%d", manualAlerts)
	}
	base += 300_000
	setCatalog(base, true, true, missing.UpstreamModelID, keeper.UpstreamModelID)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	f.db.Model(&models.RootAlert{}).Where("model_config_id=? AND alert_type=?", missingID, models.RootAlertTypeUpstreamReappearance).Count(&reappearance)
	if reappearance != 1 {
		t.Fatalf("reappearance repeated=%d", reappearance)
	}
	tickAt := base + 1
	oldAt, boundary := tickAt-upstreamObservationRetention.Milliseconds()-1, tickAt-upstreamObservationRetention.Milliseconds()
	for i, at := range []int64{oldAt, boundary} {
		row := models.UpstreamModelObservation{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: at, UpdatedAt: at}, UpstreamModelID: fmt.Sprintf("retention/%d", i), Provider: "provider", CatalogComplete: 1, CatalogFresh: 1, ObservedAt: at, ResponseSummaryHash: strings.Repeat("a", 64)}
		if err := f.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	base = tickAt
	setCatalog(base, true, true, missing.UpstreamModelID, keeper.UpstreamModelID)
	m.fail = func(point string) error {
		if point == "before_commit" {
			return errors.New("injected retention rollback")
		}
		return nil
	}
	if err := m.Tick(ctx); err == nil {
		t.Fatal("retention rollback injection not returned")
	}
	var rolledBackOld int64
	f.db.Model(&models.UpstreamModelObservation{}).Where("upstream_model_id=?", "retention/0").Count(&rolledBackOld)
	if rolledBackOld != 1 {
		t.Fatalf("retention delete escaped rollback=%d", rolledBackOld)
	}
	m.fail = func(string) error { return nil }
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var oldCount, boundaryCount int64
	f.db.Model(&models.UpstreamModelObservation{}).Where("upstream_model_id=?", "retention/0").Count(&oldCount)
	f.db.Model(&models.UpstreamModelObservation{}).Where("upstream_model_id=?", "retention/1").Count(&boundaryCount)
	if oldCount != 0 || boundaryCount != 1 {
		t.Fatalf("retention old=%d boundary=%d", oldCount, boundaryCount)
	}
	var keeperRow models.PublicModelConfig
	if err := f.db.First(&keeperRow, keeperID).Error; err != nil {
		t.Fatal(err)
	}
}

func quietMonitorTicker(time.Duration) (<-chan time.Time, func()) {
	return make(chan time.Time), func() {}
}

func cleanMonitorFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	statements := []string{"DELETE FROM root_alert_receipts", "DELETE FROM root_alerts", "DELETE FROM public_render_jobs", "DELETE FROM public_publication_state", "UPDATE public_content_releases SET restored_from_release_id=NULL", "DELETE FROM public_content_releases", "DELETE FROM public_content_drafts", "DELETE FROM public_price_snapshot_items", "UPDATE public_price_snapshots SET restored_from_snapshot_id=NULL", "DELETE FROM public_price_snapshots", "DELETE FROM audit_logs WHERE action LIKE 'public_%' OR action LIKE 'root_alert.%'", "DELETE FROM upstream_model_observations", "DELETE FROM public_model_configs", "UPDATE public_price_draft_state SET revision=1,updated_by=NULL WHERE state_key='pricing'", "UPDATE upstream_monitor_leases SET owner_token=NULL,lease_expires_at=0 WHERE lease_key='catalog'"}
	for _, sql := range statements {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("cleanup %q: %v", sql, err)
		}
	}
}

func TestUpstreamPriceMonitorLeaseOneOwnerAndExpiry(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openTask8MonitorDBFixture(t)
	if !f.db.Migrator().HasTable("upstream_monitor_leases") {
		t.Fatal("BLOCKED_FIXTURE: migration missing upstream_monitor_leases")
	}
	now := int64(1_900_000_000_000)
	if err := f.db.Model(&models.UpstreamMonitorLease{}).Where("lease_key=?", "catalog").Updates(map[string]any{"owner_token": nil, "lease_expires_at": 0, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.db.Model(&models.UpstreamMonitorLease{}).Where("lease_key=?", "catalog").Updates(map[string]any{"owner_token": nil, "lease_expires_at": 0}).Error
	})
	monitors := []*UpstreamPriceMonitor{{db: f.db, now: func() int64 { return now }, random: bytes.NewReader(bytes.Repeat([]byte{1}, 32))}, {db: f.db, now: func() int64 { return now }, random: bytes.NewReader(bytes.Repeat([]byte{2}, 32))}}
	start := make(chan struct{})
	owners := make(chan string, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, m := range monitors {
		wg.Add(1)
		go func(m *UpstreamPriceMonitor) {
			defer wg.Done()
			<-start
			o, e := m.acquireLease(context.Background())
			owners <- o
			errs <- e
		}(m)
	}
	close(start)
	wg.Wait()
	close(owners)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	won := 0
	var owner string
	for o := range owners {
		if o != "" {
			won++
			owner = o
		}
	}
	if won != 1 {
		t.Fatalf("owners won=%d", won)
	}
	now += upstreamMonitorLeaseDuration.Milliseconds() + 1
	takeover := &UpstreamPriceMonitor{db: f.db, now: func() int64 { return now }, random: bytes.NewReader(bytes.Repeat([]byte{5}, 32))}
	replacement, err := takeover.acquireLease(context.Background())
	if err != nil || replacement == "" || replacement == owner {
		t.Fatalf("replacement=%q err=%v", replacement, err)
	}
	monitors[0].releaseLease(owner)
	var row models.UpstreamMonitorLease
	if err = f.db.Where("lease_key=?", "catalog").First(&row).Error; err != nil || row.OwnerToken == nil || *row.OwnerToken != replacement {
		t.Fatalf("row=%#v err=%v", row, err)
	}
}

func TestUpstreamPriceMonitorLeaseRenewsAndCancellationStopsBlockedTick(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openTask8MonitorDBFixture(t)
	now := int64(1_900_000_000_000)
	_ = f.db.Model(&models.UpstreamMonitorLease{}).Where("lease_key=?", "catalog").Updates(map[string]any{"owner_token": nil, "lease_expires_at": 0}).Error
	renew := make(chan time.Time, 1)
	source := &blockingMonitorCatalog{started: make(chan struct{}), release: make(chan struct{}), observation: whitelabel.CatalogObservation{Successful: true, Complete: false, Fresh: true, FetchedAt: time.UnixMilli(now)}}
	m := NewUpstreamPriceMonitor(f.db, source, NewRootAlertService(f.db))
	m.now = func() int64 { return now }
	m.random = bytes.NewReader(bytes.Repeat([]byte{3}, 32))
	m.renewTicker = func(time.Duration) (<-chan time.Time, func()) { return renew, func() {} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Tick(ctx) }()
	<-source.started
	var before models.UpstreamMonitorLease
	if err := f.db.Where("lease_key=?", "catalog").First(&before).Error; err != nil {
		t.Fatal(err)
	}
	renew <- time.Now()
	deadline := time.Now().Add(2 * time.Second)
	var after models.UpstreamMonitorLease
	for {
		if err := f.db.Where("lease_key=?", "catalog").First(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after.Revision > before.Revision {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease was not renewed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled tick returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled tick leaked")
	}
	var released models.UpstreamMonitorLease
	if err := f.db.Where("lease_key=?", "catalog").First(&released).Error; err != nil || released.OwnerToken != nil {
		t.Fatalf("released=%#v err=%v", released, err)
	}
}

func TestUpstreamPriceMonitorLeaseLossCancelsWorkAndPreservesNewOwner(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openTask8MonitorDBFixture(t)
	now := int64(1_900_000_000_000)
	_ = f.db.Model(&models.UpstreamMonitorLease{}).Where("lease_key=?", "catalog").Updates(map[string]any{"owner_token": nil, "lease_expires_at": 0}).Error
	renew := make(chan time.Time, 1)
	source := &blockingMonitorCatalog{started: make(chan struct{}), release: make(chan struct{})}
	m := NewUpstreamPriceMonitor(f.db, source, NewRootAlertService(f.db))
	m.now = func() int64 { return now }
	m.random = bytes.NewReader(bytes.Repeat([]byte{4}, 32))
	m.renewTicker = func(time.Duration) (<-chan time.Time, func()) { return renew, func() {} }
	done := make(chan error, 1)
	go func() { done <- m.Tick(context.Background()) }()
	<-source.started
	replacement := strings.Repeat("f", 64)
	if err := f.db.Model(&models.UpstreamMonitorLease{}).Where("lease_key=?", "catalog").Updates(map[string]any{"owner_token": replacement, "lease_expires_at": now + upstreamMonitorLeaseDuration.Milliseconds()}).Error; err != nil {
		t.Fatal(err)
	}
	renew <- time.Now()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lost lease returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lost lease did not cancel work")
	}
	var row models.UpstreamMonitorLease
	if err := f.db.Where("lease_key=?", "catalog").First(&row).Error; err != nil || row.OwnerToken == nil || *row.OwnerToken != replacement {
		t.Fatalf("replacement owner cleared row=%#v err=%v", row, err)
	}
}
