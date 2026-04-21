// Package main (billing.go): `gatewayctl billing` + `gatewayctl usage`
// subcommand families.
//
//	billing reconcile [--from --to] [--apply]
//	    Compare SUM(billing_events) per (tenant,day) vs usage_counters cache.
//	    Alarm on drift > 0.1%. With --apply, rewrite usage_counters from the
//	    authoritative billing_events SUM (D-D4 reconcile semantics).
//	usage report --tenant <slug|uuid> --from <ISO-date> --to <ISO-date>
//	             [--format table|json]
//	    Per-tenant billing breakdown, day granularity. Queries billing_events
//	    directly (NOT usage_counters cache) — authoritative for reports per
//	    D-D2. Format json emits the same SC-3 shape as GET /admin/usage.
//
// The reconcile implementation compares only usage_counters rows for the
// CURRENT day (usage_counters.sql exposes GetUsageCountersToday). For
// arbitrary past days, 04-08's integration suite adds a GetUsageCountersForDay
// variant; this CLI keeps the comparison to today so Wave 4 ships without
// a schema / sqlc change.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// driftThresholdPercent is the D-D4 reconcile alarm threshold. Any (tenant,
// day) pair whose |billing_events_sum - usage_counters| / billing_events_sum
// exceeds this value is reported to stderr.
const driftThresholdPercent = 0.1

// scheduleTZ is the canonical timezone for all daily-rollover maths in the
// gateway. Mirrors gateway/internal/admin/usage.go + D-A3.
const scheduleTZ = "America/Sao_Paulo"

// runBilling dispatches `gatewayctl billing <subcommand>`.
func runBilling(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl billing <reconcile> [flags]")
		return 2
	}
	switch args[0] {
	case "reconcile":
		return runBillingReconcile(ctx, args[1:], log)
	default:
		fmt.Fprintf(os.Stderr, "unknown billing subcommand: %s\n", args[0])
		return 2
	}
}

