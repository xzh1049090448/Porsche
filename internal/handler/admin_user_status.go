package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type adminUserStatusBackend interface {
	Change(context.Context, service.AdminPermissionReadActor, int64, service.AdminUserStatusInput) (*service.UserReadDTO, error)
}

func RegisterAdminUserStatus(router *gin.Engine, state *app.State) {
	group := router.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, middleware.RequireUserWithErrorWriter(state, func(c *gin.Context, _ string) {
		adminUserStatusFixedError(c, http.StatusUnauthorized, "authentication_invalid")
	}))
	registerAdminUserStatusRoute(group, service.NewAdminUserStatusService(state.DB, state.AuthRedis))
}

func registerAdminUserStatusRoute(group *gin.RouterGroup, backend adminUserStatusBackend) {
	group.PATCH("/users/:guid/status", func(c *gin.Context) {
		guid, err := service.ParseAdminPermissionGUID(c.Param("guid"))
		if err != nil || c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" || !adminUserEditJSONContentType(c.GetHeader("Content-Type")) {
			adminUserStatusFixedError(c, http.StatusBadRequest, "invalid_admin_user_status_request")
			return
		}
		request, err := dto.DecodeAdminUserStatus(c.Request.Body)
		if err != nil {
			if errors.Is(err, dto.ErrAdminUserStatusBodyTooLarge) {
				adminUserStatusFixedError(c, http.StatusRequestEntityTooLarge, "request_body_too_large")
			} else {
				adminUserStatusFixedError(c, http.StatusBadRequest, "invalid_admin_user_status_request")
			}
			return
		}
		status, ok := models.ParseUserStatus(request.Status)
		if !ok || backend == nil {
			if !ok {
				adminUserStatusFixedError(c, http.StatusBadRequest, "invalid_admin_user_status_request")
			} else {
				adminUserStatusFixedError(c, http.StatusServiceUnavailable, "user_status_dependency_unavailable")
			}
			return
		}
		result, err := backend.Change(c.Request.Context(), adminAuthzActor(c), guid, service.AdminUserStatusInput{Status: status, Reason: request.Reason, ExpectedAuthVersion: request.ExpectedAuthVersion})
		if err != nil {
			adminUserStatusServiceError(c, err)
			return
		}
		if result == nil {
			adminUserStatusFixedError(c, http.StatusServiceUnavailable, "user_status_dependency_unavailable")
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

func adminUserStatusServiceError(c *gin.Context, err error) {
	status, _ := service.StatusFromError(err)
	code := "user_status_dependency_unavailable"
	switch status {
	case http.StatusBadRequest:
		code = "invalid_admin_user_status_request"
	case http.StatusUnauthorized:
		code = "authentication_invalid"
	case http.StatusForbidden:
		code = "user_status_forbidden"
	case http.StatusNotFound:
		code = "user_not_found"
	case http.StatusConflict:
		code = service.AdminUserStatusConflictCode(err)
		if code == "" {
			status, code = http.StatusServiceUnavailable, "user_status_dependency_unavailable"
		}
	default:
		status = http.StatusServiceUnavailable
	}
	adminUserStatusFixedError(c, status, code)
}

func adminUserStatusFixedError(c *gin.Context, status int, code string) {
	requestID := c.Writer.Header().Get("X-Request-ID")
	if requestID == "" {
		requestID = "unavailable"
		c.Header("X-Request-ID", requestID)
	}
	c.AbortWithStatusJSON(status, adminUserEditErrorEnvelope{Error: adminUserEditErrorBody{Code: code, Message: "请求无法完成", Kind: "admin_user_status_error", RequestID: requestID}})
}
