//go:build integration

// Phase 6 Plan 06-06 — emergency provisioning happy path + price cap +
// bid race retry (PRV-01 / PRV-05 / PRV-06 / PRV-07; SC-1 + SC-5).
//
// Three integration scenarios drive the in-process reconciler against a
// mock Vast.ai HTTP server (httptest.Server) + a stubbed health-check
// function. The DB + Redis come from `freshSchema` (testcontainers).
//
//   - TestEmergProvisionHappyPath
//     mock Vast returns 1 offer dph=0.35 → CreateInstance returns
//     new_contract=12345 → GetInstance returns running + ports populated →
//     stub healthCheck returns true → reconciler reaches StateEmergencyActive
//     and the lifecycle row carries vast_instance_id=12345 +
//     first_health_pass_at NOT NULL. SC-1 / PRV-07 evidence.
//
//   - TestEmergPriceCap
//     mock Vast returns offers [0.45, 0.35]. The reconciler MUST only
//     CreateInstance for the 0.35 offer (price cap epsilon rejects 0.45).
//     Asserts createHits == 1 + the offer ID forwarded matches the
//     0.35 offer. SC-5 evidence.
//
//   - TestEmergBidRaceLost
//     mock Vast returns valid search but every CreateInstance fails with
//     404+no_such_ask. After 3 attempts (D-A3), the reconciler closes the
//     lifecycle with shutdown_reason='offer_race_lost' and the FSM
//     transitions back to HEALTHY (provisionLifecycle returns ErrOfferRaceLost).
//
// All tests use a vast.NewClientWithBaseURL pointed at the mock; the
// reconciler's `SetVastClient(...)` injection slot keeps the production
// auto-build path untouched.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// mockVastServer wraps an httptest.Server with atomic counters so each
// test can assert how many times each endpoint was hit.
type mockVastServer struct {
	*httptest.Server
	searchHits        atomic.Int64
	createHits        atomic.Int64
	destroyHits       atomic.Int64
	statusHits        atomic.Int64
	searchResponse    atomic.Pointer[[]vast.Offer]
	createStatus      atomic.Int32 // HTTP status to return on PUT /asks; 0 → 200
	getResponse       atomic.Pointer[vast.Instance]
	lastCreateOfferID atomic.Int64
}

func newMockVastServer(t *testing.T) *mockVastServer {
	t.Helper()
	m := &mockVastServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/bundles"):
			m.searchHits.Add(1)
			offers := m.searchResponse.Load()
			payload := map[string]any{"offers": []vast.Offer{}}
			if offers != nil {
				payload["offers"] = *offers
			}
			_ = json.NewEncoder(w).Encode(payload)

		case strings.HasPrefix(r.URL.Path, "/asks/") && r.Method == http.MethodPut:
			m.createHits.Add(1)
			// Extract offer ID from /asks/{id}/.
			parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
			if len(parts) > 0 {
				if id, err := parseInt64(parts[len(parts)-1]); err == nil {
					m.lastCreateOfferID.Store(id)
				}
			}
			status := int(m.createStatus.Load())
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			if status == http.StatusOK {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success":      true,
					"new_contract": 12345,
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": false, "error": "no_such_ask", "msg": "already taken",
				})
			}

		case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == http.MethodDelete:
			m.destroyHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})

		case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == http.MethodGet:
			m.statusHits.Add(1)
			payload := map[string]any{"instances": nil}
			if inst := m.getResponse.Load(); inst != nil {
				payload["instances"] = inst
			}
			_ = json.NewEncoder(w).Encode(payload)

		case r.URL.Path == "/users/current":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "email": "test@ifix"})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.Server.Close)
	return m
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// TestEmergProvisionHappyPath — SC-1 + PRV-07.
func TestEmergProvisionHappyPath(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	mock := newMockVastServer(t)
	offers := []vast.Offer{{
		ID: 9001, DphTotal: 0.35, GpuName: "RTX 4090", Reliability: 0.99,
		HostID: 100, MachineID: 50, InetDown: 5000, CudaMaxGood: 12.6, NumGpus: 1,
	}}
	mock.searchResponse.Store(&offers)
	// Running instance with populated ports (W6 satisfied).
	inst := vast.Instance{
		ID: 12345, ActualStatus: "running", IntendedStatus: "running",
		PublicIPAddr: "127.0.0.1",
		Ports: map[string][]vast.PortBinding{
			"9100/tcp": {{HostIP: "0.0.0.0", HostPort: "40713"}},
		},
		HostID: 100, DphTotal: 0.35, MachineID: 50,
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
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	// Stub /health → always healthy. The HTTP probe path is exercised by
	// the unit test for podHealthURL + checkHealth shape.
	r.SetHealthCheck(func(_ context.Context, _ string) bool { return true })

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	// Drive the trigger: sustained local-llm OPEN.
	publishBreakerEvent(t, rdb, "local-llm", "open")

	// Eventually FSM reaches EmergencyActive — proves the entire flow
	// (search → create → poll → /health pass → markHealthy).
	if !waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}) {
		t.Fatalf("FSM did not reach EmergencyActive within 15s; got %s", fsm.State())
	}

	// Audit invariants on the live lifecycle row.
	var instID pgtype.Int8
	var firstHealth pgtype.Timestamptz
	var triggerReason string
	err := pool.QueryRow(ctx,
		`SELECT vast_instance_id, first_health_pass_at, trigger_reason
		 FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL`,
	).Scan(&instID, &firstHealth, &triggerReason)
	require.NoError(t, err)
	require.True(t, instID.Valid)
	require.Equal(t, int64(12345), instID.Int64, "vast_instance_id must match mock new_contract")
	require.True(t, firstHealth.Valid, "first_health_pass_at must be NOT NULL after /health pass (D-D4)")
	require.Equal(t, "failed_over_sustained", triggerReason)

	// ActivePodURL exposed for dispatcher (Plan 08).
	url, ok := r.ActivePodURL()
	require.True(t, ok)
	require.Equal(t, "http://127.0.0.1:40713/health", url)

	// Counters: search ≥1, create==1, destroy==0 (happy path).
	require.GreaterOrEqual(t, mock.searchHits.Load(), int64(1))
	require.Equal(t, int64(1), mock.createHits.Load(),
		"happy path must call CreateInstance exactly once")
	require.Equal(t, int64(0), mock.destroyHits.Load(),
		"healthy lifecycle must NOT trigger DestroyInstance during provisioning")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestEmergPriceCap — SC-5: an offer above the cap (with epsilon) MUST
