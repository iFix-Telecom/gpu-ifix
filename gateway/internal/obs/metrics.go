// Package obs (metrics.go): Prometheus collectors exposed by /metrics.
// Phase 2 budget is two counters (per CONTEXT.md Plumbing); Phase 7 adds
// latency histograms + per-tenant labels. Keep cardinality bounded.
package obs

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// RequestsTotal counts all requests to gateway routes, labelled by
// route template (not raw path — bounded cardinality per CONTEXT.md
// Plumbing). Phase 7 adds latency histograms.
var RequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total requests received by the gateway, by route template and HTTP status class.",
	},
	[]string{"route", "status"},
)

// AuditDroppedTotal counts audit events dropped because the writer
// buffer was full (D-B4 fail-safe). Non-zero value indicates backpressure.
var AuditDroppedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "gateway_audit_dropped_total",
		Help: "Audit events dropped because the writer buffer was full.",
	},
)

// ApikeyTouchBufferedTotal counts Verify-path enqueues into TouchBuffer.
// Codex review [MEDIUM] 02-03 — debounced last_used_at updates.
var ApikeyTouchBufferedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "gateway_apikey_touch_buffered_total",
		Help: "Total api_key touch enqueues into the debounced buffer.",
	},
)

// ApikeyTouchFlushTotal counts flush cycles (not individual UPDATEs).
// Codex review [MEDIUM] 02-03.
var ApikeyTouchFlushTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "gateway_apikey_touch_flush_total",
		Help: "Total flush cycles of the debounced api_key touch buffer.",
	},
)

// Phase 3 — Circuit breaker + probe + fallback metrics.

// BreakerState is the current circuit breaker state per upstream.
// 0=closed, 1=half-open, 2=open. Updated from gobreaker OnStateChange.
var BreakerState = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gateway_breaker_state",
		Help: "Current circuit breaker state per upstream. 0=closed, 1=half-open, 2=open.",
	},
	[]string{"upstream"},
)

// BreakerTripsTotal counts CLOSED→OPEN transitions per upstream.
var BreakerTripsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_breaker_trips_total",
		Help: "Count of CLOSED to OPEN transitions per upstream.",
	},
	[]string{"upstream"},
)

// BreakerMirrorFailuresTotal counts Redis HSET/PUBLISH failures when
// mirroring breaker state. Breakers keep operating in-process on
// failure (CONTEXT.md D-D1).
var BreakerMirrorFailuresTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "gateway_breaker_mirror_failures_total",
		Help: "Redis HSET/PUBLISH failures when mirroring breaker state. Breakers keep operating in-process on failure (D-D1).",
	},
)

// ProbeDurationMs is the synthetic E2E probe latency per upstream.
var ProbeDurationMs = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gateway_probe_duration_ms",
		Help:    "Synthetic E2E probe latency per upstream.",
		Buckets: []float64{50, 100, 250, 500, 1000, 2500, 5000},
	},
	[]string{"upstream"},
)

// ProbeFailureTotal counts probe failures per upstream, labeled by
// failure reason (timeout, status, etc.).
var ProbeFailureTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_probe_failure_total",
		Help: "Probe failures per upstream, labeled by failure reason.",
	},
	[]string{"upstream", "reason"},
)

// UpstreamsReloadTotal counts upstreams.Loader.Refresh invocations,
// labelled by outcome ("ok" | "error"). Phase 3 D-D2 — incremented at
// boot Refresh and on each LISTEN/NOTIFY-driven reload. Helps operators
// detect reload storms (Pitfall 7) or persistent DB read failures.
var UpstreamsReloadTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_upstreams_reload_total",
		Help: "Hot-reload attempts from LISTEN upstreams_changed. result=ok|error.",
	},
	[]string{"result"},
)

// PodConfigReloadTotal counts podconfig.Loader.Refresh invocations,
// labelled by outcome ("ok" | "error"). Phase 17 — incremented at boot
// Refresh and on each LISTEN pod_config_changed NOTIFY. Mirrors
// UpstreamsReloadTotal. A persistent "error" rate means the reconciler is
// serving the last-good snapshot (T-17-04 last-good-on-error invariant)
// rather than a fresh read — operators watch this to detect a stuck
// pod_config read without provisioning ever stalling on a DB hiccup.
var PodConfigReloadTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_pod_config_reload_total",
		Help: "Hot-reload attempts from LISTEN pod_config_changed. result=ok|error.",
	},
	[]string{"result"},
)

