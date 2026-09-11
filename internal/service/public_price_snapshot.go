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

const publicPublicationStateKey = "site"

type PublicPriceSnapshotRequest struct {
	ActorID          int64
	ExpectedRevision int64
	IdempotencyKey   string
}

type PublicPriceSnapshotRestoreRequest struct {
	ActorID          int64
	ExpectedRevision int64
	SnapshotGUID     string
	IdempotencyKey   string
}

type PublicPriceSnapshotRelease struct {
	GUID           string `json:"guid"`
	Version        int64  `json:"version"`
	Reason         string `json:"reason"`
	SourceRevision int64  `json:"source_revision"`
	CreatedAt      string `json:"created_at"`
}

// PublicPriceProjectionItem is the safe dynamic projection of the current
// immutable snapshot after applying present model lifecycle state.
type PublicPriceProjectionItem struct {
	ModelKey                       string
	DisplayName                    string
	Provider                       string
	Capabilities                   []string
	ContextWindow                  int64
	InputPriceUSDPerMillionTokens  *string
	OutputPriceUSDPerMillionTokens *string
}

// CurrentActiveProjection prevents a stale static snapshot pointer from
// exposing a model that the monitor has already inactivated.
func (s *PublicPriceSnapshotService) CurrentActiveProjection(ctx context.Context) ([]PublicPriceProjectionItem, error) {
	if s == nil || s.db == nil {
		return nil, errUnavailable("public price projection unavailable")
	}
	out := []PublicPriceProjectionItem{}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state models.PublicPublicationState
		if e := tx.Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; e != nil {
			return errUnavailable("publication state unavailable")
		}
		if state.PriceSnapshotID == nil {
			return nil
		}
		var rows []models.PublicPriceSnapshotItem
		if e := tx.Model(&models.PublicPriceSnapshotItem{}).Joins("JOIN public_model_configs m ON m.id=public_price_snapshot_items.model_config_id AND m.status=? AND m.is_deleted=0", models.PublicModelConfigStatusActive).Where("public_price_snapshot_items.snapshot_id=? AND public_price_snapshot_items.is_deleted=0", *state.PriceSnapshotID).Order("public_price_snapshot_items.model_key").Find(&rows).Error; e != nil {
			return errUnavailable("public price projection unavailable")
		}
		for _, r := range rows {
			out = append(out, PublicPriceProjectionItem{ModelKey: r.ModelKey, DisplayName: r.DisplayName, Provider: r.Provider, Capabilities: append([]string(nil), r.Capabilities...), ContextWindow: r.ContextWindow, InputPriceUSDPerMillionTokens: r.InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens: r.OutputPriceUSDPerMillionTokens})
		}
		return nil
	})
	return out, err
}

type preparedPublicPriceSnapshot struct {
	Items []models.PublicPriceSnapshotItem
	Hash  string
}

type publicPriceSnapshotRestorePlan struct {
	Include    []models.PublicPriceSnapshotItem
	Deactivate []models.PublicModelConfig
}

func planPublicPriceSnapshotRestore(items []models.PublicPriceSnapshotItem, current []models.PublicModelConfig) (*publicPriceSnapshotRestorePlan, error) {
	byID := make(map[int64]models.PublicModelConfig, len(current))
	for _, m := range current {
		byID[m.ID] = m
	}
	included := make(map[int64]bool, len(items))
	plan := &publicPriceSnapshotRestorePlan{Include: append([]models.PublicPriceSnapshotItem(nil), items...)}
	for _, item := range items {
		m, ok := byID[item.ModelConfigID]
		if !ok || m.IsDeleted != 0 || m.Status != models.PublicModelConfigStatusActive || m.ModelKey != item.ModelKey || m.UpstreamModelID != item.UpstreamModelID {
			return nil, errConflict("historical price snapshot model is no longer active")
		}
		included[m.ID] = true
	}
	for _, m := range current {
		if m.IsDeleted == 0 && m.Status == models.PublicModelConfigStatusActive && !included[m.ID] {
			plan.Deactivate = append(plan.Deactivate, m)
		}
	}
	return plan, nil
}

type PublicPriceSnapshotService struct {
	db       *gorm.DB
	now      func() int64
	nextGUID func() int64
	fail     func(string) error
}

