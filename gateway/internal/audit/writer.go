// Package audit persists per-request metadata to ai_gateway.audit_log and,
// for tenants flagged data_class=normal, full prompt/response bodies to
// ai_gateway.audit_log_content. Writes are fully async: the hot path calls
// Enqueue which never blocks; a background goroutine batches and flushes
// on a 500-row/1s interval per CONTEXT.md D-B4.
package audit

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const (
	bufferSize      = 1000
	flushBatchSize  = 500
	flushInterval   = 1 * time.Second
	contentCapBytes = 128 * 1024 // 128 KB per D-B5
)

// Event is one audit row (metadata + optional content).
type Event struct {
	TS                  time.Time
	RequestID           uuid.UUID
	TenantID            uuid.UUID
	APIKeyID            uuid.UUID // zero-UUID allowed (unauthenticated paths would use it; not expected in Phase 2 since all /v1/* is authed)
	DataClass           string    // "normal" | "sensitive"
	Route               string
	Method              string
	Upstream            string
	StatusCode          int
	LatencyMs           int64
	TokensIn            int // populated when known (post-Phase 4)
	TokensOut           int
	ErrorCode           string
	IdempotencyReplayed bool
	Stream              bool
	Truncated           bool
	// Content (inserted into audit_log_content only when DataClass == "normal")
	Prompt   []byte // JSON bytes; nil for sensitive
	Response []byte // JSON bytes OR accumulated SSE chunks; nil for sensitive
	// Whisper metadata (D-B6)
	AudioFilename  string
	AudioMime      string
	AudioSizeBytes int64
	AudioDurationS float64
	AudioLanguage  string
}

// flusher abstracts the actual DB write so tests can inject a fake without
// requiring a live Postgres. Production uses dbFlusher{pool,q}.
type flusher interface {
	Flush(ctx context.Context, batch []Event) error
}

// Writer is the async audit logger.
type Writer struct {
	ch      chan Event
	fl      flusher
	log     *slog.Logger
	dropped atomic.Uint64 // observable via Dropped() for tests
	// enqueueHook is a test-only escape: if non-nil, Enqueue forwards to it
	// instead of the channel. Production code never sets this; tests in
	// middleware_test.go set it to capture Events without running Run.
	enqueueHook func(Event)
}

// NewWriter wires the pool and logger. Call Run in a goroutine with the
// root ctx; it exits when ctx is canceled (shutting the flusher down).
func NewWriter(pool *pgxpool.Pool, log *slog.Logger) *Writer {
	return &Writer{
		ch:  make(chan Event, bufferSize),
		fl:  &dbFlusher{pool: pool, q: gen.New(pool)},
		log: log.With("module", "AUDIT"),
	}
}

// newTestWriter is a test-only constructor. Exposed to the _test package
// via same-package files. Buffer size is configurable so we can exercise
// the buffer-full branch of Enqueue deterministically.
func newTestWriter(fl flusher, buf int) *Writer {
	if buf <= 0 {
		buf = bufferSize
	}
	return &Writer{
		ch:  make(chan Event, buf),
		fl:  fl,
		log: slog.Default().With("module", "AUDIT"),
	}
}

// Enqueue is the hot-path API. NEVER blocks: if the buffer is full,
// increments gateway_audit_dropped_total and returns immediately.
func (w *Writer) Enqueue(e Event) {
	if w.enqueueHook != nil {
		w.enqueueHook(e)
		return
	}
	select {
	case w.ch <- e:
	default:
		w.dropped.Add(1)
		obs.AuditDroppedTotal.Inc()
		if w.log != nil {
			w.log.Warn("audit buffer full — event dropped",
				"request_id", e.RequestID.String(),
				"tenant_id", e.TenantID.String(),
				"route", e.Route,
			)
		}
	}
}

// Dropped is the running count of events dropped since process start.
// Test hook — production consumers use obs.AuditDroppedTotal.
func (w *Writer) Dropped() uint64 { return w.dropped.Load() }

// Run is the flusher. Run once per process, typically in a goroutine
// spawned by main. Ctx cancel drains the buffer before returning.
func (w *Writer) Run(ctx context.Context) {
	batch := make([]Event, 0, flushBatchSize)
	tick := time.NewTicker(flushInterval)
	defer tick.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.fl.Flush(context.Background(), batch); err != nil {
			w.log.Error("audit flush failed", "err", err, "batch_size", len(batch))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			// Drain remaining buffered events before exit.
			for {
				select {
				case e := <-w.ch:
					batch = append(batch, e)
					if len(batch) >= flushBatchSize {
						flush()
					}
				default:
					flush()
					w.log.Info("audit writer exited")
					return
				}
			}
		case e := <-w.ch:
			batch = append(batch, e)
			if len(batch) >= flushBatchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

// dbFlusher is the production flusher — uses pgx.CopyFrom for audit_log
// and row-by-row InsertAuditLogContent for data_class=normal rows.
type dbFlusher struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// Flush writes a batch in a single transaction: CopyFrom for audit_log
// + row-by-row InsertAuditLogContent for normal-class rows.
func (d *dbFlusher) Flush(ctx context.Context, batch []Event) error {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// audit_log CopyFrom: convert []Event to pgx.CopyFromSlice rows.
	rows := make([][]any, 0, len(batch))
	for _, e := range batch {
		rows = append(rows, []any{
			e.TS, e.RequestID, e.TenantID, nullableUUID(e.APIKeyID), e.DataClass,
			e.Route, e.Method, nullableString(e.Upstream), int16(e.StatusCode), int32(e.LatencyMs),
			nullableInt(e.TokensIn), nullableInt(e.TokensOut),
			nil, // cost_brl — Phase 4 populates
			nullableString(e.ErrorCode), e.IdempotencyReplayed, e.Stream, e.Truncated,
			nullableString(e.AudioFilename), nullableString(e.AudioMime),
			nullableInt64(e.AudioSizeBytes), nullableFloat(e.AudioDurationS),
			nullableString(e.AudioLanguage),
		})
	}
	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"ai_gateway", "audit_log"},
		[]string{
			"ts", "request_id", "tenant_id", "api_key_id", "data_class",
			"route", "method", "upstream", "status_code", "latency_ms",
			"tokens_in", "tokens_out", "cost_brl", "error_code",
			"idempotency_replayed", "stream", "truncated",
			"audio_filename", "audio_mime", "audio_size_bytes", "audio_duration_s", "audio_language",
		},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return err
	}

	// audit_log_content: per-row insert ONLY for normal + non-empty content.
	q := gen.New(tx)
	for _, e := range batch {
		if e.DataClass != "normal" {
			continue
		}
		if len(e.Prompt) == 0 && len(e.Response) == 0 {
			continue
		}
		if err := q.InsertAuditLogContent(ctx, gen.InsertAuditLogContentParams{
			RequestID: e.RequestID,
			Ts:        e.TS,
			Prompt:    e.Prompt,
			Response:  e.Response,
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Helpers: map zero-values to SQL NULL. Reduces churn in the above slice.
func nullableUUID(u uuid.UUID) any {
	if u == uuid.Nil {
		return nil
	}
	return u
}
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullableInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
func nullableInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
func nullableFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}
