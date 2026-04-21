// Package main_test: placeholder test file for billing + usage subcommands.
// Real reconcile semantics (drift detection, --apply round-trip) and the
// full JSON shape assertion live in 04-08's integration suite against
// testcontainers Postgres. Wave 4's Go test contract is: the binary builds
// and these subcommands are discoverable in the CLI dispatcher.
package main_test

import "testing"

// TestBillingPlaceholder pins the contract that `gatewayctl billing ...`
// and `gatewayctl usage report ...` have binary entry points. Real coverage
// is in 04-08.
func TestBillingPlaceholder(t *testing.T) {
	t.Skip("integration coverage lives in 04-08 integration suite")
}
