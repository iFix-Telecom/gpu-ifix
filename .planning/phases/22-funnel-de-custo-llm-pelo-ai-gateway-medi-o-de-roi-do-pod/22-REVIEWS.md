---
phase: 22
reviewers: [gemini, codex]
reviewed_at: 2026-07-19T17:02:31Z
plans_reviewed: [22-01-PLAN.md, 22-02-PLAN.md, 22-03-PLAN.md, 22-04-PLAN.md, 22-05-PLAN.md, 22-06-PLAN.md, 22-07-PLAN.md]
note: claude skipped (self — sessão rodando dentro do Claude Code)
---

# Cross-AI Plan Review — Phase 22

## Gemini Review

# Revisão do Plano de Implementação — Phase 22

Esta revisão analisa os 7 planos da **Phase 22: Funnel de custo LLM pelo ai-gateway + medição de ROI do pod**. O objetivo central é consolidar 100% dos gastos de IA no gateway para validar empiricamente se a economia gerada pelo pod primário (Vast.ai) justifica seu custo operacional.

## 1. Resumo (Assessment)

O plano é de altíssima fidelidade técnica e demonstra um entendimento profundo das "armadilhas" do ambiente atual (como o fato de o código da Phase 113 estar em estado *dangling*). A estratégia de priorizar a correção de preços (**PRICE-01**) como um "Gate de Verdade" para a medição de ROI é correta e evita conclusões enviesadas. A abordagem de "ponytail" (reúso do endpoint `/admin/economy` da Phase 15) demonstra eficiência de contexto. O plano respeita rigorosamente as restrições de rollout gradual e segurança (rollback) em um ambiente de produção (dev) com usuários reais.

## 2. Pontos Fortes (Strengths)

- **Identificação do Bloqueio Crítico (Phase 113):** A pesquisa detectou que o código de roteamento na `converseai-v4` não está no branch `develop`. O Plano **22-02** trata isso como um Gate obrigatório, evitando que a ativação das envs seja um "no-op" silencioso.
- **Auditoria de Preços Realistas:** O Plano **22-01** não assume que os preços atuais estão certos; ele exige o cruzamento com as faturas reais do Google e OpenAI antes de gravar os novos valores, garantindo que o ROI seja honesto.
- **Decisão Baseada em Evidência (Gemini Chat):** O Plano **22-04** prevê um teste de path (`path.Join`) antes de decidir entre criar um upstream nativo (risco de 404) ou um alias via OpenRouter (caminho provado).
- **Rollout Gradual e Rollback:** A sequência de ativação (STT → Followup → Secundárias) com verificações individuais de `usage report` e `billing_events` minimiza o raio de explosão.
- **Reúso de Infraestrutura:** A utilização do `gatewayctl` para hot-reload de preços e do endpoint `/admin/economy` para o cálculo de ROI economiza tempo de desenvolvimento e evita redundância de lógica.

## 3. Preocupações e Riscos (Concerns)

- **[HIGH] Re-land do Código Phase 113:** Se o Gate do plano **22-02** der "AUSENTE", o cherry-pick dos commits dangling pode introduzir conflitos imprevistos. Embora o plano preveja `pytest` e CI, o impacto é no repositório principal de produção (dev).
- **[MEDIUM] Chaves de Preço Silenciosas:** No **22-01**, se a chave `(model, provider, unit)` no banco não casar exatamente com o que o `interceptor_usage.go` monta em runtime, o custo será registrado como R$0. O monitoramento de `gateway_prices_missing_total` é a mitigação, mas requer atenção ativa do operador.
- **[MEDIUM] Media Pipeline Gemini (Visão):** Conforme notado na pesquisa, o gasto com visão (multimodal nativo) não passará pelo gateway e continuará sendo um "ponto cego" no ROI total do pod. Isso deve ser mantido explícito nos relatórios finais.
- **[LOW] Câmbio USD/BRL:** O ROI é sensível à volatilidade do câmbio. O plano prevê `set-fx`, mas a medição mensal pode ter drift se o câmbio mudar bruscamente no meio do mês.

## 4. Sugestões (Suggestions)

1. **Automação do ROI Query:** Na Task 1 do **22-07**, sugere-se que a query SQL seja salva não apenas no `.sql`, mas que o resultado seja formatado em uma tabela Markdown no `22-07-ROI.md` para facilitar a leitura gerencial pelo Pedro.
2. **Monitoramento de Erros de Preço:** No Plano **22-01**, após aplicar os preços, recomendo rodar um `SELECT` em `billing_events` para garantir que não existem entradas recentes com `cost_external_brl = 0` para os novos upstreams roteados.
3. **Log de Gateway na ConverseAI:** No rollout do **22-03** e **22-06**, garantir que os logs da stack 15 (Portainer) estejam acessíveis para confirmar o literal `provider="ifix-gateway"`, o que dá segurança visual imediata de que o roteamento mudou.

