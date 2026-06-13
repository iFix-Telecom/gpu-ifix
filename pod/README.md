# GPU Pod — Operator Runbook

**Imagens publicadas:**
- `ghcr.io/ifixtelecom/ifix-ai-pod:{tag}` — pod runtime (~2 GB sem weights)
- `ghcr.io/ifixtelecom/ifix-ai-pod-health-bridge:{tag}` — health-bridge sidecar (~10 MB)

Tags: `develop`, `develop-{sha}`, `main`, `main-{sha}`, `latest-dev`, `latest` (estável), `v{X.Y.Z}` (release). Ver `.github/workflows/build-pod.yml`.

---

## Arquitetura em 1 minuto

O pod é composto de 3 serviços (Docker Compose, `pod/docker-compose.yml`):

| Serviço | Porta | Papel |
|---|---|---|
| `llama` | 8000 | Qwen 3.5 27B Q4_K_M via llama-server CUDA (OpenAI-compat) |
| `health-bridge` | 9100 | Probes reais em llama a cada 10s (D-11) |
| `dcgm-exporter` | 9400 | Métricas GPU para Prometheus (VRAM, util, temp, power) |

**STT: off-pod (handled by tier-1 OpenAI `whisper-1` via gateway fallback chain).**
Phase 11.1 (2026-06-04) removeu o serviço Speaches local — `/v1/audio/transcriptions`
agora resolve via tier-1 OpenAI direto (sem tier-0 local). Ver `.planning/seeds/SEED-010-shrink-pod-remove-whisper-stt.md`.

Decisões-chave: `.planning/phases/01-gpu-pod-image-smoke-test/01-CONTEXT.md` (D-01..D-27). Research: `.planning/research/` (STACK, PITFALLS, etc.).

---

## Setup inicial (one-time — operator)

Siga `.planning/MINIO-SETUP.md` — é um checklist numerado. Resumo:

1. Criar bucket MinIO `ifix-ai-weights` com access policy `private` + endpoint HTTPS público
2. Rodar `./pod/scripts/upload-weights.sh` (upload ~22 GB, 30-60 min)
3. Copiar os 3 SHA-256 impressos para GitHub Secrets
4. Configurar `VAST_AI_API_KEY` nos Secrets (Vast.ai Account > Keys > Export)
5. Testar smoke: `gh workflow run smoke.yml -f image_tag=develop`

Após o primeiro smoke verde: o bucket + secrets estão validados e qualquer futura mudança de imagem só precisa de novo `gh workflow run smoke.yml`.

---

## Executar localmente (dev)

Requer RTX 4090 acessível ao Docker via `--gpus all`. Sem isso, apenas os testes unitários rodam localmente.

```bash
cd /home/pedro/projetos/pedro/gpu-ifix

# 1) Weights locais (se não rodando contra MinIO)
mkdir -p /weights/qwen
# ... copie/simlink weights manualmente ou rode download-weights.sh com env MINIO_* apontando para o bucket
# (whisper removido em Phase 11.1; bge-m3 só necessário se rodar infinity localmente — fora do pod template)

# 2) Images — build local
docker build -f pod/Dockerfile -t ifix-ai-pod:dev .
docker build -f pod/health-bridge/Dockerfile -t ifix-ai-pod-health-bridge:dev .

# 3) Compose up
IFIX_AI_POD_IMAGE=ifix-ai-pod:dev \
IFIX_AI_POD_HEALTH_BRIDGE_IMAGE=ifix-ai-pod-health-bridge:dev \
  docker compose -f pod/docker-compose.yml up -d

# 4) Smoke (modo fast — 1 chat; STT smoke roda contra tier-1 OpenAI off-pod)
pip install -r pod/smoke/requirements.txt
python pod/smoke/smoke.py --target http://localhost --fast

# 5) Limpeza
docker compose -f pod/docker-compose.yml down -v
```

---

## Executar em Vast.ai (CI)

**Trigger manual:**

```bash
gh workflow run smoke.yml \
  -f image_tag=develop \
  -f max_price_per_hour=0.40 \
  -f smoke_timeout_minutes=45
```

