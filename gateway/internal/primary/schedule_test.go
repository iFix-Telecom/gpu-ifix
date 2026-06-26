// Package primary (schedule_test.go): unit tests for ScheduleRule —
// peak-window evaluation + DST/timezone + overnight wrap day-filter
// (reviews consensus action #5) + pre-warm offset (reviews consensus
// action #8) + kill-switch.
//
// Test naming follows Phase 6.6 Plan 05 PLAN.md <behavior> table.
package primary

import (
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
)

// brt returns a fixed-offset BRT zone (-3h). Brazil abolished DST in 2019,
// so a fixed offset is sufficient for the documented BRT semantics. For
// genuine DST-coverage tests we use America/New_York fixtures further
// below (TestIsInPeak_DST_NewYorkSpringForward).
func brtZone() *time.Location {
	return time.FixedZone("BRT", -3*3600)
}

// makeDefaultCfg builds a config.Config carrying every PrimaryPodSchedule*
// field at the defaults defined in gateway/internal/config/config.go.
// Tests mutate the fields they exercise and call ParseScheduleEnv directly.
func makeDefaultCfg() config.Config {
	return config.Config{
		PrimaryPodScheduleTimezone:             "America/Sao_Paulo",
		PrimaryPodScheduleUpHour:               8,
		PrimaryPodScheduleDownHour:             22,
		PrimaryPodScheduleDays:                 []string{"mon", "tue", "wed", "thu", "fri"},
		PrimaryPodScheduleGraceRampDownSeconds: 300,
		PrimaryPodScheduleDisabled:             true, // WAVE0-GATES Decision 5 soak gate default
		PrimaryPodScheduleProvisionLeadSeconds: 1800,
	}
}

// --- ParseScheduleEnv ---------------------------------------------------

func TestParseScheduleEnv_DefaultsHappyPath(t *testing.T) {
	cfg := makeDefaultCfg()
	rule, err := ParseScheduleEnv(cfg)
	if err != nil {
		t.Fatalf("ParseScheduleEnv returned err=%v, want nil", err)
	}
	if rule.Timezone == nil {
		t.Fatalf("rule.Timezone is nil; expected resolved location")
	}
	if rule.Timezone.String() != "America/Sao_Paulo" {
		t.Fatalf("rule.Timezone = %q, want America/Sao_Paulo", rule.Timezone.String())
	}
	if rule.UpHour != 8 {
		t.Fatalf("rule.UpHour = %d, want 8", rule.UpHour)
	}
	if rule.DownHour != 22 {
		t.Fatalf("rule.DownHour = %d, want 22", rule.DownHour)
	}
	wantDays := map[time.Weekday]bool{
		time.Monday:    true,
		time.Tuesday:   true,
		time.Wednesday: true,
		time.Thursday:  true,
		time.Friday:    true,
	}
	for d, want := range wantDays {
		if rule.Days[d] != want {
			t.Fatalf("rule.Days[%s] = %v, want %v", d, rule.Days[d], want)
		}
	}
	if rule.Days[time.Saturday] || rule.Days[time.Sunday] {
		t.Fatalf("rule.Days weekend should be false; got sat=%v sun=%v",
			rule.Days[time.Saturday], rule.Days[time.Sunday])
	}
	if rule.GraceRampDownS != 300 {
		t.Fatalf("rule.GraceRampDownS = %d, want 300", rule.GraceRampDownS)
	}
	if rule.ProvisionLeadS != 1800 {
		t.Fatalf("rule.ProvisionLeadS = %d, want 1800", rule.ProvisionLeadS)
	}
	if !rule.Disabled {
		t.Fatalf("rule.Disabled = false, want true (WAVE0-GATES soak gate)")
	}
}

func TestParseScheduleEnv_TimezoneInvalidFailsLoad(t *testing.T) {
	cfg := makeDefaultCfg()
	cfg.PrimaryPodScheduleTimezone = "Mars/Olympus"
	_, err := ParseScheduleEnv(cfg)
	if err == nil {
		t.Fatalf("ParseScheduleEnv with invalid tz returned err=nil; want error (Pitfall #4 fail-fast)")
	}
}

