//go:build integration

// Phase 06.9 Plan 05b Task 2 — Migration 0026 round-trip + R3 Down-abort guard.
//
// Migration 0026 (gateway/db/migrations/0026_evolve_model_aliases_per_upstream.sql)
// widens ai_gateway.model_aliases PK from (alias) to (alias, upstream_name)
// and seeds 3 tier-1 rows (qwen/openrouter-chat, whisper/openai-whisper,
// bge-m3/openai-embed). The Down direction includes a PL/pgSQL guard that
// RAISEs an exception if operator-created duplicate-alias rows exist —
// preferring explicit failure over silent data loss when restoring the
// single-column PK over duplicates.
//
// Tests (2):
//
//  1. TestIntegration_Migration0026_UpDownUp — clean path
//     - freshSchema applies 0001..0026 (6 rows: 3 tier-0 + 3 tier-1).
//     - db.Down(1) rolls back 0026 → back to 3 tier-0 rows + PK on (alias),
//     upstream_name column PRESERVED with tier-0 values backfilled.
//     - db.Up reapplies 0026 → 6 rows again (idempotent).
//
//  2. TestIntegration_Migration0026_DownAbortsOnDuplicateAliases — R3 guard
//     - Apply migration through 0026 (6 rows).
//     - INSERT an operator-created duplicate-alias row:
//     ('qwen', 'llm', 'qwen-custom', 'openrouter-experimental').
//     - Attempt db.Down(1) → goose returns an error whose .Error() contains
//     "Phase 06.9 migration 0026 Down aborted: duplicate-alias rows exist".
//     - Assert table state unchanged (guard fired BEFORE DROP CONSTRAINT —
//     the migration's PL/pgSQL DO block runs in the same transaction as
//     the rest of the Down body, so a RAISE EXCEPTION aborts everything).
package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
)

