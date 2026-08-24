---
phase: quick-260824-s2t-kv-q8-0-ctx-32k-por-slot-no-pod-primario
plan: 01
subsystem: infra
tags: [llama.cpp, llama-server, kv-cache, q8_0, flash-attention, gpu, vast.ai, supervisord, docker-compose, go]

# Dependency graph
requires:
  - phase: quick-260821 (llama ctx per-slot 32k)
    provides: "descoberta de que --ctx-size é dividido por -np e de que os 4 pontos de args precisam ficar em sync (supervisord.conf é o runtime real)"
provides:
  - "Contexto por slot do pod primário dobrado: 16384 → 32768 tokens, na mesma GPU (RTX 3090 24 GB)"
  - "KV cache quantizado q8_0 + -fa on nos 4 pontos de args do llama-server, idênticos entre si"
  - "ChatContextCap = 32768 no guard RES-07 do gateway, alinhado ao novo teto por slot"
  - "Runbooks e comentário de flags travadas do compose sem valor stale"
affects: [deploy do pod primário (rebuild de imagem + stack Portainer 38), tokenizer fail-open (HANDOFF), análise de chamadas longas via n8n]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "KV cache quantizado (q8_0) como alavanca de contexto sem troca de GPU"
    - "-fa on como pré-requisito duro de --cache-type-* quantizado"

key-files:
  created: []
  modified:
    - pod/primary/supervisord.conf
    - pod/docker-compose.yml
    - gateway/internal/primary/onstart.go
    - gateway/internal/emerg/lifecycle.go
    - gateway/internal/proxy/tokencount.go
    - gateway/internal/proxy/errors.go
    - gateway/docs/RUNBOOK-PRIMARY-POD.md
    - gateway/docs/RUNBOOK-EMERGENCY-POD.md
    - pod/README.md

key-decisions:
  - "Quantizar o KV cache para q8_0 em vez de trocar de GPU: +278 MiB de VRAM (1,3% da 3090) compra 2× de contexto por slot"
  - "-fa on tratado como parte indivisível do conjunto de flags — documentado nos 4 pontos, porque a ausência dele mata o pod no boot (não degrada, aborta)"
  - "--ctx-size subiu para 65536 mantendo -np 2: 2 slots de 32768 em vez de 1 slot de 65536, preservando concorrência"
  - "Commit único cobrindo as 2 tasks, conforme instrução explícita do plano (Task 2 passo 8 + success_criteria), em vez de um commit por task"

patterns-established:
  - "Gate de paridade por token nos 4 pontos de args: casar string completa ('-fa on' / '\"-fa\", \"on\"'), nunca o token nu '-fa' (falso positivo com models--Systran--faster-whisper-* no supervisord.conf)"
  - "ChatContextCap sempre segue o contexto POR SLOT (--ctx-size / -np), nunca o total"

requirements-completed: [KV-Q8-32K]

# Metrics
duration: 12min
completed: 2026-08-24
---

# Quick 260824-s2t: KV cache q8_0 + contexto 32k por slot Summary

**KV cache quantizado q8_0 + `-fa on` + `--ctx-size 65536` nos 4 pontos de args do llama-server, dobrando o contexto por slot do pod primário de 16384 para 32768 tokens na mesma RTX 3090, com `ChatContextCap` do gateway subido de 16384 para 32768 para acompanhar.**

## Performance

- **Duration:** ~12 min
- **Started:** 2026-08-24 (worktree agent-a2ab98486b3b38523)
- **Completed:** 2026-08-24
- **Tasks:** 2/2
- **Files modified:** 9

## Accomplishments

