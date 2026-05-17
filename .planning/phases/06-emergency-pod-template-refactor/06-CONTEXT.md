# Phase 6: Emergency-Pod Template Refactor (Vast.ai + llama-server binary) - Context

**Gathered:** 2026-05-16
**Status:** Ready for planning

<domain>
## Phase Boundary

Trocar bootstrap-path do emergency pod usado pelo reconciler `gateway/internal/emerg`:

- **Antes:** custom Docker image `ghcr.io/ifixtelecom/ifix-ai-pod:latest-dev` (~6GB, baked com llama.cpp + Jinja template + emerg-bootstrap.sh + sshd) + `runtype=ssh` (Vast injeta sshd como PID 1, **IGNORA CMD da image** — bug crítico STATE.md:85 que travou lifecycles 29-33 em health_timeout 1800s).
- **Depois:** template público `nvidia/cuda:12.4-runtime-ubuntu22.04` (cache-hit em ~todo host Vast 4090) + `runtype=ssh_proxy` (sshd sidecar Vast, container respeita CMD/image_args) + Onstart Vast escreve `/onstart.sh` (hospedado MinIO + sha256 verificado) + `image_args=["bash", "/onstart.sh"]` (script baixa llama-server binário do GitHub release pinned com MinIO fallback, baixa Qwen weights MinIO, exec llama-server como PID 1).

**Ganhos esperados:**
1. Resolve bug runtype=ssh CMD-ignore — lifecycles 34+ podem chegar emergency_active end-to-end.
2. Cold-start ~7-12min → ~5min (template Vast cache-hit ~30s vs custom image pull 3-5min cold).
3. Iteração dev: edita onstart.sh + upload MinIO + bump versão config gateway = ~2min vs rebuild Docker image + push GHCR + Vast pull = ~10-15min.
4. Crash de llama-server detectável (PID 1 morre → container morre → Vast restart/marca falha → reconciler detecta), vs path atual onde crash não derruba sshd PID 1.

**Não é desta phase:** novos cenários PRV-XX, mudança no FSM/reconciler logic, mudança em audit/budget/lifecycle DB schema. Phase 6 é refactor cirúrgico de `provisionLifecycle` + remoção de `pod/Dockerfile` + `.github/workflows/build-pod.yml`. Phase 6.5 reqs (PRV-01..10) continuam servidos pelo mesmo subsistema, source diferente.

</domain>

<decisions>
## Implementation Decisions

### Template Vast.ai
- **D-01:** Template = `nvidia/cuda:12.4-runtime-ubuntu22.04` (Docker Hub oficial NVIDIA). CUDA 12.4 runtime (~600MB) — não devel. Cache-hit em hosts 4090 esperado alto. sm_89 compute capability coberto pela toolkit 12.4. Sem ENTRYPOINT customizado (template puro), sshd injetado via runtype ssh_proxy + Onstart Vast escreve script.

### llama-server binário (source + integrity + fallback)
- **D-02:** Download path = `github.com/ggml-org/llama.cpp/releases/download/<TAG>/llama-<TAG>-bin-ubuntu-x64-cuda.zip` pinned por TAG (e.g., `b9128` — mesma versão que `pod/Dockerfile:23` usa em `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`, mantém paridade Phase 1 D-08). sha256 verificado antes de unzip + exec.
- **D-03:** Fallback MinIO mirror = `s3.ifixtelecom.com.br/ai-gateway/llama-bins/<TAG>/llama-<TAG>-bin-ubuntu-x64-cuda.zip`. Onstart tenta GitHub primário (5s timeout), fallback MinIO se falhar (rate-limit, outage). CI (build-llama-bin-mirror.yml novo) faz upload pra MinIO em cada bump de TAG na config gateway.

