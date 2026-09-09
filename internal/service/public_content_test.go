package service

import (
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

func TestPublicContentStableRequestPayloadBinding(t *testing.T) {
	a := publicContentRequestPayload("publish", 3, "91", 0)
	if a != publicContentRequestPayload("publish", 3, "91", 0) || a == publicContentRequestPayload("publish", 4, "91", 0) || a == publicContentRequestPayload("publish", 3, "92", 0) {
		t.Fatal("unstable or incomplete request payload")
	}
}
