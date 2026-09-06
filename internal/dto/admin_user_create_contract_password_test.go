package dto_test

import (
	"bytes"
	"encoding/json"
	"testing"

	contracts "github.com/porsche/ai-gateway-go/docs/agents/contracts"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func TestAdminUserCreateContractExamplePasswordsMatchServiceValidation(t *testing.T) {
	var document map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(contracts.AdminUserCreateV1))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}

	create := adminUserCreatePasswordObject(t, document, "create")
	body := adminUserCreatePasswordObject(t, create, "request", "body")
	passwordSchema := adminUserCreatePasswordObject(t, body, "schema", "properties", "password")
	var rule string
	if err := json.Unmarshal(passwordSchema["rule"], &rule); err != nil {
		t.Fatal(err)
	}
	if rule != "required; preserve bytes without trim; existing 8-20 Unicode character and weak-password rules" {
		t.Fatal("password schema no longer declares the approved 8-20 Unicode rule")
	}

	examples := adminUserCreatePasswordObject(t, body, "examples")
	for _, name := range []string{"user", "admin"} {
		var example struct {
			Password string `json:"password"`
		}
		if err := json.Unmarshal(examples[name], &example); err != nil {
			t.Fatalf("decode %s password example: %v", name, err)
		}
		if length := len([]rune(example.Password)); length < 8 || length > 20 {
			t.Fatalf("%s example password violates the declared Unicode length constraint", name)
		}
		if err := service.ValidatePassword(example.Password); err != nil {
			t.Fatalf("%s example password is rejected by service validation", name)
		}
	}
}

func adminUserCreatePasswordObject(t *testing.T, source map[string]json.RawMessage, keys ...string) map[string]json.RawMessage {
	t.Helper()
	object := source
	for _, key := range keys {
		raw, ok := object[key]
		if !ok {
			t.Fatalf("missing contract key %q", key)
		}
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil {
			t.Fatalf("decode contract object %q: %v", key, err)
		}
		object = next
	}
	return object
}
