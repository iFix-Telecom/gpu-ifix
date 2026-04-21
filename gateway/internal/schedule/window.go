// Package schedule: pure time-of-day helpers for peak-window evaluation.
//
// Only the Hour and Minute components of the inputs are consulted; the
// date portion is irrelevant. Wrap-around ranges (e.g. 22:00-08:00 for
// overnight peak) are supported.
package schedule

import "time"

// InWindow reports whether `now` falls inside the half-open interval
// [start, end). Both `start` and `end` are time-of-day values. Wrap-around
// is detected automatically: when startMin > endMin the window crosses
// midnight, so anything ≥ start OR < end qualifies.
//
// Callers must convert `now` to the tenant's *time.Location before calling
// (schedule/policy.go does this via now.In(cfg.Location)).
func InWindow(now, start, end time.Time) bool {
	nowMin := now.Hour()*60 + now.Minute()
	startMin := start.Hour()*60 + start.Minute()
	endMin := end.Hour()*60 + end.Minute()
	if startMin <= endMin {
		return nowMin >= startMin && nowMin < endMin
	}
	// Wrap-around (overnight window like 22:00-08:00).
	return nowMin >= startMin || nowMin < endMin
}
