/**
 * Utilitários de áudio da ponte: troca de endianness e reamostragem linear.
 *
 * @module poc-gemini-bridge/audio
 */
/** s16be ↔ s16le (in place numa cópia). */
export function swap16(buf: Buffer): Buffer {
  const out = Buffer.from(buf)
  out.swap16()
  return out
}

/** Reamostragem linear com estado entre chunks (ex.: 24 kHz → 16 kHz). */
export class Resampler {
  private carry: number[] = []
  private pos = 0
  constructor(private readonly ratio: number) {} // entrada/saída, ex.: 24000/16000 = 1.5
  push(pcmLE: Buffer): Buffer {
    for (let i = 0; i + 1 < pcmLE.length; i += 2) this.carry.push(pcmLE.readInt16LE(i))
    const out: number[] = []
    while (this.pos + 1 < this.carry.length) {
      const i = Math.floor(this.pos)
      const f = this.pos - i
      out.push(Math.round(this.carry[i] * (1 - f) + this.carry[i + 1] * f))
      this.pos += this.ratio
    }
    const drop = Math.floor(this.pos)
    this.carry = this.carry.slice(drop)
    this.pos -= drop
    const buf = Buffer.alloc(out.length * 2)
    out.forEach((s, k) => buf.writeInt16LE(Math.max(-32768, Math.min(32767, s)), k * 2))
    return buf
  }
}


/**
 * Passa-baixa FIR (sinc janelado, Blackman) com estado entre chunks — anti-aliasing antes de
 * reduzir a taxa (ex.: 24 kHz → 8 kHz). Sem ele, o conteúdo acima de 4 kHz "dobra" para dentro
 * da faixa de voz e soa áspero/chiado.
 */
export class Lowpass {
  private readonly taps: Float64Array
  private hist: number[]
  constructor(inRate: number, cutoffHz: number, nTaps = 63) {
    const fc = cutoffHz / inRate
    const m = nTaps - 1
    const taps = new Float64Array(nTaps)
    let sum = 0
    for (let n = 0; n < nTaps; n++) {
      const x = n - m / 2
      const sinc = x === 0 ? 2 * fc : Math.sin(2 * Math.PI * fc * x) / (Math.PI * x)
      const w = 0.42 - 0.5 * Math.cos((2 * Math.PI * n) / m) + 0.08 * Math.cos((4 * Math.PI * n) / m)
      taps[n] = sinc * w
      sum += taps[n]
    }
    for (let n = 0; n < nTaps; n++) taps[n] /= sum
    this.taps = taps
    this.hist = new Array(nTaps - 1).fill(0)
  }
  push(pcmLE: Buffer): Buffer {
    const input: number[] = []
    for (let i = 0; i + 1 < pcmLE.length; i += 2) input.push(pcmLE.readInt16LE(i))
    const buf = this.hist.concat(input)
    const out = Buffer.alloc(input.length * 2)
    const n = this.taps.length
    for (let i = 0; i < input.length; i++) {
      let acc = 0
      for (let k = 0; k < n; k++) acc += this.taps[k] * buf[i + n - 1 - k]
      out.writeInt16LE(Math.max(-32768, Math.min(32767, Math.round(acc))), i * 2)
    }
    this.hist = buf.slice(buf.length - (n - 1))
    return out
  }
}
