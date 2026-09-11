package openaicompat

import (
	"testing"

	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

func TestResponsesStreamOrdersTextAndToolEvents(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	var events []ResponseEvent
	emit := func(event ResponseEvent) error { events = append(events, event); return nil }
	role := "assistant"
	text := "hello"
	callID := "call_1"
	callType := "function"
	name := "lookup"
	firstArgs := `{"q":`
	secondArgs := `"x"}`
	chunks := []whitelabel.ChatCompletionChunk{
		{ID: "up", Object: "chat.completion.chunk", Created: 10, Model: "model-a", Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{Role: &role, Content: &text}}}},
		{ID: "up", Object: "chat.completion.chunk", Created: 10, Model: "model-a", Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{ToolCalls: []whitelabel.ChatCompletionChunkToolCall{{Index: 0, ID: &callID, Type: &callType, Function: &whitelabel.ChatCompletionChunkFunctionCall{Name: &name, Arguments: &firstArgs}}}}}}},
		{ID: "up", Object: "chat.completion.chunk", Created: 10, Model: "model-a", Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{ToolCalls: []whitelabel.ChatCompletionChunkToolCall{{Index: 0, Function: &whitelabel.ChatCompletionChunkFunctionCall{Arguments: &secondArgs}}}}}}},
	}
	for _, chunk := range chunks {
		if err := stream.Accept(chunk, emit); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Complete(emit); err != nil {
		t.Fatal(err)
	}
	want := []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.function_call_arguments.done", "response.output_item.done", "response.completed"}
	if len(events) != len(want) {
		t.Fatalf("events=%#v", events)
	}
	for index, event := range events {
		if event.Type != want[index] || event.SequenceNumber != int64(index+1) {
			t.Fatalf("event[%d]=%#v", index, event)
		}
	}
}

func TestResponsesStreamFailureIsTerminal(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	var events []ResponseEvent
	emit := func(event ResponseEvent) error { events = append(events, event); return nil }
	text := "started"
	chunk := whitelabel.ChatCompletionChunk{ID: "up", Object: "chat.completion.chunk", Created: 10, Model: "model-a", Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{Content: &text}}}}
	if err := stream.Accept(chunk, emit); err != nil {
		t.Fatal(err)
	}
	if err := stream.Failed("gateway_upstream_unavailable", emit); err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Type != "response.failed" {
		t.Fatalf("events=%#v", events)
	}
	if err := stream.Complete(emit); err == nil {
		t.Fatal("Complete succeeded after failure")
	}
}

func TestResponsesStreamFlushesArgumentsBufferedBeforeIdentity(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	var events []ResponseEvent
	emit := func(event ResponseEvent) error { events = append(events, event); return nil }
	first, second := "A", "B"
	callID, callType, name := "call_1", "function", "lookup"
	chunks := []whitelabel.ChatCompletionChunk{
		{Model: "model-a", Created: 1, Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{ToolCalls: []whitelabel.ChatCompletionChunkToolCall{{Index: 0, Function: &whitelabel.ChatCompletionChunkFunctionCall{Arguments: &first}}}}}}},
		{Model: "model-a", Created: 1, Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{ToolCalls: []whitelabel.ChatCompletionChunkToolCall{{Index: 0, ID: &callID, Type: &callType, Function: &whitelabel.ChatCompletionChunkFunctionCall{Name: &name, Arguments: &second}}}}}}},
	}
	for _, chunk := range chunks {
		if err := stream.Accept(chunk, emit); err != nil {
			t.Fatal(err)
		}
	}
	got := ""
	for _, event := range events {
		if event.Type == "response.function_call_arguments.delta" {
			got += event.Delta
		}
	}
	if got != "AB" {
		t.Fatalf("argument deltas=%q, want AB", got)
	}
}

func TestResponsesStreamValidatesFirstChunkBeforeEmitting(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	emitted := 0
	badType := "custom"
	chunk := whitelabel.ChatCompletionChunk{Model: "model-a", Created: 1, Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{ToolCalls: []whitelabel.ChatCompletionChunkToolCall{{Index: 0, Type: &badType}}}}}}
	if err := stream.Accept(chunk, func(ResponseEvent) error { emitted++; return nil }); err == nil {
		t.Fatal("invalid first chunk accepted")
	}
	if emitted != 0 || stream.Started() {
		t.Fatalf("emitted=%d started=%v", emitted, stream.Started())
	}
}
