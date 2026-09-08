package config

import (
	"encoding/base64"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestLoadActionSecurityRootKey(t *testing.T) {
	validBytes := []byte("action-security-root-key-32-byte")
	valid := base64.RawURLEncoding.EncodeToString(validBytes)
	for _, tc := range []struct {
		name, env, raw, wantReason string
		declared                   bool
	}{
		{name: "production valid", env: "production", raw: valid, declared: true},
		{name: "staging valid", env: "staging", raw: valid, declared: true},
		{name: "production missing", env: "production", wantReason: "missing"},
		{name: "staging missing", env: "staging", wantReason: "missing"},
		{name: "development omitted", env: "development"},
		{name: "test omitted", env: "test"},
		{name: "development declared empty", env: "development", raw: "", declared: true, wantReason: "missing"},
		{name: "development declared invalid", env: "development", raw: "short", declared: true, wantReason: "invalid_length"},
		{name: "test declared invalid", env: "test", raw: "not valid", declared: true, wantReason: "invalid_length"},
		{name: "staging declared invalid", env: "staging", raw: "not valid", declared: true, wantReason: "invalid_length"},
		{name: "production invalid encoding", env: "production", raw: valid[:42] + "+", declared: true, wantReason: "invalid_encoding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setLoadTestEnvironment(t)
			t.Setenv("APP_ENV", tc.env)
			if tc.env != "development" {
				setSafeProductionAuthEnvironment(t)
				t.Setenv("APP_ENV", tc.env)
			}
			unsetEnvironment(t, "ACTION_SECURITY_HMAC_KEY")
			if tc.declared {
				t.Setenv("ACTION_SECURITY_HMAC_KEY", tc.raw)
			}
			got, err := Load()
			if tc.wantReason != "" {
				if err == nil || err.Error() != "ACTION_SECURITY_HMAC_KEY: "+tc.wantReason {
					t.Fatalf("Load() error = %v, want redacted reason %q", err, tc.wantReason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if tc.declared {
				if base64.RawURLEncoding.EncodeToString(got.ActionSecurityHMACKey) != valid {
					t.Fatal("Load() did not store decoded action-security key bytes")
				}
			} else if got.ActionSecurityHMACKey != nil {
				t.Fatal("Load() populated an omitted optional action-security key")
			}
		})
	}
}

func TestActionSecurityRootKeyReuseChecksAllSecrets(t *testing.T) {
	calls := 0
	compare := func(_, _ []byte) int {
		calls++
		if calls == 1 {
			return 1
		}
		return 0
	}
	if !actionSecurityRootKeyReused([]byte("root"), []byte("auth"), []byte("jwt"), []byte("upstream"), compare) {
		t.Fatal("actionSecurityRootKeyReused() did not retain the first match")
	}
	if calls != 3 {
		t.Fatalf("constant-time comparisons = %d, want 3", calls)
	}
}

func TestLoadActionSecurityRootKeyRejectsReuse(t *testing.T) {
	for _, name := range []string{"AUTH_HMAC_KEY", "JWT_SECRET_KEY", "JIEKOU_API_KEY"} {
		t.Run(name, func(t *testing.T) {
			setSafeProductionAuthEnvironment(t)
			reused := []byte("distinct-auth-key-material-12345")
			t.Setenv(name, string(reused))
			t.Setenv("ACTION_SECURITY_HMAC_KEY", base64.RawURLEncoding.EncodeToString(reused))
			_, err := Load()
			if err == nil || err.Error() != "ACTION_SECURITY_HMAC_KEY: key_reuse" {
				t.Fatalf("Load() error = %v, want redacted key_reuse", err)
			}
		})
	}
}

func TestValidateRootBootstrapCredentials(t *testing.T) {
	for _, tc := range []struct {
		name     string
		username string
		password string
		wantErr  string
	}{
		{name: "valid", username: "root_admin", password: "Aa1@0123456789ab"},
		{name: "username contains spaces", username: "root admin", password: "Aa1@0123456789ab", wantErr: "ROOT_BOOTSTRAP credentials are invalid"},
		{name: "password too short", username: "root_admin", password: "Aa1@short", wantErr: "ROOT_BOOTSTRAP credentials are invalid"},
		{name: "password missing uppercase", username: "root_admin", password: "aa1@0123456789ab", wantErr: "ROOT_BOOTSTRAP credentials are invalid"},
		{name: "password contains non ASCII emoji", username: "root_admin", password: "Aa1@abcdefgh🙂", wantErr: "ROOT_BOOTSTRAP credentials are invalid"},
		{name: "development default password", username: "root_admin", password: "change-me-root-bootstrap-password-for-dev-only", wantErr: "ROOT_BOOTSTRAP_PASSWORD must not use the development default in production"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRootBootstrapCredentials(tc.username, tc.password)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateRootBootstrapCredentials() error = %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("ValidateRootBootstrapCredentials() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadRejectsRootBootstrapEnvironmentOutsideDevelopment(t *testing.T) {
	const wantErr = "ROOT_BOOTSTRAP environment variables are not allowed; use the one-shot bootstrap-root command"

	for _, tc := range []struct {
		name   string
		appEnv string
		set    func(*testing.T)
	}{
		{name: "empty username", appEnv: "production", set: func(t *testing.T) { t.Setenv("ROOT_BOOTSTRAP_USERNAME", "") }},
		{name: "empty password", appEnv: "staging", set: func(t *testing.T) { t.Setenv("ROOT_BOOTSTRAP_PASSWORD", "") }},
		{name: "whitespace username", appEnv: "test", set: func(t *testing.T) { t.Setenv("ROOT_BOOTSTRAP_USERNAME", " \t ") }},
		{name: "unknown prefixed key", appEnv: "production", set: func(t *testing.T) { t.Setenv("ROOT_BOOTSTRAP_ROTATION_TOKEN", "sensitive-root-token") }},
		{name: "valid credential pair", appEnv: "production", set: func(t *testing.T) {
			t.Setenv("ROOT_BOOTSTRAP_USERNAME", "root_admin")
			t.Setenv("ROOT_BOOTSTRAP_PASSWORD", "Aa1@0123456789ab")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setSafeProductionAuthEnvironment(t)
			t.Setenv("APP_ENV", tc.appEnv)
			tc.set(t)

			_, err := Load()
			if err == nil || err.Error() != wantErr {
				t.Fatalf("Load() error = %v, want %q", err, wantErr)
			}
			if strings.Contains(err.Error(), "sensitive-root-token") || strings.Contains(err.Error(), "Aa1@0123456789ab") {
				t.Fatalf("Load() error exposed a Root bootstrap value: %v", err)
			}
		})
	}
}

func TestLoadAllowsUnrelatedRootBootstrapNearMatchOutsideDevelopment(t *testing.T) {
	setSafeProductionAuthEnvironment(t)
	t.Setenv("ROOT_BOOTSTRAPX_TOKEN", "unrelated-value")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v, want unrelated near-match to be accepted", err)
	}
}

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
	t.Setenv("ACTION_SECURITY_HMAC_KEY", base64.RawURLEncoding.EncodeToString([]byte("action-security-root-key-32-byte")))
	t.Setenv("REGISTER_ENABLED", "true")
	t.Setenv("REDIS_URL", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "REDIS_URL") {
		t.Fatalf("Load() error = %v, want REDIS_URL validation error", err)
	}
}

func TestLoadAllowsOmittedFixedLoginCredentialsOutsideDevelopment(t *testing.T) {
	setSafeProductionAuthEnvironment(t)
	unsetEnvironment(t, "FIXED_LOGIN_PHONE")
	unsetEnvironment(t, "FIXED_LOGIN_PASSWORD")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v, want omitted fixed-login credentials to be accepted", err)
	}
}

func TestLoadRejectsDeclaredFixedLoginCredentialsOutsideDevelopment(t *testing.T) {
	const wantErr = "FIXED_LOGIN credentials are not allowed outside development"
	for _, tc := range []struct {
		name, appEnv, key, value string
	}{
		{name: "production phone", appEnv: "production", key: "FIXED_LOGIN_PHONE", value: "13800138000"},
		{name: "production empty phone", appEnv: "production", key: "FIXED_LOGIN_PHONE"},
		{name: "staging password", appEnv: "staging", key: "FIXED_LOGIN_PASSWORD", value: "secret"},
		{name: "test empty password", appEnv: "test", key: "FIXED_LOGIN_PASSWORD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setSafeProductionAuthEnvironment(t)
			t.Setenv("APP_ENV", tc.appEnv)
			unsetEnvironment(t, "FIXED_LOGIN_PHONE")
			unsetEnvironment(t, "FIXED_LOGIN_PASSWORD")
			t.Setenv(tc.key, tc.value)

			_, err := Load()
			if err == nil || err.Error() != wantErr {
				t.Fatalf("Load() error = %v, want %q", err, wantErr)
			}
		})
	}
}

func TestLoadRetainsDevelopmentFixedLoginCredentials(t *testing.T) {
	setLoadTestEnvironment(t)
	t.Setenv("APP_ENV", "development")
	t.Setenv("FIXED_LOGIN_ENABLED", "true")
	t.Setenv("FIXED_LOGIN_PHONE", "13900000000")
	t.Setenv("FIXED_LOGIN_PASSWORD", "development-password")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !got.FixedLoginEnabled || got.FixedLoginPhone != "13900000000" || got.FixedLoginPassword != "development-password" {
		t.Fatalf("fixed-login settings = (%t, %q, %q), want development values", got.FixedLoginEnabled, got.FixedLoginPhone, got.FixedLoginPassword)
	}
}

func TestEnvironmentExampleDocumentsEveryRuntimeSettingExactlyOnce(t *testing.T) {
	expected := []string{
		"ACTION_SECURITY_HMAC_KEY", "ADMIN_TOKEN", "ALLOWED_HOSTS", "ANALYTICS_ADMIN_PHONES",
		"ANALYTICS_TOKEN_PRICE_PER_1K", "APP_ENV", "AUTH_HMAC_KEY", "AUTH_TRUSTED_ORIGINS",
		"BILLING_ALLOW_MOCK_PAYMENT", "CIRCUIT_FAILURE_THRESHOLD", "CIRCUIT_OPEN_SECONDS",
		"DATABASE_URL", "FIXED_LOGIN_ENABLED", "FIXED_LOGIN_PASSWORD", "FIXED_LOGIN_PHONE",
		"HOST", "JIEKOU_ALLOWED_MODELS", "JIEKOU_API_KEY", "JWT_EXPIRE_MINUTES", "JWT_SECRET_KEY",
		"LOG_LEVEL", "METRICS_TOKEN", "PASSWORD_LOGIN_ENABLED", "PASSWORD_REGISTER_ENABLED",
		"PLAN_ENTERPRISE_PRICE", "PLAN_PROFESSIONAL_PRICE", "PORT", "REAL_NAME_AUTO_VERIFY",
		"REDIS_URL", "REFRESH_REPLAY_SECONDS", "REGISTER_ENABLED", "ROOT_BOOTSTRAP_PASSWORD",
		"ROOT_BOOTSTRAP_USERNAME", "SESSION_ACCESS_MINUTES", "SESSION_DAYS", "SESSION_ISSUE_LIMIT_24H",
		"SESSION_MAX_ACTIVE", "SMS_DEV_MODE", "SMS_SEND_LIMIT_PER_IP", "SMS_SEND_LIMIT_PER_PHONE",
		"SMS_VERIFY_MAX_ATTEMPTS", "SNOWFLAKE_NODE_ID", "TRUSTED_PROXY_CIDRS", "TRUST_PROXY_HEADERS",
		"UPSTREAM_REGION", "UPSTREAM_TIMEOUT_SECONDS",
	}

	raw, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	activeAssignment := regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=`)
	commentedEmptyAssignment := regexp.MustCompile(`^# ([A-Z][A-Z0-9_]*)=$`)
	counts := make(map[string]int, len(expected))
	for _, line := range strings.Split(string(raw), "\n") {
		if match := activeAssignment.FindStringSubmatch(line); match != nil {
			counts[match[1]]++
		}
		if match := commentedEmptyAssignment.FindStringSubmatch(line); match != nil {
			counts[match[1]]++
		}
	}
	for _, key := range expected {
		if counts[key] != 1 {
			t.Errorf(".env.example records %s %d times, want exactly once", key, counts[key])
		}
	}
	if !strings.Contains(string(raw), "# ACTION_SECURITY_HMAC_KEY=\n") {
		t.Error(".env.example must contain exact commented empty assignment # ACTION_SECURITY_HMAC_KEY=")
	}
	validActionKey := regexp.MustCompile(`(?m)^#? ?ACTION_SECURITY_HMAC_KEY=[A-Za-z0-9_-]{43}$`)
	if validActionKey.Match(raw) {
		t.Error(".env.example contains a valid action-security key")
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
	clearRootBootstrapEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("JWT_SECRET_KEY", "jwt-secret-material-0123456789-ABCDE")
	t.Setenv("AUTH_HMAC_KEY", "hmac-secret-material-0123456789-ABCD")
	t.Setenv("ADMIN_TOKEN", "admin-secret-material-0123456789-ABC")
	t.Setenv("METRICS_TOKEN", "metrics-secret-material-0123456789-AB")
	t.Setenv("AUTH_TRUSTED_ORIGINS", "https://app.example.com")
	t.Setenv("FIXED_LOGIN_ENABLED", "false")
	unsetEnvironment(t, "FIXED_LOGIN_PHONE")
	unsetEnvironment(t, "FIXED_LOGIN_PASSWORD")
	t.Setenv("SMS_DEV_MODE", "false")
	t.Setenv("ACTION_SECURITY_HMAC_KEY", base64.RawURLEncoding.EncodeToString([]byte("action-security-root-key-32-byte")))
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("os.Unsetenv(%q) error = %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			if err := os.Setenv(key, value); err != nil {
				t.Errorf("os.Setenv(%q) error = %v", key, err)
			}
			return
		}
		if err := os.Unsetenv(key); err != nil {
			t.Errorf("os.Unsetenv(%q) error = %v", key, err)
		}
	})
}

func clearRootBootstrapEnvironment(t *testing.T) {
	t.Helper()
	previous := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "ROOT_BOOTSTRAP_") {
			previous[key] = value
			if err := os.Unsetenv(key); err != nil {
				t.Fatalf("os.Unsetenv(%q) error = %v", key, err)
			}
		}
	}
	t.Cleanup(func() {
		for key, value := range previous {
			if err := os.Setenv(key, value); err != nil {
				t.Errorf("os.Setenv(%q) error = %v", key, err)
			}
		}
	})
}
