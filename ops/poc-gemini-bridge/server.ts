/**
 * PoC SEED-009 — ponte Asterisk (ARI externalMedia slin16) ↔ Gemini Live (WebSocket).
 *
 * Mesmo padrão do tronco WhatsApp do voip-api (services/whatsapp/media-bridge.ts):
 * o Asterisk converte G.711 → PCM 16 kHz (slin16, s16be no fio) e manda RTP/UDP; a
 * ponte repassa ao Gemini (PCM 16 kHz s16le em base64) e devolve o áudio dele
 * (PCM 24 kHz s16le) reamostrado para 16 kHz, em frames de 20 ms.
 *
 * App ARI próprio (`poc-gemini`): não entra no Stasis do voip-api, não muda dialplan.
 * Chamada de saída: ARI originate no tronco COM callerId (canal sem caller ID sai
 * anônimo e a operadora desvia/bloqueia — medido na PoC OpenAI). O canal só entra
 * no Stasis quando o destino ATENDE ⇒ a IA nunca ouve toque/early media.
 *
 * Travas: 1 chamada por vez, hangup em POC_MAX_CALL_SECONDS, anúncio de operadora
 * (caixa postal etc.) ⇒ hangup. Controle HTTP só em 127.0.0.1.
 *
 * @module poc-gemini-bridge
 */
import { createSocket, type Socket } from 'node:dgram'
import { appendFileSync, mkdirSync } from 'node:fs'
import { Resampler, swap16 } from './audio'

const env = (k: string, d?: string) => {
  const v = process.env[k] ?? d
  if (v === undefined) throw new Error(`env ${k} obrigatória`)
  return v
}

const ARI_URL = env('ARI_URL') // http://10.0.0.82:8089
const ARI_AUTH = `${env('ARI_USER')}:${env('ARI_PASS')}`
const ARI_APP = env('ARI_APP', 'poc-gemini')
const GOOGLE_API_KEY = env('GOOGLE_API_KEY', '')
const GEMINI_MODEL = env('GEMINI_MODEL', 'gemini-3.8-live')
const GEMINI_VOICE = env('GEMINI_VOICE', 'Kore')
const GEMINI_LANGUAGE = env('GEMINI_LANGUAGE', 'pt-BR')
const VAD_SILENCE_MS = Number(env('POC_VAD_SILENCE_MS', '300'))
const TRUNK_ENDPOINT = env('POC_TRUNK_ENDPOINT', 'trunk_2b137c12')
const TRUNK_PREFIX = env('POC_TRUNK_PREFIX', '0983489#')
const CALLER_ID = env('POC_CALLER_ID', 'IA PoC <9990>')
const RTP_BIND = env('POC_RTP_BIND', '127.0.0.1')
const CONTROL_PORT = Number(env('POC_CONTROL_PORT', '8110'))
const MAX_CALL_SECONDS = Number(env('POC_MAX_CALL_SECONDS', '720'))
const LOG_DIR = env('POC_LOG_DIR', '/data/calls')
const GATEWAY_BASE_URL = env('GATEWAY_BASE_URL', 'https://ai-gateway.converse-ai.app/v1')
const GATEWAY_MODEL = env('GATEWAY_MODEL', 'qwen')
const GATEWAY_API_KEY = env('GATEWAY_API_KEY', '')
const BACKEND = env('POC_BACKEND', GATEWAY_API_KEY ? 'gateway' : 'none')

const GEMINI_WS =
  'wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent'
const TOOL_NAME = 'consultar_atendente'
const PTIME_MS = 20
const FRAME_BYTES = 640 // 20 ms @ 16 kHz × 2 bytes
const TX_QUEUE_MAX = 1500 // 30 s de áudio bufferizado (Gemini manda rajadas mais rápido que tempo real)