### Onstart script storage
- **D-04:** Script `pod/scripts/emerg-onstart.sh` hospedado em MinIO `s3.ifixtelecom.com.br/ai-gateway/emerg-onstart/<VERSION>.sh` (versionado por filename, immutable). CI (extensão do upload-weights.sh OU novo workflow) faz upload em cada commit que toca o arquivo. Config gateway env `EMERG_ONSTART_VERSION=<VERSION>` + `EMERG_ONSTART_SHA256=<HEX>` determina qual versão é injetada no Onstart Vast.
- **D-05:** Onstart Vast inline (curto, ~10 linhas) faz: `curl -fsSL "${MINIO_ENDPOINT}/${MINIO_BUCKET}/emerg-onstart/${VERSION}.sh" -o /onstart.sh && echo "${SHA256}  /onstart.sh" | sha256sum -c && chmod +x /onstart.sh`. Mesmo padrão sha256-check que `pod/onstart.sh:78-90` usa pra download-weights.sh.

### Runtype + exec strategy
- **D-06:** `Runtype: "ssh_proxy"` + `Onstart: "<inline 10-line curl+sha256 + writes /onstart.sh>"` + `ImageArgs: ["bash", "/onstart.sh"]`. Vast garante sshd sidecar (operator pode `vast ssh <id>` pra debug), llama-server vira PID 1 do container principal (crash detect automático).
- **D-07:** Script `/onstart.sh` (no MinIO, baixado e exec via Vast) faz: (1) curl llama-bin GitHub OR MinIO fallback + sha256 verify + unzip pra /app/llama-server; (2) curl Qwen weights MinIO + sha256 verify (idêntico ao emerg-bootstrap.sh:33-62 atual); (3) `exec /app/llama-server --host 0.0.0.0 --port 8000 -m /weights/qwen/model.gguf -ngl 99 -np 2 --ctx-size 16384 --jinja --chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja` (flags idênticos a emerg-bootstrap.sh:64-73); (4) Jinja template é embedded inline OR baixado de MinIO (decide em planning — template é pequeno ~20KB, inline simplifica).

### Cleanup custom image baseline
- **D-08:** Custom image GHCR descontinuada (delete tudo). Phase 6 commit remove: `pod/Dockerfile`, `pod/scripts/emerg-bootstrap.sh`, `.github/workflows/build-pod.yml`, `pod/sshd_config` (se existe), config field `EmergencyPodImageTag` em `gateway/internal/config/config.go:243`, env var `EMERGENCY_POD_IMAGE_TAG` em `.env.portainer.dev`. GHCR cleanup manual operator pode rodar `gh api -X DELETE /repos/ifixtelecom/ifix-ai-pod/packages/container/...` pós-deploy.
- **D-08-risk:** Burnt-bridge. Se template Vast/llama-bin download tiver bug que só aparece em produção, reverter requer code change + recompile + redeploy (~30min). Mitigação: Phase 6 implementa template path em PR separado do delete-custom-image; VERIFICATION live valida 3 lifecycles consecutivos GREEN antes do delete commit.

### Claude's Discretion
- Naming exato do MinIO path/filename (`emerg-onstart/v1.sh` vs `emerg-onstart/2026-05-16-a.sh` vs hash-named).
- Estrutura interna de `/onstart.sh` (funções, error handling, log format) — segue padrão `pod/onstart.sh` Phase 1.
- Health-bridge sidecar (porta 9100 do Phase 1) — NÃO incluir em Phase 6 (`emerg-bootstrap.sh` atual já omite per Phase 6.5 D-C2 — emergency pods são LLM-only). Manter omisso. Reconciler já faz health-poll direto em `:8000/v1/models` (lifecycle.go).
- Vast offer filter `Disk` — atualmente 80GB (lifecycle.go:693). Template `nvidia/cuda:12.4-runtime-ubuntu22.04` é ~600MB (vs custom image 6GB) + llama-bin 100MB + Qwen weights 16GB + tmp = ~17GB. Pode reduzir disk filter pra 40-50GB e abrir mais offers no spot market. Decide em planning.

