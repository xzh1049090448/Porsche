package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

func TestPublicContentDBConcurrentSameKeyUsesOneReleaseWithoutDeadlock(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	seed, err := seedContentPublicationFixture(f.db, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanPublicContentDBFixture(t, f.db)
		if e := f.db.Exec("DELETE FROM audit_logs WHERE user_id=? AND action LIKE 'public_content.%'", f.actor.ID).Error; e != nil {
			t.Errorf("fixture audit cleanup: %v", e)
		}
	})
	s := NewPublicContentService(f.db)
	ctx := context.Background()
	d, err := s.GetDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	home, about, terms, privacy, reviewed := "[model](/pricing/"+seed.modelKey+")", "About", "Terms", "Privacy", true
	d, err = s.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: d.Revision, Home: &home, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan *PublicContentRelease, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, e := s.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "concurrent"})
			results <- r
			errs <- e
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	var guid string
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for r := range results {
		if r == nil {
			t.Fatal("nil release")
		}
		if guid == "" {
			guid = r.GUID
		} else if r.GUID != guid {
			t.Fatalf("duplicate releases %s %s", guid, r.GUID)
		}
	}
	var count int64
	if e := f.db.Model(&models.PublicContentRelease{}).Where("guid=?", mustGUID(t, guid)).Count(&count).Error; e != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, e)
	}
}

func TestPublicHomeDraftMutationsShareAggregateRevision(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ctx := context.Background()
	s := NewPublicContentService(tx)
	documents, err := s.GetDocumentsDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	home, err := s.CreateAnnouncement(ctx, f.actor.ID, AnnouncementCreateRequest{
		ExpectedRevision: documents.Revision, Title: "maintenance", BodyMarkdown: "body", IsVisible: true, SortOrder: 10,
	})
	if err != nil || home.Revision != documents.Revision+1 || len(home.Announcements) != 1 {
		t.Fatalf("home=%#v err=%v", home, err)
	}
	if _, err = s.SaveDocumentsDraft(ctx, f.actor.ID, DocumentsDraftSaveRequest{ExpectedRevision: documents.Revision, About: "a", Terms: "t", Privacy: "p", LegalReviewed: true}); status(err) != 409 {
		t.Fatalf("stale documents status=%d err=%v", status(err), err)
	}
	documents, err = s.SaveDocumentsDraft(ctx, f.actor.ID, DocumentsDraftSaveRequest{ExpectedRevision: home.Revision, About: "a", Terms: "t", Privacy: "p", LegalReviewed: true})
	if err != nil || documents.Revision != home.Revision+1 {
		t.Fatalf("documents=%#v err=%v", documents, err)
	}
	if _, err = s.CreateFAQ(ctx, f.actor.ID, FAQCreateRequest{ExpectedRevision: home.Revision, Question: "q", AnswerMarkdown: "a", IsVisible: true}); status(err) != 409 {
		t.Fatalf("stale home status=%d err=%v", status(err), err)
	}
}

