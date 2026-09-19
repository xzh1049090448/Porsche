package openaicompat

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestExtractChatRouting(t *testing.T) {
	model, stream, err := ExtractChatRouting([]byte(`{"model":"m","stream":true,"reasoning_effort":"high","unknown":{"a":1}}`))
	if err != nil || model != "m" || !stream {
		t.Fatalf("routing = %q/%t/%#v", model, stream, err)
	}
	model, stream, err = ExtractChatRouting([]byte(`{"model":"m","messages":[]}`))
	if err != nil || model != "m" || stream {
		t.Fatalf("default stream routing = %q/%t/%#v", model, stream, err)
	}
	for name, body := range map[string]string{
		"missing model": `{"messages":[]}`,
		"blank model":   `{"model":"  "}`,
		"array":         `[1,2]`,
		"multiple":      `{"model":"m"} {"model":"n"}`,
		"malformed":     `{"model":`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, got := ExtractChatRouting([]byte(body)); got == nil || got.Code != "invalid_request" {
				t.Fatalf("error = %#v, want invalid_request", got)
			}
		})
	}
	if _, _, got := ExtractChatRouting(make([]byte, MaxRequestBodyBytes+1)); got == nil || got.Code != "request_too_large" {
		t.Fatalf("oversize error = %#v, want request_too_large", got)
	}
}

func TestSanitizePassthroughStripsDeniedFieldsAndPreservesAllowedOnes(t *testing.T) {
	body := []byte(`{
		"model":"m",
		"messages":[{"role":"user","content":"prompt-sentinel"}],
		"reasoning_effort":"high",
		"thinking":{"type":"enabled"},
		"temperature":0.5,
		"user":"user-sentinel",
		"metadata":{"a":1},
		"safety_identifier":"safety-sentinel",
		"service_tier":"priority",
		"inference_geo":"eu",
		"speed":"fast",
		"store":true,
		"previous_response_id":"resp_1",
		"prompt_cache_key":"cache-sentinel",
		"logit_bias":{"1":1},
		"logprobs":true,
		"top_logprobs":5,
		"api_key":"key-sentinel",
		"authorization":"bearer-sentinel",
		"secret":"secret-sentinel",
		"password":"password-sentinel",
		"credentials":"cred-sentinel",
		"provider":"provider-sentinel",
		"stream_options":{"include_usage":true,"include_obfuscation":true}
	}`)
	sanitized, report, err := SanitizePassthrough(body, "m")
	if err != nil {
		t.Fatalf("SanitizePassthrough() error = %#v", err)
	}
	wantStripped := []string{
		"api_key", "authorization", "credentials", "inference_geo", "logit_bias", "logprobs",
		"metadata", "password", "previous_response_id", "prompt_cache_key", "provider",
		"safety_identifier", "secret", "service_tier", "speed", "store",
		"stream_options.include_obfuscation", "top_logprobs", "user",
	}
	sort.Strings(wantStripped)
	if strings.Join(report.StrippedFields, ",") != strings.Join(wantStripped, ",") {
		t.Fatalf("stripped = %v, want %v", report.StrippedFields, wantStripped)
	}
	if report.MessageCount != 1 || report.ToolCount != 0 {
		t.Fatalf("report = %#v", report)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(sanitized, &fields) != nil {
		t.Fatalf("sanitized body is not JSON: %s", sanitized)
	}
	for _, denied := range []string{
		"user", "metadata", "safety_identifier", "service_tier", "inference_geo", "speed",
		"store", "previous_response_id", "prompt_cache_key", "logit_bias", "logprobs",
		"top_logprobs", "api_key", "authorization", "secret", "password", "credentials", "provider",
	} {
		if _, present := fields[denied]; present {
			t.Fatalf("denied field %q was forwarded: %s", denied, sanitized)
		}
	}
	for _, kept := range []string{"model", "messages", "reasoning_effort", "thinking", "temperature", "stream_options"} {
		if _, present := fields[kept]; !present {
			t.Fatalf("allowed field %q was dropped: %s", kept, sanitized)
		}
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(fields["stream_options"], &options) != nil || len(options) != 1 {
		t.Fatalf("stream_options = %s", fields["stream_options"])
	}
	if _, present := options["include_usage"]; !present {
		t.Fatalf("stream_options.include_usage was dropped: %s", fields["stream_options"])
	}
	for _, sentinel := range []string{"user-sentinel", "safety-sentinel", "cache-sentinel", "key-sentinel", "bearer-sentinel", "secret-sentinel", "password-sentinel", "cred-sentinel", "provider-sentinel"} {
		if strings.Contains(string(sanitized), sentinel) {
			t.Fatalf("sanitized body leaked %q: %s", sentinel, sanitized)
		}
	}
	if !strings.Contains(string(sanitized), "prompt-sentinel") {
		t.Fatalf("allowed message content must survive: %s", sanitized)
	}
}

func TestSanitizePassthroughRemovesEmptyStreamOptions(t *testing.T) {
	sanitized, report, err := SanitizePassthrough([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"stream_options":{"include_obfuscation":true}}`), "m")
	if err != nil {
		t.Fatalf("SanitizePassthrough() error = %#v", err)
	}
	if strings.Join(report.StrippedFields, ",") != "stream_options.include_obfuscation" {
		t.Fatalf("stripped = %v", report.StrippedFields)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(sanitized, &fields) != nil {
		t.Fatal("sanitized body is not JSON")
	}
	if _, present := fields["stream_options"]; present {
		t.Fatalf("empty stream_options must be removed: %s", sanitized)
	}
}

func TestSanitizePassthroughRejectsInvalidStructure(t *testing.T) {
	tooManyMessages := `{"model":"m","messages":[` + strings.TrimSuffix(strings.Repeat(`{"role":"user","content":"x"},`, MaxMessages+1), ",") + `]}`
	tooManyTools := `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[` + strings.TrimSuffix(strings.Repeat(`{"type":"function"},`, MaxTools+1), ",") + `]}`
	for name, body := range map[string]string{
		"non object":         `"string"`,
		"array":              `[]`,
		"null":               `null`,
		"multiple values":    `{"model":"m","messages":[]}{"model":"m"}`,
		"missing model":      `{"messages":[{"role":"user","content":"x"}]}`,
		"model mismatch":     `{"model":"other","messages":[{"role":"user","content":"x"}]}`,
		"missing messages":   `{"model":"m"}`,
		"empty messages":     `{"model":"m","messages":[]}`,
		"messages not array": `{"model":"m","messages":{}}`,
		"too many messages":  tooManyMessages,
		"tools not array":    `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":{}}`,
		"too many tools":     tooManyTools,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := SanitizePassthrough([]byte(body), "m"); err == nil || err.Code != "invalid_request" {
				t.Fatalf("error = %#v, want invalid_request", err)
			}
		})
	}
	if _, _, err := SanitizePassthrough(make([]byte, MaxRequestBodyBytes+1), "m"); err == nil || err.Code != "request_too_large" {
		t.Fatalf("oversize error = %#v, want request_too_large", err)
	}
}

func TestSanitizePassthroughPreservesAssistantReasoningContent(t *testing.T) {
	sanitized, _, err := SanitizePassthrough([]byte(`{"model":"m","messages":[{"role":"assistant","content":"","reasoning_content":"cot"}]}`), "m")
	if err != nil {
		t.Fatalf("SanitizePassthrough() error = %#v", err)
	}
	if !strings.Contains(string(sanitized), `"reasoning_content":"cot"`) {
		t.Fatalf("reasoning_content must survive passthrough: %s", sanitized)
	}
}
