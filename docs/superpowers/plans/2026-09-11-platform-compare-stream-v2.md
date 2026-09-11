# BE06 Platform Compare SSE v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task by task.

**Goal:** Activate the authenticated `platform-chat-sse.v2` compare path for two or three models with one shared generation lease, independent model outcomes, durable partial success, detached HTTP output, and authoritative GET/cancel recovery.

**Architecture:** Add a dedicated `PlatformCompareGenerationRunner` beside the existing single runner. The coordinator owns one compare claim, registry entry, renewal loop, receipt reader, persistence transaction, terminal decision, and a shared serial critical section spanning owner-bound Redis mutation, worker state, encoder state, and complete frame write/detach; per-model workers own only their upstream body and local inputs/results. Reuse the existing BE02–BE05 store, persistence, control, cancellation, and SSE helpers without generalizing the single runner, reusing its non-thread-safe `platformSingleOutput`, or changing the schema.

**Tech Stack:** Go, Gin, GORM/MySQL 8.4, Redis 7, `httptest`, `go test -race`, Docker disposable fixtures.

**Approved design:** `docs/superpowers/specs/2026-09-11-platform-compare-stream-v2-design.md`

## Delivery boundary and execution rules

- Work only in the dedicated `platform-compare-stream-v2` worktree on `feature/platform-compare-stream-v2`.
- Do not edit Porsche-Web, migrations, deployment files, production configuration, or the legacy compare implementation.
- Do not call real or paid upstreams. Unit tests use controlled fakes; integration tests use disposable MySQL and Redis plus local deterministic upstream doubles.
- Keep `go-018` as `in_progress`. BE06 completion does not imply frontend/backend joint acceptance, production migration/deployment, public HTTPS acceptance, or real-upstream acceptance.
- Treat the current Porsche-Web contract only as an unresolved handoff: `interface-contract.json` SHA-256 `0891e452f122922f576745db89c96c853a9a7cf4ff00078c30ae3b3f0769e970`, version `v1.0.0`, status `draft`, with `interfaces/sse_events` empty. The frontend's existing compare behavior, parser, cancel flow, and GET hydration differences remain for later coordinated joint acceptance and are not BE06 implementation scope.
- Use the repository's full review sequence because this change crosses SSE, Redis, persistence, handler, and lifecycle boundaries: Explorer before implementation; original Worker for all implementation tasks; then snapshot-bound Spec, Security, and Test reviews. Any reviewed-file change invalidates downstream reviews and restarts at Spec.
- Use `GOCACHE=/private/tmp/porsche-be06-go-cache` for all Go commands. A sandbox loopback denial is an environment result, not a product result; rerun the same command in an approved environment and record both outcomes.
- Commit after every green task. Never combine a red test and its implementation in the same unverified step.

## Planned file map

**Create:**

- `internal/service/platform_compare_generation.go`
- `internal/service/platform_compare_generation_test.go`
- `internal/service/platform_compare_generation_integration_test.go`
- `internal/handler/platform_compare_v2_test.go`
- `docs/superpowers/reports/2026-09-11-platform-compare-stream-v2.md`

**Modify:**

- `internal/service/platform_generation_store.go`
- `internal/service/platform_generation_store_test.go`
- `internal/service/platform_generation_store_redis_test.go`
- `internal/handler/platform.go`
- `internal/handler/platform_single_v2_test.go`
- `internal/handler/platform_v2_contract_test.go`
- `internal/router/router_test.go` only if route inventory needs an explicit v2 compare assertion
- `internal/app/state.go`
- `internal/app/state_test.go`
- `progress.md`
- `feature_list.json`

No migration file is permitted in this plan.

### Task 1: Add the owner-bound per-model failure transition

**Files:**

- Modify: `internal/service/platform_generation_store.go:304-337`
- Test: `internal/service/platform_generation_store_test.go`
- Test: `internal/service/platform_generation_store_redis_test.go`

**Step 1: Write failing validation and in-memory behavior tests**

Add tests named:

```go
func TestPlatformGenerationStoreMarkModelFailedOwnedRejectsInvalidInputBeforeRedis(t *testing.T)
func TestPlatformGenerationStoreMarkModelFailedOwnedRequiresCurrentLease(t *testing.T)
func TestPlatformGenerationStoreMarkModelFailedOwnedIsTerminalAndRejectsReplay(t *testing.T)
func TestPlatformGenerationStoreMarkModelFailedOwnedLosesToCancelOrCommit(t *testing.T)
```

