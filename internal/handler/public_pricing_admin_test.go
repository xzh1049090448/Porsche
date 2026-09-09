package handler

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicAdminJSONRejectsDuplicateUnknownTrailingAndOversize(t *testing.T) {
	for _, body := range []string{`{"expected_revision":1,"expected_revision":2}`, `{"unknown":1}`, `{"expected_revision":1} {}`, strings.Repeat(" ", int(publicAdminBodyLimit+1))} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		var out struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if publicAdminStrictJSON(c, &out) {
			t.Fatalf("accepted invalid body")
		}
	}
}
