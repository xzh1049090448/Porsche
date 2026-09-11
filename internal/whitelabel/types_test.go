package whitelabel

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestProjectChatCompletionAllowsNullContentWithToolCalls(t *testing.T) {
	raw := []byte(`{"id":"safe","object":"chat.completion","created":1,"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"raw"}}]},"finish_reason":"tool_calls"}]}`)
	got, err := (&WhiteLabelService{}).ProjectChatCompletion(raw, "model-a")
	if err != nil || got.Choices[0].Message.Content != nil || len(got.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("completion=%#v err=%#v", got, err)
	}
}

func TestProjectChatCompletionProjectsNestedMessageAndDropsLogprobs(t *testing.T) {
	var service WhiteLabelService
	completion, err := service.ProjectChatCompletion([]byte(`{
		"id":"safe","object":"chat.completion","created":1,"model":"upstream-model",
		"choices":[{
			"index":0,"finish_reason":"tool_calls","logprobs":{"secret":"logprobs-secret"},
			"message":{"role":"assistant","content":[{"type":"text","text":"hello","secret":"part-secret"}],"refusal":"no","secret":"message-secret","tool_calls":[{"id":"call_1","type":"function","secret":"tool-secret","function":{"name":"lookup","arguments":"query=x","secret":"function-secret"}}]}
		}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3},"secret":"top-secret"
	}`), "model-a")
	if err != nil {
		t.Fatalf("ProjectChatCompletion() error = %#v", err)
	}
	encoded, marshalErr := json.Marshal(completion)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, secret := range []string{"top-secret", "message-secret", "part-secret", "tool-secret", "function-secret", "logprobs-secret"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("projected completion leaked %q: %s", secret, encoded)
		}
	}
	if !bytes.Contains(encoded, []byte(`"role":"assistant"`)) || !bytes.Contains(encoded, []byte(`"content":[{"type":"text","text":"hello"}]`)) || !bytes.Contains(encoded, []byte(`"refusal":"no"`)) || !bytes.Contains(encoded, []byte(`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"query=x"}}]`)) || !bytes.Contains(encoded, []byte(`"finish_reason":"tool_calls"`)) {
		t.Fatalf("allowed nested completion fields missing: %s", encoded)
	}
	if bytes.Contains(encoded, []byte(`"logprobs"`)) {
		t.Fatalf("logprobs must not be projected: %s", encoded)
	}
}

func TestProjectChatCompletionRejectsMalformedNestedKnownFields(t *testing.T) {
	var service WhiteLabelService
	for _, malformed := range []string{
		`{"role":"assistant","content":123}`,
		`{"role":"assistant","content":null}`,
		`{"role":"assistant","content":"hello","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":123}}]}`,
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call 1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`,
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"custom","function":{"name":"lookup","arguments":"{}"}}]}`,
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bad name","arguments":"{}"}}]}`,
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}},{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`,
		`{"role":"assistant","content":[{"type":"text","text":123}]}`,
	} {
		_, err := service.ProjectChatCompletion([]byte(`{"id":"safe","object":"chat.completion","created":1,"choices":[{"index":0,"message":`+malformed+`,"finish_reason":"stop"}]}`), "model-a")
		if err == nil || err.Status != 503 {
			t.Fatalf("malformed message %s: error = %#v, want safe 503", malformed, err)
		}
	}
}

func TestValidModelIDAcceptsSafeSlashSeparatedIDs(t *testing.T) {
	for _, id := range []string{
		"model-a",
		"zai-org/glm-5.1",
		"deepseek/deepseek-v4-pro",
		"team/subteam/model-v2",
	} {
		if !validModelID(id) {
			t.Fatalf("validModelID(%q) = false, want true", id)
		}
	}
}

func TestValidModelIDRejectsUnsafeOrMalformedSlashIDs(t *testing.T) {
	for _, id := range []string{
		"/model",
		"org/",
		"org//model",
		"./model",
		"org/../model",
		"org\\model",
		"org/model?query=value",
		"org/model#fragment",
		"org%2Fmodel",
		" org/model",
		"org/model ",
		"org/\tmodel",
		"org/\x00model",
		"org/\u200bmodel",
		"org/\u202emodel",
	} {
		if validModelID(id) {
			t.Fatalf("validModelID(%q) = true, want false", id)
		}
	}
}

func TestCloneModelsNilReturnsNonNilEmptySlice(t *testing.T) {
	cloned := cloneModels(nil)
	if cloned == nil {
		t.Fatal("cloneModels(nil) = nil, want non-nil empty slice")
	}
	if len(cloned) != 0 {
		t.Fatalf("len(cloneModels(nil)) = %d, want 0", len(cloned))
	}
}
