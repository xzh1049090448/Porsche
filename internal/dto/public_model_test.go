package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/porsche/ai-gateway-go/internal/service"
	"strings"
	"testing"
)

func TestPublicModelDTOUsesExactSafeFields(t *testing.T) {
	v := PublicModelAdmin{GUID: "42", ModelKey: "safe-key", Capabilities: []string{}, Status: "draft", Revision: 1}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, key := range []string{`"guid"`, `"model_key"`, `"upstream_model_id"`, `"input_price_usd_per_million_tokens"`, `"output_price_usd_per_million_tokens"`, `"last_upstream_check_at"`} {
		if !strings.Contains(got, key) {
			t.Fatalf("missing %s: %s", key, got)
		}
	}
	for _, forbidden := range []string{"credential", "raw_payload", "api_key"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("unsafe field %q", forbidden)
		}
	}
}

func TestPublicModelMutationDTOsKeepIdentityImmutable(t *testing.T) {
	typ := UpdatePublicModelRequest{ExpectedRevision: 1}
	b, _ := json.Marshal(typ)
	if strings.Contains(string(b), "model_key") || strings.Contains(string(b), "upstream_model_id") {
		t.Fatal(string(b))
	}
	if strings.Contains(string(b), "price_usd") {
		t.Fatalf("omitted nullable prices serialized: %s", b)
	}
}

func TestDecodeUpdatePublicModelDistinguishesOmittedValueAndNull(t *testing.T) {
	for _, tc := range []struct {
		body  string
		set   bool
		value *string
	}{
		{`{"expected_revision":1}`, false, nil},
		{`{"expected_revision":1,"input_price_usd_per_million_tokens":null}`, true, nil},
		{`{"expected_revision":1,"input_price_usd_per_million_tokens":"1.25000000"}`, true, stringRef("1.25000000")},
	} {
		got, err := DecodeUpdatePublicModelRequest(bytes.NewBufferString(tc.body))
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if got.InputPriceUSDPerMillionTokens.Set != tc.set || !sameString(got.InputPriceUSDPerMillionTokens.Value, tc.value) {
			t.Fatalf("%s: %#v", tc.body, got.InputPriceUSDPerMillionTokens)
		}
		var canonical service.UpdatePublicModelRequest = got
		if canonical.InputPriceUSDPerMillionTokens.Set != tc.set {
			t.Fatal("DTO/service presence drift")
		}
	}
}

func TestDecodeUpdatePublicModelRejectsUnknownDuplicateTrailingAndMissingRevision(t *testing.T) {
	for _, body := range []string{`{"expected_revision":1,"unknown":1}`, `{"expected_revision":1,"expected_revision":2}`, `{"expected_revision":1}{}`, `{}`} {
		if _, err := DecodeUpdatePublicModelRequest(bytes.NewBufferString(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestDecodeUpdatePublicModelBodyLimit(t *testing.T) {
	prefix := `{"expected_revision":1}`
	exact := prefix + strings.Repeat(" ", PublicModelRequestBodyLimit-len(prefix))
	if _, err := DecodeUpdatePublicModelRequest(strings.NewReader(exact)); err != nil {
		t.Fatalf("exact limit: %v", err)
	}
	for _, body := range []string{
		exact + " ",
		prefix + strings.Repeat(" ", PublicModelRequestBodyLimit-len(prefix)) + `{}`,
		prefix + strings.Repeat("x", PublicModelRequestBodyLimit-len(prefix)+1),
	} {
		if _, err := DecodeUpdatePublicModelRequest(strings.NewReader(body)); !errors.Is(err, ErrPublicModelRequestTooLarge) {
			t.Fatalf("oversize error=%v", err)
		}
		if err := func() error { _, e := DecodeUpdatePublicModelRequest(strings.NewReader(body)); return e }(); err == nil || strings.Contains(err.Error(), "expected_revision") {
			t.Fatalf("unstable or content-bearing error=%v", err)
		}
	}
}

func stringRef(v string) *string   { return &v }
func sameString(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
