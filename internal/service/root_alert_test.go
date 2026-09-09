package service

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestRootAlertFingerprintStableAndSeparatesIdentity(t *testing.T) {
	a := rootAlertFingerprint(models.RootAlertTypeUpstreamMissing, "model-a", "catalog")
	if a != rootAlertFingerprint(models.RootAlertTypeUpstreamMissing, "model-a", "catalog") || a == rootAlertFingerprint(models.RootAlertTypeUpstreamMissing, "model-b", "catalog") || len(a) != 64 {
		t.Fatalf("unstable fingerprint %q", a)
	}
}

func TestRootAlertPayloadSanitizedAndBounded(t *testing.T) {
	p, err := projectRootAlertPayload(models.RootAlertTypeCatalogSyncFailure, models.JSONMap{"provider": "safe-provider", "error_code": "upstream_timeout", "observed_at": int64(1900000000000), "authorization": "Bearer secret", "upstream_response": map[string]any{"key": "secret", "body": "secret"}, "wrapper": map[string]any{"safe": "secret"}, "array": []any{"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil || len(b) > rootAlertPayloadLimit || strings.Contains(strings.ToLower(string(b)), "secret") || strings.Contains(string(b), strings.Repeat("x", 600)) {
		t.Fatalf("unsafe payload %s err=%v", b, err)
	}
}

func TestRootAlertPayloadUsesPerKindTypedAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		typ     models.RootAlertType
		payload models.JSONMap
		want    []string
	}{
		{"price", models.RootAlertTypePublishedPriceBelowUpstream, models.JSONMap{"model_key": "model-a", "provider": "vendor", "price_component": "input", "published_price_usd_per_million_tokens": "1.25", "upstream_price_usd_per_million_tokens": "2.50", "observed_at": int64(1900000000000), "body": "secret"}, []string{"model_key", "observed_at", "price_component", "provider", "published_price_usd_per_million_tokens", "upstream_price_usd_per_million_tokens"}},
		{"missing", models.RootAlertTypeUpstreamMissing, models.JSONMap{"model_key": "model-a", "provider": "vendor", "consecutive_absences": 3, "observed_at": int64(1900000000000), "nested": map[string]any{"body": "secret"}}, []string{"consecutive_absences", "model_key", "observed_at", "provider"}},
		{"renderer", models.RootAlertTypeRendererFailure, models.JSONMap{"release_version": int64(7), "render_job_guid": "123", "error_code": "render_validation_failed", "observed_at": int64(1900000000000), "headers": map[string]any{"x": "secret"}}, []string{"error_code", "observed_at", "release_version", "render_job_guid"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := projectRootAlertPayload(tc.typ, tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(got))
			for k := range got {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, tc.want) {
				t.Fatalf("keys=%v want=%v", keys, tc.want)
			}
		})
	}
}

func TestRootAlertPayloadCanonicalEncodingIsDeterministic(t *testing.T) {
	a, _ := projectRootAlertPayload(models.RootAlertTypeCatalogSyncFailure, models.JSONMap{"observed_at": int64(9), "error_code": "timeout", "provider": "vendor"})
	b, _ := projectRootAlertPayload(models.RootAlertTypeCatalogSyncFailure, models.JSONMap{"provider": "vendor", "observed_at": int64(9), "error_code": "timeout"})
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	if string(aj) != string(bj) {
		t.Fatalf("canonical mismatch %s %s", aj, bj)
	}
}

func TestRootAlertPayloadRejectsUnsafeAllowedValues(t *testing.T) {
	bad := []models.JSONMap{
		{"provider": "Bearer secret", "error_code": "timeout", "observed_at": int64(1)},
		{"provider": "vendor\u0000hidden", "error_code": "timeout", "observed_at": int64(1)},
		{"provider": "vendor\u200bhidden", "error_code": "timeout", "observed_at": int64(1)},
		{"provider": strings.Repeat("界", 129), "error_code": "timeout", "observed_at": int64(1)},
		{"provider": "vendor", "error_code": "secret", "observed_at": int64(1)},
		{"provider": "vendor", "error_code": map[string]any{"body": "secret"}, "observed_at": int64(1)},
		{"provider": "vendor", "error_code": "timeout", "observed_at": "1900000000000"},
	}
	for i, p := range bad {
		if got, err := projectRootAlertPayload(models.RootAlertTypeCatalogSyncFailure, p); err == nil || got != nil {
			t.Fatalf("case %d accepted %#v", i, got)
		}
	}
}

func TestRootAlertAuditDetailHasFixedScalarProjection(t *testing.T) {
	got := projectRootAlertAuditDetail(models.JSONMap{"alert_type": "catalog_sync_failure", "fingerprint": strings.Repeat("a", 64), "alert_guid": "123", "wrapper": map[string]any{"body": "secret"}, "note": "Bearer secret"})
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "secret") || len(got) != 3 {
		t.Fatalf("audit detail=%s", b)
	}
}

func TestRootAlertDeliveryOnlyInApp(t *testing.T) {
	var _ RootAlertDelivery = InAppRootAlertDelivery{}
	if (InAppRootAlertDelivery{}).Channel() != RootAlertChannelInApp {
		t.Fatal("unexpected channel")
	}
}

func TestRootAlertViewHasExactFrozenContractFields(t *testing.T) {
	b, err := json.Marshal(RootAlertView{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"acknowledged", "created_at", "guid", "read", "state", "type", "updated_at"}
	got := make([]string, 0, len(fields))
	for k := range fields {
		got = append(got, k)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fields=%v want=%v", got, want)
	}
}
