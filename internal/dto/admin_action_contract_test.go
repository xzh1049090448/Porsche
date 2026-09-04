package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const adminActionContractPath = "../../docs/agents/contracts/admin-action-future-contract.json"

type futureDeleteIntent struct {
	TargetGUID          string `json:"target_guid"`
	ExpectedAuthVersion int    `json:"expected_auth_version"`
	Reason              string `json:"reason"`
}

type futureIssueRequest struct {
	Action          string             `json:"action"`
	Intent          futureDeleteIntent `json:"intent"`
	CurrentPassword string             `json:"current_password"`
}

type futureIssueResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresAt int64  `json:"expires_at"`
}

type futureOperationResponse struct {
	OperationRef string  `json:"operation_ref"`
	Scope        string  `json:"scope"`
	Status       string  `json:"status"`
	FinishedAt   *int64  `json:"finished_at"`
	FailureCode  *string `json:"failure_code"`
}

type futureErrorEnvelope struct {
	Error futureError `json:"error"`
}

type futureError struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	OperationRef string `json:"operation_ref,omitempty"`
}

type futureContractDocument struct {
	Status                string                   `json:"status"`
	ProductionObservation int                      `json:"production_observation"`
	Implementation        futureImplementation     `json:"implementation"`
	Endpoints             futureEndpoints          `json:"endpoints"`
	OperationStatuses     []string                 `json:"operation_statuses"`
	FailureCodes          []string                 `json:"failure_codes"`
	ErrorContract         futureErrorContract      `json:"error_contract"`
	Frontend              futureFrontendContract   `json:"frontend"`
	Acceptance            futureAcceptanceContract `json:"acceptance"`
}

type futureImplementation struct {
	HandlerExists        bool `json:"handler_exists"`
	BackendRouteExists   bool `json:"backend_route_exists"`
	FrontendClientExists bool `json:"frontend_client_exists"`
}

type futureEndpoints struct {
	Issue         futureIssueEndpoint        `json:"issue"`
	BusinessPOSTs futureBusinessPOSTContract `json:"business_posts"`
	Query         futureQueryEndpoint        `json:"query"`
}

type futureIssueEndpoint struct {
	Method          string                `json:"method"`
	Path            string                `json:"path"`
	RequestHeaders  futureRequestHeaders  `json:"request_headers"`
	ResponseHeaders futureResponseHeaders `json:"response_headers"`
	RequestExample  futureIssueRequest    `json:"request_example"`
	ResponseStatus  int                   `json:"response_status"`
	ResponseExample futureIssueResponse   `json:"response_example"`
	POSTReplayCount int                   `json:"post_replay_count"`
}

type futureQueryEndpoint struct {
	Method                   string                  `json:"method"`
	Path                     string                  `json:"path"`
	ExamplePath              string                  `json:"example_path"`
	RequestHeaders           futureRequestHeaders    `json:"request_headers"`
	ResponseHeaders          futureResponseHeaders   `json:"response_headers"`
	ProcessingResponse       futureOperationResponse `json:"processing_response"`
	GETReplayAfterRefresh    int                     `json:"get_replay_after_refresh"`
	RequiresExactAdapter     bool                    `json:"requires_exact_adapter"`
	RequiresOriginalScopeKey bool                    `json:"requires_original_scope_key"`
}

type futureBusinessPOSTContract struct {
	Method                     string                `json:"method"`
	PathRule                   string                `json:"path_rule"`
	GenericExecuteRoute        bool                  `json:"generic_execute_route"`
	CurrentlyUnregisteredPaths []string              `json:"currently_unregistered_paths"`
	RequestHeaders             futureRequestHeaders  `json:"request_headers"`
	ResponseHeaders            futureResponseHeaders `json:"response_headers"`
	SuccessSemantics           string                `json:"success_semantics"`
	POSTReplayCount            int                   `json:"post_replay_count"`
}

type futureRequestHeaders struct {
	IdempotencyKey string `json:"Idempotency-Key"`
	ActionTicket   string `json:"X-Action-Ticket,omitempty"`
}

type futureResponseHeaders struct {
	CacheControl string `json:"Cache-Control"`
	RequestID    string `json:"X-Request-ID"`
	RetryAfter   string `json:"Retry-After,omitempty"`
}

