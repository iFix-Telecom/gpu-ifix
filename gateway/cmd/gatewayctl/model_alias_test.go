// Package main — Phase 06.9 Plan 04 Task 4: unit tests for the
// `gatewayctl model-alias` subcommand family. R7 sqlc queries + R10
// input validation. The Postgres-roundtrip tests live in
// model_alias_integration_test.go behind the `integration` build tag.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// =====================================================================
// R10 — validateModelAliasInput (pure-function tests; no Postgres)
// =====================================================================

func TestValidateModelAliasInput_RejectsWhitespaceInAlias(t *testing.T) {
	err := validateModelAliasInput("qw en", "openrouter-chat", "qwen/qwen3.5-27b")
	if err == nil {
		t.Fatal("expected error for whitespace in alias; got nil")
	}
	if !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("error must mention whitespace; got %q", err.Error())
	}
}

func TestValidateModelAliasInput_RejectsControlCharsInTarget(t *testing.T) {
	err := validateModelAliasInput("qwen", "openrouter-chat", "qwen\nslug")
	if err == nil {
		t.Fatal("expected error for control chars in target; got nil")
	}
	if !strings.Contains(err.Error(), "control") && !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("error must mention control chars / whitespace; got %q", err.Error())
	}
}

func TestValidateModelAliasInput_RejectsNULBytesInAlias(t *testing.T) {
	err := validateModelAliasInput("qw\x00en", "openrouter-chat", "qwen/qwen3.5-27b")
	if err == nil {
		t.Fatal("expected error for NUL byte in alias; got nil")
	}
}

func TestValidateModelAliasInput_RejectsAliasOverMaxLength(t *testing.T) {
	overLong := strings.Repeat("a", 65)
	err := validateModelAliasInput(overLong, "openrouter-chat", "qwen/qwen3.5-27b")
	if err == nil {
		t.Fatal("expected error for alias > 64 chars; got nil")
	}
	if !strings.Contains(err.Error(), "exceeds max length") {
		t.Errorf("error must mention 'exceeds max length'; got %q", err.Error())
	}
}

func TestValidateModelAliasInput_RejectsTargetOverMaxLength(t *testing.T) {
	overLong := strings.Repeat("a", 129)
	err := validateModelAliasInput("qwen", "openrouter-chat", overLong)
	if err == nil {
		t.Fatal("expected error for target > 128 chars; got nil")
	}
	if !strings.Contains(err.Error(), "exceeds max length") {
		t.Errorf("error must mention 'exceeds max length'; got %q", err.Error())
	}
}

func TestValidateModelAliasInput_AcceptsValidShapes(t *testing.T) {
	cases := []struct {
		alias, upstream, target string
	}{
		{"qwen", "openrouter-chat", "qwen/qwen3.5-27b"},
		{"bge-m3", "openai-embed", "text-embedding-3-small"},
		{"whisper", "openai-whisper", "whisper-1"},
		// Boundary: exactly at max length.
		{strings.Repeat("a", 64), "openrouter-chat", strings.Repeat("b", 128)},
	}
	for _, c := range cases {
		if err := validateModelAliasInput(c.alias, c.upstream, c.target); err != nil {
			t.Errorf("validateModelAliasInput(%q,%q,%q) = %v; want nil", c.alias, c.upstream, c.target, err)
		}
	}
}

// =====================================================================
// Dispatcher / usage tests (no Postgres required)
// =====================================================================

func TestModelAlias_NoArgsPrintsUsage(t *testing.T) {
	ctx := context.Background()
	log := slog.Default()
	if code := runModelAlias(ctx, []string{}, log); code == 0 {
		t.Errorf("expected non-zero exit when no subcommand; got %d", code)
	}
}

func TestModelAlias_UnknownSubcommand(t *testing.T) {
	ctx := context.Background()
	log := slog.Default()
	if code := runModelAlias(ctx, []string{"frobnicate"}, log); code == 0 {
		t.Errorf("expected non-zero exit for unknown subcommand; got %d", code)
	}
}

// Test 4 — set with missing flags returns non-zero + usage text.
func TestModelAliasSet_MissingFlagsErrors(t *testing.T) {
	ctx := context.Background()
	log := slog.Default()
	// Missing all flags.
	if code := runModelAlias(ctx, []string{"set"}, log); code != 2 {
		t.Errorf("set with no flags: want 2; got %d", code)
	}
	// Missing --target.
	if code := runModelAlias(ctx, []string{"set", "--alias", "qwen", "--upstream", "openrouter-chat"}, log); code != 2 {
		t.Errorf("set with --alias+--upstream only: want 2; got %d", code)
	}
	// Missing --upstream.
	if code := runModelAlias(ctx, []string{"set", "--alias", "qwen", "--target", "qwen/qwen3.5-27b"}, log); code != 2 {
		t.Errorf("set with --alias+--target only: want 2; got %d", code)
	}
}

