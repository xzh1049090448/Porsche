package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/handler"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func New(state *app.State) *gin.Engine {
	if state.Settings.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery(), gin.Logger())
	r.Use(middleware.InjectState(state))

	handler.RegisterHealth(r, state)
	handler.RegisterOpenAIChat(r, state)
	handler.RegisterAuth(r, state)
	handler.RegisterUsers(r, state)
	handler.RegisterConversations(r, state)
	handler.RegisterDatasets(r, state)
	handler.RegisterBilling(r, state)
	handler.RegisterPlatform(r, state)
	handler.RegisterAnalytics(r, state)

	handler.RegisterAdmin(r, state)
	handler.RegisterAdminUsers(r, state)
	handler.RegisterAdminDatasets(r, state)
	handler.RegisterAdminLogs(r, state)
	handler.RegisterAdminDashboard(r, state)

	r.GET("/metrics", func(c *gin.Context) {
		token := httpx.BearerToken(c)
		if token == "" {
			httpx.AbortJSON(c, http.StatusUnauthorized, "Missing metrics Authorization")
			return
		}
		if token != state.Settings.MetricsAuthToken() {
			httpx.AbortJSON(c, http.StatusForbidden, "Forbidden")
			return
		}
		promhttp.Handler().ServeHTTP(c.Writer, c.Request)
	})

	return r
}
