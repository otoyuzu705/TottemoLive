import type { params } from '../../wailsjs/go/models'

/** step から表示する小数桁数を決める(0.5 → 1桁、0.01 → 2桁、1 → 0桁)。 */
export function decimals(step: number): number {
  return step >= 1 ? 0 : Math.min(4, Math.ceil(-Math.log10(step)))
}

export function fmt(v: number, spec: params.ParamSpec): string {
  return v.toFixed(decimals(spec.step))
}

export function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi)
}

/** step の倍数に丸める。 */
export function snap(v: number, spec: params.ParamSpec): number {
  const s = spec.step > 0 ? Math.round(v / spec.step) * spec.step : v
  return Number(clamp(s, spec.min, spec.max).toFixed(6))
}

/** スライダー位置(0〜1)と値の変換。Scale が log の項目は対数。 */
export function toPos(v: number, spec: params.ParamSpec): number {
  if (spec.scale === 'log') return Math.log(v / spec.min) / Math.log(spec.max / spec.min)
  return (v - spec.min) / (spec.max - spec.min)
}

export function fromPos(pos: number, spec: params.ParamSpec): number {
  const v = spec.scale === 'log' ? spec.min * Math.pow(spec.max / spec.min, pos) : spec.min + pos * (spec.max - spec.min)
  return snap(v, spec)
}

export function mmss(sec: number): string {
  const s = Math.max(0, Math.round(sec))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

/** バイト数を、1024 区切りの B / KB / MB / GB / TB で表す(3桁未満は小数1桁、それ以上は整数)。 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return i === 0 ? `${Math.round(v)} B` : `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}
