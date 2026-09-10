package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

type PublicCatalogItem struct {
	ModelKey                       string   `json:"model_key"`
	DisplayName                    string   `json:"display_name"`
	Provider                       string   `json:"provider"`
	Capabilities                   []string `json:"capabilities"`
	ContextWindow                  int64    `json:"context_window"`
	InputPriceUSDPerMillionTokens  string   `json:"-"`
	OutputPriceUSDPerMillionTokens string   `json:"-"`
	PricingType                    string   `json:"pricing_type"`
	PublicDisplayGroup             string   `json:"public_display_group,omitempty"`
	EndpointTypes                  []string `json:"endpoint_types"`
	PublicRestrictions             []string `json:"public_restrictions,omitempty"`
	PriceSource                    string   `json:"price_source,omitempty"`
	PriceReviewer                  string   `json:"price_reviewer,omitempty"`
	EffectiveAt                    string   `json:"effective_at,omitempty"`
	UpdatedAt                      string   `json:"updated_at"`
}

type PublicModelRead struct {
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

type PublicCatalogListRequest struct {
	Search, Provider, Capability, EndpointType, PublicDisplayGroup, PricingType, Sort, Order string
	Page, PageSize                                                                           int
}
type PublicModelListRead struct {
	Items          []PublicModelRead   `json:"items"`
	Page           int                 `json:"page"`
	PageSize       int                 `json:"page_size"`
	Total          int                 `json:"total"`
	ReleaseVersion int64               `json:"release_version"`
	Facets         PublicCatalogFacets `json:"facets"`
}
type PublicCatalogFacets struct {
	Providers           []string `json:"providers"`
	Capabilities        []string `json:"capabilities"`
	EndpointTypes       []string `json:"endpoint_types"`
	PublicDisplayGroups []string `json:"public_display_groups"`
}
type PublicModelDetailRead struct {
	Model PublicModelRead `json:"model"`
}
type PublicCatalogProjection struct {
	Content               PublicContentDraft
	ContentReleaseVersion int64
	PriceReleaseVersion   int64
	PriceVisibility       models.PublicPriceVisibility
	ETag                  string
	Items                 []PublicCatalogItem
	GoneKeys              map[string]struct{}
}

type PublicCatalogReadService struct{ db *gorm.DB }

func NewPublicCatalogReadService(db *gorm.DB) *PublicCatalogReadService {
	return &PublicCatalogReadService{db: db}
}

func (s *PublicCatalogReadService) Projection(ctx context.Context) (*PublicCatalogProjection, error) {
	if s == nil || s.db == nil {
		return nil, errUnavailable("public catalog unavailable")
	}
	var out *PublicCatalogProjection
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state models.PublicPublicationState
		if e := tx.Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; e != nil || state.ContentReleaseID == nil || state.PriceSnapshotID == nil {
			return errUnavailable("committed publication unavailable")
		}
		if state.PriceVisibility != models.PublicPriceVisibilityVisible && state.PriceVisibility != models.PublicPriceVisibilityAuthenticatedOnly {
			return errUnavailable("committed publication unavailable")
		}
		var content models.PublicContentRelease
		var price models.PublicPriceSnapshot
		if e := tx.First(&content, *state.ContentReleaseID).Error; e != nil || content.IsDeleted != 0 || verifyPublicContentRelease(content) != nil {
			return errUnavailable("committed content integrity unavailable")
		}
		if e := tx.First(&price, *state.PriceSnapshotID).Error; e != nil || price.IsDeleted != 0 {
			return errUnavailable("committed price snapshot unavailable")
		}
		boundVersion, ok := jsonNumberInt64(content.Payload["price_snapshot_version"])
		if fmt.Sprint(content.Payload["price_snapshot_guid"]) != fmt.Sprint(price.Guid) || !ok || boundVersion != price.Version {
			return errUnavailable("committed publication binding unavailable")
		}
		var rows []models.PublicPriceSnapshotItem
		if e := tx.Where("snapshot_id=? AND is_deleted=0", price.ID).Order("model_key").Find(&rows).Error; e != nil {
			return errUnavailable("committed price snapshot unavailable")
		}
		hash, e := hashPublicPriceSnapshotItems(rows)
		if e != nil || len(price.ContentHash) != 64 || hash != price.ContentHash {
			return errUnavailable("committed price snapshot integrity unavailable")
		}
		var configs []models.PublicModelConfig
		if e = tx.Find(&configs).Error; e != nil {
			return errUnavailable("public model state unavailable")
		}
		byID := make(map[int64]models.PublicModelConfig, len(configs))
		gone := map[string]struct{}{}
		for _, config := range configs {
			byID[config.ID] = config
			if config.EverPublished == 1 && (config.IsDeleted != 0 || config.Status != models.PublicModelConfigStatusActive) {
				gone[config.ModelKey] = struct{}{}
			}
		}
		items := make([]PublicCatalogItem, 0, len(rows))
		for _, row := range rows {
			config, exists := byID[row.ModelConfigID]
			if !exists || config.IsDeleted != 0 || config.Status != models.PublicModelConfigStatusActive || config.ModelKey != row.ModelKey {
				return errUnavailable("committed publication generation pending")
			}
			items = append(items, projectPublicCatalogItem(row, price))
		}
		if e = validateContentReleaseForPriceItems(content, rows); e != nil {
			return errUnavailable("committed publication generation pending")
		}
		sum := sha256.Sum256([]byte(content.ContentHash + ":" + price.ContentHash + ":" + state.PriceVisibility.String()))
		out = &PublicCatalogProjection{Content: projectContentPayload(content.Payload, content.SourceRevision), ContentReleaseVersion: content.Version, PriceReleaseVersion: price.Version, PriceVisibility: state.PriceVisibility, ETag: `"` + hex.EncodeToString(sum[:]) + `"`, Items: items, GoneKeys: gone}
		return nil
	})
	return out, err
}