// TestIntegration_Migration0026_UpDownUp validates the clean Up→Down→Up
// idempotency contract on the live testcontainers Postgres.
func TestIntegration_Migration0026_UpDownUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	// freshSchema applied all migrations up to 0026 — assert seed state.
	var aliasCountAfterUp int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&aliasCountAfterUp); err != nil {
		t.Fatalf("count after Up: %v", err)
	}
	if aliasCountAfterUp != 6 {
		t.Errorf("model_aliases count after Up = %d, want 6 (3 tier-0 + 3 tier-1)", aliasCountAfterUp)
	}

	// Composite PK present.
	var pkCols string
	if err := pool.QueryRow(ctx, `
		SELECT string_agg(a.attname, ',' ORDER BY array_position(c.conkey, a.attnum))
		  FROM pg_constraint c
		  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		 WHERE c.conrelid = 'ai_gateway.model_aliases'::regclass
		   AND c.contype = 'p'`).Scan(&pkCols); err != nil {
		t.Fatalf("read PK columns after Up: %v", err)
	}
	if pkCols != "alias,upstream_name" {
		t.Errorf("PK columns after Up = %q, want %q", pkCols, "alias,upstream_name")
	}

	// Spot check the qwen tier-1 row landed. After migration 0027 the seeded
	// tier-1 OpenRouter target is `deepseek/deepseek-v4-flash:nitro`
	// (migration 0027 UPDATEs the row that 0026 seeded — value is what the
	// schema currently holds after `freshSchema` applies ALL migrations).
	var qwenTarget string
	if err := pool.QueryRow(ctx,
		`SELECT target FROM ai_gateway.model_aliases WHERE alias='qwen' AND upstream_name='openrouter-chat'`).
		Scan(&qwenTarget); err != nil {
		t.Fatalf("read qwen tier-1 row: %v", err)
	}
	const tier1QwenTarget = "deepseek/deepseek-v4-flash:nitro"
	if qwenTarget != tier1QwenTarget {
		t.Errorf("qwen tier-1 target = %q, want %q", qwenTarget, tier1QwenTarget)
	}

	// Down 2 steps → revert 0027 + 0026 (test specifically exercises 0026's
	// Down behavior; 0027 lives on top and must be reverted first).
	if err := db.Down(ctx, pool, 2); err != nil {
		t.Fatalf("db.Down(2): %v", err)
	}

	// After Down: tier-1 rows removed (3 rows left) AND PK reverted to (alias).
	var aliasCountAfterDown int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&aliasCountAfterDown); err != nil {
		t.Fatalf("count after Down: %v", err)
	}
	if aliasCountAfterDown != 3 {
		t.Errorf("model_aliases count after Down = %d, want 3 (3 tier-0 only)", aliasCountAfterDown)
	}

	var pkColsAfterDown string
	if err := pool.QueryRow(ctx, `
		SELECT string_agg(a.attname, ',' ORDER BY array_position(c.conkey, a.attnum))
		  FROM pg_constraint c
		  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		 WHERE c.conrelid = 'ai_gateway.model_aliases'::regclass
		   AND c.contype = 'p'`).Scan(&pkColsAfterDown); err != nil {
		t.Fatalf("read PK columns after Down: %v", err)
	}
	if pkColsAfterDown != "alias" {
		t.Errorf("PK columns after Down = %q, want %q", pkColsAfterDown, "alias")
	}

	// Per migration's Step 4: upstream_name column DELIBERATELY preserved.
	var colExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_schema = 'ai_gateway'
			   AND table_name   = 'model_aliases'
			   AND column_name  = 'upstream_name')`).Scan(&colExists); err != nil {
		t.Fatalf("check upstream_name column after Down: %v", err)
	}
	if !colExists {
		t.Errorf("upstream_name column missing after Down; the migration deliberately preserves it for re-Up idempotency")
	}

	// Per Step 4: the tier-0 backfilled values must survive. The 3 remaining
	// rows should all have non-null upstream_name pointing at local-*.
	var tier0NotNullCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases WHERE upstream_name LIKE 'local-%'`).
		Scan(&tier0NotNullCount); err != nil {
		t.Fatalf("count tier-0 backfilled rows after Down: %v", err)
	}
	if tier0NotNullCount != 3 {
		t.Errorf("tier-0 backfilled rows after Down = %d, want 3 (local-llm, local-stt, local-embed all preserved)",
			tier0NotNullCount)
	}

	// Re-apply Up — must be idempotent.
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("db.Up after Down: %v", err)
	}
	var aliasCountAfterReUp int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&aliasCountAfterReUp); err != nil {
		t.Fatalf("count after re-Up: %v", err)
	}
	if aliasCountAfterReUp != 6 {
		t.Errorf("model_aliases count after re-Up = %d, want 6 (idempotent re-application)", aliasCountAfterReUp)
	}

	t.Logf("MIGRATION 0026 ROUND-TRIP VERIFIED: Up→Down→Up clean; composite PK on (alias,upstream_name); column preserved; tier-0 values intact")
}

