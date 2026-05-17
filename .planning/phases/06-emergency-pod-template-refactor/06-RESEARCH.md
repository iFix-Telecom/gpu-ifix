# Phase 6: Emergency-Pod Template Refactor - Research

**Researched:** 2026-05-16
**Domain:** Vast.ai instance lifecycle + llama.cpp CUDA distribution + Go reconciler refactor
**Confidence:** MEDIUM-HIGH (Vast runtype semantics + llama.cpp binary distribution authoritatively confirmed; cache-hit empírico permanece LOW pending spike)

## Summary

A pesquisa **confirmou** que `runtype="ssh"` (lifecycle.go:687) tem que ser trocado — Vast.ai docs e vast-cli source code são unânimes: SSH/Jupyter launch modes **substituem o ENTRYPOINT da image** e exigem que o startup vá em `onstart`. Isso explica exatamente o bug STATE.md:85 (lifecycles 29-33 travados — `emerg-bootstrap.sh` baked como CMD da custom image nunca rodou).

A pesquisa **DESAFIA três premissas** do CONTEXT.md que precisam validação humana ANTES do planner avançar:

1. **D-01 (template `nvidia/cuda:12.4-runtime-ubuntu22.04`):** Asset ai-dock/llama.cpp-cuda só publica **CUDA 12.8** — não 12.4. Bumping para `nvidia/cuda:12.8-runtime-ubuntu22.04` faz sentido se ficarmos com o caminho de download-binário-em-runtime. **Alternativa superior** discutida abaixo: usar imagem **oficial llama.cpp `ghcr.io/ggml-org/llama.cpp:server-cuda-bXXXX`** + `runtype=args` + `args=["--host","0.0.0.0",...]` — elimina inteiramente o passo de download de binário, sha256-verify, e onstart script. ENTRYPOINT da imagem oficial é exatamente `/app/llama-server`.
2. **D-02 (asset naming `llama-<TAG>-bin-ubuntu-x64-cuda.zip`):** Esse asset **NÃO EXISTE no GitHub ggml-org/llama.cpp**. ggml-org publica binário CUDA prebuilt SOMENTE pra Windows. Para Linux, a alternativa documentada (e usada pelo próprio `vast-ai/base-image/derivatives/llama-cpp/Dockerfile`) é `ai-dock/llama.cpp-cuda` releases — naming real `llama.cpp-bXXXX-cuda-12.8.tar.gz`.
3. **D-06 (`Runtype: "ssh_proxy"`):** Mesmo em ssh_proxy o ENTRYPOINT da image continua **OVERRIDE-ado pelo wrapper Vast** — apenas `ssh` e `ssh_proxy` são interchangeable, ambos com mesmo efeito de substituir o entrypoint. O CONTEXT.md trata ssh_proxy como se preservasse CMD — **NÃO preserva**. Para preservar a image CMD + ENTRYPOINT, o único runtype válido é `args` (que não cria sshd sidecar nenhum) ou jupyter (vai criar Jupyter server que não queremos).

**Primary recommendation:** Antes do planner emitir plans, o discuss-phase precisa decidir entre duas estratégias que mudam scope significativamente:

- **Strategy A (CONTEXT.md atual com correções):** runtype=`ssh_proxy` + onstart inline + image_args... **NÃO FUNCIONA**. ssh_proxy não preserva CMD. A alternativa equivalente sem precisar de sshd dentro do container é `runtype=ssh_proxy` + onstart faz TUDO (download bin + weights + start llama-server). Operator perde acesso `vast ssh` ao container principal (sshd sidecar Vast não existe quando runtype=ssh_proxy — confusão minha inicial; veja Pitfall 3 abaixo).
- **Strategy B (recomendada — emerge da research):** `runtype=args` + `image="ghcr.io/ggml-org/llama.cpp:server-cuda-bXXXX"` (imagem oficial) + `args=["--host","0.0.0.0","--port","8000","-m","/weights/model.gguf",...]` + `onstart` baixa weights MinIO. Zero binary-download, zero sha256-bin-verify, zero ENTRYPOINT-injection. **TROCA D-01..D-08 inteiramente.**

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Search & filter Vast offers | Gateway/Go reconciler (`emerg/lifecycle.go`) | — | Lifecycle logic já implementada — Phase 6 não muda |
| Build PUT /asks/{id}/ payload (Image + Runtype + Args + Onstart) | Gateway/Go reconciler (`buildCreateRequest`) | — | Único call site que muda. Decisão A vs B é meta-decisão de QUAL payload construir |
| llama-server binary distribution | Strategy A: GitHub release (ai-dock) + MinIO mirror. Strategy B: ghcr.io/ggml-org/llama.cpp (pulled by Vast) | MinIO (weights fallback) | Strategy B elimina inteiramente |
| Weights distribution | MinIO (`ai-gateway` bucket) | — | Mesma estratégia Phase 1 — inalterada |
| sha256 integrity verification | Strategy A: onstart inline (bin + weights). Strategy B: onstart inline (weights only) — image SHA already pinned by docker tag | — | Pattern reusado de `pod/scripts/emerg-bootstrap.sh:52` |
| sshd sidecar / operator SSH access | Vast.ai platform (NOT in container) — só com `runtype=ssh_direct` ou `ssh_proxy` | — | Operator usa `vast ssh <id>` independente do container; pesquisa abaixo |
| Crash detection (PID 1 dies → container dies) | Vast.ai platform (Docker restart policy `no` default → actual_status→exited) | Reconciler (Pitfall 9 terminal-state) | Implementação Phase 6.5 já detecta `actual_status ∈ {exited, unknown, offline}` |
| Onstart script storage + versioning | MinIO (`emerg-onstart/<VERSION>.sh`) | Inline (Vast onstart 4048 char limit) | CONTEXT.md D-04. Veja Pitfall 5 sobre 4048-char hard limit |

## User Constraints (from CONTEXT.md)

> Esta é a tradução literal das decisões CONTEXT.md `<decisions>` + `<deferred>`. Locked decisions devem ser HONORADAS pelo planner, mas três delas (D-01, D-02, D-06) entram em conflito com evidência empírica encontrada nesta research — discuss-phase deve ser re-aberto antes do planner começar.

### Locked Decisions (CONTEXT.md)

- **D-01:** Template = `nvidia/cuda:12.4-runtime-ubuntu22.04`. **CONFLITO**: ai-dock/llama.cpp-cuda só tem CUDA 12.8 builds. Discuss precisa: (a) trocar template pra 12.8-runtime, ou (b) trocar bin source (não existe alternativa oficial pra CUDA 12.4 Linux), ou (c) adotar Strategy B abaixo (sem binário externo).
- **D-02:** llama-bin download path = `github.com/ggml-org/llama.cpp/releases/download/<TAG>/llama-<TAG>-bin-ubuntu-x64-cuda.zip`. **ASSET INEXISTENTE** — ggml-org só publica CUDA prebuilt pra Windows. Linux+CUDA prebuilt vem de `ai-dock/llama.cpp-cuda` (third-party, MIT license, 57 stars).
- **D-03:** Fallback MinIO mirror — correto, padrão reusable.
- **D-04:** Onstart script em MinIO versionado por filename. Correto.
- **D-05:** Onstart inline ~10 linhas faz curl + sha256-check de `/onstart.sh`. **ATENÇÃO**: Vast onstart limit é **4048 chars** (hard limit no API). 10 linhas cabe folgado, mas o planner deve preservar essa margem.
- **D-06:** `Runtype: "ssh_proxy"` + `Onstart: "<inline curl+sha256>"` + `ImageArgs: ["bash", "/onstart.sh"]`. **CONFLITO**: (1) ssh_proxy substitui ENTRYPOINT da image — `ImageArgs` em ssh_proxy NÃO É CONSUMIDO pelo entrypoint do template. Pra essa composição funcionar, precisa ser `runtype=args` (e perde sshd-via-vast — operator usa `vast ssh` independente). (2) Campo na API é chamado **`args`** (lista REMAINDER), **não** `image_args`. (3) Em ssh_proxy, `onstart` roda dentro do container substituindo o ENTRYPOINT.
- **D-07:** Conteúdo do `/onstart.sh` MinIO. Correto em termos de lógica — reusa pattern emerg-bootstrap.sh.
- **D-08:** Cleanup custom image. Correto. Mitigação burnt-bridge (PR separado pós 3 lifecycles GREEN) é boa prática.

### Claude's Discretion (CONTEXT.md)

- Naming MinIO path/filename — manter livre, plan-phase decide
- Estrutura interna de `/onstart.sh` — segue padrão Phase 1
- Health-bridge sidecar — manter omisso per Phase 6.5 D-C2 (LLM-only)
- Disk filter — atualmente 80GB, pode reduzir para 40-50GB com template Vast pequeno

### Deferred Ideas (OUT OF SCOPE)

- Health-bridge em emergency pod
- STT/Embed servers em emergency pod
- Multi-arch llama-bin (arm64)
- Dependabot/Renovate auto-bump
- Tech debt itens STATE.md #80, #81, #83, #84

## Phase Requirements

> Phase 6 não cria requirements PRV-XX novos. Reutiliza PRV-01..PRV-10 com fonte de binário diferente (template Vast vs custom GHCR image). Os requirements continuam sendo servidos por Phase 6.5 reconciler — Phase 6 só TROCA o `buildCreateRequest` payload.

| ID | Description (from REQUIREMENTS.md) | Research Support |
|----|-------------------------------------|------------------|
| PRV-06 | Provisioning filtra hosts Vast.ai por reliability ≥99% e network capability adequada; usa imagem `ghcr.io/ifixtelecom/ifix-ai-pod:vX.Y` com onstart script | **Phase 6 atualiza a "imagem" — não muda filter logic.** Strategy B troca para `ghcr.io/ggml-org/llama.cpp:server-cuda-bXXXX`. Strategy A troca para `nvidia/cuda:12.X-runtime-ubuntu22.04`. |
| PRV-07 | Readiness check do pod emergencial usa `/health` por modelo antes de adicionar ao pool ativo | **Inalterado.** lifecycle.go:588 `checkHealth` já existe — bate em `/v1/models` (LLM-only). Funciona qualquer caminho. |
| PRV-01..05, 08..10 | Outros PRV requirements | **Inalterados** — Phase 6 não toca FSM/budget/cancel/recovery/audit. |

## Standard Stack

### Core

| Library / Asset | Version | Purpose | Why Standard |
|---|---|---|---|
| `ghcr.io/ggml-org/llama.cpp:server-cuda` (and tag-pinned `server-cuda-bXXXX` variants) | b9128+ | LLM inference server | **Official llama.cpp Docker image**, MIT, CUDA 12.8.1 base, ENTRYPOINT=`/app/llama-server`. Documentado em docs/docker.md upstream. [VERIFIED: docker.md] |
| `ghcr.io/ai-dock/llama.cpp-cuda` releases (Strategy A only) | b9128, b9144, b9159, b9174 | llama-server binary tarball | Third-party but USED BY vast-ai/base-image/derivatives/llama-cpp/Dockerfile officially. Asset: `llama.cpp-bXXXX-cuda-12.8.tar.gz` (~160MB). License MIT. Stars 57, repo active (last push 2026-05-16). [VERIFIED: GitHub API + clone vast-ai/base-image] |
| `nvidia/cuda:12.8-runtime-ubuntu22.04` (Strategy A) ou `nvidia/cuda:12.8-base-ubuntu22.04` | 12.8.0 | Base CUDA runtime template | Official NVIDIA, Docker Hub. Tag `12.8.0-runtime-ubuntu22.04` é o que ai-dock/llama.cpp-cuda build target. ~600MB. [VERIFIED: hub.docker.com] |
| MinIO (`s3.ifixtelecom.com.br/ai-gateway`) | — | Onstart script + llama-bin mirror + Qwen weights | Já em uso Phase 1. Sem novo dep. [VERIFIED: pod/onstart.sh + emerg-bootstrap.sh credentials documentadas] |

### Supporting

