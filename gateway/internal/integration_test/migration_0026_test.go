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

	// freshSchema applied all migrations up to HEAD — assert seed state.
	// Phase 11.1 migration 0028 DELETEs the (whisper, local-stt) row so the
	// post-freshSchema count is 5 (2 tier-0 local-llm/local-embed + 3 tier-1)
	// instead of the historical 6 (3 + 3).
	var aliasCountAfterUp int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&aliasCountAfterUp); err != nil {
		t.Fatalf("count after Up: %v", err)
	}
	if aliasCountAfterUp != 8 {
		t.Errorf("model_aliases count after Up = %d, want 8 (post-0029: 2 tier-0 + 6 tier-1 incl gemini-stt/groq-whisper/local-stt re-added)", aliasCountAfterUp)
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

	// Down 4 steps → revert 0029 + 0028 + 0027 + 0026 (test specifically
	// exercises 0026's Down behavior; 0027 + 0028 + 0029 live on top and must
	// be reverted first).
	// Phase 11.1: was Down(2); bumped to Down(3) when 0028 landed on HEAD.
	// Phase 11.2: bumped to Down(4) when 0029 landed on HEAD.
	//
	// Phase 11.2 quirk — split into Down(2) + cleanup + Down(2):
	// reverting 0028 restores (whisper, local-stt), which (combined with the
	// surviving (whisper, openai-whisper) tier-1 row) becomes a duplicate
	// alias. 0026's R3 guard would then RAISE EXCEPTION and abort the chain.
	// Manually DELETE the restored row before continuing the Down march so
	// the clean-path round-trip can be exercised.
	// HEAD is now 0030 (probe_status_allow_config, row-neutral on model_aliases).
	// Down(3) peels 0030+0029+0028 to reach the same point the old Down(2) did
	// when 0029 was HEAD.
	if err := db.Down(ctx, pool, 3); err != nil {
		t.Fatalf("db.Down(3) revert 0030+0029+0028: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM ai_gateway.model_aliases WHERE alias='whisper' AND upstream_name='local-stt'`); err != nil {
		t.Fatalf("manual cleanup of restored (whisper,local-stt) row: %v", err)
	}
	if err := db.Down(ctx, pool, 2); err != nil {
		t.Fatalf("db.Down(2) revert 0027+0026: %v", err)
	}

	// After Down: tier-1 rows removed (2 rows left) AND PK reverted to (alias).
	// Phase 11.2: was 3 (3 tier-0); now 2 because the manual cleanup above
	// removed (whisper, local-stt) to dodge 0026's R3 guard. Only the
	// (qwen, local-llm) + (bge-m3, local-embed) tier-0 rows remain.
	var aliasCountAfterDown int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&aliasCountAfterDown); err != nil {
		t.Fatalf("count after Down: %v", err)
	}
	if aliasCountAfterDown != 2 {
		t.Errorf("model_aliases count after Down = %d, want 2 (tier-0 only, minus manually-deleted (whisper,local-stt))", aliasCountAfterDown)
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
	if tier0NotNullCount != 2 {
		t.Errorf("tier-0 backfilled rows after Down = %d, want 2 (local-llm + local-embed; local-stt cleaned up to dodge R3 guard)",
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
	// Phase 11.1: re-Up applies through 0028 again, so count is 5 (idempotent
	// across the whole stack — was 6 before 0028).
	if aliasCountAfterReUp != 8 {
		t.Errorf("model_aliases count after re-Up = %d, want 8 (idempotent post-0029 re-application)", aliasCountAfterReUp)
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

	// freshSchema applied all migrations through HEAD (currently 0028) → 5
	// rows after the Phase 11.1 (whisper, local-stt) DELETE. The seeded
	// tier-1 (qwen, openrouter-chat) row's target is "deepseek/deepseek-v4-flash:nitro"
	// (migration 0027 UPDATEd it from "qwen/qwen3.5-27b"). INSERT an
	// operator-created duplicate-alias row that 0026's R3 guard MUST detect
	// when its Down direction runs.
	if _, err := pool.Exec(ctx,
		`INSERT INTO ai_gateway.model_aliases (alias, upstream, target, upstream_name)
		 VALUES ('qwen', 'llm', 'qwen-custom', 'openrouter-experimental')`); err != nil {
		t.Fatalf("INSERT operator-duplicate row: %v", err)
	}

	// Count rows BEFORE the attempted Down so we can assert it didn't change.
	// Phase 11.1: 5 seeded (post-0028) + 1 operator = 6.
	// Phase 11.2: 8 seeded (post-0029: 5 + 3 new STT rows) + 1 operator = 9.
	var countBefore int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&countBefore); err != nil {
		t.Fatalf("count before Down: %v", err)
	}
	if countBefore != 9 { // 8 seeded post-0029 + 1 operator
		t.Fatalf("count before Down = %d, want 9 (8 seeded post-0029 + 1 operator-injected)", countBefore)
	}

	// Attempt Down 4 steps — reverts 0029 (removes 3 STT rows + restores
	// openai-whisper.tier_priority=0), then 0028 (restores (whisper, local-stt)),
	// then 0027 (clean), then 0026 (MUST fail with the guard's RAISE EXCEPTION
	// message — at this point both (whisper, openai-whisper) and the duplicate
	// (qwen, openrouter-experimental) inserted by the operator are present
	// alongside the restored (whisper, local-stt), so the guard catches both
	// duplicate-alias clusters). goose runs each step as its own transaction;
	// the 0029+0028+0027 Downs complete first, then 0026's Down fires the
	// guard. db.Down returns the failing step's error.
	// Phase 11.1: was Down(2); bumped to Down(3) when 0028 landed on HEAD.
	// Phase 11.2: bumped to Down(4) when 0029 landed on HEAD.
	// HEAD is now 0030 (probe_status_allow_config, row-neutral). Down(5) peels
	// 0030+0029+0028+0027 then fires 0026's R3 guard (was Down(4) when 0029 HEAD).
	err := db.Down(ctx, pool, 5)
	if err == nil {
		t.Fatal("db.Down(5) succeeded; expected error from R3 duplicate-alias guard during 0026 Down")
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
	// the WHOLE 0026 Down transaction, those DELETEs roll back inside 0026's
	// own txn.
	//
	// Phase 11.1: 0028's Down already committed (separate txn) and restored
	// (whisper, local-stt) before the guard fired in 0026's Down. So the
	// post-aborted-Down count is countBefore + 1 (one extra row from the
	// successful 0028 Down before the abort).
	// Phase 11.2: 0029's Down committed first (-3), then 0028's Down (+1),
	// so net delta = -2 vs countBefore. 0027's Down is row-neutral. 0026's
	// Down aborts → state preserved at countBefore - 2.
	var countAfter int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&countAfter); err != nil {
		t.Fatalf("count after aborted Down: %v", err)
	}
	wantCountAfter := countBefore - 2 // 0029 Down -3 STT rows; 0028 Down +1 (whisper,local-stt); 0027 +0
	if countAfter != wantCountAfter {
		t.Errorf("model_aliases count after aborted Down = %d, want %d (R3 guard MUST be transactional within 0026; 0029+0028 Downs already committed)",
			countAfter, wantCountAfter)
	}

	// Recovery path: operator DELETEs the offending duplicate row, then
	// Down succeeds. Spot-check this contract too (it's documented in the
	// migration's Step 5 comment).
	if _, err := pool.Exec(ctx,
		`DELETE FROM ai_gateway.model_aliases
		 WHERE alias='qwen' AND upstream_name='openrouter-experimental'`); err != nil {
		t.Fatalf("DELETE operator-duplicate row: %v", err)
	}
	// Phase 11.2: also remove (whisper, local-stt) — restored by the
	// committed 0028 Down — to clear the second duplicate-alias cluster
	// (otherwise 0026's R3 guard would fire again on the (whisper, *) pair).
	if _, err := pool.Exec(ctx,
		`DELETE FROM ai_gateway.model_aliases
		 WHERE alias='whisper' AND upstream_name='local-stt'`); err != nil {
		t.Fatalf("DELETE restored (whisper, local-stt) row: %v", err)
	}
	if err := db.Down(ctx, pool, 1); err != nil {
		t.Fatalf("db.Down after recovery DELETE: %v (Down should succeed once duplicates are removed)", err)
	}
	var countAfterRecovery int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ai_gateway.model_aliases`).Scan(&countAfterRecovery); err != nil {
		t.Fatalf("count after recovery Down: %v", err)
	}
	// Phase 11.2: post-recovery count is 2 — (local-llm, local-embed)
	// tier-0 rows. (whisper, local-stt) was manually deleted above to
	// clear the duplicate; the 3 tier-1 0026-seeded rows were deleted
	// by 0026's Step 1 before the now-passing guard.
	if countAfterRecovery != 2 {
		t.Errorf("model_aliases count after recovery Down = %d, want 2 (tier-0 only minus manually-deleted whisper,local-stt)", countAfterRecovery)
	}

	t.Logf("R3 GUARD VERIFIED: duplicate-alias Down aborted with %q; transactional safety preserved; recovery path works",
		wantPhrase)
}
