package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const platformV2GenerationID = "550e8400-e29b-41d4-a716-446655440000"

func decodePlatformV2Body(t *testing.T, payload string) (*platformChatBody, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/chat/completions", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	body := new(platformChatBody)
	if err := decodePlatformRequest(context, body, false); err != nil {
		return nil, err
	}
	return body, nil
}

func TestDecodePlatformRequestProjectsV2FieldsOutOfUpstreamPayload(t *testing.T) {
	body, err := decodePlatformV2Body(t, `{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"stream":true,"stream_version":"platform-chat-sse.v2","generation_id":"`+platformV2GenerationID+`"}`)
	if err != nil {
		t.Fatalf("decodePlatformRequest() error = %v", err)
	}
	if body.StreamVersion != "platform-chat-sse.v2" || body.GenerationID != platformV2GenerationID {
		t.Fatalf("v2 fields were not retained locally: %#v", body)
	}
	var upstream map[string]json.RawMessage
	if err := json.Unmarshal(body.WhiteLabelBody, &upstream); err != nil {
		t.Fatalf("unmarshal projected upstream body: %v", err)
	}
	for _, forbidden := range []string{"stream_version", "generation_id"} {
		if _, found := upstream[forbidden]; found {
			t.Fatalf("projected upstream body retained %q: %s", forbidden, body.WhiteLabelBody)
		}
	}
}

func TestDecodePlatformRequestRejectsIncompleteOrInvalidV2Contract(t *testing.T) {
	valid := `{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"stream":true,"stream_version":"platform-chat-sse.v2","generation_id":"` + platformV2GenerationID + `"}`
	for name, payload := range map[string]string{
		"stream false":       strings.Replace(valid, `"stream":true`, `"stream":false`, 1),
		"wrong version":      strings.Replace(valid, "platform-chat-sse.v2", "platform-chat-sse.v3", 1),
		"malformed UUID":     strings.Replace(valid, platformV2GenerationID, "not-a-uuid", 1),
		"uppercase UUID":     strings.Replace(valid, platformV2GenerationID, strings.ToUpper(platformV2GenerationID), 1),
		"missing generation": strings.Replace(valid, `,"generation_id":"`+platformV2GenerationID+`"`, "", 1),
		"version only":       strings.Replace(valid, `,"generation_id":"`+platformV2GenerationID+`"`, "", 1),
		"ID only":            strings.Replace(valid, `,"stream_version":"platform-chat-sse.v2"`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePlatformV2Body(t, payload); err == nil {
				t.Fatalf("decodePlatformRequest() accepted %s", name)
			}
		})
	}
}

func TestDecodePlatformRequestKeepsLegacyStreamContract(t *testing.T) {
	body, err := decodePlatformV2Body(t, `{"model":"model-a","messages":[{"role":"user","content":"hello"}],"max_tokens":1,"stream":true}`)
	if err != nil {
		t.Fatalf("decodePlatformRequest() changed legacy stream behavior: %v", err)
	}
	if body.StreamVersion != "" || body.GenerationID != "" {
		t.Fatalf("legacy request unexpectedly treated as v2: %#v", body)
	}
}
