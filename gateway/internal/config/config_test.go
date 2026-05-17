// Package config_test exercises Load() against env var fixtures.
package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
)

// allRequired are the five env vars Load() insists on (Phase 3 MED-06:
// UPSTREAM_HEALTH_BRIDGE_URL was demoted to optional — see config.go).
var allRequired = []string{
	"AI_GATEWAY_PG_DSN",
	"AI_GATEWAY_REDIS_ADDR",
	"UPSTREAM_LLM_URL",
	"UPSTREAM_STT_URL",
	"UPSTREAM_EMBED_URL",
}

// optionalVars are other vars we may want to clear so one test does not
// bleed state into the next via `os.Environ`.
var optionalVars = []string{
	"GATEWAY_PORT",
	"AI_GATEWAY_PG_MAX_CONNS",
	"AI_GATEWAY_REDIS_PASSWORD",
	"SENTRY_DSN",
	"LOG_LEVEL",
	"ENV",
	"BOOTSTRAP_TENANT_SLUG",
}

func clearAll(t *testing.T) {
	t.Helper()
	for _, v := range allRequired {
		t.Setenv(v, "")
	}
	for _, v := range optionalVars {
		t.Setenv(v, "")
	}
}

func setAllRequired(t *testing.T) {
	t.Helper()
	for _, v := range allRequired {
		t.Setenv(v, "fake-"+v)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	clearAll(t)
	_, err := config.Load()
	if err == nil {
		t.Fatalf("expected error when all required vars unset, got nil")
	}
	if !errors.Is(err, config.ErrMissingEnv) {
		t.Fatalf("expected errors.Is(err, ErrMissingEnv) true, got err=%v", err)
	}
	for _, name := range allRequired {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("expected error to mention %q, got %q", name, err.Error())
		}
	}
}

func TestLoad_MissingSingleVarNamedInError(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	t.Setenv("UPSTREAM_STT_URL", "")
	_, err := config.Load()
	if err == nil {
		t.Fatalf("expected error when UPSTREAM_STT_URL unset")
	}
	if !strings.Contains(err.Error(), "UPSTREAM_STT_URL") {
		t.Fatalf("expected error to mention UPSTREAM_STT_URL, got %q", err.Error())
	}
}

func TestLoad_AllRequiredPresent(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PGDSN == "" || cfg.RedisAddr == "" || cfg.UpstreamLLMURL == "" ||
		cfg.UpstreamSTTURL == "" || cfg.UpstreamEmbedURL == "" {
		t.Fatalf("expected populated required fields, got %+v", cfg)
	}
	// UpstreamHealthBridgeURL is optional since Phase 3 MED-06; not checked here.
}

func TestLoad_DefaultPort(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Fatalf("expected default Port=8080, got %d", cfg.Port)
	}
}

func TestLoad_CustomPort(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	t.Setenv("GATEWAY_PORT", "9999")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 9999 {
		t.Fatalf("expected Port=9999, got %d", cfg.Port)
	}
}

func TestLoad_PGMaxConnsDefault(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PGMaxConns != 10 {
		t.Fatalf("expected PGMaxConns=10, got %d", cfg.PGMaxConns)
	}
}

func TestLoad_FixedTimeouts(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout want 10s got %v", cfg.ReadHeaderTimeout)
	}
	if cfg.ReadTimeout != 60*time.Second {
		t.Errorf("ReadTimeout want 60s got %v", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 0 {
		t.Errorf("WriteTimeout want 0 got %v", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout want 120s got %v", cfg.IdleTimeout)
	}
	if cfg.MaxBodyBytes != 25*(1<<20) {
		t.Errorf("MaxBodyBytes want 25 MiB got %d", cfg.MaxBodyBytes)
	}
	if cfg.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes want 1 MiB got %d", cfg.MaxHeaderBytes)
	}
	if cfg.RedisKeyPrefix != "gw:" {
		t.Errorf("RedisKeyPrefix want 'gw:' got %q", cfg.RedisKeyPrefix)
	}
}

func TestLoad_SentryOptional(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SentryDSN != "" {
		t.Fatalf("SentryDSN should default to empty, got %q", cfg.SentryDSN)
	}
}

func TestLoad_LogLevelDefaultInfo(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default want 'info' got %q", cfg.LogLevel)
	}
	if cfg.Env != "production" {
		t.Errorf("Env default want 'production' got %q", cfg.Env)
	}
	if cfg.BootstrapTenantSlug != "converseai" {
		t.Errorf("BootstrapTenantSlug default want 'converseai' got %q", cfg.BootstrapTenantSlug)
	}
}

