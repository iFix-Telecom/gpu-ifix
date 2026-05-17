# Spike: Vast Port Discovery (Pitfall 2 + Open Question 1+5)

**Date:** 2026-05-13
**Operator:** Pedro
**Cost:** ~$0.02 (two ephemeral instances, both <8 min wall, billed only for actual run time)
**Instances used:** 36716507 (broken — manifest 404), 36717044 (running)
**Outcome:** RESOLVED — strategy (a) JSON field-parse via `instances.ports["{container_port}/tcp"][0].HostPort`.

---

## Decision

**Strategy:** **(a) field-parse**.
SSH proxy fallback (b) is NOT implemented in Phase 6 because (a) succeeds
deterministically once `actual_status == "running"` (verified empirically
below) and Pitfall 6 (W6) already handles the pre-running window via
`inst.PublicIPAddr == ""` short-circuit in `checkHealth`.

**Field path (Go):**

```go
inst.Ports["9100/tcp"][0].HostPort  // string, must strconv.Atoi
inst.PublicIPAddr                    // string, e.g. "140.228.20.111"
```

**Health URL formula:**

```go
func (r *Reconciler) podHealthURL(inst vast.Instance) string {
    bindings, ok := inst.Ports["9100/tcp"]
    if !ok || len(bindings) == 0 || inst.PublicIPAddr == "" {
        return "" // caller checkHealth treats empty as not-yet-ready (Pitfall 6 W6 fix)
    }
    return "http://" + inst.PublicIPAddr + ":" + bindings[0].HostPort + "/health"
}
```

**Vast Instance struct addition** (`gateway/internal/emerg/vast/types.go`):

```go
type PortBinding struct {
    HostIP   string `json:"HostIp"`   // capital "Ip" — Docker convention
    HostPort string `json:"HostPort"` // STRING in API response (must strconv.Atoi)
}

type Instance struct {
    // ... existing fields
    Ports map[string][]PortBinding `json:"ports"` // nil until actual_status=="running"
}
```

**Rejected alternatives:**

- **(b) SSH proxy** — viable but adds `golang.org/x/crypto/ssh` dep (~3 MB) and the SSH host-key handling would conflict with our distroless container layout. Reserve for Phase 10 if Vast.ai changes the response shape.
- **(c) image self-registration** — would require a secondary Vast.ai write-token in `pod/onstart.sh`. Out of scope; the response field is canonical and stable across the 4 captures we made.

---

## Captured Response (running instance — `id=36717044`)

The full JSON is reproduced verbatim below (collected via `GET /instances/36717044/` after `actual_status` flipped to `running`):

```json
{
  "instances": {
    "actual_status": "running",
    "intended_status": "running",
    "id": 36717044,
    "machine_id": 33307,
    "host_id": 120840,
    "geolocation": "Texas, US",
    "public_ipaddr": "140.228.20.111",
    "ssh_host": "ssh7.vast.ai",
    "ssh_port": 37044,
    "image_uuid": "vastai/base-image:cuda-12.4.1-cudnn-devel-ubuntu22.04",
    "label": "phase6-spike-port-mapping-v2",
    "extra_env": [
      ["-p 9100:9100", "1"],
      ["-p 8000:8000", "1"]
    ],
    "ports": {
      "8000/tcp": [
        {"HostIp": "0.0.0.0", "HostPort": "37708"},
        {"HostIp": "::",      "HostPort": "37708"}
      ],
      "9100/tcp": [
        {"HostIp": "0.0.0.0", "HostPort": "40713"},
        {"HostIp": "::",      "HostPort": "40713"}
      ]
    },
    "direct_port_start": 37708,
    "direct_port_end": 40713,
    "direct_port_count": 6250,
    "dph_total": 0.3518518518518518,
    "rentable": true,
    "verified": "verified",
    "reliability2": 0.9904841,
    "status_msg": "success, running vastai/base-image_cuda-12.4.1-cudnn-devel-ubuntu22.04/ssh"
  }
}
```

### Sanity-check curl

```
$ curl -v http://140.228.20.111:40713/
* Connected to 140.228.20.111 (140.228.20.111) port 40713 (#0)
> GET / HTTP/1.1
< HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK
```

