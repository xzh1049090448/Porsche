package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type adminUserEntitlementBackend interface {
	ChangeGroup(context.Context, service.AdminPermissionReadActor, int64, service.AdminUserGroupChangeInput) (*service.UserReadDTO, error)
	ChangePlan(context.Context, service.AdminPermissionReadActor, int64, service.AdminUserPlanChangeInput) (*service.UserReadDTO, error)
}

func RegisterAdminUserEntitlements(router *gin.Engine, state *app.State) {
	group := router.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, middleware.RequireUser(state))
	registerAdminUserEntitlementRoutes(group, service.NewAdminUserEntitlementService(state.DB, state.AuthRedis))
}

func registerAdminUserEntitlementRoutes(group *gin.RouterGroup, backend adminUserEntitlementBackend) {
	group.PATCH("/users/:guid/group", func(c *gin.Context) {
		guid, ok := validAdminUserEntitlementRequest(c)
		if !ok {
			return
		}
		request, err := dto.DecodeAdminUserGroupChange(c.Request.Body)
		if err != nil {
			adminUserEntitlementDecodeError(c, err)
			return
		}
		if backend == nil {
			adminUserEntitlementFixedError(c, http.StatusServiceUnavailable, "user_entitlement_dependency_unavailable")
			return
		}
		result, err := backend.ChangeGroup(c.Request.Context(), adminAuthzActor(c), guid, service.AdminUserGroupChangeInput{GroupGUID: request.GroupGUID, Reason: request.Reason, ExpectedAuthVersion: request.ExpectedAuthVersion, RequestID: adminUserEntitlementRequestID(c)})
		if err != nil {
			adminUserEntitlementServiceError(c, err)
			return
		}
		if result == nil {
			adminUserEntitlementFixedError(c, http.StatusServiceUnavailable, "user_entitlement_dependency_unavailable")
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.PATCH("/users/:guid/plan", func(c *gin.Context) {
		guid, ok := validAdminUserEntitlementRequest(c)
		if !ok {
			return
		}
		request, err := dto.DecodeAdminUserPlanChange(c.Request.Body)
		if err != nil {
			adminUserEntitlementDecodeError(c, err)
			return
		}
		plan, valid := models.ParsePlanType(request.PlanType)
		if !valid {
			adminUserEntitlementFixedError(c, http.StatusBadRequest, "invalid_admin_user_entitlement_request")
			return
		}
		if backend == nil {
			adminUserEntitlementFixedError(c, http.StatusServiceUnavailable, "user_entitlement_dependency_unavailable")
			return
		}
		result, err := backend.ChangePlan(c.Request.Context(), adminAuthzActor(c), guid, service.AdminUserPlanChangeInput{PlanType: plan, Reason: request.Reason, ExpectedAuthVersion: request.ExpectedAuthVersion, RequestID: adminUserEntitlementRequestID(c)})
		if err != nil {
			adminUserEntitlementServiceError(c, err)
			return
		}
		if result == nil {
			adminUserEntitlementFixedError(c, http.StatusServiceUnavailable, "user_entitlement_dependency_unavailable")
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

func validAdminUserEntitlementRequest(c *gin.Context) (int64, bool) {
	guid, err := service.ParseAdminPermissionGUID(c.Param("guid"))
	if err != nil || c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" || !adminUserEditJSONContentType(c.GetHeader("Content-Type")) {
		adminUserEntitlementFixedError(c, http.StatusBadRequest, "invalid_admin_user_entitlement_request")
		return 0, false
	}
	return guid, true
}

func adminUserEntitlementDecodeError(c *gin.Context, err error) {
	if errors.Is(err, dto.ErrAdminUserEntitlementBodyTooLarge) {
		adminUserEntitlementFixedError(c, http.StatusRequestEntityTooLarge, "request_body_too_large")
	} else {
		adminUserEntitlementFixedError(c, http.StatusBadRequest, "invalid_admin_user_entitlement_request")
	}
}

func adminUserEntitlementServiceError(c *gin.Context, err error) {
	status, _ := service.StatusFromError(err)
	code := "user_entitlement_dependency_unavailable"
	switch status {
	case http.StatusBadRequest:
		code = "invalid_admin_user_entitlement_request"
	case http.StatusUnauthorized:
		httpx.AbortJSON(c, http.StatusUnauthorized, "认证会话不可用")
		return
	case http.StatusForbidden:
		code = "user_entitlement_forbidden"
	case http.StatusNotFound:
		code = service.AdminUserEntitlementNotFoundCode(err)
		if code == "" {
			status, code = http.StatusServiceUnavailable, "user_entitlement_dependency_unavailable"
		}
	case http.StatusConflict:
		code = service.AdminUserEntitlementConflictCode(err)
		if code == "" {
			status, code = http.StatusServiceUnavailable, "user_entitlement_dependency_unavailable"
		}
	default:
		status = http.StatusServiceUnavailable
	}
	adminUserEntitlementFixedError(c, status, code)
}

func adminUserEntitlementRequestID(c *gin.Context) string {
	if value := c.Writer.Header().Get("X-Request-ID"); value != "" {
		return value
	}
	return "unavailable"
}

func adminUserEntitlementFixedError(c *gin.Context, status int, code string) {
	requestID := adminUserEntitlementRequestID(c)
	if requestID == "unavailable" {
		c.Header("X-Request-ID", requestID)
	}
	c.AbortWithStatusJSON(status, adminUserEditErrorEnvelope{Error: adminUserEditErrorBody{Code: code, Message: "请求无法完成", Kind: "admin_user_entitlement_error", RequestID: requestID}})
}
