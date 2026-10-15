//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// TestTenantLLMEffort_RoundTrip — Phase 23 (HAIKU-05). Caminho real:
// banco (migration 0041) → sqlc UpdateTenantLLMEffort → Loader.Refresh
// (ListTenantsForLoader) → LLMEffortByTenantID, incluindo NULL e a CHECK.
func TestTenantLLMEffort_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)
	q := gen.New(pool)
	id := seed.ConverseAITenantID
	const slug = "converseai"

	loader, err := tenants.NewLoader(ctx, pool, time.UTC, discardLogger())
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}
	if got := loader.LLMEffortByTenantID(id.String()); got != "" {
		t.Fatalf("estado inicial: got %q, want \"\" (sem override)", got)
	}

	// 'low' via sqlc real.
	n, err := q.UpdateTenantLLMEffort(ctx, gen.UpdateTenantLLMEffortParams{
		Slug:      slug,
		LlmEffort: pgtype.Text{String: "low", Valid: true},
	})
	if err != nil {
		t.Fatalf("UpdateTenantLLMEffort low: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows afetadas = %d, want 1", n)
	}
	if err := loader.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := loader.LLMEffortByTenantID(id.String()); got != "low" {
		t.Fatalf("após low: got %q", got)
	}
	cfg, err := q.GetTenantConfig(ctx, id)
	if err != nil {
		t.Fatalf("GetTenantConfig: %v", err)
	}
	if !cfg.LlmEffort.Valid || cfg.LlmEffort.String != "low" {
		t.Fatalf("GetTenantConfig.LlmEffort = %+v, want low", cfg.LlmEffort)
	}

	// NULL limpa o override.
	if _, err := q.UpdateTenantLLMEffort(ctx, gen.UpdateTenantLLMEffortParams{
		Slug:      slug,
		LlmEffort: pgtype.Text{Valid: false},
	}); err != nil {
		t.Fatalf("UpdateTenantLLMEffort NULL: %v", err)
	}
	if err := loader.Refresh(ctx); err != nil {
		t.Fatalf("Refresh 2: %v", err)
	}
	if got := loader.LLMEffortByTenantID(id.String()); got != "" {
		t.Fatalf("após NULL: got %q, want \"\"", got)
	}

	// Valor fora da CHECK é rejeitado pelo banco.
	if _, err := q.UpdateTenantLLMEffort(ctx, gen.UpdateTenantLLMEffortParams{
		Slug:      slug,
		LlmEffort: pgtype.Text{String: "bogus", Valid: true},
	}); err == nil {
		t.Fatalf("UpdateTenantLLMEffort bogus: want erro da CHECK, got nil")
	}

	// Slug inexistente: 0 linhas, sem erro.
	n, err = q.UpdateTenantLLMEffort(ctx, gen.UpdateTenantLLMEffortParams{
		Slug:      "nao-existe",
		LlmEffort: pgtype.Text{String: "high", Valid: true},
	})
	if err != nil || n != 0 {
		t.Fatalf("slug inexistente: n=%d err=%v, want 0/nil", n, err)
	}
}
