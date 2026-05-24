// Package models implements model alias resolution. Clients send
// `model: "qwen"` (friendly name); the gateway rewrites the request to
// `model: "<target>"` (pod-specific identifier) before proxying. Backed
// by the ai_gateway.model_aliases table populated in Plan 02-02.
package models

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// refreshInterval is how often Start re-reads model_aliases.
const refreshInterval = 60 * time.Second

// aliasKey composes the table's (alias, upstream_name) primary-key pair so
// the same alias can legitimately exist on multiple upstreams without
// collision.
//
// Phase 06.9: the Upstream field is now the upstream NAME (e.g.
// "local-llm", "openrouter-chat") sourced from model_aliases.upstream_name
// column. NOT the role tag. The struct field name stays "Upstream" to keep
// the diff minimal — only the semantic meaning changed. Plan 06.9-01
// widened the table PK to (alias, upstream_name); this struct is the
// in-memory cache key.
type aliasKey struct {
	Alias    string
	Upstream string // Phase 06.9: upstream NAME (e.g. "local-llm", "openrouter-chat"), NOT role
}

// resolverQueries isolates the sqlc surface we use so tests can stub it.
//
// Phase 06.9 (Plan 06.9-01): the regenerated sqlc no longer returns the table
// struct directly because the queries omit `created_at` and add `upstream_name`
// — sqlc generates a query-specific `ListModelAliasesRow`. Phase 06.9 (Plan
// 06.9-02) updates the Refresh body to populate the cache by `row.UpstreamName`.
type resolverQueries interface {
	ListModelAliases(ctx context.Context) ([]gen.ListModelAliasesRow, error)
}

// upstreamEnvVarMap is the curated D-06 env-override-wins mapping (Phase
// 06.9). Operators may override the schema-row target on a per-instance
// basis by setting the corresponding env var; resolver Resolve consults
// env first, schema second. Local tier-0 upstreams are deliberately not
// mapped — no escape hatch needed (the tier-0 backfill is a no-op rewrite).
//
// New tier-1 providers MUST add an entry here AND a schema row when they
// land — otherwise the env-var operator escape hatch silently does not
// apply to the new provider.
var upstreamEnvVarMap = map[string]string{
	"openrouter-chat": "UPSTREAM_LLM_OPENROUTER_MODEL",
	"openai-whisper":  "UPSTREAM_STT_OPENAI_MODEL",
	"openai-embed":    "UPSTREAM_EMBED_OPENAI_MODEL",
}

// Resolver holds the current (alias, upstream_name) → target map in memory.
type Resolver struct {
	q       resolverQueries
	log     *slog.Logger
	mu      sync.RWMutex
	aliases map[aliasKey]string
}

// NewResolver wires the Postgres pool.
func NewResolver(pool *pgxpool.Pool, log *slog.Logger) *Resolver {
	return &Resolver{
		q:       gen.New(pool),
		log:     log.With("module", "MODELS"),
		aliases: map[aliasKey]string{},
	}
}

// Refresh reads the model_aliases table into a fresh map and atomically
// swaps it in under write-lock.
//
// Phase 06.9: cache keys are (alias, upstream_name) — the upstream NAME
// from the new model_aliases.upstream_name column. The old `row.Upstream`
// (role) column is retained on the table for compatibility but is no
// longer read here.
func (r *Resolver) Refresh(ctx context.Context) error {
	rows, err := r.q.ListModelAliases(ctx)
	if err != nil {
		return err
	}
	fresh := make(map[aliasKey]string, len(rows))
	for _, row := range rows {
		fresh[aliasKey{Alias: row.Alias, Upstream: row.UpstreamName}] = row.Target
	}
	r.mu.Lock()
	r.aliases = fresh
	r.mu.Unlock()
	r.log.Info("model aliases refreshed", "count", len(fresh))

	// Phase 06.9 D-06 observability: surface which env overrides are
	// currently active so operators can confirm their per-instance
	// escape hatch is being honored. Presence-only — never log the
	// override VALUE (avoid leaking operator-overridden slugs).
	for upstream, envVar := range upstreamEnvVarMap {
		if os.Getenv(envVar) != "" {
			r.log.Info("env override active for upstream", "upstream", upstream, "env_var", envVar)
		}
	}
	return nil
}

// Start spawns a 60s-ticker goroutine that calls Refresh. Exits on ctx
// cancel — a simple ticker is fine for Phase 2; Phase 5 LISTEN/NOTIFY is
// overkill here.
func (r *Resolver) Start(ctx context.Context) {
	go func() {
		tick := time.NewTicker(refreshInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := r.Refresh(ctx); err != nil {
					r.log.Warn("resolver refresh failed", "err", err)
				}
			}
		}
	}()
}

// Resolve returns the target for alias on the resolved upstream NAME.
//
// Precedence per D-06 (Phase 06.9):
//  1. Env override via curated upstreamEnvVarMap — env wins when non-empty.
//  2. Schema row in model_aliases — keyed on (alias, upstream_name).
//  3. Passthrough — alias returned unchanged (safety net for new upstreams
//     not yet seeded; pod decides what to do with an unknown model name).
//
// Empty-string env values are treated as unset (do NOT override schema).
//
// Env-override is the documented operator escape hatch — see CONTEXT.md
// D-06 for rationale. The CLI `gatewayctl model-alias set` (Plan 04) is
// the multi-instance-consistent override path; env is the per-instance
// path. Both are supported and coequal.
func (r *Resolver) Resolve(alias, upstream string) string {
	// (1) Env-override layer — env wins when non-empty.
	if envVar, ok := upstreamEnvVarMap[upstream]; ok {
		if val := os.Getenv(envVar); val != "" {
			return val
		}
	}
	// (2) Schema layer — under RLock for concurrent-safe map read.
	r.mu.RLock()
	target, hit := r.aliases[aliasKey{Alias: alias, Upstream: upstream}]
	r.mu.RUnlock()
	if hit {
		return target
	}
	// (3) Passthrough layer.
	return alias
}
