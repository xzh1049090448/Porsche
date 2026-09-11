# Platform single-generation SSE v2 design (BE05)

Date: 2026-09-10

Status: approved for implementation planning

Scope owner: Porsche backend

Related product contract: `/Users/xuzhihao/code/Porsche-Web/.worktrees/chat-streaming-prd/docs/superpowers/specs/2026-09-07-chat-adaptive-character-streaming-prd.md`

Depends on:

- BE01 strict `platform-chat-sse.v2` request projection and SSE encoder;
- BE02 Redis generation registry and ordered per-model sequence state;
- BE03 receipt-backed atomic generation persistence;
- BE04 generation GET/cancel, 30-second owner-bound runner leases, convergence worker, and application lifecycle, merged to `main` by PR #12 at `fd243c6995c8213e76ce5b0007df3413dd1a7c97`.

## 1. Decision and objective

BE05 activates strict SSE v2 for the single-model form of `POST /api/v1/platform/chat/completions`. It adds one application-managed generation runner that owns the upstream request, renews the BE04 lease every 10 seconds, projects only validated text deltas into SSE v2, accumulates the final assistant reply in memory, and commits the complete exchange through BE03 before emitting global success.

The runner is deliberately detached from the HTTP request's cancellation after a successful generation claim. Closing, reloading, or losing the client connection stops only downstream SSE writes. The backend continues upstream generation and persistence, and the client can later obtain the durable terminal result through the existing authenticated generation GET endpoint. Explicit generation cancellation and application shutdown still stop upstream work.

The selected architecture keeps the request-started runner in the current application process. It does not introduce a channel-backed queue or durable job system. This is sufficient for client-disconnect continuity; it does not promise cross-process upstream resumption after a crash.

The user-approved failure policy discards partial assistant content whenever upstream generation fails, times out, is malformed, ends prematurely, is explicitly cancelled, or is stopped during shutdown. Failed and cancelled work creates no BE03 receipt, message, usage record, token increment, or successful-call quota charge.

This approval covers local design, implementation, tests, and commits only. It does not authorize push, merge, production migration, deployment, production data changes, or a real paid upstream call.

## 2. Goals

BE05 must provide all of the following:

1. Activate single-model `platform-chat-sse.v2` completion requests while preserving legacy request behavior.
2. Enforce authentication, strict v2 validation, current model authorization, quota preflight, and generation claim before any upstream call.
3. Return duplicate generation claims as HTTP `409` with the authoritative generation state and make zero upstream calls.
4. Emit ordered, sanitized `meta -> delta* -> model_done -> done` success streams using the existing BE01 encoder.
5. Keep an accepted generation running after downstream write failure or client disconnection.
6. Renew the owner-bound BE04 lease every 10 seconds while upstream work is active.
7. Stop promptly on explicit cancel, lost lease authority, configured upstream timeout, or application shutdown.
8. Reserve a conversation snowflake GUID before the first SSE frame without inserting an empty conversation, then persist that exact GUID atomically on success.
9. Use BE03 as the only success transaction and BE04 GET as the durable result retrieval path.
10. Preserve stable public errors, privacy boundaries, quota rules, and lifecycle convergence under dependency races.

## 3. Non-goals

BE05 does not:

- activate or implement `POST /api/v1/platform/chat/compare` v2 fan-out; that remains BE06;
- provide reconnect-to-live-stream, event replay, multi-subscriber fan-out, or a resumable upstream cursor;
- resume an unfinished upstream call after process crash, host loss, or deployment replacement;
- introduce a database migration, durable job queue, Redis stream/list, distributed runner bus, or leader election;
- persist partial assistant text in Redis, MySQL, logs, files, diagnostics, or error envelopes;
- change the BE03 receipt schema or the BE04 public GET/cancel response contracts;
- change legacy completion, legacy streaming, compare, or non-streaming behavior;
- implement frontend playback, polling, or browser acceptance;
- implement a separate provider-cost ledger for failed/cancelled requests;
- run production migration `0011`, push, merge, deploy, or call a real paid upstream.

## 4. HTTP admission and route behavior

The existing authenticated completion route continues to distinguish legacy requests from exact `stream_version: "platform-chat-sse.v2"` requests. Only strict v2 single requests enter BE05. Legacy requests retain their current handler and service behavior.

Before claiming a generation or committing SSE headers, the handler/service boundary must complete:

