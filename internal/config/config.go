package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Settings struct {
	AppEnv                   string
	Host                     string
	Port                     int
	AllowedHosts             string
	AdminToken               string
	RedisURL                 string
	LogLevel                 string
	UpstreamTimeoutSeconds   float64
	CircuitFailureThreshold  int
	CircuitOpenSeconds       int
	DatabaseURL              string
	SnowflakeNodeID          int
	JWTSecretKey             string
	JWTExpireMinutes         int
	RegisterEnabled          bool
	PasswordRegisterEnabled  bool
	PasswordLoginEnabled     bool
	SessionAccessMinutes     int
	SessionDays              int
	SessionMaxActive         int
	SessionIssueLimit24h     int
	RefreshReplaySeconds     int
	AuthTrustedOrigins       []string
	RootBootstrapUsername    string
	RootBootstrapPassword    string
	AuthHMACKey              string
	FixedLoginEnabled        bool
	FixedLoginPhone          string
	FixedLoginPassword       string
	SMSDevMode               bool
	SMSSendLimitPerPhone     int
	SMSSendLimitPerIP        int
	SMSVerifyMaxAttempts     int
	BillingAllowMockPayment  bool
	MetricsToken             string
	TrustProxyHeaders        bool
	TrustedProxyCIDRs        string
	RealNameAutoVerify       bool
	PlanProfessionalPrice    float64
	PlanEnterprisePrice      float64
	AnalyticsAdminPhones     string
	AnalyticsTokenPricePer1K float64
	WhiteLabel               WhiteLabelSettings
}

// WhiteLabelSettings contains the only supported upstream configuration. BaseURL
// is selected from a fixed region mapping and must never be supplied by callers.
type WhiteLabelSettings struct {
	Region               string
	BaseURL              string
	APIKey               string
	AllowedModels        map[string]struct{}
	AllowedModelPatterns []*regexp.Regexp
}

func (s WhiteLabelSettings) Allows(model string) bool {
	model = strings.TrimSpace(model)
	if _, ok := s.AllowedModels[model]; ok {
		return true
	}
	for _, pattern := range s.AllowedModelPatterns {
		if pattern == nil {
			continue
		}
		if pattern.MatchString(model) {
			return true
		}
	}
	return false
}

func ParseWhiteLabelSettings(region, apiKey, allowedModels string) (WhiteLabelSettings, error) {
	region = strings.TrimSpace(region)
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return WhiteLabelSettings{}, fmt.Errorf("JIEKOU_API_KEY must be non-empty")
	}

	baseURLs := map[string]string{
		"cn":     "https://api.highwayapi.ai/openai/v1",
		"global": "https://api.jiekou.ai/openai/v1",
	}
	baseURL, ok := baseURLs[region]
	if !ok {
		return WhiteLabelSettings{}, fmt.Errorf("UPSTREAM_REGION must be cn or global")
	}

	models := make(map[string]struct{})
	patterns := make([]*regexp.Regexp, 0)
	for _, model := range strings.Split(allowedModels, ",") {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if strings.HasPrefix(model, "re:") {
			expression := strings.TrimPrefix(model, "re:")
			if expression == "" {
				return WhiteLabelSettings{}, fmt.Errorf("JIEKOU_ALLOWED_MODELS contains an invalid regular expression")
			}
			pattern, err := regexp.Compile(expression)
			if err != nil {
				return WhiteLabelSettings{}, fmt.Errorf("JIEKOU_ALLOWED_MODELS contains an invalid regular expression")
			}
			patterns = append(patterns, pattern)
			continue
		}
		models[model] = struct{}{}
	}
	if len(models)+len(patterns) == 0 {
		return WhiteLabelSettings{}, fmt.Errorf("JIEKOU_ALLOWED_MODELS must contain at least one model")
	}

	return WhiteLabelSettings{Region: region, BaseURL: baseURL, APIKey: apiKey, AllowedModels: models, AllowedModelPatterns: patterns}, nil
}

