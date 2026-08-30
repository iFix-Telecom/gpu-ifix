// Package admin (model_aliases_admin_http.go): admin HTTP surface for the
// model_aliases table (quick 260830-o2j). Gives the dashboard the same power
// as `gatewayctl model-alias` — list / upsert / delete rows — PLUS the new
// per-model OpenRouter provider_prefs.
//
//	GET    /admin/model-aliases                       → every row
//	PUT    /admin/model-aliases {alias,upstream_name,target,provider_prefs?}
//	                                                  → 200 row (upsert)
//	DELETE /admin/model-aliases/{alias}/{upstream}    → 204
//
// Writes refresh the in-process resolver immediately (the 60s poll would
// otherwise leave the dashboard showing a change the gateway has not yet
// applied). Other replicas pick it up on their next poll — same contract as
// gatewayctl.
//
// Validation mirrors gatewayctl R10 (no whitespace/control/NUL, length caps)
// and adds: upstream_name must EXIST in ai_gateway.upstreams (role is taken
// from that row — no hardcoded name→role map here), provider_prefs only
// accepted for upstream_name=openrouter-chat and must pass
// models.ValidateProviderPrefs.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const modelAliasesRoute = "/admin/model-aliases"

// providerPrefsUpstream is the only upstream whose director consumes
// provider_prefs. Storing prefs on any other row would be silently inert, so
// the API refuses it.
const providerPrefsUpstream = "openrouter-chat"

const (
	modelAliasMaxAliasLen    = 64
	modelAliasMaxUpstreamLen = 64
	modelAliasMaxTargetLen   = 128
)

// modelAliasQueries isolates the sqlc surface consumed by the handler.
type modelAliasQueries interface {
	ListModelAliases(ctx context.Context) ([]gen.ListModelAliasesRow, error)
	GetModelAlias(ctx context.Context, arg gen.GetModelAliasParams) (gen.GetModelAliasRow, error)
	UpsertModelAlias(ctx context.Context, arg gen.UpsertModelAliasParams) error
	DeleteModelAlias(ctx context.Context, arg gen.DeleteModelAliasParams) error
	GetUpstreamByName(ctx context.Context, name string) (gen.GetUpstreamByNameRow, error)
}

// aliasRefresher is the resolver seam (models.Resolver.Refresh). nil-safe.
type aliasRefresher interface {
	Refresh(ctx context.Context) error
}

// ModelAliasRow is the typed API item (never the raw gen row).
type ModelAliasRow struct {
	Alias         string          `json:"alias"`
	UpstreamName  string          `json:"upstream_name"`
	Role          string          `json:"role"`
	Target        string          `json:"target"`
	ProviderPrefs json.RawMessage `json:"provider_prefs"` // null when unset
}

type upsertModelAliasRequest struct {
	Alias         string          `json:"alias"`
	UpstreamName  string          `json:"upstream_name"`
	Target        string          `json:"target"`
	ProviderPrefs json.RawMessage `json:"provider_prefs"` // absent/null clears
}

// ModelAliasAdminHandler serves the three model-alias admin routes.
type ModelAliasAdminHandler struct {
	q         modelAliasQueries
	refresher aliasRefresher
	log       *slog.Logger
}

// NewModelAliasAdminHandler wires the production dependencies.
func NewModelAliasAdminHandler(q *gen.Queries, refresher aliasRefresher, log *slog.Logger) *ModelAliasAdminHandler {
	return newModelAliasAdminHandlerWithQueries(q, refresher, log)
}

func newModelAliasAdminHandlerWithQueries(q modelAliasQueries, refresher aliasRefresher, log *slog.Logger) *ModelAliasAdminHandler {
	if log == nil {
		log = slog.Default()
	}
	return &ModelAliasAdminHandler{q: q, refresher: refresher, log: log.With("module", "ADMIN_MODEL_ALIASES")}
}

func toModelAliasRow(alias, upstreamName, role, target string, prefs []byte) ModelAliasRow {
	row := ModelAliasRow{Alias: alias, UpstreamName: upstreamName, Role: role, Target: target}
	if len(prefs) > 0 {
		row.ProviderPrefs = json.RawMessage(prefs)
	} else {
		row.ProviderPrefs = json.RawMessage("null")
	}
	return row
}

// List serves GET /admin/model-aliases.
func (h *ModelAliasAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q.ListModelAliases(r.Context())
	if err != nil {
		h.log.Error("ListModelAliases failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "model_alias_list_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "5xx").Inc()
		return
	}
	out := make([]ModelAliasRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, toModelAliasRow(row.Alias, row.UpstreamName, row.Upstream, row.Target, row.ProviderPrefs))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
	obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "2xx").Inc()
}

