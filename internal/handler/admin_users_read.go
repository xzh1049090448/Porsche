package handler

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func adminUsersReadHeaders(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() }
func RegisterAdminUsersRead(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/v2/users", gatewayRequestID(), adminUsersReadHeaders, middleware.RequireUser(state))
	g.GET("", func(c *gin.Context) {
		q, err := service.ParseAdminUsersReadQuery(c.Request.URL.RawQuery)
		if err != nil {
			adminUsersReadError(c, err)
			return
		}
		out, err := service.NewAdminUsersReadService(state.DB, state.AuthRedis).List(c.Request.Context(), adminAuthzActor(c), q)
		if err != nil {
			adminUsersReadError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/:guid", func(c *gin.Context) {
		guid, err := service.ParseAdminPermissionGUID(c.Param("guid"))
		if err != nil || c.Request.URL.RawQuery != "" {
			adminUsersReadError(c, service.ErrAdminPermissionInvalid)
			return
		}
		out, err := service.NewAdminUsersReadService(state.DB, state.AuthRedis).Detail(c.Request.Context(), adminAuthzActor(c), guid)
		if err != nil {
			adminUsersReadError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
}
func registerLegacyAdminUsersRead(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/users", gatewayRequestID(), adminUsersReadHeaders, middleware.RequireUser(state))
	g.GET("", func(c *gin.Context) {
		skip, limit, err := service.ParseLegacyUsersPagination(c.Query("skip"), c.Query("limit"))
		if err != nil {
			adminUsersReadError(c, err)
			return
		}
		status := c.Query("status")
		if status != "" {
			if _, ok := models.ParseUserStatus(status); !ok {
				httpx.AbortJSON(c, http.StatusUnprocessableEntity, "无效用户状态")
				return
			}
		}
		users, err := service.NewAdminUsersReadService(state.DB, state.AuthRedis).LegacyList(c.Request.Context(), adminAuthzActor(c), skip, limit, status)
		if err != nil {
			adminUsersReadError(c, err)
			return
		}
		out := make([]map[string]interface{}, 0, len(users))
		for i := range users {
			out = append(out, dto.AdminUser(&users[i]))
		}
		c.JSON(http.StatusOK, out)
	})
	g.GET("/:guid", func(c *gin.Context) {
		guid, err := legacyUserReadGUID(c.Param("guid"))
		if err != nil {
			adminUsersReadError(c, err)
			return
		}
		out, err := service.NewAdminUsersReadService(state.DB, state.AuthRedis).LegacyDetail(c.Request.Context(), adminAuthzActor(c), guid)
		if err != nil {
			adminUsersReadError(c, err)
			return
		}
		c.JSON(http.StatusOK, dto.AdminUser(out))
	})
	g.GET("/:guid/behavior", func(c *gin.Context) {
		guid, err := legacyUserReadGUID(c.Param("guid"))
		if err != nil {
			adminUsersReadError(c, err)
			return
		}
		out, err := service.NewAdminUsersReadService(state.DB, state.AuthRedis).LegacyBehavior(c.Request.Context(), adminAuthzActor(c), guid)
		if err != nil {
			adminUsersReadError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	})
}
func legacyUserReadGUID(raw string) (int64, error) {
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 || v > math.MaxInt64 {
		return 0, service.ErrAdminPermissionHidden
	}
	return int64(v), nil
}
func adminUsersReadError(c *gin.Context, err error) {
	status, message := http.StatusServiceUnavailable, "用户信息暂不可用"
	switch {
	case errors.Is(err, service.ErrAdminPermissionUnauthenticated):
		status, message = http.StatusUnauthorized, "认证会话无效"
	case errors.Is(err, service.ErrAdminUsersGroupUnsupported):
		status, message = http.StatusBadRequest, "分组筛选尚未接入"
	case errors.Is(err, service.ErrAdminPermissionInvalid):
		status, message = http.StatusBadRequest, "无效请求参数"
	case errors.Is(err, service.ErrAdminPermissionDenied):
		status, message = http.StatusForbidden, "无权限访问"
	case errors.Is(err, service.ErrAdminPermissionHidden):
		status, message = http.StatusNotFound, "用户不存在"
	}
	httpx.AbortJSON(c, status, message)
}
