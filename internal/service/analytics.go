package service

import (
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

// AnalyticsFilters limits analytics queries to a UTC half-open interval and optional models/user.
type AnalyticsFilters struct {
	StartAtMillis int64
	EndAtMillis   int64
	RangeLabel    string
	Granularity   string
	Metric        string
	TopN          int
	Models        []string
	UserGUID      int64
}
type analyticsUsage struct {
	UserID    int64
	Model     string
	Tokens    int64
	CreatedAt int64
}
type analyticsItem struct {
	Key, Label    string
	Tokens, Calls int64
	Cost          float64
}

func AnalyticsSummary(db *gorm.DB, settings *config.Settings, f AnalyticsFilters) map[string]interface{} {
	rows := analyticsUsageRows(db, f)
	var tokens int64
	for _, r := range rows {
		tokens += r.Tokens
	}
	return map[string]interface{}{"total_tokens": tokens, "total_cost": analyticsCost(settings, tokens), "total_calls": int64(len(rows)), "range_label": f.RangeLabel, "start_at": formatAnalyticsMillis(f.StartAtMillis), "end_at": formatAnalyticsMillis(f.EndAtMillis), "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
}

func AnalyticsModels(db *gorm.DB, f AnalyticsFilters) map[string]interface{} {
	items := aggregateModelItems(analyticsUsageRows(db, f), nil)
	sort.Slice(items, func(i, j int) bool { return items[i].Tokens > items[j].Tokens })
	out := make([]map[string]interface{}, 0, len(items))
	for i, x := range items {
		out = append(out, map[string]interface{}{"model": x.Key, "total_tokens": x.Tokens, "total_calls": x.Calls, "is_top5": i < 5})
	}
	return map[string]interface{}{"items": out}
}

// AnalyticsChart returns data shaped for the six approved billing dashboard views.
func AnalyticsChart(db *gorm.DB, settings *config.Settings, view string, f AnalyticsFilters) map[string]interface{} {
	rows := analyticsUsageRows(db, f)
	chart := map[string]interface{}{"view": view, "metric": f.Metric, "granularity": f.Granularity, "start_at": formatAnalyticsMillis(f.StartAtMillis), "end_at": formatAnalyticsMillis(f.EndAtMillis), "time_labels": analyticsTimeLabels(f), "series": []map[string]interface{}{}, "ranking": []map[string]interface{}{}}
	switch view {
	case "consumption_distribution":
		chart["series"] = modelBucketSeries(rows, settings, f)
	case "call_trend":
		chart["series"] = totalCallBucketSeries(rows, settings, f)
	case "call_distribution", "call_ranking":
		chart["ranking"] = rankingMaps(aggregateModelItems(rows, settings), f.TopN, "calls")
	case "user_consumption_ranking":
		chart["ranking"] = rankingMaps(aggregateUserItems(db, rows, settings), f.TopN, "cost")
	case "user_consumption_trend":
		chart["series"] = totalCallBucketSeries(rows, settings, f)
	}
	return chart
}

func analyticsUsageRows(db *gorm.DB, f AnalyticsFilters) []analyticsUsage {
	var rows []analyticsUsage
	q := db.Model(&models.UsageRecord{}).
		Select("usage_records.user_id, usage_records.model, usage_records.tokens, usage_records.created_at").
		Joins("JOIN users ON users.id = usage_records.user_id AND users.is_deleted = 0").
		Where("usage_records.is_deleted = 0 AND usage_records.created_at >= ? AND usage_records.created_at < ? AND usage_records.model IS NOT NULL AND TRIM(usage_records.model) <> ''", f.StartAtMillis, f.EndAtMillis)
	if len(f.Models) > 0 {
		q = q.Where("model IN ?", f.Models)
	}
	if f.UserGUID > 0 {
		q = q.Where("users.guid = ?", f.UserGUID)
	}
	q.Order("usage_records.created_at asc, usage_records.id asc").Scan(&rows)
	return rows
}

func analyticsTimeLabels(f AnalyticsFilters) []string {
	d := analyticsBucketDuration(f.Granularity)
	start, end := time.UnixMilli(f.StartAtMillis).UTC().Truncate(d), time.UnixMilli(f.EndAtMillis).UTC()
	labels := []string{}
	for current := start; current.Before(end); current = current.Add(d) {
		labels = append(labels, current.Format(time.RFC3339))
	}
	return labels
}
func analyticsBucketDuration(value string) time.Duration {
	switch value {
	case "1h":
		return time.Hour
	case "4h":
		return 4 * time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return 2 * time.Hour
	}
}

func modelBucketSeries(rows []analyticsUsage, settings *config.Settings, f AnalyticsFilters) []map[string]interface{} {
	labels, byModel := analyticsTimeLabels(f), map[string]map[string]*analyticsItem{}
	for _, r := range rows {
		bucket := analyticsBucketLabel(r.CreatedAt, f.Granularity)
		if byModel[r.Model] == nil {
			byModel[r.Model] = map[string]*analyticsItem{}
		}
		if byModel[r.Model][bucket] == nil {
			byModel[r.Model][bucket] = &analyticsItem{}
		}
		byModel[r.Model][bucket].Tokens += r.Tokens
		byModel[r.Model][bucket].Calls++
	}
	names := []string{}
	for name := range byModel {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]map[string]interface{}, 0, len(names))
	for _, name := range names {
		data := make([]map[string]interface{}, 0, len(labels))
		for _, label := range labels {
			point := byModel[name][label]
			if point == nil {
				point = &analyticsItem{}
			}
			data = append(data, pointMap(label, point, settings))
		}
		out = append(out, map[string]interface{}{"name": name, "data": data})
	}
	return out
}

func totalCallBucketSeries(rows []analyticsUsage, settings *config.Settings, f AnalyticsFilters) []map[string]interface{} {
	labels, points := analyticsTimeLabels(f), map[string]*analyticsItem{}
	for _, r := range rows {
		bucket := analyticsBucketLabel(r.CreatedAt, f.Granularity)
		if points[bucket] == nil {
			points[bucket] = &analyticsItem{}
		}
		points[bucket].Tokens += r.Tokens
		points[bucket].Calls++
	}
	data := make([]map[string]interface{}, 0, len(labels))
	for _, label := range labels {
		point := points[label]
		if point == nil {
			point = &analyticsItem{}
		}
		data = append(data, pointMap(label, point, settings))
	}
	return []map[string]interface{}{{"name": "calls", "data": data}}
}
func pointMap(label string, point *analyticsItem, settings *config.Settings) map[string]interface{} {
	return map[string]interface{}{"time": label, "tokens": point.Tokens, "calls": point.Calls, "cost": analyticsCost(settings, point.Tokens)}
}

func aggregateModelItems(rows []analyticsUsage, settings *config.Settings) []analyticsItem {
	byKey := map[string]*analyticsItem{}
	for _, r := range rows {
		if byKey[r.Model] == nil {
			byKey[r.Model] = &analyticsItem{Key: r.Model, Label: r.Model}
		}
		byKey[r.Model].Tokens += r.Tokens
		byKey[r.Model].Calls++
	}
	return finalizeItems(byKey, settings)
}
func aggregateUserItems(db *gorm.DB, rows []analyticsUsage, settings *config.Settings) []analyticsItem {
	byKey, seen, ids := map[string]*analyticsItem{}, map[int64]bool{}, []int64{}
	for _, r := range rows {
		key := strconv.FormatInt(r.UserID, 10)
		if byKey[key] == nil {
			byKey[key] = &analyticsItem{Key: key}
			if !seen[r.UserID] {
				ids = append(ids, r.UserID)
				seen[r.UserID] = true
			}
		}
		byKey[key].Tokens += r.Tokens
		byKey[key].Calls++
	}
	var users []struct {
		ID       int64
		GUID     int64
		Nickname *string
	}
	if len(ids) > 0 {
		db.Model(&models.User{}).Select("id, guid, nickname").Where("id IN ? AND is_deleted = 0", ids).Scan(&users)
	}
	for _, user := range users {
		if item := byKey[strconv.FormatInt(user.ID, 10)]; item != nil {
			item.Key = strconv.FormatInt(user.GUID, 10)
			if user.Nickname == nil || strings.TrimSpace(*user.Nickname) == "" {
				item.Label = "用户 #" + item.Key
			} else {
				item.Label = strings.TrimSpace(*user.Nickname)
			}
		}
	}
	for _, item := range byKey {
		if item.Label == "" {
			item.Label = fmt.Sprintf("用户 #%s", item.Key)
		}
	}
	return finalizeItems(byKey, settings)
}

func analyticsBucketLabel(millis int64, granularity string) string {
	return time.UnixMilli(millis).UTC().Truncate(analyticsBucketDuration(granularity)).Format(time.RFC3339)
}

func formatAnalyticsMillis(millis int64) string {
	return time.UnixMilli(millis).UTC().Format(time.RFC3339Nano)
}
func finalizeItems(byKey map[string]*analyticsItem, settings *config.Settings) []analyticsItem {
	items := make([]analyticsItem, 0, len(byKey))
	for _, x := range byKey {
		x.Cost = analyticsCost(settings, x.Tokens)
		items = append(items, *x)
	}
	return items
}

func rankingMaps(items []analyticsItem, topN int, metric string) []map[string]interface{} {
	sort.Slice(items, func(i, j int) bool {
		if metric == "calls" {
			if items[i].Calls == items[j].Calls {
				return items[i].Key < items[j].Key
			}
			return items[i].Calls > items[j].Calls
		}
		if items[i].Cost == items[j].Cost {
			return items[i].Key < items[j].Key
		}
		return items[i].Cost > items[j].Cost
	})
	var total float64
	for _, x := range items {
		if metric == "calls" {
			total += float64(x.Calls)
		} else {
			total += x.Cost
		}
	}
	if topN <= 0 {
		topN = len(items)
	}
	visible := items
	if len(items) > topN {
		visible = append([]analyticsItem(nil), items[:topN]...)
		other := analyticsItem{Key: "other", Label: "其他"}
		for _, x := range items[topN:] {
			other.Tokens += x.Tokens
			other.Calls += x.Calls
			other.Cost += x.Cost
		}
		visible = append(visible, other)
	}
	out := make([]map[string]interface{}, 0, len(visible))
	for _, x := range visible {
		value := x.Cost
		if metric == "calls" {
			value = float64(x.Calls)
		}
		ratio := 0.0
		if total > 0 {
			ratio = value / total
		}
		out = append(out, map[string]interface{}{"key": x.Key, "label": x.Label, "tokens": x.Tokens, "calls": x.Calls, "cost": x.Cost, "ratio": ratio})
	}
	return out
}
func analyticsCost(settings *config.Settings, tokens int64) float64 {
	if settings == nil {
		return 0
	}
	return round2(float64(tokens) * settings.AnalyticsTokenPricePer1K / 1000)
}

func AnalyticsExportCSV(db *gorm.DB, view string, f AnalyticsFilters) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"view", "model", "tokens", "calls"})
	for _, x := range aggregateModelItems(analyticsUsageRows(db, f), nil) {
		_ = w.Write([]string{safeCSVField(view), safeCSVField(x.Key), strconv.FormatInt(x.Tokens, 10), strconv.FormatInt(x.Calls, 10)})
	}
	w.Flush()
	return b.String()
}

func safeCSVField(value string) string {
	if value != "" && strings.ContainsRune("=+-@", rune(value[0])) {
		return "'" + value
	}
	return value
}
func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
