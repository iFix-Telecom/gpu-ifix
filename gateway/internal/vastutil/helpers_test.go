// Package vastutil (helpers_test.go): unit tests ported verbatim from
// gateway/internal/emerg/lifecycle_test.go:26-118 (the 5 pure-helper
// assertions). Adds two net-new tests for the helpers that grew arity
// (or lost their receiver) during extraction:
//
//   - TestBestEffortDestroy_CallsDestroyInstance — fake VastDestroyer
//     captures the instanceID; helper tolerates a non-nil error from
//     the fake.
//   - TestCaptureBreadcrumb_NoOp_WhenNoSentryHub — defensive: helper
//     does not panic when Sentry has never been initialized (ops
//     scripts + this very test binary exercise that path).
//
// Imports kept minimal — zero new external deps in go.mod (T-06.6-SC).
package vastutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// ---------------------------------------------------------------------
// 5 pure-helper tests ported from emerg/lifecycle_test.go:26-118 with
// identifier capitalization (free functions are exported now).
// ---------------------------------------------------------------------

// TestFilterBelowCap_Epsilon — Pitfall 5: epsilon comparison
// `cap + 0.0001`. Offers exactly at the cap pass; offers above the cap
// + epsilon are rejected.
func TestFilterBelowCap_Epsilon(t *testing.T) {
	cap := 0.40
	offers := []vast.Offer{
		{ID: 1, DphTotal: 0.45},   // above cap → rejected
		{ID: 2, DphTotal: 0.35},   // below cap → kept
		{ID: 3, DphTotal: 0.40},   // exactly at cap → kept
		{ID: 4, DphTotal: 0.4001}, // exactly cap+epsilon → kept (boundary)
		{ID: 5, DphTotal: 0.4002}, // just above cap+epsilon → rejected
	}
	got := FilterBelowCap(offers, cap)
	require.Len(t, got, 3, "ids 2, 3, 4 must pass; ids 1 + 5 rejected")
	wantIDs := map[int64]bool{2: true, 3: true, 4: true}
	for _, o := range got {
		require.True(t, wantIDs[o.ID], "unexpected offer ID %d in filtered output", o.ID)
	}
}

// TestFilterBelowCap_EmptyInput — defensive: empty in → empty non-nil out.
func TestFilterBelowCap_EmptyInput(t *testing.T) {
	got := FilterBelowCap(nil, 0.40)
	require.NotNil(t, got, "should return empty slice, not nil")
	require.Len(t, got, 0)
}

// TestExcludeHost — known host removed; unknown (hostID=0) keeps all.
func TestExcludeHost(t *testing.T) {
	offers := []vast.Offer{
		{ID: 1, HostID: 100},
		{ID: 2, HostID: 200},
		{ID: 3, HostID: 100},
		{ID: 4, HostID: 300},
	}
	got := ExcludeHost(offers, 100)
	require.Len(t, got, 2, "host 100 (ids 1, 3) must be removed")
	for _, o := range got {
		require.NotEqual(t, int64(100), o.HostID)
	}

	// hostID=0 is "unknown" — return input unchanged.
	got2 := ExcludeHost(offers, 0)
	require.Len(t, got2, 4)
}

// TestMustEventJSON — output must be a valid JSON array containing one
// row with the expected `type` + `payload` keys + a `ts` timestamp.
func TestMustEventJSON(t *testing.T) {
	out := MustEventJSON("offer_accepted", map[string]any{
		"offer_id": int64(123),
		"dph":      0.35,
	})
	var parsed []map[string]any
	require.NoError(t, json.Unmarshal(out, &parsed),
		"output must be a valid JSON array")
	require.Len(t, parsed, 1)
	row := parsed[0]
	require.Equal(t, "offer_accepted", row["type"])
	require.NotNil(t, row["ts"], "ts must be populated for audit timeline")
	payload, ok := row["payload"].(map[string]any)
	require.True(t, ok, "payload key must be an object")
	require.InDelta(t, 0.35, payload["dph"], 0.0001)
	require.EqualValues(t, 123, payload["offer_id"])
}

