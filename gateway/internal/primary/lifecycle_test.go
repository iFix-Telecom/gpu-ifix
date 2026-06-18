// Package primary (lifecycle_test.go): unit tests covering Plan 06.6-04
// supervisord invariants, Wave 0 LOCKED defaults (b9191 + B1 embedded
// Jinja + supervisord-not-DinD), shell hardening (reviews #7), SHA
// fail-fast (reviews #6), and structural assertions on
// pod/primary/Dockerfile + pod/primary/supervisord.conf.
package primary

import (
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"

	"os"
)

// Wave 0 LOCKED image digest literals — these are the EXACT strings the
// gateway emits to Vast.ai (cfg.PrimaryTemplateImage default per
// Plan 06.6-03) and the EXACT strings baked into pod/primary/Dockerfile
// FROM lines (per Task 1). Asserting them here freezes the SHA-pin
// contract at the test layer; any drift in either config defaults or
// the Dockerfile would surface as a CI test failure rather than as a
// silent supply-chain change at runtime.
const (
	wave0LlamaImage    = "ghcr.io/ggml-org/llama.cpp:server-cuda-b9191@sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3"
	wave0InfinityImage = "michaelf34/infinity:0.0.77@sha256:11e8b3921b9f1a58965afaad4a844c435c9807cbc82c51e47cb147b7d977fc88"
	wave0DCGMImage     = "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless@sha256:60d3b00ac80b4ae77f94dae2f943685605585ad9e92fdccda3154d009ae317cc"
)

// cfgWithDefaults returns a config.Config populated with the Wave 0
// LOCKED defaults (per Plan 06.6-03 + WAVE0-GATES Decisions 1, 3) plus
// non-empty Whisper / BGE-M3 SHA256 test values so buildCreateRequest's
// fail-fast gate does NOT bail out. Each test that wants to exercise a
// fail-fast path zeroes the relevant SHA on a local copy.
func cfgWithDefaults() config.Config {
	return config.Config{
		PrimaryTemplateImage: wave0LlamaImage,
		PrimaryInfinityImage: wave0InfinityImage,
		PrimaryDCGMImage:     wave0DCGMImage,

		// SEED-019 part 3: PRIMARY_POD_SERVE_STT is DELETED. The "stt" tier-0
		// override is now gated on the pod-reported whisper_device value
		// (primaryPodURLs.WhisperDevice, via Deps.DeviceReport) rather than a
		// config flag — so there is no PrimaryPodServeSTT field to seed here.

		// Qwen GGUF — Wave 0 verified digest (per 06.6-WAVE0-GATES.md
		// Decision 3 default).
		PrimaryQwenWeightsKey:    "qwen3.6-27b-Q4_K_M/v1.0.0/model.gguf",
		PrimaryQwenWeightsSHA256: "a7cbd3ecc0e3f9b333edee61ae66bc87ed713c5d49587a8355814722ed329e0f",

		// B1 GGUF-embedded Jinja default (WAVE0-GATES Decision 3).
		PrimaryQwenJinjaKey:    "",
		PrimaryQwenJinjaSHA256: "",

		// PrimaryLlamaArgs empty → primaryLlamaArgsDefault is used.
		PrimaryLlamaArgs: nil,

		// Whisper + BGE-M3 — empty by default in config (per reviews #6
		// fail-fast policy). Test fixture provides non-empty placeholders
		// so the precondition gate passes; the dedicated empty-SHA tests
		// zero them explicitly. Phase 11.2 D-B5′ restored Whisper fields
		// (Phase 11.1 D-A4 had removed them).
		PrimaryWhisperWeightsKey:       "whisper-large-v3/v1.0.0/model.tar.gz",
		PrimaryWhisperWeightsSHA256:    "wh1sp3rsh4test256",
		PrimaryBGEM3WeightsKey:         "bge-m3/v1.0.0/model.tar.gz",
		PrimaryBGEM3WeightsSHA256:      "bg3m35h4test256",
		PrimaryChatterboxWeightsKey:    "chatterbox-mtl-v2/v1.0.0/cache.tar.gz",
		PrimaryChatterboxWeightsSHA256: "ch4tt3rb0xsh4test256",

		// MinIO 4 credentials (test values).
		MinioEndpoint:  "https://s3.example.com",
		MinioBucket:    "ai-gateway",
		MinioAccessKey: "AKID-test",
		MinioSecretKey: "SK-test",

		// Off by default.
		PodDebugSSHPublicKey: "",
	}
}

