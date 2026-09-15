package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestPublicContentSafeEmptyInitialDraft(t *testing.T) {
	d := emptyPublicContentDraft()
	if d.Revision != 1 || d.Home != "" || d.About != "" || d.Terms != "" || d.Privacy != "" || d.LegalReviewed {
		t.Fatalf("unsafe empty draft: %#v", d)
	}
}

func TestPublicContentPreparationSanitizesAndRequiresReviewedLegalAndExactModels(t *testing.T) {
	d := PublicContentDraft{Revision: 2, Home: "Use [alpha](/pricing/alpha)", About: "About", Terms: "Terms", Privacy: "Privacy", LegalReviewed: true}
	price := models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 4}
	items := []models.PublicPriceSnapshotItem{{ModelKey: "alpha", UpstreamModelID: "org/alpha", InputPriceUSDPerMillionTokens: snapshotStringPointer("1.00000000"), OutputPriceUSDPerMillionTokens: snapshotStringPointer("2.00000000")}}
	p, issues := preparePublicContent(d, PublicHomeDraft{Revision: d.Revision}, price, items)
	if issues == nil || len(issues) != 0 || p.Hash == "" || p.Payload["home"] != "<p>Use <a href=\"/pricing/alpha\">alpha</a></p>\n" {
		t.Fatalf("prepared=%#v issues=%#v", p, issues)
	}
	d.LegalReviewed = false
	if _, issues = preparePublicContent(d, PublicHomeDraft{Revision: d.Revision}, price, items); !hasPublicContentIssue(issues, "legal_review_required") {
		t.Fatalf("issues=%#v", issues)
	}
	d.LegalReviewed = true
	d.Home = "Use [other](/pricing/other)"
	if _, issues = preparePublicContent(d, PublicHomeDraft{Revision: d.Revision}, price, items); !hasPublicContentIssue(issues, "unknown_home_model") {
		t.Fatalf("issues=%#v", issues)
	}
	d.Home = strings.Repeat("x", PublicContentDocumentLimit+1)
	if _, issues = preparePublicContent(d, PublicHomeDraft{Revision: d.Revision}, price, items); !hasPublicContentIssue(issues, "content_too_large") {
		t.Fatalf("issues=%#v", issues)
	}
}

func TestPublicContentIdempotencyBindingUsesActorOperationKeyAndPayload(t *testing.T) {
	a := publicContentIdempotencyBinding(7, "publish", "key", "payload")
	for _, b := range []string{
		publicContentIdempotencyBinding(8, "publish", "key", "payload"),
		publicContentIdempotencyBinding(7, "restore", "key", "payload"),
		publicContentIdempotencyBinding(7, "publish", "other", "payload"),
		publicContentIdempotencyBinding(7, "publish", "key", "other"),
	} {
		if a == b {
			t.Fatal("binding omitted a required dimension")
		}
	}
}

func TestPublicContentAboutTruthGateAndParserReferences(t *testing.T) {
	d := PublicContentDraft{Revision: 2, Home: "[alpha][model] <a href='/pricing/html'>html</a> `[/pricing/code]`\n\n[model]: /pricing/alpha", About: "100% reliable", Terms: "Terms", Privacy: "Privacy", LegalReviewed: true}
	price := models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 4}
	items := []models.PublicPriceSnapshotItem{{ModelKey: "alpha", UpstreamModelID: "org/alpha", InputPriceUSDPerMillionTokens: snapshotStringPointer("1"), OutputPriceUSDPerMillionTokens: snapshotStringPointer("2")}}
	_, issues := preparePublicContent(d, PublicHomeDraft{Revision: d.Revision}, price, items)
	if !hasPublicContentIssue(issues, "unsubstantiated_prototype_claim") || !hasPublicContentIssue(issues, "unknown_home_model") {
		t.Fatalf("issues=%#v", issues)
	}
}

