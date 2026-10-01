//go:build integration

// Quick 260930-vkt: helpers de isolamento entre testes de integração do
// gatewayctl (container Postgres compartilhado pelo binário de teste).
package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
)

var (
	modelAliasesSeedOnce sync.Once
	modelAliasesSeed     []byte // jsonb array com as linhas do seed
	modelAliasesSeedErr  error
)

// restoreModelAliasesSeed captura (1ª chamada) e restaura (todas as
// chamadas) o conteúdo de ai_gateway.model_aliases. A 1ª chamada roda num
// container recém-criado logo após db.Up, então o snapshot = estado do seed
// das migrations.
func restoreModelAliasesSeed(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	modelAliasesSeedOnce.Do(func() {
		modelAliasesSeedErr = pool.QueryRow(ctx,
			`SELECT COALESCE(jsonb_agg(to_jsonb(m)), '[]'::jsonb) FROM ai_gateway.model_aliases m`,
		).Scan(&modelAliasesSeed)
	})
	if modelAliasesSeedErr != nil {
		t.Fatalf("snapshot model_aliases seed: %v", modelAliasesSeedErr)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin restore model_aliases: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM ai_gateway.model_aliases`); err != nil {
		t.Fatalf("delete model_aliases: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ai_gateway.model_aliases
		 SELECT * FROM jsonb_populate_recordset(NULL::ai_gateway.model_aliases, $1::jsonb)`,
		modelAliasesSeed); err != nil {
		t.Fatalf("restore model_aliases: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit restore model_aliases: %v", err)
	}
}

// ensureTestPartitions cria as partições mensais do mês anterior até +3
// usando db.EnsurePartitions (mesma função do boot do gateway).
func ensureTestPartitions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	// 1º dia do mês anterior (AddDate(0,-1,0) num dia 31 pode pular um mês).
	now := time.Now().UTC()
	prev := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.EnsurePartitions(ctx, pool, prev, db.DefaultPartitionLookahead+1); err != nil {
		t.Fatalf("ensure partitions: %v", err)
	}
}