// newReconcilerWith returns a Reconciler ready for buildCreateRequest
// payload assertions. No DB / Redis / Vast wiring needed because
// buildCreateRequest is a pure function on cfg + offer + lifecycleID.
func newReconcilerWith(cfg config.Config) *Reconciler {
	return &Reconciler{
		deps: Deps{Cfg: cfg},
		cfg:  cfg,
	}
}

// TestBuildPrimaryCreateRequest_Supervisord — happy path for the
// supervisord-bundled custom-image create request shape.
func TestBuildPrimaryCreateRequest_Supervisord(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 999}, 7)
	require.NoError(t, err)

	require.Equal(t, wave0LlamaImage, req.Image)
	require.Equal(t, "args", req.Runtype)
	require.Equal(t, "/bin/bash", req.Onstart)
	require.Len(t, req.Args, 2)
	require.Equal(t, "-c", req.Args[0])

	require.Contains(t, req.Args[1], "exec /usr/bin/supervisord", "supervisord PID 1 invariant")
	require.Contains(t, req.Args[1], "aria2c", "multi-stream weight download")
	require.Contains(t, req.Args[1], "/weights/qwen/model.gguf", "Qwen weights path")
	require.Contains(t, req.Args[1], "set -euo pipefail", "reviews #7 shell hardening")

	require.Equal(t, 50, req.Disk)
	require.Equal(t, "ifix-primary-lifecycle-7", req.Label)
	require.Equal(t, "running", req.TargetState)

	require.Equal(t, "https://s3.example.com", req.Env["MINIO_ENDPOINT"])
	require.Equal(t, "ai-gateway", req.Env["MINIO_BUCKET"])
	require.Equal(t, "AKID-test", req.Env["MINIO_ACCESS_KEY"])
	require.Equal(t, "SK-test", req.Env["MINIO_SECRET_KEY"])
}

// TestBuildPrimaryCreateRequest_Has4PortMappings — Pitfall #8 (4
// port forwards on the pod: 8000 LLM + 8001 STT + 8003 TTS + 9400 DCGM).
// Phase 06.7 (D-11): the embed:8002 forward was replaced by the
// Chatterbox tts:8003 forward; 8002 must NOT be present.
func TestBuildPrimaryCreateRequest_Has4PortMappings(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	for _, key := range []string{"-p 8000:8000", "-p 8001:8001", "-p 8003:8003", "-p 9400:9400"} {
		require.Equal(t, "1", req.Env[key], "port mapping %s must be present with value \"1\"", key)
	}
	_, has8002 := req.Env["-p 8002:8002"]
	require.False(t, has8002, "embed port 8002 must NOT be forwarded (embed left the pod, D-03)")
}

// TestBuildPrimaryCreateRequest_MinIOCredentialsNotEmpty — all 4 MinIO
// credentials must be forwarded into the pod env so the in-pod
// `: "${MINIO_*:?required}"` guards don't fail.
func TestBuildPrimaryCreateRequest_MinIOCredentialsNotEmpty(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	for _, key := range []string{"MINIO_ENDPOINT", "MINIO_BUCKET", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY"} {
		v, ok := req.Env[key]
		require.True(t, ok, "env key %s must be present", key)
		require.NotEmpty(t, v, "env key %s must be non-empty", key)
	}
}

