//go:build integration

// Phase 6 Plan 06-07 Task 2 — D-D5 leader recovery, active+healthy
// cenário (d). BLOCKER 4 fix (revisão 2026-05-13): this branch is now
// FUNCIONAL — JSONB events replay reconstructs FSM, re-attaches cancel
// context, restarts healthcheck goroutine.
//
// Two test scenarios:
//
// 1. TestEmergLeaderRecoveryActiveResume (happy path)
//   - Pre-seed lifecycle row with vast_instance_id=99 + events JSONB
//     containing offer_accepted + health_pass.
//   - mockVast GetInstance(99) returns running + populated ports.
//   - mockPodHealthServer returns 200 healthy.
//   - Assert FSM == EmergencyActive (recovered from events.health_pass)
//     AND activeLifecycle != nil AND activePodURL != nil.
//
// 2. TestEmergLeaderRecoveryActiveResume_HealthFailureCancels (sad path)
//   - Same setup but mockPodHealthServer returns 500.
//   - Assert: after 3 consecutive failures (~15s), the resumed lifecycle
//     is cancelled — FSM returns to Healthy AND activeLifecycle == nil
//     AND lifecycle row is closed with cancel-related shutdown_reason.
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

// resumeMockVastServer returns a configurable instance for GetInstance
// (so we can simulate "alive + healthy" — actual_status=running with
// populated ports), and tracks destroy/get hits.
type resumeMockVastServer struct {
	*httptest.Server
	getHits     atomic.Int64
	destroyHits atomic.Int64
	getResponse atomic.Pointer[vast.Instance]
}

func newResumeMockVastServer(t *testing.T) *resumeMockVastServer {
	t.Helper()
	m := &resumeMockVastServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == http.MethodDelete:
			m.destroyHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})

		case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == http.MethodGet:
			m.getHits.Add(1)
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

// mockPodHealthServer mocks the resumed pod's /health endpoint.
type mockPodHealthServer struct {
	*httptest.Server
	healthHits atomic.Int64
	healthy    atomic.Bool // true → 200 healthy; false → 500
}

func newMockPodHealthServer(t *testing.T, healthy bool) *mockPodHealthServer {
	t.Helper()
	m := &mockPodHealthServer{}
	m.healthy.Store(healthy)
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.healthHits.Add(1)
		if !m.healthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":"unhealthy"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Mirror llama-server /v1/models (OpenAI-compatible).
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen","object":"model"}]}`))
	}))
	t.Cleanup(m.Server.Close)
	return m
}

// instanceFromHealthURL builds a vast.Instance whose podHealthURL(inst)
// computes to the given healthURL string (must be of form
// http://<ip>:<port>/health). The PublicIPAddr + Ports map are
// extracted from the URL so the production podHealthURL helper produces
// the same string.
func instanceFromHealthURL(t *testing.T, instanceID int64, healthURL string) vast.Instance {
	t.Helper()
	// Parse "http://<ip>:<port>/v1/models"
	require.True(t, strings.HasPrefix(healthURL, "http://"))
	rest := strings.TrimPrefix(healthURL, "http://")
	rest = strings.TrimSuffix(rest, "/v1/models")
	parts := strings.Split(rest, ":")
	require.Len(t, parts, 2)
	ip, port := parts[0], parts[1]
	return vast.Instance{
		ID:           instanceID,
		ActualStatus: "running",
		PublicIPAddr: ip,
		Ports: map[string][]vast.PortBinding{
			"8000/tcp": {{HostIP: "0.0.0.0", HostPort: port}},
		},
	}
}

