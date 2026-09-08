# A08 managed-user role and permission writes design

Status: `AGREED_FOR_IMPLEMENTATION`

Chosen approach: mixed endpoint contract (user selection `1`, 2026-09-09)

Contract revision: `2026-09-09-a08-v1`

## Scope and acceptance boundary

A08 adds exactly three Root-only managed-user mutations: promote User to Admin, demote Admin to User, and replace an Admin's permission overrides. It also adds the corresponding frontend controls and recovery behavior. It builds on the accepted A03, A05, A06, A07, A14, B1-C and B1-E foundations.

A08 excludes arbitrary role creation, Root creation/promotion/demotion/deletion, self-role changes, assignment of Root-only capabilities, amount/quota Mock work, public-content permissions, model ACL editing, session-management UI, deployment, production migration execution and production acceptance. Gateway Key owner-role and permission invalidation must nevertheless be covered by the A08 runtime acceptance because the PRD requires old credentials to stop using superseded authorization.

This design does not mark A08, `web-012`, the 26-case joint matrix or the whole PRD complete. External backend `project_manager` confirmation remains pending until the frozen contract and final implementation evidence are reviewed in writing.

## Chosen architecture

Role changes use the existing verified-action lifecycle:

- `POST /admin/v2/action-verifications`
- `POST /admin/v2/users/{guid}/actions`
- `GET /admin/v2/operations?scope=users.promote`
- `GET /admin/v2/operations?scope=users.demote`

Permission replacement keeps the PRD's resource endpoint while adopting the same security guarantees:

- `GET /admin/v2/users/{guid}/permissions`
- `POST /admin/v2/action-verifications`
- `PATCH /admin/v2/users/{guid}/permissions`
- `GET /admin/v2/operations?scope=users.permissions.write`

The permission PATCH is a sensitive, verified, idempotent mutation. It requires exactly one original `Idempotency-Key` and one `X-Action-Ticket`. It returns only a committed terminal result or a commit-unknown response with an operation reference. Neither mutation POST/PATCH is automatically replayed. Query recovery uses the original key and exact scope.

This mixed contract preserves the explicit permissions resource in the PRD and reuses the operation safety foundation. Moving permissions to `/actions`, or allowing an unverified direct PATCH, is outside this revision.

## Authorization and eligibility

All three mutations require a fresh actor snapshot with effective Root capability. `users.promote`, `users.demote` and `users.permissions.write` remain unavailable to Admin overrides. Frontend visibility is only a convenience; the backend performs the authoritative check after locking current actor, actor session and target state.

- Promote accepts only a nondeleted User target in active or disabled state.
- Demote accepts only a nondeleted Admin target in active or disabled state.
- Permission replacement accepts only a nondeleted Admin target in active or disabled state.
- Root targets, self targets, stale targets, invisible targets, deleted targets, disabled actors, revoked actor sessions and denied capabilities fail closed.
- An invisible target returns 404. A visible target without the action capability returns 403. Stale role, auth version, policy version or catalog version returns 409.
- Repeating the current role or replacing a policy with the same canonical policy is a 409 state conflict and performs no security-version, session, policy, audit or operation-result mutation.

Every execution rechecks current actor and target state. A ticket issued from an earlier dialog snapshot cannot authorize a changed target.

## Frozen request intents

All request bodies are one strict JSON object with exact keys, a 4096-byte limit, canonical decimal string GUIDs, valid UTF-8 and no unknown, duplicate, case-folded duplicate or trailing fields. Reasons are Unicode-trimmed 1–200 code points.

### Promote

Issue uses action `users.promote`. Execute uses action `promote` at `/admin/v2/users/{guid}/actions`. The canonical intent binds:

```json
{
  "target_guid": "123456789012345678",
  "expected_auth_version": 7,
  "expected_permissions_version": 0,
  "catalog_version": 1,
  "overrides": [],
  "reason": "grant administrator duties"
}
```

