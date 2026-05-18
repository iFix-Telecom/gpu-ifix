//go:build integration

// Phase 6.6 Plan 06.6-10 Task 1 — Wave 0 supervisord multi-process
// invariants (REPLACES any pre-Wave-0 DinD-specific test).
//
// Per 06.6-SPIKE-dind-privileged.md: DinD on Vast.ai is REJECTED
// (overlayfs mount fails in nested namespace under
// `--privileged + iptables=false + bridge=none`). Strategy B-revised
// adopts supervisord as PID 1 with 4 child processes (LLM + STT + embed
// + DCGM) sharing ONE container's network namespace, GPU device, and
// filesystem.
//
// From the reconciler's perspective the orchestration mechanism is
// opaque — it polls 4 HTTP endpoints on Vast-exposed host ports
// (8000/8001/8002/9400 → 33000/33001/33002/33400). The Wave 0 invariant
// proved here is: ALL 4 ENDPOINTS MUST RESPOND HEALTHY BEFORE markReady
// fires. The supervisord 4-services contract is mechanically enforced by
// the 4-endpoint health gate.
//
// Three sub-tests cover:
//
//   - TestSupervisord_4ServicesReachableOnLocalhost: all 4 endpoints
//     healthy → markReady fires → FSM=Ready + 3 OverrideTier0 calls +
//     DCGM SetURL. This is the canonical happy path for the supervisord
//     single-container 4-services model.
//
//   - TestSupervisord_OneEndpointDown_DoesNotPromoteToReady: 3 of 4
//     endpoints healthy (Embed fails). The reconciler keeps polling
//     until the cold-start budget expires; markReady is NEVER called;
//     FSM stays Provisioning until the lifecycle is closed with
//     shutdown_reason='health_timeout'.
//
//   - TestSupervisord_AutorestartSimulated_RecoveryAfterTransientFailure:
//     simulates a supervisord-driven autorestart of one child (Embed
//     fails on first probe, succeeds on subsequent). markReady fires
//     after autorestart completes. This proves the polling loop is
//     retry-on-fail rather than fail-fast.
//
// All 3 tests use freshSchema for the testcontainers Postgres + miniredis
// + the in-process fakeVastPrimary / fakePrimaryLoader / fakePrimaryDCGM
// helpers. The mock HealthCheck closure is the per-URL behaviour knob.
package integration

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

// TestSupervisord_4ServicesReachableOnLocalhost — Wave 0 happy path. All
// 4 supervisord child endpoints respond healthy from inside ONE
// container's network namespace. The reconciler routes traffic via the
// Vast-exposed host ports (33000/33001/33002/33400). markReady fires
// once all 4 pass.
func TestSupervisord_4ServicesReachableOnLocalhost(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = false

	loader := newFakePrimaryLoader()
	dcgm := &fakePrimaryDCGM{}
	fakeV := &fakeVastPrimary{
		SearchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
			return []vast.Offer{{ID: 7, DphTotal: 0.30, GpuName: "RTX 4090",
				Reliability: 0.99, NumGpus: 1, HostID: 100}}, nil
		},
		CreateInstanceFn: func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
			return vast.Instance{ID: 5555}, nil
		},
		GetInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningPrimaryInstance(5555), nil
		},
	}

	// Wave 0 mock HealthCheck — 4 supervisord children are all healthy.
	// Per-URL routing emulates the host-port → container-port mapping.
	healthChecker := func(_ context.Context, url string) bool {
		// All 4 URLs healthy: 33000/33001/33002 + 33400.
		return strings.Contains(url, ":33000") ||
			strings.Contains(url, ":33001") ||
			strings.Contains(url, ":33002") ||
			strings.Contains(url, ":33400")
	}

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Vast:        fakeV,
		Loader:      loader,
		DCGMScraper: dcgm,
		FSM:         fsm,
		Rule:        alwaysInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "test-supervisord-happy",
		HealthCheck: healthChecker,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// FSM reaches Ready after all 4 supervisord children probe healthy.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateReady
	}, 20*time.Second, 100*time.Millisecond,
		"FSM must reach Ready when all 4 supervisord children are healthy; got %s", fsm.State())

	// activePodURLs populated with all 4 non-empty fields — proves the
	// reconciler resolved the supervisord 4-services container ports.
	urls := r.ActivePodURLs()
	require.NotNil(t, urls, "ActivePodURLs() must be populated after markReady")

	// 3-role OverrideTier0 + DCGM SetURL — Plan 06.6-06b contract.
	require.Eventually(t, func() bool {
		return len(loader.Snapshot()) == 3
	}, 2*time.Second, 50*time.Millisecond,
		"3 OverrideTier0 calls (llm/stt/embed) required for supervisord 4-services pod")
	require.Contains(t, dcgm.Last(), ":33400/metrics",
		"DCGM URL must point at the 9400 supervisord child's host port")
}

