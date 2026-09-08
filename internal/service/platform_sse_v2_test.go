package service

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const sseV2GenerationID = "550e8400-e29b-41d4-a716-446655440000"

func TestPlatformSSEV2ModelIdentifierMatchesExistingPersistenceColumns(t *testing.T) {
	if !platformSSEV2ModelIdentifier(strings.Repeat("m", 128)) {
		t.Fatal("128-byte model identifier was rejected")
	}
	if platformSSEV2ModelIdentifier(strings.Repeat("m", 129)) {
		t.Fatal("129-byte model identifier would overflow existing model columns")
	}
	if !platformSSEV2ModelIdentifier(strings.Repeat("界", 42)) || platformSSEV2ModelIdentifier(strings.Repeat("界", 43)) {
		t.Fatal("model identifier limit must be measured in UTF-8 bytes")
	}
	if !platformSSEV2Identifier(strings.Repeat("i", 255)) {
		t.Fatal("model-specific bound unexpectedly narrowed generic opaque identifiers")
	}
}

func TestPlatformSSEV2IdentifiersRejectInvalidUTF8(t *testing.T) {
	invalid := string([]byte{0xff})
	if platformSSEV2Identifier(invalid) || platformSSEV2ModelIdentifier(invalid) {
		t.Fatal("malformed UTF-8 identifier was accepted")
	}
	if encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{invalid}); err == nil || encoder != nil {
		t.Fatal("encoder accepted malformed UTF-8 model before dependencies")
	}
}

func TestPlatformSSEV2EncoderFramesSanitizedSingleStream(t *testing.T) {
	encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if err != nil {
		t.Fatal(err)
	}
	frames := []struct {
		got  []byte
		want string
	}{
		{encoder.Meta("conversation-1"), "event: meta\ndata: {\"schema\":\"platform-chat-sse.v2\",\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"conversation_guid\":\"conversation-1\",\"models\":[\"model-a\"]}\n\n"},
		{encoder.Delta("model-a", 1, "hello"), "event: delta\ndata: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"model\":\"model-a\",\"seq\":1,\"delta\":\"hello\"}\n\n"},
		{encoder.ModelDone("model-a", 1), "event: model_done\ndata: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"model\":\"model-a\",\"last_seq\":1}\n\n"},
		{encoder.DoneSingle("conversation-1", 3, 30), "event: done\ndata: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"status\":\"completed\",\"conversation_guid\":\"conversation-1\",\"tokens\":3,\"total_tokens_used\":30}\n\n"},
	}
	for _, frame := range frames {
		if frame.got == nil {
			t.Fatalf("frame error: %v", encoder.Err())
		}
		if got := string(frame.got); got != frame.want {
			t.Fatalf("frame = %q\nwant  = %q", got, frame.want)
		}
	}
}

func TestPlatformSSEV2EncoderAllowsInterleavedCompareModels(t *testing.T) {
	encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a", "model-b"})
	if err != nil {
		t.Fatal(err)
	}
	if encoder.Meta("conversation-1") == nil || encoder.Delta("model-b", 1, "b") == nil || encoder.Delta("model-a", 1, "a") == nil || encoder.ModelDone("model-a", 1) == nil || encoder.ModelError("model-b", "untrusted upstream detail", "request-1") == nil {
		t.Fatalf("interleaved frame failed: %v", encoder.Err())
	}
	want := "event: done\ndata: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"status\":\"completed\",\"conversation_guid\":\"conversation-1\",\"total_tokens_used\":30,\"models\":{\"model-a\":{\"status\":\"completed\",\"tokens\":3},\"model-b\":{\"status\":\"failed\",\"code\":\"upstream_error\"}}}\n\n"
	if got := string(encoder.DoneCompare("conversation-1", 30, map[string]int64{"model-a": 3})); got != want {
		t.Fatalf("compare done = %q\nwant = %q (err=%v)", got, want, encoder.Err())
	}
}