func TestPreparePublicContentBindsStructuredHomeToPriceSnapshot(t *testing.T) {
	effective := "2026-09-20T00:00:00Z"
	draft := PublicContentDraft{Revision: 4, Home: "legacy", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true}
	home := PublicHomeDraft{
		Revision: 4,
		Announcements: []PublicHomeAnnouncementDraft{
			{GUID: "12", Title: "later", BodyMarkdown: "**future**", EffectiveAt: &effective, IsVisible: true, SortOrder: 20},
			{GUID: "10", Title: "first", BodyMarkdown: "[safe](/about)", IsVisible: true, SortOrder: 10},
			{GUID: "11", Title: "hidden", BodyMarkdown: "must not publish", IsVisible: false, SortOrder: 0},
		},
		FAQs: []PublicHomeFAQDraft{
			{GUID: "21", Question: "visible?", AnswerMarkdown: "yes", IsVisible: true, SortOrder: 1},
			{GUID: "22", Question: "hidden?", AnswerMarkdown: "no", IsVisible: false, SortOrder: 0},
		},
		FeaturedModelKeys: []string{"deepseek-chat"},
	}
	price := models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 4}
	items := []models.PublicPriceSnapshotItem{{ModelKey: "deepseek-chat", UpstreamModelID: "org/deepseek-chat", InputPriceUSDPerMillionTokens: snapshotStringPointer("1"), OutputPriceUSDPerMillionTokens: snapshotStringPointer("2"), PricingType: "token"}}

	prepared, issues := preparePublicContent(draft, home, price, items)
	if len(issues) != 0 {
		t.Fatalf("issues=%+v", issues)
	}
	encoded, err := json.Marshal(prepared.Payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	var databaseRoundTrip models.JSONMap
	if err = json.Unmarshal(encoded, &databaseRoundTrip); err != nil {
		t.Fatal(err)
	}
	roundTripHash, err := hashPublicContentPayload(databaseRoundTrip)
	if err != nil || roundTripHash != prepared.Hash {
		t.Fatalf("hash changed after JSON database round trip: prepared=%s round_trip=%s err=%v", prepared.Hash, roundTripHash, err)
	}
	if err = verifyPublicContentRelease(models.PublicContentRelease{Payload: databaseRoundTrip, ContentHash: prepared.Hash}); err != nil {
		t.Fatalf("database JSON round trip failed release verification: %v", err)
	}
	for _, forbidden := range []string{"body_markdown", "answer_markdown", "is_visible", "is_deleted", "must not publish", "\"id\""} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("payload leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"schema_version":2`) || !strings.Contains(text, `"featured_model_keys":["deepseek-chat"]`) || !strings.Contains(text, effective) {
		t.Fatalf("payload=%s", text)
	}
	config, ok := prepared.Payload["home_config"].(models.JSONMap)
	decodedConfig, decodeErr := decodePublishedContentPayloadV2(prepared.Payload)
	if !ok || !reflect.DeepEqual(config["featured_model_keys"], []string{"deepseek-chat"}) || decodeErr != nil || len(decodedConfig.HomeConfig.Announcements) != 2 || decodedConfig.HomeConfig.Announcements[0].BodyHTML != "<p><a href=\"/about\">safe</a></p>\n" || len(decodedConfig.HomeConfig.FAQs) != 1 || decodedConfig.HomeConfig.FAQs[0].AnswerHTML != "<p>yes</p>\n" {
		t.Fatalf("home_config=%#v", prepared.Payload["home_config"])
	}
	if prepared.Payload["home"] != "<p>legacy</p>\n" || prepared.Hash == "" {
		t.Fatalf("prepared=%#v", prepared)
	}
	reordered := home
	reordered.Announcements = []PublicHomeAnnouncementDraft{home.Announcements[2], home.Announcements[1], home.Announcements[0]}
	again, againIssues := preparePublicContent(draft, reordered, price, items)
	if len(againIssues) != 0 || again.Hash != prepared.Hash {
		t.Fatalf("nondeterministic hash first=%s second=%#v issues=%+v", prepared.Hash, again, againIssues)
	}
}

func TestProjectPublicCatalogHomeConfigFiltersFutureAnnouncementsAndMarksLegacyUnavailable(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Second).Format(time.RFC3339)
	future := now.Add(time.Second).Format(time.RFC3339)
	draft := PublicContentDraft{Revision: 4, Home: "home", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true}
	home := PublicHomeDraft{
		Revision: 4,
		Announcements: []PublicHomeAnnouncementDraft{
			{GUID: "10", Title: "now", BodyMarkdown: "visible", IsVisible: true, SortOrder: 1},
			{GUID: "11", Title: "past", BodyMarkdown: "visible", EffectiveAt: &past, IsVisible: true, SortOrder: 2},
			{GUID: "12", Title: "future", BodyMarkdown: "must wait", EffectiveAt: &future, IsVisible: true, SortOrder: 3},
		},
		FAQs: []PublicHomeFAQDraft{{GUID: "20", Question: "q", AnswerMarkdown: "a", IsVisible: true, SortOrder: 1}},
	}
	prepared, issues := preparePublicContent(draft, home, models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 9}, nil)
	if len(issues) != 0 {
		t.Fatalf("issues=%+v", issues)
	}
	got, available, err := projectPublicCatalogHomeConfig(prepared.Payload, 7, 9, now)
	if err != nil {
		t.Fatal(err)
	}
	if !available || len(got.Announcements) != 2 || got.Announcements[0].GUID != "10" || got.Announcements[1].GUID != "11" || len(got.FAQs) != 1 || got.ContentReleaseVersion != 7 || got.PriceReleaseVersion != 9 {
		t.Fatalf("projection=%#v", got)
	}

	legacy, legacyAvailable, legacyErr := projectPublicCatalogHomeConfig(models.JSONMap{"model_keys": []string{}}, 3, 4, now)
	if legacyErr != nil || legacyAvailable {
		t.Fatalf("legacy current projection=%#v available=%v err=%v", legacy, legacyAvailable, legacyErr)
	}
	if legacy.Announcements == nil || legacy.FAQs == nil || legacy.FeaturedModelKeys == nil || len(legacy.Announcements)+len(legacy.FAQs)+len(legacy.FeaturedModelKeys) != 0 {
		t.Fatalf("legacy projection must preserve non-nil empty arrays: %#v", legacy)
	}

	emptyPrepared, emptyIssues := preparePublicContent(draft, PublicHomeDraft{Revision: draft.Revision}, models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 9}, nil)
	if len(emptyIssues) != 0 {
		t.Fatalf("empty structured issues=%+v", emptyIssues)
	}
	empty, emptyAvailable, emptyErr := projectPublicCatalogHomeConfig(emptyPrepared.Payload, 8, 9, now)
	if emptyErr != nil || !emptyAvailable || empty.Announcements == nil || empty.FAQs == nil || empty.FeaturedModelKeys == nil {
		t.Fatalf("empty structured projection=%#v available=%v err=%v", empty, emptyAvailable, emptyErr)
	}
}

