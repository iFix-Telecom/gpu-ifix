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
// D-A2: RTX 4090, ≥0.99 reliability, ≤cap dph_total, ≥500 Mbps inet_down,
// ≥12.4 cuda_max_good, ordered by dph_total ascending limit 20.
//
// `primaryHostID` excludes the primary's host when known (>0); pass 0 to
// disable the host_id filter when the primary host is unknown (D-A2).
func DefaultSearchFilter(maxDPH float64, primaryHostID int64) SearchFilter {
	f := SearchFilter{
		"gpu_name":      map[string]any{"eq": "RTX 4090"},
		"num_gpus":      map[string]any{"eq": 1},
		"reliability":   map[string]any{"gte": 0.99},
		"dph_total":     map[string]any{"lte": maxDPH},
		"inet_down":     map[string]any{"gte": 500},
		"cuda_max_good": map[string]any{"gte": 12.4},
		"rentable":      map[string]any{"eq": true},
		"verified":      map[string]any{"eq": true},
		"order":         []any{[]any{"dph_total", "asc"}},
		"limit":         20,
	}
	if primaryHostID > 0 {
		f["host_id"] = map[string]any{"neq": primaryHostID}
	}
	return f
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
