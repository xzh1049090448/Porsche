package service

import (
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
