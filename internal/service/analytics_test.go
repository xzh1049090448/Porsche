package service

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func TestAnalyticsChartBuildsAllApprovedViewsFromFilteredUsage(t *testing.T) {
	database, analyticsAliceGUID := analyticsFixtureDB(t)
	settings := &config.Settings{AnalyticsTokenPricePer1K: 2}
	filters := AnalyticsFilters{
		StartAtMillis: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC).UnixMilli(),
		EndAtMillis:   time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC).UnixMilli(),
		Granularity:   "1h",
		TopN:          2,
	}

	for _, view := range []string{
		"consumption_distribution", "call_trend", "call_distribution", "call_ranking",
		"user_consumption_ranking", "user_consumption_trend",
	} {
		if view == "user_consumption_trend" {
			filters.UserGUID = analyticsAliceGUID
		} else {
			filters.UserGUID = 0
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

func analyticsFixtureDB(t *testing.T) (*gorm.DB, int64) {
	t.Helper()
	database := openTestMySQL(t)
	alice, bob := "Alice", "Bob"
	base := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	aliceGUID := testSnowflake.Next()
	users := []models.User{
		{AuditFields: models.AuditFields{Guid: aliceGUID, CreatedAt: base.UnixMilli(), UpdatedAt: base.UnixMilli(), IsDeleted: 0}, Phone: testPhone(), Nickname: &alice, Status: models.UserStatusActive, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{}},
		{AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: base.UnixMilli(), UpdatedAt: base.UnixMilli(), IsDeleted: 0}, Phone: testPhone(), Nickname: &bob, Status: models.UserStatusActive, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{}},
	}
	if err := database.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	a, b, c := "model-a", "model-b", "model-c"
	records := []models.UsageRecord{
		usageFixture(users[0].ID, &a, 1000, base.Add(5*time.Minute)),
		usageFixture(users[0].ID, &b, 100, base.Add(10*time.Minute)),
		usageFixture(users[1].ID, &b, 100, base.Add(15*time.Minute)),
		usageFixture(users[1].ID, &b, 100, base.Add(20*time.Minute)),
		usageFixture(users[0].ID, &c, 1000, base.Add(65*time.Minute)),
		usageFixture(users[0].ID, &a, 1000, base.Add(70*time.Minute)),
		usageFixture(users[1].ID, nil, 9999, base.Add(30*time.Minute)),
	}
	if err := database.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	return database, aliceGUID
}

func usageFixture(userID int64, model *string, tokens int, created time.Time) models.UsageRecord {
	createdAt := created.UTC().UnixMilli()
	return models.UsageRecord{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: createdAt, UpdatedAt: createdAt, IsDeleted: 0},
		UserID:      userID, RecordType: models.UsageRecordChat, Model: model, Tokens: tokens,
	}
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
	database, analyticsAliceGUID := analyticsFixtureDB(t)
	model := "=SUM(1,1)"
	var alice models.User
	if err := database.Where("guid = ? AND is_deleted = 0", analyticsAliceGUID).First(&alice).Error; err != nil {
		t.Fatal(err)
	}
	record := usageFixture(alice.ID, &model, 1, time.Date(2026, 8, 19, 10, 30, 0, 0, time.UTC))
	if err := database.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	csv := AnalyticsExportCSV(database, "call_ranking", AnalyticsFilters{StartAtMillis: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC).UnixMilli(), EndAtMillis: time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC).UnixMilli()})
	if !strings.Contains(csv, "'=SUM(1,1)") {
		t.Fatalf("formula-like model must be prefixed in csv: %q", csv)
	}
}
