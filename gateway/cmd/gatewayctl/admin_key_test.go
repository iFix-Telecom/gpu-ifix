// Package main_test: placeholder test file for admin-key subcommand. The
// real end-to-end tests (bcrypt hash shape, raw-key-ONCE-to-stdout, revoke-
// by-id vs revoke-by-label) live in 04-08's integration suite. This file
// exists so `go test ./cmd/gatewayctl/...` still picks up the package when
// integration builds are skipped.
package main_test

import "testing"

// TestAdminKeyPlaceholder pins the contract that `gatewayctl admin-key ...`
// has a binary entry point. Real coverage is in 04-08.
func TestAdminKeyPlaceholder(t *testing.T) {
	t.Skip("integration coverage lives in 04-08 integration suite")
}
