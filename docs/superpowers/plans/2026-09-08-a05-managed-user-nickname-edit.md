# A05 Managed User Nickname Edit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the A05 `PATCH /admin/v2/users/{guid}` nickname-only edit flow with strict field rejection, fresh authorization, version conflict handling, atomic audit, and a race-safe accessible frontend.

**Architecture:** The backend adds a dedicated v2 decoder, service, and handler instead of extending the legacy multi-field PUT. The frontend adds a narrow adapter and owned dialog workflow that submits exactly two fields and reconciles only the currently selected target. One frozen backend contract is compared byte-for-structure with the frontend contract before joint acceptance.

**Tech Stack:** Go 1.22, Gin, GORM/MySQL 8, Redis-backed session validation, Vue 3, Pinia, Element Plus, Node test runner, Vite.

---

## Repository roots

- Backend: `/Users/xuzhihao/code/Porsche/.worktrees/a05-managed-user-edit`
- Frontend: `/Users/xuzhihao/code/Porsche-Web/.worktrees/a05-managed-user-edit`

Do not edit deployment/configuration files owned by the concurrent emergency-fix agent. Before final verification, fetch the latest `origin/main` and integrate the emergency fix if it has merged, resolving only A05-related conflicts.

### Task 1: Freeze the cross-repository A05 contract

**Files:**
- Create: backend `docs/agents/contracts/admin-user-edit-v1.json`
- Modify: backend `docs/agents/contracts/admin-action-future-contract.json`
- Modify: backend `docs/agents/contracts/prd-260903-interface-draft.json`
- Modify: backend `interface-contract.json`
- Modify: frontend `docs/agents/contracts/prd-260903-interface-draft.json`
- Modify: frontend `interface-contract.json`
- Create: frontend `src/api/admin-user-edit-contract.test.js`

- [ ] **Step 1: Write the failing frontend equality test**

Require `A05_BACKEND_CONTRACT`, load the backend contract path, select exactly one `admin_user_patch` entry from each frontend contract document, and compare method, path, request keys/types, forbidden-field policy, response DTO keys/headers, errors, capability, and no-retry rule.

- [ ] **Step 2: Run the contract test and verify RED**

Run:

```bash
A05_BACKEND_CONTRACT=/Users/xuzhihao/code/Porsche/.worktrees/a05-managed-user-edit/docs/agents/contracts/admin-user-edit-v1.json \
node --test src/api/admin-user-edit-contract.test.js
```

Expected: FAIL because the backend contract and frozen v2 PATCH entry do not exist.

- [ ] **Step 3: Add the minimal frozen contract**

Encode the approved design exactly: body keys `nickname` and `expected_auth_version`; nullable nickname; positive INT32 expected version; strict 4 KiB JSON; exact `UserReadDTO`; errors 400/401/403/404/409/413/503; `users.edit`; no mutation retry; old PUT excluded.

- [ ] **Step 4: Run the contract test and JSON checks**

Run the focused Node test plus `python3 -m json.tool` for every modified JSON document and `git diff --check` in both repositories. Expected: PASS.

- [ ] **Step 5: Commit the contract freeze in each repository**

Backend commit: `docs: freeze A05 user nickname edit contract`  
Frontend commit: `test: bind A05 frontend contract to backend`

### Task 2: Implement the strict backend request decoder

**Files:**
- Create: backend `internal/dto/admin_user_edit.go`
- Create: backend `internal/dto/admin_user_edit_test.go`

- [ ] **Step 1: Write failing decoder tests**

