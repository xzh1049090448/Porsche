package service

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func TestRootAlertFingerprintStableAndSeparatesIdentity(t *testing.T) {
	a := rootAlertFingerprint(models.RootAlertTypeUpstreamMissing, "model-a", "catalog")
	if a != rootAlertFingerprint(models.RootAlertTypeUpstreamMissing, "model-a", "catalog") || a == rootAlertFingerprint(models.RootAlertTypeUpstreamMissing, "model-b", "catalog") || len(a) != 64 {
		t.Fatalf("unstable fingerprint %q", a)
	}
}

func TestRootAlertPayloadSchemasAreExactRequiredAndIdentityBound(t *testing.T) {
	tests := []struct {
		typ     models.RootAlertType
		key     string
		payload models.JSONMap
	}{
		{models.RootAlertTypePublishedPriceBelowUpstream, "model-a", models.JSONMap{"model_key": "model-a", "price_component": "input", "current_price_usd_per_million_tokens": "1.25", "upstream_price_usd_per_million_tokens": "2.50", "observed_at": int64(9)}},
		{models.RootAlertTypeUpstreamMissing, "model-a", models.JSONMap{"model_key": "model-a", "consecutive_absences": int64(1), "observed_at": int64(9)}},
		{models.RootAlertTypeAutomaticInactivation, "model-a", models.JSONMap{"model_key": "model-a", "consecutive_absences": int64(3), "reason_code": "upstream_removed", "observed_at": int64(9)}},
		{models.RootAlertTypeUpstreamReappearance, "model-a", models.JSONMap{"model_key": "model-a", "observed_at": int64(9)}},
		{models.RootAlertTypeCatalogSyncFailure, "", models.JSONMap{"error_code": "timeout", "observed_at": int64(9)}},
		{models.RootAlertTypePriceNotComparable, "model-a", models.JSONMap{"model_key": "model-a", "price_component": "output", "reason_code": "invalid_upstream_price", "observed_at": int64(9)}},
		{models.RootAlertTypeRendererFailure, "", models.JSONMap{"release_version": int64(7), "render_job_guid": "123", "error_code": "validation_failed", "observed_at": int64(9)}},
	}
	for _, tc := range tests {
		if got, err := projectRootAlertPayload(tc.typ, tc.key, tc.payload); err != nil || len(got) == 0 {
			t.Fatalf("type=%s got=%#v err=%v", tc.typ.String(), got, err)
		}
	}
}

func TestRootAlertModelScopeAndRequiredConfigAreExplicit(t *testing.T) {
	modelScoped := []models.RootAlertType{models.RootAlertTypePublishedPriceBelowUpstream, models.RootAlertTypeUpstreamMissing, models.RootAlertTypeAutomaticInactivation, models.RootAlertTypeUpstreamReappearance, models.RootAlertTypePriceNotComparable}
	for _, typ := range modelScoped {
		if !rootAlertRequiresModelConfig(typ) {
			t.Fatalf("%s must require config", typ.String())
		}
	}
	for _, typ := range []models.RootAlertType{models.RootAlertTypeCatalogSyncFailure, models.RootAlertTypeRendererFailure} {
		if rootAlertRequiresModelConfig(typ) {
			t.Fatalf("%s must be global", typ.String())
		}
	}
}

func TestRootAlertConfigLoadErrorClassificationPreservesDatabaseFailures(t *testing.T) {
	if got := mapRootAlertConfigLoadError(gorm.ErrRecordNotFound); status(got) != 400 {
		t.Fatalf("not found=%v", got)
	}
	for _, source := range []error{&drivermysql.MySQLError{Number: 1213, Message: "deadlock raw"}, &drivermysql.MySQLError{Number: 1205, Message: "timeout raw"}, errors.New("driver raw query detail")} {
		if got := mapRootAlertConfigLoadError(source); !errors.Is(got, source) {
			t.Fatalf("error replaced: %v", got)
		}
	}
}

