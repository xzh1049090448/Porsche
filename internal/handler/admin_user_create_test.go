package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const adminUserCreatePassword = "Ex4mple!Pass1"

type scriptedUserManagementBackend struct {
	issued            *service.IssuedVerification
	issueErr          error
	identity          *service.OperationIdentity
	beginView         *service.OperationView
	beginErr          error
	ready             bool
	executeView       *service.OperationView
	executeErr        error
	queryView         *service.OperationView
	queryErr          error
	outcomeResponse   *service.PersistedActionResponse
	outcomeStatus     int
	outcomeErr        error
	issueCalls        int
	beginCalls        int
	newCreateCalls    int
	hashCalls         int
	executeCalls      int
	queryCalls        int
	outcomeCalls      int
	issueAction       actionsecurity.Action
	issueIntent       actionsecurity.CreateAccountIntent
	issueInitial      []byte
	issueCurrent      []byte
	issueInitialHash  [sha256.Size]byte
	issueCurrentHash  [sha256.Size]byte
	beginAction       actionsecurity.Action
	beginIntent       actionsecurity.CreateAccountIntent
	beginPassword     []byte
	beginPasswordHash [sha256.Size]byte
	hashPassword      []byte
	hashPasswordHash  [sha256.Size]byte
	hashErr           error
	beginKeys         []string
	beginTickets      []string
	createAction      actionsecurity.Action
	createIntent      actionsecurity.CreateAccountIntent
	createHash        []byte
	createHashValid   bool
	createMetadata    service.CreateAccountRequestMetadata
	queryAction       actionsecurity.Action
	queryKeys         []string
	outcomeAction     actionsecurity.Action
	outcomeActor      service.ActionActor
	outcomeRef        string
}

type concurrentUserCreateHashBackend struct {
	freshIdentity  *service.OperationIdentity
	replayIdentity *service.OperationIdentity
	bothBegun      chan struct{}
	beginCalls     atomic.Int32
	hashCalls      atomic.Int32
	newCalls       atomic.Int32
	executeCalls   atomic.Int32
}

func (backend *concurrentUserCreateHashBackend) Issue(context.Context, service.VerificationIssue) (*service.IssuedVerification, error) {
	return nil, service.ErrActionOperationUnavailable
}
func (backend *concurrentUserCreateHashBackend) Begin(_ context.Context, begin service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error) {
	if intent, ok := begin.Intent.(actionsecurity.CreateAccountIntent); ok {
		clear(intent.Password)
	}
	finished := int64(1_790_000_000_000)
	switch backend.beginCalls.Add(1) {
	case 1:
		<-backend.bothBegun
		return backend.freshIdentity, &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "processing", RetryAfterSeconds: 30}, nil
	case 2:
		close(backend.bothBegun)
		return backend.replayIdentity, &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "succeeded", FinishedAt: &finished}, nil
	default:
		return nil, nil, service.ErrActionOperationUnavailable
	}
}
func (backend *concurrentUserCreateHashBackend) ExecutionReady(identity *service.OperationIdentity) bool {
	return identity == backend.freshIdentity
}
func (backend *concurrentUserCreateHashBackend) HashCreatePassword(password []byte) ([]byte, error) {
	backend.hashCalls.Add(1)
	return service.HashManagedCreationPasswordBytes(password)
}
func (backend *concurrentUserCreateHashBackend) NewDeleteExecution(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
	return nil, service.ErrActionOperationUnavailable
}
func (backend *concurrentUserCreateHashBackend) ExecuteDelete(context.Context, *service.OperationIdentity, *service.DeleteUserExecution) (*service.OperationView, error) {
	return nil, service.ErrActionOperationUnavailable
}
func (backend *concurrentUserCreateHashBackend) NewCreateExecution(actionsecurity.Action, actionsecurity.CreateAccountIntent, []byte, service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error) {
	backend.newCalls.Add(1)
	return &service.CreateAccountExecution{}, nil
}
func (backend *concurrentUserCreateHashBackend) ExecuteCreate(context.Context, *service.OperationIdentity, *service.CreateAccountExecution) (*service.OperationView, error) {
	backend.executeCalls.Add(1)
	finished := int64(1_790_000_000_000)
	return &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "succeeded", FinishedAt: &finished}, nil
}
func (backend *concurrentUserCreateHashBackend) CreateOutcome(context.Context, actionsecurity.Action, service.ActionActor, string) (*service.PersistedActionResponse, int, error) {
	return adminUserCreateResult("user"), http.StatusCreated, nil
}
func (backend *concurrentUserCreateHashBackend) Query(context.Context, actionsecurity.Action, service.ActionActor, []string) (*service.OperationView, error) {
	return nil, service.ErrActionOperationUnavailable
}

