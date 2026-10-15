// Package proxy (openrouter_director.go): Director extension for the
// openrouter-chat upstream. Wraps BuildDirector by injecting the
// `Authorization: Bearer <key>` header AND rewriting the
// /v1/chat/completions request body to:
//
//  1. Rewrite "model" alias to the per-upstream target via the resolver
//     (Phase 06.9 — closes OR-FIX). The env-override-wins precedence per
//     D-06 lives inside Resolver.Resolve (Plan 02). When
//     UPSTREAM_LLM_OPENROUTER_MODEL is set, models.RewriteJSONModel returns
//     the env-overridden target transparently; this director contains no
//     separate env-read logic.
//  2. Inject {"provider":{"order":[...],"allow_fallbacks":<bool>}} per
//     CONTEXT.md D-C2 / 03-WAVE0-GATES.md (D-C1 amendment: Novita pin).
//  3. Inject {"stream_options":{"include_usage":true}} when client asked
//     for streaming (Pitfall 5 — usage chunks for cost attribution).
//
// R5 honesty: this director performs THREE sequential JSON unmarshal+marshal
// passes (RewriteJSONModel → injectProviderOrder → injectStreamOptionsIncludeUsage).
// Each pass is well-scoped and idempotent. A future refactor may combine
// into a single helper if profiling identifies allocation pressure; for
// now the multi-pass shape is honest and explicit.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// TenantPrefsLookup resolves a tenant's OpenRouter provider_prefs by tenant
// id (quick 260830-o2j). Implemented by tenants.Loader. nil-safe at the
// call site — a nil lookup means "no tenant layer".
type TenantPrefsLookup interface {
	ProviderPrefsByTenantID(tenantID string) []byte
}

