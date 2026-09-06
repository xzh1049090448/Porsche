package handler

import (
	"context"
	"crypto/sha256"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const adminUserCreatePassword = "Ex4mple!Pass1"

type scriptedUserManagementBackend struct {
	issued            *service.IssuedVerification
	issueErr          error
	identity          *service.OperationIdentity
	beginView         *service.OperationView
	beginErr          error
	ready             bool
	executeView       *service.OperationView
	executeErr        error
	queryView         *service.OperationView
	queryErr          error
	outcomeUser       *service.UserReadDTO
	outcomeStatus     int
	outcomeErr        error
	issueCalls        int
	beginCalls        int
	newCreateCalls    int
	executeCalls      int
	queryCalls        int
	outcomeCalls      int
	issueAction       actionsecurity.Action
	issueIntent       actionsecurity.CreateAccountIntent
	issueInitial      []byte
	issueCurrent      []byte
	issueInitialHash  [sha256.Size]byte
	issueCurrentHash  [sha256.Size]byte
	beginAction       actionsecurity.Action
	beginIntent       actionsecurity.CreateAccountIntent
	beginPassword     []byte
	beginPasswordHash [sha256.Size]byte
	beginKeys         []string
	beginTickets      []string
	createAction      actionsecurity.Action
	createIntent      actionsecurity.CreateAccountIntent
	createHash        []byte
	createHashValid   bool
	createMetadata    service.CreateAccountRequestMetadata
	queryAction       actionsecurity.Action
	queryKeys         []string
	outcomeAction     actionsecurity.Action
	outcomeActor      service.ActionActor
	outcomeRef        string
	outcomeExecution  *service.CreateAccountExecution
}

func (s *scriptedUserManagementBackend) Issue(_ context.Context, issue service.VerificationIssue) (*service.IssuedVerification, error) {
	s.issueCalls++
	s.issueAction = issue.Action
	s.issueIntent, _ = issue.Intent.(actionsecurity.CreateAccountIntent)
	s.issueInitial = s.issueIntent.Password
	s.issueCurrent = issue.CurrentPassword
	s.issueInitialHash = sha256.Sum256(issueIntentPassword(s.issueIntent))
	s.issueCurrentHash = sha256.Sum256(issue.CurrentPassword)
	return s.issued, s.issueErr
}

func (s *scriptedUserManagementBackend) Begin(_ context.Context, begin service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error) {
	s.beginCalls++
	s.beginAction = begin.Action
	s.beginIntent, _ = begin.Intent.(actionsecurity.CreateAccountIntent)
	s.beginPassword = s.beginIntent.Password
	s.beginPasswordHash = sha256.Sum256(s.beginIntent.Password)
	s.beginKeys = append([]string(nil), begin.IdempotencyKeyValues...)
	s.beginTickets = append([]string(nil), begin.TicketValues...)
	return s.identity, s.beginView, s.beginErr
}

func (s *scriptedUserManagementBackend) ExecutionReady(*service.OperationIdentity) bool {
	return s.ready
}

func (s *scriptedUserManagementBackend) NewDeleteExecution(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
	return &service.DeleteUserExecution{}, nil
}

func (s *scriptedUserManagementBackend) ExecuteDelete(context.Context, *service.OperationIdentity, *service.DeleteUserExecution) (*service.OperationView, error) {
	return nil, service.ErrActionOperationUnavailable
}

func (s *scriptedUserManagementBackend) NewCreateExecution(action actionsecurity.Action, intent actionsecurity.CreateAccountIntent, hash []byte, metadata service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error) {
	s.newCreateCalls++
	s.createAction = action
	s.createIntent = intent
	s.createHash = hash
	s.createHashValid = security.VerifyPassword(adminUserCreatePassword, string(hash))
	s.createMetadata = metadata
	return &service.CreateAccountExecution{}, nil
}

func (s *scriptedUserManagementBackend) ExecuteCreate(_ context.Context, _ *service.OperationIdentity, _ *service.CreateAccountExecution) (*service.OperationView, error) {
	s.executeCalls++
	return s.executeView, s.executeErr
}

func (s *scriptedUserManagementBackend) CreateOutcome(_ context.Context, action actionsecurity.Action, actor service.ActionActor, ref string, execution *service.CreateAccountExecution) (*service.UserReadDTO, int, error) {
	s.outcomeCalls++
	s.outcomeAction, s.outcomeActor, s.outcomeRef, s.outcomeExecution = action, actor, ref, execution
	return s.outcomeUser, s.outcomeStatus, s.outcomeErr
}

func (s *scriptedUserManagementBackend) Query(_ context.Context, action actionsecurity.Action, _ service.ActionActor, keys []string) (*service.OperationView, error) {
	s.queryCalls++
	s.queryAction = action
	s.queryKeys = append([]string(nil), keys...)
	return s.queryView, s.queryErr
}

func newScriptedUserManagementEngine(t *testing.T, backend userManagementActionBackend, role models.UserRole) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, func(c *gin.Context) {
		c.Set("user", &models.User{ID: 17, AuditFields: models.AuditFields{Guid: 1701}, Role: role, AuthVersion: 9})
		c.Set("session_sid", "11111111-2222-4333-8444-555555555555")
		c.Set("authenticated_session_version", 4)
		c.Next()
	})
	registerAdminUserManagementActionRoutes(g, backend, &config.Settings{TrustProxyHeaders: true, TrustedProxyCIDRs: "192.0.2.0/24"})
	return r
}

