// Package main (thresholds.go): `gatewayctl thresholds set` subcommand for
// tuning Phase 5 saturation thresholds on a per-upstream basis
// (CONTEXT.md D-A4).
//
// Writes are issued through gen.UpdateUpstreamAdmin, which fires the
// notify_upstreams_changed() trigger (migration 0009). The running
// gateway picks up the new thresholds via its hot-reload listener
// within <2s (SC-3 budget).
//
// The threshold flags map to keys inside ai_gateway.upstreams.circuit_config
// (JSONB):
//
//	--inflight    -> "shed_inflight_max"      (int)
//	--p95-ms      -> "shed_p95_ms"            (int, milliseconds)
//	--vram-mib    -> "shed_vram_used_mib"     (int64, MiB; native DCGM unit)
//	--arm-s       -> "shed_arm_seconds"       (int, seconds; >=5)
//	--recover-s   -> "shed_recover_seconds"   (int, seconds; >=5)
//
// The CLI does a JSONB merge in Go (read row -> unmarshal -> overlay ->
// marshal -> UPDATE) so existing fields like "failures" / "cooldown_s"
// set by `upstreams update --circuit-failures` are PRESERVED. Same
// pattern as runUpstreamsUpdate in upstreams.go.
//
// Range checks are applied pre-DB:
//   - inflight, p95-ms, vram-mib: rejected at 0 (always-saturated risk)
//   - vram-mib: capped at 1_000_000 MiB (= 1 PiB, operator typo guard, T-05-13)
//   - arm-s, recover-s: minimum 5s (hysteresis floor)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// thresholdFlags is the parsed state of `thresholds set`. Sentinel
// values: -1 / 0 mean "leave unchanged" depending on the field (see
// ranges above). anyDirty is true iff at least one flag was provided.
type thresholdFlags struct {
	upstream       string
	inflight       int
	p95Ms          int
	vramMiB        int64
	armSeconds     int
	recoverSeconds int
	inflightSet    bool
	p95Set         bool
	vramSet        bool
	armSet         bool
	recoverSet     bool
}

// runThresholds dispatches `gatewayctl thresholds <subcommand>`. Today
// only "set" is defined; future operations like "show" could live here.
func runThresholds(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl thresholds set --upstream X [--inflight N --p95-ms N --vram-mib N --arm-s N --recover-s N]")
		return 2
	}
	switch args[0] {
	case "set":
		return runThresholdsSet(ctx, args[1:], log)
	default:
		fmt.Fprintf(os.Stderr, "unknown thresholds subcommand: %s\n", args[0])
		return 2
	}
}

// runThresholdsSet implements `gatewayctl thresholds set`. Returns the
// process exit code.
func runThresholdsSet(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("thresholds set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	upstream := fs.String("upstream", "", "upstream name (required)")
	inflight := fs.Int("inflight", -1, "shed_inflight_max (>=1); -1 = unchanged")
	p95 := fs.Int("p95-ms", -1, "shed_p95_ms (>=1, milliseconds); -1 = unchanged")
	vramMiB := fs.Int64("vram-mib", -1, "shed_vram_used_mib (1..1_000_000, MiB); -1 = unchanged")
	armS := fs.Int("arm-s", -1, "shed_arm_seconds (>=5); -1 = unchanged")
	recoverS := fs.Int("recover-s", -1, "shed_recover_seconds (>=5); -1 = unchanged")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "--upstream required")
		return 2
	}

	tf := thresholdFlags{upstream: *upstream}

	// Per-field range validation. Each block sets tf.<field>Set=true
	// only AFTER validation passes; the post-loop anyDirty check then
	// guarantees at least one valid threshold flag was provided.
	if *inflight >= 0 {
		if *inflight < 1 {
			fmt.Fprintln(os.Stderr, "--inflight must be >= 1 (zero would always trip the FSM)")
			return 2
		}
		tf.inflight = *inflight
		tf.inflightSet = true
	}
	if *p95 >= 0 {
		if *p95 < 1 {
			fmt.Fprintln(os.Stderr, "--p95-ms must be >= 1")
			return 2
		}
		tf.p95Ms = *p95
		tf.p95Set = true
	}
	if *vramMiB >= 0 {
		if *vramMiB < 1 || *vramMiB > 1_000_000 {
			fmt.Fprintln(os.Stderr, "--vram-mib must be in [1, 1_000_000] MiB")
			return 2
		}
		tf.vramMiB = *vramMiB
		tf.vramSet = true
	}
	if *armS >= 0 {
		if *armS < 5 {
			fmt.Fprintln(os.Stderr, "--arm-s must be >= 5 (hysteresis floor; lower values cause FSM flapping)")
			return 2
		}
		tf.armSeconds = *armS
		tf.armSet = true
	}
	if *recoverS >= 0 {
		if *recoverS < 5 {
			fmt.Fprintln(os.Stderr, "--recover-s must be >= 5 (hysteresis floor; lower values cause FSM flapping)")
			return 2
		}
		tf.recoverSeconds = *recoverS
		tf.recoverSet = true
	}

	if !tf.anyDirty() {
		fmt.Fprintln(os.Stderr, "at least one threshold flag required (--inflight, --p95-ms, --vram-mib, --arm-s, --recover-s)")
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	// Read existing circuit_config so the merge preserves fields the
	// operator did NOT touch (e.g. failures, cooldown_s from the
	// breaker tuning subcommand).
	row, err := q.GetUpstreamByName(ctx, tf.upstream)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "upstream %q not found\n", tf.upstream)
			return 1
		}
		fmt.Fprintf(os.Stderr, "lookup upstream: %v\n", err)
		return 1
	}

	merged := map[string]any{}
	if len(row.CircuitConfig) > 0 {
		if err := json.Unmarshal(row.CircuitConfig, &merged); err != nil {
			fmt.Fprintf(os.Stderr, "parse existing circuit_config: %v\n", err)
			return 1
		}
	}
	if tf.inflightSet {
		merged["shed_inflight_max"] = tf.inflight
	}
	if tf.p95Set {
		merged["shed_p95_ms"] = tf.p95Ms
	}
	if tf.vramSet {
		merged["shed_vram_used_mib"] = tf.vramMiB
	}
	if tf.armSet {
		merged["shed_arm_seconds"] = tf.armSeconds
	}
	if tf.recoverSet {
		merged["shed_recover_seconds"] = tf.recoverSeconds
	}

	buf, err := json.Marshal(merged)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal circuit_config: %v\n", err)
		return 1
	}

	// UpdateUpstreamAdmin treats nil for Tier/Enabled as COALESCE
	// (preserve existing). CircuitConfig is the only field we touch.
	if err := q.UpdateUpstreamAdmin(ctx, gen.UpdateUpstreamAdminParams{
		Name:          tf.upstream,
		Tier:          pgtype.Int4{Valid: false},
		Enabled:       pgtype.Bool{Valid: false},
		CircuitConfig: buf,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "thresholds updated: %s => %s\n", tf.upstream, string(buf))
	log.Info("thresholds updated",
		"upstream", tf.upstream,
		"inflight_set", tf.inflightSet,
		"p95_set", tf.p95Set,
		"vram_set", tf.vramSet,
		"arm_set", tf.armSet,
		"recover_set", tf.recoverSet,
	)
	return 0
}

// anyDirty reports whether at least one threshold flag was provided.
// Used to enforce "no-op call is a usage error".
func (tf *thresholdFlags) anyDirty() bool {
	return tf.inflightSet || tf.p95Set || tf.vramSet || tf.armSet || tf.recoverSet
}
