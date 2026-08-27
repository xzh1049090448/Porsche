package config

import (
	"fmt"
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
	whiteLabel, err := ParseWhiteLabelSettings(
		os.Getenv("UPSTREAM_REGION"),
		os.Getenv("JIEKOU_API_KEY"),
		os.Getenv("JIEKOU_ALLOWED_MODELS"),
	)
	if err != nil {
		return nil, err
	}

	s := &Settings{
		AppEnv:                   getEnv("APP_ENV", "development"),
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
	if !isMySQLURL(s.DatabaseURL) {
		return nil, fmt.Errorf("DATABASE_URL must use mysql://, mysql+aiomysql://, or mysql+asyncmy://")
	}
	var nodeErr error
	s.SnowflakeNodeID, nodeErr = requiredSnowflakeNodeID(os.Getenv("SNOWFLAKE_NODE_ID"))
	if nodeErr != nil {
		return nil, nodeErr
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

// LoadMigrationSettings loads only the configuration required to inspect or
// apply schema migrations. Migrations must not require an upstream API key.
func LoadMigrationSettings() (*Settings, error) {
	_ = godotenv.Load()
	s := &Settings{
		AppEnv:      getEnv("APP_ENV", "development"),
		DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
	}
	if !isMySQLURL(s.DatabaseURL) {
		return nil, fmt.Errorf("DATABASE_URL must use mysql://, mysql+aiomysql://, or mysql+asyncmy://")
	}
	var err error
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
	if s.MetricsToken != "" {
		return s.MetricsToken
	}
	return s.AdminToken
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
