# Public Content and Pricing Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver Root-only public-model administration, immutable USD/token price snapshots, safe public-content publication, persistent Root alerts, and a five-minute white-label monitor.

**Architecture:** Add forward-only MySQL migrations and focused domain services behind `/admin/v2` and `/api/v1/public`. Reuse the current white-label catalog, Root middleware, action-verification service, Redis, audit service, clock/GUID facilities, and router patterns. Snapshot pointers advance transactionally; background work uses a database lease and never treats stale or failed catalogs as removal evidence.

**Tech Stack:** Go 1.22, Gin, GORM, MySQL 8, Redis 7, existing white-label client and action-security primitives.

---

### Task 1: Freeze the public pricing contract

**Files:**
- Create: `docs/agents/contracts/public-content-pricing-v1.json`
- Create: `internal/dto/public_content_pricing_contract_test.go`
- Modify: `interface-contract.json`

- [ ] **Step 1: Write the failing contract test**

Define a table that requires every approved route, fixed `USD`/`million_tokens`, input/output-only prices, Root-only mutations, `expected_revision`, `Idempotency-Key`, 404/410 model semantics, ETag/version headers, notification types, and no deleted-list endpoint.

```go
func TestPublicContentPricingContract(t *testing.T) {
    raw := readContract(t, "../../docs/agents/contracts/public-content-pricing-v1.json")
    requireJSONPath(t, raw, "pricing.currency", "USD")
    requireJSONPath(t, raw, "pricing.unit", "million_tokens")
    forbidJSONText(t, raw, "per_request")
    requireRoute(t, raw, "DELETE", "/admin/v2/public-models/{guid}", "root")
    forbidRoute(t, raw, "/admin/v2/public-models/deleted")
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/dto -run TestPublicContentPricingContract -count=1`  
Expected: FAIL because the contract file does not exist.

- [ ] **Step 3: Add the exact contract and interface entries**

Document request/response DTOs, errors, cache headers, roles, idempotency, and example decimal strings without credentials or internal IDs.

- [ ] **Step 4: Run the test and verify GREEN**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/dto -run TestPublicContentPricingContract -count=1`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/agents/contracts/public-content-pricing-v1.json interface-contract.json internal/dto/public_content_pricing_contract_test.go
git commit -m "docs: freeze public pricing contract"
```

### Task 2: Add schema migrations 0012 and 0013

**Files:**
- Create: `internal/migration/sql/0012_public_content_pricing.up.sql`
- Create: `internal/migration/sql/0012_public_content_pricing.down.sql`
- Create: `internal/migration/sql/0013_public_price_draft_state.up.sql`
- Create: `internal/migration/sql/0013_public_price_draft_state.down.sql`
- Create: `internal/migration/public_content_pricing_test.go`
- Modify: `internal/migration/runner.go`
- Create: `internal/models/public_content_pricing.go`
- Create: `internal/models/public_content_pricing_test.go`

- [ ] **Step 1: Write RED tests for schema and model tags**

Require tables `public_model_configs`, `public_price_draft_state`, `public_price_snapshots`, `public_price_snapshot_items`, `public_publication_state`, `public_content_drafts`, `public_content_releases`, `upstream_model_observations`, `root_alerts`, `root_alert_receipts`, and `public_render_jobs`. The additive `0013` migration seeds the independent `pricing` draft revision singleton. Require decimal columns `DECIMAL(20,8)`, immutable identity uniqueness, revision/status indexes, foreign keys, audit columns, and down order.

- [ ] **Step 2: Run migration tests and verify RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/migration ./internal/models -run 'Public(Content|Pricing)' -count=1`  
Expected: FAIL because migrations 0012/0013 and models are absent.

- [ ] **Step 3: Implement migration and models**

Use integer enums for lifecycle and alert state, string decimals at DTO boundaries, `models.JSONMap` for bounded content payloads, and explicit table names. Never add automatic migration calls.

- [ ] **Step 4: Run migration unit tests**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/migration ./internal/models -run 'Public(Content|Pricing)' -count=1`  
Expected: PASS.

