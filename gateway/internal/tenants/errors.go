// Package tenants owns the in-memory snapshot of tenant configuration
// (mode, peak window, schedule timezone, quotas, rate limits). The loader
// hot-reloads via LISTEN/NOTIFY on channel `tenants_changed`. This file
// declares the sentinel errors used by the loader + boot-time validation
// (D-C1, D-C4).
package tenants

import "errors"

var (
	// ErrTenantNotFound — loader snapshot has no row for the given UUID.
	// Returned by Get(); callers may fall through (legacy auth path) or 401.
	ErrTenantNotFound = errors.New("tenants: not found")
	// ErrSensitivePeakInvariant — boot-time check found row with mode='peak'
	// AND data_class='sensitive'. Should be impossible — CHECK constraint
	// enforces it. If raised, gateway os.Exit(1) per D-C1 path 3.
	ErrSensitivePeakInvariant = errors.New("tenants: sensitive+peak invariant breach")
)
