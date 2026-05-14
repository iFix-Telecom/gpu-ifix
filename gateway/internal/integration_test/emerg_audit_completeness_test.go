//go:build integration

// Phase 6 Plan 06-09 — PRV-10 audit-log completeness integration test.
// Drives the full emergency lifecycle happy path (HEALTHY →
// EmergencyProvisioning → EmergencyActive → Recovering → Cooldown)
// and asserts every audit-log column on the closed lifecycle row is
// populated:
//
//   - id                     non-zero (BIGSERIAL)
//   - started_at             non-zero
//   - first_health_pass_at   set (mock /health returns true on first poll)
//   - ended_at               set (cutback_idle close path fired)
//   - trigger_reason         "failed_over_sustained" (Plan 06-05 trigger)
//   - vast_offer_id          set (CreateInstance succeeded)
//   - vast_instance_id       12345 (mock new_contract)
//   - accepted_dph           > 0 (offer 0.35)
//   - total_cost_brl         >= 0 (D-D4 calc; may be 0 if elapsed
//     < 1s but the column itself MUST be set)
//   - shutdown_reason        "cutback_idle"
//   - leader_replica         non-empty (os.Hostname() at boot)
//   - events JSONB           ≥3 entries (offer_accepted + healthy +
//     lifecycle_close at minimum)
package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// TestEmergAuditCompleteness — drive the FULL happy-path lifecycle and
// verify every audit column on the closed row is populated. Runtime
// budget ~10s wall time (1s healthy duration + 1s idle grace + tick
// latency).
func TestEmergAuditCompleteness(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

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

	// HEALTHY → EmergencyActive.
	publishBreakerEvent(t, rdb, "local-llm", "open")
	require.True(t, waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}), "FSM did not reach EmergencyActive within 15s; got %s", fsm.State())

	// Capture the lifecycle ID before close clears the active pointer.
	var liveID int64
	require.NoError(t, pool.QueryRow(rootCtx,
		`SELECT id FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL`,
	).Scan(&liveID))

	// Sleep so first_health_pass_at → ended_at delta is at least 1s
	// before driving cutback. This makes total_cost_brl strictly > 0
	// in the D-D4 calc (hours_active = 1s/3600 ~ 0.000277 → cost =
	// 0.35 * 0.000277 * 5.0 ~ R$0.00048; rounds to 0 at scale=4 in
	// the pgNumericFromFloat helper). To get a measurable cost we'd
	// need to wait longer — for completeness purposes we accept
	// total_cost_brl >= 0 (column populated, even if rounds to 0).
	time.Sleep(1 * time.Second)

	// Trigger cutback → idle-grace destroy → close.
	publishBreakerEvent(t, rdb, "local-llm", "closed")
	require.True(t, waitFor(t, 10*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateCooldown
	}), "FSM did not reach Cooldown within 10s; got %s", fsm.State())

	// Query ALL 11 audit columns from the closed row.
	var (
		id                int64
		startedAt         time.Time
		firstHealthPassAt pgtype.Timestamptz
		endedAt           pgtype.Timestamptz
		triggerReason     string
		vastOfferID       pgtype.Int8
		vastInstanceID    pgtype.Int8
		acceptedDPH       pgtype.Numeric
		totalCostBRL      pgtype.Numeric
		shutdownReason    pgtype.Text
		leaderReplica     pgtype.Text
		eventsJSON        []byte
	)
	err := pool.QueryRow(rootCtx, `
		SELECT id, started_at, first_health_pass_at, ended_at,
		       trigger_reason, vast_offer_id, vast_instance_id,
		       accepted_dph, total_cost_brl, shutdown_reason,
		       leader_replica, events
		FROM ai_gateway.emergency_lifecycles
		WHERE id = $1
	`, liveID).Scan(
		&id, &startedAt, &firstHealthPassAt, &endedAt,
		&triggerReason, &vastOfferID, &vastInstanceID,
		&acceptedDPH, &totalCostBRL, &shutdownReason,
		&leaderReplica, &eventsJSON,
	)
	require.NoError(t, err, "query closed lifecycle row")

	// Column-by-column completeness assertions.
	require.NotZero(t, id, "id (BIGSERIAL) must be non-zero")
	require.False(t, startedAt.IsZero(), "started_at must be NOT NULL")
	require.True(t, firstHealthPassAt.Valid,
		"first_health_pass_at must be set after /health pass (D-D4)")
	require.True(t, endedAt.Valid, "ended_at must be set after close")
	require.Equal(t, "failed_over_sustained", triggerReason,
		"trigger_reason must reflect Plan 06-05 sustained-OPEN trigger")
	require.True(t, vastOfferID.Valid, "vast_offer_id must be set after CreateInstance")
	require.Equal(t, int64(9001), vastOfferID.Int64,
		"vast_offer_id must match the mock offer ID")
	require.True(t, vastInstanceID.Valid,
		"vast_instance_id must be set after CreateInstance returns new_contract")
	require.Equal(t, int64(12345), vastInstanceID.Int64,
		"vast_instance_id must match the mock new_contract")
	require.True(t, acceptedDPH.Valid, "accepted_dph must be set after CreateInstance")
	dphFloat, err := acceptedDPH.Float64Value()
	require.NoError(t, err)
	require.True(t, dphFloat.Valid)
	require.InDelta(t, 0.35, dphFloat.Float64, 0.0001,
		"accepted_dph must equal the mock offer 0.35 dph")
	require.True(t, totalCostBRL.Valid,
		"total_cost_brl must be set on close (D-D4; may be ~0 if hours_active < 1s but the column MUST be populated)")
	costFloat, err := totalCostBRL.Float64Value()
	require.NoError(t, err)
	require.True(t, costFloat.Valid)
	require.GreaterOrEqual(t, costFloat.Float64, 0.0,
		"total_cost_brl must be >= 0 (D-D4 calc)")
	require.True(t, shutdownReason.Valid, "shutdown_reason must be set on close")
	require.Equal(t, "cutback_idle", shutdownReason.String,
		"shutdown_reason must reflect D-D1 idle-grace destroy path")
	require.True(t, leaderReplica.Valid, "leader_replica must be set at INSERT time")
	require.NotEmpty(t, leaderReplica.String,
		"leader_replica must be non-empty (os.Hostname() at boot)")

	// events JSONB completeness — ≥3 entries (offer_accepted + healthy +
	// lifecycle_close).
	require.NotEmpty(t, eventsJSON, "events JSONB must not be empty")
	var events []map[string]any
	require.NoError(t, json.Unmarshal(eventsJSON, &events),
		"events JSONB must parse as a JSON array")
	require.GreaterOrEqual(t, len(events), 3,
		"events JSONB must contain ≥3 entries (offer_accepted + healthy + lifecycle_close); got %d", len(events))

	// Verify the events contain at minimum the close event.
	hasClose := false
	for _, ev := range events {
		if ev["type"] == "lifecycle_close" {
			hasClose = true
			break
		}
	}
	require.True(t, hasClose, "events JSONB must include a lifecycle_close entry")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
