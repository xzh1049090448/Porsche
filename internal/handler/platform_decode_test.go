package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDecodePlatformRequestRejectsRemovedDatasetFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"dataset_enabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request

	var body platformChatBody
	if err := decodePlatformRequest(context, &body, false); err == nil {
		t.Fatal("decodePlatformRequest() accepted removed dataset_enabled field")
	}
}

func TestDecodePlatformRequestAcceptsConversationGUIDString(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"conversation_guid":"9001"}`))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request

	var body platformChatBody
	if err := decodePlatformRequest(context, &body, false); err != nil {
		t.Fatalf("decodePlatformRequest() error = %v", err)
	}
	if body.ConversationGUID == nil || *body.ConversationGUID != "9001" {
		t.Fatalf("conversation GUID was not preserved: %#v", body.ConversationGUID)
	}
}

func TestDecodePlatformRequestRejectsLegacyConversationID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"conversation_id":1}`))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request

	var body platformChatBody
	if err := decodePlatformRequest(context, &body, false); err == nil {
		t.Fatal("decodePlatformRequest() accepted legacy conversation_id")
	}
}