func (s *scriptedUserManagementBackend) Issue(_ context.Context, issue service.VerificationIssue) (*service.IssuedVerification, error) {
	s.issueCalls++
	s.issueAction = issue.Action
	s.issueIntent, _ = issue.Intent.(actionsecurity.CreateAccountIntent)
	s.issueInitial = s.issueIntent.Password
	s.issueCurrent = issue.CurrentPassword
	s.issueInitialHash = sha256.Sum256(issueIntentPassword(s.issueIntent))
	s.issueCurrentHash = sha256.Sum256(issue.CurrentPassword)
	return s.issued, s.issueErr
}

func (s *scriptedUserManagementBackend) Begin(_ context.Context, begin service.OperationBegin) (*service.OperationIdentity, *service.OperationView, error) {
	s.beginCalls++
	s.beginAction = begin.Action
	s.beginIntent, _ = begin.Intent.(actionsecurity.CreateAccountIntent)
	s.beginPassword = s.beginIntent.Password
	s.beginPasswordHash = sha256.Sum256(s.beginIntent.Password)
	s.beginKeys = append([]string(nil), begin.IdempotencyKeyValues...)
	s.beginTickets = append([]string(nil), begin.TicketValues...)
	// Operation.Begin owns the descriptor password and may clear it while
	// encoding. The HTTP adapter must retain a separate owned hashing copy.
	clear(s.beginIntent.Password)
	return s.identity, s.beginView, s.beginErr
}

func (s *scriptedUserManagementBackend) ExecutionReady(*service.OperationIdentity) bool {
	return s.ready
}

func (s *scriptedUserManagementBackend) HashCreatePassword(password []byte) ([]byte, error) {
	s.hashCalls++
	s.hashPassword = password
	s.hashPasswordHash = sha256.Sum256(password)
	if s.hashErr != nil {
		return nil, s.hashErr
	}
	return service.HashManagedCreationPasswordBytes(password)
}

func (s *scriptedUserManagementBackend) NewDeleteExecution(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
	return &service.DeleteUserExecution{}, nil
}

func (s *scriptedUserManagementBackend) ExecuteDelete(context.Context, *service.OperationIdentity, *service.DeleteUserExecution) (*service.OperationView, error) {
	return nil, service.ErrActionOperationUnavailable
}

func (s *scriptedUserManagementBackend) NewCreateExecution(action actionsecurity.Action, intent actionsecurity.CreateAccountIntent, hash []byte, metadata service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error) {
	s.newCreateCalls++
	s.createAction = action
	s.createIntent = intent
	s.createHash = hash
	s.createHashValid = security.VerifyPassword(adminUserCreatePassword, string(hash))
	s.createMetadata = metadata
	return &service.CreateAccountExecution{}, nil
}

func (s *scriptedUserManagementBackend) ExecuteCreate(_ context.Context, _ *service.OperationIdentity, _ *service.CreateAccountExecution) (*service.OperationView, error) {
	s.executeCalls++
	return s.executeView, s.executeErr
}

func (s *scriptedUserManagementBackend) CreateOutcome(_ context.Context, action actionsecurity.Action, actor service.ActionActor, ref string) (*service.PersistedActionResponse, int, error) {
	s.outcomeCalls++
	s.outcomeAction, s.outcomeActor, s.outcomeRef = action, actor, ref
	return s.outcomeResponse, s.outcomeStatus, s.outcomeErr
}

func (s *scriptedUserManagementBackend) Query(_ context.Context, action actionsecurity.Action, _ service.ActionActor, keys []string) (*service.OperationView, error) {
	s.queryCalls++
	s.queryAction = action
	s.queryKeys = append([]string(nil), keys...)
	return s.queryView, s.queryErr
}

func newScriptedUserManagementEngine(t *testing.T, backend userManagementActionBackend, role models.UserRole) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/admin/v2", gatewayRequestID(), adminUserActionNoStore, func(c *gin.Context) {
		c.Set("user", &models.User{ID: 17, AuditFields: models.AuditFields{Guid: 1701}, Role: role, AuthVersion: 9})
		c.Set("session_sid", "11111111-2222-4333-8444-555555555555")
		c.Set("authenticated_session_version", 4)
		c.Next()
	})
	registerAdminUserManagementActionRoutes(g, backend, &config.Settings{TrustProxyHeaders: true, TrustedProxyCIDRs: "192.0.2.0/24"})
	return r
}

func adminUserCreateResult(role string) *service.PersistedActionResponse {
	permissionsVersion := "null"
	if role == "admin" {
		permissionsVersion = `"1"`
	}
	body := `{"operation_ref":"` + testOperationRef + `","user":{"guid":"123456789012345678","username":"alice","nickname":"Alice","email":null,"group":"default","plan_type":"free","role":"` + role + `","status":"active","auth_version":1,"created_at":"2026-09-06T00:00:00Z","last_login_at":null},"permissions_version":` + permissionsVersion + `}`
	return &service.PersistedActionResponse{HTTPStatus: http.StatusCreated, MediaType: "application/json", Body: []byte(body)}
}

