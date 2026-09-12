package openaicompat

import "testing"

func TestValidateCallSequenceAllowsParallelResultsInAnyOrder(t *testing.T) {
	c := Conversation{Model: "m", Messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "a", Arguments: "{}"}, {ID: "call_2", Name: "b", Arguments: "{}"}}},
		{Role: RoleTool, ToolCallID: "call_2", Content: "two"},
		{Role: RoleTool, ToolCallID: "call_1", Content: "one"},
		{Role: RoleUser, Content: "continue"},
	}}
	if err := validateConversation(c); err != nil {
		t.Fatalf("validateConversation() error = %#v", err)
	}
}