## 5. Avaliação de Risco (Risk Assessment)

**Risco Geral: MÉDIO**

O risco técnico de quebrar a `converseai-v4` é controlado pelo rollout gradual e pela lógica de "key vazia = comportamento antigo". O maior risco é o **re-land do código (22-02)**, que exige manipulação de commits fora do branch principal. No entanto, uma vez que o código esteja no lugar, a ativação via variáveis de ambiente é segura e facilmente reversível. O risco de medição (ROI errado) é mitigado pelo Gate do Plano **22-01**.

**Veredito: PLANOS APROVADOS PARA EXECUÇÃO.**

---

## Codex Review

**Avaliação Geral**

Os planos são fortes, bem lastreados na pesquisa e respeitam os gates críticos: `PRICE-01` antes de ROI, runtime gate do CV-01 antes de ligar envs, rollout gradual por tenant e rollback por variável. O principal risco é que a meta “100% do gasto LLM pelo gateway” ainda não é literalmente atingida, porque o pipeline multimodal Gemini nativo parece fora do alcance do gateway atual. Outro ponto: há muitos checkpoints humanos corretos para produção, mas isso torna a fase dependente de execução operacional disciplinada e boa documentação dos estados antes/depois.

---

## 22-01 — PRICE-01

**Summary:** Plano muito bom como gate de medição. Corrige a premissa central: `cost_external_brl` é calculado por request a partir de `prices`, não armazenado em `prices`.

**Strengths**
- Reconhece corretamente que `prices` não tem `cost_external_brl`.
- Exige auditoria antes de mutar produção.
- Valida lookup exato por `(model, provider, unit)`.
- Usa prova empírica pós-request e métrica `gateway_prices_missing_total`.

**Concerns**
- **HIGH:** “1 linha active por chave” pode ser falso se `gatewayctl prices set` só insere nova linha sem expirar a anterior. O plano assume comportamento que precisa ser confirmado.
- **MEDIUM:** Usa faturas para inferir preço, mas fatura pode misturar volume off-gateway com preço errado.
- **MEDIUM:** Chaves/admin secrets aparecem em texto do plano.
- **LOW:** Não define como lidar com preços por milhão de tokens vs por token em auditoria, ponto propenso a erro decimal.

**Suggestions**
- Adicionar check SQL explícito: `count(*) WHERE valid_to IS NULL GROUP BY model,provider,unit HAVING count(*)>1`.
- Separar “preço de tabela provider” de “custo observado em fatura”.
- Redigir comandos com placeholders para admin key.
- Incluir cálculo exemplo para cada unidade: token, audio_second, embed_request.

**Risk Assessment:** **MEDIUM**. Baixo risco técnico, mas alto impacto se preço ou unidade forem aplicados errados.

---

## 22-02 — Gate runtime Phase 113

**Summary:** Plano essencial. Captura o maior risco descoberto pela pesquisa: ativar envs sem código consumidor no runtime.

**Strengths**
- Excelente gate antes do rollout.
- Registra digest da imagem para rollback.
- Trata re-land como decisão humana.
- Mantém comportamento inerte com keys vazias.

**Concerns**
- **HIGH:** Cherry-pick de commits dangling pode trazer código antigo incompatível com `develop`.
- **HIGH:** Push em `develop` + redeploy stack 15 pode afetar componentes além dos agents.
- **MEDIUM:** O check `hasattr(settings, ...)` prova config, mas não prova que os paths de STT/LLM realmente usam a config.
- **MEDIUM:** Não há plano claro para rollback de código se CI passa mas runtime falha.

**Suggestions**
- Além de `hasattr`, adicionar grep/teste funcional no branch consumidor, por exemplo chamadas mockadas do provider/STT.
- Registrar digest anterior e comando/procedimento exato de rollback.
- Rodar testes específicos dos fluxos Phase 113, não só `pytest` genérico.
- Se possível, abrir PR ou branch temporária em vez de push direto em `develop`.

**Risk Assessment:** **HIGH**. Necessário, mas toca código e deploy de app real; o gate reduz bastante o risco operacional.

---

## 22-03 — STT Gateway Key

**Summary:** Bom plano de rollout incremental. Começar por STT é coerente com menor blast radius e maior aprendizado.

**Strengths**
- Depende corretamente do 22-02.
- Baseline antes/depois bem definido.
- Rollback simples e documentado.
- Verifica billing, upstream e qualidade da transcrição.

