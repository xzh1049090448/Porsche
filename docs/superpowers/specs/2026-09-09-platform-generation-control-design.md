# Platform generation control design (BE04)

Date: 2026-09-09

Status: approved for implementation planning

Scope owner: Porsche backend

Related product contract: `/Users/xuzhihao/code/Porsche-Web/.worktrees/chat-streaming-prd/docs/superpowers/specs/2026-09-07-chat-adaptive-character-streaming-prd.md`

Depends on: BE01 protocol primitives, BE02 Redis generation registry, and BE03 durable generation persistence merged by PR #11 at `ee9c4ca`

## 1. Decision and objective

BE04 activates the authenticated generation status and cancellation endpoints and adds restart-safe convergence for Redis generation records. It keeps Redis as the short-lived lifecycle authority, uses the BE03 receipt graph as the only authority for completed content, and introduces a renewable owner-bound lease so a healthy long-running generation is not mistaken for an orphan.

The selected recovery design is a bounded Redis `SCAN` worker plus compare-and-swap transitions. It avoids an additional scheduling index while retaining multi-instance safety. Each instance may observe the same record, but only one valid CAS transition wins.

The user approved cancellation-before-stream semantics. A valid cancellation request for a generation that does not yet exist atomically creates a `cancelled` tombstone. The tombstone initially has `mode: null`, no models, and no content. A later claim may atomically enrich its immutable mode/model identity, but it remains cancelled, keeps its original TTL, receives no runner lease, and must cause the later stream orchestration to perform zero upstream calls.

This approval covers local design, implementation, tests, and commits only. It does not authorize push, merge, deployment, production migration, production data changes, or calls to a real paid upstream.

## 2. Goals

BE04 must provide all of the following:

1. Authenticated `GET /api/v1/platform/chat/generations/{generation_id}` with owner isolation and authoritative terminal hydration.
2. Authenticated, idempotent `POST /api/v1/platform/chat/generations/{generation_id}/cancel` with the specified `200`/`202` behavior.
3. Atomic cancellation-before-claim tombstones that prevent later upstream work.
4. A server-generated lease capability for newly claimed running generations and owner-checked renewal for BE05.
5. Immediate and periodic bounded reconciliation of expired `running`, stale `cancelling`, and `committing` records after startup and during normal operation.
6. Safe multi-instance behavior using existing Redis CAS and the BE03 MySQL advisory-lock/receipt reconciliation boundary.
7. Stable, body-free dependency failures and no disclosure of another user's generation existence.
8. Explicit application lifecycle ownership so the background worker and Redis clients can be stopped without goroutine leaks.

## 3. Non-goals

BE04 does not:

- activate v2 behavior for `POST /api/v1/platform/chat/completions` or `/compare`;
- call the white-label adapter or any real/paid upstream;
- implement SSE streaming, delta forwarding, single-model orchestration, or compare fan-out;
- implement BE05's periodic renewal loop, although it exposes the lease primitive BE05 must call;
- persist prompts, partial replies, cancelled replies, or failed replies;
- add a MySQL table, migration, Redis sorted set, leader election, distributed cancellation bus, or durable task queue;
- change the BE03 receipt schema or transaction boundary;
- change legacy streaming, legacy compare, or non-streaming behavior;
- alter frontend polling/playback behavior;
- push, merge, deploy, run a production migration, or perform production acceptance.

## 4. Existing constraints and invariants

Redis records are keyed only by authenticated internal `users.id` plus the canonical lowercase generation UUID and retain their original 24-hour TTL across all mutations. Redis contains lifecycle metadata and message references only; it never contains prompt or reply text.

BE03 establishes the successful commit order: `running -> committing`, one complete MySQL transaction and receipt, then `committing -> completed`. The status path must never return completed content merely because Redis says `completed`; it must load and validate the entire owned BE03 receipt graph. A missing, corrupt, mismatched, or unavailable graph fails closed without returning content.

