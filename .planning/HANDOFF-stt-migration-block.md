# HANDOFF — STT flag deploy blocked by migration tests (2026-06-17)

## Bloqueador imediato
`build-gateway` no branch **main** falha em 3 testes de integração de migration → `:main` não builda → o flag STT não deploya em prod.

```
--- FAIL: TestIntegration_Migration0026_UpDownUp
--- FAIL: TestIntegration_Migration0026_DownAbortsOnDuplicateAliases
--- FAIL: TestIntegration_Migration0029_Down_Symmetric
```
- Run que falhou: 27714511054 (main, sha 86c0597). Integration job log via `gh api .../jobs/<id>/logs`.
- Suspeito #1: **migration `0030_probe_status_allow_config.sql`** (adicionada hoje, dbc73ce) quebrou o down-symmetry de 0026/0029. Pode ser o Down do 0030 ou o harness de teste que aplica todas e testa down do topo.
- NÃO é o flag STT (esses são testes de model_aliases, não de reconciler). `primaryTestCfg` não é a causa aqui.
- Verificar se o build anterior de main (6786c1b, migration 0030) JÁ falhava — se sim, 0030 é a causa; último main verde = d0f1f6b (STT rewrite, SEM 0030).

## Como reproduzir local
```
cd gateway && go test ./internal/integration_test/ -run 'Migration0026|Migration0029' -count=1 -v   # precisa Docker; usar: sudo env PATH=/usr/local/go/bin:$PATH HOME=$HOME GOCACHE=$HOME/.cache/go-build GOPATH=$HOME/go go test ...
```

## Estado de prod AGORA
- Gateway prod (n8n-ia-vm, ai-gateway.converse-ai.app, `/opt/ai-gateway-prod/`): imagem **`:main` = d0f1f6b** (STT model-rewrite; SEM o flag SERVE_STT).
- `.env` prod tem `PRIMARY_POD_SERVE_STT=false` MAS é **no-op** (código do flag não está em d0f1f6b).
- Pod: machine 7970 (1×3090, $0.23/h), schedule ON (seg-sex 9-17h BRT, drena 17:00). STT roteia pro pod → whisper CPU ~17s/3s → **timeout 60s em áudio real**.
- DB prod `bd_ai_gateway_prod` JÁ tem o constraint `config` aplicado manualmente (probe grava ok).

## Objetivo (Passo 0 — desbloqueio STT)
Flag `PRIMARY_POD_SERVE_STT=false` deve rotear STT pro gemini (rápido) em vez do pod CPU lento. Código pronto (commit 4021901, em develop+main): config `Config.PrimaryPodServeSTT` (default true) + skip do override `stt` em 3 sites do reconciler.go.

## Sequência pra fechar
1. **Consertar os 3 migration tests** (provável: ajustar `0030_probe_status_allow_config.sql` Down OU o teste de symmetry). Diagnose com `/gsd:debug`.
2. push develop → merge main → **build-gateway main verde** → `:main` novo.
3. Deploy prod: `ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose pull ifix-ai-gateway && docker compose up -d ifix-ai-gateway'` (Portainer NÃO força pull — SEED-017). Confirmar `rev` mudou (≠ d0f1f6b).
4. Testar STT: `model=whisper` deve dar ~2-3s (gemini), não 17-25s (pod). Texto diferente de "Thank you" num tom puro confirma gemini.

## Depois — projeto SEED-019 (cascata de shapes)
2×3090 ($0.229) → 1×5090 ($0.459) → 1×3090-no-STT ($0.130). 3 partes: gateway N-shape cascade + pod whisper VRAM-adaptive (tira pin CPU do supervisord.conf:46) + override STT condicional (o flag é a parte 3, base). Ver `.planning/seeds/SEED-019-*.md`.

## SEEDs abertos hoje
013 (probe rewrite, DEPLOYADO+funciona) · 014 (pod loop, RESOLVIDO) · 015 (CI integration, RESOLVIDO) · 016 (gemini MIME) · 017 (deploy propagation: Portainer no-force-pull + build-primary-pod develop-only) · 018 (STT model-rewrite, DEPLOYADO d0f1f6b) · 019 (shape cascade).

## Segurança
Usuário colou `.env` do stack DEV no chat com key OpenAI viva (`sk-proj-MNUq...`) — **rotacionar** se transcript puder vazar.