func adminUserCreateBackend(role string) *scriptedUserManagementBackend {
	finished := int64(1790000000000)
	return &scriptedUserManagementBackend{
		identity: &service.OperationIdentity{PublicRef: testOperationRef}, ready: true,
		beginView:       &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "processing", RetryAfterSeconds: 30},
		executeView:     &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "succeeded", FinishedAt: &finished},
		outcomeResponse: adminUserCreateResult(role), outcomeStatus: http.StatusCreated,
	}
}

func TestAdminUserCreateOrdinaryUsesTicketlessActionHashAndExactDTO(t *testing.T) {
	backend := adminUserCreateBackend("user")
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
	body := `{"username":"alice","nickname":"Alice","password":"` + adminUserCreatePassword + `","role":"user","group_guid":null,"plan_type":"free","permission_overrides":[]}`
	rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users", body, http.Header{
		"Idempotency-Key": {testActionKey}, "X-Request-ID": {"request-create-1"}, "X-Forwarded-For": {"203.0.113.8"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body_length=%d", rec.Code, rec.Body.Len())
	}
	want := `{"operation_ref":"` + testOperationRef + `","user":{"guid":"123456789012345678","username":"alice","nickname":"Alice","email":null,"group":"default","plan_type":"free","role":"user","status":"active","auth_version":1,"created_at":"2026-09-06T00:00:00Z","last_login_at":null},"permissions_version":null}`
	assertActionTestExactBody(t, rec, want)
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") != "request-create-1" {
		t.Fatalf("security headers cache=%q request_id=%q", rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
	}
	passwordDigest := sha256.Sum256([]byte(adminUserCreatePassword))
	if backend.beginCalls != 1 || backend.beginAction != actionsecurity.ActionUsersCreate || len(backend.beginTickets) != 0 || backend.hashCalls != 1 ||
		backend.beginPasswordHash != passwordDigest || backend.hashPasswordHash != passwordDigest || backend.newCreateCalls != 1 || backend.createAction != actionsecurity.ActionUsersCreate ||
		backend.createIntent.Password != nil || backend.createIntent.Role != "user" || backend.createIntent.PlanType != int(models.PlanFree) ||
		len(backend.createIntent.AllowedModels) != 0 || backend.createIntent.DailyCallLimit != 100 || len(backend.createIntent.Overrides) != 0 ||
		!backend.createHashValid || backend.createMetadata.RequestID != "request-create-1" ||
		backend.createMetadata.TrustedIP != "203.0.113.8" || backend.executeCalls != 1 || backend.outcomeCalls != 1 {
		t.Fatal("ordinary create did not preserve the reviewed action, defaults, hash, metadata, and execution flow")
	}
	for _, secret := range [][]byte{backend.beginPassword, backend.hashPassword, backend.createHash} {
		for _, value := range secret {
			if value != 0 {
				t.Fatal("create handler retained an owned password buffer")
			}
		}
	}
}