func TestPlatformSSEV2EncoderFailsClosedOnOrderingAndTerminalViolations(t *testing.T) {
	encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := encoder.Delta("model-a", 1, "before-meta"); got != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2MetaRequired) {
		t.Fatalf("delta before meta = %q, err=%v", got, encoder.Err())
	}
	if got := encoder.Meta("conversation-1"); got != nil {
		t.Fatalf("encoder emitted after failure: %q", got)
	}

	encoder, _ = NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if encoder.Meta("conversation-1") == nil || encoder.Delta("model-a", 2, "gap") != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2Sequence) {
		t.Fatalf("sequence violation did not fail closed: %v", encoder.Err())
	}
}

func TestPlatformSSEV2EncoderRejectsEmptyDeltaAndPrematureDone(t *testing.T) {
	encoder, _ := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if encoder.Meta("conversation-1") == nil || encoder.Delta("model-a", 1, "") != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2InvalidEvent) {
		t.Fatalf("empty delta did not fail closed: %v", encoder.Err())
	}

	encoder, _ = NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a", "model-b"})
	if encoder.Meta("conversation-1") == nil || encoder.ModelDone("model-a", 0) == nil || encoder.DoneCompare("conversation-1", 0, map[string]int64{"model-a": 0}) != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2ModelsRunning) {
		t.Fatalf("premature done did not fail closed: %v", encoder.Err())
	}
}

func TestPlatformSSEV2EncoderFailsClosedForDuplicateUnknownAndPostTerminalEvents(t *testing.T) {
	newEncoder := func() *PlatformSSEV2Encoder {
		encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
		if err != nil {
			t.Fatal(err)
		}
		if encoder.Meta("conversation-1") == nil {
			t.Fatal("meta failed")
		}
		return encoder
	}

	encoder := newEncoder()
	if encoder.Meta("conversation-1") != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2InvalidEvent) || encoder.Delta("model-a", 1, "after") != nil {
		t.Fatalf("duplicate meta was not terminal: %v", encoder.Err())
	}

	encoder = newEncoder()
	if encoder.Delta("unknown-model", 1, "x") != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2InvalidEvent) {
		t.Fatalf("unknown model was accepted: %v", encoder.Err())
	}

	encoder = newEncoder()
	if encoder.ModelDone("model-a", 0) == nil || encoder.Delta("model-a", 1, "after done") != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2InvalidEvent) {
		t.Fatalf("post-model-terminal event was accepted: %v", encoder.Err())
	}

	encoder = newEncoder()
	if encoder.ModelDone("model-a", 0) == nil || encoder.DoneSingle("conversation-1", 0, 0) == nil || encoder.DoneSingle("conversation-1", 0, 0) != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2Terminal) {
		t.Fatalf("duplicate global terminal was accepted: %v", encoder.Err())
	}
}

func TestPlatformSSEV2EncoderSanitizesErrorsAndNeverEmitsLegacyDone(t *testing.T) {
	encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if err != nil {
		t.Fatal(err)
	}
	if encoder.Meta("conversation-1") == nil {
		t.Fatal("meta failed")
	}
	frame := string(encoder.ModelError("model-a", "https://internal.example/upstream?secret=leak", "request-1"))
	if strings.Contains(frame, "internal.example") || strings.Contains(frame, "secret=") || strings.Contains(frame, "[DONE]") || !strings.Contains(frame, `"code":"upstream_error"`) {
		t.Fatalf("model error leaked or used legacy terminal: %s", frame)
	}
	if encoder.DoneSingle("conversation-1", 0, 0) != nil {
		t.Fatal("encoder emitted done after a failed model")
	}

	encoder, _ = NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if encoder.Meta("conversation-1") == nil {
		t.Fatal("meta failed")
	}
	frame = string(encoder.Error("unknown provider response", "request-1"))
	if strings.Contains(frame, "provider response") || strings.Contains(frame, "[DONE]") || !strings.Contains(frame, `"code":"upstream_error"`) {
		t.Fatalf("global error leaked or used legacy terminal: %s", frame)
	}
}

