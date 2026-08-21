package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultPartitionLookahead is the rolling window (months) for which
// EnsurePartitions creates missing partitions. N=3 covers current + 2 next,
// matching the seed migration 0003/0004.
const DefaultPartitionLookahead = 3

// EnsurePartitions creates monthly partitions of ai_gateway.audit_log,
// ai_gateway.audit_log_content and ai_gateway.billing_events for the window
// [truncMonth(now), now+nMonths]. Idempotent (CREATE TABLE IF NOT EXISTS).
// Safe to call on every gateway boot. Addresses Codex review [LOW] 02-02
// (partition automation). billing_events added 2026-08-21: it was missing
// from this list, so once the seed partitions from migration 0010 ran out
// (202607) every billing flush failed with SQLSTATE 23514 — billing events
// from 2026-08-01 onward were dropped until the partitions were created by
// hand and this fix shipped.
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time, nMonths int) error {
	if nMonths <= 0 {
		nMonths = DefaultPartitionLookahead
	}
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= nMonths; i++ {
		m := start.AddDate(0, i, 0)
		end := m.AddDate(0, 1, 0)
		for _, table := range []string{"audit_log", "audit_log_content", "billing_events"} {
			partName := fmt.Sprintf("%s_%04d%02d", table, m.Year(), int(m.Month()))
			q := fmt.Sprintf(
				`CREATE TABLE IF NOT EXISTS ai_gateway.%s PARTITION OF ai_gateway.%s FOR VALUES FROM ('%s') TO ('%s')`,
				partName, table,
				m.Format("2006-01-02"), end.Format("2006-01-02"),
			)
			if _, err := pool.Exec(ctx, q); err != nil {
				return fmt.Errorf("db.EnsurePartitions: create %s: %w", partName, err)
			}
		}
	}
	return nil
}
