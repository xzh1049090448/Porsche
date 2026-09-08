# A08 Managed-User Roles and Permissions Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Root-only promotion, demotion and permission-override replacement with verified idempotent execution, immediate credential invalidation and durable policy history.

**Architecture:** Promote and demote extend the verified `/actions` lifecycle. Permission replacement keeps its PATCH resource while entering the same verification, operation and recovery engine. Transactional consumers lock target and policy state, establish Redis denial before SQL mutation and store stable role/auth/policy results.

**Tech Stack:** Go 1.22, Gin, GORM, MySQL 8, Redis 7, `internal/actionsecurity`, existing admin-operation framework.

---

### Task 1: Freeze the executable A08 contract

**Files:**
- Create: `docs/agents/contracts/admin-user-roles-permissions-v1.json`
- Create: `internal/dto/admin_user_roles_permissions_contract_test.go`

- [ ] **Step 1: Write the failing contract test**

```go
func TestA08ContractHasExactScopesAndStableResult(t *testing.T) {
    contract := readA08Contract(t)
    assertA08Scope(t, contract, "promote", "users.promote")
    assertA08Scope(t, contract, "demote", "users.demote")
    assertA08Scope(t, contract, "permissions_write", "users.permissions.write")
    assertA08StableFields(t, contract, []string{"operation_ref", "target_guid", "resulting_auth_version", "resulting_permissions_version", "resulting_role"})
}
```

- [ ] **Step 2: Run `go test ./internal/dto -run '^TestA08Contract' -count=1`**

Expected: FAIL because the contract fixture and helpers do not exist.

- [ ] **Step 3: Add the exact JSON fixture**

Freeze separate Issue/Execute/Query entries for the three scopes, strict request keys, exact stable results, status/failure allowlists, headers, memory-only secrets and revision `2026-09-09-a08-v1`. Record the paired frontend file and byte-equality requirement.

- [ ] **Step 4: Rerun the focused test and commit**

Expected: PASS.

```bash
git add docs/agents/contracts/admin-user-roles-permissions-v1.json internal/dto/admin_user_roles_permissions_contract_test.go
git commit -m "docs: freeze A08 role permission contract"
```

### Task 2: Bind complete canonical intents before activation

**Files:**
- Modify: `internal/actionsecurity/intent_users.go`
- Modify: `internal/actionsecurity/intent_users_test.go`
- Modify: `internal/actionsecurity/registry.go`
- Modify: `internal/actionsecurity/registry_test.go`

- [ ] **Step 1: Add failing golden tests**

```go
func TestPromoteIntentBindsPolicy(t *testing.T) {
    in := PromoteIntent{TargetGUID: 9, ExpectedAuthVersion: 3, ExpectedPermissionsVersion: 0, CatalogVersion: 1,
        Overrides: []PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}}, Reason: "rotation"}
    first := mustEncodeAction(t, ActionUsersPromote, in)
    in.Overrides[0].Effect = 3
    second := mustEncodeAction(t, ActionUsersPromote, in)
    if bytes.Equal(first, second) { t.Fatal("policy was not bound") }
}
```

Add cases for both expected versions, reason, catalog version, input-order canonicalization, duplicate/inherit/unknown/ungrantable rejection, promote-only zero policy version and demote's absence of overrides.

- [ ] **Step 2: Run `go test ./internal/actionsecurity -run 'Test(Promote|Demote|PermissionsWrite)' -count=1`**

Expected: FAIL against the pre-A08 encoders.

- [ ] **Step 3: Implement the approved typed intents**

```go
type PromoteIntent struct { TargetGUID int64; ExpectedAuthVersion int; ExpectedPermissionsVersion int64; CatalogVersion int; Overrides []PermissionOverrideIntent; Reason string }
type DemoteIntent struct { TargetGUID int64; ExpectedAuthVersion int; ExpectedPermissionsVersion int64; CatalogVersion int; Reason string }
type PermissionsWriteIntent struct { TargetGUID int64; ExpectedAuthVersion int; ExpectedPermissionsVersion int64; CatalogVersion int; Overrides []PermissionOverrideIntent; Reason string }
```

Clone and sort override input before encoding. Keep action integers 3, 4 and 5 unchanged and inactive.