type futureErrorContract struct {
	EnvelopeExample      futureErrorEnvelope        `json:"envelope_example"`
	CommitUnknownExample futureErrorEnvelope        `json:"commit_unknown_example"`
	RequiredFields       []string                   `json:"required_fields"`
	FixedMessage         string                     `json:"fixed_message"`
	FixedType            string                     `json:"fixed_type"`
	ProhibitedFields     []string                   `json:"prohibited_fields"`
	OperationRefRule     string                     `json:"operation_ref_rule"`
	HTTPStatuses         []futureHTTPStatusContract `json:"http_statuses"`
}

type futureHTTPStatusContract struct {
	Status           int                    `json:"status"`
	Category         string                 `json:"category"`
	RetryAfterHeader string                 `json:"retry_after_header,omitempty"`
	RetryAfterRange  *futureRetryAfterRange `json:"retry_after_seconds_range"`
}

type futureRetryAfterRange struct {
	Minimum int  `json:"minimum"`
	Maximum *int `json:"maximum"`
}

type futureFrontendContract struct {
	MemoryOnly              []string `json:"memory_only"`
	StorageProhibitions     []string `json:"storage_prohibitions"`
	POSTReplayCount         int      `json:"post_replay_count"`
	QueryGETAfterRefreshMax int      `json:"query_get_after_refresh_max"`
	UnloadRecovery          bool     `json:"unload_recovery"`
}

type futureAcceptanceContract struct {
	InternalFoundationIsHTTPAcceptance bool   `json:"internal_foundation_is_http_acceptance"`
	ContractCallable                   bool   `json:"contract_callable"`
	ContractAccepted                   bool   `json:"contract_accepted"`
	Statement                          string `json:"statement"`
}

func TestAdminActionFutureContractExactJSONFixtures(t *testing.T) {
	issue := futureIssueRequest{
		Action: "users.delete",
		Intent: futureDeleteIntent{
			TargetGUID:          "123456789012345678",
			ExpectedAuthVersion: 7,
			Reason:              "duplicate account",
		},
		CurrentPassword: "example-only-not-a-secret",
	}
	assertExactJSON(t, issue, `{"action":"users.delete","intent":{"target_guid":"123456789012345678","expected_auth_version":7,"reason":"duplicate account"},"current_password":"example-only-not-a-secret"}`)

	assertExactJSON(t, futureIssueResponse{
		Ticket:    "av_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ExpiresAt: 1790000300000,
	}, `{"ticket":"av_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","expires_at":1790000300000}`)

	processing := futureOperationResponse{
		OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Scope:        "users.delete",
		Status:       "processing",
	}
	assertExactJSON(t, processing, `{"operation_ref":"op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","scope":"users.delete","status":"processing","finished_at":null,"failure_code":null}`)

	regular := futureErrorEnvelope{Error: futureError{
		Code:      "idempotency_conflict",
		Message:   "请求无法完成",
		Type:      "admin_action_error",
		RequestID: "req-contract-example-1",
	}}
	assertExactJSON(t, regular, `{"error":{"code":"idempotency_conflict","message":"请求无法完成","type":"admin_action_error","request_id":"req-contract-example-1"}}`)

	unknown := futureErrorEnvelope{Error: futureError{
		Code:         "operation_commit_unknown",
		Message:      "请求无法完成",
		Type:         "admin_action_error",
		RequestID:    "req-contract-example-2",
		OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}}
	assertExactJSON(t, unknown, `{"error":{"code":"operation_commit_unknown","message":"请求无法完成","type":"admin_action_error","request_id":"req-contract-example-2","operation_ref":"op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}`)

	var decoded futureIssueRequest
	decoder := json.NewDecoder(bytes.NewBufferString(`{"action":"users.delete","intent":{"target_guid":"123456789012345678","expected_auth_version":7,"reason":"duplicate account"},"current_password":"example-only-not-a-secret","actor_password":"must-be-rejected"}`))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err == nil {
		t.Fatal("unknown actor_password field was accepted")
	}
}