// TestPgInt8 — wrap returns Valid=true.
func TestPgInt8(t *testing.T) {
	v := PgInt8(12345)
	require.True(t, v.Valid)
	require.Equal(t, int64(12345), v.Int64)
}

// TestPgNumericFromFloat — round-trip via Float64Value. Verbatim cases
// from emerg/lifecycle_test.go:97-118 (PATTERNS.md:154-176 spec).
func TestPgNumericFromFloat(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0.0, 0.0},
		{0.35, 0.35},
		{0.4001, 0.4001},
		{200.0, 200.0},
		{200.1234, 200.1234},
	}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			n := PgNumericFromFloat(c.in)
			require.True(t, n.Valid)
			fv, err := n.Float64Value()
			require.NoError(t, err)
			require.True(t, fv.Valid)
			require.InDelta(t, c.want, fv.Float64, 0.0001)
		})
	}
}

// ---------------------------------------------------------------------
// New tests for the two helpers that lost their receiver / grew arity.
// ---------------------------------------------------------------------

// fakeVastDestroyer captures the instanceID DestroyInstance was called
// with and optionally returns an error. Used to drive BestEffortDestroy
// coverage without touching the real Vast.ai client.
//
// errSequence (optional) returns scripted errors in order — once drained,
// subsequent calls fall back to .err. Used to model "429 N times then
// nil" patterns for the 429-retry tests.
type fakeVastDestroyer struct {
	calledID    int64
	calls       int
	err         error
	errSequence []error
	// deadlines records ctx.Deadline() of every call (zero Time when the
	// ctx had no deadline) so tests can assert per-attempt budgets.
	deadlines []time.Time
	// ctxErrs records ctx.Err() at call time (a cancelled caller ctx must
	// NOT leak into the destroy ctx — Pitfall 8).
	ctxErrs []error
}

func (f *fakeVastDestroyer) DestroyInstance(ctx context.Context, id int64) error {
	f.calledID = id
	f.calls++
	dl, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, dl)
	f.ctxErrs = append(f.ctxErrs, ctx.Err())
	if len(f.errSequence) > 0 {
		e := f.errSequence[0]
		f.errSequence = f.errSequence[1:]
		return e
	}
	return f.err
}

// shrinkBackoff sets the BestEffortDestroy backoff knobs to near-zero
// for the duration of the test so retry tests finish in microseconds
// instead of 15s. Restores originals via t.Cleanup.
func shrinkBackoff(t *testing.T) {
	t.Helper()
	origInit, origMax := destroyInitialBackoff, destroyMaxBackoff
	origAttempt, origTotal := destroyAttemptTimeout, destroyTotalBudget
	destroyInitialBackoff = 1 * time.Microsecond
	destroyMaxBackoff = 1 * time.Microsecond
	destroyAttemptTimeout = 2 * time.Second
	destroyTotalBudget = 10 * time.Second
	t.Cleanup(func() {
		destroyInitialBackoff = origInit
		destroyMaxBackoff = origMax
		destroyAttemptTimeout = origAttempt
		destroyTotalBudget = origTotal
	})
}

