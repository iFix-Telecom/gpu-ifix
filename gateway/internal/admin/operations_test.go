package admin

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

// fakeOperationsQueries is an in-memory operationsQueries double — no
// pgxpool. It returns canned lifecycle rows and records the StartedAt
// window argument so a test can assert the month-start math.
type fakeOperationsQueries struct {
	rows   []gen.ListPrimaryLifecyclesRow
	err    error
	gotArg gen.ListPrimaryLifecyclesParams
	called bool
}

func (f *fakeOperationsQueries) ListPrimaryLifecycles(_ context.Context, arg gen.ListPrimaryLifecyclesParams) ([]gen.ListPrimaryLifecyclesRow, error) {
	f.called = true
	f.gotArg = arg
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// opNumeric builds a pgtype.Numeric carrying the given float (4-decimal
// scale) — mirrors the budget_test helper so the cost aggregation reads
// back the expected value.
func opNumeric(v float64) pgtype.Numeric {
	if v == 0 {
		return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
	}
	scaled := int64(v * 10000)
	return pgtype.Numeric{Int: big.NewInt(scaled), Exp: -4, Valid: true}
}

func opTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func opInt8(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

// newOpBreakerSet builds a real breaker.Set backed by miniredis with the
// given upstream names (mirrors internal/breaker test wiring). Cleanup
// closes both.
func newOpBreakerSet(t *testing.T, names []string) *breaker.Set {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	return breaker.NewSet(rdb, discardLog(), breaker.Options{
		ConsecutiveFailures: 3,
		Cooldown:            5 * time.Second,
	}, names)
}

// opTestCfg returns a config with the budget + USD rate the cost
// aggregation depends on, plus a non-disabled weekday schedule.
func opTestCfg() config.Config {
	return config.Config{
		MonthlyPrimaryBudgetBRL:    800.0,
		USDToBRLRate:               5.0,
		PrimaryPodScheduleTimezone: "America/Sao_Paulo",
		PrimaryPodScheduleDays:     []string{"mon", "tue", "wed", "thu", "fri"},
		// UpHour/DownHour default to 0 here — NextTransition still resolves
		// a kind; Disabled defaults false so days renders non-empty.
	}
}

// opResponse is the decode target — pointer cost fields let the test
// distinguish JSON null (open lifecycle) from 0.
type opResponse struct {
	FSM struct {
		PrimaryState string `json:"primary_state"`
		EmergState   string `json:"emerg_state"`
		IsLeader     bool   `json:"is_leader"`
	} `json:"fsm"`
	Schedule struct {
		UpHour             int      `json:"up_hour"`
		DownHour           int      `json:"down_hour"`
		Days               []string `json:"days"`
		Disabled           bool     `json:"disabled"`
		NextTransitionKind string   `json:"next_transition_kind"`
	} `json:"schedule"`
	Lifecycles []struct {
		ID      int64    `json:"id"`
		EndedAt *string  `json:"ended_at"`
		CostBRL *float64 `json:"cost_brl"`
	} `json:"lifecycles"`
	Breakers []struct {
		Upstream string `json:"upstream"`
		State    string `json:"state"`
	} `json:"breakers"`
	VastCost struct {
		TodayBRL      float64 `json:"today_brl"`
		MonthBRL      float64 `json:"month_brl"`
		BudgetBRL     float64 `json:"budget_brl"`
		BudgetPctUsed float64 `json:"budget_pct_used"`
	} `json:"vast_cost"`
}

func doOperationsRequest(t *testing.T, h *OperationsHandler) (*httptest.ResponseRecorder, opResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/operations", nil)
	h.ServeHTTP(rec, req)
	var body opResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec, body
}

// TestOperationsHandler_NilReconciler_Unknown: rec nil + emergFSM nil →
// primary_state and emerg_state "unknown", is_leader false. Closed-row
// cost contributes to month/today; open row serializes cost_brl null and
// its accrual is added on top.
func TestOperationsHandler_NilReconciler_Unknown(t *testing.T) {
	// Anchor both lifecycle starts to 00:00 BRT of the current day. This keeps
	// them (a) inside today's America/Sao_Paulo bucket and (b) always <= real
	// time.Now(), so the open-row accrual (operations.go computes it from the
	// REAL clock) stays positive. A raw now.Add(-2h)/now.Add(-1h) flaked between
	// 00:00–02:00 BRT: the starts rolled into the previous BRT day and
	// today_brl bucketed to 0 (operations.go buckets "today" in BRT).
	now := time.Now()
	brt, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load America/Sao_Paulo: %v", err)
	}
	y, mo, d := now.In(brt).Date()
	todayBRT := time.Date(y, mo, d, 0, 0, 0, 0, brt) // 00:00 BRT today
	closedStart := todayBRT                          // started today
	openStart := todayBRT                            // started today, still running

	fake := &fakeOperationsQueries{
		rows: []gen.ListPrimaryLifecyclesRow{
			{
				ID:             1,
				StartedAt:      closedStart,
				EndedAt:        opTimestamptz(now), // closed just now (still today BRT)
				TriggerReason:  "schedule_up",
				VastInstanceID: opInt8(12345),
				AcceptedDph:    opNumeric(0.40),
				TotalCostBrl:   opNumeric(4.0), // billing-of-record for the closed row
				ShutdownReason: pgtype.Text{String: "schedule_down", Valid: true},
			},
			{
				ID:            2,
				StartedAt:     openStart,
				EndedAt:       pgtype.Timestamptz{Valid: false}, // OPEN
				TriggerReason: "schedule_up",
				AcceptedDph:   opNumeric(0.40),
				TotalCostBrl:  pgtype.Numeric{Valid: false}, // not computed yet
			},
		},
	}

	bset := newOpBreakerSet(t, []string{"primary", "tier1-openrouter"})
	cfg := opTestCfg()
	h := newOperationsHandlerWithQueries(fake, bset, nil, nil, nil, cfg, discardLog())

	rec, body := doOperationsRequest(t, h)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}

	// (2) rec nil → primary_state unknown, is_leader false.
	if body.FSM.PrimaryState != "unknown" {
		t.Errorf("primary_state = %q, want unknown", body.FSM.PrimaryState)
	}
	if body.FSM.IsLeader {
		t.Errorf("is_leader = true, want false (rec nil)")
	}
	// (3) emergFSM nil → emerg_state unknown.
	if body.FSM.EmergState != "unknown" {
		t.Errorf("emerg_state = %q, want unknown", body.FSM.EmergState)
	}

	// (4) the OPEN row serializes cost_brl null, not 0.
	var openRow *float64
	var foundOpen bool
	for _, lc := range body.Lifecycles {
		if lc.ID == 2 {
			foundOpen = true
			openRow = lc.CostBRL
			if lc.EndedAt != nil {
				t.Errorf("open row ended_at = %v, want null", *lc.EndedAt)
			}
		}
	}
	if !foundOpen {
		t.Fatal("open lifecycle row (id=2) missing from response")
	}
	if openRow != nil {
		t.Errorf("open row cost_brl = %v, want null", *openRow)
	}

	// (5) closed row contributes month_brl > 0; budget_pct_used consistent.
	if body.VastCost.MonthBRL <= 0 {
		t.Errorf("month_brl = %v, want > 0", body.VastCost.MonthBRL)
	}
	wantPct := body.VastCost.MonthBRL / body.VastCost.BudgetBRL * 100
	if diff := body.VastCost.BudgetPctUsed - wantPct; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("budget_pct_used = %v, want %v", body.VastCost.BudgetPctUsed, wantPct)
	}
	if body.VastCost.BudgetBRL != 800.0 {
		t.Errorf("budget_brl = %v, want 800", body.VastCost.BudgetBRL)
	}

	// (6) open accrual adds on top of the closed total (4.0). The open row
	// ran ~1h at 0.40 USD/h × 5.0 = ~2.0 BRL, so month should exceed 4.0.
	if body.VastCost.MonthBRL <= 4.0 {
		t.Errorf("month_brl = %v, want > 4.0 (closed total + open accrual)", body.VastCost.MonthBRL)
	}
	// Both rows started today → today_brl equals month_brl here.
	if body.VastCost.TodayBRL <= 4.0 {
		t.Errorf("today_brl = %v, want > 4.0 (both rows today)", body.VastCost.TodayBRL)
	}

	// (7) breakers == the Set upstreams, ordered.
	if len(body.Breakers) != 2 {
		t.Fatalf("breakers len = %d, want 2", len(body.Breakers))
	}
	if body.Breakers[0].Upstream != "primary" || body.Breakers[1].Upstream != "tier1-openrouter" {
		t.Errorf("breakers not ordered by upstream: %+v", body.Breakers)
	}
	for _, b := range body.Breakers {
		if b.State != "closed" {
			t.Errorf("breaker %s state = %q, want closed (cold start)", b.Upstream, b.State)
		}
	}

	// (8) schedule kind in {up,down,""}; days non-empty when not disabled.
	switch body.Schedule.NextTransitionKind {
	case "up", "down", "":
	default:
		t.Errorf("next_transition_kind = %q, want up|down|empty", body.Schedule.NextTransitionKind)
	}
	if !body.Schedule.Disabled && len(body.Schedule.Days) == 0 {
		t.Errorf("schedule days empty while not disabled")
	}

	// Window math: the query StartedAt must be the first of the current
	// month in BRT (00:00).
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	nl := time.Now().In(loc)
	wantStart := time.Date(nl.Year(), nl.Month(), 1, 0, 0, 0, 0, loc)
	if !fake.gotArg.StartedAt.Equal(wantStart) {
		t.Errorf("query StartedAt = %v, want month start %v", fake.gotArg.StartedAt, wantStart)
	}
}

