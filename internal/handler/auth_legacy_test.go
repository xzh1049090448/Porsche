package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLegacyPhoneAuthEndpointsAreGone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterAuth(r, nil)
	for _, path := range []string{"/api/v1/auth/send-code", "/api/v1/auth/login/password", "/api/v1/auth/login/code"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusGone {
			t.Fatalf("%s status=%d, want %d", path, rec.Code, http.StatusGone)
		}
	}
	for _, path := range []string{"/api/v1/auth/register", "/api/v1/auth/login"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("removed legacy path %s status=%d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}