The product contract requires `cancelling` and `committing` to converge within 30 seconds when dependencies are healthy. BE04 extends this to orphaned `running` records by assigning a 30-second renewable lease. Dependency outages may delay convergence, but the worker must not fabricate a terminal state when Redis or MySQL cannot establish the required preconditions.

All public identifiers remain strings. `generation_id` is a canonical lowercase UUID. Conversation and assistant message GUIDs are positive signed 64-bit snowflake values rendered as decimal strings. The status endpoints never expose internal database IDs, Redis keys, lease material, SQL details, raw dependency errors, or upstream bodies.

## 5. Redis record variants

The strict Redis decoder is extended to recognize two valid record variants.

### 5.1 Claimed generation

A claimed generation retains the BE02 immutable identity and state machine:

- non-null mode: `single` or `compare`;
- one model for single, two or three distinct models for compare, in request order;
- one model state for every model;
- generation state and timestamps satisfying the existing BE02 invariants.

While the generation state is `running`, the record also contains:

- `lease_owner_sha256`: lowercase SHA-256 hex of a random runner capability;
- `lease_until_ms`: a positive JS-safe Unix millisecond timestamp exactly 30 seconds after claim or the latest valid renewal.

The raw lease capability is generated from 32 cryptographically random bytes and returned only to the successful internal claimant. Redis stores only its digest. Duplicate claims never receive the original capability. Neither the raw capability nor its digest may enter an HTTP DTO, application log, diagnostic event, error, receipt, or database row.

Terminal, `cancelling`, and `committing` records carry no usable lease. A transition out of `running` clears both lease fields in the same CAS update. Existing terminal records written by BE02 without lease fields remain valid. A legacy `running` record without a complete lease is treated as an expired orphan by the reconciler, not as a renewable active generation.

### 5.2 Cancellation tombstone

A pristine tombstone contains only:

- the canonical generation ID;
- state `cancelled`;
- empty model list and model-state map;
- `mode` absent internally and projected as JSON `null`;
- equal positive `created_at_ms` and `updated_at_ms`;
- no error, message GUID, request ID, or lease fields.

The tombstone is valid only in `cancelled`. No other state may have absent mode or empty models.

When a later claim reaches a pristine tombstone, one Lua/CAS operation validates the expected tombstone bytes and enriches mode, ordered models, and cancelled per-model states. It does not change state, timestamps, or the remaining TTL and does not issue a lease. The claim returns the authoritative cancelled snapshot as a duplicate conflict. A later claim with a different identity returns the already enriched authoritative snapshot as a conflict and changes nothing.

### 5.3 Atomic operations

BE04 adds narrowly scoped store operations:

- `CancelOrCreate`: atomically creates a pristine tombstone if absent, changes `running -> cancelling`, or returns the existing non-running authority without rewriting it.
- `RenewLease`: hashes the presented capability and renews only when the generation is still `running`, the digest matches, and the request time has not passed the current lease deadline.
- `FailExpiredRunning`: changes only an expired `running` record to `failed` with stable code `internal_error`, preserving completed/failed model terminals and failing any still-running models.
- `ConvergeStaleCancelling`: changes only a `cancelling` record at least 30 seconds old to `cancelled`, cancelling every still-running model.
- `ScanGenerationKeys`: performs cursor-based `SCAN MATCH` over the dedicated generation namespace without using `KEYS`.

All mutations preserve the original 24-hour TTL with `KEEPTTL`. Clock regression, non-JS-safe timestamps, malformed stored JSON, unexpected key shapes, and CAS loss fail closed. State conflicts always return or reload the authoritative snapshot; they never retry a stale write blindly.

## 6. Lease ownership contract

The claim API returns a structured internal result containing the public lifecycle snapshot and, only when a new running record was created, the raw lease capability. This avoids adding secret material to `PlatformGenerationSnapshot`.