- [ ] **Step 4: Run `go test ./internal/actionsecurity -count=1` and commit**

Expected: PASS while the active registry still contains only the four pre-A08 actions.

```bash
git add internal/actionsecurity
git commit -m "feat(authz): bind A08 action intents"
```

### Task 3: Decode exact DTOs and reject legacy bypasses

**Files:**
- Create: `internal/dto/admin_user_roles_permissions.go`
- Create: `internal/dto/admin_user_roles_permissions_test.go`
- Modify: `internal/handler/admin_user_update_decode.go`
- Modify: `internal/handler/admin_user_update_test.go`

- [ ] **Step 1: Add failing decoder tests**

Cover valid Issue/Execute/PATCH bodies plus 4096-byte limit, invalid UTF-8, escaped/case-folded/duplicate keys, trailing JSON, GUID/version bounds, reason normalization, duplicate/inherit/unknown/Root-only overrides.

```go
func TestDecodePermissionWriteRejectsRootOnlyGrant(t *testing.T) {
    body := `{"expected_auth_version":7,"expected_permissions_version":3,"catalog_version":1,"overrides":[{"capability":"users.promote","effect":"allow"}],"reason":"case"}`
    if _, err := DecodePermissionWrite(strings.NewReader(body)); !errors.Is(err, ErrA08InvalidBody) { t.Fatalf("got %v", err) }
}
```

- [ ] **Step 2: Run `go test ./internal/dto -run 'TestDecode(Promote|Demote|Permission)' -count=1`**

Expected: FAIL because the decoders are absent.

- [ ] **Step 3: Implement wire DTOs and stable response**

```go
type RolePermissionResult struct {
    OperationRef string `json:"operation_ref"`
    TargetGUID string `json:"target_guid"`
    ResultingAuthVersion int `json:"resulting_auth_version"`
    ResultingPermissionsVersion int64 `json:"resulting_permissions_version"`
    ResultingRole string `json:"resulting_role"`
}
```

Convert only validated allow/deny strings to persistence effects. Reject role, versions, catalog, overrides, action and reason on generic legacy/update paths before calling a service.

- [ ] **Step 4: Run `go test ./internal/dto ./internal/handler -run 'Test.*(A08|Role|Permission|Legacy)' -count=1` and commit**

Expected: PASS.

```bash
git add internal/dto/admin_user_roles_permissions.go internal/dto/admin_user_roles_permissions_test.go internal/handler/admin_user_update_decode.go internal/handler/admin_user_update_test.go
git commit -m "feat(api): decode A08 role permission requests"
```

### Task 4: Store stable role and policy results

**Files:**
- Create: `internal/migration/sql/0012_admin_operation_role_permission_results.up.sql`
- Create: `internal/migration/sql/0012_admin_operation_role_permission_results.down.sql`
- Create: `internal/migration/admin_operation_role_permission_results.go`
- Create: `internal/migration/admin_operation_role_permission_results_test.go`
- Modify: `internal/migration/runner.go`
- Modify: `internal/models/admin_operation.go`
- Modify: `internal/service/action_primitives.go`
- Modify: `internal/service/action_operation.go`
- Modify: `internal/service/action_operation_db_test.go`

- [ ] **Step 1: Add failing migration and outcome-invariant tests**

