// Package vast — unit tests for DTO JSON shapes. These tests pin the wire
// format Vast.ai expects so a refactor cannot silently rename the JSON keys
// (`args` vs `image_args` vs `args_str` is the Pitfall 5 trap documented in
// 06-RESEARCH.md line 436).
package vast

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreateRequest_ArgsOmitempty asserts that the new Args field added in
// plan 06-03 marshals to the JSON key `args` (lowercase, no prefix), and
// that the omitempty tag suppresses the field when zero-valued (so legacy
// ssh/ssh_proxy runtypes do not send a spurious `"args":null`).
func TestCreateRequest_ArgsOmitempty(t *testing.T) {
	t.Run("populated_emits_args_key", func(t *testing.T) {
		req := CreateRequest{Args: []string{"--host", "0.0.0.0"}}
		out, err := json.Marshal(req)
		require.NoError(t, err)
		require.Contains(t, string(out), `"args":["--host","0.0.0.0"]`,
			"Args field MUST serialize to JSON key `args` (Pitfall 5: NOT image_args, NOT args_str)")
	})

	t.Run("zero_value_omits_args_key", func(t *testing.T) {
		req := CreateRequest{} // Args is nil
		out, err := json.Marshal(req)
		require.NoError(t, err)
		require.NotContains(t, string(out), `"args"`,
			"omitempty MUST suppress the args key when Args is nil so ssh/ssh_proxy runtypes do not send it")
	})

	t.Run("wrong_keys_never_appear", func(t *testing.T) {
		req := CreateRequest{Args: []string{"x"}}
		out, err := json.Marshal(req)
		require.NoError(t, err)
		s := string(out)
		require.False(t, strings.Contains(s, "image_args"),
			"image_args is the WRONG key per RESEARCH.md Pitfall 5")
		require.False(t, strings.Contains(s, "args_str"),
			"args_str is the WRONG key per RESEARCH.md Pitfall 5")
	})
}

// TestCreateRequest_EntrypointOmitempty asserts the new Entrypoint field
// added in plan 06-03 (per WAVE0-GATES Decision 4 — spike Round 2 finding
// that Strategy B requires entrypoint override). Marshals to JSON key
// `entrypoint`; omitempty suppresses for legacy runtypes.
func TestCreateRequest_EntrypointOmitempty(t *testing.T) {
	t.Run("populated_emits_entrypoint_key", func(t *testing.T) {
		req := CreateRequest{Entrypoint: "/bin/bash"}
		out, err := json.Marshal(req)
		require.NoError(t, err)
		require.Contains(t, string(out), `"entrypoint":"/bin/bash"`,
			"Entrypoint field MUST serialize to JSON key `entrypoint` (matches vastai CLI --entrypoint)")
	})

	t.Run("zero_value_omits_entrypoint_key", func(t *testing.T) {
		req := CreateRequest{} // Entrypoint is ""
		out, err := json.Marshal(req)
		require.NoError(t, err)
		require.NotContains(t, string(out), `"entrypoint"`,
			"omitempty MUST suppress entrypoint key when zero so ssh/ssh_proxy runtypes do not send it")
	})
}

// TestCreateRequest_StrategyB_FullShape pins the exact wire payload Strategy B
// emits (per 06-SPIKE-runtype-args.md Round 2 + 06-WAVE0-GATES.md Decision 4).
// This is the "golden" shape plan 06-04 buildCreateRequest will produce.
func TestCreateRequest_StrategyB_FullShape(t *testing.T) {
	req := CreateRequest{
		ClientID:   "me",
		Image:      "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128",
		Env:        map[string]string{"-p 8000:8000": "1"},
		Runtype:    "args",
		Entrypoint: "/bin/bash",
		Args:       []string{"-c", "exec /app/llama-server --version"},
		Disk:       40,
		Label:      "ifix-emerg-test",
	}
	out, err := json.Marshal(req)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, `"runtype":"args"`)
	require.Contains(t, s, `"entrypoint":"/bin/bash"`)
	require.Contains(t, s, `"args":["-c","exec /app/llama-server --version"]`)
	require.Contains(t, s, `"image":"ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"`)
	require.Contains(t, s, `"disk":40`)
}