BE05 must retain the raw capability in the owning request goroutine and renew every 10 seconds while upstream work is active. Renewal extends the deadline to `now + 30 seconds`. Renewal after the deadline, after cancellation, after commit begins, or with a different capability returns a typed conflict and must cause the future stream orchestrator to stop upstream work. A late renewal can never resurrect a record that the scheduler has failed.

BE04 tests exercise claim, renewal, expiry, and ownership, but no production request path starts a running generation in this tranche because the v2 stream routes remain guarded.

## 7. Generation query service

The handler delegates to one service that accepts `context`, database handle, generation store, authenticated `user_id`, canonical generation ID, and current UTC Unix milliseconds. It never accepts an owner from request JSON or query parameters.

The service first reads the current user's Redis record. A missing user-scoped key returns not found even if another user has the same UUID. Redis unavailability or malformed data returns unavailable.

Before projection it performs one opportunistic convergence attempt:

- expired `running` is failed through `FailExpiredRunning`;
- stale `cancelling` is finalized through `ConvergeStaleCancelling`;
- any `committing` record is passed to `ReconcilePlatformGeneration`, which completes it when an exact BE03 receipt exists, leaves it committing before the 30-second deadline when no receipt exists, or fails it after that deadline;
- terminal states are not rewritten.

A CAS conflict causes one authoritative reload. Dependency or integrity errors remain errors; the handler does not return the pre-reconciliation snapshot as if it were current.

For `completed`, the service loads the complete BE03 receipt graph by authenticated `user_id + generation_id`, verifies that mode, model order, terminal result states, stable codes, and every successful assistant message GUID exactly match Redis, and uses only the hydrated receipt/message content in the response. It separately reads the active authenticated user's current `total_tokens_used`; this field is a current account total and must not be confused with the receipt's generation-local `TotalTokens`.

For all non-completed states, the service projects lifecycle metadata only. `conversation_guid` is `null`, and no result content, partial text, token count, message GUID, prompt, or receipt content is returned. A failed response includes its allowlisted stable code. `request_id` is optional and is omitted unless a later orchestration tranche safely stores a failure correlation value; BE04 does not invent it from the status request.

## 8. HTTP contracts

Both endpoints are registered under the existing authenticated platform route group and use `middleware.RequireUser`. Both set `Cache-Control: no-store`. They consume no JSON request body.

### 8.1 `GET /api/v1/platform/chat/generations/{generation_id}`

- malformed or non-canonical UUID: `400` with stable invalid-request JSON;
- missing current-user record: `404` with stable generation-not-found JSON;
- current lifecycle state, including non-terminal state: `200`;
- Redis/MySQL unavailable, completed graph missing/corrupt/mismatched, or reconciliation integrity failure: sanitized `503` with no content or dependency detail.

Completed single response:

```json
{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"completed","mode":"single","conversation_guid":"123456789","result":{"model":"model-a","status":"completed","assistant_message_guid":"223456789","content":"final text","tokens":12},"total_tokens_used":120}
```

Completed compare response preserves claimed model order. Successful entries contain assistant GUID, content, and tokens; failed entries contain only model, status, and stable code:

```json
{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"completed","mode":"compare","conversation_guid":"123456789","results":[{"model":"model-a","status":"completed","assistant_message_guid":"223456789","content":"final text","tokens":12},{"model":"model-b","status":"failed","code":"gateway_upstream_error"}],"total_tokens_used":120}
```

Ordinary non-completed response:

```json
{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"cancelling","mode":"single","conversation_guid":null}
```

Pristine cancellation tombstone response:

```json
{"generation_id":"550e8400-e29b-41d4-a716-446655440000","status":"cancelled","mode":null,"conversation_guid":null}
```

### 8.2 `POST /api/v1/platform/chat/generations/{generation_id}/cancel`

The handler uses `CancelOrCreate` for the initial decision:

- absent creates a pristine cancelled tombstone and returns `200` immediately;
- `running` changes atomically to `cancelling`, then invokes the optional process-local cancellation hook outside Redis/CAS locks;
- `cancelling` or `committing` keeps its authority and enters the bounded wait;
- `completed`, `failed`, or `cancelled` returns the authoritative terminal projection idempotently.

