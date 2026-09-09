package handler

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type scriptedRolePermissionBackend struct {
	*scriptedUserManagementBackend
	issueIntent  any
	beginIntent  any
	newCalls     int
	executeCalls int
}

func (backend *scriptedRolePermissionBackend) Issue(_ context.Context, issue service.VerificationIssue) (*service.IssuedVerification, error) {
	backend.issueCalls++
	backend.issueAction = issue.Action
	backend.issueIntent = issue.Intent
	return backend.issued, backend.issueErr
}

func (backend *scriptedRolePermissionBackend) Begin(_ context.Context, begin service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error) {
	backend.beginCalls++
	backend.beginAction = begin.Action
	backend.beginIntent = begin.Intent
	backend.beginKeys = append([]string(nil), begin.IdempotencyKeyValues...)
	backend.beginTickets = append([]string(nil), begin.TicketValues...)
	return backend.identity, backend.beginView, backend.beginErr
}

func (backend *scriptedRolePermissionBackend) NewPromoteExecution(actionsecurity.PromoteIntent) (*service.RolePermissionExecution, error) {
	backend.newCalls++
	return &service.RolePermissionExecution{}, nil
}

func (backend *scriptedRolePermissionBackend) NewDemoteExecution(actionsecurity.DemoteIntent) (*service.RolePermissionExecution, error) {
	backend.newCalls++
	return &service.RolePermissionExecution{}, nil
}

func (backend *scriptedRolePermissionBackend) NewPermissionsWriteExecution(actionsecurity.PermissionsWriteIntent) (*service.RolePermissionExecution, error) {
	backend.newCalls++
	return &service.RolePermissionExecution{}, nil
}

func (backend *scriptedRolePermissionBackend) ExecuteRolePermission(context.Context, *service.OperationIdentity, *service.RolePermissionExecution) (*service.OperationView, error) {
	backend.executeCalls++
	return backend.executeView, backend.executeErr
}

func TestA08PromoteIssueAndExecuteBindIdenticalCanonicalIntent(t *testing.T) {
	finished, target, authVersion, permissionsVersion, role := int64(1_790_000_000_000), int64(123), 8, int64(4), models.UserRoleAdmin
	base := &scriptedUserManagementBackend{
		issued:      &service.IssuedVerification{Ticket: testActionTicket, ExpiresAt: 1_790_000_300_000},
		identity:    &service.OperationIdentity{PublicRef: testOperationRef},
		ready:       true,
		beginView:   &service.OperationView{PublicRef: testOperationRef, Scope: "users.promote", Status: "processing", RetryAfterSeconds: 30},
		executeView: &service.OperationView{PublicRef: testOperationRef, Scope: "users.promote", Status: "succeeded", FinishedAt: &finished, TargetGUID: &target, ResultAuthVersion: &authVersion, ResultPermissionsVersion: &permissionsVersion, ResultRole: &role},
	}
	backend := &scriptedRolePermissionBackend{scriptedUserManagementBackend: base}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
	issueBody := `{"action":"users.promote","intent":{"target_guid":"123","expected_auth_version":7,"expected_permissions_version":3,"catalog_version":1,"overrides":[{"capability":"users.delete","effect":"deny"}],"reason":"grant duties"},"current_password":"Actor!Pass1"}`
	issued := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", issueBody, http.Header{"X-Request-ID": {"a08-issue"}})
	if issued.Code != http.StatusCreated || issued.Header().Get("Cache-Control") != "no-store" || issued.Header().Get("X-Request-ID") != "a08-issue" {
		t.Fatalf("issue status/headers mismatch status=%d cache=%q request_id=%q body=%s", issued.Code, issued.Header().Get("Cache-Control"), issued.Header().Get("X-Request-ID"), issued.Body.String())
	}
	executeBody := `{"action":"promote","expected_auth_version":7,"expected_permissions_version":3,"catalog_version":1,"overrides":[{"capability":"users.delete","effect":"deny"}],"reason":"grant duties"}`
	executed := performActionRequest(engine, http.MethodPost, "/admin/v2/users/123/actions", executeBody, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}, "X-Request-ID": {"a08-execute"}})
	if executed.Code != http.StatusOK || backend.newCalls != 1 || backend.executeCalls != 1 {
		t.Fatalf("execute status/calls mismatch status=%d new=%d execute=%d body=%s", executed.Code, backend.newCalls, backend.executeCalls, executed.Body.String())
	}
	assertActionTestExactBody(t, executed, `{"operation_ref":"`+testOperationRef+`","target_guid":"123","resulting_auth_version":8,"resulting_permissions_version":4,"resulting_role":"admin"}`)
	issueIntent, issueOK := backend.issueIntent.(actionsecurity.PromoteIntent)
	beginIntent, beginOK := backend.beginIntent.(actionsecurity.PromoteIntent)
	if !issueOK || !beginOK || !reflect.DeepEqual(issueIntent, beginIntent) {
		t.Fatalf("issue and execute intents differ issue=%#v execute=%#v", backend.issueIntent, backend.beginIntent)
	}
	var descriptor actionsecurity.Descriptor
	for _, candidate := range actionsecurity.ActiveActionRegistry() {
		if candidate.Action == actionsecurity.ActionUsersPromote {
			descriptor = candidate
		}
	}
	issueCanonical, issueErr := descriptor.Encode(issueIntent)
	executeCanonical, executeErr := descriptor.Encode(beginIntent)
	if issueErr != nil || executeErr != nil || !bytes.Equal(issueCanonical, executeCanonical) {
		t.Fatalf("canonical intents differ issue_err=%v execute_err=%v", issueErr, executeErr)
	}
}