// BuildOpenRouterDirector returns a Director for the openrouter-chat
// upstream. Extends proxy.BuildDirector by:
//
//   - injecting `Authorization: Bearer <authBearer>` header from the
//     resolved upstreams.UpstreamConfig.AuthBearer (env-resolved at
//     loader.Refresh time),
//   - rewriting the /v1/chat/completions JSON body's "model" alias via
//     resolver.Resolve(alias, upstreamName) — env-override-wins per D-06
//     handled transparently inside Resolver.Resolve, and
//   - injecting {"provider":{"order":[...],"allow_fallbacks":<bool>}} +
//     {"stream_options":{"include_usage":true}} (when streaming).
//
// Non-chat paths (e.g. an /v1/embeddings request mistakenly routed
// through this director) pass through unchanged — the body is left as-is.
//
// providerOrder MUST be the resolved order list (e.g. ["novita"]); the
// caller resolves UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER via config.
// allowFallbacks reflects UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS (default
// false per D-C2 / D-C4 — no fallback of fallback for chat).
//
// resolver + upstreamName drive the per-upstream model rewrite. The
// upstreamName is compile-time-known at the wire-up site in main.go
// (always "openrouter-chat" for this director). The env-override-wins
// precedence per D-06 lives inside Resolver.Resolve — see Plan 02 /
// resolver.go for the env→schema→passthrough chain.
//
// log is used for R12 director error logging — DEBUG/WARN classes on
// rewrite failures, never logs the request body (sensitive).
func BuildOpenRouterDirector(
	upstream *url.URL,
	authBearer string,
	providerOrder []string,
	allowFallbacks bool,
	resolver *models.Resolver,
	upstreamName string,
	log *slog.Logger,
	tenantPrefs TenantPrefsLookup,
) func(*http.Request) {
	base := BuildDirector(upstream)
	return func(r *http.Request) {
		base(r) // strips client auth, propagates X-Request-ID, rewrites URL
		if authBearer != "" {
			r.Header.Set("Authorization", "Bearer "+authBearer)
		}
		// HasSuffix (not equality) so the check survives BuildDirector's
		// upstream.Path join — for `https://openrouter.ai/api`, the rewritten
		// path becomes `/api/v1/chat/completions`. The defensive intent is
		// "only run body rewrite on chat requests"; suffix match preserves
		// that semantic across upstream prefixes.
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			return
		}
		if r.Body == nil {
			return
		}
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil || len(body) == 0 {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}

		// PASS 1 — model rewrite via resolver (Phase 06.9 OR-FIX).
		// RewriteJSONModel is best-effort: on parse error it returns the
		// original body bytes; on resolver miss it returns the original
		// body bytes too (alias unchanged). Either way the body is safe
		// to feed into PASS 2.
		rewritten, _, err1 := models.RewriteJSONModel(body, resolver, upstreamName)
		if err1 != nil {
			// R12: DEBUG log — parse failure is non-fatal; downstream may
			// still 4xx, which the breaker's IsSuccessful filter classifies
			// as non-failure. Do NOT log the body (sensitive).
			if log != nil {
				log.Debug("openrouter_director model rewrite parse failed",
					"upstream", upstreamName,
					"error_class", fmt.Sprintf("%T", err1),
				)
			}
			rewritten = body
		}

		// PASS 2 — provider injection. quick 260830-o2j precedence (Pedro,
		// 2026-08-30): TENANT provider_prefs > (alias, openrouter-chat) row
		// prefs > legacy global env pin (D-C2 order + allow_fallbacks). A
		// resolved prefs object is injected VERBATIM as the whole `provider`
		// field; the client-supplied `provider` never wins.
		var patched []byte
		var err2 error
		if prefs := resolveProviderPrefs(r.Context(), tenantPrefs, resolver, modelAliasFromBody(body), upstreamName); prefs != nil {
			patched, err2 = injectProviderPrefs(rewritten, prefs)
		} else {
			patched, err2 = injectProviderOrder(rewritten, providerOrder, allowFallbacks)
		}
		if err2 != nil {
			// R12: WARN log — provider.order injection is the load-bearing
			// pin; failing means the request goes to OpenRouter without
			// our provider preference. Still best-effort forwarding —
			// breaker filters 4xx, and the upstream may still succeed.
			if log != nil {
				log.Warn("openrouter_director injectProviderOrder failed",
					"upstream", upstreamName,
					"error_class", fmt.Sprintf("%T", err2),
				)
			}
			// Parse failure → leave body unchanged. The upstream may still
			// 4xx the request; the breaker's IsSuccessful filter (D-A4)
			// classifies 4xx as non-failure so the breaker stays CLOSED.
			rewriteRequestBody(r, rewritten)
			return
		}

		// PASS 3 — stream_options.include_usage injection (Pitfall 5).
		// OpenRouter mirrors OpenAI: the final {"usage":...} chunk is
		// omitted on streaming responses unless stream_options.include_usage
		// is set. Inject defensively when the client asked to stream but
		// did NOT set the option themselves.
		patched = injectStreamOptionsIncludeUsage(patched)

		// PASS 4 — prompt caching para alvos Anthropic (Phase 23, 2026-10-13).
		// Anthropic só cacheia com breakpoint explícito: sem cache_control o
		// prompt inteiro é cobrado cheio a cada chamada. Medido via OpenRouter
		// (Haiku 5.5, prompt ~10k): leitura de cache custa ~10x menos que a
		// entrada normal e o OpenRouter mantém o provedor (sticky).
		patched = injectAnthropicCacheControl(patched)

		// PASS 5 — normalização Anthropic (Phase 23, HAIKU-01). Só para alvo
		// resolvido Anthropic (o `model` já foi reescrito no PASS 1):
		//   - prefill: conversa terminando em assistant sem tool_calls dá 400
		//     "does not support assistant message prefill" em produção (F5 do
		//     23-RESEARCH) → anexa user neutro "Prossiga.";
		//   - sampling: temperature/top_p/top_k/frequency_penalty/
		//     presence_penalty dão 400 no Haiku 5.5 (migration guide) → removidos.
		// Ordem DEPOIS do PASS 4 (revisão cross-AI 2026-10-14): o breakpoint de
		// conversa cai no último turno real, que volta igual no request seguinte
		// da mesma conversa; se rodasse antes, cairia no "Prossiga." sintético,
		// que nunca volta, e o prefixo não seria reaproveitado. A remoção de
		// sampling mexe só em campos top-level, sem efeito no PASS 4.
		patched = normalizeAnthropicChatBody(patched)

		r.Body = io.NopCloser(bytes.NewReader(patched))
		r.ContentLength = int64(len(patched))
		r.Header.Set("Content-Length", strconv.Itoa(len(patched)))
	}
}

// injectProviderOrder merges {"provider":{"order":...,"allow_fallbacks":...}}
// into an existing JSON request body, preserving other fields by
// unmarshaling into map[string]json.RawMessage (raw bytes survive). If
// the caller already set "provider" we overwrite it — gateway-injected
// pin always wins over client-supplied.
func injectProviderOrder(body []byte, order []string, allowFallbacks bool) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	providerBytes, err := json.Marshal(map[string]any{
		"order":           order,
		"allow_fallbacks": allowFallbacks,
	})
	if err != nil {
		return nil, err
	}
	m["provider"] = providerBytes
	return json.Marshal(m)
}

