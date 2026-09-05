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

	assertContractKeys(t, document, "status", "production_observation", "implementation", "v2_user_read", "endpoints", "operation_statuses", "failure_codes", "error_contract", "frontend", "security_properties", "acceptance")
	assertContractValue(t, document, "active_users_delete_contract", "status")
	assertContractValue(t, document, true, "implementation", "handler_exists")
	assertContractValue(t, document, true, "implementation", "backend_route_exists")
	assertContractValue(t, document, false, "implementation", "frontend_client_exists")
	assertContractValue(t, document, true, "production_observation", "only_users_delete_active")
	assertContractValue(t, document, []any{
		map[string]any{"method": "POST", "path": "/admin/v2/action-verifications"},
		map[string]any{"method": "POST", "path": "/admin/v2/users/:guid/actions"},
		map[string]any{"method": "GET", "path": "/admin/v2/operations?scope=users.delete"},
	}, "production_observation", "registered_routes")
	assertContractValue(t, document, []any{"admin_users_list.items[].auth_version", "admin_user_detail.auth_version"}, "v2_user_read", "locations")
	assertContractValue(t, document, "integer_1_to_2147483647", "v2_user_read", "auth_version")
	assertContractValue(t, document, false, "v2_user_read", "legacy_shapes_changed")
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
	assertJSONFixture(t, contractAt(t, document, "endpoints", "query", "response_examples", "processing"), UserDeleteQueryResponse{OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Scope: "users.delete", Status: "processing"})

	assertContractValue(t, document, "forbidden", "endpoints", "issue", "request_headers", "Idempotency-Key")
	assertContractValue(t, document, "forbidden", "endpoints", "issue", "request_headers", "X-Action-Ticket")
	assertContractValue(t, document, "required_exactly_one_unique_original", "endpoints", "execute", "request_headers", "Idempotency-Key")
	assertContractValue(t, document, "required_exactly_one", "endpoints", "execute", "request_headers", "X-Action-Ticket")
	assertContractValue(t, document, "required_exactly_one_original", "endpoints", "query", "request_headers", "Idempotency-Key")
	assertContractValue(t, document, "forbidden", "endpoints", "query", "request_headers", "X-Action-Ticket")
	assertContractValue(t, document, []any{"processing", "succeeded", "failed", "pending_recovery"}, "operation_statuses")
	assertContractValue(t, document, []any{"action_rejected", "target_version_conflict", "policy_version_conflict", "target_state_conflict", "consumer_validation_failed"}, "failure_codes")
	assertContractValue(t, document, json.Number("0"), "endpoints", "issue", "post_replay_count")
	assertContractValue(t, document, json.Number("0"), "endpoints", "execute", "post_replay_count")
	assertContractValue(t, document, json.Number("1"), "endpoints", "query", "get_replay_after_refresh")
	assertContractValue(t, document, "processing_only_integer_seconds_1_to_30", "endpoints", "query", "response_headers", "Retry-After")
	assertContractValue(t, document, json.Number("410"), "endpoints", "legacy_delete", "response_status")
	assertContractValue(t, document, "legacy_user_delete_gone", "endpoints", "legacy_delete", "response_example", "error", "code")
	assertContractValue(t, document, map[string]any{
		"scope": "users.delete", "sensitivePostAutoReplay": false, "clientPersistence": "memory_only",
		"reasonPersistence": []any{"audit_logs.detail"}, "legacyDeleteStatus": json.Number("410"),
	}, "security_properties")
	assertContractValue(t, document, true, "acceptance", "contract_callable")
	assertContractValue(t, document, false, "acceptance", "frontend_connected")
	assertContractKeys(t, contractAt(t, document, "error_contract").(map[string]any), "envelope_example", "commit_unknown_example", "required_fields", "fixed_message", "fixed_type", "prohibited_fields", "operation_ref_rule", "http_statuses")
	assertContractKeys(t, contractAt(t, document, "frontend").(map[string]any), "memory_only", "storage_prohibitions", "post_replay_count", "query_get_after_refresh_max", "unload_recovery")
	assertContractKeys(t, contractAt(t, document, "security_properties").(map[string]any), "scope", "sensitivePostAutoReplay", "clientPersistence", "reasonPersistence", "legacyDeleteStatus")
	assertContractKeys(t, contractAt(t, document, "acceptance").(map[string]any), "internal_foundation_is_http_acceptance", "contract_callable", "frontend_connected", "contract_accepted", "statement")
}

func TestUserDeleteActiveContractStatusAndErrorMatrix(t *testing.T) {
	document := readContractTree(t)
	statuses := contractAt(t, document, "error_contract", "http_statuses").([]any)
	want := []json.Number{"400", "401", "403", "404", "409", "410", "422", "429", "503"}
	got := make([]json.Number, 0, len(statuses))
	for _, item := range statuses {
		row := item.(map[string]any)
		assertContractKeys(t, row, "status", "codes", "envelope", "retry_after")
		got = append(got, row["status"].(json.Number))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("HTTP status set=%v, want %v", got, want)
	}
	assertContractValue(t, document, "admin_action_error", "error_contract", "fixed_type")
	assertContractValue(t, document, "请求无法完成", "error_contract", "fixed_message")
	assertContractValue(t, document, "only_operation_commit_unknown", "error_contract", "operation_ref_rule")
	assertContractValue(t, document, []any{"action_rate_limited"}, "error_contract", "http_statuses", 7, "codes")
	assertContractValue(t, document, "required_integer_seconds_minimum_1", "error_contract", "http_statuses", 7, "retry_after")
	assertContractValue(t, document, []any{"action_dependency_unavailable", "operation_commit_unknown"}, "error_contract", "http_statuses", 8, "codes")
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