Cover blank/invalid model, model outside the claim, unsafe error code, wrong lease, expired lease, non-running global state, repeat with the same code, and repeat with a different code. Assert every terminal replay conflicts and rejected mutations leave the encoded record byte-for-byte unchanged.

**Step 2: Run the focused tests and observe the missing method failure**

Run:

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformGenerationStoreMarkModelFailedOwned' -count=1
```

Expected: compile failure because `MarkModelFailedOwned` does not exist.

**Step 3: Implement the minimal owner-bound CAS method**

Add this public shape next to `MarkModelDoneOwned`:

```go
func (s *PlatformGenerationStore) MarkModelFailedOwned(
    ctx context.Context,
    userID int64,
    generationID, leaseToken, model, code string,
    nowMillis int64,
) (PlatformGenerationSnapshot, error)
```

Implement it through the same `mutateOwned` boundary used by `RecordDeltaOwned` and `MarkModelDoneOwned`. Within that mutation, require `running`, the current unexpired owner lease, model membership, a currently running model state, and a stable public code. Reject done-to-failed, every failed-state replay, cancelling, committing, and terminal global states.

Do not weaken or redirect the existing unowned `MarkModelFailed`; existing callers and compatibility tests must keep their current behavior.

**Step 4: Add real Redis CAS tests**

Exercise two store instances against the test Redis and prove a wrong owner or expired lease cannot fail a model, while cancel-versus-owned-failure has one authoritative winner.

Run:

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformGenerationStore.*MarkModelFailedOwned|TestPlatformGenerationStore.*Cancel.*Failure' -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/service -run 'TestPlatformGenerationStore.*MarkModelFailedOwned' -count=1
```

Expected: PASS with zero skips.

**Step 5: Commit**

```bash
git add internal/service/platform_generation_store.go internal/service/platform_generation_store_test.go internal/service/platform_generation_store_redis_test.go
git commit -m "feat: add owned compare model failure transition"
```

### Task 2: Define compare-runner contracts and side-effect-free preparation

**Files:**

- Create: `internal/service/platform_compare_generation.go`
- Create: `internal/service/platform_compare_generation_test.go`

**Step 1: Write failing constructor, input-copy, and preflight tests**

Add tests named:

```go
func TestPlatformCompareGenerationRejectsInvalidDependencies(t *testing.T)
func TestPlatformCompareGenerationPreflightRejectsBeforeClaim(t *testing.T)
func TestPlatformCompareGenerationRequiresTwoOrThreeDistinctModels(t *testing.T)
func TestPlatformCompareGenerationCopiesRequestBeforeClaim(t *testing.T)
func TestPlatformCompareGenerationValidatesConversationOwnershipAndQuota(t *testing.T)
func TestPlatformCompareGenerationQuotaRequiresCapacityForEveryRequestedModel(t *testing.T)
func TestPlatformCompareGenerationQuotaAppliesDailyReset(t *testing.T)
func TestPlatformCompareGenerationProfessionalAndEnterpriseQuotaRemainUnlimited(t *testing.T)
```

Assert canonical lowercase UUID, exact request-order model preservation, distinct two-or-three models, message and context limits, conversation ownership, quota availability, and removal of v2-only controls before any claim/upstream/persistence/output call. For Free or another daily-call-limited user, compute effective current-day usage with the existing reset rule and require remaining capacity to be at least the requested model count. Cover both two- and three-model requests and both sides of the daily reset boundary. Professional and Enterprise preserve the existing unlimited rule. Do not add quota reservation, schema, or persistence state.

**Step 2: Run the tests and observe the missing symbols**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration(Rejects|Preflight|Requires|Copies|Validates|Quota|Professional)' -count=1
```

Expected: compile failure for the absent compare runner types.

**Step 3: Add the narrow public API and immutable run state**

Define:

```go
var (
    ErrPlatformCompareGenerationInvalid     = errors.New("invalid platform compare generation")
    ErrPlatformCompareGenerationQuota       = errors.New("platform compare generation quota unavailable")
    ErrPlatformCompareGenerationUnavailable = errors.New("platform compare generation unavailable")
)

type PlatformCompareGenerationInput struct {
    Context      context.Context
    User         *models.User
    GenerationID string
    RequestID    string
    Models       []string
    Params       ChatParams
    Write        func([]byte) error
}

type PlatformCompareGenerationRunResult struct {
    Started   bool
    Duplicate *PlatformGenerationSnapshot
}