1. authentication through the existing platform user middleware;
2. strict JSON decoding and BE01 v2 projection;
3. `stream: true`, canonical lowercase UUID `generation_id`, exactly one valid model, and rejection of unknown platform fields;
4. a non-empty, well-formed UTF-8 final user message within the existing message byte limit;
5. optional conversation GUID parsing plus active current-user ownership when an existing conversation is requested;
6. current model catalog and ACL authorization;
7. read-only quota admission sufficient for one successful model;
8. availability of the generation store, persistence service, runner registry, and upstream adapter;
9. one BE02/BE04 owner-scoped generation claim.

Validation and authorization failures occur before upstream work. Existing stable JSON mappings are retained: invalid requests use `400`, unauthorized models use the existing `403` boundary, duplicate claims use `409`, quota admission uses `429`, and unavailable v2 dependencies use `503`. Authentication remains the middleware's `401` response.

A duplicate claim always returns the authoritative current lifecycle view in the established conflict envelope. It never attaches to a live runner, replays prior deltas, replaces the registered runner, renews the prior lease, or calls upstream.

After a new claim succeeds, the response switches irrevocably to `platform-chat-sse.v2`. Later operational failures are expressed only as v2 SSE when the writer is still usable; the handler must not append a JSON error or raw upstream body to an SSE response.

## 5. Reserved conversation identity

The v2 `meta` event requires a conversation GUID before upstream completion, while BE03 intentionally creates no conversation for failed or cancelled generations. BE05 resolves that tension without inserting an empty row:

- an existing-conversation request resolves and retains the authenticated active conversation GUID during admission;
- a new-conversation request obtains one positive signed 64-bit snowflake GUID from the shared `persistence.NextGUID` generator before `meta`;
- reserving the number is an in-memory allocation only; skipped snowflake values are valid and no database row exists yet;
- the reserved GUID is rendered as a decimal string in `meta` and retained only by the runner until finalization;
- BE03's typed persistence input is extended to distinguish `requested existing conversation` from `requested new conversation with this server-reserved GUID`;
- on successful finalization BE03 creates the new conversation with exactly the reserved GUID while storing `requested_existing_conversation = 0` in the receipt;
- duplicate receipt comparison continues to treat that exchange as originally requesting a new conversation, not as an existing-conversation retry.

The reserved GUID is server-generated and cannot be supplied or changed by the client. The BE03 transaction must still fail on a GUID collision and roll back every effect. BE05 adds no schema change and continues to follow `docs/conventions/database-standards.md`.

## 6. Application-managed runner

### 6.1 Ownership

`PlatformSingleGenerationRunner` is the orchestration component. It receives injected interfaces for the generation store, persistence finalizer, upstream stream adapter, clock/timers, GUID generation, and output sink. It owns orchestration only; parsing, Redis transitions, SQL finalization, and SSE framing remain in their existing focused components.

After claim, the handler registers the runner under authenticated internal `users.id + generation_id` in an application-owned registry. Registration uses an unguessable compare-and-delete token and fails closed if a runner is already present. Cleanup is idempotent and cannot remove a newer owner.

The runner context is derived from the application lifecycle, not from `http.Request.Context()`. It is additionally bounded by the existing configured upstream request timeout and can be cancelled by the local explicit-cancel callback. BE05 adds no separate fixed generation timeout.

### 6.2 Downstream output

While the HTTP connection is usable, the runner synchronously writes each complete BE01 frame and flushes it. There is no unbounded frame queue. The output sink is concurrency-safe or single-owner so lease/cancel logic never writes frames concurrently with upstream delta handling.

The first downstream write or flush failure atomically disables all later output. It does not cancel the runner, upstream request, lease renewal, or persistence. Once detached, no later error is written to the dead client; the client uses GET for status/result.

The request goroutine becomes the application-managed runner and does not return from the handler until the runner reaches terminal cleanup. Client cancellation is deliberately not used as the runner context after claim. A write/flush failure only disables later output; the same execution path keeps driving upstream, renewal, and persistence while the application registry makes it cancellable during explicit cancel or shutdown. The runner releases the `http.ResponseWriter` when it exits, and no goroutine writes after the handler returns.

### 6.3 Upstream stream projection

BE05 adds a strict adapter/parser for the currently supported white-label upstream streaming format. It accepts only the documented safe text-delta and terminal/usage fields needed by the platform contract. It must not reuse the legacy path's raw frame forwarding or expose bare `[DONE]` to the v2 client.

Unknown provider fields are ignored only when the documented envelope remains structurally valid. Malformed JSON, invalid UTF-8, invalid event shape, non-text content, inconsistent terminal data, missing terminal completion, or impossible token values fail the generation with a stable public code. Raw provider bodies and errors never reach SSE, Redis, MySQL, or logs.

