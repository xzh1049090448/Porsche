package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const (
	userPromoteScopeQuery          = "scope=users.promote"
	userDemoteScopeQuery           = "scope=users.demote"
	userPermissionsWriteScopeQuery = "scope=users.permissions.write"
)

type rolePermissionActionBackend interface {
	Issue(context.Context, service.VerificationIssue) (*service.IssuedVerification, error)
	Begin(context.Context, service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error)
	ExecutionReady(*service.OperationIdentity) bool
	NewPromoteExecution(actionsecurity.PromoteIntent) (*service.RolePermissionExecution, error)
	NewDemoteExecution(actionsecurity.DemoteIntent) (*service.RolePermissionExecution, error)
	NewPermissionsWriteExecution(actionsecurity.PermissionsWriteIntent) (*service.RolePermissionExecution, error)
	ExecuteRolePermission(context.Context, *service.OperationIdentity, *service.RolePermissionExecution) (*service.OperationView, error)
}

func issueRolePermissionVerification(c *gin.Context, backend userManagementActionBackend, settings *config.Settings, literal string, raw []byte) {
	roleBackend, ok := backend.(rolePermissionActionBackend)
	if !ok {
		adminUserActionError(c, service.ErrActionVerificationUnavailable, "")
		return
	}
	var action actionsecurity.Action
	var intent any
	var password []byte
	switch literal {
	case "users.promote":
		request, err := dto.DecodePromoteIssue(bytes.NewReader(raw))
		if err != nil {
			rolePermissionDecodeError(c, err)
			return
		}
		defer request.ClearSecrets()
		action, intent, password = actionsecurity.ActionUsersPromote, request.Intent, request.CurrentPassword
	case "users.demote":
		request, err := dto.DecodeDemoteIssue(bytes.NewReader(raw))
		if err != nil {
			rolePermissionDecodeError(c, err)
			return
		}
		defer request.ClearSecrets()
		action, intent, password = actionsecurity.ActionUsersDemote, request.Intent, request.CurrentPassword
	case "users.permissions.write":
		request, err := dto.DecodePermissionsWriteIssue(bytes.NewReader(raw))
		if err != nil {
			rolePermissionDecodeError(c, err)
			return
		}
		defer request.ClearSecrets()
		action, intent, password = actionsecurity.ActionUsersPermissionsWrite, request.Intent, request.CurrentPassword
	default:
		adminUserActionFixedError(c, http.StatusUnprocessableEntity, "action_inactive", "")
		return
	}
	target := rolePermissionTargetGUID(intent)
	trustedIP := ""
	if settings != nil {
		trustedIP = httpx.ClientIP(c, settings.TrustProxyHeaders, settings.TrustedProxyCIDRs)
	}
	issued, err := roleBackend.Issue(c.Request.Context(), service.VerificationIssue{Action: action, Actor: adminUserActionActor(c), TargetGUID: &target, Intent: intent, CurrentPassword: password, TrustedIP: trustedIP})
	if err != nil {
		adminUserActionError(c, err, "")
		return
	}
	if issued == nil || issued.ExpiresAt <= 0 {
		adminUserActionError(c, service.ErrActionVerificationUnavailable, "")
		return
	}
	if _, err := actionsecurity.ParseTicket([]string{issued.Ticket}); err != nil {
		adminUserActionError(c, service.ErrActionVerificationUnavailable, "")
		return
	}
	c.JSON(http.StatusCreated, dto.UserDeleteIssueResponse{Ticket: issued.Ticket, ExpiresAt: issued.ExpiresAt})
}

func executeRoleChange(c *gin.Context, backend userManagementActionBackend, literal string) {
	guid, keys, tickets, ok := rolePermissionMutationHeaders(c)
	if !ok {
		return
	}
	var action actionsecurity.Action
	var intent any
	switch literal {
	case "promote":
		request, err := dto.DecodePromoteExecute(c.Request.Body)
		if err != nil {
			rolePermissionDecodeError(c, err)
			return
		}
		action, intent = actionsecurity.ActionUsersPromote, request.Intent(guid)
	case "demote":
		request, err := dto.DecodeDemoteExecute(c.Request.Body)
		if err != nil {
			rolePermissionDecodeError(c, err)
			return
		}
		action, intent = actionsecurity.ActionUsersDemote, request.Intent(guid)
	default:
		adminUserActionFixedError(c, http.StatusUnprocessableEntity, "action_inactive", "")
		return
	}
	executeRolePermissionIntent(c, backend, action, intent, keys, tickets)
}

