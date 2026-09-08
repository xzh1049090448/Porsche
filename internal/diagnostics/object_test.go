package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func objectRecord(t *testing.T, tr *Trace) Record {
	t.Helper()
	var b bytes.Buffer
	tr.End(&b, 503, "safe")
	if strings.Contains(b.String(), "SENSITIVE") {
		t.Fatal("object entry leaked caller text")
	}
	var r Record
	if err := json.Unmarshal(b.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestObjectDiagnosticEntryWhitelistAndCopy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input, want ObjectDetail
	}{
		{"valid", ObjectDetail{"empty", "null", "canonical"}, ObjectDetail{"empty", "null", "canonical"}},
		{"unknown_decoded", ObjectDetail{"SENSITIVE-decoded", "null", "canonical"}, ObjectDetail{"unknown", "null", "canonical"}},
		{"unknown_shape", ObjectDetail{"other", "SENSITIVE-shape", "multiple"}, ObjectDetail{"other", "unknown", "multiple"}},
		{"unknown_key", ObjectDetail{"known_chat_completion", "string_nonempty", "SENSITIVE-key"}, ObjectDetail{"known_chat_completion", "string_nonempty", "unknown"}},
		{"zero", ObjectDetail{}, ObjectDetail{"unknown", "unknown", "unknown"}},
		{"all_unknown", ObjectDetail{"unknown", "unknown", "unknown"}, ObjectDetail{"unknown", "unknown", "unknown"}},
		{"missing", ObjectDetail{"empty", "missing", "none"}, ObjectDetail{"empty", "missing", "none"}},
		{"empty_case", ObjectDetail{"empty", "string_empty", "case_variant"}, ObjectDetail{"empty", "string_empty", "case_variant"}},
		{"ambiguous", ObjectDetail{"other", "ambiguous", "multiple"}, ObjectDetail{"other", "ambiguous", "multiple"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, tr := New(context.Background())
			tr.MalformedChunk(ChunkInvalidValue, ChunkObject, &tc.input)
			tc.input = ObjectDetail{"SENSITIVE-mutated", "SENSITIVE-mutated", "SENSITIVE-mutated"}
			r := objectRecord(t, tr)
			if r.MalformedChunkDetail.Object == nil || *r.MalformedChunkDetail.Object != tc.want {
				t.Fatalf("object=%+v want=%+v", r.MalformedChunkDetail.Object, tc.want)
			}
		})
	}
	From(context.Background()).MalformedChunk(ChunkInvalidValue, ChunkObject, &ObjectDetail{"SENSITIVE", "SENSITIVE", "SENSITIVE"})
}

func TestObjectDiagnosticEntryScopeAndLegacyCalls(t *testing.T) {
	for _, tc := range []struct {
		reason ChunkReason
		field  ChunkField
	}{
		{ChunkJSONType, ChunkObject}, {ChunkInvalidValue, ChunkID}, {ChunkReason("SENSITIVE"), ChunkObject}, {ChunkInvalidValue, ChunkField("SENSITIVE")},
	} {
		_, tr := New(context.Background())
		tr.MalformedChunk(tc.reason, tc.field, &ObjectDetail{"empty", "null", "canonical"})
		if objectRecord(t, tr).MalformedChunkDetail.Object != nil {
			t.Fatal("out-of-scope object detail")
		}
	}
	for _, detail := range [][]*ObjectDetail{nil, {nil}} {
		_, tr := New(context.Background())
		tr.MalformedChunk(ChunkInvalidValue, ChunkObject, detail...)
		if objectRecord(t, tr).MalformedChunkDetail.Object != nil {
			t.Fatal("legacy call emitted object detail")
		}
	}
}

func TestObjectDiagnosticEntryParallel(t *testing.T) {
	_, tr := New(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				tr.MalformedChunk(ChunkInvalidValue, ChunkObject, &ObjectDetail{"other", "ambiguous", "multiple"})
				tr.Begin(Stream)(Malformed)
			}
		}()
	}
	wg.Wait()
	if r := objectRecord(t, tr); r.MalformedChunkDetail.Object == nil || *r.MalformedChunkDetail.Object != (ObjectDetail{"other", "ambiguous", "multiple"}) {
		t.Fatal("concurrent classification lost")
	}
}