func TestAdminUserCreateRealHTTPPasswordSurvivesBeginEncoding(t *testing.T) {
	state := realUserCreateHTTPState(t)
	actorUsername := fmt.Sprintf("u%019d", platformTestSnowflake.Next())
	actor, err := state.Auth.RegisterUsername(context.Background(), actorUsername, "Actor!Strong9", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.DB.Model(&models.User{}).Where("id = ?", actor.ID).Update("role", models.UserRoleAdmin).Error; err != nil {
		t.Fatal(err)
	}
	actor.Role = models.UserRoleAdmin
	access := platformJWT(t, state, actor)

	engine := gin.New()
	RegisterAuth(engine, state)
	RegisterAdminUserManagementActions(engine, state)
	username := fmt.Sprintf("u%019d", platformTestSnowflake.Next())
	body := `{"username":"` + username + `","password":"` + adminUserCreatePassword + `","role":"user","group_guid":null,"plan_type":"free","permission_overrides":[]}`
	create := performActionRequest(engine, http.MethodPost, "/admin/v2/users", body, http.Header{
		"Authorization":   {"Bearer " + access},
		"Content-Type":    {"application/json"},
		"Idempotency-Key": {testActionKey},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("real create status=%d body_length=%d", create.Code, create.Body.Len())
	}
	authHTTPLogin(t, engine, username, adminUserCreatePassword)
	nulLogin := serveAuthRequest(engine, authJSONRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"`+username+`","password":"`+adminUserCreatePassword+`\u0000"}`))
	if nulLogin.Code != http.StatusUnauthorized {
		t.Fatalf("NUL-suffixed password status=%d, want 401", nulLogin.Code)
	}
}

func realUserCreateHTTPState(t *testing.T) *app.State {
	t.Helper()
	settings := platformTestSettings(t)
	settings.RegisterEnabled = true
	settings.PasswordRegisterEnabled = true
	settings.PasswordLoginEnabled = true
	settings.ActionSecurityHMACKey = bytes.Repeat([]byte{0x5a}, 32)
	gdb, err := db.Open(settings.DatabaseURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	preparePlatformAuthSchema(t, gdb)
	state, err := app.NewState(settings, gdb)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = state.AuthRedis.Close()
		_ = sqlDB.Close()
	})
	return state
}

func ownedRealUserCreateHTTPState(t *testing.T) *app.State {
	t.Helper()
	parent, err := db.Open(testDatabaseURL(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	parentSQL, err := parent.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parentSQL.Close() })
	name := fmt.Sprintf("porsche_handler_%d_test", platformTestSnowflake.Next())
	if err := parent.Exec("CREATE DATABASE `" + name + "`").Error; err != nil {
		t.Fatalf("create owned handler database: %v", err)
	}
	t.Cleanup(func() {
		if err := parent.Exec("DROP DATABASE `" + name + "`").Error; err != nil {
			t.Errorf("drop owned handler database: %v", err)
		}
	})
	childURL, err := url.Parse(testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	childURL.Path, childURL.RawPath = "/"+name, ""
	child, err := db.Open(childURL.String(), "test")
	if err != nil {
		t.Fatal(err)
	}
	childSQL, err := child.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = childSQL.Close() })
	settings := platformTestSettings(t)
	settings.DatabaseURL = childURL.String()
	settings.RegisterEnabled = true
	settings.PasswordRegisterEnabled = true
	settings.PasswordLoginEnabled = true
	settings.ActionSecurityHMACKey = bytes.Repeat([]byte{0x5a}, 32)
	preparePlatformAuthSchema(t, child)
	state, err := app.NewState(settings, child)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.AuthRedis.Close() })
	return state
}

func TestAdminUserCreateRealHTTPDeletedPre0010SnapshotReplaysStableGone(t *testing.T) {
	state := ownedRealUserCreateHTTPState(t)
	actorUsername := fmt.Sprintf("u%019d", platformTestSnowflake.Next())
	actorPassword := "Actor!Strong9"
	actor, err := state.Auth.RegisterUsername(context.Background(), actorUsername, actorPassword, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.DB.Model(&models.User{}).Where("id = ?", actor.ID).Update("role", models.UserRoleRoot).Error; err != nil {
		t.Fatal(err)
	}
	actor.Role = models.UserRoleRoot
	access := platformJWT(t, state, actor)
	engine := gin.New()
	RegisterAuth(engine, state)
	RegisterAdminUserManagementActions(engine, state)

	username := fmt.Sprintf("u%019d", platformTestSnowflake.Next())
	nickname := "Pre 0010 private nickname"
	createKey := testActionKey
	createBody := `{"username":"` + username + `","nickname":"` + nickname + `","password":"` + adminUserCreatePassword + `","role":"user","group_guid":null,"plan_type":"free","permission_overrides":[]}`
	createHeaders := http.Header{"Authorization": {"Bearer " + access}, "Content-Type": {"application/json"}, "Idempotency-Key": {createKey}}
	created := performActionRequest(engine, http.MethodPost, "/admin/v2/users", createBody, createHeaders)
	if created.Code != http.StatusCreated {
		t.Fatalf("real pre-0010 create status=%d body_length=%d", created.Code, created.Body.Len())
	}
	var createdResponse struct {
		OperationRef string `json:"operation_ref"`
		User         struct {
			GUID        string  `json:"guid"`
			Username    string  `json:"username"`
			Nickname    *string `json:"nickname"`
			Email       *string `json:"email"`
			Group       string  `json:"group"`
			PlanType    string  `json:"plan_type"`
			Role        string  `json:"role"`
			Status      string  `json:"status"`
			AuthVersion int     `json:"auth_version"`
			CreatedAt   string  `json:"created_at"`
			LastLoginAt *string `json:"last_login_at"`
		} `json:"user"`
		PermissionsVersion *string `json:"permissions_version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(created.Body.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&createdResponse); err != nil {
		t.Fatalf("decode real create response: %v", err)
	}
	verificationBody := fmt.Sprintf(`{"action":"users.delete","intent":{"target_guid":"%s","expected_auth_version":%d,"reason":"privacy lifecycle"},"current_password":"%s"}`, createdResponse.User.GUID, createdResponse.User.AuthVersion, actorPassword)
	issued := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", verificationBody, http.Header{"Authorization": {"Bearer " + access}, "Content-Type": {"application/json"}})
	if issued.Code != http.StatusCreated {
		t.Fatalf("real pre-0010 delete verification status=%d body_length=%d", issued.Code, issued.Body.Len())
	}
	issuedResponse := decodeActionTestResponse[struct {
		Ticket    string `json:"ticket"`
		ExpiresAt int64  `json:"expires_at"`
	}](t, issued)
	deleted := performActionRequest(engine, http.MethodPost, "/admin/v2/users/"+createdResponse.User.GUID+"/actions",
		fmt.Sprintf(`{"action":"delete","expected_auth_version":%d,"reason":"privacy lifecycle"}`, createdResponse.User.AuthVersion),
		http.Header{"Authorization": {"Bearer " + access}, "Content-Type": {"application/json"}, "Idempotency-Key": {testActionKey}, "X-Action-Ticket": {issuedResponse.Ticket}})
	if deleted.Code != http.StatusOK {
		t.Fatalf("real pre-0010 delete status=%d body_length=%d", deleted.Code, deleted.Body.Len())
	}

	migrations, err := migration.All()
	if err != nil || len(migrations) != 10 || migrations[9].Version != "0010" {
		t.Fatalf("load 0010 for HTTP lifecycle = %d/%v", len(migrations), err)
	}
	for index, statement := range strings.Split(string(migrations[9].DownSQL), ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			if err := state.DB.Exec(statement).Error; err != nil {
				t.Fatalf("isolated HTTP 0010 down statement %d: %v", index+1, err)
			}
		}
	}
	if err := state.DB.Exec("DELETE FROM schema_migrations WHERE version = '0010'").Error; err != nil {
		t.Fatal(err)
	}
	if err := migration.Up(context.Background(), state.DB, func() int64 { return platformTestSnowflake.Next() }, func() int64 { return 1_910_053_040_000 }); err != nil {
		t.Fatalf("isolated HTTP 0010 up: %v", err)
	}
	replay := performActionRequest(engine, http.MethodPost, "/admin/v2/users", createBody, createHeaders)
	if replay.Code != http.StatusGone || bytes.Contains(replay.Body.Bytes(), []byte(username)) || bytes.Contains(replay.Body.Bytes(), []byte(nickname)) {
		t.Fatalf("deleted pre-0010 HTTP replay status=%d body_length=%d", replay.Code, replay.Body.Len())
	}
	replayError := decodeActionTestResponse[actionTestErrorEnvelope](t, replay)
	if replayError.Error.Code != "created_user_deleted" || replayError.Error.OperationRef != createdResponse.OperationRef {
		t.Fatalf("deleted pre-0010 HTTP replay code/ref=%q/%q", replayError.Error.Code, replayError.Error.OperationRef)
	}
}