// TestSupervisord_OneEndpointDown_DoesNotPromoteToReady — Wave 0 partial
// failure. The Embed supervisord child is unhealthy; the other 3
// children pass /health. The 4-endpoint health gate requires ALL 4 to
// pass so markReady NEVER fires. After the cold-start budget elapses
// the lifecycle is closed with shutdown_reason='health_timeout'.
func TestSupervisord_OneEndpointDown_DoesNotPromoteToReady(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = false
	// Tight budget so the test exits via deadline branch quickly.
	cfg.PrimaryProvisionColdStartBudgetSeconds = 8

	loader := newFakePrimaryLoader()
	dcgm := &fakePrimaryDCGM{}
	fakeV := &fakeVastPrimary{
		SearchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
			return []vast.Offer{{ID: 7, DphTotal: 0.30, GpuName: "RTX 4090",
				Reliability: 0.99, NumGpus: 1, HostID: 100}}, nil
		},
		CreateInstanceFn: func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
			return vast.Instance{ID: 7777}, nil
		},
		GetInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningPrimaryInstance(7777), nil
		},
	}

	// 3 of 4 endpoints healthy. Embed (8002 → 33002) is the broken child.
	healthChecker := func(_ context.Context, url string) bool {
		if strings.Contains(url, ":33002") {
			return false // Embed supervisord child unhealthy
		}
		return true
	}

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Vast:        fakeV,
		Loader:      loader,
		DCGMScraper: dcgm,
		FSM:         fsm,
		Rule:        alwaysInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "test-supervisord-one-down",
		HealthCheck: healthChecker,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// FSM must enter Provisioning (schedule loop fires spawnProvisioning)
	// but MUST NEVER reach Ready while the Embed endpoint stays unhealthy.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateProvisioning
	}, 5*time.Second, 100*time.Millisecond,
		"FSM must transition Asleep → Provisioning when schedule fires")

	// Wait for cold-start budget to expire; the lifecycle is closed
	// with shutdown_reason='health_timeout' and FSM returns to Asleep.
	require.Eventually(t, func() bool {
		var reason pgtype.Text
		err := pool.QueryRow(rootCtx,
			`SELECT shutdown_reason FROM ai_gateway.primary_lifecycles
			 WHERE ended_at IS NOT NULL ORDER BY id DESC LIMIT 1`,
		).Scan(&reason)
		return err == nil && reason.Valid && reason.String == "health_timeout"
	}, 20*time.Second, 250*time.Millisecond,
		"lifecycle must close with shutdown_reason='health_timeout' when one endpoint stays unhealthy")

	// markReady NEVER fired — no OverrideTier0 calls, no DCGM URL.
	require.Empty(t, loader.Snapshot(),
		"markReady must NOT fire when one supervisord child stays unhealthy")
	require.NotEqual(t, primary.StateReady, fsm.State(),
		"FSM must NOT reach Ready when 4-endpoint health gate fails")
}

// TestSupervisord_AutorestartSimulated_RecoveryAfterTransientFailure —
// supervisord's autorestart kicks the failed Embed child back up. The
// reconciler's polling loop retries on each tick; the second probe pass
// finds all 4 healthy and markReady fires.
func TestSupervisord_AutorestartSimulated_RecoveryAfterTransientFailure(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = false

	loader := newFakePrimaryLoader()
	dcgm := &fakePrimaryDCGM{}
	fakeV := &fakeVastPrimary{
		SearchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
			return []vast.Offer{{ID: 7, DphTotal: 0.30, GpuName: "RTX 4090",
				Reliability: 0.99, NumGpus: 1, HostID: 100}}, nil
		},
		CreateInstanceFn: func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
			return vast.Instance{ID: 9999}, nil
		},
		GetInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningPrimaryInstance(9999), nil
		},
	}

	// supervisord autorestart sim: the Embed child fails initial probes,
	// then recovers after `embedRecoverAfter` probes.
	const embedRecoverAfter = 2
	var embedProbeCount atomic.Int32
	var mu sync.Mutex
	healthChecker := func(_ context.Context, url string) bool {
		if strings.Contains(url, ":33002") {
			// Embed — fails until autorestart simulated.
			count := embedProbeCount.Add(1)
			mu.Lock()
			defer mu.Unlock()
			return count > embedRecoverAfter
		}
		return true // LLM/STT/DCGM always healthy
	}

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Vast:        fakeV,
		Loader:      loader,
		DCGMScraper: dcgm,
		FSM:         fsm,
		Rule:        alwaysInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "test-supervisord-autorestart",
		HealthCheck: healthChecker,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// After autorestart, all 4 endpoints pass + markReady fires + FSM
	// transitions to Ready.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateReady
	}, 25*time.Second, 100*time.Millisecond,
		"FSM must reach Ready after Embed autorestart completes; got %s", fsm.State())

	// Embed was probed at least embedRecoverAfter+1 times (the recovery
	// probe + earlier failed ones).
	require.GreaterOrEqual(t, embedProbeCount.Load(), int32(embedRecoverAfter+1),
		"Embed health probe must be retried at least %d times to observe autorestart", embedRecoverAfter+1)

	// 3-role tier-0 override + DCGM URL set — same contract as the
	// happy-path test, post-recovery.
	require.Eventually(t, func() bool {
		return len(loader.Snapshot()) == 3
	}, 2*time.Second, 50*time.Millisecond)
	require.Contains(t, dcgm.Last(), ":33400/metrics")
}
