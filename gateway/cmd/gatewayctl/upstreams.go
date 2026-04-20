package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// runUpstreams dispatches `gatewayctl upstreams <subcommand>`. Returns
// the process exit code so main.go can `os.Exit(runUpstreams(...))`.
//
// Subcommands:
//   - list: print all rows in ai_gateway.upstreams as a tab-separated table
//   - update: mutate tier / enabled / circuit_config for one upstream by name
//     (NOTIFY-triggering write — hot-reloads the running gateway)
//   - enable / disable: shortcuts for `update --enabled=true|false`
func runUpstreams(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl upstreams list|update|enable|disable [flags]")
		return 2
	}
	switch args[0] {
	case "list":
		return runUpstreamsList(ctx, args[1:], log)
	case "update":
		return runUpstreamsUpdate(ctx, args[1:], log)
	case "enable":
		return runUpstreamsSetEnabled(ctx, args[1:], log, true)
	case "disable":
		return runUpstreamsSetEnabled(ctx, args[1:], log, false)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		return 2
	}
}

// runUpstreamsList implements `gatewayctl upstreams list`. Output is a
// tab-separated table with columns NAME, ROLE, TIER, ENABLED, URL_ENV,
// AUTH_BEARER_ENV, LAST_PROBE_STATUS, LAST_PROBE_MS, LAST_PROBE_AT.
func runUpstreamsList(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("upstreams list", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)
	rows, err := q.ListAllUpstreams(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tROLE\tTIER\tENABLED\tURL_ENV\tAUTH_BEARER_ENV\tLAST_PROBE_STATUS\tLAST_PROBE_MS\tLAST_PROBE_AT")
	for _, r := range rows {
		abe := "-"
		if r.AuthBearerEnv.Valid {
			abe = r.AuthBearerEnv.String
		}
		lps := "-"
		if r.LastProbeStatus.Valid {
			lps = r.LastProbeStatus.String
		}
		lpm := "-"
		if r.LastProbeMs.Valid {
			lpm = fmt.Sprintf("%d", r.LastProbeMs.Int32)
		}
		lpa := "-"
		if r.LastProbeAt.Valid {
			lpa = r.LastProbeAt.Time.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%v\t%s\t%s\t%s\t%s\t%s\n",
			r.Name, r.Role, r.Tier, r.Enabled, r.UrlEnv, abe, lps, lpm, lpa)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "error: flush table: %v\n", err)
		return 1
	}
	return 0
}

// runUpstreamsUpdate implements `gatewayctl upstreams update --name=<NAME>`
// with optional --tier / --enabled / --circuit-failures / --circuit-cooldown-s
// flags. Writes via UpdateUpstreamAdmin (which fires the NOTIFY trigger).
func runUpstreamsUpdate(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("upstreams update", flag.ExitOnError)
	name := fs.String("name", "", "upstream name (required)")
	tier := fs.Int("tier", -1, "tier (0=primary, 1=fallback; -1 = leave unchanged)")
	enabled := fs.String("enabled", "", "'true' or 'false'; empty = leave unchanged")
	ccFailures := fs.Int("circuit-failures", 0, "trip threshold; 0 = leave unchanged")
	ccCooldown := fs.Int("circuit-cooldown-s", 0, "cooldown seconds; 0 = leave unchanged")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "--name required")
		return 2
	}
	if *enabled != "" && *enabled != "true" && *enabled != "false" {
		fmt.Fprintf(os.Stderr, "--enabled must be 'true' or 'false' (got %q)\n", *enabled)
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	// Verify the row exists; surface a clean error if the name is typo'd
	// (otherwise the UPDATE is a silent no-op).
	row, err := q.GetUpstreamByName(ctx, *name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "upstream %q not found\n", *name)
			return 1
		}
		fmt.Fprintf(os.Stderr, "lookup upstream: %v\n", err)
		return 1
	}

	params := gen.UpdateUpstreamAdminParams{Name: *name}
	if *tier >= 0 {
		params.Tier = pgtype.Int4{Int32: int32(*tier), Valid: true}
	}
	switch *enabled {
	case "true":
		params.Enabled = pgtype.Bool{Bool: true, Valid: true}
	case "false":
		params.Enabled = pgtype.Bool{Bool: false, Valid: true}
	}
	if *ccFailures > 0 || *ccCooldown > 0 {
		// Merge the new values into the existing JSONB so unrelated
		// fields are preserved (e.g. future Phase 5 saturation thresholds).
		merged := map[string]any{}
		if len(row.CircuitConfig) > 0 {
			_ = json.Unmarshal(row.CircuitConfig, &merged)
		}
		if *ccFailures > 0 {
			merged["failures"] = *ccFailures
		}
		if *ccCooldown > 0 {
			merged["cooldown_s"] = *ccCooldown
		}
		buf, err := json.Marshal(merged)
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal circuit_config: %v\n", err)
			return 1
		}
		params.CircuitConfig = buf
	}

	if err := q.UpdateUpstreamAdmin(ctx, params); err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "updated upstream %q\n", *name)
	log.Info("upstream updated",
		"upstream", *name,
		"tier", *tier,
		"enabled", *enabled,
		"circuit_failures", *ccFailures,
		"circuit_cooldown_s", *ccCooldown,
	)
	return 0
}

// runUpstreamsSetEnabled implements `gatewayctl upstreams enable|disable
// --name=<NAME>` via SetUpstreamEnabled (which also fires NOTIFY).
func runUpstreamsSetEnabled(ctx context.Context, args []string, log *slog.Logger, enabled bool) int {
	op := "enable"
	if !enabled {
		op = "disable"
	}
	fs := flag.NewFlagSet("upstreams "+op, flag.ExitOnError)
	name := fs.String("name", "", "upstream name (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "--name required")
		return 2
	}
	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)
	if _, err := q.GetUpstreamByName(ctx, *name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "upstream %q not found\n", *name)
			return 1
		}
		fmt.Fprintf(os.Stderr, "lookup upstream: %v\n", err)
		return 1
	}
	if err := q.SetUpstreamEnabled(ctx, gen.SetUpstreamEnabledParams{
		Name:    *name,
		Enabled: enabled,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "set enabled failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "upstream %q enabled=%v\n", *name, enabled)
	log.Info("upstream set enabled", "upstream", *name, "enabled", enabled)
	return 0
}
