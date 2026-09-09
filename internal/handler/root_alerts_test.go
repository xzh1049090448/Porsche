package handler

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicAdminPaginationDefaults(t *testing.T) {
	p, s := parsePublicAdminPage(map[string]string{})
	if p != 1 || s != 20 {
		t.Fatalf("got %d %d", p, s)
	}
}

func TestPublicAdminNoBodyRejectsUnknownLengthContent(t *testing.T) {
	for _, body := range []string{" ", `{}`} {
		req := httptest.NewRequest("POST", "/", io.NopCloser(strings.NewReader(body)))
		req.ContentLength = -1
		if publicAdminRequestHasNoBody(req) {
			t.Fatalf("accepted %q", body)
		}
	}
	req := httptest.NewRequest("POST", "/", io.NopCloser(strings.NewReader("")))
	req.ContentLength = -1
	if !publicAdminRequestHasNoBody(req) {
		t.Fatal("rejected empty unknown-length body")
	}
}
