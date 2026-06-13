package primary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// VastAPI is the subset of vast.Client methods the primary reconciler
// calls. Mirrors emerg.VastAPI shape — same DTOs, same Vast.ai endpoint
// surface — but declared in this package so primary does not import
// emerg (would create a cycle once Plan 06.6-08 wires the gateway main).
// Production wires *vast.Client; unit tests inject a fake.
type VastAPI interface {
	SearchOffers(ctx context.Context, filter vast.SearchFilter) ([]vast.Offer, error)
	CreateInstance(ctx context.Context, offerID int64, req vast.CreateRequest) (vast.Instance, error)
	GetInstance(ctx context.Context, instanceID int64) (vast.Instance, error)
	DestroyInstance(ctx context.Context, instanceID int64) error
}

// LoaderAdapter is the surface the primary reconciler consumes from the
// upstreams.Loader. Plan 06.6-06b satisfied this interface on the real
// *upstreams.Loader for OverrideTier0/RestoreTier0; Phase 06.7 Plan 03
// added Tier0OverrideURL (the Pitfall #11 re-assert getter) and swapped the
// dynamic primary roster to "llm", "tts" ("embed" left the pod per
// D-03 and is now a static tier-0 row; "stt" left the pod per Phase 11.1
// D-A4 — Whisper deleted, /v1/audio/transcriptions routes to tier-1
// OpenAI-Whisper static row only).
//
// The OverrideTier0 / RestoreTier0 signatures are deliberately void
// (no error return) to match the existing upstreams.Loader.OverrideTier0
// + RestoreTier0 contract — the real implementation never fails; misroute
// is logged at warn level inside the Loader. Refresh keeps its error
// return because reloading enabled-upstreams from Postgres can legitimately
// fail (DB connectivity loss). Tier0OverrideURL reports the active override
// URL for a role (or set=false when the slot is empty) so evaluateReady can
// re-assert a slot cleared by an emerg cutback (Pitfall #11 / D-13).
type LoaderAdapter interface {
	OverrideTier0(role, url string)
	RestoreTier0(role string)
	Tier0OverrideURL(role string) (string, bool)
	Refresh(ctx context.Context) error
}

// DCGMScraperAdapter is the minimal surface the primary reconciler needs
// from the DCGM Prometheus scraper. Plan 06.6-06b's job is to add SetURL
// to the real dcgm.Scraper so the reconciler can point the scraper at the
// new primary pod's :9400/metrics endpoint when StateReady fires AND
// blank it when the lifecycle closes (StateDestroying → StateAsleep).
type DCGMScraperAdapter interface {
	SetURL(url string)
}

// InflightAdapter is the minimal surface the primary reconciler needs
// from the shed.InflightRegistry. Plan 06.6-06b's job is to add Count on
// the real *shed.InflightRegistry (wrapping the existing GlobalInflight)
// so the reconciler can sum local-llm + local-embed inflight (Phase 11.1
// D-A4: local-stt term removed — Whisper deleted from pod and DB)
// during evaluateDraining (drain-complete gate: inflight==0 OR grace
// elapsed → transition Draining→Destroying).
type InflightAdapter interface {
	Count(upstream string) int64
}

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
	// Phase 11.2 D-B5′: ErrMissingWhisperSHA restored (revert 11.1 D-A4 —
	// tier-0 Speaches/Whisper STT is back on the primary pod).
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
// upstream loader (LLM/STT/TTS tier-0 overrides) and the DCGM scraper
// (SetURL). LLM/STT/TTS use the public IP + host-port mapping for
// 8000/8001/8003; DCGM uses the host-port mapping for 9400.
//
// Phase 06.7 (D-03/D-11): embed left the pod (relocated to a 24/7 CPU
// host, static tier-0 row), and Chatterbox TTS took its container slot on
// port 8003. The dynamic tier-0 roster the reconciler drives is now
// {llm,stt,tts}.
type primaryPodURLs struct {
	LLM  string
	STT  string // Phase 11.2 D-B5′: restored (revert 11.1 D-A4 — speaches back on pod)
	TTS  string
	DCGM string
}

