package service

import (
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
	items := []models.PublicPriceSnapshotItem{{ModelKey: "alpha", UpstreamModelID: "org/alpha", InputPriceUSDPerMillionTokens: "1.00000000", OutputPriceUSDPerMillionTokens: "2.00000000"}}
	p, issues := preparePublicContent(d, price, items)
	if len(issues) != 0 || p.Hash == "" || p.Payload["home"] != d.Home {
		t.Fatalf("prepared=%#v issues=%#v", p, issues)
	}
	d.LegalReviewed = false
	if _, issues = preparePublicContent(d, price, items); !hasPublicContentIssue(issues, "legal_review_required") {
		t.Fatalf("issues=%#v", issues)
	}
	d.LegalReviewed = true
	d.Home = "Use [other](/pricing/other)"
	if _, issues = preparePublicContent(d, price, items); !hasPublicContentIssue(issues, "unknown_home_model") {
		t.Fatalf("issues=%#v", issues)
	}
	d.Home = strings.Repeat("x", PublicContentDocumentLimit+1)
	if _, issues = preparePublicContent(d, price, items); !hasPublicContentIssue(issues, "content_too_large") {
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
	items := []models.PublicPriceSnapshotItem{{ModelKey: "alpha", UpstreamModelID: "org/alpha", InputPriceUSDPerMillionTokens: "1", OutputPriceUSDPerMillionTokens: "2"}}
	_, issues := preparePublicContent(d, price, items)
	if !hasPublicContentIssue(issues, "unsubstantiated_prototype_claim") || !hasPublicContentIssue(issues, "unknown_home_model") {
		t.Fatalf("issues=%#v", issues)
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
	items := []models.PublicPriceSnapshotItem{{ModelKey: "alpha", UpstreamModelID: "org/alpha", InputPriceUSDPerMillionTokens: "1", OutputPriceUSDPerMillionTokens: "2"}}
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