func TestAdminActionFutureContractDocumentMatchesFrozenFixtures(t *testing.T) {
	documentBytes, err := os.ReadFile(adminActionContractPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFutureContractRawTokens(documentBytes); err != nil {
		t.Fatalf("validate raw contract token stream: %v", err)
	}
	var document futureContractDocument
	decoder := json.NewDecoder(bytes.NewReader(documentBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("contract contains trailing JSON data: %v", err)
	}

	if document.Status != "inactive_contract" || document.ProductionObservation != 404 {
		t.Fatalf("inactive boundary drifted: status=%q observation=%d", document.Status, document.ProductionObservation)
	}
	if document.Implementation.HandlerExists || document.Implementation.BackendRouteExists || document.Implementation.FrontendClientExists {
		t.Fatalf("inactive contract claims an implementation exists: %+v", document.Implementation)
	}
	if document.Endpoints.Issue.Method != "POST" || document.Endpoints.Issue.Path != "/admin/v2/action-verifications" {
		t.Fatalf("Issue endpoint drifted: %+v", document.Endpoints.Issue)
	}
	if document.Endpoints.Issue.ResponseStatus != 201 {
		t.Fatalf("Issue response status=%d, want 201", document.Endpoints.Issue.ResponseStatus)
	}
	if document.Endpoints.Issue.RequestHeaders.IdempotencyKey != "forbidden" || document.Endpoints.Issue.RequestHeaders.ActionTicket != "forbidden" {
		t.Fatalf("Issue request headers drifted: %+v", document.Endpoints.Issue.RequestHeaders)
	}
	if document.Endpoints.Query.Method != "GET" || document.Endpoints.Query.Path != "/admin/v2/operations?scope={descriptor_action_name}" || document.Endpoints.Query.ExamplePath != "/admin/v2/operations?scope=users.delete" {
		t.Fatalf("Query endpoint drifted: %+v", document.Endpoints.Query)
	}
	wantInactiveBusinessPaths := []string{
		"/admin/v2/users",
		"/admin/v2/users/:guid/actions",
		"/admin/v2/public-content/announcements/publish",
		"/admin/v2/public-content/announcements/rollback",
	}
	if document.Endpoints.BusinessPOSTs.Method != "POST" || document.Endpoints.BusinessPOSTs.PathRule != "action_specific_prd_route" || document.Endpoints.BusinessPOSTs.GenericExecuteRoute || !slices.Equal(document.Endpoints.BusinessPOSTs.CurrentlyUnregisteredPaths, wantInactiveBusinessPaths) {
		t.Fatalf("business POST boundary drifted: %+v", document.Endpoints.BusinessPOSTs)
	}
	if document.Endpoints.BusinessPOSTs.RequestHeaders.IdempotencyKey != "required_unique_original" || document.Endpoints.BusinessPOSTs.RequestHeaders.ActionTicket != "required" {
		t.Fatalf("business POST request headers drifted: %+v", document.Endpoints.BusinessPOSTs.RequestHeaders)
	}
	if document.Endpoints.Query.RequestHeaders.IdempotencyKey != "required_original" || document.Endpoints.Query.RequestHeaders.ActionTicket != "" {
		t.Fatalf("Query request headers drifted: %+v", document.Endpoints.Query.RequestHeaders)
	}
	for name, headers := range map[string]futureResponseHeaders{
		"Issue":        document.Endpoints.Issue.ResponseHeaders,
		"BusinessPOST": document.Endpoints.BusinessPOSTs.ResponseHeaders,
		"Query":        document.Endpoints.Query.ResponseHeaders,
	} {
		if headers.CacheControl != "no-store" || headers.RequestID != "required_non_empty" {
			t.Fatalf("%s response headers drifted: %+v", name, headers)
		}
	}
	if document.Endpoints.Query.ResponseHeaders.RetryAfter != "processing_integer_seconds_1_to_30" {
		t.Fatalf("processing Retry-After drifted: %q", document.Endpoints.Query.ResponseHeaders.RetryAfter)
	}
	if document.Endpoints.Issue.POSTReplayCount != 0 || document.Endpoints.BusinessPOSTs.POSTReplayCount != 0 || document.Endpoints.BusinessPOSTs.SuccessSemantics != "committed_terminal_only_with_operation_ref" || document.Endpoints.Query.GETReplayAfterRefresh != 1 || !document.Endpoints.Query.RequiresExactAdapter || !document.Endpoints.Query.RequiresOriginalScopeKey {
		t.Fatalf("replay/query authorization contract drifted: issue=%d query=%+v", document.Endpoints.Issue.POSTReplayCount, document.Endpoints.Query)
	}

	assertEqualJSON(t, document.Endpoints.Issue.RequestExample, futureIssueRequest{
		Action:          "users.delete",
		Intent:          futureDeleteIntent{TargetGUID: "123456789012345678", ExpectedAuthVersion: 7, Reason: "duplicate account"},
		CurrentPassword: "example-only-not-a-secret",
	})
	assertEqualJSON(t, document.Endpoints.Issue.ResponseExample, futureIssueResponse{Ticket: "av_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ExpiresAt: 1790000300000})
	assertEqualJSON(t, document.Endpoints.Query.ProcessingResponse, futureOperationResponse{OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Scope: "users.delete", Status: "processing"})

	wantStatuses := []string{"processing", "succeeded", "failed", "pending_recovery"}
	if !slices.Equal(document.OperationStatuses, wantStatuses) {
		t.Fatalf("operation statuses=%v, want %v", document.OperationStatuses, wantStatuses)
	}
	wantFailureCodes := []string{"action_rejected", "target_version_conflict", "policy_version_conflict", "target_state_conflict", "consumer_validation_failed"}
	if !slices.Equal(document.FailureCodes, wantFailureCodes) {
		t.Fatalf("failure codes=%v, want %v", document.FailureCodes, wantFailureCodes)
	}

	assertEqualJSON(t, document.ErrorContract.EnvelopeExample, futureErrorEnvelope{Error: futureError{Code: "idempotency_conflict", Message: "请求无法完成", Type: "admin_action_error", RequestID: "req-contract-example-1"}})
	assertEqualJSON(t, document.ErrorContract.CommitUnknownExample, futureErrorEnvelope{Error: futureError{Code: "operation_commit_unknown", Message: "请求无法完成", Type: "admin_action_error", RequestID: "req-contract-example-2", OperationRef: "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}})
	if !slices.Equal(document.ErrorContract.RequiredFields, []string{"code", "message", "type", "request_id"}) || document.ErrorContract.FixedMessage != "请求无法完成" || document.ErrorContract.FixedType != "admin_action_error" {
		t.Fatalf("fixed error envelope drifted: %+v", document.ErrorContract)
	}
	if !slices.Equal(document.ErrorContract.ProhibitedFields, []string{"target", "ticket", "idempotency_key", "digest", "internal_id", "dependency_raw_text"}) {
		t.Fatalf("error redaction fields drifted: %v", document.ErrorContract.ProhibitedFields)
	}
	if document.ErrorContract.OperationRefRule != "only_operation_commit_unknown" {
		t.Fatalf("operation_ref rule=%q", document.ErrorContract.OperationRefRule)
	}
	wantHTTPStatuses := []futureHTTPStatusContract{
		{Status: 400, Category: "invalid_header_body_or_format"},
		{Status: 401, Category: "existing_authentication_failure"},
		{Status: 403, Category: "verification_ticket_permission_or_session_rejection"},
		{Status: 404, Category: "operation_or_target_not_confirmable_or_hidden"},
		{Status: 409, Category: "idempotency_payload_or_cross_session_conflict"},
		{Status: 410, Category: "operation_result_query_expired"},
		{Status: 422, Category: "action_descriptor_inactive"},
		{Status: 429, Category: "redis_rate_limited", RetryAfterHeader: "required_integer_seconds", RetryAfterRange: &futureRetryAfterRange{Minimum: 1}},
		{Status: 503, Category: "redis_mysql_audit_outbox_unavailable_or_commit_unknown"},
	}
	if !reflect.DeepEqual(document.ErrorContract.HTTPStatuses, wantHTTPStatuses) {
		t.Fatalf("HTTP status contract drifted\n got: %#v\nwant: %#v", document.ErrorContract.HTTPStatuses, wantHTTPStatuses)
	}

	wantMemoryOnly := []string{"ticket", "idempotency_key", "unknown_state"}
	wantStorageProhibitions := []string{"localStorage", "sessionStorage", "URL", "analytics", "ordinary_logs"}
	if !slices.Equal(document.Frontend.MemoryOnly, wantMemoryOnly) || !slices.Equal(document.Frontend.StorageProhibitions, wantStorageProhibitions) {
		t.Fatalf("frontend storage boundary drifted: %+v", document.Frontend)
	}
	if document.Frontend.POSTReplayCount != 0 || document.Frontend.QueryGETAfterRefreshMax != 1 || document.Frontend.UnloadRecovery {
		t.Fatalf("frontend replay boundary drifted: %+v", document.Frontend)
	}
	if document.Acceptance.InternalFoundationIsHTTPAcceptance || document.Acceptance.ContractCallable || document.Acceptance.ContractAccepted || document.Acceptance.Statement != "The internal foundation does not constitute HTTP acceptance." {
		t.Fatalf("inactive contract overclaims acceptance: %+v", document.Acceptance)
	}
}

func TestAdminActionFutureContractRawShapeRejectsSchemaMutations(t *testing.T) {
	original := readFutureContractTree(t)
	if err := validateFutureContractShape(original); err != nil {
		t.Fatalf("original contract shape rejected: %v", err)
	}

	mutations := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing processing finished_at null", mutate: deleteFutureContractPath("endpoints", "query", "processing_response", "finished_at")},
		{name: "missing processing failure_code null", mutate: deleteFutureContractPath("endpoints", "query", "processing_response", "failure_code")},
		{name: "missing implementation handler_exists false", mutate: deleteFutureContractPath("implementation", "handler_exists")},
		{name: "missing business generic_execute_route false", mutate: deleteFutureContractPath("endpoints", "business_posts", "generic_execute_route")},
		{name: "missing frontend unload_recovery false", mutate: deleteFutureContractPath("frontend", "unload_recovery")},
		{name: "missing HTTP 400 null range", mutate: deleteFutureContractPath("error_contract", "http_statuses", 0, "retry_after_seconds_range")},
		{name: "missing HTTP 429 maximum null", mutate: deleteFutureContractPath("error_contract", "http_statuses", 7, "retry_after_seconds_range", "maximum")},
		{name: "unknown root field", mutate: setFutureContractPath(true, "unknown")},
		{name: "unknown nested field", mutate: setFutureContractPath("leak", "error_contract", "envelope_example", "error", "unknown")},
		{name: "false changed to string", mutate: setFutureContractPath("false", "implementation", "handler_exists")},
		{name: "nullable number changed to bool", mutate: setFutureContractPath(false, "endpoints", "query", "processing_response", "finished_at")},
		{name: "required null changed to object", mutate: setFutureContractPath(map[string]any{}, "error_contract", "http_statuses", 0, "retry_after_seconds_range")},
		{name: "nullable maximum changed to bool", mutate: setFutureContractPath(false, "error_contract", "http_statuses", 7, "retry_after_seconds_range", "maximum")},
		{name: "string changed to number", mutate: setFutureContractPath(json.Number("404"), "status")},
		{name: "array item changed to bool", mutate: setFutureContractPath(false, "operation_statuses", 0)},
		{name: "array changed to empty", mutate: setFutureContractPath([]any{}, "failure_codes")},
		{name: "object changed to empty", mutate: setFutureContractPath(map[string]any{}, "implementation")},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := cloneFutureContractTree(t, original)
			mutation.mutate(candidate)
			if err := validateFutureContractShape(candidate); err == nil {
				t.Fatal("mutated contract shape was accepted")
			}
		})
	}
}

func TestAdminActionFutureContractRawTokensRejectDuplicateKeysAndInvalidRoots(t *testing.T) {
	original, err := os.ReadFile(adminActionContractPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFutureContractRawTokens(original); err != nil {
		t.Fatalf("original contract token stream rejected: %v", err)
	}

	mutations := []struct {
		name        string
		oldFragment string
		newFragment string
	}{
		{
			name:        "duplicate root status with same value",
			oldFragment: "  \"status\": \"inactive_contract\",",
			newFragment: "  \"status\": \"inactive_contract\",\n  \"status\": \"inactive_contract\",",
		},
		{
			name:        "duplicate root status with conflicting value",
			oldFragment: "  \"status\": \"inactive_contract\",",
			newFragment: "  \"status\": \"inactive_contract\",\n  \"status\": \"active\",",
		},
		{
			name:        "duplicate nested implementation boolean",
			oldFragment: "    \"handler_exists\": false,",
			newFragment: "    \"handler_exists\": false,\n    \"handler_exists\": false,",
		},
		{
			name:        "duplicate nested nullable field",
			oldFragment: "        \"finished_at\": null,",
			newFragment: "        \"finished_at\": null,\n        \"finished_at\": 1790000000000,",
		},
		{
			name:        "duplicate object key inside array item",
			oldFragment: "        \"status\": 400,",
			newFragment: "        \"status\": 400,\n        \"status\": 401,",
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if bytes.Count(original, []byte(mutation.oldFragment)) != 1 {
				t.Fatalf("test mutation fragment is not unique: %q", mutation.oldFragment)
			}
			candidate := bytes.Replace(original, []byte(mutation.oldFragment), []byte(mutation.newFragment), 1)
			if err := validateFutureContractRawTokens(candidate); err == nil {
				t.Fatal("duplicate-key token stream was accepted")
			}
		})
	}

	invalidRoots := []struct {
		name      string
		candidate []byte
	}{
		{name: "trailing second root", candidate: append(append([]byte(nil), original...), []byte("\n{}")...)},
		{name: "truncated root", candidate: append([]byte(nil), original[:len(original)-2]...)},
		{name: "object key without value", candidate: []byte(`{"status":}`)},
	}
	for _, mutation := range invalidRoots {
		t.Run(mutation.name, func(t *testing.T) {
			if err := validateFutureContractRawTokens(mutation.candidate); err == nil {
				t.Fatal("invalid token stream was accepted")
			}
		})
	}
}

func validateFutureContractRawTokens(contents []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := validateFutureJSONTokenValue(decoder, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); err == nil {
		return fmt.Errorf("$ has a trailing root value")
	} else if err != io.EOF {
		return fmt.Errorf("$ has invalid trailing token: %w", err)
	}
	return nil
}

func validateFutureJSONTokenValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%s is missing or invalid: %w", path, err)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		switch token.(type) {
		case nil, bool, string, json.Number:
			return nil
		default:
			return fmt.Errorf("%s has unsupported primitive type %T", path, token)
		}
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("%s has invalid object key: %w", path, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%s has non-string object key", path)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("%s has duplicate key %q", path, key)
			}
			seen[key] = struct{}{}
			if err := validateFutureJSONTokenValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%s object is not closed: %w", path, err)
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("%s object has mismatched closing delimiter", path)
		}
		return nil
	case '[':
		index := 0
		for decoder.More() {
			if err := validateFutureJSONTokenValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%s array is not closed: %w", path, err)
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("%s array has mismatched closing delimiter", path)
		}
		return nil
	default:
		return fmt.Errorf("%s starts with unexpected closing delimiter", path)
	}
}

