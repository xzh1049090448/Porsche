package httpx

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func ClientIP(c *gin.Context, trustProxy bool, trustedProxyCIDRs ...string) string {
	if trustProxy && remoteIsTrustedProxy(c.Request.RemoteAddr, trustedProxyCIDRs) {
		if fwd := c.GetHeader("X-Forwarded-For"); fwd != "" {
			return strings.TrimSpace(strings.Split(fwd, ",")[0])
		}
	}
	return directRemoteIP(c.Request.RemoteAddr)
}

func directRemoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(remoteAddr)
}

func remoteIsTrustedProxy(remoteAddr string, configured []string) bool {
	if len(configured) == 0 || strings.TrimSpace(configured[0]) == "" {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	for _, raw := range strings.Split(configured[0], ",") {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func AbortJSON(c *gin.Context, code int, detail string) {
	c.AbortWithStatusJSON(code, gin.H{"detail": detail})
}

func BearerToken(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	if auth == "" || !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return ""
	}
	return strings.TrimSpace(auth[7:])
}

func RequireBearer(c *gin.Context) (string, bool) {
	token := BearerToken(c)
	if token == "" {
		AbortJSON(c, http.StatusUnauthorized, "未登录")
		return "", false
	}
	return token, true
}
