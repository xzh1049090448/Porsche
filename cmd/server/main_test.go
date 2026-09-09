package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type blockingMonitorRunner struct {
	started  chan struct{}
	released atomic.Bool
}

func (m *blockingMonitorRunner) Run(ctx context.Context) {
	close(m.started)
	<-ctx.Done()
	m.released.Store(true)
}

func TestMonitorLifecycleCancelsJoinsAndReleasesBeforeExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &blockingMonitorRunner{started: make(chan struct{})}
	done := startMonitor(ctx, m)
	<-m.started
	cancel()
	joinCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := joinMonitor(joinCtx, done); err != nil {
		t.Fatal(err)
	}
	if !m.released.Load() {
		t.Fatal("monitor joined before owner release")
	}
}

func TestMonitorLifecycleJoinIsBounded(t *testing.T) {
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := joinMonitor(ctx, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("join error=%v", err)
	}
}

func TestMonitorLifecycleNilRunnerIsAlreadyJoined(t *testing.T) {
	if err := joinMonitor(context.Background(), startMonitor(context.Background(), nil)); err != nil {
		t.Fatal(err)
	}
}
