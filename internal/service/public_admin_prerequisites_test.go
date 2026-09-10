package service

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestAdminReleaseProjectionsUseRFC3339AndSafeFields(t *testing.T) {
	release := projectPublicPriceReleaseDetail(models.PublicPriceSnapshot{Guid: 91, Version: 3, Reason: models.PublicPriceSnapshotReasonRootPublish, SourceRevision: 7, PublishedAt: 1_700_000_000_000}, []models.PublicPriceSnapshotItem{{ModelKey: "alpha", UpstreamModelID: "secret/upstream", DisplayName: "Alpha", Provider: "p", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 10, InputPriceUSDPerMillionTokens: "1.23000000", OutputPriceUSDPerMillionTokens: "4.56000000"}})
	b, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"release":{"guid":"91","version":3,"reason":"root_publish","source_revision":7,"created_at":"2023-11-14T22:13:20Z"},"items":[{"model_key":"alpha","display_name":"Alpha","provider":"p","capabilities":["chat"],"context_window":10,"input_price_usd_per_million_tokens":"1.23000000","output_price_usd_per_million_tokens":"4.56000000","price_visibility":"visible","release_version":3,"pricing_type":"token","endpoint_types":[],"updated_at":"2023-11-14T22:13:20Z"}]}` {
		t.Fatalf("unsafe or non-contract projection: %s", b)
	}
}

func TestMutationReleaseProjectionsUseFrozenReleaseSchema(t *testing.T) {
	price := projectPublicPriceSnapshot(models.PublicPriceSnapshot{Guid: 7, Version: 2, Reason: models.PublicPriceSnapshotReasonRestore, SourceRevision: 4, PublishedAt: 1_700_000_000_000})
	content := projectContentRelease(models.PublicContentRelease{Guid: 8, Version: 3, SourceRevision: 5, PublishedAt: 1_700_000_000_000}, "restore")
	for _, value := range []any{price, content} {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) == "" || !json.Valid(b) || !containsJSONCreatedAtRFC3339(string(b)) {
			t.Fatalf("release=%s", b)
		}
	}
}

func containsJSONCreatedAtRFC3339(v string) bool {
	return len(v) > 0 && json.Valid([]byte(v)) && bytes.Contains([]byte(v), []byte(`"created_at":"2023-11-14T22:13:20Z"`))
}

func TestPriceDraftValidationPreservesNullableDecimals(t *testing.T) {
	in := "0.10000000"
	draft := PublicPriceDraft{Revision: 2, Models: []PublicModelAdmin{{GUID: "1", ModelKey: "a", UpstreamModelID: "u", DisplayName: "A", Provider: "p", Capabilities: []string{"chat"}, ContextWindow: 1, InputPriceUSDPerMillionTokens: &in, OutputPriceUSDPerMillionTokens: nil, Status: "active", Revision: 1}}, Currency: "USD", Unit: "million_tokens"}
	issues := validatePublicPriceDraft(draft)
	if len(issues) != 1 || issues[0].Field != "models[0].output_price_usd_per_million_tokens" || issues[0].Code != "required" {
		t.Fatalf("issues=%#v", issues)
	}
}

func TestPublicModelMissingNilServiceFailsClosed(t *testing.T) {
	var s *PublicModelAdminService
	if _, err := s.Missing(context.Background()); err == nil {
		t.Fatal("nil service succeeded")
	}
}
