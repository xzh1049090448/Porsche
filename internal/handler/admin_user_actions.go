package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const userDeleteScopeQuery = "scope=users.delete"

type userDeleteActionBackend interface {
	Issue(context.Context, service.VerificationIssue) (*service.IssuedVerification, error)
	Begin(context.Context, service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error)
	ExecutionReady(*service.OperationIdentity) bool
	NewExecution(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error)
	Execute(context.Context, *service.OperationIdentity, *service.DeleteUserExecution) (*service.OperationView, error)
	Query(context.Context, actionsecurity.Action, service.ActionActor, []string) (*service.OperationView, error)
}

func adminUserActionNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func issueUserDeleteVerification(c *gin.Context, backend userDeleteActionBackend, settings *config.Settings) {
	if hasHeader(c, "Idempotency-Key") || hasHeader(c, "X-Action-Ticket") || c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	request, err := dto.DecodeUserDeleteIssue(c.Request.Body)
	if err != nil {
		adminUserActionDecodeError(c, err)
		return
	}
	defer clear(request.Password)
	intent := actionsecurity.DeleteUserIntent{TargetGUID: request.TargetGUID, ExpectedAuthVersion: request.ExpectedVersion, Reason: request.Reason}
	target := request.TargetGUID
	trustedIP := ""
	if settings != nil {
		trustedIP = httpx.ClientIP(c, settings.TrustProxyHeaders, settings.TrustedProxyCIDRs)
	}
	issued, err := backend.Issue(c.Request.Context(), service.VerificationIssue{
		Action: actionsecurity.ActionUsersDelete, Actor: adminUserActionActor(c), TargetGUID: &target,
		Intent: intent, CurrentPassword: request.Password, TrustedIP: trustedIP,
	})
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

func executeUserDelete(c *gin.Context, backend userDeleteActionBackend) {
	guidRaw := c.Param("guid")
	guid, err := service.ParseAdminPermissionGUID(guidRaw)
	if err != nil || c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	keyValues, keyCanonical := exactHeaderValues(c, "Idempotency-Key")
	ticketValues, ticketCanonical := exactHeaderValues(c, "X-Action-Ticket")
	if !keyCanonical || !ticketCanonical {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	if _, err := actionsecurity.ParseIdempotencyKey(keyValues); err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	if _, err := actionsecurity.ParseTicket(ticketValues); err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	request, err := dto.DecodeUserDeleteExecute(c.Request.Body)
	if err != nil {
		adminUserActionDecodeError(c, err)
		return
	}
	intent := actionsecurity.DeleteUserIntent{TargetGUID: guid, ExpectedAuthVersion: request.ExpectedVersion, Reason: request.Reason}
	identity, view, err := backend.Begin(c.Request.Context(), service.OperationBegin{
		Action: actionsecurity.ActionUsersDelete, Actor: adminUserActionActor(c), IdempotencyKeyValues: keyValues,
		TicketValues: ticketValues, Intent: intent,
	})
	if err != nil {
		adminUserActionError(c, err, "")
		return
	}
	if view == nil || identity == nil || !validUserDeleteOperationRef(view.PublicRef) || identity.PublicRef != view.PublicRef {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	if !backend.ExecutionReady(identity) {
		writeExistingUserDeleteView(c, view, guidRaw)
		return
	}
	execution, err := backend.NewExecution(intent)
	if err != nil || execution == nil {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	view, err = backend.Execute(c.Request.Context(), identity, execution)
	if err != nil {
		var unknown *service.CommitUnknownError
		if errors.As(err, &unknown) && unknown != nil && unknown.PublicRef == identity.PublicRef && validUserDeleteOperationRef(unknown.PublicRef) {
			adminUserActionError(c, err, unknown.PublicRef)
			return
		}
		adminUserActionError(c, err, "")
		return
	}
	writeExecutedUserDeleteView(c, view, guidRaw)
}

func writeExistingUserDeleteView(c *gin.Context, view *service.OperationView, guid string) {
	if view.Scope != "users.delete" {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	switch view.Status {
	case "succeeded":
		if view.FinishedAt == nil || view.FailureCode != nil || view.RetryAfterSeconds != 0 {
			adminUserActionError(c, service.ErrActionOperationUnavailable, "")
			return
		}
		writeUserDeleteSuccess(c, view.PublicRef, guid)
	case "failed":
		if code := safeUserDeleteFailure(view.FailureCode); code != "" && view.FinishedAt != nil && view.RetryAfterSeconds == 0 {
			adminUserActionFixedError(c, http.StatusConflict, code, "")
			return
		}
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
	case "processing", "pending_recovery":
		if view.FinishedAt != nil || view.FailureCode != nil || (view.Status == "processing" && (view.RetryAfterSeconds < 1 || view.RetryAfterSeconds > 30)) ||
			(view.Status == "pending_recovery" && view.RetryAfterSeconds != 0) {
			adminUserActionError(c, service.ErrActionOperationUnavailable, "")
			return
		}
		adminUserActionFixedError(c, http.StatusServiceUnavailable, "operation_commit_unknown", view.PublicRef)
	default:
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
	}
}

func writeExecutedUserDeleteView(c *gin.Context, view *service.OperationView, guid string) {
	if view == nil || view.Scope != "users.delete" || !validUserDeleteOperationRef(view.PublicRef) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	if view.Status == "succeeded" && view.FinishedAt != nil && view.FailureCode == nil && view.RetryAfterSeconds == 0 {
		writeUserDeleteSuccess(c, view.PublicRef, guid)
		return
	}
	if view.Status == "failed" {
		if code := safeUserDeleteFailure(view.FailureCode); code != "" && view.FinishedAt != nil && view.RetryAfterSeconds == 0 {
			adminUserActionFixedError(c, http.StatusConflict, code, "")
			return
		}
	}
	adminUserActionError(c, service.ErrActionOperationUnavailable, "")
}

func writeUserDeleteSuccess(c *gin.Context, publicRef, guid string) {
	c.JSON(http.StatusOK, dto.DeleteUserResponse{OperationRef: publicRef, User: dto.UserDeleteResponseUser{GUID: guid, Status: "deleted"}})
}

func queryUserDeleteOperation(c *gin.Context, backend userDeleteActionBackend) {
	if c.Request.URL.RawQuery != userDeleteScopeQuery || c.Request.URL.RawPath != "" || hasHeader(c, "X-Action-Ticket") {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	keyValues, keyCanonical := exactHeaderValues(c, "Idempotency-Key")
	if !keyCanonical {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	if _, err := actionsecurity.ParseIdempotencyKey(keyValues); err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	view, err := backend.Query(c.Request.Context(), actionsecurity.ActionUsersDelete, adminUserActionActor(c), keyValues)
	if err != nil {
		adminUserActionError(c, err, "")
		return
	}
	if !validUserDeleteQueryView(view) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	c.Writer.Header().Del("Retry-After")
	if view.Status == "processing" {
		c.Header("Retry-After", strconv.Itoa(view.RetryAfterSeconds))
	}
	c.JSON(http.StatusOK, dto.UserDeleteQueryResponse{OperationRef: view.PublicRef, Scope: view.Scope, Status: view.Status, FinishedAt: view.FinishedAt, FailureCode: view.FailureCode})
}

func validUserDeleteQueryView(view *service.OperationView) bool {
	if view == nil || view.Scope != "users.delete" || !validUserDeleteOperationRef(view.PublicRef) {
		return false
	}
	switch view.Status {
	case "processing":
		return view.RetryAfterSeconds >= 1 && view.RetryAfterSeconds <= 30 && view.FinishedAt == nil && view.FailureCode == nil
	case "succeeded":
		return view.RetryAfterSeconds == 0 && view.FinishedAt != nil && view.FailureCode == nil
	case "failed":
		return view.RetryAfterSeconds == 0 && view.FinishedAt != nil && safeUserDeleteFailure(view.FailureCode) != ""
	case "pending_recovery":
		return view.RetryAfterSeconds == 0 && view.FinishedAt == nil && view.FailureCode == nil
	default:
		return false
	}
}

func safeUserDeleteFailure(code *string) string {
	if code == nil {
		return ""
	}
	switch *code {
	case "action_rejected", "target_version_conflict", "policy_version_conflict", "target_state_conflict", "consumer_validation_failed":
		return *code
	default:
		return ""
	}
}

func validUserDeleteOperationRef(publicRef string) bool {
	_, err := actionsecurity.ParsePublicRef(publicRef)
	return err == nil
}

func adminUserActionActor(c *gin.Context) service.ActionActor {
	user := middleware.CurrentUser(c)
	if user == nil {
		return service.ActionActor{}
	}
	return service.ActionActor{UserID: user.ID, UserGUID: user.Guid, AuthVersion: user.AuthVersion, SessionSID: middleware.CurrentSessionSID(c), SessionVersion: middleware.CurrentSessionVersion(c)}
}

func hasHeader(c *gin.Context, name string) bool {
	for key, values := range c.Request.Header {
		if strings.EqualFold(key, name) && len(values) != 0 {
			return true
		}
	}
	return false
}

// exactHeaderValues preserves Header.Values for the service boundary while
// rejecting manually constructed non-canonical duplicate map keys as well.
func exactHeaderValues(c *gin.Context, name string) ([]string, bool) {
	values := c.Request.Header.Values(name)
	occurrences := 0
	for key, raw := range c.Request.Header {
		if strings.EqualFold(key, name) {
			occurrences += len(raw)
		}
	}
	return values, occurrences == len(values)
}

var errInvalidAdminUserAction = errors.New("invalid admin user action request")

func adminUserActionDecodeError(c *gin.Context, err error) {
	if errors.Is(err, dto.ErrUserDeleteInactiveAction) {
		adminUserActionFixedError(c, http.StatusUnprocessableEntity, "action_inactive", "")
		return
	}
	adminUserActionError(c, errInvalidAdminUserAction, "")
}

type adminUserActionErrorBody struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	OperationRef string `json:"operation_ref,omitempty"`
}

type adminUserActionErrorEnvelope struct {
	Error adminUserActionErrorBody `json:"error"`
}

func adminUserActionError(c *gin.Context, err error, operationRef string) {
	status, code := http.StatusServiceUnavailable, "action_dependency_unavailable"
	var retry *service.RetryAfterError
	switch {
	case errors.Is(err, errInvalidAdminUserAction):
		status, code = http.StatusBadRequest, "invalid_admin_action_request"
	case errors.As(err, &retry):
		if retry != nil && retry.Seconds > 0 {
			status, code = http.StatusTooManyRequests, "action_rate_limited"
		} else {
			retry = nil
		}
	case errors.Is(err, service.ErrActionVerificationInactive), errors.Is(err, service.ErrActionOperationInactive):
		status, code = http.StatusUnprocessableEntity, "action_inactive"
	case errors.Is(err, service.ErrActionVerificationForbidden):
		status, code = http.StatusForbidden, "action_verification_rejected"
	case errors.Is(err, service.ErrActionOperationForbidden):
		status, code = http.StatusForbidden, "action_operation_rejected"
	case errors.Is(err, service.ErrActionVerificationHidden):
		status, code = http.StatusNotFound, "action_target_not_found"
	case errors.Is(err, service.ErrActionOperationHidden):
		status, code = http.StatusNotFound, "action_operation_not_found"
	case errors.Is(err, service.ErrActionVerificationConflict):
		status, code = http.StatusConflict, "action_verification_conflict"
	case errors.Is(err, service.ErrActionOperationConflict):
		status, code = http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, service.ErrActionOperationCrossSession):
		status, code = http.StatusConflict, "idempotency_cross_session"
	case errors.Is(err, service.ErrRolePermissionActionRejected):
		status, code = http.StatusConflict, "action_rejected"
	case errors.Is(err, service.ErrRolePermissionTargetVersionConflict):
		status, code = http.StatusConflict, "target_version_conflict"
	case errors.Is(err, service.ErrRolePermissionPolicyVersionConflict):
		status, code = http.StatusConflict, "policy_version_conflict"
	case errors.Is(err, service.ErrRolePermissionTargetStateConflict):
		status, code = http.StatusConflict, "target_state_conflict"
	case errors.Is(err, service.ErrRolePermissionConsumerValidation):
		status, code = http.StatusConflict, "consumer_validation_failed"
	case errors.Is(err, service.ErrActionOperationExpired):
		status, code = http.StatusGone, "operation_expired"
	default:
		var unknown *service.CommitUnknownError
		if errors.As(err, &unknown) && operationRef != "" {
			status, code = http.StatusServiceUnavailable, "operation_commit_unknown"
		} else {
			operationRef = ""
		}
	}
	if retry != nil && retry.Seconds > 0 {
		c.Header("Retry-After", strconv.Itoa(retry.Seconds))
	}
	adminUserActionFixedError(c, status, code, operationRef)
}

func adminUserActionFixedError(c *gin.Context, status int, code, operationRef string) {
	if (code != "operation_commit_unknown" && code != "created_user_deleted") || !validUserDeleteOperationRef(operationRef) {
		operationRef = ""
	}
	requestID := c.Writer.Header().Get("X-Request-ID")
	if requestID == "" {
		requestID = "unavailable"
		c.Header("X-Request-ID", requestID)
	}
	c.AbortWithStatusJSON(status, adminUserActionErrorEnvelope{Error: adminUserActionErrorBody{
		Code: code, Message: "请求无法完成", Type: "admin_action_error", RequestID: requestID, OperationRef: operationRef,
	}})
}