Empty upstream deltas do not produce v2 delta events. Each non-empty safe delta is appended to the bounded in-memory assistant buffer, assigned the next sequence beginning at 1, recorded in Redis, then emitted to the connected client using the same sequence. Redis is therefore the sequence authority even though it stores no text.

The accumulated assistant content must remain well-formed UTF-8 and at most the existing MySQL message content limit of 65,535 bytes. The runner checks the prospective total before appending or emitting a delta that would exceed the limit. Overflow stops upstream, discards the partial buffer, and fails with stable `upstream_error`.

## 7. Lease renewal and authority loss

The runner retains the raw lease capability returned only to the successful claim owner. It renews every 10 seconds while upstream work remains active; each successful renewal extends the BE04 deadline to `now + 30 seconds`. Timer behavior is injected so boundary tests do not sleep.

Renewal stops permanently after the runner leaves `running`, upstream work ends, commit begins, cancellation wins, or the runner exits. A renewal result is authoritative:

- `running` with the same capability permits work to continue;
- `cancelling` triggers immediate upstream cancellation and local cancellation acknowledgement;
- `cancelled`, `failed`, `committing`, or `completed` stops upstream and obeys that state without attempting to overwrite it;
- wrong capability, expired lease, malformed state, Redis unavailability, or an unresolved store error stops upstream and prevents persistence.

When Redis is unavailable, BE05 cannot prove ownership and must not continue producing a result that could later overwrite another authority. It cancels upstream, discards partial content, and leaves BE04 expiry/restart convergence to determine the final Redis state when dependencies recover. It must not fabricate a successful or failed mutation without a proven current snapshot.

## 8. Successful event and persistence sequence

For a connected client, the normal sequence is:

1. claim creates `running` and returns the lease capability;
2. runner registration succeeds;
3. emit one `meta` containing schema, generation ID, conversation GUID, and the one claimed model;
4. for each non-empty validated text piece, call Redis `RecordDelta(seq)` and emit the matching `delta` only after Redis accepts it;
5. after a valid upstream terminal, call Redis `MarkModelDone(last_seq)`;
6. atomically win `running -> committing` through `BeginCommit`;
7. emit `model_done` with the accepted `last_seq`;
8. invoke BE03 with the exact claimed identity, final user message, resolved/reserved conversation identity, accumulated assistant content, sanitized token count, and terminal sequence;
9. after the MySQL receipt transaction succeeds, call Redis `Complete` with the exact committed assistant-message GUID;
10. load/use the committed account total and emit the unique global `done` only after Redis confirms `completed`;
11. stop renewal, unregister the runner, release in-memory content, and exit.

`model_done` means that the model stream has ended and the generation is committing; it is not the durable success acknowledgement. `done` is the only live-stream proof that both BE03 and Redis completion succeeded. If the client detached at any point, the same state and persistence sequence continues without output, and GET later hydrates the completed receipt.

The runner never calls BE03 while Redis remains `running`, `cancelling`, or `cancelled`. Only the winner of `BeginCommit` may attempt persistence.

## 9. Commit-unknown and completion acknowledgement

BE03 already resolves uncertain MySQL commit outcomes by querying the durable receipt on an independent bounded context. BE05 must consume that typed outcome rather than replaying the SQL mutation.

- a validated matching receipt is treated as committed and is used for idempotent Redis completion;
- proven absence before the convergence deadline leaves `committing` for receipt-aware BE04 reconciliation;
- malformed/mismatched receipt or unavailable recovery access fails closed and returns no content;
- an unresolved Redis completion acknowledgement never emits global `done`.

If the writer remains connected and the runner can prove a terminal failure, it emits the appropriate stable v2 terminal pair. If completion remains unproven because a dependency is unavailable, it emits a stable error if possible and leaves GET/BE04 reconciliation to expose the later authoritative result. It never changes an uncertain commit to an unconditional failed state.

## 10. Failure, cancellation, and shutdown semantics

### 10.1 Upstream or validation failure after `meta`

Timeout, connection failure, malformed chunks, invalid terminal data, premature EOF, encoder failure, or content overflow cancels the upstream stream and discards all accumulated assistant content. While Redis authority is still `running`, the runner uses a new owner-bound fail transition that verifies the lease capability before changing still-running model state and generation state to `failed`.

The public mapping uses only the existing allowlist: `gateway_upstream_error`, `invalid_request`, `rate_limited`, `cancelled`, `timeout`, `internal_error`, and `upstream_error`. Provider-specific details are mapped internally to one stable code.

If the client is still connected, the terminal sequence is `model_error` followed by global `error`, both carrying the same stable code and only an already-sanitized optional request ID. Failed work does not call BE03 and does not consume successful-call quota.

