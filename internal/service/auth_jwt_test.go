package service

import (
	"testing"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
)

func TestAccessTokenSubjectUsesUserGUID(t *testing.T) {
	auth := NewAuthService(&config.Settings{JWTSecretKey: "test-secret", JWTExpireMinutes: 5}, nil, nil)
	token, err := auth.makeToken(&models.User{ID: 42, AuditFields: models.AuditFields{Guid: 9001}, Role: models.UserRoleAdmin, AuthVersion: 7}, &models.Session{SID: "0f5b20dd-90aa-4b99-aed4-0d8d74eaa8da", SessionVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := security.DecodeAccessToken(token, "test-secret")
	if err != nil || claims["sub"] != "9001" {
		t.Fatalf("JWT subject leaked internal ID: claims=%#v err=%v", claims, err)
	}
	if claims["sid"] != "0f5b20dd-90aa-4b99-aed4-0d8d74eaa8da" || claims["sv"] != float64(3) || claims["av"] != float64(7) || claims["role"] != float64(models.UserRoleAdmin) {
		t.Fatalf("session claims missing or invalid: %#v", claims)
	}
	if _, ok := claims["id"]; ok {
		t.Fatalf("JWT must not carry internal user id: %#v", claims)
	}
}
