// Package primary (schedule.go): pure schedule rule evaluator for the
// Phase 6.6 primary-pod reconciler (Plan 06.6-06a consumer).
//
// ScheduleRule is constructed once at boot via ParseScheduleEnv(cfg) and
// is immutable thereafter. The two hot-path methods consumed by the
// reconciler are:
//
//   - IsInPeak(now) bool — pod SHOULD be Ready right now?
//     (reconciler.evaluateReady drain trigger when this turns false)
//
//   - ShouldBeProvisioned(now) bool — pod SHOULD be provisioned now?
//     Returns true throughout the active peak window AND for
//     ProvisionLeadS seconds BEFORE UpHour fires, so the pod is Ready
//     by UpHour (reviews consensus action #8). 25–30min cold-start
//     reality forces the pre-warm offset.
//     (reconciler.evaluateAsleep provisioning trigger)
//
// IsInPeak handles overnight wrap-around with the DAY-FILTER fix from
// reviews consensus action #5: for a schedule like UP=22 DOWN=8 with
// only Monday enabled, Tuesday 02:00 IS in peak — the active window
// originates at Monday 22:00 (the WRAP ORIGINATOR), and Monday's day-bit
// is the one to consult. With only Tuesday enabled, Tuesday 02:00 is
// NOT in peak — Tuesday's own UpHour=22 hasn't fired yet, and Monday's
// bit is off.
//
// Pitfall #4 fail-fast: ParseScheduleEnv returns an error if
// time.LoadLocation(cfg.PrimaryPodScheduleTimezone) fails. The gateway
// MUST surface this at boot rather than silently falling back to UTC
// (which would shift the entire peak window by hours).
//
// Wave 0 orthogonality: schedule.go is pure Go stdlib `time` logic —
// independent of orchestration mechanism (supervisord vs DinD vs
// single-process). Wave 0 decisions (custom multi-stage image, b9191,
// supervisord) live in Plan 04 and never touch this file.
package primary

import (
	"fmt"
	"strings"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
)

// weekdayFromCSV maps the lowercase 3-letter day tokens used by the
// PRIMARY_POD_SCHEDULE_DAYS CSV env var to time.Weekday values.
// Tokens outside this map are silently dropped by ParseScheduleEnv
// (TestParseScheduleEnv_InvalidDayIgnored).
var weekdayFromCSV = map[string]time.Weekday{
	"sun": time.Sunday,
	"mon": time.Monday,
	"tue": time.Tuesday,
	"wed": time.Wednesday,
	"thu": time.Thursday,
	"fri": time.Friday,
	"sat": time.Saturday,
}

// ScheduleRule is the immutable schedule evaluator. Construct via
// ParseScheduleEnv at boot; never mutate the struct afterwards (the
// reconciler reads it from a single goroutine but the lack of mutation
// makes it safe for read from any goroutine).
type ScheduleRule struct {
	// Timezone is the resolved *time.Location used for hour/weekday
	// arithmetic. Resolved via time.LoadLocation at boot — fail-fast on
	// error (Pitfall #4).
	Timezone *time.Location
	// UpHour is the hour-of-day at which the pod should be Ready
	// (range 0..23).
	UpHour int
	// DownHour is the hour-of-day at which the pod should drain
	// (range 0..23). When DownHour <= UpHour, the schedule wraps
	// across midnight.
	DownHour int
	// Days is the day-of-week filter. Days[Monday]==true means the peak
	// window applies on Mondays. For overnight wrap windows, the
	// WRAP ORIGINATOR's day-bit governs the early-morning hours of the
	// FOLLOWING day (reviews consensus action #5).
	Days map[time.Weekday]bool
	// GraceRampDownS is the drain timeout in seconds — how long the
	// reconciler waits for in-flight requests to complete before
	// destroying the pod. Surfaced via getter for the reconciler;
	// schedule.go itself does not consume it.
	GraceRampDownS int
	// ProvisionLeadS is the pre-warm offset in seconds (reviews
	// consensus action #8). ShouldBeProvisioned returns true for this
	// many seconds before UpHour fires.
	ProvisionLeadS int
	// Disabled is the kill-switch. When true, IsInPeak AND
	// ShouldBeProvisioned always return false regardless of time.
	// Default true per WAVE0-GATES Decision 5 soak gate — operator
	// flips to false after UAT GREEN.
	Disabled bool
}

