package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	upstreamMonitorInterval        = 5 * time.Minute
	upstreamMonitorLeaseDuration   = 2 * time.Minute
	upstreamObservationRetention   = 30 * 24 * time.Hour
	upstreamObservationDeleteBatch = 1000
)

type upstreamCatalogSource interface {
	ObserveCatalog(context.Context) (whitelabel.CatalogObservation, error)
}

type UpstreamPriceMonitor struct {
	db          *gorm.DB
	catalog     upstreamCatalogSource
	alerts      *RootAlertService
	now         func() int64
	nextGUID    func() int64
	random      io.Reader
	interval    time.Duration
	ticker      func(time.Duration) (<-chan time.Time, func())
	renewTicker func(time.Duration) (<-chan time.Time, func())
	tick        func(context.Context) error
	fail        func(string) error
}

func NewUpstreamPriceMonitor(db *gorm.DB, catalog upstreamCatalogSource, alerts *RootAlertService) *UpstreamPriceMonitor {
	m := &UpstreamPriceMonitor{db: db, catalog: catalog, alerts: alerts, now: persistence.NowMillis, nextGUID: persistence.NextGUID, random: rand.Reader, interval: upstreamMonitorInterval, fail: func(string) error { return nil }}
	m.ticker = func(d time.Duration) (<-chan time.Time, func()) { t := time.NewTicker(d); return t.C, t.Stop }
	m.renewTicker = m.ticker
	m.tick = m.Tick
	return m
}

func (m *UpstreamPriceMonitor) Run(ctx context.Context) {
	if m == nil || m.tick == nil {
		return
	}
	interval := m.interval
	if interval <= 0 {
		interval = upstreamMonitorInterval
	}
	ch, stop := m.ticker(interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			_ = m.tick(ctx)
		}
	}
}

type monitorComparisonKind int

const (
	monitorComparisonBelow   monitorComparisonKind = 1
	monitorComparisonInvalid monitorComparisonKind = 2
)

type monitorComparison struct {
	Component  string
	Kind       monitorComparisonKind
	Current    string
	Upstream   string
	Reason     string
	ObservedAt int64
}

func planPriceComparisons(modelKey string, currentInput, currentOutput *string, upstream *whitelabel.CatalogObservedModel, observedAt int64) []monitorComparison {
	var out []monitorComparison
	components := []struct {
		name              string
		current, upstream *string
	}{{"input", currentInput, upstream.InputPriceUSDPerMillionTokens}, {"output", currentOutput, upstream.OutputPriceUSDPerMillionTokens}}
	for _, c := range components {
		if c.current == nil {
			out = append(out, monitorComparison{Component: c.name, Kind: monitorComparisonInvalid, Reason: "missing_current_price", ObservedAt: observedAt})
			continue
		}
		cv, cok := new(big.Rat).SetString(*c.current)
		if !cok || cv.Sign() < 0 {
			out = append(out, monitorComparison{Component: c.name, Kind: monitorComparisonInvalid, Reason: "invalid_current_price", ObservedAt: observedAt})
			continue
		}
		if c.upstream == nil {
			out = append(out, monitorComparison{Component: c.name, Kind: monitorComparisonInvalid, Reason: "missing_upstream_price", ObservedAt: observedAt})
			continue
		}
		uv, uok := new(big.Rat).SetString(*c.upstream)
		if !uok || uv.Sign() < 0 {
			out = append(out, monitorComparison{Component: c.name, Kind: monitorComparisonInvalid, Reason: "invalid_upstream_price", ObservedAt: observedAt})
			continue
		}
		if cv.Cmp(uv) < 0 {
			out = append(out, monitorComparison{Component: c.name, Kind: monitorComparisonBelow, Current: *c.current, Upstream: *c.upstream, ObservedAt: observedAt})
		}
	}
	return out
}

