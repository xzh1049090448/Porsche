# Platform Generation Control (BE04) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Activate authenticated generation GET/cancel endpoints with cancellation-before-claim tombstones, owner-bound Redis leases, receipt-backed completed results, and bounded restart convergence.

**Architecture:** Extend the BE02 Redis registry with two strictly validated record variants and lease/cancellation CAS operations, then put a small control service in front of BE03 receipt reconciliation. Keep HTTP DTO projection, process-local cancellation callbacks, and the bounded background scanner in separate files; assemble and close them through `app.State` without activating BE05/BE06 SSE routes.

**Tech Stack:** Go 1.22, Gin, go-redis v9 with Lua CAS, GORM, MySQL 8 migration ledger `0001`-`0011`, standard-library context/crypto/sync/time, existing Porsche test fixtures.

---

## File structure

- Modify `internal/service/platform_generation_store.go`: record variants, claim result, lease metadata validation, secure claim behavior.
- Create `internal/service/platform_generation_store_control.go`: tombstone/cancel/renew/expire/scan Redis operations and Lua scripts.
- Modify `internal/service/platform_generation_store_test.go`: strict decoder, claim compatibility, TTL, and real Redis races.
- Create `internal/service/platform_generation_cancel_registry.go`: process-local callback ownership.
- Create `internal/service/platform_generation_cancel_registry_test.go`: registration, invocation, removal, and races.
- Create `internal/service/platform_generation_control.go`: status/cancel orchestration and public projection values.
- Create `internal/service/platform_generation_control_test.go`: pure, Redis, and MySQL/Redis status/cancel coverage.
- Create `internal/service/platform_generation_converger.go`: bounded cursor worker and lifecycle.
- Create `internal/service/platform_generation_converger_test.go`: pass limits, restart recovery, multi-instance CAS, and shutdown.
- Create `internal/handler/platform_generation.go`: HTTP routing helpers, exact response/error envelopes, cache and retry headers.
- Create `internal/handler/platform_generation_test.go`: authenticated endpoint contract tests using a fake controller.
- Modify `internal/handler/platform.go`: register the two generation routes in the existing authenticated group.
- Modify `internal/app/state.go`: assemble controller/registry/worker and provide idempotent `Close`.
- Modify `internal/app/state_test.go`: dependency assembly, partial cleanup, worker shutdown, and double-close tests.
- Modify `internal/router/router_test.go`: replace the obsolete “generation routes absent” assertion with active control-route and still-guarded SSE assertions.
- Modify `cmd/server/main.go`: defer `State.Close` after successful construction.
- Modify `progress.md` and `feature_list.json`: record BE04 evidence without marking the overall BE01-BE06 feature complete.
- Create `docs/superpowers/reports/2026-09-09-platform-generation-control.md`: verification evidence and remaining BE05/BE06 boundaries.

### Task 1: Introduce strict tombstone and lease-aware record shapes

**Files:**
- Modify: `internal/service/platform_generation_store.go`
- Modify: `internal/service/platform_generation_store_test.go`
- Test: `internal/service/platform_generation_persistence_test.go`
- Test: `internal/service/platform_generation_reconcile_test.go`

- [ ] **Step 1: Write failing tests for both record variants and lease secrecy**

Add table-driven tests that call `encodePlatformGeneration`/`decodePlatformGeneration` directly and prove these exact rules:

