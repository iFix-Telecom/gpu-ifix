#!/usr/bin/env bash
# scripts/deploy/cf-dns-create.sh — Phase 10 idempotent Cloudflare DNS A-record
# creator for the prod ifix-ai-gateway ingress hostnames.
#
# WARNING — ORDER MATTERS (RESEARCH §Pitfall 3):
#   1. Plan 10-06 HUMAN-UAT Step 1 — operator rsyncs the edge Traefik file-
#      provider entry (.planning/phases/10-prod-deploy-ai-gateway/artifacts/
#      ai-gateway-prod.yml) to vps-ifix-vm:/home/pedro/projetos/pedro/infra/
#      traefik-dynamic/ FIRST. Edge Traefik hot-reloads the route; logs cleanly
#      say `"no certificate for ai-gateway.converse-ai.app"`.
#   2. THEN — and only then — this script POSTs the Cloudflare A records. The
#      first :443 probe to either hostname triggers the TLS-ALPN-01 ACME
#      challenge → cert cached in `acme.json` on the edge.
# Flipping DNS before the route exists causes ACME to never start (the router
# is what triggers cert acquisition) — browsers then see the edge default
# self-signed cert and reject the connection.
#
# CF_API_TOKEN rotation pointer: https://dash.cloudflare.com/profile/api-tokens
# Current token literal lives in `~/.claude/CLAUDE.md` under "Cloudflare DNS
# API Token" (Zone:Read + DNS:Edit on the 3 Ifix zones). Rotate immediately if
# the token leaks (token covers converse-ai.app + converseai.app.br +
# ifixtelecom.com.br — blast radius = all Ifix DNS).
#
# Usage (Plan 10-06 HUMAN-UAT Step 6):
#   export CF_API_TOKEN=<REDACTED-CF-TOKEN>
#   scripts/deploy/cf-dns-create.sh
#
# Exit codes:
#   0 — both records exist and resolve to 162.55.92.154 within propagation budget
#   1 — CF_API_TOKEN unset
#   2 — CF API call failed (non-success, parsed via jq)
#   3 — DNS propagation budget exceeded (records POSTed but dig does not return
#       expected origin IP within 30s on 1.1.1.1)

set -euo pipefail

# --- constants (hardcoded — see threat T-10-03-03 in PLAN 10-03) -------------
# Zone id for converse-ai.app — NEVER make this an env var. Hardcoding prevents
# accidental record creation in the wrong zone (converseai.app.br or
# ifixtelecom.com.br are the other two zones covered by the same CF token).
ZONE_ID="0e779b74b86957bdb628d646dbf33978"   # converse-ai.app
ORIGIN_IP="162.55.92.154"                    # ifix-prod-01 Hetzner public IP
HOSTS=(ai-gateway ai-dashboard)              # → ai-gateway.converse-ai.app + ai-dashboard.converse-ai.app

# --- helpers -----------------------------------------------------------------
log() {
  printf '[%s] %s\n' "$(date -Iseconds)" "$*" >&2
}

# Fail-fast on missing token.
if [ -z "${CF_API_TOKEN:-}" ]; then
  cat >&2 <<'EOF'
FATAL: CF_API_TOKEN is unset.

Source the token from ~/.claude/CLAUDE.md (Cloudflare DNS API Token block) and re-run:

  export CF_API_TOKEN=<REDACTED-CF-TOKEN>
  scripts/deploy/cf-dns-create.sh

The same token covers 3 Ifix zones (converse-ai.app + converseai.app.br +
ifixtelecom.com.br). Rotation pointer:
  https://dash.cloudflare.com/profile/api-tokens
EOF
  exit 1
fi

# Dependency check — curl + jq + dig required. All present on ops-claude by default.
for bin in curl jq dig; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    log "FATAL: ${bin} not found on PATH"
    exit 1
  fi
done

