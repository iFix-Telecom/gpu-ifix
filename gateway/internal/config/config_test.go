// Package config_test exercises Load() against env var fixtures.
package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
)

// allRequired are the six env vars Load() insists on.
var allRequired = []string{
	"AI_GATEWAY_PG_DSN",
	"AI_GATEWAY_REDIS_ADDR",
	"UPSTREAM_LLM_URL",
	"UPSTREAM_STT_URL",
	"UPSTREAM_EMBED_URL",
	"UPSTREAM_HEALTH_BRIDGE_URL",
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
		cfg.UpstreamSTTURL == "" || cfg.UpstreamEmbedURL == "" ||
		cfg.UpstreamHealthBridgeURL == "" {
		t.Fatalf("expected populated required fields, got %+v", cfg)
	}
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

// TestLoad_Phase3Defaults verifies that with only the 6 Phase-2 required
// vars set, Load returns the documented Plan-03-02 defaults: probe
// 10s/5s, breaker 3 failures / 30s cooldown, per-route WriteTimeout
// 0/30s/120s for chat/embed/audio (Folded Todo from CONTEXT.md), and
// OpenRouter provider order ['fireworks'] with allow_fallbacks=false.
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
		cfg.UpstreamOpenRouterProviderOrder[0] != "fireworks" {
		t.Errorf("UpstreamOpenRouterProviderOrder = %v, want [fireworks]",
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
	setAllRequired(t) // Only the 6 Phase 2 required vars.
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