func TestPublicHomeDraftMutationsCRUDSoftDeleteFeaturedAndAudit(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ctx := context.Background()
	s := NewPublicContentService(tx)
	draft, err := s.GetHomeDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	effective := int64(1_800_000_000_000)
	draft, err = s.CreateAnnouncement(ctx, f.actor.ID, AnnouncementCreateRequest{ExpectedRevision: draft.Revision, Title: "one", BodyMarkdown: "body", EffectiveAt: &effective, IsVisible: true, SortOrder: 10})
	if err != nil {
		t.Fatal(err)
	}
	announcementGUID := draft.Announcements[0].GUID
	title, body, visible, order := "updated", "updated body", false, 2
	draft, err = s.UpdateAnnouncement(ctx, f.actor.ID, announcementGUID, AnnouncementUpdateRequest{ExpectedRevision: draft.Revision, Title: &title, BodyMarkdown: &body, EffectiveAt: OptionalNullableUnixMillis{Set: true}, IsVisible: &visible, SortOrder: &order})
	if err != nil || draft.Announcements[0].Title != title || draft.Announcements[0].EffectiveAt != nil || draft.Announcements[0].IsVisible || draft.Announcements[0].SortOrder != order {
		t.Fatalf("announcement update=%#v err=%v", draft, err)
	}
	draft, err = s.CreateFAQ(ctx, f.actor.ID, FAQCreateRequest{ExpectedRevision: draft.Revision, Question: "q", AnswerMarkdown: "answer", IsVisible: true, SortOrder: 9})
	if err != nil {
		t.Fatal(err)
	}
	faqGUID := draft.FAQs[0].GUID
	question, answer, faqVisible, faqOrder := "updated q", "updated answer", false, 1
	draft, err = s.UpdateFAQ(ctx, f.actor.ID, faqGUID, FAQUpdateRequest{ExpectedRevision: draft.Revision, Question: &question, AnswerMarkdown: &answer, IsVisible: &faqVisible, SortOrder: &faqOrder})
	if err != nil || draft.FAQs[0].Question != question || draft.FAQs[0].IsVisible || draft.FAQs[0].SortOrder != faqOrder {
		t.Fatalf("FAQ update=%#v err=%v", draft, err)
	}
	if _, invalidErr := s.ReplaceFeaturedModels(ctx, f.actor.ID, FeaturedModelsSaveRequest{ExpectedRevision: draft.Revision, FeaturedModelKeys: []string{"missing-model"}}); status(invalidErr) != 400 {
		t.Fatalf("invalid featured status=%d err=%v", status(invalidErr), invalidErr)
	}
	configs := []models.PublicModelConfig{
		{AuditFields: auditFields(&f.actor.ID), ModelKey: "featured-a", UpstreamModelID: "org/a", DisplayName: "A", Provider: "p", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 1, Status: models.PublicModelConfigStatusActive, Revision: 1},
		{AuditFields: auditFields(&f.actor.ID), ModelKey: "featured-b", UpstreamModelID: "org/b", DisplayName: "B", Provider: "p", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 1, Status: models.PublicModelConfigStatusActive, Revision: 1},
	}
	if err = tx.Create(&configs).Error; err != nil {
		t.Fatal(err)
	}
	draft, err = s.ReplaceFeaturedModels(ctx, f.actor.ID, FeaturedModelsSaveRequest{ExpectedRevision: draft.Revision, FeaturedModelKeys: []string{"featured-b", "featured-a"}})
	if err != nil || !reflect.DeepEqual(draft.FeaturedModelKeys, []string{"featured-b", "featured-a"}) {
		t.Fatalf("featured=%#v err=%v", draft, err)
	}
	brokenFeatured := NewPublicContentService(tx)
	brokenFeatured.fail = func(point string) error {
		if point == "public_home_after_cas" {
			return fmt.Errorf("injected")
		}
		return nil
	}
	if _, replaceErr := brokenFeatured.ReplaceFeaturedModels(ctx, f.actor.ID, FeaturedModelsSaveRequest{ExpectedRevision: draft.Revision, FeaturedModelKeys: []string{"featured-a"}}); status(replaceErr) != 503 {
		t.Fatalf("featured rollback status=%d err=%v", status(replaceErr), replaceErr)
	}
	unchanged, err := s.GetHomeDraft(ctx, f.actor.ID)
	if err != nil || unchanged.Revision != draft.Revision || !reflect.DeepEqual(unchanged.FeaturedModelKeys, draft.FeaturedModelKeys) {
		t.Fatalf("featured rollback draft=%#v err=%v", unchanged, err)
	}
	revision, err := s.DeleteAnnouncement(ctx, f.actor.ID, announcementGUID, AnnouncementDeleteRequest{ExpectedRevision: draft.Revision})
	if err != nil || revision != draft.Revision+1 {
		t.Fatalf("delete revision=%d err=%v", revision, err)
	}
	if _, repeatedErr := s.DeleteAnnouncement(ctx, f.actor.ID, announcementGUID, AnnouncementDeleteRequest{ExpectedRevision: revision}); status(repeatedErr) != 404 {
		t.Fatalf("repeated delete status=%d err=%v", status(repeatedErr), repeatedErr)
	}
	revision, err = s.DeleteFAQ(ctx, f.actor.ID, faqGUID, FAQDeleteRequest{ExpectedRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetHomeDraft(ctx, f.actor.ID)
	if err != nil || loaded.Revision != revision || len(loaded.Announcements) != 0 || len(loaded.FAQs) != 0 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	var announcement models.PublicHomeAnnouncement
	if err = tx.Unscoped().Where("guid=?", mustGUID(t, announcementGUID)).First(&announcement).Error; err != nil || announcement.IsDeleted != 1 || announcement.UpdatedBy == nil || *announcement.UpdatedBy != f.actor.ID || announcement.Revision != 3 {
		t.Fatalf("announcement audit=%#v err=%v", announcement, err)
	}
	var faq models.PublicHomeFAQ
	if err = tx.Unscoped().Where("guid=?", mustGUID(t, faqGUID)).First(&faq).Error; err != nil || faq.IsDeleted != 1 || faq.CreatedBy == nil || *faq.CreatedBy != f.actor.ID || faq.UpdatedBy == nil || *faq.UpdatedBy != f.actor.ID || faq.Revision != 3 {
		t.Fatalf("FAQ audit=%#v err=%v", faq, err)
	}
	var auditRows []models.AuditLog
	if err = tx.Where("user_id=? AND action LIKE 'public_content.home.%'", f.actor.ID).Find(&auditRows).Error; err != nil || len(auditRows) != 7 {
		t.Fatalf("audits=%d err=%v", len(auditRows), err)
	}
	encoded, _ := json.Marshal(auditRows)
	if strings.Contains(string(encoded), "updated body") || strings.Contains(string(encoded), "updated answer") {
		t.Fatalf("audit leaked body: %s", encoded)
	}
}

func TestPublicHomeSoftDeleteMissingAndRollbackAreOpaqueAndAtomic(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ctx := context.Background()
	s := NewPublicContentService(tx)
	draft, err := s.GetHomeDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteAnnouncement(ctx, f.actor.ID, "999999", AnnouncementDeleteRequest{ExpectedRevision: draft.Revision}); status(err) != 404 {
		t.Fatalf("missing status=%d err=%v", status(err), err)
	}
	for _, failurePoint := range []string{"public_home_after_row", "public_home_audit", "public_home_before_cas", "public_home_after_cas"} {
		broken := NewPublicContentService(tx)
		broken.fail = func(point string) error {
			if point == failurePoint {
				return fmt.Errorf("injected")
			}
			return nil
		}
		if _, err = broken.CreateFAQ(ctx, f.actor.ID, FAQCreateRequest{ExpectedRevision: draft.Revision, Question: "rollback", AnswerMarkdown: "secret", IsVisible: true}); status(err) != 503 {
			t.Fatalf("%s rollback status=%d err=%v", failurePoint, status(err), err)
		}
		loaded, loadErr := s.GetHomeDraft(ctx, f.actor.ID)
		if loadErr != nil || loaded.Revision != draft.Revision || len(loaded.FAQs) != 0 {
			t.Fatalf("%s rollback leaked=%#v err=%v", failurePoint, loaded, loadErr)
		}
	}
}

func TestPublicHomeDraftAggregateLimitRollsBackEveryWritePath(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ctx := context.Background()
	s := NewPublicContentService(tx)
	draft, err := s.GetHomeDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	var aggregate models.PublicContentDraft
	if err = tx.Where("document_kind=? AND is_deleted=0", models.PublicContentDocumentSite).First(&aggregate).Error; err != nil {
		t.Fatal(err)
	}
	payload := clonePublicContentPayload(aggregate.Payload)
	payload["padding"] = ""
	baseSize, err := publicHomeAggregateDraftSize(payload, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload["padding"] = strings.Repeat("x", PublicContentDraftAggregateLimit-baseSize)
	if err = tx.Model(&aggregate).Update("payload", payload).Error; err != nil {
		t.Fatal(err)
	}
	config := models.PublicModelConfig{AuditFields: auditFields(&f.actor.ID), ModelKey: "aggregate-featured", UpstreamModelID: "org/aggregate", DisplayName: "Aggregate", Provider: "p", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 1, Status: models.PublicModelConfigStatusActive, Revision: 1}
	if err = tx.Create(&config).Error; err != nil {
		t.Fatal(err)
	}

	assertRejectedWithoutPartialState := func(name string, write func() error) {
		t.Helper()
		if writeErr := write(); status(writeErr) != 400 {
			t.Fatalf("%s status=%d err=%v", name, status(writeErr), writeErr)
		}
		var current models.PublicContentDraft
		if loadErr := tx.Where("id=?", aggregate.ID).First(&current).Error; loadErr != nil || current.Revision != draft.Revision || current.Payload["padding"] != payload["padding"] {
			t.Fatalf("%s aggregate=%#v err=%v", name, current, loadErr)
		}
		var announcements, faqs, audits int64
		if countErr := tx.Model(&models.PublicHomeAnnouncement{}).Where("content_draft_id=?", aggregate.ID).Count(&announcements).Error; countErr != nil {
			t.Fatal(countErr)
		}
		if countErr := tx.Model(&models.PublicHomeFAQ{}).Where("content_draft_id=?", aggregate.ID).Count(&faqs).Error; countErr != nil {
			t.Fatal(countErr)
		}
		if countErr := tx.Model(&models.AuditLog{}).Where("user_id=? AND action LIKE 'public_content.home.%'", f.actor.ID).Count(&audits).Error; countErr != nil {
			t.Fatal(countErr)
		}
		if announcements != 0 || faqs != 0 || audits != 0 {
			t.Fatalf("%s left rows announcements=%d faqs=%d audits=%d", name, announcements, faqs, audits)
		}
	}
	assertRejectedWithoutPartialState("announcement", func() error {
		_, writeErr := s.CreateAnnouncement(ctx, f.actor.ID, AnnouncementCreateRequest{ExpectedRevision: draft.Revision, Title: "a", BodyMarkdown: "b", IsVisible: true})
		return writeErr
	})
	assertRejectedWithoutPartialState("FAQ", func() error {
		_, writeErr := s.CreateFAQ(ctx, f.actor.ID, FAQCreateRequest{ExpectedRevision: draft.Revision, Question: "q", AnswerMarkdown: "a", IsVisible: true})
		return writeErr
	})
	assertRejectedWithoutPartialState("featured", func() error {
		_, writeErr := s.ReplaceFeaturedModels(ctx, f.actor.ID, FeaturedModelsSaveRequest{ExpectedRevision: draft.Revision, FeaturedModelKeys: []string{config.ModelKey}})
		return writeErr
	})
	assertRejectedWithoutPartialState("documents", func() error {
		_, writeErr := s.SaveDocumentsDraft(ctx, f.actor.ID, DocumentsDraftSaveRequest{ExpectedRevision: draft.Revision, About: "a"})
		return writeErr
	})
	assertRejectedWithoutPartialState("legacy", func() error {
		home, empty, reviewed := "h", "", false
		_, writeErr := s.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: draft.Revision, Home: &home, About: &empty, Terms: &empty, Privacy: &empty, LegalReviewed: &reviewed})
		return writeErr
	})
}

func TestPublicHomePatchAuditDistinguishesVisibilityAndSortWithoutContent(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ctx := context.Background()
	s := NewPublicContentService(tx)
	draft, err := s.GetHomeDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err = s.CreateAnnouncement(ctx, f.actor.ID, AnnouncementCreateRequest{ExpectedRevision: draft.Revision, Title: "audit title", BodyMarkdown: "audit body", IsVisible: true})
	if err != nil {
		t.Fatal(err)
	}
	announcementGUID := draft.Announcements[0].GUID
	draft, err = s.CreateFAQ(ctx, f.actor.ID, FAQCreateRequest{ExpectedRevision: draft.Revision, Question: "audit question", AnswerMarkdown: "audit answer", IsVisible: true})
	if err != nil {
		t.Fatal(err)
	}
	faqGUID := draft.FAQs[0].GUID
	visible, order := false, 9
	for _, mutation := range []func() error{
		func() error {
			var e error
			draft, e = s.UpdateAnnouncement(ctx, f.actor.ID, announcementGUID, AnnouncementUpdateRequest{ExpectedRevision: draft.Revision, IsVisible: &visible})
			return e
		},
		func() error {
			var e error
			draft, e = s.UpdateAnnouncement(ctx, f.actor.ID, announcementGUID, AnnouncementUpdateRequest{ExpectedRevision: draft.Revision, SortOrder: &order})
			return e
		},
		func() error {
			var e error
			draft, e = s.UpdateFAQ(ctx, f.actor.ID, faqGUID, FAQUpdateRequest{ExpectedRevision: draft.Revision, IsVisible: &visible})
			return e
		},
		func() error {
			var e error
			draft, e = s.UpdateFAQ(ctx, f.actor.ID, faqGUID, FAQUpdateRequest{ExpectedRevision: draft.Revision, SortOrder: &order})
			return e
		},
	} {
		if err = mutation(); err != nil {
			t.Fatal(err)
		}
	}
	var audits []models.AuditLog
	if err = tx.Where("user_id=? AND action IN ?", f.actor.ID, []string{"public_content.home.announcement.update", "public_content.home.faq.update"}).Order("id").Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 4 {
		t.Fatalf("audit count=%d", len(audits))
	}
	want := [][]string{{"is_visible"}, {"sort_order"}, {"is_visible"}, {"sort_order"}}
	for i := range audits {
		if got := publicHomeAuditChangedFields(audits[i].Detail); !reflect.DeepEqual(got, want[i]) {
			t.Fatalf("audit %d changed_fields=%v want=%v detail=%#v", i, got, want[i], audits[i].Detail)
		}
		encoded, _ := json.Marshal(audits[i].Detail)
		for _, forbidden := range []string{"audit title", "audit body", "audit question", "audit answer", "password"} {
			if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
				t.Fatalf("audit leaked %q: %s", forbidden, encoded)
			}
		}
	}
}