// TestOperationsHandler_QueryError_500: a query failure returns a 500 with
// the OpenAI error envelope, never a panic.
func TestOperationsHandler_QueryError_500(t *testing.T) {
	fake := &fakeOperationsQueries{err: context.DeadlineExceeded}
	bset := newOpBreakerSet(t, []string{"primary"})
	h := newOperationsHandlerWithQueries(fake, bset, nil, nil, nil, opTestCfg(), discardLog())

	rec, _ := doOperationsRequest(t, h)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestOperationsHandler_BudgetFromPodConfig: Phase-17 gap fix — when a
// podconfig loader is present, the /operacao cost bar reports the LIVE
// pod_config monthly budget (owner-editable, hot-reloaded), not the stale
// boot cfg value. opTestCfg budget is 800; the loader overrides to 1234.
func TestOperationsHandler_BudgetFromPodConfig(t *testing.T) {
	fake := &fakeOperationsQueries{} // no rows → month/today 0, budget from loader
	bset := newOpBreakerSet(t, []string{"primary"})
	loader := podconfig.NewStaticLoaderForTest(
		podconfig.PodConfig{MonthlyBudgetBRL: 1234.0},
		podconfig.ScheduleRule{},
		podconfig.PodConfigBounds{},
		discardLog(),
	)
	h := newOperationsHandlerWithQueries(fake, bset, nil, nil, loader, opTestCfg(), discardLog())

	rec, body := doOperationsRequest(t, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.VastCost.BudgetBRL != 1234.0 {
		t.Errorf("budget_brl = %v, want 1234 (from pod_config snapshot, not boot cfg 800)", body.VastCost.BudgetBRL)
	}
}

// TestOperationsHandler_ScheduleFromLiveRule (dashboard ops audit
// 2026-08-21): with a wired reconciler, the schedule section MUST read
// rec.LiveRule() — the pod_config-snapshot-backed rule the reconciler
// actually evaluates — NOT the static env schedule. Boot/env says 9→17
// DISABLED; the live snapshot says 8→19 enabled mon-fri. The panel must
// show the snapshot values.
func TestOperationsHandler_ScheduleFromLiveRule(t *testing.T) {
	fake := &fakeOperationsQueries{}
	bset := newOpBreakerSet(t, []string{"primary"})

	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	bootRule := primary.ScheduleRule{
		Timezone: loc,
		UpHour:   9, DownHour: 17,
		Days:     map[time.Weekday]bool{time.Monday: true},
		Disabled: true, // env kill-switch ON — must NOT surface
	}
	loader := podconfig.NewStaticLoaderForTest(podconfig.PodConfig{
		ScheduleUpHour:   8,
		ScheduleDownHour: 19,
		ScheduleDays:     []string{"mon", "tue", "wed", "thu", "fri"},
		ScheduleDisabled: false,
		GraceRampDownS:   300,
		ProvisionLeadS:   1800,
	}, podconfig.ScheduleRule{}, podconfig.PodConfigBounds{}, discardLog())
	rec := primary.NewReconcilerFull(primary.Deps{
		Rule:   bootRule,
		PodCfg: loader,
		Log:    discardLog(),
	})

	// cfg carries a DIFFERENT env schedule (opTestCfg: hours 0/0) so any
	// regression back to ParseScheduleEnv fails the up/down assertions.
	h := newOperationsHandlerWithQueries(fake, bset, rec, nil, nil, opTestCfg(), discardLog())

	rr, body := doOperationsRequest(t, h)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if body.Schedule.UpHour != 8 || body.Schedule.DownHour != 19 {
		t.Errorf("schedule hours = %d→%d, want 8→19 (live snapshot, not env)",
			body.Schedule.UpHour, body.Schedule.DownHour)
	}
	if body.Schedule.Disabled {
		t.Errorf("schedule disabled = true, want false (snapshot enabled must win over env kill-switch)")
	}
	if len(body.Schedule.Days) != 5 {
		t.Errorf("schedule days = %v, want 5 weekdays from the snapshot", body.Schedule.Days)
	}
}
