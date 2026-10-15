// Package admin (aux_pod_lifecycles_http.go): POST /admin/aux-pod-lifecycles
// (Phase 23, 23-11, HAIKU-09). Caminho de escrita do custo de pods Vast
// auxiliares que vivem fora do reconciler do primary — hoje a 3060 unificada,
// cujo ciclo diário up→destroy é feito pelo unified3060.py no ops-claude. O
// script publica start/stop aqui (o host não tem credencial de banco).
//
//	POST /admin/aux-pod-lifecycles
//	  {"pod":"vast-3060","event":"start"|"stop","vast_instance_id":123,
//	   "dph_usd":0.12,"at":"RFC3339 opcional","total_cost_usd":1.23 (stop, opcional)}
//	→ 200 {"id":N,"event":"start"|"stop"}
//	→ 200 {"id":N,"event":"stop","already_closed":true}  (stop repetido; custo inalterado)
//	→ 400 validação · 404 stop de (pod, vast_instance_id) que nunca teve start
//
// Idempotência (o publisher é best-effort e repete quando a resposta HTTP se
// perde): start repetido devolve o mesmo id e mantém o started_at mais antigo;
// stop repetido não sobrescreve o custo gravado.
//
// Autenticação: o handler NÃO valida X-Admin-Key — a rota é montada dentro do
// adminRouter (middleware de admin key) pelo 23-14 (T-23-34).
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const (
	auxPodLifecyclesRoute = "/admin/aux-pod-lifecycles"
	auxPodMaxBodyBytes    = 64 << 10
	auxPodMaxPodLen       = 64
)

type auxPodLifecycleQueries interface {
	StartAuxPodLifecycle(ctx context.Context, arg gen.StartAuxPodLifecycleParams) (int64, error)
	GetAuxPodLifecycle(ctx context.Context, arg gen.GetAuxPodLifecycleParams) (gen.GetAuxPodLifecycleRow, error)
	CloseAuxPodLifecycle(ctx context.Context, arg gen.CloseAuxPodLifecycleParams) (int64, error)
}

type auxPodLifecycleRequest struct {
	Pod            string   `json:"pod"`
	Event          string   `json:"event"`
	VastInstanceID int64    `json:"vast_instance_id"`
	DphUSD         *float64 `json:"dph_usd"`
	At             string   `json:"at"`
	TotalCostUSD   *float64 `json:"total_cost_usd"`
}

type auxPodLifecycleResponse struct {
	ID            int64  `json:"id"`
	Event         string `json:"event"`
	AlreadyClosed bool   `json:"already_closed,omitempty"`
}

// AuxPodLifecycleHandler serve POST /admin/aux-pod-lifecycles.
type AuxPodLifecycleHandler struct {
	q   auxPodLifecycleQueries
	cfg config.Config
	log *slog.Logger
	now func() time.Time
}

// NewAuxPodLifecycleHandler liga a dependência de produção. cfg fornece
// USDToBRLRate para o total_cost_brl.
func NewAuxPodLifecycleHandler(q *gen.Queries, cfg config.Config, log *slog.Logger) *AuxPodLifecycleHandler {
	return newAuxPodLifecycleHandlerWithQueries(q, cfg, log)
}

func newAuxPodLifecycleHandlerWithQueries(q auxPodLifecycleQueries, cfg config.Config, log *slog.Logger) *AuxPodLifecycleHandler {
	if log == nil {
		log = slog.Default()
	}
	return &AuxPodLifecycleHandler{q: q, cfg: cfg, log: log.With("module", "ADMIN_AUX_POD_LIFECYCLES"), now: time.Now}
}

func (h *AuxPodLifecycleHandler) badRequest(w http.ResponseWriter, code, msg string) {
	httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", code, msg)
	obs.GatewayAdminRequests.WithLabelValues(auxPodLifecyclesRoute, "4xx").Inc()
}

func (h *AuxPodLifecycleHandler) serverError(w http.ResponseWriter, code string) {
	httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", code, "")
	obs.GatewayAdminRequests.WithLabelValues(auxPodLifecyclesRoute, "5xx").Inc()
}

func (h *AuxPodLifecycleHandler) ok(w http.ResponseWriter, resp auxPodLifecycleResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	obs.GatewayAdminRequests.WithLabelValues(auxPodLifecyclesRoute, "2xx").Inc()
}

