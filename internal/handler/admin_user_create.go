package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	userCreateScopeQuery        = "scope=users.create"
	userCreateAdminScopeQuery   = "scope=users.create_admin"
	userResetPasswordScopeQuery = "scope=users.reset_password"
)

var errInvalidAdminUserCreate = errors.New("invalid admin user create request")

type userManagementActionBackend interface {
	Issue(context.Context, service.VerificationIssue) (*service.IssuedVerification, error)
	Begin(context.Context, service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error)
	ExecutionReady(*service.OperationIdentity) bool
	HashCreatePassword([]byte) ([]byte, error)
	NewDeleteExecution(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error)
	ExecuteDelete(context.Context, *service.OperationIdentity, *service.DeleteUserExecution) (*service.OperationView, error)
	NewCreateExecution(actionsecurity.Action, actionsecurity.CreateAccountIntent, []byte, service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error)
	ExecuteCreate(context.Context, *service.OperationIdentity, *service.CreateAccountExecution) (*service.OperationView, error)
	CreateOutcome(context.Context, actionsecurity.Action, service.ActionActor, string) (*service.PersistedActionResponse, int, error)
	Query(context.Context, actionsecurity.Action, service.ActionActor, []string) (*service.OperationView, error)
}

type userManagementActionBundleBackend struct {
	bundle *service.UserManagementActions
	db     *gorm.DB
}

type userManagementResetBackend interface {
	userManagementActionBackend
	HashResetPassword([]byte) ([]byte, error)
	NewResetPasswordExecution(actionsecurity.ResetPasswordIntent, []byte, service.ResetPasswordRequestMetadata) (*service.ResetPasswordExecution, error)
	ExecuteResetPassword(context.Context, *service.OperationIdentity, *service.ResetPasswordExecution) (*service.OperationView, error)
	ResetPasswordOutcome(context.Context, service.ActionActor, string) (*service.ResetPasswordResult, error)
}

func (backend userManagementActionBundleBackend) Issue(ctx context.Context, issue service.VerificationIssue) (*service.IssuedVerification, error) {
	return backend.bundle.Verifications.Issue(ctx, issue)
}

func (backend userManagementActionBundleBackend) Begin(ctx context.Context, begin service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error) {
	return backend.bundle.Operations.Begin(ctx, begin)
}

func (userManagementActionBundleBackend) ExecutionReady(identity *service.OperationIdentity) bool {
	return identity.ReadyForExecution()
}

func (userManagementActionBundleBackend) HashCreatePassword(password []byte) ([]byte, error) {
	return service.HashManagedCreationPasswordBytes(password)
}

func (userManagementActionBundleBackend) HashResetPassword(password []byte) ([]byte, error) {
	return service.HashManagedCreationPasswordBytes(password)
}

func (backend userManagementActionBundleBackend) NewDeleteExecution(intent actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
	return backend.bundle.NewDeleteExecution(intent)
}

func (backend userManagementActionBundleBackend) ExecuteDelete(ctx context.Context, identity *service.OperationIdentity, execution *service.DeleteUserExecution) (*service.OperationView, error) {
	return backend.bundle.Operations.Execute(ctx, identity, execution, execution, backend.bundle.DeleteOutbox)
}

func (backend userManagementActionBundleBackend) NewCreateExecution(action actionsecurity.Action, intent actionsecurity.CreateAccountIntent, hash []byte, metadata service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error) {
	return backend.bundle.NewCreateExecution(action, intent, hash, metadata)
}

func (backend userManagementActionBundleBackend) ExecuteCreate(ctx context.Context, identity *service.OperationIdentity, execution *service.CreateAccountExecution) (*service.OperationView, error) {
	return backend.bundle.Operations.Execute(ctx, identity, execution, execution, backend.bundle.CreateOutbox)
}

func (backend userManagementActionBundleBackend) NewResetPasswordExecution(intent actionsecurity.ResetPasswordIntent, hash []byte, metadata service.ResetPasswordRequestMetadata) (*service.ResetPasswordExecution, error) {
	return backend.bundle.NewResetExecution(intent, hash, metadata)
}

func (backend userManagementActionBundleBackend) ExecuteResetPassword(ctx context.Context, identity *service.OperationIdentity, execution *service.ResetPasswordExecution) (*service.OperationView, error) {
	return backend.bundle.Operations.Execute(ctx, identity, execution, execution, backend.bundle.ResetOutbox)
}

