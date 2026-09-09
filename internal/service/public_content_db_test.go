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

func TestPublicContentDBAtomicPublishIdempotencyRollbackAndRestore(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	f.db = tx
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ctx := context.Background()
	if err := seedContentPublicationFixture(f.db, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	s := NewPublicContentService(f.db)
	d, err := s.GetDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Revision != 1 || d.LegalReviewed {
		t.Fatalf("draft=%#v", d)
	}
	home, about, terms, privacy, reviewed := "[alpha](/pricing/alpha)", "About", "Terms", "Privacy", true
	d, err = s.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: d.Revision, Home: &home, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: 1, Home: &home, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed}); status(err) != 409 {
		t.Fatalf("stale save=%v", err)
	}
	before := contentDBCounts(t, f.db)
	for _, point := range []string{"release", "render_job", "audit", "pointer", "after_pointer"} {
		broken := NewPublicContentService(f.db)
		broken.fail = func(got string) error {
			if got == point {
				return fmt.Errorf("injected")
			}
			return nil
		}
		if _, e := broken.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, PriceReleaseGUID: "8001", IdempotencyKey: "fail-" + point}); status(e) != 503 {
			t.Fatalf("%s err=%v", point, e)
		}
		if got := contentDBCounts(t, f.db); got != before {
			t.Fatalf("%s partial rows: before=%v after=%v", point, before, got)
		}
	}
	r, err := s.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, PriceReleaseGUID: "8001", IdempotencyKey: "publish"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, PriceReleaseGUID: "8001", IdempotencyKey: "publish"})
	if err != nil || replay.GUID != r.GUID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	pub, err := s.PublicProjection(ctx)
	if err != nil || pub.Content.Home != home || pub.ContentReleaseVersion != r.Version {
		t.Fatalf("public=%#v err=%v", pub, err)
	}
	// Draft edits never affect the committed projection.
	home2 := "changed"
	d, err = s.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: d.Revision, Home: &home2, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed})
	if err != nil {
		t.Fatal(err)
	}
	pub2, _ := s.PublicProjection(ctx)
	if pub2.Content.Home != home {
		t.Fatal("public projection leaked draft")
	}
	restored, err := s.Restore(ctx, PublicContentRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, ReleaseGUID: r.GUID, IdempotencyKey: "restore"})
	if err != nil || restored.GUID == r.GUID || restored.Version <= r.Version {
		t.Fatalf("restore=%#v err=%v", restored, err)
	}
}

type contentCounts struct{ releases, jobs, audits int64 }

func contentDBCounts(t *testing.T, db *gorm.DB) contentCounts {
	t.Helper()
	var c contentCounts
	if e := db.Model(&models.PublicContentRelease{}).Count(&c.releases).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&models.PublicRenderJob{}).Count(&c.jobs).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&models.AuditLog{}).Where("action LIKE 'public_content.%'").Count(&c.audits).Error; e != nil {
		t.Fatal(e)
	}
	return c
}
func seedContentPublicationFixture(db *gorm.DB, actor int64) error {
	for _, table := range []string{"public_content_drafts", "public_content_releases", "public_publication_state", "public_price_snapshots", "public_price_snapshot_items", "public_render_jobs"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("BLOCKED_FIXTURE: migration missing %s", table)
		}
	}
	now := persistence.NowMillis()
	price := models.PublicPriceSnapshot{Guid: 8001, CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor, Version: 1, Reason: models.PublicPriceSnapshotReasonRootPublish, SourceRevision: 1, ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PublishedAt: now}
	if e := db.Create(&price).Error; e != nil {
		return e
	}
	in, out := "1.00000000", "2.00000000"
	model := models.PublicModelConfig{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, ModelKey: "alpha", UpstreamModelID: "org/alpha", DisplayName: "Alpha", Provider: "provider", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: &in, OutputPriceUSDPerMillionTokens: &out, Status: models.PublicModelConfigStatusActive, Revision: 1}
	if e := db.Create(&model).Error; e != nil {
		return e
	}
	item := models.PublicPriceSnapshotItem{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor, SnapshotID: price.ID, ModelConfigID: model.ID, ModelKey: "alpha", UpstreamModelID: "org/alpha", DisplayName: "Alpha", Provider: "provider", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: in, OutputPriceUSDPerMillionTokens: out}
	if e := db.Create(&item).Error; e != nil {
		return e
	}
	state := models.PublicPublicationState{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, StateKey: publicPublicationStateKey, PriceSnapshotID: &price.ID, PriceVisibility: models.PublicPriceVisibilityVisible, Revision: 1}
	return db.Create(&state).Error
}