type futureJSONKind uint8

const (
	futureJSONObject futureJSONKind = iota + 1
	futureJSONArray
	futureJSONString
	futureJSONInteger
	futureJSONBool
	futureJSONNull
)

type futureJSONShape struct {
	kind     futureJSONKind
	nullable bool
	fields   map[string]*futureJSONShape
	items    []*futureJSONShape
}

func validateFutureContractShape(document map[string]any) error {
	return validateFutureJSONShape(document, futureContractDocumentShape(), "$.")
}

func futureContractDocumentShape() *futureJSONShape {
	stringList := func(length int) *futureJSONShape {
		items := make([]*futureJSONShape, length)
		for index := range items {
			items[index] = futureShape(futureJSONString)
		}
		return futureArrayShape(items...)
	}
	requestHeaders := func(withTicket bool) *futureJSONShape {
		fields := map[string]*futureJSONShape{"Idempotency-Key": futureShape(futureJSONString)}
		if withTicket {
			fields["X-Action-Ticket"] = futureShape(futureJSONString)
		}
		return futureObjectShape(fields)
	}
	responseHeaders := func(withRetryAfter bool) *futureJSONShape {
		fields := map[string]*futureJSONShape{
			"Cache-Control": futureShape(futureJSONString),
			"X-Request-ID":  futureShape(futureJSONString),
		}
		if withRetryAfter {
			fields["Retry-After"] = futureShape(futureJSONString)
		}
		return futureObjectShape(fields)
	}
	errorShape := func(withOperationRef bool) *futureJSONShape {
		fields := map[string]*futureJSONShape{
			"code":       futureShape(futureJSONString),
			"message":    futureShape(futureJSONString),
			"type":       futureShape(futureJSONString),
			"request_id": futureShape(futureJSONString),
		}
		if withOperationRef {
			fields["operation_ref"] = futureShape(futureJSONString)
		}
		return futureObjectShape(map[string]*futureJSONShape{"error": futureObjectShape(fields)})
	}
	httpStatusShape := func(withRetryHeader bool, retryRange *futureJSONShape) *futureJSONShape {
		fields := map[string]*futureJSONShape{
			"status":                    futureShape(futureJSONInteger),
			"category":                  futureShape(futureJSONString),
			"retry_after_seconds_range": retryRange,
		}
		if withRetryHeader {
			fields["retry_after_header"] = futureShape(futureJSONString)
		}
		return futureObjectShape(fields)
	}

	nullRange := futureShape(futureJSONNull)
	return futureObjectShape(map[string]*futureJSONShape{
		"status":                 futureShape(futureJSONString),
		"production_observation": futureShape(futureJSONInteger),
		"implementation": futureObjectShape(map[string]*futureJSONShape{
			"handler_exists":         futureShape(futureJSONBool),
			"backend_route_exists":   futureShape(futureJSONBool),
			"frontend_client_exists": futureShape(futureJSONBool),
		}),
		"endpoints": futureObjectShape(map[string]*futureJSONShape{
			"issue": futureObjectShape(map[string]*futureJSONShape{
				"method":           futureShape(futureJSONString),
				"path":             futureShape(futureJSONString),
				"request_headers":  requestHeaders(true),
				"response_headers": responseHeaders(false),
				"request_example": futureObjectShape(map[string]*futureJSONShape{
					"action": futureShape(futureJSONString),
					"intent": futureObjectShape(map[string]*futureJSONShape{
						"target_guid":           futureShape(futureJSONString),
						"expected_auth_version": futureShape(futureJSONInteger),
						"reason":                futureShape(futureJSONString),
					}),
					"current_password": futureShape(futureJSONString),
				}),
				"response_status": futureShape(futureJSONInteger),
				"response_example": futureObjectShape(map[string]*futureJSONShape{
					"ticket":     futureShape(futureJSONString),
					"expires_at": futureShape(futureJSONInteger),
				}),
				"post_replay_count": futureShape(futureJSONInteger),
			}),
			"business_posts": futureObjectShape(map[string]*futureJSONShape{
				"method":                       futureShape(futureJSONString),
				"path_rule":                    futureShape(futureJSONString),
				"generic_execute_route":        futureShape(futureJSONBool),
				"currently_unregistered_paths": stringList(4),
				"request_headers":              requestHeaders(true),
				"response_headers":             responseHeaders(false),
				"success_semantics":            futureShape(futureJSONString),
				"post_replay_count":            futureShape(futureJSONInteger),
			}),
			"query": futureObjectShape(map[string]*futureJSONShape{
				"method":           futureShape(futureJSONString),
				"path":             futureShape(futureJSONString),
				"example_path":     futureShape(futureJSONString),
				"request_headers":  requestHeaders(false),
				"response_headers": responseHeaders(true),
				"processing_response": futureObjectShape(map[string]*futureJSONShape{
					"operation_ref": futureShape(futureJSONString),
					"scope":         futureShape(futureJSONString),
					"status":        futureShape(futureJSONString),
					"finished_at":   futureNullableShape(futureJSONInteger),
					"failure_code":  futureNullableShape(futureJSONString),
				}),
				"get_replay_after_refresh":    futureShape(futureJSONInteger),
				"requires_exact_adapter":      futureShape(futureJSONBool),
				"requires_original_scope_key": futureShape(futureJSONBool),
			}),
		}),
		"operation_statuses": stringList(4),
		"failure_codes":      stringList(5),
		"error_contract": futureObjectShape(map[string]*futureJSONShape{
			"envelope_example":       errorShape(false),
			"commit_unknown_example": errorShape(true),
			"required_fields":        stringList(4),
			"fixed_message":          futureShape(futureJSONString),
			"fixed_type":             futureShape(futureJSONString),
			"prohibited_fields":      stringList(6),
			"operation_ref_rule":     futureShape(futureJSONString),
			"http_statuses": futureArrayShape(
				httpStatusShape(false, nullRange),
				httpStatusShape(false, futureShape(futureJSONNull)),
				httpStatusShape(false, futureShape(futureJSONNull)),
				httpStatusShape(false, futureShape(futureJSONNull)),
				httpStatusShape(false, futureShape(futureJSONNull)),
				httpStatusShape(false, futureShape(futureJSONNull)),
				httpStatusShape(false, futureShape(futureJSONNull)),
				httpStatusShape(true, futureObjectShape(map[string]*futureJSONShape{
					"minimum": futureShape(futureJSONInteger),
					"maximum": futureNullableShape(futureJSONInteger),
				})),
				httpStatusShape(false, futureShape(futureJSONNull)),
			),
		}),
		"frontend": futureObjectShape(map[string]*futureJSONShape{
			"memory_only":                 stringList(3),
			"storage_prohibitions":        stringList(5),
			"post_replay_count":           futureShape(futureJSONInteger),
			"query_get_after_refresh_max": futureShape(futureJSONInteger),
			"unload_recovery":             futureShape(futureJSONBool),
		}),
		"acceptance": futureObjectShape(map[string]*futureJSONShape{
			"internal_foundation_is_http_acceptance": futureShape(futureJSONBool),
			"contract_callable":                      futureShape(futureJSONBool),
			"contract_accepted":                      futureShape(futureJSONBool),
			"statement":                              futureShape(futureJSONString),
		}),
	})
}

