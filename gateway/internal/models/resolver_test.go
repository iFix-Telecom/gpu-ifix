package models

import (
	"io"
	"log/slog"
	"sync"
	"testing"
)

// newResolverFromMap builds a Resolver with a preloaded alias map without
// touching Postgres. Exercises Resolve directly.
func newResolverFromMap(m map[aliasKey]string) *Resolver {
	r := &Resolver{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		aliases: m,
	}
	return r
}

func TestResolver_ResolveKnownAlias(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "llm"}: "qwen-v1",
	})
	if got := r.Resolve("qwen", "llm"); got != "qwen-v1" {
		t.Fatalf("Resolve(qwen,llm)=%q; want qwen-v1", got)
	}
}

func TestResolver_ResolveUnknownAliasPassesThrough(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{})
	if got := r.Resolve("gpt-5", "llm"); got != "gpt-5" {
		t.Fatalf("Resolve(gpt-5,llm)=%q; want gpt-5 (pass-through)", got)
	}
}

// TestResolver_SameAliasDifferentUpstreams — Phase 06.9: aliasKey.Upstream
// is now the upstream NAME (e.g. "local-llm", "local-embed"), NOT the role.
// Fixture keys updated accordingly to reflect the new semantics; assertion
// shape unchanged.
func TestResolver_SameAliasDifferentUpstreams(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "local-llm"}:   "qwen-llm-target",
		{"qwen", "local-embed"}: "qwen-embed-target",
	})
	if got := r.Resolve("qwen", "local-llm"); got != "qwen-llm-target" {
		t.Errorf("llm target=%q", got)
	}
	if got := r.Resolve("qwen", "local-embed"); got != "qwen-embed-target" {
		t.Errorf("embed target=%q", got)
	}
}

func TestResolver_ConcurrentRefreshSafe(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "llm"}: "qwen-v1",
	})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = r.Resolve("qwen", "llm")
				}
			}
		}()
	}
	// Concurrent writer simulating Refresh swapping the map.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			select {
			case <-stop:
				return
			default:
				r.mu.Lock()
				r.aliases = map[aliasKey]string{{"qwen", "llm"}: "qwen-vX"}
				r.mu.Unlock()
			}
		}
	}()
	close(stop)
	wg.Wait()
}

// --- Phase 06.9 Plan 02 base tests (per-upstream-name lookup) ---

// TestResolver_PerUpstreamLookup — same alias, different upstreams (tier-0
// local vs tier-1 openrouter) — schema rows route to distinct targets.
func TestResolver_PerUpstreamLookup(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "local-llm"}:       "qwen",
		{"qwen", "openrouter-chat"}: "qwen/qwen3.5-27b",
	})
	// Defensive: ensure no env override interferes with this fixture-only test.
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")
	if got := r.Resolve("qwen", "local-llm"); got != "qwen" {
		t.Errorf("Resolve(qwen,local-llm)=%q; want qwen (tier-0 backfill)", got)
	}
	if got := r.Resolve("qwen", "openrouter-chat"); got != "qwen/qwen3.5-27b" {
		t.Errorf("Resolve(qwen,openrouter-chat)=%q; want qwen/qwen3.5-27b (tier-1 seed)", got)
	}
}

// TestResolver_PassThroughOnUnseededUpstream — alias passes through unchanged
// when no row matches AND no env-var mapping entry covers the upstream.
func TestResolver_PassThroughOnUnseededUpstream(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "local-llm"}:       "qwen",
		{"qwen", "openrouter-chat"}: "qwen/qwen3.5-27b",
	})
	if got := r.Resolve("qwen", "openai-experimental"); got != "qwen" {
		t.Errorf("Resolve(qwen,openai-experimental)=%q; want qwen (passthrough)", got)
	}
}

// TestResolver_PassThroughOnUnknownAlias — unknown alias passes through.
// With no env var set, env-override layer is inactive and schema-miss falls
// to the passthrough layer.
func TestResolver_PassThroughOnUnknownAlias(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "openrouter-chat"}: "qwen/qwen3.5-27b",
	})
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")
	if got := r.Resolve("unknown-alias", "openrouter-chat"); got != "unknown-alias" {
		t.Errorf("Resolve(unknown-alias,openrouter-chat)=%q; want unknown-alias", got)
	}
}

// TestResolver_AllThreeRolesPerUpstream — tier-0 + tier-1 fixture covering
// all three roles (llm/stt/embed). Six distinct (alias, upstream) keys.
func TestResolver_AllThreeRolesPerUpstream(t *testing.T) {
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	t.Setenv("UPSTREAM_EMBED_OPENAI_MODEL", "")
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "local-llm"}:         "qwen",
		{"qwen", "openrouter-chat"}:   "qwen/qwen3.5-27b",
		{"whisper", "local-stt"}:      "whisper",
		{"whisper", "openai-whisper"}: "whisper-1",
		{"bge-m3", "local-embed"}:     "bge-m3",
		{"bge-m3", "openai-embed"}:    "text-embedding-3-small",
	})
	cases := []struct {
		alias, upstream, want string
	}{
		{"qwen", "local-llm", "qwen"},
		{"qwen", "openrouter-chat", "qwen/qwen3.5-27b"},
		{"whisper", "local-stt", "whisper"},
		{"whisper", "openai-whisper", "whisper-1"},
		{"bge-m3", "local-embed", "bge-m3"},
		{"bge-m3", "openai-embed", "text-embedding-3-small"},
	}
	for _, c := range cases {
		if got := r.Resolve(c.alias, c.upstream); got != c.want {
			t.Errorf("Resolve(%q,%q)=%q; want %q", c.alias, c.upstream, got, c.want)
		}
	}
}