type PlatformCompareGenerationRunnerAPI interface {
    Run(PlatformCompareGenerationInput) (PlatformCompareGenerationRunResult, error)
}
```

Add `NewPlatformCompareGenerationRunner` with the same dependency classes as the single runner plus an explicit receipt-reader dependency in its internal deps:

```go
loadReceipt func(context.Context, *gorm.DB, int64, string) (PlatformGenerationReceiptSnapshot, error)
```

Production construction binds it to `LoadPlatformGenerationReceipt`. Tests inject a spy/fake and prove a nil reader fails closed. Implement only dependency validation and `prepare`. Reuse package-local single-runner helpers for cloning messages, trimming context, selecting the final user message, and loading an existing conversation; extend compare quota preflight rather than applying the single-model boolean unchanged. Do not move or rename the existing helpers.

The prepared state must own copied slices/maps and construct `NewPlatformSSEV2Encoder(generationID, orderedModels)` before claim.

**Step 4: Run focused tests**

```bash
gofmt -w internal/service/platform_compare_generation.go internal/service/platform_compare_generation_test.go
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration(Rejects|Preflight|Requires|Copies|Validates|Quota|Professional)' -count=1
```

Expected: PASS with no claim or upstream call in rejection cases.

**Step 5: Commit**

```bash
git add internal/service/platform_compare_generation.go internal/service/platform_compare_generation_test.go
git commit -m "feat: define compare generation runner"
```

### Task 3: Claim once, register once, and handle duplicates authoritatively

**Files:**

- Modify: `internal/service/platform_compare_generation.go`
- Modify: `internal/service/platform_compare_generation_test.go`

**Step 1: Write failing lifecycle-entry tests**

Add:

```go
func TestPlatformCompareGenerationClaimsOrderedModelsOnce(t *testing.T)
func TestPlatformCompareGenerationDuplicateHasNoPostClaimSideEffects(t *testing.T)
func TestPlatformCompareGenerationAdmissionCancellationStopsBeforeClaim(t *testing.T)
func TestPlatformCompareGenerationRegistrationFailureSettlesOwnedRunningState(t *testing.T)
func TestPlatformCompareGenerationRequestCancellationAfterClaimDoesNotStopRunner(t *testing.T)
```

Assert one `mode=compare` claim, one reserved conversation GUID, one registry key `(userID, generationID)`, one application-rooted timeout context after handoff, and a validated duplicate projection with zero upstream/persistence/quota/output mutation.

**Step 2: Run and observe behavioral failures**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration(Claims|Duplicate|Admission|Registration|RequestCancellation)' -count=1
```

Expected: tests fail because `Run` has no claim/registration lifecycle.

**Step 3: Implement the minimal entry lifecycle**

Follow the single runner's admission ordering exactly:

```text
copy + validate -> acquire admission -> prepare -> claim compare ->
validate claimed/duplicate snapshot -> register cancellation -> emit meta -> fan-out
```

If registration loses to shutdown, settle only an authority-proven owned `running` state; never overwrite `cancelling`, `committing`, or a terminal state. Mark `Started` before the first attempted stream write so a writer failure can never cause the handler to append JSON.

**Step 4: Run normal and race tests**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration(Claims|Duplicate|Admission|Registration|RequestCancellation)' -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/service -run 'TestPlatformCompareGeneration(Admission|Registration|RequestCancellation)' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/service/platform_compare_generation.go internal/service/platform_compare_generation_test.go
git commit -m "feat: claim and register compare generations"
```

### Task 4: Fan out independent model workers with serialized state and SSE

**Files:**

- Modify: `internal/service/platform_compare_generation.go`
- Modify: `internal/service/platform_compare_generation_test.go`

**Step 1: Write failing concurrency and protocol tests**

Add:

```go
func TestPlatformCompareGenerationFansOutTwoAndThreeModels(t *testing.T)
func TestPlatformCompareGenerationAllowsCrossModelInterleavingWithStrictPerModelSequence(t *testing.T)
func TestPlatformCompareGenerationModelFailureDoesNotCancelSiblings(t *testing.T)
func TestPlatformCompareGenerationSerializesMutationStateEncoderAndFrames(t *testing.T)
func TestPlatformCompareGenerationNeverCallsHTTPWriterConcurrently(t *testing.T)
func TestPlatformCompareGenerationClosesEveryResponseBodyExactlyOnce(t *testing.T)
func TestPlatformCompareGenerationDetachedWriterStillRunsAllModels(t *testing.T)
```

Use barriers rather than sleeps to force a slow model, a fast model, concurrent deltas, a malformed upstream event, and first-write failure. Parse complete SSE frames and assert per-model sequence monotonicity, permitted cross-model interleaving, exactly one local terminal event, no torn frame, and a maximum HTTP-writer concurrency of one.

**Step 2: Run and observe failures**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration(FansOut|Allows|ModelFailure|Serializes|NeverCallsHTTPWriter|Closes|Detached)' -count=1
```

