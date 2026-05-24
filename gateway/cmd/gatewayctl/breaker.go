// Package main — `gatewayctl breaker` subcommand family (Phase 06.9
// Plan 04 Task 3).
//
// Operator surface for deterministic breaker forcing required by Plan 06
// HUMAN-UAT scenarios S1-S3 (cannot drive tier-1 fallback by breaking
// real OpenRouter / OpenAI credentials).
//
// Subcommands:
//
//	breaker force-open  --upstream X --ttl D
//	  Writes `gw:breaker:force:X` with JSON value {state:"open",
//	  ttl_sec, set_by, set_at} and Redis EX = D. TTL is mandatory + max
//	  300s (R1 WARNING-4 — bounded expiry so a forgotten override
//	  releases naturally). Also writes an audit_log row with
//	  event_kind="breaker_force_open" so the action is auditable.
//
//	breaker force-close --upstream X
//	  DELs the Redis key + writes audit_log row event_kind=
//	  "breaker_force_close".
//
//	breaker list
//	  Reads known upstreams from Postgres (ListAllUpstreams) and the
//	  per-upstream force Redis key. Prints a tab-separated table:
//	    UPSTREAM  STATE  TTL_REMAINING  SET_BY  SET_AT
//	  STATE column reads FORCED_OPEN when a force key is present and
//	  OBSERVATION otherwise. The plain breaker mirror at
//	  gw:breaker:{name} is read opportunistically for the observation
//	  state column when present.
//
// Operator-only deployment note: gatewayctl assumes operator-only access
// (Docker exec / shell). There is NO auth boundary inside the CLI — the
// access boundary is the shell session that launches gatewayctl.
//
// Audit-write defensive default: WriteStateChange leaves DataClass="" by
// default, which violates the ai_gateway.data_class enum and silently
// drops the audit row (Phase 6.5 known tech debt). breaker.go sets
// DataClass="normal" explicitly so breaker force events actually persist.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"text/tabwriter"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// breakerForceMaxTTL is the R1 / WARNING-4 hard cap on operator-imposed
// force-override TTL. Five minutes is enough to cover a single UAT
// scenario (S1-S3 each <2min wall-clock) but short enough that a
// forgotten override releases on its own.
const breakerForceMaxTTL = 300 * time.Second

// runBreaker dispatches `gatewayctl breaker <subcommand>`. Returns the
// process exit code so main.go can `os.Exit(runBreaker(...))`.
//
// Subcommands: list | force-open | force-close.
func runBreaker(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		printBreakerUsage()
		return 2
	}
	switch args[0] {
	case "list":
		return runBreakerList(ctx, args[1:], log)
	case "force-open":
		return runBreakerForceOpen(ctx, args[1:], log)
	case "force-close":
		return runBreakerForceClose(ctx, args[1:], log)
	case "-h", "--help":
		printBreakerUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown breaker subcommand: %s\n\n", args[0])
		printBreakerUsage()
		return 2
	}
}

// printBreakerUsage emits the help text to stderr. Includes the operator-
// only deployment note per R1.
func printBreakerUsage() {
	fmt.Fprint(os.Stderr, `gatewayctl breaker — operator-driven circuit-breaker control

Subcommands:
  list                                 List known upstreams + current state (FORCED_OPEN | OBSERVATION).
  force-open  --upstream X --ttl D     Force breaker OPEN for D (max 5m). Writes audit_log row.
  force-close --upstream X             Release the forced-OPEN state. Writes audit_log row.

Flags:
  --upstream X     Upstream name (e.g. openrouter-chat, local-llm). Required for force-open/force-close.
  --ttl D          Duration (e.g. 60s, 5m). Required for force-open. Max 300s = 5min.

Notes:
  gatewayctl assumes operator-only access (Docker exec / shell). No auth
  boundary inside the CLI itself.

  Force-override Redis key: gw:breaker:force:{upstream-name}. Value JSON
  {state, ttl_sec, set_by, set_at}. Released automatically via Redis EX
  when TTL expires.
`)
}