```go
func TestPlatformGenerationRecordVariantsAreStrict(t *testing.T) {
	claimed := PlatformGenerationSnapshot{
		GenerationID: generationTestID,
		Mode: PlatformGenerationModeSingle,
		Models: []string{"model-a"},
		State: PlatformGenerationStateRunning,
		ModelStates: map[string]PlatformGenerationModel{"model-a": {State: PlatformGenerationStateRunning}},
		CreatedAtMillis: 1_000,
		UpdatedAtMillis: 1_000,
		LeaseOwnerSHA256: strings.Repeat("a", 64),
		LeaseUntilMillis: 31_000,
	}
	tombstone := PlatformGenerationSnapshot{
		GenerationID: generationTestID,
		State: PlatformGenerationStateCancelled,
		Models: []string{},
		ModelStates: map[string]PlatformGenerationModel{},
		CreatedAtMillis: 1_000,
		UpdatedAtMillis: 1_000,
	}
	for _, snapshot := range []PlatformGenerationSnapshot{claimed, tombstone} {
		raw, err := encodePlatformGeneration(snapshot)
		if err != nil { t.Fatal(err) }
		if _, err := decodePlatformGeneration(raw); err != nil { t.Fatal(err) }
	}
}

func TestPlatformGenerationRecordRejectsMixedVariants(t *testing.T) {
	claimed := PlatformGenerationSnapshot{
		GenerationID: generationTestID, Mode: PlatformGenerationModeSingle,
		Models: []string{"model-a"}, State: PlatformGenerationStateRunning,
		ModelStates: map[string]PlatformGenerationModel{"model-a": {State: PlatformGenerationStateRunning}},
		CreatedAtMillis: 1_000, UpdatedAtMillis: 1_000,
		LeaseOwnerSHA256: strings.Repeat("a", 64), LeaseUntilMillis: 31_000,
	}
	tombstone := PlatformGenerationSnapshot{
		GenerationID: generationTestID, State: PlatformGenerationStateCancelled,
		Models: []string{}, ModelStates: map[string]PlatformGenerationModel{},
		CreatedAtMillis: 1_000, UpdatedAtMillis: 1_000,
	}
	cases := []struct { name string; value PlatformGenerationSnapshot }{
		{"partial lease digest", func() PlatformGenerationSnapshot { v := claimed; v.LeaseUntilMillis = 0; return v }()},
		{"partial lease deadline", func() PlatformGenerationSnapshot { v := claimed; v.LeaseOwnerSHA256 = ""; return v }()},
		{"malformed digest", func() PlatformGenerationSnapshot { v := claimed; v.LeaseOwnerSHA256 = strings.Repeat("G", 64); return v }()},
		{"expired creation lease", func() PlatformGenerationSnapshot { v := claimed; v.LeaseUntilMillis = 1_000; return v }()},
		{"tombstone mode", func() PlatformGenerationSnapshot { v := tombstone; v.Mode = PlatformGenerationModeSingle; return v }()},
		{"tombstone models", func() PlatformGenerationSnapshot { v := tombstone; v.Models = []string{"model-a"}; return v }()},
		{"tombstone error", func() PlatformGenerationSnapshot { v := tombstone; v.ErrorCode = "internal_error"; return v }()},
		{"tombstone lease", func() PlatformGenerationSnapshot { v := tombstone; v.LeaseOwnerSHA256 = strings.Repeat("a", 64); v.LeaseUntilMillis = 31_000; return v }()},
		{"running empty identity", func() PlatformGenerationSnapshot { v := tombstone; v.State = PlatformGenerationStateRunning; return v }()},
		{"terminal lease", func() PlatformGenerationSnapshot { v := claimed; v.State = PlatformGenerationStateFailed; v.ErrorCode = "internal_error"; v.ModelStates["model-a"] = PlatformGenerationModel{State: PlatformGenerationStateFailed, ErrorCode: "internal_error"}; return v }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := encodePlatformGeneration(tc.value); !errors.Is(err, ErrPlatformGenerationInvalid) { t.Fatalf("error=%v", err) }
		})
	}
}

func TestPlatformGenerationClaimResultNeverSerializesLeaseToken(t *testing.T) {
	encoded, err := json.Marshal(PlatformGenerationClaimResult{LeaseToken: "secret"})
	if err != nil { t.Fatal(err) }
	if bytes.Contains(encoded, []byte("secret")) { t.Fatal("lease token escaped") }
}
```

Implement the mixed-variant test as explicit named cases with concrete snapshots; do not generate random invalid cases.

- [ ] **Step 2: Run the record tests and verify RED**

Run:

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run 'TestPlatformGenerationRecord' -count=1
```

Expected: build failure because lease fields and `PlatformGenerationClaimResult` do not exist.

- [ ] **Step 3: Add record fields, constants, and claim result without weakening existing validation**

Add these declarations to `platform_generation_store.go`:

```go
const (
	platformGenerationLeaseDuration = 30 * time.Second
	platformGenerationLeaseBytes = 32
)

type PlatformGenerationSnapshot struct {
	GenerationID string `json:"generation_id"`
	Mode PlatformGenerationMode `json:"mode,omitempty"`
	Models []string `json:"models"`
	State PlatformGenerationState `json:"state"`
	ModelStates map[string]PlatformGenerationModel `json:"model_states"`
	CreatedAtMillis int64 `json:"created_at_ms"`
	UpdatedAtMillis int64 `json:"updated_at_ms"`
	ErrorCode string `json:"error_code,omitempty"`
	LeaseOwnerSHA256 string `json:"lease_owner_sha256,omitempty"`
	LeaseUntilMillis int64 `json:"lease_until_ms,omitempty"`
}

type PlatformGenerationClaimResult struct {
	Snapshot PlatformGenerationSnapshot `json:"snapshot"`
	Duplicate bool `json:"duplicate"`
	LeaseToken string `json:"-"`
}
```

Split `validPlatformGenerationSnapshot` into `validPlatformGenerationTombstone` and `validPlatformGenerationClaimed`. A pristine tombstone is valid only when cancelled with zero mode, empty models/model states, equal positive timestamps, and no error/lease fields. A running record accepts either both lease fields absent (a decodable BE02 orphan that cannot renew) or a 64-character lowercase hex digest plus `lease_until_ms > updated_at_ms`; any partial lease is invalid. Claim itself must always create the leased form with `lease_until_ms == updated_at_ms + 30_000`. Every non-running claimed record requires both lease fields to be empty/zero.

Keep existing BE02 terminal/model invariants byte-for-byte equivalent for claimed records. Existing terminal JSON without lease fields must still decode.

- [ ] **Step 4: Generate lease capability inside Claim and change its return type**

Use `crypto/rand`, `crypto/sha256`, `encoding/base64`, and `encoding/hex`:

```go
func newPlatformGenerationLease() (token, digest string, err error) {
	raw := make([]byte, platformGenerationLeaseBytes)
	if _, err = rand.Read(raw); err != nil { return "", "", ErrPlatformGenerationUnavailable }
	token = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}