func futureShape(kind futureJSONKind) *futureJSONShape {
	return &futureJSONShape{kind: kind}
}

func futureNullableShape(kind futureJSONKind) *futureJSONShape {
	return &futureJSONShape{kind: kind, nullable: true}
}

func futureObjectShape(fields map[string]*futureJSONShape) *futureJSONShape {
	return &futureJSONShape{kind: futureJSONObject, fields: fields}
}

func futureArrayShape(items ...*futureJSONShape) *futureJSONShape {
	return &futureJSONShape{kind: futureJSONArray, items: items}
}

func validateFutureJSONShape(value any, shape *futureJSONShape, path string) error {
	if value == nil {
		if shape.nullable || shape.kind == futureJSONNull {
			return nil
		}
		return fmt.Errorf("%s must not be null", strings.TrimSuffix(path, "."))
	}
	if shape.kind == futureJSONNull {
		return fmt.Errorf("%s must be null", strings.TrimSuffix(path, "."))
	}
	switch shape.kind {
	case futureJSONObject:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be object, got %T", strings.TrimSuffix(path, "."), value)
		}
		if len(object) != len(shape.fields) {
			return fmt.Errorf("%s object key count=%d, want %d", strings.TrimSuffix(path, "."), len(object), len(shape.fields))
		}
		for key, childShape := range shape.fields {
			child, exists := object[key]
			if !exists {
				return fmt.Errorf("%s%s is missing", path, key)
			}
			if err := validateFutureJSONShape(child, childShape, path+key+"."); err != nil {
				return err
			}
		}
		for key := range object {
			if _, exists := shape.fields[key]; !exists {
				return fmt.Errorf("%s%s is unknown", path, key)
			}
		}
		return nil
	case futureJSONArray:
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be array, got %T", strings.TrimSuffix(path, "."), value)
		}
		if len(array) != len(shape.items) {
			return fmt.Errorf("%s array length=%d, want %d", strings.TrimSuffix(path, "."), len(array), len(shape.items))
		}
		for index, childShape := range shape.items {
			if err := validateFutureJSONShape(array[index], childShape, fmt.Sprintf("%s[%d].", path, index)); err != nil {
				return err
			}
		}
		return nil
	case futureJSONString:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be string, got %T", strings.TrimSuffix(path, "."), value)
		}
		return nil
	case futureJSONInteger:
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("%s must be integer, got %T", strings.TrimSuffix(path, "."), value)
		}
		if _, err := strconv.ParseInt(number.String(), 10, 64); err != nil {
			return fmt.Errorf("%s must be base-10 int64: %w", strings.TrimSuffix(path, "."), err)
		}
		return nil
	case futureJSONBool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be bool, got %T", strings.TrimSuffix(path, "."), value)
		}
		return nil
	default:
		return fmt.Errorf("%s has unsupported schema kind %d", strings.TrimSuffix(path, "."), shape.kind)
	}
}

