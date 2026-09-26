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
