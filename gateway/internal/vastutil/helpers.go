// Package vastutil — pure, framework-free helpers shared by the
// `emerg` and (Wave 2+) `primary` pod lifecycle subsystems.
//
// # Why a separate package
//
// Phase 6.6 (D-08.3) introduces a second consumer of the Vast.ai
// lifecycle plumbing (primary pod) that needs the EXACT same epsilon
// filter, host-exclude, JSONB event marshaller, pgtype scalar mappers,
// Sentry breadcrumb helper, and best-effort destroy. Duplicating these
// inside `internal/primary/` would create two slowly-diverging copies
// of the Pitfall 5 epsilon (cap+0.0001), the W7 events JSONB shape,
// and the 30s background destroy budget — exactly the anti-pattern
// RESEARCH.md §"Decisions Resolved" item 4 calls out.
//
// Everything here is a free function (no receiver) and depends only
// on stdlib, sentry-go (already in go.mod), pgx/v5/pgtype (already in
// go.mod), and the existing `vast` DTO subpackage. ZERO new external
// deps per phase 06.6-02 threat T-06.6-SC mitigation.
//
// # Pitfall references (preserved verbatim from emerg/lifecycle.go)
//
//   - Pitfall 5 epsilon `cap + 0.0001` — `FilterBelowCap`
//   - D-A2 host_id exclude — `ExcludeHost`
//   - W7 events-JSONB-first invariant — `MustEventJSON`
//   - Pitfall 8 fresh background ctx + 30s destroy budget —
//     `BestEffortDestroy`
//   - D-E4 Sentry breadcrumb + caller-supplied category prefix —
//     `CaptureBreadcrumb`
package vastutil

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/big"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// destroyShutdownBudget mirrors the value in emerg/lifecycle.go:82.
// Owned here so consumers of BestEffortDestroy do not need to import
// `emerg` (which would create a primary→emerg import cycle once Wave
// 2 plans land).
const destroyShutdownBudget = 30 * time.Second

// VastDestroyer is the minimum contract BestEffortDestroy needs from
// the Vast.ai client. emerg.VastAPI already exposes
// `DestroyInstance(ctx, id) error`, so emerg consumers satisfy this
// interface implicitly with no cast. primary (Wave 2) will satisfy it
// the same way.
type VastDestroyer interface {
	DestroyInstance(ctx context.Context, instanceID int64) error
}

// FilterBelowCap applies the Pitfall 5 epsilon comparison cap+0.0001 to
// the offer list. Defense in depth on top of the server-side dph_total
// filter (which can include hosts that priced at exactly cap+1e-6 due to
// float rounding upstream).
//
// Returns a fresh slice (caller may mutate the result without affecting
// the input). Empty/nil input yields an empty non-nil slice.
func FilterBelowCap(offers []vast.Offer, cap float64) []vast.Offer {
	out := make([]vast.Offer, 0, len(offers))
	for _, o := range offers {
		if o.DphTotal > cap+0.0001 {
			continue
		}
		out = append(out, o)
	}
	return out
}

// ExcludeHost removes any offer whose HostID matches the given host. Used
// when the primary host is known to avoid bidding on the same physical
// machine (D-A2 host_id != filter). Returns a fresh slice. hostID<=0
// means "unknown" — input is returned unchanged.
func ExcludeHost(offers []vast.Offer, hostID int64) []vast.Offer {
	if hostID <= 0 {
		return offers
	}
	out := make([]vast.Offer, 0, len(offers))
	for _, o := range offers {
		if o.HostID == hostID {
			continue
		}
		out = append(out, o)
	}
	return out
}

// MustEventJSON marshals a single event row {ts, type, payload} for the
// emergency_lifecycles.events JSONB column (and the future
// primary_lifecycles.events column). Returns a length-1 JSON array (the
// SQL `events || $::jsonb` operator requires the right side to be
// JSONB-compatible — wrapping in [...] keeps the array-of-events shape).
//
// `json.Marshal` on a map[string]any with primitive values cannot
// realistically fail; on the unreachable error path we return a sentinel
// fallback rather than panic to keep the calling goroutine alive.
func MustEventJSON(eventType string, payload map[string]any) []byte {
	row := map[string]any{
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		"type":    eventType,
		"payload": payload,
	}
	arr := []map[string]any{row}
	out, err := json.Marshal(arr)
	if err != nil {
		return []byte(`[{"type":"event_marshal_failed"}]`)
	}
	return out
}

// PgInt8 wraps an int64 as a non-null pgtype.Int8 (sqlc's BIGINT mapping).
func PgInt8(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

// PgNumericFromFloat converts a float64 to pgtype.Numeric. Used for
// accepted_dph (NUMERIC(6,4)) and total_cost_brl (NUMERIC(10,4)). Values
// are scaled by 10^4 and truncated to int — matches the column scale of
// 4 decimal places.
func PgNumericFromFloat(v float64) pgtype.Numeric {
	if v == 0 {
		return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
	}
	scaled := int64(v * 10000)
	return pgtype.Numeric{Int: big.NewInt(scaled), Exp: -4, Valid: true}
}

// CaptureBreadcrumb adds a Sentry breadcrumb at the info level. Used for
// non-terminal events (offer_accepted, instance_created, health_pass).
// Per D-E4 — breadcrumbs ride along the next CaptureMessage so terminal
// errors land in Sentry with the full lifecycle timeline attached.
//
// The receiver-bound emerg/lifecycle.go:903 origin was free of
// receiver state apart from prepending the literal "emerg." prefix
// to category. Free-function form pushes the prefix decision to the
// caller (emerg passes "emerg."+cat; primary will pass "primary."+
// cat). The breadcrumb body itself is callsite-stable.
//
// Safe to call when Sentry is not initialized — `sentry.AddBreadcrumb`
// is a no-op against the default hub in that case (defensive coverage
// for tests + ops scripts that exercise this path without booting the
// Sentry transport).
func CaptureBreadcrumb(category string, data map[string]any) {
	sentry.AddBreadcrumb(&sentry.Breadcrumb{
		Category:  category,
		Message:   category,
		Level:     sentry.LevelInfo,
		Timestamp: time.Now(),
		Data:      data,
	})
}

// BestEffortDestroy issues DestroyInstance with a fresh background context
// + 30s budget. Errors are logged and swallowed — caller is already on a
// failure path and the orphan cleanup goroutine (Plan 07) will reconcile
// any leaks.
//
// `instanceID == 0` and `vastClient == nil` are tolerated as no-ops so
// callers can invoke this from a deferred / early-failure branch without
// pre-checking. The `log` argument matches emerg/lifecycle.go's
// `r.deps.Log` (*slog.Logger); pass slog.Default() if no scoped logger
// is available.
func BestEffortDestroy(ctx context.Context, vastClient VastDestroyer, log *slog.Logger, instanceID int64) {
	if instanceID == 0 || vastClient == nil {
		return
	}
	// `ctx` arg currently unused — the destroy uses a fresh background
	// ctx by design (Pitfall 8: parent ctx is already cancelled on the
	// shutdown path). Accepted as a parameter for signature stability
	// (future ctx-aware tracing / cancellation hooks).
	_ = ctx
	destroyCtx, cancel := context.WithTimeout(context.Background(), destroyShutdownBudget)
	defer cancel()
	if err := vastClient.DestroyInstance(destroyCtx, instanceID); err != nil {
		if log != nil {
			log.Warn("BestEffortDestroy failed; orphan recovery will reconcile",
				"instance_id", instanceID, "err", err)
		}
	}
}
