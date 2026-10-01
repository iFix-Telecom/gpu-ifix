// Package upstreams (exports_helpers.go): test-only construction helpers
// exposed to other packages' test code. This file lives at the package
// root (NOT in _test.go) so external test packages can call into it;
// _test.go files are not visible across package boundaries.
//
// The cost: a tiny ~30-line constructor compiles into the production
// binary. The benefit: dispatcher tests in gateway/internal/proxy can
// build a Loader without standing up testcontainers Postgres.
//
// DO NOT call NewLoaderInMemory from production code — there is no
// LISTEN/NOTIFY thread, no env-resolution, no Refresh capability.
package upstreams

// NewLoaderInMemory constructs a Loader with a fixed in-memory snapshot
// (no Postgres connection, no Refresh). Used by external-package tests
// (e.g. dispatcher tests in gateway/internal/proxy/).
//
// Identical to NewLoaderForTest in loader_export_test.go (which is
// accessible only to upstreams_test internal tests).
func NewLoaderInMemory(cfgs ...UpstreamConfig) *Loader {
	l := &Loader{tier0Override: newTier0OverrideMap()}
	l.snap.Store(newSnapshot(cfgs))
	return l
}
