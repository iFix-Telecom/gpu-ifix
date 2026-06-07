//go:build integration

// Phase 11.2 Plan 01 — Wave 0 RED stubs for migration 0029 (D-B6′).
//
// Migration 0029 (gateway/db/migrations/0029_readd_whisper_add_gemini_groq.sql,
// owned by Plan 02):
//
//  1. ALTER TABLE upstreams ADD COLUMN tier_priority INT NOT NULL DEFAULT 0
//  2. Replace UNIQUE(role,tier) with UNIQUE(role,tier,tier_priority)
//  3. Re-INSERT local-stt + alias (whisper,local-stt) → faster-whisper-large-v3
//  4. INSERT gemini-stt tier=1 tier_priority=10 + alias + circuit_config={"cooldown_s":120}
//  5. INSERT groq-whisper tier=1 tier_priority=15 + alias → whisper-large-v3
//  6. UPDATE openai-whisper SET tier_priority=20
//
// `circuit_config` column already exists since 0007 (PATTERNS.md line 533) —
// migration 0029 only UPDATES it on the gemini-stt row, no ADD COLUMN.
//
// DOWN: symmetric DELETEs; keep tier_priority column (additive change).
//
// Tests below pin the post-Up + post-Down + roundtrip contract.
package integration

import "testing"

// TestIntegration_Migration0029_Up_RestoresLocalSTT — D-B6′ step 3.
// After 0029 Up, the local-stt upstream row + (whisper,local-stt) alias
// MUST exist (reverts 0028 deletion).
func TestIntegration_Migration0029_Up_RestoresLocalSTT(t *testing.T) {
	t.Skip("OWNER: Plan 02 — implements migration 0029 Up; unskip + assert COUNT(upstreams WHERE name='local-stt')=1 + COUNT(model_aliases WHERE alias='whisper' AND upstream_name='local-stt')=1")
	// Expected:
	//   require.Equal(t, 1, countLocalSTTUpstream)
	//   require.Equal(t, 1, countLocalSTTAlias)
	// Reference: PATTERNS.md line 136-144 (verbatim INSERT pattern from 0028 DOWN).
}

// TestIntegration_Migration0029_Up_AddsGeminiSTT_TierPriority10_CooldownS120 — D-B6′ step 4 + D-B11.
// gemini-stt row MUST exist with tier=1, tier_priority=10, and
// circuit_config->>'cooldown_s' = '120' (JSONB column already present
// since 0007 — only UPDATE needed, no ADD COLUMN).
func TestIntegration_Migration0029_Up_AddsGeminiSTT_TierPriority10_CooldownS120(t *testing.T) {
	t.Skip("OWNER: Plan 02 — implements migration 0029 Up gemini-stt row; unskip + assert tier=1, tier_priority=10, circuit_config->>'cooldown_s'='120'")
	// Expected:
	//   row := pool.QueryRow(ctx, `SELECT tier, tier_priority, circuit_config->>'cooldown_s' FROM ai_gateway.upstreams WHERE name='gemini-stt'`)
	//   require.NoError(t, row.Scan(&tier, &prio, &cooldown))
	//   require.Equal(t, 1, tier); require.Equal(t, 10, prio); require.Equal(t, "120", cooldown)
	// Reference: PATTERNS.md line 146 (circuit_config pre-existence note).
}

// TestIntegration_Migration0029_Up_AddsGroqWhisper_TierPriority15 — D-B6′ step 5.
// groq-whisper row MUST exist with tier=1, tier_priority=15; alias
// (whisper,groq-whisper) → whisper-large-v3.
func TestIntegration_Migration0029_Up_AddsGroqWhisper_TierPriority15(t *testing.T) {
	t.Skip("OWNER: Plan 02 — implements migration 0029 Up groq-whisper row; unskip + assert tier=1, tier_priority=15 + alias target='whisper-large-v3'")
	// Expected:
	//   require.Equal(t, 1, tier); require.Equal(t, 15, prio)
	//   require.Equal(t, "whisper-large-v3", aliasTarget)
	// Reference: CONTEXT D-B8, PATTERNS.md line 153.
}

// TestIntegration_Migration0029_Up_PromotesOpenAIWhisper_TierPriority20 — D-B6′ step 6.
// openai-whisper.tier_priority MUST update from 0 (default after step 1) to 20.
func TestIntegration_Migration0029_Up_PromotesOpenAIWhisper_TierPriority20(t *testing.T) {
	t.Skip("OWNER: Plan 02 — implements migration 0029 Up openai-whisper promotion; unskip + assert openai-whisper.tier_priority=20")
	// Expected:
	//   row := pool.QueryRow(ctx, `SELECT tier_priority FROM ai_gateway.upstreams WHERE name='openai-whisper'`)
	//   require.NoError(t, row.Scan(&prio))
	//   require.Equal(t, 20, prio)
	// Reference: PATTERNS.md line 154.
}

// TestIntegration_Migration0029_Down_Symmetric — D-B6′ DOWN.
// After Up→Down: local-stt, gemini-stt, groq-whisper rows + their
// aliases MUST be DELETEd; openai-whisper.tier_priority reverts to 0.
// `tier_priority` column itself stays (additive — explicit choice).
func TestIntegration_Migration0029_Down_Symmetric(t *testing.T) {
	t.Skip("OWNER: Plan 02 — implements migration 0029 Down; unskip + assert symmetric DELETEs + openai-whisper.tier_priority=0 + column tier_priority still present")
	// Expected:
	//   require.Equal(t, 0, countLocalSTT)
	//   require.Equal(t, 0, countGeminiSTT)
	//   require.Equal(t, 0, countGroqWhisper)
	//   require.Equal(t, 0, openAIWhisperPriorityPostDown)
	//   // column tier_priority MUST still exist (additive)
	//   require.True(t, columnExists("upstreams", "tier_priority"))
	// Reference: PATTERNS.md line 156.
}

// TestIntegration_Migration0029_Roundtrip_Idempotent — round-trip invariant.
// Up→Down→Up MUST yield identical post-Up state (idempotent).
func TestIntegration_Migration0029_Roundtrip_Idempotent(t *testing.T) {
	t.Skip("OWNER: Plan 02 — implements migration 0029 idempotency check; unskip + assert second Up produces same row set as first Up")
	// Expected:
	//   Snapshot row set after first Up; run Down; run Up again; diff = empty.
	// Reference: migration_0028_test.go analog TestIntegration_Migration0028_Roundtrip.
}