// phase3OptionalEnv enumerates the env vars introduced in Plan 03-02. Tests
// clear them in setUp so a stray value from a previous test does not leak
// into the default-value assertions.
var phase3OptionalEnv = []string{
	"UPSTREAM_LLM_OPENROUTER_URL",
	"UPSTREAM_LLM_OPENROUTER_AUTH_BEARER",
	"UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER",
	"UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS",
	"UPSTREAM_STT_OPENAI_URL",
	"UPSTREAM_STT_OPENAI_AUTH_BEARER",
	"UPSTREAM_EMBED_OPENAI_URL",
	"UPSTREAM_EMBED_OPENAI_AUTH_BEARER",
	"PROBE_INTERVAL_SECONDS",
	"PROBE_BUDGET_SECONDS",
	"BREAKER_CONSECUTIVE_FAILURES",
	"BREAKER_COOLDOWN_SECONDS",
	"WRITE_TIMEOUT_CHAT_SECONDS",
	"WRITE_TIMEOUT_EMBED_SECONDS",
	"WRITE_TIMEOUT_AUDIO_SECONDS",
}

func clearPhase3(t *testing.T) {
	t.Helper()
	for _, v := range phase3OptionalEnv {
		t.Setenv(v, "")
	}
}

// TestLoad_Phase3Defaults verifies that with only the 5 required vars set
// (UPSTREAM_HEALTH_BRIDGE_URL is now optional per MED-06), Load returns
// the documented Plan-03-02 defaults: probe
// 10s/5s, breaker 3 failures / 30s cooldown, per-route WriteTimeout
// 0/30s/120s for chat/embed/audio (Folded Todo from CONTEXT.md), and
// OpenRouter provider order ['novita'] with allow_fallbacks=false.
// (D-C1 amendment per 03-WAVE0-GATES.md — Fireworks does not serve Qwen 3
// family on OpenRouter as of 2026-04-20; Novita confirmed serving with
// finish_reason: "tool_calls".)
func TestLoad_Phase3Defaults(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.ProbeIntervalSeconds != 10 {
		t.Errorf("ProbeIntervalSeconds = %d, want 10", cfg.ProbeIntervalSeconds)
	}
	if cfg.ProbeBudgetSeconds != 5 {
		t.Errorf("ProbeBudgetSeconds = %d, want 5", cfg.ProbeBudgetSeconds)
	}
	if cfg.BreakerConsecutiveFailures != 3 {
		t.Errorf("BreakerConsecutiveFailures = %d, want 3", cfg.BreakerConsecutiveFailures)
	}
	if cfg.BreakerCooldownSeconds != 30 {
		t.Errorf("BreakerCooldownSeconds = %d, want 30", cfg.BreakerCooldownSeconds)
	}
	if cfg.WriteTimeoutChat != 0 {
		t.Errorf("WriteTimeoutChat = %v, want 0", cfg.WriteTimeoutChat)
	}
	if cfg.WriteTimeoutEmbed != 30*time.Second {
		t.Errorf("WriteTimeoutEmbed = %v, want 30s", cfg.WriteTimeoutEmbed)
	}
	if cfg.WriteTimeoutAudio != 120*time.Second {
		t.Errorf("WriteTimeoutAudio = %v, want 120s", cfg.WriteTimeoutAudio)
	}
	if len(cfg.UpstreamOpenRouterProviderOrder) != 1 ||
		cfg.UpstreamOpenRouterProviderOrder[0] != "novita" {
		t.Errorf("UpstreamOpenRouterProviderOrder = %v, want [novita]",
			cfg.UpstreamOpenRouterProviderOrder)
	}
	if cfg.UpstreamOpenRouterAllowFallbacks != false {
		t.Errorf("UpstreamOpenRouterAllowFallbacks = %v, want false",
			cfg.UpstreamOpenRouterAllowFallbacks)
	}
}

