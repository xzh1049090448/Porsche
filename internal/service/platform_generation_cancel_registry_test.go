package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const cancellationRegistryGenerationID = "c0a8012e-ef48-4a5d-9ca7-9a78d055e7f6"

func TestPlatformGenerationCancellationRegistryCloseCancelsAndDrains(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	cancelled := make(chan int64, 2)
	firstToken, err := registry.Register(7, cancellationRegistryGenerationID, func() { cancelled <- 7 })
	if err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	secondToken, err := registry.Register(8, cancellationRegistryGenerationID, func() { cancelled <- 8 })
	if err != nil {
		t.Fatalf("second Register() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- registry.CloseAndWait(ctx) }()
	cancelledUsers := make(map[int64]bool, 2)
	for range 2 {
		select {
		case userID := <-cancelled:
			cancelledUsers[userID] = true
		case <-time.After(time.Second):
			t.Fatal("CloseAndWait() did not cancel all active registrations")
		}
	}
	if !cancelledUsers[7] || !cancelledUsers[8] {
		t.Fatalf("cancelled users = %#v, want 7 and 8", cancelledUsers)
	}
	if _, err := registry.Register(9, cancellationRegistryGenerationID, func() {}); !errors.Is(err, ErrPlatformGenerationUnavailable) {
		t.Fatalf("Register after close error = %v, want ErrPlatformGenerationUnavailable", err)
	}
	if !registry.Unregister(7, cancellationRegistryGenerationID, firstToken) {
		t.Fatal("Unregister first active runner = false")
	}
	select {
	case err := <-done:
		t.Fatalf("CloseAndWait() returned before all runners drained: %v", err)
	default:
	}
	if !registry.Unregister(8, cancellationRegistryGenerationID, secondToken) {
		t.Fatal("Unregister second active runner = false")
	}
	if err := <-done; err != nil {
		t.Fatalf("CloseAndWait() error = %v", err)
	}
}

func TestPlatformGenerationCancellationRegistryCloseCallbacksRunOutsideMutex(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var token string
	unregistered := make(chan bool, 1)
	var err error
	token, err = registry.Register(7, cancellationRegistryGenerationID, func() {
		unregistered <- registry.Unregister(7, cancellationRegistryGenerationID, token)
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := registry.CloseAndWait(ctx); err != nil {
		t.Fatalf("CloseAndWait() error = %v", err)
	}
	if ok := <-unregistered; !ok {
		t.Fatal("callback Unregister() = false, want true")
	}
}

func TestPlatformGenerationCancellationRegistryDrainTimeoutKeepsLiveEntry(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var calls atomic.Int64
	token, err := registry.Register(7, cancellationRegistryGenerationID, func() { calls.Add(1) })
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := registry.CloseAndWait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseAndWait() error = %v, want context.DeadlineExceeded", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls = %d, want 1", got)
	}
	if !registry.Unregister(7, cancellationRegistryGenerationID, token) {
		t.Fatal("Unregister after timeout = false, live entry was deleted")
	}
	if err := registry.CloseAndWait(context.Background()); err != nil {
		t.Fatalf("repeated CloseAndWait() after drain error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls after repeated close = %d, want 1", got)
	}
}

func TestPlatformGenerationCancellationRegistryCloseDoesNotRepeatExplicitCancel(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var calls atomic.Int64
	token, err := registry.Register(7, cancellationRegistryGenerationID, func() { calls.Add(1) })
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if !registry.Cancel(7, cancellationRegistryGenerationID) {
		t.Fatal("first Cancel() = false, want true")
	}
	if registry.Cancel(7, cancellationRegistryGenerationID) {
		t.Fatal("second Cancel() = true, want false")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- registry.CloseAndWait(ctx) }()
	if !registry.Unregister(7, cancellationRegistryGenerationID, token) {
		t.Fatal("Unregister() = false, want true")
	}
	if err := <-done; err != nil {
		t.Fatalf("CloseAndWait() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls = %d, want 1", got)
	}
}

func TestPlatformGenerationCancellationRegistryCloseSupportsZeroValueAndEmptyRegistry(t *testing.T) {
	var registry PlatformGenerationCancellationRegistry
	if err := registry.CloseAndWait(context.Background()); err != nil {
		t.Fatalf("zero-value CloseAndWait() error = %v", err)
	}
	if _, err := registry.Register(7, cancellationRegistryGenerationID, func() {}); !errors.Is(err, ErrPlatformGenerationUnavailable) {
		t.Fatalf("zero-value Register after close error = %v, want ErrPlatformGenerationUnavailable", err)
	}

	var nilRegistry *PlatformGenerationCancellationRegistry
	if err := nilRegistry.CloseAndWait(context.Background()); !errors.Is(err, ErrPlatformGenerationUnavailable) {
		t.Fatalf("nil CloseAndWait() error = %v, want ErrPlatformGenerationUnavailable", err)
	}
	if err := NewPlatformGenerationCancellationRegistry().CloseAndWait(nil); !errors.Is(err, ErrPlatformGenerationUnavailable) {
		t.Fatalf("nil-context CloseAndWait() error = %v, want ErrPlatformGenerationUnavailable", err)
	}
}

func TestPlatformGenerationCancellationRegistryRegistrationTokenUsesSequentialRawURLEncoding(t *testing.T) {
	raw := make([]byte, 64)
	for index := range raw {
		raw[index] = byte(index)
	}
	reader := bytes.NewReader(raw)

	first, err := newPlatformGenerationCancellationRegistrationTokenFrom(reader)
	if err != nil {
		t.Fatalf("first token error = %v", err)
	}
	second, err := newPlatformGenerationCancellationRegistrationTokenFrom(reader)
	if err != nil {
		t.Fatalf("second token error = %v", err)
	}
	if want := base64.RawURLEncoding.EncodeToString(raw[:32]); first != want {
		t.Fatalf("first token = %q, want %q", first, want)
	}
	if want := base64.RawURLEncoding.EncodeToString(raw[32:]); second != want {
		t.Fatalf("second token = %q, want %q", second, want)
	}
	if len(first) != 43 || len(second) != 43 {
		t.Fatalf("token lengths = %d, %d, want 43, 43", len(first), len(second))
	}
	const rawURLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, token := range []string{first, second} {
		for _, character := range token {
			if !strings.ContainsRune(rawURLAlphabet, character) {
				t.Fatalf("token %q contains non-RawURL character %q", token, character)
			}
		}
	}
	if first == second {
		t.Fatal("sequential tokens are equal")
	}
}

func TestPlatformGenerationCancellationRegistryNilAndZeroValueReceivers(t *testing.T) {
	var nilRegistry *PlatformGenerationCancellationRegistry
	if token, err := nilRegistry.Register(1, cancellationRegistryGenerationID, func() {}); token != "" || err != ErrPlatformGenerationUnavailable {
		t.Fatalf("nil Register() = (%q, %v), want empty token and ErrPlatformGenerationUnavailable", token, err)
	}
	if nilRegistry.Cancel(1, cancellationRegistryGenerationID) {
		t.Fatal("nil Cancel() = true, want false")
	}
	if nilRegistry.Unregister(1, cancellationRegistryGenerationID, "") {
		t.Fatal("nil Unregister() = true, want false")
	}

	var zeroValue PlatformGenerationCancellationRegistry
	var calls atomic.Int64
	token, err := zeroValue.Register(1, cancellationRegistryGenerationID, func() { calls.Add(1) })
	if err != nil || token == "" {
		t.Fatalf("zero-value Register() = (%q, %v), want nonempty token and nil error", token, err)
	}
	if !zeroValue.Cancel(1, cancellationRegistryGenerationID) {
		t.Fatal("zero-value Cancel() = false, want true")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("zero-value callback calls = %d, want 1", got)
	}
	if !zeroValue.Unregister(1, cancellationRegistryGenerationID, token) {
		t.Fatal("zero-value Unregister() = false, want true")
	}
}

func TestPlatformGenerationCancellationRegistryRejectsInvalidRegistrationWithoutMutation(t *testing.T) {
	registry := newPlatformGenerationCancellationRegistryFrom(errorReader{})

	for _, test := range []struct {
		name         string
		userID       int64
		generationID string
		cancel       func()
	}{
		{name: "zero user", userID: 0, generationID: cancellationRegistryGenerationID, cancel: func() {}},
		{name: "noncanonical UUID", userID: 1, generationID: "C0A8012E-EF48-4A5D-9CA7-9A78D055E7F6", cancel: func() {}},
		{name: "nil callback", userID: 1, generationID: cancellationRegistryGenerationID},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := registry.Register(test.userID, test.generationID, test.cancel); !errors.Is(err, ErrPlatformGenerationInvalid) {
				t.Fatalf("Register() error = %v, want ErrPlatformGenerationInvalid", err)
			}
			if len(registry.entries) != 0 {
				t.Fatalf("registry was mutated: %#v", registry.entries)
			}
		})
	}
}