### Folded Todos
Nenhum todo de outras phases foi dobrado — esta phase nasce do SEED-001 standalone. STATE.md tech debt items #2 (GPU error detect lifecycle.go), #4 (recovery FSM reset), #5 (redis mirror stale boot), #80 (audit data_class enum) NÃO entram em Phase 6 — são bugs ortogonais do reconciler/FSM, ficam em quick-tasks separados pós-Phase 6.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### SEED + decisão arquitetural
- `.planning/seeds/SEED-001-emergency-pod-template-vast-vs-custom-image.md` — origem do refator, trade-offs medidos durante UAT 2026-05-16, breadcrumbs de arquivos a tocar.
- `.planning/STATE.md` §"Open Todos" linha 85 — bug runtype=ssh CMD-ignore (root cause que esta phase resolve).
- `.planning/ROADMAP.md` Phase 6 — goal + success criteria locked.

### Código atual (a refatorar)
- `gateway/internal/emerg/lifecycle.go:670-697` — `buildCreateRequest` atual com `Runtype: "ssh"` + `Onstart: ""` (bug). É o ponto principal de mudança.
- `gateway/internal/emerg/lifecycle.go:670-680` — env vars MinIO/Weights atualmente injetados no `Env` field; serão usados pelo `/onstart.sh` baixado.
- `pod/Dockerfile` — referência do que está baked atualmente (CUDA backend layout `/app/`, Jinja template path, llama.cpp tag pinned `:server-cuda-b9128`). **Delete em Phase 6 (D-08).**
- `pod/scripts/emerg-bootstrap.sh` — script atual baked no Dockerfile CMD. Lógica MinIO download + sha256 + exec llama-server reusada quase 1:1 em `/onstart.sh`. **Delete em Phase 6 (D-08).**
- `.github/workflows/build-pod.yml` — CI build/push GHCR. **Delete em Phase 6 (D-08).**

### Config + env
- `gateway/internal/config/config.go:243` — `EmergencyPodImageTag` field (default `"v1.0"`). **Delete em Phase 6.** Adicionar novos fields: `EmergencyTemplateImage`, `EmergencyLlamaBinTag`, `EmergencyLlamaBinSHA256`, `EmergencyLlamaBinMirrorURL`, `EmergencyOnstartVersion`, `EmergencyOnstartSHA256`.
- `gateway/.env.portainer.dev:34` — `EMERGENCY_POD_IMAGE_TAG=latest-dev`. Substitui pelos novos.

### Phase 6.5 (renomeada de Phase 6, código permanece)
- `.planning/phases/06.5-auto-provisioning-emergency-pod-vast-ai/06.5-CONTEXT.md` — decisões Phase 6.5 sobre LLM-only emergency pod (D-C2), Vast.ai port-visibility quirk (sshd :22 antes do actual=running flip), reconciler FSM 9-state.
- `.planning/phases/06.5-auto-provisioning-emergency-pod-vast-ai/06.5-SPIKE-vast-port-mapping.md` — evidência empírica Vast.ai port quirks (relevante pra runtype=ssh_proxy spike confirmation).
- `.planning/phases/06.5-auto-provisioning-emergency-pod-vast-ai/06.5-RESEARCH.md` — research Vast.ai REST API quirks (search/create/destroy timing, bid race).

### Phase 1 (paridade flags llama-server + sha256 pattern)
- `.planning/phases/01-gpu-pod-image-smoke-test/01-CONTEXT.md` D-01/D-05/D-07/D-08 — image stays small, weights via MinIO+sha256, llama-server flags com Jinja template.
- `pod/onstart.sh:80-92` — padrão de download MinIO + sha256 verify reusado pelo `/onstart.sh` Phase 6.
- `pod/templates/qwen3.5-27b-tool-calling.jinja` + `.sha256` — template tool-calling. Decide em planning: embed inline no `/onstart.sh` MinIO OU hospedar separado em MinIO `emerg-onstart/templates/<sha>.jinja`.

