package handler_test

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestGatewayChatAcceptsUnboundedAgentConversation covers the deliberate
// removal of the message-count ceiling end to end: a long tool-call history
// (well past the previous 128-message cap) must reach the upstream.
func TestGatewayChatAcceptsUnboundedAgentConversation(t *testing.T) {
	state, recorder := gatewayHarnessState(t, "model-a", "")
	secret := gatewayHarnessToken(t, state)

	var builder strings.Builder
	builder.WriteString(`{"model":"model-a","max_tokens":16,"messages":[{"role":"user","content":"start"}`)
	for call := 0; call < 200; call++ {
		builder.WriteString(`,{"role":"assistant","content":"","tool_calls":[{"id":"call_`)
		builder.WriteString(strconv.Itoa(call))
		builder.WriteString(`","type":"function","function":{"name":"bash","arguments":"{}"}}]}`)
		builder.WriteString(`,{"role":"tool","tool_call_id":"call_`)
		builder.WriteString(strconv.Itoa(call))
		builder.WriteString(`","content":"ok"}`)
	}
	builder.WriteString(`]}`)

	rec := postGatewayChat(t, state, secret, builder.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	forwarded := recorder.last()
	if forwarded == nil || !bytes.Contains(forwarded, []byte(`"messages"`)) {
		t.Fatalf("chat body was not forwarded upstream: %s", forwarded)
	}
	if got := bytes.Count(forwarded, []byte(`"role":"tool"`)); got != 200 {
		t.Fatalf("forwarded tool messages = %d, want 200", got)
	}
}