func NewPublicPriceSnapshotService(db *gorm.DB) *PublicPriceSnapshotService {
	return &PublicPriceSnapshotService{db: db, now: persistence.NowMillis, nextGUID: persistence.NextGUID, fail: func(string) error { return nil }}
}

func preparePublicPriceSnapshot(rows []models.PublicModelConfig) (*preparedPublicPriceSnapshot, error) {
	items := make([]models.PublicPriceSnapshotItem, 0, len(rows))
	modelIDs, modelKeys, upstreamIDs := map[int64]bool{}, map[string]bool{}, map[string]bool{}
	for _, m := range rows {
		if m.IsDeleted != 0 || m.Status != models.PublicModelConfigStatusActive {
			continue
		}
		if m.ID <= 0 || modelIDs[m.ID] || modelKeys[m.ModelKey] || upstreamIDs[m.UpstreamModelID] || !publiccontent.ValidModelKey(m.ModelKey) || !publiccontent.ValidUpstreamModelID(m.UpstreamModelID) ||
			!validPublicModelText(m.DisplayName, 128) || !validPublicModelText(m.Provider, 128) ||
			!validPublicModelCapabilities([]string(m.Capabilities)) || m.ContextWindow <= 0 ||
			!validPublicPrice(m.InputPriceUSDPerMillionTokens) || !validPublicPrice(m.OutputPriceUSDPerMillionTokens) || !validPublishedPriceProvenance(m) {
			return nil, errUnprocessable("public price snapshot validation failed")
		}
		modelIDs[m.ID], modelKeys[m.ModelKey], upstreamIDs[m.UpstreamModelID] = true, true, true
		items = append(items, models.PublicPriceSnapshotItem{ModelConfigID: m.ID, ModelKey: m.ModelKey, UpstreamModelID: m.UpstreamModelID, DisplayName: m.DisplayName, Provider: m.Provider, Capabilities: clonePublicJSONSlice(m.Capabilities), ContextWindow: m.ContextWindow, InputPriceUSDPerMillionTokens: m.InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens: m.OutputPriceUSDPerMillionTokens, UpstreamCheckedAt: m.LastUpstreamCheckAt, PricingType: "token", PublicDisplayGroup: m.PublicDisplayGroup, EndpointTypes: clonePublicJSONSlice(m.EndpointTypes), PublicRestrictions: clonePublicJSONSlice(m.PublicRestrictions), PriceSource: m.PriceSource, PriceReviewer: m.PriceReviewer, EffectiveAt: m.PriceEffectiveAt})
	}
	if len(items) == 0 {
		return nil, errUnprocessable("public price snapshot requires an active priced model")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ModelKey < items[j].ModelKey })
	hash, err := hashPublicPriceSnapshotItems(items)
	if err != nil {
		return nil, errUnavailable("public price snapshot hashing unavailable")
	}
	return &preparedPublicPriceSnapshot{Items: items, Hash: hash}, nil
}

func validPublishedPriceProvenance(m models.PublicModelConfig) bool {
	if m.InputPriceUSDPerMillionTokens == nil && m.OutputPriceUSDPerMillionTokens == nil {
		return true
	}
	return validPublicModelText(m.PriceSource, 255) && validPublicModelText(m.PriceReviewer, 128) && m.PriceEffectiveAt != nil && *m.PriceEffectiveAt > 0
}

