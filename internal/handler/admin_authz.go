package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

// RegisterAdminAuthz registers display-only endpoints. It does not change any
// legacy writer's authorization or treat display hints as permission grants.
func RegisterAdminAuthz(r *gin.Engine, state *app.State) {
	g := r.Group("/admin/v2", gatewayRequestID(), func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() }, middleware.RequireUser(state))
	g.GET("/authz/catalog", func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" {
			adminAuthzError(c, service.ErrAdminPermissionInvalid)
			return
		}
		result, err := service.NewAdminPermissionReadService(state.DB, state.AuthRedis).Catalog(c.Request.Context(), adminAuthzActor(c))
		if err != nil {
			adminAuthzError(c, err)
			return
		}
		c.JSON(http.StatusOK, (*dto.AdminPermissionCatalog)(result))
	})
	g.GET("/users/:guid/permissions", func(c *gin.Context) {
		guid, err := service.ParseAdminPermissionGUID(c.Param("guid"))
		if err != nil || c.Request.URL.RawQuery != "" {
			adminAuthzError(c, service.ErrAdminPermissionInvalid)
			return
		}
		result, err := service.NewAdminPermissionReadService(state.DB, state.AuthRedis).Detail(c.Request.Context(), adminAuthzActor(c), guid)
		if err != nil {
			adminAuthzError(c, err)
			return
		}
		c.JSON(http.StatusOK, (*dto.AdminPermissionDetail)(result))
	})
}
func adminAuthzActor(c *gin.Context) service.AdminPermissionReadActor {
	user := middleware.CurrentUser(c)
	if user == nil {
		return service.AdminPermissionReadActor{}
	}
	return service.AdminPermissionReadActor{UserID: user.ID, AuthVersion: user.AuthVersion, SessionSID: middleware.CurrentSessionSID(c), SessionVersion: middleware.CurrentSessionVersion(c)}
}
func adminAuthzError(c *gin.Context, err error) {
	status, message := http.StatusServiceUnavailable, "权限信息暂不可用"
	switch {
	case errors.Is(err, service.ErrAdminPermissionUnauthenticated):
		status, message = http.StatusUnauthorized, "认证会话无效"
	case errors.Is(err, service.ErrAdminPermissionInvalid):
		status, message = http.StatusBadRequest, "无效请求参数"
	case errors.Is(err, service.ErrAdminPermissionDenied):
		status, message = http.StatusForbidden, "无权限访问"
	case errors.Is(err, service.ErrAdminPermissionHidden):
		status, message = http.StatusNotFound, "权限目标不存在"
	}
	httpx.AbortJSON(c, status, message)
}