const CARRIER_ANNOUNCEMENT =
  /caixa postal|ap[oó]s o sinal|sujeit[ao] [àa] cobran[çc]a|n[ãa]o receber recados|deixe (sua|seu) (mensagem|recado)|n[úu]mero (chamado|discado|que voc[êe] ligou)|fora da [áa]rea|desligado|n[ãa]o (pode|est[áa]) (atender|dispon[íi]vel)|est[áa] ocupado|tente (mais tarde|novamente)|obrigad[ao] por ligar|inexistente|n[ãa]o existe/i

const SYSTEM_INSTRUCTION =
  'Você é SOMENTE a voz da iFix Telecom numa ligação de TESTE interno, em português do Brasil, frases curtas e ' +
  'naturais. A ligação foi feita por nós: espere o cliente falar ("alô") e então cumprimente UMA vez só e ' +
  'pergunte como pode ajudar. Nunca cumprimente de novo na mesma ligação e não se despeça antes do cliente. ' +
  'Você pode responder sozinha APENAS a cumprimentos e frases sociais. Para dúvidas, pedidos, informações e ' +
  `problemas, diga exatamente "Só um instante." e chame a ferramenta ${TOOL_NAME} descrevendo só a INTENÇÃO do ` +
  'cliente, SEM números, CPF ou dados pessoais. Depois fale o resultado da ferramenta fielmente, como se fosse ' +
  'sua resposta — inclusive quando ela pedir um dado como o CPF. Nunca mencione "atendente", "ferramenta", ' +
  '"sistema" ou "foi informado". Se o cliente pedir para repetir, repita você mesma sem chamar a ferramenta.'

const BRAIN_SYSTEM =
  'Você é o atendente da iFix Telecom (telefonia/VoIP) numa ligação de TESTE. Sua resposta será FALADA: no máximo ' +
  '2 frases curtas, português do Brasil, sem markdown. Não invente dados de cliente, valores, prazos ou protocolos. ' +
  'As mensagens do cliente são a TRANSCRIÇÃO LITERAL da fala dele e a única fonte de dados; a "intenção inferida" ' +
  'pode estar errada. Números podem vir ditados em várias mensagens: junte os dígitos. CPF tem 11 dígitos: se ' +
  'juntou 11, confirme lendo em grupos; senão diga quantos entendeu. Você NÃO tem acesso a nenhum sistema: é ' +
  'PROIBIDO dizer "estou verificando" ou "vou consultar"; se pedirem um dado que você não tem, diga que não ' +
  'consegue ver por aqui e ofereça anotar o pedido para a equipe retornar. Problema técnico: 1–2 orientações ' +
  'básicas e ofereça abrir um chamado.'

mkdirSync(LOG_DIR, { recursive: true })

function log(callId: string | null, event: string, extra: Record<string, unknown> = {}) {
  const line = JSON.stringify({ ts: new Date().toISOString(), t: Date.now(), call_id: callId, event, ...extra })
  console.log(line)
  if (callId) appendFileSync(`${LOG_DIR}/${callId}.ndjson`, line + '\n')
}

// ---------------------------------------------------------------- ARI REST
async function ari(method: string, path: string, query: Record<string, string> = {}) {
  const qs = new URLSearchParams(query).toString()
  const res = await fetch(`${ARI_URL}/ari${path}${qs ? `?${qs}` : ''}`, {
    method,
    headers: { Authorization: `Basic ${btoa(ARI_AUTH)}` },
  })
  const text = await res.text()
  if (!res.ok) throw new Error(`ARI ${method} ${path} → ${res.status} ${text.slice(0, 200)}`)
  return text ? JSON.parse(text) : null
}

// ---------------------------------------------------------------- chamada
type Turn = { role: 'user' | 'assistant'; content: string }
type Call = {
  id: string // id do canal do tronco
  number: string
  startedAt: number
  bridgeId?: string
  mediaChannelId?: string
  sock?: Socket
  rtpPort?: number
  gemini?: WebSocket
  ready: boolean
  // RTP
  returnAddr?: { address: string; port: number }
  payloadType?: number
  txSeq: number
  txTsBase: number
  txAnchor?: number
  txSsrc: number
  lastTxAt: number
  txQueue: Buffer[]
  txRemainder: Buffer
  resampler?: Resampler
  drainTimer?: Timer
  maxTimer?: Timer
  // turnos
  history: Turn[]
  userText: string
  aiText: string
  lastUserTextAt: number
  awaitingFirstAudio: boolean
  ended: boolean
}