```

Change Claim to:

```go
func (s *PlatformGenerationStore) Claim(ctx context.Context, input PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error)
```

For a newly created running record, set the digest and deadline and return the raw token. For every duplicate/conflict path, return the authoritative snapshot, `Duplicate: true`, an empty lease token, and `ErrPlatformGenerationConflict`. Never include the raw token in the Redis JSON.

- [ ] **Step 5: Update every existing Claim caller to the structured result**

Update all seven call sites identified by:

```bash
rg -n '\.Claim\(' --glob '*.go'
```

The shared test helper becomes:

```go
func claimTestGeneration(t *testing.T, store *PlatformGenerationStore, input PlatformGenerationClaimInput) PlatformGenerationClaimResult {
	t.Helper()
	result, err := store.Claim(context.Background(), input)
	if err != nil || result.Duplicate || result.Snapshot.GenerationID != input.GenerationID || result.LeaseToken == "" {
		t.Fatalf("claim=%#v error=%v", result, err)
	}
	return result
}
```

No BE03 persistence matcher may depend on lease fields.

- [ ] **Step 6: Run focused store, persistence, and reconciliation tests**

Run:

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run 'TestPlatformGeneration(Store|Record|Persistence|Reconcile)' -count=1
```

Expected: PASS, with fixture-dependent tests explicitly reported as `BLOCKED_FIXTURE` skips when their URLs are absent.

- [ ] **Step 7: Commit Task 1**

```bash
git add internal/service/platform_generation_store.go internal/service/platform_generation_store_test.go internal/service/platform_generation_persistence_test.go internal/service/platform_generation_reconcile_test.go
git commit -m "feat(platform): add generation runner leases"
```

### Task 2: Add atomic tombstone, cancel, renewal, expiry, and scan operations

**Files:**
- Create: `internal/service/platform_generation_store_control.go`
- Modify: `internal/service/platform_generation_store.go`
- Modify: `internal/service/platform_generation_store_test.go`

- [ ] **Step 1: Write real Redis tests for the new atomic operations**

Add tests using `openTestPlatformGenerationStore` and exact key cleanup:

```go
func TestPlatformGenerationCancelBeforeClaimCreatesAndEnrichesTombstone(t *testing.T) {
	store, client := openTestPlatformGenerationStore(t)
	preparePlatformGenerationTestKey(t, client, store.key(910100, generationTestID))

	before, err := store.CancelOrCreate(context.Background(), 910100, generationTestID, 1_000)
	if err != nil || before.State != PlatformGenerationStateCancelled || before.Mode != 0 || len(before.Models) != 0 { t.Fatalf("%#v %v", before, err) }
	ttlBefore := client.PTTL(context.Background(), store.key(910100, generationTestID)).Val()

	claim, err := store.Claim(context.Background(), PlatformGenerationClaimInput{
		UserID: 910100, GenerationID: generationTestID,
		Mode: PlatformGenerationModeSingle, Models: []string{"model-a"}, NowMillis: 2_000,
	})
	if !errors.Is(err, ErrPlatformGenerationConflict) || !claim.Duplicate || claim.LeaseToken != "" || claim.Snapshot.State != PlatformGenerationStateCancelled { t.Fatalf("%#v %v", claim, err) }
	if ttlAfter := client.PTTL(context.Background(), store.key(910100, generationTestID)).Val(); ttlAfter > ttlBefore { t.Fatalf("TTL refreshed: %v > %v", ttlAfter, ttlBefore) }
}
```

Also add named tests for concurrent cancel-before-claim, cancel-versus-commit, correct/wrong/late renewal, renew-versus-expire, stale cancelling, expired running with partially completed models, strict key parsing, bounded cursor continuation, malformed namespace keys, and disconnected Redis sanitization.

- [ ] **Step 2: Run the new tests and verify RED**

Run:

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run 'TestPlatformGeneration(Cancel|Lease|Expired|Scan)' -count=1
```

Expected: build failure because the new store methods do not exist.

- [ ] **Step 3: Implement `CancelOrCreate` as one Lua decision**

Create `platform_generation_store_control.go` with typed scan identity and operation validation:

```go
type PlatformGenerationIdentity struct {
	UserID int64
	GenerationID string
}

func (s *PlatformGenerationStore) CancelOrCreate(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error)
```

The Lua script must atomically:

```lua
local old = redis.call('GET', KEYS[1])
if not old then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
  return {1, ARGV[1]}
end
local ok, value = pcall(cjson.decode, old)
if not ok then return {0, old} end
if value.state == tonumber(ARGV[4]) then
  value.state = tonumber(ARGV[5])
  value.updated_at_ms = tonumber(ARGV[2])
  value.lease_owner_sha256 = nil
  value.lease_until_ms = nil
  local next = cjson.encode(value)
  redis.call('SET', KEYS[1], next, 'KEEPTTL')
  return {1, next}
