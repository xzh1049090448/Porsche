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
		{name: "unknown-permissions", body: `{"permissions":["users.promote"]}`, wantStatus: http.StatusBadRequest},
		{name: "unknown-money", body: `{"amount":1}`, wantStatus: http.StatusBadRequest},
		{name: "retired-status-only", body: `{"status":"disabled"}`, wantStatus: http.StatusBadRequest},
		{name: "retired-status-null", body: `{"status":null}`, wantStatus: http.StatusBadRequest},
		{name: "retired-status-mixed", body: `{"status":"active","plan_type":"professional"}`, wantStatus: http.StatusBadRequest},
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
		{name: "unknown-plan-enum", body: `{"plan_type":"gold"}`, wantStatus: http.StatusUnprocessableEntity},
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
		`{"plan_type":null,"allowed_models":null,"daily_call_limit":null}`,
		`{"plan_type":"free","allowed_models":["model-a","model-a"],"daily_call_limit":17}`,
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

// TestAdminUserUpdateAppliesPlanAndRevokesOnlyTheTargetSession proves that a
// valid protected PUT reaches the managed-user service after strict decoding.
// It checks the persisted enum, security invalidation, target audit event,
// and actual authentication outcomes for both users.
func TestAdminUserUpdateAppliesPlanAndRevokesOnlyTheTargetSession(t *testing.T) {
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
	actorAccess := platformJWT(t, state, &actor)
	targetAccess := platformJWT(t, state, &target)
	engine := gin.New()
	RegisterAuth(engine, state)
	RegisterAdminUsers(engine, state)

	request := httptest.NewRequest(http.MethodPut, "/admin/users/"+strconv.FormatInt(target.Guid, 10), strings.NewReader(`{"plan_type":"professional"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+actorAccess)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"plan_type":"professional"`) {
		t.Fatalf("plan update status=%d body=%s, want DTO plan_type professional", recorder.Code, recorder.Body.String())
	}

	var updated models.User
	if err := state.DB.Where("id = ? AND is_deleted = 0", target.ID).First(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if updated.PlanType != models.PlanProfessional || updated.AuthVersion != target.AuthVersion+1 {
		t.Fatalf("persisted plan/auth version = (%v, %d), want (%v, %d)", updated.PlanType, updated.AuthVersion, models.PlanProfessional, target.AuthVersion+1)
	}
	var targetSession models.Session
	if err := state.DB.Where("user_id = ? AND is_deleted = 0", target.ID).First(&targetSession).Error; err != nil || targetSession.RevokedAt == nil {
		t.Fatalf("target session was not durably revoked: session=%+v err=%v", targetSession, err)
	}
	var updateAudits int64
	if err := state.DB.Model(&models.AuthAuditEvent{}).Where("user_id = ? AND event_type = ? AND is_deleted = 0", target.ID, models.AuthAuditEventManagedUserUpdated).Count(&updateAudits).Error; err != nil || updateAudits != 1 {
		t.Fatalf("managed-user update audit count=%d err=%v, want 1", updateAudits, err)
	}

	for _, testCase := range []struct {
		name   string
		access string
		want   int
	}{
		{name: "target-access-revoked", access: targetAccess, want: http.StatusUnauthorized},
		{name: "actor-access-still-valid", access: actorAccess, want: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/self", nil)
			req.Header.Set("Authorization", "Bearer "+testCase.access)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != testCase.want {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), testCase.want)
			}
		})
	}
}
