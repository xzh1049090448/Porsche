package dto

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

const adminUserRolesPermissionsContractPath = "../../docs/agents/contracts/admin-user-roles-permissions-v1.json"

func TestA08ContractHasExactScopesAndStableResult(t *testing.T) {
	contents, err := os.ReadFile(adminUserRolesPermissionsContractPath)
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

	assertContractKeys(t, document, "contract", "revision", "status", "design_source", "paired_frontend_contract", "common", "actions", "stable_result", "operation_query", "errors", "frontend_security", "implementation_boundary")
	assertContractValue(t, document, "admin-user-roles-permissions-v1", "contract")
	assertContractValue(t, document, "2026-09-09-a08-v1", "revision")
	assertContractValue(t, document, "AGREED_FOR_IMPLEMENTATION", "status")
	assertContractValue(t, document, "docs/superpowers/specs/2026-09-09-a08-managed-user-roles-permissions-design.md", "design_source")
	assertContractValue(t, document, map[string]any{
		"path":          "../Porsche-Web/docs/agents/contracts/admin-user-roles-permissions-v1.json",
		"equality_rule": "frontend copy must be byte-identical to this authoritative backend contract",
		"verification":  "compare raw bytes before joint acceptance",
	}, "paired_frontend_contract")

	assertContractValue(t, document, json.Number("4096"), "common", "body_limit_bytes")
	assertContractValue(t, document, "one exact JSON object; reject unknown, duplicate, case-folded duplicate, invalid UTF-8, trailing fields, and oversized bodies before service invocation", "common", "strict_json")
	assertContractValue(t, document, map[string]any{"Cache-Control": "no-store", "X-Request-ID": "required non-empty; error body request_id equals this header"}, "common", "response_headers")
	assertContractValue(t, document, "never automatically replay Issue, POST execute, or PATCH execute; a 409 allows at most one owned refresh and requires a new user gesture", "common", "mutation_replay")

	actions := contractAt(t, document, "actions").(map[string]any)
	assertContractKeys(t, actions, "users.promote", "users.demote", "users.permissions.write")
	for _, tc := range []struct {
		scope, method, path, executeAction string
		issueKeys, executeKeys             []any
	}{
		{"users.promote", "POST", "/admin/v2/users/{guid}/actions", "promote", []any{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []any{"action", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}},
		{"users.demote", "POST", "/admin/v2/users/{guid}/actions", "demote", []any{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "reason"}, []any{"action", "expected_auth_version", "expected_permissions_version", "catalog_version", "reason"}},
		{"users.permissions.write", "PATCH", "/admin/v2/users/{guid}/permissions", "field absent; method and path select users.permissions.write", []any{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []any{"expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			action := actions[tc.scope].(map[string]any)
			assertContractKeys(t, action, "scope", "issue", "execute", "query")
			assertContractValue(t, action, tc.scope, "scope")
			assertContractValue(t, action, "POST", "issue", "method")
			assertContractValue(t, action, "/admin/v2/action-verifications", "issue", "path")
			assertContractValue(t, action, []any{"action", "intent", "current_password"}, "issue", "body", "required")
			assertContractValue(t, action, []any{"action", "intent", "current_password"}, "issue", "body", "allowed")
			assertContractValue(t, action, false, "issue", "body", "additionalProperties")
			assertContractValue(t, action, tc.issueKeys, "issue", "body", "intent_required")
			assertContractValue(t, action, tc.issueKeys, "issue", "body", "intent_allowed")
			assertContractValue(t, action, []any{"Idempotency-Key", "X-Action-Ticket"}, "issue", "forbidden_headers")
			assertContractValue(t, action, json.Number("201"), "issue", "response_status")
			assertContractValue(t, action, []any{"ticket", "expires_at"}, "issue", "response_keys")

			assertContractValue(t, action, tc.method, "execute", "method")
			assertContractValue(t, action, tc.path, "execute", "path")
			assertContractValue(t, action, []any{"Idempotency-Key", "X-Action-Ticket"}, "execute", "required_headers")
			assertContractValue(t, action, tc.executeKeys, "execute", "body", "required")
			assertContractValue(t, action, tc.executeKeys, "execute", "body", "allowed")
			assertContractValue(t, action, false, "execute", "body", "additionalProperties")
			assertContractValue(t, action, tc.executeAction, "execute", "body", "action_literal")
			assertContractValue(t, action, json.Number("200"), "execute", "response_status")
			assertContractValue(t, action, []any{"operation_ref", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role"}, "execute", "response_keys")
			assertContractValue(t, action, "committed terminal result only; operation_commit_unknown is an error carrying operation_ref", "execute", "success_rule")

			assertContractValue(t, action, "GET", "query", "method")
			assertContractValue(t, action, "/admin/v2/operations", "query", "path")
			assertContractValue(t, action, tc.scope, "query", "exact_scope")
			assertContractValue(t, action, []any{"Idempotency-Key"}, "query", "required_headers")
			assertContractValue(t, action, []any{"X-Action-Ticket"}, "query", "forbidden_headers")
			assertContractValue(t, action, "required exactly once using the original mutation key", "query", "original_key_rule")
			assertContractValue(t, action, json.Number("200"), "query", "response_status")
		})
	}

	stable := contractAt(t, document, "stable_result").(map[string]any)
	assertContractKeys(t, stable, "exact_keys", "schema", "role_by_scope", "storage_and_replay")
	assertContractValue(t, stable, []any{"operation_ref", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role"}, "exact_keys")
	assertContractKeys(t, contractAt(t, stable, "schema").(map[string]any), "operation_ref", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role")
	assertContractValue(t, stable, map[string]any{"users.promote": "admin", "users.demote": "user", "users.permissions.write": "admin"}, "role_by_scope")
	assertContractValue(t, stable, "store all five fields on the terminal operation and replay those stored values; never project the current user row", "storage_and_replay")

	assertContractValue(t, document, []any{"processing", "succeeded", "failed", "pending_recovery"}, "operation_query", "statuses")
	assertContractValue(t, document, []any{"operation_ref", "scope", "status", "finished_at", "failure_code", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role"}, "operation_query", "response_keys")
	assertContractValue(t, document, "target_guid, resulting_auth_version, resulting_permissions_version, and resulting_role are nullable; present only from the stored stable result for succeeded", "operation_query", "nullable_result_rule")
	assertContractValue(t, document, "processing only; integer seconds 1..30", "operation_query", "retry_after")

	assertContractValue(t, document, "existing authentication middleware body with exact {detail}; never wrap as admin_action_error", "errors", "authentication_401")
	assertContractValue(t, document, map[string]any{
		"400": []any{"invalid_admin_action_request"},
		"403": []any{"action_verification_rejected", "action_operation_rejected"},
		"404": []any{"action_target_not_found", "action_operation_not_found"},
		"409": []any{"action_verification_conflict", "idempotency_conflict", "idempotency_cross_session", "action_rejected", "target_version_conflict", "policy_version_conflict", "target_state_conflict", "consumer_validation_failed"},
		"410": []any{"operation_expired"},
		"413": []any{"request_body_too_large"},
		"422": []any{"action_inactive"},
		"429": []any{"action_rate_limited"},
		"503": []any{"action_dependency_unavailable", "operation_commit_unknown"},
	}, "errors", "status_code_allowlist")
	assertContractValue(t, document, []any{"action_rejected", "target_version_conflict", "policy_version_conflict", "target_state_conflict", "consumer_validation_failed"}, "errors", "operation_failure_codes")
	assertContractValue(t, document, map[string]any{
		"exact_body_keys": []any{"error"}, "required_error_keys": []any{"code", "message", "type", "request_id"}, "optional_error_key": "operation_ref only for operation_commit_unknown", "message": "请求无法完成", "type": "admin_action_error",
	}, "errors", "envelope")
	assertContractValue(t, document, []any{"ticket", "idempotency_key", "current_password", "password_material", "HMAC_input", "digest", "dependency_raw_text"}, "errors", "prohibited_fields")

	assertContractValue(t, document, []any{"current_password", "ticket", "idempotency_key", "unknown_state"}, "frontend_security", "memory_only")
	assertContractValue(t, document, []any{"current_password", "ticket", "idempotency_key", "password_material", "HMAC_input"}, "frontend_security", "prohibited_values")
	assertContractValue(t, document, []any{"responses", "audit_detail", "outbox_payload", "URL", "localStorage", "sessionStorage", "analytics", "ordinary_logs"}, "frontend_security", "secret_prohibitions")
	assertContractValue(t, document, json.Number("0"), "frontend_security", "mutation_auto_replay_count")
	assertContractValue(t, document, "contract only; no route or consumer is claimed active and no implementation or acceptance is claimed complete", "implementation_boundary")
}
