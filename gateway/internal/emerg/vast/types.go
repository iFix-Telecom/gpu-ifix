// Package vast — DTOs for the Vast.ai REST API (https://console.vast.ai/api/v0).
//
// Field shapes were captured empirically during the Phase 6 spike
// (.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-SPIKE-vast-port-mapping.md).
// Key insights driving struct design:
//
//   - The search filter (`SearchFilter`) is a `map[string]any` rather than a
//     typed struct. Vast.ai changed its filter schema during this project's
//     lifetime (now requires every value to be a dictionary like
//     {"eq": "RTX 4090"} or {"lte": 0.40}); a typed struct would silently
//     break on the next iteration. The map gives operators the freedom to
//     change one knob without recompiling.
//
//   - `Instance.Ports` is `map[string][]PortBinding` keyed by
//     "{container_port}/tcp" (Docker convention). The W6 invariant is that
//     this map is **empty until `actual_status == "running"`**, so callers
//     of `podHealthURL` MUST guard against `len(Ports[...]) == 0`.
//
//   - `PortBinding.HostPort` is a STRING in the JSON response (Docker's
//     ContainerPortMap.HostPort is also a string), even though it always
//     parses cleanly to int. We do NOT auto-convert here — the Phase 6
//     `podHealthURL` does the strconv at use-site.
//
//   - `vastErrorEnvelope` accepts both `msg` and `message` keys because the
//     API returns inconsistent shapes for 4xx vs 5xx responses (RESEARCH
//     lines 745-779). `Error` carries the machine-readable code
//     ("no_such_ask", "no_such_instance", "bad_request").
//
//   - Phase 6 Strategy B Locked (CONTEXT.md D-06-B + D-07-B): `Runtype="args"`
//     preserves the image ENTRYPOINT and passes `Args []string` as the JSON
//     `args` REMAINDER field (NOT `image_args`, NOT `args_str` — VERIFIED via
//     vast-cli/vast.py:2509). Earlier Phase 6.5 lifecycles used `Runtype="ssh"`
//     which silently overrode the image CMD (STATE.md:85 — bug fixed here).
//     The llama.cpp:server-cuda image's ENTRYPOINT is `llama-server` direct,
//     so Strategy B ALSO requires `Entrypoint: "/bin/bash"` override to run a
//     bootstrap bash script (06-SPIKE-runtype-args.md empirical Round 2 —
//     `--onstart-cmd` does NOT shell-wrap in args runtype).
package vast

// Offer is one row of the `offers` array returned by GET /bundles?q=...
//
// `ID` is the ask_id used as the path parameter in PUT /asks/{id}/. Other
// fields are subset of the ~80-field row Vast returns; we only project
// the ones the Phase 6 reconciler reads (filter eligibility + audit).
type Offer struct {
	ID          int64   `json:"id"` // ask_id used in PUT /asks/{id}/
	GpuName     string  `json:"gpu_name"`
	NumGpus     int     `json:"num_gpus"`
	DphTotal    float64 `json:"dph_total"` // dollars per hour, total (GPU + disk + bandwidth)
	Reliability float64 `json:"reliability"`
	InetDown    float64 `json:"inet_down"` // Mbps; some hosts publish fractional like 5453.6
	CudaMaxGood float64 `json:"cuda_max_good"`
	MachineID   int64   `json:"machine_id"`
	HostID      int64   `json:"host_id"`
	Geolocation string  `json:"geolocation"`
	Rentable    bool    `json:"rentable"`
}

// PortBinding is one entry in the `ports["{container_port}/tcp"]` array
// returned by GET /instances/{id}/. Mirrors Docker's NetworkSettings.Ports
// shape: HostIp can be "0.0.0.0" or "::" (IPv6); HostPort is a string
// representation of the mapped public port.
type PortBinding struct {
	HostIP   string `json:"HostIp"`   // Docker capitalisation — "Ip" not "IP"
	HostPort string `json:"HostPort"` // STRING in API; caller does strconv.Atoi
}

