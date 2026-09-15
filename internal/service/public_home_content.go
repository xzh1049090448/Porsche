package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/publiccontent"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	PublicHomeAnnouncementLimit      = 20
	PublicHomeFAQLimit               = 50
	PublicHomeFeaturedModelLimit     = 12
	PublicHomeMarkdownLimit          = 16 << 10
	PublicHomeSortOrderMaximum       = 1_000_000
	PublicContentDraftAggregateLimit = 256 << 10
)

type PublicHomeAnnouncementDraft struct {
	GUID         string  `json:"guid"`
	Title        string  `json:"title"`
	BodyMarkdown string  `json:"body_markdown"`
	EffectiveAt  *string `json:"effective_at"`
	IsVisible    bool    `json:"is_visible"`
	SortOrder    int     `json:"sort_order"`
}

type PublicHomeFAQDraft struct {
	GUID           string `json:"guid"`
	Question       string `json:"question"`
	AnswerMarkdown string `json:"answer_markdown"`
	IsVisible      bool   `json:"is_visible"`
	SortOrder      int    `json:"sort_order"`
}

type PublicHomeDraft struct {
	Revision          int64                         `json:"revision"`
	Announcements     []PublicHomeAnnouncementDraft `json:"announcements"`
	FAQs              []PublicHomeFAQDraft          `json:"faqs"`
	FeaturedModelKeys []string                      `json:"featured_model_keys"`
}

type PublicHomeDocumentsDraft struct {
	Revision      int64  `json:"revision"`
	About         string `json:"about"`
	Terms         string `json:"terms"`
	Privacy       string `json:"privacy"`
	LegalReviewed bool   `json:"legal_reviewed"`
}

type AnnouncementCreateRequest struct {
	ExpectedRevision int64
	Title            string
	BodyMarkdown     string
	EffectiveAt      *int64
	IsVisible        bool
	SortOrder        int
}

type OptionalNullableUnixMillis struct {
	Set   bool
	Value *int64
}

type AnnouncementUpdateRequest struct {
	ExpectedRevision int64
	Title            *string
	BodyMarkdown     *string
	EffectiveAt      OptionalNullableUnixMillis
	IsVisible        *bool
	SortOrder        *int
}

type AnnouncementDeleteRequest struct {
	ExpectedRevision int64
}

type FAQCreateRequest struct {
	ExpectedRevision int64
	Question         string
	AnswerMarkdown   string
	IsVisible        bool
	SortOrder        int
}

type FAQUpdateRequest struct {
	ExpectedRevision int64
	Question         *string
	AnswerMarkdown   *string
	IsVisible        *bool
	SortOrder        *int
}

type FAQDeleteRequest struct {
	ExpectedRevision int64
}

type FeaturedModelsSaveRequest struct {
	ExpectedRevision  int64
	FeaturedModelKeys []string
}

type DocumentsDraftSaveRequest struct {
	ExpectedRevision int64
	About            string
	Terms            string
	Privacy          string
	LegalReviewed    bool
}

type PublicHomeConfigAnnouncement struct {
	GUID        string  `json:"guid"`
	Title       string  `json:"title"`
	BodyHTML    string  `json:"body_html"`
	EffectiveAt *string `json:"effective_at"`
	SortOrder   int     `json:"sort_order"`
}

type PublicHomeConfigFAQ struct {
	GUID       string `json:"guid"`
	Question   string `json:"question"`
	AnswerHTML string `json:"answer_html"`
	SortOrder  int    `json:"sort_order"`
}

type PublicHomeConfig struct {
	Announcements         []PublicHomeConfigAnnouncement `json:"announcements"`
	FAQs                  []PublicHomeConfigFAQ          `json:"faqs"`
	FeaturedModelKeys     []string                       `json:"featured_model_keys"`
	ContentReleaseVersion int64                          `json:"content_release_version"`
	PriceReleaseVersion   int64                          `json:"price_release_version"`
}

func (s *PublicContentService) GetHomeDraft(ctx context.Context, actorID int64) (*PublicHomeDraft, error) {
	if actorID <= 0 {
		return nil, errBadRequest("invalid public home actor")
	}
	var out PublicHomeDraft
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPublicModelRoot(tx, actorID); e != nil {
			return e
		}
		draft, e := lockOrCreatePublicContentDraft(tx, actorID, s.now, s.nextGUID)
		if e != nil {
			return e
		}
		out, e = loadPublicHomeDraft(tx, draft)
		return e
	})
	if err != nil {
		return nil, mapPublicContentError(err)
	}
	return &out, nil
}

func (s *PublicContentService) GetDocumentsDraft(ctx context.Context, actorID int64) (*PublicHomeDocumentsDraft, error) {
	if actorID <= 0 {
		return nil, errBadRequest("invalid public content actor")
	}
	var out PublicHomeDocumentsDraft
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPublicModelRoot(tx, actorID); e != nil {
			return e
		}
		draft, e := lockOrCreatePublicContentDraft(tx, actorID, s.now, s.nextGUID)
		if e != nil {
			return e
		}
		out = projectPublicHomeDocumentsDraft(*draft)
		return nil
	})
	if err != nil {
		return nil, mapPublicContentError(err)
	}
	return &out, nil
}

