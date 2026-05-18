// Package main (primary.go): `gatewayctl primary` subcommand family for
// the Phase 6.6 primary-pod auto-provisioning subsystem (06.6-CONTEXT.md
// D-08). Five FUNCTIONAL subcommands — espelhando emerg.go pattern com 1
// novo `schedule` subcommand sem analog em emerg (per reviews consensus
// action #8: surface PROVISION_LEAD_SECONDS for operator pre-flight verify):
//
//	primary state
//	    Read-only diagnostic. Reads gw:primary:state Hash from Redis
//	    (redisx.PrimaryStateKey) and renders as a 2-column table.
//
//	primary force-up [--reason "..."]
//	    PUBLISHES PrimaryEvent{Type:"force_up_request"} to
//	    gw:primary:events. The REAL CONSUMER (per reviews consensus
//	    action #3) is primary.Reconciler.handleForceUpRequest landed
//	    in Plan 06.6-06a. gatewayctl is a CLIENT — leader-only
//	    filtering happens in the reconciler.
//
//	primary force-down
//	    PUBLISHES PrimaryEvent{Type:"force_down_request"} to
//	    gw:primary:events. REAL CONSUMER = primary.Reconciler.
//	    handleForceDownRequest (Plan 06.6-06a). No-op when no live
//	    lifecycle — handler logs Warn and returns.
//
//	primary schedule
//	    Pure-function operator pre-flight: calls primary.ParseScheduleEnv
//	    against the live env and prints every resolved field INCLUDING
//	    ProvisionLeadSeconds (the pre-warm offset from reviews #8),
//	    the next-transition timestamp, and whether the pod SHOULD be
//	    provisioned RIGHT NOW. No Redis or Postgres round-trip.
//
//	primary lifecycles [--since 7d] [--limit 50] [--format table|json]
//	    Queries ai_gateway.primary_lifecycles via the
//	    ListPrimaryLifecycles sqlc query and renders as tabwriter or
//	    JSON. The --since flag accepts Go-standard durations PLUS the
//	    operator-friendly "Nd" suffix via parseDurationDays (shared
//	    with emerg lifecycles).
//
// Wave 0 orthogonality: every operation in this file is at the
// PROVISIONING LAYER — publish events / read mirror state / parse
// schedule env / query Postgres. Whether the pod internally runs
// supervisord (Wave 0 LOCKED) or DinD (rejected, pre-Wave-0) is
// invisible from the CLI's perspective.
//
// reviews consensus action #3 closure: force_up_request and
// force_down_request events have REAL CONSUMERS in Plan 06.6-06a — no
// orphan publishes.
//
// reviews consensus action #8 closure: runPrimarySchedule surfaces
// ProvisionLeadSeconds so the operator can verify the pre-warm offset
// matches the 25–30min cold-start reality of the upstream image.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// runPrimary dispatches `gatewayctl primary <subcommand>` to the
// appropriate runPrimary* handler. Returns the desired process exit
// code so main.go can `os.Exit(runPrimary(...))`. Returns 2 on usage
// errors (no subcommand, unknown subcommand) — convention shared with
// runEmerg / runShedForce.
func runPrimary(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr,
			"Usage: gatewayctl primary state|force-up|force-down|schedule|lifecycles [flags]")
		return 2
	}
	switch args[0] {
	case "state":
		return runPrimaryState(ctx, args[1:], log)
	case "force-up":
		return runPrimaryForceUp(ctx, args[1:], log)
	case "force-down":
		return runPrimaryForceDown(ctx, args[1:], log)
	case "schedule":
		return runPrimarySchedule(ctx, args[1:], log)
	case "lifecycles":
		return runPrimaryLifecycles(ctx, args[1:], log)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		return 2
	}
}

// --- state subcommand --------------------------------------------------------

// runPrimaryState parses flags, opens a Redis connection via loadAndRedis,
// and delegates to runPrimaryStateWithRedis. Split for testability:
// unit tests call runPrimaryStateWithRedis directly with a miniredis
// client. Mirror of runEmergState.
func runPrimaryState(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("primary state", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "table", "output format: table | json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(os.Stderr, "unknown format %q (want table|json)\n", *format)
		return 2
	}
	rdb, err := loadAndRedis(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = rdb.Close() }()
	return runPrimaryStateWithRedis(ctx, rdb, args, log)
}

