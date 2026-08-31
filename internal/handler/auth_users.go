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
	// The phone/SMS and fixed-account authentication surface was retired. Keep
	// explicit 410 responses for deployed clients instead of silently routing a
	// legacy credential into the username/session authentication flow.
	for _, path := range []string{"/send-code", "/login/password", "/login/code"} {
		g.POST(path, func(c *gin.Context) {
			httpx.AbortJSON(c, http.StatusGone, "该认证方式已停用")
		})
	}
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
