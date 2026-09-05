package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const (
	testActionKey    = "ik_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testActionTicket = "av_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testOperationRef = "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

type scriptedUserDeleteBackend struct {
	issued              *service.IssuedVerification
	issueErr            error
	identity            *service.OperationIdentity
	beginView           *service.OperationView
	beginErr            error
	ready               bool
	executeView         *service.OperationView
	executeErr          error
	queryView           *service.OperationView
	queryErr            error
	executionErr        error
	issueCalls          int
	beginCalls          int
	executeCalls        int
	queryCalls          int
	issueInputMatches   bool
	issuePasswordDigest [sha256.Size]byte
	issuePasswordLength int
	beginInputMatches   bool
	queryInputMatches   bool
}

func (s *scriptedUserDeleteBackend) Issue(_ context.Context, in service.VerificationIssue) (*service.IssuedVerification, error) {
	s.issueCalls++
	intent, intentOK := in.Intent.(actionsecurity.DeleteUserIntent)
	s.issueInputMatches = intentOK && in.Action == actionsecurity.ActionUsersDelete && in.TargetGUID != nil && *in.TargetGUID == 123 &&
		intent.TargetGUID == 123 && intent.ExpectedAuthVersion == 7 && intent.Reason == "duplicate account" &&
		in.Actor.UserID == 17 && in.Actor.UserGUID == 1701 && in.Actor.AuthVersion == 9 &&
		in.Actor.SessionSID == "11111111-2222-4333-8444-555555555555" && in.Actor.SessionVersion == 4 && in.TrustedIP == "203.0.113.8"
	s.issuePasswordLength = len(in.CurrentPassword)
	s.issuePasswordDigest = sha256.Sum256(in.CurrentPassword)
	return s.issued, s.issueErr
}
func (s *scriptedUserDeleteBackend) Begin(_ context.Context, in service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error) {
	s.beginCalls++
	intent, intentOK := in.Intent.(actionsecurity.DeleteUserIntent)
	s.beginInputMatches = intentOK && in.Action == actionsecurity.ActionUsersDelete &&
		intent.TargetGUID == 123 && intent.ExpectedAuthVersion == 7 && intent.Reason == "duplicate account" &&
		in.Actor.UserID == 17 && in.Actor.UserGUID == 1701 && in.Actor.AuthVersion == 9 &&
		in.Actor.SessionSID == "11111111-2222-4333-8444-555555555555" && in.Actor.SessionVersion == 4 &&
		len(in.IdempotencyKeyValues) == 1 && in.IdempotencyKeyValues[0] == testActionKey &&
		len(in.TicketValues) == 1 && in.TicketValues[0] == testActionTicket
	return s.identity, s.beginView, s.beginErr
}
func (s *scriptedUserDeleteBackend) ExecutionReady(*service.OperationIdentity) bool { return s.ready }
func (s *scriptedUserDeleteBackend) NewExecution(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
	return &service.DeleteUserExecution{}, s.executionErr
}
func (s *scriptedUserDeleteBackend) Execute(_ context.Context, _ *service.OperationIdentity, _ *service.DeleteUserExecution) (*service.OperationView, error) {
	s.executeCalls++
	return s.executeView, s.executeErr
}
func (s *scriptedUserDeleteBackend) Query(_ context.Context, action actionsecurity.Action, actor service.ActionActor, keys []string) (*service.OperationView, error) {
	s.queryCalls++
	s.queryInputMatches = action == actionsecurity.ActionUsersDelete && actor.UserID == 17 && actor.UserGUID == 1701 && actor.AuthVersion == 9 &&
		actor.SessionSID == "11111111-2222-4333-8444-555555555555" && actor.SessionVersion == 4 &&
		len(keys) == 1 && keys[0] == testActionKey
	return s.queryView, s.queryErr
}

func newScriptedUserDeleteEngine(t *testing.T, backend userDeleteActionBackend, settings *config.Settings) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, func(c *gin.Context) {
		c.Set("user", &models.User{ID: 17, AuditFields: models.AuditFields{Guid: 1701}, AuthVersion: 9})
		c.Set("session_sid", "11111111-2222-4333-8444-555555555555")
		c.Set("authenticated_session_version", 4)
		c.Next()
	})
	registerAdminUserActionRoutes(g, backend, settings)
	return r
}

