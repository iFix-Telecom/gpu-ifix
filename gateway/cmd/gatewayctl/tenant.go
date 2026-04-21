// Package main (tenant.go): `gatewayctl tenant` subcommand family.
//
//	create     — Phase 2 (initial tenant insert)
//	set-mode   — Phase 4 D-C1 (24/7 or peak; rejects sensitive+peak pre-DB)
//	set-quota  — Phase 4 D-D4 (partial UPDATE via sqlc.narg wrappers)
//
// Sensitive+peak defense in depth (D-C1):
//  1. set-mode rejects data_class=sensitive with --mode peak BEFORE SQL
//     issues an UPDATE. Returns exit 2 with an LGPD-specific message.
//  2. DB CHECK constraint chk_sensitive_no_peak rejects raw SQL bypass.
//  3. Gateway boot-time CountSensitivePeakInvariant fails-fast.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// runTenant dispatches `gatewayctl tenant <subcommand>`. Returns the process
// exit code.
func runTenant(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl tenant <create|set-mode|set-quota> [flags]")
		return 2
	}
	switch args[0] {
	case "create":
		return runTenantCreate(ctx, args[1:], log)
	case "set-mode":
		return runTenantSetMode(ctx, args[1:], log)
	case "set-quota":
		return runTenantSetQuota(ctx, args[1:], log)
	default:
		fmt.Fprintf(os.Stderr, "unknown tenant subcommand: %s\n", args[0])
		return 2
	}
}

// runTenantCreate is Phase 2's tenant create. Preserved verbatim — only the
// enclosing dispatcher changed.
func runTenantCreate(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("tenant create", flag.ExitOnError)
	name := fs.String("name", "", "tenant display name (required)")
	slug := fs.String("slug", "", "tenant slug, url-safe, required")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*name) == "" || strings.TrimSpace(*slug) == "" {
		fs.Usage()
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: %v\n", err)
		return 1
	}
	defer pool.Close()

	q := gen.New(pool)
	t, err := q.CreateTenant(ctx, gen.CreateTenantParams{Slug: *slug, Name: *name})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			fmt.Fprintf(fs.Output(), "error: tenant slug '%s' already exists\n", *slug)
			return 1
		}
		fmt.Fprintf(fs.Output(), "error: create tenant: %v\n", err)
		return 1
	}
	fmt.Printf("id=%s slug=%s name=%q\n", t.ID.String(), t.Slug, t.Name)
	log.Info("tenant created", "tenant_id", t.ID.String(), "slug", t.Slug)
	return 0
}

