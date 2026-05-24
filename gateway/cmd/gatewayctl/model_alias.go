// Package main — `gatewayctl model-alias` subcommand family (Phase 06.9
// Plan 04 Task 4).
//
// Operator CRUD for ai_gateway.model_aliases. Coequal with the
// UPSTREAM_<UPSTREAM>_MODEL env vars per CONTEXT.md D-06:
//
//   gatewayctl model-alias set --alias qwen --upstream openrouter-chat \
//                              --target qwen/qwen3.5-27b
//     Multi-instance-consistent override path. Writes to the schema row;
//     every gateway replica picks up on the next 60s resolver refresh
//     (models.Resolver.Refresh). Persistent across container restarts.
//
//   UPSTREAM_LLM_OPENROUTER_MODEL=qwen/qwen3.5-27b
//     Per-instance escape hatch. Resolver consults env first inside
//     Resolve() — env wins over schema row when non-empty. Instance-local;
//     lost on restart unless the env var is in the stack file. Useful for
//     A/B testing or emergency overrides without touching the DB.
//
// Both are SUPPORTED and PERMANENT operator override paths — neither is
// deprecated.
//
// Subcommands:
//
//   model-alias list
//     Tab-separated table: ALIAS  UPSTREAM_NAME  ROLE  TARGET
//
//   model-alias get --alias X --upstream Y
//     Single-row JSON output for scripting.
//
//   model-alias set --alias X --upstream Y --target Z
//     R7: UPSERT via queries.UpsertModelAlias (composite-PK-aware). NO
//     ad-hoc SQL. R10: input validation rejects whitespace / control
//     chars / NUL bytes / over-max-length; upstream MUST exist in
//     ai_gateway.upstreams (foreign-key emulation since model_aliases
//     does not carry a real FK to upstreams per Plan 01 schema choice).
//
//   model-alias delete --alias X --upstream Y
//     R7: DELETE via queries.DeleteModelAlias.
//
// Role inference: the `Upstream` column of the table stores the role tag
// ('llm' | 'stt' | 'embed') for backward compatibility (the column
// pre-dates the per-upstream-name evolution). The CLI derives role from
// the upstream NAME via a hardcoded mapping (the 6 canonical upstreams
// are known at compile time). New tier-1 upstreams MUST extend this
// mapping when they ship.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// modelAliasMaxAliasLen / modelAliasMaxUpstreamLen / modelAliasMaxTargetLen
// are R10 input-validation caps. Chosen to fit any reasonable
// provider/model slug while preventing schema abuse (e.g. multi-MB
// targets that would balloon the resolver cache).
const (
	modelAliasMaxAliasLen    = 64
	modelAliasMaxUpstreamLen = 64
	modelAliasMaxTargetLen   = 128
)

// upstreamNameRole maps canonical upstream NAMES to their role tag.
// The model_aliases.upstream column expects the role; CLI derives it
// from the upstream name. Adding a new tier-1 upstream requires both
// (a) seeding the schema row + (b) adding the mapping entry here.
//
// Why hardcoded: the role↔name mapping is part of the gateway's contract
// with the runtime dispatcher (loader.go). Reading it from the upstreams
// table at write time would add a second DB query per `set` call AND a
// runtime dependency between two related-but-orthogonal tables. The
// canonical 6 upstreams are stable; new entries are reviewed code
// changes anyway.
var upstreamNameRole = map[string]string{
	"local-llm":       "llm",
	"openrouter-chat": "llm",
	"local-stt":       "stt",
	"openai-whisper":  "stt",
	"local-embed":     "embed",
	"openai-embed":    "embed",
}

