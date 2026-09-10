package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

type MissingModelsResponse struct {
	ObservedWithoutConfiguration []string           `json:"observed_without_configuration"`
	ConfiguredMissingUpstream    []PublicModelAdmin `json:"configured_missing_upstream"`
}

type PublicModelSyncTrigger interface{ Tick(context.Context) error }

func (s *PublicModelAdminService) Missing(ctx context.Context) (*MissingModelsResponse, error) {
	if s == nil || s.db == nil {
		return nil, errUnavailable("public model persistence unavailable")
	}
	out := &MissingModelsResponse{ObservedWithoutConfiguration: []string{}, ConfiguredMissingUpstream: []PublicModelAdmin{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var configured []models.PublicModelConfig
		if e := tx.Where("is_deleted=0 AND last_upstream_check_at IS NOT NULL AND (last_upstream_observed_at IS NULL OR last_upstream_observed_at < last_upstream_check_at)").Order("model_key").Find(&configured).Error; e != nil {
			return errUnavailable("public model persistence unavailable")
		}
		for _, row := range configured {
			out.ConfiguredMissingUpstream = append(out.ConfiguredMissingUpstream, projectPublicModel(row))
		}
		if e := tx.Raw(`SELECT DISTINCT o.upstream_model_id FROM upstream_model_observations o LEFT JOIN public_model_configs m ON m.upstream_model_id=o.upstream_model_id WHERE o.is_deleted=0 AND o.catalog_complete=1 AND o.catalog_fresh=1 AND m.id IS NULL ORDER BY o.upstream_model_id`).Scan(&out.ObservedWithoutConfiguration).Error; e != nil {
			return errUnavailable("upstream observation persistence unavailable")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PublicModelAdminService) Sync(ctx context.Context, trigger PublicModelSyncTrigger) error {
	if trigger == nil {
		return errUnavailable("upstream catalog sync unavailable")
	}
	if err := trigger.Tick(ctx); err != nil {
		return errUnavailable("upstream catalog sync unavailable")
	}
	return nil
}

type PublicPriceDraft struct {
	Revision int64              `json:"revision"`
	Models   []PublicModelAdmin `json:"models"`
	Currency string             `json:"currency"`
	Unit     string             `json:"unit"`
}
type PublicPriceDraftSaveRequest struct {
	ExpectedRevision int64              `json:"expected_revision"`
	Models           []PublicModelAdmin `json:"models"`
}
type PublicValidationIssue struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}
type PublicValidationResponse struct {
	Valid  bool                    `json:"valid"`
	Issues []PublicValidationIssue `json:"issues"`
}
type PublicRelease struct {
	GUID           string `json:"guid"`
	Version        int64  `json:"version"`
	Reason         string `json:"reason"`
	SourceRevision int64  `json:"source_revision"`
	CreatedAt      string `json:"created_at"`
}
type PublicReleaseListResponse struct {
	Items    []PublicRelease `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int64           `json:"total"`
}
type PublicModelVisible struct {
	ModelKey                       string   `json:"model_key"`
	DisplayName                    string   `json:"display_name"`
	Provider                       string   `json:"provider"`
	Capabilities                   []string `json:"capabilities"`
	ContextWindow                  int64    `json:"context_window"`
	InputPriceUSDPerMillionTokens  *string  `json:"input_price_usd_per_million_tokens,omitempty"`
	OutputPriceUSDPerMillionTokens *string  `json:"output_price_usd_per_million_tokens,omitempty"`
	PriceVisibility                string   `json:"price_visibility"`
	ReleaseVersion                 int64    `json:"release_version"`
	PricingType                    string   `json:"pricing_type"`
	PublicDisplayGroup             string   `json:"public_display_group,omitempty"`
	EndpointTypes                  []string `json:"endpoint_types"`
	PublicRestrictions             []string `json:"public_restrictions,omitempty"`
	PriceSource                    string   `json:"price_source,omitempty"`
	PriceReviewer                  string   `json:"price_reviewer,omitempty"`
	EffectiveAt                    string   `json:"effective_at,omitempty"`
	UpdatedAt                      string   `json:"updated_at"`
}
type PublicPriceReleaseView struct {
	Release PublicRelease        `json:"release"`
	Items   []PublicModelVisible `json:"items"`
}

func releaseTime(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }
func projectRelease(guid, version int64, reason string, revision, at int64) PublicRelease {
	return PublicRelease{strconv.FormatInt(guid, 10), version, reason, revision, releaseTime(at)}
}

func (s *PublicPriceSnapshotService) GetDraft(ctx context.Context, actorID int64) (*PublicPriceDraft, error) {
	if s == nil || s.db == nil || actorID <= 0 {
		return nil, errBadRequest("invalid public price draft request")
	}
	var out PublicPriceDraft
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPublicModelRoot(tx, actorID); e != nil {
			return e
		}
		state, e := lockPublicPriceDraftState(tx)
		if e != nil {
			return e
		}
		var rows []models.PublicModelConfig
		if e = tx.Where("is_deleted=0").Order("model_key").Find(&rows).Error; e != nil {
			return errUnavailable("public model persistence unavailable")
		}
		out = PublicPriceDraft{Revision: state.Revision, Models: make([]PublicModelAdmin, len(rows)), Currency: "USD", Unit: "million_tokens"}
		for i := range rows {
			out.Models[i] = projectPublicModel(rows[i])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *PublicPriceSnapshotService) SaveDraft(ctx context.Context, actorID int64, in PublicPriceDraftSaveRequest) (*PublicPriceDraft, error) {
	if s == nil || s.db == nil || actorID <= 0 || in.ExpectedRevision < 1 || in.Models == nil {
		return nil, errBadRequest("invalid public price draft request")
	}
	var out *PublicPriceDraft
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, e := lockPublicModelRoot(tx, actorID)
		if e != nil {
			return e
		}
		state, e := lockPublicPriceDraftState(tx)
		if e != nil {
			return e
		}
		if state.Revision != in.ExpectedRevision {
			return errConflict("public price draft revision conflict")
		}
		var rows []models.PublicModelConfig
		if e = tx.Where("is_deleted=0").Order("model_key").Find(&rows).Error; e != nil {
			return errUnavailable("public model persistence unavailable")
		}
		if len(rows) != len(in.Models) {
			return errBadRequest("public price draft models mismatch")
		}
		byGUID := map[string]PublicModelAdmin{}
		for _, v := range in.Models {
			if _, ok := byGUID[v.GUID]; ok {
				return errBadRequest("duplicate public price draft model")
			}
			byGUID[v.GUID] = v
		}
		for i := range rows {
			current := projectPublicModel(rows[i])
			candidate, ok := byGUID[current.GUID]
			if !ok || candidate.ModelKey != current.ModelKey || candidate.UpstreamModelID != current.UpstreamModelID || candidate.DisplayName != current.DisplayName || candidate.Provider != current.Provider || fmt.Sprint(candidate.Capabilities) != fmt.Sprint(current.Capabilities) || candidate.ContextWindow != current.ContextWindow || candidate.Status != current.Status || candidate.Revision != current.Revision || !validPublicPrice(candidate.InputPriceUSDPerMillionTokens) || !validPublicPrice(candidate.OutputPriceUSDPerMillionTokens) {
				return errBadRequest("public price draft models mismatch")
			}
			if e = tx.Model(&models.PublicModelConfig{}).Where("id=? AND revision=? AND is_deleted=0", rows[i].ID, rows[i].Revision).Updates(map[string]any{"input_price_usd_per_million_tokens": candidate.InputPriceUSDPerMillionTokens, "output_price_usd_per_million_tokens": candidate.OutputPriceUSDPerMillionTokens, "updated_at": s.now(), "updated_by": actor.ID}).Error; e != nil {
				return errUnavailable("public price draft persistence unavailable")
			}
			rows[i].InputPriceUSDPerMillionTokens = candidate.InputPriceUSDPerMillionTokens
			rows[i].OutputPriceUSDPerMillionTokens = candidate.OutputPriceUSDPerMillionTokens
		}
		if e = advancePublicPriceDraftState(tx, state, actor.ID, s.now()); e != nil {
			return e
		}
		if e = writePublicModelAudit(tx, s.nextGUID(), s.now(), actor.ID, "public_pricing.draft.save", "pricing", state.Guid, models.JSONMap{"revision": state.Revision}); e != nil {
			return errUnavailable("audit persistence unavailable")
		}
		v := PublicPriceDraft{Revision: state.Revision, Models: make([]PublicModelAdmin, len(rows)), Currency: "USD", Unit: "million_tokens"}
		for i := range rows {
			v.Models[i] = projectPublicModel(rows[i])
		}
		out = &v
		return nil
	})
	if err != nil {
		return nil, mapPublicModelWriteError(err)
	}
	return out, nil
}

func validatePublicPriceDraft(d PublicPriceDraft) []PublicValidationIssue {
	issues := []PublicValidationIssue{}
	for i, m := range d.Models {
		if m.Status != "active" {
			continue
		}
		if !validPublicPrice(m.InputPriceUSDPerMillionTokens) {
			issues = append(issues, PublicValidationIssue{fmt.Sprintf("models[%d].input_price_usd_per_million_tokens", i), "invalid_decimal"})
		}
		if !validPublicPrice(m.OutputPriceUSDPerMillionTokens) {
			issues = append(issues, PublicValidationIssue{fmt.Sprintf("models[%d].output_price_usd_per_million_tokens", i), "invalid_decimal"})
		}
		if m.InputPriceUSDPerMillionTokens != nil || m.OutputPriceUSDPerMillionTokens != nil {
			if !validPublicModelText(m.PriceSource, 255) {
				issues = append(issues, PublicValidationIssue{fmt.Sprintf("models[%d].price_source", i), "required"})
			}
			if !validPublicModelText(m.PriceReviewer, 128) {
				issues = append(issues, PublicValidationIssue{fmt.Sprintf("models[%d].price_reviewer", i), "required"})
			}
			if m.PriceEffectiveAt == nil || *m.PriceEffectiveAt <= 0 {
				issues = append(issues, PublicValidationIssue{fmt.Sprintf("models[%d].price_effective_at", i), "required"})
			}
		}
	}
	if len(d.Models) == 0 {
		issues = append(issues, PublicValidationIssue{"models", "required"})
	}
	return issues
}
func (s *PublicPriceSnapshotService) Validate(ctx context.Context, actorID, revision int64) (*PublicValidationResponse, error) {
	d, e := s.GetDraft(ctx, actorID)
	if e != nil {
		return nil, e
	}
	if d.Revision != revision {
		return nil, errConflict("public price draft revision conflict")
	}
	issues := validatePublicPriceDraft(*d)
	return &PublicValidationResponse{Valid: len(issues) == 0, Issues: issues}, nil
}

func normalizePage(page, size int) (int, int, error) {
	if page < 1 {
		return 0, 0, errBadRequest("invalid page")
	}
	if size != 20 && size != 50 && size != 100 {
		return 0, 0, errBadRequest("invalid page size")
	}
	return page, size, nil
}
func (s *PublicPriceSnapshotService) ListReleases(ctx context.Context, page, pageSize int) (*PublicReleaseListResponse, error) {
	page, pageSize, e := normalizePage(page, pageSize)
	if e != nil {
		return nil, e
	}
	out := &PublicReleaseListResponse{Items: []PublicRelease{}, Page: page, PageSize: pageSize}
	var rows []models.PublicPriceSnapshot
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if x := tx.Model(&models.PublicPriceSnapshot{}).Where("is_deleted=0").Count(&out.Total).Error; x != nil {
			return errUnavailable("price release persistence unavailable")
		}
		if x := tx.Where("is_deleted=0").Order("version DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; x != nil {
			return errUnavailable("price release persistence unavailable")
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		out.Items = append(out.Items, projectRelease(r.Guid, r.Version, r.Reason.String(), r.SourceRevision, r.PublishedAt))
	}
	return out, nil
}
func (s *PublicPriceSnapshotService) GetRelease(ctx context.Context, guid int64) (*PublicPriceReleaseView, error) {
	if guid <= 0 {
		return nil, errBadRequest("invalid price release guid")
	}
	var snap models.PublicPriceSnapshot
	var items []models.PublicPriceSnapshotItem
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if x := tx.Where("guid=? AND is_deleted=0", guid).First(&snap).Error; x == gorm.ErrRecordNotFound {
			return errNotFound("price release not found")
		} else if x != nil {
			return errUnavailable("price release persistence unavailable")
		}
		if x := tx.Where("snapshot_id=? AND is_deleted=0", snap.ID).Order("model_key").Find(&items).Error; x != nil {
			return errUnavailable("price release persistence unavailable")
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	if _, e = prepareRestoredPublicPriceSnapshot(items, snap.ContentHash); e != nil {
		return nil, e
	}
	return projectPublicPriceReleaseDetail(snap, items), nil
}
func projectPublicPriceReleaseDetail(snap models.PublicPriceSnapshot, rows []models.PublicPriceSnapshotItem) *PublicPriceReleaseView {
	v := &PublicPriceReleaseView{Release: projectRelease(snap.Guid, snap.Version, snap.Reason.String(), snap.SourceRevision, snap.PublishedAt), Items: make([]PublicModelVisible, len(rows))}
	for i, r := range rows {
		item := projectPublicCatalogItem(r, snap)
		v.Items[i] = PublicModelVisible{ModelKey: r.ModelKey, DisplayName: r.DisplayName, Provider: r.Provider, Capabilities: append([]string(nil), r.Capabilities...), ContextWindow: r.ContextWindow, InputPriceUSDPerMillionTokens: r.InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens: r.OutputPriceUSDPerMillionTokens, PriceVisibility: "visible", ReleaseVersion: snap.Version, PricingType: item.PricingType, PublicDisplayGroup: item.PublicDisplayGroup, EndpointTypes: item.EndpointTypes, PublicRestrictions: item.PublicRestrictions, PriceSource: item.PriceSource, PriceReviewer: item.PriceReviewer, EffectiveAt: item.EffectiveAt, UpdatedAt: item.UpdatedAt}
	}
	return v
}

func (s *PublicContentService) ListReleases(ctx context.Context, page, pageSize int) (*PublicReleaseListResponse, error) {
	page, pageSize, e := normalizePage(page, pageSize)
	if e != nil {
		return nil, e
	}
	out := &PublicReleaseListResponse{Items: []PublicRelease{}, Page: page, PageSize: pageSize}
	var rows []models.PublicContentRelease
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if x := tx.Model(&models.PublicContentRelease{}).Where("is_deleted=0 AND document_kind=?", models.PublicContentDocumentSite).Count(&out.Total).Error; x != nil {
			return errUnavailable("content release persistence unavailable")
		}
		return tx.Where("is_deleted=0 AND document_kind=?", models.PublicContentDocumentSite).Order("version DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	})
	if e != nil {
		return nil, errUnavailable("content release persistence unavailable")
	}
	for _, r := range rows {
		reason := "root_publish"
		if r.RestoredFromReleaseID != nil {
			reason = "restore"
		}
		out.Items = append(out.Items, projectRelease(r.Guid, r.Version, reason, r.SourceRevision, r.PublishedAt))
	}
	return out, nil
}

type PublicContentReleaseAdminView struct {
	Release PublicRelease      `json:"release"`
	Content PublicContentDraft `json:"content"`
}

func (s *PublicContentService) GetRelease(ctx context.Context, guid int64) (*PublicContentReleaseAdminView, error) {
	if guid <= 0 {
		return nil, errBadRequest("invalid content release guid")
	}
	var r models.PublicContentRelease
	e := s.db.WithContext(ctx).Where("guid=? AND is_deleted=0 AND document_kind=?", guid, models.PublicContentDocumentSite).First(&r).Error
	if e == gorm.ErrRecordNotFound {
		return nil, errNotFound("content release not found")
	}
	if e != nil {
		return nil, errUnavailable("content release persistence unavailable")
	}
	if e = verifyPublicContentRelease(r); e != nil {
		return nil, e
	}
	reason := "root_publish"
	if r.RestoredFromReleaseID != nil {
		reason = "restore"
	}
	return &PublicContentReleaseAdminView{Release: projectRelease(r.Guid, r.Version, reason, r.SourceRevision, r.PublishedAt), Content: projectContentPayload(r.Payload, r.SourceRevision)}, nil
}