func publicHomeAuditChangedFields(detail models.JSONMap) []string {
	values, ok := detail["changed_fields"].([]any)
	if !ok {
		if typed, typedOK := detail["changed_fields"].(models.JSONSlice); typedOK {
			return append([]string(nil), typed...)
		}
		return nil
	}
	fields := make([]string, 0, len(values))
	for _, value := range values {
		field, ok := value.(string)
		if !ok {
			return nil
		}
		fields = append(fields, field)
	}
	return fields
}

func TestPublicHomeDraftMutationLimitsAndConcurrentRevision(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	t.Cleanup(func() {
		cleanPublicContentDBFixture(t, f.db)
		if err := f.db.Exec("DELETE FROM audit_logs WHERE user_id=? AND action LIKE 'public_content.home.%'", f.actor.ID).Error; err != nil {
			t.Errorf("fixture audit cleanup: %v", err)
		}
	})
	ctx := context.Background()
	s := NewPublicContentService(f.db)
	draft, err := s.GetHomeDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type mutationResult struct {
		draft *PublicHomeDraft
		err   error
	}
	results := make(chan mutationResult, 2)
	for i := 0; i < 2; i++ {
		go func(index int) {
			<-start
			created, createErr := s.CreateAnnouncement(ctx, f.actor.ID, AnnouncementCreateRequest{ExpectedRevision: draft.Revision, Title: fmt.Sprintf("concurrent-%d", index), BodyMarkdown: "body", IsVisible: true})
			results <- mutationResult{draft: created, err: createErr}
		}(i)
	}
	close(start)
	var success, conflict int
	for i := 0; i < 2; i++ {
		result := <-results
		switch status(result.err) {
		case 0:
			success++
		case 409:
			conflict++
		default:
			t.Fatalf("concurrent result=%#v", result)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	draft, err = s.GetHomeDraft(ctx, f.actor.ID)
	if err != nil || len(draft.Announcements) != 1 {
		t.Fatalf("post-concurrency draft=%#v err=%v", draft, err)
	}

	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	limited := NewPublicContentService(tx)
	for len(draft.Announcements) < PublicHomeAnnouncementLimit {
		draft, err = limited.CreateAnnouncement(ctx, f.actor.ID, AnnouncementCreateRequest{ExpectedRevision: draft.Revision, Title: fmt.Sprintf("announcement-%d", len(draft.Announcements)), BodyMarkdown: "body", IsVisible: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = limited.CreateAnnouncement(ctx, f.actor.ID, AnnouncementCreateRequest{ExpectedRevision: draft.Revision, Title: "over", BodyMarkdown: "body", IsVisible: true}); status(err) != 400 {
		t.Fatalf("announcement limit status=%d err=%v", status(err), err)
	}
	for len(draft.FAQs) < PublicHomeFAQLimit {
		draft, err = limited.CreateFAQ(ctx, f.actor.ID, FAQCreateRequest{ExpectedRevision: draft.Revision, Question: fmt.Sprintf("faq-%d", len(draft.FAQs)), AnswerMarkdown: "answer", IsVisible: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = limited.CreateFAQ(ctx, f.actor.ID, FAQCreateRequest{ExpectedRevision: draft.Revision, Question: "over", AnswerMarkdown: "answer", IsVisible: true}); status(err) != 400 {
		t.Fatalf("FAQ limit status=%d err=%v", status(err), err)
	}
}

func TestDocumentsDraftAndLegacySavePreserveStructuredPayload(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ctx := context.Background()
	s := NewPublicContentService(tx)
	docs, err := s.GetDocumentsDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	docs, err = s.SaveDocumentsDraft(ctx, f.actor.ID, DocumentsDraftSaveRequest{ExpectedRevision: docs.Revision, About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true})
	if err != nil || docs.About != "about" || docs.Revision != 2 {
		t.Fatalf("docs=%#v err=%v", docs, err)
	}
	var row models.PublicContentDraft
	if err = tx.Where("document_kind=? AND is_deleted=0", models.PublicContentDocumentSite).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	payload := clonePublicContentPayload(row.Payload)
	payload["featured_model_keys"] = []string{"keep"}
	payload["future"] = map[string]any{"keep": true}
	if err = tx.Model(&row).Update("payload", payload).Error; err != nil {
		t.Fatal(err)
	}
	home, about, terms, privacy, reviewed := "legacy", "new about", "new terms", "new privacy", false
	legacy, err := s.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: docs.Revision, Home: &home, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Where("id=?", row.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(publicHomeFeaturedKeys(row.Payload), []string{"keep"}) || !reflect.DeepEqual(row.Payload["future"], map[string]any{"keep": true}) || legacy.Home != "legacy" {
		t.Fatalf("legacy payload=%#v draft=%#v", row.Payload, legacy)
	}
}

func TestPublicContentDBAtomicPublishIdempotencyRollbackAndRestore(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	f.db = tx
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ctx := context.Background()
	seed, err := seedContentPublicationFixture(f.db, f.actor.ID)
	if err != nil {
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
	home, about, terms, privacy, reviewed := "[model](/pricing/"+seed.modelKey+")", "About", "Terms", "Privacy", true
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
		if _, e := broken.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "fail-" + point}); status(e) != 503 {
			t.Fatalf("%s err=%v", point, e)
		}
		if got := contentDBCounts(t, f.db); got != before {
			t.Fatalf("%s partial rows: before=%v after=%v", point, before, got)
		}
	}
	r, err := s.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "publish"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "publish"})
	if err != nil || replay.GUID != r.GUID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if _, err = s.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision + 1, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "publish"}); status(err) != 409 {
		t.Fatalf("same key changed payload=%v", err)
	}
	second := f.actor
	second.ID = 0
	second.Guid = persistence.NextGUID()
	username := fmt.Sprintf("pc%d", persistence.NextGUID())
	second.Username = &username
	if err = f.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	other, otherErr := s.Publish(ctx, PublicContentPublicationRequest{ActorID: second.ID, ExpectedRevision: d.Revision, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "publish"})
	if otherErr != nil || other.GUID == r.GUID {
		t.Fatalf("cross actor replay=%#v err=%v", other, otherErr)
	}
	pub, err := s.PublicProjection(ctx)
	expectedHome, expectedHomeIssues := sanitizeContentHTML("home", home, false)
	if err != nil || len(expectedHomeIssues) != 0 || pub.Content.Home != expectedHome || pub.ContentReleaseVersion != other.Version {
		t.Fatalf("public=%#v err=%v", pub, err)
	}
	// Draft edits never affect the committed projection.
	home2 := "changed"
	d, err = s.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: d.Revision, Home: &home2, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed})
	if err != nil {
		t.Fatal(err)
	}
	pub2, _ := s.PublicProjection(ctx)
	if pub2.Content.Home != expectedHome {
		t.Fatal("public projection leaked draft")
	}
	var pointerBeforeReplay models.PublicPublicationState
	if err = f.db.Where("state_key=?", publicPublicationStateKey).First(&pointerBeforeReplay).Error; err != nil {
		t.Fatal(err)
	}
	replay, err = s.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision - 1, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "publish"})
	if err != nil || replay.GUID != r.GUID {
		t.Fatalf("post-draft replay=%#v err=%v", replay, err)
	}
	var pointerAfterReplay models.PublicPublicationState
	if err = f.db.Where("state_key=?", publicPublicationStateKey).First(&pointerAfterReplay).Error; err != nil {
		t.Fatal(err)
	}
	if *pointerAfterReplay.ContentReleaseID != *pointerBeforeReplay.ContentReleaseID || pointerAfterReplay.Revision != pointerBeforeReplay.Revision {
		t.Fatal("replay advanced pointer")
	}
	secondPriceGUID := persistence.NextGUID()
	secondPriceID := cloneContentPriceSnapshot(t, f.db, f.actor.ID, 2, secondPriceGUID, seed.modelKey)
	if err = f.db.Model(&models.PublicPublicationState{}).Where("state_key=?", publicPublicationStateKey).Updates(map[string]any{"price_snapshot_id": secondPriceID, "revision": gorm.Expr("revision+1")}).Error; err != nil {
		t.Fatal(err)
	}
	replay, err = s.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision - 1, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "publish"})
	if err != nil || replay.GUID != r.GUID {
		t.Fatalf("post-price replay=%#v err=%v", replay, err)
	}
	var committed models.PublicContentRelease
	if err = f.db.Where("guid=?", mustGUID(t, r.GUID)).First(&committed).Error; err != nil {
		t.Fatal(err)
	}
	if committed.ContentHash == "" || fmt.Sprint(committed.Payload["price_snapshot_guid"]) != seed.priceGUID || committed.Payload["price_snapshot_version"].(float64) != 1 {
		t.Fatalf("binding=%#v hash=%s", committed.Payload, committed.ContentHash)
	}
	beforeRestoreDraft := *d
	beforeRestoreCounts := contentDBCounts(t, f.db)
	var beforeState models.PublicPublicationState
	if err = f.db.Where("state_key=?", publicPublicationStateKey).First(&beforeState).Error; err != nil {
		t.Fatal(err)
	}
	brokenRestore := NewPublicContentService(f.db)
	brokenRestore.fail = func(point string) error {
		if point == "after_pointer" {
			return fmt.Errorf("injected restore")
		}
		return nil
	}
	if _, failureErr := brokenRestore.Restore(ctx, PublicContentRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, ReleaseGUID: r.GUID, IdempotencyKey: "restore-fail"}); status(failureErr) != 503 {
		t.Fatalf("restore failure=%v", failureErr)
	}
	afterDraft, loadErr := s.GetDraft(ctx, f.actor.ID)
	if loadErr != nil || *afterDraft != beforeRestoreDraft {
		t.Fatalf("restore draft rollback=%#v err=%v", afterDraft, loadErr)
	}
	var afterState models.PublicPublicationState
	_ = f.db.Where("state_key=?", publicPublicationStateKey).First(&afterState).Error
	if afterState.ContentReleaseID == nil || beforeState.ContentReleaseID == nil || *afterState.ContentReleaseID != *beforeState.ContentReleaseID || contentDBCounts(t, f.db) != beforeRestoreCounts {
		t.Fatal("restore failure changed committed state")
	}
	restored, err := s.Restore(ctx, PublicContentRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, ReleaseGUID: r.GUID, IdempotencyKey: "restore"})
	if err != nil || restored.GUID == r.GUID || restored.Version <= r.Version {
		t.Fatalf("restore=%#v err=%v", restored, err)
	}
	if _, err = s.Restore(ctx, PublicContentRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision + 1, ReleaseGUID: r.GUID, IdempotencyKey: "restore"}); status(err) != 409 {
		t.Fatalf("restore same key changed payload=%v", err)
	}
	restoreReplay, err := s.Restore(ctx, PublicContentRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: d.Revision, ReleaseGUID: r.GUID, IdempotencyKey: "restore"})
	if err != nil || restoreReplay.GUID != restored.GUID {
		t.Fatalf("restore replay=%#v err=%v", restoreReplay, err)
	}
	var restoredRow models.PublicContentRelease
	if err = f.db.Where("guid=?", mustGUID(t, restored.GUID)).First(&restoredRow).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(restoredRow.Payload["price_snapshot_guid"]) != fmt.Sprint(secondPriceGUID) || restoredRow.Payload["price_snapshot_version"].(float64) != 2 {
		t.Fatalf("restore binding=%#v", restoredRow.Payload)
	}
}

