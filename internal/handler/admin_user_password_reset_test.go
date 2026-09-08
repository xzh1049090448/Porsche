package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type scriptedResetPasswordBackend struct {
	*scriptedUserManagementBackend
	hashCalls   int
	resetResult *service.ResetPasswordResult
}

func (b *scriptedResetPasswordBackend) HashResetPassword(password []byte) ([]byte, error) {
	b.hashCalls++
	return service.HashManagedCreationPasswordBytes(password)
}
func (*scriptedResetPasswordBackend) NewResetPasswordExecution(actionsecurity.ResetPasswordIntent, []byte, service.ResetPasswordRequestMetadata) (*service.ResetPasswordExecution, error) {
	return &service.ResetPasswordExecution{}, nil
}
func (b *scriptedResetPasswordBackend) ExecuteResetPassword(context.Context, *service.OperationIdentity, *service.ResetPasswordExecution) (*service.OperationView, error) {
	return b.executeView, b.executeErr
}
func (b *scriptedResetPasswordBackend) ResetPasswordOutcome(context.Context, service.ActionActor, string) (*service.ResetPasswordResult, error) {
	return b.resetResult, nil
}

func TestResetPasswordTerminalReplayDoesNotHashAndReturnsStoredResult(t *testing.T) {
	finished := int64(1_790_000_000_000)
	base := &scriptedUserManagementBackend{
		identity: &service.OperationIdentity{PublicRef: testOperationRef}, ready: false,
		beginView: &service.OperationView{PublicRef: testOperationRef, Scope: "users.reset_password", Status: "succeeded", FinishedAt: &finished},
	}
	backend := &scriptedResetPasswordBackend{scriptedUserManagementBackend: base, resetResult: &service.ResetPasswordResult{TargetGUID: 123, ResultingAuthVersion: 8}}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
	body := `{"action":"reset_password","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"rotation"}`
	rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users/123/actions", body, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertActionTestExactBody(t, rec, `{"operation_ref":"`+testOperationRef+`","target_guid":"123","resulting_auth_version":8}`)
	if backend.hashCalls != 0 {
		t.Fatalf("terminal replay hashed password %d times", backend.hashCalls)
	}
}

func TestResetPasswordQueryReturnsExactSevenFields(t *testing.T) {
	finished, target, version := int64(1_790_000_000_000), int64(123), 8
	base := &scriptedUserManagementBackend{queryView: &service.OperationView{PublicRef: testOperationRef, Scope: "users.reset_password", Status: "succeeded", FinishedAt: &finished, TargetGUID: &target, ResultAuthVersion: &version}}
	backend := &scriptedResetPasswordBackend{scriptedUserManagementBackend: base}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
	rec := performActionRequest(engine, http.MethodGet, "/admin/v2/operations?scope=users.reset_password", "", http.Header{"Idempotency-Key": {testActionKey}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertActionTestExactBody(t, rec, `{"operation_ref":"`+testOperationRef+`","scope":"users.reset_password","status":"succeeded","finished_at":1790000000000,"failure_code":null,"target_guid":"123","resulting_auth_version":8}`)
}

func TestResetPasswordIssueExactEnvelopeAndHeaders(t *testing.T) {
	base := &scriptedUserManagementBackend{issued: &service.IssuedVerification{Ticket: testActionTicket, ExpiresAt: 1_790_000_300_000}}
	backend := &scriptedResetPasswordBackend{scriptedUserManagementBackend: base}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
	body := `{"action":"users.reset_password","intent":{"target_guid":"123","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"rotation"},"current_password":"Actor!Pass1"}`
	rec := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", body, http.Header{"X-Request-ID": {"reset-issue-1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertActionTestExactBody(t, rec, `{"ticket":"`+testActionTicket+`","expires_at":1790000300000}`)
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") != "reset-issue-1" || base.issueCalls != 1 || base.issueAction != actionsecurity.ActionUsersResetPassword {
		t.Fatalf("headers/calls mismatch cache=%q request=%q calls=%d action=%d", rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"), base.issueCalls, base.issueAction)
	}
}

func TestResetPasswordExecuteRejectsStrictHeaderAndBodyShapes(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    string
		headers http.Header
	}{
		{"missing key", "/admin/v2/users/123/actions", `{"action":"reset_password","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"rotation"}`, http.Header{"X-Action-Ticket": {testActionTicket}}},
		{"duplicate ticket", "/admin/v2/users/123/actions", `{"action":"reset_password","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"rotation"}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket, testActionTicket}}},
		{"unknown body field", "/admin/v2/users/123/actions", `{"action":"reset_password","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"rotation","extra":true}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}},
		{"query suffix", "/admin/v2/users/123/actions?x=1", `{"action":"reset_password","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"rotation"}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}},
		{"invalid target", "/admin/v2/users/0/actions", `{"action":"reset_password","expected_auth_version":7,"new_password":"Strong!Pass1","reason":"rotation"}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := &scriptedUserManagementBackend{}
			backend := &scriptedResetPasswordBackend{scriptedUserManagementBackend: base}
			engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
			rec := performActionRequest(engine, http.MethodPost, test.path, test.body, test.headers)
			if rec.Code != http.StatusBadRequest || base.beginCalls != 0 || backend.hashCalls != 0 {
				t.Fatalf("status=%d begin=%d hash=%d body=%s", rec.Code, base.beginCalls, backend.hashCalls, rec.Body.String())
			}
			response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
			if response.Error.Code != "invalid_admin_action_request" || response.Error.Type != "admin_action_error" || response.Error.RequestID == "" {
				t.Fatalf("unexpected error envelope: %#v", response)
			}
		})
	}
}

func TestResetPasswordQueryRejectsNonCanonicalRequestWithoutBackendCall(t *testing.T) {
	for _, test := range []struct {
		path    string
		headers http.Header
	}{
		{"/admin/v2/operations?scope=users.reset_password&extra=1", http.Header{"Idempotency-Key": {testActionKey}}},
		{"/admin/v2/operations?scope=users.reset_password", http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}},
		{"/admin/v2/operations?scope=users.reset_password", http.Header{"Idempotency-Key": {testActionKey, testActionKey}}},
	} {
		base := &scriptedUserManagementBackend{}
		backend := &scriptedResetPasswordBackend{scriptedUserManagementBackend: base}
		engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
		rec := performActionRequest(engine, http.MethodGet, test.path, "", test.headers)
		if rec.Code != http.StatusBadRequest || base.queryCalls != 0 {
			t.Fatalf("status=%d query=%d body=%s", rec.Code, base.queryCalls, rec.Body.String())
		}
	}
}