func (s *PublicContentService) SaveDocumentsDraft(ctx context.Context, actorID int64, in DocumentsDraftSaveRequest) (*PublicHomeDocumentsDraft, error) {
	if actorID <= 0 || in.ExpectedRevision < 1 {
		return nil, errBadRequest("invalid public documents draft request")
	}
	for _, value := range []string{in.About, in.Terms, in.Privacy} {
		if len(value) > PublicContentDocumentLimit {
			return nil, errBadRequest("public content document too large")
		}
	}
	var out PublicHomeDocumentsDraft
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, e := lockPublicModelRoot(tx, actorID)
		if e != nil {
			return e
		}
		draft, e := lockOrCreatePublicContentDraft(tx, actor.ID, s.now, s.nextGUID)
		if e != nil {
			return e
		}
		if draft.Revision != in.ExpectedRevision {
			return errConflict("public content draft revision conflict")
		}
		now := s.now()
		if now <= 0 {
			return errUnavailable("public content draft persistence unavailable")
		}
		payload := clonePublicContentPayload(draft.Payload)
		payload["about"] = in.About
		payload["terms"] = in.Terms
		payload["privacy"] = in.Privacy
		payload["legal_reviewed"] = in.LegalReviewed
		review := models.PublicContentReviewPending
		if in.LegalReviewed {
			review = models.PublicContentReviewApproved
		}
		if e = validatePublicHomeAggregateDraftTx(tx, draft, payload); e != nil {
			return e
		}
		if e = s.writePublicHomeAudit(tx, actor.ID, draft.Guid, now, "public_content.home.documents.save", draft.Revision, models.JSONSlice{"about", "terms", "privacy", "legal_reviewed"}); e != nil {
			return e
		}
		if e = s.advancePublicContentDraft(tx, draft, actor.ID, now, payload, &review); e != nil {
			return e
		}
		if e = s.fail("public_home_after_cas"); e != nil {
			return e
		}
		draft.Payload = payload
		draft.ReviewState = review
		draft.Revision++
		out = projectPublicHomeDocumentsDraft(*draft)
		return nil
	})
	if err != nil {
		return nil, mapPublicContentError(err)
	}
	return &out, nil
}

func (s *PublicContentService) CreateAnnouncement(ctx context.Context, actorID int64, in AnnouncementCreateRequest) (*PublicHomeDraft, error) {
	if err := validateAnnouncementCreate(in); err != nil {
		return nil, err
	}
	return s.mutatePublicHome(ctx, actorID, in.ExpectedRevision, "public_content.home.announcement.create", models.JSONSlice{"title", "body_markdown", "effective_at", "is_visible", "sort_order"}, func(tx *gorm.DB, draft *models.PublicContentDraft, actorID, now int64) (int64, models.JSONMap, error) {
		var count int64
		if e := tx.Model(&models.PublicHomeAnnouncement{}).Where("content_draft_id=? AND is_deleted=0", draft.ID).Count(&count).Error; e != nil {
			return 0, nil, errUnavailable("public home announcement persistence unavailable")
		}
		if count >= PublicHomeAnnouncementLimit {
			return 0, nil, errBadRequest("public home announcement limit reached")
		}
		guid := s.nextGUID()
		if guid <= 0 {
			return 0, nil, errUnavailable("public home announcement persistence unavailable")
		}
		row := models.PublicHomeAnnouncement{AuditFields: publicHomeAuditFields(guid, now, actorID), ContentDraftID: draft.ID, Title: in.Title, BodyMarkdown: in.BodyMarkdown, EffectiveAt: clonePublicHomeInt64Pointer(in.EffectiveAt), IsVisible: boolInt(in.IsVisible), SortOrder: in.SortOrder, Revision: 1}
		if e := tx.Create(&row).Error; e != nil {
			return 0, nil, errUnavailable("public home announcement persistence unavailable")
		}
		return row.Guid, nil, nil
	})
}

func (s *PublicContentService) UpdateAnnouncement(ctx context.Context, actorID int64, guid string, in AnnouncementUpdateRequest) (*PublicHomeDraft, error) {
	target, err := parsePublicHomeTargetGUID(guid)
	if err != nil {
		return nil, err
	}
	if err = validateAnnouncementUpdate(in); err != nil {
		return nil, err
	}
	return s.mutatePublicHome(ctx, actorID, in.ExpectedRevision, "public_content.home.announcement.update", announcementChangedFields(in), func(tx *gorm.DB, draft *models.PublicContentDraft, actorID, now int64) (int64, models.JSONMap, error) {
		var row models.PublicHomeAnnouncement
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("content_draft_id=? AND guid=? AND is_deleted=0", draft.ID, target).First(&row).Error; e != nil {
			return 0, nil, publicHomeTargetError(e, "public home announcement not found")
		}
		updates := map[string]any{"revision": row.Revision + 1, "updated_at": now, "updated_by": actorID}
		if in.Title != nil {
			updates["title"] = *in.Title
		}
		if in.BodyMarkdown != nil {
			updates["body_markdown"] = *in.BodyMarkdown
		}
		if in.EffectiveAt.Set {
			updates["effective_at"] = in.EffectiveAt.Value
		}
		if in.IsVisible != nil {
			updates["is_visible"] = boolInt(*in.IsVisible)
		}
		if in.SortOrder != nil {
			updates["sort_order"] = *in.SortOrder
		}
		result := tx.Model(&models.PublicHomeAnnouncement{}).Where("id=? AND is_deleted=0", row.ID).Updates(updates)
		if result.Error != nil {
			return 0, nil, errUnavailable("public home announcement persistence unavailable")
		}
		if result.RowsAffected != 1 {
			return 0, nil, errNotFound("public home announcement not found")
		}
		return row.Guid, nil, nil
	})
}