// TestBuildPrimaryCreateRequest_JinjaEmbeddedMode — B1 default (Jinja
// key empty). PRIMARY_QWEN_JINJA_KEY / SHA256 must be ABSENT from env so
// the in-pod conditional fetch is skipped; the conditional `if [ -n
// "${PRIMARY_QWEN_JINJA_KEY:-}" ]` check string lives inside the static
// onstart script (asserted on req.Args[1]).
func TestBuildPrimaryCreateRequest_JinjaEmbeddedMode(t *testing.T) {
	c := cfgWithDefaults()
	c.PrimaryQwenJinjaKey = ""
	c.PrimaryQwenJinjaSHA256 = ""
	r := newReconcilerWith(c)
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	_, hasKey := req.Env["PRIMARY_QWEN_JINJA_KEY"]
	_, hasSHA := req.Env["PRIMARY_QWEN_JINJA_SHA256"]
	require.False(t, hasKey, "B1 embedded: PRIMARY_QWEN_JINJA_KEY must be absent")
	require.False(t, hasSHA, "B1 embedded: PRIMARY_QWEN_JINJA_SHA256 must be absent")
	require.Contains(t, req.Args[1], `if [ -n "${PRIMARY_QWEN_JINJA_KEY:-}" ]`,
		"static conditional Jinja-fetch block must be present in onstart for B2 fallback path")
}

// TestBuildPrimaryCreateRequest_JinjaMinIOFallback — B2 mode (Jinja key
// non-empty). PRIMARY_QWEN_JINJA_KEY / SHA256 must both be forwarded so
// the in-pod conditional fetches + verifies the Jinja template.
func TestBuildPrimaryCreateRequest_JinjaMinIOFallback(t *testing.T) {
	c := cfgWithDefaults()
	c.PrimaryQwenJinjaKey = "qwen-templates/qwen3.6-tools.jinja"
	c.PrimaryQwenJinjaSHA256 = "deadbeefjinja256"
	r := newReconcilerWith(c)
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.Equal(t, "qwen-templates/qwen3.6-tools.jinja", req.Env["PRIMARY_QWEN_JINJA_KEY"])
	require.Equal(t, "deadbeefjinja256", req.Env["PRIMARY_QWEN_JINJA_SHA256"])
}

// TestBuildPrimaryCreateRequest_TemplateImageIsB9191 — Wave 0 LOCKED
// engine image. The default must be the b9191 SHA-pinned digest;
// b9128 (Phase 6 emergency tag) is missing Qwen3.6 SSM support per
// 06.6-SPIKE-qwen3.6-jinja.md Round 3.
func TestBuildPrimaryCreateRequest_TemplateImageIsB9191(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.Contains(t, req.Image, "server-cuda-b9191")
	require.Contains(t, req.Image, "@sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3")
}

// TestPrimaryOnstartLengthBelowLimit — Vast onstart has a generous limit
// (~16 KB), but we keep a 14 KB regression net so silent growth shows
// up at CI time.
func TestPrimaryOnstartLengthBelowLimit(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)
	require.Less(t, len(req.Args[1]), 14000, "onstart must stay under 14 KB regression budget")
}

// TestPrimaryOnstart_StartsWithSetEuo — reviews #7 shell hardening: bash
// strict mode is `set -euo pipefail` (NOT `set -euxo` — `x` xtrace would
// leak MinIO credentials in pod logs via `mc alias set`).
func TestPrimaryOnstart_StartsWithSetEuo(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	// Reviews #7: bash strict mode must be present (the optional sshd
	// debug block runs before `set -euo` so the operator can still SSH
	// in when env-var checks fail; that block is independently safe via
	// `${VAR:-}` defaults). Look anywhere in the script.
	require.Contains(t, req.Args[1], "set -euo pipefail")
	require.NotContains(t, req.Args[1], "set -euxo")
}

