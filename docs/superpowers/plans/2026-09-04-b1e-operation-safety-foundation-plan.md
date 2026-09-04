# B1-E Operation Safety Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the internal, fail-closed primitives for five-minute action verification tickets, idempotent admin operations, leases, result queries, and transactional execution while exposing zero new production routes or business effects.

**Architecture:** `internal/actionsecurity` owns strict external-value parsing, purpose-separated cryptography, typed inactive descriptors, and canonical intent encoding. `internal/service` owns Redis limits and MySQL orchestration; persisted rows live in `internal/models`, explicit schema in migration `0005`, and production wiring keeps `ActiveActionRegistry()` empty so the frozen HTTP contracts remain 404. Only `_test.go` code supplies `test.noop`, fixture effects, audit, and outbox writers.

**Tech Stack:** Go 1.22+, GORM/MySQL 8, go-redis/Redis 7 Lua, HKDF-SHA256, HMAC-SHA256, Gin route inventory tests, Go `testing`/`httptest`, Docker only in the later authorized isolated-fixture execution task.

---

## Scope and fixed boundaries

- Implementation baseline is approved design commit `4df7482c2e5eb9c3ba829acf8da8f272978b9096`; preserve existing migration checksums `0001` through `0004`.
- Production `ActiveActionRegistry()` stays empty. `InactiveActionDescriptors()` contains eight frozen descriptors, all inactive. `test.noop` and integer `2147483000` occur only in `_test.go` files.
- Do not register `POST /admin/v2/action-verifications` or `GET /admin/v2/operations`; both paths, plus future admin write routes, remain 404 for authenticated and unauthenticated requests.
- Internal services are the only validation surface in this slice. There is no production consumer, business effect, outbox/recovery worker, frontend production adapter, deployment, push, model, chat, SSE, upstream call, or production migration.
- User approval for development does not authorize production access, destructive database work, broad cleanup, or Docker cleanup outside the exact disposable fixture identities created in Task 12.
- Completion may contribute only `A14=limited_subscope`; A14 remains `BLOCKED_NOT_IMPLEMENTED` and the joint-acceptance blocker count remains 18.

## File structure and responsibilities

| Path | Responsibility |
| --- | --- |
| `internal/actionsecurity/value.go` | Exact parsers/generators for `ik_`, `av_`, and `op_` values. |
| `internal/actionsecurity/crypto.go` | Root-key copy, four HKDF keys, fixed-purpose HMAC, constant-time compare. |
| `internal/actionsecurity/types.go` | Stable action/target enums, descriptors, actor/request/result contracts. |
| `internal/actionsecurity/registry.go` | Eight inactive descriptors and empty production active registry. |
| `internal/actionsecurity/encode.go` | Fixed-tag binary writer and typed canonical encoders. |
| `internal/actionsecurity/intent_users.go` | Six user-action DTO validators/encoders. |
| `internal/actionsecurity/intent_public.go` | Two public-content DTO validators/encoders. |
| `internal/models/admin_operation.go` | Persistence enums and exact GORM mappings for the two new tables. |
| `internal/migration/sql/0005_admin_operation_safety.*.sql` | Explicit reversible MySQL 8 schema. |
| `internal/migration/admin_operation_safety.go` | Information-schema verifier for columns, indexes, FKs, and engines. |
| `internal/service/action_security_redis.go` | Atomic Lua limit checks with opaque HMAC Redis keys. |
| `internal/service/action_identity.go` | Shared actor/session lock and fresh authorization validation. |
| `internal/service/action_verification.go` | Internal Issue primitive and reissue invalidation. |
| `internal/service/action_operation.go` | Begin, Query, expiry, and recovery-state transitions. |
| `internal/service/action_execute.go` | Transactional callback boundary and commit-unknown result. |
| `internal/service/action_primitives.go` | Typed audit/outbox interfaces used inside the caller transaction. |
| `internal/app/state.go` | Optional internal action-security dependencies; no route exposure. |
| `internal/router/router_test.go` | Route-count and frozen-path 404 proof. |
| `internal/dto/admin_action_contract_test.go` | Future JSON/error-envelope fixture contract only. |
| `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/` | Redacted gates, fixture identities, independent reviews, and cleanup evidence. |

### Task 1: Freeze the baseline and ownership gate

**Files:**
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/baseline.md`
- Test: `init.sh`
- Test: all existing Go packages

- [ ] **Step 1: Record the exact clean baseline without changing files**

Run:

```bash
pwd
git rev-parse HEAD
git status --short
git log -5 --oneline
sha256sum docs/superpowers/specs/2026-09-04-b1e-operation-safety-foundation-design.md
```

Expected: repository root is the B1-E worktree, HEAD is `4df7482c...`, and status is empty. Stop and report any pre-existing delta; never reset or clean it.

- [ ] **Step 2: Run repository initialization with task variables absent**

Run:

```bash
env -u TEST_DATABASE_URL -u TEST_REDIS_URL -u DATABASE_URL -u REDIS_URL -u APP_ENV -u RUN_START_COMMAND ./init.sh
```

Expected: PASS and no tracked-file changes. If the sandbox denies a loopback bind, preserve the exact permission error and rerun the same command only through the approved sandbox escalation; do not call it a product failure.

- [ ] **Step 3: Run the pre-change full gate**

Run:

```bash
env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -p 1 ./... -count=1
go build ./...
go vet ./...
git diff --check
```

Expected: all existing tests PASS or existing fixture tests explicitly SKIP because `TEST_*` is absent; build/vet/diff PASS.

- [ ] **Step 4: Write the redacted baseline record**

Write `baseline.md` with only commit, command, exit code, package counts, skip names, sandbox classification, and `git status --short`; do not include environment values.

- [ ] **Step 5: Commit only the baseline record**

```bash
git add docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/baseline.md
git diff --cached --name-only
git commit -m "docs: record B1-E implementation baseline"
```

Expected: staged list contains one file.

### Task 2: Validate and derive the action-security root key

**Files:**
- Create: `internal/actionsecurity/value.go`
- Create: `internal/actionsecurity/value_test.go`
- Create: `internal/actionsecurity/crypto.go`
- Create: `internal/actionsecurity/crypto_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/app/state.go`
- Test: `internal/app/state_test.go`

- [ ] **Step 1: Write RED configuration and parser tests**

Add table tests that call this exact API:

```go
func ParseRootKey(raw string) ([]byte, KeyErrorReason)
func ParseIdempotencyKey(values []string) ([32]byte, error)
func ParseTicket(values []string) ([32]byte, error)
func ParsePublicRef(raw string) ([32]byte, error)
func NewTicket(random io.Reader) (string, [32]byte, error)
func NewPublicRef(random io.Reader) (string, error)
```

Cover exact 43-character raw base64url root keys; reject padding, whitespace, Unicode, bad length, and bad encoding. For external values cover 46 total ASCII characters, duplicate header values, commas, case sensitivity, and no trimming. Add config cases for production/staging missing and malformed values, development/test omission, declared invalid values in every environment, and reuse against `AUTH_HMAC_KEY` or `JWT_SECRET_KEY` bytes. Assert errors contain only `missing`, `invalid_length`, `invalid_encoding`, or `key_reuse`.

- [ ] **Step 2: Run RED tests**

Run:

```bash
go test ./internal/actionsecurity ./internal/config ./internal/app -run 'RootKey|ExternalValue|ActionSecurity' -count=1
```

Expected: FAIL because package/functions/field do not exist.

- [ ] **Step 3: Implement exact value parsing and key configuration**

Use this shape; no `strings.TrimSpace` is allowed on external values:

```go
type KeyErrorReason string
const (
    KeyMissing KeyErrorReason = "missing"
    KeyInvalidLength KeyErrorReason = "invalid_length"
    KeyInvalidEncoding KeyErrorReason = "invalid_encoding"
    KeyReuse KeyErrorReason = "key_reuse"
)

