package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/publiccontent"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	xhtml "golang.org/x/net/html"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const PublicContentDocumentLimit = 256 << 10

type PublicContentDraft struct {
	Revision      int64  `json:"revision"`
	Home          string `json:"home"`
	About         string `json:"about"`
	Terms         string `json:"terms"`
	Privacy       string `json:"privacy"`
	LegalReviewed bool   `json:"legal_reviewed"`
}
type PublicContentDraftSaveRequest struct {
	ExpectedRevision int64   `json:"expected_revision"`
	Home             *string `json:"home"`
	About            *string `json:"about"`
	Terms            *string `json:"terms"`
	Privacy          *string `json:"privacy"`
	LegalReviewed    *bool   `json:"legal_reviewed"`
}
type PublicContentPublicationRequest struct {
	ActorID          int64  `json:"-"`
	ExpectedRevision int64  `json:"expected_revision"`
	PriceReleaseGUID string `json:"price_release_guid"`
	IdempotencyKey   string `json:"-"`
}
type PublicContentRestoreRequest struct {
	ActorID          int64  `json:"-"`
	ExpectedRevision int64  `json:"expected_revision"`
	ReleaseGUID      string `json:"-"`
	IdempotencyKey   string `json:"-"`
}
type PublicContentPreview struct {
	Document string `json:"document"`
	Revision int64  `json:"revision"`
}
type PublicContentRelease struct {
	GUID           string `json:"guid"`
	Version        int64  `json:"version"`
	Reason         string `json:"reason"`
	SourceRevision int64  `json:"source_revision"`
	CreatedAt      string `json:"created_at"`
}
type PublicContentReleaseView struct {
	Release PublicContentRelease `json:"release"`
	Content PublicContentDraft   `json:"content"`
}
type PublicContentPublicProjection struct {
	Content               PublicContentDraft `json:"content"`
	ContentReleaseVersion int64              `json:"content_release_version"`
	PriceReleaseVersion   int64              `json:"price_release_version"`
	ETag                  string             `json:"etag"`
}
type preparedPublicContent struct {
	Payload              models.JSONMap
	Hash                 string
	Documents            map[string]string
	PriceSnapshotID      int64
	PriceSnapshotGUID    int64
	PriceSnapshotVersion int64
}

type publishedHomeConfig struct {
	Announcements     []publishedAnnouncement `json:"announcements"`
	FAQs              []publishedFAQ          `json:"faqs"`
	FeaturedModelKeys []string                `json:"featured_model_keys"`
}

type publishedAnnouncement struct {
	GUID        string  `json:"guid"`
	Title       string  `json:"title"`
	BodyHTML    string  `json:"body_html"`
	EffectiveAt *string `json:"effective_at"`
	SortOrder   int     `json:"sort_order"`
}

type publishedFAQ struct {
	GUID       string `json:"guid"`
	Question   string `json:"question"`
	AnswerHTML string `json:"answer_html"`
	SortOrder  int    `json:"sort_order"`
}

type publishedContentPayloadV2 struct {
	SchemaVersion        int64               `json:"schema_version"`
	HomeConfig           publishedHomeConfig `json:"home_config"`
	Home                 string              `json:"home"`
	About                string              `json:"about"`
	Terms                string              `json:"terms"`
	Privacy              string              `json:"privacy"`
	LegalReviewed        bool                `json:"legal_reviewed"`
	PriceSnapshotGUID    string              `json:"price_snapshot_guid"`
	PriceSnapshotVersion int64               `json:"price_snapshot_version"`
}

type PublicContentService struct {
	db       *gorm.DB
	now      func() int64
	nextGUID func() int64
	fail     func(string) error
}

func NewPublicContentService(db *gorm.DB) *PublicContentService {
	return &PublicContentService{db: db, now: persistence.NowMillis, nextGUID: persistence.NextGUID, fail: func(string) error { return nil }}
}
func emptyPublicContentDraft() PublicContentDraft { return PublicContentDraft{Revision: 1} }

func (s *PublicContentService) GetDraft(ctx context.Context, actorID int64) (*PublicContentDraft, error) {
	if actorID <= 0 {
		return nil, errBadRequest("invalid public content actor")
	}
	var out PublicContentDraft
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPublicModelRoot(tx, actorID); e != nil {
			return e
		}
		d, e := lockOrCreatePublicContentDraft(tx, actorID, s.now, s.nextGUID)
		if e != nil {
			return e
		}
		out = projectContentDraft(*d)
		return nil
	})
	if err != nil {
		return nil, mapPublicContentError(err)
	}
	return &out, nil
}
func (s *PublicContentService) SaveDraft(ctx context.Context, actorID int64, in PublicContentDraftSaveRequest) (*PublicContentDraft, error) {
	if actorID <= 0 || in.ExpectedRevision < 1 || in.Home == nil || in.About == nil || in.Terms == nil || in.Privacy == nil || in.LegalReviewed == nil {
		return nil, errBadRequest("invalid public content draft request")
	}
	for _, v := range []*string{in.Home, in.About, in.Terms, in.Privacy} {
		if len(*v) > PublicContentDocumentLimit {
			return nil, errBadRequest("public content document too large")
		}
	}
	var out PublicContentDraft
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, e := lockPublicModelRoot(tx, actorID)
		if e != nil {
			return e
		}
		d, e := lockOrCreatePublicContentDraft(tx, actor.ID, s.now, s.nextGUID)
		if e != nil {
			return e
		}
		if d.Revision != in.ExpectedRevision {
			return errConflict("public content draft revision conflict")
		}
		now := s.now()
		payload := mergePublicContentDraftPayload(d.Payload, in)
		if e = validatePublicHomeAggregateDraftTx(tx, d, payload); e != nil {
			return e
		}
		review := models.PublicContentReviewPending
		if *in.LegalReviewed {
			review = models.PublicContentReviewApproved
		}
		r := tx.Model(&models.PublicContentDraft{}).Where("id=? AND revision=? AND is_deleted=0", d.ID, d.Revision).Updates(map[string]any{"payload": payload, "review_state": review, "revision": d.Revision + 1, "updated_at": now, "updated_by": actor.ID})
		if r.Error != nil {
			return errUnavailable("public content draft persistence unavailable")
		}
		if r.RowsAffected != 1 {
			return errConflict("public content draft revision conflict")
		}
		d.Payload = payload
		d.ReviewState = review
		d.Revision++
		out = projectContentDraft(*d)
		return nil
	})
	if err != nil {
		return nil, mapPublicContentError(err)
	}
	return &out, nil
}
func (s *PublicContentService) Preview(ctx context.Context, actorID, revision int64) (*PublicContentPreview, error) {
	d, e := s.GetDraft(ctx, actorID)
	if e != nil {
		return nil, e
	}
	if revision > 0 && revision != d.Revision {
		return nil, errConflict("public content draft revision conflict")
	}
	docs, _ := sanitizeContentDocuments(*d)
	b, _ := json.Marshal(docs)
	return &PublicContentPreview{Document: string(b), Revision: d.Revision}, nil
}
func (s *PublicContentService) Validate(ctx context.Context, actorID, revision int64) ([]publiccontent.ValidationIssue, error) {
	if actorID <= 0 || revision < 1 {
		return nil, errBadRequest("invalid public content validation request")
	}
	var issues []publiccontent.ValidationIssue
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, e := lockPublicModelRoot(tx, actorID)
		if e != nil {
			return e
		}
		draft, e := lockOrCreatePublicContentDraft(tx, actor.ID, s.now, s.nextGUID)
		if e != nil {
			return e
		}
		if revision != draft.Revision {
			return errConflict("public content draft revision conflict")
		}
		home, e := loadPublicHomeDraft(tx, draft)
		if e != nil {
			return e
		}
		var state models.PublicPublicationState
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; e != nil || state.PriceSnapshotID == nil {
			return errUnavailable("committed price snapshot unavailable")
		}
		var price models.PublicPriceSnapshot
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND is_deleted=0", *state.PriceSnapshotID).First(&price).Error; e != nil {
			return errUnavailable("committed price snapshot unavailable")
		}
		items, e := loadEligiblePublicPriceItems(tx, price.ID)
		if e != nil {
			return e
		}
		_, issues = preparePublicContent(projectContentDraft(*draft), home, price, items)
		return nil
	})
	if err != nil {
		return nil, mapPublicContentError(err)
	}
	return issues, nil
}

