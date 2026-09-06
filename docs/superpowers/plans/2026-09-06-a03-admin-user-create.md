# A03 Admin User Creation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the real `/admin/v2/users` create flow for ordinary users and administrators with permanent username uniqueness, default groups, authorization, optional administrator verification tickets, atomic permissions/audit/outbox writes, idempotent recovery, and a safe Vue form.

**Architecture:** Stage `users.create` and `users.create_admin` descriptor contracts before their consumers exist, while keeping production resolution on the reviewed `users.delete` action. Task 7 atomically activates both create actions only with the complete user-management bundle. Both paths normalize into one immutable account-create intent and execute one transactional consumer. Migration 0007 adds the real business-group relation; the frontend uses a dedicated create workflow and strict DTO mapper before integrating a dialog into `/users`.

**Tech Stack:** Go 1.22, Gin, GORM, MySQL 8, Redis 7, Vue 3, Pinia, Element Plus, Node test runner, Vite.

---

## Worktrees and fixed constraints

- Backend: `/Users/xuzhihao/code/Porsche/.worktrees/admin-public-260903`
- Frontend: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903`
- Design: `docs/superpowers/specs/2026-09-06-a03-admin-user-create-design.md`
- Use TDD for every behavior change. Do not alter migration 0001–0006 or their checksums.
- Do not run production migrations, deploy, push, create real business users, or activate actions before Task 7 constructs and validates their coherent consumer bundle.
- A03 completion may update only A03 from `BLOCKED_NOT_IMPLEMENTED` to `PASS_LIMITED_SCOPE`; all unrelated acceptance rows retain their current state.

## File map

Backend files to create:

- `docs/agents/contracts/admin-user-create-v1.json`: executable endpoint examples and exact error/header contract.
- `internal/migration/sql/0007_business_groups.up.sql` and `.down.sql`: group table, default seed strategy, and `users.group_id` relation.
- `internal/migration/business_groups.go` and `_test.go`: strict schema verifier.
- `internal/models/business_group.go`: stable group status and persistence model.
- `internal/dto/admin_user_create.go` and `_test.go`: strict JSON decoding and redacted formatting.
- `internal/service/action_create_user.go`, `_test.go`, and `_integration_test.go`: validation and transactional consumer.
- `internal/service/action_create_user_bundle.go` and `_test.go`: coherent shared dependency bundle.
- `internal/handler/admin_user_create.go` and `_test.go`: verification/create/query HTTP adapters.
- `internal/handler/admin_groups_read.go` and `_test.go`: active group options for the create form.

Backend files to modify:

- `internal/migration/runner.go`: embed, apply, and verify 0007.
- `internal/models/models.go`: add `GroupID` and managed-create audit event.
- `internal/actionsecurity/types.go`, `intent_users.go`, `intent_users_test.go`, `registry.go`, `registry_test.go`: typed ordinary/admin create descriptors and canonical permission overrides.
- `internal/service/action_operation.go` and tests: allow a descriptor with `RequiresTicket=false` while preserving all ticket checks for verified actions.
- `internal/service/action_verification.go` and tests: validate `users.create_admin` locked intent.
- `internal/service/admin_users_projection.go`, `admin_users_read.go`, and tests: return the persisted group key.
- `internal/app/state.go` and tests, `internal/router/router.go` and tests: construct and register the complete create bundle fail-closed.

Frontend files to create:

- `src/api/admin-user-create.js` and `.test.js`: strict request/response/error adapter.
- `src/api/admin-user-create-state.js` and `.test.js`: idempotency, verification, query, cancellation, and secret lifecycle state machine.
- `src/stores/admin-user-create.js` and `.test.js`: dialog ownership and list reconciliation.
- `src/components/admin/UserCreateDialog.vue` and `.contract.test.js`: accessible create form and administrator permission editor.
- `src/api/admin-groups.js` and `.test.js`: strict active-group directory adapter.

Frontend files to modify:

- `src/api/admin-users.js` and tests: accept a non-null persisted group key.
- `src/stores/admin-users.js`: expose catalog loading for the dialog.
- `src/views/Users.vue`: capability-gated create entry and result reconciliation.
- `src/i18n/messages.js`: Chinese and English create-flow text.
- `docs/agents/contracts/prd-260903-interface-draft.json`: replace the A03 draft with the frozen contract.

### Task 1: Freeze the executable A03 contract

**Files:**
- Create: `docs/agents/contracts/admin-user-create-v1.json`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/docs/agents/contracts/prd-260903-interface-draft.json`
- Test: `internal/dto/admin_user_create_contract_test.go`

