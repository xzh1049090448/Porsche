# Slash-Qualified Model ID Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely support upstream model IDs containing organization/path separators such as `zai-org/glm-5.1`, while returning empty model catalogs as JSON arrays and retaining all existing ACL guarantees.

**Architecture:** Keep the upstream model ID as the sole logical ID: no database mapping or migration is needed. Extend the white-label validator to permit safe slash-separated segments, keep upstream detail URL construction as one escaped segment, and add query-parameter detail endpoints so Gin never parses a slash model ID as a route path. Platform, Gateway, Admin health, and the frontend adapter all call the same service and authorization gates.

**Tech Stack:** Go 1.22, Gin, GORM/MySQL 8, `net/url`, Go `httptest`, Vue 3/Vite, Node test runner.

---

## Files and responsibilities

- Modify: `internal/whitelabel/types.go` — validate slash-qualified IDs and return non-nil cloned model arrays.
- Modify: `internal/whitelabel/service_test.go`, `internal/whitelabel/types_test.go` — unit-test safe IDs, upstream escaping, empty catalogs, ACL and no-upstream behavior.
- Modify: `internal/handler/platform.go`, `internal/handler/gateway_tokens.go`, `internal/handler/health.go` — register secure query-detail/health endpoints while retaining legacy safe-ID endpoints.
- Modify: `internal/handler/platform_whitelabel_test.go`, `internal/handler/gateway_whitelabel_test.go` — HTTP contract, ACL, route, and upstream-call assertions.
- Modify: `Porsche-Web/src/api/platform.js` — request the platform detail endpoint via `URLSearchParams`.
- Modify: `Porsche-Web/src/api/platform.test.js` (create if absent) — assert slash IDs are sent as query parameters and encoded once.
- Modify: `feature_list.json`, `progress.md` — record only verified completion evidence after all code and smoke checks pass.

### Task 1: Define safe model-ID and empty-list primitives

**Files:**
- Modify: `internal/whitelabel/types.go:279-290`
- Test: `internal/whitelabel/types_test.go`

- [ ] **Step 1: Write failing validation and JSON-shape tests**

Add tests that accept normal and slash-qualified IDs, reject routing/control syntax, and assert cloning an empty list is non-nil:

```go
func TestValidModelIDAllowsSafeSlashSegments(t *testing.T) {
	for _, id := range []string{"glm-5.1", "zai-org/glm-5.1", "deepseek/deepseek-v4-pro", "a/b/c"} {
		if !validModelID(id) { t.Fatalf("expected valid ID %q", id) }
	}
}

func TestValidModelIDRejectsUnsafeSlashSyntax(t *testing.T) {
	for _, id := range []string{"/model", "org/", "org//model", "./model", "org/../model", "org\\model", "org/model?x=1", "org/model#x", "org%2Fmodel", "org/ model"} {
		if validModelID(id) { t.Fatalf("expected invalid ID %q", id) }
	}
}

func TestCloneModelsReturnsEmptyJSONArrayShape(t *testing.T) {
	cloned := cloneModels(nil)
	if cloned == nil || len(cloned) != 0 { t.Fatalf("want non-nil empty slice, got %#v", cloned) }
}
```

- [ ] **Step 2: Run the focused tests and confirm RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -run 'Test(ValidModelID|CloneModelsReturnsEmpty)' -count=1`

Expected: FAIL because slash IDs are currently rejected and `cloneModels(nil)` returns nil.

- [ ] **Step 3: Implement segment-based validation and non-nil cloning**

Replace the current broad slash rejection with validation that still rejects path-changing syntax:

```go
func validModelID(id string) bool {
	if id == "" || len(id) > 256 || id != strings.TrimSpace(id) || !utf8.ValidString(id) {
		return false
	}
	for _, segment := range strings.Split(id, "/") {
		if segment == "" || segment == "." || segment == ".." { return false }
		if strings.IndexFunc(segment, func(r rune) bool {
			return r < 0x20 || r == 0x7f || unicode.IsSpace(r) || strings.ContainsRune("\\?#%", r)
		}) >= 0 { return false }
	}
	return true
}

func cloneModels(in []Model) []Model {
	out := make([]Model, len(in))
	copy(out, in)
	return out
}
```

Import `unicode`. Do not normalize or rewrite the model ID: ACL and upstream calls must retain the exact approved string.

- [ ] **Step 4: Run focused tests and package verification**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the primitive change**

```bash
git add internal/whitelabel/types.go internal/whitelabel/types_test.go
git commit -m "fix: allow safe slash-qualified model ids"
```

### Task 2: Preserve escaped upstream detail behavior and empty catalog responses

**Files:**
- Modify: `internal/whitelabel/service_test.go`
- Modify: `internal/whitelabel/service.go` only if focused tests demonstrate a defect

- [ ] **Step 1: Write service regression tests**

Use an `httptest.Server` to assert that a slash ID reaches the upstream as a single escaped path segment, catalog filtering retains it, and an empty catalog does not become null:

```go
func TestGetModelEscapesSlashQualifiedIDAsOneSegment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/openai/v1/models/zai-org%2Fglm-5.1" { t.Fatalf("path=%q", r.URL.EscapedPath()) }
		io.WriteString(w, `{"id":"zai-org/glm-5.1","object":"model"}`)
	}))
	defer server.Close()
	// Construct a service pointed at server.URL+"/openai/v1", prime its catalog with the same ID, then call GetModel.
}