// TestPrimaryOnstart_NoSetX_NoSecretLeak — reviews #7: full-script grep
// for any xtrace toggle (`set -x` / `set -euxo`). Zero occurrences.
func TestPrimaryOnstart_NoSetX_NoSecretLeak(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.NotContains(t, req.Args[1], "set -x", "xtrace mode is forbidden")
	require.NotContains(t, req.Args[1], "set -euxo", "xtrace mode is forbidden")
}

// TestPrimaryOnstart_NoDinD — Wave 0 LOCKED rejection of DinD. The
// docker daemon must not be referenced anywhere in the onstart bash.
func TestPrimaryOnstart_NoDinD(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.NotContains(t, req.Args[1], "dockerd",
		"Wave 0 REJECTED DinD per 06.6-SPIKE-dind-privileged.md — supervisord LOCKED")
}

// TestPrimaryOnstart_NoDockerd — explicit secondary assertion: no
// `service docker start` invocation, no daemon-binary path.
func TestPrimaryOnstart_NoDockerd(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.False(t,
		strings.Contains(req.Args[1], "/usr/bin/dockerd") ||
			strings.Contains(req.Args[1], "service docker start"),
		"Wave 0 LOCKED: no docker daemon start commands allowed in primary onstart")
}

// TestPrimaryOnstart_NoNestedDockerRun — Wave 0 LOCKED. The 4 services
// are supervisord child processes, NOT nested `docker run` sidecars.
func TestPrimaryOnstart_NoNestedDockerRun(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.NotContains(t, req.Args[1], "docker run -d",
		"Wave 0 LOCKED: 4 services are supervisord child processes, NOT nested docker run sidecars")
}

// TestPrimaryOnstart_ExecSupervisordIsLastLine — supervisord becomes
// PID 1 via `exec /usr/bin/supervisord -n -c
// /etc/supervisor/conf.d/services.conf` as the LAST line of the onstart
// (Pitfall #2 invariant).
func TestPrimaryOnstart_ExecSupervisordIsLastLine(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	last := strings.TrimSpace(req.Args[1])
	require.True(t,
		strings.HasSuffix(last, "exec /usr/bin/supervisord -n -c /etc/supervisor/conf.d/services.conf"),
		"supervisord must be PID 1; last line of onstart must exec it directly (Pitfall #2)")
}

// TestPrimaryOnstart_HasRequiredEnvGuards — reviews #7: 10 mandatory
// `: "${VAR:?required}"` guards covering MinIO 4 + 3 weight key/sha
// pairs.
func TestPrimaryOnstart_HasRequiredEnvGuards(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	for _, guard := range []string{
		`: "${MINIO_ENDPOINT:?required}"`,
		`: "${MINIO_BUCKET:?required}"`,
		`: "${MINIO_ACCESS_KEY:?required}"`,
		`: "${MINIO_SECRET_KEY:?required}"`,
		`: "${PRIMARY_QWEN_WEIGHTS_KEY:?required}"`,
		`: "${PRIMARY_QWEN_WEIGHTS_SHA256:?required}"`,
		// Phase 11.1 D-A4: PRIMARY_WHISPER_WEIGHTS_* removed.
		`: "${PRIMARY_BGEM3_WEIGHTS_KEY:?required}"`,
		`: "${PRIMARY_BGEM3_WEIGHTS_SHA256:?required}"`,
	} {
		require.Contains(t, req.Args[1], guard, "required env guard %q must be present", guard)
	}
}

