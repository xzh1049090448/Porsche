package openaicompat

import (
	"fmt"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

func TestProjectResponseIncludesTextAndParallelCalls(t *testing.T) {
	completion := whitelabel.ChatCompletion{ID: "upstream", Object: "chat.completion", Created: 10, Model: "model-a", Choices: []whitelabel.ChatCompletionChoice{{Index: 0, Message: whitelabel.ChatCompletionMessage{Role: "assistant", Content: "hello", ToolCalls: []whitelabel.ChatCompletionToolCall{{ID: "call_1", Type: "function", Function: whitelabel.ChatCompletionFunctionCall{Name: "a", Arguments: "{}"}}, {ID: "call_2", Type: "function", Function: whitelabel.ChatCompletionFunctionCall{Name: "b", Arguments: "raw"}}}}}}}
	got, err := ProjectResponse(completion, true, sequentialIDSource())
	if err != nil || got.Object != "response" || len(got.Output) != 3 || got.Store || got.ID == "upstream" {
		t.Fatalf("response=%#v err=%v", got, err)
	}
	if got.Output[1].CallID != "call_1" || got.Output[2].CallID != "call_2" {
		t.Fatalf("tool outputs=%#v", got.Output)
	}
}

func sequentialIDSource() IDSource {
	n := 0
	return func(prefix string) (string, error) {
		n++
		return fmt.Sprintf("%s_test_%d", prefix, n), nil
	}
}
