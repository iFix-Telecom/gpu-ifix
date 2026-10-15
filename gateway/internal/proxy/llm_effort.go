// Package proxy (llm_effort.go): política única de effort/thinking do LLM
// (Phase 23, HAIKU-05), compartilhada pelo passo ① (Anthropic direto, 23-12)
// e pelo passo ② (OpenRouter, PASS 6 do openrouter_director.go).
//
// Precedência travada no 23-CONTEXT (Pedro): "High por padrão quando o
// cliente não especifica. Se o request pedir explicitamente off/low/medium,
// RESPEITAR. Override por tenant disponível (tenant > padrão)." Isto
// SUBSTITUI o "cliente ignorado" da tabela de testes do 23-RESEARCH.
//
//	cliente explícito (válido) > tenants.llm_effort (válido) > DefaultLLMEffort
//
// Valores fora da allowlist (cliente ou tenant) são ignorados e caem para o
// próximo nível (T-23-28).
package proxy

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
)

// TenantEffortLookup resolve o override de effort do tenant (tenants.llm_effort).
// Implementado por *tenants.Loader; "" = sem override.
type TenantEffortLookup interface {
	LLMEffortByTenantID(tenantID string) string
}

// DefaultLLMEffort é o effort aplicado quando nem o cliente nem o tenant
// especificam (decisão do CONTEXT: high por padrão).
const DefaultLLMEffort = "high"

// validLLMEfforts é a allowlist (mesma da CHECK da coluna tenants.llm_effort, 0041).
var validLLMEfforts = map[string]bool{"off": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// Fontes devolvidas por ResolveLLMEffort.
const (
	LLMEffortSourceClient  = "client"
	LLMEffortSourceTenant  = "tenant"
	LLMEffortSourceDefault = "default"
)

// ResolveLLMEffort devolve o effort efetivo e a fonte ("client"|"tenant"|"default").
//
// Cliente, nessa ordem: `reasoning_effort` string válida; senão
// `reasoning.effort` válida; senão `reasoning.enabled == false` → "off".
// O tenant vem de auth.FromContext(ctx) + lookup (nil-safe).
func ResolveLLMEffort(ctx context.Context, body map[string]json.RawMessage, lookup TenantEffortLookup) (effort string, source string) {
	if e, ok := clientLLMEffort(body); ok {
		return e, LLMEffortSourceClient
	}
	if lookup != nil {
		if ac, ok := auth.FromContext(ctx); ok {
			if e := lookup.LLMEffortByTenantID(ac.TenantID); validLLMEfforts[e] {
				return e, LLMEffortSourceTenant
			}
		}
	}
	return DefaultLLMEffort, LLMEffortSourceDefault
}

// clientLLMEffort extrai o pedido explícito do cliente, se houver e for válido.
func clientLLMEffort(body map[string]json.RawMessage) (string, bool) {
	if raw, ok := body["reasoning_effort"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil && validLLMEfforts[s] {
			return s, true
		}
	}
	raw, ok := body["reasoning"]
	if !ok {
		return "", false
	}
	var r struct {
		Effort  *string `json:"effort"`
		Enabled *bool   `json:"enabled"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", false
	}
	if r.Effort != nil && validLLMEfforts[*r.Effort] {
		return *r.Effort, true
	}
	if r.Enabled != nil && !*r.Enabled {
		return "off", true
	}
	return "", false
}

// OpenRouterThinkingHeadroom é a folga somada ao max_tokens do cliente
// quando o raciocínio do Haiku pode rodar (mesmo valor do ThinkingHeadroom
// do passo ①).
const OpenRouterThinkingHeadroom = 4096

// applyAnthropicEffort é o PASS 6 do director do OpenRouter: para alvo
// Anthropic, troca os campos reasoning do cliente pelo `reasoning` resolvido
// ({"enabled":false} para off, {"effort":<e>} nos demais). Alvo não-Anthropic
// ou body inválido → bytes intactos. Devolve também effort/source para log
// (vazios quando não aplicou).
func applyAnthropicEffort(ctx context.Context, body []byte, lookup TenantEffortLookup) ([]byte, string, string) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body, "", ""
	}
	var model string
	if err := json.Unmarshal(m["model"], &model); err != nil || !isAnthropicTarget(model) {
		return body, "", ""
	}
	effort, source := ResolveLLMEffort(ctx, m, lookup)
	var reasoning []byte
	if effort == "off" {
		reasoning = []byte(`{"enabled":false}`)
	} else {
		rb, err := json.Marshal(map[string]string{"effort": effort})
		if err != nil {
			return body, "", ""
		}
		reasoning = rb
	}
	delete(m, "reasoning_effort")
	m["reasoning"] = reasoning
	// Thinking conta dentro do limite de saída: sem folga, um max_tokens
	// baixo do cliente (campanhas ~500) é consumido pelo raciocínio e a
	// resposta sai cortada ou vazia (medido em prod 2026-10-15). Mesma
	// regra do passo ①: soma folga só quando o raciocínio pode rodar.
	if effort != "off" && effort != "low" {
		for _, k := range []string{"max_completion_tokens", "max_tokens"} {
			var n int
			if err := json.Unmarshal(m[k], &n); err == nil && n > 0 {
				m[k] = json.RawMessage(strconv.Itoa(n + OpenRouterThinkingHeadroom))
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body, "", ""
	}
	return out, effort, source
}
