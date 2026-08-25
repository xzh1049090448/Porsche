package service

import (
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func GetDashboard(db *gorm.DB) (map[string]interface{}, error) {
	var totalUsers, totalConversations int64
	if err := db.Model(&models.User{}).Where("is_deleted = 0").Count(&totalUsers).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&models.Conversation{}).Where("is_deleted = 0").Count(&totalConversations).Error; err != nil {
		return nil, err
	}

	today := time.Now().UTC().Truncate(24 * time.Hour).UnixMilli()
	var activeToday int64
	if err := db.Model(&models.UsageRecord{}).
		Joins("JOIN users ON users.id = usage_records.user_id AND users.is_deleted = 0").
		Where("usage_records.is_deleted = 0 AND usage_records.created_at >= ?", today).
		Distinct("user_id").
		Count(&activeToday).Error; err != nil {
		return nil, err
	}

	var totalTokens int64
	if err := db.Model(&models.User{}).Where("is_deleted = 0").Select("COALESCE(SUM(total_tokens_used),0)").Scan(&totalTokens).Error; err != nil {
		return nil, err
	}

	modelUsage := map[string]int64{}
	var modelRows []struct {
		Model string
		Count int64
	}
	if err := db.Model(&models.UsageRecord{}).Select("usage_records.model, count(*) as count").
		Joins("JOIN users ON users.id = usage_records.user_id AND users.is_deleted = 0").
		Where("usage_records.is_deleted = 0 AND usage_records.model IS NOT NULL").Group("usage_records.model").Scan(&modelRows).Error; err != nil {
		return nil, err
	}
	for _, row := range modelRows {
		if row.Model != "" {
			modelUsage[row.Model] = row.Count
		}
	}

	planDistribution := map[string]int64{}
	var planRows []struct {
		PlanType models.PlanType
		Count    int64
	}
	if err := db.Model(&models.User{}).Where("is_deleted = 0").Select("plan_type, count(*) as count").Group("plan_type").Scan(&planRows).Error; err != nil {
		return nil, err
	}
	for _, row := range planRows {
		planDistribution[row.PlanType.String()] = row.Count
	}

	return map[string]interface{}{
		"total_users":         totalUsers,
		"active_users_today":  activeToday,
		"total_conversations": totalConversations,
		"total_tokens":        totalTokens,
		"model_usage":         modelUsage,
		"plan_distribution":   planDistribution,
	}, nil
}

func UserBehavior(db *gorm.DB, userID int64) (map[string]interface{}, error) {
	var modelRows []struct {
		Model  string
		Calls  int64
		Tokens int64
	}
	if err := db.Model(&models.UsageRecord{}).
		Select("model, count(*) as calls, COALESCE(SUM(tokens),0) as tokens").
		Where("user_id = ? AND is_deleted = 0 AND model IS NOT NULL", userID).
		Group("model").Scan(&modelRows).Error; err != nil {
		return nil, err
	}

	modelsOut := make([]map[string]interface{}, 0)
	for _, row := range modelRows {
		if row.Model == "" {
			continue
		}
		modelsOut = append(modelsOut, map[string]interface{}{
			"model": row.Model, "calls": row.Calls, "tokens": row.Tokens,
		})
	}

	return map[string]interface{}{
		"model_preferences": modelsOut,
	}, nil
}
