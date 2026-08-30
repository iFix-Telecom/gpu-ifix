package models

import (
	"io"
	"log/slog"
)

// NewResolverForTesting constructs a Resolver pre-populated with the given
// (alias, upstream-name) -> target fixture map. INTENDED FOR TESTS ONLY in
// other internal packages (e.g. proxy) — production code should use
// NewResolver against a real Postgres pool.
//
// Phase 06.9 Plan 03: the proxy package's director tests need to instantiate
// a Resolver without touching Postgres; this exported helper sidesteps the
// in-package newResolverFromMap helper which is intentionally lowercase.
//
// The fixture is keyed by [alias, upstream] (Phase 06.9 semantics — upstream
// is the NAME, not the role tag). Empty fixtures are valid and result in
// a Resolver whose Resolve method always falls through to the env-override
// layer (when applicable) then to passthrough.
func NewResolverForTesting(fixture map[[2]string]string) *Resolver {
	aliases := make(map[aliasKey]string, len(fixture))
	for k, target := range fixture {
		aliases[aliasKey{Alias: k[0], Upstream: k[1]}] = target
	}
	return &Resolver{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		aliases: aliases,
	}
}

// SetProviderPrefsForTesting stores a provider_prefs blob for (alias,
// upstream) on a test Resolver (quick 260830-o2j). TESTS ONLY.
func (r *Resolver) SetProviderPrefsForTesting(alias, upstream string, raw []byte) {
	r.mu.Lock()
	if r.prefs == nil {
		r.prefs = map[aliasKey][]byte{}
	}
	r.prefs[aliasKey{Alias: alias, Upstream: upstream}] = raw
	r.mu.Unlock()
}
