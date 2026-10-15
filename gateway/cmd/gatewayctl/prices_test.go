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