**Concerns**
- **HIGH:** O plano afirma “fallback fornecedor direto” no branch 113; se isso não for provado no código, a mitigação de DoS pode ser falsa.
- **MEDIUM:** UAT por tráfego natural pode atrasar ou gerar evidência fraca.
- **MEDIUM:** Não define timeout/error-budget aceitável durante UAT.
- **LOW:** `provider="ifix-gateway"` vs `gateway_enabled=True` ainda está ambíguo.

**Suggestions**
- Adicionar teste ativo com áudio curto controlado.
- Confirmar no código se falha do gateway volta ao provider direto ou apenas falha a request.
- Definir critério mínimo: latência, status code, transcrição não vazia, custo esperado.
- Trocar expectativa de log para o campo real encontrado no código.

**Risk Assessment:** **MEDIUM**. Rollout pequeno e reversível, mas STT afeta dados reais e UX diretamente.

---

## 22-04 — Gemini Chat / CV-02

**Summary:** Plano bom por não aceitar cegamente “criar upstream Gemini” e exigir prova de path/tool-calling. É o plano com maior risco de escopo.

**Strengths**
- Decide A vs B por teste empírico.
- Reconhece falta de director Gemini chat.
- Testa `tools`/`tool_choice`, crítico para structured output.
- Evita quebrar `qwen` e Phase 20.

**Concerns**
- **HIGH:** Caminho B via OpenRouter não é “Google direto”; pode não capturar a mesma semântica/custo do gasto Gemini original.
- **HIGH:** A meta de “100% do gasto Gemini” não cobre media pipeline multimodal nativo.
- **MEDIUM:** Criar alias “temporário” já muta config prod; precisa rollback do alias.
- **MEDIUM:** `gemini-flash-lite` alias pode não casar com o model literal usado pelo código (`google/gemini-2.5-flash-lite`).

**Suggestions**
- Declarar explicitamente que CV-02 cobre apenas Gemini chat OpenAI-compatible, não visão/multimodal.
- Adicionar rollback para alias/upstream.
- Testar exatamente o modelo enviado pelo cliente real, não só alias escolhido.
- Se Caminho B for aceito, atualizar a meta: “funnel via gateway usando OpenRouter como provider”, não “Google direto”.

**Risk Assessment:** **HIGH**. Importante para o gasto maior, mas há risco semântico, de custo e de escopo.

---

## 22-05 — Followup Worker TS

**Summary:** Plano razoável e bem isolado. O opt-in por env é o desenho certo.

**Strengths**
- Não depende da Phase 113.
- Exige teste com key setada e vazia.
- Mantém rollback operacional simples.
- Define tenant específico para medição.

**Concerns**
- **MEDIUM:** Forçar `model=qwen` pode alterar comportamento se o followup atual usa outro modelo com características diferentes.
- **MEDIUM:** Não confirma se `qwen` alias tem fallback externo correto e preço correto para esse tenant.
- **MEDIUM:** “log do worker mostra baseURL” pode vazar informação operacional se logar demais.
- **LOW:** Precisa garantir que build/test do worker está no workspace correto.

**Suggestions**
- Documentar modelo atual do followup e diferença esperada ao mudar para `qwen`.
- Adicionar UAT comparando outputs antes/depois em 2-3 casos reais.
- Não logar API key; logar só provider/baseURL sanitizado.
- Confirmar alias `qwen` e billing antes de ativar env.

**Risk Assessment:** **MEDIUM**. Boa reversibilidade, mas muda modelo efetivo do fluxo.

---

## 22-06 — Secundárias CV-01

**Summary:** Plano operacionalmente sólido: uma key por vez, UAT e rollback. Bom alinhamento com o objetivo de funnelar Gemini secundário.

**Strengths**
- Sequência gradual correta.
- Gateia classifier/format-hint no 22-04.
- Verifica cada tenant separadamente.
- Rollback por key é simples.

**Concerns**
- **HIGH:** Se o código real envia `google/gemini-2.5-flash-lite`, o alias `gemini-flash-lite` pode não resolver.
- **HIGH:** Structured output pode passar no teste sintético e ainda falhar no schema real.
- **MEDIUM:** AI-match está pouco definido; “confirmar alias se faltar” pode virar trabalho não planejado.
- **MEDIUM:** Não há critério quantitativo de qualidade para classifier/format-hint/ai-match.

**Suggestions**
- Testar payloads reais dos três fluxos antes de ativar em produção.
- Adicionar aliases para todos os nomes de modelo literais usados pelo app.
- Dividir ai-match em subgate se o modelo/upstream for diferente.
- Registrar taxa de erro e amostra qualitativa antes/depois por passo.

**Risk Assessment:** **MEDIUM-HIGH**. Rollout seguro, mas os fluxos usam structured output e podem degradar comportamento sem erro óbvio.

---

## 22-07 — ROI

**Summary:** Plano bom como fechamento, mas o veredito só será confiável se o escopo do funnel for honesto sobre o que ficou fora.

