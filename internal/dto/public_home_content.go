package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/publiccontent"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const (
	PublicHomeAnnouncementLimit  = service.PublicHomeAnnouncementLimit
	PublicHomeFAQLimit           = service.PublicHomeFAQLimit
	PublicHomeFeaturedModelLimit = service.PublicHomeFeaturedModelLimit
	PublicHomeMarkdownLimit      = service.PublicHomeMarkdownLimit
	PublicHomeSortOrderMaximum   = service.PublicHomeSortOrderMaximum
)

var ErrPublicHomeInvalidRequest = errors.New("invalid public home content request")

type AnnouncementDraft = service.PublicHomeAnnouncementDraft
type FAQDraft = service.PublicHomeFAQDraft
type HomeDraftResponse = service.PublicHomeDraft
type DocumentsDraftResponse = service.PublicHomeDocumentsDraft
type HomeConfigPublicAnnouncement = service.PublicHomeConfigAnnouncement
type HomeConfigPublicFAQ = service.PublicHomeConfigFAQ
type HomeConfigPublicResponse = service.PublicHomeConfig

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

type FeaturedModelReplacementRequest = FeaturedModelsSaveRequest
type FeaturedModelsReplaceRequest = FeaturedModelsSaveRequest

type DocumentsDraftSaveRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	About            string `json:"about"`
	Terms            string `json:"terms"`
	Privacy          string `json:"privacy"`
	LegalReviewed    bool   `json:"legal_reviewed"`
}

type announcementCreateWire struct {
	ExpectedRevision int64           `json:"expected_revision"`
	Title            string          `json:"title"`
	BodyMarkdown     string          `json:"body_markdown"`
	EffectiveAt      json.RawMessage `json:"effective_at"`
	IsVisible        bool            `json:"is_visible"`
	SortOrder        int             `json:"sort_order"`
}

type announcementUpdateWire struct {
	ExpectedRevision int64            `json:"expected_revision"`
	Title            *string          `json:"title"`
	BodyMarkdown     *string          `json:"body_markdown"`
	EffectiveAt      *json.RawMessage `json:"effective_at"`
	IsVisible        *bool            `json:"is_visible"`
	SortOrder        *int             `json:"sort_order"`
}

type faqCreateWire struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Question         string `json:"question"`
	AnswerMarkdown   string `json:"answer_markdown"`
	IsVisible        bool   `json:"is_visible"`
	SortOrder        int    `json:"sort_order"`
}

type faqUpdateWire struct {
	ExpectedRevision int64   `json:"expected_revision"`
	Question         *string `json:"question"`
	AnswerMarkdown   *string `json:"answer_markdown"`
	IsVisible        *bool   `json:"is_visible"`
	SortOrder        *int    `json:"sort_order"`
}

type featuredModelsSaveWire struct {
	ExpectedRevision  int64    `json:"expected_revision"`
	FeaturedModelKeys []string `json:"featured_model_keys"`
}

func DecodeAnnouncementCreateRequest(r io.Reader) (AnnouncementCreateRequest, error) {
	var wire announcementCreateWire
	seen, err := decodePublicHomeObject(r, &wire, "expected_revision", "title", "body_markdown", "effective_at", "is_visible", "sort_order")
	if err != nil {
		return AnnouncementCreateRequest{}, err
	}
	if !hasEveryPublicHomeField(seen, "expected_revision", "title", "body_markdown", "effective_at", "is_visible", "sort_order") || isJSONNull(seen["title"]) || isJSONNull(seen["body_markdown"]) || isJSONNull(seen["is_visible"]) || isJSONNull(seen["sort_order"]) {
		return AnnouncementCreateRequest{}, ErrPublicHomeInvalidRequest
	}
	effectiveAt, err := decodeCanonicalPublicHomeTime(wire.EffectiveAt)
	if err != nil || !validPublicHomeRevision(wire.ExpectedRevision) || !validPublicHomePlainText(wire.Title, 120) || len(wire.BodyMarkdown) > PublicHomeMarkdownLimit || !validPublicHomeSortOrder(wire.SortOrder) {
		return AnnouncementCreateRequest{}, ErrPublicHomeInvalidRequest
	}
	return AnnouncementCreateRequest{ExpectedRevision: wire.ExpectedRevision, Title: wire.Title, BodyMarkdown: wire.BodyMarkdown, EffectiveAt: effectiveAt, IsVisible: wire.IsVisible, SortOrder: wire.SortOrder}, nil
}

