// Package config_test exercises Load() against env var fixtures.
package config_test

import (
	"bytes"
	"errors"
	"log/slog"
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

// phase6_6OptionalEnv enumerates the 24 Phase 6.6 primary-pod env vars
// introduced in Plan 06.6-03. Cleared in setUp so a stray Portainer value
// does not leak into the default-value assertions.
//
// Wave 0 operator-locked decisions (06.6-WAVE0-GATES.md):
//   - Decision 1: 4 image SHA pins (template/speaches/infinity/dcgm)
//   - Decision 3: B1 GGUF-embedded Jinja LOCKED (key+sha256 empty defaults)
//   - Decision 5: PrimaryPodScheduleDisabled default true (soak gate)
//   - Decision 6: ColdStartBudget default 2400 (40min generous margin)
var phase6_6OptionalEnv = []string{
	"PRIMARY_TEMPLATE_IMAGE",
	"PRIMARY_SPEACHES_IMAGE",
	"PRIMARY_INFINITY_IMAGE",
	"PRIMARY_DCGM_IMAGE",
	"PRIMARY_QWEN_WEIGHTS_KEY",
	"PRIMARY_QWEN_WEIGHTS_SHA256",
	"PRIMARY_QWEN_JINJA_KEY",
	"PRIMARY_QWEN_JINJA_SHA256",
	"PRIMARY_LLAMA_ARGS",
	"PRIMARY_WHISPER_WEIGHTS_KEY",
	"PRIMARY_WHISPER_WEIGHTS_SHA256",
	"PRIMARY_BGEM3_WEIGHTS_KEY",
	"PRIMARY_BGEM3_WEIGHTS_SHA256",
	"PRIMARY_VAST_PRICE_CAP_DPH",
	"PRIMARY_PROVISION_COLDSTART_BUDGET_SECONDS",
	"PRIMARY_PROVISION_FAILURE_COOLDOWN_SECONDS",
	"MONTHLY_PRIMARY_BUDGET_BRL",
	"PRIMARY_POD_SCHEDULE_TIMEZONE",
	"PRIMARY_POD_SCHEDULE_UP_HOUR",
	"PRIMARY_POD_SCHEDULE_DOWN_HOUR",
	"PRIMARY_POD_SCHEDULE_DAYS",
	"PRIMARY_POD_SCHEDULE_GRACE_RAMP_DOWN_SECONDS",
	"PRIMARY_POD_SCHEDULE_DISABLED",
	"PRIMARY_POD_SCHEDULE_PROVISION_LEAD_SECONDS",
}

func clearPhase6_6(t *testing.T) {
	t.Helper()
	for _, v := range phase6_6OptionalEnv {
		t.Setenv(v, "")
	}
}

// TestConfig_PrimaryPod_DefaultsLoaded validates that with no Phase 6.6 env
// vars set, Load returns the documented Wave 0 LOCKED defaults from
// 06.6-WAVE0-GATES.md Decisions 1, 3, 5, 6 + Reviews 2026-05-17 #6 #8.
func TestConfig_PrimaryPod_DefaultsLoaded(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	// Schedule defaults (D-08.1).
	if cfg.PrimaryPodScheduleTimezone != "America/Sao_Paulo" {
		t.Errorf("PrimaryPodScheduleTimezone = %q, want America/Sao_Paulo (D-08.1)", cfg.PrimaryPodScheduleTimezone)
	}
	if cfg.PrimaryPodScheduleUpHour != 8 {
		t.Errorf("PrimaryPodScheduleUpHour = %d, want 8 (D-08.1)", cfg.PrimaryPodScheduleUpHour)
	}
	if cfg.PrimaryPodScheduleDownHour != 22 {
		t.Errorf("PrimaryPodScheduleDownHour = %d, want 22 (D-08.1)", cfg.PrimaryPodScheduleDownHour)
	}
	wantDays := []string{"mon", "tue", "wed", "thu", "fri"}
	if got := cfg.PrimaryPodScheduleDays; len(got) != 5 ||
		got[0] != wantDays[0] || got[1] != wantDays[1] || got[2] != wantDays[2] ||
		got[3] != wantDays[3] || got[4] != wantDays[4] {
		t.Errorf("PrimaryPodScheduleDays = %v, want %v (D-08.1)", got, wantDays)
	}
	if cfg.PrimaryPodScheduleGraceRampDownSeconds != 300 {
		t.Errorf("PrimaryPodScheduleGraceRampDownSeconds = %d, want 300 (D-08.1)", cfg.PrimaryPodScheduleGraceRampDownSeconds)
	}
	// WAVE0-GATES Decision 5: soak gate enforced at config layer.
	if !cfg.PrimaryPodScheduleDisabled {
		t.Errorf("PrimaryPodScheduleDisabled default = false, want true (WAVE0-GATES Decision 5 soak gate)")
	}
	// Reviews consensus action #8: pre-warm offset.
	if cfg.PrimaryPodScheduleProvisionLeadSeconds != 1800 {
		t.Errorf("PrimaryPodScheduleProvisionLeadSeconds = %d, want 1800 (reviews #8 pre-warm offset)", cfg.PrimaryPodScheduleProvisionLeadSeconds)
	}

	// Image defaults (WAVE0-GATES Decision 1) — full SHA-pinned literals.
	if !strings.Contains(cfg.PrimaryTemplateImage, "server-cuda-b9191") {
		t.Errorf("PrimaryTemplateImage missing b9191 tag: %q", cfg.PrimaryTemplateImage)
	}
	if !strings.Contains(cfg.PrimaryTemplateImage, "@sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3") {
		t.Errorf("PrimaryTemplateImage missing locked sha256 cb37...: %q", cfg.PrimaryTemplateImage)
	}

	// Cold-start budget (WAVE0-GATES Decision 6).
	if cfg.PrimaryProvisionColdStartBudgetSeconds != 2400 {
		t.Errorf("PrimaryProvisionColdStartBudgetSeconds = %d, want 2400 (WAVE0-GATES Decision 6 40min budget)", cfg.PrimaryProvisionColdStartBudgetSeconds)
	}

	// FAIL-FAST policy per reviews consensus action #6 — Whisper/BGE SHA
	// have NO envOr default; empty passthrough so Plan 06.6-04
	// buildPrimaryCreateRequest rejects at build time.
	if cfg.PrimaryWhisperWeightsSHA256 != "" {
		t.Errorf("PrimaryWhisperWeightsSHA256 default = %q, want empty (reviews #6 fail-fast policy)", cfg.PrimaryWhisperWeightsSHA256)
	}
	if cfg.PrimaryBGEM3WeightsSHA256 != "" {
		t.Errorf("PrimaryBGEM3WeightsSHA256 default = %q, want empty (reviews #6 fail-fast policy)", cfg.PrimaryBGEM3WeightsSHA256)
	}

	// WAVE0-GATES Decision 3 — B1 GGUF-embedded Jinja LOCKED.
	if cfg.PrimaryQwenJinjaKey != "" {
		t.Errorf("PrimaryQwenJinjaKey default = %q, want empty (WAVE0-GATES Decision 3 B1 embedded)", cfg.PrimaryQwenJinjaKey)
	}
	if cfg.PrimaryQwenJinjaSHA256 != "" {
		t.Errorf("PrimaryQwenJinjaSHA256 default = %q, want empty (WAVE0-GATES Decision 3 B1 embedded)", cfg.PrimaryQwenJinjaSHA256)
	}

	// Other defaults from CONTEXT.md D-04 + RESEARCH Decisions Resolved #5.
	if cfg.PrimaryQwenWeightsKey != "qwen3.6-27b-Q4_K_M/v1.0.0/model.gguf" {
		t.Errorf("PrimaryQwenWeightsKey = %q, want qwen3.6-27b-Q4_K_M/v1.0.0/model.gguf", cfg.PrimaryQwenWeightsKey)
	}
	if cfg.PrimaryQwenWeightsSHA256 != "a7cbd3ecc0e3f9b333edee61ae66bc87ed713c5d49587a8355814722ed329e0f" {
		t.Errorf("PrimaryQwenWeightsSHA256 = %q, want a7cbd3ec... (Wave 0 verified)", cfg.PrimaryQwenWeightsSHA256)
	}
	if cfg.PrimaryLlamaArgs != nil {
		t.Errorf("PrimaryLlamaArgs default = %v, want nil (empty CSV → lifecycle.go uses const)", cfg.PrimaryLlamaArgs)
	}
	if cfg.PrimaryWhisperWeightsKey != "whisper-large-v3/v1.0.0/model.tar.gz" {
		t.Errorf("PrimaryWhisperWeightsKey = %q, want whisper-large-v3/v1.0.0/model.tar.gz", cfg.PrimaryWhisperWeightsKey)
	}
	if cfg.PrimaryBGEM3WeightsKey != "bge-m3/v1.0.0/model.tar.gz" {
		t.Errorf("PrimaryBGEM3WeightsKey = %q, want bge-m3/v1.0.0/model.tar.gz", cfg.PrimaryBGEM3WeightsKey)
	}
	if cfg.PrimaryVastPriceCapDPH != 2.20 {
		t.Errorf("PrimaryVastPriceCapDPH = %v, want 2.20 (RTX 5090 EU cap; UAT 17 follow-up)", cfg.PrimaryVastPriceCapDPH)
	}
	if cfg.MonthlyPrimaryBudgetBRL != 800.0 {
		t.Errorf("MonthlyPrimaryBudgetBRL = %v, want 800.0 (Pitfall #12 separate from emergency)", cfg.MonthlyPrimaryBudgetBRL)
	}
}

// TestConfig_PrimaryPod_EnvOverride exercises atoiOr / boolOr / floatOr /
// csvOr overrides for representative Phase 6.6 env vars.
func TestConfig_PrimaryPod_EnvOverride(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	t.Setenv("PRIMARY_POD_SCHEDULE_UP_HOUR", "7")
	t.Setenv("PRIMARY_POD_SCHEDULE_DISABLED", "false")
	t.Setenv("PRIMARY_VAST_PRICE_CAP_DPH", "0.50")
	t.Setenv("PRIMARY_LLAMA_ARGS", "--host,0.0.0.0,--port,8000")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.PrimaryPodScheduleUpHour != 7 {
		t.Errorf("PrimaryPodScheduleUpHour override = %d, want 7", cfg.PrimaryPodScheduleUpHour)
	}
	if cfg.PrimaryPodScheduleDisabled {
		t.Errorf("PrimaryPodScheduleDisabled override: want false")
	}
	if cfg.PrimaryVastPriceCapDPH != 0.50 {
		t.Errorf("PrimaryVastPriceCapDPH override = %v, want 0.50", cfg.PrimaryVastPriceCapDPH)
	}
	if got := cfg.PrimaryLlamaArgs; len(got) != 4 ||
		got[0] != "--host" || got[1] != "0.0.0.0" ||
		got[2] != "--port" || got[3] != "8000" {
		t.Errorf("PrimaryLlamaArgs override = %v, want [--host 0.0.0.0 --port 8000]", got)
	}
}

// TestConfig_PrimaryPod_DaysCSVParse confirms csvOr handles the schedule
// days override correctly (trimming whitespace, preserving order).
func TestConfig_PrimaryPod_DaysCSVParse(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	t.Setenv("PRIMARY_POD_SCHEDULE_DAYS", "tue,wed,thu")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got := cfg.PrimaryPodScheduleDays; len(got) != 3 ||
		got[0] != "tue" || got[1] != "wed" || got[2] != "thu" {
		t.Errorf("PrimaryPodScheduleDays override = %v, want [tue wed thu]", got)
	}
}

// TestConfig_PrimaryPod_WhisperSHANoDefault enforces reviews consensus
// action #6 — fail-fast SHA policy. Operator MUST set the env explicitly;
// Plan 06.6-04 buildPrimaryCreateRequest rejects empty values.
func TestConfig_PrimaryPod_WhisperSHANoDefault(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.PrimaryWhisperWeightsSHA256 != "" {
		t.Errorf("PrimaryWhisperWeightsSHA256 = %q, want empty (reviews #6 fail-fast — operator must set explicitly)", cfg.PrimaryWhisperWeightsSHA256)
	}
}

// TestConfig_PrimaryPod_BGEM3SHANoDefault — same fail-fast policy as
// TestConfig_PrimaryPod_WhisperSHANoDefault for BGE-M3 embedding weights.
func TestConfig_PrimaryPod_BGEM3SHANoDefault(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.PrimaryBGEM3WeightsSHA256 != "" {
		t.Errorf("PrimaryBGEM3WeightsSHA256 = %q, want empty (reviews #6 fail-fast)", cfg.PrimaryBGEM3WeightsSHA256)
	}
}

// TestConfig_PrimaryPod_FailureCooldownDefault enforces the 300s default
// (scaled-up vs emerg ProvisionFailureCooldownSeconds=60 — schedule cadence
// is hourly, not per-failover-event). Plan 06.6-06a reconciler consumer.
func TestConfig_PrimaryPod_FailureCooldownDefault(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.PrimaryProvisionFailureCooldownSeconds != 300 {
		t.Errorf("PrimaryProvisionFailureCooldownSeconds default = %d, want 300 (5min scaled-up vs emerg 60s)", cfg.PrimaryProvisionFailureCooldownSeconds)
	}
	t.Setenv("PRIMARY_PROVISION_FAILURE_COOLDOWN_SECONDS", "120")
	cfg2, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg2.PrimaryProvisionFailureCooldownSeconds != 120 {
		t.Errorf("PrimaryProvisionFailureCooldownSeconds override = %d, want 120", cfg2.PrimaryProvisionFailureCooldownSeconds)
	}
}

// TestConfig_PrimaryPod_ProvisionLeadDefault enforces 1800s default (30min
// pre-warm offset) per reviews consensus action #8. Plan 06.6-05 consumer.
func TestConfig_PrimaryPod_ProvisionLeadDefault(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.PrimaryPodScheduleProvisionLeadSeconds != 1800 {
		t.Errorf("PrimaryPodScheduleProvisionLeadSeconds default = %d, want 1800 (30min pre-warm; reviews #8)", cfg.PrimaryPodScheduleProvisionLeadSeconds)
	}
	t.Setenv("PRIMARY_POD_SCHEDULE_PROVISION_LEAD_SECONDS", "2400")
	cfg2, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg2.PrimaryPodScheduleProvisionLeadSeconds != 2400 {
		t.Errorf("PrimaryPodScheduleProvisionLeadSeconds override = %d, want 2400", cfg2.PrimaryPodScheduleProvisionLeadSeconds)
	}
}

// TestConfig_PrimaryPod_ColdStartBudget40MinDefault enforces WAVE0-GATES
// Decision 6 — 2400s (40min) generous margin accommodating slow inet hosts
// + multi-stage image pull + aria2c weight download + 4-service supervisord
// startup. Reconciler treats >40min as provision failure.
func TestConfig_PrimaryPod_ColdStartBudget40MinDefault(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.PrimaryProvisionColdStartBudgetSeconds != 2400 {
		t.Errorf("PrimaryProvisionColdStartBudgetSeconds default = %d, want 2400 (WAVE0-GATES Decision 6 40min budget)", cfg.PrimaryProvisionColdStartBudgetSeconds)
	}
}

// TestConfig_PrimaryPod_TemplateImageIsB9191SHAPinned asserts the literal
// b9191 digest from WAVE0-GATES Decision 1. Wave 0 spike
// (06.6-SPIKE-qwen3.6-jinja.md Round 3) proved that prior b9128 builds fail
// Qwen3.6 SSM tensor load with `missing tensor 'blk.64.ssm_conv1d.weight'`
// — b9191 includes upstream PRs #23121 + #22384 that add SSM/recurrent
// model support. This test enforces the upgrade at the config layer.
func TestConfig_PrimaryPod_TemplateImageIsB9191SHAPinned(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(cfg.PrimaryTemplateImage, "server-cuda-b9191") {
		t.Errorf("PrimaryTemplateImage = %q, must contain server-cuda-b9191 (WAVE0-GATES Decision 1 — UPGRADED from prior b9128 build)", cfg.PrimaryTemplateImage)
	}
	if !strings.Contains(cfg.PrimaryTemplateImage, "@sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3") {
		t.Errorf("PrimaryTemplateImage = %q, must contain @sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3 (WAVE0-GATES Decision 1 locked digest)", cfg.PrimaryTemplateImage)
	}
	// Negative assertion: confirm prior build is NOT the default for the
	// PRIMARY pod path (Phase 6 emergency pod continues on its own build —
	// scoped to PrimaryTemplateImage field only, NOT EmergencyTemplateImage).
	if strings.Contains(cfg.PrimaryTemplateImage, "server-cuda-b9128") {
		t.Errorf("PrimaryTemplateImage = %q, must NOT use prior b9128 build — Wave 0 spike Round 3 empirically proved it fails Qwen3.6 SSM tensor load", cfg.PrimaryTemplateImage)
	}
}

// TestConfig_PrimaryPod_AllUpstreamImagesSHAPinned enforces all 4
// WAVE0-GATES Decision 1 digests — proves the OCI image references the
// custom multi-stage build (Plan 06.6-04) consumes are locked at the
// config layer. Image bumps require WAVE0-GATES amendment + operator
// sign-off (T-06.6-SC tampering mitigation).
func TestConfig_PrimaryPod_AllUpstreamImagesSHAPinned(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(cfg.PrimaryTemplateImage, "@sha256:cb37") {
		t.Errorf("PrimaryTemplateImage missing @sha256:cb37 digest prefix: %q", cfg.PrimaryTemplateImage)
	}
	if !strings.Contains(cfg.PrimarySpeachesImage, "@sha256:5c62") {
		t.Errorf("PrimarySpeachesImage missing @sha256:5c62 digest prefix: %q", cfg.PrimarySpeachesImage)
	}
	if !strings.Contains(cfg.PrimaryInfinityImage, "@sha256:11e8") {
		t.Errorf("PrimaryInfinityImage missing @sha256:11e8 digest prefix: %q", cfg.PrimaryInfinityImage)
	}
	if !strings.Contains(cfg.PrimaryDCGMImage, "@sha256:60d3") {
		t.Errorf("PrimaryDCGMImage missing @sha256:60d3 digest prefix: %q", cfg.PrimaryDCGMImage)
	}
}

// TestConfig_PrimaryPod_DisabledDefaultsTrue_SoakGate enforces WAVE0-GATES
// Decision 5 soak gate. Operator manual flip to false required after Plan
// 06.6-11 Live UAT GREEN. Prevents scheduled provisioning from kicking in
// during soak period when integrations may still be debugged. Same pattern
// as Phase 6 PR2 EMERGENCY_POD_DISABLED=true initial deploy.
func TestConfig_PrimaryPod_DisabledDefaultsTrue_SoakGate(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !cfg.PrimaryPodScheduleDisabled {
		t.Errorf("PrimaryPodScheduleDisabled default = false, want true (WAVE0-GATES Decision 5 soak gate — operator manual flip after UAT GREEN)")
	}
}

// TestConfig_PrimaryPod_JinjaDefaultsEmpty_B1Embedded enforces WAVE0-GATES
// Decision 3 — B1 GGUF-embedded Jinja LOCKED. The `--jinja` flag alone
// (no `--chat-template-file`) extracts PEG-native parser from the Qwen3.6
// GGUF chat_template. Wave 0 spike Round 3 validated tool-calling end-to-
// end: `chat_format: peg-native`, `tool_calls[0].function.arguments=
// {"location":"São Paulo"}`. Override via env permitted for future B2
// MinIO fallback.
func TestConfig_PrimaryPod_JinjaDefaultsEmpty_B1Embedded(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase4(t)
	clearPhase6(t)
	clearPhase6_6(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if cfg.PrimaryQwenJinjaKey != "" {
		t.Errorf("PrimaryQwenJinjaKey default = %q, want empty (WAVE0-GATES Decision 3 B1 GGUF-embedded LOCKED)", cfg.PrimaryQwenJinjaKey)
	}
	if cfg.PrimaryQwenJinjaSHA256 != "" {
		t.Errorf("PrimaryQwenJinjaSHA256 default = %q, want empty (WAVE0-GATES Decision 3 B1 embedded)", cfg.PrimaryQwenJinjaSHA256)
	}
}

// =============================================================================
// Phase 06.9 Plan 04 — D-07 URL suffix fail-fast + D-06 INFO log on active overrides
// =============================================================================
//
// Tests below exercise the new validation block that rejects
// UPSTREAM_{LLM_OPENROUTER,STT_OPENAI,EMBED_OPENAI}_URL ending in `/v1` at
// boot time (D-07) and the D-06 / BLOCKER-1 observability INFO log that
// surfaces which UPSTREAM_*_MODEL env overrides are currently active. Env
// vars are SUPPORTED operator escape hatches per D-06 — NOT deprecated.

// phase69Plan04OptionalEnv enumerates the env vars whose presence affects
// the Phase 06.9 Plan 04 D-06 INFO log + D-07 URL suffix validation.
// Cleared before each Phase 06.9 Plan 04 test so a stray value from a
// previous test in the same process does not leak.
var phase69Plan04OptionalEnv = []string{
	"UPSTREAM_LLM_OPENROUTER_URL",
	"UPSTREAM_STT_OPENAI_URL",
	"UPSTREAM_EMBED_OPENAI_URL",
	"UPSTREAM_LLM_OPENROUTER_MODEL",
	"UPSTREAM_STT_OPENAI_MODEL",
	"UPSTREAM_EMBED_OPENAI_MODEL",
}

func clearPhase69Plan04(t *testing.T) {
	t.Helper()
	for _, v := range phase69Plan04OptionalEnv {
		t.Setenv(v, "")
	}
}

// captureDefaultLog swaps the slog default logger for one writing to a
// returned *bytes.Buffer. Cleanup restores the prior default. Used by the
// Phase 06.9 Plan 04 D-06 INFO-log tests — config.Load emits the active-
// override observability lines via slog.Default() because config.Load has
// no logger arg (the gateway logger is constructed AFTER Load returns).
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return &buf
}

// TestLoad_RejectsOpenRouterURLEndingInV1 — D-07: UPSTREAM_LLM_OPENROUTER_URL
// ending in `/v1` is rejected at boot with an actionable error message that
// names the offending env var + suggests the correct value.
func TestLoad_RejectsOpenRouterURLEndingInV1(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase69Plan04(t)
	setAllRequired(t)
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "https://openrouter.ai/api/v1")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for URL ending in /v1, got nil")
	}
	if !errors.Is(err, config.ErrInvalidURLSuffix) {
		t.Fatalf("expected errors.Is(err, ErrInvalidURLSuffix) true, got err=%v", err)
	}
	if !strings.Contains(err.Error(), "UPSTREAM_LLM_OPENROUTER_URL") {
		t.Errorf("error message must name UPSTREAM_LLM_OPENROUTER_URL, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "https://openrouter.ai/api/v1") {
		t.Errorf("error message must include the offending URL value, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "https://openrouter.ai/api") {
		t.Errorf("error message must suggest correct value https://openrouter.ai/api, got %q", err.Error())
	}
}

// TestLoad_RejectsURLEndingInV1WithTrailingSlash — D-07: trailing slash
// (e.g. `/api/v1/`) is also rejected; trim-right strips the slash before
// the suffix check so this case must trip the same as the no-slash case.
func TestLoad_RejectsURLEndingInV1WithTrailingSlash(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase69Plan04(t)
	setAllRequired(t)
	t.Setenv("UPSTREAM_STT_OPENAI_URL", "https://api.openai.com/v1/")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for URL ending in /v1/, got nil")
	}
	if !errors.Is(err, config.ErrInvalidURLSuffix) {
		t.Fatalf("expected errors.Is(err, ErrInvalidURLSuffix) true, got err=%v", err)
	}
	if !strings.Contains(err.Error(), "UPSTREAM_STT_OPENAI_URL") {
		t.Errorf("error message must name UPSTREAM_STT_OPENAI_URL, got %q", err.Error())
	}
}

