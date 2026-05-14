//go:build integration

// Phase 6 Plan 06-09 — D-D2 monthly budget alert integration test
// (PRV-05). Pre-seeds the emergency_lifecycles table with R$199 worth
// of closed lifecycles in the current month, then drives a fresh
// lifecycle through the happy path. After it closes, the SUM crosses
// the R$200 budget and the reconciler MUST emit a Sentry warning
// tagged `subsystem=emerg / alert=budget_exceeded` with extras
// `month_cost_brl > 200` and `budget_brl == 200`.
//
// Sentry capture uses a process-wide test transport (recordingTransport
// from this file) installed via sentry.Init — same pattern as the unit
// tests in budget_test.go but here the events flow through the real
// reconciler tick wrapper (60s gating bypassed via a direct
// checkBudget call after the lifecycle closes).
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	sentry "github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// recordingSentryTransport captures every SendEvent for assertion.
// Local copy of the unit-test helper so this file does not import the
// emerg package's _test.go (impossible across packages anyway). Buffer
// 16 is generous — the test only emits 1 event.
type recordingSentryTransport struct {
	events chan *sentry.Event
}

func newRecordingSentryTransport() *recordingSentryTransport {
	return &recordingSentryTransport{events: make(chan *sentry.Event, 16)}
}

func (t *recordingSentryTransport) Configure(_ sentry.ClientOptions) {}
func (t *recordingSentryTransport) SendEvent(event *sentry.Event) {
	select {
	case t.events <- event:
	default:
	}
}
func (t *recordingSentryTransport) Flush(_ time.Duration) bool { return true }

// installSentryTestTransport replaces the process-wide hub with a
// fresh client wired to recordingSentryTransport. t.Cleanup restores
// an empty hub so concurrent integration tests do not see leakage.
func installSentryTestTransportIntegration(t *testing.T) *recordingSentryTransport {
	t.Helper()
	transport := newRecordingSentryTransport()
	err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://public@sentry.example.com/1",
		Transport: transport,
	})
	require.NoError(t, err, "sentry.Init")
	t.Cleanup(func() {
		sentry.Flush(2 * time.Second)
		_ = sentry.Init(sentry.ClientOptions{})
	})
	return transport
}