func loadEligiblePublicPriceItems(tx *gorm.DB, snapshotID int64) ([]models.PublicPriceSnapshotItem, error) {
	var items []models.PublicPriceSnapshotItem
	err := tx.Model(&models.PublicPriceSnapshotItem{}).
		Select("public_price_snapshot_items.*").
		Joins("JOIN public_model_configs m ON m.id=public_price_snapshot_items.model_config_id AND m.status=? AND m.is_deleted=0 AND m.input_price_usd_per_million_tokens IS NOT NULL AND m.output_price_usd_per_million_tokens IS NOT NULL", models.PublicModelConfigStatusActive).
		Where("public_price_snapshot_items.snapshot_id=? AND public_price_snapshot_items.is_deleted=0", snapshotID).
		Order("public_price_snapshot_items.model_key").Find(&items).Error
	if err != nil {
		return nil, errUnavailable("price snapshot unavailable")
	}
	return items, nil
}

func preparePublicContent(d PublicContentDraft, home PublicHomeDraft, price models.PublicPriceSnapshot, items []models.PublicPriceSnapshotItem) (*preparedPublicContent, []publiccontent.ValidationIssue) {
	issues := []publiccontent.ValidationIssue{}
	if home.Revision != d.Revision {
		issues = append(issues, publiccontent.ValidationIssue{Field: "home_config.revision", Code: "revision_mismatch"})
	}
	if normalized, err := NormalizePublicHomeDraft(home); err != nil {
		issues = append(issues, publiccontent.ValidationIssue{Field: "home_config", Code: "invalid_home_config"})
	} else {
		home = normalized
	}
	for _, x := range []struct{ name, value string }{{"home", d.Home}, {"about", d.About}, {"terms", d.Terms}, {"privacy", d.Privacy}} {
		if len(x.value) > PublicContentDocumentLimit {
			issues = append(issues, publiccontent.ValidationIssue{Field: x.name, Code: "content_too_large"})
		}
	}
	modelsV := make([]publiccontent.Model, 0, len(items))
	for _, i := range items {
		if i.IsDeleted != 0 {
			continue
		}
		modelsV = append(modelsV, publiccontent.Model{ModelKey: i.ModelKey, UpstreamModelID: i.UpstreamModelID, Active: true, Price: publiccontent.Price{Currency: publiccontent.CurrencyUSD, Unit: publiccontent.UnitMillionTokens, Input: publicPriceValue(i.InputPriceUSDPerMillionTokens), Output: publicPriceValue(i.OutputPriceUSDPerMillionTokens)}})
	}
	refs := extractPublicContentModelReferences(d.Home)
	pub := publiccontent.Publication{Models: modelsV, HomeModelKeys: append(append([]string(nil), refs...), home.FeaturedModelKeys...), Documents: []publiccontent.Document{{Kind: publiccontent.DocumentHome, Body: d.Home}, {Kind: publiccontent.DocumentKind("about"), Body: d.About}, {Kind: publiccontent.DocumentTerms, Body: d.Terms, Reviewed: d.LegalReviewed}, {Kind: publiccontent.DocumentPrivacy, Body: d.Privacy, Reviewed: d.LegalReviewed}}}
	issues = append(issues, publiccontent.ValidatePublication(pub)...)
	issues = append(issues, validatePublishedFeaturedModels(home.FeaturedModelKeys, items, true)...)
	docs, sanitizeIssues := sanitizeContentDocuments(d)
	issues = append(issues, sanitizeIssues...)
	published := publishedHomeConfig{Announcements: []publishedAnnouncement{}, FAQs: []publishedFAQ{}, FeaturedModelKeys: clonePublicHomeStrings(home.FeaturedModelKeys)}
	for index, announcement := range home.Announcements {
		if !announcement.IsVisible {
			continue
		}
		body, bodyIssues := sanitizeContentHTML("home_config.announcements["+strconv.Itoa(index)+"].body", announcement.BodyMarkdown, true)
		issues = append(issues, bodyIssues...)
		published.Announcements = append(published.Announcements, publishedAnnouncement{GUID: announcement.GUID, Title: announcement.Title, BodyHTML: body, EffectiveAt: clonePublicHomeStringPointer(announcement.EffectiveAt), SortOrder: announcement.SortOrder})
	}
	for index, faq := range home.FAQs {
		if !faq.IsVisible {
			continue
		}
		answer, answerIssues := sanitizeContentHTML("home_config.faqs["+strconv.Itoa(index)+"].answer", faq.AnswerMarkdown, true)
		issues = append(issues, answerIssues...)
		published.FAQs = append(published.FAQs, publishedFAQ{GUID: faq.GUID, Question: faq.Question, AnswerHTML: answer, SortOrder: faq.SortOrder})
	}
	if len(issues) > 0 {
		return nil, issues
	}
	normalized, normalizeErr := normalizePublishedHomeConfig(published)
	if normalizeErr != nil {
		return nil, []publiccontent.ValidationIssue{{Field: "home_config", Code: "invalid_home_config"}}
	}
	payload := models.JSONMap{"schema_version": int64(2), "home_config": publishedHomeConfigPayload(normalized), "home": docs["home"], "about": docs["about"], "terms": docs["terms"], "privacy": docs["privacy"], "legal_reviewed": true, "price_snapshot_guid": strconv.FormatInt(price.Guid, 10), "price_snapshot_version": price.Version}
	hash, e := hashPublicContentPayload(payload)
	if e != nil {
		return nil, []publiccontent.ValidationIssue{{Field: "content", Code: "serialization_failed"}}
	}
	return &preparedPublicContent{Payload: payload, Hash: hash, Documents: docs, PriceSnapshotID: price.ID, PriceSnapshotGUID: price.Guid, PriceSnapshotVersion: price.Version}, issues
}

func clonePublicHomeStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sanitizeContentHTML(field, raw string, requireVisible bool) (string, []publiccontent.ValidationIssue) {
	sanitized, issues := publiccontent.SanitizeMarkdown(raw)
	if len(issues) != 0 {
		mapped := make([]publiccontent.ValidationIssue, 0, len(issues))
		for _, issue := range issues {
			mapped = append(mapped, publiccontent.ValidationIssue{Field: field, Code: issue.Code})
		}
		return "", mapped
	}
	var rendered bytes.Buffer
	if err := goldmark.New(goldmark.WithRendererOptions(goldmarkhtml.WithUnsafe())).Convert([]byte(sanitized), &rendered); err != nil {
		return "", []publiccontent.ValidationIssue{{Field: field, Code: "serialization_failed"}}
	}
	html := rendered.String()
	if requireVisible && !publicContentHTMLHasVisibleContent(html) {
		return "", []publiccontent.ValidationIssue{{Field: field, Code: "empty_sanitized_content"}}
	}
	return html, nil
}

func validateCanonicalPublishedHTML(raw string, allowEmpty bool) error {
	rendered, issues := sanitizeContentHTML("content", raw, false)
	if len(issues) != 0 || rendered != raw {
		return fmt.Errorf("noncanonical published HTML")
	}
	if !allowEmpty && !publicContentHTMLHasVisibleContent(raw) {
		return fmt.Errorf("empty published HTML")
	}
	return nil
}

func publicContentHTMLHasVisibleContent(raw string) bool {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(raw))
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return false
		case xhtml.TextToken:
			if strings.TrimSpace(string(tokenizer.Text())) != "" {
				return true
			}
		}
	}
}

func normalizePublishedHomeConfig(input publishedHomeConfig) (publishedHomeConfig, error) {
	config := PublicHomeConfig{ContentReleaseVersion: 1, PriceReleaseVersion: 1, FeaturedModelKeys: clonePublicHomeStrings(input.FeaturedModelKeys), Announcements: make([]PublicHomeConfigAnnouncement, len(input.Announcements)), FAQs: make([]PublicHomeConfigFAQ, len(input.FAQs))}
	for index, announcement := range input.Announcements {
		config.Announcements[index] = PublicHomeConfigAnnouncement{GUID: announcement.GUID, Title: announcement.Title, BodyHTML: announcement.BodyHTML, EffectiveAt: clonePublicHomeStringPointer(announcement.EffectiveAt), SortOrder: announcement.SortOrder}
	}
	for index, faq := range input.FAQs {
		config.FAQs[index] = PublicHomeConfigFAQ{GUID: faq.GUID, Question: faq.Question, AnswerHTML: faq.AnswerHTML, SortOrder: faq.SortOrder}
	}
	normalized, err := NormalizePublicHomeConfig(config)
	if err != nil {
		return publishedHomeConfig{}, err
	}
	output := publishedHomeConfig{Announcements: make([]publishedAnnouncement, len(normalized.Announcements)), FAQs: make([]publishedFAQ, len(normalized.FAQs)), FeaturedModelKeys: clonePublicHomeStrings(normalized.FeaturedModelKeys)}
	for index, announcement := range normalized.Announcements {
		output.Announcements[index] = publishedAnnouncement{GUID: announcement.GUID, Title: announcement.Title, BodyHTML: announcement.BodyHTML, EffectiveAt: clonePublicHomeStringPointer(announcement.EffectiveAt), SortOrder: announcement.SortOrder}
	}
	for index, faq := range normalized.FAQs {
		output.FAQs[index] = publishedFAQ{GUID: faq.GUID, Question: faq.Question, AnswerHTML: faq.AnswerHTML, SortOrder: faq.SortOrder}
	}
	return output, nil
}

func publishedHomeConfigPayload(config publishedHomeConfig) models.JSONMap {
	announcements := make([]models.JSONMap, len(config.Announcements))
	for index, announcement := range config.Announcements {
		var effectiveAt any
		if announcement.EffectiveAt != nil {
			effectiveAt = *announcement.EffectiveAt
		}
		announcements[index] = models.JSONMap{"guid": announcement.GUID, "title": announcement.Title, "body_html": announcement.BodyHTML, "effective_at": effectiveAt, "sort_order": announcement.SortOrder}
	}
	faqs := make([]models.JSONMap, len(config.FAQs))
	for index, faq := range config.FAQs {
		faqs[index] = models.JSONMap{"guid": faq.GUID, "question": faq.Question, "answer_html": faq.AnswerHTML, "sort_order": faq.SortOrder}
	}
	return models.JSONMap{"announcements": announcements, "faqs": faqs, "featured_model_keys": clonePublicHomeStrings(config.FeaturedModelKeys)}
}

type publicContentHTMLTag struct {
	name       string
	suppressed bool
}
type publicContentHTMLState struct {
	stack      []publicContentHTMLTag
	suppressed int
}