Cover exact valid objects (`nickname` string and null), whitespace normalization, 1/64 Unicode code points, positive INT32 version, missing keys, duplicate/case-folded keys, unknown keys, every forbidden A05 field, invalid UTF-8, arrays/scalars, trailing JSON, over 4 KiB, empty nickname string after trimming, lone invalid number forms, zero, negative, float, exponent, and overflow.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
GOCACHE=/tmp/porsche-a05-go-cache go test ./internal/dto -run '^TestDecodeAdminUserEdit' -count=1 -v
```

Expected: compile failure because `DecodeAdminUserEdit` is absent.

- [ ] **Step 3: Implement the minimal decoder**

Use token-level JSON parsing so duplicate and case-folded keys cannot enter a struct binder. Return an owned `AdminUserEditRequest` containing `Nickname *string`, `ClearNickname bool`, and `ExpectedAuthVersion int`; never retain the raw body.

- [ ] **Step 4: Run decoder tests and verify GREEN**

Run the focused tests and `go test ./internal/dto`. Expected: PASS.

- [ ] **Step 5: Commit**

Commit: `feat: decode A05 user nickname edits`

### Task 3: Implement atomic authorized nickname editing

**Files:**
- Create: backend `internal/service/admin_user_edit.go`
- Create: backend `internal/service/admin_user_edit_test.go`
- Create: backend `internal/service/admin_user_edit_failures_test.go`

- [ ] **Step 1: Write failing service tests**

Using the isolated MySQL/Redis fixture pattern, cover Root→User, Root→Admin, Admin→User, explicit capability deny, self/equal/Root/hidden target, disabled or stale actor/session, corrupt policy, version mismatch, set/clear nickname, no-op, and unchanged role/status/plan/group/ACL/quota/auth version/session state.

- [ ] **Step 2: Add failure and concurrency RED tests**

Force user-update, audit-insert, and commit failures and assert complete rollback. Race an actor permission/auth-version change against edit and require the stale edit to fail. Assert no-op creates no audit row; changed edits create exactly one `managed_user_updated` event without copying old/new nickname into an audit payload.

- [ ] **Step 3: Run focused tests and verify RED**

```bash
GOCACHE=/tmp/porsche-a05-go-cache go test ./internal/service -run '^TestAdminUserNicknameEdit' -count=1 -v
```

Expected: compile failure because `AdminUserNicknameEditService` is absent.

- [ ] **Step 4: Implement the minimal transactional service**

Build a dedicated service with DB and AuthRedis dependencies. Revalidate actor account, permission policy, session and Redis revocation; lock actor then target; evaluate `users.edit`; apply hierarchy; compare expected auth version; update only nickname/audit fields; insert the existing managed-user-updated event in the same transaction; project the response with the target's real group.

- [ ] **Step 5: Run focused, race, and related regression tests**

```bash
GOCACHE=/tmp/porsche-a05-go-cache go test ./internal/service -run '^(TestAdminUserNicknameEdit|TestUpdateManagedUser)' -count=1
GOCACHE=/tmp/porsche-a05-go-cache go test -race ./internal/service -run '^TestAdminUserNicknameEdit' -count=1
```

Expected: PASS with no fixture skips in the A05 selection.

- [ ] **Step 6: Commit**

Commit: `feat: edit managed user nicknames atomically`

### Task 4: Publish the v2 PATCH handler

**Files:**
- Create: backend `internal/handler/admin_user_edit.go`
- Create: backend `internal/handler/admin_user_edit_test.go`
- Modify: backend `internal/router/router.go`
- Modify: backend `internal/router/router_test.go`

- [ ] **Step 1: Write failing route and HTTP tests**

Assert exactly one `PATCH /admin/v2/users/:guid` route, authentication before mutation, strict decoder status mapping, `users.edit` enforcement, hidden target 404, conflict 409 with stable code, 413, dependency 503, exact 200 DTO, `no-store`, request ID, and no invocation of the legacy PUT service.

- [ ] **Step 2: Verify RED**

```bash
GOCACHE=/tmp/porsche-a05-go-cache go test ./internal/handler ./internal/router -run '^(TestAdminUserEdit|TestAdminUserNicknameEditRoute)' -count=1 -v
```

Expected: FAIL because the route is not registered.

- [ ] **Step 3: Implement and register the handler**

Create `RegisterAdminUserEdit`, reuse the established v2 authentication/request-ID/no-store middleware, parse canonical positive decimal GUIDs, call the dedicated service once, and emit fixed safe envelopes. Do not add PATCH to the generic action ticket routes and do not change legacy PUT.

- [ ] **Step 4: Run focused and backend-wide gates**

```bash
GOCACHE=/tmp/porsche-a05-go-cache go test ./internal/handler ./internal/router -run '^(TestAdminUserEdit|TestAdminUserNicknameEditRoute)' -count=1
GOCACHE=/tmp/porsche-a05-go-cache go test ./...
go vet ./...
git diff --check
```

Expected: PASS.

- [ ] **Step 5: Commit**

Commit: `feat: expose A05 managed user edit API`

### Task 5: Implement the frontend adapter and owned state machine

**Files:**
- Create: frontend `src/api/admin-user-edit.js`
- Create: frontend `src/api/admin-user-edit.test.js`
- Create: frontend `src/api/admin-user-edit-state.js`
- Create: frontend `src/api/admin-user-edit-state.test.js`
- Create: frontend `src/stores/admin-user-edit.js`
- Create: frontend `src/stores/admin-user-edit.test.js`

- [ ] **Step 1: Write failing adapter tests**

Require canonical GUID, normalize a nullable nickname, require positive INT32 `expected_auth_version`, send exactly one PATCH with the two approved snake-case fields, validate exact response DTO/status/security headers, and map stable 400/401/403/404/409/413/503 results without exposing transport internals. Assert no automatic retry after 401, network ambiguity, or timeout.

- [ ] **Step 2: Verify adapter RED**

```bash
node --test src/api/admin-user-edit.test.js
```

Expected: module-not-found failure.

- [ ] **Step 3: Implement the adapter and verify GREEN**

Reuse `mapUserReadDto` and the mutation-safe authenticated transport pattern. Keep the adapter independent from create/delete operation state.

- [ ] **Step 4: Write failing state/store tests**

Cover closed state vocabulary, duplicate submit sharing, immutable open snapshot, null clearing, synchronous subscriber re-entry, reset/dispose, late resolution after route/identity/dialog changes, conflict requiring an explicit target refresh, and absence of persistence/logging channels.

- [ ] **Step 5: Implement state/store and run tests**

```bash
node --test src/api/admin-user-edit.test.js src/api/admin-user-edit-state.test.js src/stores/admin-user-edit.test.js
```

Expected: PASS.

- [ ] **Step 6: Commit**

Commit: `feat: add A05 user edit workflow`

### Task 6: Add the accessible edit dialog and detail-page wiring

**Files:**
- Create: frontend `src/components/admin/UserNicknameEditDialog.vue`
- Create: frontend `src/components/admin/UserNicknameEditDialog.contract.test.js`
- Create: frontend `src/components/admin/UserNicknameEditDialog.element-plus.test.js`
- Modify: frontend `src/views/UserDetail.vue`
- Create: frontend `src/views/admin-user-edit.contract.test.js`
- Modify: frontend `src/i18n/messages.js`

- [ ] **Step 1: Write failing visibility and component tests**

Cover the exact actor/target/capability predicate; controlled open/close; focus trap; Escape; initial focus; null clearing; 64-code-point validation; submit ownership; disabled duplicate submit; success announcement; 409 refresh without replay; 401 session clearing; 403/404 invalidation; 503 explicit retry; and focus restoration.

- [ ] **Step 2: Verify UI RED**

```bash
node --test src/components/admin/UserNicknameEditDialog.contract.test.js src/components/admin/UserNicknameEditDialog.element-plus.test.js src/views/admin-user-edit.contract.test.js
```

Expected: FAIL because the component and wiring are absent.

- [ ] **Step 3: Implement the minimal dialog and page integration**

Add one edit button beside nickname, one field dialog, and ownership-checked callbacks. On success update selected detail and a matching cached list row only if route GUID, identity epoch, permission revision, dialog token, and target GUID are unchanged. On conflict fetch detail once and never replay PATCH.

- [ ] **Step 4: Run focused and frontend-wide gates**

```bash
A14_BACKEND_CONTRACT=/Users/xuzhihao/code/Porsche/.worktrees/a05-managed-user-edit/docs/agents/contracts/admin-action-future-contract.json \
A03_BACKEND_CONTRACT=/Users/xuzhihao/code/Porsche/.worktrees/a05-managed-user-edit/docs/agents/contracts/admin-user-create-v1.json \
A05_BACKEND_CONTRACT=/Users/xuzhihao/code/Porsche/.worktrees/a05-managed-user-edit/docs/agents/contracts/admin-user-edit-v1.json \
npm test
npm run build
git diff --check
```

Expected: all tests pass with zero skips; production build succeeds.

- [ ] **Step 5: Commit**

Commit: `feat: add managed user nickname editor`

### Task 7: Run isolated joint acceptance and independent review

**Files:**
- Create: backend `docs/superpowers/reports/validation/2026-09-08-a05-managed-user-edit/manifest.json`
- Create: backend `docs/superpowers/reports/validation/2026-09-08-a05-managed-user-edit/README.md`
- Create: frontend `docs/agents/validation/a05-managed-user-edit-20260908/report.md`
- Create: frontend `docs/agents/validation/a05-managed-user-edit-20260908/browser-results.json`

- [ ] **Step 1: Provision isolated MySQL/Redis fixtures**

Use task-specific container names, ports, database, Redis namespace, credentials, labels, and private temp paths. Record exact identities before testing. Never use production credentials or data.

- [ ] **Step 2: Run real backend and HTTP acceptance**

Apply migrations `0001`–latest, run all A05 service/handler cases without skips, and prove user/audit database facts plus rollback cases. Record exact tested backend/frontend commits.

- [ ] **Step 3: Run visible-browser acceptance**

Exercise Root→User set/clear, Root→Admin, Admin→User, denied actor, forbidden target, 409 refresh, keyboard/focus, and direct-detail refresh at representative widths. Inspect console and network logs; assert the PATCH body has only the two approved keys.

- [ ] **Step 4: Perform adversarial and secret checks**

Mutate role/guid/auth_version/unknown/duplicate fields; race route and identity changes; scan evidence for credentials, headers, environment values, internal IDs, and user nickname text copied into audit evidence.

- [ ] **Step 5: Obtain independent spec, quality, and security verdicts**

Review final commits and canonical evidence. Any fix invalidates affected evidence and must be rerun before PASS.

- [ ] **Step 6: Clean up exact fixture resources**

Remove only recorded task containers, volumes, listeners, processes, and private paths; compare unrelated resources before/after.

### Task 8: Update trackers and prepare reviewable branches

**Files:**
- Modify: backend `feature_list.json`
- Modify: backend `progress.md`
- Modify: frontend `feature_list.json`
- Modify: frontend `progress.md`
- Modify: frontend `docs/agents/validation/joint-acceptance-20260904/acceptance-matrix.json`
- Modify: frontend `docs/agents/validation/prd-260903-confirmations.json`

- [ ] **Step 1: Write a failing status-invariant check in `/tmp`**

Assert A05 is the only acceptance row allowed to move, final counts become 12 `PASS_LIMITED_SCOPE`, 12 `BLOCKED_NOT_IMPLEMENTED`, 1 `BLOCKED_PRODUCT`, and 1 `BLOCKED_ENV`, and every A06–A11/P01/P03–P08/R02 row retains its previous status.

- [ ] **Step 2: Update status only after all gates pass**

Record exact commits, commands, evidence paths, limitations, and the concurrent emergency-fix integration point. Preserve the no-production-deploy boundary.

- [ ] **Step 3: Run final verification**

Re-run backend full tests/vet, frontend full tests/build, JSON parsing, contract equality, status invariants, `git diff --check`, and secret scan against final HEADs.

- [ ] **Step 4: Commit final evidence and status separately**

Backend commit: `docs: accept A05 managed user edit slice`  
Frontend commit: `docs: record A05 joint acceptance`

- [ ] **Step 5: Use the finishing-a-development-branch workflow**

Confirm both worktrees are clean and present merge/PR options. Do not push, merge, deploy, migrate production, or alter production data without the user's explicit instruction for that step.