// runTenantSetMode implements
//
//	gatewayctl tenant set-mode --tenant <slug> --mode {24/7|peak}
//	                           [--window 08-22] [--tz America/Sao_Paulo]
//
// D-C1 path 1: when --mode peak is requested and the tenant row has
// data_class='sensitive', this function rejects the update BEFORE issuing
// any SQL UPDATE. The error message is explicit about LGPD policy so the
// operator understands why the operation is blocked.
//
// For --mode 24/7 the --window / --tz flags are optional and, when absent,
// the existing values on the row are preserved (peak_window_start/end are
// cleared to NULL because the tenant is no longer in peak mode).
func runTenantSetMode(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("tenant set-mode", flag.ExitOnError)
	slug := fs.String("tenant", "", "tenant slug (required)")
	mode := fs.String("mode", "", "24/7 | peak (required)")
	window := fs.String("window", "", "HH-HH (e.g. 08-22); required with --mode peak")
	tz := fs.String("tz", "", "IANA timezone (e.g. America/Sao_Paulo); unchanged if empty")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *slug == "" || *mode == "" {
		fs.Usage()
		return 2
	}
	if *mode != "24/7" && *mode != "peak" {
		fmt.Fprintf(os.Stderr, "invalid --mode %q; must be 24/7 or peak\n", *mode)
		return 2
	}
	if *mode == "peak" && strings.TrimSpace(*window) == "" {
		fmt.Fprintln(os.Stderr, "--window HH-HH is required when --mode peak")
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	// Resolve tenant row (needed to validate data_class for peak mode) AND
	// to produce a clear "not found" error for typo'd slugs (otherwise the
	// UPDATE would be a silent no-op).
	tenantRow, err := q.GetTenantBySlug(ctx, *slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "tenant %q not found\n", *slug)
			return 1
		}
		fmt.Fprintf(os.Stderr, "lookup tenant: %v\n", err)
		return 1
	}
	// GetTenantConfig is the only query that returns data_class; GetTenantBySlug
	// in admin.sql returns only (id, slug, name, created_at, updated_at).
	cfg, err := q.GetTenantConfig(ctx, tenantRow.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lookup tenant config: %v\n", err)
		return 1
	}
	if *mode == "peak" && dataClassString(cfg.DataClass) == "sensitive" {
		fmt.Fprintf(os.Stderr,
			"cannot set peak mode for sensitive tenant %q (LGPD policy: external providers blocked)\n",
			*slug)
		return 2
	}

	// Build time.Time values for pgtype.Time. pgtype.Time is encoded as
	// microseconds-since-midnight; scanning an HH:MM:SS string yields the
	// correct value for sqlc's COALESCE(... ::time) signature.
	var startT, endT pgtype.Time
	if *mode == "peak" {
		startH, endH, perr := parseWindowHours(*window)
		if perr != nil {
			fmt.Fprintln(os.Stderr, perr)
			return 2
		}
		if err := startT.Scan(fmt.Sprintf("%02d:00:00", startH)); err != nil {
			fmt.Fprintf(os.Stderr, "scan start time: %v\n", err)
			return 1
		}
		if err := endT.Scan(fmt.Sprintf("%02d:00:00", endH)); err != nil {
			fmt.Fprintf(os.Stderr, "scan end time: %v\n", err)
			return 1
		}
	}
	// If mode=24/7 we pass Valid=false for both peak_window_* — sqlc emits
	// `$3::time` which with Valid=false sets the column NULL. That matches
	// the seed state for tenants never put into peak mode.

	var tzParam pgtype.Text
	if strings.TrimSpace(*tz) != "" {
		tzParam = pgtype.Text{String: *tz, Valid: true}
	}

	if err := q.UpdateTenantMode(ctx, gen.UpdateTenantModeParams{
		Slug:             *slug,
		Mode:             *mode,
		PeakWindowStart:  startT,
		PeakWindowEnd:    endT,
		ScheduleTimezone: tzParam,
	}); err != nil {
		// DB CHECK constraint (path 2) would surface here as pgcode 23514
		// if path-1 validation above was ever bypassed; propagate verbatim.
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		return 1
	}
	log.Info("tenant mode updated", "slug", *slug, "mode", *mode)
	fmt.Printf("mode updated: slug=%s mode=%s\n", *slug, *mode)
	return 0
}