// TestLoad_AcceptsURLNotEndingInV1 — D-07: the correct shape per Wave 0
// Gate A amendment (https://openrouter.ai/api) is accepted; no error
// returned for this var.
func TestLoad_AcceptsURLNotEndingInV1(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase69Plan04(t)
	setAllRequired(t)
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "https://openrouter.ai/api")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error for /api URL: %v", err)
	}
	if cfg.UpstreamOpenRouterChatURL != "https://openrouter.ai/api" {
		t.Errorf("UpstreamOpenRouterChatURL = %q, want https://openrouter.ai/api", cfg.UpstreamOpenRouterChatURL)
	}
}

// TestLoad_EmptyURLPassesValidation — D-07: the three external upstream URL
// vars are OPTIONAL (Phase 3 D-D4 — external tier-1 fallback disabled when
// unset). Absence MUST NOT trip the suffix validation.
func TestLoad_EmptyURLPassesValidation(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase69Plan04(t)
	setAllRequired(t)
	// All three URLs explicitly unset (clearPhase69Plan04 already does this; assert intent).
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "")
	t.Setenv("UPSTREAM_STT_OPENAI_URL", "")
	t.Setenv("UPSTREAM_EMBED_OPENAI_URL", "")
	_, err := config.Load()
	if err != nil {
		t.Fatalf("expected nil error when all three URLs empty, got %v", err)
	}
}