Expected: failures because model workers and local terminal handling are absent.

**Step 3: Implement model workers**

Give every worker an immutable upstream request, response-body owner, builder, token accumulator, next sequence, and result channel. For each valid non-empty delta, enforce:

```text
validate typed chunk and model -> check content/token bounds ->
lock the one shared serial section -> RecordDeltaOwned -> update worker state ->
advance encoder -> write the complete frame or detach -> unlock
```

On a valid terminal, keep `MarkModelDoneOwned`, the worker terminal-slot update, encoder transition, and complete `model_done` write/detach in that same serial section. On timeout, malformed stream, upstream error, or content overflow, do the corresponding `MarkModelFailedOwned`, worker terminal-slot update, encoder transition, and one complete `model_error` write/detach in the same section. Convert provider details only to the stable v2 code vocabulary. A local failure must not cancel sibling contexts.

The shared encoder is protected by the same serial section; do not assume it is independently thread-safe. Add compare-specific detach state under that section and do not reuse `platformSingleOutput`, which is not safe for concurrent workers. Once detached, later frame transitions remain serialized no-ops at the writer boundary while model work and persistence continue. No code path may call the HTTP writer outside the serial section.

**Step 4: Run focused normal and race tests**

```bash
gofmt -w internal/service/platform_compare_generation.go internal/service/platform_compare_generation_test.go
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration(FansOut|Allows|ModelFailure|Serializes|NeverCallsHTTPWriter|Closes|Detached)' -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/service -run 'TestPlatformCompareGeneration(FansOut|Allows|Serializes|NeverCallsHTTPWriter|Detached)' -count=1
```

Expected: PASS; each response body closes exactly once and no race is reported.

**Step 5: Commit**

```bash
git add internal/service/platform_compare_generation.go internal/service/platform_compare_generation_test.go
git commit -m "feat: stream concurrent compare model results"
```

### Task 5: Add shared renewal, cancellation, and all-failed convergence

**Files:**

- Modify: `internal/service/platform_compare_generation.go`
- Modify: `internal/service/platform_compare_generation_test.go`

**Step 1: Write failing authority tests**

Add:

```go
func TestPlatformCompareGenerationRenewsOneLeaseEveryTenSeconds(t *testing.T)
func TestPlatformCompareGenerationExplicitCancelStopsAllWorkersAndBodies(t *testing.T)
func TestPlatformCompareGenerationAllModelsFailedSkipsCommitAndPersistence(t *testing.T)
func TestPlatformCompareGenerationRenewalFailureCancelsWorkersAndFailsClosed(t *testing.T)
func TestPlatformCompareGenerationShutdownDrainsAdmissionAndRegistration(t *testing.T)
func TestPlatformCompareGenerationCancelAndTerminalRaceHasOneAuthority(t *testing.T)
```

Inject the clock/ticker. Use channels to prove renewal shares the same serial section with deltas and terminal operations, so its owner-bound Redis mutation cannot interleave with them. Also prove renewal does not advance the encoder or write a frame. Capture before/after persistence and quota counters for all-failed and cancel cases.

**Step 2: Run and observe failures**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration(Renews|ExplicitCancel|AllModelsFailed|RenewalFailure|Shutdown|CancelAndTerminal)' -count=1
```

Expected: failures because shared renewal and terminal authority convergence are incomplete.

**Step 3: Implement one renewal loop and generation-level convergence**

Start one ten-second renewal loop only after registration. Explicit cancel and application shutdown cancel every model context and close each owned body. Join workers and renewal without dropping an in-flight unknown result.

When all workers report failed, call `FailRunningOwned` directly; do not call `BeginCommitOwned` or `Finalize`. Emit one sanitized global `error` and no `done`. If authority is `cancelling`, call `AcknowledgeCancelledOwned`; if `committing`, load and strictly validate the full receipt graph through the injected receipt reader and call `ReconcileComplete`, never `Finalize`; if the snapshot cannot be trusted, fail closed without inventing a terminal success.

Every goroutine, timer, body, registration, and cancel function must have one idempotent cleanup owner.

**Step 4: Run focused normal and race tests**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration(Renews|ExplicitCancel|AllModelsFailed|RenewalFailure|Shutdown|CancelAndTerminal)' -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/service -run 'TestPlatformCompareGeneration(Renews|ExplicitCancel|Shutdown|CancelAndTerminal)' -count=1
```

