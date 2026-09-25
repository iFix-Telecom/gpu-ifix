/**
 * PoC SEED-009 — webhook OpenAI Realtime SIP.
 *
 * Recebe `realtime.call.incoming`, valida assinatura, filtra quem chama
 * (From user = POC_CALLER_TOKEN, setado como `from_user` no endpoint PJSIP),
 * aceita com gpt-realtime-2.1-mini e grava eventos com timestamp para medir
 * latência de turno e custo por ligação.
 *
 * Travas de custo: 1 chamada simultânea, teto de duração, fail-closed sem secret.
 *
 * @module poc-voz-realtime
 */
import OpenAI from 'openai'
import { appendFileSync, mkdirSync } from 'node:fs'

const PORT = Number(process.env.PORT ?? 8099)
const WEBHOOK_SECRET = process.env.OPENAI_WEBHOOK_SECRET ?? ''
const CALLER_TOKEN = process.env.POC_CALLER_TOKEN ?? ''
const MODEL = process.env.POC_MODEL ?? 'gpt-realtime-2.1-mini'
const VOICE = process.env.POC_VOICE ?? 'marin'
const MAX_CONCURRENT = Number(process.env.POC_MAX_CONCURRENT ?? 1)
const MAX_CALL_SECONDS = Number(process.env.POC_MAX_CALL_SECONDS ?? 720)
const LOG_DIR = process.env.POC_LOG_DIR ?? '/data/calls'
// Backend de conteúdo: 'gateway' = voz OpenAI consulta nossa LLM via tool; 'none' = modelo de voz responde sozinho.
const BACKEND = process.env.POC_BACKEND ?? 'gateway'
const GATEWAY_BASE_URL = process.env.GATEWAY_BASE_URL ?? 'https://ai-gateway.converse-ai.app/v1'
const GATEWAY_MODEL = process.env.GATEWAY_MODEL ?? 'qwen'
const GATEWAY_TIMEOUT_MS = Number(process.env.GATEWAY_TIMEOUT_MS ?? 12000)
const TOOL_NAME = 'consultar_atendente'
// A transcrição alimenta o cérebro (fonte de verdade dos dados) — qualidade dela importa.
const TRANSCRIBE_MODEL = process.env.POC_TRANSCRIBE_MODEL ?? 'gpt-4o-transcribe'
const TRANSCRIBE_PROMPT =
  process.env.POC_TRANSCRIBE_PROMPT ??
  'Ligação telefônica em português do Brasil com a iFix Telecom (telefonia, VoIP, internet). Termos comuns: ' +
    'fatura, boleto, segunda via, CPF, CNPJ, contrato, ramal, PABX, internet caiu, instabilidade, suporte, ' +
    'protocolo, parcelamento. Números podem ser ditados dígito a dígito.'
const NOISE_REDUCTION = process.env.POC_NOISE_REDUCTION ?? 'near_field'
const GREETING = process.env.POC_GREETING ?? 'on'

const INSTRUCTIONS_SOLO =
  'Você é a assistente de voz da iFix Telecom em uma ligação de TESTE interno. ' +
  'Fale português do Brasil, frases curtas e naturais. Cumprimente e pergunte como pode ajudar. ' +
  'Nunca diga que executou uma ação que não executou. Se não souber, diga que não sabe.'

