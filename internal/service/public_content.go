package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/publiccontent"
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
	CreatedAt      int64  `json:"created_at"`
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
		payload := models.JSONMap{"home": *in.Home, "about": *in.About, "terms": *in.Terms, "privacy": *in.Privacy, "legal_reviewed": *in.LegalReviewed}
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
	d, e := s.GetDraft(ctx, actorID)
	if e != nil {
		return nil, e
	}
	if revision != d.Revision {
		return nil, errConflict("public content draft revision conflict")
	}
	var state models.PublicPublicationState
	if e = s.db.WithContext(ctx).Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; e != nil || state.PriceSnapshotID == nil {
		return nil, errUnavailable("committed price snapshot unavailable")
	}
	var p models.PublicPriceSnapshot
	if e = s.db.WithContext(ctx).Where("id=? AND is_deleted=0", *state.PriceSnapshotID).First(&p).Error; e != nil {
		return nil, errUnavailable("committed price snapshot unavailable")
	}
	var items []models.PublicPriceSnapshotItem
	if e = s.db.WithContext(ctx).Where("snapshot_id=? AND is_deleted=0", p.ID).Order("model_key").Find(&items).Error; e != nil {
		return nil, errUnavailable("committed price snapshot unavailable")
	}
	_, issues := preparePublicContent(*d, p, items)
	return issues, nil
}

