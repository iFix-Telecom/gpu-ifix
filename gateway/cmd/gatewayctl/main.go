// Binary gatewayctl is the admin CLI for the gateway. Plan 02-01
// installs the dispatcher + subcommand stubs; Plans 02-02 (migrate),
// 02-03 (tenant/key), and 02-09 (audit export) implement the real
// subcommand logic.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

func usage() {
	fmt.Fprint(os.Stderr, `gatewayctl — ifix-ai-gateway admin CLI

Usage:
  gatewayctl <command> [flags]

Commands:
  migrate           Apply or revert Postgres migrations (Plan 02-02).
  tenant            Create and list tenants (Plan 02-03).
  key               Create and revoke API keys (Plan 02-03).
  audit             Export audit-log partitions to MinIO cold storage (Plan 02-09).

Use "gatewayctl <command> --help" for subcommand flags.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	log := newCLILogger()

	switch cmd {
	case "migrate":
		runMigrate(args, log)
	case "tenant":
		runTenant(args, log)
	case "key":
		runKey(args, log)
	case "audit":
		runAudit(args, log)
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

// Subcommand stubs. Each owns a FlagSet so --help produces subcommand
// help without the top-level usage.

func runMigrate(args []string, log *slog.Logger) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	_ = fs.String("dir", "up", "up | down | status (implemented in Plan 02-02)")
	_ = fs.Parse(args)
	log.Info("gatewayctl migrate: stub — implemented in Plan 02-02")
}

func runTenant(args []string, log *slog.Logger) {
	fs := flag.NewFlagSet("tenant", flag.ExitOnError)
	_ = fs.String("name", "", "tenant display name")
	_ = fs.String("slug", "", "tenant slug (url-safe)")
	_ = fs.Parse(args)
	log.Info("gatewayctl tenant: stub — implemented in Plan 02-03")
}

func runKey(args []string, log *slog.Logger) {
	fs := flag.NewFlagSet("key", flag.ExitOnError)
	_ = fs.String("tenant", "", "tenant slug to associate with")
	_ = fs.String("data-class", "normal", "normal | sensitive (CONTEXT.md D-A4)")
	_ = fs.Parse(args)
	log.Info("gatewayctl key: stub — implemented in Plan 02-03")
}

func runAudit(args []string, log *slog.Logger) {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	_ = fs.String("month", "", "YYYY-MM partition to export (Plan 02-09)")
	_ = fs.Parse(args)
	log.Info("gatewayctl audit: stub — implemented in Plan 02-09")
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

// Silence unused-import warnings during the scaffold phase — config will
// be used by migrate/tenant/key/audit in later plans.
var _ = config.Load
