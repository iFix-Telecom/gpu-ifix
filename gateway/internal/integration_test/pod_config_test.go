//go:build integration

// Phase 17 Plan 01 Task 2 — Migration 0031 (pod_config) round-trip.
//
// Migration 0031 (gateway/db/migrations/0031_create_pod_config.sql) creates the
// single-row ai_gateway.pod_config table EMPTY (no seed INSERT — the env->DB
// seed runs in Go at boot via SeedPodConfig, Plan 17-03). This test proves the
// generated query contract the downstream loader + admin write endpoint depend on:
//
//  1. table exists + EMPTY before seed  (GetPodConfig → pgx.ErrNoRows)
//  2. SeedPodConfig populates the row    (GetPodConfig returns the seeded values)
//  3. a SECOND SeedPodConfig is a NO-OP   (ON CONFLICT DO NOTHING — T-17-01 idempotent)
//  4. UpdatePodConfigField* / Bound* mutate the row + advance updated_at
//
// NOTIFY delivery is intentionally NOT asserted here — that is covered by the
// LISTEN test in Plan 17-02.
package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// seedParams returns a representative SeedPodConfig argument set: prod-ish hot
// values + the RESEARCH Validation-Bounds defaults for the 10 numeric fields.
func seedParams(t *testing.T) gen.SeedPodConfigParams {
	t.Helper()
	return gen.SeedPodConfigParams{
		VastMachineBlocklist: []int64{55942, 45778},
		VastMachineAllowlist: []int64{7970, 12863},
		CapPrimary:           numericFromString(t, "0.30"),
		CapFallback:          numericFromString(t, "0.60"),
		HostID:               0,
		RejectPrivateIp:      true,
		ColdstartBudgetS:     3600,
		PortBindBudgetS:      120,
		FailureCooldownS:     300,
		MonthlyBudgetBrl:     numericFromString(t, "2400"),
		ScheduleUpHour:       9,
		ScheduleDownHour:     17,
		ScheduleDays:         []string{"mon", "tue", "wed", "thu", "fri"},
		GraceRampDownS:       300,
		ProvisionLeadS:       1800,
		ScheduleDisabled:     false,
		// RESEARCH bound defaults.
		CapPrimaryMin:       numericFromString(t, "0.10"),
		CapPrimaryMax:       numericFromString(t, "1.50"),
		CapFallbackMin:      numericFromString(t, "0.10"),
		CapFallbackMax:      numericFromString(t, "1.50"),
		ColdstartBudgetSMin: 300,
		ColdstartBudgetSMax: 5400,
		PortBindBudgetSMin:  30,
		PortBindBudgetSMax:  600,
		FailureCooldownSMin: 60,
		FailureCooldownSMax: 1800,
		MonthlyBudgetBrlMin: numericFromString(t, "0"),
		MonthlyBudgetBrlMax: numericFromString(t, "100000"),
		ScheduleUpHourMin:   0,
		ScheduleUpHourMax:   23,
		ScheduleDownHourMin: 0,
		ScheduleDownHourMax: 23,
		GraceRampDownSMin:   0,
		GraceRampDownSMax:   1800,
		ProvisionLeadSMin:   0,
		ProvisionLeadSMax:   7200,
	}
}

