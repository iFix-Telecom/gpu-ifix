// Package models implements model alias resolution. Clients send
// `model: "qwen"` (friendly name); the gateway rewrites the request to
// `model: "<target>"` (pod-specific identifier) before proxying. Backed
// by the ai_gateway.model_aliases table populated in Plan 02-02.
package models

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// refreshInterval is how often Start re-reads model_aliases.
const refreshInterval = 60 * time.Second

// aliasKey composes the table's (alias, upstream) primary-key-ish pair so
// the same alias can legitimately exist on multiple upstreams without
// collision (Codex review [MEDIUM] 02-05). Even though Phase 2 seeds no
// duplicate aliases across upstreams, the type makes the model correct
// on day one — no refactor when an operator later adds `qwen` to embed.
type aliasKey struct {
	Alias    string
	Upstream string // "llm" | "stt" | "embed"
}

// resolverQueries isolates the sqlc surface we use so tests can stub it.
//
// Phase 06.9 (Plan 06.9-01): the regenerated sqlc no longer returns the table
// struct directly because the queries omit `created_at` and add `upstream_name`
// — sqlc generates a query-specific `ListModelAliasesRow`. The resolver
// signature is updated to the new row type so the package compiles. The
// Refresh body below still maps by `row.Upstream` (role column) — Plan 06.9-02
// owns the actual per-upstream-name resolution rewire; this plan's only job
// here is to keep the build green after the sqlc regen.
type resolverQueries interface {
	ListModelAliases(ctx context.Context) ([]gen.ListModelAliasesRow, error)
}

// Resolver holds the current (alias, upstream) → target map in memory.
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
func (r *Resolver) Refresh(ctx context.Context) error {
	rows, err := r.q.ListModelAliases(ctx)
	if err != nil {
		return err
	}
	fresh := make(map[aliasKey]string, len(rows))
	for _, row := range rows {
		fresh[aliasKey{Alias: row.Alias, Upstream: row.Upstream}] = row.Target
	}
	r.mu.Lock()
	r.aliases = fresh
	r.mu.Unlock()
	r.log.Info("model aliases refreshed", "count", len(fresh))
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

// Resolve returns the target for (alias, upstream); if unknown, returns
// the alias itself (pod decides — keeps forward-compat when admin hasn't
// seeded a new model yet).
func (r *Resolver) Resolve(alias, upstream string) string {
	r.mu.RLock()
	t, ok := r.aliases[aliasKey{Alias: alias, Upstream: upstream}]
	r.mu.RUnlock()
	if !ok {
		return alias
	}
	return t
}
