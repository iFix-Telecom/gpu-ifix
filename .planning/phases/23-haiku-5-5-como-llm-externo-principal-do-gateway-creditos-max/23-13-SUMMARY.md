---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 13
subsystem: dashboard/economia + ops/vast-3060
tags: [economy, haiku, creditos, 3060, aux-pod-lifecycles, dashboard]
requires: [23-11]
provides:
  - "EconomyResponse.summary com pct_servido_local_tokens, custo_creditos_haiku_brl, custo_externo_caixa_brl, vast_3060_brl"
  - "Painel Economia: Haiku (coberto por créditos), Custo externo (caixa), % local por tokens, Vast com a 3060"
  - "unified3060.record_aux_lifecycle + stop gravado dentro de vast_destroy"
affects: [23-14, 23-16]
tech-stack:
  added: []
  patterns: ["gravação de custo best-effort num ponto único (vast_destroy)", "state.aux_lifecycles {id: started_at, dph_usd} com poda de 3 dias"]
key-files:
  created:
    - dashboard/src/components/economy-panel.test.tsx
  modified:
    - dashboard/src/lib/gateway.ts
    - dashboard/src/lib/gateway.test.ts
    - dashboard/src/components/economy-panel.tsx
    - ops/vast-3060/unified3060.py
    - ops/vast-3060/test_unified3060.py
decisions:
  - "pct_servido_local_tokens está na escala 0–1 (decisão do 23-11), então o painel reaproveita o formatPctLocal existente"
  - "O card 'Custo OpenRouter' (custo_openrouter_brl, total externo que inclui o Haiku) saiu do painel e deu lugar a 'Custo externo (caixa)'. O campo continua no tipo para manter compatibilidade"
  - "dph_usd do start = pick['cost']['total'] (GPU + storage, US$/h), o mesmo custo real que o script já usa no teto e no state"
  - "O início vem do horário de antes do create, e não de um campo da Vast. O create devolve só new_contract e nenhum JSON de instância é buscado antes da gravação do start. HIPÓTESE não verificada: a Vast expõe um campo start_date (nada no repo usa esse campo)"
  - "O stop só é gravado com destroy 200 ou 404. Se o destroy falha, a instância continua cobrando e o lifecycle fica aberto"
  - "No resume, o start é reenviado com o started_at guardado no state. O gateway faz upsert com LEAST(started_at), então o reenvio é seguro e cobre o caso em que o start original não foi gravado por o gateway estar fora"
  - "A gravação do stop fica em _record_stop, chamado por vast_destroy. Por isso o grep de 'record_aux_lifecycle(' dá 4 (definição, _record_stop, start do create, start do resume)"
metrics:
  duration: "~25min"
  completed: 2026-10-15
  tasks: 2
  files: 6
---

# Phase 23 Plan 13: painel Economia fidedigno + custo da 3060 gravado no gateway — Summary

O painel Economia agora separa o custo do Haiku (passo ①), marcado "coberto por créditos" com caixa real de R$ 0,00, do gasto externo que sai de fato do caixa. O % servido local passa a ser medido por tokens, com o % por requests no hint, e o card do Vast mostra quanto da conta é da 3060. O `unified3060.py` grava o start (logo depois do create, com o horário de antes do create) e o stop (dentro de `vast_destroy`) em `POST /admin/aux-pod-lifecycles`. Essa gravação nunca derruba a operação do pod.

## Tarefas

| Task | Nome | Commits | Arquivos |
|------|------|---------|----------|
| 1 | Dashboard: tipos e cards do painel Economia | fc3c6ad (RED), 0f93f74 (GREEN) | gateway.ts, gateway.test.ts, economy-panel.tsx, economy-panel.test.tsx |
| 2 | unified3060.py grava start/stop no gateway | 17aa96c (RED), 5d7f6bd (GREEN) | unified3060.py, test_unified3060.py |

## Verificação (FATOS, com fonte)

