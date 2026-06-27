// Package admin (operations.go): GET /admin/operations handler. Emits the
// aggregated JSON the dashboard's Tier-2 "Operação" panel polls — the
// primary-pod FSM state, the schedule window + next transition, the recent
// primary lifecycles of the current month, the per-upstream breaker states,
// and the Vast cost/budget. The dashboard never touches Postgres/Redis
// directly; it polls this single endpoint behind the X-Admin-Key admin
// sub-router. Clones the UsageHandler/MetricsHandler shape exactly:
// query-interface isolation, dual constructor, OpenAI error envelope on
// query failure, admin-metric increment per branch.
//
// All data sources are read in-process: the primary.Reconciler exposes a
// lockless Snapshot() of FSM state, primary.ParseScheduleEnv(cfg) recomputes
// the schedule rule, breaker.Set.EffectiveStateSnapshot() yields the
// per-upstream states, and gen.ListPrimaryLifecycles supplies the lifecycle
// rows the cost aggregation sums over. No new sqlc query is introduced —
// the month/day cost is aggregated in Go.
//
// Economy (phantom vs OpenRouter) is DEFERRED: phantom_month_brl is omitted
// from this version (omitempty) because a gateway-wide phantom sum needs a
// new no-tenant-filter sqlc query (see 260625-v04-RESEARCH §5). This panel
// reports Vast cost only (today/month + budget bar).
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

// operationsLifecycleLimit caps the lifecycle rows pulled for the month
// window — the panel renders a compact timeline, not an audit ledger.
const operationsLifecycleLimit = 50

// OperationsResponse is the aggregated shape the "Operação" panel polls.
// Mirrored field-for-field by the dashboard's TS OperationsResponse —
// this Go struct is the source of truth (260625-v04-RESEARCH §2).
type OperationsResponse struct {
	FSM        FSMSection      `json:"fsm"`
	Schedule   ScheduleSection `json:"schedule"`
	Lifecycles []LifecycleRow  `json:"lifecycles"`
	Breakers   []BreakerRow    `json:"breakers"`
	VastCost   VastCostSection `json:"vast_cost"`
}

// FSMSection is the current primary + emergency FSM state.
type FSMSection struct {
	PrimaryState      string `json:"primary_state"` // asleep|provisioning|ready|draining|destroying|unknown
	EmergState        string `json:"emerg_state"`   // reuse fsmStateString(emergFSM); "unknown" if Vast off
	ActiveLifecycleID int64  `json:"active_lifecycle_id"`
	ActiveInstanceID  int64  `json:"active_instance_id"`
	IsLeader          bool   `json:"is_leader"`
}

// ScheduleSection is the resolved schedule window + the next transition.
type ScheduleSection struct {
	Timezone            string   `json:"timezone"`
	UpHour              int      `json:"up_hour"`
	DownHour            int      `json:"down_hour"`
	Days                []string `json:"days"` // ordered ["mon","tue",...]
	ProvisionLeadS      int      `json:"provision_lead_seconds"`
	GraceRampDownS      int      `json:"grace_ramp_down_seconds"`
	Disabled            bool     `json:"disabled"`
	ShouldBeProvisioned bool     `json:"should_be_provisioned_now"`
	NextTransitionAt    string   `json:"next_transition_at"`   // RFC3339; "" if none
	NextTransitionKind  string   `json:"next_transition_kind"` // up|down|""
}

// LifecycleRow is one primary lifecycle. Nullable columns render as JSON
// null (not zero) so the dashboard distinguishes "not yet computed" from
// "zero cost" (Pitfall D).
type LifecycleRow struct {
	ID             int64    `json:"id"`
	StartedAt      string   `json:"started_at"`       // RFC3339
	DrainStartedAt *string  `json:"drain_started_at"` // null if no drain
	EndedAt        *string  `json:"ended_at"`         // null = still running
	TriggerReason  string   `json:"trigger_reason"`
	VastInstanceID *int64   `json:"vast_instance_id"`
	AcceptedDPH    *float64 `json:"accepted_dph"`
	CostBRL        *float64 `json:"cost_brl"` // null while open (Pitfall B)
	ShutdownReason *string  `json:"shutdown_reason"`
}

// BreakerRow is one upstream's effective breaker state.
type BreakerRow struct {
	Upstream string `json:"upstream"`
	State    string `json:"state"` // closed|half-open|open|forced-open
}

// VastCostSection is the Vast spend + budget for the day/month window.
type VastCostSection struct {
	TodayBRL        float64  `json:"today_brl"`
	MonthBRL        float64  `json:"month_brl"`
	BudgetBRL       float64  `json:"budget_brl"`
	BudgetPctUsed   float64  `json:"budget_pct_used"`
	PhantomMonthBRL *float64 `json:"phantom_month_brl,omitempty"` // DEFERRED — never set this version
}