func (s *PublicContentService) DeleteAnnouncement(ctx context.Context, actorID int64, guid string, in AnnouncementDeleteRequest) (int64, error) {
	target, err := parsePublicHomeTargetGUID(guid)
	if err != nil || in.ExpectedRevision < 1 {
		return 0, errBadRequest("invalid public home announcement request")
	}
	draft, err := s.mutatePublicHome(ctx, actorID, in.ExpectedRevision, "public_content.home.announcement.delete", models.JSONSlice{"is_deleted"}, func(tx *gorm.DB, aggregate *models.PublicContentDraft, actorID, now int64) (int64, models.JSONMap, error) {
		var row models.PublicHomeAnnouncement
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("content_draft_id=? AND guid=? AND is_deleted=0", aggregate.ID, target).First(&row).Error; e != nil {
			return 0, nil, publicHomeTargetError(e, "public home announcement not found")
		}
		result := tx.Model(&models.PublicHomeAnnouncement{}).Where("id=? AND is_deleted=0", row.ID).Updates(map[string]any{"is_deleted": 1, "revision": row.Revision + 1, "updated_at": now, "updated_by": actorID})
		if result.Error != nil {
			return 0, nil, errUnavailable("public home announcement persistence unavailable")
		}
		if result.RowsAffected != 1 {
			return 0, nil, errNotFound("public home announcement not found")
		}
		return row.Guid, nil, nil
	})
	if err != nil {
		return 0, err
	}
	return draft.Revision, nil
}

func (s *PublicContentService) CreateFAQ(ctx context.Context, actorID int64, in FAQCreateRequest) (*PublicHomeDraft, error) {
	if err := validateFAQCreate(in); err != nil {
		return nil, err
	}
	return s.mutatePublicHome(ctx, actorID, in.ExpectedRevision, "public_content.home.faq.create", models.JSONSlice{"question", "answer_markdown", "is_visible", "sort_order"}, func(tx *gorm.DB, draft *models.PublicContentDraft, actorID, now int64) (int64, models.JSONMap, error) {
		var count int64
		if e := tx.Model(&models.PublicHomeFAQ{}).Where("content_draft_id=? AND is_deleted=0", draft.ID).Count(&count).Error; e != nil {
			return 0, nil, errUnavailable("public home FAQ persistence unavailable")
		}
		if count >= PublicHomeFAQLimit {
			return 0, nil, errBadRequest("public home FAQ limit reached")
		}
		guid := s.nextGUID()
		if guid <= 0 {
			return 0, nil, errUnavailable("public home FAQ persistence unavailable")
		}
		row := models.PublicHomeFAQ{AuditFields: publicHomeAuditFields(guid, now, actorID), ContentDraftID: draft.ID, Question: in.Question, AnswerMarkdown: in.AnswerMarkdown, IsVisible: boolInt(in.IsVisible), SortOrder: in.SortOrder, Revision: 1}
		if e := tx.Create(&row).Error; e != nil {
			return 0, nil, errUnavailable("public home FAQ persistence unavailable")
		}
		return row.Guid, nil, nil
	})
}

func (s *PublicContentService) UpdateFAQ(ctx context.Context, actorID int64, guid string, in FAQUpdateRequest) (*PublicHomeDraft, error) {
	target, err := parsePublicHomeTargetGUID(guid)
	if err != nil {
		return nil, err
	}
	if err = validateFAQUpdate(in); err != nil {
		return nil, err
	}
	return s.mutatePublicHome(ctx, actorID, in.ExpectedRevision, "public_content.home.faq.update", faqChangedFields(in), func(tx *gorm.DB, draft *models.PublicContentDraft, actorID, now int64) (int64, models.JSONMap, error) {
		var row models.PublicHomeFAQ
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("content_draft_id=? AND guid=? AND is_deleted=0", draft.ID, target).First(&row).Error; e != nil {
			return 0, nil, publicHomeTargetError(e, "public home FAQ not found")
		}
		updates := map[string]any{"revision": row.Revision + 1, "updated_at": now, "updated_by": actorID}
		if in.Question != nil {
			updates["question"] = *in.Question
		}
		if in.AnswerMarkdown != nil {
			updates["answer_markdown"] = *in.AnswerMarkdown
		}
		if in.IsVisible != nil {
			updates["is_visible"] = boolInt(*in.IsVisible)
		}
		if in.SortOrder != nil {
			updates["sort_order"] = *in.SortOrder
		}
		result := tx.Model(&models.PublicHomeFAQ{}).Where("id=? AND is_deleted=0", row.ID).Updates(updates)
		if result.Error != nil {
			return 0, nil, errUnavailable("public home FAQ persistence unavailable")
		}
		if result.RowsAffected != 1 {
			return 0, nil, errNotFound("public home FAQ not found")
		}
		return row.Guid, nil, nil
	})
}

