package handler

import "testing"

func TestPublicAdminGUIDIsCanonical(t *testing.T) {
	for _, v := range []string{"", "0", "01", "-1", "9223372036854775808", "1x"} {
		if _, ok := publicAdminGUID(v); ok {
			t.Fatalf("accepted %q", v)
		}
	}
	if v, ok := publicAdminGUID("123"); !ok || v != 123 {
		t.Fatal("canonical guid rejected")
	}
}
func TestPublicAdminQueryRejectsUnknownAndDuplicate(t *testing.T) {
	allowed := map[string]bool{"page": true}
	for _, raw := range []string{"x=1", "page=1&page=2", "page"} {
		if _, ok := publicAdminQuery(raw, allowed); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
}