// UpstreamThrottledTotal counts HTTP 429 responses per upstream.
// Tracked separately from breaker failures (CONTEXT.md D-A4).
var UpstreamThrottledTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_upstream_throttled_total",
		Help: "HTTP 429 responses per upstream. Tracked separately from breaker failures (D-A4).",
	},
	[]string{"upstream", "status"},
)

// SensitiveRetryTotal records outcomes of the sensitive-tenant retry
// loop. outcome=closed|exhausted|canceled.
var SensitiveRetryTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_sensitive_retry_total",
		Help: "Outcomes of the sensitive-tenant retry loop. outcome=closed|exhausted|canceled.",
	},
	[]string{"outcome"},
)

// ToolCallPartialTotal counts streams interrupted after a tool_call
// chunk was emitted (RES-06 — never retry tool calls).
var ToolCallPartialTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_tool_call_partial_total",
		Help: "Streams interrupted after tool_call emission (RES-06).",
	},
	[]string{"route", "upstream"},
)

// Phase 4 — quota + tenants + schedule collectors.

// GatewayRateLimitRejected counts requests rejected by the rate-limit
// middleware, labeled by tenant and window ("rps" | "rpm").
var GatewayRateLimitRejected = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_rate_limit_rejected_total",
	Help: "Number of requests rejected by the rate-limit middleware, labeled by tenant and window (rps|rpm).",
}, []string{"tenant", "window"})

// GatewayRateLimitCheckFailures counts rate-limit check failures (Lua
// transport errors, Redis down) — incremented when the fail-open path is
// taken (cfg.RateLimitFailOpen=true, D-A2).
var GatewayRateLimitCheckFailures = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_rate_limit_check_failures_total",
	Help: "Number of rate-limit check failures (Lua transport errors, etc.), labeled by reason. Incremented when fail-open path is taken (cfg.RateLimitFailOpen=true).",
}, []string{"reason"})

// GatewayQuotaRejected counts requests rejected by the quota middleware,
// labeled by tenant, dimension (tokens|audio_minutes|embeds), and period
// (daily|monthly).
var GatewayQuotaRejected = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_quota_rejected_total",
	Help: "Number of requests rejected by the quota middleware, labeled by tenant, dimension (tokens|audio_minutes|embeds), and period (daily|monthly).",
}, []string{"tenant", "dimension", "period"})

// GatewayQuotaCheckFailures counts times the quota check could not run
// (Postgres lookup failed; fail-closed per D-A2). Labels: reason=today|month.
var GatewayQuotaCheckFailures = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_quota_check_failures_total",
	Help: "Number of times the quota check could not run (Postgres lookup failed, fail-closed). Labeled by reason: today|month.",
}, []string{"reason"})

// GatewayScheduleRouting counts pre-dispatch routing decisions made by
// the schedule middleware. Labels: tenant, decision=local|off_hours_external.
var GatewayScheduleRouting = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_schedule_routing_total",
	Help: "Number of routing decisions made by the schedule middleware, labeled by tenant and decision (local|off_hours_external).",
}, []string{"tenant", "decision"})

// GatewayTenantsReload counts tenants-config reloads triggered by
// NOTIFY tenants_changed or boot refresh. Labels: result=ok|error.
var GatewayTenantsReload = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_tenants_reload_total",
	Help: "Number of tenants config reloads triggered by NOTIFY tenants_changed, labeled by result (ok|error).",
}, []string{"result"})

// Phase 4 — billing + admin collectors (consolidated here to avoid same-
// wave file conflict with Plan 04-05; Plan 04-05 references these names
// but does NOT touch obs/metrics.go).

// GatewayBillingFlush counts billing events flushed to Postgres. Labels:
// source=final|partial|ok.
var GatewayBillingFlush = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_billing_flush_total",
	Help: "Number of billing events flushed to Postgres, labeled by source (final|partial|ok).",
}, []string{"source"})

// GatewayBillingFlushFailures counts billing flush failures. Labels: reason.
var GatewayBillingFlushFailures = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_billing_flush_failures_total",
	Help: "Number of billing flush failures, labeled by reason.",
}, []string{"reason"})

// GatewayBillingFlushDropped counts billing events dropped at Enqueue
// because the channel buffer was full (back-pressure).
var GatewayBillingFlushDropped = promauto.NewCounter(prometheus.CounterOpts{
	Name: "gateway_billing_flush_dropped_total",
	Help: "Number of billing events dropped at Enqueue due to channel full (back-pressure).",
})

