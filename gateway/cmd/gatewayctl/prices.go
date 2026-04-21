// Package main (prices.go): `gatewayctl prices` subcommand family.
//
//	set     — atomic swap in a single pg txn (ExpireActivePrice + InsertPrice).
//	          The NOTIFY prices_changed trigger fires exactly once on commit.
//	list    — tabwriter dump of currently active prices (or --all history).
//	set-fx  — same atomic swap for USD/BRL (or any currency_pair) fx_rates.
//
// D-B3 hot-reload pattern: gateway's prices loader LISTENs on prices_changed
// and rebuilds its in-memory map within <1s of the commit below. The loader
// lives in gateway/internal/billing/ (Plan 04-05).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// runPrices dispatches `gatewayctl prices <subcommand>`.
func runPrices(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: gatewayctl prices <set|list|set-fx> [flags]")
		return 2
	}
	switch args[0] {
	case "set":
		return runPricesSet(ctx, args[1:], log)
	case "list":
		return runPricesList(ctx, args[1:], log)
	case "set-fx":
		return runPricesSetFX(ctx, args[1:], log)
	default:
		fmt.Fprintf(os.Stderr, "unknown prices subcommand: %s\n", args[0])
		return 2
	}
}

// runPricesSet implements
//
//	gatewayctl prices set --model X --provider Y --unit Z --usd N [--notes S]
//
// Executes ExpireActivePrice + InsertPrice inside the same pgx txn so that
// the NOTIFY prices_changed trigger fires exactly once on commit. If either
// statement fails the txn rolls back, preserving the prior active row.
func runPricesSet(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("prices set", flag.ExitOnError)
	model := fs.String("model", "", "model name (required)")
	provider := fs.String("provider", "", "provider name (required)")
	unit := fs.String("unit", "", "input_token|output_token|audio_second|embed_request (required)")
	usd := fs.Float64("usd", 0, "unit cost USD, must be > 0 (required)")
	notes := fs.String("notes", "", "free-form note stored with the row")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*model) == "" || strings.TrimSpace(*provider) == "" ||
		strings.TrimSpace(*unit) == "" || *usd <= 0 {
		fs.Usage()
		return 2
	}
	switch *unit {
	case "input_token", "output_token", "audio_second", "embed_request":
	default:
		fmt.Fprintf(os.Stderr, "invalid --unit %q; expected input_token|output_token|audio_second|embed_request\n", *unit)
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "begin tx: %v\n", err)
		return 1
	}
	// Rollback on any early return; Commit below sets the tx to committed
	// (Rollback on a committed tx is a harmless no-op in pgx v5).
	defer func() { _ = tx.Rollback(ctx) }()

	q := gen.New(tx)

	if err := q.ExpireActivePrice(ctx, gen.ExpireActivePriceParams{
		Model:    *model,
		Provider: *provider,
		Unit:     *unit,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "expire active price: %v\n", err)
		return 1
	}

	var usdNum pgtype.Numeric
	if err := usdNum.Scan(fmt.Sprintf("%.8f", *usd)); err != nil {
		fmt.Fprintf(os.Stderr, "encode usd: %v\n", err)
		return 1
	}
	var notesParam pgtype.Text
	if strings.TrimSpace(*notes) != "" {
		notesParam = pgtype.Text{String: *notes, Valid: true}
	}

	ins, err := q.InsertPrice(ctx, gen.InsertPriceParams{
		Model:       *model,
		Provider:    *provider,
		Unit:        *unit,
		UnitCostUsd: usdNum,
		Notes:       notesParam,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "insert price: %v\n", err)
		return 1
	}

	if err := tx.Commit(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "commit: %v\n", err)
		return 1
	}

	log.Info("price updated",
		"price_id", ins.ID.String(),
		"model", *model,
		"provider", *provider,
		"unit", *unit,
		"usd", *usd,
	)
	fmt.Printf("price set: id=%s model=%s provider=%s unit=%s usd=%.8f\n",
		ins.ID.String(), *model, *provider, *unit, *usd)
	return 0
}

// runPricesList implements `gatewayctl prices list [--all]`. By default only
// currently-active rows (valid_to IS NULL) are shown; --all includes expired
// history for audit.
func runPricesList(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("prices list", flag.ExitOnError)
	all := fs.Bool("all", false, "include expired rows")
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

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tPROVIDER\tUNIT\tUSD\tVALID_FROM\tVALID_TO\tNOTES")

	var rows []gen.AiGatewayPrice
	if *all {
		rows, err = q.ListAllPrices(ctx)
	} else {
		rows, err = q.ListActivePrices(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "list prices: %v\n", err)
		return 1
	}
	for _, r := range rows {
		var cost float64
		if f, ferr := r.UnitCostUsd.Float64Value(); ferr == nil {
			cost = f.Float64
		}
		vt := "active"
		if r.ValidTo.Valid {
			vt = r.ValidTo.Time.UTC().Format(time.RFC3339)
		}
		notes := "-"
		if r.Notes.Valid {
			notes = r.Notes.String
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.8f\t%s\t%s\t%s\n",
			r.Model, r.Provider, r.Unit, cost,
			r.ValidFrom.UTC().Format(time.RFC3339), vt, notes)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "flush: %v\n", err)
		return 1
	}
	return 0
}

// runPricesSetFX implements `gatewayctl prices set-fx --usd-brl N`. Mirrors
// runPricesSet: ExpireActiveFX + InsertFX in a single txn. Currency pair is
// fixed to USD/BRL for now — the table supports others but the CLI only
// exposes the rate Ifix cares about weekly.
func runPricesSetFX(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("prices set-fx", flag.ExitOnError)
	usdBrl := fs.Float64("usd-brl", 0, "USD→BRL rate, must be > 0 (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *usdBrl <= 0 {
		fs.Usage()
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "begin tx: %v\n", err)
		return 1
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := gen.New(tx)
	if err := q.ExpireActiveFX(ctx, "USD/BRL"); err != nil {
		fmt.Fprintf(os.Stderr, "expire fx: %v\n", err)
		return 1
	}
	var rateNum pgtype.Numeric
	if err := rateNum.Scan(fmt.Sprintf("%.6f", *usdBrl)); err != nil {
		fmt.Fprintf(os.Stderr, "encode rate: %v\n", err)
		return 1
	}
	ins, err := q.InsertFX(ctx, gen.InsertFXParams{
		CurrencyPair: "USD/BRL",
		Rate:         rateNum,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "insert fx: %v\n", err)
		return 1
	}
	if err := tx.Commit(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "commit: %v\n", err)
		return 1
	}
	log.Info("USD/BRL fx updated", "fx_id", ins.ID.String(), "rate", *usdBrl)
	fmt.Printf("fx set: id=%s pair=USD/BRL rate=%.6f\n", ins.ID.String(), *usdBrl)
	return 0
}
