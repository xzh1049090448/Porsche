# A14 User Soft-Delete Consumer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver `users.delete` as the first production consumer of the B1-E action-safety foundation, connect it to the frontend, and prove the A12/A14 delete slice through isolated joint acceptance.

**Architecture:** The backend exposes one action-specific Issue/Execute/Query chain and keeps ticket, idempotency, authorization, locking, operation, delete effects, audit, and outbox within the existing typed B1-E boundaries. The frontend uses a dedicated no-replay adapter and memory-only state machine after the backend contract is frozen; one task-only MySQL/Redis fixture is retained for backend and browser acceptance, then removed by exact identity.

**Tech Stack:** Go 1.22+, Gin, GORM, MySQL 8, Redis 7, Vue 3, Pinia, Axios, Element Plus, Node test runner, Vite, Playwright, Docker for an explicitly authorized isolated fixture only.

---

## Scope and fixed boundaries

- Implementation baseline is design commit `19bf5cf`; record the full commit before the first code change.
- Activate only `ActionUsersDelete=6`. The other seven descriptors, every generic action dispatcher, and every unrelated write route remain inactive.
- `DELETE /admin/users/:guid` remains registered but returns fixed `410 Gone` without target lookup or writes.
- The new write contract is `POST /admin/v2/action-verifications`, `POST /admin/v2/users/:guid/actions`, and `GET /admin/v2/operations?scope=users.delete`.
- The normalized deletion reason contains 1–200 Unicode code points and appears only in the management audit row. It must not appear in the user tombstone, auth audit, operation, outbox, error, request log, browser storage, or analytics.
- The v2 list/detail DTO gains read-only integer `auth_version`; legacy `/admin/users` DTOs stay byte-for-byte compatible.
- The delete consumer performs no Redis write and no network call. Database invalidation of the user, sessions, Gateway tokens, and policy rows is the synchronous security fact.
- Migration `0006` only adds the transactional outbox table. Existing migrations and checksums remain immutable. No worker is added in this slice.
- Completion may mark only A12 and A14 as passing for the `users.delete` slice. All unrelated acceptance rows retain their current status.
- This plan does not authorize push, deployment, production migration, production data access, physical deletion, restore, billing, balance, or other admin actions.

## File structure and responsibilities

### Backend repository

| Path | Responsibility |
| --- | --- |
| `internal/dto/admin_user_action.go` | Strict Issue/Execute/Query HTTP decoding, canonical GUID/version/reason validation, and public response types. |
| `internal/dto/admin_user_action_test.go` | Exact JSON, header, Unicode, size, and secret-retention contract tests. |
| `internal/service/admin_users_projection.go` | Adds `auth_version` to v2 read projection only. |
| `internal/models/admin_action_outbox.go` | Stable persistence enums and exact GORM mapping for pending action delivery records. |
| `internal/migration/sql/0006_admin_action_outbox.up.sql` | Forward-only production schema for the transactional outbox. |
| `internal/migration/sql/0006_admin_action_outbox.down.sql` | Explicit local rollback for the new table only. |
| `internal/migration/admin_action_outbox.go` | Information-schema verifier for columns, indexes, FK, checks, and engine. |
| `internal/actionsecurity/registry.go` | Activates only the existing typed `users.delete` descriptor. |
| `internal/service/action_delete_user.go` | Request-bound transaction consumer, success audit facts, and domain-result mapping. |
| `internal/service/action_delete_user_writers.go` | Real management audit and outbox writers used by Execute. |
| `internal/service/action_delete_user_policy.go` | Typed Issue-time version/state validation against the already locked target. |
| `internal/handler/admin_user_actions.go` | Action-specific Issue, Execute, and Query handlers plus fixed public error mapping. |
| `internal/app/state.go` | Constructs the active action service bundle only when all required dependencies exist. |
| `internal/router/router.go` | Registers the three explicit v2 routes; no generic action route. |
| `internal/handler/admin.go` | Replaces the legacy delete body with a dependency-free fixed 410 response. |
| `internal/service/action_delete_user_integration_test.go` | Real MySQL/Redis lifecycle, atomicity, idempotency, credential invalidation, and race coverage. |
| `docs/agents/contracts/admin-action-future-contract.json` | Promotes the backend-owned B1-E draft into the callable A14 HTTP contract. |

### Frontend repository

| Path | Responsibility |
| --- | --- |
| `src/api/admin-users.js` | Accepts and maps the new v2 `auth_version` field. |
| `src/api/admin-user-actions.js` | Dedicated Issue/Execute/Query transport; sensitive POSTs never auto-replay. |
| `src/api/admin-user-actions-state.js` | Pure memory-only delete workflow state machine and bounded query policy. |
| `src/stores/admin-user-actions.js` | Per-view orchestration, secret cleanup, refresh, and success reconciliation. |
| `src/components/admin/UserSoftDeleteDialog.vue` | Accessible confirmation, reason/password form, unknown-result display, and focus restoration. |
| `src/components/admin/UserSoftDeleteDialog.contract.test.js` | Source-contract assertions for dialog fields, warnings, focus behavior, and secret handling without adding a component-test dependency. |
| `src/views/Users.vue` | Capability-gated list action and last-page reconciliation. |
| `src/views/UserDetail.vue` | Capability-gated detail action and deleted-state reconciliation. |
| `src/i18n/messages.js` | Chinese and English soft-delete wording with irreversible semantics. |
| `docs/agents/contracts/prd-260903-interface-draft.json` | Incorporates the same frozen A14 fields into the existing frontend PRD contract document. |

### Shared evidence

| Path | Responsibility |
| --- | --- |
| Backend `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/` | Baseline, fixture identity, backend gates, joint browser evidence, reviews, and cleanup manifest. |
| Frontend `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/frontend-gates.md` | Frontend unit/build and static security evidence. |
| Frontend `docs/agents/validation/joint-acceptance-20260904/acceptance-matrix.json` | Updates only the A12/A14 delete slice after every required gate passes. |
| Backend and frontend `progress.md` | Records bounded status, commit identities, retained blockers, and production exclusions. |

## Task 1: Freeze both repository baselines

**Files:**
- Create: `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/baseline.md`
- Test: backend `init.sh`
- Test: frontend `npm test` and `npm run build`

- [ ] **Step 1: Record exact clean baselines**

Run in the backend worktree:

```bash
pwd
git rev-parse HEAD
git status --short
git log -5 --oneline
sha256sum docs/superpowers/specs/2026-09-05-a14-user-soft-delete-consumer-design.md
```