After a running transition or existing non-terminal result, the request waits for at most three seconds. It performs bounded status-service checks until a terminal state is observed, the deadline expires, or the request context ends. A terminal `cancelled`, `completed`, or `failed` response is `200`. A still-current `cancelling` or `committing` response is `202` with `Retry-After: 1`. The endpoint never returns `running`: a CAS race is resolved by reloading and, if the record is still running, repeating only the atomic cancel decision within the original three-second budget.

Malformed UUID returns `400`. Dependency failures return sanitized `503`. A valid absent ID never returns `404`; it creates the current user's tombstone. Because keys are user-scoped, cancelling the same UUID owned by another user creates an independent tombstone and reveals nothing about the other record.

## 9. Process-local cancellation registry

BE04 introduces a small concurrency-safe cancellation registry keyed by authenticated `user_id + generation_id`. The future BE05 stream registers a `context.CancelFunc` after a successful claim and unregisters it with a compare-and-delete token on every exit path. Registering duplicate owners fails closed rather than replacing a live cancel function.

The cancel endpoint invokes a present callback at most once per successful `running -> cancelling` decision and does so outside registry and Redis locks. Missing registration is normal during startup races or after a process restart; the Redis state remains authoritative and the 30-second reconciler still guarantees eventual cancellation when dependencies are healthy. The registry contains no prompt, reply, credential, or upstream object.

## 10. Background convergence worker

Each application instance owns one worker. It runs an immediate pass after successful construction and then ticks every five seconds. A pass continues from its previous Redis cursor and stops after either 512 candidate keys or 100 milliseconds, whichever comes first. These constants bound Redis and CPU work while allowing at least 3,072 candidate observations per 30-second convergence window under normal latency. Deployments exceeding that active-generation envelope require a separately approved indexed scheduler; BE04 does not silently switch to `KEYS` or an unbounded scan.

The scanner accepts only keys with the exact prefix followed by a canonical positive decimal `users.id`, one colon, and a canonical lowercase UUID. It ignores malformed keys without reading, mutating, extending, or deleting them. Redis `SCAN COUNT` is treated only as a hint; duplicate keys are safe because convergence operations are idempotent/CAS protected.

For each valid identity the worker applies the same single-record convergence rules as the query service. It never projects, logs, or retains completed content; committing reconciliation may load the full receipt graph internally because BE03 requires that validation before completion. `Committing` reconciliation uses the BE03 advisory lock and full receipt validation; `running` expiry and stale cancellation use Redis CAS only. A per-record conflict is benign. A dependency or integrity error is recorded only as a bounded, body-free category and retried on a later pass; it does not stop the worker or change a record without proof.

One worker processes records serially. It does not spawn an unbounded goroutine per key. Cancellation of the worker context stops scanning promptly.

## 11. Application assembly and lifecycle

`app.State` assembles one generation control service, one cancellation registry, and one convergence worker only when the required Redis generation store and database are present. The HTTP routes remain registered in test/minimal states, but a missing dependency returns stable `503` instead of falling back to legacy behavior.

`State.Close` cancels the worker, waits for its completion with a bounded timeout, and then closes the generation and authentication Redis clients exactly once. Partial-construction cleanup follows the same ownership order. `cmd/server` defers state closure after successful construction. Closing twice is safe and does not panic, leak a goroutine, or close an externally owned database connection.

Worker startup is not allowed to block application construction on a full scan. The immediate pass runs in the owned worker goroutine. A startup Redis/MySQL error is retried on the normal schedule and does not bypass the existing fail-closed dependency construction checks.

## 12. Error mapping and privacy

Service errors remain typed and are mapped at the handler boundary:

- invalid identity or timestamp -> `400 invalid_request`;
- missing current-user generation on GET -> `404 generation_not_found`;
- current lifecycle state -> `200`, or `202` only for the cancellation endpoint's unresolved non-terminal result;
- Redis, MySQL, receipt integrity, worker dependency, or hydration mismatch -> `503 generation_status_unavailable`.