### Vast.ai API docs (research alvo)
- A pesquisar (gsd-phase-researcher): docs oficiais Vast.ai sobre runtype `ssh_proxy` semantics — confirmar respeita `image_args` field + sshd sidecar comportamento. Endpoint `https://vast.ai/api/v0` + console UI source.
- A pesquisar: cache-hit empírico de `nvidia/cuda:12.4-runtime-ubuntu22.04` em hosts Vast 4090 (search offers + filter `cached_images`).

### llama.cpp release docs
- A pesquisar: github.com/ggml-org/llama.cpp/releases — confirmar asset naming exact, qual zip contém `llama-server` standalone bin com CUDA 12.4 support, sha256 disponibilidade na release page.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `gateway/internal/emerg/lifecycle.go:provisionLifecycle` — fluxo search→create→/health já implementado em Phase 6.5. Phase 6 só TROCA o conteúdo de `buildCreateRequest` (linhas 660-697). Resto do reconciler intacto.
- `gateway/internal/vast/client.go:CreateInstance` — wrapper API já tem todos os fields necessários (`Runtype`, `Onstart`, `ImageArgs`, `Env`, `Disk`, `Label`, `TargetState`). Sem mudanças no client layer.
- `pod/scripts/download-weights.sh` (referenciado por `pod/onstart.sh:84`) — padrão MinIO download paralelo + sha256 check. Lógica reusable pra `/onstart.sh` Phase 6 (mas só baixa Qwen, não whisper/bge-m3 — emergency pod é LLM-only).
- `gateway/internal/config/config.go` — pattern de env var loading + validation (godotenv + envconfig). Adicionar 6 novos fields segue padrão existente.
- Sentry breadcrumb pattern em `gateway/internal/emerg/fsm.go` — adicionar breadcrumb pra "emerg_onstart_version=X llama_bin_tag=Y" facilita debug futuro.

### Established Patterns
- **sha256-verify-before-exec** (pod/onstart.sh, emerg-bootstrap.sh, Phase 1 D-05): MANDATORY pra qualquer binário/script baixado em runtime. Phase 6 estende pra llama-bin + onstart.sh.
- **PID 1 = service** (Phase 1 docker-compose pattern): llama-server deve ser PID 1 do container pra crash → restart automático. Phase 6 D-06 (`image_args=["bash", "/onstart.sh"]` + script termina com `exec`) garante isso.
- **Vast.ai port-visibility quirk** (Phase 6.5 emerg-bootstrap.sh:22-31): sshd MUST estar listening :22 ANTES de Vast flipar actual=running. Com `runtype=ssh_proxy`, sshd é responsabilidade do sidecar Vast — NÃO precisa rodar dentro do container principal. Confirma em spike.
- **MinIO como source-of-truth pra artefatos** (Phase 1 weights, Phase 6 llama-bin mirror + onstart script): bucket `ai-gateway` já configurado, mc client baixado on-demand pelo script, mesma credencial reusada.
- **Versão pinned + sha256 em config** (Phase 1 `WeightsQwenKey` + `WeightsQwenSHA256`): Phase 6 segue pattern (`EmergencyLlamaBinTag` + `EmergencyLlamaBinSHA256` + `EmergencyOnstartVersion` + `EmergencyOnstartSHA256`).

### Integration Points
- `gateway/internal/emerg/lifecycle.go:buildCreateRequest` (linhas 660-697) — único call site que precisa mudar lógica.
- `gateway/internal/config/config.go:243` — adiciona 6 novos fields (substituindo o 1 atual).
- `gateway/.env.portainer.dev` — substituir 1 env var por 6.
- Reconciler health-poll path (lifecycle.go) — INALTERADO. Continua chamando `:8000/v1/models` via Vast port-forward, mesma estratégia.
- Integration tests `gateway/internal/integration_test/emerg_*` — alguns tests assumem custom image ou runtype=ssh (verificar Plan 06.5-04..06.5-08). Refatorar pra mock template + image_args + sshd sidecar.

</code_context>

<specifics>
## Specific Ideas