// --- Phase 06.9 Plan 02 BLOCKER-1 / D-06 env-override-wins tests ---

// TestResolver_EnvOverrideWinsOverSchema — when the curated env var is set,
// its value wins over the schema row at resolver-lookup time (D-06).
func TestResolver_EnvOverrideWinsOverSchema(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "openrouter-chat"}: "qwen/qwen3.5-27b",
	})
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "qwen/custom-override")
	if got := r.Resolve("qwen", "openrouter-chat"); got != "qwen/custom-override" {
		t.Errorf("Resolve(qwen,openrouter-chat)=%q; want qwen/custom-override (env wins)", got)
	}
}

// TestResolver_EnvUnsetUsesSchema — when the curated env var is unset, the
// schema row provides the target (env-override layer falls through).
func TestResolver_EnvUnsetUsesSchema(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "openrouter-chat"}: "qwen/qwen3.5-27b",
	})
	// Explicitly clear to dodge any host-level export from the test runner.
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")
	if got := r.Resolve("qwen", "openrouter-chat"); got != "qwen/qwen3.5-27b" {
		t.Errorf("Resolve(qwen,openrouter-chat)=%q; want qwen/qwen3.5-27b (schema wins when env empty)", got)
	}
}

// TestResolver_EnvOverrideWinsEvenWhenSchemaAbsent — env value wins even when
// no schema row exists (operator escape hatch for new tier-1 upstreams).
func TestResolver_EnvOverrideWinsEvenWhenSchemaAbsent(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{})
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "qwen/something")
	if got := r.Resolve("qwen", "openrouter-chat"); got != "qwen/something" {
		t.Errorf("Resolve(qwen,openrouter-chat)=%q; want qwen/something (env wins over empty schema)", got)
	}
}

// TestResolver_EmptyEnvValueTreatedAsUnset — empty-string env value does NOT
// override the schema; treated as if the var were unset.
func TestResolver_EmptyEnvValueTreatedAsUnset(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "openrouter-chat"}: "qwen/qwen3.5-27b",
	})
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")
	if got := r.Resolve("qwen", "openrouter-chat"); got != "qwen/qwen3.5-27b" {
		t.Errorf("Resolve(qwen,openrouter-chat)=%q; want qwen/qwen3.5-27b (empty env treated as unset)", got)
	}
}

// ---------------------------------------------------------------------------
// Phase 11.2 Plan 01 — Wave 0 RED stubs for upstreamEnvVarMap extension (D-B7/D-B8).
// OWNER: Plan 06 — resolver.go :56-60 adds gemini-stt + groq-whisper entries
// per PATTERNS.md lines 276-295.
// ---------------------------------------------------------------------------

// TestUpstreamEnvVarMap_GeminiSTT_MapsToGeminiModelEnv — D-B7.
// gemini-stt MUST resolve via UPSTREAM_STT_FALLBACK_1_MODEL.
func TestUpstreamEnvVarMap_GeminiSTT_MapsToGeminiModelEnv(t *testing.T) {
	t.Skip("OWNER: Plan 06 — extends upstreamEnvVarMap; unskip + assert env UPSTREAM_STT_FALLBACK_1_MODEL drives Resolve(alias, gemini-stt)")
	// Expected:
	//   t.Setenv("UPSTREAM_STT_FALLBACK_1_MODEL", "gemini-2.5-flash")
	//   require.Equal(t, "gemini-2.5-flash", r.Resolve("whisper", "gemini-stt"))
	// Reference: PATTERNS.md line 282-291.
}

// TestUpstreamEnvVarMap_GroqWhisper_MapsToGroqModelEnv — D-B8.
// groq-whisper MUST resolve via UPSTREAM_STT_FALLBACK_2_MODEL (Groq reuses
// OpenAI-compat director with different URL/bearer/model).
func TestUpstreamEnvVarMap_GroqWhisper_MapsToGroqModelEnv(t *testing.T) {
	t.Skip("OWNER: Plan 06 — extends upstreamEnvVarMap; unskip + assert env UPSTREAM_STT_FALLBACK_2_MODEL drives Resolve(alias, groq-whisper)")
	// Expected:
	//   t.Setenv("UPSTREAM_STT_FALLBACK_2_MODEL", "whisper-large-v3")
	//   require.Equal(t, "whisper-large-v3", r.Resolve("whisper", "groq-whisper"))
	// Reference: CONTEXT D-B8, PATTERNS.md line 290.
}