end
return {0, old}
```

Map states through arguments rather than embedding reorderable Go enum numbers. Strictly decode the returned value. Existing non-running records return authority without an error; malformed records return `ErrPlatformGenerationInvalid` after strict decode.

- [ ] **Step 4: Extend Claim's Lua path to enrich only a pristine tombstone**

Pass both the running candidate and an enriched-cancelled candidate. The script may replace an existing value only when decoded state is cancelled, mode is absent/zero, and both models/model_states are empty. Copy the old timestamps into the enriched value, remove lease fields, and `SET ... KEEPTTL`. Return it as a duplicate conflict. Any non-pristine existing record remains byte-unchanged.

- [ ] **Step 5: Implement renewal and convergence CAS methods**

Add:

```go
func (s *PlatformGenerationStore) RenewLease(ctx context.Context, userID int64, generationID, leaseToken string, nowMillis int64) (PlatformGenerationSnapshot, error)
func (s *PlatformGenerationStore) FailExpiredRunning(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error)
func (s *PlatformGenerationStore) ConvergeStaleCancelling(ctx context.Context, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error)
```

Hash the presented lease token before comparison. Renewal requires `nowMillis <= LeaseUntilMillis` and sets `LeaseUntilMillis = nowMillis + 30_000` without changing `CreatedAtMillis` or refreshing TTL. Expiry requires running with a missing lease or `nowMillis >= LeaseUntilMillis`; it fails remaining running model states and sets overall `internal_error`. Cancellation convergence requires `nowMillis-UpdatedAtMillis >= 30_000` and marks remaining running models cancelled. All three use existing byte-CAS and return the authoritative snapshot on conflict.

- [ ] **Step 6: Implement bounded scanning and strict key parsing**

Add:

```go
func (s *PlatformGenerationStore) ScanGenerationKeys(ctx context.Context, cursor uint64, count int64) ([]PlatformGenerationIdentity, uint64, error)
```

Use `SCAN cursor MATCH porsche:platform:generation:v2:* COUNT count`. Parse the suffix with one `strings.Cut`, `strconv.ParseInt`, and canonical round-trip checks. Reject zero, signs, leading zeros, extra colons, uppercase/noncanonical UUIDs, and unrelated prefixes by omitting them from results. Return Redis failures as `ErrPlatformGenerationUnavailable` without addresses or commands.

- [ ] **Step 7: Run focused real Redis tests and race tests**

Run with the isolated fixture environment:

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run 'TestPlatformGeneration(Cancel|Lease|Expired|Scan)' -count=1
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service -run 'TestPlatformGeneration(Cancel|Lease|Expired|Scan)' -count=1
```

Expected: PASS with zero fixture skips. If `TEST_REDIS_URL` is absent, record `BLOCKED_FIXTURE` and do not mark this task complete.

- [ ] **Step 8: Commit Task 2**

```bash
git add internal/service/platform_generation_store.go internal/service/platform_generation_store_control.go internal/service/platform_generation_store_test.go
git commit -m "feat(platform): add atomic generation cancellation"
```

### Task 3: Implement the process-local cancellation registry

**Files:**
- Create: `internal/service/platform_generation_cancel_registry.go`
- Create: `internal/service/platform_generation_cancel_registry_test.go`

- [ ] **Step 1: Write registry ownership and concurrency tests**

Cover invalid identity, unique registration, duplicate rejection, invoke without holding the mutex, compare-and-delete unregister, cross-user isolation, invoke-at-most-once, and concurrent cancel/unregister:

```go
func TestPlatformGenerationCancellationRegistryOwnsCallback(t *testing.T) {
	r := NewPlatformGenerationCancellationRegistry()
	called := 0
	token, err := r.Register(7, generationTestID, func() { called++ })
	if err != nil || token == "" { t.Fatal(err) }
	if _, err := r.Register(7, generationTestID, func() {}); !errors.Is(err, ErrPlatformGenerationConflict) { t.Fatal(err) }
	if !r.Cancel(7, generationTestID) || called != 1 { t.Fatalf("called=%d", called) }
	if r.Cancel(7, generationTestID) { t.Fatal("callback invoked twice") }
	if !r.Unregister(7, generationTestID, token) { t.Fatal("owner could not unregister") }
}
```

- [ ] **Step 2: Run registry tests and verify RED**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run TestPlatformGenerationCancellationRegistry -count=1
```

Expected: build failure because the registry does not exist.

- [ ] **Step 3: Implement the registry with opaque registration tokens**

Use a mutex-protected map keyed by a comparable struct:

```go
type platformGenerationCancellationKey struct { UserID int64; GenerationID string }
type platformGenerationCancellationEntry struct { token string; cancel context.CancelFunc; invoked bool }
type PlatformGenerationCancellationRegistry struct { mu sync.Mutex; entries map[platformGenerationCancellationKey]*platformGenerationCancellationEntry }