// Deps is the full wiring for the primary Reconciler after Plan 06.6-06a.
// Plan 06.6-04 shipped a 4-field minimal Deps (Cfg/Log/Vast/HealthCheck);
// this plan extends it with FSM, DB queries, Redis lock, Inflight counter,
// schedule.Rule, upstream Loader, DCGM scraper, and replica identifier.
//
// All fields except TickInterval / ReplicaID have no defaults — wire them
// at construction time in Plan 06.6-08 main.go. Unit tests fill only the
// fields the code paths under test exercise.
type Deps struct {
	// Cfg holds Phase 6.6 primary-pod knobs (PrimaryPodSchedule* +
	// PrimaryProvision* + MinIO creds + image SHAs).
	Cfg config.Config

	// Log is the structured logger. nil defaults to slog.Default(); the
	// reconciler attaches `subsystem=primary.reconciler` + the replica ID
	// at Start.
	Log *slog.Logger

	// Vast is the Vast.ai REST client. Implements VastAPI subset; tests
	// inject a fake to avoid live HTTP calls.
	Vast VastAPI

	// HealthCheck is the per-endpoint /health probe. Returns true when the
	// URL responds 2xx within an internal timeout. Tests override with a
	// scriptable bool-returning closure (so the 4-endpoint health gate in
	// evaluateProvisioning can be exercised deterministically).
	HealthCheck func(ctx context.Context, url string) bool

	// Reachable is the connection-LEVEL reachability probe for Option B
	// (CR-01 6.6.Y review). Given a pod URL it performs a cheap TCP dial to
	// the URL's host:port and returns:
	//
	//   - true  → the host accepted the connection OR refused it (the host
	//             is NAT-published and responding; services may still be
	//             booting — a legitimate cold start, keep polling).
	//   - false → connection-level failure (dial timeout / no route): the
	//             host never NAT-published its port. This is the EXACT spike
	//             signature (running + populated Vast ports map yet TCP
	//             timeout from external vantage points for 40+ min).
	//
	// This is the gate Option B MUST key on — NOT buildPodURLs / the Vast
	// ports map (6.6.Y-01 spike DIRECTIVE), and NOT the HTTP HealthCheck
	// (which cannot distinguish "host unreachable" from "host up, service
	// not ready yet" — killing the latter would defeat the cold-start
	// budget that intentionally allows slow post-reachability weight
	// downloads). When nil the port-bind budget gate is skipped (no
	// false-positive destroys); the cold-start budget remains the backstop.
	// Tests inject a scriptable closure.
	Reachable func(ctx context.Context, url string) bool

	// Loader is the upstream loader (3-role tier-0 override target). Plan
	// 06.6-06b satisfies LoaderAdapter on the real *upstreams.Loader.
	Loader LoaderAdapter

	// DCGMScraper points the GPU-metrics scraper at the new primary pod's
	// :9400/metrics endpoint when StateReady fires (and clears it on
	// closeLifecycle). Plan 06.6-06b adds SetURL to the real dcgm.Scraper.
	DCGMScraper DCGMScraperAdapter

	// Inflight reports the per-upstream in-flight request count consumed by
	// evaluateDraining (drain-complete gate). Plan 06.6-06b adds Count to
	// the real *shed.InflightRegistry.
	Inflight InflightAdapter

	// FSM is the in-process 5-state primary FSM (Plan 06.6-05). MUST be
	// non-nil — State() / Transition / SetState drive the dispatcher.
	FSM *FSM

	// Rule is the immutable schedule rule (Plan 06.6-05). ShouldBeProvisioned
	// gates evaluateAsleep; IsInPeak gates evaluateReady drain trigger.
	Rule ScheduleRule

	// DB is the Postgres pool used for sqlc-generated query bindings:
	// InsertPrimaryLifecycle / UpdatePrimaryLifecycleVastIDs /
	// MarkPrimaryLifecycleHealthy / MarkPrimaryLifecycleDraining /
	// ClosePrimaryLifecycle / GetOpenPrimaryLifecycle.
	DB *pgxpool.Pool

	// Redis is the go-redis v9 client used for redsync leader election
	// (gw:primary:lock) + Pub/Sub (gw:primary:events) for the event
	// subscriber + state mirror writes (gw:primary:state Hash).
	Redis *redis.Client

	// ReplicaID identifies this gateway replica. Defaults to os.Hostname()
	// in NewReconcilerFull when empty. Tagged on every PublishPrimaryEvent
	// so cross-replica observers can attribute publishes.
	ReplicaID string
}

