package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Settings struct {
	AppEnv                   string
	Host                     string
	Port                     int
	AdminToken               string
	ModelsConfigPath         string
	ClientsConfigPath        string
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
	RealNameAutoVerify       bool
	DatasetUploadMaxBytes    int64
	ChromaPersistDir         string
	RAGTopK                  int
	RAGChunkSize             int
	RAGChunkOverlap          int
	DatasetUploadDir         string
	PlatformClientSecret     string
	PlanProfessionalPrice    float64
	PlanEnterprisePrice      float64
	AnalyticsAdminPhones     string
	AnalyticsTokenPricePer1K float64

	// Upstream API keys from env (env name -> value)
	EnvKeys map[string]string
}

func Load() (*Settings, error) {
	_ = godotenv.Load()

	s := &Settings{
		AppEnv:                   getEnv("APP_ENV", "development"),
		Host:                     getEnv("HOST", "0.0.0.0"),
		Port:                     getEnvInt("PORT", 8000),
		AdminToken:               getEnv("ADMIN_TOKEN", "change-me-for-dev-only"),
		ModelsConfigPath:         getEnv("MODELS_CONFIG_PATH", "config/models.yaml"),
		ClientsConfigPath:        getEnv("CLIENTS_CONFIG_PATH", "config/clients.yaml"),
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
		RealNameAutoVerify:       getEnvBool("REAL_NAME_AUTO_VERIFY", true),
		DatasetUploadMaxBytes:    int64(getEnvInt("DATASET_UPLOAD_MAX_BYTES", 50*1024*1024)),
		ChromaPersistDir:         getEnv("CHROMA_PERSIST_DIR", "./data/chroma"),
		RAGTopK:                  getEnvInt("RAG_TOP_K", 5),
		RAGChunkSize:             getEnvInt("RAG_CHUNK_SIZE", 512),
		RAGChunkOverlap:          getEnvInt("RAG_CHUNK_OVERLAP", 64),
		DatasetUploadDir:         getEnv("DATASET_UPLOAD_DIR", "./data/uploads"),
		PlatformClientSecret:     getEnv("PLATFORM_CLIENT_SECRET", "sk-platform-internal"),
		PlanProfessionalPrice:    getEnvFloat("PLAN_PROFESSIONAL_PRICE", 99),
		PlanEnterprisePrice:      getEnvFloat("PLAN_ENTERPRISE_PRICE", 999),
		AnalyticsAdminPhones:     strings.TrimSpace(os.Getenv("ANALYTICS_ADMIN_PHONES")),
		AnalyticsTokenPricePer1K: getEnvFloat("ANALYTICS_TOKEN_PRICE_PER_1K", 1),
		EnvKeys:                  make(map[string]string),
	}

	if s.AppEnv == "development" {
		if os.Getenv("BILLING_ALLOW_MOCK_PAYMENT") == "" {
			s.BillingAllowMockPayment = true
		}
		if s.AnalyticsAdminPhones == "" {
			s.AnalyticsAdminPhones = s.FixedLoginPhone
		}
	}

	for _, name := range []string{
		"OPENAI_API_KEYS", "ANTHROPIC_API_KEYS", "GOOGLE_API_KEYS", "MISTRAL_API_KEYS",
		"QWEN_API_KEYS", "ERNIE_API_KEYS", "HUNYUAN_API_KEYS", "DOUBAO_API_KEYS",
		"DEEPSEEK_API_KEYS", "GLM_API_KEYS", "MOONSHOT_API_KEYS", "YI_API_KEYS",
	} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			s.EnvKeys[name] = v
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