// ParseScheduleEnv reads the PrimaryPodSchedule* fields from cfg and
// returns an immutable ScheduleRule.
//
// Fail-fast contract (Pitfall #4): if time.LoadLocation fails on the
// configured timezone, the error is returned so main.go can crash the
// gateway at boot. Silently falling back to UTC would shift the peak
// window by 3+ hours, which is worse than refusing to start.
func ParseScheduleEnv(cfg config.Config) (ScheduleRule, error) {
	loc, err := time.LoadLocation(cfg.PrimaryPodScheduleTimezone)
	if err != nil {
		return ScheduleRule{}, fmt.Errorf("primary schedule: invalid timezone %q: %w",
			cfg.PrimaryPodScheduleTimezone, err)
	}
	days := make(map[time.Weekday]bool, 7)
	for _, raw := range cfg.PrimaryPodScheduleDays {
		token := strings.ToLower(strings.TrimSpace(raw))
		if token == "" {
			continue
		}
		wd, ok := weekdayFromCSV[token]
		if !ok {
			// Silently drop unknown tokens — operator misconfiguration
			// surfaces via the "weekend excluded" behaviour at runtime
			// rather than a boot crash. The config CSV is documented
			// as 3-letter lowercase abbreviations.
			continue
		}
		days[wd] = true
	}
	return ScheduleRule{
		Timezone:       loc,
		UpHour:         cfg.PrimaryPodScheduleUpHour,
		DownHour:       cfg.PrimaryPodScheduleDownHour,
		Days:           days,
		GraceRampDownS: cfg.PrimaryPodScheduleGraceRampDownSeconds,
		ProvisionLeadS: cfg.PrimaryPodScheduleProvisionLeadSeconds,
		Disabled:       cfg.PrimaryPodScheduleDisabled,
	}, nil
}

// ParseScheduleFromSnapshot builds an evaluable primary.ScheduleRule from the
// 6 HOT schedule fields of a LIVE pod_config snapshot (Phase 17 POD-CFG-04),
// using the pre-resolved STRUCTURAL timezone (loc). It is the cycle-free bridge
// from the data-only podconfig.ScheduleRule mirror into this package's rule
// with the IsInPeak / ShouldBeProvisioned / ShouldStayUp methods.
//
// loc is resolved ONCE at boot (config.Load fail-fast + the loader's NewLoader)
// and is structural (D-02/D-03a) — it never changes at runtime, so this
// function never re-runs time.LoadLocation. The only error paths are a nil loc
// (programmer error) or an out-of-range up/down hour; both let the caller keep
// the previous good rule (never swap a broken rule into the live path, T-17-06).
// Day-token parsing mirrors ParseScheduleEnv (unknown tokens silently dropped).
func ParseScheduleFromSnapshot(cfg podconfig.PodConfig, loc *time.Location) (ScheduleRule, error) {
	if loc == nil {
		return ScheduleRule{}, fmt.Errorf("primary schedule from snapshot: nil structural timezone")
	}
	if cfg.ScheduleUpHour < 0 || cfg.ScheduleUpHour > 23 {
		return ScheduleRule{}, fmt.Errorf("primary schedule from snapshot: up_hour out of range [0,23]: %d", cfg.ScheduleUpHour)
	}
	if cfg.ScheduleDownHour < 0 || cfg.ScheduleDownHour > 23 {
		return ScheduleRule{}, fmt.Errorf("primary schedule from snapshot: down_hour out of range [0,23]: %d", cfg.ScheduleDownHour)
	}
	days := make(map[time.Weekday]bool, 7)
	for _, raw := range cfg.ScheduleDays {
		token := strings.ToLower(strings.TrimSpace(raw))
		if token == "" {
			continue
		}
		wd, ok := weekdayFromCSV[token]
		if !ok {
			continue
		}
		days[wd] = true
	}
	return ScheduleRule{
		Timezone:       loc,
		UpHour:         cfg.ScheduleUpHour,
		DownHour:       cfg.ScheduleDownHour,
		Days:           days,
		GraceRampDownS: cfg.GraceRampDownS,
		ProvisionLeadS: cfg.ProvisionLeadS,
		Disabled:       cfg.ScheduleDisabled,
	}, nil
}