func Load() (*Settings, error) {
	_ = godotenv.Load()
	appEnv, err := loadAppEnv(os.Getenv("APP_ENV"))
	if err != nil {
		return nil, err
	}
	if err := rejectRootBootstrapEnvironment(appEnv); err != nil {
		return nil, err
	}
	whiteLabel, err := ParseWhiteLabelSettings(
		os.Getenv("UPSTREAM_REGION"),
		os.Getenv("JIEKOU_API_KEY"),
		os.Getenv("JIEKOU_ALLOWED_MODELS"),
	)
	if err != nil {
		return nil, err
	}
	registerEnabled, err := loadAuthBool("REGISTER_ENABLED", true)
	if err != nil {
		return nil, err
	}
	passwordRegisterEnabled, err := loadAuthBool("PASSWORD_REGISTER_ENABLED", true)
	if err != nil {
		return nil, err
	}
	passwordLoginEnabled, err := loadAuthBool("PASSWORD_LOGIN_ENABLED", true)
	if err != nil {
		return nil, err
	}
	sessionAccessMinutes, err := loadFixedAuthInt("SESSION_ACCESS_MINUTES", 15)
	if err != nil {
		return nil, err
	}
	sessionDays, err := loadAuthPositiveInt("SESSION_DAYS", 30)
	if err != nil {
		return nil, err
	}
	sessionMaxActive, err := loadAuthPositiveInt("SESSION_MAX_ACTIVE", 50)
	if err != nil {
		return nil, err
	}
	sessionIssueLimit24h, err := loadAuthPositiveInt("SESSION_ISSUE_LIMIT_24H", 100)
	if err != nil {
		return nil, err
	}
	refreshReplaySeconds, err := loadAuthPositiveInt("REFRESH_REPLAY_SECONDS", 30)
	if err != nil {
		return nil, err
	}

	s := &Settings{
		AppEnv:                   appEnv,
		Host:                     getEnv("HOST", "0.0.0.0"),
		Port:                     getEnvInt("PORT", 8000),
		AllowedHosts:             getEnv("ALLOWED_HOSTS", "aiportcloud.com"),
		AdminToken:               getEnv("ADMIN_TOKEN", "change-me-for-dev-only"),
		RedisURL:                 strings.TrimSpace(os.Getenv("REDIS_URL")),
		LogLevel:                 getEnv("LOG_LEVEL", "INFO"),
		UpstreamTimeoutSeconds:   getEnvFloat("UPSTREAM_TIMEOUT_SECONDS", 120),
		CircuitFailureThreshold:  getEnvInt("CIRCUIT_FAILURE_THRESHOLD", 5),
		CircuitOpenSeconds:       getEnvInt("CIRCUIT_OPEN_SECONDS", 60),
		DatabaseURL:              strings.TrimSpace(os.Getenv("DATABASE_URL")),
		JWTSecretKey:             getEnv("JWT_SECRET_KEY", "change-me-jwt-secret-for-dev-only"),
		JWTExpireMinutes:         getEnvInt("JWT_EXPIRE_MINUTES", 60*24*7),
		RegisterEnabled:          registerEnabled,
		PasswordRegisterEnabled:  passwordRegisterEnabled,
		PasswordLoginEnabled:     passwordLoginEnabled,
		SessionAccessMinutes:     sessionAccessMinutes,
		SessionDays:              sessionDays,
		SessionMaxActive:         sessionMaxActive,
		SessionIssueLimit24h:     sessionIssueLimit24h,
		RefreshReplaySeconds:     refreshReplaySeconds,
		RootBootstrapUsername:    strings.TrimSpace(os.Getenv("ROOT_BOOTSTRAP_USERNAME")),
		RootBootstrapPassword:    strings.TrimSpace(os.Getenv("ROOT_BOOTSTRAP_PASSWORD")),
		AuthHMACKey:              getEnv("AUTH_HMAC_KEY", "change-me-auth-hmac-key-for-dev-only"),
		FixedLoginEnabled:        getEnvBool("FIXED_LOGIN_ENABLED", true),
		FixedLoginPhone:          getEnv("FIXED_LOGIN_PHONE", "13800138000"),
		FixedLoginPassword:       getEnv("FIXED_LOGIN_PASSWORD", "Porsche@2026"),
		SMSDevMode:               getEnvBool("SMS_DEV_MODE", true),
		SMSSendLimitPerPhone:     getEnvInt("SMS_SEND_LIMIT_PER_PHONE", 5),
		SMSSendLimitPerIP:        getEnvInt("SMS_SEND_LIMIT_PER_IP", 20),
		SMSVerifyMaxAttempts:     getEnvInt("SMS_VERIFY_MAX_ATTEMPTS", 5),
		BillingAllowMockPayment:  getEnvBool("BILLING_ALLOW_MOCK_PAYMENT", false),
		MetricsToken:             strings.TrimSpace(os.Getenv("METRICS_TOKEN")),
		TrustProxyHeaders:        getEnvBool("TRUST_PROXY_HEADERS", false),
		TrustedProxyCIDRs:        strings.TrimSpace(os.Getenv("TRUSTED_PROXY_CIDRS")),
		RealNameAutoVerify:       getEnvBool("REAL_NAME_AUTO_VERIFY", true),
		PlanProfessionalPrice:    getEnvFloat("PLAN_PROFESSIONAL_PRICE", 99),
		PlanEnterprisePrice:      getEnvFloat("PLAN_ENTERPRISE_PRICE", 999),
		AnalyticsAdminPhones:     strings.TrimSpace(os.Getenv("ANALYTICS_ADMIN_PHONES")),
		AnalyticsTokenPricePer1K: getEnvFloat("ANALYTICS_TOKEN_PRICE_PER_1K", 1),
		WhiteLabel:               whiteLabel,
	}
	trustedOrigins, err := parseTrustedOrigins(os.Getenv("AUTH_TRUSTED_ORIGINS"))
	if err != nil {
		return nil, err
	}
	s.AuthTrustedOrigins = trustedOrigins
	if !isMySQLURL(s.DatabaseURL) {
		return nil, fmt.Errorf("DATABASE_URL must use mysql://, mysql+aiomysql://, or mysql+asyncmy://")
	}
	var nodeErr error
	s.SnowflakeNodeID, nodeErr = requiredSnowflakeNodeID(os.Getenv("SNOWFLAKE_NODE_ID"))
	if nodeErr != nil {
		return nil, nodeErr
	}
	if err := validateProductionAuthSettings(s); err != nil {
		return nil, err
	}

	if s.AppEnv == "development" {
		if os.Getenv("BILLING_ALLOW_MOCK_PAYMENT") == "" {
			s.BillingAllowMockPayment = true
		}
		if s.AnalyticsAdminPhones == "" {
			s.AnalyticsAdminPhones = s.FixedLoginPhone
		}
	}

	return s, nil
}