- [ ] **Step 1: Write the failing backend contract test**

Create a test that embeds `admin-user-create-v1.json`, parses its user/admin examples as raw JSON with `UseNumber`, and asserts exact method/path/status/header/error fields without depending on DTO code from a later task:

```go
func TestAdminUserCreateContractExamples(t *testing.T) {
    contract := loadAdminUserCreateContract(t)
    if contract.Method != "POST" || contract.Path != "/admin/v2/users" || contract.SuccessStatus != 201 {
        t.Fatalf("unexpected endpoint contract: %#v", contract)
    }
    ordinary := decodeContractObject(t, contract.UserRequestJSON)
    assertExactKeys(t, ordinary, "username", "nickname", "password", "role", "group_guid", "plan_type", "permission_overrides")
    if ordinary["role"] != "user" || ordinary["group_guid"] != nil || ordinary["plan_type"] != "free" { t.Fatalf("ordinary=%#v", ordinary) }
    admin := decodeContractObject(t, contract.AdminRequestJSON)
    if admin["role"] != "admin" || len(admin["permission_overrides"].([]any)) != 2 { t.Fatalf("admin=%#v", admin) }
}
```

- [ ] **Step 2: Run the contract test and verify RED**

Run: `go test ./internal/dto -run '^TestAdminUserCreateContractExamples$' -count=1`

Expected: FAIL because the contract file and loader do not exist.

- [ ] **Step 3: Write the frozen JSON contract**

Include exact body keys, `Idempotency-Key`, role-dependent `X-Action-Ticket`, `201` response, operation query scopes `users.create|users.create_admin`, all error codes from the design, and examples. Set `additionalProperties:false` at every request/response object. In the frontend root draft, set `admin_user_create.status` to `AGREED_FOR_IMPLEMENTATION` and reference this revision without changing other interface entries.

- [ ] **Step 4: Validate JSON and diff scope**

Run:

```bash
python3 -m json.tool docs/agents/contracts/admin-user-create-v1.json >/dev/null
python3 -m json.tool /Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/docs/agents/contracts/prd-260903-interface-draft.json >/dev/null
git diff --check
```

Expected: all commands exit 0; the frontend diff changes only `admin_user_create` and contract revision metadata.

- [ ] **Step 5: Commit both repositories**

Backend: `git add docs/agents/contracts/admin-user-create-v1.json internal/dto/admin_user_create_contract_test.go && git commit -m "test: freeze admin user create contract"`

Frontend: `git add docs/agents/contracts/prd-260903-interface-draft.json && git commit -m "docs: freeze A03 user create contract"`

### Task 2: Add real business-group persistence

**Files:**
- Create: `internal/models/business_group.go`
- Create: `internal/migration/sql/0007_business_groups.up.sql`
- Create: `internal/migration/sql/0007_business_groups.down.sql`
- Create: `internal/migration/business_groups.go`
- Create: `internal/migration/business_groups_test.go`
- Modify: `internal/migration/runner.go`
- Modify: `internal/models/models.go`

- [ ] **Step 1: Write migration RED tests**

Test that `All()` ends at 0007 and the verifier requires the exact group columns/indexes/FKs plus non-null `users.group_id`. Add an isolated MySQL test that migrates existing active and tombstoned users and asserts they reference the one active `default` row.

```go
func TestBusinessGroupsMigrationIsLatest(t *testing.T) {
    migrations, err := All()
    if err != nil { t.Fatal(err) }
    if got := migrations[len(migrations)-1].Version; got != "0007" { t.Fatalf("latest=%s", got) }
}
```

- [ ] **Step 2: Run migration tests and verify RED**

Run: `go test ./internal/migration ./internal/models -run 'BusinessGroup|Migration.*Latest' -count=1`

Expected: failure because 0007 and `BusinessGroup` are absent.

- [ ] **Step 3: Implement models and SQL**

Define stable enums and models:

```go
type BusinessGroupStatus int
const (
    BusinessGroupActive BusinessGroupStatus = 1
    BusinessGroupInactive BusinessGroupStatus = 2
)
type BusinessGroup struct {
    ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
    AuditFields
    Key string `gorm:"column:group_key;size:64;not null" json:"-"`
    DisplayName string `gorm:"size:64;not null" json:"-"`
    Status BusinessGroupStatus `gorm:"type:int;not null" json:"-"`
}
```

