package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
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

	assertContractKeys(t, a08ContractObject(t, document, "common"), "authentication", "path_guid", "body_limit_bytes", "strict_json", "reason", "versions", "overrides", "response_headers", "mutation_replay", "eligibility")
	assertContractValue(t, document, "existing Access Bearer authentication only; reject Refresh tokens as endpoint authentication; before every Issue, Execute, and Query authorization check require a fresh actor auth_version, current logical session and Redis revocation state, exact effective Root capability, target visibility, target state, target role, target auth version, policy version, and catalog version when applicable", "common", "authentication")
	assertContractValue(t, document, "canonical positive decimal signed int64 string without sign, whitespace, or leading zero", "common", "path_guid")
	assertContractValue(t, document, json.Number("4096"), "common", "body_limit_bytes")
	assertContractValue(t, document, "one exact JSON object; reject unknown, duplicate, case-folded duplicate, invalid UTF-8, trailing fields, and oversized bodies before service invocation", "common", "strict_json")
	assertContractValue(t, document, "Unicode-trimmed 1..200 code points", "common", "reason")
	assertContractKeys(t, a08ContractObject(t, document, "common", "versions"), "expected_auth_version", "expected_permissions_version", "catalog_version")
	assertContractValue(t, document, "integer 1..2147483647", "common", "versions", "expected_auth_version")
	assertContractValue(t, document, "integer 0..9223372036854775807; zero is allowed only for promote when no target policy head exists; demote and permissions.write require a positive current version", "common", "versions", "expected_permissions_version")
	assertContractValue(t, document, "integer 1..2147483647 matching the current capability catalog", "common", "versions", "catalog_version")
	assertContractValue(t, document, "complete array of unique capabilities, each item an exact object with only capability and effect; wire effect is allow or deny only; UI inherit is represented by omitting the capability; reject explicit inherit, unknown, unavailable, duplicate, case-folded duplicate, and allow on Root-only or ungrantable capabilities; persistence stores allow or deny only", "common", "overrides")
	assertContractValue(t, document, map[string]any{"Cache-Control": "no-store", "X-Request-ID": "required non-empty; error body request_id equals this header"}, "common", "response_headers")
	assertContractValue(t, document, "never automatically replay Issue, POST execute, or PATCH execute; a 409 allows at most one owned refresh and requires a new user gesture", "common", "mutation_replay")
	assertContractKeys(t, a08ContractObject(t, document, "common", "eligibility"), "actor", "promote_target", "demote_target", "permissions_target", "invisible_target", "visible_but_forbidden", "stale_or_same_state")
	assertContractValue(t, document, "active Root with a current nonrevoked logical session and the exact effective capability; Admin overrides cannot obtain these capabilities", "common", "eligibility", "actor")
	assertContractValue(t, document, "visible, nondeleted User in active or disabled state; never self or Root", "common", "eligibility", "promote_target")
	assertContractValue(t, document, "visible, nondeleted Admin in active or disabled state; never self or Root", "common", "eligibility", "demote_target")
	assertContractValue(t, document, "visible, nondeleted Admin in active or disabled state; never self or Root", "common", "eligibility", "permissions_target")
	assertContractValue(t, document, "404", "common", "eligibility", "invisible_target")
	assertContractValue(t, document, "403", "common", "eligibility", "visible_but_forbidden")
	assertContractValue(t, document, "409 with no security-version, session, policy, audit, outbox, or operation-result mutation", "common", "eligibility", "stale_or_same_state")

	actions := a08ContractObject(t, document, "actions")
	assertContractKeys(t, actions, "users.promote", "users.demote", "users.permissions.write")
	for _, tc := range []struct {
		scope, method, path, executeAction string
		issueKeys, executeKeys             []any
		issueBodyKeys, executeBodyKeys     []string
	}{
		{"users.promote", "POST", "/admin/v2/users/{guid}/actions", "promote", []any{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []any{"action", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []string{"type", "additionalProperties", "required", "allowed", "action_literal", "intent_additionalProperties", "intent_required", "intent_allowed", "current_password"}, []string{"type", "additionalProperties", "required", "allowed", "action_literal", "path_supplies_target_guid"}},
		{"users.demote", "POST", "/admin/v2/users/{guid}/actions", "demote", []any{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "reason"}, []any{"action", "expected_auth_version", "expected_permissions_version", "catalog_version", "reason"}, []string{"type", "additionalProperties", "required", "allowed", "action_literal", "intent_additionalProperties", "intent_required", "intent_allowed", "intent_forbidden", "current_password"}, []string{"type", "additionalProperties", "required", "allowed", "action_literal", "path_supplies_target_guid", "forbidden"}},
		{"users.permissions.write", "PATCH", "/admin/v2/users/{guid}/permissions", "field absent; method and path select users.permissions.write", []any{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []any{"expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []string{"type", "additionalProperties", "required", "allowed", "action_literal", "intent_additionalProperties", "intent_required", "intent_allowed", "current_password"}, []string{"type", "additionalProperties", "required", "allowed", "action_literal", "path_supplies_target_guid"}},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			action := a08ContractObject(t, document, "actions", tc.scope)
			assertContractKeys(t, action, "scope", "issue", "execute", "query")
			assertContractValue(t, action, tc.scope, "scope")
			assertContractKeys(t, a08ContractObject(t, document, "actions", tc.scope, "issue"), "method", "path", "forbidden_headers", "body", "response_status", "response_keys", "ticket_ttl_seconds")
			assertContractValue(t, action, "POST", "issue", "method")
			assertContractValue(t, action, "/admin/v2/action-verifications", "issue", "path")
			assertContractKeys(t, a08ContractObject(t, document, "actions", tc.scope, "issue", "body"), tc.issueBodyKeys...)
			assertContractValue(t, action, []any{"action", "intent", "current_password"}, "issue", "body", "required")
			assertContractValue(t, action, []any{"action", "intent", "current_password"}, "issue", "body", "allowed")
			assertContractValue(t, action, false, "issue", "body", "additionalProperties")
			assertContractValue(t, action, tc.issueKeys, "issue", "body", "intent_required")
			assertContractValue(t, action, tc.issueKeys, "issue", "body", "intent_allowed")
			assertContractValue(t, action, false, "issue", "body", "intent_additionalProperties")
			assertContractValue(t, action, tc.scope, "issue", "body", "action_literal")
			assertContractValue(t, action, "required independent owned UTF-8 bytes, cleared on every exit", "issue", "body", "current_password")
			assertContractValue(t, action, []any{"Idempotency-Key", "X-Action-Ticket"}, "issue", "forbidden_headers")
			assertContractValue(t, action, json.Number("201"), "issue", "response_status")
			assertContractValue(t, action, []any{"ticket", "expires_at"}, "issue", "response_keys")
			assertContractValue(t, action, json.Number("300"), "issue", "ticket_ttl_seconds")

			assertContractKeys(t, a08ContractObject(t, document, "actions", tc.scope, "execute"), "method", "path", "required_headers", "header_rules", "body", "response_status", "response_keys", "success_rule", "processing_success_response")
			assertContractValue(t, action, tc.method, "execute", "method")
			assertContractValue(t, action, tc.path, "execute", "path")
			assertContractValue(t, action, []any{"Idempotency-Key", "X-Action-Ticket"}, "execute", "required_headers")
			assertContractKeys(t, a08ContractObject(t, document, "actions", tc.scope, "execute", "header_rules"), "Idempotency-Key", "X-Action-Ticket")
			assertContractValue(t, action, "exactly one unique original value", "execute", "header_rules", "Idempotency-Key")
			assertContractValue(t, action, "exactly one valid "+tc.scope+" ticket bound to the path target and complete canonical intent", "execute", "header_rules", "X-Action-Ticket")
			assertContractKeys(t, a08ContractObject(t, document, "actions", tc.scope, "execute", "body"), tc.executeBodyKeys...)
			assertContractValue(t, action, tc.executeKeys, "execute", "body", "required")
			assertContractValue(t, action, tc.executeKeys, "execute", "body", "allowed")
			assertContractValue(t, action, false, "execute", "body", "additionalProperties")
			assertContractValue(t, action, tc.executeAction, "execute", "body", "action_literal")
			assertContractValue(t, action, true, "execute", "body", "path_supplies_target_guid")
			if tc.scope == "users.demote" {
				assertContractValue(t, action, []any{"overrides"}, "issue", "body", "intent_forbidden")
				assertContractValue(t, action, []any{"overrides"}, "execute", "body", "forbidden")
			}
			assertContractValue(t, action, json.Number("200"), "execute", "response_status")
			assertContractValue(t, action, []any{"operation_ref", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role"}, "execute", "response_keys")
			assertContractValue(t, action, "committed terminal result only; operation_commit_unknown is an error carrying operation_ref", "execute", "success_rule")
			assertContractValue(t, action, false, "execute", "processing_success_response")

			assertContractKeys(t, a08ContractObject(t, document, "actions", tc.scope, "query"), "method", "path", "exact_scope", "query_rule", "required_headers", "forbidden_headers", "original_key_rule", "response_status")
			assertContractValue(t, action, "GET", "query", "method")
			assertContractValue(t, action, "/admin/v2/operations", "query", "path")
			assertContractValue(t, action, tc.scope, "query", "exact_scope")
			assertContractValue(t, action, "accept exactly scope="+tc.scope+" and no other query parameters", "query", "query_rule")
			assertContractValue(t, action, []any{"Idempotency-Key"}, "query", "required_headers")
			assertContractValue(t, action, []any{"X-Action-Ticket"}, "query", "forbidden_headers")
			assertContractValue(t, action, "required exactly once using the original mutation key", "query", "original_key_rule")
			assertContractValue(t, action, json.Number("200"), "query", "response_status")
		})
	}

	stable := a08ContractObject(t, document, "stable_result")
	assertContractKeys(t, stable, "exact_keys", "schema", "role_by_scope", "storage_and_replay")
	assertContractValue(t, stable, []any{"operation_ref", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role"}, "exact_keys")
	assertContractKeys(t, a08ContractObject(t, document, "stable_result", "schema"), "operation_ref", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role")
	assertContractValue(t, stable, "valid opaque op_ value", "schema", "operation_ref")
	assertContractValue(t, stable, "canonical path target GUID string", "schema", "target_guid")
	assertContractValue(t, stable, "positive INT32 equal to the single committed auth-version advance", "schema", "resulting_auth_version")
	assertContractValue(t, stable, "positive INT64 equal to the single committed policy-head advance", "schema", "resulting_permissions_version")
	assertContractValue(t, stable, "user or admin as fixed by the exact scope", "schema", "resulting_role")
	assertContractValue(t, stable, map[string]any{"users.promote": "admin", "users.demote": "user", "users.permissions.write": "admin"}, "role_by_scope")
	assertContractValue(t, stable, "store all five fields on the terminal operation and replay those stored values; never project the current user row", "storage_and_replay")

	assertContractKeys(t, a08ContractObject(t, document, "operation_query"), "statuses", "response_keys", "nullable_result_rule", "state_rules", "retry_after", "visibility", "refresh")
	assertContractValue(t, document, []any{"processing", "succeeded", "failed", "pending_recovery"}, "operation_query", "statuses")
	assertContractValue(t, document, []any{"operation_ref", "scope", "status", "finished_at", "failure_code", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role"}, "operation_query", "response_keys")
	assertContractValue(t, document, "all four nullable result keys are always present; succeeded uses the stored stable result and processing, failed, and pending_recovery use null for every result key", "operation_query", "nullable_result_rule")
	stateRules := a08ContractObject(t, document, "operation_query", "state_rules")
	assertContractKeys(t, stateRules, "processing", "succeeded", "failed", "pending_recovery")
	for state, want := range map[string]map[string]any{
		"processing":       {"finished_at": nil, "failure_code": nil, "result_keys": "all present and null", "Retry-After": "required integer seconds 1..30"},
		"succeeded":        {"finished_at": "positive Unix milliseconds", "failure_code": nil, "result_keys": "all present from stored stable result", "Retry-After": "forbidden"},
		"failed":           {"finished_at": "positive Unix milliseconds", "failure_code": "one operation_failure_codes value", "result_keys": "all present and null", "Retry-After": "forbidden"},
		"pending_recovery": {"finished_at": nil, "failure_code": nil, "result_keys": "all present and null", "Retry-After": "forbidden"},
	} {
		stateRule := a08ContractObject(t, document, "operation_query", "state_rules", state)
		assertContractKeys(t, stateRule, "finished_at", "failure_code", "result_keys", "Retry-After")
		if !reflect.DeepEqual(stateRule, want) {
			t.Fatalf("operation query state %s=%#v, want %#v", state, stateRule, want)
		}
	}
	assertContractValue(t, document, "processing only; integer seconds 1..30", "operation_query", "retry_after")
	assertContractValue(t, document, "only the originating actor, current originating logical session, exact scope, and original key can query; hidden or unverifiable combinations return 404 and expired or tombstoned results return 410", "operation_query", "visibility")
	assertContractValue(t, document, "a succeeded query authorizes one owned target detail and permissions refresh; query never triggers mutation callbacks or consumes begin rate limit", "operation_query", "refresh")

	assertContractKeys(t, a08ContractObject(t, document, "errors"), "authentication_401", "envelope", "status_code_allowlist", "operation_failure_codes", "header_rules", "prohibited_fields")
	assertContractValue(t, document, map[string]any{"exact_body_keys": []any{"detail"}, "additionalProperties": false, "details": []any{"未登录", "Token无效或已过期"}}, "errors", "authentication_401")
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
		"exact_body_keys": []any{"error"},
		"base_error":      map[string]any{"exact_keys": []any{"code", "message", "type", "request_id"}, "additionalProperties": false, "message": "请求无法完成", "type": "admin_action_error"},
		"operation_commit_unknown_error": map[string]any{
			"exact_keys":           []any{"code", "message", "type", "request_id", "operation_ref"},
			"required":             []any{"code", "message", "type", "request_id", "operation_ref"},
			"additionalProperties": false,
			"code":                 "operation_commit_unknown",
			"message":              "请求无法完成",
			"type":                 "admin_action_error",
			"operation_ref":        "required valid opaque op_ value",
		},
	}, "errors", "envelope")
	assertContractValue(t, document, map[string]any{
		"matched":       "Cache-Control no-store and nonempty X-Request-ID; admin_action_error request_id equals X-Request-ID",
		"Retry-After":   "required only for action_rate_limited and processing query; forbidden otherwise",
		"operation_ref": "present only in operation_commit_unknown error",
	}, "errors", "header_rules")
	assertContractValue(t, document, []any{"ticket", "idempotency_key", "current_password", "password_material", "HMAC_input", "digest", "dependency_raw_text"}, "errors", "prohibited_fields")

	assertContractKeys(t, a08ContractObject(t, document, "frontend_security"), "memory_only", "prohibited_values", "secret_prohibitions", "mutation_auto_replay_count", "ownership")
	assertContractValue(t, document, []any{"current_password", "ticket", "idempotency_key", "unknown_state"}, "frontend_security", "memory_only")
	assertContractValue(t, document, []any{"current_password", "ticket", "idempotency_key", "password_material", "HMAC_input"}, "frontend_security", "prohibited_values")
	assertContractValue(t, document, []any{"responses", "audit_detail", "outbox_payload", "URL", "localStorage", "sessionStorage", "analytics", "ordinary_logs"}, "frontend_security", "secret_prohibitions")
	assertContractValue(t, document, json.Number("0"), "frontend_security", "mutation_auto_replay_count")
	assertContractValue(t, document, "closing or superseding the dialog clears current password, ticket, key, and unknown-state ownership; failed requests retain only nonsecret editor values and normalized reason", "frontend_security", "ownership")
	assertContractValue(t, document, "contract only; no route or consumer is claimed active and no implementation or acceptance is claimed complete", "implementation_boundary")
}

func TestA08ContractRejectsBoundaryMutations(t *testing.T) {
	contents, err := os.ReadFile(adminUserRolesPermissionsContractPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateA08ContractGuard(contents); err != nil {
		t.Fatalf("canonical contract rejected: %v", err)
	}
	mutations := []struct{ name, old, replacement string }{
		{"delete required path rule", "    \"path_guid\": \"canonical positive decimal signed int64 string without sign, whitespace, or leading zero\",\n", ""},
		{"rename exact query scope", `        "exact_scope": "users.promote",`, `        "renamed_scope": "users.promote",`},
		{"add unknown root key", `  "contract": "admin-user-roles-permissions-v1",`, "  \"contract\": \"admin-user-roles-permissions-v1\",\n  \"unknown\": true,"},
		{"relax strict JSON", "reject unknown, duplicate", "allow unknown and duplicate"},
		{"relax Root-only actor", "active Root with a current nonrevoked logical session", "active authenticated user with a current nonrevoked logical session"},
		{"downgrade authentication to Bearer-only", "existing Access Bearer authentication only; reject Refresh tokens as endpoint authentication; before every Issue, Execute, and Query authorization check require a fresh actor auth_version, current logical session and Redis revocation state, exact effective Root capability, target visibility, target state, target role, target auth version, policy version, and catalog version when applicable", "existing Bearer authentication"},
		{"allow promote self", "visible, nondeleted User in active or disabled state; never self or Root", "visible, nondeleted User in active or disabled state; self allowed; never Root"},
		{"allow promote Root", "visible, nondeleted User in active or disabled state; never self or Root", "visible, nondeleted User or Root in active or disabled state; never self"},
		{"relax ticket TTL", `        "ticket_ttl_seconds": 300`, `        "ticket_ttl_seconds": 3600`},
		{"relax password ownership", "required independent owned UTF-8 bytes, cleared on every exit", "required password string"},
		{"remove path target binding", `          "path_supplies_target_guid": true`, `          "path_supplies_target_guid": false`},
		{"weaken stable result", "valid opaque op_ value", "arbitrary string"},
		{"weaken query visibility", "only the originating actor, current originating logical session, exact scope, and original key can query", "any Root can query"},
		{"remove ownership cleanup", "closing or superseding the dialog clears current password, ticket, key, and unknown-state ownership", "closing retains dialog secrets"},
		{"query object becomes null", `      "query": {`, "      \"query\": null,\n      \"discarded_query\": {"},
		{"query object becomes string", `      "query": {`, "      \"query\": \"invalid\",\n      \"discarded_query\": {"},
		{"query object becomes array", `      "query": {`, "      \"query\": [],\n      \"discarded_query\": {"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := bytes.Replace(contents, []byte(mutation.old), []byte(mutation.replacement), 1)
			if bytes.Equal(candidate, contents) {
				t.Fatal("mutation did not match canonical contract")
			}
			if err := validateA08ContractGuard(candidate); err == nil {
				t.Fatal("boundary mutation was accepted")
			}
		})
	}
}

func validateA08ContractGuard(contents []byte) error {
	if err := validateContractTokens(contents); err != nil {
		return err
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	if err := a08ExactKeys(document, "contract", "revision", "status", "design_source", "paired_frontend_contract", "common", "actions", "stable_result", "operation_query", "errors", "frontend_security", "implementation_boundary"); err != nil {
		return fmt.Errorf("root: %w", err)
	}
	checks := []struct {
		want any
		path []string
	}{
		{"canonical positive decimal signed int64 string without sign, whitespace, or leading zero", []string{"common", "path_guid"}},
		{"one exact JSON object; reject unknown, duplicate, case-folded duplicate, invalid UTF-8, trailing fields, and oversized bodies before service invocation", []string{"common", "strict_json"}},
		{"integer 1..2147483647 matching the current capability catalog", []string{"common", "versions", "catalog_version"}},
		{"existing Access Bearer authentication only; reject Refresh tokens as endpoint authentication; before every Issue, Execute, and Query authorization check require a fresh actor auth_version, current logical session and Redis revocation state, exact effective Root capability, target visibility, target state, target role, target auth version, policy version, and catalog version when applicable", []string{"common", "authentication"}},
		{"active Root with a current nonrevoked logical session and the exact effective capability; Admin overrides cannot obtain these capabilities", []string{"common", "eligibility", "actor"}},
		{"visible, nondeleted User in active or disabled state; never self or Root", []string{"common", "eligibility", "promote_target"}},
		{"visible, nondeleted Admin in active or disabled state; never self or Root", []string{"common", "eligibility", "demote_target"}},
		{"visible, nondeleted Admin in active or disabled state; never self or Root", []string{"common", "eligibility", "permissions_target"}},
		{"404", []string{"common", "eligibility", "invisible_target"}},
		{"403", []string{"common", "eligibility", "visible_but_forbidden"}},
		{"409 with no security-version, session, policy, audit, outbox, or operation-result mutation", []string{"common", "eligibility", "stale_or_same_state"}},
		{false, []string{"actions", "users.promote", "issue", "body", "additionalProperties"}},
		{"required independent owned UTF-8 bytes, cleared on every exit", []string{"actions", "users.promote", "issue", "body", "current_password"}},
		{json.Number("300"), []string{"actions", "users.promote", "issue", "ticket_ttl_seconds"}},
		{true, []string{"actions", "users.promote", "execute", "body", "path_supplies_target_guid"}},
		{"users.promote", []string{"actions", "users.promote", "query", "exact_scope"}},
		{"valid opaque op_ value", []string{"stable_result", "schema", "operation_ref"}},
		{"all four nullable result keys are always present; succeeded uses the stored stable result and processing, failed, and pending_recovery use null for every result key", []string{"operation_query", "nullable_result_rule"}},
		{"only the originating actor, current originating logical session, exact scope, and original key can query; hidden or unverifiable combinations return 404 and expired or tombstoned results return 410", []string{"operation_query", "visibility"}},
		{"closing or superseding the dialog clears current password, ticket, key, and unknown-state ownership; failed requests retain only nonsecret editor values and normalized reason", []string{"frontend_security", "ownership"}},
	}
	for _, check := range checks {
		got, err := a08At(document, check.path...)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, check.want) {
			return fmt.Errorf("%v=%#v, want %#v", check.path, got, check.want)
		}
	}
	query, err := a08ObjectAt(document, "actions", "users.promote", "query")
	if err != nil {
		return err
	}
	return a08ExactKeys(query, "method", "path", "exact_scope", "query_rule", "required_headers", "forbidden_headers", "original_key_rule", "response_status")
}

func a08ContractObject(t *testing.T, root any, path ...any) map[string]any {
	t.Helper()
	value := contractAt(t, root, path...)
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("contract path %v expected object, got %T", path, value)
	}
	return object
}

func a08At(root map[string]any, path ...string) (any, error) {
	var current any = root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%v is not an object", path)
		}
		current, ok = object[key]
		if !ok {
			return nil, fmt.Errorf("missing %v", path)
		}
	}
	return current, nil
}

func a08ObjectAt(root map[string]any, path ...string) (map[string]any, error) {
	value, err := a08At(root, path...)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("path %v expected object, got %T", path, value)
	}
	return object, nil
}

func a08ExactKeys(object map[string]any, keys ...string) error {
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	slices.Sort(got)
	slices.Sort(keys)
	if !slices.Equal(got, keys) {
		return fmt.Errorf("keys=%v, want %v", got, keys)
	}
	return nil
}
