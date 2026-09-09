package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type platformGenerationConvergerFakeTicker struct {
	c       chan time.Time
	stopped atomic.Bool
}

func newPlatformGenerationConvergerFakeTicker() *platformGenerationConvergerFakeTicker {
	return &platformGenerationConvergerFakeTicker{c: make(chan time.Time, 8)}
}

func (t *platformGenerationConvergerFakeTicker) C() <-chan time.Time { return t.c }
func (t *platformGenerationConvergerFakeTicker) Stop()               { t.stopped.Store(true) }

func TestPlatformGenerationConvergerRunPassContinuesCursorAndProcessesSerially(t *testing.T) {
	var scanCursors []uint64
	var active atomic.Int32
	var maximum atomic.Int32
	var processed []int64
	w := &PlatformGenerationConverger{
		scan: func(_ context.Context, cursor uint64, count int64) ([]PlatformGenerationIdentity, uint64, error) {
			scanCursors = append(scanCursors, cursor)
			if cursor == 0 {
				if count != platformGenerationConvergerMaxKeys {
					t.Fatalf("first scan count = %d", count)
				}
				return []PlatformGenerationIdentity{{UserID: 1, GenerationID: generationTestID}}, 41, nil
			}
			if count != platformGenerationConvergerMaxKeys-1 {
				t.Fatalf("second scan count = %d", count)
			}
			return []PlatformGenerationIdentity{{UserID: 2, GenerationID: generationTestID}}, 0, nil
		},
		converge: func(_ context.Context, identity PlatformGenerationIdentity, nowMillis int64) error {
			current := active.Add(1)
			defer active.Add(-1)
			if current > maximum.Load() {
				maximum.Store(current)
			}
			if nowMillis != 1_000 {
				t.Fatalf("nowMillis = %d", nowMillis)
			}
			processed = append(processed, identity.UserID)
			return nil
		},
		elapsedNow: time.Now,
		nowMillis:  func() int64 { return 1_000 },
		maxKeys:    platformGenerationConvergerMaxKeys,
		budget:     platformGenerationConvergerBudget,
	}

	if err := w.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(scanCursors) != 2 || scanCursors[0] != 0 || scanCursors[1] != 41 {
		t.Fatalf("scan cursors = %v", scanCursors)
	}
	if len(processed) != 2 || processed[0] != 1 || processed[1] != 2 {
		t.Fatalf("processed = %v", processed)
	}
	if maximum.Load() != 1 || w.currentCursor() != 0 {
		t.Fatalf("maximum/cursor = %d/%d", maximum.Load(), w.currentCursor())
	}
}

func TestPlatformGenerationConvergerRunPassIsBoundedByKeys(t *testing.T) {
	ids := make([]PlatformGenerationIdentity, 600)
	for i := range ids {
		ids[i] = PlatformGenerationIdentity{UserID: int64(i + 1), GenerationID: generationTestID}
	}
	processed := 0
	w := &PlatformGenerationConverger{
		scan: func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) {
			return ids, 77, nil
		},
		converge: func(context.Context, PlatformGenerationIdentity, int64) error {
			processed++
			return nil
		},
		elapsedNow: time.Now,
		nowMillis:  func() int64 { return 1_000 },
		maxKeys:    platformGenerationConvergerMaxKeys,
		budget:     platformGenerationConvergerBudget,
	}

	if err := w.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if processed != platformGenerationConvergerMaxKeys || w.currentCursor() != 77 {
		t.Fatalf("processed/cursor = %d/%d", processed, w.currentCursor())
	}
}

func TestPlatformGenerationConvergerRunPassIsBoundedByElapsedBudget(t *testing.T) {
	var elapsedCalls int
	times := []time.Time{
		time.Unix(0, 0),
		time.Unix(0, 0),
		time.Unix(0, 0),
		time.Unix(0, int64(101*time.Millisecond)),
	}
	scans := 0
	processed := 0
	w := &PlatformGenerationConverger{
		scan: func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) {
			scans++
			return []PlatformGenerationIdentity{{UserID: int64(scans), GenerationID: generationTestID}}, uint64(scans), nil
		},
		converge: func(context.Context, PlatformGenerationIdentity, int64) error {
			processed++
			return nil
		},
		elapsedNow: func() time.Time {
			value := times[elapsedCalls]
			if elapsedCalls < len(times)-1 {
				elapsedCalls++
			}
			return value
		},
		nowMillis: func() int64 { return 1_000 },
		maxKeys:   platformGenerationConvergerMaxKeys,
		budget:    platformGenerationConvergerBudget,
	}

	if err := w.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scans != 1 || processed != 1 || w.currentCursor() != 1 {
		t.Fatalf("scans/processed/cursor = %d/%d/%d", scans, processed, w.currentCursor())
	}
}

