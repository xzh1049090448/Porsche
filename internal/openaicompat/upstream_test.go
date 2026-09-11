package openaicompat

import (
	"bytes"
	"testing"
)

func TestEncodeUpstreamProjectsOnlyNormalizedFields(t *testing.T) {
	parallel := true
	c := Conversation{Model: "model-a", ParallelToolCalls: &parallel, Instructions: []Message{{Role: RoleDeveloper, Content: "rules"}}, Messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", Arguments: "{}"}}},
		{Role: RoleTool, ToolCallID: "call_1", Content: "secret-result"},
	}}
	body, err := EncodeUpstream(c)
	if err != nil || !bytes.Contains(body, []byte(`"role":"system"`)) || !bytes.Contains(body, []byte(`"tool_call_id":"call_1"`)) || !bytes.Contains(body, []byte(`"parallel_tool_calls":true`)) {
		t.Fatalf("body=%s err=%v", body, err)
	}
	for _, forbidden := range [][]byte{[]byte(`"store"`), []byte(`"previous_response_id"`), []byte("sk-gw-")} {
		if bytes.Contains(body, forbidden) {
			t.Fatalf("body contains %q: %s", forbidden, body)
		}
	}
}