// TestLoad_RejectsAllThreeVarsWhenAllEndV1 — D-07: when all three URLs end
// in /v1, the error message lists all three var names so the operator can
// fix them in one pass.
func TestLoad_RejectsAllThreeVarsWhenAllEndV1(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase69Plan04(t)
	setAllRequired(t)
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "https://openrouter.ai/api/v1")
	t.Setenv("UPSTREAM_STT_OPENAI_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_EMBED_OPENAI_URL", "https://api.openai.com/v1")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when all three URLs end in /v1, got nil")
	}
	if !errors.Is(err, config.ErrInvalidURLSuffix) {
		t.Fatalf("expected errors.Is(err, ErrInvalidURLSuffix), got %v", err)
	}
	for _, name := range []string{
		"UPSTREAM_LLM_OPENROUTER_URL",
		"UPSTREAM_STT_OPENAI_URL",
		"UPSTREAM_EMBED_OPENAI_URL",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error message must mention %s, got %q", name, err.Error())
		}
	}
}

// TestLoad_LogsInfoOnActiveOpenRouterModelEnv — BLOCKER-1 / D-06: when
// UPSTREAM_LLM_OPENROUTER_MODEL is set, Load() emits an INFO log surfacing
// the active override per operator observability per D-06. The log line
// MUST NOT contain the env VALUE (presence-only) and MUST NOT use the word
// "deprecated" (D-06 keeps env-overrides supported permanently).
func TestLoad_LogsInfoOnActiveOpenRouterModelEnv(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase69Plan04(t)
	setAllRequired(t)
	buf := captureDefaultLog(t)
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "qwen/qwen3.5-27b")
	if _, err := config.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "env override active for upstream") {
		t.Errorf("expected log message 'env override active for upstream', got:\n%s", out)
	}
	if !strings.Contains(out, "upstream=openrouter-chat") {
		t.Errorf("expected log attr upstream=openrouter-chat, got:\n%s", out)
	}
	if !strings.Contains(out, "env_var=UPSTREAM_LLM_OPENROUTER_MODEL") {
		t.Errorf("expected log attr env_var=UPSTREAM_LLM_OPENROUTER_MODEL, got:\n%s", out)
	}
	// Presence-only: the log MUST NOT leak the override VALUE.
	if strings.Contains(out, "qwen/qwen3.5-27b") {
		t.Errorf("log MUST NOT contain the env VALUE; got:\n%s", out)
	}
	// D-06: the word "deprecated" MUST NOT appear in the boot log.
	if strings.Contains(strings.ToLower(out), "deprecated") {
		t.Errorf("log MUST NOT use the word 'deprecated' (D-06 keeps env-overrides supported); got:\n%s", out)
	}
}

