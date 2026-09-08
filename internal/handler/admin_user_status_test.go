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

type recordingAdminUserStatusBackend struct {
	calls  int
	actor  service.AdminPermissionReadActor
	guid   int64
	input  service.AdminUserStatusInput
	result *service.UserReadDTO
	err    error
}

func (b *recordingAdminUserStatusBackend) Change(_ context.Context, actor service.AdminPermissionReadActor, guid int64, input service.AdminUserStatusInput) (*service.UserReadDTO, error) {
	b.calls++
	b.actor, b.guid, b.input = actor, guid, input
	return b.result, b.err
}

func adminUserStatusTestEngine(backend adminUserStatusBackend) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, func(c *gin.Context) {
		c.Set(middleware.ContextUser, &models.User{ID: 17, AuthVersion: 9})
		c.Set(middleware.ContextSessionSID, "11111111-2222-4333-8444-555555555555")
		c.Set("authenticated_session_version", 4)
		c.Next()
	})
	registerAdminUserStatusRoute(group, backend)
	return r
}

func statusRequest(engine http.Handler, path, body string) *httptest.ResponseRecorder {
	return statusRequestWithContentType(engine, path, body, "application/json")
}

func statusRequestWithContentType(engine http.Handler, path, body, contentType string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Request-ID", "a06-request")
	engine.ServeHTTP(recorder, request)
	return recorder
}

func assertAdminUserStatusErrorEnvelope(t *testing.T, recorder *httptest.ResponseRecorder, code string) {
	t.Helper()
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &outer); err != nil || len(outer) != 1 {
		t.Fatalf("outer error envelope=%s err=%v", recorder.Body.String(), err)
	}
	var body map[string]any
	if err := json.Unmarshal(outer["error"], &body); err != nil || len(body) != 4 {
		t.Fatalf("inner error envelope=%s err=%v", recorder.Body.String(), err)
	}
	if body["code"] != code || body["message"] != "请求无法完成" || body["kind"] != "admin_user_status_error" || body["request_id"] != "a06-request" {
		t.Fatalf("error envelope=%#v", body)
	}
}

func TestAdminUserStatusSuccessInvokesDedicatedBackendOnce(t *testing.T) {
	username, group := "managed", "default"
	backend := &recordingAdminUserStatusBackend{result: &service.UserReadDTO{GUID: "123", Username: &username, Group: &group, PlanType: "free", Role: "user", Status: "disabled", AuthVersion: 8, CreatedAt: "2026-09-08T00:00:00Z"}}
	recorder := statusRequest(adminUserStatusTestEngine(backend), "/admin/v2/users/123/status", `{"status":"disabled","reason":" review ","expected_auth_version":7}`)
	if recorder.Code != 200 || backend.calls != 1 || backend.guid != 123 || backend.actor.UserID != 17 || backend.input.Status != models.UserStatusDisabled || backend.input.Reason == nil || *backend.input.Reason != "review" || backend.input.ExpectedAuthVersion != 7 {
		t.Fatalf("status=%d backend=%#v body=%s", recorder.Code, backend, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Request-ID") != "a06-request" {
		t.Fatalf("headers=%v", recorder.Header())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || len(body) != 11 {
		t.Fatalf("body=%s err=%v", recorder.Body.String(), err)
	}
	for _, key := range []string{"guid", "username", "nickname", "email", "group", "plan_type", "role", "status", "auth_version", "created_at", "last_login_at"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing exact DTO key %q in %#v", key, body)
		}
	}
}

func TestAdminUserStatusRejectsInvalidRequestBeforeBackend(t *testing.T) {
	for _, tc := range []struct {
		path, body, contentType string
		status                  int
	}{
		{"/admin/v2/users/01/status", `{"status":"disabled","reason":"x","expected_auth_version":1}`, "application/json", 400},
		{"/admin/v2/users/1/status?x=1", `{"status":"disabled","reason":"x","expected_auth_version":1}`, "application/json", 400},
		{"/admin/v2/users/1/status", `{"status":"disabled","reason":"x","expected_auth_version":1,"role":"admin"}`, "application/json", 400},
		{"/admin/v2/users/1/status", strings.Repeat(" ", 4097), "application/json", 413},
		{"/admin/v2/users/1/status", `{"status":"disabled","reason":"x","expected_auth_version":1}`, "application/json, text/plain", 400},
		{"/admin/v2/users/1/status", `{"status":"disabled","reason":"x","expected_auth_version":1}`, "text/plain", 400},
	} {
		backend := &recordingAdminUserStatusBackend{}
		recorder := statusRequestWithContentType(adminUserStatusTestEngine(backend), tc.path, tc.body, tc.contentType)
		if recorder.Code != tc.status || backend.calls != 0 {
			t.Fatalf("path=%s status=%d calls=%d body=%s", tc.path, recorder.Code, backend.calls, recorder.Body.String())
		}
		code := "invalid_admin_user_status_request"
		if tc.status == http.StatusRequestEntityTooLarge {
			code = "request_body_too_large"
		}
		assertAdminUserStatusErrorEnvelope(t, recorder, code)
	}
}

func TestAdminUserStatusMapsSafeErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{&service.HTTPError{Status: 401, Message: "private"}, 401, "authentication_invalid"}, {&service.HTTPError{Status: 403, Message: "private"}, 403, "user_status_forbidden"}, {&service.HTTPError{Status: 404, Message: "private"}, 404, "user_not_found"},
		{&service.HTTPError{Status: 409, Message: "admin user status version conflict"}, 409, "auth_version_conflict"}, {&service.HTTPError{Status: 409, Message: "admin user status state conflict"}, 409, "user_status_conflict"}, {errors.New("dial secret"), 503, "user_status_dependency_unavailable"},
	}
	for _, tc := range tests {
		backend := &recordingAdminUserStatusBackend{err: tc.err}
		recorder := statusRequest(adminUserStatusTestEngine(backend), "/admin/v2/users/1/status", `{"status":"active","reason":null,"expected_auth_version":1}`)
		if recorder.Code != tc.status || backend.calls != 1 || strings.Contains(recorder.Body.String(), "private") || strings.Contains(recorder.Body.String(), "secret") {
			t.Fatalf("status=%d calls=%d body=%s", recorder.Code, backend.calls, recorder.Body.String())
		}
		assertAdminUserStatusErrorEnvelope(t, recorder, tc.code)
	}
}