func executePermissionsWrite(c *gin.Context, backend userManagementActionBackend) {
	guid, keys, tickets, ok := rolePermissionMutationHeaders(c)
	if !ok {
		return
	}
	request, err := dto.DecodePermissionsWriteExecute(c.Request.Body)
	if err != nil {
		rolePermissionDecodeError(c, err)
		return
	}
	executeRolePermissionIntent(c, backend, actionsecurity.ActionUsersPermissionsWrite, request.Intent(guid), keys, tickets)
}

func rolePermissionDecodeError(c *gin.Context, err error) {
	if errors.Is(err, dto.ErrRolePermissionBodyTooLarge) {
		adminUserActionFixedError(c, http.StatusRequestEntityTooLarge, "request_body_too_large", "")
		return
	}
	adminUserActionDecodeError(c, err)
}

func rolePermissionMutationHeaders(c *gin.Context) (int64, []string, []string, bool) {
	guid, err := service.ParseAdminPermissionGUID(c.Param("guid"))
	if err != nil || c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return 0, nil, nil, false
	}
	keys, keyOK := exactHeaderValues(c, "Idempotency-Key")
	tickets, ticketOK := exactHeaderValues(c, "X-Action-Ticket")
	if !keyOK || !ticketOK {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return 0, nil, nil, false
	}
	if _, err := actionsecurity.ParseIdempotencyKey(keys); err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return 0, nil, nil, false
	}
	if _, err := actionsecurity.ParseTicket(tickets); err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return 0, nil, nil, false
	}
	return guid, keys, tickets, true
}

