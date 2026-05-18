//go:build integration

// Phase 6.6 Plan 06.6-10 Task 1 — markReady + 3-role tier-0 override +
// DCGMScraper.SetURL + 4-endpoint reachability E2E coverage.
//
// Setup drives the full primary.Reconciler Start() path:
//   - testcontainers Postgres + miniredis (via freshSchema)
//   - fakeVastPrimary: scripted SearchOffers (1 cheap offer) +
//     CreateInstance (success) + GetInstance (running + 4 host port
//     mappings) + DestroyInstance.
//   - fakePrimaryLoader: records OverrideTier0 / RestoreTier0 calls per
//     role.
//   - fakePrimaryDCGM: records SetURL.
//   - alwaysHealthy HealthCheck closure (4-endpoint reachability passes).
//   - alwaysInPeakRule: ShouldBeProvisioned returns true so the schedule
//     loop fires evaluateAsleep → spawnProvisioning at the first tick.
//
// The Wave 0 supervisord-4-services invariant is mechanically proven by
// asserting (a) the reconciler probes all 4 derived URLs (LLM/STT/Embed/
// DCGM via Ports map), (b) Loader.OverrideTier0 fires 3x with the
// correctly-stripped base URLs, (c) DCGMScraper.SetURL receives the
// /metrics URL verbatim, (d) DB row carries first_health_pass_at != NULL,
// (e) FSM advances Provisioning → Ready.
package integration

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

// TestPrimaryProbe_MarkReady_OverridesTier03Roles_4EndpointsReachable —
// the canonical happy-path proof for Plan 06.6-06a markReady + Plan
// 06.6-06b 3-role tier-0 override + Wave 0 4-endpoint single-container
// reachability.
func TestPrimaryProbe_MarkReady_OverridesTier03Roles_4EndpointsReachable(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = false // schedule loop must drive provisioning

	// Track which URLs were probed — the Wave 0 4-endpoint
	// reachability invariant requires all 4 to be called before markReady.
	var probedMu sync.Mutex
	probedURLs := map[string]bool{}
	healthCheck := func(_ context.Context, url string) bool {
		probedMu.Lock()
		probedURLs[url] = true
		probedMu.Unlock()
		return true
	}

	loader := newFakePrimaryLoader()
	dcgm := &fakePrimaryDCGM{}
	fakeV := &fakeVastPrimary{
		SearchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
			return []vast.Offer{{ID: 9001, DphTotal: 0.30, GpuName: "RTX 4090",
				Reliability: 0.99, NumGpus: 1, HostID: 100}}, nil
		},
		CreateInstanceFn: func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
			return vast.Instance{ID: 12345}, nil
		},
		GetInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningPrimaryInstance(12345), nil
		},
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
		ReplicaID:   "test-primary-probe",
		HealthCheck: healthCheck,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// Wait for the FSM to reach Ready — proves the full pipeline:
	// schedule loop → spawnProvisioning → waitForReadyOrDestroy →
	// 4-endpoint health pass → markReady → FSM Provisioning → Ready.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateReady
	}, 20*time.Second, 100*time.Millisecond,
		"FSM must reach Ready after 4-endpoint healthy probe + markReady; got %s", fsm.State())

	// (a) Wave 0 4-endpoint reachability assertion — all 4 derived URLs
	// must have been probed.
	probedMu.Lock()
	probed := make(map[string]bool, len(probedURLs))
	for k, v := range probedURLs {
		probed[k] = v
	}
	probedMu.Unlock()
	require.True(t, probed["http://203.0.113.7:33000/v1/models"],
		"LLM /v1/models endpoint must be probed")
	require.True(t, probed["http://203.0.113.7:33001/health"],
		"STT /health endpoint must be probed")
	require.True(t, probed["http://203.0.113.7:33002/health"],
		"Embed /health endpoint must be probed")
	require.True(t, probed["http://203.0.113.7:33400/metrics"],
		"DCGM /metrics endpoint must be probed")

	// (b) 3-role tier-0 override assertion — Plan 06.6-06b contract.
	require.Eventually(t, func() bool {
		snap := loader.Snapshot()
		return len(snap) == 3
	}, 2*time.Second, 50*time.Millisecond,
		"Loader.OverrideTier0 must be called 3x (llm/stt/embed); got %v", loader.Snapshot())
	snap := loader.Snapshot()
	require.Equal(t, "http://203.0.113.7:33000", snap["llm"],
		"/v1/models suffix stripped for LLM (parity emerg stripHealthSuffix)")
	require.Equal(t, "http://203.0.113.7:33001", snap["stt"],
		"/health suffix stripped for STT")
	require.Equal(t, "http://203.0.113.7:33002", snap["embed"],
		"/health suffix stripped for embed")

	// (c) DCGMScraper.SetURL contract — Plan 06.6-06b.
	require.Equal(t, "http://203.0.113.7:33400/metrics", dcgm.Last(),
		"DCGM URL passed verbatim to scraper (NOT stripped — scraper expects /metrics)")

	// (d) DB row marked healthy.
	var firstHealth pgtype.Timestamptz
	var instID pgtype.Int8
	err := pool.QueryRow(rootCtx,
		`SELECT first_health_pass_at, vast_instance_id
		 FROM ai_gateway.primary_lifecycles WHERE ended_at IS NULL`,
	).Scan(&firstHealth, &instID)
	require.NoError(t, err)
	require.True(t, firstHealth.Valid,
		"first_health_pass_at must be NOT NULL after markReady (parity emerg first_health_pass_at)")
	require.True(t, instID.Valid)
	require.Equal(t, int64(12345), instID.Int64)

	// (e) Defensive: DestroyInstance NOT called on happy path.
	require.Equal(t, int32(0), fakeV.DestroyCalls.Load(),
		"happy path must NOT call DestroyInstance during provisioning")
}