`expected_permissions_version` may be zero only when the target has no policy head. `overrides` is the complete requested policy. An empty list means Admin baseline only. Promote creates or advances the policy head, changes the role to Admin and never inherits an unrelated historical policy silently.

The existing pre-activation promote descriptor may change its canonical encoding because no production operation can yet reference it. It becomes a dedicated promote intent that binds both the role transition and the full policy request. The numeric action value is not renumbered.

### Demote

Issue uses action `users.demote`. Execute uses action `demote`. The intent binds `target_guid`, `expected_auth_version`, `expected_permissions_version`, `catalog_version` and `reason`. Demote accepts no override list, preventing a request from retaining or installing Admin policy during the transition.

### Permission replacement

Issue uses action `users.permissions.write`. Execute PATCH has:

```json
{
  "expected_auth_version": 7,
  "expected_permissions_version": 3,
  "catalog_version": 1,
  "overrides": [
    {"capability": "users.sessions.read", "effect": "allow"},
    {"capability": "users.delete", "effect": "deny"}
  ],
  "reason": "support rotation"
}
```

The path supplies the target GUID; Issue binds the same GUID. The canonical intent sorts by capability and binds target GUID, both expected versions, catalog version, every effect and the normalized reason. Duplicate capabilities are invalid. Unknown or unavailable capabilities are invalid. `allow` for a Root-only/ungrantable capability is forbidden. `inherit`, `allow` and `deny` are accepted UI values; canonical persistence omits `inherit` rows so absence means baseline inheritance.

The existing pre-activation `PermissionsWriteIntent` is extended with `expected_auth_version` and `reason`. This is allowed only before activation; existing action integers are preserved.

## Stable responses and recovery

Trusted promote success is:

```json
{"operation_ref":"op_...","target_guid":"123456789012345678","resulting_auth_version":8,"resulting_permissions_version":1,"resulting_role":"admin"}
```

Trusted demote success uses the same shape with `resulting_role: "user"`. Permission replacement returns the same fields with `resulting_role: "admin"`. Stable results are stored on the operation row and replayed from the stored terminal result; replay must not project the current user row.

Operation Query uses the exact requested scope and returns the standard processing, succeeded, failed or pending-recovery state plus nullable target GUID, resulting auth version, resulting permissions version and resulting role. Processing alone carries `Retry-After`. A succeeded query provides the stable result needed for one owned refresh. Expired/tombstoned results remain 410 and hidden operations remain 404.

All matched responses carry `Cache-Control: no-store` and a nonempty body/header-equal request ID. Errors use the existing `admin_action_error` envelope and fixed, allowlisted failure codes. Secrets, tickets, idempotency keys, password material and HMAC inputs never enter responses, audit detail, outbox payloads, URLs, browser storage, analytics or ordinary logs.

## Persistence and transaction behavior

Writers follow the existing database standards and use only forward migrations. A forward migration extends the operation's stable result fields with nullable `result_permissions_version BIGINT` and `result_role INT`; the API maps the stable integer role to the external string enum. The migration does not alter prior checksums. No new role table is introduced.

Policy replacement locks the policy head and all active rules. It advances `policy_version` exactly once, tombstones previous active override rows and inserts the new canonical allow/deny set. Historical rows remain available for audit but are excluded from authorization. The head stays nondeleted and records the new version, catalog version and active rule count.

Demotion also advances the policy head to a nondeleted empty policy version. It tombstones active override rows and sets rule count to zero. This is deliberate: deleting the head would make the current snapshot loader treat retained history as corrupt, while retaining an old active head could restore stale Admin permissions on a later promotion.

Promote with no previous head creates version 1. Promote with a historical empty head advances it. Promote never reactivates tombstoned rows. Permission replacement requires a positive current policy version because only Admin targets are eligible.

Each real transition changes the role or canonical policy, advances `users.auth_version` exactly once, revokes all target sessions durably, clears/rejects stale permission cache state, writes per-session revocation events, one managed-user role/policy event, one redacted management audit, one outbox row and one terminal operation result in the transaction.

