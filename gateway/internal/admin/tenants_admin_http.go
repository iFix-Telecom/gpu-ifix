// Package admin (tenants_admin_http.go): the admin HTTP surface for tenant
// list + create (Plan 18-01). Two thin handlers over the existing sqlc
// ListTenants / CreateTenant queries — no new migration, no dynamic SQL.
//
// The dashboard owner server action (Plan 18-02) calls these with X-Admin-Key:
//   - GET  /admin/tenants          → every tenant, including ones with no traffic
//   - POST /admin/tenants {slug,name} → 201 with the new id; slug dup → 409
//
// Mirrors the config_read/config_write template: an isolated query interface
// (so tests inject a call-counting fake), a dual constructor (prod *gen.Queries
// + test fake), typed response structs (never the raw gen row), the OpenAI
// error envelope, and the obs.GatewayAdminRequests metric per outcome.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const tenantsRoute = "/admin/tenants"

// tenantAdminQueries isolates the sqlc surface consumed by the tenant handlers.
// *gen.Queries satisfies it; tests inject a fake that records call-counts so a
// rejected body reaches NO query.
type tenantAdminQueries interface {
	ListTenants(ctx context.Context) ([]gen.ListTenantsRow, error)
	CreateTenant(ctx context.Context, arg gen.CreateTenantParams) (gen.CreateTenantRow, error)
	// quick 260830-o2j — per-tenant OpenRouter provider routing.
	UpdateTenantProviderPrefs(ctx context.Context, arg gen.UpdateTenantProviderPrefsParams) (int64, error)
}

// tenantRefresher is the tenants.Loader seam so a provider_prefs write is
// visible on THIS replica immediately (other replicas: NOTIFY tenants_changed).
type tenantRefresher interface {
	Refresh(ctx context.Context) error
}

// tenantResponse is the typed list/create item — never the raw gen row.
type tenantResponse struct {
	ID            string          `json:"id"`
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	ProviderPrefs json.RawMessage `json:"provider_prefs"` // null when unset (quick 260830-o2j)
	CreatedAt     string          `json:"created_at,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
}

// setTenantProviderPrefsRequest is the PUT /admin/tenants/{slug}/provider-prefs
// body. `null` / absent clears the tenant preference.
type setTenantProviderPrefsRequest struct {
	ProviderPrefs json.RawMessage `json:"provider_prefs"`
}

func rawOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// createTenantRequest is the POST body.
type createTenantRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// TenantAdminHandler serves GET + POST /admin/tenants via its List and Create
// methods (one struct, two routes — mounted separately in main.go).
type TenantAdminHandler struct {
	q         tenantAdminQueries
	refresher tenantRefresher // nil-safe
	log       *slog.Logger
}

// NewTenantAdminHandler wires the production dependencies (the concrete
// *gen.Queries + the tenants loader used to refresh after a prefs write).
func NewTenantAdminHandler(q *gen.Queries, refresher tenantRefresher, log *slog.Logger) *TenantAdminHandler {
	if log == nil {
		log = slog.Default()
	}
	return &TenantAdminHandler{q: q, refresher: refresher, log: log.With("module", "ADMIN_TENANTS")}
}

// newTenantAdminHandlerWithQueries is the test constructor.
func newTenantAdminHandlerWithQueries(q tenantAdminQueries, log *slog.Logger) *TenantAdminHandler {
	if log == nil {
		log = slog.Default()
	}
	return &TenantAdminHandler{q: q, log: log.With("module", "ADMIN_TENANTS")}
}

// List serves GET /admin/tenants.
func (h *TenantAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q.ListTenants(r.Context())
	if err != nil {
		h.log.Error("ListTenants failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "tenant_list_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "5xx").Inc()
		return
	}
	out := make([]tenantResponse, 0, len(rows))
	for _, t := range rows {
		out = append(out, tenantResponse{
			ID:            t.ID.String(),
			Slug:          t.Slug,
			Name:          t.Name,
			ProviderPrefs: rawOrNull(t.ProviderPrefs),
			CreatedAt:     t.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:     t.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
	obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "2xx").Inc()
}

// Create serves POST /admin/tenants. slug/name are validated server-side before
// any query; a slug unique_violation (pg 23505) maps to 409 tenant_exists.
func (h *TenantAdminHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body createTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.badRequest(w, "invalid_body", "request body is not valid JSON")
		return
	}
	slug := strings.TrimSpace(body.Slug)
	name := strings.TrimSpace(body.Name)
	if slug == "" || name == "" {
		h.badRequest(w, "invalid_body", "slug and name are required")
		return
	}

	t, err := h.q.CreateTenant(r.Context(), gen.CreateTenantParams{Slug: slug, Name: name})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			httpx.WriteOpenAIError(w, http.StatusConflict, "invalid_request_error", "tenant_exists", "slug já existe")
			obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "4xx").Inc()
			return
		}
		h.log.Error("CreateTenant failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "tenant_create_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "5xx").Inc()
		return
	}
	h.log.Info("tenant created", "tenant_id", t.ID.String(), "slug", t.Slug)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(tenantResponse{ID: t.ID.String(), Slug: t.Slug, Name: t.Name, ProviderPrefs: json.RawMessage("null")})
	obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "2xx").Inc()
}

// SetProviderPrefs serves PUT /admin/tenants/{slug}/provider-prefs
// (quick 260830-o2j). Body {provider_prefs: <object>|null}. The object is
// validated by models.ValidateProviderPrefs BEFORE any write; null clears.
// 404 when the slug is unknown. Refreshes the tenants loader on success.
func (h *TenantAdminHandler) SetProviderPrefs(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	if slug == "" {
		h.badRequest(w, "invalid_slug", "slug is required")
		return
	}
	var body setTenantProviderPrefsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		h.badRequest(w, "invalid_body", "request body is not valid JSON")
		return
	}
	var prefs []byte
	if len(body.ProviderPrefs) > 0 && string(body.ProviderPrefs) != "null" {
		canon, err := models.ValidateProviderPrefs(body.ProviderPrefs)
		if err != nil {
			h.badRequest(w, "invalid_provider_prefs", err.Error())
			return
		}
		prefs = canon
	}
	n, err := h.q.UpdateTenantProviderPrefs(r.Context(), gen.UpdateTenantProviderPrefsParams{Slug: slug, ProviderPrefs: prefs})
	if err != nil {
		h.log.Error("UpdateTenantProviderPrefs failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "tenant_prefs_update_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "5xx").Inc()
		return
	}
	if n == 0 {
		httpx.WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "tenant_not_found", "tenant não encontrado")
		obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "4xx").Inc()
		return
	}
	if h.refresher != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if rerr := h.refresher.Refresh(ctx); rerr != nil {
			h.log.Warn("tenants refresh after provider_prefs write failed", "err", rerr)
		}
		cancel()
	}
	h.log.Info("tenant provider_prefs set", "slug", slug, "has_provider_prefs", prefs != nil)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"slug": slug, "provider_prefs": rawOrNull(prefs)})
	obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "2xx").Inc()
}

func (h *TenantAdminHandler) badRequest(w http.ResponseWriter, code, msg string) {
	httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", code, msg)
	obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "4xx").Inc()
}