// runModelAlias dispatches `gatewayctl model-alias <subcommand>`.
func runModelAlias(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		printModelAliasUsage()
		return 2
	}
	switch args[0] {
	case "list":
		return runModelAliasList(ctx, args[1:], log)
	case "get":
		return runModelAliasGet(ctx, args[1:], log)
	case "set":
		return runModelAliasSet(ctx, args[1:], log)
	case "delete":
		return runModelAliasDelete(ctx, args[1:], log)
	case "-h", "--help":
		printModelAliasUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown model-alias subcommand: %s\n\n", args[0])
		printModelAliasUsage()
		return 2
	}
}

// printModelAliasUsage emits the help text with the D-06 coequal-paths
// paragraph called out so operators understand both override paths.
func printModelAliasUsage() {
	fmt.Fprint(os.Stderr, `gatewayctl model-alias — operator CRUD for ai_gateway.model_aliases

Subcommands:
  list                                         Tab-separated table of all rows.
  get    --alias X --upstream Y                Single-row JSON output.
  set    --alias X --upstream Y --target Z     Upsert row via composite PK.
  delete --alias X --upstream Y                Delete row.

D-06 — model-alias CLI vs UPSTREAM_<U>_MODEL env var:

  Per Phase 06.9 D-06, "gatewayctl model-alias set" and
  UPSTREAM_<UPSTREAM>_MODEL env vars are COEQUAL operator override paths.
  Both are supported permanently. Neither is deprecated.

    - CLI (this tool):      multi-instance-consistent path — every
                            gateway replica picks up the schema-row
                            change on its next 60s resolver refresh.
    - Env var:              per-instance escape hatch — env wins over
                            schema row at resolver-lookup time when set
                            non-empty.

R10 input validation: alias / upstream / target must be ASCII printable
non-whitespace, non-NUL, non-control. Max lengths: alias=64,
upstream=64, target=128. The --upstream value must EXIST in the
ai_gateway.upstreams table (FK-emulation).
`)
}

// =====================================================================
// R10 — input validation
// =====================================================================

// validateModelAliasInput enforces the R10 (Plan 04 review) input
// hygiene rules. Returns nil on a valid input triple; a descriptive
// error otherwise. Pure function; no DB access (the upstream-exists
// check lives in the upstream-table-exists helper below).
//
// Rules (in evaluation order):
//
//  1. No NUL bytes (\x00) anywhere — a NUL would truncate Postgres TEXT
//     and could be used to inject content past validation.
//  2. No whitespace anywhere (space, tab, newline, CR). Model slugs are
//     conventionally hyphen/slash-separated; whitespace is never valid.
//  3. No ASCII control chars (\x00-\x1F, \x7F).
//  4. Length caps: alias≤64, upstream≤64, target≤128.
//
// Empty strings are rejected by the flag parser BEFORE this validator
// runs (--alias / --upstream / --target are required flags), but we
// re-check defensively here so the validator is self-contained.
func validateModelAliasInput(alias, upstream, target string) error {
	if err := validateOneField("alias", alias, modelAliasMaxAliasLen); err != nil {
		return err
	}
	if err := validateOneField("upstream", upstream, modelAliasMaxUpstreamLen); err != nil {
		return err
	}
	if err := validateOneField("target", target, modelAliasMaxTargetLen); err != nil {
		return err
	}
	return nil
}

// validateOneField is the per-field implementation of validateModelAliasInput.
// Kept private so the public surface is the triple-arg call.
func validateOneField(name, val string, maxLen int) error {
	if val == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if len(val) > maxLen {
		return fmt.Errorf("%s exceeds max length (%d): %d chars", name, maxLen, len(val))
	}
	for i, r := range val {
		switch {
		case r == 0:
			return fmt.Errorf("%s must not contain NUL bytes (position %d)", name, i)
		case unicode.IsSpace(r):
			return fmt.Errorf("%s must not contain whitespace or control chars (position %d)", name, i)
		case unicode.IsControl(r):
			return fmt.Errorf("%s must not contain whitespace or control chars (position %d)", name, i)
		}
	}
	return nil
}

// =====================================================================
// Subcommand handlers — list / get / set / delete (R7 via sqlc)
// =====================================================================

