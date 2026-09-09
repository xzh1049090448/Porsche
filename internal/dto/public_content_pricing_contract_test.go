package dto

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

const publicContentPricingContractPath = "../../docs/agents/contracts/public-content-pricing-v1.json"

func TestPublicContentPricingContract(t *testing.T) {
	raw, err := os.ReadFile(publicContentPricingContractPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateContractTokens(raw); err != nil {
		t.Fatalf("contract token stream: %v", err)
	}

	var contract map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&contract); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("contract contains a trailing JSON value: %v", err)
	}

	publicContentPricingRequire(t, contract, "v1", "version")
	publicContentPricingRequire(t, contract, "USD", "pricing", "currency")
	publicContentPricingRequire(t, contract, "million_tokens", "pricing", "unit")
	publicContentPricingRequire(t, contract, []any{"input", "output"}, "pricing", "components")
	publicContentPricingRequire(t, contract, "decimal_string", "pricing", "wire_type")
	publicContentPricingRequire(t, contract, "12.34567890", "pricing", "safe_examples", "input_price_usd_per_million_tokens")
	publicContentPricingRequire(t, contract, "3.21000000", "pricing", "safe_examples", "output_price_usd_per_million_tokens")
	publicContentPricingRequire(t, contract, "references_only_no_automatic_charge", "pricing", "billing_semantics")
	publicContentPricingForbidText(t, raw, "per_request")
	publicContentPricingForbidText(t, raw, "price_per_request")

	publicRoutes := []publicContentPricingRoute{
		{"GET", "/api/v1/public/site", "anonymous"},
		{"GET", "/api/v1/public/home", "anonymous"},
		{"GET", "/api/v1/public/models", "anonymous"},
		{"GET", "/api/v1/public/models/{modelKey}", "anonymous"},
		{"GET", "/api/v1/public/pages/about", "anonymous"},
		{"GET", "/api/v1/public/pages/terms", "anonymous"},
		{"GET", "/api/v1/public/pages/privacy", "anonymous"},
	}
	adminRoutes := []publicContentPricingRoute{
		{"GET", "/admin/v2/public-models", "root"},
		{"POST", "/admin/v2/public-models", "root"},
		{"GET", "/admin/v2/public-models/{guid}", "root"},
		{"PATCH", "/admin/v2/public-models/{guid}", "root"},
		{"POST", "/admin/v2/public-models/{guid}/activate", "root"},
		{"POST", "/admin/v2/public-models/{guid}/deactivate", "root"},
		{"DELETE", "/admin/v2/public-models/{guid}", "root"},
		{"GET", "/admin/v2/public-models/missing", "root"},
		{"POST", "/admin/v2/public-models/sync", "root"},
		{"GET", "/admin/v2/public-pricing/draft", "root"},
		{"PUT", "/admin/v2/public-pricing/draft", "root"},
		{"POST", "/admin/v2/public-pricing/validate", "root"},
		{"POST", "/admin/v2/public-pricing/publish", "root"},
		{"GET", "/admin/v2/public-pricing/releases", "root"},
		{"GET", "/admin/v2/public-pricing/releases/{guid}", "root"},
		{"POST", "/admin/v2/public-pricing/releases/{guid}/restore", "root"},
		{"GET", "/admin/v2/notifications", "root"},
		{"GET", "/admin/v2/notifications/unread-count", "root"},
		{"POST", "/admin/v2/notifications/{guid}/read", "root"},
		{"POST", "/admin/v2/notifications/{guid}/acknowledge", "root"},
		{"GET", "/admin/v2/public-content/draft", "root"},
		{"PUT", "/admin/v2/public-content/draft", "root"},
		{"POST", "/admin/v2/public-content/validate", "root"},
		{"GET", "/admin/v2/public-content/preview", "root"},
		{"POST", "/admin/v2/public-content/publish", "root"},
		{"GET", "/admin/v2/public-content/releases", "root"},
		{"GET", "/admin/v2/public-content/releases/{guid}", "root"},
		{"POST", "/admin/v2/public-content/releases/{guid}/restore", "root"},
	}
	publicContentPricingRequireRoutes(t, contract, append(publicRoutes, adminRoutes...))

	for _, route := range publicContentPricingRoutes(t, contract) {
		path := route["path"].(string)
		if strings.Contains(path, "/deleted") {
			t.Fatalf("deleted-list route is forbidden: %s", path)
		}
		if route["method"] != "GET" && route["role"] != "root" {
			t.Fatalf("mutation %s %s must be Root-only", route["method"], path)
		}
		if _, ok := route["request"]; !ok {
			t.Fatalf("route %s %s is missing its request DTO", route["method"], path)
		}
		if _, ok := route["response"]; !ok {
			t.Fatalf("route %s %s is missing its response DTO", route["method"], path)
		}
	}

	publicContentPricingRequire(t, contract, "expected_revision", "mutation_requirements", "optimistic_concurrency_field")
	publicContentPricingRequire(t, contract, []any{
		"PATCH /admin/v2/public-models/{guid}",
		"POST /admin/v2/public-models/{guid}/activate",
		"POST /admin/v2/public-models/{guid}/deactivate",
		"DELETE /admin/v2/public-models/{guid}",
		"PUT /admin/v2/public-pricing/draft",
		"POST /admin/v2/public-pricing/publish",
		"POST /admin/v2/public-pricing/releases/{guid}/restore",
		"PUT /admin/v2/public-content/draft",
		"POST /admin/v2/public-content/publish",
		"POST /admin/v2/public-content/releases/{guid}/restore",
	}, "mutation_requirements", "expected_revision_routes")
	publicContentPricingRequire(t, contract, []any{
		"POST /admin/v2/public-pricing/publish",
		"POST /admin/v2/public-pricing/releases/{guid}/restore",
		"POST /admin/v2/public-content/publish",
		"POST /admin/v2/public-content/releases/{guid}/restore",
	}, "mutation_requirements", "idempotency_key_routes")

	publicContentPricingRequire(t, contract, "404", "model_lookup_semantics", "unknown_or_never_published")
	publicContentPricingRequire(t, contract, "410", "model_lookup_semantics", "previously_published_inactive_or_deleted")
	publicContentPricingRequire(t, contract, "required", "public_response_headers", "ETag")
	publicContentPricingRequire(t, contract, "required_release_version", "public_response_headers", "X-Public-Release-Version")
	publicContentPricingRequire(t, contract, "no-store", "admin_response_headers", "Cache-Control")
	publicContentPricingRequire(t, contract, "required_authenticated_root_session_or_bearer", "admin_request_headers", "Authorization")
	publicContentPricingRequire(t, contract, "required_exactly_once_unique_per_root_and_operation", "admin_publish_request_headers", "Idempotency-Key")
	publicContentPricingRequire(t, contract, "required_single_use_action_ticket", "admin_publish_request_headers", "X-Action-Ticket")
	publicContentPricingRequire(t, contract, []any{
		"published_price_below_upstream",
		"upstream_missing",
		"automatic_inactivation",
		"upstream_reappearance",
		"catalog_sync_failure",
		"price_not_comparable",
		"renderer_failure",
	}, "notifications", "types")
	publicContentPricingRequire(t, contract, "in_app_only", "notifications", "delivery")

	for _, forbidden := range []string{"credential_value", "api_key", "current_password", "internal_id", "database_id", "upstream_url"} {
		publicContentPricingForbidText(t, raw, forbidden)
	}
}

