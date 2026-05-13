// Package emerg (recovery_test.go): Plan 06-07 unit tests for the pure
// helpers in recovery.go.
//
// The full recoverOrphanLifecycles + resumeFSMFromEvents flow (with DB +
// Vast.GetInstance interactions) is exercised in
// gateway/internal/integration_test/emerg_leader_recovery_*_test.go
// (build tag `integration`). This file only covers the synchronous,
// side-effect-free helpers that don't need a Postgres + Redis harness:
//
//   - TestInferStateFromEvents — pure function over a parsed JSONB array
//     determines which state the lifecycle was last in based on event
//     types observed (offer_accepted vs health_pass).
//   - TestRecoveryConstants — defensive: the recovery healthcheck cadence
//     and failure threshold are 5s × 3 = 15s degradation budget.
package emerg

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInferStateFromEvents — exhaustive coverage over the event-type
// inference table. Documents the mapping from Plan 06's mustEventJSON
// emit sites (offer_accepted at CreateInstance success, health_pass at
// markHealthy) to the FSM state the recovery code reconstructs.
func TestInferStateFromEvents(t *testing.T) {
	cases := []struct {
		name     string
		events   []map[string]any
		want     State
		wantOK   bool
	}{
		{
			name:   "empty events array → no inference",
			events: nil,
			want:   StateHealthy,
			wantOK: false,
		},
		{
			name: "only offer_accepted → EMERGENCY_PROVISIONING",
			events: []map[string]any{
				{"type": "offer_accepted", "ts": "2026-05-13T00:00:00Z"},
			},
			want:   StateEmergencyProvisioning,
			wantOK: true,
		},
		{
			name: "offer_accepted then health_pass → EMERGENCY_ACTIVE",
			events: []map[string]any{
				{"type": "offer_accepted", "ts": "2026-05-13T00:00:00Z"},
				{"type": "health_pass", "ts": "2026-05-13T00:01:00Z"},
			},
			want:   StateEmergencyActive,
			wantOK: true,
		},
		{
			name: "only health_pass (defensive — should not happen in practice)",
			events: []map[string]any{
				{"type": "health_pass", "ts": "2026-05-13T00:01:00Z"},
			},
			want:   StateEmergencyActive,
			wantOK: true,
		},
		{
			name: "unrecognized types → no inference",
			events: []map[string]any{
				{"type": "lifecycle_close", "ts": "2026-05-13T00:00:00Z"},
				{"type": "offer_race_attempt", "ts": "2026-05-13T00:00:01Z"},
			},
			want:   StateHealthy,
			wantOK: false,
		},
		{
			name: "explicit to_state on event → preferred over inference",
			events: []map[string]any{
				{"type": "offer_accepted", "ts": "2026-05-13T00:00:00Z"},
				{"type": "transition", "to_state": "recovering", "ts": "2026-05-13T00:01:00Z"},
			},
			want:   StateRecovering,
			wantOK: true,
		},
		{
			name: "invalid to_state value → falls back to inference",
			events: []map[string]any{
				{"type": "offer_accepted", "ts": "2026-05-13T00:00:00Z"},
				{"type": "transition", "to_state": "invalid_state", "ts": "2026-05-13T00:01:00Z"},
			},
			want:   StateEmergencyProvisioning,
			wantOK: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := inferStateFromEvents(c.events)
			require.Equal(t, c.wantOK, ok, "ok mismatch")
			require.Equal(t, c.want, got, "state mismatch")
		})
	}
}

// TestRecoveryConstants documents the recovery /health polling
// configuration: 5s ticker × 3 consecutive failures = 15s degradation
// budget. Matches the cold-start grace philosophy used during fresh
// provisioning. Brittle on purpose — changing these requires re-validating
// the test budgets in the integration layer.
func TestRecoveryConstants(t *testing.T) {
	require.Equal(t, 5*time.Second, recoveryHealthcheckInterval,
		"recoveryHealthcheckInterval must be 5s (matches Plan 06 instancePollInterval)")
	require.Equal(t, 3, recoveryHealthcheckMaxFailures,
		"recoveryHealthcheckMaxFailures must be 3 (15s degradation budget)")
}