func (m *UpstreamPriceMonitor) Tick(ctx context.Context) error {
	if m == nil || m.db == nil || m.catalog == nil || m.alerts == nil {
		return errors.New("upstream monitor unavailable")
	}
	owner, err := m.acquireLease(ctx)
	if err != nil || owner == "" {
		return err
	}
	leaseCtx, stopLease, leaseLost := m.maintainLease(ctx, owner)
	defer func() { stopLease(); m.releaseLease(owner) }()
	observation, err := m.catalog.ObserveCatalog(leaseCtx)
	if err != nil || !observation.Successful {
		_, alertErr := m.alerts.Occur(ctx, RootAlertOccurrence{Type: models.RootAlertTypeCatalogSyncFailure, Identity: "catalog", Payload: models.JSONMap{"error_code": "catalog_fetch_failed", "observed_at": m.now()}})
		if err != nil {
			return err
		}
		return alertErr
	}
	if !observation.Complete || !observation.Fresh {
		return nil
	}
	observedAt := observation.FetchedAt.UnixMilli()
	if observedAt <= 0 {
		observedAt = m.now()
	}
	var safetyRemoved map[string]bool
	err = m.db.WithContext(leaseCtx).Transaction(func(tx *gorm.DB) error {
		if e := m.assertLeaseTx(tx, owner); e != nil {
			return e
		}
		draft, e := lockPublicPriceDraftState(tx)
		if e != nil {
			return e
		}
		published := map[int64]models.PublicPriceSnapshotItem{}
		var state models.PublicPublicationState
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; e != nil {
			return e
		}
		if state.PriceSnapshotID != nil {
			var items []models.PublicPriceSnapshotItem
			if e := tx.Where("snapshot_id=? AND is_deleted=0", *state.PriceSnapshotID).Find(&items).Error; e != nil {
				return e
			}
			for _, item := range items {
				published[item.ModelConfigID] = item
			}
		}
		var configs []models.PublicModelConfig
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("is_deleted=0").Order("id").Find(&configs).Error; e != nil {
			return e
		}
		present := make(map[string]whitelabel.CatalogObservedModel, len(observation.Models))
		for _, item := range observation.Models {
			present[item.NormalizedID] = item
			if e := m.recordObservation(tx, item, observedAt); e != nil {
				return e
			}
		}
		inactivated := map[string]bool{}
		lifecycleChanged := false
		var alerts []RootAlertOccurrence
		for i := range configs {
			cfg := &configs[i]
			if cfg.Status == models.PublicModelConfigStatusInactive && cfg.InactiveReason != nil && *cfg.InactiveReason == "upstream_removed" {
				if _, stillPublished := published[cfg.ID]; stillPublished {
					inactivated[cfg.ModelKey] = true
				}
			}
			if cfg.LastUpstreamCheckAt != nil && *cfg.LastUpstreamCheckAt >= observedAt {
				continue
			}
			item, ok := present[cfg.UpstreamModelID]
			if ok {
				updates := map[string]any{"last_upstream_observed_at": observedAt, "last_upstream_check_at": observedAt, "consecutive_absences": 0, "updated_at": observedAt, "updated_by": nil}
				if e := tx.Model(&models.PublicModelConfig{}).Where("id=? AND is_deleted=0", cfg.ID).Updates(updates).Error; e != nil {
					return e
				}
				if cfg.Status == models.PublicModelConfigStatusInactive && cfg.ConsecutiveAbsences > 0 && cfg.InactiveReason != nil && *cfg.InactiveReason == "upstream_removed" {
					alerts = append(alerts, RootAlertOccurrence{Type: models.RootAlertTypeUpstreamReappearance, ModelConfigID: &cfg.ID, ModelKey: cfg.ModelKey, Identity: "catalog", Payload: models.JSONMap{"model_key": cfg.ModelKey, "observed_at": observedAt}})
					continue
				}
				if cfg.Status == models.PublicModelConfigStatusInactive {
					continue
				}
				if snap, exists := published[cfg.ID]; exists && cfg.Status == models.PublicModelConfigStatusActive {
					for _, c := range planPriceComparisons(cfg.ModelKey, &snap.InputPriceUSDPerMillionTokens, &snap.OutputPriceUSDPerMillionTokens, &item, observedAt) {
						alerts = append(alerts, comparisonAlert(*cfg, c))
					}
				}
				continue
			}
			if cfg.Status != models.PublicModelConfigStatusActive {
				continue
			}
			strikes := cfg.ConsecutiveAbsences + 1
			if strikes < 3 {
				if e := tx.Model(&models.PublicModelConfig{}).Where("id=? AND status=? AND is_deleted=0", cfg.ID, models.PublicModelConfigStatusActive).Updates(map[string]any{"consecutive_absences": strikes, "last_upstream_check_at": observedAt, "updated_at": observedAt, "updated_by": nil}).Error; e != nil {
					return e
				}
				alerts = append(alerts, RootAlertOccurrence{Type: models.RootAlertTypeUpstreamMissing, ModelConfigID: &cfg.ID, ModelKey: cfg.ModelKey, Identity: "catalog", Payload: models.JSONMap{"model_key": cfg.ModelKey, "consecutive_absences": int64(strikes), "observed_at": observedAt}})
				continue
			}
			reason := "upstream_removed"
			res := tx.Model(&models.PublicModelConfig{}).Where("id=? AND status=? AND is_deleted=0", cfg.ID, models.PublicModelConfigStatusActive).Updates(map[string]any{"status": models.PublicModelConfigStatusInactive, "inactive_reason": reason, "consecutive_absences": strikes, "last_upstream_check_at": observedAt, "revision": gorm.Expr("revision+1"), "updated_at": observedAt, "updated_by": nil})
			if res.Error != nil || res.RowsAffected != 1 {
				return errors.New("public model changed")
			}
			inactivated[cfg.ModelKey] = true
			lifecycleChanged = true
			alerts = append(alerts, RootAlertOccurrence{Type: models.RootAlertTypeAutomaticInactivation, ModelConfigID: &cfg.ID, ModelKey: cfg.ModelKey, Identity: "catalog", Payload: models.JSONMap{"model_key": cfg.ModelKey, "consecutive_absences": int64(strikes), "reason_code": reason, "observed_at": observedAt}})
			if e := m.systemAudit(tx, cfg, observedAt, strikes); e != nil {
				return e
			}
		}
		for _, occurrence := range alerts {
			if _, e := m.alerts.OccurInTx(leaseCtx, tx, occurrence); e != nil {
				return e
			}
		}
		if len(inactivated) > 0 {
			if _, e := m.alerts.OccurInTx(leaseCtx, tx, RootAlertOccurrence{Type: models.RootAlertTypeCatalogSyncFailure, Identity: "safety", Payload: models.JSONMap{"error_code": "safety_publication_pending", "observed_at": observedAt}}); e != nil {
				return e
			}
		}
		if lifecycleChanged {
			advanced := tx.Model(&models.PublicPriceDraftState{}).Where("id=? AND revision=?", draft.ID, draft.Revision).Updates(map[string]any{"revision": draft.Revision + 1, "updated_at": observedAt, "updated_by": nil})
			if advanced.Error != nil || advanced.RowsAffected != 1 {
				return errors.New("public price draft changed")
			}
		}
		if e := m.pruneObservations(tx, observedAt); e != nil {
			return e
		}
		if e := m.assertLeaseTx(tx, owner); e != nil {
			return e
		}
		safetyRemoved = inactivated
		return m.fail("before_commit")
	})
	if err != nil {
		return err
	}
	select {
	case e := <-leaseLost:
		if e != nil {
			return e
		}
	default:
	}
	if len(safetyRemoved) > 0 {
		if err = m.publishSafetySnapshot(leaseCtx, owner, safetyRemoved, observedAt); err != nil {
			_, _ = m.alerts.Occur(ctx, RootAlertOccurrence{Type: models.RootAlertTypeCatalogSyncFailure, Identity: "safety", Payload: models.JSONMap{"error_code": "safety_publication_failed", "observed_at": observedAt}})
			return err
		}
		_ = m.alerts.Resolve(ctx, models.RootAlertTypeCatalogSyncFailure, "", "safety")
	}
	return nil
}

