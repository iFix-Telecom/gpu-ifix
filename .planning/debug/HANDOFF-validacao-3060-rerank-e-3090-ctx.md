# HANDOFF — validação em duas frentes (para novas sessões)

**Origem:** sessão gpu-ifix de 2026-08-24/25 (quicks 260824-s2t + 260824-ucv, ambos DEPLOYADOS em prod).
Estado consolidado no STATE.md (linhas 260824-s2t e 260824-ucv) e nas memórias
`llama-ctx-per-slot-32k`, `glm47-vs-qwen3-bench-3090`, `res08-overcontext-carveout`.

---

## Frente 1 — validar 3060 + rerank (sessão do rerank)

**Contexto crítico:** o push do rerank (`eec80fc`/`b9e8feb`) deixou o CI VERMELHO (migration 0035 sem o
bump ritual dos `Down(N)` em `migration_0026_test.go`/`migration_0029_test.go`) → **a imagem do gateway
com rerank NUNCA foi buildada pelo push original**. O código só chegou em prod de carona no deploy
`develop-06e209a` (2026-08-24 ~23h), que também aplicou a **migration 0035** (`gatewayctl migrate up`,
schema v35). Bump dos testes: commit `06e209a`.

Checklist:
1. `curl -s -H "X-Admin-Key: $K" https://ai-gateway.converse-ai.app/admin/... ` — conferir upstreams
   `rerank-gpu`/`rerank-cpu` existem (seed da 0035) e envs `UPSTREAM_RERANK_URL` / `UPSTREAM_RERANK_FALLBACK_URL`
   estão setadas no stack 38 (**suspeita: NÃO estão** — a 0035 só cria as rows; env nunca foi adicionada ao
   compose/Env do stack. Sem env → upstream sem URL → dispatch falha).
2. `POST /v1/rerank` via gateway com tenant real → 200 do tier-0 (pod 3060 unificado, Infinity bge-reranker-v2-m3).
3. Derrubar/parar o Infinity do pod → mesma chamada cai no tier-1 `rerank-cpu` (worker-vm `ai-gateway-rerank`).
4. Billing/metrics do role novo aparecem (usage_counters/billing_events aceitam role rerank?).
5. Decidir destino do pod ANTIGO `stt-tts-3060-auto` (id 48644310, $0,040/h) — os DOIS 3060 estão rodando
   em paralelo agora (migração pro unificado `stt-tts-rerank-unified` id 48611358, $0,055/h, incompleta?).
   Dois pods = custo dobrado até cortar.

## Frente 2 — validar 3090 (contexto 32k + fixes A/B) sob tráfego real

Já validado sinteticamente E2E (5/5, ver STATE 260824-ucv). Falta observar TRÁFEGO REAL:
1. Amanhã ≥8h (pod up pelo schedule): `docker logs` do gateway — **zero** `exceed_context_size_error`
   nos turnos do Maestro (eram 63/2h); requests 16-32k servidos pelo pod.
2. `gateway_over_context_cascaded_total` — taxa de crescimento = gasto externo; se crescer rápido,
   é o Maestro estourando 32k e virando custo OpenRouter (alertar Pedro).
3. Latência: KV q8_0 custou -6% de geração; conferir P95 no dashboard não degradou visivelmente.
4. Qualidade: KV q8_0 é quantização — se o Maestro alucinar mais, suspeitar disso primeiro (rollback:
   gateway `develop-b7daed5`, pod `@sha256:4eaa0fa4`, mas PERDE os fixes — preferir só reverter flags do pod).
5. `tokencount /tokenize request failed` deve permanecer ZERO com pod up.

## Pendências conhecidas (nenhuma bloqueia)
- **Bug audit 128KiB** (`audit/middleware.go:57-59`): request body >128KiB de tenant normal é truncado
  e morre com 502 `ContentLength mismatch` DESDE SEMPRE. Maestro ~87KB se aproximando. Fix pequeno:
  capturar capped + encaminhar stream completo (MultiReader). Candidato a próximo quick.
- **Fix C do handoff antigo** (alerta de guard inerte) — aberto.
- Guard n8n "Cabe no Pod?" ≤14000 est_tokens — desatualizado (cap real agora 32k/slot); afrouxar dobra o
  que o n8n manda pro pod grátis.

## Embed na 3060? (análise 2026-08-25 00:15)
**Medido:** 3060 unificada com **9,6 GB VRAM livres** (2,67 usados / 12,3). bge-m3 = 1,2 GiB de arquivo
(R2), ~2,5 GB servido fp16 → **cabe com folga**. O pod já roda Infinity (rerank bge-reranker-v2-m3) e o
Infinity serve múltiplos modelos no mesmo processo — embed entraria barato.
**MAS dois poréns:** (1) hoje o embed tier-0 é `embed:7997` no worker-vm (CPU, estável, sem spot-churn);
mover pra pod spot troca estabilidade por velocidade num role que raramente é gargalo. (2) **dimensão:**
bge-m3 = 1024 dims; o tier-1 `openai-embed` (text-embedding-3-small) = 1536 — fallback JÁ é incoerente
pra vetores armazenados (pendência Phase 114). Colocar o tier-0 num pod que migra AUMENTA a frequência
de fallback incoerente. Recomendação: só mover depois de resolver a política de fallback do embed
(pin de dimensão ou 503 explícito).
