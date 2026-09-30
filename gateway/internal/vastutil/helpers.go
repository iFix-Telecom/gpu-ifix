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
// and the background destroy retry policy — exactly the anti-pattern
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
//   - Pitfall 8 fresh background ctx (per attempt, 15s each, 75s total,
//     retry on 429/5xx/transport) — `BestEffortDestroy`
//   - Strict lifecycle-label parser + orphan selector for the leader
//     label sweep — `ParseLifecycleLabel`, `SelectLabelOrphans` (sweep.go)
//   - D-E4 Sentry breadcrumb + caller-supplied category prefix —
//     `CaptureBreadcrumb`
package vastutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// Destroy retry policy (ClickUp 86akr57nj, 2026-09-30).
//
// History: Phase 6.6 UAT 2026-05-18 caught an orphan pod (instance
// 37028480, ~3h30, ~$2.17) because the first 429 aborted destroy. The
// fix then retried ONLY 429 inside a single shared 30s ctx. On
// 2026-09-30 primary lifecycle 494 / instance 53345260 was found running
// ~29h; the diagnosis attributed it to a DELETE that failed with a
// timeout, which the old policy classified as non-retryable (1 attempt).
//
// Now every attempt gets its OWN fresh ctx bounded by
// destroyAttemptTimeout (the Vast http.Client additionally caps each
// request at 30s), transport errors and 5xx are retried like 429, and
// destroyTotalBudget bounds the whole call. Package vars (not consts) so
// tests can shrink them.
var (
	destroyAttemptTimeout = 15 * time.Second
	destroyTotalBudget    = 75 * time.Second
	destroyMaxAttempts    = 6
	destroyInitialBackoff = 1 * time.Second
	destroyMaxBackoff     = 8 * time.Second
)

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

// destroyRetryable classifies a DestroyInstance error.
//
//   - ErrRateLimited (429) → retry
//   - *vast.VastError with Status >= 500 → retry
//   - *vast.VastError with Status 400-499 → permanent
//   - ErrUnauthorized (401/403), ErrOfferGone → permanent
//   - anything else (transport errors, context.DeadlineExceeded,
//     *url.Error, net errors) → retry
//
// ErrInstanceNotFound is handled by the caller as success before this
// classifier is consulted (listed as permanent here defensively).
func destroyRetryable(err error) bool {
	if errors.Is(err, vast.ErrRateLimited) {
		return true
	}
	if errors.Is(err, vast.ErrUnauthorized) || errors.Is(err, vast.ErrOfferGone) ||
		errors.Is(err, vast.ErrInstanceNotFound) {
		return false
	}
	var ve *vast.VastError
	if errors.As(err, &ve) {
		return ve.Status >= 500
	}
	return true
}

// BestEffortDestroy issues DestroyInstance with retries and returns nil
// once the instance is confirmed gone (200, or 404 no_such_instance).
//
// Context contract (Pitfall 8): the caller ctx is deliberately NOT used
// for cancellation. Shutdown/drain paths call this with an already
// cancelled ctx and the destroy must still go out. Each attempt runs on
// a fresh context.Background()-derived ctx bounded by
// destroyAttemptTimeout; the whole call is bounded by destroyTotalBudget.
//
// Retry policy: 429, 5xx and transport/timeout errors are retried with
// exponential backoff (1s doubling, capped at destroyMaxBackoff) up to
// destroyMaxAttempts or destroyTotalBudget, whichever comes first.
// 401/403 and other 4xx are permanent and returned after 1 attempt.
//
// On failure an Error-level log + Sentry breadcrumb is emitted and a
// wrapped error returned. The instance may then be orphaned: the real
// reconciler is the leader label sweep (primary/sweep.go,
// emerg/sweep.go), which destroys any exactly-labelled instance whose
// lifecycle is not live in the DB.
//
// `instanceID == 0` and `vastClient == nil` are tolerated as no-ops
// returning nil so callers can invoke this from a deferred /
// early-failure branch without pre-checking. Callers that ignore the
// returned error remain valid (statement form).
func BestEffortDestroy(ctx context.Context, vastClient VastDestroyer, log *slog.Logger, instanceID int64) error {
	if instanceID == 0 || vastClient == nil {
		return nil
	}
	_ = ctx // intentionally unused for cancellation — see Pitfall 8 above.

	deadline := time.Now().Add(destroyTotalBudget)
	backoff := destroyInitialBackoff
	var lastErr error
	attempt := 0
	for attempt < destroyMaxAttempts {
		if attempt > 0 && !time.Now().Before(deadline) {
			break
		}
		attempt++
		attemptCtx, cancel := context.WithTimeout(context.Background(), destroyAttemptTimeout)
		err := vastClient.DestroyInstance(attemptCtx, instanceID)
		cancel()
		if err == nil || errors.Is(err, vast.ErrInstanceNotFound) {
			if attempt > 1 && log != nil {
				log.Info("BestEffortDestroy succeeded after retry",
					"instance_id", instanceID, "attempt", attempt)
			}
			return nil
		}
		lastErr = err
		if !destroyRetryable(err) {
			if log != nil {
				log.Error("BestEffortDestroy: non-retryable error; instance may be orphaned — leader label sweep will retry",
					"instance_id", instanceID, "attempt", attempt, "err", err)
			}
			CaptureBreadcrumb("vastutil.destroy.orphan", map[string]any{
				"instance_id": instanceID,
				"reason":      "non_retryable",
				"attempts":    attempt,
			})
			return fmt.Errorf("vastutil: destroy instance %d: non-retryable: %w", instanceID, err)
		}
		if attempt >= destroyMaxAttempts {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		sleep := backoff
		if sleep > remaining {
			sleep = remaining
		}
		if log != nil {
			log.Warn("BestEffortDestroy retryable error; backing off",
				"instance_id", instanceID, "attempt", attempt, "backoff", sleep, "err", err)
		}
		time.Sleep(sleep)
		backoff *= 2
		if backoff > destroyMaxBackoff {
			backoff = destroyMaxBackoff
		}
	}
	if log != nil {
		log.Error("BestEffortDestroy exhausted retries; instance may be orphaned — leader label sweep will retry",
			"instance_id", instanceID, "attempts", attempt, "err", lastErr)
	}
	CaptureBreadcrumb("vastutil.destroy.orphan", map[string]any{
		"instance_id": instanceID,
		"reason":      "retries_exhausted",
		"attempts":    attempt,
	})
	return fmt.Errorf("vastutil: destroy instance %d: retries exhausted after %d attempts: %w",
		instanceID, attempt, lastErr)
}
