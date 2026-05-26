---
phase: 10
plan: 01
captured: "<ISO date placeholder — populated by scripts/deploy/preflight.sh>"
host: n8n-ia-vm
expected_egress_ip: 162.55.92.154
---

# Phase 10-01 — Live capacity probe (n8n-ia-vm)

This file is the SCAFFOLD created by Plan 10-01 (autonomous). It is populated
on first run by `scripts/deploy/preflight.sh`, which OVERWRITES this content
with a timestamped capture of the 5 probe sections below.

The actual capture is INTENTIONALLY DEFERRED to Plan 10-06 HUMAN-UAT Gate B.
Autonomous executor does not hold SSH credentials at runtime and the probe
results are operator-recorded evidence. This scaffold exists so:

1. The preflight script has a known target path to write to.
2. Plan 10-01 verify step (`test -f`) passes against the static skeleton.
3. The 5 section anchors match the probe order the script will populate.

## 1) Connectivity

`ssh n8n-ia-vm 'echo ok'` round-trip check.

_Populated by preflight.sh §1._

## 2) Capacity snapshot

`free -h` + `df -h /` + `df -h /var/lib/docker` + `docker info | grep -iE swarm|cpus`
+ `curl -s --max-time 5 ifconfig.io`.

Gates:
- Disk `/` > 80% → ABORT (RESEARCH Pitfall 6 — run `docker image prune -af` first).
- Egress IP ≠ `162.55.92.154` → ABORT (RESEARCH How To #3 — DO Postgres Trusted
  Sources mismatch; the prod DSN would be rejected).

_Populated by preflight.sh §2._

## 3) Intra overlay attachability

`docker network inspect intra -f '{{.Attachable}}'` must print `true`
(RESEARCH Assumption A4). Closes RESEARCH Pitfall 1 — the prod compose declares
`networks.intra.external: true` and requires the existing Swarm overlay named
`intra` (NOT `worker_intra` from the canonical Portainer Swarm template).

_Populated by preflight.sh §3._

## 4) Internal-Traefik discovery probe

Open Question 2 / Assumption A2 — proves the Swarm-provider internal Traefik
discovers standalone-compose containers attached to the `intra` overlay.

The script spawns `preflight-hello` (nginxdemos/hello:plain-text) on `intra`
with synthetic Traefik labels, waits 3 s, and greps `docker service logs
traefik-internal_traefik` (last 60 s) for a router-added match. The ephemeral
container is removed unconditionally (trap on EXIT) regardless of probe result.

If FAIL → operator must switch traefik-internal to dual-provider
(`--providers.docker=true` alongside `--providers.docker.swarmMode=true`) per
RESEARCH Open Question 2 option B. Plan 10-02 cannot proceed until this gate
passes.

_Populated by preflight.sh §4._

## 5) GHA runners health

`gh run list --limit 1 --workflow build-gateway.yml` confirms a recent
workflow run exists OR explicitly flags "no recent runs" so the operator
can confirm the runner pool is healthy before the `v1.0.0` tag push
(Assumption A7).

_Populated by preflight.sh §5._

---

_This is the Wave 0 scaffold. The live capture happens during Plan 10-06
HUMAN-UAT Gate B and supersedes this content._