- [ ] **Step 5: Run real migration up/down in an isolated MySQL fixture**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/migration -run 'Test(PublicContentPricing|PublicPriceDraftState)MigrationRealMySQL' -count=1` with the task's explicit `TEST_DATABASE_URL`; the tests apply 0001-0013, inspect keys/types and the unique `pricing` singleton, safely run the independent 0013 down, require both its schema verifier and the global verifier to fail closed, then reapply 0013 and require both verifiers to pass. The existing 0012 down/reapply coverage remains separate.
Expected: ledger lists 0001 through 0013 with immutable checksums and an active singleton draft revision; no production database or `.env` is used.

- [ ] **Step 6: Commit**

```bash
git add internal/migration internal/models/public_content_pricing.go internal/models/public_content_pricing_test.go
git commit -m "feat: add public pricing schema"
```

### Task 3: Implement exact price and publication validation

**Files:**
- Create: `internal/publiccontent/decimal.go`
- Create: `internal/publiccontent/decimal_test.go`
- Create: `internal/publiccontent/validate.go`
- Create: `internal/publiccontent/validate_test.go`
- Create: `internal/publiccontent/sanitize.go`
- Create: `internal/publiccontent/sanitize_test.go`

- [ ] **Step 1: Write failing validation tests**

Cover canonical decimal parsing with at most eight fractional digits, non-negative bounded prices, fixed USD/million-token units, missing prices, stable `modelKey` syntax, duplicate upstream IDs, broken homepage references, pending legal review, `40+`/`100%`/`MIT`, scripts, event attributes, iframe/embed, `javascript:`/`data:` URLs, and remote images.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/publiccontent -count=1`  
Expected: compile failure because validation functions do not exist.

- [ ] **Step 3: Implement minimal validators and allowlist sanitizer**

Expose typed validation issues with stable field/code pairs. Preserve safe Markdown text; never fetch a URL during validation.

- [ ] **Step 4: Run GREEN and fuzz hostile URLs**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/publiccontent -count=1`  
Expected: PASS with no network access.

- [ ] **Step 5: Commit**

```bash
git add internal/publiccontent
git commit -m "feat: validate public pricing content"
```

### Task 4: Implement Root model administration service

**Files:**
- Create: `internal/service/public_model_admin.go`
- Create: `internal/service/public_model_admin_test.go`
- Create: `internal/service/public_model_admin_db_test.go`
- Create: `internal/dto/public_model.go`
- Create: `internal/dto/public_model_test.go`

- [ ] **Step 1: Write RED service tests**

Cover create only from a fresh accepted observation, list/search/filter/page, detail, revision-checked update, activate/inactivate, permanent soft delete, key reservation, deleted omission, and sanitized audit entries.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service ./internal/dto -run 'PublicModel' -count=1`  
Expected: compile failure for missing service/DTOs.

- [ ] **Step 3: Implement service and DTOs**

Use transactions for every mutation, lock the actor as active Root inside the transaction, select target rows `FOR UPDATE`, compare `expected_revision`, and write success audit in the same transaction.

- [ ] **Step 4: Run unit and isolated DB tests**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service ./internal/dto -run 'PublicModel' -count=1`  
Expected: PASS, zero fixture skip when explicit test URLs are set.

- [ ] **Step 5: Run race tests**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test -race ./internal/service -run 'PublicModel.*(Revision|Delete|Identity)' -count=1`  
Expected: PASS; exactly one concurrent revision wins.

- [ ] **Step 6: Commit**

```bash
git add internal/service/public_model_admin* internal/dto/public_model*
git commit -m "feat: manage public model pricing"
```

### Task 5: Implement immutable price snapshots

**Files:**
- Create: `internal/service/public_price_snapshot.go`
- Create: `internal/service/public_price_snapshot_test.go`
- Create: `internal/service/public_price_snapshot_db_test.go`

- [ ] **Step 1: Write RED snapshot tests**

Assert validation before publication, one immutable item per active model, atomic pointer advance, content hash stability, exact decimal preservation, idempotent retry, audit failure rollback, inactive/deleted exclusion, and restore-as-new-version.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'PublicPriceSnapshot' -count=1`  
Expected: compile failure.

- [ ] **Step 3: Implement snapshot transactions**

Lock publication state, verify draft revision and idempotency binding, insert immutable snapshot/items, enqueue a render job, audit, and update pointer in one transaction.

- [ ] **Step 4: Run GREEN and failure injection**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'PublicPriceSnapshot' -count=1`  
Expected: PASS; injected insert/audit/pointer failures leave the old pointer.

- [ ] **Step 5: Commit**

```bash
git add internal/service/public_price_snapshot*
git commit -m "feat: publish immutable price snapshots"
```

### Task 6: Implement content drafts and publication

**Files:**
- Create: `internal/service/public_content.go`
- Create: `internal/service/public_content_test.go`
- Create: `internal/service/public_content_db_test.go`
- Create: `internal/dto/public_content.go`
- Create: `internal/dto/public_content_test.go`

- [ ] **Step 1: Write RED tests**

