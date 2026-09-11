package openaicompat

import "testing"

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