func NewPlatformGenerationCancellationRegistry() *PlatformGenerationCancellationRegistry
func (r *PlatformGenerationCancellationRegistry) Register(userID int64, generationID string, cancel context.CancelFunc) (string, error)
func (r *PlatformGenerationCancellationRegistry) Cancel(userID int64, generationID string) bool
func (r *PlatformGenerationCancellationRegistry) Unregister(userID int64, generationID, token string) bool
```

Generate registration tokens with the same 32-byte base64url helper pattern as leases. In `Cancel`, mark `invoked` under the mutex, copy the callback, unlock, then invoke. `Unregister` deletes only when the token matches. Nil callbacks and invalid identities return `ErrPlatformGenerationInvalid` before mutation.

- [ ] **Step 4: Run unit and race tests**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run TestPlatformGenerationCancellationRegistry -count=1
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service -run TestPlatformGenerationCancellationRegistry -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 3**

```bash
git add internal/service/platform_generation_cancel_registry.go internal/service/platform_generation_cancel_registry_test.go
git commit -m "feat(platform): add generation cancellation registry"
```

### Task 4: Build authoritative status and cancellation orchestration

**Files:**
- Create: `internal/service/platform_generation_control.go`
- Create: `internal/service/platform_generation_control_test.go`
- Modify: `internal/service/platform_generation_receipt.go`

- [ ] **Step 1: Write projection and error tests against fakes**

Define tests for every state, tombstone null mode, content omission, receipt/Redis exact matching, current user total tokens, one conflict reload, and sanitized typed errors. The public values must use pointers where JSON presence matters:

```go
type PlatformGenerationView struct {
	GenerationID string `json:"generation_id"`
	Status string `json:"status"`
	Mode *string `json:"mode"`
	ConversationGUID *string `json:"conversation_guid"`
	Result *PlatformGenerationResultView `json:"result,omitempty"`
	Results []PlatformGenerationResultView `json:"results,omitempty"`
	TotalTokensUsed *int64 `json:"total_tokens_used,omitempty"`
	Code string `json:"code,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}
```

Assert serialized non-completed responses contain none of `content`, `tokens`, or `assistant_message_guid`.

- [ ] **Step 2: Run control tests and verify RED**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run TestPlatformGenerationControl -count=1
```

Expected: build failure because controller/view types do not exist.

- [ ] **Step 3: Define a handler-facing interface and controller dependencies**

Add:

```go
var (
	ErrPlatformGenerationControlInvalid = errors.New("invalid platform generation control request")
	ErrPlatformGenerationControlNotFound = errors.New("platform generation control not found")
	ErrPlatformGenerationControlUnavailable = errors.New("platform generation control unavailable")
)

type PlatformGenerationController interface {
	Get(context.Context, int64, string) (PlatformGenerationView, error)
	Cancel(context.Context, int64, string) (PlatformGenerationView, bool, error)
}

type PlatformGenerationControl struct {
	db *gorm.DB
	store *PlatformGenerationStore
	cancellations *PlatformGenerationCancellationRegistry
	now func() time.Time
	cancelBudget time.Duration
	cancelPoll time.Duration
}
```

Back this struct with a package-private `platformGenerationControlDeps` containing exact function fields for `get`, `cancelOrCreate`, `failExpiredRunning`, `convergeStaleCancelling`, `reconcile`, `loadReceipt`, and `loadTotalTokens`. The production constructor binds them to the store, BE03 reader, and GORM query. Pure tests construct deterministic fakes through those fields. The production constructor sets three seconds and 50 milliseconds; tests may replace the private durations and clock in the same package.

- [ ] **Step 4: Implement one-record convergence and projection**

`Get` derives `nowMillis`, validates input, reads the user-scoped Redis key, and calls an internal method:

```go
func (c *PlatformGenerationControl) converge(ctx context.Context, snapshot PlatformGenerationSnapshot, userID int64, now int64) (PlatformGenerationSnapshot, error)
```

Apply expired-running, stale-cancelling, and BE03 committing reconciliation. On CAS conflict, reload once. Map all raw Redis/GORM/receipt errors to the three fixed controller errors.

For completed projection, call `LoadPlatformGenerationReceipt`, compare mode, ordered models, per-model states/codes/GUIDs exactly, then query only `total_tokens_used` from an active `users.id = ? AND is_deleted = 0` row. Convert conversation and assistant GUIDs with `strconv.FormatInt`. Single uses `Result`; compare uses ordered `Results`. Never copy `receipt.UserMessage` into a view.

- [ ] **Step 5: Implement the three-second cancellation decision**

`Cancel` calls `CancelOrCreate`. If it wins `running -> cancelling`, invoke `cancellations.Cancel` once. Return terminal projections immediately. For cancelling/committing, poll `Get` within `cancelBudget`, respecting caller cancellation. Return `(view, true, nil)` only when the budget ends in cancelling/committing; `true` means the handler must use HTTP 202. If a CAS race returns running, repeat `CancelOrCreate` within the same deadline. Never return a running view.

- [ ] **Step 6: Add exact receipt matching helper tests**

Expose no new public receipt API. Add a package-private matcher that rejects extra/missing/reordered Redis models, completed GUID mismatch, failed code mismatch, tombstones, and generation/user mismatch. Confirm failed compare items have no content/token/GUID in the view.

- [ ] **Step 7: Run pure control tests**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run 'TestPlatformGeneration(Control|View|ReceiptMatches)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 4**

```bash
git add internal/service/platform_generation_control.go internal/service/platform_generation_control_test.go internal/service/platform_generation_receipt.go
git commit -m "feat(platform): add generation control service"
```

### Task 5: Add the bounded restart convergence worker

**Files:**
- Create: `internal/service/platform_generation_converger.go`
- Create: `internal/service/platform_generation_converger_test.go`

- [ ] **Step 1: Write deterministic pass and lifecycle tests**

Test immediate pass, five-second cadence, cursor continuation, 512-key limit, 100ms budget, serial processing, benign conflicts, dependency retry, malformed-key omission, and idempotent shutdown. Use injected `now`, `elapsed`, and ticker channel functions; do not sleep five seconds in tests.

```go
func TestPlatformGenerationConvergerRunPassIsBounded(t *testing.T) {
	ids := make([]PlatformGenerationIdentity, 600)
	for i := range ids {
		ids[i] = PlatformGenerationIdentity{UserID: int64(i + 1), GenerationID: generationTestID}
	}
	processed := 0
	times := []time.Time{time.Unix(0, 0), time.Unix(0, int64(101*time.Millisecond))}
	timeIndex := 0
	w := &PlatformGenerationConverger{
		scan: func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) { return ids, 77, nil },
		converge: func(context.Context, PlatformGenerationIdentity, int64) error { processed++; return nil },
		elapsedNow: func() time.Time { value := times[timeIndex]; if timeIndex == 0 { timeIndex++ }; return value },
		nowMillis: func() int64 { return 1_000 },
		maxKeys: 512, budget: 100 * time.Millisecond,
	}
	if err := w.RunPass(context.Background()); err != nil { t.Fatal(err) }
	if processed != 512 || w.cursor != 77 { t.Fatalf("processed/cursor=%d/%d", processed, w.cursor) }
}
```

- [ ] **Step 2: Run worker tests and verify RED**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run TestPlatformGenerationConverger -count=1
```

