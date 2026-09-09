package service

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
)

func TestRootAlertDBLifecycleReceiptsAndConcurrentOccurrence(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	for _, table := range []string{"root_alerts", "root_alert_receipts"} {
		if !f.db.Migrator().HasTable(table) {
			t.Fatalf("BLOCKED_FIXTURE: migration missing %s", table)
		}
	}
	ctx := context.Background()
	s := NewRootAlertService(f.db)
	in := RootAlertOccurrence{Type: models.RootAlertTypeCatalogSyncFailure, Identity: "catalog", Payload: models.JSONMap{"reason": "timeout"}}
	fp := rootAlertFingerprint(in.Type, in.ModelKey, in.Identity)
	t.Cleanup(func() {
		_ = f.db.Exec("DELETE r FROM root_alert_receipts r JOIN root_alerts a ON a.id=r.alert_id WHERE a.fingerprint=?", fp).Error
		var ids []int64
		_ = f.db.Model(&models.RootAlert{}).Where("fingerprint=?", fp).Pluck("guid", &ids).Error
		for _, id := range ids {
			_ = f.db.Where("resource=?", "root-alerts/"+fmt.Sprint(id)).Delete(&models.AuditLog{}).Error
		}
		_ = f.db.Where("fingerprint=?", fp).Delete(&models.RootAlert{}).Error
	})
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, e := s.Occur(ctx, in); errs <- e }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	page, err := s.List(ctx, f.actor.ID, RootAlertListQuery{State: "active", Page: 1, PageSize: 20})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	guid := page.Items[0].GUID
	var persisted models.RootAlert
	if e := f.db.Where("fingerprint=?", fp).First(&persisted).Error; e != nil || persisted.OccurrenceCount != 2 || persisted.LastObservedAt < persisted.FirstObservedAt {
		t.Fatalf("persisted=%#v %v", persisted, e)
	}
	username := fmt.Sprintf("ra%d", persistence.NextGUID()%1e9)
	hash := "test-only"
	now := persistence.NowMillis()
	other := models.User{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now}, GroupID: f.actor.GroupID, Username: &username, PasswordHash: &hash, PlanType: models.PlanFree, Status: models.UserStatusActive, Role: models.UserRoleRoot, AuthVersion: 1, AllowedModels: models.JSONSlice{}}
	if e := f.db.Create(&other).Error; e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = f.db.Exec("DELETE FROM root_alert_receipts WHERE root_user_id=?", other.ID).Error
		_ = f.db.Exec("DELETE FROM audit_logs WHERE user_id=?", other.ID).Error
		_ = f.db.Exec("DELETE FROM users WHERE id=?", other.ID).Error
	})
	if otherPage, e := s.List(ctx, other.ID, RootAlertListQuery{State: "active", Page: 1, PageSize: 20}); e != nil || otherPage.Total != 1 || otherPage.Items[0].Read {
		t.Fatalf("new root page=%#v %v", otherPage, e)
	}
	if detail, e := s.Get(ctx, f.actor.ID, guid); e != nil || detail.GUID != guid {
		t.Fatalf("detail=%#v %v", detail, e)
	}
	if n, e := s.UnreadCount(ctx, f.actor.ID); e != nil || n.UnreadCount != 1 {
		t.Fatalf("unread=%#v %v", n, e)
	}
	if _, e := s.MarkRead(ctx, f.actor.ID, guid); e != nil {
		t.Fatal(e)
	}
	if otherPage, e := s.List(ctx, other.ID, RootAlertListQuery{State: "active", Page: 1, PageSize: 20}); e != nil || otherPage.Items[0].Read {
		t.Fatalf("receipt leaked=%#v %v", otherPage, e)
	}
	start = make(chan struct{})
	errs = make(chan error, 2)
	wg = sync.WaitGroup{}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, e := s.Acknowledge(ctx, f.actor.ID, guid); errs <- e }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Acknowledge(ctx, f.actor.ID, guid); e != nil {
		t.Fatal(e)
	}
	if e := s.Resolve(ctx, in.Type, in.ModelKey, in.Identity); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Occur(ctx, in); e != nil {
		t.Fatal(e)
	}
	page, err = s.List(ctx, f.actor.ID, RootAlertListQuery{State: "active", Page: 1, PageSize: 20})
	if err != nil || page.Total != 1 || page.Items[0].Read || page.Items[0].Acknowledged {
		t.Fatalf("reopen=%#v %v", page, err)
	}
}

func TestRootAlertDBFailureInjectionRollsBack(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	fp := rootAlertFingerprint(models.RootAlertTypeRendererFailure, "", "render")
	t.Cleanup(func() { _ = f.db.Where("fingerprint=?", fp).Delete(&models.RootAlert{}).Error })
	s := NewRootAlertService(f.db)
	s.fail = func(point string) error {
		if point == "audit" {
			return errUnavailable("injected")
		}
		return nil
	}
	if _, err := s.Occur(context.Background(), RootAlertOccurrence{Type: models.RootAlertTypeRendererFailure, Identity: "render"}); err == nil {
		t.Fatal("expected rollback")
	}
	var n int64
	if err := f.db.Model(&models.RootAlert{}).Where("fingerprint=?", fp).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}
