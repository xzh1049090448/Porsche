package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func deepSeekHarnessChatBody() []byte {
	return []byte(`{
		"model":"deepseek/deepseek-v4-pro",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"","reasoning_content":"chain-of-thought","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"result"}
		],
		"reasoning_effort":"high",
		"thinking":{"type":"enabled"},
		"stream":true,
		"stream_options":{"include_usage":true},
		"max_tokens":10,
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`)
}

func capableReasoningPolicy(model string) ReasoningPolicy {
	return ReasoningPolicy{Models: map[string]struct{}{model: {}}}
}

func TestDecodeChatDeepSeekHarnessReasoningRoundTrip(t *testing.T) {
	policy := capableReasoningPolicy("deepseek/deepseek-v4-pro")
	conversation, err := DecodeChat(deepSeekHarnessChatBody(), policy)
	if err != nil {
		t.Fatalf("DecodeChat() error = %#v", err)
	}
	if conversation.ReasoningEffort != "high" || conversation.Thinking == nil || *conversation.Thinking != ThinkingEnabled {
		t.Fatalf("canonical reasoning = %q / %#v", conversation.ReasoningEffort, conversation.Thinking)
	}
	assistant := conversation.Messages[1]
	if assistant.Role != RoleAssistant || assistant.ReasoningContent == nil || *assistant.ReasoningContent != "chain-of-thought" {
		t.Fatalf("assistant message = %#v", assistant)
	}

	encoded, encodeErr := EncodeUpstream(conversation)
	if encodeErr != nil {
		t.Fatalf("EncodeUpstream() error = %v", encodeErr)
	}
	var upstream struct {
		ReasoningEffort string `json:"reasoning_effort"`
		Thinking        *struct {
			Type string `json:"type"`
		} `json:"thinking"`
		Messages []struct {
			Role             string  `json:"role"`
			ReasoningContent *string `json:"reasoning_content"`
		} `json:"messages"`
	}
	if json.Unmarshal(encoded, &upstream) != nil {
		t.Fatalf("upstream body is not JSON: %s", encoded)
	}
	if upstream.ReasoningEffort != "high" || upstream.Thinking == nil || upstream.Thinking.Type != "enabled" {
		t.Fatalf("upstream reasoning = %q / %#v: %s", upstream.ReasoningEffort, upstream.Thinking, encoded)
	}
	if len(upstream.Messages) != 3 || upstream.Messages[1].ReasoningContent == nil || *upstream.Messages[1].ReasoningContent != "chain-of-thought" {
		t.Fatalf("upstream messages = %#v", upstream.Messages)
	}
	if upstream.Messages[0].ReasoningContent != nil || upstream.Messages[2].ReasoningContent != nil {
		t.Fatalf("reasoning_content leaked to non-assistant messages: %s", encoded)
	}
}

func TestDecodeChatRejectsReasoningForUncapableModel(t *testing.T) {
	for name, policy := range map[string]ReasoningPolicy{
		"empty":       NoReasoning,
		"other model": capableReasoningPolicy("some/other-model"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeChat(deepSeekHarnessChatBody(), policy); err == nil || err.Code != "unsupported_parameter" || err.Status != 400 {
				t.Fatalf("error = %#v, want 400 unsupported_parameter", err)
			}
		})
	}
}

