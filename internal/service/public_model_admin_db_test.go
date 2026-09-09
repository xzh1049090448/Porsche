package service

import (
	"context"
	"fmt"
	projectdb "github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

type publicModelDBFixture struct {
	db    *gorm.DB
	actor models.User
}

func openPublicModelDBFixture(t *testing.T) *publicModelDBFixture {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit TEST_DATABASE_URL; .env is never read")
	}
	u, err := url.Parse(raw)
	name := ""
	if u != nil {
		name = strings.TrimPrefix(u.Path, "/")
	}
	if err != nil || !strings.HasSuffix(name, "_test") {
		t.Fatal("explicit disposable *_test database required")
	}
	db, err := projectdb.Open(raw, "test")
	if err != nil {
		t.Fatal(err)
	}
	db = db.Session(&gorm.Session{Logger: logger.Discard})
	pool, _ := db.DB()
	t.Cleanup(func() { _ = pool.Close() })
	var actual string
	if err = db.Raw("SELECT DATABASE()").Scan(&actual).Error; err != nil || actual != name {
		t.Fatal("test database identity mismatch")
	}
	for _, table := range []string{"public_model_configs", "upstream_model_observations", "audit_logs", "users", "public_price_draft_state"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("BLOCKED_FIXTURE: migration missing %s", table)
		}
	}
	var group models.BusinessGroup
	if err = db.Where("is_deleted = 0").First(&group).Error; err != nil {
		t.Fatal("BLOCKED_FIXTURE: active business group required")
	}
	now := persistence.NowMillis()
	username := fmt.Sprintf("pm%d", persistence.NextGUID()%1e9)
	hash := "test-only"
	actor := models.User{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now}, GroupID: group.ID, Username: &username, PasswordHash: &hash, PlanType: models.PlanFree, Status: models.UserStatusActive, Role: models.UserRoleRoot, AuthVersion: 1, AllowedModels: models.JSONSlice{}}
	if err = db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, cleanup := range []*gorm.DB{db.Exec("DELETE FROM audit_logs WHERE user_id = ?", actor.ID), db.Exec("DELETE FROM public_model_configs WHERE created_by = ?", actor.ID), db.Exec("DELETE FROM upstream_model_observations WHERE created_by = ?", actor.ID), db.Exec("DELETE FROM users WHERE id = ?", actor.ID)} {
			if cleanup.Error != nil {
				t.Error(cleanup.Error)
			}
		}
	})
	return &publicModelDBFixture{db: db, actor: actor}
}
func (f *publicModelDBFixture) observe(t *testing.T, id string) {
	f.observeAt(t, id, persistence.NowMillis())
}
func (f *publicModelDBFixture) observeAt(t *testing.T, id string, now int64) {
	t.Helper()
	actor := f.actor.ID
	o := models.UpstreamModelObservation{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, UpstreamModelID: id, Provider: "provider", CatalogComplete: 1, CatalogFresh: 1, ObservedAt: now, ResponseSummaryHash: strings.Repeat("a", 64)}
	if err := f.db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
}

