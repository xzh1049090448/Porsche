package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeChatNormalizesToolRoundTrip(t *testing.T) {
	body := []byte(`{"model":"model-a","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"not-json"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`)
	got, err := DecodeChat(body)
	if err != nil || got.Messages[0].ToolCalls[0].Arguments != "not-json" || got.MaxOutputTokens != nil {
		t.Fatalf("conversation=%#v err=%#v", got, err)
	}
}

func TestDecodeChatRejectsBrokenCallSequences(t *testing.T) {
	inputs := []string{
		`{"model":"m","messages":[{"role":"tool","tool_call_id":"missing","content":"x"}]}`,
		`{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"same","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"same","type":"function","function":{"name":"b","arguments":"{}"}}]}]}`,
		`{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"open","type":"function","function":{"name":"a","arguments":"{}"}}]},{"role":"user","content":"continue"}]}`,
	}
	for _, body := range inputs {
		if _, err := DecodeChat([]byte(body)); err == nil || err.Code != "invalid_request" {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestDecodeChatRejectsDuplicateOrUnknownSelectedTools(t *testing.T) {
	inputs := []string{
		`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"same"}},{"type":"function","function":{"name":"same"}}]}`,
		`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"known"}}],"tool_choice":{"type":"function","function":{"name":"missing"}}}`,
	}
	for _, body := range inputs {
		if _, err := DecodeChat([]byte(body)); err == nil || err.Code != "invalid_request" {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestDecodeChatClassifiesOversizedHistoricalToolPayloads(t *testing.T) {
	tests := []struct {
		name    string
		message map[string]any
	}{
		{
			name: "assistant tool arguments",
			message: map[string]any{
				"role": "assistant", "content": nil,
				"tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "lookup", "arguments": strings.Repeat("a", MaxArgumentsBytes+1)},
				}},
			},
		},
		{
			name:    "tool output",
			message: map[string]any{"role": "tool", "tool_call_id": "call_1", "content": strings.Repeat("o", MaxToolOutputBytes+1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"model": "m", "messages": []any{tt.message}})
			if err != nil {
				t.Fatal(err)
			}
			if len(body) >= MaxRequestBodyBytes {
				t.Fatalf("test body=%d must remain below body limit", len(body))
			}
			if _, got := DecodeChat(body); got == nil || got.Status != 413 || got.Code != "request_too_large" {
				t.Fatalf("error=%#v, want 413 request_too_large", got)
			}
		})
	}
}

func TestDecodeChatKeepsMalformedToolPayloadsAt400(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":1}}]}]}`),
		append([]byte(`{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"`), append([]byte{0xff}, []byte(`"}}]}]}`)...)...),
		[]byte(`{"model":"m","messages":[{"role":"tool","tool_call_id":"call_1","content":{"bad":"shape"}}]}`),
		append([]byte(`{"model":"m","messages":[{"role":"tool","tool_call_id":"call_1","content":"`), append([]byte{0xff}, []byte(`"}]}`)...)...),
	} {
		if _, got := DecodeChat(body); got == nil || got.Status != 400 || got.Code != "invalid_request" {
			t.Fatalf("error=%#v, want 400 invalid_request", got)
		}
	}
}

func TestDecodeChatRejectsInvalidAssistantContentWithToolCalls(t *testing.T) {
	for _, content := range []string{`1`, `{"bad":"shape"}`, `["bad"]`} {
		body := []byte(`{"model":"m","messages":[{"role":"assistant","content":` + content + `,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
		if _, got := DecodeChat(body); got == nil || got.Status != 400 || got.Code != "invalid_request" {
			t.Fatalf("content=%s error=%#v, want 400 invalid_request", content, got)
		}
	}
}
