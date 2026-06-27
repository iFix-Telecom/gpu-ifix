// Package admin (economy.go): GET /admin/economy handler. Computes — server-side
// — the OBS-09 "Economia" panel numbers that prove whether running the own GPU
// (Vast) actually saves money vs the OpenRouter fallback, plus a daily time
// series. Clones the UsageHandler shape exactly: query-interface isolation,
// dual constructor, OpenAI error envelope on failure, admin-metric increment
// per branch, and the America/Sao_Paulo tz idiom.
//
// The five locked summary metrics (CONTEXT.md):
//
//	economia_liquida_brl = phantom_brl − vast_brl
//	roi_multiplier       = phantom_brl / vast_brl  (null when vast_brl == 0)
//	custo_openrouter_brl = SUM(cost_external_brl)  (real external spend, pod DOWN)
//	pct_servido_local    = local_requests / total_requests (null when total == 0)
//	horas_pod_up         = SUM over lifecycles of active hours
//
// The gateway-wide phantom sum (no tenant filter) is the OBS-09 blocker fix:
// operations.go left phantom_month_brl nil because no no-tenant-filter sqlc
// query existed. The Vast cost + pod-up hours reuse the operations.go accrual.
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// EconomyResponse is the aggregated shape the dashboard's "Economia" panel
// fetches. Mirrored field-for-field by the dashboard's TS type.
type EconomyResponse struct {
	Range   EconomyRange    `json:"range"`
	Summary EconomySummary  `json:"summary"`
	Series  []EconomyDayRow `json:"series"`
}

// EconomyRange describes the query window. Timezone is always
// America/Sao_Paulo (CONTEXT A3).
type EconomyRange struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Timezone string `json:"timezone"`
}

// EconomySummary is the full-range aggregate carrying all five locked metrics.
// ROIMultiplier and PctServidoLocal are *float64 so they render JSON null
// (never Inf/NaN) when their denominator is zero.
type EconomySummary struct {
	PhantomBRL         float64  `json:"phantom_brl"`
	VastBRL            float64  `json:"vast_brl"`
	EconomiaLiquidaBRL float64  `json:"economia_liquida_brl"`
	ROIMultiplier      *float64 `json:"roi_multiplier"`
	CustoOpenRouterBRL float64  `json:"custo_openrouter_brl"`
	PctServidoLocal    *float64 `json:"pct_servido_local"`
	HorasPodUp         float64  `json:"horas_pod_up"`
}

// EconomyDayRow is one entry in the daily series (economia = phantom − vast
// for that BRT day).
type EconomyDayRow struct {
	Date        string  `json:"date"`
	PhantomBrl  float64 `json:"phantom_brl"`
	VastBrl     float64 `json:"vast_brl"`
	EconomiaBrl float64 `json:"economia_brl"`
}

// economyQueries isolates the sqlc surface used by the handler. Test injection
// replaces this with a fake without a real pgxpool.
type economyQueries interface {
	SumPhantomAllTenantsByDate(ctx context.Context, arg gen.SumPhantomAllTenantsByDateParams) ([]gen.SumPhantomAllTenantsByDateRow, error)
	SumBillingAllTenantsRange(ctx context.Context, arg gen.SumBillingAllTenantsRangeParams) (gen.SumBillingAllTenantsRangeRow, error)
	ListPrimaryLifecyclesInRange(ctx context.Context, arg gen.ListPrimaryLifecyclesInRangeParams) ([]gen.ListPrimaryLifecyclesInRangeRow, error)
}

// EconomyHandler serves GET /admin/economy.
type EconomyHandler struct {
	q   economyQueries
	cfg config.Config
	log *slog.Logger
}

// NewEconomyHandler wires queries + config + logger. Accepts the concrete
// *gen.Queries. cfg supplies USDToBRLRate for the open-lifecycle accrual.
func NewEconomyHandler(q *gen.Queries, cfg config.Config, log *slog.Logger) *EconomyHandler {
	if log == nil {
		log = slog.Default()
	}
	return &EconomyHandler{q: q, cfg: cfg, log: log.With("module", "ADMIN_ECONOMY")}
}

// newEconomyHandlerWithQueries is the test constructor: accepts any
// economyQueries (fake or real).
func newEconomyHandlerWithQueries(q economyQueries, cfg config.Config, log *slog.Logger) *EconomyHandler {
	if log == nil {
		log = slog.Default()
	}
	return &EconomyHandler{q: q, cfg: cfg, log: log.With("module", "ADMIN_ECONOMY")}
}

