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
const INSTRUCTIONS =
  process.env.POC_INSTRUCTIONS ??
  'Você é a assistente de voz da iFix Telecom em uma ligação de TESTE interno. ' +
    'Fale português do Brasil, frases curtas e naturais. Cumprimente e pergunte como pode ajudar. ' +
    'Nunca diga que executou uma ação que não executou. Se não souber, diga que não sabe.'

const client = new OpenAI({ apiKey: process.env.OPENAI_API_KEY, project: process.env.OPENAI_PROJECT_ID })

mkdirSync(LOG_DIR, { recursive: true })

type CallState = { startedAt: number; lastSpeechStop?: number; awaitingFirstAudio: boolean; ws?: WebSocket; timer?: Timer }
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

function monitor(callId: string, state: CallState) {
  const ws = new WebSocket(`wss://api.openai.com/v1/realtime?call_id=${callId}`, {
    headers: { Authorization: `Bearer ${process.env.OPENAI_API_KEY}`, 'OpenAI-Project': process.env.OPENAI_PROJECT_ID ?? '' },
  } as unknown as string[])
  state.ws = ws
  ws.onopen = () => {
    log(callId, 'ws_open')
    ws.send(JSON.stringify({ type: 'response.create' }))
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
        break
      case 'response.output_audio_transcript.done':
        log(callId, 'ai_transcript', { text: ev.transcript })
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
  const state: CallState = { startedAt: Date.now(), awaitingFirstAudio: false }
  calls.set(callId, state)
  try {
    await client.realtime.calls.accept(callId, {
      type: 'realtime',
      model: MODEL,
      instructions: INSTRUCTIONS,
      audio: {
        input: { transcription: { model: 'gpt-4o-mini-transcribe', language: 'pt' } },
        output: { voice: VOICE },
      },
    })
    log(callId, 'accepted', { model: MODEL, voice: VOICE })
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
      return Response.json({ ok: true, secret_configured: Boolean(WEBHOOK_SECRET), caller_token_configured: Boolean(CALLER_TOKEN), active_calls: calls.size, model: MODEL })
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
