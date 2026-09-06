package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestAdminUsersReadHTTPAuthenticationHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterAdminUsersRead(r, &app.State{})
	RegisterAdminUsers(r, &app.State{})
	for _, path := range []string{"/admin/v2/users", "/admin/v2/users/123", "/admin/users", "/admin/users/123", "/admin/users/123/behavior"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			adminAuthzAssertError(t, rec, 401, "")
		})
	}
}

func TestAuthProjectionIssuedResponseFailureNoCredentials(t *testing.T) {
	for _, err := range []error{service.ErrAdminPermissionUnauthenticated, service.ErrAdminPermissionUnavailable} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		respondIssuedAuth(c, &app.State{}, &service.IssuedSession{RefreshToken: "must-not-leak"}, "must-not-leak", nil, err)
		if rec.Code != 401 && rec.Code != 503 {
			t.Fatal(rec.Code)
		}
		if len(rec.Result().Cookies()) != 0 || strings.Contains(rec.Body.String(), "must-not-leak") {
			t.Fatal("error exposed credential or changed cookie")
		}
	}
}

func TestAdminUsersReadHTTPAuthVersionQueryAndVisibility(t *testing.T) {
	state := adminAuthzHTTPState(t)
	r := gin.New()
	RegisterAdminUsersRead(r, state)
	RegisterAdminUsers(r, state)
	root := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
	target := adminAuthzHTTPUser(t, state, models.UserRoleUser)
	access := platformJWT(t, state, root)
	guid := strconv.FormatInt(target.Guid, 10)
	for _, path := range []string{"/admin/v2/users?page=0", "/admin/v2/users?page=1&page=2", "/admin/v2/users?group=x", "/admin/v2/users/0", "/admin/v2/users/" + guid + "?x=1", "/admin/users?limit=101", "/admin/users?skip=9223372036854775808"} {
		adminAuthzAssertError(t, adminAuthzRequest(r, path, access), 400, "")
	}
	adminAuthzAssertError(t, adminAuthzRequest(r, "/admin/users?status=deleted", access), 422, "")
	for _, path := range []string{"/admin/users/9223372036854775808", "/admin/v2/users/" + strconv.FormatInt(root.Guid, 10)} {
		adminAuthzAssertError(t, adminAuthzRequest(r, path, access), 404, "用户不存在")
	}
	rec := adminAuthzRequest(r, "/admin/v2/users/"+guid, access)
	body := adminAuthzObject(t, rec, "guid", "username", "nickname", "email", "group", "plan_type", "role", "status", "auth_version", "created_at", "last_login_at")
	if body["guid"] != guid || body["auth_version"] != float64(target.AuthVersion) || body["email"] != nil || body["group"] != "default" {
		t.Fatal("DTO")
	}
	rec = adminAuthzRequest(r, "/admin/v2/users?q="+guid, access)
	body = adminAuthzObject(t, rec, "items", "total", "page", "page_size")
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("v2 list items=%v", items)
	}
	item := items[0].(map[string]any)
	adminAuthzKeys(t, item, "guid", "username", "nickname", "email", "group", "plan_type", "role", "status", "auth_version", "created_at", "last_login_at")
	if item["guid"] != guid || item["auth_version"] != float64(target.AuthVersion) || item["group"] != "default" {
		t.Fatalf("v2 list auth version differs: %v", item)
	}
	rec = adminAuthzRequest(r, "/admin/v2/users?q="+guid+"&page=2", access)
	body = adminAuthzObject(t, rec, "items", "total", "page", "page_size")
	if body["total"] != float64(1) || len(body["items"].([]any)) != 0 {
		t.Fatal("empty page")
	}
	rec = adminAuthzRequest(r, "/admin/users?limit=0", access)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatal("legacy zero")
	}
	rec = adminAuthzRequest(r, "/admin/users/"+guid, access)
	body = adminAuthzObject(t, rec, "guid", "nickname", "plan_type", "status", "is_verified", "total_tokens_used", "created_at")
	if body["guid"] != guid {
		t.Fatalf("legacy detail differs: %v", body)
	}
	rec = adminAuthzRequest(r, "/admin/users?limit=100", access)
	adminAuthzAssertHeaders(t, rec)
	if rec.Code != 200 {
		t.Fatalf("legacy list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var legacyItems []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &legacyItems); err != nil {
		t.Fatal("invalid legacy list JSON")
	}
	var legacyTarget map[string]any
	for _, candidate := range legacyItems {
		if candidate["guid"] == guid {
			legacyTarget = candidate
			break
		}
	}
	if legacyTarget == nil {
		t.Fatal("target missing from legacy list")
	}
	adminAuthzKeys(t, legacyTarget, "guid", "nickname", "plan_type", "status", "is_verified", "total_tokens_used", "created_at")
	admin := adminAuthzHTTPUser(t, state, models.UserRoleAdmin)
	ordinary := adminAuthzHTTPUser(t, state, models.UserRoleUser)
	for _, actor := range []*models.User{admin, ordinary} {
		token := platformJWT(t, state, actor)
		if actor.Role == models.UserRoleUser {
			for _, path := range []string{"/admin/v2/users", "/admin/v2/users/" + guid, "/admin/users", "/admin/users/" + guid, "/admin/users/" + guid + "/behavior"} {
				adminAuthzAssertError(t, adminAuthzRequest(r, path, token), 403, "无权限访问")
			}
		} else {
			body := adminAuthzObject(t, adminAuthzRequest(r, "/admin/users/"+guid+"/behavior", token), "model_preferences")
			if _, ok := body["model_preferences"].([]any); !ok {
				t.Fatal("behavior adapter")
			}
		}
	}
}

