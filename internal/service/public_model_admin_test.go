package service

import (
	"fmt"
	"strings"
	"testing"
)

func TestPublicModelInputValidation(t *testing.T) {
	if err := validatePublicModelCreate(CreatePublicModelRequest{}); err == nil {
		t.Fatal("accepted empty model")
	}
	bad := "1.123456789"
	if err := validatePublicModelCreate(CreatePublicModelRequest{UpstreamModelID: "u", ModelKey: "k", DisplayName: "n", Provider: "p", Capabilities: []string{}, ContextWindow: 1, InputPriceUSDPerMillionTokens: &bad}); err == nil {
		t.Fatal("accepted excess precision")
	}
}

func TestPublicModelAuditDetailIsSanitized(t *testing.T) {
	d := publicModelAuditDetail("key", 2, "reason")
	if len(d) != 3 || d["model_key"] != "key" || d["revision"] != int64(2) {
		t.Fatalf("detail=%#v", d)
	}
}

func TestPublicModelUsesSharedIdentityValidators(t *testing.T) {
	base := CreatePublicModelRequest{UpstreamModelID: "org/model.v1", ModelKey: "model-key", DisplayName: "n", Provider: "p", Capabilities: []string{}, ContextWindow: 1}
	if err := validatePublicModelCreate(base); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"1model", "model_key", "model--key", "model-"} {
		bad := base
		bad.ModelKey = key
		if validatePublicModelCreate(bad) == nil {
			t.Fatalf("accepted key %q", key)
		}
	}
	for _, id := range []string{"org//model", "org/../model", "org/model?secret=1", strings.Repeat("x", 256)} {
		bad := base
		bad.UpstreamModelID = id
		if validatePublicModelCreate(bad) == nil {
			t.Fatalf("accepted upstream id %q", id)
		}
	}
}

func TestPublicModelAuditReasonRedactsSecretBearingInput(t *testing.T) {
	for _, hostile := range []string{"api_key=sk-live", "Password: hunter2", "Bearer token-value", "Authorization abc", "client_secret=x", "credential value", "raw payload {secret}"} {
		d := publicModelAuditDetail("key", 2, hostile)
		encoded := strings.ToLower(fmt.Sprint(d))
		for _, leak := range []string{"sk-live", "hunter2", "token-value", " abc", "=x", "credential value", "{secret}"} {
			if strings.Contains(encoded, leak) {
				t.Fatalf("leaked %q in %s", leak, encoded)
			}
		}
	}
}