func TestStructuredHomePublishRestoreFiltersTombstonesAtomically(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ctx := context.Background()
	seed, err := seedContentPublicationFixture(tx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := NewPublicContentService(tx)
	documents, err := service.GetDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	homeText, about, terms, privacy, reviewed := "legacy", "about", "terms", "privacy", true
	documents, err = service.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: documents.Revision, Home: &homeText, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed})
	if err != nil {
		t.Fatal(err)
	}
	future := int64(1_800_000_000_000)
	home, err := service.CreateAnnouncement(ctx, f.actor.ID, AnnouncementCreateRequest{ExpectedRevision: documents.Revision, Title: "future", BodyMarkdown: "announcement", EffectiveAt: &future, IsVisible: true, SortOrder: 20})
	if err != nil {
		t.Fatal(err)
	}
	home, err = service.CreateFAQ(ctx, f.actor.ID, FAQCreateRequest{ExpectedRevision: home.Revision, Question: "question", AnswerMarkdown: "answer", IsVisible: true, SortOrder: 10})
	if err != nil {
		t.Fatal(err)
	}
	home, err = service.ReplaceFeaturedModels(ctx, f.actor.ID, FeaturedModelsSaveRequest{ExpectedRevision: home.Revision, FeaturedModelKeys: []string{seed.modelKey}})
	if err != nil {
		t.Fatal(err)
	}
	if issues, validateErr := service.Validate(ctx, f.actor.ID, home.Revision); validateErr != nil || len(issues) != 0 {
		t.Fatalf("structured validate issues=%+v err=%v", issues, validateErr)
	}
	for _, lifecycle := range []struct {
		name   string
		column string
		value  any
		reset  any
	}{
		{name: "inactive", column: "status", value: models.PublicModelConfigStatusInactive, reset: models.PublicModelConfigStatusActive},
		{name: "soft_deleted", column: "is_deleted", value: 1, reset: 0},
		{name: "missing_input", column: "input_price_usd_per_million_tokens", value: nil, reset: "1.00000000"},
		{name: "missing_output", column: "output_price_usd_per_million_tokens", value: nil, reset: "2.00000000"},
	} {
		t.Run("publish_rejects_"+lifecycle.name, func(t *testing.T) {
			before := contentDBCounts(t, tx)
			var stateBefore models.PublicPublicationState
			if err := tx.Where("state_key=?", publicPublicationStateKey).First(&stateBefore).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Model(&models.PublicModelConfig{}).Where("model_key=?", seed.modelKey).Update(lifecycle.column, lifecycle.value).Error; err != nil {
				t.Fatal(err)
			}
			_, publishErr := service.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: home.Revision, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "reject-" + lifecycle.name})
			if status(publishErr) != 422 {
				t.Fatalf("publish=%v", publishErr)
			}
			if err := tx.Model(&models.PublicModelConfig{}).Where("model_key=?", seed.modelKey).Update(lifecycle.column, lifecycle.reset).Error; err != nil {
				t.Fatal(err)
			}
			var stateAfter models.PublicPublicationState
			if err := tx.Where("state_key=?", publicPublicationStateKey).First(&stateAfter).Error; err != nil {
				t.Fatal(err)
			}
			if contentDBCounts(t, tx) != before || stateBefore.Revision != stateAfter.Revision || !sameOptionalInt64(stateBefore.ContentReleaseID, stateAfter.ContentReleaseID) {
				t.Fatal("rejected structured publish changed committed state")
			}
		})
	}
	release, err := service.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: home.Revision, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "structured-publish"})
	if err != nil {
		t.Fatal(err)
	}
	var published models.PublicContentRelease
	if err = tx.Where("guid=?", mustGUID(t, release.GUID)).First(&published).Error; err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePublishedContentPayloadV2(published.Payload)
	if err != nil || len(decoded.HomeConfig.Announcements) != 1 || decoded.HomeConfig.Announcements[0].EffectiveAt == nil || len(decoded.HomeConfig.FAQs) != 1 || !reflect.DeepEqual(decoded.HomeConfig.FeaturedModelKeys, []string{seed.modelKey}) {
		t.Fatalf("published=%#v decoded=%#v err=%v", published.Payload, decoded, err)
	}
	if _, err = service.DeleteAnnouncement(ctx, f.actor.ID, home.Announcements[0].GUID, AnnouncementDeleteRequest{ExpectedRevision: home.Revision}); err != nil {
		t.Fatal(err)
	}
	home, err = service.GetHomeDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.DeleteFAQ(ctx, f.actor.ID, home.FAQs[0].GUID, FAQDeleteRequest{ExpectedRevision: home.Revision}); err != nil {
		t.Fatal(err)
	}
	home, err = service.GetHomeDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Model(&models.PublicModelConfig{}).Where("model_key=?", seed.modelKey).Updates(map[string]any{"status": models.PublicModelConfigStatusInactive, "revision": gorm.Expr("revision+1")}).Error; err != nil {
		t.Fatal(err)
	}
	before := contentDBCounts(t, tx)
	var stateBefore models.PublicPublicationState
	if err = tx.Where("state_key=?", publicPublicationStateKey).First(&stateBefore).Error; err != nil {
		t.Fatal(err)
	}
	broken := NewPublicContentService(tx)
	broken.fail = func(point string) error {
		if point == "after_pointer" {
			return fmt.Errorf("injected structured restore")
		}
		return nil
	}
	if _, err = broken.Restore(ctx, PublicContentRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: home.Revision, ReleaseGUID: release.GUID, IdempotencyKey: "structured-restore-fail"}); status(err) != 503 {
		t.Fatalf("restore failure=%v", err)
	}
	var stateAfterFailure models.PublicPublicationState
	if err = tx.Where("state_key=?", publicPublicationStateKey).First(&stateAfterFailure).Error; err != nil {
		t.Fatal(err)
	}
	if contentDBCounts(t, tx) != before || !sameOptionalInt64(stateBefore.ContentReleaseID, stateAfterFailure.ContentReleaseID) || stateBefore.Revision != stateAfterFailure.Revision {
		t.Fatal("failed structured restore changed committed state")
	}
	restored, err := service.Restore(ctx, PublicContentRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: home.Revision, ReleaseGUID: release.GUID, IdempotencyKey: "structured-restore"})
	if err != nil {
		t.Fatal(err)
	}
	var restoredRow models.PublicContentRelease
	if err = tx.Where("guid=?", mustGUID(t, restored.GUID)).First(&restoredRow).Error; err != nil {
		t.Fatal(err)
	}
	restoredPayload, err := decodePublishedContentPayloadV2(restoredRow.Payload)
	if err != nil || len(restoredPayload.HomeConfig.Announcements) != 0 || len(restoredPayload.HomeConfig.FAQs) != 0 || len(restoredPayload.HomeConfig.FeaturedModelKeys) != 0 || restoredRow.ContentHash == published.ContentHash {
		t.Fatalf("restored=%#v err=%v", restoredRow, err)
	}
	var originalAfter models.PublicContentRelease
	if err = tx.First(&originalAfter, published.ID).Error; err != nil || originalAfter.ContentHash != published.ContentHash || !reflect.DeepEqual(originalAfter.Payload, published.Payload) {
		t.Fatalf("historical release mutated: before=%#v after=%#v err=%v", published, originalAfter, err)
	}
}