- `npx vitest run src/lib/gateway.test.ts src/components/economy-panel.test.tsx` deu 21/21. No RED, antes da implementação, 4 testes do painel falharam.
- `npx tsc --noEmit` saiu com 0.
- Na suíte completa do dashboard, a 1ª rodada deu 105/106: uma falha intermitente que não se repetiu. As duas rodadas seguintes deram 106/106. O log não identificou o teste que falhou; a falha coincidiu com o outro executor (23-12) rodando ao mesmo tempo.
- `python3 -m pytest -q test_unified3060.py` deu 134 passed (118 antigos + 16 novos). No RED, os 16 novos falharam. `python3 -m unittest test_unified3060` também deu OK.
- Greps de aceite: `custo_creditos_haiku_brl` aparece 1× em gateway.ts; "coberto por créditos" está em economy-panel.tsx; `def record_aux_lifecycle` aparece 1×; `record_aux_lifecycle(` aparece 4×; `aux-pod-lifecycles` está presente.
- Pontos de destroy (grep `vast_destroy(env`): sweep, pending, fail() do boot falho, anterior, stop noturno e watchdog. Os seis passam por `vast_destroy`, que grava o stop. O teste do sweep confirma 1 stop por órfã.

Os testes novos do Python cobrem: POST com X-Admin-Key, pod "vast-3060" e timeout 10 s; `at` em RFC3339 com precisão de segundos; ausência de URL ou KEY sem nenhuma chamada HTTP; HTTP 500 devolvendo False sem a chave no stderr; exceção não propagada; already_closed devolvendo True; 404 devolvendo False; stop com total = 13 h × 0,1 = 1,3; stop sem total quando o state não tem entrada; destroy com 404 ainda gravando o stop; destroy com 500 sem stop; gateway fora sem afetar o retorno do destroy; state corrompido; sweep; ordem create → start → espera de boot com `at` anterior ao create; poda do state.

Não houve validação visual com agent-browser. O plano não tem checkpoint de UI, e a evidência é o teste de renderização com Testing Library.

## Desvios do plano

### Corrigidos automaticamente

**1. [Rule 2 - Correção] O stop não é gravado quando o destroy falha**
- O plano pedia stop em todo destroy. Com destroy HTTP 500 a instância continua viva e cobrando, e fechar o lifecycle subestimaria o custo. Por isso o stop só é gravado com 200 ou 404. A cobertura dos 6 pontos se mantém, porque cada destroy bem-sucedido grava o stop.
- Commit: 5d7f6bd

**2. [Rule 2] Reenvio do start no resume + poda do state**
- O plano não tratava o resume. Sem o reenvio, um start perdido (gateway fora) deixaria o stop daquela instância com 404 e o custo de fora. A poda de 3 dias em `aux_lifecycles` evita que o state.json cresça para sempre.
- Commit: 5d7f6bd

## Itens adiados (fora do escopo dos arquivos deste plano)

- `dashboard/src/components/local-vs-external-donut.tsx` ainda usa `pct_servido_local` (requests) e mostra `custo_openrouter_brl` como "OpenRouter (externo) … pagos". Esse total inclui o Haiku coberto por créditos, então mistura créditos com caixa. Precisa da mesma separação deste plano.
- A instalação de `GATEWAY_ADMIN_URL`/`GATEWAY_ADMIN_KEY` em `/etc/onboard/secrets/vast-3060.env` e o deploy do script no host ficam no 23-16. Nada de prod foi tocado aqui. A rota também só passa a responder depois que o 23-14 a montar no adminRouter. Até lá a gravação retorna False (404/405) sem efeito colateral.

## Known Stubs

Nenhum.

## Threat Flags

Nenhum além do threat_model. O T-23-41 está mitigado: a chave é lida do arquivo de segredos, vai só no header, é redigida em qualquer log, e há um teste que garante que ela não aparece no stderr. O T-23-42 está mitigado com timeout de 10 s, try/except amplo e um teste de gateway fora do ar.

## Self-Check: PASSED

- Arquivos existem: economy-panel.test.tsx, economy-panel.tsx, gateway.ts, unified3060.py, test_unified3060.py.
- Commits fc3c6ad, 0f93f74, 17aa96c, 5d7f6bd estão em `git log`.

## TDD Gate Compliance

RED → GREEN nas duas tasks: test(fc3c6ad) → feat(0f93f74) e test(17aa96c) → feat(5d7f6bd).
