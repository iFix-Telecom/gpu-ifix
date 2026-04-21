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
