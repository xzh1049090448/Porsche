# Platform generation persistence design (BE03)

Date: 2026-09-08

Status: approved for implementation planning

Scope owner: Porsche backend

Related product contract: `/Users/xuzhihao/code/Porsche-Web/.worktrees/chat-streaming-prd/docs/superpowers/specs/2026-09-07-chat-adaptive-character-streaming-prd.md`

## 1. Decision and objective

BE03 adds the durable MySQL commit boundary required by `platform-chat-sse.v2`. A successful generation must persist its complete exchange, including exactly one non-empty final user message, quota and token effects, usage records, and a durable generation receipt in one MySQL transaction. The receipt is the recovery proof used when the process crashes or Redis cannot be updated after MySQL commits.

The user approved adding forward migration `0011` for local development and test implementation. The user also approved tightening v2 model identifiers to well-formed UTF-8 of at most 128 bytes so they fit the existing `conversations.model`, `messages.model`, and `usage_records.model` columns without altering legacy tables. The generic v2 opaque-identifier guard remains 255 bytes and also requires well-formed UTF-8; the model-specific limit must be enforced before Redis or MySQL. These approvals do not authorize running a production migration, deploying, pushing, merging, or calling a real upstream model.

BE03 remains below the HTTP boundary. It introduces the schema, persistence models, transactional finalizer, receipt reader, and Redis reconciliation primitives needed by later tranches, but it does not activate v2 stream, generation status, or cancellation routes.

## 2. Goals

BE03 must provide all of the following:

1. A forward-only `0011` migration containing durable generation receipt and per-model result tables that comply with `docs/conventions/database-standards.md`.
2. One transactional finalization service for single and compare generations.
3. Exactly one non-empty user message per successful generation exchange and one independent assistant message per successful model.
4. A permanent idempotency boundary on authenticated `users.id` plus canonical client `generation_id`.
5. Exact agreement among successful assistant-message token counts, usage records, `users.total_tokens_used`, daily call consumption, and receipt result metadata.
6. A durable way to distinguish “MySQL committed, Redis not completed” from “MySQL did not commit.”
7. A receipt reader that returns only owned, active database records and never trusts client-supplied ownership or message references.
8. Stable failure behavior that never exposes or persists cancelled/failed partial model text as a successful reply.

## 3. Non-goals

BE03 does not:

- register or activate `POST /api/v1/platform/chat/completions` v2 behavior;
- register `GET /api/v1/platform/chat/generations/{generation_id}` or `POST .../cancel`;
- call the white-label adapter or any real/paid upstream;
- implement live SSE fan-out, context cancellation, browser integration, Nginx/CDN configuration, public HTTPS acceptance, or frontend playback;
- change legacy streaming, legacy compare aggregation, or ordinary non-streaming behavior;
- persist prompt or reply text in Redis or in the generation receipt tables;
- persist cancelled/failed partial assistant replies or charge their daily call quota;
- implement the separate incurred-upstream-cost audit/ledger boundary;
- apply migration `0011` outside an explicit isolated local/test fixture;
- push, merge, deploy, or update production data.

## 4. Existing constraints

BE01 supplies strict v2 request projection and a sanitized, ordered SSE encoder. BE02 supplies a Redis registry with authenticated ownership, 24-hour TTL, per-model sequence state, atomic cancel-versus-commit transitions, and final assistant-message GUID references. Neither tranche activates a v2 route.

The existing legacy chat path is not an acceptable persistence primitive for v2 because it performs independent writes and consumes daily quota before upstream completion. Its compare path also stores one `__MULTI_MODEL__` aggregate assistant message. BE03 therefore adds a separate v2 finalizer and does not silently change the legacy methods.

The current shared `whitelabel.ValidateRequest` contract accepts an empty `messages` array and does not require a final user message. BE03 deliberately tightens only the inactive v2 durable-generation contract: every successful v2 exchange must have one final user message whose content is non-empty, well-formed UTF-8, and within the existing text-column byte bound; completed assistant content has the same UTF-8 and byte requirements. Whitespace is preserved and is not normalized for idempotency. The future v2 HTTP/orchestration tranche must reject a request with no valid non-empty final user message before Redis claim or upstream work; the BE03 finalizer repeats the check defensively before any dependency access. Legacy request validation and legacy chat behavior remain unchanged.

