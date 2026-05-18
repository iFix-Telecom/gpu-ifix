package primary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/vastutil"
)

// Sentinel errors used by buildCreateRequest to fail fast when the
// operator left a critical weight SHA256 unset. Reviews consensus action
// #6 (06.6-REVIEWS.md): empty SHA would silently no-op the in-pod
// `sha256sum -c -` check (sha256sum prints a warning but exit 0 when the
// expected hash is empty, allowing tampered weights to be served). The
// gateway refuses to build the create-instance payload in that case and
// lets the reconciler transition to Cooldown with forensic-grade events.
var (
	ErrMissingQwenSHA = errors.New(
		"primary: PRIMARY_QWEN_WEIGHTS_SHA256 is empty — refusing to build pod request",
	)
	ErrMissingWhisperSHA = errors.New(
		"primary: PRIMARY_WHISPER_WEIGHTS_SHA256 is empty — operator must set this env var explicitly (no default shipped)",
	)
	ErrMissingBGEM3SHA = errors.New(
		"primary: PRIMARY_BGEM3_WEIGHTS_SHA256 is empty — operator must set this env var explicitly (no default shipped)",
	)
)

// primaryPodURLs collects the 4 per-service URLs (one per supervisord
// child) exposed by the running pod on Vast.ai. Populated by the
// reconciler in Plan 06.6-06a once GetInstance reports a running state
// and the public_ipaddr + ports map are populated; consumed by the
// upstream loader (LLM/STT/embed tier-0 overrides) and the DCGM scraper
// (SetURL). LLM/STT/Embed use the public IP + host-port mapping for
// 8000/8001/8002; DCGM uses the host-port mapping for 9400.
type primaryPodURLs struct {
	LLM   string
	STT   string
	Embed string
	DCGM  string
}

// Deps is the minimal viable wiring for the primary Reconciler at the
// end of Plan 06.6-04. Plan 06.6-06a extends it with FSM, DB queries,
// Redis lock, Inflight counter, schedule.Rule, upstream Loader, and
// dcgm.Scraper. Kept narrow here so this plan compiles standalone and
// the downstream plans can grow the struct organically without breaking
// Plan 06.6-04 tests.
type Deps struct {
	Cfg         config.Config
	Log         *slog.Logger
	Vast        vastutil.VastDestroyer
	HealthCheck func(ctx context.Context, url string) bool
}

// Reconciler drives the primary pod's 5-state FSM (Asleep | Provisioning
// | Ready | Draining | Destroying). Plan 06.6-04 only declares the struct
// + constructor + buildCreateRequest; Plan 06.6-06a extends it with
// startProvisioning / waitForReadyOrDestroy / markReady / closeLifecycle
// flows mirroring the emerg package shape.
type Reconciler struct {
	deps           Deps
	cfg            config.Config
	activePodURLs  atomic.Pointer[primaryPodURLs]
	rule           ScheduleRule
	drainStartedAt atomic.Pointer[time.Time]
}

// NewReconciler constructs a Reconciler with the given Deps. cfg is
// copied from Deps.Cfg into a top-level field so subsequent methods can
// read the operator config without dereferencing Deps each call.
func NewReconciler(deps Deps) *Reconciler {
	return &Reconciler{
		deps: deps,
		cfg:  deps.Cfg,
	}
}

// ActivePodURLs returns the currently active primary pod URLs (one per
// service), or nil if no pod has reached the Ready state. Plan 06.6-06a
// populates this pointer in markReady; Plan 06.6-06b consumers
// (upstream loader, DCGM scraper, voice-api proxy) read from it via
// this getter.
func (r *Reconciler) ActivePodURLs() *primaryPodURLs {
	return r.activePodURLs.Load()
}

