package openaicompat

import "testing"

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
