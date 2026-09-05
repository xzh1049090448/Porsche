package handler

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
)

type legacyDeleteContractDocument struct {
	Endpoints struct {
		LegacyDelete struct {
			Method          string            `json:"method"`
			Path            string            `json:"path"`
			RequestHeaders  map[string]string `json:"request_headers"`
			ResponseHeaders map[string]string `json:"response_headers"`
			ResponseStatus  int               `json:"response_status"`
			ResponseExample json.RawMessage   `json:"response_example"`
		} `json:"legacy_delete"`
	} `json:"endpoints"`
}

func TestLegacyAdminDeleteGoneRuntimeMatchesFrozenContract(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("contract source location unavailable")
	}
	contractPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "docs", "agents", "contracts", "admin-action-future-contract.json")
	contents, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal("read frozen contract failed")
	}
	var document legacyDeleteContractDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal("decode frozen contract failed")
	}
	contract := document.Endpoints.LegacyDelete
	if contract.Method != http.MethodDelete || !strings.HasSuffix(contract.Path, "/:guid") {
		t.Fatal("legacy route contract is not the registered DELETE shape")
	}
	for name, rule := range contract.RequestHeaders {
		if rule != "ignored_no_effect" {
			t.Fatalf("legacy request header rule mismatch header=%q", name)
		}
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	groupPath := strings.TrimSuffix(contract.Path, "/:guid")
	admin := engine.Group(groupPath, func(c *gin.Context) {
		c.Set(middleware.ContextUser, &models.User{ID: 7, Role: models.UserRoleAdmin})
		c.Set(middleware.ContextUserID, int64(7))
		// Nil DB/Auth/Redis dependencies make any business-state access fail.
		c.Set("app_state", &app.State{})
		c.Next()
	})
	admin.DELETE("/:guid", gatewayRequestID(), adminUserActionNoStore, legacyAdminUserDeleteGone)

	headerCases := []http.Header{{}}
	for name := range contract.RequestHeaders {
		headerCases = append(headerCases, http.Header{name: {"contract-ignored-value"}})
	}
	both := http.Header{}
	for name := range contract.RequestHeaders {
		both.Set(name, "contract-ignored-value")
	}
	headerCases = append(headerCases, both)

	var expectedBody map[string]any
	if err := json.Unmarshal(contract.ResponseExample, &expectedBody); err != nil {
		t.Fatal("decode legacy response fixture failed")
	}
	expectedError, ok := expectedBody["error"].(map[string]any)
	if !ok {
		t.Fatal("legacy response fixture error object is invalid")
	}
	expectedRequestID, ok := expectedError["request_id"].(string)
	if !ok || expectedRequestID == "" {
		t.Fatal("legacy response fixture request id is invalid")
	}
	expectedBytes, err := json.Marshal(expectedBody)
	if err != nil {
		t.Fatal("normalize legacy response fixture failed")
	}

	requestPath := strings.Replace(contract.Path, ":guid", "123456789012345678", 1)
	for caseIndex, headers := range headerCases {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(contract.Method, requestPath, nil)
		request.Header = headers.Clone()
		engine.ServeHTTP(recorder, request)

		if recorder.Code != contract.ResponseStatus {
			t.Fatalf("legacy contract status mismatch case=%d got=%d", caseIndex, recorder.Code)
		}
		requestID := recorder.Header().Get("X-Request-ID")
		if requestID == "" {
			t.Fatalf("legacy contract request id missing case=%d", caseIndex)
		}
		for name, rule := range contract.ResponseHeaders {
			value := recorder.Header().Get(name)
			switch rule {
			case "required_non_empty":
				if value == "" {
					t.Fatalf("legacy response header missing case=%d header=%q", caseIndex, name)
				}
			case "no-store":
				if value != rule {
					t.Fatalf("legacy response header mismatch case=%d header=%q", caseIndex, name)
				}
			default:
				t.Fatalf("unsupported legacy response header rule header=%q", name)
			}
		}

		var actualBody map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &actualBody); err != nil {
			t.Fatalf("decode legacy runtime response failed case=%d body_length=%d", caseIndex, recorder.Body.Len())
		}
		actualError, ok := actualBody["error"].(map[string]any)
		if !ok || actualError["request_id"] != requestID {
			t.Fatalf("legacy runtime request id binding failed case=%d", caseIndex)
		}
		actualError["request_id"] = expectedRequestID
		actualBytes, err := json.Marshal(actualBody)
		if err != nil {
			t.Fatalf("normalize legacy runtime response failed case=%d", caseIndex)
		}
		if !bytes.Equal(actualBytes, expectedBytes) {
			t.Fatalf("legacy runtime response differs from contract case=%d body_length=%d", caseIndex, recorder.Body.Len())
		}
	}
}

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
