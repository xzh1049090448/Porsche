package config

import (
	"regexp"
	"strings"
	"testing"
)

func TestLoadRejectsNonMySQLDatabaseURL(t *testing.T) {
	setLoadTestEnvironment(t)
	t.Setenv("DATABASE_URL", "sqlite://./data/platform.db")
	t.Setenv("SNOWFLAKE_NODE_ID", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-MySQL DATABASE_URL")
	}
}

func TestLoadRejectsUnsafeAuthProductionConfiguration(t *testing.T) {
	setLoadTestEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("REGISTER_ENABLED", "true")
	t.Setenv("REDIS_URL", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "REDIS_URL") {
		t.Fatalf("Load() error = %v, want REDIS_URL validation error", err)
	}
}

func TestLoadRejectsProductionAuthSecretsAndOrigins(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*testing.T)
		want string
	}{
		{
			name: "default JWT secret",
			set: func(t *testing.T) {
				t.Setenv("JWT_SECRET_KEY", "change-me-jwt-secret-for-dev-only")
			},
			want: "JWT_SECRET_KEY",
		},
		{
			name: "default HMAC secret",
			set: func(t *testing.T) {
				t.Setenv("AUTH_HMAC_KEY", "change-me-auth-hmac-key-for-dev-only")
			},
			want: "AUTH_HMAC_KEY",
		},
		{
			name: "non HTTPS trusted origin",
			set: func(t *testing.T) {
				t.Setenv("AUTH_TRUSTED_ORIGINS", "http://app.example.com")
			},
			want: "AUTH_TRUSTED_ORIGINS",
		},
		{
			name: "incomplete root bootstrap",
			set: func(t *testing.T) {
				t.Setenv("ROOT_BOOTSTRAP_USERNAME", "root")
				t.Setenv("ROOT_BOOTSTRAP_PASSWORD", "")
			},
			want: "ROOT_BOOTSTRAP",
		},
		{
			name: "fixed login enabled",
			set: func(t *testing.T) {
				t.Setenv("FIXED_LOGIN_ENABLED", "true")
			},
			want: "FIXED_LOGIN_ENABLED",
		},
		{
			name: "default admin token",
			set: func(t *testing.T) {
				t.Setenv("ADMIN_TOKEN", "change-me-to-a-long-random-secret")
			},
			want: "ADMIN_TOKEN",
		},
		{
			name: "reused metrics token",
			set: func(t *testing.T) {
				t.Setenv("METRICS_TOKEN", "production-jwt-secret")
			},
			want: "METRICS_TOKEN",
		},
		{
			name: "weak root bootstrap password",
			set: func(t *testing.T) {
				t.Setenv("ROOT_BOOTSTRAP_USERNAME", "root")
				t.Setenv("ROOT_BOOTSTRAP_PASSWORD", "password")
			},
			want: "ROOT_BOOTSTRAP",
		},
		{
			name: "invalid session access minutes",
			set: func(t *testing.T) {
				t.Setenv("SESSION_ACCESS_MINUTES", "invalid")
			},
			want: "SESSION_ACCESS_MINUTES",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setSafeProductionAuthEnvironment(t)
			tc.set(t)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want %s validation error", err, tc.want)
			}
		})
	}
}

func TestLoadRejectsUnsafeNonDevelopmentAuthConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*testing.T)
		want string
	}{
		{
			name: "missing admin token",
			set:  func(t *testing.T) { t.Setenv("ADMIN_TOKEN", "") },
			want: "ADMIN_TOKEN",
		},
		{
			name: "known admin placeholder",
			set:  func(t *testing.T) { t.Setenv("ADMIN_TOKEN", "change-me-for-dev-only") },
			want: "ADMIN_TOKEN",
		},
		{
			name: "short JWT secret",
			set:  func(t *testing.T) { t.Setenv("JWT_SECRET_KEY", "too-short") },
			want: "JWT_SECRET_KEY",
		},
		{
			name: "repeated HMAC secret",
			set:  func(t *testing.T) { t.Setenv("AUTH_HMAC_KEY", strings.Repeat("a", 32)) },
			want: "AUTH_HMAC_KEY",
		},
		{
			name: "reused admin token",
			set:  func(t *testing.T) { t.Setenv("ADMIN_TOKEN", "jwt-secret-material-0123456789-ABCDE") },
			want: "ADMIN_TOKEN",
		},
		{
			name: "invalid register switch",
			set:  func(t *testing.T) { t.Setenv("REGISTER_ENABLED", "enabled") },
			want: "REGISTER_ENABLED",
		},
		{
			name: "access lifetime above fixed value",
			set:  func(t *testing.T) { t.Setenv("SESSION_ACCESS_MINUTES", "16") },
			want: "SESSION_ACCESS_MINUTES",
		},
		{
			name: "invalid Redis URL",
			set:  func(t *testing.T) { t.Setenv("REDIS_URL", "not-a-url") },
			want: "REDIS_URL",
		},
		{
			name: "SMS development mode enabled",
			set:  func(t *testing.T) { t.Setenv("SMS_DEV_MODE", "true") },
			want: "SMS_DEV_MODE",
		},
		{
			name: "trusted origin with path",
			set:  func(t *testing.T) { t.Setenv("AUTH_TRUSTED_ORIGINS", "https://app.example.com/login") },
			want: "AUTH_TRUSTED_ORIGINS",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setSafeProductionAuthEnvironment(t)
			tc.set(t)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want %s validation error", err, tc.want)
			}
		})
	}
}

