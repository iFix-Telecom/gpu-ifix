// Package emerg (lifecycle_test.go): unit tests for the lifecycle-bound
// helpers that still live inside the emerg package (podHealthURL,
// errReason, buildCreateRequest, emergencyOnstart). The 5 pure helpers
// (filterBelowCap, excludeHost, mustEventJSON, pgInt8,
// pgNumericFromFloat) moved to gateway/internal/vastutil/ in Plan
// 06.6-02 — their tests live in `gateway/internal/vastutil/helpers_test.go`
// to avoid duplicate coverage. The previous duplicate copies of
// TestFilterBelowCap_*, TestExcludeHost, TestMustEventJSON,
// TestPgInt8 and TestPgNumericFromFloat were deleted in that plan;
// behaviour is unchanged because the emerg call sites now delegate to
// vastutil.* and the assertions are word-for-word the same.
//
// The full provisionLifecycle + waitForReadyOrDestroy + reconciler-state
// flow is exercised in
// gateway/internal/integration_test/emerg_provision_happy_test.go (build
// tag `integration`) — this file only covers the synchronous, side-effect-
// free helpers that don't need a Postgres + Redis harness.
package emerg

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// TestPodHealthURL_RunningWithPort — happy path.
func TestPodHealthURL_RunningWithPort(t *testing.T) {
	r := &Reconciler{}
	inst := vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports: map[string][]vast.PortBinding{
			"8000/tcp": {{HostIP: "0.0.0.0", HostPort: "40713"}},
		},
	}
	require.Equal(t, "http://1.2.3.4:40713/v1/models", r.podHealthURL(inst))
}

// TestPodHealthURL_W6_Empty — Pitfall 6 fix: any of (no IP, no ports
// entry, empty bindings, empty HostPort) returns "".
func TestPodHealthURL_W6_Empty(t *testing.T) {
	r := &Reconciler{}

	// No IP.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		Ports: map[string][]vast.PortBinding{"8000/tcp": {{HostPort: "40713"}}},
	}))

	// No ports map at all.
	require.Equal(t, "", r.podHealthURL(vast.Instance{PublicIPAddr: "1.2.3.4"}))

	// 9100/tcp absent.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports:        map[string][]vast.PortBinding{"22/tcp": {{HostPort: "30000"}}},
	}))

	// Empty bindings list.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports:        map[string][]vast.PortBinding{"8000/tcp": {}},
	}))

	// Empty HostPort string.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports:        map[string][]vast.PortBinding{"8000/tcp": {{HostPort: ""}}},
	}))
}

// TestErrReason — sanity for the error-token mapping used in FSM transition reasons.
func TestErrReason(t *testing.T) {
	require.Equal(t, "offer_race_lost", errReason(ErrOfferRaceLost))
	require.Equal(t, "health_timeout", errReason(ErrHealthTimeout))
	require.Equal(t, "instance_terminal_state", errReason(ErrInstanceTerminal))
	require.Equal(t, "no_offers_below_cap", errReason(ErrNoOffersBelowCap))
	require.Equal(t, "other", errReason(errors.New("unrelated error")))
}

// ---------------------------------------------------------------------
// Plan 06-04 — Strategy B Locked buildCreateRequest unit tests.
//
// Pattern revised per 06-WAVE0-GATES.md Decision 4 (supersedes plan
// must_haves truth #6 verbatim 15-token args slice): runtype=args with
// entrypoint=/bin/bash + args=["-c", <onstart-script>]. The 15
// llama-server flags now live inside the onstart script's final
// `exec /app/llama-server ...` line, NOT in the wire-level Args array.
// Empirical evidence: 06-SPIKE-runtype-args.md Round 2 (Vast CLI
// `--onstart-cmd` does NOT shell-wrap in args runtype; entrypoint
// override is mandatory).
// ---------------------------------------------------------------------

