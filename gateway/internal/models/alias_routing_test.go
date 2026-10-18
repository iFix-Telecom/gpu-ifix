package models

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

func TestResolver_PrioritiesAndFallbacksFromRefresh(t *testing.T) {
	q := &fakeAliasQueries{rows: []gen.ListModelAliasesRow{
		// alias com priority em parte das linhas
		{Alias: "jev", UpstreamName: "openrouter-chat", Target: "x/a", Priority: pgtype.Int4{Int32: 1, Valid: true},
			FallbackModels: []string{"x/b", "x/c"}},
		{Alias: "jev", UpstreamName: "anthropic-haiku", Target: "claude", Priority: pgtype.Int4{Int32: 2, Valid: true}},
		{Alias: "jev", UpstreamName: "openrouter-deepseek", Target: "x/d"},
		// alias legado: nenhuma linha com priority
		{Alias: "qwen", UpstreamName: "openrouter-chat", Target: "x/q"},
		{Alias: "qwen", UpstreamName: "anthropic-haiku", Target: "claude"},
	}}
	r := &Resolver{q: q, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	p := r.PinnedPriorities("jev")
	if len(p) != 2 || p["openrouter-chat"] != 1 || p["anthropic-haiku"] != 2 {
		t.Fatalf("priorities(jev) = %v", p)
	}
	if _, ok := p["openrouter-deepseek"]; ok {
		t.Fatalf("NULL priority row must be absent from the map: %v", p)
	}
	if got := r.PinnedPriorities("qwen"); got != nil {
		t.Fatalf("alias without any priority must return nil (global order), got %v", got)
	}
	if got := r.PinnedPriorities("unknown"); got != nil {
		t.Fatalf("unknown alias = %v, want nil", got)
	}

	// cópia: mutar o retorno não afeta o cache
	p["openrouter-chat"] = 99
	if r.PinnedPriorities("jev")["openrouter-chat"] != 1 {
		t.Fatal("PinnedPriorities must return a copy")
	}

	fb := r.FallbackModels("jev", "openrouter-chat")
	if strings.Join(fb, ",") != "x/b,x/c" {
		t.Fatalf("fallbacks = %v", fb)
	}
	fb[0] = "mutated"
	if r.FallbackModels("jev", "openrouter-chat")[0] != "x/b" {
		t.Fatal("FallbackModels must return a copy")
	}
	if got := r.FallbackModels("jev", "anthropic-haiku"); got != nil {
		t.Fatalf("row without fallbacks = %v, want nil", got)
	}

	// pins continuam iguais (membros), independentemente de priority
	if got := r.PinnedUpstreams("qwen"); len(got) != 2 {
		t.Fatalf("pins(qwen) = %v", got)
	}
}

func TestValidateFallbackModels(t *testing.T) {
	ok, err := ValidateFallbackModels("deepseek/deepseek-v4-flash", []string{"google/gemini-2.5-flash", "openai/gpt-5-mini"})
	if err != nil || len(ok) != 2 {
		t.Fatalf("valid list rejected: %v %v", ok, err)
	}
	if got, err := ValidateFallbackModels("t", nil); err != nil || got != nil {
		t.Fatalf("empty list must canonicalize to nil, got %v %v", got, err)
	}
	cases := map[string][]string{
		"empty entry":  {"a/b", ""},
		"whitespace":   {"a b"},
		"control":      {"a\tb"},
		"repeats tgt":  {"t"},
		"duplicate":    {"a/b", "a/b"},
		"too many":     {"a", "b", "c", "d", "e", "f"},
		"too long":     {strings.Repeat("x", 129)},
		"nul":          {"a\x00b"},
		"newline tail": {"a/b\n"},
	}
	for name, in := range cases {
		if _, err := ValidateFallbackModels("t", in); err == nil {
			t.Errorf("%s: expected error for %q", name, in)
		}
	}
}
