# HANDOFF 2026-10-01 ~19:20 BRT — custo dos pods Vast (retomar em nova sessão)

ClickUp ativo: [pod primário 3090: reduzir custo](https://app.clickup.com/t/86akrnpwc) (86akrnpwc).
Relacionados em "em testes": 86akregky (custo 3060), 86akreh6u (STT→gemini).

## Estado

### Em execução (pode já ter terminado)
- **Quick 261001-qdd** (primário 3090): executor gsd em worktree
  `.claude/worktrees/agent-ae3bfecd27e30132b`, branch `worktree-agent-ae3bfecd27e30132b`,
  base `0a9c9fa`. Escopo (PLAN em `.planning/quick/261001-qdd-primario-3090-custo-real-bid-fallback/`):
  - migration **0039**: pod_config `offer_mode` (default `bid`), `bid_margin` 1.15,
    `max_preemptions_per_day` 2, **`min_reliability` 0.95** (decisão Pedro 2026-10-01, só primário;
    hoje filtro é `reliability >= 0.99` em emerg/vast/types.go:357-368 — emerg NÃO muda);
    primary_lifecycles `is_bid`, `bid_price`.
  - ranking por custo real = GPU (base ou lance) + storage×disk/730 + **download amortizado**
    (inet_down_cost × ~20G / ~8h) — teto aplicado em GPU+disco.
  - bid default com fallback on-demand (sem oferta bid / ≥2 preempções no dia);
    preempção → destroy + close reason `preempted` (sem blocklist, sem no-credit block) → reprovisiona.
  - métrica `gateway_primary_preemptions_total`; MODE em `gatewayctl primary lifecycles`.

### Próximos passos (APROVADOS pelo Pedro: "ok para os próximos passos")
1. Se o worktree existir: conferir commits, copiar SUMMARY, merge `--no-ff` em develop, remover worktree.
2. Verificar: `cd gateway && gofmt -l . && go build ./... && go vet ./... && go vet -tags integration ./... && go test ./...`
   + integração com Docker via sudo (receita na memória `gateway-prod-build-deploy`):
   `sudo -n env CI_ALLOW_TIGHT_SHED_TIMING=1 PATH=... go test -tags=integration ./gateway/internal/integration_test/... ./gateway/cmd/gatewayctl/...`
   (rodar 1 pacote por vez — host com pouca RAM; `TestSensitiveSaturated503` pode falhar por pressão de memória → re-rodar isolado).
3. Docs GSD (SUMMARY + STATE), scan de segredos no diff, push develop, aguardar CI `build-gateway`.
4. Deploy prod (ordem):
   a. pre-pull `ghcr.io/ifixtelecom/ifix-ai-gateway:develop-<sha>` no worker-vm;
   b. **migration 0039** one-off com a imagem NOVA (env-file = TODAS as envs do container vivo,
      umask 077, apagar depois): `docker run --rm --env-file f --entrypoint /gatewayctl <img> migrate status|up`;
   c. PUT stack 38 (Portainer endpoint 6) trocando só `image:` (84 envs iguais; backup do stack antes);
   d. verificar versão, 0 ERROR, `primary state`, overrides 3060 preservados, smoke chat/embeddings;
   e. confirmar `offer_mode='bid'` e `min_reliability=0.95` no pod_config.
   Rollback: `offer_mode='ondemand'`; `docker service update --rollback ai-gateway-prod_gateway`.
   ⚠️ Restart do gateway durante provisionamento do primário reinicia o lifecycle (aceitável).
5. Validar no 1º dia: primário nasce bid? custo real logado vs `dph_total` da instância
   (HIPÓTESE: min_bid sem storage); status real de uma preempção.

### Já em produção hoje
- Gateway `develop-66050fd`: hot-reload URL (url_override), auth singleflight+L1, revoke imediato,
  shed dwell, STT OOM sem breaker, sweep de órfãos com log.
- `/opt/vast-3060` (commit 3d4b0b2): bid default + fallback, watchdog 3 min (07–19h), disco 40G,
  whisper int8_float16, ranking com download amortizado. **1º pod 3060 interruptível: 2026-10-02 07:00**
  — conferir `journalctl -u vast-unified-start -u vast-unified-watchdog`.
- Preços groq-whisper + phantom cadastrados.

### Achados registrados
- L4: cabe ctx de prod mas ~90 tok/s (3090 ~190) e US$/1M tokens pior → não usar no primário.
- Taxa de download por host (`inet_down_cost`) pesa até ~US$0,50/subida.
- Primário barato (US$0,10) morreu sozinho às 13:08Z (host terminal) — mercado, não bug.