```go
func TestA08OutcomeRequiresCompleteUserResult(t *testing.T) {
    role, policy, guid, auth := models.UserRoleAdmin, int64(4), int64(9), 8
    outcome := TerminalOutcome{ResultKind: models.ResultUser, ResultGUID: &guid, ResultAuthVersion: &auth,
        ResultPermissionsVersion: &policy, ResultRole: &role, HTTPStatus: 200}
    if err := validateTerminalOutcome(outcome); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Run `go test ./internal/migration ./internal/models ./internal/service -run 'Test.*(0012|A08Outcome|StableRole)' -count=1`**

Expected: FAIL for missing columns and fields.

- [ ] **Step 3: Add `result_permissions_version BIGINT NULL` and `result_role INT NULL`**

Extend schema verification, down migration, model, `TerminalOutcome`, `OperationView`, persistence, expiry clearing and stable replay. API maps the stored integer role to `user`/`admin`. Require complete A08 tuples without invalidating prior action result shapes.

- [ ] **Step 4: Rerun the migration/model/service tests and commit**

Expected: PASS, including up/down/schema and replay-after-later-mutation cases.

```bash
git add internal/migration internal/models/admin_operation.go internal/service/action_primitives.go internal/service/action_operation.go internal/service/action_operation_db_test.go
git commit -m "feat(db): store A08 stable operation results"
```

### Task 5: Apply policy and role transitions transactionally

**Files:**
- Create: `internal/service/action_roles_permissions.go`
- Create: `internal/service/action_roles_permissions_test.go`
- Create: `internal/service/action_roles_permissions_integration_test.go`
- Modify: `internal/service/permission_snapshot.go`
- Modify: `internal/service/permission_snapshot_test.go`

- [ ] **Step 1: Add failing transition tests**

Test no-head promotion to version 1, historical empty-head promotion, canonical replacement, empty-head demotion, later promotion without stale revival, no-op/stale/overflow conflict, Root/self/deleted/wrong-role rejection, concurrency and each Redis/SQL rollback point.

```go
func TestDemoteLeavesReadableEmptyPolicyHead(t *testing.T) {
    result := runDemoteFixture(t, adminWithAllowOverride())
    if result.Role != models.UserRoleUser || result.RuleCount != 0 || result.PolicyVersion != 2 { t.Fatalf("result=%+v", result) }
    assertNoActiveOverrides(t, result.UserID)
    assertHistoricalOverrideRetained(t, result.UserID)
}
```

- [ ] **Step 2: Run `go test ./internal/service -run 'Test(Promote|Demote|PermissionWrite)' -count=1`**

Expected: FAIL because no consumer exists.

- [ ] **Step 3: Implement the consumer family**

Lock actor/session through the operation engine, then target, target sessions, policy head and active rules. Validate fresh role/status/auth/policy/catalog state, establish Redis denial, revoke sessions, tombstone active rules, insert sorted allow/deny rules, keep a nondeleted policy head, increment policy/auth versions once and return the complete stable result.

- [ ] **Step 4: Write atomic audit/outbox facts**

Write N session-revoked events, one role/policy event, one redacted management audit and one outbox entry. Include allowlisted before/after public values; exclude passwords, ticket/key/HMAC/SID and internal IDs.

- [ ] **Step 5: Rerun focused, concurrency and failure tests and commit**

Expected: PASS; Redis failure leaves zero SQL facts, while later SQL rollback may leave only a safe denial marker.

```bash
git add internal/service/action_roles_permissions.go internal/service/action_roles_permissions_test.go internal/service/action_roles_permissions_integration_test.go internal/service/permission_snapshot.go internal/service/permission_snapshot_test.go
git commit -m "feat(authz): apply A08 role permission transitions"
```

### Task 6: Wire routes, recovery and all-or-nothing activation

**Files:**
- Create: `internal/handler/admin_user_roles_permissions.go`
- Create: `internal/handler/admin_user_roles_permissions_test.go`
- Modify: `internal/service/action_create_user_bundle.go`
- Modify: `internal/service/action_create_user_bundle_test.go`
- Modify: `internal/handler/admin_user_actions.go`
- Modify: `internal/handler/admin_user_actions_contract_runtime_test.go`
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_test.go`
- Modify: `internal/router/admin_action_inactive_test.go`
- Modify: `internal/app/state.go`
- Modify: `internal/app/state_test.go`

- [ ] **Step 1: Add failing bundle/route/runtime-contract tests**

Assert exact POST action dispatch, verified PATCH headers, three literal Query scopes, no alternate routes, no partial bundle construction and seven active descriptors in canonical order.

- [ ] **Step 2: Run `go test ./internal/handler ./internal/router ./internal/app -run 'Test.*A08' -count=1`**

Expected: FAIL because factories and routes are absent.

- [ ] **Step 3: Extend the complete bundle**

```go
type UserManagementActions struct {
    // preserve existing members
    NewPromoteExecution func(actionsecurity.PromoteIntent) (*RolePermissionExecution, error)
    NewDemoteExecution func(actionsecurity.DemoteIntent) (*RolePermissionExecution, error)
    NewPermissionsWriteExecution func(actionsecurity.PermissionsWriteIntent) (*RolePermissionExecution, error)
}
```