type Settings struct {
    // existing fields...
    ActionSecurityHMACKey []byte
}
```

`ParseRootKey` checks `len(raw)==43`, `base64.RawURLEncoding.DecodeString`, and decoded length 32. `config.Load` requires it for `production` and `staging`, accepts absence for development/test, rejects any declared invalid value, compares the decoded bytes with `[]byte(AuthHMACKey)` and `[]byte(JWTSecretKey)` using `subtle.ConstantTimeCompare`, and stores a defensive copy. `NewState` calls `actionsecurity.NewCrypto` only when key bytes are present; action-service construction remains impossible when absent.

- [ ] **Step 4: Implement purpose-separated crypto**

```go
type Crypto struct {
    ticket, intent, idempotency, lease [32]byte
}
func NewCrypto(root []byte) (*Crypto, error)
func (c *Crypto) TicketDigest(raw [32]byte) [32]byte
func (c *Crypto) IntentDigest(encoded []byte) [32]byte
func (c *Crypto) IdempotencyDigest(raw [32]byte) [32]byte
func (c *Crypto) LeaseOwnerDigest(raw [32]byte) [32]byte
func (c *Crypto) RateDigest(purpose string, payload []byte) ([32]byte, error)
func EqualDigest(a, b [32]byte) bool
```

Derive four 32-byte keys with HKDF-SHA256 and exact infos from the spec. The unexported HMAC helper writes fixed `purpose`, one NUL byte, then payload. `RateDigest` accepts only the four fixed rate purpose constants via a switch. Copy and zero temporary root/derived buffers after initialization.

- [ ] **Step 5: Add golden and separation assertions**

Generate expected hex inside the test from fixed root/payload literals using an independent `hkdf.New` + `hmac.New` reference helper. Assert all four digests differ, purpose-with-NUL differs from concatenation, invalid rate purpose fails, and `EqualDigest` accepts equal/rejects unequal values.

- [ ] **Step 6: Run GREEN and focused race**

```bash
go test ./internal/actionsecurity ./internal/config ./internal/app -run 'RootKey|ExternalValue|Crypto|ActionSecurity' -count=1
go test -race ./internal/actionsecurity ./internal/config -run 'RootKey|ExternalValue|Crypto' -count=1
```

Expected: PASS.

- [ ] **Step 7: Exact-stage commit**

```bash
git add internal/actionsecurity/value.go internal/actionsecurity/value_test.go internal/actionsecurity/crypto.go internal/actionsecurity/crypto_test.go internal/config/config.go internal/config/config_test.go internal/app/state.go internal/app/state_test.go
git diff --cached --name-only
git commit -m "feat: add action security key isolation"
```

### Task 3: Add typed inactive descriptors and canonical intent encoders

**Files:**
- Create: `internal/actionsecurity/types.go`
- Create: `internal/actionsecurity/registry.go`
- Create: `internal/actionsecurity/registry_test.go`
- Create: `internal/actionsecurity/encode.go`
- Create: `internal/actionsecurity/encode_test.go`
- Create: `internal/actionsecurity/intent_users.go`
- Create: `internal/actionsecurity/intent_users_test.go`
- Create: `internal/actionsecurity/intent_public.go`
- Create: `internal/actionsecurity/intent_public_test.go`

- [ ] **Step 1: Write RED registry contract tests**

Use stable explicit enums:

```go
type Action int
const (
    ActionUsersCreateAdmin Action = 1
    ActionUsersResetPassword Action = 2
    ActionUsersPromote Action = 3
    ActionUsersDemote Action = 4
    ActionUsersPermissionsWrite Action = 5
    ActionUsersDelete Action = 6
    ActionPublicContentPublish Action = 7
    ActionPublicContentRollback Action = 8
)
type TargetKind int
const (TargetNone TargetKind = 1; TargetUser TargetKind = 2; TargetPublicContent TargetKind = 3)
type Descriptor struct { Action Action; Name, Capability string; RootOnly, RequiresTicket, Active bool; TargetKind TargetKind; Encode func(any) ([]byte, error) }
func InactiveActionDescriptors() []Descriptor
func ActiveActionRegistry() []Descriptor
func ResolveActiveAction(action Action) (Descriptor, bool)
```

Assert exact integer/name/capability/root/ticket/target table, eight inactive entries, zero active entries, copied slices, unique names/integers, and rejection of the wrong DTO type.

- [ ] **Step 2: Run registry RED**

```bash
go test ./internal/actionsecurity -run 'Registry|Descriptor' -count=1
```

Expected: FAIL with undefined types/functions.

- [ ] **Step 3: Implement descriptors without consumers or handlers**

Return new slice copies from both functions. Bind each descriptor directly to its typed encoder through a type assertion switch; never accept an action string or `map[string]any`. `ResolveActiveAction` searches only `ActiveActionRegistry`, so it always returns false in B1-E. Service production constructors receive this resolver; an unexported constructor may accept a resolver function so same-package `_test.go` code can inject only `test.noop`. No production constructor accepts `InactiveActionDescriptors()` or an arbitrary descriptor slice.

- [ ] **Step 4: Write RED canonical encoding tests**

Define concrete DTOs, including:

```go
type CreateAdminIntent struct { Username string; Nickname *string; Password []byte; GroupGUID *int64; PlanType int; AllowedModels []string; DailyCallLimit int }
type ResetPasswordIntent struct { TargetGUID int64; NewPassword []byte; Reason string }
type RoleIntent struct { TargetGUID int64; ExpectedAuthVersion int; Reason string }
type PermissionOverrideIntent struct { Capability string; Effect int }
type PermissionsWriteIntent struct { TargetGUID int64; ExpectedPermissionsVersion int64; CatalogVersion int; Overrides []PermissionOverrideIntent }
type DeleteUserIntent struct { TargetGUID int64; ExpectedAuthVersion int; Reason string }
type PublishIntent struct { ContentType string; VersionGUID, ExpectedBaseVersion int64; Reason string }
type RollbackIntent struct { ContentType string; VersionGUID, ExpectedCurrentVersion int64; Reason string }
```

Golden tests must assert tag order, explicit null tags, uint32 big-endian lengths, fixed-width big-endian integers, arrays with count, sorted/deduplicated `AllowedModels`, capability-sorted unique overrides, fixed roles, validation before encoding, no implicit trim/case/NFKC, and password byte slices zeroed after `Encode` returns.

- [ ] **Step 5: Run encoder RED**

```bash
go test ./internal/actionsecurity -run 'Encode|Intent|PasswordZero' -count=1
```

Expected: FAIL with undefined DTOs/encoders.

- [ ] **Step 6: Implement the binary writer and eight typed encoders**

Use an unexported writer with methods `fieldString`, `fieldNullableString`, `fieldInt64`, `fieldInt32`, `fieldBytes`, and `fieldArray`. Each writes one-byte field tag, one-byte type tag, uint32 length, and bytes. DTO validators reject non-canonical GUIDs/versions, invalid effect values, duplicates, and forbidden empty fields. `CreateAdminIntent` writes fixed role `admin`; role intents write fixed `admin` or `user`. Password buffers are passed by value as slice headers and cleared with `clear()` via `defer` after HMAC input is formed.

- [ ] **Step 7: Run GREEN and static test-action absence check**

```bash
go test ./internal/actionsecurity -count=1
go test -race ./internal/actionsecurity -count=1
if rg -n 'test\.noop|2147483000' internal --glob '!**/*_test.go'; then exit 1; fi
```

Expected: tests PASS and `rg` prints nothing.

- [ ] **Step 8: Exact-stage commit**

```bash
git add internal/actionsecurity/types.go internal/actionsecurity/registry.go internal/actionsecurity/registry_test.go internal/actionsecurity/encode.go internal/actionsecurity/encode_test.go internal/actionsecurity/intent_users.go internal/actionsecurity/intent_users_test.go internal/actionsecurity/intent_public.go internal/actionsecurity/intent_public_test.go
git diff --cached --name-only
git commit -m "feat: define inactive admin action contracts"
```

### Task 4: Define persistence models and enum contracts

**Files:**
- Create: `internal/models/admin_operation.go`
- Create: `internal/models/admin_operation_contract_test.go`

- [ ] **Step 1: Write RED model contract tests**

Assert exact `TableName()` values, GORM column names/types, stable integer/string mappings, legal transitions, derived verification status, and that `AdminOperation` has no plaintext ticket/idempotency/password/SID field.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/models -run 'AdminActionVerification|AdminOperation' -count=1
```