const INSTRUCTIONS_GATEWAY =
  'Você é SOMENTE a voz da iFix Telecom em uma ligação de TESTE interno, em português do Brasil. ' +
  'Cumprimente e pergunte como pode ajudar. Você pode responder sozinha APENAS a cumprimentos e frases sociais ' +
  '("alô", "tudo bem", "obrigado", "tchau"). Se a fala do cliente NÃO tiver pedido nem pergunta (ex.: "só um instante", ' +
  '"espera", "hã", "tá bom", fala incompleta ou sem sentido), NÃO chame a ferramenta: responda curto ("claro, fico ' +
  'aguardando") ou peça para repetir. Nunca repita a mesma resposta sem o cliente pedir. Se o cliente pedir para ' +
  'repetir ("não entendi", "pode repetir?", "como?"), repita VOCÊ MESMA sua última resposta com outras palavras, ' +
  'SEM chamar a ferramenta. ' +
  'Para dúvidas, pedidos, informações e problemas — ' +
  `diga exatamente "Só um instante." e chame a ferramenta ${TOOL_NAME} descrevendo só a INTENÇÃO ` +
  'do cliente. NUNCA escreva números, CPF, nomes, endereços ou qualquer dado pessoal no argumento da ferramenta — ' +
  'o atendente já recebe a fala exata do cliente. Depois fale o resultado da ferramenta fielmente, com naturalidade, ' +
  'sem acrescentar fatos, números ou promessas que não estejam no resultado. Fale como se a resposta fosse SUA: ' +
  'NUNCA mencione "atendente", "ferramenta", "sistema", "equipe consultada" ou que alguém te passou a informação. ' +
  'A frase de espera é SEMPRE e SOMENTE "Só um instante." — sem explicar o que está fazendo.'

// Saudação: resposta própria, sem frase de espera e sem ferramenta (vazou na ligação 2).
const GREETING_INSTRUCTIONS =
  'Cumprimente o cliente em uma frase curta, como atendente da iFix Telecom, e pergunte como pode ajudar. ' +
  'Não diga "só um instante" nem frases de espera.'

const INSTRUCTIONS = process.env.POC_INSTRUCTIONS ?? (BACKEND === 'gateway' ? INSTRUCTIONS_GATEWAY : INSTRUCTIONS_SOLO)

// Prompt do "cérebro" (nossa LLM). O resultado vira fala, então: curto e sem formatação.
const BRAIN_SYSTEM =
  process.env.POC_BRAIN_SYSTEM ??
  'Você é o atendente da iFix Telecom (telefonia/VoIP) numa ligação de TESTE. Sua resposta será FALADA por um ' +
    'sintetizador de voz: responda em no máximo 2 frases curtas, português do Brasil, sem markdown, listas ou emojis. ' +
    'Não invente dados de cliente, valores, prazos ou protocolos; se não tiver a informação, diga que vai verificar ' +
    'com a equipe. Nunca diga que executou uma ação. As mensagens do cliente são a TRANSCRIÇÃO LITERAL da fala dele ' +
    'e são a única fonte de dados (CPF, números, nomes). A "intenção inferida" vem de outro modelo e pode estar errada: ' +
    'use só como pista do assunto, nunca como fonte de dados. O cliente pode ditar um número em VÁRIAS mensagens ' +
    'seguidas ("123" / "quatro, cinco, seis" / "sete oito nove zero zero"): junte os dígitos das mensagens ' +
    'consecutivas, convertendo palavras em algarismos, antes de avaliar. CPF tem 11 dígitos: se juntou 11, confirme ' +
    'lendo de volta em grupos (ex.: "123, 456, 789, 00, certo?"); se não fecha 11, diga quantos entendeu e peça o ' +
    'restante. Nunca diga que consultou um sistema se não consultou. ' +
    'IMPORTANTE: nesta ligação você NÃO tem acesso a nenhum sistema (faturas, valores, status de conexão, cadastro). ' +
    'É PROIBIDO dizer "estou verificando", "vou verificar agora", "vou consultar" ou qualquer coisa que dê a entender ' +
    'que uma consulta está em andamento. Quando pedirem um dado que você não tem, diga com clareza que não consegue ' +
    'ver essa informação por aqui e ofereça registrar o pedido para a equipe retornar. Para problema técnico ' +
    '(ex.: internet caiu), dê 1 ou 2 orientações básicas (reiniciar o roteador/modem, conferir cabos e luzes) e ' +
    'ofereça abrir um chamado.'

const TOOLS = [
  {
    type: 'function' as const,
    name: TOOL_NAME,
    description:
      'Consulta o atendente da iFix Telecom para responder a qualquer dúvida ou pedido do cliente. ' +
      'O atendente já recebe a fala exata do cliente.',
    parameters: {
      type: 'object',
      properties: {
        intencao: {
          type: 'string',
          description: 'Resumo curto do que o cliente quer, SEM números, CPF ou dados pessoais. Ex.: "consultar valor da fatura".',
        },
      },
      required: ['intencao'],
    },
  },
]