// GatewayPricesReload counts prices/fx config reloads triggered by NOTIFY.
// Labels: result=ok|error.
var GatewayPricesReload = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_prices_reload_total",
	Help: "Number of prices/fx config reloads triggered by NOTIFY, labeled by result (ok|error).",
}, []string{"result"})

// GatewayPricesMissing counts price-lookup misses during cost attribution.
// Labels: model, provider, unit. ME-05 fix — surfaces cost drift that
// would otherwise accumulate silently in billing_events.cost_external_brl
// between a new-model deploy and the corresponding INSERT into prices.
// Cardinality budget: (model × provider × unit) — kept bounded by the
// small Phase 4 model catalog.
var GatewayPricesMissing = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_prices_missing_total",
	Help: "Number of cost-attribution calls that could not resolve a price row, labeled by (model, provider, unit). Non-zero indicates billing drift.",
}, []string{"model", "provider", "unit"})

// GatewayAdminRequests counts admin endpoint requests served. Labels:
// route, status class.
var GatewayAdminRequests = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_admin_requests_total",
	Help: "Number of admin endpoint requests served, labeled by route and status class.",
}, []string{"route", "status"})

// ============================================================================
// Phase 5 — Load Shedding (saturation-aware routing). CONTEXT.md D-D4.
// Cardinality budget: 3 tier-0 upstreams x 6 tenants + 4 FSM states +
// transitions = ~60 added series (<< 10k, Pitfall 13).
// ============================================================================

// GatewayInflight is the current in-flight request gauge per upstream
// (sum across tenants). Updated by shed middleware via atomic counters;
// consumed by the 2-of-3 saturation composite.
var GatewayInflight = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_inflight",
	Help: "Current in-flight requests per upstream (sum across tenants). Atomic counter.",
}, []string{"upstream"})

// GatewayInflightTenant is the per-(upstream,tenant) inflight gauge
// used for fairness enforcement (D-B1). Cardinality: 3 upstreams x 6
// known tenants = 18 series.
var GatewayInflightTenant = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_inflight_tenant",
	Help: "Current in-flight requests per (upstream, tenant). Cardinality: 3 upstreams x 6 tenants = 18 series.",
}, []string{"upstream", "tenant"})

// GatewayInflightTier1 counts in-flight requests routed to tier-1
// during shedding. Dashboard-only signal; NOT consumed by shed FSM
// decisions (tier-1 never sheds).
var GatewayInflightTier1 = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_inflight_tier1",
	Help: "In-flight requests routed to tier-1 during shedding. Dashboard-only; not used in decisions.",
}, []string{"upstream"})

// GatewayShedState is the current shed FSM state per upstream.
// 0=off, 1=armed, 2=on, 3=recovering (D-C1).
var GatewayShedState = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_shed_state",
	Help: "Current shed FSM state per upstream. 0=off, 1=armed, 2=on, 3=recovering.",
}, []string{"upstream"})

// GatewayShedDecisions counts routing decisions made by shed middleware.
// reason ∈ {passed, inflight, p95, vram, tenant_cap, sensitive_capped,
// skipped_peak_offhours, tier1_unavailable}.
var GatewayShedDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_shed_decisions_total",
	Help: "Count of routing decisions made by shed middleware. reason=passed|inflight|p95|vram|tenant_cap|sensitive_capped|skipped_peak_offhours|tier1_unavailable.",
}, []string{"upstream", "reason"})

// GatewayShedTransitions counts FSM transitions per upstream, labeled
// by (from, to) state pairs. Observes flapping (Pitfall 2).
var GatewayShedTransitions = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_shed_transitions_total",
	Help: "Count of FSM transitions per upstream, labeled by from -> to states.",
}, []string{"upstream", "from", "to"})

// GatewayShedMirrorFailures counts Redis HSET/PUBLISH failures while
// mirroring shed state. FSMs keep operating in-process on failure
// (D-C3, same philosophy as breaker mirror).
var GatewayShedMirrorFailures = promauto.NewCounter(prometheus.CounterOpts{
	Name: "gateway_shed_mirror_failures_total",
	Help: "Count of Redis HSET/PUBLISH failures mirroring shed state. FSMs keep operating in-process (D-C3).",
})

