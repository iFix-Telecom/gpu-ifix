---
phase: quick-260923-sho
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - .planning/seeds/SEED-009-realtime-voice-ai-phone-call.md
  - .planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md
autonomous: true
requirements: [SEED-009]
user_setup: []

must_haves:
  truths:
    - "SEED-009 mostra status poc-open, a decisão do Pedro de 2026-09-23 (OpenAI gpt-realtime-2.1-mini via SIP nativo primeiro) e link para o POC-PLAN.md"
    - "SEED-009 tem tabela de preços 2026-09-23 com fonte oficial por linha; a tabela antiga (gpt-4o-realtime-preview, Gemini 2.5 Live) continua no arquivo, marcada como DESATUALIZADA"
    - "POC-PLAN.md descreve arquitetura, pré-requisitos, etapas com gate APROVAÇÃO PEDRO antes de toda ação externa/prod, métricas, critérios de sucesso/falha, rollback, riscos, FATOS vs HIPÓTESES e próximos passos"
    - "Nenhuma ação é executada fora do repositório: zero SSH, zero chamada de API, zero mudança em Asterisk/stack 36/trunk 8880/OpenAI"
  artifacts:
    - path: ".planning/seeds/SEED-009-realtime-voice-ai-phone-call.md"
      provides: "Seed atualizado para poc-open"
      contains: "status: poc-open"
    - path: ".planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md"
      provides: "Plano da PoC de voz realtime OpenAI SIP"
      min_lines: 150
  key_links:
    - from: ".planning/seeds/SEED-009-realtime-voice-ai-phone-call.md"
      to: "POC-PLAN.md"
      via: "link relativo no seed"
      pattern: "260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md"
---

<objective>
Abrir formalmente a PoC de voz em tempo real em ligação VoIP com OpenAI `gpt-realtime-2.1-mini` via SIP nativo (Arquitetura B do SEED-009), SOMENTE em documentação.

Purpose: registrar a decisão do Pedro de 2026-09-23, atualizar preços com fontes oficiais e deixar um plano de PoC executável depois, com gates de aprovação explícita antes de qualquer ação externa.
Output: SEED-009 atualizado + POC-PLAN.md novo. Nenhum código, nenhuma ação em infraestrutura.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@.planning/seeds/SEED-009-realtime-voice-ai-phone-call.md

<guardrail>
GUARDRAIL DURO (decisão Pedro 2026-09-23): esta quick é só documentação. O executor NÃO faz SSH, NÃO chama API (OpenAI, Portainer, Cloudflare), NÃO toca Asterisk do Worker-Oracle, stack Portainer 36 (endpoint 7), trunk 8880, NÃO compra chave/créditos, NÃO configura webhook. O executor NÃO commita — o orquestrador commita.
Leitura do repo /home/pedro/projetos/pedro/voip-api é permitida (só leitura) se precisar confirmar nome de arquivo/contexto.
</guardrail>

