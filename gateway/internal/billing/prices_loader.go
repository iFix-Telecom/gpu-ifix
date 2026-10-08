package billing

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// pricesSnapshot is the immutable in-memory view served by PricesLoader.Get.
type pricesSnapshot struct {
	byKey map[PriceKey]Price
}

// pricesQueries isolates the sqlc surface so tests can stub the loader without
// standing up a live pgxpool. Mirrors upstreams.loaderQueries.
type pricesQueries interface {
	ListActivePrices(ctx context.Context) ([]gen.AiGatewayPrice, error)
}

// PricesLoader holds the in-memory authoritative snapshot of
// ai_gateway.prices (valid_to IS NULL). Readers call Get on the hot path
// (atomic.Pointer — lock-free). Refresh is called at boot + on each
// LISTEN/NOTIFY prices_changed.
type PricesLoader struct {
	pool *pgxpool.Pool
	q    pricesQueries
	snap atomic.Pointer[pricesSnapshot]
	log  *slog.Logger
}

// NewPricesLoader constructs the loader and performs the initial Refresh.
// Boot MUST fail-fast if the initial query fails — an empty snapshot would
// silently bill every request at 0 BRL.
func NewPricesLoader(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*PricesLoader, error) {
	l := &PricesLoader{
		pool: pool,
		q:    gen.New(pool),
		log:  log.With("module", "PRICES"),
	}
	if err := l.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("billing: initial prices refresh: %w", err)
	}
	return l, nil
}

// Refresh loads all active rows and atomically swaps in a new snapshot.
func (l *PricesLoader) Refresh(ctx context.Context) error {
	rows, err := l.q.ListActivePrices(ctx)
	if err != nil {
		obs.GatewayPricesReload.WithLabelValues("error").Inc()
		return fmt.Errorf("billing: list active prices: %w", err)
	}
	s := &pricesSnapshot{byKey: make(map[PriceKey]Price, len(rows))}
	for _, r := range rows {
		f, ferr := r.UnitCostUsd.Float64Value()
		if ferr != nil || !f.Valid {
			l.log.Warn("price row has invalid unit_cost_usd; skipped",
				"model", r.Model, "provider", r.Provider, "unit", r.Unit,
				"err", ferr)
			continue
		}
		s.byKey[PriceKey{Model: r.Model, Provider: r.Provider, Unit: r.Unit}] = Price{
			UnitCostUSD: f.Float64,
			ValidFrom:   r.ValidFrom,
		}
	}
	l.snap.Store(s)
	obs.GatewayPricesReload.WithLabelValues("ok").Inc()
	l.log.Info("prices refreshed", "rows", len(s.byKey))
	return nil
}

// Get returns the active Price for (model, provider, unit). Lock-free.
// Returns (Price{}, false) if no active row exists — caller should log
// and proceed with cost=0 to avoid blocking the flush.
func (l *PricesLoader) Get(model, provider, unit string) (Price, bool) {
	s := l.snap.Load()
	if s == nil {
		return Price{}, false
	}
	p, ok := s.byKey[PriceKey{Model: model, Provider: provider, Unit: unit}]
	return p, ok
}

// NewStaticPricesLoader builds a loader over a fixed in-memory snapshot with
// no Postgres backing (Refresh must NOT be called on it). Test seam so other
// packages (proxy) can exercise cost attribution DB-free (quick-261007-t9f).
func NewStaticPricesLoader(rows map[PriceKey]Price) *PricesLoader {
	s := &pricesSnapshot{byKey: make(map[PriceKey]Price, len(rows))}
	for k, v := range rows {
		s.byKey[k] = v
	}
	l := &PricesLoader{log: slog.Default().With("module", "PRICES")}
	l.snap.Store(s)
	return l
}
