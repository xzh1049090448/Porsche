package config

import (
	"fmt"
	"os"
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
	DatasetUploadMaxBytes    int64
	ChromaPersistDir         string
	RAGTopK                  int
	RAGChunkSize             int
	RAGChunkOverlap          int
	DatasetUploadDir         string
	PlanProfessionalPrice    float64
	PlanEnterprisePrice      float64
	AnalyticsAdminPhones     string
	AnalyticsTokenPricePer1K float64
	WhiteLabel               WhiteLabelSettings
}

// WhiteLabelSettings contains the only supported upstream configuration. BaseURL
// is selected from a fixed region mapping and must never be supplied by callers.
type WhiteLabelSettings struct {
	Region        string
	BaseURL       string
	APIKey        string
	AllowedModels map[string]struct{}
}

func (s WhiteLabelSettings) Allows(model string) bool {
	_, ok := s.AllowedModels[strings.TrimSpace(model)]
	return ok
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
	for _, model := range strings.Split(allowedModels, ",") {
		if model = strings.TrimSpace(model); model != "" {
			models[model] = struct{}{}
		}
	}
	if len(models) == 0 {
		return WhiteLabelSettings{}, fmt.Errorf("JIEKOU_ALLOWED_MODELS must contain at least one model")
	}

	return WhiteLabelSettings{Region: region, BaseURL: baseURL, APIKey: apiKey, AllowedModels: models}, nil
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
		DatabaseURL:              getEnv("DATABASE_URL", "sqlite://./data/platform.db"),
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
		DatasetUploadMaxBytes:    int64(getEnvInt("DATASET_UPLOAD_MAX_BYTES", 50*1024*1024)),
		ChromaPersistDir:         getEnv("CHROMA_PERSIST_DIR", "./data/chroma"),
		RAGTopK:                  getEnvInt("RAG_TOP_K", 5),
		RAGChunkSize:             getEnvInt("RAG_CHUNK_SIZE", 512),
		RAGChunkOverlap:          getEnvInt("RAG_CHUNK_OVERLAP", 64),
		DatasetUploadDir:         getEnv("DATASET_UPLOAD_DIR", "./data/uploads"),
		PlanProfessionalPrice:    getEnvFloat("PLAN_PROFESSIONAL_PRICE", 99),
		PlanEnterprisePrice:      getEnvFloat("PLAN_ENTERPRISE_PRICE", 999),
		AnalyticsAdminPhones:     strings.TrimSpace(os.Getenv("ANALYTICS_ADMIN_PHONES")),
		AnalyticsTokenPricePer1K: getEnvFloat("ANALYTICS_TOKEN_PRICE_PER_1K", 1),
		WhiteLabel:               whiteLabel,
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