func (s *PublicContentService) DeleteFAQ(ctx context.Context, actorID int64, guid string, in FAQDeleteRequest) (int64, error) {
	target, err := parsePublicHomeTargetGUID(guid)
	if err != nil || in.ExpectedRevision < 1 {
		return 0, errBadRequest("invalid public home FAQ request")
	}
	draft, err := s.mutatePublicHome(ctx, actorID, in.ExpectedRevision, "public_content.home.faq.delete", models.JSONSlice{"is_deleted"}, func(tx *gorm.DB, aggregate *models.PublicContentDraft, actorID, now int64) (int64, models.JSONMap, error) {
		var row models.PublicHomeFAQ
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("content_draft_id=? AND guid=? AND is_deleted=0", aggregate.ID, target).First(&row).Error; e != nil {
			return 0, nil, publicHomeTargetError(e, "public home FAQ not found")
		}
		result := tx.Model(&models.PublicHomeFAQ{}).Where("id=? AND is_deleted=0", row.ID).Updates(map[string]any{"is_deleted": 1, "revision": row.Revision + 1, "updated_at": now, "updated_by": actorID})
		if result.Error != nil {
			return 0, nil, errUnavailable("public home FAQ persistence unavailable")
		}
		if result.RowsAffected != 1 {
			return 0, nil, errNotFound("public home FAQ not found")
		}
		return row.Guid, nil, nil
	})
	if err != nil {
		return 0, err
	}
	return draft.Revision, nil
}

func (s *PublicContentService) ReplaceFeaturedModels(ctx context.Context, actorID int64, in FeaturedModelsSaveRequest) (*PublicHomeDraft, error) {
	keys, err := normalizePublicHomeModelKeys(in.FeaturedModelKeys)
	if actorID <= 0 || in.ExpectedRevision < 1 || err != nil {
		return nil, errBadRequest("invalid featured models request")
	}
	return s.mutatePublicHome(ctx, actorID, in.ExpectedRevision, "public_content.home.featured_models.replace", models.JSONSlice{"featured_model_keys"}, func(tx *gorm.DB, draft *models.PublicContentDraft, actorID, now int64) (int64, models.JSONMap, error) {
		if len(keys) > 0 {
			var count int64
			if e := tx.Model(&models.PublicModelConfig{}).Where("model_key IN ? AND status=? AND is_deleted=0", keys, models.PublicModelConfigStatusActive).Count(&count).Error; e != nil {
				return 0, nil, errUnavailable("featured model persistence unavailable")
			}
			if count != int64(len(keys)) {
				return 0, nil, errBadRequest("invalid featured models")
			}
		}
		payload := clonePublicContentPayload(draft.Payload)
		payload["featured_model_keys"] = clonePublicHomeStrings(keys)
		return draft.Guid, payload, nil
	})
}

type publicHomeMutation func(*gorm.DB, *models.PublicContentDraft, int64, int64) (int64, models.JSONMap, error)

func (s *PublicContentService) mutatePublicHome(ctx context.Context, actorID, expectedRevision int64, action string, changedFields models.JSONSlice, mutation publicHomeMutation) (*PublicHomeDraft, error) {
	if actorID <= 0 || expectedRevision < 1 || mutation == nil {
		return nil, errBadRequest("invalid public home mutation")
	}
	var out PublicHomeDraft
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, e := lockPublicModelRoot(tx, actorID)
		if e != nil {
			return e
		}
		draft, e := lockOrCreatePublicContentDraft(tx, actor.ID, s.now, s.nextGUID)
		if e != nil {
			return e
		}
		if draft.Revision != expectedRevision {
			return errConflict("public content draft revision conflict")
		}
		now := s.now()
		if now <= 0 {
			return errUnavailable("public home persistence unavailable")
		}
		targetGUID, payload, e := mutation(tx, draft, actor.ID, now)
		if e != nil {
			return e
		}
		if e = validatePublicHomeAggregateDraftTx(tx, draft, payload); e != nil {
			return e
		}
		if e = s.fail("public_home_after_row"); e != nil {
			return e
		}
		if e = s.writePublicHomeAudit(tx, actor.ID, targetGUID, now, action, draft.Revision, changedFields); e != nil {
			return e
		}
		if e = s.fail("public_home_before_cas"); e != nil {
			return e
		}
		if e = s.advancePublicContentDraft(tx, draft, actor.ID, now, payload, nil); e != nil {
			return e
		}
		if e = s.fail("public_home_after_cas"); e != nil {
			return e
		}
		if payload != nil {
			draft.Payload = payload
		}
		draft.Revision++
		out, e = loadPublicHomeDraft(tx, draft)
		return e
	})
	if err != nil {
		return nil, mapPublicContentError(err)
	}
	return &out, nil
}

func (s *PublicContentService) advancePublicContentDraft(tx *gorm.DB, draft *models.PublicContentDraft, actorID, now int64, payload models.JSONMap, review *models.PublicContentReviewState) error {
	updates := map[string]any{"revision": draft.Revision + 1, "updated_at": now, "updated_by": actorID}
	if payload != nil {
		updates["payload"] = payload
	}
	if review != nil {
		updates["review_state"] = *review
	}
	result := tx.Model(&models.PublicContentDraft{}).Where("id=? AND revision=? AND is_deleted=0", draft.ID, draft.Revision).Updates(updates)
	return publicHomeCASResult(result)
}