Run in the frontend worktree:

```bash
pwd
git rev-parse HEAD
git status --short
git log -5 --oneline
```

Expected: both statuses are empty. Stop and report any pre-existing delta; do not reset or clean it.

- [ ] **Step 2: Run the backend baseline gate**

```bash
env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u DATABASE_URL -u REDIS_URL -u APP_ENV -u RUN_START_COMMAND ./init.sh
env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -p 1 ./... -count=1
go build ./...
go vet ./...
git diff --check
```

Expected: PASS; fixture-only tests may explicitly SKIP because `TEST_*` is absent. A sandbox loopback-bind denial is rerun unchanged with the minimum approved permission and recorded as infrastructure, not product failure.

- [ ] **Step 3: Run the frontend baseline gate**

```bash
npm test
npm run build
git diff --check
```

Expected: PASS with the exact test count recorded.

- [ ] **Step 4: Write and commit the baseline record**

Record repository paths, full commits, commands, exit codes, test counts, skips, and clean statuses. Do not record environment values or credentials.

```bash
git add docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/baseline.md
git diff --cached --name-only
git commit -m "docs: record A14 implementation baseline"
```

Expected: exactly one staged evidence file.

## Task 2: Freeze strict action DTOs and v2 user versions

**Files:**
- Create: `internal/dto/admin_user_action.go`
- Create: `internal/dto/admin_user_action_test.go`
- Modify: `internal/service/admin_users_projection.go`
- Modify: `internal/service/admin_users_projection_test.go`
- Test: `internal/handler/admin_users_read_test.go`

- [ ] **Step 1: Write RED DTO and projection tests**

Define the public types exactly:

```go
type IssueUserDeleteRequest struct {
    Action          string
    TargetGUID      int64
    ExpectedVersion int
    Reason          string
    Password        []byte
}

type ExecuteUserDeleteRequest struct {
    ExpectedVersion int
    Reason          string
}

type DeleteUserResponse struct {
    OperationRef string `json:"operation_ref"`
    User struct {
        GUID   string `json:"guid"`
        Status string `json:"status"`
    } `json:"user"`
}
```

Test a 4 KiB maximum body, one object, exact fields, duplicate keys, unknown keys, trailing JSON, canonical positive signed-int64 decimal GUID, version `1..2147483647`, and trimmed reason length of 1–200 Unicode code points. Assert the decoder returns a normalized reason and a password byte slice that callers can clear.

Add v2 list/detail assertions for integer `auth_version`; add legacy snapshots proving the old response shape has no new field.

- [ ] **Step 2: Run RED tests**

```bash
go test ./internal/dto ./internal/service ./internal/handler -run 'UserDelete|AuthVersion|LegacyAdminUserShape' -count=1
```

Expected: FAIL because the DTO decoder and v2 field do not exist.

- [ ] **Step 3: Implement one strict decoder path**

Use a token-level duplicate-field check before decoding and reject any second top-level or nested key. Normalize only the reason:

```go
func NormalizeDeleteReason(raw string) (string, error) {
    reason := strings.TrimSpace(raw)
    n := utf8.RuneCountInString(reason)
    if n < 1 || n > 200 {
        return "", ErrInvalidDeleteReason
    }
    return reason, nil
}
```

Convert `current_password` to a dedicated byte copy, clear the raw body buffer immediately after decode, and require the handler to `defer clear(request.Password)`. Never format the request in an error.

- [ ] **Step 4: Add `auth_version` only to the v2 projection**

```go
type UserReadDTO struct {
    // existing v2 fields
    AuthVersion int `json:"auth_version"`
}
```

Project `users.auth_version` and reject non-positive stored values as an internal consistency error. Do not change the legacy `dto.AdminUser` type or its serializer.

- [ ] **Step 5: Run GREEN tests and commit**

```bash
go test ./internal/dto ./internal/service ./internal/handler -run 'UserDelete|AuthVersion|LegacyAdminUserShape' -count=1
git add internal/dto/admin_user_action.go internal/dto/admin_user_action_test.go internal/service/admin_users_projection.go internal/service/admin_users_projection_test.go internal/handler/admin_users_read_test.go
git diff --cached --name-only
git commit -m "feat: define user delete action contract"
```

Expected: focused tests PASS and only the listed files are staged.

## Task 3: Add migration 0006 and the outbox model

**Files:**
- Create: `internal/models/admin_action_outbox.go`
- Create: `internal/models/admin_action_outbox_test.go`
- Create: `internal/migration/sql/0006_admin_action_outbox.up.sql`
- Create: `internal/migration/sql/0006_admin_action_outbox.down.sql`
- Create: `internal/migration/admin_action_outbox.go`
- Create: `internal/migration/admin_action_outbox_test.go`
- Modify: `internal/migration/runner.go`
- Modify: `internal/migration/runner_test.go`

- [ ] **Step 1: Write RED model and migration-ledger tests**

Use stable values:

```go
type AdminActionDeliveryState int

const (
    AdminActionDeliveryPending   AdminActionDeliveryState = 1
    AdminActionDeliveryDelivered AdminActionDeliveryState = 2
    AdminActionDeliveryDead      AdminActionDeliveryState = 3
)

type AdminActionOutbox struct {
    ID int64
    AuditFields
    OperationID int64
    PublicRef string
    Action int
    TargetKind int
    TargetGUID *int64
    State AdminOperationState
    ResultGUID *int64
    DeliveryState AdminActionDeliveryState
    AvailableAt int64
    DeliveredAt *int64
    AttemptCount int
}
```

Assert `TableName() == "admin_action_outbox"`, exact enum values, migration sequence `0001..0006`, and unchanged checksums for `0001..0005`.

- [ ] **Step 2: Run RED tests**

```bash
go test ./internal/models ./internal/migration -run 'AdminActionOutbox|MigrationLedger' -count=1
```

Expected: FAIL because model and migration 0006 are absent.

- [ ] **Step 3: Write the exact forward schema**

The up migration creates one InnoDB/utf8mb4 table with:

