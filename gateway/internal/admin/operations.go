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
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

// secondaryPodsCacheTTL bounds how often the /admin/operations handler calls
// Vast's account-wide ListInstances. The panel is polled ~every 10s; a 60s
// cache keeps Vast API pressure to ~1 req/min per replica (T-secpods-02 DoS
// mitigation) while staying fresh enough for a read-only inventory view.
const secondaryPodsCacheTTL = 60 * time.Second

// operationsLifecycleLimit caps the lifecycle rows pulled for the month
// window — the panel renders a compact timeline, not an audit ledger.
const operationsLifecycleLimit = 50

// OperationsResponse is the aggregated shape the "Operação" panel polls.
// Mirrored field-for-field by the dashboard's TS OperationsResponse —
// this Go struct is the source of truth (260625-v04-RESEARCH §2).
type OperationsResponse struct {
	FSM           FSMSection        `json:"fsm"`
	Schedule      ScheduleSection   `json:"schedule"`
	Lifecycles    []LifecycleRow    `json:"lifecycles"`
	Breakers      []BreakerRow      `json:"breakers"`
	VastCost      VastCostSection   `json:"vast_cost"`
	SecondaryPods []SecondaryPodRow `json:"secondary_pods"`
}

// SecondaryPodRow is one Vast instance on the account that is NOT the active
// primary (the gateway-managed 3090 LLM pod). It surfaces externally-managed
// pods — e.g. the 3060 STT/TTS box driven by ops/vast-3060/vast3060.py — in a
// read-only "Outros pods" panel. Only non-secret projection fields are
// exposed (T-secpods-01 Information-Disclosure mitigation): no ssh_host,
// ports, image_uuid, or API key leaves the handler. DphBRL is pre-converted
// to BRL server-side (DphTotal USD × cfg.USDToBRLRate) so the whole
// operations response stays BRL-consistent (matches VastCostSection) and the
// dashboard reuses formatBrl — raw USD is never shipped to the client.
type SecondaryPodRow struct {
	ID            int64   `json:"id"`
	GpuName       string  `json:"gpu_name"`
	NumGpus       int     `json:"num_gpus"`
	Status        string  `json:"status"` // from ActualStatus
	Label         string  `json:"label"`
	DphBRL        float64 `json:"dph_brl"`
	UptimeSeconds int64   `json:"uptime_seconds"`
}

// vastLister is the minimal Vast surface the secondary-pods section needs —
// the account-wide read-only instance list. Kept as an interface so tests
// inject a fake without real Vast traffic; *vast.Client satisfies it.
type vastLister interface {
	ListInstances(ctx context.Context) ([]vast.Instance, error)
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
	podCfg   *podconfig.Loader   // nil-safe: Phase 17 live budget; falls back to boot cfg
	vast     vastLister          // nil-safe: Vast off / VAST_AI_API_KEY unset → secondary_pods empty
	cfg      config.Config
	log      *slog.Logger

	// secondary-pods cache — Vast's account-wide list is fetched at most once
	// per secondaryPodsCacheTTL to bound API pressure (T-secpods-02). Guarded
	// by mu; secCache holds the last-good raw instances, secCacheAt its fetch
	// time.
	mu         sync.Mutex
	secCacheAt time.Time
	secCache   []vast.Instance
}

// NewOperationsHandler wires the production dependencies. Accepts the
// concrete *gen.Queries; rec, emergFSM and podCfg may be nil when
// Vast/Phase-6/Phase-17 is disabled — the handler reports "unknown"
// (or falls back to the boot budget) rather than panicking.
func NewOperationsHandler(q *gen.Queries, b *breaker.Set, rec *primary.Reconciler,
	emergFSM *emerg.FSM, podCfg *podconfig.Loader, vastL vastLister, cfg config.Config, log *slog.Logger) *OperationsHandler {
	if log == nil {
		log = slog.Default()
	}
	return &OperationsHandler{
		q:        q,
		breakers: b,
		rec:      rec,
		emergFSM: emergFSM,
		podCfg:   podCfg,
		vast:     vastL,
		cfg:      cfg,
		log:      log.With("module", "ADMIN_OPERATIONS"),
	}
}

