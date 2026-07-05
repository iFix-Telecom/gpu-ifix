// Package admin (keys_admin_http.go): the admin HTTP surface for per-tenant API
// key list + create + revoke (Plan 18-01). Thin handlers over the existing
// GetTenantBySlug / ListActiveKeysByTenantWithMeta / InsertAPIKey / RevokeAPIKey
// sqlc queries plus auth.GenerateAPIKey — no new migration, no dynamic SQL.
//
//   - GET  /admin/tenants/{slug}/keys → active keys, operator-safe projection
//     (never key_hash, never key_lookup_hash)
//   - POST /admin/tenants/{slug}/keys {data_class?} → 201 with the raw key
//     serialized EXACTLY ONCE (field "key"); the hash columns are never in the
//     response struct and the raw is never logged (threat T-18-04)
//   - POST /admin/keys/{id}/revoke → 200; idempotent (a second call is a no-op
//     because the UPDATE is scoped WHERE status='active')
//
// Mirrors the config_read/config_write template: isolated query interface, dual
// constructor, typed response structs, OpenAI error envelope, admin metric.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const (
	keysRoute      = "/admin/tenants/{slug}/keys"
	keyRevokeRoute = "/admin/keys/{id}/revoke"
)

// keysAdminQueries isolates the sqlc surface consumed by the key handlers.
type keysAdminQueries interface {
	GetTenantBySlug(ctx context.Context, slug string) (gen.GetTenantBySlugRow, error)
	ListActiveKeysByTenantWithMeta(ctx context.Context, tenantID uuid.UUID) ([]gen.ListActiveKeysByTenantWithMetaRow, error)
	InsertAPIKey(ctx context.Context, arg gen.InsertAPIKeyParams) (gen.InsertAPIKeyRow, error)
	RevokeAPIKey(ctx context.Context, id uuid.UUID) error
}