Redis and MySQL cannot participate in one atomic transaction. The fixed cross-store order is:

1. Redis wins `running -> committing`.
2. MySQL commits the complete exchange and durable receipt in one transaction.
3. Redis moves `committing -> completed` with the committed assistant-message GUIDs.
4. A later stream tranche may send the unique `done` event only after step 3 succeeds.

The MySQL receipt is the authoritative proof for recovery across the gap between steps 2 and 3.

## 5. Migration 0011 schema

Migration `0011` adds two normalized tables. It does not alter existing `users`, `conversations`, `messages`, or `usage_records` tables and must not use GORM `AutoMigrate`.

All primary and foreign keys are signed `BIGINT`. Every table contains an internal `id`, a snowflake-generated public/business `guid`, Unix-millisecond audit timestamps, actor IDs, and `is_deleted`. Enum values are explicit stable integers rather than strings or native MySQL enums. Their Go types provide explicit `String` and `Parse` mappings for `single`/`compare` and `completed`/`failed`; unknown integers render as `unknown`, while unrecognized strings return the zero value plus `false`.

### 5.1 `platform_chat_generation_receipts`

This parent row is written only inside a successful finalization transaction. Its existence means the exchange and all associated accounting writes committed atomically.

| Column | Type and constraints | Meaning |
| --- | --- | --- |
| `id` | `BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY` | Internal database key only. |
| `guid` | `BIGINT NOT NULL` | Server-generated snowflake business identifier. |
| `user_id` | `BIGINT NOT NULL` | Authenticated owner; references `users.id`. |
| `generation_id` | `CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL` | Canonical lowercase client UUID used only with `user_id`. |
| `mode` | `INT NOT NULL` | `1 = single`, `2 = compare`; values are permanent. |
| `conversation_id` | `BIGINT NOT NULL` | Internal reference to the committed conversation. |
| `user_message_id` | `BIGINT NOT NULL` | Internal reference to the exchange's one committed user message. |
| `successful_model_count` | `INT NOT NULL` | Number of successful model results; at least one. |
| `daily_calls_charged` | `INT NOT NULL` | Must equal `successful_model_count`. |
| `total_tokens` | `BIGINT NOT NULL` | Sum of successful per-model token counts. |
| `committed_at` | `BIGINT NOT NULL` | UTC Unix milliseconds for the commit decision. |
| `created_at` | `BIGINT NOT NULL` | UTC Unix milliseconds. |
| `created_by` | `BIGINT NULL` | Authenticated `users.id`; non-null for BE03 writes. |
| `updated_at` | `BIGINT NOT NULL` | Equals `created_at` because receipts are immutable. |
| `updated_by` | `BIGINT NULL` | Authenticated `users.id`; non-null for BE03 writes. |
| `is_deleted` | `INT NOT NULL DEFAULT 0` | Logical deletion flag; BE03 exposes no delete path. |

Required indexes and constraints:

- unique `guid`;
- unique `(user_id, generation_id)` without `is_deleted`;
- globally unique `user_message_id`, so one user message cannot prove two generation receipts;
- index `(user_id, is_deleted, created_at)`;
- index `(conversation_id, is_deleted)`;
- foreign keys from `user_id` to `users.id`, `conversation_id` to `conversations.id`, and `user_message_id` to `messages.id`, all `ON DELETE RESTRICT ON UPDATE RESTRICT`;
- checks that `mode IN (1,2)`, `successful_model_count >= 1`, `daily_calls_charged = successful_model_count`, `total_tokens >= 0`, `committed_at > 0`, and `is_deleted IN (0,1)`.

The `(user_id, generation_id)` uniqueness is intentionally permanent. A client can always generate a new UUID, while allowing a logically deleted generation ID to be reused would break idempotency and recovery. This is not a “soft-delete then reusable” business identifier. The receipt stores only `user_message_id`, never user-message content; exact duplicate comparison loads that owned message through the receipt integrity graph.

