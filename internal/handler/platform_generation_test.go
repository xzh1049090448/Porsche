package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const platformGenerationHandlerTestID = "550e8400-e29b-41d4-a716-446655440000"

type platformGenerationHandlerFake struct {
	get         func(context.Context, int64, string) (service.PlatformGenerationView, error)
	cancel      func(context.Context, int64, string) (service.PlatformGenerationView, bool, error)
	getCalls    int
	cancelCalls int
}

func (f *platformGenerationHandlerFake) Get(ctx context.Context, userID int64, generationID string) (service.PlatformGenerationView, error) {
	f.getCalls++
	if f.get == nil {
		return service.PlatformGenerationView{}, service.ErrPlatformGenerationControlUnavailable
	}
	return f.get(ctx, userID, generationID)
}

func (f *platformGenerationHandlerFake) Cancel(ctx context.Context, userID int64, generationID string) (service.PlatformGenerationView, bool, error) {
	f.cancelCalls++
	if f.cancel == nil {
		return service.PlatformGenerationView{}, false, service.ErrPlatformGenerationControlUnavailable
	}
	return f.cancel(ctx, userID, generationID)
}

func platformGenerationHandlerEngine(controller service.PlatformGenerationController) *gin.Engine {
	return platformGenerationHandlerEngineForUser(controller, 47)
}

func platformGenerationHandlerEngineForUser(controller service.PlatformGenerationController, userID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	state := &app.State{Settings: &config.Settings{}, PlatformGenerationControl: controller}
	registerPlatformWithAuthentication(engine, state, func(c *gin.Context) {
		c.Set(middleware.ContextUser, &models.User{ID: userID})
		c.Set(middleware.ContextUserID, userID)
		c.Next()
	})
	return engine
}

func platformGenerationRequest(t *testing.T, controller service.PlatformGenerationController, method, path string) *httptest.ResponseRecorder {
	return platformGenerationRequestForUser(t, controller, 47, method, path)
}

func platformGenerationRequestForUser(t *testing.T, controller service.PlatformGenerationController, userID int64, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Request-ID", "generation-request-1")
	rec := httptest.NewRecorder()
	platformGenerationHandlerEngineForUser(controller, userID).ServeHTTP(rec, req)
	return rec
}

func generationStringPointer(value string) *string { return &value }
func generationInt64Pointer(value int64) *int64    { return &value }

