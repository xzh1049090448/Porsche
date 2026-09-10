package publiccontent

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidatePublicationAcceptsReviewedSafePublication(t *testing.T) {
	publication := Publication{
		Models: []Model{
			{
				ModelKey:        "gpt-4o-mini",
				UpstreamModelID: "vendor/gpt-4o-mini",
				Active:          true,
				Price:           Price{Currency: CurrencyUSD, Unit: UnitMillionTokens, Input: "12.34567890", Output: "3.21000000"},
			},
		},
		HomeModelKeys: []string{"gpt-4o-mini"},
		Documents: []Document{
			{Kind: DocumentHome, Reviewed: true, Body: "# Welcome\n\nSee [pricing](/pricing)."},
			{Kind: DocumentTerms, Reviewed: true, Body: "Terms of service"},
			{Kind: DocumentPrivacy, Reviewed: true, Body: "Privacy notice"},
		},
	}

	if issues := ValidatePublication(publication); len(issues) != 0 {
		t.Fatalf("ValidatePublication() issues = %#v", issues)
	}
}

func TestValidatePublicationReturnsStableIssuesForInvalidInput(t *testing.T) {
	publication := Publication{
		Models: []Model{
			{
				ModelKey:        "GPT/4",
				UpstreamModelID: "duplicate",
				Active:          true,
				Price:           Price{Currency: "EUR", Unit: "request", Input: "", Output: "-1"},
			},
			{
				ModelKey:        "GPT/4",
				UpstreamModelID: "duplicate",
				Active:          true,
				Price:           Price{Currency: CurrencyUSD, Unit: UnitMillionTokens, Input: "1", Output: "1"},
			},
		},
		HomeModelKeys: []string{"missing-model"},
		Documents: []Document{
			{Kind: DocumentHome, Reviewed: true, Body: "40+ models with 100% uptime under MIT"},
			{Kind: DocumentTerms, Reviewed: false, Body: "Pending terms"},
			{Kind: DocumentPrivacy, Reviewed: false, Body: "Pending privacy"},
		},
	}

	want := []ValidationIssue{
		{Field: "models[0].model_key", Code: "invalid_model_key"},
		{Field: "models[0].price.currency", Code: "invalid_currency"},
		{Field: "models[0].price.unit", Code: "invalid_price_unit"},
		{Field: "models[0].price.output", Code: "invalid_price"},
		{Field: "models[1].model_key", Code: "invalid_model_key"},
		{Field: "models[1].model_key", Code: "duplicate_model_key"},
		{Field: "models[1].upstream_model_id", Code: "duplicate_upstream_model_id"},
		{Field: "home.model_keys[0]", Code: "unknown_home_model"},
		{Field: "documents.terms.review", Code: "legal_review_required"},
		{Field: "documents.privacy.review", Code: "legal_review_required"},
		{Field: "documents.home.body", Code: "unsubstantiated_prototype_claim"},
	}
	if got := ValidatePublication(publication); !reflect.DeepEqual(got, want) {
		t.Fatalf("ValidatePublication() = %#v, want %#v", got, want)
	}
}

func TestValidateModelKeyRequiresStablePermanentSafeShape(t *testing.T) {
	for _, key := range []string{"gpt-4o", "model2", "a-b-c", "x9"} {
		t.Run("valid_"+key, func(t *testing.T) {
			if !ValidModelKey(key) {
				t.Fatalf("ValidModelKey(%q) = false", key)
			}
		})
	}
	for _, key := range []string{"", "GPT-4", "gpt_4", "gpt/4", "-gpt", "gpt-", "gpt--4", "gpt 4", "gpt%2f4"} {
		t.Run("invalid_"+key, func(t *testing.T) {
			if ValidModelKey(key) {
				t.Fatalf("ValidModelKey(%q) = true", key)
			}
		})
	}
}

func TestValidatePublicationRequiresSafeUpstreamModelReference(t *testing.T) {
	for _, test := range []struct {
		name string
		id   string
		code string
	}{
		{name: "empty", id: "", code: "missing_upstream_model_id"},
		{name: "whitespace", id: " \t\n", code: "missing_upstream_model_id"},
		{name: "empty_segment", id: "org//model", code: "invalid_upstream_model_id"},
		{name: "traversal", id: "org/../model", code: "invalid_upstream_model_id"},
		{name: "escaped_path", id: "org%2Fmodel", code: "invalid_upstream_model_id"},
		{name: "query", id: "org/model?version=1", code: "invalid_upstream_model_id"},
		{name: "control", id: "org/\u200bmodel", code: "invalid_upstream_model_id"},
		{name: "internal_space", id: "org/model name", code: "invalid_upstream_model_id"},
		{name: "overlong", id: strings.Repeat("a", 256), code: "invalid_upstream_model_id"},
		{name: "invalid_utf8", id: string([]byte{0xff}), code: "invalid_upstream_model_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			issues := ValidatePublication(publicationWithModels(Model{ModelKey: "gpt-4o-mini", UpstreamModelID: test.id, Active: true, Price: validPrice()}))
			if !hasIssue(issues, "models[0].upstream_model_id", test.code) {
				t.Fatalf("ValidatePublication(%q) issues = %#v, missing %s", test.id, issues, test.code)
			}
		})
	}
}

