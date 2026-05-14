// Package alert: the in-gateway alerting goroutine and its per-channel
// clients. Phase 7 (OBS-04 / OBS-05) of the gpu-ifix project.
//
// The alerter is a single long-lived goroutine that fans alert events out
// to three independent delivery channels — Chatwoot (critical-tier
// WhatsApp), ClickUp (a task per critical/warning alert), and Brevo
// (critical/warning email). Each channel is wrapped in its own circuit
// breaker so a flaky provider cannot stall the others, and the fan-out is
// bounded: when a per-channel worker queue fills, the event is dropped and
// obs.AlertDroppedTotal is incremented rather than blocking the producer.
//
// # Contract ordering (interface-first)
//
// Plan 07-01 (this Wave-0 plan) lays only the shared test support —
// FakeChatwoot, FakeClickUp, FakeBrevo recording fakes in testsupport.go —
// so that the concrete-client plans (07-04) and the alerter plan (07-05)
// have stable test doubles to program against. The real Channel interface
// (Name() + Send(ctx, Message) error) and the Message struct are defined
// by plan 07-04 in client.go; the concrete chatwoot.go / clickup.go /
// brevo.go clients implement it.
//
// # Configuration
//
// Every alert channel is optional. The twelve CHATWOOT_/CLICKUP_/BREVO_/
// ALERT_EMAIL_ env vars (parsed in internal/config) default to empty;
// an unset channel logs one WARN at startup and stays disabled — the
// gateway never fails boot over a missing alert credential (threat
// T-07-01: credentials are plain Config strings and are never logged).
//
// # Secret handling
//
// Channel clients isolate their credential in exactly one method that
// touches the outbound request; sentinel errors returned to the alerter
// never include a token or a URL host. The recording fakes in this
// package hold no real credentials.
package alert
