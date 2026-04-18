// Package auth (touch_buffer.go): debounced last_used_at updater.
// Codex review [MEDIUM] 02-03 — instead of a per-request goroutine that
// calls TouchKeyLastUsed, we coalesce repeated touches per api_key_id and
// flush them once per window or on shutdown. This bounds the write load
// regardless of request volume.
package auth

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DefaultTouchFlushInterval is the debounce window. 60s balances hot-path
// zero-cost touches against `last_used_at` freshness acceptable for audit.
const DefaultTouchFlushInterval = 60 * time.Second

// TouchBuffer debounces api_key.last_used_at updates. Verify enqueues an id;
// repeated enqueues for the same id within a window coalesce to one UPDATE.
// Flush runs every flushInterval or on ctx cancel.
type TouchBuffer struct {
	mu            sync.Mutex
	pending       map[uuid.UUID]time.Time
	flushFn       func(context.Context, uuid.UUID) error
	flushInterval time.Duration
	log           *slog.Logger
	bufferedInc   func() // metric: gateway_apikey_touch_buffered_total
	flushInc      func() // metric: gateway_apikey_touch_flush_total
}

// NewTouchBuffer constructs a TouchBuffer with the given flush callback and
// optional metric increment hooks (nil-safe).
func NewTouchBuffer(flushFn func(context.Context, uuid.UUID) error, interval time.Duration, log *slog.Logger, bufferedInc, flushInc func()) *TouchBuffer {
	if interval <= 0 {
		interval = DefaultTouchFlushInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &TouchBuffer{
		pending:       make(map[uuid.UUID]time.Time),
		flushFn:       flushFn,
		flushInterval: interval,
		log:           log.With("module", "AUTH_TOUCH"),
		bufferedInc:   bufferedInc,
		flushInc:      flushInc,
	}
}

// Enqueue coalesces: repeated calls with the same id within one window
// collapse into a single UPDATE.
func (t *TouchBuffer) Enqueue(id uuid.UUID) {
	t.mu.Lock()
	t.pending[id] = time.Now().UTC()
	t.mu.Unlock()
	if t.bufferedInc != nil {
		t.bufferedInc()
	}
}

// PendingCount returns the number of unique api_key_ids waiting to flush.
func (t *TouchBuffer) PendingCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}

// Run loops on a ticker; flushes pending updates. Cancel ctx to stop (final
// flush runs before returning).
func (t *TouchBuffer) Run(ctx context.Context) {
	ticker := time.NewTicker(t.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Flush(context.Background())
			return
		case <-ticker.C:
			t.Flush(ctx)
		}
	}
}

// Flush drains the pending map and calls flushFn for each id. Errors are
// logged but never returned — touch is best-effort by design.
func (t *TouchBuffer) Flush(ctx context.Context) {
	t.mu.Lock()
	batch := t.pending
	t.pending = make(map[uuid.UUID]time.Time, len(batch))
	t.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	if t.flushInc != nil {
		t.flushInc()
	}
	if t.flushFn == nil {
		return
	}
	for id := range batch {
		fctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := t.flushFn(fctx, id); err != nil {
			t.log.Warn("touch flush failed", "err", err, "api_key_id", id.String())
		}
		cancel()
	}
}
