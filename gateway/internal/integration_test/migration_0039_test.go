//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
)

// TestIntegration_Migration0039_DefaultsAndDownUp — quick-261001-qdd.
// Up adds pod_config offer_mode/bid_margin/max_preemptions_per_day/
// min_reliability with DEFAULTs (bid / 1.15 / 2 / 0.95) + primary_lifecycles
// is_bid/bid_price; Down drops all 6 columns and keeps exactly one
// pod_config_update_notify trigger; re-Up restores them.
func TestIntegration_Migration0039_DefaultsAndDownUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	colCount := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema='ai_gateway' AND (
			  (table_name='pod_config' AND column_name IN ('offer_mode','bid_margin','max_preemptions_per_day','min_reliability'))
			  OR (table_name='primary_lifecycles' AND column_name IN ('is_bid','bid_price')))`).Scan(&n); err != nil {
			t.Fatalf("columns query: %v", err)
		}
		return n
	}
	if got := colCount(); got != 6 {
		t.Fatalf("0039 columns after Up = %d, want 6", got)
	}

	// The DEFAULTs are what backfill the already-seeded prod row; assert them
	// via information_schema (independent of whether a row is seeded).
	var modeDefault, marginDefault, maxDefault string
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT column_default FROM information_schema.columns WHERE table_schema='ai_gateway' AND table_name='pod_config' AND column_name='offer_mode'),
		(SELECT column_default FROM information_schema.columns WHERE table_schema='ai_gateway' AND table_name='pod_config' AND column_name='bid_margin'),
		(SELECT column_default FROM information_schema.columns WHERE table_schema='ai_gateway' AND table_name='pod_config' AND column_name='max_preemptions_per_day')`).
		Scan(&modeDefault, &marginDefault, &maxDefault); err != nil {
		t.Fatalf("defaults query: %v", err)
	}
	if modeDefault != "'bid'::text" || marginDefault != "1.15" || maxDefault != "2" {
		t.Fatalf("defaults = (%q,%q,%q), want ('bid'::text,1.15,2)", modeDefault, marginDefault, maxDefault)
	}
	var relDefault string
	if err := pool.QueryRow(ctx, `SELECT column_default FROM information_schema.columns
		WHERE table_schema='ai_gateway' AND table_name='pod_config' AND column_name='min_reliability'`).Scan(&relDefault); err != nil {
		t.Fatalf("min_reliability default query: %v", err)
	}
	if relDefault != "0.95" {
		t.Fatalf("min_reliability default = %q, want 0.95", relDefault)
	}

	if err := db.Down(ctx, pool, 1); err != nil {
		t.Fatalf("db.Down(1) revert 0039: %v", err)
	}
	if got := colCount(); got != 0 {
		t.Fatalf("0039 columns after Down = %d, want 0", got)
	}
	var trig int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_trigger
		WHERE tgname='pod_config_update_notify' AND NOT tgisinternal`).Scan(&trig); err != nil {
		t.Fatalf("trigger query: %v", err)
	}
	if trig != 1 {
		t.Fatalf("pod_config_update_notify count after Down = %d, want 1", trig)
	}
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if got := colCount(); got != 6 {
		t.Fatalf("0039 columns after re-Up = %d, want 6", got)
	}
}
