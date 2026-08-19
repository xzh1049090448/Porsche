package service

import (
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/db"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestGatewayTokenCreateAndAuthenticate(t *testing.T) {
	gdb, err := db.Open("sqlite://"+t.TempDir()+"/gateway-token.db", "test")
	if err != nil {
		t.Fatal(err)
	}
	user := models.User{Phone: "13800138001", Status: models.UserStatusActive}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewGatewayTokenService(gdb)
	created, secret, err := svc.Create(&user, GatewayTokenCreateInput{
		Name:          "production",
		AllowedModels: models.JSONSlice{"qwen-turbo"},
		IPAllowlist:   models.JSONSlice{"127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) < 20 || secret[:6] != "sk-gw-" {
		t.Fatalf("unexpected token format: %q", secret)
	}
	if created.TokenHash == secret || created.TokenHash == "" {
		t.Fatal("plaintext token must not be persisted")
	}

	got, err := svc.Authenticate(secret, "127.0.0.1", "qwen-turbo", time.Now())
	if err != nil || got.ID != created.ID {
		t.Fatalf("authenticate token: token=%v err=%v", got, err)
	}
	if _, err := svc.Authenticate(secret, "127.0.0.1", "qwen-plus", time.Now()); !IsGatewayTokenError(err, GatewayTokenModelDenied) {
		t.Fatalf("expected model denied, got %v", err)
	}
}

func TestGatewayTokenRejectsExpiredAndRevoked(t *testing.T) {
	gdb, err := db.Open("sqlite://"+t.TempDir()+"/gateway-token.db", "test")
	if err != nil {
		t.Fatal(err)
	}
	user := models.User{Phone: "13800138002", Status: models.UserStatusActive}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewGatewayTokenService(gdb)
	expired, expiredSecret, err := svc.Create(&user, GatewayTokenCreateInput{Name: "expired"})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	if err := gdb.Model(expired).Update("expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(expiredSecret, "", "qwen-turbo", time.Now()); !IsGatewayTokenError(err, GatewayTokenExpired) {
		t.Fatalf("expected expired, got %v", err)
	}
	created, secret, err := svc.Create(&user, GatewayTokenCreateInput{Name: "revoke"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(user.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(secret, "", "qwen-turbo", time.Now()); !IsGatewayTokenError(err, GatewayTokenRevoked) {
		t.Fatalf("expected revoked, got %v", err)
	}
}

func TestGatewayTokenRejectsDisabledOwner(t *testing.T) {
	gdb, err := db.Open("sqlite://"+t.TempDir()+"/gateway-token.db", "test")
	if err != nil {
		t.Fatal(err)
	}
	user := models.User{Phone: "13800138003", Status: models.UserStatusActive}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewGatewayTokenService(gdb)
	_, secret, err := svc.Create(&user, GatewayTokenCreateInput{Name: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Model(&user).Update("status", models.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(secret, "", "qwen-turbo", time.Now()); !IsGatewayTokenError(err, GatewayTokenDisabled) {
		t.Fatalf("expected disabled owner rejection, got %v", err)
	}
}