// runPrimaryStateWithRedis is the testable inner body of `gatewayctl
// primary state`. Reads gw:primary:state Hash, renders JSON or
// tabwriter. Mirrors runEmergStateWithRedis.
func runPrimaryStateWithRedis(ctx context.Context, rdb *redis.Client, args []string, _ *slog.Logger) int {
	fs := flag.NewFlagSet("primary state", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "table", "output format: table | json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(os.Stderr, "unknown format %q (want table|json)\n", *format)
		return 2
	}

	m, err := rdb.HGetAll(ctx, redisx.PrimaryStateKey()).Result()
	if err != nil {
		fmt.Fprintf(os.Stderr, "HGetAll %s: %v\n", redisx.PrimaryStateKey(), err)
		return 1
	}
	// HGetAll returns a non-nil empty map when the key is missing — we
	// emit `{}` (json) or "(no state)" (table) so the operator sees the
	// reconciler has not yet mirrored a state.
	if *format == "json" {
		var (
			out  []byte
			mErr error
		)
		if len(m) == 0 {
			out, mErr = json.Marshal(m)
		} else {
			out, mErr = json.MarshalIndent(m, "", "  ")
		}
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "marshal json: %v\n", mErr)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	// Table: fixed 2-column KEY/VALUE layout.
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tVALUE")
	if len(m) == 0 {
		fmt.Fprintln(tw, "(no state mirrored — reconciler may be in ASLEEP)\t-")
	} else {
		// Render in canonical key order so test output is deterministic.
		for _, k := range []string{"state", "lifecycle_id", "pod_url", "pod_instance_id", "entered_at"} {
			if v, ok := m[k]; ok {
				fmt.Fprintf(tw, "%s\t%s\n", k, v)
			}
		}
		// Any extra keys (forward-compat) print after the canonical set.
		for k, v := range m {
			switch k {
			case "state", "lifecycle_id", "pod_url", "pod_instance_id", "entered_at":
				continue
			default:
				fmt.Fprintf(tw, "%s\t%s\n", k, v)
			}
		}
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "flush table: %v\n", err)
		return 1
	}
	return 0
}

// --- force-up: reviews #3 real-consumer contract ----------------------------

// runPrimaryForceUp is the env-driven wrapper that opens Redis +
// delegates to runPrimaryForceUpWithRedis.
func runPrimaryForceUp(ctx context.Context, args []string, log *slog.Logger) int {
	rdb, err := loadAndRedis(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = rdb.Close() }()
	return runPrimaryForceUpWithRedis(ctx, rdb, args, log)
}

