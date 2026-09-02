---
quick_id: 260902-be9
slug: recriar-pod-3060-disco-40g
date: 2026-09-02
status: complete
---

# SUMMARY — Recriar pod 3060 unificado com disco 40G

## Resultado

Pod novo **49644867** (machine 148305, Coreia do Sul, RTX 3060, **disco 40G**,
$0,0329/h) substituiu o 48611358 (PL, 25G a 90%). Disco final: **48%** (19G/40G).
Stack 38 flipado (4 envs → 182.224.239.168, 8000→34089, 7998→34061), validado
via edge (STT+TTS), validação direta ok (STT/TTS/embed dims 1024/rerank),
gpu_temp 57°, disk-guard plantado e rodando. Antigo destruído às 10:49.
`INSTANCE` atualizado no unified3060.py (repo + /opt). Stop/start noturno mantido.

## Por que NÃO pod-novo-diário (pedido original)

Medição no pod antigo: 23G usados = stack instalado (HF cache 9,6G + speaches
8,1G + venv Infinity 6,2G + base 3,7G), lixo diário era KB/MB. Pod fresco
voltaria a ~90% de 25G no dia 1. Pedro escolheu recriar 1x com 40G
(AskUserQuestion).

## Descobertas (importantes p/ futuro provision)

1. **Receita onstart QUEBRADA p/ install fresco:** `pip install
   infinity-emb[all]==0.0.77` + transformers git-pinado → `ResolutionImpossible`
   (colpali-engine novo exige transformers ≥4.47/<4.54). Fix aplicado: `pip
   freeze` do venv do pod antigo → `ops/vast-3060/infinity-freeze.txt` (repo) →
   instalar com `--no-deps --extra-index-url https://download.pytorch.org/whl/cu121`
   (torch 2.5.1+cu121 não está no PyPI).
2. **Máquinas CN inviáveis** (38103, 146752): porta pública inacessível/lenta —
   excluir CN na seleção de oferta.
3. **Speaches re-install de modelo retorna 201** (não 200) — aceitar 2xx.
4. MX 33731: speaches health externo falhou 10min+ (interno não diagnosticado,
   instância destruída antes do fix do bail) — sem veredito.
5. Bail deve PRESERVAR instância + coletar diag via SSH (health interno vs
   externo, logs) — destruir apaga a evidência.

## Tentativas

| # | Machine | Geo | Falha |
|---|---------|-----|-------|
| 1 | 38103 | CN | avoid-list ignorado (erro meu); destruída proativa |
| 2 | 146752 | CN | speaches health timeout 10min (rede CN) |
| 3 | 148305 | KR | infinity timeout 30min (= pip ResolutionImpossible, diagnosticado depois) |
| 4 | 33731 | MX | speaches health timeout (processo harness morto no meio + porta pública) |
| 5 | 148305 | KR | ✅ sucesso após pip pinado manual |

Script one-shot: scratchpad `provision40.py` (não versionado; lições no item acima).
