package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

func TestPublicPriceSnapshotDBFirstPublishInitializesFailClosedState(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	f.db = tx
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && err != gorm.ErrInvalidTransaction {
			t.Error(err)
		}
	})
	for _, statement := range []string{
		"DELETE FROM public_render_jobs",
		"DELETE FROM public_publication_state",
		"DELETE FROM public_price_snapshot_items",
		"UPDATE public_price_snapshots SET restored_from_snapshot_id = NULL",
		"DELETE FROM public_price_snapshots",
	} {
		if err := tx.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	model := f.input("first-publish-" + fmt.Sprint(persistence.NextGUID()))
	model.InputPriceUSDPerMillionTokens = nil
	model.OutputPriceUSDPerMillionTokens = nil
	f.observe(t, model.UpstreamModelID)
	admin := NewPublicModelAdminService(tx)
	created, err := admin.Create(context.Background(), f.actor.ID, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Activate(context.Background(), f.actor.ID, mustGUID(t, created.GUID), created.Revision); err != nil {
		t.Fatal(err)
	}
	revision := publicPriceDraftRevision(t, tx)
	request := PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: revision, IdempotencyKey: "fresh-install-first-price"}

	broken := NewPublicPriceSnapshotService(tx)
	broken.fail = func(point string) error {
		if point == "snapshot" {
			return fmt.Errorf("injected snapshot failure")
		}
		return nil
	}
	if _, err = broken.Publish(context.Background(), request); status(err) != 503 {
		t.Fatalf("failed first publish = %v", err)
	}
	var count int64
	if err = tx.Model(&models.PublicPublicationState{}).Where("state_key = ? AND is_deleted = 0", publicPublicationStateKey).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed first publish retained state: count=%d err=%v", count, err)
	}

	service := NewPublicPriceSnapshotService(tx)
	release, err := service.Publish(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Publish(context.Background(), request)
	if err != nil || replay.GUID != release.GUID {
		t.Fatalf("first publish replay=%#v err=%v", replay, err)
	}
	releaseGUID, err := strconv.ParseInt(release.GUID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	var persistedSnapshot models.PublicPriceSnapshot
	var persistedItems []models.PublicPriceSnapshotItem
	if err = tx.Where("guid = ?", releaseGUID).First(&persistedSnapshot).Error; err != nil {
		t.Fatal(err)
	}
	if err = tx.Where("snapshot_id = ?", persistedSnapshot.ID).Order("model_key").Find(&persistedItems).Error; err != nil {
		t.Fatal(err)
	}
	roundTripHash, hashErr := hashPublicPriceSnapshotItems(persistedItems)
	if hashErr != nil || roundTripHash != persistedSnapshot.ContentHash {
		t.Fatalf("snapshot hash changed after DB round trip: stored=%s recalculated=%s err=%v items=%#v", persistedSnapshot.ContentHash, roundTripHash, hashErr, persistedItems)
	}
	view, err := service.GetRelease(context.Background(), releaseGUID)
	if err != nil {
		t.Fatalf("first release round trip=%#v err=%v", view, err)
	}
	foundCreated := false
	for _, item := range view.Items {
		if item.Capabilities == nil || item.EndpointTypes == nil || item.PublicRestrictions == nil {
			t.Fatalf("release returned null collection: %#v", item)
		}
		if item.ModelKey == model.ModelKey {
			foundCreated = true
		}
	}
	if !foundCreated {
		t.Fatalf("first release omitted created model: %#v", view.Items)
	}
	var state models.PublicPublicationState
	if err = tx.Where("state_key = ? AND is_deleted = 0", publicPublicationStateKey).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.PriceVisibility != models.PublicPriceVisibilityAuthenticatedOnly || state.Revision != 2 || state.PriceSnapshotID == nil || state.ContentReleaseID != nil {
		t.Fatalf("first publication state = %#v", state)
	}
	var releases, jobs int64
	if err = tx.Model(&models.PublicContentRelease{}).Where("created_by = ?", f.actor.ID).Count(&releases).Error; err != nil {
		t.Fatal(err)
	}
	if err = tx.Model(&models.PublicRenderJob{}).Where("created_by = ?", f.actor.ID).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if releases != 0 || jobs != 0 {
		t.Fatalf("first price publish fabricated public content: releases=%d jobs=%d", releases, jobs)
	}
}

// The full transactional suite is deliberately opt-in and never reads .env.
func TestPublicPriceSnapshotDBAtomicPublicationIdempotencyAndRestore(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	f.db = tx
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && err != gorm.ErrInvalidTransaction {
			t.Error(err)
		}
	})
	s := NewPublicPriceSnapshotService(f.db)
	ctx := context.Background()
	// A migrated disposable fixture owns setup of publication state/content release.
	if err := requirePublicPriceSnapshotFixture(f.db, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	model := f.input("snapshot-" + fmt.Sprint(persistence.NextGUID()))
	f.observe(t, model.UpstreamModelID)
	created, err := NewPublicModelAdminService(f.db).Create(ctx, f.actor.ID, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewPublicModelAdminService(f.db).Activate(ctx, f.actor.ID, mustGUID(t, created.GUID), 1); err != nil {
		t.Fatal(err)
	}
	model2 := f.input("snapshot-" + fmt.Sprint(persistence.NextGUID()))
	f.observe(t, model2.UpstreamModelID)
	created2, err := NewPublicModelAdminService(f.db).Create(ctx, f.actor.ID, model2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewPublicModelAdminService(f.db).Activate(ctx, f.actor.ID, mustGUID(t, created2.GUID), 1); err != nil {
		t.Fatal(err)
	}
	_, err = loadPublicPriceSnapshotFixtureState(f.db)
	if err != nil {
		t.Fatal(err)
	}
	draftRevision := publicPriceDraftRevision(t, f.db)
	release, err := s.Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: draftRevision, IdempotencyKey: "publish-1"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: draftRevision, IdempotencyKey: "publish-1"})
	if err != nil || replay.GUID != release.GUID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	var snapshot models.PublicPriceSnapshot
	if err = f.db.Where("guid = ?", release.GUID).First(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	var items []models.PublicPriceSnapshotItem
	if err = f.db.Where("snapshot_id = ?", snapshot.ID).Order("model_key").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || publicPriceValue(items[0].InputPriceUSDPerMillionTokens) != "2.00000000" || publicPriceValue(items[0].OutputPriceUSDPerMillionTokens) != "2.00000000" {
		t.Fatalf("snapshot items=%#v", items)
	}
	assertPublicModelEverPublished(t, f.db, publicModelIDByGUID(t, f.db, created.GUID), true)
	assertPublicModelEverPublished(t, f.db, publicModelIDByGUID(t, f.db, created2.GUID), true)
	name := "Changed after draft read"
	if _, err = NewPublicModelAdminService(f.db).Update(ctx, f.actor.ID, mustGUID(t, created.GUID), UpdatePublicModelRequest{ExpectedRevision: 2, DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	newDraftRevision := publicPriceDraftRevision(t, f.db)
	if _, err = s.Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: newDraftRevision, IdempotencyKey: "publish-1"}); status(err) != 409 {
		t.Fatalf("idempotency conflict=%v", err)
	}
	if _, err = s.Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: draftRevision, IdempotencyKey: "stale"}); status(err) != 409 {
		t.Fatalf("stale draft=%v", err)
	}
	model3 := f.input("snapshot-extra-" + fmt.Sprint(persistence.NextGUID()))
	f.observe(t, model3.UpstreamModelID)
	created3, err := NewPublicModelAdminService(f.db).Create(ctx, f.actor.ID, model3)
	if err != nil {
		t.Fatal(err)
	}
	created3, err = NewPublicModelAdminService(f.db).Activate(ctx, f.actor.ID, mustGUID(t, created3.GUID), 1)
	if err != nil {
		t.Fatal(err)
	}
	newDraftRevision = publicPriceDraftRevision(t, f.db)
	assertPublicModelEverPublished(t, f.db, publicModelIDByGUID(t, f.db, created3.GUID), false)
	after, _ := loadPublicPriceSnapshotFixtureState(f.db)
	for _, point := range []string{"snapshot", "ever_published", "item", "content_release", "render_job", "audit", "pointer", "after_pointer"} {
		beforeCounts := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID)
		broken := NewPublicPriceSnapshotService(f.db)
		itemCalls := 0
		broken.fail = func(got string) error {
			if got == "item" {
				itemCalls++
				if point == "item" && itemCalls == 2 {
					return fmt.Errorf("injected %s", point)
				}
			}
			if got == point && point != "item" {
				return fmt.Errorf("injected %s", point)
			}
			return nil
		}
		if _, failureErr := broken.Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: newDraftRevision, IdempotencyKey: "failure-" + point}); status(failureErr) != 503 {
			t.Fatalf("point %s status=%d err=%v", point, status(failureErr), failureErr)
		}
		unchanged, loadErr := loadPublicPriceSnapshotFixtureState(f.db)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if unchanged.Revision != after.Revision || unchanged.PriceSnapshotID == nil || *unchanged.PriceSnapshotID != *after.PriceSnapshotID {
			t.Fatalf("point %s advanced state", point)
		}
		if got := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID); got != beforeCounts {
			t.Fatalf("point %s partial rows before=%v after=%v", point, beforeCounts, got)
		}
		assertPublicModelEverPublished(t, f.db, publicModelIDByGUID(t, f.db, created3.GUID), false)
	}
	secondID := publicModelIDByGUID(t, f.db, created2.GUID)
	if err = f.db.Model(&models.PublicModelConfig{}).Where("id = ?", secondID).Update("ever_published", 0).Error; err != nil {
		t.Fatal(err)
	}
	assertPublicModelEverPublished(t, f.db, secondID, false)
	beforeRestoreRevision := publicPriceDraftRevision(t, f.db)
	brokenRestore := NewPublicPriceSnapshotService(f.db)
	brokenRestore.fail = func(point string) error {
		if point == "after_pointer" {
			return fmt.Errorf("injected restore rollback")
		}
		return nil
	}
	if _, failureErr := brokenRestore.Restore(ctx, PublicPriceSnapshotRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: beforeRestoreRevision, SnapshotGUID: release.GUID, IdempotencyKey: "restore-rollback"}); status(failureErr) != 503 {
		t.Fatalf("restore rollback status=%d err=%v", status(failureErr), failureErr)
	}
	var unchangedModel, unchangedExtra models.PublicModelConfig
	if err = f.db.First(&unchangedModel, publicModelIDByGUID(t, f.db, created.GUID)).Error; err != nil {
		t.Fatal(err)
	}
	if err = f.db.First(&unchangedExtra, publicModelIDByGUID(t, f.db, created3.GUID)).Error; err != nil {
		t.Fatal(err)
	}
	if publicPriceDraftRevision(t, f.db) != beforeRestoreRevision || unchangedModel.DisplayName != name || unchangedExtra.Status != models.PublicModelConfigStatusActive {
		t.Fatalf("failed restore partially materialized draft: model=%#v extra=%#v", unchangedModel, unchangedExtra)
	}
	assertPublicModelEverPublished(t, f.db, unchangedExtra.ID, false)
	assertPublicModelEverPublished(t, f.db, secondID, false)
	restored, err := s.Restore(ctx, PublicPriceSnapshotRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: newDraftRevision, SnapshotGUID: release.GUID, IdempotencyKey: "restore-1"})
	if err != nil || restored.Version <= release.Version || restored.GUID == release.GUID {
		t.Fatalf("restore=%#v err=%v", restored, err)
	}
	var restoredSnapshot models.PublicPriceSnapshot
	if err = f.db.Where("guid=?", restored.GUID).First(&restoredSnapshot).Error; err != nil {
		t.Fatal(err)
	}
	var restoredItems []models.PublicPriceSnapshotItem
	if err = f.db.Where("snapshot_id=?", restoredSnapshot.ID).Order("model_key").Find(&restoredItems).Error; err != nil {
		t.Fatal(err)
	}
	strip := func(values []models.PublicPriceSnapshotItem) {
		for i := range values {
			values[i].ID = 0
			values[i].Guid = 0
			values[i].SnapshotID = 0
			values[i].CreatedAt = 0
			values[i].CreatedBy = nil
			values[i].UpdatedAt = 0
			values[i].UpdatedBy = nil
		}
	}
	strip(items)
	strip(restoredItems)
	if !reflect.DeepEqual(items, restoredItems) || restoredSnapshot.ContentHash != snapshot.ContentHash {
		t.Fatal("restore content/hash mismatch")
	}
	if got := publicPriceDraftRevision(t, f.db); restoredSnapshot.SourceRevision != got {
		t.Fatalf("restore source revision=%d draft revision=%d", restoredSnapshot.SourceRevision, got)
	}
	var restoredModel, restoredExtra models.PublicModelConfig
	if err = f.db.First(&restoredModel, publicModelIDByGUID(t, f.db, created.GUID)).Error; err != nil {
		t.Fatal(err)
	}
	if err = f.db.First(&restoredExtra, publicModelIDByGUID(t, f.db, created3.GUID)).Error; err != nil {
		t.Fatal(err)
	}
	if restoredModel.DisplayName != model.DisplayName {
		t.Fatalf("restore did not materialize historical draft display_name=%q", restoredModel.DisplayName)
	}
	if restoredExtra.Status != models.PublicModelConfigStatusInactive || restoredExtra.IsDeleted != 0 {
		t.Fatalf("restore did not remove extra model from draft: %#v", restoredExtra)
	}
	assertPublicModelEverPublished(t, f.db, publicModelIDByGUID(t, f.db, created.GUID), true)
	assertPublicModelEverPublished(t, f.db, secondID, true)
	assertPublicModelEverPublished(t, f.db, publicModelIDByGUID(t, f.db, created3.GUID), false)

	admin := NewPublicModelAdminService(f.db)
	deactivated, err := admin.Deactivate(ctx, f.actor.ID, mustGUID(t, created.GUID), DeactivationRequest{ExpectedRevision: restoredModel.Revision, Reason: "restore lifecycle test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Restore(ctx, PublicPriceSnapshotRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: publicPriceDraftRevision(t, f.db), SnapshotGUID: restored.GUID, IdempotencyKey: "restore-inactive"}); status(err) != 409 {
		t.Fatalf("inactive historical identity restore=%v", err)
	}
	if _, err = admin.Activate(ctx, f.actor.ID, mustGUID(t, created.GUID), deactivated.Revision); err != nil {
		t.Fatal(err)
	}
	var secondCurrent models.PublicModelConfig
	if err = f.db.First(&secondCurrent, publicModelIDByGUID(t, f.db, created2.GUID)).Error; err != nil {
		t.Fatal(err)
	}
	if err = admin.Delete(ctx, f.actor.ID, mustGUID(t, created2.GUID), DeletePublicModelRequest{ExpectedRevision: secondCurrent.Revision, Reason: "restore lifecycle test"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Restore(ctx, PublicPriceSnapshotRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: publicPriceDraftRevision(t, f.db), SnapshotGUID: restored.GUID, IdempotencyKey: "restore-deleted"}); status(err) != 409 {
		t.Fatalf("deleted historical identity restore=%v", err)
	}
	if err = f.db.Exec("UPDATE public_price_snapshots SET content_hash=? WHERE id=?", strings.Repeat("f", 64), snapshot.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.Restore(ctx, PublicPriceSnapshotRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: publicPriceDraftRevision(t, f.db), SnapshotGUID: release.GUID, IdempotencyKey: "restore-tampered"}); status(err) != 422 {
		t.Fatalf("tampered historical hash=%v", err)
	}
}

func TestPublicPriceSnapshotDBRejectsContentBindingDriftAndRebindsCompatibleGeneration(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	f.db = tx
	t.Cleanup(func() {
		if e := tx.Rollback().Error; e != nil && e != gorm.ErrInvalidTransaction {
			t.Error(e)
		}
	})
	if err := requirePublicPriceSnapshotFixture(f.db, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin := NewPublicModelAdminService(f.db)
	createActive := func(label string) *PublicModelAdmin {
		in := f.input(label + fmt.Sprint(persistence.NextGUID()))
		f.observe(t, in.UpstreamModelID)
		m, e := admin.Create(ctx, f.actor.ID, in)
		if e != nil {
			t.Fatal(e)
		}
		m, e = admin.Activate(ctx, f.actor.ID, mustGUID(t, m.GUID), 1)
		if e != nil {
			t.Fatal(e)
		}
		return m
	}
	a, b := createActive("content-a-"), createActive("content-b-")
	if _, e := NewPublicPriceSnapshotService(f.db).Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: publicPriceDraftRevision(t, f.db), IdempotencyKey: "initial-binding"}); e != nil {
		t.Fatal(e)
	}
	var state models.PublicPublicationState
	if e := f.db.Where("state_key=?", publicPublicationStateKey).First(&state).Error; e != nil {
		t.Fatal(e)
	}
	var content models.PublicContentRelease
	if e := f.db.First(&content, *state.ContentReleaseID).Error; e != nil {
		t.Fatal(e)
	}
	var boundPrice models.PublicPriceSnapshot
	if e := f.db.First(&boundPrice, *state.PriceSnapshotID).Error; e != nil {
		t.Fatal(e)
	}
	payload := models.JSONMap{"home": "[model](/pricing/" + a.ModelKey + ")", "about": "About", "terms": "Terms", "privacy": "Privacy", "legal_reviewed": true, "model_keys": []string{a.ModelKey}, "price_snapshot_guid": fmt.Sprint(boundPrice.Guid), "price_snapshot_version": boundPrice.Version}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	if e := f.db.Exec("UPDATE public_content_releases SET payload=?, content_hash=? WHERE id=?", payload, hex.EncodeToString(sum[:]), content.ID).Error; e != nil {
		t.Fatal(e)
	}
	deactivated, e := admin.Deactivate(ctx, f.actor.ID, mustGUID(t, a.GUID), DeactivationRequest{ExpectedRevision: a.Revision, Reason: "binding test"})
	if e != nil {
		t.Fatal(e)
	}
	beforeCounts := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID)
	var before models.PublicPublicationState
	_ = f.db.Where("state_key=?", publicPublicationStateKey).First(&before).Error
	if _, e = NewPublicPriceSnapshotService(f.db).Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: publicPriceDraftRevision(t, f.db), IdempotencyKey: "incompatible"}); status(e) != 409 {
		t.Fatalf("incompatible publish=%v", e)
	}
	var unchanged models.PublicPublicationState
	_ = f.db.Where("state_key=?", publicPublicationStateKey).First(&unchanged).Error
	if unchanged.Revision != before.Revision || !sameOptionalInt64(unchanged.PriceSnapshotID, before.PriceSnapshotID) || !sameOptionalInt64(unchanged.ContentReleaseID, before.ContentReleaseID) || publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID) != beforeCounts {
		t.Fatal("incompatible publish changed committed state")
	}
	if _, e = admin.Activate(ctx, f.actor.ID, mustGUID(t, a.GUID), deactivated.Revision); e != nil {
		t.Fatal(e)
	}
	compatibleRevision := publicPriceDraftRevision(t, f.db)
	compatibleRequest := PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: compatibleRevision, IdempotencyKey: "compatible"}
	beforeCompatibleCounts := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID)
	var beforeCompatibleState models.PublicPublicationState
	if e := f.db.Where("state_key=?", publicPublicationStateKey).First(&beforeCompatibleState).Error; e != nil {
		t.Fatal(e)
	}
	broken := NewPublicPriceSnapshotService(f.db)
	broken.fail = func(point string) error {
		if point == "content_release" {
			return fmt.Errorf("injected content release failure")
		}
		return nil
	}
	if _, e = broken.Publish(ctx, compatibleRequest); status(e) != 503 {
		t.Fatalf("compatible injected failure=%v", e)
	}
	var afterCompatibleFailure models.PublicPublicationState
	if e := f.db.Where("state_key=?", publicPublicationStateKey).First(&afterCompatibleFailure).Error; e != nil {
		t.Fatal(e)
	}
	if afterCompatibleFailure.Revision != beforeCompatibleState.Revision || !sameOptionalInt64(afterCompatibleFailure.PriceSnapshotID, beforeCompatibleState.PriceSnapshotID) || !sameOptionalInt64(afterCompatibleFailure.ContentReleaseID, beforeCompatibleState.ContentReleaseID) || publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID) != beforeCompatibleCounts {
		t.Fatal("failed compatible rebind left partial state")
	}
	release, e := NewPublicPriceSnapshotService(f.db).Publish(ctx, compatibleRequest)
	if e != nil {
		t.Fatal(e)
	}
	projection, e := NewPublicContentService(f.db).PublicProjection(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if projection.PriceReleaseVersion != release.Version {
		t.Fatalf("projection=%#v release=%#v", projection, release)
	}
	var latestState models.PublicPublicationState
	_ = f.db.Where("state_key=?", publicPublicationStateKey).First(&latestState).Error
	var latestContent models.PublicContentRelease
	_ = f.db.First(&latestContent, *latestState.ContentReleaseID).Error
	if fmt.Sprint(latestContent.Payload["price_snapshot_guid"]) != release.GUID || latestContent.Payload["price_snapshot_version"].(float64) != float64(release.Version) {
		t.Fatalf("content binding=%#v release=%#v", latestContent.Payload, release)
	}
	verifiedHash, hashErr := hashPublicContentPayload(latestContent.Payload)
	if hashErr != nil || latestContent.ContentHash != verifiedHash || latestContent.Version != content.Version+1 {
		t.Fatalf("rebound content release is not truthful or monotonic: %#v hashErr=%v", latestContent, hashErr)
	}
	_ = b
}

