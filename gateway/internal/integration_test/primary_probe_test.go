//go:build integration

// Phase 6.6 Plan 06.6-10 Task 1 — markReady + tier-0 override +
// DCGMScraper.SetURL + 3-endpoint reachability E2E coverage (Phase 21:
// TTS removed → llm/stt/dcgm).
//
// Setup drives the full primary.Reconciler Start() path:
//   - testcontainers Postgres + miniredis (via freshSchema)
//   - fakeVastPrimary: scripted SearchOffers (1 cheap offer) +
//     CreateInstance (success) + GetInstance (running + 4 host port
//     mappings) + DestroyInstance.
//   - fakePrimaryLoader: records OverrideTier0 / RestoreTier0 calls per
//     role.
//   - fakePrimaryDCGM: records SetURL.
//   - alwaysHealthy HealthCheck closure (3-endpoint reachability passes).
//   - alwaysInPeakRule: ShouldBeProvisioned returns true so the schedule
//     loop fires evaluateAsleep → spawnProvisioning at the first tick.
//
// The Phase 21 supervisord-3-services invariant is mechanically proven by
// asserting (a) the reconciler probes the 3 derived URLs (LLM/STT/
// DCGM via Ports map), (b) Loader.OverrideTier0 fires 2x with the
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

// TestPrimaryProbe_MarkReady_OverridesTier02Roles_3EndpointsReachable —
// the canonical happy-path proof for Plan 06.6-06a markReady + Plan
// 06.6-06b tier-0 override + Phase 21 3-endpoint (llm/stt/dcgm) single-
// container reachability (TTS removed).
func TestPrimaryProbe_MarkReady_OverridesTier02Roles_3EndpointsReachable(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = false // schedule loop must drive provisioning

	// Track which URLs were probed — the 3-endpoint reachability
	// invariant requires all 3 to be called before markReady.
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
		Cfg:          cfg,
		Log:          slog.New(slog.DiscardHandler),
		Vast:         fakeV,
		Loader:       loader,
		DCGMScraper:  dcgm,
		FSM:          fsm,
		Rule:         alwaysInPeakRule(),
		DB:           pool,
		Redis:        rdb,
		ReplicaID:    "test-primary-probe",
		HealthCheck:  healthCheck,
		DeviceReport: cudaDeviceReport, // Phase 14: GPU pod reports cuda → stt override fires (llm/stt pair)
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// Wait for the FSM to reach Ready — proves the full pipeline:
	// schedule loop → spawnProvisioning → waitForReadyOrDestroy →
	// 3-endpoint health pass → markReady → FSM Provisioning → Ready.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateReady
	}, 20*time.Second, 100*time.Millisecond,
		"FSM must reach Ready after 3-endpoint healthy probe + markReady; got %s", fsm.State())

	// (a) 3-endpoint reachability assertion — all 3 derived URLs
	// must have been probed.
	probedMu.Lock()
	probed := make(map[string]bool, len(probedURLs))
	for k, v := range probedURLs {
		probed[k] = v
	}
	probedMu.Unlock()
	require.True(t, probed["http://203.0.113.7:33000/v1/models"],
		"LLM /v1/models endpoint must be probed")
	// Phase 21: TTS (:33003) removed from the pod — no longer probed.
	// Phase 11.2: STT :33001 endpoint restored — tier-0 STT re-added (revert 11.1 D-A1).
	require.True(t, probed["http://203.0.113.7:33001/health"],
		"STT /health endpoint must be probed post-Phase 11.2")
	require.True(t, probed["http://203.0.113.7:33400/metrics"],
		"DCGM /metrics endpoint must be probed")

	// (b) Phase 21: 2-role tier-0 override {llm,stt} — TTS removed, embed off-pod (D-03).
	require.Eventually(t, func() bool {
		snap := loader.Snapshot()
		return len(snap) == 2
	}, 2*time.Second, 50*time.Millisecond,
		"Loader.OverrideTier0 must be called 2x (llm/stt); got %v", loader.Snapshot())
	snap := loader.Snapshot()
	require.Equal(t, "http://203.0.113.7:33000", snap["llm"],
		"/v1/models suffix stripped for LLM (parity emerg stripHealthSuffix)")
	require.Equal(t, "http://203.0.113.7:33001", snap["stt"],
		"/health suffix stripped for STT (Phase 11.2 restore)")
	_, ttsSet := snap["tts"]
	require.False(t, ttsSet, "tts must NOT be a dynamic primary role (Phase 21 — TTS removed)")
	_, embedSet := snap["embed"]
	require.False(t, embedSet, "embed must NOT be a dynamic primary role (D-03)")

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
