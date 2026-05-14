package alert

// severity.go — the pure event → severity tier → channel-matrix mapping
// for the Phase 7 alerting goroutine (OBS-04). This file is the analog
// of breaker.stateFloat: a small, deterministic switch with NO I/O — it
// does not touch Redis, the network, or a database. Everything here is a
// transform from "a raw Pub/Sub payload" to "(tier, Message)" or from
// "(tier)" to "the set of channels that tier fans out to".
//
// Keeping classification pure means alerter.go can be tested by feeding
// it synthetic events, and means the dedup/fan-out logic never has to
// reason about JSON shapes — severityFor owns that entirely.
//
// # The channel matrix (07-CONTEXT.md)
//
//	critical → Chatwoot + ClickUp + Brevo   (page the on-call operator)
//	warning  → ClickUp + Brevo              (a task + an email, no WhatsApp)
//	info     → {}                           (dashboard banner / log only)
//
// # Fingerprints
//
// Every classified event carries a STABLE Fingerprint of the form
// "<source>:<key>:<state>" — deterministic for the same logical incident
// so the alerter's SET NX dedup gate (dedup.go) collapses a flapping
// storm into one notification. Crucially the fingerprint does NOT
// include the event's timestamp: a breaker re-tripping for the same
// upstream must produce the same fingerprint, or the dedup never fires.

import (
	"encoding/json"
	"fmt"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// primaryLLMUpstream is the name of the tier-0 GPU upstream. A breaker
// or shed event for THIS upstream is the "GPU primary down / saturated"
// signal — critical-class — whereas the same event for a fallback
// upstream (openrouter, openai) is only warning-class, because the
// fallback chain is doing its job.
const primaryLLMUpstream = "local-llm"

// severityFor classifies one raw Pub/Sub event into a tier + a
// channel-agnostic Message. channel is the Pub/Sub channel name the
// message arrived on (it discriminates the payload shape); payload is
// the raw JSON bytes.
//
// Returns an error — never panics — when:
//   - the channel name is not one of the three known event channels;
//   - the payload does not unmarshal into the matching redisx.*Event.
//
// The alerter's consume loop treats that error as "log a WARN and
// continue" (threat T-07-17): a malformed or hostile payload can never
// crash the goroutine or be reflected into a Send.
func severityFor(channel string, payload []byte) (Severity, Message, error) {
	switch channel {
	case redisx.BreakerEventsChannel():
		return severityForBreaker(payload)
	case redisx.ShedEventsChannel:
		return severityForShed(payload)
	case redisx.EmergEventsChannel:
		return severityForEmerg(payload)
	default:
		return "", Message{}, fmt.Errorf("alert: unknown event channel %q", channel)
	}
}

// severityForBreaker classifies a gw:breaker:events payload.
//
//   - primary (local-llm) breaker → open  = critical (GPU primary down)
//   - any other upstream breaker  → open  = warning  (a fallback degraded;
//     the chain still serves)
//   - any breaker → closed / half-open    = info     (recovery)
func severityForBreaker(payload []byte) (Severity, Message, error) {
	var ev redisx.BreakerEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return "", Message{}, fmt.Errorf("alert: malformed breaker event: %w", err)
	}
	var sev Severity
	switch ev.State {
	case "open":
		if ev.Upstream == primaryLLMUpstream {
			sev = SeverityCritical
		} else {
			sev = SeverityWarning
		}
	default: // "closed", "half-open", or anything benign
		sev = SeverityInfo
	}
	title := fmt.Sprintf("Circuit breaker %s → %s", ev.Upstream, ev.State)
	body := fmt.Sprintf("Upstream %q circuit breaker transitioned to %q.", ev.Upstream, ev.State)
	if ev.Reason != "" {
		body += " Reason: " + ev.Reason + "."
	}
	return sev, Message{
		Severity:    sev,
		Title:       title,
		Body:        body,
		Fingerprint: fmt.Sprintf("breaker:%s:%s", ev.Upstream, ev.State),
	}, nil
}

