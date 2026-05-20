// Package upstreams (loader_test.go): Plan 06-08 Task 1 unit tests for
// OverrideTier0 / RestoreTier0 (D-E3 emergency-pod dispatcher integration).
//
// Race-test coverage: 100 concurrent reader goroutines + 1 writer
// alternating Override/Restore — `-race` flag MUST detect any data race
// on the atomic.Pointer[string] under tier0Override["llm"].
package upstreams

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newOverrideFixture builds a Loader with two upstreams: tier-0 local-llm
// + tier-1 openrouter-chat (mirrors the production Phase 3 setup) so
// override interaction with tier-1 fallback can be asserted.
func newOverrideFixture() *Loader {
	return NewLoaderForTest(
		UpstreamConfig{
			Name:    "local-llm",
			Role:    "llm",
			Tier:    0,
			URL:     "http://primary:8000",
			Enabled: true,
			CircuitConfig: CircuitConfig{
				Failures:  3,
				CooldownS: 30,
				Cooldown:  30 * time.Second,
			},
		},
		UpstreamConfig{
			Name:    "openrouter-chat",
			Role:    "llm",
			Tier:    1,
			URL:     "https://openrouter.example/v1",
			Enabled: true,
		},
		UpstreamConfig{
			Name:    "local-stt",
			Role:    "stt",
			Tier:    0,
			URL:     "http://stt:8000",
			Enabled: true,
		},
	)
}

// TestOverrideTier0 — calling OverrideTier0("llm", url) then Resolve("llm",0)
// returns the override URL with Name="emergency_pod_llm" and IsEmergency=true.
// CircuitConfig + auth fields inherited from the underlying tier-0 row so
// the dispatcher's breaker hooks remain intact.
func TestOverrideTier0(t *testing.T) {
	l := newOverrideFixture()

	// Pre-condition: Resolve returns primary.
	u, ok := l.Resolve("llm", 0)
	if !ok {
		t.Fatalf("pre-condition: Resolve(llm,0) not found")
	}
	if u.URL != "http://primary:8000" {
		t.Fatalf("pre-condition: Resolve URL = %q, want http://primary:8000", u.URL)
	}
	if u.IsEmergency {
		t.Fatalf("pre-condition: IsEmergency must be false before override")
	}

	// Activate override.
	l.OverrideTier0("llm", "http://emergency.pod:8000")

	got, ok := l.Resolve("llm", 0)
	if !ok {
		t.Fatalf("Resolve(llm,0) not found after override")
	}
	if got.URL != "http://emergency.pod:8000" {
		t.Errorf("Resolve URL = %q, want http://emergency.pod:8000", got.URL)
	}
	if got.Name != "emergency_pod_llm" {
		t.Errorf("Resolve Name = %q, want emergency_pod_llm", got.Name)
	}
	if !got.IsEmergency {
		t.Errorf("Resolve IsEmergency = false, want true")
	}
	if got.Role != "llm" || got.Tier != 0 {
		t.Errorf("Resolve Role/Tier = %q/%d, want llm/0", got.Role, got.Tier)
	}
	// Inherited fields from the underlying tier-0 row.
	if got.CircuitConfig.Failures != 3 {
		t.Errorf("CircuitConfig.Failures = %d, want 3 (inherited from primary)",
			got.CircuitConfig.Failures)
	}
}

// TestRestoreTier0 — after OverrideTier0, calling RestoreTier0 returns
// Resolve to the primary URL with Name=local-llm and IsEmergency=false.
func TestRestoreTier0(t *testing.T) {
	l := newOverrideFixture()
	l.OverrideTier0("llm", "http://emergency.pod:8000")

	// Sanity: override is active.
	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://emergency.pod:8000" {
		t.Fatalf("override not active before restore")
	}

	l.RestoreTier0("llm")

	got, ok := l.Resolve("llm", 0)
	if !ok {
		t.Fatalf("Resolve(llm,0) not found after restore")
	}
	if got.URL != "http://primary:8000" {
		t.Errorf("post-restore URL = %q, want http://primary:8000", got.URL)
	}
	if got.Name != "local-llm" {
		t.Errorf("post-restore Name = %q, want local-llm", got.Name)
	}
	if got.IsEmergency {
		t.Errorf("post-restore IsEmergency = true, want false")
	}
}

