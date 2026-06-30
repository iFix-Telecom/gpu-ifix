// Package admin (lifecycle.go): GET /admin/primary/lifecycle handler. Serves
// the live-status panel poll (Plan 17-04, D-05): the current primary FSM
// state + leadership, the emergency FSM state, and the OPEN primary lifecycle's
// event trail (started_at, first_health_pass_at, drain_started_at, ended_at,
// trigger_reason, accepted_dph, total_cost_brl, shutdown_reason, events jsonb).
//
// This is NOT a history table (D-05): it returns ONLY the single open
// lifecycle (GetOpenPrimaryLifecycle → at most one row by the
// primary_live_singleton unique index), or null when the pod is asleep.
//
// Mirrors OperationsHandler exactly: query-interface isolation, dual
// constructor, nil-safe reconciler (Vast off → fsm_state "unknown"), OpenAI
// error envelope on query failure, admin-metric increment per branch.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

const lifecycleRoute = "/admin/primary/lifecycle"

// LifecycleResponse is the live-status contract polled by the dashboard.
// The Go struct is the source of truth (consumed field-for-field by Plan
// 17-05). open_lifecycle is null when no primary lifecycle is open.
type LifecycleResponse struct {
	FSMState       string                `json:"fsm_state"`
	Leader         bool                  `json:"leader"`
	EmergencyState string                `json:"emergency_state"`
	OpenLifecycle  *OpenLifecycleSection `json:"open_lifecycle"`
}

// OpenLifecycleSection is the OPEN primary lifecycle's event trail. Nullable
// pgtype columns render as JSON null (not zero) so the dashboard distinguishes
// "not yet computed" from a real zero.
type OpenLifecycleSection struct {
	ID                int64           `json:"id"`
	TriggerReason     string          `json:"trigger_reason"`
	StartedAt         string          `json:"started_at"` // RFC3339
	FirstHealthPassAt *string         `json:"first_health_pass_at"`
	DrainStartedAt    *string         `json:"drain_started_at"`
	EndedAt           *string         `json:"ended_at"` // null = still running
	AcceptedDPH       *float64        `json:"accepted_dph"`
	TotalCostBRL      *float64        `json:"total_cost_brl"`
	ShutdownReason    *string         `json:"shutdown_reason"`
	Events            json.RawMessage `json:"events"` // jsonb event trail; null when empty
}

// lifecycleQueries isolates the sqlc surface used by the handler. Test
// injection replaces this with a fake without a real pgxpool.
type lifecycleQueries interface {
	GetOpenPrimaryLifecycle(ctx context.Context) (gen.AiGatewayPrimaryLifecycle, error)
}

// PrimaryLifecycleHandler serves GET /admin/primary/lifecycle.
type PrimaryLifecycleHandler struct {
	q        lifecycleQueries
	rec      *primary.Reconciler // nil-safe: Vast off
	emergFSM *emerg.FSM          // nil-safe
	log      *slog.Logger
}

// NewPrimaryLifecycleHandler wires the production dependencies. rec and
// emergFSM may be nil when Vast/Phase-6 is disabled — the handler reports
// "unknown" rather than panicking.
func NewPrimaryLifecycleHandler(q *gen.Queries, rec *primary.Reconciler,
	emergFSM *emerg.FSM, log *slog.Logger) *PrimaryLifecycleHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PrimaryLifecycleHandler{
		q:        q,
		rec:      rec,
		emergFSM: emergFSM,
		log:      log.With("module", "ADMIN_PRIMARY_LIFECYCLE"),
	}
}

// newPrimaryLifecycleHandlerWithQueries is the test constructor: accepts any
// lifecycleQueries (fake or real) plus the rest of the deps.
func newPrimaryLifecycleHandlerWithQueries(q lifecycleQueries, rec *primary.Reconciler,
	emergFSM *emerg.FSM, log *slog.Logger) *PrimaryLifecycleHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PrimaryLifecycleHandler{
		q:        q,
		rec:      rec,
		emergFSM: emergFSM,
		log:      log.With("module", "ADMIN_PRIMARY_LIFECYCLE"),
	}
}

func (h *PrimaryLifecycleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	resp := LifecycleResponse{
		FSMState:       "unknown",
		EmergencyState: fsmStateString(h.emergFSM),
	}
	if h.rec != nil {
		snap := h.rec.Snapshot()
		resp.FSMState = snap.State
		resp.Leader = snap.IsLeader
	}

	row, err := h.q.GetOpenPrimaryLifecycle(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No open lifecycle — pod asleep. open_lifecycle stays null.
			h.writeJSON(w, resp)
			return
		}
		h.log.Error("GetOpenPrimaryLifecycle failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "lifecycle_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(lifecycleRoute, "5xx").Inc()
		return
	}
	resp.OpenLifecycle = openLifecycleToJSON(row)
	h.writeJSON(w, resp)
}

func (h *PrimaryLifecycleHandler) writeJSON(w http.ResponseWriter, resp LifecycleResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	obs.GatewayAdminRequests.WithLabelValues(lifecycleRoute, "2xx").Inc()
}

// openLifecycleToJSON maps the open lifecycle row's event-trail columns into
// the response section, rendering nullable pgtype columns as JSON null and an
// empty jsonb events column as null.
func openLifecycleToJSON(row gen.AiGatewayPrimaryLifecycle) *OpenLifecycleSection {
	sec := &OpenLifecycleSection{
		ID:                row.ID,
		TriggerReason:     row.TriggerReason,
		StartedAt:         row.StartedAt.Format(time.RFC3339),
		FirstHealthPassAt: timestamptzPtr(row.FirstHealthPassAt),
		DrainStartedAt:    timestamptzPtr(row.DrainStartedAt),
		EndedAt:           timestamptzPtr(row.EndedAt),
		AcceptedDPH:       numericPtr(row.AcceptedDph),
		TotalCostBRL:      numericPtr(row.TotalCostBrl),
		ShutdownReason:    pgTextPtr(row.ShutdownReason),
	}
	if len(row.Events) > 0 {
		sec.Events = json.RawMessage(row.Events)
	}
	return sec
}