func publicHomeCASResult(result *gorm.DB) error {
	if result == nil || result.Error != nil {
		return errUnavailable("public content draft persistence unavailable")
	}
	if result.RowsAffected != 1 {
		return errConflict("public content draft revision conflict")
	}
	return nil
}

func (s *PublicContentService) writePublicHomeAudit(tx *gorm.DB, actorID, targetGUID, now int64, action string, previousRevision int64, changedFields models.JSONSlice) error {
	if e := s.fail("public_home_audit"); e != nil {
		return e
	}
	auditGUID := s.nextGUID()
	if auditGUID <= 0 || targetGUID <= 0 {
		return errUnavailable("public home audit unavailable")
	}
	resource := "public-content/home-draft/" + strconv.FormatInt(targetGUID, 10)
	detail := publicHomeAuditDetail(targetGUID, previousRevision, changedFields)
	row := models.AuditLog{AuditFields: publicHomeAuditFields(auditGUID, now, actorID), UserID: &actorID, Action: action, Resource: &resource, Detail: detail}
	if e := tx.Create(&row).Error; e != nil {
		return errUnavailable("public home audit unavailable")
	}
	return nil
}

func publicHomeAuditDetail(targetGUID, previousRevision int64, changedFields models.JSONSlice) models.JSONMap {
	return models.JSONMap{"target_guid": strconv.FormatInt(targetGUID, 10), "previous_revision": previousRevision, "revision": previousRevision + 1, "result": "success", "changed_fields": append(models.JSONSlice(nil), changedFields...)}
}

func loadPublicHomeDraft(tx *gorm.DB, draft *models.PublicContentDraft) (PublicHomeDraft, error) {
	var announcements []models.PublicHomeAnnouncement
	if e := tx.Where("content_draft_id=? AND is_deleted=0", draft.ID).Find(&announcements).Error; e != nil {
		return PublicHomeDraft{}, errUnavailable("public home announcement persistence unavailable")
	}
	var faqs []models.PublicHomeFAQ
	if e := tx.Where("content_draft_id=? AND is_deleted=0", draft.ID).Find(&faqs).Error; e != nil {
		return PublicHomeDraft{}, errUnavailable("public home FAQ persistence unavailable")
	}
	keys, e := decodePublicHomeFeaturedKeys(draft.Payload)
	if e != nil {
		return PublicHomeDraft{}, errUnavailable("public content draft persistence unavailable")
	}
	out, e := ProjectPublicHomeDraft(draft.Revision, announcements, faqs, keys)
	if e != nil {
		return PublicHomeDraft{}, errUnavailable("public content draft persistence unavailable")
	}
	return out, nil
}

func validatePublicHomeAggregateDraftTx(tx *gorm.DB, draft *models.PublicContentDraft, candidatePayload models.JSONMap) error {
	var announcements []models.PublicHomeAnnouncement
	if err := tx.Where("content_draft_id=? AND is_deleted=0", draft.ID).Find(&announcements).Error; err != nil {
		return errUnavailable("public home announcement persistence unavailable")
	}
	var faqs []models.PublicHomeFAQ
	if err := tx.Where("content_draft_id=? AND is_deleted=0", draft.ID).Find(&faqs).Error; err != nil {
		return errUnavailable("public home FAQ persistence unavailable")
	}
	payload := candidatePayload
	if payload == nil {
		payload = draft.Payload
	}
	return validatePublicHomeAggregateDraftSize(payload, announcements, faqs)
}

func validatePublicHomeAggregateDraftSize(payload models.JSONMap, announcements []models.PublicHomeAnnouncement, faqs []models.PublicHomeFAQ) error {
	size, err := publicHomeAggregateDraftSize(payload, announcements, faqs)
	if err != nil {
		return errUnavailable("public content draft persistence unavailable")
	}
	if size > PublicContentDraftAggregateLimit {
		return errBadRequest("public content draft too large")
	}
	return nil
}