func prepareRestoredPublicPriceSnapshot(items []models.PublicPriceSnapshotItem, storedHash string) (*preparedPublicPriceSnapshot, error) {
	legacyMatch, integrityOK := verifyPublicPriceSnapshotHash(items, storedHash)
	if !integrityOK {
		return nil, errUnprocessable("historical price snapshot integrity validation failed")
	}
	rows := make([]models.PublicModelConfig, len(items))
	for i, item := range items {
		rows[i] = models.PublicModelConfig{ID: item.ModelConfigID, ModelKey: item.ModelKey, UpstreamModelID: item.UpstreamModelID, DisplayName: item.DisplayName, Provider: item.Provider, Capabilities: clonePublicJSONSlice(item.Capabilities), ContextWindow: item.ContextWindow, InputPriceUSDPerMillionTokens: item.InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens: item.OutputPriceUSDPerMillionTokens, Status: models.PublicModelConfigStatusActive, LastUpstreamCheckAt: item.UpstreamCheckedAt, PublicDisplayGroup: item.PublicDisplayGroup, EndpointTypes: clonePublicJSONSlice(item.EndpointTypes), PublicRestrictions: clonePublicJSONSlice(item.PublicRestrictions), PriceSource: item.PriceSource, PriceReviewer: item.PriceReviewer, PriceEffectiveAt: item.EffectiveAt}
	}
	prepared, err := preparePublicPriceSnapshot(rows)
	if err != nil {
		if !legacyMatch {
			return nil, err
		}
		currentHash, hashErr := hashPublicPriceSnapshotItems(items)
		if hashErr != nil {
			return nil, errUnavailable("public price snapshot hashing unavailable")
		}
		return &preparedPublicPriceSnapshot{Items: append([]models.PublicPriceSnapshotItem(nil), items...), Hash: currentHash}, nil
	}
	if prepared.Hash != storedHash && !legacyMatch {
		return nil, errUnprocessable("historical price snapshot integrity validation failed")
	}
	return prepared, nil
}

func verifyPublicPriceSnapshotHash(items []models.PublicPriceSnapshotItem, storedHash string) (legacy, ok bool) {
	if len(storedHash) != 64 {
		return false, false
	}
	current, err := hashPublicPriceSnapshotItems(items)
	if err == nil && current == storedHash {
		return false, true
	}
	preNormalization, err := hashPublicPriceSnapshotItemsWithCollectionMode(items, false)
	if err == nil && preNormalization == storedHash {
		return true, true
	}
	for _, item := range items {
		if item.PricingType != "token" || item.PublicDisplayGroup != "" || len(item.EndpointTypes) != 0 || len(item.PublicRestrictions) != 0 || item.PriceSource != "" || item.PriceReviewer != "" || item.EffectiveAt != nil {
			return false, false
		}
	}
	legacyHash, legacyOK := hashLegacyPublicPriceSnapshotItems(items)
	return legacyOK && legacyHash == storedHash, legacyOK && legacyHash == storedHash
}

func hashLegacyPublicPriceSnapshotItems(items []models.PublicPriceSnapshotItem) (string, bool) {
	type legacyCanonical struct {
		ModelKey        string   `json:"model_key"`
		UpstreamModelID string   `json:"upstream_model_id"`
		DisplayName     string   `json:"display_name"`
		Provider        string   `json:"provider"`
		Capabilities    []string `json:"capabilities"`
		ContextWindow   int64    `json:"context_window"`
		Input           string   `json:"input_price_usd_per_million_tokens"`
		Output          string   `json:"output_price_usd_per_million_tokens"`
		CheckedAt       *int64   `json:"upstream_checked_at"`
	}
	values := make([]legacyCanonical, len(items))
	for i, v := range items {
		if v.InputPriceUSDPerMillionTokens == nil || v.OutputPriceUSDPerMillionTokens == nil {
			return "", false
		}
		values[i] = legacyCanonical{v.ModelKey, v.UpstreamModelID, v.DisplayName, v.Provider, append([]string(nil), v.Capabilities...), v.ContextWindow, *v.InputPriceUSDPerMillionTokens, *v.OutputPriceUSDPerMillionTokens, v.UpstreamCheckedAt}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ModelKey < values[j].ModelKey })
	b, err := json.Marshal(values)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true
}

func hashPublicPriceSnapshotItems(items []models.PublicPriceSnapshotItem) (string, error) {
	return hashPublicPriceSnapshotItemsWithCollectionMode(items, true)
}