func TestPlatformGenerationCancellationRegistryRegisterReturnsTokenAndPreservesDuplicate(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var originalCalls atomic.Int64
	var duplicateCalls atomic.Int64

	token, err := registry.Register(1, cancellationRegistryGenerationID, func() { originalCalls.Add(1) })
	if err != nil || token == "" {
		t.Fatalf("Register() = (%q, %v), want nonempty token and nil error", token, err)
	}
	duplicateToken, err := registry.Register(1, cancellationRegistryGenerationID, func() { duplicateCalls.Add(1) })
	if duplicateToken != "" || !errors.Is(err, ErrPlatformGenerationConflict) {
		t.Fatalf("duplicate Register() = (%q, %v), want empty token and ErrPlatformGenerationConflict", duplicateToken, err)
	}
	if !registry.Cancel(1, cancellationRegistryGenerationID) {
		t.Fatal("Cancel() = false, want true")
	}
	if got := originalCalls.Load(); got != 1 {
		t.Fatalf("original callback calls = %d, want 1", got)
	}
	if got := duplicateCalls.Load(); got != 0 {
		t.Fatalf("duplicate callback calls = %d, want 0", got)
	}
}

func TestPlatformGenerationCancellationRegistryDuplicateWinsBeforeExhaustedEntropy(t *testing.T) {
	registry := newPlatformGenerationCancellationRegistryFrom(bytes.NewReader(make([]byte, platformGenerationCancellationRegistrationTokenBytes)))
	var originalCalls atomic.Int64
	token, err := registry.Register(1, cancellationRegistryGenerationID, func() { originalCalls.Add(1) })
	if err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	if duplicateToken, err := registry.Register(1, cancellationRegistryGenerationID, func() {}); duplicateToken != "" || !errors.Is(err, ErrPlatformGenerationConflict) {
		t.Fatalf("duplicate Register() with exhausted entropy = (%q, %v), want empty token and ErrPlatformGenerationConflict", duplicateToken, err)
	}
	if !registry.Cancel(1, cancellationRegistryGenerationID) {
		t.Fatal("Cancel() original registration = false, want true")
	}
	if got := originalCalls.Load(); got != 1 {
		t.Fatalf("original callback calls = %d, want 1", got)
	}
	if !registry.Unregister(1, cancellationRegistryGenerationID, token) {
		t.Fatal("Unregister() original token = false, want true")
	}
}