func TestProjectPublicHomeConfigReleaseChecksIntegrityAndSupportsLegacy(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour).Format(time.RFC3339)
	draft := PublicContentDraft{Revision: 4, Home: "home", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true}
	prepared, issues := preparePublicContent(draft, PublicHomeDraft{Revision: 4, Announcements: []PublicHomeAnnouncementDraft{{GUID: "10", Title: "scheduled", BodyMarkdown: "later", EffectiveAt: &future, IsVisible: true}}}, models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 9}, nil)
	if len(issues) != 0 {
		t.Fatalf("issues=%+v", issues)
	}
	release := models.PublicContentRelease{Payload: prepared.Payload, ContentHash: prepared.Hash, Version: 7}
	got, err := projectPublicHomeConfigRelease(release)
	if err != nil || got.ContentReleaseVersion != 7 || got.PriceReleaseVersion != 9 || len(got.Announcements) != 1 || got.FAQs == nil || got.FeaturedModelKeys == nil {
		t.Fatalf("projection=%#v err=%v", got, err)
	}
	tampered := release
	tampered.ContentHash = strings.Repeat("0", 64)
	if _, err = projectPublicHomeConfigRelease(tampered); status(err) != 503 {
		t.Fatalf("tampered release status=%d err=%v", status(err), err)
	}

	legacyPayload := models.JSONMap{"home": "home", "about": "about", "terms": "terms", "privacy": "privacy", "legal_reviewed": true, "model_keys": []string{}, "price_snapshot_guid": "80", "price_snapshot_version": int64(4)}
	legacyHash, err := hashPublicContentPayload(legacyPayload)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := projectPublicHomeConfigRelease(models.PublicContentRelease{Payload: legacyPayload, ContentHash: legacyHash, Version: 3})
	if err != nil || legacy.ContentReleaseVersion != 3 || legacy.PriceReleaseVersion != 4 || legacy.Announcements == nil || legacy.FAQs == nil || legacy.FeaturedModelKeys == nil {
		t.Fatalf("legacy=%#v err=%v", legacy, err)
	}
}