func performActionRequest(engine http.Handler, method, path, body string, headers http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestAdminUserActionIssueBuildsTrustedInputAndExactSuccess(t *testing.T) {
	backend := &scriptedUserDeleteBackend{issued: &service.IssuedVerification{Ticket: testActionTicket, ExpiresAt: 1790000300000}}
	settings := &config.Settings{TrustProxyHeaders: true, TrustedProxyCIDRs: "192.0.2.0/24"}
	engine := newScriptedUserDeleteEngine(t, backend, settings)
	body := `{"action":"users.delete","intent":{"target_guid":"123","expected_auth_version":7,"reason":"  duplicate account  "},"current_password":"actor-secret"}`
	headers := http.Header{"X-Request-ID": {"request-1"}, "X-Forwarded-For": {"203.0.113.8"}}
	rec := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", body, headers)
	if rec.Code != http.StatusCreated || rec.Body.String() != `{"ticket":"`+testActionTicket+`","expires_at":1790000300000}` {
		t.Fatalf("status/body=%d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") != "request-1" {
		t.Fatalf("security headers differ: cache=%q request_id=%q", rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
	}
	wantPasswordDigest := sha256.Sum256([]byte("actor-secret"))
	if backend.issueCalls != 1 || !backend.issueInputMatches || backend.issuePasswordLength != len("actor-secret") || backend.issuePasswordDigest != wantPasswordDigest {
		t.Fatalf("issue observation mismatch: calls=%d input_match=%t password_length=%d password_digest_match=%t", backend.issueCalls, backend.issueInputMatches, backend.issuePasswordLength, backend.issuePasswordDigest == wantPasswordDigest)
	}
}

func TestAdminUserActionIssueRejectsAnySensitiveHeadersBeforeService(t *testing.T) {
	for _, headers := range []http.Header{
		{"Idempotency-Key": {""}}, {"Idempotency-Key": {testActionKey, testActionKey}}, {"X-Action-Ticket": {""}},
	} {
		backend := &scriptedUserDeleteBackend{}
		engine := newScriptedUserDeleteEngine(t, backend, &config.Settings{})
		rec := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", `{}`, headers)
		if rec.Code != 400 || backend.issueCalls != 0 || !strings.Contains(rec.Body.String(), `"code":"invalid_admin_action_request"`) {
			t.Fatalf("sensitive header shape result: status=%d calls=%d body=%s", rec.Code, backend.issueCalls, rec.Body.String())
		}
	}
}

func TestAdminUserActionExecuteUsesRawHeadersOnceAndDoesNotReplay(t *testing.T) {
	finished := int64(1790000000000)
	backend := &scriptedUserDeleteBackend{
		identity: &service.OperationIdentity{PublicRef: testOperationRef}, ready: true,
		beginView:   &service.OperationView{PublicRef: testOperationRef, Scope: "users.delete", Status: "processing", RetryAfterSeconds: 30},
		executeView: &service.OperationView{PublicRef: testOperationRef, Scope: "users.delete", Status: "succeeded", FinishedAt: &finished},
	}
	engine := newScriptedUserDeleteEngine(t, backend, &config.Settings{})
	body := `{"action":"delete","expected_auth_version":7,"reason":" duplicate account "}`
	headers := http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}, "X-Request-ID": {"request-2"}}
	rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users/123/actions", body, headers)
	if rec.Code != 200 || rec.Body.String() != `{"operation_ref":"`+testOperationRef+`","user":{"guid":"123","status":"deleted"}}` {
		t.Fatalf("status/body=%d %s", rec.Code, rec.Body.String())
	}
	if backend.beginCalls != 1 || backend.executeCalls != 1 || !backend.beginInputMatches {
		t.Fatalf("execute observation mismatch: begin_calls=%d execute_calls=%d input_match=%t", backend.beginCalls, backend.executeCalls, backend.beginInputMatches)
	}
}

func TestAdminUserActionExecuteExistingViewsNeverExecute(t *testing.T) {
	failed := "target_version_conflict"
	finished := int64(1790000000000)
	tests := []struct {
		name       string
		view       *service.OperationView
		wantStatus int
		wantCode   string
	}{
		{"succeeded", &service.OperationView{PublicRef: testOperationRef, Scope: "users.delete", Status: "succeeded", FinishedAt: &finished}, 200, ""},
		{"failed", &service.OperationView{PublicRef: testOperationRef, Scope: "users.delete", Status: "failed", FinishedAt: &finished, FailureCode: &failed}, 409, failed},
		{"processing", &service.OperationView{PublicRef: testOperationRef, Scope: "users.delete", Status: "processing", RetryAfterSeconds: 3}, 503, "operation_commit_unknown"},
		{"pending recovery", &service.OperationView{PublicRef: testOperationRef, Scope: "users.delete", Status: "pending_recovery"}, 503, "operation_commit_unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &scriptedUserDeleteBackend{identity: &service.OperationIdentity{PublicRef: testOperationRef}, beginView: test.view}
			engine := newScriptedUserDeleteEngine(t, backend, &config.Settings{})
			rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users/123/actions", `{"action":"delete","expected_auth_version":7,"reason":"reason"}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}})
			if rec.Code != test.wantStatus || backend.executeCalls != 0 || (test.wantCode != "" && !strings.Contains(rec.Body.String(), `"code":"`+test.wantCode+`"`)) {
				t.Fatalf("status/body/execute=%d %s %d", rec.Code, rec.Body.String(), backend.executeCalls)
			}
		})
	}
}

