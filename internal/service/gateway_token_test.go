package service

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func TestGatewayTokenAuthenticateNilServiceFailsClosed(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("nil gateway token service panicked: %v", recovered)
		}
	}()
	var svc *GatewayTokenService
	token, err := svc.Authenticate("sk-gw-test", "", "", time.Now())
	if token != nil || !errors.Is(err, GatewayTokenUnavailable) {
		t.Fatalf("nil service = token:%v err:%v, want unavailable", token, err)
	}
}

func TestGatewayAllowedModelsRawValidationAndPrincipalCopies(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  sql.NullString
		want models.JSONSlice
		bad  bool
	}{
		{name: "sql nil", raw: sql.NullString{}, want: models.JSONSlice{}},
		{name: "whole null", raw: sql.NullString{String: "null", Valid: true}, want: models.JSONSlice{}},
		{name: "empty array", raw: sql.NullString{String: "[]", Valid: true}, want: models.JSONSlice{}},
		{name: "exact values", raw: sql.NullString{String: `["model-a"," model-b "]`, Valid: true}, want: models.JSONSlice{"model-a", " model-b "}},
		{name: "null element", raw: sql.NullString{String: `[null]`, Valid: true}, bad: true},
		{name: "empty element", raw: sql.NullString{String: `[""]`, Valid: true}, bad: true},
		{name: "number element", raw: sql.NullString{String: `[1]`, Valid: true}, bad: true},
		{name: "object element", raw: sql.NullString{String: `[{}]`, Valid: true}, bad: true},
		{name: "not array", raw: sql.NullString{String: `"model-a"`, Valid: true}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGatewayAllowedModels(tc.raw)
			if tc.bad {
				if err == nil {
					t.Fatal("invalid raw ACL accepted")
				}
				return
			}
			if err != nil || len(got) != len(tc.want) {
				t.Fatalf("parse=%v err=%v", got, err)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("value[%d]=%q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}

	zero := &GatewayTokenPrincipal{}
	if zero.AllowsModel("model-a") || zero.Token() != nil || zero.KeyAllowedModels() != nil || zero.OwnerAllowedModels() != nil {
		t.Fatal("zero principal must not authorize or expose state")
	}
	p := &GatewayTokenPrincipal{valid: true, token: models.GatewayAPIToken{AllowedModels: models.JSONSlice{"key"}, IPAllowlist: models.JSONSlice{"127.0.0.1"}}, keyAllowedModels: models.JSONSlice{"key"}, ownerAllowedModels: models.JSONSlice{"owner"}}
	key := p.KeyAllowedModels()
	owner := p.OwnerAllowedModels()
	token := p.Token()
	key[0], owner[0], token.AllowedModels[0], token.IPAllowlist[0] = "changed", "changed", "changed", "changed"
	if p.KeyAllowedModels()[0] != "key" || p.OwnerAllowedModels()[0] != "owner" || p.Token().AllowedModels[0] != "key" || p.Token().IPAllowlist[0] != "127.0.0.1" {
		t.Fatal("principal exposed mutable ACL state")
	}
	if p.AllowsModel("key") || p.AllowsModel("owner") || p.AllowsModel("") {
		t.Fatal("principal allowed a model outside the two ACL intersection")
	}
}

func TestGatewayTokenCreateAndAuthenticate(t *testing.T) {
	gdb := openTestMySQL(t)
	user := testUser("13800138001")
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

	now := time.Now().UTC()
	got, err := svc.Authenticate(secret, "127.0.0.1", "qwen-turbo", now)
	if err != nil || got.ID != created.ID {
		t.Fatalf("authenticate token: token=%v err=%v", got, err)
	}
	// MySQL may report zero changed rows for the second same-millisecond write;
	// authentication must remain valid because the update itself succeeded.
	if got, err := svc.Authenticate(secret, "127.0.0.1", "qwen-turbo", now); err != nil || got == nil {
		t.Fatalf("same-millisecond authenticate: token=%v err=%v", got, err)
	}
	if _, err := svc.Authenticate(secret, "127.0.0.1", "qwen-plus", time.Now()); !IsGatewayTokenError(err, GatewayTokenModelDenied) {
		t.Fatalf("expected model denied, got %v", err)
	}
}

func TestGatewayTokenRejectsExpiredAndRevoked(t *testing.T) {
	gdb := openTestMySQL(t)
	user := testUser("13800138002")
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewGatewayTokenService(gdb)
	expired, expiredSecret, err := svc.Create(&user, GatewayTokenCreateInput{Name: "expired"})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	if err := gdb.Model(expired).Update("expires_at", past.UTC().UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(expiredSecret, "", "qwen-turbo", time.Now()); !IsGatewayTokenError(err, GatewayTokenExpired) {
		t.Fatalf("expected expired, got %v", err)
	}
	created, secret, err := svc.Create(&user, GatewayTokenCreateInput{Name: "revoke"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(user.ID, created.Guid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(secret, "", "qwen-turbo", time.Now()); !IsGatewayTokenError(err, GatewayTokenRevoked) {
		t.Fatalf("expected revoked, got %v", err)
	}
}

func TestGatewayTokenRejectsDisabledOwner(t *testing.T) {
	gdb := openTestMySQL(t)
	user := testUser("13800138003")
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

func TestGatewayTokenOwnerACLChangeAppliesToNextAuthentication(t *testing.T) {
	gdb := openTestMySQL(t)
	user := testUser("13800138004")
	user.AllowedModels = models.JSONSlice{"owner-a"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewGatewayTokenService(gdb)
	_, secret, err := svc.Create(&user, GatewayTokenCreateInput{Name: "fresh-owner", AllowedModels: models.JSONSlice{"owner-a", "token-only"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(secret, "", "owner-a", time.Now()); err != nil {
		t.Fatalf("initial owner allowance: %v", err)
	}
	if err := gdb.Model(&models.User{}).Where("id = ?", user.ID).Update("allowed_models", models.JSONSlice{"different"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(secret, "", "owner-a", time.Now()); !IsGatewayTokenError(err, GatewayTokenModelDenied) {
		t.Fatalf("owner ACL change was not applied to next request: %v", err)
	}
}

func TestGatewayTokenPersistedACLShapesFailClosed(t *testing.T) {
	gdb := openTestMySQL(t)
	user := testUser("13800138005")
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewGatewayTokenService(gdb)
	created, secret, err := svc.Create(&user, GatewayTokenCreateInput{Name: "raw-acl"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, table, value string
		want               GatewayTokenError
	}{
		{"token null element", "gateway_api_tokens", `[null]`, GatewayTokenUnavailable},
		{"token empty element", "gateway_api_tokens", `[""]`, GatewayTokenUnavailable},
		{"token number", "gateway_api_tokens", `[1]`, GatewayTokenUnavailable},
		{"token object", "gateway_api_tokens", `[{}]`, GatewayTokenUnavailable},
		{"owner null element", "users", `[null]`, GatewayTokenUnavailable},
		{"owner empty element", "users", `[""]`, GatewayTokenUnavailable},
		{"owner number", "users", `[1]`, GatewayTokenUnavailable},
		{"owner object", "users", `[{}]`, GatewayTokenUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := gdb.Table(tc.table).Where("id = ?", func() int64 {
				if tc.table == "users" {
					return user.ID
				}
				return created.ID
			}()).Update("allowed_models", gorm.Expr("CAST(? AS JSON)", tc.value)).Error; err != nil {
				t.Fatal(err)
			}
			got, err := svc.AuthenticatePrincipal(secret, "", "model-a", time.Now())
			if got != nil || !IsGatewayTokenError(err, tc.want) {
				t.Fatalf("principal=%v err=%v", got, err)
			}
		})
		if err := gdb.Model(created).Update("allowed_models", models.JSONSlice{}).Error; err != nil {
			t.Fatal(err)
		}
		if err := gdb.Model(&user).Update("allowed_models", models.JSONSlice{}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, table, value string }{{"token whole null", "gateway_api_tokens", "null"}, {"owner whole null", "users", "null"}} {
		t.Run(tc.name, func(t *testing.T) {
			id := created.ID
			if tc.table == "users" {
				id = user.ID
			}
			if err := gdb.Table(tc.table).Where("id = ?", id).Update("allowed_models", gorm.Expr("CAST(? AS JSON)", tc.value)).Error; err != nil {
				t.Fatal(err)
			}
			got, err := svc.AuthenticatePrincipal(secret, "", "model-a", time.Now())
			if err != nil || got == nil || !got.AllowsModel("model-a") {
				t.Fatalf("whole null not unrestricted: %v", err)
			}
			if err := gdb.Model(created).Update("allowed_models", models.JSONSlice{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := gdb.Model(&user).Update("allowed_models", models.JSONSlice{}).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
}
