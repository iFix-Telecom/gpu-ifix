// Package primary (budget.go): Plan 06.6-06a Task 4 — monthly primary-pod
// budget alert (Pitfall #12 / RESEARCH.md lines 558-568).
//
// # Design parity emerg.budget.go
//
// Same overall shape as gateway/internal/emerg/budget.go: rate-limited
// SUM aggregate of total_cost_brl from the lifecycle table, mirrored
// into a Prometheus gauge, and dedupe-gated Sentry warning when the
// running spend crosses MonthlyPrimaryBudgetBRL.
//
// # Pitfall #12 — separate budget per pod kind
//
// CRITICAL: this aggregator queries `ai_gateway.primary_lifecycles` ONLY.
// It does NOT mix in emergency_lifecycles costs. Without the table
// separation the Sentry alert fires when (primary + emergency) > primary
// budget, which would mask cost overruns of either kind. The emerg
// aggregator (emerg.checkBudget) operates on emergency_lifecycles ONLY
// for the symmetric reason.
//
// # Sentry alert dedupe
//
// budgetAlertDedupe is the SAME atomic.Int64 day-bucket pattern from
// emerg.budget.go (Pitfall 11). Re-declared in this package to avoid the
// cross-package import; the struct is tiny and the duplication keeps the
// dependency graph clean.
package primary

import (
	"context"
	"sync/atomic"
	"time"

	sentry "github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
)

// primaryBudgetAlertDedupe gates Sentry primary-budget warnings to at
// most one per UTC day. CAS-based to stay race-free under concurrent
// caller goroutines.
type primaryBudgetAlertDedupe struct {
	lastEmittedDay atomic.Int64
}

func (b *primaryBudgetAlertDedupe) shouldEmit() bool {
	today := time.Now().UTC().Unix() / 86400
	last := b.lastEmittedDay.Load()
	if last == today {
		return false
	}
	return b.lastEmittedDay.CompareAndSwap(last, today)
}

// monthlyPrimaryCostFn is the function signature consumed by the budget
// check. Production wires this to a direct SQL query against the
// `primary_lifecycles` table; tests inject a fake that returns a scripted
// value without needing a real Postgres pool.
type monthlyPrimaryCostFn func(ctx context.Context) (pgtype.Numeric, error)

// BudgetChecker is the standalone primary-pod budget aggregator. Lives
// outside the Reconciler struct (unlike emerg's setup) so Plan 06.6-08
// main.go can wire it as a sibling subsystem to the reconciler — same
// rationale as the Pitfall #12 table separation: the two budgets are
// independent subsystems.
type BudgetChecker struct {
	cfg            config.Config
	db             *pgxpool.Pool
	podCfg         *podconfig.Loader
	dedupe         *primaryBudgetAlertDedupe
	costOverride   monthlyPrimaryCostFn
	lastCheckUnix  atomic.Int64
	lastEmittedSum atomic.Pointer[float64] // most recent observed month_cost_brl (for tests + ops gauges)
}

// NewBudgetChecker constructs a BudgetChecker with the given config + DB +
// live pod_config loader (Phase 17 POD-CFG-04). db may be nil — in that case
// checkBudget short-circuits with zero cost (production wires the gateway's
// *pgxpool.Pool; the test fixtures pass nil + a costOverride via
// SetCostOverrideForTest). podCfg may be nil — CheckBudget then falls back to
// the boot cfg's MonthlyPrimaryBudgetBRL (never zero-config).
func NewBudgetChecker(cfg config.Config, db *pgxpool.Pool, podCfg *podconfig.Loader) *BudgetChecker {
	return &BudgetChecker{
		cfg:    cfg,
		db:     db,
		podCfg: podCfg,
		dedupe: &primaryBudgetAlertDedupe{},
	}
}

// monthlyBudgetBRL returns the live monthly-budget threshold: the pod_config
// snapshot value when the loader is wired + has a snapshot, else the boot cfg
// fallback (Phase 17 POD-CFG-04; never zero-config).
func (b *BudgetChecker) monthlyBudgetBRL() float64 {
	if b.podCfg != nil {
		if s := b.podCfg.Load(); s != nil {
			return b.podCfg.Cfg().MonthlyBudgetBRL
		}
	}
	return b.cfg.MonthlyPrimaryBudgetBRL
}

// SetCostOverrideForTest is the test-only injection slot for the SUM
// aggregate. Production leaves this nil; tests inject a fake that
// returns a scripted pgtype.Numeric.
func (b *BudgetChecker) SetCostOverrideForTest(fn monthlyPrimaryCostFn) {
	b.costOverride = fn
}

// LastObservedCostBRL returns the most recent monthly cost observation
// (production: from a real query; tests: from the override). Returns 0
// when no check has run yet. Used by ops dashboards + tests.
func (b *BudgetChecker) LastObservedCostBRL() float64 {
	if p := b.lastEmittedSum.Load(); p != nil {
		return *p
	}
	return 0
}

// CheckBudget runs ONE budget check pass: fetch SUM, update gauge, emit
// dedupe-gated Sentry alert if over budget. Safe to call from any
// goroutine; the caller (Plan 06.6-08 ticker wrapper) gates frequency to
// avoid hammering Postgres on the 1Hz hot path.
//
// Returns the observed monthly cost in BRL so callers can log/inspect
// without re-querying.
func (b *BudgetChecker) CheckBudget(ctx context.Context) float64 {
	cost, err := b.invokeMonthlyCost(ctx)
	if err != nil {
		return 0
	}
	costFloat := primaryNumericToFloat(cost)
	b.lastEmittedSum.Store(&costFloat)
	budgetBRL := b.monthlyBudgetBRL()
	if costFloat <= budgetBRL {
		return costFloat
	}
	if !b.dedupe.shouldEmit() {
		return costFloat
	}
	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetTag("subsystem", "primary")
	hub.Scope().SetTag("alert", "budget_exceeded")
	hub.Scope().SetExtra("month_cost_brl", costFloat)
	hub.Scope().SetExtra("budget_brl", budgetBRL)
	hub.Scope().SetLevel(sentry.LevelWarning)
	hub.CaptureMessage("monthly primary pod budget exceeded")
	return costFloat
}

// invokeMonthlyCost runs either the test override OR a direct SQL query
// against primary_lifecycles for the current month's closed lifecycles.
// Pitfall #12 — the SUM is restricted to event_kind='primary_lifecycle
// _close' equivalent: closed rows in `ai_gateway.primary_lifecycles`,
// which by table-separation alone do not include emergency closes.
func (b *BudgetChecker) invokeMonthlyCost(ctx context.Context) (pgtype.Numeric, error) {
	if b.costOverride != nil {
		return b.costOverride(ctx)
	}
	if b.db == nil {
		return pgtype.Numeric{Valid: false}, nil
	}
	const q = `SELECT COALESCE(SUM(total_cost_brl), 0)::numeric AS month_cost
	           FROM ai_gateway.primary_lifecycles
	           WHERE started_at >= date_trunc('month', NOW())
	             AND ended_at IS NOT NULL`
	var monthCost pgtype.Numeric
	row := b.db.QueryRow(ctx, q)
	err := row.Scan(&monthCost)
	return monthCost, err
}

// primaryNumericToFloat converts a pgtype.Numeric to float64. Returns 0
// on any conversion failure — a missing aggregate should NEVER trigger a
// false-positive alert.
func primaryNumericToFloat(n pgtype.Numeric) float64 {
	if !n.Valid {
		return 0
	}
	v, err := n.Float64Value()
	if err != nil || !v.Valid {
		return 0
	}
	return v.Float64
}
