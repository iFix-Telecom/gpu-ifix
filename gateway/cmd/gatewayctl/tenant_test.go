// Package main tests for gatewayctl tenant subcommand flag parsing + validation
// helpers. End-to-end set-mode / set-quota flows (including DB CHECK constraint
// verification of sensitive+peak rejection) live in 04-08's integration suite.
package main

import "testing"

// TestParseWindowHours exercises the --window HH-HH parser used by
// `gatewayctl tenant set-mode --mode peak`. It is a pure helper — no DB,
// no flags — so unit-testing it here guarantees the CLI error message
// is stable across changes.
func TestParseWindowHours(t *testing.T) {
	cases := []struct {
		in                 string
		wantStart, wantEnd int
		wantErr            bool
	}{
		{"08-22", 8, 22, false},
		{"22-08", 22, 8, false}, // overnight window — legitimate for some tenants
		{"00-23", 0, 23, false},
		{"24-08", 0, 0, true}, // hour out of range
		{"08-24", 0, 0, true},
		{"-1-08", 0, 0, true},
		{"abc", 0, 0, true},
		{"", 0, 0, true},
		{"08", 0, 0, true},
		{"08-", 0, 0, true},
	}
	for _, c := range cases {
		s, e, err := parseWindowHours(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseWindowHours(%q): wantErr=%v, gotErr=%v", c.in, c.wantErr, err)
			continue
		}
		if c.wantErr {
			continue
		}
		if s != c.wantStart || e != c.wantEnd {
			t.Errorf("parseWindowHours(%q): want %d-%d, got %d-%d",
				c.in, c.wantStart, c.wantEnd, s, e)
		}
	}
}
