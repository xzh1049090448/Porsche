# B1-B3 Gateway Owner ACL Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `executing-plans` task-by-task with checkbox steps.

**Goal:** Enforce the latest owner user-model ACL alongside Gateway Key ACL for each `/v1` request.

**Architecture:** The service loads token then owner on every authentication, strictly decodes each persisted ACL, and returns an immutable-copy principal. Gateway handlers authenticate identity before parsing model-specific requests, then enforce both ACLs without changing Key CRUD or WhiteLabel’s existing global/key filtering.

**Tech Stack:** Go 1.22, GORM/MySQL 8, Redis 7 test fixture, Gin, httptest upstream.

---

### Task 1: Principal and strict owner ACL read

**Files:**
- Modify: `internal/service/gateway_token.go`
- Modify: `internal/service/gateway_token_test.go`

- [ ] Write behavior tests first: each side SQL nil, whole JSON null, `[]`, `[null]`, `[""]`, number/object; one-side deny/intersection; principal copy/zero behavior; owner state; read/write unavailable; same-millisecond last-used `RowsAffected=0`; and legacy `Authenticate` delegation.
- [ ] Run `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/service -run 'Gateway.*(Owner|Principal|Authenticate)' -count=1`; capture behavior RED before implementation.
- [ ] Add `GatewayTokenPrincipal`, `AuthenticatePrincipal`, strict raw JSON decoding and fixed unavailable sentinel. Query owner on each call after token validation; never join/cache/merge ACL or reject same-millisecond `RowsAffected=0`.
- [x] Re-ran focused fixture tests with private `TEST_*`; final independent suite recorded zero test skips.

### Task 2: `/v1` owner-aware model enforcement

**Files:**
- Modify: `internal/handler/gateway_tokens.go`
- Modify: `internal/whitelabel/errors.go`
- Modify: `internal/handler/gateway_whitelabel_test.go`

- [ ] Add failing httptest-upstream cases for list filtering/no shared slice and cache refresh filtering, both detail query and legacy detail path owner-first 404, chat/SSE 403 before both catalog and generation upstream calls, unavailable 503 envelope, no-store success headers, and B1-B2 owner ACL update visible on the next request.
- [ ] Run `GOCACHE=/private/tmp/porsche-go-build-cache go test ./internal/handler -run 'Gateway.*Owner' -count=1`; capture RED.
- [ ] Route every `/v1` handler through principal; list Key/global then owner; detail owner before Key `GetModel`; chat/SSE check both after model parse and before upstream. Add only the fixed unavailable public error mapping.
- [x] Focused real fixture/httptest suite completed; forbidden owner ACL paths made zero catalog/generation upstream calls.

### Task 3: Failure, concurrency and acceptance evidence

**Files:**
- Modify: `internal/service/gateway_token_test.go`
- Modify: `internal/handler/gateway_whitelabel_test.go`
- Modify: `feature_list.json`, `progress.md`
- Create: `docs/superpowers/reports/2026-09-03-b1b3-gateway-owner-acl.md`
- Create: `docs/superpowers/reports/validation/2026-09-03-b1b3-gateway-owner-acl/validation.json`

- [x] Used fixture-isolated data and callback injection for JSON/read/last-used faults; no production DSN or secret was emitted.
- [x] Final independent race, exported-fixture JSON suite, build, vet, diff and JSON parsing passed.
- [x] PM final SPEC PASS and independent VERDICT PASS completed; `go-014` is passing. Fixture cleanup follows the verified task-only procedure; no deploy, commit, push, migration edit, or unrelated cleanup.

**Limits:** no new schema/migration implementation, 24-capability/snapshot change, ticket/idempotency/outbox, new FE, or production operation. Existing migrations ran only on the authorized disposable fixture. Owner freshness is per-request committed-read only, not cross-table atomicity or in-flight revocation.