func TestPlatformGenerationGetProjectsEveryLifecycleState(t *testing.T) {
	single := "single"
	cases := []struct {
		name string
		view service.PlatformGenerationView
		want string
	}{
		{"running", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "running", Mode: &single}, `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"running","mode":"single","conversation_guid":null}`},
		{"cancelling", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "cancelling", Mode: &single}, `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"cancelling","mode":"single","conversation_guid":null}`},
		{"committing", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "committing", Mode: &single}, `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"committing","mode":"single","conversation_guid":null}`},
		{"cancelled", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "cancelled", Mode: &single}, `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"cancelled","mode":"single","conversation_guid":null}`},
		{"failed", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "failed", Mode: &single, Code: "internal_error"}, `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"failed","mode":"single","conversation_guid":null,"code":"internal_error"}`},
		{"pristine tombstone", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "cancelled"}, `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"cancelled","mode":null,"conversation_guid":null}`},
		{"completed single", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "completed", Mode: &single, ConversationGUID: generationStringPointer("8001"), Result: &service.PlatformGenerationResultView{Model: "model-a", Status: "completed", AssistantMessageGUID: "9001", Content: "answer", Tokens: generationInt64Pointer(12)}, TotalTokensUsed: generationInt64Pointer(120)}, `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"completed","mode":"single","conversation_guid":"8001","result":{"model":"model-a","status":"completed","assistant_message_guid":"9001","content":"answer","tokens":12},"total_tokens_used":120}`},
		{"completed compare", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "completed", Mode: generationStringPointer("compare"), ConversationGUID: generationStringPointer("8001"), Results: []service.PlatformGenerationResultView{{Model: "model-a", Status: "completed", AssistantMessageGUID: "9001", Content: "answer", Tokens: generationInt64Pointer(12)}, {Model: "model-b", Status: "failed", Code: "gateway_upstream_error"}}, TotalTokensUsed: generationInt64Pointer(120)}, `{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"completed","mode":"compare","conversation_guid":"8001","results":[{"model":"model-a","status":"completed","assistant_message_guid":"9001","content":"answer","tokens":12},{"model":"model-b","status":"failed","code":"gateway_upstream_error"}],"total_tokens_used":120}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &platformGenerationHandlerFake{get: func(_ context.Context, userID int64, generationID string) (service.PlatformGenerationView, error) {
				if userID != 47 || generationID != platformGenerationHandlerTestID {
					t.Fatalf("identity=%d/%q", userID, generationID)
				}
				return tc.view, nil
			}}
			rec := platformGenerationRequest(t, fake, http.MethodGet, "/api/v1/platform/chat/generations/"+platformGenerationHandlerTestID)
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != tc.want {
				t.Fatalf("status/body=%d/%q want 200/%q", rec.Code, rec.Body.String(), tc.want)
			}
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Retry-After") != "" {
				t.Fatalf("headers cache=%q retry=%q", rec.Header().Get("Cache-Control"), rec.Header().Get("Retry-After"))
			}
		})
	}
}

func TestPlatformGenerationCancelUsesTerminalAndPendingHTTPContracts(t *testing.T) {
	single := "single"
	cases := []struct {
		name    string
		view    service.PlatformGenerationView
		pending bool
		status  int
		retry   string
	}{
		{"cancelled", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "cancelled", Mode: &single}, false, http.StatusOK, ""},
		{"completed", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "completed", Mode: &single, ConversationGUID: generationStringPointer("8001"), Result: &service.PlatformGenerationResultView{Model: "model-a", Status: "completed", AssistantMessageGUID: "9001", Content: "answer", Tokens: generationInt64Pointer(1)}, TotalTokensUsed: generationInt64Pointer(9)}, false, http.StatusOK, ""},
		{"failed", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "failed", Mode: &single, Code: "internal_error"}, false, http.StatusOK, ""},
		{"cancelling", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "cancelling", Mode: &single}, true, http.StatusAccepted, "1"},
		{"committing", service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "committing", Mode: &single}, true, http.StatusAccepted, "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &platformGenerationHandlerFake{cancel: func(_ context.Context, userID int64, generationID string) (service.PlatformGenerationView, bool, error) {
				if userID != 47 || generationID != platformGenerationHandlerTestID {
					t.Fatalf("identity=%d/%q", userID, generationID)
				}
				return tc.view, tc.pending, nil
			}}
			rec := platformGenerationRequest(t, fake, http.MethodPost, "/api/v1/platform/chat/generations/"+platformGenerationHandlerTestID+"/cancel")
			if rec.Code != tc.status || rec.Header().Get("Retry-After") != tc.retry || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q retry=%q body=%s", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Retry-After"), rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `"status":"running"`) {
				t.Fatalf("cancel response exposed running: %s", rec.Body.String())
			}
		})
	}
}

func TestPlatformGenerationCancelRejectsImpossibleRunningProjection(t *testing.T) {
	for _, pending := range []bool{false, true} {
		fake := &platformGenerationHandlerFake{cancel: func(context.Context, int64, string) (service.PlatformGenerationView, bool, error) {
			return service.PlatformGenerationView{GenerationID: platformGenerationHandlerTestID, Status: "running", Mode: generationStringPointer("single")}, pending, nil
		}}
		rec := platformGenerationRequest(t, fake, http.MethodPost, "/api/v1/platform/chat/generations/"+platformGenerationHandlerTestID+"/cancel")
		assertPlatformGenerationError(t, rec, http.StatusServiceUnavailable, "generation_status_unavailable", "Generation status is temporarily unavailable.", "api_error")
		if strings.Contains(rec.Body.String(), "running") || rec.Header().Get("Retry-After") != "" {
			t.Fatalf("impossible running leaked: headers=%v body=%s", rec.Header(), rec.Body.String())
		}
	}
}

func TestPlatformGenerationValidatesCanonicalUUIDBeforeController(t *testing.T) {
	fake := &platformGenerationHandlerFake{}
	for _, methodPath := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/platform/chat/generations/not-a-uuid"},
		{http.MethodGet, "/api/v1/platform/chat/generations/550E8400-E29B-41D4-A716-446655440000"},
		{http.MethodPost, "/api/v1/platform/chat/generations/not-a-uuid/cancel"},
		{http.MethodPost, "/api/v1/platform/chat/generations/550E8400-E29B-41D4-A716-446655440000/cancel"},
	} {
		rec := platformGenerationRequest(t, fake, methodPath.method, methodPath.path)
		assertPlatformGenerationError(t, rec, http.StatusBadRequest, "invalid_request", "Invalid request.", "invalid_request_error")
	}
	if fake.getCalls != 0 || fake.cancelCalls != 0 {
		t.Fatalf("controller called before validation get=%d cancel=%d", fake.getCalls, fake.cancelCalls)
	}
}

func TestPlatformGenerationMapsOnlyStableSanitizedErrors(t *testing.T) {
	secret := errors.New("redis://private:password@db.internal generation raw json")
	cases := []struct {
		name       string
		controller service.PlatformGenerationController
		method     string
		path       string
		status     int
		code       string
		message    string
		typeName   string
	}{
		{"nil controller", nil, http.MethodGet, "/api/v1/platform/chat/generations/" + platformGenerationHandlerTestID, 503, "generation_status_unavailable", "Generation status is temporarily unavailable.", "api_error"},
		{"missing get", &platformGenerationHandlerFake{get: func(context.Context, int64, string) (service.PlatformGenerationView, error) {
			return service.PlatformGenerationView{}, service.ErrPlatformGenerationControlNotFound
		}}, http.MethodGet, "/api/v1/platform/chat/generations/" + platformGenerationHandlerTestID, 404, "generation_not_found", "Generation not found.", "invalid_request_error"},
		{"unavailable get", &platformGenerationHandlerFake{get: func(context.Context, int64, string) (service.PlatformGenerationView, error) {
			return service.PlatformGenerationView{}, secret
		}}, http.MethodGet, "/api/v1/platform/chat/generations/" + platformGenerationHandlerTestID, 503, "generation_status_unavailable", "Generation status is temporarily unavailable.", "api_error"},
		{"cancel not found fails closed", &platformGenerationHandlerFake{cancel: func(context.Context, int64, string) (service.PlatformGenerationView, bool, error) {
			return service.PlatformGenerationView{}, false, service.ErrPlatformGenerationControlNotFound
		}}, http.MethodPost, "/api/v1/platform/chat/generations/" + platformGenerationHandlerTestID + "/cancel", 503, "generation_status_unavailable", "Generation status is temporarily unavailable.", "api_error"},
		{"unavailable cancel", &platformGenerationHandlerFake{cancel: func(context.Context, int64, string) (service.PlatformGenerationView, bool, error) {
			return service.PlatformGenerationView{}, false, secret
		}}, http.MethodPost, "/api/v1/platform/chat/generations/" + platformGenerationHandlerTestID + "/cancel", 503, "generation_status_unavailable", "Generation status is temporarily unavailable.", "api_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := platformGenerationRequest(t, tc.controller, tc.method, tc.path)
			assertPlatformGenerationError(t, rec, tc.status, tc.code, tc.message, tc.typeName)
			if strings.Contains(rec.Body.String(), "redis") || strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "raw json") {
				t.Fatalf("dependency detail leaked: %s", rec.Body.String())
			}
		})
	}
}

func assertPlatformGenerationError(t *testing.T, rec *httptest.ResponseRecorder, status int, code, message, typeName string) {
	t.Helper()
	want := `{"error":{"code":"` + code + `","message":"` + message + `","type":"` + typeName + `","request_id":"generation-request-1"}}`
	if rec.Code != status || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("status/body=%d/%q want %d/%q", rec.Code, rec.Body.String(), status, want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("error headers cache=%q retry=%q", rec.Header().Get("Cache-Control"), rec.Header().Get("Retry-After"))
	}
}

func TestPlatformGenerationHandlerIntegrationReceiptAndOwnerIsolation(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" || strings.TrimSpace(os.Getenv("TEST_REDIS_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires TEST_DATABASE_URL and TEST_REDIS_URL")
	}
	state := ownedRealUserCreateHTTPState(t)
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Errorf("close generation integration state: %v", err)
		}
	})
	if err := migration.Verify(context.Background(), state.DB); err != nil {
		t.Fatalf("verify owned handler migration ledger: %v", err)
	}
	user := platformTestUser(t, state, "generation-owner", nil)
	user.DailyCallLimit = 100
	user.DailyCallsUsed = 2
	user.TotalTokensUsed = 41
	resetAt := time.Now().UTC().UnixMilli()
	user.DailyCallsResetAt = &resetAt
	if err := state.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	generationID := fmt.Sprintf("550e8400-e29b-41d4-a716-%012x", platformTestSnowflake.Next()&0xffffffffffff)
	nowMillis := time.Now().UTC().UnixMilli()
	claim, err := state.PlatformGenerations.Claim(context.Background(), service.PlatformGenerationClaimInput{
		UserID: user.ID, GenerationID: generationID, Mode: service.PlatformGenerationModeSingle, Models: []string{"model-a"}, NowMillis: nowMillis,
	})
	if err != nil || claim.Duplicate || claim.LeaseToken == "" {
		t.Fatalf("claim=%#v error=%v", claim, err)
	}
	if _, err := state.PlatformGenerations.MarkModelDone(context.Background(), user.ID, generationID, "model-a", 0, nowMillis+1); err != nil {
		t.Fatal(err)
	}
	if _, err := state.PlatformGenerations.BeginCommit(context.Background(), user.ID, generationID, nowMillis+2); err != nil {
		t.Fatal(err)
	}
	receipt, err := state.PlatformGenerationPersistence.Finalize(context.Background(), state.DB, service.PlatformGenerationPersistenceInput{
		UserID: user.ID, GenerationID: generationID, Mode: service.PlatformGenerationModeSingle,
		Models: []string{"model-a"}, UserMessage: "private prompt bytes", NowMillis: nowMillis + 3,
		Results: []service.PlatformGenerationPersistenceResult{{Model: "model-a", State: service.PlatformGenerationStateCompleted, Content: "real receipt answer", Tokens: 7, Seq: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const currentTotal = int64(909)
	if err := state.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("total_tokens_used", currentTotal).Error; err != nil {
		t.Fatal(err)
	}

	ownerPath := "/api/v1/platform/chat/generations/" + generationID
	owner := platformGenerationRequestForUser(t, state.PlatformGenerationControl, user.ID, http.MethodGet, ownerPath)
	if owner.Code != http.StatusOK || !strings.Contains(owner.Body.String(), `"status":"completed"`) ||
		!strings.Contains(owner.Body.String(), `"content":"real receipt answer"`) ||
		!strings.Contains(owner.Body.String(), `"assistant_message_guid":"`+receipt.Results[0].AssistantMessageGUID+`"`) ||
		!strings.Contains(owner.Body.String(), `"total_tokens_used":909`) || strings.Contains(owner.Body.String(), "private prompt bytes") {
		t.Fatalf("real owner HTTP status/body=%d/%s", owner.Code, owner.Body.String())
	}

	foreignUserID := user.ID + 1_000_000
	foreignGet := platformGenerationRequestForUser(t, state.PlatformGenerationControl, foreignUserID, http.MethodGet, ownerPath)
	assertPlatformGenerationError(t, foreignGet, http.StatusNotFound, "generation_not_found", "Generation not found.", "invalid_request_error")
	foreignCancel := platformGenerationRequestForUser(t, state.PlatformGenerationControl, foreignUserID, http.MethodPost, ownerPath+"/cancel")
	wantForeign := `{"generation_id":"` + generationID + `","status":"cancelled","mode":null,"conversation_guid":null}`
	if foreignCancel.Code != http.StatusOK || strings.TrimSpace(foreignCancel.Body.String()) != wantForeign {
		t.Fatalf("foreign cancel status/body=%d/%s", foreignCancel.Code, foreignCancel.Body.String())
	}
	ownerAfter := platformGenerationRequestForUser(t, state.PlatformGenerationControl, user.ID, http.MethodGet, ownerPath)
	if ownerAfter.Code != http.StatusOK || !strings.Contains(ownerAfter.Body.String(), `"content":"real receipt answer"`) {
		t.Fatalf("owner changed by foreign cancel status/body=%d/%s", ownerAfter.Code, ownerAfter.Body.String())
	}
}