```sql
CREATE TABLE admin_action_outbox (
  id BIGINT NOT NULL AUTO_INCREMENT,
  guid BIGINT NOT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NOT NULL,
  is_deleted TINYINT(1) NOT NULL DEFAULT 0,
  operation_id BIGINT NOT NULL,
  public_ref CHAR(46) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  action INT NOT NULL,
  target_kind INT NOT NULL,
  target_guid BIGINT NULL,
  state INT NOT NULL,
  result_guid BIGINT NULL,
  delivery_state INT NOT NULL DEFAULT 1,
  available_at BIGINT NOT NULL,
  delivered_at BIGINT NULL,
  attempt_count INT NOT NULL DEFAULT 0,
  PRIMARY KEY (id),
  UNIQUE KEY uk_admin_action_outbox_guid (guid),
  UNIQUE KEY uk_admin_action_outbox_operation (operation_id),
  UNIQUE KEY uk_admin_action_outbox_public_ref (public_ref),
  KEY idx_admin_action_outbox_delivery (delivery_state,is_deleted,available_at,id),
  KEY idx_admin_action_outbox_target (target_kind,target_guid,is_deleted,created_at),
  CONSTRAINT fk_admin_action_outbox_operation FOREIGN KEY (operation_id) REFERENCES admin_operations(id),
  CONSTRAINT chk_admin_action_outbox_action CHECK (action BETWEEN 1 AND 2147483647),
  CONSTRAINT chk_admin_action_outbox_target_kind CHECK (target_kind IN (1,2,3)),
  CONSTRAINT chk_admin_action_outbox_state CHECK (state IN (2,3,4)),
  CONSTRAINT chk_admin_action_outbox_delivery_state CHECK (delivery_state IN (1,2,3)),
  CONSTRAINT chk_admin_action_outbox_attempt_count CHECK (attempt_count BETWEEN 0 AND 2147483647),
  CONSTRAINT chk_admin_action_outbox_delivery_time CHECK ((delivery_state=1 AND delivered_at IS NULL) OR (delivery_state IN (2,3) AND delivered_at IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
```

Before implementation, compare `AdminOperationState` values with the `state IN (...)` set and use the exact existing terminal-state integers. Do not invent or renumber a state. The down migration is exactly `DROP TABLE admin_action_outbox;`.

- [ ] **Step 4: Add the verifier and runner entry**

```go
func verifyAdminActionOutbox(ctx context.Context, db *gorm.DB) error
```

Verify every column type/nullability/default, named index column order, FK target/delete rule, CHECK clause, engine, charset, and collation after both fresh apply and existing-schema startup. Add 0006 to `All()` and call the verifier from Apply and Verify.

- [ ] **Step 5: Run GREEN unit tests and commit**

```bash
go test ./internal/models ./internal/migration -run 'AdminActionOutbox|MigrationLedger' -count=1
git diff -- internal/migration/sql/0001* internal/migration/sql/0002* internal/migration/sql/0003* internal/migration/sql/0004* internal/migration/sql/0005*
git add internal/models/admin_action_outbox.go internal/models/admin_action_outbox_test.go internal/migration/sql/0006_admin_action_outbox.up.sql internal/migration/sql/0006_admin_action_outbox.down.sql internal/migration/admin_action_outbox.go internal/migration/admin_action_outbox_test.go internal/migration/runner.go internal/migration/runner_test.go
git commit -m "feat: add admin action outbox schema"
```

Expected: tests PASS and the diff for migrations 0001–0005 is empty.

## Task 4: Activate only the typed users.delete descriptor

**Files:**
- Modify: `internal/actionsecurity/registry.go`
- Modify: `internal/actionsecurity/registry_test.go`
- Modify: `internal/actionsecurity/intent_users.go`
- Modify: `internal/actionsecurity/intent_users_test.go`

- [ ] **Step 1: Write RED registry and intent tests**

Assert `ActiveActionRegistry()` contains exactly one copied descriptor:

```go
Descriptor{
    Action: ActionUsersDelete,
    Name: "users.delete",
    Capability: "users.delete",
    RequiresTicket: true,
    TargetKind: TargetUser,
    Active: true,
}
```

Assert every other descriptor remains inactive and `ResolveActiveAction` resolves only integer action 6. Add reason normalization/version overflow cases to `DeleteUserIntent` and assert canonical encoding is identical between Issue and Execute inputs after normalization.

- [ ] **Step 2: Run RED tests**

```bash
go test ./internal/actionsecurity -run 'ActiveActionRegistry|DeleteUserIntent' -count=1
```

Expected: FAIL because production registry is still empty or reason limits are absent.

- [ ] **Step 3: Implement the minimal activation**

Keep all descriptor definitions in the inactive catalog and construct the active copy by selecting only `ActionUsersDelete`, setting `Active=true`, and returning a defensive slice copy. Bind its encoder directly to `DeleteUserIntent`; do not accept maps or runtime action strings.

- [ ] **Step 4: Run GREEN tests and commit**

```bash
go test ./internal/actionsecurity -run 'Registry|DeleteUserIntent' -count=1
git add internal/actionsecurity/registry.go internal/actionsecurity/registry_test.go internal/actionsecurity/intent_users.go internal/actionsecurity/intent_users_test.go
git commit -m "feat: activate users delete action descriptor"
```

Expected: PASS; registry test proves active count is exactly one.

## Task 5: Implement fresh delete authorization policy

**Files:**
- Create: `internal/service/action_delete_user_policy.go`
- Create: `internal/service/action_delete_user_policy_test.go`
- Modify: `internal/service/action_identity.go`
- Modify: `internal/service/action_identity_test.go`
- Modify: `internal/service/action_verification.go`
- Modify: `internal/service/action_verification_test.go`

- [ ] **Step 1: Write RED policy matrix tests**

Extend the internal locked identity to retain the already locked target for typed validation and define:

```go
type lockedActionIdentity struct {
    actor models.User
    session models.Session
    target *models.User
}

func validateLockedDeleteIntent(descriptor actionsecurity.Descriptor, intent any, target *models.User) error
```

Cover Root→User, Root→Admin, and authorized Admin→User success through the existing evaluator. Cover self, Root target, same/higher role, missing/soft-deleted/hidden target as the existing hidden error; capability deny as the existing forbidden error; disabled actor/session as forbidden; and expected-version mismatch/overflow as a fixed rejection. Assert Issue executes this validator after actor/session/target/policy locks and before password verification/ticket insert.

- [ ] **Step 2: Run RED tests**

```bash
go test ./internal/service -run 'DeleteUserPolicy' -count=1
```

Expected: FAIL because policy does not exist.

- [ ] **Step 3: Implement the existing global lock order**

