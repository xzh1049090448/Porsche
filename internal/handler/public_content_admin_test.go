package handler

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/publiccontent"
)

func TestPublicContentValidationResponseUsesArrayAndPreservesIssues(t *testing.T) {
	raw, err := json.Marshal(publicContentValidationResponse(nil))
	if err != nil || string(raw) != `{"issues":[],"valid":true}` {
		t.Fatalf("empty validation response=%s err=%v", raw, err)
	}
	issues := []publiccontent.ValidationIssue{{Field: "terms", Code: "legal_review_required"}, {Field: "home", Code: "unknown_home_model"}}
	raw, err = json.Marshal(publicContentValidationResponse(issues))
	if err != nil || string(raw) != `{"issues":[{"field":"terms","code":"legal_review_required"},{"field":"home","code":"unknown_home_model"}],"valid":false}` {
		t.Fatalf("invalid validation response=%s err=%v", raw, err)
	}
	if bytes.Contains(raw, []byte(`"Field"`)) || bytes.Contains(raw, []byte(`"Code"`)) {
		t.Fatalf("validation response leaked Go field names: %s", raw)
	}
}

func TestPublicAdminPaginationExactValues(t *testing.T) {
	for _, q := range []map[string]string{{"page": "0"}, {"page_size": "21"}, {"page": "01"}} {
		p, _ := parsePublicAdminPage(q)
		if p >= 0 {
			t.Fatalf("accepted %#v", q)
		}
	}
}