func adminUserCreateResult(role string) *service.UserReadDTO {
	username, nickname, group := "alice", "Alice", "default"
	return &service.UserReadDTO{
		GUID: "123456789012345678", Username: &username, Nickname: &nickname, Group: &group,
		PlanType: "free", Role: role, Status: "active", AuthVersion: 1,
		CreatedAt: "2026-09-06T00:00:00Z",
	}
}

func adminUserCreateBackend(role string) *scriptedUserManagementBackend {
	finished := int64(1790000000000)
	return &scriptedUserManagementBackend{
		identity: &service.OperationIdentity{PublicRef: testOperationRef}, ready: true,
		beginView:   &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "processing", RetryAfterSeconds: 30},
		executeView: &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "succeeded", FinishedAt: &finished},
		outcomeUser: adminUserCreateResult(role), outcomeStatus: http.StatusCreated,
	}
}

func TestAdminUserCreateOrdinaryUsesTicketlessActionHashAndExactDTO(t *testing.T) {
	backend := adminUserCreateBackend("user")
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
	body := `{"username":"alice","nickname":"Alice","password":"` + adminUserCreatePassword + `","role":"user","group_guid":null,"plan_type":"free","permission_overrides":[]}`
	rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users", body, http.Header{
		"Idempotency-Key": {testActionKey}, "X-Request-ID": {"request-create-1"}, "X-Forwarded-For": {"203.0.113.8"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body_length=%d", rec.Code, rec.Body.Len())
	}
	want := `{"operation_ref":"` + testOperationRef + `","user":{"guid":"123456789012345678","username":"alice","nickname":"Alice","email":null,"group":"default","plan_type":"free","role":"user","status":"active","auth_version":1,"created_at":"2026-09-06T00:00:00Z","last_login_at":null},"permissions_version":null}`
	assertActionTestExactBody(t, rec, want)
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") != "request-create-1" {
		t.Fatalf("security headers cache=%q request_id=%q", rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
	}
	passwordDigest := sha256.Sum256([]byte(adminUserCreatePassword))
	if backend.beginCalls != 1 || backend.beginAction != actionsecurity.ActionUsersCreate || len(backend.beginTickets) != 0 ||
		backend.beginPasswordHash != passwordDigest || backend.newCreateCalls != 1 || backend.createAction != actionsecurity.ActionUsersCreate ||
		backend.createIntent.Password != nil || backend.createIntent.Role != "user" || backend.createIntent.PlanType != int(models.PlanFree) ||
		len(backend.createIntent.AllowedModels) != 0 || backend.createIntent.DailyCallLimit != 100 || len(backend.createIntent.Overrides) != 0 ||
		!backend.createHashValid || backend.createMetadata.RequestID != "request-create-1" ||
		backend.createMetadata.TrustedIP != "203.0.113.8" || backend.executeCalls != 1 || backend.outcomeCalls != 1 || backend.outcomeExecution == nil {
		t.Fatal("ordinary create did not preserve the reviewed action, defaults, hash, metadata, and execution flow")
	}
	for _, secret := range [][]byte{backend.beginPassword, backend.createHash} {
		for _, value := range secret {
			if value != 0 {
				t.Fatal("create handler retained an owned password buffer")
			}
		}
	}
}