func publicModelIDByGUID(t *testing.T, db *gorm.DB, guid string) int64 {
	t.Helper()
	var model models.PublicModelConfig
	if err := db.Select("id").Where("guid = ?", guid).First(&model).Error; err != nil {
		t.Fatal(err)
	}
	return model.ID
}

func assertPublicModelEverPublished(t *testing.T, db *gorm.DB, id int64, want bool) {
	t.Helper()
	var model models.PublicModelConfig
	if err := db.Select("id", "ever_published").First(&model, id).Error; err != nil {
		t.Fatal(err)
	}
	if (model.EverPublished == 1) != want {
		t.Fatalf("model %d ever_published=%v want=%v", id, model.EverPublished, want)
	}
}

type publicPriceSnapshotCounts struct{ snapshots, items, contentReleases, jobs, audits int64 }

func publicPriceSnapshotFixtureCounts(t *testing.T, db *gorm.DB, actor int64) publicPriceSnapshotCounts {
	t.Helper()
	var c publicPriceSnapshotCounts
	if err := db.Model(&models.PublicPriceSnapshot{}).Where("created_by = ?", actor).Count(&c.snapshots).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.PublicPriceSnapshotItem{}).Where("created_by = ?", actor).Count(&c.items).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.PublicContentRelease{}).Where("created_by = ?", actor).Count(&c.contentReleases).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.PublicRenderJob{}).Where("created_by = ?", actor).Count(&c.jobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.AuditLog{}).Where("user_id = ? AND action LIKE 'public_pricing.%'", actor).Count(&c.audits).Error; err != nil {
		t.Fatal(err)
	}
	return c
}

