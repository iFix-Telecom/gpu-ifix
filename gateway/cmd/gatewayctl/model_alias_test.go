// Package main — Phase 06.9 Plan 04 Task 4: unit tests for the
// `gatewayctl model-alias` subcommand family. R7 sqlc queries + R10
// input validation. The Postgres-roundtrip tests live in
// model_alias_integration_test.go behind the `integration` build tag.
package main

import (
	"context"
	"log/slog"
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
