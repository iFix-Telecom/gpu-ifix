# GPU Pod — Operator Runbook

**Image:** `ghcr.io/ifixtelecom/ifix-ai-pod:{tag}`

> This is a stub. The full runbook is produced by Phase 1 plan 09 after
> smoke-test gates (D-19) are green. Until then, refer to
> `.planning/phases/01-gpu-pod-image-smoke-test/` for the in-progress
> specification.

## Layout

| Path | Purpose |
|---|---|
| `Dockerfile` | Multi-stage CUDA image (llama.cpp server-cuda + Speaches + Infinity + dcgm-exporter + health-bridge) |
| `docker-compose.yml` | 5-service pod composition (run as `docker compose up` after `onstart.sh`) |
| `onstart.sh` | Vast.ai onstart hook — downloads weights from MinIO (D-02) + launches compose |
| `health-bridge/` | Go service on port 9100 exposing per-model health probes (D-10..D-13) |
| `smoke/` | Python asyncio smoke-test script + report schema + fixtures |
| `templates/qwen3.5-27b-tool-calling.jinja` | Qwen 3.5 27B patched Jinja template (D-14..D-16) |
| `weights/` | (empty — weights live on MinIO per D-01; see `weights/README.md` for upload procedure) |

## Ports

| Port | Service | OpenAI-compat |
|---|---|---|
| 8000 | llama-server (LLM) | yes |
| 8001 | Speaches (STT) | yes |
| 8002 | Infinity (embed) | yes |
| 9100 | health-bridge | no (internal) |
| 9400 | dcgm-exporter | no (Prometheus metrics) |

## See also

- `docs/CONVENTIONS.md` — coding conventions
- `pod/weights/README.md` — MinIO weight upload procedure
- `.planning/phases/01-gpu-pod-image-smoke-test/` — phase spec + success criteria