// operationsQueries isolates the sqlc surface used by the handler. Test
// injection replaces this with a fake without a real pgxpool.
type operationsQueries interface {
	ListPrimaryLifecycles(ctx context.Context, arg gen.ListPrimaryLifecyclesParams) ([]gen.ListPrimaryLifecyclesRow, error)
}

// OperationsHandler serves GET /admin/operations.
type OperationsHandler struct {
	q        operationsQueries
	breakers *breaker.Set
	rec      *primary.Reconciler // nil-safe: Vast off
	emergFSM *emerg.FSM          // nil-safe
	cfg      config.Config
	log      *slog.Logger
}

// NewOperationsHandler wires the production dependencies. Accepts the
// concrete *gen.Queries; rec and emergFSM may be nil when Vast/Phase-6 is
// disabled — the handler reports "unknown" rather than panicking.
func NewOperationsHandler(q *gen.Queries, b *breaker.Set, rec *primary.Reconciler,
	emergFSM *emerg.FSM, cfg config.Config, log *slog.Logger) *OperationsHandler {
	if log == nil {
		log = slog.Default()
	}
	return &OperationsHandler{
		q:        q,
		breakers: b,
		rec:      rec,
		emergFSM: emergFSM,
		cfg:      cfg,
		log:      log.With("module", "ADMIN_OPERATIONS"),
	}
}

// newOperationsHandlerWithQueries is the test constructor: accepts any
// operationsQueries (fake or real) plus the rest of the deps.
func newOperationsHandlerWithQueries(q operationsQueries, b *breaker.Set, rec *primary.Reconciler,
	emergFSM *emerg.FSM, cfg config.Config, log *slog.Logger) *OperationsHandler {
	if log == nil {
		log = slog.Default()
	}
	return &OperationsHandler{
		q:        q,
		breakers: b,
		rec:      rec,
		emergFSM: emergFSM,
		cfg:      cfg,
		log:      log.With("module", "ADMIN_OPERATIONS"),
	}
}

// weekdayOrder is the stable Mon→Sun ordering for the schedule days
// slice. time.Weekday is Sunday=0; the panel reads Monday-first.
var weekdayOrder = []time.Weekday{
	time.Monday, time.Tuesday, time.Wednesday, time.Thursday,
	time.Friday, time.Saturday, time.Sunday,
}

var weekdayShort = map[time.Weekday]string{
	time.Monday:    "mon",
	time.Tuesday:   "tue",
	time.Wednesday: "wed",
	time.Thursday:  "thu",
	time.Friday:    "fri",
	time.Saturday:  "sat",
	time.Sunday:    "sun",
}

