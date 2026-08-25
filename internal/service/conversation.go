package service

import (
	"fmt"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

type ConversationService struct{}

func CreateConversation(db *gorm.DB, user *models.User, title, model string) (*models.Conversation, error) {
	t := "新对话"
	if title != "" {
		t = title
	}
	var modelPtr *string
	if model != "" {
		modelPtr = &model
	}
	conv := models.Conversation{
		UserID:      user.ID,
		Title:       t,
		Model:       modelPtr,
		AuditFields: auditFields(&user.ID),
	}
	return &conv, db.Create(&conv).Error
}

func GetConversation(db *gorm.DB, user *models.User, id int64, withMessages bool) (*models.Conversation, error) {
	q := db.Where("guid = ? AND user_id = ? AND is_deleted = 0", id, user.ID)
	if withMessages {
		q = q.Preload("Messages", func(tx *gorm.DB) *gorm.DB {
			return tx.Where("is_deleted = 0").Order("created_at asc")
		})
	}
	var conv models.Conversation
	if err := q.First(&conv).Error; err != nil {
		return nil, errNotFound("对话不存在")
	}
	return &conv, nil
}

func ListConversations(db *gorm.DB, user *models.User, skip, limit int) ([]models.Conversation, int64, error) {
	var total int64
	if err := db.Model(&models.Conversation{}).Where("user_id = ? AND is_deleted = 0", user.ID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []models.Conversation
	err := db.Where("user_id = ? AND is_deleted = 0", user.ID).Order("updated_at desc").Offset(skip).Limit(limit).Find(&items).Error
	return items, total, err
}

func UpdateConversationTitle(db *gorm.DB, user *models.User, id int64, title string) (*models.Conversation, error) {
	conv, err := GetConversation(db, user, id, true)
	if err != nil {
		return nil, err
	}
	conv.Title = title
	stampUpdate(&conv.AuditFields, user.ID)
	return conv, db.Save(conv).Error
}

func DeleteConversation(db *gorm.DB, user *models.User, id int64) error {
	return db.Transaction(func(tx *gorm.DB) error {
		conv, err := GetConversation(tx, user, id, false)
		if err != nil {
			return err
		}
		now := persistence.NowMillis()
		if err := tx.Model(&models.Message{}).Where("conversation_id = ? AND is_deleted = 0", conv.ID).Updates(map[string]interface{}{"is_deleted": 1, "updated_at": now, "updated_by": user.ID}).Error; err != nil {
			return err
		}
		result := tx.Model(&models.Conversation{}).Where("id = ? AND is_deleted = 0", conv.ID).Updates(map[string]interface{}{"is_deleted": 1, "updated_at": now, "updated_by": user.ID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errNotFound("对话不存在")
		}
		return nil
	})
}

func AddMessage(db *gorm.DB, conv *models.Conversation, role, content, model string, tokens int) (*models.Message, error) {
	var modelPtr *string
	if model != "" {
		modelPtr = &model
	}
	messageRole, ok := models.ParseMessageRole(role)
	if !ok {
		return nil, errBadRequest("无效的消息角色")
	}
	msg := models.Message{
		ConversationID: conv.ID,
		Role:           messageRole,
		Content:        content,
		Model:          modelPtr,
		Tokens:         tokens,
		AuditFields:    auditFields(&conv.UserID),
	}
	if err := db.Create(&msg).Error; err != nil {
		return nil, err
	}
	conv.UpdatedAt = msg.CreatedAt
	conv.UpdatedBy = &conv.UserID
	if err := db.Save(conv).Error; err != nil {
		return nil, err
	}
	return &msg, nil
}

func TrimMessages(messages []map[string]interface{}, contextWindow *int) []map[string]interface{} {
	if contextWindow == nil || *contextWindow <= 0 {
		return messages
	}
	n := *contextWindow * 2
	if len(messages) <= n {
		return messages
	}
	return messages[len(messages)-n:]
}

func ExportMarkdown(conv *models.Conversation) string {
	lines := []string{fmt.Sprintf("# %s", conv.Title), ""}
	labels := map[string]string{"user": "用户", "assistant": "助手", "system": "系统"}
	for _, msg := range conv.Messages {
		label := labels[msg.Role.String()]
		if label == "" {
			label = msg.Role.String()
		}
		lines = append(lines, fmt.Sprintf("## %s", label), msg.Content)
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func auditFields(actor *int64) models.AuditFields {
	now := persistence.NowMillis()
	return models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: actor, UpdatedAt: now, UpdatedBy: actor, IsDeleted: 0}
}

func stampUpdate(fields *models.AuditFields, actor int64) {
	fields.UpdatedAt = persistence.NowMillis()
	fields.UpdatedBy = &actor
}

// TouchAudit updates the mutable audit columns for a user-initiated write.
// It is shared by handlers and services so direct Save calls cannot omit them.
func TouchAudit(fields *models.AuditFields, actor int64) {
	if actor <= 0 {
		stampSystemUpdate(fields)
		return
	}
	stampUpdate(fields, actor)
}

func stampSystemUpdate(fields *models.AuditFields) {
	fields.UpdatedAt = persistence.NowMillis()
	fields.UpdatedBy = nil
}