let active: Call | null = null

async function hangup(call: Call, reason: string) {
  if (call.ended) return
  call.ended = true
  log(call.id, 'hangup', { reason, duration_s: Math.round((Date.now() - call.startedAt) / 1000) })
  clearInterval(call.drainTimer)
  clearTimeout(call.maxTimer)
  try {
    call.gemini?.close()
  } catch {}
  call.sock?.close()
  for (const ch of [call.mediaChannelId, call.id]) {
    if (ch) await ari('DELETE', `/channels/${ch}`).catch(() => {})
  }
  if (call.bridgeId) await ari('DELETE', `/bridges/${call.bridgeId}`).catch(() => {})
  if (active === call) active = null
}

function flushUser(call: Call) {
  const text = call.userText.trim()
  if (!text) return
  call.userText = ''
  call.history.push({ role: 'user', content: text })
  log(call.id, 'user_transcript', { text })
  if (CARRIER_ANNOUNCEMENT.test(text) && call.history.filter((t) => t.role === 'assistant').length === 0) {
    log(call.id, 'carrier_announcement_hangup', { text })
    void hangup(call, 'carrier_announcement')
  }
}

function flushAi(call: Call) {
  const text = call.aiText.trim()
  if (!text) return
  call.aiText = ''
  call.history.push({ role: 'assistant', content: text })
  log(call.id, 'ai_transcript', { text })
}

async function askBrain(call: Call, intencao: string): Promise<string> {
  const t0 = Date.now()
  flushUser(call)
  const messages = [
    { role: 'system', content: BRAIN_SYSTEM },
    ...call.history.slice(-12),
    { role: 'system', content: `Intenção inferida pelo modelo de voz (pode estar errada, não use como dado): ${intencao}` },
  ]
  try {
    const res = await fetch(`${GATEWAY_BASE_URL}/chat/completions`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${GATEWAY_API_KEY}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: GATEWAY_MODEL, messages, max_tokens: 800, temperature: 0.3 }),
      signal: AbortSignal.timeout(12000),
    })
    const data = (await res.json()) as { model?: string; choices?: Array<{ finish_reason?: string; message?: { content?: string } }> }
    const c = data.choices?.[0]
    let text = c?.message?.content?.trim()
    if (text && c?.finish_reason === 'length') {
      const end = Math.max(text.lastIndexOf('.'), text.lastIndexOf('!'), text.lastIndexOf('?'))
      text = end > 0 ? text.slice(0, end + 1) : undefined
    }
    log(call.id, 'brain_done', { ms: Date.now() - t0, http: res.status, upstream_model: data.model, finish_reason: c?.finish_reason, text })
    if (!res.ok || !text) throw new Error(`gateway ${res.status}`)
    return text
  } catch (e) {
    log(call.id, 'brain_fail', { ms: Date.now() - t0, err: String(e) })
    return 'No momento não consegui essa informação. Posso anotar seu pedido para a equipe retornar.'
  }
}