func publicHomeAggregateDraftSize(payload models.JSONMap, announcements []models.PublicHomeAnnouncement, faqs []models.PublicHomeFAQ) (int, error) {
	draft, err := ProjectPublicHomeDraft(1, announcements, faqs, nil)
	if err != nil {
		return 0, err
	}
	normalized := struct {
		Payload       models.JSONMap                `json:"payload"`
		Announcements []PublicHomeAnnouncementDraft `json:"announcements"`
		FAQs          []PublicHomeFAQDraft          `json:"faqs"`
	}{
		Payload:       payload,
		Announcements: draft.Announcements,
		FAQs:          draft.FAQs,
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

func projectPublicHomeDocumentsDraft(draft models.PublicContentDraft) PublicHomeDocumentsDraft {
	return PublicHomeDocumentsDraft{Revision: draft.Revision, About: jsonString(draft.Payload["about"]), Terms: jsonString(draft.Payload["terms"]), Privacy: jsonString(draft.Payload["privacy"]), LegalReviewed: jsonBool(draft.Payload["legal_reviewed"])}
}

func decodePublicHomeFeaturedKeys(payload models.JSONMap) ([]string, error) {
	value, ok := payload["featured_model_keys"]
	if !ok || value == nil {
		return []string{}, nil
	}
	var keys []string
	switch typed := value.(type) {
	case []string:
		keys = clonePublicHomeStrings(typed)
	case models.JSONSlice:
		keys = append([]string(nil), typed...)
	case []any:
		keys = make([]string, len(typed))
		for i, item := range typed {
			text, valid := item.(string)
			if !valid {
				return nil, fmt.Errorf("invalid featured model payload")
			}
			keys[i] = text
		}
	default:
		return nil, fmt.Errorf("invalid featured model payload")
	}
	return normalizePublicHomeModelKeys(keys)
}

func publicHomeFeaturedKeys(payload models.JSONMap) []string {
	keys, _ := decodePublicHomeFeaturedKeys(payload)
	return keys
}

func validateAnnouncementCreate(in AnnouncementCreateRequest) error {
	if in.ExpectedRevision < 1 || !validPublicHomeText(in.Title, 120) || len(in.BodyMarkdown) > PublicHomeMarkdownLimit || !validPublicHomeSortOrder(in.SortOrder) {
		return errBadRequest("invalid public home announcement request")
	}
	if in.EffectiveAt != nil {
		if _, err := formatPublicHomeMillis(*in.EffectiveAt); err != nil {
			return errBadRequest("invalid public home announcement request")
		}
	}
	return nil
}

func validateAnnouncementUpdate(in AnnouncementUpdateRequest) error {
	if in.ExpectedRevision < 1 || (in.Title == nil && in.BodyMarkdown == nil && !in.EffectiveAt.Set && in.IsVisible == nil && in.SortOrder == nil) {
		return errBadRequest("invalid public home announcement request")
	}
	if in.Title != nil && !validPublicHomeText(*in.Title, 120) || in.BodyMarkdown != nil && len(*in.BodyMarkdown) > PublicHomeMarkdownLimit || in.SortOrder != nil && !validPublicHomeSortOrder(*in.SortOrder) {
		return errBadRequest("invalid public home announcement request")
	}
	if in.EffectiveAt.Set && in.EffectiveAt.Value != nil {
		if _, err := formatPublicHomeMillis(*in.EffectiveAt.Value); err != nil {
			return errBadRequest("invalid public home announcement request")
		}
	}
	return nil
}

func announcementChangedFields(in AnnouncementUpdateRequest) models.JSONSlice {
	fields := make(models.JSONSlice, 0, 5)
	if in.Title != nil {
		fields = append(fields, "title")
	}
	if in.BodyMarkdown != nil {
		fields = append(fields, "body_markdown")
	}
	if in.EffectiveAt.Set {
		fields = append(fields, "effective_at")
	}
	if in.IsVisible != nil {
		fields = append(fields, "is_visible")
	}
	if in.SortOrder != nil {
		fields = append(fields, "sort_order")
	}
	return fields
}

func validateFAQCreate(in FAQCreateRequest) error {
	if in.ExpectedRevision < 1 || !validPublicHomeText(in.Question, 200) || len(in.AnswerMarkdown) > PublicHomeMarkdownLimit || !validPublicHomeSortOrder(in.SortOrder) {
		return errBadRequest("invalid public home FAQ request")
	}
	return nil
}

func validateFAQUpdate(in FAQUpdateRequest) error {
	if in.ExpectedRevision < 1 || (in.Question == nil && in.AnswerMarkdown == nil && in.IsVisible == nil && in.SortOrder == nil) {
		return errBadRequest("invalid public home FAQ request")
	}
	if in.Question != nil && !validPublicHomeText(*in.Question, 200) || in.AnswerMarkdown != nil && len(*in.AnswerMarkdown) > PublicHomeMarkdownLimit || in.SortOrder != nil && !validPublicHomeSortOrder(*in.SortOrder) {
		return errBadRequest("invalid public home FAQ request")
	}
	return nil
}

func faqChangedFields(in FAQUpdateRequest) models.JSONSlice {
	fields := make(models.JSONSlice, 0, 4)
	if in.Question != nil {
		fields = append(fields, "question")
	}
	if in.AnswerMarkdown != nil {
		fields = append(fields, "answer_markdown")
	}
	if in.IsVisible != nil {
		fields = append(fields, "is_visible")
	}
	if in.SortOrder != nil {
		fields = append(fields, "sort_order")
	}
	return fields
}

func parsePublicHomeTargetGUID(value string) (int64, error) {
	if !validPublicHomeGUID(value) {
		return 0, errBadRequest("invalid public home target")
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed, nil
}

func publicHomeTargetError(err error, message string) error {
	if err == gorm.ErrRecordNotFound {
		return errNotFound(message)
	}
	return errUnavailable("public home persistence unavailable")
}

func publicHomeAuditFields(guid, now, actorID int64) models.AuditFields {
	return models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID}
}

func clonePublicHomeInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ProjectPublicHomeDraft(revision int64, announcementRows []models.PublicHomeAnnouncement, faqRows []models.PublicHomeFAQ, featuredModelKeys []string) (PublicHomeDraft, error) {
	announcements := make([]PublicHomeAnnouncementDraft, 0, len(announcementRows))
	for _, row := range announcementRows {
		if row.IsDeleted != 0 {
			continue
		}
		if row.IsVisible != 0 && row.IsVisible != 1 {
			return PublicHomeDraft{}, fmt.Errorf("invalid announcement visibility")
		}
		var effectiveAt *string
		if row.EffectiveAt != nil {
			formatted, err := formatPublicHomeMillis(*row.EffectiveAt)
			if err != nil {
				return PublicHomeDraft{}, err
			}
			effectiveAt = &formatted
		}
		announcements = append(announcements, PublicHomeAnnouncementDraft{
			GUID:         strconv.FormatInt(row.Guid, 10),
			Title:        row.Title,
			BodyMarkdown: row.BodyMarkdown,
			EffectiveAt:  effectiveAt,
			IsVisible:    row.IsVisible == 1,
			SortOrder:    row.SortOrder,
		})
	}
	faqs := make([]PublicHomeFAQDraft, 0, len(faqRows))
	for _, row := range faqRows {
		if row.IsDeleted != 0 {
			continue
		}
		if row.IsVisible != 0 && row.IsVisible != 1 {
			return PublicHomeDraft{}, fmt.Errorf("invalid FAQ visibility")
		}
		faqs = append(faqs, PublicHomeFAQDraft{
			GUID:           strconv.FormatInt(row.Guid, 10),
			Question:       row.Question,
			AnswerMarkdown: row.AnswerMarkdown,
			IsVisible:      row.IsVisible == 1,
			SortOrder:      row.SortOrder,
		})
	}
	return NormalizePublicHomeDraft(PublicHomeDraft{
		Revision:          revision,
		Announcements:     announcements,
		FAQs:              faqs,
		FeaturedModelKeys: clonePublicHomeStrings(featuredModelKeys),
	})
}

func NormalizePublicHomeDraft(input PublicHomeDraft) (PublicHomeDraft, error) {
	if input.Revision < 1 {
		return PublicHomeDraft{}, fmt.Errorf("invalid home draft revision")
	}
	if len(input.Announcements) > PublicHomeAnnouncementLimit || len(input.FAQs) > PublicHomeFAQLimit {
		return PublicHomeDraft{}, fmt.Errorf("home draft collection limit exceeded")
	}
	keys, err := normalizePublicHomeModelKeys(input.FeaturedModelKeys)
	if err != nil {
		return PublicHomeDraft{}, err
	}
	announcements := append(make([]PublicHomeAnnouncementDraft, 0, len(input.Announcements)), input.Announcements...)
	announcementGUIDs := make(map[string]struct{}, len(announcements))
	for i := range announcements {
		announcement := &announcements[i]
		if !addUniquePublicHomeGUID(announcementGUIDs, announcement.GUID) || !validPublicHomeText(announcement.Title, 120) || len(announcement.BodyMarkdown) > PublicHomeMarkdownLimit || !validPublicHomeSortOrder(announcement.SortOrder) {
			return PublicHomeDraft{}, fmt.Errorf("invalid announcement")
		}
		if announcement.EffectiveAt != nil {
			value := *announcement.EffectiveAt
			if !validCanonicalPublicHomeTime(value) {
				return PublicHomeDraft{}, fmt.Errorf("invalid announcement effective time")
			}
			announcement.EffectiveAt = &value
		}
	}
	faqs := append(make([]PublicHomeFAQDraft, 0, len(input.FAQs)), input.FAQs...)
	faqGUIDs := make(map[string]struct{}, len(faqs))
	for i := range faqs {
		if !addUniquePublicHomeGUID(faqGUIDs, faqs[i].GUID) || !validPublicHomeText(faqs[i].Question, 200) || len(faqs[i].AnswerMarkdown) > PublicHomeMarkdownLimit || !validPublicHomeSortOrder(faqs[i].SortOrder) {
			return PublicHomeDraft{}, fmt.Errorf("invalid FAQ")
		}
	}
	sort.Slice(announcements, func(i, j int) bool { return lessPublicHomeAnnouncement(announcements[i], announcements[j]) })
	sort.Slice(faqs, func(i, j int) bool { return lessPublicHomeFAQ(faqs[i], faqs[j]) })
	return PublicHomeDraft{Revision: input.Revision, Announcements: announcements, FAQs: faqs, FeaturedModelKeys: keys}, nil
}

func NormalizePublicHomeConfig(input PublicHomeConfig) (PublicHomeConfig, error) {
	if input.ContentReleaseVersion < 1 || input.PriceReleaseVersion < 1 || len(input.Announcements) > PublicHomeAnnouncementLimit || len(input.FAQs) > PublicHomeFAQLimit {
		return PublicHomeConfig{}, fmt.Errorf("invalid public home config")
	}
	keys, err := normalizePublicHomeModelKeys(input.FeaturedModelKeys)
	if err != nil {
		return PublicHomeConfig{}, err
	}
	announcements := append(make([]PublicHomeConfigAnnouncement, 0, len(input.Announcements)), input.Announcements...)
	announcementGUIDs := make(map[string]struct{}, len(announcements))
	for i := range announcements {
		announcement := &announcements[i]
		if !addUniquePublicHomeGUID(announcementGUIDs, announcement.GUID) || !validPublicHomeText(announcement.Title, 120) || !validPublicHomeSortOrder(announcement.SortOrder) {
			return PublicHomeConfig{}, fmt.Errorf("invalid public announcement")
		}
		if announcement.EffectiveAt != nil {
			value := *announcement.EffectiveAt
			if !validCanonicalPublicHomeTime(value) {
				return PublicHomeConfig{}, fmt.Errorf("invalid public announcement effective time")
			}
			announcement.EffectiveAt = &value
		}
	}
	faqs := append(make([]PublicHomeConfigFAQ, 0, len(input.FAQs)), input.FAQs...)
	faqGUIDs := make(map[string]struct{}, len(faqs))
	for i := range faqs {
		if !addUniquePublicHomeGUID(faqGUIDs, faqs[i].GUID) || !validPublicHomeText(faqs[i].Question, 200) || !validPublicHomeSortOrder(faqs[i].SortOrder) {
			return PublicHomeConfig{}, fmt.Errorf("invalid public FAQ")
		}
	}
	sort.Slice(announcements, func(i, j int) bool {
		return lessPublicHomeConfigAnnouncement(announcements[i], announcements[j])
	})
	sort.Slice(faqs, func(i, j int) bool { return lessPublicHomeConfigFAQ(faqs[i], faqs[j]) })
	return PublicHomeConfig{
		Announcements:         announcements,
		FAQs:                  faqs,
		FeaturedModelKeys:     keys,
		ContentReleaseVersion: input.ContentReleaseVersion,
		PriceReleaseVersion:   input.PriceReleaseVersion,
	}, nil
}

func formatPublicHomeMillis(millis int64) (string, error) {
	if millis%1000 != 0 {
		return "", fmt.Errorf("effective time must have second precision")
	}
	formatted := time.UnixMilli(millis).UTC().Format(time.RFC3339)
	if !validCanonicalPublicHomeTime(formatted) {
		return "", fmt.Errorf("effective time is outside RFC3339 range")
	}
	return formatted, nil
}

func normalizePublicHomeModelKeys(input []string) ([]string, error) {
	if len(input) > PublicHomeFeaturedModelLimit {
		return nil, fmt.Errorf("featured model limit exceeded")
	}
	output := append(make([]string, 0, len(input)), input...)
	seen := make(map[string]struct{}, len(output))
	for _, key := range output {
		if !publiccontent.ValidModelKey(key) {
			return nil, fmt.Errorf("invalid featured model key")
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate featured model key")
		}
		seen[key] = struct{}{}
	}
	return output, nil
}

func clonePublicHomeStrings(input []string) []string {
	return append(make([]string, 0, len(input)), input...)
}

func validPublicHomeText(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) >= 1 && utf8.RuneCountInString(value) <= maximum && strings.TrimSpace(value) != "" && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validPublicHomeSortOrder(value int) bool {
	return value >= 0 && value <= PublicHomeSortOrderMaximum
}

func validPublicHomeGUID(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}

func addUniquePublicHomeGUID(seen map[string]struct{}, value string) bool {
	if !validPublicHomeGUID(value) {
		return false
	}
	if _, duplicate := seen[value]; duplicate {
		return false
	}
	seen[value] = struct{}{}
	return true
}

func validCanonicalPublicHomeTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339, value)
	return err == nil && parsed.Nanosecond() == 0 && parsed.Location() == time.UTC && parsed.UTC().Format(time.RFC3339) == value
}

