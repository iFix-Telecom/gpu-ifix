//go:build integration

// Phase 6 Plan 06-07 Task 1 — D-C3 cancel-in-flight, post-create branch
// (PRV-09 / SC-3 evidence #2).
//
// Setup:
//   - mockVastServer: CreateInstance returns 200+new_contract=12345
//     IMMEDIATELY (no spin); GetInstance returns actual_status="loading"
//     (NOT running), so waitForReadyOrDestroy is stuck in the poll loop.
//
// Flow:
//  1. publishBreakerEvent("local-llm","open") → trigger sustains; FSM
//     advances to EMERGENCY_PROVISIONING; provisionLifecycle calls
//     SearchOffers + CreateInstance (succeeds — instance 12345 created
//     in-flight per Vast).
//  2. waitForReadyOrDestroy enters poll loop; GetInstance keeps returning
//     loading (no /health URL formed → keep polling).
//  3. publishBreakerEvent("local-llm","closed") → reconciler tick observes
//     tracker.State()=="closed" + activeLifecycle non-nil + FSM in
//     EmergencyProvisioning; calls cancelActiveLifecycle.
//  4. Layer 1 ctx cancel → waitForReadyOrDestroy hits ctx.Done() →
//     bestEffortDestroy(12345) (Layer 3) + closeLifecycle('cancelled_in_flight').
//
// Assertions:
//   - createSucceededHits >= 1 (instance was created before cancel arrived)
//   - destroyHits == 1 (Layer 3 fired — the instance was destroyed before
//     return)
//   - DB row carries shutdown_reason='cancelled_in_flight'
package integration

import (
	"context"
	"encoding/json"
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

// loadingMockVastServer wraps mockVastServer such that CreateInstance
// succeeds immediately (200) but GetInstance always returns loading —
// the lifecycle stays stuck in waitForReadyOrDestroy poll loop until the
// cancel signal forces it to exit through the ctx.Done() branch.
type loadingMockVastServer struct {
	*httptest.Server
	searchHits          atomic.Int64
	createSucceededHits atomic.Int64
	destroyHits         atomic.Int64
	statusHits          atomic.Int64
	searchResponse      atomic.Pointer[[]vast.Offer]
}

func newLoadingMockVastServer(t *testing.T) *loadingMockVastServer {
	t.Helper()
	m := &loadingMockVastServer{}
	loadingInst := vast.Instance{
		ID:           12345,
		ActualStatus: "loading", // NOT running — keeps poll loop spinning
		PublicIPAddr: "",        // W6: empty IP triggers "keep polling" path
	}
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
			m.createSucceededHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":      true,
				"new_contract": 12345,
			})

		case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == http.MethodDelete:
			m.destroyHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})

		case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == http.MethodGet:
			m.statusHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"instances": loadingInst})

		case r.URL.Path == "/users/current":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "email": "test@ifix"})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.Server.Close)
	return m
}

// TestEmergCancelPostCreate — D-C3 layer 3 (post-create destroy) fires
// when cancel arrives AFTER CreateInstance succeeded but BEFORE the pod
// became healthy. Asserts:
//   - createSucceededHits >= 1 (instance was created)
//   - destroyHits == 1 (instance was destroyed before lifecycle close)
//   - DB row carries shutdown_reason='cancelled_in_flight'
func TestEmergCancelPostCreate(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)
	// Override the cold-start budget upward so the deadline doesn't fire
	// before our cancel signal can land. defaultTestCfg sets it to 5s
	// which is comfortably > our cancel window (~1s after CreateInstance).
	cfg.ProvisionColdStartBudgetSeconds = 30

	mock := newLoadingMockVastServer(t)
	offers := []vast.Offer{{
		ID: 9001, DphTotal: 0.35, GpuName: "RTX 4090", Reliability: 0.99,
		HostID: 100, MachineID: 50, InetDown: 5000, CudaMaxGood: 12.6, NumGpus: 1,
	}}
	mock.searchResponse.Store(&offers)

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

	// Trigger: sustained OPEN.
	publishBreakerEvent(t, rdb, "local-llm", "open")

	// Wait until CreateInstance has succeeded AND we are inside
	// waitForReadyOrDestroy poll loop (≥ 1 GetInstance hit).
	if !waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return mock.createSucceededHits.Load() >= 1 && mock.statusHits.Load() >= 1
	}) {
		t.Fatalf("CreateInstance + first GetInstance did not occur within 15s; "+
			"searchHits=%d createSucceededHits=%d statusHits=%d fsm=%s",
			mock.searchHits.Load(), mock.createSucceededHits.Load(),
			mock.statusHits.Load(), fsm.State())
	}

	// Now publish CLOSED — cancel detection fires on next reconciler tick.
	publishBreakerEvent(t, rdb, "local-llm", "closed")

	// Eventually destroy fires (Layer 3) AND the row is closed. Budget 10s
	// — cancel detection (100ms tick) + waitForReadyOrDestroy ctx.Done()
	// switch (next 5s poll tick or sooner via select), bestEffortDestroy
	// (sub-second), closeLifecycle (sub-second).
	if !waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return mock.destroyHits.Load() >= 1
	}) {
		t.Fatalf("DestroyInstance was not called within 15s; "+
			"createSucceededHits=%d destroyHits=%d fsm=%s",
			mock.createSucceededHits.Load(), mock.destroyHits.Load(), fsm.State())
	}

	// Wait for lifecycle row to be closed.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		var endedAt pgtype.Timestamptz
		err := pool.QueryRow(ctx,
			`SELECT ended_at FROM ai_gateway.emergency_lifecycles
			 WHERE trigger_reason = 'failed_over_sustained' ORDER BY id DESC LIMIT 1`,
		).Scan(&endedAt)
		return err == nil && endedAt.Valid
	}) {
		t.Fatalf("lifecycle row was not closed within 5s")
	}

	// Critical D-C3 layer 3 invariant: instance was created AND destroyed.
	require.GreaterOrEqual(t, mock.createSucceededHits.Load(), int64(1),
		"D-C3 post-create: at least one CreateInstance must have succeeded")
	require.Equal(t, int64(1), mock.destroyHits.Load(),
		"D-C3 post-create: Layer 3 destroy MUST fire exactly once after cancel")

	// shutdown_reason='cancelled_in_flight' (waitForReadyOrDestroy ctx.Done()
	// branch is the canonical post-create cancel path).
	var reason pgtype.Text
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT shutdown_reason FROM ai_gateway.emergency_lifecycles
		 WHERE trigger_reason = 'failed_over_sustained' ORDER BY id DESC LIMIT 1`,
	).Scan(&reason))
	require.True(t, reason.Valid)
	require.Equal(t, "cancelled_in_flight", reason.String,
		"shutdown_reason must be cancelled_in_flight (Layer 3 path)")

	// Best-effort: FSM returns to Healthy.
	_ = waitFor(t, 3*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateHealthy
	})

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