Expected: FAIL because models are undefined.

- [ ] **Step 3: Implement exact model shapes**

```go
type AdminOperationState int
const (OperationProcessing AdminOperationState = 1; OperationSucceeded AdminOperationState = 2; OperationFailed AdminOperationState = 3; OperationPendingRecovery AdminOperationState = 4; OperationExpired AdminOperationState = 5)
type AdminOperationFailure int
const (FailureActionRejected AdminOperationFailure = 1; FailureTargetVersionConflict AdminOperationFailure = 2; FailurePolicyVersionConflict AdminOperationFailure = 3; FailureTargetStateConflict AdminOperationFailure = 4; FailureConsumerValidation AdminOperationFailure = 5)
type AdminResultKind int
const (ResultNone AdminResultKind = 1; ResultUser AdminResultKind = 2; ResultPublicContent AdminResultKind = 3)

type AdminActionVerification struct { ID int64; AuditFields; ActorUserID int64; ActorAuthVersion int; SessionID int64; Action int; TargetKind int; TargetGUID *int64; IntentHMAC string; TicketHMAC string; ExpiresAt int64; ConsumedAt *int64 }
func (AdminActionVerification) TableName() string { return "admin_action_verifications" }

type AdminOperation struct { ID int64; AuditFields; ActorUserID int64; ActorAuthVersion int; SessionID int64; Action int; IdempotencyKeyHMAC string; RequestHMAC string; VerificationID *int64; State AdminOperationState; PublicRef string; LeaseOwnerHMAC *string; LeaseExpiresAt *int64; FinishedAt *int64; QueryExpiresAt int64; ErrorCode *AdminOperationFailure; ResultKind *AdminResultKind; ResultGUID *int64; ResultHTTPStatus *int }
func (AdminOperation) TableName() string { return "admin_operations" }
```

Use explicit `gorm:"column:...;type:..."` tags; all enum values are explicit constants, never `iota`.

- [ ] **Step 4: Run GREEN and commit**

```bash
go test ./internal/models -run 'AdminActionVerification|AdminOperation' -count=1
git add internal/models/admin_operation.go internal/models/admin_operation_contract_test.go
git diff --cached --name-only
git commit -m "feat: model admin operation safety records"
```