func hashPublicPriceSnapshotItemsWithCollectionMode(items []models.PublicPriceSnapshotItem, emptyArrays bool) (string, error) {
	type canonical struct {
		ModelKey           string   `json:"model_key"`
		UpstreamModelID    string   `json:"upstream_model_id"`
		DisplayName        string   `json:"display_name"`
		Provider           string   `json:"provider"`
		Capabilities       []string `json:"capabilities"`
		ContextWindow      int64    `json:"context_window"`
		Input              *string  `json:"input_price_usd_per_million_tokens,omitempty"`
		Output             *string  `json:"output_price_usd_per_million_tokens,omitempty"`
		CheckedAt          *int64   `json:"upstream_checked_at"`
		PricingType        string   `json:"pricing_type"`
		PublicDisplayGroup string   `json:"public_display_group,omitempty"`
		EndpointTypes      []string `json:"endpoint_types"`
		PublicRestrictions []string `json:"public_restrictions,omitempty"`
		PriceSource        string   `json:"price_source,omitempty"`
		PriceReviewer      string   `json:"price_reviewer,omitempty"`
		EffectiveAt        *int64   `json:"effective_at,omitempty"`
	}
	values := make([]canonical, len(items))
	for i, v := range items {
		capabilities := append([]string(nil), v.Capabilities...)
		endpointTypes := append([]string(nil), v.EndpointTypes...)
		publicRestrictions := append([]string(nil), v.PublicRestrictions...)
		if emptyArrays {
			capabilities = clonePublicStrings(v.Capabilities)
			endpointTypes = clonePublicStrings(v.EndpointTypes)
			publicRestrictions = clonePublicStrings(v.PublicRestrictions)
		}
		values[i] = canonical{v.ModelKey, v.UpstreamModelID, v.DisplayName, v.Provider, capabilities, v.ContextWindow, v.InputPriceUSDPerMillionTokens, v.OutputPriceUSDPerMillionTokens, v.UpstreamCheckedAt, v.PricingType, v.PublicDisplayGroup, endpointTypes, publicRestrictions, v.PriceSource, v.PriceReviewer, v.EffectiveAt}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ModelKey < values[j].ModelKey })
	b, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func clonePublicJSONSlice(values models.JSONSlice) models.JSONSlice {
	if len(values) == 0 {
		return models.JSONSlice{}
	}
	return append(models.JSONSlice(nil), values...)
}

func publicPriceIdempotencyBinding(actor int64, operation, key, payload string) string {
	s := sha256.Sum256([]byte(strconv.FormatInt(actor, 10) + "\x00" + operation + "\x00" + key + "\x00" + payload))
	return hex.EncodeToString(s[:])
}
func publicPriceIdempotencyKeyDigest(key string) string {
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])
}

func (s *PublicPriceSnapshotService) Publish(ctx context.Context, in PublicPriceSnapshotRequest, values ...PublicAdminTransactionOption) (*PublicPriceSnapshotRelease, error) {
	return s.transact(ctx, in.ActorID, in.ExpectedRevision, in.IdempotencyKey, "publish", 0, values)
}
func (s *PublicPriceSnapshotService) Restore(ctx context.Context, in PublicPriceSnapshotRestoreRequest, values ...PublicAdminTransactionOption) (*PublicPriceSnapshotRelease, error) {
	guid, err := strconv.ParseInt(in.SnapshotGUID, 10, 64)
	if err != nil || guid <= 0 {
		return nil, errBadRequest("invalid price snapshot guid")
	}
	return s.transact(ctx, in.ActorID, in.ExpectedRevision, in.IdempotencyKey, "restore", guid, values)
}

func newPublicPublicationState(actorID, now, guid int64) models.PublicPublicationState {
	return models.PublicPublicationState{
		AuditFields:     models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID},
		StateKey:        publicPublicationStateKey,
		PriceVisibility: models.PublicPriceVisibilityAuthenticatedOnly,
		Revision:        1,
	}
}

func lockOrCreatePublicPublicationState(tx *gorm.DB, actorID int64, nowFn func() int64, guidFn func() int64) (*models.PublicPublicationState, error) {
	var state models.PublicPublicationState
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_key = ? AND is_deleted = 0", publicPublicationStateKey).First(&state).Error
	if err == nil {
		return &state, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, errUnavailable("publication state unavailable")
	}
	now, guid := nowFn(), guidFn()
	if now <= 0 || guid <= 0 {
		return nil, errUnavailable("publication state unavailable")
	}
	initial := newPublicPublicationState(actorID, now, guid)
	if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
		return nil, errUnavailable("publication state unavailable")
	}
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_key = ? AND is_deleted = 0", publicPublicationStateKey).First(&state).Error; err != nil {
		return nil, errUnavailable("publication state unavailable")
	}
	return &state, nil
}

