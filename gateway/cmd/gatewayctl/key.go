package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

func runKey(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: gatewayctl key create|revoke|list [flags]")
		return 2
	}
	switch args[0] {
	case "create":
		return runKeyCreate(ctx, args[1:], log)
	case "revoke":
		return runKeyRevoke(ctx, args[1:], log)
	case "list":
		return runKeyList(ctx, args[1:], log)
	default:
		fmt.Fprintf(flag.CommandLine.Output(), "unknown subcommand: %s\n", args[0])
		return 2
	}
}

func runKeyCreate(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("key create", flag.ExitOnError)
	tenantSlug := fs.String("tenant", "", "tenant slug (required)")
	dataClass := fs.String("data-class", "normal", "normal | sensitive")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tenantSlug == "" {
		fs.Usage()
		return 2
	}
	if *dataClass != "normal" && *dataClass != "sensitive" {
		fmt.Fprintf(fs.Output(), "--data-class must be 'normal' or 'sensitive'\n")
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	tenant, err := q.GetTenantBySlug(ctx, *tenantSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(fs.Output(), "error: tenant '%s' not found\n", *tenantSlug)
			return 1
		}
		fmt.Fprintf(fs.Output(), "error: lookup tenant: %v\n", err)
		return 1
	}

	raw, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: generate key: %v\n", err)
		return 1
	}

	inserted, err := q.InsertAPIKey(ctx, gen.InsertAPIKeyParams{
		TenantID:      tenant.ID,
		KeyHash:       hash,
		KeyLookupHash: lookupHash, // SHA-256 for fast hot-path lookup; Codex review [HIGH] 02-03
		KeyPrefix:     prefix,
		DataClass:     *dataClass, // pgx encodes string → ENUM
	})
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: insert key: %v\n", err)
		return 1
	}

	// IMPORTANT: print the raw key to stdout ONCE. Operator must copy it now.
	// Do NOT log.Info(raw, ...) — the slog redactor only covers known key names.
	fmt.Printf("key=%s\nid=%s\nprefix=%s\ntenant=%s\ndata_class=%s\n",
		raw, inserted.ID.String(), prefix, *tenantSlug, *dataClass)
	log.Info("api key issued",
		"api_key_id", inserted.ID.String(),
		"tenant_id", tenant.ID.String(),
		"tenant_slug", *tenantSlug,
		"data_class", *dataClass,
		"key_prefix", prefix,
	) // NO raw key in log record
	return 0
}

func runKeyRevoke(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("key revoke", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(fs.Output(), "Usage: gatewayctl key revoke <api_key_id>")
		return 2
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: invalid UUID: %v\n", err)
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	existing, err := q.GetAPIKeyByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(fs.Output(), "error: api_key '%s' not found\n", id.String())
			return 1
		}
		fmt.Fprintf(fs.Output(), "error: lookup key: %v\n", err)
		return 1
	}
	// Status comes back from sqlc as interface{} (Postgres ENUM). Coerce.
	statusStr := ""
	switch s := existing.Status.(type) {
	case string:
		statusStr = s
	case []byte:
		statusStr = string(s)
	}
	if statusStr == "revoked" {
		fmt.Printf("already revoked: id=%s\n", id.String())
		return 0
	}
	if err := q.RevokeAPIKey(ctx, id); err != nil {
		fmt.Fprintf(fs.Output(), "error: revoke key: %v\n", err)
		return 1
	}
	fmt.Printf("revoked: id=%s prefix=%s\n", id.String(), existing.KeyPrefix)
	log.Info("api key revoked", "api_key_id", id.String(), "key_prefix", existing.KeyPrefix)
	return 0
}

// keyLister is the sqlc surface consumed by runKeyList. Declared as an
// interface so the unit test (key_test.go) can substitute a stub
// returning canned Rows without touching loadAndPool / the real DB pool
// (Pattern D invariant 4: DB access via gen.New(pool) in production,
// but tests use a hermetic fake).
type keyLister interface {
	ListActiveKeysAllWithMeta(ctx context.Context) ([]gen.ListActiveKeysAllWithMetaRow, error)
	ListActiveKeysByTenantWithMeta(ctx context.Context, tenantID uuid.UUID) ([]gen.ListActiveKeysByTenantWithMetaRow, error)
	GetTenantBySlug(ctx context.Context, slug string) (gen.GetTenantBySlugRow, error)
}

