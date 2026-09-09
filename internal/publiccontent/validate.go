package publiccontent

import (
	"regexp"
	"strings"
)

const (
	CurrencyUSD       = "USD"
	UnitMillionTokens = "million_tokens"
)

var modelKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,127}$`)
var prototypeClaimPattern = regexp.MustCompile(`(?i)(40\+|100%|\bmit\b)`)

// ValidationIssue is stable machine-readable publication feedback. Field and
// Code deliberately contain no submitted content or upstream response data.
type ValidationIssue struct {
	Field string
	Code  string
}

type Price struct {
	Currency string
	Unit     string
	Input    string
	Output   string
}

type Model struct {
	ModelKey        string
	UpstreamModelID string
	Active          bool
	Price           Price
}

type DocumentKind string

const (
	DocumentHome    DocumentKind = "home"
	DocumentTerms   DocumentKind = "terms"
	DocumentPrivacy DocumentKind = "privacy"
)

type Document struct {
	Kind     DocumentKind
	Reviewed bool
	Body     string
}

// Publication is the pure validation projection used before persistence. It
// contains no clients, credentials, or I/O hooks by design.
type Publication struct {
	Models        []Model
	HomeModelKeys []string
	Documents     []Document
}

func ValidModelKey(value string) bool {
	return modelKeyPattern.MatchString(value) && !strings.HasSuffix(value, "-") && !strings.Contains(value, "--")
}

// ValidatePublication deterministically validates an in-memory publication.
// It has no network, database, filesystem, or clock dependency.
func ValidatePublication(publication Publication) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	modelKeys := make(map[string]struct{}, len(publication.Models))
	upstreamIDs := make(map[string]struct{}, len(publication.Models))
	activeModelKeys := make(map[string]struct{}, len(publication.Models))

	for i, model := range publication.Models {
		base := "models[" + strconvItoa(i) + "]"
		if !ValidModelKey(model.ModelKey) {
			issues = append(issues, ValidationIssue{Field: base + ".model_key", Code: "invalid_model_key"})
		}
		if _, duplicate := modelKeys[model.ModelKey]; duplicate {
			issues = append(issues, ValidationIssue{Field: base + ".model_key", Code: "duplicate_model_key"})
		} else {
			modelKeys[model.ModelKey] = struct{}{}
		}
		if _, duplicate := upstreamIDs[model.UpstreamModelID]; duplicate {
			issues = append(issues, ValidationIssue{Field: base + ".upstream_model_id", Code: "duplicate_upstream_model_id"})
		} else {
			upstreamIDs[model.UpstreamModelID] = struct{}{}
		}
		issues = append(issues, validatePrice(base+".price", model.Price)...)
		if model.Active && ValidModelKey(model.ModelKey) {
			activeModelKeys[model.ModelKey] = struct{}{}
		}
	}

	for i, modelKey := range publication.HomeModelKeys {
		if _, ok := activeModelKeys[modelKey]; !ok {
			issues = append(issues, ValidationIssue{Field: "home.model_keys[" + strconvItoa(i) + "]", Code: "unknown_home_model"})
		}
	}

	for _, kind := range []DocumentKind{DocumentTerms, DocumentPrivacy} {
		count := 0
		reviewed := true
		for _, document := range publication.Documents {
			if document.Kind == kind {
				count++
				reviewed = reviewed && document.Reviewed
			}
		}
		if count != 1 || !reviewed {
			issues = append(issues, ValidationIssue{Field: "documents." + string(kind) + ".review", Code: "legal_review_required"})
		}
	}
	for _, document := range publication.Documents {
		field := "documents." + string(document.Kind) + ".body"
		if prototypeClaimPattern.MatchString(normalizedRenderedText(document.Body)) {
			issues = append(issues, ValidationIssue{Field: field, Code: "unsubstantiated_prototype_claim"})
		}
		_, sanitizeIssues := SanitizeMarkdown(document.Body)
		for _, issue := range sanitizeIssues {
			issues = append(issues, ValidationIssue{Field: field, Code: issue.Code})
		}
	}
	return issues
}

func validatePrice(field string, price Price) []ValidationIssue {
	issues := make([]ValidationIssue, 0, 4)
	if price.Currency != CurrencyUSD {
		issues = append(issues, ValidationIssue{Field: field + ".currency", Code: "invalid_currency"})
	}
	if price.Unit != UnitMillionTokens {
		issues = append(issues, ValidationIssue{Field: field + ".unit", Code: "invalid_price_unit"})
	}
	for _, component := range []struct {
		name  string
		value string
	}{{"input", price.Input}, {"output", price.Output}} {
		if component.value == "" {
			issues = append(issues, ValidationIssue{Field: field + "." + component.name, Code: "missing_price"})
			continue
		}
		if _, err := ParseDecimal(component.value); err != nil {
			issues = append(issues, ValidationIssue{Field: field + "." + component.name, Code: "invalid_price"})
		}
	}
	return issues
}

func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte(value%10) + '0'
		value /= 10
	}
	return string(digits[i:])
}