func TestAdminUserCreateAdminVerificationThenCreateUsesExactActionAndSeparateSecrets(t *testing.T) {
	backend := adminUserCreateBackend("admin")
	backend.issued = &service.IssuedVerification{Ticket: testActionTicket, ExpiresAt: 1790000300000}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
	verificationBody := `{"action":"users.create_admin","intent":{"username":"alice","nickname":"Alice","password":"` + adminUserCreatePassword + `","role":"admin","group_guid":null,"plan_type":"free","permission_overrides":[]},"current_password":"Current!Pass9"}`
	issue := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", verificationBody, http.Header{"X-Request-ID": {"request-verify-1"}})
	if issue.Code != http.StatusCreated {
		t.Fatalf("issue status=%d body_length=%d", issue.Code, issue.Body.Len())
	}
	assertActionTestExactBody(t, issue, `{"ticket":"`+testActionTicket+`","expires_at":1790000300000}`)
	if backend.issueCalls != 1 || backend.issueAction != actionsecurity.ActionUsersCreateAdmin || backend.issueIntent.Role != "admin" ||
		backend.issueInitialHash != sha256.Sum256([]byte(adminUserCreatePassword)) ||
		backend.issueCurrentHash != sha256.Sum256([]byte("Current!Pass9")) ||
		(len(backend.issueInitial) > 0 && &backend.issueInitial[0] == &backend.issueCurrent[0]) {
		t.Fatal("administrator verification did not keep separate exact credential ownership")
	}
	for _, secret := range [][]byte{backend.issueInitial, backend.issueCurrent} {
		for _, value := range secret {
			if value != 0 {
				t.Fatal("administrator verification retained an owned password buffer")
			}
		}
	}

	backend.beginView.Scope = "users.create_admin"
	backend.executeView.Scope = "users.create_admin"
	createBody := `{"username":"alice","nickname":"Alice","password":"` + adminUserCreatePassword + `","role":"admin","group_guid":null,"plan_type":"free","permission_overrides":[]}`
	created := performActionRequest(engine, http.MethodPost, "/admin/v2/users", createBody, http.Header{
		"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}, "X-Request-ID": {"request-create-admin"},
	})
	if created.Code != http.StatusCreated || backend.beginAction != actionsecurity.ActionUsersCreateAdmin || len(backend.beginTickets) != 1 || backend.beginTickets[0] != testActionTicket {
		t.Fatalf("admin create status/action/tickets=%d/%d/%v", created.Code, backend.beginAction, backend.beginTickets)
	}
	want := `{"operation_ref":"` + testOperationRef + `","user":{"guid":"123456789012345678","username":"alice","nickname":"Alice","email":null,"group":"default","plan_type":"free","role":"admin","status":"active","auth_version":1,"created_at":"2026-09-06T00:00:00Z","last_login_at":null},"permissions_version":"1"}`
	assertActionTestExactBody(t, created, want)
}