- **Contexto por slot 2×** — `--ctx-size` 32768 → 65536 com `-np 2` mantido, viabilizado por `--cache-type-k q8_0 --cache-type-v q8_0 -fa on`. Requests da classe ~21k tokens (que hoje devolvem 400 `exceed_context_size_error`) passam a caber no slot.
- **Paridade dos 4 pontos garantida por gate** — `pod/primary/supervisord.conf` (o runtime REAL), `pod/docker-compose.yml`, `primaryLlamaArgsDefault` e `emergencyLlamaArgsDefault` carregam o mesmo conjunto de 4 flags novas. Nenhum ponto ficou com `--cache-type-*` sem `-fa on` (mitigação T-q8-01: sem flash-attn o llama-server aborta no boot).
- **Guard RES-07 alinhado** — `ChatContextCap = 32768` (mitigação T-q8-02: o cap ficar em 16384 rejeitaria requests que agora cabem). Nenhum consumidor precisou mudar — todos usam o símbolo.
- **Docs sem valor stale** — 2 runbooks, `pod/README.md` e o comentário de "Flags locked" do compose atualizados; zero ocorrências de `ctx-size 32768` fora de `.planning/`.

## Task Commits

As duas tasks foram entregues num commit único, conforme instrução explícita do plano (Task 2, passo 8; `success_criteria`: "Um unico commit convencional"):

1. **Task 1 + Task 2** — `dcb6b0e` `fix(pod): KV cache q8_0 + --ctx-size 65536 → 32768 tokens por slot`

**Plan metadata:** commitado pelo orquestrador (SUMMARY.md / STATE.md fora do escopo deste executor).

## Files Created/Modified

- `pod/primary/supervisord.conf` — comando do `[program:llama]`: `--ctx-size 65536 --cache-type-k q8_0 --cache-type-v q8_0 -fa on`; nota de 5 linhas acima do bloco com custo medido e o motivo de `-fa on` ser obrigatório. **Este é o runtime real do pod.**
- `pod/docker-compose.yml` — bloco `command: >` do service `llama` com as mesmas flags; comentário "Flags locked" (linha 36) reescrito com o cálculo `65536 total / -np 2 = 32768/slot`.
- `gateway/internal/primary/onstart.go` — `primaryLlamaArgsDefault` espelhando o supervisord.conf; doc comment com o trade-off medido e o aviso do flash-attn. `--chat-template-file` continua ausente (invariante B1 embedded LOCKED preservada).
- `gateway/internal/emerg/lifecycle.go` — `emergencyLlamaArgsDefault` com o mesmo conjunto; `--chat-template-file` do fim preservado; doc comment não cita mais "13-flag" (a contagem ficaria errada).
- `gateway/internal/proxy/tokencount.go` — `ChatContextCap = 16384` → `32768`; header do package "chat (16k)" → "chat (32k)"; doc comment do const reescrito mantendo a nota histórica OPERACOES-26306 (que explica por que o cap segue o per-slot). `EmbedContextCap` intocado (8192).
- `gateway/internal/proxy/errors.go` — comentário de `ErrContextLengthExceeded`: `> 16384` → `> 32768`. Sem mudança de comportamento nem de assinatura.
- `gateway/docs/RUNBOOK-PRIMARY-POD.md` — linha do diagrama ASCII do `[program:llama]` (quebrada em linhas extras para manter alinhamento).
- `gateway/docs/RUNBOOK-EMERGENCY-POD.md` — comando `exec /app/llama-server ...` do passo 4 do onstart.
- `pod/README.md` — linha de troubleshooting `vram_peak_gb > 21` cita `--ctx-size 65536`; a orientação (reduzir ctx sob pressão de VRAM, fallback D-09) permanece.

## Decisions Made

- **Commit único em vez de commit por task.** O default do executor é um commit atômico por task, mas o plano instrui explicitamente (Task 2 passo 8 + `success_criteria` + gate `git log -1 | grep 65536`) a entregar as duas tasks num commit só. A instrução específica do plano prevaleceu.
- **Nenhum teste foi editado.** O plano já havia confirmado por grep que nenhum teste hardcoda `ChatContextCap` nem o valor de `--ctx-size`; as suites de `proxy`, `primary` e `emerg` passaram sem alteração, incluindo os budgets de tamanho do onstart (`< 2500` emerg, `< 14000` primary) — as flags novas somam ~48 chars.
- **`audio_duration_test.go:63-64` não foi tocado** — o literal `16384` ali é contagem de BYTES de um fixture mp3, não contexto.

## Deviations from Plan

Nenhum desvio de conteúdo. Uma nota de processo:

**1. [Processo] Marcador ClickUp espelhado no worktree**
- **Found during:** Task 1 (primeira edição de arquivo)
- **Issue:** O hook `clickup-link-enforce.sh` bloqueou edições porque `.planning/clickup-active-task.json` não existia no worktree. O arquivo é gitignored (`.gitignore:68`), então não veio no checkout.
- **Fix:** Copiado o marcador já existente do repo principal (`{"skip": true}` — GSD puro, rastreamento de demanda já dispensado neste repo). Nenhuma política nova foi criada; só espelhado o estado vigente.
- **Files modified:** `.planning/clickup-active-task.json` (gitignored, NÃO commitado)
- **Verification:** `git status --short` limpo após o commit.

---

**Total deviations:** 0 de conteúdo, 1 de processo (marcador gitignored espelhado).
**Impact on plan:** Nenhum. Zero scope creep.

## Issues Encountered

Nenhum. Build, gofmt e as 4 suites de teste passaram na primeira tentativa.

## Verification Results

| Gate | Resultado |
|---|---|
| `cd gateway && gofmt -l .` | sem saída |
| `cd gateway && go build ./...` | sem saída (verde) |
| `go test ./internal/proxy/... ./internal/primary/... ./internal/emerg/... ./internal/config/...` | 5 pacotes `ok` (proxy 13,8s · primary 22,8s · emerg 5,4s · emerg/vast 0,1s · config 0,02s) |
| `grep -c 'ChatContextCap = 32768' internal/proxy/tokencount.go` | 1 |
| Tokens Go (`"--ctx-size", "65536"`, `"--cache-type-k", "q8_0"`, `"--cache-type-v", "q8_0"`, `"-fa", "on"`) | 2 ocorrências cada (onstart.go + lifecycle.go), zero em linha de comentário |
| Tokens pod (`--ctx-size 65536`, `--cache-type-k q8_0`, `--cache-type-v q8_0`, `-fa on`) | presentes em linha não-comentada de `supervisord.conf:50` e `docker-compose.yml:49-52` |
| `grep -rn -- '"--ctx-size", "32768"' gateway/` | zero hits |
| `grep -rn 'ctx-size 32768' pod/ gateway/` | zero hits |
| `grep -rn 'ctx-size 32768' . --exclude-dir=.planning --exclude-dir=.git` | zero hits |
| Deleções acidentais no commit (`git diff --diff-filter=D HEAD~1 HEAD`) | nenhuma |

## User Setup Required

Nenhuma configuração de serviço externo.

## Next Phase Readiness

**Repo-only — a mudança AINDA NÃO existe no pod rodando.** Para ela pegar:

1. Rebuild da imagem `ifix-ai-pod` (o `supervisord.conf` é bakeado em build time) e push.
2. Redeploy do pod primário (stack Portainer 38) com a imagem nova.
3. Rebuild + redeploy do gateway para o `ChatContextCap = 32768` valer.
4. Na primeira subida, conferir no `/var/log/llama.log` do pod que **não** aparece `quantized V cache requires flash_attn to be enabled` e que o `n_ctx_per_seq` logado é **32768** — é a prova de que a mudança pegou de verdade (na última mexida em ctx, um dos 4 pontos ficou de fora e o fix não chegou ao pod).

**Fora de escopo, ainda aberto:** o `ChatContextCap` continua **inerte** em produção — o guard tokeniza contra uma URL morta e falha-aberto (`.planning/debug/HANDOFF-tokenizer-fail-open-pod-ctx-16k.md`). Subir a constante é correto e necessário, mas sozinho não faz o guard voltar a funcionar. O ganho real desta task (requests de ~21k passarem a ser servidos em vez de 400ar no slot) vem do lado do pod, não do guard.

---
*Quick task: 260824-s2t-kv-q8-0-ctx-32k-por-slot-no-pod-primario*
*Completed: 2026-08-24*

## Self-Check: PASSED

- `.planning/quick/260824-s2t-kv-q8-0-ctx-32k-por-slot-no-pod-primario/260824-s2t-SUMMARY.md` — FOUND
- Commit `dcb6b0e` — FOUND (`git log --oneline -1 dcb6b0e`)
- Todos os 9 arquivos modificados listados estão no commit (`git show --stat dcb6b0e`: 9 files changed, 59 insertions(+), 19 deletions(-))
