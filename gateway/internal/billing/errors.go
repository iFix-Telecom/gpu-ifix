// Package billing provides the request-scoped usage accountant, the
// price/fx loaders (hot-reload via LISTEN/NOTIFY), and the async flusher
// that writes billing_events + usage_counters in a single CTE. This file
// declares the sentinel errors used by those components in Wave 2.
package billing

import "errors"

var (
	// ErrFlushFailed — wraps the inner pgx error returned by the same-txn
	// INSERT billing_events + UPSERT usage_counters CTE.
	ErrFlushFailed = errors.New("billing: flush failed")
	// ErrPriceMissing — no row in prices for (model, provider, unit). Logged
	// at WARN; cost columns default to 0 to keep flush from blocking.
	ErrPriceMissing = errors.New("billing: price missing for model/provider/unit")
	// ErrFXMissing — no fx_rates row for the requested pair (default USD/BRL).
	ErrFXMissing = errors.New("billing: fx rate missing")
)
