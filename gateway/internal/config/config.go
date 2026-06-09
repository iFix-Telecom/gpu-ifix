// Package config loads runtime configuration for the gateway and the
// gatewayctl CLI from environment variables. Load is called once at
// startup; the returned Config is immutable for the lifetime of the
// process (CONTEXT.md D-D3 Plumbing + cobrancas-api src/config.ts pattern).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the typed view of required + optional env vars.
type Config struct {
	// HTTP
	Port              int           // GATEWAY_PORT (default 8080)
	ReadHeaderTimeout time.Duration // fixed 10s per CONTEXT.md Plumbing
	ReadTimeout       time.Duration // fixed 60s (whisper multipart)
	// DEPRECATED in Phase 3 — use WriteTimeoutChat/Embed/Audio per route.
	// Kept at 0 for backwards compatibility during the refactor; main.go
	// wire-up reads the three new fields below instead.
	WriteTimeout time.Duration
	// Operational note (Codex review [LOW] 02-01):
	//   WriteTimeout=0 is required for SSE chat streams but removes a slow-
	//   client-DoS defense on non-streaming routes. Phase 3 introduces the
	//   per-route WriteTimeout* fields below; HTTP wire-up in 03-08 reads
	//   them via http.TimeoutHandler per chi route.
	IdleTimeout    time.Duration // fixed 120s
	MaxHeaderBytes int           // fixed 1 MiB
	MaxBodyBytes   int64         // fixed 25 MiB (OpenAI audio limit)

	// Data layer
	PGDSN          string // AI_GATEWAY_PG_DSN (required)
	PGMaxConns     int32  // AI_GATEWAY_PG_MAX_CONNS (default 10)
	RedisAddr      string // AI_GATEWAY_REDIS_ADDR (required, host:port)
	RedisPassword  string // AI_GATEWAY_REDIS_PASSWORD (optional)
	RedisDB        int    // AI_GATEWAY_REDIS_DB (default 0; pick 1-15 to isolate from shared Redis)
	RedisKeyPrefix string // fixed "gw:" (CONTEXT.md Integration Points)

	// Upstreams
	UpstreamLLMURL          string // UPSTREAM_LLM_URL (required)
	UpstreamSTTURL          string // UPSTREAM_STT_URL (Phase 11.1: optional/deprecated — tier-0 STT removed; kept transitionally for old .env compat, no longer wired in main.go when empty)
	UpstreamEmbedURL        string // UPSTREAM_EMBED_URL (required)
	UpstreamHealthBridgeURL string // UPSTREAM_HEALTH_BRIDGE_URL (optional — Phase 3 D-D4: health-bridge is a pod-internal debug surface, not required for gateway operation)

	// Phase 06.7 — TTS (POST /v1/audio/speech) + voice-clone surface (Plan 07,
	// REVIEWS action #6 — explicit enumerated env). UpstreamTTSURL is a tier-0
	// placeholder overwritten by the reconciler's dynamic override (D-11);
	// UpstreamTTSPiperURL is the tier-1 Piper fallback target (GATE 3 Option A).
	UpstreamTTSURL      string // UPSTREAM_TTS_URL (tier-0 placeholder; reconciler override target for the pod Chatterbox server)
	UpstreamTTSPiperURL string // UPSTREAM_TTS_PIPER_URL (tier-1 Piper fallback per GATE 3 Option A — ulaw 8kHz -> WAV 16kHz adapter)
	TTSMaxInputChars    int    // TTS_MAX_INPUT_CHARS (synth-text DoS cap; default 4000)
	VoiceMaxUploadBytes int64  // VOICE_MAX_UPLOAD_BYTES (reference-WAV upload DoS cap; default 10485760 = 10 MiB)
	S3VoicePrefix       string // S3_VOICE_PREFIX (S3 key prefix for reference WAVs; default "voices"; MUST match the pod server CHATTERBOX_S3_VOICE_PREFIX, Plan 05)

	// Phase 3 — External fallback upstreams (optional at boot; warn-log if a
	// row in ai_gateway.upstreams is enabled but the env it points to is missing)
	UpstreamOpenRouterChatURL        string   // UPSTREAM_LLM_OPENROUTER_URL
	UpstreamOpenRouterChatAuthBearer string   // UPSTREAM_LLM_OPENROUTER_AUTH_BEARER
	UpstreamOpenRouterProviderOrder  []string // UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER (CSV; default ["novita"] — D-C1 amendment per 03-WAVE0-GATES.md, Fireworks does not serve Qwen 3 family on OpenRouter as of 2026-04-20)
	UpstreamOpenRouterAllowFallbacks bool     // UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS (default false)
	UpstreamOpenAIWhisperURL         string   // UPSTREAM_STT_OPENAI_URL
	UpstreamOpenAIWhisperAuthBearer  string   // UPSTREAM_STT_OPENAI_AUTH_BEARER
	UpstreamOpenAIEmbedURL           string   // UPSTREAM_EMBED_OPENAI_URL
	UpstreamOpenAIEmbedAuthBearer    string   // UPSTREAM_EMBED_OPENAI_AUTH_BEARER

	// Phase 11.2 — STT tier-1 cascade slots (D-B7 + D-B8). Slot-named so the
	// operator can swap provider without renaming envs. Current assignment:
	//   slot 1 = Gemini 2.5 Flash Lite (https://generativelanguage.googleapis.com/v1beta)
	//   slot 2 = Groq Whisper-large-v3 (https://api.groq.com/openai/v1, OpenAI-compat)
	// AuthBearer fields tagged `json:"-"` per T-11.2-CFG (Information Disclosure
	// mitigation; PATTERNS Shared Pattern §Secret Hygiene).
	UpstreamSTTFallback1URL        string `json:"upstream_stt_fallback_1_url"`   // UPSTREAM_STT_FALLBACK_1_URL  (default Gemini v1beta)
	UpstreamSTTFallback1AuthBearer string `json:"-"`                             // UPSTREAM_STT_FALLBACK_1_AUTH_BEARER (Gemini API key; never log)
	UpstreamSTTFallback1Model      string `json:"upstream_stt_fallback_1_model"` // UPSTREAM_STT_FALLBACK_1_MODEL (default gemini-2.5-flash-lite)
	UpstreamSTTFallback2URL        string `json:"upstream_stt_fallback_2_url"`   // UPSTREAM_STT_FALLBACK_2_URL  (default Groq /openai/v1)
	UpstreamSTTFallback2AuthBearer string `json:"-"`                             // UPSTREAM_STT_FALLBACK_2_AUTH_BEARER (Groq API key; never log)
	UpstreamSTTFallback2Model      string `json:"upstream_stt_fallback_2_model"` // UPSTREAM_STT_FALLBACK_2_MODEL (default whisper-large-v3)

	// Phase 3 — Probe + breaker tuning (CONTEXT.md D-A2 + D-A3)
	ProbeIntervalSeconds       int // PROBE_INTERVAL_SECONDS (default 10)
	ProbeBudgetSeconds         int // PROBE_BUDGET_SECONDS (default 5)
	BreakerConsecutiveFailures int // BREAKER_CONSECUTIVE_FAILURES (default 3)
	BreakerCooldownSeconds     int // BREAKER_COOLDOWN_SECONDS (default 30)

	// Phase 5 — saturation-aware shedding runtime tuning (CONTEXT D-A3 /
	// Claude's Discretion). All five vars are optional; empty DCGMExporterURL
	// disables the VRAM signal so the 2-of-3 composite reduces to a 1-of-2
	// majority over inflight+P95 until the pod exporter is reachable.
	DCGMExporterURL          string // DCGM_EXPORTER_URL — pod :9400/metrics URL; empty = VRAM signal disabled
	ShedLatencyRingSize      int    // SHED_LATENCY_RING_SIZE (default 200) — samples retained per-upstream latency ring
	ShedTickIntervalMs       int    // SHED_TICK_INTERVAL_MS (default 1000) — FSM evaluation cadence
	ShedDcgmScrapeIntervalMs int    // SHED_DCGM_SCRAPE_INTERVAL_MS (default 5000) — pod VRAM poll cadence
	ShedDcgmTimeoutMs        int    // SHED_DCGM_TIMEOUT_MS (default 2000) — HTTP client timeout for scrape

	// Phase 3 — Per-route WriteTimeout (folded todo from Phase 2; D-A1 Plumbing).
	// Replaces the single WriteTimeout=0 default; chat MUST stay 0 for SSE
	// but non-streaming routes get slow-client-DoS defense.
	WriteTimeoutChat  time.Duration // WRITE_TIMEOUT_CHAT_SECONDS  (default 0 — unlimited for SSE)
	WriteTimeoutEmbed time.Duration // WRITE_TIMEOUT_EMBED_SECONDS (default 30s)
	WriteTimeoutAudio time.Duration // WRITE_TIMEOUT_AUDIO_SECONDS (default 120s; Whisper long multipart)

	// Observability
	SentryDSN string // SENTRY_DSN (optional; empty = Sentry disabled)
	LogLevel  string // LOG_LEVEL (default info)
	Env       string // ENV (default production)

	// Admin / bootstrap
	BootstrapTenantSlug string // BOOTSTRAP_TENANT_SLUG (default converseai)

	// Phase 4 — admin endpoint bootstrap (D-D3).
	// Optional: if empty at first boot, migration 0014 generates a random
	// key and logs a WARN so the operator can capture it from the logs.
	// Production deploys should set this in the Portainer stack BEFORE
	// pushing the Phase 4 image.
	AdminKeyBootstrap string // AI_GATEWAY_ADMIN_KEY_BOOTSTRAP (required=false, default="")

	// Phase 4 — rate-limit + quota fail policy (D-A2). Rate-limit defaults
	// to fail-open (preserve "failover invisible" core value during Redis
	// incidents); quota defaults to fail-closed (stopping is better than
	// burning OpenRouter/OpenAI cost without visibility).
	RateLimitFailOpen bool // AI_GATEWAY_RATE_LIMIT_FAIL_OPEN (default true)
	QuotaFailOpen     bool // AI_GATEWAY_QUOTA_FAIL_OPEN      (default false)

	// Phase 4 — pricing fallback (D-B3). Used when fx_rates has no live
	// row for USD/BRL. Operator updates weekly via `gatewayctl prices set-fx`.
	USDBRLDefault float64 // AI_GATEWAY_USD_BRL_RATE_DEFAULT (default 5.10)

	// Phase 4 — per-route WriteTimeout in integer seconds (folded TODO
	// from Phase 3). Separate from the Phase 3 time.Duration fields so the
	// TODO is truly resolved at the Phase 4 layer (the HTTP wiring in
	// Plan 04-06 will prefer these fields). chat=0 keeps SSE unlimited;
	// embed=30s and audio=120s restore slow-client-DoS defense on
	// non-streaming routes now justified by rate-limiting.
	WriteTimeoutChatS  int // GATEWAY_WRITE_TIMEOUT_CHAT_S  (default 0 — unlimited for SSE)
	WriteTimeoutEmbedS int // GATEWAY_WRITE_TIMEOUT_EMBED_S (default 30)
	WriteTimeoutAudioS int // GATEWAY_WRITE_TIMEOUT_AUDIO_S (default 120; Whisper multipart)

	// Phase 6 — emergency-pod auto-provisioning (Vast.ai). All fifteen
	// fields are read at boot; defaults match CONTEXT.md decisions
	// D-A1..D-D4 + Strategy B Locked (D-01-B..D-08-B) + 06-WAVE0-GATES.md.
	// VastAIAPIKey empty does NOT fail boot — the reconciler logs a
	// warning and stays disabled (graceful degrade so a missing dev
	// secret does not block the rest of the gateway from serving
	// traffic).
	//
	// Strategy B Locked per CONTEXT.md D-01-B..D-08-B + 06-WAVE0-GATES.md
	// Decisions 2 & 3: emergency pod uses upstream llama.cpp server image
	// (no custom GHCR build), fetches Jinja chat template from MinIO at
	// onstart with sha256 integrity check (B2 default), and uses a
	// hardcoded llama-server args slice in lifecycle.go (EmergencyLlamaArgs
	// empty CSV → nil → const default).
	EmergencyTemplateImage            string   // EMERGENCY_TEMPLATE_IMAGE (default "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"; CONTEXT.md D-01-B + 06-WAVE0-GATES.md Decision 3; tag SHA-pinned by ggml-org build process — operator can pin harder via @sha256:... per RESEARCH.md security domain)
	EmergencyJinjaTemplateKey         string   // EMERGENCY_JINJA_TEMPLATE_KEY (MinIO object key; D-04-B option B2 LOCKED per 06-WAVE0-GATES.md Decision 2; default points at production qwen3.5-27b-tool-calling Jinja — empty would represent B1 image-overlay path, not selected)
	EmergencyJinjaTemplateSHA256      string   // EMERGENCY_JINJA_TEMPLATE_SHA256 (hex sha256; D-04-B B2 LOCKED; default matches EmergencyJinjaTemplateKey; sha256 is public-grade integrity check, not secret — safe to log)
	EmergencyLlamaArgs                []string // EMERGENCY_LLAMA_ARGS (CSV; D-07-B; empty → nil → lifecycle.go uses hardcoded const; override only if production needs different llama-server flags)
	PodDebugSSHPublicKey              string   // POD_DEBUG_SSH_PUBLIC_KEY (optional; when non-empty, onstart installs+starts sshd inside the pod with this key in /root/.ssh/authorized_keys and Vast maps -p 22:22/tcp; intended for operator debug during UAT; leave empty in production for least-privilege)
	MonthlyEmergencyBudgetBRL         float64  // MONTHLY_EMERGENCY_BUDGET_BRL (default 200.0; D-D2 — Sentry alert only, no auto-block)
	PrimaryHostID                     int64    // PRIMARY_HOST_ID (default 0 = unknown; D-A2 host_id != filter only applied if known)
	ProvisionColdStartBudgetSeconds   int      // PROVISION_COLDSTART_BUDGET_SECONDS (default 600; D-A4 — /health poll budget)
	ProvisionFailureCooldownSeconds   int      // PROVISION_FAILURE_COOLDOWN_SECONDS (default 60 — after a provisioning failure e.g. offer_race_lost, hold the FSM in Cooldown this long before re-arming the trigger; MUST exceed one 2+4+8≈14s attempt cycle so a single failed cycle actually backs off instead of hammer-looping the Vast.ai spot market)
	ProvisionHealthyDurationSeconds   int      // PROVISION_HEALTHY_DURATION_SECONDS (default 300; D-D1 — primary must be healthy this long before cutback)
	ProvisionIdleGraceSeconds         int      // PROVISION_IDLE_GRACE_SECONDS (default 300; D-D1 — emergency pod idle grace before destroy)
	ProvisionTriggerFailedOverSeconds int      // PROVISION_TRIGGER_FAILED_OVER_SECONDS (default 120; D-C1 — local-llm OPEN must persist this long)
	USDToBRLRate                      float64  // USD_TO_BRL_RATE (default 5.0; D-D4 — operator updates quarterly for cost audit)
	VastAIAPIKey                      string   // VAST_AI_API_KEY (D-A5; empty = Phase 6 disabled with warning, NOT fail-loud)
	VastAPIQPSLimit                   int      // VAST_API_QPS_LIMIT (default 1; RESEARCH Open Question 12 — conservative 1 req/s token bucket)
	VastPriceCapDPH                   float64  // VAST_PRICE_CAP_DPH (default 0.40; RTX 4090 cap; epsilon comparison cap+0.0001 per Pitfall 5)
	VastGPUName                       string   // VAST_GPU_NAME (default "RTX 4090"; emerg pod GPU model — kept cheap because emerg is LLM-only fallback)

	// Phase 6.6 — primary pod auto-provisioning (schedule-driven; D-08 +
	// Decisions Resolved #5 + WAVE0-GATES.md Decisions 1+3+5+6 + Reviews
	// 2026-05-17 #6 #8). All 24 fields are read at boot; defaults match
	// WAVE0-GATES.md operator-locked decisions:
	//   - Decision 1: 4 upstream OCI images SHA-pinned (llama.cpp b9191,
	//     speaches 0.9.0-rc.3, infinity 0.0.77, dcgm-exporter 4.5.3).
	//     b9191 UPGRADED from Phase 6's prior build (Qwen3.6 SSM tensor
	//     support; 06.6-SPIKE-qwen3.6-jinja.md Round 3 empirical validation).
	//   - Decision 3: B1 GGUF-embedded Jinja LOCKED — PrimaryQwenJinjaKey
	//     + PrimaryQwenJinjaSHA256 default empty; `--jinja` flag alone
	//     extracts PEG-native parser from Qwen3.6 GGUF chat_template.
	//   - Decision 5: PrimaryPodScheduleDisabled default `true` (soak gate;
	//     operator manual flip to false after Plan 06.6-11 Live UAT GREEN).
	//   - Decision 6: PrimaryProvisionColdStartBudgetSeconds default 2400
	//     (40min generous margin for multi-stage image pull + aria2c
	//     weight download + 4-service supervisord startup).
	// NO docker-in-docker env vars — Wave 0 spike REJECTED nested-runtime
	// orchestration; supervisord is PID 1 inside the custom multi-stage
	// image (Plan 06.6-04 pod/primary/Dockerfile + supervisord.conf —
	// implementation detail, not user-facing env).
	PrimaryTemplateImage     string   // PRIMARY_TEMPLATE_IMAGE (default llama.cpp:server-cuda-b9191 SHA-pinned; WAVE0-GATES Decision 1; Plan 06.6-04 multi-stage FROM base)
	PrimaryInfinityImage     string   // PRIMARY_INFINITY_IMAGE (default infinity:0.0.77 SHA-pinned; WAVE0-GATES Decision 1; Plan 06.6-04 multi-stage source)
	PrimaryDCGMImage         string   // PRIMARY_DCGM_IMAGE (default dcgm-exporter:4.5.3-4.8.2-distroless SHA-pinned; WAVE0-GATES Decision 1; Plan 06.6-04 multi-stage source)
	PrimaryQwenWeightsKey    string   // PRIMARY_QWEN_WEIGHTS_KEY (MinIO object key; default qwen3.6-27b-Q4_K_M/v1.0.0/model.gguf — CONTEXT.md D-04)
	PrimaryQwenWeightsSHA256 string   // PRIMARY_QWEN_WEIGHTS_SHA256 (Qwen3.6 sha256; default Wave 0 verified digest a7cbd3ec...; sha256 is public-grade integrity check, safe to log)
	PrimaryQwenJinjaKey      string   // PRIMARY_QWEN_JINJA_KEY (default empty; WAVE0-GATES Decision 3 B1 GGUF-embedded LOCKED — `--jinja` flag alone extracts PEG-native parser; override allowed for future B2 MinIO fallback)
	PrimaryQwenJinjaSHA256   string   // PRIMARY_QWEN_JINJA_SHA256 (default empty; WAVE0-GATES Decision 3 B1 embedded LOCKED)
	PrimaryLlamaArgs         []string // PRIMARY_LLAMA_ARGS (CSV; empty → nil → lifecycle.go uses hardcoded primaryLlamaArgsDefault const; default args MUST NOT include --chat-template-file per B1 embedded LOCKED)
	// Phase 11.2 D-B7 — Whisper weights RESTORED after 11.1 D-A4 revert.
	// FAIL-FAST: PrimaryWhisperWeightsSHA256 has no envOr default; empty
	// passthrough so buildPrimaryCreateRequest rejects at build time with
	// ErrMissingWhisperSHA. Operator MUST set this env before deploy.
	PrimaryWhisperWeightsKey    string // PRIMARY_WHISPER_WEIGHTS_KEY (MinIO; default whisper-large-v3/v1.0.0/model.tar.gz)
	PrimaryWhisperWeightsSHA256 string // PRIMARY_WHISPER_WEIGHTS_SHA256 (FAIL-FAST; reviews consensus action #6)
	// Phase 11.2 D-B1 LOCKED — speaches image pin (Phase 06.7/06.8 UAT-validated, HF_HUB_CACHE workaround known).
	PrimarySpeachesImage        string  // PRIMARY_SPEACHES_IMAGE (default ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3)
	PrimaryBGEM3WeightsKey      string  // PRIMARY_BGEM3_WEIGHTS_KEY (MinIO; default bge-m3/v1.0.0/model.tar.gz)
	PrimaryBGEM3WeightsSHA256   string  // PRIMARY_BGEM3_WEIGHTS_SHA256 (FAIL-FAST policy per reviews consensus action #6 — gateway refuses to build the create-instance payload if empty)
	PrimaryVastPriceCapDPH      float64 // PRIMARY_VAST_PRICE_CAP_DPH (DEPRECATED — historical default 2.20 sized for RTX 5090 EU running Qwen 27B + bge-m3 + KV cache + whisper-large-v3 on-pod; Phase 11.1 D-A4 dropped STT and Phase 11.1 D-A6 split caps into PRIMARY_VAST_PRICE_CAP_PRIMARY (0.30 / 1×3090) and PRIMARY_VAST_PRICE_CAP_FALLBACK (0.60 / 2×3090). Read-with-warn for backwards compat. TODO-remove-in-11.2.)
	PrimaryVastMachineBlocklist []int64 // PRIMARY_VAST_MACHINE_BLOCKLIST (comma-separated Vast machine_ids excluded from offer search; catalogs hosts that fail pod boot, e.g. multi-GPU machines with broken CDI on non-zero GPU slots)
	PrimaryVastMachineAllowlist []int64 // PRIMARY_VAST_MACHINE_ALLOWLIST (comma-separated Vast machine_ids PREFERRED in offer search; catalogs known-good hosts (CDI ok, reliability/price validated). PREFERENCE not guarantee: reconciler searches allowlist-only first, then broadens to the full qualified search when allowlisted hosts are unavailable. Vast is a spot marketplace — no machine can be reserved, so a hard filter would block provisioning whenever the host is busy; the broaden-fallback keeps the cheap-marketplace economics while still preferring trusted hosts. Empty = no preference (default))
	PrimaryGPUName              string  // PRIMARY_GPU_NAME (DEPRECATED-alias for PrimaryVastGPUNamePrimary; Phase 11.1 D-A6 split shape into primary "RTX 3090" + fallback "RTX 3090" — leaving legacy "RTX 5090" in .env would silently overshoot the new $0.30 primary cap and starve the search. Read-with-warn for backwards compat. TODO-remove-in-11.2.)
	PrimaryNumGPUs              int     // PRIMARY_NUM_GPUS (DEPRECATED-alias for PrimaryVastNumGPUsFallback; legacy var historically meant "GPUs per primary pod" which is now the FALLBACK shape (default 2 — 2×3090) per primary-gpu-shape-06.8-final memory. Read-with-warn for backwards compat. TODO-remove-in-11.2.)
	// Phase 11.1 D-A6 primary+fallback shape (split per Wave 0 EVIDENCE-00 —
	// 4090 @ $0.40 returned 0 EU offers, pivoted to 2×3090 @ $0.60 fallback
	// with 7 EU offers, cheapest $0.42/h). Same GPU model both shapes —
	// single CDI/driver matrix. Reconciler iterates [primary, fallback]
	// filters and breaks on the first non-empty offer list.
	PrimaryVastGPUNamePrimary              string   // PRIMARY_VAST_GPU_NAME_PRIMARY (default "RTX 3090"; primary shape — single 3090 ~$0.30/h, llama-only flux)
	PrimaryVastGPUNameFallback             string   // PRIMARY_VAST_GPU_NAME_FALLBACK (default "RTX 3090"; fallback shape — 2×3090 @ ~$0.60/h, deeper EU pool)
	PrimaryVastPriceCapPrimary             float64  // PRIMARY_VAST_PRICE_CAP_PRIMARY (default 0.30; single 3090 EU cap)
	PrimaryVastPriceCapFallback            float64  // PRIMARY_VAST_PRICE_CAP_FALLBACK (default 0.60; 2×3090 EU cap; EVIDENCE-00 found 7 EU offers within this cap)
	PrimaryVastNumGPUsPrimary              int      // PRIMARY_VAST_NUM_GPUS_PRIMARY (default 1; single-GPU primary)
	PrimaryVastNumGPUsFallback             int      // PRIMARY_VAST_NUM_GPUS_FALLBACK (default 2; 2×3090 single-pod, auto-tensor-split via llama.cpp -ngl 99)
	PrimaryProvisionColdStartBudgetSeconds int      // PRIMARY_PROVISION_COLDSTART_BUDGET_SECONDS (default 2400 = 40min; WAVE0-GATES Decision 6 — generous margin for slow inet hosts + multi-stage image pull + aria2c weight download + 4-service supervisord startup; reconciler treats >40min as provision failure)
	PrimaryProvisionFailureCooldownSeconds int      // PRIMARY_PROVISION_FAILURE_COOLDOWN_SECONDS (default 300 = 5min; mirror emerg's ProvisionFailureCooldownSeconds=60 scaled-up for schedule cadence; Plan 06.6-06a reconciler.evaluateAsleep enforces)
	MonthlyPrimaryBudgetBRL                float64  // MONTHLY_PRIMARY_BUDGET_BRL (default 800.0; Pitfall #12 separate from emergency budget — primary pod runs ~14h × 22 days × $0.40 ≈ R$130/mo, budget gives 5x headroom for soak phase)
	PrimaryPodScheduleTimezone             string   // PRIMARY_POD_SCHEDULE_TIMEZONE (default America/Sao_Paulo; D-08.1)
	PrimaryPodScheduleUpHour               int      // PRIMARY_POD_SCHEDULE_UP_HOUR (default 8; D-08.1 peak-hour start)
	PrimaryPodScheduleDownHour             int      // PRIMARY_POD_SCHEDULE_DOWN_HOUR (default 22; D-08.1 peak-hour end)
	PrimaryPodScheduleDays                 []string // PRIMARY_POD_SCHEDULE_DAYS (CSV; default mon,tue,wed,thu,fri; D-08.1 weekdays-only)
	PrimaryPodScheduleGraceRampDownSeconds int      // PRIMARY_POD_SCHEDULE_GRACE_RAMP_DOWN_SECONDS (default 300 = 5min drain inflight before destroy; D-08.1)
	PrimaryPodScheduleDisabled             bool     // PRIMARY_POD_SCHEDULE_DISABLED (default true per WAVE0-GATES Decision 5 soak gate — operator manual flip to false after Plan 06.6-11 Live UAT GREEN)
	PrimaryPodScheduleProvisionLeadSeconds int      // PRIMARY_POD_SCHEDULE_PROVISION_LEAD_SECONDS (default 1800 = 30min; reviews consensus action #8 — reconciler provisions lead_seconds before UpHour to honor schedule semantics with 25-30min cold-start reality; Plan 06.6-05 consumer)

	// Pod-side secrets forwarded to the Vast.ai emergency pod via CreateRequest.Env.
	// Mirror Phase 1 smoke.yml — pod onstart aborts without them. Sensitive; never log.
	MinioEndpoint     string // MINIO_ENDPOINT (e.g. https://s3.ifixtelecom.com.br)
	MinioBucket       string // MINIO_BUCKET (default "ai-gateway")
	MinioAccessKey    string // MINIO_ACCESS_KEY
	MinioSecretKey    string // MINIO_SECRET_KEY
	WeightsQwenKey    string // WEIGHTS_QWEN_KEY     (MinIO object path)
	WeightsQwenSHA256 string // WEIGHTS_QWEN_SHA256
	// Phase 11.1 D-A4: WeightsWhisper* removed — STT shrunk to tier-1-only.
	WeightsBGEM3Key    string // WEIGHTS_BGE_M3_KEY
	WeightsBGEM3SHA256 string // WEIGHTS_BGE_M3_SHA256

	// Phase 7 — alerting (all optional; empty = channel disabled with WARN).
	// Mirrors the SentryDSN precedent: an unset alert var NEVER fails boot.
	// The downstream alert clients (plans 07-04/07-05) log a single WARN per
	// disabled channel at startup; config.go itself does not log and these
	// fields are never added to the required-env validation slice. Credentials
	// are plain strings here and MUST NOT be logged (threat T-07-01).
	ChatwootAPIURL          string   // CHATWOOT_API_URL
	ChatwootAPIToken        string   // CHATWOOT_API_TOKEN
	ChatwootOncallAccountID string   // CHATWOOT_ONCALL_ACCOUNT_ID
	ChatwootOncallInboxID   string   // CHATWOOT_ONCALL_INBOX_ID
	ChatwootOncallContactID string   // CHATWOOT_ONCALL_CONTACT_ID
	ClickUpAPIToken         string   // CLICKUP_API_TOKEN
	ClickUpAlertListID      string   // CLICKUP_ALERT_LIST_ID
	BrevoSMTPHost           string   // BREVO_SMTP_HOST
	BrevoSMTPPort           int      // BREVO_SMTP_PORT (default 587)
	BrevoSMTPUser           string   // BREVO_SMTP_USER
	BrevoSMTPPass           string   // BREVO_SMTP_PASS
	AlertEmailTo            []string // ALERT_EMAIL_TO (CSV; empty default = email channel disabled)
	AlertEmailFrom          string   // ALERT_EMAIL_FROM
}