func TestPlatformGenerationCancellationRegistryConcurrentRegisterProtectsInjectedReader(t *testing.T) {
	registry := newPlatformGenerationCancellationRegistryFrom(&nonThreadSafeDeterministicReader{})

	const registrations = 64
	start := make(chan struct{})
	results := make(chan cancellationRegistryRegistrationResult, registrations)
	var workers sync.WaitGroup
	workers.Add(registrations)
	for userID := int64(1); userID <= registrations; userID++ {
		go func(userID int64) {
			defer workers.Done()
			<-start
			token, err := registry.Register(userID, cancellationRegistryGenerationID, func() {})
			results <- cancellationRegistryRegistrationResult{token: token, err: err}
		}(userID)
	}
	close(start)
	workers.Wait()
	close(results)

	tokens := make(map[string]struct{}, registrations)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Register() error = %v", result.err)
		}
		if !validPlatformGenerationCancellationRegistrationToken(result.token) {
			t.Fatalf("concurrent Register() token = %q, want canonical 32-byte RawURL token", result.token)
		}
		tokens[result.token] = struct{}{}
	}
	if len(tokens) != registrations {
		t.Fatalf("unique registration tokens = %d, want %d", len(tokens), registrations)
	}
	if len(registry.entries) != registrations {
		t.Fatalf("registry entries = %d, want %d", len(registry.entries), registrations)
	}
}