// TestEmergBudgetAlert — pre-seed R$199 of month-to-date closed cost,
// drive a happy lifecycle to completion (close adds R$5+), then
// directly invoke checkBudget (bypassing the 60s tick gate) and assert
// the Sentry hub captured a `budget_exceeded` event.
func TestEmergBudgetAlert(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)
	cfg.MonthlyEmergencyBudgetBRL = 200.0

	// Pre-seed lifecycles totalling R$199 in the current month. Use
	// `started_at = date_trunc('month', NOW())` so they fall inside the
	// SUM aggregate window.
	_, err := pool.Exec(rootCtx, `
		INSERT INTO ai_gateway.emergency_lifecycles
		  (started_at, ended_at, trigger_reason, shutdown_reason,
		   total_cost_brl, leader_replica)
		VALUES
		  (date_trunc('month', NOW()),
		   date_trunc('month', NOW()) + interval '1 hour',
		   'manual_force', 'cutback_idle', 199.0, 'seed-replica')
	`)
	require.NoError(t, err, "seed pre-existing lifecycle")

	// Install Sentry test transport BEFORE building the reconciler so any
	// hub clones in NewReconciler / startProvisioning see the test client.
	transport := installSentryTestTransportIntegration(t)

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{
			Name: "local-llm", Role: "llm", Tier: 0, URL: "http://primary:8000",
			Enabled: true,
		},
	)

	mock := newMockVastServer(t)
	offers := []vast.Offer{{
		ID: 9001, DphTotal: 0.35, GpuName: "RTX 4090", Reliability: 0.99,
		HostID: 100, MachineID: 50, NumGpus: 1,
	}}
	mock.searchResponse.Store(&offers)
	inst := vast.Instance{
		ID: 12345, ActualStatus: "running", PublicIPAddr: "127.0.0.1",
		Ports: map[string][]vast.PortBinding{
			"9100/tcp": {{HostIP: "0.0.0.0", HostPort: "40713"}},
		},
		HostID: 100, DphTotal: 0.35,
	}
	mock.getResponse.Store(&inst)

	vastClient := vast.NewClientWithBaseURL("test-key", mock.Server.URL)
	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		Vast:         vastClient,
		Loader:       loader,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	r.SetHealthCheck(func(_ context.Context, _ string) bool { return true })

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	require.True(t, waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader),
		"reconciler did not acquire leadership within 3s")

	// Drain any startup-time events (none expected, but be defensive
	// against future Sentry breadcrumbs that might land here).
	drainEvents(transport)

	// Drive HEALTHY → EmergencyActive.
	publishBreakerEvent(t, rdb, "local-llm", "open")
	require.True(t, waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}), "FSM did not reach EmergencyActive within 15s; got %s", fsm.State())

	// Sleep ~1.5s so total_cost_brl on close is non-zero (cost = dph *
	// hours_active * USDToBRLRate; dph=0.35, USD->BRL=5, hours~0.0004
	// → cost ~R$0.001 — close to zero but positive). The seed already
	// set R$199 so we just need ANY positive contribution to push us
	// over R$200. We bypass the strict requirement by manually adding
	// R$5 via a second seed AFTER the lifecycle closes; the production
	// path's actual contribution is irrelevant — what matters is that
	// the SUM at checkBudget time is > budget.
	time.Sleep(500 * time.Millisecond)

	// Trigger cutback → idle-grace destroy → close.
	publishBreakerEvent(t, rdb, "local-llm", "closed")
	require.True(t, waitFor(t, 10*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateCooldown
	}), "FSM did not reach Cooldown within 10s; got %s", fsm.State())

	// Now the live lifecycle has been closed via destroyAndCloseLifecycle.
	// Top up an additional R$5 so the SUM strictly exceeds R$200 even if
	// the close path's tiny computed total_cost_brl rounds to ~0. This
	// keeps the test deterministic regardless of how long the cold-start
	// + idle-grace took — what we are testing is the alert PATH, not the
	// cost arithmetic (covered by the unit tests + lifecycle_test).
	_, err = pool.Exec(ctx, `
		INSERT INTO ai_gateway.emergency_lifecycles
		  (started_at, ended_at, trigger_reason, shutdown_reason,
		   total_cost_brl, leader_replica)
		VALUES
		  (date_trunc('month', NOW()),
		   date_trunc('month', NOW()) + interval '1 hour',
		   'manual_force', 'cutback_idle', 5.0, 'seed-replica')
	`)
	require.NoError(t, err, "top-up seed for budget alert deterministic test")

	// Drive a checkBudget directly. Use the production code path via
	// the public Reconciler — but the 60s gate in runOneTick may not
	// have elapsed yet. The simplest deterministic trigger is to wait
	// for the reconciler tick to fire checkBudget naturally OR to call
	// the public CheckBudget hook (added in Plan 06-09 for ops parity
	// with gatewayctl emerg-state).
	//
	// Per Plan 06-09 design: the 60s gate IS the production gate. To
	// avoid waiting 60s in test, we directly invoke a public test-only
	// helper exposed on the reconciler.
	r.CheckBudgetForTest(ctx)
	sentry.Flush(2 * time.Second)

	// Assert: at least 1 Sentry event arrived with the expected tags.
	select {
	case ev := <-transport.events:
		require.Equal(t, "emerg", ev.Tags["subsystem"],
			"Sentry tag subsystem must be 'emerg'")
		require.Equal(t, "budget_exceeded", ev.Tags["alert"],
			"Sentry tag alert must be 'budget_exceeded'")
		mc, ok := ev.Extra["month_cost_brl"].(float64)
		require.True(t, ok, "Sentry extra month_cost_brl must be float64; got %T", ev.Extra["month_cost_brl"])
		require.Greater(t, mc, 200.0, "month_cost_brl must exceed budget threshold")
		require.Equal(t, 200.0, ev.Extra["budget_brl"],
			"Sentry extra budget_brl must equal MonthlyEmergencyBudgetBRL=200")
		require.NotEmpty(t, ev.Message, "Sentry event Message must be non-empty")
	case <-time.After(3 * time.Second):
		t.Fatal("expected Sentry event with alert=budget_exceeded; none received in 3s")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// drainEvents flushes any pending events without blocking.
func drainEvents(t *recordingSentryTransport) {
	for {
		select {
		case <-t.events:
		default:
			return
		}
	}
}
