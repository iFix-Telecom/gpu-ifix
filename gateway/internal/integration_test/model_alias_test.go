//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// TestIntegration_05_ModelAlias verifies the resolver reads aliases from
// Postgres, returns expected targets, and picks up new rows on Refresh.
func TestIntegration_05_ModelAlias(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	r := models.NewResolver(pool, discardLogger())
	if err := r.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	// Phase 06.9 (migration 0026): Resolve takes (alias, upstream_name) — the
	// upstream NAME ('local-llm', 'openrouter-chat', ...), not the role tag
	// ('llm','stt','embed'). Seeded rows after migration 0026: 3 tier-0
	// (local-*) + 3 tier-1 (openrouter-chat / openai-whisper / openai-embed).
	cases := []struct{ alias, upstreamName, want string }{
		// tier-0 (local pods)
		{"qwen", "local-llm", "qwen"},
		{"whisper", "local-stt", "Systran/faster-whisper-large-v3"},
		{"bge-m3", "local-embed", "BAAI/bge-m3"},
		// tier-1 (external providers; OpenRouter target updated to
		// deepseek/deepseek-v4-flash:nitro by migration 0027 — alias=qwen
		// stays per Q1 transparency decision).
		{"qwen", "openrouter-chat", "deepseek/deepseek-v4-flash:nitro"},
		{"whisper", "openai-whisper", "whisper-1"},
		{"bge-m3", "openai-embed", "text-embedding-3-small"},
		// Unknown alias → pass-through (pod decides).
		{"unknown", "local-llm", "unknown"},
	}
	for _, c := range cases {
		if got := r.Resolve(c.alias, c.upstreamName); got != c.want {
			t.Errorf("Resolve(%q,%q) got %q want %q", c.alias, c.upstreamName, got, c.want)
		}
	}

	// Add a new alias via SQL, call Refresh again, verify picked up. Phase 06.9
	// schema: model_aliases has (alias, upstream, upstream_name, target) — both
	// the role column (`upstream`) and the upstream name column (`upstream_name`,
	// NOT NULL, part of composite PK with alias) must be provided.
	if _, err := pool.Exec(ctx,
		`INSERT INTO ai_gateway.model_aliases (alias, upstream, upstream_name, target) VALUES ('gpt4o','llm','local-llm','gpt4o-v2')`); err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("gpt4o", "local-llm"); got != "gpt4o-v2" {
		t.Errorf("post-refresh Resolve(gpt4o,local-llm) got %q want gpt4o-v2", got)
	}
}