The global lock order remains: Redis limiter, actor user, actor session, operation, verification, target user, target sessions, policy head, policy rules, Gateway Keys when a consumer writes them, authentication audit, management audit, outbox, terminal operation result. The injected revoker establishes Redis denial barriers after target-session locks and before consumer/business SQL mutation. Redis failure returns 503 and rolls back all SQL facts. A later SQL rollback may leave only a safe extra denial marker.

## Immediate authorization effect

After a trusted success, every new Access, Refresh and Gateway Key request must evaluate the new user role, auth version, account state and current permission policy. Old Access/Refresh sessions fail immediately rather than waiting for JWT expiry. Existing Gateway Keys are not deleted merely because of promotion, demotion or an override change, but their requests must use the owner's current role and effective capabilities and cannot retain a cached grant.

Demotion must prove that no active override row contributes to effective policy. Subsequent promotion starts from the submitted policy or the Admin baseline, not the old pre-demotion override set.

Legacy `PUT /admin/users/{guid}` and general `PATCH /admin/v2/users/{guid}` reject role, auth version, policy version, catalog version, overrides and action fields before any write. No old role or permission endpoint may bypass the verified A08 paths.

## Frontend behavior

The user detail page derives controls from the fresh capability set and target projection:

- Root viewing a User sees “提升为管理员”.
- Root viewing an Admin sees “权限设置” and “降级为普通用户”.
- No target sees self-role controls; Root targets expose none of the three controls.

The promote dialog offers Admin baseline as the default and an optional expanded permission editor. The editor groups catalog capabilities and shows `inherit`, `allow`, `deny`, final effective value and source. Unavailable and Root-only capabilities are visible as locked metadata only when useful for explanation and are never serializable as grants.

The demote confirmation states that Admin permissions stop applying and all sessions become invalid. Permission-save confirmation states that all sessions become invalid. Every action requires a normalized reason and current Root password in the verification step.

Tickets, passwords and idempotency keys are memory-only and owned by the dialog execution. Submit is disabled in flight. A timeout enters “结果待确认”; the adapter queries once with the original key and exact scope, never automatically repeats Issue or mutation. A trusted success triggers one target detail/permissions refresh guarded by route, target GUID, identity epoch, capability revision, expected versions and dialog owner. A 409 performs at most one owned refresh and requires a new user gesture.

The UI never claims success from an optimistic local role or policy update. Failed submission retains only nonsecret editor values and reason. Closing or superseding a dialog clears current password, ticket, key and unknown-state ownership.

## Verification plan boundary

Implementation must be test-driven and demonstrate:

- strict request/response and envelope contracts for all three actions;
- actor/target eligibility, Root-only enforcement and legacy-field rejection;
- exact idempotent terminal replay and commit-unknown recovery;
- role/policy optimistic conflicts and same-state no-write behavior;
- concurrent promote/demote/write serialization;
- real MySQL 8 policy history, empty-policy demotion and later re-promotion behavior;
- Redis 7 rollback/denial behavior and old Access/Refresh failure;
- Gateway Key requests using the new owner authorization immediately;
- audit/outbox allowlists and absence of secrets;
- frontend mounted dialogs, keyboard/focus behavior, narrow viewport layout, late-result ownership and browser-visible success/conflict/recovery paths;
- full backend tests/race/build/vet, frontend tests/build, diff checks and independent specification, quality and security review.

Production migration, deployment and production acceptance remain `NOT_RUN` until separately authorized. Missing real MySQL/Redis or browser evidence remains an explicit blocker and cannot be converted to a pass.

## Written decisions still requiring review

The user approved this written specification on 2026-09-09. The approval covers the precise intent fields, operation stable-result expansion, same-policy 409 behavior, `inherit` omission in persistence, policy-head preservation on demotion and Gateway Key runtime acceptance. Implementation must follow the paired TDD plan; external backend `project_manager` confirmation and final acceptance evidence remain separately pending.