func TestParseScheduleEnv_DaysFilterMonFriDefault(t *testing.T) {
	cfg := makeDefaultCfg()
	rule, err := ParseScheduleEnv(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday} {
		if !rule.Days[d] {
			t.Fatalf("Days[%s] = false, want true", d)
		}
	}
	for _, d := range []time.Weekday{time.Saturday, time.Sunday} {
		if rule.Days[d] {
			t.Fatalf("Days[%s] = true, want false (weekend exclusion)", d)
		}
	}
}

func TestParseScheduleEnv_DaysFilterCustomCSV(t *testing.T) {
	cfg := makeDefaultCfg()
	cfg.PrimaryPodScheduleDays = []string{"sat", "sun"}
	rule, err := ParseScheduleEnv(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !rule.Days[time.Saturday] {
		t.Fatalf("Days[Saturday] = false, want true")
	}
	if !rule.Days[time.Sunday] {
		t.Fatalf("Days[Sunday] = false, want true")
	}
	if rule.Days[time.Monday] {
		t.Fatalf("Days[Monday] = true, want false (not in custom csv)")
	}
}

func TestParseScheduleEnv_InvalidDayIgnored(t *testing.T) {
	cfg := makeDefaultCfg()
	cfg.PrimaryPodScheduleDays = []string{"mon", "XXX", "fri"}
	rule, err := ParseScheduleEnv(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !rule.Days[time.Monday] {
		t.Fatalf("Days[Monday] = false, want true")
	}
	if !rule.Days[time.Friday] {
		t.Fatalf("Days[Friday] = false, want true")
	}
	// "XXX" must be silently dropped — no day key created. Tue/wed/thu must
	// remain false (they were not in the CSV).
	if rule.Days[time.Tuesday] || rule.Days[time.Wednesday] || rule.Days[time.Thursday] {
		t.Fatalf("Invalid day token leaked into Days map: %+v", rule.Days)
	}
}

func TestParseScheduleEnv_ProvisionLeadSecondsRespected(t *testing.T) {
	cfg := makeDefaultCfg()
	cfg.PrimaryPodScheduleProvisionLeadSeconds = 600
	rule, err := ParseScheduleEnv(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if rule.ProvisionLeadS != 600 {
		t.Fatalf("rule.ProvisionLeadS = %d, want 600", rule.ProvisionLeadS)
	}
}

// --- IsInPeak: simple windows ------------------------------------------

// buildRule constructs an immutable ScheduleRule directly (bypassing
// ParseScheduleEnv) so tests can target IsInPeak / NextTransition with
// surgical inputs.
func buildRule(loc *time.Location, up, down int, days map[time.Weekday]bool, disabled bool, leadS int) ScheduleRule {
	return ScheduleRule{
		Timezone:       loc,
		UpHour:         up,
		DownHour:       down,
		Days:           days,
		GraceRampDownS: 300,
		ProvisionLeadS: leadS,
		Disabled:       disabled,
	}
}

func allWeekdays() map[time.Weekday]bool {
	return map[time.Weekday]bool{
		time.Monday:    true,
		time.Tuesday:   true,
		time.Wednesday: true,
		time.Thursday:  true,
		time.Friday:    true,
	}
}

func TestIsInPeak_DefaultRule(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	// Wednesday 14:00 BRT
	now := time.Date(2026, 5, 13, 14, 0, 0, 0, loc) // 2026-05-13 is Wednesday
	if now.Weekday() != time.Wednesday {
		t.Fatalf("test fixture wrong: weekday = %s, want Wednesday", now.Weekday())
	}
	if !rule.IsInPeak(now) {
		t.Fatalf("IsInPeak(Wed 14:00, UP=8 DOWN=22 weekdays) = false, want true")
	}
}

func TestIsInPeak_BeforeUp(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	now := time.Date(2026, 5, 13, 7, 0, 0, 0, loc) // Wednesday 07:00
	if rule.IsInPeak(now) {
		t.Fatalf("IsInPeak(Wed 07:00) = true, want false (before UpHour=8)")
	}
}

func TestIsInPeak_AfterDown(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	now := time.Date(2026, 5, 13, 23, 0, 0, 0, loc) // Wednesday 23:00
	if rule.IsInPeak(now) {
		t.Fatalf("IsInPeak(Wed 23:00) = true, want false (after DownHour=22)")
	}
}

// --- IsInPeak: overnight wrap ------------------------------------------

func TestIsInPeak_OvernightWindow_TuesdayLate(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 22, 8, allWeekdays(), false, 1800)
	// Tuesday 23:00 BRT — Tuesday is enabled & hour >= UpHour
	now := time.Date(2026, 5, 12, 23, 0, 0, 0, loc) // 2026-05-12 is Tuesday
	if now.Weekday() != time.Tuesday {
		t.Fatalf("fixture wrong: %s", now.Weekday())
	}
	if !rule.IsInPeak(now) {
		t.Fatalf("IsInPeak(Tue 23:00, overnight UP=22 DOWN=8 weekdays) = false, want true")
	}
}

func TestIsInPeak_OvernightWindow_BeforeUp(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 22, 8, allWeekdays(), false, 1800)
	// Tuesday 10:00: hour < UpHour=22 AND hour >= DownHour=8 → not in peak
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, loc)
	if rule.IsInPeak(now) {
		t.Fatalf("IsInPeak(Tue 10:00, overnight UP=22 DOWN=8) = true, want false (between DownHour and UpHour)")
	}
}

