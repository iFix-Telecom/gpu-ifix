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

// Handler returns the /metrics endpoint handler.
func Handler() http.Handler { return promhttp.Handler() }