// ErrMissingEnv is returned by Load when one or more required env vars are unset.
var ErrMissingEnv = errors.New("config: required environment variable not set")

// ErrInvalidURLSuffix is returned by Load when a tier-1 external upstream URL
// env var (UPSTREAM_{LLM_OPENROUTER,STT_OPENAI,EMBED_OPENAI}_URL) ends in
// `/v1`. Phase 06.9 D-07 fail-fast: the gateway preserves the inbound
// `/v1/<route>` path on forward (BuildDirector intentionally leaves
// r.URL.Path = /v1/chat/completions etc. unchanged so pod and gateway routes
// mirror 1:1), so concatenation with a URL ending in `/v1` produces a
// double-/v1 path (HTTP 404 on the upstream). This bug silently masked
// OR-FIX for months; the validation makes the misconfiguration crash boot
// with an operator-actionable error rather than ship as a runtime 404.
var ErrInvalidURLSuffix = errors.New("config: external upstream URL must not end with /v1")

// upstreamModelEnvVarMap is the curated Phase 06.9 D-06 env-override
// observability mapping. Mirrors models.upstreamEnvVarMap in the models
// package (which owns the runtime resolver precedence) — kept as a local
// copy here to avoid an import cycle (config is imported by virtually every
// package, including models). Each entry maps an upstream NAME to the env
// var operators may set to override that upstream's schema-row target.
//
// New tier-1 providers MUST add an entry here AND keep models.upstreamEnvVarMap
// in sync — otherwise the boot-time observability log silently omits the new
// provider while the resolver does honor it (or vice versa).
//
// Per D-06 / BLOCKER-1: env-overrides are SUPPORTED operator escape hatches
// (multi-instance: gatewayctl model-alias set; per-instance: this env var).
// They are NOT deprecated. Phase 06.9 Plan 04 replaces an earlier
// deprecation-WARN draft with this presence-only INFO observability.
var upstreamModelEnvVarMap = map[string]string{
	"openrouter-chat": "UPSTREAM_LLM_OPENROUTER_MODEL",
	"openai-whisper":  "UPSTREAM_STT_OPENAI_MODEL",
	"openai-embed":    "UPSTREAM_EMBED_OPENAI_MODEL",
}