func comparisonAlert(cfg models.PublicModelConfig, c monitorComparison) RootAlertOccurrence {
	if c.Kind == monitorComparisonBelow {
		return RootAlertOccurrence{Type: models.RootAlertTypePublishedPriceBelowUpstream, ModelConfigID: &cfg.ID, ModelKey: cfg.ModelKey, Identity: c.Component, Payload: models.JSONMap{"model_key": cfg.ModelKey, "price_component": c.Component, "current_price_usd_per_million_tokens": c.Current, "upstream_price_usd_per_million_tokens": c.Upstream, "observed_at": c.ObservedAt}}
	}
	return RootAlertOccurrence{Type: models.RootAlertTypePriceNotComparable, ModelConfigID: &cfg.ID, ModelKey: cfg.ModelKey, Identity: c.Component, Payload: models.JSONMap{"model_key": cfg.ModelKey, "price_component": c.Component, "reason_code": c.Reason, "observed_at": c.ObservedAt}}
}

func (m *UpstreamPriceMonitor) recordObservation(tx *gorm.DB, item whitelabel.CatalogObservedModel, at int64) error {
	b, _ := json.Marshal(item)
	sum := sha256.Sum256(b)
	row := models.UpstreamModelObservation{AuditFields: models.AuditFields{Guid: m.nextGUID(), CreatedAt: at, UpdatedAt: at}, UpstreamModelID: item.NormalizedID, Provider: item.Provider, InputPriceUSDPerMillionTokens: item.InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens: item.OutputPriceUSDPerMillionTokens, CatalogComplete: 1, CatalogFresh: 1, ObservedAt: at, ResponseSummaryHash: hex.EncodeToString(sum[:])}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func (m *UpstreamPriceMonitor) systemAudit(tx *gorm.DB, cfg *models.PublicModelConfig, at int64, strikes int) error {
	resource := "public-models/" + cfg.ModelKey
	return tx.Create(&models.AuditLog{AuditFields: models.AuditFields{Guid: m.nextGUID(), CreatedAt: at, UpdatedAt: at}, Action: "public_models.upstream_auto_inactivate", Resource: &resource, Detail: models.JSONMap{"model_key": cfg.ModelKey, "consecutive_absences": int64(strikes), "reason_code": "upstream_removed", "actor": "system"}}).Error
}

func (m *UpstreamPriceMonitor) publishSafetySnapshot(ctx context.Context, owner string, removed map[string]bool, now int64) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := m.assertLeaseTx(tx, owner); err != nil {
			return err
		}
		var state models.PublicPublicationState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_key=? AND is_deleted=0", publicPublicationStateKey).First(&state).Error; err != nil {
			return err
		}
		draft, err := lockPublicPriceDraftState(tx)
		if err != nil {
			return err
		}
		var rows []models.PublicModelConfig
		if err = tx.Where("status=? AND is_deleted=0", models.PublicModelConfigStatusActive).Order("model_key").Find(&rows).Error; err != nil {
			return err
		}
		prepared, err := preparePublicPriceSnapshot(rows)
		if err != nil {
			return err
		}
		var content models.PublicContentRelease
		if state.ContentReleaseID == nil {
			return errors.New("content release unavailable")
		}
		if err = tx.First(&content, *state.ContentReleaseID).Error; err != nil {
			return err
		}
		payload := models.JSONMap{}
		for k, v := range content.Payload {
			payload[k] = v
		}
		if refs, ok := payload["model_keys"].([]any); ok {
			filtered := make([]any, 0, len(refs))
			for _, r := range refs {
				if key, ok := r.(string); ok && !removed[key] {
					filtered = append(filtered, key)
				}
			}
			payload["model_keys"] = filtered
		}
		hash, err := hashPublicContentPayload(payload)
		if err != nil {
			return err
		}
		content.Payload = payload
		content.ContentHash = hash
		version := int64(1)
		if state.PriceSnapshotID != nil {
			var current models.PublicPriceSnapshot
			if err = tx.First(&current, *state.PriceSnapshotID).Error; err != nil {
				return err
			}
			version = current.Version + 1
		}
		snapshot := models.PublicPriceSnapshot{Guid: m.nextGUID(), CreatedAt: now, UpdatedAt: now, Version: version, Reason: models.PublicPriceSnapshotReasonUpstreamSafety, SourceRevision: draft.Revision, ContentHash: prepared.Hash, PublishedAt: now}
		if err = tx.Create(&snapshot).Error; err != nil {
			return err
		}
		for i := range prepared.Items {
			item := prepared.Items[i]
			item.Guid = m.nextGUID()
			item.SnapshotID = snapshot.ID
			item.CreatedAt = now
			item.UpdatedAt = now
			if err = tx.Create(&item).Error; err != nil {
				return err
			}
		}
		rebound, err := prepareContentReleaseRebinding(content, snapshot, prepared.Items)
		if err != nil {
			return err
		}
		release := models.PublicContentRelease{Guid: m.nextGUID(), CreatedAt: now, UpdatedAt: now, DocumentKind: models.PublicContentDocumentSite, Version: content.Version + 1, SourceRevision: content.SourceRevision, Payload: rebound.Payload, ContentHash: rebound.Hash, PublishedAt: now}
		if err = tx.Create(&release).Error; err != nil {
			return err
		}
		job := models.PublicRenderJob{AuditFields: models.AuditFields{Guid: m.nextGUID(), CreatedAt: now, UpdatedAt: now}, PriceSnapshotID: snapshot.ID, ContentReleaseID: release.ID, State: models.PublicRenderJobQueued}
		if err = tx.Create(&job).Error; err != nil {
			return err
		}
		if err = m.fail("safety_before_pointer"); err != nil {
			return err
		}
		res := tx.Model(&models.PublicPublicationState{}).Where("id=? AND revision=?", state.ID, state.Revision).Updates(map[string]any{"price_snapshot_id": snapshot.ID, "content_release_id": release.ID, "revision": state.Revision + 1, "updated_at": now, "updated_by": nil})
		if res.Error != nil || res.RowsAffected != 1 {
			return errors.New("publication state changed")
		}
		return m.assertLeaseTx(tx, owner)
	})
}

