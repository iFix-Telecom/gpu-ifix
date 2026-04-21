//go:build integration

// Package integration: Phase 4 shared fixtures.
//
// seedPhase4 inserts a known-good baseline used by every Phase 4 integration
// test: 2 tenants (1 normal = converseai, 1 sensitive = cobrancas), 1 API key
// for the normal tenant, 2 active prices for qwen3.5-27b (input + output), 1
// FX row (USD/BRL), and 1 admin key (bcrypt cost 10). It relies on
// freshSchema having been called first.
//
// The helper returns a seededPhase4 struct exposing all identifiers tests
// need to build authenticated/admin requests or assert rows in downstream
// tables (billing_events, usage_counters).
package integration

import (
	"context"
	"crypto/sha256"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// seededPhase4 bundles the identifiers returned by seedPhase4 so tests can
// wire up authenticated requests and assert DB rows by known UUID.
type seededPhase4 struct {
	ConverseAITenantID uuid.UUID
	CobrancasTenantID  uuid.UUID // sensitive
	ConverseAIAPIKeyID uuid.UUID
	ConverseAIAPIKey   string // raw (for Authorization header)
	AdminKeyRaw        string // raw (for X-Admin-Key header)
}

// seedPhase4 inserts the Phase 4 baseline fixture. Assumes freshSchema ran
// first; reuses the pool from freshSchema's return.
//
// Idempotency:
//   - tenants converseai/cobrancas are INSERTed with ON CONFLICT(slug) DO
//     NOTHING-like behavior via the seed table state (freshSchema TRUNCATE
//     clears them first; seedPhase4 then inserts cleanly).
//   - prices use InsertPrice (one row per call). Calling seedPhase4 more
//     than once per test would duplicate active rows; tests MUST call it
//     exactly once after freshSchema.
func seedPhase4(t *testing.T, ctx context.Context, pool *pgxpool.Pool) seededPhase4 {
	t.Helper()
	q := gen.New(pool)

	// ConverseAI (normal, mode=24/7, default quotas from migration 0013).
	// freshSchema re-seeds `converseai` — fetch the existing row instead of
	// inserting to avoid duplicate-slug errors.
	cvRow, err := q.GetTenantBySlug(ctx, "converseai")
	if err != nil {
		t.Fatalf("get converseai tenant: %v", err)
	}
	converseaiID := cvRow.ID

	// Cobrancas (sensitive, mode=24/7 — CHECK chk_sensitive_no_peak forbids
	// peak for sensitive). Use direct INSERT so we control data_class.
	cobrancasID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO ai_gateway.tenants
			(id, slug, name, data_class, status, mode)
		VALUES ($1, 'cobrancas', 'Cobranças', 'sensitive', 'active', '24/7')
	`, cobrancasID); err != nil {
		t.Fatalf("seed cobrancas: %v", err)
	}

	// API key for ConverseAI — matches auth.GenerateAPIKey shape so the
	// real auth.Middleware could verify it end-to-end.
	raw, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	apiKeyRow, err := q.InsertAPIKey(ctx, gen.InsertAPIKeyParams{
		TenantID:      converseaiID,
		KeyHash:       hash,
		KeyLookupHash: lookupHash,
		KeyPrefix:     prefix,
		DataClass:     string(auth.DataClassNormal),
	})
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}

	// Active prices for qwen3.5-27b input + output (USD per token).
	// Seed migration 0015 may already have inserted rows with valid_to=NULL
	// — ExpireActivePrice closes them first to keep our test values current.
	for _, pr := range []struct {
		model, provider, unit string
		cost                  string
	}{
		{"qwen3.5-27b", "openrouter-fireworks", "input_token", "0.00000020"},
		{"qwen3.5-27b", "openrouter-fireworks", "output_token", "0.00000060"},
	} {
		if err := q.ExpireActivePrice(ctx, gen.ExpireActivePriceParams{
			Model: pr.model, Provider: pr.provider, Unit: pr.unit,
		}); err != nil {
			t.Fatalf("expire prior price %s/%s/%s: %v", pr.model, pr.provider, pr.unit, err)
		}
		var cost pgtype.Numeric
		if err := cost.Scan(pr.cost); err != nil {
			t.Fatalf("parse price %q: %v", pr.cost, err)
		}
		if _, err := q.InsertPrice(ctx, gen.InsertPriceParams{
			Model: pr.model, Provider: pr.provider, Unit: pr.unit,
			UnitCostUsd: cost,
		}); err != nil {
			t.Fatalf("insert price %s/%s/%s: %v", pr.model, pr.provider, pr.unit, err)
		}
	}

	// FX USD/BRL rate — seeded by migration 0015 already; expire + reinsert
	// with a test-known rate so ComputeCostBRL math is predictable.
	if err := q.ExpireActiveFX(ctx, "USD/BRL"); err != nil {
		t.Fatalf("expire fx: %v", err)
	}
	var fxRate pgtype.Numeric
	if err := fxRate.Scan("5.10"); err != nil {
		t.Fatalf("parse fx: %v", err)
	}
	if _, err := q.InsertFX(ctx, gen.InsertFXParams{
		CurrencyPair: "USD/BRL", Rate: fxRate,
	}); err != nil {
		t.Fatalf("insert fx: %v", err)
	}

	// Admin key: bcrypt cost 10. The SHA-256 lookup hash is what
	// GetAdminKeyByLookupHash queries on; bcrypt verifies the raw key
	// during Middleware request processing.
	adminRaw := "ifix_admin_" + uuid.NewString()
	sum := sha256.Sum256([]byte(adminRaw))
	bhash, err := bcrypt.GenerateFromPassword([]byte(adminRaw), 10)
	if err != nil {
		t.Fatalf("bcrypt admin key: %v", err)
	}
	suffix := adminRaw
	if len(suffix) > 4 {
		suffix = suffix[len(suffix)-4:]
	}
	adminPrefix := "ifix_admin_****" + suffix
	if _, err := q.InsertAdminKey(ctx, gen.InsertAdminKeyParams{
		KeyLookupHash: sum[:],
		KeyHash:       string(bhash),
		KeyPrefix:     adminPrefix,
		Label:         "phase4-test",
	}); err != nil {
		t.Fatalf("insert admin key: %v", err)
	}

	return seededPhase4{
		ConverseAITenantID: converseaiID,
		CobrancasTenantID:  cobrancasID,
		ConverseAIAPIKeyID: apiKeyRow.ID,
		ConverseAIAPIKey:   raw,
		AdminKeyRaw:        adminRaw,
	}
}

// numericFromString turns a fixed-point string (e.g., "1.234567") into a
// pgtype.Numeric suitable for INSERT binding. Used by tests that write
// billing_events rows directly.
func numericFromString(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan numeric %q: %v", s, err)
	}
	return n
}

// numericToFloat is the inverse convenience — reads a pgtype.Numeric into a
// plain float64 for assertion. Used only in tests; silently returns 0 on
// any conversion failure (callers assert on the numeric value anyway).
func numericToFloat(n pgtype.Numeric) float64 {
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return 0
	}
	return f.Float64
}

// bigRatToFloat converts a big.Rat to a float64 — used in a couple of
// billing assertions where math/big intermediate values are compared.
func bigRatToFloat(r *big.Rat) float64 {
	f, _ := r.Float64()
	return f
}