<locked_decisions>
D-01: PoC começa por OpenAI `gpt-realtime-2.1-mini` via SIP nativo (Arquitetura B). Etapa 2 = Gemini Live (mais barato, exige ponte ARI externalMedia → WebSocket). ElevenLabs só se nenhum dos dois passar no teste de ouvido.
D-02: Preços 2026-09-23 (FATOS, fontes oficiais):
  - OpenAI (https://developers.openai.com/api/docs/pricing): gpt-realtime-2.1 = $32 in / $0,40 cached in / $64 out por 1M tokens de áudio; gpt-realtime-2.1-mini = $10 / $0,30 / $20.
  - Gemini (https://ai.google.dev/gemini-api/docs/pricing): gemini-3.8-live = $3/1M ($0,005/min) entrada de áudio, $12/1M ($0,018/min) saída.
  - ElevenLabs Agents (https://elevenlabs.io/pricing/agents): $0,08/min (burst $0,16/min acima da concorrência), LLM cobrado À PARTE, aceita SIP trunk.
  - OpenAI tem SIP nativo documentado (https://developers.openai.com/api/docs/guides/realtime-sip): webhook `realtime.call.incoming` (configurado em platform > Project > Webhooks), nosso serviço aceita a chamada via API passando instructions/tools. Gemini Live = só WebSocket, sem SIP.
D-03: HIPÓTESES (devem aparecer rotuladas `HIPÓTESE:` com "o que resolve"):
  - Custo/min estimado: mini ~$0,018, full ~$0,058, Gemini ~$0,014, ElevenLabs $0,08 + LLM. Conversão OpenAI usa razão antiga ~600 tok/min entrada e ~1200 tok/min saída, NÃO confirmada. Resolve: ligação teste + usage no dashboard OpenAI.
  - Custo real maior por re-cobrança de contexto a cada turno; silêncio conta como entrada no Gemini? Resolve: ligações de 3 e 10 min comparadas com a fatura.
  - Latência speech-to-speech menor que pipeline STT→LLM→TTS. Resolve: medir na PoC.
  - Qualidade PT-BR em áudio telefônico 8 kHz: não medida.
D-04: LGPD — áudio de cliente sai para provider externo; mesmo trade-off já aceito nos tenants transcricao-voip. Na PoC, só ramal de teste interno (sem cliente).
D-05: Cada ação externa/prod da PoC exige gate "APROVAÇÃO PEDRO" explícito.
</locked_decisions>

<interfaces>
<!-- Fatos do repo voip-api (lido em 2026-09-23, só leitura) para o executor citar nos docs sem explorar. -->
Repo: /home/pedro/projetos/pedro/voip-api/voip-api/docker/asterisk/
- pjsip.conf.template: transports [transport-udp], [transport-wss], [transport-tls], [transport-tls-5061] (method=tlsv1_2).
- sorcery.conf: endpoint/auth/aor/identify/registration = realtime Postgres (ps_endpoints, ps_auths, ps_aors, ps_identify, ps_registrations).
- extensions.conf contextos: [from-external] (inbound do trunk → Stasis(voip-api,...)), [from-extensions], [route-cascade], [b2bua-hangup], [internal-hangup], [webphone-out], [from-queue], [webphone-control].
Infra: Asterisk roda no Worker-Oracle (Portainer stack 36, endpoint 7, Tailscale 100.96.9.111); trunk 8880 = produção.
Consequência para o plano: o endpoint OpenAI e o contexto de teste devem ser NOVOS e isolados (ex.: contexto `[poc-openai-realtime]` + endpoint `openai-realtime` via transport-tls) — nunca alterar [from-external], [from-extensions] nem o trunk 8880. Onde o endpoint é criado (arquivo estático vs linha em ps_endpoints) é decisão da etapa correspondente, sob aprovação.
</interfaces>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Atualizar SEED-009 para poc-open com preços 2026-09-23 e decisão do Pedro</name>
  <files>.planning/seeds/SEED-009-realtime-voice-ai-phone-call.md</files>
  <action>
Editar o seed PRESERVANDO todo o conteúdo existente (Intent, Por que whisper batch não serve, Arquitetura A, mudanças no gateway, integração telefonia, pré-requisitos, quick-wins). Mudanças:
1. Frontmatter: `status: backlog` → `status: poc-open`; adicionar `poc_opened_at: 2026-09-23` e `poc_plan: .planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md`. Manter os demais campos.
2. Logo após o título, nova seção `## Status 2026-09-23 — PoC aberta` com: decisão do Pedro (D-01) em texto; ordem das etapas (1 OpenAI gpt-realtime-2.1-mini SIP nativo, 2 Gemini Live via ponte ARI externalMedia→WebSocket, 3 ElevenLabs só se nenhum passar no teste de ouvido); link relativo para `../quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md`; frase de guardrail: nada executado em prod, cada ação externa exige aprovação do Pedro.
3. Na seção Arquitetura B: adicionar título `#### Preços oficiais 2026-09-23 (FATOS)` com tabela colunas Modelo | Entrada | Entrada cached | Saída | Unidade | Fonte, com as linhas de D-02 (gpt-realtime-2.1, gpt-realtime-2.1-mini, gemini-3.8-live com $/1M e $/min, ElevenLabs Agents $0,08/min burst $0,16/min + LLM à parte) e URL oficial por linha. Adicionar nota de FATO: OpenAI tem SIP nativo (webhook `realtime.call.incoming`, URL do guia realtime-sip); Gemini Live só WebSocket, sem SIP.
4. Logo abaixo, bloco `#### Custo por minuto estimado` com cada estimativa de D-03 prefixada `HIPÓTESE:` e linha "Resolve: ...".
5. A tabela antiga (gpt-4o-realtime-preview / Gemini 2.5 Live) e a frase "Latência típica: 200-500ms. Custo: $0.10-0.30/min" ficam no arquivo, sob um subtítulo `#### Tabela antiga (DESATUALIZADA — capturada 2026-06-04, substituída pelos preços de 2026-09-23)`.
6. Na seção "Pré-requisitos antes do phase 12", anotar que o item 3 (PoC de latência) agora é coberto pelo POC-PLAN.md, e que a PoC via SIP nativo NÃO depende do item 2 (WebSocket no gateway) — a integração no gateway fica para depois do resultado da PoC.
Português-BR com acentuação correta. Não inventar número além de D-02/D-03.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix && f=.planning/seeds/SEED-009-realtime-voice-ai-phone-call.md && grep -q '^status: poc-open' $f && grep -q 'gpt-realtime-2.1-mini' $f && grep -q 'developers.openai.com/api/docs/pricing' $f && grep -q 'ai.google.dev/gemini-api/docs/pricing' $f && grep -q 'elevenlabs.io/pricing/agents' $f && grep -q 'realtime.call.incoming' $f && grep -q 'DESATUALIZADA' $f && grep -q 'gpt-4o-realtime-preview' $f && grep -q 'POC-PLAN.md' $f && grep -c 'HIPÓTESE:' $f | awk '$1>=3{ok=1} END{exit !ok}' && grep -q 'Arquitetura A' $f && grep -q 'Quick-wins' $f && echo OK</automated>
  </verify>
  <done>Seed em poc-open, com decisão, tabela de preços 2026-09-23 com fonte por linha, estimativas marcadas HIPÓTESE, tabela antiga preservada e marcada DESATUALIZADA, link para POC-PLAN.md; nenhuma seção original removida.</done>
</task>

<task type="auto">
  <name>Task 2: Criar POC-PLAN.md da PoC OpenAI Realtime via SIP</name>
  <files>.planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md</files>
  <action>
Criar documento em Português-BR (acentuação correta), título "PoC — Voz em tempo real em ligação VoIP com OpenAI gpt-realtime-2.1-mini via SIP nativo", cabeçalho com data 2026-09-23, origem SEED-009, decisão D-01, status "plano — nada executado". Seções obrigatórias, nesta ordem:

1. Objetivo: provar (ou reprovar) conversa ao vivo humano↔IA numa ligação real pelo Asterisk do voip-api, medindo latência, barge-in, custo real e qualidade PT-BR em áudio 8 kHz; resultado alimenta a decisão Arquitetura A vs B do SEED-009.
2. Arquitetura: diagrama em texto (bloco de código) — ramal de teste (softphone interno) → Asterisk (Worker-Oracle) contexto isolado `[poc-openai-realtime]` → endpoint PJSIP `openai-realtime` via transport-tls → sip.api.openai.com (OpenAI Realtime) ; paralelo: OpenAI → webhook `realtime.call.incoming` (HTTPS público via Traefik) → serviço PoC nosso → chamada de aceite na API com instructions/tools/voz. Explicar em prosa os dois planos (mídia SIP/RTP e controle webhook+API). Citar os fatos de <interfaces> (transports existentes, sorcery realtime, contextos existentes que NÃO serão tocados). Registrar como ponto a confirmar na etapa de pré-voo: parâmetros exatos do SIP URI/projeto e codecs aceitos, conforme guia https://developers.openai.com/api/docs/guides/realtime-sip (não inventar formato de URI).
3. Pré-requisitos: projeto OpenAI DEDICADO à PoC com chave própria e limite de gasto (budget/hard cap) configurado; webhook público via Traefik (edge da vps-ifix-vm, regra DNS-first antes do router TLS) apontando para o serviço PoC, com verificação da assinatura do webhook; ramal + contexto de dialplan ISOLADO que não recebe nem origina chamada de cliente e não passa pelo trunk 8880; codec G.711 (ulaw/alaw) negociado; forma de medir latência (gravação da chamada no Asterisk ou captura RTP no ramal de teste).
4. Etapas numeradas (mínimo 8), cada uma com: o que é feito, onde, quem, e — para toda ação externa ou em prod — linha `GATE: APROVAÇÃO PEDRO` (texto puro, sem emoji). Sequência sugerida: 0 pré-voo só leitura (reler guia SIP, confirmar formato de URI/codecs/auth, listar o que muda no Asterisk) → 1 criar projeto OpenAI + limite de gasto + chave [GATE] → 2 subir serviço PoC do webhook (host e forma decididos na etapa, sem tocar stacks de prod) [GATE] → 3 DNS + rota Traefik do webhook [GATE] → 4 cadastrar webhook `realtime.call.incoming` no projeto [GATE] → 5 adicionar endpoint PJSIP + contexto isolado no Asterisk Oracle, com backup e diff apresentados antes [GATE] → 6 ligação de fumaça 30 s do ramal de teste [GATE] → 7 ligações de medição 3 min e 10 min + roteiro de barge-in e de tool call [GATE] → 8 coleta de usage/custo no dashboard OpenAI e relatório → 9 rollback/limpeza [GATE] → 10 decisão go/no-go e abertura da etapa Gemini.
5. Métricas: latência de turno (fim da fala do humano → 1º áudio da IA, em ms, p50/p95 sobre N turnos); interrupção/barge-in (IA para de falar em quanto tempo após o humano começar); custo real por ligação de 3 e 10 min via usage do projeto (tokens in/cached/out → US$ e US$/min); WER/qualidade PT-BR ouvida (transcrição de referência humana vs transcrição da sessão, mais nota subjetiva de naturalidade); taxa de alucinação de ação (tool calls indevidas ou com argumento inventado / total de tool calls).
6. Critérios de sucesso e de falha OBJETIVOS com limiares numéricos declarados como metas propostas pelo planejamento (ex.: p95 de latência de turno, barge-in, US$/min máximo, WER máximo, zero ação alucinada no roteiro) — deixar explícito que os limiares são propostos e o Pedro confirma antes da etapa 7.
7. Rollback: remover endpoint/contexto do Asterisk (restaurar backup da etapa 5 + reload), desativar webhook no projeto, derrubar serviço PoC, remover rota Traefik/DNS, revogar chave e arquivar projeto OpenAI; checagem pós-rollback de que trunk 8880 e contextos de prod estão idênticos ao backup.
8. Riscos: LGPD (áudio sai para provider externo; PoC usa só ramal interno, sem cliente; mesmo trade-off aceito em transcricao-voip); custo descontrolado (limite de gasto no projeto, ligações com duração máxima, desligar webhook após medição); impacto no trunk 8880 de produção (contexto isolado, sem rota de entrada/saída pelo trunk, backup + diff aprovado); webhook público exposto (verificação de assinatura, rota só com o path necessário); regressão no Asterisk do Oracle (reload em vez de restart, janela combinada).
9. FATOS vs HIPÓTESES: dois blocos separados. FATOS com fonte (preços D-02 com URLs, SIP nativo OpenAI, Gemini só WebSocket, fatos de infra de <interfaces>). HIPÓTESES com prefixo `HIPÓTESE:` e "Resolve: ..." (todas de D-03). Adicionar "NÃO SEI / dado insuficiente" para o que não há dado (ex.: formato exato de URI SIP, comportamento de NAT/RTP entre Oracle e OpenAI).
10. Próximos passos: etapa 2 Gemini Live (ponte ARI externalMedia → WebSocket, mesmas métricas e roteiro para comparação direta); ElevenLabs só se nenhum passar no teste de ouvido; se aprovada, integração no ai-gateway (endpoint realtime, bilhetagem por minuto — itens já listados no SEED-009).

Não incluir comandos que executem ação em prod como se fossem para rodar agora; comandos, se houver, ficam dentro das etapas com gate e marcados como exemplo a validar no pré-voo.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix && f=.planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md && test $(wc -l < $f) -ge 150 && test $(grep -c 'APROVAÇÃO PEDRO' $f) -ge 7 && grep -q 'realtime.call.incoming' $f && grep -q 'sip.api.openai.com' $f && grep -q 'G.711' $f && grep -q '8880' $f && grep -qi 'rollback' $f && grep -q 'LGPD' $f && grep -qi 'barge-in' $f && grep -q 'WER' $f && grep -q 'FATOS' $f && test $(grep -c 'HIPÓTESE:' $f) -ge 4 && grep -q 'Gemini' $f && grep -q 'poc-openai-realtime' $f && echo OK</automated>
  </verify>
  <done>POC-PLAN.md existe com as 10 seções, gate APROVAÇÃO PEDRO em toda etapa externa/prod, métricas e critérios numéricos (marcados como propostos), rollback, riscos, FATOS com fonte separados de HIPÓTESES rotuladas, e próximos passos com Gemini na etapa 2.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| Documento → executor futuro da PoC | Instruções do plano viram ações em prod se mal redigidas |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-sho-01 | Tampering | Asterisk Oracle / trunk 8880 | mitigate | Esta quick não executa nada; POC-PLAN exige gate APROVAÇÃO PEDRO + backup/diff antes de mexer no Asterisk, contexto isolado sem rota pelo trunk 8880 |
| T-sho-02 | Information Disclosure | Credenciais nos docs | mitigate | Docs NÃO incluem chave OpenAI, secret de webhook nem tokens; só nomes de variáveis/etapas |
| T-sho-03 | Denial of Service (financeiro) | Projeto OpenAI | mitigate | POC-PLAN exige projeto dedicado com limite de gasto antes da 1ª ligação |
| T-sho-04 | Information Disclosure | Áudio de cliente (LGPD) | mitigate | PoC restrita a ramal interno de teste; sem cliente |
</threat_model>

<verification>
- Os dois automated verifies retornam OK.
- `git status` mostra só os 2 arquivos (seed modificado + POC-PLAN.md novo) e este PLAN; nenhum arquivo fora de .planning/ alterado.
- Nenhum comando de rede/SSH executado durante a quick.
</verification>

<success_criteria>
- SEED-009 em poc-open com decisão, preços com fonte e link para a PoC; conteúdo original preservado.
- POC-PLAN.md pronto para execução futura, com gates de aprovação e separação FATOS/HIPÓTESES.
- Zero ação em prod; executor não commita.
</success_criteria>

<output>
Criar `.planning/quick/260923-sho-poc-voz-realtime-openai-sip/260923-sho-SUMMARY.md` ao concluir.
</output>