// =====================================================================
// Phase 23-20 — upstreams novos (anthropic-haiku, openrouter-deepseek,
// openrouter-tts) + provider_prefs aceito em openrouter-chat E
// openrouter-deepseek. Testes puros sobre validateModelAliasSet (sem DB).
// =====================================================================

const deepseekPrefsJSON = `{"sort":"price","preferred_min_throughput":{"p90":100},"max_price":{"prompt":0.4},"quantizations":["fp8"],"data_collection":"deny"}`

func TestModelAliasSet_OpenRouterDeepseekWithPrefsAccepted(t *testing.T) {
	role, prefs, err := validateModelAliasSet("deepseek-flash", "openrouter-deepseek", "deepseek/deepseek-v4.1-flash:exacto", deepseekPrefsJSON)
	if err != nil {
		t.Fatalf("openrouter-deepseek + prefs DeepSeek: err = %v; want nil", err)
	}
	if role != "llm" {
		t.Errorf("role = %q; want llm", role)
	}
	if len(prefs) == 0 {
		t.Errorf("prefs canônicas vazias; want bytes")
	}
}

func TestModelAliasSet_AnthropicHaikuNoPrefsAccepted(t *testing.T) {
	role, prefs, err := validateModelAliasSet("qwen", "anthropic-haiku", "claude-haiku-5-5", "")
	if err != nil {
		t.Fatalf("anthropic-haiku sem prefs: err = %v; want nil", err)
	}
	if role != "llm" || prefs != nil {
		t.Errorf("role=%q prefs=%s; want llm + nil", role, prefs)
	}
}

func TestModelAliasSet_OpenRouterTTSAcceptedAsTTS(t *testing.T) {
	role, _, err := validateModelAliasSet("tts-1", "openrouter-tts", "openai/gpt-4o-mini-tts", "")
	if err != nil {
		t.Fatalf("openrouter-tts: err = %v; want nil", err)
	}
	if role != "tts" {
		t.Errorf("role = %q; want tts", role)
	}
}

func TestModelAliasSet_PrefsOnAnthropicHaikuRejected(t *testing.T) {
	_, _, err := validateModelAliasSet("qwen", "anthropic-haiku", "claude-haiku-5-5", `{"zdr":true}`)
	if err == nil {
		t.Fatal("prefs em anthropic-haiku: want erro")
	}
	msg := err.Error()
	if !strings.Contains(msg, "openrouter-chat") || !strings.Contains(msg, "openrouter-deepseek") {
		t.Errorf("mensagem deve listar openrouter-chat e openrouter-deepseek; got %q", msg)
	}
	// Superfície CLI: exit 2 antes de tocar o DB.
	code := runModelAlias(context.Background(), []string{"set", "--alias", "qwen", "--upstream", "anthropic-haiku", "--target", "claude-haiku-5-5", "--provider-prefs", `{"zdr":true}`}, slog.Default())
	if code != 2 {
		t.Errorf("CLI exit = %d; want 2", code)
	}
}

func TestModelAliasSet_OpenRouterChatPrefsStillAccepted(t *testing.T) {
	if _, _, err := validateModelAliasSet("qwen", "openrouter-chat", "deepseek/deepseek-v4-flash", `{"order":["novita"]}`); err != nil {
		t.Errorf("openrouter-chat + prefs: err = %v; want nil (regressão)", err)
	}
}

func TestModelAliasSet_UnknownUpstreamStillExit2(t *testing.T) {
	if _, _, err := validateModelAliasSet("qwen", "ghost-upstream", "x", ""); err == nil {
		t.Error("upstream desconhecido: want erro")
	}
	code := runModelAlias(context.Background(), []string{"set", "--alias", "qwen", "--upstream", "ghost-upstream", "--target", "x"}, slog.Default())
	if code != 2 {
		t.Errorf("CLI exit = %d; want 2", code)
	}
}

func TestModelAliasSet_ProviderPrefsUpstreams_ParityFixture(t *testing.T) {
	raw, err := os.ReadFile("../../internal/admin/testdata/provider_prefs_upstreams.json")
	if err != nil {
		t.Fatalf("ler fixture: %v", err)
	}
	var fx struct {
		Upstreams []string `json:"provider_prefs_upstreams"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fx.Upstreams) != len(providerPrefsUpstreams) {
		t.Fatalf("gatewayctl providerPrefsUpstreams tem %d itens, fixture %d (%v)", len(providerPrefsUpstreams), len(fx.Upstreams), fx.Upstreams)
	}
	for _, u := range fx.Upstreams {
		if !providerPrefsUpstreams[u] {
			t.Errorf("fixture lista %q mas gatewayctl não aceita prefs nele", u)
		}
	}
}