func (backend userManagementActionBundleBackend) ResetPasswordOutcome(ctx context.Context, actor service.ActionActor, ref string) (*service.ResetPasswordResult, error) {
	return backend.bundle.Operations.ResetPasswordResponse(ctx, actor, ref)
}

func (backend userManagementActionBundleBackend) Query(ctx context.Context, action actionsecurity.Action, actor service.ActionActor, keys []string) (*service.OperationView, error) {
	return backend.bundle.Operations.Query(ctx, action, actor, keys)
}

func (backend userManagementActionBundleBackend) CreateOutcome(ctx context.Context, action actionsecurity.Action, actor service.ActionActor, publicRef string) (*service.PersistedActionResponse, int, error) {
	if backend.bundle == nil || backend.bundle.Operations == nil || backend.db == nil || backend.db.Statement == nil || backend.db.Statement.ConnPool == nil || ctx == nil {
		return nil, 0, service.ErrActionOperationUnavailable
	}
	var operations []models.AdminOperation
	db := backend.db.Session(&gorm.Session{NewDB: true, Logger: logger.Discard}).WithContext(ctx)
	if db.Where("public_ref = ? AND actor_user_id = ? AND actor_auth_version = ? AND action = ? AND is_deleted = 0", publicRef, actor.UserID, actor.AuthVersion, int(action)).Limit(2).Find(&operations).Error != nil || len(operations) != 1 {
		return nil, 0, service.ErrActionOperationUnavailable
	}
	operation := operations[0]
	if operation.ID <= 0 || operation.PublicRef != publicRef || operation.ResultHTTPStatus == nil || operation.FinishedAt == nil {
		return nil, 0, service.ErrActionOperationUnavailable
	}
	status := *operation.ResultHTTPStatus
	switch operation.State {
	case models.OperationSucceeded:
		response, err := backend.bundle.Operations.CreateAccountResponse(ctx, action, actor, publicRef)
		if errors.Is(err, service.ErrCreatedAccountDeleted) && response == nil {
			return nil, http.StatusGone, service.ErrCreatedAccountDeleted
		}
		if err != nil || response == nil || response.HTTPStatus != status {
			return nil, 0, service.ErrActionOperationUnavailable
		}
		return response, status, nil
	case models.OperationFailed:
		if operation.ErrorCode == nil || operation.ResultKind != nil || operation.ResultGUID != nil || (status != http.StatusForbidden && status != http.StatusNotFound && status != http.StatusConflict) {
			return nil, 0, service.ErrActionOperationUnavailable
		}
		return nil, status, nil
	default:
		return nil, 0, service.ErrActionOperationUnavailable
	}
}

type userManagementDeleteBackend struct{ backend userManagementActionBackend }

func (backend userManagementDeleteBackend) Issue(ctx context.Context, issue service.VerificationIssue) (*service.IssuedVerification, error) {
	return backend.backend.Issue(ctx, issue)
}
func (backend userManagementDeleteBackend) Begin(ctx context.Context, begin service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error) {
	return backend.backend.Begin(ctx, begin)
}
func (backend userManagementDeleteBackend) ExecutionReady(identity *service.OperationIdentity) bool {
	return backend.backend.ExecutionReady(identity)
}
func (backend userManagementDeleteBackend) NewExecution(intent actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
	return backend.backend.NewDeleteExecution(intent)
}
func (backend userManagementDeleteBackend) Execute(ctx context.Context, identity *service.OperationIdentity, execution *service.DeleteUserExecution) (*service.OperationView, error) {
	return backend.backend.ExecuteDelete(ctx, identity, execution)
}
func (backend userManagementDeleteBackend) Query(ctx context.Context, action actionsecurity.Action, actor service.ActionActor, keys []string) (*service.OperationView, error) {
	return backend.backend.Query(ctx, action, actor, keys)
}

// RegisterAdminUserManagementActions publishes one route owner only when the
// create, create-admin, and delete action bundle is complete.
func RegisterAdminUserManagementActions(r *gin.Engine, state *app.State) {
	if r == nil || state == nil || state.Settings == nil || !completeUserManagementActionBundle(state.UserManagementActions) {
		return
	}
	group := r.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, middleware.RequireUser(state))
	registerAdminUserManagementActionRoutes(group, userManagementActionBundleBackend{bundle: state.UserManagementActions, db: state.DB}, state.Settings)
}