func (h *EconomyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		// Should never happen — embedded tz data in Go stdlib.
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "tz_load_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "5xx").Inc()
		return
	}

	now := time.Now()
	nowLoc := now.In(loc)
	var fromT, toT time.Time

	// Default range = current month (BRT) when from/to absent.
	if from == "" {
		fromT = time.Date(nowLoc.Year(), nowLoc.Month(), 1, 0, 0, 0, 0, loc)
		from = fromT.Format("2006-01-02")
	} else {
		var ferr error
		fromT, ferr = time.ParseInLocation("2006-01-02", from, loc)
		if ferr != nil {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_date",
				"from/to must be ISO dates (YYYY-MM-DD).")
			obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "4xx").Inc()
			return
		}
	}
	if to == "" {
		toDay := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), 0, 0, 0, 0, loc)
		to = toDay.Format("2006-01-02")
		toT = toDay.Add(24 * time.Hour)
	} else {
		parsed, terr := time.ParseInLocation("2006-01-02", to, loc)
		if terr != nil {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_date",
				"from/to must be ISO dates (YYYY-MM-DD).")
			obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "4xx").Inc()
			return
		}
		// to is exclusive end — add 1 day so the window is [from, to+1).
		toT = parsed.Add(24 * time.Hour)
	}

	// Gateway-wide range summary (no tenant filter).
	sumRow, err := h.q.SumBillingAllTenantsRange(ctx, gen.SumBillingAllTenantsRangeParams{
		Ts:   fromT,
		Ts_2: toT,
	})
	if err != nil {
		h.log.Error("SumBillingAllTenantsRange failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "billing_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "5xx").Inc()
		return
	}

	// Gateway-wide per-day phantom series.
	dayRows, err := h.q.SumPhantomAllTenantsByDate(ctx, gen.SumPhantomAllTenantsByDateParams{
		Ts:   fromT,
		Ts_2: toT,
	})
	if err != nil {
		h.log.Error("SumPhantomAllTenantsByDate failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "billing_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "5xx").Inc()
		return
	}

	// Lifecycles overlapping the window — for the Vast accrual + pod-up hours.
	lcRows, err := h.q.ListPrimaryLifecyclesInRange(ctx, gen.ListPrimaryLifecyclesInRangeParams{
		StartedAt:   fromT,
		StartedAt_2: toT,
	})
	if err != nil {
		h.log.Error("ListPrimaryLifecyclesInRange failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "lifecycles_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "5xx").Inc()
		return
	}

	// Vast cost + pod-up hours reduction (operations.go:214-245 accrual,
	// reusing numericPtr). A CLOSED row carries the billing-of-record
	// total_cost_brl and ended−started hours; an OPEN row accrues
	// accepted_dph × hours-since-started × USD→BRL and now−started hours.
	// Each lifecycle's vast cost buckets into its started_at BRT date (one
	// lifecycle = one BRT day, CONTEXT A1).
	var vastTotal, horasPodUp float64
	vastByDate := make(map[string]float64)
	for _, row := range lcRows {
		var cost, hours float64
		if row.EndedAt.Valid {
			hours = row.EndedAt.Time.Sub(row.StartedAt).Hours()
			if hours < 0 {
				hours = 0
			}
			if f := numericPtr(row.TotalCostBrl); f != nil {
				cost = *f
			}
		} else {
			hours = now.Sub(row.StartedAt).Hours()
			if hours < 0 {
				hours = 0
			}
			if dph := numericPtr(row.AcceptedDph); dph != nil {
				cost = *dph * hours * h.cfg.USDToBRLRate
			}
		}
		vastTotal += cost
		horasPodUp += hours
		vastByDate[row.StartedAt.In(loc).Format("2006-01-02")] += cost
	}

	// Phantom series → map; merge with the per-day vast buckets.
	phantomByDate := make(map[string]float64)
	dateSet := make(map[string]struct{})
	for _, row := range dayRows {
		date := ""
		if row.Date.Valid {
			date = row.Date.Time.Format("2006-01-02")
		}
		pf, _ := row.PhantomBrl.Float64Value()
		phantomByDate[date] = pf.Float64
		dateSet[date] = struct{}{}
	}
	for d := range vastByDate {
		dateSet[d] = struct{}{}
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	series := make([]EconomyDayRow, 0, len(dates))
	for _, d := range dates {
		p := phantomByDate[d]
		v := vastByDate[d]
		series = append(series, EconomyDayRow{
			Date:        d,
			PhantomBrl:  p,
			VastBrl:     v,
			EconomiaBrl: p - v,
		})
	}

	// Summary metrics.
	phantomF, _ := sumRow.PhantomBrl.Float64Value()
	externalF, _ := sumRow.ExternalBrl.Float64Value()
	phantom := phantomF.Float64
	custoOpenRouter := externalF.Float64

	var roi *float64
	if vastTotal != 0 {
		r := phantom / vastTotal
		roi = &r
	}
	var pctLocal *float64
	if sumRow.TotalRequests != 0 {
		p := float64(sumRow.LocalRequests) / float64(sumRow.TotalRequests)
		pctLocal = &p
	}

	resp := EconomyResponse{
		Range: EconomyRange{
			From:     from,
			To:       to,
			Timezone: "America/Sao_Paulo",
		},
		Summary: EconomySummary{
			PhantomBRL:         phantom,
			VastBRL:            vastTotal,
			EconomiaLiquidaBRL: phantom - vastTotal,
			ROIMultiplier:      roi,
			CustoOpenRouterBRL: custoOpenRouter,
			PctServidoLocal:    pctLocal,
			HorasPodUp:         horasPodUp,
		},
		Series: series,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "2xx").Inc()
}