// newOperationsHandlerWithQueries is the test constructor: accepts any
// operationsQueries (fake or real) plus the rest of the deps.
func newOperationsHandlerWithQueries(q operationsQueries, b *breaker.Set, rec *primary.Reconciler,
	emergFSM *emerg.FSM, podCfg *podconfig.Loader, vastL vastLister, cfg config.Config, log *slog.Logger) *OperationsHandler {
	if log == nil {
		log = slog.Default()
	}
	return &OperationsHandler{
		q:        q,
		breakers: b,
		rec:      rec,
		emergFSM: emergFSM,
		podCfg:   podCfg,
		vast:     vastL,
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

	// Phase 17: the monthly budget is a HOT pod_config field the owner edits
	// live. Read it from the podconfig snapshot (same source the reconciler
	// enforces) so the /operacao cost bar reflects edits without a restart;
	// fall back to the boot cfg when the loader is absent (Phase 17 disabled).
	budget := h.cfg.MonthlyPrimaryBudgetBRL
	if h.podCfg != nil {
		budget = h.podCfg.Cfg().MonthlyBudgetBRL
	}
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

	// Secondary pods: every account instance EXCEPT the active primary (which
	// resp.FSM.ActiveInstanceID surfaces). A nil/failing Vast client degrades
	// THIS section to empty and NEVER 5xxs the endpoint (T-secpods-02).
	resp.SecondaryPods = h.secondaryPods(ctx, resp.FSM.ActiveInstanceID)

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

// secondaryPods returns the account-wide Vast instances EXCEPT the active
// primary (activeInstanceID), mapped to the read-only SecondaryPodRow
// projection. It is nil/failure-safe by contract: a data-source problem
// degrades this ONE section (empty or last-good), it NEVER fails the whole
// /admin/operations response — mirroring the schedule parse-error posture.
//
//   - h.vast nil (Vast off / VAST_AI_API_KEY unset) → empty non-nil slice.
//   - cache hit (< secondaryPodsCacheTTL since last fetch) → serve secCache.
//   - cache miss → ListInstances; on error WARN + fall back to last-good
//     (or empty), on success refresh secCache + timestamp.
//
// The returned slice is always non-nil. dph_brl converts USD/h → BRL/h with
// cfg.USDToBRLRate; uptime_seconds is now - start_date clamped to >= 0.
func (h *OperationsHandler) secondaryPods(ctx context.Context, activeInstanceID int64) []SecondaryPodRow {
	if h.vast == nil {
		return []SecondaryPodRow{}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	insts := h.secCache
	if time.Since(h.secCacheAt) >= secondaryPodsCacheTTL {
		fresh, err := h.vast.ListInstances(ctx)
		if err != nil {
			// Degrade to last-good (or empty) — never fail the response.
			h.log.Warn("ListInstances failed; serving cached/empty secondary pods", "err", err)
		} else {
			h.secCache = fresh
			h.secCacheAt = time.Now()
			insts = fresh
		}
	}

	now := time.Now().Unix()
	rows := make([]SecondaryPodRow, 0, len(insts))
	for _, inst := range insts {
		if inst.ID == activeInstanceID {
			continue // the primary pod is shown by the FSM panel, not here
		}
		uptime := int64(0)
		if inst.StartDate > 0 {
			if u := now - int64(inst.StartDate); u > 0 {
				uptime = u
			}
		}
		rows = append(rows, SecondaryPodRow{
			ID:            inst.ID,
			GpuName:       inst.GpuName,
			NumGpus:       inst.NumGpus,
			Status:        inst.ActualStatus,
			Label:         inst.Label,
			DphBRL:        inst.DphTotal * h.cfg.USDToBRLRate,
			UptimeSeconds: uptime,
		})
	}
	return rows
}

// scheduleSection reports the schedule rule + next transition. When the
// reconciler is wired (rec non-nil) it reads rec.LiveRule() — the SAME
// pod_config-snapshot-backed rule the reconciler evaluates every tick —
// so the panel can never contradict the pod's real behavior (dashboard
// ops audit 2026-08-21: the static env said 9→17 disabled while the live
// snapshot said 8→19 enabled). LiveRule never errors (it falls back to
// the boot rule internally). With rec nil (Vast off / tests) it falls
// back to the pure env parse; on a parse error (bad tz) it returns a
// minimal section flagged disabled rather than failing the whole request.
func (h *OperationsHandler) scheduleSection(now time.Time) ScheduleSection {
	var rule primary.ScheduleRule
	if h.rec != nil {
		rule = h.rec.LiveRule()
	} else {
		var err error
		rule, err = primary.ParseScheduleEnv(h.cfg)
		if err != nil {
			h.log.Warn("ParseScheduleEnv failed; reporting schedule disabled", "err", err)
			return ScheduleSection{
				Timezone: h.cfg.PrimaryPodScheduleTimezone,
				Disabled: true,
				Days:     []string{},
			}
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
