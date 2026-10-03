import assert from 'node:assert/strict'
import { test } from 'node:test'
import { BAND_CENTERS, CALIBRATION_DB, DB_FLOOR, Meter, bandEdges, bandLevels, parseBandSeries, seriesAt } from './spectrum.ts'

const SR = 48000

// AnalyserNode と同じ計算(Blackman窓、1/N正規化、dB)を、指定の周波数付近のbinだけ素朴なDFTで求める。
function analyserLike(signal: (n: number) => number, fftSize: number): Float32Array {
  const x = new Float64Array(fftSize)
  for (let n = 0; n < fftSize; n++) {
    const w = 0.42 - 0.5 * Math.cos((2 * Math.PI * n) / fftSize) + 0.08 * Math.cos((4 * Math.PI * n) / fftSize)
    x[n] = signal(n) * w
  }
  const out = new Float32Array(fftSize / 2).fill(-Infinity)
  const binHz = SR / fftSize
  for (let k = 1; k < fftSize / 2; k++) {
    if (k * binHz > 24000) break
    let re = 0
    let im = 0
    for (let n = 0; n < fftSize; n++) {
      const ph = (2 * Math.PI * k * n) / fftSize
      re += x[n] * Math.cos(ph)
      im -= x[n] * Math.sin(ph)
    }
    out[k] = 20 * Math.log10(Math.hypot(re, im) / fftSize + 1e-30)
  }
  return out
}

test('隣り合う帯域の境目が連続し、幅は1/3オクターブ', () => {
  for (let i = 0; i + 1 < BAND_CENTERS.length; i++) {
    const [, hi] = bandEdges(BAND_CENTERS[i])
    const [lo] = bandEdges(BAND_CENTERS[i + 1])
    // ISOの中心周波数は丸めてあるので、境目は数%以内で一致すればよい
    assert.ok(Math.abs(hi / lo - 1) < 0.04, `band ${i}: ${hi} vs ${lo}`)
  }
  const [lo, hi] = bandEdges(1000)
  assert.ok(Math.abs(hi / lo - Math.pow(2, 1 / 3)) < 1e-9)
})

test('0 dBFS の正弦波は、その帯域が約 0 dB になる(補正の検証)', () => {
  const fftSize = 2048
  // 帯域の中心付近(bin中心に合わせる)と、少しずれた周波数の両方
  for (const f of [1000, 1010, 997]) {
    const db = analyserLike((n) => Math.sin((2 * Math.PI * f * n) / SR), fftSize)
    const levels = bandLevels(db, SR, fftSize)
    const i = BAND_CENTERS.indexOf(1000)
    assert.ok(Math.abs(levels[i] - 0) < 1.0, `${f} Hz: ${levels[i].toFixed(2)} dB`)
    // 離れた帯域は十分に小さい(Blackman窓の漏れ)
    assert.ok(levels[BAND_CENTERS.indexOf(250)] < -50)
    assert.ok(levels[BAND_CENTERS.indexOf(4000)] < -50)
  }
  // 振幅を半分にすると約 -6 dB
  const half = analyserLike((n) => 0.5 * Math.sin((2 * Math.PI * 1000 * n) / SR), fftSize)
  const i = BAND_CENTERS.indexOf(1000)
  assert.ok(Math.abs(bandLevels(half, SR, fftSize)[i] + 6.02) < 1.0)
})

test('低域の狭い帯域でも値が出る(bin幅より帯域が狭い場合)', () => {
  const fftSize = 2048 // bin幅 23 Hz。31.5 Hz帯(約7 Hz幅)より広い
  const db = analyserLike((n) => Math.sin((2 * Math.PI * 31.5 * n) / SR), fftSize)
  const levels = bandLevels(db, SR, fftSize)
  const i = BAND_CENTERS.indexOf(31.5)
  assert.ok(levels[i] > DB_FLOOR + 20, `31.5 Hz band: ${levels[i]}`)
})

test('無音(-Infinity)・全て極小の入力は下限になる', () => {
  const silent = new Float32Array(1024).fill(-Infinity)
  for (const v of bandLevels(silent, SR, 2048)) assert.equal(v, DB_FLOOR)
  const tiny = new Float32Array(1024).fill(-200)
  for (const v of bandLevels(tiny, SR, 2048)) assert.equal(v, DB_FLOOR)
})

test('Meter: 上がるときは即座に、下がるときは一定の速さで落ちる', () => {
  const m = new Meter(2, 40, 15, 0.5)
  m.update([-10, -60], 0.016)
  assert.equal(m.level[0], -10)
  m.update([-90, -90], 0.5) // 0.5秒で 20 dB 落ちる
  assert.ok(Math.abs(m.level[0] - -30) < 1e-4, `level ${m.level[0]}`)
  assert.ok(Math.abs(m.level[1] - -80) < 1e-4)
  // 下限を下回らない
  for (let i = 0; i < 20; i++) m.update([-90, -90], 0.5)
  assert.equal(m.level[0], DB_FLOOR)
})

