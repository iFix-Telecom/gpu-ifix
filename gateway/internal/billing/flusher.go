package billing

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const (
	bufferSize     = 1000
	flushBatchSize = 500
	flushInterval  = 1 * time.Second
)

// Event is one billing record. All cost columns are BRL (NUMERIC 10,6 on disk).
// Source is "final" for clean completion, "partial" for abnormal close
// (upstream cut, client disconnect). The CTE in InsertBillingEvent
// (queries/billing.sql) is idempotent via ON CONFLICT (request_id, ts)
// DO NOTHING — retries never double-count.
type Event struct {
	TS                  time.Time
	RequestID           uuid.UUID
	TenantID            uuid.UUID
	APIKeyID            uuid.UUID
	Route               string // "chat" | "embed" | "stt" | "rerank"
	Upstream            string // "local-llm" | "openrouter-chat" | ...
	Model               string
	TokensIn            int
	TokensOut           int
	AudioSeconds        float64
	EmbedsCount         int
	CostLocalBRL        float64 // always 0 (D-B4)
	CostLocalPhantomBRL float64
	CostExternalBRL     float64
	Source              string // "final" | "partial"
}

// flusher abstracts the DB writer so tests can inject a fake. Production
// uses dbFlusher{pool,q}.
type flusher interface {
	Flush(ctx context.Context, batch []Event) error
}

// Flusher owns the buffered channel + DB writer goroutine. Hot-path
// Enqueue is non-blocking; Run drains on ctx cancel before exiting.
// Mirrors audit.Writer.
type Flusher struct {
	ch      chan Event
	fl      flusher
	log     *slog.Logger
	dropped atomic.Uint64
}

// NewFlusher wires the pool + logger. Call Run in a goroutine with the
// root ctx; it exits when ctx is canceled (draining the buffer first).
func NewFlusher(pool *pgxpool.Pool, log *slog.Logger) *Flusher {
	return &Flusher{
		ch:  make(chan Event, bufferSize),
		fl:  &dbFlusher{pool: pool, q: gen.New(pool)},
		log: log.With("module", "BILLING"),
	}
}

// newTestFlusher lets in-package tests inject a fake DB writer + custom
// buffer size so buffer-full semantics can be exercised deterministically.
// Mirrors audit.newTestWriter.
func newTestFlusher(fl flusher, buf int) *Flusher {
	if buf <= 0 {
		buf = bufferSize
	}
	return &Flusher{
		ch:  make(chan Event, buf),
		fl:  fl,
		log: slog.Default().With("module", "BILLING"),
	}
}

// Enqueue is the hot-path API. NEVER blocks: if the buffer is full,
// increments gateway_billing_flush_dropped_total and returns immediately.
func (f *Flusher) Enqueue(e Event) {
	select {
	case f.ch <- e:
	default:
		f.dropped.Add(1)
		obs.GatewayBillingFlushDropped.Inc()
		if f.log != nil {
			f.log.Warn("billing buffer full — event dropped",
				"request_id", e.RequestID.String(),
				"tenant_id", e.TenantID.String(),
				"route", e.Route,
			)
		}
	}
}

// Dropped is the running count of events dropped since process start.
// Test hook — production consumers use obs.GatewayBillingFlushDropped.
func (f *Flusher) Dropped() uint64 { return f.dropped.Load() }

// Run is the flusher loop. Run once per process in a goroutine spawned by
// main. Ctx cancel drains the buffer before returning. Mirrors audit.Writer.Run.
func (f *Flusher) Run(ctx context.Context) {
	batch := make([]Event, 0, flushBatchSize)
	tick := time.NewTicker(flushInterval)
	defer tick.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := f.fl.Flush(context.Background(), batch); err != nil {
			obs.GatewayBillingFlushFailures.WithLabelValues("flush").Inc()
			f.log.Error("billing flush failed", "err", err, "batch_size", len(batch))
		} else {
			// Label rows by their source ("final" vs "partial") so
			// /metrics can show the split at a glance.
			byFinal := 0
			byPartial := 0
			for _, e := range batch {
				if e.Source == "partial" {
					byPartial++
				} else {
					byFinal++
				}
			}
			if byFinal > 0 {
				obs.GatewayBillingFlush.WithLabelValues("final").Add(float64(byFinal))
			}
			if byPartial > 0 {
				obs.GatewayBillingFlush.WithLabelValues("partial").Add(float64(byPartial))
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			// Drain remaining buffered events before exit.
			for {
				select {
				case e := <-f.ch:
					batch = append(batch, e)
					if len(batch) >= flushBatchSize {
						flush()
					}
				default:
					flush()
					f.log.Info("billing flusher exited")
					return
				}
			}
		case e := <-f.ch:
			batch = append(batch, e)
			if len(batch) >= flushBatchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

// dbFlusher is the production flusher — one txn per batch; per-row INSERT
// through the CTE (InsertBillingEvent) keeps the ON CONFLICT (request_id,
// ts) DO NOTHING semantics intact. CopyFrom would bypass the conflict
// (Pitfall 7 — replay double-count guard).
type dbFlusher struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// Flush issues a single txn per batch and inserts row-by-row.
func (d *dbFlusher) Flush(ctx context.Context, batch []Event) error {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := d.q.WithTx(tx)
	for _, e := range batch {
		if err := insertOne(ctx, q, e); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
