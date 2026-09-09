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
		if !reflect.DeepEqual(sortedRouteKeys(route), []string{"body_schema", "method", "path", "path_schema", "query_schema", "request_headers", "response_headers", "response_schema", "role", "status"}) {
			t.Fatalf("route %s %s has unfrozen keys %v", route["method"], path, sortedRouteKeys(route))
		}
		for _, field := range []string{"path_schema", "query_schema", "body_schema", "response_schema"} {
			if _, ok := route[field].(string); !ok {
				t.Fatalf("route %s %s is missing its %s reference", route["method"], path, field)
			}
		}
	}

	publicContentPricingRequire(t, contract, "expected_revision", "mutation_requirements", "optimistic_concurrency_field")
	publicContentPricingRequire(t, contract, []any{
		"PATCH /admin/v2/public-models/{guid}",
		"POST /admin/v2/public-models/{guid}/activate",
		"POST /admin/v2/public-models/{guid}/deactivate",
		"DELETE /admin/v2/public-models/{guid}",
		"PUT /admin/v2/public-pricing/draft",
		"POST /admin/v2/public-pricing/validate",
		"POST /admin/v2/public-pricing/publish",
		"POST /admin/v2/public-pricing/releases/{guid}/restore",
		"PUT /admin/v2/public-content/draft",
		"POST /admin/v2/public-content/validate",
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
	publicContentPricingRequire(t, contract, "required_single_use_action_ticket", "admin_verified_action_request_headers", "X-Action-Ticket")
	for _, route := range []publicContentPricingRoute{
		{"POST", "/admin/v2/public-pricing/publish", "root"},
		{"POST", "/admin/v2/public-pricing/releases/{guid}/restore", "root"},
		{"POST", "/admin/v2/public-content/publish", "root"},
		{"POST", "/admin/v2/public-content/releases/{guid}/restore", "root"},
	} {
		publicContentPricingRequireRouteValue(t, contract, route, "admin_publish_request_headers", "request_headers")
	}
	publicContentPricingRequireRouteValue(t, contract, publicContentPricingRoute{"DELETE", "/admin/v2/public-models/{guid}", "root"}, "admin_verified_action_request_headers", "request_headers")
	preview := publicContentPricingRoute{"GET", "/admin/v2/public-content/preview", "root"}
	publicContentPricingRequireRouteValue(t, contract, preview, "admin_preview_response_headers", "response_headers")
	publicContentPricingRequire(t, contract, "no-store", "admin_preview_response_headers", "Cache-Control")
	publicContentPricingRequire(t, contract, "noindex_nofollow", "admin_preview_response_headers", "X-Robots-Tag")
	publicContentPricingRequire(t, contract, []any{"draft", "active", "inactive"}, "schemas", "AdminModelListRequest", "properties", "status", "enum")
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
	publicContentPricingRequire(t, contract, "one_exact_json_object_no_unknown_duplicate_or_trailing_fields", "body_rules", "mutation")
	publicContentPricingRequire(t, contract, []any{"action_ticket"}, "body_rules", "forbidden_body_fields")
	publicContentPricingRequire(t, contract, "400_invalid_public_content_pricing_request", "body_rules", "rejection")
	publicContentPricingRequire(t, contract, []any{
		"DELETE /admin/v2/public-models/{guid}",
		"POST /admin/v2/public-pricing/publish",
		"POST /admin/v2/public-pricing/releases/{guid}/restore",
		"POST /admin/v2/public-content/publish",
		"POST /admin/v2/public-content/releases/{guid}/restore",
	}, "mutation_requirements", "action_ticket_header_routes")
	publicContentPricingAssertSchemas(t, contract)
	publicContentPricingRequire(t, contract, "^[1-9][0-9]{0,18}$", "schemas", "GUIDRequest", "properties", "guid", "pattern")
	publicContentPricingRequire(t, contract, json.Number("1"), "schemas", "RevisionRequest", "properties", "expected_revision", "minimum")
	publicContentPricingRequire(t, contract, []any{"20", "50", "100"}, "schemas", "PaginationRequest", "properties", "page_size", "enum")
	publicContentPricingRequire(t, contract, "date-time-rfc3339-utc", "schemas", "Release", "properties", "created_at", "format")

	for _, forbidden := range []string{"credential_value", "api_key", "current_password", "internal_id", "database_id", "upstream_url"} {
		publicContentPricingForbidText(t, raw, forbidden)
	}
}

func publicContentPricingAssertSchemas(t *testing.T, contract map[string]any) {
	t.Helper()
	schemas, ok := contract["schemas"].(map[string]any)
	if !ok {
		t.Fatal("contract schemas must be an object")
	}
	for name, rawSchema := range schemas {
		publicContentPricingValidateSchema(t, schemas, name, rawSchema)
	}
	for _, route := range publicContentPricingRoutes(t, contract) {
		for _, field := range []string{"path_schema", "query_schema", "body_schema", "response_schema"} {
			name := route[field].(string)
			if _, ok := schemas[name]; !ok {
				t.Fatalf("route %s %s references undefined %s %q", route["method"], route["path"], field, name)
			}
		}
		for _, field := range []string{"request_headers", "response_headers"} {
			name := route[field].(string)
			if _, ok := contract[name].(map[string]any); !ok {
				t.Fatalf("route %s %s references undefined %s %q", route["method"], route["path"], field, name)
			}
		}
	}
	publicContentPricingRequire(t, contract, []any{"model_key", "display_name", "provider", "capabilities", "context_window", "input_price_usd_per_million_tokens", "output_price_usd_per_million_tokens", "price_visibility", "release_version"}, "schemas", "PublicModelVisible", "required")
	publicContentPricingRequire(t, contract, []any{"model_key", "display_name", "provider", "capabilities", "context_window", "price_visibility", "release_version"}, "schemas", "PublicModelRedacted", "required")
	for _, field := range []string{"input_price_usd_per_million_tokens", "output_price_usd_per_million_tokens"} {
		if _, exists := publicContentPricingSchemaProperties(t, contract, "PublicModelRedacted")[field]; exists {
			t.Fatalf("redacted model schema must omit %s", field)
		}
	}
	publicContentPricingRequire(t, contract, []any{"content_release_version", "price_release_version", "price_visibility"}, "schemas", "SiteReleaseResponse", "required")
	publicContentPricingRequire(t, contract, []any{"code", "message", "request_id"}, "schemas", "Error", "required")
	publicContentPricingRequire(t, contract, []any{"error"}, "schemas", "ErrorEnvelope", "required")
	publicContentPricingRequire(t, contract, "ErrorEnvelope", "errors", "envelope_schema")
	publicContentPricingRequireSchemaKeys(t, contract, "PriceDraft", "revision", "models", "currency", "unit")
	publicContentPricingRequireSchemaKeys(t, contract, "ValidationIssue", "field", "code")
	publicContentPricingRequireSchemaKeys(t, contract, "ImmutablePriceReleaseResponse", "release", "items")
	publicContentPricingRequireSchemaKeys(t, contract, "ImmutableContentReleaseResponse", "release", "content")
}

func publicContentPricingRequireSchemaKeys(t *testing.T, contract map[string]any, name string, want ...string) {
	t.Helper()
	got := sortedRouteKeys(publicContentPricingSchemaProperties(t, contract, name))
	for left := range want {
		for right := left + 1; right < len(want); right++ {
			if want[right] < want[left] {
				want[left], want[right] = want[right], want[left]
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema %s properties=%v, want %v", name, got, want)
	}
}

func publicContentPricingValidateSchema(t *testing.T, schemas map[string]any, name string, rawSchema any) {
	t.Helper()
	schema, ok := rawSchema.(map[string]any)
	if !ok {
		t.Fatalf("schema %s must be an object", name)
	}
	if _, ok := schema["type"].(string); !ok {
		t.Fatalf("schema %s must define type", name)
	}
	if reference, ok := schema["$ref"].(string); ok {
		if _, exists := schemas[reference]; !exists {
			t.Fatalf("schema %s references undefined schema %q", name, reference)
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for propertyName, rawProperty := range properties {
			if propertyName == "action_ticket" {
				t.Fatalf("schema %s must not place action_ticket in a request body", name)
			}
			publicContentPricingValidateSchema(t, schemas, name, rawProperty)
		}
	}
	if items, ok := schema["items"]; ok {
		publicContentPricingValidateSchema(t, schemas, name, items)
	}
	if alternatives, ok := schema["one_of"].([]any); ok {
		for _, rawAlternative := range alternatives {
			alternative, ok := rawAlternative.(string)
			if !ok || schemas[alternative] == nil {
				t.Fatalf("schema %s has undefined alternative %v", name, rawAlternative)
			}
		}
	}
	if required, ok := schema["required"].([]any); ok {
		properties, _ := schema["properties"].(map[string]any)
		for _, rawField := range required {
			field, ok := rawField.(string)
			if !ok || properties[field] == nil {
				t.Fatalf("schema %s required field %v is not a property", name, rawField)
			}
		}
	}
}

func publicContentPricingSchemaProperties(t *testing.T, contract map[string]any, name string) map[string]any {
	t.Helper()
	schemas := contract["schemas"].(map[string]any)
	properties, ok := schemas[name].(map[string]any)["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema %s properties must be an object", name)
	}
	return properties
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

func publicContentPricingRequireRouteValue(t *testing.T, contract map[string]any, expected publicContentPricingRoute, want any, path ...string) {
	t.Helper()
	for _, route := range publicContentPricingRoutes(t, contract) {
		if route["method"] != expected.method || route["path"] != expected.path || route["role"] != expected.role {
			continue
		}
		value := any(route)
		for _, part := range path {
			object, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("route %s %s path %v expected object before %s", expected.method, expected.path, path, part)
			}
			var exists bool
			value, exists = object[part]
			if !exists {
				t.Fatalf("route %s %s path %v missing %s", expected.method, expected.path, path, part)
			}
		}
		if !reflect.DeepEqual(value, want) {
			t.Fatalf("route %s %s path %v=%#v, want %#v", expected.method, expected.path, path, value, want)
		}
		return
	}
	t.Fatalf("route %s %s role=%s not found", expected.method, expected.path, expected.role)
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

func sortedRouteKeys(route map[string]any) []string {
	keys := make([]string, 0, len(route))
	for key := range route {
		keys = append(keys, key)
	}
	for left := range keys {
		for right := left + 1; right < len(keys); right++ {
			if keys[right] < keys[left] {
				keys[left], keys[right] = keys[right], keys[left]
			}
		}
	}
	return keys
}