func TestDecodeChatClassificationForReasoningFields(t *testing.T) {
	policy := capableReasoningPolicy("m")
	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{name: "out of enum effort", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"reasoning_effort":"extreme"}`, code: "invalid_request"},
		{name: "non string effort", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"reasoning_effort":5}`, code: "invalid_request"},
		{name: "empty thinking", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"thinking":{}}`, code: "unsupported_parameter"},
		{name: "unknown thinking value", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"thinking":{"type":"nope"}}`, code: "unsupported_parameter"},
		{name: "unknown thinking field", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"thinking":{"type":"enabled","x":1}}`, code: "unsupported_parameter"},
		{name: "thinking wrong type", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"thinking":"enabled"}`, code: "unsupported_parameter"},
		{name: "user reasoning content", body: `{"model":"m","messages":[{"role":"user","content":"x","reasoning_content":"cot"}]}`, code: "invalid_request"},
		{name: "tool reasoning content", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"reasoning_effort":"low"}`, code: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeChat([]byte(tc.body), policy)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("expected success, got %#v", err)
				}
				return
			}
			if err == nil || err.Code != tc.code {
				t.Fatalf("error = %#v, want %s", err, tc.code)
			}
		})
	}
}

func TestDecodeChatRejectsOversizeOrNonUtf8ReasoningContent(t *testing.T) {
	policy := capableReasoningPolicy("m")
	oversize := strings.Repeat("x", MaxTextContentBytes+1)
	body, marshalErr := json.Marshal(map[string]any{
		"model": "m",
		"messages": []any{
			map[string]any{"role": "assistant", "content": "hi", "reasoning_content": oversize},
		},
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := DecodeChat(body, policy); err == nil || err.Code != "invalid_request" {
		t.Fatalf("oversize reasoning_content error = %#v, want invalid_request", err)
	}
}

func TestDecodeResponsesReasoningEffortMapping(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","reasoning":{"effort":"low"}}`)
	conversation, err := DecodeResponses(body, capableReasoningPolicy("m"))
	if err != nil {
		t.Fatalf("DecodeResponses() error = %#v", err)
	}
	if conversation.ReasoningEffort != "low" {
		t.Fatalf("ReasoningEffort = %q, want low", conversation.ReasoningEffort)
	}
	if _, err := DecodeResponses(body, NoReasoning); err == nil || err.Code != "unsupported_parameter" {
		t.Fatalf("uncapable error = %#v, want unsupported_parameter", err)
	}
	for _, tc := range []struct {
		name, body, code string
	}{
		{name: "out of enum", body: `{"model":"m","input":"hi","reasoning":{"effort":"extreme"}}`, code: "invalid_request"},
		{name: "unknown nested", body: `{"model":"m","input":"hi","reasoning":{"summary":"x"}}`, code: "unsupported_parameter"},
		{name: "wrong type", body: `{"model":"m","input":"hi","reasoning":"low"}`, code: "invalid_request"},
		{name: "empty object", body: `{"model":"m","input":"hi","reasoning":{}}`, code: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, decodeErr := DecodeResponses([]byte(tc.body), capableReasoningPolicy("m"))
			if tc.code == "" {
				if decodeErr != nil {
					t.Fatalf("expected success, got %#v", decodeErr)
				}
				return
			}
			if decodeErr == nil || decodeErr.Code != tc.code {
				t.Fatalf("error = %#v, want %s", decodeErr, tc.code)
			}
		})
	}
}

func TestValidateConversationRejectsInvalidReasoning(t *testing.T) {
	badEffort := Conversation{Model: "m", Messages: []Message{{Role: RoleUser, Content: "x"}}, ReasoningEffort: "extreme"}
	if err := validateConversation(badEffort); err == nil {
		t.Fatal("invalid effort must be rejected")
	}
	badThinking := ThinkingMode("maybe")
	unknownThinking := Conversation{Model: "m", Messages: []Message{{Role: RoleUser, Content: "x"}}, Thinking: &badThinking}
	if err := validateConversation(unknownThinking); err == nil {
		t.Fatal("invalid thinking mode must be rejected")
	}
	oversize := strings.Repeat("x", MaxTextContentBytes+1)
	badContent := Conversation{Model: "m", Messages: []Message{{Role: RoleAssistant, Content: "x", ReasoningContent: &oversize}}}
	if err := validateConversation(badContent); err == nil {
		t.Fatal("oversize reasoning content must be rejected")
	}
	good := Conversation{Model: "m", Messages: []Message{{Role: RoleAssistant, Content: "x", ReasoningContent: new(string)}}, ReasoningEffort: "low", Thinking: thinkingPointer(ThinkingDisabled)}
	if err := validateConversation(good); err != nil {
		t.Fatalf("valid reasoning rejected: %#v", err)
	}
}

func thinkingPointer(mode ThinkingMode) *ThinkingMode { return &mode }
