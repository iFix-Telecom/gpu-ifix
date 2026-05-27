// Package main (debug.go): `gatewayctl debug` subcommand family. Phase
// 11 Plan 04 D-18.2 — operator surface for exercising the panic-path
// proof end-to-end against the deployed gateway.
//
// Subcommands:
//
//	debug emit-error  --gateway URL --admin-key KEY
//	  POST {gateway}/admin/debug/panic with X-Admin-Key. The handler
//	  (gateway/internal/admin/debug_panic.go) panics inside the chain;
//	  httpx.Recoverer catches the panic, forwards to Sentry via
//	  sentry.CurrentHub().Recover + sentry.Flush(500ms), then writes a
//	  sanitized 500 envelope. The CLI asserts StatusCode == 500 and
//	  returns 0 on success.
//
// Pattern D invariants (11-PATTERNS.md): runCmd(ctx, args, log) int
// signature; exit codes 0/1/2; flag.NewFlagSet; stdout = parseable
// result; stderr = log + error messages; the raw admin key is NEVER
// passed to slog (only redacted prefix in the success log).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// debugEmitErrorTimeout bounds the round-trip against the deployed
// gateway. The Recoverer flushes Sentry with a 500ms budget, so 10s is
// generous headroom for slow CI / VPN paths.
const debugEmitErrorTimeout = 10 * time.Second

// runDebug dispatches `gatewayctl debug <subcommand>`. Returns the
// process exit code so main.go can `os.Exit(runDebug(...))`.
func runDebug(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl debug <emit-error> [flags]")
		return 2
	}
	switch args[0] {
	case "emit-error":
		return runDebugEmitError(ctx, args[1:], log)
	case "-h", "--help":
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl debug <emit-error> [flags]")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown debug subcommand: %s\n", args[0])
		return 2
	}
}

// runDebugEmitError implements
//
//	gatewayctl debug emit-error --gateway URL --admin-key KEY
//
// Both flags fall back to environment variables (AI_GATEWAY_URL,
// AI_GATEWAY_ADMIN_KEY) so a docker-exec invocation can rely on the
// container env without leaking the key on the argv visible to ps(1).
// The raw admin key is sent as the X-Admin-Key header and is NEVER
// logged at any log level (Pattern A secret-once discipline).
func runDebugEmitError(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("debug emit-error", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	gwURL := fs.String("gateway", os.Getenv("AI_GATEWAY_URL"),
		"gateway base URL (env AI_GATEWAY_URL); required")
	adminKey := fs.String("admin-key", os.Getenv("AI_GATEWAY_ADMIN_KEY"),
		"admin key (env AI_GATEWAY_ADMIN_KEY); required — NEVER logged")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *gwURL == "" {
		fmt.Fprintln(os.Stderr, "error: --gateway or AI_GATEWAY_URL is required")
		return 2
	}
	if *adminKey == "" {
		fmt.Fprintln(os.Stderr, "error: --admin-key or AI_GATEWAY_ADMIN_KEY is required")
		return 2
	}

	reqCtx, cancel := context.WithTimeout(ctx, debugEmitErrorTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		*gwURL+"/admin/debug/panic", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: build request: %v\n", err)
		return 1
	}
	req.Header.Set("X-Admin-Key", *adminKey)

	client := &http.Client{Timeout: debugEmitErrorTimeout}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: POST /admin/debug/panic: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	// Recoverer ALWAYS emits 500 after a panic — any other status means
	// either the route is not mounted under Recoverer (wrap-order bug,
	// threat T-11-OPS-04) or admin auth rejected the key. Either case
	// is a runtime failure for the operator.
	if resp.StatusCode != http.StatusInternalServerError {
		fmt.Fprintf(os.Stderr,
			"error: expected 500 (panic recovered), got %d — wrap-order or auth misconfig\n",
			resp.StatusCode)
		return 1
	}

	fmt.Printf("debug emit-error sent: status=%d gateway=%s\n",
		resp.StatusCode, *gwURL)
	log.Info("debug emit-error sent",
		"gateway", *gwURL,
		"status", resp.StatusCode,
	) // NO admin key in the log record (Pattern A)
	return 0
}