Migration 0007 must create `business_groups`, add nullable `users.group_id`, backfill every row, then make it non-null and add a RESTRICT/NO ACTION FK and index. Put the exact marker `-- porsche:seed-default-business-group` between the pre-seed and post-seed statements. Add a 0007-only runner that splits once on that marker, idempotently applies the pre-seed schema, reads the active `default` row under the migration advisory lock, and inserts it with `nextGUID()` plus `nowMillis()` only when absent. It then runs the backfill and final constraints. The embedded SQL, including the marker, remains the ledger checksum source; a crash rerun must detect completed phases instead of allocating a second default row. The verifier must reject unsigned types, missing audit columns, wrong defaults, cascade rules, multiple active defaults, or a nullable relation.

- [ ] **Step 4: Register 0007 and run GREEN tests**

Embed up/down SQL in `runner.go`; route only version 0007 through `applyBusinessGroupsMigration(conn, migration.UpSQL, nextGUID, nowMillis)` and keep 0001–0006 on the existing path. Call `VerifyBusinessGroupsSchema` after apply and from `Verify`, then run:

`go test ./internal/migration ./internal/models -run 'BusinessGroup|Migration.*Latest' -count=1`

Expected: non-fixture tests PASS; MySQL fixture test is an explicit SKIP when `TEST_DATABASE_URL` is absent.

- [ ] **Step 5: Commit**

`git add internal/models internal/migration && git commit -m "feat: add default business group schema"`

### Task 3: Define canonical create intents and candidate descriptors

**Files:**
- Modify: `internal/actionsecurity/types.go`
- Modify: `internal/actionsecurity/intent_users.go`
- Modify: `internal/actionsecurity/intent_users_test.go`
- Modify: `internal/actionsecurity/registry.go`
- Modify: `internal/actionsecurity/registry_test.go`

- [ ] **Step 1: Write canonical-intent RED tests**

Add table tests proving role/default/group/plan/password and sorted permission overrides affect HMAC input, duplicate capabilities and invalid effects fail, inputs are not mutated except owned password bytes are cleared, and the future candidate order is create/create-admin/delete. Production `ActiveActionRegistry()` and `ResolveActiveAction` remain delete-only until Task 7.

```go
func TestCreateAdminIntentCanonicalOverrides(t *testing.T) {
    got, err := descriptorFor(t, ActionUsersCreateAdmin).Encode(CreateAccountIntent{
        Username: "alice", Password: []byte("Str0ng!Pass1"), Role: "admin",
        PlanType: 1, DailyCallLimit: 100,
        Overrides: []PermissionOverrideIntent{{Capability:"users.audit.read", Effect:3}, {Capability:"users.read", Effect:2}},
    })
    if err != nil { t.Fatal(err) }
    assertCanonicalFieldOrderAndSortedOverrides(t, got)
}
```

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/actionsecurity -run 'Create.*Intent|Registry' -count=1`

Expected: compile/assertion failure for missing ordinary action and overrides.

- [ ] **Step 3: Implement exact descriptors**

Append `ActionUsersCreate Action = 9`; do not renumber 1–8. Replace the narrow input with `CreateAccountIntent` containing username, optional nickname, owned password bytes, role, optional group GUID, plan, resolved models/limit, and overrides. Stage these exact future activation candidates, with both create descriptors inactive:

```go
{ActionUsersCreate, "users.create", "users.create", false, false, false, TargetNone, encodeCreateUserAny},
{ActionUsersCreateAdmin, "users.create_admin", "users.create", true, true, false, TargetNone, encodeCreateAdminAny},
{ActionUsersDelete, "users.delete", "users.delete", false, true, true, TargetUser, encodeDeleteUserAny},
```

Keep `ActiveActionRegistry()` and `ResolveActiveAction` on the existing delete-only descriptor. Keep all other descriptors inactive. Encode ordinary/admin role explicitly; canonicalize overrides by capability and reject duplicate/unknown-shaped entries before encoding. Task 7 is the sole activation point after its complete bundle checks pass.

- [ ] **Step 4: Run GREEN tests and secret scan**

Run:

```bash
go test ./internal/actionsecurity -count=1
go test -race ./internal/actionsecurity -count=1
rg -n 'fmt\.(Print|Sprint)|log\.' internal/actionsecurity
```

Expected: tests PASS; scan reveals no new logging of intent/password bytes.

- [ ] **Step 5: Commit**

`git add internal/actionsecurity && git commit -m "feat: define user create action intents"`

### Task 4: Extend the operation ledger for ticketless actions

**Files:**
- Modify: `internal/service/action_operation.go`
- Modify: `internal/service/action_operation_test.go`
- Modify: `internal/service/action_operation_db_test.go`
- Modify: `internal/service/action_execute.go`
- Modify: `internal/service/action_execute_db_test.go`

- [ ] **Step 1: Write ticket-mode RED tests**

Cover new/replay/conflict/cross-session/expiry paths for `RequiresTicket=false`, and assert no verification query, digest, FK, or consumption occurs. Retain tests proving a verified descriptor rejects zero/multiple/malformed tickets.

```go
func TestActionOperationBeginTicketlessDescriptor(t *testing.T) {
    descriptor := testDescriptor
    descriptor.RequiresTicket = false
    identity, view, err := serviceWith(descriptor).Begin(ctx, OperationBegin{
        Action: descriptor.Action, Actor: actor, IdempotencyKeyValues: []string{key}, Intent: intent,
    })
    if err != nil || identity == nil || view == nil { t.Fatalf("begin=%#v %#v %v", identity, view, err) }
    if script.verificationQueries != 0 { t.Fatalf("verification queries=%d", script.verificationQueries) }
}
```

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/service -run 'ActionOperation.*Ticketless|ActionExecute.*Ticketless' -count=1`