function openGemini(call: Call) {
  const ws = new WebSocket(`${GEMINI_WS}?key=${GOOGLE_API_KEY}`)
  call.gemini = ws
  ws.onopen = () => {
    const tools =
      BACKEND === 'gateway'
        ? [
            {
              functionDeclarations: [
                {
                  name: TOOL_NAME,
                  description: 'Consulta o atendente da iFix Telecom para qualquer dúvida ou pedido. Ele já recebe a fala exata do cliente.',
                  parameters: {
                    type: 'OBJECT',
                    properties: { intencao: { type: 'STRING', description: 'Resumo do que o cliente quer, SEM números, CPF ou dados pessoais.' } },
                    required: ['intencao'],
                  },
                },
              ],
            },
          ]
        : []
    ws.send(
      JSON.stringify({
        setup: {
          model: `models/${GEMINI_MODEL}`,
          generationConfig: {
            responseModalities: ['AUDIO'],
            speechConfig: { voiceConfig: { prebuiltVoiceConfig: { voiceName: GEMINI_VOICE } }, languageCode: GEMINI_LANGUAGE },
          },
          systemInstruction: { parts: [{ text: SYSTEM_INSTRUCTION }] },
          tools,
          realtimeInputConfig: { automaticActivityDetection: { silenceDurationMs: VAD_SILENCE_MS, prefixPaddingMs: 100 } },
          inputAudioTranscription: {},
          outputAudioTranscription: {},
        },
      }),
    )
    log(call.id, 'gemini_open', { model: GEMINI_MODEL, voice: GEMINI_VOICE, language: GEMINI_LANGUAGE, vad_silence_ms: VAD_SILENCE_MS, backend: BACKEND })
  }
  ws.onmessage = async (msg) => {
    const raw = typeof msg.data === 'string' ? msg.data : new TextDecoder().decode(msg.data as ArrayBuffer)
    const ev = JSON.parse(raw) as Record<string, any>
    if (ev.setupComplete) {
      call.ready = true
      log(call.id, 'gemini_ready')
      return
    }
    const sc = ev.serverContent
    if (sc) {
      if (sc.inputTranscription?.text) {
        call.userText += sc.inputTranscription.text
        call.lastUserTextAt = Date.now()
        call.awaitingFirstAudio = true
      }
      if (sc.outputTranscription?.text) call.aiText += sc.outputTranscription.text
      for (const part of sc.modelTurn?.parts ?? []) {
        const d = part.inlineData
        if (!d?.data || !String(d.mimeType ?? '').startsWith('audio/pcm')) continue
        if (call.awaitingFirstAudio && call.lastUserTextAt) {
          log(call.id, 'turn_latency_approx', { ms: Date.now() - call.lastUserTextAt, via: 'last_input_transcription→first_audio' })
          call.awaitingFirstAudio = false
          flushUser(call)
        }
        const rate = Number(/rate=(\d+)/.exec(d.mimeType)?.[1] ?? 24000)
        call.resampler ??= new Resampler(rate / 16000)
        enqueueTx(call, call.resampler.push(Buffer.from(d.data, 'base64')))
      }
      if (sc.interrupted) {
        log(call.id, 'barge_in', { dropped_frames: call.txQueue.length })
        call.txQueue.length = 0
        call.txRemainder = Buffer.alloc(0)
        flushAi(call)
      }
      if (sc.turnComplete) {
        flushUser(call)
        flushAi(call)
      }
    }
    if (ev.toolCall?.functionCalls) {
      const responses = []
      for (const fc of ev.toolCall.functionCalls) {
        const intencao = String(fc.args?.intencao ?? '')
        log(call.id, 'tool_call', { name: fc.name, intencao })
        const result = fc.name === TOOL_NAME ? await askBrain(call, intencao) : 'Ferramenta desconhecida.'
        responses.push({ id: fc.id, name: fc.name, response: { result } })
      }
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ toolResponse: { functionResponses: responses } }))
      log(call.id, 'tool_output_sent')
    }
    if (ev.goAway) log(call.id, 'gemini_goaway', { timeLeft: ev.goAway.timeLeft })
    if (ev.usageMetadata) log(call.id, 'usage', { usage: ev.usageMetadata })
  }
  ws.onclose = (e) => {
    log(call.id, 'gemini_close', { code: e.code, reason: String(e.reason).slice(0, 200) })
    void hangup(call, 'gemini_closed')
  }
  ws.onerror = () => log(call.id, 'gemini_error')
}