### Task 5: Add explicit migration 0005 and schema verification

**Files:**
- Create: `internal/migration/sql/0005_admin_operation_safety.up.sql`
- Create: `internal/migration/sql/0005_admin_operation_safety.down.sql`
- Create: `internal/migration/admin_operation_safety.go`
- Create: `internal/migration/admin_operation_safety_test.go`
- Modify: `internal/migration/runner.go`
- Modify: `internal/migration/runner_test.go`

- [ ] **Step 1: Write RED embed/ledger/schema tests**

Assert `All()` returns immutable versions `0001..0005`, old four checksums are unchanged, `0005` up contains both exact table/index/FK names and no `TIMESTAMP`, `DATETIME`, native `ENUM`, or physical-delete trigger. Assert down drops `admin_operations` before `admin_action_verifications`.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/migration -run 'AdminOperationSafety|MigrationSequence|VerifyApplied' -count=1
```

Expected: FAIL because version `0005` and verifier are absent.

- [ ] **Step 3: Write exact up/down SQL**

Create both tables with every column, type, nullability, default, unique/index order, FK name, `ENGINE=InnoDB`, `DEFAULT CHARSET=utf8mb4`, and `COLLATE=utf8mb4_unicode_ci` specified in the approved design. Include `CHECK` constraints for target nullability, state values, result coherence, and nullable lease/terminal columns where MySQL 8 enforces them. Down contains only:

```sql
DROP TABLE IF EXISTS admin_operations;
DROP TABLE IF EXISTS admin_action_verifications;
```

- [ ] **Step 4: Embed 0005 and call the verifier**

Add `//go:embed` variables and `{Version:"0005", ...}` to `All()`. Implement:

```go
func VerifyAdminOperationSafetySchema(ctx context.Context, db *gorm.DB) error
```

It queries `information_schema.COLUMNS`, `STATISTICS`, `KEY_COLUMN_USAGE`, and `TABLES` for the active schema and compares exact ordered contracts. Invoke it after applying/finding `0005` and from `Verify`.

- [ ] **Step 5: Run GREEN and migration regression**

```bash
go test ./internal/migration -count=1
go test ./internal/models ./internal/db -count=1
```

Expected: PASS without a database; real schema remains Task 12.

- [ ] **Step 6: Exact-stage commit**

```bash
git add internal/migration/sql/0005_admin_operation_safety.up.sql internal/migration/sql/0005_admin_operation_safety.down.sql internal/migration/admin_operation_safety.go internal/migration/admin_operation_safety_test.go internal/migration/runner.go internal/migration/runner_test.go
git diff --cached --name-only
git commit -m "feat: add admin operation safety schema"
```

### Task 6: Implement fail-closed Redis limits

**Files:**
- Create: `internal/service/action_security_redis.go`
- Create: `internal/service/action_security_redis_test.go`

- [ ] **Step 1: Write RED miniredis/unit contracts**

Define:

```go
type ActionSecurityRedis struct { client redis.UniversalClient; crypto *actionsecurity.Crypto }
type RetryAfterError struct { Seconds int }
func NewActionSecurityRedis(client redis.UniversalClient, crypto *actionsecurity.Crypto) (*ActionSecurityRedis, error)
func (r *ActionSecurityRedis) ReserveVerification(ctx context.Context, actorID, sessionID int64, trustedIP string) error
func (r *ActionSecurityRedis) ReserveBegin(ctx context.Context, sessionID int64) error
```

Tests assert Issue limits actor 5/900s, IP 20/900s, session 10/3600s; Begin 60/60s; first create sets TTL, later calls do not slide it; all dimensions update in one Eval; earliest exceeded remaining TTL is rounded up and at least 1; missing client, timeout, malformed script response, and Redis error map to a single unavailable error. Inspect Redis keys and prove they contain no actor/session/IP text.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/service -run 'ActionSecurityRedis' -count=1
```

Expected: FAIL with undefined service.

- [ ] **Step 3: Implement one atomic Lua window script**

Use one `EVAL` call with the three Issue keys or one Begin key. For every attempt the script increments all relevant counters atomically, sets TTL only when a key is first created, reads remaining PTTL without extending it, then returns `{allowed,retry_ms}` based on the post-increment counts. Thus rejected attempts cannot bypass another dimension and later MySQL failure is never refunded. Create keys from the rate HMAC lowercase hex only, with prefix `porsche:action:rate:v1:`. Convert the earliest exceeded remaining `retry_ms` with `(ms+999)/1000`, minimum one.

- [ ] **Step 4: Run GREEN and race**

```bash
go test ./internal/service -run 'ActionSecurityRedis' -count=1
go test -race ./internal/service -run 'ActionSecurityRedis' -count=1
```

Expected: PASS.

- [ ] **Step 5: Exact-stage commit**

```bash
git add internal/service/action_security_redis.go internal/service/action_security_redis_test.go
git diff --cached --name-only
git commit -m "feat: enforce admin action Redis limits"
```

### Task 7: Implement internal verification-ticket Issue

**Files:**
- Create: `internal/service/action_identity.go`
- Create: `internal/service/action_identity_test.go`
- Create: `internal/service/action_verification.go`
- Create: `internal/service/action_verification_test.go`
- Modify: `internal/app/state.go`
- Test: `internal/app/state_test.go`

- [ ] **Step 1: Write RED identity and Issue tests**

Use these contracts:

```go
type ActionActor struct { UserID, UserGUID int64; AuthVersion int; SessionSID string; SessionVersion int }
type VerificationIssue struct { Action actionsecurity.Action; Actor ActionActor; TargetGUID *int64; Intent any; CurrentPassword []byte; TrustedIP string }
type IssuedVerification struct { Ticket string; ExpiresAt int64 }
type ActionVerificationService struct { db *gorm.DB; redis *ActionSecurityRedis; authRedis *AuthRedis; crypto *actionsecurity.Crypto; resolve func(actionsecurity.Action) (actionsecurity.Descriptor, bool); clock persistence.Clock; random io.Reader; nextGUID func() int64 }
func NewActionVerificationService(...) (*ActionVerificationService, error)
func (s *ActionVerificationService) Issue(ctx context.Context, in VerificationIssue) (*IssuedVerification, error)
```

Tests prove Redis runs before MySQL; actor then session are `FOR UPDATE`; actor/session/JWT/auth version/role/capability/target are fresh; Redis revocation errors fail closed; password failure is fixed 403; ticket TTL is exactly 300000ms; actor/session/action/target/intent/ticket hashes only are stored; audit actors equal actor ID; raw inputs do not reach SQL arguments/logs. Reissue invalidates every unconsumed active same session/action/target binding in the same transaction without physical delete.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/service -run 'ActionIdentity|ActionVerificationIssue' -count=1
```