Expected: failure because `validOperationDescriptor` and `Begin` currently require a ticket.

- [ ] **Step 3: Implement ticket-mode branching**

Always parse and HMAC the idempotency key and request. When `RequiresTicket` is true, require exactly one ticket and retain the current constant-time binding/consumption path. When false, require `len(TicketValues)==0`, leave `verification_id=NULL`, skip all verification reads and validate replay by actor/action/key/request/session/auth version. Keep authorization fresh for processing/recovery states.

Use one explicit internal value rather than a fake zero ticket digest:

```go
var ticketHex string
if descriptor.RequiresTicket {
    ticketRaw, err := actionsecurity.ParseTicket(in.TicketValues)
    if err != nil { return nil, nil, ErrActionOperationForbidden }
    ticketHex = digestAndClearTicket(s.crypto, ticketRaw)
} else if len(in.TicketValues) != 0 {
    return nil, nil, ErrActionOperationForbidden
}
```

- [ ] **Step 4: Run all operation and race tests**

Run:

```bash
go test ./internal/service -run 'Action(Operation|Execute)' -count=1
go test -race ./internal/service -run 'Action(Operation|Execute)' -count=1
```

Expected: PASS with all prior ticket-required cases unchanged.

- [ ] **Step 5: Commit**

`git add internal/service/action_operation* internal/service/action_execute* && git commit -m "feat: support ticketless admin operations"`

### Task 5: Implement strict create DTOs and normalization

**Files:**
- Create: `internal/dto/admin_user_create.go`
- Create: `internal/dto/admin_user_create_test.go`
- Modify: `internal/dto/admin_user_create_contract_test.go`
- Modify: `internal/service/auth.go`
- Test: `internal/service/auth_registration_test.go`

- [ ] **Step 1: Write DTO and reusable-validator RED tests**

