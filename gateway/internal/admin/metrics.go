// Package admin (metrics.go): GET /admin/metrics handler. Emits the
// OBS-01 aggregated JSON the observability dashboard polls — per-tenant
// P50/P95/P99 + error rate (computed natively in Postgres via
// percentile_cont, NOT from a Prometheus label — see 07-RESEARCH Pitfall
// 1), the current emergency FSM state, and per-upstream inflight read
// from the GatewayInflight gauge. The dashboard never touches Postgres
// or Prometheus directly — it polls this endpoint (plus /admin/usage and
// /admin/audit). Clones the UsageHandler shape in usage.go exactly:
// query-interface isolation, dual constructor, OpenAI error envelope on
// bad input, admin-metric increment on every branch.
//
// Query params:
//
//	window=<duration>   optional (Go duration string, e.g. "5m"); default 5m.
//	                    The handler passes NOW()-window to the percentile query.
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// defaultMetricsWindow is the look-back window for the per-tenant
// percentile query when the caller omits ?window. 07-RESEARCH RESOLVED:
// "5-minute rolling window computed from audit_log via percentile_cont".
const defaultMetricsWindow = 5 * time.Minute

// maxMetricsWindow caps the look-back so a hostile caller cannot make the
// percentile query scan an unbounded slice of audit_log (threat T-07-08).
const maxMetricsWindow = 24 * time.Hour

// MetricsResponse is the OBS-01 aggregated shape the dashboard polls.
type MetricsResponse struct {
	Window   string             `json:"window"`
	FSMState string             `json:"fsm_state"`
	Tenants  []TenantLatencyRow `json:"tenants"`
	Inflight []InflightRow      `json:"inflight"`
}

// TenantLatencyRow is one per-(tenant,route) percentile row, sourced from
// the TenantLatencyPercentiles sqlc query.
//
// WR-10: TenantSlug + TenantName carry the human-readable identifiers from
// the LEFT JOIN on ai_gateway.tenants. They are JSON null when the audit
// row's tenant no longer exists in the tenants table (deleted tenant); the
// dashboard falls back to TenantID in that case. TenantID is always a
// non-null raw UUID — the stable join key and the fallback label.
type TenantLatencyRow struct {
	TenantID   string  `json:"tenant_id"`
	TenantSlug *string `json:"tenant_slug"`
	TenantName *string `json:"tenant_name"`
	Route      string  `json:"route"`
	P50        float64 `json:"p50"`
	P95        float64 `json:"p95"`
	P99        float64 `json:"p99"`
	Requests   int64   `json:"requests"`
	ErrorRate  float64 `json:"error_rate"`
}

// InflightRow is the current in-flight request count for one upstream,
// read from the GatewayInflight gauge.
type InflightRow struct {
	Upstream string  `json:"upstream"`
	Inflight float64 `json:"inflight"`
}

// metricsQueries isolates the sqlc surface used by the handler. Test
// injection replaces this with a fake without a real pgxpool.
type metricsQueries interface {
	TenantLatencyPercentiles(ctx context.Context, ts time.Time) ([]gen.TenantLatencyPercentilesRow, error)
}

// MetricsHandler serves GET /admin/metrics.
type MetricsHandler struct {
	q   metricsQueries
	fsm *emerg.FSM
	log *slog.Logger
}

// NewMetricsHandler wires queries + FSM + logger. Accepts the concrete
// *gen.Queries.
func NewMetricsHandler(q *gen.Queries, fsm *emerg.FSM, log *slog.Logger) *MetricsHandler {
	if log == nil {
		log = slog.Default()
	}
	return &MetricsHandler{q: q, fsm: fsm, log: log.With("module", "ADMIN_METRICS")}
}

// newMetricsHandlerWithQueries is the test constructor: accepts any
// metricsQueries (fake or real).
func newMetricsHandlerWithQueries(q metricsQueries, fsm *emerg.FSM, log *slog.Logger) *MetricsHandler {
	if log == nil {
		log = slog.Default()
	}
	return &MetricsHandler{q: q, fsm: fsm, log: log.With("module", "ADMIN_METRICS")}
}

func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	window := defaultMetricsWindow
	if raw := r.URL.Query().Get("window"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 || d > maxMetricsWindow {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_query_param",
				"window must be a positive Go duration no greater than 24h (e.g. 5m).")
			obs.GatewayAdminRequests.WithLabelValues("/admin/metrics", "4xx").Inc()
			return
		}
		window = d
	}

	rows, err := h.q.TenantLatencyPercentiles(ctx, time.Now().Add(-window))
	if err != nil {
		h.log.Error("TenantLatencyPercentiles failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "metrics_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/metrics", "5xx").Inc()
		return
	}

	resp := MetricsResponse{
		Window:   window.String(),
		FSMState: fsmStateString(h.fsm),
		Tenants:  make([]TenantLatencyRow, 0, len(rows)),
		Inflight: readInflight(),
	}
	for _, row := range rows {
		resp.Tenants = append(resp.Tenants, TenantLatencyRow{
			TenantID:   row.TenantID.String(),
			TenantSlug: pgTextPtr(row.TenantSlug),
			TenantName: pgTextPtr(row.TenantName),
			Route:      row.Route,
			P50:        row.P50,
			P95:        row.P95,
			P99:        row.P99,
			Requests:   row.Requests,
			ErrorRate:  row.ErrorRate,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	obs.GatewayAdminRequests.WithLabelValues("/admin/metrics", "2xx").Inc()
}

// fsmStateString returns the emergency FSM state name, or "unknown" when
// the FSM was not wired (nil during early-boot or in tests that do not
// need it). State() is a lockless atomic read — safe to call here.
func fsmStateString(fsm *emerg.FSM) string {
	if fsm == nil {
		return "unknown"
	}
	return fsm.State().String()
}

// readInflight reads the current value of every label of the
// GatewayInflight gauge. A GaugeVec is not directly indexable by value,
// so we Collect each child Metric, Write it into its protobuf dto form,
// and read the gauge value + the "upstream" label off each.
//
// WR-02: Collect is synchronous and writes EVERY child series into the
// channel. The upstream set is hot-reloadable (shedInflight.AddUpstream)
// with no enforced ceiling — so a fixed buffer is not a correctness
// guarantee: if the series count ever exceeds the buffer, Collect blocks
// forever on a full channel and deadlocks the request goroutine. Running
// Collect in its own goroutine and closing the channel from there means
// a full channel can never block the request: the drain `for range`
// below keeps the buffer moving, and the goroutine closes the channel
// once Collect returns. The buffer is just to reduce goroutine handoffs.
func readInflight() []InflightRow {
	ch := make(chan prometheus.Metric, 256)
	go func() {
		obs.GatewayInflight.Collect(ch)
		close(ch)
	}()

	out := make([]InflightRow, 0)
	for m := range ch {
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			continue
		}
		row := InflightRow{}
		for _, lp := range pb.GetLabel() {
			if lp.GetName() == "upstream" {
				row.Upstream = lp.GetValue()
			}
		}
		if g := pb.GetGauge(); g != nil {
			row.Inflight = g.GetValue()
		}
		out = append(out, row)
	}
	return out
}
