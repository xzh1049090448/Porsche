package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestPublicCatalogReadDBCommittedIntegrityAndDynamicInactivation(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL; .env is never read")
	}
	f := openPublicModelDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	seed, err := seedContentPublicationFixture(tx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	var price models.PublicPriceSnapshot
	if err = tx.First(&price).Error; err != nil {
		t.Fatal(err)
	}
	var priceItems []models.PublicPriceSnapshotItem
	if err = tx.Where("snapshot_id=?", price.ID).Find(&priceItems).Error; err != nil {
		t.Fatal(err)
	}
	priceHash, err := hashPublicPriceSnapshotItems(priceItems)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Model(&price).Update("content_hash", priceHash).Error; err != nil {
		t.Fatal(err)
	}
	content := NewPublicContentService(tx)
	draft, err := content.GetDraft(context.Background(), f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	home, about, terms, privacy, reviewed := "[model](/pricing/"+seed.modelKey+")", "About", "Terms", "Privacy", true
	draft, err = content.SaveDraft(context.Background(), f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: draft.Revision, Home: &home, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = content.Publish(context.Background(), PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: draft.Revision, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "public-read"}); err != nil {
		t.Fatal(err)
	}
	reader := NewPublicCatalogReadService(tx)
	projection, err := reader.Projection(context.Background())
	if err != nil || len(projection.Items) != 1 || projection.Items[0].ModelKey != seed.modelKey {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
	if err = tx.Model(&models.PublicPriceSnapshotItem{}).Where("snapshot_id=?", price.ID).Update("display_name", "tampered").Error; err != nil {
		t.Fatal(err)
	}
	if got, readErr := reader.Projection(context.Background()); status(readErr) != 503 || got != nil {
		t.Fatalf("tampered projection=%#v err=%v", got, readErr)
	}
}

func TestProjectPublicCatalogFiltersAndRedactsWithoutInternalFields(t *testing.T) {
	projection := &PublicCatalogProjection{
		Content:               PublicContentDraft{Home: "home", About: "about", Terms: "terms", Privacy: "privacy"},
		ContentReleaseVersion: 7, PriceReleaseVersion: 9, PriceVisibility: models.PublicPriceVisibilityAuthenticatedOnly,
		Items: []PublicCatalogItem{{ModelKey: "alpha-chat", DisplayName: "Alpha", Provider: "acme", Capabilities: []string{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: "1.25000000", OutputPriceUSDPerMillionTokens: "2.50000000"}},
	}
	list := projection.List(PublicCatalogListRequest{Page: 1, PageSize: 20}, false)
	if len(list.Items) != 1 || list.Items[0].InputPriceUSDPerMillionTokens != nil || list.Items[0].OutputPriceUSDPerMillionTokens != nil || list.Items[0].PriceVisibility != "authenticated_only" {
		t.Fatalf("anonymous projection = %#v", list)
	}
	auth := projection.List(PublicCatalogListRequest{Page: 1, PageSize: 20}, true)
	if auth.Items[0].InputPriceUSDPerMillionTokens == nil || *auth.Items[0].InputPriceUSDPerMillionTokens != "1.25000000" {
		t.Fatalf("authenticated projection = %#v", auth)
	}
	raw, _ := json.Marshal(auth)
	for _, forbidden := range []string{"upstream_model_id", "snapshot_id", "model_config_id", "guid", "channel_address"} {
		if json.Valid(raw) && containsJSONField(raw, forbidden) {
			t.Fatalf("leaked %s: %s", forbidden, raw)
		}
	}
	assertPublicModelKeys(t, list.Items[0], []string{"capabilities", "context_window", "display_name", "model_key", "price_visibility", "provider", "release_version"})
	assertPublicModelKeys(t, auth.Items[0], []string{"capabilities", "context_window", "display_name", "input_price_usd_per_million_tokens", "model_key", "output_price_usd_per_million_tokens", "price_visibility", "provider", "release_version"})
}

func assertPublicModelKeys(t *testing.T, item PublicModelRead, want []string) {
	t.Helper()
	raw, _ := json.Marshal(item)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keys=%v want=%v body=%s", got, want, raw)
	}
}

func TestProjectPublicCatalogSearchProviderCapabilityAndPagination(t *testing.T) {
	p := &PublicCatalogProjection{PriceReleaseVersion: 3, PriceVisibility: models.PublicPriceVisibilityVisible, Items: []PublicCatalogItem{
		{ModelKey: "alpha-chat", DisplayName: "Alpha", Provider: "acme", Capabilities: []string{"chat"}},
		{ModelKey: "beta-embed", DisplayName: "Beta", Provider: "other", Capabilities: []string{"embedding"}},
	}}
	got := p.List(PublicCatalogListRequest{Search: "ALPHA", Provider: "acme", Capability: "chat", Page: 1, PageSize: 20}, false)
	if got.Total != 1 || len(got.Items) != 1 || got.Items[0].ModelKey != "alpha-chat" {
		t.Fatalf("filtered = %#v", got)
	}
	if _, status := p.Detail("missing", false); status != 404 {
		t.Fatalf("unknown status=%d", status)
	}
	p.GoneKeys = map[string]struct{}{"retired": {}}
	if _, status := p.Detail("retired", false); status != 410 {
		t.Fatalf("retired status=%d", status)
	}
}

func containsJSONField(raw []byte, field string) bool {
	var value any
	_ = json.Unmarshal(raw, &value)
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, y := range x {
				if k == field || walk(y) {
					return true
				}
			}
		case []any:
			for _, y := range x {
				if walk(y) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}
