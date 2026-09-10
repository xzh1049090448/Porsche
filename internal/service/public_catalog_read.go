package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
}

type PublicCatalogListRequest struct {
	Search, Provider, Capability string
	Page, PageSize               int
}
type PublicModelListRead struct {
	Items          []PublicModelRead `json:"items"`
	Page           int               `json:"page"`
	PageSize       int               `json:"page_size"`
	Total          int               `json:"total"`
	ReleaseVersion int64             `json:"release_version"`
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
			items = append(items, PublicCatalogItem{row.ModelKey, row.DisplayName, row.Provider, append([]string(nil), row.Capabilities...), row.ContextWindow, row.InputPriceUSDPerMillionTokens, row.OutputPriceUSDPerMillionTokens})
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
	out := PublicModelRead{item.ModelKey, item.DisplayName, item.Provider, append([]string(nil), item.Capabilities...), item.ContextWindow, nil, nil, visibility, p.PriceReleaseVersion}
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
		filtered = append(filtered, item)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ModelKey < filtered[j].ModelKey })
	page, size := req.Page, req.PageSize
	if page < 1 {
		page = 1
	}
	if size == 0 {
		size = 20
	}
	start := (page - 1) * size
	end := start + size
	if start > len(filtered) {
		start = len(filtered)
	}
	if end > len(filtered) {
		end = len(filtered)
	}
	items := make([]PublicModelRead, 0, end-start)
	for _, item := range filtered[start:end] {
		items = append(items, p.model(item, authenticated))
	}
	return PublicModelListRead{items, page, size, len(filtered), p.PriceReleaseVersion}
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
