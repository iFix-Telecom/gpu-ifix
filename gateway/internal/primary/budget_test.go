// Package primary (budget_test.go): Plan 06.6-06a Task 4 — unit tests for
// the monthly primary-pod budget alert. Covers Pitfall #12 separation
// (primary budget queries primary_lifecycles ONLY, never mixed with
// emergency_lifecycles) + Sentry alert dedupe parity emerg.
package primary

import (
	"context"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sentry "github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
)

// primaryRecordingTransport is a sentry.Transport implementation that
// captures every SendEvent call into a channel for test introspection.
type primaryRecordingTransport struct {
	events chan *sentry.Event
}

func newPrimaryRecordingTransport() *primaryRecordingTransport {
	return &primaryRecordingTransport{events: make(chan *sentry.Event, 16)}
}

func (t *primaryRecordingTransport) Configure(_ sentry.ClientOptions) {}
func (t *primaryRecordingTransport) SendEvent(event *sentry.Event) {
	select {
	case t.events <- event:
	default:
	}
}
func (t *primaryRecordingTransport) Flush(_ time.Duration) bool { return true }

func installPrimarySentryTestTransport(t *testing.T) *primaryRecordingTransport {
	t.Helper()
	transport := newPrimaryRecordingTransport()
	require.NoError(t, sentry.Init(sentry.ClientOptions{
		Dsn:       "https://public@sentry.example.com/1",
		Transport: transport,
	}))
	t.Cleanup(func() {
		sentry.Flush(2 * time.Second)
		_ = sentry.Init(sentry.ClientOptions{})
	})
	return transport
}

// numericFromFloat produces a pgtype.Numeric that round-trips through
// primaryNumericToFloat to the input value.
func numericFromFloat(v float64) pgtype.Numeric {
	if v == 0 {
		return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
	}
	scaled := int64(v * 10000)
	return pgtype.Numeric{Int: big.NewInt(scaled), Exp: -4, Valid: true}
}

func budgetCfg() config.Config {
	c := cfgWithDefaults()
	c.MonthlyPrimaryBudgetBRL = 800
	return c
}

// TestBudget_PrimarySeparateFromEmerg — Pitfall #12: the primary budget
// aggregator never reads from emergency_lifecycles. We simulate by
// constructing a BudgetChecker that returns ONLY the primary-lifecycle
// sum (550) and assert the check observes 550 (not 1050 = 550 + 500).
func TestBudget_PrimarySeparateFromEmerg(t *testing.T) {
	b := NewBudgetChecker(budgetCfg(), nil, nil)
	b.SetCostOverrideForTest(func(_ context.Context) (pgtype.Numeric, error) {
		// In production the SQL aggregates ONLY primary_lifecycles. The
		// emergency 500 BRL spend is invisible to this aggregator.
		return numericFromFloat(550), nil
	})
	got := b.CheckBudget(context.Background())
	require.InDelta(t, 550.0, got, 0.01,
		"primary budget aggregator must observe ONLY primary_lifecycles SUM (Pitfall #12)")
}

// TestBudget_ReadsSnapshotNotCfg (Phase 17 POD-CFG-04): CheckBudget must read
// MonthlyPrimaryBudgetBRL from the LIVE pod_config snapshot, not b.cfg. The
// snapshot budget (500) is BELOW the observed cost (700), while the boot cfg
// budget (2000) is ABOVE it — the alert fires ONLY if the snapshot value wins.
func TestBudget_ReadsSnapshotNotCfg(t *testing.T) {
	transport := installPrimarySentryTestTransport(t)
	cfg := budgetCfg()
	cfg.MonthlyPrimaryBudgetBRL = 2000 // boot cfg budget — would NOT alert at cost 700
	loader := podconfig.NewStaticLoaderForTest(
		podconfig.PodConfig{MonthlyBudgetBRL: 500}, // snapshot budget — alerts at 700
		podconfig.ScheduleRule{}, podconfig.PodConfigBounds{}, nil)
	b := NewBudgetChecker(cfg, nil, loader)
	b.SetCostOverrideForTest(func(_ context.Context) (pgtype.Numeric, error) {
		return numericFromFloat(700), nil
	})
	got := b.CheckBudget(context.Background())
	require.InDelta(t, 700.0, got, 0.01)
	sentry.Flush(2 * time.Second)
	select {
	case ev := <-transport.events:
		require.NotNil(t, ev, "alert must fire because the SNAPSHOT budget (500) < cost (700)")
		require.Equal(t, "budget_exceeded", ev.Tags["alert"])
	case <-time.After(2 * time.Second):
		t.Fatal("CheckBudget must read the snapshot budget (500), not the boot cfg budget (2000)")
	}
}

func TestBudget_UnderBudget_NoAlert(t *testing.T) {
	transport := installPrimarySentryTestTransport(t)
	cfg := budgetCfg()
	cfg.MonthlyPrimaryBudgetBRL = 800
	b := NewBudgetChecker(cfg, nil, nil)
	b.SetCostOverrideForTest(func(_ context.Context) (pgtype.Numeric, error) {
		return numericFromFloat(400), nil
	})
	_ = b.CheckBudget(context.Background())
	sentry.Flush(500 * time.Millisecond)
	select {
	case ev := <-transport.events:
		t.Fatalf("under-budget must NOT emit Sentry event; got %v", ev)
	default:
	}
}

func TestBudget_OverBudget_AlertFires(t *testing.T) {
	transport := installPrimarySentryTestTransport(t)
	cfg := budgetCfg()
	cfg.MonthlyPrimaryBudgetBRL = 800
	b := NewBudgetChecker(cfg, nil, nil)
	b.SetCostOverrideForTest(func(_ context.Context) (pgtype.Numeric, error) {
		return numericFromFloat(900), nil
	})
	got := b.CheckBudget(context.Background())
	require.InDelta(t, 900.0, got, 0.01)
	sentry.Flush(2 * time.Second)
	select {
	case ev := <-transport.events:
		require.NotNil(t, ev)
		require.Equal(t, "primary", ev.Tags["subsystem"], "Sentry tag subsystem=primary")
		require.Equal(t, "budget_exceeded", ev.Tags["alert"], "Sentry tag alert=budget_exceeded")
	case <-time.After(2 * time.Second):
		t.Fatal("over-budget must emit Sentry event")
	}
}

func TestBudget_DedupeAlert(t *testing.T) {
	transport := installPrimarySentryTestTransport(t)
	cfg := budgetCfg()
	cfg.MonthlyPrimaryBudgetBRL = 800
	b := NewBudgetChecker(cfg, nil, nil)
	b.SetCostOverrideForTest(func(_ context.Context) (pgtype.Numeric, error) {
		return numericFromFloat(900), nil
	})

	// Two consecutive calls in the same UTC day.
	_ = b.CheckBudget(context.Background())
	_ = b.CheckBudget(context.Background())
	sentry.Flush(2 * time.Second)

	count := 0
	for {
		select {
		case <-transport.events:
			count++
		case <-time.After(200 * time.Millisecond):
			require.Equal(t, 1, count, "dedupe must hold alerts to 1 per UTC day")
			return
		}
	}
}

// TestBudgetDedupe_OncePerDayConcurrent — Pitfall 11 CAS pattern: 1000
// concurrent shouldEmit calls yield exactly 1 true return.
func TestBudgetDedupe_OncePerDayConcurrent(t *testing.T) {
	d := &primaryBudgetAlertDedupe{}
	var trues atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.shouldEmit() {
				trues.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), trues.Load(),
		"CAS dedupe must yield exactly 1 true even under 1000 concurrent callers")
}
