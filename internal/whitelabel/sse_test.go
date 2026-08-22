package whitelabel

import (
	"bytes"
	"testing"
)

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