O workflow:
1. Busca oferta 4090 < $0.40/h com verified=true, reliability≥98%, inet≥200 Mbps
2. Cria pod via Vast.ai REST API, injeta env (MinIO creds + weights keys)
3. onstart.sh baixa weights, valida SHA-256, sobe compose
4. Roda smoke.py — gates D-19 (vram_peak_gb≤21, tool_call_valid, no CUDA errors, p95_ttft≤3s)
5. Artifact `smoke-report.json` (retention 90 dias)
6. **Destrói pod sempre** (D-22 — `if: always()`)

Custo alvo: ≤$0.25/run (30 min @ $0.35/h).

---

## Promoção de imagem estável (D-23)

Critério: smoke.yml run verde contra a tag `develop-{sha}` ou `main-{sha}`.

```bash
# 1) Checkout o SHA que passou smoke
git checkout <sha>

# 2) Taggear estável
git tag v1.0.0
git push origin v1.0.0

# 3) Isso dispara build-pod.yml com tag push → publica ghcr.io/ifixtelecom/ifix-ai-pod:v1.0.0 + :latest
```

A partir daí o pod emergencial (Phase 6) usará `:v1.0.0` automaticamente.

---

## Arquivamento do baseline (D-20)

Após cada smoke verde (exit 0), copiar o `smoke-report.json` para arquivo versionado:

```bash
# Download do artifact mais recente
gh run download <run-id> -n smoke-report-develop-<run-id>
# Arquivar
mkdir -p .planning/phases/01-gpu-pod-image-smoke-test/baseline
cp smoke-report.json ".planning/phases/01-gpu-pod-image-smoke-test/baseline/smoke-report-$(git rev-parse --short HEAD).json"
git add .planning/phases/01-gpu-pod-image-smoke-test/baseline/
git commit -m "baseline(01): archive smoke-report for <sha>"
```

Phase 5 (Load Shedding) usa esse baseline para tunar thresholds reais de saturação.

---

## Troubleshooting

| Gate falhou | Primeira hipótese | Onde investigar |
|---|---|---|
| `vram_peak_gb > 21` (exit 2) | `--ctx-size 16384` ou `-np 2` alto demais para a carga real | Reduzir `--ctx-size` em pod/docker-compose.yml (D-09: fallback para 12288); re-rode smoke |
| `tool_call_valid == false` (exit 3) | Template Jinja quebrado (upstream llama.cpp mudou) | `pod/templates/qwen3.5-27b-tool-calling.jinja` — comparar com gist upstream; fork próprio se necessário |
| OOM/CUDA errors (exit 4) | llama --ctx-size / -np alto demais para a GPU | Reduzir `--ctx-size`; considerar llama `-np 1` |
| `p95_ttft > 3s` (exit 5) | host Vast.ai lento ou GPU thermal-throttled | Re-rode smoke; filtrar `inet_down≥500` em vast-ai.sh search |
| Pod stuck em `loading` >10 min | Image pull frio no host Vast.ai | PITFALLS §Pitfall 4 — filtrar hosts cacheados; upgrade vast-ai.sh para preferir offers com image cached |

---

## Layout do repo (Phase 1)

```
gpu-ifix/
├── go.mod                                  # plan 01
├── pkg/openai/                             # plan 01 — shared structs com Phase 2 gateway
├── pod/Dockerfile                          # plan 03
├── pod/docker-compose.yml                  # plan 03
├── pod/.env.example                        # plan 03
├── pod/onstart.sh                          # plan 05
├── pod/health-bridge/                      # plan 04 (Go service)
├── pod/smoke/                              # plan 06 (Python asyncio + JSON Schema)
├── pod/templates/                          # plan 02 (Jinja + SHA-256)
├── pod/scripts/download-weights.sh         # plan 05
├── pod/scripts/upload-weights.sh           # plan 09 (operator)
├── pod/scripts/vast-ai.sh                  # plan 08 (REST wrapper)
├── pod/weights/README.md                   # plan 09
├── pod/README.md                           # plan 09 (este arquivo)
├── .github/workflows/build-pod.yml         # plan 07
├── .github/workflows/smoke.yml             # plan 08
└── .planning/
    ├── MINIO-SETUP.md                      # plan 09 (operator checklist)
    └── phases/01-gpu-pod-image-smoke-test/
        ├── 01-CONTEXT.md
        ├── 01-PATTERNS.md
        ├── 01-01-PLAN.md ... 01-09-PLAN.md
        └── baseline/                       # preenchido a cada smoke verde
```