// TestIntegration_Migration0026_DownAbortsOnDuplicateAliases verifies R3:
// when operator-created data carries duplicate aliases across upstreams,
// the Down direction's PL/pgSQL guard aborts with an actionable message
// rather than silently restoring the PK on (alias) over duplicate rows.
func TestIntegration_Migration0026_DownAbortsOnDuplicateAliases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	// freshSchema applied all migrations through 0027 → 6 rows. The seeded
	// tier-1 (qwen, openrouter-chat) row's target is now
	// "deepseek/deepseek-v4-flash:nitro" (migration 0027 UPDATEd it from
	// "qwen/qwen3.5-27b"). INSERT an operator-created duplicate-alias row
	// that 0026's R3 guard MUST detect when its Down direction runs.
	// Step 1 of 0026's Down deletes the (qwen, openrouter-chat) seeded
	// tier-1 row but leaves the operator-inserted (qwen, openrouter-
	// experimental) row. The DO block then finds duplicate aliases
	// (qwen) across (local-llm) + (openrouter-experimental) → RAISE.
	if _, err := pool.Exec(ctx,
		`INSERT INTO ai_gateway.model_aliases (alias, upstream, target, upstream_name)
		 VALUES ('qwen', 'llm', 'qwen-custom', 'openrouter-experimental')`); err != nil {
		t.Fatalf("INSERT operator-duplicate row: %v", err)
	}

	// Count rows BEFORE the attempted Down so we can assert it didn't change.
	var countBefore int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&countBefore); err != nil {
		t.Fatalf("count before Down: %v", err)
	}
	if countBefore != 7 { // 6 seeded + 1 operator
		t.Fatalf("count before Down = %d, want 7 (6 seeded + 1 operator-injected)", countBefore)
	}

	// Attempt Down 2 steps — reverts 0027 (clean) then 0026 (MUST fail with
	// the guard's RAISE EXCEPTION message). goose runs each step as its own
	// transaction; the 0027 Down completes first, then 0026's Down fires the
	// guard. db.Down returns the first failing step's error.
	err := db.Down(ctx, pool, 2)
	if err == nil {
		t.Fatal("db.Down(2) succeeded; expected error from R3 duplicate-alias guard during 0026 Down")
	}
	wantPhrase := "Phase 06.9 migration 0026 Down aborted: duplicate-alias rows exist"
	if !strings.Contains(err.Error(), wantPhrase) {
		t.Errorf("Down error message does not contain expected guard text.\n  got:  %q\n  want substring: %q",
			err.Error(), wantPhrase)
	}

	// Table state unchanged — the guard fires inside the migration
	// transaction, so RAISE aborts everything. The composite PK must still
	// be in place (no DROP CONSTRAINT happened) AND row count unchanged.
	var pkCols string
	if err := pool.QueryRow(ctx, `
		SELECT string_agg(a.attname, ',' ORDER BY array_position(c.conkey, a.attnum))
		  FROM pg_constraint c
		  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		 WHERE c.conrelid = 'ai_gateway.model_aliases'::regclass
		   AND c.contype = 'p'`).Scan(&pkCols); err != nil {
		t.Fatalf("read PK columns after aborted Down: %v", err)
	}
	if pkCols != "alias,upstream_name" {
		t.Errorf("PK columns after aborted Down = %q, want %q (composite PK should survive transactional rollback)",
			pkCols, "alias,upstream_name")
	}

	// IMPORTANT — the migration's Step 1 deletes the tier-1 seeded rows
	// BEFORE the guard runs, but because the guard's RAISE EXCEPTION aborts
	// the WHOLE Down transaction, the DELETE rolls back too. Final row
	// count MUST equal the pre-Down count.
	var countAfter int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&countAfter); err != nil {
		t.Fatalf("count after aborted Down: %v", err)
	}
	if countAfter != countBefore {
		t.Errorf("model_aliases count after aborted Down = %d, want %d (R3 guard MUST be transactional — RAISE aborts the whole Down)",
			countAfter, countBefore)
	}

	// Recovery path: operator DELETEs the offending duplicate row, then
	// Down succeeds. Spot-check this contract too (it's documented in the
	// migration's Step 5 comment).
	if _, err := pool.Exec(ctx,
		`DELETE FROM ai_gateway.model_aliases
		 WHERE alias='qwen' AND upstream_name='openrouter-experimental'`); err != nil {
		t.Fatalf("DELETE operator-duplicate row: %v", err)
	}
	if err := db.Down(ctx, pool, 1); err != nil {
		t.Fatalf("db.Down after recovery DELETE: %v (Down should succeed once duplicates are removed)", err)
	}
	var countAfterRecovery int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&countAfterRecovery); err != nil {
		t.Fatalf("count after recovery Down: %v", err)
	}
	if countAfterRecovery != 3 {
		t.Errorf("model_aliases count after recovery Down = %d, want 3 (tier-0 only)", countAfterRecovery)
	}

	t.Logf("R3 GUARD VERIFIED: duplicate-alias Down aborted with %q; transactional safety preserved; recovery path works",
		wantPhrase)
}
