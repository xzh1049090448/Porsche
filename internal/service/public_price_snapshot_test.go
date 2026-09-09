package service

import (
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestPublicPriceSnapshotValidationRunsBeforePublication(t *testing.T) {
	valid := snapshotModelFixture("alpha")
	if _, err := preparePublicPriceSnapshot([]models.PublicModelConfig{valid}); err != nil {
		t.Fatalf("valid model rejected: %v", err)
	}
	valid.OutputPriceUSDPerMillionTokens = nil
	if _, err := preparePublicPriceSnapshot([]models.PublicModelConfig{valid}); status(err) != 422 {
		t.Fatalf("missing price status=%d err=%v", status(err), err)
	}
}

func TestPublicPriceSnapshotCanonicalHashStableAndDecimalExact(t *testing.T) {
	a, b := snapshotModelFixture("alpha"), snapshotModelFixture("beta")
	a.InputPriceUSDPerMillionTokens = snapshotStringPointer("1.23000000")
	a.OutputPriceUSDPerMillionTokens = snapshotStringPointer("999999999999.99999999")
	one, err := preparePublicPriceSnapshot([]models.PublicModelConfig{b, a})
	if err != nil {
		t.Fatal(err)
	}
	two, err := preparePublicPriceSnapshot([]models.PublicModelConfig{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if one.Hash != two.Hash || len(one.Hash) != 64 {
		t.Fatalf("unstable hash %q %q", one.Hash, two.Hash)
	}
	if one.Items[0].InputPriceUSDPerMillionTokens != "1.23000000" || one.Items[0].OutputPriceUSDPerMillionTokens != "999999999999.99999999" {
		t.Fatalf("decimal text changed: %#v", one.Items[0])
	}
	one.Items[0].DisplayName = "changed"
	three, _ := hashPublicPriceSnapshotItems(one.Items)
	if three == one.Hash {
		t.Fatal("content change did not change hash")
	}
}

func TestPublicPriceSnapshotExcludesInactiveAndDeletedModels(t *testing.T) {
	active := snapshotModelFixture("active")
	inactive := snapshotModelFixture("inactive")
	inactive.Status = models.PublicModelConfigStatusInactive
	deleted := snapshotModelFixture("deleted")
	deleted.IsDeleted = 1
	prepared, err := preparePublicPriceSnapshot([]models.PublicModelConfig{inactive, active, deleted})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Items) != 1 || prepared.Items[0].ModelKey != "active" {
		t.Fatalf("items=%#v", prepared.Items)
	}
}

func TestPublicPriceSnapshotIdempotencyBindingUsesActorOperationAndPayload(t *testing.T) {
	a := publicPriceIdempotencyBinding(7, "publish", "request-key", "payload")
	if a == publicPriceIdempotencyBinding(8, "publish", "request-key", "payload") ||
		a == publicPriceIdempotencyBinding(7, "restore", "request-key", "payload") ||
		a == publicPriceIdempotencyBinding(7, "publish", "other", "payload") ||
		a == publicPriceIdempotencyBinding(7, "publish", "request-key", "other") {
		t.Fatal("idempotency binding omitted a required dimension")
	}
}

func snapshotModelFixture(key string) models.PublicModelConfig {
	in, out := "0.00000001", "2.50000000"
	checked := int64(1900000000000)
	return models.PublicModelConfig{ID: int64(len(key) + 1), ModelKey: key, UpstreamModelID: "org/" + key, DisplayName: "Model " + key, Provider: "provider", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: &in, OutputPriceUSDPerMillionTokens: &out, Status: models.PublicModelConfigStatusActive, Revision: 3, LastUpstreamCheckAt: &checked}
}

func snapshotStringPointer(v string) *string { return &v }