func TestAdminUserActionQueryIsExactAndSetsRetryAfterOnlyForProcessing(t *testing.T) {
	backend := &scriptedUserDeleteBackend{queryView: &service.OperationView{PublicRef: testOperationRef, Scope: "users.delete", Status: "processing", RetryAfterSeconds: 7}}
	engine := newScriptedUserDeleteEngine(t, backend, &config.Settings{})
	rec := performActionRequest(engine, http.MethodGet, "/admin/v2/operations?scope=users.delete", "", http.Header{"Idempotency-Key": {testActionKey}})
	if rec.Code != 200 || rec.Header().Get("Retry-After") != "7" || rec.Body.String() != `{"operation_ref":"`+testOperationRef+`","scope":"users.delete","status":"processing","finished_at":null,"failure_code":null}` || backend.queryCalls != 1 {
		t.Fatalf("query result mismatch: status=%d retry_after=%q calls=%d body=%s", rec.Code, rec.Header().Get("Retry-After"), backend.queryCalls, rec.Body.String())
	}
	if !backend.queryInputMatches {
		t.Fatal("query input observation mismatch")
	}
	for _, path := range []string{"/admin/v2/operations", "/admin/v2/operations?scope=", "/admin/v2/operations?scope=%75sers.delete", "/admin/v2/operations?scope=users.delete&scope=users.delete", "/admin/v2/operations?scope=users.delete&x=1"} {
		before := backend.queryCalls
		rec := performActionRequest(engine, http.MethodGet, path, "", http.Header{"Idempotency-Key": {testActionKey}})
		if rec.Code != 400 || backend.queryCalls != before {
			t.Fatalf("path=%s status/body/calls=%d %s %d", path, rec.Code, rec.Body.String(), backend.queryCalls)
		}
	}
	finished := int64(1790000000000)
	backend.queryView = &service.OperationView{PublicRef: testOperationRef, Scope: "users.delete", Status: "succeeded", FinishedAt: &finished}
	rec = performActionRequest(engine, http.MethodGet, "/admin/v2/operations?scope=users.delete", "", http.Header{"Idempotency-Key": {testActionKey}})
	_, retryPresent := rec.Header()["Retry-After"]
	if rec.Code != 200 || retryPresent || rec.Body.String() != `{"operation_ref":"`+testOperationRef+`","scope":"users.delete","status":"succeeded","finished_at":1790000000000,"failure_code":null}` {
		t.Fatalf("terminal query mismatch: status=%d retry_header_present=%t body=%s", rec.Code, retryPresent, rec.Body.String())
	}
}