type publicContentPricingRoute struct {
	method string
	path   string
	role   string
}

func publicContentPricingRequireRoutes(t *testing.T, contract map[string]any, want []publicContentPricingRoute) {
	t.Helper()
	routes := publicContentPricingRoutes(t, contract)
	if len(routes) != len(want) {
		t.Fatalf("route count=%d, want %d", len(routes), len(want))
	}
	for _, expected := range want {
		matched := false
		for _, route := range routes {
			if route["method"] == expected.method && route["path"] == expected.path && route["role"] == expected.role {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("missing contract route %s %s role=%s", expected.method, expected.path, expected.role)
		}
	}
}

func publicContentPricingRoutes(t *testing.T, contract map[string]any) []map[string]any {
	t.Helper()
	rawRoutes, ok := contract["routes"].([]any)
	if !ok {
		t.Fatal("contract routes must be an array")
	}
	routes := make([]map[string]any, 0, len(rawRoutes))
	for index, rawRoute := range rawRoutes {
		route, ok := rawRoute.(map[string]any)
		if !ok {
			t.Fatalf("route %d must be an object", index)
		}
		for _, field := range []string{"method", "path", "role"} {
			if _, ok := route[field].(string); !ok {
				t.Fatalf("route %d field %s must be a string", index, field)
			}
		}
		routes = append(routes, route)
	}
	return routes
}

func publicContentPricingRequire(t *testing.T, root map[string]any, want any, path ...string) {
	t.Helper()
	value := any(root)
	for _, part := range path {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("path %v expected object before %s", path, part)
		}
		var exists bool
		value, exists = object[part]
		if !exists {
			t.Fatalf("path %v missing %s", path, part)
		}
	}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("path %v=%#v, want %#v", path, value, want)
	}
}

func publicContentPricingForbidText(t *testing.T, raw []byte, forbidden string) {
	t.Helper()
	if strings.Contains(strings.ToLower(string(raw)), forbidden) {
		t.Fatalf("contract contains forbidden text %q", forbidden)
	}
}
