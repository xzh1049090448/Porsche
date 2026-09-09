# Platform Single-Generation SSE v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Activate durable single-model `platform-chat-sse.v2` generation with strict SSE projection, 10-second lease renewal, continuation after client disconnect, explicit cancellation, application shutdown handling, and BE03 receipt-backed persistence.

**Architecture:** A synchronous request goroutine becomes an application-managed runner after admission and claim. Its context is detached from client cancellation but bounded by the configured upstream timeout, explicit cancel, lease authority, and application lifecycle; downstream write failure only disables SSE output. Redis owns lifecycle/sequence state, BE03 owns the atomic success transaction, and BE04 GET hydrates completed content from the receipt graph.

**Tech Stack:** Go 1.22, Gin, GORM/MySQL 8.4, go-redis/Redis 7, `net/http`, strict OpenAI-compatible SSE projection, existing BE01-BE04 platform generation primitives.

---

## File map and fixed boundaries

- `internal/service/platform_generation_persistence.go`: represent and persist either an existing conversation GUID or a server-reserved new GUID without changing migration `0011`.
- `internal/service/platform_generation_persistence_test.go`: validate reserved-GUID creation, provenance, collision rollback, and idempotency.
- `internal/service/platform_generation_store.go`: add capability-bound runner mutations while retaining BE02/BE04 CAS, TTL, and strict decoding.
- `internal/service/platform_generation_store_control.go`: retain a non-renewable owner digest while a running generation is cancelling.
- `internal/service/platform_generation_store_test.go`: pure store validation plus existing compatibility coverage.
- `internal/service/platform_generation_store_redis_test.go`: new focused real Redis ownership, renewal, cancellation, and race cases.
- `internal/service/platform_generation_cancel_registry.go`: extend the existing process-local registry with close/reject/drain semantics; do not introduce a second competing registry.
- `internal/service/platform_generation_cancel_registry_test.go`: deterministic registration, cancel, shutdown, and compare-and-delete tests.
- `internal/whitelabel/sse.go`: add a typed chunk-consumption entry point while preserving the legacy projected-frame API.
- `internal/whitelabel/sse_test.go`: strict upstream terminal/usage and compatibility tests.
- `internal/service/platform_single_generation.go`: own admission after model authorization, claim, runner context, renewal, output detachment, upstream consumption, state transitions, persistence, and terminal SSE.
- `internal/service/platform_single_generation_test.go`: fake-dependency runner contract, success, failure, timing, and race tests.
- `internal/service/platform_single_generation_integration_test.go`: explicit MySQL 8.4 + Redis 7 end-to-end persistence and GET-recovery tests.
- `internal/handler/platform.go`: replace only the single v2 stable-503 guard with BE05 admission; leave compare v2 guarded.
- `internal/handler/platform_single_v2_test.go`: exact HTTP/SSE contracts and zero-upstream negative cases.
- `internal/app/state.go`: construct the runner and close runners before the BE04 worker/Redis clients.
- `internal/app/state_test.go`: construction failure cleanup and bounded close ordering.
- `internal/router/router_test.go`: prove single v2 is active, compare v2 stays `503`, and legacy behavior is unchanged.
- `progress.md`, `feature_list.json`: record BE05 evidence while keeping `go-018` `in_progress` because BE06 and release work remain open.
- `docs/superpowers/reports/2026-09-10-platform-single-stream-v2.md`: final bounded evidence and exclusions.

No migration file, frontend file, deployment script, production environment, or compare implementation belongs in this plan.

### Task 1: Persist an exact server-reserved new conversation GUID

**Files:**

- Modify: `internal/service/platform_generation_persistence.go`
- Modify: `internal/service/platform_generation_persistence_test.go`

- [ ] **Step 1: Write failing validation and transaction tests**

Add table cases proving exactly one conversation identity form is required and an integration case proving the reserved GUID is the GUID committed for a new conversation:

```go
func TestValidatePlatformGenerationPersistenceInputRequiresOneConversationIdentity(t *testing.T) {
	valid := validPlatformGenerationPersistenceInput()
	reserved := int64(8101)
	existing := int64(8102)

	valid.ReservedConversationGUID = &reserved
	if err := validatePlatformGenerationPersistenceInput(valid); err != nil {
		t.Fatalf("reserved new conversation rejected: %v", err)
	}

	both := valid
	both.ConversationGUID = &existing
	if err := validatePlatformGenerationPersistenceInput(both); !errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
		t.Fatalf("both conversation identities error = %v", err)
	}

	neither := valid
	neither.ReservedConversationGUID = nil
	if err := validatePlatformGenerationPersistenceInput(neither); !errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
		t.Fatalf("missing conversation identity error = %v", err)
	}
}

func TestPlatformGenerationPersistenceCreatesReservedConversationGUID(t *testing.T) {
	f := newPlatformGenerationPersistenceFixture(t)
	input := f.singleInput()
	reserved := persistence.NextGUID()
	input.ConversationGUID = nil
	input.ReservedConversationGUID = &reserved

	receipt, err := f.persistence.Finalize(context.Background(), f.db, input)
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if receipt.ConversationGUID != reserved || receipt.RequestedExistingConversation {
		t.Fatalf("receipt conversation = %d existing=%v", receipt.ConversationGUID, receipt.RequestedExistingConversation)
	}
	var conversation models.Conversation
	if err := f.db.Where("guid = ? AND user_id = ? AND is_deleted = 0", reserved, input.UserID).First(&conversation).Error; err != nil {
		t.Fatalf("reserved conversation lookup: %v", err)
	}
}
```

Add a collision test that pre-creates the reserved GUID for another conversation and asserts no new message, usage, quota, or receipt is committed.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/service -run 'TestValidatePlatformGenerationPersistenceInputRequiresOneConversationIdentity|TestPlatformGenerationPersistenceCreatesReservedConversationGUID|TestPlatformGenerationPersistenceReservedGUIDCollisionRollsBack' -count=1
```

Expected: build fails because `ReservedConversationGUID` does not exist.

- [ ] **Step 3: Add the reserved-new identity to the typed input**

Use this exact representation and XOR validation:

```go
type PlatformGenerationPersistenceInput struct {
	UserID                  int64
	GenerationID            string
	Mode                    PlatformGenerationMode
	Models                  []string
	ConversationGUID        *int64
	ReservedConversationGUID *int64
	UserMessage             string
	Results                 []PlatformGenerationPersistenceResult
	NowMillis               int64
}

