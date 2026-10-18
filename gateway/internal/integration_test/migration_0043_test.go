//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
)

// TestIntegration_Migration0043_PricesScale — Phase 23. Up amplia
// prices.unit_cost_usd para NUMERIC(20,12): o cache_write do Haiku 5.5
// (1.25e-7) passa a ser gravado exato, não 1.2e-7. O trigger
// prices_update_notify (que cita a coluna no WHEN) continua existindo uma
// vez só e disparando. Down volta a NUMERIC(12,8) e re-Up restaura.
func TestIntegration_Migration0043_PricesScale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	typeOf := func() (int, int) {
		var p, s int
		if err := pool.QueryRow(ctx, `SELECT numeric_precision, numeric_scale FROM information_schema.columns
			WHERE table_schema='ai_gateway' AND table_name='prices' AND column_name='unit_cost_usd'`).Scan(&p, &s); err != nil {
			t.Fatalf("column type query: %v", err)
		}
		return p, s
	}
	triggerCount := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_trigger
			WHERE tgname='prices_update_notify' AND tgrelid='ai_gateway.prices'::regclass`).Scan(&n); err != nil {
			t.Fatalf("trigger query: %v", err)
		}
		return n
	}

	if p, s := typeOf(); p != 20 || s != 12 {
		t.Fatalf("unit_cost_usd after Up = NUMERIC(%d,%d), want (20,12)", p, s)
	}
	if n := triggerCount(); n != 1 {
		t.Fatalf("prices_update_notify count after Up = %d, want 1", n)
	}

	const model = "p0043-scale"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ai_gateway.prices WHERE model=$1`, model)
	})
	var got string
	if err := pool.QueryRow(ctx, `INSERT INTO ai_gateway.prices (model, provider, unit, unit_cost_usd)
		VALUES ($1, 'anthropic', 'input_token', 0.000000125) RETURNING unit_cost_usd::text`, model).Scan(&got); err != nil {
		t.Fatalf("insert price: %v", err)
	}
	if got != "0.000000125000" {
		t.Fatalf("stored unit_cost_usd = %s, want 0.000000125000 (sem arredondar para 8 casas)", got)
	}

	// O trigger recriado ainda notifica em UPDATE de unit_cost_usd.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `LISTEN prices_changed`); err != nil {
		t.Fatalf("listen: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_gateway.prices SET unit_cost_usd = 0.000000625 WHERE model=$1`, model); err != nil {
		t.Fatalf("update price: %v", err)
	}
	nctx, ncancel := context.WithTimeout(ctx, 5*time.Second)
	defer ncancel()
	if _, err := conn.Conn().WaitForNotification(nctx); err != nil {
		t.Fatalf("no prices_changed notification after UPDATE: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ai_gateway.prices WHERE model=$1`, model); err != nil {
		t.Fatalf("delete test row: %v", err)
	}

	// 0044 (model_aliases priority/fallback_models) HEAD bump: Down(1)→Down(2).
	if err := db.Down(ctx, pool, 2); err != nil {
		t.Fatalf("db.Down(2) revert 0044+0043: %v", err)
	}
	if p, s := typeOf(); p != 12 || s != 8 {
		t.Fatalf("unit_cost_usd after Down = NUMERIC(%d,%d), want (12,8)", p, s)
	}
	if n := triggerCount(); n != 1 {
		t.Fatalf("prices_update_notify count after Down = %d, want 1", n)
	}

	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("db.Up after Down: %v", err)
	}
	if p, s := typeOf(); p != 20 || s != 12 {
		t.Fatalf("unit_cost_usd after re-Up = NUMERIC(%d,%d), want (20,12)", p, s)
	}
}
