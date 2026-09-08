# A07 managed-user credentials and entitlements implementation plan

> Implement with TDD and independent review at each bounded backend/frontend milestone.

**Goal:** Deliver password reset, group change, and plan change with fresh authorization, session invalidation, exact audit, no secret leakage, and frontend recovery.

**Contract:** `docs/agents/contracts/admin-user-entitlements-v1.json`

### Task 1: Freeze and cross-copy the A07 contract

- Add identical backend/frontend contract fixtures and verify byte equality.
- Preserve A05, A06, A14 routes and all A08/A09 exclusions.

### Task 2: Implement direct group and plan changes

- Add strict DTO decoders, service, handler, routes, exact error mapping, and UserReadDTO projection.
- Reuse A06 lock/authorization/session-revocation ordering.
- Lock active group by GUID and write its internal ID only.
- Normalize plan transitions to daily limit 100 without orders or payment facts.
- Retire legacy PUT security/entitlement fields before writes.
- Cover real MySQL/Redis, rollback, capability, hidden target, concurrency, no-op, version overflow, and runtime plan behavior.

### Task 3: Activate the verified password-reset action

- Add expected auth version to the never-active Action=2 canonical intent and update golden tests.
- Extend verification validation, execution factory, consumer, audit/outbox writers, operation bundle, and route dispatch atomically before activating the registry entry.
- Hash and clear owned password bytes only after execution readiness.
- Cover ticket/key/action/target/version/password/reason binding, replay, unknown commit query, concurrency, rollback, old/new login, sessions, Keys, and secret scans.

### Task 4: Implement frontend adapters and owned workflows

- Add strict group, plan, and reset-password adapters.
- Add direct-PATCH state/coordinators for group and plan and verified-operation workflow for reset.
- Keep every secret and action key in memory and clear on all settlements, close, ownership drift, and unmount.

### Task 5: Add accessible dialogs and UserDetail wiring

- Add independent reset-password, group-change, and plan-change dialogs.
- Bind exact capability/hierarchy/route predicates and make dialogs mutually exclusive.
- Implement focus trap, native submit, error focus, trigger restoration, aria-live, singleflight, success reconciliation, and owned one-GET conflict refresh.

### Task 6: Verify and archive A07

- Run focused and full backend tests with real MySQL/Redis, race, build, vet, contract equality, and diff checks.
- Run frontend contract/state/component/integration tests, full suite, production build, and Chrome 375/390 checks.
- Obtain independent backend and frontend reviews, archive evidence, and update only A07 in the 26-item matrix.