func extractPublicContentModelReferences(raw string) []string {
	source := []byte(raw)
	document := goldmark.New().Parser().Parse(text.NewReader(source))
	keys := map[string]struct{}{}
	state := publicContentHTMLState{}
	add := func(rawURL string) {
		if key, ok := publicContentModelKey(rawURL); ok {
			keys[key] = struct{}{}
		}
	}
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.CodeSpan:
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			add(string(typed.Destination))
		case *ast.AutoLink:
			add(string(typed.URL(source)))
		case *ast.RawHTML:
			state.consume(string(typed.Text(source)), add)
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			state.consume(string(typed.Text(source)), add)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func publicContentModelKey(raw string) (string, bool) {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "%\\") || strings.HasPrefix(raw, "//") {
		return "", false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "pricing" || !publiccontent.ValidModelKey(parts[1]) || parsed.Path != "/pricing/"+parts[1] {
		return "", false
	}
	return parts[1], true
}

func (state *publicContentHTMLState) consume(raw string, add func(string)) {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(raw))
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return
		case xhtml.StartTagToken:
			name, more := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			visible := state.suppressed == 0
			for more {
				key, value, next := tokenizer.TagAttr()
				if visible && tag == "a" && strings.EqualFold(string(key), "href") {
					add(string(value))
				}
				more = next
			}
			suppressed := state.suppressed > 0 || tag == "code" || tag == "pre"
			state.stack = append(state.stack, publicContentHTMLTag{tag, suppressed})
			if suppressed {
				state.suppressed++
			}
		case xhtml.SelfClosingTagToken:
			name, more := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			visible := state.suppressed == 0
			for more {
				key, value, next := tokenizer.TagAttr()
				if visible && tag == "a" && strings.EqualFold(string(key), "href") {
					add(string(value))
				}
				more = next
			}
		case xhtml.EndTagToken:
			name, _ := tokenizer.TagName()
			state.close(strings.ToLower(string(name)))
		}
	}
}
func (state *publicContentHTMLState) close(name string) {
	for i := len(state.stack) - 1; i >= 0; i-- {
		if state.stack[i].name != name {
			continue
		}
		for _, tag := range state.stack[i:] {
			if tag.suppressed {
				state.suppressed--
			}
		}
		state.stack = state.stack[:i]
		return
	}
}
func sanitizeContentDocuments(d PublicContentDraft) (map[string]string, []publiccontent.ValidationIssue) {
	out := map[string]string{}
	issues := []publiccontent.ValidationIssue{}
	for _, x := range []struct{ name, value string }{{"home", d.Home}, {"about", d.About}, {"terms", d.Terms}, {"privacy", d.Privacy}} {
		v, is := sanitizeContentHTML(x.name, x.value, false)
		out[x.name] = v
		issues = append(issues, is...)
	}
	return out, issues
}
func hasPublicContentIssue(v []publiccontent.ValidationIssue, code string) bool {
	for _, i := range v {
		if i.Code == code {
			return true
		}
	}
	return false
}

func validateContentReleaseForPriceItems(release models.PublicContentRelease, items []models.PublicPriceSnapshotItem) error {
	if err := verifyPublicContentRelease(release); err != nil {
		return err
	}
	keys, err := publishedContentModelKeys(release.Payload)
	if err != nil {
		return errUnavailable("committed content integrity unavailable")
	}
	_, structured := jsonNumberInt64(release.Payload["schema_version"])
	issues := validatePublishedFeaturedModels(keys, items, structured)
	if len(issues) != 0 {
		return errConflict("published content is incompatible with candidate price snapshot")
	}
	return nil
}

func prepareContentReleaseRebinding(release models.PublicContentRelease, snapshot models.PublicPriceSnapshot, items []models.PublicPriceSnapshotItem) (*preparedPublicContent, error) {
	if err := verifyPublicContentRelease(release); err != nil {
		return nil, err
	}
	keys, err := publishedContentModelKeys(release.Payload)
	if err != nil {
		return nil, errUnavailable("committed content integrity unavailable")
	}
	_, structured := jsonNumberInt64(release.Payload["schema_version"])
	issues := validatePublishedFeaturedModels(keys, items, structured)
	if len(issues) != 0 {
		return nil, errConflict("published content is incompatible with candidate price snapshot")
	}
	payload := clonePublicContentPayload(release.Payload)
	payload["price_snapshot_guid"] = strconv.FormatInt(snapshot.Guid, 10)
	payload["price_snapshot_version"] = snapshot.Version
	hash, err := hashPublicContentPayload(payload)
	if err != nil {
		return nil, errUnavailable("committed content integrity unavailable")
	}
	return &preparedPublicContent{Payload: payload, Hash: hash, Documents: map[string]string{"home": jsonString(payload["home"]), "about": jsonString(payload["about"]), "terms": jsonString(payload["terms"]), "privacy": jsonString(payload["privacy"])}, PriceSnapshotID: snapshot.ID, PriceSnapshotGUID: snapshot.Guid, PriceSnapshotVersion: snapshot.Version}, nil
}

func validatePublishedFeaturedModels(keys []string, items []models.PublicPriceSnapshotItem, requireTokenPricing bool) []publiccontent.ValidationIssue {
	available := make(map[string]models.PublicPriceSnapshotItem, len(items))
	for _, item := range items {
		if item.IsDeleted == 0 {
			available[item.ModelKey] = item
		}
	}
	issues := make([]publiccontent.ValidationIssue, 0)
	seen := make(map[string]struct{}, len(keys))
	if len(keys) > PublicHomeFeaturedModelLimit {
		issues = append(issues, publiccontent.ValidationIssue{Field: "home_config.featured_model_keys", Code: "too_many_models"})
	}
	for index, key := range keys {
		field := "home_config.featured_model_keys[" + strconv.Itoa(index) + "]"
		if !publiccontent.ValidModelKey(key) {
			issues = append(issues, publiccontent.ValidationIssue{Field: field, Code: "invalid_model_key"})
		}
		if _, duplicate := seen[key]; duplicate {
			issues = append(issues, publiccontent.ValidationIssue{Field: field, Code: "duplicate_model_key"})
		} else {
			seen[key] = struct{}{}
		}
		item, ok := available[key]
		if !ok {
			issues = append(issues, publiccontent.ValidationIssue{Field: field, Code: "unknown_home_model"})
			continue
		}
		if requireTokenPricing && item.PricingType != "token" {
			issues = append(issues, publiccontent.ValidationIssue{Field: field, Code: "unsupported_pricing_type"})
		}
		if item.InputPriceUSDPerMillionTokens == nil {
			issues = append(issues, publiccontent.ValidationIssue{Field: field, Code: "missing_input_price"})
		} else if _, err := publiccontent.ParseDecimal(*item.InputPriceUSDPerMillionTokens); err != nil {
			issues = append(issues, publiccontent.ValidationIssue{Field: field, Code: "invalid_input_price"})
		}
		if item.OutputPriceUSDPerMillionTokens == nil {
			issues = append(issues, publiccontent.ValidationIssue{Field: field, Code: "missing_output_price"})
		} else if _, err := publiccontent.ParseDecimal(*item.OutputPriceUSDPerMillionTokens); err != nil {
			issues = append(issues, publiccontent.ValidationIssue{Field: field, Code: "invalid_output_price"})
		}
	}
	return issues
}

