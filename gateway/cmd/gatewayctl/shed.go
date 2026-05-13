// Package main (shed.go): `gatewayctl shed-state` and `gatewayctl shed-force`
// subcommands for the Phase 5 load-shedding subsystem (CONTEXT.md D-C5).
//
//	shed-state   — read-only diagnostic that lists gw:shed:{*} Hash mirrors
//	               and overlays any active gw:shed:force:{*} override.
//	shed-force   — operator override with bounded TTL (max 1h). Writes
//	               the gw:shed:force:{upstream} shadow key the ticker
//	               consumes on its next iteration.
//
// Both subcommands are thin wrappers over redisx.* helpers; the in-process
// FSM is authoritative — see internal/redisx/shed.go for the layered key
// layout + 2-second op timeouts.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// ttlNumericPrefixRe extracts the leading numeric portion of a duration
// string (e.g., "500" from "500ms", "1.5" from "1.5h"). Used to build an
// accurate "did you mean Ns?" hint when an operator types a sub-second
// unit suffix by mistake. We accept an optional decimal because Go's
// time.ParseDuration does. We deliberately match only the first numeric
// segment — compound durations like "1m30s" don't yield a useful hint.
var ttlNumericPrefixRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)([a-zµ]+)$`)

// loadAndRedis is the shed-subcommand counterpart of loadAndPool: it
// resolves config from env and returns a connected redis client. The
// caller MUST defer rdb.Close().
func loadAndRedis(ctx context.Context, _ *slog.Logger) (*redis.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return redisx.NewClient(ctx, cfg)
}

// runShedState implements `gatewayctl shed-state [--upstream X] [--format json|table]`.
// Reads every gw:shed:{upstream} Hash from Redis via SCAN, overlays any
// active gw:shed:force:{upstream} override, and prints a table or JSON.
//
// Output columns (table mode):
//
//	UPSTREAM  STATE  SINCE_UNIX  REASON  INFLIGHT  P95_MS  VRAM_MIB  FORCE  TTL_S
//
// The "FORCE" column reads "-" when no override is active, otherwise the
// override target state ("off" or "on"). TTL_S is the remaining seconds
// on the override key (0 when no override).
func runShedState(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("shed-state", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	upstream := fs.String("upstream", "", "restrict to a single upstream (empty = all)")
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

	type row struct {
		Upstream        string `json:"upstream"`
		State           string `json:"state"`
		SinceUnix       string `json:"since_unix"`
		Reason          string `json:"reason"`
		Inflight        string `json:"inflight"`
		P95Ms           string `json:"p95_ms"`
		VramMiB         string `json:"vram_mib"`
		ForceActive     bool   `json:"force_active"`
		ForceState      string `json:"force_state,omitempty"`
		ForceTTLSeconds int64  `json:"force_ttl_seconds,omitempty"`
	}

	keys, err := redisx.AllShedStateKeys(ctx, rdb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list shed keys: %v\n", err)
		return 1
	}

	rows := make([]row, 0, len(keys))
	for _, k := range keys {
		// Strip the "gw:shed:" prefix to recover the upstream name. The
		// AllShedStateKeys helper already filters "gw:shed:force:*".
		name := strings.TrimPrefix(k, "gw:shed:")
		if *upstream != "" && name != *upstream {
			continue
		}
		m, rerr := redisx.ReadShedState(ctx, rdb, name)
		if rerr != nil {
			// Skip rows that fail to read; surface a stderr warning so
			// the operator notices a partial result.
			fmt.Fprintf(os.Stderr, "warn: read %s: %v\n", name, rerr)
			continue
		}
		forceState, forceTTL, hasForce := redisx.GetShedForce(ctx, rdb, name)
		r := row{
			Upstream:    name,
			State:       valueOrDash(m["state"]),
			SinceUnix:   valueOrDash(m["since_unix"]),
			Reason:      valueOrDash(m["reason"]),
			Inflight:    valueOrDash(m["inflight"]),
			P95Ms:       valueOrDash(m["p95_ms"]),
			VramMiB:     valueOrDash(m["vram_mib"]),
			ForceActive: hasForce,
			ForceState:  forceState,
		}
		if hasForce {
			// Round-to-nearest second so an override with <500ms
			// remaining displays "1" (not "0"). Plain integer
			// truncation (forceTTL / time.Second) made operators
			// see "TTL_S=0 but FORCE still active" for ~1s before
			// expiry — confusing and indistinguishable from a bug
			// (WR-01).
			r.ForceTTLSeconds = int64((forceTTL + time.Second/2) / time.Second)
		}
		rows = append(rows, r)
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
		fmt.Fprintln(tw, "UPSTREAM\tSTATE\tSINCE_UNIX\tREASON\tINFLIGHT\tP95_MS\tVRAM_MIB\tFORCE\tTTL_S")
		for _, r := range rows {
			force := "-"
			if r.ForceActive {
				force = r.ForceState
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
				r.Upstream, r.State, r.SinceUnix, r.Reason, r.Inflight, r.P95Ms, r.VramMiB, force, r.ForceTTLSeconds)
		}
		if err := tw.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "flush table: %v\n", err)
			return 1
		}
	}
	return 0
}

// runShedForce implements `gatewayctl shed-force {on|off|clear} --upstream X [--ttl 300s]`.
//
//	on     — set gw:shed:force:{upstream}="on"  with the requested TTL.
//	off    — set gw:shed:force:{upstream}="off" with the requested TTL.
//	clear  — DEL gw:shed:force:{upstream}.
//
// The TTL is enforced server-side by redisx.WriteShedForce: ttl <= 1h
// (threat T-05-09 mitigation: a forgotten override MUST eventually
// expire so it cannot permanently disable shedding).
func runShedForce(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl shed-force {on|off|clear} --upstream X [--ttl 300s]")
		return 2
	}
	action := args[0]
	if action != "on" && action != "off" && action != "clear" {
		fmt.Fprintf(os.Stderr, "unknown action %q (want on|off|clear)\n", action)
		return 2
	}

	fs := flag.NewFlagSet("shed-force", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	upstream := fs.String("upstream", "", "target upstream (required)")
	ttlStr := fs.String("ttl", "300s", "TTL for on/off (default 300s, max 1h; ignored for clear)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "--upstream required")
		return 2
	}

	// Parse TTL up-front for on/off so we surface bad input before
	// opening a Redis connection. Skipped for clear (no TTL needed).
	//
	// WR-06: reject sub-second resolutions explicitly. time.ParseDuration
	// accepts "300us"/"300ns"/etc, which would parse to a TTL that
	// redisx.WriteShedForce then rejects as "out of range" — a confusing
	// error for an operator who meant "300 seconds" and typed the wrong
	// suffix. Surface the unit mistake at the CLI layer instead.
	var ttl time.Duration
	if action == "on" || action == "off" {
		var err error
		ttl, err = time.ParseDuration(*ttlStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --ttl %q: %v\n", *ttlStr, err)
			return 2
		}
		if ttl > 0 && ttl < time.Second {
			// WR-FIX-02: the previous hint used `int64(ttl/time.Microsecond)`
			// which only produces the correct suggestion when the operator
			// typed a microsecond suffix. For the COMMON typo "500ms"
			// (operator meant "500s"), that math returned 500000, yielding
			// the actively misleading "did you mean 500000s?". Extract the
			// numeric prefix from the original string instead so "500ms"
			// becomes "did you mean 500s?". If the input doesn't match the
			// simple `<number><unit>` shape (e.g., compound "1m500ms"), we
			// omit the hint rather than emit a wrong one.
			trimmed := strings.TrimSpace(*ttlStr)
			if m := ttlNumericPrefixRe.FindStringSubmatch(trimmed); m != nil {
				fmt.Fprintf(os.Stderr,
					"invalid --ttl %q: must be at least 1 second (got %s — did you mean %ss?)\n",
					*ttlStr, ttl, m[1])
			} else {
				fmt.Fprintf(os.Stderr,
					"invalid --ttl %q: must be at least 1 second (got %s)\n",
					*ttlStr, ttl)
			}
			return 2
		}
	}

	rdb, err := loadAndRedis(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = rdb.Close() }()

	switch action {
	case "on", "off":
		// WriteShedForce enforces ttl in (0, 1h] — surfaced as exit 1
		// because it is a server-side policy (T-05-09), not a CLI usage
		// error. Operators can retry with a tighter TTL.
		if err := redisx.WriteShedForce(ctx, rdb, *upstream, action, ttl); err != nil {
			fmt.Fprintf(os.Stderr, "shed-force %s: %v\n", action, err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "shed-force: %s=%s (ttl %s)\n", *upstream, action, ttl)
		log.Info("shed-force set", "upstream", *upstream, "state", action, "ttl_s", int64(ttl/time.Second))
		return 0
	case "clear":
		if err := redisx.DeleteShedForce(ctx, rdb, *upstream); err != nil {
			fmt.Fprintf(os.Stderr, "shed-force clear: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "shed-force: %s cleared\n", *upstream)
		log.Info("shed-force cleared", "upstream", *upstream)
		return 0
	}
	// Unreachable: action validated at function entry.
	return 0
}

// valueOrDash returns "-" for empty string, otherwise the input. Avoids
// blank columns in table output that confuse the eye.
func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
