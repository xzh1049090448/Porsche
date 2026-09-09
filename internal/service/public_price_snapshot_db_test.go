package service

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

// The full transactional suite is deliberately opt-in and never reads .env.
func TestPublicPriceSnapshotDBAtomicPublicationIdempotencyAndRestore(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	s := NewPublicPriceSnapshotService(f.db)
	ctx := context.Background()
	// A migrated disposable fixture owns setup of publication state/content release.
	if err := requirePublicPriceSnapshotFixture(f.db); err != nil {
		t.Fatal(err)
	}
	model := f.input("snapshot")
	f.observe(t, model.UpstreamModelID)
	created, err := NewPublicModelAdminService(f.db).Create(ctx, f.actor.ID, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewPublicModelAdminService(f.db).Activate(ctx, f.actor.ID, mustGUID(t, created.GUID), 1); err != nil {
		t.Fatal(err)
	}
	state, err := loadPublicPriceSnapshotFixtureState(f.db)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: state.Revision, IdempotencyKey: "publish-1"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: state.Revision, IdempotencyKey: "publish-1"})
	if err != nil || replay.GUID != release.GUID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	var snapshot models.PublicPriceSnapshot
	if err = f.db.Where("guid = ?", release.GUID).First(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	var items []models.PublicPriceSnapshotItem
	if err = f.db.Where("snapshot_id = ?", snapshot.ID).Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].InputPriceUSDPerMillionTokens != "2.00000000" || items[0].OutputPriceUSDPerMillionTokens != "2.00000000" {
		t.Fatalf("snapshot items=%#v", items)
	}
	after, _ := loadPublicPriceSnapshotFixtureState(f.db)
	for _, point := range []string{"snapshot", "item", "render_job", "audit", "pointer", "after_pointer"} {
		beforeCounts := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID)
		broken := NewPublicPriceSnapshotService(f.db)
		broken.fail = func(got string) error {
			if got == point {
				return fmt.Errorf("injected %s", point)
			}
			return nil
		}
		if _, failureErr := broken.Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: after.Revision, IdempotencyKey: "failure-" + point}); status(failureErr) != 503 {
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
	}
	restored, err := s.Restore(ctx, PublicPriceSnapshotRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: after.Revision, SnapshotGUID: release.GUID, IdempotencyKey: "restore-1"})
	if err != nil || restored.Version <= release.Version || restored.GUID == release.GUID {
		t.Fatalf("restore=%#v err=%v", restored, err)
	}
}

type publicPriceSnapshotCounts struct{ snapshots, items, jobs, audits int64 }

func publicPriceSnapshotFixtureCounts(t *testing.T, db *gorm.DB, actor int64) publicPriceSnapshotCounts {
	t.Helper()
	var c publicPriceSnapshotCounts
	if err := db.Model(&models.PublicPriceSnapshot{}).Where("created_by = ?", actor).Count(&c.snapshots).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.PublicPriceSnapshotItem{}).Where("created_by = ?", actor).Count(&c.items).Error; err != nil {
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

func requirePublicPriceSnapshotFixture(db *gorm.DB) error {
	for _, table := range []string{"public_price_snapshots", "public_price_snapshot_items", "public_publication_state", "public_content_releases", "public_render_jobs"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("BLOCKED_FIXTURE: migration missing %s", table)
		}
	}
	var state models.PublicPublicationState
	err := db.Where("state_key = ? AND is_deleted = 0", publicPublicationStateKey).First(&state).Error
	if err == gorm.ErrRecordNotFound {
		return fmt.Errorf("BLOCKED_FIXTURE: singleton publication state required")
	}
	if err != nil {
		return err
	}
	if state.ContentReleaseID == nil {
		now := persistence.NowMillis()
		release := models.PublicContentRelease{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now, DocumentKind: models.PublicContentDocumentSite, Version: 1, SourceRevision: 1, Payload: models.JSONMap{"fixture": true}, ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PublishedAt: now}
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