// TestDefaultSearchFilter_NumGPUs covers the num_gpus knob (PRIMARY_NUM_GPUS):
// an explicit count sets num_gpus:{eq:N} (2 for the 2×3090 single-pod topology),
// and a non-positive value falls back to 1 (preserves single-GPU default).
func TestDefaultSearchFilter_NumGPUs(t *testing.T) {
	t.Run("explicit_count", func(t *testing.T) {
		f := DefaultSearchFilter(1.0, 0, "RTX 3090", 2)
		ng := f["num_gpus"].(map[string]any)
		require.Equal(t, 2, ng["eq"], "num_gpus must reflect the requested count")
	})
	t.Run("non_positive_falls_back_to_1", func(t *testing.T) {
		f := DefaultSearchFilter(1.0, 0, "RTX 4090", 0)
		ng := f["num_gpus"].(map[string]any)
		require.Equal(t, 1, ng["eq"], "numGPUs<=0 must default to single GPU")
	})
}

// TestDefaultSearchFilters covers the Phase 11.1 D-A6 primary+fallback
// dispatch pair: DefaultSearchFilters returns a length-2 slice with the
// primary filter at index 0 and the fallback filter at index 1; each
// filter mirrors what DefaultSearchFilter would build for its own shape
// (1×3090 @ $0.30 primary; 2×3090 @ $0.60 fallback), and both filters
// carry the same blocklist verbatim.
func TestDefaultSearchFilters(t *testing.T) {
	const (
		primaryCap     = 0.30
		fallbackCap    = 0.60
		primaryHost    = int64(99)
		primaryGPU     = "RTX 3090"
		fallbackGPU    = "RTX 3090"
		primaryNumGPUs = 1
		fallbackNumGPU = 2
	)
	blocklist := []int64{111, 222}
	filters := DefaultSearchFilters(primaryCap, fallbackCap, primaryHost,
		primaryGPU, fallbackGPU, primaryNumGPUs, fallbackNumGPU, blocklist...)

	require.Len(t, filters, 2, "must return [primary, fallback]")

	// Shape #0 (primary): 1×3090 @ $0.30 + epsilon (cap+0.0001 logic
	// preserved by underlying DefaultSearchFilter).
	primary := filters[0]
	require.Equal(t, map[string]any{"eq": primaryGPU}, primary["gpu_name"])
	require.Equal(t, map[string]any{"eq": primaryNumGPUs}, primary["num_gpus"])
	require.Equal(t, map[string]any{"lte": primaryCap}, primary["dph_total"])

	// Shape #1 (fallback): 2×3090 @ $0.60.
	fallback := filters[1]
	require.Equal(t, map[string]any{"eq": fallbackGPU}, fallback["gpu_name"])
	require.Equal(t, map[string]any{"eq": fallbackNumGPU}, fallback["num_gpus"])
	require.Equal(t, map[string]any{"lte": fallbackCap}, fallback["dph_total"])

	// Both filters carry the same blocklist verbatim (regression: blocklist
	// must propagate to every shape so a broken-CDI host blocked for the
	// primary shape stays blocked for the fallback retry too).
	for i, f := range filters {
		mid, ok := f["machine_id"].(map[string]any)
		require.True(t, ok, "shape %d must carry machine_id blocklist", i)
		require.ElementsMatch(t, []any{int64(111), int64(222)}, mid["notin"],
			"shape %d blocklist must include all blocked machine_ids", i)
		hid, ok := f["host_id"].(map[string]any)
		require.True(t, ok, "shape %d must carry primaryHostID exclusion", i)
		require.Equal(t, primaryHost, hid["neq"])
	}
}