// TestLoad_Phase3CustomValues exercises atoiOr / csvOr / boolOr overrides
// for the new env vars. The CSV input includes spaces around commas to
// confirm csvOr trims whitespace per the Plan 03-02 spec.
func TestLoad_Phase3CustomValues(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	setAllRequired(t)
	t.Setenv("PROBE_INTERVAL_SECONDS", "5")
	t.Setenv("BREAKER_CONSECUTIVE_FAILURES", "5")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER", "fireworks, together ")
	t.Setenv("WRITE_TIMEOUT_EMBED_SECONDS", "15")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS", "true")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.ProbeIntervalSeconds != 5 {
		t.Errorf("got %d want 5", cfg.ProbeIntervalSeconds)
	}
	if cfg.BreakerConsecutiveFailures != 5 {
		t.Errorf("got %d want 5", cfg.BreakerConsecutiveFailures)
	}
	if got := cfg.UpstreamOpenRouterProviderOrder; len(got) != 2 ||
		got[0] != "fireworks" || got[1] != "together" {
		t.Errorf("ProviderOrder = %v, want [fireworks together]", got)
	}
	if cfg.WriteTimeoutEmbed != 15*time.Second {
		t.Errorf("got %v want 15s", cfg.WriteTimeoutEmbed)
	}
	if cfg.UpstreamOpenRouterAllowFallbacks != true {
		t.Errorf("AllowFallbacks = %v, want true", cfg.UpstreamOpenRouterAllowFallbacks)
	}
}

// TestLoad_Phase3ExternalURLsOptional asserts the Phase-3 external upstream
// env vars (OpenRouter, OpenAI Whisper/Embed) are NOT required at boot.
// The Loader will warn-log if a row in ai_gateway.upstreams is enabled but
// the env it points to is missing — boot itself must succeed.
func TestLoad_Phase3ExternalURLsOptional(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	setAllRequired(t) // Only the 5 required vars (UPSTREAM_HEALTH_BRIDGE_URL now optional).
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected Load to succeed without external URLs, got: %v", err)
	}
	if cfg.UpstreamOpenRouterChatURL != "" {
		t.Errorf("UpstreamOpenRouterChatURL = %q, want empty",
			cfg.UpstreamOpenRouterChatURL)
	}
	if cfg.UpstreamOpenAIWhisperURL != "" {
		t.Errorf("UpstreamOpenAIWhisperURL = %q, want empty",
			cfg.UpstreamOpenAIWhisperURL)
	}
	if cfg.UpstreamOpenAIEmbedURL != "" {
		t.Errorf("UpstreamOpenAIEmbedURL = %q, want empty",
			cfg.UpstreamOpenAIEmbedURL)
	}
}

// phase4OptionalEnv enumerates the env vars introduced in Plan 04-01. Tests
// clear them in setUp so a stray value from a previous test does not leak
// into the default-value assertions.
var phase4OptionalEnv = []string{
	"AI_GATEWAY_ADMIN_KEY_BOOTSTRAP",
	"AI_GATEWAY_RATE_LIMIT_FAIL_OPEN",
	"AI_GATEWAY_QUOTA_FAIL_OPEN",
	"AI_GATEWAY_USD_BRL_RATE_DEFAULT",
	"GATEWAY_WRITE_TIMEOUT_CHAT_S",
	"GATEWAY_WRITE_TIMEOUT_EMBED_S",
	"GATEWAY_WRITE_TIMEOUT_AUDIO_S",
}

func clearPhase4(t *testing.T) {
	t.Helper()
	for _, v := range phase4OptionalEnv {
		t.Setenv(v, "")
	}
}

// TestLoad_Phase4Defaults verifies that with only the 5 required vars set
// and no Phase 4 vars configured, Load returns the documented defaults:
// AdminKeyBootstrap="", RateLimitFailOpen=true (fail-open preserves failover
// invisibility during Redis blips), QuotaFailOpen=false (fail-closed stops
// runaway external cost when visibility is lost), USDBRLDefault=5.10,
// WriteTimeoutChatS=0 (SSE), WriteTimeoutEmbedS=30, WriteTimeoutAudioS=120.
func TestLoad_Phase4Defaults(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.AdminKeyBootstrap != "" {
		t.Errorf("AdminKeyBootstrap default: want empty, got %q", cfg.AdminKeyBootstrap)
	}
	if !cfg.RateLimitFailOpen {
		t.Error("RateLimitFailOpen default: want true")
	}
	if cfg.QuotaFailOpen {
		t.Error("QuotaFailOpen default: want false")
	}
	if cfg.USDBRLDefault != 5.10 {
		t.Errorf("USDBRLDefault default: want 5.10, got %v", cfg.USDBRLDefault)
	}
	if cfg.WriteTimeoutChatS != 0 {
		t.Errorf("WriteTimeoutChatS default: want 0 (SSE), got %d", cfg.WriteTimeoutChatS)
	}
	if cfg.WriteTimeoutEmbedS != 30 {
		t.Errorf("WriteTimeoutEmbedS default: want 30, got %d", cfg.WriteTimeoutEmbedS)
	}
	if cfg.WriteTimeoutAudioS != 120 {
		t.Errorf("WriteTimeoutAudioS default: want 120, got %d", cfg.WriteTimeoutAudioS)
	}
}

