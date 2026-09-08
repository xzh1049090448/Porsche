# A07 managed-user credentials and entitlements implementation plan

> Implement with TDD and independent review at each bounded backend/frontend milestone.

**Goal:** Deliver password reset, group change, and plan change with fresh authorization, session invalidation, exact audit, no secret leakage, and frontend recovery.

**Contract:** `docs/agents/contracts/admin-user-entitlements-v1.json`

### Task 1: Freeze and cross-copy the A07 contract

- Add identical backend/frontend contract fixtures and verify byte equality.
- Add executable parsers that assert all exact response, Query, envelope, ownership, and scope fields; resolve `design_source` through the authoritative backend repository path and reviewed base commit.
- Preserve A05, A06, A14 routes and all A08/A09 exclusions.

### Task 2: Implement direct group and plan changes

- Add strict DTO decoders, service, handler, routes, exact error mapping, and UserReadDTO projection.
- Reuse A06 lock/authorization/session-revocation ordering.
- Lock active group by GUID and write its internal ID only.
- Normalize plan transitions to daily limit 100 without orders or payment facts.
- Retire legacy PUT security/entitlement fields before writes.
- Cover real MySQL/Redis, rollback, capability, hidden target, concurrency, no-op, version overflow, and runtime plan behavior.
- Freeze N session-revoked events plus one managed-user-updated event and one management audit, Redis-before-SQL failure behavior, and the safe extra-denial case after SQL rollback.

### Task 3: Activate the verified password-reset action

- Add expected auth version to the never-active Action=2 canonical intent and update golden tests.
- Add forward migration `0011_admin_operation_result_auth_version` and exact up/down/schema verification. Persist successful reset target GUID and resulting auth version in the operation; replay only the stored three-field result and never project a current user row.
- Extend verification validation, execution factory, consumer, audit/outbox writers, operation bundle, and route dispatch atomically before activating the registry entry.
- Decode independent HMAC and execution password buffers. Let canonical encoding clear only its HMAC buffer; transfer/hash the execution buffer only after execution readiness and clear it on construction failure, execute return, and panic-safe cleanup. Existing terminal operations must not allocate or hash an execution password.
- Cover ticket/key/action/target/version/password/reason binding, replay, unknown commit query, concurrency, rollback, old/new login, sessions, Keys, and secret scans.
- Cover active and disabled target reset semantics, the full global lock order, N session audit rows plus one password-change row, stable replay after later user mutations, result expiry, zero nested transactions/HTTP/arbitrary network access, and the sole injected Redis session-denial exception before SQL mutation.

### Task 4: Implement frontend adapters and owned workflows

- Add strict group, plan, and reset-password adapters.
- Add direct-PATCH state/coordinators for group and plan and verified-operation workflow for reset.
- Implement the A14 lifecycle/visibility/expiry/Retry-After rules with the reset scope's exact seven-field Query response, including nullable target/version result fields. Implement exact action/entitlement error envelopes. After execute or Query success, perform one owned GET and reconcile only the stored resulting auth version; ambiguous direct PATCH refresh never claims success.
- Keep every secret and action key in memory and clear on all settlements, close, ownership drift, and unmount.

### Task 5: Add accessible dialogs and UserDetail wiring

- Add independent reset-password, group-change, and plan-change dialogs.
- Bind exact capability/hierarchy/route predicates and make dialogs mutually exclusive.
- Implement focus trap, native submit, error focus, trigger restoration, aria-live, singleflight, success reconciliation, and owned one-GET conflict refresh.

### Task 6: Verify and archive A07

- Run focused and full backend tests with real MySQL/Redis, race, build, vet, contract equality, and diff checks.
- Run frontend contract/state/component/integration tests, full suite, production build, and Chrome 375/390 checks.
- Obtain independent backend and frontend reviews, archive evidence, and update only A07 in the 26-item matrix.
- Report plan/daily enforcement only for Bearer Access platform calls. Preserve Gateway Key owner plan/quota consumption as `BLOCKED_NOT_IMPLEMENTED`; do not claim complete PRD API-Key entitlement consistency.