Expected: FAIL with undefined services.

- [ ] **Step 3: Implement shared fresh identity locking**

Add unexported `lockActionIdentity(tx, actor, descriptor, targetGUID, now)` that selects actor by `id`, session by exact `sid`, verifies ownership/current versions/status/role, calls existing permission snapshot/evaluator using locked facts, then checks Redis revocation outside MySQL only after the required Redis limiter and before mutation. Map hidden and credential mismatches to fixed service errors without echoing identifiers.

- [ ] **Step 4: Implement Issue transaction**

Resolve the action through the production active resolver before encoding; B1-E production resolution therefore always rejects and cannot reach MySQL. The same-package test constructor injects a resolver containing only `_test.go` `test.noop`. Then encode intent, calculate digest, zero password bytes after `security.VerifyPassword`, generate ticket from injected random, hash it, generate snowflake GUID, invalidate old binding with conditional soft-delete/update audit, and insert the verification. Return plaintext ticket only from the result object and never retain it on the service.

- [ ] **Step 5: Run GREEN and constructor tests**

```bash
go test ./internal/service ./internal/app -run 'ActionIdentity|ActionVerificationIssue|ActionSecurityConstructor' -count=1
go test -race ./internal/service -run 'ActionVerificationIssue' -count=1
```

Expected: PASS; app construction with no development/test key leaves action services nil, while invalid partial dependencies fail.

- [ ] **Step 6: Exact-stage commit**

```bash
git add internal/service/action_identity.go internal/service/action_identity_test.go internal/service/action_verification.go internal/service/action_verification_test.go internal/app/state.go internal/app/state_test.go
git diff --cached --name-only
git commit -m "feat: issue internal admin action verifications"
```

### Task 8: Implement idempotent Begin, Query, leases, and expiry

**Files:**
- Create: `internal/service/action_operation.go`
- Create: `internal/service/action_operation_test.go`
- Create: `internal/service/action_operation_db_test.go`

- [ ] **Step 1: Write RED state-machine and parser tests**

Define:

```go
type OperationBegin struct { Action actionsecurity.Action; Actor ActionActor; IdempotencyKeyValues, TicketValues []string; Intent any }
type OperationIdentity struct { ID int64; PublicRef string; LeaseOwner [32]byte }
type OperationView struct { PublicRef, Scope, Status string; FinishedAt *int64; FailureCode *string; RetryAfterSeconds int }
type ActionOperationService struct { db *gorm.DB; limiter *ActionSecurityRedis; authRedis *AuthRedis; crypto *actionsecurity.Crypto; resolve func(actionsecurity.Action) (actionsecurity.Descriptor, bool); clock persistence.Clock; random io.Reader; nextGUID func() int64 }
func (s *ActionOperationService) Begin(ctx context.Context, in OperationBegin) (*OperationIdentity, *OperationView, error)
func (s *ActionOperationService) Query(ctx context.Context, action actionsecurity.Action, actor ActionActor, keyValues []string) (*OperationView, error)
func (s *ActionOperationService) MarkPendingRecovery(ctx context.Context, id int64) error
```

Tests cover strict parse before Redis/MySQL, limiter before lookup, create with 30s lease/30d provisional query expiry, same actor/action/key+same payload/session returning existing state without consumer, payload conflict 409, cross-session 409, ticket mismatch/session mismatch fixed 403, one ticket with different keys, expired 410, other-session/unknown/hidden Query 404, and Query not consuming Begin quota.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/service -run 'ActionOperation(Begin|Query|State|Lease|Expiry)' -count=1
```

Expected: FAIL with undefined operation service.

- [ ] **Step 3: Implement Begin with the exact lock order**

Outside the transaction resolve the action through the injected active resolver, parse values/intent, and reserve Begin. The production resolver remains empty; only the `_test.go` constructor injects `test.noop`. Inside, lock actor, session, then query operation by `(actor_user_id, action, idempotency_key_hmac)` without filtering `is_deleted`. For a new row generate `op_` public ref and random lease owner, persist only HMACs, lock verification, verify `expires_at > now`, and reserve it with the unique nullable FK. Created/updated actors are the current actor ID.

- [ ] **Step 4: Implement Query and legal transitions**

Query resolves the action only through the active resolver, locks fresh actor/session then exact operation, requires same session, and hides all mismatches as 404. Terminal expiry condition is `now >= query_expires_at`; atomically write state 5, `is_deleted=1`, clear lease/error/result fields, preserve both HMACs and public ref, and return 410. `MarkPendingRecovery` only succeeds when `now > lease_expires_at+60000`, clears lease, never invokes a callback, and cannot transition a terminal/expired row.

- [ ] **Step 5: Add deterministic clock edges**

Assert ticket at `expires_at==now` rejects; lease at `+30000` is still processing; at `lease+60000` remains processing; only one millisecond later moves to pending recovery. Assert finished+2592000000 boundary expires and tombstones prevent key reuse.

- [ ] **Step 6: Run GREEN and focused race**

```bash
go test ./internal/service -run 'ActionOperation(Begin|Query|State|Lease|Expiry)' -count=1
go test -race ./internal/service -run 'ActionOperation(Begin|Query|State|Lease|Expiry)' -count=1
```

Expected: PASS for unit/mock gates; true MySQL row-lock race remains Task 12.

- [ ] **Step 7: Exact-stage commit**

```bash
git add internal/service/action_operation.go internal/service/action_operation_test.go internal/service/action_operation_db_test.go
git diff --cached --name-only
git commit -m "feat: begin and query idempotent admin operations"
```

### Task 9: Implement transactional Execute with a test-only consumer

**Files:**
- Create: `internal/service/action_primitives.go`
- Create: `internal/service/action_execute.go`
- Create: `internal/service/action_execute_test.go`
- Create: `internal/service/action_execute_db_test.go`
- Create: `internal/service/action_test_consumer_test.go`

- [ ] **Step 1: Write RED callback and outcome contracts**

```go
type TerminalOutcome struct { Failure *models.AdminOperationFailure; ResultKind models.AdminResultKind; ResultGUID *int64; HTTPStatus int }
type TransactionalActionConsumer interface { Execute(ctx context.Context, tx *gorm.DB, operation models.AdminOperation) (TerminalOutcome, error) }
type TransactionalAuditWriter interface { Write(ctx context.Context, tx *gorm.DB, event ActionAuditEvent) error }
type TransactionalOutboxWriter interface { Write(ctx context.Context, tx *gorm.DB, event ActionOutboxEvent) error }
type CommitUnknownError struct { PublicRef string; Cause error }
func (s *ActionOperationService) Execute(ctx context.Context, identity OperationIdentity, consumer TransactionalActionConsumer, audit TransactionalAuditWriter, outbox TransactionalOutboxWriter) (*OperationView, error)
```

Write `_test.go` declarations for action integer `2147483000`, name `test.noop`, a typed test intent encoder, fixture effect/audit/outbox tables, and a callback that writes all three only through the supplied `tx`. Give the test-only descriptor `TargetUser`, capability `users.delete`, `RequiresTicket=true`, and `Active=true` inside the test resolver so real fixture tests can exercise fresh target visibility/permission checks without activating any frozen descriptor or real consumer.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/service -run 'ActionExecute|TransactionalConsumer|CommitUnknown' -count=1
```

