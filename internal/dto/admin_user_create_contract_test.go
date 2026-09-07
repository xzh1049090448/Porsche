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
	if err := validateContractTokens(contents); err != nil {
		t.Fatalf("contract token stream: %v", err)
	}

	var document map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	adminUserCreateRawExactKeys(t, document, "contract", "revision", "status", "design_source", "create", "action_ticket", "idempotency", "operation_query", "active_group_directory", "transaction", "errors", "frontend_security", "execution_order", "audit_outbox")
	adminUserCreateRawValue(t, document, "2026-09-07-a03-v3", "revision")

	create := adminUserCreateRawObject(t, document, "create")
	adminUserCreateRawValue(t, create, "POST", "method")
	adminUserCreateRawValue(t, create, "/admin/v2/users", "path")
	response := adminUserCreateRawObject(t, create, "response")
	adminUserCreateRawExactKeys(t, response, "status", "headers", "schema", "examples", "never_return")
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
		"password":             "Ex4mple!Pass1",
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
		"password":   "Ex4mple!Pass1",
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
	integrity := adminUserCreateRawObject(t, document, "idempotency", "response_integrity")
	adminUserCreateRawExactKeys(t, integrity, "version", "key", "binding", "target_binding", "expiry_rule", "deletion_lookup", "migration", "key_rotation", "tamper_rule")
	adminUserCreateRawValue(t, integrity, json.Number("1"), "version")
	adminUserCreateRawValue(t, integrity, []any{"integrity_version", "lifecycle", "operation_id", "operation_ref", "action", "terminal_state", "result_kind", "result_guid", "http_status", "media_type", "response_body"}, "binding")
	adminUserCreateRawValue(t, integrity, "admin_operation_responses.target_guid is a durable positive target for every new active snapshot; HMAC v1 binds that same value in its backward-compatible result_guid slot", "target_binding")
	adminUserCreateRawValue(t, integrity, "operation expiry may clear admin_operations.result_guid, but the durable response target remains available for privacy redaction; expired create requests return the stable operation-expired 410 and never expose the stored success body", "expiry_rule")
	adminUserCreateRawValue(t, integrity, "A14 locks every target_guid snapshot and every candidate successful create marker before the user write; post-0010 active and current-key redacted snapshots require an exact marker, operation binding, digest, and HMAC. An exact canonical pre-0010 zero-HMAC sentinel is already PII-free and may be accepted without a matching marker, or re-HMACed by an exact guarded update when the marker is valid, so a misbound marker cannot block an unrelated user. An exact valid marker with a missing snapshot and any active snapshot without its exact marker still fail closed; zero valid markers and zero snapshots means no create snapshot exists", "deletion_lookup")
	adminUserCreateRawValue(t, integrity, "0010 treats every pre-0010 response snapshot as unverifiable because SQL cannot authenticate the runtime-key HMAC: after optional strict diagnostic target backfill from an exact live create result or exact expired-operation successful ResultUser marker, every active and redacted row is atomically normalized to fixed 201 application/json, canonical {}, its fixed digest, is_deleted=1, and a zero-HMAC sentinel. Mismatched or unresolved targets remain null and no pre-0010 PII or replayable 201 body survives before the final outcome/lifecycle CHECK, target index, and foreign key; only post-0010 writes may create active real-HMAC snapshots", "migration")
	adminUserCreateRawValue(t, integrity, "rotation is prohibited while post-0010 response snapshots that may require replay or current-key delete redaction remain active unless a separately approved key-id, multi-key verification, or transactional re-HMAC migration is deployed; canonical pre-0010 zero-HMAC sentinels are already PII-free and do not require an old key, while any unsupported active-snapshot rotation fails closed", "key_rotation")

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
		map[string]any{"status": json.Number("410"), "codes": []any{"operation_expired", "created_user_deleted"}, "rule": "operation_expired means the create operation retention ended and exposes no snapshot body; created_user_deleted means a retained successful target was soft-deleted and returns no original target fields or response body"},
		map[string]any{"status": json.Number("422"), "codes": []any{"action_inactive"}, "rule": "only action not activated server-side or deployment version mismatch"},
		map[string]any{"status": json.Number("429"), "codes": []any{"action_rate_limited"}, "rule": "include Retry-After as integer seconds"},
		map[string]any{"status": json.Number("503"), "codes": []any{"action_dependency_unavailable", "operation_commit_unknown"}, "rule": "database, Redis, permission, or commit state cannot be safely determined"},
	}, "matrix")
	adminUserCreateRawValue(t, body, []any{"allowed_models", "daily_call_limit", "status", "auth_version", "amount", "balance", "internal_id"}, "prohibited_public_fields")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, response, "schema"), "object", "type")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, response, "schema"), false, "additionalProperties")
	adminUserCreateRawValue(t, adminUserCreateRawObject(t, response, "schema"), []any{"operation_ref", "user", "permissions_version"}, "required")
	adminUserCreateRawExactKeys(t, adminUserCreateRawObject(t, response, "schema", "properties"), "operation_ref", "user", "permissions_version")
	userSchema := adminUserCreateRawObject(t, response, "schema", "properties", "user")
	adminUserCreateRawValue(t, userSchema, "object", "type")
	adminUserCreateRawValue(t, userSchema, false, "additionalProperties")
	adminUserCreateRawValue(t, userSchema, []any{"guid", "username", "nickname", "email", "group", "plan_type", "role", "status", "auth_version", "created_at", "last_login_at"}, "required")
	adminUserCreateRawExactKeys(t, adminUserCreateRawObject(t, userSchema, "properties"), "guid", "username", "nickname", "email", "group", "plan_type", "role", "status", "auth_version", "created_at", "last_login_at")
	adminUserCreateRawValue(t, response, []any{"password", "password_hash", "internal_user_id", "action_ticket", "idempotency_key", "permission_override_primary_key"}, "never_return")

	responseExamples := adminUserCreateRawObject(t, response, "examples")
	adminUserCreateExactKeys(t, adminUserCreateExample(t, responseExamples, "user"), "operation_ref", "user", "permissions_version")
	adminUserCreateExactKeys(t, adminUserCreateExample(t, responseExamples, "admin"), "operation_ref", "user", "permissions_version")
	adminUserCreateResponseExample(t, adminUserCreateExample(t, responseExamples, "user"), "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "123456789012345678", "alice", "Alice", "user", nil)
	adminUserCreateResponseExample(t, adminUserCreateExample(t, responseExamples, "admin"), "op_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "123456789012345679", "admin-alice", "Admin Alice", "admin", "1")

	errors := adminUserCreateRawObject(t, document, "errors")
	adminUserCreateRawExactKeys(t, errors, "headers", "envelope", "matrix", "prohibited_disclosures")
	adminUserCreateRawValue(t, errors, []any{"password", "ticket", "idempotency key", "dependency raw text", "existing username owner's GUID", "existing username owner's status", "existing username owner's role", "existing username owner's deletion state"}, "prohibited_disclosures")
	errorEnvelope := adminUserCreateRawObject(t, errors, "envelope")
	adminUserCreateRawValue(t, errorEnvelope, "object", "type")
	adminUserCreateRawValue(t, errorEnvelope, false, "additionalProperties")
	adminUserCreateRawValue(t, errorEnvelope, []any{"error"}, "required")
	errorBody := adminUserCreateRawObject(t, errorEnvelope, "properties", "error")
	adminUserCreateRawValue(t, errorBody, false, "additionalProperties")
	adminUserCreateRawValue(t, errorBody, []any{"code", "message", "type", "request_id"}, "required")
	adminUserCreateRawExactKeys(t, adminUserCreateRawObject(t, errorBody, "properties"), "code", "message", "type", "request_id", "operation_ref")

	operationResponseSchema := adminUserCreateRawObject(t, operationQuery, "response", "schema")
	adminUserCreateRawExactKeys(t, adminUserCreateRawObject(t, operationQuery, "response"), "status", "headers", "schema")
	adminUserCreateRawValue(t, operationResponseSchema, false, "additionalProperties")
	adminUserCreateRawValue(t, operationResponseSchema, []any{"operation_ref", "scope", "status", "finished_at", "failure_code"}, "required")
	adminUserCreateRawExactKeys(t, adminUserCreateRawObject(t, operationResponseSchema, "properties"), "operation_ref", "scope", "status", "finished_at", "failure_code")
	groupResponseSchema := adminUserCreateRawObject(t, groups, "response", "schema")
	adminUserCreateRawExactKeys(t, adminUserCreateRawObject(t, groups, "response"), "status", "headers", "schema", "result_rule")
	adminUserCreateRawValue(t, groupResponseSchema, false, "additionalProperties")
	adminUserCreateRawValue(t, groupResponseSchema, []any{"items"}, "required")
	groupItemSchema := adminUserCreateRawObject(t, groupResponseSchema, "properties", "items", "items")
	adminUserCreateRawValue(t, groupItemSchema, false, "additionalProperties")
	adminUserCreateRawValue(t, groupItemSchema, []any{"guid", "key", "display_name"}, "required")
	adminUserCreateRawExactKeys(t, adminUserCreateRawObject(t, groupItemSchema, "properties"), "guid", "key", "display_name")
}

func TestAdminUserCreateFrozenRequestExamplesDecode(t *testing.T) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(contracts.AdminUserCreateV1, &document); err != nil {
		t.Fatal(err)
	}
	examples := adminUserCreateRawObject(t, adminUserCreateRawObject(t, adminUserCreateRawObject(t, document, "create"), "request", "body"), "examples")
	for _, name := range []string{"user", "admin"} {
		t.Run(name, func(t *testing.T) {
			request, err := DecodeAdminUserCreate(bytes.NewReader(examples[name]))
			if err != nil {
				t.Fatal(err)
			}
			defer request.ClearSecrets()
			if request.Password == nil {
				t.Fatal("frozen example lost password")
			}
			if name == "admin" {
				verificationBody := append([]byte(`{"action":"users.create_admin","intent":`), examples[name]...)
				verificationBody = append(verificationBody, []byte(`,"current_password":"Current!Pass1"}`)...)
				verification, err := DecodeAdminUserCreateVerification(bytes.NewReader(verificationBody))
				if err != nil {
					t.Fatal(err)
				}
				verification.ClearSecrets()
				clear(verificationBody)
			}
		})
	}
}

