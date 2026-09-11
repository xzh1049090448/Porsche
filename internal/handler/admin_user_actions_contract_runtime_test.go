package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
	"gorm.io/gorm"
)

func TestA08RuntimeBearerRequestReloadsPermissionDeny(t *testing.T) {
	state := adminAuthzHTTPState(t)
	engine := gin.New()
	RegisterAdminUsersRead(engine, state)
	actor := adminAuthzHTTPUser(t, state, models.UserRoleAdmin)
	target := adminAuthzHTTPUser(t, state, models.UserRoleUser)
	access := platformJWT(t, state, actor)
	path := "/admin/v2/users/" + strconv.FormatInt(target.Guid, 10)
	if recorder := adminAuthzRequest(engine, path, access); recorder.Code != http.StatusOK {
		t.Fatalf("baseline admin request status=%d", recorder.Code)
	}
	head := models.PermissionPolicyHead{AuditFields: models.AuditFields{Guid: platformTestSnowflake.Next()}, UserID: actor.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 1}
	capability, ok := models.PermissionCapabilityCode("users.read")
	if !ok {
		t.Fatal("users.read missing from catalog")
	}
	rule := models.PermissionOverride{AuditFields: models.AuditFields{Guid: platformTestSnowflake.Next()}, UserID: actor.ID, PolicyVersion: 1, Capability: capability, Effect: 3}
	if err := state.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&head).Error; err != nil {
			return err
		}
		return tx.Create(&rule).Error
	}); err != nil {
		t.Fatal(err)
	}
	recorder := adminAuthzRequest(engine, path, access)
	adminAuthzAssertError(t, recorder, http.StatusForbidden, "无权限访问")
}

func readAdminActionContract(t *testing.T) map[string]any {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("contract source location unavailable")
	}
	path := filepath.Join(filepath.Dir(sourceFile), "..", "..", "docs", "agents", "contracts", "admin-action-future-contract.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read action contract failed")
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal("decode action contract failed")
	}
	return document
}

func actionContractAt(t *testing.T, root any, path ...string) any {
	t.Helper()
	value := root
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("contract path invalid at %q", key)
		}
		value, ok = object[key]
		if !ok {
			t.Fatalf("contract path missing %q", key)
		}
	}
	return value
}

func newContractBoundActionEngine(backend userDeleteActionBackend) *gin.Engine {
	engine := gin.New()
	group := engine.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, func(c *gin.Context) {
		c.Set(middleware.ContextUser, &models.User{ID: 7, AuditFields: models.AuditFields{Guid: 77}, Role: models.UserRoleAdmin, AuthVersion: 3})
		c.Set(middleware.ContextUserID, int64(7))
		c.Set(middleware.ContextSessionSID, "contract-session")
		c.Next()
	})
	registerAdminUserActionRoutes(group, backend, &config.Settings{})
	return engine
}

func contractRequestBody(t *testing.T, endpoint map[string]any) string {
	t.Helper()
	contents, err := json.Marshal(endpoint["request_example"])
	if err != nil {
		t.Fatal("encode contract request failed")
	}
	return string(contents)
}

func assertContractRuntimeResponse(t *testing.T, recorder *httptest.ResponseRecorder, endpoint map[string]any) {
	t.Helper()
	wantStatus, ok := endpoint["response_status"].(float64)
	if !ok || recorder.Code != int(wantStatus) {
		t.Fatalf("contract runtime status mismatch got=%d", recorder.Code)
	}
	for name, ruleValue := range endpoint["response_headers"].(map[string]any) {
		rule, ok := ruleValue.(string)
		if !ok {
			t.Fatalf("invalid response header rule header=%q", name)
		}
		got := recorder.Header().Get(name)
		switch rule {
		case "no-store":
			if got != rule {
				t.Fatalf("response header mismatch header=%q", name)
			}
		case "required_non_empty":
			if got == "" {
				t.Fatalf("response header missing header=%q", name)
			}
		case "processing_only_integer_seconds_1_to_30":
			if got != "7" {
				t.Fatalf("processing retry header mismatch header=%q", name)
			}
		default:
			t.Fatalf("unsupported response header rule header=%q", name)
		}
	}
}

func assertContractJSONBody(t *testing.T, recorder *httptest.ResponseRecorder, want any) {
	t.Helper()
	var got any
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("runtime body decode failed body_length=%d", recorder.Body.Len())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime body differs from contract body_length=%d", recorder.Body.Len())
	}
}