Expected: PASS with zero goroutine/body leaks observed by test barriers.

**Step 5: Commit**

```bash
git add internal/service/platform_compare_generation.go internal/service/platform_compare_generation_test.go
git commit -m "feat: converge compare generation lifecycle"
```

### Task 6: Persist successful models once and publish authoritative completion

**Files:**

- Modify: `internal/service/platform_compare_generation.go`
- Modify: `internal/service/platform_compare_generation_test.go`

**Step 1: Write failing persistence and completion tests**

Add:

```go
func TestPlatformCompareGenerationPersistsAllSuccessesInRequestOrder(t *testing.T)
func TestPlatformCompareGenerationPersistsPartialSuccessAndFailedReceiptResult(t *testing.T)
func TestPlatformCompareGenerationChargesOnlySuccessfulModels(t *testing.T)
func TestPlatformCompareGenerationRejectsInvalidReceiptBeforeComplete(t *testing.T)
func TestPlatformCompareGenerationCommitUnknownReconcilesWithoutSQLReplay(t *testing.T)
func TestPlatformCompareGenerationObservedCommittingReadsReceiptAndNeverFinalizes(t *testing.T)
func TestPlatformCompareGenerationReconcileRejectsIncompleteReceiptGraph(t *testing.T)
func TestPlatformCompareGenerationDoneUsesAuthoritativeGUIDsAndTokenTotal(t *testing.T)
func TestPlatformCompareGenerationUnprovenCompletionNeverEmitsDone(t *testing.T)
```

Assert exact request order, one user message, one assistant/usage row per success, failed receipt entries with empty content and zero tokens, successful GUIDs only in Redis, at most one `Finalize` call on the direct commit path, and one global `done` only after both MySQL and Redis authority. Every path that observes `committing` or commit/`Complete` uncertainty must call the injected receipt reader, strictly validate the complete graph, and call `ReconcileComplete`; those paths must make zero additional `Finalize` calls.

**Step 2: Run and observe failures**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration(Persists|Charges|RejectsInvalidReceipt|CommitUnknown|ObservedCommitting|ReconcileRejects|DoneUses|Unproven)' -count=1
```

Expected: failures because successful terminal aggregation is not persisted/completed.

**Step 3: Implement the single commit path**

If at least one worker succeeded:

1. Build one ordered `PlatformGenerationPersistenceInput` containing every model result.
2. Call `BeginCommitOwned` once.
3. Call `Finalize` once only on this direct authority-proven commit path.
4. Validate the returned receipt/result graph against the exact user, generation, conversation provenance, ordered models, contents, failures, sequences, tokens, charges, assistant GUID presence/absence, and committed metadata.
5. Build `assistantMessageGUIDs` for successful models only and call `Complete`.
6. If `Complete` is unknown, or any convergence path observes `committing`, call the injected `loadReceipt` dependency with the existing `LoadPlatformGenerationReceipt` shape, strictly validate the same complete graph, derive GUIDs only from that validated receipt, and call `ReconcileComplete`. Never call `Finalize` in a reconciliation path.
7. Load authoritative total tokens and emit `DoneCompare` once.

Treat a missing/incomplete/mismatched receipt, total-token load failure, or impossible completion snapshot as generation-level failure. Never emit `done` speculatively and never replay SQL to resolve uncertain commit authority.

**Step 4: Run the entire compare unit suite with race detection**

```bash
gofmt -w internal/service/platform_compare_generation.go internal/service/platform_compare_generation_test.go
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run 'TestPlatformCompareGeneration' -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/service -run 'TestPlatformCompareGeneration' -count=1
```

Expected: PASS with zero skips and no race.

**Step 5: Commit**

```bash
git add internal/service/platform_compare_generation.go internal/service/platform_compare_generation_test.go
git commit -m "feat: persist compare generation outcomes"
```

### Task 7: Activate the compare-v2 HTTP branch without changing legacy behavior

**Files:**

- Modify: `internal/handler/platform.go:124-180,293-383`
- Create: `internal/handler/platform_compare_v2_test.go`
- Modify: `internal/handler/platform_single_v2_test.go:340`
- Modify: `internal/handler/platform_v2_contract_test.go`
- Modify: `internal/router/router_test.go` only if the existing inventory assertion lacks compare POST coverage

**Step 1: Write failing handler contract tests**

Add:

```go
func TestPlatformCompareV2StreamsExactPublicProtocol(t *testing.T)
func TestPlatformCompareV2MapsPreStreamOutcomes(t *testing.T)
func TestPlatformCompareV2DuplicateUsesAuthenticatedControlView(t *testing.T)
func TestPlatformCompareV2RejectsModelCardinalityAndDuplicatesBeforeRunner(t *testing.T)
func TestPlatformCompareV2FailsClosedForMissingDependencies(t *testing.T)
func TestPlatformCompareV2NeverWritesJSONAfterStreamStarts(t *testing.T)
func TestPlatformCompareV2PreservesLegacyAndNonStreamingBehavior(t *testing.T)
```

Update the former “compare remains guarded” single-v2 assertion so it now proves single v2 remains unchanged while explicit compare v2 is delegated. Require the compare branch to call `service.SetPlatformSSEV2Headers` and assert exactly `Content-Type: text/event-stream; charset=utf-8`, `Cache-Control: no-cache, no-transform`, and `X-Accel-Buffering: no`; do not add or require `Connection`, and do not change single-v2 headers. Also assert exactly one `meta`, allowed event names, no bare `[DONE]`, authenticated `409` projection, `400 invalid_request`, `429 rate_limited`, stable `503`, and no JSON appended after `Started=true`.

**Step 2: Run and observe the stable unavailable response**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/handler -run 'TestPlatform(CompareV2|SingleV2)' -count=1
```

