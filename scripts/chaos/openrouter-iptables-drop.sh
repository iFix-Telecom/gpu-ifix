#!/usr/bin/env bash
# scripts/chaos/openrouter-iptables-drop.sh — PRD-03 chaos OpenRouter DROP.
#
# Phase 11 plan 11-08. Drops OpenRouter egress packets ONLY from the
# ai-gateway container netns (NOT host-wide). Breaker `openrouter-chat`
# opens by natural observation; sensitive tenants get HTTP 503
# upstream_unavailable_for_sensitive_tenant per RES-08; normal tenants
# fall through to tier-0 (Vast primary) when up.
#
# Modes:
#   apply     — install netns DROP, hold for CHAOS_DURATION, then cleanup
#   cleanup   — force-cleanup any residual phase11-chaos-openrouter rules
#   snapshot  — emit host OUTPUT sha256, no rule changes
#
# Env:
#   CHAOS_DURATION         seconds to hold the DROP (default 120)
#   CHAOS_RERESOLVE_LOOP   1 (default) = re-resolve openrouter.ai every 30s; 0 disables
#   CONTAINER_NAME         docker container name (default: ifix-ai-gateway)
#   HOST_SSH               SSH alias if running off-host (default: empty = local)
#
# Exit codes:
#   0  apply cycle ok (apply + hold + cleanup + sha256 match)
#   0  cleanup ok
#   1  arg/env validation error
#   2  container not running / netns unresolvable
#   3  host iptables sha256 MISMATCH at cleanup (CRITICAL)
#
# Reviews:
#   - HIGH #1: netns-scoped via nsenter; host OUTPUT sha256 pre/post equality
#   - MEDIUM #3: this script = Segment A natural observation; Segment B
#     smoke-sensitive-failover.py runs separately after full cleanup
#   - LOW #4: CF IP re-resolve every 30s; new IPs appended with same tag
#   - Pitfall 3: OpenRouter behind CF; broad CF CIDR + per-IP rules inside
#     container netns only; host egress untouched
#   - Pattern E: set -euo pipefail + ISO-8601 log + idempotent

set -euo pipefail
shopt -s nocasematch

log() {
  printf '[%s] %s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$*" >&2
}
die() {
  log "FATAL: $*"
  exit "${2:-1}"
}

: "${CHAOS_DURATION:=120}"
: "${CHAOS_RERESOLVE_LOOP:=1}"
: "${CONTAINER_NAME:=ifix-ai-gateway}"
: "${HOST_SSH:=}"
: "${COMMENT_TAG:=phase11-chaos-openrouter}"
: "${SNAPSHOT_PRE:=/tmp/host-output-pre.sha256}"
: "${SNAPSHOT_POST:=/tmp/host-output-post.sha256}"

ACTION="${1:-}"
[[ -n "$ACTION" ]] || die "missing action — apply | cleanup | snapshot" 1

run_remote() {
  if [[ -n "$HOST_SSH" ]]; then
    ssh "$HOST_SSH" "$@"
  else
    bash -c "$@"
  fi
}

get_container_pid() {
  local out
  out=$(run_remote "docker inspect -f '{{.State.Pid}}' $CONTAINER_NAME 2>/dev/null") || true
  out=$(printf '%s' "$out" | tr -d '[:space:]')
  if [[ -z "$out" || "$out" == "0" ]]; then
    die "container $CONTAINER_NAME not running (Pid empty/0)" 2
  fi
  printf '%s\n' "$out"
}

host_output_sha256() {
  # iptables-nft `-S OUTPUT -v -n` errors with "Illegal option -n"; drop -n for
  # broader compatibility. Rule output already lacks DNS resolution by default.
  run_remote "sudo iptables -S OUTPUT -v 2>/dev/null | sha256sum | awk '{print \$1}'"
}

resolve_openrouter() {
  # dig may be unavailable on minimal hosts (n8n-ia-vm Debian missing
  # dnsutils); fall back to `host` (bind9-host package, more universal).
  run_remote "(dig +short openrouter.ai 2>/dev/null || host openrouter.ai 2>/dev/null | awk '/has address/{print \$4}') | grep -E '^[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+\$' | sort -u"
}

netns_drop_ip() {
  local pid="$1" target="$2"
  run_remote "sudo nsenter --target $pid --net iptables -C OUTPUT -d $target -j DROP -m comment --comment '$COMMENT_TAG' 2>/dev/null || sudo nsenter --target $pid --net iptables -I OUTPUT 1 -d $target -j DROP -m comment --comment '$COMMENT_TAG'"
}

netns_list_residual() {
  local pid="$1"
  run_remote "sudo nsenter --target $pid --net iptables -S OUTPUT 2>/dev/null | grep -F '$COMMENT_TAG' || true"
}