// GatewayShedMirrorDropped counts FSM transitions that were not
// mirrored because the bounded publish worker pool was saturated
// (WR-03). Bumped when MakePublishTransition's job channel is full —
// a non-blocking signal that the gateway is mid-incident with FSM
// flapping. Non-zero rate indicates a configuration issue (thresholds
// too tight, hysteresis too short) rather than a Redis outage.
var GatewayShedMirrorDropped = promauto.NewCounter(prometheus.CounterOpts{
	Name: "gateway_shed_mirror_dropped_total",
	Help: "Count of FSM transitions dropped because the publish worker pool was saturated (WR-03).",
})

// GatewayShedInflightUnknownUpstream counts Inc/Dec calls on
// InflightRegistry for an upstream name not present in the registry
// (WR-05). Non-zero indicates a wiring bug — the shed middleware
// resolved an upstream that the inflight registry has not been
// rebuilt for. Most often surfaces during hot-reload windows when
// a new upstream row was inserted but the inflight registry has not
// yet been re-created.
var GatewayShedInflightUnknownUpstream = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_shed_inflight_unknown_upstream_total",
	Help: "Inflight Inc/Dec calls targeting an upstream missing from the registry (WR-05 wiring-bug detector).",
}, []string{"upstream", "op"})

// GatewayShedMirrorReconcile counts periodic HGETALL reconcile outcomes
// (RESEARCH Pitfall 3 mitigation: boot may start in OFF while another
// replica is ON). result=ok|diverged|error.
var GatewayShedMirrorReconcile = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_shed_mirror_reconcile_total",
	Help: "Periodic HGETALL reconcile outcomes (RESEARCH Pitfall 3). result=ok|diverged|error.",
}, []string{"result"})

// GatewayShedBlockedSensitive counts sensitive-tenant 503 blocks due to
// saturation + LGPD policy (D-B3).
var GatewayShedBlockedSensitive = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_shed_blocked_sensitive_total",
	Help: "Count of sensitive tenants blocked with 503 due to saturation + LGPD policy.",
}, []string{"tenant"})

// GatewayShedThresholdsChanged counts circuit_config JSONB hot-reloads
// that changed any shed_* field per upstream (D-C5).
var GatewayShedThresholdsChanged = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_shed_thresholds_changed_total",
	Help: "Count of circuit_config JSONB hot-reloads that changed shed_* fields.",
}, []string{"upstream"})

// GatewayShedForceActive is 1 when an operator override
// gw:shed:force:{upstream} Redis shadow key is set; 0 otherwise (D-C5).
var GatewayShedForceActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_shed_force_active",
	Help: "Operator override active via gw:shed:force:{upstream} Redis shadow key. 0=off, 1=active.",
}, []string{"upstream"})

// GatewayVramUsedMiB is the GPU framebuffer memory used in MiB, scraped
// from DCGM_FI_DEV_FB_USED on the pod's :9400/metrics endpoint. DCGM's
// native unit is MiB (RESEARCH Pitfall 1 — do NOT convert to bytes).
var GatewayVramUsedMiB = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "gateway_vram_used_mib",
	Help: "GPU framebuffer memory used (MiB), scraped from DCGM_FI_DEV_FB_USED on pod's :9400/metrics. DCGM native unit is MiB (RESEARCH Pitfall 1).",
})

// GatewayP95RequestMs is the rolling p95 request duration per upstream
// in milliseconds, derived from the shed latency ring buffer
// (last ~SHED_LATENCY_RING_SIZE samples; default 200).
var GatewayP95RequestMs = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_p95_request_ms",
	Help: "Rolling p95 request duration per upstream (ms), derived from shed latency ring buffer (last ~200 samples).",
}, []string{"upstream"})

// GatewayDcgmScrapeFailures counts DCGM scrape failures. reason ∈
// {http_error, status_<n>, parse_error, metric_missing, metric_not_gauge,
// sanity_check}.
var GatewayDcgmScrapeFailures = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_dcgm_scrape_failures_total",
	Help: "Count of DCGM scrape failures. reason=http_error|status_<n>|parse_error|metric_missing|metric_not_gauge|sanity_check.",
}, []string{"reason"})

// ============================================================================
// Phase 6 — Emergency-pod auto-provisioning (Vast.ai). CONTEXT.md D-E2.
// Cardinality budget (matches budget OBS-02 ceiling 10k series; consumed in
// Phase 7 dashboard):
//   - GatewayEmergencyState: 7 FSM states (BLOCKER 3 revision reduced
//     from 9 to 7; OFF_HOURS + MAINTENANCE deferred)
//   - GatewayEmergencyLifecyclesTotal: ~22 series (2 trigger_reason ×
//     11 shutdown_reason)
//   - GatewayEmergencyActivePod: 1 active pod_url at most
//   - GatewayEmergencyCostDPH: 1 series per *live* lifecycle (PRV-05
//     guarantees ≤1; cleared on cutback)
//   - GatewayVastAPIRequestsTotal: ~50 series (5 op × ~10 status)
//   Total Phase 6 baseline: ~80 series.
// ============================================================================

