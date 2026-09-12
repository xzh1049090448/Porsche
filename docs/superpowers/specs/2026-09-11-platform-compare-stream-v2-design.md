# BE06 Platform Compare SSE v2 Design

**Date:** 2026-09-11
**Status:** Approved design and plan, pending implementation
**Repository:** Porsche backend
**Baseline:** `origin/main` at `944309003ce47bbaf949f6c0f28d9bd302016d0f`
**Feature:** `go-018` / BE06 compare streaming

## 1. Scope and completion boundary

BE06 activates the authenticated multi-model branch of `POST /api/v1/platform/chat/compare` when the request explicitly selects `stream: true`, `stream_version: "platform-chat-sse.v2"`, and a canonical client-generated `generation_id`. It adds a dedicated compare-generation coordinator that fans out to two or three models while retaining one generation identity, one Redis lease, one cancellation registration, one persistence transaction, and one authoritative terminal state.

BE06 reuses the BE02 Redis state machine, BE03 receipt/result persistence, BE04 GET/cancel and restart convergence, and BE05 upstream/SSE/lifecycle primitives. It does not refactor the proven BE05 single runner into a generic engine.

The following remain out of scope and require separate authorization: Porsche-Web changes, frontend/backend joint acceptance, production migration or deployment, public HTTPS/Nginx/CDN acceptance, real or paid upstream calls, removal of legacy protocols, reconnect/replay, multiple subscribers, and resumable upstream cursors. Completion of BE06 does not by itself mark `go-018` complete.

## 2. Contract inputs and compatibility

The approved source requirement is Porsche-Web's `2026-09-07-chat-adaptive-character-streaming-prd.md` v1.0. The currently inspected Porsche-Web `interface-contract.json` is version `v1.0.0`, status `draft`, SHA-256 `0891e452f122922f576745db89c96c853a9a7cf4ff00078c30ae3b3f0769e970`, and has empty `interfaces/sse_events`. It is evidence of an unresolved cross-repository handoff, not joint contract acceptance. The frontend's existing compare behavior, SSE parser, cancel flow, and GET hydration differences remain for later coordinated joint acceptance and do not expand BE06. BE06 may implement the already approved PRD, but joint acceptance must later bind a versioned interface contract and exact frontend/backend revisions.

The v2 compare request requires:

- an authenticated active user;
- a canonical lowercase UUID `generation_id`;
- `stream: true` and exact `stream_version: "platform-chat-sse.v2"`;
- two or three distinct, non-empty, authorized model identifiers in request order;
- the existing message, conversation, `max_tokens`, context-window, request-size, and allowlist rules.

V2-only controls are stripped before constructing each upstream request. Validation and authorization finish before claim, quota mutation, response headers, persistence, or upstream access. For Free or any other daily-call-limited plan, the preflight uses the effective current-day usage after the existing daily reset rule and requires `daily_call_limit - effective_daily_calls_used >= len(models)`. Professional and Enterprise retain the existing unlimited daily-call rule. This admission invariant gives `Finalize` capacity for every possible successful model result without adding quota reservations or schema. Legacy compare streaming, non-streaming compare, single v2, GET/cancel, authentication, ACL, and public error envelopes remain behaviorally compatible.

## 3. Chosen architecture

### 3.1 Dedicated coordinator

Add a `PlatformCompareGenerationRunner` rather than invoking multiple single runners. The coordinator owns the complete compare lifecycle and exposes a narrow `Run(PlatformCompareGenerationInput) (PlatformCompareGenerationRunResult, error)` API to the handler.

One run owns:

- the authenticated `users.id`, generation UUID, ordered model list, conversation provenance, reserved conversation GUID, request ID, and sanitized upstream payload template;
- one `mode=compare` Redis claim and opaque lease token;
- one application-rooted, timeout-bounded runner context;
- one cancellation-registry entry keyed by `users.id + generation_id`;
- one ten-second renewal loop for the shared 30-second lease;
- one compare-only encoder/output state that is never the non-thread-safe `platformSingleOutput`;
- one shared serial critical section covering owner-bound Redis mutations, the corresponding worker-state transition, encoder transition, and the complete SSE frame write or detach transition;
- one final BE03 persistence transaction and Redis completion transition.

The coordinator is assembled and owned by `app.State` beside the single runner. Both reuse the same database, generation store, persistence service, cancellation registry, white-label service, application root context, and upstream timeout. Construction fails closed if any dependency is absent or malformed.

### 3.2 Per-model workers

After claim and cancellation registration, the coordinator emits and flushes the unique `meta` frame, then starts one worker per requested model. Each worker gets its own immutable upstream body, response-body owner, content builder, token accumulator, sequence counter, and terminal result slot.