Test exact body keys, omitted defaults, canonical GUID strings, plan/role enums, nickname Unicode maximum, password escape ownership/clearing, override sorting/duplicates, amount/status/internal-field rejection, and redacted `String`/`Format`. Add a separate exact verification decoder for `{action:"users.create_admin",intent:<same create object>,current_password:<owned bytes>}` and prove both password buffers clear independently. Extract reusable managed-creation normalization without enabling public self-registration defaults.

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/dto ./internal/service -run 'AdminUserCreate|NormalizeUsername|ValidatePassword' -count=1`

Expected: missing decoder and normalizer failures.

- [ ] **Step 3: Implement owned request types**

```go
type AdminUserCreateRequest struct {
    Username string
    Nickname *string
    Password []byte
    Role models.UserRole
    GroupGUID *int64
    PlanType models.PlanType
    PermissionOverrides []actionsecurity.PermissionOverrideIntent
}
func (r *AdminUserCreateRequest) ClearSecrets() { clear(r.Password); r.Password = nil }
type AdminUserCreateVerificationRequest struct {
    Intent AdminUserCreateRequest
    CurrentPassword []byte
}
func (r *AdminUserCreateVerificationRequest) ClearSecrets() {
    r.Intent.ClearSecrets(); clear(r.CurrentPassword); r.CurrentPassword = nil
}
```

Use the existing byte-level duplicate-key scanner pattern. Return defaults only after successful exact decoding. Keep `RegisterUsername` behavior stable; share only username/password validators and password hashing helpers.

- [ ] **Step 4: Run DTO, registration, and contract GREEN tests**

Run: `go test ./internal/dto ./internal/service -run 'AdminUserCreate|Registration|NormalizeUsername|ValidatePassword' -count=1`

Expected: PASS; legacy registration tests remain unchanged.

- [ ] **Step 5: Commit**

`git add internal/dto internal/service/auth.go internal/service/auth_registration_test.go && git commit -m "feat: decode managed user creation"`

### Task 6: Build the atomic create consumer

**Files:**
- Create: `internal/service/action_create_user.go`
- Create: `internal/service/action_create_user_test.go`
- Create: `internal/service/action_create_user_integration_test.go`
- Modify: `internal/models/models.go`
- Modify: `internal/service/admin_users_projection.go`

- [ ] **Step 1: Write consumer RED tests**

Use the existing scripted SQL consumer style to lock/check group and username, create the user, optionally create permission head/overrides, write audit and outbox, and finish operation. Inject failure after every write and assert rollback. Test permanent username conflict across tombstones and database unique-key races.

```go
func TestCreateAccountExecutionRollsBackEveryWriteBoundary(t *testing.T) {
    for _, failAt := range []string{"user", "permission_head", "permission_override", "audit", "outbox", "operation"} {
        t.Run(failAt, func(t *testing.T) {
            fixture := newCreateFixture(t, failAt)
            _, err := fixture.execute(validAdminCreateIntent())
            if err == nil { t.Fatal("expected injected failure") }
            fixture.assertNoCommittedCreateSideEffects(t)
        })
    }
}
```

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/service -run '^TestCreateAccount' -count=1`

Expected: compile failure because the consumer is absent.

- [ ] **Step 3: Implement immutable execution and writer**

Create `CreateAccountExecution` that owns the password hash, original safe HTTP request ID/IP metadata, and a copied/sorted intent. Its `Consume` method must revalidate the descriptor/role, lock the active group by GUID or default key, authorize plan/group extras against the already locked actor evaluator, query username without `is_deleted=0`, create the user with a new snowflake GUID, and create version-1 permission rows only for admin. Write `AuthAuditEventRegistered` for the target with `CreatedBy=actor.ID`, then write an `AuditLog` with `UserID=actor.ID`, action `users.create`, resource `users/<target-guid>`, IP, request ID, operation ref, role/group/plan and override capability/effect values. The audit detail records only `password:"set"`; it contains no password value, hash, length, ticket, or key.

- [ ] **Step 4: Run focused and race GREEN tests**

Run:

```bash
go test ./internal/service -run '^TestCreateAccount' -count=1
go test -race ./internal/service -run '^TestCreateAccount' -count=1
```

Expected: PASS; rollback assertions show zero committed partial rows.

- [ ] **Step 5: Commit**

`git add internal/models/models.go internal/service/action_create_user* internal/service/admin_users_projection.go && git commit -m "feat: create managed accounts atomically"`

### Task 7: Validate administrator tickets and construct one coherent bundle

**Files:**
- Modify: `internal/service/action_verification.go`
- Modify: `internal/service/action_verification_test.go`
- Create: `internal/service/action_create_user_bundle.go`
- Create: `internal/service/action_create_user_bundle_test.go`
- Modify: `internal/actionsecurity/registry.go`
- Modify: `internal/actionsecurity/registry_test.go`
- Modify: `internal/app/state.go`
- Modify: `internal/app/state_test.go`

- [ ] **Step 1: Write RED tests for locked admin intent and bundle completeness**