### 5.2 `platform_chat_generation_results`

This child table records one row for every requested model in original request order. It stores result metadata and references only; it never duplicates assistant content.

| Column | Type and constraints | Meaning |
| --- | --- | --- |
| `id` | `BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY` | Internal database key only. |
| `guid` | `BIGINT NOT NULL` | Server-generated snowflake business identifier. |
| `receipt_id` | `BIGINT NOT NULL` | Internal parent receipt reference. |
| `model_index` | `INT NOT NULL` | Zero-based request order. |
| `model` | `VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL` | Validated opaque, case-sensitive model identifier from the claimed Redis identity; v2 rejects values over 128 UTF-8 bytes before Redis or MySQL. |
| `status` | `INT NOT NULL` | `1 = completed`, `2 = failed`; values are permanent. |
| `assistant_message_id` | `BIGINT NULL` | Successful result's internal message reference. |
| `tokens` | `BIGINT NOT NULL DEFAULT 0` | Successful result token count; zero for failed results. |
| `error_code` | `VARCHAR(64) NULL` | Stable allowlisted code for a failed model; never raw upstream text. |
| `created_at` | `BIGINT NOT NULL` | UTC Unix milliseconds. |
| `created_by` | `BIGINT NULL` | Authenticated `users.id`; non-null for BE03 writes. |
| `updated_at` | `BIGINT NOT NULL` | Equals `created_at`; result rows are immutable. |
| `updated_by` | `BIGINT NULL` | Authenticated `users.id`; non-null for BE03 writes. |
| `is_deleted` | `INT NOT NULL DEFAULT 0` | Logical deletion flag; BE03 exposes no delete path. |

Required indexes and constraints:

- unique `guid`;
- unique `(receipt_id, model_index)`;
- unique `(receipt_id, model)` under binary `utf8mb4_bin` comparison so case-distinct opaque model IDs remain distinct;
- unique nullable `assistant_message_id`, so one message cannot prove two results;
- index `(receipt_id, is_deleted)`;
- foreign keys from `receipt_id` to the parent receipt and `assistant_message_id` to `messages.id`, both `ON DELETE RESTRICT`;
- checks that `model_index >= 0`, `status IN (1,2)`, `tokens >= 0`, and `is_deleted IN (0,1)`;
- completed invariant: `status = 1`, `assistant_message_id IS NOT NULL`, and `error_code IS NULL`;
- failed invariant: `status = 2`, `assistant_message_id IS NULL`, `tokens = 0`, and `error_code IS NOT NULL`.

For single mode there is exactly one result at index zero and it must be completed. For compare mode there are two or three rows in the claimed model order, with at least one completed row. An all-model failure does not create a receipt because there is no successful generation exchange to persist.

### 5.3 Migration behavior

`0011` is additive and forward-only. Its up migration creates the parent before the child. It uses explicit table and constraint names, `utf8mb4` for normal text, binary `utf8mb4_bin` comparison for opaque case-sensitive model IDs, ASCII binary comparison for canonical generation UUIDs, and the repository's existing InnoDB conventions. The result table itself retains the repository default `utf8mb4_unicode_ci` collation; only its `model` column overrides that default.

The down migration exists only for disposable test rollback and drops the child before the parent. Production recovery remains a separately approved forward migration; BE03 does not authorize destructive production rollback.

The migration runner and published checksum tests must advance from ten migrations ending at `0010` to eleven ending at `0011` without changing any existing migration bytes or checksums. Re-running an already applied `0011` through the ledger is a no-op. Tests must also reject a partial or structurally incompatible schema rather than accepting it as applied.

## 6. Persistence input and ownership

The v2 finalizer accepts an internal, typed value assembled after upstream processing. It is not an HTTP DTO. The value contains:

- authenticated `user_id` and canonical `generation_id`;
- mode and the exact ordered model list already claimed in Redis;
- optional existing conversation GUID or the instruction to create a new conversation;
- exactly one final user message whose content is non-empty and is persisted byte-for-byte once;
- one terminal result for each claimed model, including the final nonnegative JS-safe Redis sequence supplied later by BE05;
- completed result content and a nonnegative token count;
- failed result stable code and no content;
- one transaction timestamp and the authenticated actor ID.

Before opening the transaction, the service validates all scalar bounds, mode/model cardinality, well-formed UTF-8 and the approved 128-byte v2 model limit, the non-empty well-formed UTF-8 final user message, exact model order, terminal result coverage, stable error codes, well-formed UTF-8 completed content and its existing message byte limit, nonnegative safe token and sequence integers, and authenticated ownership. Task 5 accepts single mode only and rejects compare before any Redis, receipt, or MySQL access. It then reads the current Redis snapshot and requires the same user-scoped generation to be `committing` with identical mode, models, per-model terminal states, error codes, and final sequences, and requires the transaction timestamp to be at least the Redis snapshot's `updated_at_ms`. A mismatch or stale timestamp returns conflict before any MySQL mutation.

The sequence is a Redis precondition only. Receipts do not persist it, no schema column is added, and duplicate receipt comparison continues over durable receipt/message fields rather than retry-local sequence metadata.

The client cannot supply conversation IDs, message IDs, receipt GUIDs, audit fields, quota counts, or total-token aggregates. Those values are resolved or generated by the service.

## 7. Transaction boundary and lock order

Finalization serializes one `user_id + generation_id` through a deterministic MySQL advisory lock. `GET_LOCK`, the locked Redis identity recheck, the SQL transaction, and `RELEASE_LOCK` use one pinned physical connection through independent clean GORM sessions. After acquiring the advisory lock and before beginning the SQL transaction, finalization re-reads Redis and requires the generation to remain the same matching `committing` snapshot. A reconciliation winner that already changed Redis to `failed` or `completed` therefore returns conflict without opening a SQL transaction. Only a successful locked recheck is allowed to call `db.Transaction`. Release is always attempted on that same connection using a short, bounded context derived from `context.Background()`, so caller cancellation cannot strand cleanup indefinitely. A callback error remains the primary result even if cleanup also fails; after a successful callback, a release error or a `0`/`NULL` release result fails closed as persistence unavailable.

The complete SQL effect occurs in one `db.Transaction` callback. The fixed lock/write order is:

1. Lock the active owner row by `users.id` using `SELECT ... FOR UPDATE`; reject missing, disabled, or logically deleted users, and reject a transaction timestamp older than the locked user's `updated_at` or non-null `daily_calls_reset_at`.
2. Re-evaluate the UTC daily reset and available daily quota on the locked row without trusting the stale middleware user object.
3. If an existing conversation GUID was supplied, load and lock the active conversation by `guid + user_id + is_deleted = 0` and reject a transaction timestamp older than its `updated_at`. Otherwise create one new conversation with a server snowflake GUID.
4. Insert exactly one user message from the already validated non-empty final user-message content, retain its internal ID for the receipt, and apply the existing title rule. For a new conversation, compute the final title before `CREATE`; valid whitespace-only content remains byte-exact in the message and keeps the `新对话` fallback without a redundant update.
5. In request model order, create one independent assistant message for every completed model. Failed compare models create no message.
6. Insert one usage record per completed model with the exact same model and token count as its assistant message.
7. Increment `users.daily_calls_used` by the number of completed models and `users.total_tokens_used` by their token sum. Persist the UTC daily reset timestamp when reset was required. Update only these counters/reset and `updated_at`/`updated_by` through an active-user predicate; require exactly one affected row.
8. Insert the parent receipt with the new user-message ID and all ordered child result rows using the newly created internal conversation/assistant-message IDs.
9. Commit once.