// TestResolveWithOverride_OnlyTier0 — override applies to tier=0 only.
// Resolve("llm", 1) MUST return the openrouter-chat fallback unchanged.
// Critical: the dispatcher's tier-1 fallback path during emergency must
// continue to work if both primary AND emergency pod fail.
func TestResolveWithOverride_OnlyTier0(t *testing.T) {
	l := newOverrideFixture()
	l.OverrideTier0("llm", "http://emergency.pod:8000")

	got, ok := l.Resolve("llm", 1)
	if !ok {
		t.Fatalf("Resolve(llm,1) not found")
	}
	if got.URL != "https://openrouter.example/v1" {
		t.Errorf("tier-1 URL mutated by tier-0 override: %q", got.URL)
	}
	if got.Name != "openrouter-chat" {
		t.Errorf("tier-1 Name mutated: %q", got.Name)
	}
	if got.IsEmergency {
		t.Errorf("tier-1 IsEmergency = true, want false")
	}
}

// TestOverrideTier0_NonExistentRole — overriding a role not in the
// override map is a silent no-op. Resolve continues to return the
// snapshot row untouched.
//
// Phase 6.6 — the v1 override map was LLM-only; Phase 6.6 extended it
// to {llm, stt, embed} for primary pod routing. Use a truly non-existent
// role (e.g. "vision") to assert the silent no-op semantics survive.
func TestOverrideTier0_NonExistentRole(t *testing.T) {
	l := newOverrideFixture()

	// "vision" is not in the v6.6 override map; OverrideTier0 must be no-op.
	l.OverrideTier0("vision", "http://emergency.vision:8000")

	// Sanity: stt (which IS in the v6.6 map) still untouched.
	got, ok := l.Resolve("stt", 0)
	if !ok {
		t.Fatalf("Resolve(stt,0) not found")
	}
	if got.URL != "http://stt:8000" {
		t.Errorf("non-existent role override leaked into stt: URL = %q, want http://stt:8000", got.URL)
	}
	if got.IsEmergency {
		t.Errorf("non-existent role override leaked into stt: IsEmergency = true, want false")
	}

	// Restore is also no-op for unknown role.
	l.RestoreTier0("vision") // must not panic.
}

// TestRestoreTier0_Idempotent — calling RestoreTier0 when no override is
// active is a no-op (Store(nil) on already-nil pointer). Calling
// RestoreTier0 twice in a row is also a no-op.
func TestRestoreTier0_Idempotent(t *testing.T) {
	l := newOverrideFixture()

	// No override active.
	l.RestoreTier0("llm") // no-op
	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://primary:8000" {
		t.Errorf("Resolve URL = %q after no-op restore, want http://primary:8000", got.URL)
	}

	// Activate then restore twice.
	l.OverrideTier0("llm", "http://emergency:8000")
	l.RestoreTier0("llm")
	l.RestoreTier0("llm") // second call is no-op

	got, _ = l.Resolve("llm", 0)
	if got.URL != "http://primary:8000" {
		t.Errorf("Resolve URL = %q after double restore, want http://primary:8000", got.URL)
	}
}

// TestOverrideTier0_Replaces — a second OverrideTier0 call replaces the
// first URL atomically. Use case: leader recovery resumes a lifecycle
// with a different pod URL than the original (rare but valid — the
// resumed instance might be a different Vast.ai contract).
func TestOverrideTier0_Replaces(t *testing.T) {
	l := newOverrideFixture()
	l.OverrideTier0("llm", "http://emergency.first:8000")
	l.OverrideTier0("llm", "http://emergency.second:8000")

	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://emergency.second:8000" {
		t.Errorf("Resolve URL = %q, want http://emergency.second:8000 (second override)", got.URL)
	}
}

