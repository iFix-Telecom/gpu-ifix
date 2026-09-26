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

