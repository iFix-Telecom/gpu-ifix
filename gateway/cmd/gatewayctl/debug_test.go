// Package main_test (debug_test.go): real unit tests for
// `gatewayctl debug emit-error`. Phase 11 Plan 04 D-18.2 + reviews LOW
// #4 (no t.Skip placeholders) — every test below drives runDebugEmitError
// against an httptest server and asserts the documented exit code.
//
// These tests do NOT replace the integration test in
// gateway/internal/admin/debug_panic_test.go — that one proves the
// handler+middleware+Recoverer chain. These prove the CLI client side
// (flag parsing, env-var fallback, HTTP shape, exit code mapping).
package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func discardCLILog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// TestRunDebug_NoSubcommand_ExitsTwo: empty args -> usage error -> 2.
func TestRunDebug_NoSubcommand_ExitsTwo(t *testing.T) {
	got := runDebug(context.Background(), nil, discardCLILog())
	if got != 2 {
		t.Errorf("exit code: want 2, got %d", got)
	}
}

// TestRunDebug_UnknownSubcommand_ExitsTwo: bogus subcommand -> 2.
func TestRunDebug_UnknownSubcommand_ExitsTwo(t *testing.T) {
	got := runDebug(context.Background(), []string{"nope"}, discardCLILog())
	if got != 2 {
		t.Errorf("exit code: want 2, got %d", got)
	}
}

// TestRunDebugEmitError_MissingFlags_ExitsTwo: --gateway / --admin-key
// missing AND env vars unset -> 2 (usage). Hermetic env restoration so
// other tests are not perturbed.
func TestRunDebugEmitError_MissingFlags_ExitsTwo(t *testing.T) {
	t.Setenv("AI_GATEWAY_URL", "")
	t.Setenv("AI_GATEWAY_ADMIN_KEY", "")
	got := runDebugEmitError(context.Background(), nil, discardCLILog())
	if got != 2 {
		t.Errorf("exit code: want 2 (missing flags), got %d", got)
	}
}

// TestRunDebugEmitError_Success_ExitsZero: server returns 500 (mirroring
// the Recoverer-emitted envelope) — CLI must exit 0.
func TestRunDebugEmitError_Success_ExitsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/debug/panic" {
			t.Errorf("path: want /admin/debug/panic, got %s", r.URL.Path)
		}
		if r.Header.Get("X-Admin-Key") == "" {
			t.Error("X-Admin-Key header missing")
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","code":"internal_error"}}`))
	}))
	defer srv.Close()

	args := []string{"--gateway", srv.URL, "--admin-key", "ifix_admin_unit_test_key"}
	got := runDebugEmitError(context.Background(), args, discardCLILog())
	if got != 0 {
		t.Errorf("exit code: want 0, got %d", got)
	}
}

// TestRunDebugEmitError_WrongStatus_ExitsOne: server returns 200 instead
// of 500 — operator must see a runtime failure (1) so a wrap-order or
// auth-misconfig regression cannot read GREEN.
func TestRunDebugEmitError_WrongStatus_ExitsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	args := []string{"--gateway", srv.URL, "--admin-key", "ifix_admin_unit_test_key"}
	got := runDebugEmitError(context.Background(), args, discardCLILog())
	if got != 1 {
		t.Errorf("exit code: want 1 (wrong status), got %d", got)
	}
}

// TestRunDebugEmitError_NetError_ExitsOne: unreachable URL -> 1.
func TestRunDebugEmitError_NetError_ExitsOne(t *testing.T) {
	// Reserved test address that always refuses connections.
	args := []string{
		"--gateway", "http://127.0.0.1:1",
		"--admin-key", "ifix_admin_unit_test_key",
	}
	got := runDebugEmitError(context.Background(), args, discardCLILog())
	if got != 1 {
		t.Errorf("exit code: want 1 (network error), got %d", got)
	}
}

// TestRunDebugEmitError_EnvVarFallback: --admin-key empty BUT
// AI_GATEWAY_ADMIN_KEY set -> exit 0 (the env path the docker-exec
// invocation pattern relies on so the raw key never appears on argv).
func TestRunDebugEmitError_EnvVarFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Admin-Key") != "ifix_admin_from_env_var" {
			t.Errorf("X-Admin-Key: want env value, got %q", r.Header.Get("X-Admin-Key"))
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("AI_GATEWAY_URL", srv.URL)
	t.Setenv("AI_GATEWAY_ADMIN_KEY", "ifix_admin_from_env_var")
	got := runDebugEmitError(context.Background(), nil, discardCLILog())
	if got != 0 {
		t.Errorf("exit code: want 0 (env fallback), got %d", got)
	}
}
