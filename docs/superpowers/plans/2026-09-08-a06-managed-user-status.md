# A06 Managed User Status Implementation Plan

**Goal:** Close acceptance row A06 with a locally verified, fail-closed disable/enable workflow across backend and frontend.

**Architecture:** Add a dedicated strict v2 status endpoint and service beside A05, retire status writes through the legacy PUT, then add an isolated frontend adapter/state/store/dialog integrated into user detail. Reuse the current read DTO, authz evaluator, session locks, Redis revocation barrier, and Gateway owner-state check.

## Task 1: Freeze the shared contract

- Add `docs/agents/contracts/admin-user-status-v1.json` in backend and a byte-equivalent frontend fixture.
- Add backend and frontend contract tests for exact request, response, headers, errors, transition capabilities, credential semantics, and legacy status retirement.
- Run the focused contract tests and record the expected RED before implementation.

## Task 2: Strict decoder and legacy retirement

- Add an owned A06 request decoder in `internal/dto` with 4096-byte limit, exact/case-folded unique keys, UTF-8 and surrogate validation, and transition-specific reason validation.
- Add tests for malformed, duplicate, unknown, missing, oversized, invalid state/version/reason, and trailing bodies.
- Reject any legacy PUT body containing `status` before invoking the legacy service; test status-only and mixed bodies make zero writes.

## Task 3: Atomic status service

- Add `AdminUserStatusService` in `internal/service` with fresh actor/session/policy/target locks and transition-specific authz.
- RED/GREEN tests cover hierarchy, hidden targets, self/Root/equal denial, actor/session drift, stale version, state conflict, version overflow, Redis unavailable/revoked, rollback, one version increment, audit privacy, and concurrency.
- Disable reuses the session Redis barrier and durable revocation logic. Both transitions write authentication and official management audit events transactionally.

## Task 4: Handler and route

- Register exactly one authenticated `PATCH /admin/v2/users/:guid/status` route with request ID and no-store headers.
- Map only frozen safe errors and return exact `UserReadDTO`.
- Test path/query/content type, one invocation, headers, safe envelopes, and router registration.

## Task 5: Frontend adapter and workflow

- Add the exact A06 API adapter, response validation, safe error mapping, and production authenticated transport with no PATCH replay.
- Add a closed workflow state machine and Pinia coordinator with immutable ownership, duplicate suppression, secret/reason clearing, and conflict refresh hooks.
- RED/GREEN tests cover validation, exact wire shape, late response isolation, reset/dispose, and storage/log absence.

## Task 6: Controlled confirmation UI

- Add `UserStatusDialog.vue` and integrate it into `UserDetail.vue` using exact capability/hierarchy/status predicates.
- Require reason plus checkbox for disable; require explicit confirmation for enable; preserve focus and accessible announcements.
- Test conditional visibility, Enter/click paths, busy state, 401/403/404/409/503 handling, one GET/no replay on conflict, identity/route/permission/dialog races, and responsive layout.

## Task 7: Local joint acceptance

- Run all backend focused/full/race/build/vet/diff gates with a private Go cache.
- Run frontend focused/full/build/diff gates with `VITE_USE_MOCK=false` for production build.
- Use isolated MySQL 8 and Redis 7 fixtures to validate migrations, disable/enable state, old Access/Refresh denial, Gateway owner-state behavior, independently revoked/expired Key behavior, audit content, dependency failures, and cleanup.
- Run visible Chromium acceptance and retain raw machine-readable evidence.

## Task 8: Review and trackers

- Obtain independent specification, quality, security, and evidence reviews of the exact candidate revisions.
- Update A06 only in the 26-row acceptance matrix and update backend/frontend trackers without changing other rows.
- Record local limited-scope boundaries and backend project-manager written-confirmation status. Commit clean backend and frontend branches; do not push, merge, deploy, or use production accounts without a separate instruction.