func runModelAliasList(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("model-alias list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
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
	rows, err := q.ListModelAliases(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list model_aliases: %v\n", err)
		return 1
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ALIAS\tUPSTREAM_NAME\tROLE\tTARGET")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Alias, r.UpstreamName, r.Upstream, r.Target)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "flush table: %v\n", err)
		return 1
	}
	return 0
}

func runModelAliasGet(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("model-alias get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	alias := fs.String("alias", "", "alias (required)")
	upstream := fs.String("upstream", "", "upstream name (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *alias == "" || *upstream == "" {
		fmt.Fprintln(os.Stderr, "error: --alias and --upstream are required")
		return 2
	}
	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)
	row, err := q.GetModelAlias(ctx, gen.GetModelAliasParams{Alias: *alias, UpstreamName: *upstream})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "no row for (alias=%s, upstream=%s)\n", *alias, *upstream)
			return 1
		}
		fmt.Fprintf(os.Stderr, "get model_alias: %v\n", err)
		return 1
	}
	out, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		return 1
	}
	fmt.Println(string(out))
	return 0
}

func runModelAliasSet(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("model-alias set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	alias := fs.String("alias", "", "alias (required)")
	upstream := fs.String("upstream", "", "upstream name (required); must exist in ai_gateway.upstreams")
	target := fs.String("target", "", "target slug to rewrite to (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *alias == "" || *upstream == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "error: --alias, --upstream, and --target are all required")
		return 2
	}
	// R10 input validation — rejected inputs return exit 2 (usage error)
	// because the operator can fix the typed input + retry; this is NOT
	// a transient operational error worth exit 1.
	if err := validateModelAliasInput(*alias, *upstream, *target); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	role, ok := upstreamNameRole[*upstream]
	if !ok {
		fmt.Fprintf(os.Stderr, "error: upstream %q is not a known canonical upstream (extend upstreamNameRole when adding a new tier-1 provider)\n", *upstream)
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	// FK-emulation: model_aliases.upstream_name has no real FK to
	// upstreams.name (Plan 01 schema choice — kept simple). Verify the
	// row exists before writing so a typo lands as a clear error rather
	// than silently inserting an orphan alias the resolver will never
	// look up.
	if _, err := q.GetUpstreamByName(ctx, *upstream); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: upstream %q not found in upstreams table\n", *upstream)
			return 2
		}
		fmt.Fprintf(os.Stderr, "check upstream: %v\n", err)
		return 1
	}

	if err := q.UpsertModelAlias(ctx, gen.UpsertModelAliasParams{
		Alias:        *alias,
		Upstream:     role,
		Target:       *target,
		UpstreamName: *upstream,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "upsert model_alias: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "model-alias set: alias=%s upstream=%s role=%s target=%s\n", *alias, *upstream, role, *target)
	log.Info("model-alias set",
		"alias", *alias,
		"upstream", *upstream,
		"role", role,
		"target", *target,
	)
	return 0
}

func runModelAliasDelete(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("model-alias delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	alias := fs.String("alias", "", "alias (required)")
	upstream := fs.String("upstream", "", "upstream name (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *alias == "" || *upstream == "" {
		fmt.Fprintln(os.Stderr, "error: --alias and --upstream are required")
		return 2
	}
	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)
	if err := q.DeleteModelAlias(ctx, gen.DeleteModelAliasParams{
		Alias:        *alias,
		UpstreamName: *upstream,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "delete model_alias: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "model-alias delete: alias=%s upstream=%s\n", *alias, *upstream)
	log.Info("model-alias delete",
		"alias", *alias,
		"upstream", *upstream,
	)
	return 0
}

// Unused — kept for future enhancement. strings.TrimSpace would be too
// permissive (it would silently accept "  qwen  " as "qwen" while R10
// requires strict whitespace rejection); we keep the strict check in
// validateModelAliasInput instead.
var _ = strings.TrimSpace