### 10.2 Explicit cancellation

The existing cancel endpoint atomically moves `running -> cancelling` before invoking the process-local runner callback. The runner cancels upstream, discards partial content, stops renewal, and acknowledges cancellation through a capability-bound `cancelling -> cancelled` transition. A cancellation acknowledgement cannot cancel a different/replacement runner and cannot overwrite `committing`, `completed`, or `failed`.

If connected, the runner emits `model_error(code=cancelled)` followed by `error(code=cancelled)` only after Redis confirms the cancelled authority. The HTTP cancel endpoint retains BE04's `200`/`202` bounded-wait behavior.

### 10.3 Application shutdown

`State.Close` first prevents new runner registration, then cancels every active runner through the application lifecycle context. Each runner stops upstream and renewal, discards partial content, and immediately attempts an owner-bound `running -> failed` transition with `internal_error` before Redis is closed. A runner already in `cancelling` acknowledges `cancelled`; a runner already in `committing` is left for receipt-aware reconciliation and is never overwritten.

Shutdown waits a bounded interval for active runners to finish their terminal acknowledgement and cleanup. The convergence worker stops only after runner cancellation has begun, and the generation Redis client closes only after the bounded runner/worker drain. Repeated close is safe. If Redis cannot acknowledge a still-running record, later lease expiry and restart convergence remain authoritative.

Application shutdown is an operational failure, not a user cancellation, unless a prior explicit cancel already won.

## 11. Store and persistence extensions

BE05 may add narrowly typed operations while preserving BE02/BE04 identity, CAS, strict decoding, TTL, and privacy rules:

- `FailRunningOwned`: `running -> failed` only when the presented lease capability matches; it fails every still-running model with one stable code and clears lease fields atomically;
- `AcknowledgeCancelledOwned`: `cancelling -> cancelled` only for the runner that presents the capability issued by the original claim; it cancels every still-running model and clears lease fields;
- existing `RecordDelta`, `MarkModelDone`, and `BeginCommit` must be invoked only by the registered owner; if their current signatures cannot prove that, BE05 must introduce owner-bound variants or a single capability-bound runner store facade rather than relying on process-local convention;
- every mutation preserves the original 24-hour TTL with `KEEPTTL` and returns an authoritative snapshot on conflicts when one can be resolved;
- terminal states are immutable and idempotent matching acknowledgements do not refresh TTL.

The persistence input gains an explicit reserved-new-conversation field/state. Exactly one of these forms is valid:

- existing conversation GUID, with receipt provenance `requested_existing_conversation = 1`; or
- server-reserved new conversation GUID, with receipt provenance `requested_existing_conversation = 0`.

Neither form permits zero, negative, client-selected, or simultaneous GUIDs. Exact duplicate receipt comparison includes the resolved conversation GUID while retaining the original new-versus-existing provenance rule.

No database migration is required.

## 12. Quota, usage, and audit

Admission performs only a read-only quota preflight. One daily call and the successful token total are charged exclusively inside the BE03 transaction after a successful upstream terminal and `BeginCommit` win. The locked user row remains authoritative; a concurrent quota loss rolls back every SQL effect and produces a stable failed generation.

Failed, cancelled, timed-out, oversized, malformed, disconnected-but-eventually-failed, or shutdown-stopped work does not consume successful-call quota and does not increment `total_tokens_used`. A disconnected generation that later completes does consume quota normally because the outcome, not the connection, determines charging.

Provider cost already incurred by unsuccessful work is outside BE05's success accounting. Diagnostics may record a sanitized operational category, model count, duration, lifecycle state, and hash-safe correlation value. They must not invent a cost or write it into user usage/quota records.

## 13. Security and privacy

- Every store, registry, receipt, conversation, and quota operation is scoped by authenticated internal `users.id`.
- Client values never select a user, lease, runner token, conversation internal ID, message GUID, receipt GUID, audit actor, sequence, or token total.
- Redis stores lifecycle metadata and message references only; it never stores prompt or reply text.
- Partial assistant content lives only in the runner's bounded memory and is zeroed/released by losing references at terminal cleanup; it is never returned by GET.
- The registry contains only owner identity, cancellation function/context, and compare-and-delete token. It contains no prompt, reply, credential, response writer, upstream body, or lease digest in logs/DTOs.
- Logs and public errors exclude prompt, reply, Authorization, raw lease token/digest, Redis key/URL, database address, SQL, upstream URL/body, provider credentials, and raw Go errors.
- SSE contains only BE01 allowlisted fields and stable codes. No raw upstream frame is forwarded.
- Existing-conversation ownership is checked before claim and repeated defensively inside BE03's transaction.
- A failure to prove current Redis authority stops work rather than weakening ownership or idempotency.