// TestPrimaryOnstart_QuotedEnvExpansions — reviews #7: every expansion
// of a MinIO / PRIMARY env var must live inside a double-quoted string
// so bash word-splitting / globbing cannot mangle a value containing
// whitespace or shell metacharacters. The check enumerates each
// expansion site and walks the surrounding line to assert it sits
// inside a matched pair of double quotes.
func TestPrimaryOnstart_QuotedEnvExpansions(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	// Find every line that references a $MINIO_* or $PRIMARY_*
	// expansion (skip `: "${VAR:?required}"` guards — those are
	// already inside `"..."`). For each remaining expansion site, walk
	// backwards from the `$` and assert an odd number of unescaped
	// double-quote characters preceded it on the same line (i.e. the
	// expansion is currently inside a quoted run).
	expansion := regexp.MustCompile(`\$(MINIO|PRIMARY)_[A-Z0-9_]+`)

	for lineNum, line := range strings.Split(req.Args[1], "\n") {
		for _, idx := range expansion.FindAllStringIndex(line, -1) {
			// Count unescaped `"` chars before idx[0].
			quoteCount := 0
			for i := 0; i < idx[0]; i++ {
				if line[i] == '"' && (i == 0 || line[i-1] != '\\') {
					quoteCount++
				}
			}
			require.Equalf(t, 1, quoteCount%2,
				"line %d expansion %q must be inside a double-quoted run (saw %d preceding unescaped quotes): %q",
				lineNum+1, line[idx[0]:idx[1]], quoteCount, line)
		}
	}
}

// TestPrimaryOnstart_NoLegacyImage — defensive guard: the legacy
// `ghcr.io/ifixtelecom/ifix-ai-pod` image must not appear anywhere
// (Phase 1 namespace, deleted in Phase 6 PR2).
func TestPrimaryOnstart_NoLegacyImage(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.NotContains(t, req.Args[1], "ifix-ai-pod",
		"Phase 1 legacy ifix-ai-pod image must be gone")
	require.NotContains(t, req.Image, "ifix-ai-pod")
}

// TestPrimaryOnstart_QwenWeightsInjected — quoted $PRIMARY_QWEN_WEIGHTS_*
// expansions appear in the bash (the in-pod aria2c download uses them).
func TestPrimaryOnstart_QwenWeightsInjected(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.Contains(t, req.Args[1], `"$PRIMARY_QWEN_WEIGHTS_KEY"`)
	require.Contains(t, req.Args[1], `"$PRIMARY_QWEN_WEIGHTS_SHA256"`)
}

// TestPrimaryOnstart_AriaC_NotMcCp — Wave 0: aria2c multi-stream replaces
// mc cp single-stream which empirically failed on 16 GB GGUF download
// (06.6-SPIKE-qwen3.6-jinja.md Round 1 EOF failure). `mc share download`
// is allowed (presigned URL fetch), but `mc cp` for weight download is
// not.
func TestPrimaryOnstart_AriaC_NotMcCp(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.Contains(t, req.Args[1], "aria2c", "weight download must use aria2c multi-stream")
	require.NotRegexp(t, regexp.MustCompile(`mc\s+cp\s+ifix/`), req.Args[1],
		"mc cp single-stream weight download is forbidden (Wave 0 spike Round 1 EOF failure)")
}

// TestBuildPrimaryCreateRequest_SSHDebugConditional — PodDebugSSHPublicKey
// set → env has POD_DEBUG_SSH_PUBLIC_KEY + `-p 22:22`; empty → both absent.
func TestBuildPrimaryCreateRequest_SSHDebugConditional(t *testing.T) {
	t.Run("ssh enabled", func(t *testing.T) {
		c := cfgWithDefaults()
		c.PodDebugSSHPublicKey = "ssh-ed25519 AAAATESTKEY operator@laptop"
		r := newReconcilerWith(c)
		req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
		require.NoError(t, err)

		require.Equal(t, "ssh-ed25519 AAAATESTKEY operator@laptop", req.Env["POD_DEBUG_SSH_PUBLIC_KEY"])
		require.Equal(t, "1", req.Env["-p 22:22"])
	})

	t.Run("ssh disabled", func(t *testing.T) {
		r := newReconcilerWith(cfgWithDefaults())
		req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
		require.NoError(t, err)

		_, hasKey := req.Env["POD_DEBUG_SSH_PUBLIC_KEY"]
		_, hasPort := req.Env["-p 22:22"]
		require.False(t, hasKey, "POD_DEBUG_SSH_PUBLIC_KEY must be absent when cfg key empty")
		require.False(t, hasPort, "-p 22:22 must be absent when cfg key empty")
	})
}