// Reconciler drives the primary pod's 5-state FSM (Asleep | Provisioning
// | Ready | Draining | Destroying). The struct is constructed at gateway
// boot (Plan 06.6-08 main.go wiring) and started via Start(ctx); the
// reconciler holds gw:primary:lock as leader and dispatches FSM
// transitions at 1Hz.
type Reconciler struct {
	deps Deps
	cfg  config.Config
	rule ScheduleRule

	// activePodURLs is the per-service URL snapshot of the running primary
	// pod (set by markReady, cleared by closeLifecycle). Lockless read via
	// ActivePodURLs() — safe for cross-goroutine hot-path consumers.
	activePodURLs atomic.Pointer[primaryPodURLs]

	// activeInstanceID is the Vast.ai instance ID of the running primary
	// pod (0 when none). Used by evaluateDestroying to call
	// vastutil.BestEffortDestroy + by cancelActiveLifecycle.
	activeInstanceID atomic.Int64

	// activeLifecycleID is the primary_lifecycles row ID of the currently
	// in-flight lifecycle (0 when none). Set by startProvisioning right
	// after InsertPrimaryLifecycle returns; consulted by markReady /
	// closeLifecycle / recoverOpenLifecycle.
	activeLifecycleID atomic.Int64

	// drainStartedAt captures the wall-clock time at which evaluateReady
	// transitioned Ready→Draining. evaluateDraining uses it to decide
	// "grace elapsed?" (now.Sub(*drainStartedAt) >= GraceRampDownS).
	drainStartedAt atomic.Pointer[time.Time]

	// lastProvisionFailureAt is the wall-clock time of the most recent
	// provisioning failure (cold-start timeout, vast_create_error,
	// vast_status_msg_error per reviews #11, offer_race_lost). The
	// evaluateAsleep cooldown gate refuses to re-provision until
	// PrimaryProvisionFailureCooldownSeconds has elapsed since this
	// timestamp — the T-06.6-04 schedule-oscillation mitigation.
	lastProvisionFailureAt atomic.Pointer[time.Time]

	// isLeader is true iff this replica currently holds gw:primary:lock.
	// Set by runScheduleLoop's redsync LockContext path; consulted by the
	// event subscriber so non-leaders observe events without acting on them
	// (PRV-03 single-leader invariant parity with emerg).
	isLeader atomic.Bool

	// lifecycleCancel holds the context-cancel func for the in-flight
	// provisioning goroutine. Plan 06.6-06a startProvisioning stores it;
	// cancelActiveLifecycle swaps to nil + invokes it on operator force-
	// down or schedule wrap-up while mid-provisioning.
	lifecycleCancel atomic.Pointer[context.CancelFunc]

	// queriesOverride is the test-only injection slot for the sqlc query
	// handle. Production leaves this nil — the queries() helper builds a
	// *gen.Queries from Deps.DB on demand. Tests inject a fake DBTX-backed
	// *gen.Queries via SetQueriesForTest so the reconciler can exercise
	// the SQL paths without standing up a real *pgxpool.Pool.
	queriesOverride atomic.Pointer[gen.Queries]

	// Phase 12 Plan 02 (RES-11): Ready-tick death-poll strike counters. The
	// reconciler polls Vast for the tracked instance on EVERY Ready tick
	// (evaluateReady) and confirms a dead pod via the same 3-strike pattern
	// waitForReadyOrDestroy uses during provisioning. Unlike that in-loop
	// counter (a function local — the loop is ONE call), each Ready tick is a
	// SEPARATE evaluateReady call, so the strike counters MUST persist across
	// ticks on the struct. Guarded by deathStrikeMu because evaluateReady runs
	// on the schedule-loop goroutine and markReady (which resets them on the
	// Provisioning→Ready transition) runs on the provisioning goroutine.
	deathStrikeMu   sync.Mutex
	terminalStrikes int
	notFoundStrikes int

	// billingSuppressedAt is the wall-clock time of the most-recent
	// CONFIRMED billing-stop death (Phase 12 Plan 02, D-01). evaluateAsleep
	// checks it: while the suppression window is active (set by a billing-stop
	// death, cleared by a successful provision / operator force-up), the
	// schedule loop SKIPS re-provision so a zero-credit pod does not enter a
	// provision-fail loop. nil = no active suppression. This is a SUPPRESSION
	// FLAG checked by the existing schedule evaluator — NOT new retry
	// machinery (D-01: a flag is allowed, retry logic is not).
	billingSuppressedAt atomic.Pointer[time.Time]
}

