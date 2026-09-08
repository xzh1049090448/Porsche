package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func withAuthPermissions(body map[string]interface{}, fresh *service.FreshAuthProjection) map[string]interface{} {
	// A policy failure omits both hints; never synthesize empty authority/version.
	if fresh != nil && fresh.Permissions != nil {
		body["admin_permissions"] = fresh.Permissions.AdminPermissions
		body["permissions_version"] = fresh.Permissions.PermissionsVersion
	}
	return body
}
func authProjectionError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrAdminPermissionUnauthenticated) {
		authAbort(c, http.StatusUnauthorized, "auth_session_invalid", "认证会话无效")
		return
	}
	authAbort(c, http.StatusServiceUnavailable, "auth_unavailable", "用户信息暂不可用")
}

// Issuance already committed. Failed freshness must not expose a new token or
// set/clear a cookie; callers retain existing rotation recovery semantics.
func respondIssuedAuth(c *gin.Context, state *app.State, issued *service.IssuedSession, access string, fresh *service.FreshAuthProjection, err error) {
	if err != nil {
		authProjectionError(c, err)
		return
	}
	if fresh == nil || issued == nil || state == nil || state.Settings == nil || access == "" {
		authProjectionError(c, service.ErrAdminPermissionUnavailable)
		return
	}
	setRefreshCookie(c, issued.RefreshToken, state.Settings.SessionDays)
	c.JSON(http.StatusOK, gin.H{"access_token": access, "token_type": "Bearer", "expires_in": state.Settings.SessionAccessMinutes * 60, "user": withAuthPermissions(dto.AuthUser(&fresh.User), fresh)})
}