// severityForShed classifies a gw:shed:events payload.
//
//   - shed FSM → on          = warning (sustained saturation; tier-0→tier-1
//     shedding is live for capped tenants)
//   - shed FSM → armed/off/…  = info   (saturation observed but not yet
//     sustained, or shedding stood down)
func severityForShed(payload []byte) (Severity, Message, error) {
	var ev redisx.ShedEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return "", Message{}, fmt.Errorf("alert: malformed shed event: %w", err)
	}
	sev := SeverityInfo
	if ev.State == "on" {
		sev = SeverityWarning
	}
	title := fmt.Sprintf("Load shedding %s → %s", ev.Upstream, ev.State)
	body := fmt.Sprintf("Load-shedding FSM for upstream %q transitioned to %q.", ev.Upstream, ev.State)
	if ev.Reason != "" {
		body += " Reason: " + ev.Reason + "."
	}
	if ev.Signals != nil {
		body += fmt.Sprintf(" Signals: inflight=%d, p95=%dms, vram=%dMiB.",
			ev.Signals.Inflight, ev.Signals.P95Ms, ev.Signals.VramMiB)
	}
	return sev, Message{
		Severity:    sev,
		Title:       title,
		Body:        body,
		Fingerprint: fmt.Sprintf("shed:%s:%s", ev.Upstream, ev.State),
	}, nil
}

// emergCriticalStates is the set of emergency-FSM states that page the
// on-call operator: the gateway is actively provisioning or running on
// an emergency Vast.ai pod.
var emergCriticalStates = map[string]bool{
	"emergency_provisioning": true,
	"emergency_active":       true,
}

// emergWarningStates is the set of emergency-FSM states that warrant a
// task + an email but not a WhatsApp page: the primary failed over to
// the fallback chain, or cutback is in progress.
var emergWarningStates = map[string]bool{
	"failed_over": true,
	"recovering":  true,
}

// severityForEmerg classifies a gw:emerg:events payload.
//
// Only "transition" events are alert-worthy here — command events
// (force_provision_request / force_destroy_request) are operator intents
// the reconciler consumes, not incidents. They classify as info so they
// are still logged but never page.
//
//   - transition → emergency_provisioning / emergency_active = critical
//   - transition → failed_over / recovering                  = warning
//   - transition → healthy / degraded / cooldown             = info
func severityForEmerg(payload []byte) (Severity, Message, error) {
	var ev redisx.EmergEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return "", Message{}, fmt.Errorf("alert: malformed emerg event: %w", err)
	}
	sev := SeverityInfo
	switch {
	case emergCriticalStates[ev.State]:
		sev = SeverityCritical
	case emergWarningStates[ev.State]:
		sev = SeverityWarning
	}
	title := fmt.Sprintf("Emergency pod FSM → %s", ev.State)
	body := fmt.Sprintf("Emergency-pod FSM event %q, state %q.", ev.Type, ev.State)
	if ev.LifecycleID != 0 {
		body += fmt.Sprintf(" Lifecycle #%d.", ev.LifecycleID)
	}
	if ev.Reason != "" {
		body += " Reason: " + ev.Reason + "."
	}
	return sev, Message{
		Severity:    sev,
		Title:       title,
		Body:        body,
		Fingerprint: fmt.Sprintf("emerg:%s:%s", ev.Type, ev.State),
	}, nil
}

// channelsFor returns the set of channel names a severity tier fans out
// to — the 07-CONTEXT.md channel matrix as a plain switch:
//
//	critical → {chatwoot, clickup, brevo}
//	warning  → {clickup, brevo}
//	info     → {} (nil)
//
// An unrecognised severity returns the empty set: the alerter never
// pages on a value it does not understand. The returned slice is a fresh
// allocation per call — callers may sort/filter it without aliasing a
// shared backing array.
func channelsFor(s Severity) []string {
	switch s {
	case SeverityCritical:
		return []string{"chatwoot", "clickup", "brevo"}
	case SeverityWarning:
		return []string{"clickup", "brevo"}
	default: // SeverityInfo and any unknown tier
		return nil
	}
}