func TestListModelsEmptyCatalogReturnsNonNilData(t *testing.T) {
	// Upstream returns {"data":[]}; assert catalog.Data != nil and len(catalog.Data)==0.
}
```

Also test `GetModel(ctx, "org/../model", nil)` returns `CodeModelUnavailable` without making an HTTP request, and a configured/ACL-denied slash ID makes zero detail calls.

- [ ] **Step 2: Run tests and confirm RED/behavior**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel -run 'Test(GetModelEscapesSlashQualifiedIDAsOneSegment|ListModelsEmptyCatalogReturnsNonNilData)' -count=1`

Expected: RED until Task 1 validation is present; after Task 1, PASS unless service URL construction or cache cloning needs adjustment.

- [ ] **Step 3: Apply the smallest service correction if required**

Retain this construction; do not concatenate unescaped path components:

```go
request, err := s.newRequest(ctx, s.baseURL+"/models/"+url.PathEscape(id))
```

If list construction can still expose a nil slice, normalize at the service boundary:

```go
return Catalog{Data: cloneModels(filtered), CatalogStale: stale}, nil
```

- [ ] **Step 4: Run white-label tests including race coverage**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test -race ./internal/whitelabel -count=1`

Expected: PASS.

- [ ] **Step 5: Commit service regressions**

```bash
git add internal/whitelabel/service.go internal/whitelabel/service_test.go
git commit -m "test: cover slash model detail catalog behavior"
```

### Task 3: Add secure query-detail endpoints and retain legacy simple-ID routes

**Files:**
- Modify: `internal/handler/platform.go:21-50`
- Modify: `internal/handler/gateway_tokens.go:26-59`
- Modify: `internal/handler/health.go:37-90`
- Test: `internal/handler/platform_whitelabel_test.go`
- Test: `internal/handler/gateway_whitelabel_test.go`

- [ ] **Step 1: Write failing HTTP contract tests**

Add tests for platform, gateway, and admin endpoint behavior. Use a slash model such as `zai-org/glm-5.1` and upstream call counters:

```go
req := httptest.NewRequest(http.MethodGet,
	"/api/v1/platform/models/detail?id="+url.QueryEscape("zai-org/glm-5.1"), nil)
// attach valid platform auth; expect 200 and JSON id exactly "zai-org/glm-5.1".

req = httptest.NewRequest(http.MethodGet,
	"/v1/models/detail?id="+url.QueryEscape("zai-org/glm-5.1"), nil)
req.Header.Set("Authorization", "Bearer "+gatewayToken)
// expect 200.

req = httptest.NewRequest(http.MethodGet, "/v1/models/detail?id=org%2F..%2Fsecret", nil)
// expect same public unavailable response as missing/unauthorized and detailCalls == 0.
```

Add one compatibility test for `GET /api/v1/platform/models/model-a` and `GET /v1/models/model-a`, and one health test for `POST /admin/models/health-check?id=...` that proves concurrent same-model requests still return `health_check_in_progress`.

- [ ] **Step 2: Run focused handler tests and confirm RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler -run 'Test(Platform|Gateway|Admin).*Slash|Test.*ModelsDetail' -count=1`

Expected: FAIL with 404 because query-detail routes are absent.

- [ ] **Step 3: Factor an ID reader and add query routes**

Add a local helper in `internal/handler` which reads only a required query ID and leaves service validation/ACL as the source of truth. Each route must select its existing Platform or Gateway public-error writer; the helper must not choose a response envelope itself:

```go
func modelIDQuery(c *gin.Context) string {
	return c.Query("id")
}
```

Register query endpoints before the legacy `:id` endpoints:

```go
g.GET("/models/detail", func(c *gin.Context) {
	id := modelIDQuery(c)
	model, err := state.WhiteLabel.GetModel(c.Request.Context(), id, middleware.CurrentUser(c).AllowedModels)
	if err != nil { platformWhiteLabelError(c, err); return }
	c.JSON(http.StatusOK, model)
})
```

Implement the equivalent `/v1/models/detail` flow using `authenticateGatewayToken` and `gatewayWhiteLabelError`. Add `POST /admin/models/health-check?id=...` with the existing 10-second, per-model lock, projected response, audit, and `SaveModelHealthCheck` logic. Leave the legacy `:id` endpoints registered for slash-free clients; both paths must call the same service method.

