// provider_prefs.go — per-model OpenRouter provider-routing preferences
// (quick 260830-o2j).
//
// A model_aliases row may carry a `provider_prefs` JSON object that the
// openrouter-chat director injects VERBATIM as the request's `provider`
// field. The schema mirrors OpenRouter's documented provider-routing object
// (https://openrouter.ai/docs/features/provider-routing):
//
//	only / order / ignore                 []string provider slugs
//	allow_fallbacks / require_parameters  bool
//	zdr / enforce_distillable_text        bool
//	data_collection                       "allow" | "deny"
//	quantizations                         []string (int4 … bf16 … unknown)
//	sort                                  "price"|"throughput"|"latency" | {by, partition}
//	max_price                             {prompt, completion, request, image} $/M (>= 0)
//	preferred_min_throughput              number | {p50,p75,p90,p99}
//	preferred_max_latency                 number | {p50,p75,p90,p99}
//
// ValidateProviderPrefs is the SINGLE write-side gate: gatewayctl and the
// admin API both call it before persisting, so the director can trust the
// stored bytes and inject them without re-validating on the hot path.
package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ProviderPrefsMaxBytes caps the stored JSON so a stray paste cannot balloon
// the resolver cache or the per-request body rewrite.
const ProviderPrefsMaxBytes = 4096

var (
	providerQuantizations = map[string]bool{
		"int4": true, "int8": true, "fp4": true, "mxfp4": true, "nvfp4": true,
		"fp6": true, "fp8": true, "mxfp8": true, "fp16": true, "bf16": true,
		"fp32": true, "unknown": true,
	}
	providerSortBy        = map[string]bool{"price": true, "throughput": true, "latency": true}
	providerSortPartition = map[string]bool{"model": true, "none": true}
	providerPercentiles   = map[string]bool{"p50": true, "p75": true, "p90": true, "p99": true}
	providerDataCollect   = map[string]bool{"allow": true, "deny": true}
)

// ProviderPrefs is the typed mirror of the OpenRouter `provider` object.
// Pointer/omitempty fields so the canonical re-encode only carries what the
// operator set — OpenRouter applies its own defaults for absent keys.
type ProviderPrefs struct {
	Only                   []string          `json:"only,omitempty"`
	Order                  []string          `json:"order,omitempty"`
	Ignore                 []string          `json:"ignore,omitempty"`
	AllowFallbacks         *bool             `json:"allow_fallbacks,omitempty"`
	RequireParameters      *bool             `json:"require_parameters,omitempty"`
	DataCollection         string            `json:"data_collection,omitempty"`
	ZDR                    *bool             `json:"zdr,omitempty"`
	EnforceDistillableText *bool             `json:"enforce_distillable_text,omitempty"`
	Quantizations          []string          `json:"quantizations,omitempty"`
	Sort                   json.RawMessage   `json:"sort,omitempty"`
	MaxPrice               *ProviderMaxPrice `json:"max_price,omitempty"`
	PreferredMinThroughput json.RawMessage   `json:"preferred_min_throughput,omitempty"`
	PreferredMaxLatency    json.RawMessage   `json:"preferred_max_latency,omitempty"`
}

// ProviderMaxPrice is the `max_price` sub-object ($ per million tokens).
type ProviderMaxPrice struct {
	Prompt     *float64 `json:"prompt,omitempty"`
	Completion *float64 `json:"completion,omitempty"`
	Request    *float64 `json:"request,omitempty"`
	Image      *float64 `json:"image,omitempty"`
}

