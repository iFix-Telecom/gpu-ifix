package alert

// client.go — the alert-delivery contract. This is the interface-first
// seam of the Phase 7 alerting subsystem: the `Channel` interface and
// the `Message` struct are defined HERE, before the concrete clients,
// so that:
//
//   - chatwoot.go / clickup.go / brevo.go implement `Channel`;
//   - alerter.go (plan 07-05) consumes a `[]Channel` and never imports
//     a concrete client type;
//   - main.go (plan 07-06) builds the concrete clients, type-asserts
//     them to `Channel`, and hands the slice to the alerter.
//
// Programming the alerter against this contract (not the concrete
// clients) keeps the fan-out logic testable with the recording fakes in
// testsupport.go and lets a disabled channel simply be omitted from the
// slice.

import "context"

// Severity classifies an alert. The alerter (plan 07-05) maps each
// inbound Pub/Sub event to one of these and uses it to decide the
// channel fan-out: critical → Chatwoot + ClickUp + Brevo; warning →
// ClickUp + Brevo (see 07-RESEARCH.md fan-out table). Defined as a
// string type (not an int enum) so it round-trips through JSON / logs
// legibly and so an unknown future severity is still a readable value.
type Severity string

const (
	// SeverityCritical is a GPU-down / failover-active / emergency-pod
	// class event — the on-call operator must see it on WhatsApp.
	SeverityCritical Severity = "critical"
	// SeverityWarning is a degraded-but-serving class event — a ClickUp
	// task + an email is enough; it does not page WhatsApp.
	SeverityWarning Severity = "warning"
	// SeverityInfo is a benign / recovery-class event — recorded for the
	// dashboard banner + the logs, but it fans out to NO external channel
	// (see channelsFor in severity.go). A breaker closing, the shed FSM
	// disarming, or the emergency FSM returning to healthy are info-tier.
	SeverityInfo Severity = "info"
)

// Message is the channel-agnostic alert payload. The alerter builds one
// of these per logical alert and hands the SAME value to every channel
// in the fan-out; each channel renders it into its own wire format
// (Chatwoot conversation message, ClickUp task, SMTP body).
//
// Fingerprint is the stable dedup identity (see redisx.AlertDedupKey) —
// two events that represent the same logical incident MUST produce the
// same Fingerprint so the alerter's SET NX dedup gate collapses them.
type Message struct {
	// Severity drives the channel fan-out (critical vs warning).
	Severity Severity
	// Title is the short headline — the ClickUp task name, the email
	// Subject, the first line of the Chatwoot message.
	Title string
	// Body is the human-readable detail — the ClickUp task description,
	// the email body, the rest of the Chatwoot message.
	Body string
	// Fingerprint is the alerter's stable dedup key for this logical
	// alert (e.g. "breaker:openrouter:open"). Used by alert/dedup.go
	// (plan 07-05) as the suffix for redisx.AlertDedupKey.
	Fingerprint string
}

// Channel is one alert-delivery destination — Chatwoot, ClickUp, or
// Brevo. The alerter holds a `[]Channel` and, for each inbound alert,
// calls Send on every channel its severity fans out to. Implementations
// MUST be safe to call from the alerter's goroutine; each concrete
// client wraps its outbound call in its own circuit breaker so a dead
// provider fails fast instead of stalling the fan-out.
//
// Send's error is advisory: the alerter logs it + bumps a metric, but a
// failed channel never blocks the others. The error MUST NOT contain
// the channel's API token, URL, or any header (threat T-07-11).
type Channel interface {
	// Name identifies the channel for logs + the gateway_alert_sends_total
	// metric label. One of "chatwoot", "clickup", "brevo".
	Name() string
	// Send delivers msg to the channel's external service. Returns nil
	// on success; a secret-free error on failure.
	Send(ctx context.Context, msg Message) error
}
