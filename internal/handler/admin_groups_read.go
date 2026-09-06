package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const activeAdminGroupsQuery = "status=active"

func RegisterAdminGroupsRead(router *gin.Engine, state *app.State) {
	group := router.Group("/admin/v2/groups", gatewayRequestID(), adminUserActionNoStore, middleware.RequireUser(state))
	group.GET("", func(context *gin.Context) {
		if context.Request.URL.RawQuery != activeAdminGroupsQuery || context.Request.URL.RawPath != "" {
			adminUserActionFixedError(context, http.StatusBadRequest, "invalid_admin_action_request", "")
			return
		}
		result, err := service.NewAdminUsersReadService(state.DB, state.AuthRedis).ActiveGroups(context.Request.Context(), adminAuthzActor(context))
		if err != nil {
			adminGroupsReadError(context, err)
			return
		}
		context.JSON(http.StatusOK, result)
	})
}

func adminGroupsReadError(context *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAdminPermissionUnauthenticated):
		adminUsersReadError(context, err)
	case errors.Is(err, service.ErrAdminPermissionDenied):
		adminUserActionFixedError(context, http.StatusForbidden, "action_operation_rejected", "")
	default:
		adminUserActionFixedError(context, http.StatusServiceUnavailable, "action_dependency_unavailable", "")
	}
}
