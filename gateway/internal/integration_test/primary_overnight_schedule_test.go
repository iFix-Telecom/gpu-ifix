//go:build integration

// Phase 6.6 Plan 06.6-10 Task 2 — reviews consensus action #5
// overnight-wrap day-filter semantics (Plan 06.6-05 schedule.IsInPeak).
//
// 06.6-REVIEWS.md action #5 — the load-bearing fix: for an overnight
// schedule like UP=22 DOWN=8 with `Days={mon: true}`, Tuesday 02:00 IS
// in peak (the wrap originator is Monday 22:00, and Monday is enabled).
// With only Tuesday enabled, Tuesday 02:00 is NOT in peak (Tuesday's
// own UpHour=22 hasn't fired yet, Monday's bit is OFF). The "wrap
// originator" semantics are the load-bearing day-filter fix.
//
// Four sub-tests fully cover the wrap matrix:
//
//  1. MonOnly_TueEarly_IsInPeak     (Days={mon}, now=Tue 02:00 → true)
//  2. TueOnly_TueEarly_IsNotInPeak  (Days={tue}, now=Tue 02:00 → false)
//  3. MonOnly_TueLate_IsNotInPeak   (Days={mon}, now=Tue 23:00 → false)
//  4. TueOnly_TueLate_IsInPeak      (Days={tue}, now=Tue 23:00 → true)
//
// These tests are pure ScheduleRule.IsInPeak invocations — no
// testcontainers, no miniredis, no DB. They live in the integration_test
// package to keep all primary tests grouped (and they benefit from the
// `integration` build tag for parallel CI flow), but the build cost is
// trivial.
package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

// overnightTestRule returns a ScheduleRule with UP=22 DOWN=8 overnight
// wrap and the given Days filter. Timezone fixed to a Brazil-style
// BRT (UTC-3) so the wall-clock arithmetic is deterministic. Disabled
// is always false — the kill-switch is exercised in a different test.
func overnightTestRule(days map[time.Weekday]bool) primary.ScheduleRule {
	brt := time.FixedZone("BRT", -3*3600)
	return primary.ScheduleRule{
		Timezone: brt,
		UpHour:   22,
		DownHour: 8,
		Days:     days,
		Disabled: false,
	}
}

// TestOvernightSchedule_MonOnly_TueEarly_IsInPeak — reviews #5
// load-bearing case: Days={mon}, now=Tue 02:00 BRT. The Monday 22:00 →
// Tuesday 08:00 wrap window IS active because Monday's bit is enabled.
// IsInPeak must return TRUE.
func TestOvernightSchedule_MonOnly_TueEarly_IsInPeak(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	rule := overnightTestRule(map[time.Weekday]bool{time.Monday: true})
	now := time.Date(2026, 5, 12, 2, 0, 0, 0, brt) // Tue 02:00
	require.True(t, rule.IsInPeak(now),
		"Days={mon}, now=Tue 02:00 BRT must be IN PEAK (wrap originator = Monday)")
}

// TestOvernightSchedule_TueOnly_TueEarly_IsNotInPeak — same wall-clock,
// flipped Days filter: Days={tue}, now=Tue 02:00. Tuesday's own
// UpHour=22 hasn't fired yet, Monday's bit is OFF, so IsInPeak must
// return FALSE.
func TestOvernightSchedule_TueOnly_TueEarly_IsNotInPeak(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	rule := overnightTestRule(map[time.Weekday]bool{time.Tuesday: true})
	now := time.Date(2026, 5, 12, 2, 0, 0, 0, brt) // Tue 02:00
	require.False(t, rule.IsInPeak(now),
		"Days={tue}, now=Tue 02:00 BRT must NOT be in peak (wrap originator = Mon, OFF)")
}

// TestOvernightSchedule_MonOnly_TueLate_IsNotInPeak — Days={mon},
// now=Tue 23:00. We are PAST Tuesday's UpHour=22 but Tuesday's bit is
// OFF, so the Tuesday wrap window is not active. Monday's wrap window
// ended at Tue 08:00. IsInPeak must return FALSE.
func TestOvernightSchedule_MonOnly_TueLate_IsNotInPeak(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	rule := overnightTestRule(map[time.Weekday]bool{time.Monday: true})
	now := time.Date(2026, 5, 12, 23, 0, 0, 0, brt) // Tue 23:00
	require.False(t, rule.IsInPeak(now),
		"Days={mon}, now=Tue 23:00 BRT must NOT be in peak (Tuesday's bit OFF for late-evening wrap)")
}

// TestOvernightSchedule_TueOnly_TueLate_IsInPeak — Days={tue}, now=Tue
// 23:00. Tuesday's UpHour=22 has fired, Tuesday's bit is ON, so we are
// INSIDE Tuesday's wrap window (Tue 22:00 → Wed 08:00). IsInPeak must
// return TRUE.
func TestOvernightSchedule_TueOnly_TueLate_IsInPeak(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	rule := overnightTestRule(map[time.Weekday]bool{time.Tuesday: true})
	now := time.Date(2026, 5, 12, 23, 0, 0, 0, brt) // Tue 23:00
	require.True(t, rule.IsInPeak(now),
		"Days={tue}, now=Tue 23:00 BRT must be IN PEAK (Tuesday's own wrap originator)")
}
