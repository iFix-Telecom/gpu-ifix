//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
)

// TestIntegration_01_Migrate verifies the full migration up/down/up cycle
// and validates seed state (1 tenant, 5 model_aliases — 2 tier-0 + 3 tier-1
// after Phase 11.1 migration 0028 deleted (whisper, local-stt),
// ≥3 audit_log partitions).
func TestIntegration_01_Migrate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	// Seed tenant count.
	var tenantCount int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM ai_gateway.tenants").Scan(&tenantCount); err != nil {
		t.Fatal(err)
	}
	if tenantCount != 1 {
		t.Errorf("tenants count got %d want 1", tenantCount)
	}

	var aliasCount int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM ai_gateway.model_aliases").Scan(&aliasCount); err != nil {
		t.Fatal(err)
	}
	// Phase 06.9 migration 0026 widened model_aliases PK to (alias, upstream_name)
	// and added 3 tier-1 seed rows alongside the 3 pre-existing tier-0 rows → 6 total.
	// Phase 11.1 migration 0028 deleted (whisper, local-stt) → 5 total.
	if aliasCount != 5 {
		t.Errorf("model_aliases count got %d want 5", aliasCount)
	}

	// Idempotent re-run of Up.
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("second up: %v", err)
	}

	// Full down/up cycle validates migration rollback scripts.
	if err := db.Down(ctx, pool, 1); err != nil {
		t.Fatalf("down 1: %v", err)
	}
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("re-up: %v", err)
	}

	// Verify partitioned tables created ≥3 partitions each (current + 2
	// ahead from migration DO block).
	var partCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pg_inherits WHERE inhparent = 'ai_gateway.audit_log'::regclass`).Scan(&partCount); err != nil {
		t.Fatal(err)
	}
	if partCount < 3 {
		t.Errorf("audit_log partitions got %d want >=3", partCount)
	}
	var contentPartCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pg_inherits WHERE inhparent = 'ai_gateway.audit_log_content'::regclass`).Scan(&contentPartCount); err != nil {
		t.Fatal(err)
	}
	if contentPartCount < 3 {
		t.Errorf("audit_log_content partitions got %d want >=3", contentPartCount)
	}

	// Verify goose bookkeeping exists (schema may be `public` when goose
	// creates the table on a connection that hasn't had AfterConnect
	// applied yet — stdlib.OpenDBFromPool doesn't always re-run the hook
	// on every acquire; tooling outside gateway has to qualify as
	// `ai_gateway.goose_db_version` OR `public.goose_db_version` per
	// whatever search_path was active at first migration).
	var gooseCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relname = 'goose_db_version'`).Scan(&gooseCount); err != nil {
		t.Fatal(err)
	}
	if gooseCount < 1 {
		t.Errorf("goose_db_version table not found in any schema")
	}

	// Sanity: core gateway tables exist in ai_gateway schema (this is what
	// the gateway code depends on — unaffected by where goose's own
	// bookkeeping lives).
	for _, tbl := range []string{"tenants", "api_keys", "audit_log", "audit_log_content", "model_aliases", "usage_counters"} {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relname = $1 AND n.nspname = 'ai_gateway'
			)`, tbl).Scan(&exists)
		if err != nil {
			t.Fatalf("query pg_class for ai_gateway.%s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("ai_gateway.%s does not exist", tbl)
		}
	}
}