func TestPreparePublicContentRejectsStructuredRevisionMarkdownAndFeaturedModels(t *testing.T) {
	draft := PublicContentDraft{Revision: 4, Home: "legacy", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true}
	price := models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 4}
	good := models.PublicPriceSnapshotItem{ModelKey: "alpha", UpstreamModelID: "org/alpha", InputPriceUSDPerMillionTokens: snapshotStringPointer("1"), OutputPriceUSDPerMillionTokens: snapshotStringPointer("2"), PricingType: "token"}
	base := PublicHomeDraft{Revision: 4, FeaturedModelKeys: []string{"alpha"}}

	tests := []struct {
		name  string
		home  PublicHomeDraft
		items []models.PublicPriceSnapshotItem
		code  string
	}{
		{"revision", PublicHomeDraft{Revision: 3}, []models.PublicPriceSnapshotItem{good}, "revision_mismatch"},
		{"missing", base, nil, "unknown_home_model"},
		{"deleted snapshot item", base, []models.PublicPriceSnapshotItem{func() models.PublicPriceSnapshotItem { v := good; v.IsDeleted = 1; return v }()}, "unknown_home_model"},
		{"missing input", base, []models.PublicPriceSnapshotItem{func() models.PublicPriceSnapshotItem { v := good; v.InputPriceUSDPerMillionTokens = nil; return v }()}, "missing_input_price"},
		{"missing output", base, []models.PublicPriceSnapshotItem{func() models.PublicPriceSnapshotItem { v := good; v.OutputPriceUSDPerMillionTokens = nil; return v }()}, "missing_output_price"},
		{"per call pricing", base, []models.PublicPriceSnapshotItem{func() models.PublicPriceSnapshotItem { v := good; v.PricingType = "call"; return v }()}, "unsupported_pricing_type"},
		{"duplicate", PublicHomeDraft{Revision: 4, FeaturedModelKeys: []string{"alpha", "alpha"}}, []models.PublicPriceSnapshotItem{good}, "duplicate_model_key"},
		{"dangerous announcement", PublicHomeDraft{Revision: 4, Announcements: []PublicHomeAnnouncementDraft{{GUID: "1", Title: "x", BodyMarkdown: "[x](javascript:alert(1))", IsVisible: true}}}, []models.PublicPriceSnapshotItem{good}, "unsafe_url"},
		{"empty FAQ", PublicHomeDraft{Revision: 4, FAQs: []PublicHomeFAQDraft{{GUID: "2", Question: "x", AnswerMarkdown: "[](/about)", IsVisible: true}}}, []models.PublicPriceSnapshotItem{good}, "empty_sanitized_content"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, issues := preparePublicContent(draft, tc.home, price, tc.items)
			if !hasPublicContentIssue(issues, tc.code) {
				t.Fatalf("issues=%+v", issues)
			}
		})
	}
}

func TestPriceStructuredHomeRebindPreservesFeaturedGenerationAndV1Compatibility(t *testing.T) {
	draft := PublicContentDraft{Revision: 7, Home: "legacy", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true}
	home := PublicHomeDraft{Revision: 7, FeaturedModelKeys: []string{"beta", "alpha"}}
	oldPrice := models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 4}
	items := []models.PublicPriceSnapshotItem{
		{ModelKey: "alpha", UpstreamModelID: "org/alpha", InputPriceUSDPerMillionTokens: snapshotStringPointer("1"), OutputPriceUSDPerMillionTokens: snapshotStringPointer("2"), PricingType: "token"},
		{ModelKey: "beta", UpstreamModelID: "org/beta", InputPriceUSDPerMillionTokens: snapshotStringPointer("3"), OutputPriceUSDPerMillionTokens: snapshotStringPointer("4"), PricingType: "token"},
	}
	prepared, issues := preparePublicContent(draft, home, oldPrice, items)
	if len(issues) != 0 {
		t.Fatalf("issues=%+v", issues)
	}
	release := models.PublicContentRelease{SourceRevision: draft.Revision, Payload: prepared.Payload, ContentHash: prepared.Hash}
	newPrice := models.PublicPriceSnapshot{ID: 9, Guid: 90, Version: 5}
	rebound, err := prepareContentReleaseRebinding(release, newPrice, items)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePublishedContentPayloadV2(rebound.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.HomeConfig.FeaturedModelKeys, []string{"beta", "alpha"}) || decoded.PriceSnapshotGUID != "90" || decoded.PriceSnapshotVersion != 5 || rebound.Hash == prepared.Hash {
		t.Fatalf("rebound=%#v decoded=%#v", rebound, decoded)
	}
	if _, err = prepareContentReleaseRebinding(release, newPrice, items[:1]); status(err) != 409 {
		t.Fatalf("missing featured model rebind=%v", err)
	}

	legacyPayload := models.JSONMap{"home": "Home", "about": "About", "terms": "Terms", "privacy": "Privacy", "legal_reviewed": true, "model_keys": []string{"alpha"}, "price_snapshot_guid": "80", "price_snapshot_version": int64(4)}
	legacyHash, _ := hashPublicContentPayload(legacyPayload)
	legacy, err := prepareContentReleaseRebinding(models.PublicContentRelease{Payload: legacyPayload, ContentHash: legacyHash}, newPrice, items)
	if err != nil {
		t.Fatal(err)
	}
	if _, synthesized := legacy.Payload["home_config"]; synthesized {
		t.Fatalf("legacy release synthesized structured home: %#v", legacy.Payload)
	}
	if _, upgraded := legacy.Payload["schema_version"]; upgraded {
		t.Fatalf("legacy release upgraded schema: %#v", legacy.Payload)
	}
}

