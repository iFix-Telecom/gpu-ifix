// Binary gatewayctl is the admin CLI for the gateway. Plan 02-01
// installed the dispatcher + subcommand stubs; Plan 02-03 implements
// the real migrate/tenant/key subcommands. Plan 02-09 will land the
// audit export subcommand.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

func usage() {
	fmt.Fprint(os.Stderr, `gatewayctl — ifix-ai-gateway admin CLI

Usage:
  gatewayctl <command> [flags]

Commands:
  migrate           Apply or revert Postgres migrations.
  tenant            Create tenants, set mode (24/7 or peak), set per-tenant quotas + shed limits.
  key               Create and revoke API keys.
  upstreams         List, update, enable, or disable rows in ai_gateway.upstreams.
  prices            Set / list / set-fx for ai_gateway.prices and fx_rates (hot-reload via NOTIFY).
  billing           Reconcile usage_counters cache against authoritative billing_events.
  usage             Report per-tenant billing breakdown (day granularity, json|table).
  admin-key         Create / revoke / list X-Admin-Key bcrypt credentials.
  shed-state        Phase 5: print shed FSM state for all upstreams (read-only).
  shed-force        Phase 5: force shed FSM on|off|clear via gw:shed:force:* (TTL <= 1h).
  thresholds        Phase 5: tune shed thresholds via JSONB merge on upstreams.circuit_config.
  emerg             Phase 6: emergency-pod operator surface (state, force-provision, force-destroy, lifecycles).
  primary           Phase 6.6: primary-pod operator surface (state, force-up, force-down, schedule, lifecycles).
  breaker           Phase 06.9: operator-driven circuit-breaker control (force-open, force-close, list). TTL mandatory; max 300s. Operator-only access.
  model-alias       Phase 06.9: operator CRUD for ai_gateway.model_aliases (list, get, set, delete). Coequal with UPSTREAM_<U>_MODEL env var per D-06.
  audit             Export audit-log partitions to MinIO cold storage (Plan 02-09).
  debug             Phase 11: operator-only debug surface (emit-error -> Sentry panic-path proof).

Use "gatewayctl <command> --help" for subcommand flags.

Notes:
  gatewayctl key supports create|revoke|list (Phase 11 added list).
  gatewayctl debug emit-error POSTs /admin/debug/panic; expects HTTP 500.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	log := newCLILogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	switch cmd {
	case "migrate":
		os.Exit(runMigrate(ctx, args, log))
	case "tenant":
		os.Exit(runTenant(ctx, args, log))
	case "key":
		os.Exit(runKey(ctx, args, log))
	case "upstreams":
		os.Exit(runUpstreams(ctx, args, log))
	case "prices":
		os.Exit(runPrices(ctx, args, log))
	case "billing":
		os.Exit(runBilling(ctx, args, log))
	case "usage":
		os.Exit(runUsage(ctx, args, log))
	case "admin-key":
		os.Exit(runAdminKey(ctx, args, log))
	case "shed-state":
		os.Exit(runShedState(ctx, args, log))
	case "shed-force":
		os.Exit(runShedForce(ctx, args, log))
	case "thresholds":
		os.Exit(runThresholds(ctx, args, log))
	case "emerg":
		os.Exit(runEmerg(ctx, args, log))
	case "primary":
		os.Exit(runPrimary(ctx, args, log))
	case "breaker":
		os.Exit(runBreaker(ctx, args, log))
	case "model-alias":
		os.Exit(runModelAlias(ctx, args, log))
	case "audit":
		fmt.Fprintln(os.Stderr, "gatewayctl audit: not yet implemented (Plan 02-09)")
		os.Exit(1)
	case "debug":
		os.Exit(runDebug(ctx, args, log))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

// loadAndPool is shared across migrate/tenant/key. Caller MUST defer pool.Close().
func loadAndPool(ctx context.Context, _ *slog.Logger) (config.Config, *pgxpool.Pool, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, nil, err
	}
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, pool, nil
}

func newCLILogger() *slog.Logger {
	// CLI uses text handler when stdout is a TTY, JSON otherwise. Simple
	// heuristic: env ENV=development → text; else JSON. The redactor is
	// applied so a stray --bearer or similar never leaks into admin logs.
	env := os.Getenv("ENV")
	var inner slog.Handler
	if env == "development" {
		inner = slog.NewTextHandler(os.Stdout, nil)
	} else {
		inner = slog.NewJSONHandler(os.Stdout, nil)
	}
	return slog.New(httpx.NewRedactor(inner)).With("module", "GATEWAYCTL")
}