func (h *AuxPodLifecycleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req auxPodLifecycleRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, auxPodMaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		h.badRequest(w, "invalid_body", "body must be a JSON object ≤ 64 KiB with pod/event/vast_instance_id.")
		return
	}

	req.Pod = strings.TrimSpace(req.Pod)
	switch {
	case req.Pod == "" || len(req.Pod) > auxPodMaxPodLen:
		h.badRequest(w, "invalid_pod", "pod must be non-empty (≤ 64 chars).")
		return
	case req.Event != "start" && req.Event != "stop":
		h.badRequest(w, "invalid_event", `event must be "start" or "stop".`)
		return
	case req.VastInstanceID <= 0:
		h.badRequest(w, "invalid_vast_instance_id", "vast_instance_id must be > 0.")
		return
	case req.DphUSD != nil && *req.DphUSD < 0:
		h.badRequest(w, "invalid_dph_usd", "dph_usd must be ≥ 0.")
		return
	case req.TotalCostUSD != nil && *req.TotalCostUSD < 0:
		h.badRequest(w, "invalid_total_cost_usd", "total_cost_usd must be ≥ 0.")
		return
	}

	at := h.now()
	if req.At != "" {
		parsed, err := time.Parse(time.RFC3339, req.At)
		if err != nil {
			h.badRequest(w, "invalid_at", "at must be RFC3339.")
			return
		}
		at = parsed
	}
	inst := pgtype.Int8{Int64: req.VastInstanceID, Valid: true}

	if req.Event == "start" {
		h.start(r.Context(), w, req, inst, at)
		return
	}
	h.stop(r.Context(), w, req, inst, at)
}

func (h *AuxPodLifecycleHandler) start(ctx context.Context, w http.ResponseWriter, req auxPodLifecycleRequest, inst pgtype.Int8, at time.Time) {
	var dph pgtype.Numeric
	if req.DphUSD != nil {
		n, err := toNumeric(*req.DphUSD)
		if err != nil {
			h.badRequest(w, "invalid_dph_usd", "dph_usd must be a finite number.")
			return
		}
		dph = n
	}
	id, err := h.q.StartAuxPodLifecycle(ctx, gen.StartAuxPodLifecycleParams{
		Pod:            req.Pod,
		VastInstanceID: inst,
		StartedAt:      at,
		DphUsd:         dph,
	})
	if err != nil {
		h.log.Error("StartAuxPodLifecycle failed", "err", err, "pod", req.Pod, "vast_instance_id", req.VastInstanceID)
		h.serverError(w, "aux_pod_lifecycle_write_failed")
		return
	}
	h.log.Info("aux pod lifecycle start", "id", id, "pod", req.Pod, "vast_instance_id", req.VastInstanceID, "started_at", at)
	h.ok(w, auxPodLifecycleResponse{ID: id, Event: "start"})
}

func (h *AuxPodLifecycleHandler) stop(ctx context.Context, w http.ResponseWriter, req auxPodLifecycleRequest, inst pgtype.Int8, at time.Time) {
	row, err := h.q.GetAuxPodLifecycle(ctx, gen.GetAuxPodLifecycleParams{Pod: req.Pod, VastInstanceID: inst})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "aux_pod_lifecycle_not_found",
			"no start recorded for this (pod, vast_instance_id).")
		obs.GatewayAdminRequests.WithLabelValues(auxPodLifecyclesRoute, "4xx").Inc()
		return
	}
	if err != nil {
		h.log.Error("GetAuxPodLifecycle failed", "err", err, "pod", req.Pod, "vast_instance_id", req.VastInstanceID)
		h.serverError(w, "aux_pod_lifecycle_read_failed")
		return
	}
	if row.EndedAt.Valid {
		h.ok(w, auxPodLifecycleResponse{ID: row.ID, Event: "stop", AlreadyClosed: true})
		return
	}

	// ended_at nunca antes de started_at (retry com relógio torto não gera
	// custo negativo nem é recusado — o publisher não repetiria para sempre).
	if at.Before(row.StartedAt) {
		at = row.StartedAt
	}
	var totalUSD float64
	if req.TotalCostUSD != nil {
		totalUSD = *req.TotalCostUSD
	} else if dph := numericPtr(row.DphUsd); dph != nil {
		totalUSD = *dph * at.Sub(row.StartedAt).Hours()
	}
	usdN, err := toNumeric(totalUSD)
	if err != nil {
		h.badRequest(w, "invalid_total_cost_usd", "total_cost_usd must be a finite number.")
		return
	}
	brlN, err := toNumeric(totalUSD * h.cfg.USDToBRLRate)
	if err != nil {
		h.serverError(w, "aux_pod_lifecycle_cost_failed")
		return
	}
	n, err := h.q.CloseAuxPodLifecycle(ctx, gen.CloseAuxPodLifecycleParams{
		EndedAt:        pgtype.Timestamptz{Time: at, Valid: true},
		TotalCostUsd:   usdN,
		TotalCostBrl:   brlN,
		Pod:            req.Pod,
		VastInstanceID: inst,
	})
	if err != nil {
		h.log.Error("CloseAuxPodLifecycle failed", "err", err, "pod", req.Pod, "vast_instance_id", req.VastInstanceID)
		h.serverError(w, "aux_pod_lifecycle_write_failed")
		return
	}
	// n == 0: outro stop fechou entre o Get e o Close — mesmo desfecho de retry.
	h.log.Info("aux pod lifecycle stop", "id", row.ID, "pod", req.Pod, "vast_instance_id", req.VastInstanceID,
		"ended_at", at, "total_cost_usd", totalUSD, "already_closed", n == 0)
	h.ok(w, auxPodLifecycleResponse{ID: row.ID, Event: "stop", AlreadyClosed: n == 0})
}