func TestStructuredHomeReleaseIntegrityRejectsUnknownOrDraftFields(t *testing.T) {
	payload := models.JSONMap{
		"schema_version": int64(2),
		"home_config": publishedHomeConfig{
			Announcements: []publishedAnnouncement{}, FAQs: []publishedFAQ{}, FeaturedModelKeys: []string{},
		},
		"home": "<p>home</p>\n", "about": "<p>about</p>\n", "terms": "<p>terms</p>\n", "privacy": "<p>privacy</p>\n",
		"legal_reviewed": true, "price_snapshot_guid": "80", "price_snapshot_version": int64(4),
	}
	makeRelease := func(candidate models.JSONMap) models.PublicContentRelease {
		hash, err := hashPublicContentPayload(candidate)
		if err != nil {
			t.Fatal(err)
		}
		return models.PublicContentRelease{Payload: candidate, ContentHash: hash}
	}
	if err := verifyPublicContentRelease(makeRelease(payload)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"body_markdown", "is_visible", "credential"} {
		copy := clonePublicContentPayload(payload)
		copy[field] = "forbidden"
		if err := verifyPublicContentRelease(makeRelease(copy)); status(err) != 503 {
			t.Fatalf("unknown field %q accepted: %v", field, err)
		}
	}
}

func TestStructuredHomeReleaseIntegrityRequiresEveryCanonicalField(t *testing.T) {
	draft := PublicContentDraft{Revision: 4, Home: "", About: "", Terms: "", Privacy: "", LegalReviewed: true}
	home := PublicHomeDraft{
		Revision:      4,
		Announcements: []PublicHomeAnnouncementDraft{{GUID: "10", Title: "notice", BodyMarkdown: "body", EffectiveAt: nil, IsVisible: true, SortOrder: 0}},
		FAQs:          []PublicHomeFAQDraft{{GUID: "20", Question: "question", AnswerMarkdown: "answer", IsVisible: true, SortOrder: 0}},
	}
	prepared, issues := preparePublicContent(draft, home, models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 4}, nil)
	if len(issues) != 0 {
		t.Fatalf("issues=%+v", issues)
	}
	makeRelease := func(payload models.JSONMap) models.PublicContentRelease {
		hash, err := hashPublicContentPayload(payload)
		if err != nil {
			t.Fatal(err)
		}
		return models.PublicContentRelease{Payload: payload, ContentHash: hash}
	}
	if err := verifyPublicContentRelease(makeRelease(prepared.Payload)); err != nil {
		t.Fatalf("explicit null and zero rejected: %v", err)
	}
	for _, field := range []string{"schema_version", "home_config", "home", "about", "terms", "privacy", "legal_reviewed", "price_snapshot_guid", "price_snapshot_version"} {
		candidate := clonePublicContentPayload(prepared.Payload)
		delete(candidate, field)
		if err := verifyPublicContentRelease(makeRelease(candidate)); status(err) != 503 {
			t.Fatalf("missing top-level field %q accepted: %v", field, err)
		}
	}
	for _, field := range []string{"announcements", "faqs", "featured_model_keys"} {
		candidate := clonePublicContentPayload(prepared.Payload)
		config := candidate["home_config"].(models.JSONMap)
		delete(config, field)
		if err := verifyPublicContentRelease(makeRelease(candidate)); status(err) != 503 {
			t.Fatalf("missing home_config field %q accepted: %v", field, err)
		}
	}
	for _, tc := range []struct {
		collection string
		field      string
	}{
		{"announcements", "guid"},
		{"announcements", "title"},
		{"announcements", "body_html"},
		{"announcements", "effective_at"},
		{"announcements", "sort_order"},
		{"faqs", "guid"},
		{"faqs", "question"},
		{"faqs", "answer_html"},
		{"faqs", "sort_order"},
	} {
		candidate := clonePublicContentPayload(prepared.Payload)
		config := candidate["home_config"].(models.JSONMap)
		rows := config[tc.collection].([]models.JSONMap)
		delete(rows[0], tc.field)
		if err := verifyPublicContentRelease(makeRelease(candidate)); status(err) != 503 {
			t.Fatalf("missing %s.%s accepted: %v", tc.collection, tc.field, err)
		}
	}
	for _, tc := range []struct {
		object     string
		collection string
	}{
		{"home_config", ""},
		{"announcement", "announcements"},
		{"faq", "faqs"},
	} {
		candidate := clonePublicContentPayload(prepared.Payload)
		config := candidate["home_config"].(models.JSONMap)
		if tc.collection == "" {
			config["unknown"] = true
		} else {
			config[tc.collection].([]models.JSONMap)[0]["unknown"] = true
		}
		if err := verifyPublicContentRelease(makeRelease(candidate)); status(err) != 503 {
			t.Fatalf("unknown %s field accepted: %v", tc.object, err)
		}
	}
	wrongTypes := []struct {
		name   string
		mutate func(models.JSONMap)
	}{
		{"schema_version", func(payload models.JSONMap) { payload["schema_version"] = "2" }},
		{"home_config", func(payload models.JSONMap) { payload["home_config"] = []any{} }},
		{"home", func(payload models.JSONMap) { payload["home"] = 1 }},
		{"about", func(payload models.JSONMap) { payload["about"] = true }},
		{"terms", func(payload models.JSONMap) { payload["terms"] = []any{} }},
		{"privacy", func(payload models.JSONMap) { payload["privacy"] = models.JSONMap{} }},
		{"legal_reviewed", func(payload models.JSONMap) { payload["legal_reviewed"] = "true" }},
		{"price_snapshot_guid", func(payload models.JSONMap) { payload["price_snapshot_guid"] = 80 }},
		{"price_snapshot_version", func(payload models.JSONMap) { payload["price_snapshot_version"] = "4" }},
		{"announcements", func(payload models.JSONMap) { payload["home_config"].(models.JSONMap)["announcements"] = "invalid" }},
		{"faqs", func(payload models.JSONMap) { payload["home_config"].(models.JSONMap)["faqs"] = "invalid" }},
		{"featured_model_keys", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["featured_model_keys"] = "invalid"
		}},
		{"announcement.guid", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["announcements"].([]models.JSONMap)[0]["guid"] = 10
		}},
		{"announcement.title", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["announcements"].([]models.JSONMap)[0]["title"] = true
		}},
		{"announcement.body_html", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["announcements"].([]models.JSONMap)[0]["body_html"] = []any{}
		}},
		{"announcement.effective_at", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["announcements"].([]models.JSONMap)[0]["effective_at"] = 1
		}},
		{"announcement.sort_order", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["announcements"].([]models.JSONMap)[0]["sort_order"] = nil
		}},
		{"faq.guid", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["faqs"].([]models.JSONMap)[0]["guid"] = 20
		}},
		{"faq.question", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["faqs"].([]models.JSONMap)[0]["question"] = true
		}},
		{"faq.answer_html", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["faqs"].([]models.JSONMap)[0]["answer_html"] = []any{}
		}},
		{"faq.sort_order", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["faqs"].([]models.JSONMap)[0]["sort_order"] = nil
		}},
	}
	for _, tc := range wrongTypes {
		candidate := clonePublicContentPayload(prepared.Payload)
		tc.mutate(candidate)
		if err := verifyPublicContentRelease(makeRelease(candidate)); status(err) != 503 {
			t.Fatalf("wrong type for %s accepted: %v", tc.name, err)
		}
	}
}