func TestAdminUsersReadHTTPExplicitDenyAllAdapters(t *testing.T) {
	state := adminAuthzHTTPState(t)
	r := gin.New()
	RegisterAdminUsersRead(r, state)
	RegisterAdminUsers(r, state)
	actor := adminAuthzHTTPUser(t, state, models.UserRoleAdmin)
	target := adminAuthzHTTPUser(t, state, models.UserRoleUser)
	access := platformJWT(t, state, actor)
	head := models.PermissionPolicyHead{AuditFields: models.AuditFields{Guid: platformTestSnowflake.Next()}, UserID: actor.ID, PolicyVersion: 1, CatalogVersion: 1, RuleCount: 1}
	row := models.PermissionOverride{AuditFields: models.AuditFields{Guid: platformTestSnowflake.Next()}, UserID: actor.ID, PolicyVersion: 1, Capability: 1, Effect: 3}
	if err := state.DB.Create(&head).Error; err != nil {
		t.Fatal("head")
	}
	if err := state.DB.Create(&row).Error; err != nil {
		t.Fatal("deny")
	}
	guid := strconv.FormatInt(target.Guid, 10)
	for _, path := range []string{"/admin/v2/users", "/admin/v2/users/" + guid, "/admin/users", "/admin/users/" + guid, "/admin/users/" + guid + "/behavior"} {
		t.Run(path, func(t *testing.T) {
			adminAuthzAssertError(t, adminAuthzRequest(r, path, access), 403, "无权限访问")
		})
	}
}