func TestRootAlertTransactionRetrySuccessAndSafeExhaustion(t *testing.T) {
	for _, number := range []uint16{1213, 1205} {
		calls := 0
		err := runRootAlertTransaction(func() error {
			calls++
			if calls < 3 {
				return &drivermysql.MySQLError{Number: number, Message: "raw secret"}
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("number=%d calls=%d err=%v", number, calls, err)
		}
	}
	calls := 0
	raw := &drivermysql.MySQLError{Number: 1213, Message: "raw secret"}
	err := runRootAlertTransaction(func() error { calls++; return raw })
	if calls != 3 || status(mapRootAlertError(err)) != 503 || strings.Contains(mapRootAlertError(err).Error(), "raw secret") {
		t.Fatalf("calls=%d err=%v", calls, mapRootAlertError(err))
	}
}

func TestRootAlertPayloadRejectsMissingUnknownContradictoryEnumAndSecrets(t *testing.T) {
	base := models.JSONMap{"model_key": "model-a", "price_component": "input", "current_price_usd_per_million_tokens": "1.25", "upstream_price_usd_per_million_tokens": "2.50", "observed_at": int64(9)}
	bad := []models.JSONMap{}
	for _, remove := range []string{"model_key", "price_component", "current_price_usd_per_million_tokens", "upstream_price_usd_per_million_tokens", "observed_at"} {
		p := models.JSONMap{}
		for k, v := range base {
			if k != remove {
				p[k] = v
			}
		}
		bad = append(bad, p)
	}
	for _, extra := range []models.JSONMap{{"provider": "AKIAIOSFODNN7EXAMPLE"}, {"provider": "ghp_abcdefghijklmnopqrstuvwxyz123456"}, {"provider": "sk_live_placeholder"}, {"provider": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature"}, {"wrapper": map[string]any{"body": "secret"}}, {"array": []any{"secret"}}} {
		p := models.JSONMap{}
		for k, v := range base {
			p[k] = v
		}
		for k, v := range extra {
			p[k] = v
		}
		bad = append(bad, p)
	}
	for _, change := range []any{"both", "INPUT", map[string]any{"body": "secret"}} {
		p := models.JSONMap{}
		for k, v := range base {
			p[k] = v
		}
		p["price_component"] = change
		bad = append(bad, p)
	}
	for _, credential := range []string{"ghp_abcdefghijklmnopqrstuvwxyz123456", "sk_live_placeholder", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature"} {
		p := models.JSONMap{}
		for k, v := range base {
			p[k] = v
		}
		p["price_component"] = "input"
		p["model_key"] = "model-a"
		p["current_price_usd_per_million_tokens"] = "1"
		p["upstream_price_usd_per_million_tokens"] = "2"
		p["observed_at"] = int64(9)
		p["unexpected"] = credential
		bad = append(bad, p)
		c := models.JSONMap{"error_code": credential, "observed_at": int64(9)}
		if got, err := projectRootAlertPayload(models.RootAlertTypeCatalogSyncFailure, "", c); err == nil || got != nil {
			t.Fatalf("credential accepted %q", credential)
		}
	}
	contradict := models.JSONMap{}
	for k, v := range base {
		contradict[k] = v
	}
	contradict["model_key"] = "model-b"
	bad = append(bad, contradict)
	for i, p := range bad {
		if got, err := projectRootAlertPayload(models.RootAlertTypePublishedPriceBelowUpstream, "model-a", p); err == nil || got != nil {
			t.Fatalf("case %d accepted %#v", i, got)
		}
	}
	if got, err := projectRootAlertPayload(models.RootAlertTypeCatalogSyncFailure, "", models.JSONMap{}); err == nil || got != nil {
		t.Fatal("empty payload accepted")
	}
}

func TestRootAlertPayloadCanonicalEncodingIsDeterministic(t *testing.T) {
	a, _ := projectRootAlertPayload(models.RootAlertTypeCatalogSyncFailure, "", models.JSONMap{"observed_at": int64(9), "error_code": "timeout"})
	b, _ := projectRootAlertPayload(models.RootAlertTypeCatalogSyncFailure, "", models.JSONMap{"error_code": "timeout", "observed_at": int64(9)})
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	if string(aj) != string(bj) {
		t.Fatalf("canonical mismatch %s %s", aj, bj)
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