func completeUserManagementActionBundle(bundle *service.UserManagementActions) bool {
	return bundle != nil && bundle.Verifications != nil && bundle.Operations != nil && bundle.DeleteOutbox != nil && bundle.CreateOutbox != nil && bundle.ResetOutbox != nil &&
		bundle.NewDeleteExecution != nil && bundle.NewCreateExecution != nil && bundle.NewResetExecution != nil
}

func registerAdminUserManagementActionRoutes(group *gin.RouterGroup, backend userManagementActionBackend, settings *config.Settings) {
	group.POST("/action-verifications", func(c *gin.Context) { issueUserManagementVerification(c, backend, settings) })
	group.POST("/users", func(c *gin.Context) { executeAdminUserCreate(c, backend, settings) })
	group.POST("/users/:guid/actions", func(c *gin.Context) { executeUserManagementAction(c, backend, settings) })
	group.GET("/operations", func(c *gin.Context) { queryUserManagementOperation(c, backend) })
}

func issueUserManagementVerification(c *gin.Context, backend userManagementActionBackend, settings *config.Settings) {
	if hasHeader(c, "Idempotency-Key") || hasHeader(c, "X-Action-Ticket") || c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	raw, err := readUserManagementVerificationBody(c.Request.Body)
	if err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	defer clear(raw)
	action, err := userManagementVerificationAction(raw)
	if err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	switch action {
	case "users.delete":
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		issueUserDeleteVerification(c, userManagementDeleteBackend{backend: backend}, settings)
	case "users.create_admin":
		issueAdminUserCreateVerification(c, backend, settings, raw)
	case "users.reset_password":
		issueAdminUserPasswordResetVerification(c, backend, settings, raw)
	default:
		adminUserActionFixedError(c, http.StatusUnprocessableEntity, "action_inactive", "")
	}
}

func readUserManagementVerificationBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, errInvalidAdminUserAction
	}
	raw, err := io.ReadAll(io.LimitReader(body, dto.AdminUserCreateBodyLimit+1))
	if err != nil || int64(len(raw)) > dto.AdminUserCreateBodyLimit {
		clear(raw)
		return nil, errInvalidAdminUserAction
	}
	return raw, nil
}

