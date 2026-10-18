// Package main — quick 261018: testes puros (sem DB) das flags --priority /
// --clear-priority / --fallback-models / --clear-fallback-models de
// `gatewayctl model-alias set`.
package main

import (
	"strings"
	"testing"
)

func TestAliasRoutingFlags_NoneGivenKeepsCurrent(t *testing.T) {
	in, err := validateAliasRoutingFlags("openrouter-chat", "deepseek/x", "", false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if in.prioritySet || in.fallbackSet {
		t.Fatalf("no flag must mean keep-current, got %+v", in)
	}
}

func TestAliasRoutingFlags_PrioritySetAndClear(t *testing.T) {
	in, err := validateAliasRoutingFlags("anthropic-haiku", "claude-haiku-5-5", "3", false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !in.prioritySet || !in.priority.Valid || in.priority.Int32 != 3 {
		t.Fatalf("priority = %+v", in.priority)
	}
	in, err = validateAliasRoutingFlags("anthropic-haiku", "claude-haiku-5-5", "-1", false, "", false)
	if err != nil || in.priority.Int32 != -1 {
		t.Fatalf("negative priority: %+v %v", in.priority, err)
	}
	in, err = validateAliasRoutingFlags("anthropic-haiku", "claude-haiku-5-5", "", true, "", false)
	if err != nil || !in.prioritySet || in.priority.Valid {
		t.Fatalf("clear priority: %+v %v", in, err)
	}
}

func TestAliasRoutingFlags_PriorityRejects(t *testing.T) {
	for name, p := range map[string]string{"text": "abc", "float": "1.5", "overflow": "9999999999"} {
		if _, err := validateAliasRoutingFlags("openrouter-chat", "t", p, false, "", false); err == nil {
			t.Errorf("%s: expected error for --priority %q", name, p)
		}
	}
	if _, err := validateAliasRoutingFlags("openrouter-chat", "t", "1", true, "", false); err == nil {
		t.Error("--priority with --clear-priority must be rejected")
	}
}

func TestAliasRoutingFlags_FallbackModelsOnOpenRouterOnly(t *testing.T) {
	for _, up := range []string{"openrouter-chat", "openrouter-deepseek"} {
		in, err := validateAliasRoutingFlags(up, "deepseek/deepseek-v4-flash", "", false, "google/gemini-2.5-flash,openai/gpt-5-mini", false)
		if err != nil {
			t.Fatalf("%s: %v", up, err)
		}
		if !in.fallbackSet || strings.Join(in.fallback, ",") != "google/gemini-2.5-flash,openai/gpt-5-mini" {
			t.Fatalf("%s: fallback = %+v", up, in)
		}
	}
	for _, up := range []string{"anthropic-haiku", "local-llm", "openai-whisper"} {
		if _, err := validateAliasRoutingFlags(up, "x", "", false, "a/b", false); err == nil {
			t.Errorf("--fallback-models on %s must be rejected", up)
		}
	}
}

func TestAliasRoutingFlags_FallbackModelsRejects(t *testing.T) {
	cases := map[string]string{
		"repeats target": "deepseek/x",
		"empty entry":    "a/b,,c/d",
		"trailing comma": "a/b,",
		"space":          "a/b, c/d",
		"duplicate":      "a/b,a/b",
	}
	for name, csv := range cases {
		if _, err := validateAliasRoutingFlags("openrouter-chat", "deepseek/x", "", false, csv, false); err == nil {
			t.Errorf("%s: expected error for %q", name, csv)
		}
	}
	if _, err := validateAliasRoutingFlags("openrouter-chat", "t", "", false, "a/b", true); err == nil {
		t.Error("--fallback-models with --clear-fallback-models must be rejected")
	}
	in, err := validateAliasRoutingFlags("openrouter-chat", "t", "", false, "", true)
	if err != nil || !in.fallbackSet || in.fallback != nil {
		t.Fatalf("clear fallback: %+v %v", in, err)
	}
}