Workers may receive and validate chunks concurrently. Cross-model events may interleave, but every Redis/worker/encoder/output transition is serialized through the coordinator's one shared critical section, and the HTTP writer is never called concurrently. Within one model, a non-empty UTF-8 delta follows this order while holding that section:

1. validate the typed upstream chunk, model identity, content limit, token fields, and next sequence;
2. perform owner-bound `RecordDeltaOwned`;
3. update the worker's in-memory state;
4. advance the encoder and write the complete `delta` frame, or atomically detach output on the first write error.

A worker that reaches a valid upstream terminal performs owner-bound `MarkModelDoneOwned`, updates its terminal slot, advances the encoder, and emits one complete `model_done` frame in that same serial section. A model-local upstream timeout, error, malformed stream, invalid chunk, or content overflow performs a new owner-bound `MarkModelFailedOwned` transition with a stable code and then updates/encodes/emits one `model_error` in the same section. A model-local failure never cancels a different model. Lease renewal uses the same mutation serialization so it cannot interleave with an owner-bound mutation, but it neither advances the encoder nor emits a frame.

`MarkModelFailedOwned` must validate the lease owner, unexpired lease, model membership, current per-model state, stable error code, global running state, and current timestamp in the same Redis CAS boundary as the existing owned delta/done operations. A stale worker cannot mutate a renewed or replaced generation.

### 3.3 Shared lifecycle and detached output

The incoming HTTP context controls admission only. After a successful claim and registry handoff, the generation is rooted in the application context and continues if the browser disconnects. The first downstream write failure atomically detaches the output; later frames are not written, but workers, renewal, terminal transitions, persistence, and cleanup continue. The client later obtains the authoritative result through GET.

An explicit cancel request uses the existing cancellation registry to cancel every worker and close every owned upstream body. Cancel and commit remain Redis-CAS competitors:

- cancel wins before `committing`: acknowledge `cancelled`, persist no conversation, messages, usage, receipt, quota, or token totals;
- commit wins: cancellation returns `committing` or the recovered `completed` result and cannot rewrite it as cancelled.

Application shutdown first closes admissions, then cancels active single and compare runs, waits for registrations/admissions to drain within the existing bound, and only then closes shared Redis resources. Every timer, goroutine, response body, cancellation callback, and registration has one idempotent cleanup owner.

## 4. Terminal and persistence semantics

The coordinator waits until every model has a local terminal result or a generation-level failure/cancellation takes authority.

### 4.1 Partial or complete success

If at least one model completed, all successful and failed model results are ordered exactly like the request and the coordinator performs owner-bound `BeginCommitOwned`. It calls BE03 `Finalize` once with:

- `mode=compare` and the exact ordered models;
- one user message and exact existing/reserved conversation provenance;
- completed results containing content, tokens, and last sequence;
- failed results containing no content, zero tokens, and one stable error code.

BE03 creates the conversation when required, writes the user message once, writes one assistant message and usage row per successful model, stores failed receipt results without assistant messages, and charges daily calls/token totals only for successful models. The existing database schema and migrations `0011` through `0013` are sufficient; BE06 adds no schema or migration.

The runner has an explicit receipt-reader dependency with the existing `LoadPlatformGenerationReceipt(ctx, db, userID, generationID)` shape. On the direct post-`Finalize` path it strictly validates the complete receipt/result graph before Redis `Complete`, which receives assistant GUIDs for successful models only. On every path that observes `committing`, or where `Complete`/commit authority is unknown, it must use the receipt reader to load and strictly validate the complete graph, derive the successful assistant GUIDs from that validated receipt, and call `ReconcileComplete`; it must never call `Finalize` again. It then loads the user's authoritative total token count and emits the unique global `done`. The `models` object reports each requested model as `completed` with tokens or `failed` with its stable code.

### 4.2 All models failed

If every model failed, the coordinator must not call `BeginCommitOwned` or BE03 `Finalize`. It converts the owned running generation to global `failed`, emits one global `error`, sends no `done`, and persists no conversation, message, usage, receipt, quota, or token mutation. Per-model `model_error` frames may precede the global error while the client is attached.

### 4.3 Generation-level failure

Redis/CAS uncertainty, lease loss, impossible authoritative snapshots, database failure, persistence-integrity failure, renewal failure, or application shutdown is generation-level. The coordinator cancels all workers and converges through current authority:

- `cancelling` is acknowledged as `cancelled`;
- `committing` is resolved only by reading and strictly validating the complete receipt graph and then calling `ReconcileComplete`, never by calling `Finalize`;
- an authorized `running` generation is failed with `internal_error` or the applicable stable code;
- an unknown or invalid authority fails closed without inventing success.

