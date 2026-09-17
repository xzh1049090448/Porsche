package dto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func TestConversationDetailSerializesExactPublicContract(t *testing.T) {
	conversationModel := "conversation-model"
	messageModel := "answer-model"
	assistantGUID := "8202"
	errorCode := "upstream_error"
	detail := &service.ConversationDetail{
		Conversation: &models.Conversation{
			ID: 990001,
			AuditFields: models.AuditFields{
				Guid:      8100,
				CreatedAt: 1_700_000_000_000,
				UpdatedAt: 1_700_000_001_000,
			},
			UserID: 990002,
			Title:  "compare history",
			Model:  &conversationModel,
			Messages: []models.Message{
				{
					ID:             990003,
					AuditFields:    models.AuditFields{Guid: 8201, CreatedAt: 1_700_000_000_100},
					ConversationID: 990001,
					Role:           models.MessageRoleUser,
					Content:        "prompt",
				},
				{
					ID:             990004,
					AuditFields:    models.AuditFields{Guid: 8202, CreatedAt: 1_700_000_000_200},
					ConversationID: 990001,
					Role:           models.MessageRoleAssistant,
					Content:        "answer",
					Model:          &messageModel,
					Tokens:         17,
				},
			},
		},
		GenerationGroups: []service.ConversationGenerationGroup{
			{
				GenerationID:    "11111111-1111-4111-8111-111111111111",
				Mode:            "compare",
				UserMessageGUID: "8201",
				Results: []service.ConversationGenerationResult{
					{Model: "model-a", Status: "completed", AssistantMessageGUID: &assistantGUID, Tokens: 17},
					{Model: "model-b", Status: "failed", ErrorCode: &errorCode},
				},
			},
		},
	}

	encoded, err := json.Marshal(ConversationDetail(detail))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"created_at":"2023-11-14T22:13:20Z","generation_groups":[{"generation_id":"11111111-1111-4111-8111-111111111111","mode":"compare","results":[{"assistant_message_guid":"8202","error_code":null,"model":"model-a","status":"completed","tokens":17},{"assistant_message_guid":null,"error_code":"upstream_error","model":"model-b","status":"failed","tokens":0}],"user_message_guid":"8201"}],"guid":"8100","messages":[{"content":"prompt","created_at":"2023-11-14T22:13:20.1Z","guid":"8201","model":null,"role":"user","tokens":0},{"content":"answer","created_at":"2023-11-14T22:13:20.2Z","guid":"8202","model":"answer-model","role":"assistant","tokens":17}],"model":"conversation-model","title":"compare history","updated_at":"2023-11-14T22:13:21Z"}`
	if string(encoded) != want {
		t.Fatalf("detail JSON mismatch\n got: %s\nwant: %s", encoded, want)
	}
	for _, forbidden := range []string{"990001", "990002", "990003", "990004", `"id"`, `"user_id"`, `"conversation_id"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("detail JSON exposed internal database data %q: %s", forbidden, encoded)
		}
	}
}

func TestConversationDetailAlwaysSerializesNonNilEmptyArrays(t *testing.T) {
	detail := &service.ConversationDetail{
		Conversation: &models.Conversation{
			AuditFields: models.AuditFields{Guid: 8100, CreatedAt: 1_700_000_000_000, UpdatedAt: 1_700_000_001_000},
			Title:       "empty",
		},
	}

	encoded, err := json.Marshal(ConversationDetail(detail))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"generation_groups":[]`) || !strings.Contains(string(encoded), `"messages":[]`) {
		t.Fatalf("detail JSON must keep empty arrays non-null: %s", encoded)
	}
}
