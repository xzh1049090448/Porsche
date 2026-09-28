package openaicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// longAgentConversation builds a valid tool-call/tool-result conversation with
// the given number of message entries, mirroring a coding agent replaying its
// history (assistant tool_calls followed by the matching tool result).
func longAgentConversation(entries int) []byte {
	var builder strings.Builder
	builder.WriteString(`{"model":"deepseek/deepseek-v4-flash","max_tokens":16,"messages":[`)
	builder.WriteString(`{"role":"user","content":"start"}`)
	written := 1
	for call := 0; written+1 < entries; call++ {
		fmt.Fprintf(&builder, `,{"role":"assistant","content":"","tool_calls":[{"id":"call_%d","type":"function","function":{"name":"bash","arguments":"{}"}}]}`, call)
		fmt.Fprintf(&builder, `,{"role":"tool","tool_call_id":"call_%d","content":"ok"}`, call)
		written += 2
	}
	builder.WriteString(`]}`)
	return []byte(builder.String())
}

func TestDecodeChatAcceptsLongAgentConversation(t *testing.T) {
	policy := capableReasoningPolicy("deepseek/deepseek-v4-flash")
	// 130 entries is the size that previously failed (old MaxMessages was 128).
	for _, entries := range []int{130, 300, MaxMessages} {
		body := longAgentConversation(entries)
		conversation, err := DecodeChat(body, policy)
		if err != nil {
			t.Fatalf("entries=%d DecodeChat() error = %#v", entries, err)
		}
		if len(conversation.Messages) < 2 {
			t.Fatalf("entries=%d decoded %d messages", entries, len(conversation.Messages))
		}
		encoded, encodeErr := EncodeUpstream(conversation)
		if encodeErr != nil {
			t.Fatalf("entries=%d EncodeUpstream() error = %v", entries, encodeErr)
		}
		var upstream struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if json.Unmarshal(encoded, &upstream) != nil || len(upstream.Messages) != len(conversation.Messages) {
			t.Fatalf("entries=%d upstream messages = %d, want %d", entries, len(upstream.Messages), len(conversation.Messages))
		}
	}
}

func TestDecodeChatRejectsMessagesBeyondLimit(t *testing.T) {
	policy := capableReasoningPolicy("m")
	var builder strings.Builder
	builder.WriteString(`{"model":"m","max_tokens":16,"messages":[`)
	for i := 0; i <= MaxMessages; i++ {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"role":"user","content":"x"}`)
	}
	builder.WriteString(`]}`)
	_, err := DecodeChat([]byte(builder.String()), policy)
	if err == nil || err.Status != 413 || err.Code != "request_too_large" {
		t.Fatalf("error = %#v, want 413 request_too_large", err)
	}
	if !strings.Contains(err.Detail, fmt.Sprintf("messages=%d", MaxMessages+1)) {
		t.Fatalf("detail = %q, want the offending and limit counts", err.Detail)
	}
}

func TestDecodeChatDiagnosticDetailNamesFieldWithoutValues(t *testing.T) {
	policy := capableReasoningPolicy("m")

	_, err := DecodeChat([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"max_tokens":16,"sensitive_field":"SECRET-SENTINEL"}`), policy)
	if err == nil || err.Code != "unsupported_parameter" {
		t.Fatalf("error = %#v, want unsupported_parameter", err)
	}
	if !strings.Contains(err.Detail, "sensitive_field") {
		t.Fatalf("detail = %q, want the unknown field name", err.Detail)
	}
	if strings.Contains(err.Detail, "SECRET-SENTINEL") {
		t.Fatalf("detail leaked a value: %q", err.Detail)
	}

	_, nestedErr := DecodeChat([]byte(`{"model":"m","messages":[{"role":"assistant","content":"hi","reasoning_bogus":"SECRET-SENTINEL"}],"max_tokens":16}`), policy)
	if nestedErr == nil || nestedErr.Code != "invalid_request" {
		t.Fatalf("nested error = %#v, want invalid_request", nestedErr)
	}
	if !strings.Contains(nestedErr.Detail, "reasoning_bogus") || !strings.Contains(nestedErr.Detail, "message[0]") {
		t.Fatalf("nested detail = %q, want message index and field name", nestedErr.Detail)
	}
	if strings.Contains(nestedErr.Detail, "SECRET-SENTINEL") {
		t.Fatalf("nested detail leaked a value: %q", nestedErr.Detail)
	}

	_, oversizeErr := DecodeChat(make([]byte, MaxRequestBodyBytes+1), policy)
	if oversizeErr == nil || oversizeErr.Status != 413 || oversizeErr.Detail == "" {
		t.Fatalf("oversize error = %#v, want 413 with a size detail", oversizeErr)
	}
}