// renderKeyList writes the aligned table for `gatewayctl key list` to
// out. Pulled out of runKeyList so the unit test can drive rendering
// without standing up a Postgres pool. Columns mirror
// runAdminKeyList (admin_key.go:265-274) for operator parity.
//
// IMPORTANT: NEVER print the bcrypt hash column or the raw key. The
// projection of ListActiveKeys*WithMeta deliberately excludes
// secret-bearing columns (threat T-11-OPS-02); this helper also
// references only the operator-safe fields {ID, TenantSlug, KeyPrefix,
// Status, DataClass, CreatedAt, LastUsedAt}.
func renderKeyList(out io.Writer, rows []gen.ListActiveKeysAllWithMetaRow) error {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tTENANT\tPREFIX\tSTATUS\tDATA_CLASS\tCREATED\tLAST_USED"); err != nil {
		return err
	}
	for _, r := range rows {
		lu := "-"
		if r.LastUsedAt.Valid {
			lu = r.LastUsedAt.Time.UTC().Format(time.RFC3339)
		}
		// Status + DataClass come back as interface{} from sqlc (Postgres
		// ENUM scan target). Coerce to string for printing.
		statusStr := enumString(r.Status)
		dataClassStr := enumString(r.DataClass)
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID.String(), r.TenantSlug, r.KeyPrefix,
			statusStr, dataClassStr,
			r.CreatedAt.UTC().Format(time.RFC3339), lu); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// enumString coerces a sqlc-scanned ENUM (interface{}) to a printable
// string. Postgres ENUMs scan into either string or []byte depending on
// how pgx registered the codec; this handles both.
func enumString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return ""
}

// runKeyList implements
//
//	gatewayctl key list             # all active keys
//	gatewayctl key list --tenant X  # filter by tenant slug
//
// Reads the new ListActiveKeysAllWithMeta / ListActiveKeysByTenantWithMeta
// sqlc queries (Phase 11 Plan 04 Task 0). NEVER prints raw keys or the
// bcrypt hash column — only key_prefix (threat T-11-OPS-02).
func runKeyList(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("key list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	tenantSlug := fs.String("tenant", "", "filter by tenant slug (optional)")
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

	return runKeyListWith(ctx, q, *tenantSlug, os.Stdout, log)
}

// runKeyListWith is the testable inner core of runKeyList: takes a
// keyLister stub + an io.Writer so the test can drive without
// loadAndPool. Production runKeyList wires gen.New(pool) + os.Stdout.
func runKeyListWith(ctx context.Context, q keyLister, tenantSlug string, out io.Writer, log *slog.Logger) int {
	var rows []gen.ListActiveKeysAllWithMetaRow
	if tenantSlug != "" {
		// Resolve slug -> UUID via GetTenantBySlug (already used by
		// runKeyCreate, line 57). Returning a clean error message
		// rather than a raw pgx.ErrNoRows mirrors create's UX.
		t, err := q.GetTenantBySlug(ctx, tenantSlug)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				fmt.Fprintf(os.Stderr, "error: tenant '%s' not found\n", tenantSlug)
				return 1
			}
			fmt.Fprintf(os.Stderr, "error: lookup tenant: %v\n", err)
			return 1
		}
		byTenant, err := q.ListActiveKeysByTenantWithMeta(ctx, t.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: list keys: %v\n", err)
			return 1
		}
		// Normalize the tenant-scoped Row into the all-scope Row shape
		// so renderKeyList consumes a single type. Field set is
		// identical between the two queries by design.
		rows = make([]gen.ListActiveKeysAllWithMetaRow, 0, len(byTenant))
		for _, r := range byTenant {
			rows = append(rows, gen.ListActiveKeysAllWithMetaRow{
				ID:         r.ID,
				TenantID:   r.TenantID,
				TenantSlug: r.TenantSlug,
				KeyPrefix:  r.KeyPrefix,
				Status:     r.Status,
				DataClass:  r.DataClass,
				CreatedAt:  r.CreatedAt,
				LastUsedAt: r.LastUsedAt,
			})
		}
	} else {
		all, err := q.ListActiveKeysAllWithMeta(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: list keys: %v\n", err)
			return 1
		}
		rows = all
	}

	if err := renderKeyList(out, rows); err != nil {
		fmt.Fprintf(os.Stderr, "error: render table: %v\n", err)
		return 1
	}
	log.Info("key list rendered", "tenant", tenantSlug, "rows", len(rows))
	return 0
}