func readFutureContractTree(t *testing.T) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(adminActionContractPath)
	if err != nil {
		t.Fatal(err)
	}
	return decodeFutureContractTree(t, contents)
}

func cloneFutureContractTree(t *testing.T, original map[string]any) map[string]any {
	t.Helper()
	contents, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	return decodeFutureContractTree(t, contents)
}

func decodeFutureContractTree(t *testing.T, contents []byte) map[string]any {
	t.Helper()
	if err := validateFutureContractRawTokens(contents); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("contract contains trailing JSON data: %v", err)
	}
	return document
}

func deleteFutureContractPath(path ...any) func(map[string]any) {
	return func(document map[string]any) {
		parent := futureContractPathParent(document, path)
		key, ok := path[len(path)-1].(string)
		if !ok {
			panic("delete path must end in an object key")
		}
		delete(parent.(map[string]any), key)
	}
}

func setFutureContractPath(value any, path ...any) func(map[string]any) {
	return func(document map[string]any) {
		parent := futureContractPathParent(document, path)
		switch key := path[len(path)-1].(type) {
		case string:
			parent.(map[string]any)[key] = value
		case int:
			parent.([]any)[key] = value
		default:
			panic("unsupported path component")
		}
	}
}

func futureContractPathParent(document map[string]any, path []any) any {
	if len(path) == 0 {
		panic("path must not be empty")
	}
	var current any = document
	for _, component := range path[:len(path)-1] {
		switch component := component.(type) {
		case string:
			current = current.(map[string]any)[component]
		case int:
			current = current.([]any)[component]
		default:
			panic("unsupported path component")
		}
	}
	return current
}

func assertExactJSON(t *testing.T, value any, want string) {
	t.Helper()
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func assertEqualJSON(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}