function enqueueTx(call: Call, pcmLE16k: Buffer) {
  let buf = Buffer.concat([call.txRemainder, pcmLE16k])
  while (buf.length >= FRAME_BYTES) {
    if (call.txQueue.length < TX_QUEUE_MAX) call.txQueue.push(buf.subarray(0, FRAME_BYTES))
    buf = buf.subarray(FRAME_BYTES)
  }
  call.txRemainder = Buffer.from(buf)
}

async function startMedia(call: Call) {
  // Socket RTP: porta efêmera em 127.0.0.1 (ponte e Asterisk no mesmo host, network host).
  const sock = createSocket('udp4')
  await new Promise<void>((resolve, reject) => {
    sock.once('error', reject)
    sock.bind(0, RTP_BIND, () => resolve())
  })
  call.sock = sock
  call.rtpPort = sock.address().port
  let rxFrames = 0
  sock.on('message', (msg, rinfo) => {
    if (msg.length < 12 || (msg[0] & 0xc0) !== 0x80) return
    const pt = msg[1] & 0x7f
    if (pt >= 72 && pt <= 76) return // RTCP
    call.returnAddr ??= { address: rinfo.address, port: rinfo.port }
    call.payloadType ??= pt
    rxFrames++
    if (!call.ready || call.gemini?.readyState !== WebSocket.OPEN) return
    const pcmLE = swap16(msg.subarray(12)) // slin16 no fio = s16be
    call.gemini.send(JSON.stringify({ realtimeInput: { audio: { mimeType: 'audio/pcm;rate=16000', data: pcmLE.toString('base64') } } }))
  })
  // Drenador: 1 frame por tick de 20 ms; timestamp ancorado no relógio.
  call.drainTimer = setInterval(() => {
    if (!call.returnAddr || call.txQueue.length === 0) return
    const chunk = call.txQueue.shift() as Buffer
    const now = Date.now()
    call.txAnchor ??= now
    const ts = (call.txTsBase + Math.round((now - call.txAnchor) * 16)) >>> 0
    const marker = call.lastTxAt !== 0 && now - call.lastTxAt > PTIME_MS * 5
    call.lastTxAt = now
    const pkt = Buffer.alloc(12 + FRAME_BYTES)
    pkt[0] = 0x80
    pkt[1] = ((call.payloadType ?? 118) & 0x7f) | (marker ? 0x80 : 0)
    pkt.writeUInt16BE(call.txSeq, 2)
    call.txSeq = (call.txSeq + 1) & 0xffff
    pkt.writeUInt32BE(ts, 4)
    pkt.writeUInt32BE(call.txSsrc, 8)
    swap16(chunk).copy(pkt, 12)
    sock.send(pkt, call.returnAddr.port, call.returnAddr.address)
  }, PTIME_MS)
  setTimeout(() => log(call.id, 'rtp_rx_check', { rx_frames_5s: rxFrames }), 5000)

  const bridge = await ari('POST', '/bridges', { type: 'mixing', name: `poc-gemini-${call.id}` })
  call.bridgeId = bridge.id
  const media = await ari('POST', '/channels/externalMedia', {
    app: ARI_APP,
    external_host: `${RTP_BIND}:${call.rtpPort}`,
    format: 'slin16',
    encapsulation: 'rtp',
    transport: 'udp',
    direction: 'both',
  })
  call.mediaChannelId = media.id
  await ari('POST', `/bridges/${bridge.id}/addChannel`, { channel: `${call.id},${media.id}` })
  log(call.id, 'media_bridged', { bridge: bridge.id, media_channel: media.id, rtp_port: call.rtpPort })
}

