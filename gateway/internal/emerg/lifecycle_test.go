// Package emerg (lifecycle_test.go): Plan 06-06 unit tests for the pure
// helpers in lifecycle.go.
//
// The full provisionLifecycle + waitForReadyOrDestroy + reconciler-state
// flow is exercised in
// gateway/internal/integration_test/emerg_provision_happy_test.go (build
// tag `integration`) — this file only covers the synchronous, side-effect-
// free helpers that don't need a Postgres + Redis harness.
package emerg

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// TestFilterBelowCap_Epsilon verifies Pitfall 5: epsilon comparison
// `cap + 0.0001`. Offers exactly at the cap pass; offers above the cap
// + epsilon are rejected.
func TestFilterBelowCap_Epsilon(t *testing.T) {
	cap := 0.40
	offers := []vast.Offer{
		{ID: 1, DphTotal: 0.45},   // above cap → rejected
		{ID: 2, DphTotal: 0.35},   // below cap → kept
		{ID: 3, DphTotal: 0.40},   // exactly at cap → kept (epsilon)
		{ID: 4, DphTotal: 0.4001}, // exactly cap+epsilon → kept (boundary)
		{ID: 5, DphTotal: 0.4002}, // just above cap+epsilon → rejected
	}
	got := filterBelowCap(offers, cap)
	require.Len(t, got, 3, "ids 2, 3, 4 must pass; ids 1 + 5 rejected")
	wantIDs := map[int64]bool{2: true, 3: true, 4: true}
	for _, o := range got {
		require.True(t, wantIDs[o.ID], "unexpected offer ID %d in filtered output", o.ID)
	}
}

// TestFilterBelowCap_EmptyInput — defensive: empty in → empty out, not nil panic.
func TestFilterBelowCap_EmptyInput(t *testing.T) {
	got := filterBelowCap(nil, 0.40)
	require.NotNil(t, got, "should return empty slice, not nil")
	require.Len(t, got, 0)
}

// TestExcludeHost — known primary host removed; unknown (hostID=0) keeps all.
func TestExcludeHost(t *testing.T) {
	offers := []vast.Offer{
		{ID: 1, HostID: 100},
		{ID: 2, HostID: 200},
		{ID: 3, HostID: 100},
		{ID: 4, HostID: 300},
	}
	got := excludeHost(offers, 100)
	require.Len(t, got, 2, "host 100 (ids 1, 3) must be removed")
	for _, o := range got {
		require.NotEqual(t, int64(100), o.HostID)
	}

	// hostID=0 is "unknown" — return input unchanged.
	got2 := excludeHost(offers, 0)
	require.Len(t, got2, 4)
}

// TestMustEventJSON — output must be a valid JSON array containing one
// row with the expected `type` + `payload` keys + a `ts` timestamp.
func TestMustEventJSON(t *testing.T) {
	out := mustEventJSON("offer_accepted", map[string]any{
		"offer_id": int64(123),
		"dph":      0.35,
	})
	var parsed []map[string]any
	require.NoError(t, json.Unmarshal(out, &parsed),
		"output must be a valid JSON array")
	require.Len(t, parsed, 1)
	row := parsed[0]
	require.Equal(t, "offer_accepted", row["type"])
	require.NotNil(t, row["ts"], "ts must be populated for audit timeline")
	payload, ok := row["payload"].(map[string]any)
	require.True(t, ok, "payload key must be an object")
	require.InDelta(t, 0.35, payload["dph"], 0.0001)
	require.EqualValues(t, 123, payload["offer_id"])
}

// TestPgInt8 — wrap returns Valid=true.
func TestPgInt8(t *testing.T) {
	v := pgInt8(12345)
	require.True(t, v.Valid)
	require.Equal(t, int64(12345), v.Int64)
}

// TestPgNumericFromFloat — round-trip via Float64Value.
func TestPgNumericFromFloat(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0.0, 0.0},
		{0.35, 0.35},
		{0.4001, 0.4001},
		{200.0, 200.0},
		{200.1234, 200.1234},
	}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			n := pgNumericFromFloat(c.in)
			require.True(t, n.Valid)
			fv, err := n.Float64Value()
			require.NoError(t, err)
			require.True(t, fv.Valid)
			require.InDelta(t, c.want, fv.Float64, 0.0001)
		})
	}
}

// TestPodHealthURL_RunningWithPort — happy path.
func TestPodHealthURL_RunningWithPort(t *testing.T) {
	r := &Reconciler{}
	inst := vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports: map[string][]vast.PortBinding{
			"8000/tcp": {{HostIP: "0.0.0.0", HostPort: "40713"}},
		},
	}
	require.Equal(t, "http://1.2.3.4:40713/v1/models", r.podHealthURL(inst))
}

// TestPodHealthURL_W6_Empty — Pitfall 6 fix: any of (no IP, no ports
// entry, empty bindings, empty HostPort) returns "".
func TestPodHealthURL_W6_Empty(t *testing.T) {
	r := &Reconciler{}

	// No IP.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		Ports: map[string][]vast.PortBinding{"8000/tcp": {{HostPort: "40713"}}},
	}))

	// No ports map at all.
	require.Equal(t, "", r.podHealthURL(vast.Instance{PublicIPAddr: "1.2.3.4"}))

	// 9100/tcp absent.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports:        map[string][]vast.PortBinding{"22/tcp": {{HostPort: "30000"}}},
	}))

	// Empty bindings list.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports:        map[string][]vast.PortBinding{"8000/tcp": {}},
	}))

	// Empty HostPort string.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports:        map[string][]vast.PortBinding{"8000/tcp": {{HostPort: ""}}},
	}))
}

// TestErrReason — sanity for the error-token mapping used in FSM transition reasons.
func TestErrReason(t *testing.T) {
	require.Equal(t, "offer_race_lost", errReason(ErrOfferRaceLost))
	require.Equal(t, "health_timeout", errReason(ErrHealthTimeout))
	require.Equal(t, "instance_terminal_state", errReason(ErrInstanceTerminal))
	require.Equal(t, "no_offers_below_cap", errReason(ErrNoOffersBelowCap))
	require.Equal(t, "other", errReason(errors.New("unrelated error")))
}
