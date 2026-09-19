package whitelabel

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectChatCompletionProjectsReasoningContentAndUsageDetails(t *testing.T) {
	var service WhiteLabelService
	raw := []byte(`{
		"id":"safe","object":"chat.completion","created":1,
		"choices":[{"index":0,"message":{"role":"assistant","content":"hi","reasoning_content":"chain-of-thought"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,
			"prompt_tokens_details":{"cached_tokens":4},
			"completion_tokens_details":{"reasoning_tokens":3},
			"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":6}
	}`)
	completion, err := service.ProjectChatCompletion(raw, "model-a")
	if err != nil {
		t.Fatalf("ProjectChatCompletion() error = %#v", err)
	}
	message := completion.Choices[0].Message
	if message.ReasoningContent == nil || *message.ReasoningContent != "chain-of-thought" {
		t.Fatalf("reasoning_content = %#v", message.ReasoningContent)
	}
	usage := completion.Usage
	if usage == nil || usage.TotalTokens != 15 || usage.PromptTokens != 10 || usage.CompletionTokens != 5 {
		t.Fatalf("usage aggregates = %#v", usage)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens == nil || *usage.PromptTokensDetails.CachedTokens != 4 {
		t.Fatalf("prompt_tokens_details = %#v", usage.PromptTokensDetails)
	}
	if usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens == nil || *usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("completion_tokens_details = %#v", usage.CompletionTokensDetails)
	}
	if usage.PromptCacheHitTokens == nil || *usage.PromptCacheHitTokens != 4 || usage.PromptCacheMissTokens == nil || *usage.PromptCacheMissTokens != 6 {
		t.Fatalf("prompt cache counters = %#v / %#v", usage.PromptCacheHitTokens, usage.PromptCacheMissTokens)
	}
}

func TestProjectChatCompletionOmitsAbsentReasoningAndUsageDetails(t *testing.T) {
	var service WhiteLabelService
	completion, err := service.ProjectChatCompletion([]byte(`{"id":"safe","object":"chat.completion","created":1,"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`), "model-a")
	if err != nil {
		t.Fatalf("ProjectChatCompletion() error = %#v", err)
	}
	encoded, marshalErr := json.Marshal(completion)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, absent := range []string{"reasoning_content", "prompt_tokens_details", "completion_tokens_details", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens"} {
		if bytes.Contains(encoded, []byte(absent)) {
			t.Fatalf("absent field %q must be omitted: %s", absent, encoded)
		}
	}
}

func TestProjectChatCompletionRejectsInvalidUsageDetails(t *testing.T) {
	var service WhiteLabelService
	for _, usage := range []string{
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":-1}}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"completion_tokens_details":{"reasoning_tokens":-1}}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_cache_hit_tokens":-1}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_cache_miss_tokens":-1}`,
		`{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"completion_tokens_details":{"reasoning_tokens":2147483648}}`,
	} {
		_, err := service.ProjectChatCompletion([]byte(`{"id":"safe","object":"chat.completion","created":1,"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":`+usage+`}`), "model-a")
		if err == nil || err.Status != 503 {
			t.Fatalf("usage %s: error = %#v, want safe 503", usage, err)
		}
	}
}

func TestProjectChatCompletionRejectsOversizeReasoningContent(t *testing.T) {
	var service WhiteLabelService
	oversize := strings.Repeat("x", MaxTextContentBytes+1)
	raw, marshalErr := json.Marshal(map[string]any{
		"id": "safe", "object": "chat.completion", "created": 1,
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "hi", "reasoning_content": oversize}, "finish_reason": "stop"}},
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := service.ProjectChatCompletion(raw, "model-a"); err == nil || err.Status != 503 {
		t.Fatalf("oversize reasoning_content: error = %#v, want safe 503", err)
	}
}

func TestConsumeChatCompletionSSEProjectsReasoningContentAndUsageDetails(t *testing.T) {
	input := strings.Join([]string{
		`data: {"id":"safe","object":"chat.completion.chunk","created":1,"model":"upstream-secret","choices":[{"index":0,"delta":{"reasoning_content":"thinking"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"safe","object":"chat.completion.chunk","created":2,"model":"upstream-secret","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3,"completion_tokens_details":{"reasoning_tokens":1}}}` + "\n\n",
		"data: [DONE]\n\n",
	}, "")
	var chunks []ChatCompletionChunk
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(context.Background(), strings.NewReader(input), "model-a", func(chunk ChatCompletionChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("ConsumeChatCompletionSSEContext() error = %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %#v", chunks)
	}
	delta := chunks[0].Choices[0].Delta
	if delta.ReasoningContent == nil || *delta.ReasoningContent != "thinking" {
		t.Fatalf("delta reasoning_content = %#v", delta.ReasoningContent)
	}
	usage := chunks[1].Usage
	if usage == nil || usage.TotalTokens != 3 || usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens == nil || *usage.CompletionTokensDetails.ReasoningTokens != 1 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestConsumeChatCompletionSSERejectsNegativeUsageDetails(t *testing.T) {
	input := "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"upstream-secret\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2,\"completion_tokens_details\":{\"reasoning_tokens\":-1}}}\n\ndata: [DONE]\n\n"
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(context.Background(), strings.NewReader(input), "model-a", func(ChatCompletionChunk) error { return nil })
	if err == nil {
		t.Fatal("negative reasoning_tokens must be rejected")
	}
}