Expected: build failure because the converger does not exist.

- [ ] **Step 3: Implement a serial bounded worker**

Add constants and lifecycle:

```go
const (
	platformGenerationConvergerInterval = 5 * time.Second
	platformGenerationConvergerMaxKeys = 512
	platformGenerationConvergerBudget = 100 * time.Millisecond
)

type PlatformGenerationConverger struct {
	control *PlatformGenerationControl
	store *PlatformGenerationStore
	cancel context.CancelFunc
	done chan struct{}
	closeOnce sync.Once
	cursor uint64
	// package-private injected timing/scanner functions
}

func NewPlatformGenerationConverger(control *PlatformGenerationControl) (*PlatformGenerationConverger, error)
func (w *PlatformGenerationConverger) Start()
func (w *PlatformGenerationConverger) RunPass(context.Context) error
func (w *PlatformGenerationConverger) Close(context.Context) error
```

The struct has package-private `scan`, `converge`, `elapsedNow`, and `nowMillis` function fields plus `maxKeys` and `budget`; the constructor binds production functions and constants. `Start` launches exactly one goroutine, runs `RunPass` immediately, then on each five-second tick. `RunPass` requests bounded scan pages, updates the saved cursor after every page, and calls the controller's convergence primitive serially. Continue after typed per-record conflicts/unavailable errors; stop promptly on worker context cancellation. `Close` cancels once and waits on `done` or the supplied context.

- [ ] **Step 4: Run unit and race tests**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service -run TestPlatformGenerationConverger -count=1
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service -run TestPlatformGenerationConverger -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 5**

```bash
git add internal/service/platform_generation_converger.go internal/service/platform_generation_converger_test.go
git commit -m "feat(platform): reconcile orphan generations"
```

### Task 6: Activate authenticated GET and cancel HTTP contracts

**Files:**
- Create: `internal/handler/platform_generation.go`
- Create: `internal/handler/platform_generation_test.go`
- Modify: `internal/handler/platform.go`
- Modify: `internal/router/router_test.go`

- [ ] **Step 1: Write handler tests with a fake controller**

Use `registerPlatformWithAuthentication` and an authentication stub that sets `middleware.ContextUser` to a concrete `models.User`. The fake implements `service.PlatformGenerationController`. Cover:

- GET owner 200 for each state and both completed shapes;
- tombstone emits explicit `"mode":null`;
- cancel terminal 200, unresolved 202 plus exact `Retry-After: 1`, and never running;
- malformed/uppercase UUID 400 before controller call;
- missing GET 404;
- nil/unavailable controller 503;
- exact `Cache-Control: no-store` on every generation response;
- fixed error envelope with `error.code`, `error.message`, `error.type`, and current safe `request_id`;
- existing completion/compare v2 POSTs still return guarded 503 and legacy behavior remains unchanged.

