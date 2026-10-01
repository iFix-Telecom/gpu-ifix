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
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// runUpstreams dispatches `gatewayctl upstreams <subcommand>`. Returns
// the process exit code so main.go can `os.Exit(runUpstreams(...))`.
//
// Subcommands:
//   - list: print all rows in ai_gateway.upstreams as a tab-separated table
//   - update: mutate tier / enabled / circuit_config / url_override for one
//     upstream by name (NOTIFY-triggering write — hot-reloads the running
//     gateway). --url sets ai_gateway.upstreams.url_override (takes precedence
//     over os.Getenv(url_env), no restart); --clear-url reverts to the env.
//   - enable / disable: shortcuts for `update --enabled=true|false`
func runUpstreams(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl upstreams list|update|enable|disable [flags]")
		fmt.Fprintln(os.Stderr, "  update --name=X [--tier=N] [--enabled=true|false] [--circuit-failures=N] [--circuit-cooldown-s=N] [--url=http(s)://host:port | --clear-url]")
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
// URL_OVERRIDE ("-" when NULL), AUTH_BEARER_ENV, LAST_PROBE_STATUS, LAST_PROBE_MS, LAST_PROBE_AT.
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
	fmt.Fprintln(tw, "NAME\tROLE\tTIER\tENABLED\tURL_ENV\tURL_OVERRIDE\tAUTH_BEARER_ENV\tLAST_PROBE_STATUS\tLAST_PROBE_MS\tLAST_PROBE_AT")
	for _, r := range rows {
		uo := "-"
		if r.UrlOverride.Valid && r.UrlOverride.String != "" {
			uo = r.UrlOverride.String
		}
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
		fmt.Fprintf(tw, "%s\t%s\t%d\t%v\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Name, r.Role, r.Tier, r.Enabled, r.UrlEnv, uo, abe, lps, lpm, lpa)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "error: flush table: %v\n", err)
		return 1
	}
	return 0
}

// upstreamsUpdateOpts is the parsed flag set of `gatewayctl upstreams update`.
type upstreamsUpdateOpts struct {
	name       string
	tier       int
	enabled    string
	ccFailures int
	ccCooldown int
	url        string
	clearURL   bool
}

// hasAdminChange reports whether any UpdateUpstreamAdmin field was requested.
func (o upstreamsUpdateOpts) hasAdminChange() bool {
	return o.tier >= 0 || o.enabled != "" || o.ccFailures > 0 || o.ccCooldown > 0
}

// hasURLChange reports whether --url or --clear-url was requested.
func (o upstreamsUpdateOpts) hasURLChange() bool {
	return o.url != "" || o.clearURL
}

// parseUpstreamsUpdateFlags parses and validates the update flags without
// touching the database. Returns (opts, 0) on success or (_, exitCode) with
// the reason already written to stderr. Pure — unit-tested without DB.
func parseUpstreamsUpdateFlags(args []string) (upstreamsUpdateOpts, int) {
	var o upstreamsUpdateOpts
	fs := flag.NewFlagSet("upstreams update", flag.ContinueOnError)
	fs.StringVar(&o.name, "name", "", "upstream name (required)")
	fs.IntVar(&o.tier, "tier", -1, "tier (0=primary, 1=fallback; -1 = leave unchanged)")
	fs.StringVar(&o.enabled, "enabled", "", "'true' or 'false'; empty = leave unchanged")
	fs.IntVar(&o.ccFailures, "circuit-failures", 0, "trip threshold; 0 = leave unchanged")
	fs.IntVar(&o.ccCooldown, "circuit-cooldown-s", 0, "cooldown seconds; 0 = leave unchanged")
	fs.StringVar(&o.url, "url", "", "set url_override (http/https URL; hot-reload, takes precedence over the url_env env var)")
	fs.BoolVar(&o.clearURL, "clear-url", false, "clear url_override (gateway falls back to the url_env env var)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0
		}
		return o, 2
	}
	if o.name == "" {
		fmt.Fprintln(os.Stderr, "--name required")
		return o, 2
	}
	if o.enabled != "" && o.enabled != "true" && o.enabled != "false" {
		fmt.Fprintf(os.Stderr, "--enabled must be 'true' or 'false' (got %q)\n", o.enabled)
		return o, 2
	}
	if o.url != "" && o.clearURL {
		fmt.Fprintln(os.Stderr, "--url and --clear-url are mutually exclusive")
		return o, 2
	}
	if o.url != "" {
		if err := upstreams.ValidateUpstreamURL(o.url); err != nil {
			fmt.Fprintf(os.Stderr, "--url invalid: %v\n", err)
			return o, 2
		}
	}
	return o, 0
}