func requirePublicPriceSnapshotFixture(db *gorm.DB, actor int64) error {
	for _, table := range []string{"public_price_snapshots", "public_price_snapshot_items", "public_publication_state", "public_content_releases", "public_render_jobs", "public_price_draft_state"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("BLOCKED_FIXTURE: migration missing %s", table)
		}
	}
	var state models.PublicPublicationState
	err := db.Where("state_key = ? AND is_deleted = 0", publicPublicationStateKey).First(&state).Error
	if err == gorm.ErrRecordNotFound {
		now := persistence.NowMillis()
		state = models.PublicPublicationState{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, StateKey: publicPublicationStateKey, PriceVisibility: models.PublicPriceVisibilityVisible, Revision: 1}
		if err = db.Create(&state).Error; err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	if state.ContentReleaseID == nil {
		now := persistence.NowMillis()
		var maxVersion int64
		if err = db.Model(&models.PublicContentRelease{}).Where("document_kind=?", models.PublicContentDocumentSite).Select("COALESCE(MAX(version),0)").Scan(&maxVersion).Error; err != nil {
			return err
		}
		payload := models.JSONMap{"home": "", "about": "About", "terms": "Terms", "privacy": "Privacy", "legal_reviewed": true, "model_keys": []string{}, "price_snapshot_guid": "1", "price_snapshot_version": int64(1)}
		encoded, _ := json.Marshal(payload)
		sum := sha256.Sum256(encoded)
		release := models.PublicContentRelease{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor, DocumentKind: models.PublicContentDocumentSite, Version: maxVersion + 1, SourceRevision: 1, Payload: payload, ContentHash: hex.EncodeToString(sum[:]), PublishedAt: now}
		if err = db.Create(&release).Error; err != nil {
			return err
		}
		if err = db.Model(&state).Update("content_release_id", release.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

func loadPublicPriceSnapshotFixtureState(db *gorm.DB) (models.PublicPublicationState, error) {
	var state models.PublicPublicationState
	err := db.Where("state_key = ? AND is_deleted = 0", publicPublicationStateKey).First(&state).Error
	return state, err
}
