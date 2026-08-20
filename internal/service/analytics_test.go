package service

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func TestAnalyticsChartBuildsAllApprovedViewsFromFilteredUsage(t *testing.T) {
	database := analyticsFixtureDB(t)
	settings := &config.Settings{AnalyticsTokenPricePer1K: 2}
	filters := AnalyticsFilters{
		StartAt:     time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC),
		EndAt:       time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
		Granularity: "1h",
		TopN:        2,
	}

	for _, view := range []string{
		"consumption_distribution", "call_trend", "call_distribution", "call_ranking",
		"user_consumption_ranking", "user_consumption_trend",
	} {
		if view == "user_consumption_trend" {
			filters.UserID = 1
		} else {
			filters.UserID = 0
		}
		chart := AnalyticsChart(database, settings, view, filters)
		labels, ok := chart["time_labels"].([]string)
		if !ok || len(labels) != 2 {
			t.Fatalf("%s labels = %#v, want two UTC buckets", view, chart["time_labels"])
		}
		if view == "consumption_distribution" && len(chart["series"].([]map[string]interface{})) != 3 {
			t.Fatalf("%s series = %#v, want one series per model", view, chart["series"])
		}
		if view == "call_trend" && chart["series"].([]map[string]interface{})[0]["data"].([]map[string]interface{})[0]["calls"] != int64(4) {
			t.Fatalf("%s must aggregate calls per bucket: %#v", view, chart["series"])
		}
		if view == "call_distribution" || view == "call_ranking" {
			ranking := chart["ranking"].([]map[string]interface{})
			if len(ranking) != 3 || ranking[0]["key"] != "model-b" || ranking[0]["calls"] != int64(3) {
				t.Fatalf("%s must rank by calls and retain other: %#v", view, ranking)
			}
			assertRatioSum(t, ranking)
		}
		if view == "user_consumption_ranking" {
			filters.TopN = 1
			chart = AnalyticsChart(database, settings, view, filters)
			ranking := chart["ranking"].([]map[string]interface{})
			if len(ranking) != 2 || ranking[0]["label"] != "Alice" || ranking[1]["key"] != "other" {
				t.Fatalf("user ranking must use nickname and other bucket: %#v", ranking)
			}
			assertRatioSum(t, ranking)
			filters.TopN = 2
		}
		if view == "user_consumption_trend" && chart["series"].([]map[string]interface{})[0]["data"].([]map[string]interface{})[1]["cost"] != float64(4) {
			t.Fatalf("user trend must scope cost to selected user: %#v", chart["series"])
		}
	}
}

func analyticsFixtureDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := db.Open("sqlite://"+t.TempDir()+"/analytics.db", "test")
	if err != nil {
		t.Fatal(err)
	}
	alice, bob := "Alice", "Bob"
	users := []models.User{{ID: 1, Phone: "13800000001", Nickname: &alice}, {ID: 2, Phone: "13800000002", Nickname: &bob}}
	if err := database.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	a, b, c := "model-a", "model-b", "model-c"
	records := []models.UsageRecord{
		{UserID: 1, Model: &a, Tokens: 1000, CreatedAt: base.Add(5 * time.Minute)},
		{UserID: 1, Model: &b, Tokens: 100, CreatedAt: base.Add(10 * time.Minute)},
		{UserID: 2, Model: &b, Tokens: 100, CreatedAt: base.Add(15 * time.Minute)},
		{UserID: 2, Model: &b, Tokens: 100, CreatedAt: base.Add(20 * time.Minute)},
		{UserID: 1, Model: &c, Tokens: 1000, CreatedAt: base.Add(65 * time.Minute)},
		{UserID: 1, Model: &a, Tokens: 1000, CreatedAt: base.Add(70 * time.Minute)},
		{UserID: 2, Model: nil, Tokens: 9999, CreatedAt: base.Add(30 * time.Minute)},
	}
	if err := database.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	return database
}

func assertRatioSum(t *testing.T, ranking []map[string]interface{}) {
	t.Helper()
	var total float64
	for _, item := range ranking {
		total += item["ratio"].(float64)
	}
	if math.Abs(total-1) > 1e-12 {
		t.Fatalf("ratio sum = %v, want 1: %#v", total, ranking)
	}
}

func TestAnalyticsExportCSVEscapesFormulaLikeModelNames(t *testing.T) {
	database := analyticsFixtureDB(t)
	model := "=SUM(1,1)"
	if err := database.Create(&models.UsageRecord{UserID: 1, Model: &model, Tokens: 1, CreatedAt: time.Date(2026, 8, 19, 10, 30, 0, 0, time.UTC)}).Error; err != nil {
		t.Fatal(err)
	}
	csv := AnalyticsExportCSV(database, "call_ranking", AnalyticsFilters{StartAt: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC), EndAt: time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)})
	if !strings.Contains(csv, "'=SUM(1,1)") {
		t.Fatalf("formula-like model must be prefixed in csv: %q", csv)
	}
}