// ValidateProviderPrefs parses raw against the OpenRouter schema (unknown
// keys rejected) and returns the CANONICAL re-encoded bytes to store. An
// empty object `{}` is rejected — callers should store NULL instead.
func ValidateProviderPrefs(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("provider_prefs must not be empty (use NULL to clear)")
	}
	if len(trimmed) > ProviderPrefsMaxBytes {
		return nil, fmt.Errorf("provider_prefs exceeds %d bytes", ProviderPrefsMaxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var p ProviderPrefs
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("provider_prefs: %w", err)
	}
	if dec.More() {
		return nil, errors.New("provider_prefs: trailing data after JSON object")
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	out, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if string(out) == "{}" {
		return nil, errors.New("provider_prefs must set at least one field (use NULL to clear)")
	}
	return out, nil
}

func (p *ProviderPrefs) validate() error {
	for name, list := range map[string][]string{"only": p.Only, "order": p.Order, "ignore": p.Ignore} {
		if err := validateSlugList(name, list, nil); err != nil {
			return err
		}
	}
	if err := validateSlugList("quantizations", p.Quantizations, providerQuantizations); err != nil {
		return err
	}
	if p.DataCollection != "" && !providerDataCollect[p.DataCollection] {
		return fmt.Errorf("provider_prefs.data_collection must be \"allow\" or \"deny\", got %q", p.DataCollection)
	}
	if p.MaxPrice != nil {
		for name, v := range map[string]*float64{
			"prompt": p.MaxPrice.Prompt, "completion": p.MaxPrice.Completion,
			"request": p.MaxPrice.Request, "image": p.MaxPrice.Image,
		} {
			if v != nil && *v < 0 {
				return fmt.Errorf("provider_prefs.max_price.%s must be >= 0", name)
			}
		}
		if p.MaxPrice.Prompt == nil && p.MaxPrice.Completion == nil && p.MaxPrice.Request == nil && p.MaxPrice.Image == nil {
			return errors.New("provider_prefs.max_price must set at least one of prompt/completion/request/image")
		}
	}
	if len(p.Sort) > 0 {
		canon, err := validateSort(p.Sort)
		if err != nil {
			return err
		}
		p.Sort = canon
	}
	for name, raw := range map[string]*json.RawMessage{
		"preferred_min_throughput": &p.PreferredMinThroughput,
		"preferred_max_latency":    &p.PreferredMaxLatency,
	} {
		if len(*raw) == 0 {
			continue
		}
		canon, err := validateThreshold(name, *raw)
		if err != nil {
			return err
		}
		*raw = canon
	}
	return nil
}

func validateSlugList(name string, list []string, allowed map[string]bool) error {
	if list == nil {
		return nil
	}
	if len(list) == 0 {
		return fmt.Errorf("provider_prefs.%s must not be an empty list (omit the key instead)", name)
	}
	if len(list) > 32 {
		return fmt.Errorf("provider_prefs.%s has too many entries (max 32)", name)
	}
	for _, s := range list {
		if s == "" || strings.TrimSpace(s) != s || strings.ContainsAny(s, " \t\r\n") {
			return fmt.Errorf("provider_prefs.%s entries must be non-empty slugs without whitespace, got %q", name, s)
		}
		if len(s) > 64 {
			return fmt.Errorf("provider_prefs.%s entry %q exceeds 64 chars", name, s)
		}
		if allowed != nil && !allowed[s] {
			keys := make([]string, 0, len(allowed))
			for k := range allowed {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return fmt.Errorf("provider_prefs.%s: %q is not one of %s", name, s, strings.Join(keys, ","))
		}
	}
	return nil
}

// validateSort accepts "price"|"throughput"|"latency" or {by, partition?}.
func validateSort(raw json.RawMessage) (json.RawMessage, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if !providerSortBy[s] {
			return nil, fmt.Errorf("provider_prefs.sort must be price|throughput|latency, got %q", s)
		}
		return json.Marshal(s)
	}
	var obj struct {
		By        string `json:"by"`
		Partition string `json:"partition,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("provider_prefs.sort: %w", err)
	}
	if !providerSortBy[obj.By] {
		return nil, fmt.Errorf("provider_prefs.sort.by must be price|throughput|latency, got %q", obj.By)
	}
	if obj.Partition != "" && !providerSortPartition[obj.Partition] {
		return nil, fmt.Errorf("provider_prefs.sort.partition must be model|none, got %q", obj.Partition)
	}
	return json.Marshal(obj)
}

// validateThreshold accepts a non-negative number or an object keyed by
// p50/p75/p90/p99 with non-negative numeric values.
func validateThreshold(name string, raw json.RawMessage) (json.RawMessage, error) {
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		if n < 0 {
			return nil, fmt.Errorf("provider_prefs.%s must be >= 0", name)
		}
		return json.Marshal(n)
	}
	var obj map[string]float64
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("provider_prefs.%s must be a number or {p50|p75|p90|p99: number}: %w", name, err)
	}
	if len(obj) == 0 {
		return nil, fmt.Errorf("provider_prefs.%s object must not be empty", name)
	}
	for k, v := range obj {
		if !providerPercentiles[k] {
			return nil, fmt.Errorf("provider_prefs.%s: unknown percentile %q (use p50/p75/p90/p99)", name, k)
		}
		if v < 0 {
			return nil, fmt.Errorf("provider_prefs.%s.%s must be >= 0", name, k)
		}
	}
	return json.Marshal(obj)
}

// ProviderPrefs returns the stored provider object for (alias, upstream) or
// nil when the row has none. The returned slice is a copy of the cached
// bytes so callers may not mutate the cache.
func (r *Resolver) ProviderPrefs(alias, upstream string) []byte {
	r.mu.RLock()
	raw, ok := r.prefs[aliasKey{Alias: alias, Upstream: upstream}]
	r.mu.RUnlock()
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}
