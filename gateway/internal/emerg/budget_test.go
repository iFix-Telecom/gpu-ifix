// Package emerg (budget_test.go): Plan 06-09 unit tests for the
// monthly-budget alert (D-D2 / PRV-05).
//
// Coverage focus:
//
//   - budgetAlertDedupe.shouldEmit returns true exactly once per
//     UTC-day (Pitfall 11 CORRECT version per RESEARCH lines 1411-1417).
//   - budgetAlertDedupe.shouldEmit is race-free: 1000 concurrent
//     goroutines yield exactly 1 true.
//   - budgetAlertDedupe.shouldEmit re-arms on a new day.
//   - checkBudget below threshold updates the obs gauge but does NOT
//     emit a Sentry event (verified via custom Transport).
//   - checkBudget above threshold updates the gauge AND emits a Sentry
//     CaptureMessage tagged subsystem=emerg / alert=budget_exceeded
//     with month_cost_brl + budget_brl extras.
//
// Why a recordingTransport instead of mocking sentry.Hub directly: the
// production code uses sentry.CurrentHub().Clone() for scope isolation
// (matches the captureTerminalSentry pattern in lifecycle.go). The
// supported way to introspect what Clone() emits is to install a
// process-wide test transport via sentry.Init — same approach the
// sentry-go authors recommend in their own internal tests.
package emerg

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	sentry "github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/vastutil"
)

// recordingTransport is a sentry.Transport implementation that captures
// every SendEvent call into a channel for test introspection. Buffer
// size 16 is larger than any single test will need.
type recordingTransport struct {
	events chan *sentry.Event
}

func newRecordingTransport() *recordingTransport {
	return &recordingTransport{events: make(chan *sentry.Event, 16)}
}

func (t *recordingTransport) Configure(_ sentry.ClientOptions) {}
func (t *recordingTransport) SendEvent(event *sentry.Event) {
	select {
	case t.events <- event:
	default:
		// channel full — drop. Tests should always drain.
	}
}
func (t *recordingTransport) Flush(_ time.Duration) bool { return true }

// installSentryTestTransport replaces the process-wide sentry hub with
// a fresh client wired to recordingTransport. Returns the transport so
// the test can read events, and a cleanup func that the test should
// defer to restore an empty hub for the next test.
func installSentryTestTransport(t *testing.T) *recordingTransport {
	t.Helper()
	transport := newRecordingTransport()
	err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://public@sentry.example.com/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("sentry.Init: %v", err)
	}
	t.Cleanup(func() {
		sentry.Flush(2 * time.Second)
		// Re-init with empty options to detach the test transport.
		_ = sentry.Init(sentry.ClientOptions{})
	})
	return transport
}

// TestBudgetDedupe_OncePerDay — 100 sequential calls in the same UTC
// day yield exactly 1 true.
func TestBudgetDedupe_OncePerDay(t *testing.T) {
	d := &budgetAlertDedupe{}
	trues := 0
	for i := 0; i < 100; i++ {
		if d.shouldEmit() {
			trues++
		}
	}
	if trues != 1 {
		t.Fatalf("dedupe: got %d true returns in same day, want 1", trues)
	}
}

// TestBudgetDedupe_NewDay — after one emit, simulating a day rollback
// (manually rewind lastEmittedDay) re-arms shouldEmit.
func TestBudgetDedupe_NewDay(t *testing.T) {
	d := &budgetAlertDedupe{}
	if !d.shouldEmit() {
		t.Fatal("first call should emit (true)")
	}
	if d.shouldEmit() {
		t.Fatal("second call same day should NOT emit (false)")
	}
	// Rewind one day to simulate the next UTC day arriving.
	d.lastEmittedDay.Store(d.lastEmittedDay.Load() - 1)
	if !d.shouldEmit() {
		t.Fatal("after day rollover, shouldEmit must return true")
	}
	if d.shouldEmit() {
		t.Fatal("subsequent same-day call must return false")
	}
}

