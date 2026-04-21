// Package admin (usage.go): GET /admin/usage handler. Emits the SC-3
// response shape (CONTEXT.md D-D2) directly from the authoritative
// billing_events table — NOT from usage_counters cache (which may drift).
//
// Query params:
//
//	tenant=<slug-or-uuid>   required
//	from=<ISO-date>         required (YYYY-MM-DD, interpreted in America/Sao_Paulo)
//	to=<ISO-date>           required (exclusive end; handler adds 24h)
//	granularity=day         currently ignored — day is the only shape
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// UsageResponse mirrors the SC-3 shape (CONTEXT.md D-D2 lines 166-182).
type UsageResponse struct {
	Tenant  TenantSection `json:"tenant"`
	Range   RangeSection  `json:"range"`
	Summary Summary       `json:"summary"`
	Rows    []DayRow      `json:"rows"`
}

// TenantSection is the identity portion of the response. DataClass is
// rendered as its string form ("normal"|"sensitive"); Mode as "24/7"|"peak".
type TenantSection struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	DataClass string `json:"data_class"`
	Mode      string `json:"mode"`
}

// RangeSection describes the query window. Timezone is always
// America/Sao_Paulo per CONTEXT.md D-A3.
type RangeSection struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Granularity string `json:"granularity"`
	Timezone    string `json:"timezone"`
}

// Summary is the full-range aggregate.
type Summary struct {
	TokensIn            int64   `json:"tokens_in"`
	TokensOut           int64   `json:"tokens_out"`
	AudioSeconds        float64 `json:"audio_seconds"`
	EmbedsCount         int64   `json:"embeds_count"`
	CostLocalBRL        float64 `json:"cost_local_brl"`
	CostLocalPhantomBRL float64 `json:"cost_local_phantom_brl"`
	CostExternalBRL     float64 `json:"cost_external_brl"`
	CostTotalBRL        float64 `json:"cost_total_brl"`
	RequestsCount       int64   `json:"requests_count"`
}

// DayRow is one row in the day-granularity breakdown.
type DayRow struct {
	Date                string  `json:"date"`
	TokensIn            int64   `json:"tokens_in"`
	TokensOut           int64   `json:"tokens_out"`
	AudioSeconds        float64 `json:"audio_seconds"`
	EmbedsCount         int64   `json:"embeds_count"`
	CostLocalBRL        float64 `json:"cost_local_brl"`
	CostLocalPhantomBRL float64 `json:"cost_local_phantom_brl"`
	CostExternalBRL     float64 `json:"cost_external_brl"`
	CostTotalBRL        float64 `json:"cost_total_brl"`
	RequestsCount       int64   `json:"requests_count"`
}

// usageQueries isolates the sqlc surface used by the handler. Test
// injection replaces this with a fake without a real pgxpool.
type usageQueries interface {
	GetTenantBySlug(ctx context.Context, slug string) (gen.GetTenantBySlugRow, error)
	GetTenantConfig(ctx context.Context, id uuid.UUID) (gen.GetTenantConfigRow, error)
	SumBillingEventsByDate(ctx context.Context, arg gen.SumBillingEventsByDateParams) ([]gen.SumBillingEventsByDateRow, error)
	SumBillingEventsRange(ctx context.Context, arg gen.SumBillingEventsRangeParams) (gen.SumBillingEventsRangeRow, error)
}

// UsageHandler serves GET /admin/usage.
type UsageHandler struct {
	q   usageQueries
	log *slog.Logger
}

// NewUsageHandler wires queries + logger. Accepts the concrete *gen.Queries.
func NewUsageHandler(q *gen.Queries, log *slog.Logger) *UsageHandler {
	if log == nil {
		log = slog.Default()
	}
	return &UsageHandler{q: q, log: log.With("module", "ADMIN_USAGE")}
}

// newUsageHandlerWithQueries is the test constructor: accepts any
// usageQueries (fake or real).
func newUsageHandlerWithQueries(q usageQueries, log *slog.Logger) *UsageHandler {
	if log == nil {
		log = slog.Default()
	}
	return &UsageHandler{q: q, log: log.With("module", "ADMIN_USAGE")}
}

