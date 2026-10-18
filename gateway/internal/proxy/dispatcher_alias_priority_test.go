// dispatcher_alias_priority_test.go — quick 261018: ordem dos candidatos de
// um alias pinado por model_aliases.priority.
//
// Contrato testado:
//   - alias SEM priority em nenhuma linha → ordem global (tier, tier_priority),
//     idêntica à de antes da coluna;
//   - alias com priority → priority ASC, nulos por último, empate pela ordem
//     global;
//   - RES-08 intacto: tenant sensitive nunca chega a um tier>=1, mesmo que o
//     externo tenha a menor priority;
//   - pinner sem PinnedPriorities (interface opcional) → ordem global.
package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
)

// prioPinner implementa ModelPinner + ModelPriorities.
type prioPinner struct {
	pins map[string][]string
	prio map[string]map[string]int
}

func (p prioPinner) PinnedUpstreams(alias string) []string { return p.pins[alias] }
func (p prioPinner) PinnedPriorities(alias string) map[string]int {
	return p.prio[alias]
}

// recordOrder envolve os proxies do fixture para registrar a ORDEM em que o
// dispatcher tentou cada upstream. O mapa é compartilhado por referência com
// o DispatcherConfig já montado, então o mux enxerga os wrappers.
func recordOrder(f *statusCascadeFixture) *[]string {
	var mu sync.Mutex
	order := &[]string{}
	for name, h := range f.cfg.Proxies {
		name, h := name, h
		f.cfg.Proxies[name] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			*order = append(*order, name)
			mu.Unlock()
			h.ServeHTTP(w, r)
		})
	}
	return order
}

