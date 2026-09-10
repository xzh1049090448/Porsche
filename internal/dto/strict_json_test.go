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
	object := func(depth int, leaf string) string {
		return strings.Repeat(`{"a":`, depth) + leaf + strings.Repeat(`}`, depth)
	}
	mixed := func(depth int) string {
		value := `0`
		for i := 0; i < depth; i++ {
			if i%2 == 0 {
				value = `[` + value + `]`
			} else {
				value = `{"a":` + value + `}`
			}
		}
		return value
	}
	for _, raw := range []string{strings.Repeat(`[`, 64) + `0` + strings.Repeat(`]`, 64), object(64, `0`), mixed(64), object(63, `{"x":1,"x":2}`)} {
		if err := ValidateNoDuplicateJSON([]byte(raw), 64); raw == object(63, `{"x":1,"x":2}`) {
			if err == nil {
				t.Fatal("accepted duplicate at depth boundary")
			}
		} else if err != nil {
			t.Fatalf("rejected exactly 64 containers: %v", err)
		}
	}
	for _, raw := range []string{strings.Repeat(`[`, 65) + `0` + strings.Repeat(`]`, 65), object(65, `0`), mixed(65)} {
		if err := ValidateNoDuplicateJSON([]byte(raw), 64); err == nil {
			t.Fatal("accepted 65 nested containers")
		}
	}
}
