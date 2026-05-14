// Package main (emerg.go): `gatewayctl emerg` subcommand family for
// the Phase 6 emergency-pod auto-provisioning subsystem (CONTEXT.md
// D-E1, BLOCKER 2 fix 2026-05-13). Four FUNCTIONAL subcommands:
//
//	emerg state
//	    Read-only diagnostic. Reads gw:emerg:state Hash from Redis
//	    and renders as a 2-column table (default) or JSON object.
//
//	emerg force-provision [--reason "..."]
//	    PUBLISHES a typed EmergEvent{Type:"force_provision_request"}
//	    to gw:emerg:events. The reconciler subscriber (Plan 06-05
//	    Task 3) consumes the event leader-only and drives the FSM
//	    HEALTHY → EMERGENCY_PROVISIONING with audit
//	    trigger_reason='manual_force'. gatewayctl is a CLIENT — it
//	    does NOT check leadership locally; the reconciler is the
//	    authoritative leader-only filter.
//
//	emerg force-destroy
//	    PUBLISHES EmergEvent{Type:"force_destroy_request"} to
//	    gw:emerg:events. Reconciler leader consumes, calls
//	    destroyAndCloseLifecycle (Plan 06-08), and writes audit
//	    shutdown_reason='manual'. No-op when no live lifecycle
//	    exists — handler logs Warn and returns.
//
//	emerg lifecycles [--since 7d] [--limit 50] [--format table|json]
//	    Queries ai_gateway.emergency_lifecycles via the existing
//	    sqlc query and renders as tabwriter (default) or JSON. The
//	    --since flag accepts standard Go durations PLUS the
//	    operator-friendly "Nd" suffix (e.g. "7d", "30d") parsed by
//	    parseDurationDays.
//
// Inner helpers (runEmergStateWithRedis, runEmergForceProvisionWithRedis,
// runEmergForceDestroyWithRedis) take an *redis.Client directly so unit
// tests can drive against miniredis without env-var dancing. The public
// runEmerg{State,ForceProvision,ForceDestroy} wrappers call loadAndRedis
// then delegate.
//
// BLOCKER 2 (revision 2026-05-13): force-provision and force-destroy are
// FUNCTIONAL, not placeholder-logging stubs. The subscriber side (Plan
// 06-05 SubscribeEmergCommands + applyEmergCommand) was already wired
// when Plan 10 lands — this file completes the operator-facing publish
// half so the contract holds end-to-end.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// daysSuffixRe matches the operator-friendly "Nd" duration suffix —
// integer count of days. Bare-number-only on the left so "7days" and
// "7d12h" don't accidentally parse to "7d" (callers expect the parse to
// fail, not silently truncate).
var daysSuffixRe = regexp.MustCompile(`^(\d+)d$`)

// runEmerg dispatches `gatewayctl emerg <subcommand>` to the appropriate
// runEmerg* handler. Returns the desired process exit code so main.go can
// `os.Exit(runEmerg(...))`. Returns 2 on usage errors (no subcommand,
// unknown subcommand) — same convention as runUpstreams / runShedForce.
func runEmerg(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr,
			"Usage: gatewayctl emerg state|force-provision|force-destroy|lifecycles [flags]")
		return 2
	}
	switch args[0] {
	case "state":
		return runEmergState(ctx, args[1:], log)
	case "force-provision":
		return runEmergForceProvision(ctx, args[1:], log)
	case "force-destroy":
		return runEmergForceDestroy(ctx, args[1:], log)
	case "lifecycles":
		return runEmergLifecycles(ctx, args[1:], log)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		return 2
	}
}

// parseDurationDays extends time.ParseDuration with the operator-friendly
// "Nd" suffix (N integer days). All other Go-standard duration strings
// pass through unchanged. Returns an error on empty input OR any string
// the underlying ParseDuration rejects.
//
// "7d"     → 168h
// "30d"    → 720h
// "24h"    → 24h
// "45m"    → 45m
// "500ms"  → 500ms
// "7days"  → error (only "Nd" with bare number)
// "garbage"→ error
func parseDurationDays(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration string")
	}
	if m := daysSuffixRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			// Unreachable — regex matches \d+ only.
			return 0, fmt.Errorf("parse days %q: %w", s, err)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// --- state subcommand --------------------------------------------------------

