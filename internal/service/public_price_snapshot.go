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
	CreatedAt      int64  `json:"created_at"`
}

type preparedPublicPriceSnapshot struct {
	Items []models.PublicPriceSnapshotItem
	Hash  string
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
	for _, m := range rows {
		if m.IsDeleted != 0 || m.Status != models.PublicModelConfigStatusActive {
			continue
		}
		if !publiccontent.ValidModelKey(m.ModelKey) || !publiccontent.ValidUpstreamModelID(m.UpstreamModelID) ||
			!validPublicModelText(m.DisplayName, 128) || !validPublicModelText(m.Provider, 128) ||
			!validPublicModelCapabilities([]string(m.Capabilities)) || m.ContextWindow <= 0 ||
			!validPublicPrice(m.InputPriceUSDPerMillionTokens) || !validPublicPrice(m.OutputPriceUSDPerMillionTokens) ||
			m.InputPriceUSDPerMillionTokens == nil || m.OutputPriceUSDPerMillionTokens == nil {
			return nil, errUnprocessable("public price snapshot validation failed")
		}
		items = append(items, models.PublicPriceSnapshotItem{ModelConfigID: m.ID, ModelKey: m.ModelKey, UpstreamModelID: m.UpstreamModelID, DisplayName: m.DisplayName, Provider: m.Provider, Capabilities: append(models.JSONSlice(nil), m.Capabilities...), ContextWindow: m.ContextWindow, InputPriceUSDPerMillionTokens: *m.InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens: *m.OutputPriceUSDPerMillionTokens, UpstreamCheckedAt: m.LastUpstreamCheckAt})
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

func hashPublicPriceSnapshotItems(items []models.PublicPriceSnapshotItem) (string, error) {
	type canonical struct {
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
	values := make([]canonical, len(items))
	for i, v := range items {
		values[i] = canonical{v.ModelKey, v.UpstreamModelID, v.DisplayName, v.Provider, append([]string(nil), v.Capabilities...), v.ContextWindow, v.InputPriceUSDPerMillionTokens, v.OutputPriceUSDPerMillionTokens, v.UpstreamCheckedAt}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ModelKey < values[j].ModelKey })
	b, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func publicPriceIdempotencyBinding(actor int64, operation, key, payload string) string {
	s := sha256.Sum256([]byte(strconv.FormatInt(actor, 10) + "\x00" + operation + "\x00" + key + "\x00" + payload))
	return hex.EncodeToString(s[:])
}
func publicPriceIdempotencyKeyDigest(key string) string {
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])
}

func (s *PublicPriceSnapshotService) Publish(ctx context.Context, in PublicPriceSnapshotRequest) (*PublicPriceSnapshotRelease, error) {
	return s.transact(ctx, in.ActorID, in.ExpectedRevision, in.IdempotencyKey, "publish", 0)
}
func (s *PublicPriceSnapshotService) Restore(ctx context.Context, in PublicPriceSnapshotRestoreRequest) (*PublicPriceSnapshotRelease, error) {
	guid, err := strconv.ParseInt(in.SnapshotGUID, 10, 64)
	if err != nil || guid <= 0 {
		return nil, errBadRequest("invalid price snapshot guid")
	}
	return s.transact(ctx, in.ActorID, in.ExpectedRevision, in.IdempotencyKey, "restore", guid)
}

func (s *PublicPriceSnapshotService) transact(ctx context.Context, actorID, expected int64, key, operation string, restoreGUID int64) (*PublicPriceSnapshotRelease, error) {
	if actorID <= 0 || expected <= 0 || len(key) < 1 || len(key) > 256 || strings.TrimSpace(key) != key {
		return nil, errBadRequest("invalid public price publication request")
	}
	var out *PublicPriceSnapshotRelease
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockPublicModelRoot(tx, actorID)
		if err != nil {
			return err
		}
		var state models.PublicPublicationState
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_key = ? AND is_deleted = 0", publicPublicationStateKey).First(&state).Error; err != nil {
			return errUnavailable("publication state unavailable")
		}
		var prepared *preparedPublicPriceSnapshot
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
			prepared = &preparedPublicPriceSnapshot{Items: items, Hash: source.ContentHash}
			restoredFrom = &source.ID
		}
		if err != nil {
			return err
		}
		payload := operation + ":" + prepared.Hash + ":" + strconv.FormatInt(expected, 10) + ":" + strconv.FormatInt(restoreGUID, 10)
		binding, keyDigest := publicPriceIdempotencyBinding(actor.ID, operation, key, payload), publicPriceIdempotencyKeyDigest(key)
		if replay, found, conflictErr := findPublicPriceSnapshotReplay(tx, actor.ID, operation, keyDigest, binding); found || conflictErr != nil {
			out = replay
			return conflictErr
		}
		if state.Revision != expected {
			return errConflict("publication state revision conflict")
		}
		if state.ContentReleaseID == nil {
			return errUnprocessable("published content release required")
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
		snapshot := models.PublicPriceSnapshot{Guid: guid, CreatedAt: now, CreatedBy: &actor.ID, UpdatedAt: now, UpdatedBy: &actor.ID, Version: version, Reason: models.PublicPriceSnapshotReasonRootPublish, SourceRevision: expected, ContentHash: prepared.Hash, RestoredFromSnapshotID: restoredFrom, PublishedAt: now}
		if operation == "restore" {
			snapshot.Reason = models.PublicPriceSnapshotReasonRestore
		}
		if err = s.fail("snapshot"); err != nil {
			return errUnavailable("price snapshot persistence unavailable")
		}
		if err = tx.Create(&snapshot).Error; err != nil {
			return errUnavailable("price snapshot persistence unavailable")
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
		job := models.PublicRenderJob{AuditFields: models.AuditFields{Guid: s.nextGUID(), CreatedAt: now, CreatedBy: &actor.ID, UpdatedAt: now, UpdatedBy: &actor.ID}, PriceSnapshotID: snapshot.ID, ContentReleaseID: *state.ContentReleaseID, State: models.PublicRenderJobQueued}
		if job.Guid <= 0 {
			return errUnavailable("render job persistence unavailable")
		}
		if err = s.fail("render_job"); err != nil {
			return errUnavailable("render job persistence unavailable")
		}
		if err = tx.Create(&job).Error; err != nil {
			return errUnavailable("render job persistence unavailable")
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
		result := tx.Model(&models.PublicPublicationState{}).Where("id = ? AND revision = ?", state.ID, state.Revision).Updates(map[string]any{"price_snapshot_id": snapshot.ID, "revision": state.Revision + 1, "updated_at": now, "updated_by": actor.ID})
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
	return &PublicPriceSnapshotRelease{GUID: strconv.FormatInt(v.Guid, 10), Version: v.Version, Reason: v.Reason.String(), SourceRevision: v.SourceRevision, CreatedAt: v.PublishedAt}
}
