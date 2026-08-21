// fsm_adapter_test.go — unit tests for PrimaryFSMAuditAdapter (dashboard
// ops audit 2026-08-21). Uses the same-package enqueueHook capture escape
// from middleware_test.go: no Run goroutine, no flusher.
package audit

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newCaptureWriter() (*Writer, *[]Event) {
	w := newTestWriter(nil, 10)
	var got []Event
	w.enqueueHook = func(e Event) { got = append(got, e) }
	return w, &got
}

func TestPrimaryFSMAuditAdapter_WellFormedPayload(t *testing.T) {
	w, got := newCaptureWriter()
	a := &PrimaryFSMAuditAdapter{W: w, Log: slog.Default()}

	at := time.Unix(5000, 0)
	err := a.WriteStateChange("fsm_transition", map[string]any{
		"from":   "asleep",
		"to":     "provisioning",
		"at":     at,
		"reason": "schedule_up",
	})
	if err != nil {
		t.Fatalf("WriteStateChange returned error: %v (must be best-effort nil)", err)
	}
	if len(*got) != 1 {
		t.Fatalf("want exactly 1 enqueued event, got %d", len(*got))
	}
	e := (*got)[0]
	if e.EventKind != "primary_state_change" {
		t.Errorf("EventKind = %q, want primary_state_change (kind remapped from fsm_transition)", e.EventKind)
	}
	if e.DataClass != "normal" {
		t.Errorf("DataClass = %q, want normal (NOT NULL enum — empty poisons the CopyFrom batch)", e.DataClass)
	}
	if e.Route != "primary_fsm_transition" {
		t.Errorf("Route = %q, want primary_fsm_transition", e.Route)
	}
	if e.Method != "asleep->provisioning" {
		t.Errorf("Method = %q, want asleep->provisioning", e.Method)
	}
	if !strings.Contains(e.Reason, "asleep→provisioning (") {
		t.Errorf("Reason = %q, want it to contain %q", e.Reason, "asleep→provisioning (")
	}
	if !e.TS.Equal(at) {
		t.Errorf("TS = %v, want %v (payload 'at' must ride through)", e.TS, at)
	}
	if e.RequestID.String() == "00000000-0000-0000-0000-000000000000" {
		t.Errorf("RequestID is the zero UUID — WriteStateChange must mint a fresh one")
	}
}

func TestPrimaryFSMAuditAdapter_MalformedPayloadDropped(t *testing.T) {
	w, got := newCaptureWriter()
	a := &PrimaryFSMAuditAdapter{W: w, Log: slog.Default()}

	// Non-map payload: WARN + drop, nil error, nothing enqueued.
	if err := a.WriteStateChange("fsm_transition", "not-a-map"); err != nil {
		t.Fatalf("malformed payload must return nil (best-effort), got %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("malformed payload must enqueue nothing, got %d events", len(*got))
	}

	// Nil writer: no-op, no panic.
	empty := &PrimaryFSMAuditAdapter{}
	if err := empty.WriteStateChange("fsm_transition", map[string]any{}); err != nil {
		t.Fatalf("nil-writer adapter must return nil, got %v", err)
	}
}