Assert verification accepts only a Root with fresh `users.create`, validates group/plan/overrides under locks, rejects ordinary-create verification, and binds the exact canonical intent. Assert state construction exposes no create route dependency when any service/writer/factory is missing or registry descriptors drift.

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/service ./internal/app -run 'CreateAdminVerification|CreateAccountActions' -count=1`

Expected: verification returns unavailable and bundle symbols are absent.

- [ ] **Step 3: Implement the locked validator and bundle**

Add `validateLockedCreateAdminIntent` to the action-specific switch. Construct one `UserManagementActions` bundle sharing DB, Redis, crypto, resolver, clock, RNG, GUID source, operation service, verification service, outbox writer, delete execution factory, and create execution factory. After its completeness checks cover all three actions, atomically promote the exact production registry order `users.create`, `users.create_admin`, `users.delete`; before that point it remains delete-only. Replace the delete-only state constructor only after this activation and bundle validation are coherent.

- [ ] **Step 4: Run service/app GREEN tests**

Run: `go test ./internal/service ./internal/app -run 'CreateAdminVerification|CreateAccountActions|UserDeleteActions' -count=1`

Expected: PASS; existing delete bundle behavior remains covered during the rename/generalization.

- [ ] **Step 5: Commit**

`git add internal/service/action_verification* internal/service/action_create_user_bundle* internal/service/action_delete_user_bundle* internal/app && git commit -m "feat: wire managed account actions"`

### Task 8: Expose strict HTTP create, verification, and query routes

**Files:**
- Create: `internal/handler/admin_user_create.go`
- Create: `internal/handler/admin_user_create_test.go`
- Modify: `internal/handler/admin_user_actions.go`
- Modify: `internal/handler/admin_user_actions_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/admin_action_inactive_test.go`

- [ ] **Step 1: Write route RED tests**

Cover ordinary create with no ticket, admin verification followed by create, forbidden header combinations, capability/role matrices, exact `201` DTO, `no-store`/request ID on every outcome, replay/query scopes, nil/partial bundle route absence, and legacy `/admin/users` behavior unchanged.

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/handler ./internal/router -run 'AdminUserCreate|AdminActionInactive' -count=1`

Expected: 404 or compile failure for A03 routes.

- [ ] **Step 3: Implement adapters**

Register `POST /admin/v2/users` beside the existing GET collection without duplicate Gin route ownership. Dispatch `POST /admin/v2/action-verifications` by exact action value to delete or create-admin decoders. Query accepts only one exact `scope=users.create` or `scope=users.create_admin`. Build all error responses through the existing `admin_action_error` mapper plus the A03 codes.

For create execution, validate and hash the initial password before `Operation.Begin`, copy the raw bytes into the canonical intent, and clear the decoded/request copies on every return path. `Begin` owns and clears its intent password; `CreateAccountExecution` receives only the password hash plus safe request metadata. For admin verification, the initial password and current actor password remain separate owned buffers and both are cleared after `Issue`.

- [ ] **Step 4: Run handler/router GREEN and contract tests**

Run:

```bash
go test ./internal/dto ./internal/handler ./internal/router -run 'AdminUserCreate|AdminAction|Contract' -count=1
go test -race ./internal/handler ./internal/router -run 'AdminUserCreate|AdminAction' -count=1
```

Expected: PASS and no duplicate-route panic.

- [ ] **Step 5: Commit**

`git add internal/handler internal/router && git commit -m "feat: expose managed user creation API"`

### Task 9: Project real groups through list/detail

**Files:**
- Modify: `internal/service/admin_users_read.go`
- Modify: `internal/service/admin_users_projection.go`
- Modify: `internal/service/admin_users_read_db_test.go`
- Modify: `internal/handler/admin_users_read_test.go`
- Create: `internal/handler/admin_groups_read.go`
- Create: `internal/handler/admin_groups_read_test.go`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/src/api/admin-users.js`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/src/api/admin-users.test.js`
- Create: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/src/api/admin-groups.js`
- Create: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/src/api/admin-groups.test.js`

- [ ] **Step 1: Write backend and frontend RED tests**

Backend must join the active group and return `group:"default"`; missing/deleted/corrupt group fails closed with 503. Add exact `GET /admin/v2/groups?status=active` tests for `groups.read`, key ordering, default presence, DTO allowlist, duplicate/unknown query rejection and dependency failures. Frontend mappers must require a non-empty group key and preserve it.

- [ ] **Step 2: Run RED tests**

Backend: `go test ./internal/service ./internal/handler -run 'AdminUsers.*Group' -count=1`

Frontend: `node --test src/api/admin-users.test.js src/api/admin-groups.test.js --test-name-pattern='group'`

Expected: backend returns null/unsupported and frontend rejects non-null group.

- [ ] **Step 3: Implement one join/projection source**

Select `business_groups.group_key AS group_key` in list/detail, require active/non-deleted membership, and map it into the existing DTO. Update the frontend mapping to:

```js
if (typeof raw.group !== 'string' || !/^[a-z][a-z0-9_-]{0,63}$/.test(raw.group)) throw new Error('invalid_user')
return { ...mapped, group: raw.group }
```

Implement the group directory as exact `{items:[{guid,key,display_name}]}` with string GUIDs and no internal IDs. When `groups.read` is unavailable the frontend must not call it and exposes only an immutable default choice that serializes by omitting `group_guid`.

- [ ] **Step 4: Run GREEN tests**

Run the two commands from Step 2; expected PASS.

- [ ] **Step 5: Commit both repositories**

Backend: `git add internal/service internal/handler && git commit -m "feat: expose user business groups"`

Frontend: `git add src/api/admin-users.js src/api/admin-users.test.js src/api/admin-groups.js src/api/admin-groups.test.js && git commit -m "feat: map persisted user groups"`

### Task 10: Implement the frontend create API and workflow

**Files:**
- Create: `src/api/admin-user-create.js`
- Create: `src/api/admin-user-create.test.js`
- Create: `src/api/admin-user-create-state.js`
- Create: `src/api/admin-user-create-state.test.js`

- [ ] **Step 1: Write API/workflow RED tests**

Test exact request bodies and headers, strict `201` response, ordinary zero-verification path, admin verification→create path, one key per logical attempt, duplicate clicks, ambiguous POST→query, bounded polling, reset/unmount secret clearing, and rejection of unsafe server envelopes.

```js
test('ordinary creation skips verification and reuses one idempotency key for query', async () => {
  const { workflow, calls } = fixture({ create: ambiguousThenQuerySuccess })
  const result = await workflow.start(validUserInput())
  assert.equal(result.state, 'succeeded')
  assert.equal(calls.filter(x => x.method === 'issue').length, 0)
  assert.equal(calls.find(x => x.method === 'create').key, calls.find(x => x.method === 'query').key)
})
```

- [ ] **Step 2: Run and verify RED**

Run: `node --test src/api/admin-user-create.test.js src/api/admin-user-create-state.test.js`

Expected: module-not-found failure.

- [ ] **Step 3: Implement strict adapter and state machine**

Use the established opaque-token validation and error mapper, with create scopes and A03 error codes. Keep password, ticket, and key in closure variables; snapshots may contain only state, operation ref, safe failure code, and created user. The ordinary path calls create directly; the admin path first issues verification using the same normalized request.

- [ ] **Step 4: Run GREEN tests and storage scan**

Run:

```bash
node --test src/api/admin-user-create.test.js src/api/admin-user-create-state.test.js
rg -n 'localStorage|sessionStorage|console\.' src/api/admin-user-create*
```

Expected: tests PASS and scan has no matches.

- [ ] **Step 5: Commit**

`git add src/api/admin-user-create* && git commit -m "feat: add managed user create workflow"`

### Task 11: Add the accessible create dialog and list integration

**Files:**
- Create: `src/stores/admin-user-create.js`
- Create: `src/stores/admin-user-create.test.js`
- Create: `src/components/admin/UserCreateDialog.vue`
- Create: `src/components/admin/UserCreateDialog.contract.test.js`
- Modify: `src/stores/admin-users.js`
- Modify: `src/views/Users.vue`
- Modify: `src/i18n/messages.js`

- [ ] **Step 1: Write store/component RED tests**

Test Admin fixed user role, Root role selector, permission editor only for admin, plan/group capability gating, password confirmation, no amount field/token, focus restoration, double click suppression, stale response ownership, conflict clearing password/ticket/key, success insertion/reload, and unmount cleanup.

- [ ] **Step 2: Run and verify RED**

Run: `node --test src/stores/admin-user-create.test.js src/components/admin/UserCreateDialog.contract.test.js`

Expected: module/component missing failures.

- [ ] **Step 3: Implement the dialog and page entry**

Expose the button only when the current permission projection contains `users.create`. Keep the button absent when projection is missing. Render no amount input. Load active groups only with `groups.read`; otherwise keep the immutable omitted/default selection. Load the authz catalog only for Root/admin creation; submit explicit allow/deny rules and omit inherit. On success close, announce the created GUID/username, reload page 1, and restore focus to the trigger.

- [ ] **Step 4: Run focused and full frontend verification**

Run:

```bash
node --test src/stores/admin-user-create.test.js src/components/admin/UserCreateDialog.contract.test.js
npm test
npm run build
git diff --check
```

