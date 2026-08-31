package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func RegisterAuth(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/auth", gatewayRequestID())
	// The phone/SMS and fixed-account authentication surface was retired. Keep
	// explicit 410 responses for deployed clients instead of silently routing a
	// legacy credential into the username/session authentication flow.
	for _, path := range []string{"/send-code", "/login/password", "/login/code"} {
		g.POST(path, func(c *gin.Context) {
			httpx.AbortJSON(c, http.StatusGone, "该认证方式已停用")
		})
	}
	// Username registration never creates a session or returns credentials.
	g.POST("/register", func(c *gin.Context) {
		if !authReady(c, state) {
			return
		}
		var body struct {
			Username string  `json:"username" binding:"required"`
			Password string  `json:"password" binding:"required"`
			Nickname *string `json:"nickname"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			authAbort(c, http.StatusBadRequest, "auth_invalid_request", "请求格式无效")
			return
		}
		user, err := state.Auth.RegisterUsername(c.Request.Context(), body.Username, body.Password, body.Nickname)
		if err != nil {
			authServiceError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"user": dto.AuthUser(user)})
	})
	// Login exchanges a username/password for a short Access token and a
	// HttpOnly refresh cookie. Refresh plaintext stays inside this function.
	g.POST("/login", func(c *gin.Context) {
		if !authReady(c, state) {
			return
		}
		var body struct {
			Username string `json:"username" binding:"required"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			authAbort(c, http.StatusBadRequest, "auth_invalid_request", "请求格式无效")
			return
		}
		user, issued, access, err := state.Auth.LoginUsername(c.Request.Context(), body.Username, body.Password, service.SessionCreateInput{LoginMethod: models.LoginMethodPassword, IP: httpx.ClientIP(c, state.Settings.TrustProxyHeaders, state.Settings.TrustedProxyCIDRs), UserAgent: c.GetHeader("User-Agent")})
		if err != nil {
			authAbort(c, http.StatusUnauthorized, "auth_invalid_credentials", "用户名或密码错误")
			return
		}
		setRefreshCookie(c, issued.RefreshToken, state.Settings.SessionDays)
		c.JSON(http.StatusOK, gin.H{"access_token": access, "token_type": "Bearer", "expires_in": state.Settings.SessionAccessMinutes * 60, "user": dto.AuthUser(user)})
	})
	// Refresh accepts only the browser cookie from a configured same-site origin.
	g.POST("/refresh", func(c *gin.Context) {
		if !authReady(c, state) || !requireTrustedOrigin(c, state) {
			return
		}
		refresh, ok := refreshCookie(c)
		if !ok || !requireSessionHeader(c, refresh) {
			authAbort(c, http.StatusUnauthorized, "auth_invalid_refresh", "刷新凭据无效")
			return
		}
		issued, err := state.Sessions.Refresh(c.Request.Context(), refresh)
		if err != nil {
			authServiceError(c, err)
			return
		}
		var user models.User
		if err := state.DB.Where("id = ? AND is_deleted = 0", issued.Session.UserID).First(&user).Error; err != nil || !user.Status.IsActive() {
			authAbort(c, http.StatusUnauthorized, "auth_invalid_refresh", "刷新凭据无效")
			return
		}
		access, err := state.Auth.IssueAccessToken(&user, issued.Session)
		if err != nil {
			authAbort(c, http.StatusUnauthorized, "auth_invalid_refresh", "刷新凭据无效")
			return
		}
		setRefreshCookie(c, issued.RefreshToken, state.Settings.SessionDays)
		c.JSON(http.StatusOK, gin.H{"access_token": access, "token_type": "Bearer", "expires_in": state.Settings.SessionAccessMinutes * 60, "user": dto.AuthUser(&user)})
	})
	protected := g.Group("", middleware.RequireUser(state))
	protected.POST("/logout", func(c *gin.Context) {
		if !requireTrustedOrigin(c, state) {
			return
		}
		refresh, ok := refreshCookie(c)
		if !ok || !requireSessionHeader(c, refresh) || !strings.HasPrefix(refresh, middleware.CurrentSessionSID(c)+".") {
			authAbort(c, http.StatusUnauthorized, "auth_invalid_refresh", "刷新凭据无效")
			return
		}
		sessions, err := state.Sessions.List(c.Request.Context(), middleware.CurrentUserID(c), middleware.CurrentUser(c).AuthVersion)
		if err != nil {
			authServiceError(c, err)
			return
		}
		for _, session := range sessions {
			if session.SID == middleware.CurrentSessionSID(c) {
				if err := state.Sessions.Revoke(c.Request.Context(), middleware.CurrentUserID(c), session.Guid); err != nil {
					authServiceError(c, err)
					return
				}
				break
			}
		}
		clearRefreshCookie(c)
		c.Status(http.StatusNoContent)
	})
	protected.GET("/self", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"user": dto.AuthUser(middleware.CurrentUser(c))}) })
	protected.GET("/sessions", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		sessions, err := state.Sessions.List(c.Request.Context(), user.ID, user.AuthVersion)
		if err != nil {
			authServiceError(c, err)
			return
		}
		out := make([]map[string]interface{}, 0, len(sessions))
		for i := range sessions {
			out = append(out, dto.AuthSession(&sessions[i], sessions[i].SID == middleware.CurrentSessionSID(c)))
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	})
	protected.DELETE("/sessions/:guid", func(c *gin.Context) {
		guid, err := strconv.ParseInt(c.Param("guid"), 10, 64)
		if err != nil || guid <= 0 {
			authAbort(c, http.StatusBadRequest, "auth_invalid_request", "会话标识无效")
			return
		}
		user := middleware.CurrentUser(c)
		sessions, err := state.Sessions.List(c.Request.Context(), user.ID, user.AuthVersion)
		if err != nil {
			authServiceError(c, err)
			return
		}
		current := false
		for _, session := range sessions {
			if session.Guid == guid {
				current = session.SID == middleware.CurrentSessionSID(c)
				break
			}
		}
		if err := state.Sessions.Revoke(c.Request.Context(), user.ID, guid); err != nil {
			authServiceError(c, err)
			return
		}
		if current {
			clearRefreshCookie(c)
		}
		c.Status(http.StatusNoContent)
	})
	protected.POST("/sessions/revoke-others", func(c *gin.Context) {
		if err := state.Sessions.RevokeOthers(c.Request.Context(), middleware.CurrentUserID(c), middleware.CurrentSessionSID(c)); err != nil {
			authServiceError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	protected.POST("/self/password", func(c *gin.Context) {
		var body struct {
			OldPassword string `json:"old_password" binding:"required"`
			NewPassword string `json:"new_password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			authAbort(c, http.StatusBadRequest, "auth_invalid_request", "请求格式无效")
			return
		}
		if err := state.Auth.ChangePassword(c.Request.Context(), middleware.CurrentUserID(c), body.OldPassword, body.NewPassword); err != nil {
			authServiceError(c, err)
			return
		}
		clearRefreshCookie(c)
		c.Status(http.StatusNoContent)
	})
	protected.POST("/self/verify", func(c *gin.Context) {
		var body struct {
			RealName string `json:"real_name" binding:"required"`
			IDCard   string `json:"id_card" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || !isValidIDCard(body.IDCard) {
			authAbort(c, http.StatusBadRequest, "auth_invalid_request", "请求格式无效")
			return
		}
		if !state.Settings.RealNameAutoVerify {
			authAbort(c, http.StatusNotImplemented, "auth_verification_unavailable", "实名认证暂不可用")
			return
		}
		if err := state.Auth.VerifyOwnIdentity(c.Request.Context(), middleware.CurrentUserID(c), body.RealName, body.IDCard); err != nil {
			authServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"is_verified": true})
	})
}

// authReady prevents a partially initialized server from accepting a request
// that could otherwise degrade into a non-revocable authentication path.
func authReady(c *gin.Context, state *app.State) bool {
	if state == nil || state.Settings == nil || state.DB == nil || state.Auth == nil || state.Sessions == nil {
		authAbort(c, http.StatusServiceUnavailable, "auth_unavailable", "认证服务暂不可用")
		return false
	}
	return true
}

func authAbort(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message, "type": "authentication_error", "request_id": c.Writer.Header().Get("X-Request-ID")}})
}
func authServiceError(c *gin.Context, err error) {
	status, _ := service.StatusFromError(err)
	if status == http.StatusInternalServerError {
		authAbort(c, http.StatusServiceUnavailable, "auth_unavailable", "认证服务暂不可用")
		return
	}
	authAbort(c, status, "auth_request_failed", "认证请求失败")
}