// loadAppEnv normalizes the deployment environment before configuration
// validation so case and whitespace cannot bypass non-development safeguards.
func loadAppEnv(raw string) (string, error) {
	environment := strings.ToLower(strings.TrimSpace(raw))
	if environment == "" {
		environment = "development"
	}
	switch environment {
	case "development", "test", "staging", "production":
		return environment, nil
	default:
		return "", fmt.Errorf("APP_ENV must be development, test, staging, or production")
	}
}

// loadAuthBool rejects malformed feature switches so a typo cannot silently
// enable or disable username/password authentication.
func loadAuthBool(key string, defaultValue bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean", key)
	}
}

// loadFixedAuthInt keeps the short-lived Access JWT lifetime fixed at the
// reviewed value instead of accepting an environment override.
func loadFixedAuthInt(key string, requiredValue int) (int, error) {
	value, err := loadAuthPositiveInt(key, requiredValue)
	if err != nil {
		return 0, err
	}
	if value != requiredValue {
		return 0, fmt.Errorf("%s must be %d", key, requiredValue)
	}
	return value, nil
}

// loadAuthPositiveInt accepts an omitted auth limit as its documented default
// but rejects malformed or non-positive overrides instead of silently making
// production authentication less restrictive.
func loadAuthPositiveInt(key string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

// parseTrustedOrigins converts the explicit browser origins into canonical
// configuration values and rejects malformed entries before the auth layer can
// use them for cookie-origin checks.
func parseTrustedOrigins(raw string) ([]string, error) {
	origins := make([]string, 0)
	for _, origin := range strings.Split(raw, ",") {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(origin)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("AUTH_TRUSTED_ORIGINS must contain valid origins")
		}
		origins = append(origins, origin)
	}
	return origins, nil
}