Keep the existing actor → logical session → target → policy head → rules `FOR UPDATE` order and evaluator. Return the selected target from `lockActionIdentity` only inside `lockedActionIdentity`; never expose it to handlers. In `ActionVerificationService.Issue`, switch on the resolved descriptor action and call `validateLockedDeleteIntent` for `ActionUsersDelete`; the helper requires `DeleteUserIntent`, matching GUID, active allowed status, exact `auth_version`, and version below `2147483647`. Unknown future actions fail closed until they add their own typed validation.

- [ ] **Step 4: Run GREEN tests and commit**

```bash
go test ./internal/service -run 'DeleteUserPolicy|ActionIdentity|ActionVerification' -count=1
git add internal/service/action_delete_user_policy.go internal/service/action_delete_user_policy_test.go internal/service/action_identity.go internal/service/action_identity_test.go internal/service/action_verification.go internal/service/action_verification_test.go
git commit -m "feat: authorize user delete actions"
```

## Task 6: Implement transaction-bound audit and outbox writers

**Files:**
- Create: `internal/service/action_delete_user_writers.go`
- Create: `internal/service/action_delete_user_writers_test.go`

- [ ] **Step 1: Write RED writer tests**

Use a request-bound execution object plus a generic outbox writer:

```go
type DeleteUserExecution struct {
    intent actionsecurity.DeleteUserIntent
    facts deleteUserAuditFacts
    ids IDGenerator
    now Clock
}

func NewDeleteUserExecution(intent actionsecurity.DeleteUserIntent, ids IDGenerator, now Clock) (*DeleteUserExecution, error)
func NewAdminActionOutboxWriter(ids IDGenerator, now Clock) *AdminActionOutboxWriter
```

`DeleteUserExecution` implements both `TransactionalActionConsumer` and `TransactionalAuditWriter`, so it can retain normalized request intent and success facts without expanding the generic B1-E event. Assert the audit allowlist is exactly `operation_ref`, `actor_guid`, `target_guid`, `reason`, `before_status`, `after_status`; IP is null; created/updated actor is the operator. Assert outbox has one pending row per operation and contains no reason, password, ticket, idempotency key, intent digest, or HMAC.

- [ ] **Step 2: Run RED tests**

```bash
go test ./internal/service -run 'DeleteUserAuditWriter|AdminActionOutboxWriter' -count=1
```

Expected: FAIL because the production writers do not exist.

- [ ] **Step 3: Implement writers that require the caller transaction**

```go
func (e *DeleteUserExecution) Write(ctx context.Context, tx *gorm.DB, event ActionAuditEvent) error
func (w *AdminActionOutboxWriter) Write(ctx context.Context, tx *gorm.DB, event ActionOutboxEvent) error
```

Reject nil transactions and non-`users.delete` events. The execution writer checks the event target/result against facts recorded by its own preceding consumer call, then writes `audit_logs`; it cannot be reused for a second Execute. The outbox writer resolves the already existing operation ID by `public_ref` through the same transaction, then inserts one pending row. Use only the passed `tx`; never use the root DB handle.

- [ ] **Step 4: Run GREEN tests and commit**

```bash
go test ./internal/service -run 'DeleteUserAuditWriter|AdminActionOutboxWriter' -count=1
git add internal/service/action_delete_user_writers.go internal/service/action_delete_user_writers_test.go
git commit -m "feat: persist user delete audit and outbox"
```

## Task 7: Implement the atomic delete consumer

**Files:**
- Create: `internal/service/action_delete_user.go`
- Create: `internal/service/action_delete_user_test.go`

- [ ] **Step 1: Write RED consumer tests**

Define the consumer boundary:

```go
func (e *DeleteUserExecution) Execute(
    ctx context.Context,
    tx *gorm.DB,
    operation models.AdminOperation,
) (TerminalOutcome, error)
```

Test exact row effects: all active sessions revoked and version incremented; all valid Gateway tokens revoked; permission heads/overrides logically deleted; user disabled/soft-deleted/auth version incremented; password hash, phone, nickname, real name, ID-card hash cleared; verification false. Assert internal ID, GUID, username, role, and created audit remain. Assert one auth audit `user_deleted` without reason.

Add zero-side-effect cases for hidden target, stale version, illegal state/role, and `auth_version == 2147483647`.

- [ ] **Step 2: Run RED tests**

```bash
go test ./internal/service -run 'DeleteUserConsumer' -count=1
```

Expected: FAIL because consumer is absent.

- [ ] **Step 3: Implement minimal transaction-only mutations**

Use the transaction passed by `ActionOperationService.Execute`. Every update includes the expected active predicate and checks `RowsAffected`; map a changed row to the exact conflict code. Do not call Redis, start a nested transaction, emit a goroutine, or invoke `AuthService.SoftDeleteUser`.

Use `e.intent` for the target/version/reason binding and record only target internal ID, target GUID, and before/after status into `e.facts` for the immediately following audit write. Return:

```go
TerminalOutcome{
    HTTPStatus: http.StatusOK,
    ResultKind: models.ResultUser,
    ResultGUID: &e.intent.TargetGUID,
}
```

- [ ] **Step 4: Run GREEN tests and commit**

```bash
go test ./internal/service -run 'DeleteUserConsumer' -count=1
git add internal/service/action_delete_user.go internal/service/action_delete_user_test.go
git commit -m "feat: add atomic user soft delete consumer"
```

## Task 8: Wire the active action service bundle

**Files:**
- Modify: `internal/app/state.go`
- Modify: `internal/app/state_test.go`
- Create: `internal/service/action_delete_user_bundle.go`
- Create: `internal/service/action_delete_user_bundle_test.go`

- [ ] **Step 1: Write RED construction tests**

Add one explicit aggregate:

```go
type UserDeleteActions struct {
    Verifications *ActionVerificationService
    Operations *ActionOperationService
    Outbox *AdminActionOutboxWriter
    NewExecution func(actionsecurity.DeleteUserIntent) (*DeleteUserExecution, error)
}
```

Assert it exists only when DB, Auth Redis, action-security crypto, identity/authorization dependencies, ID generator, and clock are all present. Missing any dependency leaves all action routes unavailable rather than partially active.

- [ ] **Step 2: Run RED tests**

```bash
go test ./internal/app ./internal/service -run 'UserDeleteActions|ActionBundle' -count=1
```

Expected: FAIL because state has no active bundle.

- [ ] **Step 3: Construct one coherent bundle**

Add `UserDeleteActions *service.UserDeleteActions` to `app.State`. Reuse the existing `ActionVerificationService`; construct operations and outbox once, and inject a factory that defensively copies each normalized intent into a one-shot execution/audit object. Validate active registry count/name at startup and return a sanitized configuration error if it differs.