func TestPlatformGenerationConvergerRunPassContinuesBenignRecordErrors(t *testing.T) {
	benign := []error{
		ErrPlatformGenerationConflict,
		ErrPlatformGenerationPersistenceConflict,
		ErrPlatformGenerationPersistenceUnavailable,
		ErrPlatformGenerationUnavailable,
		ErrPlatformGenerationControlUnavailable,
		ErrPlatformGenerationNotFound,
	}
	processed := 0
	w := newPlatformGenerationConvergerTestWorker(
		func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) {
			ids := make([]PlatformGenerationIdentity, len(benign)+1)
			for i := range ids {
				ids[i] = PlatformGenerationIdentity{UserID: int64(i + 1), GenerationID: generationTestID}
			}
			return ids, 0, nil
		},
		func(context.Context, PlatformGenerationIdentity, int64) error {
			processed++
			if processed <= len(benign) {
				return benign[processed-1]
			}
			return nil
		},
	)
	if err := w.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if processed != len(benign)+1 {
		t.Fatalf("processed = %d", processed)
	}
}

func TestPlatformGenerationConvergerRunPassSanitizesUnexpectedDependencyError(t *testing.T) {
	processed := 0
	w := newPlatformGenerationConvergerTestWorker(
		func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) {
			return []PlatformGenerationIdentity{{UserID: 1, GenerationID: generationTestID}, {UserID: 2, GenerationID: generationTestID}}, 0, nil
		},
		func(context.Context, PlatformGenerationIdentity, int64) error {
			processed++
			return errors.New("secret dependency detail")
		},
	)
	if err := w.RunPass(context.Background()); err != ErrPlatformGenerationControlUnavailable {
		t.Fatalf("RunPass() error = %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d", processed)
	}
}

func TestPlatformGenerationConvergerStartRunsImmediatelyAndOnInjectedCadence(t *testing.T) {
	ticker := newPlatformGenerationConvergerFakeTicker()
	passes := make(chan struct{}, 3)
	w := newPlatformGenerationConvergerTestWorker(
		func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) {
			passes <- struct{}{}
			return nil, 0, nil
		},
		func(context.Context, PlatformGenerationIdentity, int64) error { return nil },
	)
	w.newTicker = func(interval time.Duration) platformGenerationConvergerTicker {
		if interval != platformGenerationConvergerInterval {
			t.Fatalf("ticker interval = %s", interval)
		}
		return ticker
	}
	w.interval = platformGenerationConvergerInterval

	w.Start()
	waitPlatformGenerationConvergerSignal(t, passes, "immediate pass")
	ticker.c <- time.Unix(1, 0)
	waitPlatformGenerationConvergerSignal(t, passes, "cadence pass")
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !ticker.stopped.Load() {
		t.Fatal("ticker was not stopped")
	}
}

func TestPlatformGenerationConvergerStartRetriesAfterDependencyFailure(t *testing.T) {
	ticker := newPlatformGenerationConvergerFakeTicker()
	attempts := make(chan int, 2)
	var count atomic.Int32
	w := newPlatformGenerationConvergerTestWorker(
		func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) {
			attempt := int(count.Add(1))
			attempts <- attempt
			if attempt == 1 {
				return nil, 0, ErrPlatformGenerationUnavailable
			}
			return nil, 0, nil
		},
		func(context.Context, PlatformGenerationIdentity, int64) error { return nil },
	)
	w.newTicker = func(time.Duration) platformGenerationConvergerTicker { return ticker }
	w.interval = platformGenerationConvergerInterval
	w.Start()
	if got := waitPlatformGenerationConvergerAttempt(t, attempts); got != 1 {
		t.Fatalf("first attempt = %d", got)
	}
	ticker.c <- time.Unix(1, 0)
	if got := waitPlatformGenerationConvergerAttempt(t, attempts); got != 2 {
		t.Fatalf("second attempt = %d", got)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformGenerationConvergerRunPassStopsOnContextCancellation(t *testing.T) {
	started := make(chan struct{})
	w := newPlatformGenerationConvergerTestWorker(
		func(ctx context.Context, _ uint64, _ int64) ([]PlatformGenerationIdentity, uint64, error) {
			close(started)
			<-ctx.Done()
			return nil, 0, ctx.Err()
		},
		func(context.Context, PlatformGenerationIdentity, int64) error { return nil },
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.RunPass(ctx) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunPass() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunPass did not stop after cancellation")
	}
}

func TestPlatformGenerationConvergerRunPassBoundsDependencyContext(t *testing.T) {
	w := newPlatformGenerationConvergerTestWorker(
		func(ctx context.Context, _ uint64, _ int64) ([]PlatformGenerationIdentity, uint64, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("scan context has no pass deadline")
			}
			remaining := time.Until(deadline)
			if remaining <= 0 || remaining > platformGenerationConvergerBudget {
				t.Fatalf("scan deadline remaining = %s", remaining)
			}
			return nil, 0, nil
		},
		func(context.Context, PlatformGenerationIdentity, int64) error { return nil },
	)
	if err := w.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformGenerationConvergerConcurrentStartAndCloseAreIdempotent(t *testing.T) {
	started := make(chan struct{}, 4)
	w := newPlatformGenerationConvergerTestWorker(
		func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) {
			started <- struct{}{}
			return nil, 0, nil
		},
		func(context.Context, PlatformGenerationIdentity, int64) error { return nil },
	)
	w.newTicker = func(time.Duration) platformGenerationConvergerTicker {
		return newPlatformGenerationConvergerFakeTicker()
	}
	w.interval = platformGenerationConvergerInterval

	var starts sync.WaitGroup
	for i := 0; i < 16; i++ {
		starts.Add(1)
		go func() { defer starts.Done(); w.Start() }()
	}
	starts.Wait()
	waitPlatformGenerationConvergerSignal(t, started, "single immediate pass")
	select {
	case <-started:
		t.Fatal("Start launched more than one worker")
	case <-time.After(20 * time.Millisecond):
	}

	var closes sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		closes.Add(1)
		go func() { defer closes.Done(); errs <- w.Close(context.Background()) }()
	}
	closes.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}
}