// NewReconciler constructs a Reconciler with the given Deps. cfg is
// copied from Deps.Cfg into a top-level field so subsequent methods can
// read the operator config without dereferencing Deps each call.
//
// Defaults applied here keep Plan 06.6-04 test fixtures (which pass only
// Cfg+Log) working: nil Log → slog.Default(); empty ReplicaID is left
// untouched (the caller in Plan 06.6-08 main.go will populate via
// os.Hostname()). Rule is populated from the cfg via ParseScheduleEnv at
// construction time when not pre-built — but failures bubble up to the
// caller (Plan 06.6-08 is responsible for catching the fail-fast Pitfall
// #4 timezone error at gateway boot).
func NewReconciler(deps Deps) *Reconciler {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	r := &Reconciler{
		deps: deps,
		cfg:  deps.Cfg,
		rule: deps.Rule,
	}
	return r
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
	// Phase 11.2 D-B5′: whisper SHA gate restored (revert 11.1 D-A4).
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
		"-p 8003:8003": "1", // TTS (chatterbox) — Phase 06.7 D-11 (was embed:8002)
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
		"PRIMARY_QWEN_WEIGHTS_KEY":    cfg.PrimaryQwenWeightsKey,
		"PRIMARY_QWEN_WEIGHTS_SHA256": cfg.PrimaryQwenWeightsSHA256,
		// Phase 11.2 D-B5′: PRIMARY_WHISPER_WEIGHTS_* restored (revert 11.1 D-A4 —
		// tier-0 Speaches/Whisper STT is back on the primary pod, consumed by
		// pod/scripts/download-weights.sh + pod/primary/supervisord.conf
		// [program:speaches].
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

// podPortURL extracts a public service URL from a Vast.ai instance for the
// given container port + readiness path suffix. Mirrors the emerg
// `podHealthURL` shape: returns "" when inst is not yet ready (no IP, no
// port mapping) — the caller treats empty as "keep polling" (W6 fix
// parity).
//
// Wave 0 LOCKED supervisord 4-services model: container ports 8000 (LLM,
// llama-server /v1/models), 8001 (STT, speaches /health), 8002 (embed,
// infinity /health), 9400 (DCGM exporter /metrics). All 4 land inside ONE
// container's network namespace — children of supervisord PID 1. The
// reconciler does not know about supervisord (orchestration opaque); it
// only polls 4 HTTP endpoints on Vast-exposed host ports.
func (r *Reconciler) podPortURL(inst vast.Instance, containerPort, pathSuffix string) string {
	if inst.PublicIPAddr == "" {
		return ""
	}
	bindings, ok := inst.Ports[containerPort+"/tcp"]
	if !ok || len(bindings) == 0 {
		return ""
	}
	hostPort := bindings[0].HostPort
	if hostPort == "" {
		return ""
	}
	return "http://" + inst.PublicIPAddr + ":" + hostPort + pathSuffix
}

// podLLMURL extracts the public LLM endpoint (8000/tcp -> /v1/models) from
// a running Vast.ai instance. Returns "" when the instance is not yet
// ready to serve traffic (caller treats as "keep polling").
func (r *Reconciler) podLLMURL(inst vast.Instance) string {
	return r.podPortURL(inst, "8000", "/v1/models")
}

// podSTTURL extracts the public STT endpoint (8001/tcp -> /health) from a
// running Vast.ai instance.
func (r *Reconciler) podSTTURL(inst vast.Instance) string {
	return r.podPortURL(inst, "8001", "/health")
}

// podTTSURL extracts the public TTS endpoint (8003/tcp -> /health) from a
// running Vast.ai instance. Phase 06.7 (D-11) — Chatterbox replaced the
// Infinity embed child on the pod; the container port moved 8002 -> 8003.
func (r *Reconciler) podTTSURL(inst vast.Instance) string {
	return r.podPortURL(inst, "8003", "/health")
}

// podDCGMURL extracts the public DCGM endpoint (9400/tcp -> /metrics) from
// a running Vast.ai instance.
func (r *Reconciler) podDCGMURL(inst vast.Instance) string {
	return r.podPortURL(inst, "9400", "/metrics")
}

// roleURL maps a dynamic primary role ("llm"/"stt"/"tts") to its raw
// per-service URL (with readiness suffix) from a primaryPodURLs snapshot.
// Used by the evaluateReady Pitfall #11 re-assert loop (D-13) to recover
// the pod URL for a tier-0 slot an emerg cutback cleared. "embed" is NOT a
// dynamic primary role post-Phase-06.7 (D-03) and returns "". Phase 11.2
// (D-B5′) restored "stt" to the dynamic roster after Phase 11.1 D-A4
// dropped it.
func roleURL(urls primaryPodURLs, role string) string {
	switch role {
	case "llm":
		return urls.LLM
	case "stt":
		return urls.STT
	case "tts":
		return urls.TTS
	default:
		return ""
	}
}

// stripPrimaryReadinessSuffix removes the readiness-probe suffix from a
// pod URL so the upstream loader's tier-0 override receives the BASE URL
// (parity with emerg.stripHealthSuffix). For LLM 8000 the suffix is
// "/v1/models"; for STT 8001 and embed 8002 it is "/health". DCGM 9400
// uses "/metrics" which the scraper expects in full — DCGM URLs are NOT
// stripped (handled by callers directly).
func stripPrimaryReadinessSuffix(u string) string {
	for _, suffix := range []string{"/v1/models", "/health"} {
		if len(u) > len(suffix) && u[len(u)-len(suffix):] == suffix {
			return u[:len(u)-len(suffix)]
		}
	}
	return u
}