func TestStructuredHomeReleaseIntegrityRequiresCanonicalPublishedHTML(t *testing.T) {
	draft := PublicContentDraft{Revision: 4, Home: "", About: "", Terms: "", Privacy: "", LegalReviewed: true}
	home := PublicHomeDraft{
		Revision:      4,
		Announcements: []PublicHomeAnnouncementDraft{{GUID: "10", Title: "notice", BodyMarkdown: "paragraph\n\n- item\n\n[link](/about)\n\n`code`", IsVisible: true}},
		FAQs:          []PublicHomeFAQDraft{{GUID: "20", Question: "question", AnswerMarkdown: "answer", IsVisible: true}},
	}
	prepared, issues := preparePublicContent(draft, home, models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 4}, nil)
	if len(issues) != 0 {
		t.Fatalf("issues=%+v", issues)
	}
	makeRelease := func(payload models.JSONMap) models.PublicContentRelease {
		hash, err := hashPublicContentPayload(payload)
		if err != nil {
			t.Fatal(err)
		}
		return models.PublicContentRelease{Payload: payload, ContentHash: hash}
	}
	if err := verifyPublicContentRelease(makeRelease(prepared.Payload)); err != nil {
		t.Fatalf("renderer output or empty legacy documents rejected: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(models.JSONMap)
	}{
		{"legacy document", func(payload models.JSONMap) { payload["home"] = "**raw markdown**" }},
		{"announcement", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["announcements"].([]models.JSONMap)[0]["body_html"] = "**raw markdown**"
		}},
		{"faq", func(payload models.JSONMap) {
			payload["home_config"].(models.JSONMap)["faqs"].([]models.JSONMap)[0]["answer_html"] = "**raw markdown**"
		}},
	}
	cloned := clonePublicContentPayload(prepared.Payload)
	if _, err := decodePublishedContentPayloadV2(cloned); err != nil {
		originalJSON, _ := json.Marshal(prepared.Payload["home_config"])
		clonedJSON, _ := json.Marshal(cloned["home_config"])
		t.Fatalf("unmodified payload clone failed decode: %v original=%s cloned=%s", err, originalJSON, clonedJSON)
	}
	for _, tc := range mutations {
		candidate := clonePublicContentPayload(prepared.Payload)
		tc.mutate(candidate)
		if _, err := decodePublishedContentPayloadV2(candidate); err == nil {
			t.Fatalf("raw Markdown in %s passed typed decode", tc.name)
		}
		if err := verifyPublicContentRelease(makeRelease(candidate)); status(err) != 503 {
			t.Fatalf("raw Markdown in %s accepted: %v", tc.name, err)
		}
	}
}