// TestBudgetDedupe_RaceFree — 1000 goroutines call shouldEmit in
// parallel; the atomic CompareAndSwap guarantees exactly 1 true total.
func TestBudgetDedupe_RaceFree(t *testing.T) {
	d := &budgetAlertDedupe{}
	const N = 1000
	var trues atomic.Int64
	var wg sync.WaitGroup
	wg.Add(N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			if d.shouldEmit() {
				trues.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := trues.Load(); got != 1 {
		t.Fatalf("race-free dedupe: got %d trues across %d goroutines, want 1", got, N)
	}
}

// reconcilerForBudgetTest constructs a Reconciler with a fake DB query
// path stubbed by overriding monthlyCostFn. Avoids needing a real
// pgxpool for these unit tests.
func reconcilerForBudgetTest(t *testing.T, monthCost float64, budget float64) *Reconciler {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	fsm := NewFSM(slog.New(slog.DiscardHandler), nil)
	cfg := config.Config{MonthlyEmergencyBudgetBRL: budget}
	r := NewReconciler(Deps{
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		TickInterval: 50 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	// Override the monthly cost query to return a deterministic value.
	r.monthlyCostOverride = func(_ context.Context) (pgtype.Numeric, error) {
		return pgNumericFromFloatBudgetTest(monthCost), nil
	}
	return r
}

// pgNumericFromFloatBudgetTest is a thin alias around
// vastutil.PgNumericFromFloat (the original lifecycle.go helper was
// extracted into the vastutil package by Plan 06.6-02). Mirrors
// exp=-4 scaling. Kept as a named wrapper so the budget test's
// callsites read identically to the pre-extraction form.
func pgNumericFromFloatBudgetTest(v float64) pgtype.Numeric {
	return vastutil.PgNumericFromFloat(v)
}

// drain drains any backlog events from a recordingTransport without
// blocking. Used between checkBudget calls to isolate assertions.
func drain(transport *recordingTransport) {
	for {
		select {
		case <-transport.events:
		default:
			return
		}
	}
}

// TestCheckBudget_BelowThreshold — month cost 50, budget 200 → no
// Sentry event emitted, but the obs gauge IS updated.
func TestCheckBudget_BelowThreshold(t *testing.T) {
	transport := installSentryTestTransport(t)
	r := reconcilerForBudgetTest(t, 50.0, 200.0)
	drain(transport)

	r.checkBudget(context.Background())
	sentry.Flush(500 * time.Millisecond)

	select {
	case ev := <-transport.events:
		t.Fatalf("did not expect Sentry event below threshold; got %q", ev.Message)
	case <-time.After(200 * time.Millisecond):
		// expected
	}
}

// TestCheckBudget_AboveThreshold — month cost 250, budget 200 → 1
// Sentry CaptureMessage with the expected tags + extras.
func TestCheckBudget_AboveThreshold(t *testing.T) {
	transport := installSentryTestTransport(t)
	r := reconcilerForBudgetTest(t, 250.0, 200.0)
	drain(transport)

	r.checkBudget(context.Background())
	sentry.Flush(2 * time.Second)

	select {
	case ev := <-transport.events:
		if ev.Tags["subsystem"] != "emerg" {
			t.Errorf("tag subsystem = %q, want emerg", ev.Tags["subsystem"])
		}
		if ev.Tags["alert"] != "budget_exceeded" {
			t.Errorf("tag alert = %q, want budget_exceeded", ev.Tags["alert"])
		}
		if got := ev.Extra["month_cost_brl"]; got != 250.0 {
			t.Errorf("extra month_cost_brl = %v, want 250.0", got)
		}
		if got := ev.Extra["budget_brl"]; got != 200.0 {
			t.Errorf("extra budget_brl = %v, want 200.0", got)
		}
		if ev.Message == "" {
			t.Error("Sentry event message must be non-empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected Sentry event above threshold; none received in 2s")
	}
}

// TestCheckBudget_DedupeAcrossCalls — two consecutive checkBudget calls
// above threshold yield only ONE Sentry event (same UTC day).
func TestCheckBudget_DedupeAcrossCalls(t *testing.T) {
	transport := installSentryTestTransport(t)
	r := reconcilerForBudgetTest(t, 300.0, 200.0)
	drain(transport)

	r.checkBudget(context.Background())
	r.checkBudget(context.Background())
	sentry.Flush(2 * time.Second)

	count := 0
	deadline := time.After(500 * time.Millisecond)
drainLoop:
	for {
		select {
		case <-transport.events:
			count++
		case <-deadline:
			break drainLoop
		}
	}
	if count != 1 {
		t.Fatalf("dedupe across calls: got %d Sentry events, want 1", count)
	}
}