## 14. Testing strategy

Implementation follows test-driven development. Missing real fixture configuration is a named blocker/skip and cannot be counted as a pass.

### 14.1 Pure unit and HTTP contract tests

- exact pre-SSE JSON responses for authentication, invalid request, unknown field, invalid model, unauthorized model, insufficient quota, missing dependency, cancellation tombstone, and duplicate `409` authority;
- duplicate claims, including concurrent claims and prior tombstones, make zero upstream calls and never register/replace a runner;
- exact connected success frames and ordering: one `meta`, contiguous `delta` sequences from 1, one `model_done`, and one `done` after completion;
- exact connected failure frames: `meta`, any already-sent valid deltas, then `model_error -> error`, with no success terminal;
- request projection sends no platform-only fields upstream and legacy requests retain current behavior;
- strict upstream parsing covers fragmented reads, multiple frames per read, UTF-8 boundaries, empty deltas, ignored safe fields, malformed JSON, wrong shape, premature EOF, duplicate/conflicting terminal, invalid usage, and raw `[DONE]` rejection/projection;
- writer/flush failure disables output once while upstream, renewal, finalization, and cleanup continue;
- accumulated content boundary accepts exactly 65,535 bytes and rejects the next byte before Redis delta/SSE/persistence;
- encoder/store/persistence errors never append JSON or raw dependency text to SSE.

### 14.2 Runner and lifecycle tests

- fake clock proves renewals at 10 seconds extend the 30-second lease without real sleeps;
- client context cancellation and writer failure do not cancel upstream;
- configured upstream timeout does cancel upstream and produces `timeout` without persistence or quota;
- explicit cancel stops upstream, acknowledges `cancelled`, discards content, and cleans registry state;
- application close rejects new runners, cancels/drains active runners, marks still-running work `failed/internal_error` before Redis close, preserves `committing`, and remains idempotent;
- cancellation, renewal, delta, model-done, begin-commit, and shutdown races have one authoritative winner;
- Redis unavailable during renewal stops upstream and leaves later BE04 convergence possible;
- registry compare-and-delete cleanup cannot remove a newer registration and leaves no goroutine/timer leaks.

### 14.3 Real Redis and MySQL tests

Using explicitly isolated Redis 7 and MySQL 8.4 with the current migration ledger `0001` through `0013` (including BE03 receipt migration `0011`):

- new-conversation success emits/reserves one GUID and commits exactly that GUID with receipt provenance `requested_existing_conversation = 0`;
- existing-conversation success retains exact ownership/provenance and updates through BE03 only;
- successful single generation creates one user message, one assistant message, one usage row, one receipt/result, one daily-call charge, and exact token totals;
- disconnected success persists normally and GET hydrates the final content;
- every failure/cancel/shutdown path creates no conversation/message/usage/receipt and charges no quota;
- owner-bound mutation attempts with wrong, stale, expired, or foreign capabilities fail under concurrency;
- 10-second renewal prevents the BE04 worker from failing a live runner; stopped renewal allows 30-second convergence;
- commit-unknown with a matching receipt completes Redis without a second SQL effect; receipt absence and dependency failure follow BE03/BE04 authority;
- multi-instance GET/cancel/convergence races never duplicate upstream admission, persistence, quota, or terminal transitions.

### 14.4 Delivery gates

The implementation candidate must pass:

```bash
go test ./internal/service ./internal/handler ./internal/app ./internal/router -count=1
go test -race ./internal/service ./internal/handler ./internal/app ./internal/router -count=1
go test ./... -count=1
go build ./...
go vet ./...
git diff --check
```

Real fixture suites must select and pass their intended tests with zero BE05 fixture skips. Privacy scans must prove no unique prompt/reply/lease/upstream sentinel appears in Redis, receipts, logs, public DTOs, or error output. Existing BE01-BE04, legacy platform chat, control endpoint, migration, and application-close regressions must remain green.

## 15. Delivery boundary and follow-up

BE05 is complete only when single-model v2 admission, application-managed disconnected execution, safe upstream projection, 10-second lease renewal, exact SSE ordering, BE03 finalization, owner-bound cancellation/failure, shutdown drain, and GET-based durable retrieval pass the required unit, race, real Redis/MySQL, full regression, build, vet, diff, and privacy gates.

Completion of BE05 does not complete `go-018`. BE06 compare streaming, frontend/backend joint acceptance, production migration `0011`, deployment, public HTTPS validation, and real upstream model calls remain separate, explicitly authorized work.
