package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
)

const (
	ContextUserID     = "user_id"
	ContextUser       = "user"
	ContextSessionSID = "session_sid"
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
		if !authenticateUser(c, state) {
			return
		}
		c.Next()
	}
}

func RequireUser(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authenticateUser(c, state) {
			return
		}
		c.Next()
	}
}

// RequireAdmin accepts only an authenticated server session whose persisted
// role is at least Admin; it deliberately has no ADMIN_TOKEN bypass.
func RequireAdmin(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authenticateUser(c, state) {
			return
		}
		if !hasMinimumRole(CurrentUser(c).Role, models.UserRoleAdmin) {
			httpx.AbortJSON(c, http.StatusForbidden, "无管理员权限")
			return
		}
		c.Next()
	}
}

// RequireRoot accepts only an authenticated Root session.
func RequireRoot(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authenticateUser(c, state) {
			return
		}
		if !hasMinimumRole(CurrentUser(c).Role, models.UserRoleRoot) {
			httpx.AbortJSON(c, http.StatusForbidden, "无Root权限")
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
		if !hasAnalyticsAccess(user) {
			httpx.AbortJSON(c, http.StatusForbidden, "无分析权限")
			return
		}
		c.Next()
	}
}

// hasAnalyticsAccess is the shared persistent-role authorization predicate
// for analytics UI access and analytics API enforcement. Phone configuration
// is deliberately not an authorization input.
func hasAnalyticsAccess(user *models.User) bool {
	return user != nil && hasMinimumRole(user.Role, models.UserRoleAdmin)
}

// HasAnalyticsAccess exposes the same role-only predicate to the HTTP access
// capability endpoint without duplicating authorization policy in handlers.
func HasAnalyticsAccess(user *models.User) bool { return hasAnalyticsAccess(user) }

func authenticateUser(c *gin.Context, state *app.State) bool {
	if state == nil || state.Settings == nil || state.DB == nil || state.Sessions == nil {
		httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
		return false
	}
	token, ok := httpx.RequireBearer(c)
	if !ok {
		return false
	}
	claims, err := security.DecodeAccessToken(token, state.Settings.JWTSecretKey)
	sessionClaims, ok := parseSessionClaims(claims)
	if err != nil || !ok {
		httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
		return false
	}
	var user models.User
	if err := state.DB.Where("guid = ? AND is_deleted = 0", sessionClaims.UserGUID).First(&user).Error; err != nil || !user.Status.IsActive() || user.AuthVersion != sessionClaims.AuthVersion || user.Role != sessionClaims.Role {
		httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
		return false
	}
	if _, err := state.Sessions.Validate(c.Request.Context(), sessionClaims.SID, user.ID, sessionClaims.SessionVersion, sessionClaims.AuthVersion); err != nil {
		httpx.AbortJSON(c, http.StatusUnauthorized, "Token无效或已过期")
		return false
	}
	c.Set(ContextUserID, user.ID)
	c.Set(ContextUser, &user)
	c.Set(ContextSessionSID, sessionClaims.SID)
	return true
}

// CurrentSessionSID returns the authenticated server session selector only for
// request-local authorization checks; handlers must never serialize it.
func CurrentSessionSID(c *gin.Context) string {
	v, _ := c.Get(ContextSessionSID)
	sid, _ := v.(string)
	return sid
}

func CurrentUser(c *gin.Context) *models.User {
	v, _ := c.Get(ContextUser)
	user, _ := v.(*models.User)
	return user
}

func CurrentUserID(c *gin.Context) int64 {
	v, _ := c.Get(ContextUserID)
	id, _ := v.(int64)
	return id
}
