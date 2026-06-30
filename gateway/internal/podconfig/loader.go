package podconfig

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// snapshot is the immutable view of pod_config served from the hot path.
// Built fresh on every Refresh, atomically swapped via atomic.Pointer so
// reads are lock-free. Mirror of upstreams.snapshot (no override layer —
// pod_config has none).
type snapshot struct {
	cfg    PodConfig
	rule   ScheduleRule
	bounds PodConfigBounds
}

// loaderQueries isolates the sqlc surface so tests can stub it without a
// real Postgres pool. Mirrors upstreams.loaderQueries.
type loaderQueries interface {
	GetPodConfig(ctx context.Context) (gen.AiGatewayPodConfig, error)
}

// Loader holds the in-memory authoritative snapshot of ai_gateway.pod_config.
// Readers call Cfg/Rule/Bounds on the hot path (atomic.Pointer — lock-free).
// Refresh is called at boot + on each LISTEN/NOTIFY from pod_config_changed.
//
// tz is the STRUCTURAL timezone (D-02) — NOT a pod_config column. It is
// resolved ONCE at boot in NewLoader (fail-fast time.LoadLocation) and never
// changes at runtime (D-03a), so the rule re-parse on each Refresh can never
// fail on a bad timezone post-boot.
type Loader struct {
	pool *pgxpool.Pool
	q    loaderQueries
	snap atomic.Pointer[snapshot]
	log  *slog.Logger
	tz   *time.Location
}

// NewLoader constructs the Loader and performs the initial Refresh.
//
// tz is the structural schedule timezone string (from config.Config); it is
// resolved via time.LoadLocation here so the failure is surfaced at boot
// (fail-fast — a silent UTC fallback would shift the whole peak window).
// Returns an error if the timezone is invalid OR the initial GetPodConfig
// fails (boot MUST fail-fast — the Plan 17-03 seed guarantees a row exists,
// so an unreadable pod_config is a hard error, not a zero-config serve).
func NewLoader(ctx context.Context, pool *pgxpool.Pool, tz string, log *slog.Logger) (*Loader, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("podconfig: invalid structural timezone %q: %w", tz, err)
	}
	l := &Loader{
		pool: pool,
		q:    gen.New(pool),
		log:  log.With("module", "PODCONFIG"),
		tz:   loc,
	}
	if err := l.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("initial pod_config refresh: %w", err)
	}
	return l, nil
}

// Refresh reads the single pod_config row and atomically swaps in a new
// snapshot. The last-good-on-error invariant is load-bearing (T-17-04,
// RESEARCH Pitfall 1): on ANY error — query failure OR a broken schedule
// re-parse — Refresh increments the error metric and RETURNS WITHOUT
// swapping, so the reconciler keeps serving the previous good snapshot and
// provisioning never reads zero-config on a transient DB hiccup.
func (l *Loader) Refresh(ctx context.Context) error {
	row, err := l.q.GetPodConfig(ctx)
	if err != nil {
		obs.PodConfigReloadTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("get pod_config: %w", err)
		// NOTE: snapshot UNCHANGED — caller (listen handler) logs + returns nil.
	}
	cfg := rowToPodConfig(row)
	rule, perr := ParseScheduleFromSnapshot(cfg, l.tz)
	if perr != nil {
		obs.PodConfigReloadTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("reparse schedule: %w", perr)
		// NOTE: never swap a broken rule into the live path (T-17-06).
	}
	s := &snapshot{cfg: cfg, rule: rule, bounds: rowToBounds(row)}
	l.snap.Store(s)
	obs.PodConfigReloadTotal.WithLabelValues("ok").Inc()
	l.log.Info("pod_config refreshed",
		"cap_primary", cfg.CapPrimary,
		"schedule_disabled", cfg.ScheduleDisabled)
	return nil
}

// Load returns the current snapshot pointer (lock-free atomic read). Returns
// nil before the first Refresh — production NewLoader does the initial
// Refresh and fatals on failure, so the gateway never observes nil.
func (l *Loader) Load() *snapshot {
	return l.snap.Load()
}

// Cfg returns the 16 hot config fields from the current snapshot (single
// atomic.Pointer load — zero synchronous DB call on the hot path). Returns
// the zero PodConfig before the first Refresh.
func (l *Loader) Cfg() PodConfig {
	s := l.snap.Load()
	if s == nil {
		return PodConfig{}
	}
	return s.cfg
}

// Rule returns the pre-parsed schedule rule from the current snapshot.
func (l *Loader) Rule() ScheduleRule {
	s := l.snap.Load()
	if s == nil {
		return ScheduleRule{}
	}
	return s.rule
}

// Bounds returns the owner-editable min/max gates from the current snapshot.
func (l *Loader) Bounds() PodConfigBounds {
	s := l.snap.Load()
	if s == nil {
		return PodConfigBounds{}
	}
	return s.bounds
}