- [ ] **Step 2: Run handler tests and verify RED**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/handler ./internal/router -run 'TestPlatformGeneration|TestNewState.*GenerationRoute' -count=1
```

Expected: route tests fail with 404 or missing symbols.

- [ ] **Step 3: Implement exact generation errors and handlers**

Create a fixed envelope local to `platform_generation.go`:

```go
type platformGenerationPublicError struct {
	Error struct {
		Code string `json:"code"`
		Message string `json:"message"`
		Type string `json:"type"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}
```

Use only these mappings:

```go
const (
	platformGenerationNotFoundCode = "generation_not_found"
	platformGenerationUnavailableCode = "generation_status_unavailable"
)
```

Invalid requests use existing code `invalid_request`. Messages are fixed English strings, never `err.Error()`. Both handlers set `Cache-Control: no-store` before validation. The cancel handler sets `Retry-After: 1` only for a successful pending response.

- [ ] **Step 4: Register routes in the existing authenticated group**

Inside `registerPlatformWithAuthentication`, add:

```go
g.GET("/chat/generations/:generation_id", platformGenerationGet(state))
g.POST("/chat/generations/:generation_id/cancel", platformGenerationCancel(state))
```

Do not change either v2 POST guard.

- [ ] **Step 5: Replace the obsolete router assertion**

Change `TestNewStateDoesNotRegisterGenerationRoutes` so it asserts GET and cancel are registered behind authentication while the two v2 stream POST bodies still receive the BE01 stable 503 response. Keep route ownership under `/api/v1/platform/chat/generations/...` only.

- [ ] **Step 6: Run handler/router tests and regression package tests**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/handler ./internal/router -run 'TestPlatformGeneration|TestNewState.*GenerationRoute|TestDecodePlatformRequest' -count=1
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/handler ./internal/router -run 'TestPlatformGeneration|TestNewState.*GenerationRoute' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit Task 6**

```bash
git add internal/handler/platform.go internal/handler/platform_generation.go internal/handler/platform_generation_test.go internal/router/router_test.go
git commit -m "feat(platform): expose generation control routes"
```

### Task 7: Assemble and close BE04 application services

**Files:**
- Modify: `internal/app/state.go`
- Modify: `internal/app/state_test.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Write state lifecycle tests**

Add constructor injection for controller/converger creation and tests proving:

- Redis plus DB wires registry, controller, and one started converger;
- Redis without DB leaves controller/worker nil so HTTP fails closed;
- partial construction closes both owned Redis clients once;
- `State.Close` stops worker before clients, is idempotent, and never closes the external GORM DB;
- a bounded worker-close timeout returns a fixed sanitized error but still attempts client cleanup.

- [ ] **Step 2: Run state tests and verify RED**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/app -run 'TestNewState.*Generation|TestStateClose' -count=1
```

Expected: build failure because BE04 fields and `State.Close` do not exist.

- [ ] **Step 3: Add state-owned BE04 dependencies**

Add fields:

```go
PlatformGenerationControl service.PlatformGenerationController
PlatformGenerationCancellations *service.PlatformGenerationCancellationRegistry
PlatformGenerationConverger *service.PlatformGenerationConverger
closeOnce sync.Once
closeErr error
```

After Redis store/persistence construction, when `db != nil`, create the registry, concrete controller, converger, assign the interface, and start the worker only after all constructors succeed. Preserve current fail-closed Redis construction and partial cleanup order.

- [ ] **Step 4: Implement bounded idempotent State.Close**

Use one five-second cleanup context. Stop the converger first, then close `PlatformGenerations`, then `AuthRedis`. Join errors without exposing them to HTTP. Do not close `DB` because its ownership remains with startup.

```go
func (s *State) Close() error {
	if s == nil { return nil }
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.PlatformGenerationConverger != nil { s.closeErr = errors.Join(s.closeErr, s.PlatformGenerationConverger.Close(ctx)) }
		if s.PlatformGenerations != nil { s.closeErr = errors.Join(s.closeErr, s.PlatformGenerations.Close()) }
		if s.AuthRedis != nil { s.closeErr = errors.Join(s.closeErr, s.AuthRedis.Close()) }
	})
	return s.closeErr
}
```

- [ ] **Step 5: Transfer server cleanup ownership**

Immediately after successful `app.NewState` in `cmd/server/main.go`, add:

```go
defer func() {
	if err := state.Close(); err != nil { log.Printf("close app state: %v", err) }
}()
```

Do not add signal handling or change server startup behavior in BE04.

- [ ] **Step 6: Run app, server build, and race tests**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/app -run 'TestNewState.*Generation|TestStateClose' -count=1
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/app -run 'TestNewState.*Generation|TestStateClose' -count=1
GOCACHE=/private/tmp/porsche-be04-go-build-cache go build ./cmd/server
```

Expected: PASS.

- [ ] **Step 7: Commit Task 7**

```bash
git add internal/app/state.go internal/app/state_test.go cmd/server/main.go
git commit -m "feat(platform): own generation worker lifecycle"
```

### Task 8: Prove real MySQL/Redis restart and multi-instance behavior

**Files:**
- Modify: `internal/service/platform_generation_control_test.go`
- Modify: `internal/service/platform_generation_converger_test.go`
- Modify: `internal/handler/platform_generation_test.go`

- [ ] **Step 1: Add explicit combined-fixture tests**

Gate the tests exactly:

```go
if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" || strings.TrimSpace(os.Getenv("TEST_REDIS_URL")) == "" {
	t.Skip("BLOCKED_FIXTURE: requires TEST_DATABASE_URL and TEST_REDIS_URL")
}
```

Reuse the BE03 owned test database naming guard, migration verification, receipt seed helpers, and exact cleanup. Add cases for:

- completed single and partial-success compare HTTP views from real receipt graphs;
- current `users.total_tokens_used` rather than generation-local receipt tokens;
- Redis/receipt GUID, mode, order, state, and code mismatch returning unavailable without content;
- receipt-present committing restart recovery;
- receipt-absent pre-30-second committing preservation and post-30-second failure;
- two convergers racing one committing record with one completed authority and no duplicate SQL writes;
- renewed long-running record surviving multiple passes;
- expired running and stale cancelling convergence;
- owner isolation for GET and cancel tombstone creation.

- [ ] **Step 2: Run combined fixture tests without accepting skips**

Run:

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./internal/service ./internal/handler -run 'TestPlatformGeneration.*(Integration|Restart|MultiInstance|Receipt|Owner)' -count=1 -json
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service ./internal/handler -run 'TestPlatformGeneration.*(Integration|Restart|MultiInstance|Owner)' -count=1 -json
```

Expected: every selected leaf test passes, zero fixture skips, zero failures. If the regex selects no tests, treat the command as failed selection and correct the exact names before proceeding.

- [ ] **Step 3: Run exact Redis race set again**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service -run 'TestPlatformGeneration(Cancel|Lease|Expired|Scan|Converger)' -count=10
```

Expected: PASS, zero skips/failures.

- [ ] **Step 4: Commit Task 8 tests**

```bash
git add internal/service/platform_generation_control_test.go internal/service/platform_generation_converger_test.go internal/handler/platform_generation_test.go
git commit -m "test(platform): verify generation restart recovery"
```

### Task 9: Run final gates and publish bounded BE04 evidence

**Files:**
- Modify: `progress.md`
- Modify: `feature_list.json`
- Create: `docs/superpowers/reports/2026-09-09-platform-generation-control.md`

- [ ] **Step 1: Run formatting and static gates**

```bash
gofmt -w internal/service/platform_generation_store.go internal/service/platform_generation_store_control.go internal/service/platform_generation_store_test.go internal/service/platform_generation_cancel_registry.go internal/service/platform_generation_cancel_registry_test.go internal/service/platform_generation_control.go internal/service/platform_generation_control_test.go internal/service/platform_generation_converger.go internal/service/platform_generation_converger_test.go internal/handler/platform.go internal/handler/platform_generation.go internal/handler/platform_generation_test.go internal/app/state.go internal/app/state_test.go cmd/server/main.go
git diff --check
GOCACHE=/private/tmp/porsche-be04-go-build-cache go vet ./...
GOCACHE=/private/tmp/porsche-be04-go-build-cache go build ./...
```

Expected: all commands exit 0.

- [ ] **Step 2: Run fresh full and affected-package race suites**

```bash
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test -race ./internal/service ./internal/handler ./internal/app ./internal/router -count=1
```

Expected: PASS. Report every opt-in skip by exact test name and reason; no BE04 fixture test may be skipped.

- [ ] **Step 3: Run content, lease, and scope scans**

```bash
rg -n 'LeaseToken|lease_owner_sha256' internal/handler internal/models docs/superpowers/reports/2026-09-09-platform-generation-control.md
rg -n 'prompt|reply|Authorization|DATABASE_URL|REDIS_URL' internal/service/platform_generation_control.go internal/service/platform_generation_converger.go internal/handler/platform_generation.go
git diff origin/main -- internal/handler/platform.go internal/service/platform_chat.go internal/service/platform_sse_v2.go
```

Expected: no lease material in handler/models/report, no content or credential serialization in BE04 control paths, no change to legacy chat orchestration or v2 SSE encoder, and only the planned route registration in `platform.go`.

- [ ] **Step 4: Update tracker and report without overstating completion**

In `feature_list.json`, keep `go-018.status` as `in_progress`, append BE04 evidence, and change notes to say BE01-BE04 complete while BE05, BE06, joint acceptance, production migration, deployment, and real upstream remain outstanding.

At the top of `progress.md`, add the BE04 candidate commit, exact focused/race/full/static results, real fixture versions and migration ledger, route status, and exclusions.

Create the report with these fixed sections:

```markdown
# Platform generation control BE04 report

## Scope and candidate
## Implemented contracts
## Real Redis evidence
## MySQL plus Redis restart evidence
## Full regression and static gates
## Security and privacy checks
## Fixture cleanup
## Explicitly not run and remaining BE05-BE06 work
```

Do not write `PASS` for any missing fixture, no-match test filter, production action, or real upstream call.

- [ ] **Step 5: Re-run document and repository checks**

```bash
python3 -m json.tool feature_list.json >/dev/null
rg -n 'TB[D]|TO[D]O|BLOCKED_FIXTURE.*PASS' docs/superpowers/reports/2026-09-09-platform-generation-control.md progress.md feature_list.json
git diff --check
git status --short
```

Expected: valid JSON, no placeholders or false fixture-pass wording, clean whitespace, and only planned files modified.

- [ ] **Step 6: Commit Task 9**

```bash
git add progress.md feature_list.json docs/superpowers/reports/2026-09-09-platform-generation-control.md
git commit -m "docs(platform): record generation control evidence"
```

- [ ] **Step 7: Final verification against the committed tree**

```bash
git status --short --branch
git log --oneline origin/main..HEAD
GOCACHE=/private/tmp/porsche-be04-go-build-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-be04-go-build-cache go vet ./...
git diff --check origin/main...HEAD
```

Expected: clean worktree, the planned BE04 commit series only, full tests/vet/diff passing, and no claim that BE05/BE06, deployment, production migration, or real upstream acceptance is complete.