// TestBestEffortDestroy_CallsDestroyInstance — happy path: the helper
// forwards instanceID to the VastDestroyer impl and returns without
// signalling failure to the caller (errors are logged + swallowed).
func TestBestEffortDestroy_CallsDestroyInstance(t *testing.T) {
	fake := &fakeVastDestroyer{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	require.NoError(t, BestEffortDestroy(context.Background(), fake, log, 42))
	require.Equal(t, int64(42), fake.calledID, "fake VastDestroyer must observe instanceID=42")
	require.Equal(t, 1, fake.calls)
}

// TestBestEffortDestroy_NonRetryableReturnsError — the helper never
// panics, and a non-retryable failure is surfaced to the caller as an
// error (it used to be swallowed with a misleading "orphan recovery will
// reconcile" log; the real reconciler is now the leader label sweep).
func TestBestEffortDestroy_NonRetryableReturnsError(t *testing.T) {
	shrinkBackoff(t)
	fake := &fakeVastDestroyer{err: &vast.VastError{Status: 400, Code: "bad_request", Msg: "boom"}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var err error
	require.NotPanics(t, func() {
		err = BestEffortDestroy(context.Background(), fake, log, 99)
	})
	require.Error(t, err)
	require.Equal(t, int64(99), fake.calledID)
	require.Equal(t, 1, fake.calls, "4xx (non-429) must NOT retry")
}

// TestBestEffortDestroy_NoOpOnZeroID — instanceID==0 is the "no row was
// created yet" sentinel; helper must short-circuit without calling
// DestroyInstance.
func TestBestEffortDestroy_NoOpOnZeroID(t *testing.T) {
	fake := &fakeVastDestroyer{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.NoError(t, BestEffortDestroy(context.Background(), fake, log, 0))
	require.Equal(t, 0, fake.calls, "instanceID=0 must short-circuit")
}

// TestBestEffortDestroy_NoOpOnNilClient — defensive: nil VastDestroyer
// (e.g. operator forgot to wire vast client; unit test that does not
// stub it) must not panic.
func TestBestEffortDestroy_NoOpOnNilClient(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.NotPanics(t, func() {
		require.NoError(t, BestEffortDestroy(context.Background(), nil, log, 42))
	})
}

// TestBestEffortDestroy_Retries429_ThenSucceeds — Phase 6.6 UAT
// 2026-05-18 regression: a transient HTTP 429 must trigger exponential
// backoff retries, not an immediate orphan. Fake returns 429 twice then
// nil; helper must call DestroyInstance 3 times.
func TestBestEffortDestroy_Retries429_ThenSucceeds(t *testing.T) {
	shrinkBackoff(t)
	fake := &fakeVastDestroyer{
		errSequence: []error{vast.ErrRateLimited, vast.ErrRateLimited, nil},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	require.NoError(t, BestEffortDestroy(context.Background(), fake, log, 7777))
	require.Equal(t, int64(7777), fake.calledID)
	require.Equal(t, 3, fake.calls, "expected 2 retries after initial 429s")
}

// TestBestEffortDestroy_Retries429_AllExhausted — persistent 429 (Vast
// in deep rate-limit) must exhaust destroyMaxAttempts and emit the
// orphan-alert breadcrumb. Verifies the retry cap behaviour so a
// runaway Vast API can never wedge the shutdown path indefinitely.
func TestBestEffortDestroy_Retries429_AllExhausted(t *testing.T) {
	shrinkBackoff(t)
	fake := &fakeVastDestroyer{err: vast.ErrRateLimited}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	require.Error(t, BestEffortDestroy(context.Background(), fake, log, 8888))
	require.Equal(t, destroyMaxAttempts, fake.calls,
		"expected exactly destroyMaxAttempts before giving up")
}

// TestBestEffortDestroy_RetriesTransportTimeout — 2026-09-30 leak root
// cause (ClickUp 86akr57nj): a DELETE that timed out (context deadline /
// *url.Error) was treated as non-retryable and the pod kept running for
// 29h. A timeout must now be retried with a fresh ctx.
func TestBestEffortDestroy_RetriesTransportTimeout(t *testing.T) {
	shrinkBackoff(t)
	cases := map[string]error{
		"deadline": context.DeadlineExceeded,
		"url_error": &url.Error{Op: "Delete", URL: "https://console.vast.ai/api/v0/instances/1/",
			Err: context.DeadlineExceeded},
	}
	for name, first := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fakeVastDestroyer{errSequence: []error{first, nil}}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			require.NoError(t, BestEffortDestroy(context.Background(), fake, log, 53345260))
			require.Equal(t, 2, fake.calls)
		})
	}
}

// TestBestEffortDestroy_Retries5xx — Vast 503 twice then success.
func TestBestEffortDestroy_Retries5xx(t *testing.T) {
	shrinkBackoff(t)
	e503 := &vast.VastError{Status: 503, Code: "server_error", Msg: "unavailable"}
	fake := &fakeVastDestroyer{errSequence: []error{e503, e503, nil}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.NoError(t, BestEffortDestroy(context.Background(), fake, log, 1))
	require.Equal(t, 3, fake.calls)
}

// TestBestEffortDestroy_NotFoundIsSuccess — 404 no_such_instance means the
// instance is already gone: success after exactly 1 call.
func TestBestEffortDestroy_NotFoundIsSuccess(t *testing.T) {
	shrinkBackoff(t)
	fake := &fakeVastDestroyer{err: vast.ErrInstanceNotFound}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.NoError(t, BestEffortDestroy(context.Background(), fake, log, 1))
	require.Equal(t, 1, fake.calls)
}

// TestBestEffortDestroy_UnauthorizedNoRetry — 401/403 and other non-429
// 4xx are permanent: exactly 1 call, error returned.
func TestBestEffortDestroy_UnauthorizedNoRetry(t *testing.T) {
	shrinkBackoff(t)
	for name, e := range map[string]error{
		"unauthorized": vast.ErrUnauthorized,
		"offer_gone":   vast.ErrOfferGone,
		"http_400":     &vast.VastError{Status: 400, Code: "bad", Msg: "bad"},
		"wrapped_401":  fmt.Errorf("destroy: %w", vast.ErrUnauthorized),
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeVastDestroyer{err: e}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			require.Error(t, BestEffortDestroy(context.Background(), fake, log, 1))
			require.Equal(t, 1, fake.calls)
		})
	}
}

