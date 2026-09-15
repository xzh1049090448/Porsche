package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

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