// injectProviderPrefs replaces the request's `provider` object with the
// stored per-model prefs (already validated at write time by
// models.ValidateProviderPrefs). Client-supplied `provider` is overwritten —
// gateway policy always wins, same as injectProviderOrder.
func injectProviderPrefs(body []byte, prefs []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m["provider"] = json.RawMessage(prefs)
	return json.Marshal(m)
}

// modelAliasFromBody extracts the ORIGINAL (pre-rewrite) `model` string so
// the per-model prefs lookup keys on the alias the client sent, not on the
// rewritten target. Returns "" on any parse miss (→ no prefs → legacy path).
func modelAliasFromBody(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	return m.Model
}

// resolveProviderPrefs applies the tenant > alias precedence. The tenant id
// comes from the auth context stamped by auth.Middleware; an unauthenticated
// request (tests, internal probes) simply skips the tenant layer.
func resolveProviderPrefs(ctx context.Context, tenantPrefs TenantPrefsLookup, resolver *models.Resolver, alias, upstreamName string) []byte {
	if tenantPrefs != nil {
		if ac, ok := auth.FromContext(ctx); ok {
			if p := tenantPrefs.ProviderPrefsByTenantID(ac.TenantID); p != nil {
				return p
			}
		}
	}
	if resolver == nil || alias == "" {
		return nil
	}
	return resolver.ProviderPrefs(alias, upstreamName)
}

// injectAnthropicCacheControl marca dois breakpoints de prompt caching quando
// o modelo (já reescrito) é Anthropic: o fim do último bloco de texto da
// ÚLTIMA mensagem system (cacheia tools+system, reaproveitado entre conversas)
// e o fim do último bloco de texto da última mensagem user/assistant (cacheia
// o histórico, reaproveitado no turno seguinte da mesma conversa). Conteúdo
// string vira [{type:text,text,cache_control}]. Cliente que já mandou algum
// cache_control decide sozinho — não mexemos. Qualquer falha de parse devolve
// o body intacto (best-effort, nunca quebra a requisição).
func injectAnthropicCacheControl(body []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	var model string
	if err := json.Unmarshal(m["model"], &model); err != nil || !strings.HasPrefix(model, "anthropic/") {
		return body
	}
	if bytes.Contains(body, []byte(`"cache_control"`)) {
		return body
	}
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(m["messages"], &msgs); err != nil || len(msgs) == 0 {
		return body
	}
	// De trás pra frente: a primeira system/developer e a primeira
	// user/assistant com bloco de texto marcável (assistant só com
	// tool_calls tem content null e é pulado).
	changed, doneSystem, doneConv := false, false, false
	for i := len(msgs) - 1; i >= 0 && !(doneSystem && doneConv); i-- {
		var role string
		_ = json.Unmarshal(msgs[i]["role"], &role)
		isSystem := role == "system" || role == "developer"
		isConv := role == "user" || role == "assistant"
		if (isSystem && doneSystem) || (isConv && doneConv) || (!isSystem && !isConv) {
			continue
		}
		c, ok := markLastTextBlock(msgs[i]["content"])
		if !ok {
			continue
		}
		msgs[i]["content"] = c
		changed = true
		if isSystem {
			doneSystem = true
		} else {
			doneConv = true
		}
	}
	if !changed {
		return body
	}
	mb, err := json.Marshal(msgs)
	if err != nil {
		return body
	}
	m["messages"] = mb
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// markLastTextBlock devolve o content com cache_control no último bloco de
// texto. Aceita string (vira um bloco) ou array de blocos; qualquer outro
// formato, string vazia ou array sem bloco de texto → ok=false.
func markLastTextBlock(raw json.RawMessage) (json.RawMessage, bool) {
	cc := json.RawMessage(`{"type":"ephemeral"}`)
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if str == "" {
			return nil, false
		}
		out, err := json.Marshal([]map[string]any{{"type": "text", "text": str, "cache_control": cc}})
		return out, err == nil
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, false
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		var typ string
		_ = json.Unmarshal(blocks[i]["type"], &typ)
		if typ != "text" {
			continue
		}
		blocks[i]["cache_control"] = cc
		out, err := json.Marshal(blocks)
		return out, err == nil
	}
	return nil, false
}
