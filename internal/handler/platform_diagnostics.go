package handler

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/diagnostics"
)

const platformDiagnosticAuthEnd = "platform_diagnostic_auth_end"

// This middleware must follow gatewayRequestID and precede authentication.
func platformDiagnostics() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost || c.Request.URL.Path != "/api/v1/platform/chat/completions" {
			c.Next()
			return
		}
		ctx, tr := diagnostics.New(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		authEnd := tr.Begin(diagnostics.Auth)
		c.Set(platformDiagnosticAuthEnd, authEnd)
		defer func() {
			if _, pending := c.Get(platformDiagnosticAuthEnd); pending {
				authEnd(diagnostics.Rejected)
			}
			tr.End(log.Writer(), c.Writer.Status(), c.Writer.Header().Get("X-Request-ID"))
		}()
		c.Next()
	}
}
func platformDiagnosticAuthenticated() gin.HandlerFunc {
	return func(c *gin.Context) {
		if value, ok := c.Get(platformDiagnosticAuthEnd); ok {
			value.(func(diagnostics.Reason))(diagnostics.OK)
			delete(c.Keys, platformDiagnosticAuthEnd)
		}
		c.Next()
	}
}