// runBillingReconcile implements
//
//	gatewayctl billing reconcile [--from YYYY-MM-DD] [--to YYYY-MM-DD] [--apply]
//
// Range defaults to "today" (00:00 to 24:00 in America/Sao_Paulo). For each
// active tenant, compares the SUM over billing_events rows in the range vs
// the usage_counters cache for today (the cache only holds the current day
// under the current sqlc surface — see package doc). Drift above
// driftThresholdPercent is written to stderr. Exit code:
//
//	0 — no drift found (or --apply succeeded)
//	1 — drift found AND --apply not supplied (operator must investigate)
//	2 — flag parse / invalid date
//
// With --apply, the authoritative SUM is rewritten into usage_counters via
// ResetUsageCountersForReconcile (idempotent UPSERT).
func runBillingReconcile(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("billing reconcile", flag.ExitOnError)
	fromS := fs.String("from", "", "ISO date (YYYY-MM-DD); default = today")
	toS := fs.String("to", "", "ISO date (YYYY-MM-DD); default = today")
	apply := fs.Bool("apply", false, "rewrite usage_counters from billing_events on drift")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	loc, err := time.LoadLocation(scheduleTZ)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load tz: %v\n", err)
		return 1
	}
	now := time.Now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	to := from.Add(24 * time.Hour)
	if *fromS != "" {
		t, err := time.ParseInLocation("2006-01-02", *fromS, loc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse --from: %v\n", err)
			return 2
		}
		from = t
	}
	if *toS != "" {
		t, err := time.ParseInLocation("2006-01-02", *toS, loc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse --to: %v\n", err)
			return 2
		}
		to = t.Add(24 * time.Hour) // exclusive end
	}
	if !from.Before(to) {
		fmt.Fprintf(os.Stderr, "invalid range: --from must be before --to\n")
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	tenants, err := q.ListTenantsForLoader(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list tenants: %v\n", err)
		return 1
	}

	todayLocal := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	anyDrift := false
	for _, tenant := range tenants {
		days, err := q.SumBillingEventsByDate(ctx, gen.SumBillingEventsByDateParams{
			TenantID: tenant.ID,
			Ts:       from,
			Ts_2:     to,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "sum billing events (tenant=%s): %v\n", tenant.Slug, err)
			return 1
		}
		if len(days) == 0 {
			continue
		}
		// Cache is only queryable for TODAY in the current sqlc surface; the
		// reconcile comparison below applies only to the today row and any
		// other day rows are reported as "cache unavailable" so the operator
		// still sees drift analysis structure.
		cachedToday, cacheErr := q.GetUsageCountersToday(ctx, tenant.ID)

		for _, day := range days {
			billingSum := day.TokensIn + day.TokensOut

			// Only the row whose date matches today's local date can be
			// compared against GetUsageCountersToday. Other rows are treated
			// as "drift unknown" — not an error, but surfaced for operator
			// visibility.
			var cachedSum int64
			dayIsToday := day.Date.Valid && sameYMD(day.Date.Time, todayLocal)
			if dayIsToday {
				if cacheErr == nil {
					cachedSum = cachedToday.TokensIn + cachedToday.TokensOut
				}
			} else {
				fmt.Fprintf(os.Stderr,
					"INFO tenant=%s date=%s billing=%d cached=?? (historical; cache query restricted to today)\n",
					tenant.Slug, formatDate(day.Date), billingSum)
				continue
			}

			if billingSum == 0 && cachedSum == 0 {
				continue
			}
			var drift float64
			if billingSum > 0 {
				drift = math.Abs(float64(billingSum-cachedSum)) / float64(billingSum) * 100.0
			} else {
				drift = 100.0 // cached has value, billing does not → full drift
			}
			if drift <= driftThresholdPercent {
				continue
			}

			anyDrift = true
			fmt.Fprintf(os.Stderr,
				"DRIFT tenant=%s date=%s billing=%d cached=%d drift=%.4f%%\n",
				tenant.Slug, formatDate(day.Date), billingSum, cachedSum, drift)

			if *apply {
				// Rewrite usage_counters from billing_events. phantom +
				// external flow directly from the SumBillingEventsByDate row.
				phantom := day.CostLocalPhantomBrl
				external := day.CostExternalBrl
				// Cast audio_seconds (float32 on billing SUM) → bigint for
				// usage_counters storage; integer seconds is the contract.
				audioSec := int64(math.Round(float64(day.AudioSeconds)))

				if err := q.ResetUsageCountersForReconcile(ctx, gen.ResetUsageCountersForReconcileParams{
					TenantID:            tenant.ID,
					Date:                day.Date,
					TokensIn:            day.TokensIn,
					TokensOut:           day.TokensOut,
					AudioSeconds:        audioSec,
					EmbedsCount:         day.EmbedsCount,
					CostLocalPhantomBrl: phantom,
					CostExternalBrl:     external,
					RequestsCount:       day.RequestsCount,
				}); err != nil {
					fmt.Fprintf(os.Stderr, "apply tenant=%s date=%s: %v\n",
						tenant.Slug, formatDate(day.Date), err)
					return 1
				}
				log.Info("usage_counters rewritten from billing",
					"tenant", tenant.Slug, "date", formatDate(day.Date))
			}
		}
	}

	if anyDrift && !*apply {
		return 1
	}
	if anyDrift && *apply {
		fmt.Println("reconcile applied")
	} else {
		fmt.Println("no drift detected")
	}
	return 0
}

