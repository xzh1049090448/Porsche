package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

func RegisterOpenAIChat(r *gin.Engine, state *app.State) {
	registerGatewayRoutes(r, state)
}

func RegisterAdminUsers(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/users", middleware.RequireAdmin(state))
	g.GET("", func(c *gin.Context) {
		skip := parseUintQuery(c, "skip", 0)
		limit := parseUintQuery(c, "limit", 50)
		status := c.Query("status")
		q := state.DB.Order("created_at desc").Offset(skip).Limit(limit)
		if status != "" {
			q = q.Where("status = ?", status)
		}
		var users []models.User
		q.Find(&users)
		out := make([]map[string]interface{}, 0, len(users))
		for i := range users {
			out = append(out, dto.AdminUser(&users[i]))
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/:id", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		var user models.User
		if err := state.DB.First(&user, id).Error; err != nil {
			httpx.AbortJSON(c, http.StatusNotFound, "用户不存在")
			return
		}
		c.JSON(http.StatusOK, dto.AdminUser(&user))
	})
	g.PUT("/:id", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		var user models.User
		if err := state.DB.First(&user, id).Error; err != nil {
			httpx.AbortJSON(c, http.StatusNotFound, "用户不存在")
			return
		}
		var body struct {
			Status          *string  `json:"status"`
			PlanType        *string  `json:"plan_type"`
			AllowedModels   []string `json:"allowed_models"`
			AllowedDatasets []int    `json:"allowed_datasets"`
			DailyCallLimit  *int     `json:"daily_call_limit"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Status != nil {
			user.Status = models.UserStatus(*body.Status)
		}
		if body.PlanType != nil {
			user.PlanType = models.PlanType(*body.PlanType)
		}
		if body.AllowedModels != nil {
			user.AllowedModels = body.AllowedModels
		}
		if body.AllowedDatasets != nil {
			user.AllowedDatasets = body.AllowedDatasets
		}
		if body.DailyCallLimit != nil {
			user.DailyCallLimit = *body.DailyCallLimit
		}
		_ = state.DB.Save(&user).Error
		c.JSON(http.StatusOK, dto.AdminUser(&user))
	})
	g.GET("/:id/behavior", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		var user models.User
		if err := state.DB.First(&user, id).Error; err != nil {
			httpx.AbortJSON(c, http.StatusNotFound, "用户不存在")
			return
		}
		c.JSON(http.StatusOK, service.UserBehavior(state.DB, int(id)))
	})
}

func RegisterAdminDatasets(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/datasets", middleware.RequireAdmin(state))
	g.GET("", func(c *gin.Context) {
		var rows []models.Dataset
		state.DB.Order("id asc").Find(&rows)
		out := make([]map[string]interface{}, 0, len(rows))
		for i := range rows {
			out = append(out, dto.Dataset(&rows[i]))
		}
		c.JSON(http.StatusOK, out)
	})
	g.POST("", func(c *gin.Context) {
		var body struct {
			Name        string   `json:"name" binding:"required"`
			Slug        string   `json:"slug" binding:"required"`
			Category    string   `json:"category" binding:"required"`
			Description *string  `json:"description"`
			AccessPlans []string `json:"access_plans"`
			AssetID     *string  `json:"asset_id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		var existing models.Dataset
		if err := state.DB.Where("slug = ?", body.Slug).First(&existing).Error; err == nil {
			httpx.AbortJSON(c, http.StatusConflict, "slug 已存在")
			return
		}
		plans := body.AccessPlans
		if len(plans) == 0 {
			plans = []string{"free", "professional", "enterprise"}
		}
		ds := models.Dataset{
			Name:        body.Name,
			Slug:        body.Slug,
			Category:    models.DatasetCategory(body.Category),
			Description: body.Description,
			AccessPlans: plans,
			AssetID:     body.AssetID,
			Status:      models.DatasetDraft,
		}
		if err := state.DB.Create(&ds).Error; err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, dto.Dataset(&ds))
	})
}

var alertConfigs = []map[string]interface{}{
	{"alert_type": "cost_overrun", "threshold": 10000.0, "enabled": true},
	{"alert_type": "abnormal_access", "threshold": 100.0, "enabled": true},
	{"alert_type": "service_down", "threshold": 1.0, "enabled": true},
}