func equalOrder(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Espelho do cenário de prod: haiku prio0, openrouter-chat prio10,
// openrouter-deepseek prio20 (todos tier 1). Todos respondem 503 para a
// cascata percorrer o roster inteiro e a ordem ficar visível.
func allFailingExternals() []upSpec {
	return []upSpec{
		{name: "anthropic-haiku", tier: 1, prio: 0, status: 503, body: `{"error":{"message":"haiku"}}`},
		{name: "openrouter-chat", tier: 1, prio: 10, status: 503, body: `{"error":{"message":"orchat"}}`},
		{name: "openrouter-deepseek", tier: 1, prio: 20, status: 503, body: `{"error":{"message":"deepseek"}}`},
	}
}

var jevAliasPins = map[string][]string{
	"m": {"anthropic-haiku", "openrouter-chat", "openrouter-deepseek"},
}

// Sem priority em nenhuma linha → ordem global (comportamento pré-261018).
func TestDispatchPinned_NoPriorityKeepsGlobalOrder(t *testing.T) {
	f := newStatusCascadeFixture(t, prioPinner{pins: jevAliasPins}, allFailingExternals()...)
	order := recordOrder(f)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if !equalOrder(*order, "anthropic-haiku", "openrouter-chat", "openrouter-deepseek") {
		t.Fatalf("order = %v, want global tier_priority order", *order)
	}
}

// Pinner antigo (sem PinnedPriorities) → ordem global, nada quebra.
func TestDispatchPinned_PinnerWithoutPrioritiesKeepsGlobalOrder(t *testing.T) {
	f := newStatusCascadeFixture(t, stubPinner(jevAliasPins), allFailingExternals()...)
	order := recordOrder(f)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if !equalOrder(*order, "anthropic-haiku", "openrouter-chat", "openrouter-deepseek") {
		t.Fatalf("order = %v, want global order", *order)
	}
}

// Todas com priority → priority ASC, independente de tier_priority.
func TestDispatchPinned_PriorityReordersRoster(t *testing.T) {
	pins := prioPinner{pins: jevAliasPins, prio: map[string]map[string]int{
		"m": {"openrouter-deepseek": 1, "openrouter-chat": 2, "anthropic-haiku": 3},
	}}
	f := newStatusCascadeFixture(t, pins, allFailingExternals()...)
	order := recordOrder(f)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if !equalOrder(*order, "openrouter-deepseek", "openrouter-chat", "anthropic-haiku") {
		t.Fatalf("order = %v, want priority ASC", *order)
	}
	// Exaustão devolve o último erro real = o do último tentado (haiku).
	if rw.Code != 503 || rw.Body.String() != `{"error":{"message":"haiku"}}` {
		t.Fatalf("got %d %s, want haiku's 503 as last error", rw.Code, rw.Body.String())
	}
}

// Só uma linha com priority → ela primeiro; nulos depois na ordem global.
func TestDispatchPinned_PriorityNullsLastGlobalTiebreak(t *testing.T) {
	pins := prioPinner{pins: jevAliasPins, prio: map[string]map[string]int{
		"m": {"openrouter-deepseek": 5},
	}}
	f := newStatusCascadeFixture(t, pins, allFailingExternals()...)
	order := recordOrder(f)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if !equalOrder(*order, "openrouter-deepseek", "anthropic-haiku", "openrouter-chat") {
		t.Fatalf("order = %v, want deepseek then NULLs in global order", *order)
	}
}

// Priorities iguais → empate pela ordem global.
func TestDispatchPinned_PriorityTieUsesGlobalOrder(t *testing.T) {
	pins := prioPinner{pins: jevAliasPins, prio: map[string]map[string]int{
		"m": {"openrouter-deepseek": 1, "openrouter-chat": 1, "anthropic-haiku": 9},
	}}
	f := newStatusCascadeFixture(t, pins, allFailingExternals()...)
	order := recordOrder(f)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if !equalOrder(*order, "openrouter-chat", "openrouter-deepseek", "anthropic-haiku") {
		t.Fatalf("order = %v, want tie broken by tier_priority", *order)
	}
}

// Priority é por ALIAS: outro alias com os mesmos upstreams e sem priority
// segue a ordem global.
func TestDispatchPinned_PriorityIsPerAlias(t *testing.T) {
	pins := prioPinner{
		pins: map[string][]string{
			"m":     jevAliasPins["m"],
			"other": jevAliasPins["m"],
		},
		prio: map[string]map[string]int{"m": {"openrouter-deepseek": 1}},
	}
	f := newStatusCascadeFixture(t, pins, allFailingExternals()...)
	order := recordOrder(f)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"other"}`, auth.DataClassNormal))
	if !equalOrder(*order, "anthropic-haiku", "openrouter-chat", "openrouter-deepseek") {
		t.Fatalf("order = %v, want global order for alias without priority", *order)
	}
}

// Serve no primeiro que responder 200 na ordem por priority.
func TestDispatchPinned_PriorityServesFirstHealthy(t *testing.T) {
	pins := prioPinner{pins: jevAliasPins, prio: map[string]map[string]int{
		"m": {"openrouter-chat": 1, "anthropic-haiku": 2},
	}}
	f := newStatusCascadeFixture(t, pins,
		upSpec{name: "anthropic-haiku", tier: 1, prio: 0, status: 200, body: `{"upstream":"haiku"}`},
		upSpec{name: "openrouter-chat", tier: 1, prio: 10, status: 200, body: `{"upstream":"orchat"}`},
		upSpec{name: "openrouter-deepseek", tier: 1, prio: 20, status: 200, body: `{"upstream":"deepseek"}`},
	)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if rw.Code != 200 || rw.Body.String() != `{"upstream":"orchat"}` {
		t.Fatalf("got %d %s, want orchat", rw.Code, rw.Body.String())
	}
	if f.hit("anthropic-haiku") != 0 {
		t.Fatalf("haiku hit %d times, want 0", f.hit("anthropic-haiku"))
	}
}

// Tier-0 pinado também entra na ordenação (chave = nome da linha do alias).
func TestDispatchPinned_PriorityCanPutExternalBeforeTier0(t *testing.T) {
	pins := prioPinner{
		pins: map[string][]string{"m": {"local-llm", "openrouter-chat"}},
		prio: map[string]map[string]int{"m": {"openrouter-chat": 1, "local-llm": 2}},
	}
	f := newStatusCascadeFixture(t, pins,
		upSpec{name: "local-llm", tier: 0, prio: 0, status: 503, body: `{"error":{"message":"pod"}}`},
		upSpec{name: "openrouter-chat", tier: 1, prio: 10, status: 503, body: `{"error":{"message":"orchat"}}`},
	)
	order := recordOrder(f)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if !equalOrder(*order, "openrouter-chat", "local-llm") {
		t.Fatalf("order = %v, want external first by priority", *order)
	}
}

// RES-08: externo com a menor priority NÃO é tentado para tenant sensitive;
// o tier-0 serve.
func TestDispatchPinned_PrioritySensitiveNeverExternal(t *testing.T) {
	pins := prioPinner{
		pins: map[string][]string{"m": {"local-llm", "openrouter-chat"}},
		prio: map[string]map[string]int{"m": {"openrouter-chat": 1, "local-llm": 2}},
	}
	f := newStatusCascadeFixture(t, pins,
		upSpec{name: "local-llm", tier: 0, prio: 0, status: 200, body: `{"upstream":"pod"}`},
		upSpec{name: "openrouter-chat", tier: 1, prio: 10, status: 200, body: `{"upstream":"orchat"}`},
	)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassSensitive))
	if rw.Code != 200 || rw.Body.String() != `{"upstream":"pod"}` {
		t.Fatalf("got %d %s, want tier-0 for sensitive tenant", rw.Code, rw.Body.String())
	}
	if f.hit("openrouter-chat") != 0 {
		t.Fatalf("external hit %d times for sensitive tenant (RES-08)", f.hit("openrouter-chat"))
	}
}

// RES-08: roster só externo + priority → sensitive continua recebendo o 503
// de bloqueio, nenhum externo chamado.
func TestDispatchPinned_PrioritySensitiveExternalOnlyBlocks(t *testing.T) {
	pins := prioPinner{pins: jevAliasPins, prio: map[string]map[string]int{
		"m": {"openrouter-deepseek": 1},
	}}
	f := newStatusCascadeFixture(t, pins,
		upSpec{name: "anthropic-haiku", tier: 1, prio: 0, status: 200, body: `{}`},
		upSpec{name: "openrouter-chat", tier: 1, prio: 10, status: 200, body: `{}`},
		upSpec{name: "openrouter-deepseek", tier: 1, prio: 20, status: 200, body: `{}`},
	)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassSensitive))
	if rw.Code != 503 {
		t.Fatalf("status = %d, want 503 sensitive block", rw.Code)
	}
	for _, n := range []string{"anthropic-haiku", "openrouter-chat", "openrouter-deepseek"} {
		if f.hit(n) != 0 {
			t.Fatalf("%s hit %d times for sensitive tenant", n, f.hit(n))
		}
	}
}