func TestAdminUserActionRejectsMalformedPathHeadersBodiesAndWrongAction(t *testing.T) {
	tests := []struct {
		method, path, body string
		headers            http.Header
		status             int
	}{
		{http.MethodPost, "/admin/v2/users/0123/actions", `{"action":"delete","expected_auth_version":7,"reason":"reason"}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}, 400},
		{http.MethodPost, "/admin/v2/users/123/actions?x=1", `{}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}, 400},
		{http.MethodPost, "/admin/v2/users/123/actions", `{}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}, 400},
		{http.MethodPost, "/admin/v2/action-verifications", `{"action":"users.promote","intent":{"target_guid":"123","expected_auth_version":7,"reason":"reason"},"current_password":"secret"}`, nil, 422},
		{http.MethodPost, "/admin/v2/users/123/actions", `{"action":"promote","expected_auth_version":7,"reason":"reason"}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}, 422},
		{http.MethodPost, "/admin/v2/users/123/actions", `{"action":"delete","expected_auth_version":7,"reason":"reason"}`, http.Header{"Idempotency-Key": {testActionKey + "," + testActionKey}, "X-Action-Ticket": {testActionTicket}}, 400},
		{http.MethodGet, "/admin/v2/operations?scope=users.delete", "", http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {""}}, 400},
	}
	for _, test := range tests {
		backend := &scriptedUserDeleteBackend{}
		engine := newScriptedUserDeleteEngine(t, backend, &config.Settings{})
		rec := performActionRequest(engine, test.method, test.path, test.body, test.headers)
		if rec.Code != test.status || backend.issueCalls+backend.beginCalls+backend.queryCalls != 0 {
			t.Fatalf("%s %s status/body/calls=%d %s %d", test.method, test.path, rec.Code, rec.Body.String(), backend.issueCalls+backend.beginCalls+backend.queryCalls)
		}
	}
}

func TestAdminUserActionExecuteRejectsEveryMalformedHeaderShapeBeforeBegin(t *testing.T) {
	tests := []http.Header{
		{"X-Action-Ticket": {testActionTicket}},
		{"Idempotency-Key": {testActionKey}},
		{"Idempotency-Key": {""}, "X-Action-Ticket": {testActionTicket}},
		{"Idempotency-Key": {testActionKey, testActionKey}, "X-Action-Ticket": {testActionTicket}},
		{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket, testActionTicket}},
		{"Idempotency-Key": {testActionKey + "," + testActionKey}, "X-Action-Ticket": {testActionTicket}},
		{"Idempotency-Key": {testActionKey + " "}, "X-Action-Ticket": {testActionTicket}},
		{"Idempotency-Key": {"IK_" + strings.TrimPrefix(testActionKey, "ik_")}, "X-Action-Ticket": {testActionTicket}},
		{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {"AV_" + strings.TrimPrefix(testActionTicket, "av_")}},
	}
	for _, headers := range tests {
		backend := &scriptedUserDeleteBackend{}
		engine := newScriptedUserDeleteEngine(t, backend, &config.Settings{})
		rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users/123/actions", `{"action":"delete","expected_auth_version":7,"reason":"reason"}`, headers)
		if rec.Code != 400 || backend.beginCalls != 0 {
			t.Fatalf("malformed header result: status=%d begin_calls=%d body=%s", rec.Code, backend.beginCalls, rec.Body.String())
		}
	}
}

