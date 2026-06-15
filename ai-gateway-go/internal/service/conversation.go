package service

import (
	"fmt"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

type ConversationService struct{}

func CreateConversation(db *gorm.DB, user *models.User, title, model string, datasetEnabled bool, datasetIDs []int) (*models.Conversation, error) {
	t := "新对话"
	if title != "" {
		t = title
	}
	var modelPtr *string
	if model != "" {
		modelPtr = &model
	}
	conv := models.Conversation{
		UserID:         user.ID,
		Title:          t,
		Model:          modelPtr,
		DatasetEnabled: datasetEnabled,
		DatasetIDs:     datasetIDs,
	}
	return &conv, db.Create(&conv).Error
}

func GetConversation(db *gorm.DB, user *models.User, id uint, withMessages bool) (*models.Conversation, error) {
	q := db.Where("id = ? AND user_id = ?", id, user.ID)
	if withMessages {
		q = q.Preload("Messages", func(tx *gorm.DB) *gorm.DB {
			return tx.Order("created_at asc")
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
	if err := db.Model(&models.Conversation{}).Where("user_id = ?", user.ID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []models.Conversation
	err := db.Where("user_id = ?", user.ID).Order("updated_at desc").Offset(skip).Limit(limit).Find(&items).Error
	return items, total, err
}

func UpdateConversationTitle(db *gorm.DB, user *models.User, id uint, title string) (*models.Conversation, error) {
	conv, err := GetConversation(db, user, id, true)
	if err != nil {
		return nil, err
	}
	conv.Title = title
	return conv, db.Save(conv).Error
}

func DeleteConversation(db *gorm.DB, user *models.User, id uint) error {
	conv, err := GetConversation(db, user, id, false)
	if err != nil {
		return err
	}
	return db.Select("Messages").Delete(conv).Error
}

func AddMessage(db *gorm.DB, conv *models.Conversation, role, content, model string, datasetUsed bool, attribution *string, tokens int) (*models.Message, error) {
	var modelPtr *string
	if model != "" {
		modelPtr = &model
	}
	msg := models.Message{
		ConversationID:     conv.ID,
		Role:               role,
		Content:            content,
		Model:              modelPtr,
		DatasetUsed:        datasetUsed,
		DatasetAttribution: attribution,
		Tokens:             tokens,
	}
	if err := db.Create(&msg).Error; err != nil {
		return nil, err
	}
	conv.UpdatedAt = msg.CreatedAt
	_ = db.Save(conv).Error
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
		label := labels[msg.Role]
		if label == "" {
			label = msg.Role
		}
		lines = append(lines, fmt.Sprintf("## %s", label), msg.Content)
		if msg.DatasetAttribution != nil && *msg.DatasetAttribution != "" {
			lines = append(lines, fmt.Sprintf("> %s", *msg.DatasetAttribution))
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