// newReconcilerForBuildTest constructs a minimal Reconciler with Cfg
// populated for the buildCreateRequest payload assertions. The Reconciler
// has no Redis/DB wiring — buildCreateRequest is a pure function on
// r.deps.Cfg and the offer/lifecycleID args.
func newReconcilerForBuildTest(jinjaKey, jinjaSHA string, llamaArgsOverride []string) *Reconciler {
	return &Reconciler{
		deps: Deps{
			Cfg: config.Config{
				EmergencyTemplateImage:       "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128",
				EmergencyJinjaTemplateKey:    jinjaKey,
				EmergencyJinjaTemplateSHA256: jinjaSHA,
				EmergencyLlamaArgs:           llamaArgsOverride,
				MinioEndpoint:                "https://s3.example.com",
				MinioBucket:                  "ai-gateway",
				MinioAccessKey:               "AKID-test",
				MinioSecretKey:               "SK-test",
				WeightsQwenKey:               "qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf",
				WeightsQwenSHA256:            "abc123deadbeef",
			},
		},
	}
}

// TestBuildCreateRequest_StrategyB_args verifies the Strategy B Locked
// payload shape (06-WAVE0-GATES.md Decision 4 — supersedes the verbatim
// 15-token Args slice in plan must_haves):
//
//   - Image == EmergencyTemplateImage (NOT ghcr.io/ifixtelecom/ifix-ai-pod)
//   - Runtype == "args"
//   - Onstart == "/bin/bash" (REQUIRED — live lifecycle 35 evidence;
//     Vast API has NO `entrypoint` field, vast-cli coerces --entrypoint
//     into onstart_cmd at api/instances.py:85)
//   - Args has exactly 2 elements: ["-c", <onstart-script>]
//   - Args[1] contains the inline `exec /app/llama-server` with all
//     15 llama-server CLI flags (not in the wire Args slice — bug fix
//     STATE.md:85)
//   - Label uses lifecycle ID
//   - Disk == 40 (WAVE0-GATES Decision 1 — 40 GB opens more spot hosts)
//   - Env map purged of Whisper/BGE-M3 keys (LLM-only emergency pod
//     per CONTEXT.md `<deferred>` line 171)
func TestBuildCreateRequest_StrategyB_args(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 999}, 7)

	require.Equal(t, "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128", req.Image)
	require.Equal(t, "args", req.Runtype)
	require.Equal(t, "/bin/bash", req.Onstart)
	require.Len(t, req.Args, 2, "Strategy B Locked: args=[\"-c\", <script>] only (2 elements)")
	require.Equal(t, "-c", req.Args[0])
	require.Contains(t, req.Args[1], "exec /app/llama-server", "onstart MUST end with exec /app/llama-server so PID 1 == llama-server (spike Round 2 pattern)")
	require.Contains(t, req.Args[1], "--host 0.0.0.0", "default llama-server args MUST be embedded in onstart")
	require.Contains(t, req.Args[1], "--jinja", "default llama-server args MUST be embedded in onstart")
	require.Contains(t, req.Args[1], "/weights/qwen/model.gguf", "onstart MUST reference Qwen weights path")
	require.Contains(t, req.Args[1], "WEIGHTS_QWEN_SHA256", "onstart MUST sha256 verify Qwen weights")
	require.Equal(t, "ifix-emerg-lifecycle-7", req.Label)
	require.Equal(t, 40, req.Disk)

	// LLM-only emergency pod — no Whisper or BGE-M3 env keys.
	_, hasWhisperKey := req.Env["WEIGHTS_WHISPER_KEY"]
	_, hasBgeKey := req.Env["WEIGHTS_BGE_M3_KEY"]
	require.False(t, hasWhisperKey, "WEIGHTS_WHISPER_KEY must be removed — emergency pod is LLM-only (CONTEXT.md deferred line 171)")
	require.False(t, hasBgeKey, "WEIGHTS_BGE_M3_KEY must be removed — emergency pod is LLM-only")

	// MinIO + Qwen creds preserved.
	require.Equal(t, "https://s3.example.com", req.Env["MINIO_ENDPOINT"])
	require.Equal(t, "ai-gateway", req.Env["MINIO_BUCKET"])
	require.Equal(t, "AKID-test", req.Env["MINIO_ACCESS_KEY"])
	require.Equal(t, "SK-test", req.Env["MINIO_SECRET_KEY"])
	require.Equal(t, "qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf", req.Env["WEIGHTS_QWEN_KEY"])
	require.Equal(t, "abc123deadbeef", req.Env["WEIGHTS_QWEN_SHA256"])

	// B2 mode (non-empty Jinja key) — env carries Jinja key + sha256 so
	// onstart-shell can fetch + verify.
	require.Equal(t, "emerg-onstart/templates/foo.jinja", req.Env["EMERGENCY_JINJA_TEMPLATE_KEY"])
	require.Equal(t, "deadbeefSHA", req.Env["EMERGENCY_JINJA_TEMPLATE_SHA256"])
}