// A nil effective_at means immediately effective and therefore sorts before
// every scheduled timestamp. Remaining ties use the numeric GUID value.
func lessPublicHomeAnnouncement(a, b PublicHomeAnnouncementDraft) bool {
	if a.SortOrder != b.SortOrder {
		return a.SortOrder < b.SortOrder
	}
	if a.EffectiveAt == nil || b.EffectiveAt == nil {
		if a.EffectiveAt == nil && b.EffectiveAt != nil {
			return true
		}
		if a.EffectiveAt != nil && b.EffectiveAt == nil {
			return false
		}
	} else if *a.EffectiveAt != *b.EffectiveAt {
		return *a.EffectiveAt < *b.EffectiveAt
	}
	return publicHomeGUIDValue(a.GUID) < publicHomeGUIDValue(b.GUID)
}

func lessPublicHomeFAQ(a, b PublicHomeFAQDraft) bool {
	if a.SortOrder != b.SortOrder {
		return a.SortOrder < b.SortOrder
	}
	return publicHomeGUIDValue(a.GUID) < publicHomeGUIDValue(b.GUID)
}

func lessPublicHomeConfigAnnouncement(a, b PublicHomeConfigAnnouncement) bool {
	return lessPublicHomeAnnouncement(
		PublicHomeAnnouncementDraft{GUID: a.GUID, EffectiveAt: a.EffectiveAt, SortOrder: a.SortOrder},
		PublicHomeAnnouncementDraft{GUID: b.GUID, EffectiveAt: b.EffectiveAt, SortOrder: b.SortOrder},
	)
}

func lessPublicHomeConfigFAQ(a, b PublicHomeConfigFAQ) bool {
	return lessPublicHomeFAQ(
		PublicHomeFAQDraft{GUID: a.GUID, SortOrder: a.SortOrder},
		PublicHomeFAQDraft{GUID: b.GUID, SortOrder: b.SortOrder},
	)
}

func publicHomeGUIDValue(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}
