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

	var totalTokens, datasetCalls int64
	db.Model(&models.User{}).Select("COALESCE(SUM(total_tokens_used),0)").Scan(&totalTokens)
	db.Model(&models.User{}).Select("COALESCE(SUM(dataset_calls),0)").Scan(&datasetCalls)

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

	datasetUsage := map[string]int64{}
	var dsRows []struct {
		DatasetID uint
		Count     int64
	}
	db.Model(&models.UsageRecord{}).Select("dataset_id, count(*) as count").
		Where("dataset_id IS NOT NULL").Group("dataset_id").Scan(&dsRows)
	for _, row := range dsRows {
		if row.DatasetID > 0 {
			datasetUsage[itoa(int(row.DatasetID))] = row.Count
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
		"total_users":          totalUsers,
		"active_users_today":   activeToday,
		"total_conversations":  totalConversations,
		"total_tokens":         totalTokens,
		"dataset_calls":        datasetCalls,
		"model_usage":          modelUsage,
		"dataset_usage":        datasetUsage,
		"plan_distribution":    planDistribution,
	}
}

func UserBehavior(db *gorm.DB, userID uint) map[string]interface{} {
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

	datasetUsage := map[string]int64{}
	var dsRows []struct {
		DatasetID uint
		Count     int64
	}
	db.Model(&models.UsageRecord{}).
		Select("dataset_id, count(*) as count").
		Where("user_id = ? AND dataset_id IS NOT NULL", userID).
		Group("dataset_id").Scan(&dsRows)
	for _, row := range dsRows {
		if row.DatasetID > 0 {
			datasetUsage[itoa(int(row.DatasetID))] = row.Count
		}
	}

	return map[string]interface{}{
		"model_preferences": modelsOut,
		"dataset_usage":     datasetUsage,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