func TestAdminUserCreateContractRejectsDuplicateAndTrailingJSON(t *testing.T) {
	contents := contracts.AdminUserCreateV1
	mutations := []struct {
		name        string
		old         string
		replacement string
	}{
		{"duplicate root key", `  "contract": "admin-user-create-v1",`, "  \"contract\": \"admin-user-create-v1\",\n  \"contract\": \"admin-user-create-v1\","},
		{"duplicate nested key", `      "status": 201,`, "      \"status\": 201,\n      \"status\": 201,"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := bytes.Replace(contents, []byte(mutation.old), []byte(mutation.replacement), 1)
			if bytes.Equal(candidate, contents) {
				t.Fatal("mutation did not match the frozen contract")
			}
			if err := validateContractTokens(candidate); err == nil {
				t.Fatal("malformed contract was accepted")
			}
		})
	}
	if err := validateContractTokens(append(append([]byte{}, contents...), []byte("\n{}")...)); err == nil {
		t.Fatal("trailing root JSON value was accepted")
	}
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

func adminUserCreateResponseExample(t *testing.T, example map[string]any, operationRef, guid, username, nickname, role string, permissionsVersion any) {
	t.Helper()
	if example["operation_ref"] != operationRef || example["permissions_version"] != permissionsVersion {
		t.Fatal("response example has the wrong operation or permissions version")
	}
	user, ok := example["user"].(map[string]any)
	if !ok {
		t.Fatal("response example user is not an object")
	}
	adminUserCreateExactKeys(t, user, "guid", "username", "nickname", "email", "group", "plan_type", "role", "status", "auth_version", "created_at", "last_login_at")
	if user["guid"] != guid || user["username"] != username || user["nickname"] != nickname || user["role"] != role || user["group"] != "default" || user["plan_type"] != "free" || user["status"] != "active" || user["auth_version"] != json.Number("1") || user["created_at"] != "2026-09-06T00:00:00.000Z" || user["email"] != nil || user["last_login_at"] != nil {
		t.Fatal("response example user fields differ from the frozen contract")
	}
}

func adminUserCreateRawExactKeys(t *testing.T, object map[string]json.RawMessage, want ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("raw object keys=%v, want %v", got, want)
	}
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