func TestPlatformGenerationConvergerCloseHonorsContext(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	w := newPlatformGenerationConvergerTestWorker(
		func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) {
			close(blocked)
			<-release
			return nil, 0, nil
		},
		func(context.Context, PlatformGenerationIdentity, int64) error { return nil },
	)
	w.newTicker = func(time.Duration) platformGenerationConvergerTicker {
		return newPlatformGenerationConvergerFakeTicker()
	}
	w.interval = platformGenerationConvergerInterval
	w.Start()
	<-blocked
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v", err)
	}
	close(release)
	if err := w.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestPlatformGenerationConvergerNilZeroAndConstructorSafety(t *testing.T) {
	var nilWorker *PlatformGenerationConverger
	nilWorker.Start()
	if err := nilWorker.RunPass(context.Background()); err != ErrPlatformGenerationControlUnavailable {
		t.Fatalf("nil RunPass() error = %v", err)
	}
	if err := nilWorker.Close(context.Background()); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}

	zero := &PlatformGenerationConverger{}
	zero.Start()
	if err := zero.Close(context.Background()); err != nil {
		t.Fatalf("zero Close() error = %v", err)
	}
	if _, err := NewPlatformGenerationConverger(nil); err != ErrPlatformGenerationControlUnavailable {
		t.Fatalf("nil constructor error = %v", err)
	}
	fakeControl := &PlatformGenerationControl{}
	if _, err := NewPlatformGenerationConverger(fakeControl); err != ErrPlatformGenerationControlUnavailable {
		t.Fatalf("fake constructor error = %v", err)
	}
}

func TestPlatformGenerationConvergerConstructorBindsProductionDependencies(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewPlatformGenerationStore(client)
	if err != nil {
		t.Fatal(err)
	}
	db := &gorm.DB{Config: &gorm.Config{}}
	control, err := NewPlatformGenerationControl(db, store, NewPlatformGenerationCancellationRegistry())
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewPlatformGenerationConverger(control)
	if err != nil {
		t.Fatal(err)
	}
	if w.control != control || w.store != store || w.scan == nil || w.converge == nil || w.elapsedNow == nil || w.nowMillis == nil || w.newTicker == nil {
		t.Fatal("constructor did not bind production dependencies")
	}
	if w.interval != platformGenerationConvergerInterval || w.maxKeys != platformGenerationConvergerMaxKeys || w.budget != platformGenerationConvergerBudget {
		t.Fatalf("defaults = %s/%d/%s", w.interval, w.maxKeys, w.budget)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformGenerationConvergerEmptyScanPagePreservesMalformedOmissionContract(t *testing.T) {
	converged := false
	w := newPlatformGenerationConvergerTestWorker(
		func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error) {
			// ScanGenerationKeys omits malformed keys before returning identities.
			return nil, 0, nil
		},
		func(context.Context, PlatformGenerationIdentity, int64) error {
			converged = true
			return nil
		},
	)
	if err := w.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if converged {
		t.Fatal("worker converged an identity omitted by the scanner")
	}
}

func newPlatformGenerationConvergerTestWorker(
	scan func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error),
	converge func(context.Context, PlatformGenerationIdentity, int64) error,
) *PlatformGenerationConverger {
	return &PlatformGenerationConverger{
		scan:       scan,
		converge:   converge,
		elapsedNow: time.Now,
		nowMillis:  func() int64 { return 1_000 },
		maxKeys:    platformGenerationConvergerMaxKeys,
		budget:     platformGenerationConvergerBudget,
	}
}

func waitPlatformGenerationConvergerSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitPlatformGenerationConvergerAttempt(t *testing.T, attempts <-chan int) int {
	t.Helper()
	select {
	case attempt := <-attempts:
		return attempt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for attempt")
		return 0
	}
}