func TestAdminUsersReadHTTPGroupProjectionFailsClosed(t *testing.T) {
	state := adminAuthzHTTPState(t)
	router := gin.New()
	RegisterAdminUsersRead(router, state)
	root := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
	target := adminAuthzHTTPUser(t, state, models.UserRoleUser)
	access := platformJWT(t, state, root)
	guid := strconv.FormatInt(target.Guid, 10)
	defaultGroupID := platformTestDefaultBusinessGroupID(t, state)

	for _, test := range []struct {
		name  string
		group models.BusinessGroup
	}{
		{name: "deleted", group: models.BusinessGroup{AuditFields: adminAuthzAudit(), Key: "deleted-http-group", DisplayName: "Deleted", Status: models.BusinessGroupStatusActive}},
		{name: "inactive", group: models.BusinessGroup{AuditFields: adminAuthzAudit(), Key: "inactive-http-group", DisplayName: "Inactive", Status: models.BusinessGroupStatusInactive}},
		{name: "corrupt", group: models.BusinessGroup{AuditFields: adminAuthzAudit(), Key: "INVALID", DisplayName: "Corrupt", Status: models.BusinessGroupStatusActive}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "deleted" {
				test.group.IsDeleted = 1
			}
			if err := state.DB.Create(&test.group).Error; err != nil {
				t.Fatal("create group fixture")
			}
			if err := state.DB.Model(target).Update("group_id", test.group.ID).Error; err != nil {
				t.Fatal("assign group fixture")
			}
			defer func() {
				if err := state.DB.Model(target).Update("group_id", defaultGroupID).Error; err != nil {
					t.Errorf("restore default group: %v", err)
				}
				if err := state.DB.Delete(&test.group).Error; err != nil {
					t.Errorf("delete group fixture: %v", err)
				}
			}()
			for _, path := range []string{"/admin/v2/users/" + guid, "/admin/v2/users?q=" + guid} {
				adminAuthzAssertError(t, adminAuthzRequest(router, path, access), http.StatusServiceUnavailable, "用户信息暂不可用")
			}
		})
	}
}

func TestAuthProjectionHTTPFourSources(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(strconv.FormatBool(corrupt), func(t *testing.T) {
			state := adminAuthzHTTPState(t)
			state.Settings.RegisterEnabled = true
			state.Settings.PasswordRegisterEnabled = true
			state.Settings.PasswordLoginEnabled = true
			state.Settings.AuthTrustedOrigins = []string{"https://app.example.test"}
			user, err := state.Auth.RegisterUsername(context.Background(), fmt.Sprintf("d%019d", platformTestSnowflake.Next()), "Str0ng!Pass1", nil)
			if err != nil {
				t.Fatal("register fixture")
			}
			if corrupt {
				head := models.PermissionPolicyHead{AuditFields: models.AuditFields{Guid: platformTestSnowflake.Next()}, UserID: user.ID, PolicyVersion: 1, CatalogVersion: 99}
				if err := state.DB.Create(&head).Error; err != nil {
					t.Fatal("bad policy fixture")
				}
			}
			r := gin.New()
			RegisterAuth(r, state)
			RegisterUsers(r, state)
			login := serveAuthRequest(r, authJSONRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"`+*user.Username+`","password":"Str0ng!Pass1"}`))
			assertAuthProjectionHTTPFields(t, login, true, corrupt)
			if len(login.Result().Cookies()) != 1 {
				t.Fatal("login cookie missing")
			}
			cookie := login.Result().Cookies()[0]
			access := authAccessToken(t, login.Body.Bytes())
			for _, path := range []string{"/api/v1/auth/self", "/api/v1/users/me"} {
				req := authJSONRequest(http.MethodGet, path, "")
				req.Header.Set("Authorization", "Bearer "+access)
				assertAuthProjectionHTTPFields(t, serveAuthRequest(r, req), path != "/api/v1/users/me", corrupt)
			}
			refresh := authJSONRequest(http.MethodPost, "/api/v1/auth/refresh", "")
			refresh.Header.Set("Origin", "https://app.example.test")
			refresh.Header.Set("X-Auth-Session", refreshSID(t, cookie))
			refresh.AddCookie(cookie)
			rec := serveAuthRequest(r, refresh)
			assertAuthProjectionHTTPFields(t, rec, true, corrupt)
			if len(rec.Result().Cookies()) != 1 {
				t.Fatal("policy omission blocked refresh cookie")
			}
		})
	}
}
func assertAuthProjectionHTTPFields(t *testing.T, rec *httptest.ResponseRecorder, nested, omitted bool) {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("projection status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal("JSON")
	}
	if nested {
		body = body["user"].(map[string]any)
	}
	permissions, hasPermissions := body["admin_permissions"]
	version, hasVersion := body["permissions_version"]
	if omitted {
		if hasPermissions || hasVersion {
			t.Fatal("partial/fake projection")
		}
	} else {
		if !hasPermissions || !hasVersion || version != "0" || len(permissions.([]any)) != 0 {
			t.Fatal("ordinary projection")
		}
	}
}
