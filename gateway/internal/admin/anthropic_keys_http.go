// Package admin (anthropic_keys_http.go): visão read-only do pool de chaves
// da Anthropic (passo ①, Phase 23-12, HAIKU-02).
//
//	GET /admin/anthropic/keys → {"keys":[{label,fingerprint,state,until,next_probe_at,reason}],"configured":bool}
//
// Nunca expõe a chave: a fonte é KeyPool.Snapshot, que só carrega rótulo +
// fingerprint (T-23-37). A rota é registrada dentro do adminRouter
// (X-Admin-Key) no wiring do 23-14.
package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

const anthropicKeysAdminRoute = "/admin/anthropic/keys"

// AnthropicKeyRow é uma chave na resposta do admin. Datas em RFC3339.
type AnthropicKeyRow struct {
	Label       string  `json:"label"`
	Fingerprint string  `json:"fingerprint"`
	State       string  `json:"state"`
	Until       *string `json:"until,omitempty"`
	NextProbeAt *string `json:"next_probe_at,omitempty"`
	Reason      string  `json:"reason,omitempty"`
}

type anthropicKeysResponse struct {
	Keys       []AnthropicKeyRow `json:"keys"`
	Configured bool              `json:"configured"`
}

type anthropicKeysHandler struct {
	pool *proxy.KeyPool
	log  *slog.Logger
}

// NewAnthropicKeysHandler devolve o handler de GET /admin/anthropic/keys.
// pool nil (env UPSTREAM_LLM_ANTHROPIC_API_KEYS vazio) → {"keys":[],"configured":false}.
func NewAnthropicKeysHandler(pool *proxy.KeyPool, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return &anthropicKeysHandler{pool: pool, log: log}
}

func (h *anthropicKeysHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpx.WriteOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "")
		obs.GatewayAdminRequests.WithLabelValues(anthropicKeysAdminRoute, "4xx").Inc()
		return
	}
	out := anthropicKeysResponse{Keys: []AnthropicKeyRow{}}
	if h.pool != nil {
		out.Configured = true
		for _, s := range h.pool.Snapshot(r.Context()) {
			out.Keys = append(out.Keys, AnthropicKeyRow{
				Label:       s.Label,
				Fingerprint: s.Fingerprint,
				State:       s.State,
				Until:       rfc3339Ptr(s.Until),
				NextProbeAt: rfc3339Ptr(s.NextProbeAt),
				Reason:      s.Reason,
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(out); err != nil {
		h.log.Warn("anthropic keys encode failed", "err", err)
	}
	obs.GatewayAdminRequests.WithLabelValues(anthropicKeysAdminRoute, "2xx").Inc()
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
