package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
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
	if err = tx.Exec("UPDATE public_price_snapshots SET content_hash=? WHERE id=?", priceHash, price.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = tx.First(&price, price.ID).Error; err != nil || price.ContentHash != priceHash {
		t.Fatalf("stored price hash=%q want=%q err=%v", price.ContentHash, priceHash, err)
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
	if err = tx.Exec("UPDATE public_price_snapshot_items SET display_name=? WHERE snapshot_id=?", "tampered", price.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got, readErr := reader.Projection(context.Background()); status(readErr) != 503 || got != nil {
		t.Fatalf("tampered projection=%#v err=%v", got, readErr)
	}
	if err = tx.Exec("UPDATE public_price_snapshot_items SET display_name=? WHERE snapshot_id=?", priceItems[0].DisplayName, price.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = tx.Model(&models.PublicModelConfig{}).Where("model_key=?", seed.modelKey).Update("status", models.PublicModelConfigStatusInactive).Error; err != nil {
		t.Fatal(err)
	}
	if got, readErr := reader.Projection(context.Background()); status(readErr) != 503 || got != nil {
		t.Fatalf("pending inactivation projection=%#v err=%v", got, readErr)
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
	assertPublicModelKeys(t, list.Items[0], []string{"capabilities", "context_window", "display_name", "endpoint_types", "model_key", "price_visibility", "pricing_type", "provider", "release_version", "updated_at"})
	assertPublicModelKeys(t, auth.Items[0], []string{"capabilities", "context_window", "display_name", "endpoint_types", "input_price_usd_per_million_tokens", "model_key", "output_price_usd_per_million_tokens", "price_visibility", "pricing_type", "provider", "release_version", "updated_at"})
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

func TestProjectPublicCatalogFiltersEndpointAndGroupAndSortsGloballyBeforePagination(t *testing.T) {
	p := &PublicCatalogProjection{PriceReleaseVersion: 3, PriceVisibility: models.PublicPriceVisibilityVisible, Items: []PublicCatalogItem{
		{ModelKey: "zeta", DisplayName: "Zeta", Provider: "acme", PublicDisplayGroup: "featured", PricingType: "token", EndpointTypes: []string{"chat_completions"}, InputPriceUSDPerMillionTokens: "9.00000000", OutputPriceUSDPerMillionTokens: "10.00000000"},
		{ModelKey: "alpha", DisplayName: "Alpha", Provider: "acme", PublicDisplayGroup: "featured", PricingType: "token", EndpointTypes: []string{"chat_completions", "responses"}, InputPriceUSDPerMillionTokens: "1.25000000", OutputPriceUSDPerMillionTokens: "2.50000000"},
		{ModelKey: "beta", DisplayName: "Beta", Provider: "acme", PublicDisplayGroup: "other", PricingType: "token", EndpointTypes: []string{"embeddings"}, InputPriceUSDPerMillionTokens: "0.50000000", OutputPriceUSDPerMillionTokens: "3.00000000"},
	}}
	got := p.List(PublicCatalogListRequest{EndpointType: "responses", PublicDisplayGroup: "featured", PricingType: "token", Sort: "input_price", Order: "asc", Page: 1, PageSize: 20}, false)
	if got.Total != 1 || len(got.Items) != 1 || got.Items[0].ModelKey != "alpha" {
		t.Fatalf("filtered = %#v", got)
	}
	all := p.List(PublicCatalogListRequest{Sort: "input_price", Order: "asc", Page: 1, PageSize: 20}, false)
	if gotKeys := []string{all.Items[0].ModelKey, all.Items[1].ModelKey, all.Items[2].ModelKey}; fmt.Sprint(gotKeys) != "[beta alpha zeta]" {
		t.Fatalf("global numeric order=%v", gotKeys)
	}
}

func TestProjectPublicCatalogReturnsGlobalFacetsIndependentOfCurrentPage(t *testing.T) {
	p := &PublicCatalogProjection{PriceReleaseVersion: 2, PriceVisibility: models.PublicPriceVisibilityVisible, Items: []PublicCatalogItem{
		{ModelKey: "a", Provider: "p1", Capabilities: []string{"chat"}, EndpointTypes: []string{"responses"}, PublicDisplayGroup: "featured", PricingType: "token"},
		{ModelKey: "b", Provider: "p2", Capabilities: []string{"embedding"}, EndpointTypes: []string{"embeddings"}, PublicDisplayGroup: "other", PricingType: "token"},
	}}
	got := p.List(PublicCatalogListRequest{Page: 1, PageSize: 1}, false)
	if len(got.Items) != 1 || fmt.Sprint(got.Facets.Providers) != "[p1 p2]" || fmt.Sprint(got.Facets.EndpointTypes) != "[embeddings responses]" || fmt.Sprint(got.Facets.PublicDisplayGroups) != "[featured other]" {
		t.Fatalf("global facets=%#v", got)
	}
}

func TestProjectPublicCatalogSortKeepsMissingPricesLastInBothDirections(t *testing.T) {
	p := &PublicCatalogProjection{Items: []PublicCatalogItem{
		{ModelKey: "missing"},
		{ModelKey: "low", InputPriceUSDPerMillionTokens: "1.00000000"},
		{ModelKey: "high", InputPriceUSDPerMillionTokens: "9.00000000"},
	}}
	for _, tc := range []struct{ order, want string }{{"asc", "[low high missing]"}, {"desc", "[high low missing]"}} {
		got := p.List(PublicCatalogListRequest{Sort: "input_price", Order: tc.order, Page: 1, PageSize: 20}, false)
		keys := []string{got.Items[0].ModelKey, got.Items[1].ModelKey, got.Items[2].ModelKey}
		if fmt.Sprint(keys) != tc.want {
			t.Fatalf("%s order=%v", tc.order, keys)
		}
	}
}

func TestProjectPublicCatalogDetailIncludesApprovedSnapshotMetadata(t *testing.T) {
	p := &PublicCatalogProjection{PriceReleaseVersion: 4, PriceVisibility: models.PublicPriceVisibilityVisible, Items: []PublicCatalogItem{{
		ModelKey: "alpha", DisplayName: "Alpha", PricingType: "token", PublicDisplayGroup: "featured",
		EndpointTypes: []string{"responses"}, PublicRestrictions: []string{"region_limited"},
		PriceSource: "published catalog", PriceReviewer: "pricing team", EffectiveAt: "2026-09-10T00:00:00Z", UpdatedAt: "2026-09-10T01:00:00Z",
	}}}
	out, status := p.Detail("alpha", false)
	if status != 200 || out.Model.PricingType != "token" || out.Model.PublicDisplayGroup != "featured" || fmt.Sprint(out.Model.EndpointTypes) != "[responses]" || out.Model.PriceSource != "published catalog" || out.Model.PriceReviewer != "pricing team" {
		t.Fatalf("detail=%#v status=%d", out, status)
	}
	raw, _ := json.Marshal(out)
	for _, forbidden := range []string{"upstream_model_id", "channel_address", "model_config_id"} {
		if containsJSONField(raw, forbidden) {
			t.Fatalf("leaked %s: %s", forbidden, raw)
		}
	}
}

func TestProjectPublicCatalogOmitsUnavailablePriceFields(t *testing.T) {
	p := &PublicCatalogProjection{PriceReleaseVersion: 1, PriceVisibility: models.PublicPriceVisibilityVisible, Items: []PublicCatalogItem{{ModelKey: "alpha", DisplayName: "Alpha", Capabilities: []string{}}}}
	out := p.List(PublicCatalogListRequest{Page: 1, PageSize: 20}, true)
	raw, _ := json.Marshal(out.Items[0])
	if strings.Contains(string(raw), "input_price_") || strings.Contains(string(raw), "output_price_") {
		t.Fatalf("missing prices serialized: %s", raw)
	}
}

func TestProjectPublicCatalogPaginationNeverOverflows(t *testing.T) {
	p := &PublicCatalogProjection{Items: []PublicCatalogItem{{ModelKey: "alpha"}}}
	for _, page := range []int{1, 2, int(^uint(0) >> 1)} {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("page=%d panic=%v", page, recovered)
				}
			}()
			got := p.List(PublicCatalogListRequest{Page: page, PageSize: 100}, false)
			if page > 1 && len(got.Items) != 0 {
				t.Fatalf("page=%d items=%d", page, len(got.Items))
			}
		}()
	}
	_ = strconv.IntSize
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
