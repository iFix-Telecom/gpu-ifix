// Package emerg (budget.go): Plan 06-09 monthly budget alert
// (D-D2 / PRV-05).
//
// # Design
//
// The reconciler tick (Plan 06-04) runs at 1Hz in production but the
// monthly cost SUM aggregate is expensive (table scan over the current
// month's lifecycles). We rate-limit the budget check to once per 60s
// via a separate atomic gate (lastBudgetCheckUnix in reconciler.go) —
// the 1Hz hot path stays cheap, the budget alert stays fresh enough to
// catch a runaway spend within a minute.
//
// On every check, the running monthly cost is mirrored into the
// gateway_emergency_month_cost_brl Prometheus gauge so the dashboard
// shows current spend independent of whether the alert has fired.
//
// # Sentry alert dedupe (Pitfall 11)
//
// budgetAlertDedupe wraps an atomic.Int64 day-bucket and emits at most
// once per UTC day. The CompareAndSwap loop is the CORRECT version per
// RESEARCH lines 1411-1417 — the obvious "if last == today { return
// false }; lastEmittedDay.Store(today)" approach has a benign race
// where two simultaneous calls both pass the check + both Store, but
// they would both also call CaptureMessage. The CAS-and-check pattern
// here guarantees exactly one return-true per day even under
// concurrent calls from multiple goroutines.
//
// # Sentry payload
//
//   - Level   : Warning (informational; budget is exceeded but the
//     provisioning path is NOT blocked — D-D2 explicitly
//     leaves the operator in charge via gatewayctl emerg
//     force-stop).
//   - Tags    : subsystem=emerg, alert=budget_exceeded.
//   - Extras  : month_cost_brl, budget_brl.
//   - Message : "monthly emergency budget exceeded".
//
// We use sentry.CurrentHub().Clone() to isolate the scope mutations
// from the global hub — same pattern as captureTerminalSentry in
// lifecycle.go.
package emerg

import (
	"context"
	"sync/atomic"
	"time"

	sentry "github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/vastutil"
)

// budgetAlertDedupe gates Sentry budget warnings to at most one per
// UTC day. The day bucket is unix-seconds / 86400 so it changes
// exactly at 00:00 UTC — drift-tolerant + monotonically increasing.
//
// Implementation note (RESEARCH Pitfall 11): the buggy version is
// `if last == today { return false }; b.lastEmittedDay.Store(today)`
// — two concurrent calls both pass the check + both Store. The CAS
// version below guarantees exactly one returns true per day.
type budgetAlertDedupe struct {
	lastEmittedDay atomic.Int64 // unix epoch seconds / 86400
}

// shouldEmit returns true at most once per UTC day. CAS-based to
// stay race-free even under heavy concurrency.
func (b *budgetAlertDedupe) shouldEmit() bool {
	today := time.Now().UTC().Unix() / 86400
	last := b.lastEmittedDay.Load()
	if last == today {
		return false
	}
	return b.lastEmittedDay.CompareAndSwap(last, today)
}

// monthlyCostFn is the function signature consumed by checkBudget.
// Production wires this to gen.Queries.GetMonthlyCostBRL via the
// reconciler's q field; tests override via Reconciler.monthlyCostOverride
// to avoid needing a real DB pool.
type monthlyCostFn func(ctx context.Context) (pgtype.Numeric, error)

// checkBudget queries the monthly cost aggregate, mirrors it into the
// obs gauge, and emits a Sentry warning when month_cost > budget AND
// the dedupe gate allows. Safe to call from the reconciler tick.
//
// The function is INTENTIONALLY not gated by isLeader inside itself —
// the caller (runOneTick in reconciler.go) decides leader-only via the
// 60s tick wrapper. Keeping the gate at the call site lets unit tests
// drive checkBudget directly without setting up leader election.
func (r *Reconciler) checkBudget(ctx context.Context) {
	costNumeric, err := r.invokeMonthlyCost(ctx)
	if err != nil {
		r.deps.Log.Warn("budget check: monthly cost query failed",
			"err", err)
		return
	}
	costFloat := numericToFloat(costNumeric)
	obs.GatewayEmergencyMonthCostBRL.Set(costFloat)

	budget := r.deps.Cfg.MonthlyEmergencyBudgetBRL
	if costFloat <= budget {
		return
	}
	if !r.budgetDedupe.shouldEmit() {
		// Already emitted today — gauge update above is enough.
		return
	}
	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetTag("subsystem", "emerg")
	hub.Scope().SetTag("alert", "budget_exceeded")
	hub.Scope().SetExtra("month_cost_brl", costFloat)
	hub.Scope().SetExtra("budget_brl", budget)
	hub.Scope().SetLevel(sentry.LevelWarning)
	hub.CaptureMessage("monthly emergency budget exceeded")
	r.deps.Log.Warn("budget alert emitted (D-D2)",
		"month_cost_brl", costFloat,
		"budget_brl", budget)
}

// invokeMonthlyCost runs either the test override OR the production
// sqlc query. Keeping the indirection here (rather than inlining in
// checkBudget) makes the test-only field surface visible in one spot.
func (r *Reconciler) invokeMonthlyCost(ctx context.Context) (pgtype.Numeric, error) {
	if r.monthlyCostOverride != nil {
		return r.monthlyCostOverride(ctx)
	}
	if r.q == nil {
		// No DB pool wired AND no override — return 0 and let the
		// caller short-circuit (no budget alert possible).
		return vastutil.PgNumericFromFloat(0), nil
	}
	return r.q.GetMonthlyCostBRL(ctx)
}

// CheckBudgetForTest is the integration-test entry point that bypasses
// the 60s rate-limit gate in runOneTick. Production code MUST NOT call
// this — use the rate-limited path inside runOneTick instead. Exposed
// here so the integration test in emerg_budget_alert_test.go can drive
// the alert deterministically without waiting 60 wall-clock seconds.
func (r *Reconciler) CheckBudgetForTest(ctx context.Context) {
	r.checkBudget(ctx)
}

// numericToFloat converts a pgtype.Numeric to float64. Handles both the
// happy path (Float64Value() succeeds) and the rare InvalidNumeric case
// (NaN-like values from a malformed DB row) by falling back to 0 — a
// missing aggregate should NEVER trigger a false-positive alert.
func numericToFloat(n pgtype.Numeric) float64 {
	if !n.Valid {
		return 0
	}
	v, err := n.Float64Value()
	if err != nil || !v.Valid {
		return 0
	}
	return v.Float64
}