Expected: FAIL because Execute/primitives are undefined.

- [ ] **Step 3: Implement Execute transaction and terminal outcomes**

Open a new owned GORM transaction. Lock actor, session, operation, verification in order; compare lease owner HMAC constant-time; revalidate state/auth/intent/session; conditionally consume verification with `consumed_at IS NULL AND expires_at > now AND is_deleted=0`, setting `consumed_at=now`, `is_deleted=1`, and update audit fields; invoke consumer, audit, and outbox in that same transaction. Known typed rejection rolls back callback savepoint, then records failed operation + rejection audit/outbox in the same outer transaction. Infrastructure error rolls back everything. Success writes finished/query expiry/result and clears lease.

- [ ] **Step 4: Make commit unknown explicit**

Use an injectable transaction runner in tests so a commit can return an error after the callback. Return `CommitUnknownError{PublicRef: ...}` mapped later to 503; do not open a second transaction, update state outside the transaction, or retry callback. A subsequent Query is the only confirmation route.

- [ ] **Step 5: Add the complete fault matrix**

Inject failure at actor lock, session lock, operation lock, verification lock, ticket consume, fixture effect, audit, outbox, terminal update, and commit return. Assert no partial effect, no false terminal success/failure, no callback replay, and no secrets in errors. Add succeeded and typed failed atomic cases.

- [ ] **Step 6: Run GREEN, race, and production-source scan**

```bash
go test ./internal/service -run 'ActionExecute|TransactionalConsumer|CommitUnknown' -count=1
go test -race ./internal/service -run 'ActionExecute|TransactionalConsumer|CommitUnknown' -count=1
if rg -n 'test\.noop|2147483000|fixture_action_effects|fixture_action_outbox' internal --glob '!**/*_test.go'; then exit 1; fi
```

Expected: PASS and no production-source matches.

- [ ] **Step 7: Exact-stage commit**

```bash
git add internal/service/action_primitives.go internal/service/action_execute.go internal/service/action_execute_test.go internal/service/action_execute_db_test.go internal/service/action_test_consumer_test.go
git diff --cached --name-only
git commit -m "feat: execute admin action transaction primitives"
```

### Task 10: Prove production route and registry remain inactive

**Files:**
- Modify: `internal/router/router_test.go`
- Create: `internal/router/admin_action_inactive_test.go`
- Test: `internal/actionsecurity/registry_test.go`

- [ ] **Step 1: Write a route-inventory regression test**

Capture the exact existing route method/path multiset before B1-E in a golden slice. Assert `router.New(state).Routes()` equals it and contains neither `/admin/v2/action-verifications` nor `/admin/v2/operations`.

- [ ] **Step 2: Add authenticated and unauthenticated 404 requests**

Issue POST and GET requests to both frozen paths, plus `/admin/v2/users`, `/admin/v2/users/123/actions`, and representative public-content write paths. Assert 404 before and after attaching syntactically valid auth headers; do not assert 401/422 because no route exists.

- [ ] **Step 3: Run route and build-boundary gates**

```bash
go test ./internal/router ./internal/actionsecurity -run 'RouteInventory|AdminActionInactive|Registry' -count=1
go test ./internal/router -count=1
go list -deps ./cmd/server >/dev/null
if rg -n 'test\.noop|2147483000' --glob '!**/*_test.go' .; then exit 1; fi
```

Expected: all requests 404, route count unchanged, active registry length zero, and source scan empty.

- [ ] **Step 4: Exact-stage commit**

```bash
git add internal/router/router_test.go internal/router/admin_action_inactive_test.go internal/actionsecurity/registry_test.go
git diff --cached --name-only
git commit -m "test: prove admin action routes remain inactive"
```

### Task 11: Freeze future HTTP and frontend-facing contracts as tests only

**Files:**
- Create: `internal/dto/admin_action_contract_test.go`
- Create: `docs/agents/contracts/admin-action-future-contract.json`
- Test: `internal/router/admin_action_inactive_test.go`

- [ ] **Step 1: Add exact JSON fixture tests**

In `_test.go`, define test-local structs and marshal the exact future Issue request/201 response, operation processing response, and error envelope from the design. Assert `current_password` spelling, decimal GUID strings where applicable, nullable fields, allowed statuses, fixed failure-code strings, `operation_ref` appearing only for commit unknown, and Retry-After ranges.

- [ ] **Step 2: Add a machine-readable frozen contract document**

Write JSON containing `status:"inactive_contract"`, exact paths, methods, headers, JSON examples, replay limits (`POST:0`, exact Query GET after refresh:1), storage prohibitions, and `production_observation:404`. It must not claim any handler/client exists.

