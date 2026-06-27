// Package admin (audit.go): GET /admin/audit handler. Emits the OBS-07
// paginated audit_log state-change feed the observability dashboard polls
// — newest-first FSM/state-change rows (event_kind IS NOT NULL), one
// compact page at a time. The dashboard never touches Postgres directly;
// it polls this endpoint (plus /admin/metrics and /admin/usage). Clones
// the UsageHandler / MetricsHandler shape exactly: query-interface
// isolation, dual constructor, OpenAI error envelope on bad input,
// admin-metric increment on every branch.
//
// The underlying ListAuditStateChanges query selects only audit_log
// *metadata* columns (ts, route, status, latency, event_kind) — never the
// audit_log_content prompts/responses (threat T-07-09).
//
// Query params:
//
//	limit=<int>    optional; default 50, capped at 200 (threat T-07-08 —
//	               a hostile caller cannot request an unbounded result set).
//	offset=<int>   optional; default 0, must be >= 0.
//	from=<date>    optional; ISO YYYY-MM-DD (BRT). Defaults to the first day
//	               of the current month (Pitfall 6 — partitions only cover
//	               recent months). Bad value → 400 invalid_date.
//	to=<date>      optional; ISO YYYY-MM-DD (BRT), exclusive end (handler
//	               adds 1 day). Defaults to the first day of next month.
//	search=<str>   optional; free-text. Bound as a single parameterized
//	               ILIKE arg over route/reason/error_code/event_kind — never
//	               string-concatenated into SQL (threat T-15-05).
//
// OBS-10 (Phase 15): the response also carries a real COUNT(*) Total so the
// /incidents pager derives honest bounds (offset+limit < total).
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// defaultAuditLimit is the page size when the caller omits ?limit.
const defaultAuditLimit = 50

// maxAuditLimit caps the page size so a hostile caller cannot request an
// unbounded result set (threat T-07-08).
const maxAuditLimit = 200

