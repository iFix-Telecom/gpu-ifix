//go:build integration

// Phase 6.6 Plan 06.6-10 Task 1 — Wave 0 supervisord multi-process
// invariants (REPLACES any pre-Wave-0 DinD-specific test).
//
// Per 06.6-SPIKE-dind-privileged.md: DinD on Vast.ai is REJECTED
// (overlayfs mount fails in nested namespace under
// `--privileged + iptables=false + bridge=none`). Strategy B-revised
// adopts supervisord as PID 1 with 3 child processes (LLM + STT +
// DCGM) sharing ONE container's network namespace, GPU device, and
// filesystem (Phase 21: TTS/Chatterbox removed from the pod).
//
// From the reconciler's perspective the orchestration mechanism is
// opaque — it polls 3 HTTP endpoints on Vast-exposed host ports
// (8000/8001/9400 → 33000/33001/33400). The invariant
// proved here is: ALL 3 ENDPOINTS MUST RESPOND HEALTHY BEFORE markReady
// fires. The supervisord 3-services contract is mechanically enforced by
// the 3-endpoint health gate.
//
// Three sub-tests cover:
//
//   - TestSupervisord_3ServicesReachableOnLocalhost: all 3 endpoints
//     healthy → markReady fires → FSM=Ready + 2 OverrideTier0 calls +
//     DCGM SetURL. This is the canonical happy path for the supervisord
//     single-container 3-services model.
//
//   - TestSupervisord_OneEndpointDown_DoesNotPromoteToReady: 2 of 3
//     endpoints healthy (STT fails). The reconciler keeps polling
//     until the cold-start budget expires; markReady is NEVER called;
//     FSM stays Provisioning until the lifecycle is closed with
//     shutdown_reason='health_timeout'.
//
//   - TestSupervisord_AutorestartSimulated_RecoveryAfterTransientFailure:
//     simulates a supervisord-driven autorestart of one child (STT
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

// TestSupervisord_3ServicesReachableOnLocalhost — happy path. All
// 3 supervisord child endpoints respond healthy from inside ONE
// container's network namespace. The reconciler routes traffic via the
// Vast-exposed host ports (33000/33001/33400). markReady fires
// once all 3 pass.
func TestSupervisord_3ServicesReachableOnLocalhost(t *testing.T) {
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

	// Mock HealthCheck — 3 supervisord children are all healthy.
	// Per-URL routing emulates the host-port → container-port mapping.
	healthChecker := func(_ context.Context, url string) bool {
		// All 3 URLs healthy: 33000/33001 + 33400 (Phase 21: no TTS 33003).
		return strings.Contains(url, ":33000") ||
			strings.Contains(url, ":33001") ||
			strings.Contains(url, ":33400")
	}

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:          cfg,
		Log:          slog.New(slog.DiscardHandler),
		Vast:         fakeV,
		Loader:       loader,
		DCGMScraper:  dcgm,
		FSM:          fsm,
		Rule:         alwaysInPeakRule(),
		DB:           pool,
		Redis:        rdb,
		ReplicaID:    "test-supervisord-happy",
		HealthCheck:  healthChecker,
		DeviceReport: cudaDeviceReport, // Phase 14: GPU pod reports cuda → stt override fires (llm/stt pair)
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// FSM reaches Ready after all 3 supervisord children probe healthy.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateReady
	}, 20*time.Second, 100*time.Millisecond,
		"FSM must reach Ready when all 3 supervisord children are healthy; got %s", fsm.State())

	// activePodURLs populated with non-empty fields — proves the
	// reconciler resolved the supervisord 3-services container ports.
	urls := r.ActivePodURLs()
	require.NotNil(t, urls, "ActivePodURLs() must be populated after markReady")

	// Phase 21: 2-role OverrideTier0 (llm/stt) — TTS removed, embed off-pod (D-03).
	require.Eventually(t, func() bool {
		return len(loader.Snapshot()) == 2
	}, 2*time.Second, 50*time.Millisecond,
		"2 OverrideTier0 calls (llm/stt) required post-Phase 21 supervisord")
	require.Contains(t, dcgm.Last(), ":33400/metrics",
		"DCGM URL must point at the 9400 supervisord child's host port")
}

// TestSupervisord_OneEndpointDown_DoesNotPromoteToReady — partial
// failure. The STT supervisord child is unhealthy; the other 2
// children pass /health. The 3-endpoint health gate requires ALL 3 to
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

	// 2 of 3 endpoints healthy. STT (8001 → 33001) is the broken child.
	healthChecker := func(_ context.Context, url string) bool {
		if strings.Contains(url, ":33001") {
			return false // STT supervisord child unhealthy
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
	// but MUST NEVER reach Ready while the STT endpoint stays unhealthy.
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
		"FSM must NOT reach Ready when 3-endpoint health gate fails")
}

// TestSupervisord_AutorestartSimulated_RecoveryAfterTransientFailure —
// supervisord's autorestart kicks the failed STT child back up. The
// reconciler's polling loop retries on each tick; the second probe pass
// finds all 3 healthy and markReady fires.
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

	// supervisord autorestart sim: the STT child fails initial probes,
	// then recovers after `sttRecoverAfter` probes.
	const sttRecoverAfter = 2
	var sttProbeCount atomic.Int32
	var mu sync.Mutex
	healthChecker := func(_ context.Context, url string) bool {
		if strings.Contains(url, ":33001") {
			// STT — fails until autorestart simulated.
			count := sttProbeCount.Add(1)
			mu.Lock()
			defer mu.Unlock()
			return count > sttRecoverAfter
		}
		return true // LLM/DCGM always healthy
	}

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:          cfg,
		Log:          slog.New(slog.DiscardHandler),
		Vast:         fakeV,
		Loader:       loader,
		DCGMScraper:  dcgm,
		FSM:          fsm,
		Rule:         alwaysInPeakRule(),
		DB:           pool,
		Redis:        rdb,
		ReplicaID:    "test-supervisord-autorestart",
		HealthCheck:  healthChecker,
		DeviceReport: cudaDeviceReport, // Phase 14: GPU pod reports cuda → stt override fires after autorestart (llm/stt pair)
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// After autorestart, all 3 endpoints pass + markReady fires + FSM
	// transitions to Ready.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateReady
	}, 25*time.Second, 100*time.Millisecond,
		"FSM must reach Ready after STT autorestart completes; got %s", fsm.State())

	// STT was probed at least sttRecoverAfter+1 times (the recovery
	// probe + earlier failed ones).
	require.GreaterOrEqual(t, sttProbeCount.Load(), int32(sttRecoverAfter+1),
		"STT health probe must be retried at least %d times to observe autorestart", sttRecoverAfter+1)

	// Phase 21: 2-role tier-0 override (llm/stt) — same contract as the
	// happy-path test, post-recovery.
	require.Eventually(t, func() bool {
		return len(loader.Snapshot()) == 2
	}, 2*time.Second, 50*time.Millisecond)
	require.Contains(t, dcgm.Last(), ":33400/metrics")
}