func preparePublicContent(d PublicContentDraft, price models.PublicPriceSnapshot, items []models.PublicPriceSnapshotItem) (*preparedPublicContent, []publiccontent.ValidationIssue) {
	issues := []publiccontent.ValidationIssue{}
	for _, x := range []struct{ name, value string }{{"home", d.Home}, {"about", d.About}, {"terms", d.Terms}, {"privacy", d.Privacy}} {
		if len(x.value) > PublicContentDocumentLimit {
			issues = append(issues, publiccontent.ValidationIssue{Field: x.name, Code: "content_too_large"})
		}
	}
	modelsV := make([]publiccontent.Model, 0, len(items))
	for _, i := range items {
		modelsV = append(modelsV, publiccontent.Model{ModelKey: i.ModelKey, UpstreamModelID: i.UpstreamModelID, Active: true, Price: publiccontent.Price{Currency: publiccontent.CurrencyUSD, Unit: publiccontent.UnitMillionTokens, Input: i.InputPriceUSDPerMillionTokens, Output: i.OutputPriceUSDPerMillionTokens}})
	}
	refs, _ := publiccontent.PublicModelReferences(d.Home)
	pub := publiccontent.Publication{Models: modelsV, HomeModelKeys: refs, Documents: []publiccontent.Document{{Kind: publiccontent.DocumentHome, Body: d.Home}, {Kind: publiccontent.DocumentAbout, Body: d.About}, {Kind: publiccontent.DocumentTerms, Body: d.Terms, Reviewed: d.LegalReviewed}, {Kind: publiccontent.DocumentPrivacy, Body: d.Privacy, Reviewed: d.LegalReviewed}}}
	issues = append(issues, publiccontent.ValidatePublication(pub)...)
	docs, sanitizeIssues := sanitizeContentDocuments(d)
	issues = append(issues, sanitizeIssues...)
	if len(issues) > 0 {
		return nil, issues
	}
	sort.Strings(refs)
	payload := models.JSONMap{"home": docs["home"], "about": docs["about"], "terms": docs["terms"], "privacy": docs["privacy"], "legal_reviewed": true, "model_keys": refs, "price_snapshot_guid": strconv.FormatInt(price.Guid, 10), "price_snapshot_version": price.Version}
	b, e := json.Marshal(payload)
	if e != nil {
		return nil, []publiccontent.ValidationIssue{{Field: "content", Code: "serialization_failed"}}
	}
	sum := sha256.Sum256(b)
	return &preparedPublicContent{Payload: payload, Hash: hex.EncodeToString(sum[:]), Documents: docs, PriceSnapshotID: price.ID, PriceSnapshotGUID: price.Guid, PriceSnapshotVersion: price.Version}, nil
}
func sanitizeContentDocuments(d PublicContentDraft) (map[string]string, []publiccontent.ValidationIssue) {
	out := map[string]string{}
	issues := []publiccontent.ValidationIssue{}
	for _, x := range []struct{ name, value string }{{"home", d.Home}, {"about", d.About}, {"terms", d.Terms}, {"privacy", d.Privacy}} {
		v, is := publiccontent.SanitizeMarkdown(x.value)
		out[x.name] = v
		for _, i := range is {
			issues = append(issues, publiccontent.ValidationIssue{Field: x.name, Code: i.Code})
		}
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

func (s *PublicContentService) Publish(ctx context.Context, in PublicContentPublicationRequest) (*PublicContentRelease, error) {
	return s.transact(ctx, in.ActorID, in.ExpectedRevision, in.PriceReleaseGUID, in.IdempotencyKey, "publish", 0)
}
func (s *PublicContentService) Restore(ctx context.Context, in PublicContentRestoreRequest) (*PublicContentRelease, error) {
	g, e := strconv.ParseInt(in.ReleaseGUID, 10, 64)
	if e != nil || g <= 0 {
		return nil, errBadRequest("invalid content release guid")
	}
	return s.transact(ctx, in.ActorID, in.ExpectedRevision, "", in.IdempotencyKey, "restore", g)
}
func (s *PublicContentService) transact(ctx context.Context, actorID, expected int64, priceGUID, key, op string, restoreGUID int64) (*PublicContentRelease, error) {
	if actorID <= 0 || expected < 1 || key == "" || len(key) > 256 || strings.TrimSpace(key) != key {
		return nil, errBadRequest("invalid public content publication request")
	}
	var out *PublicContentRelease
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, e := lockPublicModelRoot(tx, actorID)
		if e != nil {
			return e
		}
		requestPayload := publicContentRequestPayload(op, expected, priceGUID, restoreGUID)
		binding := publicContentIdempotencyBinding(actor.ID, op, key, requestPayload)
		keyHash := publicContentKeyDigest(key)
		if replay, found, replayErr := findPublicContentReplay(tx, actor.ID, op, keyHash, binding); found || replayErr != nil {
			out = replay
			return replayErr
		}
		draft, e := lockOrCreatePublicContentDraft(tx, actor.ID, s.now, s.nextGUID)
		if e != nil {
			return e
		}
		var restoredFrom *int64
		var restoreDraft *PublicContentDraft
		if op == "restore" {
			var source models.PublicContentRelease
			if e = tx.Where("guid=? AND document_kind=? AND is_deleted=0", restoreGUID, models.PublicContentDocumentSite).First(&source).Error; e == gorm.ErrRecordNotFound {
				return errNotFound("content release not found")
			}
			if e != nil {
				return errUnavailable("content release persistence unavailable")
			}
			encoded, hashErr := json.Marshal(source.Payload)
			if hashErr != nil {
				return errUnprocessable("historical content release invalid")
			}
			sourceSum := sha256.Sum256(encoded)
			if source.ContentHash != hex.EncodeToString(sourceSum[:]) {
				return errUnprocessable("historical content release integrity validation failed")
			}
			old := projectContentPayload(source.Payload, source.SourceRevision)
			if old.Revision < 1 {
				return errUnprocessable("historical content release invalid")
			}
			restoreDraft = &old
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
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&price, *state.PriceSnapshotID).Error; e != nil {
			return errUnavailable("price snapshot unavailable")
		}
		if op == "publish" && strconv.FormatInt(price.Guid, 10) != priceGUID {
			return errConflict("selected price snapshot is not current")
		}
		var items []models.PublicPriceSnapshotItem
		if e = tx.Where("snapshot_id=? AND is_deleted=0", price.ID).Order("model_key").Find(&items).Error; e != nil {
			return errUnavailable("price snapshot unavailable")
		}
		candidate := projectContentDraft(*draft)
		if restoreDraft != nil {
			candidate = *restoreDraft
		}
		prepared, issues := preparePublicContent(candidate, price, items)
		if len(issues) > 0 {
			return errUnprocessable("public content publication validation failed")
		}
		if draft.Revision != expected {
			return errConflict("public content draft revision conflict")
		}
		if restoreDraft != nil {
			now := s.now()
			payload := models.JSONMap{"home": restoreDraft.Home, "about": restoreDraft.About, "terms": restoreDraft.Terms, "privacy": restoreDraft.Privacy, "legal_reviewed": restoreDraft.LegalReviewed}
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
func (s *PublicContentService) PublicProjection(ctx context.Context) (*PublicContentPublicProjection, error) {
	var state models.PublicPublicationState
	if e := s.db.WithContext(ctx).Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; e != nil || state.ContentReleaseID == nil || state.PriceSnapshotID == nil {
		return nil, errUnavailable("committed publication unavailable")
	}
	var c models.PublicContentRelease
	var p models.PublicPriceSnapshot
	if e := s.db.First(&c, *state.ContentReleaseID).Error; e != nil {
		return nil, errUnavailable("committed publication unavailable")
	}
	if e := s.db.First(&p, *state.PriceSnapshotID).Error; e != nil {
		return nil, errUnavailable("committed publication unavailable")
	}
	return &PublicContentPublicProjection{Content: projectContentPayload(c.Payload, c.SourceRevision), ContentReleaseVersion: c.Version, PriceReleaseVersion: p.Version, ETag: `"` + c.ContentHash + `"`}, nil
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
	d = models.PublicContentDraft{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, DocumentKind: models.PublicContentDocumentSite, Payload: models.JSONMap{"home": "", "about": "", "terms": "", "privacy": "", "legal_reviewed": false}, Revision: 1, ReviewState: models.PublicContentReviewPending}
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
	return &PublicContentRelease{GUID: strconv.FormatInt(r.Guid, 10), Version: r.Version, Reason: reason, SourceRevision: r.SourceRevision, CreatedAt: r.PublishedAt}
}
func mapPublicContentError(e error) error {
	if _, ok := e.(*HTTPError); ok {
		return e
	}
	return errUnavailable("public content unavailable")
}