func equalInt64Slice(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPodConfig_SeedIdempotentAndUpdate exercises the full migration-0031
// query contract in one ordered sequence (empty → seed → re-seed no-op →
// update). Sub-steps share one freshSchema container so the single-row table's
// state carries across them deterministically.
func TestPodConfig_SeedIdempotentAndUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	q := gen.New(pool)

	// (1) table exists + EMPTY before seed.
	if _, err := q.GetPodConfig(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetPodConfig before seed: err = %v, want pgx.ErrNoRows (table must be created EMPTY)", err)
	}

	// (2) SeedPodConfig populates the single row.
	if err := q.SeedPodConfig(ctx, seedParams(t)); err != nil {
		t.Fatalf("SeedPodConfig (first): %v", err)
	}
	row, err := q.GetPodConfig(ctx)
	if err != nil {
		t.Fatalf("GetPodConfig after seed: %v", err)
	}
	if row.ID != true {
		t.Errorf("seeded row id = %v, want true (single-row guard)", row.ID)
	}
	if !equalInt64Slice(row.VastMachineBlocklist, []int64{55942, 45778}) {
		t.Errorf("seeded blocklist = %v, want [55942 45778]", row.VastMachineBlocklist)
	}
	if got := numericToFloat(row.CapPrimary); got != 0.30 {
		t.Errorf("seeded cap_primary = %v, want 0.30", got)
	}
	if row.HostID != 0 {
		t.Errorf("seeded host_id = %d, want 0", row.HostID)
	}
	if row.ScheduleUpHour != 9 || row.ScheduleDownHour != 17 {
		t.Errorf("seeded schedule hours = %d/%d, want 9/17", row.ScheduleUpHour, row.ScheduleDownHour)
	}
	if got := numericToFloat(row.CapPrimaryMax); got != 1.50 {
		t.Errorf("seeded cap_primary_max bound = %v, want 1.50", got)
	}
	if row.ProvisionLeadSMax != 7200 {
		t.Errorf("seeded provision_lead_s_max bound = %d, want 7200", row.ProvisionLeadSMax)
	}

	// (3) a SECOND seed with DIFFERENT values is a NO-OP (ON CONFLICT DO NOTHING).
	diff := seedParams(t)
	diff.VastMachineBlocklist = []int64{1, 2, 3}
	diff.CapPrimary = numericFromString(t, "0.99")
	diff.HostID = 999
	diff.ScheduleUpHour = 1
	if err := q.SeedPodConfig(ctx, diff); err != nil {
		t.Fatalf("SeedPodConfig (second): %v", err)
	}
	after, err := q.GetPodConfig(ctx)
	if err != nil {
		t.Fatalf("GetPodConfig after second seed: %v", err)
	}
	if !equalInt64Slice(after.VastMachineBlocklist, []int64{55942, 45778}) {
		t.Errorf("blocklist after idempotent re-seed = %v, want UNCHANGED [55942 45778] (T-17-01)", after.VastMachineBlocklist)
	}
	if got := numericToFloat(after.CapPrimary); got != 0.30 {
		t.Errorf("cap_primary after idempotent re-seed = %v, want UNCHANGED 0.30 (T-17-01)", got)
	}
	if after.HostID != 0 {
		t.Errorf("host_id after idempotent re-seed = %d, want UNCHANGED 0 (T-17-01)", after.HostID)
	}
	if after.ScheduleUpHour != 9 {
		t.Errorf("schedule_up_hour after idempotent re-seed = %d, want UNCHANGED 9 (T-17-01)", after.ScheduleUpHour)
	}
	if !after.UpdatedAt.Equal(row.UpdatedAt) {
		t.Errorf("updated_at changed on a no-op re-seed (%v → %v); ON CONFLICT DO NOTHING must not touch the row",
			row.UpdatedAt, after.UpdatedAt)
	}

	// (4) UpdatePodConfigField* + UpdatePodConfigBound* mutate + advance updated_at.
	// Small sleep guarantees the post-update statement_timestamp() is strictly
	// later than the seed's (timestamptz has microsecond resolution).
	time.Sleep(5 * time.Millisecond)
	if err := q.UpdatePodConfigFieldBlocklist(ctx, []int64{99, 88}); err != nil {
		t.Fatalf("UpdatePodConfigFieldBlocklist: %v", err)
	}
	if err := q.UpdatePodConfigBoundCapPrimaryMax(ctx, numericFromString(t, "2.00")); err != nil {
		t.Fatalf("UpdatePodConfigBoundCapPrimaryMax: %v", err)
	}
	updated, err := q.GetPodConfig(ctx)
	if err != nil {
		t.Fatalf("GetPodConfig after update: %v", err)
	}
	if !equalInt64Slice(updated.VastMachineBlocklist, []int64{99, 88}) {
		t.Errorf("blocklist after UpdatePodConfigFieldBlocklist = %v, want [99 88]", updated.VastMachineBlocklist)
	}
	if got := numericToFloat(updated.CapPrimaryMax); got != 2.00 {
		t.Errorf("cap_primary_max after UpdatePodConfigBoundCapPrimaryMax = %v, want 2.00", got)
	}
	if !updated.UpdatedAt.After(after.UpdatedAt) {
		t.Errorf("updated_at did not advance after an UPDATE: %v → %v", after.UpdatedAt, updated.UpdatedAt)
	}
}
