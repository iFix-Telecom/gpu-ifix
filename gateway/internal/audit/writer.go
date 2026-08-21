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
	// Phase 4 extensions — billing-aware audit (additive; existing Phase 2/3
	// callers continue to compile with zero-value defaults).
	// These fields are NOT written to the audit_log table by the current
	// Flush (the DB schema only has a single cost_brl column); they exist
	// here for vocabulary alignment with billing.Event so a future writer
	// can surface the richer cost breakdown to audit consumers without
	// redesigning the struct. The billing pipeline is the authoritative
	// cost store (ai_gateway.billing_events).
	AudioSeconds        float64
	EmbedsCount         int
	CostLocalBRL        float64
	CostLocalPhantomBRL float64
	CostExternalBRL     float64
	// Phase 7 — state-change rows (OBS-07); additive, zero-value "" for
	// existing per-request callers. Set by WriteStateChange to one of
	// "fsm_transition" | "tenant_activate" | "pod_lifecycle" |
	// "threshold_change"; written to audit_log.event_kind (nullable
	// column from migration 0020). An empty value maps to SQL NULL via
	// nullableString in dbFlusher.Flush — per-request rows stay NULL.
	EventKind string
	// Phase 7 (CR-03) — human-readable cause of a state-change row (e.g.
	// the emergency FSM transition reason). Written to audit_log.reason
	// (nullable column from migration 0022), a column DEDICATED to this
	// purpose so the transition reason no longer overloads ErrorCode
	// (which carries request error codes). Zero-value "" maps to SQL NULL
	// via nullableString — per-request rows stay NULL.
	Reason string
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

// WriteStateChange enqueues an append-only state-change audit row
// (OBS-07). It stamps ev.EventKind = kind, defaults ev.TS to time.Now()
// when the caller left it zero, and forwards to the existing non-blocking
// Enqueue — it deliberately reuses the one async writer goroutine and the
// one channel; there is NO second goroutine or channel for state changes.
//
// kind is one of: "primary_state_change" (primary FSM via
// PrimaryFSMAuditAdapter) | "emerg_state_change" (emergency FSM) |
// "tenant_activate" | "pod_lifecycle" | "threshold_change" |
// "breaker_force_*" (gatewayctl). There is NO runtime validation of
// kinds — the column is TEXT (migration 0020). Callers (the alerter in
// 07-05, tenant/pod lifecycle hooks) typically pass an Event with only
// the relevant fields populated; per-request fields stay zero-value,
// which the flusher maps to SQL NULL. NOTE: ev.DataClass must be set
// ("normal") — audit_log.data_class is a NOT NULL enum and an empty
// value poisons the whole CopyFrom batch.
//
// CR-03: audit_log.request_id is NOT NULL and part of the
// PRIMARY KEY (request_id, ts). State-change callers (the emergency FSM)
// have no real request_id, so a zero ev.RequestID would write an
// all-zeros UUID — colliding across every state change at the same ts
// and risking the whole batch INSERT failing. When the caller leaves
// RequestID zero, mint a fresh uuid.New() so every state-change row has a
// unique, non-nil request_id.
func (w *Writer) WriteStateChange(kind string, ev Event) {
	ev.EventKind = kind
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	if ev.RequestID == uuid.Nil {
		ev.RequestID = uuid.New()
	}
	w.Enqueue(ev)
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

// auditLogCopyColumns is the column list passed to tx.CopyFrom for
// ai_gateway.audit_log. It MUST stay positionally aligned with the row
// tuple auditLogCopyRow builds — pgx.CopyFrom matches values to columns
// purely by position, so a column inserted here without the matching
// edit in auditLogCopyRow (or vice versa) silently writes values into
// the wrong columns with NO compile error. WR-11: TestAuditLogCopy*
// in writer_test.go locks this alignment against future drift.
var auditLogCopyColumns = []string{
	"ts", "request_id", "tenant_id", "api_key_id", "data_class",
	"route", "method", "upstream", "status_code", "latency_ms",
	"tokens_in", "tokens_out", "cost_brl", "error_code",
	"idempotency_replayed", "stream", "truncated",
	"audio_filename", "audio_mime", "audio_size_bytes", "audio_duration_s", "audio_language",
	"event_kind", "reason",
}

// auditLogCopyRow builds the one CopyFrom value tuple for an Event. The
// element order MUST match auditLogCopyColumns exactly (see that var's
// doc). Nullable columns route zero-values through the nullable* helpers
// so per-request rows leave state-change columns (event_kind, reason)
// NULL and vice versa.
func auditLogCopyRow(e Event) []any {
	return []any{
		e.TS, e.RequestID, e.TenantID, nullableUUID(e.APIKeyID), e.DataClass,
		e.Route, e.Method, nullableString(e.Upstream), int16(e.StatusCode), int32(e.LatencyMs),
		nullableInt(e.TokensIn), nullableInt(e.TokensOut),
		nil, // cost_brl — Phase 4 populates
		nullableString(e.ErrorCode), e.IdempotencyReplayed, e.Stream, e.Truncated,
		nullableString(e.AudioFilename), nullableString(e.AudioMime),
		nullableInt64(e.AudioSizeBytes), nullableFloat(e.AudioDurationS),
		nullableString(e.AudioLanguage),
		// Phase 7 — event_kind (migration 0020, nullable). Zero-value
		// "" maps to SQL NULL: per-request rows stay NULL, only
		// WriteStateChange rows carry a kind.
		nullableString(e.EventKind),
		// Phase 7 (CR-03) — reason (migration 0022, nullable). The
		// human-readable transition cause for state-change rows;
		// zero-value "" maps to SQL NULL for per-request rows.
		nullableString(e.Reason),
	}
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
		rows = append(rows, auditLogCopyRow(e))
	}
	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"ai_gateway", "audit_log"},
		auditLogCopyColumns,
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