// runUsage implements `gatewayctl usage report ...`. Queries billing_events
// directly via sqlc so the CLI does not require the admin HTTP socket nor
// an X-Admin-Key.
func runUsage(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 || args[0] != "report" {
		fmt.Fprintln(os.Stderr,
			"Usage: gatewayctl usage report --tenant <slug|uuid> --from YYYY-MM-DD --to YYYY-MM-DD [--format table|json]")
		return 2
	}
	fs := flag.NewFlagSet("usage report", flag.ExitOnError)
	tenantFlag := fs.String("tenant", "", "tenant slug or UUID (required)")
	fromS := fs.String("from", "", "ISO date YYYY-MM-DD (required)")
	toS := fs.String("to", "", "ISO date YYYY-MM-DD (required)")
	format := fs.String("format", "table", "table | json")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *tenantFlag == "" || *fromS == "" || *toS == "" {
		fs.Usage()
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(os.Stderr, "invalid --format %q; expected table or json\n", *format)
		return 2
	}

	loc, err := time.LoadLocation(scheduleTZ)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load tz: %v\n", err)
		return 1
	}
	fromT, err1 := time.ParseInLocation("2006-01-02", *fromS, loc)
	toT, err2 := time.ParseInLocation("2006-01-02", *toS, loc)
	if err1 != nil || err2 != nil {
		fmt.Fprintln(os.Stderr, "date parse error: --from and --to must be YYYY-MM-DD")
		return 2
	}
	toT = toT.Add(24 * time.Hour) // exclusive end

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	// Resolve tenant id: try UUID first, then slug.
	var tenantID uuid.UUID
	var tenantSlug, tenantName string
	if id, perr := uuid.Parse(*tenantFlag); perr == nil {
		tenantID = id
		cfg, cerr := q.GetTenantConfig(ctx, id)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "lookup tenant id %q: %v\n", *tenantFlag, cerr)
			return 1
		}
		tenantSlug = cfg.Slug
		tenantName = cfg.Name
	} else {
		slugRow, serr := q.GetTenantBySlug(ctx, *tenantFlag)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "tenant %q not found: %v\n", *tenantFlag, serr)
			return 1
		}
		tenantID = slugRow.ID
		tenantSlug = slugRow.Slug
		tenantName = slugRow.Name
	}

	dayRows, err := q.SumBillingEventsByDate(ctx, gen.SumBillingEventsByDateParams{
		TenantID: tenantID, Ts: fromT, Ts_2: toT,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sum billing events by date: %v\n", err)
		return 1
	}
	sumRow, err := q.SumBillingEventsRange(ctx, gen.SumBillingEventsRangeParams{
		TenantID: tenantID, Ts: fromT, Ts_2: toT,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sum billing events range: %v\n", err)
		return 1
	}

	if *format == "json" {
		return emitUsageJSON(tenantID, tenantSlug, tenantName, *fromS, *toS, sumRow, dayRows)
	}
	return emitUsageTable(dayRows)
}

// emitUsageJSON writes the same SC-3 shape documented in D-D2 / admin/usage.go.
// The CostTotalBRL is cost_local_brl + cost_external_brl (phantom is
// reporting-only and NOT summed into total).
func emitUsageJSON(
	tenantID uuid.UUID,
	slug, name, fromS, toS string,
	sumRow gen.SumBillingEventsRangeRow,
	dayRows []gen.SumBillingEventsByDateRow,
) int {
	type daySection struct {
		Date                string  `json:"date"`
		TokensIn            int64   `json:"tokens_in"`
		TokensOut           int64   `json:"tokens_out"`
		AudioSeconds        float64 `json:"audio_seconds"`
		EmbedsCount         int64   `json:"embeds_count"`
		CostLocalBRL        float64 `json:"cost_local_brl"`
		CostLocalPhantomBRL float64 `json:"cost_local_phantom_brl"`
		CostExternalBRL     float64 `json:"cost_external_brl"`
		CostTotalBRL        float64 `json:"cost_total_brl"`
		RequestsCount       int64   `json:"requests_count"`
	}
	type summary struct {
		TokensIn            int64   `json:"tokens_in"`
		TokensOut           int64   `json:"tokens_out"`
		AudioSeconds        float64 `json:"audio_seconds"`
		EmbedsCount         int64   `json:"embeds_count"`
		CostLocalBRL        float64 `json:"cost_local_brl"`
		CostLocalPhantomBRL float64 `json:"cost_local_phantom_brl"`
		CostExternalBRL     float64 `json:"cost_external_brl"`
		CostTotalBRL        float64 `json:"cost_total_brl"`
		RequestsCount       int64   `json:"requests_count"`
	}
	type tenantSec struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	type rangeSec struct {
		From        string `json:"from"`
		To          string `json:"to"`
		Granularity string `json:"granularity"`
		Timezone    string `json:"timezone"`
	}
	type envelope struct {
		Tenant  tenantSec    `json:"tenant"`
		Range   rangeSec     `json:"range"`
		Summary summary      `json:"summary"`
		Rows    []daySection `json:"rows"`
	}

	sumLocal, _ := sumRow.CostLocalBrl.Float64Value()
	sumPhantom, _ := sumRow.CostLocalPhantomBrl.Float64Value()
	sumExternal, _ := sumRow.CostExternalBrl.Float64Value()

	out := envelope{
		Tenant: tenantSec{ID: tenantID.String(), Slug: slug, Name: name},
		Range:  rangeSec{From: fromS, To: toS, Granularity: "day", Timezone: scheduleTZ},
		Summary: summary{
			TokensIn:            sumRow.TokensIn,
			TokensOut:           sumRow.TokensOut,
			AudioSeconds:        float64(sumRow.AudioSeconds),
			EmbedsCount:         sumRow.EmbedsCount,
			CostLocalBRL:        sumLocal.Float64,
			CostLocalPhantomBRL: sumPhantom.Float64,
			CostExternalBRL:     sumExternal.Float64,
			CostTotalBRL:        sumLocal.Float64 + sumExternal.Float64,
			RequestsCount:       sumRow.RequestsCount,
		},
		Rows: make([]daySection, 0, len(dayRows)),
	}
	for _, r := range dayRows {
		localF, _ := r.CostLocalBrl.Float64Value()
		phantomF, _ := r.CostLocalPhantomBrl.Float64Value()
		externalF, _ := r.CostExternalBrl.Float64Value()
		out.Rows = append(out.Rows, daySection{
			Date:                formatDate(r.Date),
			TokensIn:            r.TokensIn,
			TokensOut:           r.TokensOut,
			AudioSeconds:        float64(r.AudioSeconds),
			EmbedsCount:         r.EmbedsCount,
			CostLocalBRL:        localF.Float64,
			CostLocalPhantomBRL: phantomF.Float64,
			CostExternalBRL:     externalF.Float64,
			CostTotalBRL:        localF.Float64 + externalF.Float64,
			RequestsCount:       r.RequestsCount,
		})
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "encode json: %v\n", err)
		return 1
	}
	return 0
}