func (h *UsageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantArg := r.URL.Query().Get("tenant")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	gran := r.URL.Query().Get("granularity")
	if gran == "" {
		gran = "day"
	}
	if tenantArg == "" || from == "" || to == "" {
		httpx.WriteOpenAIError(w, http.StatusBadRequest,
			"invalid_request_error", "missing_query_param",
			"Required query params: tenant, from, to.")
		obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "4xx").Inc()
		return
	}

	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		// Should never happen — embedded tz data in Go stdlib.
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "tz_load_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "5xx").Inc()
		return
	}
	fromT, ferr := time.ParseInLocation("2006-01-02", from, loc)
	toT, terr := time.ParseInLocation("2006-01-02", to, loc)
	if ferr != nil || terr != nil {
		httpx.WriteOpenAIError(w, http.StatusBadRequest,
			"invalid_request_error", "invalid_date",
			"from/to must be ISO dates (YYYY-MM-DD).")
		obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "4xx").Inc()
		return
	}
	// to is exclusive end — add 1 day so the query window is [from, to+1).
	toT = toT.Add(24 * time.Hour)

	tenantID, tenantRow, ok := h.resolveTenant(ctx, w, tenantArg)
	if !ok {
		return
	}

	dayRows, err := h.q.SumBillingEventsByDate(ctx, gen.SumBillingEventsByDateParams{
		TenantID: tenantID,
		Ts:       fromT,
		Ts_2:     toT,
	})
	if err != nil {
		h.log.Error("SumBillingEventsByDate failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "billing_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "5xx").Inc()
		return
	}

	sumRow, err := h.q.SumBillingEventsRange(ctx, gen.SumBillingEventsRangeParams{
		TenantID: tenantID,
		Ts:       fromT,
		Ts_2:     toT,
	})
	if err != nil {
		h.log.Error("SumBillingEventsRange failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "billing_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "5xx").Inc()
		return
	}

	sumLocalF, _ := sumRow.CostLocalBrl.Float64Value()
	sumPhantomF, _ := sumRow.CostLocalPhantomBrl.Float64Value()
	sumExternalF, _ := sumRow.CostExternalBrl.Float64Value()

	resp := UsageResponse{
		Tenant: TenantSection{
			ID:        tenantRow.ID.String(),
			Slug:      tenantRow.Slug,
			Name:      tenantRow.Name,
			DataClass: dataClassString(tenantRow.DataClass),
			Mode:      tenantRow.Mode,
		},
		Range: RangeSection{
			From: from, To: to, Granularity: gran, Timezone: "America/Sao_Paulo",
		},
		Summary: Summary{
			TokensIn:            sumRow.TokensIn,
			TokensOut:           sumRow.TokensOut,
			AudioSeconds:        float64(sumRow.AudioSeconds),
			EmbedsCount:         sumRow.EmbedsCount,
			CostLocalBRL:        sumLocalF.Float64,
			CostLocalPhantomBRL: sumPhantomF.Float64,
			CostExternalBRL:     sumExternalF.Float64,
			// cost_total = real-money costs (local GPU is fixed-cost, so
			// only cost_external is real). Phantom is a reporting-only
			// column — NOT summed into total.
			CostTotalBRL:  sumLocalF.Float64 + sumExternalF.Float64,
			RequestsCount: sumRow.RequestsCount,
		},
		Rows: make([]DayRow, 0, len(dayRows)),
	}
	for _, row := range dayRows {
		localF, _ := row.CostLocalBrl.Float64Value()
		phantomF, _ := row.CostLocalPhantomBrl.Float64Value()
		externalF, _ := row.CostExternalBrl.Float64Value()
		date := ""
		if row.Date.Valid {
			date = row.Date.Time.Format("2006-01-02")
		}
		resp.Rows = append(resp.Rows, DayRow{
			Date:                date,
			TokensIn:            row.TokensIn,
			TokensOut:           row.TokensOut,
			AudioSeconds:        float64(row.AudioSeconds),
			EmbedsCount:         row.EmbedsCount,
			CostLocalBRL:        localF.Float64,
			CostLocalPhantomBRL: phantomF.Float64,
			CostExternalBRL:     externalF.Float64,
			CostTotalBRL:        localF.Float64 + externalF.Float64,
			RequestsCount:       row.RequestsCount,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "2xx").Inc()
}

// resolveTenant looks up a tenant by UUID first, falling back to slug.
// On miss: writes 404 and returns ok=false.
func (h *UsageHandler) resolveTenant(ctx context.Context, w http.ResponseWriter, arg string) (uuid.UUID, gen.GetTenantConfigRow, bool) {
	var tenantID uuid.UUID
	var tenantRow gen.GetTenantConfigRow

	if id, err := uuid.Parse(arg); err == nil {
		tenantID = id
	} else {
		slugRow, err := h.q.GetTenantBySlug(ctx, arg)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteOpenAIError(w, http.StatusNotFound,
					"not_found_error", "tenant_not_found",
					"No tenant with that slug.")
				obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "4xx").Inc()
				return uuid.Nil, tenantRow, false
			}
			h.log.Error("GetTenantBySlug failed", "err", err)
			httpx.WriteOpenAIError(w, http.StatusInternalServerError,
				"api_error", "tenant_lookup_failed", "")
			obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "5xx").Inc()
			return uuid.Nil, tenantRow, false
		}
		tenantID = slugRow.ID
	}

	cfg, err := h.q.GetTenantConfig(ctx, tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteOpenAIError(w, http.StatusNotFound,
				"not_found_error", "tenant_not_found",
				"No tenant with that ID.")
			obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "4xx").Inc()
			return uuid.Nil, tenantRow, false
		}
		h.log.Error("GetTenantConfig failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "tenant_lookup_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/usage", "5xx").Inc()
		return uuid.Nil, tenantRow, false
	}
	return tenantID, cfg, true
}

// dataClassString normalizes the interface{} shape sqlc uses for the
// pg_enum data_class column ("normal"|"sensitive"). Falls back to the
// zero string if the driver returned something unexpected.
func dataClassString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case nil:
		return ""
	default:
		return ""
	}
}
