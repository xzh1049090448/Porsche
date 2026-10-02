package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// messagesBody builds a Chat Completions body with count minimal user messages.
// The total size stays far below MaxRequestBodyBytes, so only the (removed)
// message-count ceiling could have rejected it.
func messagesBody(count int) []byte {
	var builder strings.Builder
	builder.WriteString(`{"model":"m","max_tokens":16,"messages":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"role":"user","content":"x"}`)
	}
	builder.WriteString(`]}`)
	return []byte(builder.String())
}

// TestDecodeChatAcceptsUnboundedMessageCount records the deliberate removal of
// the message-count ceiling on the gateway path.
func TestDecodeChatAcceptsUnboundedMessageCount(t *testing.T) {
	policy := capableReasoningPolicy("m")
	for _, count := range []int{129, 1024, 20000} {
		body := messagesBody(count)
		if len(body) > MaxRequestBodyBytes {
			t.Fatalf("fixture count=%d exceeds the body cap", count)
		}
		conversation, err := DecodeChat(body, policy)
		if err != nil {
			t.Fatalf("count=%d rejected: %#v", count, err)
		}
		if len(conversation.Messages) != count {
			t.Fatalf("count=%d decoded %d messages", count, len(conversation.Messages))
		}
		encoded, encodeErr := EncodeUpstream(conversation)
		if encodeErr != nil {
			t.Fatalf("count=%d EncodeUpstream() error = %v", count, encodeErr)
		}
		var upstream struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if json.Unmarshal(encoded, &upstream) != nil || len(upstream.Messages) != count {
			t.Fatalf("count=%d upstream messages = %d", count, len(upstream.Messages))
		}
	}
}

// TestSanitizePassthroughAcceptsUnboundedMessageCount covers the explicit
// passthrough path, which previously shared the same ceiling.
func TestSanitizePassthroughAcceptsUnboundedMessageCount(t *testing.T) {
	for _, count := range []int{129, 20000} {
		sanitized, report, err := SanitizePassthrough(messagesBody(count), "m")
		if err != nil {
			t.Fatalf("count=%d rejected: %#v", count, err)
		}
		if report.MessageCount != count || len(sanitized) == 0 {
			t.Fatalf("count=%d report=%#v", count, report)
		}
	}
}

// TestDecodeResponsesAcceptsUnboundedInputItems covers the Responses input
// array, which previously allowed only 2x the Chat ceiling.
func TestDecodeResponsesAcceptsUnboundedInputItems(t *testing.T) {
	for _, count := range []int{257, 5000} {
		var builder strings.Builder
		builder.WriteString(`{"model":"m","input":[`)
		for i := 0; i < count; i++ {
			if i > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"x"}]}`)
		}
		builder.WriteString(`]}`)
		if _, err := DecodeResponses([]byte(builder.String()), capableReasoningPolicy("m")); err != nil {
			t.Fatalf("items=%d rejected: %#v", count, err)
		}
	}
}
