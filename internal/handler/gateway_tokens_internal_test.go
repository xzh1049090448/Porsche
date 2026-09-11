package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func TestGatewayACLFailureUsesPermissionError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Header("X-Request-ID", "request-id")
	gatewayAuthenticationError(context, http.StatusForbidden, service.GatewayTokenModelDenied)
	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Type != "permission_error" {
		t.Fatalf("type=%q body=%s", body.Error.Type, recorder.Body.String())
	}
}
