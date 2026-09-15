package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func TestPublicContentLegacyDraftPayloadPreservesUnknownKeysWithoutAliasing(t *testing.T) {
	featured := []any{"alpha", "beta"}
	existing := models.JSONMap{
		"home":                "old",
		"about":               "old",
		"terms":               "old",
		"privacy":             "old",
		"legal_reviewed":      false,
		"featured_model_keys": featured,
		"future":              map[string]any{"enabled": true},
	}
	home, about, terms, privacy, reviewed := "home", "about", "terms", "privacy", true
	got := mergePublicContentDraftPayload(existing, PublicContentDraftSaveRequest{
		Home: &home, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed,
	})
	if !reflect.DeepEqual(got["featured_model_keys"], []any{"alpha", "beta"}) || !reflect.DeepEqual(got["future"], map[string]any{"enabled": true}) {
		t.Fatalf("unknown keys lost: %#v", got)
	}
	featured[0] = "mutated"
	existing["future"].(map[string]any)["enabled"] = false
	if !reflect.DeepEqual(got["featured_model_keys"], []any{"alpha", "beta"}) || !reflect.DeepEqual(got["future"], map[string]any{"enabled": true}) {
		t.Fatalf("payload aliases source: %#v", got)
	}
}

func TestPublicHomeCASRowsAffectedZeroIsConflict(t *testing.T) {
	if err := publicHomeCASResult(&gorm.DB{RowsAffected: 0}); status(err) != 409 {
		t.Fatalf("status=%d err=%v", status(err), err)
	}
	if err := publicHomeCASResult(&gorm.DB{RowsAffected: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestPublicHomeMutationValidationUsesTask3Boundaries(t *testing.T) {
	validAnnouncement := AnnouncementCreateRequest{ExpectedRevision: 1, Title: "title", BodyMarkdown: "body", IsVisible: true, SortOrder: 1}
	if err := validateAnnouncementCreate(validAnnouncement); err != nil {
		t.Fatal(err)
	}
	badAnnouncement := validAnnouncement
	badAnnouncement.Title = "\n"
	if status(validateAnnouncementCreate(badAnnouncement)) != 400 {
		t.Fatal("accepted invalid announcement title")
	}
	validFAQ := FAQCreateRequest{ExpectedRevision: 1, Question: "question", AnswerMarkdown: "answer", IsVisible: true, SortOrder: 1}
	if err := validateFAQCreate(validFAQ); err != nil {
		t.Fatal(err)
	}
	badFAQ := validFAQ
	badFAQ.AnswerMarkdown = strings.Repeat("x", PublicHomeMarkdownLimit+1)
	if status(validateFAQCreate(badFAQ)) != 400 {
		t.Fatal("accepted oversized FAQ answer")
	}
	if _, err := normalizePublicHomeModelKeys([]string{"alpha", "alpha"}); err == nil {
		t.Fatal("accepted duplicate featured model key")
	}
}

func TestPublicHomeAuditDetailContainsTargetRevisionAndResultOnly(t *testing.T) {
	got := publicHomeAuditDetail(123, 7, models.JSONSlice{"is_visible", "sort_order", "body_markdown"})
	want := models.JSONMap{"target_guid": "123", "previous_revision": int64(7), "revision": int64(8), "result": "success", "changed_fields": models.JSONSlice{"is_visible", "sort_order", "body_markdown"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detail=%#v", got)
	}
	encoded, _ := json.Marshal(got)
	for _, forbidden := range []string{"sensitive body value", "sensitive answer value", "password", "secret"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("audit detail contains %q: %s", forbidden, encoded)
		}
	}
	title, body, visible, order := "sensitive title value", "sensitive body value", false, 9
	if fields := announcementChangedFields(AnnouncementUpdateRequest{Title: &title, BodyMarkdown: &body, IsVisible: &visible, SortOrder: &order}); !reflect.DeepEqual(fields, models.JSONSlice{"title", "body_markdown", "is_visible", "sort_order"}) {
		t.Fatalf("announcement changed fields=%v", fields)
	}
	question, answer := "sensitive question value", "sensitive answer value"
	if fields := faqChangedFields(FAQUpdateRequest{Question: &question, AnswerMarkdown: &answer, IsVisible: &visible, SortOrder: &order}); !reflect.DeepEqual(fields, models.JSONSlice{"question", "answer_markdown", "is_visible", "sort_order"}) {
		t.Fatalf("FAQ changed fields=%v", fields)
	}
}

func TestPublicHomeAggregateDraftNormalizedSizeAllowsExactLimitAndRejectsOneMore(t *testing.T) {
	payload := models.JSONMap{
		"home": "", "about": "", "terms": "", "privacy": "", "legal_reviewed": false,
		"featured_model_keys": []string{}, "padding": "",
	}
	baseSize, err := publicHomeAggregateDraftSize(payload, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if baseSize >= PublicContentDraftAggregateLimit {
		t.Fatalf("base size=%d", baseSize)
	}
	payload["padding"] = strings.Repeat("x", PublicContentDraftAggregateLimit-baseSize)
	if size, err := publicHomeAggregateDraftSize(payload, nil, nil); err != nil || size != PublicContentDraftAggregateLimit {
		t.Fatalf("exact size=%d err=%v", size, err)
	}
	if err := validatePublicHomeAggregateDraftSize(payload, nil, nil); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	payload["padding"] = payload["padding"].(string) + "x"
	if err := validatePublicHomeAggregateDraftSize(payload, nil, nil); status(err) != 400 {
		t.Fatalf("over limit status=%d err=%v", status(err), err)
	}

	deleted := models.PublicHomeAnnouncement{AuditFields: models.AuditFields{Guid: 1, IsDeleted: 1}, Title: "deleted", BodyMarkdown: strings.Repeat("x", PublicHomeMarkdownLimit)}
	withoutDeleted, err := publicHomeAggregateDraftSize(models.JSONMap{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	withDeleted, err := publicHomeAggregateDraftSize(models.JSONMap{}, []models.PublicHomeAnnouncement{deleted}, nil)
	if err != nil || withDeleted != withoutDeleted {
		t.Fatalf("soft deleted item counted: without=%d with=%d err=%v", withoutDeleted, withDeleted, err)
	}
	activeFAQ := models.PublicHomeFAQ{AuditFields: models.AuditFields{Guid: 2}, Question: "active", AnswerMarkdown: "answer"}
	withActiveFAQ, err := publicHomeAggregateDraftSize(models.JSONMap{}, nil, []models.PublicHomeFAQ{activeFAQ})
	if err != nil || withActiveFAQ <= withoutDeleted {
		t.Fatalf("active FAQ not counted: without=%d with=%d err=%v", withoutDeleted, withActiveFAQ, err)
	}
}

func TestPublicHomeDraftProjectionFiltersDeletedConvertsAndSorts(t *testing.T) {
	early := int64(1_700_000_000_000)
	later := int64(1_800_000_000_000)
	rows := []models.PublicHomeAnnouncement{
		{AuditFields: models.AuditFields{Guid: 10}, Title: "ten", EffectiveAt: &later, IsVisible: 1, SortOrder: 1},
		{AuditFields: models.AuditFields{Guid: 2}, Title: "two", EffectiveAt: &early, IsVisible: 1, SortOrder: 1},
		{AuditFields: models.AuditFields{Guid: 9}, Title: "immediate", EffectiveAt: nil, SortOrder: 1},
		{AuditFields: models.AuditFields{Guid: 1, IsDeleted: 1}, Title: "deleted", SortOrder: 0},
	}
	faqs := []models.PublicHomeFAQ{
		{AuditFields: models.AuditFields{Guid: 10}, Question: "ten", SortOrder: 1},
		{AuditFields: models.AuditFields{Guid: 2}, Question: "two", SortOrder: 1},
		{AuditFields: models.AuditFields{Guid: 1, IsDeleted: 1}, Question: "deleted", SortOrder: 0},
	}
	keys := []string{"zeta", "alpha"}

	got, err := ProjectPublicHomeDraft(3, rows, faqs, keys)
	if err != nil {
		t.Fatal(err)
	}
	if ids := []string{got.Announcements[0].GUID, got.Announcements[1].GUID, got.Announcements[2].GUID}; !reflect.DeepEqual(ids, []string{"9", "2", "10"}) {
		t.Fatalf("announcement order=%v", ids)
	}
	if got.Announcements[1].EffectiveAt == nil || *got.Announcements[1].EffectiveAt != time.UnixMilli(early).UTC().Format(time.RFC3339) {
		t.Fatalf("effective_at=%v", got.Announcements[1].EffectiveAt)
	}
	if ids := []string{got.FAQs[0].GUID, got.FAQs[1].GUID}; !reflect.DeepEqual(ids, []string{"2", "10"}) {
		t.Fatalf("FAQ order=%v", ids)
	}
	if !reflect.DeepEqual(got.FeaturedModelKeys, keys) {
		t.Fatalf("featured order=%v", got.FeaturedModelKeys)
	}
	rows[0].Title = "mutated"
	faqs[0].Question = "mutated"
	keys[0] = "mutated"
	if got.Announcements[2].Title != "ten" || got.FAQs[1].Question != "ten" || got.FeaturedModelKeys[0] != "zeta" {
		t.Fatalf("projection aliases input: %#v", got)
	}
}

func TestPublicHomeProjectionUsesNumericGUIDOrderingAndNilEffectiveAtFirst(t *testing.T) {
	draft := PublicHomeDraft{
		Revision: 1,
		Announcements: []PublicHomeAnnouncementDraft{
			{GUID: "10", Title: "ten", SortOrder: 1},
			{GUID: "2", Title: "two", SortOrder: 1},
		},
		FAQs: []PublicHomeFAQDraft{
			{GUID: "10", Question: "ten", SortOrder: 1},
			{GUID: "2", Question: "two", SortOrder: 1},
		},
	}
	got, err := NormalizePublicHomeDraft(draft)
	if err != nil {
		t.Fatal(err)
	}
	if got.Announcements[0].GUID != "2" || got.FAQs[0].GUID != "2" {
		t.Fatalf("lexicographic GUID ordering used: %#v", got)
	}
}

func TestPublicHomeDraftNormalizationValidatesLimitsGUIDsAndKeys(t *testing.T) {
	valid := PublicHomeDraft{Revision: 1, Announcements: []PublicHomeAnnouncementDraft{}, FAQs: []PublicHomeFAQDraft{}, FeaturedModelKeys: []string{}}
	if _, err := NormalizePublicHomeDraft(valid); err != nil {
		t.Fatal(err)
	}
	atLimit := valid
	atLimit.Announcements = make([]PublicHomeAnnouncementDraft, PublicHomeAnnouncementLimit)
	for i := range atLimit.Announcements {
		atLimit.Announcements[i] = PublicHomeAnnouncementDraft{GUID: strconv.Itoa(i + 1), Title: "t"}
	}
	atLimit.FAQs = make([]PublicHomeFAQDraft, PublicHomeFAQLimit)
	for i := range atLimit.FAQs {
		atLimit.FAQs[i] = PublicHomeFAQDraft{GUID: strconv.Itoa(i + 1), Question: "q"}
	}
	atLimit.FeaturedModelKeys = make([]string, PublicHomeFeaturedModelLimit)
	for i := range atLimit.FeaturedModelKeys {
		atLimit.FeaturedModelKeys[i] = fmt.Sprintf("model-%d", i)
	}
	if _, err := NormalizePublicHomeDraft(atLimit); err != nil {
		t.Fatalf("collection limits rejected: %v", err)
	}
	for _, guid := range []string{"", "0", "-1", "+1", "01", "9223372036854775808"} {
		bad := valid
		bad.Announcements = []PublicHomeAnnouncementDraft{{GUID: guid, Title: "t"}}
		if _, err := NormalizePublicHomeDraft(bad); err == nil {
			t.Fatalf("accepted GUID %q", guid)
		}
	}
	for count, field := range map[int]string{PublicHomeAnnouncementLimit + 1: "announcement", PublicHomeFAQLimit + 1: "faq", PublicHomeFeaturedModelLimit + 1: "featured"} {
		bad := valid
		switch field {
		case "announcement":
			bad.Announcements = make([]PublicHomeAnnouncementDraft, count)
		case "faq":
			bad.FAQs = make([]PublicHomeFAQDraft, count)
		case "featured":
			bad.FeaturedModelKeys = make([]string, count)
		}
		if _, err := NormalizePublicHomeDraft(bad); err == nil {
			t.Fatalf("accepted %s count %d", field, count)
		}
	}
	duplicate := valid
	duplicate.FeaturedModelKeys = []string{"alpha", "alpha"}
	if _, err := NormalizePublicHomeDraft(duplicate); err == nil {
		t.Fatal("accepted duplicate featured keys")
	}
	invalidKey := valid
	invalidKey.FeaturedModelKeys = []string{" "}
	if _, err := NormalizePublicHomeDraft(invalidKey); err == nil {
		t.Fatal("accepted blank featured key")
	}
}

func TestPublicHomeProjectionRejectsSubsecondPersistedEffectiveAt(t *testing.T) {
	millis := int64(1_700_000_000_001)
	_, err := ProjectPublicHomeDraft(1, []models.PublicHomeAnnouncement{{AuditFields: models.AuditFields{Guid: 1}, Title: "t", EffectiveAt: &millis}}, nil, nil)
	if err == nil {
		t.Fatal("accepted persisted time that cannot round-trip at contract precision")
	}
}

func TestPublicHomeConfigNormalizationSortsCopiesAndUsesNonNilArrays(t *testing.T) {
	config := PublicHomeConfig{
		Announcements: []PublicHomeConfigAnnouncement{
			{GUID: "10", Title: "ten", BodyHTML: "<p>10</p>", SortOrder: 1},
			{GUID: "2", Title: "two", BodyHTML: "<p>2</p>", SortOrder: 1},
		},
		FAQs: []PublicHomeConfigFAQ{
			{GUID: "10", Question: "ten", AnswerHTML: "<p>10</p>", SortOrder: 1},
			{GUID: "2", Question: "two", AnswerHTML: "<p>2</p>", SortOrder: 1},
		},
		FeaturedModelKeys:     []string{"zeta", "alpha"},
		ContentReleaseVersion: 2,
		PriceReleaseVersion:   3,
	}
	got, err := NormalizePublicHomeConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if got.Announcements[0].GUID != "2" || got.FAQs[0].GUID != "2" || fmt.Sprint(got.FeaturedModelKeys) != "[zeta alpha]" {
		t.Fatalf("wrong normalized config: %#v", got)
	}
	config.Announcements[0].Title = "mutated"
	config.FAQs[0].Question = "mutated"
	config.FeaturedModelKeys[0] = "mutated"
	if got.Announcements[1].Title != "ten" || got.FAQs[1].Question != "ten" || got.FeaturedModelKeys[0] != "zeta" {
		t.Fatal("normalized config aliases input")
	}

	emptyDraft, err := NormalizePublicHomeDraft(PublicHomeDraft{Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	emptyConfig, err := NormalizePublicHomeConfig(PublicHomeConfig{ContentReleaseVersion: 1, PriceReleaseVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"draft": emptyDraft, "config": emptyConfig} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(encoded), ":null") {
			t.Fatalf("%s contains null array: %s", name, encoded)
		}
	}
}

func TestPublicHomeConfigNormalizationEnforcesCollectionLimits(t *testing.T) {
	config := PublicHomeConfig{ContentReleaseVersion: 1, PriceReleaseVersion: 1}
	config.Announcements = make([]PublicHomeConfigAnnouncement, PublicHomeAnnouncementLimit)
	for i := range config.Announcements {
		config.Announcements[i] = PublicHomeConfigAnnouncement{GUID: strconv.Itoa(i + 1), Title: "t"}
	}
	config.FAQs = make([]PublicHomeConfigFAQ, PublicHomeFAQLimit)
	for i := range config.FAQs {
		config.FAQs[i] = PublicHomeConfigFAQ{GUID: strconv.Itoa(i + 1), Question: "q"}
	}
	config.FeaturedModelKeys = make([]string, PublicHomeFeaturedModelLimit)
	for i := range config.FeaturedModelKeys {
		config.FeaturedModelKeys[i] = fmt.Sprintf("model-%d", i)
	}
	if _, err := NormalizePublicHomeConfig(config); err != nil {
		t.Fatalf("public response limits rejected: %v", err)
	}
	for _, field := range []string{"announcements", "faqs", "featured"} {
		bad := config
		switch field {
		case "announcements":
			bad.Announcements = append(bad.Announcements, PublicHomeConfigAnnouncement{GUID: "21", Title: "t"})
		case "faqs":
			bad.FAQs = append(bad.FAQs, PublicHomeConfigFAQ{GUID: "51", Question: "q"})
		case "featured":
			bad.FeaturedModelKeys = append(bad.FeaturedModelKeys, "model-extra")
		}
		if _, err := NormalizePublicHomeConfig(bad); err == nil {
			t.Fatalf("accepted public response over %s limit", field)
		}
	}
}

func TestPublicHomeDraftNormalizationRejectsDuplicateItemGUIDs(t *testing.T) {
	for _, test := range []struct {
		name  string
		draft PublicHomeDraft
	}{
		{
			name: "announcements",
			draft: PublicHomeDraft{
				Revision: 1,
				Announcements: []PublicHomeAnnouncementDraft{
					{GUID: "7", Title: "first"},
					{GUID: "7", Title: "second"},
				},
			},
		},
		{
			name: "FAQs",
			draft: PublicHomeDraft{
				Revision: 1,
				FAQs: []PublicHomeFAQDraft{
					{GUID: "7", Question: "first"},
					{GUID: "7", Question: "second"},
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizePublicHomeDraft(test.draft); err == nil {
				t.Fatal("accepted duplicate item GUID")
			}
		})
	}
}

func TestPublicHomeConfigNormalizationRejectsDuplicateItemGUIDs(t *testing.T) {
	for _, test := range []struct {
		name   string
		config PublicHomeConfig
	}{
		{
			name: "announcements",
			config: PublicHomeConfig{
				ContentReleaseVersion: 1,
				PriceReleaseVersion:   1,
				Announcements: []PublicHomeConfigAnnouncement{
					{GUID: "7", Title: "first"},
					{GUID: "7", Title: "second"},
				},
			},
		},
		{
			name: "FAQs",
			config: PublicHomeConfig{
				ContentReleaseVersion: 1,
				PriceReleaseVersion:   1,
				FAQs: []PublicHomeConfigFAQ{
					{GUID: "7", Question: "first"},
					{GUID: "7", Question: "second"},
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizePublicHomeConfig(test.config); err == nil {
				t.Fatal("accepted duplicate item GUID")
			}
		})
	}
}
