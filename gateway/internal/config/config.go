// Package config loads runtime configuration for the gateway and the
// gatewayctl CLI from environment variables. Load is called once at
// startup; the returned Config is immutable for the lifetime of the
// process (CONTEXT.md D-D3 Plumbing + cobrancas-api src/config.ts pattern).
package config

import (
	"errors"
	"fmt"
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
	UpstreamSTTURL          string // UPSTREAM_STT_URL (required)
	UpstreamEmbedURL        string // UPSTREAM_EMBED_URL (required)
	UpstreamHealthBridgeURL string // UPSTREAM_HEALTH_BRIDGE_URL (required)

	// Phase 3 — External fallback upstreams (optional at boot; warn-log if a
	// row in ai_gateway.upstreams is enabled but the env it points to is missing)
	UpstreamOpenRouterChatURL        string   // UPSTREAM_LLM_OPENROUTER_URL
	UpstreamOpenRouterChatAuthBearer string   // UPSTREAM_LLM_OPENROUTER_AUTH_BEARER
	UpstreamOpenRouterProviderOrder  []string // UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER (CSV; default ["fireworks"])
	UpstreamOpenRouterAllowFallbacks bool     // UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS (default false)
	UpstreamOpenAIWhisperURL         string   // UPSTREAM_STT_OPENAI_URL
	UpstreamOpenAIWhisperAuthBearer  string   // UPSTREAM_STT_OPENAI_AUTH_BEARER
	UpstreamOpenAIEmbedURL           string   // UPSTREAM_EMBED_OPENAI_URL
	UpstreamOpenAIEmbedAuthBearer    string   // UPSTREAM_EMBED_OPENAI_AUTH_BEARER

	// Phase 3 — Probe + breaker tuning (CONTEXT.md D-A2 + D-A3)
	ProbeIntervalSeconds       int // PROBE_INTERVAL_SECONDS (default 10)
	ProbeBudgetSeconds         int // PROBE_BUDGET_SECONDS (default 5)
	BreakerConsecutiveFailures int // BREAKER_CONSECUTIVE_FAILURES (default 3)
	BreakerCooldownSeconds     int // BREAKER_COOLDOWN_SECONDS (default 30)

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
}

// ErrMissingEnv is returned by Load when one or more required env vars are unset.
var ErrMissingEnv = errors.New("config: required environment variable not set")

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

		// Phase 3 external upstreams (optional at boot)
		UpstreamOpenRouterChatURL:        os.Getenv("UPSTREAM_LLM_OPENROUTER_URL"),
		UpstreamOpenRouterChatAuthBearer: os.Getenv("UPSTREAM_LLM_OPENROUTER_AUTH_BEARER"),
		UpstreamOpenRouterProviderOrder:  csvOr(os.Getenv("UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER"), []string{"fireworks"}),
		UpstreamOpenRouterAllowFallbacks: boolOr(os.Getenv("UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS"), false),
		UpstreamOpenAIWhisperURL:         os.Getenv("UPSTREAM_STT_OPENAI_URL"),
		UpstreamOpenAIWhisperAuthBearer:  os.Getenv("UPSTREAM_STT_OPENAI_AUTH_BEARER"),
		UpstreamOpenAIEmbedURL:           os.Getenv("UPSTREAM_EMBED_OPENAI_URL"),
		UpstreamOpenAIEmbedAuthBearer:    os.Getenv("UPSTREAM_EMBED_OPENAI_AUTH_BEARER"),

		ProbeIntervalSeconds:       atoiOr(os.Getenv("PROBE_INTERVAL_SECONDS"), 10),
		ProbeBudgetSeconds:         atoiOr(os.Getenv("PROBE_BUDGET_SECONDS"), 5),
		BreakerConsecutiveFailures: atoiOr(os.Getenv("BREAKER_CONSECUTIVE_FAILURES"), 3),
		BreakerCooldownSeconds:     atoiOr(os.Getenv("BREAKER_COOLDOWN_SECONDS"), 30),

		WriteTimeoutChat:  time.Duration(atoiOr(os.Getenv("WRITE_TIMEOUT_CHAT_SECONDS"), 0)) * time.Second,
		WriteTimeoutEmbed: time.Duration(atoiOr(os.Getenv("WRITE_TIMEOUT_EMBED_SECONDS"), 30)) * time.Second,
		WriteTimeoutAudio: time.Duration(atoiOr(os.Getenv("WRITE_TIMEOUT_AUDIO_SECONDS"), 120)) * time.Second,

		SentryDSN: os.Getenv("SENTRY_DSN"),
		LogLevel:  envOr("LOG_LEVEL", "info"),
		Env:       envOr("ENV", "production"),

		BootstrapTenantSlug: envOr("BOOTSTRAP_TENANT_SLUG", "converseai"),
	}

	// Iterate in a fixed order so error messages are deterministic — tests
	// assert specific var names in a stable string, and ops appreciate
	// predictable output.
	requiredOrder := []string{
		"AI_GATEWAY_PG_DSN",
		"AI_GATEWAY_REDIS_ADDR",
		"UPSTREAM_LLM_URL",
		"UPSTREAM_STT_URL",
		"UPSTREAM_EMBED_URL",
		"UPSTREAM_HEALTH_BRIDGE_URL",
	}
	required := map[string]string{
		"AI_GATEWAY_PG_DSN":          cfg.PGDSN,
		"AI_GATEWAY_REDIS_ADDR":      cfg.RedisAddr,
		"UPSTREAM_LLM_URL":           cfg.UpstreamLLMURL,
		"UPSTREAM_STT_URL":           cfg.UpstreamSTTURL,
		"UPSTREAM_EMBED_URL":         cfg.UpstreamEmbedURL,
		"UPSTREAM_HEALTH_BRIDGE_URL": cfg.UpstreamHealthBridgeURL,
	}
	var missing []string
	for _, name := range requiredOrder {
		if strings.TrimSpace(required[name]) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("%w: %s", ErrMissingEnv, strings.Join(missing, ", "))
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