Every insert receives a fresh snowflake GUID and explicit audit fields from the same transaction timestamp. Existing-conversation updates are restricted to `title`, `updated_at`, and `updated_by` through the locked active owner predicate. A known no-op against the already locked active owned row skips the update; an attempted update still requires exactly one affected row. New conversations need no audit update after creation. User updates are restricted to quota/token/reset and update-audit fields. Neither path uses GORM `Save`, rewrites immutable/authentication fields, or permits upsert fallback. Legacy public behavior remains unchanged.

No success DTO, message GUID, or quota mutation is published before commit returns success. A callback error rolls back the conversation, title change, user message, assistant messages, usage rows, user counters, receipt, and result rows together.

MySQL commit can return an uncertain transport error. The service must not blindly replay the callback. It resolves the outcome by querying the receipt with `(user_id, generation_id)` on a fresh database operation using a short bounded context derived independently from caller cancellation: an active, internally consistent receipt means committed success; absence means no proven success and preserves an original typed quota/conflict/invalid result or otherwise returns unavailable. A malformed or partial receipt is returned explicitly as an integrity error; an unavailable recovery read remains unavailable.

## 8. Single and compare persistence semantics

### 8.1 Single

A successful single generation creates or resolves one conversation, creates exactly one non-empty user message, creates exactly one assistant message, creates one usage record, charges one daily call, adds the model tokens to `total_tokens_used`, and writes one parent receipt referencing that user message plus one completed result row.

A failed or cancelled single generation calls no BE03 finalizer and creates no conversation, message, usage, receipt, token increment, or daily-call increment.

### 8.2 Compare

A successful compare generation creates or resolves one shared conversation and creates exactly one non-empty user message referenced by the parent receipt. Each successful model creates its own normal assistant message with that model and token count. Each failed model creates no assistant message and no usage record, but receives one failed receipt result containing only its stable code.

Compare v2 never writes the legacy `__MULTI_MODEL__` aggregate message. That format remains unchanged for legacy compare requests only.

The number of daily calls charged equals the number of successful models, not the number requested. `users.total_tokens_used` equals the sum of successful model tokens, and one usage row is written for each successful model. A partially successful compare has overall durable `completed` status because it has at least one completed model; the receipt retains ordered failed-model codes for generation status hydration. An all-model failure writes nothing through BE03 and is finalized as Redis `failed` by a later orchestration tranche.

## 9. Quota, token, and usage rules

Quota admission before upstream is a read-only preflight in later stream orchestration. Consumption occurs only inside the BE03 success transaction. The locked user row is the authoritative quota source, so concurrent generations cannot overrun the free-plan limit.

If quota remains insufficient at finalization time because another generation committed first, the entire transaction rolls back. The generation becomes a stable failed outcome in Redis, no message or receipt is retained, and no daily call or token counter is charged. This may incur upstream provider cost; that cost belongs to the separately deferred audit boundary and cannot be disguised as a successful user quota charge.

BE03 does not choose a tokenizer or parse provider-specific usage. It accepts the sanitized nonnegative per-model token values supplied by the future orchestration layer and guarantees they are persisted consistently in:

- each successful assistant message;
- the corresponding usage record;
- each completed receipt result;
- the parent receipt's `total_tokens` sum; and
- the increment to `users.total_tokens_used`.

BE05/BE06 must preserve the existing platform token contract when producing these values. Raw provider bodies, arbitrary provider fields, costs, prices, or credentials are not valid BE03 inputs.

## 10. Idempotency and duplicate finalization

`(user_id, generation_id)` is the sole durable finalization key. A generation ID belonging to another user is unrelated and cannot be queried or returned.

Concurrent or repeated finalizers may execute, but only one transaction can commit the unique receipt. If a candidate transaction loses the unique constraint race, all of its candidate writes roll back. Outside that failed transaction, the service loads and validates the winner's receipt and returns the same authoritative committed result without adding messages, usage records, quota, or token totals.

A duplicate input with different user-message content, mode, model order, conversation, terminal statuses, assistant content, token values, or stable codes does not return the existing success as though it matched. It returns a typed idempotency conflict after comparing the immutable receipt identity and hydrated internal snapshot. User and assistant message content is compared only by loading owned referenced message rows inside the service; user-message content is retained only in the internal snapshot used for exact duplicate comparison and is never copied into the receipt, public DTO, Redis, log, or integrity error.