const client = new OpenAI({ apiKey: process.env.OPENAI_API_KEY, project: process.env.OPENAI_PROJECT_ID })

mkdirSync(LOG_DIR, { recursive: true })

type Turn = { role: 'user' | 'assistant'; content: string }
type CallState = {
  startedAt: number
  lastSpeechStop?: number
  awaitingFirstAudio: boolean
  ws?: WebSocket
  timer?: Timer
  history: Turn[]
  /** Transcrição da fala atual ainda pendente (speech_stopped visto, transcrição não). */
  transcriptPending?: Promise<void>
  resolveTranscript?: () => void
}

const TRANSCRIPT_WAIT_MS = Number(process.env.POC_TRANSCRIPT_WAIT_MS ?? 2000)
const calls = new Map<string, CallState>()

function log(callId: string | null, event: string, extra: Record<string, unknown> = {}) {
  const line = JSON.stringify({ ts: new Date().toISOString(), t: Date.now(), call_id: callId, event, ...extra })
  console.log(line)
  if (callId) appendFileSync(`${LOG_DIR}/${callId}.ndjson`, line + '\n')
}

/** Extrai o user part do header From (`sip:<user>@host` ou `"Nome" <sip:user@host>`). */
function fromUser(headers: Array<{ name: string; value: string }>): string | null {
  const from = headers.find((h) => h.name.toLowerCase() === 'from')?.value ?? ''
  const m = from.match(/sips?:([^@;>]+)@/i)
  return m ? m[1] : null
}

/** Pergunta à nossa LLM (ai-gateway) com o histórico da ligação; devolve texto para a voz falar. */
async function askBrain(callId: string, state: CallState, intencao: string): Promise<string> {
  const t0 = Date.now()
  // Histórico = transcrições literais (fonte de verdade). A intenção do modelo de voz entra só como pista.
  const messages = [
    { role: 'system', content: BRAIN_SYSTEM },
    ...state.history.slice(-12),
    { role: 'system', content: `Intenção inferida pelo modelo de voz (pode estar errada, não use como dado): ${intencao}` },
  ]
  try {
    const res = await fetch(`${GATEWAY_BASE_URL}/chat/completions`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${process.env.GATEWAY_API_KEY}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: GATEWAY_MODEL, messages, max_tokens: Number(process.env.GATEWAY_MAX_TOKENS ?? 800), temperature: 0.3 }),
      signal: AbortSignal.timeout(GATEWAY_TIMEOUT_MS),
    })
    const data = (await res.json()) as {
      model?: string
      usage?: unknown
      choices?: Array<{ finish_reason?: string; message?: { content?: string } }>
    }
    const choice = data.choices?.[0]
    let text = choice?.message?.content?.trim()
    // Resposta cortada por limite vira fala pela metade ("Enquanto isso,"): fica só até a última frase completa.
    if (text && choice?.finish_reason === 'length') {
      const end = Math.max(text.lastIndexOf('.'), text.lastIndexOf('!'), text.lastIndexOf('?'))
      text = end > 0 ? text.slice(0, end + 1) : undefined
    }
    log(callId, 'brain_done', {
      ms: Date.now() - t0,
      http: res.status,
      upstream_model: data.model,
      finish_reason: choice?.finish_reason,
      usage: data.usage,
      text,
    })
    if (!res.ok || !text) throw new Error(`gateway http ${res.status} finish=${choice?.finish_reason}`)
    return text
  } catch (e) {
    log(callId, 'brain_fail', { ms: Date.now() - t0, err: String(e) })
    return 'No momento não consegui consultar essa informação. Vou pedir para a equipe retornar.'
  }
}