func userManagementVerificationAction(raw []byte) (string, error) {
	var envelope struct {
		Action json.RawMessage `json:"action"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Action) == 0 {
		return "", errInvalidAdminUserAction
	}
	var action string
	if err := json.Unmarshal(envelope.Action, &action); err != nil || action == "" {
		return "", errInvalidAdminUserAction
	}
	return action, nil
}

func issueAdminUserCreateVerification(c *gin.Context, backend userManagementActionBackend, settings *config.Settings, raw []byte) {
	request, err := dto.DecodeAdminUserCreateVerification(bytes.NewReader(raw))
	if err != nil {
		adminUserCreateDecodeError(c, err)
		return
	}
	defer request.ClearSecrets()
	intent := adminUserCreateIntent(request.Intent)
	defer clear(intent.Password)
	currentPassword := append([]byte(nil), request.CurrentPassword...)
	defer clear(currentPassword)
	request.ClearSecrets()
	trustedIP := ""
	if settings != nil {
		trustedIP = httpx.ClientIP(c, settings.TrustProxyHeaders, settings.TrustedProxyCIDRs)
	}
	issued, err := backend.Issue(c.Request.Context(), service.VerificationIssue{
		Action: actionsecurity.ActionUsersCreateAdmin, Actor: adminUserActionActor(c), Intent: intent,
		CurrentPassword: currentPassword, TrustedIP: trustedIP,
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

func issueAdminUserPasswordResetVerification(c *gin.Context, backend userManagementActionBackend, settings *config.Settings, raw []byte) {
	request, err := dto.DecodeAdminUserPasswordResetIssue(bytes.NewReader(raw))
	if err != nil {
		adminUserActionDecodeError(c, err)
		return
	}
	defer request.ClearSecrets()
	intent := actionsecurity.ResetPasswordIntent{TargetGUID: request.TargetGUID, ExpectedAuthVersion: request.ExpectedAuthVersion, NewPassword: append([]byte(nil), request.NewPassword...), Reason: request.Reason}
	defer clear(intent.NewPassword)
	current := append([]byte(nil), request.CurrentPassword...)
	defer clear(current)
	request.ClearSecrets()
	trustedIP := ""
	if settings != nil {
		trustedIP = httpx.ClientIP(c, settings.TrustProxyHeaders, settings.TrustedProxyCIDRs)
	}
	target := intent.TargetGUID
	issued, err := backend.Issue(c.Request.Context(), service.VerificationIssue{Action: actionsecurity.ActionUsersResetPassword, Actor: adminUserActionActor(c), TargetGUID: &target, Intent: intent, CurrentPassword: current, TrustedIP: trustedIP})
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

func executeUserManagementAction(c *gin.Context, backend userManagementActionBackend, settings *config.Settings) {
	raw, err := readUserManagementVerificationBody(c.Request.Body)
	if err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	defer clear(raw)
	action, err := userManagementVerificationAction(raw)
	if err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	switch action {
	case "delete":
		executeUserDelete(c, userManagementDeleteBackend{backend: backend})
	case "reset_password":
		reset, ok := backend.(userManagementResetBackend)
		if !ok {
			adminUserActionError(c, service.ErrActionOperationUnavailable, "")
			return
		}
		executeAdminUserPasswordReset(c, reset, settings)
	default:
		adminUserActionFixedError(c, http.StatusUnprocessableEntity, "action_inactive", "")
	}
}

func executeAdminUserPasswordReset(c *gin.Context, backend userManagementResetBackend, settings *config.Settings) {
	guidRaw := c.Param("guid")
	guid, err := service.ParseAdminPermissionGUID(guidRaw)
	if err != nil || c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	keys, keyOK := exactHeaderValues(c, "Idempotency-Key")
	tickets, ticketOK := exactHeaderValues(c, "X-Action-Ticket")
	if !keyOK || !ticketOK {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	if _, err := actionsecurity.ParseIdempotencyKey(keys); err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	if _, err := actionsecurity.ParseTicket(tickets); err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	request, err := dto.DecodeAdminUserPasswordResetExecute(c.Request.Body)
	if err != nil {
		adminUserActionDecodeError(c, err)
		return
	}
	defer request.ClearSecrets()
	intent := actionsecurity.ResetPasswordIntent{TargetGUID: guid, ExpectedAuthVersion: request.ExpectedAuthVersion, NewPassword: append([]byte(nil), request.NewPassword...), Reason: request.Reason}
	defer clear(intent.NewPassword)
	actor := adminUserActionActor(c)
	identity, view, err := backend.Begin(c.Request.Context(), service.OperationBegin{Action: actionsecurity.ActionUsersResetPassword, Actor: actor, IdempotencyKeyValues: keys, TicketValues: tickets, Intent: intent})
	if err != nil {
		adminUserActionError(c, err, "")
		return
	}
	if identity == nil || view == nil || identity.PublicRef != view.PublicRef || view.Scope != "users.reset_password" || !validUserDeleteOperationRef(view.PublicRef) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	if !backend.ExecutionReady(identity) {
		writeResetPasswordView(c, backend, actor, view)
		return
	}
	hash, err := backend.HashResetPassword(request.NewPassword)
	if err != nil || len(hash) == 0 {
		clear(hash)
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	defer clear(hash)
	trustedIP := ""
	if settings != nil {
		trustedIP = httpx.ClientIP(c, settings.TrustProxyHeaders, settings.TrustedProxyCIDRs)
	}
	// Rebuild the execution intent only after Begin returned a fresh lease. The
	// constructor authenticates the complete plaintext-bearing intent and owns
	// clearing this sole execution buffer.
	executionPassword := request.NewPassword
	intent.NewPassword = executionPassword
	execution, err := backend.NewResetPasswordExecution(intent, hash, service.ResetPasswordRequestMetadata{RequestID: c.Writer.Header().Get("X-Request-ID"), TrustedIP: trustedIP})
	clear(executionPassword)
	request.NewPassword = nil
	intent.NewPassword = nil
	clear(hash)
	if err != nil || execution == nil {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	defer execution.ClearSecrets()
	view, err = backend.ExecuteResetPassword(c.Request.Context(), identity, execution)
	if err != nil {
		var unknown *service.CommitUnknownError
		if errors.As(err, &unknown) && unknown != nil {
			adminUserActionError(c, err, unknown.PublicRef)
			return
		}
		adminUserActionError(c, err, "")
		return
	}
	writeResetPasswordView(c, backend, actor, view)
}

func writeResetPasswordView(c *gin.Context, backend userManagementResetBackend, actor service.ActionActor, view *service.OperationView) {
	if view == nil || view.Scope != "users.reset_password" || !validUserDeleteOperationRef(view.PublicRef) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	switch view.Status {
	case "succeeded":
		if view.FinishedAt == nil || view.FailureCode != nil {
			adminUserActionError(c, service.ErrActionOperationUnavailable, "")
			return
		}
		result, err := backend.ResetPasswordOutcome(c.Request.Context(), actor, view.PublicRef)
		if err != nil || result == nil {
			adminUserActionError(c, service.ErrActionOperationUnavailable, "")
			return
		}
		c.JSON(http.StatusOK, dto.AdminUserPasswordResetResponse{OperationRef: view.PublicRef, TargetGUID: strconv.FormatInt(result.TargetGUID, 10), ResultingAuthVersion: result.ResultingAuthVersion})
	case "failed":
		if view.FinishedAt != nil && view.FailureCode != nil {
			writeAdminUserCreateFailure(c, *view.FailureCode, http.StatusConflict)
			return
		}
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
	case "processing", "pending_recovery":
		adminUserActionFixedError(c, http.StatusServiceUnavailable, "operation_commit_unknown", view.PublicRef)
	default:
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
	}
}

func executeAdminUserCreate(c *gin.Context, backend userManagementActionBackend, settings *config.Settings) {
	if c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" {
		adminUserCreateError(c, errInvalidAdminUserCreate, "")
		return
	}
	keyValues, keyCanonical := exactHeaderValues(c, "Idempotency-Key")
	if !keyCanonical {
		adminUserCreateError(c, errInvalidAdminUserCreate, "")
		return
	}
	if _, err := actionsecurity.ParseIdempotencyKey(keyValues); err != nil {
		adminUserCreateError(c, errInvalidAdminUserCreate, "")
		return
	}
	request, err := dto.DecodeAdminUserCreate(c.Request.Body)
	if err != nil {
		adminUserCreateDecodeError(c, err)
		return
	}
	defer request.ClearSecrets()
	action := actionsecurity.ActionUsersCreate
	ticketValues := []string(nil)
	if request.Role == models.UserRoleAdmin {
		action = actionsecurity.ActionUsersCreateAdmin
		values, canonical := exactHeaderValues(c, "X-Action-Ticket")
		if !canonical {
			adminUserCreateError(c, errInvalidAdminUserCreate, "")
			return
		}
		if _, err := actionsecurity.ParseTicket(values); err != nil {
			adminUserCreateError(c, errInvalidAdminUserCreate, "")
			return
		}
		ticketValues = values
	} else if request.Role != models.UserRoleUser || hasHeader(c, "X-Action-Ticket") {
		adminUserCreateError(c, errInvalidAdminUserCreate, "")
		return
	}

	// Begin owns the password carried by the canonical descriptor intent and its
	// encoder clears that slice. Keep a separate backing array for the one
	// permitted fresh-attempt hash after Begin reports ExecutionReady.
	hashPassword := append([]byte(nil), request.Password...)
	defer clear(hashPassword)
	intent := adminUserCreateIntent(request)
	defer clear(intent.Password)
	request.ClearSecrets()
	actor := adminUserActionActor(c)
	identity, view, err := backend.Begin(c.Request.Context(), service.OperationBegin{
		Action: action, Actor: actor, IdempotencyKeyValues: keyValues, TicketValues: ticketValues, Intent: intent,
	})
	if err != nil {
		adminUserActionError(c, err, "")
		return
	}
	if identity == nil || view == nil || identity.PublicRef != view.PublicRef || !validUserDeleteOperationRef(view.PublicRef) || view.Scope != createActionScope(action) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	if !backend.ExecutionReady(identity) {
		writeAdminUserCreateView(c, backend, action, actor, identity, view)
		return
	}
	passwordHash, err := backend.HashCreatePassword(hashPassword)
	clear(hashPassword)
	clear(intent.Password)
	intent.Password = nil
	if err != nil || len(passwordHash) == 0 {
		clear(passwordHash)
		adminUserCreateError(c, errInvalidAdminUserCreate, "")
		return
	}
	defer clear(passwordHash)
	trustedIP := ""
	if settings != nil {
		trustedIP = httpx.ClientIP(c, settings.TrustProxyHeaders, settings.TrustedProxyCIDRs)
	}
	execution, err := backend.NewCreateExecution(action, intent, passwordHash, service.CreateAccountRequestMetadata{
		RequestID: c.Writer.Header().Get("X-Request-ID"), TrustedIP: trustedIP,
	})
	clear(passwordHash)
	if err != nil || execution == nil {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	defer execution.ClearSecrets()
	view, err = backend.ExecuteCreate(c.Request.Context(), identity, execution)
	if err != nil {
		var unknown *service.CommitUnknownError
		if errors.As(err, &unknown) && unknown != nil && unknown.PublicRef == identity.PublicRef && validUserDeleteOperationRef(unknown.PublicRef) {
			adminUserActionError(c, err, unknown.PublicRef)
			return
		}
		adminUserActionError(c, err, "")
		return
	}
	writeAdminUserCreateView(c, backend, action, actor, identity, view)
}

func adminUserCreateIntent(request dto.AdminUserCreateRequest) actionsecurity.CreateAccountIntent {
	return actionsecurity.CreateAccountIntent{
		Username: request.Username, Nickname: request.Nickname, Password: append([]byte(nil), request.Password...), Role: request.Role.String(),
		GroupGUID: request.GroupGUID, PlanType: int(request.PlanType), AllowedModels: []string{}, DailyCallLimit: 100,
		Overrides: append([]actionsecurity.PermissionOverrideIntent(nil), request.PermissionOverrides...),
	}
}

func writeAdminUserCreateView(c *gin.Context, backend userManagementActionBackend, action actionsecurity.Action, actor service.ActionActor, identity *service.OperationIdentity, view *service.OperationView) {
	if identity == nil || view == nil || identity.PublicRef != view.PublicRef || view.Scope != createActionScope(action) || !validUserDeleteOperationRef(view.PublicRef) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	switch view.Status {
	case "succeeded":
		if view.FinishedAt == nil || view.FailureCode != nil || view.RetryAfterSeconds != 0 {
			adminUserActionError(c, service.ErrActionOperationUnavailable, "")
			return
		}
		response, status, err := backend.CreateOutcome(c.Request.Context(), action, actor, view.PublicRef)
		if errors.Is(err, service.ErrCreatedAccountDeleted) && response == nil && status == http.StatusGone {
			adminUserActionFixedError(c, http.StatusGone, "created_user_deleted", view.PublicRef)
			return
		}
		if err != nil || response == nil || status != http.StatusCreated || response.HTTPStatus != status || response.MediaType != "application/json" || len(response.Body) == 0 {
			adminUserActionError(c, service.ErrActionOperationUnavailable, "")
			return
		}
		c.Data(response.HTTPStatus, response.MediaType, response.Body)
	case "failed":
		if view.FinishedAt == nil || view.FailureCode == nil || view.RetryAfterSeconds != 0 {
			adminUserActionError(c, service.ErrActionOperationUnavailable, "")
			return
		}
		response, status, err := backend.CreateOutcome(c.Request.Context(), action, actor, view.PublicRef)
		if err != nil || response != nil {
			adminUserActionError(c, service.ErrActionOperationUnavailable, "")
			return
		}
		writeAdminUserCreateFailure(c, *view.FailureCode, status)
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

func writeAdminUserCreateFailure(c *gin.Context, failure string, status int) {
	switch {
	case failure == "consumer_validation_failed" && status == http.StatusConflict:
		adminUserActionFixedError(c, status, "username_conflict", "")
	case failure == "consumer_validation_failed" && status == http.StatusNotFound:
		adminUserActionFixedError(c, status, "action_group_not_found", "")
	case failure == "action_rejected" && status == http.StatusForbidden:
		adminUserActionFixedError(c, status, "action_operation_rejected", "")
	case status == http.StatusConflict && (failure == "action_rejected" || failure == "target_version_conflict" || failure == "policy_version_conflict" || failure == "target_state_conflict"):
		adminUserActionFixedError(c, status, failure, "")
	default:
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
	}
}

func createActionScope(action actionsecurity.Action) string {
	switch action {
	case actionsecurity.ActionUsersCreate:
		return "users.create"
	case actionsecurity.ActionUsersCreateAdmin:
		return "users.create_admin"
	default:
		return ""
	}
}

func queryUserManagementOperation(c *gin.Context, backend userManagementActionBackend) {
	var action actionsecurity.Action
	switch c.Request.URL.RawQuery {
	case userDeleteScopeQuery:
		action = actionsecurity.ActionUsersDelete
	case userCreateScopeQuery:
		action = actionsecurity.ActionUsersCreate
	case userCreateAdminScopeQuery:
		action = actionsecurity.ActionUsersCreateAdmin
	case userResetPasswordScopeQuery:
		action = actionsecurity.ActionUsersResetPassword
	default:
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	if c.Request.URL.RawPath != "" || hasHeader(c, "X-Action-Ticket") {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	keys, canonical := exactHeaderValues(c, "Idempotency-Key")
	if !canonical {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	if _, err := actionsecurity.ParseIdempotencyKey(keys); err != nil {
		adminUserActionError(c, errInvalidAdminUserAction, "")
		return
	}
	view, err := backend.Query(c.Request.Context(), action, adminUserActionActor(c), keys)
	if err != nil {
		adminUserActionError(c, err, "")
		return
	}
	if !validManagedOperationQueryView(view, createActionOrDeleteScope(action)) {
		adminUserActionError(c, service.ErrActionOperationUnavailable, "")
		return
	}
	c.Writer.Header().Del("Retry-After")
	if view.Status == "processing" {
		c.Header("Retry-After", strconv.Itoa(view.RetryAfterSeconds))
	}
	if action == actionsecurity.ActionUsersResetPassword {
		var target *string
		if view.TargetGUID != nil {
			value := strconv.FormatInt(*view.TargetGUID, 10)
			target = &value
		}
		c.JSON(http.StatusOK, dto.AdminUserPasswordResetQueryResponse{OperationRef: view.PublicRef, Scope: view.Scope, Status: view.Status, FinishedAt: view.FinishedAt, FailureCode: view.FailureCode, TargetGUID: target, ResultingAuthVersion: view.ResultAuthVersion})
		return
	}
	c.JSON(http.StatusOK, dto.UserDeleteQueryResponse{OperationRef: view.PublicRef, Scope: view.Scope, Status: view.Status, FinishedAt: view.FinishedAt, FailureCode: view.FailureCode})
}

func createActionOrDeleteScope(action actionsecurity.Action) string {
	if action == actionsecurity.ActionUsersDelete {
		return "users.delete"
	}
	if action == actionsecurity.ActionUsersResetPassword {
		return "users.reset_password"
	}
	return createActionScope(action)
}

func validManagedOperationQueryView(view *service.OperationView, scope string) bool {
	if view == nil || view.Scope != scope || !validUserDeleteOperationRef(view.PublicRef) {
		return false
	}
	switch view.Status {
	case "processing":
		return view.RetryAfterSeconds >= 1 && view.RetryAfterSeconds <= 30 && view.FinishedAt == nil && view.FailureCode == nil && view.TargetGUID == nil && view.ResultAuthVersion == nil
	case "succeeded":
		if scope == "users.reset_password" {
			return view.RetryAfterSeconds == 0 && view.FinishedAt != nil && view.FailureCode == nil && view.TargetGUID != nil && *view.TargetGUID > 0 && view.ResultAuthVersion != nil && *view.ResultAuthVersion > 0
		}
		return view.RetryAfterSeconds == 0 && view.FinishedAt != nil && view.FailureCode == nil && view.TargetGUID == nil && view.ResultAuthVersion == nil
	case "failed":
		return view.RetryAfterSeconds == 0 && view.FinishedAt != nil && safeUserDeleteFailure(view.FailureCode) != "" && view.TargetGUID == nil && view.ResultAuthVersion == nil
	case "pending_recovery":
		return view.RetryAfterSeconds == 0 && view.FinishedAt == nil && view.FailureCode == nil && view.TargetGUID == nil && view.ResultAuthVersion == nil
	default:
		return false
	}
}

func adminUserCreateDecodeError(c *gin.Context, err error) {
	if errors.Is(err, dto.ErrAdminUserCreateInactiveAction) {
		adminUserActionFixedError(c, http.StatusUnprocessableEntity, "action_inactive", "")
		return
	}
	adminUserCreateError(c, errInvalidAdminUserCreate, "")
}

func adminUserCreateError(c *gin.Context, err error, operationRef string) {
	if errors.Is(err, errInvalidAdminUserCreate) {
		adminUserActionFixedError(c, http.StatusBadRequest, "invalid_admin_user_create_request", "")
		return
	}
	adminUserActionError(c, err, operationRef)
}
