package alert

// severity_test.go — Task 1 (07-05) unit tests for the pure
// event → severity tier → channel matrix mapping. No I/O: every case
// feeds a raw Pub/Sub channel name + JSON payload to severityFor and
// asserts the tier, the channel fan-out, and fingerprint stability.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// mustJSON marshals v or fails the test — keeps the table cases terse.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestSeverityFor_Classification covers behaviors 1+2: each of the three
// Pub/Sub channels classified into the right tier.
func TestSeverityFor_Classification(t *testing.T) {
	cases := []struct {
		name     string
		channel  string
		payload  []byte
		wantTier Severity
	}{
		{
			name:    "breaker local-llm open → critical",
			channel: redisx.BreakerEventsChannel(),
			payload: mustJSON(t, redisx.BreakerEvent{
				Upstream: "local-llm", State: "open", SinceUnix: 1000,
			}),
			wantTier: SeverityCritical,
		},
		{
			name:    "breaker openrouter open → warning (non-primary)",
			channel: redisx.BreakerEventsChannel(),
			payload: mustJSON(t, redisx.BreakerEvent{
				Upstream: "openrouter", State: "open", SinceUnix: 1000,
			}),
			wantTier: SeverityWarning,
		},
		{
			name:    "breaker local-llm closed → info (recovery)",
			channel: redisx.BreakerEventsChannel(),
			payload: mustJSON(t, redisx.BreakerEvent{
				Upstream: "local-llm", State: "closed", SinceUnix: 1000,
			}),
			wantTier: SeverityInfo,
		},
		{
			name:    "shed sustained saturation (on) → warning",
			channel: redisx.ShedEventsChannel,
			payload: mustJSON(t, redisx.ShedEvent{
				Upstream: "local-llm", State: "on", SinceUnix: 1000,
			}),
			wantTier: SeverityWarning,
		},
		{
			name:    "shed armed → info (not yet sustained)",
			channel: redisx.ShedEventsChannel,
			payload: mustJSON(t, redisx.ShedEvent{
				Upstream: "local-llm", State: "armed", SinceUnix: 1000,
			}),
			wantTier: SeverityInfo,
		},
		{
			name:    "shed off → info (benign)",
			channel: redisx.ShedEventsChannel,
			payload: mustJSON(t, redisx.ShedEvent{
				Upstream: "local-llm", State: "off", SinceUnix: 1000,
			}),
			wantTier: SeverityInfo,
		},
		{
			name:    "emerg transition → emergency_active → critical",
			channel: redisx.EmergEventsChannel,
			payload: mustJSON(t, redisx.EmergEvent{
				Type: "transition", State: "emergency_active", SinceUnix: 1000,
			}),
			wantTier: SeverityCritical,
		},
		{
			name:    "emerg transition → emergency_provisioning → critical",
			channel: redisx.EmergEventsChannel,
			payload: mustJSON(t, redisx.EmergEvent{
				Type: "transition", State: "emergency_provisioning", SinceUnix: 1000,
			}),
			wantTier: SeverityCritical,
		},
		{
			name:    "emerg transition → failed_over → warning",
			channel: redisx.EmergEventsChannel,
			payload: mustJSON(t, redisx.EmergEvent{
				Type: "transition", State: "failed_over", SinceUnix: 1000,
			}),
			wantTier: SeverityWarning,
		},
		{
			name:    "emerg transition → healthy → info (benign)",
			channel: redisx.EmergEventsChannel,
			payload: mustJSON(t, redisx.EmergEvent{
				Type: "transition", State: "healthy", SinceUnix: 1000,
			}),
			wantTier: SeverityInfo,
		},
		{
			name:    "emerg transition → cooldown → info (benign)",
			channel: redisx.EmergEventsChannel,
			payload: mustJSON(t, redisx.EmergEvent{
				Type: "transition", State: "cooldown", SinceUnix: 1000,
			}),
			wantTier: SeverityInfo,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sev, msg, err := severityFor(tc.channel, tc.payload)
			if err != nil {
				t.Fatalf("severityFor: unexpected error: %v", err)
			}
			if sev != tc.wantTier {
				t.Errorf("tier = %q, want %q", sev, tc.wantTier)
			}
			if msg.Severity != tc.wantTier {
				t.Errorf("msg.Severity = %q, want %q", msg.Severity, tc.wantTier)
			}
			if msg.Fingerprint == "" {
				t.Error("msg.Fingerprint is empty — every classified event must have a stable fingerprint")
			}
			if msg.Title == "" {
				t.Error("msg.Title is empty — every classified event must have a headline")
			}
		})
	}
}

