package service

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	platformGenerationConvergerInterval = 5 * time.Second
	platformGenerationConvergerMaxKeys  = 512
	platformGenerationConvergerBudget   = 100 * time.Millisecond
)

type platformGenerationConvergerTicker interface {
	C() <-chan time.Time
	Stop()
}

type platformGenerationRealTicker struct {
	*time.Ticker
}

func (t platformGenerationRealTicker) C() <-chan time.Time { return t.Ticker.C }

// PlatformGenerationConverger serially advances orphaned generation records.
// It retains only a Redis scan cursor; generation content never enters it.
type PlatformGenerationConverger struct {
	control *PlatformGenerationControl
	store   *PlatformGenerationStore

	scan       func(context.Context, uint64, int64) ([]PlatformGenerationIdentity, uint64, error)
	converge   func(context.Context, PlatformGenerationIdentity, int64) error
	elapsedNow func() time.Time
	nowMillis  func() int64
	newTicker  func(time.Duration) platformGenerationConvergerTicker
	interval   time.Duration
	maxKeys    int
	budget     time.Duration

	passMu   sync.Mutex
	cursorMu sync.Mutex
	cursor   uint64

	lifecycleMu sync.Mutex
	started     bool
	closed      bool
	cancel      context.CancelFunc
	done        chan struct{}
}

func NewPlatformGenerationConverger(control *PlatformGenerationControl) (*PlatformGenerationConverger, error) {
	if control == nil || control.store == nil || control.store.client == nil {
		return nil, ErrPlatformGenerationControlUnavailable
	}
	store := control.store
	w := &PlatformGenerationConverger{
		control:    control,
		store:      store,
		scan:       store.ScanGenerationKeys,
		elapsedNow: time.Now,
		nowMillis:  func() int64 { return time.Now().UTC().UnixMilli() },
		newTicker: func(interval time.Duration) platformGenerationConvergerTicker {
			return platformGenerationRealTicker{Ticker: time.NewTicker(interval)}
		},
		interval: platformGenerationConvergerInterval,
		maxKeys:  platformGenerationConvergerMaxKeys,
		budget:   platformGenerationConvergerBudget,
		done:     make(chan struct{}),
	}
	w.converge = func(ctx context.Context, identity PlatformGenerationIdentity, nowMillis int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, err := control.deps.get(ctx, identity.UserID, identity.GenerationID)
		if err != nil {
			return err
		}
		_, err = control.converge(ctx, identity.UserID, identity.GenerationID, nowMillis, snapshot)
		return err
	}
	return w, nil
}

func (w *PlatformGenerationConverger) Start() {
	if !w.canRun() {
		return
	}
	w.lifecycleMu.Lock()
	if w.started || w.closed {
		w.lifecycleMu.Unlock()
		return
	}
	if w.done == nil {
		w.done = make(chan struct{})
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.started = true
	w.lifecycleMu.Unlock()

	go w.run(workerCtx)
}

func (w *PlatformGenerationConverger) run(ctx context.Context) {
	defer close(w.done)
	_ = w.RunPass(ctx)
	if ctx.Err() != nil {
		return
	}
	ticker := w.newTicker(w.interval)
	if ticker == nil {
		return
	}
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			_ = w.RunPass(ctx)
		}
	}
}

func (w *PlatformGenerationConverger) RunPass(ctx context.Context) error {
	if w == nil || ctx == nil || w.scan == nil || w.converge == nil || w.elapsedNow == nil || w.nowMillis == nil || w.maxKeys <= 0 || w.maxKeys > platformGenerationConvergerMaxKeys || w.budget <= 0 {
		return ErrPlatformGenerationControlUnavailable
	}
	w.passMu.Lock()
	defer w.passMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	passCtx, cancel := context.WithTimeout(ctx, w.budget)
	defer cancel()
	startedAt := w.elapsedNow()
	if startedAt.IsZero() {
		return ErrPlatformGenerationControlUnavailable
	}
	processed := 0
	for processed < w.maxKeys {
		if err := passCtx.Err(); err != nil {
			if callerErr := ctx.Err(); callerErr != nil {
				return callerErr
			}
			return nil
		}
		if w.elapsedNow().Sub(startedAt) >= w.budget {
			return nil
		}
		cursor := w.currentCursor()
		identities, next, err := w.scan(passCtx, cursor, int64(w.maxKeys-processed))
		if err != nil {
			if callerErr := ctx.Err(); callerErr != nil {
				return callerErr
			}
			if passCtx.Err() != nil {
				return nil
			}
			return ErrPlatformGenerationControlUnavailable
		}
		w.setCursor(next)
		for _, identity := range identities {
			if processed >= w.maxKeys || w.elapsedNow().Sub(startedAt) >= w.budget {
				return nil
			}
			if err := passCtx.Err(); err != nil {
				if callerErr := ctx.Err(); callerErr != nil {
					return callerErr
				}
				return nil
			}
			if validatePlatformGenerationIdentity(identity.UserID, identity.GenerationID) != nil {
				continue
			}
			nowMillis := w.nowMillis()
			if nowMillis <= 0 || !platformSSEV2SafeInteger(nowMillis) {
				return ErrPlatformGenerationControlUnavailable
			}
			processed++
			err = w.converge(passCtx, identity, nowMillis)
			if err == nil || platformGenerationConvergerBenignRecordError(err) {
				continue
			}
			if callerErr := ctx.Err(); callerErr != nil {
				return callerErr
			}
			if passCtx.Err() != nil {
				return nil
			}
			return ErrPlatformGenerationControlUnavailable
		}
		if next == 0 {
			return nil
		}
	}
	return nil
}

func (w *PlatformGenerationConverger) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		return ErrPlatformGenerationControlUnavailable
	}
	w.lifecycleMu.Lock()
	if w.done == nil {
		w.lifecycleMu.Unlock()
		return nil
	}
	if !w.closed {
		w.closed = true
		if w.cancel != nil {
			w.cancel()
		}
		if !w.started {
			close(w.done)
		}
	}
	done := w.done
	w.lifecycleMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *PlatformGenerationConverger) canRun() bool {
	return w != nil && w.scan != nil && w.converge != nil && w.elapsedNow != nil && w.nowMillis != nil && w.newTicker != nil && w.interval > 0 && w.maxKeys > 0 && w.maxKeys <= platformGenerationConvergerMaxKeys && w.budget > 0
}

func (w *PlatformGenerationConverger) currentCursor() uint64 {
	w.cursorMu.Lock()
	defer w.cursorMu.Unlock()
	return w.cursor
}

func (w *PlatformGenerationConverger) setCursor(cursor uint64) {
	w.cursorMu.Lock()
	w.cursor = cursor
	w.cursorMu.Unlock()
}

func platformGenerationConvergerBenignRecordError(err error) bool {
	return errors.Is(err, ErrPlatformGenerationInvalid) ||
		errors.Is(err, ErrPlatformGenerationConflict) ||
		errors.Is(err, ErrPlatformGenerationPersistenceConflict) ||
		errors.Is(err, ErrPlatformGenerationPersistenceUnavailable) ||
		errors.Is(err, ErrPlatformGenerationUnavailable) ||
		errors.Is(err, ErrPlatformGenerationControlUnavailable) ||
		errors.Is(err, ErrPlatformGenerationNotFound) ||
		errors.Is(err, ErrPlatformGenerationControlNotFound)
}
