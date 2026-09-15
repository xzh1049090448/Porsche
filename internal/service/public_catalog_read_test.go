package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestProjectionHomeConfigETagTracksEffectiveBoundaryAndGeneration(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	effective := now.Format(time.RFC3339)
	price := models.PublicPriceSnapshot{Guid: 80, Version: 9, ContentHash: strings.Repeat("b", 64)}
	input, output := "1.00000000", "2.00000000"
	items := []models.PublicPriceSnapshotItem{{ModelKey: "alpha", UpstreamModelID: "provider/alpha", PricingType: "token", InputPriceUSDPerMillionTokens: &input, OutputPriceUSDPerMillionTokens: &output}}
	price.ContentHash, _ = hashPublicPriceSnapshotItems(items)
	draft := PublicContentDraft{Revision: 4, Home: "legacy", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true}
	home := PublicHomeDraft{Revision: 4, Announcements: []PublicHomeAnnouncementDraft{{GUID: "10", Title: "scheduled", BodyMarkdown: "ready", EffectiveAt: &effective, IsVisible: true}}, FeaturedModelKeys: []string{"alpha"}}
	prepared, issues := preparePublicContent(draft, home, models.PublicPriceSnapshot{ID: 8, Guid: price.Guid, Version: price.Version}, items)
	if len(issues) != 0 {
		t.Fatalf("issues=%+v", issues)
	}
	release := models.PublicContentRelease{Payload: prepared.Payload, ContentHash: prepared.Hash, Version: 7}

	before, beforeAvailable, beforeTag, err := projectPublicCatalogGeneration(release, price, models.PublicPriceVisibilityVisible, items, now.Add(-time.Second))
	if err != nil || !beforeAvailable || len(before.Announcements) != 0 {
		t.Fatalf("before=%#v available=%v tag=%q err=%v", before, beforeAvailable, beforeTag, err)
	}
	at, atAvailable, atTag, err := projectPublicCatalogGeneration(release, price, models.PublicPriceVisibilityVisible, items, now)
	if err != nil || !atAvailable || len(at.Announcements) != 1 || at.ContentReleaseVersion != 7 || at.PriceReleaseVersion != 9 {
		t.Fatalf("at=%#v available=%v tag=%q err=%v", at, atAvailable, atTag, err)
	}
	if beforeTag == atTag || len(atTag) != 66 || atTag[0] != '"' || atTag[len(atTag)-1] != '"' {
		t.Fatalf("effective boundary tags before=%q at=%q", beforeTag, atTag)
	}
	again, _, againTag, err := projectPublicCatalogGeneration(release, price, models.PublicPriceVisibilityVisible, items, now)
	if err != nil || againTag != atTag || !reflect.DeepEqual(again, at) {
		t.Fatalf("same representation not stable: first=%q second=%q err=%v", atTag, againTag, err)
	}
	newGeneration := release
	newGeneration.Version++
	newHome, _, newTag, err := projectPublicCatalogGeneration(newGeneration, price, models.PublicPriceVisibilityVisible, items, now)
	if err != nil || newHome.ContentReleaseVersion != 8 || newTag == atTag {
		t.Fatalf("generation tag=%q old=%q home=%#v err=%v", newTag, atTag, newHome, err)
	}
}

func TestProjectionLegacyHomeRemainsSanitizedAndStructuredConfigUnavailable(t *testing.T) {
	draft := PublicContentDraft{Revision: 2, Home: "[safe](/about)", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true}
	price := models.PublicPriceSnapshot{Guid: 80, Version: 4, ContentHash: strings.Repeat("c", 64)}
	price.ContentHash, _ = hashPublicPriceSnapshotItems(nil)
	prepared, issues := preparePublicContent(draft, PublicHomeDraft{Revision: draft.Revision}, models.PublicPriceSnapshot{ID: 8, Guid: price.Guid, Version: price.Version}, nil)
	if len(issues) != 0 || prepared == nil {
		t.Fatalf("prepared=%#v issues=%+v", prepared, issues)
	}
	// Use the server-produced safe document while keeping a legacy release shape.
	legacyPayload := models.JSONMap{"home": prepared.Documents["home"], "about": prepared.Documents["about"], "terms": prepared.Documents["terms"], "privacy": prepared.Documents["privacy"], "legal_reviewed": true, "model_keys": []string{}, "price_snapshot_guid": "80", "price_snapshot_version": int64(4)}
	hash, err := hashPublicContentPayload(legacyPayload)
	if err != nil {
		t.Fatal(err)
	}
	release := models.PublicContentRelease{Payload: legacyPayload, ContentHash: hash, Version: 3}
	home, available, _, err := projectPublicCatalogGeneration(release, price, models.PublicPriceVisibilityVisible, nil, time.Now().UTC())
	if err != nil || available || home.Announcements == nil || projectContentPayload(release.Payload, 1).Home != "<p><a href=\"/about\">safe</a></p>\n" {
		t.Fatalf("legacy home=%#v available=%v document=%q err=%v", home, available, projectContentPayload(release.Payload, 1).Home, err)
	}
}

func TestProjectionDBRejectsCorrectHashFeaturedModelOutsideBoundSnapshot(t *testing.T) {
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
	if err = tx.Where("guid=?", seed.priceGUID).First(&price).Error; err != nil {
		t.Fatal(err)
	}
	var items []models.PublicPriceSnapshotItem
	if err = tx.Where("snapshot_id=? AND is_deleted=0", price.ID).Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	price.ContentHash, err = hashPublicPriceSnapshotItems(items)
	if err != nil || tx.Model(&price).Update("content_hash", price.ContentHash).Error != nil {
		t.Fatalf("price hash=%q err=%v", price.ContentHash, err)
	}
	draft := PublicContentDraft{Revision: 2, Home: "legacy", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true}
	prepared, issues := preparePublicContent(draft, PublicHomeDraft{Revision: 2, FeaturedModelKeys: []string{seed.modelKey}}, price, items)
	if len(issues) != 0 {
		t.Fatalf("issues=%+v", issues)
	}
	homeConfig := prepared.Payload["home_config"].(models.JSONMap)
	homeConfig["featured_model_keys"] = []string{"outside-bound-snapshot"}
	prepared.Hash, err = hashPublicContentPayload(prepared.Payload)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	release := models.PublicContentRelease{Guid: f.actor.Guid + 7001, CreatedAt: now, CreatedBy: &f.actor.ID, UpdatedAt: now, UpdatedBy: &f.actor.ID, DocumentKind: models.PublicContentDocumentSite, Version: 2, SourceRevision: 2, Payload: prepared.Payload, ContentHash: prepared.Hash, PublishedAt: now}
	if err = tx.Create(&release).Error; err != nil {
		t.Fatal(err)
	}
	if err = tx.Model(&models.PublicPublicationState{}).Where("state_key=?", publicPublicationStateKey).Updates(map[string]any{"content_release_id": release.ID, "price_snapshot_id": price.ID}).Error; err != nil {
		t.Fatal(err)
	}
	got, readErr := NewPublicCatalogReadService(tx).Projection(context.Background())
	if got != nil || status(readErr) != 503 {
		t.Fatalf("projection=%#v status=%d err=%v", got, status(readErr), readErr)
	}
}

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