// reviews consensus action #5: overnight wrap day-filter semantics —
// Tuesday 02:00 with mon-only enabled IS in peak (Monday's enabled bit
// wraps).
func TestIsInPeak_OvernightWrap_MonOnly_TueEarlyIsInPeak(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 22, 8, map[time.Weekday]bool{time.Monday: true}, false, 1800)
	now := time.Date(2026, 5, 12, 2, 0, 0, 0, loc) // Tuesday 02:00
	if !rule.IsInPeak(now) {
		t.Fatalf("reviews #5: IsInPeak(Tue 02:00, mon-only) = false, want true (Monday's bit wraps)")
	}
}

// reviews consensus action #5: Tuesday 02:00 with tue-only enabled is NOT
// in peak — Tuesday's own UpHour=22 hasn't fired yet, and Monday's bit is
// off so the wrap doesn't apply.
func TestIsInPeak_OvernightWrap_TueOnly_TueEarlyIsNotInPeak(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 22, 8, map[time.Weekday]bool{time.Tuesday: true}, false, 1800)
	now := time.Date(2026, 5, 12, 2, 0, 0, 0, loc) // Tuesday 02:00
	if rule.IsInPeak(now) {
		t.Fatalf("reviews #5: IsInPeak(Tue 02:00, tue-only) = true, want false (Monday bit off; wrap doesn't apply)")
	}
}

// reviews consensus action #5: Tuesday 23:00 with tue-only enabled IS in
// peak — Tuesday's bit applies because hour >= UpHour.
func TestIsInPeak_OvernightWrap_TueOnly_TueLateIsInPeak(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 22, 8, map[time.Weekday]bool{time.Tuesday: true}, false, 1800)
	now := time.Date(2026, 5, 12, 23, 0, 0, 0, loc) // Tuesday 23:00
	if !rule.IsInPeak(now) {
		t.Fatalf("reviews #5: IsInPeak(Tue 23:00, tue-only) = false, want true (Tuesday's bit applies; hour >= UpHour)")
	}
}

// --- IsInPeak: day-filter + kill-switch --------------------------------

func TestIsInPeak_WeekendExcluded(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	// 2026-05-16 is Saturday
	now := time.Date(2026, 5, 16, 14, 0, 0, 0, loc)
	if now.Weekday() != time.Saturday {
		t.Fatalf("fixture wrong: %s", now.Weekday())
	}
	if rule.IsInPeak(now) {
		t.Fatalf("IsInPeak(Sat 14:00, weekdays-only) = true, want false")
	}
}