// validateProductionAuthSettings fails closed whenever production auth could
// issue credentials without Redis, safe secrets, trusted HTTPS origins, or a
// complete one-time Root bootstrap configuration.
func validateProductionAuthSettings(s *Settings) error {
	if s.AppEnv == "development" {
		return nil
	}
	if err := validateRedisURL(s.RedisURL); err != nil {
		return err
	}
	if s.FixedLoginEnabled {
		return fmt.Errorf("FIXED_LOGIN_ENABLED must be false outside development")
	}
	if s.SMSDevMode {
		return fmt.Errorf("SMS_DEV_MODE must be false outside development")
	}
	if s.FixedLoginPhone == "13800138000" || s.FixedLoginPassword == "Porsche@2026" {
		return fmt.Errorf("FIXED_LOGIN_PHONE and FIXED_LOGIN_PASSWORD must not use development credentials in production")
	}
	if err := validateProductionAuthSecrets(s); err != nil {
		return err
	}
	if len(s.AuthTrustedOrigins) == 0 {
		return fmt.Errorf("AUTH_TRUSTED_ORIGINS must contain at least one HTTPS origin in production")
	}
	for _, origin := range s.AuthTrustedOrigins {
		parsed, _ := url.Parse(origin)
		if parsed.Scheme != "https" {
			return fmt.Errorf("AUTH_TRUSTED_ORIGINS must contain only HTTPS origins in production")
		}
	}
	return nil
}

// rejectRootBootstrapEnvironment keeps privileged credentials out of the
// long-running service configuration. Root creation is a one-shot operation.
func rejectRootBootstrapEnvironment(appEnv string) error {
	if appEnv == "development" {
		return nil
	}
	if os.Getenv("ROOT_BOOTSTRAP_USERNAME") != "" || os.Getenv("ROOT_BOOTSTRAP_PASSWORD") != "" {
		return fmt.Errorf("ROOT_BOOTSTRAP environment variables are not allowed; use the one-shot bootstrap-root command")
	}
	return nil
}

// validateRedisURL ensures non-development authentication has a concrete
// Redis endpoint before it relies on Redis for revocation and rate limiting.
func validateRedisURL(raw string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") {
		return fmt.Errorf("REDIS_URL must be a redis:// or rediss:// URL outside development")
	}
	return nil
}

// validateProductionAuthSecrets prevents production credentials from using
// shipped placeholders, empty values, or a secret reused by another auth or
// privileged-management boundary.
func validateProductionAuthSecrets(s *Settings) error {
	secrets := []struct {
		name, value, developmentDefault string
	}{
		{"JWT_SECRET_KEY", s.JWTSecretKey, "change-me-jwt-secret-for-dev-only"},
		{"AUTH_HMAC_KEY", s.AuthHMACKey, "change-me-auth-hmac-key-for-dev-only"},
		{"ADMIN_TOKEN", s.AdminToken, "change-me-to-a-long-random-secret"},
		{"METRICS_TOKEN", s.MetricsToken, ""},
	}
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if isKnownAuthPlaceholder(secret.value, secret.developmentDefault) {
			return fmt.Errorf("%s must not use an empty or development placeholder value outside development", secret.name)
		}
		if len([]byte(secret.value)) < 32 {
			return fmt.Errorf("%s must contain at least 32 bytes outside development", secret.name)
		}
		if hasRepeatedSecretMaterial(secret.value) {
			return fmt.Errorf("%s must not use repeated secret material outside development", secret.name)
		}
		if _, exists := seen[secret.value]; exists {
			return fmt.Errorf("%s must not reuse another authentication or management secret in production", secret.name)
		}
		seen[secret.value] = struct{}{}
	}
	return nil
}

// isKnownAuthPlaceholder identifies shipped or legacy development placeholders
// without including their values in validation errors.
func isKnownAuthPlaceholder(value, developmentDefault string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == developmentDefault {
		return true
	}
	for _, placeholder := range []string{
		"change-me-for-dev-only",
		"change-me-to-a-long-random-secret",
		"change-me-to-a-distinct-random-secret",
		"change-me-jwt-secret-for-dev-only",
		"change-me-auth-hmac-key-for-dev-only",
	} {
		if value == placeholder {
			return true
		}
	}
	return false
}

