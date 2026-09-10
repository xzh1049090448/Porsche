package whitelabel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/diagnostics"
)

const typedChunk = `{"id":"safe","object":"chat.completion.chunk","created":1,"model":"upstream-secret","choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null}]}`
const typedUsageChunk = `{"id":"safe","object":"chat.completion.chunk","created":2,"model":"upstream-secret","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`

func TestConsumeChatCompletionSSEContextProjectsTypedChunks(t *testing.T) {
	input := strings.Join([]string{
		"data: " + typedChunk + "\n\n",
		"data: " + typedUsageChunk + "\n\n",
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
		t.Fatalf("ConsumeChatCompletionSSEContext() chunks = %#v", chunks)
	}
	if chunks[0].Model != "model-a" || chunks[0].Choices[0].Delta.Content == nil || *chunks[0].Choices[0].Delta.Content != "A" {
		t.Fatalf("first chunk = %#v", chunks[0])
	}
	if chunks[1].Model != "model-a" || chunks[1].Usage == nil || chunks[1].Usage.TotalTokens != 3 {
		t.Fatalf("usage chunk = %#v", chunks[1])
	}
}

func TestConsumeChatCompletionSSEContextHandlesFragmentedInput(t *testing.T) {
	reader := io.MultiReader(
		strings.NewReader("da"),
		strings.NewReader("ta: "+typedChunk[:31]),
		strings.NewReader(typedChunk[31:]+"\n"),
		strings.NewReader("\ndata: [DO"),
		strings.NewReader("NE]\n\n"),
	)
	var chunks []ChatCompletionChunk
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(context.Background(), reader, "model-a", func(chunk ChatCompletionChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil || len(chunks) != 1 || chunks[0].Model != "model-a" {
		t.Fatalf("ConsumeChatCompletionSSEContext() chunks=%#v err=%v", chunks, err)
	}
}

func TestConsumeChatCompletionSSEContextProcessesMultipleFramesFromOneRead(t *testing.T) {
	input := "data: " + typedChunk + "\n\ndata: " + typedUsageChunk + "\n\ndata: [DONE]\n\n"
	count := 0
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(context.Background(), strings.NewReader(input), "model-a", func(ChatCompletionChunk) error {
		count++
		return nil
	})
	if err != nil || count != 2 {
		t.Fatalf("ConsumeChatCompletionSSEContext() count=%d err=%v", count, err)
	}
}

func TestConsumeChatCompletionSSEContextRequiresExactTerminalDone(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "premature EOF", body: "data: " + typedChunk + "\n\n"},
		{name: "done suffix", body: "data: [DONE] \n\n"},
		{name: "malformed JSON", body: "data: {\"id\":\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(context.Background(), strings.NewReader(tc.body), "model-a", func(ChatCompletionChunk) error { return nil }); err == nil {
				t.Fatal("ConsumeChatCompletionSSEContext() returned success")
			}
		})
	}
}

func TestConsumeChatCompletionSSEContextClassifiesCallbackFailureAsWrite(t *testing.T) {
	ctx, trace := diagnostics.New(context.Background())
	sentinel := errors.New("callback failed")
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(ctx, strings.NewReader("data: "+typedChunk+"\n\ndata: [DONE]\n\n"), "model-a", func(ChatCompletionChunk) error {
		return sentinel
	})
	if err == nil || err.Detail != "stream write failed" {
		t.Fatalf("ConsumeChatCompletionSSEContext() error = %#v", err)
	}
	record := diagnosticRecord(t, trace)
	if record.Stages[diagnostics.Stream].Reason != diagnostics.Write {
		t.Fatalf("stream reason = %s", record.Stages[diagnostics.Stream].Reason)
	}
}

func TestConsumeChatCompletionSSEContextRejectsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, trace := diagnostics.New(ctx)
	calls := 0
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(ctx, strings.NewReader("data: "+typedChunk+"\n\ndata: [DONE]\n\n"), "model-a", func(ChatCompletionChunk) error {
		calls++
		return nil
	})
	if err == nil || err.Detail != "stream read failed" || calls != 0 {
		t.Fatalf("ConsumeChatCompletionSSEContext() calls=%d error=%#v", calls, err)
	}
	record := diagnosticRecord(t, trace)
	if record.Stages[diagnostics.Stream].Reason != diagnostics.Canceled {
		t.Fatalf("stream reason = %s", record.Stages[diagnostics.Stream].Reason)
	}
}

type cancelOnReadReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r cancelOnReadReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if n > 0 {
		r.cancel()
	}
	return n, err
}

