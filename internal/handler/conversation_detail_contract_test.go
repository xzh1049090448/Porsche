package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type conversationDetailContextKey struct{}

type conversationDetailHandlerFixture struct {
	db                *gorm.DB
	user              *models.User
	title             string
	queryError        error
	omitGroup         bool
	contextSeen       bool
	conversationReads int
	receiptReads      int
	resultReads       int
	updateCalls       int
	createCalls       int
}

func newConversationDetailHandlerFixture(t *testing.T) *conversationDetailHandlerFixture {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "test:test@tcp(127.0.0.1:1)/conversation_detail_handler_test",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	fixture := &conversationDetailHandlerFixture{
		db:    db,
		user:  &models.User{ID: 42},
		title: "original title",
	}
	if err := db.Callback().Query().Replace("gorm:query", fixture.query); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Replace("gorm:create", fixture.create); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Update().Replace("gorm:update", fixture.update); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *conversationDetailHandlerFixture) query(tx *gorm.DB) {
	if tx.Statement.Context.Value(conversationDetailContextKey{}) == "request-context" {
		fixture.contextSeen = true
	}
	switch destination := tx.Statement.Dest.(type) {
	case *models.Conversation:
		fixture.conversationReads++
		if fixture.queryError != nil {
			tx.AddError(fixture.queryError)
			return
		}
		*destination = fixture.conversation()
		tx.RowsAffected = 1
	case *[]models.Conversation:
		*destination = []models.Conversation{fixture.conversation()}
		tx.RowsAffected = 1
	case *int64:
		*destination = 1
		tx.RowsAffected = 1
	case *[]*models.Message:
		messages := fixture.messages()
		*destination = []*models.Message{&messages[0], &messages[1]}
		tx.RowsAffected = 2
	case *[]models.Message:
		*destination = fixture.messages()
		tx.RowsAffected = 2
	case *[]models.PlatformChatGenerationReceipt:
		fixture.receiptReads++
		*destination = []models.PlatformChatGenerationReceipt{fixture.receipt()}
		tx.RowsAffected = 1
	case *[]models.PlatformChatGenerationResult:
		fixture.resultReads++
		*destination = fixture.results()
		tx.RowsAffected = 2
	default:
		tx.AddError(errors.New("unexpected conversation detail query destination"))
	}
}

func (fixture *conversationDetailHandlerFixture) create(tx *gorm.DB) {
	if tx.Statement.Table == "messages" {
		tx.RowsAffected = 2
		return
	}
	conversation, ok := tx.Statement.Dest.(*models.Conversation)
	if !ok {
		tx.AddError(errors.New("unexpected create destination"))
		return
	}
	fixture.createCalls++
	conversation.ID = 7100
	conversation.Guid = 8101
	conversation.CreatedAt = 1_700_000_000_000
	conversation.UpdatedAt = 1_700_000_000_000
	tx.RowsAffected = 1
}

func (fixture *conversationDetailHandlerFixture) update(tx *gorm.DB) {
	conversation, ok := tx.Statement.Dest.(*models.Conversation)
	if !ok {
		tx.AddError(errors.New("unexpected update destination"))
		return
	}
	fixture.updateCalls++
	fixture.title = conversation.Title
	tx.RowsAffected = 1
}

func (fixture *conversationDetailHandlerFixture) conversation() models.Conversation {
	model := "conversation-model"
	return models.Conversation{
		ID:          7001,
		AuditFields: models.AuditFields{Guid: 8001, CreatedAt: 1_700_000_000_000, UpdatedAt: 1_700_000_001_000},
		UserID:      fixture.user.ID,
		Title:       fixture.title,
		Model:       &model,
	}
}

func (fixture *conversationDetailHandlerFixture) messages() []models.Message {
	model := "private-model-a"
	return []models.Message{
		{
			ID: 7201, AuditFields: models.AuditFields{Guid: 8201, CreatedAt: 1_700_000_000_100},
			ConversationID: 7001, Role: models.MessageRoleUser, Content: "secret prompt",
		},
		{
			ID: 7202, AuditFields: models.AuditFields{Guid: 8202, CreatedAt: 1_700_000_000_200},
			ConversationID: 7001, Role: models.MessageRoleAssistant, Content: "secret answer", Model: &model, Tokens: 9,
		},
	}
}

func (fixture *conversationDetailHandlerFixture) receipt() models.PlatformChatGenerationReceipt {
	actor := fixture.user.ID
	conversationID := int64(7001)
	if fixture.omitGroup {
		conversationID = 7999
	}
	return models.PlatformChatGenerationReceipt{
		ID: 7301,
		AuditFields: models.AuditFields{
			Guid: 8301, CreatedAt: 1_700_000_000_300, UpdatedAt: 1_700_000_000_300,
			CreatedBy: &actor, UpdatedBy: &actor,
		},
		UserID: fixture.user.ID, GenerationID: "11111111-1111-4111-8111-111111111111",
		Mode: models.PlatformGenerationReceiptModeCompare, RequestedExistingConversation: 1,
		ConversationID: conversationID, UserMessageID: 7201, SuccessfulModelCount: 1,
		DailyCallsCharged: 1, TotalTokens: 9, CommittedAt: 1_700_000_000_300,
	}
}

