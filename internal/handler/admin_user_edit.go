package handler

import (
	"context"
	"errors"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type adminUserEditBackend interface {
	Edit(context.Context, service.AdminPermissionReadActor, int64, service.AdminUserNicknameEditInput) (*service.UserReadDTO, error)
}

// RegisterAdminUserEdit publishes only the A05 nickname edit endpoint. It is
// deliberately separate from both the legacy PUT writer and action-ticket
// routes.
func RegisterAdminUserEdit(router *gin.Engine, state *app.State) {
	group := router.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, middleware.RequireUserWithErrorWriter(state, func(c *gin.Context, _ string) {
		adminUserEditFixedError(c, http.StatusUnauthorized, "authentication_invalid")
	}))
	registerAdminUserEditRoute(group, service.NewAdminUserNicknameEditService(state.DB, state.AuthRedis))
}

func registerAdminUserEditRoute(group *gin.RouterGroup, backend adminUserEditBackend) {
	group.PATCH("/users/:guid", func(c *gin.Context) {
		guid, err := service.ParseAdminPermissionGUID(c.Param("guid"))
		if err != nil || c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" || !adminUserEditJSONContentType(c.GetHeader("Content-Type")) {
			adminUserEditFixedError(c, http.StatusBadRequest, "invalid_admin_user_edit_request")
			return
		}
		request, err := dto.DecodeAdminUserEdit(c.Request.Body)
		if err != nil {
			if errors.Is(err, dto.ErrAdminUserEditBodyTooLarge) {
				adminUserEditFixedError(c, http.StatusRequestEntityTooLarge, "request_body_too_large")
			} else {
				adminUserEditFixedError(c, http.StatusBadRequest, "invalid_admin_user_edit_request")
			}
			return
		}
		if backend == nil {
			adminUserEditFixedError(c, http.StatusServiceUnavailable, "user_edit_dependency_unavailable")
			return
		}
		result, err := backend.Edit(c.Request.Context(), adminAuthzActor(c), guid, service.AdminUserNicknameEditInput{
			Nickname: request.Nickname, ClearNickname: request.ClearNickname, ExpectedAuthVersion: request.ExpectedAuthVersion,
		})
		if err != nil {
			adminUserEditServiceError(c, err)
			return
		}
		if result == nil {
			adminUserEditFixedError(c, http.StatusServiceUnavailable, "user_edit_dependency_unavailable")
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

func adminUserEditJSONContentType(raw string) bool {
	mediaType, _, err := mime.ParseMediaType(raw)
	return err == nil && mediaType == "application/json"
}

type adminUserEditErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Kind      string `json:"kind"`
	RequestID string `json:"request_id"`
}

type adminUserEditErrorEnvelope struct {
	Error adminUserEditErrorBody `json:"error"`
}

func adminUserEditServiceError(c *gin.Context, err error) {
	status, _ := service.StatusFromError(err)
	code := "user_edit_dependency_unavailable"
	switch status {
	case http.StatusBadRequest:
		code = "invalid_admin_user_edit_request"
	case http.StatusUnauthorized:
		code = "authentication_invalid"
	case http.StatusForbidden:
		code = "user_edit_forbidden"
	case http.StatusNotFound:
		code = "user_not_found"
	case http.StatusConflict:
		code = "auth_version_conflict"
	default:
		status = http.StatusServiceUnavailable
	}
	adminUserEditFixedError(c, status, code)
}

func adminUserEditFixedError(c *gin.Context, status int, code string) {
	requestID := c.Writer.Header().Get("X-Request-ID")
	if requestID == "" {
		requestID = "unavailable"
		c.Header("X-Request-ID", requestID)
	}
	c.AbortWithStatusJSON(status, adminUserEditErrorEnvelope{Error: adminUserEditErrorBody{
		Code: code, Message: "请求无法完成", Kind: "admin_user_edit_error", RequestID: requestID,
	}})
}