func TestAdminUserCreateVerificationDispatchesDeleteAndRejectsEveryOtherAction(t *testing.T) {
	backend := adminUserCreateBackend("user")
	backend.issued = &service.IssuedVerification{Ticket: testActionTicket, ExpiresAt: 1790000300000}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
	deleted := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications",
		`{"action":"users.delete","intent":{"target_guid":"123","expected_auth_version":7,"reason":"duplicate"},"current_password":"Current!Pass9"}`, nil)
	if deleted.Code != http.StatusCreated || backend.issueCalls != 1 || backend.issueAction != actionsecurity.ActionUsersDelete {
		t.Fatalf("delete dispatch status/calls/action=%d/%d/%d", deleted.Code, backend.issueCalls, backend.issueAction)
	}
	for _, action := range []string{"users.create", "users.promote"} {
		before := backend.issueCalls
		body := `{"action":"` + action + `","intent":{},"current_password":"Current!Pass9"}`
		rec := performActionRequest(engine, http.MethodPost, "/admin/v2/action-verifications", body, nil)
		if rec.Code != http.StatusUnprocessableEntity || backend.issueCalls != before {
			t.Fatalf("action=%q status/calls=%d/%d", action, rec.Code, backend.issueCalls)
		}
		response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
		if response.Error.Code != "action_inactive" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
			t.Fatalf("action=%q error/header=%q/%q/%q", action, response.Error.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
		}
	}
}

func TestAdminUserCreateRejectsForbiddenHeaderAndRoleCombinationsBeforeBegin(t *testing.T) {
	validUser := `{"username":"alice","password":"` + adminUserCreatePassword + `","role":"user"}`
	validAdmin := `{"username":"alice","password":"` + adminUserCreatePassword + `","role":"admin"}`
	tests := []struct {
		name    string
		path    string
		body    string
		headers http.Header
	}{
		{"ordinary ticket", "/admin/v2/users", validUser, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket}}},
		{"ordinary empty ticket", "/admin/v2/users", validUser, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {""}}},
		{"admin missing ticket", "/admin/v2/users", validAdmin, http.Header{"Idempotency-Key": {testActionKey}}},
		{"admin duplicate ticket", "/admin/v2/users", validAdmin, http.Header{"Idempotency-Key": {testActionKey}, "X-Action-Ticket": {testActionTicket, testActionTicket}}},
		{"missing key", "/admin/v2/users", validUser, nil},
		{"duplicate key", "/admin/v2/users", validUser, http.Header{"Idempotency-Key": {testActionKey, testActionKey}}},
		{"create query", "/admin/v2/users?scope=users.create", validUser, http.Header{"Idempotency-Key": {testActionKey}}},
		{"root role", "/admin/v2/users", `{"username":"alice","password":"` + adminUserCreatePassword + `","role":"root"}`, http.Header{"Idempotency-Key": {testActionKey}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := adminUserCreateBackend("user")
			engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
			rec := performActionRequest(engine, http.MethodPost, test.path, test.body, test.headers)
			if rec.Code != http.StatusBadRequest || backend.beginCalls != 0 || backend.hashCalls != 0 || backend.newCreateCalls != 0 {
				t.Fatalf("status=%d begin/hash/new=%d/%d/%d body_length=%d", rec.Code, backend.beginCalls, backend.hashCalls, backend.newCreateCalls, rec.Body.Len())
			}
			response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
			if response.Error.Code != "invalid_admin_user_create_request" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
				t.Fatalf("error/header contract = %q/%q/%q", response.Error.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
			}
		})
	}
}

func TestAdminUserCreateHashFailureClearsBothOwnedPasswordBuffers(t *testing.T) {
	backend := adminUserCreateBackend("user")
	backend.hashErr = errors.New("injected hash failure")
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
	rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users", `{"username":"alice","password":"`+adminUserCreatePassword+`","role":"user"}`, http.Header{"Idempotency-Key": {testActionKey}})
	if rec.Code != http.StatusBadRequest || backend.beginCalls != 1 || backend.hashCalls != 1 || backend.newCreateCalls != 0 || backend.executeCalls != 0 {
		t.Fatalf("status/begin/hash/new/execute=%d/%d/%d/%d/%d", rec.Code, backend.beginCalls, backend.hashCalls, backend.newCreateCalls, backend.executeCalls)
	}
	assertClearedCreatePassword(t, backend.beginPassword)
	assertClearedCreatePassword(t, backend.hashPassword)
}