// TestSeverityFor_MalformedJSON covers the tampering threat T-07-17: an
// unparseable payload must return an error, never panic.
func TestSeverityFor_MalformedJSON(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		payload []byte
	}{
		{"breaker garbage", redisx.BreakerEventsChannel(), []byte(`{not json`)},
		{"shed garbage", redisx.ShedEventsChannel, []byte(`}}}`)},
		{"emerg garbage", redisx.EmergEventsChannel, []byte(`<xml/>`)},
		{"unknown channel", "gw:bogus:events", []byte(`{}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := severityFor(tc.channel, tc.payload)
			if err == nil {
				t.Errorf("severityFor(%q, %q): want error, got nil", tc.channel, tc.payload)
			}
		})
	}
}

// TestChannelsFor covers behavior 3: the severity → channel matrix.
func TestChannelsFor(t *testing.T) {
	crit := channelsFor(SeverityCritical)
	if len(crit) != 3 {
		t.Fatalf("channelsFor(critical) = %v, want exactly 3 channels", crit)
	}
	wantCrit := map[string]bool{"chatwoot": true, "clickup": true, "brevo": true}
	for _, c := range crit {
		if !wantCrit[c] {
			t.Errorf("channelsFor(critical) contains unexpected channel %q", c)
		}
	}

	warn := channelsFor(SeverityWarning)
	if len(warn) != 2 {
		t.Fatalf("channelsFor(warning) = %v, want exactly 2 channels", warn)
	}
	wantWarn := map[string]bool{"clickup": true, "brevo": true}
	for _, c := range warn {
		if !wantWarn[c] {
			t.Errorf("channelsFor(warning) contains unexpected channel %q", c)
		}
	}

	info := channelsFor(SeverityInfo)
	if len(info) != 0 {
		t.Errorf("channelsFor(info) = %v, want empty set", info)
	}

	// Unknown severity is conservatively empty — never page on a value
	// the alerter does not recognise.
	if got := channelsFor(Severity("bogus")); len(got) != 0 {
		t.Errorf("channelsFor(bogus) = %v, want empty set", got)
	}
}

// TestSeverityFor_FingerprintStable covers behavior 4: the same event
// yields the same fingerprint on repeated calls (the dedup invariant).
func TestSeverityFor_FingerprintStable(t *testing.T) {
	payload := mustJSON(t, redisx.BreakerEvent{
		Upstream: "local-llm", State: "open", SinceUnix: 1000,
	})
	_, m1, err := severityFor(redisx.BreakerEventsChannel(), payload)
	if err != nil {
		t.Fatalf("severityFor #1: %v", err)
	}
	_, m2, err := severityFor(redisx.BreakerEventsChannel(), payload)
	if err != nil {
		t.Fatalf("severityFor #2: %v", err)
	}
	if m1.Fingerprint != m2.Fingerprint {
		t.Errorf("fingerprint not stable: %q != %q", m1.Fingerprint, m2.Fingerprint)
	}

	// A SinceUnix change (same logical incident, re-published) MUST NOT
	// change the fingerprint — otherwise the dedup gate never collapses
	// a flapping incident.
	payload2 := mustJSON(t, redisx.BreakerEvent{
		Upstream: "local-llm", State: "open", SinceUnix: 9999,
	})
	_, m3, err := severityFor(redisx.BreakerEventsChannel(), payload2)
	if err != nil {
		t.Fatalf("severityFor #3: %v", err)
	}
	if m1.Fingerprint != m3.Fingerprint {
		t.Errorf("fingerprint changed on re-publish of same incident: %q != %q", m1.Fingerprint, m3.Fingerprint)
	}

	// A different upstream MUST get a different fingerprint.
	payload3 := mustJSON(t, redisx.BreakerEvent{
		Upstream: "openrouter", State: "open", SinceUnix: 1000,
	})
	_, m4, err := severityFor(redisx.BreakerEventsChannel(), payload3)
	if err != nil {
		t.Fatalf("severityFor #4: %v", err)
	}
	if m1.Fingerprint == m4.Fingerprint {
		t.Errorf("distinct incidents share a fingerprint: %q", m1.Fingerprint)
	}
}

// ===========================================================================
// Phase 12 Plan 02 Task 3 — severityForPrimary (D-03, FINDING 1)
//
// The Alerter consumes PrimaryEventsChannel and maps primary_death_confirmed
// events to SeverityCritical with a DISTINCT billing-stop vs host-death title.
// ===========================================================================

func TestSeverityForPrimary_BillingStopCritical(t *testing.T) {
	payload := mustJSON(t, redisx.PrimaryEvent{
		Type: "primary_death_confirmed", State: "draining",
		LifecycleID: 42, Reason: "billing_stopped",
	})
	sev, msg, err := severityForPrimary(payload)
	if err != nil {
		t.Fatalf("severityForPrimary: %v", err)
	}
	if sev != SeverityCritical {
		t.Errorf("billing-stop death severity = %q, want critical", sev)
	}
	if !strings.Contains(msg.Title, "Vast account sem crédito") {
		t.Errorf("billing-stop title = %q, want it to contain %q", msg.Title, "Vast account sem crédito")
	}
}

func TestSeverityForPrimary_HostDeathCritical(t *testing.T) {
	payload := mustJSON(t, redisx.PrimaryEvent{
		Type: "primary_death_confirmed", State: "draining",
		LifecycleID: 7, Reason: "host_death",
	})
	sev, msg, err := severityForPrimary(payload)
	if err != nil {
		t.Fatalf("severityForPrimary: %v", err)
	}
	if sev != SeverityCritical {
		t.Errorf("host-death severity = %q, want critical", sev)
	}
	if strings.Contains(msg.Title, "Vast account sem crédito") {
		t.Errorf("host-death title must NOT be the billing-stop title: %q", msg.Title)
	}
	if msg.Title == "" {
		t.Errorf("host-death title must be non-empty")
	}
}

func TestSeverityForPrimary_Malformed(t *testing.T) {
	_, _, err := severityForPrimary([]byte("{not json"))
	if err == nil {
		t.Errorf("malformed primary payload must return an error, not panic")
	}
}

func TestSeverityFor_RoutesPrimaryChannel(t *testing.T) {
	payload := mustJSON(t, redisx.PrimaryEvent{
		Type: "primary_death_confirmed", State: "draining", Reason: "billing_stopped",
	})
	sev, _, err := severityFor(redisx.PrimaryEventsChannel, payload)
	if err != nil {
		t.Fatalf("severityFor(PrimaryEventsChannel): %v", err)
	}
	if sev != SeverityCritical {
		t.Errorf("severityFor must route PrimaryEventsChannel to severityForPrimary (critical); got %q", sev)
	}
}