// TestLoad_Phase4FromEnv exercises overrides for every Phase 4 env var,
// including the floatOr helper (USD/BRL) and boolOr polarity flips.
func TestLoad_Phase4FromEnv(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	setAllRequired(t)
	t.Setenv("AI_GATEWAY_ADMIN_KEY_BOOTSTRAP", "ifix_admin_deadbeef")
	t.Setenv("AI_GATEWAY_RATE_LIMIT_FAIL_OPEN", "false")
	t.Setenv("AI_GATEWAY_QUOTA_FAIL_OPEN", "true")
	t.Setenv("AI_GATEWAY_USD_BRL_RATE_DEFAULT", "5.42")
	t.Setenv("GATEWAY_WRITE_TIMEOUT_CHAT_S", "45")
	t.Setenv("GATEWAY_WRITE_TIMEOUT_EMBED_S", "15")
	t.Setenv("GATEWAY_WRITE_TIMEOUT_AUDIO_S", "180")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.AdminKeyBootstrap != "ifix_admin_deadbeef" {
		t.Errorf("AdminKeyBootstrap = %q, want ifix_admin_deadbeef", cfg.AdminKeyBootstrap)
	}
	if cfg.RateLimitFailOpen {
		t.Error("RateLimitFailOpen: want false override")
	}
	if !cfg.QuotaFailOpen {
		t.Error("QuotaFailOpen: want true override")
	}
	if cfg.USDBRLDefault != 5.42 {
		t.Errorf("USDBRLDefault = %v, want 5.42", cfg.USDBRLDefault)
	}
	if cfg.WriteTimeoutChatS != 45 {
		t.Errorf("WriteTimeoutChatS = %d, want 45", cfg.WriteTimeoutChatS)
	}
	if cfg.WriteTimeoutEmbedS != 15 {
		t.Errorf("WriteTimeoutEmbedS = %d, want 15", cfg.WriteTimeoutEmbedS)
	}
	if cfg.WriteTimeoutAudioS != 180 {
		t.Errorf("WriteTimeoutAudioS = %d, want 180", cfg.WriteTimeoutAudioS)
	}
}

// TestLoad_Phase4FloatOrBogusValue confirms that a bogus USD/BRL env value
// falls back to the default 5.10 rather than silently becoming 0 (which
// would produce zero BRL costs for all rows — a Pitfall 6 catastrophe).
func TestLoad_Phase4FloatOrBogusValue(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	setAllRequired(t)
	t.Setenv("AI_GATEWAY_USD_BRL_RATE_DEFAULT", "not-a-number")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.USDBRLDefault != 5.10 {
		t.Errorf("USDBRLDefault on bogus input: want 5.10 fallback, got %v", cfg.USDBRLDefault)
	}
}

// phase6OptionalEnv enumerates the fifteen Phase 6 emergency-pod env vars
// after the Strategy B Locked refactor (06-02-PLAN): EMERGENCY_POD_IMAGE_TAG
// is gone (custom GHCR image dropped per CONTEXT.md D-01-B..D-08-B), replaced
// by EMERGENCY_TEMPLATE_IMAGE + EMERGENCY_JINJA_TEMPLATE_KEY +
// EMERGENCY_JINJA_TEMPLATE_SHA256 + EMERGENCY_LLAMA_ARGS. Cleared in setUp
// so a stray Portainer value does not leak into the default-value assertions.
var phase6OptionalEnv = []string{
	"EMERGENCY_JINJA_TEMPLATE_KEY",
	"EMERGENCY_JINJA_TEMPLATE_SHA256",
	"EMERGENCY_LLAMA_ARGS",
	"EMERGENCY_TEMPLATE_IMAGE",
	"MONTHLY_EMERGENCY_BUDGET_BRL",
	"PRIMARY_HOST_ID",
	"PROVISION_COLDSTART_BUDGET_SECONDS",
	"PROVISION_FAILURE_COOLDOWN_SECONDS",
	"PROVISION_HEALTHY_DURATION_SECONDS",
	"PROVISION_IDLE_GRACE_SECONDS",
	"PROVISION_TRIGGER_FAILED_OVER_SECONDS",
	"USD_TO_BRL_RATE",
	"VAST_AI_API_KEY",
	"VAST_API_QPS_LIMIT",
	"VAST_PRICE_CAP_DPH",
}