**Strengths**
- Reusa `/admin/economy` em vez de reinventar.
- Query reproduzível e committed.
- Cruza endpoint com SQL.
- Mantém `PRICE-01` como gate explícito.

**Concerns**
- **HIGH:** “pod compensa?” pode sair enviesado se ainda houver gasto fora do gateway, especialmente Gemini multimodal.
- **MEDIUM:** Query de pod usando `started_at >= month` pode perder lifecycle iniciado antes do mês e ainda ativo dentro do mês.
- **MEDIUM:** `cost_external_brl` não é retroativo; após corrigir preços, o mês atual mistura eventos pré e pós-correção.
- **LOW:** `custo_openrouter_brl` parece nome limitado se inclui externos não-OpenRouter.

**Suggestions**
- Ajustar query de lifecycles para interseção temporal com o mês, não só `started_at`.
- Separar relatório em “medido pelo gateway” vs “fora do gateway conhecido”.
- Marcar período válido pós-PRICE-01, ou recalcular histórico se houver base suficiente.
- Incluir “coverage ratio”: gasto faturado provider vs gasto observado em `billing_events`.

**Risk Assessment:** **MEDIUM**. A query é simples, mas o risco está na interpretação do ROI.

---

## Veredito Final

A fase está bem planejada e os gates certos estão no lugar. Eu aprovaria a execução com ajustes, especialmente:

- confirmar unicidade real de preços ativos no 22-01;
- reforçar o 22-02 com prova funcional, não só `hasattr`;
- declarar que media pipeline Gemini nativo não entra no “100%” atual;
- ajustar o 22-07 para não produzir ROI como verdade absoluta enquanto houver gasto conhecido fora do gateway.

Com esses ajustes, o plano atinge bem o objetivo prático: aumentar drasticamente a cobertura do billing pelo gateway e tornar o ROI do pod mensurável com lastro.

---

## Consensus Summary

Ambos (gemini + codex) **aprovam a execução com ajustes**. Zero recomendação de replanejar do zero. Gates críticos (PRICE-01 antes de ROI, runtime-gate CV-01 antes de env, rollout gradual + rollback) reconhecidos como corretos por ambos.

### Agreed Strengths (2+ reviewers)
- **Runtime-gate 22-02** captura o maior risco (código 113 dangling → env seria no-op). Ambos elogiam.
- **PRICE-01 como gate honesto** — auditoria cruzando faturas antes de mutar prod; `cost_external_brl` corretamente entendido como calculado por-request, não coluna de `prices`.
- **CV-02 decisão por path-test** (A vs B) em vez de assumir upstream nativo.
- **Rollout gradual + rollback por key/env**; reuso de `/admin/economy` (Phase 15) e `gatewayctl` hot-reload.

### Agreed Concerns (2+ reviewers — prioridade alta)
1. **[HIGH] "100%" não é literal — media pipeline Gemini multimodal (visão) fica FORA do gateway** (não-proxyável). Ambos: 22-07 deve declarar cobertura honesta ("medido pelo gateway" vs "fora do gateway conhecido" + coverage ratio), senão o veredito "pod compensa?" sai enviesado.
2. **[HIGH] Alias `gemini-flash-lite` pode não casar o literal `google/gemini-2.5-flash-lite` que o app envia** (22-04/22-06) → resolve falha silenciosa. Ambos pedem alias para TODOS os nomes de modelo literais + teste com payload real, não sintético.
3. **[HIGH] 22-02 re-land de commits dangling** é o maior risco operacional. Codex: preferir PR/branch temporária a push direto em `develop`; `hasattr` prova config mas não que os paths STT/LLM usam a config → adicionar prova funcional. Gemini idem (conflitos imprevistos).

### Divergent / Single-reviewer (worth investigating)
- **[codex HIGH] 22-01 unicidade de preço ativo** — "1 linha active por chave" assume que `gatewayctl prices set` expira a anterior; não provado. Add check SQL `count(*) WHERE valid_to IS NULL GROUP BY ... HAVING count>1`.
- **[codex MEDIUM] 22-07 janela temporal** — lifecycle do pod iniciado antes do mês e ativo dentro perde na query por `started_at >= month`; usar interseção temporal. E mês atual mistura eventos pré/pós-correção de preço (`cost_external_brl` não retroativo) → marcar período válido pós-PRICE-01.
- **[codex MEDIUM] Secrets em texto do plano** (admin key / `ifix_sk_…`) → usar placeholders.
- **[gemini LOW] Drift de câmbio USD/BRL** no meio do mês afeta ROI (`set-fx`).
- **[codex MEDIUM] 22-05 `model=qwen`** muda modelo efetivo do followup; documentar diferença + UAT antes/depois.
