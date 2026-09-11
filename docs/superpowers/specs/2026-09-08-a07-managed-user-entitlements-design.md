# A07 managed-user credentials and entitlements design

Status: `AGREED_FOR_IMPLEMENTATION` (contract revision `2026-09-08-a07-v2`)

A07 contains exactly three administrator actions: reset a managed user's password, change the user's active business group, and grant or adjust the user's plan. It excludes role and permission changes (A08), amount/quota Mock work (A09–A11), model ACL editing, session-management UI, Key CRUD, payment/order writes, deployment, production migration execution, and production acceptance. It includes only the forward migration definition and local verification needed for its stable reset result.

## Frozen endpoints

- Password reset uses the existing verified-action lifecycle: `POST /admin/v2/action-verifications`, `POST /admin/v2/users/{guid}/actions`, and `GET /admin/v2/operations?scope=users.reset_password`.
- Group change uses `PATCH /admin/v2/users/{guid}/group`.
- Plan change uses `PATCH /admin/v2/users/{guid}/plan`.

Every body is one strict JSON object with exact keys, a 4096-byte limit, canonical path/GUID values, and no duplicate, case-folded duplicate, unknown, invalid UTF-8, or trailing content. Every matched response carries `Cache-Control: no-store` and a nonempty `X-Request-ID`.

## Password reset

The external action name and capability are both `users.reset_password`. The existing internal Action value 2 has never been active and no valid operation can reference it. Before its first activation, its canonical intent is explicitly upgraded to bind `target_guid`, `expected_auth_version`, the exact new-password bytes, and the normalized reason. This is the only pre-activation encoding change; the integer is not renumbered.

Issue binds the actor's current password, current logical session, action, target, target version, new password, and reason into a single five-minute ticket. Execute requires exactly one original idempotency key and ticket. Neither POST is automatically replayed. A commit-unknown response exposes only its operation reference; recovery uses the original key and exact query scope. Password, current password, ticket, key, hashes, digests, and password-derived metadata never enter responses, ordinary logs, audit detail, outbox payloads, browser storage, URLs, or analytics.

The global order is Redis limiter, actor user, actor session, operation, verification, target, target sessions, Gateway Keys only when a consumer writes them, policy head/rules, authentication audit, management audit, outbox, and terminal operation result. The consumer uses the supplied transaction and creates no nested transaction, HTTP call, or arbitrary network call. The sole network exception is the injected session revoker: after target sessions are locked and before any consumer or business SQL mutation, it may call Redis `MarkSessionRevoked` to establish denial barriers. Any such failure returns 503; the surrounding transaction rolls back its verification update and every other SQL fact, leaving zero committed SQL facts. A later SQL rollback may leave only a safe extra denial marker.

Password buffers have explicit ownership. Issue owns independent current-password and new-password buffers and clears both on every exit. Execute decodes independent HMAC and execution buffers. Canonical encoding clears only the HMAC buffer. The execution buffer is transferred for hashing only after `ExecutionReady`; every construction failure, execute return, and panic-safe cleanup clears it. An already-terminal operation neither allocates nor hashes an execution password.

The transaction validates the ticket and target version, creates the password hash only for an execution-ready operation, establishes target-session Redis barriers before consumer or business SQL mutation, durably revokes all target sessions, changes the password hash, advances `auth_version` exactly once, and writes N session-revoked events, one password-changed event, one management audit, one outbox row, and the terminal operation atomically. Redis failure returns 503 and rolls back the transaction, including its verification update, leaving zero committed SQL facts. SQL failure rolls back every SQL fact; an already-written Redis denial marker may remain as a safe extra denial, so the design does not describe Redis and MySQL as one atomic commit.

The stable success response is exactly `{"operation_ref":"op_...","target_guid":"...","resulting_auth_version":8}`. A forward `0011_admin_operation_result_auth_version` migration adds nullable `result_auth_version INT` to `admin_operations`. A succeeded reset stores `ResultUser`, target GUID, resulting positive INT32 auth version, and HTTP 200; exact idempotent replay returns those stored values and never projects the current user row. Expiry clears the optional result version with the other result fields while retaining the permanent key tombstone. After trusted 200, the frontend performs one owned target GET and applies it only if route, identity epoch, permission revision, target GUID, returned version, and dialog owner still match.