func clearPhase6(t *testing.T) {
	t.Helper()
	for _, v := range phase6OptionalEnv {
		t.Setenv(v, "")
	}
}

// TestLoad_Phase6Defaults validates that with no Phase 6 env vars set,
// Load returns the documented Wave-0 defaults from
// .planning/phases/06-emergency-pod-template-refactor/06-CONTEXT.md
// decisions D-01-B (template image), D-04-B option B2 (Jinja MinIO key+sha256
// non-empty defaults — locked by 06-WAVE0-GATES.md Decision 2), D-07-B
// (EmergencyLlamaArgs CSV empty → nil → lifecycle.go uses hardcoded const),
// plus legacy phase-6 emergency-pod defaults D-A1, D-A4, D-A5, D-C1, D-D1,
// D-D2, D-D4 (per 06-02 Plan Behavior Test 1-8 Strategy B Locked).
// VastAIAPIKey defaults empty to graceful-degrade (the reconciler logs a
// warning and stays disabled rather than failing boot — Phase 6 is opt-in
// until 06-WAVE0-GATES.md is closed).
func TestLoad_Phase6Defaults(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// Strategy B Locked defaults (06-WAVE0-GATES.md Decisions 2 + 3).
	if cfg.EmergencyTemplateImage != "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128" {
		t.Errorf("EmergencyTemplateImage = %q, want ghcr.io/ggml-org/llama.cpp:server-cuda-b9128 (D-01-B locked)", cfg.EmergencyTemplateImage)
	}
	// 06-WAVE0-GATES.md Decision 2 locked Strategy B2 → Jinja key+sha256
	// default to the production MinIO coordinates, NOT empty. Empty would
	// represent Strategy B1 (image overlay path) which the operator did
	// not select.
	const wantJinjaKey = "emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja"
	const wantJinjaSHA = "1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67"
	if cfg.EmergencyJinjaTemplateKey != wantJinjaKey {
		t.Errorf("EmergencyJinjaTemplateKey = %q, want %q (D-04-B B2 locked)", cfg.EmergencyJinjaTemplateKey, wantJinjaKey)
	}
	if cfg.EmergencyJinjaTemplateSHA256 != wantJinjaSHA {
		t.Errorf("EmergencyJinjaTemplateSHA256 = %q, want %q (D-04-B B2 locked)", cfg.EmergencyJinjaTemplateSHA256, wantJinjaSHA)
	}
	if cfg.EmergencyLlamaArgs != nil {
		t.Errorf("EmergencyLlamaArgs = %v, want nil (D-07-B; empty CSV → nil; lifecycle.go uses hardcoded const)", cfg.EmergencyLlamaArgs)
	}
	if cfg.MonthlyEmergencyBudgetBRL != 200.0 {
		t.Errorf("MonthlyEmergencyBudgetBRL = %v, want 200.0 (D-D2)", cfg.MonthlyEmergencyBudgetBRL)
	}
	if cfg.PrimaryHostID != 0 {
		t.Errorf("PrimaryHostID = %d, want 0 (unknown — D-A2)", cfg.PrimaryHostID)
	}
	if cfg.ProvisionColdStartBudgetSeconds != 600 {
		t.Errorf("ProvisionColdStartBudgetSeconds = %d, want 600 (D-A4)", cfg.ProvisionColdStartBudgetSeconds)
	}
	if cfg.ProvisionFailureCooldownSeconds != 60 {
		t.Errorf("ProvisionFailureCooldownSeconds = %d, want 60 (emerg-bid-race-lost backoff)", cfg.ProvisionFailureCooldownSeconds)
	}
	if cfg.ProvisionHealthyDurationSeconds != 300 {
		t.Errorf("ProvisionHealthyDurationSeconds = %d, want 300 (D-D1)", cfg.ProvisionHealthyDurationSeconds)
	}
	if cfg.ProvisionIdleGraceSeconds != 300 {
		t.Errorf("ProvisionIdleGraceSeconds = %d, want 300 (D-D1)", cfg.ProvisionIdleGraceSeconds)
	}
	if cfg.ProvisionTriggerFailedOverSeconds != 120 {
		t.Errorf("ProvisionTriggerFailedOverSeconds = %d, want 120 (D-C1)", cfg.ProvisionTriggerFailedOverSeconds)
	}
	if cfg.USDToBRLRate != 5.0 {
		t.Errorf("USDToBRLRate = %v, want 5.0 (D-D4)", cfg.USDToBRLRate)
	}
	if cfg.VastAIAPIKey != "" {
		t.Errorf("VastAIAPIKey: want empty default (graceful-degrade per D-A5), got %q", cfg.VastAIAPIKey)
	}
	if cfg.VastAPIQPSLimit != 1 {
		t.Errorf("VastAPIQPSLimit = %d, want 1 (RESEARCH OQ12)", cfg.VastAPIQPSLimit)
	}
	if cfg.VastPriceCapDPH != 0.40 {
		t.Errorf("VastPriceCapDPH = %v, want 0.40 (D-A2)", cfg.VastPriceCapDPH)
	}
}