// emitUsageTable writes a tabwriter-formatted daily breakdown. Phantom cost
// is shown explicitly so operators can see the notional OpenRouter-equivalent
// the GPU displaced (D-B4).
func emitUsageTable(dayRows []gen.SumBillingEventsByDateRow) int {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "DATE\tTOKENS_IN\tTOKENS_OUT\tAUDIO_S\tEMBEDS\tCOST_LOCAL\tCOST_PHANTOM\tCOST_EXTERNAL\tREQS")
	for _, r := range dayRows {
		local, _ := r.CostLocalBrl.Float64Value()
		phantom, _ := r.CostLocalPhantomBrl.Float64Value()
		external, _ := r.CostExternalBrl.Float64Value()
		fmt.Fprintf(tw, "%s\t%d\t%d\t%.1f\t%d\t%.4f\t%.4f\t%.4f\t%d\n",
			formatDate(r.Date),
			r.TokensIn, r.TokensOut,
			float64(r.AudioSeconds), r.EmbedsCount,
			local.Float64, phantom.Float64, external.Float64,
			r.RequestsCount)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "flush: %v\n", err)
		return 1
	}
	return 0
}

// formatDate renders a pgtype.Date as YYYY-MM-DD, or "-" if the value is
// NULL. Kept in one place so the JSON + table paths stay consistent.
func formatDate(d pgtype.Date) string {
	if !d.Valid {
		return "-"
	}
	return d.Time.Format("2006-01-02")
}

// sameYMD returns true if a and b fall on the same year-month-day (in their
// native locations). Used by the reconcile loop to match a billing SUM row
// against the today-only usage_counters cache query.
func sameYMD(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