// hasRepeatedSecretMaterial rejects trivial repeated-character and repeated-
// block values that satisfy a length check but are weak credential material.
func hasRepeatedSecretMaterial(value string) bool {
	for prefixLength := 1; prefixLength <= len(value)/2; prefixLength++ {
		if len(value)%prefixLength != 0 {
			continue
		}
		if strings.Repeat(value[:prefixLength], len(value)/prefixLength) == value {
			return true
		}
	}
	return false
}

// validRootBootstrapUsername keeps the one-time Root identity within the
// username contract before it can be persisted by a later authentication task.
func validRootBootstrapUsername(username string) bool {
	if len(username) < 3 || len(username) > 20 {
		return false
	}
	for _, character := range username {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// strongRootBootstrapPassword requires a non-default, mixed-character secret
// for the privileged one-time Root bootstrap without retaining that secret.
func strongRootBootstrapPassword(password string) bool {
	if len(password) < 12 || len(password) > 20 {
		return false
	}
	hasUpper, hasLower, hasDigit, hasSymbol := false, false, false, false
	for _, character := range password {
		if character > 0x7f {
			return false
		}
		switch {
		case character >= 'a' && character <= 'z':
			hasLower = true
		case character >= 'A' && character <= 'Z':
			hasUpper = true
		case character >= '0' && character <= '9':
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	return hasUpper && hasLower && hasDigit && hasSymbol
}

// ValidateRootBootstrapCredentials validates the one-time privileged Root
// bootstrap credentials before they can be persisted.
func ValidateRootBootstrapCredentials(username, password string) error {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if isDefaultAuthSecret(password, "change-me-root-bootstrap-password-for-dev-only") && password != "" {
		return fmt.Errorf("ROOT_BOOTSTRAP_PASSWORD must not use the development default in production")
	}
	if !validRootBootstrapUsername(username) || !strongRootBootstrapPassword(password) {
		return fmt.Errorf("ROOT_BOOTSTRAP credentials are invalid")
	}
	return nil
}

// isDefaultAuthSecret identifies empty and known development placeholders so
// production cannot accidentally start with forgeable credential material.
func isDefaultAuthSecret(value, developmentDefault string) bool {
	return strings.TrimSpace(value) == "" || value == developmentDefault
}

// LoadMigrationSettings loads only the configuration required to inspect or
// apply schema migrations. Migrations must not require an upstream API key.
func LoadMigrationSettings() (*Settings, error) {
	_ = godotenv.Load()
	appEnv, err := loadAppEnv(os.Getenv("APP_ENV"))
	if err != nil {
		return nil, err
	}
	s := &Settings{
		AppEnv:      appEnv,
		DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
	}
	if !isMySQLURL(s.DatabaseURL) {
		return nil, fmt.Errorf("DATABASE_URL must use mysql://, mysql+aiomysql://, or mysql+asyncmy://")
	}
	s.SnowflakeNodeID, err = requiredSnowflakeNodeID(os.Getenv("SNOWFLAKE_NODE_ID"))
	if err != nil {
		return nil, err
	}
	return s, nil
}

func isMySQLURL(databaseURL string) bool {
	return strings.HasPrefix(databaseURL, "mysql://") ||
		strings.HasPrefix(databaseURL, "mysql+aiomysql://") ||
		strings.HasPrefix(databaseURL, "mysql+asyncmy://")
}

func requiredSnowflakeNodeID(raw string) (int, error) {
	nodeID, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || nodeID < 0 || nodeID > 1023 {
		return 0, fmt.Errorf("SNOWFLAKE_NODE_ID must be an integer from 0 to 1023")
	}
	return nodeID, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func (s *Settings) MetricsAuthToken() string {
	return s.MetricsToken
}

func (s *Settings) IsAnalyticsAdmin(phone string) bool {
	phone = strings.TrimSpace(phone)
	for _, p := range strings.Split(s.AnalyticsAdminPhones, ",") {
		if strings.TrimSpace(p) == phone {
			return true
		}
	}
	return false
}