func TestIsInPeak_DisabledKillSwitch(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), true, 1800)
	now := time.Date(2026, 5, 13, 14, 0, 0, 0, loc) // Wednesday 14:00 — middle of peak
	if rule.IsInPeak(now) {
		t.Fatalf("IsInPeak under Disabled=true = true, want false (kill-switch overrides)")
	}
}

// --- IsInPeak: DST coverage (America/New_York) -------------------------

// Brazil abolished DST in 2019. We use America/New_York to mechanically
// verify that the rule honours the resolved *time.Location through a real
// DST jump. 2026-03-08 02:00 EST jumps to 03:00 EDT (spring forward).
func TestIsInPeak_DST_NewYorkSpringForward(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	// Sunday 2026-03-08 is the spring-forward day in the US.
	// Note: weekdays-only schedule means Sunday is excluded entirely.
	now := time.Date(2026, 3, 8, 10, 0, 0, 0, loc) // Sunday — should be false
	if rule.IsInPeak(now) {
		t.Fatalf("IsInPeak(Sun 2026-03-08 10:00 EDT) = true, want false (Sunday excluded)")
	}
	// Monday 2026-03-09 09:00 EDT is firmly inside the new EDT window.
	mon := time.Date(2026, 3, 9, 9, 0, 0, 0, loc)
	if mon.Weekday() != time.Monday {
		t.Fatalf("fixture wrong: %s", mon.Weekday())
	}
	if !rule.IsInPeak(mon) {
		t.Fatalf("IsInPeak(Mon 2026-03-09 09:00 EDT, weekdays UP=8 DOWN=22) = false, want true")
	}
}

// --- ShouldBeProvisioned (reviews #8) ----------------------------------

func TestShouldBeProvisioned_DuringActiveWindow(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	now := time.Date(2026, 5, 13, 14, 0, 0, 0, loc) // Wed 14:00 — mid-peak
	if !rule.ShouldBeProvisioned(now) {
		t.Fatalf("ShouldBeProvisioned during active window = false, want true")
	}
}

func TestShouldBeProvisioned_LeadBeforeUpHour(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800) // 30-min lead
	now := time.Date(2026, 5, 13, 7, 30, 0, 0, loc)           // Wed 07:30 (30 min before UpHour=8)
	if !rule.ShouldBeProvisioned(now) {
		t.Fatalf("ShouldBeProvisioned 30min before UpHour with lead=1800 = false, want true")
	}
}

func TestShouldBeProvisioned_BeyondLead_NotYetProvisioned(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	now := time.Date(2026, 5, 13, 7, 0, 0, 0, loc) // Wed 07:00 (60 min before UpHour=8; beyond 30-min lead)
	if rule.ShouldBeProvisioned(now) {
		t.Fatalf("ShouldBeProvisioned 60min before UpHour with lead=1800 = true, want false")
	}
}

func TestShouldBeProvisioned_RespectsLeadSecondsConfig(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 3600) // 60-min lead
	now := time.Date(2026, 5, 13, 7, 0, 0, 0, loc)            // Wed 07:00 — 60 min before UpHour, exactly within 1h lead
	if !rule.ShouldBeProvisioned(now) {
		t.Fatalf("ShouldBeProvisioned 60min before UpHour with lead=3600 = false, want true (within 1h lead)")
	}
}

func TestShouldBeProvisioned_DisabledKillSwitch(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), true, 1800)
	now := time.Date(2026, 5, 13, 14, 0, 0, 0, loc) // mid-peak
	if rule.ShouldBeProvisioned(now) {
		t.Fatalf("ShouldBeProvisioned with Disabled=true = true, want false (kill-switch)")
	}
}

// --- ShouldStayUp (pre-warm flap fix) ----------------------------------
//
// Regression for primary-pod-flap-prewarm-window: the keep-up/drain gate
// MUST be lead-aware (mirror ShouldBeProvisioned), otherwise a pod that
// reaches Ready inside the pre-warm lead window [UpHour-lead, UpHour) is
// immediately drained because IsInPeak is still false → provision/drain
// disagree → flap. ShouldStayUp must be true throughout that lead window.

