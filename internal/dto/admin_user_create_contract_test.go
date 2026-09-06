package dto

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	contracts "github.com/porsche/ai-gateway-go/docs/agents/contracts"
)

func TestAdminUserCreateContractExamples(t *testing.T) {
	contents := contracts.AdminUserCreateV1

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
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, create, "request", "headers"), "required exactly once; unique original value; format and secret handling follow the admin action foundation", "Idempotency-Key")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, create, "request", "headers", "X-Action-Ticket"), "forbidden", "role=user")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, create, "request", "headers", "X-Action-Ticket"), "required exactly once; valid users.create_admin ticket bound to the complete intent", "role=admin")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, response, "headers"), "no-store", "Cache-Control")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, response, "headers"), "required non-empty", "X-Request-ID")

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

	operationQuery := adminUserCreateRawObject(t, document, "operation_query")
	adminUserCreateRawValue(t, operationQuery, "GET", "method")
	adminUserCreateRawValue(t, operationQuery, "/admin/v2/operations", "path")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, operationQuery, "request", "headers"), "required exactly once; unique original value", "Idempotency-Key")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, operationQuery, "request", "headers"), "forbidden", "X-Action-Ticket")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, operationQuery, "request", "query_schema", "properties", "scope"), []any{"users.create", "users.create_admin"}, "enum")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, document, "idempotency"), []any{"users.create", "users.create_admin"}, "operation_scopes")

	groups := adminUserCreateRawObject(t, document, "active_group_directory")
	adminUserCreateRawValue(t, groups, "GET", "method")
	adminUserCreateRawValue(t, groups, "/admin/v2/groups?status=active", "path")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, groups, "request"), "accept exactly one status=active and no other query parameters", "query_rule")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, groups, "response", "headers"), "no-store", "Cache-Control")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, groups, "response", "headers"), "required non-empty", "X-Request-ID")

	adminUserCreateRawValue(t, adminUserCreateRawObject(t, document, "errors"), []any{
		map[string]any{"status": json.Number("400"), "codes": []any{"invalid_admin_user_create_request"}, "rule": "invalid JSON or fields, username, nickname, password, role, group GUID, plan, override format, amount, or another unknown field"},
		map[string]any{"status": json.Number("401"), "codes": []any{}, "rule": "existing authentication middleware detail shape"},
		map[string]any{"status": json.Number("403"), "codes": []any{"action_operation_rejected"}, "rule": "role hierarchy, users.create, or additional capability denied; do not disclose hidden resources"},
		map[string]any{"status": json.Number("404"), "codes": []any{"action_group_not_found"}, "rule": "explicit group missing, inactive, or invisible; use one safe response"},
		map[string]any{"status": json.Number("409"), "codes": []any{"username_conflict", "idempotency_conflict", "idempotency_cross_session", "action_rejected", "action_verification_conflict", "policy_version_conflict", "target_state_conflict", "consumer_validation_failed"}, "rule": "username conflict reveals only the conflict fact; ticket, actor/session, and policy drift retain existing action codes"},
		map[string]any{"status": json.Number("422"), "codes": []any{"action_inactive"}, "rule": "only action not activated server-side or deployment version mismatch"},
		map[string]any{"status": json.Number("429"), "codes": []any{"action_rate_limited"}, "rule": "include Retry-After as integer seconds"},
		map[string]any{"status": json.Number("503"), "codes": []any{"action_dependency_unavailable", "operation_commit_unknown"}, "rule": "database, Redis, permission, or commit state cannot be safely determined"},
	}, "matrix")
}

func adminUserCreateRawObject(t *testing.T, source map[string]json.RawMessage, keys ...string) map[string]json.RawMessage {
	t.Helper()
	object := source
	for _, key := range keys {
		raw, ok := object[key]
		if !ok {
			t.Fatalf("missing contract key %q", key)
		}
		var next map[string]json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&next); err != nil {
			t.Fatalf("decode contract object %q: %v", key, err)
		}
		object = next
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
