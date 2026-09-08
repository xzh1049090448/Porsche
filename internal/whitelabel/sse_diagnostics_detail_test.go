package whitelabel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/diagnostics"
)

func detailChunk(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	v := map[string]any{"id": "SENSITIVE-id", "object": "chat.completion.chunk", "created": 0, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "SENSITIVE-content"}}}}
	edit(v)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func detailChoice(v map[string]any) map[string]any { return v["choices"].([]any)[0].(map[string]any) }
func TestMalformedChunkDetailClassifiesActualRejection(t *testing.T) {
	cases := []struct {
		name, reason, field string
		edit                func(map[string]any)
	}{
		{"id_missing", "missing_required", "id", func(v map[string]any) { delete(v, "id") }},
		{"id_type", "json_type", "id", func(v map[string]any) { v["id"] = 5 }},
		{"object", "invalid_value", "object", func(v map[string]any) { v["object"] = "SENSITIVE" }},
		{"created", "negative_value", "created", func(v map[string]any) { v["created"] = -1 }},
		{"created_type", "json_type", "created", func(v map[string]any) { v["created"] = "SENSITIVE" }},
		{"usage_type", "json_type", "usage", func(v map[string]any) { v["usage"] = "SENSITIVE" }},
		{"prompt_negative", "negative_value", "usage.prompt_tokens", func(v map[string]any) { v["usage"] = map[string]any{"prompt_tokens": -1} }},
		{"completion_negative", "negative_value", "usage.completion_tokens", func(v map[string]any) { v["usage"] = map[string]any{"completion_tokens": -1} }},
		{"total_negative", "negative_value", "usage.total_tokens", func(v map[string]any) { v["usage"] = map[string]any{"total_tokens": -1} }},
		{"usage_count_type", "json_type", "usage.total_tokens", func(v map[string]any) { v["usage"] = map[string]any{"total_tokens": "SENSITIVE"} }},
		{"empty_choices", "missing_required", "choices", func(v map[string]any) { v["choices"] = []any{} }},
		{"choices_type", "json_type", "choices", func(v map[string]any) { v["choices"] = "SENSITIVE" }},
		{"choice_index", "negative_value", "choices[].index", func(v map[string]any) { detailChoice(v)["index"] = -1 }},
		{"choice_index_type", "json_type", "choices[].index", func(v map[string]any) { detailChoice(v)["index"] = "SENSITIVE" }},
		{"finish_type", "json_type", "choices[].finish_reason", func(v map[string]any) { detailChoice(v)["finish_reason"] = 5 }},
		{"delta_missing", "missing_required", "choices[].delta", func(v map[string]any) { delete(detailChoice(v), "delta") }},
		{"delta_null", "invalid_shape", "choices[].delta", func(v map[string]any) { detailChoice(v)["delta"] = nil }},
		{"delta_type", "json_type", "choices[].delta", func(v map[string]any) { detailChoice(v)["delta"] = []any{} }},
		{"content_type", "json_type", "choices[].delta.content", func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{"content": 3} }},
		{"role_type", "json_type", "choices[].delta.role", func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{"role": 3} }},
		{"refusal_type", "json_type", "choices[].delta.refusal", func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{"refusal": 3} }},
		{"tools_type", "json_type", "choices[].delta.tool_calls", func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{"tool_calls": "SENSITIVE"} }},
	}
	for _, tc := range []struct {
		name, reason, field string
		call                map[string]any
	}{
		{"tool_index", "negative_value", "choices[].delta.tool_calls[].index", map[string]any{"index": -1}},
		{"tool_index_type", "json_type", "choices[].delta.tool_calls[].index", map[string]any{"index": "SENSITIVE"}},
		{"tool_id_type", "json_type", "choices[].delta.tool_calls[].id", map[string]any{"id": 3}},
		{"tool_type_type", "json_type", "choices[].delta.tool_calls[].type", map[string]any{"type": 3}},
		{"function_type", "json_type", "choices[].delta.tool_calls[].function", map[string]any{"function": "SENSITIVE"}},
		{"function_name", "json_type", "choices[].delta.tool_calls[].function.name", map[string]any{"function": map[string]any{"name": 3}}},
		{"function_arguments", "json_type", "choices[].delta.tool_calls[].function.arguments", map[string]any{"function": map[string]any{"arguments": 3}}},
	} {
		tc := tc
		cases = append(cases, struct {
			name, reason, field string
			edit                func(map[string]any)
		}{tc.name, tc.reason, tc.field, func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{"tool_calls": []any{tc.call}} }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertMalformedDetail(t, detailChunk(t, tc.edit), tc.reason, tc.field) })
	}
	t.Run("syntax", func(t *testing.T) { assertMalformedDetail(t, `{"SENSITIVE":`, "json_syntax", "chunk") })
	t.Run("root_type", func(t *testing.T) { assertMalformedDetail(t, `["SENSITIVE"]`, "json_type", "chunk") })
	t.Run("priority_root", func(t *testing.T) {
		assertMalformedDetail(t, detailChunk(t, func(v map[string]any) { v["id"] = ""; v["created"] = -1 }), "missing_required", "id")
	})
	t.Run("priority_delta_before_index", func(t *testing.T) {
		assertMalformedDetail(t, detailChunk(t, func(v map[string]any) { detailChoice(v)["index"] = -1; detailChoice(v)["delta"] = nil }), "invalid_shape", "choices[].delta")
	})
}
func assertMalformedDetail(t *testing.T, payload, reason, field string) {
	t.Helper()
	if _, err := projectChatCompletionChunk([]byte(payload), "model-a"); err != errMalformedCompletion {
		t.Fatalf("legacy sentinel changed: %v", err)
	}
	ctx, tr := diagnostics.New(context.Background())
	err := (&WhiteLabelService{}).ProjectChatCompletionSSEContext(ctx, strings.NewReader("data: "+payload+"\n\n"), "model-a", func([]byte) error { t.Fatal("malformed first chunk emitted"); return nil })
	if err == nil || err.Status != 503 || err.Code != CodeGatewayUpstreamUnavailable {
		t.Fatal("public contract changed")
	}
	var b bytes.Buffer
	tr.End(&b, 503, "safe-id")
	if strings.Contains(b.String(), "SENSITIVE") {
		t.Fatal("sensitive diagnostic")
	}
	var record struct {
		Detail struct{ Reason, Field string }     `json:"malformed_chunk_detail"`
		Stages map[string]struct{ Reason string } `json:"stages"`
	}
	if err := json.Unmarshal(b.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.Detail.Reason != reason || record.Detail.Field != field || record.Stages["sse_stream"].Reason != "malformed_chunk" {
		t.Fatalf("detail=%+v want=%s/%s", record.Detail, reason, field)
	}
}

// The output digests are also run against the unchanged HEAD SSE implementation
// using go test -overlay, so valid fixtures prove byte-for-byte compatibility.
func TestSSEAcceptedChunkCompatibility(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"content", func(map[string]any) {}},
		{"created_missing", func(v map[string]any) { delete(v, "created") }},
		{"usage_null", func(v map[string]any) { v["usage"] = nil }},
		{"usage_only", func(v map[string]any) {
			v["choices"] = []any{}
			v["usage"] = map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}
		}},
		{"usage_empty", func(v map[string]any) { v["choices"] = nil; v["usage"] = map[string]any{} }},
		{"empty_delta", func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{} }},
		{"null_content", func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{"content": nil} }},
		{"role_only", func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{"role": "assistant"} }},
		{"finish_only", func(v map[string]any) {
			detailChoice(v)["delta"] = map[string]any{}
			detailChoice(v)["finish_reason"] = "stop"
		}},
		{"tool_function", func(v map[string]any) {
			detailChoice(v)["delta"] = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "SENSITIVE-tool", "type": "function", "function": map[string]any{"name": "SENSITIVE-function", "arguments": "SENSITIVE-arguments"}}}}
		}},
		{"tool_partial_function", func(v map[string]any) {
			detailChoice(v)["delta"] = map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"arguments": "SENSITIVE"}}, map[string]any{"function": nil}}}
		}},
		{"tools_null", func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{"tool_calls": nil} }},
		{"tools_empty", func(v map[string]any) { detailChoice(v)["delta"] = map[string]any{"tool_calls": []any{}} }},
		{"ignored_fields", func(v map[string]any) { v["SENSITIVE-field-name"] = "SENSITIVE-value" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := detailChunk(t, tc.edit)
			wire := "data: " + payload + "\n\ndata: [DONE]\n\n"
			ctx, tr := diagnostics.New(context.Background())
			var withTrace, withoutTrace bytes.Buffer
			s := &WhiteLabelService{}
			if err := s.ProjectChatCompletionSSEContext(ctx, strings.NewReader(wire), "model-a", func(frame []byte) error { _, err := withTrace.Write(frame); return err }); err != nil {
				t.Fatal("accepted fixture rejected")
			}
			if err := s.ProjectChatCompletionSSE(strings.NewReader(wire), "model-a", func(frame []byte) error { _, err := withoutTrace.Write(frame); return err }); err != nil {
				t.Fatal("legacy wrapper rejected")
			}
			if !bytes.Equal(withTrace.Bytes(), withoutTrace.Bytes()) {
				t.Fatal("tracing changed emitted bytes")
			}
			var log bytes.Buffer
			tr.End(&log, 200, "safe")
			if strings.Contains(log.String(), "malformed_chunk_detail") || strings.Contains(log.String(), "SENSITIVE") {
				t.Fatal("unexpected detail/content")
			}
			t.Logf("COMPAT %s %x", tc.name, sha256.Sum256(withTrace.Bytes()))
		})
	}
	for _, wire := range []string{"data: [DONE]\n\n", "data: [DONE]\r\n\r\n", "data: {\n" + "data: \"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{}}]}\n\ndata: [DONE]\n\n"} {
		var output bytes.Buffer
		if err := (&WhiteLabelService{}).ProjectChatCompletionSSE(strings.NewReader(wire), "model-a", func(frame []byte) error { _, err := output.Write(frame); return err }); err != nil {
			t.Fatal("wire compatibility changed")
		}
		t.Logf("COMPAT wire %x", sha256.Sum256(output.Bytes()))
	}
}
func TestMalformedChunkDetailsDoNotLeakAfterFirstFrame(t *testing.T) {
	first := detailChunk(t, func(map[string]any) {})
	bad := detailChunk(t, func(v map[string]any) {
		detailChoice(v)["delta"] = map[string]any{"content": map[string]any{"SENSITIVE-field": "SENSITIVE-value"}}
	})
	ctx, tr := diagnostics.New(context.Background())
	frames := 0
	err := (&WhiteLabelService{}).ProjectChatCompletionSSEContext(ctx, strings.NewReader("data: "+first+"\n\ndata: "+bad+"\n\n"), "model-a", func([]byte) error { frames++; diagnostics.From(ctx).Mark(diagnostics.FirstFrame); return nil })
	if err == nil || frames != 1 {
		t.Fatal("stream behavior changed")
	}
	r := diagnosticRecord(t, tr)
	if !r.FirstFrameEmitted || r.MalformedChunkDetail == nil || r.MalformedChunkDetail.Field != diagnostics.ChunkDeltaContent || r.Stages[diagnostics.Stream].Reason != diagnostics.Malformed {
		t.Fatal("missing post-frame detail")
	}
}

