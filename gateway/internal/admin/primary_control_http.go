// Package admin (primary_control_http.go): operator control of the primary
// pod from the dashboard (quick 260830-o2j).
//
//	POST /admin/primary/force-up   {reason?} → 202
//	POST /admin/primary/force-down {reason?} → 202
//
// These publish EXACTLY the events `gatewayctl primary force-up|force-down`
// publish (redisx.PrimaryEvent{Type:"force_up_request"|"force_down_request"}
// on gw:primary:events) — the reconciler LEADER consumes them on its next
// tick, so the response is 202 Accepted (the request is queued, not
// applied). The reason is prefixed "dashboard:" so lifecycle trigger_reason
// audit rows distinguish the dashboard from the CLI.
package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

const primaryControlRoute = "/admin/primary/control"

const primaryControlMaxReasonLen = 200

type primaryControlRequest struct {
	Reason string `json:"reason"`
}

// PrimaryControlHandler publishes force-up / force-down requests.
type PrimaryControlHandler struct {
	rdb       *redis.Client // nil → 503 (Redis disabled)
	replicaID string
	log       *slog.Logger
}

// NewPrimaryControlHandler wires the production dependencies.
func NewPrimaryControlHandler(rdb *redis.Client, replicaID string, log *slog.Logger) *PrimaryControlHandler {
	if log == nil {
		log = slog.Default()
	}
	return &PrimaryControlHandler{rdb: rdb, replicaID: replicaID, log: log.With("module", "ADMIN_PRIMARY_CONTROL")}
}

// ForceUp serves POST /admin/primary/force-up.
func (h *PrimaryControlHandler) ForceUp(w http.ResponseWriter, r *http.Request) {
	h.publish(w, r, "force_up_request")
}

// ForceDown serves POST /admin/primary/force-down.
func (h *PrimaryControlHandler) ForceDown(w http.ResponseWriter, r *http.Request) {
	h.publish(w, r, "force_down_request")
}

func (h *PrimaryControlHandler) publish(w http.ResponseWriter, r *http.Request, eventType string) {
	if h.rdb == nil {
		httpx.WriteOpenAIError(w, http.StatusServiceUnavailable, "api_error", "redis_unavailable",
			"controle do pod indisponível: Redis desabilitado neste gateway")
		obs.GatewayAdminRequests.WithLabelValues(primaryControlRoute, "5xx").Inc()
		return
	}
	var body primaryControlRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body", "request body is not valid JSON")
			obs.GatewayAdminRequests.WithLabelValues(primaryControlRoute, "4xx").Inc()
			return
		}
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "manual"
	}
	if len(reason) > primaryControlMaxReasonLen {
		reason = reason[:primaryControlMaxReasonLen]
	}
	reason = "dashboard:" + strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == 0 {
			return ' '
		}
		return r
	}, reason)

	ev := redisx.PrimaryEvent{
		Type:      eventType,
		Reason:    reason,
		SinceUnix: time.Now().Unix(),
		ReplicaID: h.replicaID,
	}
	if err := redisx.PublishPrimaryEvent(r.Context(), h.rdb, ev); err != nil {
		h.log.Error("publish primary control event failed", "type", eventType, "err", err)
		httpx.WriteOpenAIError(w, http.StatusBadGateway, "api_error", "publish_failed",
			"não foi possível publicar o pedido no Redis")
		obs.GatewayAdminRequests.WithLabelValues(primaryControlRoute, "5xx").Inc()
		return
	}
	h.log.Info("primary control event published", "type", eventType, "reason", reason)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"queued":  true,
		"type":    eventType,
		"reason":  reason,
		"channel": redisx.PrimaryEventsChannel,
	})
	obs.GatewayAdminRequests.WithLabelValues(primaryControlRoute, "2xx").Inc()
}