// GatewayEmergencyState is the current emergency FSM state. Caller sets
// 1 for the live state, 0 for the others (D-E2). Label values:
// "healthy" | "degraded" | "failed_over" | "emergency_provisioning" |
// "emergency_active" | "recovering" | "cooldown" (7-state FSM).
var GatewayEmergencyState = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_emergency_state",
	Help: "Current emergency FSM state. 1 for active, 0 for others.",
}, []string{"state"})

// GatewayEmergencyLifecyclesTotal counts emergency lifecycles by trigger
// and shutdown reason. trigger_reason ∈ {failed_over_sustained, manual_force}
// × shutdown_reason ∈ {cutback_idle, cancelled_in_flight, health_timeout,
// offer_race_lost, manual, budget_exceeded, instance_terminal_state,
// leader_recovery_zombie, leader_recovery_lost, leader_recovery_pre_create,
// no_offers_below_cap}.
var GatewayEmergencyLifecyclesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_emergency_lifecycles_total",
	Help: "Count of emergency lifecycles by trigger and shutdown reason.",
}, []string{"trigger_reason", "shutdown_reason"})

// GatewayEmergencyActivePod is 1 when an emergency pod is live serving
// traffic (FSM state EMERGENCY_ACTIVE), 0 otherwise. The pod_url label
// rotates per lifecycle (Vast.ai assigns a new ip:port each create).
// PRV-05 guarantees only one live series at a time.
var GatewayEmergencyActivePod = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_emergency_active_pod",
	Help: "1 when emergency pod is live serving traffic; 0 otherwise.",
}, []string{"pod_url"})

// GatewayEmergencyProvisionDurationSeconds is the elapsed time from the
// first search_offers call to the first /health pass for emergency pod
// provisions. Buckets cover 30s (best case Vast.ai already-warm host)
// through 900s (15 min — well past the 10 min cold-start budget;
// outliers indicate MinIO/inet weight pull degraded).
var GatewayEmergencyProvisionDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "gateway_emergency_provision_duration_seconds",
	Help:    "Time from search to /health pass for emergency pod provisions.",
	Buckets: []float64{30, 60, 120, 300, 600, 900},
})

// GatewayEmergencyCostDPH is the current emergency pod USD-per-hour cost
// for the live lifecycle. lifecycle_id is high-cardinality but bounded
// to 1 live series at any time (PRV-05); operators may drop closed
// lifecycle_ids in scrape config if accumulated cardinality grows.
var GatewayEmergencyCostDPH = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_emergency_cost_dph",
	Help: "Current emergency pod USD per hour cost.",
}, []string{"lifecycle_id"})

// GatewayEmergencyMonthCostBRL is the running monthly aggregate cost in
// BRL of *closed* emergency lifecycles (sums total_cost_brl WHERE
// started_at >= date_trunc('month', NOW()) AND ended_at IS NOT NULL).
// Updated every 60s by the reconciler (D-D2). Compares against
// MonthlyEmergencyBudgetBRL to fire the Sentry budget warning.
var GatewayEmergencyMonthCostBRL = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "gateway_emergency_month_cost_brl",
	Help: "Running monthly aggregate cost in BRL of closed lifecycles.",
})

// GatewayVastAPIRequestsTotal counts Vast.ai REST requests by operation
// and HTTP status. op ∈ {search, create, get, destroy, ping}; status ∈
// {started, transport_error, 200, 401, 403, 404, 409, 410, 422, 429,
// 500, 502, 503, 504}. The "started" status is incremented before the
// HTTP call so operators can see in-flight requests vs completions
// (transport_error covers ctx cancellation + DNS/connect failures).
var GatewayVastAPIRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_vast_api_requests_total",
	Help: "Vast.ai REST requests by operation and HTTP status (or 'transport_error', 'started').",
}, []string{"op", "status"})

