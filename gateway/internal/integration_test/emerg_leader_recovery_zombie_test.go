//go:build integration

// Phase 6 Plan 06-07 Task 2 — D-D5 leader recovery, zombie cenário (c).
//
// Scenario:
//   - Pre-seed an emergency_lifecycles row with vast_instance_id=88888,
//     ended_at IS NULL (simulates leader crash mid-lifecycle while a Vast
//     pod was alive).
//   - mockVast.GetInstance(88888) returns actual_status="exited" — i.e.,
//     the Vast pod has crashed/timed-out and is now zombie.
//   - Start a fresh reconciler — on leader acquisition, recoverOrphanLifecycles
//     scans the live row, calls GetInstance, observes inst.IsActive()==false,
//     calls DestroyInstance + closeLifecycle('leader_recovery_zombie').
//
// Assertions:
//   - mockVast.destroyHits == 1 (zombie was destroyed on cleanup)
//   - DB row ended_at NOT NULL (closed)
//   - DB row shutdown_reason == 'leader_recovery_zombie'
//   - FSM state remains Healthy throughout (zombie cleanup does NOT
//     advance the FSM — it only closes the audit row)
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

// recoveryMockVastServer responds to GetInstance with a configurable
// instance + actual_status, and tracks destroyHits.
type recoveryMockVastServer struct {
	*httptest.Server
	getHits     atomic.Int64
	destroyHits atomic.Int64
	getResponse atomic.Pointer[vast.Instance]
}

func newRecoveryMockVastServer(t *testing.T) *recoveryMockVastServer {
	t.Helper()
	m := &recoveryMockVastServer{}
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

// TestEmergLeaderRecoveryZombie — pre-seed lifecycle with vast_instance_id;
// mock GetInstance returns exited; leader recovery destroys + closes.
func TestEmergLeaderRecoveryZombie(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	// Pre-seed: insert orphan lifecycle row with vast_instance_id=88888,
	// ended_at IS NULL, accepted_dph populated (so closeLifecycle's
	// calculateCostBRL has the data it would normally have).
	var orphanID int64
	require.NoError(t, pool.QueryRow(rootCtx,
		`INSERT INTO ai_gateway.emergency_lifecycles
		 (trigger_reason, vast_offer_id, vast_instance_id, accepted_dph)
		 VALUES ('failed_over_sustained', 7777, 88888, 0.35)
		 RETURNING id`).Scan(&orphanID))

	mock := newRecoveryMockVastServer(t)
	zombieInst := vast.Instance{
		ID:           88888,
		ActualStatus: "exited", // terminal — IsActive()==false, IsTerminal()==true
		PublicIPAddr: "1.2.3.4",
	}
	mock.getResponse.Store(&zombieInst)

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

	// Wait for leader acquisition (which triggers recovery).
	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	// Recovery should destroy the zombie + close the row within a few ticks.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return mock.destroyHits.Load() >= 1
	}) {
		t.Fatalf("zombie destroy did not fire within 5s; "+
			"getHits=%d destroyHits=%d", mock.getHits.Load(), mock.destroyHits.Load())
	}
	require.Equal(t, int64(1), mock.destroyHits.Load(),
		"D-D5 cenário (c): zombie instance MUST be destroyed exactly once")

	// Wait for the row to be closed.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		var endedAt pgtype.Timestamptz
		err := pool.QueryRow(ctx,
			`SELECT ended_at FROM ai_gateway.emergency_lifecycles WHERE id = $1`,
			orphanID).Scan(&endedAt)
		return err == nil && endedAt.Valid
	}) {
		t.Fatalf("zombie lifecycle row was not closed within 5s")
	}

	var reason pgtype.Text
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT shutdown_reason FROM ai_gateway.emergency_lifecycles WHERE id = $1`,
		orphanID).Scan(&reason))
	require.True(t, reason.Valid)
	require.Equal(t, "leader_recovery_zombie", reason.String,
		"D-D5 cenário (c): shutdown_reason MUST be leader_recovery_zombie")

	// FSM should NOT have advanced — recovery is row-level cleanup, not
	// FSM mutation (the recovered lifecycle was zombie, so no resume).
	require.Equal(t, emerg.StateHealthy, fsm.State(),
		"FSM must stay Healthy after zombie recovery")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