func TestPublicModelCreateObservationAgeBoundary(t *testing.T) {
	f := openPublicModelDBFixture(t)
	base := int64(1_900_000_000_000)
	in := f.input(fmt.Sprint(persistence.NextGUID()))
	f.observeAt(t, in.UpstreamModelID, base-publicModelObservationFreshnessMillis-1)
	s := NewPublicModelAdminService(f.db)
	s.now = func() int64 { return base }
	if _, err := s.Create(context.Background(), f.actor.ID, in); status(err) != 409 {
		t.Fatalf("stale observation=%v", err)
	}
	f.observeAt(t, in.UpstreamModelID, base-publicModelObservationFreshnessMillis)
	if _, err := s.Create(context.Background(), f.actor.ID, in); err != nil {
		t.Fatalf("boundary observation=%v", err)
	}
}
func (f *publicModelDBFixture) input(s string) CreatePublicModelRequest {
	p := "2.00000000"
	return CreatePublicModelRequest{UpstreamModelID: "org/model-" + s, ModelKey: "model-" + s, DisplayName: "Model", Provider: "provider", Capabilities: []string{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: &p, OutputPriceUSDPerMillionTokens: &p}
}

func TestPublicModelDBLifecycleReservationOmissionAuditAndRollback(t *testing.T) {
	f := openPublicModelDBFixture(t)
	ctx := context.Background()
	suffix := fmt.Sprint(persistence.NextGUID())
	in := f.input(suffix)
	f.observe(t, in.UpstreamModelID)
	s := NewPublicModelAdminService(f.db)
	initialDraftRevision := publicPriceDraftRevision(t, f.db)
	m, err := s.Create(ctx, f.actor.ID, in)
	if err != nil || m.Revision != 1 {
		t.Fatalf("create %#v %v", m, err)
	}
	list, err := s.List(ctx, AdminModelListRequest{Search: suffix, Status: "draft", UpstreamState: "present", Page: 1, PageSize: 20})
	if err != nil || list.Total != 1 {
		t.Fatalf("list %#v %v", list, err)
	}
	if err = f.db.Model(&models.PublicModelConfig{}).Where("guid = ?", mustGUID(t, m.GUID)).Update("consecutive_absences", 1).Error; err != nil {
		t.Fatal(err)
	}
	present, _ := s.List(ctx, AdminModelListRequest{Search: suffix, UpstreamState: "present", Page: 1, PageSize: 20})
	missing, _ := s.List(ctx, AdminModelListRequest{Search: suffix, UpstreamState: "missing", Page: 1, PageSize: 20})
	if present.Total != 0 || missing.Total != 1 {
		t.Fatalf("present/missing transition=%d/%d", present.Total, missing.Total)
	}
	if got, err := s.Get(ctx, mustGUID(t, m.GUID)); err != nil || got.ModelKey != in.ModelKey {
		t.Fatal(err)
	}
	name := "Renamed"
	m, err = s.Update(ctx, f.actor.ID, mustGUID(t, m.GUID), UpdatePublicModelRequest{ExpectedRevision: 1, DisplayName: &name, InputPriceUSDPerMillionTokens: OptionalNullableString{Set: true}})
	if err != nil || m.InputPriceUSDPerMillionTokens != nil {
		t.Fatalf("clear %#v %v", m, err)
	}
	m, err = s.Activate(ctx, f.actor.ID, mustGUID(t, m.GUID), 2)
	if err != nil || m.Status != "active" {
		t.Fatal(err)
	}
	m, err = s.Deactivate(ctx, f.actor.ID, mustGUID(t, m.GUID), DeactivationRequest{ExpectedRevision: 3, Reason: "catalog mismatch"})
	if err != nil || m.Status != "inactive" {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, f.actor.ID, mustGUID(t, m.GUID), DeletePublicModelRequest{ExpectedRevision: 4, Reason: "retired"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, mustGUID(t, m.GUID)); status(err) != 404 {
		t.Fatalf("deleted get %v", err)
	}
	list, _ = s.List(ctx, AdminModelListRequest{Search: suffix, Page: 1, PageSize: 20})
	if list.Total != 0 {
		t.Fatal("deleted listed")
	}
	if _, err = s.Create(ctx, f.actor.ID, in); status(err) != 409 {
		t.Fatalf("key reused %v", err)
	}
	differentKey := in
	differentKey.ModelKey = in.ModelKey + "-new"
	if _, err = s.Create(ctx, f.actor.ID, differentKey); status(err) != 409 {
		t.Fatalf("upstream identity reused: %v", err)
	}
	var audits int64
	f.db.Model(&models.AuditLog{}).Where("user_id = ? AND action LIKE 'public_models.%'", f.actor.ID).Count(&audits)
	if audits != 5 {
		t.Fatalf("audits=%d", audits)
	}
	if got := publicPriceDraftRevision(t, f.db); got != initialDraftRevision+5 {
		t.Fatalf("draft revision=%d want=%d", got, initialDraftRevision+5)
	}
	rollback := f.input(suffix + "x")
	f.observe(t, rollback.UpstreamModelID)
	broken := NewPublicModelAdminService(f.db)
	calls := 0
	broken.nextGUID = func() int64 {
		calls++
		if calls == 1 {
			return persistence.NextGUID()
		}
		return 0
	}
	if _, err = broken.Create(ctx, f.actor.ID, rollback); err == nil {
		t.Fatal("audit failure accepted")
	}
	var count int64
	f.db.Model(&models.PublicModelConfig{}).Where("model_key = ?", rollback.ModelKey).Count(&count)
	if count != 0 {
		t.Fatal("audit failure committed")
	}
	if got := publicPriceDraftRevision(t, f.db); got != initialDraftRevision+5 {
		t.Fatalf("audit rollback advanced draft=%d", got)
	}
}

func publicPriceDraftRevision(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var state models.PublicPriceDraftState
	if err := db.Where("state_key='pricing' AND is_deleted=0").First(&state).Error; err != nil {
		t.Fatal(err)
	}
	return state.Revision
}

func TestPublicModelConcurrentTwoRootsAdvanceDraftRevisionTwice(t *testing.T) {
	f := openPublicModelDBFixture(t)
	start := publicPriceDraftRevision(t, f.db)
	var second models.User
	if err := f.db.First(&second, f.actor.ID).Error; err != nil {
		t.Fatal(err)
	}
	second.ID = 0
	second.Guid = persistence.NextGUID()
	name := fmt.Sprintf("pm%d", persistence.NextGUID()%1e9)
	second.Username = &name
	if err := f.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Where("user_id=?", second.ID).Delete(&models.AuditLog{}).Error; err != nil {
			t.Error(err)
		}
		if err := f.db.Where("created_by=?", second.ID).Delete(&models.PublicModelConfig{}).Error; err != nil {
			t.Error(err)
		}
		if err := f.db.Delete(&models.User{}, second.ID).Error; err != nil {
			t.Error(err)
		}
	})
	a, b := f.input(fmt.Sprint(persistence.NextGUID())), f.input(fmt.Sprint(persistence.NextGUID()))
	f.observe(t, a.UpstreamModelID)
	f.observe(t, b.UpstreamModelID)
	errs := make(chan error, 2)
	go func() {
		_, e := NewPublicModelAdminService(f.db).Create(context.Background(), f.actor.ID, a)
		errs <- e
	}()
	go func() { _, e := NewPublicModelAdminService(f.db).Create(context.Background(), second.ID, b); errs <- e }()
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	if got := publicPriceDraftRevision(t, f.db); got != start+2 {
		t.Fatalf("draft revision=%d want=%d", got, start+2)
	}
}

