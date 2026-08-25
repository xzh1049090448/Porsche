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
		q := state.DB.Where("is_deleted = 0").Order("created_at desc").Offset(skip).Limit(limit)
		if status != "" {
			parsed, ok := models.ParseUserStatus(status)
			if !ok {
				httpx.AbortJSON(c, http.StatusUnprocessableEntity, "无效用户状态")
				return
			}
			q = q.Where("status = ?", parsed)
		}
		var users []models.User
		if err := q.Find(&users).Error; err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, "读取用户失败")
			return
		}
		out := make([]map[string]interface{}, 0, len(users))
		for i := range users {
			out = append(out, dto.AdminUser(&users[i]))
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/:guid", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("guid"), 10, 64)
		var user models.User
		if err := state.DB.Where("guid = ? AND is_deleted = 0", id).First(&user).Error; err != nil {
			httpx.AbortJSON(c, http.StatusNotFound, "用户不存在")
			return
		}
		c.JSON(http.StatusOK, dto.AdminUser(&user))
	})
	g.PUT("/:guid", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("guid"), 10, 64)
		var user models.User
		if err := state.DB.Where("guid = ? AND is_deleted = 0", id).First(&user).Error; err != nil {
			httpx.AbortJSON(c, http.StatusNotFound, "用户不存在")
			return
		}
		var body struct {
			Status         *string  `json:"status"`
			PlanType       *string  `json:"plan_type"`
			AllowedModels  []string `json:"allowed_models"`
			DailyCallLimit *int     `json:"daily_call_limit"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Status != nil {
			status, ok := models.ParseUserStatus(*body.Status)
			if !ok {
				httpx.AbortJSON(c, http.StatusUnprocessableEntity, "无效用户状态")
				return
			}
			user.Status = status
		}
		if body.PlanType != nil {
			plan, ok := models.ParsePlanType(*body.PlanType)
			if !ok {
				httpx.AbortJSON(c, http.StatusUnprocessableEntity, "无效套餐类型")
				return
			}
			user.PlanType = plan
		}
		if body.AllowedModels != nil {
			user.AllowedModels = body.AllowedModels
		}
		if body.DailyCallLimit != nil {
			user.DailyCallLimit = *body.DailyCallLimit
		}
		service.TouchAudit(&user.AuditFields, middleware.CurrentUserID(c))
		if err := state.DB.Save(&user).Error; err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, "更新用户失败")
			return
		}
		c.JSON(http.StatusOK, dto.AdminUser(&user))
	})
	g.GET("/:guid/behavior", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("guid"), 10, 64)
		var user models.User
		if err := state.DB.Where("guid = ? AND is_deleted = 0", id).First(&user).Error; err != nil {
			httpx.AbortJSON(c, http.StatusNotFound, "用户不存在")
			return
		}
		behavior, err := service.UserBehavior(state.DB, user.ID)
		if err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, "读取用户行为失败")
			return
		}
		c.JSON(http.StatusOK, behavior)
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
		q := state.DB.Where("is_deleted = 0").Order("created_at desc").Offset(skip).Limit(limit)
		if action := c.Query("action"); action != "" {
			q = q.Where("action = ?", action)
		}
		if rawGUID := c.Query("user_guid"); rawGUID != "" {
			guid, err := strconv.ParseInt(rawGUID, 10, 64)
			if err != nil || guid < 1 {
				httpx.AbortJSON(c, http.StatusBadRequest, "无效用户标识")
				return
			}
			var user models.User
			if err := state.DB.Where("guid = ? AND is_deleted = 0", guid).First(&user).Error; err != nil {
				httpx.AbortJSON(c, http.StatusNotFound, "用户不存在")
				return
			}
			q = q.Where("user_id = ?", user.ID)
		}
		var logs []models.AuditLog
		if err := q.Find(&logs).Error; err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, "读取审计日志失败")
			return
		}
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
		dashboard, err := service.GetDashboard(state.DB)
		if err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, "读取仪表盘失败")
			return
		}
		c.JSON(http.StatusOK, dashboard)
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
			health, err := service.GetOrCreateModelHealth(state.DB, model.ID, "whitelabel")
			if err != nil {
				httpx.AbortJSON(c, http.StatusInternalServerError, "读取模型健康状态失败")
				return
			}
			results = append(results, dto.ModelHealth(health))
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
			health, healthErr := service.GetOrCreateModelHealth(state.DB, model.ID, "whitelabel")
			if healthErr != nil {
				httpx.AbortJSON(c, http.StatusInternalServerError, "读取模型健康状态失败")
				return
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
			now := timeNowUTC().UnixMilli()
			if err := service.SaveModelHealthCheck(state.DB, health, chatErr == nil, health.AvgLatencyMs, now); err != nil {
				httpx.AbortJSON(c, http.StatusInternalServerError, "保存模型健康状态失败")
				return
			}
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