func TestAdminUserCreateAdminVerificationThenCreateUsesExactActionAndSeparateSecrets(t *testing.T) {
	backend := adminUserCreateBackend("admin")
	backend.issued = &service.IssuedVerification{Ticket: testActionTicket, ExpiresAt: 1790000300000}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
	verificationBody := `{"action":"users.create_admin","intent":{"username":"alice","nickname":"Alice","password":"` + adminUserCreatePassword + `","role":"admin","group_guid":null,"plan_type":"free","permission_overrides":[]},"current_password":"Current!Pass9"}`
	issue := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", verificationBody, http.Header{"X-Request-ID": {"request-verify-1"}})
	if issue.Code != http.StatusCreated {
		t.Fatalf("issue status=%d body_length=%d", issue.Code, issue.Body.Len())
	}
	assertActionTestExactBody(t, issue, `{"ticket":"`+testActionTicket+`","expires_at":1790000300000}`)
	if backend.issueCalls != 1 || backend.issueAction != actionsecurity.ActionUsersCreateAdmin || backend.issueIntent.Role != "admin" ||
		backend.issueInitialHash != sha256.Sum256([]byte(adminUserCreatePassword)) ||
		backend.issueCurrentHash != sha256.Sum256([]byte("Current!Pass9")) ||
		(len(backend.issueInitial) > 0 && &backend.issueInitial[0] == &backend.issueCurrent[0]) {
		t.Fatal("administrator verification did not keep separate exact credential ownership")
	}
	for _, secret := range [][]byte{backend.issueInitial, backend.issueCurrent} {
		for _, value := range secret {
			if value != 0 {
				t.Fatal("administrator verification retained an owned password buffer")
			}
		}
	}

	backend.beginView.Scope = "users.create_admin"
	backend.executeView.Scope = "users.create_admin"
	createBody := `{"username":"alice","nickname":"Alice","password":"` + adminUserCreatePassword + `","role":"admin","group_guid":null,"plan_type":"free","permission_overrides":[]}`
	created := performActionRequest(engine, http.MethodPost, "/admin/v2/users", createBody, http.Header{
		"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}, "X-Request-ID": {"request-create-admin"},
	})
	if created.Code != http.StatusCreated || backend.beginAction != actionsecurity.ActionUsersCreateAdmin || len(backend.beginTickets) != 1 || backend.beginTickets[0] != testActionTicket {
		t.Fatalf("admin create status/action/tickets=%d/%d/%v", created.Code, backend.beginAction, backend.beginTickets)
	}
	want := `{"operation_ref":"` + testOperationRef + `","user":{"guid":"123456789012345678","username":"alice","nickname":"Alice","email":null,"group":"default","plan_type":"free","role":"admin","status":"active","auth_version":1,"created_at":"2026-09-06T00:00:00Z","last_login_at":null},"permissions_version":"1"}`
	assertActionTestExactBody(t, created, want)
}

func TestAdminUserCreateVerificationDispatchesDeleteAndRejectsEveryOtherAction(t *testing.T) {
	backend := adminUserCreateBackend("user")
	backend.issued = &service.IssuedVerification{Ticket: testActionTicket, ExpiresAt: 1790000300000}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
	deleted := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications",
		`{"action":"users.delete","intent":{"target_guid":"123","expected_auth_version":7,"reason":"duplicate"},"current_password":"Current!Pass9"}`, nil)
	if deleted.Code != http.StatusCreated || backend.issueCalls != 1 || backend.issueAction != actionsecurity.ActionUsersDelete {
		t.Fatalf("delete dispatch status/calls/action=%d/%d/%d", deleted.Code, backend.issueCalls, backend.issueAction)
	}
	for _, action := range []string{"users.create", "users.promote"} {
		before := backend.issueCalls
		body := `{"action":"` + action + `","intent":{},"current_password":"Current!Pass9"}`
		rec := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", body, nil)
		if rec.Code != http.StatusUnprocessableEntity || backend.issueCalls != before {
			t.Fatalf("action=%q status/calls=%d/%d", action, rec.Code, backend.issueCalls)
		}
		response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
		if response.Error.Code != "action_inactive" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
			t.Fatalf("action=%q error/header=%q/%q/%q", action, response.Error.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
		}
	}
}