func (fixture *conversationDetailHandlerFixture) results() []models.PlatformChatGenerationResult {
	actor := fixture.user.ID
	assistantID := int64(7202)
	errorCode := "upstream_error"
	audit := func(guid int64) models.AuditFields {
		return models.AuditFields{
			Guid: guid, CreatedAt: 1_700_000_000_400, UpdatedAt: 1_700_000_000_400,
			CreatedBy: &actor, UpdatedBy: &actor,
		}
	}
	return []models.PlatformChatGenerationResult{
		{ID: 7401, AuditFields: audit(8401), ReceiptID: 7301, ModelIndex: 0, Model: "private-model-a", Status: models.PlatformGenerationResultCompleted, AssistantMessageID: &assistantID, Tokens: 9},
		{ID: 7402, AuditFields: audit(8402), ReceiptID: 7301, ModelIndex: 1, Model: "private-model-b", Status: models.PlatformGenerationResultFailed, ErrorCode: &errorCode},
	}
}

func (fixture *conversationDetailHandlerFixture) router() *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/api/v1/conversations", func(c *gin.Context) {
		c.Set(middleware.ContextUser, fixture.user)
		c.Set(middleware.ContextUserID, fixture.user.ID)
		c.Header("X-Request-ID", "request-detail-safe")
		c.Next()
	})
	registerConversationRoutes(group, &app.State{DB: fixture.db})
	return engine
}

func performConversationRequest(engine *gin.Engine, method, path, body string, ctx context.Context) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	engine.ServeHTTP(recorder, request)
	return recorder
}

func decodeConversationResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response status=%d body=%q: %v", recorder.Code, recorder.Body.String(), err)
	}
	return response
}

func assertConversationGroups(t *testing.T, response map[string]any, want int) {
	t.Helper()
	groups, ok := response["generation_groups"].([]any)
	if !ok || len(groups) != want {
		t.Fatalf("generation_groups=%#v, want array length %d", response["generation_groups"], want)
	}
}

func TestConversationDetailGETAndPUTReturnGroupsAndReloadUpdatedTitle(t *testing.T) {
	fixture := newConversationDetailHandlerFixture(t)
	engine := fixture.router()
	requestContext := context.WithValue(context.Background(), conversationDetailContextKey{}, "request-context")

	get := performConversationRequest(engine, http.MethodGet, "/api/v1/conversations/8001", "", requestContext)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	assertConversationGroups(t, decodeConversationResponse(t, get), 1)
	if !fixture.contextSeen {
		t.Fatal("GET detail did not pass the request context to the service query")
	}

	readsBeforePut := fixture.conversationReads
	put := performConversationRequest(engine, http.MethodPut, "/api/v1/conversations/8001", `{"title":"renamed title"}`, context.Background())
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
	}
	response := decodeConversationResponse(t, put)
	assertConversationGroups(t, response, 1)
	if response["title"] != "renamed title" || fixture.updateCalls != 1 {
		t.Fatalf("PUT title/update=%#v/%d", response["title"], fixture.updateCalls)
	}
	if fixture.conversationReads-readsBeforePut != 2 {
		t.Fatalf("PUT conversation reads=%d, want update lookup plus detail reload", fixture.conversationReads-readsBeforePut)
	}

	updatesBeforeEmpty := fixture.updateCalls
	emptyPut := performConversationRequest(engine, http.MethodPut, "/api/v1/conversations/8001", `{}`, context.Background())
	if emptyPut.Code != http.StatusOK {
		t.Fatalf("empty PUT status=%d body=%s", emptyPut.Code, emptyPut.Body.String())
	}
	assertConversationGroups(t, decodeConversationResponse(t, emptyPut), 1)
	if fixture.updateCalls != updatesBeforeEmpty {
		t.Fatalf("empty PUT unexpectedly updated title calls=%d", fixture.updateCalls)
	}
}