func TestLoadNormalizesAndRestrictsApplicationEnvironment(t *testing.T) {
	setSafeProductionAuthEnvironment(t)
	t.Setenv("APP_ENV", " Production ")
	settings, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if settings.AppEnv != "production" {
		t.Fatalf("AppEnv = %q, want production", settings.AppEnv)
	}

	for _, appEnv := range []string{"staging", "test"} {
		t.Run(appEnv, func(t *testing.T) {
			setSafeProductionAuthEnvironment(t)
			t.Setenv("APP_ENV", appEnv)
			t.Setenv("REDIS_URL", "")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "REDIS_URL") {
				t.Fatalf("Load() error = %v, want REDIS_URL validation error", err)
			}
		})
	}

	setSafeProductionAuthEnvironment(t)
	t.Setenv("APP_ENV", "preview")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
		t.Fatalf("Load() error = %v, want APP_ENV validation error", err)
	}
}

func TestLoadMigrationSettingsDoesNotRequireUpstreamConfiguration(t *testing.T) {
	t.Setenv("UPSTREAM_REGION", "")
	t.Setenv("JIEKOU_API_KEY", "")
	t.Setenv("JIEKOU_ALLOWED_MODELS", "")
	t.Setenv("DATABASE_URL", "mysql://test:test@localhost:3306/porsche_test")
	t.Setenv("SNOWFLAKE_NODE_ID", "7")

	got, err := LoadMigrationSettings()
	if err != nil {
		t.Fatalf("LoadMigrationSettings() error = %v", err)
	}
	if got.DatabaseURL == "" || got.SnowflakeNodeID != 7 {
		t.Fatalf("unexpected migration settings: %#v", got)
	}
}

func TestWhiteLabelSettingsFailClosedAndUseFixedRegionURLs(t *testing.T) {
	for _, tc := range []struct {
		name, region, key, models, wantURL string
		wantErr                            bool
	}{
		{name: "cn", region: "cn", key: "test-key", models: "model-a,model-b", wantURL: "https://api.highwayapi.ai/openai/v1"},
		{name: "global", region: "global", key: "test-key", models: "model-a", wantURL: "https://api.jiekou.ai/openai/v1"},
		{name: "missing region", key: "test-key", models: "model-a", wantErr: true},
		{name: "invalid region", region: "other", key: "test-key", models: "model-a", wantErr: true},
		{name: "missing key", region: "cn", models: "model-a", wantErr: true},
		{name: "whitespace key", region: "cn", key: " \t ", models: "model-a", wantErr: true},
		{name: "empty allowlist", region: "cn", key: "test-key", wantErr: true},
		{name: "whitespace allowlist", region: "cn", key: "test-key", models: " , \t ", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseWhiteLabelSettings(tc.region, tc.key, tc.models)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected configuration error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseWhiteLabelSettings() error = %v", err)
			}
			if got.BaseURL != tc.wantURL || !got.Allows("model-a") {
				t.Fatalf("unexpected settings: %#v", got)
			}
		})
	}
}

func TestParseWhiteLabelSettingsSupportsExactAndRegexModels(t *testing.T) {
	settings, err := ParseWhiteLabelSettings("cn", "test-key", "model-a,re:^zai-org/.+$,re:^.+$")
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"model-a", "zai-org/glm-5.1", "other/model"} {
		if !settings.Allows(model) {
			t.Fatalf("Allows(%q) = false, want true", model)
		}
	}
}

