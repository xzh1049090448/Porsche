package whitelabel

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/diagnostics"
)

func TestSSEObjectDiagnosticNull(t *testing.T) {
	assertObjectDiagnostic(t, `{"id":"safe","object":null}`, "empty", "null", "canonical")
}

func TestSSEObjectDiagnosticShapes(t *testing.T) {
	for _, tc := range []struct{ name, fields, decoded, shape, key string }{
		{"missing", `"nested":{"object":"SENSITIVE"}`, "empty", "missing", "none"},
		{"empty", `"object":""`, "empty", "string_empty", "canonical"},
		{"known", `"object":"chat.completion"`, "known_chat_completion", "string_nonempty", "canonical"},
		{"other", `"object":"SENSITIVE-value"`, "other", "string_nonempty", "canonical"},
		{"whitespace", `"object":" "`, "other", "string_nonempty", "canonical"},
		{"case", `"OBJECT":"chat.completion"`, "known_chat_completion", "string_nonempty", "case_variant"},
		{"mixed_case", `"oBjEcT":null`, "empty", "null", "case_variant"},
		{"escaped_key", `"ob\u006aect":""`, "empty", "string_empty", "canonical"},
		{"escaped_case", `"\u004fbject":"SENSITIVE"`, "other", "string_nonempty", "case_variant"},
		{"escaped_value", `"object":"chat.\u0063ompletion"`, "known_chat_completion", "string_nonempty", "canonical"},
		{"unknown_key", `"SENSITIVE-key":{"object":"SENSITIVE"}`, "empty", "missing", "none"},
		{"nested", `"unknown":[{"object":null}],"object":null`, "empty", "null", "canonical"},
		{"duplicate_nulls", `"object":null,"object":null`, "empty", "ambiguous", "multiple"},
		{"duplicate_known_null", `"object":"chat.completion","object":null`, "known_chat_completion", "ambiguous", "multiple"},
		{"duplicate_null_known", `"object":null,"object":"chat.completion"`, "known_chat_completion", "ambiguous", "multiple"},
		{"duplicate_other_null", `"object":"SENSITIVE","object":null`, "other", "ambiguous", "multiple"},
		{"duplicate_null_other", `"object":null,"object":"SENSITIVE"`, "other", "ambiguous", "multiple"},
		{"duplicate_empty_null", `"object":"","object":null`, "empty", "ambiguous", "multiple"},
		{"duplicate_standard_empty", `"object":"chat.completion.chunk","object":""`, "empty", "ambiguous", "multiple"},
		{"duplicate_case", `"object":"chat.completion.chunk","OBJECT":"SENSITIVE"`, "other", "ambiguous", "multiple"},
		{"duplicate_escaped", `"object":"chat.completion","ob\u006aect":null`, "known_chat_completion", "ambiguous", "multiple"},
		{"three_matches", `"OBJECT":"chat.completion","object":"SENSITIVE","Object":null`, "other", "ambiguous", "multiple"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"id":"safe",` + tc.fields + `,"choices":[{"index":0,"delta":{"content":"SENSITIVE-content","tool_calls":[{"function":{"arguments":"SENSITIVE-args"}}]}}]}`
			assertObjectDiagnostic(t, payload, tc.decoded, tc.shape, tc.key)
			_, failure := projectChatCompletionChunkDetail([]byte(payload), "model-a")
			if failure.Object == nil || string(failure.Object.DecodedKind) != tc.decoded || failure.Object.FieldShape != "unknown" || failure.Object.KeyMatch != "unknown" {
				t.Fatalf("original decoded classification=%+v", failure.Object)
			}
		})
	}
}

func TestSSEObjectDiagnosticOmittedOutsideScope(t *testing.T) {
	for _, tc := range []struct{ name, payload, reason, field string }{
		{"id_priority", `{"object":null}`, "missing_required", "id"},
		{"type_before_id", `{"object":23}`, "json_type", "object"},
		{"type_before_object", `{"id":"safe","object":null,"created":"SENSITIVE"}`, "json_type", "created"},
		{"object_type", `{"id":"safe","object":{"SENSITIVE":1}}`, "json_type", "object"},
		{"case_object_type", `{"id":"safe","OBJECT":false}`, "json_type", "object"},
		{"syntax", `{"id":"safe","object":`, "json_syntax", "chunk"},
		{"other_field", `{"id":"safe","object":"chat.completion.chunk"}`, "missing_required", "choices"},
		{"null_root", `null`, "missing_required", "id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertMalformedDetail(t, tc.payload, tc.reason, tc.field)
			ctx, tr := diagnostics.New(context.Background())
			(&WhiteLabelService{}).ProjectChatCompletionSSEContext(ctx, strings.NewReader("data: "+tc.payload+"\n\n"), "model-a", func([]byte) error { t.Fatal("invalid emission"); return nil })
			if r := diagnosticRecord(t, tr); r.MalformedChunkDetail == nil || r.MalformedChunkDetail.Object != nil {
				t.Fatalf("object detail outside scope: %+v", r.MalformedChunkDetail)
			}
		})
	}
}

func TestSSEObjectDiagnosticAcceptedAndNoTraceCompatibility(t *testing.T) {
	for _, fields := range []string{
		`"object":"chat.completion.chunk"`,
		`"object":"chat.completion.chunk","object":null`,
		`"object":null,"object":"chat.completion.chunk"`,
		`"OBJECT":"chat.completion.chunk","object":null`,
		`"object":"SENSITIVE","OBJECT":"chat.completion.chunk"`,
		`"ob\u006aect":"chat.completion.chunk","nested":{"object":"SENSITIVE"}`,
	} {
		t.Run(fields, func(t *testing.T) {
			payload := `{"id":"safe",` + fields + `,"choices":[{"index":0,"delta":{}}]}`
			body := "data: " + payload + "\n\ndata: [DONE]\n\n"
			ctx, tr := diagnostics.New(context.Background())
			var withTrace, withoutTrace bytes.Buffer
			service := &WhiteLabelService{}
			if err := service.ProjectChatCompletionSSEContext(ctx, strings.NewReader(body), "model-a", func(b []byte) error { withTrace.Write(b); return nil }); err != nil {
				t.Fatal(err)
			}
			if err := service.ProjectChatCompletionSSE(strings.NewReader(body), "model-a", func(b []byte) error { withoutTrace.Write(b); return nil }); err != nil {
				t.Fatal(err)
			}
			if withTrace.String() != withoutTrace.String() || diagnosticRecord(t, tr).MalformedChunkDetail != nil {
				t.Fatal("success trace changed output or added failure")
			}
		})
	}
	err := (&WhiteLabelService{}).ProjectChatCompletionSSE(strings.NewReader("data: {\"id\":\"safe\",\"object\":null}\n\n"), "model-a", func([]byte) error { t.Fatal("invalid emission"); return nil })
	if err == nil || err.Status != 503 || PublicError(err, "safe-id") != PublicError(ErrUpstreamUnavailable(""), "safe-id") {
		t.Fatal("no trace public error changed")
	}
}

func TestObjectFieldDetailsScannerFallback(t *testing.T) {
	for _, payload := range []string{``, `null`, `[]`, `{"object":`, `{"object":null`, `{"object":null} {}`, `{"object":null} SENSITIVE`, `{"object":false}`, `{"object":{}}`} {
		t.Run(payload, func(t *testing.T) {
			shape, key := objectFieldDetails([]byte(payload))
			if shape != "unknown" || key != "unknown" {
				t.Fatalf("scanner fallback=%s/%s", shape, key)
			}
			failure := &diagnostics.ChunkFailure{Reason: diagnostics.ChunkInvalidValue, Field: diagnostics.ChunkObject, Object: &diagnostics.ObjectDetail{DecodedKind: "known_chat_completion", FieldShape: "null", KeyMatch: "canonical"}}
			enrichObjectFailure([]byte(payload), failure)
			if *failure.Object != (diagnostics.ObjectDetail{DecodedKind: "known_chat_completion", FieldShape: "unknown", KeyMatch: "unknown"}) {
				t.Fatalf("fallback changed original decoded kind: %+v", failure.Object)
			}
		})
	}
}

func assertObjectDiagnostic(t *testing.T, payload, decoded, shape, key string) {
	t.Helper()
	assertMalformedDetail(t, payload, "invalid_value", "object")
	ctx, tr := diagnostics.New(context.Background())
	err := (&WhiteLabelService{}).ProjectChatCompletionSSEContext(ctx, strings.NewReader("data: "+payload+"\n\n"), "model-a", func([]byte) error {
		t.Fatal("malformed first chunk emitted")
		return nil
	})
	if err == nil || err.Status != 503 {
		t.Fatal("public failure changed")
	}
	var b bytes.Buffer
	tr.End(&b, 503, "safe-id")
	var record struct {
		Detail struct {
			Object map[string]string `json:"object_detail"`
		} `json:"malformed_chunk_detail"`
	}
	if err := json.Unmarshal(b.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"decoded_kind": decoded, "field_shape": shape, "key_match": key}
	if !reflect.DeepEqual(record.Detail.Object, want) {
		t.Fatalf("object_detail=%v want=%v", record.Detail.Object, want)
	}
	if strings.Contains(b.String(), "SENSITIVE") {
		t.Fatal("sensitive object diagnostics")
	}
}