func TestStructuredHomeReleaseRejectsMixedSchemaDispatch(t *testing.T) {
	draft := PublicContentDraft{Revision: 4, Home: "home", About: "about", Terms: "terms", Privacy: "privacy", LegalReviewed: true}
	prepared, issues := preparePublicContent(draft, PublicHomeDraft{Revision: 4}, models.PublicPriceSnapshot{ID: 8, Guid: 80, Version: 4}, nil)
	if len(issues) != 0 {
		t.Fatalf("issues=%+v", issues)
	}
	legacy := models.JSONMap{
		"home": "home", "about": "about", "terms": "terms", "privacy": "privacy", "legal_reviewed": true,
		"model_keys": []string{}, "price_snapshot_guid": "80", "price_snapshot_version": int64(4),
	}
	cases := []struct {
		name    string
		payload models.JSONMap
	}{
		{"string schema version with legacy keys", func() models.JSONMap {
			payload := clonePublicContentPayload(prepared.Payload)
			payload["schema_version"] = "2"
			payload["model_keys"] = []string{}
			return payload
		}()},
		{"legacy with home config", func() models.JSONMap {
			payload := clonePublicContentPayload(legacy)
			payload["home_config"] = publishedHomeConfigPayload(publishedHomeConfig{Announcements: []publishedAnnouncement{}, FAQs: []publishedFAQ{}, FeaturedModelKeys: []string{}})
			return payload
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := hashPublicContentPayload(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			release := models.PublicContentRelease{Payload: tc.payload, ContentHash: hash}
			if err = verifyPublicContentRelease(release); status(err) != 503 {
				t.Fatalf("mixed payload passed release verification: %v", err)
			}
			if _, err = publishedContentModelKeys(tc.payload); err == nil {
				t.Fatal("mixed payload passed model-key extraction")
			}
			if _, err = prepareContentReleaseRebinding(release, models.PublicPriceSnapshot{ID: 9, Guid: 90, Version: 5}, nil); status(err) != 503 {
				t.Fatalf("mixed payload passed price rebind: %v", err)
			}
			if _, err = prepareRestoredContentReleaseTx(nil, nil, release, models.PublicPriceSnapshot{ID: 9, Guid: 90, Version: 5}, nil); status(err) != 422 {
				t.Fatalf("mixed payload passed restore dispatch: %v", err)
			}
		})
	}
}

func TestPublicContentModelReferencesSuppressHTMLAndMarkdownCode(t *testing.T) {
	raw := "prefix <CoDe class='sample'>\n<a href='/pricing/code-a'>code</a>\n<strong><a href='/pricing/code-b'>nested</a></strong>\n</cOdE> after\ninside <PRE data-x='1'>\n<a href='/pricing/pre-a'>pre</a>\n</pre> after\n\n`[span](/pricing/span)`\n```md\n[fence](/pricing/fence)\n```\n[real][m] <a title='ok' HREF='/pricing/html'>html</a>\n\n[m]: /pricing/reference"
	got := extractPublicContentModelReferences(raw)
	want := []string{"html", "reference"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
}

func TestPublicContentModelReferencesHandleMalformedAllowedHTMLState(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{"open_code_fragment", "<code>example\n\n<a href='/pricing/hidden'>hidden</a>\n\n</code> <a href='/pricing/visible'>visible</a>", []string{"visible"}},
		{"nested_code_pre", "<pre><code><a href='/pricing/hidden'>x</a></code></pre><a href='/pricing/visible'>v</a>", []string{"visible"}},
		{"malformed_unclosed_code", "<code><a href='/pricing/hidden'>x</a><strong>still code</strong>", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractPublicContentModelReferences(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%#v want=%#v", got, tc.want)
			}
		})
	}
}

func TestPublicContentModelReferencesRequireExactRoutableURL(t *testing.T) {
	valid := "[inline](/pricing/alpha) [ref][m] <a href='/pricing/html'>h</a>\n\n[m]: /pricing/reference"
	if got := extractPublicContentModelReferences(valid); !reflect.DeepEqual(got, []string{"alpha", "html", "reference"}) {
		t.Fatalf("valid=%#v", got)
	}
	for _, raw := range []string{"[x](/PRICING/ALPHA)", "[x](/pricing/al%0Apha)", "[x](/pricing/al%E2%80%8Bpha)", "[x](/pricing/al\\ pha)", "[x](/pricing/alpha?q=1)", "[x](/pricing/alpha#x)", "[x](/pricing/alpha/)", "[x](/pricing/alpha/more)", "<a href='/pricing/ALPHA'>x</a>", "<a href='/pricing/al&#x0A;pha'>x</a>"} {
		if got := extractPublicContentModelReferences(raw); len(got) != 0 {
			t.Fatalf("manufactured model reference from %q: %#v", raw, got)
		}
	}
}

