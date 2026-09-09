package handler

import "testing"

func TestPublicAdminPaginationDefaults(t *testing.T) {
	p, s := parsePublicAdminPage(map[string]string{})
	if p != 1 || s != 20 {
		t.Fatalf("got %d %d", p, s)
	}
}