func DecodeAnnouncementUpdateRequest(r io.Reader) (AnnouncementUpdateRequest, error) {
	var wire announcementUpdateWire
	seen, err := decodePublicHomeObject(r, &wire, "expected_revision", "title", "body_markdown", "effective_at", "is_visible", "sort_order")
	if err != nil {
		return AnnouncementUpdateRequest{}, err
	}
	if _, ok := seen["expected_revision"]; !ok || !validPublicHomeRevision(wire.ExpectedRevision) || len(seen) == 1 {
		return AnnouncementUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	request := AnnouncementUpdateRequest{ExpectedRevision: wire.ExpectedRevision, Title: wire.Title, BodyMarkdown: wire.BodyMarkdown, IsVisible: wire.IsVisible, SortOrder: wire.SortOrder}
	if _, ok := seen["title"]; ok && (wire.Title == nil || !validPublicHomePlainText(*wire.Title, 120)) {
		return AnnouncementUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	if _, ok := seen["body_markdown"]; ok && (wire.BodyMarkdown == nil || len(*wire.BodyMarkdown) > PublicHomeMarkdownLimit) {
		return AnnouncementUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	if raw, ok := seen["effective_at"]; ok {
		request.EffectiveAt.Set = true
		request.EffectiveAt.Value, err = decodeCanonicalPublicHomeTime(raw)
		if err != nil {
			return AnnouncementUpdateRequest{}, ErrPublicHomeInvalidRequest
		}
	}
	if _, ok := seen["is_visible"]; ok && wire.IsVisible == nil {
		return AnnouncementUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	if _, ok := seen["sort_order"]; ok && (wire.SortOrder == nil || !validPublicHomeSortOrder(*wire.SortOrder)) {
		return AnnouncementUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	return request, nil
}

func DecodeAnnouncementDeleteRequest(r io.Reader) (AnnouncementDeleteRequest, error) {
	revision, err := decodePublicHomeRevisionRequest(r)
	return AnnouncementDeleteRequest{ExpectedRevision: revision}, err
}

func DecodeFAQCreateRequest(r io.Reader) (FAQCreateRequest, error) {
	var wire faqCreateWire
	seen, err := decodePublicHomeObject(r, &wire, "expected_revision", "question", "answer_markdown", "is_visible", "sort_order")
	if err != nil {
		return FAQCreateRequest{}, err
	}
	if !hasEveryPublicHomeField(seen, "expected_revision", "question", "answer_markdown", "is_visible", "sort_order") || isJSONNull(seen["question"]) || isJSONNull(seen["answer_markdown"]) || isJSONNull(seen["is_visible"]) || isJSONNull(seen["sort_order"]) || !validPublicHomeRevision(wire.ExpectedRevision) || !validPublicHomePlainText(wire.Question, 200) || len(wire.AnswerMarkdown) > PublicHomeMarkdownLimit || !validPublicHomeSortOrder(wire.SortOrder) {
		return FAQCreateRequest{}, ErrPublicHomeInvalidRequest
	}
	return FAQCreateRequest{ExpectedRevision: wire.ExpectedRevision, Question: wire.Question, AnswerMarkdown: wire.AnswerMarkdown, IsVisible: wire.IsVisible, SortOrder: wire.SortOrder}, nil
}

func DecodeFAQUpdateRequest(r io.Reader) (FAQUpdateRequest, error) {
	var wire faqUpdateWire
	seen, err := decodePublicHomeObject(r, &wire, "expected_revision", "question", "answer_markdown", "is_visible", "sort_order")
	if err != nil {
		return FAQUpdateRequest{}, err
	}
	if _, ok := seen["expected_revision"]; !ok || !validPublicHomeRevision(wire.ExpectedRevision) || len(seen) == 1 {
		return FAQUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	if _, ok := seen["question"]; ok && (wire.Question == nil || !validPublicHomePlainText(*wire.Question, 200)) {
		return FAQUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	if _, ok := seen["answer_markdown"]; ok && (wire.AnswerMarkdown == nil || len(*wire.AnswerMarkdown) > PublicHomeMarkdownLimit) {
		return FAQUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	if _, ok := seen["is_visible"]; ok && wire.IsVisible == nil {
		return FAQUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	if _, ok := seen["sort_order"]; ok && (wire.SortOrder == nil || !validPublicHomeSortOrder(*wire.SortOrder)) {
		return FAQUpdateRequest{}, ErrPublicHomeInvalidRequest
	}
	return FAQUpdateRequest{ExpectedRevision: wire.ExpectedRevision, Question: wire.Question, AnswerMarkdown: wire.AnswerMarkdown, IsVisible: wire.IsVisible, SortOrder: wire.SortOrder}, nil
}

func DecodeFAQDeleteRequest(r io.Reader) (FAQDeleteRequest, error) {
	revision, err := decodePublicHomeRevisionRequest(r)
	return FAQDeleteRequest{ExpectedRevision: revision}, err
}

func DecodeFeaturedModelsSaveRequest(r io.Reader) (FeaturedModelsSaveRequest, error) {
	var wire featuredModelsSaveWire
	seen, err := decodePublicHomeObject(r, &wire, "expected_revision", "featured_model_keys")
	if err != nil {
		return FeaturedModelsSaveRequest{}, err
	}
	if !hasEveryPublicHomeField(seen, "expected_revision", "featured_model_keys") || isJSONNull(seen["featured_model_keys"]) || !validPublicHomeRevision(wire.ExpectedRevision) || len(wire.FeaturedModelKeys) > PublicHomeFeaturedModelLimit {
		return FeaturedModelsSaveRequest{}, ErrPublicHomeInvalidRequest
	}
	keys := append(make([]string, 0, len(wire.FeaturedModelKeys)), wire.FeaturedModelKeys...)
	seenKeys := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if !publiccontent.ValidModelKey(key) {
			return FeaturedModelsSaveRequest{}, ErrPublicHomeInvalidRequest
		}
		if _, duplicate := seenKeys[key]; duplicate {
			return FeaturedModelsSaveRequest{}, ErrPublicHomeInvalidRequest
		}
		seenKeys[key] = struct{}{}
	}
	return FeaturedModelsSaveRequest{ExpectedRevision: wire.ExpectedRevision, FeaturedModelKeys: keys}, nil
}

func DecodeFeaturedModelReplacementRequest(r io.Reader) (FeaturedModelReplacementRequest, error) {
	return DecodeFeaturedModelsSaveRequest(r)
}

func DecodeFeaturedModelsReplaceRequest(r io.Reader) (FeaturedModelsReplaceRequest, error) {
	return DecodeFeaturedModelsSaveRequest(r)
}

func DecodeDocumentsDraftSaveRequest(r io.Reader) (DocumentsDraftSaveRequest, error) {
	var request DocumentsDraftSaveRequest
	seen, err := decodePublicHomeObject(r, &request, "expected_revision", "about", "terms", "privacy", "legal_reviewed")
	if err != nil {
		return DocumentsDraftSaveRequest{}, err
	}
	if !hasEveryPublicHomeField(seen, "expected_revision", "about", "terms", "privacy", "legal_reviewed") || !validPublicHomeRevision(request.ExpectedRevision) {
		return DocumentsDraftSaveRequest{}, ErrPublicHomeInvalidRequest
	}
	for _, key := range []string{"about", "terms", "privacy", "legal_reviewed"} {
		if isJSONNull(seen[key]) {
			return DocumentsDraftSaveRequest{}, ErrPublicHomeInvalidRequest
		}
	}
	return request, nil
}

func ParsePublicHomePreviewRevision(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	if raw[0] < '1' || raw[0] > '9' {
		return 0, ErrPublicHomeInvalidRequest
	}
	for _, value := range raw[1:] {
		if value < '0' || value > '9' {
			return 0, ErrPublicHomeInvalidRequest
		}
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != raw {
		return 0, ErrPublicHomeInvalidRequest
	}
	return revision, nil
}

func ValidatePublicHomePreviewRevision(raw string) (int64, error) {
	return ParsePublicHomePreviewRevision(raw)
}

func decodePublicHomeRevisionRequest(r io.Reader) (int64, error) {
	var wire struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	seen, err := decodePublicHomeObject(r, &wire, "expected_revision")
	if err != nil {
		return 0, err
	}
	if !hasEveryPublicHomeField(seen, "expected_revision") || !validPublicHomeRevision(wire.ExpectedRevision) {
		return 0, ErrPublicHomeInvalidRequest
	}
	return wire.ExpectedRevision, nil
}

func decodePublicHomeObject(r io.Reader, output any, allowedFields ...string) (map[string]json.RawMessage, error) {
	if r == nil {
		return nil, ErrPublicHomeInvalidRequest
	}
	raw, err := io.ReadAll(io.LimitReader(r, PublicContentRequestBodyLimit+1))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return nil, ErrPublicContentRequestTooLarge
		}
		return nil, ErrPublicHomeInvalidRequest
	}
	defer clear(raw)
	if len(raw) > PublicContentRequestBodyLimit {
		return nil, ErrPublicContentRequestTooLarge
	}
	if len(raw) == 0 || !utf8.Valid(raw) || !validAdminUserEditStringTokens(raw) || ValidateNoDuplicateJSON(raw, 64) != nil {
		return nil, ErrPublicHomeInvalidRequest
	}
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, ErrPublicHomeInvalidRequest
	}
	seen := make(map[string]json.RawMessage, len(allowedFields))
	for decoder.More() {
		token, tokenErr := decoder.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok {
			return nil, ErrPublicHomeInvalidRequest
		}
		if _, accepted := allowed[key]; !accepted {
			return nil, ErrPublicHomeInvalidRequest
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrPublicHomeInvalidRequest
		}
		seen[key] = value
	}
	if last, tokenErr := decoder.Token(); tokenErr != nil || last != json.Delim('}') {
		return nil, ErrPublicHomeInvalidRequest
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, ErrPublicHomeInvalidRequest
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err = strict.Decode(output); err != nil {
		return nil, ErrPublicHomeInvalidRequest
	}
	return seen, nil
}

func decodeCanonicalPublicHomeTime(raw json.RawMessage) (*int64, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil, ErrPublicHomeInvalidRequest
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Nanosecond() != 0 || parsed.Location() != time.UTC || parsed.UTC().Format(time.RFC3339) != value {
		return nil, ErrPublicHomeInvalidRequest
	}
	millis := parsed.UnixMilli()
	return &millis, nil
}

func validPublicHomePlainText(value string, maximum int) bool {
	count := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && count >= 1 && count <= maximum && strings.TrimSpace(value) != "" && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validPublicHomeRevision(value int64) bool { return value >= 1 }

func validPublicHomeSortOrder(value int) bool {
	return value >= 0 && value <= PublicHomeSortOrderMaximum
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func hasEveryPublicHomeField(seen map[string]json.RawMessage, fields ...string) bool {
	for _, field := range fields {
		if _, ok := seen[field]; !ok {
			return false
		}
	}
	return true
}

func (o *OptionalNullableUnixMillis) UnmarshalJSON(raw []byte) error {
	o.Set = true
	value, err := decodeCanonicalPublicHomeTime(raw)
	if err != nil {
		return err
	}
	o.Value = value
	return nil
}

func (o OptionalNullableUnixMillis) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(time.UnixMilli(*o.Value).UTC().Format(time.RFC3339))
}