// TestLoad_Phase6CustomValues exercises floatOr / atoiOr / csvOr overrides
// for the Phase 6 env vars. Includes a bogus VAST_PRICE_CAP_DPH to confirm
// the floatOr fallback prevents 0.0 cap (which would reject every offer).
// Strategy B Locked fields (EmergencyTemplateImage, EmergencyJinjaTemplateKey,
// EmergencyJinjaTemplateSHA256, EmergencyLlamaArgs) overrideable via env per
// 06-02 Plan Behavior Test 5-8.
func TestLoad_Phase6CustomValues(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	setAllRequired(t)
	t.Setenv("VAST_PRICE_CAP_DPH", "0.55")
	t.Setenv("MONTHLY_EMERGENCY_BUDGET_BRL", "350")
	t.Setenv("USD_TO_BRL_RATE", "5.42")
	t.Setenv("PROVISION_TRIGGER_FAILED_OVER_SECONDS", "60")
	t.Setenv("PROVISION_COLDSTART_BUDGET_SECONDS", "300")
	t.Setenv("PROVISION_FAILURE_COOLDOWN_SECONDS", "90")
	t.Setenv("VAST_AI_API_KEY", "fake-key-1234")
	t.Setenv("PRIMARY_HOST_ID", "987654")
	t.Setenv("EMERGENCY_TEMPLATE_IMAGE", "ghcr.io/ggml-org/llama.cpp:server-cuda-b9200")
	t.Setenv("EMERGENCY_JINJA_TEMPLATE_KEY", "emerg-onstart/templates/qwen-abc.jinja")
	t.Setenv("EMERGENCY_JINJA_TEMPLATE_SHA256", "deadbeef")
	t.Setenv("EMERGENCY_LLAMA_ARGS", "--host,0.0.0.0,--port,8000")
	t.Setenv("VAST_API_QPS_LIMIT", "2")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.VastPriceCapDPH != 0.55 {
		t.Errorf("VastPriceCapDPH override = %v, want 0.55", cfg.VastPriceCapDPH)
	}
	if cfg.MonthlyEmergencyBudgetBRL != 350.0 {
		t.Errorf("MonthlyEmergencyBudgetBRL override = %v, want 350.0", cfg.MonthlyEmergencyBudgetBRL)
	}
	if cfg.USDToBRLRate != 5.42 {
		t.Errorf("USDToBRLRate override = %v, want 5.42", cfg.USDToBRLRate)
	}
	if cfg.ProvisionTriggerFailedOverSeconds != 60 {
		t.Errorf("ProvisionTriggerFailedOverSeconds override = %d, want 60", cfg.ProvisionTriggerFailedOverSeconds)
	}
	if cfg.ProvisionColdStartBudgetSeconds != 300 {
		t.Errorf("ProvisionColdStartBudgetSeconds override = %d, want 300", cfg.ProvisionColdStartBudgetSeconds)
	}
	if cfg.ProvisionFailureCooldownSeconds != 90 {
		t.Errorf("ProvisionFailureCooldownSeconds override = %d, want 90", cfg.ProvisionFailureCooldownSeconds)
	}
	if cfg.VastAIAPIKey != "fake-key-1234" {
		t.Errorf("VastAIAPIKey override = %q, want fake-key-1234", cfg.VastAIAPIKey)
	}
	if cfg.PrimaryHostID != 987654 {
		t.Errorf("PrimaryHostID override = %d, want 987654", cfg.PrimaryHostID)
	}
	// Strategy B Locked overrides — 06-02 Plan Behavior Test 5-8.
	if cfg.EmergencyTemplateImage != "ghcr.io/ggml-org/llama.cpp:server-cuda-b9200" {
		t.Errorf("EmergencyTemplateImage override = %q, want ghcr.io/ggml-org/llama.cpp:server-cuda-b9200", cfg.EmergencyTemplateImage)
	}
	if cfg.EmergencyJinjaTemplateKey != "emerg-onstart/templates/qwen-abc.jinja" {
		t.Errorf("EmergencyJinjaTemplateKey override = %q, want emerg-onstart/templates/qwen-abc.jinja", cfg.EmergencyJinjaTemplateKey)
	}
	if cfg.EmergencyJinjaTemplateSHA256 != "deadbeef" {
		t.Errorf("EmergencyJinjaTemplateSHA256 override = %q, want deadbeef", cfg.EmergencyJinjaTemplateSHA256)
	}
	if got := cfg.EmergencyLlamaArgs; len(got) != 4 ||
		got[0] != "--host" || got[1] != "0.0.0.0" ||
		got[2] != "--port" || got[3] != "8000" {
		t.Errorf("EmergencyLlamaArgs override = %v, want [--host 0.0.0.0 --port 8000]", got)
	}
	if cfg.VastAPIQPSLimit != 2 {
		t.Errorf("VastAPIQPSLimit override = %d, want 2", cfg.VastAPIQPSLimit)
	}
}