- [ ] **Step 3: Prove no frontend or production adapter changed**

```bash
go test ./internal/dto ./internal/router -run 'AdminActionFutureContract|AdminActionInactive' -count=1
git diff --name-only 4df7482c2e5eb9c3ba829acf8da8f272978b9096..HEAD
rg -n 'Register.*ActionVerification|/admin/v2/operations' internal --glob '!**/*_test.go'
```

Expected: tests PASS; the backend diff contains no path outside this repository and no frontend adapter file; production registration search prints nothing. Do not run a writer command in Porsche-Web.

- [ ] **Step 4: Exact-stage commit**

```bash
git add internal/dto/admin_action_contract_test.go docs/agents/contracts/admin-action-future-contract.json
git diff --cached --name-only
git commit -m "docs: freeze future admin action contracts"
```

### Task 12: Validate with fresh isolated MySQL 8 and Redis 7 fixtures

**Files:**
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/fixture-plan.md`
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/fixture-identity.json`
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/migration-ledger.txt`
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/fixture-results.json`
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/secret-scan.txt`
- Test: `internal/migration/admin_operation_safety_test.go`
- Test: `internal/service/action_operation_db_test.go`
- Test: `internal/service/action_execute_db_test.go`

- [ ] **Step 1: Write the fixture lifecycle before creating resources**

Record unique task label, random container names, private `mktemp -d` path with mode 0700/files 0600, loopback random host ports, MySQL 8 and Redis 7 immutable image IDs, `--rm`, `--read-only`, `--tmpfs` data directories, and only required read-only mounts. State that only full IDs created here may be stopped; forbid `prune`, globs, named volumes, `docker compose down -v`, and `docker volume rm`.

- [ ] **Step 2: Preflight exact identities and isolation**

Before tests, inspect full IDs, name, task label, image digest, AutoRemove, mounts, tmpfs, loopback bindings, and absence of named volumes. Save redacted structured fields only. Generate MySQL password, Redis password if configured, action root key, actor password, session material, keys, and tickets inside the private directory; never place them in shell arguments, stdout, or Git. The future executor uses task-specific variables and the image-provided password-file interface:

```bash
B1E_PRIVATE_DIR="$(mktemp -d /private/tmp/porsche-b1e-260904.XXXXXX)"
chmod 700 "$B1E_PRIVATE_DIR"
umask 077
openssl rand -base64 24 >"$B1E_PRIVATE_DIR/mysql-root-password"
openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n' >"$B1E_PRIVATE_DIR/action-root-key"
docker image inspect mysql:8.0 redis:7-alpine
docker run -d --rm --read-only --tmpfs /var/lib/mysql:rw,noexec,nosuid,size=768m --mount "type=bind,src=$B1E_PRIVATE_DIR/mysql-root-password,dst=/run/secrets/mysql-root-password,readonly" -e MYSQL_ROOT_PASSWORD_FILE=/run/secrets/mysql-root-password -e MYSQL_DATABASE=porsche_test -p 127.0.0.1::3306 --label codex.task=porsche-b1e-260904 --name porsche-b1e-260904-mysql mysql:8.0
docker run -d --rm --read-only --tmpfs /data:rw,noexec,nosuid,size=128m -p 127.0.0.1::6379 --label codex.task=porsche-b1e-260904 --name porsche-b1e-260904-redis redis:7-alpine redis-server --save '' --appendonly no
```

Expected: each `docker run` prints one full container ID. Copy each exact ID into a mode-0600 identity file, then inspect it with `docker inspect <full-id>` and reject any identity/mount/port/label mismatch before migrations. If the chosen images do not support the declared read-only/tmpfs contract, stop only those exact IDs and report the fixture blocked; do not weaken isolation.

- [ ] **Step 3: Run migrations up, down/up, and schema verification**

With `DATABASE_URL` pointing only to the fixture, run:

```bash
go run ./cmd/migrate up
go run ./cmd/migrate status
go test ./internal/migration -run 'AdminOperationSafetyRealMySQL' -count=1
```

Use an isolated test helper to apply `0005 down`, confirm both new tables absent and `0001..0004` intact, then reapply `0005` and call `migration.Verify`. Expected: ordered `0001..0005`, exact checksums, schema/FKs/indexes/types PASS. Production never runs down.

- [ ] **Step 4: Run real concurrency, clocks, Redis, and fault tests**

Inject only `TEST_DATABASE_URL`, `TEST_REDIS_URL`, and the generated test action key. Execute:

```bash
go test ./internal/service -run 'Action(SecurityRedis|Verification|Operation|Execute).*Real' -count=1
go test -race ./internal/service -run 'Action(OperationConcurrency|ExecuteConcurrency).*Real' -count=1
```

Required evidence: 300s ticket equality edge; 30s lease and 60s grace edges; 30d expiry/tombstone; same-key concurrent Begin; payload conflict; ticket reuse; cross-session; refresh-same-session; auth/policy/target changes; hidden facts; actor/IP/session Issue windows; Begin 60/min; non-sliding TTL; Redis total failure 503; at least two goroutines using real MySQL row locks; every fault point from Task 9; commit unknown with zero replay.

- [ ] **Step 5: Run serial full gates against the fixture**

```bash
go test -p 1 ./... -count=1 -json
go test -race ./internal/actionsecurity ./internal/service -run 'Action' -count=1
go build ./...
go vet ./...
git diff --check
```

Expected: PASS; no model/chat/SSE/upstream request is made.

- [ ] **Step 6: Perform redacted secret scans**

Scan Git diff, generated reports, and captured stdout for exact generated secrets plus generic connection/password/token/cookie patterns. Store only pattern names, match counts, and PASS/FAIL; never store the searched values or matched lines. Expected: zero matches.

- [ ] **Step 7: Independent QA on the retained exact fixture**

Give a fresh reviewer only commit SHA, public fixture identity, and private-path location. Reviewer independently repeats migration verify, real race/concurrency/clock/fault cases, registry/routes scans, and secret-count checks without altering production code. Save signed verdict and command hashes; keep resources alive for cleanup review.

- [ ] **Step 8: Exact-stage evidence commit**

```bash
git add docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/fixture-plan.md docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/fixture-identity.json docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/migration-ledger.txt docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/fixture-results.json docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/secret-scan.txt
git diff --cached --name-only
git commit -m "test: validate B1-E isolated fixtures"
```

### Task 13: Update limited-subslice status and evidence manifests

**Files:**
- Modify: `progress.md`
- Modify: `feature_list.json`
- Modify: `session-handoff.md`
- Create: `docs/superpowers/reports/2026-09-04-b1e-operation-safety-foundation.md`
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/manifest.json`