func TestValidatePublicationPreservesSafeUpstreamIDsAndDuplicateOrder(t *testing.T) {
	validIDs := []string{"model-a", "zai-org/glm-5.1", "team/subteam/model-v2"}
	for _, id := range validIDs {
		t.Run("valid_"+id, func(t *testing.T) {
			issues := ValidatePublication(publicationWithModels(Model{ModelKey: "gpt-4o-mini", UpstreamModelID: id, Active: true, Price: validPrice()}))
			if hasIssueCode(issues, "missing_upstream_model_id") || hasIssueCode(issues, "invalid_upstream_model_id") {
				t.Fatalf("valid upstream model ID %q issues = %#v", id, issues)
			}
		})
	}
	issues := ValidatePublication(publicationWithModels(
		Model{ModelKey: "gpt-4o-mini", UpstreamModelID: "zai-org/glm-5.1", Active: true, Price: validPrice()},
		Model{ModelKey: "gpt-4o", UpstreamModelID: "zai-org/glm-5.1", Active: true, Price: validPrice()},
	))
	want := []ValidationIssue{{Field: "models[1].upstream_model_id", Code: "duplicate_upstream_model_id"}}
	if !reflect.DeepEqual(issues, want) {
		t.Fatalf("duplicate upstream model ID issues = %#v, want %#v", issues, want)
	}
}

func TestValidatePublicationDecodesRenderedTextBeforeCheckingPrototypeClaims(t *testing.T) {
	publication := Publication{
		Documents: []Document{
			{Kind: DocumentHome, Reviewed: true, Body: "40&#43; models, 100&#37; uptime, M&#73;T licensed"},
			{Kind: DocumentTerms, Reviewed: true, Body: "Terms"},
			{Kind: DocumentPrivacy, Reviewed: true, Body: "Privacy"},
		},
	}
	if !hasIssueCode(ValidatePublication(publication), "unsubstantiated_prototype_claim") {
		t.Fatalf("encoded prototype claim was accepted")
	}
}

func TestValidatePublicationChecksRenderedSemanticPrototypeClaimsAndIgnoresCode(t *testing.T) {
	for _, body := range []string{
		"40\\+ models", "100\\% uptime", "M*I*T licensed", "4<strong>0+</strong> models",
	} {
		t.Run(body, func(t *testing.T) {
			if !hasIssueCode(ValidatePublication(publicationWithHome(body)), "unsubstantiated_prototype_claim") {
				t.Fatalf("rendered claim was accepted: %q", body)
			}
		})
	}
	for _, body := range []string{"`40+ models`", "```text\n100% uptime\n```"} {
		t.Run("code_"+body, func(t *testing.T) {
			if hasIssueCode(ValidatePublication(publicationWithHome(body)), "unsubstantiated_prototype_claim") {
				t.Fatalf("code claim was treated as rendered text: %q", body)
			}
		})
	}
}

func TestValidatePublicationChecksVisibleSafeHTMLBlockText(t *testing.T) {
	for _, body := range []string{
		"<p>40+ models</p>",
		"<h1>100% uptime</h1>",
		"<blockquote>MIT licensed</blockquote>",
	} {
		t.Run(body, func(t *testing.T) {
			if !hasIssueCode(ValidatePublication(publicationWithHome(body)), "unsubstantiated_prototype_claim") {
				t.Fatalf("safe HTML block claim was accepted: %q", body)
			}
		})
	}
}

func TestValidatePublicationIgnoresCodeAndPreHTMLText(t *testing.T) {
	for _, body := range []string{"<code>40+ models</code>", "<pre>100% uptime</pre>"} {
		t.Run(body, func(t *testing.T) {
			if hasIssueCode(ValidatePublication(publicationWithHome(body)), "unsubstantiated_prototype_claim") {
				t.Fatalf("code-like HTML was treated as visible prose: %q", body)
			}
		})
	}
}