// TestBuildPrimaryCreateRequest_D03_EngineImagePinnedSHA — D-03
// traceability: cfg.PrimaryTemplateImage default carries an @sha256:
// digest (no floating tag).
func TestBuildPrimaryCreateRequest_D03_EngineImagePinnedSHA(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.Contains(t, req.Image, "@sha256:", "engine image must be SHA-pinned (D-03 traceability)")
}

// TestPrimaryOnstart_DCGMPort9400Preserved — D-07 DCGM port mapping
// is preserved in the pod env. The in-container exporter binds to
// 0.0.0.0:9400 via supervisord.conf (asserted separately in
// TestSupervisordConf_4ProgramBlocks).
func TestPrimaryOnstart_DCGMPort9400Preserved(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	require.Equal(t, "1", req.Env["-p 9400:9400"])
}

// Phase 11.1 D-A4: TestBuildPrimaryCreateRequest_RejectsEmptyWhisperSHA
// removed — PRIMARY_WHISPER_WEIGHTS_* fields no longer exist (STT shrunk
// to tier-1-only).

// TestBuildPrimaryCreateRequest_RejectsEmptyBGEM3SHA — reviews #6
// fail-fast: empty PRIMARY_BGEM3_WEIGHTS_SHA256 must return
// ErrMissingBGEM3SHA and a zero CreateRequest.
func TestBuildPrimaryCreateRequest_RejectsEmptyBGEM3SHA(t *testing.T) {
	c := cfgWithDefaults()
	c.PrimaryBGEM3WeightsSHA256 = ""
	r := newReconcilerWith(c)
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)

	require.ErrorIs(t, err, ErrMissingBGEM3SHA)
	require.Equal(t, vast.CreateRequest{}, req)
}

// TestBuildPrimaryCreateRequest_RejectsEmptyQwenSHA — defensive: empty
// PRIMARY_QWEN_WEIGHTS_SHA256 must return ErrMissingQwenSHA.
func TestBuildPrimaryCreateRequest_RejectsEmptyQwenSHA(t *testing.T) {
	c := cfgWithDefaults()
	c.PrimaryQwenWeightsSHA256 = ""
	r := newReconcilerWith(c)
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)

	require.ErrorIs(t, err, ErrMissingQwenSHA)
	require.Equal(t, vast.CreateRequest{}, req)
}

// TestBuildPrimaryCreateRequest_NoSidecarImageEnv — Wave 0: the 3
// non-engine upstream images (Speaches, Infinity, DCGM) are
// build-time-only refs consumed by pod/primary/Dockerfile multi-stage
// FROM lines. They must NOT appear in the pod runtime env (the
// container already carries the extracted binaries).
func TestBuildPrimaryCreateRequest_NoSidecarImageEnv(t *testing.T) {
	r := newReconcilerWith(cfgWithDefaults())
	req, err := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.NoError(t, err)

	for _, key := range []string{"PRIMARY_INFINITY_IMAGE", "PRIMARY_DCGM_IMAGE"} {
		_, present := req.Env[key]
		require.False(t, present, "sidecar image env %s must NOT be passed at runtime (build-time-only)", key)
	}
}