func TestShouldStayUp_DuringPeak(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 9, 17, allWeekdays(), false, 1800)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, loc) // Wed 12:00 — mid-peak
	if !rule.ShouldStayUp(now) {
		t.Fatalf("ShouldStayUp during active peak = false, want true")
	}
}

func TestShouldStayUp_LeadWindowBeforeUpHour(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 9, 17, allWeekdays(), false, 1800) // 30-min lead
	now := time.Date(2026, 5, 13, 8, 45, 0, 0, loc)           // Wed 08:45 — 15 min before UpHour=9
	if !rule.ShouldStayUp(now) {
		t.Fatalf("ShouldStayUp 15min before UpHour with lead=1800 = false, want true (pre-warm keep-up)")
	}
}

func TestShouldStayUp_AfterDownHour(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 9, 17, allWeekdays(), false, 1800)
	now := time.Date(2026, 5, 13, 17, 30, 0, 0, loc) // Wed 17:30 — past DownHour=17
	if rule.ShouldStayUp(now) {
		t.Fatalf("ShouldStayUp after DownHour = true, want false (window exited → drain)")
	}
}

func TestShouldStayUp_BeyondLead(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 9, 17, allWeekdays(), false, 1800)
	now := time.Date(2026, 5, 13, 7, 0, 0, 0, loc) // Wed 07:00 — 2h before UpHour, beyond 30-min lead
	if rule.ShouldStayUp(now) {
		t.Fatalf("ShouldStayUp 2h before UpHour with lead=1800 = true, want false (beyond lead)")
	}
}

func TestShouldStayUp_DisabledKillSwitch(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 9, 17, allWeekdays(), true, 1800)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, loc) // mid-peak but disabled
	if rule.ShouldStayUp(now) {
		t.Fatalf("ShouldStayUp with Disabled=true = true, want false (kill-switch)")
	}
}

// --- NextTransition ----------------------------------------------------

func TestNextTransition_BeforePeakReturnsUp(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	now := time.Date(2026, 5, 13, 6, 0, 0, 0, loc) // Wed 06:00
	tt, kind := rule.NextTransition(now)
	if kind != "up" {
		t.Fatalf("NextTransition before peak: kind = %q, want up", kind)
	}
	want := time.Date(2026, 5, 13, 8, 0, 0, 0, loc)
	if !tt.Equal(want) {
		t.Fatalf("NextTransition before peak: tt = %v, want %v", tt, want)
	}
}

func TestNextTransition_DuringPeakReturnsDown(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	now := time.Date(2026, 5, 13, 14, 0, 0, 0, loc) // Wed 14:00
	tt, kind := rule.NextTransition(now)
	if kind != "down" {
		t.Fatalf("NextTransition during peak: kind = %q, want down", kind)
	}
	want := time.Date(2026, 5, 13, 22, 0, 0, 0, loc)
	if !tt.Equal(want) {
		t.Fatalf("NextTransition during peak: tt = %v, want %v", tt, want)
	}
}

func TestNextTransition_WeekendSkipped(t *testing.T) {
	loc := brtZone()
	rule := buildRule(loc, 8, 22, allWeekdays(), false, 1800)
	// Saturday 14:00 — next "up" should be Monday 08:00
	now := time.Date(2026, 5, 16, 14, 0, 0, 0, loc) // 2026-05-16 Saturday
	if now.Weekday() != time.Saturday {
		t.Fatalf("fixture wrong: %s", now.Weekday())
	}
	tt, kind := rule.NextTransition(now)
	if kind != "up" {
		t.Fatalf("NextTransition weekend: kind = %q, want up", kind)
	}
	want := time.Date(2026, 5, 18, 8, 0, 0, 0, loc) // Monday 2026-05-18 08:00
	if !tt.Equal(want) {
		t.Fatalf("NextTransition weekend: tt = %v, want %v", tt, want)
	}
}