- [ ] **Step 4: Verify authorization and no-enumeration behavior**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler -count=1`

Expected: PASS. Review tests to show missing, malformed, catalog-absent, and ACL-denied IDs use the same public unavailable response and cause no detail upstream call.

- [ ] **Step 5: Commit routes and HTTP regressions**

```bash
git add internal/handler/platform.go internal/handler/gateway_tokens.go internal/handler/health.go internal/handler/platform_whitelabel_test.go internal/handler/gateway_whitelabel_test.go
git commit -m "feat: add query detail endpoints for slash model ids"
```

### Task 4: Update the frontend detail adapter without double encoding

**Files:**
- Modify: `Porsche-Web/src/api/platform.js:18-21`
- Create: `Porsche-Web/src/api/platform.test.js`

- [ ] **Step 1: Write a failing adapter test**

Mock `request.get`, call the adapter with a slash ID, and assert the exact query form:

```js
test('getModel sends a slash-qualified model id as one query parameter', async () => {
  request.get = async (url) => {
    assert.equal(url, '/api/v1/platform/models/detail?id=zai-org%2Fglm-5.1')
    return { id: 'zai-org/glm-5.1', object: 'model' }
  }
  const model = await getModel('zai-org/glm-5.1')
  assert.equal(model.id, 'zai-org/glm-5.1')
})
```

Add an invalid/non-string ID test that asserts no request is made and the result is `null`.

- [ ] **Step 2: Run the adapter test and confirm RED**

Run: `cd /Users/xuzhihao/code/Porsche-Web && node --test src/api/platform.test.js`

Expected: FAIL because the adapter currently constructs `/models/${encodeURIComponent(id)}`.

- [ ] **Step 3: Implement query construction with `URLSearchParams`**

Use the browser encoder exactly once:

```js
export async function getModel(id) {
  if (typeof id !== 'string' || id.trim() === '') return null
  const params = new URLSearchParams({ id })
  const res = await request.get(`${PREFIX}/models/detail?${params.toString()}`)
  return catalogModels({ data: [res] })[0] || null
}
```

Do not add a static model fallback and do not decode/re-encode the ID in stores.

- [ ] **Step 4: Run frontend focused and full verification**

Run:

```bash
cd /Users/xuzhihao/code/Porsche-Web
npm test
npm run build
git diff --check
```

Expected: all commands exit 0; record existing Vite chunk warnings only if they remain warnings.

- [ ] **Step 5: Commit frontend adapter support**

```bash
git add src/api/platform.js src/api/platform.test.js
git commit -m "fix: request slash model details by query"
```

### Task 5: End-to-end regression, deployment smoke, and tracker evidence

**Files:**
- Modify: `feature_list.json`
- Modify: `progress.md`

- [ ] **Step 1: Run backend complete verification**

Run from `/Users/xuzhihao/code/Porsche`:

```bash
GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
git diff --check
```

Expected: all exit 0. If the local sandbox forbids `httptest` loopback binding, rerun the same Go test command in an approved local-loopback environment and record that constraint accurately.

- [ ] **Step 2: Perform real white-label smoke after deployment configuration is updated**

Update the server `.env` allowlist to exact upstream IDs, for example:

```dotenv
JIEKOU_ALLOWED_MODELS=zai-org/glm-5.1,deepseek/deepseek-v4-pro
```

After a normal application deployment, verify an authenticated platform user and a Gateway Token each can:

```bash
curl -fsS -H "Authorization: Bearer $PLATFORM_JWT" "$BASE_URL/api/v1/platform/models" | jq '.data | type'
curl -fsS -H "Authorization: Bearer $PLATFORM_JWT" --get --data-urlencode 'id=zai-org/glm-5.1' "$BASE_URL/api/v1/platform/models/detail" | jq '.id'
curl -fsS -H "Authorization: Bearer $GW_TOKEN" --get --data-urlencode 'id=zai-org/glm-5.1' "$BASE_URL/v1/models/detail" | jq '.id'
```

Then run one authorized non-stream Chat and one SSE Chat with a slash-qualified ID. Do not print tokens, upstream API keys, or full response prompts in `progress.md`.

- [ ] **Step 3: Record only proven evidence and update issue state**

When all tests and real smoke checks pass, mark `go-004` `passing` only if all its stated white-label acceptance work is complete; otherwise retain `in_progress` and record exactly which smoke check remains. Add the Issue #2 code/test/smoke evidence to `progress.md`. Close GitHub Issue #2 only after the deployed catalog, detail, non-stream, and SSE checks have succeeded.

- [ ] **Step 4: Commit tracker evidence separately**

```bash
git add feature_list.json progress.md
git commit -m "docs: record slash model id verification"
```

## Plan self-review

- **Spec coverage:** Tasks 1–2 cover safe slash IDs, escaped upstream requests, ACL gate behavior, and `data: []`; Task 3 covers Platform/Gateway/Admin HTTP compatibility and anti-enumeration; Task 4 covers browser query encoding; Task 5 covers back-end/front-end checks and real directory/detail/Chat/SSE smoke.
- **No placeholders:** Each code change has concrete route, validator, test, command, and expected result. Deployment smoke deliberately remains an explicit externally configured step rather than a fabricated local result.
- **Type consistency:** Model IDs remain `string` across validator, ACL, URL query, service, handler, and frontend. Query endpoints call `WhiteLabelService.GetModel`, preserving the existing authorization/cached-catalog path.