// TestWithMachineAllowlist covers the PRIMARY_VAST_MACHINE_ALLOWLIST preference
// pass: a non-empty allowlist sets machine_id:{in:[...]} (overwriting any
// blocklist notin clause), an empty allowlist is a no-op, and the original
// filter is not mutated (the reconciler reuses it for the broaden-fallback).
func TestWithMachineAllowlist(t *testing.T) {
	t.Run("sets_in_clause_and_overwrites_blocklist", func(t *testing.T) {
		base := DefaultSearchFilter(1.0, 0, "RTX 3090", 1, 111, 222) // blocklist 111,222
		out := WithMachineAllowlist(base, []int64{333, 444})
		mid, ok := out["machine_id"].(map[string]any)
		require.True(t, ok, "machine_id clause must be present")
		require.Contains(t, mid, "in", "allowlist must use the `in` clause")
		require.NotContains(t, mid, "notin", "allowlist overwrites the blocklist `notin`")
		require.ElementsMatch(t, []any{int64(333), int64(444)}, mid["in"])
	})

	t.Run("empty_allowlist_is_noop", func(t *testing.T) {
		base := DefaultSearchFilter(1.0, 0, "RTX 3090", 1, 111)
		out := WithMachineAllowlist(base, nil)
		require.Equal(t, base["machine_id"], out["machine_id"],
			"empty allowlist must leave the blocklist clause untouched")
	})

	t.Run("does_not_mutate_input", func(t *testing.T) {
		base := DefaultSearchFilter(1.0, 0, "RTX 3090", 1, 111, 222)
		_ = WithMachineAllowlist(base, []int64{333})
		mid := base["machine_id"].(map[string]any)
		require.Contains(t, mid, "notin",
			"WithMachineAllowlist must not mutate the input filter (reconciler reuses it for broaden-fallback)")
	})

	// reconciler_composition_preserves_default_fields pins the EXACT wire shape
	// the primary reconciler's allowlist-first pass sends to Vast.ai
	// (reconciler.go L769+L785 — `DefaultSearchFilter(0.60, 0, "RTX 3090", 2,
	// 55942, 45778)` then `WithMachineAllowlist(filter, []int64{43803})`). The
	// 06.8-05 diagnosis (.planning/phases/06.8-multi-pod-gpu-topology-sizing-stt-fix/
	// 06.8-ALLOWLIST-DIAGNOSIS.md §3.1) captured the byte-equivalent JSON the
	// runtime produces; this test guards against any future refactor silently
	// dropping a DefaultSearchFilter field from the allowlist branch (which
	// would re-introduce a steering bug like the deploy-staleness pattern the
	// diagnosis caught — see also Phase 06.8 Plan 05 Task 2).
	//
	// Asserts:
	//   - machine_id clause is exactly {"in":[allowlist...]} (the `in`
	//     operator overwrites the DefaultSearchFilter `notin` blocklist).
	//   - Every OTHER DefaultSearchFilter field survives the composition:
	//     gpu_name, num_gpus, reliability, dph_total, inet_down,
	//     cuda_max_good, driver_vers, rentable, order, limit. A composition
	//     that strips any of these would broaden the search beyond the
	//     primary's safety envelope.
	t.Run("reconciler_composition_preserves_default_fields", func(t *testing.T) {
		// Mirror the exact call the reconciler makes (reconciler.go L769+L785).
		base := DefaultSearchFilter(0.60, 0, "RTX 3090", 2, 55942, 45778)
		f := WithMachineAllowlist(base, []int64{43803})

		// machine_id: {"in":[43803]} — overwrites the blocklist {"notin":[55942,45778]}.
		mid, ok := f["machine_id"].(map[string]any)
		require.True(t, ok, "machine_id clause must be present after composition")
		require.Contains(t, mid, "in", "allowlist composition must use the `in` clause")
		require.NotContains(t, mid, "notin",
			"`in` must overwrite the blocklist `notin` set by DefaultSearchFilter")
		require.ElementsMatch(t, []any{int64(43803)}, mid["in"],
			"machine_id.in must carry exactly the allowlist ids")

		// Every DefaultSearchFilter field must survive the composition.
		gn, ok := f["gpu_name"].(map[string]any)
		require.True(t, ok, "gpu_name clause must survive composition")
		require.Equal(t, "RTX 3090", gn["eq"], "gpu_name.eq must be preserved")

		ng, ok := f["num_gpus"].(map[string]any)
		require.True(t, ok, "num_gpus clause must survive composition")
		require.Equal(t, 2, ng["eq"], "num_gpus.eq must be preserved (2 for 2×3090 single-pod)")

		rel, ok := f["reliability"].(map[string]any)
		require.True(t, ok, "reliability clause must survive composition")
		require.Equal(t, 0.99, rel["gte"], "reliability.gte must be preserved")

		dph, ok := f["dph_total"].(map[string]any)
		require.True(t, ok, "dph_total clause must survive composition")
		require.Equal(t, 0.60, dph["lte"], "dph_total.lte (price cap) must be preserved")

		inet, ok := f["inet_down"].(map[string]any)
		require.True(t, ok, "inet_down clause must survive composition")
		require.Equal(t, 200, inet["gte"], "inet_down.gte (Mbps) must be preserved (lowered 500→200 on 2026-05-28 — EU 3090 inventory inet ceiling)")

		cuda, ok := f["cuda_max_good"].(map[string]any)
		require.True(t, ok, "cuda_max_good clause must survive composition")
		require.Equal(t, 12.8, cuda["gte"], "cuda_max_good.gte must be preserved")

		drv, ok := f["driver_vers"].(map[string]any)
		require.True(t, ok, "driver_vers clause must survive composition")
		require.Equal(t, 570000000, drv["gte"], "driver_vers.gte (≥570 driver gate) must be preserved")

		rent, ok := f["rentable"].(map[string]any)
		require.True(t, ok, "rentable clause must survive composition")
		require.Equal(t, true, rent["eq"], "rentable.eq must be preserved")

		require.Equal(t, []any{[]any{"dph_total", "asc"}}, f["order"],
			"order (dph_total asc) must be preserved")
		require.Equal(t, 20, f["limit"], "limit must be preserved")

		// Marshal-roundtrip: confirm the JSON byte shape contains the
		// allowlist `in` clause AND every DefaultSearchFilter field.
		// Mirrors the diagnosis-captured wire shape (06.8-ALLOWLIST-DIAGNOSIS.md §3.1).
		raw, err := json.Marshal(f)
		require.NoError(t, err)
		s := string(raw)
		require.Contains(t, s, `"machine_id":{"in":[43803]}`,
			"marshaled JSON MUST contain the allowlist `in` clause")
		require.NotContains(t, s, `"notin"`,
			"marshaled JSON MUST NOT contain a `notin` clause after allowlist composition")
		require.Contains(t, s, `"gpu_name":{"eq":"RTX 3090"}`)
		require.Contains(t, s, `"num_gpus":{"eq":2}`)
		require.Contains(t, s, `"dph_total":{"lte":0.6}`)
		require.Contains(t, s, `"reliability":{"gte":0.99}`)
		require.Contains(t, s, `"inet_down":{"gte":200}`)
		require.Contains(t, s, `"cuda_max_good":{"gte":12.8}`)
		require.Contains(t, s, `"driver_vers":{"gte":570000000}`)
		require.Contains(t, s, `"rentable":{"eq":true}`)
		require.Contains(t, s, `"order":[["dph_total","asc"]]`)
		require.Contains(t, s, `"limit":20`)
	})
}

