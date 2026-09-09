package handler

import "testing"

func TestPublicAdminPaginationExactValues(t *testing.T) {
	for _, q := range []map[string]string{{"page": "0"}, {"page_size": "21"}, {"page": "01"}} {
		p, _ := parsePublicAdminPage(q)
		if p >= 0 {
			t.Fatalf("accepted %#v", q)
		}
	}
}
