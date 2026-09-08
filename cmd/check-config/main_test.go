package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestRunAcceptsSafeProductionConfiguration(t *testing.T) {
	setSafeProductionEnvironment(t)

	var stdout, stderr bytes.Buffer
	if code := run(&stdout, &stderr); code != 0 {
		t.Fatalf("run() exit = %d, want 0; stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != "configuration valid\n" {
		t.Fatalf("stdout = %q, want exact success message", got)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestRunReportsSanitizedConfigurationFailure(t *testing.T) {
	const sensitive = "unique-check-config-secret-value-9f643a"
	setSafeProductionEnvironment(t)
	t.Setenv("JIEKOU_API_KEY", sensitive)
	unsetEnvironment(t, "ACTION_SECURITY_HMAC_KEY")

	var stdout, stderr bytes.Buffer
	if code := run(&stdout, &stderr); code != 1 {
		t.Fatalf("run() exit = %d, want 1", code)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
	if got := stderr.String(); got != "ACTION_SECURITY_HMAC_KEY: missing\n" {
		t.Fatalf("stderr = %q, want exact sanitized error", got)
	}
	if strings.Contains(stdout.String(), sensitive) || strings.Contains(stderr.String(), sensitive) {
		t.Fatal("run() exposed an environment value")
	}
}

func setSafeProductionEnvironment(t *testing.T) {
	t.Helper()
	clearPrefixedEnvironment(t, "ROOT_BOOTSTRAP_")
	t.Setenv("APP_ENV", "production")
	t.Setenv("UPSTREAM_REGION", "cn")
	t.Setenv("JIEKOU_API_KEY", "fixture-upstream-secret-unique")
	t.Setenv("JIEKOU_ALLOWED_MODELS", "fixture-model")
	t.Setenv("DATABASE_URL", "mysql://fixture:fixture@127.0.0.1:3306/fixture")
	t.Setenv("SNOWFLAKE_NODE_ID", "0")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("JWT_SECRET_KEY", "fixture-jwt-secret-material-0123456789")
	t.Setenv("AUTH_HMAC_KEY", "fixture-auth-hmac-material-0123456789")
	t.Setenv("ADMIN_TOKEN", "fixture-admin-token-material-0123456789")
	t.Setenv("METRICS_TOKEN", "fixture-metrics-token-material-01234567")
	t.Setenv("AUTH_TRUSTED_ORIGINS", "https://fixture.example.com")
	t.Setenv("FIXED_LOGIN_ENABLED", "false")
	unsetEnvironment(t, "FIXED_LOGIN_PHONE")
	unsetEnvironment(t, "FIXED_LOGIN_PASSWORD")
	t.Setenv("SMS_DEV_MODE", "false")
	t.Setenv("ACTION_SECURITY_HMAC_KEY", base64.RawURLEncoding.EncodeToString([]byte("fixture-action-root-key-32-bytes")))
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("os.Unsetenv(%q) error = %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func clearPrefixedEnvironment(t *testing.T, prefix string) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, prefix) {
			unsetEnvironment(t, key)
		}
	}
}