func TestConversationDetailPUTEmptyTitleReloadsWithoutUpdate(t *testing.T) {
	fixture := newConversationDetailHandlerFixture(t)
	recorder := performConversationRequest(
		fixture.router(),
		http.MethodPut,
		"/api/v1/conversations/8001",
		`{"title":""}`,
		context.Background(),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty-title PUT status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeConversationResponse(t, recorder)
	assertConversationGroups(t, response, 1)
	if response["title"] != "original title" {
		t.Fatalf("empty-title PUT title=%#v, want unchanged original title", response["title"])
	}
	if fixture.updateCalls != 0 {
		t.Fatalf("empty-title PUT update calls=%d, want 0", fixture.updateCalls)
	}
	if fixture.conversationReads != 1 || fixture.receiptReads != 1 || fixture.resultReads != 1 {
		t.Fatalf(
			"empty-title PUT detail reads conversation/receipt/result=%d/%d/%d, want 1/1/1",
			fixture.conversationReads,
			fixture.receiptReads,
			fixture.resultReads,
		)
	}
}

func TestConversationDetailRoutesPreserveErrorAndAuthenticationStatus(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantDetail string
	}{
		{name: "owner not found", err: gorm.ErrRecordNotFound, wantStatus: http.StatusNotFound, wantDetail: "对话不存在"},
		{name: "storage unavailable", err: errors.New("database-password-secret"), wantStatus: http.StatusServiceUnavailable, wantDetail: "会话详情不可用"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConversationDetailHandlerFixture(t)
			fixture.queryError = test.err
			recorder := performConversationRequest(fixture.router(), http.MethodGet, "/api/v1/conversations/8001", "", context.Background())
			if recorder.Code != test.wantStatus || !strings.Contains(recorder.Body.String(), test.wantDetail) || strings.Contains(recorder.Body.String(), "database-password-secret") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterConversations(engine, &app.State{})
	recorder := performConversationRequest(engine, http.MethodGet, "/api/v1/conversations/8001", "", context.Background())
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestConversationListCreateAndExportKeepLegacyShapes(t *testing.T) {
	fixture := newConversationDetailHandlerFixture(t)
	engine := fixture.router()

	list := performConversationRequest(engine, http.MethodGet, "/api/v1/conversations", "", context.Background())
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	listResponse := decodeConversationResponse(t, list)
	items, ok := listResponse["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("list items=%#v", listResponse["items"])
	}
	listItem := items[0].(map[string]any)
	if _, exists := listItem["generation_groups"]; exists {
		t.Fatalf("list exposed detail groups: %#v", listItem)
	}
	if _, exists := listItem["messages"]; exists {
		t.Fatalf("list exposed messages: %#v", listItem)
	}

	create := performConversationRequest(engine, http.MethodPost, "/api/v1/conversations", `{"title":"created","model":"model-c"}`, context.Background())
	if create.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	createResponse := decodeConversationResponse(t, create)
	if createResponse["title"] != "created" || createResponse["model"] != "model-c" || fixture.createCalls != 1 {
		t.Fatalf("create response/calls=%#v/%d", createResponse, fixture.createCalls)
	}
	if _, exists := createResponse["generation_groups"]; exists {
		t.Fatalf("create exposed detail groups: %#v", createResponse)
	}

	export := performConversationRequest(engine, http.MethodGet, "/api/v1/conversations/8001/export/markdown", "", context.Background())
	if export.Code != http.StatusOK || export.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("export status/content-type=%d/%q body=%s", export.Code, export.Header().Get("Content-Type"), export.Body.String())
	}
	if export.Body.String() != "# original title\n\n## 用户\nsecret prompt\n\n## 助手\nsecret answer\n" {
		t.Fatalf("export markdown changed: %q", export.Body.String())
	}
	if fixture.receiptReads != 0 || fixture.resultReads != 0 {
		t.Fatalf("legacy routes loaded detail groups receipts/results=%d/%d", fixture.receiptReads, fixture.resultReads)
	}
}

func TestConversationDetailDiagnosticOnlyLogsSafeFieldsForOmittedGroups(t *testing.T) {
	fixture := newConversationDetailHandlerFixture(t)
	fixture.omitGroup = true
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	recorder := performConversationRequest(fixture.router(), http.MethodGet, "/api/v1/conversations/8001", "", context.Background())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertConversationGroups(t, decodeConversationResponse(t, recorder), 0)
	logged := logs.String()
	for _, required := range []string{"request-detail-safe", "8001", "omitted_count=1"} {
		if !strings.Contains(logged, required) {
			t.Fatalf("diagnostic omitted safe field %q: %q", required, logged)
		}
	}
	for _, forbidden := range []string{
		"7001", "7201", "7202", "7301", "7401", "7402",
		"secret prompt", "secret answer", "private-model-a", "private-model-b",
		"11111111-1111-4111-8111-111111111111",
	} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("diagnostic leaked forbidden value %q: %q", forbidden, logged)
		}
	}

	logs.Reset()
	fixture.omitGroup = false
	recorder = performConversationRequest(fixture.router(), http.MethodGet, "/api/v1/conversations/8001", "", context.Background())
	if recorder.Code != http.StatusOK || logs.Len() != 0 {
		t.Fatalf("valid detail unexpectedly logged status=%d log=%q", recorder.Code, logs.String())
	}
}