// ---------------------------------------------------------------- eventos ARI
function connectAriEvents() {
  const wsUrl = ARI_URL.replace(/^http/, 'ws')
  const ws = new WebSocket(`${wsUrl}/ari/events?app=${ARI_APP}&api_key=${encodeURIComponent(ARI_AUTH)}`)
  ws.onopen = () => log(null, 'ari_connected', { app: ARI_APP })
  ws.onclose = () => {
    log(null, 'ari_disconnected')
    setTimeout(connectAriEvents, 3000)
  }
  ws.onmessage = async (msg) => {
    const ev = JSON.parse(String(msg.data)) as Record<string, any>
    const chId: string | undefined = ev.channel?.id
    if (ev.type === 'StasisStart' && active && chId === active.id) {
      log(active.id, 'answered', { channel: ev.channel?.name })
      try {
        openGemini(active)
        await startMedia(active)
      } catch (e) {
        log(active.id, 'media_fail', { err: String(e) })
        await hangup(active, 'media_fail')
      }
    }
    if ((ev.type === 'StasisEnd' || ev.type === 'ChannelDestroyed') && active && (chId === active.id || chId === active.mediaChannelId)) {
      await hangup(active, `${ev.type}${ev.cause_txt ? `:${ev.cause_txt}` : ''}`)
    }
  }
}

// ---------------------------------------------------------------- controle
Bun.serve({
  hostname: '127.0.0.1',
  port: CONTROL_PORT,
  async fetch(req) {
    const url = new URL(req.url)
    if (req.method === 'GET' && url.pathname === '/health') {
      return Response.json({ ok: true, key_configured: Boolean(GOOGLE_API_KEY), model: GEMINI_MODEL, active: active?.id ?? null, backend: BACKEND })
    }
    if (req.method === 'POST' && url.pathname === '/call') {
      if (!GOOGLE_API_KEY) return Response.json({ error: 'GOOGLE_API_KEY ausente' }, { status: 503 })
      if (active) return Response.json({ error: 'já existe chamada ativa', active: active.id }, { status: 409 })
      const { number } = (await req.json()) as { number?: string }
      if (!number || !/^\d{10,13}$/.test(number)) return Response.json({ error: 'number inválido' }, { status: 400 })
      const digits = number.startsWith('55') ? number : `55${number}`
      const id = crypto.randomUUID()
      const call: Call = {
        id,
        number: digits,
        startedAt: Date.now(),
        ready: false,
        txSeq: Math.floor(Math.random() * 0x10000),
        txTsBase: Math.floor(Math.random() * 0xffffffff) >>> 0,
        txSsrc: Math.floor(Math.random() * 0xffffffff) >>> 0,
        lastTxAt: 0,
        txQueue: [],
        txRemainder: Buffer.alloc(0),
        history: [],
        userText: '',
        aiText: '',
        lastUserTextAt: 0,
        awaitingFirstAudio: false,
        ended: false,
      }
      active = call
      call.maxTimer = setTimeout(() => void hangup(call, 'max_duration'), MAX_CALL_SECONDS * 1000)
      // Não atendeu: o canal pode morrer sem entrar no Stasis (sem evento p/ este app) — libera a trava.
      setTimeout(() => {
        if (active === call && !call.bridgeId) void hangup(call, 'no_answer_timeout')
      }, 60_000)
      try {
        await ari('POST', '/channels', {
          endpoint: `PJSIP/${TRUNK_PREFIX}${digits}@${TRUNK_ENDPOINT}`,
          app: ARI_APP,
          appArgs: 'outbound',
          channelId: id,
          callerId: CALLER_ID,
          timeout: '45',
        })
        log(id, 'originate', { number_masked: `${digits.slice(0, 6)}*****`, trunk: TRUNK_ENDPOINT })
        return Response.json({ call_id: id }, { status: 202 })
      } catch (e) {
        log(id, 'originate_fail', { err: String(e) })
        clearTimeout(call.maxTimer)
        active = null
        return Response.json({ error: String(e) }, { status: 502 })
      }
    }
    return new Response('not found', { status: 404 })
  },
})

connectAriEvents()
log(null, 'listening', { control: `127.0.0.1:${CONTROL_PORT}`, model: GEMINI_MODEL, key_configured: Boolean(GOOGLE_API_KEY) })