func (s *PublicPriceSnapshotService) transact(ctx context.Context, actorID, expected int64, key, operation string, restoreGUID int64, values []PublicAdminTransactionOption) (*PublicPriceSnapshotRelease, error) {
	if actorID <= 0 || expected <= 0 || len(key) < 1 || len(key) > 256 || strings.TrimSpace(key) != key {
		return nil, errBadRequest("invalid public price publication request")
	}
	options, optionErr := resolvePublicAdminTransactionOptions(values)
	if optionErr != nil {
		return nil, errUnavailable("public price action verification unavailable")
	}
	var out *PublicPriceSnapshotRelease
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockPublicModelRoot(tx, actorID)
		if err != nil {
			return err
		}
		if err = options.consumeTicket(ctx, tx); err != nil {
			return err
		}
		draft, err := lockPublicPriceDraftState(tx)
		if err != nil {
			return err
		}
		state, err := lockOrCreatePublicPublicationState(tx, actor.ID, s.now, s.nextGUID)
		if err != nil {
			return err
		}
		var prepared *preparedPublicPriceSnapshot
		var restorePlan *publicPriceSnapshotRestorePlan
		var restoredFrom *int64
		if operation == "publish" {
			var rows []models.PublicModelConfig
			if err = tx.Where("status = ? AND is_deleted = 0", models.PublicModelConfigStatusActive).Order("model_key ASC").Find(&rows).Error; err != nil {
				return errUnavailable("public model persistence unavailable")
			}
			prepared, err = preparePublicPriceSnapshot(rows)
		} else {
			var source models.PublicPriceSnapshot
			if err = tx.Where("guid = ? AND is_deleted = 0", restoreGUID).First(&source).Error; err == gorm.ErrRecordNotFound {
				return errNotFound("price snapshot not found")
			}
			if err != nil {
				return errUnavailable("price snapshot persistence unavailable")
			}
			var items []models.PublicPriceSnapshotItem
			if err = tx.Where("snapshot_id = ? AND is_deleted = 0", source.ID).Order("model_key ASC").Find(&items).Error; err != nil {
				return errUnavailable("price snapshot persistence unavailable")
			}
			prepared, err = prepareRestoredPublicPriceSnapshot(items, source.ContentHash)
			if err == nil {
				var current []models.PublicModelConfig
				if loadErr := tx.Find(&current).Error; loadErr != nil {
					return errUnavailable("public model persistence unavailable")
				}
				restorePlan, err = planPublicPriceSnapshotRestore(prepared.Items, current)
			}
			restoredFrom = &source.ID
		}
		if err != nil {
			return err
		}
		payload := operation + ":" + prepared.Hash + ":" + strconv.FormatInt(expected, 10) + ":" + strconv.FormatInt(restoreGUID, 10)
		binding, keyDigest := publicPriceIdempotencyBinding(actor.ID, operation, key, payload), publicPriceIdempotencyKeyDigest(key)
		if err = s.fail("replay_lookup"); err != nil {
			return errUnavailable("price snapshot replay unavailable")
		}
		if replay, found, conflictErr := findPublicPriceSnapshotReplay(tx, actor.ID, operation, keyDigest, binding); found || conflictErr != nil {
			out = replay
			return conflictErr
		}
		if draft.Revision != expected {
			return errConflict("public price draft revision conflict")
		}
		var currentContent *models.PublicContentRelease
		if state.ContentReleaseID != nil {
			currentContent = &models.PublicContentRelease{}
			if err = tx.Where("id = ? AND document_kind = ? AND is_deleted = 0", *state.ContentReleaseID, models.PublicContentDocumentSite).First(currentContent).Error; err != nil {
				return errUnavailable("published content release unavailable")
			}
			if err = validateContentReleaseForPriceItems(*currentContent, prepared.Items); err != nil {
				return err
			}
		}
		sourceRevision := draft.Revision
		if operation == "restore" {
			now := s.now()
			if now <= 0 {
				return errUnavailable("price snapshot persistence unavailable")
			}
			for _, item := range restorePlan.Include {
				result := tx.Model(&models.PublicModelConfig{}).Where("id=? AND model_key=? AND upstream_model_id=? AND status=? AND is_deleted=0", item.ModelConfigID, item.ModelKey, item.UpstreamModelID, models.PublicModelConfigStatusActive).Updates(map[string]any{"display_name": item.DisplayName, "provider": item.Provider, "capabilities": item.Capabilities, "context_window": item.ContextWindow, "input_price_usd_per_million_tokens": item.InputPriceUSDPerMillionTokens, "output_price_usd_per_million_tokens": item.OutputPriceUSDPerMillionTokens, "last_upstream_check_at": item.UpstreamCheckedAt, "revision": gorm.Expr("revision + 1"), "updated_at": now, "updated_by": actor.ID})
				if result.Error != nil || result.RowsAffected != 1 {
					return errConflict("historical price snapshot model changed")
				}
			}
			for _, m := range restorePlan.Deactivate {
				reason := "snapshot_restore"
				result := tx.Model(&models.PublicModelConfig{}).Where("id=? AND status=? AND is_deleted=0", m.ID, models.PublicModelConfigStatusActive).Updates(map[string]any{"status": models.PublicModelConfigStatusInactive, "inactive_reason": reason, "revision": gorm.Expr("revision + 1"), "updated_at": now, "updated_by": actor.ID})
				if result.Error != nil || result.RowsAffected != 1 {
					return errConflict("public price draft changed")
				}
			}
			if err = advancePublicPriceDraftState(tx, draft, actor.ID, now); err != nil {
				return err
			}
			sourceRevision = draft.Revision
		}
		now, guid := s.now(), s.nextGUID()
		if now <= 0 || guid <= 0 {
			return errUnavailable("price snapshot persistence unavailable")
		}
		version := int64(1)
		if state.PriceSnapshotID != nil {
			var current models.PublicPriceSnapshot
			if err = tx.Select("version").First(&current, *state.PriceSnapshotID).Error; err != nil {
				return errUnavailable("price snapshot persistence unavailable")
			}
			version = current.Version + 1
		}
		snapshot := models.PublicPriceSnapshot{Guid: guid, CreatedAt: now, CreatedBy: &actor.ID, UpdatedAt: now, UpdatedBy: &actor.ID, Version: version, Reason: models.PublicPriceSnapshotReasonRootPublish, SourceRevision: sourceRevision, ContentHash: prepared.Hash, RestoredFromSnapshotID: restoredFrom, PublishedAt: now}
		if operation == "restore" {
			snapshot.Reason = models.PublicPriceSnapshotReasonRestore
		}
		if err = s.fail("snapshot"); err != nil {
			return errUnavailable("price snapshot persistence unavailable")
		}
		if err = tx.Create(&snapshot).Error; err != nil {
			return errUnavailable("price snapshot persistence unavailable")
		}
		includedIDs := make([]int64, len(prepared.Items))
		for i := range prepared.Items {
			includedIDs[i] = prepared.Items[i].ModelConfigID
		}
		marked := tx.Model(&models.PublicModelConfig{}).Where("id IN ? AND status=? AND is_deleted=0", includedIDs, models.PublicModelConfigStatusActive).Update("ever_published", 1)
		if marked.Error != nil {
			return errUnavailable("public model persistence unavailable")
		}
		if err = s.fail("ever_published"); err != nil {
			return errUnavailable("public model persistence unavailable")
		}
		for i := range prepared.Items {
			item := prepared.Items[i]
			item.ID = 0
			item.Guid = s.nextGUID()
			item.SnapshotID = snapshot.ID
			item.CreatedAt = now
			item.CreatedBy = &actor.ID
			item.UpdatedAt = now
			item.UpdatedBy = &actor.ID
			item.IsDeleted = 0
			if item.Guid <= 0 {
				return errUnavailable("price snapshot persistence unavailable")
			}
			if err = s.fail("item"); err != nil {
				return errUnavailable("price snapshot persistence unavailable")
			}
			if err = tx.Create(&item).Error; err != nil {
				return errUnavailable("price snapshot persistence unavailable")
			}
		}
		var contentReleaseID *int64
		if currentContent != nil {
			rebound, bindErr := prepareContentReleaseRebinding(*currentContent, snapshot, prepared.Items)
			if bindErr != nil {
				return bindErr
			}
			contentRelease := models.PublicContentRelease{Guid: s.nextGUID(), CreatedAt: now, CreatedBy: &actor.ID, UpdatedAt: now, UpdatedBy: &actor.ID, DocumentKind: models.PublicContentDocumentSite, Version: currentContent.Version + 1, SourceRevision: currentContent.SourceRevision, Payload: rebound.Payload, ContentHash: rebound.Hash, PublishedAt: now}
			if contentRelease.Guid <= 0 {
				return errUnavailable("content release persistence unavailable")
			}
			if err = s.fail("content_release"); err != nil {
				return errUnavailable("content release persistence unavailable")
			}
			if err = tx.Create(&contentRelease).Error; err != nil {
				return errUnavailable("content release persistence unavailable")
			}
			job := models.PublicRenderJob{AuditFields: models.AuditFields{Guid: s.nextGUID(), CreatedAt: now, CreatedBy: &actor.ID, UpdatedAt: now, UpdatedBy: &actor.ID}, PriceSnapshotID: snapshot.ID, ContentReleaseID: contentRelease.ID, State: models.PublicRenderJobQueued}
			if job.Guid <= 0 {
				return errUnavailable("render job persistence unavailable")
			}
			if err = s.fail("render_job"); err != nil {
				return errUnavailable("render job persistence unavailable")
			}
			if err = tx.Create(&job).Error; err != nil {
				return errUnavailable("render job persistence unavailable")
			}
			contentReleaseID = &contentRelease.ID
		}
		detail := models.JSONMap{"idempotency_key_hash": keyDigest, "idempotency_binding": binding, "payload_hash": prepared.Hash, "snapshot_id": snapshot.ID, "version": snapshot.Version, "operation": operation}
		if err = s.fail("audit"); err != nil {
			return errUnavailable("audit persistence unavailable")
		}
		if err = writePublicModelAudit(tx, s.nextGUID(), now, actor.ID, "public_pricing."+operation, "snapshot", snapshot.Guid, detail); err != nil {
			return errUnavailable("audit persistence unavailable")
		}
		if err = s.fail("pointer"); err != nil {
			return errUnavailable("publication state unavailable")
		}
		updates := map[string]any{"price_snapshot_id": snapshot.ID, "revision": state.Revision + 1, "updated_at": now, "updated_by": actor.ID}
		if contentReleaseID != nil {
			updates["content_release_id"] = *contentReleaseID
		}
		result := tx.Model(&models.PublicPublicationState{}).Where("id = ? AND revision = ?", state.ID, state.Revision).Updates(updates)
		if result.Error != nil || result.RowsAffected != 1 {
			return errConflict("publication state revision conflict")
		}
		if err = s.fail("after_pointer"); err != nil {
			return errUnavailable("publication state unavailable")
		}
		out = projectPublicPriceSnapshot(snapshot)
		return nil
	})
	if err != nil {
		if _, ok := err.(*HTTPError); ok {
			return nil, err
		}
		return nil, errUnavailable("public price publication unavailable")
	}
	return out, nil
}

