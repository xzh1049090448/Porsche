package service

import (
	"reflect"
	"strings"
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

func TestPublicPriceSnapshotRestoreRevalidatesAndRehashesHistoricalItems(t *testing.T) {
	prepared, err := preparePublicPriceSnapshot([]models.PublicModelConfig{snapshotModelFixture("alpha")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepareRestoredPublicPriceSnapshot(prepared.Items, strings.Repeat("f", 64)); status(err) != 422 {
		t.Fatalf("tampered hash=%v", err)
	}
	if _, err = prepareRestoredPublicPriceSnapshot(nil, prepared.Hash); status(err) != 422 {
		t.Fatalf("empty=%v", err)
	}
	invalid := append([]models.PublicPriceSnapshotItem(nil), prepared.Items...)
	invalid[0].InputPriceUSDPerMillionTokens = "bad"
	if _, err = prepareRestoredPublicPriceSnapshot(invalid, prepared.Hash); status(err) != 422 {
		t.Fatalf("invalid=%v", err)
	}
	duplicate := append(append([]models.PublicPriceSnapshotItem(nil), prepared.Items...), prepared.Items[0])
	duplicateHash, _ := hashPublicPriceSnapshotItems(duplicate)
	if _, err = prepareRestoredPublicPriceSnapshot(duplicate, duplicateHash); status(err) != 422 {
		t.Fatalf("duplicate historical identity=%v", err)
	}
	restored, err := prepareRestoredPublicPriceSnapshot(prepared.Items, prepared.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Hash != prepared.Hash || !reflect.DeepEqual(restored.Items, prepared.Items) {
		t.Fatalf("restored=%#v want=%#v", restored, prepared)
	}
}

func snapshotModelFixture(key string) models.PublicModelConfig {
	in, out := "0.00000001", "2.50000000"
	checked := int64(1900000000000)
	return models.PublicModelConfig{ID: int64(len(key) + 1), ModelKey: key, UpstreamModelID: "org/" + key, DisplayName: "Model " + key, Provider: "provider", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: &in, OutputPriceUSDPerMillionTokens: &out, Status: models.PublicModelConfigStatusActive, Revision: 3, LastUpstreamCheckAt: &checked}
}

func snapshotStringPointer(v string) *string { return &v }