// TestLoad_Phase6FloatOrBogusValue confirms that a bogus VAST_PRICE_CAP_DPH
// env value falls back to the default 0.40 rather than silently becoming
// 0.0 (which would reject every offer, defeating the purpose of the
// emergency reconciler — analog to Phase 4 USD_BRL Pitfall 6).
func TestLoad_Phase6FloatOrBogusValue(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	setAllRequired(t)
	t.Setenv("VAST_PRICE_CAP_DPH", "not-a-price")
	t.Setenv("MONTHLY_EMERGENCY_BUDGET_BRL", "garbage")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.VastPriceCapDPH != 0.40 {
		t.Errorf("VastPriceCapDPH on bogus input: want 0.40 fallback, got %v", cfg.VastPriceCapDPH)
	}
	if cfg.MonthlyEmergencyBudgetBRL != 200.0 {
		t.Errorf("MonthlyEmergencyBudgetBRL on bogus input: want 200.0 fallback, got %v", cfg.MonthlyEmergencyBudgetBRL)
	}
}

// phase7OptionalEnv enumerates the twelve* Phase 7 alerting env vars
// (*thirteen names — five Chatwoot, two ClickUp, five Brevo/email; the plan
// frontmatter groups them as "12 channels worth" of config). Cleared in
// setUp so a stray Portainer value does not leak into the default-value
// assertions.
var phase7OptionalEnv = []string{
	"CHATWOOT_API_URL",
	"CHATWOOT_API_TOKEN",
	"CHATWOOT_ONCALL_ACCOUNT_ID",
	"CHATWOOT_ONCALL_INBOX_ID",
	"CHATWOOT_ONCALL_CONTACT_ID",
	"CLICKUP_API_TOKEN",
	"CLICKUP_ALERT_LIST_ID",
	"BREVO_SMTP_HOST",
	"BREVO_SMTP_PORT",
	"BREVO_SMTP_USER",
	"BREVO_SMTP_PASS",
	"ALERT_EMAIL_TO",
	"ALERT_EMAIL_FROM",
}

func clearPhase7(t *testing.T) {
	t.Helper()
	for _, v := range phase7OptionalEnv {
		t.Setenv(v, "")
	}
}

// TestLoad_Phase7Defaults verifies that with only the 5 required vars set
// and no Phase 7 alerting vars configured, Load succeeds (boot is NEVER
// blocked by an unset alert var — same precedent as SentryDSN) and returns
// the documented zero/default values: every string empty, AlertEmailTo nil,
// BrevoSMTPPort defaulting to 587.
func TestLoad_Phase7Defaults(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase7(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected Load to succeed with all alert vars unset, got: %v", err)
	}
	if cfg.ChatwootAPIURL != "" || cfg.ChatwootAPIToken != "" ||
		cfg.ChatwootOncallAccountID != "" || cfg.ChatwootOncallInboxID != "" ||
		cfg.ChatwootOncallContactID != "" {
		t.Errorf("Chatwoot fields: want all empty, got %+v", cfg)
	}
	if cfg.ClickUpAPIToken != "" || cfg.ClickUpAlertListID != "" {
		t.Errorf("ClickUp fields: want all empty, got token=%q list=%q",
			cfg.ClickUpAPIToken, cfg.ClickUpAlertListID)
	}
	if cfg.BrevoSMTPHost != "" || cfg.BrevoSMTPUser != "" || cfg.BrevoSMTPPass != "" {
		t.Errorf("Brevo string fields: want all empty, got host=%q user=%q",
			cfg.BrevoSMTPHost, cfg.BrevoSMTPUser)
	}
	if cfg.BrevoSMTPPort != 587 {
		t.Errorf("BrevoSMTPPort default: want 587, got %d", cfg.BrevoSMTPPort)
	}
	if cfg.AlertEmailFrom != "" {
		t.Errorf("AlertEmailFrom: want empty, got %q", cfg.AlertEmailFrom)
	}
	if len(cfg.AlertEmailTo) != 0 {
		t.Errorf("AlertEmailTo: want nil/empty (email channel disabled), got %v", cfg.AlertEmailTo)
	}
}

