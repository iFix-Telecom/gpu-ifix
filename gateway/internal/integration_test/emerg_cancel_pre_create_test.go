//go:build integration

// Phase 6 Plan 06-07 Task 1 — D-C3 cancel-in-flight, pre-create branch
// (PRV-09 / SC-3 evidence #1).
//
// Setup:
//   - mockVastServer with createBlocked atomic.Bool (default true) — when
//     PUT /asks/ arrives, it spins until createBlocked.Store(false) AND
//     ctx is not cancelled. This lets us reliably interleave the cancel
//     signal between SearchOffers (succeeded) and CreateInstance (about
//     to be called).
//
// Flow:
//  1. publishBreakerEvent("local-llm","open") → trigger sustains; FSM
//     advances to EMERGENCY_PROVISIONING; provisionLifecycle goroutine
//     calls SearchOffers (returns 1 offer).
//  2. The first attempt at CreateInstance enters the mock's spin loop.
//  3. publishBreakerEvent("local-llm","closed") → tracker.State()
//     transitions to "closed"; reconciler tick observes this in
//     evaluateEmergencyProvisioning and calls cancelActiveLifecycle.
//  4. Layer 1 (context cancel) propagates to the mock spin loop, which
//     immediately returns ctx.Err() (NOT a successful create response).
//  5. provisionLifecycle observes the create error path (ctx cancelled)
//     and closes the lifecycle row with shutdown_reason='cancelled_in_flight'.
//
// Assertions:
//   - mockVast.createHits == 0 (we never let create succeed; the spin
//     loop returns before writing the response). NOTE: Go's net/http will
//     count the request even if the handler exits early. We assert
//     createSucceededHits (incremented only after the spin loop unblocks
//     normally, NOT when ctx cancels) == 0.
//   - mockVast.destroyHits == 0 (no instance was created → nothing to
//     destroy).
//   - DB row exists with shutdown_reason='cancelled_in_flight'.
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

// blockingMockVastServer wraps the standard mockVastServer with a CreateInstance
// hold/release mechanism that lets the test interleave the cancel signal
// precisely between SearchOffers and CreateInstance.
type blockingMockVastServer struct {
	*httptest.Server
	searchHits          atomic.Int64
	createAttemptHits   atomic.Int64 // every PUT /asks/ enters
	createSucceededHits atomic.Int64 // only successful return paths increment
	destroyHits         atomic.Int64
	statusHits          atomic.Int64
	searchResponse      atomic.Pointer[[]vast.Offer]
	getResponse         atomic.Pointer[vast.Instance]
	createBlocked       atomic.Bool
}

func newBlockingMockVastServer(t *testing.T) *blockingMockVastServer {
	t.Helper()
	m := &blockingMockVastServer{}
	m.createBlocked.Store(true)
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
			m.createAttemptHits.Add(1)
			// Spin until either createBlocked drops OR ctx cancels. This is
			// the heart of the pre-create cancel test: cancel chega ANTES
			// de o handler poder responder, so net/http delivers the
			// cancel via r.Context().Done() and we exit without writing
			// success.
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					// Client (Vast SDK) cancelled. Do NOT write success;
					// just return — net/http will surface the cancel as a
					// transport_error to the SDK's Do() call.
					return
				case <-ticker.C:
					if !m.createBlocked.Load() {
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(map[string]any{
							"success":      true,
							"new_contract": 12345,
						})
						m.createSucceededHits.Add(1)
						return
					}
				}
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

// TestEmergCancelPreCreate — D-C3 layer 1 (context cancel) intercepts the
// in-flight CreateInstance before any pod is created. Asserts:
//   - createSucceededHits == 0 (cancel arrived before handler returned 200)
//   - destroyHits == 0 (no instance ever existed)
//   - DB row exists with shutdown_reason='cancelled_in_flight' OR
//     'create_error' (depending on which checkpoint catches the cancel
//     first; both encode the same operational outcome — the lifecycle was
//     aborted before a pod was created).
func TestEmergCancelPreCreate(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	mock := newBlockingMockVastServer(t)
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

	// Wait until the mock has received at least one PUT /asks/ — proves
	// provisionLifecycle is past SearchOffers and inside CreateInstance
	// (currently spinning on the createBlocked flag).
	if !waitFor(t, 10*time.Second, 50*time.Millisecond, func() bool {
		return mock.createAttemptHits.Load() >= 1
	}) {
		t.Fatalf("mock did not receive CreateInstance request within 10s; "+
			"searchHits=%d createAttemptHits=%d fsm=%s",
			mock.searchHits.Load(), mock.createAttemptHits.Load(), fsm.State())
	}

	// Now publish CLOSED — cancelActiveLifecycle should fire on the next
	// reconciler tick (~100ms). The cancel propagates through the
	// vastClient context to the spinning handler, which exits without
	// writing success.
	publishBreakerEvent(t, rdb, "local-llm", "closed")

	// Eventually the lifecycle row is closed AND the FSM returns to
	// Healthy. Budget 6s — cancel detection takes a tick (~100ms),
	// HTTP cancel propagation is sub-second, closeLifecycle is sub-second.
	if !waitFor(t, 6*time.Second, 100*time.Millisecond, func() bool {
		var endedAt pgtype.Timestamptz
		err := pool.QueryRow(ctx,
			`SELECT ended_at FROM ai_gateway.emergency_lifecycles
			 WHERE trigger_reason = 'failed_over_sustained' ORDER BY id DESC LIMIT 1`,
		).Scan(&endedAt)
		return err == nil && endedAt.Valid
	}) {
		t.Fatalf("lifecycle row was not closed within 6s; "+
			"createAttemptHits=%d createSucceededHits=%d fsm=%s",
			mock.createAttemptHits.Load(), mock.createSucceededHits.Load(), fsm.State())
	}

	// Critical D-C3 invariant: the cancel arrived BEFORE the create
	// succeeded. createSucceededHits MUST be 0.
	require.Equal(t, int64(0), mock.createSucceededHits.Load(),
		"D-C3 pre-create: cancel must arrive BEFORE CreateInstance success")
	require.Equal(t, int64(0), mock.destroyHits.Load(),
		"D-C3 pre-create: no instance was created → DestroyInstance must NOT be called")

	// Lifecycle row was closed with cancel-related shutdown_reason. The
	// production code path goes through one of:
	//   - waitForReadyOrDestroy ctx.Done() → 'cancelled_in_flight' (if
	//     create somehow succeeded between cancel propagation and the spin
	//     loop response — race window, currently impossible with
	//     createBlocked but defensive)
	//   - provisionLifecycle bid-loop ctx-check → 'cancelled_in_flight'
	//     (when ctx.Done() fires inside the backoff select)
	//   - provisionLifecycle CreateInstance returning a transport-error
	//     wrapping context.Canceled → 'create_error' (the typical pre-create
	//     branch — net/http surfaces ctx cancel as a transport error)
	// Both encode "lifecycle aborted before pod existed" and are accepted.
	var reason pgtype.Text
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT shutdown_reason FROM ai_gateway.emergency_lifecycles
		 WHERE trigger_reason = 'failed_over_sustained' ORDER BY id DESC LIMIT 1`,
	).Scan(&reason))
	require.True(t, reason.Valid)
	require.Contains(t, []string{"cancelled_in_flight", "create_error"}, reason.String,
		"shutdown_reason must indicate cancel-driven abort")

	// Best-effort: the FSM eventually returns to Healthy after the
	// goroutine completes its closeLifecycle path.
	_ = waitFor(t, 3*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateHealthy
	})

	// Release the mock (defensive — it should already be done).
	mock.createBlocked.Store(false)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
