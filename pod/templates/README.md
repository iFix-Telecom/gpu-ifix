# Chat Templates

Jinja templates usados pelo `llama-server` para formatação de prompts (tool-calling, thinking mode, system/user/assistant/tool roles).

## Arquivos

| Arquivo | Descrição |
|---|---|
| `qwen3.5-27b-tool-calling.jinja` | Template patched pela comunidade para Qwen 3.5 27B. Corrige o bug de rejeição do role `developer` e preserva o thinking mode |
| `qwen3.5-27b-tool-calling.jinja.sha256` | SHA-256 do template — detecta drift em CI e smoke-test |

## Por que este template existe (D-14)

O template stock que vem com o GGUF `unsloth/Qwen3.5-27B-GGUF` rejeita o role `developer` quando o cliente OpenAI-compatible envia uma mensagem do tipo (comportamento observado em agents que usam a convenção moderna da OpenAI). O workaround óbvio de trocar para `--chat-template chatml` desabilita silenciosamente o **thinking mode** do Qwen 3.5, degradando a qualidade das respostas em tasks complexas e quebrando tool-calling estruturado.

O template da comunidade (gist `sudoingX/c2facf7d8f7608c65c1024ef3b22d431`):

- Aceita o role `developer` sem raise.
- Preserva a semântica de thinking mode (blocos `<think>...</think>`).
- Emite `tool_calls` no shape estruturado da OpenAI (não string-interpolado no `content`).

O smoke-test valida esse último ponto via função sintética `get_weather` (D-15). Se o shape do tool-call derivar, `tool_call_valid == false` e a imagem não é promovida a tag estável.

## Como o template é consumido

O template é carregado pelo `llama-server` (binário nativo, imagem `ghcr.io/ggml-org/llama.cpp:server-cuda`) via o flag `--chat-template-file`:

```
--jinja --chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja
```

A wiring é feita em `pod/docker-compose.yml` (plano 03), onde o container `llama` recebe o flag no `command:` e monta o diretório `pod/templates/` como `/app/templates/` via `COPY` no `pod/Dockerfile`.

## Revisão upstream (D-16)

Revisar este template a cada release major do Qwen/unsloth:

1. Conferir o template stock em `unsloth/Qwen3.5-27B-GGUF` (HuggingFace) — verificar se o bug de role `developer` foi consertado.
2. Rodar `pod/smoke/smoke.py` com o template stock em um pod Vast.ai de teste (via `workflow_dispatch` do `smoke.yml`).
3. Se `tool_call_valid == true` e `errors == []`, migrar: substituir este arquivo pelo stock, recomputar o SHA-256, atualizar o header de proveniência e o `CHANGELOG` deste diretório.

## Substituição do template

Quando precisar substituir o template (upstream consertou o bug, ou gist mudou e foi re-validado):

```bash
# 1. Baixar novo template (troque <url> pela URL raw correta)
curl -sSL "<url>" -o pod/templates/qwen3.5-27b-tool-calling.jinja.new

# 2. Escrever manualmente um header Jinja block-comment ({# ... #}) com a mesma estrutura
#    do arquivo atual (Source, Fetched, Purpose, Provenance, Review cadence, Validation).
#    Depois anexar o conteúdo da gist:
#    cat novo-header.txt pod/templates/qwen3.5-27b-tool-calling.jinja.new > pod/templates/qwen3.5-27b-tool-calling.jinja
#    rm pod/templates/qwen3.5-27b-tool-calling.jinja.new

# 3. Recomputar o digest e regravar o sidecar
sha256sum pod/templates/qwen3.5-27b-tool-calling.jinja | awk '{print $1}' > pod/templates/qwen3.5-27b-tool-calling.jinja.sha256

# 4. Rodar o smoke-test via workflow_dispatch no GitHub Actions (.github/workflows/smoke.yml)
#    e validar os gates D-19 (tool_call_valid == true, errors == [])

# 5. Commit
git add pod/templates/qwen3.5-27b-tool-calling.jinja pod/templates/qwen3.5-27b-tool-calling.jinja.sha256
git commit -m "chore(templates): update Qwen 3.5 27B template to <source>"
```

## Validação (D-15, D-19)

O smoke-test (`pod/smoke/smoke.py`) executa uma chamada sintética de tool-calling (`get_weather`) e grava `tool_call_valid` em `pod/smoke/smoke-report.json`. O workflow `.github/workflows/smoke.yml` lê esse campo e bloqueia a promoção da imagem a tag estável quando `tool_call_valid == false` (gate D-19).

Se o smoke falhar:

1. Ler `errors[]` em `pod/smoke/smoke-report.json` — procurar por mensagens como `invalid tool_call shape`, `developer role rejected`, ou falhas de parsing Jinja.
2. Conferir que o SHA-256 do template em disco (no pod Vast.ai) bate com `pod/templates/qwen3.5-27b-tool-calling.jinja.sha256` do repo: `sha256sum /app/templates/qwen3.5-27b-tool-calling.jinja` dentro do container `llama`. Se divergir, há tampering no caminho de build ou a imagem não foi reconstruída após troca do template — rebuildar via `.github/workflows/build-pod.yml`.
3. Se o digest bate mas o teste ainda falha (ou seja, o template do gist não funciona com a versão atual de `llama.cpp` + GGUF): o fallback documentado é a trilha **"fork própria"** registrada em `.planning/phases/01-gpu-pod-image-smoke-test/01-CONTEXT.md` `<specifics>` — abrir issue e escalar para o usuário antes de patchear em cima.
