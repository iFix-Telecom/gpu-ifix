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
// Superfície de voz. 'realtime' = gpt-realtime-* + tool consultar_atendente (caminho original).
// 'live' = GPT-Live 1 com delegação `client`: o modelo de voz delega e nós respondemos com o cérebro.
// O mesmo INVITE gera `realtime.call.incoming` e `live.transport.incoming`; o 1º accept escolhe a superfície.
const VOICE_MODE = process.env.POC_VOICE_MODE ?? 'realtime'
const LIVE_MODEL = process.env.POC_LIVE_MODEL ?? 'gpt-live-1'
const LIVE_VOICE = process.env.POC_LIVE_VOICE ?? 'marin'
// store=true guarda a sessão para baixar a gravação (GET /v1/live/sessions/{id}/content) e comparar de ouvido.
const LIVE_STORE = (process.env.POC_LIVE_STORE ?? 'true') === 'true'
// Delegação chega antes da transcrição terminar: espera a entrada ficar quieta antes de montar o histórico.
const LIVE_TRANSCRIPT_QUIET_MS = Number(process.env.POC_LIVE_TRANSCRIPT_QUIET_MS ?? 350)
const LIVE_TRANSCRIPT_MAX_WAIT_MS = Number(process.env.POC_LIVE_TRANSCRIPT_MAX_WAIT_MS ?? 1500)
// Cérebro do modo live = o mesmo da ponte Gemini (comparação justa): direto no OpenRouter, cadeia por tok/s
// com provedor fixado. Formato: modelo[@Provedor],...
const BRAIN_BASE_URL = process.env.BRAIN_BASE_URL ?? 'https://openrouter.ai/api/v1'
const BRAIN_API_KEY = process.env.BRAIN_API_KEY ?? ''
const BRAIN_CHAIN = (process.env.BRAIN_CHAIN ?? 'openai/gpt-oss-120b@Cerebras,openai/gpt-oss-safeguard-20b@Groq,google/gemini-3.1-flash-lite')
  .split(',')
  .map((x) => x.trim())
  .filter(Boolean)
  .map((x) => {
    const [model, provider] = x.split('@')
    return { model, provider }
  })
const BRAIN_REASONING_EFFORT = process.env.BRAIN_REASONING_EFFORT ?? 'low'
const BRAIN_ATTEMPT_TIMEOUT_MS = Number(process.env.BRAIN_ATTEMPT_TIMEOUT_MS ?? 4000)
const GREETING = process.env.POC_GREETING ?? 'on'
// Detecção de fim de turno. server_vad: silêncio (ms) antes de considerar que o cliente terminou.
const TURN_MODE = process.env.POC_TURN_MODE ?? 'server_vad'
const VAD_SILENCE_MS = Number(process.env.POC_VAD_SILENCE_MS ?? 300)
const VAD_EAGERNESS = (process.env.POC_VAD_EAGERNESS ?? 'high') as 'low' | 'medium' | 'high' | 'auto'
// Chamada de saída: a IA ouve o early media do tronco (toque, caixa postal, anúncio da operadora).
// Até ouvir gente, não responde sozinha; anúncio de operadora => desliga.
const OUTBOUND = GREETING === 'off'
const CARRIER_ANNOUNCEMENT =
  /caixa postal|ap[oó]s o sinal|sujeit[ao] [àa] cobran[çc]a|n[ãa]o receber recados|deixe (sua|seu) (mensagem|recado)|n[úu]mero (chamado|discado|que voc[êe] ligou)|fora da [áa]rea|desligado|n[ãa]o (pode|est[áa]) (atender|dispon[íi]vel)|est[áa] ocupado|tente (mais tarde|novamente)|obrigad[ao] por ligar|inexistente|n[ãa]o existe/i