// TestBestEffortDestroy_PerAttemptCtx — every attempt gets its own fresh
// deadline <= destroyAttemptTimeout, and a cancelled caller ctx does NOT
// abort the destroy (Pitfall 8: shutdown paths pass a cancelled ctx).
func TestBestEffortDestroy_PerAttemptCtx(t *testing.T) {
	shrinkBackoff(t)
	fake := &fakeVastDestroyer{errSequence: []error{context.DeadlineExceeded, vast.ErrRateLimited, nil}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	caller, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	require.NoError(t, BestEffortDestroy(caller, fake, log, 1))
	require.Equal(t, 3, fake.calls)
	for i, dl := range fake.deadlines {
		require.False(t, dl.IsZero(), "attempt %d ctx must carry a deadline", i+1)
		require.LessOrEqual(t, dl.Sub(start), destroyAttemptTimeout+50*time.Millisecond,
			"attempt %d deadline must be bounded by destroyAttemptTimeout", i+1)
		require.NoError(t, fake.ctxErrs[i], "attempt %d ctx must not be cancelled", i+1)
	}
}

// TestDestroyRetryable — classifier table.
func TestDestroyRetryable(t *testing.T) {
	require.True(t, destroyRetryable(vast.ErrRateLimited))
	require.True(t, destroyRetryable(&vast.VastError{Status: 500}))
	require.True(t, destroyRetryable(&vast.VastError{Status: 504}))
	require.True(t, destroyRetryable(context.DeadlineExceeded))
	require.True(t, destroyRetryable(errors.New("connection reset by peer")))
	require.False(t, destroyRetryable(&vast.VastError{Status: 404}))
	require.False(t, destroyRetryable(&vast.VastError{Status: 400}))
	require.False(t, destroyRetryable(vast.ErrUnauthorized))
	require.False(t, destroyRetryable(vast.ErrOfferGone))
}

// TestCaptureBreadcrumb_NoOp_WhenNoSentryHub — Sentry is not initialized
// inside `go test` by default; AddBreadcrumb against the default hub is
// documented as a no-op. Defensive guard against a future regression
// where the breadcrumb path dereferences a nil hub.
func TestCaptureBreadcrumb_NoOp_WhenNoSentryHub(t *testing.T) {
	require.NotPanics(t, func() {
		CaptureBreadcrumb("test.event", map[string]any{"k": "v"})
	}, "CaptureBreadcrumb MUST tolerate uninitialized Sentry hub")
}
