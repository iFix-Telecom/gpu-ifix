import { expect, test } from 'bun:test'
import { Resampler, swap16 } from './audio'

test('Resampler 24k→16k preserva o sinal em chunks irregulares', () => {
  const n = 24000
  const buf = Buffer.alloc(n * 2)
  for (let i = 0; i < n; i++) buf.writeInt16LE(Math.round(10000 * Math.sin((2 * Math.PI * 440 * i) / 24000)), i * 2)
  const r = new Resampler(1.5)
  const out = Buffer.concat([r.push(buf.subarray(0, 7001 * 2)), r.push(buf.subarray(7001 * 2, 15000 * 2)), r.push(buf.subarray(15000 * 2))])
  const m = out.length / 2
  expect(Math.abs(m - 16000)).toBeLessThanOrEqual(2)
  let err = 0
  for (let k = 0; k < m; k++) err = Math.max(err, Math.abs(out.readInt16LE(k * 2) - 10000 * Math.sin((2 * Math.PI * 440 * k) / 16000)))
  expect(err).toBeLessThan(200)
})

test('swap16 inverte bytes sem alterar a entrada', () => {
  const b = Buffer.from([0x01, 0x02, 0x03, 0x04])
  expect([...swap16(b)]).toEqual([0x02, 0x01, 0x04, 0x03])
  expect([...b]).toEqual([0x01, 0x02, 0x03, 0x04])
})

test('Resampler 8k→16k (entrada do Gemini) dobra as amostras', () => {
  const buf = Buffer.alloc(8000 * 2)
  for (let i = 0; i < 8000; i++) buf.writeInt16LE(Math.round(8000 * Math.sin((2 * Math.PI * 300 * i) / 8000)), i * 2)
  const r = new Resampler(0.5)
  const out = Buffer.concat([r.push(buf.subarray(0, 3001 * 2)), r.push(buf.subarray(3001 * 2))])
  expect(Math.abs(out.length / 2 - 16000)).toBeLessThanOrEqual(2)
})

test('Lowpass antes de 24k→8k remove aliasing de 6 kHz e preserva 1 kHz', async () => {
  const { Lowpass } = await import('./audio')
  const rms = (b: Buffer) => { let s = 0; const n = b.length / 2; for (let i = 0; i < n; i++) s += b.readInt16LE(i * 2) ** 2; return Math.sqrt(s / n) }
  const tone = (f: number) => { const b = Buffer.alloc(24000 * 2); for (let i = 0; i < 24000; i++) b.writeInt16LE(Math.round(10000 * Math.sin((2 * Math.PI * f * i) / 24000)), i * 2); return b }
  const down = (b: Buffer, lp: boolean) => { const f = new Lowpass(24000, 3600); const r = new Resampler(3); return r.push(lp ? f.push(b) : b).subarray(200) }
  const alias6k = rms(down(tone(6000), true)), alias6kSemFiltro = rms(down(tone(6000), false)), voz1k = rms(down(tone(1000), true))
  expect(alias6kSemFiltro).toBeGreaterThan(5000) // sem filtro 6 kHz vira ~2 kHz audível
  expect(alias6k).toBeLessThan(300)
  expect(voz1k).toBeGreaterThan(6500)
})
