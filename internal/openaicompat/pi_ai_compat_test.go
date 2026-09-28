package openaicompat

import (
	"bytes"
	"strings"
	"testing"
)

// piAiZaiBody mirrors the request shape @earendil-works/pi-ai builds when a
// route is detected as a Z.ai/GLM ("zai") provider: thinking carries an extra
// clear_thinking flag and no reasoning_effort is sent.
func piAiZaiBody() []byte {
	return []byte(`{
		"model":"deepseek/deepseek-v4-flash",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"","reasoning_content":"cot","reasoning_details":[{"type":"reasoning.text","text":"cot"}]}
		],
		"thinking":{"type":"enabled","clear_thinking":false},
		"stream":true,
		"stream_options":{"include_usage":true},
		"max_tokens":16
	}`)
}

func TestDecodeChatPiAiZaiThinkingRoundTrip(t *testing.T) {
	policy := capableReasoningPolicy("deepseek/deepseek-v4-flash")
	conversation, err := DecodeChat(piAiZaiBody(), policy)
	if err != nil {
		t.Fatalf("DecodeChat() error = %#v", err)
	}
	if conversation.Thinking == nil || *conversation.Thinking != ThinkingEnabled {
		t.Fatalf("Thinking = %#v", conversation.Thinking)
	}
	if conversation.ThinkingClearThinking == nil || *conversation.ThinkingClearThinking {
		t.Fatalf("ThinkingClearThinking = %#v, want explicit false", conversation.ThinkingClearThinking)
	}
	if assistant := conversation.Messages[1]; assistant.ReasoningContent == nil || *assistant.ReasoningContent != "cot" {
		t.Fatalf("assistant message = %#v", assistant)
	}

	encoded, encodeErr := EncodeUpstream(conversation)
	if encodeErr != nil {
		t.Fatalf("EncodeUpstream() error = %v", encodeErr)
	}
	for _, want := range []string{`"thinking":{"type":"enabled","clear_thinking":false}`, `"reasoning_content":"cot"`} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Fatalf("upstream body missing %s: %s", want, encoded)
		}
	}
	if bytes.Contains(encoded, []byte("reasoning_details")) {
		t.Fatalf("reasoning_details must be dropped, not forwarded: %s", encoded)
	}
}

func TestDecodeChatPiAiZaiThinkingOmittedClearThinking(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"},"max_tokens":16}`)
	conversation, err := DecodeChat(body, capableReasoningPolicy("m"))
	if err != nil {
		t.Fatalf("DecodeChat() error = %#v", err)
	}
	if conversation.ThinkingClearThinking != nil {
		t.Fatalf("ThinkingClearThinking = %#v, want nil", conversation.ThinkingClearThinking)
	}
	encoded, encodeErr := EncodeUpstream(conversation)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if !bytes.Contains(encoded, []byte(`"thinking":{"type":"disabled"}`)) || bytes.Contains(encoded, []byte("clear_thinking")) {
		t.Fatalf("omitted clear_thinking must stay omitted: %s", encoded)
	}
}

func TestDecodeChatThinkingShapeErrorsStayUnsupported(t *testing.T) {
	policy := capableReasoningPolicy("m")
	for name, body := range map[string]string{
		"unknown nested":     `{"model":"m","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","x":1},"max_tokens":16}`,
		"clear wrong type":   `{"model":"m","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","clear_thinking":"false"},"max_tokens":16}`,
		"missing type":       `{"model":"m","messages":[{"role":"user","content":"hi"}],"thinking":{"clear_thinking":false},"max_tokens":16}`,
		"unknown type value": `{"model":"m","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"nope","clear_thinking":false},"max_tokens":16}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeChat([]byte(body), policy); err == nil || err.Code != "unsupported_parameter" {
				t.Fatalf("error = %#v, want unsupported_parameter", err)
			}
		})
	}
}

func TestValidateConversationRejectsClearThinkingWithoutThinking(t *testing.T) {
	clear := false
	conversation := Conversation{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, ThinkingClearThinking: &clear}
	if err := validateConversation(conversation); err == nil {
		t.Fatal("clear_thinking without thinking must be rejected")
	}
}

func TestDecodeChatAssistantReasoningDetailsForms(t *testing.T) {
	policy := capableReasoningPolicy("m")
	accepted := map[string]string{
		"array": `{"model":"m","messages":[{"role":"assistant","content":"hi","reasoning_details":[{"type":"reasoning.text","text":"cot"}]}],"max_tokens":16}`,
		"empty": `{"model":"m","messages":[{"role":"assistant","content":"hi","reasoning_details":[]}],"max_tokens":16}`,
		"null":  `{"model":"m","messages":[{"role":"assistant","content":"hi","reasoning_details":null}],"max_tokens":16}`,
	}
	for name, body := range accepted {
		t.Run("accept "+name, func(t *testing.T) {
			conversation, err := DecodeChat([]byte(body), policy)
			if err != nil {
				t.Fatalf("DecodeChat() error = %#v", err)
			}
			encoded, encodeErr := EncodeUpstream(conversation)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if bytes.Contains(encoded, []byte("reasoning_details")) {
				t.Fatalf("reasoning_details must never reach upstream: %s", encoded)
			}
		})
	}

	rejected := map[string]string{
		"object not array": `{"model":"m","messages":[{"role":"assistant","content":"hi","reasoning_details":{"a":1}}],"max_tokens":16}`,
		"string":           `{"model":"m","messages":[{"role":"assistant","content":"hi","reasoning_details":"cot"}],"max_tokens":16}`,
		"non assistant":    `{"model":"m","messages":[{"role":"user","content":"hi","reasoning_details":[{"a":1}]}],"max_tokens":16}`,
		"oversize":         `{"model":"m","messages":[{"role":"assistant","content":"hi","reasoning_details":[{"text":"` + strings.Repeat("x", MaxArgumentsBytes+1) + `"}]}],"max_tokens":16}`,
	}
	for name, body := range rejected {
		t.Run("reject "+name, func(t *testing.T) {
			if _, err := DecodeChat([]byte(body), policy); err == nil || err.Code != "invalid_request" {
				t.Fatalf("error = %#v, want invalid_request", err)
			}
		})
	}
}

func TestDecodeChatReasoningDetailsAloneIsDroppedWithoutCapability(t *testing.T) {
	// reasoning_details is accepted replay metadata that is never forwarded, so
	// it does not by itself require the model to be reasoning-capable.
	body := `{"model":"m","messages":[{"role":"assistant","content":"hi","reasoning_details":[{"a":1}]}],"max_tokens":16}`
	conversation, err := DecodeChat([]byte(body), NoReasoning)
	if err != nil {
		t.Fatalf("DecodeChat() error = %#v", err)
	}
	encoded, encodeErr := EncodeUpstream(conversation)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if bytes.Contains(encoded, []byte("reasoning_details")) {
		t.Fatalf("reasoning_details must be dropped: %s", encoded)
	}
}
