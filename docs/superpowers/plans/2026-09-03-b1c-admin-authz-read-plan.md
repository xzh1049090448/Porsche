# B1-C Admin Authorization Read Plan

**Goal:** Deliver catalog and exact-Root Admin permission detail reads without
creating a permission write path.

**Architecture:** A narrowly scoped service owns a root-pool READ COMMITTED
transaction and projects stable authz metadata. Middleware passes only an
already-authenticated session version through a private key. Handlers whitelist
DTO fields and map fixed errors without exposing persistence details.

## Task 1 — Pure contract and projection RED→GREEN

Files: `internal/service/admin_permission_read.go`,
`internal/service/admin_permission_read_test.go`, `internal/authz/*` only if a
read-only projection helper is required.

- Write pure tests for 24 ordered catalog items, effect order, Admin baseline,
  unavailable/root-only false, deny/grantable/baseline precedence, disabled
  effective false, and strict positive decimal GUID parsing.
- Implement the no-DB projection and fixed errors; do not create an evaluator
  that treats a disabled target as active.

## Task 2 — Fresh actor/session and policy loader reuse

Files: `internal/service/admin_permission_read.go`,
`internal/service/permission_snapshot.go`, `internal/middleware/*`.

- Write fixture tests for root-pool rejection, actor/target/session lock order,
  stale session/AuthVersion, Redis barrier, commit failure, bad role/status and
  corrupt head/rows.
- Extract `readPermissionPolicyRows(tx,userID)` without relaxing snapshot
  validation. Service owns READ COMMITTED and returns only after commit.
- Add a private context session-version getter; do not alter global 401 rules.

## Task 3 — HTTP boundary

Files: `internal/handler/admin_authz.go`, `internal/dto/admin_permissions.go`,
`internal/router/*`, handler tests.

- Add group request ID before auth, no-store, exact route/path/query/GUID checks
  and fixed 400/403/404/503 mapping.
- Test catalog Admin/Root eligibility and permission-detail Root-only visibility,
  disabled Admin response, hidden target cases, DTO field exclusion and request
  ID presence.

## Task 4 — Real fixture verification and review

- Create a new labeled disposable MySQL8/Redis7 fixture only after explicit
  authorization; never reuse B1-B3 credentials or migration permission.
- Run focused real tests, race, serial JSON full suite, build, vet and diff.
  Record skips precisely and freeze for PM/independent review before status
  changes.

**Out of scope:** PATCH/write operations, new schema/migration, ticket/outbox,
frontend UI, production operations, and using this display API to enable legacy
writes.
