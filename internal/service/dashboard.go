package service

import (
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func GetDashboard(db *gorm.DB) map[string]interface{} {
	var totalUsers, totalConversations int64
	db.Model(&models.User{}).Count(&totalUsers)
	db.Model(&models.Conversation{}).Count(&totalConversations)

	today := time.Now().UTC().Truncate(24 * time.Hour)
	var activeToday int64
	db.Model(&models.UsageRecord{}).
		Where("created_at >= ?", today).
		Distinct("user_id").
		Count(&activeToday)

	var totalTokens int64
	db.Model(&models.User{}).Select("COALESCE(SUM(total_tokens_used),0)").Scan(&totalTokens)

	modelUsage := map[string]int64{}
	var modelRows []struct {
		Model string
		Count int64
	}
	db.Model(&models.UsageRecord{}).Select("model, count(*) as count").
		Where("model IS NOT NULL").Group("model").Scan(&modelRows)
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
	db.Model(&models.User{}).Select("plan_type, count(*) as count").Group("plan_type").Scan(&planRows)
	for _, row := range planRows {
		planDistribution[string(row.PlanType)] = row.Count
	}

	return map[string]interface{}{
		"total_users":         totalUsers,
		"active_users_today":  activeToday,
		"total_conversations": totalConversations,
		"total_tokens":        totalTokens,
		"model_usage":         modelUsage,
		"plan_distribution":   planDistribution,
	}
}

func UserBehavior(db *gorm.DB, userID int) map[string]interface{} {
	var modelRows []struct {
		Model  string
		Calls  int64
		Tokens int64
	}
	db.Model(&models.UsageRecord{}).
		Select("model, count(*) as calls, COALESCE(SUM(tokens),0) as tokens").
		Where("user_id = ? AND model IS NOT NULL", userID).
		Group("model").Scan(&modelRows)

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
	}
}
