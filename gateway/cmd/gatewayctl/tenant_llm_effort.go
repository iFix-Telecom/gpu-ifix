// gatewayctl tenant set-llm-effort — override de effort/thinking do LLM por
// tenant (Phase 23, HAIKU-05).
//
//	gatewayctl tenant set-llm-effort --slug <slug> --effort <off|low|medium|high|xhigh|max>
//	gatewayctl tenant set-llm-effort --slug <slug> --clear
//
// O valor é validado contra a allowlist ANTES do UPDATE (a CHECK da coluna,
// migration 0041, é a segunda barreira). O trigger tenants_update_notify
// recarrega o tenants.Loader de cada réplica. Precedência no request:
// pedido explícito do cliente > tenants.llm_effort > padrão "high".
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// tenantLLMEffortValues é a allowlist (mesma de proxy.validLLMEfforts e da CHECK 0041).
var tenantLLMEffortValues = []string{"off", "low", "medium", "high", "xhigh", "max"}

// llmEffortUpdater é o recorte de gen.Queries usado pelo comando (fake nos testes).
type llmEffortUpdater interface {
	UpdateTenantLLMEffort(ctx context.Context, arg gen.UpdateTenantLLMEffortParams) (int64, error)
}

func runTenantSetLLMEffort(ctx context.Context, args []string, log *slog.Logger) int {
	code := setTenantLLMEffort(ctx, args, os.Stdout, os.Stderr, func(ctx context.Context) (llmEffortUpdater, func(), error) {
		_, pool, err := loadAndPool(ctx, log)
		if err != nil {
			return nil, nil, err
		}
		return gen.New(pool), pool.Close, nil
	})
	if code == 0 {
		log.Info("tenant llm_effort set")
	}
	return code
}

// setTenantLLMEffort é o núcleo testável: parse + validação (sem tocar o
// banco) e só então connect + UPDATE.
func setTenantLLMEffort(ctx context.Context, args []string, stdout, stderr io.Writer, connect func(context.Context) (llmEffortUpdater, func(), error)) int {
	fs := flag.NewFlagSet("tenant set-llm-effort", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slug := fs.String("slug", "", "tenant slug (required)")
	effort := fs.String("effort", "", "effort: "+strings.Join(tenantLLMEffortValues, "|"))
	clear := fs.Bool("clear", false, "clear the tenant's llm_effort (NULL → padrão high)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *slug == "" {
		fmt.Fprintln(stderr, "error: --slug is required")
		return 2
	}
	if (*effort == "") == !*clear {
		fmt.Fprintln(stderr, "error: pass exactly one of --effort <value> or --clear")
		return 2
	}
	var val pgtype.Text
	if *effort != "" {
		ok := false
		for _, v := range tenantLLMEffortValues {
			if v == *effort {
				ok = true
				break
			}
		}
		if !ok {
			fmt.Fprintf(stderr, "error: invalid --effort %q (accepted: %s)\n", *effort, strings.Join(tenantLLMEffortValues, ", "))
			return 2
		}
		val = pgtype.Text{String: *effort, Valid: true}
	}

	q, closeFn, err := connect(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer closeFn()
	n, err := q.UpdateTenantLLMEffort(ctx, gen.UpdateTenantLLMEffortParams{Slug: *slug, LlmEffort: val})
	if err != nil {
		fmt.Fprintf(stderr, "update tenant llm_effort: %v\n", err)
		return 1
	}
	if n == 0 {
		fmt.Fprintf(stderr, "error: tenant not found: %q\n", *slug)
		return 2
	}
	out := "-"
	if val.Valid {
		out = val.String
	}
	fmt.Fprintf(stdout, "tenant set-llm-effort: slug=%s llm_effort=%s\n", *slug, out)
	return 0
}