// Upsert serves PUT /admin/model-aliases.
func (h *ModelAliasAdminHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	var body upsertModelAliasRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&body); err != nil {
		h.bad(w, "invalid_body", "request body is not valid JSON")
		return
	}
	if err := validateAliasField("alias", body.Alias, modelAliasMaxAliasLen); err != nil {
		h.bad(w, "invalid_alias", err.Error())
		return
	}
	if err := validateAliasField("upstream_name", body.UpstreamName, modelAliasMaxUpstreamLen); err != nil {
		h.bad(w, "invalid_upstream", err.Error())
		return
	}
	if err := validateAliasField("target", body.Target, modelAliasMaxTargetLen); err != nil {
		h.bad(w, "invalid_target", err.Error())
		return
	}

	var prefs []byte
	if len(body.ProviderPrefs) > 0 && string(body.ProviderPrefs) != "null" {
		if body.UpstreamName != providerPrefsUpstream {
			h.bad(w, "provider_prefs_not_supported",
				fmt.Sprintf("provider_prefs só se aplica ao upstream %q", providerPrefsUpstream))
			return
		}
		canon, err := models.ValidateProviderPrefs(body.ProviderPrefs)
		if err != nil {
			h.bad(w, "invalid_provider_prefs", err.Error())
			return
		}
		prefs = canon
	}

	// FK-emulation (same as gatewayctl): the upstream row must exist; its
	// role is the value stored in model_aliases.upstream.
	up, err := h.q.GetUpstreamByName(r.Context(), body.UpstreamName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "upstream_not_found",
				fmt.Sprintf("upstream %q não existe", body.UpstreamName))
			obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "4xx").Inc()
			return
		}
		h.log.Error("GetUpstreamByName failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "model_alias_upsert_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "5xx").Inc()
		return
	}

	if err := h.q.UpsertModelAlias(r.Context(), gen.UpsertModelAliasParams{
		Alias:         body.Alias,
		Upstream:      up.Role,
		Target:        body.Target,
		UpstreamName:  body.UpstreamName,
		ProviderPrefs: prefs,
	}); err != nil {
		h.log.Error("UpsertModelAlias failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "model_alias_upsert_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "5xx").Inc()
		return
	}
	h.refresh(r.Context())
	h.log.Info("model alias upserted", "alias", body.Alias, "upstream", body.UpstreamName,
		"role", up.Role, "target", body.Target, "has_provider_prefs", prefs != nil)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(toModelAliasRow(body.Alias, body.UpstreamName, up.Role, body.Target, prefs))
	obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "2xx").Inc()
}

// Delete serves DELETE /admin/model-aliases/{alias}/{upstream}.
func (h *ModelAliasAdminHandler) Delete(w http.ResponseWriter, r *http.Request) {
	alias := chi.URLParam(r, "alias")
	upstreamName := chi.URLParam(r, "upstream")
	if err := validateAliasField("alias", alias, modelAliasMaxAliasLen); err != nil {
		h.bad(w, "invalid_alias", err.Error())
		return
	}
	if err := validateAliasField("upstream_name", upstreamName, modelAliasMaxUpstreamLen); err != nil {
		h.bad(w, "invalid_upstream", err.Error())
		return
	}
	if _, err := h.q.GetModelAlias(r.Context(), gen.GetModelAliasParams{Alias: alias, UpstreamName: upstreamName}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "model_alias_not_found", "")
			obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "4xx").Inc()
			return
		}
		h.log.Error("GetModelAlias failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "model_alias_delete_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "5xx").Inc()
		return
	}
	if err := h.q.DeleteModelAlias(r.Context(), gen.DeleteModelAliasParams{Alias: alias, UpstreamName: upstreamName}); err != nil {
		h.log.Error("DeleteModelAlias failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "model_alias_delete_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "5xx").Inc()
		return
	}
	h.refresh(r.Context())
	h.log.Info("model alias deleted", "alias", alias, "upstream", upstreamName)
	w.WriteHeader(http.StatusNoContent)
	obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "2xx").Inc()
}

// refresh applies the write to THIS replica's resolver immediately. Failure
// is logged, not surfaced — the 60s poll is the safety net.
func (h *ModelAliasAdminHandler) refresh(ctx context.Context) {
	if h.refresher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := h.refresher.Refresh(ctx); err != nil {
		h.log.Warn("resolver refresh after admin write failed", "err", err)
	}
}

func (h *ModelAliasAdminHandler) bad(w http.ResponseWriter, code, msg string) {
	httpx.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", code, msg)
	obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "4xx").Inc()
}

// validateAliasField mirrors gatewayctl's R10 rules: non-empty, length cap,
// no NUL / whitespace / control characters.
func validateAliasField(name, val string, maxLen int) error {
	if val == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if len(val) > maxLen {
		return fmt.Errorf("%s exceeds max length (%d): %d chars", name, maxLen, len(val))
	}
	for i, r := range val {
		if r == 0 || unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain whitespace or control chars (position %d)", name, i)
		}
	}
	return nil
}
