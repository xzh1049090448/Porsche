package service

import (
	"context"
	"fmt"
	"os"
	"reflect"
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
	in := RootAlertOccurrence{Type: models.RootAlertTypeCatalogSyncFailure, Identity: "catalog", Payload: models.JSONMap{"error_code": "timeout", "observed_at": int64(100)}}
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
	if _, err := s.Occur(context.Background(), RootAlertOccurrence{Type: models.RootAlertTypeRendererFailure, Identity: "render", Payload: models.JSONMap{"release_version": int64(1), "render_job_guid": "123", "error_code": "failed", "observed_at": int64(100)}}); err == nil {
		t.Fatal("expected rollback")
	}
	var n int64
	if err := f.db.Model(&models.RootAlert{}).Where("fingerprint=?", fp).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}

func TestRootAlertDBExactLifecycleTimestampsCountsAndAudits(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	ctx := context.Background()
	s := NewRootAlertService(f.db)
	current := int64(100)
	s.now = func() int64 { return current }
	in := RootAlertOccurrence{Type: models.RootAlertTypeUpstreamMissing, ModelKey: "exact-model", Identity: "exact", Payload: models.JSONMap{"model_key": "exact-model", "consecutive_absences": int64(1), "observed_at": current}}
	fp := rootAlertFingerprint(in.Type, in.ModelKey, in.Identity)
	t.Cleanup(func() {
		var a models.RootAlert
		if f.db.Where("fingerprint=?", fp).First(&a).Error == nil {
			_ = f.db.Exec("DELETE FROM root_alert_receipts WHERE alert_id=?", a.ID).Error
			_ = f.db.Exec("DELETE FROM audit_logs WHERE resource=?", "root-alerts/"+fmt.Sprint(a.Guid)).Error
			_ = f.db.Exec("DELETE FROM root_alerts WHERE id=?", a.ID).Error
		}
	})
	if _, e := s.Occur(ctx, in); e != nil {
		t.Fatal(e)
	}
	current = 200
	in.Payload["observed_at"] = current
	in.Payload["consecutive_absences"] = int64(2)
	if _, e := s.Occur(ctx, in); e != nil {
		t.Fatal(e)
	}
	var a models.RootAlert
	if e := f.db.Where("fingerprint=?", fp).First(&a).Error; e != nil || a.FirstObservedAt != 100 || a.LastObservedAt != 200 || a.OccurrenceCount != 2 {
		t.Fatalf("repeat=%#v %v", a, e)
	}
	current = 300
	if e := s.Resolve(ctx, in.Type, in.ModelKey, in.Identity); e != nil {
		t.Fatal(e)
	}
	current = 400
	in.Payload["observed_at"] = current
	in.Payload["consecutive_absences"] = int64(1)
	if _, e := s.Occur(ctx, in); e != nil {
		t.Fatal(e)
	}
	if e := f.db.Where("id=?", a.ID).First(&a).Error; e != nil || a.FirstObservedAt != 100 || a.LastObservedAt != 400 || a.OccurrenceCount != 3 || a.State != models.RootAlertStateActive || a.ResolvedAt != nil {
		t.Fatalf("reopen=%#v %v", a, e)
	}
	var actions []string
	if e := f.db.Model(&models.AuditLog{}).Where("resource=?", "root-alerts/"+fmt.Sprint(a.Guid)).Order("id").Pluck("action", &actions).Error; e != nil || !reflect.DeepEqual(actions, []string{"root_alert.occurred", "root_alert.occurred", "root_alert.resolved", "root_alert.occurred"}) {
		t.Fatalf("audits=%v %v", actions, e)
	}
}

func TestRootAlertDBMutationAuditFailuresRollback(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	ctx := context.Background()
	clean := NewRootAlertService(f.db)
	in := RootAlertOccurrence{Type: models.RootAlertTypeCatalogSyncFailure, Identity: "rollback", Payload: models.JSONMap{"error_code": "timeout", "observed_at": int64(100)}}
	view, e := clean.Occur(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	var a models.RootAlert
	if e = f.db.Where("guid=?", mustGUID(t, view.GUID)).First(&a).Error; e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = f.db.Exec("DELETE FROM root_alert_receipts WHERE alert_id=?", a.ID).Error
		_ = f.db.Exec("DELETE FROM audit_logs WHERE resource=?", "root-alerts/"+view.GUID).Error
		_ = f.db.Exec("DELETE FROM root_alerts WHERE id=?", a.ID).Error
	})
	assertNoReceipt := func() {
		t.Helper()
		var n int64
		if e := f.db.Model(&models.RootAlertReceipt{}).Where("alert_id=? AND root_user_id=?", a.ID, f.actor.ID).Count(&n).Error; e != nil || n != 0 {
			t.Fatalf("receipt count=%d %v", n, e)
		}
	}
	broken := NewRootAlertService(f.db)
	broken.fail = func(point string) error {
		if point == "resolve.audit" {
			return errUnavailable("injected")
		}
		return nil
	}
	if e = broken.Resolve(ctx, in.Type, in.ModelKey, in.Identity); e == nil {
		t.Fatal("expected resolve failure")
	}
	if e = f.db.Where("id=?", a.ID).First(&a).Error; e != nil || a.State != models.RootAlertStateActive || a.ResolvedAt != nil {
		t.Fatalf("resolve rollback=%#v %v", a, e)
	}
	broken = NewRootAlertService(f.db)
	broken.fail = func(point string) error {
		if point == "receipt.read.audit" {
			return errUnavailable("injected")
		}
		return nil
	}
	if _, e = broken.MarkRead(ctx, f.actor.ID, view.GUID); e == nil {
		t.Fatal("expected read failure")
	}
	assertNoReceipt()
	broken = NewRootAlertService(f.db)
	broken.fail = func(point string) error {
		if point == "receipt.acknowledge.audit" {
			return errUnavailable("injected")
		}
		return nil
	}
	if _, e = broken.Acknowledge(ctx, f.actor.ID, view.GUID); e == nil {
		t.Fatal("expected acknowledge failure")
	}
	assertNoReceipt()
	var auditCount int64
	if e = f.db.Model(&models.AuditLog{}).Where("resource=?", "root-alerts/"+view.GUID).Count(&auditCount).Error; e != nil || auditCount != 1 {
		t.Fatalf("audit count=%d %v", auditCount, e)
	}
}