func (m *UpstreamPriceMonitor) pruneObservations(tx *gorm.DB, now int64) error {
	cutoff := now - upstreamObservationRetention.Milliseconds()
	return tx.Exec("DELETE FROM upstream_model_observations WHERE id IN (SELECT id FROM (SELECT id FROM upstream_model_observations WHERE is_deleted=0 AND observed_at < ? ORDER BY observed_at,id LIMIT ?) old_rows)", cutoff, upstreamObservationDeleteBatch).Error
}

func (m *UpstreamPriceMonitor) assertLeaseTx(tx *gorm.DB, owner string) error {
	var lease models.UpstreamMonitorLease
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("lease_key=? AND owner_token=? AND lease_expires_at > ? AND is_deleted=0", "catalog", owner, m.now()).First(&lease).Error; err != nil {
		return errors.New("upstream monitor lease lost")
	}
	return nil
}

func (m *UpstreamPriceMonitor) maintainLease(parent context.Context, owner string) (context.Context, context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(parent)
	lost := make(chan error, 1)
	ticker := m.renewTicker
	if ticker == nil {
		ticker = m.ticker
	}
	ch, stop := ticker(upstreamMonitorLeaseDuration / 3)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				if err := m.renewLease(ctx, owner); err != nil {
					select {
					case lost <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }, lost
}

func (m *UpstreamPriceMonitor) acquireLease(ctx context.Context) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, e := io.ReadFull(m.random, tokenBytes); e != nil {
		return "", e
	}
	token := hex.EncodeToString(tokenBytes)
	now := m.now()
	expiry := now + upstreamMonitorLeaseDuration.Milliseconds()
	res := m.db.WithContext(ctx).Model(&models.UpstreamMonitorLease{}).Where("lease_key=? AND is_deleted=0 AND (owner_token IS NULL OR lease_expires_at <= ?)", "catalog", now).Updates(map[string]any{"owner_token": token, "lease_expires_at": expiry, "revision": gorm.Expr("revision+1"), "updated_at": now, "updated_by": nil})
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected != 1 {
		return "", nil
	}
	return token, nil
}
func (m *UpstreamPriceMonitor) renewLease(ctx context.Context, owner string) error {
	now := m.now()
	res := m.db.WithContext(ctx).Model(&models.UpstreamMonitorLease{}).Where("lease_key=? AND owner_token=? AND lease_expires_at >= ? AND is_deleted=0", "catalog", owner, now).Updates(map[string]any{"lease_expires_at": now + upstreamMonitorLeaseDuration.Milliseconds(), "revision": gorm.Expr("revision+1"), "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("upstream monitor lease lost")
	}
	return nil
}
func (m *UpstreamPriceMonitor) releaseLease(owner string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	now := m.now()
	_ = m.db.WithContext(ctx).Model(&models.UpstreamMonitorLease{}).Where("lease_key=? AND owner_token=? AND is_deleted=0", "catalog", owner).Updates(map[string]any{"owner_token": nil, "lease_expires_at": 0, "revision": gorm.Expr("revision+1"), "updated_at": now}).Error
}
