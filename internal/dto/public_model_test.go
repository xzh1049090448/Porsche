package dto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicModelDTOUsesExactSafeFields(t *testing.T) {
	v := PublicModelAdmin{GUID: "42", ModelKey: "safe-key", Capabilities: []string{}, Status: "draft", Revision: 1}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, key := range []string{`"guid"`, `"model_key"`, `"upstream_model_id"`, `"input_price_usd_per_million_tokens"`, `"output_price_usd_per_million_tokens"`, `"last_upstream_check_at"`} {
		if !strings.Contains(got, key) {
			t.Fatalf("missing %s: %s", key, got)
		}
	}
	for _, forbidden := range []string{"credential", "raw_payload", "api_key"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("unsafe field %q", forbidden)
		}
	}
}

func TestPublicModelMutationDTOsKeepIdentityImmutable(t *testing.T) {
	typ := UpdatePublicModelRequest{ExpectedRevision: 1}
	b, _ := json.Marshal(typ)
	if strings.Contains(string(b), "model_key") || strings.Contains(string(b), "upstream_model_id") {
		t.Fatal(string(b))
	}
}