- [ ] **Step 4: Run GREEN tests and commit**

```bash
go test ./internal/app ./internal/service -run 'UserDeleteActions|ActionBundle' -count=1
git add internal/app/state.go internal/app/state_test.go internal/service/action_delete_user_bundle.go internal/service/action_delete_user_bundle_test.go
git commit -m "feat: wire user delete action services"
```

## Task 9: Expose the exact Issue, Execute, and Query routes

**Files:**
- Create: `internal/handler/admin_user_actions.go`
- Create: `internal/handler/admin_user_actions_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_test.go`

- [ ] **Step 1: Write RED route and handler contract tests**

Assert only these routes are added:

```text
POST /admin/v2/action-verifications
POST /admin/v2/users/:guid/actions
GET  /admin/v2/operations?scope=users.delete
```

Issue rejects action headers and decodes the exact Issue body. Execute requires exactly one `Idempotency-Key` and `X-Action-Ticket`; rejects commas/duplicates; accepts only body action `delete`; reconstructs the identical normalized intent. Query requires exactly one idempotency key, exact scope, and no ticket.

Test `Cache-Control: no-store`, non-empty `X-Request-ID`, 201 Issue response, committed 200 Execute response, Query response, and fixed `admin_action_error` envelope for 400/401/403/404/409/410/422/429/503. Only `operation_commit_unknown` may include `operation_ref`.

- [ ] **Step 2: Run RED tests**

```bash
go test ./internal/handler ./internal/router -run 'AdminUserActions|ActionRouteInventory' -count=1
```

Expected: FAIL because routes are absent.

- [ ] **Step 3: Implement action-specific handlers**

Register only when `state.UserDeleteActions != nil`. Use the authenticated actor/session/request ID from existing middleware, immediately clear password bytes after Issue, and never log request bodies or sensitive headers. Map service results through one exhaustive switch:

```go
type adminActionError struct {
    Error struct {
        Type string `json:"type"`
        Code string `json:"code"`
        Message string `json:"message"`
        RequestID string `json:"request_id"`
        OperationRef string `json:"operation_ref,omitempty"`
    } `json:"error"`
}
```

Serialize regular errors exactly as `{"error":{"code":"...","message":"请求无法完成","type":"admin_action_error","request_id":"..."}}`; append `operation_ref` only for `operation_commit_unknown`. Set integer `Retry-After` only for 429 and Query `processing`.

The Execute handler performs the fixed sequence below and never calls `Execute` for an already terminal `Begin` view:

```go
identity, existing, err := actions.Operations.Begin(ctx, begin)
if err != nil { /* fixed error mapping */ }
if existing != nil { /* map the existing terminal or processing view */ }
execution, err := actions.NewExecution(intent)
if err != nil { /* fixed 400 mapping */ }
view, err := actions.Operations.Execute(ctx, identity, execution, execution, actions.Outbox)
```

For a reliable succeeded view, serialize the target GUID from the already HMAC-bound intent and status `deleted`; for commit unknown, return only the safe operation reference supplied by `CommitUnknownError`.

- [ ] **Step 4: Prove no generic dispatcher exists**

```bash
rg -n 'actions/:action|/admin/v2/actions|map\[string\].*Consumer|InactiveActionDescriptors\(' internal --glob '*.go'
```

Expected: no generic route/consumer map; production use of inactive descriptors remains limited to registry construction/tests.

- [ ] **Step 5: Run GREEN tests and commit**

```bash
go test ./internal/handler ./internal/router -run 'AdminUserActions|ActionRouteInventory' -count=1
git add internal/handler/admin_user_actions.go internal/handler/admin_user_actions_test.go internal/router/router.go internal/router/router_test.go
git commit -m "feat: expose user delete action endpoints"
```

## Task 10: Retire the legacy DELETE write path

**Files:**
- Modify: `internal/handler/admin.go`
- Modify: `internal/handler/admin_test.go`
- Modify: `internal/router/router_test.go`

- [ ] **Step 1: Write a RED zero-dependency legacy test**

Call `DELETE /admin/users/:guid` with a state whose DB/Auth/Redis collaborators panic if used. Assert fixed 410, `Cache-Control: no-store`, request ID, stable migration error, and zero collaborator calls for canonical, nonexistent, malformed, and already-deleted GUID strings.

- [ ] **Step 2: Run RED test**

```bash
go test ./internal/handler ./internal/router -run 'LegacyAdminDeleteGone' -count=1
```

Expected: FAIL because the handler still performs a lookup and soft delete.

- [ ] **Step 3: Replace only the DELETE body**

```go
admin.DELETE("/users/:guid", func(c *gin.Context) {
    c.Header("Cache-Control", "no-store")
    writeAdminActionError(c, http.StatusGone, "legacy_user_delete_gone", "Use the verified v2 user delete action flow.", "")
})
```

Do not parse the path and do not call `AuthService.SoftDeleteUser`. Preserve all legacy GET and PUT behavior.

- [ ] **Step 4: Run GREEN regression and commit**

```bash
go test ./internal/handler ./internal/router -run 'LegacyAdminDeleteGone|AdminUsers' -count=1
git add internal/handler/admin.go internal/handler/admin_test.go internal/router/router_test.go
git commit -m "feat: retire legacy admin user delete"
```

## Task 11: Prove backend behavior without external fixtures

**Files:**
- Modify: focused `_test.go` files from Tasks 2–10 only when a concrete gate exposes a defect
- Create: `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/backend-no-fixture.md`

- [ ] **Step 1: Run focused packages, race, full test, build, and vet**

```bash
env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -p 1 ./internal/actionsecurity ./internal/dto ./internal/models ./internal/migration ./internal/service ./internal/handler ./internal/router ./internal/app -count=1
env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -race ./internal/actionsecurity ./internal/service ./internal/handler -run 'UserDelete|Action' -count=1
env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -p 1 ./... -count=1
go build ./...
go vet ./...
git diff --check
```

Expected: zero FAIL; real-fixture tests explicitly SKIP with one reason if variables are absent.

- [ ] **Step 2: Scan production boundaries and secrets**

```bash
rg -n 'current_password|X-Action-Ticket|Idempotency-Key|reason' internal --glob '*.go'
rg -n 'ActionUsers(CreateAdmin|ResetPassword|Promote|Demote|PermissionsWrite)|ActionPublicContent' internal --glob '*.go'
rg -n 'SoftDeleteUser\(' internal --glob '*.go'
```