func TestPublicModelConcurrentCreatorsSameUpstreamExactlyOneWinner(t *testing.T) {
	f := openPublicModelDBFixture(t)
	suffix := fmt.Sprint(persistence.NextGUID())
	first := f.input(suffix)
	second := first
	second.ModelKey = first.ModelKey + "-other"
	f.observe(t, first.UpstreamModelID)
	s := NewPublicModelAdminService(f.db)
	runTwo(t, func(i int) error {
		in := first
		if i == 1 {
			in = second
		}
		_, err := s.Create(context.Background(), f.actor.ID, in)
		return err
	})
}

func TestPublicModelRevisionConcurrencyExactlyOneWinner(t *testing.T) {
	f, m, s := newConcurrentModel(t)
	guid := mustGUID(t, m.GUID)
	runTwo(t, func(i int) error {
		name := fmt.Sprintf("winner-%d", i)
		_, e := s.Update(context.Background(), f.actor.ID, guid, UpdatePublicModelRequest{ExpectedRevision: 1, DisplayName: &name})
		return e
	})
}
func TestPublicModelDeleteConcurrencyExactlyOneWinner(t *testing.T) {
	f, m, s := newConcurrentModel(t)
	guid := mustGUID(t, m.GUID)
	runTwo(t, func(int) error {
		return s.Delete(context.Background(), f.actor.ID, guid, DeletePublicModelRequest{ExpectedRevision: 1, Reason: "race"})
	})
}
func TestPublicModelIdentityRevalidationRejectsDisabledRoot(t *testing.T) {
	f, m, s := newConcurrentModel(t)
	if err := f.db.Model(&models.User{}).Where("id = ?", f.actor.ID).Update("status", models.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Activate(context.Background(), f.actor.ID, mustGUID(t, m.GUID), 1); status(err) != 403 {
		t.Fatalf("disabled root %v", err)
	}
}

func TestPublicModelIdentityConcurrencyExactlyOneWinner(t *testing.T) {
	f, m, service := newConcurrentModel(t)
	modelGUID := mustGUID(t, m.GUID)
	locked, release := make(chan struct{}), make(chan struct{})
	disableResult := make(chan error, 1)
	go func() {
		disableResult <- f.db.Transaction(func(tx *gorm.DB) error {
			var actor models.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", f.actor.ID).First(&actor).Error; err != nil {
				return err
			}
			close(locked)
			<-release
			return tx.Model(&models.User{}).Where("id = ?", f.actor.ID).Update("status", models.UserStatusDisabled).Error
		})
	}()
	<-locked
	mutationResult := make(chan error, 1)
	go func() {
		_, err := service.Activate(context.Background(), f.actor.ID, modelGUID, 1)
		mutationResult <- err
	}()
	close(release)
	if err := <-disableResult; err != nil {
		t.Fatal(err)
	}
	if err := <-mutationResult; status(err) != 403 {
		t.Fatalf("identity loser=%v", err)
	}
}
func newConcurrentModel(t *testing.T) (*publicModelDBFixture, *PublicModelAdmin, *PublicModelAdminService) {
	t.Helper()
	f := openPublicModelDBFixture(t)
	in := f.input(fmt.Sprint(persistence.NextGUID()))
	f.observe(t, in.UpstreamModelID)
	s := NewPublicModelAdminService(f.db)
	m, err := s.Create(context.Background(), f.actor.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	return f, m, s
}
func runTwo(t *testing.T, fn func(int) error) {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; errs <- fn(i) }(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	wins := 0
	for err := range errs {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("winners=%d", wins)
	}
}
func mustGUID(t *testing.T, v string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(v, &n); err != nil {
		t.Fatal(err)
	}
	return n
}
func status(err error) int { code, _ := StatusFromError(err); return code }