func TestPriceStructuredHomeDBRebindKeepsFeaturedKeysAndRollsBack(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	if err := requirePublicPriceSnapshotFixture(tx, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin := NewPublicModelAdminService(tx)
	input := f.input("structured-rebind-" + fmt.Sprint(persistence.NextGUID()))
	f.observe(t, input.UpstreamModelID)
	model, err := admin.Create(ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	model, err = admin.Activate(ctx, f.actor.ID, mustGUID(t, model.GUID), model.Revision)
	if err != nil {
		t.Fatal(err)
	}
	priceService := NewPublicPriceSnapshotService(tx)
	boundPrice, err := priceService.Publish(ctx, PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: publicPriceDraftRevision(t, tx), IdempotencyKey: "structured-price-initial"})
	if err != nil {
		t.Fatal(err)
	}
	contentService := NewPublicContentService(tx)
	draft, err := contentService.GetDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	homeText, about, terms, privacy, reviewed := "legacy", "about", "terms", "privacy", true
	draft, err = contentService.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: draft.Revision, Home: &homeText, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed})
	if err != nil {
		t.Fatal(err)
	}
	home, err := contentService.ReplaceFeaturedModels(ctx, f.actor.ID, FeaturedModelsSaveRequest{ExpectedRevision: draft.Revision, FeaturedModelKeys: []string{model.ModelKey}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = contentService.Publish(ctx, PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: home.Revision, PriceReleaseGUID: boundPrice.GUID, IdempotencyKey: "structured-content"}); err != nil {
		t.Fatal(err)
	}
	changedName := model.DisplayName + " updated"
	if _, err = admin.Update(ctx, f.actor.ID, mustGUID(t, model.GUID), UpdatePublicModelRequest{ExpectedRevision: model.Revision, DisplayName: &changedName}); err != nil {
		t.Fatal(err)
	}
	request := PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: publicPriceDraftRevision(t, tx), IdempotencyKey: "structured-price-rebind"}
	beforeCounts := publicPriceSnapshotFixtureCounts(t, tx, f.actor.ID)
	var beforeState models.PublicPublicationState
	if err = tx.Where("state_key=?", publicPublicationStateKey).First(&beforeState).Error; err != nil {
		t.Fatal(err)
	}
	broken := NewPublicPriceSnapshotService(tx)
	broken.fail = func(point string) error {
		if point == "content_release" {
			return fmt.Errorf("injected structured rebind")
		}
		return nil
	}
	if _, err = broken.Publish(ctx, request); status(err) != 503 {
		t.Fatalf("injected rebind=%v", err)
	}
	var afterFailure models.PublicPublicationState
	if err = tx.Where("state_key=?", publicPublicationStateKey).First(&afterFailure).Error; err != nil {
		t.Fatal(err)
	}
	if beforeCounts != publicPriceSnapshotFixtureCounts(t, tx, f.actor.ID) || beforeState.Revision != afterFailure.Revision || !sameOptionalInt64(beforeState.PriceSnapshotID, afterFailure.PriceSnapshotID) || !sameOptionalInt64(beforeState.ContentReleaseID, afterFailure.ContentReleaseID) {
		t.Fatal("failed structured price rebind changed committed state")
	}
	reboundPrice, err := priceService.Publish(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var state models.PublicPublicationState
	if err = tx.Where("state_key=?", publicPublicationStateKey).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	var content models.PublicContentRelease
	if err = tx.First(&content, *state.ContentReleaseID).Error; err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePublishedContentPayloadV2(content.Payload)
	if err != nil || !reflect.DeepEqual(decoded.HomeConfig.FeaturedModelKeys, []string{model.ModelKey}) || decoded.PriceSnapshotGUID != reboundPrice.GUID || decoded.PriceSnapshotVersion != reboundPrice.Version {
		t.Fatalf("rebound content=%#v price=%#v err=%v", decoded, reboundPrice, err)
	}
	if hash, hashErr := hashPublicContentPayload(content.Payload); hashErr != nil || hash != content.ContentHash {
		t.Fatalf("rebound hash=%s stored=%s err=%v", hash, content.ContentHash, hashErr)
	}
}

