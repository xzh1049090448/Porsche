package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestAdminUsersActiveGroupsRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterAdminGroupsRead(router, &app.State{})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v2/groups?status=active", nil))
	adminAuthzAssertError(t, recorder, http.StatusUnauthorized, "")
}

func TestAdminUsersActiveGroupsExactQueryPermissionOrderingAndDTO(t *testing.T) {
	state := adminAuthzHTTPState(t)
	router := gin.New()
	RegisterAdminGroupsRead(router, state)
	root := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
	access := platformJWT(t, state, root)

	for _, group := range []models.BusinessGroup{
		{AuditFields: adminAuthzAudit(), Key: "a-task9-" + strconv.FormatInt(root.Guid, 10), DisplayName: "Alpha", Status: models.BusinessGroupStatusActive},
		{AuditFields: adminAuthzAudit(), Key: "z-task9-" + strconv.FormatInt(root.Guid, 10), DisplayName: "Zulu", Status: models.BusinessGroupStatusActive},
		{AuditFields: adminAuthzAudit(), Key: "inactive-task9-" + strconv.FormatInt(root.Guid, 10), DisplayName: "Inactive", Status: models.BusinessGroupStatusInactive},
	} {
		candidate := group
		if err := state.DB.Create(&candidate).Error; err != nil {
			t.Fatal("create group fixture")
		}
	}
	for _, path := range []string{
		"/admin/v2/groups", "/admin/v2/groups?status=", "/admin/v2/groups?status=inactive",
		"/admin/v2/groups?status=active&status=active", "/admin/v2/groups?status=active&x=1",
		"/admin/v2/groups?x=1&status=active", "/admin/v2/groups?status=%61ctive",
	} {
		assertAdminGroupsActionError(t, adminAuthzRequest(router, path, access), http.StatusBadRequest, "invalid_admin_action_request")
	}

	recorder := adminAuthzRequest(router, "/admin/v2/groups?status=active", access)
	adminAuthzAssertHeaders(t, recorder)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || len(body) != 1 {
		t.Fatalf("invalid group envelope: %s", recorder.Body.String())
	}
	rawItems, ok := body["items"].([]any)
	if !ok || len(rawItems) < 3 {
		t.Fatalf("group items missing: %s", recorder.Body.String())
	}
	keys := make([]string, 0, len(rawItems))
	defaultCount := 0
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok || len(item) != 3 {
			t.Fatalf("group item allowlist differs: %#v", raw)
		}
		guid, guidOK := item["guid"].(string)
		key, keyOK := item["key"].(string)
		displayName, displayOK := item["display_name"].(string)
		if !guidOK || !keyOK || !displayOK || displayName == "" || guid == "" || guid[0] == '0' {
			t.Fatalf("invalid public group DTO: %#v", item)
		}
		if key == "default" {
			defaultCount++
		}
		if key[:1] == "i" && len(key) > len("inactive-task9-") && key[:len("inactive-task9-")] == "inactive-task9-" {
			t.Fatalf("inactive group leaked: %#v", item)
		}
		keys = append(keys, key)
	}
	want := append([]string(nil), keys...)
	sort.Strings(want)
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("groups are not key ordered: %v", keys)
		}
	}
	if defaultCount != 1 {
		t.Fatalf("default count=%d body=%s", defaultCount, recorder.Body.String())
	}

	admin := adminAuthzHTTPUser(t, state, models.UserRoleAdmin)
	capability, ok := models.PermissionCapabilityCode("groups.read")
	if !ok {
		t.Fatal("groups.read capability missing")
	}
	head := models.PermissionPolicyHead{AuditFields: adminAuthzAudit(), UserID: admin.ID, PolicyVersion: 1, CatalogVersion: 1, RuleCount: 1}
	rule := models.PermissionOverride{AuditFields: adminAuthzAudit(), UserID: admin.ID, PolicyVersion: 1, Capability: capability, Effect: 3}
	if err := state.DB.Create(&head).Error; err != nil {
		t.Fatal("create deny head")
	}
	if err := state.DB.Create(&rule).Error; err != nil {
		t.Fatal("create deny rule")
	}
	assertAdminGroupsActionError(t, adminAuthzRequest(router, "/admin/v2/groups?status=active", platformJWT(t, state, admin)), http.StatusForbidden, "action_operation_rejected")
}

func TestAdminUsersActiveGroupsDependencyAndCorruptionFailClosed(t *testing.T) {
	for _, mode := range []string{"missing_default", "corrupt_visible", "redis"} {
		t.Run(mode, func(t *testing.T) {
			state := adminAuthzHTTPState(t)
			router := gin.New()
			RegisterAdminGroupsRead(router, state)
			root := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
			access := platformJWT(t, state, root)
			var restore func()
			switch mode {
			case "missing_default":
				if err := state.DB.Model(&models.BusinessGroup{}).Where("BINARY group_key = BINARY ? AND is_deleted = 0", "default").Update("status", models.BusinessGroupStatusInactive).Error; err != nil {
					t.Fatal("hide default")
				}
				restore = func() {
					_ = state.DB.Model(&models.BusinessGroup{}).Where("BINARY group_key = BINARY ? AND is_deleted = 0", "default").Update("status", models.BusinessGroupStatusActive).Error
				}
			case "corrupt_visible":
				group := models.BusinessGroup{AuditFields: adminAuthzAudit(), Key: "INVALID", DisplayName: "Corrupt", Status: models.BusinessGroupStatusActive}
				if err := state.DB.Create(&group).Error; err != nil {
					t.Fatal("create corrupt group")
				}
				restore = func() { _ = state.DB.Delete(&group).Error }
			case "redis":
				if err := state.AuthRedis.Close(); err != nil {
					t.Fatal("close Redis fixture")
				}
			}
			if restore != nil {
				defer restore()
			}
			assertAdminGroupsActionError(t, adminAuthzRequest(router, "/admin/v2/groups?status=active", access), http.StatusServiceUnavailable, "action_dependency_unavailable")
		})
	}
}

func assertAdminGroupsActionError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	adminAuthzAssertHeaders(t, recorder)
	if recorder.Code != status {
		t.Fatalf("status=%d want=%d body=%s", recorder.Code, status, recorder.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || len(envelope) != 1 {
		t.Fatalf("invalid action error envelope: %s", recorder.Body.String())
	}
	errorBody, ok := envelope["error"].(map[string]any)
	if !ok || len(errorBody) != 4 || errorBody["code"] != code || errorBody["message"] != "请求无法完成" || errorBody["type"] != "admin_action_error" || errorBody["request_id"] == "" {
		t.Fatalf("invalid action error body: %s", recorder.Body.String())
	}
}