func publishedContentModelKeys(payload models.JSONMap) ([]string, error) {
	if version, ok := jsonNumberInt64(payload["schema_version"]); ok {
		if version != 2 {
			return nil, fmt.Errorf("unsupported schema")
		}
		decoded, err := decodePublishedContentPayloadV2(payload)
		if err != nil {
			return nil, err
		}
		return clonePublicHomeStrings(decoded.HomeConfig.FeaturedModelKeys), nil
	}
	return decodeLegacyContentModelKeys(payload["model_keys"])
}

func decodeLegacyContentModelKeys(value any) ([]string, error) {
	var keys []string
	switch refs := value.(type) {
	case []string:
		keys = append([]string(nil), refs...)
	case models.JSONSlice:
		keys = append([]string(nil), refs...)
	case []any:
		for _, value := range refs {
			key, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("invalid model keys")
			}
			keys = append(keys, key)
		}
	default:
		return nil, fmt.Errorf("invalid model keys")
	}
	return keys, nil
}

func hashPublicContentPayload(payload models.JSONMap) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var canonical any
	if err = decoder.Decode(&canonical); err != nil {
		return "", err
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func verifyPublicContentRelease(release models.PublicContentRelease) error {
	computed, err := hashPublicContentPayload(release.Payload)
	if err != nil || len(release.ContentHash) != 64 || !hmac.Equal([]byte(computed), []byte(release.ContentHash)) {
		return errUnavailable("committed content integrity unavailable")
	}
	for _, key := range []string{"home", "about", "terms", "privacy"} {
		if _, ok := release.Payload[key].(string); !ok {
			return errUnavailable("committed content integrity unavailable")
		}
	}
	if reviewed, ok := release.Payload["legal_reviewed"].(bool); !ok || !reviewed {
		return errUnavailable("committed content integrity unavailable")
	}
	guid, ok := release.Payload["price_snapshot_guid"].(string)
	if !ok {
		return errUnavailable("committed content integrity unavailable")
	}
	parsed, parseErr := strconv.ParseInt(guid, 10, 64)
	if parseErr != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != guid {
		return errUnavailable("committed content integrity unavailable")
	}
	version, ok := jsonNumberInt64(release.Payload["price_snapshot_version"])
	if !ok || version <= 0 {
		return errUnavailable("committed content integrity unavailable")
	}
	if version, hasVersion := jsonNumberInt64(release.Payload["schema_version"]); hasVersion {
		if version != 2 {
			return errUnavailable("committed content integrity unavailable")
		}
		if _, decodeErr := decodePublishedContentPayloadV2(release.Payload); decodeErr != nil {
			return errUnavailable("committed content integrity unavailable")
		}
		return nil
	}
	keys, keyErr := decodeLegacyContentModelKeys(release.Payload["model_keys"])
	if keyErr != nil {
		return errUnavailable("committed content integrity unavailable")
	}
	for i, key := range keys {
		if !publiccontent.ValidModelKey(key) || (i > 0 && keys[i-1] >= key) {
			return errUnavailable("committed content integrity unavailable")
		}
	}
	return nil
}

func decodePublishedContentPayloadV2(payload models.JSONMap) (*publishedContentPayloadV2, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded publishedContentPayloadV2
	if err = decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing payload")
	}
	if decoded.SchemaVersion != 2 || !decoded.LegalReviewed || !validCanonicalPositiveGUID(decoded.PriceSnapshotGUID) || decoded.PriceSnapshotVersion <= 0 {
		return nil, fmt.Errorf("invalid v2 payload")
	}
	normalized, err := normalizePublishedHomeConfig(decoded.HomeConfig)
	if err != nil {
		return nil, err
	}
	canonical := models.JSONMap{
		"schema_version":         int64(2),
		"home_config":            publishedHomeConfigPayload(normalized),
		"home":                   decoded.Home,
		"about":                  decoded.About,
		"terms":                  decoded.Terms,
		"privacy":                decoded.Privacy,
		"legal_reviewed":         true,
		"price_snapshot_guid":    decoded.PriceSnapshotGUID,
		"price_snapshot_version": decoded.PriceSnapshotVersion,
	}
	canonicalJSON, marshalErr := json.Marshal(canonical)
	if marshalErr != nil || !bytes.Equal(encoded, canonicalJSON) {
		return nil, fmt.Errorf("noncanonical published payload")
	}
	for _, document := range []string{decoded.Home, decoded.About, decoded.Terms, decoded.Privacy} {
		if validateErr := validateCanonicalPublishedHTML(document, true); validateErr != nil {
			return nil, fmt.Errorf("unsafe published document")
		}
	}
	for _, announcement := range decoded.HomeConfig.Announcements {
		if validateErr := validateCanonicalPublishedHTML(announcement.BodyHTML, false); validateErr != nil {
			return nil, fmt.Errorf("unsafe published announcement")
		}
	}
	for _, faq := range decoded.HomeConfig.FAQs {
		if validateErr := validateCanonicalPublishedHTML(faq.AnswerHTML, false); validateErr != nil {
			return nil, fmt.Errorf("unsafe published FAQ")
		}
	}
	return &decoded, nil
}

func validCanonicalPositiveGUID(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}