func findPublicPriceSnapshotReplay(tx *gorm.DB, actor int64, operation, keyDigest, binding string) (*PublicPriceSnapshotRelease, bool, error) {
	var audit models.AuditLog
	err := tx.Where("user_id = ? AND action = ? AND JSON_UNQUOTE(JSON_EXTRACT(detail, '$.idempotency_key_hash')) = ?", actor, "public_pricing."+operation, keyDigest).Order("id DESC").First(&audit).Error
	if err == gorm.ErrRecordNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errUnavailable("idempotency state unavailable")
	}
	if fmt.Sprint(audit.Detail["idempotency_binding"]) != binding {
		return nil, true, errConflict("idempotency conflict")
	}
	id, ok := jsonNumberInt64(audit.Detail["snapshot_id"])
	if !ok {
		return nil, true, errUnavailable("idempotency state unavailable")
	}
	var snapshot models.PublicPriceSnapshot
	if err = tx.First(&snapshot, id).Error; err != nil {
		return nil, true, errUnavailable("idempotency state unavailable")
	}
	return projectPublicPriceSnapshot(snapshot), true, nil
}
func jsonNumberInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), n == float64(int64(n))
	case json.Number:
		i, e := n.Int64()
		return i, e == nil
	case int64:
		return n, true
	default:
		return 0, false
	}
}
func projectPublicPriceSnapshot(v models.PublicPriceSnapshot) *PublicPriceSnapshotRelease {
	return &PublicPriceSnapshotRelease{GUID: strconv.FormatInt(v.Guid, 10), Version: v.Version, Reason: v.Reason.String(), SourceRevision: v.SourceRevision, CreatedAt: releaseTime(v.PublishedAt)}
}
