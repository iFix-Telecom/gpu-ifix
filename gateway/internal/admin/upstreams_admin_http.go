// Package admin (upstreams_admin_http.go): admin HTTP surface for the
// upstreams table (quick 260830-o2j) — the dashboard counterpart of
// `gatewayctl upstreams list|enable|disable`.
//
//	GET  /admin/upstreams                   → every row (enabled or not) with probe state
//	POST /admin/upstreams/{name}/enabled {enabled:bool} → 204
//
// The UPDATE fires the 0009 notify_upstreams_changed trigger, so the running
// gateway hot-reloads its dispatcher roster exactly like the CLI path.
//
// Safety: disabling the LAST enabled upstream of a role is refused (409
// last_enabled_upstream) — it would leave that route with no candidate at
// all. Re-enabling is always allowed.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const upstreamsAdminRoute = "/admin/upstreams"

type upstreamAdminQueries interface {
	ListAllUpstreams(ctx context.Context) ([]gen.ListAllUpstreamsRow, error)
	GetUpstreamByName(ctx context.Context, name string) (gen.GetUpstreamByNameRow, error)
	SetUpstreamEnabled(ctx context.Context, arg gen.SetUpstreamEnabledParams) error
}

// UpstreamRow is the typed API item. Env var NAMES are exposed (they are
// config identifiers, not secrets); the resolved URL / bearer VALUES never
// leave the gateway.
type UpstreamRow struct {
	Name            string  `json:"name"`
	Role            string  `json:"role"`
	Tier            int32   `json:"tier"`
	TierPriority    int32   `json:"tier_priority"`
	Enabled         bool    `json:"enabled"`
	URLEnv          string  `json:"url_env"`
	HasAuth         bool    `json:"has_auth"`
	LastProbeAt     *string `json:"last_probe_at"`
	LastProbeMs     *int32  `json:"last_probe_ms"`
	LastProbeStatus *string `json:"last_probe_status"`
	LastProbeError  *string `json:"last_probe_error"`
	UpdatedAt       string  `json:"updated_at"`
}

type setEnabledRequest struct {
	Enabled *bool `json:"enabled"`
}

// UpstreamAdminHandler serves GET /admin/upstreams + POST .../{name}/enabled.
type UpstreamAdminHandler struct {
	q   upstreamAdminQueries
	log *slog.Logger
}

// NewUpstreamAdminHandler wires the production dependency.
func NewUpstreamAdminHandler(q *gen.Queries, log *slog.Logger) *UpstreamAdminHandler {
	return newUpstreamAdminHandlerWithQueries(q, log)
}

func newUpstreamAdminHandlerWithQueries(q upstreamAdminQueries, log *slog.Logger) *UpstreamAdminHandler {
	if log == nil {
		log = slog.Default()
	}
	return &UpstreamAdminHandler{q: q, log: log.With("module", "ADMIN_UPSTREAMS")}
}

func toUpstreamRow(u gen.ListAllUpstreamsRow) UpstreamRow {
	row := UpstreamRow{
		Name:         u.Name,
		Role:         u.Role,
		Tier:         u.Tier,
		TierPriority: u.TierPriority,
		Enabled:      u.Enabled,
		URLEnv:       u.UrlEnv,
		HasAuth:      u.AuthBearerEnv.Valid && u.AuthBearerEnv.String != "",
		UpdatedAt:    u.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if u.LastProbeAt.Valid {
		s := u.LastProbeAt.Time.UTC().Format(time.RFC3339)
		row.LastProbeAt = &s
	}
	if u.LastProbeMs.Valid {
		v := u.LastProbeMs.Int32
		row.LastProbeMs = &v
	}
	if u.LastProbeStatus.Valid {
		s := u.LastProbeStatus.String
		row.LastProbeStatus = &s
	}
	if u.LastProbeError.Valid {
		s := u.LastProbeError.String
		row.LastProbeError = &s
	}
	return row
}

// List serves GET /admin/upstreams.
func (h *UpstreamAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q.ListAllUpstreams(r.Context())
	if err != nil {
		h.log.Error("ListAllUpstreams failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "upstreams_list_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "5xx").Inc()
		return
	}
	out := make([]UpstreamRow, 0, len(rows))
	for _, u := range rows {
		out = append(out, toUpstreamRow(u))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
	obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "2xx").Inc()
}

// SetEnabled serves POST /admin/upstreams/{name}/enabled.
func (h *UpstreamAdminHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validateAliasField("name", name, modelAliasMaxUpstreamLen); err != nil {
		httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_upstream", err.Error())
		obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "4xx").Inc()
		return
	}
	var body setEnabledRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Enabled == nil {
		httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body", `body must be {"enabled": true|false}`)
		obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "4xx").Inc()
		return
	}
	enabled := *body.Enabled

	target, err := h.q.GetUpstreamByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "upstream_not_found",
				fmt.Sprintf("upstream %q não existe", name))
			obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "4xx").Inc()
			return
		}
		h.log.Error("GetUpstreamByName failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "upstream_update_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "5xx").Inc()
		return
	}

	if !enabled && target.Enabled {
		// Guard: never leave a role with zero enabled upstreams.
		all, err := h.q.ListAllUpstreams(r.Context())
		if err != nil {
			h.log.Error("ListAllUpstreams failed", "err", err)
			httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "upstream_update_failed", "")
			obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "5xx").Inc()
			return
		}
		others := 0
		for _, u := range all {
			if u.Role == target.Role && u.Enabled && u.Name != target.Name {
				others++
			}
		}
		if others == 0 {
			httpx.WriteOpenAIError(w, http.StatusConflict, "invalid_request_error", "last_enabled_upstream",
				fmt.Sprintf("%q é o único upstream habilitado do role %q — desabilitar deixaria a rota sem candidato", name, target.Role))
			obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "4xx").Inc()
			return
		}
	}

	if err := h.q.SetUpstreamEnabled(r.Context(), gen.SetUpstreamEnabledParams{Name: name, Enabled: enabled}); err != nil {
		h.log.Error("SetUpstreamEnabled failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "upstream_update_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "5xx").Inc()
		return
	}
	h.log.Info("upstream enabled flag set", "upstream", name, "role", target.Role, "enabled", enabled)
	w.WriteHeader(http.StatusNoContent)
	obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "2xx").Inc()
}