Expected: sensitive values appear only in strict decode/use/clear code and tests; no logger includes them; only users.delete is active; legacy handler has no `SoftDeleteUser` call.

- [ ] **Step 3: Record and commit exact evidence**

```bash
git add docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/backend-no-fixture.md
git commit -m "docs: record A14 backend no-fixture gates"
```

## Task 12: Validate migration and delete semantics in one isolated fixture

**Files:**
- Create: `internal/service/action_delete_user_integration_test.go`
- Create: `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/fixture-lifecycle.md`
- Create: `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/backend-real-fixture.md`

- [ ] **Step 1: Write the lifecycle plan before creating resources**

Generate one unique task suffix with `A14_FIXTURE_SUFFIX="$(date -u +%Y%m%d%H%M%S)-$(openssl rand -hex 4)"`, validate it against `^[0-9]{14}-[0-9a-f]{8}$`, and retain that shell variable for every fixture command. Record only container names, image digests, loopback ports, database name, creation time, `AutoRemove`, read-only rootfs/tmpfs settings, and exact cleanup commands. Credentials remain in an untracked mode-0600 temp env file at `/tmp/a14-user-delete-${A14_FIXTURE_SUFFIX}.env` and never enter evidence.

Required identities:

```text
a14-user-delete-mysql-${A14_FIXTURE_SUFFIX}
a14-user-delete-redis-${A14_FIXTURE_SUFFIX}
a14_user_delete_${A14_FIXTURE_SUFFIX}
```

- [ ] **Step 2: Create only the declared loopback fixture**

Use pinned MySQL 8 and Redis 7 images, random task-only credentials, `127.0.0.1` port binding, `--read-only`, `--tmpfs`, health checks, and labels containing the suffix. Inspect the created resources and stop if any identity differs from the plan.

- [ ] **Step 3: Write RED real integration cases**

Cover migration `0001→0006`, verifier, `0006 down/up`, Root→User, Root→Admin, authorized Admin→User, self/Root/equal/higher/deny/hidden, version and state conflicts, version overflow, ticket expiry/replay, same/different key payload, cross-session, refresh in same logical session, duplicate delete, and username re-registration conflict.

Add fault injection at every user/session/token/policy/auth-audit/management-audit/outbox/terminal-operation write and commit-return boundary. Assert pre-commit faults leave zero partial delete facts. Commit-unknown returns 503 plus the operation reference and resolves only through Query.

- [ ] **Step 4: Run real fixture, race, and credential-invalidation gates**

```bash
set -a
source "/tmp/a14-user-delete-${A14_FIXTURE_SUFFIX}.env"
set +a
go test -p 1 ./internal/migration ./internal/service ./internal/handler -run 'AdminActionOutbox|DeleteUser|ActionVerification|ActionOperation' -count=1
go test -race ./internal/service ./internal/handler -run 'DeleteUser.*Concurrent|Action.*Concurrent' -count=1
```

Expected: PASS with exact counts. After committed deletion, old Access, Refresh, logical session, and Gateway token requests all reject; username creation conflicts; the user row still exists; one management audit and one outbox row exist.

- [ ] **Step 5: Retain the exact healthy fixture for frontend joint acceptance**

Record inspection evidence and explicitly mark the two exact containers `RETAINED_FOR_TASK_18`. Do not remove or reuse them for unrelated work.

- [ ] **Step 6: Commit tests and redacted evidence**

```bash
git add internal/service/action_delete_user_integration_test.go docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/fixture-lifecycle.md docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/backend-real-fixture.md
git commit -m "test: verify real user delete lifecycle"
```

## Task 13: Freeze the synchronized HTTP contract

**Files:**
- Modify: `docs/agents/contracts/admin-action-future-contract.json`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/docs/agents/contracts/prd-260903-interface-draft.json`
- Create: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/src/api/admin-user-actions-contract.test.js`
- Create: `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/backend-contract-review.md`

- [ ] **Step 1: Write the backend contract object**

Promote the backend contract from `inactive_contract` to `active_users_delete_contract`, set production-observation semantics to the three registered routes, and update the implementation booleans to true for backend handler/route while frontend remains false. Add exact machine-readable entries for v2 `auth_version`, Issue, Execute, Query, legacy 410, all request/response examples, required/forbidden headers, status/error matrix, retry rules, and these security properties:

```json
{
  "scope": "users.delete",
  "sensitivePostAutoReplay": false,
  "clientPersistence": "memory_only",
  "reasonPersistence": ["audit_logs.detail"],
  "legacyDeleteStatus": 410
}
```

Use the contract file's existing schema and ordering; do not introduce a parallel format.

- [ ] **Step 2: Validate JSON and compare runtime fixtures**

```bash
python3 -m json.tool docs/agents/contracts/admin-action-future-contract.json >/dev/null
go test ./internal/dto ./internal/handler ./internal/router -run 'UserDelete|AuthVersion|LegacyAdminDeleteGone' -count=1
```

Expected: JSON parses and runtime contract tests PASS.

- [ ] **Step 3: Merge the exact A14 contract into the existing frontend draft**

```bash
python3 -m json.tool /Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/docs/agents/contracts/prd-260903-interface-draft.json >/dev/null
```

Update `admin_users_list` and `admin_user_detail` exact fields with `auth_version`; replace the draft `admin_user_action` and `action_verification` descriptions with the backend's exact request/response/header/error rules; add the exact operation-query interface and legacy-410 declaration. Set only these A14 entries to `AGREED_FOR_IMPLEMENTATION`; preserve unrelated draft interfaces. Add a contract test that reads the frontend document and the backend document from `A14_BACKEND_CONTRACT`, then asserts equality of the A14 request/response examples, header rules, status set, replay limits, and security properties. The test must fail with `missing_A14_BACKEND_CONTRACT` when the variable is absent rather than guessing a checkout path.

Run the cross-repository equality test from the frontend worktree:

```bash
A14_BACKEND_CONTRACT=/Users/xuzhihao/code/Porsche/.worktrees/admin-public-260903/docs/agents/contracts/admin-action-future-contract.json node --test src/api/admin-user-actions-contract.test.js
```

Expected: PASS and all compared A14 fields are equal.

- [ ] **Step 4: Record review and commit in each repository**

Backend:

```bash
git add docs/agents/contracts/admin-action-future-contract.json internal/dto/admin_action_contract_test.go docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/backend-contract-review.md
git commit -m "docs: freeze user delete interface contract"
```

Frontend:

```bash
git add docs/agents/contracts/prd-260903-interface-draft.json src/api/admin-user-actions-contract.test.js
git commit -m "docs: sync user delete interface contract"
```

## Task 14: Add the frontend contract adapter

**Files:**
- Modify: `src/api/admin-users.js`
- Modify: `src/api/admin-users.test.js`
- Create: `src/api/admin-user-actions.js`
- Create: `src/api/admin-user-actions.test.js`
- Modify: `src/api/request.js`
- Modify: `src/api/auth-request-policy.test.js`

- [ ] **Step 1: Write RED API tests**

Require `mapUserReadDto` to accept exact integer `auth_version` and expose `authVersion`. Define:

```js
export async function issueUserDelete({ targetGuid, expectedAuthVersion, reason, currentPassword })
export async function executeUserDelete({ targetGuid, expectedAuthVersion, reason, ticket, idempotencyKey })
export async function queryUserDelete({ idempotencyKey })
```

Assert Issue sends no action headers; Execute sends one exact ticket/key header; Query sends key and `scope=users.delete`; POST is attempted exactly once on 401/network failure; Query GET may refresh and retry once. Assert error mapping retains only public code/message/status/operationRef/retryAfter and never copies password, ticket, key, reason, raw config, or response body.

- [ ] **Step 2: Run RED tests**

```bash
node --test src/api/admin-users.test.js src/api/admin-user-actions.test.js src/api/auth-request-policy.test.js
```

Expected: FAIL because auth version and action adapter are absent.

- [ ] **Step 3: Implement a dedicated no-replay POST transport**

Create an Axios instance that shares base URL and explicit bearer acquisition but has no 401 response interceptor for Issue/Execute. Keep the existing refresh-capable GET transport for Query. Generate no idempotency key in this file; accept it only from the workflow state machine.

- [ ] **Step 4: Map the v2 field and exact action responses**

```js
return {
  // existing mapped fields
  authVersion: assertPositiveInteger(dto.auth_version, 'auth_version')
}
```

Reject extra/missing action response keys through the existing exact-key validator.

- [ ] **Step 5: Run GREEN tests and commit**

```bash
node --test src/api/admin-users.test.js src/api/admin-user-actions.test.js src/api/auth-request-policy.test.js
git add src/api/admin-users.js src/api/admin-users.test.js src/api/admin-user-actions.js src/api/admin-user-actions.test.js src/api/request.js src/api/auth-request-policy.test.js
git commit -m "feat: add user delete action API adapter"
```

## Task 15: Add the memory-only delete workflow state machine

**Files:**
- Create: `src/api/admin-user-actions-state.js`
- Create: `src/api/admin-user-actions-state.test.js`

- [ ] **Step 1: Write RED transition tests**

Define states and public methods:

```js
export const DELETE_STATES = Object.freeze({
  IDLE: 'idle', VERIFYING: 'verifying', SUBMITTING: 'submitting',
  UNKNOWN: 'unknown', QUERYING: 'querying', SUCCEEDED: 'succeeded',
  FAILED: 'failed', PENDING_RECOVERY: 'pending_recovery'
})

export function createUserDeleteWorkflow({ api, randomBytes, now, schedule })
```

Test idle→verifying→submitting→succeeded; every known failure→failed→idle; commit unknown/network ambiguity→unknown→querying; processing with integer Retry-After 1–30; succeeded/failed/pending_recovery terminals; duplicate-click suppression; unmount cancellation; and secret/key/ticket clearing.

Assert the idempotency key is generated once from cryptographically secure random bytes and never written to `localStorage`, `sessionStorage`, URL, console, or analytics.

- [ ] **Step 2: Run RED tests**

```bash
node --test src/api/admin-user-actions-state.test.js
```

Expected: FAIL because state machine is absent.

- [ ] **Step 3: Implement explicit transitions and bounded polling**

Allow transitions only through a closed transition table. Store password only during Issue, ticket only through Execute, and key only until known completion/known failure/unmount. Query only after ambiguous Execute result and stop immediately on pending recovery. Use server Retry-After clamped to 1–30 seconds; do not replay either POST.

- [ ] **Step 4: Run GREEN tests and commit**

```bash
node --test src/api/admin-user-actions-state.test.js
git add src/api/admin-user-actions-state.js src/api/admin-user-actions-state.test.js
git commit -m "feat: add user delete workflow state machine"
```

## Task 16: Connect the store, dialog, list, and detail views

**Files:**
- Create: `src/stores/admin-user-actions.js`
- Create: `src/stores/admin-user-actions.test.js`
- Create: `src/components/admin/UserSoftDeleteDialog.vue`
- Create: `src/components/admin/UserSoftDeleteDialog.contract.test.js`
- Modify: `src/views/Users.vue`
- Modify: `src/views/UserDetail.vue`
- Modify: `src/i18n/messages.js`
- Create: `src/views/admin-user-delete.contract.test.js`

- [ ] **Step 1: Write RED behavior tests**

Assert the delete action renders only when capability includes `users.delete`, target is a manageable lower role, and status is not deleted. The dialog shows username/GUID and irreversible soft-delete/username-occupied/session-token invalidation warnings in Chinese and English; reason/password are required; password uses `autocomplete="current-password"`; submit is disabled while verifying/submitting/querying.

Test successful list-row removal, refetch of the previous valid page when the last row disappears, preservation of filters/sort, detail transition to deleted read-only state, 409 target refresh, 401 full reset, pending-recovery guidance, focus trap, Escape/close behavior, error focus, and trigger-focus restoration.

- [ ] **Step 2: Run RED tests**

```bash
node --test src/stores/admin-user-actions.test.js src/components/admin/UserSoftDeleteDialog.contract.test.js src/views/admin-user-delete.contract.test.js
```

Expected: FAIL because store/dialog/action hooks are absent.

- [ ] **Step 3: Implement the store boundary**

The store owns one workflow instance per open dialog, derives the intent from the displayed `guid/authVersion/reason`, delegates HTTP to `admin-user-actions.js`, and exposes only display-safe state. Its `close()` and view-unmount hooks overwrite password/reason/ticket/key references and cancel scheduled Query.

- [ ] **Step 4: Implement accessible UI and reconciliation**

Use Element Plus dialog/form primitives already in the repository. Reliable Execute 200 triggers reconciliation; ambiguous results keep the row/detail unchanged until Query resolves. Do not add money mocks or any UI for the seven inactive actions.

- [ ] **Step 5: Run GREEN tests and commit**

