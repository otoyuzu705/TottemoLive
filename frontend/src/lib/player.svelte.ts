// プレビュー再生。<audio> に加工後・原音(曲全体)の2本を切り替えて流す。
// 新しいプレビューが届いたら、再生位置を保ったまま音源を差し替える。
import { bandLevels, seriesAt, type BandSeries } from './spectrum'

export type Mode = 'processed' | 'original'

/** 再生音量(dB)の範囲。0 dB を超えると出力が0 dBFSを超えて歪むことがある。 */
export const VOLUME_MIN_DB = -30
export const VOLUME_MAX_DB = 6
const VOLUME_KEY = 'tottemolive.volumeDb'

/** スペクトラム表示用のFFT長。48 kHz で bin幅 約2.9 Hz、窓の長さ 約0.34 秒(低域の帯域まで分解できる) */
const ANALYSER_FFT_SIZE = 16384

function loadVolume(): number {
  try {
    const v = Number(localStorage.getItem(VOLUME_KEY))
    if (Number.isFinite(v) && localStorage.getItem(VOLUME_KEY) !== null) return Math.min(Math.max(v, VOLUME_MIN_DB), VOLUME_MAX_DB)
  } catch {
    // ストレージが使えなくても、音量は既定値(0 dB)で動く
  }
  return 0
}

class Player {
  mode = $state<Mode>('processed')
  playing = $state(false)
  /** 曲頭からの再生位置(秒) */
  position = $state(0)
  /** 再生する音源の長さ(秒、残響の尾を含む)。まだ読み込んでいなければ 0 */
  duration = $state(0)
  loaded = $state<Record<Mode, boolean>>({ processed: false, original: false })
  /** 再生音量(dB)。聞こえ方だけを変える(再計算なし)。プレビュー専用で、書き出しには反映されない */
  volumeDb = $state(loadVolume())
  muted = $state(false)
  /** 加工後のプレビューに対応する、PA出力の帯域レベル(スペクトラム表示で重ねる)。無ければ null */
  pa = $state.raw<BandSeries | null>(null)

  private audio = new Audio()
  private blobs: Partial<Record<Mode, string>> = {}
  private raf = 0
  private ctx?: AudioContext
  private gain?: GainNode
  private analyser?: AnalyserNode
  private freqBuf?: Float32Array<ArrayBuffer>

  constructor() {
    this.audio.addEventListener('play', () => {
      this.playing = true
      this.tick()
    })
    this.audio.addEventListener('pause', () => {
      this.playing = false
      cancelAnimationFrame(this.raf)
    })
  }

  private tick = () => {
    this.position = this.audio.currentTime
    if (this.playing) this.raf = requestAnimationFrame(this.tick)
  }

  /**
   * 音量用のWebAudioの経路(<audio> → GainNode → 出力)を作る。0 dB を超える増幅は
   * <audio>.volume (最大1)では出来ないため。ブラウザの規則で、ユーザー操作の中で作る必要がある。
   * 加工後・原音とも同じ <audio> を通るので、A/Bで同じ音量になる。
   * 音量の後ろにスペクトラム表示用のアナライザーを挟む(耳に届く出力そのものを見るため)。
   */
  private ensureGraph() {
    if (this.gain) return
    this.ctx = new AudioContext()
    const source = this.ctx.createMediaElementSource(this.audio)
    this.gain = this.ctx.createGain()
    this.analyser = this.ctx.createAnalyser()
    this.analyser.fftSize = ANALYSER_FFT_SIZE
    this.analyser.smoothingTimeConstant = 0 // 表示側(Meter)で動きをつけるので、ここでは平滑化しない
    this.analyser.minDecibels = -140
    this.analyser.maxDecibels = 0
    this.freqBuf = new Float32Array(this.analyser.frequencyBinCount)
    source.connect(this.gain).connect(this.analyser).connect(this.ctx.destination)
    this.applyVolume(true)
  }