// IsInPeak reports whether `now` falls inside the active schedule window.
//
// Algorithm:
//
//   - Disabled kill-switch short-circuits to false.
//
//   - Compute hour + weekday in r.Timezone.
//
//   - Simple window (UpHour < DownHour): require Days[weekday] AND
//     UpHour <= hour < DownHour.
//
//   - Overnight wrap (UpHour >= DownHour): the window straddles
//     midnight. Two cases:
//
//   - Case A — now in [UpHour, 24): use TODAY's day-bit
//     (today is the wrap originator).
//
//   - Case B — now in [0, DownHour): use YESTERDAY's day-bit
//     (yesterday is the wrap originator — reviews
//     consensus action #5 day-filter fix).
//
//   - Anything else (DownHour <= hour < UpHour): not in peak.
//
// The reviews #5 fix is the LOAD-BEARING part of IsInPeak — without it,
// an operator setting Days={mon: true} and UP=22 DOWN=8 would silently
// see Tuesday 02:00 as NOT in peak (consulting Tuesday's bit), even
// though the Monday 22:00 → Tuesday 08:00 window IS active because
// Monday's bit IS enabled.
func (r ScheduleRule) IsInPeak(now time.Time) bool {
	if r.Disabled {
		return false
	}
	local := now.In(r.Timezone)
	hour := local.Hour()
	weekday := local.Weekday()

	if r.UpHour < r.DownHour {
		// Simple intra-day window.
		if !r.Days[weekday] {
			return false
		}
		return hour >= r.UpHour && hour < r.DownHour
	}

	// Overnight wrap (UpHour >= DownHour). The window crosses midnight.
	if hour >= r.UpHour {
		// Case A: now is in [UpHour, 24) on `weekday`. Today is the
		// wrap originator → use today's bit.
		return r.Days[weekday]
	}
	if hour < r.DownHour {
		// Case B: now is in [0, DownHour) on `weekday`. The active
		// window originated YESTERDAY at YESTERDAY 22:00 (or whatever
		// UpHour is). Yesterday is the wrap originator → use
		// yesterday's bit. (reviews consensus action #5).
		yesterday := time.Weekday((int(weekday) - 1 + 7) % 7)
		return r.Days[yesterday]
	}
	// hour is in [DownHour, UpHour) — between the morning drain and the
	// evening up — not in peak.
	return false
}

// ShouldBeProvisioned reports whether the reconciler should ensure a pod
// is provisioned RIGHT NOW. Returns true:
//
//   - throughout the active peak window (IsInPeak), AND
//   - for ProvisionLeadS seconds BEFORE the next "up" transition
//     (reviews consensus action #8 pre-warm offset).
//
// The kill-switch (Disabled=true) overrides both branches and forces
// false.
//
// Consumer: reconciler.evaluateAsleep uses this method (NOT IsInPeak)
// for the provisioning decision so the pod is Ready by UpHour given a
// 25–30min cold-start.
func (r ScheduleRule) ShouldBeProvisioned(now time.Time) bool {
	if r.Disabled {
		return false
	}
	if r.IsInPeak(now) {
		return true
	}
	nextT, kind := r.NextTransition(now)
	if kind != "up" {
		return false
	}
	lead := time.Duration(r.ProvisionLeadS) * time.Second
	delta := nextT.Sub(now)
	return delta > 0 && delta <= lead
}

