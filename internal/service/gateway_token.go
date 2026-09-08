package service

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type GatewayTokenError string

const (
	GatewayTokenInvalid     GatewayTokenError = "gateway_invalid_token"
	GatewayTokenDisabled    GatewayTokenError = "gateway_token_disabled"
	GatewayTokenRevoked     GatewayTokenError = "gateway_token_revoked"
	GatewayTokenExpired     GatewayTokenError = "gateway_token_expired"
	GatewayTokenIPDenied    GatewayTokenError = "gateway_ip_not_allowed"
	GatewayTokenModelDenied GatewayTokenError = "gateway_model_not_allowed"
	// GatewayTokenUnavailable deliberately collapses storage/decode failures.
	GatewayTokenUnavailable GatewayTokenError = "gateway_authentication_unavailable"
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
	var expiresAt *int64
	if in.ExpiresAt != nil {
		value := in.ExpiresAt.UTC().UnixMilli()
		expiresAt = &value
	}
	token := &models.GatewayAPIToken{
		UserID: user.ID, Name: name, TokenHash: gatewayTokenHash(secret),
		TokenPrefix: tokenPrefix(secret), Status: models.GatewayTokenActive,
		AllowedModels: normalizeStrings(in.AllowedModels), IPAllowlist: normalizeStrings(in.IPAllowlist), ExpiresAt: expiresAt, AuditFields: auditFields(&user.ID),
	}
	if err := s.db.Create(token).Error; err != nil {
		return nil, "", err
	}
	return token, secret, nil
}

func (s *GatewayTokenService) List(userID int64) ([]models.GatewayAPIToken, error) {
	var tokens []models.GatewayAPIToken
	return tokens, s.db.Where("user_id = ? AND is_deleted = 0", userID).Order("created_at desc").Find(&tokens).Error
}