test('Meter: ピークは保持時間のあいだ動かず、そのあと落ちる', () => {
  const m = new Meter(1, 1000, 10, 0.5)
  m.update([-20], 0.016)
  assert.equal(m.peak[0], -20)
  m.update([-90], 0.3) // 保持の途中
  assert.equal(m.peak[0], -20)
  m.update([-90], 0.3) // 保持が終わる(残り 0.2 秒は落ち始めの前)
  m.update([-90], 1) // 1秒で 10 dB 落ちる
  assert.ok(Math.abs(m.peak[0] - -30) < 1e-4, `peak ${m.peak[0]}`)
  // 新しいピークで保持し直す
  m.update([-10], 0.016)
  assert.equal(m.peak[0], -10)
  m.reset()
  assert.equal(m.peak[0], DB_FLOOR)
})

function makeSeries(frames: number, bands: number, fill: (f: number, i: number) => number, offsetDb = 0) {
  const data = new Float32Array(frames * bands)
  for (let f = 0; f < frames; f++) for (let i = 0; i < bands; i++) data[f * bands + i] = fill(f, i)
  return { hopSec: 0.1, bands, frames, data, offsetDb }
}

test('seriesAt: フレームの間を dB のまま線形に補間する', () => {
  const s = makeSeries(3, 2, (f, i) => -20 * f - 10 * i) // フレーム0: [0,-10]、1: [-20,-30]、2: [-40,-50]
  const out = new Float32Array(2)
  seriesAt(s, 0, out)
  assert.deepEqual([...out], [0, -10])
  seriesAt(s, 0.05, out) // 0 と 1 の中間
  assert.ok(Math.abs(out[0] - -10) < 1e-5 && Math.abs(out[1] - -20) < 1e-5, `${[...out]}`)
  seriesAt(s, 0.1, out)
  assert.deepEqual([...out], [-20, -30])
  seriesAt(s, 0.25, out) // 最後のフレームの手前(最後のフレームの値に向かう)
  assert.ok(Math.abs(out[0] - -40) < 1e-5)
})

test('seriesAt: 曲の外は無音、オフセットを足し、下限は割らない', () => {
  const s = makeSeries(2, 1, () => -30, 6)
  const out = new Float32Array(1)
  seriesAt(s, 0.05, out)
  assert.ok(Math.abs(out[0] - -24) < 1e-5, `offset: ${out[0]}`)
  seriesAt(s, -0.5, out)
  assert.equal(out[0], DB_FLOOR)
  seriesAt(s, 5, out)
  assert.equal(out[0], DB_FLOOR)
  // 無音(下限)にオフセットを足しても無音のまま
  const silent = makeSeries(2, 1, () => DB_FLOOR, 20)
  seriesAt(silent, 0.05, out)
  assert.equal(out[0], DB_FLOOR)
  // 大きな負のオフセットでも下限を割らない
  const quiet = makeSeries(2, 1, () => -80, -30)
  seriesAt(quiet, 0.05, out)
  assert.equal(out[0], DB_FLOOR)
})

test('parseBandSeries: バイト数が合うときだけ作る', () => {
  const buf = new Float32Array([1, 2, 3, 4, 5, 6]).buffer
  const ok = parseBandSeries(buf, { bands: 3, frames: 2, hopSec: 0.05, offsetDb: -3 })
  assert.ok(ok && ok.data[5] === 6 && ok.offsetDb === -3)
  assert.equal(parseBandSeries(buf, { bands: 3, frames: 3, hopSec: 0.05, offsetDb: 0 }), null)
  assert.equal(parseBandSeries(buf, { bands: 0, frames: 2, hopSec: 0.05, offsetDb: 0 }), null)
})

test('seriesAt: 再生音量(gainDb)をPA出力にも掛ける', () => {
  const s = makeSeries(2, 1, () => -30, 6)
  const out = new Float32Array(1)
  seriesAt(s, 0.05, out, -6)
  assert.ok(Math.abs(out[0] - -30) < 1e-5, `offset 6 + gain -6: ${out[0]}`)
  seriesAt(s, 0.05, out, 0)
  assert.ok(Math.abs(out[0] - -24) < 1e-5)
  // 無音は音量を上げても無音のまま
  const silent = makeSeries(2, 1, () => DB_FLOOR, 0)
  seriesAt(silent, 0.05, out, 6)
  assert.equal(out[0], DB_FLOOR)
})