func TestConsumeChatCompletionSSEContextChecksCancellationAfterRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := cancelOnReadReader{
		reader: strings.NewReader("data: " + typedChunk + "\n\ndata: [DONE]\n\n"),
		cancel: cancel,
	}
	calls := 0
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(ctx, reader, "model-a", func(ChatCompletionChunk) error {
		calls++
		return nil
	})
	if err == nil || err.Detail != "stream read failed" || calls != 0 {
		t.Fatalf("ConsumeChatCompletionSSEContext() calls=%d error=%#v", calls, err)
	}
}

func TestConsumeChatCompletionSSEContextStopsCallbacksAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := "data: " + typedChunk + "\n\ndata: " + typedUsageChunk + "\n\ndata: [DONE]\n\n"
	calls := 0
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(ctx, strings.NewReader(input), "model-a", func(ChatCompletionChunk) error {
		calls++
		cancel()
		return nil
	})
	if err == nil || err.Detail != "stream read failed" || calls != 1 {
		t.Fatalf("ConsumeChatCompletionSSEContext() calls=%d error=%#v", calls, err)
	}
}

func TestConsumeChatCompletionSSEContextRejectsInvalidUTF8(t *testing.T) {
	payload := append([]byte(`data: {"id":"safe","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"delta":{"content":"`), 0xff)
	payload = append(payload, []byte("\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")...)
	ctx, trace := diagnostics.New(context.Background())
	calls := 0
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(ctx, bytes.NewReader(payload), "model-a", func(ChatCompletionChunk) error {
		calls++
		return nil
	})
	if err == nil || err.Detail != "malformed chat completion chunk" || calls != 0 {
		t.Fatalf("ConsumeChatCompletionSSEContext() calls=%d error=%#v", calls, err)
	}
	record := diagnosticRecord(t, trace)
	if record.Stages[diagnostics.Stream].Reason != diagnostics.Malformed || record.MalformedChunkDetail == nil || record.MalformedChunkDetail.Reason != diagnostics.ChunkJSONSyntax || record.MalformedChunkDetail.Field != diagnostics.ChunkRoot {
		t.Fatalf("record = %#v", record)
	}
}

func TestProjectChatCompletionSSERequiresTerminalDoneFrame(t *testing.T) {
	var emitted bytes.Buffer
	err := (&WhiteLabelService{}).ProjectChatCompletionSSE(
		bytes.NewBufferString("data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"},\"finish_reason\":null}]}\n\n"),
		"model-a",
		func(frame []byte) error {
			_, writeErr := emitted.Write(frame)
			return writeErr
		},
	)
	if err == nil {
		t.Fatal("EOF after a chunk without [DONE] returned success")
	}
	if got := emitted.String(); got != "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"},\"finish_reason\":null}]}\n\n" {
		t.Fatalf("emitted = %q", got)
	}
}

func TestProjectChatCompletionSSEAcceptsExactTerminalDoneFrame(t *testing.T) {
	var emitted bytes.Buffer
	err := (&WhiteLabelService{}).ProjectChatCompletionSSE(
		bytes.NewBufferString("data: [DONE]\n\n"),
		"model-a",
		func(frame []byte) error {
			_, writeErr := emitted.Write(frame)
			return writeErr
		},
	)
	if err != nil {
		t.Fatalf("exact [DONE] returned error: %v", err)
	}
	if got := emitted.String(); got != "data: [DONE]\n\n" {
		t.Fatalf("emitted = %q", got)
	}
}

func TestProjectChatCompletionSSEPreservesProjectedBytesAndSingleDone(t *testing.T) {
	input := "data: " + typedChunk + "\n\ndata: " + typedUsageChunk + "\n\ndata: [DONE]\n\ndata: [DONE]\n\n"
	var emitted bytes.Buffer
	err := (&WhiteLabelService{}).ProjectChatCompletionSSE(strings.NewReader(input), "model-a", func(frame []byte) error {
		_, writeErr := emitted.Write(frame)
		return writeErr
	})
	if err != nil {
		t.Fatalf("ProjectChatCompletionSSE() error = %v", err)
	}
	want := "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"A\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":2,\"model\":\"model-a\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	if got := emitted.String(); got != want {
		t.Fatalf("emitted = %q, want %q", got, want)
	}
}

func TestProjectChatCompletionSSEContextClassifiesTerminalWriteFailure(t *testing.T) {
	ctx, trace := diagnostics.New(context.Background())
	err := (&WhiteLabelService{}).ProjectChatCompletionSSEContext(ctx, strings.NewReader("data: [DONE]\n\n"), "model-a", func([]byte) error {
		return errors.New("terminal write failed")
	})
	if err == nil || err.Detail != "stream write failed" {
		t.Fatalf("ProjectChatCompletionSSEContext() error = %#v", err)
	}
	record := diagnosticRecord(t, trace)
	if record.Stages[diagnostics.Stream].Reason != diagnostics.Write {
		t.Fatalf("stream reason = %s", record.Stages[diagnostics.Stream].Reason)
	}
}
