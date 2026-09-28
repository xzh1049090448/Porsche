package handler_test

import (
	"bytes"
	"log"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestGatewayChatAcceptsLongAgentConversation covers the regression that broke
// long coding-agent sessions: a valid tool-call history above the previous
// 128-message cap must reach the upstream instead of being rejected as
// invalid_request.
func TestGatewayChatAcceptsLongAgentConversation(t *testing.T) {
	state, recorder := gatewayHarnessState(t, "model-a", "")
	secret := gatewayHarnessToken(t, state)

	var builder strings.Builder
	builder.WriteString(`{"model":"model-a","max_tokens":16,"messages":[{"role":"user","content":"start"}`)
	written := 1
	for call := 0; written+1 < 140; call++ {
		builder.WriteString(`,{"role":"assistant","content":"","tool_calls":[{"id":"call_`)
		builder.WriteString(strconv.Itoa(call))
		builder.WriteString(`","type":"function","function":{"name":"bash","arguments":"{}"}}]}`)
		builder.WriteString(`,{"role":"tool","tool_call_id":"call_`)
		builder.WriteString(strconv.Itoa(call))
		builder.WriteString(`","content":"ok"}`)
		written += 2
	}
	builder.WriteString(`]}`)

	rec := postGatewayChat(t, state, secret, builder.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The catalog refresh is also an upstream call, so only require that the
	// chat body itself was forwarded.
	if forwarded := recorder.last(); forwarded == nil || !bytes.Contains(forwarded, []byte(`"messages"`)) {
		t.Fatalf("chat body was not forwarded upstream: %s", forwarded)
	}
}

func TestGatewayChatLogsRejectedFieldWithoutValues(t *testing.T) {
	state, _ := gatewayHarnessState(t, "", "")
	secret := gatewayHarnessToken(t, state)

	var logBuffer bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logBuffer)
	t.Cleanup(func() { log.SetOutput(previous) })

	body := `{"model":"model-a","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"sensitive_field":"SECRET-SENTINEL"}`
	rec := postGatewayChat(t, state, secret, body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"unsupported_parameter"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "SECRET-SENTINEL") || strings.Contains(rec.Body.String(), "sensitive_field") {
		t.Fatalf("public error must not name the field or leak the value: %s", rec.Body.String())
	}
	logged := logBuffer.String()
	if !strings.Contains(logged, "gateway request rejected") || !strings.Contains(logged, "sensitive_field") {
		t.Fatalf("diagnostic log missing the rejected field: %s", logged)
	}
	if strings.Contains(logged, "SECRET-SENTINEL") {
		t.Fatalf("diagnostic log leaked a value: %s", logged)
	}
}