Receipt reads require:

- `receipt.user_id` equal to the authenticated internal user ID;
- parent and children with `is_deleted = 0`;
- `receipt.user_message_id` resolving to exactly one active message in the same conversation with `role = user`; the globally unique key prevents that message from backing another receipt;
- parent `mode` resolving only to the permanent single/compare values, `successful_model_count >= 1`, `daily_calls_charged = successful_model_count`, `total_tokens >= 0`, and `committed_at` being a positive safe Unix-millisecond integer;
- exact child count and contiguous model indexes for the stored mode;
- successful model count and token sum equal to the parent aggregates;
- completed child message references resolving to active assistant messages in the same conversation with the same model and token count;
- failed children containing only allowlisted stable codes; and
- no extra or shared assistant-message reference.

Any violation is a stable internal integrity failure, not a partially accepted result.

## 11. Redis/MySQL crash windows and recovery anchor

The parent receipt and validated child rows are the only durable proof that MySQL committed. Recovery never infers commit from an assistant message's text, timestamps, conversation ordering, or a client value.

| Crash or dependency window | Durable facts | Required outcome |
| --- | --- | --- |
| Before Redis `BeginCommit` | Redis is `running` or a cancellation state; no receipt. | No BE03 write. Later control/recovery logic chooses cancellation or stable failure. |
| After `BeginCommit`, before SQL commit | Redis is `committing`; no receipt. | After the 30-second convergence threshold, mark the generation failed through a dedicated `committing -> failed` CAS. |
| SQL transaction rolls back | Redis is `committing`; no receipt and no partial SQL effect. | Mark failed; never send done and never charge quota. |
| SQL commit succeeds, before Redis `Complete` | Redis is `committing`; an active receipt references the exchange's active user message and ordered results. | Load and validate the complete receipt graph, then complete Redis with the exact per-model assistant-message GUID map. |
| Redis `Complete` succeeds, before SSE done | Redis and MySQL both prove completed. | Generation GET returns completed data; a stream reconnect is not attempted. |
| Redis update temporarily fails after SQL commit | MySQL receipt proves success; Redis may remain `committing` or be unavailable. | Do not send done while Redis completion is unconfirmed. Retry only the idempotent Redis completion transition; recovery later derives completed from the receipt. |
| Redis key expires or is lost while receipt remains | MySQL proves a historical committed exchange. | BE04 may reconstruct the completed status for the authenticated owner during the supported status window; it must never restart upstream. |
| SQL commit result is unknown | Receipt presence is checked on a fresh connection. | Valid receipt means success; no receipt means unproven and converges to failed, never an automatic mutation replay. |

BE03 may extend the BE02 store with narrowly typed reconciliation transitions:

- `committing -> completed` from a validated receipt;
- `committing -> failed` only after no receipt is found beyond the convergence threshold;
- idempotent acceptance of an already identical `completed` snapshot.

Those transitions retain BE02 ownership, CAS, TTL, identity, strict decoding, and sensitive-data exclusions. They do not scan or run automatically in BE03. Scheduling and HTTP exposure belong to BE04.

When a reconciliation mutation loses a CAS race, the store returns the authoritative nonzero snapshot alongside the typed error. Reconciliation returns that resolved snapshot rather than the stale pre-lock read. If the mutation succeeds but advisory-lock release or acknowledgement later fails, reconciliation likewise returns the resolved terminal snapshot alongside the release error. Errors that occur before any mutation can resolve an authoritative snapshot, including receipt-reader, advisory-lock acquisition, and database failures, return the original pre-lock Redis snapshot alongside the error.

## 12. Failure and cancellation behavior

BE03 only persists completed exchanges. The future orchestrator must never invoke the finalizer for `running`, `cancelling`, `cancelled`, all-model `failed`, or missing/empty final-user-message input. The finalizer rejects invalid input before reading Redis or opening MySQL.