// repoRoot resolves the repository root by walking up from this test
// file (runtime.Caller(0)) — 3 levels up from gateway/internal/primary/
// lands at the repo root. Works regardless of `go test` cwd (package
// dir vs repo root).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller(0) must succeed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// TestSupervisordConf_4ProgramBlocks — Phase 11.2 D-B5′ structural assertion
// on pod/primary/supervisord.conf: nodaemon=true + exactly 4 [program:*]
// blocks (llama, speaches, chatterbox, dcgm) + --jinja flag on llama, no
// --chat-template-file (B1 embedded LOCKED). Phase 06.7 D-03: infinity embed
// stays removed; Phase 11.2 D-B5′ restored [program:speaches] (Phase 11.1
// D-A4 had removed it).
func TestSupervisordConf_3ProgramBlocks(t *testing.T) {
	confPath := filepath.Join(repoRoot(t), "pod", "primary", "supervisord.conf")
	data, err := os.ReadFile(confPath)
	require.NoError(t, err, "supervisord.conf must exist at %s", confPath)
	src := string(data)

	require.Contains(t, src, "[supervisord]", "must declare [supervisord] section")
	require.Contains(t, src, "nodaemon=true", "PID 1 invariant")
	progRe := regexp.MustCompile(`(?m)^\[program:([a-z0-9_-]+)\]`)
	activePrograms := map[string]bool{}
	for _, m := range progRe.FindAllStringSubmatch(src, -1) {
		activePrograms[m[1]] = true
	}
	require.True(t, activePrograms["llama"], "must have [program:llama]")
	require.True(t, activePrograms["speaches"], "Phase 11.2 D-B5′: must have [program:speaches] STT child (restored)")
	require.True(t, activePrograms["chatterbox"], "Phase 06.7 D-05: must have [program:chatterbox] TTS child")
	require.False(t, activePrograms["infinity"], "Phase 06.7 D-03: [program:infinity] embed must be removed (relocated off the pod)")
	require.True(t, activePrograms["dcgm"], "must have [program:dcgm]")
	require.Contains(t, src, "--jinja", "llama command must include --jinja (B1 embedded LOCKED)")
	require.NotContains(t, src, "--chat-template-file", "B1 embedded LOCKED: no --chat-template-file flag")

	require.Contains(t, src, "0.0.0.0:9400", "dcgm-exporter must bind to 0.0.0.0:9400 (D-07)")
}

// TestDockerfile_HybridStages — Phase 11.2 D-B5′ structural assertion on
// pod/primary/Dockerfile: SHA-pinned FROM stages preserved + Speaches venv
// restored. Phase 11.2 D-B5′ reverted Phase 11.1 D-A4 (Speaches/Whisper
// back on the pod). Phase 06.7 D-03 stays in effect — Infinity venv stays
// removed (embed relocated off-pod). SHA-pin invariant preserved for
// runtime-critical llama.cpp b9191 (Qwen3.6 SSM tensor support —
// non-substitutable).
func TestDockerfile_HybridStages(t *testing.T) {
	dockerfilePath := filepath.Join(repoRoot(t), "pod", "primary", "Dockerfile")
	data, err := os.ReadFile(dockerfilePath)
	require.NoError(t, err, "Dockerfile must exist at %s", dockerfilePath)
	src := string(data)

	fromLine := regexp.MustCompile(`(?m)^FROM\s+\S+@sha256:[0-9a-f]{64}\s+AS\s+\S+`)
	matches := fromLine.FindAllString(src, -1)
	require.GreaterOrEqual(t, len(matches), 2,
		"Dockerfile must have at least 2 SHA-pinned FROM ... AS ... lines (dcgm-stage + final; speaches stage allowed)")

	require.Contains(t, src, "server-cuda-b9191@sha256:cb37",
		"final stage must use llama.cpp b9191 (Wave 0 UPGRADED from b9128)")
	require.Contains(t, src, "COPY --from=dcgm-stage")
	require.Regexp(t, regexp.MustCompile(`(?s)apt-get\s+install.*supervisor`), src,
		"final stage must install supervisor via apt-get")
	require.NotRegexp(t, regexp.MustCompile(`(?s)pip\s+install.*infinity-emb`), src,
		"Phase 06.7 D-03: infinity venv must NOT be installed (embed off-pod)")
}

