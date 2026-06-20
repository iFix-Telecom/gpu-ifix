---
quick_id: 260620-o7d
description: Fix TTS health prober per-upstream — probe Piper at /tts, keep /v1/audio/speech for OpenAI-shaped tts upstreams
date: 2026-06-20
status: complete
commit: 639e4c7
---

# Quick Task 260620-o7d — Summary

## Problem
`gateway/internal/upstreams/probe.go` `dispatch` `case "tts":` hardcoded `POST <url>/v1/audio/speech` (OpenAI-shaped body) for every tts-role upstream. Tier-1 `voice-api-piper` only serves `POST /tts` with `{"text":...}` → probe 404 → row classified `config` instead of `ok`. Live request path (piperTTSAdapter in `proxy/tts.go`) was already correct; only the prober was wrong.

## Fix
Branch on `u.Name` (the only stable discriminator in scope — `UpstreamConfig` exposes `Name`/`Role`/`URL`, not `UrlEnv`):
- `voice-api-piper` → `POST <url>/tts` body `{"text":"ping"}`, JSON Content-Type, trailing-slash trimmed (mirrors `piperTTSAdapter` in `proxy/tts.go`).
- All other tts upstreams → unchanged OpenAI `/v1/audio/speech` path.

2xx from Piper `/tts` now classifies `ok`.

## Verification (all green on merged develop)
- `go build ./...`, `go vet ./internal/upstreams/...` clean
- `gofmt -l` empty on both files
- `go test ./internal/upstreams/... -count=1` passes — new `piper_posts_tts` + `piper_trailing_slash` subtests; existing `primary-tts` OpenAI-path subtests intact (no regression)
- Scope: only `probe.go` + `probe_test.go` touched (+94/-11)

## Files
- `gateway/internal/upstreams/probe.go`
- `gateway/internal/upstreams/probe_test.go`

## Out of scope (handled separately)
- Prod URL fix (`UPSTREAM_TTS_PIPER_URL` 172.18.0.1→10.10.10.30 in `/opt/ai-gateway-prod/.env`) was an ops change applied before this task — that fix resolved the live 503; this prober fix removes the misleading `config` status.

## Deploy note
Code on `develop`. Needs build + GHCR push + gateway redeploy on n8n-ia-vm for the prober change to take effect in prod (cosmetic — does not affect already-working fallback).