func TestPublicContentStableRequestPayloadBinding(t *testing.T) {
	a := publicContentRequestPayload("publish", 3, "91", 0)
	if a != publicContentRequestPayload("publish", 3, "91", 0) || a == publicContentRequestPayload("publish", 4, "91", 0) || a == publicContentRequestPayload("publish", 3, "92", 0) {
		t.Fatal("unstable or incomplete request payload")
	}
}

func TestPublicContentReleaseIntegrityRejectsTamperingAndMalformedPayload(t *testing.T) {
	payload := models.JSONMap{"home": "Home", "about": "About", "terms": "Terms", "privacy": "Privacy", "legal_reviewed": true, "model_keys": []string{"alpha"}, "price_snapshot_guid": "80", "price_snapshot_version": int64(4)}
	hash, err := hashPublicContentPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	release := models.PublicContentRelease{Payload: payload, ContentHash: hash}
	if err = verifyPublicContentRelease(release); err != nil {
		t.Fatal(err)
	}
	badHash := release
	badHash.ContentHash = strings.Repeat("0", 64)
	if err = verifyPublicContentRelease(badHash); status(err) != 503 {
		t.Fatalf("bad hash=%v", err)
	}
	items := []models.PublicPriceSnapshotItem{{ModelKey: "alpha", UpstreamModelID: "org/alpha", InputPriceUSDPerMillionTokens: snapshotStringPointer("1"), OutputPriceUSDPerMillionTokens: snapshotStringPointer("2")}}
	if err = validateContentReleaseForPriceItems(badHash, items); status(err) != 503 {
		t.Fatalf("validation accepted bad hash=%v", err)
	}
	if _, err = prepareContentReleaseRebinding(badHash, models.PublicPriceSnapshot{ID: 9, Guid: 90, Version: 5}, items); status(err) != 503 {
		t.Fatalf("rebinding accepted bad hash=%v", err)
	}
	tampered := release
	tampered.Payload = models.JSONMap{}
	for k, v := range payload {
		tampered.Payload[k] = v
	}
	tampered.Payload["home"] = "changed"
	if err = verifyPublicContentRelease(tampered); status(err) != 503 {
		t.Fatalf("tampered=%v", err)
	}
	malformed := release
	malformed.Payload = models.JSONMap{"home": 7, "about": "About", "terms": "Terms", "privacy": "Privacy", "legal_reviewed": true, "price_snapshot_guid": "80", "price_snapshot_version": 4}
	malformed.ContentHash, _ = hashPublicContentPayload(malformed.Payload)
	if err = verifyPublicContentRelease(malformed); status(err) != 503 {
		t.Fatalf("malformed=%v", err)
	}
}

func TestPublicContentReleaseIntegrityRequiresCanonicalBindingFields(t *testing.T) {
	canonical := models.JSONMap{"home": "Home", "about": "About", "terms": "Terms", "privacy": "Privacy", "legal_reviewed": true, "model_keys": []string{"alpha", "beta"}, "price_snapshot_guid": "80", "price_snapshot_version": int64(4)}
	releaseFor := func(changes models.JSONMap) models.PublicContentRelease {
		payload := models.JSONMap{}
		for key, value := range canonical {
			payload[key] = value
		}
		for key, value := range changes {
			payload[key] = value
		}
		hash, err := hashPublicContentPayload(payload)
		if err != nil {
			t.Fatal(err)
		}
		return models.PublicContentRelease{Payload: payload, ContentHash: hash}
	}
	if err := verifyPublicContentRelease(releaseFor(nil)); err != nil {
		t.Fatalf("canonical payload rejected: %v", err)
	}
	for _, guid := range []string{"01", "+1", " 1", "1 ", "0", "-1", "9223372036854775808"} {
		if err := verifyPublicContentRelease(releaseFor(models.JSONMap{"price_snapshot_guid": guid})); status(err) != 503 {
			t.Fatalf("noncanonical guid %q accepted: %v", guid, err)
		}
	}
	for name, keys := range map[string][]string{
		"duplicate":    {"alpha", "alpha"},
		"out_of_order": {"beta", "alpha"},
		"empty":        {""},
		"invalid":      {"Alpha"},
	} {
		bad := releaseFor(models.JSONMap{"model_keys": keys})
		if err := verifyPublicContentRelease(bad); status(err) != 503 {
			t.Fatalf("%s keys accepted: %v", name, err)
		}
		if err := validateContentReleaseForPriceItems(bad, nil); status(err) != 503 {
			t.Fatalf("validation accepted %s keys: %v", name, err)
		}
	}
}
