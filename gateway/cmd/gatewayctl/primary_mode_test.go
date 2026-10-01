package main

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// TestOfferModeLabel — quick-261001-qdd MODE column of `primary lifecycles`.
func TestOfferModeLabel(t *testing.T) {
	var price pgtype.Numeric
	if err := price.Scan("0.1234"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		isBid pgtype.Bool
		price pgtype.Numeric
		want  string
	}{
		{"legacy_null", pgtype.Bool{}, pgtype.Numeric{}, "-"},
		{"ondemand", pgtype.Bool{Bool: false, Valid: true}, pgtype.Numeric{}, "ondemand"},
		{"bid", pgtype.Bool{Bool: true, Valid: true}, price, "bid@0.1234"},
		{"bid_null_price", pgtype.Bool{Bool: true, Valid: true}, pgtype.Numeric{}, "bid@-"},
	}
	for _, c := range cases {
		if got := offerModeLabel(c.isBid, c.price); got != c.want {
			t.Errorf("%s: offerModeLabel = %q, want %q", c.name, got, c.want)
		}
	}
}
