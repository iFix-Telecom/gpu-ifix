// Package main: unit tests for the `gatewayctl thresholds set` subcommand.
//
// These tests cover flag validation + range checks that run BEFORE any
// DB connection is opened. End-to-end JSONB merge semantics (preserving
// existing fields like failures/cooldown_s while injecting the new
// shed_* fields) is verified by the integration suite under build tag
// `integration` in thresholds_integration_test.go.
package main

import (
	"context"
	"log/slog"
	"testing"
)

// TestRunThresholds_NoArgs asserts the top-level dispatcher rejects
// missing subcommand with exit 2.
func TestRunThresholds_NoArgs(t *testing.T) {
	ctx := context.Background()
	if code := runThresholds(ctx, []string{}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for no args, got %d", code)
	}
}

// TestRunThresholds_UnknownSubcommand asserts the dispatcher rejects
// unknown subcommands with exit 2.
func TestRunThresholds_UnknownSubcommand(t *testing.T) {
	ctx := context.Background()
	if code := runThresholds(ctx, []string{"reset"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for unknown subcommand, got %d", code)
	}
}

// TestRunThresholdsSet_MissingUpstream asserts the --upstream flag is
// required.
func TestRunThresholdsSet_MissingUpstream(t *testing.T) {
	ctx := context.Background()
	if code := runThresholdsSet(ctx, []string{"--inflight", "8"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 without --upstream, got %d", code)
	}
}

// TestRunThresholdsSet_NoFlagsIsError asserts that calling set with
// only --upstream (no threshold flags) is a usage error.
func TestRunThresholdsSet_NoFlagsIsError(t *testing.T) {
	ctx := context.Background()
	if code := runThresholdsSet(ctx, []string{"--upstream", "local-llm"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 with no threshold flags, got %d", code)
	}
}

// TestRunThresholdsSet_RejectsInvalidArmSeconds asserts --arm-s < 5 is
// rejected pre-DB (CONTEXT.md hysteresis requires ARM >= 5s to avoid
// flapping).
func TestRunThresholdsSet_RejectsInvalidArmSeconds(t *testing.T) {
	ctx := context.Background()
	if code := runThresholdsSet(ctx, []string{"--upstream", "local-llm", "--arm-s", "2"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for arm-s=2, got %d", code)
	}
}

// TestRunThresholdsSet_RejectsInvalidRecoverSeconds asserts --recover-s
// < 5 is rejected pre-DB.
func TestRunThresholdsSet_RejectsInvalidRecoverSeconds(t *testing.T) {
	ctx := context.Background()
	if code := runThresholdsSet(ctx, []string{"--upstream", "local-llm", "--recover-s", "2"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for recover-s=2, got %d", code)
	}
}

// TestRunThresholdsSet_RejectsVramOutOfRange asserts --vram-mib must be
// in [1, 1_000_000] (operator typo guard, T-05-13).
func TestRunThresholdsSet_RejectsVramOutOfRange(t *testing.T) {
	ctx := context.Background()
	if code := runThresholdsSet(ctx, []string{"--upstream", "local-llm", "--vram-mib", "0"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for vram-mib=0, got %d", code)
	}
	if code := runThresholdsSet(ctx, []string{"--upstream", "local-llm", "--vram-mib", "9999999"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for vram-mib=9999999, got %d", code)
	}
}

// TestRunThresholdsSet_RejectsInflightZero asserts --inflight must be
// >= 1 (zero would mean "always saturated" — accident-prone).
func TestRunThresholdsSet_RejectsInflightZero(t *testing.T) {
	ctx := context.Background()
	if code := runThresholdsSet(ctx, []string{"--upstream", "local-llm", "--inflight", "0"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for inflight=0, got %d", code)
	}
}

// TestRunThresholdsSet_RejectsP95Zero asserts --p95-ms must be >= 1.
func TestRunThresholdsSet_RejectsP95Zero(t *testing.T) {
	ctx := context.Background()
	if code := runThresholdsSet(ctx, []string{"--upstream", "local-llm", "--p95-ms", "0"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for p95-ms=0, got %d", code)
	}
}
