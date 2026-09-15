package service

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/publiccontent"
)

const (
	PublicHomeAnnouncementLimit  = 20
	PublicHomeFAQLimit           = 50
	PublicHomeFeaturedModelLimit = 12
	PublicHomeMarkdownLimit      = 16 << 10
	PublicHomeSortOrderMaximum   = 1_000_000
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
	for i := range announcements {
		announcement := &announcements[i]
		if !validPublicHomeGUID(announcement.GUID) || !validPublicHomeText(announcement.Title, 120) || len(announcement.BodyMarkdown) > PublicHomeMarkdownLimit || !validPublicHomeSortOrder(announcement.SortOrder) {
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
	for i := range faqs {
		if !validPublicHomeGUID(faqs[i].GUID) || !validPublicHomeText(faqs[i].Question, 200) || len(faqs[i].AnswerMarkdown) > PublicHomeMarkdownLimit || !validPublicHomeSortOrder(faqs[i].SortOrder) {
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
	for i := range announcements {
		announcement := &announcements[i]
		if !validPublicHomeGUID(announcement.GUID) || !validPublicHomeText(announcement.Title, 120) || !validPublicHomeSortOrder(announcement.SortOrder) {
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
	for i := range faqs {
		if !validPublicHomeGUID(faqs[i].GUID) || !validPublicHomeText(faqs[i].Question, 200) || !validPublicHomeSortOrder(faqs[i].SortOrder) {
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
