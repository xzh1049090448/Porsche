package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func RegisterAnalytics(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/billing/analytics", middleware.RequireUser(state))

	g.GET("/access", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		c.JSON(http.StatusOK, gin.H{"allowed": state.Settings.IsAnalyticsAdmin(user.Phone)})
	})

	admin := g.Group("", middleware.RequireAnalyticsAdmin(state))
	admin.GET("/summary", func(c *gin.Context) {
		f := parseAnalyticsFilters(c)
		c.JSON(http.StatusOK, service.AnalyticsSummary(state.DB, state.Settings, f))
	})
	admin.GET("/models", func(c *gin.Context) {
		f := parseAnalyticsFilters(c)
		c.JSON(http.StatusOK, service.AnalyticsModels(state.DB, f))
	})
	admin.GET("/charts/:view", func(c *gin.Context) {
		view := c.Param("view")
		f := parseAnalyticsFilters(c)
		c.JSON(http.StatusOK, service.AnalyticsChart(state.DB, state.Settings, view, f))
	})
	admin.GET("/export", func(c *gin.Context) {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="analytics.csv"`)
		f := parseAnalyticsFilters(c)
		view := c.Query("view")
		c.String(http.StatusOK, service.AnalyticsExportCSV(state.DB, view, f))
	})
}

func parseAnalyticsFilters(c *gin.Context) service.AnalyticsFilters {
	preset := c.DefaultQuery("range", "24h")
	granularity := c.DefaultQuery("granularity", "2h")
	metric := c.DefaultQuery("metric", "cost")
	topN := parseUintQuery(c, "top_n", 10)
	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)
	label := "近24小时"
	switch preset {
	case "1h":
		start = now.Add(-1 * time.Hour)
		label = "近1小时"
	case "6h":
		start = now.Add(-6 * time.Hour)
		label = "近6小时"
	case "7d":
		start = now.Add(-7 * 24 * time.Hour)
		label = "近7天"
	}
	return service.AnalyticsFilters{
		StartAt:     start,
		EndAt:       now,
		RangeLabel:  label,
		Granularity: granularity,
		Metric:      metric,
		TopN:        topN,
	}
}
