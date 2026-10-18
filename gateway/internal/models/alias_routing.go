// alias_routing.go — per-row routing knobs of model_aliases (quick 261018).
//
// Two optional columns travel with each (alias, upstream_name) row:
//
//	priority         INT     order of the upstream INSIDE one pinned alias.
//	                         When at least one row of the alias is non-NULL the
//	                         dispatcher tries that alias' upstreams in
//	                         priority ASC (NULLs last, ties broken by the
//	                         global (tier, tier_priority) order). All NULL →
//	                         the global order, exactly as before the column.
//	fallback_models  TEXT[]  OpenRouter-only: the director injects
//	                         "models": [target, ...fallback_models] (native
//	                         OpenRouter model fallback), keeping model=target.
//
// ValidateFallbackModels is the SINGLE write-side gate for the list (shared
// by gatewayctl and the admin API) so the director can inject the stored
// slice on the hot path without re-validating.
package models

import (
	"fmt"
	"unicode"
)

// FallbackModelsMax caps the stored list. OpenRouter walks `models` in order
// on provider errors; a long tail only adds latency on a bad day, and the
// cap keeps a stray paste from ballooning every request body.
//
// HIPÓTESE: OpenRouter accepts more entries than this; the cap is our policy,
// not a documented provider limit.
const FallbackModelsMax = 5

// fallbackModelMaxLen mirrors the R10 cap on model_aliases.target (128).
const fallbackModelMaxLen = 128

// FallbackModelsUpstreams is the closed set of upstreams whose director
// consumes fallback_models (the two OpenRouter chat directors). Same set as
// providerPrefsUpstreams in gatewayctl/admin; storing a list on any other
// row would be silently inert, so writes are refused.
var FallbackModelsUpstreams = map[string]bool{
	"openrouter-chat":     true,
	"openrouter-deepseek": true,
}

// ValidateFallbackModels checks the OpenRouter fallback list for one row and
// returns the canonical slice to store (nil when the input is empty, so the
// column becomes NULL). target is the row's own target: it is always sent as
// the first entry of `models`, so repeating it in the list is refused.
//
// Rules: at most FallbackModelsMax entries; each entry non-empty, ≤128 chars,
// no whitespace/control/NUL (same R10 hygiene as target); no duplicates.
func ValidateFallbackModels(target string, models []string) ([]string, error) {
	if len(models) == 0 {
		return nil, nil
	}
	if len(models) > FallbackModelsMax {
		return nil, fmt.Errorf("fallback_models accepts at most %d entries, got %d", FallbackModelsMax, len(models))
	}
	seen := make(map[string]bool, len(models))
	out := make([]string, 0, len(models))
	for i, m := range models {
		if m == "" {
			return nil, fmt.Errorf("fallback_models[%d] must not be empty", i)
		}
		if len(m) > fallbackModelMaxLen {
			return nil, fmt.Errorf("fallback_models[%d] exceeds max length (%d): %d chars", i, fallbackModelMaxLen, len(m))
		}
		for pos, r := range m {
			if r == 0 || unicode.IsSpace(r) || unicode.IsControl(r) {
				return nil, fmt.Errorf("fallback_models[%d] must not contain whitespace or control chars (position %d)", i, pos)
			}
		}
		if m == target {
			return nil, fmt.Errorf("fallback_models[%d] repeats the target %q (it is already the first model)", i, m)
		}
		if seen[m] {
			return nil, fmt.Errorf("fallback_models[%d] duplicates %q", i, m)
		}
		seen[m] = true
		out = append(out, m)
	}
	return out, nil
}

// PinnedPriorities returns upstream_name → priority for the rows of alias
// that carry a non-NULL priority, or nil when NO row of the alias has one
// (the dispatcher then keeps the global order, byte-for-byte the pre-261018
// behaviour). Rows with NULL priority are simply absent from the map — the
// caller sorts them after every prioritized row. Returned map is a copy.
func (r *Resolver) PinnedPriorities(alias string) map[string]int {
	r.mu.RLock()
	src, ok := r.priorities[alias]
	r.mu.RUnlock()
	if !ok || len(src) == 0 {
		return nil
	}
	out := make(map[string]int, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// FallbackModels returns the stored OpenRouter fallback list for (alias,
// upstream) or nil when the row has none. Returned slice is a copy.
func (r *Resolver) FallbackModels(alias, upstream string) []string {
	r.mu.RLock()
	src, ok := r.fallbacks[aliasKey{Alias: alias, Upstream: upstream}]
	r.mu.RUnlock()
	if !ok || len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}
