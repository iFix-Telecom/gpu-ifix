//go:build integration

// Phase 23-14 (HAIKU-06) — RES-08 com a cadeia Haiku no roster: chave de
// tenant sensível com o pod desligado recebe 503
// upstream_unavailable_for_sensitive_tenant sem tocar ①/②/③ nem o
// openrouter-tts.
//
// O roster é deixado IGUAL ao de prod pós-23-16 ANTES do request (anthropic-haiku
// e openrouter-deepseek habilitados, url_env presentes, Loader.Refresh) e o
// teste afirma que os externos ESTÃO no tier 1 — sem isso passaria
// trivialmente com os externos fora do roster.
package integration

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

func tier1Names(l *upstreams.Loader, role string) map[string]bool {
	out := map[string]bool{}
	for _, u := range l.ResolveAllTier1(role) {
		out[u.Name] = true
	}
	return out
}

func expectSensitiveBlock(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_unavailable_for_sensitive_tenant") {
		t.Fatalf("body sem o código RES-08: %s", rec.Body.String())
	}
}

func TestSensitiveNeverExternalHaiku(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)
	tenantID, apiKeyID, _ := seedTenantAndKey(t, ctx, pool, "sensitive-haiku", auth.DataClassSensitive)

	// Roster de prod: url_env presentes para todos os upstreams envolvidos
	// (endereços mortos — nenhum proxy real é montado; todos são panic).
	const dead = "http://127.0.0.1:1"
	for _, env := range []string{
		"UPSTREAM_LLM_URL",
		"UPSTREAM_LLM_ANTHROPIC_URL",
		"UPSTREAM_LLM_OPENROUTER_URL",
		"UPSTREAM_TTS_URL",
		"UPSTREAM_TTS_KOKORO_URL",
		"UPSTREAM_TTS_OPENROUTER_URL",
	} {
		t.Setenv(env, dead)
	}
	// Habilita via gen.SetUpstreamEnabled (mesma query do gatewayctl/23-16);
	// a 0041 cria ① e ③ com enabled=false.
	setUpstreamEnabled(t, ctx, pool, "anthropic-haiku", true)
	setUpstreamEnabled(t, ctx, pool, "openrouter-chat", true)
	setUpstreamEnabled(t, ctx, pool, "openrouter-deepseek", true)
	setUpstreamEnabled(t, ctx, pool, "openrouter-tts", true)
	pinAliases(t, ctx, pool, "deepseek-flash")
	resolver := models.NewResolver(pool, discardLogger())
	if err := resolver.Refresh(ctx); err != nil {
		t.Fatalf("resolver refresh: %v", err)
	}

	log := slog.New(slog.NewTextHandler(discardWriter{}, nil))

	llmProxies := func() map[string]http.Handler {
		return map[string]http.Handler{
			"local-llm":           newPanicProxy(t, "pod desligado (breaker OPEN) não pode ser chamado"),
			"anthropic-haiku":     newPanicProxy(t, "① não pode atender tenant sensível (RES-08)"),
			"openrouter-chat":     newPanicProxy(t, "② não pode atender tenant sensível (RES-08)"),
			"openrouter-deepseek": newPanicProxy(t, "③ não pode atender tenant sensível (RES-08)"),
		}
	}
	sensitiveReq := func(path, body string) *http.Request {
		r := makeSensitiveAuthedRequest(t, tenantID, apiKeyID, body, false)
		r.URL.Path = path
		r.RequestURI = path
		return r
	}

	t.Run("pod_breaker_open", func(t *testing.T) {
		loader, err := upstreams.NewLoader(ctx, pool, log)
		if err != nil {
			t.Fatalf("loader: %v", err)
		}
		if err := loader.Refresh(ctx); err != nil {
			t.Fatalf("loader refresh: %v", err)
		}
		llm := tier1Names(loader, "llm")
		for _, n := range []string{"anthropic-haiku", "openrouter-chat", "openrouter-deepseek"} {
			if !llm[n] {
				t.Fatalf("roster tier-1 llm sem %s (%v) — o teste passaria trivialmente", n, llm)
			}
		}
		ttsHasOR := false
		for _, u := range loader.ResolveAllTier1("tts") {
			if u.Name == "openrouter-tts" {
				ttsHasOR = true
			}
		}
		if !ttsHasOR {
			t.Fatalf("roster tier-1 tts sem openrouter-tts — o teste passaria trivialmente")
		}

		bs := breaker.NewSet(rdb, log, breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second}, loader.Names())
		driveBreaker(t, bs, "local-llm", 500, 3)
		driveBreaker(t, bs, "local-tts", 500, 3)

		llmDisp := proxy.NewDispatcher(proxy.DispatcherConfig{
			Role: "llm", Loader: loader, Breaker: bs, Proxies: llmProxies(), Pins: resolver, Log: log,
		})
		// Alias pinado na cadeia (prod) — dispatchPinned.
		rec := httptest.NewRecorder()
		llmDisp.ServeHTTP(rec, sensitiveReq("/v1/chat/completions",
			`{"model":"deepseek-flash","messages":[{"role":"user","content":"cpf 123"}]}`))
		expectSensitiveBlock(t, rec)
		// Modelo sem pino, streaming — caminho de breaker OPEN (fail-fast).
		rec = httptest.NewRecorder()
		llmDisp.ServeHTTP(rec, sensitiveReq("/v1/chat/completions",
			`{"model":"modelo-sem-pino","stream":true,"messages":[{"role":"user","content":"cpf 123"}]}`))
		expectSensitiveBlock(t, rec)

		ttsDisp := proxy.NewDispatcher(proxy.DispatcherConfig{
			Role: "tts", Loader: loader, Breaker: bs,
			Proxies: map[string]http.Handler{
				"local-tts":      newPanicProxy(t, "pod desligado (breaker OPEN) não pode ser chamado"),
				"kokoro-tts":     newPanicProxy(t, "tier-1 tts não pode atender tenant sensível"),
				"openrouter-tts": newPanicProxy(t, "openrouter-tts não pode atender tenant sensível (RES-08)"),
			},
			Pins: resolver, Log: log,
		})
		rec = httptest.NewRecorder()
		ttsDisp.ServeHTTP(rec, sensitiveReq("/v1/audio/speech",
			`{"model":"tts-1","input":"dado sensível","voice":"pm_alex"}`))
		expectSensitiveBlock(t, rec)
	})

	t.Run("local_llm_desabilitado", func(t *testing.T) {
		setUpstreamEnabled(t, ctx, pool, "local-llm", false)
		loader, err := upstreams.NewLoader(ctx, pool, log)
		if err != nil {
			t.Fatalf("loader: %v", err)
		}
		if llm := tier1Names(loader, "llm"); !llm["anthropic-haiku"] || !llm["openrouter-chat"] || !llm["openrouter-deepseek"] {
			t.Fatalf("roster tier-1 llm incompleto: %v", llm)
		}
		bs := breaker.NewSet(rdb, log, breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second}, loader.Names())
		llmDisp := proxy.NewDispatcher(proxy.DispatcherConfig{
			Role: "llm", Loader: loader, Breaker: bs, Proxies: llmProxies(), Pins: resolver, Log: log,
		})
		rec := httptest.NewRecorder()
		llmDisp.ServeHTTP(rec, sensitiveReq("/v1/chat/completions",
			`{"model":"deepseek-flash","messages":[{"role":"user","content":"cpf 123"}]}`))
		expectSensitiveBlock(t, rec)
	})
}
