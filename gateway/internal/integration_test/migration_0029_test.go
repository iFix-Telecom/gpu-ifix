//go:build integration

// Phase 11.2 Plan 03 Task 1 — Migration 0029 round-trip + cascade STT shape.
//
// Migration 0029 (gateway/db/migrations/0029_readd_whisper_add_gemini_groq.sql):
//
//  1. ALTER TABLE upstreams ADD COLUMN tier_priority INT NOT NULL DEFAULT 0
//  2. Replace UNIQUE(role,tier) with UNIQUE(role,tier,tier_priority)
//  3. Re-INSERT local-stt + alias (whisper,local-stt) → faster-whisper-large-v3
//  4. INSERT gemini-stt tier=1 tier_priority=10 + alias + circuit_config={"cooldown_s":120}
//  5. INSERT groq-whisper tier=1 tier_priority=15 + alias → whisper-large-v3
//  6. UPDATE openai-whisper SET tier_priority=20
//
// `circuit_config` column already exists since 0007 (PATTERNS.md line 533) —
// migration 0029 only UPDATEs it on the gemini-stt row, no ADD COLUMN.
//
// DOWN: symmetric DELETEs; keep tier_priority column (additive change).
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
)

// TestIntegration_Migration0029_Up_RestoresLocalSTT — D-B6′ step 3.
// After 0029 Up, the local-stt upstream row + (whisper,local-stt) alias
// MUST exist (reverts 0028 deletion).
func TestIntegration_Migration0029_Up_RestoresLocalSTT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	var role, urlEnv string
	var tier, prio int
	var authEnv *string
	if err := pool.QueryRow(ctx, `
		SELECT role, tier, tier_priority, url_env, auth_bearer_env
		  FROM ai_gateway.upstreams
		 WHERE name='local-stt'`).Scan(&role, &tier, &prio, &urlEnv, &authEnv); err != nil {
		t.Fatalf("read local-stt row after 0029 Up: %v", err)
	}
	if role != "stt" || tier != 0 || prio != 0 || urlEnv != "UPSTREAM_STT_URL" || authEnv != nil {
		ae := "<nil>"
		if authEnv != nil {
			ae = *authEnv
		}
		t.Errorf("local-stt shape after 0029 Up: role=%q tier=%d prio=%d url_env=%q auth_env=%s; want stt/0/0/UPSTREAM_STT_URL/<nil>",
			role, tier, prio, urlEnv, ae)
	}

	var target string
	if err := pool.QueryRow(ctx, `
		SELECT target FROM ai_gateway.model_aliases
		 WHERE alias='whisper' AND upstream_name='local-stt'`).Scan(&target); err != nil {
		t.Fatalf("read (whisper,local-stt) alias after 0029 Up: %v", err)
	}
	if target != "Systran/faster-whisper-large-v3" {
		t.Errorf("(whisper,local-stt) target after 0029 Up = %q, want Systran/faster-whisper-large-v3", target)
	}
}

