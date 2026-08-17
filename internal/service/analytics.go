package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

type AnalyticsFilters struct {
	StartAt     time.Time
	EndAt       time.Time
	RangeLabel  string
	Granularity string
	Metric      string
	TopN        int
}

func AnalyticsSummary(db *gorm.DB, settings *config.Settings, f AnalyticsFilters) map[string]interface{} {
	var totalTokens int64
	var totalCalls int64
	db.Model(&models.UsageRecord{}).
		Where("created_at >= ? AND created_at <= ?", f.StartAt, f.EndAt).
		Select("COALESCE(SUM(tokens),0)").Scan(&totalTokens)
	db.Model(&models.UsageRecord{}).
		Where("created_at >= ? AND created_at <= ?", f.StartAt, f.EndAt).
		Count(&totalCalls)
	cost := float64(totalTokens) * settings.AnalyticsTokenPricePer1K / 1000
	return map[string]interface{}{
		"total_tokens": totalTokens,
		"total_cost":   round2(cost),
		"total_calls":  totalCalls,
		"range_label":  f.RangeLabel,
		"start_at":     f.StartAt.Format(time.RFC3339Nano),
		"end_at":       f.EndAt.Format(time.RFC3339Nano),
		"updated_at":   time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func AnalyticsModels(db *gorm.DB, f AnalyticsFilters) map[string]interface{} {
	var rows []struct {
		Model  string
		Tokens int64
		Calls  int64
	}
	db.Model(&models.UsageRecord{}).
		Select("model, COALESCE(SUM(tokens),0) as tokens, count(*) as calls").
		Where("created_at >= ? AND created_at <= ? AND model IS NOT NULL", f.StartAt, f.EndAt).
		Group("model").Order("tokens desc").Scan(&rows)

	items := make([]map[string]interface{}, 0, len(rows))
	for i, row := range rows {
		items = append(items, map[string]interface{}{
			"model":        row.Model,
			"total_tokens": row.Tokens,
			"total_calls":  row.Calls,
			"is_top5":      i < 5,
		})
	}
	return map[string]interface{}{"items": items}
}

func AnalyticsChart(db *gorm.DB, settings *config.Settings, view string, f AnalyticsFilters) map[string]interface{} {
	return map[string]interface{}{
		"view":         view,
		"metric":       f.Metric,
		"granularity":  f.Granularity,
		"start_at":     f.StartAt.Format(time.RFC3339Nano),
		"end_at":       f.EndAt.Format(time.RFC3339Nano),
		"time_labels":  []string{},
		"series":       []map[string]interface{}{},
		"ranking":      analyticsRanking(db, settings, f),
	}
}

func analyticsRanking(db *gorm.DB, settings *config.Settings, f AnalyticsFilters) []map[string]interface{} {
	var rows []struct {
		Model  string
		Tokens int64
		Calls  int64
	}
	db.Model(&models.UsageRecord{}).
		Select("model, COALESCE(SUM(tokens),0) as tokens, count(*) as calls").
		Where("created_at >= ? AND created_at <= ? AND model IS NOT NULL", f.StartAt, f.EndAt).
		Group("model").Order("tokens desc").Limit(f.TopN).Scan(&rows)

	var total int64
	for _, r := range rows {
		total += r.Tokens
	}
	out := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		ratio := 0.0
		if total > 0 {
			ratio = float64(row.Tokens) / float64(total)
		}
		out = append(out, map[string]interface{}{
			"key":    row.Model,
			"label":  row.Model,
			"tokens": row.Tokens,
			"cost":   round2(float64(row.Tokens) * settings.AnalyticsTokenPricePer1K / 1000),
			"calls":  row.Calls,
			"ratio":  round2(ratio),
		})
	}
	return out
}

func AnalyticsExportCSV(db *gorm.DB, view string, f AnalyticsFilters) string {
	var b strings.Builder
	b.WriteString("view,model,tokens,calls\n")
	var rows []struct {
		Model  string
		Tokens int64
		Calls  int64
	}
	db.Model(&models.UsageRecord{}).
		Select("model, COALESCE(SUM(tokens),0) as tokens, count(*) as calls").
		Where("created_at >= ? AND created_at <= ? AND model IS NOT NULL", f.StartAt, f.EndAt).
		Group("model").Scan(&rows)
	for _, row := range rows {
		b.WriteString(fmt.Sprintf("%s,%s,%d,%d\n", view, row.Model, row.Tokens, row.Calls))
	}
	return b.String()
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