// runPrimaryForceUpWithRedis publishes a typed PrimaryEvent of type
// "force_up_request" to gw:primary:events. The reconciler subscriber
// (Plan 06.6-06a primary.Reconciler.handleForceUpRequest — REAL
// CONSUMER per reviews consensus action #3) consumes the event
// leader-only and drives the FSM transition ASLEEP →
// PROVISIONING with trigger_reason='manual_gatewayctl'. gatewayctl
// returns immediately after a successful publish — operator inspects
// subsequent state via `gatewayctl primary state`.
//
// The published event carries:
//   - Type:      "force_up_request"
//   - Reason:    --reason flag value (default "manual_gatewayctl")
//   - SinceUnix: time.Now().Unix() at publish time
//   - ReplicaID: "gatewayctl-cli" (so audit + logs distinguish operator-
//     driven events from FSM-driven transitions)
func runPrimaryForceUpWithRedis(ctx context.Context, rdb *redis.Client, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("primary force-up", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	reason := fs.String("reason", "manual_gatewayctl", "human-readable reason recorded with the force-up request")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ev := redisx.PrimaryEvent{
		Type:      "force_up_request",
		Reason:    *reason,
		SinceUnix: time.Now().Unix(),
		ReplicaID: "gatewayctl-cli",
	}
	if err := redisx.PublishPrimaryEvent(ctx, rdb, ev); err != nil {
		fmt.Fprintf(os.Stderr, "publish force-up request: %v\n", err)
		return 1
	}
	fmt.Println("force-up request published — consumer will pick up at next leader tick.")
	fmt.Println("Run `gatewayctl primary state` to confirm the FSM transition.")
	log.Info("force-up request published",
		"reason", *reason,
		"channel", redisx.PrimaryEventsChannel)
	return 0
}

// --- force-down: reviews #3 real-consumer contract --------------------------

// runPrimaryForceDown is the env-driven wrapper that opens Redis +
// delegates to runPrimaryForceDownWithRedis.
func runPrimaryForceDown(ctx context.Context, args []string, log *slog.Logger) int {
	rdb, err := loadAndRedis(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = rdb.Close() }()
	return runPrimaryForceDownWithRedis(ctx, rdb, args, log)
}

// runPrimaryForceDownWithRedis publishes PrimaryEvent{Type:
// "force_down_request"} to gw:primary:events. Reconciler leader
// consumes (handleForceDownRequest in Plan 06.6-06a — REAL CONSUMER
// per reviews consensus action #3) and either:
//   - drives the FSM into DRAINING (or directly DESTROYING) when an
//     active lifecycle exists, or
//   - logs Warn + returns when no live lifecycle (idempotent no-op).
//
// gatewayctl does NOT pre-check the active lifecycle — that read is
// authoritative only on the leader's process memory. We just publish
// and let the leader's handler decide.
func runPrimaryForceDownWithRedis(ctx context.Context, rdb *redis.Client, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("primary force-down", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	reason := fs.String("reason", "manual_gatewayctl", "human-readable reason recorded with the force-down request")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ev := redisx.PrimaryEvent{
		Type:      "force_down_request",
		Reason:    *reason,
		SinceUnix: time.Now().Unix(),
		ReplicaID: "gatewayctl-cli",
	}
	if err := redisx.PublishPrimaryEvent(ctx, rdb, ev); err != nil {
		fmt.Fprintf(os.Stderr, "publish force-down request: %v\n", err)
		return 1
	}
	fmt.Println("force-down request published — consumer will pick up at next leader tick.")
	fmt.Println("Run `gatewayctl primary state` to confirm the FSM transition.")
	log.Info("force-down request published",
		"reason", *reason,
		"channel", redisx.PrimaryEventsChannel)
	return 0
}

// --- schedule subcommand: reviews #8 ProvisionLeadSeconds surfacing ---------

// runPrimarySchedule is the pure-function pre-flight: it loads config
// from env, parses the schedule rule, and prints every resolved field
// INCLUDING ProvisionLeadSeconds (reviews consensus action #8).
//
// No Redis or Postgres round-trip — purely cfg → ScheduleRule
// reflection. Returns 1 on ParseScheduleEnv error (invalid timezone is
// the most common cause; Pitfall #4 fail-fast applies here too).
//
// Output format (single space-aligned column, 26-char label width):
//
//	Timezone:                  America/Sao_Paulo
//	UpHour:                    8
//	DownHour:                  22
//	Days:                      [mon tue wed thu fri]
//	GraceRampDownSeconds:      300
//	ProvisionLeadSeconds:      1800           # NEW per reviews #8
//	Disabled:                  true
//	Next transition:           2026-05-18T08:00:00-03:00 (up)
//	Should be provisioned now: false
func runPrimarySchedule(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("primary schedule", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		return 1
	}
	return runPrimaryScheduleWithCfg(ctx, cfg, log)
}

// runPrimaryScheduleWithCfg is the testable inner body — accepts an
// already-built config so unit tests can drive specific field values
// without env-var dancing.
func runPrimaryScheduleWithCfg(_ context.Context, cfg config.Config, _ *slog.Logger) int {
	rule, err := primary.ParseScheduleEnv(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse schedule env: %v\n", err)
		return 1
	}
	// Render Days as a sorted []string for stable output. We iterate
	// the canonical Weekday order (Sun..Sat) and keep only enabled
	// short-name tokens — matches the input CSV format.
	dayTokens := scheduleDayTokens(rule.Days)
	fmt.Printf("Timezone:                  %s\n", rule.Timezone)
	fmt.Printf("UpHour:                    %d\n", rule.UpHour)
	fmt.Printf("DownHour:                  %d\n", rule.DownHour)
	fmt.Printf("Days:                      %v\n", dayTokens)
	fmt.Printf("GraceRampDownSeconds:      %d\n", rule.GraceRampDownS)
	// reviews consensus action #8: surface ProvisionLeadSeconds so the
	// operator can pre-flight verify the pre-warm offset against the
	// 25–30min cold-start reality of the upstream image.
	fmt.Printf("ProvisionLeadSeconds:      %d             # kicks off provisioning %d seconds before UpHour (pre-warm offset)\n",
		rule.ProvisionLeadS, rule.ProvisionLeadS)
	fmt.Printf("Disabled:                  %v\n", rule.Disabled)

	now := time.Now()
	nextT, kind := rule.NextTransition(now)
	if kind == "" {
		fmt.Println("Next transition:           (none — pathological config or all-days-disabled)")
	} else {
		fmt.Printf("Next transition:           %s (%s)\n", nextT.Format(time.RFC3339), kind)
	}
	fmt.Printf("Should be provisioned now: %v\n", rule.ShouldBeProvisioned(now))
	return 0
}

// scheduleDayTokens maps the canonical Weekday→bool map back to the
// 3-letter lowercase CSV tokens for human-readable output. Iterates in
// time.Weekday declaration order (Sun..Sat) so the output is stable
// regardless of map insertion order. NOT exported — purely a render
// helper for runPrimarySchedule output.
func scheduleDayTokens(days map[time.Weekday]bool) []string {
	// Canonical 3-letter lowercase abbreviations matching the input CSV.
	abbr := map[time.Weekday]string{
		time.Sunday:    "sun",
		time.Monday:    "mon",
		time.Tuesday:   "tue",
		time.Wednesday: "wed",
		time.Thursday:  "thu",
		time.Friday:    "fri",
		time.Saturday:  "sat",
	}
	out := make([]string, 0, 7)
	for d := time.Sunday; d <= time.Saturday; d++ {
		if days[d] {
			out = append(out, abbr[d])
		}
	}
	return out
}

// --- lifecycles subcommand ---------------------------------------------------

// runPrimaryLifecycles validates flags BEFORE opening any DB
// connection so usage errors surface quickly without a Postgres
// round-trip. Mirrors runEmergLifecycles — DB access path is
// intentionally NOT split into a *_WithPool helper because the
// straightforward query is exercised by an integration test (deferred
// to Plan 06.6-10 per the t.Skip pointer in primary_test.go).
func runPrimaryLifecycles(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("primary lifecycles", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	since := fs.String("since", "7d", "time window from now (e.g. 7d, 24h, 45m)")
	limit := fs.Int("limit", 20, "max rows to return")
	format := fs.String("format", "table", "output format: table | json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(os.Stderr, "unknown format %q (want table|json)\n", *format)
		return 2
	}
	if *limit <= 0 {
		fmt.Fprintf(os.Stderr, "--limit must be > 0 (got %d)\n", *limit)
		return 2
	}
	dur, err := parseDurationDays(*since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --since %q: %v\n", *since, err)
		return 2
	}
	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)
	startedAt := time.Now().Add(-dur)
	rows, err := q.ListPrimaryLifecycles(ctx, gen.ListPrimaryLifecyclesParams{
		StartedAt: startedAt,
		Limit:     int32(*limit),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "query lifecycles: %v\n", err)
		return 1
	}
	switch *format {
	case "json":
		out, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal json: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
	case "table":
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw,
			"ID\tSTARTED\tDRAIN\tENDED\tTRIGGER\tVAST_OFFER\tVAST_INST\tDPH\tCOST_BRL\tSHUTDOWN\tREPLICA")
		for _, r := range rows {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID,
				r.StartedAt.UTC().Format(time.RFC3339),
				timestamptzOrDash(r.DrainStartedAt),
				timestamptzOrDash(r.EndedAt),
				r.TriggerReason,
				int8OrDash(r.VastOfferID),
				int8OrDash(r.VastInstanceID),
				numericOrDash(r.AcceptedDph),
				numericOrDash(r.TotalCostBrl),
				textOrDash(r.ShutdownReason),
				textOrDash(r.LeaderReplica),
			)
		}
		if err := tw.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "flush table: %v\n", err)
			return 1
		}
	}
	return 0
}