// TestIntegration_Migration0029_Up_AddsGeminiSTT_TierPriority10_CooldownS120 — D-B6′ step 4 + D-B11.
// gemini-stt row MUST exist with tier=1, tier_priority=10, and
// circuit_config->>'cooldown_s' = '120'.
func TestIntegration_Migration0029_Up_AddsGeminiSTT_TierPriority10_CooldownS120(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	var role, urlEnv string
	var authEnv *string
	var tier, prio int
	var cooldown string
	if err := pool.QueryRow(ctx, `
		SELECT role, tier, tier_priority, url_env, auth_bearer_env,
		       circuit_config->>'cooldown_s'
		  FROM ai_gateway.upstreams
		 WHERE name='gemini-stt'`).Scan(&role, &tier, &prio, &urlEnv, &authEnv, &cooldown); err != nil {
		t.Fatalf("read gemini-stt row after 0029 Up: %v", err)
	}
	if role != "stt" || tier != 1 || prio != 10 {
		t.Errorf("gemini-stt shape after 0029 Up: role=%q tier=%d prio=%d; want stt/1/10",
			role, tier, prio)
	}
	if urlEnv != "UPSTREAM_STT_FALLBACK_1_URL" {
		t.Errorf("gemini-stt url_env = %q, want UPSTREAM_STT_FALLBACK_1_URL (D-B7)", urlEnv)
	}
	if authEnv == nil || *authEnv != "UPSTREAM_STT_FALLBACK_1_AUTH_BEARER" {
		ae := "<nil>"
		if authEnv != nil {
			ae = *authEnv
		}
		t.Errorf("gemini-stt auth_bearer_env = %s, want UPSTREAM_STT_FALLBACK_1_AUTH_BEARER", ae)
	}
	if cooldown != "120" {
		t.Errorf("gemini-stt circuit_config cooldown_s = %q, want 120 (D-B11)", cooldown)
	}

	var target string
	if err := pool.QueryRow(ctx, `
		SELECT target FROM ai_gateway.model_aliases
		 WHERE alias='whisper' AND upstream_name='gemini-stt'`).Scan(&target); err != nil {
		t.Fatalf("read (whisper,gemini-stt) alias after 0029 Up: %v", err)
	}
	if target != "gemini-2.5-flash-lite" {
		t.Errorf("(whisper,gemini-stt) target = %q, want gemini-2.5-flash-lite (D-B3)", target)
	}
}

// TestIntegration_Migration0029_Up_AddsGroqWhisper_TierPriority15 — D-B6′ step 5.
// groq-whisper row MUST exist with tier=1, tier_priority=15; alias
// (whisper,groq-whisper) → whisper-large-v3.
func TestIntegration_Migration0029_Up_AddsGroqWhisper_TierPriority15(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	var role, urlEnv string
	var authEnv *string
	var tier, prio int
	if err := pool.QueryRow(ctx, `
		SELECT role, tier, tier_priority, url_env, auth_bearer_env
		  FROM ai_gateway.upstreams
		 WHERE name='groq-whisper'`).Scan(&role, &tier, &prio, &urlEnv, &authEnv); err != nil {
		t.Fatalf("read groq-whisper row after 0029 Up: %v", err)
	}
	if role != "stt" || tier != 1 || prio != 15 {
		t.Errorf("groq-whisper shape after 0029 Up: role=%q tier=%d prio=%d; want stt/1/15",
			role, tier, prio)
	}
	if urlEnv != "UPSTREAM_STT_FALLBACK_2_URL" {
		t.Errorf("groq-whisper url_env = %q, want UPSTREAM_STT_FALLBACK_2_URL (D-B8)", urlEnv)
	}
	if authEnv == nil || *authEnv != "UPSTREAM_STT_FALLBACK_2_AUTH_BEARER" {
		ae := "<nil>"
		if authEnv != nil {
			ae = *authEnv
		}
		t.Errorf("groq-whisper auth_bearer_env = %s, want UPSTREAM_STT_FALLBACK_2_AUTH_BEARER", ae)
	}

	var target string
	if err := pool.QueryRow(ctx, `
		SELECT target FROM ai_gateway.model_aliases
		 WHERE alias='whisper' AND upstream_name='groq-whisper'`).Scan(&target); err != nil {
		t.Fatalf("read (whisper,groq-whisper) alias after 0029 Up: %v", err)
	}
	if target != "whisper-large-v3" {
		t.Errorf("(whisper,groq-whisper) target = %q, want whisper-large-v3 (D-B8)", target)
	}
}

// TestIntegration_Migration0029_Up_PromotesOpenAIWhisper_TierPriority20 — D-B6′ step 6.
// openai-whisper.tier_priority MUST update from 0 (default after step 1) to 20.
func TestIntegration_Migration0029_Up_PromotesOpenAIWhisper_TierPriority20(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	var prio int
	if err := pool.QueryRow(ctx,
		`SELECT tier_priority FROM ai_gateway.upstreams WHERE name='openai-whisper'`).Scan(&prio); err != nil {
		t.Fatalf("read openai-whisper tier_priority after 0029 Up: %v", err)
	}
	if prio != 20 {
		t.Errorf("openai-whisper tier_priority after 0029 Up = %d, want 20", prio)
	}
}