Expected: compare-v2 tests fail because the branch still returns `platform_stream_v2_unavailable`.

**Step 3: Implement `platformCompareSSEV2`**

Keep decode, authentication, active-user, model authorization, and request validation before runner invocation. Replace only the explicit v2 compare guard with a helper parallel to `platformSingleSSEV2`:

```go
result, err := state.PlatformCompareGeneration.Run(service.PlatformCompareGenerationInput{
    Context: c.Request.Context(), User: user, GenerationID: body.GenerationID,
    RequestID: c.Writer.Header().Get("X-Request-ID"), Models: body.Models,
    Params: body.toParams(), Write: write,
})
```

Map invalid/quota/unavailable sentinels before streaming. Hydrate duplicates through `PlatformGenerationControl.Get`. After `result.Started`, return without calling `respond.Error` regardless of runner error. Do not alter legacy streaming or non-streaming dispatch.

**Step 4: Run handler/router tests**

```bash
gofmt -w internal/handler/platform.go internal/handler/platform_compare_v2_test.go internal/handler/platform_single_v2_test.go internal/handler/platform_v2_contract_test.go internal/router/router_test.go
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/handler ./internal/router -run 'TestPlatform(CompareV2|SingleV2|V2)|Test.*Platform.*Route' -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/handler -run 'TestPlatform(CompareV2|SingleV2)' -count=1
```

Expected: PASS; legacy comparison assertions remain byte-compatible.

**Step 5: Commit**

```bash
git add internal/handler/platform.go internal/handler/platform_compare_v2_test.go internal/handler/platform_single_v2_test.go internal/handler/platform_v2_contract_test.go internal/router/router_test.go
git commit -m "feat: activate platform compare sse v2"
```

If `internal/router/router_test.go` is unchanged, omit it from `git add` rather than making a no-op edit.

### Task 8: Wire compare generation into application state and shutdown

**Files:**

- Modify: `internal/app/state.go:34-90,121-265`
- Modify: `internal/app/state_test.go`

**Step 1: Write failing construction and shutdown tests**

Add:

```go
func TestNewStateWiresPlatformCompareGenerationWithExactDependencies(t *testing.T)
func TestNewStatePlatformCompareGenerationDependencyMatrix(t *testing.T)
func TestNewStatePlatformCompareGenerationFailsClosed(t *testing.T)
func TestNewStateUsesOneRootAndRegistryForSingleAndCompareRunners(t *testing.T)
func TestStateCloseWaitsForCompareAdmissionAndRegistrationBeforeRedis(t *testing.T)
func TestStateCloseCancelsBothGenerationRunnersBeforeSharedResources(t *testing.T)
```

Prove compare construction receives the exact DB/store/persistence/registry/upstream/root/timeout values, missing dependencies expose no runner, constructor errors are sanitized, and shutdown order is root cancel → registry close/drain → generation worker/store/Redis cleanup.