// runTenantSetQuota implements
//
//	gatewayctl tenant set-quota --tenant <slug>
//	    [--daily-tokens N] [--monthly-tokens N]
//	    [--daily-audio-minutes N] [--monthly-audio-minutes N]
//	    [--daily-embeds N] [--monthly-embeds N]
//	    [--rps N] [--rpm N]
//
// Every flag is optional — the default sentinel -1 maps to "leave unchanged"
// (sqlc.narg wrapping). Passing 0 explicitly is allowed and resets the
// quota to zero (hard block).
func runTenantSetQuota(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("tenant set-quota", flag.ExitOnError)
	slug := fs.String("tenant", "", "tenant slug (required)")
	dailyTokens := fs.Int64("daily-tokens", -1, "daily LLM token quota; -1 = unchanged")
	monthlyTokens := fs.Int64("monthly-tokens", -1, "monthly LLM token quota; -1 = unchanged")
	dailyAudio := fs.Int("daily-audio-minutes", -1, "daily STT minutes; -1 = unchanged")
	monthlyAudio := fs.Int("monthly-audio-minutes", -1, "monthly STT minutes; -1 = unchanged")
	dailyEmbeds := fs.Int("daily-embeds", -1, "daily embed requests; -1 = unchanged")
	monthlyEmbeds := fs.Int("monthly-embeds", -1, "monthly embed requests; -1 = unchanged")
	rps := fs.Int("rps", -1, "requests per second; -1 = unchanged")
	rpm := fs.Int("rpm", -1, "requests per minute; -1 = unchanged")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *slug == "" {
		fs.Usage()
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	// Verify tenant exists for a clean error; UpdateTenantQuota returns no
	// rows-affected count so a bad slug otherwise fails silently.
	if _, err := q.GetTenantBySlug(ctx, *slug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "tenant %q not found\n", *slug)
			return 1
		}
		fmt.Fprintf(os.Stderr, "lookup tenant: %v\n", err)
		return 1
	}

	params := gen.UpdateTenantQuotaParams{Slug: *slug}
	anyFlag := false
	if *dailyTokens >= 0 {
		params.DailyQuotaTokens = pgtype.Int8{Int64: *dailyTokens, Valid: true}
		anyFlag = true
	}
	if *monthlyTokens >= 0 {
		params.MonthlyQuotaTokens = pgtype.Int8{Int64: *monthlyTokens, Valid: true}
		anyFlag = true
	}
	if *dailyAudio >= 0 {
		params.DailyQuotaAudioMinutes = pgtype.Int4{Int32: int32(*dailyAudio), Valid: true}
		anyFlag = true
	}
	if *monthlyAudio >= 0 {
		params.MonthlyQuotaAudioMinutes = pgtype.Int4{Int32: int32(*monthlyAudio), Valid: true}
		anyFlag = true
	}
	if *dailyEmbeds >= 0 {
		params.DailyQuotaEmbeds = pgtype.Int4{Int32: int32(*dailyEmbeds), Valid: true}
		anyFlag = true
	}
	if *monthlyEmbeds >= 0 {
		params.MonthlyQuotaEmbeds = pgtype.Int4{Int32: int32(*monthlyEmbeds), Valid: true}
		anyFlag = true
	}
	if *rps >= 0 {
		params.RpsLimit = pgtype.Int4{Int32: int32(*rps), Valid: true}
		anyFlag = true
	}
	if *rpm >= 0 {
		params.RpmLimit = pgtype.Int4{Int32: int32(*rpm), Valid: true}
		anyFlag = true
	}
	if !anyFlag {
		fmt.Fprintln(os.Stderr, "at least one quota/limit flag required")
		return 2
	}

	if err := q.UpdateTenantQuota(ctx, params); err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		return 1
	}
	log.Info("tenant quota updated", "slug", *slug)
	fmt.Printf("quota updated: slug=%s\n", *slug)
	return 0
}

// parseWindowHours parses the `HH-HH` window flag accepted by
// `gatewayctl tenant set-mode --window`. Both hours must be in the range
// [0, 23]. The ordering is NOT validated — overnight windows (e.g. 22-08)
// are legitimate for some tenant operations.
func parseWindowHours(s string) (start, end int, err error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, fmt.Errorf("invalid --window %q; expected HH-HH (e.g. 08-22)", s)
	}
	var s1, s2 int
	n1, err1 := fmt.Sscanf(parts[0], "%d", &s1)
	n2, err2 := fmt.Sscanf(parts[1], "%d", &s2)
	if err1 != nil || err2 != nil || n1 != 1 || n2 != 1 {
		return 0, 0, fmt.Errorf("invalid --window %q; expected HH-HH (e.g. 08-22)", s)
	}
	if s1 < 0 || s1 > 23 || s2 < 0 || s2 > 23 {
		return 0, 0, fmt.Errorf("invalid --window %q; hours must be 0..23", s)
	}
	return s1, s2, nil
}

// dataClassString converts the interface{} returned by sqlc for the
// data_class pg_enum into its canonical string form ("normal"|"sensitive").
// Mirrors gateway/internal/admin/usage.go dataClassString.
func dataClassString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}