// buildCreateRequest assembles the CreateRequest body for PUT
// /asks/{id}/ used to create the primary pod on Vast.ai.
//
// Differences vs the emergency-pod buildCreateRequest (gateway/internal/
// emerg/lifecycle.go:756):
//
//   - Image: cfg.PrimaryTemplateImage (Wave 0 SHA-pinned to llama.cpp
//     server-cuda-b9191, override allowed for the custom converseai-
//     primary-pod image once GHA build-primary-pod publishes it).
//   - Env: 4 port forwards (8000 LLM + 8001 STT + 8002 embed + 9400
//     DCGM per Pitfall #8) instead of 1; 3 weight key/sha pairs (Qwen +
//     Whisper + BGE-M3) instead of 1; PRIMARY_SPEACHES_IMAGE /
//     INFINITY_IMAGE / DCGM_IMAGE are NOT passed at runtime — they are
//     consumed only at custom-image build time by
//     pod/primary/Dockerfile multi-stage FROM lines.
//   - Disk request 50 GB (WAVE0-GATES Decision 4 host filter is 40 GB;
//     the pod asks for fifty gigabytes to leave ~11 GB headroom above
//     ~29 GB total payload of Qwen GGUF 16 GB + image overhead ~3 GB
//   - Whisper ~3 GB + BGE-M3 ~2 GB + workspace ~5 GB).
//   - Label: "ifix-primary-lifecycle-<id>" (distinct from emerg's
//     "ifix-emerg-lifecycle-<id>" for forensic separation).
//
// Reviews #6 SHA fail-fast: refuses to build the request when any of
// the 3 weight SHA256s is empty. The reconciler logs the explicit
// sentinel error and transitions to Cooldown without ever calling
// Vast CreateInstance.
func (r *Reconciler) buildCreateRequest(offer vast.Offer, lifecycleID int64) (vast.CreateRequest, error) {
	cfg := r.cfg

	if cfg.PrimaryQwenWeightsSHA256 == "" {
		return vast.CreateRequest{}, ErrMissingQwenSHA
	}
	if cfg.PrimaryWhisperWeightsSHA256 == "" {
		return vast.CreateRequest{}, ErrMissingWhisperSHA
	}
	if cfg.PrimaryBGEM3WeightsSHA256 == "" {
		return vast.CreateRequest{}, ErrMissingBGEM3SHA
	}

	llamaArgs := primaryLlamaArgsDefault
	if len(cfg.PrimaryLlamaArgs) > 0 {
		llamaArgs = cfg.PrimaryLlamaArgs
	}

	jinjaPath := ""
	if cfg.PrimaryQwenJinjaKey != "" {
		jinjaPath = "/app/templates/qwen3.6.jinja"
	}

	onstart := buildPrimaryOnstart(llamaArgs, jinjaPath)

	env := map[string]string{
		// 4 port forwards (Pitfall #8 RESEARCH.md:494-525) — keys are
		// the literal `-p HOST:CONTAINER` strings that Vast.ai forwards
		// to `docker run -p ...` at instance create time.
		"-p 8000:8000": "1", // LLM (llama-server)
		"-p 8001:8001": "1", // STT (speaches)
		"-p 8002:8002": "1", // embeddings (infinity)
		"-p 9400:9400": "1", // GPU metrics (dcgm-exporter)

		// MinIO 4 credentials — must all be non-empty per the in-pod
		// `: "${MINIO_*:?required}"` guards. The gateway does not
		// guard them at this layer because cfg already validates them
		// at startup via env.MustLoad (Plan 06.6-03 + emerg parity).
		"MINIO_ENDPOINT":   cfg.MinioEndpoint,
		"MINIO_BUCKET":     cfg.MinioBucket,
		"MINIO_ACCESS_KEY": cfg.MinioAccessKey,
		"MINIO_SECRET_KEY": cfg.MinioSecretKey,

		// 3 weight key/sha pairs — keys are MinIO object paths,
		// sha256s drive in-pod sha256sum -c verify (T-06.6-02
		// mitigation). All 3 SHA256s are guaranteed non-empty here
		// because the precondition gate above bails out otherwise.
		"PRIMARY_QWEN_WEIGHTS_KEY":       cfg.PrimaryQwenWeightsKey,
		"PRIMARY_QWEN_WEIGHTS_SHA256":    cfg.PrimaryQwenWeightsSHA256,
		"PRIMARY_WHISPER_WEIGHTS_KEY":    cfg.PrimaryWhisperWeightsKey,
		"PRIMARY_WHISPER_WEIGHTS_SHA256": cfg.PrimaryWhisperWeightsSHA256,
		"PRIMARY_BGEM3_WEIGHTS_KEY":      cfg.PrimaryBGEM3WeightsKey,
		"PRIMARY_BGEM3_WEIGHTS_SHA256":   cfg.PrimaryBGEM3WeightsSHA256,
	}

	if cfg.PrimaryQwenJinjaKey != "" {
		// B2 fallback: the pod fetches a custom Jinja template from
		// MinIO when this env var is set. Default empty (B1 embedded
		// LOCKED per Wave 0 Decision 3).
		env["PRIMARY_QWEN_JINJA_KEY"] = cfg.PrimaryQwenJinjaKey
		env["PRIMARY_QWEN_JINJA_SHA256"] = cfg.PrimaryQwenJinjaSHA256
	}

	if cfg.PodDebugSSHPublicKey != "" {
		// Operator debug SSH (off by default; only when env set in
		// Portainer/.env). The pod installs sshd inline at onstart
		// and Vast maps container port 22 to a random host port.
		env["POD_DEBUG_SSH_PUBLIC_KEY"] = cfg.PodDebugSSHPublicKey
		env["-p 22:22"] = "1"
	}

	// Note: PRIMARY_SPEACHES_IMAGE / PRIMARY_INFINITY_IMAGE /
	// PRIMARY_DCGM_IMAGE are intentionally NOT included in the env
	// map — they are build-time-only refs consumed by
	// pod/primary/Dockerfile multi-stage FROM lines. At runtime the
	// service binaries are already at /app/speaches-bin /app/infinity-bin
	// and /usr/bin/dcgm-exporter, baked into the custom image.

	return vast.CreateRequest{
		ClientID:    "me",
		Image:       cfg.PrimaryTemplateImage,
		Env:         env,
		Onstart:     "/bin/bash",
		Runtype:     "args",
		Args:        []string{"-c", onstart},
		Disk:        50,
		Label:       fmt.Sprintf("ifix-primary-lifecycle-%d", lifecycleID),
		TargetState: "running",
	}, nil
}

// podLLMURL extracts the public LLM endpoint (8000/tcp -> /v1/models) from
// a running Vast.ai instance. Stub returning "" in this plan; Plan
// 06.6-06a fills in the full host-port-mapping extraction (mirrors the
// emerg podHealthURL pattern at lifecycle.go:564-577).
func (r *Reconciler) podLLMURL(inst vast.Instance) string {
	_ = inst
	return ""
}

// podSTTURL extracts the public STT endpoint (8001/tcp -> /health) from a
// running Vast.ai instance. Stub — Plan 06.6-06a fills in.
func (r *Reconciler) podSTTURL(inst vast.Instance) string {
	_ = inst
	return ""
}

// podEmbedURL extracts the public embed endpoint (8002/tcp -> /health) from
// a running Vast.ai instance. Stub — Plan 06.6-06a fills in.
func (r *Reconciler) podEmbedURL(inst vast.Instance) string {
	_ = inst
	return ""
}

// podDCGMURL extracts the public DCGM endpoint (9400/tcp -> /metrics) from
// a running Vast.ai instance. Stub — Plan 06.6-06a fills in.
func (r *Reconciler) podDCGMURL(inst vast.Instance) string {
	_ = inst
	return ""
}
