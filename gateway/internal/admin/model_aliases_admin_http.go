// Package admin (model_aliases_admin_http.go): admin HTTP surface for the
// model_aliases table (quick 260830-o2j). Gives the dashboard the same power
// as `gatewayctl model-alias` — list / upsert / delete rows — PLUS the new
// per-model OpenRouter provider_prefs.
//
//	GET    /admin/model-aliases                       → every row
//	PUT    /admin/model-aliases {alias,upstream_name,target,provider_prefs?,
//	                         priority?,fallback_models?}   (quick 261018:
//	                         priority/fallback_models absent = keep current)
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
// accepted for upstream_name in providerPrefsUpstreams (openrouter-chat,
// openrouter-deepseek) and must pass
// models.ValidateProviderPrefs.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const modelAliasesRoute = "/admin/model-aliases"

// providerPrefsUpstreams is the closed set of upstreams whose director
// consumes provider_prefs (openrouter_director reads them by
// (alias, upstreamName); Phase 23-20 added openrouter-deepseek, the passo ③
// row). Storing prefs on any other row would be silently inert, so the API
// refuses it. Copy of gatewayctl's providerPrefsUpstreams (the packages do
// not import each other) and of the dashboard PROVIDER_PREFS_UPSTREAMS —
// all three are pinned to testdata/provider_prefs_upstreams.json.
var providerPrefsUpstreams = map[string]bool{
	"openrouter-chat":     true,
	"openrouter-deepseek": true,
}

// providerPrefsUpstreamsList renders the set sorted, for error messages.
func providerPrefsUpstreamsList() string {
	names := make([]string, 0, len(providerPrefsUpstreams))
	for n := range providerPrefsUpstreams {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

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
	// quick 261018 — ordem dentro do alias e fallback nativo do OpenRouter.
	Priority       *int32   `json:"priority"`        // null when unset
	FallbackModels []string `json:"fallback_models"` // null when unset
}

// upsertModelAliasRequest: provider_prefs keeps its historical contract
// (absent/null clears). The quick-261018 fields use json.RawMessage to tell
// ABSENT (keep the row's current value — a dashboard that does not know the
// field yet never wipes it) from explicit null (clear) and a value (set).
type upsertModelAliasRequest struct {
	Alias          string          `json:"alias"`
	UpstreamName   string          `json:"upstream_name"`
	Target         string          `json:"target"`
	ProviderPrefs  json.RawMessage `json:"provider_prefs"`  // absent/null clears
	Priority       json.RawMessage `json:"priority"`        // absent keeps, null clears, int sets
	FallbackModels json.RawMessage `json:"fallback_models"` // absent keeps, null/[] clears, []string sets
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

func toModelAliasRow(alias, upstreamName, role, target string, prefs []byte, priority pgtype.Int4, fallback []string) ModelAliasRow {
	row := ModelAliasRow{Alias: alias, UpstreamName: upstreamName, Role: role, Target: target}
	if len(prefs) > 0 {
		row.ProviderPrefs = json.RawMessage(prefs)
	} else {
		row.ProviderPrefs = json.RawMessage("null")
	}
	if priority.Valid {
		p := priority.Int32
		row.Priority = &p
	}
	if len(fallback) > 0 {
		row.FallbackModels = fallback
	}
	return row
}

// isJSONNull reports an explicit JSON null (absent fields decode to len 0).
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
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
		out = append(out, toModelAliasRow(row.Alias, row.UpstreamName, row.Upstream, row.Target, row.ProviderPrefs, row.Priority, row.FallbackModels))
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
		if !providerPrefsUpstreams[body.UpstreamName] {
			h.bad(w, "provider_prefs_not_supported",
				fmt.Sprintf("provider_prefs só se aplica aos upstreams %s", providerPrefsUpstreamsList()))
			return
		}
		canon, err := models.ValidateProviderPrefs(body.ProviderPrefs)
		if err != nil {
			h.bad(w, "invalid_provider_prefs", err.Error())
			return
		}
		prefs = canon
	}

	// quick 261018 — priority / fallback_models (absent = keep current).
	var priority pgtype.Int4
	prioritySet := len(body.Priority) > 0
	if prioritySet && !isJSONNull(body.Priority) {
		var n int32
		if err := json.Unmarshal(body.Priority, &n); err != nil {
			h.bad(w, "invalid_priority", "priority deve ser inteiro (int32) ou null")
			return
		}
		priority = pgtype.Int4{Int32: n, Valid: true}
	}
	var fallback []string
	fallbackSet := len(body.FallbackModels) > 0
	if fallbackSet && !isJSONNull(body.FallbackModels) {
		var list []string
		if err := json.Unmarshal(body.FallbackModels, &list); err != nil {
			h.bad(w, "invalid_fallback_models", "fallback_models deve ser lista de strings ou null")
			return
		}
		if len(list) > 0 && !models.FallbackModelsUpstreams[body.UpstreamName] {
			h.bad(w, "fallback_models_not_supported",
				fmt.Sprintf("fallback_models só se aplica aos upstreams %s", providerPrefsUpstreamsList()))
			return
		}
		canon, err := models.ValidateFallbackModels(body.Target, list)
		if err != nil {
			h.bad(w, "invalid_fallback_models", err.Error())
			return
		}
		fallback = canon
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

	if !prioritySet || !fallbackSet {
		cur, err := h.q.GetModelAlias(r.Context(), gen.GetModelAliasParams{Alias: body.Alias, UpstreamName: body.UpstreamName})
		switch {
		case err == nil:
			if !prioritySet {
				priority = cur.Priority
			}
			if !fallbackSet {
				fallback = cur.FallbackModels
			}
		case !errors.Is(err, pgx.ErrNoRows):
			h.log.Error("GetModelAlias failed", "err", err)
			httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "model_alias_upsert_failed", "")
			obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "5xx").Inc()
			return
		}
		// A kept list may now repeat a NEW target — refuse instead of storing
		// a row the write path itself would reject.
		if !fallbackSet && len(fallback) > 0 {
			if _, err := models.ValidateFallbackModels(body.Target, fallback); err != nil {
				h.bad(w, "invalid_fallback_models", "fallback_models atual inválido para o novo target: "+err.Error())
				return
			}
		}
	}

	if err := h.q.UpsertModelAlias(r.Context(), gen.UpsertModelAliasParams{
		Alias:          body.Alias,
		Upstream:       up.Role,
		Target:         body.Target,
		UpstreamName:   body.UpstreamName,
		ProviderPrefs:  prefs,
		Priority:       priority,
		FallbackModels: fallback,
	}); err != nil {
		h.log.Error("UpsertModelAlias failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "model_alias_upsert_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(modelAliasesRoute, "5xx").Inc()
		return
	}
	h.refresh(r.Context())
	h.log.Info("model alias upserted", "alias", body.Alias, "upstream", body.UpstreamName,
		"role", up.Role, "target", body.Target, "has_provider_prefs", prefs != nil,
		"priority_set", priority.Valid, "fallback_models", len(fallback))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(toModelAliasRow(body.Alias, body.UpstreamName, up.Role, body.Target, prefs, priority, fallback))
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
