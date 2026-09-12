package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeResponsesNormalizesFunctionRoundTrip(t *testing.T) {
	body := []byte(`{"model":"model-a","instructions":"be concise","input":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}","id":"fc_1","status":"completed"},{"type":"function_call_output","call_id":"call_1","output":"ok","id":"fco_1","status":"completed"}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"store":false}`)
	got, err := DecodeResponses(body)
	if err != nil || len(got.Instructions) != 1 || got.Messages[0].ToolCalls[0].ID != "call_1" || got.Messages[1].ToolCallID != "call_1" {
		t.Fatalf("conversation=%#v err=%#v", got, err)
	}
}

func TestDecodeResponsesRejectsStatefulAndManagedFeatures(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","input":"x","store":true}`,
		`{"model":"m","input":"x","previous_response_id":"resp_1"}`,
		`{"model":"m","input":"x","tools":[{"type":"web_search"}]}`,
	} {
		if _, err := DecodeResponses([]byte(body)); err == nil || err.Code != "unsupported_parameter" {
			t.Fatalf("accepted %s with err=%#v", body, err)
		}
	}
}

func TestDecodeResponsesDefaultsParallelCallsAndAcceptsTextItems(t *testing.T) {
	body := []byte(`{"model":"model-a","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	got, err := DecodeResponses(body)
	if err != nil || got.ParallelToolCalls == nil || !*got.ParallelToolCalls || got.Messages[0].Content != "hello" {
		t.Fatalf("conversation=%#v err=%#v", got, err)
	}
}

func TestDecodeResponsesClassifiesOversizedHistoricalToolPayloads(t *testing.T) {
	tests := []struct {
		name string
		item map[string]any
	}{
		{name: "function arguments", item: map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": strings.Repeat("a", MaxArgumentsBytes+1)}},
		{name: "function output", item: map[string]any{"type": "function_call_output", "call_id": "call_1", "output": strings.Repeat("o", MaxToolOutputBytes+1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"model": "m", "input": []any{tt.item}})
			if err != nil {
				t.Fatal(err)
			}
			if len(body) >= MaxRequestBodyBytes {
				t.Fatalf("test body=%d must remain below body limit", len(body))
			}
			if _, got := DecodeResponses(body); got == nil || got.Status != 413 || got.Code != "request_too_large" {
				t.Fatalf("error=%#v, want 413 request_too_large", got)
			}
		})
	}
}

func TestDecodeResponsesKeepsMalformedToolPayloadsAt400(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"model":"m","input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":1}]}`),
		[]byte(`{"model":"m","input":[{"type":"function_call_output","call_id":"call_1","output":{"bad":"shape"}}]}`),
		append([]byte(`{"model":"m","input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"`), append([]byte{0xff}, []byte(`"}]}`)...)...),
		append([]byte(`{"model":"m","input":[{"type":"function_call_output","call_id":"call_1","output":"`), append([]byte{0xff}, []byte(`"}]}`)...)...),
	} {
		if _, got := DecodeResponses(body); got == nil || got.Status != 400 || got.Code != "invalid_request" {
			t.Fatalf("body=%q error=%#v, want 400 invalid_request", body, got)
		}
	}
}

func TestDecodeResponsesClassifiesNestedUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","input":[{"type":"message","role":"user","content":"x","extra":true}]}`,
		`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"x","extra":true}]}]}`,
		`{"model":"m","input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}","extra":true}]}`,
		`{"model":"m","input":[{"type":"function_call_output","call_id":"call_1","output":"x","extra":true}]}`,
		`{"model":"m","input":"x","tools":[{"type":"function","name":"lookup","parameters":{},"extra":true}]}`,
		`{"model":"m","input":"x","tool_choice":{"type":"function","name":"lookup","extra":true}}`,
	} {
		if _, got := DecodeResponses([]byte(body)); got == nil || got.Status != 400 || got.Code != "unsupported_parameter" {
			t.Fatalf("body=%s error=%#v, want 400 unsupported_parameter", body, got)
		}
	}
}

func TestDecodeResponsesKeepsMalformedNestedValuesAtInvalidRequest(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","input":[{"type":"message","role":"user","content":1}]}`,
		`{"model":"m","input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":1}]}`,
		`{"model":"m","input":[{"type":"function_call_output","call_id":"call_1","output":1}]}`,
		`{"model":"m","input":"x","tools":[{"type":"function","name":"bad name","parameters":{}}]}`,
		`{"model":"m","input":"x","tool_choice":{"type":"function","name":"bad name"}}`,
	} {
		if _, got := DecodeResponses([]byte(body)); got == nil || got.Status != 400 || got.Code != "invalid_request" {
			t.Fatalf("body=%s error=%#v, want 400 invalid_request", body, got)
		}
	}
}
