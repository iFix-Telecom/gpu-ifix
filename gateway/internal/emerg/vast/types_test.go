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