Active and disabled nondeleted targets are eligible. For an active target the old password fails and the new password can log in. A disabled target remains unable to log in with either password; after a later A06 enable, only the new password works. Old Access and Refresh sessions fail in both cases. The reset does not delete or revoke Gateway Keys; active Keys continue only under current owner and Key state. The target is never automatically logged in.

## Group and plan changes

Group and plan changes are direct, non-replayed PATCH actions with optimistic `expected_auth_version`. They do not use current-password verification, tickets, idempotency keys, or operation rows. Their lock order is actor user, actor session, target user, destination business group for group changes, target sessions, then policy head/rules. They establish Redis denial barriers before one atomic SQL transition. Admin may manage only User; Root may manage User or Admin; self, Root, deleted, same-level, hidden, stale, disabled actor, revoked actor session, and denied capability fail closed. Active and disabled targets are eligible.

Group input is an active, nondeleted business-group GUID. Persistence writes only `users.group_id -> business_groups.id`; API and audit never expose the internal ID. Group membership is an organizational label and does not implicitly change plan, model ACL, permissions, amount, or quota.

Plan input is exactly `free`, `professional`, or `enterprise`. It is an administrator grant/adjustment and creates no order or payment fact. Each actual plan transition sets the stored `daily_call_limit` to the canonical free-policy value 100 and preserves `daily_calls_used`; professional and enterprise remain unlimited under the current runtime rule, while downgrade to free immediately enforces the 100-call limit. Clients cannot submit a daily limit.

Same group or plan is a state conflict. Every actual change revokes old sessions, advances `auth_version` by one, updates one entitlement, writes N session-revoked events plus one managed-user-updated event and one management audit with normalized reason and allowlisted before/after values, and returns the exact `UserReadDTO`. Redis denial barriers precede consumer or business SQL mutation; a Redis failure rolls back the transaction and leaves zero committed SQL facts, while a later SQL rollback may leave only a safe extra Redis denial.

## Compatibility and recovery

Legacy `PUT /admin/users/{guid}` rejects every A07/security field before any write, including status, plan, group, model ACL, daily limit, passwords, reason, and expected version. A05 nickname, A06 status, and A14 delete contracts remain unchanged.

For direct PATCH 409, the frontend performs at most one owned detail GET and never replays the mutation. For a response without a trusted terminal result, it refreshes the target without claiming success and requires a new user gesture. Password reset query recovery never repeats Issue or Execute. It inherits A14 lifecycle, visibility, expiry, and header rules, but its scope-specific response always has seven exact fields by adding nullable `target_guid` and `resulting_auth_version`. Processing alone has `Retry-After: 1..30` and null result fields; succeeded has positive `finished_at` and the stored target/version result; failed alone has an allowlisted failure code and null result fields; pending recovery stops polling; hidden combinations return 404 and expired results return 410. A succeeded Query therefore supplies the same stable result required for the owned GET. Route, target, identity epoch, permission revision, auth version, and dialog owner bind every late result.

Password action failures use the exact A14 `admin_action_error` envelope. Group and plan use an exact `admin_user_entitlement_error` envelope. Both contain a fixed message and body/header-equal request ID; `operation_ref` is allowed only for commit unknown, and `Retry-After` only for action rate limiting or processing Query. Existing authentication middleware 401 remains unchanged.

## Gateway Key limited subscope

A07 does not revoke or reactivate Gateway Keys. Existing owner-status, owner-model-ACL, and Key status/expiry/ACL checks remain in force. Plan and daily-limit runtime acceptance in A07 covers existing Bearer Access platform calls only. Atomic owner plan/quota reload and consumption for Gateway Key calls is not implemented by this slice and remains an explicit PRD blocker; A07 cannot be reported as complete API-Key entitlement consistency.

## Acceptance boundary

Local acceptance requires real MySQL 8 and Redis 7 coverage for old-session failure, active/disabled reset login semantics, stable idempotent replay, plan runtime enforcement on Bearer Access calls, group persistence/projection, active and separately revoked Key behavior, concurrency, rollback at every durable write, secret ownership/scan, the 0011 up/down/schema verifier, exact contracts and envelopes, frontend mounted dialogs, visible browser behavior, full regressions, race, build, vet, and diff checks. Production migration execution, production and real business accounts remain `NOT_RUN`; Gateway Key plan/quota consumption remains `BLOCKED_NOT_IMPLEMENTED`.