# ensure_record(name) — idempotent A-record creator.
#   1. GET zones/{ZONE_ID}/dns_records?type=A&name={name}.converse-ai.app
#   2. If result|length == 0 → POST body shape (built via jq -nc below):
#        {"type":"A","name":"<fqdn>","content":"<ORIGIN_IP>","proxied":false,"ttl":300,"comment":"..."}
#   3. If result|length >= 1 → log skip (idempotent)
# "proxied":false is REQUIRED — RESEARCH §Anti-Patterns: TLS-ALPN-01 needs direct
# origin reachability + the gateway emits OpenAI-format error bodies that benefit
# from no proxy interference. Phase 11 may flip this on if WAF at the edge is
# desired.
ensure_record() {
  local name="$1"
  local fqdn="${name}.converse-ai.app"

  log "checking existing A record for ${fqdn}"
  local query_response
  if ! query_response=$(curl -sS -X GET \
    -H "Authorization: Bearer ${CF_API_TOKEN}" \
    -H "Content-Type: application/json" \
    "https://api.cloudflare.com/client/v4/zones/${ZONE_ID}/dns_records?type=A&name=${fqdn}"); then
    log "FATAL: GET dns_records failed for ${fqdn}"
    exit 2
  fi

  local query_success
  query_success=$(printf '%s' "$query_response" | jq -r '.success')
  if [ "$query_success" != "true" ]; then
    log "FATAL: CF API GET reported success=false for ${fqdn}"
    printf '%s\n' "$query_response" | jq -r '.errors' >&2
    exit 2
  fi

  local existing_count
  existing_count=$(printf '%s' "$query_response" | jq -r '.result | length')
  if [ "$existing_count" -gt 0 ]; then
    log "DNS record ${fqdn} already present — skipping (idempotent)"
    return 0
  fi

  log "POSTing new A record ${fqdn} → ${ORIGIN_IP} (proxied=false, ttl=300)"
  local create_body
  create_body=$(jq -nc \
    --arg name "$fqdn" \
    --arg content "$ORIGIN_IP" \
    --arg comment "Phase 10 prod ifix-ai-gateway (created $(date -Iseconds))" \
    '{type:"A", name:$name, content:$content, proxied:false, ttl:300, comment:$comment}')

  local create_response
  if ! create_response=$(curl -sS -X POST \
    -H "Authorization: Bearer ${CF_API_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "$create_body" \
    "https://api.cloudflare.com/client/v4/zones/${ZONE_ID}/dns_records"); then
    log "FATAL: POST dns_records failed for ${fqdn}"
    exit 2
  fi

  local create_success
  create_success=$(printf '%s' "$create_response" | jq -r '.success')
  if [ "$create_success" != "true" ]; then
    log "FATAL: CF API POST reported success=false for ${fqdn}"
    printf '%s\n' "$create_response" | jq -r '.errors' >&2
    exit 2
  fi

  # Sanity-check what we actually wrote — proxied MUST be false; if CF ever
  # silently coerces this we want to know immediately.
  local got_proxied
  got_proxied=$(printf '%s' "$create_response" | jq -r '.result.proxied')
  if [ "$got_proxied" != "false" ]; then
    log "FATAL: CF returned proxied=${got_proxied} (expected false) for ${fqdn} — would block ACME TLS-ALPN-01"
    exit 2
  fi

  local record_id
  record_id=$(printf '%s' "$create_response" | jq -r '.result.id')
  log "DNS record ${fqdn} created (id=${record_id})"
}

# verify_propagation(name) — dig +short against 1.1.1.1; loop up to 6×5s.
# CF nameservers propagate edits within a few seconds at TTL=300; the loop is a
# courtesy budget for the rare case where CF anycast hasn't converged yet.
verify_propagation() {
  local name="$1"
  local fqdn="${name}.converse-ai.app"
  local attempts=6
  local sleep_s=5
  local i resolved
  for ((i = 1; i <= attempts; i++)); do
    resolved=$(dig +short "$fqdn" @1.1.1.1 || true)
    if [ "$resolved" = "$ORIGIN_IP" ]; then
      log "PASS — ${fqdn} resolves to ${ORIGIN_IP} (attempt ${i}/${attempts})"
      return 0
    fi
    log "wait — ${fqdn} resolves to '${resolved:-<empty>}' (expected ${ORIGIN_IP}); retry in ${sleep_s}s (${i}/${attempts})"
    sleep "$sleep_s"
  done
  log "FATAL: ${fqdn} did not propagate to ${ORIGIN_IP} within $((attempts * sleep_s))s on 1.1.1.1"
  exit 3
}

# --- main --------------------------------------------------------------------
main() {
  log "Phase 10 — Cloudflare DNS A-record provisioning"
  log "zone_id=${ZONE_ID} origin_ip=${ORIGIN_IP} hosts='${HOSTS[*]}'"

  for host in "${HOSTS[@]}"; do
    ensure_record "$host"
  done

  log "verifying DNS propagation via 1.1.1.1 (TTL=300, ≤30s budget)"
  for host in "${HOSTS[@]}"; do
    verify_propagation "$host"
  done

  cat <<EOF

================================================================================
DNS records live.

NEXT (Plan 10-06 HUMAN-UAT Step 6 — POST-DNS):
  The edge Traefik route file MUST already be deployed (HUMAN-UAT Step 1)
  BEFORE the first https probe — the first request to either hostname triggers
  the ACME TLS-ALPN-01 challenge, and the router is what makes Traefik attempt
  cert acquisition. RESEARCH §Pitfall 3.

VERIFY first cert issuance after a few seconds:
  curl -vI https://ai-gateway.converse-ai.app
  ssh vps-ifix-vm 'docker exec infra-traefik-1 cat /letsencrypt/acme.json | jq ".letsencrypt.Certificates[].domain.main"'
================================================================================
EOF
}

main "$@"
