package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

type GatewayTokenError string

const (
	GatewayTokenInvalid     GatewayTokenError = "gateway_invalid_token"
	GatewayTokenDisabled    GatewayTokenError = "gateway_token_disabled"
	GatewayTokenRevoked     GatewayTokenError = "gateway_token_revoked"
	GatewayTokenExpired     GatewayTokenError = "gateway_token_expired"
	GatewayTokenIPDenied    GatewayTokenError = "gateway_ip_not_allowed"
	GatewayTokenModelDenied GatewayTokenError = "gateway_model_not_allowed"
)

func (e GatewayTokenError) Error() string { return string(e) }

func IsGatewayTokenError(err error, expected GatewayTokenError) bool {
	return errors.Is(err, expected)
}

type GatewayTokenCreateInput struct {
	Name          string
	AllowedModels models.JSONSlice
	IPAllowlist   models.JSONSlice
	ExpiresAt     *time.Time
}

type GatewayTokenUpdateInput struct {
	Name          *string
	AllowedModels *models.JSONSlice
	IPAllowlist   *models.JSONSlice
	ExpiresAt     **time.Time
	Status        *models.GatewayTokenStatus
}

type GatewayTokenService struct{ db *gorm.DB }

func NewGatewayTokenService(db *gorm.DB) *GatewayTokenService { return &GatewayTokenService{db: db} }

func (s *GatewayTokenService) Create(user *models.User, in GatewayTokenCreateInput) (*models.GatewayAPIToken, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 128 {
		return nil, "", fmt.Errorf("token name must be 1-128 characters")
	}
	if err := validateIPAllowlist(in.IPAllowlist); err != nil {
		return nil, "", err
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now().UTC()) {
		return nil, "", fmt.Errorf("token expiration must be in the future")
	}
	secret, err := generateGatewaySecret()
	if err != nil {
		return nil, "", err
	}
	token := &models.GatewayAPIToken{
		UserID: user.ID, Name: name, TokenHash: gatewayTokenHash(secret),
		TokenPrefix: tokenPrefix(secret), Status: models.GatewayTokenActive,
		AllowedModels: normalizeStrings(in.AllowedModels), IPAllowlist: normalizeStrings(in.IPAllowlist), ExpiresAt: in.ExpiresAt,
	}
	if err := s.db.Create(token).Error; err != nil {
		return nil, "", err
	}
	return token, secret, nil
}

func (s *GatewayTokenService) List(userID int) ([]models.GatewayAPIToken, error) {
	var tokens []models.GatewayAPIToken
	return tokens, s.db.Where("user_id = ?", userID).Order("created_at desc").Find(&tokens).Error
}

func (s *GatewayTokenService) Get(userID, id int) (*models.GatewayAPIToken, error) {
	var token models.GatewayAPIToken
	if err := s.db.Where("id = ? AND user_id = ?", id, userID).First(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func (s *GatewayTokenService) Update(userID, id int, in GatewayTokenUpdateInput) (*models.GatewayAPIToken, error) {
	token, err := s.Get(userID, id)
	if err != nil {
		return nil, err
	}
	updates := map[string]interface{}{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" || len(name) > 128 {
			return nil, fmt.Errorf("token name must be 1-128 characters")
		}
		updates["name"] = name
	}
	if in.AllowedModels != nil {
		updates["allowed_models"] = normalizeStrings(*in.AllowedModels)
	}
	if in.IPAllowlist != nil {
		if err := validateIPAllowlist(*in.IPAllowlist); err != nil {
			return nil, err
		}
		updates["ip_allowlist"] = normalizeStrings(*in.IPAllowlist)
	}
	if in.ExpiresAt != nil {
		if *in.ExpiresAt != nil && !(*in.ExpiresAt).After(time.Now().UTC()) {
			return nil, fmt.Errorf("token expiration must be in the future")
		}
		updates["expires_at"] = *in.ExpiresAt
	}
	if in.Status != nil {
		if *in.Status != models.GatewayTokenActive && *in.Status != models.GatewayTokenDisabled {
			return nil, fmt.Errorf("invalid token status")
		}
		updates["status"] = *in.Status
	}
	if len(updates) > 0 {
		if err := s.db.Model(token).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	return s.Get(userID, id)
}

func (s *GatewayTokenService) Revoke(userID, id int) error {
	result := s.db.Model(&models.GatewayAPIToken{}).Where("id = ? AND user_id = ?", id, userID).Update("status", models.GatewayTokenRevoked)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *GatewayTokenService) Authenticate(secret, ip, model string, now time.Time) (*models.GatewayAPIToken, error) {
	if !strings.HasPrefix(secret, "sk-gw-") {
		return nil, GatewayTokenInvalid
	}
	var token models.GatewayAPIToken
	if err := s.db.Where("token_hash = ?", gatewayTokenHash(secret)).First(&token).Error; err != nil {
		return nil, GatewayTokenInvalid
	}
	switch token.Status {
	case models.GatewayTokenActive:
	case models.GatewayTokenDisabled:
		return nil, GatewayTokenDisabled
	case models.GatewayTokenRevoked:
		return nil, GatewayTokenRevoked
	default:
		return nil, GatewayTokenInvalid
	}
	if token.ExpiresAt != nil && !token.ExpiresAt.After(now) {
		return nil, GatewayTokenExpired
	}
	var owner models.User
	if err := s.db.Select("id", "status").First(&owner, token.UserID).Error; err != nil || !owner.Status.IsActive() {
		return nil, GatewayTokenDisabled
	}
	if !ipAllowed(token.IPAllowlist, ip) {
		return nil, GatewayTokenIPDenied
	}
	if model != "" && !modelAllowed(token.AllowedModels, model) {
		return nil, GatewayTokenModelDenied
	}
	_ = s.db.Model(&token).Update("last_used_at", now).Error
	return &token, nil
}

func generateGatewaySecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate gateway token: %w", err)
	}
	return "sk-gw-" + base64.RawURLEncoding.EncodeToString(b), nil
}

func gatewayTokenHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
func tokenPrefix(secret string) string {
	if len(secret) > 14 {
		return secret[:14]
	}
	return secret
}
func normalizeStrings(in models.JSONSlice) models.JSONSlice {
	out := make(models.JSONSlice, 0, len(in))
	for _, value := range in {
		if v := strings.TrimSpace(value); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func modelAllowed(allowed models.JSONSlice, model string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, v := range allowed {
		if v == model {
			return true
		}
	}
	return false
}
func ipAllowed(allowed models.JSONSlice, ip string) bool {
	if len(allowed) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, value := range allowed {
		if parsed.Equal(net.ParseIP(value)) {
			return true
		}
	}
	return false
}
func validateIPAllowlist(allowlist models.JSONSlice) error {
	for _, value := range allowlist {
		if net.ParseIP(strings.TrimSpace(value)) == nil {
			return fmt.Errorf("ip_allowlist must contain literal IP addresses")
		}
	}
	return nil
}