Authentication remains the existing middleware's `401` boundary. Owner isolation is structural because the handler derives `users.id` from authentication and the store/database queries include that ID. No endpoint searches globally by generation UUID, so a foreign record is indistinguishable from absence.

Logs and errors may include only route, stable category, state, bounded timing, and a hash-safe correlation value. They must not contain generation record JSON, lease capability/digest, prompt, reply, Authorization, Redis URL/key, database address, SQL, constraint name, raw dependency error, or upstream response.

## 13. Testing strategy

### 13.1 Pure unit and contract tests

- strict decoding accepts pristine/enriched tombstones and lease-bearing running records and rejects every mixed invalid variant;
- DTOs render exact state strings, `mode: null` only for pristine tombstones, decimal GUID strings, ordered compare results, and no content outside completed;
- route tests prove authentication, canonical UUID validation, `Cache-Control: no-store`, method behavior, exact `200`/`202`/`400`/`404`/`503`, and `Retry-After: 1`;
- existing legacy endpoints remain registered and unchanged, while both v2 stream requests still return the BE01 stable `503` guard.

### 13.2 Real Redis tests

Using an explicit isolated `TEST_REDIS_URL` fixture:

- cancellation-before-claim atomically creates one 24-hour tombstone under concurrency;
- later matching claim enriches identity without refreshing TTL and returns no lease; a different claim cannot overwrite it;
- claim returns lease material only to the creation winner;
- correct-owner renewal succeeds before expiry, while wrong-owner, late, cancelled, committing, and terminal renewals fail;
- cancel-versus-commit, cancel-versus-renew, expiry-versus-renew, and multiple-worker races have exactly one authoritative outcome;
- all mutations keep TTL and strict sequence/model invariants;
- cursor scanning is bounded, resumes across passes, tolerates duplicates, and never mutates malformed or foreign-prefix keys.

### 13.3 MySQL plus Redis tests

Using explicit isolated MySQL 8 and Redis fixtures with migration ledger `0001` through `0011`:

- completed single and partial-success compare statuses hydrate the exact owned receipt graph and current account total;
- Redis/receipt mode, order, state, code, or assistant-GUID mismatch returns unavailable with no content;
- missing/deleted/cross-user conversation or message references return integrity failure with no content;
- a receipt-present committing record converges to completed after restart;
- a receipt-absent committing record remains committing before 30 seconds and becomes failed after 30 seconds;
- two service instances and workers converge the same records without duplicate database effects;
- expired running becomes failed, stale cancelling becomes cancelled, and active renewed running is never misclassified.

### 13.4 Lifecycle and regression gates

- fake clock/ticker tests make the three-second cancel budget, five-second worker cadence, 10-second future renewal cadence, and 30-second convergence boundaries deterministic;
- worker cancellation and repeated `State.Close` complete without goroutine/client leaks;
- fresh focused tests and focused race tests cover store, control service, handler, app state, and router;
- `go test ./...`, `go test -race` for affected packages, `go build ./...`, `go vet ./...`, `gofmt`, `git diff --check`, and secret/content scans must pass;
- real fixture tests may not be counted as passing when their environment variable is absent or their filter selects no tests.

## 14. Delivery boundary and acceptance

BE04 is complete only when the two authenticated control endpoints are active, cancellation tombstones and lease ownership pass real Redis races, restart convergence passes real MySQL/Redis tests, lifecycle cleanup is verified, and all regression gates pass. The evidence report must identify any fixture skip or unavailable environment as `BLOCKED_FIXTURE`, not as success.

Completion of BE04 does not complete `go-018`. BE05 still owns single-model v2 SSE orchestration and live lease renewal; BE06 owns compare fan-out; frontend/backend joint browser acceptance remains a later tranche. Production migration `0011`, deployment, public HTTPS validation, and real upstream model calls remain `NOT_RUN` until separately authorized.
