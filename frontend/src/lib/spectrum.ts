// スペクトラム表示(GEQ型: 1/3オクターブの帯域ごとのレベル)の計算。DOMに依存しない純粋な関数で、
// `npm test`(node:test)で検証する。

/** ISOの1/3オクターブ中心周波数(Hz)、20 Hz〜20 kHz の31帯域 */
export const BAND_CENTERS = [
  20, 25, 31.5, 40, 50, 63, 80, 100, 125, 160, 200, 250, 315, 400, 500, 630, 800, 1000, 1250, 1600, 2000, 2500,
  3150, 4000, 5000, 6300, 8000, 10000, 12500, 16000, 20000,
]

/** 表示の下限(dB)。これ以下は無音として扱う。 */
export const DB_FLOOR = -90

/**
 * Web Audio の AnalyserNode の値(Blackman窓・1/N正規化のFFT)を、「0 dBFS の正弦波 = 0 dB」の
 * 帯域レベルに直すための補正(dB)。窓の2乗平均(0.3046)と、正のbinが半分のパワーを持つことから
 * 10·log10(1 / (0.3046 / 4)) ≒ 11.2。実際のFFTで 0 dBFS の正弦波が 0 dB になることをテストで確認している。
 */
export const CALIBRATION_DB = 11.2

/** 帯域の下端・上端(1/3オクターブ = 中心の 2^(±1/6) 倍) */
export function bandEdges(center: number): [number, number] {
  const r = Math.pow(2, 1 / 6)
  return [center / r, center * r]
}

/**
 * AnalyserNode.getFloatFrequencyData の結果(dB)から、1/3オクターブ帯域ごとのレベル(dB、0 dBFS の正弦波 = 0 dB)を求める。
 * 帯域内のbinのパワーを足す。低域で帯域がbin幅より狭いときは、最も近いbinのパワーを帯域幅の比で換算する。
 */
export function bandLevels(
  freqDb: Float32Array,
  sampleRate: number,
  fftSize: number,
  out = new Float32Array(BAND_CENTERS.length),
): Float32Array {
  const binHz = sampleRate / fftSize
  BAND_CENTERS.forEach((fc, i) => {
    const [lo, hi] = bandEdges(fc)
    const from = Math.max(1, Math.ceil(lo / binHz))
    const to = Math.min(freqDb.length - 1, Math.ceil(hi / binHz) - 1)
    let power = 0
    if (to >= from) {
      for (let k = from; k <= to; k++) power += dbToPower(freqDb[k])
    } else {
      const k = Math.min(freqDb.length - 1, Math.max(1, Math.round(fc / binHz)))
      power = (dbToPower(freqDb[k]) * (hi - lo)) / binHz
    }
    out[i] = power > 0 ? Math.max(DB_FLOOR, 10 * Math.log10(power) + CALIBRATION_DB) : DB_FLOOR
  })
  return out
}

function dbToPower(db: number): number {
  return Number.isFinite(db) ? Math.pow(10, db / 10) : 0
}

/**
 * 表示用の動き。レベルは上がるときは即座に、下がるときは一定の速さで落ちる(GEQ型の見え方)。
 * ピークは一定時間保持してから、ゆっくり落ちる。
 */
export class Meter {
  level: Float32Array
  peak: Float32Array
  private hold: Float32Array

  private fallDbPerSec: number
  private peakFallDbPerSec: number
  private holdSec: number

  constructor(n: number, fallDbPerSec = 40, peakFallDbPerSec = 15, holdSec = 0.8) {
    this.fallDbPerSec = fallDbPerSec
    this.peakFallDbPerSec = peakFallDbPerSec
    this.holdSec = holdSec
    this.level = new Float32Array(n).fill(DB_FLOOR)
    this.peak = new Float32Array(n).fill(DB_FLOOR)
    this.hold = new Float32Array(n)
  }

  /** 新しい帯域レベルを取り込み、dt 秒ぶん進める。 */
  update(levels: ArrayLike<number>, dt: number) {
    for (let i = 0; i < this.level.length; i++) {
      const v = Math.max(DB_FLOOR, levels[i])
      this.level[i] = v >= this.level[i] ? v : Math.max(v, this.level[i] - this.fallDbPerSec * dt, DB_FLOOR)
      if (this.level[i] >= this.peak[i]) {
        this.peak[i] = this.level[i]
        this.hold[i] = this.holdSec
      } else if (this.hold[i] > 0) {
        this.hold[i] -= dt
      } else {
        this.peak[i] = Math.max(this.level[i], this.peak[i] - this.peakFallDbPerSec * dt)
      }
    }
  }

  reset() {
    this.level.fill(DB_FLOOR)
    this.peak.fill(DB_FLOOR)
    this.hold.fill(0)
  }
}
