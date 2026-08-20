package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/httpx"
)

func RequireAllowedHost(allowedHosts string) gin.HandlerFunc {
	allowed := make(map[string]struct{})
	for _, value := range strings.Split(allowedHosts, ",") {
		if host := strings.ToLower(strings.TrimSpace(value)); host != "" {
			allowed[host] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		host := requestHostname(c.Request.Host)
		if _, ok := allowed[host]; !ok {
			httpx.AbortJSON(c, http.StatusForbidden, "Forbidden")
			return
		}
		c.Next()
	}
}

func requestHostname(host string) string {
	host = strings.TrimSpace(host)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
