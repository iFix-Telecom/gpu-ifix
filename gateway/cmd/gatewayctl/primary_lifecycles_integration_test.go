//go:build integration

// Phase 6.6 Plan 06.6-10 Task 3 — gatewayctl primary lifecycles DB-fetch
// integration coverage (BLOCKER 3 closure).
//
// Plan 06.6-09's primary_test.go skipped TestRunPrimaryLifecycles_FetchesFromDB
// with a pointer to this file: the DB round-trip requires a live
// Postgres + migrations applied + seeded rows + ListPrimaryLifecycles
// against the actual sqlc binding. This test spins up a
// testcontainers Postgres, applies db.Up to land the ai_gateway schema
// (including 0023_primary_lifecycles.sql), seeds rows of varying
// shapes, and asserts:
//
//   1. TestRunPrimaryLifecycles_FetchesFromDB: 3 seeded rows, default
//      --since 7d, --limit 20 → tabwriter output contains all 3 IDs in
//      DESC chronological order.
//
//   2. TestRunPrimaryLifecycles_RespectsLimitFlag: 5 seeded rows,
//      --limit 2 → tabwriter shows exactly 2 rows.
//
//   3. TestRunPrimaryLifecycles_EmptyTable_NoRows: empty table → the
//      header row prints (table mode) but no data rows.
//
// The test uses runPrimaryLifecyclesWithPool (Plan 06.6-10 Task 3
// refactor) so the testcontainers pool can be threaded in directly
// without env-var dancing.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// freshPrimaryLifecyclesPool brings up a testcontainers Postgres, applies
// db.Up to land the full ai_gateway schema (including
// 0023_primary_lifecycles.sql), TRUNCATES the primary_lifecycles table
// to ensure deterministic seed counts. Returns a pool whose lifetime
// is managed by t.Cleanup.
func freshPrimaryLifecyclesPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("gatewayctl_primary_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	cfg := config.Config{PGDSN: dsn}
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool.Reset()

	if _, err := pool.Exec(ctx,
		`TRUNCATE ai_gateway.primary_lifecycles RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate primary_lifecycles: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	_ = filepath.Clean
	_ = runtime.Caller
	return pool
}

// seedPrimaryLifecycle inserts a primary_lifecycles row with the given
// fields. started_at is set to NOW() - offsetMinutes * 1m so the test
// can construct deterministic chronological orderings.
func seedPrimaryLifecycle(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	triggerReason string, offsetMinutes int, instanceID int64, ended bool) int64 {
	t.Helper()
	q := gen.New(pool)
	row, err := q.InsertPrimaryLifecycle(ctx, gen.InsertPrimaryLifecycleParams{
		TriggerReason: triggerReason,
	})
	if err != nil {
		t.Fatalf("insert lifecycle: %v", err)
	}
	// Push started_at backwards via UPDATE so the ORDER BY DESC is
	// deterministic across the 3 / 5 row seeds.
	if _, err := pool.Exec(ctx,
		`UPDATE ai_gateway.primary_lifecycles
		 SET started_at = NOW() - $2 * INTERVAL '1 minute',
		     vast_instance_id = $3
		 WHERE id = $1`, row.ID, offsetMinutes, instanceID); err != nil {
		t.Fatalf("update started_at: %v", err)
	}
	if ended {
		if err := q.ClosePrimaryLifecycle(ctx, gen.ClosePrimaryLifecycleParams{
			ID:             row.ID,
			ShutdownReason: pgtype.Text{String: "test_ended", Valid: true},
			EventJson:      []byte(`{"reason":"test"}`),
		}); err != nil {
			t.Fatalf("close lifecycle: %v", err)
		}
	}
	return row.ID
}

// capturePrimaryLifecyclesStdout swaps os.Stdout for a pipe + buffer for
// the duration of fn() and returns the captured bytes. Mirror of
// emerg_test.go captureStdout — same ordering invariant.
func capturePrimaryLifecyclesStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, r)
	}()
	fn()
	_ = w.Close()
	wg.Wait()
	os.Stdout = orig
	return buf.String()
}

// TestRunPrimaryLifecyclesIntegration_FetchesFromDB — BLOCKER 3 closure:
// 3 seeded rows + default --since 7d --limit 20 + table format → output
// contains all 3 IDs (DESC chronological order). Renamed from
// TestRunPrimaryLifecycles_FetchesFromDB (which is the t.Skip placeholder
// in primary_test.go) to avoid the duplicate-symbol error under the
// `integration` build tag.
func TestRunPrimaryLifecyclesIntegration_FetchesFromDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool := freshPrimaryLifecyclesPool(t, ctx)

	// Seed 3 rows with varying timestamps. Higher offset = older.
	id1 := seedPrimaryLifecycle(t, ctx, pool, "schedule_window_entered", 10, 101, false)
	id2 := seedPrimaryLifecycle(t, ctx, pool, "manual_force_up", 5, 102, false)
	id3 := seedPrimaryLifecycle(t, ctx, pool, "schedule_window_entered", 1, 103, true)

	stdout := capturePrimaryLifecyclesStdout(t, func() {
		code := runPrimaryLifecyclesWithPool(ctx, pool, 7*24*time.Hour, 20, "table")
		require.Equal(t, 0, code, "runPrimaryLifecyclesWithPool must exit 0 on happy path")
	})

	// Output must contain the table header + all 3 row IDs.
	require.Contains(t, stdout, "ID", "table header must be present")
	require.Contains(t, stdout, "STARTED")
	require.Contains(t, stdout, "TRIGGER")
	require.Contains(t, stdout, fmt.Sprintf("%d\t", id1), "row id %d must appear in output", id1)
	require.Contains(t, stdout, fmt.Sprintf("%d\t", id2), "row id %d must appear in output", id2)
	require.Contains(t, stdout, fmt.Sprintf("%d\t", id3), "row id %d must appear in output", id3)

	// DESC chronological order: id3 (newest) appears BEFORE id1 (oldest).
	idxID3 := strings.Index(stdout, fmt.Sprintf("%d\t", id3))
	idxID2 := strings.Index(stdout, fmt.Sprintf("%d\t", id2))
	idxID1 := strings.Index(stdout, fmt.Sprintf("%d\t", id1))
	require.True(t, idxID3 < idxID2 && idxID2 < idxID1,
		"rows must be ORDER BY started_at DESC: id3(newest) < id2 < id1(oldest); got idxID3=%d idxID2=%d idxID1=%d",
		idxID3, idxID2, idxID1)
}

// TestRunPrimaryLifecycles_RespectsLimitFlag — seed 5 rows, --limit 2 →
// tabwriter shows exactly 2 data rows. Proves the LIMIT clause is honored.
func TestRunPrimaryLifecycles_RespectsLimitFlag(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool := freshPrimaryLifecyclesPool(t, ctx)

	ids := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		offset := 10 - i // older rows first
		id := seedPrimaryLifecycle(t, ctx, pool, "schedule_window_entered", offset, int64(200+i), false)
		ids = append(ids, id)
	}

	stdout := capturePrimaryLifecyclesStdout(t, func() {
		code := runPrimaryLifecyclesWithPool(ctx, pool, 7*24*time.Hour, 2, "table")
		require.Equal(t, 0, code)
	})

	// Exactly 2 data rows (header + 2 lines). The tabwriter introduces
	// padding/alignment so a strict line count is brittle — instead count
	// how many seeded IDs appear in the output.
	count := 0
	for _, id := range ids {
		if strings.Contains(stdout, fmt.Sprintf("%d\t", id)) {
			count++
		}
	}
	require.Equal(t, 2, count,
		"--limit=2 must return exactly 2 rows from a 5-row table; got %d in stdout=%s", count, stdout)
}

// TestRunPrimaryLifecycles_EmptyTable_NoRows — empty primary_lifecycles
// table; runPrimaryLifecyclesWithPool must exit 0 with the header row +
// no data rows. Proves the SQL doesn't error on an empty set.
func TestRunPrimaryLifecycles_EmptyTable_NoRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool := freshPrimaryLifecyclesPool(t, ctx)

	stdout := capturePrimaryLifecyclesStdout(t, func() {
		code := runPrimaryLifecyclesWithPool(ctx, pool, 7*24*time.Hour, 20, "table")
		require.Equal(t, 0, code, "empty table must exit 0 (no error on empty set)")
	})

	require.Contains(t, stdout, "ID", "header must print even on empty table")
	require.Contains(t, stdout, "STARTED")
	require.NotContains(t, stdout, "\t101\t", "no seeded row id 101 should appear")
}