// TestOverrideTier0_RaceFreeReads — 100 reader goroutines + 1 writer
// alternating Override/Restore. With -race, any data race on the
// atomic.Pointer[string] surfaces. Without -race this is still useful as
// a smoke test for the lockless path: readers must always observe
// either the primary URL OR the override URL — never garbage.
//
// Run via: `go test -race -run TestOverrideTier0_RaceFreeReads`.
func TestOverrideTier0_RaceFreeReads(t *testing.T) {
	l := newOverrideFixture()

	const numReaders = 100
	const iterations = 1000

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var failures atomic.Int64

	// Spawn readers.
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					u, ok := l.Resolve("llm", 0)
					if !ok {
						failures.Add(1)
						return
					}
					switch u.URL {
					case "http://primary:8000", "http://emergency.race:8000":
						// expected
					default:
						failures.Add(1)
						t.Errorf("reader observed unexpected URL: %q", u.URL)
						return
					}
				}
			}
		}()
	}

	// Single writer alternates Override/Restore.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				l.OverrideTier0("llm", "http://emergency.race:8000")
			} else {
				l.RestoreTier0("llm")
			}
		}
		close(stop)
	}()

	wg.Wait()

	if failures.Load() > 0 {
		t.Fatalf("race-test detected %d failures", failures.Load())
	}
}

// TestNewLoaderForTest_IncludesOverrideMap — defensive check that
// NewLoaderForTest constructs the override map. A regression where the
// helper was updated without adding the map would silently disable
// OverrideTier0 (no-op for all calls).
func TestNewLoaderForTest_IncludesOverrideMap(t *testing.T) {
	l := NewLoaderForTest(UpstreamConfig{
		Name: "local-llm", Role: "llm", Tier: 0, URL: "http://primary", Enabled: true,
	})
	l.OverrideTier0("llm", "http://check:8000")
	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://check:8000" {
		t.Fatalf("NewLoaderForTest does not init override map; URL = %q", got.URL)
	}
}

// TestNewLoaderInMemory_IncludesOverrideMap — same defensive check for
// the cross-package helper used by dispatcher tests.
func TestNewLoaderInMemory_IncludesOverrideMap(t *testing.T) {
	l := NewLoaderInMemory(UpstreamConfig{
		Name: "local-llm", Role: "llm", Tier: 0, URL: "http://primary", Enabled: true,
	})
	l.OverrideTier0("llm", "http://check:8000")
	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://check:8000" {
		t.Fatalf("NewLoaderInMemory does not init override map; URL = %q", got.URL)
	}
}

// TestNewTier0OverrideMap_Has3Roles — Phase 6.6 → Phase 06.7 — the canonical
// override map exposes 3 dynamic-override role keys. In Phase 06.7 (D-03/D-11)
// the roster changed from {llm,stt,embed} to {llm,stt,tts}: tts moved onto the
// primary pod and embed relocated to a STATIC tier-0 upstream (n8n-ia-vm CPU).
// Defensive against a future refactor that silently shrinks the map.
func TestNewTier0OverrideMap_Has3Roles(t *testing.T) {
	m := newTier0OverrideMap()
	if len(m) != 3 {
		t.Fatalf("expected 3 roles in override map, got %d", len(m))
	}
	for _, role := range []string{"llm", "stt", "tts"} {
		p, ok := m[role]
		if !ok {
			t.Errorf("expected role %q in override map", role)
		}
		if p == nil {
			t.Errorf("role %q has nil atomic.Pointer", role)
		}
		// Each pointer starts empty (no override active).
		if v := p.Load(); v != nil {
			t.Errorf("role %q expected empty pointer, got %v", role, v)
		}
	}
	// embed must NOT have a dynamic-override slot post-06.7 (it is static).
	if _, ok := m["embed"]; ok {
		t.Errorf("embed must not be in the dynamic-override map post-06.7 (D-03)")
	}
}