// TestEmergLeaderRecoveryActiveResume — D-D5 cenário (d) happy path.
// BLOCKER 4 fix: events replay reconstructs FSM to EMERGENCY_ACTIVE,
// activeLifecycle is re-attached, podURL is stored.
func TestEmergLeaderRecoveryActiveResume(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	mockHealth := newMockPodHealthServer(t, true)

	// Pre-seed lifecycle row with events JSONB showing the
	// offer_accepted → health_pass progression.
	events := `[
	  {"ts":"2026-05-13T00:00:00Z","type":"offer_accepted","payload":{"offer_id":7777,"instance_id":99,"dph":0.35}},
	  {"ts":"2026-05-13T00:01:00Z","type":"health_pass","payload":{"lifecycle_id":1,"health_url":"` + mockHealth.URL + `/v1/models","dph":0.35}}
	]`
	var orphanID int64
	require.NoError(t, pool.QueryRow(rootCtx,
		`INSERT INTO ai_gateway.emergency_lifecycles
		 (trigger_reason, vast_offer_id, vast_instance_id, accepted_dph, first_health_pass_at, events)
		 VALUES ('failed_over_sustained', 7777, 99, 0.35, NOW(), $1::jsonb)
		 RETURNING id`, events).Scan(&orphanID))

	mockVast := newResumeMockVastServer(t)
	// Build instance whose podHealthURL produces mockHealth.URL/health.
	resumedInst := instanceFromHealthURL(t, 99, mockHealth.URL+"/v1/models")
	mockVast.getResponse.Store(&resumedInst)

	vastClient := vast.NewClientWithBaseURL("test-key", mockVast.Server.URL)

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

	// Wait for leadership (triggers recovery).
	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	// Recovery should observe the alive instance + replay events to
	// reconstruct EMERGENCY_ACTIVE state. Budget 5s.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}) {
		t.Fatalf("FSM did not resume to EmergencyActive within 5s; got %s; "+
			"getHits=%d destroyHits=%d",
			fsm.State(), mockVast.getHits.Load(), mockVast.destroyHits.Load())
	}

	// activeLifecycle is re-attached.
	url, ok := r.ActivePodURL()
	require.True(t, ok, "activePodURL must be set after resume")
	require.Equal(t, mockHealth.URL+"/v1/models", url,
		"podURL must match the value derived from the resumed instance")

	// IsActive() — the dispatcher contract Plan 08 reads.
	require.True(t, r.IsActive(),
		"reconciler.IsActive() must be true after resume")

	// DB row must NOT have been closed (the lifecycle is still live).
	var endedAt pgtype.Timestamptz
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT ended_at FROM ai_gateway.emergency_lifecycles WHERE id = $1`,
		orphanID).Scan(&endedAt))
	require.False(t, endedAt.Valid,
		"resumed lifecycle row must NOT be closed (ended_at IS NULL)")

	// Healthcheck goroutine should be polling mockHealth — wait for >=1
	// hit to confirm runHealthcheckResumeLoop is alive. Budget 8s
	// (interval is 5s, so the FIRST tick lands 5s after resume).
	if !waitFor(t, 8*time.Second, 200*time.Millisecond, func() bool {
		return mockHealth.healthHits.Load() >= 1
	}) {
		t.Fatalf("runHealthcheckResumeLoop did not poll /health within 8s; "+
			"healthHits=%d", mockHealth.healthHits.Load())
	}

	require.Equal(t, int64(0), mockVast.destroyHits.Load(),
		"healthy resume must NOT destroy the instance")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestEmergLeaderRecoveryActiveResume_HealthFailureCancels — D-D5 cenário
// (d) sad path. Pod /health fails after resume; the runHealthcheckResumeLoop
// detects 3 consecutive failures and calls cancelActiveLifecycle. The
// lifecycle is then closed with shutdown_reason='cancelled_in_flight'.
func TestEmergLeaderRecoveryActiveResume_HealthFailureCancels(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	// Mock pod health server returning 500 (unhealthy).
	mockHealth := newMockPodHealthServer(t, false)

	events := `[
	  {"ts":"2026-05-13T00:00:00Z","type":"offer_accepted","payload":{"offer_id":7777,"instance_id":100,"dph":0.35}},
	  {"ts":"2026-05-13T00:01:00Z","type":"health_pass","payload":{"lifecycle_id":1,"health_url":"` + mockHealth.URL + `/v1/models","dph":0.35}}
	]`
	var orphanID int64
	require.NoError(t, pool.QueryRow(rootCtx,
		`INSERT INTO ai_gateway.emergency_lifecycles
		 (trigger_reason, vast_offer_id, vast_instance_id, accepted_dph, first_health_pass_at, events)
		 VALUES ('failed_over_sustained', 7777, 100, 0.35, NOW(), $1::jsonb)
		 RETURNING id`, events).Scan(&orphanID))

	mockVast := newResumeMockVastServer(t)
	resumedInst := instanceFromHealthURL(t, 100, mockHealth.URL+"/v1/models")
	mockVast.getResponse.Store(&resumedInst)

	vastClient := vast.NewClientWithBaseURL("test-key", mockVast.Server.URL)

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

	// Resume to EmergencyActive (same as happy path).
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}) {
		t.Fatalf("FSM did not resume to EmergencyActive within 5s; got %s",
			fsm.State())
	}

	// runHealthcheckResumeLoop polls every 5s; 3 failures = 15s wall.
	// Wait up to 25s for the cancel to fire (5+5+5 = 15s + propagation).
	if !waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		var endedAt pgtype.Timestamptz
		err := pool.QueryRow(ctx,
			`SELECT ended_at FROM ai_gateway.emergency_lifecycles WHERE id = $1`,
			orphanID).Scan(&endedAt)
		return err == nil && endedAt.Valid
	}) {
		t.Fatalf("lifecycle was not closed after sustained /health failures within 30s; "+
			"healthHits=%d", mockHealth.healthHits.Load())
	}

	require.GreaterOrEqual(t, mockHealth.healthHits.Load(), int64(3),
		"runHealthcheckResumeLoop must poll /health at least 3 times before cancelling")

	// runHealthcheckResumeLoop closes the row with shutdown_reason='resume_health_failed'
	// and destroys the underlying instance — see recovery.go for the full path.
	var reason pgtype.Text
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT shutdown_reason FROM ai_gateway.emergency_lifecycles WHERE id = $1`,
		orphanID).Scan(&reason))
	require.True(t, reason.Valid,
		"shutdown_reason must be set after cancel-driven close")
	require.Equal(t, "resume_health_failed", reason.String,
		"shutdown_reason must be resume_health_failed (recovery sad path)")
	// The unhealthy instance must be destroyed best-effort.
	require.GreaterOrEqual(t, mockVast.destroyHits.Load(), int64(1),
		"resume_health_failed path must destroy the unhealthy instance")

	// FSM eventually returns to Healthy after the goroutine exits.
	_ = waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateHealthy
	})

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
