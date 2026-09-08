package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
)

type failingWriter struct {
	writes int
}

func (w *failingWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("fixture write failure")
}

type shortWriter struct {
	writes int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	w.writes++
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

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

func TestRunFailsWhenSuccessOutputCannotBeWritten(t *testing.T) {
	setSafeProductionEnvironment(t)
	for _, writer := range []struct {
		name string
		out  interface {
			Write([]byte) (int, error)
		}
	}{
		{name: "error", out: &failingWriter{}},
		{name: "short write", out: &shortWriter{}},
	} {
		t.Run(writer.name, func(t *testing.T) {
			if code := run(writer.out, &bytes.Buffer{}); code == 0 {
				t.Fatal("run() returned success after incomplete success output")
			}
		})
	}
}

func TestRunKeepsFailureNonzeroWhenErrorOutputCannotBeWritten(t *testing.T) {
	setSafeProductionEnvironment(t)
	unsetEnvironment(t, "ACTION_SECURITY_HMAC_KEY")

	for _, writer := range []struct {
		name string
		err  interface {
			Write([]byte) (int, error)
		}
	}{
		{name: "error", err: &failingWriter{}},
		{name: "short write", err: &shortWriter{}},
	} {
		t.Run(writer.name, func(t *testing.T) {
			if code := run(&bytes.Buffer{}, writer.err); code == 0 {
				t.Fatal("run() returned success after configuration and error-output failures")
			}
		})
	}
}

func setSafeProductionEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range runtimeEnvironmentKeys {
		unsetEnvironment(t, key)
	}
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

// Keep this list aligned with the canonical runtime-key parity list in
// internal/config/config_test.go so ambient configuration cannot affect tests.
var runtimeEnvironmentKeys = []string{
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
