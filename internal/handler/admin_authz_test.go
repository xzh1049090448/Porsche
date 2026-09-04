package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAdminAuthzHTTPAuthenticationEnvelopeWithoutFixture(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterAdminAuthz(r, &app.State{})
	RegisterAdminUsers(r, &app.State{})
	for _, path := range []string{"/admin/v2/authz/catalog", "/admin/v2/users/123/permissions"} {
		for _, authorization := range []string{"", "Basic invalid", "Bearer malformed"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", authorization)
			req.Header.Set("X-Request-ID", "authz-read-contract-test")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			adminAuthzAssertError(t, rec, http.StatusUnauthorized, "")
			if rec.Header().Get("X-Request-ID") != "authz-read-contract-test" {
				t.Fatal("authentication error lost its valid request ID")
			}
		}
	}
}

func adminAuthzAssertError(t *testing.T, rec *httptest.ResponseRecorder, status int, detail string) {
	t.Helper()
	adminAuthzAssertHeaders(t, rec)
	if rec.Code != status {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, status, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 {
		t.Fatalf("error must contain only detail: %s", rec.Body.String())
	}
	value, ok := body["detail"].(string)
	if !ok || value == "" || (detail != "" && value != detail) {
		t.Fatalf("unexpected public detail: %s", rec.Body.String())
	}
}

func adminAuthzAssertHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
		t.Fatalf("missing noncacheable/request-ID contract: %v", rec.Header())
	}
}

var adminAuthzExpectedNames = []string{
	"users.read", "users.create", "users.edit", "users.enable", "users.disable",
	"users.reset_password", "users.sessions.read", "users.sessions.revoke", "users.plan.change", "users.group.change",
	"users.quota.adjust", "users.delete", "users.deleted.read", "users.promote", "users.demote", "users.permissions.write",
	"users.audit.read", "groups.read", "groups.write", "public_content.read", "public_content.edit", "public_content.preview", "public_content.publish", "public_content.rollback",
}

func TestAdminAuthzHTTPCatalogRoleMatrixAndDTO(t *testing.T) {
	state := adminAuthzHTTPState(t)
	r := gin.New()
	RegisterAdminAuthz(r, state)
	RegisterAdminUsers(r, state)
	for _, role := range []models.UserRole{models.UserRoleRoot, models.UserRoleAdmin, models.UserRoleUser} {
		t.Run(role.String(), func(t *testing.T) {
			actor := adminAuthzHTTPUser(t, state, role)
			rec := adminAuthzRequest(r, "/admin/v2/authz/catalog", platformJWT(t, state, actor))
			if role == models.UserRoleUser {
				adminAuthzAssertError(t, rec, 403, "无权限访问")
				return
			}
			body := adminAuthzObject(t, rec, "catalog_version", "override_effects", "capabilities")
			if body["catalog_version"] != float64(1) || !reflect.DeepEqual(body["override_effects"], []any{"inherit", "allow", "deny"}) {
				t.Fatal("catalog version/effect contract differs")
			}
			items := adminAuthzCapabilities(t, body)
			for i, item := range items {
				adminAuthzKeys(t, item, "name", "admin_default", "grantable", "root_only", "available")
				rootOnly := i == 13 || i == 14 || i == 15 || i == 18
				if item["name"] != adminAuthzExpectedNames[i] || item["admin_default"] != adminAuthzBaseline(i) || item["root_only"] != rootOnly || item["available"] != (i != 10) || item["grantable"] != (!rootOnly && i != 10) {
					t.Fatalf("catalog capability %d differs: %v", i, item)
				}
			}
		})
	}
}

func TestAdminAuthzHTTPDetailTargetVisibility(t *testing.T) {
	state := adminAuthzHTTPState(t)
	r := gin.New()
	RegisterAdminAuthz(r, state)
	root := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
	access := platformJWT(t, state, root)
	for _, tc := range []struct {
		name    string
		role    models.UserRole
		status  models.UserStatus
		deleted int
		want    int
	}{
		{"active_admin", models.UserRoleAdmin, models.UserStatusActive, 0, 200},
		{"disabled_admin", models.UserRoleAdmin, models.UserStatusDisabled, 0, 200},
		{"root", models.UserRoleRoot, models.UserStatusActive, 0, 404},
		{"user", models.UserRoleUser, models.UserStatusActive, 0, 404},
		{"deleted_admin", models.UserRoleAdmin, models.UserStatusActive, 1, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := adminAuthzHTTPUser(t, state, tc.role)
			if err := state.DB.Model(target).Updates(map[string]any{"status": tc.status, "is_deleted": tc.deleted}).Error; err != nil {
				t.Fatal("prepare target failed")
			}
			rec := adminAuthzRequest(r, adminAuthzDetailPath(target.Guid), access)
			if tc.want != 200 {
				adminAuthzAssertError(t, rec, tc.want, "权限目标不存在")
				return
			}
			adminAuthzAssertDetail(t, rec, target.Guid, tc.status.String(), "0", nil)
		})
	}
	for _, guid := range []int64{root.Guid, 9223372036854775807} {
		adminAuthzAssertError(t, adminAuthzRequest(r, adminAuthzDetailPath(guid), access), 404, "权限目标不存在")
	}
	for _, role := range []models.UserRole{models.UserRoleAdmin, models.UserRoleUser} {
		actor := adminAuthzHTTPUser(t, state, role)
		adminAuthzAssertError(t, adminAuthzRequest(r, adminAuthzDetailPath(root.Guid), platformJWT(t, state, actor)), 403, "无权限访问")
	}
}