func TestAdminUserCreateReplayAndA03FailureCodesDoNotReexecute(t *testing.T) {
	finished := int64(1790000000000)
	tests := []struct {
		name            string
		view            *service.OperationView
		outcomeStatus   int
		outcomeResponse *service.PersistedActionResponse
		outcomeErr      error
		wantStatus      int
		wantCode        string
	}{
		{"replay", &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "succeeded", FinishedAt: &finished}, 201, adminUserCreateResult("user"), nil, 201, ""},
		{"deleted replay", &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "succeeded", FinishedAt: &finished}, 410, nil, service.ErrCreatedAccountDeleted, 410, "created_user_deleted"},
		{"username conflict", &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "failed", FinishedAt: &finished, FailureCode: stringPointer("consumer_validation_failed")}, 409, nil, nil, 409, "username_conflict"},
		{"group hidden", &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "failed", FinishedAt: &finished, FailureCode: stringPointer("consumer_validation_failed")}, 404, nil, nil, 404, "action_group_not_found"},
		{"capability rejected", &service.OperationView{PublicRef: testOperationRef, Scope: "users.create", Status: "failed", FinishedAt: &finished, FailureCode: stringPointer("action_rejected")}, 403, nil, nil, 403, "action_operation_rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := adminUserCreateBackend("user")
			backend.ready = false
			backend.beginView = test.view
			backend.outcomeStatus, backend.outcomeResponse, backend.outcomeErr = test.outcomeStatus, test.outcomeResponse, test.outcomeErr
			engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
			rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users", `{"username":"alice","password":"`+adminUserCreatePassword+`","role":"user"}`, http.Header{"Idempotency-Key": {testActionKey}})
			if rec.Code != test.wantStatus || backend.hashCalls != 0 || backend.newCreateCalls != 0 || backend.executeCalls != 0 || backend.outcomeCalls != 1 {
				t.Fatalf("status/new/execute/outcome=%d/%d/%d/%d", rec.Code, backend.newCreateCalls, backend.executeCalls, backend.outcomeCalls)
			}
			assertClearedCreatePassword(t, backend.beginPassword)
			if test.wantStatus == http.StatusCreated {
				if !bytes.Equal(rec.Body.Bytes(), backend.outcomeResponse.Body) || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
					t.Fatalf("replay body/header drift body=%q cache=%q request_id=%q", rec.Body.Bytes(), rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
				}
			}
			if test.wantCode != "" {
				response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
				if response.Error.Code != test.wantCode {
					t.Fatalf("code=%q want=%q", response.Error.Code, test.wantCode)
				}
				if test.name == "deleted replay" && (response.Error.OperationRef != testOperationRef || strings.Contains(rec.Body.String(), "alice")) {
					t.Fatalf("deleted replay leaked identity or lost operation reference: %q", rec.Body.String())
				}
			}
		})
	}
}

func TestAdminUserCreateRoleCapabilityAndDependencyOutcomesKeepSafeHeaders(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		actor      models.UserRole
		err        error
		wantStatus int
		wantCode   string
		wantRetry  string
	}{
		{"user actor denied ordinary", "user", models.UserRoleUser, service.ErrActionOperationForbidden, 403, "action_operation_rejected", ""},
		{"admin denied administrator", "admin", models.UserRoleAdmin, service.ErrActionOperationForbidden, 403, "action_operation_rejected", ""},
		{"users create capability denied", "user", models.UserRoleAdmin, service.ErrActionOperationForbidden, 403, "action_operation_rejected", ""},
		{"expired create replay", "user", models.UserRoleRoot, service.ErrActionOperationExpired, 410, "operation_expired", ""},
		{"inactive", "user", models.UserRoleRoot, service.ErrActionOperationInactive, 422, "action_inactive", ""},
		{"rate limited", "user", models.UserRoleRoot, &service.RetryAfterError{Seconds: 9}, 429, "action_rate_limited", "9"},
		{"dependency unavailable", "user", models.UserRoleRoot, service.ErrActionOperationUnavailable, 503, "action_dependency_unavailable", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := adminUserCreateBackend(test.role)
			backend.beginErr = test.err
			engine := newScriptedUserManagementEngine(t, backend, test.actor)
			headers := http.Header{"Idempotency-Key": {testActionKey}, "X-Request-ID": {"request-outcome"}}
			if test.role == "admin" {
				headers.Set("X-Action-Ticket", testActionTicket)
			}
			rec := performActionRequest(engine, http.MethodPost, "/admin/v2/users", `{"username":"alice","password":"`+adminUserCreatePassword+`","role":"`+test.role+`"}`, headers)
			if rec.Code != test.wantStatus || backend.beginCalls != 1 || backend.hashCalls != 0 || backend.newCreateCalls != 0 || rec.Header().Get("Retry-After") != test.wantRetry {
				t.Fatalf("status/begin/new/retry=%d/%d/%d/%q", rec.Code, backend.beginCalls, backend.newCreateCalls, rec.Header().Get("Retry-After"))
			}
			assertClearedCreatePassword(t, backend.beginPassword)
			response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
			if response.Error.Code != test.wantCode || response.Error.RequestID != "request-outcome" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") != "request-outcome" {
				t.Fatalf("code/request/cache/header=%q/%q/%q/%q", response.Error.Code, response.Error.RequestID, rec.Header().Get("Cache-Control"), rec.Header().Get("X-Request-ID"))
			}
		})
	}
}

func assertClearedCreatePassword(t *testing.T, password []byte) {
	t.Helper()
	for _, value := range password {
		if value != 0 {
			t.Fatal("create handler retained a Begin password buffer")
		}
	}
}