// TestRejectPrivateIPOffers asserts the Option A (6.6.Y-03) client-side
// RFC1918 reject filter: offers whose non-empty public_ipaddr is in
// 10.0.0.0/8, 172.16.0.0/12, or 192.168.0.0/16 are dropped; offers with a
// routable public IP or an EMPTY public_ipaddr are KEPT (empty cannot be
// proven private — Option B is the runtime backstop per 6.6.Y-01 spike).
// The six cases mirror the plan <behavior> block exactly.
func TestRejectPrivateIPOffers(t *testing.T) {
	// iter-1 root cause: host advertised public_ipaddr=192.168.1.8 → dropped.
	offers := []Offer{
		{ID: 1, PublicIPAddr: "192.168.1.8"},  // 192.168/16 → reject
		{ID: 2, PublicIPAddr: "172.20.0.5"},   // 172.16/12 → reject
		{ID: 3, PublicIPAddr: "10.5.5.5"},     // 10/8 → reject
		{ID: 4, PublicIPAddr: "85.218.235.6"}, // routable → keep
		{ID: 5, PublicIPAddr: ""},             // empty → keep (Option B backstop)
		{ID: 6, PublicIPAddr: "172.32.0.1"},   // 172.32 is OUTSIDE 172.16/12 → keep
	}

	got := RejectPrivateIPOffers(offers)

	gotIDs := make(map[int64]bool, len(got))
	for _, o := range got {
		gotIDs[o.ID] = true
	}
	require.False(t, gotIDs[1], "192.168.1.8 (192.168/16) MUST be rejected — iter-1 root cause")
	require.False(t, gotIDs[2], "172.20.0.5 (172.16/12) MUST be rejected")
	require.False(t, gotIDs[3], "10.5.5.5 (10/8) MUST be rejected")
	require.True(t, gotIDs[4], "85.218.235.6 (routable) MUST be kept")
	require.True(t, gotIDs[5], "empty public_ipaddr MUST be kept (Option B backstop)")
	require.True(t, gotIDs[6], "172.32.0.1 (outside 172.16/12) MUST be kept")
	require.Len(t, got, 3, "exactly 3 of 6 offers survive the RFC1918 reject")
}

// TestIsRFC1918 pins the CIDR boundary logic for the three private ranges
// plus the routable / empty / malformed edge cases.
func TestIsRFC1918(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.0", true},
		{"10.255.255.255", true},
		{"172.16.0.0", true},
		{"172.31.255.255", true},
		{"172.15.255.255", false}, // just below 172.16/12
		{"172.32.0.0", false},     // just above 172.16/12
		{"192.168.0.0", true},
		{"192.168.255.255", true},
		{"192.167.255.255", false},
		{"192.169.0.0", false},
		{"85.218.235.6", false},
		{"", false},          // empty cannot be proven private
		{"not-an-ip", false}, // unparseable → not private (kept)
		{"::1", false},       // IPv6 loopback is not RFC1918
	}
	for _, c := range cases {
		require.Equalf(t, c.want, isRFC1918(c.ip), "isRFC1918(%q)", c.ip)
	}
}
