package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func RegisterAuth(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/auth")
	passwordOnlyMsg := "当前仅支持固定账号密码登录"

	rejectPasswordOnly := func(c *gin.Context) bool {
		if state.Settings.FixedLoginEnabled {
			httpx.AbortJSON(c, http.StatusForbidden, passwordOnlyMsg)
			return false
		}
		return true
	}

	g.POST("/send-code", func(c *gin.Context) {
		if !rejectPasswordOnly(c) {
			return
		}
		var body struct {
			Phone string `json:"phone" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		ip := httpx.ClientIP(c, state.Settings.TrustProxyHeaders)
		if err := state.SMS.CheckSendAllowed(body.Phone, ip); err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		code := state.SMS.SendCode(body.Phone)
		resp := gin.H{"message": "验证码已发送"}
		if state.Settings.SMSDevMode {
			resp["dev_code"] = code
		}
		c.JSON(http.StatusOK, resp)
	})

	g.POST("/register", func(c *gin.Context) {
		if !rejectPasswordOnly(c) {
			return
		}
		var body struct {
			Phone    string  `json:"phone" binding:"required"`
			Code     string  `json:"code" binding:"required"`
			Password string  `json:"password" binding:"required"`
			Nickname *string `json:"nickname"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		user, token, err := state.Auth.Register(body.Phone, body.Code, body.Password, body.Nickname)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		uid := user.ID
		_ = state.Audit.Log(state.DB, "user.register", &uid, "", nil, httpx.ClientIP(c, state.Settings.TrustProxyHeaders))
		c.JSON(http.StatusOK, gin.H{
			"access_token": token,
			"token_type":   "bearer",
			"user_id":      user.ID,
			"plan_type":    string(user.PlanType),
		})
	})

	g.POST("/login/password", func(c *gin.Context) {
		var body struct {
			Phone    string `json:"phone" binding:"required"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		user, token, err := state.Auth.LoginPassword(body.Phone, body.Password)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		uid := user.ID
		_ = state.Audit.Log(state.DB, "user.login", &uid, "", map[string]interface{}{"method": "password"}, httpx.ClientIP(c, state.Settings.TrustProxyHeaders))
		c.JSON(http.StatusOK, gin.H{
			"access_token": token,
			"token_type":   "bearer",
			"user_id":      user.ID,
			"plan_type":    string(user.PlanType),
		})
	})

	g.POST("/login/code", func(c *gin.Context) {
		if !rejectPasswordOnly(c) {
			return
		}
		var body struct {
			Phone string `json:"phone" binding:"required"`
			Code  string `json:"code" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		user, token, err := state.Auth.LoginCode(body.Phone, body.Code)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		uid := user.ID
		_ = state.Audit.Log(state.DB, "user.login", &uid, "", map[string]interface{}{"method": "code"}, httpx.ClientIP(c, state.Settings.TrustProxyHeaders))
		c.JSON(http.StatusOK, gin.H{
			"access_token": token,
			"token_type":   "bearer",
			"user_id":      user.ID,
			"plan_type":    string(user.PlanType),
		})
	})
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
			user.Nickname = body.Nickname
			_ = state.DB.Save(user).Error
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
		if user.PasswordHash == nil || !serviceVerifyPassword(body.OldPassword, *user.PasswordHash) {
			httpx.AbortJSON(c, http.StatusBadRequest, "原密码错误")
			return
		}
		hash, _ := serviceHashPassword(body.NewPassword)
		user.PasswordHash = &hash
		_ = state.DB.Save(user).Error
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
		user.RealName = &body.RealName
		hash := service.HashIDCard(body.IDCard)
		user.IDCardHash = &hash
		user.IsVerified = true
		_ = state.DB.Save(user).Error
		c.JSON(http.StatusOK, gin.H{"message": "实名认证成功", "is_verified": true})
	})
	g.GET("/me/usage", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		c.JSON(http.StatusOK, service.GetUsageStats(user))
	})
}