func cloneContentPriceSnapshot(t *testing.T, db *gorm.DB, actor, version, guid int64, modelKey string) int64 {
	t.Helper()
	now := persistence.NowMillis()
	p := models.PublicPriceSnapshot{Guid: guid, CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor, Version: version, Reason: models.PublicPriceSnapshotReasonRootPublish, SourceRevision: version, ContentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PublishedAt: now}
	if e := db.Create(&p).Error; e != nil {
		t.Fatal(e)
	}
	var old models.PublicPriceSnapshotItem
	if e := db.Where("model_key=?", modelKey).First(&old).Error; e != nil {
		t.Fatal(e)
	}
	old.ID = 0
	old.Guid = persistence.NextGUID()
	old.SnapshotID = p.ID
	old.CreatedAt = now
	old.UpdatedAt = now
	if e := db.Create(&old).Error; e != nil {
		t.Fatal(e)
	}
	return p.ID
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

type contentSeed struct{ priceGUID, modelKey string }

func seedContentPublicationFixture(db *gorm.DB, actor int64) (contentSeed, error) {
	for _, table := range []string{"public_content_drafts", "public_content_releases", "public_publication_state", "public_price_snapshots", "public_price_snapshot_items", "public_render_jobs"} {
		if !db.Migrator().HasTable(table) {
			return contentSeed{}, fmt.Errorf("BLOCKED_FIXTURE: migration missing %s", table)
		}
	}
	now := persistence.NowMillis()
	priceGUID := persistence.NextGUID()
	modelKey := "content-" + fmt.Sprint(persistence.NextGUID())
	price := models.PublicPriceSnapshot{Guid: priceGUID, CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor, Version: 1, Reason: models.PublicPriceSnapshotReasonRootPublish, SourceRevision: 1, ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PublishedAt: now}
	if e := db.Create(&price).Error; e != nil {
		return contentSeed{}, e
	}
	in, out := "1.00000000", "2.00000000"
	model := models.PublicModelConfig{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, ModelKey: modelKey, UpstreamModelID: "org/" + modelKey, DisplayName: "Content Model", Provider: "provider", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: &in, OutputPriceUSDPerMillionTokens: &out, Status: models.PublicModelConfigStatusActive, Revision: 1}
	if e := db.Create(&model).Error; e != nil {
		return contentSeed{}, e
	}
	item := models.PublicPriceSnapshotItem{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor, SnapshotID: price.ID, ModelConfigID: model.ID, ModelKey: modelKey, UpstreamModelID: "org/" + modelKey, DisplayName: "Content Model", Provider: "provider", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: &in, OutputPriceUSDPerMillionTokens: &out, PricingType: "token"}
	if e := db.Create(&item).Error; e != nil {
		return contentSeed{}, e
	}
	state := models.PublicPublicationState{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, StateKey: publicPublicationStateKey, PriceSnapshotID: &price.ID, PriceVisibility: models.PublicPriceVisibilityVisible, Revision: 1}
	if e := db.Create(&state).Error; e != nil {
		return contentSeed{}, e
	}
	return contentSeed{priceGUID: fmt.Sprint(priceGUID), modelKey: modelKey}, nil
}

func cleanPublicContentDBFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, statement := range []string{"DELETE FROM public_render_jobs", "DELETE FROM public_home_announcements", "DELETE FROM public_home_faqs", "DELETE FROM public_publication_state", "UPDATE public_content_releases SET restored_from_release_id=NULL", "DELETE FROM public_content_releases", "DELETE FROM public_content_drafts", "DELETE FROM public_price_snapshot_items", "UPDATE public_price_snapshots SET restored_from_snapshot_id=NULL", "DELETE FROM public_price_snapshots", "DELETE FROM public_model_configs WHERE model_key LIKE 'content-%' OR model_key LIKE 'featured-%'"} {
		if e := db.Exec(statement).Error; e != nil {
			t.Fatalf("fixture cleanup %q: %v", statement, e)
		}
	}
}
