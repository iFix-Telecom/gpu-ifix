// Package primary (export_test.go): test-only export of unexported
// symbols for integration tests that need to safely mutate Reconciler
// internals.
//
// WARNING 4 closure: ScheduleRule is constructed via ParseScheduleEnv at
// boot and is documented as IMMUTABLE in production. Integration tests
// for cancel-in-flight (Plan 06.6-10 Task 2) need to flip the rule from
// "in peak" to "out of peak" mid-test to drive Layer 1+2+3 cancellation
// — but doing this via direct field write is a data race vs the schedule
// loop. SetScheduleRuleForTest replaces the value atomically (well, under
// the same single-writer assumption ScheduleRule already carries) and
// only exists in the _test build. The production binary has no such
// setter, preserving the immutability contract.
package primary

import "testing"

// SetScheduleRuleForTest replaces the Reconciler's ScheduleRule with the
// given value. test-only — ScheduleRule is immutable in production
// (constructed once via ParseScheduleEnv at boot). Per WARNING 4
// revision: integration tests use this to drive cancel-in-flight
// (window-closes-mid-provision) without violating the production
// immutability invariant.
//
// MUST be called from a single goroutine (the test goroutine). The
// Reconciler reads r.rule from the schedule loop goroutine; tests that
// use this helper SHOULD stop or pause the reconciler before mutating
// the rule, or rely on the next schedule tick observing the new value.
func SetScheduleRuleForTest(t *testing.T, r *Reconciler, rule ScheduleRule) {
	t.Helper()
	r.rule = rule
}