// Instance is the body of GET /instances/{id}/ at `instances.{...}`.
//
// `ActualStatus` is the source of truth for "is the pod ready" — it
// progresses loading → running on success, or loading → exited / unknown /
// offline on failure. See Pitfall 9 in 06-RESEARCH.md.
//
// `Ports` is populated only after `ActualStatus == "running"` (see
// 06-SPIKE-vast-port-mapping.md). Pre-running, the field is `{}` or
// absent. The Phase 6 reconciler treats an empty Ports as "not yet
// ready, keep polling" rather than as a transient HTTP error (W6 fix).
type Instance struct {
	ID             int64                    `json:"id"`
	ActualStatus   string                   `json:"actual_status"` // running|exited|unknown|offline|loading|scheduling
	IntendedStatus string                   `json:"intended_status"`
	SshHost        string                   `json:"ssh_host"`
	SshPort        int                      `json:"ssh_port"`
	PublicIPAddr   string                   `json:"public_ipaddr"`
	MachineID      int64                    `json:"machine_id"`
	HostID         int64                    `json:"host_id"`
	DphTotal       float64                  `json:"dph_total"`
	ImageUUID      string                   `json:"image_uuid"`
	Label          string                   `json:"label"`
	Ports          map[string][]PortBinding `json:"ports"`
	// StatusMsg surfaces operator-actionable Vast.ai status messages — most
	// often empty during normal lifecycles, but populated with strings like
	// "Error: Container failed to start" or "GPU error" when the host
	// rejects the create / boot fails. Phase 6.6 Plan 06.6-06a reads it
	// inside evaluateProvisioning's poll loop per reviews suggestion #11
	// (06.6-REVIEWS.md): if non-empty AND case-insensitive-contains "error",
	// the reconciler aborts provisioning, closes the lifecycle with a
	// forensic reason `vast_status_msg_error:<msg>`, and enters the cooldown
	// gate so the next tick does not immediately re-bid. Carries the
	// lifecycle-29 forensics fix from STATE.md.
	StatusMsg string `json:"status_msg"`
}

// IsActive returns true when the instance is in a non-terminal state. Used
// by leader-recovery (Plan 06-07) to decide whether an orphan lifecycle's
// instance should be resumed (active) or destroyed-and-closed (terminal).
//
// "scheduling" is treated as active because Vast.ai uses it briefly during
// the create handshake before flipping to "loading"; treating it as
// terminal would race the create flow.
func (i Instance) IsActive() bool {
	switch i.ActualStatus {
	case "running", "loading", "scheduling":
		return true
	}
	return false
}

// IsTerminal returns true when the instance entered a state from which it
// cannot recover without operator intervention. Used by `waitForReadyOrDestroy`
// to short-circuit the cold-start budget per Pitfall 9.
func (i Instance) IsTerminal() bool {
	switch i.ActualStatus {
	case "exited", "unknown", "offline":
		return true
	}
	return false
}

// CreateRequest is the JSON body of PUT /asks/{offer_id}/.
//
// `Env` map keys are the literal strings Vast.ai uses to forward `docker
// run -p ...` flags — e.g. `"-p 9100:9100"` mapped to `"1"`. The peculiar
// shape is documented in `pod/scripts/vast-ai.sh` (Phase 1 reference).
//
// `Onstart` is a bash script (not a base64 wrapper) — the gateway sends
// the script body directly; Vast handles base64 encoding internally.
//
// `TargetState` defaults to "running" if omitted — kept explicit so test
// fixtures can stop the instance for the leader-recovery zombie test.
type CreateRequest struct {
	ClientID string            `json:"client_id"` // always "me"
	Image    string            `json:"image"`
	Env      map[string]string `json:"env"`
	Onstart  string            `json:"onstart"`
	// Runtype values: "args" (Phase 6 Strategy B Locked — preserves image
	// ENTRYPOINT, args field is REMAINDER list passed to entrypoint),
	// "ssh_proxy" (Vast injects sshd sidecar; REPLACES ENTRYPOINT with
	// vast-ai/base-image chain — see RESEARCH.md Pitfall 1),
	// "ssh" (deprecated alias for ssh_proxy; Phase 6 root cause STATE.md:85
	// bug — CMD silently ignored, lifecycles 29-33 timeouts).
	Runtype string `json:"runtype"`
	// Entrypoint overrides the Docker image ENTRYPOINT when set. Required by
	// Strategy B (Phase 6) — the llama.cpp:server-cuda image has ENTRYPOINT
	// `llama-server` direct, so to run a bash bootstrap (curl Jinja from MinIO,
	// sha256 verify, exec llama-server) we MUST set Entrypoint="/bin/bash" and
	// pass Args=["-c","<script>"]. Empirically validated in
	// 06-SPIKE-runtype-args.md Round 2 (Vast.ai CLI flag `--entrypoint`).
	// Omitempty so legacy ssh/ssh_proxy runtypes do not send the field.
	Entrypoint string `json:"entrypoint,omitempty"`
	// Args is the JSON `args` field (NOT image_args, NOT args_str — VERIFIED
	// via vast-cli/vast.py:2509 `json_blob["args"] = args.args`, RESEARCH.md
	// Pitfall 5 line 436). Array of CLI tokens passed REMAINDER-style to the
	// image ENTRYPOINT when Runtype="args". Omitempty so ssh_proxy / ssh
	// runtypes do not send the field on the wire.
	Args        []string `json:"args,omitempty"`
	Disk        int      `json:"disk"`
	Label       string   `json:"label"`
	TargetState string   `json:"target_state,omitempty"` // "running" default
}

