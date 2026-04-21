//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/quota"
)

// TestQuotaDailyRolloverBRT — SC-1 quota rollover invariant.
//
// The daily-quota check reads the CURRENT date row from usage_counters
// (`(now() AT TIME ZONE 'America/Sao_Paulo')::date`). Yesterday's row must
// NEVER block today's request — that is the rollover contract. This test:
//
//  1. Seeds a row for YESTERDAY with tokens_in+out = 101 (above the 100
//     daily limit). CheckQuotaToday MUST return nil (no today row).
//  2. Inserts a TODAY row also at 101 → CheckQuotaToday MUST return
//     ErrQuotaExceededDailyTokens.
//
// The fake clock path is rejected here — we use the real clock + today's
// date, which is the same strategy Phase 4's CheckQuotaToday uses in
// production. A fake clock would also test the production SQL
// `(now() AT TIME ZONE 'America/Sao_Paulo')::date` filter.
func TestQuotaDailyRolloverBRT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	// Set daily_quota_tokens = 100.
	if _, err := pool.Exec(ctx,
		`UPDATE ai_gateway.tenants SET daily_quota_tokens = 100 WHERE id = $1`,
		seed.ConverseAITenantID); err != nil {
		t.Fatal(err)
	}

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Now().In(loc)
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	today := now.Format("2006-01-02")

	// Seed YESTERDAY's row over-limit.
	if _, err := pool.Exec(ctx, `
		INSERT INTO ai_gateway.usage_counters
			(tenant_id, date, tokens_in, tokens_out, requests_count)
		VALUES ($1, $2::date, 99, 2, 1)
	`, seed.ConverseAITenantID, yesterday); err != nil {
		t.Fatalf("seed yesterday row: %v", err)
	}

	checker := quota.NewQuotaChecker(gen.New(pool), discardLogger())
	limits := quota.QuotaLimits{
		DailyTokens:         100,
		MonthlyTokens:       1_000_000,
		DailyAudioMinutes:   600,
		MonthlyAudioMinutes: 18000,
		DailyEmbeds:         100_000,
		MonthlyEmbeds:       3_000_000,
	}

	// Today row absent — today must pass even though yesterday is over.
	if err := checker.CheckQuotaToday(ctx, seed.ConverseAITenantID, limits); err != nil {
		t.Errorf("rollover violation: yesterday=over must NOT block today; got %v", err)
	}

	// Now seed TODAY's row over the limit — daily check must block.
	if _, err := pool.Exec(ctx, `
		INSERT INTO ai_gateway.usage_counters
			(tenant_id, date, tokens_in, tokens_out, requests_count)
		VALUES ($1, $2::date, 99, 2, 1)
	`, seed.ConverseAITenantID, today); err != nil {
		t.Fatalf("seed today row: %v", err)
	}
	err := checker.CheckQuotaToday(ctx, seed.ConverseAITenantID, limits)
	if !errors.Is(err, quota.ErrQuotaExceededDailyTokens) {
		t.Errorf("want ErrQuotaExceededDailyTokens once today is over, got %v", err)
	}
}