func TestUserDeleteActionRoutesRuntimeMatchFrozenContract(t *testing.T) {
	document := readAdminActionContract(t)
	endpoints := actionContractAt(t, document, "endpoints").(map[string]any)

	issue := endpoints["issue"].(map[string]any)
	issueExample := issue["response_example"].(map[string]any)
	backend := &scriptedUserDeleteBackend{issued: &service.IssuedVerification{
		Ticket: issueExample["ticket"].(string), ExpiresAt: int64(issueExample["expires_at"].(float64)),
	}}
	engine := newContractBoundActionEngine(backend)
	issueRecorder := performActionRequest(engine, issue["method"].(string), issue["path"].(string), contractRequestBody(t, issue), nil)
	assertContractRuntimeResponse(t, issueRecorder, issue)
	assertContractJSONBody(t, issueRecorder, issueExample)

	execute := endpoints["execute"].(map[string]any)
	executeExample := execute["response_example"].(map[string]any)
	finishedAt := int64(1790000000000)
	operationRef := executeExample["operation_ref"].(string)
	backend = &scriptedUserDeleteBackend{
		identity: &service.OperationIdentity{PublicRef: operationRef}, ready: true,
		beginView:   &service.OperationView{PublicRef: operationRef, Scope: "users.delete", Status: "processing", RetryAfterSeconds: 30},
		executeView: &service.OperationView{PublicRef: operationRef, Scope: "users.delete", Status: "succeeded", FinishedAt: &finishedAt},
	}
	engine = newContractBoundActionEngine(backend)
	executePath := bytes.ReplaceAll([]byte(execute["path"].(string)), []byte(":guid"), []byte(executeExample["user"].(map[string]any)["guid"].(string)))
	executeRecorder := performActionRequest(engine, execute["method"].(string), string(executePath), contractRequestBody(t, execute), http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}})
	assertContractRuntimeResponse(t, executeRecorder, execute)
	assertContractJSONBody(t, executeRecorder, executeExample)

	query := endpoints["query"].(map[string]any)
	queryExample := query["response_examples"].(map[string]any)["processing"].(map[string]any)
	backend = &scriptedUserDeleteBackend{queryView: &service.OperationView{PublicRef: queryExample["operation_ref"].(string), Scope: queryExample["scope"].(string), Status: queryExample["status"].(string), RetryAfterSeconds: 7}}
	engine = newContractBoundActionEngine(backend)
	queryRecorder := performActionRequest(engine, query["method"].(string), query["path"].(string), "", http.Header{"Idempotency-Key": {testActionKey}})
	assertContractRuntimeResponse(t, queryRecorder, query)
	assertContractJSONBody(t, queryRecorder, queryExample)
}

func TestUserDeleteActionUnauthenticatedRuntimeMatches401Contract(t *testing.T) {
	document := readAdminActionContract(t)
	statuses := actionContractAt(t, document, "error_contract", "http_statuses").([]any)
	var contract401 map[string]any
	for _, candidate := range statuses {
		row := candidate.(map[string]any)
		if row["status"] == float64(http.StatusUnauthorized) {
			contract401 = row
			break
		}
	}
	if contract401 == nil || len(contract401["codes"].([]any)) != 0 || contract401["envelope"] != "legacy_detail" || contract401["category"] != "existing_authentication_failure" {
		t.Fatal("401 contract is not the legacy detail middleware shape")
	}

	engine := gin.New()
	group := engine.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, middleware.RequireUser(&app.State{}))
	registerAdminUserActionRoutes(group, &scriptedUserDeleteBackend{}, &config.Settings{})
	issue := actionContractAt(t, document, "endpoints", "issue").(map[string]any)
	recorder := performActionRequest(engine, issue["method"].(string), issue["path"].(string), contractRequestBody(t, issue), nil)
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Retry-After") != "" {
		t.Fatalf("unauthenticated runtime status mismatch got=%d", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unauthenticated body decode failed body_length=%d", recorder.Body.Len())
	}
	bodyShape := contract401["body_shape"].(map[string]any)
	if len(body) != 1 || len(bodyShape["required_fields"].([]any)) != 1 || bodyShape["required_fields"].([]any)[0] != "detail" || bodyShape["additional_fields"] != false {
		t.Fatal("unauthenticated runtime body shape differs from contract")
	}
	detail, ok := body["detail"].(string)
	if !ok {
		t.Fatal("unauthenticated runtime detail missing")
	}
	allowed := false
	for _, candidate := range contract401["details"].([]any) {
		allowed = allowed || candidate == detail
	}
	if !allowed {
		t.Fatal("unauthenticated runtime detail differs from contract")
	}
}
