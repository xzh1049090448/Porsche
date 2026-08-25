package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

var analyticsViews = map[string]bool{
	"consumption_distribution": true, "call_trend": true, "call_distribution": true,
	"call_ranking": true, "user_consumption_ranking": true, "user_consumption_trend": true,
}

func RegisterAnalytics(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/billing/analytics", middleware.RequireUser(state))
	g.GET("/access", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"allowed": state.Settings.IsAnalyticsAdmin(middleware.CurrentUser(c).Phone)})
	})
	admin := g.Group("", middleware.RequireAnalyticsAdmin(state))
	admin.GET("/summary", func(c *gin.Context) {
		f, ok := analyticsFiltersOrAbort(c, "")
		if ok {
			c.JSON(http.StatusOK, service.AnalyticsSummary(state.DB, state.Settings, f))
		}
	})
	admin.GET("/models", func(c *gin.Context) {
		f, ok := analyticsFiltersOrAbort(c, "")
		if ok {
			c.JSON(http.StatusOK, service.AnalyticsModels(state.DB, f))
		}
	})
	admin.GET("/charts/:view", func(c *gin.Context) {
		view := c.Param("view")
		f, ok := analyticsFiltersOrAbort(c, view)
		if ok {
			c.JSON(http.StatusOK, service.AnalyticsChart(state.DB, state.Settings, view, f))
		}
	})
	admin.GET("/export", func(c *gin.Context) {
		view := c.Query("view")
		if !analyticsViews[view] {
			analyticsFiltersOrAbort(c, view)
			return
		}
		f, ok := analyticsFiltersOrAbort(c, view)
		if !ok {
			return
		}
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="analytics.csv"`)
		c.String(http.StatusOK, service.AnalyticsExportCSV(state.DB, view, f))
	})
}

func analyticsFiltersOrAbort(c *gin.Context, view string) (service.AnalyticsFilters, bool) {
	f, err := parseAnalyticsFilters(c, view)
	if err == nil {
		return f, true
	}
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_analytics_query", "message": "invalid analytics query"}})
	return service.AnalyticsFilters{}, false
}

func parseAnalyticsFilters(c *gin.Context, view string) (service.AnalyticsFilters, error) {
	if view != "" && !analyticsViews[view] {
		return service.AnalyticsFilters{}, errInvalidAnalyticsQuery
	}
	now := time.Now().UTC()
	if _, present := c.GetQuery("user_id"); present {
		// Internal user IDs are never accepted at the HTTP boundary.
		return service.AnalyticsFilters{}, errInvalidAnalyticsQuery
	}
	rangeValue := c.DefaultQuery("range", "24h")
	f := service.AnalyticsFilters{Granularity: c.DefaultQuery("granularity", "2h"), Metric: c.DefaultQuery("metric", "cost"), TopN: 10}
	if f.Granularity != "1h" && f.Granularity != "2h" && f.Granularity != "4h" && f.Granularity != "1d" {
		return f, errInvalidAnalyticsQuery
	}
	if f.Metric != "cost" && f.Metric != "tokens" {
		return f, errInvalidAnalyticsQuery
	}
	if raw, present := c.GetQuery("top_n"); present {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 5 || n > 50 {
			return f, errInvalidAnalyticsQuery
		}
		f.TopN = n
	}
	if raw, present := c.GetQuery("models"); present {
		f.Models = normalizeAnalyticsModels(raw)
		if strings.TrimSpace(raw) != "" && (len(f.Models) == 0 || len(f.Models) > 50) {
			return f, errInvalidAnalyticsQuery
		}
	}
	startRaw, hasStart := c.GetQuery("start_at")
	endRaw, hasEnd := c.GetQuery("end_at")
	if hasStart != hasEnd {
		return f, errInvalidAnalyticsQuery
	}
	if hasStart {
		start, startErr := time.Parse(time.RFC3339, startRaw)
		end, endErr := time.Parse(time.RFC3339, endRaw)
		if startErr != nil || endErr != nil || !start.Before(end) || end.Sub(start) > 90*24*time.Hour {
			return f, errInvalidAnalyticsQuery
		}
		f.StartAtMillis, f.EndAtMillis, f.RangeLabel = start.UTC().UnixMilli(), end.UTC().UnixMilli(), "自定义时间"
	} else {
		switch rangeValue {
		case "1h":
			f.StartAtMillis, f.RangeLabel = now.Add(-time.Hour).UnixMilli(), "近1小时"
		case "6h":
			f.StartAtMillis, f.RangeLabel = now.Add(-6*time.Hour).UnixMilli(), "近6小时"
		case "24h":
			f.StartAtMillis, f.RangeLabel = now.Add(-24*time.Hour).UnixMilli(), "近24小时"
		case "7d":
			f.StartAtMillis, f.RangeLabel = now.Add(-7*24*time.Hour).UnixMilli(), "近7天"
		case "yesterday":
			end := now.Truncate(24 * time.Hour)
			f.EndAtMillis = end.UnixMilli()
			f.StartAtMillis, f.RangeLabel = end.Add(-24*time.Hour).UnixMilli(), "昨日"
		default:
			return f, errInvalidAnalyticsQuery
		}
		if f.EndAtMillis == 0 {
			f.EndAtMillis = now.UnixMilli()
		}
	}
	if raw, present := c.GetQuery("user_guid"); present {
		userGUID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || userGUID <= 0 || view != "user_consumption_trend" {
			return f, errInvalidAnalyticsQuery
		}
		f.UserGUID = userGUID
	}
	if view == "user_consumption_trend" && f.UserGUID <= 0 {
		return f, errInvalidAnalyticsQuery
	}
	return f, nil
}

var errInvalidAnalyticsQuery = strconv.ErrSyntax

func normalizeAnalyticsModels(raw string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, part := range strings.Split(raw, ",") {
		model := strings.TrimSpace(part)
		if len(model) > 128 {
			return nil
		}
		if model != "" && !seen[model] {
			seen[model] = true
			out = append(out, model)
		}
	}
	return out
}