// TestBuildCreateRequest_JSONShape verifies the wire-level JSON shape
// of the request body. Critical assertions:
//
//   - Top-level "args" key present, "image_args" + "args_str" absent
//     (VERIFIED via vast-cli/vast.py:2509 RESEARCH.md Pitfall 5 — the
//     server only reads `args`)
//   - "onstart" key present at top level with value "/bin/bash"
//     (Vast API has NO `entrypoint` field — vast-cli coerces
//     --entrypoint into onstart_cmd; live lifecycle 35 proved that
//     sending entrypoint:"/bin/bash" is a no-op and the container
//     fell back to image ENTRYPOINT = llama-server, exiting on bad args)
//   - "runtype" == "args"
func TestBuildCreateRequest_JSONShape(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 42)

	raw, err := json.Marshal(req)
	require.NoError(t, err)
	js := string(raw)

	// Decode + introspect top-level keys.
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &top))

	require.Contains(t, top, "image", "top-level image key must exist")
	require.Contains(t, top, "runtype", "top-level runtype key must exist")
	require.Contains(t, top, "onstart", "top-level onstart key must exist (Strategy B requirement — carries /bin/bash shell)")
	require.Contains(t, top, "args", "top-level args key must exist (Strategy B requirement)")
	require.Contains(t, top, "env", "top-level env key must exist")
	require.Contains(t, top, "disk", "top-level disk key must exist")

	require.NotContains(t, top, "image_args", "wire field is `args`, NOT image_args (vast-cli/vast.py:2509)")
	require.NotContains(t, top, "args_str", "wire field is `args`, NOT args_str")

	// Onstart MUST be /bin/bash so Vast container exec = `/bin/bash -c <script>`.
	var onstartVal string
	require.NoError(t, json.Unmarshal(top["onstart"], &onstartVal))
	require.Equal(t, "/bin/bash", onstartVal, "Strategy B onstart MUST carry /bin/bash; vast wraps as `/bin/bash -c <script>` from args=[\"-c\", ...]")

	require.Contains(t, js, `"runtype":"args"`, "raw JSON sanity check")
	require.Contains(t, js, `"onstart":"/bin/bash"`, "raw JSON sanity check")
}

// TestBuildCreateRequest_DeterministicJSON verifies that two successive
// calls with identical Cfg produce byte-identical JSON. No time.Now, no
// rand. Map ordering would defeat this naively — json.Marshal sorts map
// keys alphabetically, so determinism comes for free as long as the
// inputs are stable.
func TestBuildCreateRequest_DeterministicJSON(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	offer := vast.Offer{ID: 999}

	first, err := json.Marshal(r.buildCreateRequest(offer, 7))
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		next, err := json.Marshal(r.buildCreateRequest(offer, 7))
		require.NoError(t, err)
		require.Equal(t, string(first), string(next), "buildCreateRequest must be deterministic across calls")
	}
}

// TestBuildCreateRequest_JinjaB1Mode — Cfg.EmergencyJinjaTemplateKey
// empty (B1 fallback path) means no Jinja env keys forwarded. The
// onstart's `if [[ -n "${EMERGENCY_JINJA_TEMPLATE_KEY:-}" ]]` block
// short-circuits and llama-server runs WITHOUT --chat-template-file,
// falling back to image-embedded template. NOTE: production config
// defaults non-empty (B2 LOCKED per WAVE0-GATES Decision 1) — this
// test exists to validate the runtime override path for operator
// emergencies where Jinja becomes unavailable.
func TestBuildCreateRequest_JinjaB1Mode(t *testing.T) {
	r := newReconcilerForBuildTest("", "", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)

	_, hasKey := req.Env["EMERGENCY_JINJA_TEMPLATE_KEY"]
	_, hasSHA := req.Env["EMERGENCY_JINJA_TEMPLATE_SHA256"]
	require.False(t, hasKey, "EMERGENCY_JINJA_TEMPLATE_KEY must be absent in B1 mode")
	require.False(t, hasSHA, "EMERGENCY_JINJA_TEMPLATE_SHA256 must be absent in B1 mode")
}