```bash
node --test src/stores/admin-user-actions.test.js src/components/admin/UserSoftDeleteDialog.contract.test.js src/views/admin-user-delete.contract.test.js
git add src/stores/admin-user-actions.js src/stores/admin-user-actions.test.js src/components/admin/UserSoftDeleteDialog.vue src/components/admin/UserSoftDeleteDialog.contract.test.js src/views/Users.vue src/views/UserDetail.vue src/i18n/messages.js src/views/admin-user-delete.contract.test.js
git commit -m "feat: add verified user soft delete UI"
```

## Task 17: Run complete frontend gates and security review

**Files:**
- Create: `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/frontend-gates.md`
- Modify: implementation/tests only for defects demonstrated by these gates

- [ ] **Step 1: Run complete tests and production build**

```bash
npm test
npm run build
git diff --check
```

Expected: zero FAIL and successful production bundle.

- [ ] **Step 2: Inspect the built output and source for leakage/replay**

```bash
rg -n 'localStorage|sessionStorage|console\.(log|info|debug)|current_password|X-Action-Ticket|Idempotency-Key' src dist
rg -n 'users\.(create_admin|reset_password|promote|demote|permissions_write)|public_content\.(publish|rollback)' src dist
```

Expected: no persistence/logging of password, ticket, key, reason, or unknown state; no inactive action UI/transport; built output contains only intended public field/header names.

- [ ] **Step 3: Record exact results and commit**

```bash
git add docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/frontend-gates.md
git commit -m "docs: record A14 frontend gates"
```

## Task 18: Perform isolated joint browser acceptance and exact cleanup

**Files:**
- Create: backend `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/joint-acceptance.md`
- Create: backend `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/cleanup-manifest.md`
- Create: backend `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/spec-review.md`
- Create: backend `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/quality-review.md`
- Create: backend `docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete/security-review.md`
- Modify: frontend `docs/agents/validation/joint-acceptance-20260904/acceptance-matrix.json`
- Modify: backend and frontend `progress.md`

- [ ] **Step 1: Start the exact backend/frontend commits against the retained fixture**

Use loopback-only application ports and an untracked test account secret file. Record commit IDs, ports, fixture container IDs, migration version, and process IDs. Do not use `aiportcloud.com` or any production/staging database for this task.

- [ ] **Step 2: Run the joint browser matrix**

With Playwright, prove Root→User, Root→Admin, and authorized Admin→User success; self/Admin/Root/hidden denial; version conflict; ticket expiry/replay; same key same/different payload; duplicate delete; commit unknown followed only by Query; processing Retry-After; pending recovery; and no POST replay after 401/network ambiguity.

After success prove login, Refresh, session, and Gateway token rejection; default list omission; deleted read-only display; username non-reuse; retained physical user row; one management audit with normalized reason; one auth audit without reason; one outbox without reason; one terminal operation.

Reload and close the page during each sensitive state and verify no password, ticket, key, reason, or unknown state remains in browser storage, URL, console, analytics, screenshots, traces, or evidence text.

- [ ] **Step 3: Run final backend/frontend gates on the accepted commits**

Backend:

```bash
go test -p 1 ./... -count=1
go test -race ./internal/actionsecurity ./internal/service ./internal/handler -run 'UserDelete|Action' -count=1
go build ./...
go vet ./...
git diff --check
```

Frontend:

```bash
npm test
npm run build
git diff --check
```

Expected: zero FAIL. Record exact counts and commit identities.

- [ ] **Step 4: Obtain three independent written reviews**

The specification reviewer maps every design section to code/test/evidence and confirms unrelated acceptance rows stayed unchanged. The quality reviewer checks error mapping, transaction boundaries, fixture validity, and maintainability. The security reviewer checks ticket/key/session binding, target visibility, credential invalidation, reason/secret leakage, route inventory, and inactive actions. Each report ends with exact `PASS` or actionable findings; fix findings and rerun affected gates before continuing.

- [ ] **Step 5: Update only bounded acceptance status**

Set A12 and A14 to passing for the `users.delete` slice only, attach exact evidence paths/commits, and preserve every unrelated `BLOCKED_NOT_IMPLEMENTED`, failure, and blocker count. In both `progress.md` files state that production migration/deploy/push remain unauthorized and outbox delivery/recovery worker remains outside this slice.

- [ ] **Step 6: Remove only exact task resources**

Stop application processes by recorded PID. Remove the two recorded container IDs/names and the task network/volume only if their labels and creation IDs match `fixture-lifecycle.md`. Delete the exact `/tmp/a14-user-delete-${A14_FIXTURE_SUFFIX}.env` file, then prove zero residual task containers, networks, volumes, processes, listeners, database names, and temp secrets. Do not run broad Docker prune or wildcard cleanup.

- [ ] **Step 7: Record cleanup and final commits**

Backend:

```bash
git add docs/superpowers/reports/validation/2026-09-05-a14-user-soft-delete progress.md
git diff --cached --name-only
git commit -m "docs: accept A14 user delete slice"
```

Frontend:

```bash
git add docs/agents/validation/joint-acceptance-20260904/acceptance-matrix.json progress.md
git diff --cached --name-only
git commit -m "docs: record user delete joint acceptance"
```

Expected: both worktrees are clean; cleanup manifest proves zero task residual; A12/A14 wording is explicitly limited to `users.delete`; no push or deployment occurred.

## Final completion gate

- [ ] Backend and frontend worktrees are clean at the exact reviewed commits.
- [ ] Only `users.delete` is active; the seven other action descriptors and routes remain inactive.
- [ ] Legacy DELETE is fixed 410 with zero dependency calls.
- [ ] `0001..0005` checksums are unchanged; `0006` fresh apply, existing verify, and down/up pass in MySQL 8.
- [ ] Committed deletion atomically persists user/session/token/policy invalidation, both audits, outbox, and operation terminal state.
- [ ] Commit-unknown is resolved only by scoped Query with the original session/key; sensitive POST is never replayed.
- [ ] Frontend secrets and unknown state are memory-only and absent from logs, storage, URL, traces, and evidence.
- [ ] Full backend test/race/build/vet and frontend test/build gates pass on accepted commits.
- [ ] Independent specification, quality, and security reviews all pass after final fixes.
- [ ] A12/A14 are updated only for the delete slice; every unrelated blocker remains accurate.
- [ ] Exact fixture cleanup reports zero residual resources.
- [ ] No production access, migration, deployment, push, physical delete, restore, money capability, or unrelated action occurred.