func TestPlatformSSEV2HeadersAreExact(t *testing.T) {
	headers := make(http.Header)
	SetPlatformSSEV2Headers(headers)
	for key, want := range map[string]string{
		"Content-Type":      "text/event-stream; charset=utf-8",
		"Cache-Control":     "no-cache, no-transform",
		"X-Accel-Buffering": "no",
	} {
		if got := headers.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestPlatformSSEV2EncoderRejectsNonCanonicalIdentifiersAndModelSets(t *testing.T) {
	invalidGenerationIDs := []string{
		"", "550E8400-E29B-41D4-A716-446655440000", "550e8400-e29b-41d4-a716-44665544000z",
	}
	for _, generationID := range invalidGenerationIDs {
		if encoder, err := NewPlatformSSEV2Encoder(generationID, []string{"model-a"}); err == nil || encoder != nil {
			t.Fatalf("NewPlatformSSEV2Encoder(%q) accepted invalid generation ID", generationID)
		}
	}
	for _, models := range [][]string{
		{" "}, {"model-a", "model-a"}, {"model-a", "model-b", "model-c", "model-d"},
		{strings.Repeat("m", platformSSEV2MaxIdentifierBytes+1)},
	} {
		if encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, models); err == nil || encoder != nil {
			t.Fatalf("NewPlatformSSEV2Encoder(%q) accepted invalid models", models)
		}
	}

	encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if err != nil {
		t.Fatal(err)
	}
	if frame := encoder.Meta(" \t "); frame != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2InvalidEvent) {
		t.Fatalf("blank conversation GUID = %q, err=%v", frame, encoder.Err())
	}
}

func TestPlatformSSEV2EncoderBoundsCountersAndDeltaBytes(t *testing.T) {
	encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if err != nil {
		t.Fatal(err)
	}
	if encoder.Meta("conversation-1") == nil {
		t.Fatal("meta failed")
	}
	if frame := encoder.Delta("model-a", 1, strings.Repeat("x", platformSSEV2MaxDeltaBytes+1)); frame != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2InvalidEvent) {
		t.Fatalf("oversized delta = %q, err=%v", frame, encoder.Err())
	}

	encoder, _ = NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if encoder.Meta("conversation-1") == nil {
		t.Fatal("meta failed")
	}
	encoder.states["model-a"].nextSeq = platformSSEV2MaxSafeInteger
	if encoder.Delta("model-a", platformSSEV2MaxSafeInteger, "x") == nil {
		t.Fatalf("max-safe seq rejected: %v", encoder.Err())
	}
	if encoder.ModelDone("model-a", platformSSEV2MaxSafeInteger) == nil || encoder.DoneSingle("conversation-1", platformSSEV2MaxSafeInteger+1, 0) != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2InvalidEvent) {
		t.Fatalf("unsafe tokens were accepted: %v", encoder.Err())
	}
}

func TestPlatformSSEV2EncoderSanitizesRequestIDsAndRequiresExactCompareTokenMap(t *testing.T) {
	for _, requestID := range []string{"https://internal.example/?secret=x", "req\nsecret", strings.Repeat("x", 129)} {
		encoder, _ := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
		if encoder.Meta("conversation-1") == nil {
			t.Fatal("meta failed")
		}
		if frame := string(encoder.Error("timeout", requestID)); strings.Contains(frame, "request_id") || strings.Contains(frame, requestID) {
			t.Fatalf("unsafe request ID leaked: %q", frame)
		}
	}

	encoder, _ := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a", "model-b"})
	if encoder.Meta("conversation-1") == nil || encoder.ModelDone("model-a", 0) == nil || encoder.ModelError("model-b", "timeout", "request-1") == nil {
		t.Fatalf("terminal setup failed: %v", encoder.Err())
	}
	if frame := encoder.DoneCompare("conversation-1", 0, map[string]int64{"model-a": 0, "unexpected": 1}); frame != nil || !errors.Is(encoder.Err(), ErrPlatformSSEV2InvalidEvent) {
		t.Fatalf("extra model token was accepted: %q, err=%v", frame, encoder.Err())
	}
}

func TestPlatformSSEV2EncoderConcurrentDeltaAndTerminalIsRaceFree(t *testing.T) {
	encoder, err := NewPlatformSSEV2Encoder(sseV2GenerationID, []string{"model-a"})
	if err != nil {
		t.Fatal(err)
	}
	if encoder.Meta("conversation-1") == nil {
		t.Fatal("meta failed")
	}

	var workers sync.WaitGroup
	for index := 0; index < 32; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_ = encoder.Delta("model-a", 1, "delta")
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		_ = encoder.ModelDone("model-a", 1)
	}()
	workers.Wait()
	_ = encoder.Err()
}
