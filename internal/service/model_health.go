package service

import (
	"errors"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

// NewModelHealth builds a system-owned health record with the mandatory
// metadata. Callers cannot create health rows without a GUID or timestamps.
func NewModelHealth(modelName, provider string, now, guid int64) models.ModelHealth {
	return models.ModelHealth{
		ModelName: modelName, Provider: provider, IsAvailable: true,
		AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, UpdatedAt: now, IsDeleted: 0},
	}
}

// GetOrCreateModelHealth returns only active health state and creates a
// system-owned row when the model has not been checked before.
func GetOrCreateModelHealth(db *gorm.DB, modelName, provider string) (*models.ModelHealth, error) {
	var health models.ModelHealth
	err := db.Where("model_name = ? AND is_deleted = 0", modelName).First(&health).Error
	if err == nil {
		return &health, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := persistence.NowMillis()
	health = NewModelHealth(modelName, provider, now, persistence.NextGUID())
	if err := db.Create(&health).Error; err != nil {
		// A concurrent health check may have inserted the globally unique model
		// name first; re-read the active row instead of failing the request.
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			if readErr := db.Where("model_name = ? AND is_deleted = 0", modelName).First(&health).Error; readErr == nil {
				return &health, nil
			}
		}
		return nil, err
	}
	return &health, nil
}

// SaveModelHealthCheck records a health check as a system update. It never
// revives a logically deleted row and updates all mutable audit fields.
func SaveModelHealthCheck(db *gorm.DB, health *models.ModelHealth, available bool, latency float64, checkedAt int64) error {
	if health == nil || health.ID == 0 {
		return errors.New("model health record is required")
	}
	health.IsAvailable = available
	health.AvgLatencyMs = latency
	health.LastCheckedAt = &checkedAt
	if !available {
		health.ErrorRate += 0.1
		if health.ErrorRate > 1 {
			health.ErrorRate = 1
		}
	}
	health.UpdatedAt = checkedAt
	health.UpdatedBy = nil
	result := db.Model(&models.ModelHealth{}).Where("id = ? AND is_deleted = 0", health.ID).Updates(map[string]interface{}{
		"is_available": health.IsAvailable, "avg_latency_ms": health.AvgLatencyMs, "error_rate": health.ErrorRate,
		"last_checked_at": health.LastCheckedAt, "updated_at": health.UpdatedAt, "updated_by": nil,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