func TestAdminAuthzHTTPRejectsNoncanonicalGUIDAndQuery(t *testing.T) {
	state := adminAuthzHTTPState(t)
	r := gin.New()
	RegisterAdminAuthz(r, state)
	actor := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
	access := platformJWT(t, state, actor)
	for _, guid := range []string{"0", "-1", "+1", "01", "9223372036854775808", "abc", "1.0", "%201", "1%20"} {
		t.Run(guid, func(t *testing.T) {
			adminAuthzAssertError(t, adminAuthzRequest(r, "/admin/v2/users/"+guid+"/permissions", access), 400, "无效请求参数")
		})
	}
	for _, suffix := range []string{"?x=1", "?x", "?%zz", "?&&", "?guid=123"} {
		for _, path := range []string{"/admin/v2/authz/catalog", adminAuthzDetailPath(actor.Guid)} {
			adminAuthzAssertError(t, adminAuthzRequest(r, path+suffix, access), 400, "无效请求参数")
		}
	}
}

func TestAdminAuthzHTTPPolicyProjectionAndCorruption(t *testing.T) {
	state := adminAuthzHTTPState(t)
	r := gin.New()
	RegisterAdminAuthz(r, state)
	root := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
	access := platformJWT(t, state, root)
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled_%t", disabled), func(t *testing.T) {
			target := adminAuthzHTTPUser(t, state, models.UserRoleAdmin)
			status := "active"
			if disabled {
				if err := state.DB.Model(target).Update("status", models.UserStatusDisabled).Error; err != nil {
					t.Fatal("disable fixture failed")
				}
				status = "disabled"
			}
			head := models.PermissionPolicyHead{AuditFields: adminAuthzAudit(), UserID: target.ID, PolicyVersion: 7, CatalogVersion: 1, RuleCount: 2}
			if err := state.DB.Create(&head).Error; err != nil {
				t.Fatal("create policy head failed")
			}
			for _, pair := range [][2]int{{1, 3}, {6, 2}} {
				rule := models.PermissionOverride{AuditFields: adminAuthzAudit(), UserID: target.ID, PolicyVersion: 7, Capability: pair[0], Effect: pair[1]}
				if err := state.DB.Create(&rule).Error; err != nil {
					t.Fatal("create policy override failed")
				}
			}
			adminAuthzAssertDetail(t, adminAuthzRequest(r, adminAuthzDetailPath(target.Guid), access), target.Guid, status, "7", map[int]string{0: "deny", 5: "allow"})
			if err := state.DB.Model(&head).Update("rule_count", 3).Error; err != nil {
				t.Fatal("prepare corrupt policy failed")
			}
			adminAuthzAssertError(t, adminAuthzRequest(r, adminAuthzDetailPath(target.Guid), access), 503, "权限信息暂不可用")
		})
	}
	target := adminAuthzHTTPUser(t, state, models.UserRoleAdmin)
	head := models.PermissionPolicyHead{AuditFields: adminAuthzAudit(), UserID: target.ID, PolicyVersion: 9, CatalogVersion: 1, RuleCount: 0}
	if err := state.DB.Create(&head).Error; err != nil {
		t.Fatal("create empty head failed")
	}
	adminAuthzAssertDetail(t, adminAuthzRequest(r, adminAuthzDetailPath(target.Guid), access), target.Guid, "active", "9", nil)
}

func TestAdminAuthzHTTPRejectsStaleOrRevokedAuthentication(t *testing.T) {
	state := adminAuthzHTTPState(t)
	r := gin.New()
	RegisterAdminAuthz(r, state)
	for _, field := range []string{"auth_version", "revoked_at", "is_deleted", "status"} {
		t.Run(field, func(t *testing.T) {
			actor := adminAuthzHTTPUser(t, state, models.UserRoleRoot)
			access := platformJWT(t, state, actor)
			switch field {
			case "revoked_at":
				if err := state.DB.Model(&models.Session{}).Where("user_id = ?", actor.ID).Update("revoked_at", time.Now().UnixMilli()).Error; err != nil {
					t.Fatal("revoke fixture failed")
				}
			case "auth_version":
				if err := state.DB.Model(actor).Update(field, 2).Error; err != nil {
					t.Fatal("stale fixture failed")
				}
			case "is_deleted":
				if err := state.DB.Model(actor).Update(field, 1).Error; err != nil {
					t.Fatal("deleted fixture failed")
				}
			case "status":
				if err := state.DB.Model(actor).Update(field, models.UserStatusDisabled).Error; err != nil {
					t.Fatal("disabled fixture failed")
				}
			}
			for _, path := range []string{"/admin/v2/authz/catalog", adminAuthzDetailPath(actor.Guid)} {
				adminAuthzAssertError(t, adminAuthzRequest(r, path, access), 401, "")
			}
		})
	}
}

