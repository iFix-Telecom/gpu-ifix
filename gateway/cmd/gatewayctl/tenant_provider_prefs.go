// gatewayctl tenant set-provider-prefs — per-tenant OpenRouter provider
// routing (quick 260830-o2j).
//
//	gatewayctl tenant set-provider-prefs --slug <slug> --prefs '<json>'
//	gatewayctl tenant set-provider-prefs --slug <slug> --clear
//
// The JSON is validated by models.ValidateProviderPrefs BEFORE the UPDATE;
// the tenants_update_notify trigger (0037) hot-reloads every replica.
// Precedence at request time: tenant prefs > model_aliases row prefs >
// global env UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

func runTenantSetProviderPrefs(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("tenant set-provider-prefs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	slug := fs.String("slug", "", "tenant slug (required)")
	prefs := fs.String("prefs", "", "OpenRouter provider object as JSON")
	clear := fs.Bool("clear", false, "clear the tenant's provider_prefs (NULL)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *slug == "" {
		fmt.Fprintln(os.Stderr, "error: --slug is required")
		return 2
	}
	if (*prefs == "") == !*clear {
		fmt.Fprintln(os.Stderr, "error: pass exactly one of --prefs '<json>' or --clear")
		return 2
	}
	var stored []byte
	if *prefs != "" {
		canon, err := models.ValidateProviderPrefs([]byte(*prefs))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		stored = canon
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)
	n, err := q.UpdateTenantProviderPrefs(ctx, gen.UpdateTenantProviderPrefsParams{Slug: *slug, ProviderPrefs: stored})
	if err != nil {
		fmt.Fprintf(os.Stderr, "update tenant provider_prefs: %v\n", err)
		return 1
	}
	if n == 0 {
		fmt.Fprintf(os.Stderr, "error: tenant %q not found\n", *slug)
		return 2
	}
	out := "-"
	if stored != nil {
		out = string(stored)
	}
	fmt.Fprintf(os.Stdout, "tenant set-provider-prefs: slug=%s provider_prefs=%s\n", *slug, out)
	log.Info("tenant provider_prefs set", "slug", *slug, "has_provider_prefs", stored != nil)
	return 0
}
