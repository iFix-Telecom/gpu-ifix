// Package main: unit tests for `gatewayctl prices set` --unit validation.
// The end-to-end tests (atomic swap semantics, NOTIFY triggering, tabwriter
// format) live in the 04-08 integration suite against testcontainers Postgres.
//
// Phase 23 (23-10): as units de cache (cache_read_token, cache_write_token,
// cache_write_1h_token) passaram a ser aceitas — o banco já as aceita desde a
// migration do 23-03.
package main

import (
	"context"
	"strings"
	"testing"
)

func TestPricesValidUnitAcceptsCacheUnits(t *testing.T) {
	for _, u := range []string{
		"input_token", "output_token", "audio_second", "embed_request",
		"cache_read_token", "cache_write_token", "cache_write_1h_token",
	} {
		if !validPriceUnit(u) {
			t.Errorf("validPriceUnit(%q) = false, want true", u)
		}
	}
	for _, u := range []string{"bogus", "", "cache_token"} {
		if validPriceUnit(u) {
			t.Errorf("validPriceUnit(%q) = true, want false", u)
		}
	}
}

func TestPricesUnitListNamesSevenUnits(t *testing.T) {
	list := priceUnitList()
	if n := len(strings.Split(list, "|")); n != 7 {
		t.Errorf("priceUnitList: want 7 units, got %d (%s)", n, list)
	}
	if !strings.Contains(list, "cache_write_1h_token") {
		t.Errorf("priceUnitList sem cache_write_1h_token: %s", list)
	}
}

// Unit inválida sai com 2 antes de qualquer acesso ao banco.
func TestPricesSetRejectsBogusUnit(t *testing.T) {
	rc := runPricesSet(context.Background(),
		[]string{"-model", "claude-haiku-5-5", "-provider", "anthropic", "-unit", "bogus", "-usd", "0.00000001"},
		nil)
	if rc != 2 {
		t.Errorf("runPricesSet bogus unit: rc=%d, want 2", rc)
	}
}

// TestFormatUSDKeepsSubEightDecimalPrecision — 0043: o set não pode mais
// arredondar para 8 casas (1.25e-7 virava 1.2e-7 no banco).
func TestFormatUSDKeepsSubEightDecimalPrecision(t *testing.T) {
	cases := map[float64]string{
		0.000000125: "0.000000125",
		0.000000625: "0.000000625",
		0.00000001:  "0.00000001",
		0.00004:     "0.00004",
		0.195e-6:    "0.000000195",
		1.5:         "1.5",
	}
	for in, want := range cases {
		if got := formatUSD(in); got != want {
			t.Errorf("formatUSD(%g) = %q, want %q", in, got, want)
		}
	}
}