func adminAuthzRequest(r http.Handler, path, access string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}
func adminAuthzDetailPath(guid int64) string {
	return "/admin/v2/users/" + strconv.FormatInt(guid, 10) + "/permissions"
}
func adminAuthzAudit() models.AuditFields {
	now := time.Now().UTC().UnixMilli()
	return models.AuditFields{Guid: platformTestSnowflake.Next(), CreatedAt: now, UpdatedAt: now}
}
func adminAuthzBaseline(i int) bool { return i < 5 || i == 16 || i == 17 }
func adminAuthzKeys(t *testing.T, body map[string]any, keys ...string) {
	t.Helper()
	if len(body) != len(keys) {
		t.Fatalf("DTO field allowlist differs: %v", body)
	}
	for _, key := range keys {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing field %s", key)
		}
	}
}
func adminAuthzObject(t *testing.T, rec *httptest.ResponseRecorder, keys ...string) map[string]any {
	t.Helper()
	adminAuthzAssertHeaders(t, rec)
	if rec.Code != 200 {
		t.Fatalf("status=%d want=200 body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal("invalid response JSON")
	}
	adminAuthzKeys(t, body, keys...)
	return body
}
func adminAuthzCapabilities(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["capabilities"].([]any)
	if !ok || len(raw) != 24 {
		t.Fatal("capabilities must be a non-null 24-entry array")
	}
	items := make([]map[string]any, len(raw))
	for i, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			t.Fatal("capability must be an object")
		}
		items[i] = item
	}
	return items
}
func adminAuthzAssertDetail(t *testing.T, rec *httptest.ResponseRecorder, guid int64, status, version string, overrides map[int]string) {
	t.Helper()
	body := adminAuthzObject(t, rec, "user_guid", "role", "status", "catalog_version", "permissions_version", "capabilities")
	if body["user_guid"] != strconv.FormatInt(guid, 10) || body["role"] != "admin" || body["status"] != status || body["catalog_version"] != float64(1) || body["permissions_version"] != version {
		t.Fatalf("detail identity/version differs: %v", body)
	}
	for i, item := range adminAuthzCapabilities(t, body) {
		adminAuthzKeys(t, item, "name", "baseline", "override", "policy_effective", "effective")
		override := overrides[i]
		if override == "" {
			override = "inherit"
		}
		policy := adminAuthzBaseline(i)
		if override == "allow" {
			policy = true
		}
		if override == "deny" {
			policy = false
		}
		if item["name"] != adminAuthzExpectedNames[i] || item["baseline"] != adminAuthzBaseline(i) || item["override"] != override || item["policy_effective"] != policy || item["effective"] != (status == "active" && policy) {
			t.Fatalf("capability %d projection differs: %v", i, item)
		}
	}
}

// This helper only connects to the coordinator-provided fixtures. It never
// migrates schema, clears tables, flushes Redis, or reads production settings.
func adminAuthzHTTPState(t *testing.T) *app.State {
	t.Helper()
	databaseURL, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Skip("requires explicit isolated TEST_DATABASE_URL and TEST_REDIS_URL fixtures")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed == nil || (!strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") && parsed.Path != "/porsche_test") {
		t.Fatal("TEST_DATABASE_URL must identify a disposable test database")
	}
	gdb, err := db.Open(databaseURL, "test")
	if err != nil {
		t.Fatal("open isolated MySQL fixture failed")
	}
	gdb = gdb.Session(&gorm.Session{Logger: logger.Discard})
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal("get isolated SQL pool failed")
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var actual string
	if err := gdb.Raw("SELECT DATABASE()").Scan(&actual).Error; err != nil || actual != strings.TrimPrefix(parsed.Path, "/") {
		t.Fatal("connected database does not match explicit fixture")
	}
	settings := &config.Settings{AppEnv: "test", DatabaseURL: databaseURL, RedisURL: redisURL,
		JWTSecretKey: "admin-authz-http-fixture-jwt", AuthHMACKey: "admin-authz-http-fixture-hmac-0123456789",
		SessionDays: 1, SessionAccessMinutes: 15, SessionMaxActive: 10, SessionIssueLimit24h: 30, RefreshReplaySeconds: 30}
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal("initialize isolated application fixture failed")
	}
	t.Cleanup(func() { _ = state.AuthRedis.Close() })
	gin.SetMode(gin.TestMode)
	return state
}
func adminAuthzHTTPUser(t *testing.T, state *app.State, role models.UserRole) *models.User {
	t.Helper()
	user := platformTestUser("admin-authz-read", nil)
	user.Role = role
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal("create unique HTTP fixture user failed")
	}
	return &user
}
