package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/publiccontent"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const rootAlertPayloadLimit = 8192

type RootAlertChannel string

const RootAlertChannelInApp RootAlertChannel = "in_app"

// RootAlertDelivery preserves the delivery boundary for a later email implementation.
// TODO: add email recipient policy, delivery, retry and bounce handling in its own approved task.
type RootAlertDelivery interface{ Channel() RootAlertChannel }
type InAppRootAlertDelivery struct{}

func (InAppRootAlertDelivery) Channel() RootAlertChannel { return RootAlertChannelInApp }

type RootAlertOccurrence struct {
	Type          models.RootAlertType
	ModelConfigID *int64
	ModelKey      string
	Identity      string
	Payload       models.JSONMap
}
type RootAlertView struct {
	GUID         string `json:"guid"`
	Type         string `json:"type"`
	State        string `json:"state"`
	Read         bool   `json:"read"`
	Acknowledged bool   `json:"acknowledged"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}
type RootAlertListQuery struct {
	State    string `json:"state,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"page_size,omitempty"`
}
type RootAlertList struct {
	Items    []RootAlertView `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int64           `json:"total"`
}
type RootAlertUnreadCount struct {
	UnreadCount int64 `json:"unread_count"`
}

type RootAlertService struct {
	db       *gorm.DB
	now      func() int64
	nextGUID func() int64
	fail     func(string) error
	delivery RootAlertDelivery
}

func NewRootAlertService(db *gorm.DB) *RootAlertService {
	return &RootAlertService{db: db, now: persistence.NowMillis, nextGUID: persistence.NextGUID, fail: func(string) error { return nil }, delivery: InAppRootAlertDelivery{}}
}

func rootAlertFingerprint(t models.RootAlertType, modelKey, identity string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("v1\x00%d\x00%s\x00%s", int(t), strings.TrimSpace(modelKey), strings.TrimSpace(identity))))
	return hex.EncodeToString(h[:])
}

type rootAlertPayloadField int

const (
	rootAlertCode rootAlertPayloadField = iota + 1
	rootAlertDecimal
	rootAlertPositiveInt
	rootAlertTimestamp
	rootAlertModelKey
	rootAlertPriceComponent
	rootAlertInactivationReason
	rootAlertNotComparableReason
)

type rootAlertPayloadSchema struct {
	fields      map[string]rootAlertPayloadField
	required    []string
	modelScoped bool
}

var rootAlertPayloadSchemas = map[models.RootAlertType]rootAlertPayloadSchema{
	models.RootAlertTypePublishedPriceBelowUpstream: {fields: map[string]rootAlertPayloadField{"model_key": rootAlertModelKey, "price_component": rootAlertPriceComponent, "current_price_usd_per_million_tokens": rootAlertDecimal, "upstream_price_usd_per_million_tokens": rootAlertDecimal, "observed_at": rootAlertTimestamp}, required: []string{"model_key", "price_component", "current_price_usd_per_million_tokens", "upstream_price_usd_per_million_tokens", "observed_at"}, modelScoped: true},
	models.RootAlertTypeUpstreamMissing:             {fields: map[string]rootAlertPayloadField{"model_key": rootAlertModelKey, "consecutive_absences": rootAlertPositiveInt, "observed_at": rootAlertTimestamp}, required: []string{"model_key", "consecutive_absences", "observed_at"}, modelScoped: true},
	models.RootAlertTypeAutomaticInactivation:       {fields: map[string]rootAlertPayloadField{"model_key": rootAlertModelKey, "consecutive_absences": rootAlertPositiveInt, "reason_code": rootAlertInactivationReason, "observed_at": rootAlertTimestamp}, required: []string{"model_key", "consecutive_absences", "reason_code", "observed_at"}, modelScoped: true},
	models.RootAlertTypeUpstreamReappearance:        {fields: map[string]rootAlertPayloadField{"model_key": rootAlertModelKey, "observed_at": rootAlertTimestamp}, required: []string{"model_key", "observed_at"}, modelScoped: true},
	models.RootAlertTypeCatalogSyncFailure:          {fields: map[string]rootAlertPayloadField{"error_code": rootAlertCode, "observed_at": rootAlertTimestamp}, required: []string{"error_code", "observed_at"}},
	models.RootAlertTypePriceNotComparable:          {fields: map[string]rootAlertPayloadField{"model_key": rootAlertModelKey, "price_component": rootAlertPriceComponent, "reason_code": rootAlertNotComparableReason, "observed_at": rootAlertTimestamp}, required: []string{"model_key", "price_component", "reason_code", "observed_at"}, modelScoped: true},
	models.RootAlertTypeRendererFailure:             {fields: map[string]rootAlertPayloadField{"release_version": rootAlertPositiveInt, "render_job_guid": rootAlertCode, "error_code": rootAlertCode, "observed_at": rootAlertTimestamp}, required: []string{"release_version", "render_job_guid", "error_code", "observed_at"}},
}

func rootAlertRequiresModelConfig(typ models.RootAlertType) bool {
	schema, ok := rootAlertPayloadSchemas[typ]
	return ok && schema.modelScoped
}

func validRootAlertConfigStatus(typ models.RootAlertType, status models.PublicModelConfigStatus) bool {
	switch typ {
	case models.RootAlertTypeAutomaticInactivation, models.RootAlertTypeUpstreamReappearance:
		return status == models.PublicModelConfigStatusInactive
	case models.RootAlertTypePublishedPriceBelowUpstream, models.RootAlertTypeUpstreamMissing, models.RootAlertTypePriceNotComparable:
		return status == models.PublicModelConfigStatusActive
	default:
		return false
	}
}

func projectRootAlertPayload(typ models.RootAlertType, modelKey string, in models.JSONMap) (models.JSONMap, error) {
	schema, ok := rootAlertPayloadSchemas[typ]
	if !ok {
		return nil, errBadRequest("invalid root alert payload")
	}
	if schema.modelScoped {
		if !publiccontent.ValidModelKey(modelKey) {
			return nil, errBadRequest("invalid root alert payload")
		}
	} else if modelKey != "" {
		return nil, errBadRequest("invalid root alert payload")
	}
	for key := range in {
		if _, ok := schema.fields[key]; !ok {
			return nil, errBadRequest("invalid root alert payload")
		}
	}
	out := models.JSONMap{}
	for _, key := range schema.required {
		raw, exists := in[key]
		if !exists {
			return nil, errBadRequest("invalid root alert payload")
		}
		value, valid := normalizeRootAlertField(schema.fields[key], raw)
		if !valid {
			return nil, errBadRequest("invalid root alert payload")
		}
		out[key] = value
	}
	if schema.modelScoped && out["model_key"] != modelKey {
		return nil, errBadRequest("invalid root alert payload")
	}
	b, _ := json.Marshal(out)
	if len(b) > rootAlertPayloadLimit {
		return nil, errBadRequest("invalid root alert payload")
	}
	return out, nil
}
func normalizeRootAlertField(kind rootAlertPayloadField, raw any) (any, bool) {
	switch kind {
	case rootAlertCode, rootAlertDecimal, rootAlertModelKey, rootAlertPriceComponent, rootAlertInactivationReason, rootAlertNotComparableReason:
		v, ok := raw.(string)
		if !ok || !validRootAlertScalar(v) {
			return nil, false
		}
		switch kind {
		case rootAlertModelKey:
			if !publiccontent.ValidModelKey(v) {
				return nil, false
			}
		case rootAlertPriceComponent:
			if v != "input" && v != "output" {
				return nil, false
			}
		case rootAlertInactivationReason:
			if v != "upstream_removed" {
				return nil, false
			}
		case rootAlertNotComparableReason:
			if v != "missing_current_price" && v != "missing_upstream_price" && v != "invalid_current_price" && v != "invalid_upstream_price" {
				return nil, false
			}
		case rootAlertCode:
			if len(v) > 128 {
				return nil, false
			}
			for _, r := range v {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '/' || r == '.') {
					return nil, false
				}
			}
		case rootAlertDecimal:
			if !publicModelDecimal.MatchString(v) {
				return nil, false
			}
		}
		return v, true
	case rootAlertPositiveInt, rootAlertTimestamp:
		v, ok := rootAlertInt64(raw)
		if !ok || v <= 0 {
			return nil, false
		}
		if kind == rootAlertPositiveInt && v > 1_000_000_000 {
			return nil, false
		}
		return v, true
	}
	return nil, false
}
func rootAlertInt64(raw any) (int64, bool) {
	switch v := raw.(type) {
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		n, e := strconv.ParseInt(string(v), 10, 64)
		return n, e == nil
	default:
		return 0, false
	}
}
func validRootAlertScalar(v string) bool {
	if v == "" || !utf8.ValidString(v) || strings.TrimSpace(v) != v {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	if looksLikeRootAlertCredential(v) {
		return false
	}
	return true
}

func looksLikeRootAlertCredential(v string) bool {
	lower := strings.ToLower(v)
	for _, prefix := range []string{"akia", "ghp_", "github_pat_", "sk_live_", "sk_test_", "sk-"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if len(v) >= 32 {
		encoded := true
		for _, r := range v {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '=' || r == '_' || r == '-' || r == '.') {
				encoded = false
				break
			}
		}
		if encoded {
			return true
		}
	}
	return false
}

func (s *RootAlertService) Occur(ctx context.Context, in RootAlertOccurrence) (*RootAlertView, error) {
	if s == nil || s.db == nil || in.Type.String() == "unknown" || (rootAlertRequiresModelConfig(in.Type) && (in.ModelConfigID == nil || *in.ModelConfigID <= 0)) || (!rootAlertRequiresModelConfig(in.Type) && in.ModelConfigID != nil) || (in.ModelKey != "" && !publiccontent.ValidModelKey(in.ModelKey)) || !validRootAlertIdentity(in.Identity) {
		return nil, errBadRequest("invalid root alert occurrence")
	}
	var row models.RootAlert
	err := runRootAlertTransaction(func() error {
		row = models.RootAlert{}
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			effectiveModelKey := in.ModelKey
			modelConfigGUID := ""
			payloadInput := models.JSONMap{}
			for k, v := range in.Payload {
				payloadInput[k] = v
			}
			if in.ModelConfigID != nil {
				var config models.PublicModelConfig
				if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "model_key", "status", "is_deleted").Where("id=? AND is_deleted=0", *in.ModelConfigID).First(&config).Error; e != nil {
					return mapRootAlertConfigLoadError(e)
				}
				if !validRootAlertConfigStatus(in.Type, config.Status) {
					return errBadRequest("invalid root alert model config")
				}
				if effectiveModelKey != "" && effectiveModelKey != config.ModelKey {
					return errBadRequest("invalid root alert identity")
				}
				effectiveModelKey = config.ModelKey
				modelConfigGUID = fmt.Sprint(config.Guid)
			}
			payload, payloadErr := projectRootAlertPayload(in.Type, effectiveModelKey, payloadInput)
			if payloadErr != nil {
				return payloadErr
			}
			if modelConfigGUID != "" {
				payload["model_config_guid"] = modelConfigGUID
			}
			fp := rootAlertFingerprint(in.Type, effectiveModelKey, in.Identity)
			e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("fingerprint=? AND is_deleted=0", fp).First(&row).Error
			now := s.now()
			if errors.Is(e, gorm.ErrRecordNotFound) {
				row = models.RootAlert{AuditFields: models.AuditFields{Guid: s.nextGUID(), CreatedAt: now, UpdatedAt: now}, ModelConfigID: in.ModelConfigID, AlertType: in.Type, State: models.RootAlertStateActive, Fingerprint: fp, Payload: payload, OccurrenceCount: 1, FirstObservedAt: now, LastObservedAt: now}
				if effectiveModelKey != "" {
					key := effectiveModelKey
					row.ModelKey = &key
				}
				if e = tx.Create(&row).Error; e != nil {
					return e
				}
			} else if e != nil {
				return e
			} else {
				reopened := row.State == models.RootAlertStateResolved
				updates := map[string]any{"state": models.RootAlertStateActive, "payload": payload, "occurrence_count": gorm.Expr("occurrence_count + 1"), "last_observed_at": now, "resolved_at": nil, "updated_at": now, "updated_by": nil}
				if e = tx.Model(&models.RootAlert{}).Where("id=? AND is_deleted=0", row.ID).Updates(updates).Error; e != nil {
					return e
				}
				row.State = models.RootAlertStateActive
				row.UpdatedAt = now
				if reopened {
					if e = tx.Model(&models.RootAlertReceipt{}).Where("alert_id=? AND is_deleted=0", row.ID).Updates(map[string]any{"read_at": nil, "acknowledged_at": nil, "updated_at": now, "updated_by": nil}).Error; e != nil {
						return e
					}
				}
			}
			if e = s.fail("audit"); e != nil {
				return e
			}
			return writeRootAlertAudit(tx, s.nextGUID(), now, nil, "root_alert.occurred", row.Guid, models.JSONMap{"alert_type": in.Type.String(), "fingerprint": fp})
		})
	})
	if err != nil {
		return nil, mapRootAlertError(err)
	}
	return &RootAlertView{GUID: fmt.Sprint(row.Guid), Type: row.AlertType.String(), State: models.RootAlertStateActive.String(), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func mapRootAlertConfigLoadError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errBadRequest("invalid root alert model config")
	}
	return err
}

func runRootAlertTransaction(run func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = run()
		if err == nil {
			return nil
		}
		var mysqlErr *drivermysql.MySQLError
		if !(errors.As(err, &mysqlErr) && (mysqlErr.Number == 1062 || mysqlErr.Number == 1205 || mysqlErr.Number == 1213)) {
			return err
		}
	}
	return err
}

func (s *RootAlertService) Get(ctx context.Context, rootID int64, guid string) (*RootAlertView, error) {
	id, err := parseStrictGUID(guid)
	if err != nil {
		return nil, errBadRequest("invalid root alert guid")
	}
	var row rootAlertJoined
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPublicModelRoot(tx, rootID); e != nil {
			return e
		}
		return tx.Model(&models.RootAlert{}).
			Select("root_alerts.*, r.read_at AS receipt_read_at, r.acknowledged_at AS receipt_acknowledged_at").
			Joins("LEFT JOIN root_alert_receipts r ON r.alert_id=root_alerts.id AND r.root_user_id=? AND r.is_deleted=0", rootID).
			Where("root_alerts.guid=? AND root_alerts.is_deleted=0", id).Scan(&row).Error
	})
	if err != nil {
		return nil, mapRootAlertError(err)
	}
	if row.ID == 0 {
		return nil, errNotFound("root alert not found")
	}
	view := projectRootAlert(row)
	return &view, nil
}

func (s *RootAlertService) Resolve(ctx context.Context, typ models.RootAlertType, modelKey, identity string) error {
	if typ.String() == "unknown" || (modelKey != "" && !publiccontent.ValidModelKey(modelKey)) || !validRootAlertIdentity(identity) {
		return errBadRequest("invalid root alert resolution")
	}
	fp := rootAlertFingerprint(typ, modelKey, identity)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var a models.RootAlert
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("fingerprint=? AND is_deleted=0", fp).First(&a).Error; e != nil {
			return e
		}
		if a.State == models.RootAlertStateResolved {
			return nil
		}
		now := s.now()
		if e := tx.Model(&a).Updates(map[string]any{"state": models.RootAlertStateResolved, "resolved_at": now, "updated_at": now, "updated_by": nil}).Error; e != nil {
			return e
		}
		if e := s.fail("resolve.audit"); e != nil {
			return e
		}
		return writeRootAlertAudit(tx, s.nextGUID(), now, nil, "root_alert.resolved", a.Guid, models.JSONMap{"alert_type": typ.String(), "fingerprint": fp})
	})
	return mapRootAlertError(err)
}

func validRootAlertIdentity(value string) bool {
	return len(value) <= 128 && validRootAlertScalar(value)
}

type rootAlertJoined struct {
	models.RootAlert
	ReadAt         *int64 `gorm:"column:receipt_read_at"`
	AcknowledgedAt *int64 `gorm:"column:receipt_acknowledged_at"`
}

func (s *RootAlertService) List(ctx context.Context, rootID int64, q RootAlertListQuery) (*RootAlertList, error) {
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 20
	}
	if q.Page < 1 || (q.PageSize != 20 && q.PageSize != 50 && q.PageSize != 100) {
		return nil, errBadRequest("invalid root alert pagination")
	}
	var state models.RootAlertState
	if q.State != "" {
		var ok bool
		state, ok = models.ParseRootAlertState(q.State)
		if !ok {
			return nil, errBadRequest("invalid root alert state")
		}
	}
	out := &RootAlertList{Items: []RootAlertView{}, Page: q.Page, PageSize: q.PageSize}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPublicModelRoot(tx, rootID); e != nil {
			return e
		}
		base := tx.Model(&models.RootAlert{}).Where("root_alerts.is_deleted=0")
		if state != 0 {
			base = base.Where("root_alerts.state=?", state)
		}
		if e := base.Count(&out.Total).Error; e != nil {
			return e
		}
		var rows []rootAlertJoined
		e := base.Select("root_alerts.*, r.read_at AS receipt_read_at, r.acknowledged_at AS receipt_acknowledged_at").Joins("LEFT JOIN root_alert_receipts r ON r.alert_id=root_alerts.id AND r.root_user_id=? AND r.is_deleted=0", rootID).Order("root_alerts.updated_at DESC, root_alerts.id DESC").Limit(q.PageSize).Offset((q.Page - 1) * q.PageSize).Scan(&rows).Error
		if e != nil {
			return e
		}
		for _, r := range rows {
			out.Items = append(out.Items, projectRootAlert(r))
		}
		return nil
	})
	if err != nil {
		return nil, mapRootAlertError(err)
	}
	return out, nil
}
func (s *RootAlertService) UnreadCount(ctx context.Context, rootID int64) (*RootAlertUnreadCount, error) {
	var out RootAlertUnreadCount
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPublicModelRoot(tx, rootID); e != nil {
			return e
		}
		return tx.Table("root_alerts a").Joins("LEFT JOIN root_alert_receipts r ON r.alert_id=a.id AND r.root_user_id=? AND r.is_deleted=0", rootID).Where("a.state=? AND a.is_deleted=0 AND r.read_at IS NULL", models.RootAlertStateActive).Count(&out.UnreadCount).Error
	})
	if err != nil {
		return nil, mapRootAlertError(err)
	}
	return &out, nil
}
func (s *RootAlertService) MarkRead(ctx context.Context, rootID int64, guid string) (*RootAlertView, error) {
	return s.mutateReceipt(ctx, rootID, guid, false)
}
func (s *RootAlertService) Acknowledge(ctx context.Context, rootID int64, guid string) (*RootAlertView, error) {
	return s.mutateReceipt(ctx, rootID, guid, true)
}
func (s *RootAlertService) mutateReceipt(ctx context.Context, rootID int64, guid string, ack bool) (*RootAlertView, error) {
	id, e := parseStrictGUID(guid)
	if e != nil {
		return nil, errBadRequest("invalid root alert guid")
	}
	var joined rootAlertJoined
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, e := lockPublicModelRoot(tx, rootID)
		if e != nil {
			return e
		}
		var a models.RootAlert
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guid=? AND is_deleted=0", id).First(&a).Error; e != nil {
			return e
		}
		var r models.RootAlertReceipt
		e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("alert_id=? AND root_user_id=? AND is_deleted=0", a.ID, root.ID).First(&r).Error
		now := s.now()
		if errors.Is(e, gorm.ErrRecordNotFound) {
			r = models.RootAlertReceipt{AuditFields: models.AuditFields{Guid: s.nextGUID(), CreatedAt: now, CreatedBy: &root.ID, UpdatedAt: now, UpdatedBy: &root.ID}, AlertID: a.ID, RootUserID: root.ID}
			if ack {
				r.AcknowledgedAt = &now
				r.ReadAt = &now
			} else {
				r.ReadAt = &now
			}
			if e = tx.Create(&r).Error; e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else {
			updates := map[string]any{"updated_at": now, "updated_by": root.ID}
			if ack {
				updates["acknowledged_at"] = now
				updates["read_at"] = now
			} else {
				updates["read_at"] = now
			}
			if e = tx.Model(&r).Updates(updates).Error; e != nil {
				return e
			}
			r.ReadAt = &now
			if ack {
				r.AcknowledgedAt = &now
			}
		}
		action := "root_alert.read"
		failurePoint := "receipt.read.audit"
		if ack {
			action = "root_alert.acknowledged"
			failurePoint = "receipt.acknowledge.audit"
		}
		if e = s.fail(failurePoint); e != nil {
			return e
		}
		if e = writeRootAlertAudit(tx, s.nextGUID(), now, &root.ID, action, a.Guid, models.JSONMap{"alert_guid": fmt.Sprint(a.Guid)}); e != nil {
			return e
		}
		joined = rootAlertJoined{RootAlert: a, ReadAt: r.ReadAt, AcknowledgedAt: r.AcknowledgedAt}
		return nil
	})
	if err != nil {
		return nil, mapRootAlertError(err)
	}
	v := projectRootAlert(joined)
	return &v, nil
}
func projectRootAlert(r rootAlertJoined) RootAlertView {
	return RootAlertView{GUID: fmt.Sprint(r.Guid), Type: r.AlertType.String(), State: r.State.String(), Read: r.ReadAt != nil, Acknowledged: r.AcknowledgedAt != nil, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
func writeRootAlertAudit(tx *gorm.DB, guid, now int64, actor *int64, action string, alertGUID int64, detail models.JSONMap) error {
	if guid <= 0 {
		return errUnavailable("root alert audit unavailable")
	}
	resource := "root-alerts/" + fmt.Sprint(alertGUID)
	return tx.Create(&models.AuditLog{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: actor, UpdatedAt: now, UpdatedBy: actor}, UserID: actor, Action: action, Resource: &resource, Detail: projectRootAlertAuditDetail(detail)}).Error
}
func projectRootAlertAuditDetail(in models.JSONMap) models.JSONMap {
	out := models.JSONMap{}
	if v, ok := in["alert_type"].(string); ok {
		if _, valid := models.ParseRootAlertType(v); valid {
			out["alert_type"] = v
		}
	}
	if v, ok := in["fingerprint"].(string); ok && len(v) == 64 {
		if _, e := hex.DecodeString(v); e == nil {
			out["fingerprint"] = v
		}
	}
	if v, ok := in["alert_guid"].(string); ok {
		if _, e := parseStrictGUID(v); e == nil {
			out["alert_guid"] = v
		}
	}
	return out
}
func mapRootAlertError(e error) error {
	if e == nil {
		return nil
	}
	if _, ok := e.(*HTTPError); ok {
		return e
	}
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return errNotFound("root alert not found")
	}
	return errUnavailable("root alert unavailable")
}

func parseStrictGUID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
		return 0, errors.New("invalid guid")
	}
	return id, nil
}