func TestValidatePublicationUsesCommonMarkVisibleLinkLabelText(t *testing.T) {
	for _, body := range []string{"[4](/pricing)0+ published models", "`40+``"} {
		if !hasIssueCode(ValidatePublication(publicationWithHome(body)), "unsubstantiated_prototype_claim") {
			t.Fatalf("visible CommonMark claim was accepted: %q", body)
		}
	}
}

func TestValidatePublicationChecksVisibleLinkAndImageTitles(t *testing.T) {
	for _, body := range []string{
		`[pricing](/pricing "40+ published models")`,
		`![catalog](/assets/catalog.svg "100% uptime")`,
		`<a href="/pricing" title="MIT licensed">pricing</a>`,
		`<img src="/assets/catalog.svg" alt="40+ published models">`,
	} {
		t.Run(body, func(t *testing.T) {
			if !hasIssueCode(ValidatePublication(publicationWithHome(body)), "unsubstantiated_prototype_claim") {
				t.Fatalf("visible title or alt claim was accepted: %q", body)
			}
		})
	}
}

func TestValidatePublicationRequiresExactlyOneReviewedTermsAndPrivacyDocument(t *testing.T) {
	for _, documents := range [][]Document{
		{
			{Kind: DocumentHome, Reviewed: true, Body: "Home"},
			{Kind: DocumentTerms, Reviewed: true, Body: "Terms 1"},
			{Kind: DocumentTerms, Reviewed: true, Body: "Terms 2"},
			{Kind: DocumentPrivacy, Reviewed: true, Body: "Privacy"},
		},
		{
			{Kind: DocumentHome, Reviewed: true, Body: "Home"},
			{Kind: DocumentTerms, Reviewed: true, Body: "Terms 1"},
			{Kind: DocumentTerms, Reviewed: false, Body: "Terms 2"},
			{Kind: DocumentPrivacy, Reviewed: true, Body: "Privacy"},
		},
		{
			{Kind: DocumentHome, Reviewed: true, Body: "Home"},
			{Kind: DocumentTerms, Reviewed: true, Body: "Terms"},
			{Kind: DocumentPrivacy, Reviewed: true, Body: "Privacy 1"},
			{Kind: DocumentPrivacy, Reviewed: false, Body: "Privacy 2"},
		},
	} {
		issues := ValidatePublication(Publication{Documents: documents})
		if !hasIssueCode(issues, "legal_review_required") {
			t.Fatalf("duplicate or unreviewed legal documents were accepted: %#v", issues)
		}
	}
}

func publicationWithHome(body string) Publication {
	return Publication{Documents: []Document{
		{Kind: DocumentHome, Reviewed: true, Body: body},
		{Kind: DocumentTerms, Reviewed: true, Body: "Terms"},
		{Kind: DocumentPrivacy, Reviewed: true, Body: "Privacy"},
	}}
}

func publicationWithModels(models ...Model) Publication {
	return Publication{Models: models, Documents: []Document{
		{Kind: DocumentHome, Reviewed: true, Body: "Home"},
		{Kind: DocumentTerms, Reviewed: true, Body: "Terms"},
		{Kind: DocumentPrivacy, Reviewed: true, Body: "Privacy"},
	}}
}

func validPrice() Price {
	return Price{Currency: CurrencyUSD, Unit: UnitMillionTokens, Input: "1", Output: "1"}
}

func hasIssue(issues []ValidationIssue, field, code string) bool {
	for _, issue := range issues {
		if issue.Field == field && issue.Code == code {
			return true
		}
	}
	return false
}

func FuzzValidateUpstreamModelReferenceDeterministically(f *testing.F) {
	for _, seed := range []string{"", " \t", "model-a", "zai-org/glm-5.1", "org//model", "org/../model", "org%2Fmodel", "org/model?query=1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, id string) {
		publication := publicationWithModels(Model{ModelKey: "gpt-4o-mini", UpstreamModelID: id, Active: true, Price: validPrice()})
		first := ValidatePublication(publication)
		second := ValidatePublication(publication)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("upstream reference validation was nondeterministic for %q: %#v != %#v", id, first, second)
		}
		if strings.TrimSpace(id) == "" {
			if !hasIssue(first, "models[0].upstream_model_id", "missing_upstream_model_id") {
				t.Fatalf("blank upstream reference missing required issue: %q, %#v", id, first)
			}
			return
		}
		if ValidUpstreamModelID(id) {
			if hasIssueCode(first, "missing_upstream_model_id") || hasIssueCode(first, "invalid_upstream_model_id") {
				t.Fatalf("valid upstream reference was rejected: %q, %#v", id, first)
			}
			return
		}
		if !hasIssue(first, "models[0].upstream_model_id", "invalid_upstream_model_id") {
			t.Fatalf("invalid upstream reference lacked invalid issue: %q, %#v", id, first)
		}
	})
}
