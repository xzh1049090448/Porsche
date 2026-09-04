package dto

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"slices"
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
