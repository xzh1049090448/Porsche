package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

var modelHealthChecks sync.Map

func RegisterHealth(r *gin.Engine, state *app.State) {
	r.GET("/health", func(c *gin.Context) {
		if state.Settings.AppEnv == "production" {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":         "ok",
			"models_loaded":  state.Models.Count(),
			"clients_loaded": state.Clients.ClientCount(),
		})
	})
}

func RegisterAdmin(r *gin.Engine, state *app.State) {
	g := r.Group("/admin", middleware.RequireAdmin(state))
	g.GET("/status", func(c *gin.Context) {
		routes := map[string]string{}
		for name, route := range state.Models.Routes() {
			routes[name] = route.Provider
		}
		c.JSON(http.StatusOK, gin.H{
			"models":  state.Models.Count(),
			"clients": state.Clients.ClientCount(),
			"routes":  routes,
		})
	})
	g.POST("/reload-config", func(c *gin.Context) {
		state.ReloadConfig()
		c.JSON(http.StatusOK, gin.H{
			"status":  "reloaded",
			"models":  state.Models.Count(),
			"clients": state.Clients.ClientCount(),
		})
	})
	g.POST("/models/:id/health-check", func(c *gin.Context) {
		modelID := c.Param("id")
		if state.WhiteLabel == nil {
			platformWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("white-label service unavailable"))
			return
		}
		if _, loaded := modelHealthChecks.LoadOrStore(modelID, struct{}{}); loaded {
			platformWhiteLabelError(c, &whitelabel.Error{Code: "health_check_in_progress", Status: http.StatusConflict, Type: whitelabel.TypeAPI})
			return
		}
		defer modelHealthChecks.Delete(modelID)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		if _, err := state.WhiteLabel.GetModel(ctx, modelID, nil); err != nil {
			platformWhiteLabelError(c, err)
			return
		}
		maxTokens := 5
		payload, _ := json.Marshal(map[string]interface{}{"model": modelID, "messages": []map[string]string{{"role": "user", "content": "Reply with OK."}}, "max_tokens": maxTokens, "n": 1, "stream": false})
		started := time.Now()
		resp, upstreamErr := state.WhiteLabel.Chat(ctx, payload)
		if upstreamErr != nil {
			platformWhiteLabelError(c, upstreamErr)
			return
		}
		defer resp.Body.Close()
		raw := make([]byte, 0)
		// The adapter projection validates structure while discarding every
		// upstream-owned field. The prompt and completion are never stored.
		raw, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			platformWhiteLabelError(c, whitelabel.ErrUpstreamUnavailable("health response read failed"))
			return
		}
		if _, projectErr := state.WhiteLabel.ProjectChatCompletion(raw, modelID); projectErr != nil {
			platformWhiteLabelError(c, projectErr)
			return
		}
		checkedAt := time.Now().UTC()
		latency := float64(time.Since(started).Milliseconds())
		health := models.ModelHealth{ModelName: modelID, Provider: "whitelabel", IsAvailable: true, AvgLatencyMs: latency, LastCheckedAt: &checkedAt}
		if err := state.DB.Where("model_name = ?", modelID).FirstOrCreate(&health).Error; err == nil {
			health.IsAvailable, health.AvgLatencyMs, health.LastCheckedAt = true, latency, &checkedAt
			_ = state.DB.Save(&health).Error
		}
		_ = state.Audit.Log(state.DB, "model.health_check", nil, modelID, nil, "")
		c.JSON(http.StatusOK, gin.H{"model_id": modelID, "status": "healthy", "latency_ms": int(latency), "request_id": c.Writer.Header().Get("X-Request-ID"), "checked_at": checkedAt.Format(time.RFC3339)})
	})
}

func parseUintQuery(c *gin.Context, key string, def int) int {
	v := c.DefaultQuery(key, strconv.Itoa(def))
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