async function handleToolCall(callId: string, state: CallState, ws: WebSocket, ev: { call_id: string; name: string; arguments: string }) {
  let intencao = ''
  try {
    intencao = (JSON.parse(ev.arguments) as { intencao?: string }).intencao ?? ''
  } catch {
    intencao = ev.arguments
  }
  log(callId, 'tool_call', { name: ev.name, intencao, transcript_pending: Boolean(state.transcriptPending) })
  // Garante que a fala literal do cliente já está no histórico antes de consultar o cérebro.
  if (state.transcriptPending) {
    const t0 = Date.now()
    const got = await Promise.race([
      state.transcriptPending.then(() => true),
      Bun.sleep(TRANSCRIPT_WAIT_MS).then(() => false),
    ])
    log(callId, 'transcript_wait', { ms: Date.now() - t0, got })
  }
  const output = ev.name === TOOL_NAME ? await askBrain(callId, state, intencao) : 'Ferramenta desconhecida.'
  if (ws.readyState !== WebSocket.OPEN) return log(callId, 'tool_output_dropped', { reason: 'ws_closed' })
  ws.send(JSON.stringify({ type: 'conversation.item.create', item: { type: 'function_call_output', call_id: ev.call_id, output } }))
  ws.send(JSON.stringify({ type: 'response.create' }))
  log(callId, 'tool_output_sent')
}

function monitor(callId: string, state: CallState) {
  const ws = new WebSocket(`wss://api.openai.com/v1/realtime?call_id=${callId}`, {
    headers: { Authorization: `Bearer ${process.env.OPENAI_API_KEY}`, 'OpenAI-Project': process.env.OPENAI_PROJECT_ID ?? '' },
  } as unknown as string[])
  state.ws = ws
  ws.onopen = () => {
    log(callId, 'ws_open')
    // Chamada de saída (IA disca o cliente): a IA atende antes do cliente — sem saudação,
    // espera o "alô" dele. POC_GREETING=off.
    if (GREETING === 'off') return log(callId, 'greeting_skipped')
    ws.send(
      JSON.stringify({
        type: 'response.create',
        response: BACKEND === 'gateway' ? { instructions: GREETING_INSTRUCTIONS, tool_choice: 'none' } : {},
      }),
    )
  }
  ws.onmessage = (msg) => {
    const ev = JSON.parse(String(msg.data)) as { type: string; [k: string]: unknown }
    const now = Date.now()
    switch (ev.type) {
      case 'input_audio_buffer.speech_started':
        log(callId, 'speech_started')
        break
      case 'input_audio_buffer.speech_stopped':
        state.lastSpeechStop = now
        state.awaitingFirstAudio = true
        state.transcriptPending = new Promise<void>((r) => {
          state.resolveTranscript = r
        })
        log(callId, 'speech_stopped')
        break
      case 'response.output_audio.delta':
      case 'response.output_audio_transcript.delta':
        // O WebSocket de monitoramento (?call_id=) não recebe o áudio em si; o 1º delta
        // da transcrição do áudio da IA é o proxy mais cedo de "IA começou a falar".
        if (state.awaitingFirstAudio && state.lastSpeechStop) {
          log(callId, 'turn_latency', { ms: now - state.lastSpeechStop, via: ev.type })
          state.awaitingFirstAudio = false
        }
        break
      case 'conversation.item.input_audio_transcription.completed':
        log(callId, 'user_transcript', { text: ev.transcript })
        if (ev.transcript) state.history.push({ role: 'user', content: String(ev.transcript) })
        state.resolveTranscript?.()
        state.transcriptPending = undefined
        state.resolveTranscript = undefined
        break
      case 'response.output_audio_transcript.done':
        log(callId, 'ai_transcript', { text: ev.transcript })
        if (ev.transcript) state.history.push({ role: 'assistant', content: String(ev.transcript) })
        break
      case 'response.function_call_arguments.done':
        void handleToolCall(callId, state, ws, ev as unknown as { call_id: string; name: string; arguments: string })
        break
      case 'response.done': {
        const r = ev.response as { status?: string; usage?: unknown } | undefined
        log(callId, 'response_done', { status: r?.status, usage: r?.usage })
        break
      }
      case 'error':
        log(callId, 'error', { error: ev.error })
        break
    }
  }
  ws.onclose = (e) => {
    const elapsed = Math.round((Date.now() - state.startedAt) / 1000)
    log(callId, 'ws_close', { code: e.code, duration_s: elapsed })
    // Queda do monitoramento NÃO encerra a chamada SIP (medido na 1ª ligação: 1006 aos 92 s
    // com os canais ainda Up). Mantém a trava de duração e manda hangup explícito.
    if (e.code !== 1000) {
      log(callId, 'ws_abnormal_close_hangup')
      client.realtime.calls.hangup(callId).catch((err) => log(callId, 'hangup_fail', { err: String(err) }))
    }
    calls.delete(callId)
  }
}