func validPlatformGenerationConversationIdentity(input PlatformGenerationPersistenceInput) bool {
	existing := input.ConversationGUID != nil
	reserved := input.ReservedConversationGUID != nil
	if existing == reserved {
		return false
	}
	if existing {
		return *input.ConversationGUID > 0
	}
	return *input.ReservedConversationGUID > 0
}
```

Call `validPlatformGenerationConversationIdentity` from `validatePlatformGenerationPersistenceInput`. Update all BE03 test builders so new-conversation inputs receive a server-generated reserved GUID and existing-conversation inputs retain `ConversationGUID` only.

- [ ] **Step 4: Create and compare the exact reserved GUID**

In `persistPlatformGeneration`, keep the existing branch unchanged and set the audit GUID explicitly in the new branch:

```go
} else {
	model := input.Models[0]
	conversationCreated = true
	audit := platformPersistenceAudit(input.UserID, input.NowMillis)
	audit.Guid = *input.ReservedConversationGUID
	conversation = models.Conversation{
		AuditFields: audit,
		UserID:      input.UserID,
		Title:       truncateTitle(input.UserMessage),
		Model:       &model,
	}
	if err := tx.Create(&conversation).Error; err != nil {
		return err
	}
}
```

In `platformReceiptMatchesInput`, retain `receipt.RequestedExistingConversation == (input.ConversationGUID != nil)` and require the stored GUID to equal whichever pointer is non-nil:

```go
expectedConversationGUID := *input.ReservedConversationGUID
if input.ConversationGUID != nil {
	expectedConversationGUID = *input.ConversationGUID
}
if receipt.ConversationGUID != expectedConversationGUID {
	return false
}
```

- [ ] **Step 5: Run persistence regression and commit**

Run:

```bash
go test ./internal/service -run 'Test.*PlatformGenerationPersistence|TestValidatePlatformGenerationPersistenceInput' -count=1
git diff --check
```

Expected: PASS, with fixture-dependent cases either running against the explicit fixture or retaining their existing named skip.

Commit:

```bash
git add internal/service/platform_generation_persistence.go internal/service/platform_generation_persistence_test.go
git commit -m "feat(platform): persist reserved conversation guid"
```

### Task 2: Add capability-bound runner state mutations

**Files:**

- Modify: `internal/service/platform_generation_store.go`
- Modify: `internal/service/platform_generation_store_control.go`
- Modify: `internal/service/platform_generation_store_test.go`
- Create: `internal/service/platform_generation_store_redis_test.go`

- [ ] **Step 1: Write RED tests for owner-bound mutations**

Add pure validation tests and real Redis tests for these exact methods:

```go
func (s *PlatformGenerationStore) RecordDeltaOwned(ctx context.Context, userID int64, generationID, leaseToken, model string, seq, nowMillis int64) (PlatformGenerationSnapshot, error)
func (s *PlatformGenerationStore) MarkModelDoneOwned(ctx context.Context, userID int64, generationID, leaseToken, model string, lastSeq, nowMillis int64) (PlatformGenerationSnapshot, error)
func (s *PlatformGenerationStore) BeginCommitOwned(ctx context.Context, userID int64, generationID, leaseToken string, nowMillis int64) (PlatformGenerationSnapshot, error)
func (s *PlatformGenerationStore) FailRunningOwned(ctx context.Context, userID int64, generationID, leaseToken, code string, nowMillis int64) (PlatformGenerationSnapshot, error)
func (s *PlatformGenerationStore) AcknowledgeCancelledOwned(ctx context.Context, userID int64, generationID, leaseToken string, nowMillis int64) (PlatformGenerationSnapshot, error)
```

The tests must prove correct tokens succeed, wrong/empty/malformed tokens fail without mutation, CAS losers return authority, TTL is not refreshed, a cancelling record cannot renew, and a cancellation acknowledgement cannot overwrite committing/completed/failed.

Use a race table with one claim token and two goroutines:

```go
results := make(chan PlatformGenerationState, 2)
go func() {
	snapshot, _ := store.BeginCommitOwned(ctx, userID, generationID, leaseToken, now+1)
	results <- snapshot.State
}()
go func() {
	decision, _ := store.CancelOrCreate(ctx, userID, generationID, now+1)
	results <- decision.Snapshot.State
}()
```

Assert the final state is exactly one valid authority and never returns to running after leaving it.

- [ ] **Step 2: Run focused store tests and verify RED**

Run:

```bash
go test ./internal/service -run 'TestPlatformGeneration.*Owned|TestPlatformGenerationOwner.*Race' -count=1
```

Expected: build fails because the owner-bound methods are absent.

- [ ] **Step 3: Implement constant-time capability verification inside CAS**

Add strict token hashing and an owned mutation wrapper:

```go
func platformGenerationLeaseMatches(snapshot PlatformGenerationSnapshot, digest string) bool {
	if len(snapshot.LeaseOwnerSHA256) != sha256.Size*2 || len(digest) != sha256.Size*2 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(snapshot.LeaseOwnerSHA256), []byte(digest)) == 1
}

func (s *PlatformGenerationStore) mutateOwned(
	ctx context.Context,
	userID int64,
	generationID string,
	leaseToken string,
	nowMillis int64,
	change func(*PlatformGenerationSnapshot) error,
) (PlatformGenerationSnapshot, error) {
	if !validPlatformGenerationLeaseToken(leaseToken) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	digest := platformGenerationLeaseDigest(leaseToken)
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if !platformGenerationLeaseMatches(*snapshot, digest) {
			return ErrPlatformGenerationConflict
		}
		return change(snapshot)
	})
}
```

Import `crypto/subtle`. Each public owned method validates model/code/sequence, then calls `mutateOwned` and repeats the existing transition invariant in its callback.

- [ ] **Step 4: Preserve cancellation ownership without permitting renewal**

Change transition cleanup so `running -> cancelling` clears only `LeaseUntilMillis`; retain `LeaseOwnerSHA256` until `AcknowledgeCancelledOwned` or `ConvergeStaleCancelling` reaches terminal. Update strict decoding to accept hash-only capability material only in `cancelling`:

```go
func validPlatformGenerationLease(snapshot PlatformGenerationSnapshot) bool {
	switch snapshot.State {
	case PlatformGenerationStateRunning:
		return validPlatformGenerationLeaseDigest(snapshot.LeaseOwnerSHA256) && snapshot.LeaseUntilMillis > 0
	case PlatformGenerationStateCancelling:
		return validPlatformGenerationLeaseDigest(snapshot.LeaseOwnerSHA256) && snapshot.LeaseUntilMillis == 0
	default:
		return snapshot.LeaseOwnerSHA256 == "" && snapshot.LeaseUntilMillis == 0
	}
}

func validPlatformGenerationLeaseDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
```

In `CancelOrCreate`, construct `running -> cancelling` with the current `LeaseOwnerSHA256` retained and `LeaseUntilMillis = 0`; do not clear both fields as BE04 did. `RenewLease` continues to require `running`, so the retained digest is not a usable lease. Pristine/enriched cancelled tombstones remain digest-free. `AcknowledgeCancelledOwned` and `ConvergeStaleCancelling` clear the digest in their terminal CAS even when no local runner exists.

- [ ] **Step 5: Implement terminal owner operations**

Use these state changes inside `mutateOwned`:

```go
func failOwnedSnapshot(snapshot *PlatformGenerationSnapshot, code string) error {
	if snapshot.State != PlatformGenerationStateRunning {
		return ErrPlatformGenerationConflict
	}
	for model, state := range snapshot.ModelStates {
		if state.State == PlatformGenerationStateRunning {
			state.State = PlatformGenerationStateFailed
			state.ErrorCode = code
			snapshot.ModelStates[model] = state
		}
	}
	snapshot.State = PlatformGenerationStateFailed
	snapshot.ErrorCode = code
	return nil
}