// NOT be passed to CreateInstance.
func TestEmergPriceCap(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)
	cfg.VastPriceCapDPH = 0.40

	mock := newMockVastServer(t)
	// Two offers; only the 0.35 one is below cap (0.45 is above 0.40+0.0001 epsilon).
	offers := []vast.Offer{
		{ID: 8001, DphTotal: 0.45, GpuName: "RTX 4090", Reliability: 0.99, NumGpus: 1, HostID: 100},
		{ID: 8002, DphTotal: 0.35, GpuName: "RTX 4090", Reliability: 0.99, NumGpus: 1, HostID: 200},
	}
	mock.searchResponse.Store(&offers)
	inst := vast.Instance{
		ID: 12345, ActualStatus: "running", PublicIPAddr: "127.0.0.1",
		Ports: map[string][]vast.PortBinding{
			"9100/tcp": {{HostIP: "0.0.0.0", HostPort: "40713"}},
		},
		HostID: 200,
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
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	r.SetHealthCheck(func(_ context.Context, _ string) bool { return true })

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	publishBreakerEvent(t, rdb, "local-llm", "open")

	if !waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}) {
		t.Fatalf("FSM did not reach EmergencyActive within 15s; got %s", fsm.State())
	}

	// CreateInstance was called exactly once AND the offer ID was 8002
	// (the 0.35 offer, NOT the 0.45 above-cap offer).
	require.Equal(t, int64(1), mock.createHits.Load(),
		"price cap must NOT allow CreateInstance for the 0.45 offer")
	require.Equal(t, int64(8002), mock.lastCreateOfferID.Load(),
		"the lifecycle must bid on the cheapest below-cap offer (8002), not the above-cap (8001)")

	// DB row carries dph=0.35, not 0.45.
	var dph pgtype.Numeric
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT accepted_dph FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL`,
	).Scan(&dph))
	require.True(t, dph.Valid)
	dphFloat, err := dph.Float64Value()
	require.NoError(t, err)
	require.True(t, dphFloat.Valid)
	require.InDelta(t, 0.35, dphFloat.Float64, 0.0001)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestEmergBidRaceLost — D-A3: every CreateInstance returns 404, after 3
// attempts the lifecycle aborts with shutdown_reason='offer_race_lost'
// and the FSM returns to HEALTHY.
//
// We accelerate the test by leaving the search/create returning HTTP 404.
// The 2s/4s/8s backoff means the test budgets ~15s wall time (acceptable
// per RESEARCH Pitfall 13 — under 60s).
func TestEmergBidRaceLost(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	mock := newMockVastServer(t)
	offers := []vast.Offer{{
		ID: 9001, DphTotal: 0.35, GpuName: "RTX 4090", Reliability: 0.99,
		HostID: 100, NumGpus: 1,
	}}
	mock.searchResponse.Store(&offers)
	// Force every PUT /asks/ to return 404+no_such_ask.
	mock.createStatus.Store(http.StatusNotFound)

	vastClient := vast.NewClientWithBaseURL("test-key", mock.Server.URL)
	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		Vast:         vastClient,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	publishBreakerEvent(t, rdb, "local-llm", "open")

	// After 3 race losses (with 2s/4s/8s backoff), provisionLifecycle
	// returns ErrOfferRaceLost and startProvisioning transitions FSM
	// EmergencyProvisioning → Healthy. Budget 30s (sum of 2+4+8 backoffs
	// + scheduling slack).
	if !waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		return mock.createHits.Load() >= 3 && fsm.State() == emerg.StateHealthy
	}) {
		t.Fatalf("FSM did not return to Healthy after 3 bid race losses; "+
			"got fsm=%s create_hits=%d", fsm.State(), mock.createHits.Load())
	}
	require.GreaterOrEqual(t, mock.createHits.Load(), int64(3),
		"D-A3 requires 3 attempts before abort")

	// The lifecycle row was closed with shutdown_reason='offer_race_lost'.
	var reason pgtype.Text
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT shutdown_reason FROM ai_gateway.emergency_lifecycles
		 WHERE trigger_reason = 'failed_over_sustained' ORDER BY id DESC LIMIT 1`,
	).Scan(&reason))
	require.True(t, reason.Valid)
	require.Equal(t, "offer_race_lost", reason.String)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