// upstreamURLEnvVarMap is the Phase 06.9 D-07 fail-fast mapping from env
// var name to its accessor over the *Config that just loaded. Iterating in
// a fixed slice order keeps error-message ordering deterministic across
// runs — operators see the same comma-joined list shape each time and
// tests assert against a stable string. Adding a new tier-1 URL gate MUST
// register its env-name + accessor here.
type upstreamURLCheck struct {
	envName string
	url     string
}

// Load reads env vars into a Config. Returns an error listing any missing
// required variables. Callers should os.Exit(2) after logging the error.
func Load() (Config, error) {
	cfg := Config{
		Port:              atoiOr(os.Getenv("GATEWAY_PORT"), 8080),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,        // 1 MiB
		MaxBodyBytes:      25 * (1 << 20), // 25 MiB

		PGDSN:          os.Getenv("AI_GATEWAY_PG_DSN"),
		PGMaxConns:     int32(atoiOr(os.Getenv("AI_GATEWAY_PG_MAX_CONNS"), 10)),
		RedisAddr:      os.Getenv("AI_GATEWAY_REDIS_ADDR"),
		RedisPassword:  os.Getenv("AI_GATEWAY_REDIS_PASSWORD"),
		RedisDB:        atoiOr(os.Getenv("AI_GATEWAY_REDIS_DB"), 0),
		RedisKeyPrefix: "gw:",

		UpstreamLLMURL:          os.Getenv("UPSTREAM_LLM_URL"),
		UpstreamSTTURL:          os.Getenv("UPSTREAM_STT_URL"),
		UpstreamEmbedURL:        os.Getenv("UPSTREAM_EMBED_URL"),
		UpstreamHealthBridgeURL: os.Getenv("UPSTREAM_HEALTH_BRIDGE_URL"),

		// Phase 06.7 — TTS + voice-clone surface (Plan 07).
		UpstreamTTSURL:      os.Getenv("UPSTREAM_TTS_URL"),
		UpstreamTTSPiperURL: os.Getenv("UPSTREAM_TTS_PIPER_URL"),
		TTSMaxInputChars:    atoiOr(os.Getenv("TTS_MAX_INPUT_CHARS"), 4000),
		VoiceMaxUploadBytes: atoi64Or(os.Getenv("VOICE_MAX_UPLOAD_BYTES"), 10485760),
		S3VoicePrefix:       strOr(os.Getenv("S3_VOICE_PREFIX"), "voices"),

		// Phase 3 external upstreams (optional at boot)
		UpstreamOpenRouterChatURL:        os.Getenv("UPSTREAM_LLM_OPENROUTER_URL"),
		UpstreamOpenRouterChatAuthBearer: os.Getenv("UPSTREAM_LLM_OPENROUTER_AUTH_BEARER"),
		UpstreamOpenRouterProviderOrder:  csvOr(os.Getenv("UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER"), []string{"novita"}),
		UpstreamOpenRouterAllowFallbacks: boolOr(os.Getenv("UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS"), false),
		UpstreamOpenAIWhisperURL:         os.Getenv("UPSTREAM_STT_OPENAI_URL"),
		UpstreamOpenAIWhisperAuthBearer:  os.Getenv("UPSTREAM_STT_OPENAI_AUTH_BEARER"),
		UpstreamOpenAIEmbedURL:           os.Getenv("UPSTREAM_EMBED_OPENAI_URL"),
		UpstreamOpenAIEmbedAuthBearer:    os.Getenv("UPSTREAM_EMBED_OPENAI_AUTH_BEARER"),

		// Phase 11.2 D-B7 + D-B8 — STT tier-1 cascade slots (slot 1 = Gemini,
		// slot 2 = Groq Whisper). Bearer fields read straight from env (no
		// envOr) so empty stays empty — resolver.upstreamEnvVarMap and the
		// dispatcher honor empty as "slot disabled" without logging the value.
		UpstreamSTTFallback1URL:        envOr("UPSTREAM_STT_FALLBACK_1_URL", "https://generativelanguage.googleapis.com/v1beta"),
		UpstreamSTTFallback1AuthBearer: os.Getenv("UPSTREAM_STT_FALLBACK_1_AUTH_BEARER"),
		UpstreamSTTFallback1Model:      envOr("UPSTREAM_STT_FALLBACK_1_MODEL", "gemini-2.5-flash-lite"),
		UpstreamSTTFallback2URL:        envOr("UPSTREAM_STT_FALLBACK_2_URL", "https://api.groq.com/openai"),
		UpstreamSTTFallback2AuthBearer: os.Getenv("UPSTREAM_STT_FALLBACK_2_AUTH_BEARER"),
		UpstreamSTTFallback2Model:      envOr("UPSTREAM_STT_FALLBACK_2_MODEL", "whisper-large-v3"),

		ProbeIntervalSeconds:       atoiOr(os.Getenv("PROBE_INTERVAL_SECONDS"), 10),
		ProbeBudgetSeconds:         atoiOr(os.Getenv("PROBE_BUDGET_SECONDS"), 5),
		BreakerConsecutiveFailures: atoiOr(os.Getenv("BREAKER_CONSECUTIVE_FAILURES"), 3),
		BreakerCooldownSeconds:     atoiOr(os.Getenv("BREAKER_COOLDOWN_SECONDS"), 30),

		// Phase 5 — shedding (CONTEXT.md D-A3). All five opt-in; no boot dep.
		DCGMExporterURL:          envOr("DCGM_EXPORTER_URL", ""),
		ShedLatencyRingSize:      atoiOr(os.Getenv("SHED_LATENCY_RING_SIZE"), 200),
		ShedTickIntervalMs:       atoiOr(os.Getenv("SHED_TICK_INTERVAL_MS"), 1000),
		ShedDcgmScrapeIntervalMs: atoiOr(os.Getenv("SHED_DCGM_SCRAPE_INTERVAL_MS"), 5000),
		ShedDcgmTimeoutMs:        atoiOr(os.Getenv("SHED_DCGM_TIMEOUT_MS"), 2000),

		WriteTimeoutChat:  time.Duration(atoiOr(os.Getenv("WRITE_TIMEOUT_CHAT_SECONDS"), 0)) * time.Second,
		WriteTimeoutEmbed: time.Duration(atoiOr(os.Getenv("WRITE_TIMEOUT_EMBED_SECONDS"), 30)) * time.Second,
		WriteTimeoutAudio: time.Duration(atoiOr(os.Getenv("WRITE_TIMEOUT_AUDIO_SECONDS"), 120)) * time.Second,

		SentryDSN: os.Getenv("SENTRY_DSN"),
		LogLevel:  envOr("LOG_LEVEL", "info"),
		Env:       envOr("ENV", "production"),

		BootstrapTenantSlug: envOr("BOOTSTRAP_TENANT_SLUG", "converseai"),

		// Phase 4 — admin bootstrap, fail policy, fx default, per-route write timeouts.
		AdminKeyBootstrap:  envOr("AI_GATEWAY_ADMIN_KEY_BOOTSTRAP", ""),
		RateLimitFailOpen:  boolOr(os.Getenv("AI_GATEWAY_RATE_LIMIT_FAIL_OPEN"), true),
		QuotaFailOpen:      boolOr(os.Getenv("AI_GATEWAY_QUOTA_FAIL_OPEN"), false),
		USDBRLDefault:      floatOr(os.Getenv("AI_GATEWAY_USD_BRL_RATE_DEFAULT"), 5.10),
		WriteTimeoutChatS:  atoiOr(os.Getenv("GATEWAY_WRITE_TIMEOUT_CHAT_S"), 0),
		WriteTimeoutEmbedS: atoiOr(os.Getenv("GATEWAY_WRITE_TIMEOUT_EMBED_S"), 30),
		WriteTimeoutAudioS: atoiOr(os.Getenv("GATEWAY_WRITE_TIMEOUT_AUDIO_S"), 120),

		// Phase 6 — emergency pod (CONTEXT.md D-A1..D-D4 + Strategy B
		// Locked D-01-B..D-08-B + 06-WAVE0-GATES.md). All defaults
		// conservative. Operator confirms production values via
		// 06-WAVE0-GATES.md before Phase 6 LIVE UAT.
		//
		// Strategy B Locked defaults (06-WAVE0-GATES.md Decisions 2 & 3):
		// EmergencyTemplateImage     = upstream llama.cpp server build b9128 (CUDA)
		// EmergencyJinjaTemplateKey  = production qwen3.5-27b Jinja path (B2 LOCKED non-empty)
		// EmergencyJinjaTemplateSHA  = sha256 of the Jinja above (B2 LOCKED non-empty)
		// EmergencyLlamaArgs         = nil (empty CSV; lifecycle.go uses hardcoded const)
		EmergencyTemplateImage:            envOr("EMERGENCY_TEMPLATE_IMAGE", "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"),
		EmergencyJinjaTemplateKey:         envOr("EMERGENCY_JINJA_TEMPLATE_KEY", "emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja"),
		EmergencyJinjaTemplateSHA256:      envOr("EMERGENCY_JINJA_TEMPLATE_SHA256", "1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67"),
		EmergencyLlamaArgs:                csvOr(os.Getenv("EMERGENCY_LLAMA_ARGS"), nil),
		PodDebugSSHPublicKey:              envOr("POD_DEBUG_SSH_PUBLIC_KEY", ""),
		MonthlyEmergencyBudgetBRL:         floatOr(os.Getenv("MONTHLY_EMERGENCY_BUDGET_BRL"), 200.0),
		PrimaryHostID:                     int64(atoiOr(os.Getenv("PRIMARY_HOST_ID"), 0)),
		ProvisionColdStartBudgetSeconds:   atoiOr(os.Getenv("PROVISION_COLDSTART_BUDGET_SECONDS"), 600),
		ProvisionFailureCooldownSeconds:   atoiOr(os.Getenv("PROVISION_FAILURE_COOLDOWN_SECONDS"), 60),
		ProvisionHealthyDurationSeconds:   atoiOr(os.Getenv("PROVISION_HEALTHY_DURATION_SECONDS"), 300),
		ProvisionIdleGraceSeconds:         atoiOr(os.Getenv("PROVISION_IDLE_GRACE_SECONDS"), 300),
		ProvisionTriggerFailedOverSeconds: atoiOr(os.Getenv("PROVISION_TRIGGER_FAILED_OVER_SECONDS"), 120),
		USDToBRLRate:                      floatOr(os.Getenv("USD_TO_BRL_RATE"), 5.0),
		VastAIAPIKey:                      os.Getenv("VAST_AI_API_KEY"),
		VastAPIQPSLimit:                   atoiOr(os.Getenv("VAST_API_QPS_LIMIT"), 1),
		VastPriceCapDPH:                   floatOr(os.Getenv("VAST_PRICE_CAP_DPH"), 0.40),
		VastGPUName:                       envOr("VAST_GPU_NAME", "RTX 4090"),

		// Phase 6.6 — primary pod auto-provisioning (schedule-driven).
		// Defaults match WAVE0-GATES.md operator-locked Decisions 1+3+5+6
		// + Reviews 2026-05-17 #6 #8. See struct doc above for full
		// rationale. NO DinD env vars added — Wave 0 REJECTED DinD;
		// supervisord lives inside Plan 06.6-04 custom multi-stage image.
		PrimaryTemplateImage:     envOr("PRIMARY_TEMPLATE_IMAGE", "ghcr.io/ggml-org/llama.cpp:server-cuda-b9191@sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3"),
		PrimaryInfinityImage:     envOr("PRIMARY_INFINITY_IMAGE", "michaelf34/infinity:0.0.77@sha256:11e8b3921b9f1a58965afaad4a844c435c9807cbc82c51e47cb147b7d977fc88"),
		PrimaryDCGMImage:         envOr("PRIMARY_DCGM_IMAGE", "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless@sha256:60d3b00ac80b4ae77f94dae2f943685605585ad9e92fdccda3154d009ae317cc"),
		PrimaryQwenWeightsKey:    envOr("PRIMARY_QWEN_WEIGHTS_KEY", "qwen3.6-27b-Q4_K_M/v1.0.0/model.gguf"),
		PrimaryQwenWeightsSHA256: envOr("PRIMARY_QWEN_WEIGHTS_SHA256", "a7cbd3ecc0e3f9b333edee61ae66bc87ed713c5d49587a8355814722ed329e0f"),
		// default empty per WAVE0-GATES Decision 3 — B1 GGUF-embedded Jinja LOCKED; --jinja flag alone extracts PEG-native parser from Qwen3.6 GGUF chat_template; override via env if B2 MinIO fallback needed.
		PrimaryQwenJinjaKey:    envOr("PRIMARY_QWEN_JINJA_KEY", ""),
		PrimaryQwenJinjaSHA256: envOr("PRIMARY_QWEN_JINJA_SHA256", ""),
		PrimaryLlamaArgs:       csvOr(os.Getenv("PRIMARY_LLAMA_ARGS"), nil),
		// Phase 11.2 D-B7 — Whisper weights restored. FAIL-FAST SHA per reviews
		// consensus action #6: no envOr default; empty passthrough so
		// buildPrimaryCreateRequest rejects at build time with ErrMissingWhisperSHA.
		PrimaryWhisperWeightsKey:    envOr("PRIMARY_WHISPER_WEIGHTS_KEY", "whisper-large-v3/v1.0.0/model.tar.gz"),
		PrimaryWhisperWeightsSHA256: os.Getenv("PRIMARY_WHISPER_WEIGHTS_SHA256"),
		// Phase 11.2 D-B1 LOCKED — speaches image pin.
		PrimarySpeachesImage:   envOr("PRIMARY_SPEACHES_IMAGE", "ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3"),
		PrimaryBGEM3WeightsKey: envOr("PRIMARY_BGEM3_WEIGHTS_KEY", "bge-m3/v1.0.0/model.tar.gz"),
		// FAIL-FAST policy per reviews consensus action #6 — no envOr default; empty passthrough so buildPrimaryCreateRequest rejects at build time.
		PrimaryBGEM3WeightsSHA256:   os.Getenv("PRIMARY_BGEM3_WEIGHTS_SHA256"),
		PrimaryVastPriceCapDPH:      floatOr(os.Getenv("PRIMARY_VAST_PRICE_CAP_DPH"), 2.20),
		PrimaryVastMachineBlocklist: parseInt64CSV(os.Getenv("PRIMARY_VAST_MACHINE_BLOCKLIST")),
		PrimaryVastMachineAllowlist: parseInt64CSV(os.Getenv("PRIMARY_VAST_MACHINE_ALLOWLIST")),
		PrimaryGPUName:              envOr("PRIMARY_GPU_NAME", "RTX 5090"),
		PrimaryNumGPUs:              atoiOr(os.Getenv("PRIMARY_NUM_GPUS"), 1),
		// Phase 11.1 D-A6 primary+fallback shape (Wave 0 EVIDENCE-00).
		// Defaults: 1×RTX 3090 @ $0.30 primary; 2×RTX 3090 @ $0.60 fallback.
		PrimaryVastGPUNamePrimary:              envOr("PRIMARY_VAST_GPU_NAME_PRIMARY", "RTX 3090"),
		PrimaryVastGPUNameFallback:             envOr("PRIMARY_VAST_GPU_NAME_FALLBACK", "RTX 3090"),
		PrimaryVastPriceCapPrimary:             floatOr(os.Getenv("PRIMARY_VAST_PRICE_CAP_PRIMARY"), 0.30),
		PrimaryVastPriceCapFallback:            floatOr(os.Getenv("PRIMARY_VAST_PRICE_CAP_FALLBACK"), 0.60),
		PrimaryVastNumGPUsPrimary:              atoiOr(os.Getenv("PRIMARY_VAST_NUM_GPUS_PRIMARY"), 1),
		PrimaryVastNumGPUsFallback:             atoiOr(os.Getenv("PRIMARY_VAST_NUM_GPUS_FALLBACK"), 2),
		PrimaryProvisionColdStartBudgetSeconds: atoiOr(os.Getenv("PRIMARY_PROVISION_COLDSTART_BUDGET_SECONDS"), 2400),
		PrimaryProvisionFailureCooldownSeconds: atoiOr(os.Getenv("PRIMARY_PROVISION_FAILURE_COOLDOWN_SECONDS"), 300),
		MonthlyPrimaryBudgetBRL:                floatOr(os.Getenv("MONTHLY_PRIMARY_BUDGET_BRL"), 800.0),
		PrimaryPodScheduleTimezone:             envOr("PRIMARY_POD_SCHEDULE_TIMEZONE", "America/Sao_Paulo"),
		PrimaryPodScheduleUpHour:               atoiOr(os.Getenv("PRIMARY_POD_SCHEDULE_UP_HOUR"), 8),
		PrimaryPodScheduleDownHour:             atoiOr(os.Getenv("PRIMARY_POD_SCHEDULE_DOWN_HOUR"), 22),
		PrimaryPodScheduleDays:                 csvOr(os.Getenv("PRIMARY_POD_SCHEDULE_DAYS"), []string{"mon", "tue", "wed", "thu", "fri"}),
		PrimaryPodScheduleGraceRampDownSeconds: atoiOr(os.Getenv("PRIMARY_POD_SCHEDULE_GRACE_RAMP_DOWN_SECONDS"), 300),
		// default true per WAVE0-GATES Decision 5 soak gate — operator manual flip to false after Plan 06.6-11 Live UAT GREEN.
		PrimaryPodScheduleDisabled: boolOr(os.Getenv("PRIMARY_POD_SCHEDULE_DISABLED"), true),
		// default 1800 (30min pre-warm offset) per reviews consensus action #8 — reconciler provisions lead_seconds before UpHour to honor schedule semantics with 25-30min cold-start reality.
		PrimaryPodScheduleProvisionLeadSeconds: atoiOr(os.Getenv("PRIMARY_POD_SCHEDULE_PROVISION_LEAD_SECONDS"), 1800),

		MinioEndpoint:     envOr("MINIO_ENDPOINT", "https://s3.ifixtelecom.com.br"),
		MinioBucket:       envOr("MINIO_BUCKET", "ai-gateway"),
		MinioAccessKey:    os.Getenv("MINIO_ACCESS_KEY"),
		MinioSecretKey:    os.Getenv("MINIO_SECRET_KEY"),
		WeightsQwenKey:    envOr("WEIGHTS_QWEN_KEY", "qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf"),
		WeightsQwenSHA256: os.Getenv("WEIGHTS_QWEN_SHA256"),
		// Phase 11.1 D-A4: WEIGHTS_WHISPER_* removed (STT shrunk to tier-1-only).
		WeightsBGEM3Key:    envOr("WEIGHTS_BGE_M3_KEY", "bge-m3/v1.0.0/model.tar.gz"),
		WeightsBGEM3SHA256: os.Getenv("WEIGHTS_BGE_M3_SHA256"),

		// Phase 7 — alerting. All optional; not added to requiredOrder below.
		ChatwootAPIURL:          os.Getenv("CHATWOOT_API_URL"),
		ChatwootAPIToken:        os.Getenv("CHATWOOT_API_TOKEN"),
		ChatwootOncallAccountID: os.Getenv("CHATWOOT_ONCALL_ACCOUNT_ID"),
		ChatwootOncallInboxID:   os.Getenv("CHATWOOT_ONCALL_INBOX_ID"),
		ChatwootOncallContactID: os.Getenv("CHATWOOT_ONCALL_CONTACT_ID"),
		ClickUpAPIToken:         os.Getenv("CLICKUP_API_TOKEN"),
		ClickUpAlertListID:      os.Getenv("CLICKUP_ALERT_LIST_ID"),
		BrevoSMTPHost:           os.Getenv("BREVO_SMTP_HOST"),
		BrevoSMTPPort:           atoiOr(os.Getenv("BREVO_SMTP_PORT"), 587),
		BrevoSMTPUser:           os.Getenv("BREVO_SMTP_USER"),
		BrevoSMTPPass:           os.Getenv("BREVO_SMTP_PASS"),
		AlertEmailTo:            csvOr(os.Getenv("ALERT_EMAIL_TO"), nil),
		AlertEmailFrom:          os.Getenv("ALERT_EMAIL_FROM"),
	}

	// Iterate in a fixed order so error messages are deterministic — tests
	// assert specific var names in a stable string, and ops appreciate
	// predictable output.
	requiredOrder := []string{
		"AI_GATEWAY_PG_DSN",
		"AI_GATEWAY_REDIS_ADDR",
		"UPSTREAM_LLM_URL",
		"UPSTREAM_EMBED_URL",
	}
	required := map[string]string{
		"AI_GATEWAY_PG_DSN":     cfg.PGDSN,
		"AI_GATEWAY_REDIS_ADDR": cfg.RedisAddr,
		"UPSTREAM_LLM_URL":      cfg.UpstreamLLMURL,
		"UPSTREAM_EMBED_URL":    cfg.UpstreamEmbedURL,
	}
	// Phase 11.1: UPSTREAM_STT_URL is no longer required — tier-0 STT was
	// removed (D-A4). The struct field is preserved transitionally for
	// operators with stale .env files; main.go skips wiring the local-stt
	// proxy when the value is empty.
	// UPSTREAM_HEALTH_BRIDGE_URL is now optional (Phase 3 D-D4 MED-06):
	// the health-bridge is a pod-internal debug surface only; gateway
	// routing uses upstreams.Loader + breaker.Set as the authority.
	// Operators deploying without the bridge (Vast.ai pod down, partial
	// config) should not have boot blocked by a non-critical variable.
	var missing []string
	for _, name := range requiredOrder {
		if strings.TrimSpace(required[name]) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("%w: %s", ErrMissingEnv, strings.Join(missing, ", "))
	}

	// Phase 06.9 D-07 — fail-fast on tier-1 URL convention. BuildDirector
	// preserves the client's `/v1/<route>` path on forward (so pod and
	// gateway routes mirror 1:1). A URL like `https://openrouter.ai/api/v1`
	// would therefore concatenate to `/api/v1/v1/chat/completions` upstream
	// and produce HTTP 404 — exactly the silent failure that masked the
	// OpenRouter fallback bug for months. We reject the misconfiguration at
	// boot with an operator-actionable error that names every offending var
	// + suggests the correct shape.
	urlChecks := []upstreamURLCheck{
		{"UPSTREAM_LLM_OPENROUTER_URL", cfg.UpstreamOpenRouterChatURL},
		{"UPSTREAM_STT_OPENAI_URL", cfg.UpstreamOpenAIWhisperURL},
		{"UPSTREAM_EMBED_OPENAI_URL", cfg.UpstreamOpenAIEmbedURL},
	}
	var invalidURLs []string
	for _, c := range urlChecks {
		if c.url == "" {
			// Phase 3 D-D4: external upstreams are OPTIONAL; absence =
			// tier-1 fallback disabled, not an error.
			continue
		}
		// strings.TrimRight strips trailing slashes so `/api/v1/` and
		// `/api/v1` both trip the same check. Empty input would have
		// short-circuited above.
		trimmed := strings.TrimRight(c.url, "/")
		if strings.HasSuffix(trimmed, "/v1") {
			invalidURLs = append(invalidURLs, c.envName+"="+c.url)
		}
	}
	if len(invalidURLs) > 0 {
		return Config{}, fmt.Errorf(
			"%w: %s (example for OpenRouter: https://openrouter.ai/api)",
			ErrInvalidURLSuffix,
			strings.Join(invalidURLs, ", "),
		)
	}

	// Phase 06.9 D-06 / BLOCKER-1 — operator observability for active
	// per-instance env overrides. Emit one INFO log line per active
	// UPSTREAM_*_MODEL env var so operators can confirm their per-instance
	// escape hatch is being honored at boot.
	//
	// We log via slog.Default() because config.Load runs BEFORE the
	// gateway constructs its configured logger (main.go:122). The default
	// handler is fine here — production main wires the configured slog
	// handler immediately after; this single INFO line during boot is
	// captured by whatever default writer is in place (typically stdout/
	// stderr, which Docker collects).
	//
	// Presence-only: the message names the UPSTREAM and the ENV_VAR but
	// NEVER the env VALUE — operators may have encoded customer info /
	// internal model names in the override and we treat the value as
	// sensitive. We deliberately avoid the word "deprecated": per D-06
	// the env override path is a SUPPORTED operator escape hatch (CLI
	// = multi-instance consistent; env = per-instance override; both
	// permanently supported).
	//
	// Iteration order over a Go map is randomized; that does NOT matter
	// for observability (each active override surfaces on its own line)
	// and matches the analogous boot-log in models.Resolver.Refresh.
	for upstreamName, envVar := range upstreamModelEnvVarMap {
		if os.Getenv(envVar) != "" {
			slog.Info("env override active for upstream",
				"upstream", upstreamName,
				"env_var", envVar,
			)
		}
	}

	// Phase 11.1 D-A6 deprecated-alias resolution. When the caller sets ONLY
	// the legacy env var without its _PRIMARY/_FALLBACK counterpart, copy the
	// legacy value into the new slot and emit a slog.Warn so operators see
	// the rename. The new env var always wins when set non-empty.
	//
	// Mapping (legacy → new slot):
	//   - PRIMARY_GPU_NAME            → PrimaryVastGPUNamePrimary
	//   - PRIMARY_VAST_PRICE_CAP_DPH  → PrimaryVastPriceCapPrimary
	//   - PRIMARY_NUM_GPUS            → PrimaryVastNumGPUsFallback (legacy
	//     variable historically meant "GPUs per primary pod", which is now
	//     the FALLBACK shape per primary-gpu-shape-06.8-final memory.)
	// Note: "set-but-empty" env (e.g. t.Setenv to "") counts as unset for
	// alias purposes — operators clear vars by setting them blank in
	// Portainer, not by unsetting the entry entirely.
	if legacy := os.Getenv("PRIMARY_GPU_NAME"); legacy != "" {
		if os.Getenv("PRIMARY_VAST_GPU_NAME_PRIMARY") == "" {
			slog.Warn("config: PRIMARY_GPU_NAME is deprecated; use PRIMARY_VAST_GPU_NAME_PRIMARY",
				"legacy_value", legacy)
			cfg.PrimaryVastGPUNamePrimary = legacy
		}
	}
	if legacy := os.Getenv("PRIMARY_VAST_PRICE_CAP_DPH"); legacy != "" {
		if os.Getenv("PRIMARY_VAST_PRICE_CAP_PRIMARY") == "" {
			slog.Warn("config: PRIMARY_VAST_PRICE_CAP_DPH is deprecated; use PRIMARY_VAST_PRICE_CAP_PRIMARY",
				"legacy_value", legacy)
			cfg.PrimaryVastPriceCapPrimary = floatOr(legacy, cfg.PrimaryVastPriceCapPrimary)
		}
	}
	if legacy := os.Getenv("PRIMARY_NUM_GPUS"); legacy != "" {
		if os.Getenv("PRIMARY_VAST_NUM_GPUS_FALLBACK") == "" {
			slog.Warn("config: PRIMARY_NUM_GPUS is deprecated; use PRIMARY_VAST_NUM_GPUS_FALLBACK (legacy var historically meant 'GPUs per primary pod', which is now the FALLBACK shape per Phase 11.1 D-A6)",
				"legacy_value", legacy)
			cfg.PrimaryVastNumGPUsFallback = atoiOr(legacy, cfg.PrimaryVastNumGPUsFallback)
		}
	}

	return cfg, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// atoi64Or parses an int64 from s; empty string or parse error returns def.
// Used for byte-size caps (VOICE_MAX_UPLOAD_BYTES) where int64 matches
// http.MaxBytesReader's signature without a lossy cast.
func atoi64Or(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// parseInt64CSV parses a comma-separated list of int64s (e.g. a Vast machine_id
// blocklist). Blank entries and unparseable tokens are skipped; empty input
// returns nil. Whitespace around tokens is tolerated.
func parseInt64CSV(s string) []int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []int64
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// strOr returns s unless it is empty/blank, in which case def is returned.
// Used for string env vars that carry a non-empty default (S3_VOICE_PREFIX).
func strOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// csvOr parses a comma-separated string into []string, trimming whitespace
// around each element. Empty input or all-blank elements -> default. Used
// for UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER which the Director injects
// into the request body via {"provider":{"order":[...]}}, see CONTEXT.md
// D-C2.
func csvOr(s string, def []string) []string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// boolOr parses "true"/"false"/"1"/"0"/"yes"/"no" case-insensitive. Anything
// else returns the default. Polarity-explicit per CLAUDE.md opt-out pattern —
// callers pass the desired default rather than relying on the parser to flip
// silently on bogus input.
func boolOr(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "y":
		return true
	case "false", "0", "no", "n":
		return false
	}
	return def
}

// floatOr parses a float64 from s; empty string or parse error returns def.
// Matches the shape of atoiOr so Phase 4 price/fx env vars plug in cleanly.
func floatOr(s string, def float64) float64 {
	if strings.TrimSpace(s) == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}