func TestA08PermissionsPatchTerminalReplayUsesStoredResult(t *testing.T) {
	finished, target, authVersion, permissionsVersion, role := int64(1_790_000_000_000), int64(123), 9, int64(5), models.UserRoleAdmin
	base := &scriptedUserManagementBackend{
		identity:  &service.OperationIdentity{PublicRef: testOperationRef},
		ready:     false,
		beginView: &service.OperationView{PublicRef: testOperationRef, Scope: "users.permissions.write", Status: "succeeded", FinishedAt: &finished, TargetGUID: &target, ResultAuthVersion: &authVersion, ResultPermissionsVersion: &permissionsVersion, ResultRole: &role},
	}
	backend := &scriptedRolePermissionBackend{scriptedUserManagementBackend: base}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
	body := `{"expected_auth_version":8,"expected_permissions_version":4,"catalog_version":1,"overrides":[],"reason":"support rotation"}`
	recorder := performActionRequest(engine, http.MethodPatch, "/admin/v2/users/123/permissions", body, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}})
	if recorder.Code != http.StatusOK || backend.newCalls != 0 || backend.executeCalls != 0 || backend.beginAction != actionsecurity.ActionUsersPermissionsWrite {
		t.Fatalf("replay status/calls mismatch status=%d new=%d execute=%d action=%d body=%s", recorder.Code, backend.newCalls, backend.executeCalls, backend.beginAction, recorder.Body.String())
	}
	assertActionTestExactBody(t, recorder, `{"operation_ref":"`+testOperationRef+`","target_guid":"123","resulting_auth_version":9,"resulting_permissions_version":5,"resulting_role":"admin"}`)
}

func TestA08LiteralQueriesReturnOnlyStoredRolePermissionResult(t *testing.T) {
	finished, target, authVersion, permissionsVersion := int64(1_790_000_000_000), int64(123), 8, int64(4)
	for _, test := range []struct {
		scope  string
		action actionsecurity.Action
		role   models.UserRole
	}{
		{"users.promote", actionsecurity.ActionUsersPromote, models.UserRoleAdmin},
		{"users.demote", actionsecurity.ActionUsersDemote, models.UserRoleUser},
		{"users.permissions.write", actionsecurity.ActionUsersPermissionsWrite, models.UserRoleAdmin},
	} {
		t.Run(test.scope, func(t *testing.T) {
			role := test.role
			base := &scriptedUserManagementBackend{queryView: &service.OperationView{PublicRef: testOperationRef, Scope: test.scope, Status: "succeeded", FinishedAt: &finished, TargetGUID: &target, ResultAuthVersion: &authVersion, ResultPermissionsVersion: &permissionsVersion, ResultRole: &role}}
			backend := &scriptedRolePermissionBackend{scriptedUserManagementBackend: base}
			recorder := performActionRequest(newScriptedUserManagementEngine(t, backend, models.UserRoleRoot), http.MethodGet, "/admin/v2/operations?scope="+test.scope, "", http.Header{"Idempotency-Key": {testActionKey}})
			if recorder.Code != http.StatusOK || base.queryCalls != 1 || base.queryAction != test.action {
				t.Fatalf("query mismatch status=%d calls=%d action=%d body=%s", recorder.Code, base.queryCalls, base.queryAction, recorder.Body.String())
			}
			assertActionTestExactBody(t, recorder, `{"operation_ref":"`+testOperationRef+`","scope":"`+test.scope+`","status":"succeeded","finished_at":1790000000000,"failure_code":null,"target_guid":"123","resulting_auth_version":8,"resulting_permissions_version":4,"resulting_role":"`+test.role.String()+`"}`)
		})
	}
}

func TestA08PatchAndQueryRejectNonCanonicalRequestsBeforeBackend(t *testing.T) {
	for _, test := range []struct {
		method  string
		path    string
		body    string
		headers http.Header
	}{
		{http.MethodPatch, "/admin/v2/users/123/permissions?scope=users.permissions.write", `{}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}},
		{http.MethodPatch, "/admin/v2/users/123/permissions", `{}`, http.Header{"Idempotency-Key": {testActionKey}}},
		{http.MethodPatch, "/admin/v2/users/123/permissions", `{}`, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket, testActionTicket}}},
		{http.MethodGet, "/admin/v2/operations?scope=users.promote&extra=1", "", http.Header{"Idempotency-Key": {testActionKey}}},
		{http.MethodGet, "/admin/v2/operations?scope=users%2Epromote", "", http.Header{"Idempotency-Key": {testActionKey}}},
	} {
		base := &scriptedUserManagementBackend{}
		backend := &scriptedRolePermissionBackend{scriptedUserManagementBackend: base}
		recorder := performActionRequest(newScriptedUserManagementEngine(t, backend, models.UserRoleRoot), test.method, test.path, test.body, test.headers)
		if recorder.Code != http.StatusBadRequest || backend.beginCalls != 0 || base.queryCalls != 0 {
			t.Fatalf("noncanonical request reached backend status=%d begin=%d query=%d body=%s", recorder.Code, backend.beginCalls, base.queryCalls, recorder.Body.String())
		}
	}
}