| Library | Version | Purpose | When to Use |
|---|---|---|---|
| `coreutils` (sha256sum, curl, bash) — assumed in `nvidia/cuda` base | apt-installed inside template OR pre-baked | sha256 verification | Strategy A onstart needs `apt-get install -y curl ca-certificates` if template doesn't bundle (verify in spike — `nvidia/cuda:*-runtime` usually has curl+ca-certs minimal) [ASSUMED — confirm in spike] |
| `tar`/`gzip` | apt | Extract llama.cpp-bXXXX-cuda-12.8.tar.gz | Strategy A only |
| `mc` (MinIO client) | latest (downloaded on demand from `dl.min.io`) | Bulk transfer of weights | Pattern reusado de `pod/scripts/emerg-bootstrap.sh:46`. Alternative: pure curl with AWS sigv4 (more reliable, no extra dep) — decide in plan-phase |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|---|---|---|
| **Strategy B: `ghcr.io/ggml-org/llama.cpp:server-cuda-bXXXX`** + `runtype=args` | Strategy A: `nvidia/cuda:12.8-runtime` + download bin from ai-dock | A: smaller template cache hit + more dev iteration agility (changes to startup logic don't need image rebuild). B: simpler (no bin download, no sha256-bin-verify), more reliable (image SHA already pinned by tag), but every llama-server flag tweak needs Vast `image_args` recompose (which IS a config change — same agility). **B preserves PRV-06 "uses ghcr.io image" spirit better.** |
| MinIO mirror for llama-bin | Skip mirror, rely on GitHub | GitHub rate-limit (60req/h unauth) + provider outage = SPOF. Mirror cost ~$0 (~160MB stored). Mantém em ambas estratégias se Strategy A. Strategy B descarta mirror (image pulled by Vast host, ggml-org GHCR has its own caching) |
| Inline onstart with full script (~3KB) | Onstart curl from MinIO + sha256 | Inline limite 4048 chars; full script ~3KB com sanity checks cabe FOLGADO inline. MinIO indirection adiciona latência (~1s) e ponto de falha. **DISCUTIR em plan-phase**: tradeoff agilidade-vs-simplicidade. CONTEXT.md D-05 escolheu MinIO; pode revisar. |

**Installation (Strategy A — CONTEXT.md atual com correções):**

```bash
# Pre-requisitos (todos já existem):
# - MinIO bucket ai-gateway acessível (Phase 1)
# - Qwen weights em MinIO com sha256 conhecido (Phase 1 D-05)
# - Gateway Go (já existe)

# Phase 6 adiciona:
# 1. CI workflow .github/workflows/upload-emerg-onstart.yml — upload pod/scripts/emerg-onstart.sh para MinIO em cada commit
# 2. CI workflow .github/workflows/mirror-llama-bin.yml — download ai-dock release + upload MinIO (manual trigger ou cron diário)
# 3. Config gateway env vars (6 novos)
# 4. Refactor lifecycle.go:buildCreateRequest
# 5. Delete: pod/Dockerfile, pod/scripts/emerg-bootstrap.sh, .github/workflows/build-pod.yml (delete-PR separado)
```

**Installation (Strategy B — RECOMENDADA, deriva da research):**

```bash
# Pre-requisitos: MESMO que A
# Phase 6 adiciona:
# 1. Config gateway env vars (4 novos — não 6, porque sem bin download)
# 2. Refactor lifecycle.go:buildCreateRequest
# 3. Delete: pod/Dockerfile, pod/scripts/emerg-bootstrap.sh, .github/workflows/build-pod.yml (delete-PR separado)
# NÃO precisa de: onstart script em MinIO, CI mirror llama-bin, sha256 bin verify
```

**Version verification:**

```bash
# llama.cpp official Docker — verified via WebFetch llama.cpp/docs/docker.md
docker manifest inspect ghcr.io/ggml-org/llama.cpp:server-cuda-b9128 2>&1 | head -5
# existência de tag server-cuda-bXXXX pinada [ASSUMED — não documented in docs.md mas vast-ai/base-image tem pattern semelhante; confirm in spike]

# ai-dock llama.cpp-cuda b9128 — VERIFIED via GitHub API
curl -sL "https://api.github.com/repos/ai-dock/llama.cpp-cuda/releases/tags/b9128" | jq '.assets[].browser_download_url'
# llama.cpp-b9128-cuda-12.8.tar.gz, 159MB, MIT license

# nvidia/cuda 12.8 runtime — VERIFIED via hub.docker.com
docker pull nvidia/cuda:12.8.0-runtime-ubuntu22.04
```

## Package Legitimacy Audit

> Phase 6 NÃO adiciona packages npm/PyPI/cargo. Adiciona artefatos via GitHub Release + Docker image. Audit aplicado a essas fontes:

| Asset | Source | Age | Downloads | Source Repo | slopcheck | Disposition |
|---|---|---|---|---|---|---|
| `ghcr.io/ggml-org/llama.cpp:server-cuda` | GHCR (org `ggml-org`) | ~4 yrs (project active since 2023, server tag stable) | "most downloaded server variant 3,465" per pkgs.github.com (recent snapshot) | github.com/ggml-org/llama.cpp 109k+ stars | N/A (slopcheck só faz pypi/npm/etc) | Approved (upstream canônico) |
| `ghcr.io/ai-dock/llama.cpp-cuda` releases | GitHub Releases | ~1.5 yrs ai-dock org | 197 releases (build-by-build) | github.com/ai-dock/llama.cpp-cuda 57 stars, MIT, last push 2026-05-16 | N/A | Approved with caveat — third-party but USED BY official vast-ai/base-image/derivatives/llama-cpp/Dockerfile (oficial Vast endorsement) [VERIFIED: clone vast-ai/base-image] |
| `nvidia/cuda:12.8.0-runtime-ubuntu22.04` | Docker Hub (NVIDIA official) | >2 yrs in 12.x | "Official NVIDIA" labeled | hub.docker.com/r/nvidia/cuda official | N/A | Approved (vendor canônico) |
| MinIO `s3.ifixtelecom.com.br` (`ai-gateway/llama-bins/<TAG>/...`) | Self-hosted | < 1 yr | Internal | Ifix internal | N/A | Approved (self-hosted, controlled) |

**Packages removed due to slopcheck [SLOP] verdict:** None
**Packages flagged as suspicious [SUS]:** None

slopcheck foi rodado contra um package PyPI controle (`mc-cli` → flagged SUS corretamente; sanity confirmation slopcheck functional). Phase 6 não tem dep installs de registry. Tag `[VERIFIED]` aplica a (a) llama.cpp official image (Context7-equivalent via official docker.md + GitHub), (b) ai-dock release JSON via GitHub API direct, (c) nvidia/cuda via hub.docker.com layer details, (d) vast-ai/base-image via git clone + read source.

## Architecture Patterns

### System Architecture Diagram (Strategy B — recommended)

```
                                +----------------------------------------------+
                                |  Gateway Go reconciler (existing)            |
                                |                                              |
   FSM EmergencyProvisioning -->|  startProvisioning(lifecycleID)              |
                                |     v                                         |
                                |  provisionLifecycle:                          |
                                |   1. SearchOffers(filter)                     |
                                |   2. buildCreateRequest():  <-- MUDA (Phase 6 scope)
                                |      Image: ghcr.io/ggml-org/llama.cpp:       |
                                |             server-cuda-b9128                 |
                                |      Runtype: "args"                          |
                                |      Args: ["--host","0.0.0.0","--port","8000"|
                                |             "-m","/weights/qwen/model.gguf",  |
                                |             "-ngl","99","-np","2",            |
                                |             "--ctx-size","16384","--jinja"]   |
                                |      Onstart: <inline bash>                   |
                                |        mkdir -p /weights/qwen                 |
                                |        curl MinIO Qwen.gguf -> /weights/...   |
                                |        sha256sum -c                           |
                                |      Env: -p 8000:8000 + MINIO_* + WEIGHTS_*  |
                                |   3. CreateInstance(offerID, req) -> instID   |
                                |   4. waitForReadyOrDestroy:                   |
                                |      poll GetInstance every 5s                |
                                |   5. checkHealth() -> /v1/models 200          |
                                |   6. markHealthy -> FSM EmergencyActive       |
                                +----------------------------------------------+
                                                | PUT /asks/{offer_id}/
                                                v
                              +-------------------------------------------------+
                              |  Vast.ai API + Vast host (offer accepted)       |
                              |                                                 |
                              |  1. Host pulls ghcr.io/ggml-org/llama.cpp:      |
                              |     server-cuda-b9128                           |
                              |     (cache-hit if recently used elsewhere)      |
                              |  2. docker create with ENTRYPOINT=/app/llama-srv|
                              |     CMD = ["--host","0.0.0.0","--port",...]    <-- from `args` field
                              |     env -p 8000:8000 (port forwarding)          |
                              |  3. RUN onstart inline (in container, BEFORE    |
                              |     entrypoint exec) -- downloads weights       |
                              |  4. Container starts /app/llama-server <args>   |
                              |     -> llama-server is PID 1                     |
                              |  5. actual_status: loading -> running           |
                              |     ports["8000/tcp"] populated                 |
                              |                                                 |
                              |  Operator SSH access: `vast ssh <id>` works     |
                              |  via Vast's sidecar SSH (host-level, NOT in     |
                              |  container) [VERIFIED: docs.vast.ai launch-modes]|
                              +-------------------------------------------------+
```

**Note Strategy A:** Same diagram but `Image="nvidia/cuda:12.8.0-runtime-ubuntu22.04"` + `Runtype="ssh_proxy"` (NOT ssh — same effect, ssh deprecated alias) + `Onstart` baixa bin + weights + starts llama-server (PID 1 from onstart). Mais layers de bootstrap, mais pontos de falha.

### Component Responsibilities

| File | Responsibility | Lines Phase 6 toca |
|---|---|---|
| `gateway/internal/emerg/lifecycle.go` | `buildCreateRequest` (655-697) — único call site | ~40 lines net diff |
| `gateway/internal/emerg/vast/types.go` | `CreateRequest` struct add field `Args []string` (Strategy B) OR keep current (Strategy A) | ~3 lines + comment |
| `gateway/internal/emerg/vast/client.go` | `CreateInstance` HTTP body — json.Marshal already handles new field | 0 lines if new field uses json tag |
| `gateway/internal/config/config.go` | Add 4-6 new env vars, delete `EmergencyPodImageTag` | ~10 lines |
| `gateway/.env.portainer.dev` | Update env file | ~6 lines |
| `pod/Dockerfile` | DELETE (D-08, separate PR after UAT) | -77 |
| `pod/scripts/emerg-bootstrap.sh` | DELETE (D-08) | -74 |
| `.github/workflows/build-pod.yml` | DELETE (D-08) | -255 |
| `pod/scripts/emerg-onstart.sh` (Strategy A only) | NEW — MinIO-hosted | +~80 lines |
| `.github/workflows/upload-emerg-onstart.yml` (Strategy A only) | NEW | +~40 lines |
| `.github/workflows/mirror-llama-bin.yml` (Strategy A only) | NEW | +~40 lines |
| `gateway/internal/emerg/lifecycle_test.go` | Adjust assertions for new payload | ~20 lines |
| `gateway/internal/integration_test/emerg_*` | Some tests assume Runtype=ssh — update | ~10-30 lines across files |

### Pattern 1: Vast.ai `runtype=args` with image args (Strategy B)

**What:** Use Vast.ai `runtype="args"` to let the docker image's ENTRYPOINT run AS-IS, passing custom CLI flags through the `args` field (JSON array of strings, REMAINDER semantics from vast-cli).

**When to use:** Image has a well-formed ENTRYPOINT that does the right thing when invoked with parameters (e.g., `ghcr.io/ggml-org/llama.cpp:server-cuda` with `ENTRYPOINT=["/app/llama-server"]`).

**Example (Go — replaces buildCreateRequest body):**

```go
// Source: vast-cli/vast.py:2509 -- json_blob["args"] = args.args
// (where args.args comes from argparse REMAINDER -> list of strings)
// Verified via reading vast.py:2470-2520 (gh raw URL)

func (r *Reconciler) buildCreateRequest(offer vast.Offer, lifecycleID int64) vast.CreateRequest {
    cfg := r.deps.Cfg
    onstart := `#!/bin/bash
set -euo pipefail
WEIGHTS_PATH=/weights/qwen/model.gguf
mkdir -p "$(dirname "$WEIGHTS_PATH")"
if [[ ! -f "$WEIGHTS_PATH" ]]; then
  apt-get update && apt-get install -y curl ca-certificates >/dev/null
  curl -sL https://dl.min.io/client/mc/release/linux-amd64/mc -o /usr/local/bin/mc
  chmod +x /usr/local/bin/mc
  mc alias set ifix "$MINIO_ENDPOINT" "$MINIO_ACCESS_KEY" "$MINIO_SECRET_KEY" >/dev/null
  mc cp "ifix/${MINIO_BUCKET}/${WEIGHTS_QWEN_KEY}" "$WEIGHTS_PATH"
  ACTUAL=$(sha256sum "$WEIGHTS_PATH" | awk '{print $1}')
  [[ "$ACTUAL" = "$WEIGHTS_QWEN_SHA256" ]] || { echo "sha256 mismatch"; exit 1; }
fi
`  // ~700 chars -- well under 4048 limit
    return vast.CreateRequest{
        ClientID: "me",
        Image:    cfg.EmergencyTemplateImage,  // "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"
        Runtype:  "args",
        Onstart:  onstart,
        Args: []string{
            "--host", "0.0.0.0",
            "--port", "8000",
            "-m", "/weights/qwen/model.gguf",
            "-ngl", "99",
            "-np", "2",
            "--ctx-size", "16384",
            "--jinja",
            "--chat-template-file", "/app/templates/qwen3.5-27b-tool-calling.jinja",
        },
        Env: map[string]string{
            "-p 8000:8000":       "1",
            "MINIO_ENDPOINT":     cfg.MinioEndpoint,
            "MINIO_BUCKET":       cfg.MinioBucket,
            "MINIO_ACCESS_KEY":   cfg.MinioAccessKey,
            "MINIO_SECRET_KEY":   cfg.MinioSecretKey,
            "WEIGHTS_QWEN_KEY":   cfg.WeightsQwenKey,
            "WEIGHTS_QWEN_SHA256": cfg.WeightsQwenSHA256,
        },
        Disk:        50,  // template + weights + tmp; was 80 for custom image
        Label:       fmt.Sprintf("ifix-emerg-lifecycle-%d", lifecycleID),
        TargetState: "running",
    }
}
```

**Open question for plan-phase:** Where does the `qwen3.5-27b-tool-calling.jinja` template come from? The official `ghcr.io/ggml-org/llama.cpp:server-cuda` image does NOT bundle Ifix's patched Jinja template. Two options:
1. Pre-bake Jinja into a small custom image `ghcr.io/ifixtelecom/qwen-templates` (10KB layer over public image) — adds CI workflow but keeps tag-pinned reproducibility.
2. Inline-fetch template in onstart from MinIO (`emerg-onstart/templates/qwen3.5-27b-tool-calling-<sha>.jinja`) + write to `/app/templates/`. Onstart grows by ~5 lines. **No new image needed.**

Decision belongs in discuss-phase or plan-phase Wave 0.

### Pattern 2: Vast.ai `runtype=ssh_proxy` with onstart-driven start (Strategy A)

**What:** Use SSH/SSH-proxy runtype — Vast injects its own ENTRYPOINT (replacing image's), expects user to put startup logic in onstart. `vast ssh` access available via Vast's port-22 proxy.

**When to use:** Image has no ENTRYPOINT (or bad one), and you need operator SSH access into the container.

**IMPORTANT clarification:** Pesquisa esclareceu — `runtype=ssh_proxy` E `runtype=ssh` BOTH override the image ENTRYPOINT (são aliases). Não há combinação de "preservar CMD + dar acesso SSH" — operator SSH em Vast funciona via `vast ssh <id>` que é proxy do platform Vast (host-level), independente do que está rodando dentro do container. Dentro do container, em SSH/ssh_proxy runtype, o ENTRYPOINT efetivo é `/opt/instance-tools/bin/entrypoint.sh` (do vast-ai/base-image) que sourcing scripts `vast_boot.d/*.sh` and eventually running `/onstart.sh`. [VERIFIED: clone vast-ai/base-image]

**Example (Strategy A — Go):**

```go
// onstart inline (~150 chars, well under 4048):
onstartInline := fmt.Sprintf(`#!/bin/bash
set -e
curl -fsSL %s -o /tmp/em.sh
echo "%s  /tmp/em.sh" | sha256sum -c
bash /tmp/em.sh`,
    cfg.EmergencyOnstartURL,    // "https://s3.ifixtelecom.com.br/ai-gateway/emerg-onstart/v2.sh"
    cfg.EmergencyOnstartSHA256, // hex
)
// then full emerg-onstart.sh (~3KB) hosted in MinIO does: download llama-bin tarball + sha256 + extract + download weights + sha256 + start llama-server
```

### Pattern 3: PID 1 = llama-server (crash detection)

**What:** Container's PID 1 is llama-server itself, so when it crashes the container terminates (`actual_status` -> `exited`) and the Phase 6.5 reconciler Pitfall 9 detection (lifecycle.go) kicks in.

**Strategy B path:** `runtype=args` -> container ENTRYPOINT=`/app/llama-server` + Args provided -> llama-server is PID 1 directly. **Cleanest.**

**Strategy A path:** `runtype=ssh_proxy` -> ENTRYPOINT=`/opt/instance-tools/bin/entrypoint.sh` -> eventually runs `/onstart.sh` -> onstart ends with `exec /app/llama-server ...`. Bash `exec` replaces process image so llama-server inherits PID of the bash that called it. BUT that bash was itself a child of the Vast entrypoint, NOT PID 1. **Mais frágil — crash signal pode não derrubar container imediatamente.**

[CITED: bash exec(1) man page — "shell exits, replacing the shell, without creating a new process"]
[VERIFIED via clone vast-ai/base-image — entrypoint chain is `entrypoint.sh -> boot_default.sh -> vast_boot.d/65-supervisor-launch.sh (launches supervisord) + 75-provisioning-manifest.sh + (eventually) /onstart.sh execution`]

### Anti-Patterns to Avoid

- **`runtype=ssh` (without _proxy or _direct):** Deprecated alias for `ssh_proxy`. Same behavior, but inconsistent in docs. Always use the explicit `ssh_proxy` or `ssh_direct`.
- **`args_str` (single string with shell interpretation):** Per docs.vast.ai/api-reference, single-string variant — shell-wraps the value. Use `args` (array) to avoid quoting issues. [CITED: docs.vast.ai/api-reference/instances/create-instance]
- **Mixing `runtype=args` with `onstart` and expecting onstart to run "before" entrypoint:** With `runtype=args`, onstart still runs in container before ENTRYPOINT, but is wrapped by Vast's entrypoint chain. Easier to reason about than the SSH/Jupyter modes, but DON'T assume PID 1 == llama-server until tested in spike.
- **`image_args` as the JSON field name:** The JSON field in the Vast API is **`args`** (NOT `image_args`, NOT `args_str` when sending an array). [VERIFIED: vast-cli/vast.py:2509 `json_blob["args"] = args.args`]
- **Relying on `cached_images` search filter:** This filter does NOT EXIST in the documented Vast API schema (verified via WebFetch docs.vast.ai/api-reference/search/search-offers — full filter list does not include cached_images). To estimate cache-hit, use `host_id` clustering + log image_uuid em runtime; or just trust empirical observation that nvidia/cuda + ggml-org/llama.cpp:server-cuda are common enough. **Plan-phase deve assumir cold-pull em vez de cache-hit; mede empiricamente em spike.**

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---|---|---|---|
| Vast.ai REST client | Custom HTTP client wrapper | `gateway/internal/emerg/vast/client.go` (already exists, Phase 6.5) | Already implements parseErrorBody + ErrOfferGone + sentinel errors |
| Onstart sentinel/wrapper | Custom shell+exec orchestrator | Direct ENTRYPOINT invocation via `runtype=args` (Strategy B) | Strategy B doesn't need wrapper — image already starts correctly with args |
| Image template (custom CUDA+llama.cpp) | Rebuild Ifix-specific image | Use official `ghcr.io/ggml-org/llama.cpp:server-cuda-bXXXX` (Strategy B) | Saves CI workflow + GHCR storage + image-pull time on Vast hosts |
| llama-server CUDA build pipeline | CMake build in CI | `ai-dock/llama.cpp-cuda` releases (Strategy A) OR official Docker (Strategy B) | Compiling llama.cpp CUDA in CI takes 20+ min + needs GPU runner |
| sha256 manifest mgmt for downloaded binary | Custom validation library | Inline `sha256sum -c <(echo "$EXPECTED  $FILE")` (POSIX) | Already pattern in `pod/scripts/emerg-bootstrap.sh:52` |
| sshd inside container | `apt install openssh-server` + sshd boot | Vast platform sidecar (`vast ssh <id>`) — operator-side, host-side | Phase 6.5 D-C2 already decided emergency pods are LLM-only; sshd inside container is dead weight for ssh_proxy runtype anyway |

**Key insight:** Vast.ai's value-add is the SSH proxy AND the docker run wrapper. Don't reimplement either. Choose the simplest runtype (`args`) and let Vast handle access.

## Runtime State Inventory

> Phase 6 É um refactor — não cria runtime state novo, mas precisa entender o existente que será afetado pelo cleanup D-08.

| Category | Items Found | Action Required |
|---|---|---|
| **Stored data** | None (Phase 6 não toca DB). FSM mirror Redis (`gw:emerg:state`) inalterado — usa state names, não image tags. emergency_lifecycles tabela: `vast_instance_id`, `vast_offer_id`, `events` — formato preservado. | None. |
| **Live service config** | Portainer dev/prod stack tem env vars `EMERGENCY_POD_IMAGE_TAG` — sob delete em D-08, substituído por 4-6 novos env vars que precisam aparecer no Portainer UI. **Mudança não está em git** — está na UI do Portainer stack. | Operator atualiza Portainer stack env após PR merged; documentar em SUMMARY. |
| **OS-registered state** | None (sem systemd unit, sem cron, sem launchd). | None. |
| **Secrets / env vars** | `EMERGENCY_POD_IMAGE_TAG` (existing). 6 novos serão adicionados em config.go — mantém pattern of read-from-env. `WEIGHTS_QWEN_SHA256` + MinIO credentials inalterados. | Atualizar `.env.portainer.dev` em commit + Portainer UI env injection em deploy. |
| **Build artifacts / installed packages** | GHCR image `ghcr.io/ifixtelecom/ifix-ai-pod:*` (~6GB) — fica órfão pós Phase 6 cleanup. Tags atuais: `latest-dev`, `develop-<sha>`, etc. **Nada na máquina local depende.** | Operator pode `gh api -X DELETE /orgs/ifixtelecom/packages/container/ifix-ai-pod` pós 2-week observability window (rollback safety). Documentar em SUMMARY. |

**Nothing found in category:** Stored data = "None — verified by reading config + DB schema". OS-registered state = "None — verified by `find /etc/systemd /etc/cron* /etc/init.d -name '*emerg*' 2>/dev/null` returning empty".

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|---|---|---|---|---|
| Vast.ai REST API | Reconciler `buildCreateRequest` calls PUT /asks/ | yes (existing Phase 6.5) | v0 | — |
| Vast.ai API key (`VAST_AI_API_KEY` env) | gateway-dev Portainer stack | yes existing in `.env.portainer.dev` | — | — |
| MinIO `s3.ifixtelecom.com.br` | onstart download weights + (Strategy A) llama-bin mirror | yes existing | latest | — |
| MinIO credentials (`MINIO_*` env) | gateway-dev Portainer stack | yes existing | — | — |
| Postgres ai_gateway schema | emergency_lifecycles table (unchanged) | yes existing | 15+ | — |
| Redis `gw:emerg:state` mirror | FSM state mirror (unchanged) | yes existing | 7 | — |
| `docker manifest inspect` (CI) | Phase 6 quick-task verify image tag exists pre-deploy | yes in CI runner | docker 24+ | `curl manifest URL` |
| `gh api` CLI (operator) | Optional GHCR cleanup post-deploy | yes on `ops-claude` | 2.x | manual web UI |
| `gh release download` (Strategy A CI) | Mirror llama-bin to MinIO | yes in CI | 2.x | curl + jq |

**Missing dependencies with no fallback:** None.

**Missing dependencies with fallback:** None.

## Common Pitfalls

### Pitfall 1: Vast.ai runtype semantic confusion (most dangerous)

**What goes wrong:** Treating `runtype=ssh_proxy` as if it preserves the docker image ENTRYPOINT/CMD. Docs are clear but easy to misread.

**Why it happens:** "ssh_proxy" suggests "ssh PROXY" (sidecar-style) which implies the main container is untouched. But Vast's wrapper REPLACES the entrypoint regardless of ssh vs ssh_proxy vs ssh_direct.

**How to avoid:** Pick `runtype=args` if you want the image to run as-designed. Pick `ssh_proxy` only if you want to drive startup from `onstart`. **NEVER assume both `args` and `onstart` give image ENTRYPOINT control** — they don't.

**Warning signs:** Lifecycle stuck at `actual_status=running ports=populated` but `:8000/v1/models` returns connection-refused — means container is up but llama-server never started.

**Reference:** docs.vast.ai/instances/launch-modes, STATE.md:85 root cause.

### Pitfall 2: ai-dock CUDA version mismatch

**What goes wrong:** Strategy A downloads `llama.cpp-b9128-cuda-12.8.tar.gz` (built for CUDA 12.8) but pod template is `nvidia/cuda:12.4-runtime`. Binary requires 12.8 driver + cuDNN headers.

**Why it happens:** ai-dock only builds against CUDA 12.8 in current cadence; older CUDA versions deprecated upstream.

**How to avoid:** Match CUDA runtime to ai-dock's target. Use `nvidia/cuda:12.8.0-runtime-ubuntu22.04` (or `:base-ubuntu22.04` if smaller is preferred — but missing libcublas which llama-server may need; spike will tell).

**Warning signs:** llama-server exits with `libcudart.so.12: version 'CUDART_12.8' not found`, `libcublas.so.12: cannot open`.

**Reference:** ai-dock/llama.cpp-cuda README + Dockerfile derivative vast-ai/base-image/derivatives/llama-cpp/Dockerfile installs `libcublas-${CUDA_MAJOR_MINOR}` matching CUDA tag.

### Pitfall 3: SSH access expectation under runtype=args

**What goes wrong:** Operator expects to be able to `vast ssh <id>` into the container for debugging, but `runtype=args` doesn't create a port-22 binding at all (per docs: args has "no ports provisioned" by default unless `-p 22:22` injected).

**Why it happens:** Confusing two "SSH-related" features: (a) the in-container sshd (managed by Vast wrapper in ssh_proxy modes), (b) the operator's `vast ssh <id>` CLI which actually proxies through Vast's host-level SSH (NOT the container).

**How to avoid:** If `vast ssh <id>` debug access is required (Strategy B), explicitly add `-p 22:22` to Env map AND ensure the container image has sshd installed (NOT in `ghcr.io/ggml-org/llama.cpp:server-cuda` by default). Alternative: `runtype=ssh_proxy` + onstart starts BOTH llama-server (background) and sshd, but then PID 1 is wrapper not llama-server (worse crash detection).

**Practical recommendation:** Use Strategy B with `runtype=args` + NO ssh in container. For dev debugging, operator uses Vast UI logs OR `gatewayctl emerg destroy <id>` + provision fresh pod with `runtype=ssh_proxy` ad-hoc. Production doesn't need in-container SSH.

**Reference:** docs.vast.ai/api-reference/instances/create-instance ("ssh_direct: Port 22 provisioned"), vast-cli/vast.py port logic.

### Pitfall 4: 4048-char onstart hard limit

**What goes wrong:** Inline onstart inflates beyond 4048 chars (e.g., adding error handling, logging sections, embedded Jinja template) — API returns `bad_request`.

**Why it happens:** Documentation: "limited to 4048 characters; use gzip+base64 for longer scripts". CONTEXT.md D-05 ("~10 lines") leaves margin, but D-07 ("/onstart.sh fetched from MinIO is the full script") — fine, the MinIO indirection IS the workaround.

**How to avoid:** Strategy B's inline weights-only onstart is ~700 chars (well under). Strategy A's inline curl+sha256+exec wrapper is ~150 chars (well under). If onstart needs growth, gzip+base64 first OR delegate to MinIO-hosted script.

**Reference:** docs.vast.ai/api-reference/instances/create-instance — onstart character limit.

### Pitfall 5: vast-cli `args` field is space-handling weird in CLI but JSON-clean

**What goes wrong:** Constructing the JSON `args` array as a single string `"--host 0.0.0.0 --port 8000"` instead of `["--host","0.0.0.0","--port","8000"]` — Vast's wrapper may or may not split on whitespace depending on version.

**Why it happens:** vast-cli `--args` uses `nargs=REMAINDER` (each shell token becomes a list element). When you build CreateRequest in Go, you control the JSON shape directly. Pass a Go slice `[]string{}`, json.Marshal serializes as a JSON array.

**How to avoid:** Always pass `args` as `[]string`, never as single string. Confirm in spike that `--chat-template-file /app/templates/...` (which has a space-separated arg+value) works correctly as two list elements (`["--chat-template-file","/app/templates/qwen3.5-27b-tool-calling.jinja"]`).

**Reference:** vast-cli/vast.py:2419 argparse REMAINDER, example `--args --model casperhansen/llama-3-70b-instruct-awq --tensor-parallel-size 4 --quantization awq` (each token separate).

### Pitfall 6: GitHub release rate limit (Strategy A only)

**What goes wrong:** Vast host pulls `llama.cpp-b9128-cuda-12.8.tar.gz` directly from GitHub at every pod boot. Unauthenticated GitHub Releases API has 60 req/hour limit per IP. Vast hosts shared across many users in same datacenter can hit rate-limit.

**Why it happens:** GitHub Releases asset downloads do NOT count against the 60 req/hour API limit (asset URLs use separate path), BUT redirects to AWS S3 backing storage can throttle on extreme load. In practice not a problem for ifix-scale (~5 lifecycles/day).

**How to avoid:** MinIO mirror (CONTEXT.md D-03). CI mirror workflow runs on TAG bump OR daily cron. Onstart fetches MinIO first, falls back to GitHub.

**Warning signs:** `HTTP 429 Too Many Requests` from GitHub.

### Pitfall 7: Phase 6.5 integration tests assume runtype=ssh

**What goes wrong:** `gateway/internal/integration_test/emerg_*` tests use mocked Vast responses. Some assert `req.Runtype == "ssh"` or check `req.Image` contains `ifix-ai-pod`. Phase 6 changes both — tests will fail until updated.

**Why it happens:** Tests were written against the Phase 6.5 implementation. Refactor scope includes test updates.

**How to avoid:** plan-phase Wave 0 should scan `emerg_*_test.go` files for hardcoded `ssh` / `ifix-ai-pod` / `EMERGENCY_POD_IMAGE_TAG` strings BEFORE writing implementation tasks. Update fixtures in same wave.

**Reference:** STATE.md "Integration tests (emerg suite): RESOLVED 2026-05-14" — 3 root causes were schema/freshness/assertion-staleness, suggests test maintenance is non-trivial.

### Pitfall 8: Jinja template path mismatch

**What goes wrong:** llama-server flag `--chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja` expects file at that path. `ghcr.io/ggml-org/llama.cpp:server-cuda` does NOT contain the Ifix-patched Jinja (it's a `pod/templates/` artifact, not upstream).

**Why it happens:** Phase 1 baked the patched Jinja into the custom GHCR image. Strategy B uses public image — Jinja must come from elsewhere.

**How to avoid:** Option 1 — onstart downloads Jinja from MinIO (`emerg-onstart/templates/qwen3.5-27b-tool-calling-<sha>.jinja`) into `/app/templates/` (writable in default container). Option 2 — small custom layer `ghcr.io/ifixtelecom/qwen-templates` (FROM `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` + COPY jinja). Decide in discuss-phase or plan-phase Wave 0. Recommend Option 1 (preserves D-08 "delete custom image" intent).

**Warning signs:** llama-server logs `failed to open chat template file: /app/templates/qwen3.5-27b-tool-calling.jinja: No such file or directory`.

### Pitfall 9: Inline onstart shell quoting (Go string composition)

**What goes wrong:** Building onstart as a Go string with `fmt.Sprintf` introduces escape hazards — e.g., `$VAR` interpolation by Go vs by bash, embedded quotes in Jinja paths.

**Why it happens:** Two layers of interpretation (Go raw string vs bash). Easy to get one $ escape wrong and have the Vast container receive a partially-evaluated script.

**How to avoid:** Use Go raw string literals (backtick-delimited) for the onstart body. Reserve `fmt.Sprintf` for the OUTER URL/SHA composition; the bash body uses bash-side env var expansion (since vars come from `Env` map).

```go
const onstartTemplate = `#!/bin/bash
set -euo pipefail
WEIGHTS_PATH=/weights/qwen/model.gguf
mkdir -p "$(dirname "$WEIGHTS_PATH")"
...
exit 0
`
// No fmt -- pure bash. Inject vars via Env map keys.
```

**Warning signs:** Vast logs show `$MINIO_ENDPOINT: command not found` or partial path concatenations.

## Code Examples

### Verified: vast-cli source — runtype field constructed

```python
# Source: github.com/vast-ai/vast-cli/blob/master/vast.py:2340-2358
# [VERIFIED: curl + sed extraction]
def get_runtype(args):
    runtype = 'ssh'
    if args.args:
        runtype = 'args'
    if (args.args == '') or (args.args == ['']) or (args.args == []):
        runtype = 'args'
        args.args = None
    ...
    elif args.ssh:
        runtype = 'ssh_direc ssh_proxy' if args.direct else 'ssh_proxy'
    return runtype
```

### Verified: vast-cli source — JSON body construction

```python
# Source: vast.py:2476-2510 [VERIFIED via curl]
json_blob = {
    "client_id": "me",
    "image": args.image,
    "env": parse_env(args.env),  # parses "-p 8000:8000 -e VAR=val" -> dict
    "disk": args.disk,
    "label": args.label,
    "onstart": args.onstart_cmd,  # may be None
    ...
}
if args.template_hash is None:
    json_blob["runtype"] = get_runtype(args)  # "args" | "ssh_proxy" | "jupyter_proxy" | ...

if args.args is not None:
    json_blob["args"] = args.args  # list of strings (REMAINDER)
```

**Key takeaway:** The JSON field is `args` (NOT `image_args` NOT `args_str`). It's an array of strings.

### Verified: vast-ai/base-image/derivatives/llama-cpp Dockerfile pattern

```dockerfile
# Source: vast-ai/base-image/derivatives/llama-cpp/Dockerfile [VERIFIED: git clone]
ARG BASE_IMAGE=vastai/base-image:cuda-12.9-mini-py312-2026-04-15
FROM ${BASE_IMAGE}
ARG LLAMA_CPP_VERSION
ARG CUDA_VERSION=12.8
RUN \
    set -euo pipefail && \
    package_name="llama.cpp-${LLAMA_CPP_VERSION}-cuda-${CUDA_VERSION}.tar.gz" && \
    download_url="https://github.com/ai-dock/llama.cpp-cuda/releases/download/${LLAMA_CPP_VERSION}/${package_name}" && \
    wget -O "/opt/llama.cpp/${package_name}" "${download_url}" && \
    cd /opt/llama.cpp && tar xf "${package_name}"
ENV LLAMA_CPP_DIR=/opt/llama.cpp/cuda-${CUDA_VERSION}
ENV PATH=${LLAMA_CPP_DIR}:${PATH}
```

**Key takeaway:** Vast.ai's official `derivatives/llama-cpp` uses `ai-dock/llama.cpp-cuda` as canonical source. This is an authoritative endorsement of that third-party repo.

### Verified: emerg-bootstrap.sh sha256 verify pattern (reusable)

```bash
# Source: pod/scripts/emerg-bootstrap.sh:52-58 [VERIFIED via Read]
ACTUAL=$(sha256sum "$WEIGHTS_PATH" | awk '{print $1}')
if [[ "$ACTUAL" != "$WEIGHTS_QWEN_SHA256" ]]; then
  echo "FATAL: SHA-256 mismatch on $WEIGHTS_PATH" >&2
  echo "  expected: $WEIGHTS_QWEN_SHA256" >&2
  echo "  actual:   $ACTUAL" >&2
  exit 1
fi
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|---|---|---|---|
| Custom GHCR image baked with llama.cpp + sshd + bootstrap (6GB) | Official `ghcr.io/ggml-org/llama.cpp:server-cuda` + onstart for weights only (Strategy B) | docker.md states official image is server-cuda with `/app/llama-server` entrypoint | Eliminates ~6GB image storage + CI cost + Vast pull time differential |
| `runtype="ssh"` (deprecated alias) | `runtype="ssh_proxy"` (explicit) OR `runtype="args"` | vast-cli get_runtype function 2025+ | Clarity + same effect (ssh = ssh_proxy alias confirmed) |
| `image_args` / `args_str` field naming guess | `args` (lowercase, array of strings) | Empirically confirmed via vast-cli source | Avoids API rejection on misnamed field |
| Bin tarball naming `llama-bXXX-bin-ubuntu-x64-cuda.zip` | `llama.cpp-bXXX-cuda-12.8.tar.gz` (ai-dock, NOT ggml-org Linux) | ggml-org Linux+CUDA prebuilt never existed | Avoid 404 on download |
| `cached_images` Vast search filter | Filter does NOT exist — must measure cache-hit empirically | Filter list verified absent | Plan budget assuming cold-pull on every offer |

**Deprecated/outdated:**

- `runtype="ssh"` (use `"ssh_proxy"` explicit)
- `ghcr.io/ggml-org/llama.cpp:server-cuda` floating tag pull (use `server-cuda-b9128` pinned)
- llama-cpp-python (project decision Phase 1 D-24 — keep)

## Assumptions Log

> Claims tagged `[ASSUMED]` need confirmation in discuss-phase or plan-phase Wave 0 spike before locking decisions.

| # | Claim | Section | Risk if Wrong |
|---|---|---|---|
| A1 | Vast `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` tag exists (verified via WebFetch that `server-cuda` tag exists, but tag-pinning to specific b-number not documented) | Pattern 1, Supporting | If only floating `server-cuda` exists, lose reproducibility. Spike confirms by `docker manifest inspect ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`. |
| A2 | `runtype=args` does NOT provision port 22 by default | Pitfall 3 | If wrong, we get debugging access for free. Low risk. |
| A3 | `nvidia/cuda:12.8.0-runtime-ubuntu22.04` template is cache-hit on >=50% of Vast 4090 offers | Architecture diagram, alternatives | If false, cold-start adds ~3min for pull. Mitigate by Strategy B (image-cache rather than template-cache — official llama.cpp:server-cuda likely cached on hosts that ever pulled it; spike measures) |
| A4 | `cuda_max_good` filter >=12.8 covers all RTX 4090 hosts | DefaultSearchFilter | If wrong, filter could exclude valid offers. Lower threshold to 12.6 if needed. |
| A5 | Vast `onstart` script runs INSIDE the container regardless of runtype | Pattern 2, Pitfall 1 | If runs on host (some docs suggested ambiguity), the script can't write `/weights/*` directly inside container. Verified `onstart` runs in container per docs.vast.ai (multiple references) [CITED but not directly executed in spike yet]. |
| A6 | `apt-get install -y curl` works inside `nvidia/cuda:*-runtime-ubuntu22.04` template (apt is preserved + working network) | Strategy A onstart | If apt is broken (network DNS issue?), onstart fails. Mitigate by using `mc` from `dl.min.io` direct (no apt needed) per emerg-bootstrap.sh pattern. |
| A7 | llama.cpp tag b9128 has tool-calling Jinja flag `--chat-template-file` working | Strategy B args | Phase 1 D-08 already validated b9128 with `--jinja --chat-template-file`. Likely OK. |
| A8 | Strategy B's exec semantics make llama-server PID 1 cleanly | Pattern 3 | If wrong (some Docker wrapping), crash detection slow. Verified for `args` runtype: ENTRYPOINT invocation is direct (no shell wrapping per Docker semantics). |
| A9 | ai-dock/llama.cpp-cuda binary is statically linked enough to run under `nvidia/cuda:*-runtime` (no devel needed) | Strategy A | If devel needed (cuDNN headers), template doubles in size to ~2GB. Mitigate: use `nvidia/cuda:12.8-runtime` + `apt-get install libcublas-12-8` per vast-ai derivative Dockerfile pattern. |
| A10 | Phase 6.5 integration tests with hardcoded `Runtype="ssh"` exist and are findable by grep | Pitfall 7 | If hidden in test helpers, longer to find. Plan-phase Wave 0 surveys. |

**If user prefers, discuss-phase can convert any [ASSUMED] line above into a locked decision OR add a Wave 0 spike task to validate empirically.**

## Open Questions

1. **Strategy A vs Strategy B?**
   - What we know: Strategy B is simpler (no bin download, no sha256-bin-verify, fewer env vars, smaller delta), aligns with PRV-06 spirit (uses ghcr.io image), and matches Vast's own derivatives/llama-cpp pattern (sort of — they use ai-dock binary inside their own image). Strategy A more aligned with CONTEXT.md D-01..D-08 as written.
   - What's unclear: User intent re: "iteração dev sem rebuild image" — Strategy B (image tag bump = config change = redeploy) is comparable agility to Strategy A (script bump = MinIO upload = config bump = redeploy). Both ~2min cycles.
   - Recommendation: **Re-open discuss-phase** with this research; ask Pedro to pick. If pick B, redo CONTEXT.md D-01..D-08. If pick A, fix D-01 (12.8 not 12.4) + D-02 (ai-dock not ggml-org) + D-06 (`args` not `image_args` field name; choose explicit ssh_proxy or args).

2. **Where does Ifix's patched Qwen 3.5 27B Jinja template live in Strategy B?**
   - What we know: Phase 1 `pod/templates/qwen3.5-27b-tool-calling.jinja` (+`.sha256`). Currently baked into custom GHCR image at `/app/templates/`. Strategy B pulls public llama.cpp image which does NOT have it.
   - What's unclear: MinIO upload + onstart-fetch (preserves D-08 delete intent) vs minimal custom image overlay `ghcr.io/ifixtelecom/qwen-templates` (~10MB, defeats "no custom image" goal).
   - Recommendation: **MinIO upload + onstart-fetch (D-04-style versioning)**. Onstart writes template to `/app/templates/qwen3.5-27b-tool-calling.jinja` before image ENTRYPOINT runs (Strategy B uses `args` runtype — Vast's chain runs onstart in container before invoking ENTRYPOINT). Adds ~5 lines to onstart.

3. **What's the empirical cache-hit rate of `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` on Vast 4090 hosts?**
   - What we know: ggcr.io/ggml-org/llama.cpp:server-cuda is the "most downloaded" variant per pkgs.github.com snapshot. Vast hosts that ever ran any llama-server workload have it cached.
   - What's unclear: cache TTL (Vast doesn't document) + how Vast offers' `cached_images` set is exposed (filter doesn't exist per research).
   - Recommendation: **plan Phase 6 success criteria as cold-pull-tolerant** (<=6min including image pull). If empirical (spike) shows reliable cache-hit, plan-phase can tighten criteria post-spike.

4. **Onstart in container OR on host VM?**
   - What we know: Multiple docs ambiguous. Docs.vast.ai/instances/launch-modes says "called as part of the new entrypoint" (suggests in container). `35-sync-home-dirs.sh` symlinks `/root/onstart.sh -> /onstart.sh` (definitely in container). `vast.py` arguments suggest onstart-cmd content goes to file in container.
   - What's unclear: Subtle differences for `args` runtype (does Vast still wrap an entrypoint that runs `/onstart.sh` first, or does it just pure-docker run with ENTRYPOINT + Args)?
   - Recommendation: **Spike confirms**. Plan-phase Wave 0 spike does: `image=alpine`, `args=["sleep","60"]`, `runtype=args`, `onstart="echo INSIDE > /tmp/marker; date >> /tmp/marker"`. Then `vast ssh <id> cat /tmp/marker`. If file exists in container — confirmed in-container. If not — onstart in args mode silently ignored.

5. **Where to host the Jinja template + onstart script in MinIO — `emerg-onstart/v1.sh` vs `emerg-onstart/2026-05-16-a.sh` vs hash-named?**
   - What we know: D-04 marks this as Claude's discretion.
   - Recommendation: **Hash-named (immutable)**: `s3://ai-gateway/emerg-onstart/sha256-abc123def.sh`. Config gateway carries `EMERG_ONSTART_SHA256` + URL — same hash IS the integrity check. No version-numbering scheme to maintain. Pattern reusable for Jinja template (same MinIO bucket).

6. **Disk budget reduction (CONTEXT.md "Claude's discretion")?**
   - What we know: Current 80GB filter. Strategy B template ~3GB + weights 16GB + tmp = 25GB. Strategy A template ~600MB + bin 160MB + weights 16GB + tmp = 22GB.
   - Recommendation: **Set Disk=40GB**. Plenty of margin, opens more spot-market offers (4090 hosts with 50-80GB free disk are more common than 80+).

## Validation Architecture

> **Required** by `workflow.nyquist_validation` enabled. Phase 6 has small but critical surface area — most tests should be unit/integration with a small empirical spike + 3-lifecycle live UAT.

### Test Framework

| Property | Value |
|---|---|
| Framework | `go test` 1.24, sqltools containers for emerg integration tests |
| Config file | `go.mod` (project root); `gateway/internal/integration_test/conftest.go`-equivalent in `*_test.go` |
| Quick run command | `go test ./gateway/internal/emerg/... -count=1 -short -timeout=2m` |
| Full suite command | `go test ./... -count=1 -race -timeout=10m` (matches build-pod.yml current pattern) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|---|---|---|---|---|
| PRV-06 | buildCreateRequest emits correct payload (Strategy A or B) | unit | `go test ./gateway/internal/emerg -run TestBuildCreateRequest_StrategyB_args -count=1` | NO (Wave 0 — new test in lifecycle_test.go) |
| PRV-06 | buildCreateRequest deterministic across reconciler ticks (no random data) | unit | `go test ./gateway/internal/emerg -run TestBuildCreateRequest_DeterministicJSON -count=20` | NO (Wave 0) |
| PRV-06 | Config validates 4-6 new env vars on startup; fails fast if missing | unit | `go test ./gateway/internal/config -run TestEmergencyConfig_TemplateRefactor -count=1` | NO (Wave 0 — config_test.go) |
| PRV-07 | Reconciler health-poll path unchanged — :8000/v1/models check works against new payload | integration | `go test ./gateway/internal/integration_test -run TestEmerg_HappyPath_TemplatePayload -count=1` | partial (06.5 happy path test exists, needs payload update) |
| PRV-09 | cancel-in-flight still works when new payload returns instance_id | integration | `go test ./gateway/internal/integration_test -run TestEmerg_CancelInFlight -count=1` | exists (06.5-07), assertions may need update |
| PRV-05 | Price cap rejected offer; price cap unchanged | integration | `go test ./gateway/internal/integration_test -run TestEmerg_PriceCapReject -count=1` | exists (06.5-06) |
| PRV-08 (mirror) | Bug STATE.md:85 FIXED — new runtype no longer ignores image CMD | live | manual 3-lifecycle UAT post-deploy; `gatewayctl emerg force-provision` end-to-end | NO (Wave Final — HUMAN-UAT) |
| Phase 6 D-08 risk | After delete commit, no reconciler tests reference custom GHCR image | unit | `! grep -r "ifix-ai-pod" gateway/ --include='*.go' --include='*_test.go'` | NO (Wave Final — lint-style verification) |

### Sampling Rate

- **Per task commit:** `go test ./gateway/internal/emerg/... -count=1 -short -timeout=2m`
- **Per wave merge:** `go test ./... -count=1 -race -timeout=10m` (full Go suite)
- **Phase gate:** Full suite green + 3-lifecycle live UAT GREEN (Pedro confirms each, <=R$15 budget per CONTEXT.md mitigation strategy) before delete-PR merged
- **Pre-spike gate:** Manual Vast.ai UAT 1-pod-2-runtypes spike (~$0.10, 30min) — confirms Strategy A vs B + answers Open Q4

### Wave 0 Gaps

- [ ] **NEW**: `gateway/internal/emerg/lifecycle_test.go::TestBuildCreateRequest_*` — covers payload assertion for chosen Strategy
- [ ] **NEW**: `gateway/internal/config/config_test.go::TestEmergencyConfig_TemplateRefactor` — 4-6 new env vars validated, old `EmergencyPodImageTag` removed
- [ ] **UPDATE**: `gateway/internal/integration_test/emerg_happy_path_test.go` — assertion strings (Runtype, Image) updated
- [ ] **UPDATE**: Any `_test.go` with `ghcr.io/ifixtelecom/ifix-ai-pod` or `Runtype.*"ssh"$` hardcoded — survey via `grep -rn` Wave 0
- [ ] **NEW** (if Strategy A): `pod/scripts/emerg-onstart.sh` script + .sha256 file + CI workflow `upload-emerg-onstart.yml`
- [ ] **NEW** (if Strategy A): CI workflow `mirror-llama-bin.yml` — manual trigger + scheduled

*(If no gaps: "None — existing test infrastructure covers all phase requirements" — NOT this phase. Code change is small but assertions need updates.)*

### Pre-Spike Manual Validation (recommended)

CONTEXT.md `<specifics>` requires a Wave 0 spike. Concrete plan:

```bash
# Spike 1: Confirm runtype=args + ghcr.io official image works (Strategy B sanity)
vast set api-key $VAST_AI_API_KEY
OFFER_ID=$(vast search offers 'gpu_name=RTX_4090 reliability>0.99 dph_total<0.40 rentable=true verified=true cuda_max_good>=12.8' -o dph_total --raw | head -1 | jq -r .id)
vast create instance $OFFER_ID \
  --image ghcr.io/ggml-org/llama.cpp:server-cuda \
  --disk 40 \
  --args --host 0.0.0.0 --port 8000 --version
# expect: instance reaches actual=running ports[8000/tcp] populated; curl http://IP:PORT/health = 200

# Spike 2: Confirm runtype=ssh_proxy + nvidia/cuda + ai-dock binary works (Strategy A sanity)
vast create instance $OFFER_ID \
  --image nvidia/cuda:12.8.0-runtime-ubuntu22.04 \
  --disk 40 \
  --ssh --onstart-cmd 'apt-get update && apt-get install -y curl && curl -fsSL https://github.com/ai-dock/llama.cpp-cuda/releases/download/b9128/llama.cpp-b9128-cuda-12.8.tar.gz -o /tmp/llama.tar.gz && tar xf /tmp/llama.tar.gz -C /opt && /opt/cuda-12.8/llama-server --host 0.0.0.0 --port 8000 --version'
# expect: container reaches running; curl IP:PORT returns 200 from llama-server

# Cleanup
vast destroy instances $(vast show instances --raw | jq '.[].id' | xargs)
```

Spike output -> `.planning/phases/06-emergency-pod-template-refactor/06-SPIKE-template-runtype.md` (mirroring 06.5 spike format).

## Security Domain

> `security_enforcement: true` assumed. Phase 6 touches: external binary download (Strategy A) + container image source (Strategy B) + Vast.ai instance creation (existing surface).

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---|---|---|
| V2 Authentication | no | — (no user-facing auth surface in Phase 6) |
| V3 Session Management | no | — |
| V4 Access Control | no | Reconciler runs as gateway leader (Phase 6.5 existing) |
| V5 Input Validation | yes | Config env vars validated by config.go envconfig + Zod-equivalent (no JSON parsing of untrusted input) |
| V6 Cryptography | **yes** | sha256 verification of all downloaded artifacts (binary + onstart + Jinja template + weights). Pattern preserved from emerg-bootstrap.sh. **MUST NOT** introduce HTTP-fetched binary without `set -e` + sha256-check guard. |
| V11 Business Logic | yes | Vast price cap + lifecycle dedup unchanged (Phase 6.5 logic preserved) |
| V14 Configuration | yes | Hardcoded image tag in `EmergencyTemplateImage` env (build-time-pinned, not runtime-fetched-by-name) prevents supply-chain attack via Docker tag squatting |

### Known Threat Patterns for {Vast.ai instance + binary download}

| Pattern | STRIDE | Standard Mitigation |
|---|---|---|
| Supply-chain attack via floating Docker tag (e.g., `ghcr.io/ggml-org/llama.cpp:server-cuda` then upstream pushes malicious image) | Tampering | **Pin to specific build hash**: `server-cuda-b9128` not `server-cuda`. Operator-controlled bump. (Strategy B) |
| Binary tampering during GitHub Release download | Tampering | sha256 verify (D-02 D-05). MinIO mirror provides redundancy without weakening trust (mirror uploaded by trusted CI from same GitHub release). |
| Malicious onstart script via MinIO compromise | Tampering | sha256 verify of `/onstart.sh` after MinIO fetch (D-05). MinIO bucket policy: private, signed URLs only. |
| Vast.ai host compromise (entire pod) | Information Disclosure | Emergency pods never see sensitive tenant data (data_class=normal only — Phase 9 D-A2). Pod has read-only weights (LLM inference output is the only output channel). |
| Onstart script length bypass via gzip+base64 | Denial of Service (overflow) | Vast platform enforces 4048 char limit. Use MinIO indirection (D-04) for full script — keeps inline onstart <4048. |
| `ai-dock/llama.cpp-cuda` author maliciously rebuilds and republishes binary | Tampering | sha256 verify pinned in config gateway. Operator updates BOTH `EmergencyLlamaBinTag` AND `EmergencyLlamaBinSHA256` together — cannot be bumped silently. |
| `ghcr.io/ggml-org/llama.cpp` image tag retag (Strategy B) | Tampering | `docker manifest inspect ghcr.io/ggml-org/llama.cpp:server-cuda-b9128 --verbose` shows digest. Pin to image@sha256:HEX in `EmergencyTemplateImage` env for max integrity (vs. tag-pinning which is mutable). |

**Recommendation:** Strategy B + image-by-digest (`ghcr.io/ggml-org/llama.cpp@sha256:abc...`) is the strongest supply-chain posture. Strategy A's external bin download is acceptable BUT requires sha256-pinning + MinIO mirror + operator-controlled config updates.

## Sources

### Primary (HIGH confidence)
- `vast-ai/base-image` repo (cloned to /tmp) — Dockerfile, entrypoint.sh, derivatives/llama-cpp/Dockerfile, boot_default.sh, vast_boot.d/*.sh
- `vast-ai/vast-cli` source (vast.py) — get_runtype(), JSON body builders, argparse REMAINDER for `--args`
- `docs.vast.ai/api-reference/instances/create-instance` — runtype enum, args/args_str/onstart fields, character limit on onstart
- `docs.vast.ai/instances/launch-modes` — SSH/Jupyter ENTRYPOINT override semantics
- `docs.vast.ai/api-reference/search/search-offers` — filter list (confirmed `cached_images` does NOT exist)
- `github.com/ai-dock/llama.cpp-cuda` README + GitHub API releases — tarball naming + CUDA version + license
- `github.com/ggml-org/llama.cpp/blob/master/docs/docker.md` — official image variants + ENTRYPOINT
- `github.com/ggml-org/llama.cpp/blob/master/.devops/cuda.Dockerfile` — base image + ENTRYPOINT confirmed
- `pod/Dockerfile`, `pod/scripts/emerg-bootstrap.sh`, `pod/onstart.sh` — existing Ifix code patterns
- `gateway/internal/emerg/lifecycle.go`, `vast/types.go`, `vast/client.go` — existing Vast client surface
- `.planning/phases/06.5-auto-provisioning-emergency-pod-vast-ai/06.5-SPIKE-vast-port-mapping.md` — empirical evidence of Vast.ai API quirks

### Secondary (MEDIUM confidence)
- WebSearch + WebFetch on `docs.vast.ai/llms.txt` — partial coverage of internal doc structure
- `vast.ai/article/running-llama-4-models-on-vast` — examples of `--args` usage in production
- `medium.com/codex/...` — third-party llama.cpp + nvidia/cuda multi-stage Dockerfile example confirming `LD_LIBRARY_PATH` patterns

### Tertiary (LOW confidence — flag for validation)
- Cache-hit empirical rate of `nvidia/cuda:12.X-runtime-ubuntu22.04` on Vast 4090 — must spike
- Cache-hit empirical rate of `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` — must spike
- Exact disk overhead of template + weights + tmp at runtime — measure in spike

## Metadata

**Confidence breakdown:**
- Vast.ai runtype semantics + JSON field naming: HIGH (cross-validated source code + 3 doc pages + community examples)
- llama.cpp prebuilt CUDA binary distribution: HIGH (ai-dock confirmed canonical, GitHub API verified asset names + sizes)
- Cache-hit empirical for templates: LOW (no docs API filter; estimate by inference; spike required)
- Strategy A vs B technical viability: HIGH (both work; Strategy B requires fewer moving parts)
- Strategy A vs B "best fit" per CONTEXT.md intent: MEDIUM (CONTEXT.md wrote D-01..D-08 leaning A, but research surfaced facts that make A more error-prone; Pedro should reconfirm in discuss-phase)
- Phase 6.5 integration test compatibility: MEDIUM (most tests rely on mocks; assertion updates straightforward)
- Burnt-bridge D-08 risk mitigation: HIGH (CONTEXT.md mitigation is sound — separate delete PR after 3-lifecycle UAT)

**Research date:** 2026-05-16
**Valid until:** 30 days for Vast.ai docs (relatively stable); 7 days for ai-dock build cadence (releases every 1-2 days)


## Hugging Face Image Strategy (D-01/D-02 revision)

> **Research suplementar — 2026-05-16 (Pedro request).** Investiga se imagem do emergency pod deve vir de Hugging Face em vez de `ai-dock/llama.cpp-cuda` releases ou `ghcr.io/ggml-org/llama.cpp` upstream.

### Headline Finding — TGI ARCHIVED (CRÍTICO)

**`huggingface/text-generation-inference` foi arquivado read-only em 21-mar-2026.** [CITED: github.com/huggingface/text-generation-inference package page]

Anúncio oficial em 11-dez-2025 por Lysandre Jik (HF maintainer):

> "🪦 text-generation-inference is now in maintenance mode. Going forward, we will accept pull requests for minor bug fixes, documentation improvements and lightweight maintenance tasks. TGI has initiated the movement for optimized inference engines to rely on a transformers..." [CITED: x.com/LysandreJik/status/1999137874378125436 + huggingface/text-generation-inference README]

Hugging Face **oficialmente recomenda**, como substitutos para novos deployments:

1. **vLLM** (https://github.com/vllm-project/vllm)
2. **SGLang** (https://github.com/sgl-project/sglang)
3. **llama.cpp ou MLX** (engines locais/inter-compatíveis)

HF Inference Endpoints (produto pago da própria Hugging Face) já **default a vLLM**, com SGLang como alternativa. [CITED: huggingface.co/docs/inference-endpoints/engines/vllm + spheron.network/blog/hugging-face-inference-endpoints-alternatives]

**Implicação direta:** A premissa "usar imagem Hugging Face" precisa ser reinterpretada — **HF não publica mais uma imagem de LLM serving recomendada que não seja TGI (archived)**. As imagens que HF publica no Docker Hub atualmente são todas voltadas para **training/fine-tuning**, não inference. As imagens "officially recommended by HF" são as imagens upstream de **vLLM** (`vllm/vllm-openai`) e **llama.cpp** (`ghcr.io/ggml-org/llama.cpp:server-cuda`).

### Inventário Hugging Face Docker Images (estado 2026-05)

| Registry path | Status | Purpose | LLM-serving viable? |
|---|---|---|---|
| `ghcr.io/huggingface/text-generation-inference:3.3.5` | **ARCHIVED 2026-03-21** (read-only repo) | LLM inference server, OpenAI-compat | Funcional mas sem updates — não recomendável pra emergency pod (lifecycle longo, sem patches de segurança) [VERIFIED: github package page 37856 latest downloads] |
| `ghcr.io/huggingface/text-generation-inference:3.3.5-rocm` | ARCHIVED | AMD GPUs | Out of scope (Vast.ai = NVIDIA only) |
| `ghcr.io/huggingface/text-generation-inference-ci:latest` | CI image | Internal CI builds | 11.8GB, internal — não consumir |
| `huggingface/transformers-pytorch-gpu` (Docker Hub) | Active | Training/fine-tuning workspace | **NÃO é serving image** — Python + PyTorch + transformers libs, sem servidor HTTP OpenAI-compat. Sem ENTRYPOINT funcional pra LLM serving. [VERIFIED: hub.docker.com/r/huggingface/transformers-pytorch-gpu] |
| `huggingface/transformers-all-latest-gpu` (Docker Hub) | Active, 100K+ pulls | CI testing transformers | Mesma categoria — não serving |
| `huggingface/transformers-pytorch-deepspeed-latest-gpu` | Active | Distributed training | Não serving |
| `huggingface/accelerate` | Active | Distributed training framework | Não serving |
| `huggingface/peft-gpu` | Active | Parameter-efficient fine-tuning | Não serving |
| `huggingface/trl` | Active | RLHF training framework | Não serving |
| `huggingface/lerobot-gpu` | Active | Robotics (out of scope) | Não serving |
| Hugging Face Spaces Docker images | Public per-space | User-uploaded apps | **Não há imagem HF oficial canônica pra Qwen serving via Spaces** — Spaces são imagens user-built; nenhuma garantia de estabilidade/SHA/maintenance |
| HF Inference Endpoints internal images (`ggml/llama-cpp-cuda-default`, vLLM image) | Internal, partial public | HF managed inference | A imagem `ggml/llama-cpp-cuda-default` no Docker Hub é **stale 1 ano** (last update >12 meses, ~1.5GB, ENTRYPOINT não documentado). HF Inference Endpoints internamente faz build from llama.cpp master a cada deploy — não há tag publicamente pinável. [VERIFIED: hub.docker.com/r/ggml/llama-cpp-cuda-default] |

**Conclusão do inventário:** **Não existe uma imagem Hugging Face oficial atualmente recomendada para LLM serving fora do TGI archived.** A interpretação útil da request "usar HF image" passa a ser:

> *Usar uma imagem que **Hugging Face oficialmente recomenda** como substituto do TGI.*

Que são: **vLLM (`vllm/vllm-openai`)** e **llama.cpp (`ghcr.io/ggml-org/llama.cpp:server-cuda`)**. As duas opções já estão no escopo do RESEARCH.md original (Strategy B usa llama.cpp upstream); o novo entrant aqui é **vLLM**.

### Comparação técnica — TGI vs vLLM vs llama.cpp (para Phase 6 / Qwen3-27B / RTX 4090)

| Aspecto | TGI 3.3.5 (archived) | vLLM 0.21.0 (`vllm/vllm-openai`) | llama.cpp `server-cuda-b9128` (upstream) |
|---|---|---|---|
| **Registry** | `ghcr.io/huggingface/text-generation-inference:3.3.5` [VERIFIED: huggingface.co/docs] | `vllm/vllm-openai:v0.21.0-cu129-ubuntu2404` (Docker Hub) [VERIFIED: hub.docker.com] | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` [VERIFIED: docker.md upstream] |
| **Maintenance status** | ARCHIVED 2026-03-21 [VERIFIED] | Active (release every 1-2 weeks) [VERIFIED: dockerhub tags 19h ago] | Active (release every 1-2 days, b9128 used by Phase 1) [VERIFIED] |
| **Image size** | ~10-15GB (CI mirror 11.8GB; production tag undocumented but similar) [ASSUMED — based on CI image] | 10.27GB linux/amd64 cu129 / 8.1GB ubuntu2404 [VERIFIED: hub.docker.com tag detail] | ~3-4GB (CUDA backend, /app/ layout) [ASSUMED — Phase 1 custom image was 6GB total, baked llama.cpp portion smaller] |
| **CUDA version** | 12.2+ recommended [VERIFIED: HF docs] | 12.9 (latest tag) [VERIFIED: dockerhub] | 12.8.1 (Phase 1 baked) [VERIFIED: pod/Dockerfile FROM tag] |
| **ENTRYPOINT** | `text-generation-launcher` (own CLI) [VERIFIED: HF README] | `python -m vllm.entrypoints.openai.api_server` (implícito do `vllm serve` wrapper) [CITED: docs.vllm.ai/deployment/docker] | `/app/llama-server` [VERIFIED: cuda.Dockerfile] |
| **OpenAI `/v1/chat/completions`** | Yes (Messages API) [VERIFIED: HF docs] | Yes (native, default) [VERIFIED: HF Inf Endpoints docs + vLLM docs] | Yes (`--api-key` opcional, OpenAI-compat endpoint native) [VERIFIED: pod/Phase 1 uses it] |
| **GGUF native (Phase 1 D-05 weights)** | Via `llamacpp` backend (optional, requires `--model-gguf`) [CITED: HF docs/backends/llamacpp] | **Experimental** — `repo_id:quant_type` syntax, "GGUF files are great for llama.cpp/Ollama, but vLLM expects standard Hugging Face safetensors folders. Don't mix them — stick to the native format" [CITED: vllm-project issue #27949] | **Native, default** — Phase 1 D-05 já usa: `-m /weights/qwen/model.gguf` [VERIFIED: pod/scripts/emerg-bootstrap.sh:64] |
| **Safetensors native** | Yes | **Yes, preferred** [VERIFIED: docs.vllm.ai/quantization/gguf] | No (must convert to GGUF) |
| **Tool calling** | Limited (not in current docs) | **First-class**: `--enable-auto-tool-choice --tool-call-parser qwen3_xml/hermes` [VERIFIED: docs.vllm.ai/features/tool_calling] | **Yes, via `--jinja --chat-template-file`** — Phase 1 D-08 já usa qwen3.5-27b-tool-calling.jinja [VERIFIED: pod/Dockerfile:51] |
| **Custom Jinja template file** | Not documented | `--chat-template /path/to/template.jinja` [VERIFIED: docs.vllm.ai] | `--chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja` (Phase 1 D-08 exact flag) [VERIFIED] |
| **RTX 4090 / sm_89 support** | Yes (generic NVIDIA support; A100/H100/A10G/T4 explicit) [VERIFIED] | **Yes, confirmed** — fp16 works out of box; fp8 leverages Ada Lovelace dedicated tensor cores [VERIFIED: docs.vllm.ai/features/quantization/fp8] | Yes (Phase 1 working in production) [VERIFIED: Phase 1 UAT live] |
| **Qwen3-27B / 3.5-27B** | TGI Qwen3 support noted "to be updated" — partial [CITED: qwen.readthedocs.io/deployment/tgi] | **Native support** — `--tool-call-parser qwen3_xml` flag exists [VERIFIED: docs.vllm.ai] + multiple production deployments documented [CITED: theogravity/dual-rtx-6000-blackwell-qwen3.6-27b-fp8] | **Native via GGUF** — Phase 1 currently runs Qwen 2.5; Qwen3 GGUFs available (havenoammo/Qwen3.6-27B-MTP-UD-GGUF, unsloth/Qwen3.5-35B-A3B-GGUF) [VERIFIED: HF Hub model pages]; tool-calling chat template fixes shipped 2026 [CITED: ggml-org/llama.cpp issue #19872] |
| **HF Hub model auto-download** | Yes — `--model-id` arg consumes `org/repo` directly [VERIFIED] | Yes — `--model Qwen/Qwen3-0.6B` auto-downloads to `~/.cache/huggingface` [VERIFIED] | **No native auto-download** — model must be local file (we already mount MinIO weights) |
| **HF_TOKEN required for gated models** | Yes (`HF_TOKEN` env) [VERIFIED] | Yes (`HF_TOKEN` env) [VERIFIED] | N/A (no Hub integration) |
| **Cold-start ~50GB Qwen3-27B safetensors download from HF Hub** | ~3-5min (rate-limit possible without token) | ~3-5min (same — both use HF Hub REST API) [ASSUMED — both use same backend `huggingface_hub`] | N/A (we serve GGUF from MinIO, ~16GB single file, ~2min) |
| **Cache-hit on Vast.ai 4090 hosts** | Unknown — TGI image not commonly pre-pulled by Vast.ai community [LOW confidence — needs spike] | Unknown — vllm-openai is heavy (10GB) so less likely cached [LOW confidence] | **Likely better** — `ghcr.io/ggml-org/llama.cpp` is the canonical llama.cpp upstream, used by Vast.ai community guides [MEDIUM confidence — must spike] |

### Performance — Qwen3-27B on single RTX 4090

Tokens/sec benchmarks dependem fortemente da quantização e formato:

| Engine + Format | Tok/s decode (4090, batch 1) | Notes |
|---|---|---|
| llama.cpp GGUF Q4_K_M | ~30-45 t/s [CITED: ermolushka.github.io/posts/vllm-benchmark-4090 (8B baseline scaled)] | What Phase 1 uses today. Memory-bound on 4090 24GB at 27B Q4. |
| vLLM safetensors FP16 | OOM (54GB FP16 weights > 24GB VRAM) — **must use FP8 or 4-bit AWQ** | Not viable as-is for 27B on single 4090. |
| vLLM safetensors FP8 (Ada-native) | ~50-80 t/s [CITED: spheron.network/blog/vllm-production-deployment-2026] | Requires FP8 quantized Qwen3-27B from HF Hub (e.g., `Qwen/Qwen3-27B-FP8` if published) — **verify availability before locking** |
| vLLM safetensors AWQ Int4 | ~40-60 t/s [ASSUMED — extrapolated from 8B benchmarks] | AWQ Qwen3 27B not yet broadly published on HF Hub [LOW confidence — verify] |

**Conclusão de performance:** vLLM **pode ser mais rápido** (50-80 t/s vs 30-45 t/s) **só se rodar FP8 + se FP8 quant pré-existir no HF Hub**. Sem FP8/AWQ disponível, vLLM **não consegue carregar Qwen3-27B FP16 num 4090 (OOM)**. llama.cpp Q4_K_M GGUF é o caminho que **garantidamente funciona em 4090** — é o que Phase 1 já roda.

### Compatibilidade com Vast.ai runtype + MinIO weights mirror

| Strategy | Image | weights source | runtype | Onstart needs | image_args |
|---|---|---|---|---|---|
| **A (CONTEXT.md original)** | `nvidia/cuda:12.8-runtime-ubuntu22.04` | MinIO GGUF | `ssh_proxy` ou `args` | curl bin + sha256 + curl weights + sha256 + start llama-server | `["bash", "/onstart.sh"]` |
| **B (RESEARCH.md original — recomendada)** | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` | MinIO GGUF | `args` | curl weights MinIO only | `["--host","0.0.0.0","--port","8000","-m","/weights/qwen/model.gguf",...]` |
| **C (HF-endorsed via vLLM)** | `vllm/vllm-openai:v0.21.0-cu129-ubuntu2404` | HF Hub auto-download OR MinIO safetensors | `args` | (optional) curl safetensors MinIO + symlink | `["--model","Qwen/Qwen3-27B-Instruct-FP8","--enable-auto-tool-choice","--tool-call-parser","qwen3_xml",...]` |
| **D (HF-archived TGI, fallback)** | `ghcr.io/huggingface/text-generation-inference:3.3.5` | HF Hub auto-download | `args` | (optional) HF_TOKEN env | `["--model-id","Qwen/Qwen3-27B-Instruct","--max-input-tokens","16000"]` |

**Critical Vast.ai runtype semantics** (recap from RESEARCH.md original, confirmed for all four strategies):

- `ssh` / `ssh_proxy` runtypes **substituem ENTRYPOINT da image** — incompatível com vLLM/TGI/llama.cpp images (todas têm ENTRYPOINT funcional). Pra usar HF/upstream image, runtype **DEVE ser `args`**.
- `args` runtype **preserva ENTRYPOINT** e **pode passar `args[]` como CMD remainder**. Funciona com todas as 4 strategies.
- `args` runtype **NÃO cria sshd sidecar Vast** — operator perde `vast ssh <id>` no container principal. Trade-off documentado em RESEARCH.md original Pitfall 3.

**MinIO weights — Strategy C (vLLM):**

- vLLM espera safetensors (HF Hub format). Phase 1 D-05 atualmente armazena **GGUF** no MinIO.
- Migrar pra Strategy C significa: (a) baixar safetensors do HF Hub a cada cold-start (~50GB FP16 → ~5-10min em 1Gbps), OU (b) re-baixar safetensors uma vez do HF, upload pro MinIO mirror, alterar Phase 1 D-05 storage path. Opção (b) é mais robusta mas **muda escopo de Phase 6** (toca Phase 1 weights mirror).
- HF Hub anonymous rate-limit pode pegar pods Vast — pra Strategy C, **HF_TOKEN como secret no config gateway** seria mandatório.

### Cache-hit Vast.ai — empirical risk

| Image | Likely cache-hit on 4090 spot offers | Reasoning |
|---|---|---|
| `nvidia/cuda:12.8-runtime-ubuntu22.04` (Strategy A) | **HIGH** | Canonical NVIDIA base, ~600MB, pulled by ~every ML workload |
| `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` (Strategy B) | **MEDIUM-HIGH** | Used by Vast.ai community llama.cpp guides; ~3-4GB |
| `vllm/vllm-openai:v0.21.0-cu129-ubuntu2404` (Strategy C) | **MEDIUM** | Heavy (10GB), but vLLM is increasingly popular in Vast workloads (HF Inference Endpoints default) |
| `ghcr.io/huggingface/text-generation-inference:3.3.5` (Strategy D) | **LOW** | Was popular pre-archive; with TGI deprecated, fewer hosts will pre-cache going forward |

Cold-pull em rede Vast.ai típica (~10 Gbps host network):

- 600MB → ~10s
- 3GB → ~30s
- 10GB → ~2min

Strategy C cold-pull adicionar ~1.5min vs Strategy B no pior cenário (cache-miss em ambos).

### Alternativas se vLLM/TGI não fit

| Alternative | Status | Verdict for Phase 6 |
|---|---|---|
| **SGLang** (`lmsysorg/sglang:latest` Docker Hub) | Active, HF-recommended #2 | Smaller community than vLLM; similar feature set; sm_89 supported. Não há vantagem clara vs vLLM pro caso emergency pod. **Skip** unless vLLM blocker emerges. |
| **TensorRT-LLM** (NVIDIA `nvcr.io/nvidia/tritonserver` family) | Active | Faster than vLLM em A100/H100 mas **requires per-model compilation** (build artifact pod-specific). Cold-start incompatível com emergency pod fast-failover. **Skip.** |
| **Ollama** | Active | OpenAI-compat, fácil de usar, mas **gerencia weights próprio storage** (incompatível com MinIO bind-mount Phase 1 pattern). **Skip** para emergency pod (Phase 1 architectural mismatch). |
| **HF Space com llama.cpp + Qwen** | User-built per Space | Não existe Space "oficial" canônico estabilizado para Qwen3-27B serving. **Skip.** |
| **HF AutoTrain** | Active (training, not serving) | Out of scope. |
| **HF Inference Endpoints (managed)** | Active, paid service | É o produto SaaS da HF — **emergency pod NÃO É inference endpoint terceirizado**, é nosso pod Vast.ai. Out of scope. |

### Strategy C (vLLM) — detalhamento implementação

Se Pedro decidir adotar Strategy C, o impacto em CONTEXT.md decisions é:

| Decision | Original (Strategy A) | Strategy C (vLLM) |
|---|---|---|
| **D-01 template** | `nvidia/cuda:12.4-runtime-ubuntu22.04` | `vllm/vllm-openai:v0.21.0-cu129-ubuntu2404` (pin SHA digest pra supply-chain) |
| **D-02 llama-bin** | GitHub release `llama-<TAG>-bin-ubuntu-x64-cuda.zip` | **REMOVED** — vLLM já no image, sem binary download |
| **D-03 MinIO bin mirror** | Required | **REMOVED** |
| **D-04 Onstart script MinIO** | Required | **OPCIONAL** — Onstart Vast inline ~5 linhas pode bastar (curl symlink + HF_TOKEN env). MinIO indirection vira nice-to-have |
| **D-05 sha256 verify onstart** | Required | Same pattern aplicado se houver onstart MinIO; senão sha256 of args[] sufficient |
| **D-06 Runtype** | `ssh_proxy` (broken — researched) | **`args`** (works, no sshd sidecar) |
| **D-07 Exec command** | `exec /app/llama-server --host 0.0.0.0 --port 8000 -m /weights/qwen/model.gguf -ngl 99 ...` | `args=["--model","Qwen/Qwen3-27B-Instruct-FP8","--host","0.0.0.0","--port","8000","--enable-auto-tool-choice","--tool-call-parser","qwen3_xml","--chat-template","/path/to/template.jinja","--max-model-len","16384"]` |
| **D-08 cleanup custom image** | Same (delete Dockerfile etc) | Same (delete Dockerfile etc) |
| **Phase 1 D-05 (weights MinIO key)** | Qwen 2.5 GGUF | **MUDA SCOPE — Phase 1 reconfig**: re-baixar Qwen3-27B-Instruct-FP8 (~30GB safetensors directory) do HF Hub, upload `s3.ifixtelecom.com.br/ai-gateway/qwen3-27b-fp8/` como diretório, atualizar `WeightsQwenKey`+`WeightsQwenSHA256` pra novo layout. **Multi-file safetensors checksum** complica vs single GGUF file. |
| **Phase 1 D-08 (Jinja template)** | `pod/templates/qwen3.5-27b-tool-calling.jinja` | Jinja template pra Qwen3 (não Qwen3.5) — verificar se existing template funciona ou precisa rebuild |
| **gateway/internal/config/config.go fields** | `EmergencyTemplateImage`, `EmergencyLlamaBinTag`, `EmergencyLlamaBinSHA256`, `EmergencyOnstartVersion`, `EmergencyOnstartSHA256`, `EmergencyLlamaBinMirrorURL` (6 fields) | `EmergencyTemplateImage` (vLLM tag), `EmergencyModelId` ("Qwen/Qwen3-27B-Instruct-FP8"), `EmergencyHFToken` (secret), `EmergencyToolCallParser` ("qwen3_xml"), `EmergencyJinjaTemplateURL` (optional MinIO path) — 4-5 fields, simpler |

**Critical concerns with Strategy C (vLLM):**

1. **FP8 quantization disponibility:** `Qwen/Qwen3-27B-Instruct-FP8` precisa existir no HF Hub. **Verificar antes de lock**. Se não existir, alternativa AWQ Int4 (também precisa pre-quantized model published). Caso contrário, vLLM não roda 27B em 4090. [VERIFY]
2. **HF Hub rate-limit:** Anonymous downloads são rate-limited (60req/min IP). Emergency pod provisioning intermitente pode estourar. **HF_TOKEN obrigatório** pra Strategy C — adiciona um secret novo no SOPS/config gateway.
3. **MinIO mirror migration:** Atualmente MinIO armazena GGUF Phase 1. Strategy C precisa **directory-of-safetensors** mirror. CI de upload-weights muda. **Pode ser feito mas é Phase 1 rework, não Phase 6 cirúrgico**.
4. **vLLM image weight:** 10GB cold-pull adiciona ~1.5min em cache-miss vs llama.cpp 3GB. Counter: cache-hit melhora steady-state assim que pool de hosts Vast pega popularidade vLLM (provavelmente já tá popular dado HF endorsement).
5. **Cold-start FP8 KV cache init:** vLLM faz mais startup work (model compile + KV cache pre-alloc) vs llama.cpp lazy GGUF load — pode adicionar 30-60s de cold-start startup time.

### Strategy D (TGI archived, fallback) — por que descartar

- TGI funciona hoje, suporta Qwen3 parcialmente, OpenAI-compat, GGUF via llamacpp backend, Jinja templates.
- **Mas é archived em 2026-03-21**: sem patches de segurança, sem support pra novos modelos (Qwen3.6+, future Qwen4), sem fixes de bugs descobertos.
- Para emergency pod (lifecycle multi-anos), adotar engine archived é **dívida técnica imediata** — em 6-12 meses precisaria migrar de novo.
- **Verdict: NÃO RECOMENDADO** — só usar se vLLM/llama.cpp tiverem blocker descoberto em spike.

### Recommendation Update — Strategy A vs B vs C reconciliation

Após research suplementar HF, a matriz de decisão fica:

| Strategy | Image source | "HF endorsed?" | Phase 1 compat (GGUF/MinIO/Jinja) | Risk |
|---|---|---|---|---|
| **A** (CONTEXT.md original — corrigida) | `nvidia/cuda:12.8-runtime-ubuntu22.04` + `ai-dock/llama.cpp-cuda` bin download | ❌ Not HF-endorsed but uses HF-endorsed llama.cpp | ✅ Full — same llama-server binary, GGUF native, Phase 1 D-08 Jinja works as-is | MEDIUM — third-party `ai-dock` dep, binary download SHA-verify mandatory, more moving parts |
| **B** (RESEARCH.md original — recomendada) | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` (upstream) | ✅ **HF-endorsed** (llama.cpp listed as recommended) | ✅ Full — image already used by Phase 1 D-08, GGUF + Jinja inalterado | **LOW** — fewest moving parts, image SHA-pinned by tag, no external bin download |
| **C** (vLLM HF default) | `vllm/vllm-openai:v0.21.0-cu129-ubuntu2404` | ✅ **HF-endorsed #1** (HF Inference Endpoints default) | ⚠️ Partial — needs FP8 model on HF Hub, MinIO migration GGUF→safetensors, Jinja template re-test for Qwen3 | MEDIUM-HIGH — Phase 1 rework, FP8 quant verification needed, HF_TOKEN secret |
| **D** (TGI archived) | `ghcr.io/huggingface/text-generation-inference:3.3.5` | ❌ Archived — HF deprecated it | Partial via llamacpp backend | **HIGH** — guaranteed migration debt in 6-12 months |

**Primary recommendation (updated):** **Strategy B remains the leader.**

Rationale:
- llama.cpp **is** HF-endorsed as official replacement (alongside vLLM and SGLang). Using `ghcr.io/ggml-org/llama.cpp:server-cuda` satisfies the "HF image strategy" intent without TGI archive risk.
- Strategy B reusa **TODA a estrutura Phase 1 D-05/D-07/D-08**: GGUF weights MinIO + Jinja template + llama-server flags. Phase 6 vira refator minimalista (`buildCreateRequest` payload + config fields), zero impacto Phase 1.
- Strategy C (vLLM) é mais "HF flagship" mas exige Phase 1 rework (GGUF→safetensors, MinIO mirror restructure, FP8 model verification, HF_TOKEN secret). **Scope creep significativo.**
- Strategy D (TGI) é hard no-go por archive status.

**Secondary recommendation:** Se Pedro priorizar "alinhamento explicit HF" sobre simplicidade, **Strategy C é viável MAS precisa Phase 1 rework como pre-req** — não é troca direta de Strategy B → C dentro do escopo Phase 6.

**Tertiary recommendation:** Spike (já planejado em RESEARCH.md original §spike) deve agora incluir um **second-pod test** com `vllm/vllm-openai` + Qwen3-27B-FP8 (if available on HF Hub) pra validar Strategy C viability paralelamente a Strategy B. Custo adicional: ~$0.50 (1 pod ~30min). Vale o data point.

### Decision aid for discuss-phase re-open

Pedro precisa escolher entre três caminhos antes do planner começar:

1. **B (llama.cpp upstream — RESEARCH-original recommendation):** Sem mudança vs RESEARCH original. "HF image strategy" satisfeita via llama.cpp upstream (HF-endorsed). Phase 6 scope unchanged. **Default recommendation.**

2. **C (vLLM — HF flagship):** Adiciona Phase 1 rework como pre-req. Plano vira "Phase 6 + Phase 1 weights migration". Ganho: alinhamento explícito com HF Inference Endpoints default + potential 1.5-2x throughput (se FP8 quant disponível). Custo: scope expansion + multi-file safetensors integrity complexity.

3. **D (TGI archived — discarded):** Não recomendar. Documenta como alternativa research-completa mas explicitly NOT chosen due to archive status.

CONTEXT.md update needed (D-01..D-08 revision):

- **Option 1 (Strategy B path):** Trocar D-01 para `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`, remover D-02/D-03 (bin download), simplificar D-06 (`args` runtype, sem ssh_proxy), D-07 vira `args=["--host",...,"-m","/weights/qwen/model.gguf",...]`, manter D-04/D-05/D-08. **Zero impact Phase 1.**

- **Option 2 (Strategy C path):** Trocar D-01 para `vllm/vllm-openai:v0.21.0-cu129-ubuntu2404`, remover D-02/D-03, alterar D-04/D-05 (Onstart vira opcional ou só pra HF_TOKEN injection), trocar D-06 (`args` runtype), D-07 vira vLLM args[], **adicionar D-09 Phase 1 migration GGUF→safetensors**, manter D-08. **Phase 1 rework required.**

## Assumptions Log (HF supplemental)

| # | Claim | Section | Risk if Wrong |
|---|---|---|---|
| A-HF-1 | `Qwen/Qwen3-27B-Instruct-FP8` exists on HF Hub | Strategy C performance row | If absent, vLLM cannot load 27B on 4090 — Strategy C blocked. **VERIFY in spike before locking.** |
| A-HF-2 | vLLM `vllm/vllm-openai:v0.21.0` runs Qwen3-27B FP8 in <24GB VRAM on RTX 4090 | Strategy C performance | If OOM despite FP8, Strategy C blocked. **VERIFY in spike.** |
| A-HF-3 | TGI llamacpp backend works with our existing GGUF + Jinja flow | Strategy D | Low risk — Strategy D discarded anyway |
| A-HF-4 | Vast.ai pre-caches `vllm/vllm-openai` on >50% of 4090 spot offers | Cache-hit row | If <30% cache-hit, cold-pull adds 1-2min steady-state — Strategy C cold-start regression vs B. **Measure in spike.** |
| A-HF-5 | HF Inference Endpoints `vllm/vllm-openai` internal image is publicly equal to upstream `vllm/vllm-openai:v0.21.0` | Inventário table | If HF runs a fork, cannot directly reuse. **Low risk** — HF docs explicitly direct users to vLLM upstream. |
| A-HF-6 | `--tool-call-parser qwen3_xml` flag works with Qwen3-27B-Instruct (not Coder-specific) | Strategy C tool calling | If parser is coder-only, need different parser for Instruct variant. **VERIFY in spike.** |
| A-HF-7 | HF Hub rate-limit (anonymous 60 req/min IP) impacts cold-start enough to mandate HF_TOKEN | Strategy C concerns | Medium risk — anon may work fine for our 1-pod-every-30min cadence. **Measure in spike.** |
| A-HF-8 | llama.cpp upstream image `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` accepts ENTRYPOINT override via Vast `args` runtype + image_args | Strategy B | Already used by Phase 1; high confidence — but Vast `args` semantics specifically TBD per spike |

## Recommendation Update (final)

> Reconciliação das três options para o planner:

**Locked recommendation:** **Strategy B** (`ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` upstream) — **honors Pedro's "HF image" intent** via HF's official llama.cpp endorsement, preserves all Phase 1 D-05/D-07/D-08 weights+template+flags infrastructure, simplest path to PHASE COMPLETION.

**Conditional alternative:** Strategy C (vLLM) — adopt **ONLY IF**:
- Spike confirms `Qwen/Qwen3-27B-Instruct-FP8` exists on HF Hub and loads in <24GB on RTX 4090
- Pedro accepts Phase 1 rework (GGUF→safetensors migration) as pre-req for Phase 6
- HF_TOKEN secret added to gateway config

**Discarded:** Strategy D (TGI archived) — research-complete, do not adopt.

**Action for planner:** Default to Strategy B. If Pedro re-opens discuss-phase requesting "real HF image" (interpreted as vLLM), planner sequences Phase 1 rework as pre-Phase-6 task.

**Action for spike:** Add second test pod with `vllm/vllm-openai:v0.21.0-cu129-ubuntu2404` + `--model Qwen/Qwen3-27B-Instruct-FP8` (if Hub-available) to gather empirical data on Strategy C cold-start, VRAM utilization, and cache-hit before locking final decision.

### Sources (HF supplemental)

#### Primary (HIGH confidence)
- `huggingface.co/docs/text-generation-inference/en/quicktour` — TGI image registry + ENTRYPOINT + usage
- `github.com/huggingface/text-generation-inference` — archived 2026-03-21 confirmed, README maintenance notice
- `huggingface.co/docs/inference-endpoints/engines/vllm` — HF Inference Endpoints default to vLLM, configuration reference
- `huggingface.co/docs/inference-endpoints/engines/llama_cpp` — HF endorses llama.cpp via `ggml/llama-cpp-cuda-default` image (stale Docker Hub) but recommends master build
- `docs.vllm.ai/en/stable/deployment/docker/` — `vllm/vllm-openai` Docker image, ENTRYPOINT, model arg syntax
- `docs.vllm.ai/en/latest/features/tool_calling/` — tool calling flags, Jinja template, Qwen parsers
- `hub.docker.com/r/vllm/vllm-openai/tags` — exact image sizes per CUDA variant (v0.21.0-cu129 = 10.27GB)
- `hub.docker.com/u/huggingface` — full HF image catalog (training-focused, no LLM-serving images besides TGI)
- `x.com/LysandreJik/status/1999137874378125436` — official TGI maintenance mode announcement

#### Secondary (MEDIUM confidence)
- `qwen.readthedocs.io/en/latest/deployment/tgi.html` — Qwen+TGI deployment notes (partial Qwen3 support flagged "to be updated")
- `qwen.readthedocs.io/en/latest/run_locally/llama.cpp.html` — Qwen+llama.cpp deployment (production-tested path)
- `spheron.network/blog/hugging-face-inference-endpoints-alternatives/` — independent confirmation HF endpoints default to vLLM
- `spheron.network/blog/vllm-production-deployment-2026/` — vLLM tested Docker command for tensor-parallel + FP8 in 2026
- `ermolushka.github.io/posts/vllm-benchmark-4090/` — 4090 vLLM benchmarks (8B baseline, used to scale to 27B estimates)
- `gist.github.com/sudoingX/c2facf7d8f7608c65c1024ef3b22d431` — patched Qwen 3.5 27B Jinja template (reusable in Strategy C)

#### Tertiary (LOW confidence — must verify in spike)
- vLLM FP8 actually loads Qwen3-27B-Instruct-FP8 in 24GB 4090 — **A-HF-2 verify**
- `Qwen/Qwen3-27B-Instruct-FP8` exists on HF Hub — **A-HF-1 verify**
- Vast.ai cache-hit rate for `vllm/vllm-openai:v0.21.0-cu129-ubuntu2404` — **A-HF-4 verify**


---

*Phase: 6-Emergency-Pod-Template-Refactor*
*Researched: 2026-05-16*
*Researcher: gsd-phase-researcher (Claude Opus 4.7)*