func requireTrustedOrigin(c *gin.Context, state *app.State) bool {
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin == "" {
		authAbort(c, http.StatusForbidden, "auth_origin_denied", "来源不受信任")
		return false
	}
	for _, allowed := range state.Settings.AuthTrustedOrigins {
		if origin == allowed {
			return true
		}
	}
	authAbort(c, http.StatusForbidden, "auth_origin_denied", "来源不受信任")
	return false
}
func refreshCookie(c *gin.Context) (string, bool) {
	cookie, err := c.Request.Cookie("porsche_refresh")
	return func() string {
		if err != nil {
			return ""
		}
		return cookie.Value
	}(), err == nil && cookie.Value != ""
}
func requireSessionHeader(c *gin.Context, refresh string) bool {
	header := strings.TrimSpace(c.GetHeader("X-Auth-Session"))
	if header == "" {
		return true
	}
	sid, _, found := strings.Cut(refresh, ".")
	return found && header == sid
}
func setRefreshCookie(c *gin.Context, value string, days int) {
	http.SetCookie(c.Writer, &http.Cookie{Name: "porsche_refresh", Value: value, Path: "/api/v1/auth", MaxAge: int((time.Duration(days) * 24 * time.Hour).Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}
func clearRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: "porsche_refresh", Value: "", Path: "/api/v1/auth", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func RegisterUsers(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/users", middleware.RequireUser(state))
	g.GET("/me", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		c.JSON(http.StatusOK, dto.UserProfile(user))
	})
	g.PUT("/me", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body struct {
			Nickname *string `json:"nickname"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Nickname != nil {
			updated, err := state.Auth.UpdateOwnProfile(c.Request.Context(), user.ID, body.Nickname)
			if err != nil {
				code, message := service.StatusFromError(err)
				httpx.AbortJSON(c, code, message)
				return
			}
			user = updated
			if user == nil {
				httpx.AbortJSON(c, http.StatusInternalServerError, "更新用户失败")
				return
			}
		}
		c.JSON(http.StatusOK, dto.UserProfile(user))
	})
	g.POST("/me/password", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body struct {
			OldPassword string `json:"old_password" binding:"required"`
			NewPassword string `json:"new_password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := state.Auth.ChangePassword(c.Request.Context(), user.ID, body.OldPassword, body.NewPassword); err != nil {
			code, message := service.StatusFromError(err)
			httpx.AbortJSON(c, code, message)
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "密码修改成功"})
	})
	g.POST("/me/verify", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body struct {
			RealName string `json:"real_name" binding:"required"`
			IDCard   string `json:"id_card" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if !isValidIDCard(body.IDCard) {
			httpx.AbortJSON(c, http.StatusBadRequest, "身份证号格式无效")
			return
		}
		if !state.Settings.RealNameAutoVerify {
			httpx.AbortJSON(c, http.StatusNotImplemented, "实名认证需对接第三方核验服务，暂未开通")
			return
		}
		if err := state.Auth.VerifyOwnIdentity(c.Request.Context(), user.ID, body.RealName, body.IDCard); err != nil {
			code, message := service.StatusFromError(err)
			httpx.AbortJSON(c, code, message)
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "实名认证成功", "is_verified": true})
	})
	g.GET("/me/usage", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		c.JSON(http.StatusOK, service.GetUsageStats(user))
	})
}
