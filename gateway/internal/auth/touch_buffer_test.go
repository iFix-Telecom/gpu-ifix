package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTouchBuffer_EnqueueCoalesces(t *testing.T) {
	tb := NewTouchBuffer(func(context.Context, uuid.UUID) error { return nil }, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	idA, idB := uuid.New(), uuid.New()
	for i := 0; i < 100; i++ {
		tb.Enqueue(idA)
	}
	tb.Enqueue(idB)
	if got := tb.PendingCount(); got != 2 {
		t.Fatalf("PendingCount=%d want 2", got)
	}
}

func TestTouchBuffer_FlushInvokesFlushFn(t *testing.T) {
	var calls int64
	var mu sync.Mutex
	seen := map[uuid.UUID]struct{}{}
	tb := NewTouchBuffer(func(_ context.Context, id uuid.UUID) error {
		atomic.AddInt64(&calls, 1)
		mu.Lock()
		seen[id] = struct{}{}
		mu.Unlock()
		return nil
	}, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	idA, idB := uuid.New(), uuid.New()
	tb.Enqueue(idA)
	tb.Enqueue(idB)
	tb.Flush(context.Background())
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("calls=%d want 2", got)
	}
	if len(seen) != 2 {
		t.Fatalf("seen=%v want 2 distinct ids", seen)
	}
	if got := tb.PendingCount(); got != 0 {
		t.Fatalf("after flush PendingCount=%d want 0", got)
	}
}

func TestTouchBuffer_RunTickerFlushes(t *testing.T) {
	var calls int64
	tb := NewTouchBuffer(func(context.Context, uuid.UUID) error {
		atomic.AddInt64(&calls, 1)
		return nil
	}, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tb.Run(ctx)
	tb.Enqueue(uuid.New())
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&calls) >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("ticker never flushed; calls=%d", atomic.LoadInt64(&calls))
}

func TestTouchBuffer_RunDrainsOnCtxCancel(t *testing.T) {
	var calls int64
	tb := NewTouchBuffer(func(context.Context, uuid.UUID) error {
		atomic.AddInt64(&calls, 1)
		return nil
	}, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tb.Run(ctx); close(done) }()
	tb.Enqueue(uuid.New())
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on ctx cancel")
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("calls=%d want 1 (final drain)", got)
	}
}

func TestTouchBuffer_FlushErrorDoesNotPanic(t *testing.T) {
	tb := NewTouchBuffer(func(context.Context, uuid.UUID) error {
		return errors.New("boom")
	}, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	tb.Enqueue(uuid.New())
	tb.Flush(context.Background()) // must not panic
}

func TestTouchBuffer_MetricHooks(t *testing.T) {
	var bufCount, flushCount int64
	tb := NewTouchBuffer(func(context.Context, uuid.UUID) error { return nil },
		time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() { atomic.AddInt64(&bufCount, 1) },
		func() { atomic.AddInt64(&flushCount, 1) },
	)
	tb.Enqueue(uuid.New())
	tb.Enqueue(uuid.New())
	tb.Flush(context.Background())
	tb.Flush(context.Background()) // second flush has no pending → no flushInc
	if got := atomic.LoadInt64(&bufCount); got != 2 {
		t.Errorf("bufCount=%d want 2", got)
	}
	if got := atomic.LoadInt64(&flushCount); got != 1 {
		t.Errorf("flushCount=%d want 1", got)
	}
}