// TestLoad_NoLogWhenAllEnvOverridesUnset — D-06: with no UPSTREAM_*_MODEL
// vars set, zero `env override active` lines are emitted.
func TestLoad_NoLogWhenAllEnvOverridesUnset(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase69Plan04(t)
	setAllRequired(t)
	buf := captureDefaultLog(t)
	if _, err := config.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "env override active for upstream") {
		t.Errorf("expected ZERO 'env override active' log lines when all model vars unset; got:\n%s", out)
	}
}

// TestLoad_LogsOneLinePerActiveOverride — D-06: when 2 of 3 vars are set,
// exactly 2 `env override active` log lines are emitted, one per active var.
func TestLoad_LogsOneLinePerActiveOverride(t *testing.T) {
	clearAll(t)
	clearPhase3(t)
	clearPhase69Plan04(t)
	setAllRequired(t)
	buf := captureDefaultLog(t)
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "qwen/qwen3.5-27b")
	t.Setenv("UPSTREAM_EMBED_OPENAI_MODEL", "text-embedding-3-small")
	if _, err := config.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	count := strings.Count(out, "env override active for upstream")
	if count != 2 {
		t.Errorf("expected exactly 2 'env override active' log lines, got %d. Log:\n%s", count, out)
	}
	// Both active envs surface.
	if !strings.Contains(out, "env_var=UPSTREAM_LLM_OPENROUTER_MODEL") {
		t.Errorf("expected UPSTREAM_LLM_OPENROUTER_MODEL surface, got:\n%s", out)
	}
	if !strings.Contains(out, "env_var=UPSTREAM_EMBED_OPENAI_MODEL") {
		t.Errorf("expected UPSTREAM_EMBED_OPENAI_MODEL surface, got:\n%s", out)
	}
	// The unset env MUST NOT appear.
	if strings.Contains(out, "env_var=UPSTREAM_STT_OPENAI_MODEL") {
		t.Errorf("UPSTREAM_STT_OPENAI_MODEL is unset; it MUST NOT surface in the log; got:\n%s", out)
	}
}
