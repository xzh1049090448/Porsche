package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"testing"
)

const adminActionContractPath = "../../docs/agents/contracts/admin-action-future-contract.json"

func TestUserDeleteActiveContractMatchesRuntimeFixtures(t *testing.T) {
	contents, err := os.ReadFile(adminActionContractPath)
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

	assertContractKeys(t, document, "status", "production_observation", "implementation", "future_a05_user_nickname_edit", "v2_user_read", "endpoints", "operation_statuses", "failure_codes", "error_contract", "frontend", "security_properties", "acceptance")
	assertContractValue(t, document, "active_users_delete_contract", "status")
	assertContractValue(t, document, true, "implementation", "handler_exists")
	assertContractValue(t, document, true, "implementation", "backend_route_exists")
	assertContractValue(t, document, false, "implementation", "frontend_client_exists")
	assertContractKeys(t, contractAt(t, document, "future_a05_user_nickname_edit").(map[string]any), "contract_path", "status", "implementation", "method", "path", "capability", "mutation_retry", "legacy_put")
	assertContractValue(t, document, "docs/agents/contracts/admin-user-edit-v1.json", "future_a05_user_nickname_edit", "contract_path")
	assertContractValue(t, document, "AGREED_FOR_IMPLEMENTATION", "future_a05_user_nickname_edit", "status")
	assertContractValue(t, document, "pending; this frozen contract does not register a route or activate a consumer", "future_a05_user_nickname_edit", "implementation")
	assertContractValue(t, document, "PATCH", "future_a05_user_nickname_edit", "method")
	assertContractValue(t, document, "/admin/v2/users/{guid}", "future_a05_user_nickname_edit", "path")
	assertContractValue(t, document, "users.edit", "future_a05_user_nickname_edit", "capability")
	assertContractValue(t, document, "never", "future_a05_user_nickname_edit", "mutation_retry")
	assertContractValue(t, document, "excluded", "future_a05_user_nickname_edit", "legacy_put")
	assertContractValue(t, document, true, "production_observation", "only_users_delete_active")
	assertContractValue(t, document, "unregistered", "production_observation", "other_action_routes")
	assertContractValue(t, document, []any{
		map[string]any{"method": "POST", "path": "/admin/v2/action-verifications"},
		map[string]any{"method": "POST", "path": "/admin/v2/users/:guid/actions"},
		map[string]any{"method": "GET", "path": "/admin/v2/operations?scope=users.delete"},
	}, "production_observation", "registered_routes")
	assertContractValue(t, document, []any{"admin_users_list.items[].auth_version", "admin_user_detail.auth_version"}, "v2_user_read", "locations")
	assertContractValue(t, document, "integer_1_to_2147483647", "v2_user_read", "auth_version")
	assertContractValue(t, document, false, "v2_user_read", "legacy_shapes_changed")
	assertContractValue(t, document, false, "v2_user_read", "client_writable")
	assertContractKeys(t, contractAt(t, document, "production_observation").(map[string]any), "registered_routes", "only_users_delete_active", "other_action_routes")
	assertContractKeys(t, contractAt(t, document, "implementation").(map[string]any), "handler_exists", "backend_route_exists", "frontend_client_exists")
	assertContractKeys(t, contractAt(t, document, "v2_user_read").(map[string]any), "locations", "auth_version", "client_writable", "legacy_shapes_changed")
	endpoints := contractAt(t, document, "endpoints").(map[string]any)
	assertContractKeys(t, endpoints, "issue", "execute", "query", "legacy_delete")
	assertContractKeys(t, endpoints["issue"].(map[string]any), "method", "path", "request_headers", "response_headers", "body_limit_bytes", "body_rule", "field_rules", "request_example", "response_status", "response_example", "ticket_ttl_seconds", "post_replay_count")
	assertContractKeys(t, endpoints["execute"].(map[string]any), "method", "path", "request_headers", "response_headers", "body_limit_bytes", "body_rule", "field_rules", "request_example", "response_status", "response_example", "success_semantics", "processing_response_allowed", "post_replay_count")
	assertContractKeys(t, endpoints["query"].(map[string]any), "method", "path", "request_headers", "response_headers", "query_rule", "response_status", "response_examples", "get_replay_after_refresh", "requires_exact_adapter", "requires_original_scope_key", "triggers_callback", "consumes_begin_rate_limit")
	assertContractKeys(t, endpoints["legacy_delete"].(map[string]any), "method", "path", "request_headers", "response_headers", "response_status", "response_example", "target_lookup", "database_or_redis_write")
	assertContractValue(t, document, "POST", "endpoints", "issue", "method")
	assertContractValue(t, document, "/admin/v2/action-verifications", "endpoints", "issue", "path")
	assertContractValue(t, document, "POST", "endpoints", "execute", "method")
	assertContractValue(t, document, "/admin/v2/users/:guid/actions", "endpoints", "execute", "path")
	assertContractValue(t, document, "GET", "endpoints", "query", "method")
	assertContractValue(t, document, "/admin/v2/operations?scope=users.delete", "endpoints", "query", "path")
	assertContractValue(t, document, "DELETE", "endpoints", "legacy_delete", "method")
	assertContractValue(t, document, "/admin/users/:guid", "endpoints", "legacy_delete", "path")

	issueBytes, err := json.Marshal(contractAt(t, document, "endpoints", "issue", "request_example"))
	if err != nil {
		t.Fatal(err)
	}
	issue, err := DecodeUserDeleteIssue(bytes.NewReader(issueBytes))
	if err != nil {
		t.Fatalf("runtime Issue decoder rejected contract fixture: %v", err)
	}
	defer clear(issue.Password)
	if issue.Action != "users.delete" || issue.TargetGUID != 123456789012345678 || issue.ExpectedVersion != 7 || issue.Reason != "duplicate account" || string(issue.Password) != "example-only-not-a-secret" {
		t.Fatal("runtime Issue decoder differed from contract fixture")
	}

	executeBytes, err := json.Marshal(contractAt(t, document, "endpoints", "execute", "request_example"))
	if err != nil {
		t.Fatal(err)
	}
	execute, err := DecodeUserDeleteExecute(bytes.NewReader(executeBytes))
	if err != nil {
		t.Fatalf("runtime Execute decoder rejected contract fixture: %v", err)
	}
	if execute.ExpectedVersion != 7 || execute.Reason != "duplicate account" {
		t.Fatal("runtime Execute decoder differed from contract fixture")
	}

	assertJSONFixture(t, contractAt(t, document, "endpoints", "issue", "response_example"), UserDeleteIssueResponse{Ticket: "av_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ExpiresAt: 1790000300000})
	assertJSONFixture(t, contractAt(t, document, "endpoints", "execute", "response_example"), DeleteUserResponse{OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", User: UserDeleteResponseUser{GUID: "123456789012345678", Status: "deleted"}})
	finishedAt := int64(1790000000000)
	failureCode := "target_version_conflict"
	queryFixtures := map[string]UserDeleteQueryResponse{
		"processing":       {OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Scope: "users.delete", Status: "processing"},
		"succeeded":        {OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Scope: "users.delete", Status: "succeeded", FinishedAt: &finishedAt},
		"failed":           {OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Scope: "users.delete", Status: "failed", FinishedAt: &finishedAt, FailureCode: &failureCode},
		"pending_recovery": {OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Scope: "users.delete", Status: "pending_recovery"},
	}
	for name, fixture := range queryFixtures {
		assertJSONFixture(t, contractAt(t, document, "endpoints", "query", "response_examples", name), fixture)
	}
	assertContractValue(t, document, map[string]any{"error": map[string]any{
		"code": "legacy_user_delete_gone", "message": "Use the verified v2 user delete action flow.",
		"type": "admin_action_error", "request_id": "req-contract-example-legacy",
	}}, "endpoints", "legacy_delete", "response_example")

	assertContractValue(t, document, map[string]any{"Idempotency-Key": "forbidden", "X-Action-Ticket": "forbidden"}, "endpoints", "issue", "request_headers")
	assertContractValue(t, document, map[string]any{"Cache-Control": "no-store", "X-Request-ID": "required_non_empty"}, "endpoints", "issue", "response_headers")
	assertContractValue(t, document, map[string]any{"Idempotency-Key": "required_exactly_one_unique_original", "X-Action-Ticket": "required_exactly_one"}, "endpoints", "execute", "request_headers")
	assertContractValue(t, document, map[string]any{"Cache-Control": "no-store", "X-Request-ID": "required_non_empty"}, "endpoints", "execute", "response_headers")
	assertContractValue(t, document, map[string]any{"Idempotency-Key": "required_exactly_one_original", "X-Action-Ticket": "forbidden"}, "endpoints", "query", "request_headers")
	assertContractValue(t, document, map[string]any{"Cache-Control": "no-store", "X-Request-ID": "required_non_empty", "Retry-After": "processing_only_integer_seconds_1_to_30"}, "endpoints", "query", "response_headers")
	assertContractValue(t, document, map[string]any{"Idempotency-Key": "ignored_no_effect", "X-Action-Ticket": "ignored_no_effect"}, "endpoints", "legacy_delete", "request_headers")
	assertContractValue(t, document, map[string]any{"Cache-Control": "no-store", "X-Request-ID": "required_non_empty"}, "endpoints", "legacy_delete", "response_headers")
	assertContractValue(t, document, json.Number("4096"), "endpoints", "issue", "body_limit_bytes")
	assertContractValue(t, document, json.Number("4096"), "endpoints", "execute", "body_limit_bytes")
	assertContractValue(t, document, "one_exact_json_object_no_unknown_duplicate_or_trailing_fields", "endpoints", "issue", "body_rule")
	assertContractValue(t, document, "one_exact_json_object_no_unknown_duplicate_or_trailing_fields", "endpoints", "execute", "body_rule")
	assertContractValue(t, document, map[string]any{
		"target_guid": "canonical_positive_signed_int64_decimal_string", "expected_auth_version": "integer_1_to_2147483647",
		"reason": "unicode_trimmed_1_to_200_code_points", "current_password": "required_owned_bytes_cleared_after_use",
	}, "endpoints", "issue", "field_rules")
	assertContractValue(t, document, map[string]any{
		"path_guid": "canonical_positive_signed_int64_decimal_string", "action": "exact_lowercase_delete",
		"expected_auth_version": "integer_1_to_2147483647", "reason": "unicode_trimmed_1_to_200_code_points",
	}, "endpoints", "execute", "field_rules")
	assertContractValue(t, document, json.Number("201"), "endpoints", "issue", "response_status")
	assertContractValue(t, document, json.Number("300"), "endpoints", "issue", "ticket_ttl_seconds")
	assertContractValue(t, document, json.Number("200"), "endpoints", "execute", "response_status")
	assertContractValue(t, document, json.Number("200"), "endpoints", "query", "response_status")
	assertContractValue(t, document, "committed_terminal_only_with_operation_ref", "endpoints", "execute", "success_semantics")
	assertContractValue(t, document, false, "endpoints", "execute", "processing_response_allowed")
	assertContractValue(t, document, "exact_literal_scope_users.delete_no_other_query_parameters", "endpoints", "query", "query_rule")
	assertContractValue(t, document, true, "endpoints", "query", "requires_exact_adapter")
	assertContractValue(t, document, true, "endpoints", "query", "requires_original_scope_key")
	assertContractValue(t, document, false, "endpoints", "query", "triggers_callback")
	assertContractValue(t, document, false, "endpoints", "query", "consumes_begin_rate_limit")
	assertContractValue(t, document, []any{"processing", "succeeded", "failed", "pending_recovery"}, "operation_statuses")
	assertContractValue(t, document, []any{"action_rejected", "target_version_conflict", "policy_version_conflict", "target_state_conflict", "consumer_validation_failed"}, "failure_codes")
	assertContractValue(t, document, json.Number("0"), "endpoints", "issue", "post_replay_count")
	assertContractValue(t, document, json.Number("0"), "endpoints", "execute", "post_replay_count")
	assertContractValue(t, document, json.Number("1"), "endpoints", "query", "get_replay_after_refresh")
	assertContractValue(t, document, "processing_only_integer_seconds_1_to_30", "endpoints", "query", "response_headers", "Retry-After")
	assertContractValue(t, document, json.Number("410"), "endpoints", "legacy_delete", "response_status")
	assertContractValue(t, document, false, "endpoints", "legacy_delete", "target_lookup")
	assertContractValue(t, document, false, "endpoints", "legacy_delete", "database_or_redis_write")
	assertContractValue(t, document, map[string]any{
		"scope": "users.delete", "sensitivePostAutoReplay": false, "clientPersistence": "memory_only",
		"reasonPersistence": []any{"audit_logs.detail"}, "legacyDeleteStatus": json.Number("410"),
	}, "security_properties")
	assertContractValue(t, document, true, "acceptance", "contract_callable")
	assertContractValue(t, document, false, "acceptance", "frontend_connected")
	assertContractValue(t, document, false, "acceptance", "internal_foundation_is_http_acceptance")
	assertContractValue(t, document, true, "acceptance", "contract_accepted")
	assertContractValue(t, document, "The users.delete backend HTTP contract is active and frozen; frontend implementation remains pending.", "acceptance", "statement")
	assertContractKeys(t, contractAt(t, document, "error_contract").(map[string]any), "envelope_example", "commit_unknown_example", "required_fields", "fixed_message", "fixed_type", "prohibited_fields", "operation_ref_rule", "http_statuses")
	assertContractKeys(t, contractAt(t, document, "frontend").(map[string]any), "memory_only", "storage_prohibitions", "post_replay_count", "query_get_after_refresh_max", "unload_recovery")
	assertContractKeys(t, contractAt(t, document, "security_properties").(map[string]any), "scope", "sensitivePostAutoReplay", "clientPersistence", "reasonPersistence", "legacyDeleteStatus")
	assertContractKeys(t, contractAt(t, document, "acceptance").(map[string]any), "internal_foundation_is_http_acceptance", "contract_callable", "frontend_connected", "contract_accepted", "statement")
	assertContractValue(t, document, []any{"ticket", "idempotency_key", "unknown_state"}, "frontend", "memory_only")
	assertContractValue(t, document, []any{"localStorage", "sessionStorage", "URL", "analytics", "ordinary_logs"}, "frontend", "storage_prohibitions")
	assertContractValue(t, document, json.Number("0"), "frontend", "post_replay_count")
	assertContractValue(t, document, json.Number("1"), "frontend", "query_get_after_refresh_max")
	assertContractValue(t, document, false, "frontend", "unload_recovery")
}

func TestUserDeleteActiveContractStatusAndErrorMatrix(t *testing.T) {
	document := readContractTree(t)
	wantStatuses := []any{
		map[string]any{"status": json.Number("400"), "codes": []any{"invalid_admin_action_request"}, "envelope": "admin_action_error", "retry_after": "forbidden"},
		map[string]any{"status": json.Number("401"), "codes": []any{}, "envelope": "legacy_detail", "retry_after": "forbidden", "category": "existing_authentication_failure", "body_shape": map[string]any{"required_fields": []any{"detail"}, "additional_fields": false}, "details": []any{"未登录", "Token无效或已过期"}},
		map[string]any{"status": json.Number("403"), "codes": []any{"action_verification_rejected", "action_operation_rejected"}, "envelope": "admin_action_error", "retry_after": "forbidden"},
		map[string]any{"status": json.Number("404"), "codes": []any{"action_target_not_found", "action_operation_not_found"}, "envelope": "admin_action_error", "retry_after": "forbidden"},
		map[string]any{"status": json.Number("409"), "codes": []any{"action_verification_conflict", "idempotency_conflict", "idempotency_cross_session", "action_rejected", "target_version_conflict", "policy_version_conflict", "target_state_conflict", "consumer_validation_failed"}, "envelope": "admin_action_error", "retry_after": "forbidden"},
		map[string]any{"status": json.Number("410"), "codes": []any{"operation_expired"}, "envelope": "admin_action_error", "retry_after": "forbidden"},
		map[string]any{"status": json.Number("422"), "codes": []any{"action_inactive"}, "envelope": "admin_action_error", "retry_after": "forbidden"},
		map[string]any{"status": json.Number("429"), "codes": []any{"action_rate_limited"}, "envelope": "admin_action_error", "retry_after": "required_integer_seconds_minimum_1"},
		map[string]any{"status": json.Number("503"), "codes": []any{"action_dependency_unavailable", "operation_commit_unknown"}, "envelope": "admin_action_error", "retry_after": "forbidden"},
	}
	assertContractValue(t, document, wantStatuses, "error_contract", "http_statuses")
	assertContractValue(t, document, map[string]any{"error": map[string]any{
		"code": "idempotency_conflict", "message": "请求无法完成", "type": "admin_action_error", "request_id": "req-contract-example-1",
	}}, "error_contract", "envelope_example")
	assertContractValue(t, document, map[string]any{"error": map[string]any{
		"code": "operation_commit_unknown", "message": "请求无法完成", "type": "admin_action_error",
		"request_id": "req-contract-example-2", "operation_ref": "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}}, "error_contract", "commit_unknown_example")
	assertContractValue(t, document, []any{"code", "message", "type", "request_id"}, "error_contract", "required_fields")
	assertContractValue(t, document, []any{"target", "ticket", "idempotency_key", "digest", "internal_id", "dependency_raw_text"}, "error_contract", "prohibited_fields")
	assertContractValue(t, document, "admin_action_error", "error_contract", "fixed_type")
	assertContractValue(t, document, "请求无法完成", "error_contract", "fixed_message")
	assertContractValue(t, document, "only_operation_commit_unknown", "error_contract", "operation_ref_rule")
}

func TestUserDeleteActiveContractRejectsDuplicateKeys(t *testing.T) {
	contents, err := os.ReadFile(adminActionContractPath)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct{ old, replacement string }{
		{`  "status": "active_users_delete_contract",`, "  \"status\": \"active_users_delete_contract\",\n  \"status\": \"active_users_delete_contract\","},
		{`    "handler_exists": true,`, "    \"handler_exists\": true,\n    \"handler_exists\": true,"},
		{`        "status": 400,`, "        \"status\": 400,\n        \"status\": 400,"},
	}
	for index, mutation := range mutations {
		candidate := bytes.Replace(contents, []byte(mutation.old), []byte(mutation.replacement), 1)
		if bytes.Equal(candidate, contents) {
			t.Fatalf("duplicate-key mutation fixture %d did not match", index)
		}
		if err := validateContractTokens(candidate); err == nil {
			t.Fatalf("duplicate contract key mutation %d was accepted", index)
		}
	}
}

func readContractTree(t *testing.T) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(adminActionContractPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func assertJSONFixture(t *testing.T, documented, runtime any) {
	t.Helper()
	runtimeBytes, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(runtimeBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(documented, decoded) {
		t.Fatalf("documented fixture=%#v, runtime fixture=%#v", documented, decoded)
	}
}

func assertContractKeys(t *testing.T, object map[string]any, keys ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	slices.Sort(got)
	slices.Sort(keys)
	if !slices.Equal(got, keys) {
		t.Fatalf("object keys=%v, want %v", got, keys)
	}
}

func assertContractValue(t *testing.T, document map[string]any, want any, path ...any) {
	t.Helper()
	got := contractAt(t, document, path...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contract path %v=%#v, want %#v", path, got, want)
	}
}

func contractAt(t *testing.T, root any, path ...any) any {
	t.Helper()
	value := root
	for _, part := range path {
		switch key := part.(type) {
		case string:
			object, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("contract path %v expected object before %q", path, key)
			}
			var exists bool
			value, exists = object[key]
			if !exists {
				t.Fatalf("contract path %v missing %q", path, key)
			}
		case int:
			array, ok := value.([]any)
			if !ok || key < 0 || key >= len(array) {
				t.Fatalf("contract path %v invalid index %d", path, key)
			}
			value = array[key]
		default:
			t.Fatalf("contract path %v has unsupported component %T", path, part)
		}
	}
	return value
}

func validateContractTokens(contents []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := validateContractTokenValue(decoder, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); err == nil {
		return fmt.Errorf("trailing root value")
	} else if err != io.EOF {
		return err
	}
	return nil
}

func validateContractTokenValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%s: non-string key", path)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("%s: duplicate key %q", path, key)
			}
			seen[key] = struct{}{}
			if err := validateContractTokenValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := validateContractTokenValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("%s: unexpected delimiter", path)
	}
}
