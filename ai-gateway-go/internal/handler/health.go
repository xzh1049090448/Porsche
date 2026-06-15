package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
)

func RegisterHealth(r *gin.Engine, state *app.State) {
	r.GET("/health", func(c *gin.Context) {
		if state.Settings.AppEnv == "production" {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":          "ok",
			"models_loaded":   state.Models.Count(),
			"clients_loaded":  state.Clients.ClientCount(),
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
}

func parseUintQuery(c *gin.Context, key string, def int) int {
	v := c.DefaultQuery(key, strconv.Itoa(def))
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