**Step 2: Run and observe missing state wiring**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/app -run 'Test(NewState.*PlatformCompare|NewStateUsesOneRoot|StateClose.*Compare|StateCloseCancelsBoth)' -count=1
```

Expected: compile or assertion failure because `State` has no compare runner.

**Step 3: Add fail-closed construction**

Add `PlatformCompareGeneration service.PlatformCompareGenerationRunnerAPI` and a `newPlatformCompareGeneration` constructor hook. Create both runners from the existing single application-root context and shared cancellation registry. If either configured runner cannot be constructed, return `ErrPlatformCompareGenerationUnavailable` or the existing single sentinel as applicable and invoke existing cleanup in the same resource order.

Do not add a second registry or root context. Do not close externally owned DB connections.

**Step 4: Run app and cross-package tests**

```bash
gofmt -w internal/app/state.go internal/app/state_test.go
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/app ./internal/handler -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/app -run 'Test(NewState.*Generation|StateClose.*Generation|StateClose.*Compare|StateCloseCancelsBoth)' -count=1
```

Expected: PASS and shutdown order assertions remain exact.

**Step 5: Commit**

```bash
git add internal/app/state.go internal/app/state_test.go
git commit -m "feat: wire compare generation lifecycle"
```

### Task 9: Prove BE06 against disposable MySQL and Redis fixtures

**Files:**

- Create: `internal/service/platform_compare_generation_integration_test.go`

**Step 1: Add the eight acceptance tests**

Create exactly these top-level tests, guarded by the same explicit fixture environment convention as the single-runner integration suite:

```go
func TestPlatformCompareGenerationIntegrationAllModelsSucceed(t *testing.T)
func TestPlatformCompareGenerationIntegrationPartialSuccess(t *testing.T)
func TestPlatformCompareGenerationIntegrationAllModelsFailWithoutDurableMutation(t *testing.T)
func TestPlatformCompareGenerationIntegrationDisconnectThenGET(t *testing.T)
func TestPlatformCompareGenerationIntegrationCancelNoPersistence(t *testing.T)
func TestPlatformCompareGenerationIntegrationRenewalBeatsConverger(t *testing.T)
func TestPlatformCompareGenerationIntegrationCommitUnknownReconciles(t *testing.T)
func TestPlatformCompareGenerationIntegrationConcurrentQuotaAndDuplicateRaces(t *testing.T)
```

Successful cases must include both two- and three-model requests and assert the exact receipt graph, ordered model results, one user message, one assistant message/usage row per success, failed result rows without assistant messages, successful-model call/token charges only, assistant GUIDs for successes only, GET order, and aggregate token-total equality. Limited-plan cases cover remaining capacity of `models-1`, exactly `models`, and more than `models`, plus usage immediately before and after the existing daily reset boundary; claim/upstream must be untouched when capacity is insufficient. Professional and Enterprise remain unlimited. The concurrent quota case must force multiple two/three-model `Finalize` competitors and prove only transactions with capacity for all of their possible successes commit, without reservation or schema. All-failed and cancelled cases must compare complete durable snapshots before and after and prove zero changes to conversations, messages, usage, receipts/results, quota, and token totals.

**Step 2: Start fresh isolated fixtures**

Use unique BE06-labelled container, network, volume, database, user, and port names. Start MySQL 8.4 and Redis 7, wait for health, create a private temporary credential directory, and apply migrations `0011`, `0012`, and `0013` to the fresh database. Record exact container IDs and selected loopback ports in the delivery report; never print passwords or DSNs.

**Step 3: Run the integration tests and observe any real-boundary failures**

Run with the suite's documented environment variables, keeping credentials outside command output:

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service -run '^TestPlatformCompareGenerationIntegration' -count=1 -v
```

Expected: all eight tests execute with zero skips. If a test fails, retain the fixture, diagnose with `systematic-debugging`, add the smallest failing unit regression, fix, and rerun focused normal/race tests before returning here.

**Step 4: Run integration race detection**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/service -run '^TestPlatformCompareGenerationIntegration' -count=1 -v
```

Expected: PASS, eight executed tests, zero skips, no race report.

**Step 5: Commit**

```bash
git add internal/service/platform_compare_generation_integration_test.go
git commit -m "test: cover compare generation integration"
```

Keep fixtures running until Task 10 finishes so final verification uses the same inspected state.

### Task 10: Run final gates, reviews, evidence, and exact cleanup

**Files:**

- Create: `docs/superpowers/reports/2026-09-11-platform-compare-stream-v2.md`
- Modify: `progress.md`
- Modify: `feature_list.json`

**Step 1: Generate the canonical implementation snapshot**

The `project_manager` prepares one exact repository-relative scope JSON and an external baseline JSON under a dynamically allocated private directory. Use `<private-task-dir>` below only as the concrete path parameter supplied in the implementation task package; it is not an unresolved implementation decision. The authorized writer must run this exact helper from the repository root, with no backend `--contract` argument:

```bash
python3 docs/agents/review_snapshot.py snapshot \
  --scope <private-task-dir>/scope.json \
  --baseline <private-task-dir>/baseline.json \
  --output -
