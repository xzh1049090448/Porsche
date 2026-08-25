package config

import "testing"

func TestLoadRejectsNonMySQLDatabaseURL(t *testing.T) {
	setLoadTestEnvironment(t)
	t.Setenv("DATABASE_URL", "sqlite://./data/platform.db")
	t.Setenv("SNOWFLAKE_NODE_ID", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-MySQL DATABASE_URL")
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