func (h *OperationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		// Should never happen — embedded tz data in Go stdlib.
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "tz_load_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/operations", "5xx").Inc()
		return
	}
	now := time.Now()
	nowLoc := now.In(loc)
	monthStart := time.Date(nowLoc.Year(), nowLoc.Month(), 1, 0, 0, 0, 0, loc)
	dayStart := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), 0, 0, 0, 0, loc)

	resp := OperationsResponse{
		FSM:        h.fsmSection(),
		Schedule:   h.scheduleSection(now),
		Lifecycles: make([]LifecycleRow, 0),
		Breakers:   h.breakerRows(),
	}

	rows, err := h.q.ListPrimaryLifecycles(ctx, gen.ListPrimaryLifecyclesParams{
		StartedAt: monthStart,
		Limit:     operationsLifecycleLimit,
	})
	if err != nil {
		h.log.Error("ListPrimaryLifecycles failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "lifecycles_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/operations", "5xx").Inc()
		return
	}

	var todayBRL, monthBRL float64
	for _, row := range rows {
		lr := lifecycleRowToJSON(row)
		resp.Lifecycles = append(resp.Lifecycles, lr)

		// Cost aggregation. A CLOSED row carries the billing-of-record
		// total_cost_brl; an OPEN row (ended_at NULL) has no total yet, so
		// we add a live accrual = accepted_dph × hours-since-started ×
		// USD→BRL (Pitfall B: started_at approximates first_health_pass_at,
		// so this slightly over-counts only the cold-start window while the
		// pod is young; the closed total_cost_brl remains authoritative).
		var cost float64
		if row.EndedAt.Valid {
			if f := numericPtr(row.TotalCostBrl); f != nil {
				cost = *f
			}
		} else if dph := numericPtr(row.AcceptedDph); dph != nil {
			hours := now.Sub(row.StartedAt).Hours()
			if hours < 0 {
				hours = 0
			}
			cost = *dph * hours * h.cfg.USDToBRLRate
		}
		if cost == 0 {
			continue
		}
		monthBRL += cost
		// Bucket into "today" by the lifecycle's started_at in BRT.
		if !row.StartedAt.In(loc).Before(dayStart) {
			todayBRL += cost
		}
	}

	budget := h.cfg.MonthlyPrimaryBudgetBRL
	pctUsed := 0.0
	if budget > 0 {
		pctUsed = monthBRL / budget * 100
	}
	resp.VastCost = VastCostSection{
		TodayBRL:      todayBRL,
		MonthBRL:      monthBRL,
		BudgetBRL:     budget,
		BudgetPctUsed: pctUsed,
		// PhantomMonthBRL intentionally left nil (omitempty) — economy panel
		// deferred (260625-v04-RESEARCH §5).
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	obs.GatewayAdminRequests.WithLabelValues("/admin/operations", "2xx").Inc()
}

// fsmSection reads the primary reconciler snapshot (lockless) + the
// emergency FSM state. rec nil (Vast off) → primary_state "unknown".
func (h *OperationsHandler) fsmSection() FSMSection {
	sec := FSMSection{
		PrimaryState: "unknown",
		EmergState:   fsmStateString(h.emergFSM),
	}
	if h.rec != nil {
		snap := h.rec.Snapshot()
		sec.PrimaryState = snap.State
		sec.ActiveLifecycleID = snap.ActiveLifecycleID
		sec.ActiveInstanceID = snap.ActiveInstanceID
		sec.IsLeader = snap.IsLeader
	}
	return sec
}

// scheduleSection recomputes the schedule rule from cfg (pure) and the
// next transition. On a parse error (bad tz) it returns a minimal section
// flagged disabled rather than failing the whole request.
func (h *OperationsHandler) scheduleSection(now time.Time) ScheduleSection {
	rule, err := primary.ParseScheduleEnv(h.cfg)
	if err != nil {
		h.log.Warn("ParseScheduleEnv failed; reporting schedule disabled", "err", err)
		return ScheduleSection{
			Timezone: h.cfg.PrimaryPodScheduleTimezone,
			Disabled: true,
			Days:     []string{},
		}
	}
	days := make([]string, 0, len(rule.Days))
	for _, wd := range weekdayOrder {
		if rule.Days[wd] {
			days = append(days, weekdayShort[wd])
		}
	}
	sec := ScheduleSection{
		UpHour:              rule.UpHour,
		DownHour:            rule.DownHour,
		Days:                days,
		ProvisionLeadS:      rule.ProvisionLeadS,
		GraceRampDownS:      rule.GraceRampDownS,
		Disabled:            rule.Disabled,
		ShouldBeProvisioned: rule.ShouldBeProvisioned(now),
	}
	if rule.Timezone != nil {
		sec.Timezone = rule.Timezone.String()
	} else {
		sec.Timezone = h.cfg.PrimaryPodScheduleTimezone
	}
	at, kind := rule.NextTransition(now)
	sec.NextTransitionKind = kind
	if !at.IsZero() {
		sec.NextTransitionAt = at.Format(time.RFC3339)
	}
	return sec
}

// breakerRows snapshots per-upstream effective state, ordered by upstream
// for stable output. nil Set → empty slice.
func (h *OperationsHandler) breakerRows() []BreakerRow {
	out := make([]BreakerRow, 0)
	if h.breakers == nil {
		return out
	}
	snap := h.breakers.EffectiveStateSnapshot()
	names := make([]string, 0, len(snap))
	for n := range snap {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, BreakerRow{Upstream: n, State: snap[n]})
	}
	return out
}

// lifecycleRowToJSON converts a sqlc lifecycle row to its JSON shape,
// rendering nullable pgtype columns as JSON null. cost_brl is null while
// the lifecycle is open (total_cost_brl is only written at close).
func lifecycleRowToJSON(row gen.ListPrimaryLifecyclesRow) LifecycleRow {
	return LifecycleRow{
		ID:             row.ID,
		StartedAt:      row.StartedAt.Format(time.RFC3339),
		DrainStartedAt: timestamptzPtr(row.DrainStartedAt),
		EndedAt:        timestamptzPtr(row.EndedAt),
		TriggerReason:  row.TriggerReason,
		VastInstanceID: int8Ptr(row.VastInstanceID),
		AcceptedDPH:    numericPtr(row.AcceptedDph),
		CostBRL:        numericPtr(row.TotalCostBrl),
		ShutdownReason: pgTextPtr(row.ShutdownReason),
	}
}

// numericPtr converts a nullable Postgres numeric into a *float64 so the
// JSON encoder renders an unset column as null rather than 0.
func numericPtr(n pgtype.Numeric) *float64 {
	if !n.Valid {
		return nil
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return nil
	}
	v := f.Float64
	return &v
}

// int8Ptr converts a nullable Postgres int8 into a *int64.
func int8Ptr(i pgtype.Int8) *int64 {
	if !i.Valid {
		return nil
	}
	v := i.Int64
	return &v
}

// timestamptzPtr converts a nullable Postgres timestamptz into an RFC3339
// *string, null when unset.
func timestamptzPtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	v := t.Time.Format(time.RFC3339)
	return &v
}
