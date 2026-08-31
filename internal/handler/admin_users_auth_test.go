package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/models"
)

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
		{name: "admin-reads-same-admin", actorRole: models.UserRoleAdmin, targetRole: models.UserRoleAdmin, wantStatus: http.StatusForbidden},
		{name: "admin-reads-root", actorRole: models.UserRoleAdmin, targetRole: models.UserRoleRoot, wantStatus: http.StatusForbidden},
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