func (p *PublicCatalogProjection) model(item PublicCatalogItem, authenticated bool) PublicModelRead {
	visibility := p.PriceVisibility.String()
	out := PublicModelRead{ModelKey: item.ModelKey, DisplayName: item.DisplayName, Provider: item.Provider, Capabilities: clonePublicStrings(item.Capabilities), ContextWindow: item.ContextWindow, PriceVisibility: visibility, ReleaseVersion: p.PriceReleaseVersion, PricingType: item.PricingType, PublicDisplayGroup: item.PublicDisplayGroup, EndpointTypes: clonePublicStrings(item.EndpointTypes), PublicRestrictions: clonePublicStrings(item.PublicRestrictions), PriceSource: item.PriceSource, PriceReviewer: item.PriceReviewer, EffectiveAt: item.EffectiveAt, UpdatedAt: item.UpdatedAt}
	if p.PriceVisibility == models.PublicPriceVisibilityVisible || authenticated {
		out.PriceVisibility = "visible"
		if item.InputPriceUSDPerMillionTokens != "" {
			value := item.InputPriceUSDPerMillionTokens
			out.InputPriceUSDPerMillionTokens = &value
		}
		if item.OutputPriceUSDPerMillionTokens != "" {
			value := item.OutputPriceUSDPerMillionTokens
			out.OutputPriceUSDPerMillionTokens = &value
		}
	}
	return out
}
func (p *PublicCatalogProjection) List(req PublicCatalogListRequest, authenticated bool) PublicModelListRead {
	filtered := make([]PublicCatalogItem, 0, len(p.Items))
	search := strings.ToLower(req.Search)
	for _, item := range p.Items {
		if search != "" && !strings.Contains(strings.ToLower(item.ModelKey+" "+item.DisplayName), search) {
			continue
		}
		if req.Provider != "" && item.Provider != req.Provider {
			continue
		}
		if req.Capability != "" {
			found := false
			for _, c := range item.Capabilities {
				if c == req.Capability {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if req.EndpointType != "" && !containsExact(item.EndpointTypes, req.EndpointType) {
			continue
		}
		if req.PublicDisplayGroup != "" && item.PublicDisplayGroup != req.PublicDisplayGroup {
			continue
		}
		if req.PricingType != "" && item.PricingType != req.PricingType {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return publicCatalogLess(filtered[i], filtered[j], req.Sort, req.Order) })
	page, size := req.Page, req.PageSize
	if page < 1 {
		page = 1
	}
	if size == 0 {
		size = 20
	}
	start := len(filtered)
	if page-1 <= len(filtered)/size {
		start = (page - 1) * size
		if start > len(filtered) {
			start = len(filtered)
		}
	}
	end := len(filtered)
	if start < len(filtered) && size < len(filtered)-start {
		end = start + size
	}
	items := make([]PublicModelRead, 0, end-start)
	for _, item := range filtered[start:end] {
		items = append(items, p.model(item, authenticated))
	}
	return PublicModelListRead{Items: items, Page: page, PageSize: size, Total: len(filtered), ReleaseVersion: p.PriceReleaseVersion, Facets: p.facets()}
}

func (p *PublicCatalogProjection) facets() PublicCatalogFacets {
	providers, capabilities, endpoints, groups := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, item := range p.Items {
		if item.Provider != "" {
			providers[item.Provider] = true
		}
		if item.PublicDisplayGroup != "" {
			groups[item.PublicDisplayGroup] = true
		}
		for _, v := range item.Capabilities {
			capabilities[v] = true
		}
		for _, v := range item.EndpointTypes {
			endpoints[v] = true
		}
	}
	return PublicCatalogFacets{Providers: sortedPublicFacet(providers), Capabilities: sortedPublicFacet(capabilities), EndpointTypes: sortedPublicFacet(endpoints), PublicDisplayGroups: sortedPublicFacet(groups)}
}
func sortedPublicFacet(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for v := range values {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func containsExact(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func clonePublicStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func publicCatalogLess(a, b PublicCatalogItem, field, order string) bool {
	if field == "input_price" || field == "output_price" {
		av, bv := a.InputPriceUSDPerMillionTokens, b.InputPriceUSDPerMillionTokens
		if field == "output_price" {
			av, bv = a.OutputPriceUSDPerMillionTokens, b.OutputPriceUSDPerMillionTokens
		}
		if (av == "") != (bv == "") {
			return av != ""
		}
	}
	cmp := strings.Compare(a.ModelKey, b.ModelKey)
	switch field {
	case "name":
		cmp = strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName))
	case "input_price":
		cmp = compareCatalogDecimal(a.InputPriceUSDPerMillionTokens, b.InputPriceUSDPerMillionTokens)
	case "output_price":
		cmp = compareCatalogDecimal(a.OutputPriceUSDPerMillionTokens, b.OutputPriceUSDPerMillionTokens)
	}
	if cmp == 0 {
		cmp = strings.Compare(a.ModelKey, b.ModelKey)
	}
	if order == "desc" {
		return cmp > 0
	}
	return cmp < 0
}

func compareCatalogDecimal(a, b string) int {
	if a == "" {
		if b == "" {
			return 0
		}
		return 1
	}
	if b == "" {
		return -1
	}
	var ar, br big.Rat
	if _, ok := ar.SetString(a); !ok {
		return strings.Compare(a, b)
	}
	if _, ok := br.SetString(b); !ok {
		return strings.Compare(a, b)
	}
	return ar.Cmp(&br)
}

func projectPublicCatalogItem(row models.PublicPriceSnapshotItem, snapshot models.PublicPriceSnapshot) PublicCatalogItem {
	effective := ""
	if row.EffectiveAt != nil {
		effective = releaseTime(*row.EffectiveAt)
	}
	pricingType := row.PricingType
	if pricingType == "" {
		pricingType = "token"
	}
	return PublicCatalogItem{ModelKey: row.ModelKey, DisplayName: row.DisplayName, Provider: row.Provider, Capabilities: clonePublicStrings(row.Capabilities), ContextWindow: row.ContextWindow, InputPriceUSDPerMillionTokens: publicPriceValue(row.InputPriceUSDPerMillionTokens), OutputPriceUSDPerMillionTokens: publicPriceValue(row.OutputPriceUSDPerMillionTokens), PricingType: pricingType, PublicDisplayGroup: row.PublicDisplayGroup, EndpointTypes: clonePublicStrings(row.EndpointTypes), PublicRestrictions: clonePublicStrings(row.PublicRestrictions), PriceSource: row.PriceSource, PriceReviewer: row.PriceReviewer, EffectiveAt: effective, UpdatedAt: releaseTime(snapshot.PublishedAt)}
}
func publicPriceValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func (p *PublicCatalogProjection) Detail(key string, authenticated bool) (*PublicModelDetailRead, int) {
	for _, item := range p.Items {
		if item.ModelKey == key {
			return &PublicModelDetailRead{p.model(item, authenticated)}, 200
		}
	}
	if _, ok := p.GoneKeys[key]; ok {
		return nil, 410
	}
	return nil, 404
}