netns_cleanup() {
  local pid="$1"
  run_remote "
    PID=$pid
    while true; do
      MATCH=\$(sudo nsenter --target \$PID --net iptables -S OUTPUT | grep -F '$COMMENT_TAG' | head -1) || true
      if [[ -z \"\$MATCH\" ]]; then break; fi
      RULE=\$(printf '%s' \"\$MATCH\" | sed 's/^-A OUTPUT//')
      sudo nsenter --target \$PID --net iptables -D OUTPUT \$RULE || break
    done
  "
}

if [[ "$ACTION" == "snapshot" ]]; then
  sha=$(host_output_sha256)
  log "host OUTPUT chain sha256 = $sha"
  printf '%s\n' "$sha"
  exit 0
fi

cleanup_phase() {
  local pid="$1"
  log "cleanup: removing all OUTPUT rules tagged $COMMENT_TAG from container netns"
  netns_cleanup "$pid" || true
  local residual
  residual=$(netns_list_residual "$pid")
  if [[ -n "$residual" ]]; then
    log "WARN: residual rules detected after cleanup:"
    printf '%s\n' "$residual" >&2
    return 1
  fi
  log "cleanup OK — no $COMMENT_TAG rules remain in container netns"
  return 0
}

verify_host_unchanged() {
  local pre_sha post_sha
  pre_sha=$(cat "$SNAPSHOT_PRE" 2>/dev/null || true)
  post_sha=$(host_output_sha256)
  printf '%s\n' "$post_sha" > "$SNAPSHOT_POST"
  if [[ -z "$pre_sha" ]]; then
    log "WARN: no pre-snapshot at $SNAPSHOT_PRE — skipping equality check"
    return 0
  fi
  if [[ "$pre_sha" == "$post_sha" ]]; then
    log "host OUTPUT sha256 EQUALITY VERIFIED — host iptables untouched"
    return 0
  fi
  log "CRITICAL: host OUTPUT sha256 MISMATCH — pre=$pre_sha post=$post_sha"
  return 3
}

if [[ "$ACTION" == "cleanup" ]]; then
  PID=$(get_container_pid)
  cleanup_phase "$PID" || die "cleanup failed; residual rules remain" 3
  verify_host_unchanged || die "host sha256 mismatch on cleanup" 3
  log "cleanup complete; exit 0"
  exit 0
fi

if [[ "$ACTION" != "apply" ]]; then
  die "unknown action $ACTION — use apply | cleanup | snapshot" 1
fi

log "apply mode: chaos_duration=${CHAOS_DURATION}s reresolve=${CHAOS_RERESOLVE_LOOP} container=$CONTAINER_NAME"

PID=$(get_container_pid)
log "container $CONTAINER_NAME netns PID=$PID"

PRE_SHA=$(host_output_sha256)
printf '%s\n' "$PRE_SHA" > "$SNAPSHOT_PRE"
log "host OUTPUT sha256 pre-apply = $PRE_SHA → $SNAPSHOT_PRE"

initial_ips=$(resolve_openrouter)
if [[ -z "$initial_ips" ]]; then
  die "could not resolve openrouter.ai via dig" 1
fi
log "initial openrouter.ai IPs:"
printf '%s\n' "$initial_ips" >&2

log "installing netns OUTPUT DROP rules"
trap 'log "trap fired — running cleanup"; cleanup_phase "$PID" || true; verify_host_unchanged || true; exit 4' EXIT INT TERM

while IFS= read -r ip; do
  [[ -z "$ip" ]] && continue
  netns_drop_ip "$PID" "$ip"
  log "  DROP installed for $ip"
done <<< "$initial_ips"

netns_drop_ip "$PID" "104.18.0.0/15"
netns_drop_ip "$PID" "172.64.0.0/13"
log "broad CF CIDR rules installed: 104.18.0.0/15 + 172.64.0.0/13"

log "CHAOS WINDOW START — holding ${CHAOS_DURATION}s"
chaos_start=$(date +%s)
chaos_end=$((chaos_start + CHAOS_DURATION))
known_ips="$initial_ips"

while [[ $(date +%s) -lt $chaos_end ]]; do
  if [[ "$CHAOS_RERESOLVE_LOOP" == "1" ]]; then
    sleep 30
    [[ $(date +%s) -ge $chaos_end ]] && break
    cur_ips=$(resolve_openrouter || true)
    new_ips=$(comm -23 <(printf '%s\n' "$cur_ips" | sort) <(printf '%s\n' "$known_ips" | sort) | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' || true)
    if [[ -n "$new_ips" ]]; then
      log "CF_ROTATION_DETECTED — new IPs:"
      printf '%s\n' "$new_ips" >&2
      while IFS= read -r ip; do
        [[ -z "$ip" ]] && continue
        netns_drop_ip "$PID" "$ip"
        log "  DROP appended for rotated IP $ip"
      done <<< "$new_ips"
      known_ips=$(printf '%s\n%s\n' "$known_ips" "$new_ips" | sort -u)
    fi
  else
    sleep 5
  fi
done
log "CHAOS WINDOW END after $(($(date +%s) - chaos_start))s"

trap - EXIT INT TERM
cleanup_phase "$PID" || die "cleanup failed; investigate residual rules" 3
verify_host_unchanged || die "host sha256 MISMATCH at cleanup" 3

log "chaos apply cycle complete"
log "next: 11-08-EVIDENCE.md Segment A; THEN Segment B smoke-sensitive-failover.py separately"