// ShouldStayUp reports whether a Ready pod should REMAIN up right now (the
// keep-up / drain gate consumed by reconciler.evaluateReady). It is
// deliberately lead-aware and delegates to ShouldBeProvisioned so the
// keep-up gate is IDENTICAL to the provision gate.
//
// Why not IsInPeak (the original gate)? evaluateAsleep provisions during
// the pre-warm lead window [UpHour-lead, UpHour) via ShouldBeProvisioned,
// but IsInPeak only turns true at UpHour. A pod that finishes cold-start
// and reaches Ready INSIDE the lead window would then be drained
// immediately by an IsInPeak gate (still false) and re-provisioned on the
// next tick — flapping create→destroy every few minutes until the clock
// reaches UpHour (debug: primary-pod-flap-prewarm-window). Making the
// keep-up gate match the provision gate (ShouldBeProvisioned) removes the
// disagreement: a pre-warmed pod stays up through UpHour. After DownHour
// both gates report false, so drain still fires at window exit.
//
// The kill-switch (Disabled=true) propagates through ShouldBeProvisioned →
// false, preserving the operator force-up-under-DISABLED semantics
// (DISABLED is handled separately in evaluateReady, which short-circuits
// before reaching this gate).
func (r ScheduleRule) ShouldStayUp(now time.Time) bool {
	return r.ShouldBeProvisioned(now)
}

// NextTransition returns the next scheduled transition timestamp and its
// kind ("up" or "down"). Used by:
//
//   - ShouldBeProvisioned (lead-time arithmetic), and
//   - gatewayctl primary schedule show (operator visibility).
//
// Algorithm: walk forward day-by-day from `now` looking for the next
// transition that lands on a Days[]-enabled day (for "up") or that
// closes an active window (for "down"). Bounded to 8 lookahead days to
// guarantee termination on pathological configs (e.g. Days all false).
func (r ScheduleRule) NextTransition(now time.Time) (time.Time, string) {
	local := now.In(r.Timezone)

	// Walk up to 8 days forward.
	for offset := 0; offset < 8; offset++ {
		day := local.AddDate(0, 0, offset)
		upToday := time.Date(day.Year(), day.Month(), day.Day(), r.UpHour, 0, 0, 0, r.Timezone)
		downToday := time.Date(day.Year(), day.Month(), day.Day(), r.DownHour, 0, 0, 0, r.Timezone)

		if r.UpHour < r.DownHour {
			// Simple intra-day window.
			if r.Days[day.Weekday()] {
				if local.Before(upToday) {
					return upToday, "up"
				}
				if local.Before(downToday) {
					return downToday, "down"
				}
			}
			continue
		}
		// Overnight wrap: UpHour >= DownHour. On a wrap-day:
		//   - The "down" closing the wrap from the previous day lands
		//     at downToday.
		//   - The "up" opening today's wrap lands at upToday.
		//
		// On the FIRST iteration (offset=0), we may be sitting inside
		// a wrap that originated on `local-1` (yesterday). In that
		// case the next transition is downToday IF yesterday's bit is
		// enabled.
		if offset == 0 && local.Hour() < r.DownHour {
			yesterday := time.Weekday((int(local.Weekday()) - 1 + 7) % 7)
			if r.Days[yesterday] {
				return downToday, "down"
			}
		}
		if r.Days[day.Weekday()] {
			if local.Before(upToday) {
				return upToday, "up"
			}
			// We are inside or past upToday. The wrap closes on the
			// NEXT day at downHour (because UpHour >= DownHour →
			// downToday <= upToday).
			next := day.AddDate(0, 0, 1)
			downNext := time.Date(next.Year(), next.Month(), next.Day(), r.DownHour, 0, 0, 0, r.Timezone)
			if local.Before(downNext) {
				return downNext, "down"
			}
		}
	}
	// Pathological config (no enabled days, or window never opens).
	// Return a zero time so the caller can treat this as "never".
	return time.Time{}, ""
}