func TestAdminUserCreateConcurrentSameOperationHashesOnlyFreshAttempt(t *testing.T) {
	backend := &concurrentUserCreateHashBackend{
		freshIdentity: &service.OperationIdentity{PublicRef: testOperationRef}, replayIdentity: &service.OperationIdentity{PublicRef: testOperationRef},
		bothBegun: make(chan struct{}),
	}
	engine := newScriptedUserManagementEngine(t, backend, models.UserRoleAdmin)
	var wait sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			recorder := performActionRequest(engine, http.MethodPost, "/admin/v2/users", `{"username":"alice","password":"`+adminUserCreatePassword+`","role":"user"}`, http.Header{"Idempotency-Key": {testActionKey}})
			statuses <- recorder.Code
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusCreated {
			t.Fatalf("concurrent status = %d", status)
		}
	}
	if backend.beginCalls.Load() != 2 || backend.hashCalls.Load() != 1 || backend.newCalls.Load() != 1 || backend.executeCalls.Load() != 1 {
		t.Fatalf("begin/hash/new/execute = %d/%d/%d/%d", backend.beginCalls.Load(), backend.hashCalls.Load(), backend.newCalls.Load(), backend.executeCalls.Load())
	}
}

func TestAdminUserCreateQueryDispatchesOnlyExactCreateScopes(t *testing.T) {
	for _, test := range []struct {
		scope  string
		action actionsecurity.Action
	}{{"users.create", actionsecurity.ActionUsersCreate}, {"users.create_admin", actionsecurity.ActionUsersCreateAdmin}} {
		backend := adminUserCreateBackend("user")
		backend.queryView = &service.OperationView{PublicRef: testOperationRef, Scope: test.scope, Status: "processing", RetryAfterSeconds: 7}
		engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
		rec := performActionRequest(engine, http.MethodGet, "/admin/v2/operations?scope="+test.scope, "", http.Header{"Idempotency-Key": {testActionKey}})
		if rec.Code != http.StatusOK || backend.queryCalls != 1 || backend.queryAction != test.action || len(backend.queryKeys) != 1 || backend.queryKeys[0] != testActionKey || rec.Header().Get("Retry-After") != "7" {
			t.Fatalf("scope=%q status/calls/action/keys/retry=%d/%d/%d/%v/%q", test.scope, rec.Code, backend.queryCalls, backend.queryAction, backend.queryKeys, rec.Header().Get("Retry-After"))
		}
	}
	for _, path := range []string{
		"/admin/v2/operations?scope=users.create&scope=users.create", "/admin/v2/operations?scope=users.create%5fadmin",
		"/admin/v2/operations?scope=users.create_admin&x=1", "/admin/v2/operations?scope=users.promote",
	} {
		backend := adminUserCreateBackend("user")
		engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
		rec := performActionRequest(engine, http.MethodGet, path, "", http.Header{"Idempotency-Key": {testActionKey}})
		if rec.Code != http.StatusBadRequest || backend.queryCalls != 0 {
			t.Fatalf("path=%q status=%d calls=%d", path, rec.Code, backend.queryCalls)
		}
	}
}

func TestAdminUserCreateExpiredBeginAndQueryStayDistinctFromDeletedReplay(t *testing.T) {
	for _, request := range []struct {
		name   string
		method string
		path   string
		body   string
		setup  func(*scriptedUserManagementBackend)
	}{
		{name: "begin", method: http.MethodPost, path: "/admin/v2/users", body: `{"username":"alice","password":"` + adminUserCreatePassword + `","role":"user"}`, setup: func(backend *scriptedUserManagementBackend) { backend.beginErr = service.ErrActionOperationExpired }},
		{name: "query", method: http.MethodGet, path: "/admin/v2/operations?scope=users.create", setup: func(backend *scriptedUserManagementBackend) { backend.queryErr = service.ErrActionOperationExpired }},
	} {
		t.Run(request.name, func(t *testing.T) {
			backend := adminUserCreateBackend("user")
			request.setup(backend)
			engine := newScriptedUserManagementEngine(t, backend, models.UserRoleRoot)
			rec := performActionRequest(engine, request.method, request.path, request.body, http.Header{"Idempotency-Key": {testActionKey}})
			if rec.Code != http.StatusGone {
				t.Fatalf("expired %s status=%d", request.name, rec.Code)
			}
			response := decodeActionTestResponse[actionTestErrorEnvelope](t, rec)
			if response.Error.Code != "operation_expired" || response.Error.OperationRef != "" || strings.Contains(rec.Body.String(), "alice") {
				t.Fatalf("expired %s response leaked or changed category: %q", request.name, rec.Body.String())
			}
			if backend.outcomeCalls != 0 {
				t.Fatalf("expired %s loaded a create snapshot", request.name)
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

func issueIntentPassword(intent actionsecurity.CreateAccountIntent) []byte { return intent.Password }
