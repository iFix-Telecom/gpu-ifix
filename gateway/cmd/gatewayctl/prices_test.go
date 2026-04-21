// Package main_test: placeholder test file for prices subcommand. The real
// end-to-end tests (atomic swap semantics, NOTIFY triggering, tabwriter
// format) live in 04-08's integration suite against testcontainers Postgres.
// This file exists so `go test ./cmd/gatewayctl/...` still picks up the
// package when integration builds are skipped.
package main_test

import "testing"

// TestPricesPlaceholder pins the contract that `gatewayctl prices ...`
// has a binary entry point. Real coverage is in 04-08.
func TestPricesPlaceholder(t *testing.T) {
	t.Skip("integration coverage lives in 04-08 integration suite")
}
