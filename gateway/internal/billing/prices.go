// Package billing provides the request-scoped usage accountant, the
// price/fx loaders (hot-reload via LISTEN/NOTIFY), and the async flusher
// that writes billing_events + usage_counters in a single CTE. This file
// declares the shared snapshot-view types used by the loaders.
package billing

import "time"

// PriceKey identifies a row in the prices table.
type PriceKey struct {
	Model    string
	Provider string
	// Unit: input_token | output_token | audio_second | embed_request |
	// cache_read_token | cache_write_token | cache_write_1h_token (Phase 23).
	Unit string
}

// Price is the snapshot view; valid_to=NULL is implied (loader filters).
type Price struct {
	UnitCostUSD float64
	ValidFrom   time.Time
}

// FXRate is the snapshot view of an active currency_pair row.
type FXRate struct {
	CurrencyPair string
	Rate         float64
	ValidFrom    time.Time
}
