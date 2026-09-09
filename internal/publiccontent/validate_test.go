package publiccontent

import (
	"reflect"
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
		{Field: "models[0].price.input", Code: "missing_price"},
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