func acknowledgeCancelledOwnedSnapshot(snapshot *PlatformGenerationSnapshot) error {
	if snapshot.State != PlatformGenerationStateCancelling {
		return ErrPlatformGenerationConflict
	}
	for model, state := range snapshot.ModelStates {
		if state.State == PlatformGenerationStateRunning {
			state.State = PlatformGenerationStateCancelled
			snapshot.ModelStates[model] = state
		}
	}
	snapshot.State = PlatformGenerationStateCancelled
	return nil
}
```

Keep existing unowned methods for BE02-BE04 compatibility, but production BE05 runner code must depend only on an interface exposing the owned methods.

- [ ] **Step 6: Run store normal/race suites and commit**

Run with an explicit isolated `TEST_REDIS_URL`:

```bash
go test ./internal/service -run 'TestPlatformGeneration.*Owned|TestPlatformGeneration.*Lease|TestPlatformGeneration.*Cancel' -count=1
go test -race ./internal/service -run 'TestPlatformGeneration.*Owned|TestPlatformGenerationOwner.*Race' -count=1
git diff --check
```

Expected: all selected tests PASS and no selected fixture test skips.

Commit:

```bash
git add internal/service/platform_generation_store.go internal/service/platform_generation_store_control.go internal/service/platform_generation_store_test.go internal/service/platform_generation_store_redis_test.go
git commit -m "feat(platform): bind runner mutations to lease owner"
```

### Task 3: Make the cancellation registry application-owned and drainable

**Files:**

- Modify: `internal/service/platform_generation_cancel_registry.go`
- Modify: `internal/service/platform_generation_cancel_registry_test.go`

- [ ] **Step 1: Write RED lifecycle tests**

Add tests covering close rejection, cancellation of all active registrations, bounded waiting, explicit-cancel idempotency, and compare-and-delete cleanup:

```go
func TestPlatformGenerationCancellationRegistryCloseCancelsAndDrains(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	cancelled := make(chan struct{}, 1)
	token, err := registry.Register(7, cancellationRegistryGenerationID, func() { cancelled <- struct{}{} })
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- registry.CloseAndWait(ctx) }()
	<-cancelled
	if _, err := registry.Register(8, cancellationRegistryGenerationID, func() {}); !errors.Is(err, ErrPlatformGenerationUnavailable) {
		t.Fatalf("Register after close error = %v", err)
	}
	if !registry.Unregister(7, cancellationRegistryGenerationID, token) {
		t.Fatal("Unregister active runner = false")
	}
	if err := <-done; err != nil {
		t.Fatalf("CloseAndWait() error = %v", err)
	}
}
```

Add a timeout test where the runner never unregisters and expect `context.DeadlineExceeded` without deleting the live entry.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./internal/service -run 'TestPlatformGenerationCancellationRegistry.*Close|TestPlatformGenerationCancellationRegistry.*Drain' -count=1
```

Expected: build fails because `CloseAndWait` is absent.

- [ ] **Step 3: Add close state and a drain signal**

Extend the registry with one channel representing the current zero-entry boundary:

```go
type PlatformGenerationCancellationRegistry struct {
	mu        sync.Mutex
	entropyMu sync.Mutex
	entries   map[platformGenerationCancellationKey]platformGenerationCancellationEntry
	reader    io.Reader
	closed    bool
	drained   chan struct{}
}

func closedPlatformGenerationDrain() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (r *PlatformGenerationCancellationRegistry) drainLocked() chan struct{} {
	if r.drained == nil {
		r.drained = closedPlatformGenerationDrain()
	}
	return r.drained
}
```

Construct with `drained: closedPlatformGenerationDrain()`. Call `drainLocked` under every registry lock so the supported zero value also works. Under `Register`'s final lock, reject `closed`, and when inserting the first entry replace the closed channel with a new open channel. Under successful `Unregister`, close `drained` when the map becomes empty.

- [ ] **Step 4: Implement cancel-all and bounded wait**

Use this exact locking pattern so callbacks run outside the mutex:

```go
func (r *PlatformGenerationCancellationRegistry) CloseAndWait(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrPlatformGenerationUnavailable
	}
	r.mu.Lock()
	r.closed = true
	cancels := make([]context.CancelFunc, 0, len(r.entries))
	for key, entry := range r.entries {
		if !entry.invoked {
			entry.invoked = true
			r.entries[key] = entry
			cancels = append(cancels, entry.cancel)
		}
	}
	drained := r.drainLocked()
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

Repeated calls wait on the same drain boundary and do not invoke callbacks twice.

- [ ] **Step 5: Run normal/race tests and commit**

Run:

```bash
go test ./internal/service -run 'TestPlatformGenerationCancellationRegistry' -count=1
go test -race ./internal/service -run 'TestPlatformGenerationCancellationRegistry' -count=1
git diff --check
```

Expected: PASS with no goroutine leak or race report.

Commit:

```bash
git add internal/service/platform_generation_cancel_registry.go internal/service/platform_generation_cancel_registry_test.go
git commit -m "feat(platform): drain generation runners on close"
```

### Task 4: Expose strict typed upstream SSE chunks

**Files:**

- Modify: `internal/whitelabel/sse.go`
- Modify: `internal/whitelabel/sse_test.go`

- [ ] **Step 1: Write RED tests for a typed consumer**

Define tests for fragmented input, multiple frames per read, typed chunks, one `[DONE]`, premature EOF, malformed JSON, and callback failure:

```go
func TestConsumeChatCompletionSSEContextProjectsTypedChunks(t *testing.T) {
	input := strings.Join([]string{
		"data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"A\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":2,\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n",
		"data: [DONE]\n\n",
	}, "")
	var chunks []ChatCompletionChunk
	err := (&WhiteLabelService{}).ConsumeChatCompletionSSEContext(context.Background(), strings.NewReader(input), "model-a", func(chunk ChatCompletionChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil || len(chunks) != 2 || chunks[0].Model != "model-a" || chunks[1].Usage.TotalTokens != 3 {
		t.Fatalf("ConsumeChatCompletionSSEContext() chunks=%#v err=%v", chunks, err)
	}
}
```

Retain existing tests proving `ProjectChatCompletionSSEContext` emits byte-identical safe OpenAI frames and one `data: [DONE]`.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/whitelabel -run 'TestConsumeChatCompletionSSEContext|TestProjectChatCompletionSSE' -count=1
```

Expected: build fails because the typed method is absent.

- [ ] **Step 3: Factor the existing reader around typed callbacks**

Add this public method:

```go
func (s *WhiteLabelService) ConsumeChatCompletionSSEContext(
	ctx context.Context,
	reader io.Reader,
	logicalModelID string,
	emit func(ChatCompletionChunk) error,
) *Error
```

It reuses the existing bounded line/frame parser and `projectChatCompletionChunkDetail`, invokes `emit(projected)` for each data object, returns success only after an exact `[DONE]` frame, and preserves the existing fixed diagnostic categories. A callback error maps to the existing sanitized stream-write failure.

Rewrite `ProjectChatCompletionSSEContext` as a compatibility adapter using the typed method:

```go
func (s *WhiteLabelService) ProjectChatCompletionSSEContext(ctx context.Context, reader io.Reader, logicalModelID string, emit func([]byte) error) *Error {
	err := s.ConsumeChatCompletionSSEContext(ctx, reader, logicalModelID, func(chunk ChatCompletionChunk) error {
		encoded, marshalErr := json.Marshal(chunk)
		if marshalErr != nil {
			return marshalErr
		}
		frame := append([]byte("data: "), encoded...)
		frame = append(frame, '\n', '\n')
		return emit(frame)
	})
	if err != nil {
		return err
	}
	if emitErr := emit([]byte("data: [DONE]\n\n")); emitErr != nil {
		return ErrUpstreamUnavailable("stream write failed")
	}
	return nil
}
```

Keep the diagnostics begin/end ownership in one layer so the adapter does not double-record stages.

- [ ] **Step 4: Run white-label regression and commit**

Run:

```bash
go test ./internal/whitelabel -count=1
go test -race ./internal/whitelabel -count=1
git diff --check
```

Expected: PASS and existing projected wire bytes remain unchanged.

Commit:

```bash
git add internal/whitelabel/sse.go internal/whitelabel/sse_test.go
git commit -m "refactor(whitelabel): expose typed stream chunks"
```

### Task 5: Build the single-generation runner happy path

**Files:**

- Create: `internal/service/platform_single_generation.go`
- Create: `internal/service/platform_single_generation_test.go`

- [ ] **Step 1: Define fakeable runner contracts and RED success tests**

Use narrow interfaces so tests do not require Redis, MySQL, or a network server:

```go
type platformSingleGenerationStore interface {
	Claim(context.Context, PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error)
	RecordDeltaOwned(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error)
	MarkModelDoneOwned(context.Context, int64, string, string, string, int64, int64) (PlatformGenerationSnapshot, error)
	BeginCommitOwned(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error)
	Complete(context.Context, int64, string, map[string]string, int64) (PlatformGenerationSnapshot, error)
	RenewLease(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error)
	FailRunningOwned(context.Context, int64, string, string, string, int64) (PlatformGenerationSnapshot, error)
	AcknowledgeCancelledOwned(context.Context, int64, string, string, int64) (PlatformGenerationSnapshot, error)
}

type platformSingleGenerationPersistence interface {
	Finalize(context.Context, *gorm.DB, PlatformGenerationPersistenceInput) (PlatformGenerationReceiptSnapshot, error)
}

type platformSingleGenerationUpstream interface {
	Chat(context.Context, []byte) (*http.Response, *whitelabel.Error)
	ConsumeChatCompletionSSEContext(context.Context, io.Reader, string, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error
}

type PlatformSingleGenerationInput struct {
	User             *models.User
	GenerationID     string
	RequestID        string
	Params           ChatParams
	Write            func([]byte) error
}

type PlatformSingleGenerationRunResult struct {
	Started   bool
	Duplicate *PlatformGenerationSnapshot
}

type PlatformSingleGenerationRunnerAPI interface {
	Run(PlatformSingleGenerationInput) (PlatformSingleGenerationRunResult, error)
}
```

The primary success test must assert this exact dependency order:

```go
wantCalls := []string{
	"preflight", "claim", "register", "meta", "upstream", "record:1", "delta:1",
	"model_done_store", "begin_commit", "model_done_sse", "persist", "complete", "done", "unregister",
}
```

It must also assert the final SSE bytes equal BE01's `meta`, `delta`, `model_done`, and `done` frames, the persistence input uses the same generation/model/user message/reserved GUID/content/tokens/last sequence, and only `Complete` makes global `done` possible.

- [ ] **Step 2: Run the runner test and verify RED**

Run:

```bash
go test ./internal/service -run 'TestPlatformSingleGeneration.*Success|TestPlatformSingleGeneration.*Duplicate' -count=1
```

Expected: build fails because `PlatformSingleGenerationRunner` is absent.

- [ ] **Step 3: Implement construction, preflight, and detached ownership**

Create these concrete dependencies and constructor:

```go
var (
	ErrPlatformSingleGenerationInvalid     = errors.New("invalid platform single generation")
	ErrPlatformSingleGenerationQuota       = errors.New("platform single generation quota unavailable")
	ErrPlatformSingleGenerationUnavailable = errors.New("platform single generation unavailable")
	ErrPlatformSingleGenerationUpstream    = errors.New("platform single generation upstream failure")
	ErrPlatformSingleGenerationOversize    = errors.New("platform single generation content too large")
)

type platformSingleGenerationDeps struct {
	db             *gorm.DB
	store          platformSingleGenerationStore
	persistence    platformSingleGenerationPersistence
	registry       *PlatformGenerationCancellationRegistry
	upstream       platformSingleGenerationUpstream
	rootContext    context.Context
	now            func() time.Time
	newGUID        func() int64
	loadTotalTokens func(context.Context, *gorm.DB, int64) (int64, error)
	upstreamTimeout time.Duration
}

type PlatformSingleGenerationRunner struct {
	deps platformSingleGenerationDeps
}

func NewPlatformSingleGenerationRunner(
	db *gorm.DB,
	store *PlatformGenerationStore,
	persistenceService *PlatformGenerationPersistence,
	registry *PlatformGenerationCancellationRegistry,
	upstream *whitelabel.WhiteLabelService,
	rootContext context.Context,
	upstreamTimeout time.Duration,
) (*PlatformSingleGenerationRunner, error) {
	deps := platformSingleGenerationDeps{
		db: db, store: store, persistence: persistenceService, registry: registry,
		upstream: upstream, rootContext: rootContext, now: time.Now,
		newGUID: persistence.NextGUID, loadTotalTokens: loadPlatformSingleTotalTokens,
		upstreamTimeout: upstreamTimeout,
	}
	if !validPlatformSingleGenerationDeps(deps) {
		return nil, ErrPlatformSingleGenerationUnavailable
	}
	return &PlatformSingleGenerationRunner{deps: deps}, nil
}
```

`Run` must copy all request-owned values before claim, trim messages using the existing context-window rule, require one final non-empty user message, parse/resolve an existing conversation or reserve a new GUID, perform read-only quota preflight on a copy of the authenticated user, claim, and return a typed duplicate result without registering or calling upstream.

Use an injected UTC time for the non-mutating quota check:

```go
func platformSingleQuotaAvailable(user *models.User, now time.Time) bool {
	if user == nil || user.ID <= 0 || user.Status != models.UserStatusActive || user.IsDeleted != 0 {
		return false
	}
	if user.PlanType == models.PlanProfessional || user.PlanType == models.PlanEnterprise {
		return true
	}
	used := user.DailyCallsUsed
	if user.DailyCallsResetAt == nil || time.UnixMilli(*user.DailyCallsResetAt).UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		used = 0
	}
	return user.DailyCallLimit >= 0 && used >= 0 && used < user.DailyCallLimit
}
```

This check never saves the user. BE03 repeats the authoritative quota decision under row lock.

Create the runner context only after claim:

```go
runnerCtx, cancelRunner := context.WithTimeout(r.deps.rootContext, r.deps.upstreamTimeout)
defer cancelRunner()
registrationToken, err := r.deps.registry.Register(input.User.ID, input.GenerationID, cancelRunner)
if err != nil {
	_, _ = r.deps.store.FailRunningOwned(context.WithoutCancel(runnerCtx), input.User.ID, input.GenerationID, claim.LeaseToken, "internal_error", r.nowMillis())
	return PlatformSingleGenerationRunResult{}, ErrPlatformSingleGenerationUnavailable
}
defer r.deps.registry.Unregister(input.User.ID, input.GenerationID, registrationToken)
```

Do not derive `runnerCtx` from the HTTP request context.

Keep the prepared immutable values in one private value used by both streaming and persistence:

```go
type platformSingleRun struct {
	userID                   int64
	generationID             string
	model                    string
	userMessage              string
	conversationGUID         int64
	existingConversationGUID *int64
	reservedConversationGUID *int64
	leaseToken               string
	requestID                string
}

func (r *PlatformSingleGenerationRunner) nowMillis() int64 {
	return r.deps.now().UTC().UnixMilli()
}

func loadPlatformSingleTotalTokens(ctx context.Context, db *gorm.DB, userID int64) (int64, error) {
	if ctx == nil || db == nil || userID <= 0 {
		return 0, ErrPlatformSingleGenerationUnavailable
	}
	var user models.User
	if err := db.WithContext(ctx).Select("total_tokens_used").
		Where("id = ? AND status = ? AND is_deleted = 0", userID, models.UserStatusActive).
		First(&user).Error; err != nil || user.TotalTokensUsed < 0 {
		return 0, ErrPlatformSingleGenerationUnavailable
	}
	return user.TotalTokensUsed, nil
}
```

- [ ] **Step 4: Implement output detachment and upstream payload**

Use a single-owner sink that permanently disables output on the first write error:

```go
type platformSingleOutput struct {
	write    func([]byte) error
	detached bool
}

func (o *platformSingleOutput) emit(frame []byte) bool {
	if o.detached || len(frame) == 0 {
		return false
	}
	if err := o.write(frame); err != nil {
		o.detached = true
		return false
	}
	return true
}
```

Build the upstream payload through `whiteLabelPayload`, force `stream: true`, and force sanitized usage delivery:

```go
func platformSingleStreamingPayload(validated []byte, model string, messages []map[string]interface{}, temperature *float64, maxTokens *int) ([]byte, error) {
	body := whitelabel.ChatCompletionRequest{
		Model: model, Messages: toGatewayMessages(messages), Temperature: temperature, MaxTokens: maxTokens, Stream: true,
	}
	payload, err := whiteLabelPayload(validated, body)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	fields["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	return json.Marshal(fields)
}
```

The v2 request contract accepts client omission or `include_usage: true`; explicit `include_usage: false` is rejected before claim because BE05 requires an authoritative token total.

- [ ] **Step 5: Implement strict single-model chunk state**

Track exactly one choice at index zero, text-only deltas, one finish reason, one usage total, and one upstream `[DONE]`:

```go
type platformSingleChunkState struct {
	content      strings.Builder
	seq          int64
	modelEnded   bool
	usageSeen    bool
	totalTokens  int64
}

func (s *platformSingleChunkState) accept(chunk whitelabel.ChatCompletionChunk) (string, error) {
	if chunk.Usage != nil {
		if s.usageSeen || len(chunk.Choices) != 0 || chunk.Usage.TotalTokens < 0 {
			return "", ErrPlatformSingleGenerationUpstream
		}
		s.usageSeen = true
		s.totalTokens = int64(chunk.Usage.TotalTokens)
		return "", nil
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 || s.modelEnded {
		return "", ErrPlatformSingleGenerationUpstream
	}
	choice := chunk.Choices[0]
	if choice.Delta.Refusal != nil || len(choice.Delta.ToolCalls) != 0 {
		return "", ErrPlatformSingleGenerationUpstream
	}
	delta := ""
	if choice.Delta.Content != nil {
		delta = *choice.Delta.Content
	}
	if choice.FinishReason != nil {
		s.modelEnded = true
	}
	return delta, nil
}

func (s *platformSingleChunkState) complete() bool {
	return s.modelEnded && s.usageSeen && s.content.Len() > 0
}
```

Before accepting a non-empty delta, require `s.content.Len()+len(delta) <= platformGenerationMessageTextMaxBytes`. Then increment sequence, call `RecordDeltaOwned`, append to memory, and emit the BE01 delta frame. An empty delta changes no sequence.

- [ ] **Step 6: Implement commit and terminal SSE order**

After the typed consumer returns success and `chunkState.complete()` is true:

```go
_, err = r.deps.store.MarkModelDoneOwned(runnerCtx, run.userID, run.generationID, run.leaseToken, run.model, chunkState.seq, r.nowMillis())
if err != nil {
	return PlatformSingleGenerationRunResult{Started: true}, ErrPlatformSingleGenerationUnavailable
}
_, err = r.deps.store.BeginCommitOwned(runnerCtx, run.userID, run.generationID, run.leaseToken, r.nowMillis())
if err != nil {
	return PlatformSingleGenerationRunResult{Started: true}, ErrPlatformSingleGenerationUnavailable
}
output.emit(encoder.ModelDone(run.model, chunkState.seq))
persistenceInput := PlatformGenerationPersistenceInput{
	UserID: run.userID, GenerationID: run.generationID, Mode: PlatformGenerationModeSingle,
	Models: []string{run.model}, ConversationGUID: run.existingConversationGUID,
	ReservedConversationGUID: run.reservedConversationGUID, UserMessage: run.userMessage,
	Results: []PlatformGenerationPersistenceResult{{
		Model: run.model, State: PlatformGenerationStateCompleted, Content: chunkState.content.String(),
		Tokens: chunkState.totalTokens, Seq: chunkState.seq,
	}},
	NowMillis: r.nowMillis(),
}
receipt, err := r.deps.persistence.Finalize(runnerCtx, r.deps.db, persistenceInput)
if err != nil {
	return PlatformSingleGenerationRunResult{Started: true}, ErrPlatformSingleGenerationUnavailable
}
assistantGUIDs := map[string]string{run.model: receipt.Results[0].AssistantMessageGUID}
_, err = r.deps.store.Complete(runnerCtx, run.userID, run.generationID, assistantGUIDs, r.nowMillis())
if err != nil {
	return PlatformSingleGenerationRunResult{Started: true}, ErrPlatformSingleGenerationUnavailable
}
totalTokensUsed, err := r.deps.loadTotalTokens(runnerCtx, r.deps.db, run.userID)
if err != nil {
	return PlatformSingleGenerationRunResult{Started: true}, ErrPlatformSingleGenerationUnavailable
}
output.emit(encoder.DoneSingle(strconv.FormatInt(receipt.ConversationGUID, 10), receipt.Results[0].Tokens, totalTokensUsed))
return PlatformSingleGenerationRunResult{Started: true}, nil
```

`loadTotalTokens` reads the current active user's `total_tokens_used` after commit and returns an unavailable error instead of inventing a total. If the client is detached, execute the same stores and persistence while `output.emit` becomes a no-op. Task 6 replaces the temporary generic failure returns in this success-first slice with authority-aware terminal handling before the runner is exposed through HTTP.

- [ ] **Step 7: Run happy-path unit tests and commit**

Run:

```bash
go test ./internal/service -run 'TestPlatformSingleGeneration.*Success|TestPlatformSingleGeneration.*Duplicate|TestPlatformSingleGeneration.*Preflight' -count=1
go test -race ./internal/service -run 'TestPlatformSingleGeneration.*Success|TestPlatformSingleGeneration.*Duplicate' -count=1
git diff --check
```

Expected: PASS with exact dependency order and frame bytes.

Commit:

```bash
git add internal/service/platform_single_generation.go internal/service/platform_single_generation_test.go
git commit -m "feat(platform): run durable single v2 generations"
```

### Task 6: Complete renewal, disconnect, cancellation, failure, and commit-unknown behavior

**Files:**

- Modify: `internal/service/platform_single_generation.go`
- Modify: `internal/service/platform_single_generation_test.go`

- [ ] **Step 1: Add RED timing and failure matrix tests**

Use an injected timer factory and table-driven cases:

```go
type platformSingleFailure int

const (
	failTimeout platformSingleFailure = iota + 1
	failMalformed
	failEarlyEOF
	failOversize
	failCancel
	failShutdown
)

type platformSingleTimer interface {
	Chan() <-chan time.Time
	Stop()
}

type platformSingleTimerFactory func(time.Duration) platformSingleTimer

type platformSingleRealTimer struct {
	timer *time.Timer
}

func (t *platformSingleRealTimer) Chan() <-chan time.Time { return t.timer.C }
func (t *platformSingleRealTimer) Stop()                 { t.timer.Stop() }

func newPlatformSingleTimer(duration time.Duration) platformSingleTimer {
	return &platformSingleRealTimer{timer: time.NewTimer(duration)}
}

var cases = []struct {
	name             string
	failure          platformSingleFailure
	wantState        PlatformGenerationState
	wantCode         string
	wantPersistCalls int
	wantFrames       []string
}{
	{name: "upstream timeout", failure: failTimeout, wantState: PlatformGenerationStateFailed, wantCode: "timeout", wantFrames: []string{"meta", "model_error", "error"}},
	{name: "malformed chunk", failure: failMalformed, wantState: PlatformGenerationStateFailed, wantCode: "gateway_upstream_error", wantFrames: []string{"meta", "model_error", "error"}},
	{name: "early eof", failure: failEarlyEOF, wantState: PlatformGenerationStateFailed, wantCode: "gateway_upstream_error", wantFrames: []string{"meta", "model_error", "error"}},
	{name: "oversize", failure: failOversize, wantState: PlatformGenerationStateFailed, wantCode: "upstream_error", wantFrames: []string{"meta", "model_error", "error"}},
	{name: "explicit cancel", failure: failCancel, wantState: PlatformGenerationStateCancelled, wantCode: "cancelled", wantFrames: []string{"meta", "model_error", "error"}},
	{name: "shutdown", failure: failShutdown, wantState: PlatformGenerationStateFailed, wantCode: "internal_error", wantFrames: []string{"meta", "model_error", "error"}},
}
```

Extend `platformSingleGenerationDeps` with `newTimer platformSingleTimerFactory`, set it to `newPlatformSingleTimer` in the production constructor, and require it in dependency validation.

Every case asserts zero persistence calls, zero quota/token effects, partial content absent from store/errors/log capture, and exact stable frame codes when connected. Add separate tests for write failure/client cancellation continuing through persistence and GET-ready completion.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/service -run 'TestPlatformSingleGeneration.*Renew|TestPlatformSingleGeneration.*Disconnect|TestPlatformSingleGeneration.*Cancel|TestPlatformSingleGeneration.*Failure|TestPlatformSingleGeneration.*Shutdown|TestPlatformSingleGeneration.*CommitUnknown' -count=1
```

Expected: at least the renewal and failure cases fail because their orchestration is incomplete.

- [ ] **Step 3: Add a 10-second renewal loop with one serialized authority channel**

Start one renewal goroutine only after registry success. It reports once and exits on context cancellation or authority loss:

```go
type platformSingleRenewalResult struct {
	snapshot PlatformGenerationSnapshot
	err      error
}

func (r *PlatformSingleGenerationRunner) renew(
	ctx context.Context,
	input PlatformSingleGenerationInput,
	leaseToken string,
	results chan<- platformSingleRenewalResult,
) {
	timer := r.deps.newTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.Chan():
			snapshot, err := r.deps.store.RenewLease(ctx, input.User.ID, input.GenerationID, leaseToken, r.nowMillis())
			if err != nil || snapshot.State != PlatformGenerationStateRunning {
				select {
				case results <- platformSingleRenewalResult{snapshot: snapshot, err: err}:
				case <-ctx.Done():
				}
				return
			}
			timer.Stop()
			timer = r.deps.newTimer(10 * time.Second)
		}
	}
}
```

Inject `newTimer` in tests. The main runner selects between upstream completion, renewal authority loss, and `runnerCtx.Done()`. It cancels/closes the upstream response body before waiting for the consumer goroutine to exit.

- [ ] **Step 4: Map terminal causes without overwriting authority**

Use one cause classifier:

```go
func platformSingleStableCode(cause error, rootCtxErr error) string {
	switch {
	case rootCtxErr != nil:
		return "internal_error"
	case errors.Is(cause, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(cause, ErrPlatformSingleGenerationOversize):
		return "upstream_error"
	default:
		return "gateway_upstream_error"
	}
}
```

Before failing, reload/inspect an authoritative conflict snapshot:

- `cancelling`: call `AcknowledgeCancelledOwned`, then emit `cancelled` terminals if connected;
- terminal: stop and return the authority without mutation;
- `committing`: never call a running-fail transition; use receipt-aware reconciliation semantics;
- Redis unavailable or malformed: stop upstream, emit sanitized `internal_error` if the sink works, and leave BE04 lease convergence to recover;
- proven owned `running`: call `FailRunningOwned` with the classified stable code, then emit `model_error -> error`.

After `model_done`, a persistence/completion failure emits only global `error` because the model terminal was already sent. A validated receipt from BE03 commit-unknown handling is completed idempotently; an unresolved acknowledgement never emits `done`.

- [ ] **Step 5: Prove disconnect is output-only**

Use a sink whose second write returns `io.ErrClosedPipe` and cancel the original request context immediately. Assert:

```go
if upstream.cancelled.Load() {
	t.Fatal("client disconnect cancelled upstream")
}
if persistence.calls.Load() != 1 || store.finalState() != PlatformGenerationStateCompleted {
	t.Fatalf("disconnected run did not complete: persistence=%d state=%v", persistence.calls.Load(), store.finalState())
}
if got := sink.writeCalls.Load(); got != 2 {
	t.Fatalf("write calls after detachment = %d, want 2", got)
}
```

- [ ] **Step 6: Run the failure/race suite and commit**

Run:

```bash
go test ./internal/service -run 'TestPlatformSingleGeneration' -count=1
go test -race ./internal/service -run 'TestPlatformSingleGeneration' -count=1
git diff --check
```

Expected: all runner tests PASS, fake-clock tests use no wall-clock sleeps, and the race detector reports no issues.

Commit:

```bash
git add internal/service/platform_single_generation.go internal/service/platform_single_generation_test.go
git commit -m "feat(platform): harden single runner lifecycle"
```

### Task 7: Activate the single v2 HTTP contract

**Files:**

- Modify: `internal/handler/platform.go`
- Create: `internal/handler/platform_single_v2_test.go`
- Modify: `internal/handler/platform_v2_contract_test.go`
- Modify: `internal/router/router_test.go`

- [ ] **Step 1: Write RED exact HTTP/SSE tests**

Cover authenticated success, invalid body, missing/false stream, invalid UUID/model/message/conversation, unauthorized model, quota exhausted, dependency unavailable, duplicate conflict, cancellation tombstone, writer failure, and legacy/compare isolation.

The success assertion must compare an exact response body:

```go
want := "event: meta\n" +
	"data: {\"schema\":\"platform-chat-sse.v2\",\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"conversation_guid\":\"8101\",\"models\":[\"model-a\"]}\n\n" +
	"event: delta\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"model\":\"model-a\",\"seq\":1,\"delta\":\"hello\"}\n\n" +
	"event: model_done\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"model\":\"model-a\",\"last_seq\":1}\n\n" +
	"event: done\n" +
	"data: {\"generation_id\":\"550e8400-e29b-41d4-a716-446655440000\",\"status\":\"completed\",\"conversation_guid\":\"8101\",\"tokens\":3,\"total_tokens_used\":12}\n\n"
```

Assert headers exactly include `Content-Type: text/event-stream; charset=utf-8`, `Cache-Control: no-cache, no-transform`, and `X-Accel-Buffering: no`. Assert no `[DONE]`, raw provider ID/body, prompt, credential, or lease value appears.

- [ ] **Step 2: Run handler/router tests and verify RED**

Run:

```bash
go test ./internal/handler ./internal/router -run 'Test.*Platform.*Single.*V2|Test.*Platform.*V2.*Route' -count=1
```

Expected: single v2 still returns the BE01 stable `503` guard.

- [ ] **Step 3: Route exact single v2 requests to the runner**

Replace only the single-completion guard:

```go
if body.StreamVersion == platformSSEV2Version {
	platformSingleSSEV2(c, state, user, body)
	return
}
```

Extend `validatePlatformSSEV2Request` so a supplied `stream_options.include_usage: false` is rejected before claim while omission and `true` remain valid:

```go
var contract struct {
	Stream        bool    `json:"stream"`
	StreamVersion *string `json:"stream_version"`
	GenerationID  *string `json:"generation_id"`
	StreamOptions *struct {
		IncludeUsage *bool `json:"include_usage"`
	} `json:"stream_options"`
}
if err := json.Unmarshal(raw, &contract); err != nil || !contract.Stream || contract.StreamVersion == nil || *contract.StreamVersion != platformSSEV2Version || contract.GenerationID == nil || !isCanonicalUUID(*contract.GenerationID) ||
	(contract.StreamOptions != nil && contract.StreamOptions.IncludeUsage != nil && !*contract.StreamOptions.IncludeUsage) {
	return &whitelabel.Error{Code: whitelabel.CodeInvalidRequest, Status: http.StatusBadRequest, Type: whitelabel.TypeInvalidRequest}
}
```

Keep the compare guard unchanged:

```go
if body.StreamVersion == platformSSEV2Version {
	platformSSEV2Unavailable(c)
	return
}
```

The handler helper verifies `state.PlatformSingleGeneration != nil` and completes model authorization before calling the runner. Its closure installs BE01 headers lazily on the first frame, so pre-claim errors remain JSON:

```go
streamStarted := false
write := func(frame []byte) error {
	if !streamStarted {
		service.SetPlatformSSEV2Headers(c.Writer.Header())
		streamStarted = true
	}
	_, err := c.Writer.Write(frame)
	if err == nil {
		c.Writer.Flush()
	}
	return err
}
result, err := state.PlatformSingleGeneration.Run(service.PlatformSingleGenerationInput{
	User: user, GenerationID: body.GenerationID, RequestID: c.Writer.Header().Get("X-Request-ID"),
	Params: body.toParams(), Write: write,
})
```

It does not return a second JSON response after either `streamStarted` or `result.Started` is true. The runner sets `Started` immediately before attempting `meta`, including when that first write fails.

- [ ] **Step 4: Map pre-stream typed outcomes exactly**

Use a typed mapping with these public boundaries:

```go
switch {
case errors.Is(err, service.ErrPlatformSingleGenerationInvalid):
	platformWhiteLabelError(c, &whitelabel.Error{Code: whitelabel.CodeInvalidRequest, Status: http.StatusBadRequest, Type: whitelabel.TypeInvalidRequest})
case errors.Is(err, service.ErrPlatformSingleGenerationQuota):
	platformWhiteLabelError(c, &whitelabel.Error{Code: whitelabel.Code("rate_limited"), Status: http.StatusTooManyRequests, Type: whitelabel.TypeAPI})
case result.Duplicate != nil:
	view, getErr := state.PlatformGenerationControl.Get(c.Request.Context(), user.ID, body.GenerationID)
	if getErr != nil {
		platformSSEV2Unavailable(c)
		return
	}
	c.JSON(http.StatusConflict, view)
default:
	platformSSEV2Unavailable(c)
}
```

Define no second duplicate envelope: the `409` body is the same authenticated BE04 `PlatformGenerationView` returned by GET. This lets a completed duplicate include only receipt-hydrated content and keeps non-completed duplicates metadata-only. Do not introduce raw service error text.

- [ ] **Step 5: Run HTTP regression and commit**

Run:

```bash
go test ./internal/handler ./internal/router -count=1
go test -race ./internal/handler ./internal/router -run 'Test.*Platform.*Single.*V2|Test.*Platform.*Generation' -count=1
git diff --check
```

Expected: PASS; single v2 is active, compare v2 remains controlled `503`, and legacy tests retain byte-compatible behavior.

Commit:

```bash
git add internal/handler/platform.go internal/handler/platform_single_v2_test.go internal/handler/platform_v2_contract_test.go internal/router/router_test.go
git commit -m "feat(platform): activate single v2 stream route"
```

### Task 8: Assemble the runner and enforce shutdown order

**Files:**

- Modify: `internal/app/state.go`
- Modify: `internal/app/state_test.go`

- [ ] **Step 1: Write RED construction and close-order tests**

Add constructor injection for the runner, then assert it is present only with DB, Redis store, persistence, cancellation registry, and white-label upstream. Add cleanup tests for constructor failure at each boundary.

Record close order with callbacks and require:

```go
wantOrder := []string{"reject-and-cancel-runners", "stop-converger", "close-generation-redis", "close-auth-redis"}
```

The runner-close callback must block until a registered fake runner unregisters, and the timeout case must still proceed to stop the worker and close clients while returning a joined runner-close error.

- [ ] **Step 2: Run app tests and verify RED**

Run:

```bash
go test ./internal/app -run 'Test.*PlatformSingleGeneration|TestStateClose.*Generation' -count=1
```

Expected: build/test failure because State has no runner construction or close hook.

- [ ] **Step 3: Add application root context and runner construction**

Extend State and constructors:

```go
var errClosePlatformGenerationRunners = errors.New("close platform generation runners")

type State struct {
	PlatformSingleGeneration *service.PlatformSingleGenerationRunner
	platformGenerationRootCancel context.CancelFunc
	closePlatformGenerationRunners func(context.Context, *service.PlatformGenerationCancellationRegistry) error
}
```

During `newState`, create one root context before runner construction:

```go
generationRootContext, generationRootCancel := context.WithCancel(context.Background())
s.platformGenerationRootCancel = generationRootCancel
```

Construct the runner only after store, persistence, registry, DB, and white-label service exist, using:

```go
timeout := time.Duration(settings.UpstreamTimeoutSeconds * float64(time.Second))
runner, err := service.NewPlatformSingleGenerationRunner(
	db, generations, generationPersistence, generationCancellations,
	s.WhiteLabel, generationRootContext, timeout,
)
```

Reject nonpositive/overflow timeout in the runner constructor. Partial construction cancels the root context and closes already-owned dependencies in the same safe order.

- [ ] **Step 4: Close runners before the worker and Redis**

At the start of `State.Close`, prevent new registrations and cancel active work:

```go
if s.platformGenerationRootCancel != nil {
	s.platformGenerationRootCancel()
}
if s.PlatformGenerationCancellations != nil {
	closeRunners := s.closePlatformGenerationRunners
	if closeRunners == nil {
		closeRunners = func(ctx context.Context, registry *service.PlatformGenerationCancellationRegistry) error {
			return registry.CloseAndWait(ctx)
		}
	}
	if err := closeRunners(ctx, s.PlatformGenerationCancellations); err != nil {
		s.closeErr = errors.Join(s.closeErr, errClosePlatformGenerationRunners)
	}
}
```

Then retain BE04 converger close, generation Redis close, and auth Redis close. Every runner observes root cancellation as shutdown and attempts `failed/internal_error` while Redis remains open. Repeated `Close` remains guarded by `closeOnce`.

- [ ] **Step 5: Run app lifecycle/race tests and commit**

Run:

```bash
go test ./internal/app -count=1
go test -race ./internal/app -count=1
git diff --check
```

Expected: PASS with exact close ordering and no leak/race report.

Commit:

```bash
git add internal/app/state.go internal/app/state_test.go
git commit -m "feat(platform): manage single runners in app lifecycle"
```

### Task 9: Prove real Redis/MySQL behavior and archive delivery evidence

**Files:**

- Create: `internal/service/platform_single_generation_integration_test.go`
- Modify: `progress.md`
- Modify: `feature_list.json`
- Create: `docs/superpowers/reports/2026-09-10-platform-single-stream-v2.md`

- [ ] **Step 1: Add explicit real-fixture integration tests**

Gate the suite on both `TEST_DATABASE_URL` and `TEST_REDIS_URL`; print a named skip if either is absent. Use unique user/generation/conversation GUIDs and owned Redis prefixes so cleanup is exact.

Required test names:

```go
func TestPlatformSingleGenerationIntegrationNewConversationSuccess(t *testing.T)
func TestPlatformSingleGenerationIntegrationExistingConversationSuccess(t *testing.T)
func TestPlatformSingleGenerationIntegrationDisconnectThenGET(t *testing.T)
func TestPlatformSingleGenerationIntegrationCancelNoPersistence(t *testing.T)
func TestPlatformSingleGenerationIntegrationRenewalBeatsConverger(t *testing.T)
func TestPlatformSingleGenerationIntegrationCommitUnknownReconciles(t *testing.T)
func TestPlatformSingleGenerationIntegrationConcurrentQuotaAndDuplicateRaces(t *testing.T)
func TestPlatformSingleGenerationIntegrationFailureMatrixHasNoDurableContent(t *testing.T)
```

Each success test asserts one receipt, exact message graph, one usage row, one daily call, token-total equality, requested-existing provenance, and GET-hydrated final content. Each non-success test snapshots counts before/after and asserts no conversation/message/usage/receipt/quota/token delta.

- [ ] **Step 2: Run focused normal and race suites against isolated fixtures**

Run after creating a loopback-only disposable MySQL 8.4 and Redis 7 fixture with the current migration ledger `0001` through `0013` (including BE03 receipt migration `0011`):

```bash
go test -p 1 ./internal/service ./internal/handler ./internal/app ./internal/router -run 'TestPlatformSingleGeneration|Test.*Platform.*Single.*V2' -count=1
go test -race -p 1 ./internal/service ./internal/handler ./internal/app ./internal/router -run 'TestPlatformSingleGeneration|Test.*Platform.*Single.*V2' -count=1
```

Expected: all selected tests PASS, zero selected fixture skips, and no race report.

- [ ] **Step 3: Run full regression and static gates**

Run from a fresh fixture state when full tests share Redis/MySQL:

```bash
go test -p 1 ./... -count=1
go test -race -p 1 ./internal/service ./internal/handler ./internal/app ./internal/router ./internal/whitelabel -count=1
go build ./...
go vet ./...
git diff --check
```

Expected: every command exits zero. Record every skip by exact test name and reason; BE05 fixture tests must have zero skips.

- [ ] **Step 4: Run privacy and scope scans**

Use unique sentinels in tests, then run:

```bash
rg -n 'SENSITIVE_BE05_PROMPT|SENSITIVE_BE05_REPLY|SENSITIVE_BE05_LEASE|SENSITIVE_BE05_UPSTREAM' docs/superpowers/reports/2026-09-10-platform-single-stream-v2.md progress.md feature_list.json
git diff --name-only origin/main..HEAD
git diff --check origin/main..HEAD
```

Expected: the sentinel scan has no matches; changed paths stay within the file map plus test evidence. Do not include fixture credentials, Redis URLs/keys, SQL, raw provider bodies, or lease material in the report.

- [ ] **Step 5: Update status without overstating `go-018`**

Add a dated BE05 entry to `progress.md` and append evidence to `go-018`. Keep:

```json
"status": "in_progress"
```

The notes must say BE01-BE05 are locally complete, while BE06 compare streaming, frontend/backend joint acceptance, production migration, deployment, public HTTPS verification, and real upstream calls are not run.

The report must include candidate commit, fixture versions, migration ledger, exact commands/results, skip inventory, prior failed attempts with classification, privacy checks, cleanup evidence, and scope exclusions.

- [ ] **Step 6: Commit evidence and tracker updates**

Run:

```bash
git diff --check
git status --short
```

Expected: only the report, `progress.md`, `feature_list.json`, and integration test changes remain uncommitted.

Commit:

```bash
git add internal/service/platform_single_generation_integration_test.go progress.md feature_list.json docs/superpowers/reports/2026-09-10-platform-single-stream-v2.md
git commit -m "docs(platform): record single v2 stream evidence"
```

## Per-task review gate

After each implementation commit:

1. Run a specification review against the exact task and the approved design.
2. Repair every specification gap before quality review.
3. Run a code-quality/security review for races, context lifetime, ownership, persistence, privacy, and regression risk.
4. Repair every material finding and rerun the task's focused tests.
5. Record the reviewed commit hash before moving to the next task.

Do not combine specification and quality approval into one verdict. A task advances only after both gates pass on the same code snapshot.

## Final completion boundary

BE05 can be reported complete only after Tasks 1-9, all review gates, real fixture tests, full/race/build/vet/diff/privacy checks, evidence updates, and exact fixture cleanup pass. This plan does not authorize push, PR creation, merge, production migration, deployment, frontend changes, compare v2 activation, public HTTPS acceptance, or a real paid upstream call.
