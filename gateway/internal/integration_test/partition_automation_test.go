//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
)

// TestIntegration_10_PartitionAutomation verifies db.EnsurePartitions
// creates the expected monthly partitions (current + N months ahead) for
// audit_log, audit_log_content and billing_events. Codex review [LOW] 02-02
// regression guard; billing_events added 2026-08-21 after prod billing
// flushes 23514'd when the migration-seeded partitions ran out.
func TestIntegration_10_PartitionAutomation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	// The migration itself seeded current + 2 months. Run EnsurePartitions
	// for lookahead=3 (current + 3 ahead = 4 months total) to exercise the
	// idempotent creation path.
	now := time.Now().UTC()
	if err := db.EnsurePartitions(ctx, pool, now, 3); err != nil {
		t.Fatalf("ensure partitions: %v", err)
	}

	// Verify partitions exist for current month + 3 months ahead.
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= 3; i++ {
		m := start.AddDate(0, i, 0)
		for _, table := range []string{"audit_log", "audit_log_content", "billing_events"} {
			partName := fmt.Sprintf("%s_%04d%02d", table, m.Year(), int(m.Month()))
			var exists bool
			err := pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM pg_class c
					JOIN pg_namespace n ON n.oid = c.relnamespace
					WHERE c.relname = $1 AND n.nspname = 'ai_gateway'
				)`, partName).Scan(&exists)
			if err != nil {
				t.Fatalf("query pg_class for %s: %v", partName, err)
			}
			if !exists {
				t.Errorf("partition %s does not exist", partName)
			}
		}
	}

	// Second call is idempotent (CREATE TABLE IF NOT EXISTS).
	if err := db.EnsurePartitions(ctx, pool, now, 3); err != nil {
		t.Fatalf("ensure partitions idempotent: %v", err)
	}
}