Cover safe empty draft, optimistic save, preview, reviewed terms/privacy gate, model-reference agreement with the selected price snapshot, immutable release, idempotent publish, restore-as-new-release, and failed publish preserving current state.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service ./internal/dto -run 'PublicContent' -count=1`  
Expected: compile failure.

- [ ] **Step 3: Implement services and safe DTO projection**

Bind exact content and pricing releases in publication state. Preview uses draft projection; public projection uses only committed state.

- [ ] **Step 4: Run GREEN**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service ./internal/dto -run 'PublicContent' -count=1`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/public_content* internal/dto/public_content*
git commit -m "feat: publish safe public content"
```

### Task 7: Implement persistent Root alerts

**Files:**
- Create: `internal/service/root_alert.go`
- Create: `internal/service/root_alert_test.go`
- Create: `internal/service/root_alert_db_test.go`
- Create: `internal/dto/root_alert.go`

- [ ] **Step 1: Write RED tests**

Cover alert deduplication, occurrence updates, resolve/reopen, new-Root visibility, independent read/ack receipts, unread count, stable safe payloads, and `in_app` channel only.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'RootAlert' -count=1`  
Expected: compile failure.

- [ ] **Step 3: Implement alert service**

Use a unique active fingerprint and per-Root receipt upserts. Add an interface for future email delivery without SMTP settings or send calls.

- [ ] **Step 4: Run GREEN**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'RootAlert' -count=1`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/root_alert* internal/dto/root_alert.go
git commit -m "feat: add root pricing alerts"
```

### Task 8: Implement white-label monitor and automatic safety publication

**Files:**
- Create: `internal/service/upstream_price_monitor.go`
- Create: `internal/service/upstream_price_monitor_test.go`
- Create: `internal/service/upstream_price_monitor_db_test.go`
- Create: `internal/migration/sql/0014_upstream_monitor_lease.up.sql`
- Create: `internal/migration/sql/0014_upstream_monitor_lease.down.sql`
- Create: `internal/migration/upstream_monitor_lease.go`
- Create: `internal/migration/upstream_monitor_lease_test.go`
- Modify: `internal/migration/runner.go`
- Modify: `internal/models/public_content_pricing.go`
- Modify: `internal/whitelabel/service.go`
- Modify: `internal/whitelabel/types.go`
- Modify: `internal/app/state.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Write RED monitor tests with a fake clock/catalog**

Cover independent input/output comparisons, not-comparable alerts, five-minute scheduling, distributed lease, successful-complete-fresh three strikes, failure/stale/incomplete non-strikes, reappearance without activation, automatic inactivation, system audit, and safety snapshot failure behavior.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'UpstreamPriceMonitor' -count=1`  
Expected: compile failure.

- [ ] **Step 3: Expose a sanitized fresh-catalog observation method**

Return normalized ID, input/output price, fetched-at, completeness, and freshness without returning credentials or transport internals.

- [ ] **Step 4: Implement one monitor tick and lease**

Use a short database lease row with owner token/expiry, periodic renewal, and owner fencing before each mutation commit. A tick records observations, prunes sanitized observations older than 30 days in a bounded transactional batch, updates counters, and persists typed alerts through Task 7's transaction-aware seam. At the third valid absence it commits inactive state plus a durable safety-publication intent before the separately retryable safety snapshot attempt. The safety snapshot, rebound content release, render job, publication pointer, and resolution of that intent commit in one transaction; any failure retains the active intent for retry. Dynamic price reads filter inactive/deleted current configs, while the combined public projection returns a stable unavailable response until its immutable content/price pair is compatible with current lifecycle state.

- [ ] **Step 5: Add cancellable scheduler lifecycle**

Construct the monitor in `app.State`; start it from `cmd/server/main.go` with a five-minute ticker and stop on process context cancellation. Do not start background loops in unit-test constructors unless explicitly requested.

- [ ] **Step 6: Run GREEN and race tests**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/whitelabel ./internal/service ./internal/app -run '(CatalogObservation|UpstreamPriceMonitor)' -count=1`  
Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test -race ./internal/service -run 'UpstreamPriceMonitor.*Lease' -count=1`  
Expected: PASS; one concurrent owner mutates state.

- [ ] **Step 7: Commit**

```bash
git add internal/whitelabel internal/service/upstream_price_monitor* internal/app/state.go cmd/server/main.go
git commit -m "feat: monitor upstream public prices"
```

### Task 9: Register Root administration handlers

**Files:**
- Create: `internal/handler/public_model_admin.go`
- Create: `internal/handler/public_model_admin_test.go`
- Create: `internal/handler/public_pricing_admin.go`
- Create: `internal/handler/public_pricing_admin_test.go`
- Create: `internal/handler/public_content_admin.go`
- Create: `internal/handler/public_content_admin_test.go`
- Create: `internal/handler/root_alerts.go`
- Create: `internal/handler/root_alerts_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_test.go`

- [ ] **Step 1: Write RED route/auth/decode tests**