// CreateResponse is the body returned by PUT /asks/{offer_id}/. `NewContract`
// is the instance_id the caller passes to subsequent GetInstance /
// DestroyInstance calls.
type CreateResponse struct {
	Success     bool  `json:"success"`
	NewContract int64 `json:"new_contract"`
}

// SearchFilter is the q-parameter for GET /bundles?q=... Marshalled to
// JSON and URL-encoded into the query string.
//
// **WARNING:** this is `map[string]any` rather than a typed struct
// because Vast.ai's filter schema is in flux — every value must currently
// be a dictionary like {"eq": ...}, {"gte": ...}, or {"lte": ...}, but
// this changed within the project's lifetime. The map keeps the wire
// format flexible. Use `DefaultSearchFilter` to construct the canonical
// Phase 6 filter.
type SearchFilter map[string]any

// DefaultSearchFilter returns the canonical Phase 6 filter per CONTEXT.md
// D-A2: ≥0.99 reliability, ≤cap dph_total, ≥200 Mbps inet_down,
// ≥12.8 cuda_max_good, driver ≥570, ordered by dph_total ascending limit 20.
//
// inet_down lowered from 500 → 200 Mbps on 2026-05-28 after the 2×3090 inventory
// survey showed the entire EU/DE/PL fleet sits in the 246–311 Mbps band — the
// 500 gate excluded every reasonable EU offer and forced provisioning into
// $1+/hr long-haul hosts (Oman, US). 200 Mbps still completes the ~17GB cold-
// start weight download in <10 min, which fits inside coldstart_budget_seconds
// (2400s default). Tightening back to 500 stays a per-deploy operator option
// once EU 3090 inventory inet capacity catches up.
//
// `gpuName` is the gpu_name eq filter — emerg pods default to "RTX 4090"
// (cheap fallback for the LLM-only emerg path); primary pods default to
// "RTX 5090" (32 GB VRAM fits Qwen 27B + bge-m3 + KV cache + whisper-large-v3
// GPU offload; the 4090's 24 GB cannot fit STT GPU per UAT 16 CUDA OOM
// finding 2026-05-19).
//
// Driver version gate (UAT 17 2026-05-19): the llama.cpp:server-cuda-b9191
// image bundles CUDA 12.8 runtime libraries which require host driver
// ≥570 (NVIDIA driver/CUDA compatibility matrix). Host 79.160.189.79
// (driver 565.57.01) silently fell back llama-server + speaches to CPU
// — 0.49 tok/s vs 50 tok/s GPU baseline. Vast `driver_vers` is encoded
// as MAJOR*1000000 + MINOR*1000 + PATCH (e.g. 570.86.16 → 570086016);
// `gte: 570000000` excludes any driver below 570.0.0.
//
// CUDA gate (UAT 18 2026-05-19): bumped 12.6 → 12.8 because Vast's
// `cuda_max_good gte 12.6` filter has a regression specifically on
// RTX 5090 queries that returns zero offers despite EU 5090s reporting
// cuda_max_good=12.7/13.0/13.1/13.2 in the unfiltered listing.
// `gte 12.8` returns the expected 4 EU Spain offers (~$2.00/h, host
// 309734 driver 590.48.01 cuda 13.1). 12.8 also matches the b9191
// image's actual CUDA runtime requirement, so this is a tightening,
// not a workaround. RTX 4090 queries unaffected — `gte 12.4/12.6/13`
// all return 5 offers.
//
// `primaryHostID` excludes the primary's host when known (>0); pass 0 to
// disable the host_id filter when the primary host is unknown (D-A2).
// `numGPUs` is the exact GPU count per machine (`num_gpus: {eq: numGPUs}`);
// values <=0 fall back to 1. Pass 2 for a 2×RTX 3090 single-pod topology
// (48GB total; llama.cpp auto-tensor-splits Qwen across both GPUs) — emerg
// passes 1 (LLM-only fallback never needs multi-GPU).
// machineBlocklist (optional, variadic): machine_ids excluded from the search
// via `machine_id: {notin: [...]}`. Use to catalog and skip hosts that fail to
// boot the pod (e.g. multi-GPU machines with broken CDI on non-zero GPU slots,
// which crash container create with "unresolvable CDI devices gpu=N").
func DefaultSearchFilter(maxDPH float64, primaryHostID int64, gpuName string, numGPUs int, machineBlocklist ...int64) SearchFilter {
	if numGPUs <= 0 {
		numGPUs = 1
	}
	f := SearchFilter{
		"gpu_name":      map[string]any{"eq": gpuName},
		"num_gpus":      map[string]any{"eq": numGPUs},
		"reliability":   map[string]any{"gte": 0.99},
		"dph_total":     map[string]any{"lte": maxDPH},
		"inet_down":     map[string]any{"gte": 200},
		"cuda_max_good": map[string]any{"gte": 12.8},
		"driver_vers":   map[string]any{"gte": 570000000},
		"rentable":      map[string]any{"eq": true},
		"order":         []any{[]any{"dph_total", "asc"}},
		"limit":         20,
	}
	if primaryHostID > 0 {
		f["host_id"] = map[string]any{"neq": primaryHostID}
	}
	if len(machineBlocklist) > 0 {
		ids := make([]any, len(machineBlocklist))
		for i, id := range machineBlocklist {
			ids[i] = id
		}
		f["machine_id"] = map[string]any{"notin": ids}
	}
	return f
}