func TestPlatformGenerationCancellationRegistryConcurrentSameKeyHasOneWinner(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()

	const registrations = 64
	start := make(chan struct{})
	results := make(chan cancellationRegistryRegistrationResult, registrations)
	var workers sync.WaitGroup
	workers.Add(registrations)
	for range registrations {
		go func() {
			defer workers.Done()
			<-start
			token, err := registry.Register(1, cancellationRegistryGenerationID, func() {})
			results <- cancellationRegistryRegistrationResult{token: token, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	var successes int
	for result := range results {
		if result.err == nil && validPlatformGenerationCancellationRegistrationToken(result.token) {
			successes++
			continue
		}
		if result.token != "" || !errors.Is(result.err, ErrPlatformGenerationConflict) {
			t.Fatalf("concurrent same-key Register() = (%q, %v), want empty token and ErrPlatformGenerationConflict", result.token, result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("same-key registration successes = %d, want 1", successes)
	}
}

func TestPlatformGenerationCancellationRegistryCancelInvokesCallbackOnce(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var calls atomic.Int64
	if _, err := registry.Register(1, cancellationRegistryGenerationID, func() { calls.Add(1) }); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if !registry.Cancel(1, cancellationRegistryGenerationID) {
		t.Fatal("first Cancel() = false, want true")
	}
	if registry.Cancel(1, cancellationRegistryGenerationID) {
		t.Fatal("second Cancel() = true, want false")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls = %d, want 1", got)
	}
}

func TestPlatformGenerationCancellationRegistryCancelInvokesCallbackOutsideMutex(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var token string
	callbackDone := make(chan bool, 1)
	var err error
	token, err = registry.Register(1, cancellationRegistryGenerationID, func() {
		callbackDone <- registry.Unregister(1, cancellationRegistryGenerationID, token)
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cancelDone := make(chan bool, 1)
	go func() { cancelDone <- registry.Cancel(1, cancellationRegistryGenerationID) }()

	select {
	case cancelled := <-cancelDone:
		if !cancelled {
			t.Fatal("Cancel() = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("Cancel() deadlocked while callback called Unregister()")
	}
	select {
	case unregistered := <-callbackDone:
		if !unregistered {
			t.Fatal("callback Unregister() = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("callback did not complete")
	}
}

func TestPlatformGenerationCancellationRegistryUnregisterComparesTokenBeforeDelete(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var replacementCalls atomic.Int64
	token, err := registry.Register(1, cancellationRegistryGenerationID, func() {})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if registry.Unregister(1, cancellationRegistryGenerationID, "wrong-token") {
		t.Fatal("Unregister() with wrong token = true, want false")
	}
	if !registry.Unregister(1, cancellationRegistryGenerationID, token) {
		t.Fatal("Unregister() with correct token = false, want true")
	}
	replacementToken, err := registry.Register(1, cancellationRegistryGenerationID, func() { replacementCalls.Add(1) })
	if err != nil {
		t.Fatalf("replacement Register() error = %v", err)
	}
	if registry.Unregister(1, cancellationRegistryGenerationID, token) {
		t.Fatal("Unregister() with stale token = true, want false")
	}
	if !registry.Cancel(1, cancellationRegistryGenerationID) {
		t.Fatal("Cancel() replacement = false, want true")
	}
	if got := replacementCalls.Load(); got != 1 {
		t.Fatalf("replacement callback calls = %d, want 1", got)
	}
	if !registry.Unregister(1, cancellationRegistryGenerationID, replacementToken) {
		t.Fatal("Unregister() after Cancel() = false, want true")
	}
}

func TestPlatformGenerationCancellationRegistryUnregisterBeforeCancelPreventsInvocation(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var calls atomic.Int64
	token, err := registry.Register(1, cancellationRegistryGenerationID, func() { calls.Add(1) })
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if !registry.Unregister(1, cancellationRegistryGenerationID, token) {
		t.Fatal("Unregister() = false, want true")
	}
	if registry.Cancel(1, cancellationRegistryGenerationID) {
		t.Fatal("Cancel() = true, want false")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("callback calls = %d, want 0", got)
	}
}

func TestPlatformGenerationCancellationRegistryIsolatesUsers(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var firstCalls atomic.Int64
	var secondCalls atomic.Int64
	if _, err := registry.Register(1, cancellationRegistryGenerationID, func() { firstCalls.Add(1) }); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	if _, err := registry.Register(2, cancellationRegistryGenerationID, func() { secondCalls.Add(1) }); err != nil {
		t.Fatalf("second Register() error = %v", err)
	}
	if !registry.Cancel(1, cancellationRegistryGenerationID) {
		t.Fatal("Cancel() first user = false, want true")
	}
	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("first callback calls = %d, want 1", got)
	}
	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("second callback calls before cancel = %d, want 0", got)
	}
	if !registry.Cancel(2, cancellationRegistryGenerationID) {
		t.Fatal("Cancel() second user = false, want true")
	}
	if got := secondCalls.Load(); got != 1 {
		t.Fatalf("second callback calls = %d, want 1", got)
	}
}

func TestPlatformGenerationCancellationRegistryConcurrentCancelInvokesOnce(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var calls atomic.Int64
	if _, err := registry.Register(1, cancellationRegistryGenerationID, func() { calls.Add(1) }); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	const callers = 64
	var started sync.WaitGroup
	started.Add(callers)
	var finished sync.WaitGroup
	finished.Add(callers)
	results := make(chan bool, callers)
	for range callers {
		go func() {
			defer finished.Done()
			started.Done()
			results <- registry.Cancel(1, cancellationRegistryGenerationID)
		}()
	}
	started.Wait()
	finished.Wait()
	close(results)
	var successes int
	for cancelled := range results {
		if cancelled {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful Cancel() calls = %d, want 1", successes)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls = %d, want 1", got)
	}
}

func TestPlatformGenerationCancellationRegistryConcurrentCancelAndUnregisterHasLegalOutcome(t *testing.T) {
	registry := NewPlatformGenerationCancellationRegistry()
	var calls atomic.Int64
	token, err := registry.Register(1, cancellationRegistryGenerationID, func() { calls.Add(1) })
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	start := make(chan struct{})
	cancelled := make(chan bool, 1)
	unregistered := make(chan bool, 1)
	go func() { <-start; cancelled <- registry.Cancel(1, cancellationRegistryGenerationID) }()
	go func() { <-start; unregistered <- registry.Unregister(1, cancellationRegistryGenerationID, token) }()
	close(start)
	var cancelResult, unregisterResult bool
	select {
	case cancelResult = <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("concurrent Cancel() deadlocked")
	}
	select {
	case unregisterResult = <-unregistered:
	case <-time.After(time.Second):
		t.Fatal("concurrent Unregister() deadlocked")
	}
	if !unregisterResult {
		t.Fatal("Unregister() = false, want true for registered token")
	}
	if cancelResult && calls.Load() != 1 {
		t.Fatalf("Cancel() succeeded but callback calls = %d, want 1", calls.Load())
	}
	if !cancelResult && calls.Load() != 0 {
		t.Fatalf("Cancel() failed but callback calls = %d, want 0", calls.Load())
	}
	if len(registry.entries) != 0 {
		t.Fatalf("registry leaked entries: %#v", registry.entries)
	}
}

func TestPlatformGenerationCancellationRegistryEntropyFailureLeavesRegistryUnchanged(t *testing.T) {
	registry := newPlatformGenerationCancellationRegistryFrom(errorReader{})
	if token, err := registry.Register(1, cancellationRegistryGenerationID, func() {}); token != "" || !errors.Is(err, ErrPlatformGenerationUnavailable) {
		t.Fatalf("Register() = (%q, %v), want empty token and ErrPlatformGenerationUnavailable", token, err)
	}
	if len(registry.entries) != 0 {
		t.Fatalf("registry was mutated after entropy failure: %#v", registry.entries)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

type cancellationRegistryRegistrationResult struct {
	token string
	err   error
}

type nonThreadSafeDeterministicReader struct {
	next uint64
}

func (r *nonThreadSafeDeterministicReader) Read(raw []byte) (int, error) {
	sequence := r.next
	r.next++
	for index := range raw {
		raw[index] = byte(sequence + uint64(index))
	}
	return len(raw), nil
}