// runBreakerForceOpen handles `breaker force-open --upstream X --ttl D`.
// Writes the Redis key + an audit_log row.
func runBreakerForceOpen(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("breaker force-open", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	upstream := fs.String("upstream", "", "upstream name (required)")
	ttlStr := fs.String("ttl", "", "duration; max 5m (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "error: --upstream is required")
		return 2
	}
	if *ttlStr == "" {
		fmt.Fprintln(os.Stderr, "error: --ttl is required (max 300s)")
		return 2
	}
	ttl, err := time.ParseDuration(*ttlStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid --ttl %q: %v\n", *ttlStr, err)
		return 2
	}
	if ttl <= 0 {
		fmt.Fprintln(os.Stderr, "error: --ttl must be positive")
		return 2
	}
	if ttl > breakerForceMaxTTL {
		fmt.Fprintf(os.Stderr, "error: --ttl must be ≤ 300s (got %s)\n", ttl)
		return 2
	}

	rdb, err := loadAndRedis(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = rdb.Close() }()

	setBy := currentOperator()
	val := breaker.ForceOverrideValue{
		State:  "open",
		TTLSec: int(ttl / time.Second),
		SetBy:  setBy,
		SetAt:  time.Now().UTC(),
	}
	buf, err := json.Marshal(val)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal force-override value: %v\n", err)
		return 1
	}
	key := breaker.ForceOverrideKey(*upstream)
	if err := rdb.Set(ctx, key, string(buf), ttl).Err(); err != nil {
		fmt.Fprintf(os.Stderr, "redis SET %s: %v\n", key, err)
		return 1
	}

	// Audit row write. Best-effort: a DB connection failure does NOT
	// undo the Redis SET (operator already cares about the Redis effect;
	// the audit lapse is recorded via log + a Prometheus counter the
	// audit writer already increments on flush failures).
	writeBreakerAudit(ctx, log, *upstream, "breaker_force_open", fmt.Sprintf("set_by=%s ttl=%s", setBy, ttl))

	fmt.Fprintf(os.Stdout, "breaker force-open: %s forced OPEN for %s (set_by=%s)\n", *upstream, ttl, setBy)
	log.Info("breaker force-open written",
		"upstream", *upstream,
		"ttl_s", int(ttl/time.Second),
		"set_by", setBy,
	)
	return 0
}

// runBreakerForceClose handles `breaker force-close --upstream X`.
// DELs the Redis key + writes an audit_log row. Idempotent — releasing a
// non-existent force is not an error (returns 0 with a note).
func runBreakerForceClose(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("breaker force-close", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	upstream := fs.String("upstream", "", "upstream name (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "error: --upstream is required")
		return 2
	}

	rdb, err := loadAndRedis(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = rdb.Close() }()

	key := breaker.ForceOverrideKey(*upstream)
	deleted, err := rdb.Del(ctx, key).Result()
	if err != nil {
		fmt.Fprintf(os.Stderr, "redis DEL %s: %v\n", key, err)
		return 1
	}

	setBy := currentOperator()
	writeBreakerAudit(ctx, log, *upstream, "breaker_force_close", fmt.Sprintf("set_by=%s deleted=%d", setBy, deleted))

	if deleted == 0 {
		fmt.Fprintf(os.Stdout, "breaker force-close: %s had no force-override (noop)\n", *upstream)
	} else {
		fmt.Fprintf(os.Stdout, "breaker force-close: %s released (set_by=%s)\n", *upstream, setBy)
	}
	log.Info("breaker force-close",
		"upstream", *upstream,
		"deleted", deleted,
		"set_by", setBy,
	)
	return 0
}

// runBreakerList prints a table of known upstreams + their current
// breaker state. State column reads FORCED_OPEN when a `gw:breaker:force:*`
// key is present (with remaining TTL); OBSERVATION otherwise. The plain
// breaker mirror at `gw:breaker:{name}` is read opportunistically for
// the observation state.
//
// Source of upstream names: Postgres `ai_gateway.upstreams.ListAllUpstreams`
// when reachable; falls back to a static seed list via
// AI_GATEWAY_BREAKER_LIST_FALLBACK_NAMES env var (CSV) for unit tests
// that don't have a Postgres pool.
func runBreakerList(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("breaker list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	rdb, err := loadAndRedis(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = rdb.Close() }()

	names := resolveUpstreamNamesForList(ctx, log)
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "no upstreams found (DB unreachable and AI_GATEWAY_BREAKER_LIST_FALLBACK_NAMES unset)")
		return 1
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "UPSTREAM\tSTATE\tTTL_REMAINING\tSET_BY\tSET_AT")
	for _, name := range names {
		state, ttlSec, setBy, setAt := lookupBreakerRowState(ctx, rdb, name)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, state, ttlSec, setBy, setAt)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "flush table: %v\n", err)
		return 1
	}
	return 0
}

// resolveUpstreamNamesForList returns the list of known upstream names.
// Primary source: Postgres `ai_gateway.upstreams.ListAllUpstreams`.
// Fallback: AI_GATEWAY_BREAKER_LIST_FALLBACK_NAMES env (CSV), used by
// unit tests that don't carry a Postgres pool.
func resolveUpstreamNamesForList(ctx context.Context, log *slog.Logger) []string {
	if fallback := os.Getenv("AI_GATEWAY_BREAKER_LIST_FALLBACK_NAMES"); fallback != "" {
		return splitCSVNonEmpty(fallback)
	}
	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		log.Warn("breaker list: cannot resolve upstream names from DB", "err", err)
		return nil
	}
	defer pool.Close()
	q := gen.New(pool)
	rows, err := q.ListAllUpstreams(ctx)
	if err != nil {
		log.Warn("breaker list: ListAllUpstreams failed", "err", err)
		return nil
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names
}

