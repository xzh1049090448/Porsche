package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var publicModelDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,8})?$`)
var publicModelKey = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type PublicModelAdmin struct {
	GUID                           string
	ModelKey                       string
	UpstreamModelID                string
	DisplayName                    string
	Provider                       string
	Capabilities                   []string
	ContextWindow                  int64
	InputPriceUSDPerMillionTokens  *string
	OutputPriceUSDPerMillionTokens *string
	Status                         string
	Revision                       int64
	LastUpstreamCheckAt            *int64
}
type CreatePublicModelRequest struct {
	UpstreamModelID, ModelKey, DisplayName, Provider              string
	Capabilities                                                  []string
	ContextWindow                                                 int64
	InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens *string
}
type UpdatePublicModelRequest struct {
	ExpectedRevision                                              int64
	DisplayName, Provider                                         *string
	Capabilities                                                  *[]string
	ContextWindow                                                 *int64
	InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens *string
}
type DeactivationRequest struct {
	ExpectedRevision int64
	Reason           string
}
type DeletePublicModelRequest struct {
	ExpectedRevision int64
	Reason           string
}
type AdminModelListRequest struct {
	Search, Status, UpstreamState string
	Page, PageSize                int
}
type AdminModelListResponse struct {
	Items          []PublicModelAdmin
	Page, PageSize int
	Total          int64
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
	if strings.TrimSpace(in.UpstreamModelID) == "" || !publicModelKey.MatchString(in.ModelKey) || strings.TrimSpace(in.DisplayName) == "" || strings.TrimSpace(in.Provider) == "" || in.Capabilities == nil || in.ContextWindow <= 0 {
		return errBadRequest("invalid public model request")
	}
	if !validPublicPrice(in.InputPriceUSDPerMillionTokens) || !validPublicPrice(in.OutputPriceUSDPerMillionTokens) {
		return errBadRequest("invalid public model price")
	}
	return nil
}
func validPublicPrice(v *string) bool { return v == nil || publicModelDecimal.MatchString(*v) }
func publicModelAuditDetail(key string, revision int64, reason string) models.JSONMap {
	return models.JSONMap{"model_key": key, "revision": revision, "reason": strings.TrimSpace(reason)}
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
		var reserved int64
		if err = tx.Model(&models.PublicModelConfig{}).Where("model_key = ?", in.ModelKey).Count(&reserved).Error; err != nil {
			return err
		}
		if reserved != 0 {
			return errConflict("model_key is permanently reserved")
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
		guid := s.nextGUID()
		if now <= 0 || guid <= 0 {
			return errUnavailable("public model persistence unavailable")
		}
		m := models.PublicModelConfig{AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actor.ID, UpdatedAt: now, UpdatedBy: &actor.ID}, ModelKey: in.ModelKey, UpstreamModelID: in.UpstreamModelID, DisplayName: strings.TrimSpace(in.DisplayName), Provider: strings.TrimSpace(in.Provider), Capabilities: models.JSONSlice(in.Capabilities), ContextWindow: in.ContextWindow, InputPriceUSDPerMillionTokens: in.InputPriceUSDPerMillionTokens, OutputPriceUSDPerMillionTokens: in.OutputPriceUSDPerMillionTokens, Status: models.PublicModelConfigStatusDraft, LastUpstreamObservedAt: &obs.ObservedAt, LastUpstreamCheckAt: &obs.ObservedAt, Revision: 1}
		if err = tx.Create(&m).Error; err != nil {
			return err
		}
		if err = writePublicModelAudit(tx, s.nextGUID(), now, actor.ID, "public_models.create", m.ModelKey, m.Guid, publicModelAuditDetail(m.ModelKey, m.Revision, "")); err != nil {
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
		op := "IS NOT NULL"
		if q.UpstreamState == "missing" {
			op = "IS NULL"
		}
		db = db.Where("last_upstream_observed_at " + op)
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
			m.DisplayName = strings.TrimSpace(*in.DisplayName)
		}
		if in.Provider != nil {
			m.Provider = strings.TrimSpace(*in.Provider)
		}
		if in.Capabilities != nil {
			m.Capabilities = models.JSONSlice(*in.Capabilities)
		}
		if in.ContextWindow != nil {
			m.ContextWindow = *in.ContextWindow
		}
		if in.InputPriceUSDPerMillionTokens != nil {
			m.InputPriceUSDPerMillionTokens = in.InputPriceUSDPerMillionTokens
		}
		if in.OutputPriceUSDPerMillionTokens != nil {
			m.OutputPriceUSDPerMillionTokens = in.OutputPriceUSDPerMillionTokens
		}
		if m.DisplayName == "" || m.Provider == "" || m.Capabilities == nil || m.ContextWindow <= 0 || !validPublicPrice(m.InputPriceUSDPerMillionTokens) || !validPublicPrice(m.OutputPriceUSDPerMillionTokens) {
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
	if strings.TrimSpace(in.Reason) == "" {
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
	if strings.TrimSpace(in.Reason) == "" {
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
		out = projectPublicModel(m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
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