func RegisterAdminLogs(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/logs", middleware.RequireAdmin(state))
	g.GET("", func(c *gin.Context) {
		skip := parseUintQuery(c, "skip", 0)
		limit := parseUintQuery(c, "limit", 50)
		q := state.DB.Order("created_at desc").Offset(skip).Limit(limit)
		if action := c.Query("action"); action != "" {
			q = q.Where("action = ?", action)
		}
		if uid := c.Query("user_id"); uid != "" {
			q = q.Where("user_id = ?", uid)
		}
		var logs []models.AuditLog
		q.Find(&logs)
		out := make([]map[string]interface{}, 0, len(logs))
		for i := range logs {
			out = append(out, dto.AuditLog(&logs[i]))
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/alerts", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"alerts": alertConfigs})
	})
	g.PUT("/alerts/:alert_type", func(c *gin.Context) {
		alertType := c.Param("alert_type")
		threshold, _ := strconv.ParseFloat(c.DefaultQuery("threshold", "0"), 64)
		enabled := c.DefaultQuery("enabled", "true") == "true"
		for _, cfg := range alertConfigs {
			if cfg["alert_type"] == alertType {
				cfg["threshold"] = threshold
				cfg["enabled"] = enabled
				c.JSON(http.StatusOK, cfg)
				return
			}
		}
		newCfg := map[string]interface{}{"alert_type": alertType, "threshold": threshold, "enabled": enabled}
		alertConfigs = append(alertConfigs, newCfg)
		c.JSON(http.StatusOK, newCfg)
	})
}

func RegisterAdminDashboard(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/dashboard", middleware.RequireAdmin(state))
	g.GET("", func(c *gin.Context) {
		c.JSON(http.StatusOK, service.GetDashboard(state.DB))
	})
	g.GET("/models/health", func(c *gin.Context) {
		if state.WhiteLabel == nil {
			platformWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("white-label service unavailable"))
			return
		}
		catalog, err := state.WhiteLabel.ListModels(c.Request.Context(), nil)
		if err != nil {
			platformWhiteLabelError(c, err)
			return
		}
		results := make([]map[string]interface{}, 0)
		for _, model := range catalog.Data {
			var health models.ModelHealth
			err := state.DB.Where("model_name = ?", model.ID).First(&health).Error
			if err != nil {
				health = models.ModelHealth{ModelName: model.ID, Provider: "whitelabel", IsAvailable: true}
				_ = state.DB.Create(&health).Error
			}
			results = append(results, dto.ModelHealth(&health))
		}
		c.JSON(http.StatusOK, results)
	})
	g.POST("/models/health/check", func(c *gin.Context) {
		if state.WhiteLabel == nil {
			platformWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("white-label service unavailable"))
			return
		}
		catalog, err := state.WhiteLabel.ListModels(c.Request.Context(), nil)
		if err != nil {
			platformWhiteLabelError(c, err)
			return
		}
		updated := make([]map[string]interface{}, 0)
		for _, model := range catalog.Data {
			var health models.ModelHealth
			_ = state.DB.Where("model_name = ?", model.ID).First(&health).Error
			if health.ID == 0 {
				health = models.ModelHealth{ModelName: model.ID, Provider: "whitelabel"}
			}
			maxTokens := 5
			payload, _ := json.Marshal(map[string]interface{}{"model": model.ID, "messages": []map[string]string{{"role": "user", "content": "Reply with OK."}}, "max_tokens": maxTokens, "n": 1, "stream": false})
			response, chatErr := state.WhiteLabel.Chat(c.Request.Context(), payload)
			if chatErr == nil {
				raw, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				if readErr != nil {
					chatErr = whitelabel.ErrUpstreamUnavailable("health response read failed")
				} else if _, projectErr := state.WhiteLabel.ProjectChatCompletion(raw, model.ID); projectErr != nil {
					chatErr = projectErr
				}
			}
			health.IsAvailable = chatErr == nil
			if chatErr != nil {
				health.ErrorRate = minFloat(1, health.ErrorRate+0.1)
			}
			now := timeNowUTC()
			health.LastCheckedAt = &now
			_ = state.DB.Save(&health).Error
			updated = append(updated, map[string]interface{}{"model": model.ID, "available": chatErr == nil})
		}
		c.JSON(http.StatusOK, gin.H{"checked": updated})
	})
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