// lookupBreakerRowState returns (stateLabel, ttlRemainingStr, setBy,
// setAtStr) for one upstream by consulting:
//
//  1. Force-override Redis key (force takes precedence).
//  2. Plain breaker mirror Hash gw:breaker:{name} (best-effort).
//
// The OBSERVATION fallback is the catch-all when neither source has a
// recent state. Local in-process gobreaker state is intentionally NOT
// readable from gatewayctl — that lives inside the running gateway
// process and is reflected to Redis on every transition via
// publishTransition (breaker.go:215). The CLI sees what Redis sees.
func lookupBreakerRowState(ctx context.Context, rdb *redis.Client, name string) (state, ttl, setBy, setAt string) {
	// Force key takes precedence (R1 / WARNING-4).
	forceState, forceTTL, forceSet, err := breaker.ReadForceOverride(ctx, rdb, name)
	if err == nil && forceSet {
		var setByStr, setAtStr string
		// Re-fetch the JSON value to recover SetBy + SetAt for the
		// table — ReadForceOverride only returns state+ttl on its hot
		// path. The double-GET is fine for a `list` operator command
		// (called by humans, not on the request hot path).
		if raw, gerr := rdb.Get(ctx, breaker.ForceOverrideKey(name)).Result(); gerr == nil {
			var val breaker.ForceOverrideValue
			if json.Unmarshal([]byte(raw), &val) == nil {
				setByStr = val.SetBy
				if !val.SetAt.IsZero() {
					setAtStr = val.SetAt.UTC().Format(time.RFC3339)
				}
			}
		}
		stateLabel := "FORCED_OPEN"
		ttlStr := "-"
		if forceTTL > 0 {
			ttlStr = forceTTL.Round(time.Second).String()
		}
		if setByStr == "" {
			setByStr = "-"
		}
		if setAtStr == "" {
			setAtStr = "-"
		}
		// State string formatting: include the underlying force-state
		// (currently always "open" — kept generic for forward-compat).
		_ = forceState
		return stateLabel, ttlStr, setByStr, setAtStr
	}

	// No force. Try the plain mirror Hash for observation state.
	mirrorKey := "gw:breaker:" + name
	if vals, merr := rdb.HMGet(ctx, mirrorKey, "state", "since_unix").Result(); merr == nil && len(vals) == 2 && vals[0] != nil {
		if s, ok := vals[0].(string); ok && s != "" {
			label := "OBSERVATION_" + s // e.g. OBSERVATION_open, OBSERVATION_closed
			return label, "-", "-", "-"
		}
	}
	return "OBSERVATION", "-", "-", "-"
}

// currentOperator returns the OS user running gatewayctl, falling back
// to the value of OPERATOR env (so production deployments running in a
// Docker container with no resolvable user can set OPERATOR=pedro at
// invocation time). Returns "operator" as the last-resort default — the
// SetBy field is informational only.
func currentOperator() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("OPERATOR"); v != "" {
		return v
	}
	return "operator"
}

// writeBreakerAudit enqueues an audit_log row for a force-open / -close
// action. event_kind is one of "breaker_force_open" |
// "breaker_force_close". Reason carries the operator + TTL summary so
// the /admin/audit feed surfaces it for the table view.
//
// DataClass is explicitly set to "normal" — leaving it empty would
// violate the data_class enum (NOT NULL since migration 0019) and the
// audit batch INSERT would drop the row silently. This is the same
// Phase 6.5 tech-debt pattern documented in STATE.md; defensive default
// here keeps breaker-force events durable.
//
// On AI_GATEWAY_BREAKER_AUDIT_SKIP_ON_DB_ERR=1 (test-only env), DB
// connection failure is downgraded to a WARN log instead of a stderr
// print. Production deploys always have Postgres reachable.
func writeBreakerAudit(ctx context.Context, log *slog.Logger, upstream, eventKind, reason string) {
	skipOnErr := os.Getenv("AI_GATEWAY_BREAKER_AUDIT_SKIP_ON_DB_ERR") == "1"

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		if skipOnErr {
			log.Warn("breaker audit skipped (DB unreachable)", "event_kind", eventKind, "err", err)
			return
		}
		fmt.Fprintf(os.Stderr, "warn: breaker audit DB connect failed: %v\n", err)
		return
	}
	defer pool.Close()

	w := audit.NewWriter(pool, log)
	// audit.Writer is async; the CLI ends shortly after this call. Run
	// the writer briefly so the row makes it to disk, then cancel.
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go w.Run(runCtx)

	ev := audit.Event{
		Route:     "gatewayctl_breaker",
		Method:    eventKind,
		Upstream:  upstream,
		DataClass: "normal", // defensive default — see comment above.
		Reason:    reason,
	}
	w.WriteStateChange(eventKind, ev)

	// Wait briefly so the 1s flush ticker has time to fire before the
	// pool closes.
	select {
	case <-time.After(1500 * time.Millisecond):
	case <-runCtx.Done():
	}
	// Skip-on-err helper for tests: ensure miniredis pool path doesn't
	// hang on Redis.
	_ = redisx.BreakerEventsChannel
}

// splitCSVNonEmpty splits a comma-separated string and skips empty tokens.
// Local helper because no general csvOr equivalent is exported.
func splitCSVNonEmpty(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
