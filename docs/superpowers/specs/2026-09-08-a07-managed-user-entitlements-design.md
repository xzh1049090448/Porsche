# A07 managed-user credentials and entitlements design

Status: `AGREED_FOR_IMPLEMENTATION`

A07 contains exactly three administrator actions: reset a managed user's password, change the user's active business group, and grant or adjust the user's plan. It excludes role and permission changes (A08), amount/quota Mock work (A09–A11), model ACL editing, session-management UI, Key CRUD, payment/order writes, deployment, migration, and production acceptance.

## Frozen endpoints

- Password reset uses the existing verified-action lifecycle: `POST /admin/v2/action-verifications`, `POST /admin/v2/users/{guid}/actions`, and `GET /admin/v2/operations?scope=users.reset_password`.
- Group change uses `PATCH /admin/v2/users/{guid}/group`.
- Plan change uses `PATCH /admin/v2/users/{guid}/plan`.

Every body is one strict JSON object with exact keys, a 4096-byte limit, canonical path/GUID values, and no duplicate, case-folded duplicate, unknown, invalid UTF-8, or trailing content. Every matched response carries `Cache-Control: no-store` and a nonempty `X-Request-ID`.

## Password reset

The external action name and capability are both `users.reset_password`. The existing internal Action value 2 has never been active and no valid operation can reference it. Before its first activation, its canonical intent is explicitly upgraded to bind `target_guid`, `expected_auth_version`, the exact new-password bytes, and the normalized reason. This is the only pre-activation encoding change; the integer is not renumbered.

Issue binds the actor's current password, current logical session, action, target, target version, new password, and reason into a single five-minute ticket. Execute requires exactly one original idempotency key and ticket. Neither POST is automatically replayed. A commit-unknown response exposes only its operation reference; recovery uses the original key and exact query scope. Password, current password, ticket, key, hashes, digests, and password-derived metadata never enter responses, ordinary logs, audit detail, outbox payloads, browser storage, URLs, or analytics.

The transaction re-locks actor, actor session, target, target sessions, and current policy; verifies the Redis actor-session barrier; validates the ticket and target version; creates the password hash only for an execution-ready operation; establishes target-session Redis barriers; durably revokes all target sessions; changes the password hash; advances `auth_version` exactly once; and writes authentication audit, management audit, operation result, and outbox terminal state atomically. Old password/login, Access, and Refresh fail. The reset does not delete or revoke Gateway Keys; active Keys continue only under current owner and Key state. The target is never automatically logged in.

## Group and plan changes

Group and plan changes are direct, non-replayed PATCH actions with optimistic `expected_auth_version`. They do not use current-password verification, tickets, idempotency keys, or operation rows. Both re-lock the actor, actor session, target, target sessions, and policy and establish Redis denial barriers before one atomic SQL transition. Admin may manage only User; Root may manage User or Admin; self, Root, deleted, same-level, hidden, stale, disabled actor, revoked actor session, and denied capability fail closed. Active and disabled targets are eligible.

Group input is an active, nondeleted business-group GUID. Persistence writes only `users.group_id -> business_groups.id`; API and audit never expose the internal ID. Group membership is an organizational label and does not implicitly change plan, model ACL, permissions, amount, or quota.

Plan input is exactly `free`, `professional`, or `enterprise`. It is an administrator grant/adjustment and creates no order or payment fact. Each actual plan transition sets the stored `daily_call_limit` to the canonical free-policy value 100 and preserves `daily_calls_used`; professional and enterprise remain unlimited under the current runtime rule, while downgrade to free immediately enforces the 100-call limit. Clients cannot submit a daily limit.

Same group or plan is a state conflict. Every actual change revokes old sessions, advances `auth_version` by one, updates one entitlement, writes one credential-free authentication event plus one management audit with normalized reason and allowlisted before/after values, and returns the exact `UserReadDTO`.

## Compatibility and recovery

Legacy `PUT /admin/users/{guid}` rejects `status`, `plan_type`, `allowed_models`, and `daily_call_limit` before every write. It has no group or password-reset field. A05 nickname, A06 status, and A14 delete contracts remain unchanged.

For direct PATCH 409, the frontend performs at most one owned detail GET and never replays the mutation. For a response without a trusted terminal result, it refreshes the target and requires a new user gesture. Password reset query recovery never repeats Issue or Execute. Route, target, identity epoch, permission revision, auth version, and dialog owner bind every late result.

## Acceptance boundary

Local acceptance requires real MySQL 8 and Redis 7 coverage for old-session failure, new-password login, plan runtime enforcement, group persistence/projection, active and separately revoked Key behavior, concurrency, rollback at every durable write, secret scanning, exact contracts, frontend mounted dialogs, visible browser behavior, full regressions, race, build, vet, and diff checks. Production and real business accounts remain `NOT_RUN`.