func TestAdminUserActionErrorMappingIsExhaustiveAndRedacted(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrActionVerificationInactive, 422, "action_inactive"},
		{service.ErrActionVerificationForbidden, 403, "action_verification_rejected"},
		{service.ErrActionVerificationHidden, 404, "action_target_not_found"},
		{service.ErrActionVerificationConflict, 409, "action_verification_conflict"},
		{service.ErrActionVerificationUnavailable, 503, "action_dependency_unavailable"},
		{service.ErrActionOperationInactive, 422, "action_inactive"},
		{service.ErrActionOperationForbidden, 403, "action_operation_rejected"},
		{service.ErrActionOperationHidden, 404, "action_operation_not_found"},
		{service.ErrActionOperationConflict, 409, "idempotency_conflict"},
		{service.ErrActionOperationCrossSession, 409, "idempotency_cross_session"},
		{service.ErrActionOperationExpired, 410, "operation_expired"},
		{service.ErrActionOperationUnavailable, 503, "action_dependency_unavailable"},
		{errors.New("private database ticket password reason"), 503, "action_dependency_unavailable"},
	}
	for _, test := range tests {
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		c.Header("X-Request-ID", "request-errors")
		adminUserActionError(c, test.err, "")
		want := `{"error":{"code":"` + test.code + `","message":"请求无法完成","type":"admin_action_error","request_id":"request-errors"}}`
		if r.Code != test.status || r.Body.String() != want || strings.Contains(r.Body.String(), "private") {
			t.Fatalf("error mapping mismatch: code=%q status=%d want_status=%d body=%s", test.code, r.Code, test.status, r.Body.String())
		}
	}
}

func TestAdminUserActionRateLimitAndCommitUnknownExposeOnlySafeMetadata(t *testing.T) {
	backend := &scriptedUserDeleteBackend{issueErr: &service.RetryAfterError{Seconds: 13}}
	engine := newScriptedUserDeleteEngine(t, backend, &config.Settings{})
	issue := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", `{"action":"users.delete","intent":{"target_guid":"123","expected_auth_version":7,"reason":"reason"},"current_password":"secret"}`, nil)
	if issue.Code != 429 || issue.Header().Get("Retry-After") != "13" || !strings.Contains(issue.Body.String(), `"code":"action_rate_limited"`) || strings.Contains(issue.Body.String(), "secret") {
		t.Fatalf("rate response mismatch: status=%d retry_after=%q body=%s", issue.Code, issue.Header().Get("Retry-After"), issue.Body.String())
	}

	backend = &scriptedUserDeleteBackend{
		identity: &service.OperationIdentity{PublicRef: testOperationRef}, ready: true,
		beginView:  &service.OperationView{PublicRef: testOperationRef, Scope: "users.delete", Status: "processing", RetryAfterSeconds: 30},
		executeErr: &service.CommitUnknownError{PublicRef: testOperationRef, Cause: errors.New("private commit dependency")},
	}
	engine = newScriptedUserDeleteEngine(t, backend, &config.Settings{})
	execute := performActionRequest(engine, http.MethodPost, "/admin/v2/users/123/actions", `{"action":"delete","expected_auth_version":7,"reason":"private reason"}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}})
	want := `{"error":{"code":"operation_commit_unknown","message":"请求无法完成","type":"admin_action_error","request_id":"` + execute.Header().Get("X-Request-ID") + `","operation_ref":"` + testOperationRef + `"}}`
	if execute.Code != 503 || execute.Body.String() != want || backend.executeCalls != 1 || strings.Contains(execute.Body.String(), "private") {
		t.Fatalf("unknown response=%d %s execute=%d", execute.Code, execute.Body.String(), backend.executeCalls)
	}
}