func TestChunkDetailHelpersKeepSentinelsAndScrubDecoderMetadata(t *testing.T) {
	for _, raw := range []string{"{", "[]", "null"} {
		if _, err := projectChunkDelta(json.RawMessage(raw)); err != errMalformedCompletion {
			t.Fatal("delta sentinel changed")
		}
	}
	for _, raw := range []string{"{", "{}", `[{"function":[]}]`} {
		if _, err := projectChunkToolCalls(json.RawMessage(raw)); err != errMalformedCompletion {
			t.Fatal("tool sentinel changed")
		}
	}
	failure := chunkJSONFailure(&json.UnmarshalTypeError{Field: "SENSITIVE-field", Value: "SENSITIVE-value", Type: reflect.TypeOf(0)}, diagnostics.ChunkRoot)
	if failure.Reason != diagnostics.ChunkJSONType || failure.Field != diagnostics.ChunkFieldUnknown {
		t.Fatal("decoder metadata bypassed allowlist")
	}
	failure = chunkJSONFailure(errors.New("SENSITIVE error"), diagnostics.ChunkRoot)
	if failure.Reason != diagnostics.ChunkUnknown || failure.Field != diagnostics.ChunkFieldUnknown {
		t.Fatal("unknown decoder failure must stay unknown")
	}
}