// TestPrimaryLlamaArgsDefault_NoChatTemplateFile — Wave 0 Decision 3
// B1 embedded LOCKED: the default llama args slice must NOT include
// --chat-template-file (Qwen3.6 GGUF carries the chat_template).
func TestPrimaryLlamaArgsDefault_NoChatTemplateFile(t *testing.T) {
	joined := strings.Join(primaryLlamaArgsDefault, " ")
	require.NotContains(t, joined, "--chat-template-file",
		"Wave 0 B1 embedded LOCKED: primaryLlamaArgsDefault must NOT include --chat-template-file")
	require.Contains(t, joined, "--jinja",
		"Wave 0 B1 embedded LOCKED: primaryLlamaArgsDefault must include --jinja")
}

// ---------------------------------------------------------------------------
// Phase 11.2 Plan 01 — Wave 0 RED stubs for primary STT lifecycle restore
// (D-B5′ revert of 11.1-01). OWNER: Plan 03 — restores primaryPodURLs.STT
// field + port mapping + WHISPER env injection at lifecycle.go
// :104/:322/:341 per PATTERNS.md lines 341-361.
// ---------------------------------------------------------------------------

// TestPrimaryPodURLs_HasSTTField asserts the primaryPodURLs struct has
// an "STT" field of type string (Phase 11.2 D-B5′ revert of 11.1-01).
func TestPrimaryPodURLs_HasSTTField(t *testing.T) {
	field, ok := reflect.TypeOf(primaryPodURLs{}).FieldByName("STT")
	require.True(t, ok, "primaryPodURLs must have an STT field (Phase 11.2 D-B5′)")
	require.Equal(t, reflect.String, field.Type.Kind(),
		"primaryPodURLs.STT must be of type string")
}

// TestProvisionArgs_Includes_Port8001 asserts that buildCreateRequest
// injects the -p 8001:8001 port mapping for speaches STT (Phase 11.2
// D-B5′ — restored after Phase 11.1 D-A4 removed it).
func TestProvisionArgs_Includes_Port8001(t *testing.T) {
	cfg := testCfg(t)
	r := buildReconciler(t, Deps{Cfg: cfg})
	offer := vast.Offer{ID: 12345, GpuName: "RTX 3090"}
	req, err := r.buildCreateRequest(offer, 7)
	require.NoError(t, err)
	require.Contains(t, req.Env, "-p 8001:8001",
		"provision env MUST include -p 8001:8001 (Phase 11.2 D-B5′ — speaches STT port restored)")
	require.Equal(t, "1", req.Env["-p 8001:8001"],
		"-p 8001:8001 value must be \"1\" (Vast.ai port-forward indicator)")
}

// TestProvisionEnv_Injects_PRIMARY_WHISPER_WEIGHTS_KEY_And_SHA256 asserts
// that buildCreateRequest injects PRIMARY_WHISPER_WEIGHTS_KEY and
// PRIMARY_WHISPER_WEIGHTS_SHA256 (Phase 11.2 D-B5′ — restored after
// Phase 11.1 D-A4 removed them).
func TestProvisionEnv_Injects_PRIMARY_WHISPER_WEIGHTS_KEY_And_SHA256(t *testing.T) {
	cfg := testCfg(t)
	r := buildReconciler(t, Deps{Cfg: cfg})
	offer := vast.Offer{ID: 12345, GpuName: "RTX 3090"}
	req, err := r.buildCreateRequest(offer, 7)
	require.NoError(t, err)
	require.Equal(t, cfg.PrimaryWhisperWeightsKey, req.Env["PRIMARY_WHISPER_WEIGHTS_KEY"],
		"PRIMARY_WHISPER_WEIGHTS_KEY must come from cfg (Phase 11.2 D-B5′)")
	require.Equal(t, cfg.PrimaryWhisperWeightsSHA256, req.Env["PRIMARY_WHISPER_WEIGHTS_SHA256"],
		"PRIMARY_WHISPER_WEIGHTS_SHA256 must come from cfg (Phase 11.2 D-B5′)")
}