// runEmergState parses flags, opens a Redis connection via loadAndRedis,
// and delegates to runEmergStateWithRedis. Split for testability:
// unit tests call runEmergStateWithRedis directly with a miniredis
// client.
func runEmergState(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("emerg state", flag.ContinueOnError)
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
	// Re-parse + delegate to the testable helper. We re-parse inside the
	// helper so unit tests can pass the raw flag args without duplicating
	// the validation logic up here.
	return runEmergStateWithRedis(ctx, rdb, args, log)
}

// runEmergStateWithRedis is the testable inner body of `gatewayctl emerg
// state`. Reads gw:emerg:state Hash, renders JSON or tabwriter.
func runEmergStateWithRedis(ctx context.Context, rdb *redis.Client, args []string, _ *slog.Logger) int {
	fs := flag.NewFlagSet("emerg state", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "table", "output format: table | json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(os.Stderr, "unknown format %q (want table|json)\n", *format)
		return 2
	}

	m, err := rdb.HGetAll(ctx, redisx.EmergStateKey()).Result()
	if err != nil {
		fmt.Fprintf(os.Stderr, "HGetAll %s: %v\n", redisx.EmergStateKey(), err)
		return 1
	}
	// HGetAll returns a non-nil empty map when the key is missing — we
	// emit `{}` (json) or "(no state)" (table) so the operator sees the
	// reconciler has not yet mirrored a state.
	if *format == "json" {
		// Use json.Marshal directly (NOT MarshalIndent for the empty case)
		// so an empty map renders as the canonical `{}` rather than `{
		// }`. For non-empty maps, fall back to MarshalIndent for human
		// readability.
		var (
			out []byte
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
	// Table: fixed 2-column KEY/VALUE layout. Sorted-key iteration is not
	// strictly required but keeps test assertions stable.
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tVALUE")
	if len(m) == 0 {
		fmt.Fprintln(tw, "(no state mirrored — reconciler may be in HEALTHY)\t-")
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

// --- force-provision: BLOCKER 2 functional contract -------------------------

// runEmergForceProvision is the env-driven wrapper that opens Redis +
// delegates to runEmergForceProvisionWithRedis.
func runEmergForceProvision(ctx context.Context, args []string, log *slog.Logger) int {
	rdb, err := loadAndRedis(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = rdb.Close() }()
	return runEmergForceProvisionWithRedis(ctx, rdb, args, log)
}

// runEmergForceProvisionWithRedis publishes a typed EmergEvent of type
// "force_provision_request" to gw:emerg:events. The reconciler
// subscriber (Plan 06-05 SubscribeEmergCommands → applyEmergCommand →
// handleForceProvision) consumes the event leader-only and drives the
// FSM transition. gatewayctl returns immediately after a successful
// publish — the operator inspects subsequent state via `gatewayctl emerg
// state`.
//
// The published event carries:
//   - Type:      "force_provision_request"
//   - Reason:    --reason flag value (default "manual")
//   - SinceUnix: time.Now().Unix() at publish time
//   - ReplicaID: "gatewayctl" (so audit + logs distinguish operator-driven
//     events from FSM-driven transitions)
func runEmergForceProvisionWithRedis(ctx context.Context, rdb *redis.Client, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("emerg force-provision", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	reason := fs.String("reason", "manual", "human-readable reason recorded with the manual_force trigger")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ev := redisx.EmergEvent{
		Type:      "force_provision_request",
		Reason:    *reason,
		SinceUnix: time.Now().Unix(),
		ReplicaID: "gatewayctl",
	}
	if err := redisx.PublishEmergEvent(ctx, rdb, ev); err != nil {
		fmt.Fprintf(os.Stderr, "publish force-provision request: %v\n", err)
		return 1
	}
	fmt.Println("force-provision request published; reconciler tick (~1s) consumes event and starts provisioning.")
	fmt.Println("Run `gatewayctl emerg state` to confirm the FSM transition.")
	log.Info("force-provision request published",
		"reason", *reason,
		"channel", redisx.EmergEventsChannel)
	return 0
}

// --- force-destroy: BLOCKER 2 functional contract ---------------------------

// runEmergForceDestroy is the env-driven wrapper that opens Redis +
// delegates to runEmergForceDestroyWithRedis.
func runEmergForceDestroy(ctx context.Context, args []string, log *slog.Logger) int {
	rdb, err := loadAndRedis(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = rdb.Close() }()
	return runEmergForceDestroyWithRedis(ctx, rdb, args, log)
}

// runEmergForceDestroyWithRedis publishes EmergEvent{Type:
// "force_destroy_request"} to gw:emerg:events. Reconciler leader
// consumes (handleForceDestroy in reconciler.go) and either:
//   - calls destroyAndCloseLifecycle when an active lifecycle exists, or
//   - logs Warn + returns when no live lifecycle (idempotent no-op).
//
// gatewayctl does NOT pre-check the active lifecycle — that read is
// authoritative only on the leader's process memory. We just publish and
// let the leader's handler decide.
func runEmergForceDestroyWithRedis(ctx context.Context, rdb *redis.Client, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("emerg force-destroy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ev := redisx.EmergEvent{
		Type:      "force_destroy_request",
		Reason:    "manual",
		SinceUnix: time.Now().Unix(),
		ReplicaID: "gatewayctl",
	}
	if err := redisx.PublishEmergEvent(ctx, rdb, ev); err != nil {
		fmt.Fprintf(os.Stderr, "publish force-destroy request: %v\n", err)
		return 1
	}
	fmt.Println("force-destroy request published; reconciler leader consumes event and tears down the live pod.")
	fmt.Println("Run `gatewayctl emerg state` to confirm the FSM transition to COOLDOWN.")
	log.Info("force-destroy request published",
		"channel", redisx.EmergEventsChannel)
	return 0
}

// --- lifecycles subcommand ---------------------------------------------------

// runEmergLifecycles validates flags BEFORE opening any DB connection so
// usage errors surface quickly without a Postgres round-trip. The DB
// access path is intentionally NOT split into a *_WithPool helper — the
// query is straightforward enough that flag-parse-only unit tests cover
// the bulk of the surface; the SQL round-trip is exercised in CI under
// the integration build tag (deferred to Plan 06 close-out).
func runEmergLifecycles(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("emerg lifecycles", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	since := fs.String("since", "7d", "time window from now (e.g. 7d, 24h, 45m)")
	limit := fs.Int("limit", 50, "max rows to return")
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
	rows, err := q.ListEmergencyLifecycles(ctx, gen.ListEmergencyLifecyclesParams{
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
			"ID\tSTARTED\tENDED\tTRIGGER\tVAST_OFFER\tVAST_INST\tDPH\tCOST_BRL\tSHUTDOWN\tREPLICA")
		for _, r := range rows {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID,
				r.StartedAt.UTC().Format(time.RFC3339),
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

// --- pgtype helpers ----------------------------------------------------------

func timestamptzOrDash(t pgtype.Timestamptz) string {
	if !t.Valid {
		return "-"
	}
	return t.Time.UTC().Format(time.RFC3339)
}

func int8OrDash(n pgtype.Int8) string {
	if !n.Valid {
		return "-"
	}
	return strconv.FormatInt(n.Int64, 10)
}

func textOrDash(t pgtype.Text) string {
	if !t.Valid || t.String == "" {
		return "-"
	}
	return t.String
}

func numericOrDash(n pgtype.Numeric) string {
	if !n.Valid {
		return "-"
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return "-"
	}
	return strconv.FormatFloat(f.Float64, 'f', 4, 64)
}
