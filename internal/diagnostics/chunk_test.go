package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestChunkDiagnosticEntryWhitelist(t *testing.T) {
	for _, tc := range []struct {
		reason     ChunkReason
		field      ChunkField
		wantReason ChunkReason
		wantField  ChunkField
	}{{ChunkJSONType, ChunkDeltaContent, ChunkJSONType, ChunkDeltaContent}, {ChunkReason("SENSITIVE-reason"), ChunkField("SENSITIVE-path"), ChunkUnknown, ChunkFieldUnknown}} {
		_, tr := New(context.Background())
		tr.MalformedChunk(tc.reason, tc.field)
		var out bytes.Buffer
		tr.End(&out, 503, "safe")
		var r Record
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "SENSITIVE") || r.MalformedChunkDetail.Reason != tc.wantReason || r.MalformedChunkDetail.Field != tc.wantField {
			t.Fatalf("bad whitelist: %s", out.String())
		}
	}
	From(context.Background()).MalformedChunk(ChunkReason("SENSITIVE"), ChunkField("SENSITIVE"))
	_, tr := New(context.Background())
	var out bytes.Buffer
	tr.End(&out, 200, "safe")
	if strings.Contains(out.String(), "malformed_chunk_detail") {
		t.Fatal("detail emitted without failure")
	}
}
