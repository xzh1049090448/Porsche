package handler

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicAdminHeaderBoundaryRejectsSecurityHeadersOnWrongRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin/v2/public-pricing/draft", publicAdminHeaderBoundary, func(c *gin.Context) { c.Status(200) })
	for _, name := range []string{"Idempotency-Key", "X-Action-Ticket"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/admin/v2/public-pricing/draft", nil)
		req.Header.Set(name, "unexpected")
		r.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("%s status=%d", name, rec.Code)
		}
	}
}

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
