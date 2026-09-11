package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
)

func TestDecodeAdminUserUpdateRetiresStatusBeforeLegacyMutation(t *testing.T) {
	for _, body := range []string{
		`{"status":"disabled"}`,
		`{"status":null}`,
		`{"plan_type":"professional","status":"active"}`,
	} {
		if _, err := decodeAdminUserUpdate(strings.NewReader(body)); !errors.Is(err, errAdminUserUpdateStatusRetired) {
			t.Fatalf("body=%s error=%v", body, err)
		}
	}
}

func TestDecodeAdminUserUpdateRetiresEntitlementFieldsBeforeLegacyMutation(t *testing.T) {
	for _, body := range []string{
		`{"plan_type":"free"}`,
		`{"allowed_models":[]}`,
		`{"daily_call_limit":100}`,
		`{"plan_type":null,"allowed_models":null,"daily_call_limit":null}`,
	} {
		if _, err := decodeAdminUserUpdate(strings.NewReader(body)); !errors.Is(err, errAdminUserUpdateStatusRetired) {
			t.Fatalf("body=%s error=%v", body, err)
		}
	}
}

func TestDecodeAdminUserUpdateRetiresA08FieldsBeforeLegacyMutation(t *testing.T) {
	for _, field := range []string{"role", "auth_version", "expected_auth_version", "expected_permissions_version", "permissions_version", "catalog_version", "overrides", "action", "reason"} {
		body := `{"` + field + `":null}`
		if _, err := decodeAdminUserUpdate(strings.NewReader(body)); !errors.Is(err, errAdminUserUpdateStatusRetired) {
			t.Fatalf("field=%s error=%v", field, err)
		}
	}
}

// TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON exercises the real
// protected PUT route. Every rejected payload must leave the target's account
// state, session rows, and auth-audit history untouched.
func TestAdminUserUpdateRejectsMalformedOrOutOfContractJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "empty-body", body: "", wantStatus: http.StatusBadRequest},
		{name: "top-level-null", body: "null", wantStatus: http.StatusBadRequest},
		{name: "array", body: "[]", wantStatus: http.StatusBadRequest},
		{name: "unknown-role", body: `{"role":"root"}`, wantStatus: http.StatusBadRequest},
		{name: "retired-auth-version", body: `{"auth_version":1}`, wantStatus: http.StatusBadRequest},
		{name: "retired-permissions-version", body: `{"permissions_version":1}`, wantStatus: http.StatusBadRequest},
		{name: "retired-expected-permissions-version", body: `{"expected_permissions_version":1}`, wantStatus: http.StatusBadRequest},
		{name: "retired-catalog-version", body: `{"catalog_version":1}`, wantStatus: http.StatusBadRequest},
		{name: "retired-overrides", body: `{"overrides":[]}`, wantStatus: http.StatusBadRequest},
		{name: "retired-action", body: `{"action":"promote"}`, wantStatus: http.StatusBadRequest},
		{name: "unknown-permissions", body: `{"permissions":["users.promote"]}`, wantStatus: http.StatusBadRequest},
		{name: "unknown-money", body: `{"amount":1}`, wantStatus: http.StatusBadRequest},
		{name: "retired-status-only", body: `{"status":"disabled"}`, wantStatus: http.StatusBadRequest},
		{name: "retired-status-null", body: `{"status":null}`, wantStatus: http.StatusBadRequest},
		{name: "retired-status-mixed", body: `{"status":"active","plan_type":"professional"}`, wantStatus: http.StatusBadRequest},
		{name: "retired-group", body: `{"group_guid":null}`, wantStatus: http.StatusBadRequest},
		{name: "retired-password", body: `{"password":null}`, wantStatus: http.StatusBadRequest},
		{name: "retired-new-password", body: `{"new_password":null}`, wantStatus: http.StatusBadRequest},
		{name: "retired-current-password", body: `{"current_password":null}`, wantStatus: http.StatusBadRequest},
		{name: "retired-reason", body: `{"reason":null}`, wantStatus: http.StatusBadRequest},
		{name: "retired-version", body: `{"expected_auth_version":null}`, wantStatus: http.StatusBadRequest},
		{name: "retired-mixed-entitlement", body: `{"group_guid":"1","reason":"x","expected_auth_version":1}`, wantStatus: http.StatusBadRequest},
		{name: "duplicate-key", body: `{"status":"active","status":"disabled"}`, wantStatus: http.StatusBadRequest},
		{name: "case-alias", body: `{"Status":"active"}`, wantStatus: http.StatusBadRequest},
		{name: "trailing-json", body: `{"status":"active"} {}`, wantStatus: http.StatusBadRequest},
		{name: "status-wrong-type", body: `{"status":1}`, wantStatus: http.StatusBadRequest},
		{name: "plan-wrong-type", body: `{"plan_type":[]}`, wantStatus: http.StatusBadRequest},
		{name: "acl-wrong-type", body: `{"allowed_models":"model-a"}`, wantStatus: http.StatusBadRequest},
		{name: "acl-null-element", body: `{"allowed_models":[null]}`, wantStatus: http.StatusBadRequest},
		{name: "acl-empty-model", body: `{"allowed_models":[""]}`, wantStatus: http.StatusBadRequest},
		{name: "daily-limit-wrong-type", body: `{"daily_call_limit":"1"}`, wantStatus: http.StatusBadRequest},
		{name: "daily-limit-negative", body: `{"daily_call_limit":-1}`, wantStatus: http.StatusBadRequest},
		{name: "daily-limit-over-int32", body: `{"daily_call_limit":2147483648}`, wantStatus: http.StatusBadRequest},
		{name: "unknown-status-enum", body: `{"status":"paused"}`, wantStatus: http.StatusBadRequest},
		{name: "unknown-plan-enum", body: `{"plan_type":"gold"}`, wantStatus: http.StatusBadRequest},
		{name: "over-64-kib", body: `{"status":"active","padding":"` + strings.Repeat("x", 64*1024) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			state := authHTTPTestState(t)
			actor := platformTestUser(t, state, "update-actor", nil)
			actor.Role = models.UserRoleAdmin
			target := platformTestUser(t, state, "update-target", models.JSONSlice{"model-a"})
			target.DailyCallLimit = 17
			target.UpdatedAt = 1_700_000_000_000
			if err := state.DB.Create(&actor).Error; err != nil {
				t.Fatal(err)
			}
			if err := state.DB.Create(&target).Error; err != nil {
				t.Fatal(err)
			}
			access := platformJWT(t, state, &actor)
			_ = platformJWT(t, state, &target)
			var targetSession models.Session
			if err := state.DB.Where("user_id = ? AND is_deleted = 0", target.ID).First(&targetSession).Error; err != nil {
				t.Fatal(err)
			}
			if err := state.DB.Where("id = ? AND is_deleted = 0", target.ID).First(&target).Error; err != nil {
				t.Fatal(err)
			}

			var auditBefore, sessionsBefore int64
			if err := state.DB.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND is_deleted = 0", target.ID).Count(&auditBefore).Error; err != nil {
				t.Fatal(err)
			}
			if err := state.DB.Model(&models.Session{}).Where("user_id = ? AND is_deleted = 0", target.ID).Count(&sessionsBefore).Error; err != nil {
				t.Fatal(err)
			}

			engine := gin.New()
			RegisterAdminUsers(engine, state)
			req := httptest.NewRequest(http.MethodPut, "/admin/users/"+strconv.FormatInt(target.Guid, 10), strings.NewReader(testCase.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+access)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != testCase.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), testCase.wantStatus)
			}

			var after models.User
			if err := state.DB.Where("id = ? AND is_deleted = 0", target.ID).First(&after).Error; err != nil {
				t.Fatal(err)
			}
			if after.Status != target.Status || after.PlanType != target.PlanType || after.DailyCallLimit != target.DailyCallLimit || after.AuthVersion != target.AuthVersion || after.UpdatedAt != target.UpdatedAt || (after.UpdatedBy == nil) != (target.UpdatedBy == nil) || (after.UpdatedBy != nil && *after.UpdatedBy != *target.UpdatedBy) || (after.LastLoginAt == nil) != (target.LastLoginAt == nil) || (after.LastLoginAt != nil && *after.LastLoginAt != *target.LastLoginAt) || strings.Join(after.AllowedModels, "\x00") != strings.Join(target.AllowedModels, "\x00") {
				t.Fatalf("rejected input changed target: before=%+v after=%+v", target, after)
			}
			var auditAfter, sessionsAfter int64
			if err := state.DB.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND is_deleted = 0", target.ID).Count(&auditAfter).Error; err != nil {
				t.Fatal(err)
			}
			if err := state.DB.Model(&models.Session{}).Where("user_id = ? AND is_deleted = 0", target.ID).Count(&sessionsAfter).Error; err != nil {
				t.Fatal(err)
			}
			var afterSession models.Session
			if err := state.DB.First(&afterSession, targetSession.ID).Error; err != nil {
				t.Fatal(err)
			}
			if auditAfter != auditBefore || sessionsAfter != sessionsBefore {
				t.Fatalf("rejected input changed target side effects: audit %d->%d sessions %d->%d", auditBefore, auditAfter, sessionsBefore, sessionsAfter)
			}
			if afterSession.RevokedAt != nil {
				t.Fatalf("rejected input revoked target session: %+v", afterSession)
			}
		})
	}
}