function turnDetection(createResponse: boolean) {
  return TURN_MODE === 'semantic_vad'
    ? { type: 'semantic_vad' as const, eagerness: VAD_EAGERNESS, create_response: createResponse, interrupt_response: true }
    : { type: 'server_vad' as const, silence_duration_ms: VAD_SILENCE_MS, create_response: createResponse, interrupt_response: true }
}

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
  /** Saída: 'pre_answer' até ouvir gente; depois 'live'. Entrada: sempre 'live'. */
  phase: 'pre_answer' | 'live'
  /** Chamada que caiu do Gemini (ponte no Oracle): header SIP X-Poc-Fallback: 1. */
  fallback: boolean
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
  // Google recusa histórico terminando em fala da IA ("Requests ending with a model turn are not supported").
  const hist = state.history.filter((t) => !(t.role === 'assistant' && /^s[óo] um instante\.?$/i.test(t.content.trim())))
  while (hist.length && hist[hist.length - 1].role === 'assistant') hist.pop()
  const messages = [
    { role: 'system', content: `${BRAIN_SYSTEM}\n\nIntenção inferida pelo modelo de voz (pode estar errada, não use como dado): ${intencao}` },
    ...hist.slice(-12),
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
    if (state.fallback) {
      ws.send(
        JSON.stringify({
          type: 'response.create',
          response: {
            instructions:
              'Diga em uma frase curta, em português do Brasil: "Desculpe, tive uma instabilidade na ligação. Pode repetir, por favor?"',
            tool_choice: 'none',
          },
        }),
      )
      return log(callId, 'fallback_greeting')
    }
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
        log(callId, 'user_transcript', { text: ev.transcript, phase: state.phase })
        if (state.phase === 'pre_answer') {
          const text = String(ev.transcript ?? '').trim()
          if (text && CARRIER_ANNOUNCEMENT.test(text)) {
            // Caixa postal / número ocupado / anúncio: não conversa com gravação — encerra.
            log(callId, 'carrier_announcement_hangup', { text })
            client.realtime.calls.hangup(callId).catch((err) => log(callId, 'hangup_fail', { err: String(err) }))
          } else if (text) {
            // Primeira fala humana: liga resposta automática e responde a ela.
            state.phase = 'live'
            state.history.push({ role: 'user', content: text })
            ws.send(JSON.stringify({ type: 'session.update', session: { type: 'realtime', audio: { input: { turn_detection: turnDetection(true) } } } }))
            ws.send(JSON.stringify({ type: 'response.create' }))
            log(callId, 'human_detected')
          }
          state.resolveTranscript?.()
          state.transcriptPending = undefined
          state.resolveTranscript = undefined
          break
        }
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
  const isFallback = sipHeaders.some((h) => h.name.toLowerCase() === 'x-poc-fallback' && h.value.trim() === '1')
  const state: CallState = {
    startedAt: Date.now(),
    awaitingFirstAudio: false,
    history: [],
    phase: OUTBOUND && !isFallback ? 'pre_answer' : 'live',
    fallback: isFallback,
  }
  calls.set(callId, state)
  try {
    await client.realtime.calls.accept(callId, {
      type: 'realtime',
      model: MODEL,
      instructions: INSTRUCTIONS,
      audio: {
        input: {
          transcription: { model: TRANSCRIBE_MODEL, language: 'pt', prompt: TRANSCRIBE_PROMPT },
          turn_detection: turnDetection(!OUTBOUND || isFallback),
          ...(NOISE_REDUCTION === 'off' ? {} : { noise_reduction: { type: NOISE_REDUCTION as 'near_field' | 'far_field' } }),
        },
        output: { voice: VOICE },
      },
      ...(BACKEND === 'gateway' ? { tools: TOOLS, tool_choice: 'auto' as const } : {}),
    })
    log(callId, 'accepted', { fallback: isFallback, model: MODEL, voice: VOICE, transcribe: TRANSCRIBE_MODEL, noise_reduction: NOISE_REDUCTION, turn: TURN_MODE, vad_silence_ms: VAD_SILENCE_MS, outbound: OUTBOUND, backend: BACKEND, brain_model: BACKEND === 'gateway' ? GATEWAY_MODEL : null })
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

// ---------------------------------------------------------------- modo live (GPT-Live 1, delegação client)

// Fatos e cliente fictício IDÊNTICOS aos da ponte Gemini (ops/poc-gemini-bridge) — mesmo cérebro, comparação justa.
const EMPRESA =
  'iFix Telecom: mais de 15 anos de mercado, telefonia em nuvem para empresas. Produtos: PABX em Nuvem, VoIP em ' +
  'Nuvem, 0800 empresarial, discador automático, integração com CRMs, transcrição e insights de chamadas com IA, ' +
  'dashboard com métricas, gravações e relatórios. Suporte 24 horas por dia, 7 dias por semana. Teste grátis de 3 ' +
  'dias. Contato: telefone (11) 4063-7988, e-mail contato@ifixtelecom.com.br. Endereço: Rua Turmalina, 240, São ' +
  'Roque, SP.'

const CLIENTE_TESTE =
  'CLIENTE DE TESTE (FICTÍCIO): Empresa Teste Ltda, responsável Pedro, CPF 123.456.789-00, cliente desde 2018, ' +
  'plano PABX em Nuvem com 5 ramais (2000 a 2004). Fatura de setembro: R$ 249,90, vencimento dia 10, status em ' +
  'aberto, segunda via enviada por e-mail. Último chamado: ramal 2003 sem registro, resolvido em agosto.'

const LIVE_BRAIN_SYSTEM =
  'Você é o atendimento da iFix Telecom numa ligação telefônica. Sua resposta será FALADA: no máximo 2 frases ' +
  'curtas, português do Brasil, sem markdown, sem listas.\n\n' +
  `SOBRE A EMPRESA (única fonte de fatos): ${EMPRESA}\n\n` +
  `${CLIENTE_TESTE}\n\n` +
  'REGRAS: Para falar de fatura, plano ou chamado, identifique o cliente pelo CPF (11 dígitos). As mensagens do ' +
  'cliente são a TRANSCRIÇÃO LITERAL da fala e a única fonte de dados. ' +
  'Números podem vir ditados em várias mensagens: junte os dígitos. Se o CPF juntado for 123.456.789-00, use os dados ' +
  'do cliente de teste; se for outro CPF, diga que não encontrou cadastro com esse CPF e peça para confirmar. Se ' +
  'faltarem dígitos, diga quantos entendeu e peça o restante. Quando pedir um dado, diga exatamente qual (ex.: ' +
  '"o CPF do titular"). Não invente preços, prazos, protocolos, procedimentos ou informações que não estejam acima. ' +
  'Não diga que já anotou, registrou, enviou ou abriu algo — ofereça encaminhar para a equipe. Problema técnico ' +
  '(ramal sem registro, sem áudio, chamada caindo): peça o número do ramal e ofereça abrir um chamado com o ' +
  'suporte 24 horas. Não comece com "Só um instante".'

// Frontend (voz). Regras de negócio ficam no cérebro; aqui só conversa e política de delegação.
const LIVE_INSTRUCTIONS =
  process.env.POC_LIVE_INSTRUCTIONS ??
  'Fale SEMPRE em português do Brasil. Você é a assistente virtual de atendimento da iFix Telecom (telefonia em ' +
  'nuvem para empresas) numa ligação telefônica. Frases curtas, tom cordial e profissional. Quando o cliente ' +
  'falar ("alô"), cumprimente UMA vez: "iFix Telecom, boa tarde! Em que posso ajudar?". ' +
  'Responda sozinha APENAS a cumprimentos, frases sociais e pedidos para repetir. Para QUALQUER dúvida, pedido, ' +
  'informação sobre a empresa, dado de cliente, fatura, plano, CPF ou problema técnico, delegue — você não sabe ' +
  'nenhum fato sobre a empresa nem sobre o cliente. Nunca invente valores, datas, protocolos ou dados. ' +
  'Enquanto espera o resultado, não narre ações ("estou verificando", "gerando o link"). Quando o resultado ' +
  'chegar, fale fielmente, sem acrescentar fatos, e nunca mencione sistema, equipe consultada ou ferramenta.'

type LiveCall = {
  sessionId: string
  startedAt: number
  ws?: WebSocket
  timer?: Timer
  history: Turn[]
  userBuf: string
  aiBuf: string
  lastInputDeltaAt: number
  /** Fim da última fala do cliente (end_ms da transcrição, relógio da sessão) e quando chegou aqui. */
  lastUserEndMs?: number
  lastUserAt?: number
  awaitingFirstOutput: boolean
  /** delegation_id -> instante em que foi criada (latência delegação → fala do resultado). */
  pendingDelegations: Map<string, number>
  commentaryAt?: number
  ended: boolean
}

const liveCalls = new Map<string, LiveCall>()
// Dedup de entrega do webhook (header webhook-id).
const seenWebhookIds = new Set<string>()

function flushLiveUser(call: LiveCall) {
  const text = call.userBuf.trim()
  call.userBuf = ''
  if (!text) return
  call.history.push({ role: 'user', content: text })
  log(call.sessionId, 'user_transcript', { text })
  if (CARRIER_ANNOUNCEMENT.test(text) && !call.history.some((t) => t.role === 'assistant')) {
    log(call.sessionId, 'carrier_announcement_hangup', { text })
    client.live.sessions.hangup(call.sessionId).catch((e) => log(call.sessionId, 'hangup_fail', { err: String(e) }))
  }
}

function flushLiveAi(call: LiveCall) {
  const text = call.aiBuf.trim()
  call.aiBuf = ''
  if (!text) return
  call.history.push({ role: 'assistant', content: text })
  log(call.sessionId, 'ai_transcript', { text })
}

/** Cadeia do cérebro (OpenRouter, provedor fixado). Devolve texto para a voz falar. */
async function askBrainChain(call: LiveCall): Promise<string> {
  const t0 = Date.now()
  // Google recusa histórico terminando em fala da IA ("Requests ending with a model turn are not supported").
  const hist = [...call.history]
  while (hist.length && hist[hist.length - 1].role === 'assistant') hist.pop()
  const messages = [{ role: 'system', content: LIVE_BRAIN_SYSTEM }, ...hist.slice(-12)]
  for (const { model, provider } of BRAIN_CHAIN) {
    const ta = Date.now()
    try {
      const res = await fetch(`${BRAIN_BASE_URL}/chat/completions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${BRAIN_API_KEY}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({
          model,
          messages,
          max_tokens: 800,
          temperature: 0.3,
          provider: provider ? { order: [provider], allow_fallbacks: false } : { sort: 'latency' },
          ...(model.startsWith('openai/gpt-oss') ? { reasoning: { effort: BRAIN_REASONING_EFFORT } } : {}),
        }),
        signal: AbortSignal.timeout(BRAIN_ATTEMPT_TIMEOUT_MS),
      })
      const data = (await res.json()) as {
        provider?: string
        error?: unknown
        model?: string
        choices?: Array<{ finish_reason?: string; message?: { content?: string } }>
      }
      const c = data.choices?.[0]
      let text = c?.message?.content?.trim()
      if (text && c?.finish_reason === 'length') {
        const end = Math.max(text.lastIndexOf('.'), text.lastIndexOf('!'), text.lastIndexOf('?'))
        text = end > 0 ? text.slice(0, end + 1) : undefined
      }
      if (res.ok && text) {
        log(call.sessionId, 'brain_done', { ms: Date.now() - t0, attempt_ms: Date.now() - ta, model, provider: data.provider, finish_reason: c?.finish_reason, text })
        return text
      }
      log(call.sessionId, 'brain_attempt_fail', { model, provider, ms: Date.now() - ta, http: res.status, err: JSON.stringify(data.error ?? '').slice(0, 200) })
    } catch (e) {
      log(call.sessionId, 'brain_attempt_fail', { model, provider, ms: Date.now() - ta, err: String(e).slice(0, 200) })
    }
  }
  log(call.sessionId, 'brain_fail', { ms: Date.now() - t0 })
  return 'Tive uma instabilidade para consultar essa informação agora. Posso encaminhar seu pedido para a equipe retornar.'
}

async function handleDelegation(call: LiveCall, ws: WebSocket, delegationId: string) {
  // Espera a transcrição da fala que gerou a delegação assentar (sem atividade por QUIET_MS, teto MAX_WAIT_MS).
  const t0 = Date.now()
  while (Date.now() - t0 < LIVE_TRANSCRIPT_MAX_WAIT_MS && Date.now() - call.lastInputDeltaAt < LIVE_TRANSCRIPT_QUIET_MS) {
    await Bun.sleep(50)
  }
  flushLiveAi(call)
  flushLiveUser(call)
  log(call.sessionId, 'transcript_wait', { ms: Date.now() - t0 })
  const output = await askBrainChain(call)
  if (ws.readyState !== WebSocket.OPEN) return log(call.sessionId, 'commentary_dropped', { reason: 'ws_closed' })
  call.commentaryAt = Date.now()
  call.awaitingFirstOutput = true
  ws.send(JSON.stringify({ type: 'session.commentary.append', event_id: `res_${delegationId}`, delegation_id: delegationId, content: output }))
  log(call.sessionId, 'commentary_sent', { delegation_id: delegationId, since_delegation_ms: Date.now() - (call.pendingDelegations.get(delegationId) ?? t0) })
  call.pendingDelegations.delete(delegationId)
}

function endLiveCall(call: LiveCall, reason: string) {
  if (call.ended) return
  call.ended = true
  flushLiveUser(call)
  flushLiveAi(call)
  clearTimeout(call.timer)
  log(call.sessionId, 'live_end', { reason, duration_s: Math.round((Date.now() - call.startedAt) / 1000) })
  liveCalls.delete(call.sessionId)
}

function attachLive(call: LiveCall) {
  const ws = new WebSocket(`wss://api.openai.com/v1/live/sessions/${call.sessionId}/attach`, {
    headers: { Authorization: `Bearer ${process.env.OPENAI_API_KEY}`, 'OpenAI-Project': process.env.OPENAI_PROJECT_ID ?? '' },
  } as unknown as string[])
  call.ws = ws
  ws.onopen = () => log(call.sessionId, 'ws_open')
  ws.onmessage = (msg) => {
    const ev = JSON.parse(String(msg.data)) as { type: string; [k: string]: unknown }
    const now = Date.now()
    switch (ev.type) {
      case 'session.output_audio.delta':
        // Sideband não deveria receber áudio; se vier, não loga cada pacote.
        break
      case 'session.input_transcript.delta': {
        // Cliente falando de novo: fecha o que a IA estava dizendo.
        if (call.aiBuf) flushLiveAi(call)
        call.userBuf += String(ev.delta ?? '')
        call.lastInputDeltaAt = now
        if (typeof ev.end_ms === 'number') call.lastUserEndMs = ev.end_ms
        call.lastUserAt = now
        call.awaitingFirstOutput = true
        break
      }
      case 'session.output_transcript.delta': {
        if (call.userBuf) flushLiveUser(call)
        if (call.awaitingFirstOutput) {
          call.awaitingFirstOutput = false
          // Proxy de latência: última transcrição de entrada → 1º texto de saída (o áudio sai junto).
          log(call.sessionId, 'first_output', {
            since_last_input_delta_ms: call.lastUserAt ? now - call.lastUserAt : null,
            since_commentary_ms: call.commentaryAt ? now - call.commentaryAt : null,
            start_ms: ev.start_ms,
            last_user_end_ms: call.lastUserEndMs,
          })
          call.commentaryAt = undefined
        }
        call.aiBuf += String(ev.delta ?? '')
        break
      }
      case 'session.delegation.created': {
        const d = ev.delegation as { id: string; target?: string }
        call.pendingDelegations.set(d.id, now)
        log(call.sessionId, 'delegation', { id: d.id, target: d.target, offset_ms: ev.offset_ms, user_buf: call.userBuf })
        void handleDelegation(call, ws, d.id)
        break
      }
      case 'session.closed':
        log(call.sessionId, 'session_closed', { ev })
        endLiveCall(call, 'session_closed')
        break
      case 'error':
        log(call.sessionId, 'error', { error: ev.error })
        break
      default:
        // Primeira rodada com GPT-Live: registra tudo o mais (transport.*, usage, appended, info) p/ análise.
        log(call.sessionId, 'ev', { type: ev.type, body: JSON.stringify(ev).slice(0, 600) })
    }
  }
  ws.onclose = (e) => {
    log(call.sessionId, 'ws_close', { code: e.code, reason: e.reason })
    if (!call.ended && e.code !== 1000) {
      client.live.sessions.hangup(call.sessionId).catch((err) => log(call.sessionId, 'hangup_fail', { err: String(err) }))
    }
    endLiveCall(call, `ws_close_${e.code}`)
  }
}

async function handleLiveIncoming(sessionId: string, sipHeaders: Array<{ name: string; value: string }>) {
  const user = fromUser(sipHeaders)
  if (!CALLER_TOKEN || user !== CALLER_TOKEN) {
    log(sessionId, 'reject_caller', { from_user: user })
    await client.live.sessions.reject(sessionId, { status_code: 603 }).catch((e) => log(sessionId, 'reject_fail', { err: String(e) }))
    return
  }
  if (liveCalls.size + calls.size >= MAX_CONCURRENT) {
    log(sessionId, 'reject_busy', { active: liveCalls.size + calls.size })
    await client.live.sessions.reject(sessionId, { status_code: 486 }).catch((e) => log(sessionId, 'reject_fail', { err: String(e) }))
    return
  }
  const call: LiveCall = {
    sessionId,
    startedAt: Date.now(),
    history: [],
    userBuf: '',
    aiBuf: '',
    lastInputDeltaAt: 0,
    awaitingFirstOutput: false,
    pendingDelegations: new Map(),
    ended: false,
  }
  liveCalls.set(sessionId, call)
  try {
    await client.live.sessions.accept(sessionId, {
      session: {
        type: 'live',
        model: LIVE_MODEL,
        instructions: LIVE_INSTRUCTIONS,
        audio: { output: { voice: LIVE_VOICE } },
        delegation: { type: 'client' },
        store: LIVE_STORE,
      },
    })
    log(sessionId, 'accepted', { surface: 'live', model: LIVE_MODEL, voice: LIVE_VOICE, store: LIVE_STORE, brain_chain: BRAIN_CHAIN })
  } catch (e) {
    log(sessionId, 'accept_fail', { err: String(e) })
    liveCalls.delete(sessionId)
    return
  }
  call.timer = setTimeout(() => {
    log(sessionId, 'max_duration_hangup', { max_s: MAX_CALL_SECONDS })
    client.live.sessions.hangup(sessionId).catch((e) => log(sessionId, 'hangup_fail', { err: String(e) }))
  }, MAX_CALL_SECONDS * 1000)
  attachLive(call)
}

Bun.serve({
  port: PORT,
  async fetch(req) {
    const url = new URL(req.url)
    if (req.method === 'GET' && url.pathname === '/health') {
      return Response.json({
        ok: true,
        secret_configured: Boolean(WEBHOOK_SECRET),
        caller_token_configured: Boolean(CALLER_TOKEN),
        voice_mode: VOICE_MODE,
        active_calls: calls.size + liveCalls.size,
        model: VOICE_MODE === 'live' ? LIVE_MODEL : MODEL,
        backend: BACKEND,
        brain: VOICE_MODE === 'live' ? { chain: BRAIN_CHAIN, key_configured: Boolean(BRAIN_API_KEY) } : GATEWAY_MODEL,
      })
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
    const webhookId = req.headers.get('webhook-id')
    if (webhookId) {
      if (seenWebhookIds.has(webhookId)) return new Response('ok')
      seenWebhookIds.add(webhookId)
      if (seenWebhookIds.size > 1000) seenWebhookIds.clear()
    }
    // O mesmo INVITE chega pelas duas superfícies; só a do modo atual decide (1º accept vence).
    if (event.type === 'live.transport.incoming' || event.type === 'live.call.incoming') {
      if (VOICE_MODE !== 'live') {
        log(event.data.session_id, 'ignored_event', { type: event.type, voice_mode: VOICE_MODE })
        return new Response('ok')
      }
      log(event.data.session_id, 'incoming', { type: event.type, sip_headers: event.data.sip_headers, media: event.data.sip_media_security })
      void handleLiveIncoming(event.data.session_id, event.data.sip_headers)
    } else if (event.type === 'realtime.call.incoming' && VOICE_MODE === 'live') {
      log(event.data.call_id, 'ignored_event', { type: event.type, voice_mode: VOICE_MODE })
    } else if (event.type === 'realtime.call.incoming') {
      log(event.data.call_id, 'incoming', { sip_headers: event.data.sip_headers })
      // Responde 200 já; aceite roda assíncrono.
      void handleIncoming(event.data.call_id, event.data.sip_headers)
    } else {
      log(null, 'ignored_event', { type: event.type })
    }
    return new Response('ok')
  },
})

log(null, 'listening', { port: PORT, voice_mode: VOICE_MODE, model: VOICE_MODE === 'live' ? LIVE_MODEL : MODEL, secret_configured: Boolean(WEBHOOK_SECRET) })