func TestAdminUserCreateRejectsForbiddenHeaderAndRoleCombinationsBeforeBegin(t *testing.T) {
	validUser := `{"username":"alice","password":"` + adminUserCreatePassword + `","role":"user"}`
	validAdmin := `{"username":"alice","password":"` + adminUserCreatePassword + `","role":"admin"}`
	tests := []struct {
		name    string
		path    string
		body    string
		headers http.Header
	}{
		{"ordinary ticket", "/admin/v2/users", validUser, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}},
		{"ordinary empty ticket", "/admin/v2/users", validUser, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {""}}},
		{"admin missing ticket", "/admin/v2/users", validAdmin, http.Header{"Idempotency-Key": {testActionKey}}},
		{"admin duplicate ticket", "/admin/v2/users", validAdmin, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket, testActionTicket}}},
		{"missing key", "/admin/v2/users", validUser, nil},
		{"duplicate key", "/admin/v2/users", validUser, http.Header{"Idempotency-Key": {testActionKey, testActionKey}}},
		{"create query", "/admin/v2/users?scope=users.create", validUser, http.Header{"Idempotency-Key": {testActionKey}}},
		{"root role", "/admin/v2/users", `{"username":"alice","password":"` + adminUserCreatePassword + `","role":"root"}`, http.Header{"Idempotency-Key": {testActionKey}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := adminUserCreateBackend("user")
			engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
			rec := performActionRequest(engine, http.MethodPost, test.path, test.body, test.headers)
			if rec.Code != http.StatusBadRequest || backend.beginCalls != 0 {
				t.Fatalf("status=%d begin_calls=%d body_length=%d", rec.Code, backend.beginCalls, rec.Body.Len())
			}
			response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
			if response.Error.Code != "invalid_admin_user_create_request" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
				t.Fatalf("error/header contract = %q/%q/%q", response.Error.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
			}
		})
	}
}

func TestAdminUserCreateReplayAndA03FailureCodesDoNotReexecute(t *testing.T) {
	finished := int64(1790000000000)
	tests := []struct {
		name          string
		view          *service.OperationView
		outcomeStatus int
		outcomeUser   *service.UserReadDTO
		wantStatus    int
		wantCode      string
	}{
		{"replay", &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "succeeded", FinishedAt: &finished}, 201, adminUserCreateResult("user"), 201, ""},
		{"username conflict", &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "failed", FinishedAt: &finished, FailureCode: stringPointer("consumer_validation_failed")}, 409, nil, 409, "username_conflict"},
		{"group hidden", &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "failed", FinishedAt: &finished, FailureCode: stringPointer("consumer_validation_failed")}, 404, nil, 404, "action_group_not_found"},
		{"capability rejected", &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "failed", FinishedAt: &finished, FailureCode: stringPointer("action_rejected")}, 403, nil, 403, "action_operation_rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := adminUserCreateBackend("user")
			backend.ready = false
			backend.beginView = test.view
			backend.outcomeStatus, backend.outcomeUser = test.outcomeStatus, test.outcomeUser
			engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
			rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users", `{"username":"alice","password":"`+adminUserCreatePassword+`","role":"user"}`, http.Header{"Idempotency-Key": {testActionKey}})
			if rec.Code != test.wantStatus || backend.newCreateCalls != 0 || backend.executeCalls != 0 || backend.outcomeCalls != 1 {
				t.Fatalf("status/new/execute/outcome=%d/%d/%d/%d", rec.Code, backend.newCreateCalls, backend.executeCalls, backend.outcomeCalls)
			}
			if test.wantCode != "" {
				response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
				if response.Error.Code != test.wantCode {
					t.Fatalf("code=%q want=%q", response.Error.Code, test.wantCode)
				}
			}
		})
	}
}