func (s *GatewayTokenService) Get(userID, id int64) (*models.GatewayAPIToken, error) {
	var token models.GatewayAPIToken
	if err := s.db.Where("guid = ? AND user_id = ? AND is_deleted = 0", id, userID).First(&token).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func (s *GatewayTokenService) Update(userID, id int64, in GatewayTokenUpdateInput) (*models.GatewayAPIToken, error) {
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
		if *in.ExpiresAt == nil {
			updates["expires_at"] = nil
		} else {
			updates["expires_at"] = (*in.ExpiresAt).UTC().UnixMilli()
		}
	}
	if in.Status != nil {
		if *in.Status != models.GatewayTokenActive && *in.Status != models.GatewayTokenDisabled {
			return nil, fmt.Errorf("invalid token status")
		}
		updates["status"] = *in.Status
	}
	if len(updates) > 0 {
		updates["updated_at"] = persistence.NowMillis()
		updates["updated_by"] = userID
		if err := s.db.Model(token).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	return s.Get(userID, id)
}

func (s *GatewayTokenService) Revoke(userID, id int64) error {
	result := s.db.Model(&models.GatewayAPIToken{}).Where("guid = ? AND user_id = ? AND is_deleted = 0", id, userID).Updates(map[string]interface{}{"status": models.GatewayTokenRevoked, "updated_at": persistence.NowMillis(), "updated_by": userID})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// GatewayTokenPrincipal is valid only for the request that loaded it. Its
// allowlists are deliberately private so callers cannot mutate authorization
// state; accessors always return independent copies.
type GatewayTokenPrincipal struct {
	valid              bool
	token              models.GatewayAPIToken
	keyAllowedModels   models.JSONSlice
	ownerAllowedModels models.JSONSlice
}

func (p *GatewayTokenPrincipal) AllowsModel(model string) bool {
	return p != nil && p.valid && model != "" && modelAllowed(p.keyAllowedModels, model) && modelAllowed(p.ownerAllowedModels, model)
}

func (p *GatewayTokenPrincipal) KeyAllowedModels() models.JSONSlice {
	if p == nil || !p.valid {
		return nil
	}
	return cloneJSONSlice(p.keyAllowedModels)
}

func (p *GatewayTokenPrincipal) OwnerAllowedModels() models.JSONSlice {
	if p == nil || !p.valid {
		return nil
	}
	return cloneJSONSlice(p.ownerAllowedModels)
}

func (p *GatewayTokenPrincipal) Token() *models.GatewayAPIToken {
	if p == nil || !p.valid {
		return nil
	}
	token := p.token
	token.AllowedModels = cloneJSONSlice(p.keyAllowedModels)
	token.IPAllowlist = cloneJSONSlice(p.token.IPAllowlist)
	return &token
}

// Authenticate keeps the existing token API while delegating all checks to
// AuthenticatePrincipal. It returns the token's persisted ACL, never an ACL
// intersection.
func (s *GatewayTokenService) Authenticate(secret, ip, model string, now time.Time) (*models.GatewayAPIToken, error) {
	principal, err := s.AuthenticatePrincipal(secret, ip, model, now)
	if err != nil {
		return nil, err
	}
	return principal.Token(), nil
}

// AuthenticatePrincipal reloads both persisted ACLs for each request. A blank
// model establishes identity only; it is not a model authorization decision.
func (s *GatewayTokenService) AuthenticatePrincipal(secret, ip, model string, now time.Time) (*GatewayTokenPrincipal, error) {
	if s == nil || s.db == nil {
		return nil, GatewayTokenUnavailable
	}
	db := s.db.Session(&gorm.Session{Logger: logger.Discard})
	if !strings.HasPrefix(secret, "sk-gw-") {
		return nil, GatewayTokenInvalid
	}
	var tokenRow struct {
		models.GatewayAPIToken
		RawAllowedModels sql.NullString `gorm:"column:raw_allowed_models"`
	}
	if err := db.Table("gateway_api_tokens").Select("gateway_api_tokens.*, allowed_models AS raw_allowed_models").Where("token_hash = ? AND is_deleted = 0", gatewayTokenHash(secret)).First(&tokenRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, GatewayTokenInvalid
		}
		return nil, GatewayTokenUnavailable
	}
	token := tokenRow.GatewayAPIToken
	switch token.Status {
	case models.GatewayTokenActive:
	case models.GatewayTokenDisabled:
		return nil, GatewayTokenDisabled
	case models.GatewayTokenRevoked:
		return nil, GatewayTokenRevoked
	default:
		return nil, GatewayTokenInvalid
	}
	if token.ExpiresAt != nil && *token.ExpiresAt <= now.UTC().UnixMilli() {
		return nil, GatewayTokenExpired
	}
	keyAllowed, err := parseGatewayAllowedModels(tokenRow.RawAllowedModels)
	if err != nil {
		return nil, GatewayTokenUnavailable
	}
	var owner struct {
		ID            int64
		Status        models.UserStatus
		AllowedModels sql.NullString `gorm:"column:allowed_models"`
	}
	if err := db.Table("users").Select("id", "status", "allowed_models").Where("id = ? AND is_deleted = 0", token.UserID).First(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, GatewayTokenDisabled
		}
		return nil, GatewayTokenUnavailable
	}
	if !owner.Status.IsActive() {
		return nil, GatewayTokenDisabled
	}
	ownerAllowed, err := parseGatewayAllowedModels(owner.AllowedModels)
	if err != nil {
		return nil, GatewayTokenUnavailable
	}
	if !ipAllowed(token.IPAllowlist, ip) {
		return nil, GatewayTokenIPDenied
	}
	if model != "" && (!modelAllowed(keyAllowed, model) || !modelAllowed(ownerAllowed, model)) {
		return nil, GatewayTokenModelDenied
	}
	lastUsedAt := now.UTC().UnixMilli()
	if err := db.Model(&token).Where("id = ? AND is_deleted = 0", token.ID).Updates(map[string]interface{}{"last_used_at": lastUsedAt, "updated_at": lastUsedAt, "updated_by": token.UserID}).Error; err != nil {
		return nil, GatewayTokenUnavailable
	}
	token.AllowedModels = cloneJSONSlice(keyAllowed)
	return &GatewayTokenPrincipal{valid: true, token: token, keyAllowedModels: cloneJSONSlice(keyAllowed), ownerAllowedModels: cloneJSONSlice(ownerAllowed)}, nil
}

func parseGatewayAllowedModels(raw sql.NullString) (models.JSONSlice, error) {
	if !raw.Valid || raw.String == "null" {
		return models.JSONSlice{}, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal([]byte(raw.String), &values); err != nil {
		return nil, err
	}
	allowed := make(models.JSONSlice, 0, len(values))
	for _, value := range values {
		var model string
		if len(value) == 0 || string(value) == "null" || json.Unmarshal(value, &model) != nil || model == "" {
			return nil, errors.New("invalid gateway allowed_models")
		}
		allowed = append(allowed, model)
	}
	return allowed, nil
}

func cloneJSONSlice(in models.JSONSlice) models.JSONSlice {
	if in == nil {
		return nil
	}
	return append(models.JSONSlice(nil), in...)
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
