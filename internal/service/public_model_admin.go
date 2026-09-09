package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/publiccontent"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var publicModelDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,8})?$`)
var publicModelSensitiveReason = regexp.MustCompile(`(?i)(api[ _-]?key|password|bearer|token|authorization|secret|credential|raw[ _-]?payload)`)
var publicModelCapability = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Ten minutes is two monitor intervals and permits one delayed five-minute tick.
const publicModelObservationFreshnessMillis int64 = 10 * 60 * 1000
const publicModelMaxCapabilities = 32

type PublicModelAdmin struct {
	GUID                           string   `json:"guid"`
	ModelKey                       string   `json:"model_key"`
	UpstreamModelID                string   `json:"upstream_model_id"`
	DisplayName                    string   `json:"display_name"`
	Provider                       string   `json:"provider"`
	Capabilities                   []string `json:"capabilities"`
	ContextWindow                  int64    `json:"context_window"`
	InputPriceUSDPerMillionTokens  *string  `json:"input_price_usd_per_million_tokens"`
	OutputPriceUSDPerMillionTokens *string  `json:"output_price_usd_per_million_tokens"`
	Status                         string   `json:"status"`
	Revision                       int64    `json:"revision"`
	LastUpstreamCheckAt            *int64   `json:"last_upstream_check_at"`
}
type CreatePublicModelRequest struct {
	UpstreamModelID                string   `json:"upstream_model_id"`
	ModelKey                       string   `json:"model_key"`
	DisplayName                    string   `json:"display_name"`
	Provider                       string   `json:"provider"`
	Capabilities                   []string `json:"capabilities"`
	ContextWindow                  int64    `json:"context_window"`
	InputPriceUSDPerMillionTokens  *string  `json:"input_price_usd_per_million_tokens"`
	OutputPriceUSDPerMillionTokens *string  `json:"output_price_usd_per_million_tokens"`
}
type UpdatePublicModelRequest struct {
	ExpectedRevision               int64                  `json:"expected_revision"`
	DisplayName                    *string                `json:"display_name,omitempty"`
	Provider                       *string                `json:"provider,omitempty"`
	Capabilities                   *[]string              `json:"capabilities,omitempty"`
	ContextWindow                  *int64                 `json:"context_window,omitempty"`
	InputPriceUSDPerMillionTokens  OptionalNullableString `json:"input_price_usd_per_million_tokens,omitempty"`
	OutputPriceUSDPerMillionTokens OptionalNullableString `json:"output_price_usd_per_million_tokens,omitempty"`
}

func (r UpdatePublicModelRequest) MarshalJSON() ([]byte, error) {
	m := map[string]any{"expected_revision": r.ExpectedRevision}
	if r.DisplayName != nil {
		m["display_name"] = *r.DisplayName
	}
	if r.Provider != nil {
		m["provider"] = *r.Provider
	}
	if r.Capabilities != nil {
		m["capabilities"] = *r.Capabilities
	}
	if r.ContextWindow != nil {
		m["context_window"] = *r.ContextWindow
	}
	if r.InputPriceUSDPerMillionTokens.Set {
		m["input_price_usd_per_million_tokens"] = r.InputPriceUSDPerMillionTokens.Value
	}
	if r.OutputPriceUSDPerMillionTokens.Set {
		m["output_price_usd_per_million_tokens"] = r.OutputPriceUSDPerMillionTokens.Value
	}
	return json.Marshal(m)
}

type OptionalNullableString struct {
	Set   bool
	Value *string
}

func (o *OptionalNullableString) UnmarshalJSON(raw []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		o.Value = nil
		return nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}
func (o OptionalNullableString) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*o.Value)
}

type DeactivationRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}
type RevisionRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}
type DeletePublicModelRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}
type AdminModelListRequest struct {
	Search, Status, UpstreamState string
	Page, PageSize                int
}
type AdminModelListResponse struct {
	Items    []PublicModelAdmin `json:"items"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
	Total    int64              `json:"total"`
}

type PublicModelAdminService struct {
	db       *gorm.DB
	now      func() int64
	nextGUID func() int64
}

func NewPublicModelAdminService(db *gorm.DB) *PublicModelAdminService {
	return &PublicModelAdminService{db: db, now: persistence.NowMillis, nextGUID: persistence.NextGUID}
}

func validatePublicModelCreate(in CreatePublicModelRequest) error {
	if !publiccontent.ValidUpstreamModelID(in.UpstreamModelID) || !publiccontent.ValidModelKey(in.ModelKey) || !validPublicModelText(in.DisplayName, 128) || !validPublicModelText(in.Provider, 128) || !validPublicModelCapabilities(in.Capabilities) || in.ContextWindow <= 0 {
		return errBadRequest("invalid public model request")
	}
	if !validPublicPrice(in.InputPriceUSDPerMillionTokens) || !validPublicPrice(in.OutputPriceUSDPerMillionTokens) {
		return errBadRequest("invalid public model price")
	}
	return nil
}
func validPublicModelText(v string, max int) bool {
	return v == strings.TrimSpace(v) && v != "" && utf8.ValidString(v) && utf8.RuneCountInString(v) <= max && strings.IndexFunc(v, func(r rune) bool { return r < ' ' || r == 127 }) < 0
}
func validPublicModelCapabilities(values []string) bool {
	if values == nil || len(values) > publicModelMaxCapabilities {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if !publicModelCapability.MatchString(v) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func publicModelObservationIsCurrent(now, observed int64) bool {
	return observed > 0 && observed <= now && now-observed <= publicModelObservationFreshnessMillis
}
func mapPublicModelWriteError(err error) error {
	var mysqlErr *drivermysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return errConflict("public model identity is permanently reserved")
	}
	return err
}
func validPublicPrice(v *string) bool { return v == nil || publicModelDecimal.MatchString(*v) }
func publicModelAuditDetail(key string, revision int64, reason string) models.JSONMap {
	return models.JSONMap{"model_key": key, "revision": revision, "reason": sanitizePublicModelAuditReason(reason)}
}
func sanitizePublicModelAuditReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if publicModelSensitiveReason.MatchString(reason) {
		return "[redacted]"
	}
	reason = strings.Map(func(r rune) rune {
		if r < ' ' || r == 127 {
			return -1
		}
		return r
	}, reason)
	rr := []rune(reason)
	if len(rr) > 128 {
		rr = rr[:128]
	}
	return string(rr)
}

func (s *PublicModelAdminService) Create(ctx context.Context, actorID int64, in CreatePublicModelRequest) (*PublicModelAdmin, error) {
	if err := validatePublicModelCreate(in); err != nil {
		return nil, err
	}
	var out PublicModelAdmin
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockPublicModelRoot(tx, actorID)
		if err != nil {
			return err
		}
		draft, err := lockPublicPriceDraftState(tx)
		if err != nil {
			return err
		}
		var reserved int64
		if err = tx.Model(&models.PublicModelConfig{}).Where("model_key = ? OR upstream_model_id = ?", in.ModelKey, in.UpstreamModelID).Count(&reserved).Error; err != nil {
			return err
		}
		if reserved != 0 {
			return errConflict("public model identity is permanently reserved")
		}
		var obs models.UpstreamModelObservation
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("upstream_model_id = ? AND catalog_complete = 1 AND catalog_fresh = 1 AND is_deleted = 0", in.UpstreamModelID).Order("observed_at DESC, id DESC").First(&obs).Error
		if err == gorm.ErrRecordNotFound {
			return errConflict("fresh accepted upstream observation required")
		}
		if err != nil {
			return err
		}
		now := s.now()
		if !publicModelObservationIsCurrent(now, obs.ObservedAt) {
			return errConflict("fresh accepted upstream observation required")
		}
		guid := s.nextGUID()
		if now <= 0 || guid <= 0 {
			return errUnavailable("public model persistence unavailable")
		}
		m := models.PublicModelConfig{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actor.ID, UpdatedAt: now, UpdatedBy: &actor.ID}, ModelKey: in.ModelKey, UpstreamModelID: in.UpstreamModelID, DisplayName: strings.TrimSpace(in.DisplayName), Provider: strings.TrimSpace(in.Provider), Capabilities: models.JSONSlice(in.Capabilities), ContextWindow: in.ContextWindow, InputPriceUSDPerMillionTokens: in.InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens: in.OutputPriceUSDPerMillionTokens, Status: models.PublicModelConfigStatusDraft, LastUpstreamObservedAt: &obs.ObservedAt, LastUpstreamCheckAt: &obs.ObservedAt, Revision: 1}
		if err = tx.Create(&m).Error; err != nil {
			return mapPublicModelWriteError(err)
		}
		if err = writePublicModelAudit(tx, s.nextGUID(), now, actor.ID, "public_models.create", m.ModelKey, m.Guid, publicModelAuditDetail(m.ModelKey, m.Revision, "")); err != nil {
			return err
		}
		if err = advancePublicPriceDraftState(tx, draft, actor.ID, now); err != nil {
			return err
		}
		out = projectPublicModel(m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *PublicModelAdminService) List(ctx context.Context, q AdminModelListRequest) (*AdminModelListResponse, error) {
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 20
	}
	if q.Page < 1 || (q.PageSize != 20 && q.PageSize != 50 && q.PageSize != 100) {
		return nil, errBadRequest("invalid pagination")
	}
	db := s.db.WithContext(ctx).Model(&models.PublicModelConfig{}).Where("public_model_configs.is_deleted = 0")
	if v := strings.TrimSpace(q.Search); v != "" {
		like := "%" + strings.ReplaceAll(strings.ReplaceAll(v, "%", "\\%"), "_", "\\_") + "%"
		db = db.Where("(model_key LIKE ? ESCAPE '\\\\' OR upstream_model_id LIKE ? ESCAPE '\\\\' OR display_name LIKE ? ESCAPE '\\\\' OR provider LIKE ? ESCAPE '\\\\')", like, like, like, like)
	}
	if q.Status != "" {
		st, ok := models.ParsePublicModelConfigStatus(q.Status)
		if !ok {
			return nil, errBadRequest("invalid status")
		}
		db = db.Where("status = ?", st)
	}
	if q.UpstreamState != "" {
		if q.UpstreamState != "present" && q.UpstreamState != "missing" {
			return nil, errBadRequest("invalid upstream_state")
		}
		op := "= 0"
		if q.UpstreamState == "missing" {
			op = "> 0"
		}
		db = db.Where("consecutive_absences " + op)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, err
	}
	var rows []models.PublicModelConfig
	if err := db.Order("updated_at DESC, id DESC").Limit(q.PageSize).Offset((q.Page - 1) * q.PageSize).Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]PublicModelAdmin, len(rows))
	for i := range rows {
		items[i] = projectPublicModel(rows[i])
	}
	return &AdminModelListResponse{Items: items, Page: q.Page, PageSize: q.PageSize, Total: total}, nil
}

func (s *PublicModelAdminService) Get(ctx context.Context, guid int64) (*PublicModelAdmin, error) {
	var m models.PublicModelConfig
	if guid <= 0 {
		return nil, errBadRequest("invalid guid")
	}
	if err := s.db.WithContext(ctx).Where("guid = ? AND is_deleted = 0", guid).First(&m).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errNotFound("public model not found")
		}
		return nil, err
	}
	v := projectPublicModel(m)
	return &v, nil
}

func (s *PublicModelAdminService) Update(ctx context.Context, actorID, guid int64, in UpdatePublicModelRequest) (*PublicModelAdmin, error) {
	return s.mutate(ctx, actorID, guid, in.ExpectedRevision, "public_models.update", "", func(m *models.PublicModelConfig) error {
		if in.DisplayName != nil {
			m.DisplayName = *in.DisplayName
		}
		if in.Provider != nil {
			m.Provider = *in.Provider
		}
		if in.Capabilities != nil {
			m.Capabilities = models.JSONSlice(*in.Capabilities)
		}
		if in.ContextWindow != nil {
			m.ContextWindow = *in.ContextWindow
		}
		if in.InputPriceUSDPerMillionTokens.Set {
			m.InputPriceUSDPerMillionTokens = in.InputPriceUSDPerMillionTokens.Value
		}
		if in.OutputPriceUSDPerMillionTokens.Set {
			m.OutputPriceUSDPerMillionTokens = in.OutputPriceUSDPerMillionTokens.Value
		}
		if !validPublicModelText(m.DisplayName, 128) || !validPublicModelText(m.Provider, 128) || !validPublicModelCapabilities([]string(m.Capabilities)) || m.ContextWindow <= 0 || !validPublicPrice(m.InputPriceUSDPerMillionTokens) || !validPublicPrice(m.OutputPriceUSDPerMillionTokens) {
			return errBadRequest("invalid public model request")
		}
		return nil
	})
}
func (s *PublicModelAdminService) Activate(ctx context.Context, actorID, guid, revision int64) (*PublicModelAdmin, error) {
	return s.mutate(ctx, actorID, guid, revision, "public_models.activate", "", func(m *models.PublicModelConfig) error {
		m.Status = models.PublicModelConfigStatusActive
		m.InactiveReason = nil
		return nil
	})
}
func (s *PublicModelAdminService) Deactivate(ctx context.Context, actorID, guid int64, in DeactivationRequest) (*PublicModelAdmin, error) {
	if !validPublicModelText(in.Reason, 128) {
		return nil, errBadRequest("reason is required")
	}
	return s.mutate(ctx, actorID, guid, in.ExpectedRevision, "public_models.deactivate", in.Reason, func(m *models.PublicModelConfig) error {
		m.Status = models.PublicModelConfigStatusInactive
		r := strings.TrimSpace(in.Reason)
		m.InactiveReason = &r
		return nil
	})
}
func (s *PublicModelAdminService) Delete(ctx context.Context, actorID, guid int64, in DeletePublicModelRequest) error {
	if !validPublicModelText(in.Reason, 128) {
		return errBadRequest("reason is required")
	}
	_, err := s.mutate(ctx, actorID, guid, in.ExpectedRevision, "public_models.delete", in.Reason, func(m *models.PublicModelConfig) error {
		m.IsDeleted = 1
		m.Status = models.PublicModelConfigStatusInactive
		r := strings.TrimSpace(in.Reason)
		m.InactiveReason = &r
		return nil
	})
	return err
}

func (s *PublicModelAdminService) mutate(ctx context.Context, actorID, guid, expected int64, action, reason string, change func(*models.PublicModelConfig) error) (*PublicModelAdmin, error) {
	if guid <= 0 || expected <= 0 {
		return nil, errBadRequest("invalid revisioned mutation")
	}
	var out PublicModelAdmin
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockPublicModelRoot(tx, actorID)
		if err != nil {
			return err
		}
		draft, err := lockPublicPriceDraftState(tx)
		if err != nil {
			return err
		}
		var m models.PublicModelConfig
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guid = ? AND is_deleted = 0", guid).First(&m).Error
		if err == gorm.ErrRecordNotFound {
			return errNotFound("public model not found")
		}
		if err != nil {
			return err
		}
		if m.Revision != expected {
			return errConflict("public model revision conflict")
		}
		if err = change(&m); err != nil {
			return err
		}
		now := s.now()
		m.Revision++
		m.UpdatedAt = now
		m.UpdatedBy = &actor.ID
		if err = tx.Save(&m).Error; err != nil {
			return err
		}
		if err = writePublicModelAudit(tx, s.nextGUID(), now, actor.ID, action, m.ModelKey, m.Guid, publicModelAuditDetail(m.ModelKey, m.Revision, reason)); err != nil {
			return err
		}
		if err = advancePublicPriceDraftState(tx, draft, actor.ID, now); err != nil {
			return err
		}
		out = projectPublicModel(m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func lockPublicPriceDraftState(tx *gorm.DB) (*models.PublicPriceDraftState, error) {
	var state models.PublicPriceDraftState
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_key = ? AND is_deleted = 0", "pricing").First(&state).Error
	if err != nil {
		return nil, errUnavailable("public price draft state unavailable")
	}
	return &state, nil
}
func advancePublicPriceDraftState(tx *gorm.DB, state *models.PublicPriceDraftState, actor, now int64) error {
	result := tx.Model(&models.PublicPriceDraftState{}).Where("id = ? AND revision = ?", state.ID, state.Revision).Updates(map[string]any{"revision": state.Revision + 1, "updated_at": now, "updated_by": actor})
	if result.Error != nil || result.RowsAffected != 1 {
		return errConflict("public price draft revision conflict")
	}
	state.Revision++
	return nil
}

func lockPublicModelRoot(tx *gorm.DB, id int64) (*models.User, error) {
	var u models.User
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "role", "status", "is_deleted").Where("id = ?", id).First(&u).Error
	if err == gorm.ErrRecordNotFound {
		return nil, errForbidden("active root required")
	}
	if err != nil {
		return nil, err
	}
	if u.IsDeleted != 0 || u.Status != models.UserStatusActive || u.Role != models.UserRoleRoot {
		return nil, errForbidden("active root required")
	}
	return &u, nil
}
func writePublicModelAudit(tx *gorm.DB, guid, now, actor int64, action, key string, target int64, detail models.JSONMap) error {
	if guid <= 0 {
		return errUnavailable("audit persistence unavailable")
	}
	resource := "public-models/" + strconv.FormatInt(target, 10)
	b, _ := json.Marshal(detail)
	if strings.Contains(strings.ToLower(string(b)), "credential") || strings.Contains(strings.ToLower(string(b)), "raw_payload") {
		return errUnavailable("unsafe audit detail")
	}
	return tx.Create(&models.AuditLog{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, UserID: &actor, Action: action, Resource: &resource, Detail: detail}).Error
}
func projectPublicModel(m models.PublicModelConfig) PublicModelAdmin {
	return PublicModelAdmin{GUID: fmt.Sprint(m.Guid), ModelKey: m.ModelKey, UpstreamModelID: m.UpstreamModelID, DisplayName: m.DisplayName, Provider: m.Provider, Capabilities: append([]string(nil), m.Capabilities...), ContextWindow: m.ContextWindow, InputPriceUSDPerMillionTokens: m.InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens: m.OutputPriceUSDPerMillionTokens, Status: m.Status.String(), Revision: m.Revision, LastUpstreamCheckAt: m.LastUpstreamCheckAt}
}