// keyListItem is the operator-safe projection — it deliberately has NO hash
// field, so a serialized list cannot leak secret material.
type keyListItem struct {
	ID         string  `json:"id"`
	TenantSlug string  `json:"tenant_slug"`
	KeyPrefix  string  `json:"key_prefix"`
	Status     string  `json:"status"`
	DataClass  string  `json:"data_class"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

// createKeyResponse is the ONLY place the raw key is serialized. It has no hash
// field by construction.
type createKeyResponse struct {
	ID        string `json:"id"`
	KeyPrefix string `json:"key_prefix"`
	Tenant    string `json:"tenant"`
	DataClass string `json:"data_class"`
	Key       string `json:"key"` // raw — appears here, once
}

type createKeyRequest struct {
	DataClass string `json:"data_class"`
}

// KeysAdminHandler serves the three key routes via its List/Create/Revoke
// methods.
type KeysAdminHandler struct {
	q   keysAdminQueries
	log *slog.Logger
}

// NewKeysAdminHandler wires the production dependency (the concrete
// *gen.Queries).
func NewKeysAdminHandler(q *gen.Queries, log *slog.Logger) *KeysAdminHandler {
	if log == nil {
		log = slog.Default()
	}
	return &KeysAdminHandler{q: q, log: log.With("module", "ADMIN_KEYS")}
}

// newKeysAdminHandlerWithQueries is the test constructor.
func newKeysAdminHandlerWithQueries(q keysAdminQueries, log *slog.Logger) *KeysAdminHandler {
	if log == nil {
		log = slog.Default()
	}
	return &KeysAdminHandler{q: q, log: log.With("module", "ADMIN_KEYS")}
}

// List serves GET /admin/tenants/{slug}/keys.
func (h *KeysAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	tenant, err := h.q.GetTenantBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "tenant_not_found", "tenant não existe")
			obs.GatewayAdminRequests.WithLabelValues(keysRoute, "4xx").Inc()
			return
		}
		h.log.Error("GetTenantBySlug failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "tenant_lookup_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(keysRoute, "5xx").Inc()
		return
	}
	rows, err := h.q.ListActiveKeysByTenantWithMeta(r.Context(), tenant.ID)
	if err != nil {
		h.log.Error("ListActiveKeysByTenantWithMeta failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "key_list_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(keysRoute, "5xx").Inc()
		return
	}
	out := make([]keyListItem, 0, len(rows))
	for _, k := range rows {
		var lu *string
		if k.LastUsedAt.Valid {
			s := k.LastUsedAt.Time.UTC().Format(time.RFC3339)
			lu = &s
		}
		out = append(out, keyListItem{
			ID:         k.ID.String(),
			TenantSlug: k.TenantSlug,
			KeyPrefix:  k.KeyPrefix,
			Status:     dataClassString(k.Status),
			DataClass:  dataClassString(k.DataClass),
			CreatedAt:  k.CreatedAt.UTC().Format(time.RFC3339),
			LastUsedAt: lu,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
	obs.GatewayAdminRequests.WithLabelValues(keysRoute, "2xx").Inc()
}

// Create serves POST /admin/tenants/{slug}/keys. data_class is validated
// against the {normal,sensitive} whitelist BEFORE any query; the generated raw
// key is returned once and never logged.
func (h *KeysAdminHandler) Create(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	var body createKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		h.badRequest(w, keysRoute, "invalid_body", "request body is not valid JSON")
		return
	}
	dataClass := strings.TrimSpace(body.DataClass)
	if dataClass == "" {
		dataClass = "normal"
	}
	if dataClass != "normal" && dataClass != "sensitive" {
		h.badRequest(w, keysRoute, "invalid_data_class", `data_class must be "normal" or "sensitive"`)
		return
	}

	tenant, err := h.q.GetTenantBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "tenant_not_found", "tenant não existe")
			obs.GatewayAdminRequests.WithLabelValues(keysRoute, "4xx").Inc()
			return
		}
		h.log.Error("GetTenantBySlug failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "tenant_lookup_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(keysRoute, "5xx").Inc()
		return
	}

	raw, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		h.log.Error("GenerateAPIKey failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "key_generate_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(keysRoute, "5xx").Inc()
		return
	}
	inserted, err := h.q.InsertAPIKey(r.Context(), gen.InsertAPIKeyParams{
		TenantID:      tenant.ID,
		KeyHash:       hash,
		KeyLookupHash: lookupHash,
		KeyPrefix:     prefix,
		DataClass:     dataClass, // pgx encodes string → ENUM
	})
	if err != nil {
		h.log.Error("InsertAPIKey failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "key_insert_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(keysRoute, "5xx").Inc()
		return
	}
	// NEVER log the raw key — only the non-secret identifiers (mirrors the
	// gatewayctl create path).
	h.log.Info("api key issued",
		"api_key_id", inserted.ID.String(),
		"tenant_id", tenant.ID.String(),
		"tenant_slug", slug,
		"data_class", dataClass,
		"key_prefix", prefix,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createKeyResponse{
		ID:        inserted.ID.String(),
		KeyPrefix: prefix,
		Tenant:    slug,
		DataClass: dataClass,
		Key:       raw,
	})
	obs.GatewayAdminRequests.WithLabelValues(keysRoute, "2xx").Inc()
}

// Revoke serves POST /admin/keys/{id}/revoke. Idempotent by construction: the
// UPDATE is scoped WHERE status='active', so a second call affects zero rows
// and still returns 200.
func (h *KeysAdminHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_id", "id não é um UUID válido")
		obs.GatewayAdminRequests.WithLabelValues(keyRevokeRoute, "4xx").Inc()
		return
	}
	if err := h.q.RevokeAPIKey(r.Context(), id); err != nil {
		h.log.Error("RevokeAPIKey failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "key_revoke_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(keyRevokeRoute, "5xx").Inc()
		return
	}
	h.log.Info("api key revoked", "api_key_id", id.String())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "revoked", "id": id.String()})
	obs.GatewayAdminRequests.WithLabelValues(keyRevokeRoute, "2xx").Inc()
}

func (h *KeysAdminHandler) badRequest(w http.ResponseWriter, route, code, msg string) {
	httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", code, msg)
	obs.GatewayAdminRequests.WithLabelValues(route, "4xx").Inc()
}