Expected: all tests PASS and build exits 0 with only the already documented Rollup warnings.

- [ ] **Step 5: Commit**

`git add src/stores/admin-user-create* src/components/admin/UserCreateDialog* src/stores/admin-users.js src/views/Users.vue src/i18n/messages.js && git commit -m "feat: add admin user creation dialog"`

### Task 12: Run real isolated integration, browser acceptance, and close A03 evidence

**Files:**
- Modify: `internal/service/action_create_user_integration_test.go`
- Create: `docs/superpowers/reports/validation/2026-09-06-a03-admin-user-create/`
- Modify: `progress.md`
- Modify: `feature_list.json`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/docs/agents/validation/joint-acceptance-20260904/acceptance-matrix.json`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/progress.md`
- Modify: `/Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/feature_list.json`

- [ ] **Step 1: Provision task-owned fixtures and run migration 0001–0007**

Use unique container names/labels, random loopback ports, tmpfs/AutoRemove, and a private 0700 temp directory. Record IDs, image digests, ports, migration ledger, and pre-existing unrelated resources without printing credentials. Do not use existing named volumes or production `.env`.

- [ ] **Step 2: Run backend focused, race, and full gates**

Run with fixture URLs scoped only to each process:

```bash
go test ./internal/migration ./internal/actionsecurity ./internal/service ./internal/dto ./internal/handler ./internal/router -run 'BusinessGroup|CreateAccount|AdminUserCreate|ActionOperation' -count=1
go test -race ./internal/actionsecurity ./internal/service ./internal/handler -run 'CreateAccount|AdminUserCreate|ActionOperation' -count=1
go test ./... -count=1
go build ./...
go vet ./...
git diff --check
```

Expected: zero FAIL; every fixture skip must be enumerated and cannot be counted as PASS.

- [ ] **Step 3: Run frontend full gates**

Run:

```bash
npm test
npm run build
git diff --check
```

Expected: zero FAIL; only known build warnings.

- [ ] **Step 4: Run local real FE+BE Chrome acceptance**

Exercise at least: Admin creates default ordinary user; Admin is denied admin creation/non-default plan without capability; Root creates admin with allow/deny overrides; double submit creates one row; username tombstone conflict leaks no details; ambiguous response recovers through operation query; refresh/list/detail shows `group=default`; password/ticket/key absent from browser storage and logs. Save screenshots, request summaries, sanitized DB row counts, audit/outbox/operation terminal records, and cleanup identities.

- [ ] **Step 5: Obtain independent reviews**

Dispatch separate specification, code-quality, and security reviewers against exact backend/frontend commits and evidence. Fix findings through new TDD commits and rerun only the affected gate plus final full suites. Final acceptance requires all three reviewers to report PASS with no unresolved Critical/High findings.

- [ ] **Step 6: Update status without broadening claims**

Set only A03 to `PASS_LIMITED_SCOPE` after evidence passes. Record exact commits, test counts, fixture versions, NOT_RUN production migration/deploy, and unchanged blockers. Parse both feature JSON files and the acceptance matrix.

- [ ] **Step 7: Exact cleanup and final commits**

Verify task container/image/label/port identity before cleanup; remove only task-owned containers and private temp files. Confirm zero task residuals and unrelated resources unchanged. Commit backend and frontend evidence/status separately:

Backend: `git add docs/superpowers/reports progress.md feature_list.json internal/service/action_create_user_integration_test.go && git commit -m "docs: accept A03 user creation slice"`

Frontend: `git add docs/agents/validation/joint-acceptance-20260904/acceptance-matrix.json progress.md feature_list.json && git commit -m "docs: record A03 joint acceptance"`

## Final completion gate

- [ ] Backend and frontend worktrees are clean.
- [ ] Contract JSON and all status JSON parse successfully.
- [ ] Migration ledger is exactly 0001–0007 with immutable prior checksums.
- [ ] Production active registry is exactly `users.create`, `users.create_admin`, and `users.delete`.
- [ ] Ordinary creation performs zero ticket verification; admin creation cannot execute without a valid bound ticket.
- [ ] All atomic rollback, replay, concurrency, secret-lifecycle, and permission tests pass.
- [ ] Real isolated MySQL/Redis and real local browser evidence pass.
- [ ] A03 alone is promoted to `PASS_LIMITED_SCOPE`; unrelated blockers are unchanged.
- [ ] No push, production migration, deployment, or real business account creation occurred.