func (s *PublicContentService) Publish(ctx context.Context, in PublicContentPublicationRequest, values ...PublicAdminTransactionOption) (*PublicContentRelease, error) {
	return s.transact(ctx, in.ActorID, in.ExpectedRevision, in.PriceReleaseGUID, in.IdempotencyKey, "publish", 0, values)
}
func (s *PublicContentService) Restore(ctx context.Context, in PublicContentRestoreRequest, values ...PublicAdminTransactionOption) (*PublicContentRelease, error) {
	g, e := strconv.ParseInt(in.ReleaseGUID, 10, 64)
	if e != nil || g <= 0 {
		return nil, errBadRequest("invalid content release guid")
	}
	return s.transact(ctx, in.ActorID, in.ExpectedRevision, "", in.IdempotencyKey, "restore", g, values)
}
func (s *PublicContentService) transact(ctx context.Context, actorID, expected int64, priceGUID, key, op string, restoreGUID int64, values []PublicAdminTransactionOption) (*PublicContentRelease, error) {
	if actorID <= 0 || expected < 1 || key == "" || len(key) > 256 || strings.TrimSpace(key) != key {
		return nil, errBadRequest("invalid public content publication request")
	}
	options, optionErr := resolvePublicAdminTransactionOptions(values)
	if optionErr != nil {
		return nil, errUnavailable("public content action verification unavailable")
	}
	var out *PublicContentRelease
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, e := lockPublicModelRoot(tx, actorID)
		if e != nil {
			return e
		}
		if e = options.consumeTicket(ctx, tx); e != nil {
			return e
		}
		requestPayload := publicContentRequestPayload(op, expected, priceGUID, restoreGUID)
		binding := publicContentIdempotencyBinding(actor.ID, op, key, requestPayload)
		keyHash := publicContentKeyDigest(key)
		if e = s.fail("replay_lookup"); e != nil {
			return errUnavailable("content replay unavailable")
		}
		if replay, found, replayErr := findPublicContentReplay(tx, actor.ID, op, keyHash, binding); found || replayErr != nil {
			out = replay
			return replayErr
		}
		draft, e := lockOrCreatePublicContentDraft(tx, actor.ID, s.now, s.nextGUID)
		if e != nil {
			return e
		}
		if draft.Revision != expected {
			return errConflict("public content draft revision conflict")
		}
		var home PublicHomeDraft
		if op == "publish" {
			home, e = loadPublicHomeDraft(tx, draft)
			if e != nil {
				return e
			}
		}
		var restoredFrom *int64
		var restoreSource *models.PublicContentRelease
		if op == "restore" {
			var source models.PublicContentRelease
			if e = tx.Where("guid=? AND document_kind=? AND is_deleted=0", restoreGUID, models.PublicContentDocumentSite).First(&source).Error; e == gorm.ErrRecordNotFound {
				return errNotFound("content release not found")
			}
			if e != nil {
				return errUnavailable("content release persistence unavailable")
			}
			sourceHash, hashErr := hashPublicContentPayload(source.Payload)
			if hashErr != nil {
				return errUnprocessable("historical content release invalid")
			}
			if source.ContentHash != sourceHash {
				return errUnprocessable("historical content release integrity validation failed")
			}
			if verifyErr := verifyPublicContentRelease(source); verifyErr != nil {
				return errUnprocessable("historical content release invalid")
			}
			restoreSource = &source
			restoredFrom = &source.ID
		}
		var state models.PublicPublicationState
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; e != nil {
			return errUnavailable("publication state unavailable")
		}
		if state.PriceSnapshotID == nil {
			return errUnprocessable("published price snapshot required")
		}
		var price models.PublicPriceSnapshot
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND is_deleted=0", *state.PriceSnapshotID).First(&price).Error; e != nil {
			return errUnavailable("price snapshot unavailable")
		}
		if op == "publish" && strconv.FormatInt(price.Guid, 10) != priceGUID {
			return errConflict("selected price snapshot is not current")
		}
		items, e := loadEligiblePublicPriceItems(tx, price.ID)
		if e != nil {
			return e
		}
		var prepared *preparedPublicContent
		if restoreSource == nil {
			var issues []publiccontent.ValidationIssue
			prepared, issues = preparePublicContent(projectContentDraft(*draft), home, price, items)
			if len(issues) > 0 {
				return errUnprocessable("public content publication validation failed")
			}
		} else {
			prepared, e = prepareRestoredContentReleaseTx(tx, draft, *restoreSource, price, items)
			if e != nil {
				return e
			}
		}
		if restoreSource != nil {
			now := s.now()
			payload := clonePublicContentPayload(draft.Payload)
			payload["home"] = prepared.Documents["home"]
			payload["about"] = prepared.Documents["about"]
			payload["terms"] = prepared.Documents["terms"]
			payload["privacy"] = prepared.Documents["privacy"]
			payload["legal_reviewed"] = true
			r := tx.Model(draft).Where("id=? AND revision=? AND is_deleted=0", draft.ID, expected).Updates(map[string]any{"payload": payload, "review_state": models.PublicContentReviewApproved, "revision": expected + 1, "updated_at": now, "updated_by": actor.ID})
			if r.Error != nil || r.RowsAffected != 1 {
				return errConflict("public content draft revision conflict")
			}
			draft.Revision = expected + 1
		}
		now, guid := s.now(), s.nextGUID()
		if now <= 0 || guid <= 0 {
			return errUnavailable("content persistence unavailable")
		}
		version := int64(1)
		if state.ContentReleaseID != nil {
			var cur models.PublicContentRelease
			if e = tx.Select("version").First(&cur, *state.ContentReleaseID).Error; e != nil {
				return errUnavailable("content release unavailable")
			}
			version = cur.Version + 1
		}
		rel := models.PublicContentRelease{Guid: guid, CreatedAt: now, CreatedBy: &actor.ID, UpdatedAt: now, UpdatedBy: &actor.ID, DocumentKind: models.PublicContentDocumentSite, Version: version, SourceRevision: draft.Revision, Payload: prepared.Payload, ContentHash: prepared.Hash, RestoredFromReleaseID: restoredFrom, PublishedAt: now}
		if e = s.fail("release"); e != nil {
			return errUnavailable("content release persistence unavailable")
		}
		if e = tx.Create(&rel).Error; e != nil {
			return errUnavailable("content release persistence unavailable")
		}
		job := models.PublicRenderJob{AuditFields: models.AuditFields{Guid: s.nextGUID(), CreatedAt: now, CreatedBy: &actor.ID, UpdatedAt: now, UpdatedBy: &actor.ID}, PriceSnapshotID: price.ID, ContentReleaseID: rel.ID, State: models.PublicRenderJobQueued}
		if e = s.fail("render_job"); e != nil {
			return errUnavailable("render job persistence unavailable")
		}
		if e = tx.Create(&job).Error; e != nil {
			return errUnavailable("render job persistence unavailable")
		}
		detail := models.JSONMap{"idempotency_key_hash": keyHash, "idempotency_binding": binding, "payload_hash": requestPayload, "content_hash": prepared.Hash, "content_release_id": rel.ID, "version": rel.Version, "operation": op, "price_snapshot_guid": strconv.FormatInt(price.Guid, 10)}
		if e = s.fail("audit"); e != nil {
			return errUnavailable("audit persistence unavailable")
		}
		if e = writePublicModelAudit(tx, s.nextGUID(), now, actor.ID, "public_content."+op, "content", rel.Guid, detail); e != nil {
			return errUnavailable("audit persistence unavailable")
		}
		if e = s.fail("pointer"); e != nil {
			return errUnavailable("publication state unavailable")
		}
		r := tx.Model(&models.PublicPublicationState{}).Where("id=? AND revision=?", state.ID, state.Revision).Updates(map[string]any{"content_release_id": rel.ID, "revision": state.Revision + 1, "updated_at": now, "updated_by": actor.ID})
		if r.Error != nil || r.RowsAffected != 1 {
			return errConflict("publication state revision conflict")
		}
		if e = s.fail("after_pointer"); e != nil {
			return errUnavailable("publication state unavailable")
		}
		out = projectContentRelease(rel, op)
		return nil
	})
	if err != nil {
		return nil, mapPublicContentError(err)
	}
	return out, nil
}