- [ ] **Step 1: Write RED tracking assertions**

Use a short test script under `/private/tmp` that parses `feature_list.json` and asserts: B1-E is `limited_subscope`; A14 is `BLOCKED_NOT_IMPLEMENTED`; active production consumers are zero; joint blockers equal 18; no frontend/production/deployment claim exists. Expected before edits: FAIL because B1-E evidence is not recorded.

- [ ] **Step 2: Update only truthful bounded status**

Record internal primitives, exact test/fixture/review commit SHAs, PASS/SKIP/FAIL counts, and remaining exclusions. Keep every one of the 18 blocker entries unchanged. State explicitly: no real business action, audit delivery, outbox worker, recovery worker, frontend adapter, deployment, production migration, or production acceptance.

- [ ] **Step 3: Build a hash manifest**

List each evidence file with SHA-256, producing commit, redaction status, and reviewer verdict. Do not hash or list private secret files. Run the tracking assertion again; expected PASS.

- [ ] **Step 4: Exact-stage commit**

```bash
git add progress.md feature_list.json session-handoff.md docs/superpowers/reports/2026-09-04-b1e-operation-safety-foundation.md docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/manifest.json
git diff --cached --name-only
git commit -m "docs: record B1-E limited subscope validation"
```

### Task 14: Perform exact fixture cleanup and independent cleanup review

**Files:**
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/cleanup.json`
- Modify: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/manifest.json`

- [ ] **Step 1: Reconfirm identities before stopping anything**

Compare full container IDs, exact names, task label, image IDs, mounts, AutoRemove, and loopback ports with `fixture-identity.json`. Stop immediately on mismatch. Confirm test helper has dropped only fixture-only effect/audit/outbox tables; retain migrated schema until containers stop.

- [ ] **Step 2: Stop only exact full IDs and remove only the exact private path**

Stop the MySQL and Redis containers by full ID. Let `--rm` perform container removal. Delete the single recorded private directory by exact path after verifying owner, mode, non-symlink status, and task marker. Never use a wildcard, parent-directory recursion, prune, or volume command.

- [ ] **Step 3: Verify zero residuals**

Check exact IDs/names, task-label query, ports/listeners, test processes, private path, and Docker volumes created by this task. Expected: all zero/absent; unrelated resources remain untouched.

- [ ] **Step 4: Independent cleanup review**

A fresh reviewer repeats identity/name/label/port/process/path checks and reviews the executed command transcript for forbidden broad commands. Record only redacted counts and verdict.

- [ ] **Step 5: Commit cleanup evidence**

```bash
git add docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/cleanup.json docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/manifest.json
git diff --cached --name-only
git commit -m "test: record B1-E exact fixture cleanup"
```

### Task 15: Run final gates and obtain PM, implementation, and security verdicts

**Files:**
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/pm-spec-review.md`
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/implementation-review.md`
- Create: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/security-review.md`
- Modify: `docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/manifest.json`

- [ ] **Step 1: Run final clean-tree gates**

```bash
env -u TEST_DATABASE_URL -u TEST_REDIS_URL go test -p 1 ./... -count=1
go test -race ./internal/actionsecurity ./internal/service -run 'Action' -count=1
go build ./...
go vet ./...
git diff --check
git status --short
```

Expected: PASS, fixture-only real tests explicitly SKIP without `TEST_*`, and no uncommitted changes before review artifacts.

- [ ] **Step 2: Run invariant scans**

```bash
test "$(rg -l 'test\.noop|2147483000' internal --glob '!**/*_test.go' | wc -l | tr -d ' ')" = "0"
test "$(rg -n 'POST\("/admin/v2/action-verifications|GET\("/admin/v2/operations' internal --glob '!**/*_test.go' | wc -l | tr -d ' ')" = "0"
rg -n 'ActiveActionRegistry|InactiveActionDescriptors|ResolveActiveAction' internal/actionsecurity
```

Expected: first two checks succeed, registry declarations are present, placeholder scan has no matches.

- [ ] **Step 3: PM specification review**

Reviewer maps every approved design section to code/tests/evidence, confirms production active registry zero, frozen paths 404, A14 limited only, 18 blockers unchanged, and no production/FE/business-effect claim. Any gap returns `SPEC_FAIL` with exact path/line and blocks completion.

- [ ] **Step 4: Independent implementation and security reviews**

Implementation reviewer checks types/signatures, lock order, transaction ownership, error mapping, enum stability, migration standards, race/fault evidence, and full final diff. Security reviewer checks key separation, constant-time comparisons, secret lifetime, logs/reports, Redis opacity/fail-closed behavior, cross-session oracles, commit unknown, no replay, and cleanup. Both must review final HEAD and return explicit PASS.

- [ ] **Step 5: Commit only review artifacts and refreshed manifest**

```bash
git add docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/pm-spec-review.md docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/implementation-review.md docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/security-review.md docs/superpowers/reports/validation/2026-09-04-b1e-operation-safety-foundation/manifest.json
git diff --cached --name-only
git commit -m "docs: finalize B1-E operation safety evidence"
git status --short
```

Expected: staged paths exactly match the four review/manifest files; final worktree and index are clean. Do not push, deploy, run production migration, or activate a consumer.

## Completion interpretation

B1-E is complete only when all fifteen tasks, fresh-fixture checks, independent reviews, and exact cleanup pass at the same final HEAD. The resulting claim is limited to reusable internal safety primitives. Eight business descriptors remain inactive, the two frozen HTTP path families remain 404, Porsche-Web has zero production changes, A14 remains blocked, and all 18 joint-acceptance blockers remain open until separately designed consumers and frontend flows are implemented and accepted.