  private applyVolume(immediate = false) {
    if (!this.gain || !this.ctx) return
    const g = this.muted ? 0 : Math.pow(10, this.volumeDb / 20)
    // 急に変えるとプツッというノイズが出るので、ごく短い時間で滑らかに変える
    if (immediate) this.gain.gain.value = g
    else this.gain.gain.setTargetAtTime(g, this.ctx.currentTime, 0.015)
  }

  /**
   * いま出力されている音の、1/3オクターブ帯域ごとのレベル(dB、0 dBFS の正弦波 = 0 dB)を out に書く。
   * まだ再生の経路が無い(一度も再生・音量操作をしていない)ときは false。
   */
  readBands(out: Float32Array): boolean {
    if (!this.analyser || !this.ctx || !this.freqBuf) return false
    this.analyser.getFloatFrequencyData(this.freqBuf)
    bandLevels(this.freqBuf, this.ctx.sampleRate, this.analyser.fftSize, out)
    return true
  }

  /**
   * 再生位置(曲頭からの秒)でのPA出力の帯域レベルを out に書く(全体の大きさは耳に届く出力にそろえてあり、再生音量も反映する)。
   * 加工後を聴いていないとき、または帯域レベルが無いときは false(原音にはPAが掛かっていないため)。
   */
  readPA(out: Float32Array): boolean {
    if (this.mode !== 'processed' || !this.pa) return false
    // 耳の位置のアナライザーは音量(GainNode)の後ろなので、PA出力にも同じ音量を掛ける(ミュート中は無音)
    seriesAt(this.pa, this.position, out, this.muted ? -200 : this.volumeDb)
    return true
  }

  setVolume(db: number) {
    this.volumeDb = Math.min(Math.max(db, VOLUME_MIN_DB), VOLUME_MAX_DB)
    this.muted = false
    try {
      localStorage.setItem(VOLUME_KEY, String(this.volumeDb))
    } catch {
      // 保存できなくても動作には影響しない
    }
    this.ensureGraph()
    this.applyVolume()
  }

  toggleMute() {
    this.muted = !this.muted
    this.ensureGraph()
    this.applyVolume()
  }

  /** 新しい音源を読み込む。keepPosition なら今の再生位置から続ける。 */
  async load(kind: Mode, url: string, keepPosition: boolean) {
    // <audio> に URL を直接渡さず、一度blobにする(WebViewのRange対応に依存せずシークできる)
    const res = await fetch(url)
    if (!res.ok) throw new Error(`プレビューを取得できません (${res.status})`)
    const blob = URL.createObjectURL(await res.blob())
    const old = this.blobs[kind]
    this.blobs[kind] = blob
    this.loaded[kind] = true
    if (this.mode === kind) await this.swap(blob, keepPosition)
    if (old) setTimeout(() => URL.revokeObjectURL(old), 5000)
  }

  private async swap(src: string, keepPosition: boolean) {
    const t = keepPosition ? this.audio.currentTime : 0
    const wasPlaying = !this.audio.paused
    this.audio.src = src
    // 読み込み完了を待つ。バックグラウンドのタブなどで読み込みが進まなくても、処理を止めないよう上限を付ける
    await new Promise<void>((resolve) => {
      const done = () => {
        clearTimeout(timer)
        resolve()
      }
      const timer = setTimeout(done, 3000)
      this.audio.addEventListener('loadedmetadata', done, { once: true })
    })
    if (Number.isFinite(this.audio.duration)) this.duration = this.audio.duration
    if (t > 0 && t < this.audio.duration) this.audio.currentTime = t
    this.position = this.audio.currentTime
    if (wasPlaying) await this.audio.play().catch(() => {})
  }

  async setMode(mode: Mode) {
    if (mode === this.mode) return
    this.mode = mode
    const src = this.blobs[mode]
    if (src) await this.swap(src, true)
  }

  async toggle() {
    if (this.audio.paused) {
      if (!this.audio.src) return
      this.ensureGraph()
      await this.ctx?.resume()
      await this.audio.play().catch(() => {})
    } else {
      this.audio.pause()
    }
  }

  seek(sec: number) {
    if (this.audio.src) this.audio.currentTime = sec
    this.position = sec
  }
}

export const player = new Player()