// DefaultSearchFilters builds a [primary, fallback] SearchFilter pair per
// Phase 11.1 D-A6. The reconciler iterates the pair and breaks on the
// first non-empty offer list — primary shape preferred, fallback only when
// the primary cap returns zero qualified offers.
//
// Defaults: primary = 1×RTX 3090 @ $0.30; fallback = 2×RTX 3090 @ $0.60
// (same GPU model both shapes, single CDI/driver matrix; Wave 0
// EVIDENCE-00 found 7 EU offers within the fallback cap).
//
// primaryNumGPUs / fallbackNumGPUs are SPLIT so the fallback shape can ask
// for a different GPU-per-machine count than the primary (e.g. 1 primary →
// 2 fallback). Both filters carry the same primaryHostID exclusion +
// machineBlocklist, so the variadic blocklist parses identically.
func DefaultSearchFilters(primaryCap, fallbackCap float64,
	primaryHostID int64,
	primaryGPU, fallbackGPU string,
	primaryNumGPUs, fallbackNumGPUs int,
	blocklist ...int64) []SearchFilter {
	return []SearchFilter{
		DefaultSearchFilter(primaryCap, primaryHostID, primaryGPU, primaryNumGPUs, blocklist...),
		DefaultSearchFilter(fallbackCap, primaryHostID, fallbackGPU, fallbackNumGPUs, blocklist...),
	}
}

// WithMachineAllowlist returns a shallow copy of f with the machine_id clause
// REPLACED by `{in: allowlist}` — restricting the search to the preferred
// machine_ids only. Used by the primary reconciler's allowlist-first pass
// (PRIMARY_VAST_MACHINE_ALLOWLIST). Returns f unchanged when allowlist is
// empty. The `in` clause overwrites any blocklist `notin` clause set by
// DefaultSearchFilter (an explicit allowlist is the more specific intent;
// the listed machines are already known-good, so the blocklist is moot).
//
// This is a PREFERENCE pass: the reconciler falls back to the unrestricted
// (blocklist-only) filter when this allowlist-scoped search yields no offers,
// because Vast is a spot marketplace where the preferred host may be busy.
func WithMachineAllowlist(f SearchFilter, allowlist []int64) SearchFilter {
	if len(allowlist) == 0 {
		return f
	}
	out := make(SearchFilter, len(f)+1)
	for k, v := range f {
		out[k] = v
	}
	ids := make([]any, len(allowlist))
	for i, id := range allowlist {
		ids[i] = id
	}
	out["machine_id"] = map[string]any{"in": ids}
	return out
}

// vastErrorEnvelope captures the variable-shape error body Vast returns
// on 4xx/5xx. Both `msg` and `message` are observed across endpoints; the
// caller's parseErrorBody falls back to `msg` first, then `message`, then
// the raw body.
type vastErrorEnvelope struct {
	Success bool   `json:"success,omitempty"`
	Error   string `json:"error,omitempty"`
	Msg     string `json:"msg,omitempty"`
	Message string `json:"message,omitempty"`
	AskID   int64  `json:"ask_id,omitempty"`
}
