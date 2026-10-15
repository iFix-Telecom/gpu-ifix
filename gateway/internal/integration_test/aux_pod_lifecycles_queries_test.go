//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// Phase 23 (23-11) — semântica SQL real das queries de aux_pod_lifecycles:
// ON CONFLICT com LEAST(started_at)/COALESCE(dph_usd), close guardado por
// ended_at IS NULL (execrows 0 no retry) e filtro por started_at em [from,to).
func TestAuxPodLifecycleQueries_Idempotency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	q := gen.New(pool)
	pod := fmt.Sprintf("it-aux-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ai_gateway.aux_pod_lifecycles WHERE pod = $1`, pod)
	})
	inst := pgtype.Int8{Int64: 77, Valid: true}
	t10 := time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
	dph := pgtype.Numeric{}
	if err := dph.Scan("0.12"); err != nil {
		t.Fatal(err)
	}

	id1, err := q.StartAuxPodLifecycle(ctx, gen.StartAuxPodLifecycleParams{Pod: pod, VastInstanceID: inst, StartedAt: t10, DphUsd: dph})
	if err != nil {
		t.Fatalf("start 1: %v", err)
	}
	// Retry com at mais antigo e sem dph: mesmo id, started_at recua, dph preservado.
	id2, err := q.StartAuxPodLifecycle(ctx, gen.StartAuxPodLifecycleParams{Pod: pod, VastInstanceID: inst, StartedAt: t10.Add(-time.Hour)})
	if err != nil || id2 != id1 {
		t.Fatalf("start 2: id=%d err=%v want id=%d", id2, err, id1)
	}
	// Retry com at mais novo: started_at mantido.
	if _, err := q.StartAuxPodLifecycle(ctx, gen.StartAuxPodLifecycleParams{Pod: pod, VastInstanceID: inst, StartedAt: t10.Add(time.Hour)}); err != nil {
		t.Fatalf("start 3: %v", err)
	}
	row, err := q.GetAuxPodLifecycle(ctx, gen.GetAuxPodLifecycleParams{Pod: pod, VastInstanceID: inst})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !row.StartedAt.Equal(t10.Add(-time.Hour)) {
		t.Errorf("started_at = %v, want %v", row.StartedAt, t10.Add(-time.Hour))
	}
	if f, _ := row.DphUsd.Float64Value(); f.Float64 != 0.12 {
		t.Errorf("dph_usd = %v, want 0.12", f.Float64)
	}

	num := func(s string) pgtype.Numeric {
		var n pgtype.Numeric
		if err := n.Scan(s); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ended := pgtype.Timestamptz{Time: t10.Add(8 * time.Hour), Valid: true}
	n, err := q.CloseAuxPodLifecycle(ctx, gen.CloseAuxPodLifecycleParams{EndedAt: ended, TotalCostUsd: num("1.5"), TotalCostBrl: num("7.5"), Pod: pod, VastInstanceID: inst})
	if err != nil || n != 1 {
		t.Fatalf("close 1: n=%d err=%v", n, err)
	}
	n, err = q.CloseAuxPodLifecycle(ctx, gen.CloseAuxPodLifecycleParams{EndedAt: ended, TotalCostUsd: num("99"), TotalCostBrl: num("495"), Pod: pod, VastInstanceID: inst})
	if err != nil || n != 0 {
		t.Fatalf("close repetido: n=%d err=%v want 0", n, err)
	}

	rows, err := q.ListAuxPodLifecyclesInRange(ctx, gen.ListAuxPodLifecyclesInRangeParams{FromTs: t10.Add(-24 * time.Hour), ToTs: t10.Add(24 * time.Hour)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Pod == pod {
			found = true
			if f, _ := r.TotalCostUsd.Float64Value(); f.Float64 != 1.5 {
				t.Errorf("total_cost_usd = %v, want 1.5 (close repetido não sobrescreve)", f.Float64)
			}
		}
	}
	if !found {
		t.Error("lifecycle ausente do ListAuxPodLifecyclesInRange")
	}

	if _, err := q.GetAuxPodLifecycle(ctx, gen.GetAuxPodLifecycleParams{Pod: pod, VastInstanceID: pgtype.Int8{Int64: 999, Valid: true}}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("get inexistente: err=%v want pgx.ErrNoRows", err)
	}
}

// TestSumEconomyTokensRange: agregado gateway-wide real — tokens servidos
// local (phantom > 0) vs total e split créditos Haiku × caixa externo.
func TestSumEconomyTokensRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)
	ts := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		upstream          string
		in, out           int
		phantom, external string
	}{
		{"local-llm", 100, 900, "0.5", "0"},        // local: 1000 tokens
		{"anthropic-haiku", 1000, 1000, "0", "10"}, // créditos
		{"openrouter-chat", 500, 500, "0", "5"},    // caixa
	}
	for i, r := range rows {
		if _, err := pool.Exec(ctx, `
			INSERT INTO ai_gateway.billing_events
				(request_id, ts, tenant_id, route, upstream, model,
				 tokens_in, tokens_out, audio_seconds, embeds_count,
				 cost_local_brl, cost_local_phantom_brl, cost_external_brl, source)
			VALUES ($1, $2, $3, 'chat', $4, 'qwen', $5, $6, 0, 0, 0, $7::numeric, $8::numeric, 'final')
		`, uuid.New(), ts.Add(time.Duration(i)*time.Minute), seed.ConverseAITenantID, r.upstream, r.in, r.out, r.phantom, r.external); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	got, err := gen.New(pool).SumEconomyTokensRange(ctx, gen.SumEconomyTokensRangeParams{Ts: ts.Add(-time.Hour), Ts_2: ts.Add(time.Hour)})
	if err != nil {
		t.Fatalf("SumEconomyTokensRange: %v", err)
	}
	if got.LocalTokens != 1000 || got.TotalTokens != 4000 {
		t.Errorf("tokens local/total = %d/%d, want 1000/4000", got.LocalTokens, got.TotalTokens)
	}
	if f, _ := got.CreditsCoveredBrl.Float64Value(); f.Float64 != 10 {
		t.Errorf("credits_covered_brl = %v, want 10", f.Float64)
	}
	if f, _ := got.ExternalCashBrl.Float64Value(); f.Float64 != 5 {
		t.Errorf("external_cash_brl = %v, want 5", f.Float64)
	}
}