func executeRolePermissionIntent(c *gin.Context, backend userManagementActionBackend, action actionsecurity.Action, intent any, keys, tickets []string) {
	roleBackend, ok := backend.(rolePermissionActionBackend)
	if !ok {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	identity, view, err := roleBackend.Begin(c.Request.Context(), service.OperationBegin{Action: action, Actor: adminUserActionActor(c), IdempotencyKeyValues: keys, TicketValues: tickets, Intent: intent})
	if err != nil {
		adminUserActionError(c, err, "")
		return
	}
	if identity == nil || view == nil || identity.PublicRef != view.PublicRef || !validRolePermissionView(view, action) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	if !roleBackend.ExecutionReady(identity) {
		writeRolePermissionMutationView(c, view, action)
		return
	}
	var execution *service.RolePermissionExecution
	switch value := intent.(type) {
	case actionsecurity.PromoteIntent:
		execution, err = roleBackend.NewPromoteExecution(value)
	case actionsecurity.DemoteIntent:
		execution, err = roleBackend.NewDemoteExecution(value)
	case actionsecurity.PermissionsWriteIntent:
		execution, err = roleBackend.NewPermissionsWriteExecution(value)
	default:
		err = service.ErrActionOperationUnavailable
	}
	if err != nil || execution == nil {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	view, err = roleBackend.ExecuteRolePermission(c.Request.Context(), identity, execution)
	if err != nil {
		var unknown *service.CommitUnknownError
		if errors.As(err, &unknown) && unknown != nil && unknown.PublicRef == identity.PublicRef {
			adminUserActionError(c, err, unknown.PublicRef)
			return
		}
		adminUserActionError(c, err, "")
		return
	}
	writeRolePermissionMutationView(c, view, action)
}

func writeRolePermissionMutationView(c *gin.Context, view *service.OperationView, expectedAction actionsecurity.Action) {
	if !validRolePermissionView(view, expectedAction) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	switch view.Status {
	case "succeeded":
		role := view.ResultRole.String()
		c.JSON(http.StatusOK, dto.RolePermissionResult{OperationRef: view.PublicRef, TargetGUID: strconv.FormatInt(*view.TargetGUID, 10), ResultingAuthVersion: *view.ResultAuthVersion, ResultingPermissionsVersion: *view.ResultPermissionsVersion, ResultingRole: role})
	case "failed":
		writeRolePermissionFailure(c, *view.FailureCode)
	case "processing", "pending_recovery":
		adminUserActionFixedError(c, http.StatusServiceUnavailable, "operation_commit_unknown", view.PublicRef)
	}
}

func writeRolePermissionQuery(c *gin.Context, view *service.OperationView, expectedAction actionsecurity.Action) {
	if !validRolePermissionView(view, expectedAction) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	var target *string
	var role *string
	if view.TargetGUID != nil {
		value := strconv.FormatInt(*view.TargetGUID, 10)
		target = &value
	}
	if view.ResultRole != nil {
		value := view.ResultRole.String()
		role = &value
	}
	c.JSON(http.StatusOK, dto.RolePermissionQueryResponse{OperationRef: view.PublicRef, Scope: view.Scope, Status: view.Status, FinishedAt: view.FinishedAt, FailureCode: view.FailureCode, TargetGUID: target, ResultingAuthVersion: view.ResultAuthVersion, ResultingPermissionsVersion: view.ResultPermissionsVersion, ResultingRole: role})
}

func validRolePermissionView(view *service.OperationView, expectedAction actionsecurity.Action) bool {
	if view == nil || !validUserDeleteOperationRef(view.PublicRef) {
		return false
	}
	action, ok := rolePermissionActionForScope(view.Scope)
	if !ok || action != expectedAction || rolePermissionScope(expectedAction) != view.Scope {
		return false
	}
	switch view.Status {
	case "succeeded":
		return view.FinishedAt != nil && view.FailureCode == nil && view.RetryAfterSeconds == 0 && view.TargetGUID != nil && *view.TargetGUID > 0 && view.ResultAuthVersion != nil && *view.ResultAuthVersion > 0 && view.ResultPermissionsVersion != nil && *view.ResultPermissionsVersion > 0 && view.ResultRole != nil && ((*view.ResultRole == models.UserRoleAdmin && expectedAction != actionsecurity.ActionUsersDemote) || (*view.ResultRole == models.UserRoleUser && expectedAction == actionsecurity.ActionUsersDemote))
	case "failed":
		return view.FinishedAt != nil && view.FailureCode != nil && safeUserDeleteFailure(view.FailureCode) != "" && view.RetryAfterSeconds == 0 && view.TargetGUID == nil && view.ResultAuthVersion == nil && view.ResultPermissionsVersion == nil && view.ResultRole == nil
	case "processing":
		return view.FinishedAt == nil && view.FailureCode == nil && view.RetryAfterSeconds >= 1 && view.RetryAfterSeconds <= 30 && view.TargetGUID == nil && view.ResultAuthVersion == nil && view.ResultPermissionsVersion == nil && view.ResultRole == nil
	case "pending_recovery":
		return view.FinishedAt == nil && view.FailureCode == nil && view.RetryAfterSeconds == 0 && view.TargetGUID == nil && view.ResultAuthVersion == nil && view.ResultPermissionsVersion == nil && view.ResultRole == nil
	default:
		return false
	}
}

func writeRolePermissionFailure(c *gin.Context, failure string) {
	switch failure {
	case "action_rejected", "target_version_conflict", "policy_version_conflict", "target_state_conflict", "consumer_validation_failed":
		adminUserActionFixedError(c, http.StatusConflict, failure, "")
	default:
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
	}
}

func rolePermissionTargetGUID(intent any) int64 {
	switch value := intent.(type) {
	case actionsecurity.PromoteIntent:
		return value.TargetGUID
	case actionsecurity.DemoteIntent:
		return value.TargetGUID
	case actionsecurity.PermissionsWriteIntent:
		return value.TargetGUID
	}
	return 0
}
func isRolePermissionAction(action actionsecurity.Action) bool {
	return action == actionsecurity.ActionUsersPromote || action == actionsecurity.ActionUsersDemote || action == actionsecurity.ActionUsersPermissionsWrite
}
func rolePermissionScope(action actionsecurity.Action) string {
	switch action {
	case actionsecurity.ActionUsersPromote:
		return "users.promote"
	case actionsecurity.ActionUsersDemote:
		return "users.demote"
	case actionsecurity.ActionUsersPermissionsWrite:
		return "users.permissions.write"
	}
	return ""
}
func rolePermissionActionForScope(scope string) (actionsecurity.Action, bool) {
	switch scope {
	case "users.promote":
		return actionsecurity.ActionUsersPromote, true
	case "users.demote":
		return actionsecurity.ActionUsersDemote, true
	case "users.permissions.write":
		return actionsecurity.ActionUsersPermissionsWrite, true
	}
	return 0, false
}