The mapped port served the netcat HTTP fixture installed via `onstart`,
confirming end-to-end TCP reachability from the gateway's network egress
through Vast's NAT to the container's listener.

---

## Pitfall 6 (W6) Confirmation

Before the container reached `actual_status: running`, the field shape
was reproducibly:

```
[19:50:17] actual=loading intended=running ports_len=0 direct_port=-1--1
[19:50:43] actual=loading intended=running ports_len=0 direct_port=-1--1
```

→ `instances.ports = {}` (or `null`) and `direct_port_start = -1`.
This is the W6 invariant: `inst.PublicIPAddr` may be set early
(`"140.228.20.111"` was visible at create time) BUT the port map is
absent until the container is actually running. Therefore `checkHealth`
must short-circuit to "not-ready, keep polling" if **either** the IP is
empty **or** `inst.Ports["9100/tcp"]` is empty — NOT treat it as a
transient HTTP error that would consume the cold-start budget.

---

## Pitfall 9 Confirmation (terminal states observed)

The first instance attempt (`36716507`) hit the
`Error response from daemon: manifest for vastai/test:cuda-12.4.1-cudnn9 not found`
status_msg, and Vast auto-flipped `intended_status: stopped` after
~7 minutes of stuck `actual=loading`. This is one of the failure modes
the W9 terminal-state guard catches — the polling loop must surface
`actual_status ∈ {exited, unknown, offline}` as a hard failure that
triggers `vast.DestroyInstance` + `closeLifecycle("instance_terminal_state")`
rather than silently exhausting the cold-start budget.

(Strictly speaking `36716507` never reached `exited` — Vast left it in
`loading` with `intended=stopped`. That's a known sub-class: when
`intended_status != "running"` for ≥30s, the lifecycle should also
treat it as terminal. **Plan 06-06 only enforces `actual_status` per
plan scope; intended-status mismatch detection is deferred to Plan
06-07** alongside cancel-in-flight.)

---

## API Endpoint Update — `console.vast.ai`

The legacy host `https://vast.ai/api/v0` now returns HTTP 308 redirects
to `https://console.vast.ai/api/v0`. The Phase 6 client MUST hardcode
the new base URL (or follow redirects, but `http.Client` does follow by
default for GET; the `PUT /asks/` create endpoint also redirects, which
Go follows). The CONTEXT.md `https://vast.ai/api/v0` reference is
historically accurate but no longer the canonical host as of 2026-05-13.

**Decision:** `vast.DefaultBaseURL = "https://console.vast.ai/api/v0"`.

---

## API Schema Update — Filter values must be dictionaries

The legacy filter syntax `{"gpu_name": "RTX 4090", "rentable": true}`
now returns:

```json
{
  "success": false,
  "error": "bad_request",
  "msg": "q.gpu_name: Input should be a valid dictionary; q.num_gpus: ..."
}
```

The current schema requires every value to be a dictionary with the
operator key:

```json
{
  "gpu_name": {"eq": "RTX 4090"},
  "num_gpus": {"eq": 1},
  "reliability": {"gte": 0.99},
  "dph_total": {"lte": 0.40},
  "rentable": {"eq": true},
  "verified": {"eq": true},
  "order": [["dph_total", "asc"]],
  "limit": 5
}
```

**Decision:** The Go `SearchFilter` struct uses `map[string]any` rather
than typed sub-structs to keep the wire format flexible — Vast.ai has
already changed it once during this project's lifetime, and a typed
struct would silently break on the next iteration.

---

## Cleanup Verification

```
$ curl -X DELETE https://console.vast.ai/api/v0/instances/36716507/
{"success": true}

$ curl -X DELETE https://console.vast.ai/api/v0/instances/36717044/
{"success": true}

$ curl https://console.vast.ai/api/v0/instances/36716507/ | jq '.instances'
null

$ curl https://console.vast.ai/api/v0/instances/36717044/ | jq '.instances'
null
```

Both spike instances destroyed. Final billing impact: ~$0.02 (≈ 6 minutes
of running time at $0.35/hr). Operator is in the clear for end-of-month.