```

The authorized writer saves canonical stdout byte-for-byte into `<private-task-dir>/snapshot.json` as a brand-new exclusive file outside the worktree. The target must not already exist and must not be overwritten, symlinked, hardlinked, edited, normalized, or reconstructed. Do not compute, describe, or accept any manual snapshot identity algorithm.

Spec, Security, and Test each independently run the following command against the same scope, baseline, and writer-saved snapshot before reviewing or testing:

```bash
python3 docs/agents/review_snapshot.py verify \
  --scope <private-task-dir>/scope.json \
  --baseline <private-task-dir>/baseline.json \
  --snapshot <private-task-dir>/snapshot.json
```

Security starts only after `SPEC_PASS` binds that verified snapshot; Test starts only after Spec and Security pass the same verified snapshot. Any change to reviewed content requires a new canonical snapshot saved to a new exclusive file and restarts the chain from Spec.

**Step 2: Run the full verification matrix**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service ./internal/handler ./internal/app ./internal/router -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/service ./internal/handler ./internal/app ./internal/router -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./... -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go test -race ./internal/service ./internal/handler ./internal/app -count=1
GOCACHE=/private/tmp/porsche-be06-go-cache go build ./...
GOCACHE=/private/tmp/porsche-be06-go-cache go vet ./...
git diff --check
python3 -m json.tool feature_list.json >/dev/null
```

Expected: every command exits 0. Record package counts and elapsed times without claiming skipped tests passed.

**Step 3: Run evidence-specific audits**

Run a skip census and explain every skip:

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./... -count=1 -v | rg -- '--- SKIP:'
```

Run a privacy scan over the diff and report; reject credentials, DSNs, provider bodies, prompts/replies, SQL, Redis keys, lease material, and local private paths:

```bash
git diff --cached --check
git diff --check
git grep -n -E '(password|passwd|secret|api[_-]?key|authorization:|redis://|mysql://)' -- docs/superpowers/reports/2026-09-11-platform-compare-stream-v2.md
```

Inspect all tracked migration changes and require none:

```bash
git diff --name-only origin/main...HEAD | rg '(^|/)(migrations?|migration)/'
```

Expected: no migration path and no secret-bearing evidence. Benign vocabulary matches must be explained rather than hidden.

**Step 4: Write the delivery report and update trackers**

The report must include baseline and final revisions, canonical review snapshot ID, external scope/baseline/snapshot evidence references and hashes, approved scope, changed files, exact test commands/results, eight real-fixture cases with zero skips, durable row/counter evidence, SSE examples containing only sanitized synthetic content, review verdicts and each role's successful verify command, skip census, privacy audit, fixture IDs/ports, and cleanup proof.

Update `progress.md` and `feature_list.json` to say BE06 is locally complete only if every gate passed. Preserve `go-018: in_progress` and explicitly list remaining frontend/backend contract alignment, joint acceptance, production migration/deployment, public HTTPS, and real-upstream evidence. Do not say push, PR, merge, deploy, or production migration occurred.

**Step 5: Stop and remove only the exact disposable resources**

Stop/remove the recorded BE06 container IDs, named network, named volumes, private credential directory, and any test-owned listener. Verify by exact label/name/port checks that all are absent. Do not use wildcard cleanup or touch unrelated Docker resources.

**Step 6: Re-run lightweight post-cleanup gates and commit evidence**

```bash
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./internal/service ./internal/handler ./internal/app ./internal/router -count=1
git diff --check
python3 -m json.tool feature_list.json >/dev/null
git status --short
```

Expected: PASS and only the three evidence/tracker files are pending.

```bash
git add docs/superpowers/reports/2026-09-11-platform-compare-stream-v2.md progress.md feature_list.json
git commit -m "docs: record compare stream v2 evidence"
```

**Step 7: Verify the final committed tree**

```bash
git status --short --branch
git log --oneline origin/main..HEAD
GOCACHE=/private/tmp/porsche-be06-go-cache go test ./... -count=1
git diff --check origin/main...HEAD
```

Expected: clean worktree, the planned commit sequence, all tests passing, and no diff whitespace errors. Report local completion only from these fresh outputs.

## Final handoff boundary

After Task 10, present branch/revision, verification and review evidence, fixture cleanup proof, and remaining `go-018` work. Offer push/PR as a separate action. Do not push, create or merge a PR, deploy, migrate production, call a real upstream, or modify Porsche-Web without new authorization.