// AuditResponse is the OBS-07/OBS-10 paginated shape the dashboard polls.
// Items are newest-first (the query's ORDER BY ts DESC). Total is the real
// COUNT(*) over the same range+search predicate so the dashboard derives
// honest pager bounds (offset+limit < total) instead of a heuristic canNext.
type AuditResponse struct {
	Items  []AuditRow `json:"items"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
	Total  int64      `json:"total"`
}

// AuditRow is one audit_log state-change row. Nullable Postgres columns
// (upstream, error_code, event_kind, reason) are rendered as JSON null
// when unset via *string. CR-03: `reason` is the human-readable cause of
// a state-change row (e.g. the emergency FSM transition reason) — a
// column DEDICATED to that purpose, distinct from error_code.
type AuditRow struct {
	Ts         string  `json:"ts"`
	RequestID  string  `json:"request_id"`
	TenantID   string  `json:"tenant_id"`
	Route      string  `json:"route"`
	Method     string  `json:"method"`
	Upstream   *string `json:"upstream"`
	StatusCode int16   `json:"status_code"`
	LatencyMs  int32   `json:"latency_ms"`
	ErrorCode  *string `json:"error_code"`
	EventKind  *string `json:"event_kind"`
	Reason     *string `json:"reason"`
}

// auditQueries isolates the sqlc surface used by the handler. Test
// injection replaces this with a fake without a real pgxpool.
type auditQueries interface {
	ListAuditStateChanges(ctx context.Context, arg gen.ListAuditStateChangesParams) ([]gen.ListAuditStateChangesRow, error)
	CountAuditStateChanges(ctx context.Context, arg gen.CountAuditStateChangesParams) (int64, error)
}

// AuditHandler serves GET /admin/audit.
type AuditHandler struct {
	q   auditQueries
	log *slog.Logger
}

// NewAuditHandler wires queries + logger. Accepts the concrete *gen.Queries.
func NewAuditHandler(q *gen.Queries, log *slog.Logger) *AuditHandler {
	if log == nil {
		log = slog.Default()
	}
	return &AuditHandler{q: q, log: log.With("module", "ADMIN_AUDIT")}
}

// newAuditHandlerWithQueries is the test constructor: accepts any
// auditQueries (fake or real).
func newAuditHandlerWithQueries(q auditQueries, log *slog.Logger) *AuditHandler {
	if log == nil {
		log = slog.Default()
	}
	return &AuditHandler{q: q, log: log.With("module", "ADMIN_AUDIT")}
}

func (h *AuditHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit := defaultAuditLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_query_param",
				"limit must be a positive integer.")
			obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "4xx").Inc()
			return
		}
		// Cap at maxAuditLimit rather than rejecting — a large limit is
		// not hostile, just clamp it (threat T-07-08).
		if n > maxAuditLimit {
			n = maxAuditLimit
		}
		limit = n
	}

	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_query_param",
				"offset must be a non-negative integer.")
			obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "4xx").Inc()
			return
		}
		offset = n
	}

	// Date range (BRT) + free-text search. Unlike /admin/usage these are
	// OPTIONAL: with neither from nor to the window defaults to the current
	// BRT month (Pitfall 6 — partitions only cover recent months).
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		// Should never happen — embedded tz data in Go stdlib.
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "tz_load_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "5xx").Inc()
		return
	}
	now := time.Now().In(loc)
	fromT := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	toT := fromT.AddDate(0, 1, 0)
	if raw := r.URL.Query().Get("from"); raw != "" {
		t, perr := time.ParseInLocation("2006-01-02", raw, loc)
		if perr != nil {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_date",
				"from must be an ISO date (YYYY-MM-DD).")
			obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "4xx").Inc()
			return
		}
		fromT = t
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		t, perr := time.ParseInLocation("2006-01-02", raw, loc)
		if perr != nil {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_date",
				"to must be an ISO date (YYYY-MM-DD).")
			obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "4xx").Inc()
			return
		}
		// to is exclusive end — add 1 day so the window is [from, to+1).
		toT = t.Add(24 * time.Hour)
	}

	// searchPattern is the single parameterized ILIKE arg ($3). "%" is the
	// no-search sentinel (matches everything); a non-empty query becomes
	// "%term%". The value is bound — never concatenated into SQL (T-15-05).
	searchPattern := "%"
	if q := r.URL.Query().Get("search"); q != "" {
		searchPattern = "%" + q + "%"
	}

	rows, err := h.q.ListAuditStateChanges(ctx, gen.ListAuditStateChangesParams{
		Ts:      fromT,
		Ts_2:    toT,
		Column3: searchPattern,
		Limit:   int32(limit),
		Offset:  int32(offset),
	})
	if err != nil {
		h.log.Error("ListAuditStateChanges failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "audit_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "5xx").Inc()
		return
	}

	total, err := h.q.CountAuditStateChanges(ctx, gen.CountAuditStateChangesParams{
		Ts:      fromT,
		Ts_2:    toT,
		Column3: searchPattern,
	})
	if err != nil {
		h.log.Error("CountAuditStateChanges failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "audit_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "5xx").Inc()
		return
	}

	resp := AuditResponse{
		Items:  make([]AuditRow, 0, len(rows)),
		Limit:  limit,
		Offset: offset,
		Total:  total,
	}
	for _, row := range rows {
		resp.Items = append(resp.Items, AuditRow{
			Ts:         row.Ts.Format("2006-01-02T15:04:05Z07:00"),
			RequestID:  row.RequestID.String(),
			TenantID:   row.TenantID.String(),
			Route:      row.Route,
			Method:     row.Method,
			Upstream:   pgTextPtr(row.Upstream),
			StatusCode: row.StatusCode,
			LatencyMs:  row.LatencyMs,
			ErrorCode:  pgTextPtr(row.ErrorCode),
			EventKind:  pgTextPtr(row.EventKind),
			Reason:     pgTextPtr(row.Reason),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "2xx").Inc()
}

// pgTextPtr converts a nullable Postgres text column into a *string so
// the JSON encoder renders an unset column as null rather than "".
func pgTextPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	v := t.String
	return &v
}