// TestBuildCreateRequest_JinjaB2Mode — Cfg.EmergencyJinjaTemplateKey
// non-empty (B2 production default per WAVE0-GATES Decision 1) means
// both Jinja env keys forwarded. The onstart shell script will fetch
// + sha256-verify the Jinja template from MinIO.
func TestBuildCreateRequest_JinjaB2Mode(t *testing.T) {
	r := newReconcilerForBuildTest(
		"emerg-onstart/templates/qwen3.5-27b-tool-calling-XYZ.jinja",
		"sha256-hex-value",
		nil,
	)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)

	require.Equal(t, "emerg-onstart/templates/qwen3.5-27b-tool-calling-XYZ.jinja", req.Env["EMERGENCY_JINJA_TEMPLATE_KEY"])
	require.Equal(t, "sha256-hex-value", req.Env["EMERGENCY_JINJA_TEMPLATE_SHA256"])
}

// TestBuildCreateRequest_LlamaArgsOverride — operator can override the
// hard-coded llama-server flag slice via EMERGENCY_LLAMA_ARGS env CSV
// (Cfg.EmergencyLlamaArgs). The onstart script's final `exec
// /app/llama-server ...` line uses the override slice when non-nil/
// non-empty; otherwise the 13-flag default. This test verifies the
// override path lands inside the onstart script.
func TestBuildCreateRequest_LlamaArgsOverride(t *testing.T) {
	override := []string{"--port", "9999", "--verbose"}
	r := newReconcilerForBuildTest("", "", override)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)

	require.Len(t, req.Args, 2)
	script := req.Args[1]
	require.Contains(t, script, "exec /app/llama-server --port 9999 --verbose",
		"override slice MUST appear in onstart exec line")
	require.NotContains(t, script, "--host 0.0.0.0",
		"override REPLACES default flags entirely; default --host must not leak")
	require.NotContains(t, script, "--jinja",
		"override REPLACES default flags entirely; default --jinja must not leak")
}

// TestEmergencyOnstart_UnderVastLimit — Pitfall 4 RESEARCH.md:426
// enforcement. Vast API hard limit is 4048 chars; we keep a 2500-char
// budget so a future env var or sha256 check does not unexpectedly
// cross the boundary. Bumped from 1500 → 2500 in lifecycle 36 follow-up
// when the optional debug-SSH bootstrap added ~450 chars (still ~50%
// margin below the Vast cap). If this fails, gzip+base64 the script
// before assembling Args.
func TestEmergencyOnstart_UnderVastLimit(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.Len(t, req.Args, 2)
	require.Less(t, len(req.Args[1]), 2500,
		"onstart script must stay under 2500 chars (Vast 4048 limit, internal margin); gzip+base64 if growth needed")
}

// TestEmergencyOnstart_StartsWithSetE — script MUST begin with `set -e`
// so any failed step (mc download fail, sha256 mismatch) aborts the
// container with a non-zero exit. Without set -e, a silent sha256
// mismatch would still let llama-server start on tampered weights.
func TestEmergencyOnstart_StartsWithSetE(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.Len(t, req.Args, 2)
	require.True(t, strings.HasPrefix(req.Args[1], "set -e"),
		"onstart MUST start with `set -e` so download/sha256 failures abort container (T-06-03 mitigation)")
}

// TestEmergencyOnstart_NoLegacyImage — defensive guard: the legacy
// `ghcr.io/ifixtelecom/ifix-ai-pod` image must NOT appear anywhere
// in the request (Image field nor environment). Phase 6 D-08-B
// (Strategy B Locked) eliminates the custom GHCR image.
func TestEmergencyOnstart_NoLegacyImage(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "ifix-ai-pod",
		"Strategy B Locked: legacy ifix-ai-pod image must be gone (CONTEXT.md D-08-B + STATE.md:85 bug fix)")
}