// TestLoad_Phase7FromEnv exercises overrides for every Phase 7 alert env
// var, including atoiOr (BREVO_SMTP_PORT) and csvOr (ALERT_EMAIL_TO with
// whitespace around commas).
func TestLoad_Phase7FromEnv(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase7(t)
	setAllRequired(t)
	t.Setenv("CHATWOOT_API_URL", "https://crm.ifixtelecom.com.br")
	t.Setenv("CHATWOOT_API_TOKEN", "cw_token_abc")
	t.Setenv("CHATWOOT_ONCALL_ACCOUNT_ID", "7")
	t.Setenv("CHATWOOT_ONCALL_INBOX_ID", "12")
	t.Setenv("CHATWOOT_ONCALL_CONTACT_ID", "345")
	t.Setenv("CLICKUP_API_TOKEN", "pk_clickup_xyz")
	t.Setenv("CLICKUP_ALERT_LIST_ID", "901100")
	t.Setenv("BREVO_SMTP_HOST", "smtp-relay.brevo.com")
	t.Setenv("BREVO_SMTP_PORT", "2525")
	t.Setenv("BREVO_SMTP_USER", "brevo_login")
	t.Setenv("BREVO_SMTP_PASS", "brevo_key")
	t.Setenv("ALERT_EMAIL_TO", "ops1@ifix.com, ops2@ifix.com ,ops3@ifix.com")
	t.Setenv("ALERT_EMAIL_FROM", "alerts@ifix.com")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.ChatwootAPIURL != "https://crm.ifixtelecom.com.br" {
		t.Errorf("ChatwootAPIURL = %q", cfg.ChatwootAPIURL)
	}
	if cfg.ChatwootAPIToken != "cw_token_abc" {
		t.Errorf("ChatwootAPIToken = %q", cfg.ChatwootAPIToken)
	}
	if cfg.ChatwootOncallAccountID != "7" || cfg.ChatwootOncallInboxID != "12" ||
		cfg.ChatwootOncallContactID != "345" {
		t.Errorf("Chatwoot on-call ids = %q/%q/%q", cfg.ChatwootOncallAccountID,
			cfg.ChatwootOncallInboxID, cfg.ChatwootOncallContactID)
	}
	if cfg.ClickUpAPIToken != "pk_clickup_xyz" || cfg.ClickUpAlertListID != "901100" {
		t.Errorf("ClickUp = %q/%q", cfg.ClickUpAPIToken, cfg.ClickUpAlertListID)
	}
	if cfg.BrevoSMTPHost != "smtp-relay.brevo.com" || cfg.BrevoSMTPPort != 2525 ||
		cfg.BrevoSMTPUser != "brevo_login" || cfg.BrevoSMTPPass != "brevo_key" {
		t.Errorf("Brevo = host=%q port=%d user=%q", cfg.BrevoSMTPHost,
			cfg.BrevoSMTPPort, cfg.BrevoSMTPUser)
	}
	if cfg.AlertEmailFrom != "alerts@ifix.com" {
		t.Errorf("AlertEmailFrom = %q", cfg.AlertEmailFrom)
	}
	if got := cfg.AlertEmailTo; len(got) != 3 || got[0] != "ops1@ifix.com" ||
		got[1] != "ops2@ifix.com" || got[2] != "ops3@ifix.com" {
		t.Errorf("AlertEmailTo = %v, want 3 trimmed entries", got)
	}
}

// TestLoad_Phase7NotRequired is the explicit guard for threat T-07-01 /
// the must_have truth "Gateway boots with all 12 new alert env vars unset":
// none of the alert vars may appear in the required-var error string.
func TestLoad_Phase7NotRequired(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase7(t)
	// Deliberately do NOT call setAllRequired — force the missing-var error.
	_, err := config.Load()
	if err == nil {
		t.Fatalf("expected ErrMissingEnv with required vars unset")
	}
	for _, name := range phase7OptionalEnv {
		if strings.Contains(err.Error(), name) {
			t.Errorf("alert var %q must NOT be in the required-var error: %q", name, err.Error())
		}
	}
}