func TestParseWhiteLabelSettingsRejectsInvalidRegex(t *testing.T) {
	_, err := ParseWhiteLabelSettings("cn", "test-key", "re:[")
	if err == nil || strings.Contains(err.Error(), "test-key") {
		t.Fatalf("want sanitized config error, got %v", err)
	}
}

func TestParseWhiteLabelSettingsRejectsEmptyRegex(t *testing.T) {
	_, err := ParseWhiteLabelSettings("cn", "test-key", "re:")
	if err == nil {
		t.Fatal("want error")
	}
}

func TestParseWhiteLabelSettingsAcceptsRegexOnlyAllowlist(t *testing.T) {
	settings, err := ParseWhiteLabelSettings("cn", "test-key", " re:^zai-org/.+$ ")
	if err != nil {
		t.Fatalf("ParseWhiteLabelSettings() error = %v", err)
	}
	if !settings.Allows("zai-org/glm-5.1") {
		t.Fatal("regex-only allowlist did not match")
	}
}

func TestWhiteLabelSettingsAllowsSkipsNilPattern(t *testing.T) {
	settings := WhiteLabelSettings{AllowedModelPatterns: []*regexp.Regexp{nil, regexp.MustCompile(`^zai-org/.+$`)}}
	if settings.Allows("other/model") {
		t.Fatal("nil pattern unexpectedly allowed a model")
	}
	if !settings.Allows("zai-org/glm-5.1") {
		t.Fatal("valid pattern after nil entry was not evaluated")
	}
}

func TestParseWhiteLabelSettingsTreatsNonRegexPrefixAsExact(t *testing.T) {
	settings, err := ParseWhiteLabelSettings("cn", "test-key", "regex:^.+$")
	if err != nil {
		t.Fatalf("ParseWhiteLabelSettings() error = %v", err)
	}
	if !settings.Allows("regex:^.+$") || settings.Allows("other/model") {
		t.Fatal("non-re: model entry was not treated as an exact ID")
	}
}

func TestLoadParsesWhiteLabelEnvironmentAndFailsClosed(t *testing.T) {
	setLoadTestEnvironment(t)
	t.Setenv("UPSTREAM_REGION", "global")
	t.Setenv("JIEKOU_API_KEY", " test-key ")
	t.Setenv("JIEKOU_ALLOWED_MODELS", " model-a, model-b ")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.WhiteLabel.BaseURL != "https://api.jiekou.ai/openai/v1" || !got.WhiteLabel.Allows("model-b") {
		t.Fatalf("unexpected white-label settings: %#v", got.WhiteLabel)
	}

	t.Setenv("JIEKOU_API_KEY", " \t")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a whitespace-only API key")
	}
}

func TestLoadDoesNotRetainLegacyUpstreamKeys(t *testing.T) {
	setLoadTestEnvironment(t)
	t.Setenv("UPSTREAM_REGION", "cn")
	t.Setenv("JIEKOU_API_KEY", "test-key")
	t.Setenv("JIEKOU_ALLOWED_MODELS", "model-a")
	t.Setenv("DEEP"+"SEEK_API_KEYS", "obsolete-secret")

	settings, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if settings.WhiteLabel.APIKey != "test-key" {
		t.Fatalf("Load() did not retain white-label key")
	}
}

func setLoadTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("UPSTREAM_REGION", "cn")
	t.Setenv("JIEKOU_API_KEY", "test-key")
	t.Setenv("JIEKOU_ALLOWED_MODELS", "model-a")
	t.Setenv("DATABASE_URL", "mysql://test:test@localhost:3306/platform")
	t.Setenv("SNOWFLAKE_NODE_ID", "0")
}

func setSafeProductionAuthEnvironment(t *testing.T) {
	t.Helper()
	setLoadTestEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("JWT_SECRET_KEY", "jwt-secret-material-0123456789-ABCDE")
	t.Setenv("AUTH_HMAC_KEY", "hmac-secret-material-0123456789-ABCD")
	t.Setenv("ADMIN_TOKEN", "admin-secret-material-0123456789-ABC")
	t.Setenv("METRICS_TOKEN", "metrics-secret-material-0123456789-AB")
	t.Setenv("AUTH_TRUSTED_ORIGINS", "https://app.example.com")
	t.Setenv("ROOT_BOOTSTRAP_USERNAME", "")
	t.Setenv("ROOT_BOOTSTRAP_PASSWORD", "")
	t.Setenv("FIXED_LOGIN_ENABLED", "false")
	t.Setenv("FIXED_LOGIN_PHONE", "disabled")
	t.Setenv("FIXED_LOGIN_PASSWORD", "disabled")
	t.Setenv("SMS_DEV_MODE", "false")
}
