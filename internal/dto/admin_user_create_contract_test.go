package dto

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

const adminUserCreateContractPath = "../../docs/agents/contracts/admin-user-create-v1.json"

func TestAdminUserCreateContractExamples(t *testing.T) {
	contents, err := os.ReadFile(adminUserCreateContractPath)
	if err != nil {
		t.Fatal(err)
	}

	var document map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}

	create := adminUserCreateRawObject(t, document, "create")
	adminUserCreateRawValue(t, create, "POST", "method")
	adminUserCreateRawValue(t, create, "/admin/v2/users", "path")
	response := adminUserCreateRawObject(t, create, "response")
	adminUserCreateRawValue(t, response, json.Number("201"), "status")

	request := adminUserCreateRawObject(t, create, "request")
	body := adminUserCreateRawObject(t, request, "body")
	examples := adminUserCreateRawObject(t, body, "examples")
	ordinary := adminUserCreateExample(t, examples, "user")
	adminUserCreateExactKeys(t, ordinary, "username", "nickname", "password", "role", "group_guid", "plan_type", "permission_overrides")
	if !reflect.DeepEqual(ordinary, map[string]any{
		"username":             "alice",
		"nickname":             "Alice",
		"password":             "example-only-not-a-secret",
		"role":                 "user",
		"group_guid":           nil,
		"plan_type":            "free",
		"permission_overrides": []any{},
	}) {
		t.Fatalf("ordinary request example=%#v", ordinary)
	}

	administrator := adminUserCreateExample(t, examples, "admin")
	adminUserCreateExactKeys(t, administrator, "username", "nickname", "password", "role", "group_guid", "plan_type", "permission_overrides")
	if !reflect.DeepEqual(administrator, map[string]any{
		"username":   "admin-alice",
		"nickname":   "Admin Alice",
		"password":   "example-only-not-a-secret",
		"role":       "admin",
		"group_guid": nil,
		"plan_type":  "free",
		"permission_overrides": []any{
			map[string]any{"capability": "users.sessions.read", "effect": "allow"},
			map[string]any{"capability": "users.plan.change", "effect": "deny"},
		},
	}) {
		t.Fatalf("administrator request example=%#v", administrator)
	}
}

func adminUserCreateRawObject(t *testing.T, source map[string]json.RawMessage, key string) map[string]json.RawMessage {
	t.Helper()
	raw, ok := source[key]
	if !ok {
		t.Fatalf("missing contract key %q", key)
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		t.Fatalf("decode contract object %q: %v", key, err)
	}
	return object
}

func adminUserCreateRawValue(t *testing.T, source map[string]json.RawMessage, want any, key string) {
	t.Helper()
	raw, ok := source[key]
	if !ok {
		t.Fatalf("missing contract key %q", key)
	}
	var got any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("decode contract value %q: %v", key, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contract value %q=%#v, want %#v", key, got, want)
	}
}

func adminUserCreateExample(t *testing.T, examples map[string]json.RawMessage, key string) map[string]any {
	t.Helper()
	raw, ok := examples[key]
	if !ok {
		t.Fatalf("missing request example %q", key)
	}
	var example map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&example); err != nil {
		t.Fatalf("decode request example %q: %v", key, err)
	}
	return example
}

func adminUserCreateExactKeys(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("object keys=%v, want %v", got, want)
	}
}