func TestAdminUserCreateRoleCapabilityAndDependencyOutcomesKeepSafeHeaders(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		actor      models.UserRole
		err        error
		wantStatus int
		wantCode   string
		wantRetry  string
	}{
		{"user actor denied ordinary", "user", models.UserRoleUser, service.ErrActionOperationForbidden, 403, "action_operation_rejected", ""},
		{"admin denied administrator", "admin", models.UserRoleAdmin, service.ErrActionOperationForbidden, 403, "action_operation_rejected", ""},
		{"users create capability denied", "user", models.UserRoleAdmin, service.ErrActionOperationForbidden, 403, "action_operation_rejected", ""},
		{"inactive", "user", models.UserRoleRoot, service.ErrActionOperationInactive, 422, "action_inactive", ""},
		{"rate limited", "user", models.UserRoleRoot, &service.RetryAfterError{Seconds: 9}, 429, "action_rate_limited", "9"},
		{"dependency unavailable", "user", models.UserRoleRoot, service.ErrActionOperationUnavailable, 503, "action_dependency_unavailable", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := adminUserCreateBackend(test.role)
			backend.beginErr = test.err
			engine := newScriptedUserManagementEngine(t, backend, test.actor)
			headers := http.Header{"Idempotency-Key": {testActionKey}, "X-Request-ID": {"request-outcome"}}
			if test.role == "admin" {
				headers.Set("X-Action-Ticket", testActionTicket)
			}
			rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users", `{"username":"alice","password":"`+adminUserCreatePassword+`","role":"`+test.role+`"}`, headers)
			if rec.Code != test.wantStatus || backend.beginCalls != 1 || backend.newCreateCalls != 0 || rec.Header().Get("Retry-After") != test.wantRetry {
				t.Fatalf("status/begin/new/retry=%d/%d/%d/%q", rec.Code, backend.beginCalls, backend.newCreateCalls, rec.Header().Get("Retry-After"))
			}
			response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
			if response.Error.Code != test.wantCode || response.Error.RequestID != "request-outcome" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") != "request-outcome" {
				t.Fatalf("code/request/cache/header=%q/%q/%q/%q", response.Error.Code, response.Error.RequestID, rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
			}
		})
	}
}

func TestAdminUserCreateQueryDispatchesOnlyExactCreateScopes(t *testing.T) {
	for _, test := range []struct {
		scope  string
		action actionsecurity.Action
	}{{"users.create", actionsecurity.ActionUsersCreate}, {"users.create_admin", actionsecurity.ActionUsersCreateAdmin}} {
		backend := adminUserCreateBackend("user")
		backend.queryView = &service.OperationView{PublicRef: testOperationRef, Scope: test.scope, Status: "processing", RetryAfterSeconds: 7}
		engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
		rec := performActionRequest(engine, http.MethodGet, "/admin/v2/operations?scope="+test.scope, "", http.Header{"Idempotency-Key": {testActionKey}})
		if rec.Code != http.StatusOK || backend.queryCalls != 1 || backend.queryAction != test.action || len(backend.queryKeys) != 1 || backend.queryKeys[0] != testActionKey || rec.Header().Get("Retry-After") != "7" {
			t.Fatalf("scope=%q status/calls/action/keys/retry=%d/%d/%d/%v/%q", test.scope, rec.Code, backend.queryCalls, backend.queryAction, backend.queryKeys, rec.Header().Get("Retry-After"))
		}
	}
	for _, path := range []string{
		"/admin/v2/operations?scope=users.create&scope=users.create", "/admin/v2/operations?scope=users.create%5fadmin",
		"/admin/v2/operations?scope=users.create_admin&x=1", "/admin/v2/operations?scope=users.promote",
	} {
		backend := adminUserCreateBackend("user")
		engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
		rec := performActionRequest(engine, http.MethodGet, path, "", http.Header{"Idempotency-Key": {testActionKey}})
		if rec.Code != http.StatusBadRequest || backend.queryCalls != 0 {
			t.Fatalf("path=%q status=%d calls=%d", path, rec.Code, backend.queryCalls)
		}
	}
}

func stringPointer(value string) *string { return &value }

func issueIntentPassword(intent actionsecurity.CreateAccountIntent) []byte { return intent.Password }