Require authenticated active Root, reject admin/user, reject unknown fields and malformed GUID/revision/decimal values, enforce body limits, no-store admin responses, action tickets for publish/restore/delete, and exact contract errors.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler ./internal/router -run '(PublicModel|PublicPricing|PublicContent|RootAlert)' -count=1`  
Expected: FAIL because routes are absent.

- [ ] **Step 3: Implement thin handlers and register static paths before parameters**

Decode strictly, project safe DTOs, and delegate transactions to services. Register `/missing` and `/sync` before `/:guid`.

- [ ] **Step 4: Run GREEN**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler ./internal/router -run '(PublicModel|PublicPricing|PublicContent|RootAlert)' -count=1`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/public_* internal/handler/root_alerts* internal/router
git commit -m "feat: expose root public pricing APIs"
```

### Task 10: Register public read handlers

**Files:**
- Create: `internal/service/public_catalog_read.go`
- Create: `internal/service/public_catalog_read_test.go`
- Create: `internal/handler/public_content.go`
- Create: `internal/handler/public_content_test.go`
- Modify: `internal/router/router.go`

- [ ] **Step 1: Write RED public boundary tests**

Cover anonymous site/home/pages/catalog/detail, published-only data, no internal IDs/upstream routes, missing price omission, disclaimer, 404/410, ETag/version headers, authenticated-only price redaction, and cache directives.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service ./internal/handler -run 'Public(Catalog|Read|Site|Home|Page)' -count=1`  
Expected: FAIL because public reads are absent.

- [ ] **Step 3: Implement snapshot-consistent reads**

Load one publication-state generation and exact referenced releases per request. Recheck generation or use one read transaction so mixed versions cannot escape.

- [ ] **Step 4: Run GREEN**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service ./internal/handler -run 'Public(Catalog|Read|Site|Home|Page)' -count=1`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/public_catalog_read* internal/handler/public_content* internal/router/router.go
git commit -m "feat: expose public content catalog"
```

### Task 11: Add render-job API and deployment health

**Files:**
- Create: `cmd/public-render-job/main.go`
- Create: `cmd/public-render-job/main_test.go`
- Create: `internal/service/public_render_job.go`
- Create: `internal/service/public_render_job_test.go`
- Modify: `internal/handler/health.go`

- [ ] **Step 1: Write RED tests**

Cover lease/complete/fail transitions, sanitized failures, retry bounds, current-generation lookup, and health output that reports renderer lag without exposing paths.

- [ ] **Step 2: Run RED**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./cmd/public-render-job ./internal/service -run 'PublicRender' -count=1`  
Expected: compile failure.

- [ ] **Step 3: Implement job service and narrow CLI/API surface**

The frontend renderer obtains/finishes jobs through authenticated local execution, never by reading production tables directly from browser code.

- [ ] **Step 4: Run GREEN**

Run: `GOCACHE=/private/tmp/porsche-go-build-cache go test ./cmd/public-render-job ./internal/service -run 'PublicRender' -count=1`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/public-render-job internal/service/public_render_job* internal/handler/health.go
git commit -m "feat: manage public render jobs"
```

### Task 12: Backend integration and evidence

**Files:**
- Modify: `feature_list.json`
- Modify: `progress.md`
- Create: `docs/superpowers/reports/2026-09-09-public-content-pricing-backend.md`

- [ ] **Step 1: Run focused real-fixture integration**

Apply 0001-0013 in isolated MySQL 8 and use isolated Redis 7. The migration fixture must exercise 0012 and 0013 down/reapply independently and finish with both exact schema verifiers plus the global verifier passing. Run model CRUD, snapshot, alert, monitor, public read, handler, and race suites with zero unexpected skip.

- [ ] **Step 2: Run full gates**

```bash
GOCACHE=/private/tmp/porsche-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go test -race ./internal/service ./internal/handler -run '(Public|RootAlert|UpstreamPrice)' -count=1
GOCACHE=/private/tmp/porsche-go-build-cache go build ./...
GOCACHE=/private/tmp/porsche-go-build-cache go vet ./...
gofmt -w internal/publiccontent/*.go internal/models/public_content_pricing.go internal/dto/public_*.go internal/dto/root_alert.go internal/service/public_*.go internal/service/root_alert*.go internal/service/upstream_price_monitor*.go internal/handler/public_*.go internal/handler/root_alerts*.go cmd/public-render-job/*.go
git diff --check
```

Expected: all gates PASS. Any unavailable real fixture is recorded as `BLOCKED_FIXTURE`, never converted into a pass.

- [ ] **Step 3: Record bounded status**

Update P03-P08 only when their exact backend evidence exists. Preserve safe-draft product-content blocking and deployment exclusions.

- [ ] **Step 4: Commit**

```bash
git add feature_list.json progress.md docs/superpowers/reports/2026-09-09-public-content-pricing-backend.md
git commit -m "docs: record public pricing backend evidence"
```
