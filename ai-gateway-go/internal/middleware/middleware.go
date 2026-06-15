package middleware

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
)

const (
	ContextUserID = "user_id"
	ContextUser   = "user"
)

func InjectState(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("app_state", state)
		c.Next()
	}
}

func GetState(c *gin.Context) *app.State {
	v, _ := c.Get("app_state")
	state, _ := v.(*app.State)
	return state
}

func RequireUserID(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := httpx.RequireBearer(c)
		if !ok {
			return
		}
		claims, err := security.DecodeAccessToken(token, state.Settings.JWTSecretKey)
		if err != nil || claims["sub"] == nil {
			httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
			return
		}
		sub, ok := claimSubject(claims)
		if !ok {
			httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
			return
		}
		id, err := strconv.ParseUint(sub, 10, 64)
		if err != nil {
			httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
			return
		}
		c.Set(ContextUserID, uint(id))
		c.Next()
	}
}

func RequireUser(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := httpx.RequireBearer(c)
		if !ok {
			return
		}
		claims, err := security.DecodeAccessToken(token, state.Settings.JWTSecretKey)
		if err != nil || claims["sub"] == nil {
			httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
			return
		}
		sub, ok := claimSubject(claims)
		if !ok {
			httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
			return
		}
		id, err := strconv.ParseUint(sub, 10, 64)
		if err != nil {
			httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
			return
		}
		var user models.User
		if err := state.DB.First(&user, id).Error; err != nil || user.Status != models.UserStatusActive {
			httpx.AbortJSON(c, http.StatusUnauthorized, "用户不存在或已被禁用")
			return
		}
		c.Set(ContextUserID, user.ID)
		c.Set(ContextUser, &user)
		c.Next()
	}
}

func RequireAdmin(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := httpx.BearerToken(c)
		if token == "" {
			httpx.AbortJSON(c, http.StatusUnauthorized, "Missing admin Authorization")
			return
		}
		if token != state.Settings.AdminToken {
			httpx.AbortJSON(c, http.StatusForbidden, "Forbidden")
			return
		}
		c.Next()
	}
}

func RequireAnalyticsAdmin(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		userVal, ok := c.Get(ContextUser)
		if !ok {
			httpx.AbortJSON(c, http.StatusUnauthorized, "未登录")
			return
		}
		user := userVal.(*models.User)
		if !state.Settings.IsAnalyticsAdmin(user.Phone) {
			httpx.AbortJSON(c, http.StatusForbidden, "无分析权限")
			return
		}
		c.Next()
	}
}

func CurrentUser(c *gin.Context) *models.User {
	v, _ := c.Get(ContextUser)
	user, _ := v.(*models.User)
	return user
}

func CurrentUserID(c *gin.Context) uint {
	v, _ := c.Get(ContextUserID)
	id, _ := v.(uint)
	return id
}