// ============================================================================
// Phase 7 — Observability dashboard latency histograms + alerting drop counter.
// Cardinality budget (well under the OBS-02 10k-series ceiling):
//   - RequestDurationByRoute:    ~4 routes    × 9 buckets ≈ 44 series
//   - RequestDurationByUpstream: ~6 upstreams × 9 buckets ≈ 60 series
//   - AlertDroppedTotal:         1 plain counter
//   - AlertSendsTotal:           3 channels × 2 results = 6 series
//   Total Phase 7 baseline: ~111 series.
// Deliberately NOT crossed: there is no tenant × route × upstream histogram.
// Per-tenant percentiles come from audit_log SQL aggregation in plan 07-03,
// not from a Prometheus label — adding a tenant label here would multiply
// every bucket by the tenant count and blow the cardinality budget.
// ============================================================================

// RequestDurationByRoute is the end-to-end gateway request latency in
// milliseconds, bucketed by route template only (bounded cardinality —
// the route label is the same small fixed set used by RequestsTotal).
// Consumed by the observability dashboard's per-route p50/p95/p99 panels.
var RequestDurationByRoute = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gateway_request_duration_ms_by_route",
		Help:    "End-to-end gateway request latency (ms), bucketed by route template. ~4 routes x 9 buckets.",
		Buckets: []float64{25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	},
	[]string{"route"},
)

// RequestDurationByUpstream is the end-to-end gateway request latency in
// milliseconds, bucketed by the resolved upstream only. Lets the dashboard
// compare local-GPU vs OpenRouter/OpenAI fallback latency without a
// per-tenant explosion.
var RequestDurationByUpstream = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gateway_request_duration_ms_by_upstream",
		Help:    "End-to-end gateway request latency (ms), bucketed by resolved upstream. ~6 upstreams x 9 buckets.",
		Buckets: []float64{25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	},
	[]string{"upstream"},
)

// AlertDroppedTotal counts alert events the alerter dropped because a
// per-channel worker queue was full (same fail-safe shape as
// AuditDroppedTotal — a non-blocking back-pressure signal). A non-zero
// value means the gateway is mid-incident and producing alerts faster
// than the Chatwoot/ClickUp/Brevo channels can drain them.
var AlertDroppedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "gateway_alert_dropped_total",
		Help: "Alert events dropped because a per-channel worker queue was full (back-pressure fail-safe).",
	},
)

// AlertSendsTotal counts alert-delivery attempts per channel and
// outcome. The alerter (plan 07-05) increments this once per Channel.Send
// call. Cardinality is fixed and tiny: channel ∈ {chatwoot, clickup,
// brevo} × result ∈ {ok, err} = 6 series — no tenant or fingerprint
// label, deliberately, so an alert storm cannot inflate the series
// count. A rising "err" rate for one channel means that provider is
// down (its circuit breaker should also be open); a flat "ok" rate
// across all channels during a known incident means the alerter is not
// receiving events.
var AlertSendsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_alert_sends_total",
		Help: "Alert delivery attempts, labeled by channel (chatwoot|clickup|brevo) and result (ok|err).",
	},
	[]string{"channel", "result"},
)

// ============================================================================
// Phase 12 — Gateway resilience remediation. Single-owner counters defined
// here so Plan 12-01 owns obs/metrics.go and the two Wave-2 plans (02 death
// detection, 03 dial fallthrough) increment them in parallel without a
// file-edit conflict. Increments are NOT wired here — the consuming code in
// Plans 02/03 calls .WithLabelValues(...).Inc(). Cardinality bounded: both
// label sets are small fixed enums (role/outcome/cause).
// ============================================================================

// DialFallthroughTotal counts tier-0 dial failures that fell through to the
// tier-1 cascade (RES-13, Plan 03). outcome ∈ {tier1_served, chain_exhausted,
// sensitive_blocked}. ALERTABLE: outcome=chain_exhausted (no tier serving —
// drives a dashboard/alert series in a future observability phase).
var DialFallthroughTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_dial_fallthrough_total",
		Help: "Tier-0 dial failures that fell through to tier-1, by role and outcome (tier1_served|chain_exhausted|sensitive_blocked).",
	},
	[]string{"role", "outcome"},
)

// PrimaryDeathDetectedTotal counts confirmed primary-pod deaths detected on
// the Ready tick (RES-11, Plan 02). cause ∈ {billing_stopped, host_death,
// not_found}. ALERTABLE: cause=billing_stopped (Vast account out of credit —
// operator-actionable; drives a critical alert series).
var PrimaryDeathDetectedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_primary_death_detected_total",
		Help: "Confirmed primary-pod deaths detected on the Ready tick, by cause (billing_stopped|host_death|not_found).",
	},
	[]string{"cause"},
)

// Handler returns the /metrics endpoint handler.
func Handler() http.Handler { return promhttp.Handler() }
