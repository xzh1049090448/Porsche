package dto

import (
	"strings"
	"testing"
)

func TestValidateNoDuplicateJSONRecursive(t *testing.T) {
	for _, raw := range []string{
		`{"items":[{"price":"1","price":"2"}]}`,
		`{"intent":{"target_guid":"1","target_\u0067uid":"2"}}`,
		`{"payload":{"nested":{"value":1,"value":2}}}`,
		`[{"a":1},{"b":{"c":1,"\u0063":2}}]`,
	} {
		if err := ValidateNoDuplicateJSON([]byte(raw), 64); err == nil {
			t.Fatalf("accepted recursive duplicate %s", raw)
		}
	}
	if err := ValidateNoDuplicateJSON([]byte(`{"items":[{"price":"1"}],"intent":{"target_guid":"1"}}`), 64); err != nil {
		t.Fatalf("valid nested JSON: %v", err)
	}
	deep := strings.Repeat(`[`, 65) + `0` + strings.Repeat(`]`, 65)
	if err := ValidateNoDuplicateJSON([]byte(deep), 64); err == nil {
		t.Fatal("accepted excessive nesting")
	}
}