// TestIntegration_Migration0029_Down_Symmetric — D-B6′ DOWN.
// After Up→Down: local-stt, gemini-stt, groq-whisper rows + their
// aliases MUST be DELETEd; openai-whisper.tier_priority reverts to 0.
// `tier_priority` column itself stays (additive — explicit choice).
func TestIntegration_Migration0029_Down_Symmetric(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	// HEAD is now 0030 (probe_status_allow_config, row-neutral on STT rows).
	// Down(2) peels 0030 then 0029 to exercise 0029's symmetric Down.
	if err := db.Down(ctx, pool, 2); err != nil {
		t.Fatalf("db.Down(2) revert 0030+0029: %v", err)
	}

	for _, name := range []string{"local-stt", "gemini-stt", "groq-whisper"} {
		var c int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM ai_gateway.upstreams WHERE name=$1`, name).Scan(&c); err != nil {
			t.Fatalf("count upstreams %s after Down: %v", name, err)
		}
		if c != 0 {
			t.Errorf("upstreams %q count after Down = %d, want 0", name, c)
		}

		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM ai_gateway.model_aliases
			  WHERE alias='whisper' AND upstream_name=$1`, name).Scan(&c); err != nil {
			t.Fatalf("count alias %s after Down: %v", name, err)
		}
		if c != 0 {
			t.Errorf("(whisper,%s) alias count after Down = %d, want 0", name, c)
		}
	}

	var prio int
	if err := pool.QueryRow(ctx,
		`SELECT tier_priority FROM ai_gateway.upstreams WHERE name='openai-whisper'`).Scan(&prio); err != nil {
		t.Fatalf("read openai-whisper tier_priority after Down: %v", err)
	}
	if prio != 0 {
		t.Errorf("openai-whisper tier_priority after Down = %d, want 0", prio)
	}

	// tier_priority column MUST still exist (additive).
	var colExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_schema='ai_gateway'
			   AND table_name='upstreams'
			   AND column_name='tier_priority'
		)`).Scan(&colExists); err != nil {
		t.Fatalf("check tier_priority column existence: %v", err)
	}
	if !colExists {
		t.Errorf("tier_priority column missing after Down — must be additive")
	}
}

// TestIntegration_Migration0029_Roundtrip_Idempotent — round-trip invariant.
// Up→Down→Up MUST yield identical post-Up state (idempotent).
func TestIntegration_Migration0029_Roundtrip_Idempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	if err := db.Down(ctx, pool, 1); err != nil {
		t.Fatalf("db.Down(1) initial: %v", err)
	}
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("db.Up after Down: %v", err)
	}

	// Same post-Up shape assertions as TestIntegration_Migration0029_Up_*
	// — pinned to a single SELECT that joins all 4 STT rows ordered by
	// tier_priority so a drift surfaces immediately.
	rows, err := pool.Query(ctx, `
		SELECT name, tier, tier_priority FROM ai_gateway.upstreams
		 WHERE role='stt' ORDER BY tier, tier_priority`)
	if err != nil {
		t.Fatalf("list stt rows after roundtrip: %v", err)
	}
	defer rows.Close()

	type sttRow struct {
		Name string
		Tier int
		Prio int
	}
	want := []sttRow{
		{"local-stt", 0, 0},
		{"gemini-stt", 1, 10},
		{"groq-whisper", 1, 15},
		{"openai-whisper", 1, 20},
	}
	var got []sttRow
	for rows.Next() {
		var r sttRow
		if err := rows.Scan(&r.Name, &r.Tier, &r.Prio); err != nil {
			t.Fatalf("scan stt row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("stt row count after roundtrip = %d, want %d (got=%+v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("stt row[%d] = %+v, want %+v (idempotent roundtrip)", i, got[i], w)
		}
	}
}