async function handleIncoming(callId: string, sipHeaders: Array<{ name: string; value: string }>) {
  const user = fromUser(sipHeaders)
  if (!CALLER_TOKEN || user !== CALLER_TOKEN) {
    log(callId, 'reject_caller', { from_user: user })
    await client.realtime.calls.reject(callId, { status_code: 603 }).catch((e) => log(callId, 'reject_fail', { err: String(e) }))
    return
  }
  if (calls.size >= MAX_CONCURRENT) {
    log(callId, 'reject_busy', { active: calls.size })
    await client.realtime.calls.reject(callId, { status_code: 486 }).catch((e) => log(callId, 'reject_fail', { err: String(e) }))
    return
  }
  const state: CallState = { startedAt: Date.now(), awaitingFirstAudio: false, history: [] }
  calls.set(callId, state)
  try {
    await client.realtime.calls.accept(callId, {
      type: 'realtime',
      model: MODEL,
      instructions: INSTRUCTIONS,
      audio: {
        input: {
          transcription: { model: TRANSCRIBE_MODEL, language: 'pt', prompt: TRANSCRIBE_PROMPT },
          ...(NOISE_REDUCTION === 'off' ? {} : { noise_reduction: { type: NOISE_REDUCTION as 'near_field' | 'far_field' } }),
        },
        output: { voice: VOICE },
      },
      ...(BACKEND === 'gateway' ? { tools: TOOLS, tool_choice: 'auto' as const } : {}),
    })
    log(callId, 'accepted', { model: MODEL, voice: VOICE, transcribe: TRANSCRIBE_MODEL, noise_reduction: NOISE_REDUCTION, backend: BACKEND, brain_model: BACKEND === 'gateway' ? GATEWAY_MODEL : null })
  } catch (e) {
    log(callId, 'accept_fail', { err: String(e) })
    calls.delete(callId)
    return
  }
  state.timer = setTimeout(() => {
    log(callId, 'max_duration_hangup', { max_s: MAX_CALL_SECONDS })
    client.realtime.calls.hangup(callId).catch((e) => log(callId, 'hangup_fail', { err: String(e) }))
  }, MAX_CALL_SECONDS * 1000)
  monitor(callId, state)
}

Bun.serve({
  port: PORT,
  async fetch(req) {
    const url = new URL(req.url)
    if (req.method === 'GET' && url.pathname === '/health') {
      return Response.json({ ok: true, secret_configured: Boolean(WEBHOOK_SECRET), caller_token_configured: Boolean(CALLER_TOKEN), active_calls: calls.size, model: MODEL, backend: BACKEND, brain_model: GATEWAY_MODEL })
    }
    if (req.method !== 'POST' || url.pathname !== '/webhook') return new Response('not found', { status: 404 })
    // Fail-closed: sem secret nenhuma chamada é aceita.
    if (!WEBHOOK_SECRET) return new Response('webhook secret not configured', { status: 503 })

    const body = await req.text()
    let event: Awaited<ReturnType<typeof client.webhooks.unwrap>>
    try {
      event = await client.webhooks.unwrap(body, req.headers, WEBHOOK_SECRET)
    } catch (e) {
      log(null, 'bad_signature', { err: String(e) })
      return new Response('invalid signature', { status: 400 })
    }
    if (event.type === 'realtime.call.incoming') {
      log(event.data.call_id, 'incoming', { sip_headers: event.data.sip_headers })
      // Responde 200 já; aceite roda assíncrono.
      void handleIncoming(event.data.call_id, event.data.sip_headers)
    } else {
      log(null, 'ignored_event', { type: event.type })
    }
    return new Response('ok')
  },
})

log(null, 'listening', { port: PORT, model: MODEL, secret_configured: Boolean(WEBHOOK_SECRET) })