No successful `done` is emitted before MySQL persistence and Redis completion are both authoritative.

## 5. HTTP and SSE behavior

The handler keeps the existing request parser and model authorization boundary. Only the explicit compare-v2 branch changes from the stable `platform_stream_v2_unavailable` response to the compare runner.

Before streaming starts, invalid input maps to `400 invalid_request`, unavailable quota to `429 rate_limited`, unavailable dependencies to stable `503`, and a duplicate generation to `409` with the authenticated `PlatformGenerationControl.Get` projection. A duplicate does not access upstream or mutate quota/persistence.

Once streaming starts, the handler calls `service.SetPlatformSSEV2Headers` unchanged. The exact compare-v2 header set is `Content-Type: text/event-stream; charset=utf-8`, `Cache-Control: no-cache, no-transform`, and `X-Accel-Buffering: no`; BE06 does not add `Connection` and does not change single-v2 headers. The stream contains exactly one `meta`. The only body event types are `meta`, `delta`, `model_done`, `model_error`, `done`, and global `error`; v2 never emits bare `[DONE]`. A post-header failure is expressed only through sanitized SSE when the writer remains attached. EOF or writer failure is not treated as business success.

Stable model/global codes are restricted to the existing v2-safe vocabulary, including `timeout`, `upstream_error`, `gateway_upstream_error`, and `internal_error`. Provider bodies, credentials, endpoints, prompts, replies, SQL, Redis keys, lease tokens/digests, and arbitrary errors never cross the public or evidence boundary.

## 6. Implementation surface

Expected production changes are limited to:

- a new compare runner and its focused tests under `internal/service/`;
- an owner-bound per-model failure transition in the generation store plus Redis/CAS tests;
- compare-v2 handler activation and contract tests in `internal/handler/`;
- application construction/lifecycle wiring and tests in `internal/app/`;
- route inventory assertions where needed;
- BE06 integration tests, delivery report, `progress.md`, and `feature_list.json`.

The existing legacy `PlatformChatService.CompareStream` is not adapted or called by v2. The single runner is not generalized. Unrelated refactors, schema changes, frontend files, deployment files, and production configuration are excluded.

## 7. Test and review strategy

Implementation follows TDD. Focused unit and race tests cover validation before side effects, exact duplicate behavior, two/three-model fan-out, Free/limited-plan capacity for two and three requested models, the existing daily reset boundary, Professional/Enterprise unlimited behavior, concurrent quota competition, independent fast/slow workers, cross-model interleaving, per-model strict sequence, partial failure, all-failed behavior, detached output, cancel/commit races, renewal authority, every observed-`committing` and commit-unknown receipt-reader reconciliation path without `Finalize` replay, full receipt-graph rejection, shared mutation/worker/encoder/frame serialization, non-concurrent HTTP writes, response-body closure, admission drain, and shutdown.

Handler/app tests prove exact headers and first `meta`, sanitized pre/post-stream errors, two-to-three model validation, authenticated duplicate projection, nil dependency fail-closed behavior, compare-v2 activation, single-v2 preservation, and byte-compatible legacy compare/non-streaming behavior.

Real disposable MySQL 8.4 and Redis 7 integration tests must cover:

1. all models succeed;
2. partial success plus model failure;
3. all models fail with no durable mutation;
4. disconnect followed by GET hydration;
5. explicit cancellation of all workers with no durable mutation;
6. renewal beating the converger;
7. commit-unknown receipt reconciliation without SQL replay;
8. concurrent duplicate and quota races, including two- and three-model limited-plan capacity at the daily reset boundary.

Successful cases assert the exact receipt/result graph, one user message, one assistant message/usage row per success, no assistant message for failures, successful-model daily call/token deltas only, request-order GET results, Redis assistant GUIDs only for successes, and token-total equality. Non-success cases compare before/after durable snapshots and prove zero conversation/message/usage/receipt/quota/token change.

Final gates use fresh isolated fixtures and include focused normal/race, full repository tests, affected-package race, build, vet, diff checks, JSON validation, privacy scans, skip census, and exact fixture/container/port/credential cleanup. Every BE06 real-fixture test must run with zero skips. The repository's full orchestration flow applies: Explorer, original Worker, snapshot-bound Spec, Security, and Test reviews in order. Any reviewed-file change invalidates downstream gates and restarts review from Spec.

## 8. Delivery status

BE06 may be reported locally complete only after implementation, all review gates, real fixture validation, evidence updates, and exact cleanup pass. `go-018` remains `in_progress` until the separately authorized frontend/backend contract alignment, joint browser/API acceptance, production migration/deployment, public HTTPS validation, and real upstream evidence are complete.