func prepareRestoredContentReleaseTx(tx *gorm.DB, draft *models.PublicContentDraft, source models.PublicContentRelease, price models.PublicPriceSnapshot, items []models.PublicPriceSnapshotItem) (*preparedPublicContent, error) {
	version, hasVersion := jsonNumberInt64(source.Payload["schema_version"])
	if !hasVersion {
		prepared, err := prepareContentReleaseRebinding(source, price, items)
		if err != nil {
			return nil, errUnprocessable("historical content release validation failed")
		}
		return prepared, nil
	}
	if version != 2 {
		return nil, errUnprocessable("historical content release invalid")
	}
	decoded, err := decodePublishedContentPayloadV2(source.Payload)
	if err != nil {
		return nil, errUnprocessable("historical content release invalid")
	}
	config := decoded.HomeConfig
	var announcementRows []models.PublicHomeAnnouncement
	if err = tx.Where("content_draft_id=?", draft.ID).Find(&announcementRows).Error; err != nil {
		return nil, errUnavailable("public home announcement persistence unavailable")
	}
	announcementDeleted := make(map[string]bool, len(announcementRows))
	for _, row := range announcementRows {
		announcementDeleted[strconv.FormatInt(row.Guid, 10)] = row.IsDeleted != 0
	}
	announcements := make([]publishedAnnouncement, 0, len(config.Announcements))
	for _, announcement := range config.Announcements {
		if !announcementDeleted[announcement.GUID] {
			announcements = append(announcements, announcement)
		}
	}
	config.Announcements = announcements
	var faqRows []models.PublicHomeFAQ
	if err = tx.Where("content_draft_id=?", draft.ID).Find(&faqRows).Error; err != nil {
		return nil, errUnavailable("public home FAQ persistence unavailable")
	}
	faqDeleted := make(map[string]bool, len(faqRows))
	for _, row := range faqRows {
		faqDeleted[strconv.FormatInt(row.Guid, 10)] = row.IsDeleted != 0
	}
	faqs := make([]publishedFAQ, 0, len(config.FAQs))
	for _, faq := range config.FAQs {
		if !faqDeleted[faq.GUID] {
			faqs = append(faqs, faq)
		}
	}
	config.FAQs = faqs
	var modelRows []models.PublicModelConfig
	if len(config.FeaturedModelKeys) > 0 {
		if err = tx.Where("model_key IN ?", config.FeaturedModelKeys).Find(&modelRows).Error; err != nil {
			return nil, errUnavailable("public model persistence unavailable")
		}
	}
	modelCurrent := make(map[string]models.PublicModelConfig, len(modelRows))
	for _, row := range modelRows {
		modelCurrent[row.ModelKey] = row
	}
	featured := make([]string, 0, len(config.FeaturedModelKeys))
	for _, key := range config.FeaturedModelKeys {
		row, exists := modelCurrent[key]
		if exists && (row.IsDeleted != 0 || row.Status != models.PublicModelConfigStatusActive) {
			continue
		}
		featured = append(featured, key)
	}
	config.FeaturedModelKeys = featured
	config, err = normalizePublishedHomeConfig(config)
	if err != nil || len(validatePublishedFeaturedModels(config.FeaturedModelKeys, items, true)) != 0 {
		return nil, errUnprocessable("historical content release validation failed")
	}
	payload := models.JSONMap{
		"schema_version":         int64(2),
		"home_config":            publishedHomeConfigPayload(config),
		"home":                   decoded.Home,
		"about":                  decoded.About,
		"terms":                  decoded.Terms,
		"privacy":                decoded.Privacy,
		"legal_reviewed":         true,
		"price_snapshot_guid":    strconv.FormatInt(price.Guid, 10),
		"price_snapshot_version": price.Version,
	}
	hash, err := hashPublicContentPayload(payload)
	if err != nil {
		return nil, errUnprocessable("historical content release invalid")
	}
	return &preparedPublicContent{Payload: payload, Hash: hash, Documents: map[string]string{"home": decoded.Home, "about": decoded.About, "terms": decoded.Terms, "privacy": decoded.Privacy}, PriceSnapshotID: price.ID, PriceSnapshotGUID: price.Guid, PriceSnapshotVersion: price.Version}, nil
}
func (s *PublicContentService) PublicProjection(ctx context.Context) (*PublicContentPublicProjection, error) {
	var out *PublicContentPublicProjection
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state models.PublicPublicationState
		if e := tx.Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; e != nil || state.ContentReleaseID == nil || state.PriceSnapshotID == nil {
			return errUnavailable("committed publication unavailable")
		}
		var c models.PublicContentRelease
		var p models.PublicPriceSnapshot
		if e := tx.First(&c, *state.ContentReleaseID).Error; e != nil {
			return errUnavailable("committed publication unavailable")
		}
		if e := verifyPublicContentRelease(c); e != nil {
			return e
		}
		if e := tx.First(&p, *state.PriceSnapshotID).Error; e != nil {
			return errUnavailable("committed publication unavailable")
		}
		boundVersion, ok := jsonNumberInt64(c.Payload["price_snapshot_version"])
		if fmt.Sprint(c.Payload["price_snapshot_guid"]) != strconv.FormatInt(p.Guid, 10) || !ok || boundVersion != p.Version {
			return errUnavailable("committed publication binding unavailable")
		}
		var allCount int64
		if e := tx.Model(&models.PublicPriceSnapshotItem{}).Where("snapshot_id=? AND is_deleted=0", p.ID).Count(&allCount).Error; e != nil {
			return errUnavailable("committed publication generation pending")
		}
		var activeItems []models.PublicPriceSnapshotItem
		if e := tx.Model(&models.PublicPriceSnapshotItem{}).
			Joins("JOIN public_model_configs m ON m.id=public_price_snapshot_items.model_config_id AND m.status=? AND m.is_deleted=0", models.PublicModelConfigStatusActive).
			Where("public_price_snapshot_items.snapshot_id=? AND public_price_snapshot_items.is_deleted=0", p.ID).
			Order("public_price_snapshot_items.model_key").Find(&activeItems).Error; e != nil || int64(len(activeItems)) != allCount {
			return errUnavailable("committed publication generation pending")
		}
		if e := validateContentReleaseForPriceItems(c, activeItems); e != nil {
			return errUnavailable("committed publication generation pending")
		}
		out = &PublicContentPublicProjection{Content: projectContentPayload(c.Payload, c.SourceRevision), ContentReleaseVersion: c.Version, PriceReleaseVersion: p.Version, ETag: `"` + c.ContentHash + `"`}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func lockOrCreatePublicContentDraft(tx *gorm.DB, actor int64, nowFn func() int64, guidFn func() int64) (*models.PublicContentDraft, error) {
	var d models.PublicContentDraft
	e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("document_kind=? AND is_deleted=0", models.PublicContentDocumentSite).First(&d).Error
	if e == nil {
		return &d, nil
	}
	if e != gorm.ErrRecordNotFound {
		return nil, errUnavailable("public content draft unavailable")
	}
	now, guid := nowFn(), guidFn()
	if now <= 0 || guid <= 0 {
		return nil, errUnavailable("public content draft unavailable")
	}
	d = models.PublicContentDraft{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, DocumentKind: models.PublicContentDocumentSite, Payload: models.JSONMap{"home": "", "about": "", "terms": "", "privacy": "", "legal_reviewed": false, "featured_model_keys": []string{}}, Revision: 1, ReviewState: models.PublicContentReviewPending}
	if e = tx.Create(&d).Error; e != nil {
		return nil, errUnavailable("public content draft unavailable")
	}
	return &d, nil
}
func projectContentDraft(d models.PublicContentDraft) PublicContentDraft {
	return projectContentPayload(d.Payload, d.Revision)
}
func projectContentPayload(p models.JSONMap, revision int64) PublicContentDraft {
	return PublicContentDraft{Revision: revision, Home: jsonString(p["home"]), About: jsonString(p["about"]), Terms: jsonString(p["terms"]), Privacy: jsonString(p["privacy"]), LegalReviewed: jsonBool(p["legal_reviewed"])}
}
func jsonString(v any) string { s, _ := v.(string); return s }
func jsonBool(v any) bool     { b, _ := v.(bool); return b }

func mergePublicContentDraftPayload(existing models.JSONMap, in PublicContentDraftSaveRequest) models.JSONMap {
	payload := clonePublicContentPayload(existing)
	payload["home"] = *in.Home
	payload["about"] = *in.About
	payload["terms"] = *in.Terms
	payload["privacy"] = *in.Privacy
	payload["legal_reviewed"] = *in.LegalReviewed
	return payload
}

func clonePublicContentPayload(input models.JSONMap) models.JSONMap {
	output := make(models.JSONMap, len(input))
	for key, value := range input {
		output[key] = clonePublicContentJSONValue(value)
	}
	return output
}

func clonePublicContentJSONValue(value any) any {
	switch typed := value.(type) {
	case models.JSONMap:
		return clonePublicContentPayload(typed)
	case map[string]any:
		copy := make(map[string]any, len(typed))
		for key, item := range typed {
			copy[key] = clonePublicContentJSONValue(item)
		}
		return copy
	case models.JSONSlice:
		return append(make(models.JSONSlice, 0, len(typed)), typed...)
	case []string:
		return append(make([]string, 0, len(typed)), typed...)
	case []any:
		copy := make([]any, len(typed))
		for index, item := range typed {
			copy[index] = clonePublicContentJSONValue(item)
		}
		return copy
	case []models.JSONMap:
		copy := make([]models.JSONMap, len(typed))
		for index, item := range typed {
			copy[index] = clonePublicContentPayload(item)
		}
		return copy
	default:
		return value
	}
}
func publicContentIdempotencyBinding(actor int64, op, key, payload string) string {
	s := sha256.Sum256([]byte(strconv.FormatInt(actor, 10) + "\x00" + op + "\x00" + key + "\x00" + payload))
	return hex.EncodeToString(s[:])
}
func publicContentRequestPayload(op string, expected int64, priceGUID string, restoreGUID int64) string {
	b, _ := json.Marshal(struct {
		Operation          string `json:"operation"`
		ExpectedRevision   int64  `json:"expected_revision"`
		PriceReleaseGUID   string `json:"price_release_guid,omitempty"`
		RestoreReleaseGUID int64  `json:"restore_release_guid,omitempty"`
	}{op, expected, priceGUID, restoreGUID})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func publicContentKeyDigest(key string) string {
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])
}
func findPublicContentReplay(tx *gorm.DB, actor int64, op, keyHash, binding string) (*PublicContentRelease, bool, error) {
	var a models.AuditLog
	e := tx.Where("user_id=? AND action=? AND JSON_UNQUOTE(JSON_EXTRACT(detail, '$.idempotency_key_hash'))=?", actor, "public_content."+op, keyHash).Order("id DESC").First(&a).Error
	if e == gorm.ErrRecordNotFound {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, errUnavailable("idempotency state unavailable")
	}
	if fmt.Sprint(a.Detail["idempotency_binding"]) != binding {
		return nil, true, errConflict("idempotency conflict")
	}
	id, ok := jsonNumberInt64(a.Detail["content_release_id"])
	if !ok {
		return nil, true, errUnavailable("idempotency state unavailable")
	}
	var r models.PublicContentRelease
	if e = tx.First(&r, id).Error; e != nil {
		return nil, true, errUnavailable("idempotency state unavailable")
	}
	return projectContentRelease(r, op), true, nil
}
func projectContentRelease(r models.PublicContentRelease, op string) *PublicContentRelease {
	reason := "root_publish"
	if op == "restore" {
		reason = "restore"
	}
	return &PublicContentRelease{GUID: strconv.FormatInt(r.Guid, 10), Version: r.Version, Reason: reason, SourceRevision: r.SourceRevision, CreatedAt: releaseTime(r.PublishedAt)}
}
func mapPublicContentError(e error) error {
	if _, ok := e.(*HTTPError); ok {
		return e
	}
	return errUnavailable("public content unavailable")
}
