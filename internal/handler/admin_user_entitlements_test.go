package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

type recordingAdminUserEntitlementBackend struct {
	groupCalls int
	planCalls  int
	group      service.AdminUserGroupChangeInput
	plan       service.AdminUserPlanChangeInput
	result     *service.UserReadDTO
	err        error
}

func (b *recordingAdminUserEntitlementBackend) ChangeGroup(_ context.Context, _ service.AdminPermissionReadActor, _ int64, input service.AdminUserGroupChangeInput) (*service.UserReadDTO, error) {
	b.groupCalls++
	b.group = input
	return b.result, b.err
}

func (b *recordingAdminUserEntitlementBackend) ChangePlan(_ context.Context, _ service.AdminPermissionReadActor, _ int64, input service.AdminUserPlanChangeInput) (*service.UserReadDTO, error) {
	b.planCalls++
	b.plan = input
	return b.result, b.err
}

func adminUserEntitlementTestEngine(backend adminUserEntitlementBackend) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, func(c *gin.Context) {
		c.Set(middleware.ContextUser, &models.User{ID: 17, AuthVersion: 9})
		c.Set(middleware.ContextSessionSID, "11111111-2222-4333-8444-555555555555")
		c.Set("authenticated_session_version", 4)
		c.Next()
	})
	registerAdminUserEntitlementRoutes(group, backend)
	return r
}

func entitlementRequest(engine http.Handler, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "a07-request")
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestAdminUserEntitlementRoutesDispatchExactInputs(t *testing.T) {
	groupKey, username := "research", "managed"
	backend := &recordingAdminUserEntitlementBackend{result: &service.UserReadDTO{GUID: "123", Username: &username, Group: &groupKey, PlanType: "professional", Role: "user", Status: "active", AuthVersion: 8, CreatedAt: "2026-09-08T00:00:00Z"}}
	group := entitlementRequest(adminUserEntitlementTestEngine(backend), "/admin/v2/users/123/group", `{"group_guid":"456","reason":" move ","expected_auth_version":7}`)
	if group.Code != 200 || backend.groupCalls != 1 || backend.group.GroupGUID != 456 || backend.group.Reason != "move" || backend.group.RequestID != "a07-request" {
		t.Fatalf("group status=%d backend=%#v body=%s", group.Code, backend, group.Body.String())
	}
	plan := entitlementRequest(adminUserEntitlementTestEngine(backend), "/admin/v2/users/123/plan", `{"plan_type":"professional","reason":" grant ","expected_auth_version":7}`)
	if plan.Code != 200 || backend.planCalls != 1 || backend.plan.PlanType != models.PlanProfessional || backend.plan.Reason != "grant" || backend.plan.RequestID != "a07-request" {
		t.Fatalf("plan status=%d backend=%#v body=%s", plan.Code, backend, plan.Body.String())
	}
}

func TestAdminUserEntitlementRoutesRejectAmbiguousRequestsBeforeService(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/admin/v2/users/01/group", `{"group_guid":"456","reason":"x","expected_auth_version":7}`, 400},
		{"/admin/v2/users/123/group?x=1", `{"group_guid":"456","reason":"x","expected_auth_version":7}`, 400},
		{"/admin/v2/users/123/group", `{"group_guid":"456","reason":"x","expected_auth_version":7,"role":"admin"}`, 400},
		{"/admin/v2/users/123/plan", strings.Repeat(" ", 4097), 413},
	} {
		backend := &recordingAdminUserEntitlementBackend{}
		recorder := entitlementRequest(adminUserEntitlementTestEngine(backend), tc.path, tc.body)
		if recorder.Code != tc.status || backend.groupCalls+backend.planCalls != 0 {
			t.Fatalf("path=%s status=%d calls=%d body=%s", tc.path, recorder.Code, backend.groupCalls+backend.planCalls, recorder.Body.String())
		}
		var envelope struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Error["kind"] != "admin_user_entitlement_error" || envelope.Error["request_id"] != "a07-request" {
			t.Fatalf("envelope=%#v err=%v", envelope, err)
		}
	}
}

func TestAdminUserEntitlementRoutesMapOnlyFrozenErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&service.HTTPError{Status: 403, Message: "private"}, 403, "user_entitlement_forbidden"},
		{&service.HTTPError{Status: 404, Message: "用户不存在"}, 404, "user_not_found"},
		{&service.HTTPError{Status: 404, Message: "用户组不存在"}, 404, "group_not_found"},
		{&service.HTTPError{Status: 409, Message: "admin user entitlement version conflict"}, 409, "auth_version_conflict"},
		{&service.HTTPError{Status: 409, Message: "admin user plan state conflict"}, 409, "user_plan_conflict"},
		{&service.HTTPError{Status: 409, Message: "admin user group state conflict"}, 409, "user_group_conflict"},
		{errors.New("dial secret"), 503, "user_entitlement_dependency_unavailable"},
	} {
		backend := &recordingAdminUserEntitlementBackend{err: tc.err}
		recorder := entitlementRequest(adminUserEntitlementTestEngine(backend), "/admin/v2/users/123/plan", `{"plan_type":"professional","reason":"grant","expected_auth_version":7}`)
		if recorder.Code != tc.status || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Request-ID") != "a07-request" || strings.Contains(recorder.Body.String(), "private") || strings.Contains(recorder.Body.String(), "secret") {
			t.Fatalf("status/headers/body=%d/%v/%s", recorder.Code, recorder.Header(), recorder.Body.String())
		}
		var envelope struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || len(envelope.Error) != 4 || envelope.Error["code"] != tc.code || envelope.Error["message"] != "请求无法完成" || envelope.Error["kind"] != "admin_user_entitlement_error" || envelope.Error["request_id"] != "a07-request" {
			t.Fatalf("envelope=%#v err=%v", envelope, err)
		}
		if _, exists := envelope.Error["operation_ref"]; exists {
			t.Fatalf("operation_ref leaked: %#v", envelope)
		}
	}
}

func TestAdminUserEntitlementFreshAuthenticationFailureUsesExistingEnvelope(t *testing.T) {
	backend := &recordingAdminUserEntitlementBackend{err: &service.HTTPError{Status: 401, Message: "private"}}
	recorder := entitlementRequest(adminUserEntitlementTestEngine(backend), "/admin/v2/users/123/plan", `{"plan_type":"professional","reason":"grant","expected_auth_version":7}`)
	if recorder.Code != 401 || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Body.String() != `{"detail":"认证会话不可用"}` {
		t.Fatalf("status/headers/body=%d/%v/%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}
