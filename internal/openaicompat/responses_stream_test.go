package openaicompat

import (
	"strings"
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

func TestResponsesStreamRejectsCallIDReusedAcrossIndexes(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	emit := func(ResponseEvent) error { return nil }
	callID, callType, firstName, secondName := "call_1", "function", "first", "second"
	first := whitelabel.ChatCompletionChunk{Model: "model-a", Created: 1, Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{ToolCalls: []whitelabel.ChatCompletionChunkToolCall{{Index: 0, ID: &callID, Type: &callType, Function: &whitelabel.ChatCompletionChunkFunctionCall{Name: &firstName}}}}}}}
	second := whitelabel.ChatCompletionChunk{Model: "model-a", Created: 1, Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{ToolCalls: []whitelabel.ChatCompletionChunkToolCall{{Index: 1, ID: &callID, Type: &callType, Function: &whitelabel.ChatCompletionChunkFunctionCall{Name: &secondName}}}}}}}
	if err := stream.Accept(first, emit); err != nil {
		t.Fatal(err)
	}
	if err := stream.Accept(second, emit); err == nil {
		t.Fatal("duplicate call ID accepted across tool indexes")
	}
}

func TestResponsesStreamRejectsCumulativeTextOverLimit(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	emit := func(ResponseEvent) error { return nil }
	first, second := strings.Repeat("a", MaxTextContentBytes), "b"
	if err := stream.Accept(responseTextChunk(&first), emit); err != nil {
		t.Fatal(err)
	}
	if err := stream.Accept(responseTextChunk(&second), emit); err == nil {
		t.Fatal("cumulative text over limit was accepted")
	}
	if got := len(stream.text.text); got != MaxTextContentBytes {
		t.Fatalf("buffered text bytes=%d, want %d", got, MaxTextContentBytes)
	}
}

func TestResponsesStreamRejectsMoreThanMaxParallelToolStates(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	emit := func(ResponseEvent) error { return nil }
	for index := 0; index < MaxParallelCalls; index++ {
		if err := stream.Accept(responseToolChunk(index, nil), emit); err != nil {
			t.Fatalf("index=%d: %v", index, err)
		}
	}
	if err := stream.Accept(responseToolChunk(MaxParallelCalls, nil), emit); err == nil {
		t.Fatal("tool state beyond parallel limit was accepted")
	}
	if got := len(stream.tools); got != MaxParallelCalls {
		t.Fatalf("tool states=%d, want %d", got, MaxParallelCalls)
	}
}

func TestResponsesStreamRejectsSparseHugeToolIndexBeforeState(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	if err := stream.Accept(responseToolChunk(1<<30, nil), func(ResponseEvent) error { return nil }); err == nil {
		t.Fatal("sparse huge tool index was accepted")
	}
	if stream.Started() || len(stream.tools) != 0 || len(stream.output) != 0 {
		t.Fatalf("started=%v tools=%d output=%d", stream.Started(), len(stream.tools), len(stream.output))
	}
}

func TestResponsesStreamKeepsCumulativeArgumentsBounded(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	emit := func(ResponseEvent) error { return nil }
	first, second := strings.Repeat("a", MaxArgumentsBytes), "b"
	if err := stream.Accept(responseToolChunk(0, &first), emit); err != nil {
		t.Fatal(err)
	}
	if err := stream.Accept(responseToolChunk(0, &second), emit); err == nil {
		t.Fatal("cumulative arguments over limit were accepted")
	}
	if got := len(stream.tools[0].arguments); got != MaxArgumentsBytes {
		t.Fatalf("buffered argument bytes=%d, want %d", got, MaxArgumentsBytes)
	}
}

func TestResponsesStreamLimitErrorAllowsSingleHandlerFailure(t *testing.T) {
	stream := NewResponsesStream("model-a", true, sequentialIDSource())
	var events []ResponseEvent
	emit := func(event ResponseEvent) error { events = append(events, event); return nil }
	first, second := strings.Repeat("a", MaxTextContentBytes), "b"
	if err := stream.Accept(responseTextChunk(&first), emit); err != nil {
		t.Fatal(err)
	}
	if err := stream.Accept(responseTextChunk(&second), emit); err == nil {
		t.Fatal("cumulative text over limit was accepted")
	} else if stream.Started() {
		if failedErr := stream.Failed("gateway_upstream_unavailable", emit); failedErr != nil {
			t.Fatal(failedErr)
		}
	}
	if err := stream.Accept(responseTextChunk(&second), emit); err == nil {
		t.Fatal("Accept succeeded after handler failure terminal")
	}
	if err := stream.Failed("gateway_upstream_unavailable", emit); err == nil {
		t.Fatal("second handler failure terminal succeeded")
	}
	failed := 0
	for _, event := range events {
		if event.Type == "response.failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("response.failed events=%d, want 1", failed)
	}
}

func responseTextChunk(content *string) whitelabel.ChatCompletionChunk {
	return whitelabel.ChatCompletionChunk{Model: "model-a", Created: 1, Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{Content: content}}}}
}

func responseToolChunk(index int, arguments *string) whitelabel.ChatCompletionChunk {
	return whitelabel.ChatCompletionChunk{Model: "model-a", Created: 1, Choices: []whitelabel.ChatCompletionChunkChoice{{Index: 0, Delta: whitelabel.ChatCompletionChunkDelta{ToolCalls: []whitelabel.ChatCompletionChunkToolCall{{Index: index, Function: &whitelabel.ChatCompletionChunkFunctionCall{Arguments: arguments}}}}}}}
}