- **Spike PARALELO obrigatório antes do plan-phase começar implementação (alta prioridade):**
  - Provisionar 1 pod Vast manual via UI/CLI com `nvidia/cuda:12.4-runtime-ubuntu22.04` + `runtype=ssh_proxy` + `image_args=["bash", "-c", "sleep 600"]` + `onstart` curto que escreve um echo no `/var/log/onstart.log`.
  - Confirmar: (a) sshd sidecar responde `vast ssh`; (b) container respeita `image_args`; (c) Onstart Vast roda no host antes do container subir e consegue `docker cp` arquivo pro container; (d) cache-hit do template em ≥3 hosts diferentes do mesmo offer pool 4090.
  - Tempo: ~1-2h. Custo: ~R$3 (1 pod ~10min @ $0.30/h).
  - Resultado em `.planning/phases/06-emergency-pod-template-refactor/06-SPIKE-template-runtype.md`.
- **Iteração dev validada em planning:** após D-04/D-05 (script MinIO + version env), próximo pod emergency provisionado usa script atualizado SEM redeploy gateway (config env mudou via Portainer UI). Confirma em planning que esse path funciona steady-state — não só na primeira provisão.
- **Burnt-bridge mitigation (D-08):** plan-phase deve sequenciar plans assim: (1) implementar template path (lifecycle.go + config + onstart.sh + CI upload-onstart.yml) SEM tocar Dockerfile/build-pod.yml; (2) UAT live 3 lifecycles consecutivos GREEN com template path; (3) PR separado deleta `pod/Dockerfile`, `pod/scripts/emerg-bootstrap.sh`, `.github/workflows/build-pod.yml`, config field `EmergencyPodImageTag`. Não fundir (1) + (3) no mesmo PR.
- **Reutilizar emerg-bootstrap.sh lógica MinIO/sha256:** copiar lógica em `/onstart.sh` ao invés de inventar nova. Migra Phase 6.5 D-C2 (LLM-only) pra Phase 6.

</specifics>

<deferred>
## Deferred Ideas

- **Health-bridge sidecar em emergency pod (SEED-001 ponto 6):** Phase 6.5 D-C2 decidiu emergency pod é LLM-only (sem health-bridge :9100). Phase 6 mantém essa decisão. Se Phase 7 (observability) precisar de métricas detalhadas do emergency pod, considerar habilitar health-bridge em phase futura — não nesta.
- **STT/Embed servers em emergency pod:** Phase 6.5 D-C2 omitiu (Phase 3 fallback OpenRouter cobre). Não muda em Phase 6.
- **Multi-arch llama-bin (arm64):** Vast.ai oferece principalmente x86_64; arm64 GPU pods raros. Phase 6 ignora.
- **Llama.cpp updates automáticos (Dependabot/Renovate pra TAG):** desejável mas adia. Phase 6 pin manual via config env. Operator decide quando bumpar TAG.
- **Tech debt itens STATE.md ortogonais ao subsistema:**
  - #80 audit data_class enum (fsm.go:331 → DataClass=""): quick-task separado
  - #81 GPU error detect lifecycle.go (status_msg parsing): quick-task separado
  - #83 leader-recovery FSM reset (recovery.go): quick-task separado
  - #84 redis mirror boot stale (main.go): quick-task separado
  - Nenhum bloqueia Phase 6, mas todos devem ser feitos antes de Phase 6.5 HUMAN-UAT live com cenários reais.
- **Phase 6.5 HUMAN-UAT (06.5-11):** desbloqueado após Phase 6 VERIFICATION GREEN. Sequência: Phase 6 plan → execute → verify → Phase 6.5 06.5-11 live UAT → Phase 6.5 close → Phase 7+ continua.

### Reviewed Todos (not folded)
Nenhum — discussão foi laser-focused no scope SEED-001.

</deferred>

---

*Phase: 6-Emergency-Pod-Template-Refactor*
*Context gathered: 2026-05-16*