Fail construction if any A08 factory/writer is missing. Issue and Execute/PATCH must produce identical canonical HMAC input.

- [ ] **Step 4: Implement handlers and stable recovery**

Require exact headers and query strings. Replay and Query return stored target/role/auth/policy values and never read a current target row. Map only allowlisted errors; preserve no-store and body/header-equal request IDs.

- [ ] **Step 5: Activate actions 3, 4 and 5 only after complete wiring**

Update registry and inactive-route assertions without renumbering actions.

- [ ] **Step 6: Rerun handler/router/app/service tests and commit**

Expected: PASS.

```bash
git add internal/service/action_create_user_bundle.go internal/service/action_create_user_bundle_test.go internal/handler internal/router internal/app internal/actionsecurity/registry.go internal/actionsecurity/registry_test.go
git commit -m "feat(admin): activate A08 role permission actions"
```

### Task 7: Prove immediate credential invalidation

**Files:**
- Create: `internal/service/action_roles_permissions_runtime_test.go`
- Modify: `internal/service/action_roles_permissions_integration_test.go`
- Modify: `internal/handler/admin_user_actions_contract_runtime_test.go`

- [ ] **Step 1: Add failing real-runtime tests**

Mint Access, Refresh and Gateway Key credentials before each transition. Prove old sessions fail immediately; Gateway Key requests reload current owner role/policy; a new deny applies to the next request; demotion has no effective override; later baseline promotion does not revive history.

- [ ] **Step 2: Run `go test ./internal/service ./internal/handler -run 'TestA08Runtime' -count=1` against isolated MySQL/Redis**

Expected: FAIL at any runtime path that retains stale authorization.

- [ ] **Step 3: Route affected requests through fresh owner/security/policy checks**

Do not delete Gateway Keys solely because of A08 changes. Make the smallest runtime correction exposed by the failing test.

- [ ] **Step 4: Rerun the real-runtime test and commit**

Expected: PASS with database and Redis evidence.

```bash
git add internal/service/action_roles_permissions_runtime_test.go internal/service/action_roles_permissions_integration_test.go internal/handler/admin_user_actions_contract_runtime_test.go
git commit -m "test(authz): prove A08 immediate invalidation"
```

### Task 8: Run gates and archive bounded evidence

**Files:**
- Create: `docs/superpowers/reports/validation/2026-09-09-a08-managed-user-roles-permissions/backend/manifest.json`
- Create: `docs/superpowers/reports/validation/2026-09-09-a08-managed-user-roles-permissions/backend/report.md`
- Modify: `feature_list.json`
- Modify: `progress.md`

- [ ] **Step 1: Run focused, race and full gates**

```bash
go test ./internal/actionsecurity ./internal/dto ./internal/migration ./internal/models ./internal/service ./internal/handler ./internal/router ./internal/app -run 'Test.*(A08|Promote|Demote|Permission)' -count=1
go test -race ./internal/actionsecurity ./internal/service ./internal/handler ./internal/router -run 'Test.*(A08|Promote|Demote|Permission)' -count=1
go test ./... -count=1
go test -race ./... -count=1
go build ./...
go vet ./...
git diff --check
```

Expected: all selected tests pass. List declared opt-in fixture skips verbatim instead of counting them as passes.

- [ ] **Step 2: Run real MySQL 8/Redis 7 acceptance**

Record exact image IDs, migration ledger through 0012, credential lifecycle, concurrency, rollback and exact fixture cleanup. Do not use broad Docker cleanup.

- [ ] **Step 3: Obtain independent specification, implementation and security reviews**

Resolve every material finding and rerun affected focused and full gates.

- [ ] **Step 4: Update only the bounded status and commit**

Set A08 to `PASS_LIMITED_SCOPE` only with complete evidence. Preserve production migration/deployment/acceptance, unrelated blockers and external PM confirmation as observed.

```bash
git add docs/superpowers/reports/validation/2026-09-09-a08-managed-user-roles-permissions feature_list.json progress.md
git commit -m "docs: record A08 backend verification"
```