// runUpstreamsUpdate implements `gatewayctl upstreams update --name=<NAME>`
// with optional --tier / --enabled / --circuit-failures / --circuit-cooldown-s
// flags (written via UpdateUpstreamAdmin) and --url / --clear-url (written via
// SetUpstreamURLOverride). Both writes fire the NOTIFY trigger (0009/0038).
// When only --url/--clear-url is passed, UpdateUpstreamAdmin is skipped.
func runUpstreamsUpdate(ctx context.Context, args []string, log *slog.Logger) int {
	o, code := parseUpstreamsUpdateFlags(args)
	if code != 0 || o.name == "" {
		return code
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
	row, err := q.GetUpstreamByName(ctx, o.name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "upstream %q not found\n", o.name)
			return 1
		}
		fmt.Fprintf(os.Stderr, "lookup upstream: %v\n", err)
		return 1
	}

	params := gen.UpdateUpstreamAdminParams{Name: o.name}
	if o.tier >= 0 {
		params.Tier = pgtype.Int4{Int32: int32(o.tier), Valid: true}
	}
	switch o.enabled {
	case "true":
		params.Enabled = pgtype.Bool{Bool: true, Valid: true}
	case "false":
		params.Enabled = pgtype.Bool{Bool: false, Valid: true}
	}
	if o.ccFailures > 0 || o.ccCooldown > 0 {
		// Merge the new values into the existing JSONB so unrelated
		// fields are preserved (e.g. future Phase 5 saturation thresholds).
		merged := map[string]any{}
		if len(row.CircuitConfig) > 0 {
			_ = json.Unmarshal(row.CircuitConfig, &merged)
		}
		if o.ccFailures > 0 {
			merged["failures"] = o.ccFailures
		}
		if o.ccCooldown > 0 {
			merged["cooldown_s"] = o.ccCooldown
		}
		buf, err := json.Marshal(merged)
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal circuit_config: %v\n", err)
			return 1
		}
		params.CircuitConfig = buf
	}

	if o.hasAdminChange() || !o.hasURLChange() {
		if err := q.UpdateUpstreamAdmin(ctx, params); err != nil {
			fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
			return 1
		}
	}
	if o.hasURLChange() {
		uo := pgtype.Text{}
		if o.url != "" {
			uo = pgtype.Text{String: o.url, Valid: true}
		}
		if err := q.SetUpstreamURLOverride(ctx, gen.SetUpstreamURLOverrideParams{
			Name:        o.name,
			UrlOverride: uo,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "set url_override failed: %v\n", err)
			return 1
		}
		if o.url != "" {
			fmt.Fprintf(os.Stdout, "upstream %q url_override set to %s\n", o.name, o.url)
		} else {
			fmt.Fprintf(os.Stdout, "upstream %q url_override cleared\n", o.name)
		}
		log.Info("upstream url_override updated",
			"upstream", o.name, "url_override", o.url, "cleared", o.clearURL)
	}
	fmt.Fprintf(os.Stdout, "updated upstream %q\n", o.name)
	log.Info("upstream updated",
		"upstream", o.name,
		"tier", o.tier,
		"enabled", o.enabled,
		"circuit_failures", o.ccFailures,
		"circuit_cooldown_s", o.ccCooldown,
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
