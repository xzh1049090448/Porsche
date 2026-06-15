package httpx

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func ClientIP(c *gin.Context, trustProxy bool) string {
	if trustProxy {
		if fwd := c.GetHeader("X-Forwarded-For"); fwd != "" {
			return strings.TrimSpace(strings.Split(fwd, ",")[0])
		}
	}
	return c.ClientIP()
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
