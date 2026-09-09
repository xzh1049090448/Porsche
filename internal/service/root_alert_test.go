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
	p := sanitizeRootAlertPayload(models.JSONMap{"model_key": "safe", "authorization": "Bearer secret", "nested": map[string]any{"api_key": "secret", "reason": strings.Repeat("x", 900)}})
	b, err := json.Marshal(p)
	if err != nil || len(b) > rootAlertPayloadLimit || strings.Contains(strings.ToLower(string(b)), "secret") || strings.Contains(string(b), strings.Repeat("x", 600)) {
		t.Fatalf("unsafe payload %s err=%v", b, err)
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
