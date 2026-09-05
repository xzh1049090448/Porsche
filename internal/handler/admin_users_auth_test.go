package handler

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestLegacyAdminDeleteGoneRouteIsIsolatedAfterAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	previousLogWriter := log.Writer()
	previousGinWriter := gin.DefaultWriter
	log.SetOutput(&logs)
	gin.DefaultWriter = &logs
	t.Cleanup(func() {
		log.SetOutput(previousLogWriter)
		gin.DefaultWriter = previousGinWriter
	})

	engine := gin.New()
	admin := engine.Group("/admin/users", func(c *gin.Context) {
		// This middleware models successful RequireAdmin context injection while
		// deliberately providing nil business dependencies as panic guards.
		c.Set(middleware.ContextUser, &models.User{ID: 7, Role: models.UserRoleAdmin})
		c.Set(middleware.ContextUserID, int64(7))
		c.Set("app_state", &app.State{})
		c.Next()
	})
	admin.DELETE("/:guid", gatewayRequestID(), adminUserActionNoStore, legacyAdminUserDeleteGone)

	for _, testCase := range []struct {
		name      string
		path      string
		requestID string
	}{
		{name: "canonical", path: "/admin/users/123456789012345678", requestID: "legacy-delete-request"},
		{name: "missing", path: "/admin/users/9223372036854775807"},
		{name: "malformed", path: "/admin/users/not-a-guid"},
		{name: "already-deleted", path: "/admin/users/987654321"},
		{name: "query-variant", path: "/admin/users/123456789012345678?reason=reason-secret&password=password-secret&target=target-secret"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, testCase.path, nil)
			if testCase.requestID != "" {
				req.Header.Set("X-Request-ID", testCase.requestID)
			}
			engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusGone || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" || (testCase.requestID != "" && rec.Header().Get("X-Request-ID") != testCase.requestID) {
				t.Fatalf("status=%d cache_control=%q request_id_present=%t body_length=%d", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID") != "", rec.Body.Len())
			}
			want := `{"error":{"code":"legacy_user_delete_gone","message":"Use the verified v2 user delete action flow.","type":"admin_action_error","request_id":"` + rec.Header().Get("X-Request-ID") + `"}}`
			if rec.Body.String() != want {
				t.Fatalf("response body mismatch body_length=%d", rec.Body.Len())
			}
			for _, forbidden := range []string{"reason-secret", "password-secret", "target-secret", "123456789012345678"} {
				if strings.Contains(logs.String(), forbidden) {
					t.Fatalf("handler log leaked a forbidden value log_length=%d", logs.Len())
				}
			}
		})
	}
}

func TestLegacyAdminDeleteGoneRetainsAuthenticationBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterAdminUsers(engine, &app.State{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/users/not-a-guid", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want_status=%d body_length=%d", rec.Code, http.StatusUnauthorized, rec.Body.Len())
	}
}

// TestAdminUserBehaviorRequiresStrictlyLowerTargetRole ensures the behavior
// endpoint observes the same strictly-downward management hierarchy as the
// other administrator user endpoints.
func TestAdminUserBehaviorRequiresStrictlyLowerTargetRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newPlatformWhiteLabelTestState(t)

	for _, testCase := range []struct {
		name       string
		actorRole  models.UserRole
		targetRole models.UserRole
		deleted    int
		wantStatus int
	}{
		{name: "admin-reads-same-admin", actorRole: models.UserRoleAdmin, targetRole: models.UserRoleAdmin, wantStatus: http.StatusNotFound},
		{name: "admin-reads-root", actorRole: models.UserRoleAdmin, targetRole: models.UserRoleRoot, wantStatus: http.StatusNotFound},
		{name: "root-reads-lower-role", actorRole: models.UserRoleRoot, targetRole: models.UserRoleUser, wantStatus: http.StatusOK},
		{name: "soft-deleted-target", actorRole: models.UserRoleRoot, targetRole: models.UserRoleUser, deleted: 1, wantStatus: http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			actor := platformTestUser("behavior-actor", nil)
			actor.Role = testCase.actorRole
			if err := state.DB.Create(&actor).Error; err != nil {
				t.Fatal(err)
			}
			target := platformTestUser("behavior-target", nil)
			target.Role = testCase.targetRole
			target.IsDeleted = testCase.deleted
			if err := state.DB.Create(&target).Error; err != nil {
				t.Fatal(err)
			}

			engine := gin.New()
			RegisterAdminUsers(engine, state)
			req := httptest.NewRequest(http.MethodGet, "/admin/users/"+strconv.FormatInt(target.Guid, 10)+"/behavior", nil)
			req.Header.Set("Authorization", "Bearer "+platformJWT(t, state, &actor))
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != testCase.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), testCase.wantStatus)
			}
		})
	}
}
