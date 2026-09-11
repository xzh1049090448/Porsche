package service

import (
	"encoding/json"
	"errors"
	"fmt"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/models"
	"strings"
	"testing"
	"time"
)

func TestProjectPublicModelSerializesEmptyCollectionsAsArrays(t *testing.T) {
	projected := projectPublicModel(models.PublicModelConfig{})
	raw, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"capabilities", "endpoint_types", "public_restrictions"} {
		if string(body[field]) != "[]" {
			t.Errorf("%s = %s, want []", field, body[field])
		}
	}
}

func TestPublicModelInputValidation(t *testing.T) {
	if err := validatePublicModelCreate(CreatePublicModelRequest{}); err == nil {
		t.Fatal("accepted empty model")
	}
	bad := "1.123456789"
	if err := validatePublicModelCreate(CreatePublicModelRequest{UpstreamModelID: "u", ModelKey: "k", DisplayName: "n", Provider: "p", Capabilities: []string{}, ContextWindow: 1, InputPriceUSDPerMillionTokens: &bad}); err == nil {
		t.Fatal("accepted excess precision")
	}
}

func TestPublicModelObservationFreshnessBoundary(t *testing.T) {
	now := int64(1_900_000_000_000)
	if !publicModelObservationIsCurrent(now, now-publicModelObservationFreshnessMillis) {
		t.Fatal("boundary rejected")
	}
	if publicModelObservationIsCurrent(now, now-publicModelObservationFreshnessMillis-1) {
		t.Fatal("stale accepted")
	}
	if publicModelObservationIsCurrent(now, now+int64(time.Second/time.Millisecond)) {
		t.Fatal("future accepted")
	}
}
func TestPublicModelMapsIdentityUniqueRaceToStableConflict(t *testing.T) {
	err := mapPublicModelWriteError(&drivermysql.MySQLError{Number: 1062, Message: "Duplicate entry secret DSN"})
	if statusCode, _ := StatusFromError(err); statusCode != 409 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("mapped=%v", err)
	}
	sentinel := errors.New("storage")
	if mapPublicModelWriteError(sentinel) != sentinel {
		t.Fatal("non unique changed")
	}
}
func TestPublicModelValidatesDatabaseBounds(t *testing.T) {
	base := CreatePublicModelRequest{UpstreamModelID: "org/model", ModelKey: "model", DisplayName: "n", Provider: "p", Capabilities: []string{"chat"}, ContextWindow: 1}
	for _, mutate := range []func(*CreatePublicModelRequest){func(v *CreatePublicModelRequest) { v.DisplayName = strings.Repeat("x", 129) }, func(v *CreatePublicModelRequest) { v.Provider = strings.Repeat("x", 129) }, func(v *CreatePublicModelRequest) { v.Capabilities = []string{"bad capability"} }, func(v *CreatePublicModelRequest) { v.Capabilities = []string{strings.Repeat("x", 65)} }, func(v *CreatePublicModelRequest) { v.Capabilities = make([]string, 33) }} {
		v := base
		mutate(&v)
		if validatePublicModelCreate(v) == nil {
			t.Fatalf("accepted %#v", v)
		}
	}
}

func TestPublicModelInactiveReasonDatabaseBounds(t *testing.T) {
	for _, reason := range []string{"", strings.Repeat("x", 129), "line\nbreak", " padded "} {
		if validPublicModelText(reason, 128) {
			t.Fatalf("accepted reason %q", reason)
		}
	}
	if !validPublicModelText(strings.Repeat("界", 128), 128) {
		t.Fatal("rejected varchar boundary")
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
