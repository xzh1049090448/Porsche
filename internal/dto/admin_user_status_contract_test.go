package dto

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

const adminUserStatusContractPath = "../../docs/agents/contracts/admin-user-status-v1.json"

func TestAdminUserStatusContractMatchesFrozenA06Boundary(t *testing.T) {
	contents, err := os.ReadFile(adminUserStatusContractPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateContractTokens(contents); err != nil {
		t.Fatalf("contract token stream: %v", err)
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}

	assertContractKeys(t, document, "contract", "revision", "status", "design_source", "endpoint", "credential_semantics", "audit", "legacy_put", "scope")
	assertContractValue(t, document, "admin-user-status-v1", "contract")
	assertContractValue(t, document, "2026-09-08-a06-v1", "revision")
	assertContractValue(t, document, "AGREED_FOR_IMPLEMENTATION", "status")
	assertContractValue(t, document, "docs/superpowers/specs/2026-09-08-a06-managed-user-status-design.md", "design_source")

	endpoint := contractAt(t, document, "endpoint").(map[string]any)
	assertContractKeys(t, endpoint, "method", "path", "path_guid", "headers", "body_limit_bytes", "strict_json", "body", "transitions", "response", "errors", "retry")
	assertContractValue(t, document, "PATCH", "endpoint", "method")
	assertContractValue(t, document, "/admin/v2/users/{guid}/status", "endpoint", "path")
	assertContractValue(t, document, "canonical positive decimal signed int64 string (1..9223372036854775807), no sign, whitespace, or leading zero", "endpoint", "path_guid")
	assertContractValue(t, document, map[string]any{
		"Authorization": "required Bearer authentication under the existing mechanism",
		"Content-Type":  "application/json",
	}, "endpoint", "headers")
	assertContractValue(t, document, json.Number("4096"), "endpoint", "body_limit_bytes")
	assertContractValue(t, document, "one JSON object; reject duplicate keys, case-folded duplicate keys, invalid UTF-8, trailing values, unknown fields, and oversized bodies before service invocation", "endpoint", "strict_json")

	assertContractKeys(t, contractAt(t, document, "endpoint", "body").(map[string]any), "schema")
	schema := contractAt(t, document, "endpoint", "body", "schema").(map[string]any)
	assertContractKeys(t, schema, "type", "additionalProperties", "required", "properties")
	assertContractValue(t, document, "object", "endpoint", "body", "schema", "type")
	assertContractValue(t, document, false, "endpoint", "body", "schema", "additionalProperties")
	assertContractValue(t, document, []any{"status", "reason", "expected_auth_version"}, "endpoint", "body", "schema", "required")
	assertContractValue(t, document, map[string]any{
		"status":                map[string]any{"type": "string", "enum": []any{"active", "disabled"}},
		"reason":                map[string]any{"type": []any{"string", "null"}, "rule": "disabled requires a trimmed 1..200 Unicode code-point string; active requires null"},
		"expected_auth_version": map[string]any{"type": "integer", "rule": "positive INT32 (1..2147483647)"},
	}, "endpoint", "body", "schema", "properties")

	assertContractValue(t, document, map[string]any{
		"active_to_disabled": map[string]any{"capability": "users.disable", "auth_version_delta": json.Number("1"), "revoke_all_sessions": true},
		"disabled_to_active": map[string]any{"capability": "users.enable", "auth_version_delta": json.Number("1"), "revoke_legacy_active_sessions": true, "restore_sessions": false},
		"same_state":         map[string]any{"status": json.Number("409"), "code": "user_status_conflict"},
	}, "endpoint", "transitions")
	assertContractValue(t, document, map[string]any{
		"status": json.Number("200"), "dto": "UserReadDTO",
		"dto_keys": []any{"guid", "username", "nickname", "email", "group", "plan_type", "role", "status", "auth_version", "created_at", "last_login_at"},
		"headers":  map[string]any{"Cache-Control": "no-store", "X-Request-ID": "required non-empty"},
	}, "endpoint", "response")
	assertContractValue(t, document, map[string]any{
		"400": []any{"invalid_admin_user_status_request"}, "401": []any{"authentication_invalid"},
		"403": []any{"user_status_forbidden"}, "404": []any{"user_not_found"},
		"409": []any{"auth_version_conflict", "user_status_conflict"}, "413": []any{"request_body_too_large"},
		"503": []any{"user_status_dependency_unavailable"},
	}, "endpoint", "errors")
	assertContractValue(t, document, map[string]any{"patch": "never", "conflict_refresh_get_max": json.Number("1")}, "endpoint", "retry")

	assertContractValue(t, document, map[string]any{
		"disable": "old Access and Refresh sessions fail; Gateway rejects every Key while owner is disabled",
		"enable":  "any legacy active session is revoked before activation and old sessions remain revoked; independently revoked or expired Keys remain unusable; otherwise Gateway evaluates current owner state and current Key state",
	}, "credential_semantics")
	assertContractValue(t, document, map[string]any{
		"authentication": "credential-free security event",
		"management":     "one same-transaction audit_logs row containing normalized reason only for disable plus target_guid and before_status/after_status",
		"forbidden":      []any{"password", "token", "cookie", "session_sid", "authorization_header", "transport_error"},
	}, "audit")
	assertContractValue(t, document, map[string]any{
		"path":         "/admin/users/{guid}",
		"status_field": "retired and rejected before every write, including mixed bodies",
		"other_fields": "unchanged and outside A06",
	}, "legacy_put")
	assertContractValue(t, document, map[string]any{
		"included": []any{"backend", "frontend", "local MySQL 8", "local Redis 7", "visible browser"},
		"excluded": []any{"production migration", "deployment", "production acceptance", "real business accounts", "password reset", "role or permission change", "soft delete"},
	}, "scope")
}

func TestAdminUserStatusContractRejectsDuplicateAndTrailingRoots(t *testing.T) {
	contents, err := os.ReadFile(adminUserStatusContractPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := bytes.Replace(contents, []byte(`  "contract": "admin-user-status-v1",`), []byte("  \"contract\": \"admin-user-status-v1\",\n  \"contract\": \"admin-user-status-v1\","), 1)
	if bytes.Equal(duplicate, contents) || validateContractTokens(duplicate) == nil {
		t.Fatal("duplicate root key was accepted")
	}
	if validateContractTokens(append(append([]byte(nil), contents...), []byte("\n{}")...)) == nil {
		t.Fatal("trailing root object was accepted")
	}
}
