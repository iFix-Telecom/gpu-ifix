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

	"github.com/jackc/pgx/v5/pgconn"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const tenantsRoute = "/admin/tenants"

// tenantAdminQueries isolates the sqlc surface consumed by the tenant handlers.
// *gen.Queries satisfies it; tests inject a fake that records call-counts so a
// rejected body reaches NO query.
type tenantAdminQueries interface {
	ListTenants(ctx context.Context) ([]gen.ListTenantsRow, error)
	CreateTenant(ctx context.Context, arg gen.CreateTenantParams) (gen.CreateTenantRow, error)
}

// tenantResponse is the typed list/create item — never the raw gen row.
type tenantResponse struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// createTenantRequest is the POST body.
type createTenantRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// TenantAdminHandler serves GET + POST /admin/tenants via its List and Create
// methods (one struct, two routes — mounted separately in main.go).
type TenantAdminHandler struct {
	q   tenantAdminQueries
	log *slog.Logger
}

// NewTenantAdminHandler wires the production dependency (the concrete
// *gen.Queries).
func NewTenantAdminHandler(q *gen.Queries, log *slog.Logger) *TenantAdminHandler {
	if log == nil {
		log = slog.Default()
	}
	return &TenantAdminHandler{q: q, log: log.With("module", "ADMIN_TENANTS")}
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
			ID:        t.ID.String(),
			Slug:      t.Slug,
			Name:      t.Name,
			CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
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
	_ = json.NewEncoder(w).Encode(tenantResponse{ID: t.ID.String(), Slug: t.Slug, Name: t.Name})
	obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "2xx").Inc()
}

func (h *TenantAdminHandler) badRequest(w http.ResponseWriter, code, msg string) {
	httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", code, msg)
	obs.GatewayAdminRequests.WithLabelValues(tenantsRoute, "4xx").Inc()
}
