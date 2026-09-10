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
	if got, err := preparePublicPriceSnapshot([]models.PublicModelConfig{valid}); err != nil || got.Items[0].OutputPriceUSDPerMillionTokens != nil {
		t.Fatalf("missing price not preserved: got=%#v err=%v", got, err)
	}
}

func TestPublicPriceSnapshotRequiresProvenanceOnlyWhenAnyPricePublished(t *testing.T) {
	priced := snapshotModelFixture("priced")
	priced.PriceSource = ""
	priced.PriceReviewer = ""
	priced.PriceEffectiveAt = nil
	if _, err := preparePublicPriceSnapshot([]models.PublicModelConfig{priced}); status(err) != 422 {
		t.Fatalf("priced without provenance err=%v", err)
	}
	unpriced := snapshotModelFixture("unpriced")
	unpriced.InputPriceUSDPerMillionTokens = nil
	unpriced.OutputPriceUSDPerMillionTokens = nil
	unpriced.PriceSource = ""
	unpriced.PriceReviewer = ""
	unpriced.PriceEffectiveAt = nil
	if _, err := preparePublicPriceSnapshot([]models.PublicModelConfig{unpriced}); err != nil {
		t.Fatalf("unpriced metadata rejected: %v", err)
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
	if publicPriceValue(one.Items[0].InputPriceUSDPerMillionTokens) != "1.23000000" || publicPriceValue(one.Items[0].OutputPriceUSDPerMillionTokens) != "999999999999.99999999" {
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

func TestPublicPriceSnapshotContentCompatibilityRejectsMissingReferencedModel(t *testing.T) {
	payload := models.JSONMap{"home": "[alpha](/pricing/alpha)", "about": "About", "terms": "Terms", "privacy": "Privacy", "legal_reviewed": true, "model_keys": []string{"alpha"}, "price_snapshot_guid": "1", "price_snapshot_version": int64(1)}
	hash, _ := hashPublicContentPayload(payload)
	release := models.PublicContentRelease{Payload: payload, ContentHash: hash}
	alpha := models.PublicPriceSnapshotItem{ModelKey: "alpha", UpstreamModelID: "org/alpha", InputPriceUSDPerMillionTokens: snapshotStringPointer("1"), OutputPriceUSDPerMillionTokens: snapshotStringPointer("2")}
	if err := validateContentReleaseForPriceItems(release, []models.PublicPriceSnapshotItem{alpha}); err != nil {
		t.Fatal(err)
	}
	beta := alpha
	beta.ModelKey = "beta"
	beta.UpstreamModelID = "org/beta"
	if err := validateContentReleaseForPriceItems(release, []models.PublicPriceSnapshotItem{beta}); status(err) != 409 {
		t.Fatalf("missing reference err=%v", err)
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
	invalid[0].InputPriceUSDPerMillionTokens = snapshotStringPointer("bad")
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

func TestPublicPriceSnapshotRestoreAcceptsExactLegacyCanonicalHash(t *testing.T) {
	checked := int64(1_700_000_000_000)
	items := []models.PublicPriceSnapshotItem{{ModelConfigID: 7, ModelKey: "alpha", UpstreamModelID: "org/alpha", DisplayName: "Alpha", Provider: "acme", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: snapshotStringPointer("1.25000000"), OutputPriceUSDPerMillionTokens: snapshotStringPointer("2.50000000"), UpstreamCheckedAt: &checked, PricingType: "token"}}
	const legacyHash = "bac92bee165a7967fb5420ff2e15e0ec4e281e5608c29008361bcf5ca62b7922"
	got, ok := hashLegacyPublicPriceSnapshotItems(items)
	if !ok || got != legacyHash {
		t.Fatalf("legacy hash=%s ok=%v", got, ok)
	}
	restored, err := prepareRestoredPublicPriceSnapshot(items, legacyHash)
	if err != nil || restored.Hash == legacyHash {
		t.Fatalf("legacy restore=%#v err=%v", restored, err)
	}
	tampered := append([]models.PublicPriceSnapshotItem(nil), items...)
	tampered[0].DisplayName = "Tampered"
	if _, err := prepareRestoredPublicPriceSnapshot(tampered, legacyHash); status(err) != 422 {
		t.Fatalf("tampered legacy hash accepted: %v", err)
	}
}

func TestLegacyPublicPriceSnapshotHashRequiresEveryExtensionDefault(t *testing.T) {
	input, output := "1.00000000", "2.00000000"
	base := models.PublicPriceSnapshotItem{ModelKey: "alpha", UpstreamModelID: "org/alpha", DisplayName: "Alpha", Provider: "acme", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 1, InputPriceUSDPerMillionTokens: &input, OutputPriceUSDPerMillionTokens: &output, PricingType: "token"}
	hash, ok := hashLegacyPublicPriceSnapshotItems([]models.PublicPriceSnapshotItem{base})
	if !ok {
		t.Fatal("legacy fixture unavailable")
	}
	if legacy, valid := verifyPublicPriceSnapshotHash([]models.PublicPriceSnapshotItem{base}, hash); !legacy || !valid {
		t.Fatal("default legacy snapshot rejected")
	}
	mutations := []func(*models.PublicPriceSnapshotItem){
		func(v *models.PublicPriceSnapshotItem) { v.PricingType = "request" },
		func(v *models.PublicPriceSnapshotItem) { v.PublicDisplayGroup = "featured" },
		func(v *models.PublicPriceSnapshotItem) { v.EndpointTypes = models.JSONSlice{"responses"} },
		func(v *models.PublicPriceSnapshotItem) { v.PublicRestrictions = models.JSONSlice{"region"} },
		func(v *models.PublicPriceSnapshotItem) { v.PriceSource = "source" },
		func(v *models.PublicPriceSnapshotItem) { v.PriceReviewer = "reviewer" },
		func(v *models.PublicPriceSnapshotItem) { n := int64(1); v.EffectiveAt = &n },
	}
	for i, mutate := range mutations {
		item := base
		mutate(&item)
		if _, valid := verifyPublicPriceSnapshotHash([]models.PublicPriceSnapshotItem{item}, hash); valid {
			t.Fatalf("extension mutation %d accepted", i)
		}
	}
	current, err := hashPublicPriceSnapshotItems([]models.PublicPriceSnapshotItem{base})
	if err != nil {
		t.Fatal(err)
	}
	if legacy, valid := verifyPublicPriceSnapshotHash([]models.PublicPriceSnapshotItem{base}, current); legacy || !valid {
		t.Fatal("current hash path rejected")
	}
}

func TestPublicPriceSnapshotRestoreRequiresCurrentActiveExactIdentity(t *testing.T) {
	prepared, err := preparePublicPriceSnapshot([]models.PublicModelConfig{snapshotModelFixture("alpha")})
	if err != nil {
		t.Fatal(err)
	}
	current := []models.PublicModelConfig{snapshotModelFixture("alpha")}
	if _, err = planPublicPriceSnapshotRestore(prepared.Items, current); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*models.PublicModelConfig){func(v *models.PublicModelConfig) { v.IsDeleted = 1 }, func(v *models.PublicModelConfig) { v.Status = models.PublicModelConfigStatusInactive }, func(v *models.PublicModelConfig) { v.ModelKey = "reused" }, func(v *models.PublicModelConfig) { v.UpstreamModelID = "org/reused" }} {
		copyRows := append([]models.PublicModelConfig(nil), current...)
		mutate(&copyRows[0])
		if _, err = planPublicPriceSnapshotRestore(prepared.Items, copyRows); status(err) != 409 {
			t.Fatalf("unsafe lifecycle/identity accepted: %v", err)
		}
	}
	if _, err = planPublicPriceSnapshotRestore(prepared.Items, nil); status(err) != 409 {
		t.Fatalf("unknown identity accepted: %v", err)
	}
}

func TestPublicPriceSnapshotRestorePlanMaterializesHistoricalDraft(t *testing.T) {
	historical := snapshotModelFixture("alpha")
	historical.DisplayName = "Historical"
	prepared, err := preparePublicPriceSnapshot([]models.PublicModelConfig{historical})
	if err != nil {
		t.Fatal(err)
	}
	current := snapshotModelFixture("alpha")
	current.DisplayName = "Current"
	extra := snapshotModelFixture("extra-long")
	plan, err := planPublicPriceSnapshotRestore(prepared.Items, []models.PublicModelConfig{current, extra})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Include) != 1 || plan.Include[0].DisplayName != "Historical" || len(plan.Deactivate) != 1 || plan.Deactivate[0].ModelKey != "extra-long" {
		t.Fatalf("plan=%#v", plan)
	}
}

func snapshotModelFixture(key string) models.PublicModelConfig {
	in, out := "0.00000001", "2.50000000"
	checked := int64(1900000000000)
	effective := int64(1899990000000)
	return models.PublicModelConfig{ID: int64(len(key) + 1), ModelKey: key, UpstreamModelID: "org/" + key, DisplayName: "Model " + key, Provider: "provider", Capabilities: models.JSONSlice{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: &in, OutputPriceUSDPerMillionTokens: &out, Status: models.PublicModelConfigStatusActive, Revision: 3, LastUpstreamCheckAt: &checked, PriceSource: "approved catalog", PriceReviewer: "pricing team", PriceEffectiveAt: &effective}
}

func snapshotStringPointer(v string) *string { return &v }
