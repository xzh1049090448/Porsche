package handler

import (
	"bytes"
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

type recordingAdminUserEditBackend struct {
	calls  int
	actor  service.AdminPermissionReadActor
	guid   int64
	input  service.AdminUserNicknameEditInput
	result *service.UserReadDTO
	err    error
}

func (b *recordingAdminUserEditBackend) Edit(_ context.Context, actor service.AdminPermissionReadActor, guid int64, input service.AdminUserNicknameEditInput) (*service.UserReadDTO, error) {
	b.calls++
	b.actor, b.guid, b.input = actor, guid, input
	return b.result, b.err
}

func adminUserEditTestEngine(backend adminUserEditBackend) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, func(c *gin.Context) {
		c.Set(middleware.ContextUser, &models.User{ID: 17, AuthVersion: 9})
		c.Set(middleware.ContextSessionSID, "11111111-2222-4333-8444-555555555555")
		c.Set("authenticated_session_version", 4)
		c.Next()
	})
	registerAdminUserEditRoute(group, backend)
	return r
}

func performAdminUserEditRequest(engine http.Handler, path, body string) *httptest.ResponseRecorder {
	return performAdminUserEditRequestWithContentType(engine, path, body, "application/json")
}

func performAdminUserEditRequestWithContentType(engine http.Handler, path, body, contentType string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("X-Request-ID", "a05-request")
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestAdminUserEditContentTypeRejectsMalformedParametersBeforeService(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/json; definitely-not-a-parameter"} {
		backend := &recordingAdminUserEditBackend{}
		recorder := performAdminUserEditRequestWithContentType(adminUserEditTestEngine(backend), "/admin/v2/users/123", `{"nickname":null,"expected_auth_version":1}`, contentType)
		assertAdminUserEditError(t, recorder, http.StatusBadRequest)
		if backend.calls != 0 {
			t.Fatalf("backend called for invalid Content-Type %q", contentType)
		}
	}
	backend := &recordingAdminUserEditBackend{result: &service.UserReadDTO{}}
	recorder := performAdminUserEditRequestWithContentType(adminUserEditTestEngine(backend), "/admin/v2/users/123", `{"nickname":null,"expected_auth_version":1}`, "application/json; charset=utf-8")
	if recorder.Code != http.StatusOK || backend.calls != 1 {
		t.Fatalf("legal JSON Content-Type status=%d calls=%d body=%s", recorder.Code, backend.calls, recorder.Body.String())
	}
}

func TestAdminUserEditSuccessUsesDedicatedBackendOnceAndExactDTO(t *testing.T) {
	nickname, username, group := "新昵称", "managed", "default"
	backend := &recordingAdminUserEditBackend{result: &service.UserReadDTO{
		GUID: "123", Username: &username, Nickname: &nickname, Group: &group,
		PlanType: "free", Role: "user", Status: "active", AuthVersion: 7,
		CreatedAt: "2026-09-08T00:00:00Z",
	}}
	recorder := performAdminUserEditRequest(adminUserEditTestEngine(backend), "/admin/v2/users/123", `{"nickname":" 新昵称 ","expected_auth_version":7}`)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Request-ID") != "a05-request" {
		t.Fatalf("status/headers=%d/%q/%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Header().Get("X-Request-ID"), recorder.Body.String())
	}
	if backend.calls != 1 || backend.guid != 123 || backend.actor.UserID != 17 || backend.actor.AuthVersion != 9 || backend.actor.SessionSID == "" || backend.actor.SessionVersion != 4 || backend.input.ExpectedAuthVersion != 7 || backend.input.Nickname == nil || *backend.input.Nickname != "新昵称" || backend.input.ClearNickname {
		t.Fatalf("unexpected dedicated edit call: %#v calls=%d", backend, backend.calls)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"guid", "username", "nickname", "email", "group", "plan_type", "role", "status", "auth_version", "created_at", "last_login_at"}
	if len(body) != len(wantKeys) {
		t.Fatalf("response keys=%v", body)
	}
	for _, key := range wantKeys {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing response key %q", key)
		}
	}
}

func TestAdminUserEditStrictDecodeAndPathFailuresNeverInvokeBackend(t *testing.T) {
	badBodies := []string{
		`{"nickname":"x","expected_auth_version":1,"role":"admin"}`,
		`{"nickname":"x","nickname":"y","expected_auth_version":1}`,
		`{"nickname":"x","expected_auth_version":1}{}`,
		strings.Repeat(" ", 4097),
	}
	for _, body := range badBodies {
		backend := &recordingAdminUserEditBackend{}
		recorder := performAdminUserEditRequest(adminUserEditTestEngine(backend), "/admin/v2/users/123", body)
		want := http.StatusBadRequest
		if len(body) > 4096 {
			want = http.StatusRequestEntityTooLarge
		}
		assertAdminUserEditError(t, recorder, want)
		if backend.calls != 0 {
			t.Fatalf("backend called for rejected body length=%d", len(body))
		}
	}
	for _, path := range []string{"/admin/v2/users/0", "/admin/v2/users/01", "/admin/v2/users/+1", "/admin/v2/users/9223372036854775808", "/admin/v2/users/1?x=1"} {
		backend := &recordingAdminUserEditBackend{}
		recorder := performAdminUserEditRequest(adminUserEditTestEngine(backend), path, `{"nickname":null,"expected_auth_version":1}`)
		assertAdminUserEditError(t, recorder, http.StatusBadRequest)
		if backend.calls != 0 {
			t.Fatalf("backend called for invalid path %q", path)
		}
	}
}

func TestAdminUserEditMapsOnlyStableSafeErrors(t *testing.T) {
	tests := []struct {
		status int
		err    error
		code   string
	}{
		{400, &service.HTTPError{Status: 400, Message: "sensitive invalid detail"}, "invalid_admin_user_edit_request"},
		{401, &service.HTTPError{Status: 401, Message: "sensitive auth detail"}, "authentication_invalid"},
		{403, &service.HTTPError{Status: 403, Message: "sensitive policy detail"}, "user_edit_forbidden"},
		{404, &service.HTTPError{Status: 404, Message: "sensitive lookup detail"}, "user_not_found"},
		{409, &service.HTTPError{Status: 409, Message: "auth_version_conflict"}, "auth_version_conflict"},
		{503, errors.New("dial tcp secret-internal:3306"), "user_edit_dependency_unavailable"},
	}
	for _, test := range tests {
		backend := &recordingAdminUserEditBackend{err: test.err}
		recorder := performAdminUserEditRequest(adminUserEditTestEngine(backend), "/admin/v2/users/123", `{"nickname":null,"expected_auth_version":1}`)
		assertAdminUserEditError(t, recorder, test.status)
		if backend.calls != 1 || !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"`+test.code+`"`)) || bytes.Contains(recorder.Body.Bytes(), []byte("sensitive")) || bytes.Contains(recorder.Body.Bytes(), []byte("secret-internal")) {
			t.Fatalf("status=%d calls=%d body=%s", recorder.Code, backend.calls, recorder.Body.String())
		}
	}
}

func assertAdminUserEditError(t *testing.T, recorder *httptest.ResponseRecorder, status int) {
	t.Helper()
	if recorder.Code != status || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Request-ID") != "a05-request" {
		t.Fatalf("status/headers=%d/%q/%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Header().Get("X-Request-ID"), recorder.Body.String())
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Kind      string `json:"kind"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code == "" || envelope.Error.Message != "请求无法完成" || envelope.Error.Kind != "admin_user_edit_error" || envelope.Error.RequestID != "a05-request" {
		t.Fatalf("unsafe or unstable error envelope: %#v", envelope)
	}
}
