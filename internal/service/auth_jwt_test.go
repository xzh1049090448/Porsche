package service

import (
	"testing"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
)

func TestAccessTokenSubjectUsesUserGUID(t *testing.T) {
	auth := NewAuthService(&config.Settings{JWTSecretKey: "test-secret", JWTExpireMinutes: 5}, nil, nil)
	token, err := auth.makeToken(&models.User{ID: 42, AuditFields: models.AuditFields{Guid: 9001}})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := security.DecodeAccessToken(token, "test-secret")
	if err != nil || claims["sub"] != "9001" {
		t.Fatalf("JWT subject leaked internal ID: claims=%#v err=%v", claims, err)
	}
}
