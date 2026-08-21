// fsm_adapter.go — bridges the primary-pod FSM's untyped audit hook into
// the shared async audit.Writer (dashboard ops audit 2026-08-21).
//
// internal/primary deliberately does NOT import internal/audit: its
// stateChangeWriter hook is `WriteStateChange(kind string, ev any) error`
// with a map[string]any payload, so the dependency direction stays
// primary ← main ← audit. This adapter lives in package audit and is
// wired in cmd/gateway/main.go as the FSM's writer, closing the gap where
// primary.NewFSM(nil, ...) left the hook dead and /incidents permanently
// empty of pod-lifecycle rows.
package audit

import (
	"fmt"
	"log/slog"
	"time"
)

// PrimaryFSMAuditAdapter satisfies primary's stateChangeWriter interface
// and forwards every primary FSM transition to the shared audit.Writer as
// an append-only audit_log state-change row.
//
// Payload contract (primary/fsm.go commitTransitionSideEffects): the FSM
// emits kind "fsm_transition" with a map[string]any carrying
// "from"/"to"/"reason" (string) and "at" (time.Time). The kind parameter
// is REMAPPED here to "primary_state_change" so the /incidents feed can
// distinguish primary-pod lifecycle rows from the emergency FSM's
// "emerg_state_change" rows.
//
// Best-effort by design: WriteStateChange never returns a non-nil error —
// a malformed payload logs a WARN and is dropped; the underlying
// Writer.WriteStateChange rides the non-blocking async Enqueue. The FSM
// transition path is never stalled or failed by auditing.
type PrimaryFSMAuditAdapter struct {
	W   *Writer
	Log *slog.Logger
}

// WriteStateChange converts the FSM's map payload into an audit Event and
// enqueues it (async, non-blocking). DataClass "normal" is MANDATORY:
// audit_log.data_class is a NOT NULL ai_gateway.data_class enum
// (migration 0003) and the flusher passes it raw into CopyFrom — an empty
// value fails the enum cast and poisons the ENTIRE batch.
func (a *PrimaryFSMAuditAdapter) WriteStateChange(kind string, ev any) error {
	if a == nil || a.W == nil {
		return nil
	}
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	payload, ok := ev.(map[string]any)
	if !ok {
		log.Warn("primary FSM audit adapter: unexpected payload type — event dropped",
			"kind", kind, "type", fmt.Sprintf("%T", ev))
		return nil
	}
	from, _ := payload["from"].(string)
	to, _ := payload["to"].(string)
	reason, _ := payload["reason"].(string)
	at, _ := payload["at"].(time.Time) // zero → WriteStateChange defaults TS to now

	a.W.WriteStateChange("primary_state_change", Event{
		TS:       at,
		Route:    "primary_fsm_transition",
		Method:   from + "->" + to,
		Upstream: to,
		// NOT NULL enum — see doc comment above (batch-poison hazard).
		DataClass: "normal",
		// CR-03 parity with the emergency FSM: the human-readable cause
		// rides the dedicated reason column, never ErrorCode.
		Reason: fmt.Sprintf("%s→%s (%s)", from, to, reason),
	})
	return nil
}