// TestOverrideTier0_RestoreTier0_AllRoles — Phase 6.6 → Phase 06.7 —
// OverrideTier0 and RestoreTier0 are role-agnostic: same code path works
// for llm, stt, tts. Iterate all 3 dynamic-override roles, override each
// with a distinct URL, assert each lookup returns the set value, then
// RestoreTier0 each and assert the pointer is cleared.
//
// Phase 06.7 (D-03/D-11) — the third dynamic role is now "tts" (was
// "embed"); embed relocated to a static tier-0 upstream so it no longer has
// a dynamic-override slot. Fixture includes tier-0 rows for all 3 roles so
// Resolve has a base row to overlay the override URL on top of.
func TestOverrideTier0_RestoreTier0_AllRoles(t *testing.T) {
	l := NewLoaderForTest(
		UpstreamConfig{Name: "local-llm", Role: "llm", Tier: 0, URL: "http://primary-llm:8000", Enabled: true},
		UpstreamConfig{Name: "local-stt", Role: "stt", Tier: 0, URL: "http://primary-stt:8000", Enabled: true},
		UpstreamConfig{Name: "local-tts", Role: "tts", Tier: 0, URL: "http://primary-tts:8003", Enabled: true},
	)

	cases := []struct {
		role, overrideURL, baseURL string
	}{
		{"llm", "http://primary-pod:11434", "http://primary-llm:8000"},
		{"stt", "http://primary-pod:9000", "http://primary-stt:8000"},
		{"tts", "http://primary-pod:8003", "http://primary-tts:8003"},
	}

	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			// Pre-condition: Resolve returns the base tier-0 URL.
			got, ok := l.Resolve(tc.role, 0)
			if !ok {
				t.Fatalf("pre: Resolve(%q,0) not found", tc.role)
			}
			if got.URL != tc.baseURL {
				t.Fatalf("pre: Resolve URL = %q, want %q", got.URL, tc.baseURL)
			}
			if got.IsEmergency {
				t.Fatalf("pre: IsEmergency must be false before override")
			}

			// Activate override.
			l.OverrideTier0(tc.role, tc.overrideURL)

			got, ok = l.Resolve(tc.role, 0)
			if !ok {
				t.Fatalf("post-override: Resolve(%q,0) not found", tc.role)
			}
			if got.URL != tc.overrideURL {
				t.Errorf("post-override: URL = %q, want %q", got.URL, tc.overrideURL)
			}
			if got.Name != "emergency_pod_"+tc.role {
				t.Errorf("post-override: Name = %q, want emergency_pod_%s", got.Name, tc.role)
			}
			if !got.IsEmergency {
				t.Errorf("post-override: IsEmergency = false, want true")
			}

			// Restore.
			l.RestoreTier0(tc.role)
			got, ok = l.Resolve(tc.role, 0)
			if !ok {
				t.Fatalf("post-restore: Resolve(%q,0) not found", tc.role)
			}
			if got.URL != tc.baseURL {
				t.Errorf("post-restore: URL = %q, want %q (base)", got.URL, tc.baseURL)
			}
			if got.IsEmergency {
				t.Errorf("post-restore: IsEmergency = true, want false")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Phase 06.7 Wave 0 RED scaffolding (Nyquist gate). Skip stubs binding the
// tier-0 "tts" role wiring (D-11) + the new Tier0OverrideURL getter (D-13)
// to their owning implementation plan. ENGINE-AGNOSTIC — they assert the
// tier-0 override MAP shape + getter, not Chatterbox internals.
//
// OWNER map (authority: 06.7-02-PLAN.md <stub_ownership_map>):
//   - TestTier0_TTSKeyPresentEmbedAbsent -> Plan 06.7-03
//   - TestTier0OverrideURL_Getter        -> Plan 06.7-03
// ---------------------------------------------------------------------------

// TestTier0_TTSKeyPresentEmbedAbsent asserts that after the Phase 06.7
// engine swap the canonical tier-0 override map (newTier0OverrideMap)
// contains a "tts" key and DROPS the "embed" key (embed relocates to
// n8n-ia-vm CPU per D-03/D-11 — it is no longer a primary-pod tier-0 role).
// The owning plan must change newTier0OverrideMap from {llm,stt,embed} to
// {llm,stt,tts} and prove OverrideTier0("tts", url) routes while
// OverrideTier0("embed", url) is a no-op.
//
// OWNER: Plan 06.7-03 — unskip + assert real map keys before COMPLETE.
func TestTier0_TTSKeyPresentEmbedAbsent(t *testing.T) {
	// Map shape: tts present, embed absent.
	m := newTier0OverrideMap()
	if _, ok := m["tts"]; !ok {
		t.Errorf("newTier0OverrideMap missing \"tts\" key (D-11)")
	}
	if _, ok := m["embed"]; ok {
		t.Errorf("newTier0OverrideMap must not contain \"embed\" (D-03 — embed is static)")
	}

	// OverrideTier0("tts") routes; OverrideTier0("embed") is a no-op because
	// embed has no dynamic slot and resolves the static tier-0 row unchanged.
	l := NewLoaderForTest(
		UpstreamConfig{Name: "primary-tts", Role: "tts", Tier: 0, URL: "http://primary-tts:8003", Enabled: true},
		UpstreamConfig{Name: "static-embed", Role: "embed", Tier: 0, URL: "http://n8n-ia:7997", Enabled: true},
	)

	l.OverrideTier0("tts", "http://primary-pod:8003")
	got, ok := l.Resolve("tts", 0)
	if !ok {
		t.Fatalf("Resolve(tts,0) not found after override")
	}
	if got.URL != "http://primary-pod:8003" {
		t.Errorf("tts override URL = %q, want http://primary-pod:8003", got.URL)
	}
	if got.Name != "emergency_pod_tts" || !got.IsEmergency {
		t.Errorf("tts override Name/IsEmergency = %q/%v, want emergency_pod_tts/true", got.Name, got.IsEmergency)
	}

	// embed override is a no-op: the static row is served unchanged.
	l.OverrideTier0("embed", "http://should-be-ignored:8000")
	gotE, ok := l.Resolve("embed", 0)
	if !ok {
		t.Fatalf("Resolve(embed,0) not found")
	}
	if gotE.URL != "http://n8n-ia:7997" {
		t.Errorf("embed override leaked: URL = %q, want static http://n8n-ia:7997", gotE.URL)
	}
	if gotE.IsEmergency {
		t.Errorf("embed must never be IsEmergency (no dynamic slot)")
	}
}

// TestTier0OverrideURL_Getter asserts the new Loader.Tier0OverrideURL(role)
// (string, bool) getter (D-13): returns (overrideURL, true) when an override
// is active for the role, (\"\", false) when the role is unknown or no
// override is set. The primary reconciler's Pitfall #11 re-assert path
// (Plan 06.7-08) reads this to decide whether the tier-0 slot is empty.
//
// OWNER: Plan 06.7-03 — implement Tier0OverrideURL + unskip before COMPLETE.
func TestTier0OverrideURL_Getter(t *testing.T) {
	l := newOverrideFixture()

	// Before any override: known role with empty slot -> ("", false).
	if url, ok := l.Tier0OverrideURL("llm"); ok || url != "" {
		t.Errorf("pre-override Tier0OverrideURL(llm) = (%q,%v), want (\"\",false)", url, ok)
	}
	// Unknown role (no slot) -> ("", false).
	if url, ok := l.Tier0OverrideURL("embed"); ok || url != "" {
		t.Errorf("Tier0OverrideURL(embed) = (%q,%v), want (\"\",false) (no slot)", url, ok)
	}

	// After override: (url, true).
	l.OverrideTier0("llm", "http://emergency.pod:8000")
	if url, ok := l.Tier0OverrideURL("llm"); !ok || url != "http://emergency.pod:8000" {
		t.Errorf("post-override Tier0OverrideURL(llm) = (%q,%v), want (http://emergency.pod:8000,true)", url, ok)
	}

	// After restore: back to ("", false).
	l.RestoreTier0("llm")
	if url, ok := l.Tier0OverrideURL("llm"); ok || url != "" {
		t.Errorf("post-restore Tier0OverrideURL(llm) = (%q,%v), want (\"\",false)", url, ok)
	}
}
