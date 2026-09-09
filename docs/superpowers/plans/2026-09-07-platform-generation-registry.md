# Platform generation registry implementation plan

> Execute with test-driven development and independent specification/security review before the next tranche.

**Goal:** Add the Redis-backed ownership and lifecycle registry required by platform SSE v2 without activating v2 HTTP streaming, changing MySQL, or affecting legacy routes.

**Architecture:** `PlatformGenerationStore` owns a dedicated `porsche:platform:generation:v2:` namespace over its own Redis client. Keys are derived from the authenticated internal `users.id` and canonical client `generation_id`; stored data is bounded lifecycle metadata only. Atomic Lua transitions enforce claim/idempotency, cancel precedence, and commit completion. `app.State` exposes the store only when the existing configured Redis is reachable. Missing or failed Redis leaves legacy behavior unchanged and causes future v2 handlers to return a stable 503.

**Approved contract:**

- A duplicate claim never calls upstream or reattaches SSE; callers receive conflict plus the authoritative state.
- Compare stores one independent `assistant_message_guid` per model.
- Cancelled/failed generations do not consume daily call quota; incurred upstream cost belongs to a later separate audit path.
- Redis unavailability is fail-closed for v2 only; no in-memory fallback and no legacy regression.

## Task 1: Define bounded state and fail-closed construction

Files:

- Create `internal/service/platform_generation_store.go`
- Create `internal/service/platform_generation_store_test.go`

RED tests must prove:

- nil client, malformed generation UUID, non-positive user ID, empty/duplicate/over-three models, and oversized identifiers are rejected before Redis.
- the Redis key uses `users.id` plus generation UUID, never username, user GUID, prompt, response content, Authorization, or upstream credentials.
- records are limited to mode, declared models, state, per-model sequence/terminal state, millisecond timestamps, stable error code, and final assistant-message GUID references.

GREEN implementation uses explicit stable Go integer constants for lifecycle states and a 24-hour TTL. No database model or migration is introduced.

## Task 2: Implement atomic claim and duplicate semantics

RED tests against explicit `TEST_REDIS_URL` must prove one winner across concurrent claims and an authoritative duplicate snapshot for every loser. Missing `TEST_REDIS_URL` must be reported as `SKIP`, never as a Redis pass.

GREEN implementation uses one Lua script to create the bounded record with TTL only when absent. Duplicate paths read the existing record atomically and return a typed conflict result; they never reset TTL or state.

## Task 3: Implement cancel and completion CAS

RED tests must cover:

- `running -> cancelling -> cancelled` and cancellation before upstream start.
- `running -> committing -> completed` with one assistant-message GUID per successful compare model.
- cancel racing with commit: once `committing` or `completed`, cancel returns the authoritative completed/committing state and cannot overwrite it.
- terminal states cannot return to running; stale expected state/sequence is rejected.
- failed/cancelled snapshots carry no quota-consumed marker; only stable error codes are stored.

GREEN implementation performs each transition through Lua/CAS, preserves the initial TTL boundary, and validates decoded Redis data before returning it.

## Task 4: Wire application state without route activation

Files:

- Modify `internal/app/state.go`
- Modify `internal/app/state_test.go`

RED/GREEN tests prove:

- configured Redis creates and verifies the generation store independently of `AuthRedis` state ownership.
- missing Redis leaves the generation store nil so the already-gated v2 request remains stable 503.
- Redis construction failure fails closed during configured application initialization.
- legacy platform and authentication behavior is unchanged.

Do not add or activate status/cancel/stream routes in this tranche.

## Verification and review

Run:

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test -race ./internal/service ./internal/app -run 'TestPlatformGeneration|TestNewState.*Generation' -count=1
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go vet ./...
git diff --check
```

Record Redis integration tests as `BLOCKED_FIXTURE` if `TEST_REDIS_URL` is absent. Obtain independent spec and security review of the final diff. Do not push, merge, deploy, migrate, contact production Redis, or call a real paid upstream.
