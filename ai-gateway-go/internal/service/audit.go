package service

import (
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

type AuditService struct{}

func NewAuditService() *AuditService { return &AuditService{} }

func (a *AuditService) Log(db *gorm.DB, action string, userID *uint, resource string, detail models.JSONMap, ip string) error {
	var resourcePtr *string
	if resource != "" {
		resourcePtr = &resource
	}
	var ipPtr *string
	if ip != "" {
		ipPtr = &ip
	}
	log := models.AuditLog{
		UserID:   userID,
		Action:   action,
		Resource: resourcePtr,
		Detail:   detail,
		IP:       ipPtr,
	}
	return db.Create(&log).Error
}