// TestAdminUserUpdateNoOpAndAuthenticationPreconditions separates the route's
// 401 authentication gate from the authenticated JSON contract, and keeps
// explicit nulls and same-value updates free of database writes.
func TestAdminUserUpdateNoOpAndAuthenticationPreconditions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := authHTTPTestState(t)
	actor := platformTestUser(t, state, "update-actor", nil)
	actor.Role = models.UserRoleAdmin
	target := platformTestUser(t, state, "update-target", models.JSONSlice{"model-a"})
	target.DailyCallLimit = 17
	target.UpdatedAt = 1_700_000_000_000
	if err := state.DB.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	if err := state.DB.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	RegisterAdminUsers(engine, state)

	unauthenticated := httptest.NewRequest(http.MethodPut, "/admin/users/"+strconv.FormatInt(target.Guid, 10), strings.NewReader(`{"role":"root"}`))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticatedRec := httptest.NewRecorder()
	engine.ServeHTTP(unauthenticatedRec, unauthenticated)
	if unauthenticatedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated update status=%d body=%s, want 401", unauthenticatedRec.Code, unauthenticatedRec.Body.String())
	}

	access := platformJWT(t, state, &actor)
	for _, body := range []string{
		`{}`,
	} {
		req := httptest.NewRequest(http.MethodPut, "/admin/users/"+strconv.FormatInt(target.Guid, 10), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("no-op body=%s status=%d response=%s, want 200", body, rec.Code, rec.Body.String())
		}
		var after models.User
		if err := state.DB.Where("id = ? AND is_deleted = 0", target.ID).First(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after.UpdatedAt != target.UpdatedAt || after.UpdatedBy != target.UpdatedBy || after.AuthVersion != target.AuthVersion {
			t.Fatalf("no-op body=%s wrote target: before=%+v after=%+v", body, target, after)
		}
	}
}

// TestAdminUserUpdateCannotApplyPlanThroughLegacyPUT proves the retired route
// cannot bypass the dedicated A07 contract or mutate security state.
func TestAdminUserUpdateCannotApplyPlanThroughLegacyPUT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := authHTTPTestState(t)
	actor := platformTestUser(t, state, "update-actor", nil)
	actor.Role = models.UserRoleAdmin
	target := platformTestUser(t, state, "update-target", models.JSONSlice{"model-a"})
	if err := state.DB.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	if err := state.DB.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	targetSID, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	targetSession := models.Session{
		AuditFields:    models.AuditFields{Guid: platformTestSnowflake.Next(), CreatedAt: target.CreatedAt, UpdatedAt: target.UpdatedAt},
		SID:            targetSID,
		UserID:         target.ID,
		LoginMethod:    models.LoginMethodPassword,
		SessionVersion: 3,
		RefreshHMAC:    strings.Repeat("a", 64),
		LastActiveAt:   target.UpdatedAt,
		ExpiresAt:      target.UpdatedAt + 86_400_000,
	}
	if err := state.DB.Create(&targetSession).Error; err != nil {
		t.Fatal(err)
	}
	actorAccess := platformJWT(t, state, &actor)
	engine := gin.New()
	RegisterAdminUsers(engine, state)

	request := httptest.NewRequest(http.MethodPut, "/admin/users/"+strconv.FormatInt(target.Guid, 10), strings.NewReader(`{"plan_type":"professional"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+actorAccess)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("legacy plan update status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
	}

	var updated models.User
	if err := state.DB.Where("id = ? AND is_deleted = 0", target.ID).First(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if updated.PlanType != target.PlanType || updated.AuthVersion != target.AuthVersion {
		t.Fatalf("legacy route mutated plan/auth version = (%v, %d), want (%v, %d)", updated.PlanType, updated.AuthVersion, target.PlanType, target.AuthVersion)
	}
	var storedSession models.Session
	if err := state.DB.Where("id = ? AND user_id = ? AND is_deleted = 0", targetSession.ID, target.ID).First(&storedSession).Error; err != nil {
		t.Fatalf("legacy rejection lost target session: err=%v", err)
	}
	if storedSession.SID != targetSession.SID || storedSession.SessionVersion != targetSession.SessionVersion ||
		storedSession.RefreshHMAC != targetSession.RefreshHMAC || storedSession.PreviousRefreshHMAC != nil ||
		storedSession.PreviousRefreshExpiresAt != nil || storedSession.RevokedAt != nil ||
		storedSession.LastActiveAt != targetSession.LastActiveAt || storedSession.ExpiresAt != targetSession.ExpiresAt ||
		storedSession.UpdatedAt != targetSession.UpdatedAt || storedSession.UpdatedBy != nil || storedSession.IsDeleted != 0 {
		t.Fatalf("legacy rejection changed target session: before=%+v after=%+v", targetSession, storedSession)
	}
	var updateAudits int64
	if err := state.DB.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ? AND is_deleted = 0", target.ID, models.AuthAuditEventManagedUserUpdated).Count(&updateAudits).Error; err != nil || updateAudits != 0 {
		t.Fatalf("managed-user update audit count=%d err=%v, want 0", updateAudits, err)
	}
}