Validation, ownership, quota, conversation, message, usage, receipt, or transaction failure produces no committed SQL subset. If Redis is still `running`, the orchestrator may use the existing stable fail transition. If Redis is `committing`, it must use the dedicated receipt-aware reconciliation path rather than forcing a state change that could overwrite a committed success.

Cancellation that wins while Redis is `running` prevents `BeginCommit`, so no BE03 write is allowed. Cancellation that arrives after `committing` cannot overwrite the commit winner. It returns `committing` until the receipt check resolves, then returns either `completed` or `failed` as the authoritative terminal state.

Cancelled and failed partial content remains process memory only and is discarded. It is not stored in Redis, MySQL receipts, messages, local files, logs, error envelopes, or test artifacts.

## 13. Security and privacy

- Every operation is scoped by authenticated `users.id`; username, nickname, phone, user GUID, or a client-provided owner is never used for ownership.
- The finalizer compares its immutable identity to the owner-scoped Redis claim before any SQL mutation.
- Generation UUIDs are canonical lowercase values and are never accepted as database primary keys or server business GUIDs.
- Receipt tables contain only lifecycle identity, model identifiers, stable codes, counts, timestamps, and internal references, including `user_message_id`. They contain no prompt or user-message body, reply, authorization header, token secret, upstream error body, internal URL, credential, price, or cost.
- The receipt reader may retain the referenced user-message body only in the service-internal persistence snapshot for exact idempotency comparison. That field must be marked `json:"-"` and must not be exposed through BE04 public status output.
- Assistant content exists only in the existing `messages` table and is returned later only after receipt, conversation, message, and user ownership are all verified.
- Failed model errors use the existing stable allowlist. Raw Go errors and upstream bodies never enter result rows or public DTOs.
- SQL errors are mapped to stable internal categories. Constraint names, SQL text, IDs, and database addresses are not returned to clients. The transaction uses a silent GORM session so interpolated slow/error SQL logging cannot record user or assistant content; persistence code does not log either body itself.
- Logs and diagnostics may record hashed request correlation, stage, mode, model count, success/failure count, and timing. They must not record generation content or credentials.
- All ordinary reads include `is_deleted = 0`; no BE03 path performs physical deletion.
- Foreign keys use internal signed IDs and `RESTRICT`, preventing receipt evidence from being silently cascaded away.

## 14. Testing and delivery gates

Implementation follows test-driven development. A local delivery is not accepted without all applicable gates below.

### 14.1 Schema and migration tests

- `All()` returns eleven ordered migrations ending in `0011`; published `0001`-`0010` bytes and checksums remain unchanged.
- Both tables contain internal `id`, unique snowflake `guid`, audit fields, logical deletion, stable integer enums, expected signed foreign keys, and all specified checks/indexes.
- Canonical generation IDs compare byte-for-byte under ASCII binary collation.
- Duplicate `(user_id, generation_id)`, duplicate model/index, shared assistant message, broken status/result invariant, negative token/count, and invalid enum inserts are rejected.
- Duplicate `user_message_id`, missing referenced user message, and deletion/update of a referenced user message are rejected by the global unique key and `RESTRICT` foreign key.
- Re-running through the migration ledger is a no-op; isolated down removes child before parent.
- MySQL committed-prefix/partial-schema tests fail closed and prove rerun behavior appropriate to the explicit two-table DDL sequence.

### 14.2 Transaction integration tests

Use an explicitly configured disposable MySQL fixture. Tests must prove:

- single success commits one exchange with exactly one non-empty user message, one usage row, one quota charge, exact token totals, and one receipt/result whose `user_message_id` references that message;
- compare success creates independent assistant messages and ordered result references without `__MULTI_MODEL__`;
- compare partial success persists only successful messages/usage/quota while preserving failed stable codes;
- all-model failure and cancellation persist nothing and charge nothing;
- injected failure at every write boundary rolls back all conversation, title, message, usage, user, receipt, and result effects;
- the existing-conversation title/audit update is a separately injected boundary, and rollback leaves its prior title and audit fields unchanged;
- whitespace-only user content is accepted and stored byte-for-byte while a new or already-current conversation retains the fallback title without relying on `clientFoundRows` no-op affected-row behavior;
- Redis final sequence mismatch and a transaction timestamp older than Redis, locked user/reset, or locked conversation state conflict before SQL effects; equality is accepted;
- an interpolating slow/error GORM logger never receives unique prompt or assistant-content sentinels from transaction writes;
- insufficient quota under a locked concurrent race yields at most the permitted committed successes and never overcharges;
- concurrent same-generation finalization has one committed winner and identical authoritative duplicate reads;
- same generation with different user-message content or any other conflicting immutable input returns a typed conflict without new writes, even when retry timestamps differ;
- a commit-unknown simulation resolves only through a fresh receipt read under a bounded context independent of caller cancellation, and preserves an integrity result explicitly;
- cross-user receipt/message references cannot be read or reused;
- missing/deleted/wrong-role/cross-conversation user-message references and malformed, deleted, mismatched, incomplete, or duplicate receipt graphs fail closed.

### 14.3 Redis/MySQL reconciliation tests

Use explicit disposable `TEST_REDIS_URL` and MySQL fixture variables. Tests must prove:

- `committing` plus valid receipt converges to identical `completed` message GUIDs;
- `committing` without receipt cannot fail before the 30-second threshold and converges to failed after it;
- a receipt appearing before the fail CAS wins as completed;
- cancel racing with reconciliation cannot overwrite `committing/completed`;
- repeated identical completion is idempotent, while a different GUID map conflicts;
- Redis never receives content, prompt, quota, cost, user display data, or raw errors;
- Redis unavailability leaves the MySQL receipt intact and does not cause SQL replay.

Missing explicit fixture variables are reported as `SKIP` or `BLOCKED_FIXTURE`; they are never represented as a real integration pass. Fixtures must be loopback-only, isolated, and cleaned up by exact identity.

### 14.4 Regression and review gates

Required final commands include focused race tests, the complete Go suite, `go vet ./...`, migration checksum/structure checks, and `git diff --check`. Exact commands belong in the BE03 implementation plan after test names are fixed.

Independent specification, implementation-quality, and security reviews must verify transaction atomicity, migration conformance, idempotency, ownership, receipt integrity, crash-window ordering, sensitive-data exclusion, and legacy isolation. Passing unit tests without real isolated MySQL/Redis reconciliation evidence is insufficient.

## 15. BE04 handoff contract

BE04 receives the following completed primitives from BE03:

1. Load a validated committed receipt by authenticated `user_id + generation_id`.
2. Hydrate completed single/compare result metadata and content from owned active message references, preserving model request order; retain the user-message body only in the internal snapshot used for duplicate comparison and never expose it through public status DTOs.
3. Reconcile stale `committing` to `completed` from a receipt or to `failed` after the 30-second threshold when no receipt exists.
4. Return typed not-found, unavailable, integrity, idempotency-conflict, quota, and persistence errors without raw dependency details.
5. Keep v2 persistence isolated from all legacy chat methods.

BE04 will separately design and implement authenticated generation GET/cancel handlers, cancel-before-stream tombstones, `200/202` plus `Retry-After`, bounded waiting, and startup/periodic stale-state convergence. BE04 must not start an upstream call, reactivate an expired generation, or return unpersisted content.

BE05 will later activate single v2 streaming against these primitives. BE06 will add compare v2 streaming. Frontend integration, proxy behavior, public HTTPS, real-model evidence, deployment, and production migration remain later separately authorized work.

## 16. Completion boundary

BE03 is complete only when migration `0011`, the typed persistence/repository layer, transaction finalizer, receipt validation, and reconciliation primitives pass their isolated MySQL/Redis tests and full regressions with independent reviews.

Completion of BE03 does not mean any v2 HTTP route is available, a migration was run in production, streaming was accepted end to end, or `go-018` can be marked complete. The feature remains in progress until BE04-BE06, frontend integration, proxy checks, and the PRD's controlled and real-model acceptance evidence are separately delivered.
